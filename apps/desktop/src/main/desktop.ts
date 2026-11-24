import { app, BrowserWindow, dialog, ipcMain, Menu, session } from 'electron';
import type { IpcMainInvokeEvent, OpenDialogOptions, Session } from 'electron';
import { lstatSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { activeStates, MAX_FRAME } from '@relaydock/protocol';
import type { CoreEvent, CoreMethods, Result } from '@relaydock/protocol';
import { CALL_CHANNEL, EVENT_CHANNEL } from '../preload/channels';
import { CoreProcess } from './core';
import { BridgeError, publicFault } from './faults';
import { FileGrants } from './file-grants';
import type { GrantScope } from './file-grants';
import { isRecord } from './protocol';
import { isMethod, validate } from './validation';
import type { Purpose } from './validation';
import { APP_ID, PRODUCT_NAME, corePaths, rendererPath, rendererRequestAllowed, userDataPath } from './paths';
import { AppUpdater } from './updater';

const rendererRoot = join(__dirname, 'renderer');
const rendererEntry = join(rendererRoot, 'index.html');
const preload = join(__dirname, 'preload.cjs');
const fileGrants = new FileGrants();

interface WindowContext {
  window: BrowserWindow;
  scope: GrantScope;
  picking: boolean;
}
let workspace: WindowContext | undefined;
let updater: AppUpdater | undefined;
let startupUpdateTimer: ReturnType<typeof setTimeout> | undefined;
let quitting = false;
let allowQuit = false;
let coreWasReady = false;

function publish(event: CoreEvent): void {
  if (event.topic === 'core.status' && isRecord(event.data) && event.data.ready === true) coreWasReady = true;
  const current = workspace;
  if (!current || current.window.isDestroyed()) return;
  const contents = current.window.webContents;
  try {
    if (!contents.isDestroyed() && rendererPath(contents.mainFrame.url) === rendererEntry) {
      contents.send(EVENT_CHANNEL, event);
    }
  } catch {
    // Rendering may have stopped while the core was emitting an event.
  }
}
const core = new CoreProcess(publish);

function secureSession(ui: Session): void {
  ui.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  ui.setPermissionCheckHandler(() => false);
  ui.setDevicePermissionHandler(() => false);
  ui.setDisplayMediaRequestHandler((_request, callback) => callback({}));
  ui.on('will-download', event => event.preventDefault());
  ui.webRequest.onBeforeRequest((request, callback) => {
    callback({ cancel: !rendererRequestAllowed(rendererEntry, rendererRoot, request.url, request.resourceType) });
  });
}

function resetGrants(context: WindowContext): void {
  fileGrants.revoke(context.scope);
  context.scope = {};
}

function createWindow(ui: Session): void {
  const window = new BrowserWindow({
    title: PRODUCT_NAME,
    width: 1420, height: 920,
    minWidth: 900, minHeight: 620,
    show: false,
    autoHideMenuBar: true,
    backgroundColor: '#101622',
    webPreferences: {
      session: ui, preload,
      nodeIntegration: false, nodeIntegrationInSubFrames: false, nodeIntegrationInWorker: false,
      contextIsolation: true, sandbox: true, webviewTag: false, webSecurity: true,
      allowRunningInsecureContent: false, devTools: !app.isPackaged,
    },
  });
  const context: WindowContext = { window, scope: {}, picking: false };
  workspace = context;
  const contents = window.webContents;
  contents.setWindowOpenHandler(() => ({ action: 'deny' }));
  contents.on('will-navigate', event => event.preventDefault());
  contents.on('will-frame-navigate', event => event.preventDefault());
  contents.on('will-redirect', event => event.preventDefault());
  contents.on('will-attach-webview', event => event.preventDefault());
  contents.on('did-start-navigation', details => {
    if (details.isMainFrame && !details.isSameDocument) resetGrants(context);
  });
  contents.on('render-process-gone', () => resetGrants(context));
  window.once('ready-to-show', () => {
    if (!window.isDestroyed() && !allowQuit) window.show();
  });
  window.on('close', event => {
    if (allowQuit) return;
    event.preventDefault();
    void requestQuit();
  });
  window.on('session-end', () => core.forceKill());
  window.once('closed', () => {
    resetGrants(context);
    if (workspace === context) workspace = undefined;
  });
  void window.loadFile(rendererEntry).catch(() => {
    if (!window.isDestroyed() && !quitting) {
      window.show();
      dialog.showErrorBox(PRODUCT_NAME, '无法加载本地界面，请检查安装文件或重新安装应用。');
    }
  });
}

function trustedContext(event: IpcMainInvokeEvent): WindowContext | undefined {
  const context = workspace;
  if (!context || context.window.isDestroyed()) return undefined;
  const contents = context.window.webContents;
  const frame = event.senderFrame;
  if (frame && !contents.isDestroyed() && event.sender === contents && frame === contents.mainFrame &&
      rendererPath(frame.url) === rendererEntry) return context;
  return undefined;
}

const openOptions: Record<Exclude<Purpose, 'download' | 'export'>, OpenDialogOptions> = {
  key: { title: '选择 SSH 私钥', buttonLabel: '选择私钥', properties: ['openFile', 'showHiddenFiles'] },
  upload: { title: '选择上传文件', buttonLabel: '选择文件', properties: ['openFile'] },
  uploadDirectory: { title: '选择上传文件夹', buttonLabel: '选择文件夹', properties: ['openDirectory'] },
  resource: { title: '选择离线资源目录', buttonLabel: '选择目录', properties: ['openDirectory'] },
  migration: { title: '选择旧版数据库的只读快照', buttonLabel: '选择快照', properties: ['openFile'], filters: [
    { name: 'SQLite 快照', extensions: ['db', 'sqlite', 'sqlite3', 'bak'] }, { name: '所有文件', extensions: ['*'] },
  ] },
};

async function pick(context: WindowContext, purpose: Purpose, stillAllowed: () => boolean): Promise<{ token: string; name: string } | null> {
  if (context.picking) throw new BridgeError('DIALOG_BUSY', '请先关闭其他文件选择窗口。', true);
  context.picking = true;
  const scope = context.scope;
  try {
    let path: string | undefined;
    if (purpose === 'download' || purpose === 'export') {
      const exporting = purpose === 'export';
      const result = await dialog.showSaveDialog(context.window, {
        title: exporting ? '选择导出位置' : '保存远端文件',
        buttonLabel: exporting ? '导出' : '保存',
        defaultPath: exporting ? 'RelayDock.log' : '远端文件',
        ...(exporting ? { filters: [{ name: '日志和文本', extensions: ['log', 'txt'] }, { name: '所有文件', extensions: ['*'] }] } : {}),
      });
      if (!result.canceled) path = result.filePath;
    } else {
      const result = await dialog.showOpenDialog(context.window, openOptions[purpose]);
      if (!result.canceled && result.filePaths.length === 1) path = result.filePaths[0];
    }
    if (!path) return null;
    if (!stillAllowed()) throw new BridgeError('BAD_SENDER', '文件选择窗口已失效，请重新选择。');
    const granted = await fileGrants.issue(scope, purpose, path);
    if (!stillAllowed()) {
      fileGrants.revoke(scope);
      throw new BridgeError('BAD_SENDER', '文件选择窗口已失效，请重新选择。');
    }
    return granted;
  } finally {
    context.picking = false;
  }
}

function registerCalls(): void {
  ipcMain.handle(CALL_CHANNEL, async (event, ...args: unknown[]): Promise<Result> => {
    try {
      const context = trustedContext(event);
      if (!context) throw new BridgeError('BAD_SENDER', '只能从当前 RelayDock 窗口执行此操作。');
      const [method, input] = args;
      if (!isMethod(method)) throw new BridgeError('UNKNOWN_METHOD', '不支持此操作。');
      if (args.length !== 2 || !isRecord(input)) {
        throw new BridgeError('BAD_PARAMS', '参数格式无效，请检查后重试。');
      }
      validate(method, input);
      if (method === 'core.status') return { ok: true, result: core.status };
      if (method === 'update.status') {
        if (!updater) throw new BridgeError('UPDATE_UNAVAILABLE', '更新服务尚未初始化。', true);
        return { ok: true, result: updater.getState() };
      }
      if (quitting) throw new BridgeError('APP_CLOSING', '应用正在准备退出，不能开始新操作。', true);
      if (method === 'update.check') {
        if (!updater) throw new BridgeError('UPDATE_UNAVAILABLE', '更新服务尚未初始化。', true);
        return { ok: true, result: await updater.check({ silent: false }) };
      }
      if (method === 'update.download') {
        if (!updater) throw new BridgeError('UPDATE_UNAVAILABLE', '更新服务尚未初始化。', true);
        return { ok: true, result: await updater.download() };
      }
      if (method === 'update.cancel') {
        if (!updater) throw new BridgeError('UPDATE_UNAVAILABLE', '更新服务尚未初始化。', true);
        return { ok: true, result: updater.cancel() };
      }
      if (method === 'update.install') {
        if (!updater) throw new BridgeError('UPDATE_UNAVAILABLE', '更新服务尚未初始化。', true);
        const installerPath = await updater.prepareInstall();
        const started = await requestQuit(installerPath);
        return { ok: true, result: { ok: started } };
      }
      const scope = context.scope;
      const stillAllowed = () => !quitting && context.scope === scope && trustedContext(event) === context;
      if (method === 'dialog.pick') {
        return { ok: true, result: await pick(context, input.purpose as Purpose, stillAllowed) };
      }
      if (method === 'dialog.export') {
        const text = input.text as string;
        if (Buffer.byteLength(text, 'utf8') >= MAX_FRAME) throw new BridgeError('FRAME_LIMIT', '导出文本过大，请缩小日志范围。');
        await fileGrants.export(scope, input.token, text, stillAllowed);
        return { ok: true, result: { ok: true } };
      }
      if (!core.status.ready) throw new BridgeError('CORE_UNAVAILABLE', core.status.detail ?? '本地核心尚未就绪。');
      const params = await fileGrants.translate(method, input, scope);
      if (!stillAllowed()) throw new BridgeError('BAD_SENDER', '当前窗口授权已失效，请重新操作。');
      const result = await core.call(method, params as CoreMethods[typeof method][0]);
      return { ok: true, result };
    } catch (error) {
      return { ok: false, error: publicFault(error) };
    }
  });
}

async function quitWarning(): Promise<string | undefined> {
  if (!core.status.ready) return coreWasReady ? '无法确认是否仍有任务。' : undefined;
  if (core.hasPendingRequests) return '本地核心仍在处理请求。';
  try {
    const lists = await Promise.all(activeStates.map(state => core.call('operation.list', { state, limit: 1 }, 1500)));
    if (lists.some(list => !Array.isArray(list))) return '无法确认是否仍有任务。';
    return lists.some(list => list.length > 0) ? '仍有任务在等待或运行。' : undefined;
  } catch {
    return '无法确认是否仍有任务。';
  }
}

async function requestQuit(installerPath?: string): Promise<boolean> {
  if (quitting) return false;
  quitting = true;
  if (startupUpdateTimer) {
    clearTimeout(startupUpdateTimer);
    startupUpdateTimer = undefined;
  }
  try {
    const context = workspace;
    if (context && !context.window.isDestroyed()) {
      const warning = await quitWarning();
      if (warning && !context.window.isDestroyed()) {
        const { response } = await dialog.showMessageBox(context.window, {
          type: 'warning',
          title: installerPath ? '安装更新并退出 RelayDock' : '退出 RelayDock',
          message: warning,
          detail: installerPath
            ? '继续安装会先关闭本地连接并启动新版安装程序，但未必会停止远端操作。升级重开后请复查任务状态。'
            : '退出会关闭本地连接，但未必会停止远端操作。重开应用后请复查任务状态。',
          buttons: ['继续工作', installerPath ? '仍然退出并安装' : '仍然退出'],
          defaultId: 0, cancelId: 0, noLink: true,
        });
        if (response !== 1) return false;
      }
      resetGrants(context);
    }
    updater?.cancel();
    await core.stop();
    if (installerPath && updater) {
      updater.launchInstaller(installerPath);
    }
    allowQuit = true;
    app.quit();
    return true;
  } catch {
    // Do not dismiss an active-task warning due to a desktop dialog error.
    return false;
  } finally {
    if (!allowQuit) quitting = false;
  }
}

export function runDesktop(): void {
  app.setName(PRODUCT_NAME);
  if (process.platform === 'win32') app.setAppUserModelId(APP_ID);
  let data: string;
  try {
    data = userDataPath(app.getPath('appData'), process.env.RELAYDOCK_DATA_DIR);
    const chromeData = join(data, 'chromium');
    mkdirSync(chromeData, { recursive: true, mode: 0o700 });
    if (!lstatSync(chromeData).isDirectory()) {
      throw new BridgeError('INVALID_PROFILE', '新版界面数据目录不能是文件或链接。');
    }
    app.setPath('userData', data);
    app.setPath('sessionData', chromeData);
  } catch (error) {
    dialog.showErrorBox(PRODUCT_NAME, error instanceof BridgeError ? error.message : '无法初始化新版数据目录，请检查本机目录权限。');
    app.exit(1);
    return;
  }
  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  app.on('second-instance', () => {
    const current = workspace?.window;
    if (!current || current.isDestroyed() || quitting) return;
    if (current.isMinimized()) current.restore();
    current.show();
    current.focus();
  });
  app.on('before-quit', event => {
    if (allowQuit) return;
    event.preventDefault();
    void requestQuit();
  });
  app.on('window-all-closed', () => { if (!allowQuit) void requestQuit(); });
  app.on('will-quit', () => core.forceKill());
  process.once('exit', () => core.forceKill());
  void app.whenReady().then(() => {
    if (quitting) return;
    Menu.setApplicationMenu(process.platform === 'darwin' ? Menu.buildFromTemplate([
      { label: PRODUCT_NAME, submenu: [
        { role: 'about', label: `关于 ${PRODUCT_NAME}` },
        { type: 'separator' },
        { role: 'services', label: '服务' },
        { type: 'separator' },
        { role: 'hide', label: `隐藏 ${PRODUCT_NAME}` },
        { role: 'hideOthers', label: '隐藏其他应用' },
        { role: 'unhide', label: '全部显示' },
        { type: 'separator' },
        { role: 'quit', label: `退出 ${PRODUCT_NAME}` },
      ] },
      { label: '编辑', submenu: [
        { role: 'undo', label: '撤销' },
        { role: 'redo', label: '重做' },
        { type: 'separator' },
        { role: 'cut', label: '剪切' },
        { role: 'copy', label: '复制' },
        { role: 'paste', label: '粘贴' },
        { role: 'pasteAndMatchStyle', label: '粘贴并匹配样式' },
        { role: 'delete', label: '删除' },
        { role: 'selectAll', label: '全选' },
        { type: 'separator' },
        { label: '朗读', submenu: [
          { role: 'startSpeaking', label: '开始朗读' },
          { role: 'stopSpeaking', label: '停止朗读' },
        ] },
      ] },
    ]) : null);
    const ui = session.fromPartition('relaydock-ui');
    secureSession(ui);
    const customApiUrl = !app.isPackaged ? process.env.RELAYDOCK_UPDATE_API_URL : undefined;
    updater = new AppUpdater({
      currentVersion: app.getVersion(),
      platform: process.platform,
      arch: process.arch,
      updatesDir: join(data, 'updates'),
      publish,
      apiUrl: customApiUrl,
      allowLoopbackHttp: !app.isPackaged && Boolean(customApiUrl),
    });
    registerCalls();
    createWindow(ui);
    app.on('activate', () => { if (!workspace && !quitting) createWindow(ui); });
    let paths = null;
    try {
      paths = { ...corePaths(app.isPackaged, process.resourcesPath, process.env), data };
    } catch {
      // Let CoreProcess publish an actionable unavailable status to the window.
    }
    void core.start(paths);
    if (app.isPackaged || customApiUrl) {
      startupUpdateTimer = setTimeout(() => {
        startupUpdateTimer = undefined;
        if (!quitting) void updater?.check({ silent: true });
      }, 3_000);
    }
  }).catch(() => {
    dialog.showErrorBox(PRODUCT_NAME, '无法初始化桌面窗口，请重新打开应用。');
    core.forceKill();
    app.exit(1);
  });
}
