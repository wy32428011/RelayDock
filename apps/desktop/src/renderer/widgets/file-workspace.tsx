import { useState } from 'react';
import { ArrowUp, Download, File, FileCode2, Folder, FolderOpen, Link2, Pencil, Plus, RefreshCw, Trash2, Upload } from 'lucide-react';
import type { Bridge, FileEntry, Host, Methods } from '@relaydock/protocol';
import { RemoteTextEditor } from './editor';
import { Button, Card, Empty, Field, Notice, PaneHeading, faultText, formatBytes, formatDate, remoteTarget, requireOk, useAction, useConfirmation } from './ui';
import { joinRemotePath, parentRemotePath, validLeaf } from './text';

type Directory = Methods['files.list'][1];
type Transfer = NonNullable<Methods['dialog.pick'][1]> & { directory: boolean };
const MAX_UPLOADS = 12;
const linuxConfigPaths = [
  '/etc/traefik/traefik.yml',
  '/etc/traefik/dynamic.yml',
  '/usr/local/etc/traefik/traefik.yml',
  '/usr/local/etc/traefik/dynamic.yml',
  '/etc/elasticsearch/elasticsearch.yml',
  '/etc/elasticsearch/jvm.options',
  '/opt/elasticsearch/config/elasticsearch.yml',
  '/opt/elasticsearch/config/jvm.options',
];
const windowsConfigPaths = [
  'C:/Program Files/Elastic/Elasticsearch/config/elasticsearch.yml',
  'C:/Program Files/Elastic/Elasticsearch/config/jvm.options',
  'C:/elasticsearch/config/elasticsearch.yml',
  'C:/elasticsearch/config/jvm.options',
];

export function FileWorkspace({ api, host, mode }: { api: Bridge; host: Host; mode: 'files' | 'scripts' }) {
  const [directory, setDirectory] = useState<Directory>();
  const [path, setPath] = useState('.');
  const [selected, setSelected] = useState<FileEntry>();
  const [folderName, setFolderName] = useState('');
  const [renameName, setRenameName] = useState('');
  const [recursive, setRecursive] = useState(false);
  const [uploads, setUploads] = useState<Transfer[]>([]);
  const [overwrite, setOverwrite] = useState(false);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  const files = directory?.entries ?? [];
  const parent = directory ? parentRemotePath(directory.path) : '.';
  const selectedTextPath = selected && !selected.directory && !selected.link ? selected.path : undefined;

  function clearSelection() { setSelected(undefined); setRenameName(''); setRecursive(false); }
  function applyDirectory(result: Directory) {
    setDirectory(result);
    setPath(result.path);
    setOverwrite(false);
    clearSelection();
  }
  function browse(target: string) {
    if (!target.trim()) return;
    void action.run(async current => {
      const result = await api.call('files.list', { hostId: host.id, path: target });
      if (current()) applyDirectory(result);
    });
  }
  async function refreshAfterWrite(target: string, current: () => boolean, explanation: string) {
    if (!current()) return;
    action.setNotice({ tone: 'success', text: explanation });
    try {
      const result = await api.call('files.list', { hostId: host.id, path: target });
      if (current()) applyDirectory(result);
    } catch (error) {
      if (current()) {
        clearSelection();
        action.setNotice({ tone: 'info', text: `${explanation} 目录未能刷新：${faultText(error)}` });
      }
    }
  }
  function createFolder() {
    if (!directory || !validLeaf(folderName)) return;
    const dir = directory.path;
    const target = joinRemotePath(dir, folderName);
    void action.run(async current => {
      requireOk(await api.call('files.mkdir', { hostId: host.id, path: target }), '核心未确认目录已创建。');
      if (current()) setFolderName('');
      await refreshAfterWrite(dir, current, `已创建目录 ${target}。`);
    });
  }
  function rename() {
    if (!directory || !selected || !validLeaf(renameName) || renameName === selected.name) return;
    const dir = directory.path;
    const from = selected.path;
    const to = joinRemotePath(dir, renameName);
    confirm({ title: '重命名远程条目？', description: '将修改远端目录中的名称。请核对下面的完整路径。', acceptLabel: '确认重命名', danger: true,
      info: [['主机', remoteTarget(host)], ['原路径', <code>{from}</code>], ['新路径', <code>{to}</code>]], perform: () => {
        void action.run(async current => {
          requireOk(await api.call('files.rename', { hostId: host.id, path: from, to, confirmed: true }), '核心未确认重命名结果。');
          await refreshAfterWrite(dir, current, `已重命名为 ${to}。`);
        });
      } });
  }
  function remove() {
    if (!directory || !selected) return;
    const dir = directory.path;
    const target = selected.path;
    const includeChildren = selected.directory && !selected.link && recursive;
    confirm({ title: includeChildren ? '递归删除这个远程目录？' : '删除这个远程条目？',
      description: includeChildren ? '目录中的文件与子目录也在删除范围内。此操作不会移到回收站。' : selected.directory && !selected.link ? '只请求删除空目录，不删除其中的文件。' : '此操作不会移到回收站。',
      acceptLabel: includeChildren ? '确认递归删除' : '确认删除', danger: true,
      info: [['主机', remoteTarget(host)], ['路径', <code>{target}</code>]], perform: () => {
        void action.run(async current => {
          requireOk(await api.call('files.delete', { hostId: host.id, path: target, recursive: includeChildren, confirmed: true }), '核心未确认删除结果。');
          await refreshAfterWrite(dir, current, `已删除 ${target}。`);
        });
      } });
  }
  function download() {
    if (!selected || selected.directory || selected.link) return;
    const target = selected.path;
    void action.run(async current => {
      const destination = await api.call('dialog.pick', { purpose: 'download' });
      if (!destination || !current()) return;
      requireOk(await api.call('files.download', { hostId: host.id, path: target, token: destination.token }), '核心未确认下载结果。');
      if (current()) action.setNotice({ tone: 'success', text: `已将 ${target} 保存至所选位置：${destination.name}` });
    });
  }
  function pickUpload(asDirectory: boolean) {
    if (uploads.length >= MAX_UPLOADS) return;
    void action.run(async current => {
      const picked = await api.call('dialog.pick', { purpose: asDirectory ? 'uploadDirectory' : 'upload' });
      if (!picked || !current()) return;
      if (!validLeaf(picked.name)) throw new Error('选中来源的名称不能作为远端条目名称。');
      if (uploads.some(item => item.name === picked.name)) throw new Error('待上传列表中已有同名条目。');
      setUploads(old => [...old, { ...picked, directory: asDirectory }]);
    });
  }
  function upload() {
    if (!directory || !uploads.length) return;
    const dir = directory.path;
    const captured = uploads.map(item => ({ ...item, path: joinRemotePath(dir, item.name) }));
    const allowOverwrite = overwrite;
    confirm({ title: allowOverwrite ? '上传并允许覆盖？' : '上传到所选远端目录？',
      description: `${allowOverwrite ? '同名远端文件可能被替换。' : '不会主动覆盖同名文件。'}队列依次处理，出错或切换目标时未开始的项不会继续，已提交的传输不会自动撤回。`,
      acceptLabel: allowOverwrite ? '确认上传并允许覆盖' : '确认上传', danger: allowOverwrite,
      info: [['主机', remoteTarget(host)], ['目标目录', <code>{dir}</code>], ['来源与目标', <ul className="rd-w-inline-list">{captured.map(item => <li key={item.token}><code>{item.path}</code>{item.directory ? '（目录）' : ''}</li>)}</ul>]],
      perform: () => {
        void action.run(async current => {
          const done = new Set<string>();
          try {
            for (const item of captured) {
              if (!current()) return;
              requireOk(await api.call('files.upload', { hostId: host.id, path: item.path, token: item.token, overwrite: allowOverwrite, confirmed: true }), '核心未确认上传结果。');
              done.add(item.token);
            }
          } catch (error) {
            if (current()) {
              setUploads([]);
              if (done.size) {
                try {
                  const result = await api.call('files.list', { hostId: host.id, path: dir });
                  if (current()) applyDirectory(result);
                } catch { /* Retain the upload failure as the primary diagnosis. */ }
              }
            }
            throw new Error(`已确认上传 ${done.size}/${captured.length} 项；后续项未继续。请核对远端并重新选择未完成的来源。${faultText(error)}`);
          }
          if (current()) { setUploads([]); setOverwrite(false); }
          await refreshAfterWrite(dir, current, `已上传 ${done.size} 项。`);
        });
      } });
  }

  const configPaths = host.os.toLowerCase().startsWith('windows') ? windowsConfigPaths : linuxConfigPaths;
  const editor = <RemoteTextEditor api={api} host={host} suggestedPath={selectedTextPath} {...(mode === 'scripts' && { configPaths })} />;
  return <>
    <PaneHeading title={mode === 'scripts' ? '脚本与配置' : '远程文件'}
      description={mode === 'scripts' ? '在同一目标浏览脚本和配置，检查差异后按版本摘要写回。不会执行编辑的内容。' : '浏览远端目录、传输文件并核对变更。SFTP 不可用不代表 SSH 终端不可用。'}
      icon={mode === 'scripts' ? <FileCode2 size={25} /> : <FolderOpen size={25} />} host={host} />
    {mode === 'scripts' && editor}
    <Card title="远程目录" subtitle="输入绝对路径，或用“.”打开远端登录目录。" actions={directory && <Button disabled={action.busy} onClick={() => browse(directory.path)}><RefreshCw aria-hidden="true" size={16} />刷新目录</Button>}>
      <form className="rd-w-command-row" onSubmit={event => { event.preventDefault(); browse(path); }}>
        <Field label="目录路径"><input className="rd-w-mono" spellCheck={false} value={path} onChange={event => setPath(event.target.value)} required disabled={action.busy} /></Field>
        <div className="rd-w-actions">
          <Button onClick={() => browse(parent)} disabled={!directory || parent === directory.path || action.busy}><ArrowUp aria-hidden="true" size={16} />上一级</Button>
          <Button type="submit" variant="primary" disabled={!path.trim() || action.busy}><FolderOpen aria-hidden="true" size={16} />打开路径</Button>
        </div>
      </form>
      <Notice value={action.notice} />
      {action.busy && <p className="rd-w-subtle" role="status">正在处理文件操作…</p>}
      {!directory ? <Empty title="尚未打开目录" icon={<FolderOpen size={23} />} description="打开路径后查看服务器返回的目录、文件和链接。" /> : <>
        <div className="rd-w-toolbar"><code>{directory.path}</code><span className="rd-w-chip">{files.length} 项</span></div>
        {files.length ? <div className="rd-w-table-wrap"><table className="rd-w-table">
          <caption className="rd-w-sr">远程目录条目</caption>
          <thead><tr><th scope="col">名称</th><th scope="col">类型</th><th scope="col">大小</th><th scope="col">修改时间</th><th scope="col">操作</th></tr></thead>
          <tbody>{files.map(entry => {
            const Icon = entry.link ? Link2 : entry.directory ? Folder : File;
            return <tr key={entry.path} className={entry.path === selected?.path ? 'rd-w-row-selected' : undefined}>
              <th scope="row"><span className="rd-w-file-name"><Icon aria-hidden="true" size={16} /><span>{entry.name}</span></span></th>
              <td>{entry.link ? '链接' : entry.directory ? '目录' : '文件'}</td><td>{entry.directory ? '—' : formatBytes(entry.size)}</td><td>{formatDate(entry.modifiedAt)}</td>
              <td><div className="rd-w-actions">
                {entry.directory && !entry.link && <Button variant="text" disabled={action.busy} aria-label={`打开目录 ${entry.name}`} onClick={() => browse(entry.path)}>打开</Button>}
                <Button variant="text" aria-label={`选择 ${entry.name}`} aria-pressed={entry.path === selected?.path} disabled={action.busy}
                  onClick={() => { setSelected(entry); setRenameName(entry.name); setRecursive(false); }}>选择</Button>
              </div></td></tr>;
          })}</tbody>
        </table></div> : <Empty title="目录为空" description="可创建目录或上传所选文件。" />}
        {selected && <section className="rd-w-subcard" aria-label="所选条目的操作">
          <div className="rd-w-toolbar"><strong>所选条目</strong><code>{selected.path}</code></div>
          <form className="rd-w-command-row" onSubmit={event => { event.preventDefault(); rename(); }}>
            <Field label="新名称"><input spellCheck={false} value={renameName} onChange={event => setRenameName(event.target.value)} required disabled={action.busy} /></Field>
            <Button type="submit" disabled={action.busy || !validLeaf(renameName) || renameName === selected.name}><Pencil aria-hidden="true" size={16} />重命名</Button>
          </form>
          <div className="rd-w-actions">
            {!selected.directory && !selected.link && <Button disabled={action.busy} onClick={download}><Download aria-hidden="true" size={16} />下载文件</Button>}
            {selected.directory && !selected.link && <label className="rd-w-check"><input type="checkbox" checked={recursive} disabled={action.busy} onChange={event => setRecursive(event.target.checked)} />包括子目录和文件</label>}
            <Button variant="danger" disabled={action.busy} onClick={remove}><Trash2 aria-hidden="true" size={16} />{selected.directory && !selected.link ? recursive ? '递归删除' : '删除空目录' : '删除条目'}</Button>
          </div>
        </section>}
        <form className="rd-w-command-row" onSubmit={event => { event.preventDefault(); createFolder(); }}>
          <Field label="新目录名称"><input spellCheck={false} value={folderName} onChange={event => setFolderName(event.target.value)} required disabled={action.busy} /></Field>
          <Button type="submit" disabled={action.busy || !validLeaf(folderName)}><Plus aria-hidden="true" size={16} />创建目录</Button>
        </form>
        <section className="rd-w-subcard" aria-label="上传队列">
          <div className="rd-w-toolbar"><strong>上传队列</strong><span className="rd-w-chip">{uploads.length}/{MAX_UPLOADS}</span></div>
          <p className="rd-w-subtle">继续选择可追加多个来源。确认时会使用当前打开的目录，而不是路径输入框中的未打开路径。</p>
          <div className="rd-w-actions">
            <Button disabled={action.busy || uploads.length >= MAX_UPLOADS} onClick={() => pickUpload(false)}><File aria-hidden="true" size={16} />选择文件</Button>
            <Button disabled={action.busy || uploads.length >= MAX_UPLOADS} onClick={() => pickUpload(true)}><Folder aria-hidden="true" size={16} />选择文件夹</Button>
            {!!uploads.length && <Button disabled={action.busy} onClick={() => { setUploads([]); setOverwrite(false); }}>清空队列</Button>}
          </div>
          {!!uploads.length && <>
            <ul className="rd-w-inline-list">{uploads.map(item => <li key={item.token}>{item.name} {item.directory ? '（目录）' : '（文件）'}</li>)}</ul>
            <div className="rd-w-actions"><label className="rd-w-check"><input type="checkbox" checked={overwrite} disabled={action.busy} onChange={event => setOverwrite(event.target.checked)} />允许覆盖同名文件</label>
              <Button variant="primary" onClick={upload} disabled={action.busy}><Upload aria-hidden="true" size={16} />核对并上传</Button></div>
          </>}
        </section>
      </>}
    </Card>
    {mode === 'files' && editor}
    {dialog}
  </>;
}
