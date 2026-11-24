export function normalizeText(content: string): string {
  return content.replace(/\r\n?/g, '\n');
}

export function changedLines(before: string[], after: string[]) {
  let prefix = 0;
  while (prefix < before.length && prefix < after.length && before[prefix] === after[prefix]) prefix++;
  let suffix = 0;
  while (suffix < before.length - prefix && suffix < after.length - prefix && before[before.length - suffix - 1] === after[after.length - suffix - 1]) suffix++;
  return { prefix, suffix };
}

export function restoreNewlines(original: string, draft: string): string {
  // Textarea exposes LF. Preserve unchanged lines' separators and retain the file's original style.
  const separators = original.match(/\r\n|\r|\n/g);
  if (!separators || separators.every(separator => separator === '\n')) return draft;
  const predominant = new Map<string, number>();
  let defaultSeparator = separators[0]!;
  for (const separator of separators) {
    predominant.set(separator, (predominant.get(separator) ?? 0) + 1);
    if (predominant.get(separator)! > predominant.get(defaultSeparator)!) defaultSeparator = separator;
  }
  const before = normalizeText(original).split('\n');
  const after = draft.split('\n');
  const { prefix, suffix } = changedLines(before, after);
  return after.map((line, i) => {
    if (i === after.length - 1) return line;
    if (i < prefix || before.length === after.length) return line + (separators[i] ?? defaultSeparator);
    if (i >= after.length - suffix) return line + (separators[before.length - after.length + i] ?? defaultSeparator);
    return line + defaultSeparator;
  }).join('');
}

function isNativeWindowsPath(path: string) {
  return /^(?:[a-z]:\\|\\\\)/i.test(path);
}

export function joinRemotePath(directory: string, leaf: string): string {
  const separator = isNativeWindowsPath(directory) ? '\\' : '/';
  return directory.endsWith(separator) ? directory + leaf : directory + separator + leaf;
}

export function parentRemotePath(path: string): string {
  if (path === '/' || /^(?:\/)?[a-z]:[\\/]?$/i.test(path)) return path;
  if (isNativeWindowsPath(path)) {
    const root = path.match(/^(?:[a-z]:\\|\\\\[^\\]+\\[^\\]+\\?)/i)?.[0] ?? '';
    const last = path.replace(/\\+$/, '').lastIndexOf('\\');
    return last < root.length ? root : path.slice(0, last);
  }
  const last = path.replace(/\/+$/, '').lastIndexOf('/');
  if (last < 0) return '.';
  if (last === 0) return '/';
  const parent = path.slice(0, last);
  return /^(?:\/)?[a-z]:$/i.test(parent) ? parent + '/' : parent;
}

export function validLeaf(value: string): boolean {
  return value.trim().length > 0 && value !== '.' && value !== '..' && !/[\/\\\x00-\x1f]/.test(value);
}

const secretNames = 'password|passwd|pwd|secret|token|api[_-]?key|auth[_-]?token|repository[_-]?token|proxy[_-]?password';
export function redactExport(text: string): string {
  // Defense in depth for text already sanitized by the core. Never export renderer form state.
  return text
    .replace(/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g, '[已脱敏的私钥]')
    .replace(/\b(authorization\s*:\s*(?:basic|bearer)\s+)[^\s,;]+/gi, '$1[已脱敏]')
    .replace(/\b([\w+.-]+:\/\/[^\s/:@]+:)[^\s/@]+@/g, '$1[已脱敏]@')
    .replace(new RegExp(`(\\b(?:${secretNames})["']?(?:\\s*[:=]\\s*|\\s+))(?:"[^"\\r\\n]*"|'[^'\\r\\n]*'|[^\\s,;}]+)`, 'gi'), '$1[已脱敏]');
}
