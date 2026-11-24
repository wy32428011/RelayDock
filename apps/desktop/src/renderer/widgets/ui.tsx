import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { AlertTriangle, CheckCircle2, Info, Server, X } from 'lucide-react';
import type { Bridge, Host } from '@relaydock/protocol';

export function useLocalQuery<T>(load: () => Promise<T>) {
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<{ load: typeof load; revision: number; data?: T; error?: string }>();
  useEffect(() => {
    let current = true;
    void load().then(data => {
      if (current) setResult({ load, revision, data });
    }, error => {
      if (current) setResult({ load, revision, error: faultText(error) });
    });
    return () => { current = false; };
  }, [load, revision]);
  const refresh = useCallback(() => setRevision(old => old + 1), []);
  const valid = result?.load === load && result.revision === revision;
  return { data: valid ? result.data : undefined, error: valid ? result.error : undefined, pending: !valid, refresh };
}

export async function exportText(api: Bridge, text: string, current: () => boolean) {
  const picked = await api.call('dialog.pick', { purpose: 'export' });
  if (!picked || !current()) return false;
  requireOk(await api.call('dialog.export', { token: picked.token, text }), '核心未确认导出文件已写入。');
  return true;
}

export type NoticeValue = { tone: 'error' | 'info' | 'success'; text: string };

export function faultText(error: unknown): string {
  if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') {
    const code = 'code' in error && typeof error.code === 'string' ? `（${error.code}）` : '';
    return `${error.message}${code}`;
  }
  return typeof error === 'string' ? error : '操作失败，请检查本地核心或连接后重试。';
}

export function useAction() {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<NoticeValue>();
  const life = useRef({ active: false });
  const locked = useRef(false);
  useLayoutEffect(() => {
    const mounted = { active: true };
    life.current = mounted;
    return () => { mounted.active = false; };
  }, []);
  async function run(work: (current: () => boolean) => Promise<void>) {
    const mounted = life.current;
    if (!mounted.active || locked.current) return;
    locked.current = true;
    setBusy(true);
    setNotice(undefined);
    try {
      await work(() => mounted.active);
    } catch (error) {
      if (mounted.active) setNotice({ tone: 'error', text: faultText(error) });
    } finally {
      locked.current = false;
      if (mounted.active) setBusy(false);
    }
  }
  return { busy, notice, setNotice, run };
}

export function requireOk(result: { ok: boolean }, message: string) {
  if (!result.ok) throw new Error(message);
}

export function HostBadge({ host }: { host: Host }) {
  return <div className="rd-w-host"><Server aria-hidden="true" size={18} /><span><strong>{host.name || host.address}</strong><small>{sshEndpoint(host)}</small></span></div>;
}

export function PaneHeading({ title, description, icon, host }: { title: string; description: string; icon: ReactNode; host?: Host | undefined }) {
  return <header className="rd-w-heading">
    <div className="rd-w-heading-copy">
      <div className="rd-w-heading-title"><span className="rd-w-glyph" aria-hidden="true">{icon}</span><h2>{title}</h2></div>
      <p>{description}</p>
    </div>
    {host && <HostBadge host={host} />}
  </header>;
}

export function Card({ title, subtitle, actions, children, className = '' }: { title: string; subtitle?: string; actions?: ReactNode; children: ReactNode; className?: string }) {
  return <section className={`rd-w-card ${className}`}>
    <header className="rd-w-card-header">
      <div><h3>{title}</h3>{subtitle && <p>{subtitle}</p>}</div>
      {actions && <div className="rd-w-actions">{actions}</div>}
    </header>
    {children}
  </section>;
}

export function Button({ variant = 'quiet', className = '', type = 'button', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'quiet' | 'primary' | 'danger' | 'text' }) {
  return <button {...props} type={type} className={`rd-w-button rd-w-button--${variant} ${className}`} />;
}

export function Field({ label, hint, children, wide = false }: { label: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return <label className={`rd-w-field${wide ? ' rd-w-field--wide' : ''}`}>
    <span>{label}</span>{children}{hint && <small>{hint}</small>}
  </label>;
}

export function Notice({ value }: { value?: NoticeValue | undefined }) {
  if (!value) return null;
  const Icon = value.tone === 'error' ? AlertTriangle : value.tone === 'success' ? CheckCircle2 : Info;
  return <div className={`rd-w-notice rd-w-notice--${value.tone}`} role={value.tone === 'error' ? 'alert' : 'status'}>
    <Icon aria-hidden="true" size={18} /><span>{value.text}</span>
  </div>;
}

export function Empty({ title, description, icon }: { title: string; description?: string; icon?: ReactNode }) {
  return <div className="rd-w-empty">
    {icon && <span className="rd-w-glyph" aria-hidden="true">{icon}</span>}
    <strong>{title}</strong>{description && <p>{description}</p>}
  </div>;
}

export function TargetRequired({ host }: { host?: Host | undefined }) {
  return <Empty icon={<Server size={22} />} title={host ? '这台主机尚不能建立会话' : '请先选择一台主机'}
    description={host ? '请在主机管理中补录凭据并验证 SSH 主机指纹，成功保存后再继续。' : '从主机列表选择本次操作的目标；未选择时不会建立远程连接。'} />;
}

export function canConnect(host: Host | undefined): host is Host {
  return Boolean(host?.credentialReady && host.fingerprint);
}

function sshEndpoint(host: Host) {
  const address = host.address.includes(':') && !host.address.startsWith('[') ? `[${host.address}]` : host.address;
  return `${host.username}@${address}:${host.port}`;
}

export function remoteTarget(host: Host) {
  return `${host.name || host.address} · ${sshEndpoint(host)}`;
}

export type DetailRows = ReadonlyArray<readonly [string, ReactNode]>;

export function DetailList({ items }: { items: DetailRows }) {
  return <dl className="rd-w-details">{items.map(([title, detail]) => <div key={title}><dt>{title}</dt><dd>{detail}</dd></div>)}</dl>;
}

interface Confirmation {
  title: string;
  description: string;
  acceptLabel: string;
  info?: DetailRows;
  content?: ReactNode;
  danger?: boolean;
  perform: () => void;
}

function ConfirmDialog({ request, dismiss }: { request: Confirmation; dismiss: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const trigger = document.activeElement;
    el.showModal();
    cancel.current?.focus();
    return () => {
      if (el.open) el.close();
      if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus();
    };
  }, []);
  return <dialog ref={ref} className="rd-w-dialog" aria-labelledby={titleId} aria-describedby={descriptionId}
    onCancel={event => { event.preventDefault(); dismiss(); }}>
    <div className="rd-w-dialog-header">
      <span className={`rd-w-glyph${request.danger ? ' rd-w-glyph--warning' : ''}`}><AlertTriangle aria-hidden="true" size={22} /></span>
      <div><h3 id={titleId}>{request.title}</h3><p id={descriptionId}>{request.description}</p></div>
      <Button variant="text" aria-label="关闭确认对话框" onClick={dismiss}><X aria-hidden="true" size={18} /></Button>
    </div>
    {request.info && <DetailList items={request.info} />}
    {request.content}
    <footer className="rd-w-actions rd-w-dialog-footer">
      <button ref={cancel} className="rd-w-button rd-w-button--quiet" type="button" onClick={dismiss}>取消</button>
      <Button variant={request.danger ? 'danger' : 'primary'} onClick={() => { dismiss(); request.perform(); }}>{request.acceptLabel}</Button>
    </footer>
  </dialog>;
}

export function useConfirmation() {
  const [request, setRequest] = useState<Confirmation>();
  return { confirm: setRequest, dialog: request ? <ConfirmDialog request={request} dismiss={() => setRequest(undefined)} /> : null };
}

export function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)) - 1);
  return `${(bytes / 1024 ** (i + 1)).toLocaleString('zh-CN', { maximumFractionDigits: 1 })} ${units[i]}`;
}

const dateFormat = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
export function formatDate(value?: string) {
  if (!value) return '—';
  // Only interpret timestamps with explicit timezone; legacy timestamps may be local or ambiguous.
  const at = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(value) ? Date.parse(value) : Number.NaN;
  return Number.isNaN(at) ? value : dateFormat.format(at);
}
