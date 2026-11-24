import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ArrowLeft, ArrowRight, Download, FileClock, ListFilter, RefreshCw, Trash2, X } from 'lucide-react';
import { activeStates } from '@relaydock/protocol';
import type { AppEntry, Bridge, Host, Kind, Methods, Task, TaskLog, TaskState } from '@relaydock/protocol';
import { Button, Card, DetailList, Empty, Field, Notice, PaneHeading, exportText, faultText, formatDate, requireOk, useAction, useConfirmation, useLocalQuery } from './ui';
import { kindLabel as kindLabels, taskStageLabel } from '../core';
import { redactExport } from './text';

const PAGE = 20;
const MAX_LOCAL_LOGS = 1000;
const legacyKindLabels = new Map([
  ['tasks', '任务'],
  ['install_history', '安装记录'],
  ['logs', '日志'],
]);
const stateLabels: Record<TaskState, string> = {
  queued: '排队中', preflight: '预检中', running: '执行中', verifying: '复查中', cancelling: '正在取消',
  succeeded: '已完成', succeeded_with_warning: '完成，有告警', failed: '失败', cancelled: '已取消', interrupted: '中断，待复查',
};

function StateChip({ state }: { state: TaskState }) {
  const style = state === 'succeeded' ? 'good' : state === 'failed' ? 'error' : state === 'succeeded_with_warning' || state === 'interrupted' || state === 'cancelling' ? 'warning' : activeStates.includes(state) ? 'accent' : '';
  return <span className={`rd-w-chip${style ? ` rd-w-chip--${style}` : ''}`}>{stateLabels[state]}</span>;
}

function Pager({ page, next, busy, move }: { page: number; next: boolean; busy: boolean; move: (page: number) => void }) {
  return <nav className="rd-w-page-nav" aria-label="历史分页">
    <Button disabled={!page || busy} onClick={() => move(page - 1)}><ArrowLeft aria-hidden="true" size={16} />上一页</Button>
    <span>{`第 ${page + 1} 页`}</span>
    <Button disabled={!next || busy} onClick={() => move(page + 1)}>下一页<ArrowRight aria-hidden="true" size={16} /></Button>
  </nav>;
}

function taskText(task: Task) {
  return [task.createdAt, task.hostId, task.appId, task.version || '—', kindLabels[task.kind], stateLabels[task.state], task.id, task.message || '—'].join(' | ');
}

function OperationHistory({ api, host, tasks, catalog }: { api: Bridge; host?: Host | undefined; tasks: Task[]; catalog: AppEntry[] }) {
  const [page, setPage] = useState(0);
  const [onlyHost, setOnlyHost] = useState(Boolean(host));
  const [appId, setAppId] = useState('');
  const [kind, setKind] = useState<Kind | ''>('');
  const [state, setState] = useState<TaskState | ''>('');
  const [focused, setFocused] = useState<Task>();
  const hostId = onlyHost ? host?.id : undefined;
  const load = useCallback(() => api.call('operation.list', { limit: PAGE + 1, offset: page * PAGE,
    ...(hostId ? { hostId } : {}), ...(appId ? { appId } : {}), ...(kind ? { kind } : {}), ...(state ? { state } : {}),
  }), [api, page, hostId, appId, kind, state]);
  const query = useLocalQuery(load);
  const action = useAction();
  const live = new Map(tasks.map(task => [task.id, task]));
  const labels = new Map(catalog.map(app => [app.id, app.label]));
  const rows = (query.data ?? []).slice(0, PAGE).map(task => {
    const fresh = live.get(task.id);
    return fresh && fresh.seq > task.seq ? fresh : task;
  }).filter(task => !state || task.state === state);

  function go(pageNumber: number) { setPage(pageNumber); setFocused(undefined); }
  function exportPage() {
    const header = 'RelayDock · 任务历史\n时间 | 主机 ID | 应用 | 版本 | 操作 | 状态 | 任务 ID | 摘要';
    const text = redactExport(`${header}\n${rows.map(taskText).join('\n')}\n`);
    void action.run(async current => {
      if (await exportText(api, text, current) && current()) action.setNotice({ tone: 'success', text: '已导出本页任务摘要。' });
    });
  }
  return <>
    <Card title="任务记录" subtitle="分页从本地核心读取；打开单项后才加载其日志。" actions={<>
      <Button disabled={query.pending} onClick={query.refresh}><RefreshCw aria-hidden="true" size={16} />刷新</Button>
      <Button disabled={!rows.length || action.busy || query.pending} onClick={exportPage}><Download aria-hidden="true" size={16} />导出本页</Button>
    </>}>
      <div className="rd-w-form-grid" aria-label="筛选任务">
        <Field label="主机范围"><select aria-label="主机范围" value={host && onlyHost ? 'selected' : 'all'} onChange={event => { setOnlyHost(event.target.value === 'selected'); go(0); }}>
          <option value="all">全部主机</option>{host && <option value="selected">{host.name || host.address}</option>}
        </select></Field>
        <Field label="应用"><select value={appId} onChange={event => { setAppId(event.target.value); go(0); }}><option value="">所有应用</option>{catalog.map(app => <option key={app.id} value={app.id}>{app.label}</option>)}</select></Field>
        <Field label="操作类型"><select value={kind} onChange={event => { setKind(event.target.value as Kind | ''); go(0); }}><option value="">所有操作</option>{Object.entries(kindLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
        <Field label="任务状态"><select value={state} onChange={event => { setState(event.target.value as TaskState | ''); go(0); }}><option value="">所有状态</option>{Object.entries(stateLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
      </div>
      <Notice value={action.notice} />
      <Notice value={query.error ? { tone: 'error', text: query.error } : undefined} />
      {query.pending ? <p className="rd-w-subtle" role="status">正在读取任务记录…</p> : query.data && (
        rows.length ? <div className="rd-w-table-wrap"><table className="rd-w-table">
          <caption className="rd-w-sr">任务历史</caption><thead><tr><th scope="col">应用 / 版本</th><th scope="col">目标</th><th scope="col">操作</th><th scope="col">状态</th><th scope="col">提交时间</th><th scope="col">明细</th></tr></thead>
          <tbody>{rows.map(task => <tr key={task.id} className={focused?.id === task.id ? 'rd-w-row-selected' : undefined}>
            <th scope="row">{labels.get(task.appId) ?? task.appId}{task.version ? ` · ${task.version}` : ''}</th>
            <td>{task.hostId === host?.id ? host.name || host.address : <code>{task.hostId}</code>}</td><td>{kindLabels[task.kind]}</td><td><StateChip state={task.state} /></td><td>{formatDate(task.createdAt)}</td>
            <td><Button variant="text" aria-label={`查看 ${labels.get(task.appId) ?? task.appId} 任务 ${task.id}`} aria-pressed={focused?.id === task.id} onClick={() => setFocused(task)}>查看</Button></td>
          </tr>)}</tbody>
        </table></div> : <Empty icon={<ListFilter size={21} />} title="本页没有符合条件的任务" description="可修改筛选条件或返回上一页。" />
      )}
      <Pager page={page} next={(query.data?.length ?? 0) > PAGE} busy={query.pending} move={go} />
    </Card>
    {focused && <TaskDetail key={focused.id} api={api} row={focused} appName={labels.get(focused.appId) ?? focused.appId} close={() => setFocused(undefined)} />}
  </>;
}

type TaskDetailResult = Methods['operation.get'][1];
type LogView = { task: Task; logs: TaskLog[]; incomplete: boolean };

function TaskDetail({ api, row, appName, close }: { api: Bridge; row: Task; appName: string; close: () => void }) {
  const taskId = row.id;
  const [snapshot, setSnapshot] = useState<LogView>();
  const [failure, setFailure] = useState<string>();
  const [revision, setRevision] = useState(0);
  const [following, setFollowing] = useState(true);
  const [pending, setPending] = useState(true);
  const logRef = useRef<HTMLPreElement>(null);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  useEffect(() => {
    let current = true;
    let inFlight = false;
    let queued = false;
    let timeout: number | undefined;
    let last: LogView | undefined;
    setPending(true);

    function schedule() {
      if (inFlight) { queued = true; return; }
      if (timeout !== undefined || !current) return;
      timeout = window.setTimeout(() => { timeout = undefined; void refresh(); }, 180);
    }
    async function refresh() {
      if (!current) return;
      inFlight = true;
      try {
        const afterSeq = last?.logs.at(-1)?.seq;
        const data: TaskDetailResult = await api.call('operation.get', { taskId, ...(afterSeq === undefined ? {} : { afterSeq }) });
        if (!current) return;
        if (data.task.id !== taskId) throw new Error('核心返回了不匹配的任务记录。');
        if (last && data.task.seq < last.task.seq) return;
        const logs = [...(last?.logs ?? []), ...data.logs.filter(log => afterSeq === undefined || log.seq > afterSeq)].sort((a, b) => a.seq - b.seq);
        last = { task: data.task, logs: logs.slice(-MAX_LOCAL_LOGS), incomplete: Boolean(last?.incomplete || data.truncated || logs.length > MAX_LOCAL_LOGS) };
        setSnapshot(last);
        setFailure(undefined);
      } catch (error) {
        if (current) setFailure(faultText(error));
      } finally {
        inFlight = false;
        if (current) setPending(false);
        if (queued) { queued = false; schedule(); }
      }
    }
    const off = api.subscribe(event => {
      if (event.taskId === taskId && (event.topic === 'task.log' || event.topic === 'task.progress' || event.topic === 'task.state')) schedule();
    });
    void refresh();
    return () => { current = false; off(); window.clearTimeout(timeout); };
  }, [api, taskId, revision]);
  useLayoutEffect(() => {
    const element = logRef.current;
    if (following && element) element.scrollTop = element.scrollHeight;
  }, [following, snapshot]);

  const currentTask = snapshot?.task ?? row;
  const logs = snapshot?.logs ?? [];
  function exportDetail() {
    if (!snapshot) return;
    const content = redactExport(`RelayDock · 任务详情\n${taskText(currentTask)}\n${snapshot.incomplete ? '注意：日志为当前可读取的片段。\n' : ''}\n${logs.map(log => `${log.at} ${log.text}`).join('\n')}\n`);
    void action.run(async current => {
      if (await exportText(api, content, current) && current()) action.setNotice({ tone: 'success', text: '已导出任务和当前可见日志。' });
    });
  }
  function clear() {
    if (!snapshot || activeStates.includes(currentTask.state)) return;
    confirm({ title: '清空该任务的本地日志？', description: '这不会删除任务记录，也不会修改远端文件；已清除的日志无法从本工作台恢复。',
      info: [['任务 ID', <code>{taskId}</code>]], acceptLabel: '清空日志', danger: true, perform: () => {
        void action.run(async current => {
          requireOk(await api.call('operation.clearLogs', { taskId }), '核心未确认日志已清除。');
          if (!current()) return;
          setSnapshot(undefined);
          setRevision(old => old + 1);
          action.setNotice({ tone: 'success', text: '已清空该任务的本地日志。' });
        });
      } });
  }
  return <Card title={`${appName} · ${kindLabels[currentTask.kind]}`} subtitle="读取并导出所选任务；日志仅按文本显示。" actions={<>
    <Button disabled={pending} onClick={() => setRevision(old => old + 1)}><RefreshCw aria-hidden="true" size={16} />刷新详情</Button>
    <Button disabled={pending || !snapshot || action.busy} onClick={exportDetail}><Download aria-hidden="true" size={16} />导出详情</Button>
    <Button aria-label="关闭任务详情" onClick={close}><X aria-hidden="true" size={16} /></Button>
  </>}>
    <DetailList items={[
      ['任务 ID', <code>{taskId}</code>], ['阶段', <><StateChip state={currentTask.state} />{currentTask.stage !== currentTask.state && ` ${taskStageLabel(currentTask.stage)}`} · {currentTask.percent}%</>],
      ['更新时间', formatDate(currentTask.updatedAt)], ['核心提供的说明', currentTask.message || '—'],
    ]} />
    <Notice value={action.notice} />
    <Notice value={failure ? { tone: 'error', text: failure } : undefined} />
    {pending && <p className="rd-w-subtle" role="status">正在读取任务与日志…</p>}
    {snapshot?.incomplete && <p className="rd-w-subtle">部分较早日志未包含在当前可读取范围内；导出不会补齐这些内容。</p>}
    <div className="rd-w-toolbar"><strong>任务日志</strong><div className="rd-w-actions">
      <label className="rd-w-check"><input type="checkbox" checked={following} onChange={event => setFollowing(event.target.checked)} />跟随新日志</label>
      <Button variant="danger" disabled={pending || !logs.length || activeStates.includes(currentTask.state) || action.busy} onClick={clear}><Trash2 aria-hidden="true" size={16} />清空日志</Button>
    </div></div>
    <pre ref={logRef} className="rd-w-log" tabIndex={0} aria-label="任务日志文本" aria-live="off" onScroll={() => {
      const element = logRef.current;
      if (element && following && element.scrollHeight - element.clientHeight - element.scrollTop > 20) setFollowing(false);
    }}>{logs.length ? logs.map(log => `${formatDate(log.at)}  ${log.text}`).join('\n') : '暂无日志'}</pre>
    {dialog}
  </Card>;
}

function LegacyHistory({ api }: { api: Bridge }) {
  const [page, setPage] = useState(0);
  const load = useCallback(() => api.call('migration.history', { limit: PAGE + 1, offset: page * PAGE }), [api, page]);
  const query = useLocalQuery(load);
  const action = useAction();
  const rows = query.data?.slice(0, PAGE) ?? [];
  function exportPage() {
    const text = redactExport(`RelayDock · 旧版历史\n${rows.map(row => `${row.at} | ${row.kind} | ${row.id} | ${row.summary}${row.detail ? `\n${row.detail}` : ''}`).join('\n')}\n`);
    void action.run(async current => {
      if (await exportText(api, text, current) && current()) action.setNotice({ tone: 'success', text: '已导出本页旧版历史。' });
    });
  }
  return <Card title="旧版记录" subtitle="仅展示已经导入的历史；不会重放旧任务。" actions={<>
    <Button disabled={query.pending} onClick={query.refresh}><RefreshCw aria-hidden="true" size={16} />刷新</Button>
    <Button disabled={query.pending || !rows.length || action.busy} onClick={exportPage}><Download aria-hidden="true" size={16} />导出本页</Button>
  </>}>
    <Notice value={action.notice} />
    <Notice value={query.error ? { tone: 'error', text: query.error } : undefined} />
    {query.pending ? <p className="rd-w-subtle" role="status">正在读取旧版记录…</p> : query.data && (
      rows.length ? <div className="rd-w-table-wrap"><table className="rd-w-table"><caption className="rd-w-sr">导入的旧版记录</caption>
        <thead><tr><th scope="col">时间</th><th scope="col">种类</th><th scope="col">记录</th></tr></thead>
        <tbody>{rows.map(row => <tr key={`${row.kind}:${row.id}`}><td>{formatDate(row.at)}</td><td>{legacyKindLabels.get(row.kind) ?? row.kind}</td><td>
          {row.detail ? <details><summary>{row.summary || row.id}</summary><pre className="rd-w-log">{row.detail}</pre></details> : row.summary || row.id}
        </td></tr>)}</tbody>
      </table></div> : <Empty icon={<FileClock size={22} />} title="此页尚无旧版记录" description="旧版数据可在设置中先预览、再导入。" />
    )}
    <Pager page={page} next={(query.data?.length ?? 0) > PAGE} busy={query.pending} move={setPage} />
  </Card>;
}

export function HistoryWorkspace({ api, host, tasks, catalog }: { api: Bridge; host?: Host | undefined; tasks: Task[]; catalog: AppEntry[] }) {
  const [mode, setMode] = useState<'operations' | 'legacy'>('operations');
  return <>
    <PaneHeading title="任务历史" description="按主机、应用或状态查找任务，并按需查看或导出本地记录。" icon={<FileClock size={25} />} />
    <div className="rd-w-actions" role="group" aria-label="记录来源">
      <Button aria-pressed={mode === 'operations'} onClick={() => setMode('operations')}>新版任务</Button>
      <Button aria-pressed={mode === 'legacy'} onClick={() => setMode('legacy')}>导入的旧版历史</Button>
    </div>
    {mode === 'operations' ? <OperationHistory key={host?.id ?? 'all'} api={api} host={host} tasks={tasks} catalog={catalog} /> : <LegacyHistory api={api} />}
  </>;
}
