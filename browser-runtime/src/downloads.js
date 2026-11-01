import { constants } from 'node:fs';
import { mkdir, open, rm, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { HTTPError } from './session.js';

export const MAX_DOWNLOAD_BYTES = 512 * 1024 * 1024;
const GUID = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
const MAX_ITEMS = 16, MAX_TOTAL = 2 * 1024 * 1024 * 1024, MAX_AGE = 15 * 60_000, MAX_ACTIVE = 5 * 60_000;
const safeBytes = value => Math.min(Number.MAX_SAFE_INTEGER, Math.floor(value));
const failure = (status, message, code) => new HTTPError(status, message, code);
const filename = value => String(value || 'download').replace(/[\\/\x00-\x1f\x7f]/g, '_').replace(/^\.+/, '').slice(0, 180) || 'download';
// Do not propagate credentials, signed queries, fragments or data URLs into
// conversation metadata. Bytes come from Chromium, never a second URL fetch.
const sourceURL = value => {
  try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) ? `${url.origin}${url.pathname}`.slice(0, 2048) : url.protocol === 'blob:' ? `blob:${url.origin}` : ''; }
  catch { return ''; }
};

// One instance belongs to one Chromium profile. Only this trusted host can set
// download behavior; page/agent CDP method allowlists do not expose this API.
export class BrowserDownloads {
  constructor({ client, directory, ownerForFrame, onBegin, contextId, maxBytes = MAX_DOWNLOAD_BYTES, maxTotal = MAX_TOTAL, maxItems = MAX_ITEMS, now = Date.now }) {
    this.client = client; this.directory = directory; this.ownerForFrame = ownerForFrame; this.contextId = contextId;
    this.onBegin = onBegin;
    this.maxBytes = maxBytes; this.maxTotal = maxTotal; this.maxItems = maxItems; this.now = now;
    this.items = new Map(); this.begun = new Set(); this.tasks = new Set(); this.closed = false; this.rejected = 0;
    this.beginListener = event => this.begin(event);
    this.progressListener = event => this.track(this.progress(event));
  }
  scope() { return this.contextId ? { browserContextId: this.contextId } : {}; }
  track(task) { this.tasks.add(task); task.finally(() => this.tasks.delete(task)).catch(() => {}); return task; }
  async start() {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    this.client.on('Browser.downloadWillBegin', this.beginListener);
    this.client.on('Browser.downloadProgress', this.progressListener);
    try { await this.client.send('Browser.setDownloadBehavior', { ...this.scope(), behavior: 'allowAndName', downloadPath: this.directory, eventsEnabled: true }); }
    catch (error) { this.client.off('Browser.downloadWillBegin', this.beginListener); this.client.off('Browser.downloadProgress', this.progressListener); throw error; }
    this.timer = setInterval(() => this.track(this.sweep()), 1000); this.timer.unref();
    return this;
  }
  cancel(id) { return this.client.send('Browser.cancelDownload', { ...this.scope(), guid: id }).catch(() => {}); }
  async removeBytes(id) { await Promise.all([id, `${id}.crdownload`].map(name => rm(join(this.directory, name), { force: true }))); }
  begin(event) {
    if (this.closed || !GUID.test(event.guid || '')) return;
    // Keep bounded recent receipts after rejection/ACK. Active records always
    // deduplicate, even after a page floods the recent receipt window.
    if (this.items.has(event.guid) || this.begun.has(event.guid)) return;
    this.begun.add(event.guid);
    if (this.begun.size > 512) this.begun.delete(this.begun.values().next().value);
    const owner = this.ownerForFrame(event.frameId);
    // A confirmed handoff leaves the initiating web document in place, even
    // when download policy subsequently rejects or cancels the file.
    if (owner) this.onBegin?.(event, owner);
    if (!owner || this.items.size >= this.maxItems) {
      this.rejected++; this.lastRejection = owner ? 'DOWNLOAD_CAPACITY' : 'DOWNLOAD_UNBOUND_PAGE';
      this.track(this.cancel(event.guid).then(() => this.removeBytes(event.guid))); return;
    }
    this.items.set(event.guid, { id: event.guid, filename: filename(event.suggestedFilename), url: sourceURL(event.url),
      tab_id: owner.tab_id, context_ref: owner.context_ref, control: owner.control,
      state: 'in_progress', received_bytes: 0, total_bytes: 0, created_at: new Date(this.now()).toISOString(), started: this.now() });
  }
  async fail(item, code, message, bytes) {
    if (item.state !== 'in_progress') return;
    item.state = 'failed'; item.error = message; item.error_code = code;
    if (bytes) { item.actual_bytes = safeBytes(bytes.actual); item.limit_bytes = safeBytes(bytes.limit); }
    await this.cancel(item.id); await this.removeBytes(item.id);
  }
  reserved(item) { return item.state === 'completed' ? item.size : item.state === 'in_progress' ? Math.max(item.received_bytes, item.total_bytes) : 0; }
  total(excluded) { return [...this.items.values()].reduce((sum, item) => item === excluded ? sum : safeBytes(sum + this.reserved(item)), 0); }
  enforceQuota(item, fileBytes = this.reserved(item), totalBytes = this.total()) {
    if (fileBytes > this.maxBytes) return this.fail(item, 'DOWNLOAD_TOO_LARGE', 'Download exceeds the single-file size limit', { actual: fileBytes, limit: this.maxBytes });
    if (totalBytes > this.maxTotal) return this.fail(item, 'DOWNLOAD_TOTAL_LIMIT', 'Download exceeds the browser temporary storage limit', { actual: totalBytes, limit: this.maxTotal });
    return null;
  }
  async progress(event) {
    const item = this.items.get(event.guid); if (!item || item.state !== 'in_progress' || this.closed) return;
    for (const [source, target] of [['receivedBytes', 'received_bytes'], ['totalBytes', 'total_bytes']]) {
      if (Number.isFinite(event[source]) && event[source] >= 0) item[target] = Math.max(item[target], safeBytes(event[source]));
    }
    // Reserve a known length immediately, even before any bytes arrive. An
    // unknown stream instead grows its reservation with progress and disk size.
    const quota = this.enforceQuota(item); if (quota) return quota;
    if (event.state === 'canceled') return this.fail(item, 'DOWNLOAD_CANCELED', 'Chromium canceled or failed the download');
    if (event.state !== 'completed') return;
    try {
      // Chromium's event filePath is deliberately ignored; only its GUID under
      // the private allowAndName directory can ever become a readable payload.
      const handle = await open(join(this.directory, item.id), constants.O_RDONLY | constants.O_NOFOLLOW);
      try {
        const info = await handle.stat();
        if (item.state !== 'in_progress' || this.closed || this.items.get(item.id) !== item) return;
        if (!info.isFile()) throw new Error('download is not a regular file');
        // Replace this item's reservation with its actual completed file size;
        // do not double-count its advertised length alongside the stored bytes.
        const quota = this.enforceQuota(item, info.size, safeBytes(this.total(item) + info.size)); if (quota) return await quota;
        item.size = info.size; item.received_bytes = info.size; item.state = 'completed'; item.completed_at = new Date(this.now()).toISOString();
      } finally { await handle.close(); }
    } catch { await this.fail(item, 'DOWNLOAD_FILE_UNAVAILABLE', 'Chromium reported completion but the download file is unavailable'); }
  }
  list() {
    return { downloads: [...this.items.values()].map(({ started, ...item }) => ({ ...item })), max_file_bytes: this.maxBytes, max_total_bytes: this.maxTotal,
      ...(this.rejected ? { rejected_count: this.rejected, last_rejection: this.lastRejection } : {}) };
  }
  async content(id) {
    if (!GUID.test(id)) throw failure(400, 'invalid download id', 'DOWNLOAD_INVALID_ID');
    const item = this.items.get(id);
    if (!item) throw failure(404, 'download not found in this browser session', 'DOWNLOAD_NOT_FOUND');
    if (item.state !== 'completed') throw failure(409, item.error || 'download has not completed', 'DOWNLOAD_NOT_READY');
    let handle;
    try {
      handle = await open(join(this.directory, id), constants.O_RDONLY | constants.O_NOFOLLOW);
      const info = await handle.stat();
      if (!info.isFile() || info.size !== item.size || info.size > this.maxBytes) throw new Error('download changed');
      return { stream: handle.createReadStream(), size: info.size };
    } catch { await handle?.close().catch(() => {}); throw failure(410, 'download bytes are no longer available', 'DOWNLOAD_FILE_UNAVAILABLE'); }
  }
  async acknowledge(id) {
    if (!GUID.test(id)) throw failure(400, 'invalid download id', 'DOWNLOAD_INVALID_ID');
    const item = this.items.get(id); if (!item) return;
    this.items.delete(id);
    if (item.state === 'in_progress') { item.state = 'failed'; await this.cancel(id); }
    await this.removeBytes(id);
  }
  async sweep() {
    if (this.sweeping || this.closed) return; this.sweeping = true;
    try {
      for (const item of this.items.values()) {
        const age = this.now() - item.started;
        if (item.state === 'in_progress') {
          if (age > MAX_ACTIVE) { await this.fail(item, 'DOWNLOAD_TIMEOUT', 'Download did not complete within five minutes'); continue; }
          // Unknown content lengths still have a bounded on-disk lifetime and
          // size. CDP progress also applies these limits during streaming.
          try { const info = await stat(join(this.directory, `${item.id}.crdownload`)); item.received_bytes = Math.max(item.received_bytes, safeBytes(info.size)); } catch {}
          if (item.state === 'in_progress' && this.items.get(item.id) === item) await this.enforceQuota(item);
        } else if (item.state === 'completed' && age > MAX_AGE) {
          item.state = 'failed'; item.error_code = 'DOWNLOAD_EXPIRED'; item.error = 'Download expired before it was imported into the workspace'; await this.removeBytes(item.id);
        }
      }
    } finally { this.sweeping = false; }
  }
  async close() {
    if (this.closed) return; this.closed = true; clearInterval(this.timer);
    this.client.off('Browser.downloadWillBegin', this.beginListener); this.client.off('Browser.downloadProgress', this.progressListener);
    await Promise.allSettled([...this.items.values()].filter(item => item.state === 'in_progress').map(item => this.cancel(item.id)));
    await Promise.allSettled([...this.tasks]); await rm(this.directory, { recursive: true, force: true }); this.items.clear(); this.begun.clear();
  }
}
