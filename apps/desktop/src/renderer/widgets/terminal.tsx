import { useLayoutEffect, useRef, useState } from 'react';
import { ClipboardPaste, Copy, Plug, PlugZap, SquareTerminal } from 'lucide-react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import type { Bridge, CoreEvent, Host } from '@relaydock/protocol';
import { Button, Notice, PaneHeading, faultText, requireOk } from './ui';
import type { NoticeValue } from './ui';

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
}

function outputBlock(data: unknown) {
  return data && typeof data === 'object' && 'bytesB64' in data && typeof data.bytesB64 === 'string' ? data.bytesB64 : undefined;
}

export function TerminalPane({ api, host }: { api: Bridge; host: Host }) {
  const { id: hostId } = host;
  const container = useRef<HTMLDivElement>(null);
  const runtime = useRef<Runtime>(null);
  const [status, setStatus] = useState<Status>('idle');
  const [notice, setNotice] = useState<NoticeValue>();
  const [dimensions, setDimensions] = useState<Dimensions>();
  const [hasSelection, setHasSelection] = useState(false);

  useLayoutEffect(() => {
    const element = container.current;
    if (!element) return;
    const terminal = new Terminal({
      theme: { background: '#0B1422', foreground: '#E7EDF6', cursor: '#91CAFA', selectionBackground: '#35577F' },
      fontFamily: 'Cascadia Mono, Consolas, "Microsoft YaHei UI", monospace', fontSize: 14, lineHeight: 1.18,
      scrollback: 2000, cursorBlink: false, disableStdin: true,
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
    setStatus('idle');
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
      setStatus('closed');
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
          else setStatus('closed');
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
          setStatus('inputFailed');
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
      // Reconcile newer sizes, without replaying a request whose outcome was uncertain.
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
      setStatus('opening');
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
        setStatus('open');
        terminal.options.disableStdin = false;
        if (earlyTruncated) setNotice({ tone: 'info', text: '连接时终端输出过快，起始输出可能不完整。' });
        for (const event of early) deliver(event, target);
        if (session === target) { scheduleResize(target); terminal.focus(); }
      } catch (error) {
        if (!disposed && attempt === generation) { setNotice({ tone: 'error', text: faultText(error) }); setStatus('closed'); }
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
      setStatus('closing');
      try {
        requireOk(await api.call('terminal.close', { sessionId: target.id }), '核心未确认终端断开。');
        ended(target, undefined);
      } catch (error) {
        if (!disposed && session === target) {
          setStatus('closeFailed');
          setNotice({ tone: 'error', text: `无法确认会话已关闭。${faultText(error)}` });
        }
      } finally {
        if (session === target) closing = false;
      }
    }

    runtime.current = { open: () => void open(), close: () => void close(), copy: () => void copy(), paste: () => void paste() };
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
  }, [api, hostId]);

  return <>
    <PaneHeading title="远程终端" description="在独立 SSH 会话中操作所选主机。切换主机或离开终端时会关闭本次会话。" icon={<SquareTerminal size={25} />} host={host} />
    <section className="rd-w-terminal" aria-label="SSH 远程终端">
      <header className="rd-w-terminal-bar">
        <div className="rd-w-terminal-status" role="status"><span className={`rd-w-led${status === 'open' ? ' rd-w-led--active' : ''}`} aria-hidden="true" />{statusLabel[status]}</div>
        <div className="rd-w-actions">
          <Button disabled={!hasSelection} onClick={() => runtime.current?.copy()}><Copy aria-hidden="true" size={16} />复制选区</Button>
          <Button disabled={status !== 'open'} onClick={() => runtime.current?.paste()}><ClipboardPaste aria-hidden="true" size={16} />粘贴</Button>
          {status === 'open' || status === 'inputFailed' || status === 'closing' || status === 'closeFailed'
            ? <Button disabled={status === 'closing'} onClick={() => runtime.current?.close()}><Plug aria-hidden="true" size={16} />{status === 'closeFailed' ? '重试断开' : status === 'closing' ? '正在断开' : '断开会话'}</Button>
            : <Button variant="primary" disabled={status === 'opening'} onClick={() => runtime.current?.open()}><PlugZap aria-hidden="true" size={16} />{status === 'opening' ? '正在建立会话' : status === 'closed' ? '重新连接' : '建立会话'}</Button>}
        </div>
      </header>
      <div className="rd-w-terminal-view"><div ref={container} className="rd-w-xterm" /></div>
      <footer className="rd-w-terminal-footer"><span><kbd>Ctrl+C</kbd> 发送中断 · <kbd>Shift+Tab</kbd> 返回操作区 · 支持系统粘贴</span><span>{dimensions ? `${dimensions.cols} 列 × ${dimensions.rows} 行` : '尺寸待测'}</span></footer>
    </section>
    <Notice value={notice} />
  </>;
}
