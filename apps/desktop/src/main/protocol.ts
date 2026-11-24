import { TextDecoder } from 'node:util';
import { MAX_FRAME, VERSION, type CoreEvent, type Fault } from '../../../../packages/protocol/index';
import { DesktopFault } from './faults';

const utf8 = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
const has = (object: object, key: string) => Object.hasOwn(object, key);

export function isRecord(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== 'object') return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function keysAre(value: object, allowed: readonly string[]): boolean {
  return Object.keys(value).every(key => allowed.includes(key));
}

export type Response = { id: string } & ({ ok: true; result: unknown } | { ok: false; error: Fault });
export type CoreFrame = ({ type: 'response' } & Response) | CoreEvent;

function validID(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= 256 &&
    !Array.from(value).some(character => character.charCodeAt(0) < 32);
}

export function decodeFrame(bytes: Buffer): CoreFrame {
  if (bytes.length === 0 || bytes.length >= MAX_FRAME) throw new DesktopFault('BAD_FRAME');
  let value: unknown;
  try {
    value = JSON.parse(utf8.decode(bytes));
  } catch {
    throw new DesktopFault('BAD_FRAME');
  }
  if (!isRecord(value)) throw new DesktopFault('BAD_FRAME');
  if (value.v !== VERSION) throw new DesktopFault('VERSION_MISMATCH');
  if (value.type === 'response' && typeof value.id === 'string' && /^r[1-9][0-9]{0,15}$/u.test(value.id)) {
    if (value.ok === true && has(value, 'result') && keysAre(value, ['v', 'type', 'id', 'ok', 'result'])) {
      return { type: 'response', id: value.id, ok: true, result: value.result };
    }
    const error = value.error;
    if (value.ok === false && keysAre(value, ['v', 'type', 'id', 'ok', 'error']) && isRecord(error) &&
        keysAre(error, ['code', 'message', 'retryable']) && typeof error.code === 'string' &&
        /^[A-Z][A-Z0-9_]{0,63}$/u.test(error.code) && typeof error.message === 'string' &&
        typeof error.retryable === 'boolean') {
      return { type: 'response', id: value.id, ok: false, error: { code: error.code, message: error.message, retryable: error.retryable } };
    }
  }
  if (value.type === 'event' && keysAre(value, ['v', 'type', 'topic', 'taskId', 'sessionId', 'seq', 'data']) &&
      typeof value.topic === 'string' && /^[a-z][a-zA-Z0-9_.-]{0,79}$/u.test(value.topic) && value.topic !== 'core.status' &&
      has(value, 'data') && (!has(value, 'taskId') || validID(value.taskId)) &&
      (!has(value, 'sessionId') || validID(value.sessionId)) &&
      (!has(value, 'seq') || (typeof value.seq === 'number' && Number.isSafeInteger(value.seq) && value.seq >= 0))) {
    return value as unknown as CoreEvent;
  }
  throw new DesktopFault('BAD_FRAME');
}

export function encodeRequest(id: string, method: string, params: unknown): Buffer {
  const json = JSON.stringify({ v: VERSION, type: 'request', id, method, params });
  // Match Go's Scanner limit: strictly fewer than MAX_FRAME bytes, then LF.
  if (Buffer.byteLength(json, 'utf8') >= MAX_FRAME) throw new DesktopFault('FRAME_LIMIT');
  return Buffer.from(`${json}\n`, 'utf8');
}

export class FrameReader {
  private tail = Buffer.alloc(0);
  private used = 0;

  push(chunk: Buffer, consume: (frame: CoreFrame) => void): void {
    for (let start = 0; start < chunk.length;) {
      const newline = chunk.indexOf(0x0a, start);
      const end = newline === -1 ? chunk.length : newline;
      const size = this.used + end - start;
      if (size >= MAX_FRAME) throw new DesktopFault('FRAME_LIMIT');
      if (this.used === 0 && newline !== -1) {
        consume(decodeFrame(chunk.subarray(start, end)));
      } else {
        if (size > this.tail.length) {
          const grown = Buffer.allocUnsafe(Math.min(MAX_FRAME - 1, Math.max(4096, this.tail.length * 2, size)));
          this.tail.copy(grown, 0, 0, this.used);
          this.tail = grown;
        }
        chunk.copy(this.tail, this.used, start, end);
        this.used = size;
        if (newline !== -1) {
          const frame = decodeFrame(this.tail.subarray(0, size));
          this.used = 0;
          consume(frame);
        }
      }
      if (newline === -1) break;
      start = newline + 1;
    }
  }

  finish(): void {
    if (this.used > 0) throw new DesktopFault('BAD_FRAME');
  }
}
