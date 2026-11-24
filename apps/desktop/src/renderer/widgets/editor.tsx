import { useState } from 'react';
import { Braces, FileText, Save } from 'lucide-react';
import { MAX_FRAME } from '@relaydock/protocol';
import type { Bridge, Host } from '@relaydock/protocol';
import { Button, Card, Empty, Field, Notice, remoteTarget, useAction, useConfirmation } from './ui';
import { changedLines, normalizeText, restoreNewlines } from './text';

interface Snapshot { path: string; raw: string; original: string; digest: string }
const encoder = new TextEncoder();
const MAX_TEXT_BYTES = 256 * 1024;

function DiffPreview({ before, after }: { before: string; after: string }) {
  const previous = before.split('\n');
  const next = after.split('\n');
  const { prefix, suffix } = changedLines(previous, next);
  const removed = previous.length - prefix - suffix;
  const added = next.length - prefix - suffix;
  const clipped = (line: string) => line.length > 240 ? `${line.slice(0, 240)}…` : line;
  return <section className="rd-w-diff" aria-label="写入前差异">
    <p>变化范围自第 {prefix + 1} 行起 · 原文 {removed} 行 / 新内容 {added} 行</p>
    <div>
      <pre className="rd-w-diff-before" aria-label="原文片段">{removed ? previous.slice(prefix, prefix + Math.min(removed, 9)).map(line => `− ${clipped(line)}`).join('\n') : '无移除行'}{removed > 9 && '\n…'}</pre>
      <pre className="rd-w-diff-after" aria-label="修改片段">{added ? next.slice(prefix, prefix + Math.min(added, 9)).map(line => `+ ${clipped(line)}`).join('\n') : '无新增行'}{added > 9 && '\n…'}</pre>
    </div>
  </section>;
}

export function RemoteTextEditor({ api, host, suggestedPath, readOnly = false, configPaths = [] }: {
  api: Bridge; host: Host; suggestedPath?: string | undefined; readOnly?: boolean; configPaths?: readonly string[];
}) {
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  const [path, setPath] = useState(suggestedPath ?? '');
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [draft, setDraft] = useState('');
  const dirty = Boolean(snapshot && draft !== snapshot.original);

  function read(target: string) {
    void action.run(async current => {
      const result = await api.call('files.read', { hostId: host.id, path: target });
      if (!current()) return;
      const original = normalizeText(result.content);
      setSnapshot({ path: target, raw: result.content, original, digest: result.digest });
      setPath(target);
      setDraft(original);
    });
  }

  function requestRead(target: string) {
    if (!target.trim() || action.busy) return;
    if (!dirty || !snapshot) { read(target); return; }
    confirm({ title: '放弃当前未保存内容？', description: '读取另一个版本会替换编辑区草稿，不会写入远端。需要保留草稿时请先复制。',
      acceptLabel: '放弃并读取', danger: true, info: [['待放弃草稿', <code>{snapshot.path}</code>], ['将读取', <code>{target}</code>]], perform: () => read(target) });
  }

  function requestSave() {
    if (action.busy || readOnly || !snapshot?.digest || !dirty) return;
    const content = restoreNewlines(snapshot.raw, draft);
    const request = { hostId: host.id, path: snapshot.path, content, expectedDigest: snapshot.digest, confirmed: true };
    if (content.includes('\0') || encoder.encode(content).byteLength > MAX_TEXT_BYTES || encoder.encode(JSON.stringify(request)).byteLength > MAX_FRAME - 1024) {
      action.setNotice({ tone: 'error', text: '编辑器只支持不超过 256 KiB 的 UTF-8 文本，且不含 NUL 字节。可从远程文件页面上传其他文件，并核对覆盖目标。' });
      return;
    }
    confirm({ title: '写入远程文本？', description: '核心在保存前核对最初读取的摘要。若远端发生变化，将拒绝覆盖并保留当前草稿。',
      acceptLabel: '确认写入', danger: true, info: [['目标主机', remoteTarget(host)], ['文件', <code>{snapshot.path}</code>]],
      content: <DiffPreview before={snapshot.original} after={draft} />, perform: () => {
        void action.run(async current => {
          const result = await api.call('files.write', request);
          if (!current()) return;
          if (!result.digest) throw new Error('核心未返回写入后的摘要；请重新读取远端文件核对结果。');
          setSnapshot({ path: snapshot.path, raw: content, original: draft, digest: result.digest });
          action.setNotice({ tone: 'success', text: `已保存 ${snapshot.path}` });
        });
      } });
  }

  return <Card title={readOnly ? '查看远程文本' : '远程文本编辑器'} subtitle={readOnly ? '按路径读取文本；此窗口不会保存修改。' : '直接编辑脚本和配置的原始文本；保留注释、空行与未修改的字段。'}>
    {configPaths.length > 0 && <Field label="常用配置路径">
      <select value={configPaths.includes(path) ? path : ''} disabled={action.busy} onChange={event => {
        const selected = event.target.value;
        if (selected) setPath(selected);
      }}>
        <option value="">输入路径或从下方选择</option>
        {configPaths.map(candidate => <option key={candidate} value={candidate}>{candidate}</option>)}
      </select>
    </Field>}
    <form className="rd-w-command-row" onSubmit={event => { event.preventDefault(); requestRead(path); }}>
      <Field label="远程文件路径"><input className="rd-w-mono" value={path} onChange={event => setPath(event.target.value)} required disabled={action.busy} spellCheck={false} /></Field>
      <div className="rd-w-actions">
        {suggestedPath && suggestedPath !== path && <Button disabled={action.busy} onClick={() => setPath(suggestedPath)}>使用选中文件路径</Button>}
        <Button type="submit" disabled={action.busy || !path.trim()}><FileText aria-hidden="true" size={16} />读取文本</Button>
      </div>
    </form>
    {action.busy && <p className="rd-w-subtle" role="status">正在与远端交换文本…</p>}
    <Notice value={action.notice} />
    {snapshot ? <>
      <div className="rd-w-editor-top"><div><code>{snapshot.path}</code><span className={`rd-w-chip${dirty ? ' rd-w-chip--warning' : ''}`}>{dirty ? '有未保存修改' : readOnly ? '只读' : '原始快照'}</span></div>
        {!readOnly && <Button variant="primary" disabled={action.busy || !dirty || !snapshot.digest} onClick={requestSave}><Save aria-hidden="true" size={16} />检查并保存</Button>}
      </div>
      <textarea className="rd-w-editor" aria-label={readOnly ? '远程文本内容' : '远程文本草稿'} value={draft} onChange={event => setDraft(event.target.value)} spellCheck={false} readOnly={action.busy || readOnly || !snapshot.digest} rows={14} wrap="off" />
      {!snapshot.digest && !readOnly && <Notice value={{ tone: 'info', text: '读取结果没有版本摘要，无法安全编辑。' }} />}
      {dirty && <p className="rd-w-subtle">草稿尚未下发；切换主机或工作区前请保存或复制需保留的内容。</p>}
    </> : <Empty title="等待打开文件" icon={<Braces size={21} />} description="填写或从目录选择路径，再读取远程文本。" />}
    {dialog}
  </Card>;
}
