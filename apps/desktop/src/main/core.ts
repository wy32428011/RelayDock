import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { MAX_FRAME, VERSION, type CoreEvent, type CoreMethods, type ElectronMethods } from '../../../../packages/protocol/index';
import { coreFault, DesktopFault } from './faults';
import type { CorePaths } from './paths';
import { encodeRequest, FrameReader, isRecord, type CoreFrame } from './protocol';

export type CoreStatus = ElectronMethods['core.status'][1];
const handshakeTimeout = 15000;
const defaultTimeout = 120000;
const transferTimeout = 30 * 60 * 1000;
const transferMethods = new Set<keyof CoreMethods>(['files.upload', 'files.download', 'files.transfer', 'files.archive', 'files.extract', 'custom.run', 'resource.inspect', 'resource.import', 'migration.preview', 'migration.apply']);

type Pending = { resolve: (result: unknown) => void; reject: (error: DesktopFault) => void; timer: NodeJS.Timeout };

function helloVersion(value: unknown): string {
  if (!isRecord(value)) throw new DesktopFault('BAD_FRAME');
  const platform = process.platform === 'win32' ? 'windows' : process.platform;
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  if (value.selected !== VERSION || value.platform !== platform || value.arch !== arch) throw new DesktopFault('VERSION_MISMATCH');
  if (typeof value.version !== 'string' || !/^[a-zA-Z0-9._+-]{1,64}$/u.test(value.version) ||
      !Array.isArray(value.capabilities) || value.capabilities.length > 256 ||
      !value.capabilities.every(capability => typeof capability === 'string' && /^[a-zA-Z0-9._-]{1,80}$/u.test(capability))) {
    throw new DesktopFault('BAD_FRAME');
  }
  return value.version;
}

function closeWithin(closed: Promise<void>, ms: number): Promise<boolean> {
  return new Promise(resolve => {
    const timer = setTimeout(() => resolve(false), ms);
    void closed.then(() => { clearTimeout(timer); resolve(true); });
  });
}

export class CoreProcess {
  private statusValue: CoreStatus = { ready: false, platform: process.platform, arch: process.arch, detail: '正在启动本地核心…' };
  private started = false;
  private stopping = false;
  private failed = false;
  private child?: ChildProcessWithoutNullStreams;
  private closed?: Promise<void>;
  private shutdown?: Promise<void>;
  private counter = 0;
  private pending = new Map<string, Pending>();
  private expired = new Set<string>();

  constructor(private readonly emit: (event: CoreEvent) => void) {}

  get status(): CoreStatus { return this.statusValue; }
  get hasPendingRequests(): boolean { return this.pending.size > 0 || this.expired.size > 0; }

  async start(paths: CorePaths | null): Promise<void> {
    if (this.started || this.stopping) return;
    this.started = true;
    if (!paths) {
      this.fail('本地核心或资源的启动路径无效。请检查开发启动命令或重新安装应用。');
      return;
    }
    try {
      const child = spawn(paths.executable, ['-data', paths.data, '-resources', paths.resources], {
        cwd: paths.data, shell: false, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
      });
      this.child = child;
      this.closed = new Promise(resolve => child.once('close', () => {
        this.child = undefined;
        resolve();
        if (!this.failed && !this.stopping) this.fail('本地核心已退出。请重启应用；未完成的远端操作需要复查。');
      }));
      child.on('error', (error: NodeJS.ErrnoException) => {
        const details = error.code === 'ENOENT' ? '未找到本地核心程序，请重新安装应用或先构建本地核心。' :
          error.code === 'EACCES' || error.code === 'EPERM' ? '无法运行本地核心，请检查程序权限。' :
          '无法启动本地核心，请检查应用安装和系统架构。';
        this.fail(details);
      });
      child.stdin.on('error', () => this.fail('与本地核心的连接已中断；请重启应用并复查任务。'));
      child.stdout.on('error', () => this.fail('读取本地核心响应失败；请重启应用并复查任务。'));
      // Drain stderr without logging or surfacing raw local/core diagnostics.
      child.stderr.on('error', () => {});
      child.stderr.resume();
      const reader = new FrameReader();
      child.stdout.on('data', (chunk: Buffer) => {
        if (this.failed || this.stopping) return;
        try {
          reader.push(chunk, frame => this.receive(frame));
        } catch (error) {
          this.fail(error instanceof DesktopFault && error.fault.code === 'VERSION_MISMATCH'
            ? error.message : '本地核心返回了无效或过大的消息。请重启应用并检查任务状态。');
        }
      });
      child.stdout.on('end', () => {
        if (this.stopping || this.failed) return;
        try { reader.finish(); } catch {
          this.fail('本地核心在发送消息时中断。请重启应用并检查任务状态。');
          return;
        }
        // A closed pipe cannot carry further replies even if the child is still alive.
        this.fail('本地核心已断开。请重启应用；未完成的远端操作需要复查。');
      });
      const version = helloVersion(await this.request('hello', { supported: [VERSION] }, handshakeTimeout));
      if (this.failed || this.stopping) return;
      this.updateStatus({ ready: true, version, detail: `本地核心 ${version} · IPC v${VERSION}` });
    } catch (error) {
      if (this.failed || this.stopping) return;
      const reason = error instanceof DesktopFault ? error.fault.code : '';
      this.fail(reason === 'VERSION_MISMATCH' ? new DesktopFault('VERSION_MISMATCH').message :
        reason === 'CORE_TIMEOUT' ? '本地核心握手超时。请重启应用并检查本机数据目录的权限。' :
        '本地核心未完成握手。请检查应用安装和本机数据目录的权限。');
    }
  }

  call<K extends keyof CoreMethods>(method: K, params: CoreMethods[K][0], timeout?: number): Promise<CoreMethods[K][1]> {
    if (!this.status.ready || this.stopping || this.failed) return Promise.reject(new DesktopFault('CORE_UNAVAILABLE'));
    return this.request(method, params, timeout ?? (transferMethods.has(method) ? transferTimeout : defaultTimeout)) as Promise<CoreMethods[K][1]>;
  }

  stop(): Promise<void> {
    if (!this.stopping) {
      this.stopping = true;
      this.updateStatus({ ready: false, detail: '正在关闭本地核心…' });
      this.rejectPending();
    }
    return this.stopChild();
  }

  forceKill(): void {
    if (this.child && this.child.exitCode === null && this.child.signalCode === null) {
      try { this.child.kill('SIGKILL'); } catch { /* The child may already be gone. */ }
    }
  }

  private updateStatus(partial: Pick<CoreStatus, 'ready' | 'version' | 'detail'>): void {
    this.statusValue = { platform: process.platform, arch: process.arch, ...partial };
    this.emit({ v: VERSION, type: 'event', topic: 'core.status', data: this.statusValue });
  }

  private async request(method: string, params: unknown, timeout: number): Promise<unknown> {
    const lookup = this.child;
    if (!lookup || !lookup.stdin.writable || this.failed || this.stopping) throw new DesktopFault('CORE_UNAVAILABLE');
    if (this.pending.size >= 32) throw new DesktopFault('BUSY', true);
    const id = `r${++this.counter}`;
    const bytes = encodeRequest(id, method, params);
    if (lookup.stdin.writableLength + bytes.length > MAX_FRAME * 2) throw new DesktopFault('BUSY', true);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        if (!this.pending.delete(id)) return;
        this.expired.add(id);
        reject(new DesktopFault('CORE_TIMEOUT'));
        if (this.expired.size > 128) this.fail('本地核心有过多未响应的请求。请重启应用并复查任务。');
      }, timeout);
      this.pending.set(id, { resolve, reject, timer });
      try {
        lookup.stdin.write(bytes, error => {
          if (error) this.fail('发送本地请求失败；请重启应用并复查任务。');
        });
      } catch {
        this.fail('发送本地请求失败；请重启应用并复查任务。');
      }
    });
  }

  private receive(frame: CoreFrame): void {
    if (frame.type === 'event') {
      if (this.status.ready) this.emit(frame);
      return;
    }
    if (this.expired.delete(frame.id)) return;
    const request = this.pending.get(frame.id);
    if (!request) throw new DesktopFault('BAD_FRAME');
    this.pending.delete(frame.id);
    clearTimeout(request.timer);
    if (frame.ok) request.resolve(frame.result);
    else request.reject(coreFault(frame.error));
  }

  private rejectPending(): void {
    for (const request of this.pending.values()) {
      clearTimeout(request.timer);
      request.reject(new DesktopFault('CORE_UNAVAILABLE'));
    }
    this.pending.clear();
    this.expired.clear();
  }

  private fail(detail: string): void {
    if (this.failed || this.stopping) return;
    this.failed = true;
    this.rejectPending();
    this.updateStatus({ ready: false, detail });
    void this.stopChild();
  }

  private stopChild(): Promise<void> {
    const child = this.child;
    const closed = this.closed;
    if (this.shutdown) return this.shutdown;
    if (!child || !closed) return Promise.resolve();
    this.shutdown = (async () => {
      // EOF lets Go clean up. A broken protocol must not flush requests queued in Node.
      if (this.failed) child.stdin.destroy();
      else child.stdin.end();
      if (await closeWithin(closed, 3000)) return;
      try { child.kill('SIGTERM'); } catch { /* Race with process exit. */ }
      if (await closeWithin(closed, 1000)) return;
      this.forceKill();
      await closeWithin(closed, 1000);
      child.stdin.destroy();
      child.stdout.destroy();
      child.stderr.destroy();
      child.unref();
    })();
    return this.shutdown;
  }
}
