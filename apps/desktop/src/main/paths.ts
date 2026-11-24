import { lstatSync, mkdirSync, realpathSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, isAbsolute, join, normalize, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { BridgeError } from './faults';

export const PRODUCT_NAME = 'RelayDock';
export const APP_ID = 'com.relaydock.desktop';

export function localPath(value: string): string | undefined {
  if (!isAbsolute(value)) return undefined;
  for (const character of value) {
    const code = character.charCodeAt(0);
    if (code < 32 || code === 127) return undefined;
  }
  if (process.platform === 'win32') {
    // Drive-rooted paths only: exclude UNC/device namespaces and NTFS streams.
    if (!/^[a-z]:[/\\]/i.test(value) || /[:*?<>|"]/u.test(value.slice(2))) return undefined;
    for (const piece of value.slice(3).split(/[/\\]/u)) {
      if (piece === '.' || piece === '..') continue;
      if (/[. ]$/u.test(piece) || /^(?:con|prn|aux|nul|(?:com|lpt)[1-9¹²³])(?:\.|$)/iu.test(piece)) return undefined;
    }
  }
  return normalize(value);
}

export function below(root: string, target: string): boolean {
  const prefix = root.endsWith(sep) ? root : `${root}${sep}`;
  return target !== root && target.startsWith(prefix);
}

export function rendererPath(rawUrl: string): string | undefined {
  try {
    const url = new URL(rawUrl);
    if (url.protocol !== 'file:' || url.host || url.username || url.password || url.search) return undefined;
    return localPath(fileURLToPath(url));
  } catch {
    return undefined;
  }
}

export function rendererRequestAllowed(entry: string, root: string, url: string, type: string): boolean {
  const candidate = rendererPath(url);
  if (!candidate || type === 'subFrame') return false;
  if (type === 'mainFrame' && candidate !== entry) return false;
  if (!below(root, candidate)) return false;
  try {
    // Check both the URL path and its target so an asset symlink cannot open
    // files outside the renderer bundle. Electron's fs supports paths in ASAR.
    return below(realpathSync(root), realpathSync(candidate));
  } catch {
    return false;
  }
}

export function userDataPath(appData: string, override?: string): string {
  if (override !== undefined) {
    try {
      const input = localPath(override);
      if (!input) throw new Error();
      const target = realpathSync(input);
      if (!below(realpathSync(tmpdir()), target) || !basename(target).startsWith('relaydock-smoke-') || !statSync(target).isDirectory()) {
        throw new Error();
      }
      return target;
    } catch {
      // Never fall back to the real profile if a smoke run was requested.
      throw new BridgeError('INVALID_TEST_PROFILE', '测试数据目录必须是系统临时目录下已创建的 relaydock-smoke- 目录。');
    }
  }
  const target = join(appData, PRODUCT_NAME);
  mkdirSync(target, { recursive: true, mode: 0o700 });
  if (!lstatSync(target).isDirectory()) throw new BridgeError('INVALID_PROFILE', '新版数据目录不能是文件或链接。');
  return target;
}

export interface CorePaths { executable: string; data: string; resources: string }

export function corePaths(packaged: boolean, resourcesPath: string, env: NodeJS.ProcessEnv): Omit<CorePaths, 'data'> {
  const filename = process.platform === 'win32' ? 'installer-core.exe' : 'installer-core';
  const command = localPath(packaged
    ? process.platform === 'darwin' ? join(resourcesPath, '..', 'Helpers', filename) : join(resourcesPath, 'bin', filename)
    : env.RELAYDOCK_CORE ?? '');
  const resources = localPath(packaged ? join(resourcesPath, 'resources') : env.RELAYDOCK_RESOURCES ?? '');
  if (!command || !resources) {
    throw new BridgeError('CORE_LOCATION', '没有找到本地核心或资源的安装位置，请通过启动脚本打开应用或检查安装。');
  }
  return { executable: command, resources };
}
