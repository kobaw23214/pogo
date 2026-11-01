import { constants } from 'node:fs';
import { mkdir, mkdtemp, open, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';

export const MAX_UPLOAD_BYTES = 256 * 1024 * 1024;
export const MAX_UPLOAD_TOTAL_BYTES = 512 * 1024 * 1024;
export const MAX_UPLOAD_FILES = 8;
export const MAX_STAGED_UPLOAD_FILES = 128;
export const UPLOAD_ID = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
const MAX_UNUSED_AGE = 30 * 60_000;
const mimeTypes = { '.docx': 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', '.xlsx': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', '.doc': 'application/msword', '.xls': 'application/vnd.ms-excel', '.csv': 'text/csv', '.txt': 'text/plain', '.json': 'application/json', '.pdf': 'application/pdf', '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.zip': 'application/zip' };
function error(status, message, code) { return Object.assign(new Error(`${code}: ${message}`), { status, code }); }

export function uploadFilename(encoded) {
  if (typeof encoded !== 'string' || encoded.length > 1536) throw error(400, 'upload requires an encoded basename', 'UPLOAD_INVALID_FILENAME');
  let name;
  try { name = decodeURIComponent(encoded); } catch { throw error(400, 'upload filename is not valid URL encoding', 'UPLOAD_INVALID_FILENAME'); }
  if (!name || name === '.' || name === '..' || /[\\/\x00-\x1f\x7f]/.test(name) || Buffer.byteLength(name) > 255 || name.trim() !== name) throw error(400, 'upload filename must be a safe basename of at most 255 bytes', 'UPLOAD_INVALID_FILENAME');
  return name;
}

function uploadMime(name, supplied) {
  if (supplied !== undefined) {
    if (typeof supplied !== 'string' || supplied.length > 127 || !/^[a-zA-Z0-9!#$&^_.+-]+\/[a-zA-Z0-9!#$&^_.+-]+$/.test(supplied)) throw error(400, 'invalid upload MIME type', 'UPLOAD_INVALID_MIME');
    return supplied.toLowerCase();
  }
  return mimeTypes[/\.[^.]+$/.exec(name)?.[0].toLowerCase()] ?? 'application/octet-stream';
}

// This is a worker-wide byte budget, separate from each session's quota. The
// private /tmp mount is only 1 GiB; many sessions cannot each reserve 512 MiB.
export class UploadBudget {
  constructor(maxBytes = MAX_UPLOAD_TOTAL_BYTES) { this.maxBytes = maxBytes; this.used = 0; }
  change(bytes) {
    if (bytes > 0 && this.used + bytes > this.maxBytes) throw error(413, 'browser upload temporary storage is full', 'UPLOAD_STORAGE_LIMIT');
    this.used = Math.max(0, this.used + bytes);
  }
}
const workerBudget = new UploadBudget();

// One store belongs to exactly one BrowserSession. No caller-provided path is
// accepted, and metadata never exposes the private worker filesystem.
export class BrowserUploads {
  constructor({ maxBytes = MAX_UPLOAD_BYTES, maxTotal = MAX_UPLOAD_TOTAL_BYTES, maxItems = MAX_STAGED_UPLOAD_FILES, budget = workerBudget, now = Date.now, root = tmpdir() } = {}) {
    this.maxBytes = maxBytes; this.maxTotal = maxTotal; this.maxItems = maxItems; this.budget = budget; this.now = now; this.root = root;
    this.items = new Map(); this.directoryPromise = null; this.total = 0; this.closed = false; this.tasks = new Set();
    this.timer = setInterval(() => this.sweep().catch(() => {}), 60_000); this.timer.unref();
  }
  capabilities() { return { modes: ['input', 'chooser', 'drop'], max_file_bytes: this.maxBytes, max_total_bytes: this.maxTotal, max_files: MAX_UPLOAD_FILES, max_staged_files: this.maxItems, worker_max_total_bytes: this.budget.maxBytes }; }
  metadata(item) { return { id: item.id, name: item.name, size: item.size, mime_type: item.mime }; }
  directory() { return this.directoryPromise ??= mkdtemp(join(this.root, 'pogo-upload-')); }
  reserve(item, amount) {
    if (item.reserved + amount > this.maxBytes) throw error(413, 'file exceeds the browser upload limit', 'UPLOAD_TOO_LARGE');
    if (this.total + amount > this.maxTotal) throw error(413, 'session upload temporary storage is full', 'UPLOAD_TOTAL_LIMIT');
    this.budget.change(amount); this.total += amount; item.reserved += amount;
  }
  receive(source, { filename, mimeType, contentLength } = {}) {
    const task = this.receiveBytes(source, { filename, mimeType, contentLength });
    this.tasks.add(task); task.finally(() => this.tasks.delete(task)).catch(() => {}); return task;
  }
  async receiveBytes(source, { filename, mimeType, contentLength }) {
    if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
    const name = uploadFilename(filename), mime = uploadMime(name, mimeType);
    if (contentLength !== undefined && (typeof contentLength !== 'string' || !/^\d{1,12}$/.test(contentLength) || !Number.isSafeInteger(Number(contentLength)))) throw error(400, 'invalid upload content length', 'UPLOAD_INVALID_LENGTH');
    const length = contentLength === undefined ? null : Number(contentLength);
    if (length === 0) throw error(400, 'empty files cannot be uploaded', 'UPLOAD_EMPTY');
    if (this.items.size >= this.maxItems) throw error(429, 'session upload file capacity reached', 'UPLOAD_CAPACITY');
    const item = { id: randomUUID(), name, mime, reserved: 0, size: 0, state: 'receiving', source, lastUsed: this.now(), uses: 0, consumed: false };
    // Reserve synchronously, before filesystem awaits; concurrent transfers
    // cannot both consume the last slot or last bytes.
    if (length !== null) this.reserve(item, length);
    this.items.set(item.id, item);
    let handle;
    try {
      const directory = await this.directory();
      if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
      item.directory = join(directory, item.id); await mkdir(item.directory, { mode: 0o700 });
      item.path = join(item.directory, name);
      handle = await open(item.path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
      for await (const chunk of source) {
        if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
        const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
        if (length !== null && item.size + buffer.length > length) throw error(400, 'upload body exceeds declared length', 'UPLOAD_INVALID_LENGTH');
        if (length === null) this.reserve(item, buffer.length);
        // FileHandle.write may write only a prefix; do not silently truncate.
        let offset = 0;
        while (offset < buffer.length) offset += (await handle.write(buffer, offset, buffer.length - offset)).bytesWritten;
        item.size += buffer.length;
      }
      if (!item.size) throw error(400, 'empty files cannot be uploaded', 'UPLOAD_EMPTY');
      if (length !== null && item.size !== length) throw error(400, 'upload body does not match declared length', 'UPLOAD_INVALID_LENGTH');
      await handle.close(); handle = null;
      if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
      item.state = 'ready'; item.source = null; item.lastUsed = this.now();
      return this.metadata(item);
    } catch (failure) {
      await handle?.close().catch(() => {}); await this.remove(item).catch(() => {});
      throw String(failure.code ?? '').startsWith('UPLOAD_') ? failure : error(400, 'upload transfer did not complete; no file was staged', 'UPLOAD_INCOMPLETE');
    }
  }
  async acquire(ids) {
    if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
    if (!Array.isArray(ids) || !ids.length || ids.length > MAX_UPLOAD_FILES || new Set(ids).size !== ids.length || ids.some(id => typeof id !== 'string' || !UPLOAD_ID.test(id))) throw error(400, 'upload requires 1–8 unique staged file IDs', 'UPLOAD_INVALID_ID');
    const items = ids.map(id => {
      const item = this.items.get(id);
      if (!item) throw error(404, 'upload file does not belong to this browser session', 'UPLOAD_NOT_FOUND');
      if (item.state !== 'ready') throw error(409, 'upload file is not ready', 'UPLOAD_NOT_READY');
      return item;
    });
    for (const item of items) { item.uses++; item.lastUsed = this.now(); }
    try {
      for (const item of items) {
        const handle = await open(item.path, constants.O_RDONLY | constants.O_NOFOLLOW);
        try { const info = await handle.stat(); if (!info.isFile() || info.size !== item.size) throw error(410, 'upload file bytes changed or disappeared', 'UPLOAD_FILE_UNAVAILABLE'); }
        finally { await handle.close(); }
      }
      if (this.closed) throw error(410, 'browser upload session is closed', 'UPLOAD_SESSION_CLOSED');
    } catch (failure) {
      for (const item of items) item.uses--;
      throw String(failure.code ?? '').startsWith('UPLOAD_') ? failure : error(410, 'upload file bytes are no longer available', 'UPLOAD_FILE_UNAVAILABLE');
    }
    let released = false;
    return { paths: items.map(item => item.path), files: items.map(item => this.metadata(item)),
      // Mark before browser dispatch: even an unknown outcome must retain the
      // backing bytes, since the website may already hold a File object.
      retain: () => { for (const item of items) item.consumed = true; },
      release: () => { if (released) return; released = true; for (const item of items) { item.uses--; item.lastUsed = this.now(); } },
    };
  }
  async remove(item) {
    if (this.items.get(item.id) !== item) return;
    item.state = 'removed'; this.items.delete(item.id); this.total -= item.reserved; this.budget.change(-item.reserved); item.reserved = 0;
    if (item.directory) await rm(item.directory, { recursive: true, force: true });
  }
  async delete(id) {
    if (typeof id !== 'string' || !UPLOAD_ID.test(id)) throw error(400, 'invalid upload ID', 'UPLOAD_INVALID_ID');
    const item = this.items.get(id);
    if (!item) return;
    if (item.uses || item.state === 'receiving') throw error(409, 'upload file is currently in use', 'UPLOAD_BUSY');
    // A page may read its File asynchronously after input/change. Explicit
    // cleanup can only remove unused staging, never invalidate a selected file.
    if (item.consumed) throw error(409, 'selected upload is retained until the browser session closes', 'UPLOAD_RETAINED');
    await this.remove(item);
  }
  async sweep() {
    if (this.closed || this.sweeping) return; this.sweeping = true;
    try { for (const item of this.items.values()) if (!item.consumed && !item.uses && item.state === 'ready' && this.now() - item.lastUsed > MAX_UNUSED_AGE) await this.remove(item); }
    finally { this.sweeping = false; }
  }
  async close() {
    if (this.closePromise) return this.closePromise;
    this.closed = true; clearInterval(this.timer);
    this.closePromise = (async () => {
      // The receive task may still be awaiting mkdir, before its async iterator
      // has installed stream error handlers. Destroy without an error event;
      // the task's closed check supplies the scoped failure itself.
      for (const item of this.items.values()) if (item.state === 'receiving') item.source?.destroy?.();
      await Promise.allSettled([...this.tasks]);
      await Promise.allSettled([...this.items.values()].map(item => this.remove(item)));
      const directory = this.directoryPromise && await this.directoryPromise.catch(() => null);
      if (directory) await rm(directory, { recursive: true, force: true });
    })();
    return this.closePromise;
  }
}
