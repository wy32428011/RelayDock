import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createReadStream, createWriteStream } from 'node:fs';
import { lstat, mkdir, rename, rm, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { VERSION } from '@relaydock/protocol';
import type { CoreEvent, UpdateState } from '@relaydock/protocol';
import { BridgeError } from './faults';
import { isRecord } from './protocol';

export const DEFAULT_UPDATE_REPO = 'wy32428011/RelayDock';
export const DEFAULT_UPDATE_API_URL = `https://api.github.com/repos/${DEFAULT_UPDATE_REPO}/releases/latest`;

const MAX_INSTALLER_BYTES = 512 * 1024 * 1024;
const MAX_CHECKSUMS_BYTES = 256 * 1024;
const MAX_RELEASE_NOTES_CHARS = 4000;
const REQUEST_TIMEOUT_MS = 15_000;
const MAX_REDIRECTS = 5;
const SAFE_WIN_INSTALLER_NAME = /^RelayDock-[0-9A-Za-z._-]+-win-(x64|arm64)\.exe$/u;

export interface ReleaseAsset {
  name: string;
  browser_download_url: string;
  size: number;
}

interface ParsedSemver {
  major: number;
  minor: number;
  patch: number;
  prerelease: string[];
}

interface PendingRelease {
  version: string;
  releaseName?: string;
  releaseNotes?: string;
  releaseUrl?: string;
  publishedAt?: string;
  asset?: ReleaseAsset;
  checksumsAsset?: ReleaseAsset;
}

export interface AppUpdaterOptions {
  currentVersion: string;
  platform: string;
  arch: string;
  updatesDir: string;
  publish: (event: CoreEvent) => void;
  apiUrl?: string;
  repo?: string;
  allowLoopbackHttp?: boolean;
  spawnInstaller?: (installerPath: string) => void;
}

export function normalizeVersion(raw: string): string {
  return raw.trim().replace(/^[vV]/u, '');
}

function parseSemver(raw: string): ParsedSemver | null {
  const cleaned = normalizeVersion(raw);
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/u.exec(cleaned);
  if (!match) return null;
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] ? match[4].split('.') : [],
  };
}

export function compareSemver(a: string, b: string): number {
  const left = parseSemver(a);
  const right = parseSemver(b);
  if (!left || !right) return normalizeVersion(a).localeCompare(normalizeVersion(b));
  if (left.major !== right.major) return left.major - right.major;
  if (left.minor !== right.minor) return left.minor - right.minor;
  if (left.patch !== right.patch) return left.patch - right.patch;
  if (left.prerelease.length === 0 && right.prerelease.length === 0) return 0;
  if (left.prerelease.length === 0) return 1;
  if (right.prerelease.length === 0) return -1;
  const len = Math.max(left.prerelease.length, right.prerelease.length);
  for (let i = 0; i < len; i++) {
    const l = left.prerelease[i];
    const r = right.prerelease[i];
    if (l === undefined) return -1;
    if (r === undefined) return 1;
    const lNum = /^\d+$/u.test(l);
    const rNum = /^\d+$/u.test(r);
    if (lNum && rNum) {
      const diff = Number(l) - Number(r);
      if (diff !== 0) return diff;
    } else if (lNum !== rNum) {
      return lNum ? -1 : 1;
    } else if (l !== r) {
      return l < r ? -1 : 1;
    }
  }
  return 0;
}

export function parseSha256Sums(text: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const rawLine of text.split(/\r?\n/u)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const match = /^([0-9a-fA-F]{64})\s+\*?([^\s]+)$/u.exec(line);
    if (!match) continue;
    const filename = match[2].replace(/^\.\//u, '');
    map.set(filename, match[1].toLowerCase());
  }
  return map;
}

export function selectReleaseAsset(
  assets: readonly ReleaseAsset[],
  version: string,
  platform: string,
  arch: string,
): { installer?: ReleaseAsset; checksums?: ReleaseAsset } {
  const checksums = assets.find(asset => asset.name === 'SHA256SUMS.txt');
  if (platform !== 'win32' || (arch !== 'x64' && arch !== 'arm64')) {
    return { checksums };
  }
  const expectedName = `RelayDock-${normalizeVersion(version)}-win-${arch}.exe`;
  const exact = assets.find(asset => asset.name === expectedName && SAFE_WIN_INSTALLER_NAME.test(asset.name));
  return { installer: exact, checksums };
}

export function isAllowedUpdateUrl(raw: string, allowLoopbackHttp: boolean): boolean {
  try {
    const parsed = new URL(raw);
    if (parsed.protocol === 'https:') return true;
    if (allowLoopbackHttp && parsed.protocol === 'http:') {
      return parsed.hostname === '127.0.0.1' || parsed.hostname === 'localhost' || parsed.hostname === '::1';
    }
    return false;
  } catch {
    return false;
  }
}

async function sha256File(filePath: string): Promise<string> {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(filePath)) {
    hash.update(chunk as Buffer);
  }
  return hash.digest('hex');
}

function defaultSpawnInstaller(installerPath: string): void {
  const child = spawn(installerPath, [], {
    detached: true,
    stdio: 'ignore',
    windowsHide: false,
  });
  child.unref();
}

export class AppUpdater {
  private readonly currentVersion: string;
  private readonly platform: string;
  private readonly arch: string;
  private readonly updatesDir: string;
  private readonly publish: (event: CoreEvent) => void;
  private readonly apiUrl: string;
  private readonly repo: string;
  private readonly allowLoopbackHttp: boolean;
  private readonly spawnInstaller: (installerPath: string) => void;

  private state: UpdateState;
  private pending?: PendingRelease;
  private downloadedPath?: string;
  private expectedSha256?: string;
  private downloadController?: AbortController;
  private activeDownload?: Promise<UpdateState>;
  private activeCheck?: Promise<UpdateState>;

  constructor(options: AppUpdaterOptions) {
    this.currentVersion = normalizeVersion(options.currentVersion);
    this.platform = options.platform;
    this.arch = options.arch;
    this.updatesDir = options.updatesDir;
    this.publish = options.publish;
    this.repo = options.repo ?? DEFAULT_UPDATE_REPO;
    this.apiUrl = options.apiUrl || DEFAULT_UPDATE_API_URL;
    this.allowLoopbackHttp = Boolean(options.allowLoopbackHttp);
    this.spawnInstaller = options.spawnInstaller ?? defaultSpawnInstaller;
    this.state = {
      status: 'idle',
      currentVersion: this.currentVersion,
      platform: this.platform,
      arch: this.arch,
      repo: this.repo,
      canInstall: false,
      message: '尚未检查 GitHub 新版本。',
    };
  }

  getState(): UpdateState {
    return { ...this.state };
  }

  private setState(next: UpdateState): UpdateState {
    this.state = next;
    this.publish({
      v: VERSION,
      type: 'event',
      topic: 'update.status',
      data: { ...next },
    });
    return this.getState();
  }

  private async fetchChecked(url: string, headers: Record<string, string>, signal?: AbortSignal): Promise<Response> {
    let currentUrl = url;
    for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
      if (!isAllowedUpdateUrl(currentUrl, this.allowLoopbackHttp)) {
        throw new BridgeError('UPDATE_UNSAFE_URL', '更新地址不是受信任的 HTTPS 地址。');
      }
      const response = await fetch(currentUrl, {
        method: 'GET',
        headers,
        redirect: 'manual',
        signal,
      });
      if (response.status >= 300 && response.status < 400) {
        const location = response.headers.get('location');
        if (!location) throw new BridgeError('UPDATE_NETWORK', '更新服务返回了无效的重定向地址。', true);
        currentUrl = new URL(location, currentUrl).toString();
        continue;
      }
      return response;
    }
    throw new BridgeError('UPDATE_NETWORK', '更新请求重定向次数过多，请稍后重试。', true);
  }

  async check(options: { silent?: boolean } = {}): Promise<UpdateState> {
    const silent = Boolean(options.silent);
    if (this.state.status === 'downloading') return this.getState();
    if (this.activeCheck) return this.activeCheck;
    this.activeCheck = this.runCheck(silent).finally(() => {
      this.activeCheck = undefined;
    });
    return this.activeCheck;
  }

  private async runCheck(silent: boolean): Promise<UpdateState> {
    const previous = this.getState();
    if (!silent) {
      this.setState({
        ...this.state,
        status: 'checking',
        canInstall: false,
        message: '正在向 GitHub 查询最新版本…',
      });
    }

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
    try {
      const response = await this.fetchChecked(
        this.apiUrl,
        {
          Accept: 'application/vnd.github+json',
          'User-Agent': `RelayDock/${this.currentVersion}`,
        },
        controller.signal,
      );
      if (response.status === 404) {
        if (silent) return previous;
        return this.setState({
          status: 'up-to-date',
          currentVersion: this.currentVersion,
          platform: this.platform,
          arch: this.arch,
          repo: this.repo,
          canInstall: false,
          message: 'GitHub 仓库尚未发布正式版安装包。',
        });
      }
      if (!response.ok) {
        throw new BridgeError('UPDATE_HTTP', `GitHub 返回状态码 ${response.status}，请稍后重试。`, true);
      }
      const payload = await response.json() as unknown;
      if (!isRecord(payload) || typeof payload.tag_name !== 'string') {
        throw new BridgeError('UPDATE_INVALID_PAYLOAD', 'GitHub 发布信息格式无效。', true);
      }
      const latestVersion = normalizeVersion(payload.tag_name);
      const releaseName = typeof payload.name === 'string' && payload.name.trim() ? payload.name.trim() : `v${latestVersion}`;
      const rawNotes = typeof payload.body === 'string' ? payload.body.trim() : '';
      const releaseNotes = rawNotes.length > MAX_RELEASE_NOTES_CHARS
        ? `${rawNotes.slice(0, MAX_RELEASE_NOTES_CHARS)}\n…`
        : rawNotes || undefined;
      const releaseUrl = typeof payload.html_url === 'string' && isAllowedUpdateUrl(payload.html_url, this.allowLoopbackHttp)
        ? payload.html_url
        : undefined;
      const publishedAt = typeof payload.published_at === 'string' ? payload.published_at : undefined;
      const assets: ReleaseAsset[] = Array.isArray(payload.assets)
        ? payload.assets
          .filter((item): item is Record<string, unknown> => isRecord(item))
          .filter(item =>
            typeof item.name === 'string' &&
            typeof item.browser_download_url === 'string' &&
            typeof item.size === 'number' &&
            Number.isSafeInteger(item.size) &&
            item.size >= 0,
          )
          .map(item => ({
            name: item.name as string,
            browser_download_url: item.browser_download_url as string,
            size: item.size as number,
          }))
        : [];

      if (compareSemver(latestVersion, this.currentVersion) <= 0) {
        this.pending = undefined;
        this.downloadedPath = undefined;
        this.expectedSha256 = undefined;
        return this.setState({
          status: 'up-to-date',
          currentVersion: this.currentVersion,
          platform: this.platform,
          arch: this.arch,
          repo: this.repo,
          latestVersion,
          releaseName,
          releaseNotes,
          releaseUrl,
          publishedAt,
          canInstall: false,
          message: `当前已是最新版本（v${this.currentVersion}）。`,
        });
      }

      const { installer, checksums } = selectReleaseAsset(assets, latestVersion, this.platform, this.arch);
      this.pending = {
        version: latestVersion,
        releaseName,
        releaseNotes,
        releaseUrl,
        publishedAt,
        asset: installer,
        checksumsAsset: checksums,
      };

      if (this.platform !== 'win32') {
        return this.setState({
          status: 'unsupported',
          currentVersion: this.currentVersion,
          platform: this.platform,
          arch: this.arch,
          repo: this.repo,
          latestVersion,
          releaseName,
          releaseNotes,
          releaseUrl,
          publishedAt,
          canInstall: false,
          message: `发现新版本 v${latestVersion}。当前仅支持在 Windows 上应用内下载并安装，请前往 GitHub Releases 获取对应平台包。`,
        });
      }

      if (!installer) {
        return this.setState({
          status: 'unsupported',
          currentVersion: this.currentVersion,
          platform: this.platform,
          arch: this.arch,
          repo: this.repo,
          latestVersion,
          releaseName,
          releaseNotes,
          releaseUrl,
          publishedAt,
          canInstall: false,
          message: `发现新版本 v${latestVersion}，但该版本未提供适配 Windows (${this.arch}) 的安装包。`,
        });
      }

      if (!checksums) {
        return this.setState({
          status: 'error',
          currentVersion: this.currentVersion,
          platform: this.platform,
          arch: this.arch,
          repo: this.repo,
          latestVersion,
          releaseName,
          releaseNotes,
          releaseUrl,
          publishedAt,
          assetName: installer.name,
          assetSize: installer.size,
          canInstall: false,
          message: `发现新版本 v${latestVersion}，但缺少 SHA256SUMS.txt 校验清单，已拒绝自动下载。`,
        });
      }

      // Check if already downloaded and verified in this session.
      if (this.downloadedPath && this.expectedSha256 && this.downloadedPath.endsWith(installer.name)) {
        try {
          const digest = await sha256File(this.downloadedPath);
          if (digest === this.expectedSha256) {
            return this.setState({
              status: 'downloaded',
              currentVersion: this.currentVersion,
              platform: this.platform,
              arch: this.arch,
              repo: this.repo,
              latestVersion,
              releaseName,
              releaseNotes,
              releaseUrl,
              publishedAt,
              assetName: installer.name,
              assetSize: installer.size,
              downloadedBytes: installer.size,
              percent: 100,
              verified: true,
              canInstall: true,
              message: `新版本 v${latestVersion} 已下载并通过 SHA-256 校验，可立即安装。`,
            });
          }
        } catch {
          this.downloadedPath = undefined;
          this.expectedSha256 = undefined;
        }
      }

      return this.setState({
        status: 'available',
        currentVersion: this.currentVersion,
        platform: this.platform,
        arch: this.arch,
        repo: this.repo,
        latestVersion,
        releaseName,
        releaseNotes,
        releaseUrl,
        publishedAt,
        assetName: installer.name,
        assetSize: installer.size,
        canInstall: false,
        message: `发现新版本 v${latestVersion}（${installer.name}），可下载并安装。`,
      });
    } catch (error) {
      if (silent) return previous;
      const message = error instanceof BridgeError
        ? error.fault.message
        : '无法连接 GitHub 检查更新，请检查网络或代理设置后重试。';
      return this.setState({
        status: 'error',
        currentVersion: this.currentVersion,
        platform: this.platform,
        arch: this.arch,
        repo: this.repo,
        canInstall: false,
        message,
      });
    } finally {
      clearTimeout(timer);
    }
  }

  async download(): Promise<UpdateState> {
    if (this.activeDownload) return this.activeDownload;
    if (this.platform !== 'win32') {
      throw new BridgeError('UPDATE_UNSUPPORTED', '当前平台暂不支持应用内自动下载安装。');
    }
    const target = this.pending;
    if (!target?.asset || !target.checksumsAsset) {
      throw new BridgeError('UPDATE_NOT_AVAILABLE', '请先检查更新并确认存在适配当前架构的安装包。');
    }
    const asset = target.asset;
    const checksumsAsset = target.checksumsAsset;
    if (!SAFE_WIN_INSTALLER_NAME.test(asset.name)) {
      throw new BridgeError('UPDATE_INVALID_ASSET', '安装包文件名不符合安全规范，已阻止下载。');
    }
    if (asset.size > MAX_INSTALLER_BYTES) {
      throw new BridgeError('UPDATE_TOO_LARGE', '安装包体积超出允许上限，已阻止下载。');
    }

    const controller = new AbortController();
    this.downloadController = controller;
    this.downloadedPath = undefined;
    this.expectedSha256 = undefined;

    this.activeDownload = this.runDownload(target, asset, checksumsAsset, controller).finally(() => {
      this.downloadController = undefined;
      this.activeDownload = undefined;
    });
    return this.activeDownload;
  }

  private async runDownload(
    target: PendingRelease,
    asset: ReleaseAsset,
    checksumsAsset: ReleaseAsset,
    controller: AbortController,
  ): Promise<UpdateState> {
    const baseState: Omit<UpdateState, 'status' | 'message'> = {
      currentVersion: this.currentVersion,
      platform: this.platform,
      arch: this.arch,
      repo: this.repo,
      latestVersion: target.version,
      releaseName: target.releaseName,
      releaseNotes: target.releaseNotes,
      releaseUrl: target.releaseUrl,
      publishedAt: target.publishedAt,
      assetName: asset.name,
      assetSize: asset.size,
      canInstall: false,
    };

    this.setState({
      ...baseState,
      status: 'downloading',
      downloadedBytes: 0,
      percent: 0,
      verified: false,
      message: '正在获取安装包校验清单（SHA256SUMS.txt）…',
    });

    const partPath = join(this.updatesDir, `${asset.name}.part`);
    const finalPath = join(this.updatesDir, asset.name);

    try {
      await mkdir(this.updatesDir, { recursive: true, mode: 0o700 });
      const dirInfo = await lstat(this.updatesDir);
      if (!dirInfo.isDirectory()) {
        throw new BridgeError('UPDATE_DIR_INVALID', '更新缓存目录无效，无法保存安装包。');
      }

      const checksumResponse = await this.fetchChecked(
        checksumsAsset.browser_download_url,
        { 'User-Agent': `RelayDock/${this.currentVersion}` },
        controller.signal,
      );
      if (!checksumResponse.ok) {
        throw new BridgeError('UPDATE_CHECKSUM_FETCH', '无法下载 SHA256SUMS.txt 校验清单，请稍后重试。', true);
      }
      const checksumText = await checksumResponse.text();
      if (Buffer.byteLength(checksumText, 'utf8') > MAX_CHECKSUMS_BYTES) {
        throw new BridgeError('UPDATE_CHECKSUM_INVALID', '校验清单体积异常，已中止下载。');
      }
      const expected = parseSha256Sums(checksumText).get(asset.name);
      if (!expected) {
        throw new BridgeError('UPDATE_CHECKSUM_MISSING', `校验清单中未找到 ${asset.name} 的 SHA-256 摘要，已中止下载。`);
      }

      const response = await this.fetchChecked(
        asset.browser_download_url,
        { 'User-Agent': `RelayDock/${this.currentVersion}` },
        controller.signal,
      );
      if (!response.ok || !response.body) {
        throw new BridgeError('UPDATE_DOWNLOAD_FAILED', `下载安装包失败（HTTP ${response.status}），请稍后重试。`, true);
      }

      const contentLengthHeader = response.headers.get('content-length');
      const totalBytes = contentLengthHeader && /^\d+$/u.test(contentLengthHeader)
        ? Number(contentLengthHeader)
        : asset.size;
      if (totalBytes > MAX_INSTALLER_BYTES) {
        throw new BridgeError('UPDATE_TOO_LARGE', '安装包体积超出允许上限，已阻止下载。');
      }

      await rm(partPath, { force: true });
      const writer = createWriteStream(partPath, { flags: 'wx', mode: 0o600 });
      const hash = createHash('sha256');
      let downloaded = 0;
      let lastReportAt = 0;
      let lastPercent = -1;

      try {
        const reader = response.body.getReader();
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          if (!value) continue;
          downloaded += value.byteLength;
          if (downloaded > MAX_INSTALLER_BYTES) {
            throw new BridgeError('UPDATE_TOO_LARGE', '安装包下载体积超出允许上限，已中止。');
          }
          hash.update(value);
          if (!writer.write(value)) {
            await new Promise<void>((resolve, reject) => {
              writer.once('drain', resolve);
              writer.once('error', reject);
            });
          }
          const now = Date.now();
          const percent = totalBytes > 0 ? Math.min(99, Math.floor((downloaded * 100) / totalBytes)) : 0;
          if (percent !== lastPercent && (now - lastReportAt >= 120 || percent === 99)) {
            lastReportAt = now;
            lastPercent = percent;
            this.setState({
              ...baseState,
              assetSize: totalBytes || asset.size,
              status: 'downloading',
              downloadedBytes: downloaded,
              percent,
              verified: false,
              message: `正在下载 ${asset.name}（${percent}%）…`,
            });
          }
        }
        await new Promise<void>((resolve, reject) => {
          writer.once('error', reject);
          writer.end(() => resolve());
        });
      } catch (streamError) {
        writer.destroy();
        await rm(partPath, { force: true }).catch(() => undefined);
        throw streamError;
      }

      const actual = hash.digest('hex');
      if (actual !== expected) {
        await rm(partPath, { force: true }).catch(() => undefined);
        throw new BridgeError('UPDATE_CHECKSUM_MISMATCH', '安装包完整性校验失败（SHA-256 不匹配），已丢弃下载文件。');
      }

      await rm(finalPath, { force: true }).catch(() => undefined);
      await rename(partPath, finalPath);
      this.downloadedPath = finalPath;
      this.expectedSha256 = expected;

      return this.setState({
        ...baseState,
        assetSize: downloaded,
        status: 'downloaded',
        downloadedBytes: downloaded,
        percent: 100,
        verified: true,
        canInstall: true,
        message: `新版本 v${target.version} 已下载并通过 SHA-256 校验，点击“立即安装并重启”完成升级。`,
      });
    } catch (error) {
      await rm(partPath, { force: true }).catch(() => undefined);
      if (controller.signal.aborted) {
        return this.setState({
          ...baseState,
          status: 'available',
          downloadedBytes: 0,
          percent: 0,
          verified: false,
          message: `已取消下载，仍可随时重新下载 v${target.version}。`,
        });
      }
      const message = error instanceof BridgeError
        ? error.fault.message
        : '下载更新安装包失败，请检查网络连接后重试。';
      return this.setState({
        ...baseState,
        status: 'error',
        downloadedBytes: 0,
        percent: 0,
        verified: false,
        message,
      });
    }
  }

  cancel(): UpdateState {
    if (this.downloadController) {
      this.downloadController.abort();
      return this.getState();
    }
    return this.getState();
  }

  async prepareInstall(): Promise<string> {
    if (this.platform !== 'win32') {
      throw new BridgeError('UPDATE_UNSUPPORTED', '当前平台暂不支持应用内自动安装。');
    }
    if (this.state.status !== 'downloaded' || !this.downloadedPath || !this.expectedSha256) {
      throw new BridgeError('UPDATE_NOT_DOWNLOADED', '尚未下载并校验完成可用的安装包。');
    }
    const info = await stat(this.downloadedPath).catch(() => null);
    if (!info || !info.isFile()) {
      this.downloadedPath = undefined;
      this.expectedSha256 = undefined;
      this.setState({
        ...this.state,
        status: this.pending ? 'available' : 'error',
        verified: false,
        canInstall: false,
        message: '已下载的安装包不存在，请重新下载。',
      });
      throw new BridgeError('UPDATE_FILE_MISSING', '已下载的安装包不存在，请重新下载。');
    }
    const currentHash = await sha256File(this.downloadedPath);
    if (currentHash !== this.expectedSha256) {
      await rm(this.downloadedPath, { force: true }).catch(() => undefined);
      this.downloadedPath = undefined;
      this.expectedSha256 = undefined;
      this.setState({
        ...this.state,
        status: 'error',
        verified: false,
        canInstall: false,
        message: '安装前复检失败：安装包文件已变动，请重新下载。',
      });
      throw new BridgeError('UPDATE_CHECKSUM_MISMATCH', '安装前复检失败：安装包文件已变动，请重新下载。');
    }
    return this.downloadedPath;
  }

  launchInstaller(installerPath: string): void {
    this.spawnInstaller(installerPath);
  }
}
