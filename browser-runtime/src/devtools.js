// Page-scoped CDP clients. Never expose the browser transport to a caller.
import { randomBytes } from 'node:crypto';
import { TargetTree } from './cdp-targets.js';
const BODY_LIMIT = 64 * 1024;
const BODY_TOTAL = 1024 * 1024;
const NETWORK_LIMIT = 256;
const EVENT_LIMIT = 256;
const CONSOLE_LIMIT = 128;
const REDACTED = '[REDACTED]';
const PROTOCOL_ID = /^(?:id|name|type|subtype|method|className|nodeName|localName|nodeId|backendNodeId|parentId|frameId|loaderId|objectId|scriptId|callFrameId|executionContextId|uniqueId|requestId|breakpointId|styleSheetId|unserializableValue)$/;
const REMOTE_TYPES = new Set(['object', 'function', 'undefined', 'string', 'number', 'boolean', 'symbol', 'bigint']);
const CONTENT_FIELD = /^(?:headers|requestHeaders|responseHeaders|postData|body|content|payloadData)$/;
const SECRET = /password|passwd|secret|token|authorization|cookie|apikey|sessionid|credential|csrf|xsrf/i;
const DOMAINS = new Set(['DOM', 'CSS', 'DOMSnapshot', 'Accessibility', 'Runtime', 'Debugger', 'Page', 'Network', 'Log', 'Performance', 'Profiler', 'HeapProfiler', 'Emulation', 'Schema']);
const DENIED = new Set([
  'DOM.setFileInputFiles', 'Page.handleJavaScriptDialog', 'Page.setDownloadBehavior',
  'Page.setInterceptFileChooserDialog', 'Page.crash', 'Page.close', 'Page.setWebLifecycleState',
  'Network.clearBrowserCache', 'Network.clearBrowserCookies', 'Network.getAllCookies',
  'Network.getCookies', 'Network.setCookie', 'Network.setCookies', 'Network.deleteCookies',
  'Network.loadNetworkResource', 'Network.getResponseBodyForInterception',
  'Network.takeResponseBodyForInterceptionAsStream', 'Network.continueInterceptedRequest',
  'Network.setRequestInterception', 'Network.setCookieControls',
]);
const HELPERS = new Set(['help', 'targets.list', 'network.list', 'network.read', 'console.list', 'events.list']);
const EVENTS = [
  'Network.requestWillBeSent', 'Network.requestWillBeSentExtraInfo', 'Network.responseReceived',
  'Network.responseReceivedExtraInfo', 'Network.loadingFinished', 'Network.loadingFailed',
  'Network.webSocketCreated', 'Network.webSocketFrameReceived', 'Network.webSocketFrameSent',
  'Network.webSocketClosed', 'Network.eventSourceMessageReceived',
  'Runtime.consoleAPICalled', 'Runtime.exceptionThrown', 'Runtime.executionContextCreated',
  'Runtime.executionContextDestroyed', 'Runtime.executionContextsCleared', 'Log.entryAdded',
  'Debugger.paused', 'Debugger.resumed', 'Debugger.scriptParsed', 'Debugger.scriptFailedToParse',
  'Page.frameNavigated', 'Page.navigatedWithinDocument', 'Page.frameAttached', 'Page.frameDetached',
  'Page.frameStartedLoading', 'Page.frameStoppedLoading',
  'Page.lifecycleEvent', 'Page.javascriptDialogOpening', 'Page.javascriptDialogClosed',
  'DOM.documentUpdated', 'DOM.childNodeInserted', 'DOM.childNodeRemoved', 'DOM.attributeModified',
  'CSS.styleSheetAdded', 'CSS.styleSheetRemoved', 'CSS.styleSheetChanged',
  'Profiler.consoleProfileStarted', 'Profiler.consoleProfileFinished',
  'HeapProfiler.addHeapSnapshotChunk', 'HeapProfiler.reportHeapSnapshotProgress', 'HeapProfiler.heapStatsUpdate',
];

function error(code) { return new Error(code); }
function plain(value) { return value && typeof value === 'object' && !Array.isArray(value); }
function safeURL(value) {
  try {
    const url = new URL(value);
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password;
  } catch { return false; }
}
function forbiddenParameters(value, depth = 0) {
  if (depth > 24) throw error('DEVTOOLS_PARAMS_TOO_DEEP');
  if (!value || typeof value !== 'object') return;
  for (const [key, item] of Object.entries(value)) {
    // Routing belongs to top-level protocol parameters. Nested values can be
    // ordinary website data (Runtime.callFunctionOn arguments in particular).
    if (depth === 0 && ['browserContextId', 'targetId', 'sessionId'].includes(key)) throw error('DEVTOOLS_CROSS_SESSION_PARAM_DENIED');
    forbiddenParameters(item, depth + 1);
  }
}

export function validateDevToolsAction(a) {
  if (!plain(a) || a.action !== 'devtools' || typeof a.method !== 'string') throw error('DEVTOOLS_METHOD_REQUIRED');
  if (Object.keys(a).some(key => !['action', 'method', 'params', 'op_id', 'target'].includes(key))) throw error('DEVTOOLS_UNSUPPORTED_FIELD');
  if (a.target !== undefined && (typeof a.target !== 'string' || !a.target || a.target.length > 160)) throw error('DEVTOOLS_INVALID_TARGET');
  const params = a.params ?? {};
  if (!plain(params) || Buffer.byteLength(JSON.stringify(params)) > BODY_LIMIT) throw error('DEVTOOLS_INVALID_PARAMS');
  forbiddenParameters(params);
  if (HELPERS.has(a.method)) {
    const keys = a.method === 'network.read' ? ['request_id'] : ['help', 'targets.list'].includes(a.method) ? [] : ['cursor', 'limit', ...(a.method === 'events.list' ? ['method'] : [])];
    if (Object.keys(params).some(key => !keys.includes(key))) throw error('DEVTOOLS_UNSUPPORTED_PARAM');
    if (a.method === 'network.read' && (typeof params.request_id !== 'string' || !params.request_id || params.request_id.length > 160)) throw error('DEVTOOLS_REQUEST_ID_REQUIRED');
    if (params.cursor !== undefined && (!Number.isSafeInteger(params.cursor) || params.cursor < 0)) throw error('DEVTOOLS_INVALID_CURSOR');
    if (params.limit !== undefined && (!Number.isInteger(params.limit) || params.limit < 1 || params.limit > 100)) throw error('DEVTOOLS_INVALID_LIMIT');
    if (params.method !== undefined && (typeof params.method !== 'string' || params.method.length > 100)) throw error('DEVTOOLS_INVALID_EVENT_FILTER');
  } else {
    if (!/^[A-Za-z]+\.[A-Za-z][A-Za-z0-9]+$/.test(a.method) || !DOMAINS.has(a.method.split('.')[0]) || DENIED.has(a.method)) throw error('DEVTOOLS_METHOD_DENIED');
    if (a.method === 'Page.navigate' && !safeURL(params.url)) throw error('DEVTOOLS_HTTP_NAVIGATION_REQUIRED');
    if (a.method === 'Page.printToPDF' && params.transferMode === 'ReturnAsStream') throw error('DEVTOOLS_STREAM_HANDLE_DENIED');
    if (a.method === 'Runtime.evaluate' && params.timeout !== undefined && (typeof params.timeout !== 'number' || !Number.isFinite(params.timeout) || params.timeout < 0)) throw error('DEVTOOLS_INVALID_TIMEOUT');
    // These commands govern the collector itself, not just a page setting.
    if (['Network.disable', 'Runtime.disable', 'Log.disable', 'Page.disable'].includes(a.method)) throw error('DEVTOOLS_COLLECTOR_DISABLE_DENIED');
  }
  return { method: a.method, params, target: a.target };
}

function clipped(text, maximum) {
  const bytes = Buffer.from(String(text));
  if (bytes.length <= maximum) return { text: String(text), bytes: bytes.length, truncated: false };
  let end = maximum;
  while (end && (bytes[end] & 0xc0) === 0x80) end--;
  return { text: bytes.subarray(0, end).toString('utf8'), bytes: bytes.length, truncated: true };
}

class Redactor {
  constructor() { this.secrets = new Set(); }
  remember(value) {
    if (typeof value !== 'string' || !value || value === REDACTED || value.length > 8192) return;
    this.secrets.add(value);
    if (this.secrets.size > 512) this.secrets.delete(this.secrets.values().next().value);
  }
  learn(value, depth = 0) {
    if (depth > 16 || !value || typeof value !== 'object') return;
    if (typeof value.name === 'string' && SECRET.test(value.name.replace(/[^a-z0-9]/gi, ''))) this.remember(value.value);
    for (const [key, item] of Object.entries(value)) {
      if (typeof item === 'string' && /url/i.test(key)) {
        try {
          const url = new URL(item);
          for (const [name, secret] of [...url.searchParams, ...new URLSearchParams(url.hash.slice(1))]) if (SECRET.test(name.replace(/[^a-z0-9]/gi, ''))) this.remember(secret);
        } catch {}
      }
      if (SECRET.test(key.replace(/[^a-z0-9]/gi, ''))) {
        this.remember(item);
        if (typeof item === 'string') {
          if (/authorization/i.test(key)) this.remember(item.replace(/^\S+\s+/, ''));
          if (/cookie/i.test(key)) {
            const pairs = /set.cookie/i.test(key) ? [item.split(';')[0]] : item.split(';');
            for (const pair of pairs) if (pair.includes('=')) {
              this.remember(pair.trim());
              this.remember(pair.slice(pair.indexOf('=') + 1).trim());
            }
          }
        }
      }
      this.learn(item, depth + 1);
    }
  }
  text(value) {
    let text = String(value);
    // URL query/fragment values and common credential representations are
    // scrubbed before exact learned values. Do not expose URL userinfo either.
    text = text.replace(/(https?:\/\/)[^/\s:@]+:[^/@\s]+@/gi, `$1${REDACTED}@`);
    text = text.replace(/([?&#](?:[^=&#\s]*(?:token|secret|password|key|credential|session|auth|csrf|xsrf)[^=&#\s]*)=)[^&#\s]*/gi, `$1${REDACTED}`);
    text = text.replace(/\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/gi, `$1 ${REDACTED}`);
    text = text.replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, REDACTED);
    text = text.replace(/((?:password|passwd|secret|access_token|refresh_token|api_key|authorization|cookie|sessionid)\s*[=:]\s*)[^\s,;&"'<>]+/gi, `$1${REDACTED}`);
    for (const secret of [...this.secrets].sort((a, b) => b.length - a.length)) {
      if (secret.length >= 4) text = text.split(secret).join(REDACTED);
      else if (text === secret) text = REDACTED;
    }
    return text;
  }
  scrub(value, depth = 0, protocol = false) {
    if (depth > 24) return '[DEPTH_LIMIT]';
    if (typeof value === 'string') {
      // Response JSON is often nested inside CDP string fields.
      if (value.length <= 2 * 1024 * 1024 && /^[\s]*[\[{]/.test(value)) {
        try { const parsed = JSON.parse(value); this.learn(parsed); return JSON.stringify(this.scrub(parsed, depth + 1, false)); } catch {}
      }
      return this.text(value);
    }
    if (Array.isArray(value)) return value.map(item => this.scrub(item, depth + 1, protocol));
    if (plain(value)) {
      const out = Object.create(null);
      const namedSecret = typeof value.name === 'string' && SECRET.test(value.name.replace(/[^a-z0-9]/gi, ''));
      const remoteValue = protocol && REMOTE_TYPES.has(value.type);
      for (const [key, item] of Object.entries(value)) {
        if (['__proto__', 'constructor', 'prototype'].includes(key)) continue;
        // Protocol handles and enums must survive intact, even when a cookie
        // coincidentally contains "object" or part of an objectId. Descending
        // into a RemoteObject's by-value payload switches permanently to
        // content mode; business JSON with keys like id/type/name gets no
        // exemption. A PropertyDescriptor.value is itself a RemoteObject,
        // while RemoteObject.value is website data.
        const childProtocol = protocol && !CONTENT_FIELD.test(key)
          && (key !== 'value' || !remoteValue && plain(item) && REMOTE_TYPES.has(item.type));
        out[key] = SECRET.test(key.replace(/[^a-z0-9]/gi, '')) || namedSecret && ['value', 'description'].includes(key)
          ? REDACTED : protocol && typeof item === 'string' && PROTOCOL_ID.test(key) ? item : this.scrub(item, depth + 1, childProtocol);
      }
      return out;
    }
    return value;
  }
  bounded(value, maximum = BODY_LIMIT, protocol = false) {
    this.learn(value);
    const clean = this.scrub(value, 0, protocol);
    const bounded = clipped(JSON.stringify(clean), maximum);
    return bounded.truncated ? { state: 'truncated', text: bounded.text, bytes: bounded.bytes, limit: maximum } : { state: 'available', result: clean, bytes: bounded.bytes };
  }
}

async function deadline(promise, ms, code = 'DEVTOOLS_TIMEOUT') {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(error(code)), ms); })]); }
  finally { clearTimeout(timer); }
}

// Human/agent breakpoint inspection must not consume an operation's execution
// budget. Detach/cancel still ends this waiter, even if Chromium never replies.
async function activeDeadline(promise, state, ms, startedPaused = false) {
  let timer;
  let remaining = ms;
  let previous = performance.now();
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setInterval(() => {
        const now = performance.now();
        if (startedPaused || !state.paused) remaining -= now - previous;
        previous = now;
        if (state.detached) reject(error('DEVTOOLS_DETACHED'));
        else if (remaining <= 0) reject(error('DEVTOOLS_TIMEOUT'));
      }, Math.min(25, ms));
    })]);
  } finally { clearInterval(timer); }
}

export class BrowserDevTools {
  constructor(session) {
    this.session = session;
    this.clients = new Map();
    this.sequence = 0;
    this.namespace = randomBytes(12).toString('hex');
    this.attachment = 0;
    this.blocked = new WeakSet();
    this.pixelCaptures = new WeakMap();
    this.closed = false;
  }

  newState(family, descriptor) {
    const state = {
      meta: family.meta, family, serial: ++this.attachment,
      targetRef: `dt_${this.namespace}_${this.attachment}`,
      targetId: descriptor.targetId, type: descriptor.type, url: descriptor.url,
      parentTargetId: descriptor.parentTargetId, client: descriptor.client, descriptor,
      detached: false, paused: false, initialized: false,
      network: family.network, console: family.console, events: family.events,
      redactor: family.redactor, pending: family.pending, evicted: family.evicted,
      byRequest: new Map(), frames: new Map(), contexts: new Map(), extras: new Map(),
      navigation: 0, initialLoadEpoch: family.meta.pageLoadEpoch, executing: new Set(), listeners: [],
    };
    family.targets.set(state.targetRef, state);
    return state;
  }

  async initialize(state) {
    if (state === state.family.root) {
      const closed = () => {
        if (state.family.detached) return;
        // Losing a debugging transport does not prove that an already sent
        // command stopped executing in Chromium.
        if ([...state.family.targets.values()].some(target => target.executing.size)) this.blocked.add(state.meta.page);
        this.retire(state).catch(() => {});
      };
      state.client.on('close', closed);
      state.listeners.push(['close', closed]);
    }
    for (const method of EVENTS) {
      const listener = params => { if (!state.detached && !state.family.detached) this.event(state, method, params); };
      state.client.on(method, listener);
      state.listeners.push([method, listener]);
    }
    // Worker targets do not implement Page or Log. Runtime carries their
    // console and exception events; Network is attached before startup resumes.
    await Promise.all([
      state.client.send('Network.enable', { maxTotalBufferSize: 4 * BODY_TOTAL, maxResourceBufferSize: BODY_TOTAL, maxPostDataSize: 8192 }),
      state.client.send('Runtime.enable'),
      ...(state.type === 'worker' ? [] : [state.client.send('Log.enable'), state.client.send('Page.enable')]),
    ]);
    if (state === state.family.root) {
      // Reuse this page's existing CDP session; no additional browser/client.
      // A root identity prevents iframe lifecycle events from driving the
      // top-level loading indicator.
      const navigation = state.navigation;
      const tree = await deadline(state.client.send('Page.getFrameTree'), 1000);
      if (state.detached || state.family.detached) throw error('DEVTOOLS_DETACHED');
      // A navigation arriving while this read is in flight has newer authority
      // than the returned tree. Never overwrite its loader with an old reply.
      if (state.navigation === navigation) {
        state.topFrameId = tree.frameTree?.frame?.id;
        if (this.session.observeTopDocument?.(state.meta, tree.frameTree?.frame) === false) throw error('DOCUMENT_IDENTITY_UNAVAILABLE');
      }
      if (state.meta.pageLoadEpoch === state.initialLoadEpoch && !state.topLoaderId) {
        state.topLoaderId = state.meta.loadingLoaderId || (!state.meta.pageLoading || !state.meta.pageLoadEpoch || state.meta.topDocumentVersion > state.meta.pageLoadStartDocument ? tree.frameTree?.frame?.loaderId : null);
        state.topLoadEpoch = state.meta.pageLoadEpoch;
      }
      await state.client.send('Page.setLifecycleEventsEnabled', { enabled: true });
    }
    if (state.detached || state.family.detached) throw error('DEVTOOLS_DETACHED');
    state.initialized = true;
    return state;
  }

  attach(meta) {
    const existing = this.clients.get(meta.id);
    if (existing && existing.meta === meta && !existing.detached) return existing.ready;
    if (this.closed) return Promise.reject(error('DEVTOOLS_CLOSED'));
    if (this.isBlocked(meta)) return Promise.reject(error('DEVTOOLS_RESULT_UNKNOWN_CANCEL_REQUIRED'));
    const family = {
      meta, targets: new Map(), tree: null, detached: false,
      network: [], console: [], events: [], redactor: new Redactor(),
      pending: new Set(), bodyBytes: 0, evicted: { network: 0, console: 0, events: 0 },
      errors: [],
      get paused() { return [...this.targets.values()].some(target => !target.detached && target.paused); },
    };
    const state = this.newState(family, { client: null, type: 'page', url: meta.page.url(), parentTargetId: null });
    family.root = state;
    this.clients.set(meta.id, state);
    state.ready = (async () => {
      const client = await this.session.context.newCDPSession(meta.page);
      state.client = client;
      if (state.detached) { await client.detach().catch(() => {}); throw error('DEVTOOLS_DETACHED'); }
      await this.initialize(state);
      const tree = new TargetTree(client, {
        isPaused: () => family.paused,
        onAttach: async descriptor => {
          if (family.detached) throw error('DEVTOOLS_DETACHED');
          const child = this.newState(family, descriptor);
          try { await this.initialize(child); }
          catch (cause) { this.removeTarget(child); throw cause; }
        },
        onDetach: descriptor => {
          const child = [...family.targets.values()].find(item => item.client === descriptor.client);
          if (child) this.removeTarget(child);
        },
        onInfo: descriptor => {
          const target = [...family.targets.values()].find(item => item.client === descriptor.client);
          if (target) { target.targetId = descriptor.targetId; target.url = descriptor.url; target.type = descriptor.type; }
        },
        onError: (cause, descriptor) => {
          family.errors.push({ state: 'unavailable', type: descriptor?.type, error: clipped(family.redactor.text(cause.message ?? cause), 500).text });
          if (family.errors.length > 16) family.errors.shift();
        },
      });
      family.tree = tree;
      await tree.start();
      state.targetId = tree.root.targetId;
      state.type = tree.root.type;
      state.url = tree.root.url;
      state.descriptor = tree.root;
      if (family.detached) throw error('DEVTOOLS_DETACHED');
      return state;
    })().catch(async cause => { await this.retire(state); throw cause; });
    // Store readiness for navigation integration, and mark rejection handled
    // even if a tab closes before the caller gets a chance to await it.
    meta.devtoolsReady = state.ready;
    state.ready.catch(() => {});
    return state.ready;
  }

  isPaused(meta) { return Boolean(this.clients.get(meta?.id)?.family.paused); }
  isBlocked(meta) { return Boolean(meta?.page && this.blocked.has(meta.page)); }

  async capturePixels(meta, timeoutMs = 2500) {
    const end = Date.now() + timeoutMs;
    const state = await deadline(this.attach(meta), timeoutMs, 'FRAME_CAPTURE_TIMEOUT');
    if (meta.documentIdentityReady === false) throw error('DOCUMENT_IDENTITY_UNAVAILABLE');
    if (this.pixelCaptures.has(meta.page)) throw error('FRAME_CAPTURE_PENDING');
    // Playwright screenshot preprocessing evaluates every iframe. A busy
    // renderer can block that even when the compositor already has pixels.
    // Viewer and agent screenshots share this single root-CDP capture path.
    // Normalize the known host compositor density, not emulated window DPR:
    // CDP deviceScaleFactor alone does not determine captured physical pixels.
    const captureScale = this.session.host?.getCaptureScale?.(meta.page) ?? this.session.captureDeviceScaleFactor ?? 1;
    const raw = state.client.send('Page.captureScreenshot', { format: 'jpeg', quality: 75,
      fromSurface: true, captureBeyondViewport: false, clip: { x: 0, y: 0, ...this.session.viewport(meta), scale: 1 / captureScale } });
    this.pixelCaptures.set(meta.page, raw);
    raw.finally(() => { if (this.pixelCaptures.get(meta.page) === raw) this.pixelCaptures.delete(meta.page); }).catch(() => {});
    const result = await deadline(raw, Math.max(1, end - Date.now()), 'FRAME_CAPTURE_TIMEOUT');
    if (typeof result.data !== 'string' || result.data.length > 16 * 1024 * 1024 || !/^[A-Za-z0-9+/]*={0,2}$/.test(result.data)) throw error('FRAME_CAPTURE_INVALID');
    const data = Buffer.from(result.data, 'base64');
    if (data.length < 3 || data.length > 12 * 1024 * 1024 || data[0] !== 0xff || data[1] !== 0xd8 || data[2] !== 0xff) throw error('FRAME_CAPTURE_INVALID');
    return data;
  }
  provenance(state) {
    return {
      generation: this.session.generation, tab_id: state.meta.id,
      target_ref: state.targetRef, target_type: state.type,
      url: state.redactor.text(state.url || state.meta.page.url()), captured_at: Date.now(),
      scope: 'tab_family',
      coverage: 'Current page plus attached OOPIF and dedicated worker targets; shared/service workers and other tabs are excluded. Logs start at attachment.',
      target_coverage: this.coverage(state.family),
    };
  }
  coverage(family) {
    return {
      state: family.detached ? 'unavailable' : family.errors.length ? 'partial' : 'attached_targets_only',
      supported_types: ['page', 'iframe', 'worker'], excluded_types: ['service_worker', 'shared_worker', 'other_tabs'],
      attached: family.targets.size,
      initializing: [...family.targets.values()].filter(target => !target.initialized || target !== family.root && !target.descriptor.initialized).length,
      rejected: family.tree ? { ...family.tree.rejected } : {},
      prior_activity: 'not_reconstructed',
    };
  }
  targets(state) {
    const family = state.family;
    return {
      items: [...family.targets.values()].map(target => ({
        target_ref: target.targetRef, type: target.type,
        url: clipped(family.redactor.text(target.url || ''), 4096).text,
        parent_target_ref: [...family.targets.values()].find(parent => parent.targetId === target.parentTargetId)?.targetRef ?? null,
        is_root: target === family.root, ready: target.initialized && !target.detached && (target === family.root || target.descriptor.initialized === true),
        paused: target.paused,
      })),
      errors: family.redactor.scrub(family.errors),
      coverage: this.coverage(family),
    };
  }
  record(state, kind, value, limit) {
    const entry = { seq: ++this.sequence, timestamp: Date.now(), tab_id: state.meta.id, target_ref: state.targetRef, target_type: state.type, navigation_id: state.navigation, ...value };
    state[kind].push(entry);
    if (state[kind].length > limit) state.evicted[kind] = state[kind].shift().seq;
    return entry;
  }
  event(state, method, params) {
    state.redactor.learn(params);
    if (method === 'Debugger.paused') { state.paused = true; this.session.debuggerWake?.(); }
    if (method === 'Debugger.resumed') state.paused = false;
    if (method === 'Runtime.executionContextCreated') state.contexts.set(params.context.id, params.context.auxData?.frameId);
    if (method === 'Runtime.executionContextDestroyed') state.contexts.delete(params.executionContextId);
    if (method === 'Runtime.executionContextsCleared') state.contexts.clear();
    if (method === 'Page.frameNavigated') {
      if (state === state.family.root && !params.frame.parentId) {
        this.session.observeTopDocument?.(state.meta, params.frame);
        state.topFrameId = params.frame.id;
        state.topLoaderId = params.frame.loaderId;
        state.meta.loadingLoaderId = params.frame.loaderId;
        state.topLoadEpoch = state.meta.pageLoadEpoch;
      }
      if (!params.frame.parentId) state.navigation++;
      if (!params.frame.parentId || state.type === 'iframe' && params.frame.id === state.targetId) state.url = params.frame.url;
      state.frames.set(params.frame.id, params.frame.loaderId);
    }
    if (state === state.family.root) {
      if (method === 'Page.frameStartedLoading' && params.frameId === state.topFrameId) {
        this.session.startPageLoad?.(state.meta, { newCycle: true });
        state.topLoadEpoch = state.meta.pageLoadEpoch;
        state.topLoaderId = null; state.topRequestId = null;
      }
      if (method === 'Network.requestWillBeSent' && params.type === 'Document' && params.frameId === state.topFrameId) {
        state.topRequestId = params.requestId;
        this.session.startPageLoad?.(state.meta);
        state.topLoaderId = params.loaderId; state.meta.loadingLoaderId = params.loaderId; state.topLoadEpoch = state.meta.pageLoadEpoch;
        this.session.trackPageNavigation?.(state.meta, { frameId: params.frameId, url: params.request?.url, requestId: params.requestId });
      }
      if (method === 'Network.loadingFailed' && params.requestId === state.topRequestId) {
        if (this.session.failPageNavigation) this.session.failPageNavigation(state.meta, { requestId: params.requestId, errorText: params.errorText, canceled: params.canceled, epoch: state.topLoadEpoch });
        else this.session.finishPageLoad?.(state.meta, params.canceled ? 'Page navigation was cancelled.' : 'Page navigation failed.', state.topLoadEpoch);
        state.topRequestId = null;
      }
      // frameStoppedLoading carries no loader identity and can belong to an
      // older document. Only the matching loader's load event ends this cycle.
      if (method === 'Page.lifecycleEvent' && params.name === 'load' && params.frameId === state.topFrameId && params.loaderId === state.topLoaderId) this.session.finishPageLoad?.(state.meta, '', state.topLoadEpoch);
      if (method === 'Page.navigatedWithinDocument' && params.frameId === state.topFrameId && !state.topRequestId && !state.topLoaderId) this.session.finishPageLoad?.(state.meta, '', state.topLoadEpoch);
    }
    if (method === 'Page.frameDetached') state.frames.delete(params.frameId);
    // Give the request event the same private evidence ID as its network row.
    if (method === 'Network.requestWillBeSent') this.request(state, params);
    const safe = state.redactor.bounded(params, 4096, true);
    const frameId = params.frameId ?? params.frame?.id ?? state.contexts.get(params.executionContextId);
    this.record(state, 'events', { method, frame_id: frameId, ...(params.requestId ? { request_id: state.byRequest.get(params.requestId)?.id, cdp_request_id: params.requestId } : {}), params: safe.result ?? { state: 'truncated', text: safe.text } }, EVENT_LIMIT);
    if (method === 'Runtime.consoleAPICalled') {
      this.record(state, 'console', { type: params.type, args: safe.result?.args ?? [{ state: 'truncated', text: safe.text }], frame_id: state.contexts.get(params.executionContextId), execution_context_id: params.executionContextId }, CONSOLE_LIMIT);
    } else if (method === 'Runtime.exceptionThrown' || method === 'Log.entryAdded') {
      this.record(state, 'console', { type: method === 'Log.entryAdded' ? params.entry.level : 'exception', details: safe.result ?? { state: 'truncated', text: safe.text } }, CONSOLE_LIMIT);
    }
    if (method === 'Network.requestWillBeSentExtraInfo' || method === 'Network.responseReceivedExtraInfo') {
      this.extraHeaders(state, params, method === 'Network.requestWillBeSentExtraInfo' ? 'request' : 'response');
    }
    else if (method === 'Network.responseReceived') {
      const entry = state.byRequest.get(params.requestId);
      if (entry) {
        entry.response = { ...entry.response, status: params.response.status, mime_type: params.response.mimeType, headers: this.headers(state, { ...params.response.headers, ...entry.response?.headers }), protocol: params.response.protocol, from_service_worker: params.response.fromServiceWorker };
        entry.status = params.response.status;
        if (/event-stream/i.test(params.response.mimeType ?? '')) entry.body = { state: 'streaming', reason: 'response_not_finished' };
      }
    } else if (method === 'Network.loadingFinished') {
      const entry = state.byRequest.get(params.requestId);
      if (entry) {
        entry.finished_at = Date.now();
        if (state.pending.size >= 16) { entry.body = { state: 'unavailable', reason: 'capture_backpressure_limit' }; return; }
        const pending = this.captureBody(state, entry);
        state.pending.add(pending);
        pending.finally(() => state.pending.delete(pending)).catch(() => {});
      }
    } else if (method === 'Network.loadingFailed') {
      const entry = state.byRequest.get(params.requestId);
      if (entry) { entry.finished_at = Date.now(); entry.body = { state: 'unavailable', reason: 'loading_failed', error: state.redactor.text(params.errorText ?? '') }; }
    }
  }
  headers(state, value) {
    const result = state.redactor.bounded(value ?? {}, 8192);
    return result.result ?? { state: 'truncated', text: result.text };
  }
  extraHeaders(state, params, kind) {
    // ExtraInfo can precede or follow the matching regular event. Redirects
    // reuse requestId; match each successive extra event to the first hop that
    // has not received one, instead of overwriting the newest hop blindly.
    const entry = state.network.find(item => item.target_ref === state.targetRef && item.cdp_request_id === params.requestId && !item[`${kind}_headers_complete`]);
    if (entry) {
      const headers = this.headers(state, params.headers);
      if (kind === 'request') entry.headers = this.headers(state, { ...entry.headers, ...headers });
      else entry.response = { ...entry.response, status: entry.response?.status ?? params.statusCode, headers: this.headers(state, { ...entry.response?.headers, ...headers }) };
      entry[`${kind}_headers_complete`] = true;
    } else {
      const key = `${kind}:${params.requestId}`;
      const pending = state.extras.get(key) ?? [];
      if (pending.length < 8) pending.push({ requestId: params.requestId, headers: this.headers(state, params.headers), statusCode: params.statusCode });
      state.extras.set(key, pending);
      if (state.extras.size > NETWORK_LIMIT) state.extras.delete(state.extras.keys().next().value);
    }
  }
  request(state, params) {
    const previous = state.byRequest.get(params.requestId);
    if (previous && params.redirectResponse) {
      previous.status = params.redirectResponse.status;
      previous.response = { status: params.redirectResponse.status, headers: this.headers(state, params.redirectResponse.headers) };
      previous.body = { state: 'unavailable', reason: 'redirect_response' };
      previous.finished_at = Date.now();
    }
    const seq = ++this.sequence;
    const id = `${this.namespace}:${state.meta.id}:${state.serial}:${seq}`;
    const entry = { seq, id, request_id: id, cdp_request_id: params.requestId, tab_id: state.meta.id, target_ref: state.targetRef, target_type: state.type, frame_id: params.frameId, loader_id: params.loaderId, navigation_id: params.loaderId || state.navigation, timestamp: Date.now(), url: clipped(state.redactor.text(params.request.url), 4096).text, method: params.request.method, resource_type: params.type, headers: this.headers(state, params.request.headers), ...(params.request.postData ? { post_data: clipped(state.redactor.scrub(params.request.postData), 8192).text } : {}), redirect_index: params.redirectResponse && previous ? previous.redirect_index + 1 : 0, ...(params.redirectResponse && previous ? { redirect_from: previous.id } : {}), body: { state: 'pending', reason: 'response_not_finished' } };
    state.network.push(entry);
    state.byRequest.set(params.requestId, entry);
    for (const kind of ['request', 'response']) {
      const key = `${kind}:${params.requestId}`;
      const pending = state.extras.get(key);
      if (pending?.length) {
        this.extraHeaders(state, pending.shift(), kind);
        if (!pending.length) state.extras.delete(key);
      }
    }
    if (state.network.length > NETWORK_LIMIT) {
      const removed = state.network.shift();
      state.evicted.network = removed.seq;
      state.family.bodyBytes -= removed.body.stored_bytes ?? 0;
      const owner = state.family.targets.get(removed.target_ref);
      if (owner?.byRequest.get(removed.cdp_request_id) === removed) owner.byRequest.delete(removed.cdp_request_id);
    }
  }
  async captureBody(state, entry) {
    try {
      const reply = await deadline(state.client.send('Network.getResponseBody', { requestId: entry.cdp_request_id }), 3000);
      if (state.detached || !state.network.includes(entry)) return;
      const mime = entry.response?.mime_type ?? '';
      if (reply.base64Encoded && !/(?:text\/|json|javascript|xml|svg|x-www-form-urlencoded)/i.test(mime)) {
        entry.body = { state: 'binary', bytes: Buffer.byteLength(reply.body ?? '', 'base64'), reason: 'binary_body_not_returned' };
        return;
      }
      const original = reply.base64Encoded ? Buffer.from(reply.body, 'base64').toString('utf8') : reply.body ?? '';
      const result = clipped(state.redactor.scrub(original), BODY_LIMIT);
      const stored = Buffer.byteLength(result.text);
      for (const other of state.network) {
        if (state.family.bodyBytes + stored <= BODY_TOTAL) break;
        if (other === entry || !other.body.stored_bytes) continue;
        state.family.bodyBytes -= other.body.stored_bytes;
        other.body = { state: 'evicted', reason: 'body_cache_limit', bytes: other.body.bytes };
      }
      state.family.bodyBytes += stored;
      entry.body = { state: result.truncated ? 'truncated' : 'available', text: result.text, bytes: result.bytes, stored_bytes: stored, ...(result.truncated ? { limit: BODY_LIMIT } : {}) };
    } catch {
      if (!state.detached && state.network.includes(entry)) entry.body = { state: 'unavailable', reason: 'not_retained_by_browser' };
    }
  }
  publicRequest(state, item, includeBody = true) {
    return { ...item, url: state.redactor.text(item.url), headers: state.redactor.scrub(item.headers), ...(item.post_data ? { post_data: state.redactor.scrub(item.post_data) } : {}), ...(item.response ? { response: state.redactor.scrub(item.response) } : {}), body: { ...item.body, text: includeBody && item.body.text !== undefined ? state.redactor.scrub(item.body.text) : undefined } };
  }
  list(state, kind, params, targetOnly = false) {
    const limit = params.limit ?? 30;
    const retained = state[kind].filter(item => !targetOnly || item.target_ref === state.targetRef);
    const items = retained.filter(item => item.seq > (params.cursor ?? 0) && (!params.method || item.method === params.method));
    const selected = [];
    let bytes = 0;
    for (const item of items.slice(0, limit)) {
      const value = kind === 'network' ? this.publicRequest(state, item, false) : { ...item, ...(kind === 'events' ? { params: state.redactor.scrub(item.params, 0, true) } : { args: state.redactor.scrub(item.args, 0, true), details: state.redactor.scrub(item.details, 0, true) }) };
      const size = Buffer.byteLength(JSON.stringify(value));
      if (selected.length && bytes + size > BODY_LIMIT - 8192) break;
      selected.push(value); bytes += size;
    }
    return { items: selected, cursor: selected.at(-1)?.seq ?? params.cursor ?? 0, next_cursor: items.length > selected.length ? selected.at(-1).seq : null, oldest_cursor: retained[0]?.seq ?? null, evicted_before: state.evicted[kind], cursor_lost: (params.cursor ?? 0) > 0 && params.cursor < state.evicted[kind], limit };
  }
  async credentials(state) {
    // Read only this context's cookie jar, never a browser-wide CDP cookie API.
    try {
      const cookies = await deadline(this.session.context.cookies(), 500);
      for (const cookie of cookies) {
        state.redactor.remember(cookie.value);
        if (cookie.name && cookie.value) state.redactor.remember(`${cookie.name}=${cookie.value}`);
      }
    } catch {}
    if (state.detached || !safeURL(state.url)) return;
    try {
      const securityOrigin = new URL(state.url).origin;
      for (const isLocalStorage of [true, false]) {
        const result = await deadline(state.client.send('DOMStorage.getDOMStorageItems', { storageId: { securityOrigin, isLocalStorage } }), 500);
        for (const [key, value] of (result.entries ?? []).slice(0, 256)) if (SECRET.test(key)) state.redactor.remember(value);
      }
    } catch {}
  }
  async execute(a, epoch) {
    let state;
    try {
      const { method, params, target } = validateDevToolsAction(a);
      this.session.guard(epoch);
      const meta = this.session.page();
      state = await this.attach(meta);
      this.session.guard(epoch);
      if (target !== undefined) {
        const selected = state.family.targets.get(target);
        if (!selected || selected.detached) throw error('DEVTOOLS_TARGET_NOT_AVAILABLE');
        if (!selected.initialized || selected !== state.family.root && !selected.descriptor.initialized) throw error('DEVTOOLS_TARGET_NOT_READY');
        state = selected;
      }
      await this.credentials(state);
      this.session.guard(epoch);
      let result;
      if (method === 'help') {
        result = { methods: [...HELPERS], domains: [...DOMAINS], denied_methods: [...DENIED], limits: { targets: 32, body_bytes: BODY_LIMIT, total_body_bytes: BODY_TOTAL, network_entries: NETWORK_LIMIT, console_entries: CONSOLE_LIMIT, event_entries: EVENT_LIMIT }, notes: ['Raw target CDP may modify the page or worker. No automatic retries. Standard commands without target use the root page; pass a target_ref from targets.list to address its OOPIF or dedicated worker. Available CDP domains depend on target type.', 'Execution deadlines count active time only; a paused member of the tab family suspends in-flight work. Calls started while paused still have deadlines. Runtime.evaluate timeout has a 10-second maximum; 0 uses the default. Unknown outcomes block further actions until explicit cancellation closes the page.', 'Logs begin at attachment; missing earlier activity is not reconstructed. Lists aggregate the tab family unless target is supplied. Bounds are shared across that family. Lists return cursor for subsequent polling and next_cursor only when more retained items remain.', 'Read stored bodies with network.read request_id; reads never replay requests. Network.getResponseBody resolves requestId only in the selected target. Target refs expire on detach, cancellation or control changes.', 'Heap/Profiler event streams are bounded diagnostic fragments, not complete trace or heap artifacts. Artifact export is not implemented here.', 'Response redaction covers named fields and known literal credentials, not arbitrary encodings or exfiltration by page scripts.', 'No browser transport, host filesystem, cross-context targets, shared/service workers, request interception, or dialog bypass.'] };
      } else if (method === 'targets.list') {
        result = this.targets(state);
      } else if (method.endsWith('.list') && HELPERS.has(method)) {
        result = this.list(state, method.split('.')[0] === 'network' ? 'network' : method.split('.')[0] === 'console' ? 'console' : 'events', params, target !== undefined);
      } else if (method === 'network.read' || method === 'Network.getResponseBody') {
        const entry = state.network.find(item => method === 'network.read'
          ? item.id === params.request_id && (target === undefined || item.target_ref === state.targetRef)
          : item.target_ref === state.targetRef && item.cdp_request_id === params.requestId && state.byRequest.get(params.requestId) === item);
        if (method === 'network.read') {
          if (!entry) throw error('DEVTOOLS_REQUEST_NOT_RECORDED_OR_EVICTED');
          result = { request: this.publicRequest(state, entry) };
        }
        else result = entry && ['available', 'truncated'].includes(entry.body.state)
          ? { method, state: entry.body.state, result: { body: state.redactor.scrub(entry.body.text), base64Encoded: false }, bytes: entry.body.bytes, ...(entry.body.state === 'truncated' ? { limit: BODY_LIMIT } : {}) }
          : { method, state: entry?.body.state ?? 'unavailable', reason: entry?.body.reason ?? 'request_not_recorded_or_evicted' };
      } else {
        const evaluation = ['Runtime.evaluate', 'Runtime.callFunctionOn', 'Runtime.runScript', 'Debugger.evaluateOnCallFrame'].includes(method);
        if (state.family.paused && evaluation && method !== 'Debugger.evaluateOnCallFrame') throw error('DEVTOOLS_DEBUGGER_PAUSED');
        const activeCall = { method, evaluation };
        state.executing.add(activeCall);
        const maximum = Math.max(1, Math.min((method === 'Runtime.evaluate' ? params.timeout : 0) || this.session.actionTimeout || 10000, 10000));
        const callParams = { ...params };
        if (method === 'Runtime.evaluate') delete callParams.timeout;
        if (['Network.enable', 'Network.configureDurableMessages'].includes(method)) {
          callParams.maxTotalBufferSize = 4 * BODY_TOTAL;
          callParams.maxResourceBufferSize = BODY_TOTAL;
          if (method === 'Network.enable') callParams.maxPostDataSize = 8192;
        }
        try {
          const startedPaused = state.family.paused;
          const raw = await activeDeadline(state.client.send(method, callParams), state.family, maximum, startedPaused);
          await this.credentials(state);
          result = { method, ...state.redactor.bounded(raw, BODY_LIMIT, true) };
        } catch (cause) {
          const unknownOutcome = /DEVTOOLS_TIMEOUT|CDP_TARGET_(?:TIMEOUT|TRANSPORT_ERROR|DETACHED)|timed out|Execution was terminated/i.test(cause.message);
          if (unknownOutcome && !state.family.detached) { this.blocked.add(meta.page); await this.retire(state); }
          throw cause;
        } finally { state.executing.delete(activeCall); }
      }
      this.session.guard(epoch);
      if (state.detached || state.family.detached) throw error('DEVTOOLS_DETACHED');
      const data = { provenance: this.provenance(state), paused: state.family.paused, ...result };
      return { ok: true, data, url: state.redactor.text(meta.page.url()), title: state.redactor.text(meta.title ?? '') };
    } catch (cause) {
      const message = state ? state.redactor.text(cause.message ?? cause) : String(cause.message ?? cause);
      return { ok: false, error: clipped(message, 1000).text, data: { state: state && this.isBlocked(state.meta) ? 'result_unknown_cancel_required' : 'unavailable', retry: false, ...(state ? { provenance: this.provenance(state), paused: state.family.paused } : {}) } };
    }
  }

  removeTarget(state) {
    state.detached = true;
    state.paused = false;
    for (const [method, listener] of state.listeners) state.client?.off(method, listener);
    state.listeners.length = 0;
    state.family.targets.delete(state.targetRef);
    for (const kind of ['network', 'console', 'events']) {
      const entries = state[kind];
      for (let index = entries.length - 1; index >= 0; index--) {
        if (entries[index].target_ref !== state.targetRef) continue;
        const [removed] = entries.splice(index, 1);
        state.evicted[kind] = Math.max(state.evicted[kind], removed.seq);
        if (kind === 'network') state.family.bodyBytes -= removed.body.stored_bytes ?? 0;
      }
    }
    state.byRequest.clear(); state.frames.clear(); state.contexts.clear(); state.extras.clear();
  }

  prepareClose({ terminatePaused = true } = {}) {
    // Cancellation/DELETE destroy the owner before releasing its debugging
    // sessions. A normal detach/resume here could execute a paused business
    // mutation while the caller believes it is being cancelled.
    const stopping = [];
    for (const state of this.clients.values()) {
      const family = state.family;
      family.destroying = true;
      family.detached = true;
      for (const target of family.targets.values()) target.detached = true;
      family.tree?.prepareClose();
      if (terminatePaused && !family.stoppingPaused) {
        const paused = [...family.targets.values()].filter(target => target.paused && target.client);
        if (paused.length) family.stoppingPaused = Promise.all(paused.map(async target => {
          // Closing the owner alone can itself release a paused worker before
          // Chromium destroys it. Atomically terminate at the resume boundary;
          // never fall back to an ordinary resume or detach if this fails.
          try { await deadline(target.client.send('Debugger.resume', { terminateOnResume: true }, { cleanup: true, timeoutMs: 1000 }), 1000); }
          catch { if (!state.meta.page.isClosed()) throw error('DEVTOOLS_CANCEL_TERMINATION_FAILED'); }
        }));
      }
      if (family.stoppingPaused) stopping.push(family.stoppingPaused);
    }
    return stopping.length ? Promise.all(stopping) : undefined;
  }

  async retire(state) {
    const family = state.family;
    if (family.retiring) return family.retiring;
    family.detached = true;
    if (this.clients.get(state.meta.id)?.family === family) {
      if (!family.destroying) this.session.loseDocumentIdentity?.(state.meta);
      this.clients.delete(state.meta.id);
    }
    const targets = [...family.targets.values()];
    // Fence callbacks and activeDeadline immediately, before any async cleanup.
    for (const target of targets) target.detached = true;
    family.retiring = (async () => {
      await Promise.all(targets.map(async target => {
        const client = target.client;
        if (!client || family.destroying) return;
        if ([...target.executing].some(call => call.evaluation)) await deadline(client.send('Runtime.terminateExecution'), 500).catch(() => {});
        if (target.paused) await deadline(client.send('Debugger.resume'), 500).catch(() => {});
        await deadline(client.send('Debugger.disable'), 500).catch(() => {});
      }));
      for (const target of targets) this.removeTarget(target);
      if (family.tree) await deadline(family.tree.close(), 1500).catch(() => {});
      else if (family.root.client && !family.destroying) await deadline(family.root.client.detach(), 500).catch(() => {});
      family.network.length = family.console.length = family.events.length = 0;
      family.pending.clear(); family.bodyBytes = 0;
    })();
    return family.retiring;
  }
  async detach(meta) { const state = this.clients.get(meta?.id); if (state) await this.retire(state); }
  async reset() { await Promise.all([...this.clients.values()].map(state => this.retire(state))); }
  async close() { this.closed = true; await this.reset(); }
}
