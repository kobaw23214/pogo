import { EventEmitter } from 'node:events';
import { createInterface } from 'node:readline';
import { connect } from 'node:net';

// Private local IPC only. A timeout has an unknown outcome: never replay a
// request, and never let a late reply authorize a different browser operation.
export class LineRPC extends EventEmitter {
  constructor(input, output, { maxLine = 131072, timeout = 10000 } = {}) {
    super(); this.input = input; this.output = output; this.timeout = timeout;
    this.next = 0; this.pending = new Map(); this.closed = false;
    let pendingBytes = 0;
    input.on('data', bytes => {
      const buffer = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
      for (let offset = 0; offset < buffer.length;) {
        const newline = buffer.indexOf(10, offset), end = newline < 0 ? buffer.length : newline;
        pendingBytes += end - offset;
        if (pendingBytes > maxLine) { this.fail(new Error('local IPC response exceeds limit')); input.destroy(); return; }
        if (newline < 0) break;
        pendingBytes = 0; offset = newline + 1;
      }
    });
    this.lines = createInterface({ input, crlfDelay: Infinity });
    this.lines.on('line', line => {
      if (line.length > maxLine) return this.fail(new Error('local IPC response exceeds limit'));
      let value;
      try { value = JSON.parse(line); } catch { return this.fail(new Error('invalid local IPC response')); }
      if (!value || typeof value !== 'object' || Array.isArray(value)) return this.fail(new Error('invalid local IPC response'));
      if (value.id !== undefined) {
        const pending = this.pending.get(value.id);
        if (!pending) return;
        clearTimeout(pending.timer); this.pending.delete(value.id);
        if (value.ok === false || value.error) {
          const suppliedCode = typeof value.error === 'string' ? value.error : value.error?.code;
          const code = ['INVALID_REQUEST', 'SCOPE_MISMATCH', 'CAPACITY_REACHED', 'MEDIA_FAILED', 'HOST_FAILED', 'IPV6_UNSUPPORTED'].includes(suppliedCode) ? suppliedCode : 'FAILED';
          // Diagnostic reason is a fixed enum; never echo request arguments,
          // arbitrary exception text, capabilities or page content.
          const reason = /unknown window/i.test(value.error?.message ?? '') ? 'WINDOW_GONE' : 'REJECTED';
          const error = new Error(`local browser service rejected operation: ${code}`);
          error.localOperation = pending.op; error.localCode = code; error.localReason = reason;
          this.emit('rejected', { operation: pending.op, code, reason }); pending.reject(error);
        }
        else pending.resolve(value.result);
      } else this.emit('event', value);
    });
    this.lines.on('close', () => this.fail(new Error('local browser IPC closed')));
    input.on('error', () => this.fail(new Error('local browser IPC failed')));
    output.on('error', () => this.fail(new Error('local browser IPC failed')));
  }
  request(op, args = {}, timeout = this.timeout) {
    if (this.closed) return Promise.reject(new Error('local browser IPC is closed'));
    const id = `r${++this.next}`;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        const error = new Error('local browser operation outcome is unknown; do not replay'); error.status = 504;
        this.emit('uncertain', error); reject(error);
      }, timeout);
      this.pending.set(id, { resolve, reject, timer, op });
      this.output.write(JSON.stringify({ id, op, args }) + '\n', error => {
        if (error) this.fail(new Error('local browser IPC failed'));
      });
    });
  }
  waitForEvent(predicate, timeout = this.timeout) {
    if (this.closed) return Promise.reject(new Error('local browser IPC is closed'));
    return new Promise((resolve, reject) => {
      const handler = value => { if (predicate(value)) { cleanup(); resolve(value); } };
      const failure = error => { cleanup(); reject(error); };
      const timer = setTimeout(() => { cleanup(); reject(new Error('local browser service did not become ready')); }, timeout);
      const cleanup = () => { clearTimeout(timer); this.off('event', handler); this.off('failed', failure); };
      this.on('event', handler); this.once('failed', failure);
    });
  }
  fail(error) {
    if (this.closed) return;
    this.closed = true;
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear(); this.emit('failed', error); this.lines.close();
  }
  close() { this.fail(new Error('local browser IPC closed')); this.output.end(); }
}

export async function connectRPC(path, hello, timeout = 10000) {
  const socket = connect(path);
  await new Promise((resolve, reject) => { socket.once('connect', resolve); socket.once('error', reject); });
  const rpc = new LineRPC(socket, socket, { timeout });
  const ready = rpc.waitForEvent(value => value.type === 'ready' && value.version === 1);
  socket.write(JSON.stringify(hello) + '\n');
  await ready;
  return rpc;
}
