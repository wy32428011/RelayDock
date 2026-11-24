import type { ElectronMethods } from '@relaydock/protocol';

export const CALL_CHANNEL = 'relaydock:call';
export const EVENT_CHANNEL = 'relaydock:event';
export const desktopMethods = [
  'core.status', 'dialog.pick', 'dialog.export',
  'update.status', 'update.check', 'update.download', 'update.cancel', 'update.install',
] as const satisfies readonly (keyof ElectronMethods)[];