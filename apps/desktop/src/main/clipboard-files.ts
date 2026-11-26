import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export interface ClipboardItemLike {
  readonly types: readonly string[];
  getType(type: string): Promise<Blob | unknown>;
}

export interface ClipboardReader {
  has(mimetype: string): Promise<boolean>;
  read(): Promise<readonly ClipboardItemLike[]>;
}

export const MAX_CLIPBOARD_FILES = 64;

const osFormat = (format: string) => `electron application/osclipboard;format="${format}"`;

function decodeXmlEntities(value: string): string {
  return value
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, '\'')
    .replace(/&amp;/g, '&');
}

export function parseUtf16PathBuffer(buffer: Buffer): string[] {
  if (!buffer || buffer.length < 2) return [];
  const evenLength = buffer.length - (buffer.length % 2);
  const text = buffer.subarray(0, evenLength).toString('utf16le');
  return text
    .split('\0')
    .map(item => item.trim())
    .filter(Boolean);
}

export function parseUriList(raw: string): string[] {
  const out: string[] = [];
  for (const line of raw.split(/\r?\n/u)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#') || trimmed === 'copy' || trimmed === 'cut') continue;
    if (trimmed.startsWith('file://')) {
      try {
        const parsed = fileURLToPath(trimmed);
        if (parsed) out.push(parsed);
      } catch {
        // Ignore malformed file URIs from external applications.
      }
    }
  }
  return out;
}

export function parseMacPlistFilenames(raw: string): string[] {
  const out: string[] = [];
  const pattern = /<string>([\s\S]*?)<\/string>/gu;
  for (const match of raw.matchAll(pattern)) {
    const decoded = decodeXmlEntities(match[1] ?? '').trim();
    if (decoded) out.push(decoded);
  }
  return out;
}

function dedupeAndCap(paths: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of paths) {
    const trimmed = item.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    out.push(trimmed);
    if (out.length >= MAX_CLIPBOARD_FILES) break;
  }
  return out;
}

async function readFormatBuffer(reader: ClipboardReader, type: string): Promise<Buffer | undefined> {
  try {
    const items = await reader.read();
    for (const item of items) {
      if (!item.types.includes(type)) continue;
      const data = await item.getType(type);
      if (data instanceof Blob) {
        return Buffer.from(await data.arrayBuffer());
      }
    }
  } catch {
    // Ignore transient clipboard access errors from external applications.
  }
  return undefined;
}

function readWindowsDropListViaPowerShell(): Promise<string[]> {
  return new Promise(resolve => {
    const script = [
      '[Console]::OutputEncoding=[System.Text.Encoding]::UTF8',
      'Add-Type -AssemblyName System.Windows.Forms',
      '$items=[System.Windows.Forms.Clipboard]::GetFileDropList()',
      'if ($items) { $items | ForEach-Object { $_ } }',
    ].join('; ');
    execFile(
      'powershell.exe',
      ['-NoProfile', '-NonInteractive', '-STA', '-Command', script],
      { timeout: 2500, maxBuffer: 1024 * 1024, windowsHide: true, encoding: 'utf8' },
      (error, stdout) => {
        if (error || !stdout) {
          resolve([]);
          return;
        }
        const lines = stdout
          .replace(/^﻿/u, '')
          .split(/\r?\n/u)
          .map(line => line.trim())
          .filter(Boolean);
        resolve(lines);
      },
    );
  });
}

export async function readClipboardFilePaths(
  reader: ClipboardReader,
  platform: NodeJS.Platform = process.platform,
): Promise<string[]> {
  if (platform === 'win32') {
    const fileNameType = osFormat('FileNameW');
    let hasFileDrop = false;
    try {
      hasFileDrop = await reader.has(fileNameType);
    } catch {
      hasFileDrop = false;
    }
    if (hasFileDrop) {
      const viaPs = await readWindowsDropListViaPowerShell();
      if (viaPs.length > 0) return dedupeAndCap(viaPs);
      const fileNameBuf = await readFormatBuffer(reader, fileNameType);
      if (fileNameBuf && fileNameBuf.length > 0) {
        return dedupeAndCap(parseUtf16PathBuffer(fileNameBuf));
      }
    }
    const uriBuf = await readFormatBuffer(reader, 'text/uri-list');
    if (uriBuf && uriBuf.length > 0) return dedupeAndCap(parseUriList(uriBuf.toString('utf8')));
    return [];
  }
  if (platform === 'darwin') {
    const plistBuf = await readFormatBuffer(reader, osFormat('NSFilenamesPboardType'));
    if (plistBuf && plistBuf.length > 0) {
      const parsed = parseMacPlistFilenames(plistBuf.toString('utf8'));
      if (parsed.length > 0) return dedupeAndCap(parsed);
    }
    const urlBuf = await readFormatBuffer(reader, osFormat('public.file-url'))
      ?? await readFormatBuffer(reader, 'text/uri-list');
    if (urlBuf && urlBuf.length > 0) return dedupeAndCap(parseUriList(urlBuf.toString('utf8')));
    return [];
  }
  const gnomeBuf = await readFormatBuffer(reader, osFormat('x-special/gnome-copied-files'));
  if (gnomeBuf && gnomeBuf.length > 0) {
    const parsed = parseUriList(gnomeBuf.toString('utf8'));
    if (parsed.length > 0) return dedupeAndCap(parsed);
  }
  const uriBuf = await readFormatBuffer(reader, 'text/uri-list');
  if (uriBuf && uriBuf.length > 0) {
    return dedupeAndCap(parseUriList(uriBuf.toString('utf8')));
  }
  return [];
}
