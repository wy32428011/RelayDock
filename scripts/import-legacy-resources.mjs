#!/usr/bin/env node
// Generates a reviewed-only manifest from an explicitly staged bundle.
import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { lstat, open, opendir, readFile, realpath } from 'node:fs/promises';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';
import { parseArgs } from 'node:util';

const maxFiles = 4096;
const maxFileBytes = 2 * 1024 ** 3;
const maxBundleBytes = 8 * 1024 ** 3;
const maxManifestBytes = 2 * 1024 ** 2;
const component = /^[A-Za-z0-9._+@%~-]{1,120}$/;
const reserved = /^(?:CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\.|$)/i;
const extension = /\.(?:zip|tar\.gz|tgz|tar\.xz|tar\.bz2|7z|deb|rpm|msi|exe)$/i;
const osVersionKey = /^[0-9]+(?:\.[0-9]+){0,3}$/;
const systemPackageApps = new Set(['mysql', 'mariadb', 'redis', 'nginx', 'rabbitmq', 'mosquitto']);

function safePath(name) {
  const parts = name.split('/');
  return name.length <= 240 && parts.length <= 16 && parts.every((p) => component.test(p) && !p.endsWith('.') && !reserved.test(p));
}

const usage = '用法：node scripts/import-legacy-resources.mjs --dir 绝对目录 --app 应用ID --version 应用版本 --os ubuntu|centos|windows --os-version 系统版本 --arch amd64|arm64 --source 来源 [--entrypoint 包路径] [--requires 文件=依赖文件]…';

async function hashFile(root, name, before, buf) {
  const rel = relative(root, await realpath(name));
  if (!rel || rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) throw new Error('资源文件已移动到授权目录之外');
  const fd = await open(name, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0));
  try {
    const info = await fd.stat({ bigint: true });
    if (!info.isFile() || before.dev !== info.dev || before.ino !== info.ino || before.size !== info.size) throw new Error('资源文件在生成清单时发生变化');
    const hash = createHash('sha256');
    const expected = Number(info.size);
    let n = 0;
    while (n <= expected) {
      const { bytesRead } = await fd.read(buf, 0, Math.min(buf.length, expected + 1 - n), null);
      if (!bytesRead) break;
      n += bytesRead;
      hash.update(buf.subarray(0, bytesRead));
    }
    if (n !== expected || (await fd.stat({ bigint: true })).mtimeNs !== info.mtimeNs) throw new Error('资源文件在生成清单时发生变化');
    return hash.digest('hex');
  } finally {
    await fd.close();
  }
}

function checkPackage(path, os, arch) {
  const n = path.toLowerCase();
  const rpmArch = arch === 'amd64' ? 'x86_64' : 'aarch64';
  if (n.endsWith('.deb') && (os !== 'ubuntu' || !(n.endsWith(`_${arch}.deb`) || n.endsWith('_all.deb')))) throw new Error('DEB 文件与清单目标不匹配：' + path);
  if (n.endsWith('.rpm') && (os !== 'centos' || !(n.endsWith(`.${rpmArch}.rpm`) || n.endsWith('.noarch.rpm')))) throw new Error('RPM 文件与清单目标不匹配：' + path);
}

async function generate() {
  const { values } = parseArgs({ options: {
    dir: { type: 'string' }, app: { type: 'string' }, version: { type: 'string' }, os: { type: 'string' },
    'os-version': { type: 'string' }, arch: { type: 'string' }, source: { type: 'string' },
    entrypoint: { type: 'string' }, requires: { type: 'string', multiple: true }, help: { type: 'boolean' },
  } });
  if (values.help) {
    process.stderr.write(usage + '\n');
    return;
  }
  const { dir, app, version, os, 'os-version': osVersion, arch, source, entrypoint } = values;
  if (!dir || !isAbsolute(dir) || !app || !version || !os || !osVersion || !arch || !source) throw new Error(usage);
  const { applications } = JSON.parse(await readFile(new URL('../resources/catalog.json', import.meta.url), 'utf8'));
  const item = applications.find(({ id }) => id === app);
  if (!item?.versions.includes(version) || !item.platforms.includes(os) || !['amd64', 'arm64'].includes(arch) || osVersion.length > 48 || !osVersionKey.test(osVersion)) throw new Error('应用、版本、目标系统或 CPU 架构不受支持');
  if (os === 'centos' && /^7(?:\.|$)/.test(osVersion) && arch === 'arm64' && systemPackageApps.has(app)) throw new Error('未开放该应用的 CentOS 7 ARM64 系统包');
  if (!source.trim() || Buffer.byteLength(source) > 256 || /[\p{Cc}\p{Cf}]/u.test(source)) throw new Error('来源必须是可读文本（最多 256 UTF-8 字节）');
  const input = resolve(dir);
  if (!(await lstat(input)).isDirectory()) throw new Error('输入必须是实际目录，不能是链接');
  const root = await realpath(input);
  if (root.startsWith('\\\\') || root.startsWith('//')) throw new Error('资源必须是本机目录，不能是网络共享');

  let total = 0;
  const files = [];
  const names = new Set();
  const buf = Buffer.alloc(128 * 1024);
  async function walk(current, prefix) {
    let found = false;
    for await (const entry of await opendir(current)) {
      found = true;
      const rel = prefix + entry.name;
      const folded = rel.toLowerCase();
      if (!safePath(rel) || folded === 'manifest.json' || names.has(folded)) throw new Error('不允许的路径、已有清单或大小写冲突：' + rel);
      if (names.size >= maxFiles * 16) throw new Error('资源目录项过多');
      names.add(folded);
      const absolute = join(current, entry.name);
      const info = await lstat(absolute, { bigint: true });
      if (info.isDirectory()) {
        if ((await realpath(absolute)) !== absolute) throw new Error('目录在生成清单时发生变化：' + rel);
        await walk(absolute, rel + '/');
      } else if (info.isFile()) {
        checkPackage(rel, os, arch);
        const size = Number(info.size);
        total += size;
        if (size > maxFileBytes || total > maxBundleBytes || files.length >= maxFiles) throw new Error('资源文件数量或大小超过限制');
        files.push({ path: rel, size, sha256: await hashFile(root, absolute, info, buf), dependencies: [] });
      } else {
        throw new Error('资源只允许普通文件和目录，不能含链接：' + rel);
      }
    }
    if (!found) throw new Error('资源不能包含空目录');
  }
  await walk(root, '');
  files.sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
  const byPath = new Map(files.map((file) => [file.path, file]));
  const requires = values.requires ?? [];
  if (requires.length > 4 * maxFiles) throw new Error('依赖数量超过限制');
  for (const relation of requires) {
    const [file, dep, extra] = relation.split('=');
    const item = byPath.get(file);
    if (!item || !byPath.has(dep) || extra !== undefined || file === dep || item.dependencies.includes(dep)) throw new Error('无效或重复的依赖：' + relation);
    item.dependencies.push(dep);
  }
  if (entrypoint !== undefined && (!byPath.get(entrypoint)?.size || !extension.test(entrypoint))) throw new Error('entrypoint 必须是普通包文件的相对路径，不能是脚本');
  if (os === 'ubuntu' && arch === 'arm64' && systemPackageApps.has(app) && !files.some(({ path }) => path === 'Packages' || path.endsWith('/Packages'))) throw new Error('此 Ubuntu ARM64 组合必须带 Packages 索引');
  const manifest = JSON.stringify({ schema: 1, appId: app, version, os, osVersion, arch, source, entrypoint, files }, null, 2) + '\n';
  if (Buffer.byteLength(manifest) > maxManifestBytes) throw new Error('生成的清单超过大小限制');
  process.stdout.write(manifest);
}

try {
  await generate();
} catch (error) {
  process.stderr.write(`生成离线资源清单失败：${error.message}\n`);
  process.exitCode = 1;
}
