import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Bridge, CoreEvent, CustomApp, Host, Methods, MigrationPreview, OfflineResource, Settings, Task } from '@relaydock/protocol';
import { WorkspacePane } from './index';
import type { WorkspacePaneProps } from './index';
import { parentRemotePath, redactExport, restoreNewlines } from './text';

const termMock = vi.hoisted(() => ({ write: vi.fn(), typed: undefined as ((value: string) => void) | undefined }));
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    static strings = { promptLabel: '', tooMuchOutput: '' };
    rows = 24;
    cols = 80;
    options = { disableStdin: true };
    loadAddon() { /* No browser canvas in these bridge tests. */ }
    open() { /* No browser canvas in these bridge tests. */ }
    onData(listener: (value: string) => void) { termMock.typed = listener; return { dispose() { termMock.typed = undefined; } }; }
    onBinary() { return { dispose() { /* Not exercised here. */ } }; }
    onSelectionChange() { return { dispose() { /* Not exercised here. */ } }; }
    attachCustomKeyEventHandler() { /* No browser canvas in these bridge tests. */ }
    write(...args: unknown[]) { termMock.write(...args); }
    focus() { /* No browser canvas in these bridge tests. */ }
    reset() { /* No browser canvas in these bridge tests. */ }
    dispose() { /* No browser canvas in these bridge tests. */ }
  },
}));
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() { /* Dimensions fixed for these bridge tests. */ } } }));

const host: Host = { id: 'host-test', name: '合成主机', address: 'fixture.invalid', port: 22, username: 'operator', group: '', auth: 'password', os: 'ubuntu', osVersion: '24.04', arch: 'arm64', fingerprint: 'SHA256:synthetic', credentialReady: true };
const oldDigest = 'a'.repeat(64);
const app: CustomApp = { id: 'test-app', name: '合成应用定义', kind: 'CUSTOM', description: '', directory: '/opt/demo', frontendDirectory: '', configPath: '', startCommand: 'synthetic-start', stopCommand: '', statusCommand: '', logsPath: '', enabled: true };
const settings: Settings = { theme: 'dark', connectionTimeoutSeconds: 60, retryCount: 3, retryIntervalSeconds: 5, maxConcurrentHosts: 3, cacheLimitMiB: 100, repositoryUrl: '', updateCheckUrl: '', proxyUrl: '' };
const task: Task = { id: 'task-test', hostId: host.id, appId: 'sample', version: '1.2', kind: 'detect', state: 'succeeded', stage: 'succeeded', percent: 100, message: 'token=SYNTHETIC_VALUE', createdAt: '2026-08-01T09:20:50Z', updatedAt: '2026-08-01T09:20:55Z', seq: 5 };
const unit = new TextEncoder();
const bytes64 = (text: string) => btoa(String.fromCharCode(...unit.encode(text)));

function fixture(handlers: Partial<Record<keyof Methods, (params: unknown) => unknown>>) {
  const listeners = new Set<(event: CoreEvent) => void>();
  const call = vi.fn(async (method: keyof Methods, params: unknown) => {
    const handler = handlers[method];
    if (!handler) throw new Error(`Unexpected test method ${method}`);
    return handler(params);
  });
  const api: Bridge = {
    call: call as Bridge['call'],
    subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener); },
  };
  return { api, call, emit: (event: CoreEvent) => { for (const listener of listeners) listener(event); } };
}

function pane(api: Bridge, current: WorkspacePaneProps['pane'], props: Partial<WorkspacePaneProps> = {}) {
  return <WorkspacePane api={api} pane={current} host={host} catalog={[]} tasks={[]} onTheme={() => undefined} {...props} />;
}
function button(name: string) { return screen.getByRole('button', { name }); }
function modal() { return screen.getByRole('dialog'); }
function accept(label: string) { fireEvent.click(within(modal()).getByRole('button', { name: label })); }

beforeEach(() => {
  termMock.write.mockClear();
  vi.stubGlobal('ResizeObserver', class { observe() { /* Fixed synthetic size. */ } disconnect() { /* Nothing registered. */ } });
  if (!HTMLDialogElement.prototype.showModal) {
    Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
    Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); } });
  }
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('RelayDock 工作区 · 合成桥接', () => {
  it('只向匹配的 PTY 发送字节，主机移除后关闭旧会话', async () => {
    let resolve!: (value: Methods['terminal.open'][1]) => void;
    const openResult = new Promise<Methods['terminal.open'][1]>(done => { resolve = done; });
    const { api, call, emit } = fixture({
      'terminal.open': () => openResult,
      'terminal.close': () => ({ ok: true }),
      'terminal.input': () => ({ ok: true }),
      'files.list': () => ({ path: '/root', entries: [] }),
    });
    const view = render(<StrictMode>{pane(api, 'terminal')}</StrictMode>);
    expect(call.mock.calls.some(([method]) => method === 'terminal.open')).toBe(false);
    fireEvent.click(await screen.findByRole('button', { name: '建立会话' }));
    expect(call).toHaveBeenCalledWith('terminal.open', { hostId: host.id, rows: 24, cols: 80 });
    function output(sessionId: string, seq: number, text: string) {
      emit({ v: 1, type: 'event', topic: 'terminal.output', sessionId, seq, data: { bytesB64: bytes64(text) } });
    }
    await act(async () => { output('other', 1, 'wrong'); output('pty-1', 1, '好'); resolve({ sessionId: 'pty-1' }); });
    expect(termMock.write).toHaveBeenCalledTimes(1);
    expect(Array.from(termMock.write.mock.calls[0]?.[0] as Uint8Array)).toEqual(Array.from(unit.encode('好')));
    act(() => { output('other', 2, 'never'); output('pty-1', 1, 'duplicate'); termMock.typed?.('中'); termMock.typed?.('\u0003'); });
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.input', { sessionId: 'pty-1', bytesB64: bytes64('中\u0003') }));
    expect(termMock.write).toHaveBeenCalledTimes(1);
    view.rerender(<StrictMode>{pane(api, 'terminal', { host: { ...host, id: 'host-other', address: 'second.invalid' } })}</StrictMode>);
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.close', { sessionId: 'pty-1' }));
    act(() => output('pty-1', 3, 'old session'));
    expect(termMock.write).toHaveBeenCalledTimes(1);
  });

  it('关闭在切换主机后才返回的会话', async () => {
    let resolve!: (value: Methods['terminal.open'][1]) => void;
    const { api, call } = fixture({
      'terminal.open': () => new Promise(done => { resolve = done; }),
      'terminal.close': () => ({ ok: true }),
      'files.list': () => ({ path: '/root', entries: [] }),
    });
    const view = render(pane(api, 'terminal'));
    fireEvent.click(await screen.findByRole('button', { name: '建立会话' }));
    view.rerender(pane(api, 'terminal', { host: { ...host, id: 'new' } }));
    await act(async () => resolve({ sessionId: 'late' }));
    expect(call).toHaveBeenCalledWith('terminal.close', { sessionId: 'late' });
    expect(screen.getByText('未连接')).toBeTruthy();
  });

  it('支持多开终端标签，并在切换标签时同步右侧服务器文件列表', async () => {
    const hostB: Host = { ...host, id: 'host-b', name: '应用节点 B', address: 'node-b.invalid' };
    let seq = 0;
    const { api, call } = fixture({
      'terminal.open': () => ({ sessionId: `pty-${++seq}` }),
      'terminal.close': () => ({ ok: true }),
      'files.list': params => {
        const { hostId } = params as { hostId: string; path: string };
        return hostId === hostB.id
          ? { path: '/mnt/disk02', entries: [{ name: 'jdk-25', path: '/mnt/disk02/jdk-25', size: 0, directory: true }] }
          : { path: '/opt', entries: [{ name: 'app.jar', path: '/opt/app.jar', size: 1024, directory: false }] };
      },
    });
    const view = render(pane(api, 'terminal', { host, hosts: [host, hostB] }));
    expect(await screen.findByText('app.jar')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: '新建终端标签' }));
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.open', { hostId: host.id, rows: 24, cols: 80 }));
    expect(screen.getAllByRole('tab')).toHaveLength(2);

    view.rerender(pane(api, 'terminal', { host: hostB, hosts: [host, hostB], terminalOpenRequest: { hostId: hostB.id, nonce: 1, forceNew: false } }));
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.open', { hostId: hostB.id, rows: 24, cols: 80 }));
    expect(await screen.findByText('jdk-25')).toBeTruthy();
    expect(screen.getAllByRole('tab')).toHaveLength(3);
    expect(call.mock.calls.some(([method]) => method === 'terminal.close')).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: '关闭终端 应用节点 B' }));
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.close', { sessionId: 'pty-2' }));
    expect(screen.getAllByRole('tab')).toHaveLength(2);
    expect(await screen.findByText('app.jar')).toBeTruthy();
  });

  it('右侧文件列表右键菜单支持跨服务器复制粘贴与压缩归档', async () => {
    const hostB: Host = { ...host, id: 'host-b', name: '应用节点 B', address: 'node-b.invalid' };
    const { api, call } = fixture({
      'terminal.open': () => ({ sessionId: 'pty-x' }),
      'terminal.close': () => ({ ok: true }),
      'files.list': params => {
        const { hostId } = params as { hostId: string; path: string };
        return hostId === hostB.id
          ? { path: '/mnt/disk02', entries: [{ name: 'jdk-25', path: '/mnt/disk02/jdk-25', size: 0, directory: true }] }
          : { path: '/opt', entries: [{ name: 'app-dir', path: '/opt/app-dir', size: 0, directory: true }] };
      },
      'files.transfer': () => ({ ok: true, files: 3, dirs: 1, bytes: 2048 }),
      'files.archive': () => ({ ok: true, path: '/opt/app-dir.tar.gz', files: 3, bytes: 1024 }),
    });
    const view = render(pane(api, 'terminal', { host, hosts: [host, hostB] }));
    const folderCell = await screen.findByText('app-dir');

    // Right-click folder on host A and copy it.
    fireEvent.contextMenu(folderCell);
    const menu = screen.getByRole('menu', { name: '文件操作菜单' });
    expect(within(menu).getByRole('menuitem', { name: /打开/ })).toBeTruthy();
    expect(within(menu).getByRole('menuitem', { name: /SCP/ })).toBeTruthy();
    expect(within(menu).getByRole('menuitem', { name: /压缩/ })).toBeTruthy();
    fireEvent.click(within(menu).getByRole('button', { name: '复制' }));

    // Switch to host B, right-click, and paste across servers.
    view.rerender(pane(api, 'terminal', { host: hostB, hosts: [host, hostB], terminalOpenRequest: { hostId: hostB.id, nonce: 2, forceNew: false } }));
    const targetCell = await screen.findByText('jdk-25');
    fireEvent.contextMenu(targetCell);
    const menuB = screen.getByRole('menu', { name: '文件操作菜单' });
    fireEvent.click(within(menuB).getByRole('button', { name: '粘贴' }));
    accept('确认跨服务器复制');
    await waitFor(() => expect(call).toHaveBeenCalledWith('files.transfer', {
      sourceHostId: host.id,
      sourcePath: '/opt/app-dir',
      targetHostId: hostB.id,
      targetPath: '/mnt/disk02/app-dir',
      mode: 'copy',
      overwrite: false,
      confirmed: true,
    }));

    // Switch back to host A and archive app-dir.
    fireEvent.click(screen.getAllByRole('tab')[0]!);
    const folderBack = await within(screen.getByRole('table', { name: '远端目录条目' })).findByText('app-dir');
    fireEvent.contextMenu(folderBack);
    fireEvent.click(within(screen.getByRole('menu', { name: '文件操作菜单' })).getByRole('menuitem', { name: '压缩' }));
    fireEvent.click(within(screen.getByRole('dialog', { name: '压缩远程条目' })).getByRole('button', { name: '确认压缩' }));
    await waitFor(() => expect(call).toHaveBeenCalledWith('files.archive', {
      hostId: host.id,
      path: '/opt/app-dir',
      destination: '/opt/app-dir.tar.gz',
      overwrite: false,
      confirmed: true,
    }));
  });

  it('文本写入提交原摘要并保留 CRLF；远端冲突后保留草稿', async () => {
    const { api, call } = fixture({ 'files.read': () => ({ content: '# 注释\r\nport=1\r\n', digest: oldDigest }),
      'files.write': () => { throw { code: 'FILE_CHANGED', message: '远端文件已经变化' }; } });
    const view = render(pane(api, 'scripts'));
    fireEvent.change(screen.getByLabelText('远程文件路径'), { target: { value: '/opt/demo.conf' } });
    fireEvent.click(button('读取文本'));
    const editor = await screen.findByLabelText<HTMLTextAreaElement>('远程文本草稿');
    fireEvent.change(editor, { target: { value: '# 注释\nport=2\n' } });
    fireEvent.click(button('检查并保存'));
    expect(call.mock.calls.some(([method]) => method === 'files.write')).toBe(false);
    expect(within(modal()).getByText('− port=1')).toBeTruthy();
    expect(document.activeElement).toBe(within(modal()).getByRole('button', { name: '取消' }));
    accept('确认写入');
    await screen.findByText(/远端文件已经变化/);
    expect(call).toHaveBeenCalledWith('files.write', { hostId: host.id, path: '/opt/demo.conf', content: '# 注释\r\nport=2\r\n', expectedDigest: oldDigest, confirmed: true });
    expect(editor.value).toBe('# 注释\nport=2\n');
    view.rerender(pane(api, 'scripts', { host: { ...host, id: 'other' } }));
    expect(screen.queryByLabelText('远程文本草稿')).toBeNull();
  });

  it('在用户确认完整目标后依次上传所选文件与目录', async () => {
    const { api, call } = fixture({
      'files.list': () => ({ path: '/home/demo', entries: [] }),
      'dialog.pick': params => (params as Methods['dialog.pick'][0]).purpose === 'upload' ? { token: 'fixture-file', name: 'a.txt' } : { token: 'fixture-directory', name: 'scripts' },
      'files.upload': () => ({ ok: true }),
    });
    render(pane(api, 'files'));
    fireEvent.click(button('打开路径'));
    await screen.findByText('目录为空');
    fireEvent.click(button('选择文件'));
    await screen.findByText('a.txt （文件）');
    fireEvent.click(button('选择文件夹'));
    await screen.findByText('scripts （目录）');
    fireEvent.click(button('核对并上传'));
    expect(within(modal()).getByText('/home/demo/scripts')).toBeTruthy();
    expect(call.mock.calls.some(([method]) => method === 'files.upload')).toBe(false);
    accept('确认上传');
    await screen.findByText('已上传 2 项。');
    expect(call.mock.calls.filter(([method]) => method === 'files.upload')).toEqual([
      ['files.upload', { hostId: host.id, path: '/home/demo/a.txt', token: 'fixture-file', overwrite: false, confirmed: true }],
      ['files.upload', { hostId: host.id, path: '/home/demo/scripts', token: 'fixture-directory', overwrite: false, confirmed: true }],
    ]);
  });

  it('切换目标后不继续发送旧目标队列尚未提交的文件', async () => {
    let resolve!: (data: { ok: boolean }) => void;
    const { api, call } = fixture({
      'files.list': () => ({ path: '/home/demo', entries: [] }),
      'dialog.pick': params => (params as Methods['dialog.pick'][0]).purpose === 'upload' ? { token: 'a', name: 'first.txt' } : { token: 'b', name: 'second' },
      'files.upload': () => new Promise(done => { resolve = done; }),
    });
    const view = render(pane(api, 'files'));
    fireEvent.click(button('打开路径'));
    await screen.findByText('目录为空');
    fireEvent.click(button('选择文件'));
    await screen.findByText('first.txt （文件）');
    fireEvent.click(button('选择文件夹'));
    await screen.findByText('second （目录）');
    fireEvent.click(button('核对并上传'));
    accept('确认上传');
    expect(call.mock.calls.filter(([method]) => method === 'files.upload')).toHaveLength(1);
    view.rerender(pane(api, 'files', { host: { ...host, id: 'other' } }));
    await act(async () => resolve({ ok: true }));
    expect(call.mock.calls.filter(([method]) => method === 'files.upload')).toHaveLength(1);
    expect(screen.getByText('尚未打开目录')).toBeTruthy();
  });

  it('离线资源预检后仅提交已核对的摘要与选择授权', async () => {
    const entry: OfflineResource = { id: oldDigest, appId: 'sample', version: '1.0', os: 'ubuntu', osVersion: '24.04', arch: 'amd64', source: 'synthetic', bytes: 128, fileCount: 1, digest: oldDigest, trust: 'reviewed' };
    const { api, call } = fixture({ 'resource.list': () => [], 'dialog.pick': () => ({ token: 'picked-resource', name: 'fixture' }), 'resource.inspect': () => entry, 'resource.import': () => entry });
    render(pane(api, 'resources'));
    fireEvent.click(button('选择资源目录'));
    await screen.findByText('此资源的平台与所选主机不符，仍可导入供其他目标使用。');
    expect(call).toHaveBeenCalledWith('resource.inspect', { token: 'picked-resource' });
    expect(call.mock.calls.some(([method]) => method === 'resource.import')).toBe(false);
    fireEvent.click(button('核对并导入'));
    accept('确认导入');
    await screen.findByText(/资源已写入本地库/);
    expect(call).toHaveBeenCalledWith('resource.import', { token: 'picked-resource', digest: oldDigest });
  });

  it('分页查询历史并防御性移除导出文本中的敏感哨兵', async () => {
    const { api, call } = fixture({
      'operation.list': params => (params as Methods['operation.list'][0]).offset === 0 ? Array.from({ length: 21 }, (_, i) => ({ ...task, id: `t${i}` })) : [task],
      'dialog.pick': () => ({ token: 'target', name: 'test.txt' }), 'dialog.export': () => ({ ok: true }),
    });
    render(pane(api, 'history'));
    await screen.findByRole('table', { name: '任务历史' });
    fireEvent.click(button('下一页'));
    await screen.findByRole('button', { name: '查看 sample 任务 task-test' });
    expect(call).toHaveBeenCalledWith('operation.list', { limit: 21, offset: 20, hostId: host.id });
    fireEvent.click(button('导出本页'));
    await screen.findByText('已导出本页任务摘要。');
    const exportCall = call.mock.calls.find(([method]) => method === 'dialog.export')?.[1] as Methods['dialog.export'][0];
    expect(exportCall.text).toContain('token=[已脱敏]');
    expect(exportCall.text).not.toContain('SYNTHETIC_VALUE');
  });

  it('自定义上传并启动必须显式确认选定主机、来源和命令', async () => {
    const { api, call } = fixture({ 'custom.list': () => [app], 'dialog.pick': () => ({ token: 'upload-grant', name: 'demo.bin' }), 'custom.run': () => ({ output: '合成部署输出' }) });
    render(pane(api, 'deploy'));
    fireEvent.click(await screen.findByRole('button', { name: /合成应用定义/ }));
    fireEvent.click(button('上传文件并启动'));
    await screen.findByRole('dialog');
    expect(within(modal()).getByText('synthetic-start')).toBeTruthy();
    expect(within(modal()).getByText('demo.bin（文件）')).toBeTruthy();
    expect(call.mock.calls.some(([method]) => method === 'custom.run')).toBe(false);
    accept('确认上传并启动');
    await screen.findByText('合成部署输出');
    expect(call).toHaveBeenCalledWith('custom.run', { hostId: host.id, appId: app.id, action: 'uploadAndStart', confirmed: true, token: 'upload-grant' });
  });

  it('设置保存后才通知主题，旧库按默认仅主机范围预览和导入', async () => {
    const preview: MigrationPreview = { previewId: 'preview-test', totals: { hosts: 1 }, needsCredentials: 1, warnings: [], changes: [{ table: 'hosts', key: 'old-1', kind: 'add', label: '旧测试主机' }] };
    const onTheme = vi.fn();
    const onImport = vi.fn();
    const { api, call } = fixture({ 'settings.get': () => settings, 'settings.save': () => ({ ...settings, theme: 'light' }), 'dialog.pick': () => ({ token: 'fixture-db', name: 'sample.db' }),
      'migration.preview': () => preview, 'migration.apply': () => ({ imported: { hosts: 1 }, skipped: {}, message: '合成导入报告' }) });
    render(pane(api, 'settings', { onTheme, onImport }));
    fireEvent.change(await screen.findByLabelText('工作台主题'), { target: { value: 'light' } });
    expect(onTheme).not.toHaveBeenCalled();
    fireEvent.click(button('保存设置'));
    await screen.findByText('工作台设置已保存。');
    expect(onTheme).toHaveBeenCalledExactlyOnceWith('light');
    await waitFor(() => expect((button('旧版数据') as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(button('旧版数据'));
    fireEvent.click(button('选择快照并预览'));
    await screen.findByText('旧测试主机');
    expect(call).toHaveBeenCalledWith('migration.preview', { token: 'fixture-db' });
    expect(call.mock.calls.some(([method]) => method === 'migration.apply')).toBe(false);
    fireEvent.click(button('核对范围并导入'));
    accept('确认导入');
    await screen.findByText('合成导入报告');
    expect(call).toHaveBeenCalledWith('migration.apply', { previewId: preview.previewId, hostsOnly: true, acceptUpdates: false });
    expect(onImport).toHaveBeenCalledTimes(1);
  });

  it('更新检查在手动触发后执行，取不到版本不误报无更新', async () => {
    const { api, call } = fixture({ 'settings.get': () => settings,
      'settings.updateCheck': () => ({ current: '0.1.0', available: false, message: '未配置合成更新端点' }) });
    render(pane(api, 'settings'));
    await screen.findByLabelText('工作台主题');
    expect(call.mock.calls.some(([method]) => method === 'settings.updateCheck')).toBe(false);
    fireEvent.click(button('检查更新'));
    await screen.findByText('未配置合成更新端点');
    expect(screen.getByText('未确认')).toBeTruthy();
    expect(screen.queryByText('无可用更新')).toBeNull();
  });

  it('桌面客户端更新支持检查、下载并在确认后触发安装重启', async () => {
    const base = { currentVersion: '0.1.1', platform: 'win32', arch: 'x64', repo: 'wy32428011/RelayDock' };
    const { api, call } = fixture({
      'settings.get': () => settings,
      'update.status': () => ({ ...base, status: 'idle', canInstall: false, message: '尚未检查 GitHub 新版本。' }),
      'update.check': () => ({
        ...base, status: 'available', latestVersion: '0.1.2', releaseName: 'RelayDock v0.1.2',
        releaseNotes: '新增自动更新能力。', assetName: 'RelayDock-0.1.2-win-x64.exe', assetSize: 10 * 1024 * 1024,
        canInstall: false, message: '发现新版本 v0.1.2。',
      }),
      'update.download': () => ({
        ...base, status: 'downloaded', latestVersion: '0.1.2', assetName: 'RelayDock-0.1.2-win-x64.exe',
        assetSize: 10 * 1024 * 1024, downloadedBytes: 10 * 1024 * 1024, percent: 100, verified: true,
        canInstall: true, message: '新版本 v0.1.2 已下载并通过 SHA-256 校验。',
      }),
      'update.install': () => ({ ok: true }),
    });
    render(pane(api, 'settings'));
    await screen.findByText('尚未检查 GitHub 新版本。');
    fireEvent.click(button('检查桌面更新'));
    await screen.findByText('新增自动更新能力。');
    fireEvent.click(button('下载安装包'));
    await screen.findByText('新版本 v0.1.2 已下载并通过 SHA-256 校验。');
    fireEvent.click(button('立即安装并重启'));
    expect(call.mock.calls.some(([method]) => method === 'update.install')).toBe(false);
    accept('立即安装并重启');
    await waitFor(() => expect(call).toHaveBeenCalledWith('update.install', {}));
  });

  it('保留远端卷根与文本换行，并处理已知脱敏格式', () => {
    expect(parentRemotePath('/C:/Users')).toBe('/C:/');
    expect(parentRemotePath('/C:/')).toBe('/C:/');
    expect(restoreNewlines('a\r\nb\nc\r\n', 'a\ninsert\nb\nc\n')).toBe('a\r\ninsert\r\nb\nc\r\n');
    expect(redactExport('Authorization: Bearer sample\n"proxyPassword": "placeholder two"')).toBe('Authorization: Bearer [已脱敏]\n"proxyPassword": [已脱敏]');
  });
});
