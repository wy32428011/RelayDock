import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Activity, ArrowUpRight, Clock3, ListTodo, ScrollText, Square } from 'lucide-react';
import type { AppEntry, Bridge, CoreEvent, Host, Task, TaskLog } from '@relaydock/protocol';
import { faultText, isActive, kindLabel, percent, statusLabel, taskLabels, taskStageLabel, timeLabel } from './core';
import { Badge, EmptyState, Modal, Notice, Spinner } from './ui';

const RECENT_LIMIT = 200;
const VIEW_LOG_LIMIT = 600;

export function newerTask(previous: Task | undefined, task: Task): Task {
  if (!previous) return task;
  if (task.seq < previous.seq || (!isActive(previous) && isActive(task))) return previous;
  return task;
}

export function mergeTasks(current: Task[], incoming: Task[]): Task[] {
  const byId = new Map(current.map((task) => [task.id, task]));
  for (const task of incoming) byId.set(task.id, newerTask(byId.get(task.id), task));
  return Array.from(byId.values()).sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt));
}

function taskFromEvent(event: CoreEvent): Task | undefined {
  if (event.topic !== 'task.state' && event.topic !== 'task.progress') return;
  const data = event.data as Partial<Task> | null;
  if (!data || typeof data !== 'object' || typeof data.id !== 'string' || data.id !== event.taskId ||
      typeof data.seq !== 'number' || !Number.isSafeInteger(data.seq) ||
      typeof data.kind !== 'string' || !Object.hasOwn(kindLabel, data.kind) ||
      typeof data.state !== 'string' || !Object.hasOwn(taskLabels, data.state) ||
      typeof data.percent !== 'number' || typeof data.hostId !== 'string' || typeof data.appId !== 'string' ||
      typeof data.version !== 'string' || typeof data.updatedAt !== 'string' || typeof data.createdAt !== 'string' ||
      typeof data.stage !== 'string' || typeof data.message !== 'string') return;
  return data as Task;
}

export function useTaskFeed(api: Bridge | undefined, ready: boolean) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);

  const ingest = useCallback((incoming: Task[]) => {
    if (!incoming.length) return;
    setTasks((current) => {
      let terminalCount = 0;
      return mergeTasks(current, incoming).filter((task) => isActive(task) || terminalCount++ < RECENT_LIMIT);
    });
  }, []);
  const refresh = useCallback(() => setRevision((value) => value + 1), []);

  useEffect(() => {
    if (!api || !ready) return;
    let live = true;
    let timer: number | undefined;
    const buffered = new Map<string, Task>();
    const seen = new Map<string, number>();
    const gaps = new Set<string>();
    let activeReads = 0;

    function schedule() {
      if (live && timer === undefined) timer = window.setTimeout(flush, 120);
    }

    function flush() {
      timer = undefined;
      if (!live || !api) return;
      if (buffered.size) {
        ingest([...buffered.values()]);
        buffered.clear();
      }
      // Fetch the durable snapshot for missing frames. The detail pane uses its own log cursor.
      const ids = [...gaps].slice(0, Math.max(0, 5 - activeReads));
      for (const taskId of ids) {
        gaps.delete(taskId);
        activeReads++;
        api.call('operation.get', { taskId, afterSeq: Number.MAX_SAFE_INTEGER })
          .then(({ task }) => { if (live) ingest([task]); })
          .catch((cause: unknown) => { if (live) setError(faultText(cause)); })
          .finally(() => { activeReads--; if (gaps.size) schedule(); });
      }
    }

    const unsubscribe = api.subscribe((event) => {
      const { taskId, seq, topic } = event;
      if (!taskId || !['task.state', 'task.progress', 'task.log'].includes(topic)) return;
      const previous = seen.get(taskId) ?? 0;
      if (typeof seq === 'number') {
        if (seq <= previous) return;
        if (seq > previous + 1) gaps.add(taskId);
        // Insertion order bounds sequence bookkeeping even in long-lived windows.
        seen.delete(taskId);
        seen.set(taskId, seq);
        const oldest = seen.keys().next().value;
        if (seen.size > 1_400 && oldest) seen.delete(oldest);
      }
      const task = taskFromEvent(event);
      if (task) buffered.set(task.id, newerTask(buffered.get(task.id), task));
      else if (topic !== 'task.log') gaps.add(taskId);
      if (buffered.size || gaps.size) schedule();
    });
    return () => {
      live = false;
      unsubscribe();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [api, ingest, ready]);

  useEffect(() => {
    if (!api || !ready) return;
    let live = true;
    setLoading(true);
    // The active-state filters also recover older jobs after a renderer reload.
    Promise.allSettled([
      api.call('operation.list', { limit: RECENT_LIMIT }),
      ...(['queued', 'preflight', 'running', 'verifying', 'cancelling'] as const).map((state) => api.call('operation.list', { limit: RECENT_LIMIT, state })),
    ]).then((replies) => {
      if (!live) return;
      ingest(replies.flatMap((reply) => reply.status === 'fulfilled' ? reply.value : []));
      const rejection = replies.find((reply) => reply.status === 'rejected');
      setError(rejection?.status === 'rejected' ? faultText(rejection.reason) : '');
      setLoading(false);
    });
    return () => { live = false; };
  }, [api, ingest, ready, revision]);

  return { tasks, error, loading: ready && loading, ingest, refresh };
}

function Progress({ task }: { task: Task }) {
  const value = percent(task.percent);
  return <div className="task-progress">
    <progress max={100} value={value} aria-label={`${kindLabel[task.kind]}任务进度`} />
    <span>{value}%</span>
  </div>;
}

export function TaskRail({ tasks, hosts, catalog, ready, loading, error, onTask, onHistory }: {
  tasks: Task[]; hosts: Host[]; catalog: AppEntry[]; ready: boolean; loading: boolean; error: string;
  onTask: (id: string) => void; onHistory: () => void;
}) {
  const active = tasks.filter(isActive);
  const recent = tasks.filter((task) => !isActive(task)).slice(0, 6);
  const names = useMemo(() => new Map(hosts.map((host) => [host.id, host.name])), [hosts]);
  const apps = useMemo(() => new Map(catalog.map((app) => [app.id, app.label])), [catalog]);
  const title = (task: Task) => `${kindLabel[task.kind]} ${apps.get(task.appId) ?? task.appId}`;
  const target = (task: Task) => names.get(task.hostId) ?? '原主机记录不可用';
  const latest = recent[0];

  return <aside className="task-rail" aria-label="任务与事件">
    <p className="sr-only" role="status">{latest && `${target(latest)}：${title(latest)}，${taskLabels[latest.state].label}。${timeLabel(latest.updatedAt)}`}</p>
    <div className="task-rail-head"><div className="rail-title"><ListTodo size={18} aria-hidden="true" /><h2>任务队列</h2><span className="rail-count" aria-label={`${active.length} 项活动任务`}>{active.length}</span></div></div>
    <div className="task-rail-content" aria-busy={loading}>
      {error && <Notice tone="error">无法同步任务：{error}</Notice>}
      {!ready && tasks.length > 0 && <Notice tone="warn">核心不可用。下方为上次同步的状态。</Notice>}
      {loading && !tasks.length && <p className="rd-loading"><Spinner />正在同步任务…</p>}
      {!active.length && !loading && <EmptyState icon={<ListTodo size={20} />} title={ready ? '没有活动任务' : '队列未同步'}>
        {ready ? '检测、安装和卸载任务会在这里显示进度。' : '本地核心就绪后，将从本机读取任务。'}
      </EmptyState>}
      {active.length > 0 && <ul className="active-tasks">{active.slice(0, 12).map((task) => {
        const state = taskLabels[task.state];
        return <li key={task.id}>
          <button className="active-task" onClick={() => onTask(task.id)}>
            <div className="task-item-title"><strong>{title(task)}</strong><ArrowUpRight size={15} aria-hidden="true" /></div>
            <div className="task-item-meta"><small>{target(task)}</small><Badge tone={state.tone}>{state.label}</Badge></div>
            {task.stage !== task.state && <small className="task-stage">{taskStageLabel(task.stage)}</small>}
            <Progress task={task} />
          </button>
        </li>;
      })}</ul>}
      {active.length > 12 && <button className="rd-button text-button more-tasks" onClick={onHistory}>还有 {active.length - 12} 项，查看任务历史</button>}
      <section className="task-activity" aria-labelledby="task-activity-title">
        <div className="activity-heading"><Activity size={16} aria-hidden="true" /><h3 id="task-activity-title">最近事件</h3></div>
        {recent.length === 0 ? <p className="activity-empty">{ready ? '任务结束后会在这里留下记录。' : '尚未读取任务记录。'}</p> : <ol>{recent.map((task) => {
          const state = taskLabels[task.state];
          return <li key={task.id}><button className="event-row" onClick={() => onTask(task.id)}>
            <time dateTime={task.updatedAt} title={timeLabel(task.updatedAt, 'date')}>{timeLabel(task.updatedAt)}</time>
            <span><strong>{title(task)}</strong><small>{target(task)}</small><Badge tone={state.tone}>{state.label}</Badge></span>
          </button></li>;
        })}</ol>}
      </section>
    </div>
    <div className="task-rail-foot"><button className="rd-button text-button" onClick={onHistory}><Clock3 size={16} aria-hidden="true" />查看完整任务历史<ArrowUpRight size={14} aria-hidden="true" /></button></div>
  </aside>;
}

export function TaskDialog({ api, taskId, currentTask, hosts, catalog, available, onDismiss, onSnapshot }: {
  api: Bridge; taskId: string; currentTask?: Task; hosts: Host[]; catalog: AppEntry[];
  available: boolean; onDismiss: () => void; onSnapshot: (tasks: Task[]) => void;
}) {
  const [snapshot, setSnapshot] = useState<Task>();
  const [logs, setLogs] = useState<TaskLog[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [following, setFollowing] = useState(true);
  const [error, setError] = useState('');
  const [cancelError, setCancelError] = useState('');
  const [requested, setRequested] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [revision, setRevision] = useState(0);
  const tail = useRef(0);
  const logBox = useRef<HTMLDivElement>(null);
  const task = currentTask ? newerTask(snapshot, currentTask) : snapshot;
  const stage = task && task.stage !== task.state ? taskStageLabel(task.stage) : undefined;
  const result = task?.result;
  const resultStatus = result && statusLabel(result);
  const cancellable = task && isActive(task) && task.state !== 'cancelling' && !requested;

  useEffect(() => {
    if (!available) return;
    let live = true;
    let timer: number | undefined;
    let reading = false;
    let dirty = false;

    function schedule() {
      dirty = true;
      if (timer === undefined && !reading) timer = window.setTimeout(() => { timer = undefined; void load(); }, 220);
    }

    async function load() {
      if (!live) return;
      reading = true;
      dirty = false;
      try {
        const detail = await api.call('operation.get', { taskId, afterSeq: tail.current });
        if (!live) return;
        setSnapshot((previous) => newerTask(previous, detail.task));
        onSnapshot([detail.task]);
        setError('');
        if (detail.truncated) setTruncated(true);
        // Move past the snapshot sequence, including non-log state events.
        tail.current = Math.max(tail.current, detail.task.seq, ...detail.logs.map((log) => log.seq));
        if (detail.logs.length || detail.truncated) setLogs((previous) => {
          const merged = detail.truncated ? detail.logs : [...previous, ...detail.logs];
          return merged.slice(-VIEW_LOG_LIMIT);
        });
      } catch (cause) {
        if (live) setError(faultText(cause));
      } finally {
        reading = false;
        if (live && dirty) schedule();
      }
    }

    const unsubscribe = api.subscribe((event) => {
      if (event.taskId === taskId && event.topic.startsWith('task.')) schedule();
    });
    void load();
    return () => {
      live = false;
      unsubscribe();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [api, available, onSnapshot, revision, taskId]);

  useLayoutEffect(() => {
    const scroller = logBox.current;
    if (scroller && following) scroller.scrollTop = scroller.scrollHeight;
  }, [following, logs]);

  async function cancel() {
    if (!available || !cancellable || cancelling) return;
    setCancelling(true);
    setCancelError('');
    try {
      const response = await api.call('operation.cancel', { taskId });
      if (!response.ok) throw new Error('本地核心没有接受此取消请求。');
      setRequested(true);
      setConfirmCancel(false);
    } catch (cause) { setCancelError(faultText(cause)); }
    finally { setCancelling(false); setRevision((value) => value + 1); }
  }

  const app = task ? catalog.find((entry) => entry.id === task.appId)?.label ?? task.appId : '';
  const target = task ? hosts.find((host) => host.id === task.hostId)?.name ?? '原主机记录不可用' : '';
  return <Modal title={task ? `${kindLabel[task.kind]} ${app}` : '任务详情'} description="任务状态与日志来自本机持久记录。" onDismiss={onDismiss} busy={cancelling} className="task-detail-dialog">
    <div className="rd-dialog-body">
      {!available && <Notice tone="warn">本地核心不可用，无法刷新状态或读取更多日志。</Notice>}
      {error && <Notice tone="error">{error}</Notice>}
      {!task && available && !error && <p className="rd-loading"><Spinner />正在读取任务详情…</p>}
      {task && <>
        <div className="task-detail-summary">
          <div className="task-item-title"><strong>{target}</strong><Badge tone={taskLabels[task.state].tone}>{taskLabels[task.state].label}</Badge></div>
          <p role="status">{taskLabels[task.state].label}{stage ? `：${stage}` : ''}{task.message && `。${task.message}`}</p>
          <Progress task={task} />
          <dl className="review-data">
            <div><dt>任务标识</dt><dd><code>{task.id}</code></dd></div>
            {task.batchId && <div><dt>批次标识</dt><dd><code>{task.batchId}</code></dd></div>}
            <div><dt>更新时间</dt><dd>{timeLabel(task.updatedAt, 'date')}</dd></div>
            {result && resultStatus && <div><dt>远端复查</dt><dd><Badge tone={resultStatus.tone}>{resultStatus.label}</Badge>{result.version && <code>{result.version}</code>}{result.port ? <code>:{result.port}</code> : null}</dd></div>}
          </dl>
        </div>
        {requested && isActive(task) && <Notice tone="warn">已提交取消请求；是否停止以最终状态为准。</Notice>}
        {confirmCancel && cancellable && <Notice tone="warn"><p>向本地核心请求取消？已开始的远端操作可能无法立即停止。</p><div className="rd-actions">
          <button className="rd-button" disabled={cancelling} onClick={() => setConfirmCancel(false)}>保留任务</button><button className="rd-button danger" disabled={!available || cancelling} onClick={() => void cancel()}>{cancelling && <Spinner />}确认请求取消</button>
        </div></Notice>}
        {cancelError && <Notice tone="error">{cancelError}</Notice>}
        <section className="task-log-section" aria-labelledby="task-logs-title">
          <header><h3 id="task-logs-title"><ScrollText size={17} aria-hidden="true" />任务日志</h3><button className="rd-button text-button" onClick={() => setFollowing((value) => !value)}>{following ? '暂停跟随' : '跟随末尾'}</button></header>
          {(truncated || logs.length >= VIEW_LOG_LIMIT) && <p className="rd-help">仅显示可取得的近期日志；更早的记录可能已截断。</p>}
          <div ref={logBox} className="task-log-scroll" aria-label="任务日志正文" tabIndex={0} onScroll={(event) => {
            const node = event.currentTarget;
            if (node.scrollHeight - node.scrollTop - node.clientHeight > 28) setFollowing(false);
          }}>
            {logs.length ? <ol>{logs.map((log) => <li key={log.seq}><time dateTime={log.at}>{timeLabel(log.at)}</time><span>{log.text}</span></li>)}</ol>
              : <p>当前没有可显示的日志。</p>}
          </div>
        </section>
      </>}
    </div>
    <footer className="rd-dialog-footer">
      {cancellable && <button className="rd-button" disabled={!available || cancelling || confirmCancel} onClick={() => setConfirmCancel(true)}><Square size={14} aria-hidden="true" />请求取消</button>}
      <button className="rd-button" disabled={!available || cancelling} onClick={() => setRevision((value) => value + 1)}>刷新状态</button>
      <button className="rd-button primary" data-focus-initial onClick={onDismiss} disabled={cancelling}>关闭</button>
    </footer>
  </Modal>;
}
