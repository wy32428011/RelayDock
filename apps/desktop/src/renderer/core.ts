import { activeStates } from '@relaydock/protocol';
import type { AppStatus, Bridge, Host, Kind, Methods, Task, TaskState } from '@relaydock/protocol';

export type CoreStatus = Methods['core.status'][1];
export type Tone = 'neutral' | 'info' | 'good' | 'warn' | 'error';

export function readBridge(): Bridge | undefined {
  const bridge = (window as Window & { relayDock?: Bridge }).relayDock;
  return typeof bridge?.call === 'function' && typeof bridge.subscribe === 'function' ? bridge : undefined;
}

export function faultText(error: unknown, secrets: readonly (string | undefined)[] = []): string {
  let message = '操作失败，请检查本地核心状态或重试。';
  if (typeof error === 'string') message = error;
  else if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') message = error.message;
  message = message.replace(/^Error invoking remote method '[^']+':\s*(?:Error:\s*)?/, '');
  for (const secret of secrets) if (secret) message = message.split(secret).join('••••');
  return message || '操作失败，请检查连接后重试。';
}

export function endpoint(host: Pick<Host, 'address' | 'port'>): string {
  const address = host.address.includes(':') && !host.address.startsWith('[') ? `[${host.address}]` : host.address;
  return `${address}:${host.port}`;
}

export function osLabel(os: string, version?: string): string {
  const names: Record<string, string> = { win32: 'Windows', windows: 'Windows', darwin: 'macOS', linux: 'Linux', ubuntu: 'Ubuntu', centos: 'CentOS', debian: 'Debian' };
  return [names[os.toLowerCase()] ?? os, version].filter(Boolean).join(' ') || '系统未知';
}

export function hostEligibility(host: Host): string | undefined {
  if (!host.fingerprint) return '需先测试连接并核对指纹';
  if (!host.credentialReady) return '需补录凭据并测试连接';
}

export function requestKey(): string {
  return typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, '0')).join('');
}

export const kindLabel: Record<Kind, string> = { detect: '检测', install: '安装', uninstall: '卸载' };

export const taskLabels: Record<TaskState, { label: string; tone: Tone }> = {
  queued: { label: '已排队', tone: 'neutral' },
  preflight: { label: '预检中', tone: 'info' },
  running: { label: '执行中', tone: 'info' },
  verifying: { label: '复查中', tone: 'info' },
  cancelling: { label: '正在取消', tone: 'warn' },
  succeeded: { label: '已完成', tone: 'good' },
  succeeded_with_warning: { label: '完成，有告警', tone: 'warn' },
  failed: { label: '失败', tone: 'error' },
  cancelled: { label: '已取消', tone: 'neutral' },
  interrupted: { label: '中断，待复查', tone: 'warn' },
};

// Interpret known stage identifiers at display time, including entries from older profiles.
const scriptStageLabels = new Map([
  ['Initializing', '正在初始化'],
  ['CheckingInstallation', '正在检测安装状态'],
  ['CheckingPorts', '正在检查端口'],
  ['CheckingRabbitmqStatus', '正在检查 RabbitMQ 状态'],
  ['OutputtingStatus', '正在汇总检测结果'],
]);

export function taskStageLabel(stage: string): string {
  if (Object.hasOwn(taskLabels, stage)) return taskLabels[stage as TaskState].label;
  return scriptStageLabels.get(stage) ?? stage;
}

export function isActive(task: Task): boolean {
  return activeStates.includes(task.state);
}

export function statusLabel(status?: AppStatus): { label: string; tone: Tone } {
  if (status?.running === true) return { label: '运行中', tone: 'good' };
  if (status?.installed === true) {
    return status.running === false ? { label: '已安装，未运行', tone: 'info' } : { label: '已安装，运行未知', tone: 'info' };
  }
  if (status?.installed === false) {
    return status.residue ? { label: '未安装，有残留', tone: 'warn' } : { label: '未安装', tone: 'neutral' };
  }
  return { label: '状态未知', tone: 'warn' };
}

export function percent(value: number): number {
  return Number.isFinite(value) ? Math.min(100, Math.max(0, Math.round(value))) : 0;
}

export function timeLabel(value: string, format: 'time' | 'date' = 'time'): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return '时间未知';
  const options: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false };
  if (format === 'date') Object.assign(options, { year: 'numeric', month: '2-digit', day: '2-digit' });
  return date.toLocaleString('zh-CN', options);
}

export function bytesLabel(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GiB`;
}
