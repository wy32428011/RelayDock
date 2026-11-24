import { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowDownToLine, Box, ChevronDown, ChevronRight, Container, Fingerprint, LaptopMinimal, LockKeyhole,
  Network, PackageSearch, Pencil, Plus, Radio, Search, Server, Trash2,
} from 'lucide-react';
import type { AppEntry, Host, Kind, Task } from '@relaydock/protocol';
import type { CoreStatus } from './core';
import { endpoint, hostEligibility, isActive, kindLabel, osLabel, statusLabel, taskLabels, timeLabel } from './core';
import { Badge, EmptyState, Notice, Spinner } from './ui';

export function HostRoute({ host, status, ready, loading, preview, onAdd, onEdit, onRemove }: {
  host?: Host; status?: CoreStatus; ready: boolean; loading: boolean; preview: boolean;
  onAdd: () => void; onEdit: () => void; onRemove: () => void;
}) {
  const local = status?.platform ? osLabel(status.platform) : '本地电脑';
  const trust = host?.fingerprint ? '指纹已保存' : host ? '需核对指纹' : '未指定目标';
  return <section className="host-route" aria-labelledby="host-route-title">
    <div className="route-head">
      <div><span className="route-heading-icon"><Network size={19} aria-hidden="true" /></span><h2 id="host-route-title">主机路由单</h2></div>
      {host ? <Badge tone={host.fingerprint ? 'good' : 'warn'}>{trust}</Badge> : <span className="route-protocol">SSH / SFTP</span>}
    </div>
    <div className={`route-track${host?.fingerprint ? ' route-trusted' : ''}`}>
      <div className="route-node workstation-node">
        <span className="route-node-icon"><LaptopMinimal size={23} aria-hidden="true" /></span>
        <div><span className="route-node-role">工作站</span><strong>{local}</strong><small>{status?.arch ? <code>{status.arch}</code> : 'RelayDock 客户端'}</small></div>
      </div>
      <div className="route-wire"><span className="route-wire-label"><LockKeyhole size={13} aria-hidden="true" />SSH</span></div>
      <div className="route-node remote-node">
        <span className="route-node-icon"><Server size={23} aria-hidden="true" /></span>
        <div><span className="route-node-role">远端主机</span><strong>{host?.name ?? '尚未选择'}</strong><small>{host ? <code>{endpoint(host)}</code> : '连接目标待添加'}</small></div>
      </div>
    </div>
    {host ? <div className="route-detail">
      <dl className="route-metadata">
        <div><dt>远端系统 / 架构</dt><dd>{osLabel(host.os, host.osVersion)} <code>{host.arch || '架构未知'}</code></dd></div>
        <div><dt>登录</dt><dd><code>{host.username}</code><span>{host.auth === 'key' ? 'SSH 私钥' : '密码'}</span></dd></div>
        <div className="route-fingerprint"><dt><Fingerprint size={14} aria-hidden="true" />SSH 指纹</dt><dd><code>{host.fingerprint || '尚未核对'}</code></dd></div>
      </dl>
      <div className="route-actions">
        <span>{hostEligibility(host) || (host.lastSeen ? `最近测试 ${timeLabel(host.lastSeen, 'date')}` : '指纹与凭据已记录，未实时检测连接')}</span>
        <div className="rd-actions">
          <button className="rd-button" disabled={!ready} onClick={onEdit}><Pencil size={15} aria-hidden="true" />编辑 / 测试</button>
          <button className="rd-icon" aria-label="移除此主机" title="移除此主机" disabled={!ready} onClick={onRemove}><Trash2 size={16} aria-hidden="true" /></button>
        </div>
      </div>
    </div> : <div className="route-welcome">
      <div><strong>{loading ? '正在读取主机库' : ready ? '选择一台主机开始' : '桌面核心未连接'}</strong>
        <p>{loading ? '正在加载主机、分组与连接信息。' : ready ? '从主机库选择已有主机，或添加新主机并核对 SSH 指纹。'
          : preview ? '这是浏览器预览。请打开 RelayDock 桌面客户端来添加主机并执行操作。' : '核心就绪后可以添加主机，并测试 SSH 连接。'}</p></div>
      {ready && <button className="rd-button primary" onClick={onAdd}><Plus size={17} aria-hidden="true" />添加主机</button>}
    </div>}
  </section>;
}

const categoryNames: Record<string, string> = {
  database: '数据库', cache: '缓存', web: 'Web 服务', proxy: '代理与网关', search: '搜索',
  messaging: '消息队列', message: '消息队列', discovery: '服务发现', runtime: '运行时', middleware: '中间件',
};
const categoryName = (category: string) => categoryNames[category.toLowerCase()] ?? (category || '未分类');
const isJdk = (app: AppEntry) => app.family?.toLowerCase() === 'jdk' || /^jdk(?:[\d_-]|$)/i.test(app.id);
const operations: Kind[] = ['detect', 'install', 'uninstall'];

function componentState(task?: Task) {
  if (!task) return { label: '未检测', tone: 'neutral' } as const;
  if (isActive(task)) return { label: `${kindLabel[task.kind]}：${taskLabels[task.state].label}`, tone: taskLabels[task.state].tone };
  if (task.state === 'interrupted' || task.state === 'cancelled') return taskLabels[task.state];
  if (task.state === 'failed' && !task.result) return { label: `${kindLabel[task.kind]}失败`, tone: 'error' } as const;
  return statusLabel(task.result);
}

function CatalogRow({ app, jdk, task, selected, enabled, onToggle, onOperate }: {
  app: AppEntry; jdk: boolean; task?: Task; selected: boolean; enabled: boolean;
  onToggle: () => void; onOperate: (kind: Kind) => void;
}) {
  const state = componentState(task);
  return <li className={`catalog-row${jdk ? ' jdk-version' : ''}`}>
    <div className="catalog-identity">
      {jdk ? <span className="jdk-indent" aria-hidden="true" /> : <label className="catalog-check">
        <input type="checkbox" aria-label={`批量选择 ${app.label}`} checked={selected} disabled={!enabled} onChange={onToggle} />
      </label>}
      <span className="catalog-symbol"><Box size={18} aria-hidden="true" /></span>
      <div><strong>{app.label}</strong><small>{app.description || categoryName(app.category)}</small></div>
    </div>
    <div className="catalog-state">
      <Badge tone={state.tone}>{state.label}</Badge>
      {task?.result?.version && <code>{task.result.version}</code>}
      {task?.result?.port ? <small>端口 <code>{task.result.port}</code></small> : null}
    </div>
    <div className="catalog-actions" aria-label={`${app.label} 的操作`}>
      {operations.map((kind) => <button key={kind} className={`rd-button ${kind === 'install' ? 'subtle-accent' : 'text-button'}`}
        aria-label={`${kindLabel[kind]} ${app.label}`} disabled={!enabled} onClick={() => onOperate(kind)}>{kindLabel[kind]}</button>)}
    </div>
  </li>;
}

export interface OperationSelection { kind: Kind; apps: AppEntry[]; host: Host; batch: boolean }

export function Catalog({ catalog, host, tasks, ready, loading, error, onOperate, onResources }: {
  catalog: AppEntry[]; host?: Host; tasks: Task[]; ready: boolean; loading: boolean; error: string;
  onOperate: (selection: OperationSelection) => void; onResources: () => void;
}) {
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [jdkOpen, setJdkOpen] = useState(false);
  const eligible = host ? hostEligibility(host) : undefined;
  const enabled = ready && Boolean(host) && !eligible;
  const categories = useMemo(() => [...new Set(catalog.map((app) => app.category).filter(Boolean))].sort((a, b) => categoryName(a).localeCompare(categoryName(b), 'zh-CN')), [catalog]);
  const observations = useMemo(() => {
    const byApp = new Map<string, Task>();
    for (const task of tasks) {
      if (task.hostId !== host?.id) continue;
      const previous = byApp.get(task.appId);
      if (!previous || Date.parse(previous.createdAt) < Date.parse(task.createdAt) || (previous.createdAt === task.createdAt && Date.parse(previous.updatedAt) < Date.parse(task.updatedAt))) byApp.set(task.appId, task);
    }
    return byApp;
  }, [host?.id, tasks]);
  const needle = query.trim().toLowerCase();
  const visible = catalog.filter((app) => (!category || category === app.category) &&
    [app.label, app.id, app.family, app.description, categoryName(app.category), ...app.versions].some((part) => part?.toLowerCase().includes(needle)));
  const jdk = visible.filter(isJdk);
  const regular = visible.filter((app) => !isJdk(app));
  const catalogCount = catalog.filter((app) => !isJdk(app)).length + Number(catalog.some(isJdk));
  const selectedApps = catalog.filter((app) => !isJdk(app) && selected.has(app.id));
  const allVisible = regular.length > 0 && regular.every((app) => selected.has(app.id));
  const someVisible = regular.some((app) => selected.has(app.id));
  const expanded = jdkOpen || Boolean(needle);
  const selectAllRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = !allVisible && someVisible;
  }, [allVisible, someVisible]);

  function toggle(ids: string[], checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }

  function operate(kind: Kind, apps: AppEntry[], batch: boolean) {
    if (enabled && host && apps.length) onOperate({ kind, apps, host, batch });
  }

  return <section className="catalog" aria-labelledby="catalog-title">
    <div className="section-heading">
      <div><h2 id="catalog-title">应用目录</h2>{catalog.length > 0 && <span className="section-count">{catalogCount} 类组件</span>}</div>
      <button className="rd-button text-button" onClick={onResources} disabled={!ready}><ArrowDownToLine size={15} aria-hidden="true" />离线资源</button>
    </div>
    <div className="catalog-toolbar">
      <label className="rd-search"><Search size={16} aria-hidden="true" /><input aria-label="搜索应用" placeholder="搜索名称、用途或版本" value={query} onChange={(e) => setQuery(e.currentTarget.value)} disabled={!catalog.length} /></label>
      <select aria-label="应用分类" value={category} onChange={(e) => setCategory(e.currentTarget.value)} disabled={!catalog.length}>
        <option value="">全部分类</option>{categories.map((item) => <option key={item} value={item}>{categoryName(item)}</option>)}
      </select>
    </div>
    {error && <Notice tone="error">{error}</Notice>}
    {eligible && ready && <Notice tone="warn">{eligible}，然后再检测、安装或卸载。</Notice>}
    {!host && ready && catalog.length > 0 && <p className="catalog-hint">请先选择一台主机，才能操作组件。</p>}
    {regular.length > 0 && <div className="catalog-selection">
      <label className="rd-check"><input ref={selectAllRef} type="checkbox" checked={allVisible} disabled={!enabled} onChange={(e) => toggle(regular.map((app) => app.id), e.currentTarget.checked)} />选择当前结果</label>
      {selectedApps.length > 0 && <div className="rd-actions">
        <span aria-live="polite">已选 {selectedApps.length} 项</span>
        {operations.map((kind) => <button key={kind} className="rd-button" disabled={!enabled} onClick={() => operate(kind, selectedApps, true)}>{kindLabel[kind]}所选</button>)}
        <button className="rd-button text-button" onClick={() => setSelected(new Set())}>清空</button>
      </div>}
    </div>}
    {loading && !catalog.length ? <p className="rd-loading"><Spinner />正在读取应用目录…</p> : null}
    {!loading && !error && !visible.length && <EmptyState icon={<PackageSearch size={22} />} title={catalog.length ? '没有匹配的应用' : ready ? '暂无可用组件' : '目录尚未加载'}>
      {catalog.length ? '试试其他关键字或切换分类。' : ready ? '目录内容由本地核心提供，可刷新工作台后重试。' : '本地核心就绪后，将读取组件、版本和参数。'}
    </EmptyState>}
    {visible.length > 0 && <ul className="catalog-list">
      {regular.map((app) => <CatalogRow key={app.id} app={app} task={observations.get(app.id)} jdk={false} selected={selected.has(app.id)} enabled={enabled}
        onToggle={() => toggle([app.id], !selected.has(app.id))} onOperate={(kind) => operate(kind, [app], false)} />)}
      {jdk.length > 0 && <li className="jdk-family">
        <button className="jdk-toggle" aria-expanded={expanded} disabled={Boolean(needle)} onClick={() => setJdkOpen(!jdkOpen)}>
          {expanded ? <ChevronDown size={16} aria-hidden="true" /> : <ChevronRight size={16} aria-hidden="true" />}
          <span className="catalog-symbol"><Container size={18} aria-hidden="true" /></span><span><strong>JDK</strong><small>{jdk.length} 个版本入口，仅单机操作</small></span>
        </button>
        {expanded && <ul>{jdk.map((app) => <CatalogRow key={app.id} app={app} task={observations.get(app.id)} jdk selected={false} enabled={enabled} onToggle={() => {}} onOperate={(kind) => operate(kind, [app], false)} />)}</ul>}
      </li>}
    </ul>}
    {host && ready && catalog.length > 0 && <p className="catalog-hint"><Radio size={14} aria-hidden="true" />状态来自最近一次任务，不代表实时监控。</p>}
  </section>;
}
