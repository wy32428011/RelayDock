import { MAX_FRAME, activeStates, coreMethods, type ElectronMethods, type Methods } from '../../../../packages/protocol/index';
import { desktopMethods } from '../preload/channels';
import { DesktopFault } from './faults';
import { isRecord } from './protocol';

const allowed = new Set<string>([...coreMethods, ...desktopMethods]);
export function isMethod(value: unknown): value is keyof Methods {
  return typeof value === 'string' && allowed.has(value);
}

export const purposes = ['key', 'upload', 'uploadDirectory', 'download', 'resource', 'migration', 'export'] as const satisfies
  readonly ElectronMethods['dialog.pick'][0]['purpose'][];
export type Purpose = typeof purposes[number];
export const uploadActions = ['upload', 'uploadFrontend', 'uploadAndStart'] as const;
export function isUploadAction(value: unknown): boolean {
  return uploadActions.some(action => action === value);
}

type Budget = { remaining: number };
type Rule = (value: unknown, budget: Budget) => boolean;
type Fields = Record<string, Rule>;
const owns = (object: object, key: string) => Object.hasOwn(object, key);
const bool: Rule = value => typeof value === 'boolean';
const num = (min: number, max: number): Rule => value => typeof value === 'number' && Number.isSafeInteger(value) && value >= min && value <= max;
const str = (max: number, min = 0): Rule => (value, budget) =>
  typeof value === 'string' && value.length >= min && value.length <= max && !value.includes('\0') && (budget.remaining -= value.length) >= 0;
const oneOf = (values: readonly string[]): Rule => value => typeof value === 'string' && values.includes(value);
function shape(required: Fields, optional: Fields = {}): Rule {
  return (value, budget) => {
    if (!isRecord(value) || !Object.keys(required).every(key => owns(value, key))) return false;
    return Object.keys(value).every(key => {
      if (owns(required, key)) return required[key](value[key], budget);
      // Optional TS fields may be sent explicitly as undefined by structured clone.
      return owns(optional, key) && (value[key] === undefined || optional[key](value[key], budget));
    });
  };
}
const empty = shape({});
const id = str(256, 1);
const label = str(4096);
const path = str(32767);
const text = str(MAX_FRAME - 1);
const port = num(1, 65535);
const limit = num(0, 10_000);
const offset = num(0, Number.MAX_SAFE_INTEGER);
export const isFileToken = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_-]{43}$/u.test(value);
const token: Rule = isFileToken;
const kind = oneOf(['detect', 'install', 'uninstall']);
const state = oneOf([...activeStates, 'succeeded', 'succeeded_with_warning', 'failed', 'cancelled', 'interrupted']);
const hostPath = { hostId: id, path };
const fileMode: Rule = value => typeof value === 'string' && /^[0-7]{3,4}$/u.test(value);
const taskId = { taskId: id };
const dimensions = { rows: num(1, 1000), cols: num(1, 1000) };
const parameterValue = str(65536);
const parameters: Rule = (value, budget) => isRecord(value) && Object.keys(value).length <= 64 &&
  Object.entries(value).every(([key, val]) => /^[A-Za-z0-9_.-]{1,128}$/u.test(key) &&
    !['__proto__', 'constructor', 'prototype'].includes(key) && parameterValue(val, budget));
const operation = shape({ hostId: id, appId: id, kind, requestKey: id }, {
  version: str(256), sourceMode: oneOf(['offline', 'online']), resourceId: id,
  parameters, keepData: bool, confirmed: bool,
});
const host = shape({ name: label, address: label, port, username: label, group: label, auth: oneOf(['password', 'key']) }, {
  id, secret: str(65536), keyToken: token, remember: bool,
});
const probe = shape({}, { host, hostId: id, acceptFingerprint: str(1024), replaceFingerprint: bool });
const customRun = shape({ hostId: id, appId: id, action: oneOf(['start', 'stop', 'restart', 'status', ...uploadActions]), confirmed: bool }, { token });

// Match the Go terminal input limit (16 KiB decoded, padded base64).
const terminalBytes: Rule = (value, budget) => {
  if (typeof value !== 'string' || value.length === 0 || value.length > 21_848 || value.length % 4 !== 0 ||
      (budget.remaining -= value.length) < 0) return false;
  const bytes = Buffer.from(value, 'base64');
  return bytes.length <= 16_384 && bytes.toString('base64') === value;
};

const schemas = {
  'core.status': empty,
  'dialog.pick': shape({ purpose: oneOf(purposes) }),
  'dialog.export': shape({ token, text }),
  'clipboard.readFiles': empty,
  'update.status': empty,
  'update.check': empty,
  'update.download': empty,
  'update.cancel': empty,
  'update.install': empty,
  'host.list': empty,
  'host.probe': (value, budget) => {
    if (!probe(value, budget) || !isRecord(value) || (value.host === undefined) === (value.hostId === undefined)) return false;
    return !isRecord(value.host) || value.host.keyToken === undefined || value.host.auth === 'key';
  },
  'host.save': shape({ probeId: id }),
  'host.delete': shape({ id }),
  'catalog.list': empty,
  'operation.validate': operation,
  'operation.start': operation,
  'operation.batch': shape({ items: (value, budget) => Array.isArray(value) && value.length > 0 && value.length <= 200 &&
    Array.from(value).every(item => operation(item, budget)) }),
  'operation.list': shape({}, { limit, offset, hostId: id, appId: id, kind, state }),
  'operation.get': shape(taskId, { afterSeq: offset }),
  'operation.cancel': shape(taskId),
  'operation.clearLogs': shape(taskId),
  'resource.list': empty,
  'resource.inspect': shape({ token }),
  'resource.import': shape({ token, digest: str(256, 1) }),
  'terminal.open': shape({ hostId: id, ...dimensions }),
  'terminal.input': shape({ sessionId: id, bytesB64: terminalBytes }),
  'terminal.resize': shape({ sessionId: id, ...dimensions }),
  'terminal.close': shape({ sessionId: id }),
  'files.list': shape(hostPath),
  'files.read': shape(hostPath),
  'files.write': shape({ ...hostPath, content: text, expectedDigest: str(256, 1), confirmed: bool }),
  'files.mkdir': shape(hostPath),
  'files.rename': shape({ ...hostPath, to: path, confirmed: bool }),
  'files.delete': shape({ ...hostPath, confirmed: bool }, { recursive: bool }),
  'files.upload': shape({ ...hostPath, token, confirmed: bool }, { overwrite: bool }),
  'files.download': shape({ ...hostPath, token }),
  'files.transfer': shape({ sourceHostId: id, sourcePath: path, targetHostId: id, targetPath: path, mode: oneOf(['copy', 'move']), confirmed: bool }, { overwrite: bool }),
  'files.archive': shape({ ...hostPath, destination: path, confirmed: bool }, { overwrite: bool }),
  'files.extract': shape({ ...hostPath, destination: path, confirmed: bool }, { overwrite: bool }),
  'files.chmod': shape({ ...hostPath, mode: fileMode, confirmed: bool }, { recursive: bool, sudo: bool, fast: bool }),
  'custom.list': empty,
  'custom.save': shape({ id: str(256), name: label, kind: oneOf(['SUPPORT', 'GENERIC', 'CUSTOM']), description: str(65536),
    directory: path, frontendDirectory: path, configPath: path, startCommand: str(65536), stopCommand: str(65536),
    statusCommand: str(65536), logsPath: path, enabled: bool }),
  'custom.delete': shape({ id }),
  'custom.run': (value, budget) => customRun(value, budget) && isRecord(value) &&
    ((value.token !== undefined) === isUploadAction(value.action)),
  'settings.get': empty,
  'settings.save': shape({ theme: oneOf(['dark', 'light', 'system']), connectionTimeoutSeconds: num(1, 86400),
    retryCount: num(0, 100), retryIntervalSeconds: num(0, 86400), maxConcurrentHosts: num(1, 1000),
    repositoryUrl: str(8192), updateCheckUrl: str(8192), proxyUrl: str(8192), cacheLimitMiB: num(0, Number.MAX_SAFE_INTEGER),
  }, { repositoryTokenSet: bool, proxyPasswordSet: bool, repositoryToken: str(65536), proxyPassword: str(65536) }),
  'settings.updateCheck': empty,
  'migration.preview': shape({ token }),
  'migration.apply': shape({ previewId: id }, { hostsOnly: bool, acceptUpdates: bool }),
  'migration.history': shape({}, { limit, offset }),
} satisfies Record<keyof Methods, Rule>;

export function validate<K extends keyof Methods>(method: K, params: unknown): Methods[K][0] {
  if (!schemas[method](params, { remaining: MAX_FRAME })) throw new DesktopFault('BAD_PARAMS');
  return params as Methods[K][0];
}
