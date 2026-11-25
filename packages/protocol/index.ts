export const VERSION = 1 as const;
export const MAX_FRAME = 2 * 1024 * 1024;
export type Theme = 'dark' | 'light' | 'system';
export interface Host {
  id: string; name: string; address: string; port: number; username: string; group: string;
  auth: 'password' | 'key'; os: string; osVersion: string; arch: string; fingerprint: string;
  credentialReady: boolean; lastSeen?: string;
}
export interface HostDraft {
  id?: string; name: string; address: string; port: number; username: string; group: string;
  auth: 'password' | 'key'; secret?: string; keyToken?: string; remember?: boolean;
}
export type Probe = { state: 'trust'; fingerprint: string; changed: boolean } |
  { state: 'ready'; probeId: string; host: Host; sftp: boolean };
export interface Settings {
  theme: Theme; connectionTimeoutSeconds: number; retryCount: number; retryIntervalSeconds: number;
  maxConcurrentHosts: number; repositoryUrl: string; updateCheckUrl: string; proxyUrl: string;
  cacheLimitMiB: number; repositoryTokenSet?: boolean; proxyPasswordSet?: boolean;
}
export interface AppParameter {
  key: string; label: string; type: 'text' | 'number' | 'secret' | 'boolean' | 'select';
  default?: string; required?: boolean; options?: string[]; hint?: string;
}
export interface AppEntry {
  id: string; label: string; family?: string; description: string; category: string;
  versions: string[]; params: AppParameter[]; platforms: string[]; supportsOnline: boolean;
}
export type Kind = 'detect' | 'install' | 'uninstall';
export type TaskState = 'queued' | 'preflight' | 'running' | 'verifying' | 'cancelling' |
  'succeeded' | 'succeeded_with_warning' | 'failed' | 'cancelled' | 'interrupted';
export const activeStates: readonly TaskState[] = ['queued','preflight','running','verifying','cancelling'];
export interface AppStatus { installed: boolean | null; running: boolean | null; version?: string; port?: number; residue?: boolean }
export interface Task {
  id: string; hostId: string; appId: string; version: string; kind: Kind; state: TaskState;
  stage: string; percent: number; message: string; createdAt: string; updatedAt: string; seq: number;
  result?: AppStatus; batchId?: string;
}
export interface TaskLog { seq: number; text: string; at: string }
export interface Operation {
  hostId: string; appId: string; version?: string; kind: Kind; requestKey: string;
  sourceMode?: 'offline' | 'online'; resourceId?: string; parameters?: Record<string, string>;
  keepData?: boolean; confirmed?: boolean;
}
export interface OfflineResource {
  id: string; appId: string; version: string; os: string; osVersion: string; arch: string;
  source: string; bytes: number; fileCount: number; digest: string; trust: 'reviewed' | 'signed';
}
export interface FileEntry { name: string; path: string; size: number; directory: boolean; modifiedAt?: string; link?: boolean }
export interface CustomApp {
  id: string; name: string; kind: 'SUPPORT' | 'GENERIC' | 'CUSTOM'; description: string; directory: string;
  frontendDirectory: string; configPath: string; startCommand: string; stopCommand: string;
  statusCommand: string; logsPath: string; enabled: boolean;
}
export interface MigrationPreview {
  previewId: string; totals: Record<string, number>; needsCredentials: number;
  changes: { table: string; key: string; kind: 'add'|'reuse'|'update'|'conflict'|'missing'; label?: string }[];
  warnings: string[];
}
export interface LegacyRecord { kind: string; id: string; summary: string; at: string; detail?: string }
export type UpdateStatus = 'idle' | 'checking' | 'available' | 'up-to-date' | 'downloading' | 'downloaded' | 'error' | 'unsupported';
export interface UpdateState {
  status: UpdateStatus;
  currentVersion: string;
  platform: string;
  arch: string;
  repo: string;
  latestVersion?: string;
  releaseName?: string;
  releaseNotes?: string;
  releaseUrl?: string;
  publishedAt?: string;
  assetName?: string;
  assetSize?: number;
  downloadedBytes?: number;
  percent?: number;
  verified?: boolean;
  canInstall: boolean;
  message: string;
}
export interface CoreEvent {
  v: 1; type: 'event'; topic: string; taskId?: string; sessionId?: string; seq?: number; data: unknown;
}
export type Empty = Record<string, never>;
type HostPath = {hostId: string; path: string};
export interface ElectronMethods {
  'core.status': [Empty, {ready: boolean; version?: string; platform: string; arch: string; detail?: string}];
  'dialog.pick': [{purpose: 'key'|'upload'|'uploadDirectory'|'download'|'resource'|'migration'|'export'}, {token: string; name: string}|null];
  'dialog.export': [{token: string; text: string}, {ok: boolean}];
  'update.status': [Empty, UpdateState];
  'update.check': [Empty, UpdateState];
  'update.download': [Empty, UpdateState];
  'update.cancel': [Empty, UpdateState];
  'update.install': [Empty, {ok: boolean}];
}
export interface CoreMethods {
  'host.list': [Empty, Host[]];
  'host.probe': [{host?: HostDraft; hostId?: string; acceptFingerprint?: string; replaceFingerprint?: boolean}, Probe];
  'host.save': [{probeId: string}, Host];
  'host.delete': [{id: string}, {ok: boolean}];
  'catalog.list': [Empty, AppEntry[]];
  'operation.validate': [Operation, {ok: boolean; summary: string}];
  'operation.start': [Operation, {taskId: string}];
  'operation.batch': [{items: Operation[]}, {batchId: string; taskIds: string[]}];
  'operation.list': [{limit?: number; offset?: number; hostId?: string; appId?: string; kind?: Kind; state?: TaskState}, Task[]];
  'operation.get': [{taskId: string; afterSeq?: number}, {task: Task; logs: TaskLog[]; truncated?: boolean}];
  'operation.cancel': [{taskId: string}, {ok: boolean}];
  'operation.clearLogs': [{taskId: string}, {ok: boolean}];
  'resource.list': [Empty, OfflineResource[]];
  'resource.inspect': [{token: string}, OfflineResource];
  'resource.import': [{token: string; digest: string}, OfflineResource];
  'terminal.open': [{hostId: string; rows: number; cols: number}, {sessionId: string}];
  'terminal.input': [{sessionId: string; bytesB64: string}, {ok: boolean}];
  'terminal.resize': [{sessionId: string; rows: number; cols: number}, {ok: boolean}];
  'terminal.close': [{sessionId: string}, {ok: boolean}];
  'files.list': [HostPath, {path: string; entries: FileEntry[]}];
  'files.read': [HostPath, {content: string; digest: string}];
  'files.write': [HostPath & {content: string; expectedDigest: string; confirmed: boolean}, {digest: string}];
  'files.mkdir': [HostPath, {ok: boolean}];
  'files.rename': [HostPath & {to: string; confirmed: boolean}, {ok: boolean}];
  'files.delete': [HostPath & {recursive?: boolean; confirmed: boolean}, {ok: boolean}];
  'files.upload': [HostPath & {token: string; overwrite?: boolean; confirmed: boolean}, {ok: boolean}];
  'files.download': [HostPath & {token: string}, {ok: boolean}];
  'files.transfer': [{sourceHostId: string; sourcePath: string; targetHostId: string; targetPath: string; mode: 'copy'|'move'; overwrite?: boolean; confirmed: boolean}, {ok: boolean; files: number; dirs: number; bytes: number}];
  'files.archive': [HostPath & {destination: string; overwrite?: boolean; confirmed: boolean}, {ok: boolean; path: string; files: number; bytes: number}];
  'files.extract': [HostPath & {destination: string; overwrite?: boolean; confirmed: boolean}, {ok: boolean; path: string; files: number; dirs: number}];
  'custom.list': [Empty, CustomApp[]];
  'custom.save': [CustomApp, CustomApp];
  'custom.delete': [{id: string}, {ok: boolean}];
  'custom.run': [{hostId: string; appId: string; action: 'start'|'stop'|'restart'|'status'|'upload'|'uploadFrontend'|'uploadAndStart'; token?: string; confirmed: boolean}, {output: string}];
  'settings.get': [Empty, Settings];
  'settings.save': [Settings & {repositoryToken?: string; proxyPassword?: string}, Settings];
  'settings.updateCheck': [Empty, {current: string; latest?: string; available: boolean; message: string}];
  'migration.preview': [{token: string}, MigrationPreview];
  'migration.apply': [{previewId: string; hostsOnly?: boolean; acceptUpdates?: boolean}, {imported: Record<string, number>; skipped: Record<string, number>; message: string}];
  'migration.history': [{limit?: number; offset?: number}, LegacyRecord[]];
}
export const coreMethods = [
  'host.list','host.probe','host.save','host.delete','catalog.list',
  'operation.validate','operation.start','operation.batch','operation.list','operation.get','operation.cancel','operation.clearLogs',
  'resource.list','resource.inspect','resource.import',
  'terminal.open','terminal.input','terminal.resize','terminal.close',
  'files.list','files.read','files.write','files.mkdir','files.rename','files.delete','files.upload','files.download','files.transfer','files.archive','files.extract',
  'custom.list','custom.save','custom.delete','custom.run',
  'settings.get','settings.save','settings.updateCheck','migration.preview','migration.apply','migration.history',
] as const satisfies readonly (keyof CoreMethods)[];
export type Methods = CoreMethods & ElectronMethods;
export interface Bridge {
  call<K extends keyof Methods>(method: K, params: Methods[K][0]): Promise<Methods[K][1]>;
  subscribe(listener: (event: CoreEvent) => void): () => void;
}
export interface Fault { code: string; message: string; retryable: boolean }
export type Result = { ok: true; result: unknown } | { ok: false; error: Fault };
