import { WebSocketServer, WebSocket } from 'ws';
import { randomUUID } from 'node:crypto';
import { HTTPError } from './session.js';
import { isNavigationContextError, navigationObservationError } from './navigation-recovery.js';

const MAX_BUFFER = 4 * 1024 * 1024;
const integer = (value, min, max) => Number.isSafeInteger(value) && value >= min && value <= max;
const finite = value => typeof value === 'number' && Number.isFinite(value);
const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key));
const invalid = message => { throw new HTTPError(400, message); };
const bounded = async (promise, ms = 2000) => {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new HTTPError(504, 'live browser operation is still pending; do not replay')), ms); })]); }
  finally { clearTimeout(timer); }
};
const errorCode = error => ['LIVE_BUSY', 'LIVE_FRAME_STALE', 'LIVE_NAVIGATION_TRANSITION'].includes(error.code) ? error.code : ({ 400: 'LIVE_INVALID', 403: 'LIVE_FORBIDDEN', 409: 'LIVE_FENCED', 410: 'LIVE_FENCED', 504: 'LIVE_PENDING' })[error.status] ?? 'LIVE_UNAVAILABLE';
const elapsed = start => Math.round(Math.max(0, performance.now() - start) * 100) / 100;

// This is an explicit, bounded input protocol. Clients cannot send arbitrary
// CDP commands, evaluate page JavaScript, or choose another browser target.
export function validateLiveEvent(event) {
  if (!event || typeof event !== 'object' || Array.isArray(event)) invalid('live input requires an event');
  const modifiers = event.modifiers ?? 0;
  if (!integer(modifiers, 0, 15)) invalid('invalid input modifiers');
  if (event.type === 'pointer') {
    if (!exact(event, ['type', 'phase', 'x', 'y', 'button', 'buttons', 'modifiers', 'click_count']) ||
      !['down', 'move', 'up', 'cancel'].includes(event.phase) || !finite(event.x) || !finite(event.y) ||
      !['left', 'middle', 'right', 'none'].includes(event.button) || !integer(event.buttons, 0, 7) || !integer(event.click_count ?? 0, 0, 3)) invalid('invalid pointer event');
    if (['down', 'up'].includes(event.phase) && event.button === 'none') invalid('pointer down/up requires a button');
  } else if (event.type === 'wheel') {
    if (!exact(event, ['type', 'x', 'y', 'delta_x', 'delta_y', 'modifiers']) || ![event.x, event.y, event.delta_x, event.delta_y].every(finite) || Math.abs(event.delta_x) > 10000 || Math.abs(event.delta_y) > 10000) invalid('invalid wheel event');
  } else if (event.type === 'key') {
    if (!exact(event, ['type', 'phase', 'key', 'code', 'key_code', 'modifiers', 'repeat']) || !['down', 'up'].includes(event.phase) || typeof event.key !== 'string' || event.key.length > 80 || typeof event.code !== 'string' || event.code.length > 80 || !integer(event.key_code, 0, 255) || (event.repeat !== undefined && typeof event.repeat !== 'boolean')) invalid('invalid key event');
  } else if (event.type === 'text') {
    if (!exact(event, ['type', 'text']) || typeof event.text !== 'string' || !event.text.length || event.text.length > 100000) invalid('invalid committed text');
  } else if (event.type === 'reset') {
    if (!exact(event, ['type'])) invalid('invalid input reset');
  } else invalid('unsupported live input event');
  return event;
}

export class LiveView {
  constructor(socket, session, binding) {
    this.socket = socket; this.session = session; this.binding = binding;
    this.meta = session.selectedPage(binding);
    this.client = null; this.closed = false; this.revision = 0;
    this.sequence = 0; this.frameSequence = 0; this.frames = new Map();
    this.viewId = randomUUID();
    this.inputCount = 0;
    this.connectionRevision = 0;
    this.performance = { queue_ms: null, dispatch_ms: null, input_count: 0, restarts: 0, setup_ms: null };
    this.profile = 'reading'; this.activityAt = 0;
    this.media = null; this.mediaEpoch = null; this.mediaScale = null;
    this.codecPreference = 'standard';
    this.mediaQueue = Promise.resolve();
    this.navigationRecovery = null; this.navigationRetryAt = 0;
    this.viewVisible = true; this.visibilityEnabled = false; this.visibilityRevision = 0;
    this.visibilityTransition = false; this.visibilityQueue = Promise.resolve();
    this.viewportTransition = false;
    this.pressed = new Set(); this.keys = new Map(); this.point = { x: 0, y: 0 };
    this.lastState = ''; this.stamp = ''; this.minFrameTime = 0;
    this.overlayEnabled = false; this.overlaySequence = 0; this.lastOverlay = ''; this.lastCachedOverlay = '';
    this.overlayFlight = null;
    this.cleanup = Promise.resolve();
    socket.on('error', () => this.close());
    socket.on('close', () => this.close());
    socket.on('message', (data, binary) => {
      if (binary) { this.sendError(new HTTPError(400, 'live messages must be JSON')); return; }
      let body;
      try { body = JSON.parse(data.toString()); } catch { this.sendError(new HTTPError(400, 'invalid live JSON')); return; }
      this.receive(body).catch(error => {
        this.sendError(error, body?.seq);
        // An old packet never tears down a newer stream or releases its new
        // gesture. Real document/viewport/owner transitions fence at the source.
        if (error.code === 'LIVE_FRAME_STALE') this.publishState(true);
      });
    });
  }

  send(value) {
    if (this.closed || this.socket.readyState !== WebSocket.OPEN) return false;
    if (this.socket.bufferedAmount > MAX_BUFFER) { this.close(1013, 'viewer is too slow'); return false; }
    this.socket.send(JSON.stringify(value)); return true;
  }
  sendError(error, seq) {
    this.send({ type: 'error', ...(Number.isSafeInteger(seq) ? { seq } : {}), code: errorCode(error), error: String(error.message).slice(0, 500) });
  }
  identity() {
    const s = this.session, viewport = s.viewport(this.meta);
    const pixels = s.host?.displayMode === 'wayland' ? s.host.getCaptureGeometry?.(this.meta.page) : null;
    return `${s.frameIdentity(this.meta)}:${s.controlFor(this.meta).epoch}:${viewport.width}x${viewport.height}${pixels ? `:${pixels.width}x${pixels.height}` : ''}`;
  }
  checkPage() {
    if (this.closed) throw new HTTPError(410, 'live view is closed');
    if (this.session.selectedPage(this.binding) !== this.meta) throw new HTTPError(409, 'live browser page changed');
  }
  async start() {
    this.checkPage();
    if (this.session.host?.displayMode === 'desktop') throw new HTTPError(410, 'native desktop pages do not expose a remote live view');
    if (!this.session.mediaBroker) throw new HTTPError(503, 'native WebRTC capture is unavailable; restart the browser worker');
    if (this.session.liveViews.size >= 8) throw new HTTPError(429, 'too many live viewers in this session');
    // Initialize the internal capture pages alongside CDP attachment. This
    // neither captures a website nor allocates another visible browser tab.
    const warm = this.session.mediaBroker.warm?.().catch(() => {});
    await this.session.devtools.attach(this.meta);
    if (this.meta.documentIdentityReady === false) throw new HTTPError(503, 'browser document identity is unavailable');
    this.session.liveViews.add(this);
    this.client = await this.session.context.newCDPSession(this.meta.page);
    if (this.closed) { await this.client.detach().catch(() => {}); return; }
    this.checkPage();
    this.meta.page.once('close', this.pageClosed = () => this.close(1000, 'browser tab closed'));
    const wayland = this.session.mediaBroker.runtime === 'wayland';
    this.send({ type: 'ready', protocol: 2, transport: 'webrtc', capabilities: { performance: true, action_overlay: true, codec_preferences: wayland ? ['standard'] : ['standard', 'responsive'], ...(wayland ? { runtime: 'wayland', view_visibility: true, render_scale_limit: this.session.host?.runtime?.maxScale ?? this.session.mediaConfig?.deviceScaleFactor ?? 2 } : {}) }, state: this.session.state(this.binding) });
    this.configureTimer = setTimeout(() => {
      if (this.mediaScale === null) this.close(1002, 'WebRTC display configuration was not received');
    }, 15000);
    this.configureTimer.unref();
    if (this.closed) return;
    this.timer = setInterval(() => this.publishState(), 300); this.timer.unref();
    this.performanceTimer = setInterval(() => this.publishPerformance(), 2000); this.performanceTimer.unref();
    this.overlayTimer = setInterval(() => this.publishOverlay(), 100); this.overlayTimer.unref();
    this.publishState();
    void warm;
  }
  overlayBinding() {
    const s = this.session, frame = [...this.frames.values()].at(-1), control = s.controlFor(this.meta);
    if (!frame || this.closed || !this.overlayEnabled || !this.mediaEpoch || frame.media_epoch !== this.mediaEpoch) return null;
    return { frame_id: frame.frame_id, media_epoch: this.mediaEpoch, context_ref: s.contextRef,
      tab_id: this.meta.id, generation: s.generation, control_epoch: control.epoch,
      frame_identity: s.frameIdentity(this.meta), document_version: this.meta.version, viewport: s.viewport(this.meta) };
  }
  emitOverlay(binding, overlay) {
    const comparable = JSON.stringify({ ...binding, overlay: overlay ? { ...overlay, sampled_at: 0 } : null });
    if (comparable === this.lastOverlay) return;
    // DOM commits can advance while optional geometry is sampled. Publish
    // the matching status before a marker; strict clients otherwise reject a
    // valid new-document sample and deduplication would suppress its retry.
    const state = this.session.state(this.binding), value = JSON.stringify(state);
    if (state.generation !== binding.generation || state.control_epoch !== binding.control_epoch ||
      state.document_version !== binding.document_version || state.frame_identity !== binding.frame_identity ||
      state.viewport.width !== binding.viewport.width || state.viewport.height !== binding.viewport.height) return;
    if (value !== this.lastState) { this.lastState = value; this.send({ type: 'state', state }); }
    this.lastOverlay = comparable;
    if (!overlay) this.lastCachedOverlay = '';
    this.send({ type: 'action_overlay', seq: ++this.overlaySequence, ...binding, overlay });
  }
  publishOverlay() {
    try { return this.sampleOverlay(); } catch { /* Decorations never fail the live browser. */ }
  }
  sampleOverlay() {
    const binding = this.overlayBinding();
    if (!binding) return;
    const s = this.session, frame = this.frames.get(binding.frame_id), last = s.activity.state().last_action;
    const eligible = this.viewVisible && !this.visibilityTransition && !this.viewportTransition && !this.mediaDisconnected &&
      frame?.acked && this.stamp === this.identity() && s.controlFor(this.meta).mode === 'agent' &&
      s.active === this.meta.id && last?.actor === 'agent' && last.tab_id === this.meta.id &&
      (last.phase === 'started' || Date.now() - last.updated_at <= 4000) &&
      !s.dialog && !s.devtools.isPaused(this.meta) && !s.devtools.isBlocked(this.meta);
    if (!eligible) { this.emitOverlay(binding, null); return; }
    const cached = s.activity.cachedOverlay(s.version(this.meta));
    // Deliver the already observed point/phase immediately. Slow optional DOM
    // geometry cannot delay a click, its acknowledgement, or video presentation.
    const cachedKey = JSON.stringify({ ...binding, overlay: { ...cached, sampled_at: 0 } });
    if (cachedKey !== this.lastCachedOverlay) {
      this.lastCachedOverlay = cachedKey;
      this.emitOverlay(binding, cached);
    }
    if (this.overlayFlight) return;
    const action = last.seq, stamp = JSON.stringify(binding);
    const flight = Promise.resolve().then(() => s.activity.overlay(s.version(this.meta))).then(overlay => {
      if (this.closed || !this.viewVisible || this.visibilityTransition || this.viewportTransition || this.mediaDisconnected ||
        s.controlFor(this.meta).mode !== 'agent' || s.active !== this.meta.id ||
        JSON.stringify(this.overlayBinding()) !== stamp || this.stamp !== this.identity() ||
        s.activity.state().last_action?.seq !== action || !this.frames.get(binding.frame_id)?.acked ||
        s.dialog || s.devtools.isPaused(this.meta) || s.devtools.isBlocked(this.meta)) return;
      // A previous real pointer may be retained for a still document by the
      // screenshot path. Live action marks always belong to this action.
      if (overlay.pointer?.action_seq !== action || overlay.pointer?.actor !== 'agent') delete overlay.pointer;
      if (overlay.last_action?.seq !== action || overlay.last_action?.tab_id !== binding.tab_id) return;
      this.emitOverlay(binding, overlay);
    }).catch(() => {}).finally(() => { if (this.overlayFlight === flight) this.overlayFlight = null; });
    this.overlayFlight = flight;
  }
  activity() {
    this.activityAt = performance.now();
    clearTimeout(this.profileTimer);
    this.setProfile('interactive');
    this.profileTimer = setTimeout(() => this.setProfile('reading'), 1200);
    this.profileTimer.unref();
  }
  setProfile(profile) {
    if (this.closed || this.profile === profile) return;
    this.profile = profile;
    this.media?.setProfile?.(profile).catch(() => {});
  }
  async publishPerformance() {
    if (this.closed || !this.performanceEnabled || !this.media || this.performanceFlight) return;
    const media = this.media, epoch = this.mediaEpoch;
    this.performanceFlight = true;
    try {
      const stats = await media.diagnostics?.();
      if (!this.closed && this.media === media && this.mediaEpoch === epoch && stats) {
        this.send({ type: 'performance', media_epoch: epoch, stats: { ...stats, ...this.performance } });
      }
    } catch { /* Optional measurements never interrupt live input or video. */ }
    finally { this.performanceFlight = false; }
  }
  mediaState(state, current) {
    if (!current()) return;
    this.mediaConnectionState = state;
    if (state === 'connected') {
      clearTimeout(this.disconnectTimer); this.disconnectTimer = undefined;
      const cleanup = this.cleanup;
      cleanup.then(() => {
        if (current() && this.cleanup === cleanup && this.mediaConnectionState === 'connected') this.mediaDisconnected = false;
      }).catch(() => {});
    } else if (state === 'disconnected') {
      if (!this.mediaDisconnected) {
        this.mediaDisconnected = true;
        this.connectionRevision++;
        const queue = this.session.liveQueues.get(this.meta);
        // Old queued inputs fail their connection revision check. Let already
        // dispatched input finish, then release keys before enabling new input.
        this.cleanup = this.cleanup.catch(() => {}).then(async () => {
          if (queue) await bounded(queue);
          await this.release();
        });
        this.cleanup.catch(() => this.close(1011, 'live input release failed'));
      }
      // ICE can reconnect and drop again before held-input cleanup finishes.
      // Re-arm the grace deadline without duplicating cleanup, and do not let
      // duplicate disconnected notifications extend an already running timer.
      this.disconnectTimer ??= setTimeout(() => {
        this.disconnectTimer = undefined;
        if (current() && this.mediaDisconnected) {
          this.sendError(new HTTPError(503, 'WebRTC media disconnected; reconnect the browser view', 'MEDIA_DISCONNECTED'));
          this.close(1011, 'WebRTC media disconnected');
        }
      }, 800);
      this.disconnectTimer.unref();
    } else if (['failed', 'closed'].includes(state)) {
      this.sendError(new HTTPError(503, 'WebRTC media disconnected; reconnect the browser view', 'MEDIA_DISCONNECTED'));
      this.close(1011, 'WebRTC media disconnected');
    }
  }
  awaitPresentation() {
    clearTimeout(this.presentationTimer);
    this.presentationTimer = undefined;
    if (this.closed || !this.viewVisible || this.visibilityTransition || !this.mediaEpoch || !this.frames.size) return;
    const epoch = this.mediaEpoch, visibility = this.visibilityRevision;
    this.presentationTimer = setTimeout(() => {
      if (!this.closed && this.viewVisible && !this.visibilityTransition && this.mediaEpoch === epoch && this.visibilityRevision === visibility && ![...this.frames.values()].some(frame => frame.media_epoch === epoch && frame.acked)) {
        this.sendError(new HTTPError(504, 'WebRTC video was not presented; reconnect before continuing'));
        this.close(1000, 'WebRTC video presentation timed out');
      }
    }, 15000);
    this.presentationTimer.unref();
  }
  setViewVisibility(body) {
    this.checkPage();
    if (this.session.mediaBroker?.runtime !== 'wayland' || !exact(body, ['type', 'visible', 'revision']) || typeof body.visible !== 'boolean' || !integer(body.revision, 1, Number.MAX_SAFE_INTEGER) || body.revision <= this.visibilityRevision) invalid('invalid browser view visibility');
    this.visibilityEnabled = true; this.visibilityRevision = body.revision;
    this.viewVisible = body.visible; this.visibilityTransition = true;
    this.publishOverlay();
    // Revoke input at receipt, before any asynchronous capture/release work.
    // A delayed presentation ACK from the previous visit cannot restore it.
    clearTimeout(this.presentationTimer);
    for (const frame of this.frames.values()) frame.acked = false;
    if (!body.visible) this.connectionRevision++;
    const current = () => !this.closed && this.visibilityRevision === body.revision;
    const job = this.visibilityQueue = this.visibilityQueue.catch(() => {}).then(async () => {
      if (this.closed) return;
      await this.cleanup;
      if (!body.visible) {
        const queue = this.session.liveQueues.get(this.meta);
        if (queue) await bounded(queue.catch(() => {}));
        await this.release();
        if (this.meta.liveInputOwner === this) this.meta.liveInputOwner = null;
      }
      // A create already in progress must finish or retire before its capture
      // can be paused. Hiding never abandons a late native peer allocation.
      await this.mediaQueue;
      if (this.closed) return;
      if (!body.visible) await this.media?.setVisible?.(false);
      else {
        if (!current()) return;
        if (this.mediaEpoch && this.stamp !== this.identity()) await this.fence();
        if (!this.mediaEpoch) await this.restartMedia();
        if (!current()) return;
        await this.media?.setVisible?.(true);
      }
      if (!current()) return;
      this.visibilityTransition = false;
      this.send({ type: 'view_visibility', visible: body.visible, revision: body.revision });
      this.awaitPresentation();
      // Request again after the visibility confirmation. Static pages must
      // deliver a fresh decoded frame even if startup pixels arrived earlier.
      if (body.visible) await this.media?.requestFrame?.();
    });
    return job.catch(error => {
      this.close(1011, 'browser view visibility transition failed');
      throw error;
    });
  }
  restartMedia() {
    if (!this.client || this.closed || this.mediaScale === null || !this.viewVisible || Date.now() < this.navigationRetryAt) return Promise.resolve();
    const stamp = this.identity(), revision = this.revision;
    this.stamp = stamp;
    const previousEpoch = this.mediaEpoch, epoch = randomUUID();
    const startedAt = performance.now();
    this.performance.restarts++;
    this.performance.setup_ms = null;
    this.mediaStartedAt = startedAt;
    clearTimeout(this.disconnectTimer); this.disconnectTimer = undefined; this.mediaDisconnected = false;
    clearTimeout(this.presentationTimer);
    this.mediaEpoch = epoch;
    this.frames.clear();
    if (previousEpoch) this.send({ type: 'media_reset', media_epoch: previousEpoch });
    const ownsEpoch = () => !this.closed && this.mediaEpoch === epoch && revision === this.revision;
    const current = () => ownsEpoch() && stamp === this.identity();
    this.mediaQueue = this.mediaQueue.catch(() => {}).then(async () => {
      await this.cleanup.catch(() => {});
      await this.media?.close(); this.media = null;
      if (!current()) return;
      // Pogo's bookmark/new-tab UI is local. Do not run an encoder for an empty
      // Chromium document; a later navigation creates its own media epoch.
      if (this.meta.page.url() === 'about:blank') return;
      const s = this.session, viewport = s.viewport(this.meta);
      await s.devtools.attach(this.meta);
      if (this.meta.documentIdentityReady === false) throw new HTTPError(503, 'browser document identity is unavailable');
      // A new receiver must not authorize an old document's compositor buffer.
      // Wait for this document to paint, then use a compositor readback as a
      // one-shot barrier. These bytes never become the live video transport.
      if (s.mediaBroker.runtime !== 'wayland' && !s.devtools.isPaused(this.meta) && !s.dialog) {
        await bounded(this.meta.page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))), 4000, 'WebRTC source has not painted yet');
        await bounded(this.client.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }), 4000, 'WebRTC source paint barrier is pending');
      }
      if (!current()) return;
      // CSS layout and input coordinates remain independent of encoded pixels.
      const scale = Math.min(this.mediaScale, 3840 / viewport.width, 2160 / viewport.height);
      const candidates = []; let offered = false;
      const media = await s.mediaBroker.create(this.meta.page, {
        id: epoch, viewId: this.viewId, generation: s.generation, sourceKey: stamp,
        documentKey: `${s.frameIdentity(this.meta)}:${viewport.width}x${viewport.height}`, paused: s.devtools.isPaused(this.meta) || !!s.dialog,
        ...viewport, scale, iceServers: [], codecPreference: this.codecPreference,
        onCandidate: candidate => {
          if (!current()) return;
          const message = { type: 'media_candidate', media_epoch: epoch, candidate };
          if (offered) this.send(message); else candidates.push(message);
        },
        onState: state => this.mediaState(state, current),
      });
      if (!current()) { await media.close(); return; }
      this.media = media;
      if (!this.viewVisible) await media.setVisible?.(false);
      await media.setProfile?.(this.profile).catch(() => {});
      if (!current()) { await media.close(); return; }
      const pixels = media.pixels ?? media.settings;
      const frame = { type: 'media', frame_id: ++this.frameSequence, media_epoch: epoch,
        viewport, pixel_width: pixels.width, pixel_height: pixels.height,
        frame_identity: s.frameIdentity(this.meta), generation: s.generation, control_epoch: s.controlFor(this.meta).epoch,
        document_version: this.meta.version, context_ref: s.contextRef, tab_id: this.meta.id };
      this.frames.set(frame.frame_id, { ...frame, acked: false });
      this.awaitPresentation();
      this.send({ type: 'media_offer', frame, sdp: media.offer.sdp, ice_servers: [] });
      offered = true;
      for (const candidate of candidates) this.send(candidate);
    }).catch(async error => {
      // Only media preparation reads are retryable. This catch never encloses
      // pointer, keyboard, text or form dispatch, so recovery cannot submit an
      // action twice. An old document's rejection has no authority over a
      // receiver which already belongs to a newer document/control revision.
      if (error.code !== 'LIVE_NAVIGATION_TRANSITION' && !isNavigationContextError(error)) throw error;
      if (!ownsEpoch()) return;
      const media = this.media; this.media = null;
      this.mediaEpoch = null; this.stamp = ''; this.frames.clear();
      clearTimeout(this.presentationTimer);
      this.send({ type: 'media_reset', media_epoch: epoch });
      await media?.close();
      if (this.closed || revision !== this.revision) return;
      const key = `${this.session.generation}:${this.session.controlFor(this.meta).epoch}`;
      if (this.navigationRecovery?.key !== key) this.navigationRecovery = { key, startedAt: Date.now(), attempts: 0 };
      const recovery = this.navigationRecovery;
      if (Date.now() - recovery.startedAt >= 8000 || recovery.attempts >= 12) {
        throw new HTTPError(503, 'Browser display could not recover after repeated page navigations; reconnect the browser view');
      }
      recovery.attempts++;
      this.navigationRetryAt = Date.now() + Math.min(1000, recovery.attempts * 150);
      if (recovery.attempts === 1) this.sendError(navigationObservationError(error));
      // publishState prepares only the next display epoch after the backoff;
      // it does not replay the input which caused the page to navigate.
    });
    return this.mediaQueue;
  }
  publishState(force = false) {
    try {
      this.checkPage();
      const state = this.session.state(this.binding), value = JSON.stringify(state);
      if (force || value !== this.lastState) { this.lastState = value; this.send({ type: 'state', state }); }
      this.publishOverlay();
      if (!this.viewportTransition && Date.now() >= this.navigationRetryAt && this.session.phase === 'ready' && this.session.controlFor(this.meta).phase === 'ready' && this.meta.documentIdentityReady !== false && this.mediaScale !== null && this.identity() !== this.stamp) {
        const transition = this.viewVisible ? this.restartMedia() : this.mediaEpoch ? this.fence() : null;
        transition?.catch(error => { this.sendError(error); this.close(1011, 'WebRTC capture failed'); });
      }
    } catch (error) { this.sendError(error); this.close(1000, 'browser page unavailable'); }
  }
  checkInput(body, revision, display = false, ownedAction) {
    this.checkPage();
    if (!this.viewVisible || this.visibilityTransition) throw new HTTPError(409, 'browser view is hidden or changing visibility; input was not dispatched', 'LIVE_BUSY');
    const s = this.session, control = s.controlFor(this.meta);
    if (this.mediaDisconnected) throw new HTTPError(409, 'browser media is recovering; input was not dispatched', 'LIVE_BUSY');
    if ((!display || control.mode === 'human') && (control.mode !== 'human' || body.lease_token !== control.lease || !control.lease)) throw new HTTPError(403, 'live input requires this tab\'s current human control lease');
    if (body.generation !== s.generation || body.control_epoch !== control.epoch || s.phase !== 'ready' || control.phase !== 'ready') throw new HTTPError(409, 'live input control state is stale');
    if (s.contextBlocked(ownedAction)) throw new HTTPError(409, 'browser operation outcome is unknown; cancel the browser before continuing');
    if (this.meta.documentIdentityReady === false) throw new HTTPError(409, 'browser document identity is unavailable');
    if (revision !== this.revision) throw new HTTPError(409, 'live input used an earlier displayed frame; do not replay', 'LIVE_FRAME_STALE');
    const frame = this.frames.get(body.frame_id), viewport = s.viewport(this.meta);
    const pixels = s.host?.displayMode === 'wayland' ? s.host.getCaptureGeometry?.(this.meta.page) : null;
    // Static documents need not repaint periodically. An acknowledged frame
    // remains usable until its document, viewport, owner or bounded history
    // changes; wall-clock expiry would make an idle form impossible to type in.
    if (!frame?.acked || frame.media_epoch !== this.mediaEpoch || body.media_epoch !== this.mediaEpoch || frame.generation !== s.generation || frame.control_epoch !== control.epoch || frame.frame_identity !== s.frameIdentity(this.meta) || frame.viewport.width !== viewport.width || frame.viewport.height !== viewport.height || pixels && (frame.pixel_width !== pixels.width || frame.pixel_height !== pixels.height)) throw new HTTPError(409, 'live input requires a presented WebRTC frame from the current page', 'LIVE_FRAME_STALE');
    if (control.mode === 'human') {
      if (this.meta.liveInputOwner && this.meta.liveInputOwner !== this) throw new HTTPError(409, 'another live view owns this tab\'s input gesture; close it before continuing');
      this.meta.liveInputOwner = this;
    }
    return viewport;
  }
  async receive(body) {
    if (!body || typeof body !== 'object' || Array.isArray(body)) invalid('live message must be an object');
    if (body.type === 'view_visibility') return this.setViewVisibility(body);
    if (body.type === 'media_config') {
      if (!exact(body, ['type', 'scale', 'performance', 'codec_preference', 'action_overlay']) || !finite(body.scale) || body.scale < 1 || body.scale > 2 || body.performance !== undefined && typeof body.performance !== 'boolean' || body.action_overlay !== undefined && typeof body.action_overlay !== 'boolean' || body.codec_preference !== undefined && !['standard', 'responsive'].includes(body.codec_preference) || this.mediaScale !== null) invalid('invalid WebRTC display configuration');
      this.performanceEnabled = body.performance === true;
      this.overlayEnabled = body.action_overlay === true;
      this.codecPreference = this.session.mediaBroker?.runtime === 'wayland' ? 'standard' : body.codec_preference ?? 'standard';
      clearTimeout(this.configureTimer); this.mediaScale = body.scale;
      await this.restartMedia(); return;
    }
    if (body.type === 'media_answer' || body.type === 'media_candidate') {
      const answer = body.type === 'media_answer';
      if (!exact(body, ['type', 'media_epoch', answer ? 'sdp' : 'candidate']) || typeof body.media_epoch !== 'string') invalid('invalid WebRTC signal');
      if (body.media_epoch !== this.mediaEpoch || !this.media || this.stamp !== this.identity()) return;
      if (answer) {
        if (typeof body.sdp !== 'string' || !body.sdp.startsWith('v=0') || body.sdp.length > 100000) invalid('invalid WebRTC answer');
        await this.media.answer(body.sdp);
      } else {
        if (body.candidate !== null && (!body.candidate || typeof body.candidate !== 'object' || typeof body.candidate.candidate !== 'string' || body.candidate.candidate.length > 4096 || Object.keys(body.candidate).some(k => !['candidate', 'sdpMid', 'sdpMLineIndex', 'usernameFragment'].includes(k)))) invalid('invalid ICE candidate');
        await this.media.candidate(body.candidate);
      }
      return;
    }
    if (body.type === 'presentation_ack') {
      if (!exact(body, ['type', 'frame_id', 'media_epoch', 'width', 'height', 'view_revision']) || !integer(body.frame_id, 1, Number.MAX_SAFE_INTEGER) || body.view_revision !== undefined && !integer(body.view_revision, 1, Number.MAX_SAFE_INTEGER)) invalid('invalid presentation acknowledgement');
      if (this.visibilityEnabled && (!this.viewVisible || this.visibilityTransition || body.view_revision !== this.visibilityRevision)) return;
      const frame = this.frames.get(body.frame_id);
      if (frame && frame.media_epoch === body.media_epoch && body.media_epoch === this.mediaEpoch && this.stamp === this.identity() && body.width === frame.pixel_width && body.height === frame.pixel_height) {
        if (!frame.acked && this.mediaStartedAt !== undefined) this.performance.setup_ms = elapsed(this.mediaStartedAt);
        frame.acked = true;
        this.navigationRecovery = null; this.navigationRetryAt = 0;
        this.publishOverlay();
        clearTimeout(this.presentationTimer);
      }
      return;
    }
    const fields = ['type', 'seq', 'generation', 'control_epoch', 'lease_token', 'frame_id', 'media_epoch'];
    if (!['input', 'viewport'].includes(body.type) || !exact(body, [...fields, ...(body.type === 'input' ? ['event'] : ['width', 'height', 'preferred_scale'])]) || !integer(body.seq, 1, Number.MAX_SAFE_INTEGER) || body.seq <= this.sequence) invalid('live input sequence must increase; input is never replayed');
    this.sequence = body.seq;
    if (!integer(body.generation, 1, Number.MAX_SAFE_INTEGER) || !integer(body.control_epoch, 0, Number.MAX_SAFE_INTEGER) || body.lease_token !== undefined && (typeof body.lease_token !== 'string' || body.lease_token.length > 160) || body.type === 'input' && typeof body.lease_token !== 'string' || !integer(body.frame_id, 1, Number.MAX_SAFE_INTEGER)) invalid('invalid live input binding');
    if (this.inputCount >= 128) { await this.fence(); throw new HTTPError(409, 'live input queue is full; inputs were discarded, do not replay'); }
    if (body.type === 'input') validateLiveEvent(body.event);
    else if (!integer(body.width, 320, 2560) || !integer(body.height, 200, 1600)) invalid('viewport must be 320–2560 by 200–1600 CSS pixels');
    if (body.type === 'viewport' && body.preferred_scale !== undefined && (this.session.host?.displayMode !== 'wayland' || !finite(body.preferred_scale) || body.preferred_scale < 1 || body.preferred_scale > 2)) invalid('preferred_scale requires Wayland and a finite number from 1 to 2');
    const revision = this.revision;
    const connectionRevision = this.connectionRevision;
    this.checkInput(body, revision, body.type === 'viewport');
    const receivedAt = performance.now(); let dispatchedAt;
    if (body.type === 'input' && body.event.type !== 'reset') this.activity();
    this.inputCount++;
    try {
      const dispatch = body.type === 'viewport' ? this.session.resizeLiveViewport.bind(this.session) : this.session.dispatchLiveInput.bind(this.session);
      await dispatch({ ...this.binding, ...body }, async (_meta, ownedAction) => {
        if (connectionRevision !== this.connectionRevision) throw new HTTPError(409, 'input preceded a media interruption and was discarded', 'LIVE_BUSY');
        // The resize queue supplies its exact tracked promise. Exempt only
        // that operation, never other uncertain actions or failed releases.
        const viewport = this.checkInput(body, revision, body.type === 'viewport', ownedAction);
        dispatchedAt = performance.now();
        if (body.type === 'viewport') {
          if (this.pressed.size || this.keys.size) throw new HTTPError(409, 'finish the current gesture before resizing the browser', 'LIVE_BUSY');
          const host = this.session.host;
          const scaleChanged = host?.displayMode === 'wayland' && host.getTargetCaptureScale?.(this.meta.page, body, body.preferred_scale) !== host.getCaptureScale?.(this.meta.page);
          if (viewport.width !== body.width || viewport.height !== body.height || scaleChanged) {
            // Polling may run while cleanup or Chromium's resize is pending.
            // Rebuild once after this transaction, never for its old viewport.
            this.viewportTransition = true;
            try {
              await this.fence();
              if (host?.setViewport) await host.setViewport(this.meta.page, { width: body.width, height: body.height, ...(body.preferred_scale === undefined ? {} : { preferred_scale: body.preferred_scale }) }, { observationTimeout: 1200 });
              else {
                try { await this.meta.page.setViewportSize({ width: body.width, height: body.height }); }
                catch (error) { throw isNavigationContextError(error) ? navigationObservationError(error) : error; }
              }
              this.session.invalidatePage(this.meta);
              const state = this.session.state(this.binding);
              this.lastState = JSON.stringify(state); this.send({ type: 'state', state });
              await this.restartMedia();
            } finally { this.viewportTransition = false; }
          }
        } else await this.dispatch(body.event, viewport);
      });
      const timing = { queue_ms: Math.round(Math.max(0, (dispatchedAt ?? receivedAt) - receivedAt) * 100) / 100, dispatch_ms: dispatchedAt === undefined ? 0 : elapsed(dispatchedAt) };
      this.performance.queue_ms = timing.queue_ms;
      this.performance.dispatch_ms = timing.dispatch_ms;
      if (body.type === 'input') this.performance.input_count++;
      this.send({ type: 'ack', seq: body.seq, timing });
    } finally { this.inputCount--; }
  }
  async dispatch(event, viewport) {
    const native = this.session.host?.displayMode === 'wayland' ? this.session.host : null;
    if (native) {
      const revision = this.revision, connection = this.connectionRevision, identity = this.identity();
      const guard = () => {
        this.checkPage();
        if (revision !== this.revision || identity !== this.identity() || connection !== this.connectionRevision || this.mediaDisconnected || this.session.controlFor(this.meta).mode !== 'human') throw new HTTPError(409, 'input was fenced while waiting for the native seat; do not replay', 'LIVE_FRAME_STALE');
      };
      return native.runInput(this.meta.page, event, () => this.dispatchEvent(event, viewport, guard), guard);
    }
    return this.dispatchEvent(event, viewport);
  }
  async dispatchEvent(event, viewport, guard = () => {}) {
    const client = this.client;
    const native = this.session.host?.displayMode === 'wayland' ? this.session.host : null;
    if (event.type === 'reset' || event.type === 'pointer' && event.phase === 'cancel') { await this.release(); return; }
    if (['pointer', 'wheel'].includes(event.type)) {
      if (event.x < 0 || event.y < 0 || event.x >= viewport.width || event.y >= viewport.height) invalid('pointer coordinates are outside the current viewport');
      this.point = { x: event.x, y: event.y };
    }
    if (event.type === 'pointer') {
      const type = { down: 'mousePressed', up: 'mouseReleased', move: 'mouseMoved' }[event.phase];
      if (event.phase === 'down') this.pressed.add(event.button);
      if (event.phase === 'up') this.pressed.delete(event.button);
      const button = event.phase === 'move' && this.pressed.size ? [...this.pressed].at(-1) : event.button;
      if (native) await native.pointer(this.meta.page, event);
      else await client.send('Input.dispatchMouseEvent', { type, x: event.x, y: event.y, button, buttons: event.buttons, modifiers: event.modifiers ?? 0, clickCount: event.click_count ?? 0 });
    } else if (event.type === 'wheel') {
      if (native && !(event.modifiers & 6)) await native.pointer(this.meta.page, event);
      else { await native?.inputSync(this.meta.page); guard(); await client.send('Input.dispatchMouseEvent', { type: 'mouseWheel', x: event.x, y: event.y, deltaX: event.delta_x, deltaY: event.delta_y, modifiers: event.modifiers ?? 0 }); }
    } else if (event.type === 'key') {
      await native?.inputSync(this.meta.page);
      guard();
      const key = { key: event.key, code: event.code, windowsVirtualKeyCode: event.key_code, modifiers: event.modifiers ?? 0 };
      if (event.phase === 'down') this.keys.set(event.code || event.key, key); else this.keys.delete(event.code || event.key);
      // Text comes only from beforeinput/composition commit. rawKeyDown avoids
      // duplicating IME or printable text; key shortcuts still reach Chromium.
      const commands = event.phase === 'down' && (key.modifiers & 6) && event.key.toLowerCase() === 'a' ? ['selectAll'] : undefined;
      // Enter has no beforeinput in the local hidden textarea (the UI prevents
      // it), so Chromium needs its character to run native form/newline logic.
      const enter = event.phase === 'down' && event.key === 'Enter';
      await client.send('Input.dispatchKeyEvent', { ...key, type: event.phase === 'down' ? enter ? 'keyDown' : 'rawKeyDown' : 'keyUp', ...(enter ? { text: '\r', unmodifiedText: '\r' } : {}), autoRepeat: event.repeat ?? false, ...(commands ? { commands } : {}) });
    } else if (event.type === 'text') { await native?.inputSync(this.meta.page); guard(); await client.send('Input.insertText', { text: event.text }); }
  }
  async release() {
    if (!this.client) return;
    const pressed = [...this.pressed], keys = [...this.keys.values()];
    this.pressed.clear(); this.keys.clear();
    const native = this.session.host?.displayMode === 'wayland' ? this.session.host : null;
    if (!pressed.length && !keys.length && (!native || this.meta.liveInputOwner !== this)) return;
    // No release is retried. Detaching cannot undo a dispatched page action.
    try { await bounded((async () => {
      if (pressed.length) await this.client.send('Input.cancelDragging').catch(() => {});
      if (!native) for (const button of pressed) await this.client.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: -1, y: -1, button, buttons: 0, clickCount: 0 });
      for (const key of keys) await this.client.send('Input.dispatchKeyEvent', { ...key, modifiers: 0, type: 'keyUp' });
      if (native) await native.release(this.meta.page);
    })()); } catch (error) {
      // The transport cannot prove that Chromium released a held key/button.
      // Do not clear the uncertainty merely because this socket disappeared.
      this.meta.liveInputBlocked = true;
      throw new HTTPError(504, `live input release could not be confirmed; cancel the browser before continuing: ${error.message}`);
    }
  }
  fence() {
    this.revision++;
    clearTimeout(this.disconnectTimer); this.disconnectTimer = undefined; this.mediaDisconnected = false;
    clearTimeout(this.presentationTimer);
    this.stamp = '';
    this.frames.clear();
    const oldEpoch = this.mediaEpoch; this.mediaEpoch = null;
    if (oldEpoch) this.send({ type: 'media_reset', media_epoch: oldEpoch });
    const media = this.media; this.media = null;
    // Cleanup runs before the next owner's action, but never waits behind a
    // stalled input queue. Fencing is immediate even if Chromium is paused.
    this.cleanup = this.cleanup.catch(() => {}).then(() => Promise.all([this.release(), media?.close()])).finally(() => {
      if (this.meta.liveInputOwner === this) this.meta.liveInputOwner = null;
    });
    return this.cleanup;
  }
  async close(code = 1000, reason = 'live view closed') {
    if (this.closed) return;
    this.closed = true; clearInterval(this.timer); clearInterval(this.performanceTimer); clearInterval(this.overlayTimer); clearTimeout(this.configureTimer); clearTimeout(this.profileTimer); clearTimeout(this.disconnectTimer);
    this.session.liveViews.delete(this);
    if (this.pageClosed) this.meta.page.off('close', this.pageClosed);
    this.socket.close(code, reason);
    await this.fence().catch(() => {});
    await this.mediaQueue.catch(() => {});
    await this.client?.detach().catch(() => {});
  }
}

export function attachLiveServer(server, { authenticate, getSession }) {
  const sockets = new WebSocketServer({ noServer: true, maxPayload: 128 * 1024, perMessageDeflate: false });
  server.on('upgrade', async (request, socket, head) => {
    const reject = (status, error) => { if (!socket.destroyed) { socket.end(`HTTP/1.1 ${status} Error\r\nConnection: close\r\nContent-Type: application/json\r\n\r\n${JSON.stringify({ ok: false, error })}`); } };
    try {
      if (!authenticate(request)) throw new HTTPError(401, 'unauthorized');
      const parsed = new URL(request.url, 'http://worker.invalid'), match = /^\/sessions\/([A-Za-z0-9_-]{1,160})\/live$/.exec(parsed.pathname);
      if (!match || request.method !== 'GET') throw new HTTPError(404, 'route not found');
      if ([...parsed.searchParams.keys()].some(key => !['tab_id', 'context_ref'].includes(key)) || ['tab_id', 'context_ref'].some(key => parsed.searchParams.getAll(key).length !== 1)) throw new HTTPError(400, 'live view requires one tab_id and context_ref');
      const binding = Object.fromEntries(parsed.searchParams), session = await getSession(match[1]);
      session.selectedPage(binding);
      if (session.host?.displayMode === 'desktop') throw new HTTPError(410, 'native desktop pages do not expose a remote live view');
      if (session.liveViews.size >= 8) throw new HTTPError(429, 'too many live viewers');
      sockets.handleUpgrade(request, socket, head, ws => {
        const view = new LiveView(ws, session, binding);
        view.start().catch(error => { view.sendError(error); view.close(1011, 'live browser setup failed'); });
      });
    } catch (error) { reject(error.status ?? 503, String(error.message).slice(0, 500)); }
  });
  return { async close() { for (const socket of sockets.clients) socket.terminate(); await new Promise(resolve => sockets.close(resolve)); } };
}
