import { randomBytes, randomUUID } from 'node:crypto';
import type { BigIntStats } from 'node:fs';
import { lstat, open, realpath, rename, unlink, writeFile } from 'node:fs/promises';
import { basename, dirname, join } from 'node:path';
import type { CoreMethods } from '@relaydock/protocol';
import { BridgeError } from './faults';
import { isRecord } from './protocol';
import { isFileToken, isUploadAction } from './validation';
import type { Purpose } from './validation';
import { localPath } from './paths';

const TOKEN_TTL = 10 * 60_000;
const MAX_GRANTS = 64;
/** A distinct scope for the window's current document, not a renderer-supplied ID. */
export type GrantScope = object;
interface Selection {
  file: string;
  parent: BigIntStats;
  target?: BigIntStats;
}
interface Grant extends Selection {
  purpose: Purpose;
  scope: GrantScope;
  expiresAt: number;
}

const writes = (purpose: Purpose) => purpose === 'export' || purpose === 'download';
const directory = (purpose: Purpose) => purpose === 'uploadDirectory' || purpose === 'resource';
const tokenFault = () => new BridgeError('DIALOG_TOKEN_INVALID', '文件授权已失效或不适用于此操作，请重新选择。');
const fileChanged = () => new BridgeError('LOCAL_PATH_CHANGED', '所选文件或目录已变化，请重新选择。');

function sameObject(a: BigIntStats, b: BigIntStats): boolean {
  return a.dev === b.dev && a.ino === b.ino && a.mode === b.mode;
}
function unchanged(a?: BigIntStats, b?: BigIntStats): boolean {
  return a && b ? sameObject(a, b) && a.mtimeNs === b.mtimeNs && a.size === b.size : a === b;
}
async function statIfPresent(file: string): Promise<BigIntStats | undefined> {
  try {
    return await lstat(file, { bigint: true });
  } catch (error) {
    if (error && typeof error === 'object' && 'code' in error && error.code === 'ENOENT') return undefined;
    throw error;
  }
}
async function selection(input: string, purpose: Purpose): Promise<Selection> {
  const source = localPath(input);
  const name = source && basename(source);
  if (!source || !name) throw fileChanged();
  try {
    if (!writes(purpose) && (await lstat(source)).isSymbolicLink()) throw fileChanged();
    // For a save dialog the file may not yet exist. Resolve the parent only,
    // and never grant a write through an existing symlink or special file.
    const selected = localPath(writes(purpose) ? join(await realpath(dirname(source)), name) : await realpath(source));
    if (!selected) throw fileChanged();
    const parent = await lstat(dirname(selected), { bigint: true });
    const target = await statIfPresent(selected);
    if (!parent.isDirectory() || (directory(purpose) ? !target?.isDirectory() :
      target ? !target.isFile() : !writes(purpose))) throw fileChanged();
    return { file: selected, parent, target };
  } catch {
    throw fileChanged();
  }
}
async function checkSelection(grant: Grant): Promise<void> {
  const current = await selection(grant.file, grant.purpose);
  if (current.file !== grant.file || !sameObject(current.parent, grant.parent) || !unchanged(current.target, grant.target)) {
    throw fileChanged();
  }
}

export class FileGrants {
  private readonly grants = new Map<string, Grant>();

  constructor(private readonly now = () => performance.now()) {}

  private prune() {
    const now = this.now();
    for (const [token, grant] of this.grants) if (now >= grant.expiresAt) this.grants.delete(token);
  }

  async issue(scope: GrantScope, purpose: Purpose, path: string): Promise<{ token: string; name: string }> {
    const selected = await selection(path, purpose);
    this.prune();
    if (this.grants.size >= MAX_GRANTS) this.grants.delete(this.grants.keys().next().value!);
    const token = randomBytes(32).toString('base64url');
    this.grants.set(token, { ...selected, purpose, scope, expiresAt: this.now() + TOKEN_TTL });
    return { token, name: basename(path) };
  }

  revoke(scope: GrantScope): void {
    for (const [token, grant] of this.grants) if (grant.scope === scope) this.grants.delete(token);
  }

  private async take(token: unknown, scope: GrantScope, purposes: readonly Purpose[], consume: boolean): Promise<Grant> {
    if (!isFileToken(token)) throw tokenFault();
    this.prune();
    const grant = this.grants.get(token);
    if (!grant || grant.scope !== scope || !purposes.includes(grant.purpose)) throw tokenFault();
    await checkSelection(grant);
    if (this.grants.get(token) !== grant || this.now() >= grant.expiresAt) throw tokenFault();
    if (consume) this.grants.delete(token);
    return grant;
  }

  /** Resolve local authorities only where the protocol calls for them. */
  async translate(method: keyof CoreMethods, params: Record<string, unknown>, scope: GrantScope): Promise<Record<string, unknown>> {
    const local = async (purposes: readonly Purpose[], consume = true) => ({
      ...params, token: (await this.take(params.token, scope, purposes, consume)).file,
    });
    switch (method) {
      case 'host.probe': {
        const host = params.host;
        if (isRecord(host) && host.keyToken !== undefined) {
          if (host.auth !== 'key') throw tokenFault();
          // Trust confirmation can require a second probe with the same key.
          const key = await this.take(host.keyToken, scope, ['key'], false);
          return { ...params, host: { ...host, keyToken: key.file } };
        }
        break;
      }
      case 'resource.inspect': return local(['resource'], false);
      case 'resource.import': return local(['resource']);
      case 'migration.preview': return local(['migration'], false);
      case 'files.upload': return local(['upload', 'uploadDirectory']);
      case 'files.download': return local(['download']);
      case 'custom.run':
        if (isUploadAction(params.action)) {
          return local(['upload', 'uploadDirectory']);
        }
        if (params.token !== undefined) throw tokenFault();
        break;
    }
    return params;
  }

  async export(scope: GrantScope, token: unknown, text: string, stillAllowed: () => boolean): Promise<void> {
    const grant = await this.take(token, scope, ['export'], true);
    if (!stillAllowed()) throw tokenFault();
    const clean = redactExport(text);
    if (!grant.target) {
      // Exclusive creation protects a name that did not exist when picked.
      await writeFile(grant.file, clean, { flag: 'wx', mode: 0o600 });
      return;
    }
    const temporary = join(dirname(grant.file), `.relaydock-${randomUUID()}.tmp`);
    const handle = await open(temporary, 'wx', 0o600);
    try {
      try {
        await handle.writeFile(clean, 'utf8');
      } finally {
        await handle.close();
      }
      await checkSelection(grant);
      if (!stillAllowed()) throw tokenFault();
      await rename(temporary, grant.file);
    } finally {
      await unlink(temporary).catch(() => undefined);
    }
  }
}

// Core logs are already redacted. Filter common credential formats as a
// fallback for text assembled by the renderer, without logging its contents.
export function redactExport(text: string): string {
  return text
    .replace(/-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|$)/gu, '[私钥已脱敏]')
    .replace(/(\b[a-z][a-z0-9+.-]*:\/\/)[^/\s@<>"']+@/giu, '$1[已脱敏]@')
    .replace(/(\bauthorization["']?\s*[:=]\s*["']?(?:basic|bearer)[ \t]+)[^\s,"';]+/giu, '$1[已脱敏]')
    .replace(/(\b(?:[\w-]*(?:password|passwd|passphrase|token|secret)|api[-_ ]?key)["']?\s*[:=]\s*)(?:"(?:\\[^\r\n]|[^"\\\r\n])*"|'(?:\\[^\r\n]|[^'\\\r\n])*'|[^\s,;&]+)/giu, '$1[已脱敏]');
}
