import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import {
  ChevronLeft, ChevronRight, ClipboardPaste, Copy, FolderOpen, Plug, PlugZap, Plus, SquareTerminal, X,
} from 'lucide-react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import type { Bridge, CoreEvent, Host } from '@relaydock/protocol';
import { Button, Empty, Notice, canConnect, faultText, remoteTarget, requireOk } from './ui';
import type { NoticeValue } from './ui';
import { TerminalFilePanel, type FileClipboard } from './terminal-files';

const INPUT_CHUNK = 4096;
const MAX_INPUT_QUEUE = 256 * 1024;
const MAX_EARLY_OUTPUT = 256 * 1024;
const MAX_OUTPUT_BLOCK = 64 * 1024;
const encoder = new TextEncoder();

Terminal.strings.promptLabel = '远程终端输入，Shift+Tab 返回操作区';
Terminal.strings.tooMuchOutput = '终端输出过多，已暂停逐行朗读。';

type Dimensions = { rows: number; cols: number };
type Status = 'idle' | 'opening' | 'open' | 'inputFailed' | 'closing' | 'closeFailed' | 'closed';
const statusLabel: Record<Status, string> = {
  idle: '未连接', opening: '正在连接', open: '已连接', inputFailed: '输入已暂停', closing: '正在断开', closeFailed: '断开未确认', closed: '已断开',
};

interface Session {
  id: string;
  sequence: number;
  ending: boolean;
  inputFailed: boolean;
  inputSending: boolean;
  inputTimer: number | undefined;
  input: Uint8Array[];
  inputBytes: number;
  resizeSending: boolean;
  resizeTimer: number | undefined;
  sentSize: Dimensions;
}

interface Runtime {
  open: () => void;
  close: () => void;
  copy: () => void;
  paste: () => void;
  fitAndFocus: () => void;
}

export interface TerminalOpenRequest {
  hostId: string;
  nonce: number;
  forceNew?: boolean;
}

interface TerminalTab {
  id: string;
  hostId: string;
  hostScope: string;
  sequence: number;
  status: Status;
  autoConnect: boolean;
}

function hostKey(host: Host) {
  return JSON.stringify([host.id, host.address, host.port, host.username, host.auth, host.fingerprint, host.credentialReady]);
}

function outputBlock(data: unknown) {
  return data && typeof data === 'object' && 'bytesB64' in data && typeof data.bytesB64 === 'string' ? data.bytesB64 : undefined;
}

function TerminalSessionView({ api, host, active, autoConnect, onStatusChange }: {
  api: Bridge; host: Host; active: boolean; autoConnect: boolean; onStatusChange: (status: Status) => void;
}) {
  const { id: hostId } = host;
  const scope = hostKey(host);
  const container = useRef<HTMLDivElement>(null);
  const runtime = useRef<Runtime>(null);
  const onStatusRef = useRef(onStatusChange);
  onStatusRef.current = onStatusChange;
  const [status, setStatus] = useState<Status>('idle');
  const [notice, setNotice] = useState<NoticeValue>();
  const [dimensions, setDimensions] = useState<Dimensions>();
  const [hasSelection, setHasSelection] = useState(false);

  const updateStatus = useCallback((next: Status) => {
    setStatus(next);
    onStatusRef.current(next);
  }, []);

  useLayoutEffect(() => {
    const element = container.current;
    if (!element) return;
    const terminal = new Terminal({
      theme: { background: '#0B1422', foreground: '#E7EDF6', cursor: '#91CAFA', selectionBackground: '#35577F' },
      fontFamily: 'Cascadia Mono, Consolas, "Microsoft YaHei UI", monospace', fontSize: 14, lineHeight: 1.2,
      scrollback: 3000, cursorBlink: true, disableStdin: true,
      // Remote output can contain OSC links. Never navigate outside the workbench from a PTY.
      linkHandler: { activate: () => undefined },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(element);
    let disposed = false;
    let session: Session | undefined;
    let opening = false;
    let closing = false;
    let generation = 0;
    let desired: Dimensions = { rows: terminal.rows, cols: terminal.cols };
    let early: CoreEvent[] = [];
    let earlySize = 0;
    let earlyTruncated = false;
    updateStatus('idle');
    setNotice(undefined);
    setHasSelection(false);

    function dropQueue(target: Session) {
      window.clearTimeout(target.inputTimer);
      window.clearTimeout(target.resizeTimer);
      target.inputTimer = undefined;
      target.resizeTimer = undefined;
      target.input = [];
      target.inputBytes = 0;
    }

    async function closeDetached(id: string) {
      try { await api.call('terminal.close', { sessionId: id }); } catch { /* Core or remote may already be gone. */ }
    }

    function ended(target: Session, reason: unknown) {
      if (disposed || session !== target) return;
      dropQueue(target);
      terminal.options.disableStdin = true;
      session = undefined;
      closing = false;
      updateStatus('closed');
      if (reason && typeof reason === 'object' && 'reason' in reason && typeof reason.reason === 'string' && reason.reason) {
        setNotice({ tone: 'info', text: reason.reason.slice(0, 300) });
      }
    }

    function deliver(event: CoreEvent, target: Session) {
      if (disposed || session !== target || event.sessionId !== target.id || (event.seq !== undefined && event.seq <= target.sequence)) return;
      if (event.seq !== undefined) target.sequence = event.seq;
      if (event.topic === 'terminal.closed') {
        ended(target, event.data);
      } else if (event.topic === 'terminal.output') {
        const block = outputBlock(event.data);
        if (!block || block.length > MAX_OUTPUT_BLOCK) return;
        try {
          terminal.write(Uint8Array.from(atob(block), char => char.charCodeAt(0)));
        } catch {
          setNotice({ tone: 'error', text: '终端收到无法解码的输出块。' });
        }
      }
    }

    const unsubscribe = api.subscribe(event => {
      if (disposed) return;
      if (event.topic === 'core.status' && event.data && typeof event.data === 'object' && 'ready' in event.data && event.data.ready === false) {
        if (opening || session) {
          generation++;
          opening = false;
          early = [];
          earlySize = 0;
          if (session) ended(session, undefined);
          else updateStatus('closed');
          setNotice({ tone: 'info', text: '本地核心已断开。恢复后请手动连接，未确认的输入不会重发。' });
        }
        return;
      }
      if (!event.sessionId || (event.topic !== 'terminal.output' && event.topic !== 'terminal.closed')) return;
      if (session) {
        deliver(event, session);
        return;
      }
      // A PTY can send its prompt before the open response. Never write that data until the ID is known.
      if (!opening) return;
      const size = event.topic === 'terminal.output' ? (outputBlock(event.data)?.length ?? 0) : 64;
      if (event.topic === 'terminal.closed') {
        if (early.length >= 192) {
          const drop = early.findIndex(item => item.topic === 'terminal.output');
          const [removed] = early.splice(drop < 0 ? 0 : drop, 1);
          earlySize -= outputBlock(removed?.data)?.length ?? 64;
          earlyTruncated = true;
        }
      } else if (early.length >= 192 || earlySize + size > MAX_EARLY_OUTPUT) {
        earlyTruncated = true;
        return;
      }
      early.push(event);
      earlySize += size;
    });

    function scheduleInput(target: Session) {
      if (disposed || session !== target || target.ending || target.inputFailed || target.inputSending || target.inputTimer !== undefined || !target.inputBytes) return;
      target.inputTimer = window.setTimeout(() => {
        target.inputTimer = undefined;
        void flushInput(target);
      }, 16);
    }

    async function flushInput(target: Session) {
      if (disposed || session !== target || target.ending || target.inputFailed || target.inputSending || !target.inputBytes) return;
      const chunk = new Uint8Array(Math.min(target.inputBytes, INPUT_CHUNK));
      for (let used = 0; used < chunk.length;) {
        const next = target.input[0]!;
        const count = Math.min(chunk.length - used, next.length);
        chunk.set(next.subarray(0, count), used);
        used += count;
        if (count === next.length) target.input.shift();
        else target.input[0] = next.subarray(count);
      }
      target.inputBytes -= chunk.length;
      target.inputSending = true;
      try {
        requireOk(await api.call('terminal.input', { sessionId: target.id, bytesB64: btoa(String.fromCharCode(...chunk)) }), '核心未确认终端输入。');
      } catch (error) {
        if (!disposed && session === target && !target.ending) {
          target.inputFailed = true;
          target.input = [];
          target.inputBytes = 0;
          terminal.options.disableStdin = true;
          updateStatus('inputFailed');
          setNotice({ tone: 'error', text: `终端输入中止，未确认的字节不会重发。请断开后重新连接。${faultText(error)}` });
        }
      } finally {
        target.inputSending = false;
        scheduleInput(target);
      }
    }

    function enqueue(bytes: Uint8Array) {
      if (!session || session.ending || session.inputFailed || !bytes.length) return;
      if (session.inputBytes + bytes.length > MAX_INPUT_QUEUE) {
        setNotice({ tone: 'info', text: '这次输入超过终端待发送上限，未加入队列。若是粘贴，请分段后再试。' });
        return;
      }
      session.input.push(bytes);
      session.inputBytes += bytes.length;
      scheduleInput(session);
    }

    function scheduleResize(target: Session) {
      if (disposed || session !== target || target.ending || target.resizeSending || target.resizeTimer !== undefined) return;
      if (target.sentSize.cols === desired.cols && target.sentSize.rows === desired.rows) return;
      target.resizeTimer = window.setTimeout(() => {
        target.resizeTimer = undefined;
        void flushResize(target);
      }, 70);
    }

    async function flushResize(target: Session) {
      if (disposed || session !== target || target.ending) return;
      const next = desired;
      if (target.sentSize.cols === next.cols && target.sentSize.rows === next.rows) return;
      target.resizeSending = true;
      target.sentSize = next;
      try {
        requireOk(await api.call('terminal.resize', { sessionId: target.id, ...next }), '核心未确认终端尺寸。');
      } catch (error) {
        if (!disposed && session === target && !target.ending) setNotice({ tone: 'info', text: `无法更新远端终端尺寸。${faultText(error)}` });
      } finally {
        target.resizeSending = false;
        scheduleResize(target);
      }
    }

    function measure() {
      if (disposed || !element?.clientWidth || !element.clientHeight) return;
      fit.fit();
      const next = { rows: Math.min(terminal.rows, 1000), cols: Math.min(terminal.cols, 1000) };
      if (desired.cols !== next.cols || desired.rows !== next.rows) {
        desired = next;
        if (session) scheduleResize(session);
      }
      setDimensions(old => old?.rows === next.rows && old.cols === next.cols ? old : next);
    }

    async function copy() {
      if (disposed) return;
      const selected = terminal.getSelection();
      if (!selected) return;
      try {
        await navigator.clipboard.writeText(selected);
        if (!disposed) setNotice({ tone: 'success', text: '已复制终端选区。' });
      } catch {
        if (!disposed) setNotice({ tone: 'info', text: '系统未允许写入剪贴板。' });
      }
    }

    async function paste() {
      const target = session;
      if (disposed || !target || target.ending || target.inputFailed) return;
      try {
        const text = await navigator.clipboard.readText();
        if (!disposed && session === target && !target.ending && !target.inputFailed) {
          terminal.focus();
          terminal.paste(text);
        }
      } catch {
        if (!disposed && session === target) {
          terminal.focus();
          setNotice({ tone: 'info', text: '系统未允许主动读取剪贴板。请使用系统粘贴快捷键。' });
        }
      }
    }

    function warnLargeInput() { setNotice({ tone: 'info', text: '输入内容太长，未加入队列。请分段操作。' }); }
    const data = terminal.onData(value => { if (value.length > MAX_INPUT_QUEUE) warnLargeInput(); else enqueue(encoder.encode(value)); });
    const binary = terminal.onBinary(value => { if (value.length > MAX_INPUT_QUEUE) warnLargeInput(); else enqueue(Uint8Array.from(value, char => char.charCodeAt(0) & 255)); });
    const selection = terminal.onSelectionChange(() => { if (!disposed) setHasSelection(terminal.hasSelection()); });
    terminal.attachCustomKeyEventHandler(event => {
      if (event.key === 'Tab' && event.shiftKey) return false;
      if (event.type === 'keydown' && event.key.toLowerCase() === 'c' && (event.metaKey || (event.ctrlKey && event.shiftKey))) {
        event.preventDefault();
        void copy();
        return false;
      }
      return true;
    });
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    const initialFrame = window.requestAnimationFrame(measure);

    async function open() {
      if (disposed || opening || session) return;
      const attempt = ++generation;
      opening = true;
      early = [];
      earlySize = 0;
      earlyTruncated = false;
      setNotice(undefined);
      updateStatus('opening');
      terminal.reset();
      measure();
      const firstSize = desired;
      try {
        const result = await api.call('terminal.open', { hostId, ...firstSize });
        if (!result.sessionId) throw new Error('远端连接未返回终端会话标识。');
        if (disposed || attempt !== generation) { void closeDetached(result.sessionId); return; }
        const target: Session = {
          id: result.sessionId, sequence: -1, ending: false, inputFailed: false, inputSending: false, inputTimer: undefined,
          input: [], inputBytes: 0, resizeSending: false, resizeTimer: undefined, sentSize: firstSize,
        };
        session = target;
        updateStatus('open');
        terminal.options.disableStdin = false;
        if (earlyTruncated) setNotice({ tone: 'info', text: '连接时终端输出过快，起始输出可能不完整。' });
        for (const event of early) deliver(event, target);
        if (session === target) { scheduleResize(target); terminal.focus(); }
      } catch (error) {
        if (!disposed && attempt === generation) { setNotice({ tone: 'error', text: faultText(error) }); updateStatus('closed'); }
      } finally {
        if (attempt === generation) { opening = false; early = []; earlySize = 0; }
      }
    }

    async function close() {
      if (disposed || closing || !session) return;
      const target = session;
      target.ending = true;
      closing = true;
      dropQueue(target);
      terminal.options.disableStdin = true;
      setNotice(undefined);
      updateStatus('closing');
      try {
        requireOk(await api.call('terminal.close', { sessionId: target.id }), '核心未确认终端断开。');
        ended(target, undefined);
      } catch (error) {
        if (!disposed && session === target) {
          updateStatus('closeFailed');
          setNotice({ tone: 'error', text: `无法确认会话已关闭。${faultText(error)}` });
        }
      } finally {
        if (session === target) closing = false;
      }
    }

    runtime.current = {
      open: () => void open(),
      close: () => void close(),
      copy: () => void copy(),
      paste: () => void paste(),
      fitAndFocus: () => {
        measure();
        if (session && !session.ending && !session.inputFailed) terminal.focus();
      },
    };
    if (autoConnect) void open();

    return () => {
      disposed = true;
      runtime.current = null;
      unsubscribe();
      observer.disconnect();
      window.cancelAnimationFrame(initialFrame);
      data.dispose(); binary.dispose(); selection.dispose();
      if (session) { dropQueue(session); if (!closing) void closeDetached(session.id); }
      early = [];
      terminal.dispose();
    };
  }, [api, hostId, scope, autoConnect, updateStatus]);

  useEffect(() => {
    if (!active) return;
    const frame = window.requestAnimationFrame(() => runtime.current?.fitAndFocus());
    return () => window.cancelAnimationFrame(frame);
  }, [active]);

  return <section className="rd-w-terminal rd-tw-session" aria-label={`SSH 远程终端 · ${host.name || host.address}`} hidden={!active}>
    <header className="rd-w-terminal-bar">
      <div className="rd-tw-session-meta">
        <div className="rd-w-terminal-status" role="status">
          <span className={`rd-w-led${status === 'open' ? ' rd-w-led--active' : status === 'opening' ? ' rd-w-led--busy' : ''}`} aria-hidden="true" />
          {statusLabel[status]}
        </div>
        <span className="rd-tw-session-target" title={remoteTarget(host)}>{remoteTarget(host)}</span>
      </div>
      <div className="rd-w-actions">
        <Button disabled={!hasSelection} onClick={() => runtime.current?.copy()}><Copy aria-hidden="true" size={15} />复制选区</Button>
        <Button disabled={status !== 'open'} onClick={() => runtime.current?.paste()}><ClipboardPaste aria-hidden="true" size={15} />粘贴</Button>
        {status === 'open' || status === 'inputFailed' || status === 'closing' || status === 'closeFailed'
          ? <Button disabled={status === 'closing'} onClick={() => runtime.current?.close()}><Plug aria-hidden="true" size={15} />{status === 'closeFailed' ? '重试断开' : status === 'closing' ? '正在断开' : '断开会话'}</Button>
          : <Button variant="primary" disabled={status === 'opening'} onClick={() => runtime.current?.open()}><PlugZap aria-hidden="true" size={15} />{status === 'opening' ? '正在建立会话' : status === 'closed' ? '重新连接' : '建立会话'}</Button>}
      </div>
    </header>
    {notice && <div className="rd-tw-session-notice"><Notice value={notice} /></div>}
    <div className="rd-w-terminal-view rd-tw-view"><div ref={container} className="rd-w-xterm" /></div>
    <footer className="rd-w-terminal-footer">
      <span><kbd>Ctrl+C</kbd> 发送中断 · <kbd>Shift+Tab</kbd> 返回操作区 · 支持系统粘贴</span>
      <span>{dimensions ? `${dimensions.cols} 列 × ${dimensions.rows} 行` : '尺寸待测'}</span>
    </footer>
  </section>;
}

export function TerminalPane({ api, host, hosts, openRequest, onSelectHost }: {
  api: Bridge; host?: Host; hosts?: Host[]; openRequest?: TerminalOpenRequest; onSelectHost?: (id: string) => void;
}) {
  const allHosts = useMemo(() => {
    if (hosts && hosts.length > 0) return hosts;
    return host ? [host] : [];
  }, [hosts, host]);

  const hostMap = useMemo(() => {
    const map = new Map<string, Host>();
    for (const item of allHosts) map.set(item.id, item);
    if (host && !map.has(host.id)) map.set(host.id, host);
    return map;
  }, [allHosts, host]);

  const tabCounter = useRef(0);
  const lastRequestNonce = useRef<number>(openRequest?.nonce ?? 0);
  const [tabs, setTabs] = useState<TerminalTab[]>(() => {
    if (!canConnect(host)) return [];
    tabCounter.current = 1;
    return [{ id: 'tab-1', hostId: host.id, hostScope: hostKey(host), sequence: 1, status: 'idle', autoConnect: false }];
  });
  const [activeTabId, setActiveTabId] = useState<string | undefined>(() => (canConnect(host) ? 'tab-1' : undefined));
  const [filesOpen, setFilesOpen] = useState(true);
  const [filesWidth, setFilesWidth] = useState(360);
  const [fileClipboard, setFileClipboard] = useState<FileClipboard>();
  const workbenchRef = useRef<HTMLDivElement>(null);

  const openTabForHost = useCallback((target: Host, options: { forceNew: boolean; autoConnect: boolean }) => {
    if (!canConnect(target)) return;
    const scope = hostKey(target);
    setTabs(previous => {
      if (!options.forceNew) {
        const existing = [...previous].reverse().find(item => item.hostId === target.id && item.hostScope === scope);
        if (existing) {
          setActiveTabId(existing.id);
          return previous;
        }
      }
      const nextId = `tab-${++tabCounter.current}`;
      const sameHostCount = previous.filter(item => item.hostId === target.id).length;
      const nextTab: TerminalTab = {
        id: nextId,
        hostId: target.id,
        hostScope: scope,
        sequence: sameHostCount + 1,
        status: 'idle',
        autoConnect: options.autoConnect,
      };
      setActiveTabId(nextId);
      return [...previous, nextTab];
    });
  }, []);

  // Remove tabs whose hosts were deleted, invalidated, or had connection credentials changed.
  useEffect(() => {
    setTabs(previous => {
      const next = previous.filter(tab => {
        const current = hostMap.get(tab.hostId);
        return canConnect(current) && hostKey(current) === tab.hostScope;
      });
      if (next.length === previous.length) return previous;
      setActiveTabId(currentActive => (currentActive && next.some(t => t.id === currentActive) ? currentActive : next[next.length - 1]?.id));
      return next;
    });
  }, [hostMap]);

  // Handle explicit open/switch requests from the left server rail.
  useEffect(() => {
    if (!openRequest || openRequest.nonce === lastRequestNonce.current) return;
    lastRequestNonce.current = openRequest.nonce;
    const target = hostMap.get(openRequest.hostId);
    if (canConnect(target)) {
      openTabForHost(target, { forceNew: Boolean(openRequest.forceNew), autoConnect: true });
    }
  }, [openRequest, hostMap, openTabForHost]);

  // When no tabs are open yet and a connectable host becomes selected, create a ready-to-connect tab.
  useEffect(() => {
    if (tabs.length === 0 && canConnect(host)) {
      openTabForHost(host, { forceNew: false, autoConnect: false });
    }
  }, [tabs.length, host, openTabForHost]);

  const activeTab = tabs.find(item => item.id === activeTabId) ?? tabs[tabs.length - 1];
  const activeHost = activeTab ? hostMap.get(activeTab.hostId) : (canConnect(host) ? host : undefined);
  const newTabTarget = activeHost ?? (canConnect(host) ? host : allHosts.find(canConnect));

  function selectTab(tab: TerminalTab) {
    setActiveTabId(tab.id);
    if (onSelectHost && tab.hostId !== host?.id) onSelectHost(tab.hostId);
  }

  function closeTab(tabId: string) {
    setTabs(previous => {
      const idx = previous.findIndex(item => item.id === tabId);
      if (idx < 0) return previous;
      const next = previous.filter(item => item.id !== tabId);
      if (activeTabId === tabId) {
        const fallback = next[Math.min(idx, next.length - 1)];
        setActiveTabId(fallback?.id);
        if (fallback && onSelectHost && fallback.hostId !== host?.id) onSelectHost(fallback.hostId);
      }
      return next;
    });
  }

  function updateTabStatus(tabId: string, nextStatus: Status) {
    setTabs(previous => previous.map(item => (item.id === tabId && item.status !== nextStatus ? { ...item, status: nextStatus } : item)));
  }

  function startResize(event: ReactPointerEvent<HTMLDivElement>) {
    if (!filesOpen) return;
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = filesWidth;
    const containerWidth = workbenchRef.current?.clientWidth ?? 1200;
    const maxFilesWidth = Math.max(280, Math.min(640, Math.floor(containerWidth * 0.55)));

    function onMove(moveEvent: PointerEvent) {
      const delta = startX - moveEvent.clientX;
      setFilesWidth(Math.max(260, Math.min(maxFilesWidth, startWidth + delta)));
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }

  const hostTabCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const tab of tabs) counts.set(tab.hostId, (counts.get(tab.hostId) ?? 0) + 1);
    return counts;
  }, [tabs]);

  return <div ref={workbenchRef} className={`rd-tw-workbench${filesOpen ? '' : ' rd-tw-files-collapsed'}`}
    style={filesOpen ? { gridTemplateColumns: `minmax(0, 1fr) 8px ${filesWidth}px` } : { gridTemplateColumns: 'minmax(0, 1fr) auto' }}>
    <div className="rd-tw-center">
      <header className="rd-tw-tabbar">
        <div className="rd-tw-brand">
          <SquareTerminal size={16} aria-hidden="true" />
          <h2>远程终端</h2>
        </div>
        <div className="rd-tw-tabs" role="tablist" aria-label="已打开的终端会话">
          {tabs.map(tab => {
            const tabHost = hostMap.get(tab.hostId);
            if (!tabHost) return null;
            const isSelected = activeTab?.id === tab.id;
            const showSeq = (hostTabCounts.get(tab.hostId) ?? 0) > 1 || tab.sequence > 1;
            const title = `${tabHost.name || tabHost.address}${showSeq ? ` #${tab.sequence}` : ''}`;
            return <div key={tab.id} className={`rd-tw-tab${isSelected ? ' is-active' : ''}`}
              onAuxClick={event => { if (event.button === 1) { event.preventDefault(); closeTab(tab.id); } }}>
              <button type="button" role="tab" aria-selected={isSelected} className="rd-tw-tab-main"
                title={`${title} (${remoteTarget(tabHost)})`} onClick={() => selectTab(tab)}>
                <span className={`rd-tw-tab-dot is-${tab.status}`} aria-hidden="true" />
                <span className="rd-tw-tab-title">{title}</span>
              </button>
              <button type="button" className="rd-tw-tab-close" aria-label={`关闭终端 ${title}`} title="关闭标签"
                onClick={() => closeTab(tab.id)}>
                <X size={13} aria-hidden="true" />
              </button>
            </div>;
          })}
        </div>
        <div className="rd-tw-tabbar-actions">
          <button type="button" className="rd-tw-icon-btn" disabled={!newTabTarget}
            title={newTabTarget ? `为 ${newTabTarget.name || newTabTarget.address} 新开终端标签` : '请先在左侧选择可用服务器'}
            aria-label="新建终端标签"
            onClick={() => { if (newTabTarget) openTabForHost(newTabTarget, { forceNew: true, autoConnect: true }); }}>
            <Plus size={16} aria-hidden="true" />
          </button>
          <button type="button" className={`rd-tw-icon-btn${filesOpen ? ' is-active' : ''}`}
            title={filesOpen ? '收起右侧文件列表' : '展开右侧文件列表'}
            aria-label={filesOpen ? '收起右侧文件列表' : '展开右侧文件列表'}
            aria-expanded={filesOpen}
            onClick={() => setFilesOpen(prev => !prev)}>
            <FolderOpen size={15} aria-hidden="true" />
          </button>
        </div>
      </header>

      <div className="rd-tw-stage">
        {tabs.map(tab => {
          const tabHost = hostMap.get(tab.hostId);
          if (!tabHost) return null;
          return <TerminalSessionView key={tab.id} api={api} host={tabHost} active={activeTab?.id === tab.id}
            autoConnect={tab.autoConnect} onStatusChange={next => updateTabStatus(tab.id, next)} />;
        })}
        {tabs.length === 0 && <div className="rd-tw-empty">
          <Empty icon={<SquareTerminal size={24} />}
            title={host && !canConnect(host) ? '当前主机尚不能建立会话' : '点击左侧服务器列表打开终端'}
            description={host && !canConnect(host)
              ? '请在主机管理中补录凭据并验证 SSH 主机指纹，保存后即可打开终端。'
              : '支持同时多开多个服务器终端标签，右侧将同步显示当前服务器的远端文件列表。'} />
        </div>}
      </div>
    </div>

    <div className="rd-tw-splitter" role="separator" aria-orientation="vertical" aria-label="调整终端与文件列表宽度"
      onPointerDown={startResize}>
      <button type="button" className="rd-tw-splitter-toggle"
        title={filesOpen ? '收起文件列表' : '展开文件列表'}
        aria-label={filesOpen ? '收起文件列表' : '展开文件列表'}
        onPointerDown={event => event.stopPropagation()}
        onClick={() => setFilesOpen(prev => !prev)}>
        {filesOpen ? <ChevronRight size={14} aria-hidden="true" /> : <ChevronLeft size={14} aria-hidden="true" />}
      </button>
    </div>

    {filesOpen && <TerminalFilePanel api={api} host={activeHost} hosts={allHosts} clipboard={fileClipboard} onClipboardChange={setFileClipboard} />}
  </div>;
}
