import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent } from 'react';
import {
  Archive, ArrowDown, ArrowUp, ArrowUpDown, Bookmark, Check, ChevronDown, ChevronLeft, ChevronRight,
  ClipboardPaste, Copy, Download, File, FileEdit, Folder, FolderOpen, FolderPlus, FolderUp,
  Link2, LocateFixed, MoreHorizontal, Pencil, RefreshCw, Scissors, Search, Send, Shield, Trash2, Upload, X,
} from 'lucide-react';
import type { Bridge, FileEntry, Host, Methods } from '@relaydock/protocol';
import { Button, Empty, Notice, canConnect, faultText, formatBytes, formatDate, remoteTarget, requireOk, useAction, useConfirmation } from './ui';
import { joinRemotePath, parentRemotePath, validLeaf } from './text';

type Directory = Methods['files.list'][1];
type SortKey = 'name' | 'modifiedAt' | 'size';
type SortDir = 'asc' | 'desc';

export interface FileClipboardItem {
  path: string;
  name: string;
  directory: boolean;
}

export interface FileClipboard {
  hostId: string;
  hostName: string;
  items: FileClipboardItem[];
  mode: 'copy' | 'move';
  copiedAt: number;
}

type LocalClipboardFile = Methods['clipboard.readFiles'][1][number];

interface ContextMenuState {
  x: number;
  y: number;
  entry?: FileEntry;
}

interface EditorState {
  hostId: string;
  path: string;
  name: string;
  content: string;
  original: string;
  digest: string;
}

interface ScpDialogState {
  sourceEntry: FileEntry;
  targetHostId: string;
  targetDir: string;
  mode: 'copy' | 'move';
  overwrite: boolean;
}

interface ArchiveDialogState {
  kind: 'archive' | 'extract';
  entry: FileEntry;
  destination: string;
  overwrite: boolean;
}

interface ChmodDialogState {
  entry: FileEntry;
  bits: number;
  specialBits: number;
  sudo: boolean;
  fast: boolean;
  recursive: boolean;
}

const PERM_GROUPS = [
  { key: 'user', label: '用户', read: 0o400, write: 0o200, exec: 0o100 },
  { key: 'group', label: '组', read: 0o040, write: 0o020, exec: 0o010 },
  { key: 'other', label: '任何人', read: 0o004, write: 0o002, exec: 0o001 },
] as const;

function formatOctalPerm(bits: number): string {
  return (bits & 0o777).toString(8).padStart(3, '0');
}

function formatCommandMode(bits: number, specialBits: number): string {
  const octal3 = formatOctalPerm(bits);
  const special = (specialBits >> 9) & 0o7;
  return special > 0 ? `${special}${octal3}` : octal3;
}

function formatSymbolicPerm(directory: boolean, bits: number, specialBits: number): string {
  const prefix = directory ? 'd' : '-';
  const uR = bits & 0o400 ? 'r' : '-';
  const uW = bits & 0o200 ? 'w' : '-';
  const uX = specialBits & 0o4000 ? (bits & 0o100 ? 's' : 'S') : (bits & 0o100 ? 'x' : '-');
  const gR = bits & 0o040 ? 'r' : '-';
  const gW = bits & 0o020 ? 'w' : '-';
  const gX = specialBits & 0o2000 ? (bits & 0o010 ? 's' : 'S') : (bits & 0o010 ? 'x' : '-');
  const oR = bits & 0o004 ? 'r' : '-';
  const oW = bits & 0o002 ? 'w' : '-';
  const oX = specialBits & 0o1000 ? (bits & 0o001 ? 't' : 'T') : (bits & 0o001 ? 'x' : '-');
  return `${prefix}${uR}${uW}${uX}${gR}${gW}${gX}${oR}${oW}${oX}`;
}

const BOOKMARKS_KEY = 'relaydock.terminalFiles.bookmarks.v1';

function loadBookmarks(): Record<string, string[]> {
  try {
    const raw = window.localStorage.getItem(BOOKMARKS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object') return {};
    const out: Record<string, string[]> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (Array.isArray(v)) out[k] = v.filter((item): item is string => typeof item === 'string' && item.trim().length > 0);
    }
    return out;
  } catch {
    return {};
  }
}

function saveBookmarks(next: Record<string, string[]>) {
  try {
    window.localStorage.setItem(BOOKMARKS_KEY, JSON.stringify(next));
  } catch { /* ignore storage quota errors */ }
}

function isArchiveFile(name: string): boolean {
  const lower = name.toLowerCase();
  return lower.endsWith('.tar.gz') || lower.endsWith('.tgz') || lower.endsWith('.tar') || lower.endsWith('.zip');
}

function defaultExtractDir(dir: string, name: string): string {
  const lower = name.toLowerCase();
  let stem = name;
  if (lower.endsWith('.tar.gz')) stem = name.slice(0, -7);
  else if (lower.endsWith('.tgz')) stem = name.slice(0, -4);
  else if (lower.endsWith('.tar')) stem = name.slice(0, -4);
  else if (lower.endsWith('.zip')) stem = name.slice(0, -4);
  return stem ? joinRemotePath(dir, stem) : dir;
}

function formatCompactDate(value?: string) {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return formatDate(value);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
}

export function TerminalFilePanel({
  api,
  host,
  hosts,
  clipboard: externalClipboard,
  onClipboardChange,
  onTerminalCd,
}: {
  api: Bridge;
  host?: Host;
  hosts?: Host[];
  clipboard?: FileClipboard;
  onClipboardChange?: (next: FileClipboard | undefined) => void;
  onTerminalCd?: (path: string) => { ok: boolean; message: string };
}) {
  const hostId = host && canConnect(host) ? host.id : undefined;
  const hostPaths = useRef<Map<string, string>>(new Map());
  const [directory, setDirectory] = useState<Directory>();
  const [pathInput, setPathInput] = useState('.');
  const [filterOpen, setFilterOpen] = useState(false);
  const [filter, setFilter] = useState('');
  const [sortKey, setSortKey] = useState<SortKey>('name');
  const [sortDir, setSortDir] = useState<SortDir>('asc');
  const [selectedPaths, setSelectedPaths] = useState<string[]>([]);
  const [activePath, setActivePath] = useState<string>();
  const [anchorPath, setAnchorPath] = useState<string>();
  const [inlineMode, setInlineMode] = useState<'mkdir' | 'rename' | undefined>();
  const [inlineValue, setInlineValue] = useState('');
  const [loadedHostId, setLoadedHostId] = useState<string>();
  const [internalClipboard, setInternalClipboard] = useState<FileClipboard>();
  const [menu, setMenu] = useState<ContextMenuState>();
  const [openSubmenu, setOpenSubmenu] = useState<'scp' | 'more' | undefined>();
  const [bookmarks, setBookmarks] = useState<Record<string, string[]>>(() => loadBookmarks());
  const [bookmarksOpen, setBookmarksOpen] = useState(false);
  const [editor, setEditor] = useState<EditorState>();
  const [scpDialog, setScpDialog] = useState<ScpDialogState>();
  const [archiveDialog, setArchiveDialog] = useState<ArchiveDialogState>();
  const [chmodDialog, setChmodDialog] = useState<ChmodDialogState>();
  const panelRef = useRef<HTMLElement>(null);
  const filterInputRef = useRef<HTMLInputElement>(null);
  const rowRefs = useRef<Map<string, HTMLTableRowElement>>(new Map());
  const windowRefocusedAt = useRef<number>(0);
  const menuRef = useRef<HTMLDivElement>(null);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();

  const clipboard = onClipboardChange ? externalClipboard : internalClipboard;
  const setClipboard = onClipboardChange ?? setInternalClipboard;

  const connectableHosts = useMemo(() => {
    const list = hosts && hosts.length > 0 ? hosts : host ? [host] : [];
    return list.filter(canConnect);
  }, [hosts, host]);

  const otherHosts = useMemo(
    () => connectableHosts.filter(item => item.id !== hostId),
    [connectableHosts, hostId],
  );

  const isCurrentHost = Boolean(hostId && loadedHostId === hostId);
  const activeDir = isCurrentHost ? directory : undefined;
  const entries = activeDir?.entries ?? [];
  const currentPath = activeDir?.path ?? pathInput;
  const parentPath = activeDir ? parentRemotePath(activeDir.path) : '.';
  const hasParent = Boolean(activeDir && parentPath !== activeDir.path);
  const hostBookmarks = useMemo(() => (hostId ? (bookmarks[hostId] ?? []) : []), [bookmarks, hostId]);

  function clearSelection() {
    setSelectedPaths([]);
    setActivePath(undefined);
    setAnchorPath(undefined);
  }

  function selectSingle(entry: FileEntry) {
    setSelectedPaths([entry.path]);
    setActivePath(entry.path);
    setAnchorPath(entry.path);
  }

  function focusPanel() {
    panelRef.current?.focus({ preventScroll: true });
  }

  function loadDirectory(targetHostId: string, targetPath: string) {
    if (!targetPath.trim()) return;
    void action.run(async current => {
      const result = await api.call('files.list', { hostId: targetHostId, path: targetPath });
      if (!current()) return;
      hostPaths.current.set(targetHostId, result.path);
      setLoadedHostId(targetHostId);
      setDirectory(result);
      setPathInput(result.path);
      clearSelection();
      setInlineMode(undefined);
    });
  }

  useEffect(() => {
    const onFocus = () => { windowRefocusedAt.current = Date.now(); };
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, []);

  useEffect(() => {
    clearSelection();
    setInlineMode(undefined);
    setFilter('');
    setMenu(undefined);
    setOpenSubmenu(undefined);
    setBookmarksOpen(false);
    if (!hostId) {
      setDirectory(undefined);
      setLoadedHostId(undefined);
      setPathInput('.');
      action.setNotice(undefined);
      return;
    }
    const remembered = hostPaths.current.get(hostId) ?? '.';
    setPathInput(remembered);
    loadDirectory(hostId, remembered);
  }, [api, hostId]);

  useEffect(() => {
    if (!menu) return;
    function onPointerDown(event: PointerEvent) {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenu(undefined);
        setOpenSubmenu(undefined);
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setMenu(undefined);
        setOpenSubmenu(undefined);
      }
    }
    window.addEventListener('pointerdown', onPointerDown);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('pointerdown', onPointerDown);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [menu]);

  const stats = useMemo(() => {
    let fileCount = 0;
    let dirCount = 0;
    let totalBytes = 0;
    for (const entry of entries) {
      if (entry.directory) dirCount++;
      else {
        fileCount++;
        totalBytes += entry.size || 0;
      }
    }
    return { fileCount, dirCount, totalBytes };
  }, [entries]);

  const visibleEntries = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    const list = needle ? entries.filter(item => item.name.toLowerCase().includes(needle)) : [...entries];
    list.sort((a, b) => {
      if (a.directory !== b.directory) return a.directory ? -1 : 1;
      let cmp = 0;
      if (sortKey === 'name') cmp = a.name.localeCompare(b.name, 'zh-CN');
      else if (sortKey === 'size') cmp = (a.size || 0) - (b.size || 0);
      else cmp = (a.modifiedAt ?? '').localeCompare(b.modifiedAt ?? '');
      return sortDir === 'asc' ? cmp : -cmp;
    });
    return list;
  }, [entries, filter, sortKey, sortDir]);

  const selectedSet = useMemo(() => new Set(selectedPaths), [selectedPaths]);
  const selectedEntries = useMemo(
    () => visibleEntries.filter(item => selectedSet.has(item.path)),
    [visibleEntries, selectedSet],
  );
  const selected = useMemo(
    () => selectedEntries.find(item => item.path === activePath) ?? selectedEntries[0],
    [selectedEntries, activePath],
  );

  function selectAllVisible() {
    if (visibleEntries.length === 0) return;
    setSelectedPaths(visibleEntries.map(item => item.path));
    if (!activePath || !visibleEntries.some(item => item.path === activePath)) {
      setActivePath(visibleEntries[0].path);
    }
    setAnchorPath(visibleEntries[0].path);
  }

  function scrollRowIntoView(path: string) {
    const row = rowRefs.current.get(path);
    if (row && typeof row.scrollIntoView === 'function') {
      row.scrollIntoView({ block: 'nearest' });
    }
  }

  function moveSelection(targetIndex: number, extend: boolean) {
    if (visibleEntries.length === 0) return;
    const clamped = Math.max(0, Math.min(visibleEntries.length - 1, targetIndex));
    const target = visibleEntries[clamped];
    if (!target) return;
    if (extend) {
      const anchorIdx = anchorPath ? visibleEntries.findIndex(item => item.path === anchorPath) : -1;
      const start = anchorIdx >= 0 ? anchorIdx : clamped;
      const [lo, hi] = start <= clamped ? [start, clamped] : [clamped, start];
      setSelectedPaths(visibleEntries.slice(lo, hi + 1).map(item => item.path));
      setActivePath(target.path);
      if (anchorIdx < 0) setAnchorPath(target.path);
    } else {
      selectSingle(target);
    }
    scrollRowIntoView(target.path);
  }

  function handleRowClick(event: ReactMouseEvent<HTMLTableRowElement>, entry: FileEntry) {
    focusPanel();
    if (event.shiftKey) {
      const targetIdx = visibleEntries.findIndex(item => item.path === entry.path);
      const anchorKey = anchorPath ?? activePath;
      const anchorIdx = anchorKey ? visibleEntries.findIndex(item => item.path === anchorKey) : -1;
      if (targetIdx >= 0 && anchorIdx >= 0) {
        const [lo, hi] = anchorIdx <= targetIdx ? [anchorIdx, targetIdx] : [targetIdx, anchorIdx];
        const range = visibleEntries.slice(lo, hi + 1).map(item => item.path);
        if (event.ctrlKey || event.metaKey) {
          setSelectedPaths(prev => Array.from(new Set([...prev, ...range])));
        } else {
          setSelectedPaths(range);
        }
        setActivePath(entry.path);
        return;
      }
      selectSingle(entry);
      return;
    }
    if (event.ctrlKey || event.metaKey) {
      setSelectedPaths(prev => {
        if (prev.includes(entry.path)) return prev.filter(item => item !== entry.path);
        return [...prev, entry.path];
      });
      setActivePath(entry.path);
      setAnchorPath(entry.path);
      return;
    }
    selectSingle(entry);
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) setSortDir(prev => (prev === 'asc' ? 'desc' : 'asc'));
    else {
      setSortKey(key);
      setSortDir('asc');
    }
  }

  function closeMenu() {
    setMenu(undefined);
    setOpenSubmenu(undefined);
  }

  function openContextMenu(event: ReactMouseEvent, entry?: FileEntry) {
    if (!hostId || !activeDir) return;
    event.preventDefault();
    event.stopPropagation();
    focusPanel();
    if (entry && !selectedSet.has(entry.path)) selectSingle(entry);
    const menuWidth = 236;
    const menuHeight = 430;
    const x = Math.max(8, Math.min(event.clientX, window.innerWidth - menuWidth - 8));
    const y = Math.max(8, Math.min(event.clientY, window.innerHeight - menuHeight - 8));
    setOpenSubmenu(undefined);
    setMenu({ x, y, entry });
  }

  async function refreshAfterMutation(targetHostId: string, dirPath: string, current: () => boolean, message: string) {
    if (!current()) return;
    action.setNotice({ tone: 'success', text: message });
    try {
      const result = await api.call('files.list', { hostId: targetHostId, path: dirPath });
      if (!current()) return;
      hostPaths.current.set(targetHostId, result.path);
      setLoadedHostId(targetHostId);
      setDirectory(result);
      setPathInput(result.path);
      clearSelection();
    } catch (error) {
      if (current()) action.setNotice({ tone: 'info', text: `${message} 目录未能刷新：${faultText(error)}` });
    }
  }

  function submitMkdir() {
    if (!hostId || !activeDir || !validLeaf(inlineValue)) return;
    const dir = activeDir.path;
    const target = joinRemotePath(dir, inlineValue.trim());
    void action.run(async current => {
      requireOk(await api.call('files.mkdir', { hostId, path: target }), '核心未确认目录已创建。');
      if (current()) { setInlineMode(undefined); setInlineValue(''); }
      await refreshAfterMutation(hostId, dir, current, `已创建目录 ${target}。`);
    });
  }

  function submitRename() {
    if (!host || !hostId || !activeDir || !selected || !validLeaf(inlineValue) || inlineValue.trim() === selected.name) return;
    const dir = activeDir.path;
    const from = selected.path;
    const to = joinRemotePath(dir, inlineValue.trim());
    confirm({
      title: '重命名远程条目？',
      description: '将修改远端目录中的名称，请核对完整路径。',
      acceptLabel: '确认重命名',
      danger: true,
      info: [['主机', remoteTarget(host)], ['原路径', <code>{from}</code>], ['新路径', <code>{to}</code>]],
      perform: () => {
        void action.run(async current => {
          requireOk(await api.call('files.rename', { hostId, path: from, to, confirmed: true }), '核心未确认重命名结果。');
          if (current()) { setInlineMode(undefined); setInlineValue(''); }
          await refreshAfterMutation(hostId, dir, current, `已重命名为 ${to}。`);
        });
      },
    });
  }

  function handleDelete(targetsInput: FileEntry | FileEntry[]) {
    if (!host || !hostId || !activeDir) return;
    const targets = Array.isArray(targetsInput) ? targetsInput : [targetsInput];
    if (targets.length === 0) return;
    const dir = activeDir.path;
    if (targets.length === 1) {
      const entry = targets[0];
      const recursive = Boolean(entry.directory && !entry.link);
      confirm({
        title: recursive ? '删除远程目录？' : '删除远程条目？',
        description: recursive ? '将递归删除该目录及其中的全部子目录和文件，且不可撤销。' : '将删除此远端条目，且不可撤销。',
        acceptLabel: '确认删除',
        danger: true,
        info: [['主机', remoteTarget(host)], ['目标路径', <code>{entry.path}</code>], ['类型', entry.link ? '符号链接' : entry.directory ? '目录（递归）' : '文件']],
        perform: () => {
          void action.run(async current => {
            requireOk(await api.call('files.delete', { hostId, path: entry.path, recursive, confirmed: true }), '核心未确认删除结果。');
            await refreshAfterMutation(hostId, dir, current, `已删除 ${entry.path}。`);
          });
        },
      });
      return;
    }
    const dirCount = targets.filter(item => item.directory && !item.link).length;
    const preview = targets.slice(0, 4).map(item => item.name).join('、') + (targets.length > 4 ? ` 等共 ${targets.length} 项` : '');
    confirm({
      title: `删除 ${targets.length} 个远程条目？`,
      description: dirCount > 0
        ? `将永久删除所选 ${targets.length} 个条目（包含 ${dirCount} 个目录，将递归删除其中全部内容），且不可撤销。`
        : `将永久删除所选 ${targets.length} 个远端条目，且不可撤销。`,
      acceptLabel: `确认删除 (${targets.length})`,
      danger: true,
      info: [['主机', remoteTarget(host)], ['所在目录', <code>{dir}</code>], ['待删除项', preview]],
      perform: () => {
        void action.run(async current => {
          let deleted = 0;
          const failed: string[] = [];
          for (const entry of targets) {
            if (!current()) return;
            try {
              const recursive = Boolean(entry.directory && !entry.link);
              requireOk(await api.call('files.delete', { hostId, path: entry.path, recursive, confirmed: true }), '核心未确认删除结果。');
              deleted++;
            } catch (error) {
              failed.push(`${entry.name}（${faultText(error)}）`);
            }
          }
          const summary = failed.length > 0
            ? `已删除 ${deleted} 个条目，${failed.length} 个失败：${failed.slice(0, 2).join('；')}`
            : `已批量删除 ${deleted} 个远程条目。`;
          await refreshAfterMutation(hostId, dir, current, summary);
        });
      },
    });
  }

  function handleDownload(entry: FileEntry) {
    if (!hostId || entry.directory || entry.link) return;
    void action.run(async current => {
      const picked = await api.call('dialog.pick', { purpose: 'download' });
      if (!picked || !current()) return;
      requireOk(await api.call('files.download', { hostId, path: entry.path, token: picked.token }), '核心未确认下载完成。');
      if (current()) action.setNotice({ tone: 'success', text: `已下载 ${entry.name} 到 ${picked.name}。` });
    });
  }

  function handleUpload(isDirectory: boolean) {
    if (!host || !hostId || !activeDir) return;
    const dir = activeDir.path;
    void action.run(async current => {
      const picked = await api.call('dialog.pick', { purpose: isDirectory ? 'uploadDirectory' : 'upload' });
      if (!picked || !current()) return;
      if (!validLeaf(picked.name)) throw new Error('选中来源的名称不能作为远端条目名称。');
      const targetPath = joinRemotePath(dir, picked.name);
      confirm({
        title: isDirectory ? '上传文件夹到远端？' : '上传文件到远端？',
        description: '将把所选本地内容上传到当前远端目录。',
        acceptLabel: '确认上传',
        info: [['主机', remoteTarget(host)], ['本地来源', picked.name], ['目标路径', <code>{targetPath}</code>]],
        perform: () => {
          void action.run(async innerCurrent => {
            requireOk(await api.call('files.upload', { hostId, path: targetPath, token: picked.token, overwrite: true, confirmed: true }), '核心未确认上传完成。');
            await refreshAfterMutation(hostId, dir, innerCurrent, `已上传 ${picked.name} 到 ${dir}。`);
          });
        },
      });
    });
  }

  function handleEdit(entry: FileEntry) {
    if (!hostId || entry.directory || entry.link) return;
    void action.run(async current => {
      const result = await api.call('files.read', { hostId, path: entry.path });
      if (!current()) return;
      setEditor({
        hostId,
        path: entry.path,
        name: entry.name,
        content: result.content,
        original: result.content,
        digest: result.digest,
      });
    });
  }

  function submitEditorSave() {
    if (!editor) return;
    const snapshot = editor;
    void action.run(async current => {
      const result = await api.call('files.write', {
        hostId: snapshot.hostId,
        path: snapshot.path,
        content: snapshot.content,
        expectedDigest: snapshot.digest,
        confirmed: true,
      });
      if (!current()) return;
      setEditor(prev => (prev && prev.path === snapshot.path ? { ...prev, original: snapshot.content, digest: result.digest } : prev));
      action.setNotice({ tone: 'success', text: `已保存 ${snapshot.path}。` });
    });
  }

  function handleOpenEntry(entry: FileEntry) {
    if (!hostId || entry.link) return;
    if (entry.directory) loadDirectory(hostId, entry.path);
    else handleEdit(entry);
  }

  function setClipboardFromEntries(targetsInput: FileEntry | FileEntry[], mode: 'copy' | 'move') {
    if (!host || !hostId) return;
    const targets = (Array.isArray(targetsInput) ? targetsInput : [targetsInput]).filter(item => !item.link);
    if (targets.length === 0) {
      action.setNotice({ tone: 'info', text: '符号链接暂不支持复制或剪切。' });
      return;
    }
    const label = host.name || host.address;
    const items: FileClipboardItem[] = targets.map(item => ({
      path: item.path,
      name: item.name,
      directory: item.directory,
    }));
    setClipboard({
      hostId,
      hostName: label,
      items,
      mode,
      copiedAt: Date.now(),
    });
    const verb = mode === 'copy' ? '复制' : '剪切';
    action.setNotice({
      tone: 'info',
      text: items.length === 1
        ? `已${verb}「${items[0].name}」（${label}），可按 Ctrl+V 粘贴到本机或其他服务器目录。`
        : `已${verb} ${items.length} 个条目（${label}），可按 Ctrl+V 粘贴到当前或其他服务器目录。`,
    });
  }

  function confirmLocalPaste(localItems: LocalClipboardFile[], overwrite = false) {
    if (!host || !hostId || !activeDir) return;
    const validItems = localItems.filter(item => validLeaf(item.name));
    if (validItems.length === 0) {
      action.setNotice({ tone: 'info', text: '本机剪贴板中的文件名无法作为远端条目名称。' });
      return;
    }
    const dir = activeDir.path;
    const existingNames = new Set(activeDir.entries.map(item => item.name));
    const conflicts = validItems.filter(item => existingNames.has(item.name));
    const hasConflict = conflicts.length > 0;
    const preview = validItems
      .slice(0, 4)
      .map(item => `${item.name}${item.directory ? '（文件夹）' : ''}`)
      .join('、') + (validItems.length > 4 ? ` 等共 ${validItems.length} 项` : '');
    const title = validItems.length === 1
      ? (validItems[0].directory ? '粘贴本地文件夹到远端？' : '粘贴本地文件到远端？')
      : `粘贴 ${validItems.length} 个本地条目到远端？`;
    const description = hasConflict
      ? `目标目录中已存在 ${conflicts.length} 个同名条目（${conflicts.slice(0, 3).map(item => item.name).join('、')}），确认后将覆盖同名文件或合并目录。`
      : '将把从本机电脑复制的文件或文件夹上传到当前远端目录。';
    confirm({
      title,
      description,
      acceptLabel: hasConflict || overwrite ? '确认粘贴并覆盖' : '确认粘贴上传',
      danger: hasConflict || overwrite,
      info: [
        ['主机', remoteTarget(host)],
        ['目标目录', <code>{dir}</code>],
        ['本地来源', preview],
      ],
      perform: () => {
        void action.run(async current => {
          let uploaded = 0;
          const failed: string[] = [];
          for (const item of validItems) {
            if (!current()) return;
            const targetPath = joinRemotePath(dir, item.name);
            try {
              requireOk(
                await api.call('files.upload', {
                  hostId,
                  path: targetPath,
                  token: item.token,
                  overwrite: true,
                  confirmed: true,
                }),
                '核心未确认上传完成。',
              );
              uploaded++;
            } catch (error) {
              failed.push(`${item.name}（${faultText(error)}）`);
            }
          }
          const summary = failed.length > 0
            ? `已从本地粘贴上传 ${uploaded} 个条目，${failed.length} 个失败：${failed.slice(0, 2).join('；')}`
            : validItems.length === 1
              ? `已从本地粘贴上传 ${validItems[0].name} 到 ${dir}。`
              : `已从本地粘贴上传 ${uploaded} 个条目到 ${dir}。`;
          await refreshAfterMutation(hostId, dir, current, summary);
        });
      },
    });
  }

  function handleRemotePaste(overwrite = false) {
    if (!host || !hostId || !activeDir || !clipboard || clipboard.items.length === 0) return;
    const dir = activeDir.path;
    const items = clipboard.items;
    const crossHost = clipboard.hostId !== hostId;
    const verb = clipboard.mode === 'move' ? '移动' : '复制';
    const scopeLabel = crossHost ? `跨服务器${verb}` : verb;
    if (items.length === 1) {
      const single = items[0];
      const targetPath = joinRemotePath(dir, single.name);
      confirm({
        title: `${scopeLabel}到当前目录？`,
        description: crossHost
          ? `将通过双端 SFTP 把「${single.name}」从 ${clipboard.hostName} ${verb}到 ${host.name || host.address}。${overwrite ? '允许覆盖同名条目。' : '遇到同名条目时将停止以防止误覆盖。'}`
          : `将把「${single.name}」${verb}到当前目录。${overwrite ? '允许覆盖同名条目。' : '遇到同名条目时将停止。'}`,
        acceptLabel: overwrite ? `确认${scopeLabel}（覆盖）` : `确认${scopeLabel}`,
        danger: overwrite || clipboard.mode === 'move',
        info: [
          ['来源服务器', clipboard.hostName],
          ['来源路径', <code>{single.path}</code>],
          ['目标服务器', host.name || host.address],
          ['目标路径', <code>{targetPath}</code>],
          ['类型', single.directory ? '文件夹（递归）' : '文件'],
        ],
        perform: () => {
          const snapshot = clipboard;
          void action.run(async current => {
            const res = await api.call('files.transfer', {
              sourceHostId: snapshot.hostId,
              sourcePath: single.path,
              targetHostId: hostId,
              targetPath,
              mode: snapshot.mode,
              overwrite,
              confirmed: true,
            });
            requireOk(res, `核心未确认${scopeLabel}完成。`);
            if (current() && snapshot.mode === 'move') setClipboard(undefined);
            await refreshAfterMutation(
              hostId,
              dir,
              current,
              `已完成${scopeLabel}：${single.name}（${res.files} 个文件${res.dirs ? `，${res.dirs} 个目录` : ''}，${formatBytes(res.bytes)}）。`,
            );
          });
        },
      });
      return;
    }
    const preview = items.slice(0, 4).map(item => item.name).join('、') + (items.length > 4 ? ` 等共 ${items.length} 项` : '');
    confirm({
      title: `${scopeLabel} ${items.length} 个条目到当前目录？`,
      description: crossHost
        ? `将通过双端 SFTP 把 ${items.length} 个条目从 ${clipboard.hostName} ${verb}到 ${host.name || host.address}。${overwrite ? '允许覆盖同名条目。' : '遇到同名条目时将停止。'}`
        : `将把 ${items.length} 个条目${verb}到当前目录。${overwrite ? '允许覆盖同名条目。' : '遇到同名条目时将停止。'}`,
      acceptLabel: overwrite ? `确认${scopeLabel}（覆盖）` : `确认${scopeLabel} (${items.length})`,
      danger: overwrite || clipboard.mode === 'move',
      info: [
        ['来源服务器', clipboard.hostName],
        ['目标服务器', host.name || host.address],
        ['目标目录', <code>{dir}</code>],
        ['条目列表', preview],
      ],
      perform: () => {
        const snapshot = clipboard;
        void action.run(async current => {
          let totalFiles = 0;
          let totalDirs = 0;
          let totalBytes = 0;
          let doneCount = 0;
          const failed: string[] = [];
          for (const item of snapshot.items) {
            if (!current()) return;
            try {
              const targetPath = joinRemotePath(dir, item.name);
              const res = await api.call('files.transfer', {
                sourceHostId: snapshot.hostId,
                sourcePath: item.path,
                targetHostId: hostId,
                targetPath,
                mode: snapshot.mode,
                overwrite,
                confirmed: true,
              });
              requireOk(res, `核心未确认${scopeLabel}完成。`);
              doneCount++;
              totalFiles += res.files;
              totalDirs += res.dirs;
              totalBytes += res.bytes;
            } catch (error) {
              failed.push(`${item.name}（${faultText(error)}）`);
            }
          }
          if (current() && snapshot.mode === 'move' && failed.length === 0) setClipboard(undefined);
          const summary = failed.length > 0
            ? `已完成${scopeLabel} ${doneCount} 项，${failed.length} 项失败：${failed.slice(0, 2).join('；')}`
            : `已完成${scopeLabel} ${doneCount} 个条目（${totalFiles} 个文件${totalDirs ? `，${totalDirs} 个目录` : ''}，${formatBytes(totalBytes)}）。`;
          await refreshAfterMutation(hostId, dir, current, summary);
        });
      },
    });
  }

  async function handleSmartPaste(overwrite = false, forceRemote = false) {
    if (!host || !hostId || !activeDir || action.busy) return;
    const hasRemote = Boolean(clipboard && clipboard.items.length > 0);
    const preferLocal = !forceRemote && (!hasRemote || windowRefocusedAt.current > (clipboard?.copiedAt ?? 0));
    if (preferLocal) {
      let localItems: LocalClipboardFile[] = [];
      try {
        localItems = await api.call('clipboard.readFiles', {});
      } catch {
        localItems = [];
      }
      if (localItems.length > 0) {
        confirmLocalPaste(localItems, overwrite);
        return;
      }
    }
    if (hasRemote) {
      handleRemotePaste(overwrite);
      return;
    }
    action.setNotice({ tone: 'info', text: '剪贴板中没有可粘贴的文件或文件夹。' });
  }

  function openScpDialog(entry: FileEntry, targetHostId?: string) {
    const destId = targetHostId ?? otherHosts[0]?.id ?? hostId ?? '';
    if (!destId) return;
    const remembered = hostPaths.current.get(destId) ?? '.';
    setScpDialog({
      sourceEntry: entry,
      targetHostId: destId,
      targetDir: remembered,
      mode: 'copy',
      overwrite: false,
    });
  }

  function submitScpTransfer() {
    if (!host || !hostId || !activeDir || !scpDialog) return;
    const targetHost = connectableHosts.find(item => item.id === scpDialog.targetHostId);
    if (!targetHost) return;
    const dir = activeDir.path;
    const targetDir = scpDialog.targetDir.trim() || '.';
    const targetPath = joinRemotePath(targetDir, scpDialog.sourceEntry.name);
    const currentDialog = scpDialog;
    setScpDialog(undefined);
    void action.run(async current => {
      const res = await api.call('files.transfer', {
        sourceHostId: hostId,
        sourcePath: currentDialog.sourceEntry.path,
        targetHostId: currentDialog.targetHostId,
        targetPath,
        mode: currentDialog.mode,
        overwrite: currentDialog.overwrite,
        confirmed: true,
      });
      requireOk(res, '核心未确认跨服务器传输完成。');
      const verb = currentDialog.mode === 'move' ? '移动' : '复制';
      const summary = `已将 ${currentDialog.sourceEntry.name} ${verb}到 ${targetHost.name || targetHost.address}:${targetPath}（${res.files} 个文件，${formatBytes(res.bytes)}）。`;
      if (currentDialog.mode === 'move' || currentDialog.targetHostId === hostId) {
        await refreshAfterMutation(hostId, dir, current, summary);
      } else if (current()) {
        action.setNotice({ tone: 'success', text: summary });
      }
    });
  }

  function openArchiveDialog(entry: FileEntry, kind: 'archive' | 'extract') {
    if (!activeDir) return;
    const destination = kind === 'archive'
      ? joinRemotePath(activeDir.path, `${entry.name}.tar.gz`)
      : defaultExtractDir(activeDir.path, entry.name);
    setArchiveDialog({ kind, entry, destination, overwrite: false });
  }

  function submitArchiveAction() {
    if (!hostId || !activeDir || !archiveDialog) return;
    const dir = activeDir.path;
    const currentDialog = archiveDialog;
    const dest = currentDialog.destination.trim();
    if (!dest) return;
    setArchiveDialog(undefined);
    void action.run(async current => {
      if (currentDialog.kind === 'archive') {
        const res = await api.call('files.archive', {
          hostId,
          path: currentDialog.entry.path,
          destination: dest,
          overwrite: currentDialog.overwrite,
          confirmed: true,
        });
        requireOk(res, '核心未确认压缩完成。');
        await refreshAfterMutation(hostId, dir, current, `已压缩为 ${res.path}（${res.files} 个文件，${formatBytes(res.bytes)}）。`);
      } else {
        const res = await api.call('files.extract', {
          hostId,
          path: currentDialog.entry.path,
          destination: dest,
          overwrite: currentDialog.overwrite,
          confirmed: true,
        });
        requireOk(res, '核心未确认解压完成。');
        await refreshAfterMutation(hostId, dir, current, `已解压到 ${res.path}（${res.files} 个文件，${res.dirs} 个目录）。`);
      }
    });
  }

  function openChmodDialog(entry: FileEntry) {
    const rawMode = entry.mode ?? (entry.directory ? 0o755 : 0o644);
    setChmodDialog({
      entry,
      bits: rawMode & 0o777,
      specialBits: rawMode & 0o7000,
      sudo: false,
      fast: true,
      recursive: false,
    });
  }

  function submitChmodAction() {
    if (!hostId || !activeDir || !chmodDialog) return;
    const dir = activeDir.path;
    const currentDialog = chmodDialog;
    const mode = formatCommandMode(currentDialog.bits, currentDialog.specialBits);
    const recursive = currentDialog.entry.directory && currentDialog.recursive;
    setChmodDialog(undefined);
    void action.run(async current => {
      const res = await api.call('files.chmod', {
        hostId,
        path: currentDialog.entry.path,
        mode,
        recursive,
        sudo: currentDialog.sudo,
        fast: currentDialog.fast,
        confirmed: true,
      });
      requireOk(res, '核心未确认权限修改完成。');
      const scope = recursive ? '（含子目录及文件）' : '';
      await refreshAfterMutation(hostId, dir, current, `已修改 ${currentDialog.entry.name} 权限为 ${mode}${scope}。`);
    });
  }

  function toggleBookmark(pathToBookmark: string) {
    if (!hostId || !pathToBookmark) return;
    setBookmarks(prev => {
      const existing = prev[hostId] ?? [];
      const has = existing.includes(pathToBookmark);
      const nextList = has ? existing.filter(item => item !== pathToBookmark) : [pathToBookmark, ...existing].slice(0, 20);
      const next = { ...prev, [hostId]: nextList };
      saveBookmarks(next);
      action.setNotice({ tone: 'info', text: has ? `已取消收藏路径 ${pathToBookmark}` : `已收藏路径 ${pathToBookmark}` });
      return next;
    });
  }

  async function copyText(text: string, label: string) {
    try {
      await navigator.clipboard.writeText(text);
      action.setNotice({ tone: 'info', text: `已复制${label}：${text}` });
    } catch {
      action.setNotice({ tone: 'info', text: `${label}：${text}` });
    }
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (!hostId || !activeDir || action.busy) return;
    if (editor || scpDialog || archiveDialog || chmodDialog) return;
    const target = event.target as HTMLElement | null;
    const isEditable = Boolean(target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable));
    if (isEditable) {
      if (event.key === 'Escape') {
        event.preventDefault();
        if (inlineMode) {
          setInlineMode(undefined);
          setInlineValue('');
          focusPanel();
        } else if (filterOpen) {
          setFilterOpen(false);
          setFilter('');
          focusPanel();
        }
      }
      return;
    }

    const mod = event.ctrlKey || event.metaKey;
    const lower = event.key.toLowerCase();

    if (event.key === 'Escape') {
      event.preventDefault();
      if (menu) closeMenu();
      else if (inlineMode) { setInlineMode(undefined); setInlineValue(''); }
      else if (filterOpen) { setFilterOpen(false); setFilter(''); }
      else if (selectedPaths.length > 0) clearSelection();
      return;
    }

    if (event.key === 'F5' || (mod && !event.shiftKey && !event.altKey && lower === 'r')) {
      event.preventDefault();
      loadDirectory(hostId, currentPath);
      return;
    }

    if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Home' || event.key === 'End') {
      if (visibleEntries.length === 0) return;
      event.preventDefault();
      const currentIdx = activePath ? visibleEntries.findIndex(item => item.path === activePath) : -1;
      if (event.key === 'ArrowDown') {
        moveSelection(currentIdx < 0 ? 0 : currentIdx + 1, event.shiftKey);
      } else if (event.key === 'ArrowUp') {
        moveSelection(currentIdx < 0 ? visibleEntries.length - 1 : currentIdx - 1, event.shiftKey);
      } else if (event.key === 'Home') {
        moveSelection(0, event.shiftKey);
      } else {
        moveSelection(visibleEntries.length - 1, event.shiftKey);
      }
      return;
    }

    if (event.key === 'Enter' && selected) {
      event.preventDefault();
      handleOpenEntry(selected);
      return;
    }

    if (event.key === 'F2' && selected) {
      event.preventDefault();
      selectSingle(selected);
      setInlineMode('rename');
      setInlineValue(selected.name);
      return;
    }

    if ((event.key === 'Delete' || (event.metaKey && event.key === 'Backspace')) && selectedEntries.length > 0) {
      event.preventDefault();
      handleDelete(selectedEntries);
      return;
    }

    if (event.key === 'Backspace' && !mod && !event.shiftKey && !event.altKey && hasParent) {
      event.preventDefault();
      loadDirectory(hostId, parentPath);
      return;
    }

    if (event.ctrlKey && event.altKey && lower === 'e' && selected && !selected.directory && !selected.link) {
      event.preventDefault();
      handleEdit(selected);
      return;
    }

    if (mod && !event.shiftKey && !event.altKey) {
      if (lower === 'a') {
        event.preventDefault();
        selectAllVisible();
      } else if (lower === 'c' && selectedEntries.length > 0) {
        event.preventDefault();
        setClipboardFromEntries(selectedEntries, 'copy');
      } else if (lower === 'x' && selectedEntries.length > 0) {
        event.preventDefault();
        setClipboardFromEntries(selectedEntries, 'move');
      } else if (lower === 'v') {
        event.preventDefault();
        void handleSmartPaste(false);
      } else if (lower === 'f') {
        event.preventDefault();
        setFilterOpen(true);
        window.setTimeout(() => filterInputRef.current?.focus(), 0);
      }
    }
  }

  function renderSortIcon(key: SortKey) {
    if (sortKey !== key) return <ArrowUpDown size={12} aria-hidden="true" className="rd-tf-sort-idle" />;
    return sortDir === 'asc' ? <ArrowUp size={12} aria-hidden="true" /> : <ArrowDown size={12} aria-hidden="true" />;
  }

  const menuEntry = menu?.entry;
  const menuTargets = useMemo(() => {
    if (!menuEntry) return [] as FileEntry[];
    if (selectedSet.has(menuEntry.path) && selectedEntries.length > 1) return selectedEntries;
    return [menuEntry];
  }, [menuEntry, selectedSet, selectedEntries]);
  const menuEntryEditable = Boolean(menuEntry && !menuEntry.directory && !menuEntry.link);
  const menuEntryValid = Boolean(menuEntry && !menuEntry.link);
  const menuTargetsCopyable = menuTargets.some(item => !item.link);
  const menuEntryExtractable = Boolean(menuEntryEditable && menuEntry && isArchiveFile(menuEntry.name));
  const bookmarkTarget = menuEntry && menuEntry.directory && !menuEntry.link ? menuEntry.path : currentPath;
  const isBookmarked = hostBookmarks.includes(bookmarkTarget);
  const submenuAlignLeft = Boolean(menu && menu.x + 420 > window.innerWidth);
  const clipboardSummary = clipboard && clipboard.items.length > 0
    ? (clipboard.items.length === 1 ? clipboard.items[0].name : `${clipboard.items[0].name} 等 ${clipboard.items.length} 项`)
    : '';

  return <aside ref={panelRef} className="rd-tf-panel" aria-label="服务器文件列表" tabIndex={-1} onKeyDown={handleKeyDown}>
    <header className="rd-tf-toolbar">
      <button type="button" className="rd-tf-icon-btn" title="返回上级目录" aria-label="返回上级目录"
        disabled={!hostId || !hasParent || action.busy} onClick={() => hostId && loadDirectory(hostId, parentPath)}>
        <ChevronLeft size={16} aria-hidden="true" />
      </button>
      <button type="button" className="rd-tf-icon-btn" title="刷新目录" aria-label="刷新目录"
        disabled={!hostId || action.busy} onClick={() => hostId && loadDirectory(hostId, currentPath)}>
        <RefreshCw size={15} aria-hidden="true" className={action.busy ? 'rd-tf-spin' : undefined} />
      </button>
      <form className="rd-tf-path-form" onSubmit={event => { event.preventDefault(); if (hostId) loadDirectory(hostId, pathInput); }}>
        <input aria-label="当前远端路径" spellCheck={false} value={pathInput} placeholder="输入远端路径并回车"
          disabled={!hostId || action.busy} onChange={event => setPathInput(event.currentTarget.value)} />
      </form>
      <button type="button" className={`rd-tf-icon-btn${filterOpen ? ' is-active' : ''}`} title="筛选当前目录" aria-label="筛选当前目录"
        disabled={!hostId} onClick={() => { setFilterOpen(prev => !prev); if (filterOpen) setFilter(''); }}>
        <Search size={15} aria-hidden="true" />
      </button>
      <button type="button" className={`rd-tf-icon-btn${bookmarksOpen ? ' is-active' : ''}`} title="收藏的路径" aria-label="收藏的路径"
        disabled={!hostId} onClick={() => setBookmarksOpen(prev => !prev)}>
        <Bookmark size={15} aria-hidden="true" />
      </button>
      <button type="button" className="rd-tf-icon-btn" title="新建文件夹" aria-label="新建文件夹"
        disabled={!hostId || !activeDir || action.busy}
        onClick={() => { setInlineMode(prev => prev === 'mkdir' ? undefined : 'mkdir'); setInlineValue(''); }}>
        <FolderPlus size={15} aria-hidden="true" />
      </button>
      <button type="button" className="rd-tf-icon-btn" title="上传文件" aria-label="上传文件"
        disabled={!hostId || !activeDir || action.busy} onClick={() => handleUpload(false)}>
        <Upload size={15} aria-hidden="true" />
      </button>
    </header>

    {bookmarksOpen && hostId && <div className="rd-tf-bookmarks">
      <div className="rd-tf-bookmarks-head">
        <span>已收藏路径 ({hostBookmarks.length})</span>
        <button type="button" className="rd-tf-mini-btn" onClick={() => toggleBookmark(currentPath)}>
          {hostBookmarks.includes(currentPath) ? '取消收藏当前' : '收藏当前路径'}
        </button>
      </div>
      {hostBookmarks.length === 0
        ? <div className="rd-tf-bookmarks-empty">暂无收藏路径，可点击上方按钮或右键菜单收藏常用目录。</div>
        : <ul className="rd-tf-bookmarks-list">
            {hostBookmarks.map(item => <li key={item}>
              <button type="button" className="rd-tf-bookmark-jump" title={item}
                onClick={() => { loadDirectory(hostId, item); setBookmarksOpen(false); }}>
                <Folder size={13} aria-hidden="true" /><span>{item}</span>
              </button>
              <button type="button" className="rd-tf-icon-btn" aria-label={`移除收藏 ${item}`} title="移除收藏"
                onClick={() => toggleBookmark(item)}>
                <X size={12} aria-hidden="true" />
              </button>
            </li>)}
          </ul>}
    </div>}

    {clipboard && clipboard.items.length > 0 && hostId && <div className="rd-tf-clipboard-bar" role="status">
      <span className="rd-tf-clipboard-text" title={clipboard.items.map(item => `${clipboard.hostName}:${item.path}`).join('\n')}>
        {clipboard.mode === 'move' ? '剪切' : '复制'}：<strong>{clipboardSummary}</strong>
        {clipboard.hostId !== hostId && <em>（来自 {clipboard.hostName}）</em>}
      </span>
      <div className="rd-tf-clipboard-actions">
        <button type="button" className="rd-tf-mini-btn is-primary" disabled={!activeDir || action.busy}
          onClick={() => handleRemotePaste(false)}>
          粘贴到此处
        </button>
        <button type="button" className="rd-tf-icon-btn" title="清空剪贴板" aria-label="清空剪贴板"
          onClick={() => setClipboard(undefined)}>
          <X size={13} aria-hidden="true" />
        </button>
      </div>
    </div>}

    {filterOpen && hostId && <div className="rd-tf-inline-bar">
      <Search size={14} aria-hidden="true" />
      <input ref={filterInputRef} aria-label="按名称筛选文件" placeholder="筛选当前目录文件名…" value={filter} onChange={e => setFilter(e.currentTarget.value)} />
      {filter && <button type="button" className="rd-tf-icon-btn" aria-label="清空筛选" onClick={() => setFilter('')}><X size={13} aria-hidden="true" /></button>}
    </div>}

    {inlineMode && hostId && <form className="rd-tf-inline-bar" onSubmit={event => { event.preventDefault(); if (inlineMode === 'mkdir') submitMkdir(); else submitRename(); }}>
      <input aria-label={inlineMode === 'mkdir' ? '新目录名称' : '重命名新名称'} spellCheck={false} autoFocus
        placeholder={inlineMode === 'mkdir' ? '输入新文件夹名称…' : '输入新名称…'}
        value={inlineValue} disabled={action.busy} onChange={e => setInlineValue(e.currentTarget.value)} />
      <Button type="submit" variant="primary" disabled={action.busy || !validLeaf(inlineValue)}>确定</Button>
      <Button disabled={action.busy} onClick={() => { setInlineMode(undefined); setInlineValue(''); }}>取消</Button>
    </form>}

    {hostId && <div className="rd-tf-summary" role="status">
      <span>
        共 {stats.fileCount} 个文件，{stats.dirCount} 个文件夹，{formatBytes(stats.totalBytes)}
        {selectedEntries.length > 0 && <strong className="rd-tf-selected-badge">已选 {selectedEntries.length} 项</strong>}
      </span>
      {host && <span className="rd-tf-host-tag" title={remoteTarget(host)}>{host.name || host.address}</span>}
    </div>}

    {action.notice && <div className="rd-tf-notice"><Notice value={action.notice} /></div>}

    <div className="rd-tf-list-wrap"
      onClick={event => {
        focusPanel();
        if (event.target === event.currentTarget) clearSelection();
      }}
      onContextMenu={event => openContextMenu(event, undefined)}>
      {!hostId ? <div className="rd-tf-empty">
        <Empty icon={<Folder size={22} />} title="未选择服务器" description="在左侧服务器列表选择已验证的主机后，此处将显示该服务器的文件列表。" />
      </div> : <table className="rd-tf-table" aria-label="远端目录条目">
        <thead>
          <tr>
            <th scope="col"><button type="button" className="rd-tf-sort" onClick={() => toggleSort('name')}><span>名称</span>{renderSortIcon('name')}</button></th>
            <th scope="col" className="rd-tf-col-date"><button type="button" className="rd-tf-sort" onClick={() => toggleSort('modifiedAt')}><span>修改时间</span>{renderSortIcon('modifiedAt')}</button></th>
            <th scope="col" className="rd-tf-col-size"><button type="button" className="rd-tf-sort" onClick={() => toggleSort('size')}><span>大小</span>{renderSortIcon('size')}</button></th>
            <th scope="col" className="rd-tf-col-ops"><span className="rd-w-sr">操作</span></th>
          </tr>
        </thead>
        <tbody>
          {hasParent && <tr className="rd-tf-row" onDoubleClick={() => loadDirectory(hostId, parentPath)} onClick={() => { focusPanel(); loadDirectory(hostId, parentPath); }}>
            <td className="rd-tf-name-cell"><span className="rd-tf-name"><Folder size={15} aria-hidden="true" className="rd-tf-folder-icon" /><span>..</span></span></td>
            <td className="rd-tf-col-date">—</td>
            <td className="rd-tf-col-size">—</td>
            <td className="rd-tf-col-ops" />
          </tr>}
          {visibleEntries.map(entry => {
            const isSelected = selectedSet.has(entry.path);
            const isActive = activePath === entry.path;
            return <tr key={entry.path}
              ref={el => {
                if (el) rowRefs.current.set(entry.path, el);
                else rowRefs.current.delete(entry.path);
              }}
              aria-selected={isSelected}
              className={`rd-tf-row${isSelected ? ' is-selected' : ''}${isActive ? ' is-active' : ''}`}
              onClick={event => handleRowClick(event, entry)}
              onContextMenu={event => openContextMenu(event, entry)}
              onDoubleClick={() => handleOpenEntry(entry)}>
              <td className="rd-tf-name-cell" title={entry.path}>
                <span className="rd-tf-name">
                  {entry.link ? <Link2 size={15} aria-hidden="true" className="rd-tf-link-icon" />
                    : entry.directory ? <Folder size={15} aria-hidden="true" className="rd-tf-folder-icon" />
                    : <File size={15} aria-hidden="true" className="rd-tf-file-icon" />}
                  <span>{entry.name}</span>
                </span>
              </td>
              <td className="rd-tf-col-date">{formatCompactDate(entry.modifiedAt)}</td>
              <td className="rd-tf-col-size">{entry.directory ? '—' : formatBytes(entry.size)}</td>
              <td className="rd-tf-col-ops">
                <div className="rd-tf-row-ops" onClick={e => e.stopPropagation()}>
                  {!entry.directory && !entry.link && <button type="button" className="rd-tf-op-btn" title={`下载 ${entry.name}`} aria-label={`下载 ${entry.name}`}
                    disabled={action.busy} onClick={() => handleDownload(entry)}>
                    <Download size={13} aria-hidden="true" />
                  </button>}
                  <button type="button" className="rd-tf-op-btn" title={`重命名 ${entry.name}`} aria-label={`重命名 ${entry.name}`}
                    disabled={action.busy} onClick={() => { selectSingle(entry); setInlineMode('rename'); setInlineValue(entry.name); }}>
                    <Pencil size={13} aria-hidden="true" />
                  </button>
                  <button type="button" className="rd-tf-op-btn is-danger" title={`删除 ${entry.name}`} aria-label={`删除 ${entry.name}`}
                    disabled={action.busy} onClick={() => handleDelete(entry)}>
                    <Trash2 size={13} aria-hidden="true" />
                  </button>
                </div>
              </td>
            </tr>;
          })}
          {!action.busy && visibleEntries.length === 0 && <tr>
            <td colSpan={4} className="rd-tf-empty-row">{filter ? '当前筛选无匹配文件' : '目录为空（右键可新建文件夹、上传或按 Ctrl+V 粘贴）'}</td>
          </tr>}
        </tbody>
      </table>}
    </div>

    {menu && hostId && activeDir && <div ref={menuRef} className="rd-tf-menu" role="menu" aria-label="文件操作菜单"
      style={{ left: menu.x, top: menu.y }}>
      <div className="rd-tf-menu-header">
        <span className="rd-tf-menu-title">操作</span>
        <div className="rd-tf-menu-quick">
          <button type="button" className="rd-tf-menu-qbtn" title="终端切到当前目录" aria-label="终端切到当前目录"
            disabled={!onTerminalCd || !activeDir || action.busy}
            onClick={() => {
              closeMenu();
              if (!onTerminalCd || !activeDir) return;
              const result = onTerminalCd(activeDir.path);
              action.setNotice({ tone: result.ok ? 'success' : 'info', text: result.message });
            }}>
            <LocateFixed size={14} aria-hidden="true" />
          </button>
          <button type="button" className="rd-tf-menu-qbtn"
            title={clipboard && clipboard.items.length > 0 ? `粘贴 ${clipboardSummary}（来自 ${clipboard.hostName}）或本机剪贴板文件` : '粘贴（支持 Ctrl+V 粘贴本机或远端文件）'}
            aria-label="粘贴"
            disabled={!activeDir || action.busy}
            onClick={() => { closeMenu(); void handleSmartPaste(false); }}>
            <ClipboardPaste size={14} aria-hidden="true" />
          </button>
          <button type="button" className="rd-tf-menu-qbtn" title="剪切（Ctrl+X，支持多选与跨服务器移动）" aria-label="剪切"
            disabled={!menuTargetsCopyable || action.busy}
            onClick={() => { if (menuTargets.length > 0) setClipboardFromEntries(menuTargets, 'move'); closeMenu(); }}>
            <Scissors size={14} aria-hidden="true" />
          </button>
          <button type="button" className="rd-tf-menu-qbtn" title="复制（Ctrl+C，支持多选与跨服务器传输）" aria-label="复制"
            disabled={!menuTargetsCopyable || action.busy}
            onClick={() => { if (menuTargets.length > 0) setClipboardFromEntries(menuTargets, 'copy'); closeMenu(); }}>
            <Copy size={14} aria-hidden="true" />
          </button>
        </div>
      </div>

      <div className="rd-tf-menu-group">
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryValid || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) handleOpenEntry(menuEntry); }}>
          <FolderOpen size={14} aria-hidden="true" /><span>打开</span><kbd>Enter</kbd>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryEditable || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) handleEdit(menuEntry); }}>
          <FileEdit size={14} aria-hidden="true" /><span>编辑</span><kbd>Ctrl+Alt+E</kbd>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={action.busy}
          onClick={() => { closeMenu(); loadDirectory(hostId, currentPath); }}>
          <RefreshCw size={14} aria-hidden="true" /><span>刷新</span><kbd>F5</kbd>
        </button>
      </div>

      <div className="rd-tf-menu-group">
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          onClick={() => { closeMenu(); toggleBookmark(bookmarkTarget); }}>
          <Bookmark size={14} aria-hidden="true" /><span>{isBookmarked ? '取消收藏路径' : '收藏路径'}</span>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryEditable || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) handleDownload(menuEntry); }}>
          <Download size={14} aria-hidden="true" /><span>下载到...</span>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={action.busy}
          onClick={() => { closeMenu(); handleUpload(false); }}>
          <Upload size={14} aria-hidden="true" /><span>上传文件...</span>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={action.busy}
          onClick={() => { closeMenu(); handleUpload(true); }}>
          <FolderUp size={14} aria-hidden="true" /><span>上传文件夹...</span>
        </button>

        <div className={`rd-tf-menu-subwrap${openSubmenu === 'scp' ? ' is-open' : ''}`}
          onMouseEnter={() => menuEntryValid && setOpenSubmenu('scp')}>
          <button type="button" role="menuitem" aria-haspopup="menu" aria-expanded={openSubmenu === 'scp'}
            className="rd-tf-menu-item" disabled={!menuEntryValid || action.busy}
            onClick={() => setOpenSubmenu(prev => (prev === 'scp' ? undefined : 'scp'))}>
            <Send size={14} aria-hidden="true" /><span>SCP</span><ChevronRight size={13} aria-hidden="true" className="rd-tf-menu-arrow" />
          </button>
          {openSubmenu === 'scp' && menuEntry && <div className={`rd-tf-submenu${submenuAlignLeft ? ' is-left' : ''}`} role="menu" aria-label="SCP 跨服务器传输">
            {otherHosts.length > 0 ? otherHosts.map(target => (
              <button key={target.id} type="button" role="menuitem" className="rd-tf-menu-item"
                onClick={() => { closeMenu(); openScpDialog(menuEntry, target.id); }}>
                <Send size={13} aria-hidden="true" />
                <span className="rd-tf-submenu-host">{target.name || target.address}</span>
              </button>
            )) : <div className="rd-tf-submenu-empty">暂无其他可用服务器</div>}
            <div className="rd-tf-menu-sep" />
            <button type="button" role="menuitem" className="rd-tf-menu-item"
              onClick={() => { closeMenu(); openScpDialog(menuEntry); }}>
              <Copy size={13} aria-hidden="true" /><span>指定目标路径传输...</span>
            </button>
          </div>}
        </div>

        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryValid || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) openArchiveDialog(menuEntry, 'archive'); }}>
          <Archive size={14} aria-hidden="true" /><span>压缩</span>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryExtractable || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) openArchiveDialog(menuEntry, 'extract'); }}>
          <Archive size={14} aria-hidden="true" /><span>解压</span>
        </button>
      </div>

      <div className="rd-tf-menu-group">
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntryValid || action.busy}
          onClick={() => { closeMenu(); if (menuEntry) openChmodDialog(menuEntry); }}>
          <Shield size={14} aria-hidden="true" /><span>修改权限</span>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item"
          disabled={!menuEntry || action.busy}
          onClick={() => {
            closeMenu();
            if (menuEntry) { selectSingle(menuEntry); setInlineMode('rename'); setInlineValue(menuEntry.name); }
          }}>
          <Pencil size={14} aria-hidden="true" /><span>重命名</span><kbd>F2</kbd>
        </button>
        <button type="button" role="menuitem" className="rd-tf-menu-item is-danger"
          disabled={menuTargets.length === 0 || action.busy}
          onClick={() => { closeMenu(); if (menuTargets.length > 0) handleDelete(menuTargets); }}>
          <Trash2 size={14} aria-hidden="true" /><span>{menuTargets.length > 1 ? `删除 (${menuTargets.length} 项)` : '删除'}</span><kbd>Delete</kbd>
        </button>
        <div className={`rd-tf-menu-subwrap${openSubmenu === 'more' ? ' is-open' : ''}`}
          onMouseEnter={() => setOpenSubmenu('more')}>
          <button type="button" role="menuitem" aria-haspopup="menu" aria-expanded={openSubmenu === 'more'}
            className="rd-tf-menu-item"
            onClick={() => setOpenSubmenu(prev => (prev === 'more' ? undefined : 'more'))}>
            <ChevronDown size={14} aria-hidden="true" /><span>更多</span><MoreHorizontal size={13} aria-hidden="true" className="rd-tf-menu-arrow" />
          </button>
          {openSubmenu === 'more' && <div className={`rd-tf-submenu${submenuAlignLeft ? ' is-left' : ''}`} role="menu" aria-label="更多文件操作">
            <button type="button" role="menuitem" className="rd-tf-menu-item"
              disabled={visibleEntries.length === 0}
              onClick={() => { closeMenu(); selectAllVisible(); }}>
              <Check size={13} aria-hidden="true" /><span>全选</span><kbd>Ctrl+A</kbd>
            </button>
            <button type="button" role="menuitem" className="rd-tf-menu-item"
              disabled={action.busy}
              onClick={() => { closeMenu(); setInlineMode('mkdir'); setInlineValue(''); }}>
              <FolderPlus size={13} aria-hidden="true" /><span>新建文件夹</span>
            </button>
            <button type="button" role="menuitem" className="rd-tf-menu-item"
              onClick={() => { closeMenu(); void copyText(menuEntry ? menuEntry.path : currentPath, '完整路径'); }}>
              <Copy size={13} aria-hidden="true" /><span>复制完整路径</span>
            </button>
            {menuEntry && <button type="button" role="menuitem" className="rd-tf-menu-item"
              onClick={() => { closeMenu(); void copyText(menuEntry.name, '名称'); }}>
              <Copy size={13} aria-hidden="true" /><span>复制名称</span>
            </button>}
            {clipboard && clipboard.items.length > 0 && <button type="button" role="menuitem" className="rd-tf-menu-item"
              disabled={action.busy}
              onClick={() => { closeMenu(); handleRemotePaste(true); }}>
              <ClipboardPaste size={13} aria-hidden="true" /><span>粘贴并覆盖同名项</span>
            </button>}
          </div>}
        </div>
      </div>
    </div>}

    {scpDialog && host && <div className="rd-w-dialog-backdrop" role="presentation" onClick={() => setScpDialog(undefined)}>
      <div className="rd-w-dialog rd-tf-modal" role="dialog" aria-modal="true" aria-label="SCP 跨服务器传输" onClick={e => e.stopPropagation()}>
        <h3>SCP 跨服务器传输</h3>
        <p className="rd-w-subtle">将把选中的{scpDialog.sourceEntry.directory ? '文件夹（递归）' : '文件'}从当前服务器传输到目标服务器。</p>
        <dl className="rd-w-kv">
          <div><dt>来源服务器</dt><dd>{host.name || host.address}</dd></div>
          <div><dt>来源路径</dt><dd><code>{scpDialog.sourceEntry.path}</code></dd></div>
        </dl>
        <div className="rd-tf-modal-fields">
          <label className="rd-tf-modal-field">
            <span>目标服务器</span>
            <select value={scpDialog.targetHostId} onChange={e => {
              const nextId = e.currentTarget.value;
              setScpDialog(prev => prev ? { ...prev, targetHostId: nextId, targetDir: hostPaths.current.get(nextId) ?? '.' } : prev);
            }}>
              {connectableHosts.map(item => (
                <option key={item.id} value={item.id}>{item.name || item.address} ({item.address})</option>
              ))}
            </select>
          </label>
          <label className="rd-tf-modal-field">
            <span>目标目录</span>
            <input spellCheck={false} value={scpDialog.targetDir}
              onChange={e => setScpDialog(prev => prev ? { ...prev, targetDir: e.currentTarget.value } : prev)}
              placeholder="例如 /opt 或 ." />
          </label>
          <label className="rd-tf-modal-field">
            <span>传输模式</span>
            <select value={scpDialog.mode} onChange={e => setScpDialog(prev => prev ? { ...prev, mode: e.currentTarget.value as 'copy' | 'move' } : prev)}>
              <option value="copy">复制（保留源文件）</option>
              <option value="move">移动（传输完成后删除源文件）</option>
            </select>
          </label>
          <label className="rd-w-check">
            <input type="checkbox" checked={scpDialog.overwrite}
              onChange={e => setScpDialog(prev => prev ? { ...prev, overwrite: e.currentTarget.checked } : prev)} />
            允许覆盖目标服务器上的同名条目
          </label>
        </div>
        <div className="rd-w-dialog-actions">
          <Button onClick={() => setScpDialog(undefined)}>取消</Button>
          <Button variant="primary" disabled={!scpDialog.targetHostId || !scpDialog.targetDir.trim()} onClick={submitScpTransfer}>
            <Send size={14} aria-hidden="true" />确认传输
          </Button>
        </div>
      </div>
    </div>}

    {archiveDialog && <div className="rd-w-dialog-backdrop" role="presentation" onClick={() => setArchiveDialog(undefined)}>
      <div className="rd-w-dialog rd-tf-modal" role="dialog" aria-modal="true"
        aria-label={archiveDialog.kind === 'archive' ? '压缩远程条目' : '解压远程归档'} onClick={e => e.stopPropagation()}>
        <h3>{archiveDialog.kind === 'archive' ? '压缩远程条目' : '解压远程归档'}</h3>
        <dl className="rd-w-kv">
          <div><dt>来源路径</dt><dd><code>{archiveDialog.entry.path}</code></dd></div>
        </dl>
        <div className="rd-tf-modal-fields">
          <label className="rd-tf-modal-field">
            <span>{archiveDialog.kind === 'archive' ? '输出压缩包路径（支持 .tar.gz 或 .zip）' : '解压目标目录'}</span>
            <input spellCheck={false} value={archiveDialog.destination}
              onChange={e => setArchiveDialog(prev => prev ? { ...prev, destination: e.currentTarget.value } : prev)} />
          </label>
          {archiveDialog.kind === 'archive' && activeDir && <div className="rd-tf-format-chips">
            <button type="button" className="rd-tf-mini-btn"
              onClick={() => setArchiveDialog(prev => prev ? { ...prev, destination: joinRemotePath(activeDir.path, `${prev.entry.name}.tar.gz`) } : prev)}>
              .tar.gz
            </button>
            <button type="button" className="rd-tf-mini-btn"
              onClick={() => setArchiveDialog(prev => prev ? { ...prev, destination: joinRemotePath(activeDir.path, `${prev.entry.name}.zip`) } : prev)}>
              .zip
            </button>
          </div>}
          <label className="rd-w-check">
            <input type="checkbox" checked={archiveDialog.overwrite}
              onChange={e => setArchiveDialog(prev => prev ? { ...prev, overwrite: e.currentTarget.checked } : prev)} />
            允许覆盖目标位置已存在的同名文件
          </label>
        </div>
        <div className="rd-w-dialog-actions">
          <Button onClick={() => setArchiveDialog(undefined)}>取消</Button>
          <Button variant="primary" disabled={!archiveDialog.destination.trim()} onClick={submitArchiveAction}>
            <Archive size={14} aria-hidden="true" />{archiveDialog.kind === 'archive' ? '确认压缩' : '确认解压'}
          </Button>
        </div>
      </div>
    </div>}

    {chmodDialog && <div className="rd-w-dialog-backdrop" role="presentation" onClick={() => setChmodDialog(undefined)}>
      <div className="rd-w-dialog rd-tf-perm-modal" role="dialog" aria-modal="true" aria-label="权限" onClick={e => e.stopPropagation()}>
        <div className="rd-tf-perm-head">
          <h3>权限</h3>
          <code className="rd-tf-perm-path" title={chmodDialog.entry.path}>{chmodDialog.entry.path}</code>
        </div>
        <div className="rd-tf-perm-card">
          {PERM_GROUPS.map(group => (
            <section key={group.key} className="rd-tf-perm-section">
              <div className="rd-tf-perm-section-title">{group.label}</div>
              <div className="rd-tf-perm-checks">
                <label className="rd-tf-perm-check is-read">
                  <input type="checkbox" aria-label={`${group.label}-读`}
                    checked={Boolean(chmodDialog.bits & group.read)}
                    onChange={() => setChmodDialog(prev => prev ? { ...prev, bits: prev.bits ^ group.read } : prev)} />
                  <span>读</span>
                </label>
                <label className="rd-tf-perm-check is-write">
                  <input type="checkbox" aria-label={`${group.label}-写`}
                    checked={Boolean(chmodDialog.bits & group.write)}
                    onChange={() => setChmodDialog(prev => prev ? { ...prev, bits: prev.bits ^ group.write } : prev)} />
                  <span>写</span>
                </label>
                <label className="rd-tf-perm-check is-exec">
                  <input type="checkbox" aria-label={`${group.label}-执行`}
                    checked={Boolean(chmodDialog.bits & group.exec)}
                    onChange={() => setChmodDialog(prev => prev ? { ...prev, bits: prev.bits ^ group.exec } : prev)} />
                  <span>执行</span>
                </label>
              </div>
            </section>
          ))}
          <section className="rd-tf-perm-section">
            <div className="rd-tf-perm-section-title">操作</div>
            <div className="rd-tf-perm-summary">
              权限:{formatOctalPerm(chmodDialog.bits)}，文本:{formatSymbolicPerm(chmodDialog.entry.directory, chmodDialog.bits, chmodDialog.specialBits)}
            </div>
            <div className="rd-tf-perm-options">
              <label className="rd-tf-perm-check is-opt">
                <input type="checkbox" checked={chmodDialog.sudo}
                  onChange={e => { const checked = e.currentTarget.checked; setChmodDialog(prev => prev ? { ...prev, sudo: checked } : prev); }} />
                <span>sudo 提权</span>
              </label>
              <label className="rd-tf-perm-check is-opt">
                <input type="checkbox" checked={chmodDialog.fast}
                  onChange={e => { const checked = e.currentTarget.checked; setChmodDialog(prev => prev ? { ...prev, fast: checked } : prev); }} />
                <span>快速执行</span>
              </label>
              <label className={`rd-tf-perm-check is-opt${chmodDialog.entry.directory ? '' : ' is-disabled'}`}>
                <input type="checkbox" checked={chmodDialog.entry.directory && chmodDialog.recursive}
                  disabled={!chmodDialog.entry.directory}
                  onChange={e => { const checked = e.currentTarget.checked; setChmodDialog(prev => prev ? { ...prev, recursive: checked } : prev); }} />
                <span>递归修改子目录及文件</span>
              </label>
            </div>
          </section>
        </div>
        <div className="rd-w-dialog-actions rd-tf-perm-actions">
          <Button onClick={() => setChmodDialog(undefined)}>关闭</Button>
          <Button variant="primary" disabled={action.busy} onClick={submitChmodAction}>确定</Button>
        </div>
      </div>
    </div>}

    {editor && <div className="rd-w-dialog-backdrop" role="presentation" onClick={() => setEditor(undefined)}>
      <div className="rd-w-dialog rd-tf-editor-modal" role="dialog" aria-modal="true" aria-label={`编辑 ${editor.name}`} onClick={e => e.stopPropagation()}>
        <div className="rd-tf-editor-head">
          <div>
            <h3>编辑远程文本文件</h3>
            <code className="rd-tf-editor-path">{editor.path}</code>
          </div>
          <button type="button" className="rd-tf-icon-btn" aria-label="关闭编辑器" onClick={() => setEditor(undefined)}>
            <X size={16} aria-hidden="true" />
          </button>
        </div>
        <textarea className="rd-tf-editor-area" spellCheck={false} value={editor.content}
          disabled={action.busy}
          onChange={e => setEditor(prev => prev ? { ...prev, content: e.currentTarget.value } : prev)} />
        <div className="rd-w-dialog-actions">
          <span className="rd-tf-editor-state">{editor.content === editor.original ? '未修改' : '有未保存更改'}</span>
          <Button onClick={() => setEditor(undefined)}>关闭</Button>
          <Button variant="primary" disabled={action.busy || editor.content === editor.original} onClick={submitEditorSave}>
            <Check size={14} aria-hidden="true" />保存修改
          </Button>
        </div>
      </div>
    </div>}

    {dialog}
  </aside>;
}
