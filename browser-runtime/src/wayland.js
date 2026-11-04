import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, access } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { LineRPC, connectRPC } from './wayland-rpc.js';
import { HTTPError } from './session.js';
import { MEDIA_DEVICE_SCALE_FACTORS } from './media-config.js';
import { connectNativeBootstrap } from './native-bootstrap.js';
import { BrowserDownloads } from './downloads.js';
import { isNavigationContextError, navigationObservationError } from './navigation-recovery.js';

const runtimeRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const deadline = async (predicate, ms = 15000, isLive = () => true) => {
  const end = Date.now() + ms;
  const timeout = () => new Error('native browser resource did not become ready');
  const canceled = () => new Error('native browser resource wait was canceled');
  let timer, watch;
  const failure = new Promise((_, reject) => {
    timer = setTimeout(() => reject(timeout()), ms);
    watch = setInterval(() => { if (!isLive()) reject(canceled()); }, 25);
  });
  try {
    while (Date.now() < end) {
      if (!isLive()) throw canceled();
      const value = await Promise.race([Promise.resolve().then(predicate), failure]);
      if (!isLive()) throw canceled();
      if (Date.now() >= end) throw timeout();
      if (value) return value;
      await Promise.race([pause(25), failure]);
    }
    throw timeout();
  } finally { clearTimeout(timer); clearInterval(watch); }
};
// The proxy variables are pass-through, not added: a deployment whose egress
// runs through a corporate proxy (see w_deploy compose browser env) needs
// Chromium to honor them, while one without them simply sees none. Everything
// else stays whitelisted so the browser child never inherits runtime secrets.
const browserEnv = () => Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'http_proxy', 'https_proxy', 'no_proxy'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
const id = () => randomBytes(16).toString('hex');
const stop = async child => {
  if (!child) return;
  // A detached Chrome group owns its renderer/zygote/GPU children too. Killing
  // only the launcher can leave descendants after a failed CDP handshake.
  if (child.pogoProcessGroup) { try { process.kill(-child.pid, 'SIGTERM'); } catch {} }
  if (child.exitCode !== null) return;
  const exited = new Promise(resolve => child.once('exit', resolve));
  child.kill('SIGTERM');
  await Promise.race([exited, pause(2000)]);
  if (child.exitCode === null) { if (child.pogoProcessGroup) { try { process.kill(-child.pid, 'SIGKILL'); } catch {} } else child.kill('SIGKILL'); await Promise.race([exited, pause(1000)]); }
};
export function waylandViewport(value) {
  if (!value || ![value.width, value.height].every(dimension => typeof dimension === 'number' && Number.isFinite(dimension))) throw new HTTPError(400, 'native viewport dimensions must be finite numbers');
  return { width: Math.max(320, Math.min(2560, Math.round(value.width / 8) * 8)), height: Math.max(200, Math.min(1600, Math.round(value.height / 8) * 8)) };
}

export function waylandCaptureScale(viewport, preferred = 1.5, maximum = 2) {
  if (typeof preferred !== 'number' || !Number.isFinite(preferred) || preferred < 1 || preferred > 2) throw new HTTPError(400, 'preferred_scale must be a finite number from 1 to 2');
  if (!MEDIA_DEVICE_SCALE_FACTORS.includes(maximum)) throw new Error('invalid maximum native output scale');
  const { width, height } = waylandViewport(viewport);
  const feasible = MEDIA_DEVICE_SCALE_FACTORS.filter(scale => {
    const w = width * scale, h = height * scale;
    return scale <= maximum && Number.isInteger(w) && Number.isInteger(h) && w % 2 === 0 && h % 2 === 0 && w <= 3840 && h <= 2400 && w * h <= 7464960;
  });
  return feasible.find(scale => scale >= preferred) ?? feasible.at(-1);
}

// One compositor and one real Chromium profile per browser-use session. Sheets
// are separate native app windows on private outputs, never a screenshot loop
// or a hidden alternate browser. Only the worker owns process/IPC capabilities.
export class WaylandRuntime {
  constructor(config, { python = process.env.POGO_WAYLAND_PYTHON || '/opt/wl/bin/python3', stream = process.env.POGO_WAYLAND_STREAM || '/opt/pogo/bin/pogo-wlstream', scale = 1.5, sandbox = true, onFailure } = {}) {
    this.maxScale = config.deviceScaleFactor ?? 2;
    this.config = config; this.python = python; this.stream = stream; this.scale = waylandCaptureScale({ width: 1280, height: 800 }, scale, this.maxScale); this.sandbox = sandbox;
    this.onFailure = onFailure;
    this.hosts = new Map(); this.closed = false; this.factoryQueue = Promise.resolve();
  }
  async start() {
    this.dir = await mkdtemp('/tmp/pogo-wl-');
    this.streamDir = join(this.dir, 'stream'); await mkdir(this.streamDir, { mode: 0o700 });
    const token = id() + id();
    this.child = spawn(this.stream, ['--dir', this.streamDir, '--udp-range', this.config.udpPortRange, '--network', this.config.network, '--playout-delay', this.config.playoutDelay, '--max-peers', '32'], { env: { ...browserEnv(), POGO_WLSTREAM_TOKEN: token, POGO_WLSTREAM_ICE_SERVERS: '[]' }, stdio: ['ignore', 'pipe', 'pipe'] });
    this.child.stderr.on('data', () => {}); // No candidate, address or capability logging.
    this.child.on('error', () => { this.failed = true; });
    this.child.on('exit', () => { if (!this.closed) this.fail(); });
    try {
      await deadline(async () => { if (this.failed) throw new Error('Wayland media service failed to start'); try { await access(join(this.streamDir, 'ctl.sock')); return true; } catch { return false; } });
      this.rpc = await connectRPC(join(this.streamDir, 'ctl.sock'), { type: 'hello', version: 1, token });
      this.rpc.on('failed', () => { if (!this.closed) this.fail(); });
      this.rpc.on('uncertain', () => this.fail());
      return this;
    } catch (error) { await this.close(); throw error; }
  }
  connected = () => !this.closed && !this.failed && this.child?.exitCode === null && !this.rpc?.closed;
  fail() { if (this.closed || this.failed) return; this.failed = true; for (const host of this.hosts.values()) host.fail(); this.onFailure?.(); }
  async checkCapacity() {
    // Configuration is an upper bound, not permission to overrun the worker's
    // hard cgroup limits. Refuse a new process near exhaustion; retain the
    // existing browser sessions and never launch a fallback implementation.
    for (const [name, threshold] of [['pids', 0.8], ['memory', 0.9]]) {
      try {
        const [current, maximum] = await Promise.all(['current', 'max'].map(suffix => readFile(`/sys/fs/cgroup/${name}.${suffix}`, 'utf8')));
        const used = Number(current.trim()), limit = Number(maximum.trim());
        if (Number.isFinite(used) && Number.isFinite(limit) && limit > 0 && used >= limit * threshold) throw new HTTPError(429, 'browser worker is at resource capacity; close idle browser sessions before opening another', 'BROWSER_RESOURCE_CAPACITY');
      } catch (error) { if (error.status === 429) throw error; /* Older unbounded hosts may not expose cgroup v2. */ }
    }
  }
  factory = ({ id: sessionId }) => {
    const job = this.factoryQueue = this.factoryQueue.catch(() => {}).then(async () => {
      if (!this.connected()) throw new Error('Wayland runtime is unavailable');
      if (this.hosts.has(sessionId)) throw new Error('native browser session already exists');
      await this.checkCapacity();
      if (!this.connected()) throw new Error('Wayland runtime closed during admission');
      const host = new WaylandSessionHost(this, sessionId); this.hosts.set(sessionId, host);
      try { await host.start(); if (!this.connected()) throw new Error('native runtime closed during session startup'); return { context: host.context, host }; }
      catch (error) { await host.close(); throw error; }
    }); return job;
  };
  close() {
    if (this.closePromise) return this.closePromise; this.closed = true;
    this.closePromise = (async () => {
      // A started factory owns partially constructed hosts. Let its bounded
      // startup finish or reject before confirming destruction of its resources.
      await this.factoryQueue.catch(() => {});
      await Promise.allSettled([...this.hosts.values()].map(host => host.close()));
      this.rpc?.close(); await stop(this.child);
      if (this.dir) await rm(this.dir, { recursive: true, force: true });
    })(); return this.closePromise;
  }
}

export class WaylandSessionHost {
  constructor(runtime, sessionId) {
    this.runtime = runtime; this.id = sessionId; this.records = new Map(); this.pending = new WeakMap();
    this.issuedPrivateURLs = new Map();
    this.chromeChildren = new Set();
    this.nativeQueue = Promise.resolve(); this.inputQueue = Promise.resolve(); this.seatKeys = new Set(); this.seatButtons = new Set();
    this.nextEpoch = 0; this.closed = false; this.displayMode = 'wayland';
    this.chromeArgs = [
      '--ozone-platform=wayland', '--enable-features=WaylandFractionalScaleV1,WaylandPerSurfaceScale',
      '--disable-background-timer-throttling', '--disable-backgrounding-occluded-windows', '--disable-renderer-backgrounding',
      '--disable-gpu-compositing', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--num-raster-threads=2',
      '--disable-extensions', '--disable-background-networking', '--disable-component-update', '--disable-sync',
      '--no-first-run', '--no-default-browser-check', '--disable-search-engine-choice-screen', '--hide-crash-restore-bubble', '--disable-infobars',
      '--password-store=basic', '--use-mock-keychain', '--disable-popup-blocking', '--disable-prompt-on-repost',
      '--force-color-profile=srgb', '--mute-audio', '--site-per-process',
      '--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4',
      ...(!runtime.sandbox ? ['--no-sandbox'] : []),
    ];
  }
  async start() {
    this.dir = join(this.runtime.dir, id()); await mkdir(this.dir, { mode: 0o700 });
    this.sessionCap = id() + id();
    await this.runtime.rpc.request('register_session', { session: this.id, token: this.sessionCap });
    this.registered = true;
    if (this.closed || this.failed || this.runtime.closed) throw new Error('native session closed during registration');
    this.child = spawn(this.runtime.python, ['-u', join(runtimeRoot, 'wayland/host.py'), '--session', this.id, '--runtime-dir', this.dir, '--frames-socket', join(this.runtime.streamDir, 'frames.sock')], { env: { ...browserEnv(), POGO_WAYLAND_SESSION_CAP: this.sessionCap, PIXELFLUX_MANUAL_PLACEMENT: '1', WLR_RENDERER: 'pixman' }, stdio: ['pipe', 'pipe', 'pipe'] });
    this.child.stderr.on('data', () => {});
    this.child.on('error', () => this.fail()); this.child.on('exit', () => { if (!this.closed) this.fail(); });
    this.rpc = new LineRPC(this.child.stdout, this.child.stdin, { timeout: 15000 });
    this.rpc.on('failed', () => { if (!this.closed) this.fail(); });
    this.rpc.on('uncertain', () => this.fail());
    const init = await this.rpc.request('init', { scale: this.runtime.scale, network: this.runtime.config.network });
    if (this.closed || this.failed || this.runtime.closed) throw new Error('native session closed during compositor startup');
    if (typeof init?.wayland_display !== 'string' || init.xdg_runtime_dir !== this.dir || init.scale !== this.runtime.scale || !init.parking) throw new Error('invalid native compositor initialization');
    this.env = { ...browserEnv(), HOME: this.dir, XDG_RUNTIME_DIR: this.dir, WAYLAND_DISPLAY: init.wayland_display };
    this.profile = join(this.dir, 'profile'); await mkdir(this.profile, { mode: 0o700 });
    this.initialURL = this.internalURL();
    this.chrome = this.launchChrome(this.initialURL, true);
    this.chrome.on('exit', () => { if (!this.closed) this.fail(); });
    const endpoint = await deadline(async () => {
      if (this.closed || this.failed) throw new Error('native Chromium did not start');
      try {
        const lines = (await readFile(join(this.profile, 'DevToolsActivePort'), 'utf8')).trim().split('\n');
        const port = Number(lines[0]), path = lines[1];
        return Number.isInteger(port) && port > 0 && port <= 65535 && /^\/devtools\/browser\/[a-f0-9-]+$/i.test(path ?? '') ? `ws://127.0.0.1:${port}${path}` : false;
      } catch { return false; }
    });
    this.bootstrapDiagnostics = [];
    const boot = await connectNativeBootstrap({ wsEndpoint: endpoint, initialURL: this.initialURL,
      isLive: () => !this.closed && !this.failed && !this.runtime.closed && this.chrome.exitCode === null && this.child.exitCode === null && !this.session && this.records.size === 0,
      connectOverCDP: (transport, options) => chromium.connectOverCDP(transport, options), diagnostics: this.bootstrapDiagnostics });
    this.browser = boot.browser; this.context = boot.context; this.keeper = boot.keeper;
    this.browserCDP = boot.browserCDP; this.bootstrapTransport = boot.transport;
    if (this.closed || this.failed || this.runtime.closed) { await this.bootstrapTransport.closeConfirmed(); throw new Error('native session closed during browser attachment'); }
    this.browser.on('disconnected', () => { if (!this.closed) this.fail(); });
    this.downloads = await new BrowserDownloads({ client: this.browserCDP, directory: join(this.dir, 'downloads'),
      ownerForFrame: frameId => this.downloadOwner(frameId),
      onBegin: (event, owner) => {
        const meta = this.session?.pages.get(owner.tab_id);
        if (meta) this.session.confirmDownloadNavigation(meta, { frameId: event.frameId, url: event.url });
      } }).start();
    this.mediaBroker = {
      runtime: 'wayland', observePage: (page, attach) => this.observePage(page, attach),
      create: (page, options) => this.createMedia(page, options), warm: async () => {},
    };
  }
  downloadOwner(frameId) {
    const record = [...this.records.values()].find(record => record.frameIds?.has(frameId));
    const meta = record?.pageId && this.session?.pages.get(record.pageId);
    if (this.closed || this.failed || !meta || meta.page !== record.page || record.page.isClosed() || !record.nativeRegistered) return null;
    return { tab_id: meta.id, context_ref: this.session.contextRef, control: this.session.controlFor(meta).mode };
  }
  privateDocument() {
    const nonce = `pogo-private-${id()}`;
    return { nonce, url: 'data:text/html;charset=utf-8,' + encodeURIComponent(`<title>${nonce}</title>`) };
  }
  internalURL() { return this.privateDocument().url; }
  launchChrome(url, primary = false) {
    const child = spawn(chromium.executablePath(), [...this.chromeArgs, `--user-data-dir=${this.profile}`, ...(primary ? ['--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1'] : []), `--app=${url}`], { env: this.env, detached: true, stdio: ['ignore', 'ignore', 'pipe'] });
    child.pogoProcessGroup = true; this.chromeChildren.add(child);
    child.stderr.on('data', () => {}); child.on('error', () => this.fail());
    if (!primary) child.on('exit', () => this.chromeChildren.delete(child));
    return child;
  }
  bindSession(session) {
    this.session = session; session.mediaBroker = this.mediaBroker; session.mediaConfig = this.runtime.config;
    session.deviceScaleFactor = this.runtime.scale; session.captureDeviceScaleFactor = this.runtime.scale;
    // Chromium can otherwise exit when the user closes their last Sheet. This
    // empty parked window is never captured, owned or exposed as a Sheet.
    session.internalPages.add(this.keeper);
  }
  ownsPage(page) { return !this.closed && page.context() === this.context && this.records.has(page); }
  getViewport(page) { const record = this.records.get(page); return record?.viewport ?? { width: 1280, height: 800 }; }
  getCaptureScale(page) { return this.records.get(page)?.scale ?? this.runtime.scale; }
  getCaptureGeometry(page) {
    const viewport = this.getViewport(page), scale = this.getCaptureScale(page);
    return { width: viewport.width * scale, height: viewport.height * scale, scale };
  }
  chooseCaptureScale(viewport, preferred = 1.5) { return waylandCaptureScale(viewport, preferred, this.runtime.maxScale ?? 2); }
  getTargetCaptureScale(page, value, preferred = 1.5) {
    const viewport = waylandViewport(value), current = this.getViewport(page);
    const selected = this.chooseCaptureScale(viewport, preferred);
    return current.width === viewport.width && current.height === viewport.height ? Math.max(this.getCaptureScale(page), selected) : selected;
  }
  async newPage() {
    const identity = this.privateDocument();
    if (this.closed || this.failed || this.runtime.closed || this.runtime.failed) throw new Error('native session closed while creating a page');
    this.issuedPrivateURLs.set(identity.url, identity);
    try {
      this.launchChrome(identity.url);
      const page = await deadline(() => {
        if (this.closed || this.failed || this.runtime.closed || this.runtime.failed) throw new Error('native session closed while creating a page');
        return this.context.pages().find(page => page.url() === identity.url && !page.isClosed());
      }, 15000, () => !this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed);
      await this.adopt(page);
      if (this.closed || this.failed || this.runtime.closed || this.runtime.failed || page.isClosed()) throw new Error('native session closed while creating a page');
      // The private native-window identity document must never become a Back
      // entry. A fresh Sheet starts with exactly the normal empty history.
      await page.evaluate(() => location.replace('about:blank'));
      await page.waitForURL('about:blank');
      if (this.closed || this.failed || this.runtime.closed || this.runtime.failed || page.isClosed()) throw new Error('native session closed while creating a page');
      return page;
    } finally { this.issuedPrivateURLs.delete(identity.url); }
  }
  observePage(page, attach) {
    return this.adopt(page).then(() => attach(page)).catch(async () => {
      if (!page.isClosed()) { this.fail(); return; }
      const record = this.records.get(page);
      if (record) try { await this.removePage(record); } catch { this.fail(); }
    });
  }
  adopt(page) {
    if (this.pending.has(page)) return this.pending.get(page);
    const record = { page, viewport: { width: 1280, height: 800 }, scale: this.runtime.scale, peers: new Map(), peerClosures: new Set(), gate: Promise.resolve(), creating: 0, captureRevision: 0, nativeBound: false, nativeRegistered: false };
    this.records.set(page, record);
    const flight = this.nativeQueue = this.nativeQueue.catch(() => {}).then(async () => {
      const assertLive = () => {
        if (this.closed || this.failed || this.runtime.closed || this.runtime.failed || page.isClosed()) throw new Error('native page closed while binding');
      };
      assertLive();
      const client = await this.context.newCDPSession(page); record.client = client;
      const { targetInfo } = await client.send('Target.getTargetInfo'); record.target = targetInfo.targetId;
      // Bind download initiators to this registered native Sheet, including
      // child frames. Never fall back to whichever page is currently active.
      record.frameIds = new Set();
      client.on('Page.frameAttached', event => record.frameIds.add(event.frameId));
      client.on('Page.frameNavigated', event => record.frameIds.add(event.frame.id));
      client.on('Page.frameDetached', event => { if (event.reason !== 'swap') record.frameIds.delete(event.frameId); });
      await client.send('Page.enable');
      const remember = tree => { record.frameIds.add(tree.frame.id); for (const child of tree.childFrames || []) remember(child); };
      remember((await client.send('Page.getFrameTree')).frameTree);
      const window = await this.browserCDP.send('Browser.getWindowForTarget', { targetId: record.target }); record.chromeWindow = window.windowId;
      const matches = [...this.records.values()].filter(other => other !== record && other.chromeWindow === record.chromeWindow);
      if (matches.length) throw new Error('multiple Sheets share a native window; refusing unsafe capture');
      const privateIdentity = this.issuedPrivateURLs.get(targetInfo.url);
      let native;
      if (privateIdentity) {
        if (privateIdentity.page && privateIdentity.page !== page) throw new Error('private native page identity was already claimed');
        privateIdentity.page = page;
        native = await deadline(async () => {
          assertLive();
          if (this.issuedPrivateURLs.get(targetInfo.url) !== privateIdentity) throw new Error('private native page identity expired');
          const result = await this.rpc.request('windows');
          assertLive();
          const matches = result.filter(window => window.parked && window.title === privateIdentity.nonce);
          if (matches.length > 1) throw new Error('private native page identity matched multiple windows');
          return matches[0];
        }, 15000, () => !this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed && !page.isClosed());
        const current = (await client.send('Target.getTargetInfo')).targetInfo;
        const currentWindow = await this.browserCDP.send('Browser.getWindowForTarget', { targetId: record.target });
        assertLive();
        if (current.targetId !== record.target || current.url !== targetInfo.url || currentWindow.windowId !== record.chromeWindow) throw new Error('private native page identity changed while binding');
      } else {
        // Popups are matched by a worker-selected unique native window geometry,
        // never by a website's title or compositor event arrival order.
        const nonce = randomBytes(2).readUInt16BE(); const width = 720 + nonce % 256, height = 400 + Math.floor(nonce / 256);
        // Chromium may restore a fullscreen window and ignore dimensions in
        // that same command. Restore first, then apply the geometry challenge.
        await this.browserCDP.send('Browser.setWindowBounds', { windowId: record.chromeWindow, bounds: { windowState: 'normal' } });
        await this.browserCDP.send('Browser.setWindowBounds', { windowId: record.chromeWindow, bounds: { width, height } });
        record.pairing = { requested: { width, height }, chrome: (await this.browserCDP.send('Browser.getWindowBounds', { windowId: record.chromeWindow })).bounds };
        native = await deadline(async () => { const result = await this.rpc.request('windows'); record.pairing.native = result.filter(window => window.parked).map(window => ({ window_id: window.window_id, width: window.outer_width, height: window.outer_height })); const matches = result.filter(window => window.parked && window.outer_width === width && window.outer_height === height); return matches.length === 1 ? matches[0] : false; }, 15000, () => !this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed && !page.isClosed());
        const next = randomBytes(2).readUInt16BE(), nextWidth = 1040 + next % 256, nextHeight = 680 + Math.floor(next / 256);
        await this.browserCDP.send('Browser.setWindowBounds', { windowId: record.chromeWindow, bounds: { width: nextWidth, height: nextHeight } });
        record.pairing = { requested: { width: nextWidth, height: nextHeight }, chrome: (await this.browserCDP.send('Browser.getWindowBounds', { windowId: record.chromeWindow })).bounds };
        const verified = await deadline(async () => { const result = await this.rpc.request('windows'); record.pairing.native = result.filter(window => window.parked).map(window => ({ window_id: window.window_id, width: window.outer_width, height: window.outer_height })); return result.find(window => window.window_id === native.window_id && window.parked && window.outer_width === nextWidth && window.outer_height === nextHeight); }, 15000, () => !this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed && !page.isClosed());
        native = verified;
      }
      assertLive();
      record.nativeWindow = native.window_id;
      const meta = this.session?.attach(page); record.pageId = meta?.id;
      // init() creates the first Page after bindSession. Native popups also
      // inherit their real opener through BrowserSession's existing policy.
      if (!record.pageId) throw new Error('native page has no browser-use binding');
      const result = await this.rpc.request('bind_sheet', { page: record.pageId, window_id: record.nativeWindow, generation: this.session.generation, ...record.viewport, scale: record.scale });
      record.nativeBound = true;
      record.displayId = result.display_id;
      if (!Number.isSafeInteger(record.displayId) || record.displayId < 2) throw new Error('invalid native output binding');
      assertLive();
      await this.runtime.rpc.request('register_page', { session: this.id, page: record.pageId, display_id: record.displayId, generation: this.session.generation });
      record.nativeRegistered = true;
      assertLive();
      await this.setViewport(page, record.viewport);
      page.once('close', () => this.removePage(record).catch(() => this.fail()));
      return record;
    });
    this.pending.set(page, flight); return flight;
  }
  async setViewport(page, value, { observationTimeout = 15000 } = {}) {
    const record = this.records.get(page); if (!record?.pageId) throw new Error('native page is not bound');
    const viewport = waylandViewport(value);
    const scale = this.getTargetCaptureScale(page, viewport, value.preferred_scale);
    const result = await this.rpc.request('resize', { page: record.pageId, ...viewport, scale });
    if (result.width !== viewport.width || result.height !== viewport.height || result.scale !== scale || result.pixel_width !== viewport.width * scale || result.pixel_height !== viewport.height * scale) throw new Error('native compositor resize did not match the Sheet viewport');
    // These dimensions were acknowledged by the compositor. Keep them even if
    // the document changes during confirmation, but withhold a new display's
    // pixel proof until its corresponding DOM layout has also been observed.
    record.viewport = viewport; record.scale = scale; record.proof = null; record.layoutPending = true;
    try { await this.confirmViewport(page, record, observationTimeout); }
    catch (error) {
      if (isNavigationContextError(error)) throw error;
      if (!this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed && !page.isClosed() && this.records.get(page) === record && record.viewport === viewport) this.fail();
      throw error;
    }
    return viewport;
  }
  async confirmViewport(page, record, observationTimeout = 15000) {
    const viewport = record.viewport, scale = record.scale;
    const meta = this.session?.pages.get(record.pageId);
    const documentIdentity = meta ? this.session.frameIdentity(meta) : null;
    // Native output configure drives Chromium layout/DPR, no mobile emulation.
    // Resize is dispatched once. Only its read-only layout confirmation can
    // repeat if navigation replaces the evaluated document in the meantime.
    const current = () => !this.closed && !this.failed && !this.runtime.closed && !this.runtime.failed && !page.isClosed() && this.records.get(page) === record && !record.removing && record.viewport === viewport && record.scale === scale;
    let navigationError;
    try {
      await deadline(async () => {
        try {
          // A transport deadline cannot cancel Chromium's evaluate. Share one
          // in-flight layout read across recovery attempts rather than queue
          // more scripts in a temporarily unresponsive renderer.
          let reading = record.layoutRead;
          if (!reading) {
            reading = Promise.resolve().then(() => page.evaluate(() => ({ width: innerWidth, height: innerHeight, scale: devicePixelRatio })));
            record.layoutRead = reading;
            reading.finally(() => { if (record.layoutRead === reading) record.layoutRead = undefined; }).catch(() => {});
          }
          const actual = await reading;
          navigationError = undefined;
          return actual.width === viewport.width && actual.height === viewport.height && actual.scale === scale;
        } catch (error) {
          if (!isNavigationContextError(error)) throw error;
          navigationError = error;
          return false;
        }
      }, observationTimeout, current);
    } catch (error) {
      // A replaced JavaScript context is not a failed compositor or browser.
      // The caller can prepare a fresh display; it must not replay user input.
      const changing = meta && (meta.pageLoading || meta.documentIdentityReady === false || this.session.frameIdentity(meta) !== documentIdentity);
      const timedOut = error.message === 'native browser resource did not become ready';
      if (current() && (navigationError || isNavigationContextError(error) || changing || timedOut)) throw navigationObservationError(navigationError ?? error);
      throw error;
    }
    record.layoutPending = false;
  }
  async sourceFence(record, sourceKey, documentKey, paused) {
    const meta = this.session?.pages.get(record.pageId);
    const documentIdentity = meta ? this.session.frameIdentity(meta) : null;
    const checkDocument = () => {
      if (!record.page.isClosed() && meta && (meta.documentIdentityReady === false || this.session.frameIdentity(meta) !== documentIdentity)) {
        throw navigationObservationError();
      }
    };
    // Pause all production capture before injecting the private pixel probe.
    // Existing viewers hold their previous frame; marker frames never enter RTP.
    await this.rpc.request('capture_stop', { page: record.pageId });
    // A resize interrupted by navigation has already reached the compositor.
    // Recover only its read-only confirmation, never another native resize or
    // user gesture, before issuing pixels and coordinate authority again.
    if (record.layoutPending) await this.confirmViewport(record.page, record, 1200);
    checkDocument();
    if (record.proof?.key === sourceKey) return record.proof.seq;
    if (paused && record.proof?.documentKey === documentKey) return record.proof.seq;
    if (paused) throw new Error('current paused document has no verified native pixels; resume before displaying it');
    const nonce = randomBytes(8).toString('hex'), key = `__pogoPixels_${id()}`;
    let seen, failure;
    try {
      await record.page.evaluate(({ nonce, key, scale }) => {
        scale = devicePixelRatio;
        const canvas = document.createElement('canvas'); canvas.width = 16; canvas.height = 16;
        canvas.setAttribute('aria-hidden', 'true'); canvas.setAttribute('popover', 'manual');
        canvas.style.cssText = `all:initial!important;display:block!important;position:fixed!important;inset:auto!important;left:${2 / scale}px!important;top:${2 / scale}px!important;width:${16 / scale}px!important;height:${16 / scale}px!important;padding:0!important;margin:0!important;border:0!important;opacity:1!important;visibility:visible!important;filter:none!important;transform:none!important;animation:none!important;transition:none!important;pointer-events:none!important;image-rendering:pixelated!important;z-index:2147483647!important;`;
        const bits = BigInt('0x' + nonce), context = canvas.getContext('2d');
        for (let i = 0; i < 64; i++) { context.fillStyle = (bits >> BigInt(63 - i)) & 1n ? '#fff' : '#000'; context.fillRect((i % 8) * 2, Math.floor(i / 8) * 2, 2, 2); }
        document.documentElement.append(canvas); globalThis[key] = canvas; canvas.showPopover?.();
      }, { nonce, key, scale: this.getCaptureScale(record.page) });
      seen = await this.rpc.request('probe_present', { page: record.pageId, nonce, pixel_x: 2, pixel_y: 2 });
    } catch (error) {
      failure = error;
    } finally {
      // A navigation already removes the old document's marker. Cleanup must
      // not replace an earlier failure, or turn that normal transition into a
      // permanently failed media source.
      if (!record.page.isClosed()) {
        try { await record.page.evaluate(key => { globalThis[key]?.remove(); delete globalThis[key]; }, key); }
        catch (error) { failure ??= error; }
      }
    }
    checkDocument();
    if (failure) throw isNavigationContextError(failure) ? navigationObservationError(failure) : failure;
    if (!Number.isSafeInteger(seen?.content_seq) || seen.content_seq < 1) throw new Error('native pixels did not confirm the current document');
    const clean = await this.rpc.request('probe_absent', { page: record.pageId, nonce, min_seq: seen.content_seq, pixel_x: 2, pixel_y: 2 });
    checkDocument();
    if (!Number.isSafeInteger(clean?.content_seq) || clean.content_seq <= seen.content_seq) throw new Error('native pixels did not confirm probe removal');
    record.proof = { key: sourceKey, documentKey, seq: clean.content_seq }; return clean.content_seq;
  }
  createMedia(page, options) {
    const record = this.records.get(page); if (!record) return Promise.reject(new Error('media page is outside the browser-use session'));
    record.creating++; const captureRevision = ++record.captureRevision;
    const generation = options.generation;
    const current = () => !this.closed && !record.removing && !page.isClosed() && this.records.get(page) === record && generation === this.session.generation;
    const flight = record.gate = record.gate.catch(() => {}).then(async () => {
      await this.pending.get(page);
      if (!current()) throw new Error('native media creation was fenced by cancellation');
      await this.runtime.rpc.request('register_page', { session: this.id, page: record.pageId, display_id: record.displayId, generation });
      const contentSeq = await this.sourceFence(record, options.sourceKey, options.documentKey, options.paused);
      if (!current()) throw new Error('native media creation was fenced by cancellation');
      const { width, height } = this.getCaptureGeometry(page);
      const scope = { session: this.id, page: record.pageId, view: options.viewId ?? randomUUID(), epoch: ++this.nextEpoch, generation };
      const result = await this.runtime.rpc.request('create', { ...scope, codec: 'vp8', width, height, content_seq_min: contentSeq });
      const bound = { ...scope, peer_id: result.peer_id }; let closed = false, closePromise, visible = options.visible !== false;
      if (!result.offer?.sdp || result.settings?.width !== width || result.settings?.height !== height) {
        await this.runtime.rpc.request('close', bound); throw new Error('invalid WebRTC source settings');
      }
      const event = value => {
        if (closed || value.type !== 'event' || Object.keys(bound).some(key => value[key] !== bound[key])) return;
        if (value.event?.type === 'state') options.onState?.(value.event.state);
      };
      this.runtime.rpc.on('event', event);
      const media = {
        offer: result.offer, pixels: { width, height }, settings: result.settings,
        get visible() { return visible; },
        answer: sdp => this.runtime.rpc.request('answer', { ...bound, answer: { type: 'answer', sdp } }),
        candidate: candidate => candidate === null ? Promise.resolve() : this.runtime.rpc.request('candidate', { ...bound, candidate }),
        diagnostics: () => this.runtime.rpc.request('stats', bound),
        setProfile: async profile => { if (!['reading', 'interactive'].includes(profile)) throw new Error('invalid media profile'); },
        setVisible: value => {
          if (typeof value !== 'boolean') return Promise.reject(new Error('invalid native media visibility'));
          visible = value; record.captureRevision++;
          return record.gate = record.gate.catch(() => {}).then(async () => {
            if (closed || !current()) return;
            await this.syncMediaCapture(record, generation);
          });
        },
        requestFrame: async () => {
          if (!closed && visible && current()) await this.rpc.request('request_idr', { page: record.pageId });
        },
        close: () => {
          if (closePromise) return closePromise; closed = true; this.runtime.rpc.off('event', event); record.peers.delete(result.peer_id);
          const revision = record.captureRevision;
          closePromise = (async () => {
            try { await this.runtime.rpc.request('close', bound); }
            finally {
              if (!record.creating && !record.removing && revision === record.captureRevision && !this.closed) {
                // Serialize a last-visible-viewer close with pause/resume and
                // source creation. Hidden peers keep their RTP/ICE identity.
                await (record.gate = record.gate.catch(() => {}).then(async () => {
                  if (!record.removing && !this.closed && revision === record.captureRevision) await this.syncMediaCapture(record, generation);
                }));
              }
            }
          })();
          record.peerClosures.add(closePromise);
          closePromise.finally(() => record.peerClosures.delete(closePromise)).catch(() => {});
          return closePromise;
        },
      };
      record.peers.set(result.peer_id, media);
      try { if (!current()) throw new Error('native media creation was fenced by cancellation'); await this.syncMediaCapture(record, generation); }
      catch (error) { await media.close(); throw error; }
      return media;
    }).finally(async () => {
      record.creating--;
      if (!record.creating && !record.peers.size && !record.removing && captureRevision === record.captureRevision && !this.closed && this.records.get(page) === record) await this.rpc.request('capture_stop', { page: record.pageId });
    }); return flight;
  }
  async syncMediaCapture(record, generation) {
    if (this.closed || record.removing || this.records.get(record.page) !== record || generation !== this.session.generation) return;
    // The source belongs to a Page, so one hidden viewer cannot pause another
    // visible viewer. A Page with only warm peers pays no encoder cost.
    if ([...record.peers.values()].some(media => media.visible !== false)) {
      await this.rpc.request('capture_start', { page: record.pageId, generation });
      await this.rpc.request('request_idr', { page: record.pageId });
    } else await this.rpc.request('capture_stop', { page: record.pageId });
  }
  async pointer(page, event) {
    const record = await this.pending.get(page); if (!record) throw new Error('native pointer page is unbound');
    const m = event.modifiers ?? 0;
    await this.rpc.request('pointer', { page: record.pageId, x: event.x, y: event.y,
      kind: event.type === 'wheel' ? 'wheel' : event.phase === 'move' ? 'move' : 'button',
      ...(event.type === 'wheel' ? { dx: event.delta_x, dy: event.delta_y } : event.phase === 'move' ? {} : { button: event.button, pressed: event.phase === 'down' }),
      modifiers: { alt: !!(m & 1), ctrl: !!(m & 2), logo: !!(m & 4), shift: !!(m & 8) } });
  }
  runInput(page, event, dispatch, guard = () => {}) {
    const action = this.inputQueue = this.inputQueue.catch(() => {}).then(async () => {
      if (this.closed || this.failed) throw new HTTPError(410, 'native browser input session is closed');
      guard();
      if (this.seatOwner && this.seatOwner !== page && (this.seatKeys.size || this.seatButtons.size)) throw new HTTPError(409, 'finish the current Sheet gesture before entering another Sheet', 'LIVE_BUSY');
      if (event.type === 'pointer' && event.phase === 'down') this.seatButtons.add(event.button);
      if (event.type === 'key' && event.phase === 'down') this.seatKeys.add(event.code || event.key);
      if (this.seatKeys.size || this.seatButtons.size) this.seatOwner = page;
      await dispatch();
      if (event.type === 'pointer' && event.phase === 'up') this.seatButtons.delete(event.button);
      if (event.type === 'key' && event.phase === 'up') this.seatKeys.delete(event.code || event.key);
      if (!this.seatKeys.size && !this.seatButtons.size) this.seatOwner = null;
    }); return action;
  }
  async inputSync(page) { const record = await this.pending.get(page); await this.rpc.request('sync', { page: record.pageId }); }
  async release(page) {
    const record = await this.pending.get(page);
    if (record && !this.closed) await this.rpc.request('release', { page: record.pageId });
    if (this.seatOwner === page) { this.seatKeys.clear(); this.seatButtons.clear(); this.seatOwner = null; }
  }
  async setOffline(value) { await this.context.setOffline(value); }
  async closePage(page) {
    if (!this.ownsPage(page)) throw new Error('native page is outside this session');
    const record = this.records.get(page);
    await page.close({ runBeforeUnload: false }); await this.removePage(record);
  }
  removePage(record) {
    if (record.removal) return record.removal;
    record.removing = true;
    record.removal = (async () => {
      // Closing a Chromium Page is only the first stage. Cancel can create a
      // replacement after old source creation and all peer cleanup retire.
      await record.gate.catch(() => {});
      await Promise.all([...record.peers.values()].map(media => media.close()).concat([...record.peerClosures]));
      if (!this.closed && record.pageId) {
        if (record.nativeBound) await this.rpc.request('unbind_sheet', { page: record.pageId });
        if (record.nativeRegistered) await this.runtime.rpc.request('unregister_page', { session: this.id, page: record.pageId });
      }
      this.records.delete(record.page); this.pending.delete(record.page); await record.client?.detach().catch(() => {});
    })(); return record.removal;
  }
  fail() {
    if (this.closed || this.failed) return; this.failed = true;
    if (this.session) { this.session.closed = true; for (const view of this.session.liveViews) view.close(1011, 'native browser session disconnected'); }
    this.close().catch(() => {});
  }
  close() {
    if (this.closePromise) return this.closePromise; this.closed = true;
    this.issuedPrivateURLs.clear();
    this.closePromise = (async () => {
      let transportError;
      await Promise.allSettled([...this.records.values()].flatMap(record => [...record.peers.values()].map(media => media.close()).concat([...record.peerClosures])));
      await this.downloads?.close();
      await Promise.race([this.browser?.close().catch(() => {}), pause(2000)]);
      try { await this.bootstrapTransport?.closeConfirmed(); } catch (error) { transportError = error; }
      await Promise.allSettled([...this.chromeChildren].map(child => stop(child)));
      if (this.rpc && !this.rpc.closed) await this.rpc.request('shutdown', {}, 2000).catch(() => {});
      await stop(this.child); this.rpc?.close();
      await Promise.allSettled([...this.records.values()].map(record => record.gate));
      await Promise.allSettled([...this.records.values()].map(record => record.removal).filter(Boolean));
      if (this.registered && this.runtime.rpc && !this.runtime.rpc.closed) await this.runtime.rpc.request('unregister_session', { session: this.id });
      this.runtime.hosts.delete(this.id); if (this.dir) await rm(this.dir, { recursive: true, force: true });
      if (transportError && !this.bootstrapTransport.closed) throw transportError;
    })(); return this.closePromise;
  }
}
