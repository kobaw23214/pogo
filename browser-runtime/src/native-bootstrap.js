import WebSocket from 'ws';

const BOOTSTRAP_MS = 15000, ATTEMPT_MS = 5000, CLOSE_MS = 1000;
const fail = code => Object.assign(new Error(code), { code });
const retryable = error => ['NATIVE_BOOTSTRAP_TIMEOUT', 'NATIVE_BOOTSTRAP_TRANSPORT_CLOSED'].includes(error?.code);
const COMPONENT_WORKER_URLS = new Set([
  'chrome-extension://nkeimhogjdpnpccoofpliimaahmaaome/thunk.js',
  'chrome-extension://glbjnfimcajjenihimblfaponejbkoph/background.js',
]);

function bootstrapAuxiliaryTarget(target) {
  if (target?.type === 'browser' && !target.url) return true;
  // These exact Chrome component workers can start during CDP attachment even
  // with --disable-extensions. No site worker or user extension is admitted.
  if (target?.type === 'service_worker' && COMPONENT_WORKER_URLS.has(target.url)) return true;
  if (target?.type !== 'browser_ui' || typeof target.url !== 'string' || !target.url.startsWith('chrome://')) return false;
  try { const url = new URL(target.url); return url.protocol === 'chrome:' && /^[a-z0-9.-]+$/.test(url.hostname) && !url.username && !url.password && !url.port; }
  catch { return false; }
}

export function initialBootstrapTarget(targets, initialURL, expectedTarget, expectedContext) {
  if (!Array.isArray(targets) || targets.length < 1 || targets.length > 8) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
  const pages = targets.filter(target => target.type === 'page');
  if (pages.length !== 1 || targets.some(target => target.type !== 'page' && !bootstrapAuxiliaryTarget(target))) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
  const page = pages[0];
  if (typeof page.targetId !== 'string' || !page.targetId || page.targetId.length > 256 || ![initialURL, 'about:blank', ''].includes(page.url) || expectedTarget && page.targetId !== expectedTarget) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
  if (expectedContext !== undefined && page.browserContextId !== expectedContext || targets.some(target => target.type === 'service_worker' && (typeof page.browserContextId !== 'string' || !page.browserContextId || target.browserContextId !== page.browserContextId))) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
  return page.targetId;
}

async function bounded(work, end, isLive, transport) {
  if (!isLive()) throw fail('NATIVE_BOOTSTRAP_CANCELLED');
  if (Date.now() >= end) throw fail('NATIVE_BOOTSTRAP_TIMEOUT');
  let timer, watch;
  try {
    return await Promise.race([work, ...(transport ? [transport.failure] : []), new Promise((_, reject) => {
      timer = setTimeout(() => reject(fail('NATIVE_BOOTSTRAP_TIMEOUT')), Math.max(1, end - Date.now()));
      watch = setInterval(() => { if (!isLive()) reject(fail('NATIVE_BOOTSTRAP_CANCELLED')); }, 25);
    })]);
  } finally { clearTimeout(timer); clearInterval(watch); }
}

// Public Playwright ConnectOverCDPTransport. Only this owned socket is closed;
// disconnect never kills/relaunches Chrome or navigates a Page.
export class BootstrapTransport {
  constructor(endpoint, diagnostics, attempt) {
    this.socket = new WebSocket(endpoint, [], { maxPayload: 256 * 1024 * 1024, perMessageDeflate: false });
    this.started = Date.now(); this.diagnostics = diagnostics; this.attempt = attempt;
    this.pending = new Map(); this.nextRead = -1; this.inbox = []; this.inboxBytes = 0;
    this.closed = false; this.closing = false; this.opened = false; this.closeDelivered = false; this.dispatches = new Set();
    this.failure = new Promise((_, reject) => { this.rejectFailure = reject; }); this.failure.catch(() => {});
    this.closedPromise = new Promise(resolve => { this.resolveClosed = resolve; });
    this.openPromise = new Promise((resolve, reject) => { this.resolveOpen = resolve; this.rejectOpen = reject; }); this.openPromise.catch(() => {});
    this.socket.on('open', () => { this.opened = true; this.resolveOpen(); });
    this.socket.on('message', data => {
      if (this.closed || this.closing) return;
      let message;
      try { message = JSON.parse(String(data)); }
      catch { this.rejectFailure(fail('NATIVE_BOOTSTRAP_PROTOCOL_INVALID')); this.closing = true; this.socket.terminate(); return; }
      const read = this.pending.get(message.id);
      if (read) {
        this.pending.delete(message.id); this.trace(read.method, message.id, 'received');
        message.error ? read.reject(fail('NATIVE_BOOTSTRAP_PROTOCOL_INVALID')) : read.resolve(message.result); return;
      }
      this.trace(message.method, message.id, 'received');
      if (this._onmessage) this.scheduleMessage(message);
      else {
        this.inboxBytes += data.length;
        if (this.inbox.length >= 256 || this.inboxBytes > 1024 * 1024) { this.rejectFailure(fail('NATIVE_BOOTSTRAP_PROTOCOL_INVALID')); this.closing = true; this.socket.terminate(); return; }
        this.inbox.push(message);
      }
    });
    this.socket.on('error', () => { const error = fail('NATIVE_BOOTSTRAP_TRANSPORT_CLOSED'); this.rejectOpen(error); this.rejectFailure(error); });
    this.socket.on('close', () => {
      this.closed = true; this.resolveClosed();
      for (const dispatch of this.dispatches) clearImmediate(dispatch); this.dispatches.clear();
      this.inbox.length = 0; this.inboxBytes = 0;
      const error = fail('NATIVE_BOOTSTRAP_TRANSPORT_CLOSED'); this.rejectOpen(error); this.rejectFailure(error);
      for (const read of this.pending.values()) read.reject(error); this.pending.clear(); this.deliverClose();
    });
  }
  trace(method, id, event) {
    if (this.diagnostics.length >= 96) return;
    const entry = { attempt: this.attempt, event, elapsed_ms: Date.now() - this.started };
    if (typeof method === 'string' && /^[A-Za-z]+\.[A-Za-z]+$/.test(method)) entry.method = method;
    if (Number.isSafeInteger(id)) entry.id = id;
    this.diagnostics.push(entry); // Never params, endpoint, URL, token or payload.
  }
  set onmessage(callback) {
    this._onmessage = callback;
    if (callback) { const messages = this.inbox.splice(0); this.inboxBytes = 0; for (const message of messages) this.scheduleMessage(message); }
  }
  scheduleMessage(message) {
    if (this.closed || this.closing) return;
    // Match Playwright's standard WS transport: each message gets its own
    // immediate, allowing lifecycle Promise microtasks between coalesced CDP
    // messages. A synchronous batch can strand an execution-context waiter.
    const dispatch = setImmediate(() => {
      this.dispatches.delete(dispatch);
      if (this.closed || this.closing || !this._onmessage) return;
      try { this._onmessage.call(null, message); }
      catch { this.rejectFailure(fail('NATIVE_BOOTSTRAP_PROTOCOL_INVALID')); this.closing = true; this.socket.terminate(); }
    });
    this.dispatches.add(dispatch);
  }
  get onmessage() { return this._onmessage; }
  set onclose(callback) { this._onclose = callback; this.deliverClose(); }
  get onclose() { return this._onclose; }
  deliverClose() { if (this.closed && this._onclose && !this.closeDelivered) { this.closeDelivered = true; try { this._onclose(); } catch { this.rejectFailure(fail('NATIVE_BOOTSTRAP_PROTOCOL_INVALID')); } } }
  send(message) {
    if (this.closed || this.closing || this.socket.readyState !== WebSocket.OPEN) throw fail('NATIVE_BOOTSTRAP_TRANSPORT_CLOSED');
    this.trace(message.method, message.id, 'sent'); this.socket.send(JSON.stringify(message));
  }
  read(method) {
    const id = this.nextRead--;
    const result = new Promise((resolve, reject) => { this.pending.set(id, { method, resolve, reject }); });
    try { this.send({ id, method, params: {} }); }
    catch (error) { const read = this.pending.get(id); this.pending.delete(id); read.reject(error); }
    return result;
  }
  close() { this.closing = true; this.socket.close(); }
  async closeConfirmed(end = Date.now() + CLOSE_MS) {
    if (this.closed) return;
    this.closing = true;
    this.socket.terminate();
    let timer;
    try { await Promise.race([this.closedPromise, new Promise((_, reject) => { timer = setTimeout(() => reject(fail('NATIVE_BOOTSTRAP_CLOSE_UNKNOWN')), Math.max(1, end - Date.now())); })]); }
    finally { clearTimeout(timer); }
    if (!this.closed) throw fail('NATIVE_BOOTSTRAP_CLOSE_UNKNOWN');
  }
}

export async function connectNativeBootstrap({ wsEndpoint, initialURL, isLive, connectOverCDP, diagnostics = [], deadlineMs = BOOTSTRAP_MS }) {
  const endpoint = new URL(wsEndpoint);
  if (endpoint.protocol !== 'ws:' || endpoint.hostname !== '127.0.0.1' || !endpoint.port || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || !/^\/devtools\/browser\/[a-f0-9-]+$/i.test(endpoint.pathname)) throw fail('NATIVE_BOOTSTRAP_ENDPOINT_INVALID');
  if (typeof initialURL !== 'string' || !/^data:text\/html;charset=utf-8,%3Ctitle%3Epogo-private-[a-f0-9]{32}%3C%2Ftitle%3E$/.test(initialURL)) throw fail('NATIVE_BOOTSTRAP_INITIAL_PAGE_INVALID');
  if (typeof isLive !== 'function' || typeof connectOverCDP !== 'function' || !Number.isFinite(deadlineMs) || deadlineMs <= 0 || deadlineMs > BOOTSTRAP_MS) throw fail('NATIVE_BOOTSTRAP_CONFIG_INVALID');
  const end = Date.now() + deadlineMs; let expectedTarget, expectedContext, last;
  for (let attempt = 1; attempt <= 2; attempt++) {
    if (!isLive()) throw fail('NATIVE_BOOTSTRAP_CANCELLED');
    if (Date.now() >= end) throw fail('NATIVE_BOOTSTRAP_TIMEOUT');
    const transport = new BootstrapTransport(wsEndpoint, diagnostics, attempt), attemptEnd = Math.min(end, Date.now() + ATTEMPT_MS);
    try {
      await bounded(transport.openPromise, Math.min(attemptEnd, Date.now() + 1000), isLive, transport);
      const preflightEnd = Math.min(attemptEnd, Date.now() + 1000); let ready = false;
      while (!ready) {
        const targets = await bounded(transport.read('Target.getTargets'), preflightEnd, isLive, transport);
        if (Array.isArray(targets?.targetInfos) && targets.targetInfos.length <= 8 && targets.targetInfos.every(bootstrapAuxiliaryTarget)) {
          await bounded(new Promise(resolve => setTimeout(resolve, 25)), preflightEnd, isLive, transport); continue;
        }
        expectedTarget = initialBootstrapTarget(targets?.targetInfos, initialURL, expectedTarget, expectedContext);
        expectedContext = targets.targetInfos.find(target => target.type === 'page').browserContextId; ready = true;
      }
      const connected = Promise.resolve().then(() => connectOverCDP(transport, { timeout: Math.max(1, attemptEnd - Date.now()) }));
      // A late SDK result has no authority: this attempt's owned transport is
      // closed below. Never invoke a late Browser.close() against the Chrome
      // shared by a later attachment, and never publish that obsolete handle.
      connected.catch(() => {});
      const browser = await bounded(connected, attemptEnd, isLive, transport);
      if (!isLive()) throw fail('NATIVE_BOOTSTRAP_CANCELLED');
      const contexts = browser.contexts();
      if (contexts.length !== 1) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
      const context = contexts[0]; let keeper;
      while (!keeper) {
        const pages = context.pages().filter(page => !page.isClosed());
        if (!pages.length) { await bounded(new Promise(resolve => setTimeout(resolve, 25)), attemptEnd, isLive, transport); continue; }
        if (pages.length !== 1 || ![initialURL, 'about:blank', ''].includes(pages[0]?.url())) throw fail('NATIVE_BOOTSTRAP_TARGETS_CHANGED');
        if (pages[0].url() === initialURL) keeper = pages[0];
        else await bounded(new Promise(resolve => setTimeout(resolve, 25)), attemptEnd, isLive, transport);
      }
      const browserCDP = await bounded(browser.newBrowserCDPSession(), attemptEnd, isLive, transport);
      const finalTargets = await bounded(browserCDP.send('Target.getTargets'), attemptEnd, isLive, transport);
      initialBootstrapTarget(finalTargets?.targetInfos, initialURL, expectedTarget, expectedContext);
      if (!isLive()) throw fail('NATIVE_BOOTSTRAP_CANCELLED');
      return { browser, context, keeper, browserCDP, transport, diagnostics };
    } catch (cause) {
      last = cause?.code ? cause : fail(cause?.name === 'TimeoutError' ? 'NATIVE_BOOTSTRAP_TIMEOUT' : 'NATIVE_BOOTSTRAP_CONNECT_FAILED');
      await transport.closeConfirmed(Math.min(end, Date.now() + CLOSE_MS));
      if (!isLive()) throw fail('NATIVE_BOOTSTRAP_CANCELLED');
      if (!retryable(last) || attempt === 2 || Date.now() >= end) throw last;
    }
  }
  throw last;
}
