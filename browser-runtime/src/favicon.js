import { randomBytes } from 'node:crypto';

export const FAVICON_BYTES = 64 * 1024;
const MIME = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/x-icon', 'image/vnd.microsoft.icon', 'image/svg+xml']);

function image(bytes, mime) {
  mime = String(mime ?? '').split(';')[0].trim().toLowerCase();
  if (!MIME.has(mime) || !bytes?.length || bytes.length > FAVICON_BYTES) return '';
  // SVG is rendered only as an image. Reject active/external content as well so
  // the dock never creates another document or network surface for an icon.
  if (mime === 'image/svg+xml') {
    const text = bytes.toString('utf8');
    if (!/<svg\b/i.test(text) || /<\s*(?:script|foreignObject|iframe|!DOCTYPE)\b|\bon\w+\s*=|(?:href|src)\s*=\s*["']\s*(?!#)|url\(\s*["']?\s*(?!#)/i.test(text)) return '';
  }
  return `data:${mime};base64,${bytes.toString('base64')}`;
}

export function safeFaviconSource(value, base) {
  if (typeof value !== 'string' || !value || value.length > Math.ceil(FAVICON_BYTES * 4 / 3) + 128) return '';
  if (value.startsWith('data:')) {
    const match = /^data:(image\/[a-z.+-]+)(;base64)?,([\s\S]*)$/i.exec(value);
    if (!match || !MIME.has(match[1].toLowerCase())) return '';
    try {
      const bytes = match[2] ? /^[A-Za-z0-9+/]*={0,2}$/.test(match[3]) && Buffer.from(match[3], 'base64') : Buffer.from(decodeURIComponent(match[3]), 'utf8');
      return bytes && image(bytes, match[1]);
    } catch { return ''; }
  }
  if (value.length > 4096) return '';
  try {
    const url = new URL(value, base);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return '';
    url.hash = '';
    return url.href;
  } catch { return ''; }
}

function installFaviconProbe({ key }) {
  if (globalThis[key + '_installed']) return;
  let timer;
  const publish = () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      const icons = [];
      const links = document.head?.getElementsByTagName('link') ?? [];
      for (let i = 0; i < Math.min(links.length, 64); i++) {
        const link = links[i];
        if (link.rel.toLowerCase().split(/\s+/).includes('icon') && link.href.length <= 90000) icons.push(link.href);
      }
      // This binding publishes bounded display metadata, never page content.
      Promise.resolve(globalThis[key]?.({ url: document.URL.slice(0, 4096), icons: icons.slice(-16) })).catch(() => {});
    }, 80);
  };
  Object.defineProperty(globalThis, key + '_installed', { value: publish });
  const start = () => {
    if (document.head) new MutationObserver(records => {
      if (records.some(record => record.type === 'attributes' || [...record.addedNodes, ...record.removedNodes].some(node => node.nodeType === 1 && (node.tagName === 'LINK' || node.tagName === 'BASE' || node.querySelector?.('link,base'))))) publish();
    }).observe(document.head, { subtree: true, childList: true, attributes: true, attributeFilter: ['href', 'rel'] });
    publish();
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
  else start();
}

/** Page metadata is observed when it changes. Favicon bytes come from this same
 * Chromium context and its cache; they never become a frontend URL fetch. */
export class BrowserFavicons {
  constructor(session) { this.session = session; this.cache = new Map(); this.pages = new Map(); }
  async attach(meta) {
    const page = meta.page;
    if (!page.exposeBinding || !page.addInitScript) return;
    const key = `__pogoFavicon_${randomBytes(12).toString('hex')}`;
    const record = { meta, key, sequence: 0, source: '', client: null, closed: false, pending: null, lastPublished: 0 };
    this.pages.set(meta, record);
    try {
      await page.exposeBinding(key, ({ frame }, payload) => {
        if (frame !== page.mainFrame() || payload?.url !== page.url() || !Array.isArray(payload.icons) || payload.icons.length > 16) return;
        // A hostile page cannot cause unbounded network reads through the
        // private metadata binding. A final change is handled after the bound.
        record.pending = payload;
        this.schedule(record);
      });
      await page.addInitScript(installFaviconProbe, { key });
      // Already-loaded popups need the same observer. Installing it is a
      // passive read and must not hold page creation at a debugger breakpoint.
      page.evaluate(installFaviconProbe, { key }).catch(() => {});
    } catch { this.detach(meta); }
  }
  schedule(record) {
    if (this.session.closed || record.closed || record.timer || record.loading) return;
    const wait = Math.max(0, record.lastPublished + 500 - Date.now());
    record.timer = setTimeout(() => {
      record.timer = null;
      const payload = record.pending; record.pending = null;
      if (payload) this.update(record, payload).catch(() => {});
    }, wait);
    record.timer.unref?.();
  }
  navigated(meta) {
    const record = this.pages.get(meta);
    if (record) { record.sequence++; record.source = ''; record.pending = null; clearTimeout(record.timer); record.timer = null; }
    meta.favicon = '';
    // Same-document routes keep the existing head observer. Republish its
    // metadata once for the new URL; fresh documents install their own probe.
    if (record && !record.closed) meta.page.evaluate(key => {
      const publish = globalThis[key + '_installed'];
      if (typeof publish === 'function') publish();
    }, record.key).catch(() => {});
  }
  async update(record, payload) {
    const { meta } = record, page = meta.page;
    if (record.closed || payload.url !== page.url() || !/^https?:/.test(page.url())) return;
    const candidates = [...new Set(payload.icons.map(value => safeFaviconSource(value, payload.url)).filter(Boolean))].reverse();
    const fallback = safeFaviconSource('/favicon.ico', payload.url);
    if (!candidates.length && fallback) candidates.push(fallback);
    const source = candidates.join('\n');
    if (record.source === source) return;
    record.source = source; record.lastPublished = Date.now(); record.loading = true;
    const sequence = ++record.sequence, document = meta.topDocumentVersion;
    const current = () => !this.session.closed && !record.closed && this.session.pages.get(meta.id) === meta && sequence === record.sequence && document === meta.topDocumentVersion && payload.url === page.url();
    meta.favicon = '';
    try {
      for (const candidate of candidates.slice(0, 4)) {
        const icon = candidate.startsWith('data:') ? candidate : await this.read(record, candidate);
        if (!current()) return;
        if (icon) { meta.favicon = icon; break; }
      }
    } finally { record.loading = false; this.schedule(record); }
  }
  async read(record, url) {
    const cached = this.cache.get(url);
    if (cached?.expires > Date.now()) return cached.icon;
    if (cached) this.cache.delete(url);
    let client, handle, timer, expires = 0;
    try {
      client = record.client ??= await this.session.context.newCDPSession(record.meta.page);
      const loading = (async () => {
        const { frameTree } = await client.send('Page.getFrameTree');
        const { resource } = await client.send('Network.loadNetworkResource', { frameId: frameTree.frame.id, url, options: { disableCache: false, includeCredentials: true } });
        handle = resource.stream;
        const headers = Object.fromEntries(Object.entries(resource.headers ?? {}).map(([key, value]) => [key.toLowerCase(), value]));
        const mime = String(headers['content-type'] ?? '').split(';')[0].trim().toLowerCase();
        if (!resource.success || resource.httpStatusCode < 200 || resource.httpStatusCode >= 300 || !handle || !MIME.has(mime) || Number(headers['content-length']) > FAVICON_BYTES) return '';
        const policy = String(headers['cache-control'] ?? '').toLowerCase();
        if (!/(?:^|,)\s*(?:no-store|no-cache)\b/.test(policy) && !String(headers.vary ?? '').trim()) {
          const maxAge = /(?:^|,)\s*max-age\s*=\s*"?(\d+)/.exec(policy);
          const remaining = maxAge ? Math.max(0, Number(maxAge[1]) - (Number(headers.age) || 0)) * 1000 : 60000;
          expires = Date.now() + Math.min(300000, remaining);
        }
        const chunks = []; let total = 0;
        for (;;) {
          const part = await client.send('IO.read', { handle, size: Math.min(16384, FAVICON_BYTES + 1 - total) });
          const bytes = Buffer.from(part.data, part.base64Encoded ? 'base64' : 'utf8');
          total += bytes.length;
          if (total > FAVICON_BYTES) return '';
          chunks.push(bytes);
          if (part.eof) return image(Buffer.concat(chunks, total), mime);
        }
      })();
      const result = await Promise.race([loading, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('favicon read timed out')), 2000); })]);
      // A later navigation after login may make a previously unavailable icon
      // readable. Only successful bytes are reused across documents.
      if (result && expires > Date.now()) this.cache.set(url, { icon: result, expires });
      if (this.cache.size > 16) this.cache.delete(this.cache.keys().next().value);
      return result;
    } catch {
      if (record.client === client) record.client = null;
      await client?.detach().catch(() => {});
      return '';
    } finally { clearTimeout(timer); if (handle) await client?.send('IO.close', { handle }).catch(() => {}); }
  }
  detach(meta) {
    const record = this.pages.get(meta);
    if (!record) return;
    record.closed = true; record.sequence++; clearTimeout(record.timer);
    record.client?.detach().catch(() => {}); this.pages.delete(meta);
  }
  close() { for (const meta of this.pages.keys()) this.detach(meta); this.cache.clear(); }
}
