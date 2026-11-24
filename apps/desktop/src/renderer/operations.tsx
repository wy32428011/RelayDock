import { useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ArrowDownToLine, ArrowLeft, ClipboardCheck, PackageCheck, Play, Server } from 'lucide-react';
import type { AppEntry, AppParameter, Bridge, Host, OfflineResource, Operation } from '@relaydock/protocol';
import { bytesLabel, endpoint, faultText, hostEligibility, kindLabel, osLabel, requestKey } from './core';
import type { OperationSelection } from './overview';
import { Badge, Modal, Notice, Spinner } from './ui';

interface AppOptions { version: string; source: 'offline' | 'online'; values: Record<string, string> }
interface PlanItem { op: Operation; app: AppEntry; host: Host; resource?: OfflineResource }
interface ReviewedItem { item: PlanItem; ok: boolean; summary: string }
const MAX_BATCH = 128;

function initialOptions(app: AppEntry, install: boolean): AppOptions {
  return {
    version: install ? app.versions[0] ?? '' : '', source: 'offline',
    values: Object.fromEntries(app.params.map((field) => [field.key, field.default ?? (field.type === 'boolean' ? 'false' : '')])),
  };
}

const normalized = (value: string) => value.trim().toLowerCase();
function canonicalArch(value: string): string {
  const arch = normalized(value);
  if (['amd64', 'x64', 'x86_64'].includes(arch)) return 'amd64';
  if (['arm64', 'aarch64'].includes(arch)) return 'arm64';
  return '';
}
function matchingResources(resources: OfflineResource[], app: AppEntry, version: string, host: Host) {
  const arch = canonicalArch(host.arch);
  if (!host.os || !host.osVersion || !arch || !version) return [];
  return resources.filter((item) => item.appId === app.id && item.version === version &&
    normalized(item.os) === normalized(host.os) && item.osVersion === host.osVersion && canonicalArch(item.arch) === arch);
}

function sameTarget(previous: Host, current?: Host): boolean {
  return Boolean(current && !hostEligibility(current) && previous.name === current.name && previous.address === current.address && previous.port === current.port &&
    previous.username === current.username && previous.auth === current.auth && previous.fingerprint === current.fingerprint &&
    previous.os === current.os && previous.osVersion === current.osVersion && previous.arch === current.arch);
}

function resourceLabel(resource: OfflineResource): string {
  return `${resource.source} / ${resource.trust === 'signed' ? '已签名' : '已核对'} / ${bytesLabel(resource.bytes)}`;
}

function reviewParameter(field: AppParameter, value: string | undefined) {
  if (field.type === 'secret') return '已填写，不显示';
  if (field.type === 'boolean') return value === 'true' ? '是' : '否';
  return value;
}

function ParameterField({ field, value, onChange }: { field: AppParameter; value: string; onChange: (value: string) => void }) {
  if (field.type === 'boolean') return <div className="boolean-field">
    <label className="rd-check"><input type="checkbox" checked={value === 'true'} onChange={(e) => onChange(String(e.currentTarget.checked))} />{field.label}</label>
    {field.hint && <small>{field.hint}</small>}
  </div>;
  return <label>{field.label}{field.required ? '（必填）' : ''}
    {field.type === 'select' ? <select value={value} required={field.required} onChange={(e) => onChange(e.currentTarget.value)}>
      <option value="">请选择</option>{field.options?.map((option) => <option key={option} value={option}>{option}</option>)}
    </select> : <input value={value} required={field.required} onChange={(e) => onChange(e.currentTarget.value)}
      type={field.type === 'secret' ? 'password' : field.default === 'auto' ? 'text' : field.type} step={field.type === 'number' ? 1 : undefined}
      autoComplete={field.type === 'secret' ? 'new-password' : 'off'} maxLength={2_048} />}
    {field.hint && <small>{field.hint}</small>}
  </label>;
}

export function OperationDialog({ api, selection, hosts, available, onDismiss, onResources, onUncertain, onStarted }: {
  api: Bridge; selection: OperationSelection; hosts: Host[]; available: boolean;
  onDismiss: () => void; onResources: () => void; onUncertain: () => void; onStarted: (taskIds: string[], batchId?: string) => void;
}) {
  const { kind, apps, batch, host } = selection;
  const title = batch ? `批量${kindLabel[kind]}` : `${kindLabel[kind]} ${apps[0].label}`;
  const install = kind === 'install';
  const uninstall = kind === 'uninstall';
  const modifying = install || uninstall;
  const formId = useId();
  const reviewRef = useRef<HTMLHeadingElement>(null);
  const [hostIds, setHostIds] = useState(() => new Set([host.id]));
  const [options, setOptions] = useState(() => Object.fromEntries(apps.map((app) => [app.id, initialOptions(app, install)])));
  const [resources, setResources] = useState<OfflineResource[]>([]);
  const [resourceError, setResourceError] = useState('');
  const [resourcesLoading, setResourcesLoading] = useState(install);
  const [pickedResources, setPickedResources] = useState<Record<string, string>>({});
  const [keepData, setKeepData] = useState(true);
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState<'validate' | 'submit'>();
  const [review, setReview] = useState<ReviewedItem[]>();
  const [submissionUnknown, setSubmissionUnknown] = useState(false);
  const [error, setError] = useState('');

  const targets = hosts.filter((candidate) => hostIds.has(candidate.id));
  const targetCount = targets.length * apps.length;
  const allPassed = Boolean(review?.length && review.every((entry) => entry.ok));
  const secrets = apps.flatMap((app) => app.params.filter((field) => field.type === 'secret').map((field) => options[app.id].values[field.key]));

  useEffect(() => {
    if (!install || !available) return;
    let current = true;
    api.call('resource.list', {}).then((items) => {
      if (current) { setResources(items); setResourceError(''); }
    }).catch((cause: unknown) => {
      if (current) setResourceError(faultText(cause));
    }).finally(() => { if (current) setResourcesLoading(false); });
    return () => { current = false; };
  }, [api, available, install]);

  useEffect(() => {
    if (review) reviewRef.current?.focus();
  }, [review]);

  function invalidate() {
    setAcknowledged(false);
    setError('');
  }

  function changeOptions(id: string, patch: Partial<AppOptions>) {
    setOptions((current) => ({ ...current, [id]: { ...current[id], ...patch } }));
    invalidate();
  }

  function toggleHost(id: string, checked: boolean) {
    setHostIds((previous) => {
      const next = new Set(previous);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
    invalidate();
  }

  function resourceChoice(app: AppEntry, target: Host, version: string) {
    const matches = matchingResources(resources, app, version, target);
    const key = JSON.stringify([target.id, app.id, version]);
    const picked = pickedResources[key];
    const resource = matches.find((item) => item.id === picked) ?? (matches.length === 1 ? matches[0] : undefined);
    return { matches, key, resource };
  }

  async function validate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !available) return;
    setError('');
    if (!targets.length) { setError('请至少选择一台可用主机。'); return; }
    if (batch && targetCount > MAX_BATCH) { setError(`一次最多可提交 ${MAX_BATCH} 项任务，请减少主机或组件。`); return; }
    if (modifying && !acknowledged) { setError('请先核对目标、版本与操作影响。'); return; }
    const items: PlanItem[] = [];
    for (const target of targets) {
      const ineligible = hostEligibility(target);
      if (ineligible) { setError(`${target.name}：${ineligible}`); return; }
      for (const app of apps) {
        const config = options[app.id];
        const resource = install && config.source === 'offline' ? resourceChoice(app, target, config.version).resource : undefined;
        const op: Operation = { kind, hostId: target.id, appId: app.id, requestKey: requestKey() };
        if (config.version) op.version = config.version;
        if (modifying) op.confirmed = true;
        if (uninstall) op.keepData = keepData;
        if (install) {
          op.sourceMode = config.source;
          if (resource) op.resourceId = resource.id;
          op.parameters = Object.fromEntries(Object.entries(config.values).filter(([, value]) => value !== ''));
        }
        items.push({ op, app, host: target, resource });
      }
    }
    setBusy('validate');
    const entries: ReviewedItem[] = [];
    try {
      // Bounded batches leave capacity on the shared RPC pipe for other panes.
      for (let i = 0; i < items.length; i += 4) {
        const replies = await Promise.all(items.slice(i, i + 4).map(async (item): Promise<ReviewedItem> => {
          try {
            const result = await api.call('operation.validate', item.op);
            if (result.ok && item.op.sourceMode === 'offline' && !item.resource) {
              return { item, ok: false, summary: '尚未核对具体离线资源；请返回选择与远端系统、版本和架构匹配的资源。' };
            }
            return { item, ok: result.ok, summary: faultText(result.summary || (result.ok ? '通过预检' : '预检未通过'), secrets) };
          } catch (cause) {
            return { item, ok: false, summary: faultText(cause, secrets) };
          }
        }));
        entries.push(...replies);
      }
      setReview(entries);
    } finally { setBusy(undefined); }
  }

  async function start() {
    if (!review || !allPassed || !available || busy || submissionUnknown) return;
    if (review.some(({ item }) => !sameTarget(item.host, hosts.find((target) => target.id === item.host.id)))) {
      setError('预检后目标主机的连接信息已变化。请返回修改并重新预检。');
      return;
    }
    setError('');
    setBusy('submit');
    const items = review.map((entry) => entry.item.op);
    try {
      if (batch) {
        const result = await api.call('operation.batch', { items });
        onStarted(result.taskIds, result.batchId);
      } else {
        const result = await api.call('operation.start', items[0]);
        onStarted([result.taskId]);
      }
    } catch (cause) {
      const code = cause && typeof cause === 'object' && 'code' in cause ? cause.code : undefined;
      const unknown = !code || ['BRIDGE_UNAVAILABLE', 'CORE_UNAVAILABLE', 'CORE_TIMEOUT', 'TIMEOUT', 'INTERNAL', 'BAD_FRAME'].includes(String(code));
      setSubmissionUnknown(unknown);
      setError(`${unknown ? '未收到任务提交确认。请先查看任务队列，不要重复提交。' : ''}${faultText(cause, secrets)}`);
    } finally { setBusy(undefined); }
  }

  function returnToOptions() {
    setReview(undefined);
    setSubmissionUnknown(false);
    invalidate();
  }

  return <Modal title={title} description="先核对目标、版本与影响。预检不会安装或卸载应用。" onDismiss={onDismiss} busy={Boolean(busy)} className="operation-dialog">
    <ol className="rd-steps" aria-label="任务提交步骤"><li aria-current={!review ? 'step' : undefined}><span>1</span>目标与参数</li><li aria-current={review ? 'step' : undefined}><span>2</span>预检与确认</li></ol>
    <div className="rd-dialog-body">
      {!available && <Notice tone="warn">本地核心不可用，暂时无法预检或提交任务。</Notice>}
      {!review ? <form id={formId} className="rd-form" onSubmit={(event) => void validate(event)} aria-busy={Boolean(busy)}>
        <fieldset className="rd-fieldset" disabled={Boolean(busy)}>
          <legend>目标主机</legend>
          {batch ? <div className="target-picker">{hosts.map((item) => {
            const reason = hostEligibility(item);
            return <label key={item.id} className="rd-check target-choice">
              <input type="checkbox" checked={hostIds.has(item.id)} disabled={Boolean(reason)} onChange={(e) => toggleHost(item.id, e.currentTarget.checked)} />
              <span><strong>{item.name}</strong><small><code>{endpoint(item)}</code> / {osLabel(item.os, item.osVersion)} / <code>{item.arch || '架构未知'}</code>{reason && ` / ${reason}`}</small></span>
            </label>;
          })}</div> : <div className="single-target"><Server size={17} aria-hidden="true" /><div><strong>{host.name}</strong><small><code>{endpoint(host)}</code> / {osLabel(host.os, host.osVersion)} / <code>{host.arch || '架构未知'}</code></small></div></div>}
          {batch && <p className="rd-help">{targets.length} 台主机、{apps.length} 个组件，共 {targetCount} 项任务。{install ? '安装任务会依次执行。' : '同一主机的任务会依次执行。'}</p>}
        </fieldset>
        {uninstall && <div className="rd-form">
          <label className="rd-check"><input type="checkbox" checked={keepData} disabled={Boolean(busy)} onChange={(e) => { setKeepData(e.currentTarget.checked); invalidate(); }} />保留应用数据</label>
          <Notice tone="warn">{keepData ? '仅当预检能限定卸载范围时才允许继续，不会自动改为清除数据。' : '未要求保留应用数据。请先备份，并在预检中核对实际影响。'}</Notice>
        </div>}
        {install && resourcesLoading && available && <p className="rd-loading"><Spinner />正在查找本地离线资源…</p>}
        {install && resourceError && <Notice tone="error">离线资源读取失败：{resourceError}</Notice>}
        {apps.map((app) => {
          const config = options[app.id];
          return <fieldset key={app.id} className="rd-fieldset" disabled={Boolean(busy)}>
            <legend>{app.label}</legend>
            <div className="rd-fields">
              <label>应用版本<select value={config.version} disabled={!app.versions.length} onChange={(e) => changeOptions(app.id, { version: e.currentTarget.value })}>
                {(!install || !app.versions.length) && <option value="">{kind === 'detect' ? '检测已装版本' : '由核心确定'}</option>}{app.versions.map((version) => <option key={version}>{version}</option>)}
              </select></label>
              {install && <label>安装来源<select value={config.source} onChange={(e) => changeOptions(app.id, { source: e.currentTarget.value === 'online' ? 'online' : 'offline' })}>
                <option value="offline">离线资源</option>{app.supportsOnline && <option value="online">显式在线</option>}
              </select></label>}
            </div>
            {install && config.source === 'online' && <Notice tone="warn">目标主机需要联网。预检会核对该远端组合与来源；失败不会切换来源。</Notice>}
            {install && config.source === 'offline' && !resourcesLoading && targets.map((target) => {
              const { matches, key, resource } = resourceChoice(app, target, config.version);
              return <div className="host-resource" key={key}>
                {matches.length ? <label>{batch ? `${target.name}的离线资源` : '匹配的离线资源'}
                  <select value={resource?.id ?? ''} onChange={(e) => { setPickedResources((previous) => ({ ...previous, [key]: e.currentTarget.value })); invalidate(); }} required>
                    {!resource && <option value="">请选择资源</option>}{matches.map((match) => <option key={match.id} value={match.id}>{resourceLabel(match)} / {match.digest.slice(0, 12)}</option>)}
                  </select>
                </label> : <div className="resource-missing"><span>{batch && `${target.name}：`}没有匹配远端系统、版本与架构的资源。</span>
                  <button className="rd-button" type="button" onClick={onResources} disabled={!available}><ArrowDownToLine size={15} aria-hidden="true" />前往离线资源</button></div>}
              </div>;
            })}
            {install && app.params.length > 0 && <div className="rd-fields">{app.params.map((field) => <ParameterField key={field.key} field={field} value={config.values[field.key]}
              onChange={(value) => changeOptions(app.id, { values: { ...config.values, [field.key]: value } })} />)}</div>}
          </fieldset>;
        })}
        {modifying && <label className="rd-check"><input type="checkbox" checked={acknowledged} disabled={Boolean(busy)} onChange={(e) => setAcknowledged(e.currentTarget.checked)} />已核对目标主机、版本、来源及操作影响</label>}
      </form> : <section className="preflight-review" aria-labelledby={`${formId}-result`}>
        <h3 id={`${formId}-result`} tabIndex={-1} ref={reviewRef}><ClipboardCheck size={18} aria-hidden="true" />预检结果</h3>
        <Notice tone={allPassed ? 'good' : 'warn'}>{allPassed ? `全部 ${review.length} 项通过；确认后才会提交任务。` : `${review.filter((entry) => !entry.ok).length} 项未通过，当前不会提交任何任务。`}</Notice>
        <ol className="preflight-list">{review.map(({ item, ok, summary }) => <li key={item.op.requestKey}>
          <header><strong>{item.host.name} / {item.app.label}</strong><Badge tone={ok ? 'good' : 'error'}>{ok ? '已通过' : '未通过'}</Badge></header>
          <p className="preflight-target"><code>{endpoint(item.host)}</code> / {osLabel(item.host.os, item.host.osVersion)} / <code>{item.host.arch || '架构未知'}</code> / <code>{item.op.version || '由核心确定'}</code></p>
          {item.resource && <p>离线：{resourceLabel(item.resource)} <code>{item.resource.digest}</code></p>}
          {item.op.sourceMode === 'online' && <p>显式在线：需要远端联网。</p>}
          {uninstall && <p>{item.op.keepData ? '要求保留应用数据' : '未要求保留应用数据'}</p>}
          {install && <dl className="preflight-params">{item.app.params.filter((field) => item.op.parameters?.[field.key] !== undefined).map((field) => <div key={field.key}>
            <dt>{field.label}</dt><dd>{reviewParameter(field, item.op.parameters?.[field.key])}</dd>
          </div>)}</dl>}
          <p className="preflight-summary"><PackageCheck size={16} aria-hidden="true" />{summary}</p>
        </li>)}</ol>
      </section>}
      {error && <Notice tone="error">{error}</Notice>}
    </div>
    <footer className="rd-dialog-footer">
      {!review && <button className="rd-button" data-focus-initial disabled={Boolean(busy)} onClick={onDismiss}>取消</button>}
      {review && !submissionUnknown && <button className="rd-button" disabled={Boolean(busy)} onClick={returnToOptions}><ArrowLeft size={16} aria-hidden="true" />返回修改</button>}
      {!review ? <button className="rd-button primary" type="submit" form={formId} disabled={!available || Boolean(busy) || (install && resourcesLoading)}>
        {busy === 'validate' ? <Spinner /> : <ClipboardCheck size={16} aria-hidden="true" />}{busy === 'validate' ? '正在预检…' : '运行预检'}
      </button> : submissionUnknown ? <button className="rd-button primary" onClick={onUncertain}>查看任务队列</button> : <button className={`rd-button ${uninstall ? 'danger' : 'primary'}`}
        disabled={!allPassed || !available || Boolean(busy)} onClick={() => void start()}>{busy === 'submit' ? <Spinner /> : <Play size={16} aria-hidden="true" />}{busy === 'submit' ? '正在提交…' : `确认${kindLabel[kind]}`}</button>}
    </footer>
  </Modal>;
}
