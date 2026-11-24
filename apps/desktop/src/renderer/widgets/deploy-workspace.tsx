import { useCallback, useState } from 'react';
import { Boxes, FileText, Play, Plus, RefreshCw, Save, Square, Trash2, Upload } from 'lucide-react';
import type { Bridge, CustomApp, Host, Methods } from '@relaydock/protocol';
import { RemoteTextEditor } from './editor';
import { Button, Card, DetailList, Empty, Field, Notice, PaneHeading, TargetRequired, canConnect, remoteTarget, requireOk, useAction, useConfirmation, useLocalQuery } from './ui';
import type { DetailRows } from './ui';

type Action = Methods['custom.run'][0]['action'];
type UploadPurpose = 'upload' | 'uploadDirectory';
type Source = NonNullable<Methods['dialog.pick'][1]>;
const kindLabel: Record<CustomApp['kind'], string> = { SUPPORT: '前后端配套', GENERIC: '通用服务', CUSTOM: '自定义部署' };
const kindHint: Record<CustomApp['kind'], string> = {
  SUPPORT: '独立维护后端和前端目标；状态、启动和停止以已保存的命令定义为准。',
  GENERIC: '将文件或目录上传到服务目录，再按需检查、启动、停止或重启。',
  CUSTOM: '可仅上传、手动启动，或显式执行“上传并启动”。',
};
const actionLabel: Record<Action, string> = { start: '启动', stop: '停止', restart: '重启', status: '检查状态', upload: '上传', uploadFrontend: '上传前端', uploadAndStart: '上传并启动' };
const emptyApp = (): CustomApp => ({ id: crypto.randomUUID(), name: '', description: '', kind: 'GENERIC', directory: '', frontendDirectory: '', configPath: '', startCommand: '', stopCommand: '', statusCommand: '', logsPath: '', enabled: true });
const selectedCommands = (app: CustomApp, action: Action): DetailRows => {
  switch (action) {
    case 'start': case 'uploadAndStart': return [['启动命令', <code>{app.startCommand}</code>]];
    case 'stop': return [['停止命令', <code>{app.stopCommand}</code>]];
    case 'status': return [['状态命令', <code>{app.statusCommand}</code>]];
    case 'restart': return [['停止命令', <code>{app.stopCommand}</code>], ['启动命令', <code>{app.startCommand}</code>]];
    default: return [];
  }
};

export function DeployWorkspace({ api, host }: { api: Bridge; host?: Host | undefined }) {
  const load = useCallback(() => api.call('custom.list', {}), [api]);
  const query = useLocalQuery(load);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  const [saved, setSaved] = useState<CustomApp>();
  const [draft, setDraft] = useState<CustomApp>();
  const [showText, setShowText] = useState<'config' | 'log'>();
  const [result, setResult] = useState<{ label: string; target: string; output: string }>();
  const dirty = Boolean(draft && (!saved || JSON.stringify(draft) !== JSON.stringify(saved)));

  function select(next: CustomApp, stored: boolean) {
    const adopt = () => { setSaved(stored ? next : undefined); setDraft(next); setShowText(undefined); setResult(undefined); action.setNotice(undefined); };
    if (dirty) {
      confirm({ title: '放弃未保存的应用定义？', description: '这些修改尚未写入本地工作台，也不会影响远端。', acceptLabel: '放弃并切换', danger: true, perform: adopt });
    } else adopt();
  }
  function change<K extends keyof CustomApp>(key: K, value: CustomApp[K]) {
    setShowText(undefined);
    setDraft(old => old && ({ ...old, [key]: value }));
  }
  function save() {
    if (!draft?.name.trim() || !dirty) return;
    const requested = { ...draft, name: draft.name.trim() };
    void action.run(async current => {
      const app = await api.call('custom.save', requested);
      if (!current()) return;
      setSaved(app); setDraft(app); setResult(undefined); query.refresh();
      action.setNotice({ tone: 'success', text: `已保存“${app.name}”的本地定义，未执行远端动作。` });
    });
  }
  function remove() {
    if (!saved || dirty) return;
    const app = saved;
    confirm({ title: '删除本地应用定义？', description: '只移除工作台中的定义；不会卸载远端应用、停止服务或删除远端文件。',
      acceptLabel: '删除定义', danger: true, info: [['应用', app.name], ['定义 ID', <code>{app.id}</code>]], perform: () => {
        void action.run(async current => {
          requireOk(await api.call('custom.delete', { id: app.id }), '核心未确认应用定义已删除。');
          if (!current()) return;
          setSaved(undefined); setDraft(undefined); setShowText(undefined); setResult(undefined); query.refresh();
          action.setNotice({ tone: 'success', text: `已删除本地定义“${app.name}”。` });
        });
      } });
  }

  function requestRun(command: Action, purpose?: UploadPurpose) {
    if (!saved?.enabled || dirty || !canConnect(host) || action.busy) return;
    const app = saved;
    const targetHost = host;
    const directory = command === 'uploadFrontend' ? app.frontendDirectory : app.directory;
    const label = actionLabel[command];
    const preview = (source?: Source) => {
      confirm({ title: `确认${label}？`, description: purpose ? '上传会写入所示远端位置，可能替换已有文件。失败时不要直接重试；先核对结果。' : '这会在目标主机执行已保存的运维命令。请检查完整命令与目标，系统不会自动重试。',
        acceptLabel: `确认${label}`, danger: Boolean(purpose) || command !== 'status', info: [
          ['目标主机', remoteTarget(targetHost)], ['应用', app.name],
          ...(source ? [['上传目录', <code>{directory}</code>], ['本机来源', `${source.name}${purpose === 'uploadDirectory' ? '（目录）' : '（文件）'}`]] as const : []),
          ...(command !== 'upload' && command !== 'uploadFrontend' ? [['命令目录', '远端 Shell 的默认目录；命令若需要指定目录，请在定义中明确写出。']] as const : []),
          ...selectedCommands(app, command),
        ], perform: () => {
          void action.run(async current => {
            setResult(undefined);
            const response = await api.call('custom.run', { hostId: targetHost.id, appId: app.id, action: command, confirmed: true, ...(source ? { token: source.token } : {}) });
            if (!current()) return;
            setResult({ label, target: remoteTarget(targetHost), output: response.output });
            action.setNotice({ tone: 'info', text: '核心已返回动作结果。服务状态请以输出或后续独立检查为准。' });
          });
        } });
    };
    if (purpose) {
      void action.run(async current => {
        const source = await api.call('dialog.pick', { purpose });
        if (source && current()) preview(source);
      });
    } else preview();
  }

  const endpoint = canConnect(host) ? host : undefined;
  const commandDisabled = !endpoint || !saved?.enabled || dirty || action.busy;
  return <>
    <PaneHeading title="自定义部署" description="分开保存应用定义和执行部署。每个远端动作都会先显示目标、来源和将运行的命令。" icon={<Boxes size={25} />} host={host} />
    <div className="rd-w-split">
      <Card title="应用定义" subtitle="保存在本机" actions={<Button variant="primary" disabled={action.busy} onClick={() => select(emptyApp(), false)}><Plus aria-hidden="true" size={16} />新建</Button>}>
        {query.pending && <p className="rd-w-subtle" role="status">正在读取定义…</p>}
        <Notice value={query.error ? { tone: 'error', text: query.error } : undefined} />
        {query.error && <Button onClick={query.refresh}>重新读取列表</Button>}
        {!!query.data?.length && <div className="rd-w-choice-list" aria-label="选择应用定义">{query.data.map(app => <Button key={app.id} aria-pressed={saved?.id === app.id} disabled={action.busy} onClick={() => select(app, true)}>
          <span>{app.name}</span><span className="rd-w-chip">{app.enabled ? kindLabel[app.kind] : '已停用'}</span>
        </Button>)}</div>}
        {query.data?.length === 0 && <Empty title="还没有应用定义" description="新建后填写目标路径和需要的命令。" />}
      </Card>
      <Card title={saved?.name ?? (draft ? '新建应用定义' : '部署配置')} subtitle={draft ? kindHint[draft.kind] : '选择左侧定义或手动新建。'}>
        {!draft ? <Empty title="请先选择定义" icon={<Boxes size={22} />} /> : <form className="rd-w-stack" onSubmit={event => { event.preventDefault(); save(); }}>
          <div className="rd-w-form-grid">
            <Field label="应用名称"><input value={draft.name} onChange={event => change('name', event.target.value)} disabled={action.busy} required maxLength={256} /></Field>
            <Field label="类型"><select value={draft.kind} onChange={event => change('kind', event.target.value as CustomApp['kind'])} disabled={action.busy}>
              {Object.entries(kindLabel).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
            </select></Field>
            <Field label="说明" wide><input value={draft.description} onChange={event => change('description', event.target.value)} disabled={action.busy} maxLength={2048} /></Field>
            <Field label={draft.kind === 'SUPPORT' ? '后端目录' : '部署目录'} hint="远端 SFTP 路径；上传时必填。"><input value={draft.directory} onChange={event => change('directory', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={2048} /></Field>
            {draft.kind === 'SUPPORT' && <Field label="前端目录"><input value={draft.frontendDirectory} onChange={event => change('frontendDirectory', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={2048} /></Field>}
            <Field label="配置文件路径"><input value={draft.configPath} onChange={event => change('configPath', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={2048} /></Field>
            <Field label="日志路径" hint="查看文本时需要具体日志文件，不能是目录。"><input value={draft.logsPath} onChange={event => change('logsPath', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={2048} /></Field>
            <Field label="启动命令" wide hint="不要在命令中保存密码或令牌。只在明确确认后执行。"><textarea className="rd-w-mono" rows={2} value={draft.startCommand} onChange={event => change('startCommand', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={8192} /></Field>
            {draft.kind !== 'CUSTOM' && <>
              <Field label="停止命令" wide><textarea className="rd-w-mono" rows={2} value={draft.stopCommand} onChange={event => change('stopCommand', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={8192} /></Field>
              <Field label="状态命令" wide><textarea className="rd-w-mono" rows={2} value={draft.statusCommand} onChange={event => change('statusCommand', event.target.value)} disabled={action.busy} spellCheck={false} maxLength={8192} /></Field>
            </>}
          </div>
          <div className="rd-w-actions">
            <label className="rd-w-check"><input type="checkbox" checked={draft.enabled} onChange={event => change('enabled', event.target.checked)} disabled={action.busy} />启用此定义</label>
            <Button type="submit" variant="primary" disabled={action.busy || !dirty || !draft.name.trim()}><Save aria-hidden="true" size={16} />保存定义</Button>
            {saved && <Button variant="danger" disabled={action.busy || dirty} onClick={remove}><Trash2 aria-hidden="true" size={16} />删除定义</Button>}
          </div>
        </form>}
      </Card>
    </div>
    <Notice value={action.notice} />
    {action.busy && <p className="rd-w-subtle" role="status">正在处理部署请求；请等待核心确认，不要重复执行…</p>}
    <Card title="远端动作" subtitle="只使用已保存且启用的定义；保存配置不会触发部署。">
      {!endpoint ? <TargetRequired host={host} /> : !saved ? <Empty title="还没有选中的已保存应用" description="先保存或选择应用定义。" /> : dirty ? <Empty title="先保存当前改动" description="有未保存字段时，不用旧命令或旧目录执行动作。" /> : !saved.enabled ? <Empty title="此应用定义已停用" description="可从本地配置重新启用。" /> : <>
        <DetailList items={[
          ['目标主机', remoteTarget(endpoint)], ['应用 / 类型', `${saved.name} · ${kindLabel[saved.kind]}`],
          ['部署目录', <code>{saved.directory || '未设置'}</code>],
          ...(saved.kind === 'SUPPORT' ? [['前端目录', <code>{saved.frontendDirectory || '未设置'}</code>] as const] : []),
        ]} />
        <div className="rd-w-actions">
          <Button disabled={commandDisabled || !saved.directory.trim()} onClick={() => requestRun('upload', 'upload')}><Upload aria-hidden="true" size={16} />{saved.kind === 'SUPPORT' ? '选择后端文件' : '选择上传文件'}</Button>
          <Button disabled={commandDisabled || !saved.directory.trim()} onClick={() => requestRun('upload', 'uploadDirectory')}><Upload aria-hidden="true" size={16} />{saved.kind === 'SUPPORT' ? '选择后端目录' : '选择上传目录'}</Button>
          {saved.kind === 'SUPPORT' && <Button disabled={commandDisabled || !saved.frontendDirectory.trim()} onClick={() => requestRun('uploadFrontend', 'uploadDirectory')}><Upload aria-hidden="true" size={16} />选择前端目录</Button>}
          {saved.kind === 'CUSTOM' && <>
            <Button variant="primary" disabled={commandDisabled || !saved.directory.trim() || !saved.startCommand.trim()} onClick={() => requestRun('uploadAndStart', 'upload')}>上传文件并启动</Button>
            <Button variant="primary" disabled={commandDisabled || !saved.directory.trim() || !saved.startCommand.trim()} onClick={() => requestRun('uploadAndStart', 'uploadDirectory')}>上传目录并启动</Button>
          </>}
        </div>
        <div className="rd-w-actions">
          <Button disabled={commandDisabled || !saved.startCommand.trim()} onClick={() => requestRun('start')}><Play aria-hidden="true" size={16} />启动</Button>
          {saved.kind !== 'CUSTOM' && <>
            <Button disabled={commandDisabled || !saved.stopCommand.trim()} onClick={() => requestRun('stop')}><Square aria-hidden="true" size={16} />停止</Button>
            <Button disabled={commandDisabled || !saved.startCommand.trim() || !saved.stopCommand.trim()} onClick={() => requestRun('restart')}><RefreshCw aria-hidden="true" size={16} />重启</Button>
            <Button disabled={commandDisabled || !saved.statusCommand.trim()} onClick={() => requestRun('status')}>检查状态</Button>
          </>}
          <Button disabled={action.busy || !saved.configPath.trim()} aria-pressed={showText === 'config'} onClick={() => setShowText('config')}><FileText aria-hidden="true" size={16} />编辑配置</Button>
          <Button disabled={action.busy || !saved.logsPath.trim()} aria-pressed={showText === 'log'} onClick={() => setShowText('log')}>查看日志文本</Button>
        </div>
        <p className="rd-w-subtle">未配置的路径或命令保持不可执行；上传与启动是独立动作，除非主动选择组合动作。</p>
      </>}
    </Card>
    {!dirty && showText && endpoint && saved && <RemoteTextEditor key={`${saved.id}:${showText}`} api={api} host={endpoint}
      suggestedPath={showText === 'config' ? saved.configPath : saved.logsPath} readOnly={showText === 'log'} />}
    {result && <Card title={`${result.label} · 核心输出`} subtitle={result.target}><pre className="rd-w-log" tabIndex={0} aria-label="部署动作输出">{result.output || '核心未返回文本输出。'}</pre></Card>}
    {dialog}
  </>;
}
