import { spawnSync } from 'node:child_process';
import { mkdirSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const root = resolve(fileURLToPath(new URL('../', import.meta.url)));
export const metadata = JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8'));
export const platforms = { win32:'windows', darwin:'darwin', linux:'linux' };
export const architectures = { x64:'amd64', arm64:'arm64' };

export function corePath(platform=process.platform, arch=process.arch) {
  if (!Object.hasOwn(platforms, platform) || !Object.hasOwn(architectures, arch)) throw new Error(`不支持的构建目标：${platform}/${arch}`);
  return resolve(root, 'build', 'core', `${platform}-${arch}`, platform === 'win32' ? 'installer-core.exe' : 'installer-core');
}

export function buildCore(platform=process.platform, arch=process.arch) {
  const output = corePath(platform, arch);
  mkdirSync(dirname(output), {recursive:true});
  const result = spawnSync('go', ['-C', 'backend', 'build', '-trimpath', '-ldflags', `-s -w -X main.version=${metadata.version}`, '-o', output, './cmd/installer-core'], {
    cwd:root, stdio:'inherit', shell:false,
    env:{...process.env, GOOS:platforms[platform], GOARCH:architectures[arch], CGO_ENABLED:'0'},
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Go 构建失败：${platform}/${arch}`);
  console.log(`Go ${platform}/${arch} → ${output}`);
  return output;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  buildCore(process.argv[2],process.argv[3]);
}
