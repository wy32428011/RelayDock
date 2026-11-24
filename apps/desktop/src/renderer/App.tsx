import { useEffect, useState } from 'react';
import {
  Archive, CircleAlert, CircleCheck, Clock3, CodeXml, Files, LayoutPanelTop, ListTodo, MoonStar,
  RefreshCw, Server, Settings2, Sun, Terminal, Workflow,
} from 'lucide-react';
import { VERSION } from '@relaydock/protocol';
import type { AppEntry, Bridge, Host, Task, Theme } from '@relaydock/protocol';
import type { CoreStatus } from './core';
import { endpoint, faultText, hostEligibility, isActive, osLabel, readBridge } from './core';
import { HostDialog, HostRail, RemoveHostDialog } from './hosts';
import { OperationDialog } from './operations';
import { Catalog, HostRoute } from './overview';
import type { OperationSelection } from './overview';
import { mergeTasks, TaskDialog, TaskRail, useTaskFeed } from './tasks';
import { EmptyState, Modal, Notice, Spinner } from './ui';
import { WorkspacePane } from './widgets';

const workPages = [
  { id: 'overview', label: '工作区', icon: LayoutPanelTop },
  { id: 'terminal', label: '终端', icon: Terminal },
  { id: 'files', label: '远端文件', icon: Files },
  { id: 'scripts', label: '脚本库', icon: CodeXml },
  { id: 'deploy', label: '自定义部署', icon: Workflow },
] as const;
const toolPages = [
  { id: 'resources', label: '离线资源', icon: Archive },
  { id: 'history', label: '任务历史', icon: Clock3 },
  { id: 'settings', label: '设置', icon: Settings2 },
] as const;
type Page = (typeof workPages | typeof toolPages)[number]['id'];
type HostAction = { kind: 'add' } | { kind: 'edit' | 'remove'; host: Host };
type Health = { phase: 'preview' | 'checking' | 'ready' | 'offline'; status?: CoreStatus; detail?: string };

function useMedia(query: string) {
  const [match, setMatch] = useState(() => Boolean(window.matchMedia?.(query).matches));
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const change = () => setMatch(media.matches);
    change();
    media.addEventListener('change', change);
    return () => media.removeEventListener('change', change);
  }, [query]);
  return match;
}

function useCoreHealth(api: Bridge | undefined, revision: number) {
  const [health, setHealth] = useState<Health>({ phase: api ? 'checking' : 'preview' });
  useEffect(() => {
    if (!api) return;
    let live = true;
    let generation = 0;
    function accept(status: CoreStatus) {
      setHealth((previous) => {
        const phase = status.ready ? 'ready' : 'offline';
        if (previous.phase === phase && previous.status?.platform === status.platform && previous.status?.arch === status.arch &&
            previous.status?.version === status.version && previous.detail === status.detail) return previous;
        return { phase, status, detail: status.detail };
      });
    }
    async function check() {
      if (!api) return;
      const requested = ++generation;
      try {
        const status = await api.call('core.status', {});
        if (live && requested === generation) accept(status);
      } catch (cause) {
        if (live && requested === generation) setHealth({ phase: 'offline', detail: faultText(cause) });
      }
    }
    const unsubscribe = api.subscribe((event) => {
      if (event.topic !== 'core.status') return;
      const data = event.data as Partial<CoreStatus> | null;
      if (data && typeof data === 'object' && typeof data.ready === 'boolean' && typeof data.platform === 'string' && typeof data.arch === 'string') {
        generation++;
        accept(data as CoreStatus);
      } else void check();
    });
    void check();
    // Recheck even if a lifecycle event was lost during startup or reconnect.
    const timer = window.setInterval(() => void check(), 6_000);
    return () => { live = false; unsubscribe(); window.clearInterval(timer); };
  }, [api, revision]);
  return api ? health : { phase: 'preview' } as Health;
}

function Brand() {
  return <div className="rd-brand">
    <svg className="rd-brand-mark" width="33" height="33" viewBox="0 0 36 36" fill="none" aria-hidden="true">
      <rect x=".75" y=".75" width="34.5" height="34.5" rx="9.25" />
      <path d="M9 12h10l4 6h4M9 24h10l4-6" stroke="currentColor" strokeWidth="1.65" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="9" cy="12" r="2.45" /><circle cx="9" cy="24" r="2.45" /><circle cx="27" cy="18" r="2.45" />
    </svg>
    <span className="rd-wordmark">Relay<span>Dock</span></span><span className="rd-brand-divider" /><span className="rd-brand-label">远程安装工作台</span>
  </div>;
}

export function App({ api = readBridge() }: { api?: Bridge }) {
  const [page, setPage] = useState<Page>('overview');
  const [theme, setTheme] = useState<Theme>('dark');
  const [revision, setRevision] = useState(0);
  const [dataRevision, setDataRevision] = useState(0);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [catalog, setCatalog] = useState<AppEntry[]>([]);
  const [hostError, setHostError] = useState('');
  const [catalogError, setCatalogError] = useState('');
  const [loading, setLoading] = useState(false);
  const [selectedId, setSelectedId] = useState<string>();
  const [hostHistory, setHostHistory] = useState<{ id: string; tasks: Task[]; error?: string }>();
  const [hostAction, setHostAction] = useState<HostAction>();
  const [operation, setOperation] = useState<OperationSelection>();
  const [taskId, setTaskId] = useState<string>();
  const [drawer, setDrawer] = useState<'hosts' | 'tasks'>();
  const [feedback, setFeedback] = useState('');
  const small = useMedia('(max-width: 760px)');
  const tasksInDrawer = useMedia('(max-width: 1120px)');
  const systemLight = useMedia('(prefers-color-scheme: light)');
  const health = useCoreHealth(api, revision);
  const ready = health.phase === 'ready';
  const feed = useTaskFeed(api, ready);
  const host = hosts.find((item) => item.id === selectedId);
  const scopedHistory = hostHistory && hostHistory.id === host?.id ? hostHistory : undefined;
  const observations = scopedHistory ? mergeTasks(scopedHistory.tasks, feed.tasks) : feed.tasks;
  const activeCount = feed.tasks.filter(isActive).length;
  const lightTheme = theme === 'light' || (theme === 'system' && systemLight);

  useEffect(() => {
    document.documentElement.dataset.theme = lightTheme ? 'light' : 'dark';
  }, [lightTheme]);

  useEffect(() => {
    if (!api || !ready) return;
    let live = true;
    setLoading(true);
    const hostRequest = api.call('host.list', {}).then((value) => {
      if (live) { setHosts(value ?? []); setHostError(''); }
    }).catch((cause: unknown) => { if (live) setHostError(faultText(cause)); });
    const catalogRequest = api.call('catalog.list', {}).then((value) => {
      if (live) { setCatalog(value ?? []); setCatalogError(''); }
    }).catch((cause: unknown) => { if (live) setCatalogError(faultText(cause)); });
    void Promise.all([hostRequest, catalogRequest]).finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [api, dataRevision, ready]);

  useEffect(() => {
    const hostId = host?.id;
    if (!api || !ready || !hostId) return;
    let live = true;
    api.call('operation.list', { hostId, limit: 100 }).then((tasks) => {
      if (live) setHostHistory({ id: hostId, tasks: tasks ?? [] });
    }).catch((cause: unknown) => { if (live) setHostHistory({ id: hostId, tasks: [], error: faultText(cause) }); });
    return () => { live = false; };
  }, [api, dataRevision, host?.id, ready]);

  useEffect(() => {
    if (!api || !ready) return;
    let live = true;
    api.call('settings.get', {}).then((settings) => { if (live && settings) setTheme(settings.theme); }).catch(() => { /* Keep the usable default theme. */ });
    return () => { live = false; };
  }, [api, ready]);

  function refresh() {
    setRevision((value) => value + 1);
    setDataRevision((value) => value + 1);
    feed.refresh();
  }

  function navigate(next: Page) {
    setPage(next);
    setDrawer(undefined);
  }

  function selectHost(id: string) {
    setSelectedId(id);
    setDrawer(undefined);
    if (toolPages.some((item) => item.id === page)) setPage('overview');
  }

  function startAdd() {
    if (!ready) return;
    setDrawer(undefined);
    setHostAction({ kind: 'add' });
  }

  function hostSaved(saved: Host) {
    setHosts((current) => current.some((item) => item.id === saved.id) ? current.map((item) => item.id === saved.id ? saved : item) : [...current, saved]);
    setHostAction(undefined);
    setSelectedId(saved.id);
    setPage('overview');
    setFeedback(`已保存主机 ${saved.name}。`);
    setDataRevision((value) => value + 1);
  }

  function hostRemoved(removed: Host) {
    setHosts((current) => current.filter((item) => item.id !== removed.id));
    setSelectedId((current) => current === removed.id ? undefined : current);
    setHostAction(undefined);
    setFeedback(`已移除主机 ${removed.name} 的本地记录。`);
    setDataRevision((value) => value + 1);
  }

  function showTask(id: string) {
    setDrawer(undefined);
    setTaskId(id);
  }

  function taskStarted(ids: string[], batchId?: string) {
    setOperation(undefined);
    setFeedback(batchId ? `已提交 ${ids.length} 项任务。请在任务队列查看各项结果。` : '已提交任务，结果以任务状态为准。');
    feed.refresh();
    if (!batchId && ids.length === 1) showTask(ids[0]);
    else if (tasksInDrawer) setDrawer('tasks');
  }

  const hostRail = <HostRail hosts={hosts} selectedId={host?.id} ready={ready} loading={ready && loading}
    error={hostError} onAdd={startAdd} onSelect={selectHost} tools={
      <nav className="tool-nav" aria-label="全局工具">{toolPages.map(({ id, label, icon: Icon }) =>
        <button key={id} aria-current={page === id ? 'page' : undefined} onClick={() => navigate(id)}><Icon size={17} aria-hidden="true" />{label}</button>)}</nav>
    } />;
  const taskRail = <TaskRail tasks={feed.tasks} hosts={hosts} catalog={catalog} ready={ready} loading={feed.loading}
    error={feed.error} onTask={showTask} onHistory={() => navigate('history')} />;
  const targetRestriction = host ? hostEligibility(host) : undefined;
  const remotePane = page === 'terminal' || page === 'files' || page === 'scripts' || page === 'deploy';
  const pageLabel = [...workPages, ...toolPages].find((entry) => entry.id === page)?.label ?? '工作区';
  const statusLabel = { checking: '正在启动本地核心', preview: '浏览器预览 · 未连接桌面', ready: '本地核心就绪', offline: '本地核心不可用' }[health.phase];
  const offlineText = health.phase === 'preview' ? '通过 RelayDock 桌面客户端打开后，才能访问本地数据或连接远端主机。'
    : health.phase === 'checking' ? '等待桌面核心握手完成。' : health.detail || '请刷新工作台；仍未就绪时重新打开客户端。';

  return <div className="rd-shell">
    <a className="skip-link" href="#workspace-main">跳到工作区</a>
    <header className="rd-topbar">
      <div className="rd-topbar-start">
        {small && <button className="rd-icon topbar-mobile" aria-label="打开主机库" aria-haspopup="dialog" title="打开主机库" onClick={() => setDrawer('hosts')}><Server size={18} aria-hidden="true" /></button>}
        <Brand />
      </div>
      <div className="rd-topbar-end">
        {tasksInDrawer && <button className="rd-button topbar-queue" aria-label={`打开任务队列，${activeCount} 项活动任务`} aria-haspopup="dialog" onClick={() => setDrawer('tasks')}>
          <ListTodo size={17} aria-hidden="true" /><span>任务</span>{activeCount > 0 && <span className="rail-count">{activeCount}</span>}
        </button>}
        <button className="rd-icon" disabled={!api} onClick={refresh} aria-label="刷新工作台" title="刷新工作台"><RefreshCw size={16} aria-hidden="true" /></button>
        <button className="rd-icon" onClick={() => setTheme(lightTheme ? 'dark' : 'light')} aria-label={`切换本窗口为${lightTheme ? '暗色' : '亮色'}主题`} title={`切换本窗口为${lightTheme ? '暗色' : '亮色'}主题`}>
          {lightTheme ? <MoonStar size={18} aria-hidden="true" /> : <Sun size={18} aria-hidden="true" />}
        </button>
      </div>
    </header>

    <div className="rd-frame" data-host-drawer={small} data-task-drawer={tasksInDrawer}>
      {!small && hostRail}
      <div className="workspace-frame">
        <nav className="work-nav" aria-label="主机工作区功能">{workPages.map(({ id, label, icon: Icon }) =>
          <button key={id} aria-current={page === id ? 'page' : undefined} onClick={() => navigate(id)}><Icon size={17} aria-hidden="true" /><span>{label}</span></button>)}</nav>
        <main id="workspace-main" className="workspace-main" tabIndex={-1}>
          {feedback && <div className="feedback-strip"><div role="status"><CircleCheck size={17} aria-hidden="true" />{feedback}</div><button className="rd-button text-button" onClick={() => setFeedback('')}>关闭提示</button></div>}
          {page === 'overview' ? <>
            <header className="workspace-heading"><div><h1>主机工作区</h1><p>检测、安装和卸载选定主机上的组件。</p></div>
              {host && <span className="workspace-scope"><Server size={15} aria-hidden="true" />{host.group || '未分组'}</span>}
            </header>
            {health.phase === 'offline' && <Notice tone="warn">{offlineText}</Notice>}
            <HostRoute host={host} status={health.status} ready={ready} loading={ready && loading} preview={health.phase === 'preview'}
              onAdd={startAdd} onEdit={() => { if (host) setHostAction({ kind: 'edit', host }); }} onRemove={() => { if (host) setHostAction({ kind: 'remove', host }); }} />
            {scopedHistory?.error && <Notice tone="warn">未能读取当前主机的历史状态：{scopedHistory.error}</Notice>}
            <Catalog key={host?.id ?? 'no-host'} catalog={catalog} host={host} tasks={observations} ready={ready} loading={ready && loading}
              error={catalogError} onOperate={setOperation} onResources={() => navigate('resources')} />
          </> : <>
            <h1 className="sr-only">{pageLabel}</h1>
            {ready && api ? <>
              {remotePane && targetRestriction && <Notice tone="warn">{targetRestriction}。<button className="rd-button text-button" onClick={() => { if (host) setHostAction({ kind: 'edit', host }); }}>编辑 / 测试主机</button></Notice>}
              <WorkspacePane pane={page} api={api} host={remotePane && targetRestriction ? undefined : host} tasks={feed.tasks} catalog={catalog} onTheme={setTheme}
                onImport={() => setDataRevision((value) => value + 1)} />
            </> : <section className="pane-unavailable"><h2>{pageLabel}</h2><EmptyState icon={<CircleAlert size={22} />} title={statusLabel}>{offlineText}</EmptyState></section>}
          </>}
        </main>
      </div>
      {!tasksInDrawer && taskRail}
    </div>

    <footer className="rd-statusbar" aria-label="工作台状态">
      <div className={`core-indicator ${health.phase}`} role="status">
        {health.phase === 'checking' ? <Spinner /> : health.phase === 'ready' ? <CircleCheck size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}
        {statusLabel}
      </div>
      {host && <div className="status-target" title={`${host.name} ${endpoint(host)}`}><Server size={14} aria-hidden="true" />{host.name}<code>{endpoint(host)}</code></div>}
      <div className="status-meta">
        {health.status?.platform && <span>{osLabel(health.status.platform)} <code>{health.status.arch}</code></span>}
        {health.status?.version && <code>核心版本 {health.status.version}</code>}
        <code>协议 v{VERSION}</code>
      </div>
    </footer>

    {drawer === 'hosts' && small && <Modal title="主机与工具" onDismiss={() => setDrawer(undefined)} className="rail-drawer drawer-left">{hostRail}</Modal>}
    {drawer === 'tasks' && tasksInDrawer && <Modal title="任务与事件" onDismiss={() => setDrawer(undefined)} className="rail-drawer drawer-right">{taskRail}</Modal>}
    {api && hostAction && (hostAction.kind === 'remove'
      ? <RemoveHostDialog api={api} host={hostAction.host} available={ready} onCancel={() => setHostAction(undefined)} onRemoved={() => hostRemoved(hostAction.host)} />
      : <HostDialog api={api} host={hostAction.kind === 'edit' ? hostAction.host : undefined} available={ready} onCancel={() => setHostAction(undefined)} onSaved={hostSaved} />)}
    {api && operation && <OperationDialog api={api} selection={operation} hosts={hosts} available={ready} onDismiss={() => setOperation(undefined)} onResources={() => { setOperation(undefined); navigate('resources'); }}
      onUncertain={() => { setOperation(undefined); feed.refresh(); if (tasksInDrawer) setDrawer('tasks'); else navigate('history'); }} onStarted={taskStarted} />}
    {api && taskId && <TaskDialog key={taskId} api={api} taskId={taskId} currentTask={feed.tasks.find((item) => item.id === taskId)} hosts={hosts} catalog={catalog} available={ready}
      onDismiss={() => setTaskId(undefined)} onSnapshot={feed.ingest} />}
  </div>;
}

export default App;
