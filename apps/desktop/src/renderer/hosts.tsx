import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { ArrowLeft, ChevronDown, ChevronRight, FileKey, Fingerprint, Folder, Plus, Search, Server, ShieldCheck, Trash2 } from 'lucide-react';
import type { Bridge, Host, HostDraft, Probe } from '@relaydock/protocol';
import { endpoint, faultText, hostEligibility, osLabel } from './core';
import { Badge, EmptyState, Modal, Notice, Spinner } from './ui';

export function HostRail({ hosts, selectedId, ready, loading, error, onAdd, onSelect, onOpenTerminal, tools }: {
  hosts: Host[]; selectedId?: string; ready: boolean; loading: boolean; error: string;
  onAdd: () => void; onSelect: (id: string) => void; onOpenTerminal?: (id: string) => void; tools: ReactNode;
}) {
  const [query, setQuery] = useState('');
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const groups = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const result = new Map<string, Host[]>();
    for (const host of hosts) {
      if (needle && ![host.name, host.address, host.username, host.group, host.os].some((value) => value.toLowerCase().includes(needle))) continue;
      const group = host.group.trim() || '未分组';
      const members = result.get(group);
      if (members) members.push(host);
      else result.set(group, [host]);
    }
    return [...result.entries()].sort(([a], [b]) => a.localeCompare(b, 'zh-CN'));
  }, [hosts, query]);

  function toggleGroup(group: string) {
    setCollapsed((previous) => {
      const next = new Set(previous);
      if (next.has(group)) next.delete(group);
      else next.add(group);
      return next;
    });
  }

  return <aside className="host-rail" aria-label="主机与工具">
    <div className="host-rail-header">
      <div className="rail-title"><Server size={17} aria-hidden="true" /><h2>主机库</h2><span className="rail-count" aria-label={`${hosts.length} 台主机`}>{hosts.length}</span></div>
      <button className="rd-icon add-rail" aria-label="添加主机" title={ready ? '添加主机' : '桌面核心未就绪'} disabled={!ready} onClick={onAdd}><Plus size={19} aria-hidden="true" /></button>
    </div>
    <label className="rd-search host-search"><Search size={16} aria-hidden="true" /><input aria-label="搜索主机" placeholder="名称、地址或分组" value={query} onChange={(e) => setQuery(e.currentTarget.value)} /></label>
    <div className="host-groups" aria-busy={loading}>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && !hosts.length ? <p className="rd-loading"><Spinner />正在读取主机…</p> : null}
      {!loading && !error && !groups.length && <EmptyState icon={<Server size={19} />} title={query.trim() ? '没有匹配的主机' : ready ? '暂无主机' : '主机库不可用'}>
        {query.trim() ? '试试其他名称、地址或分组。' : ready ? '添加主机后，可按分组查找。' : '桌面核心就绪后显示本机保存的主机。'}
      </EmptyState>}
      {groups.map(([group, members]) => {
        const open = Boolean(query.trim()) || !collapsed.has(group);
        return <section key={group} className="host-group">
          <button className="host-group-toggle" aria-expanded={open} disabled={Boolean(query.trim())} onClick={() => toggleGroup(group)}>
            {open ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
            <Folder size={15} aria-hidden="true" /><span>{group}</span><small>{members.length}</small>
          </button>
          {open && <ul className="host-list">{members.map((host) => {
            const restriction = hostEligibility(host);
            return <li key={host.id} className="host-list-item">
              <button className="host-row" aria-current={selectedId === host.id ? 'true' : undefined} title={`${host.name} (${endpoint(host)})`} onClick={() => onSelect(host.id)}>
                <Server size={16} aria-hidden="true" /><span className="host-row-label"><strong>{host.name}</strong><small>{endpoint(host)}</small></span>
                {restriction && <span className="host-attention" title={restriction}><span className="sr-only">{restriction}</span></span>}
              </button>
              {onOpenTerminal && !restriction && <button type="button" className="host-row-open" aria-label={`为 ${host.name} 新开终端`} title={`为 ${host.name} 新开终端标签`}
                onClick={() => onOpenTerminal(host.id)}><Plus size={14} aria-hidden="true" /></button>}
            </li>;
          })}</ul>}
        </section>;
      })}
    </div>
    {tools}
  </aside>;
}

function initialDraft(host?: Host): HostDraft {
  return host ? {
    id: host.id, name: host.name, address: host.address, port: host.port, username: host.username, group: host.group, auth: host.auth,
  } : { name: '', address: '', port: 22, username: '', group: '', auth: 'password' };
}

function normalizeDraft(draft: HostDraft): HostDraft {
  return {
    name: draft.name.trim(), address: draft.address.trim().replace(/^\[([^\]]+)\]$/, '$1'),
    username: draft.username.trim(), group: draft.group.trim(), port: draft.port, auth: draft.auth,
    ...(draft.id ? { id: draft.id } : {}), ...(draft.secret ? { secret: draft.secret } : {}),
    ...(draft.auth === 'key' && draft.keyToken ? { keyToken: draft.keyToken } : {}),
  };
}

export function HostDialog({ api, host, available, onCancel, onSaved }: {
  api: Bridge; host?: Host; available: boolean; onCancel: () => void; onSaved: (host: Host) => void;
}) {
  const formId = useId();
  const stepRef = useRef<HTMLHeadingElement>(null);
  const [draft, setDraft] = useState<HostDraft>(() => initialDraft(host));
  const [keyName, setKeyName] = useState('');
  const [probe, setProbe] = useState<Probe>();
  const [trusted, setTrusted] = useState(false);
  const [pending, setPending] = useState<'key' | 'probe' | 'save'>();
  const [error, setError] = useState('');
  const normalized = normalizeDraft(draft);
  const sameTarget = Boolean(host && normalized.address.toLowerCase() === host.address.toLowerCase() && normalized.port === host.port);
  const canReuse = Boolean(host && sameTarget && host.credentialReady && normalized.username === host.username && draft.auth === host.auth);
  const hasNewCredential = draft.auth === 'password' ? Boolean(draft.secret) : Boolean(draft.keyToken);
  const hasReplacement = hasNewCredential || (draft.auth === 'key' && Boolean(draft.secret));

  useEffect(() => {
    if (probe) stepRef.current?.focus();
  }, [probe]);

  function change(next: Partial<HostDraft>) {
    setDraft((previous) => ({ ...previous, ...next }));
    setError('');
  }

  function returnToForm() {
    setProbe(undefined);
    setTrusted(false);
    setError('');
  }

  async function pickKey() {
    setError('');
    setPending('key');
    try {
      const file = await api.call('dialog.pick', { purpose: 'key' });
      if (file) {
        change({ keyToken: file.token });
        setKeyName(file.name);
      }
    } catch (cause) {
      setError(faultText(cause, [draft.secret, draft.keyToken]));
    } finally { setPending(undefined); }
  }

  async function runProbe(fingerprint?: Extract<Probe, { state: 'trust' }>) {
    if (!available || pending) return;
    setError('');
    if (!normalized.name || !normalized.address || /[\s/\\]/.test(normalized.address) || !normalized.username || !Number.isInteger(normalized.port) || normalized.port < 1 || normalized.port > 65535) {
      setError('请输入主机名称、有效的服务器地址、端口和登录用户名。');
      return;
    }
    if (!canReuse && !hasNewCredential) {
      setError(draft.auth === 'password' ? '请输入连接密码。' : '请选择本机的私钥文件。');
      return;
    }
    if (fingerprint && !trusted) return;
    setPending('probe');
    const testedDraft = { ...normalized, ...(hasReplacement ? { remember: draft.remember ?? false } : {}) };
    try {
      const result = await api.call('host.probe', fingerprint ? {
        host: testedDraft, acceptFingerprint: fingerprint.fingerprint, replaceFingerprint: fingerprint.changed,
      } : { host: testedDraft });
      setProbe(result);
      setTrusted(false);
    } catch (cause) {
      setError(faultText(cause, [draft.secret, draft.keyToken]));
    } finally { setPending(undefined); }
  }

  async function save() {
    if (!available || pending || probe?.state !== 'ready') return;
    setPending('save');
    setError('');
    try { onSaved(await api.call('host.save', { probeId: probe.probeId })); }
    catch (cause) { setError(faultText(cause, [draft.secret, draft.keyToken])); }
    finally { setPending(undefined); }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void runProbe();
  }

  return <Modal title={host ? '编辑并测试主机' : '添加主机'} description="连接测试成功后，才能保存主机。" onDismiss={onCancel} busy={Boolean(pending)} className="host-dialog">
    <ol className="rd-steps" aria-label="添加主机步骤">
      <li aria-current={!probe ? 'step' : undefined}><span>1</span>连接信息</li>
      <li aria-current={probe?.state === 'trust' ? 'step' : undefined}><span>2</span>指纹核对</li>
      <li aria-current={probe?.state === 'ready' ? 'step' : undefined}><span>3</span>测试与保存</li>
    </ol>
    <div className="rd-dialog-body">
      {!available && <Notice tone="warn">本地核心不可用。恢复后可继续。</Notice>}
      {!probe && <form id={formId} onSubmit={submit} className="rd-form" aria-busy={Boolean(pending)}>
        <div className="rd-fields">
          <label>主机名称<input data-focus-initial required maxLength={128} value={draft.name} onChange={(e) => change({ name: e.currentTarget.value })} placeholder="如：调试服务器" disabled={Boolean(pending)} /></label>
          <label>分组<input maxLength={128} value={draft.group} onChange={(e) => change({ group: e.currentTarget.value })} placeholder="留空归入未分组" disabled={Boolean(pending)} /></label>
          <label>服务器地址<input required maxLength={253} value={draft.address} onChange={(e) => change({ address: e.currentTarget.value })} placeholder="主机名或 IP（不含端口）" disabled={Boolean(pending)} /></label>
          <label>SSH 端口<input type="number" required min={1} max={65535} value={Number.isNaN(draft.port) ? '' : draft.port} onChange={(e) => change({ port: e.currentTarget.valueAsNumber })} disabled={Boolean(pending)} /></label>
          <label>登录用户名<input required maxLength={128} autoComplete="username" value={draft.username} onChange={(e) => change({ username: e.currentTarget.value })} disabled={Boolean(pending)} /></label>
          <label>认证方式<select value={draft.auth} onChange={(e) => { setKeyName(''); change({ auth: e.currentTarget.value === 'key' ? 'key' : 'password', secret: undefined, keyToken: undefined, remember: undefined }); }} disabled={Boolean(pending)}><option value="password">密码</option><option value="key">SSH 私钥</option></select></label>
        </div>
        {draft.auth === 'key' && <div className="key-picker">
          <button type="button" className="rd-button" disabled={!available || Boolean(pending)} onClick={() => void pickKey()}>{pending === 'key' ? <Spinner /> : <FileKey size={16} aria-hidden="true" />}选择私钥</button>
          <span>{keyName || (canReuse ? '未选择新私钥，继续使用现有凭据' : '尚未选择本机文件')}</span>
        </div>}
        <label>{draft.auth === 'key' ? '私钥口令（可选）' : '连接密码'}
          <input type="password" autoComplete="new-password" required={draft.auth === 'password' && !canReuse} disabled={Boolean(pending)} value={draft.secret ?? ''}
            placeholder={canReuse && draft.auth === 'password' ? '留空继续使用现有凭据' : undefined} onChange={(e) => change({ secret: e.currentTarget.value })} />
        </label>
        {(!canReuse || hasReplacement) && <label className="rd-check">
          <input type="checkbox" checked={draft.remember ?? false} disabled={Boolean(pending)} onChange={(e) => change({ remember: e.currentTarget.checked })} />保存到系统凭据库
        </label>}
        <p className="rd-help">{canReuse && !hasReplacement ? '已有凭据不会回显。' : '不勾选时，新凭据仅用于本次会话。'}</p>
      </form>}
      {probe?.state === 'trust' && <div className="trust-review">
        <h3 ref={stepRef} tabIndex={-1}>核对远端身份</h3>
        <Notice tone={probe.changed ? 'warn' : 'info'}>{probe.changed
          ? `${sameTarget ? '远端指纹已变更，连接被阻止。' : '连接目标已变更。'}请通过服务器控制台或管理员核对新指纹后，再明确替换。`
          : '首次连接必须核对 SSH 指纹。请与服务器控制台或管理员提供的指纹比对，不要仅凭本窗口接受。'}</Notice>
        <dl className="review-data">
          <div><dt>连接目标</dt><dd><code>{normalized.username}@{endpoint(normalized)}</code></dd></div>
          {probe.changed && host?.fingerprint && <div><dt>原指纹</dt><dd><code>{host.fingerprint}</code></dd></div>}
          <div><dt>{probe.changed ? '新指纹' : 'SSH 指纹'}</dt><dd><Fingerprint size={16} aria-hidden="true" /><code>{probe.fingerprint}</code></dd></div>
        </dl>
        <label className="rd-check"><input type="checkbox" checked={trusted} disabled={Boolean(pending)} onChange={(e) => setTrusted(e.currentTarget.checked)} />已通过独立渠道核对此指纹</label>
      </div>}
      {probe?.state === 'ready' && <div className="trust-review">
        <h3 ref={stepRef} tabIndex={-1}>SSH 测试成功</h3>
        <Notice tone="good">请核对远端系统与架构后保存主机。</Notice>
        <dl className="review-data">
          <div><dt>目标</dt><dd>{probe.host.name} <code>{endpoint(probe.host)}</code></dd></div>
          <div><dt>远端系统</dt><dd>{osLabel(probe.host.os, probe.host.osVersion)}<code>{probe.host.arch || '架构未知'}</code></dd></div>
          <div><dt>SSH 指纹</dt><dd><ShieldCheck size={16} aria-hidden="true" /><code>{probe.host.fingerprint || '未返回'}</code></dd></div>
          <div><dt>远端文件</dt><dd><Badge tone={probe.sftp ? 'good' : 'warn'}>{probe.sftp ? 'SFTP 测试成功' : 'SFTP 不可用'}</Badge></dd></div>
        </dl>
        {!probe.sftp && <p className="rd-help">可以保存主机并使用 SSH；需要 SFTP 的文件传输可能不可用。</p>}
      </div>}
      {error && <Notice tone="error">{error}</Notice>}
    </div>
    <footer className="rd-dialog-footer">
      {probe ? <button className="rd-button" disabled={Boolean(pending)} onClick={returnToForm}><ArrowLeft size={16} aria-hidden="true" />返回编辑</button>
        : <button className="rd-button" disabled={Boolean(pending)} onClick={onCancel}>取消</button>}
      {!probe && <button className="rd-button primary" type="submit" form={formId} disabled={!available || Boolean(pending)}>{pending === 'probe' && <Spinner />}{pending === 'probe' ? '正在检查…' : '测试连接'}</button>}
      {probe?.state === 'trust' && <button className={`rd-button ${probe.changed ? 'danger' : 'primary'}`} disabled={!trusted || !available || Boolean(pending)} onClick={() => void runProbe(probe)}>
        {pending === 'probe' && <Spinner />}{pending === 'probe' ? '正在测试…' : probe.changed ? '替换指纹并测试' : '信任指纹并测试'}
      </button>}
      {probe?.state === 'ready' && <button className="rd-button primary" disabled={!available || Boolean(pending)} onClick={() => void save()}>{pending === 'save' && <Spinner />}保存主机</button>}
    </footer>
  </Modal>;
}

export function RemoveHostDialog({ api, host, available, onCancel, onRemoved }: {
  api: Bridge; host: Host; available: boolean; onCancel: () => void; onRemoved: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function remove() {
    setBusy(true);
    setError('');
    try {
      const reply = await api.call('host.delete', { id: host.id });
      if (!reply.ok) throw new Error('未能移除主机。');
      onRemoved();
    } catch (cause) { setError(faultText(cause)); }
    finally { setBusy(false); }
  }

  return <Modal title="移除主机？" description="仅移除本地记录，不会连接服务器或卸载应用。" onDismiss={onCancel} busy={busy}>
    <div className="rd-dialog-body">
      <dl className="review-data"><div><dt>本地主机记录</dt><dd>{host.name}<code>{endpoint(host)}</code></dd></div></dl>
      {error && <Notice tone="error">{error}</Notice>}
    </div>
    <footer className="rd-dialog-footer">
      <button className="rd-button" data-focus-initial disabled={busy} onClick={onCancel}>取消</button>
      <button className="rd-button danger" disabled={!available || busy} onClick={() => void remove()}>{busy ? <Spinner /> : <Trash2 size={16} aria-hidden="true" />}移除主机</button>
    </footer>
  </Modal>;
}
