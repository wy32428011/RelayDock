import { contextBridge, ipcRenderer } from 'electron';
import type { IpcRendererEvent } from 'electron';
import { coreMethods } from '@relaydock/protocol';
import type { Bridge, CoreEvent, Fault, Methods, Result } from '@relaydock/protocol';
import { CALL_CHANNEL, EVENT_CHANNEL, desktopMethods } from './channels';

const allowed = new Set<string>([...coreMethods, ...desktopMethods]);

const bridge: Bridge = {
  async call<K extends keyof Methods>(method: K, params: Methods[K][0]): Promise<Methods[K][1]> {
    if (typeof method !== 'string' || !allowed.has(method)) {
      throw { code: 'UNKNOWN_METHOD', message: '不支持此操作。', retryable: false } satisfies Fault;
    }
    let reply: Result;
    try {
      reply = await ipcRenderer.invoke(CALL_CHANNEL, method, params) as Result;
    } catch {
      // A native IPC failure can include internal stack frames and file names.
      throw { code: 'BRIDGE_UNAVAILABLE', message: '桌面连接已断开，请重新打开应用。', retryable: false } satisfies Fault;
    }
    // A plain Fault preserves its fields when crossing contextBridge, unlike Error.
    if (!reply.ok) throw reply.error;
    return reply.result as Methods[K][1];
  },
  subscribe(listener) {
    if (typeof listener !== 'function') throw new TypeError('需要传入事件监听函数。');
    const receive = (_event: IpcRendererEvent, data: CoreEvent) => {
      try {
        listener(data);
      } catch {
        // Do not let one renderer subscriber disrupt delivery to the others.
      }
    };
    ipcRenderer.on(EVENT_CHANNEL, receive);
    return () => {
      ipcRenderer.removeListener(EVENT_CHANNEL, receive);
    };
  },
};

if (process.isMainFrame) contextBridge.exposeInMainWorld('relayDock', bridge);
