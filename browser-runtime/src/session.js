import { createHash, randomBytes } from 'node:crypto';
import { BrowserActivity } from './activity.js';
import { BrowserPicker } from './picker.js';
import { captureDOMEvidence, installEvidenceProbe } from './evidence.js';
import { EvidenceStore } from './quantities.js';
import { BrowserDevTools, validateDevToolsAction } from './devtools.js';
import { BrowserFavicons } from './favicon.js';
import { installLinkNavigationPolicy, installLinkNavigationOnPage } from './link-navigation.js';
import { BrowserUploads, MAX_UPLOAD_FILES, UPLOAD_ID } from './uploads.js';

export class HTTPError extends Error {
  constructor(status, message, code) { super(code ? `${code}: ${message}` : message); this.status = status; if (code) this.code = code; }
}

async function bounded(promise, ms, message = 'browser operation timed out') {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(message)), ms);
    })]);
  } finally { clearTimeout(timer); }
}

function fail(error) { return { ok: false, error: String(error?.message ?? error).slice(0, 2000) }; }
function finite(value) { return typeof value === 'number' && Number.isFinite(value); }
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
// A slow site gets a full minute to navigate or satisfy a condition. Keep
// this separate from connection, cancellation and live-frame deadlines.
export const DEFAULT_ACTION_TIMEOUT_MS = 60_000;
export const MAX_WAIT_TIMEOUT_MS = 60_000;

// Requests that can carry an action's visible result: a new document (form
// submit, popup, iframe navigation) or script-issued fetch/XHR. Images, fonts,
// media, beacons and long-lived streams never hold an observation back.
const SETTLE_REQUEST_TYPES = new Set(['document', 'fetch', 'xhr']);
// Page-affecting agent actions whose result is observed after a bounded settle.
// Reads, explicit waits and tab switches report immediately.
const SETTLE_ACTIONS = new Set(['click', 'type', 'select', 'press', 'hover', 'scroll', 'navigate', 'upload']);
// These commands do not dispatch site mutations. An observation timeout must
// not be presented as an unknown click/submit that requires destructive reset.
const OBSERVATION_ACTIONS = new Set(['snapshot', 'find', 'extract', 'wait', 'screenshot', 'tabs', 'calculate']);
// A breakpoint can suspend an already-dispatched click/evaluation. These
// commands inspect or resume it without replacing its unresolved promise.
function debugRecovery(a) {
  return a.action === 'devtools' && (['help', 'targets.list', 'network.list', 'network.read', 'console.list', 'events.list', 'Runtime.getProperties'].includes(a.method) || a.method?.startsWith('Debugger.'));
}
function urlFor(value, base) {
  let url;
  try { url = new URL(value, base); } catch { throw new Error('navigate requires a valid http(s) URL'); }
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('only http(s) navigation is supported');
  return url.href;
}

export function validateAction(a, nested = false) {
  if (!a || typeof a !== 'object' || Array.isArray(a)) throw new Error('action must be an object');
  const name = a.action;
  const target = typeof a.target === 'string' && a.target.length > 0 && a.target.length <= 4096;
  if (name === 'batch') {
    if (nested || !Array.isArray(a.actions) || a.actions.length < 1 || a.actions.length > 12) {
      throw new Error('batch requires 1–12 non-batch actions');
    }
    a.actions.forEach(op => validateAction(op, true));
    return;
  }
  if (name === 'devtools') {
    if (nested) throw new Error('devtools must run separately, not inside a batch');
    validateDevToolsAction(a);
    return;
  }
  if (name === 'upload') {
    if (nested) throw new Error('upload must run separately, not inside a batch; inspect website parsing before continuing');
    if (!target || !['input', 'chooser', 'drop'].includes(a.upload_mode ?? 'input') ||
      !Array.isArray(a.files) || a.files.length < 1 || a.files.length > MAX_UPLOAD_FILES || new Set(a.files).size !== a.files.length ||
      a.files.some(id => typeof id !== 'string' || !UPLOAD_ID.test(id)) ||
      Object.keys(a).some(key => !['action', 'op_id', 'target', 'upload_mode', 'files', 'upload_binding'].includes(key))) throw new Error('upload requires target, input/chooser/drop mode and 1–8 unique staged file IDs; raw file paths are not accepted');
    if (a.upload_binding !== undefined) {
      const binding = a.upload_binding;
      if (!binding || typeof binding !== 'object' || Array.isArray(binding) ||
        Object.keys(binding).some(key => !['context_ref', 'tab_id', 'generation', 'control_epoch', 'document_version'].includes(key)) ||
        !/^ctx_[a-f0-9]{24}$/.test(binding.context_ref ?? '') || !/^t[1-9]\d{0,9}$/.test(binding.tab_id ?? '') ||
        !Number.isSafeInteger(binding.generation) || binding.generation < 1 || !Number.isSafeInteger(binding.control_epoch) || binding.control_epoch < 0 || !Number.isSafeInteger(binding.document_version) || binding.document_version < 1) throw new Error('upload_binding requires the complete internal current page identity');
    }
    return;
  }
  if (['eval', 'auth_status', 'api_read'].includes(name)) {
    throw new Error(`browser action retired: ${name}; use page actions and devtools in the current browser session`);
  }
  if (!['snapshot', 'find', 'click', 'type', 'navigate', 'wait', 'extract', 'scroll', 'select', 'press', 'hover', 'screenshot', 'tabs', 'tab', 'calculate'].includes(name)) {
    throw new Error(`unsupported browser action: ${name}`);
  }
  if (name === 'calculate') {
    const refs = a.quantity_refs;
    const allowed = new Set(['action', 'op_id', 'operation', 'quantity_refs']);
    if (Object.keys(a).some(key => !allowed.has(key))) throw new Error('calculate accepts only operation and observed quantity_refs; no raw values');
    if (!['compare', 'sort', 'difference', 'sum'].includes(a.operation) || !Array.isArray(refs) || refs.length < 1 || refs.length > 16 || new Set(refs).size !== refs.length || refs.some(ref => typeof ref !== 'string' || !/^[A-Za-z0-9_.:-]{1,160}$/.test(ref))) throw new Error('calculate requires a supported operation and 1–16 unique observed quantity_refs');
    if (['compare', 'difference'].includes(a.operation) && refs.length !== 2) throw new Error('compare/difference require exactly two observed quantity_refs');
  }
  if (['type', 'extract', 'select', 'hover'].includes(name) && !target) throw new Error(`${name} requires target`);
  if (name === 'click' && !target && (!(finite(a.x) && finite(a.y)) || typeof a.version !== 'string')) throw new Error('coordinate click requires numeric x/y and screenshot version');
  if (name === 'click' && target && (a.x !== undefined || a.y !== undefined)) throw new Error('click cannot combine target and coordinates');
  if (name === 'navigate' && (typeof a.url !== 'string' || !a.url)) throw new Error('navigate requires url');
  if (name === 'press' && !(a.key || a.text)) throw new Error('press requires key');
  if (name === 'find' && !['role', 'name', 'text', 'placeholder', 'label', 'testid'].some(k => a[k])) throw new Error('find requires a query');
  if (name === 'wait' && !['text', 'gone_text', 'url', 'target'].some(k => a[k])) throw new Error('wait requires a condition');
  if (name === 'wait' && a.seconds !== undefined && (!finite(a.seconds) || a.seconds < 0 || a.seconds * 1000 > MAX_WAIT_TIMEOUT_MS)) throw new Error('wait seconds must be between 0 and 60; 0 uses the default');
  if (name === 'tab' && a.tab_action !== undefined && !['new', 'switch', 'close', 'claim'].includes(a.tab_action)) throw new Error('tab requires tab_action new, switch, close or claim');
  if (name === 'tab' && a.tab_action === 'claim') {
    if (nested) throw new Error('tab claim must run separately; inspect the claimed page before acting');
    if (!target || !/^t[1-9]\d{0,9}$/.test(a.target) || typeof a.context_ref !== 'string' || !/^ctx_[a-f0-9]{24}$/.test(a.context_ref) || !Number.isSafeInteger(a.control_epoch) || a.control_epoch < 0 ||
      Object.keys(a).some(key => !['action', 'tab_action', 'target', 'context_ref', 'control_epoch', 'op_id'].includes(key))) throw new Error('tab claim requires target, context_ref and control_epoch from current tabs');
  }
  // The executor is the last boundary: a missing text must not become fill('')
  // and silently clear a field. An explicit empty string still clears.
  if (['type', 'select'].includes(name) && typeof a.text !== 'string') {
    throw new Error(name === 'type' ? 'type requires text (a string; "" clears the field)' : 'select requires text (the option value or label)');
  }
}

export class BrowserSession {
  constructor(context, options = {}) {
    this.context = context;
    this.internalPages = options.internalPages instanceof Set ? options.internalPages : new Set(options.internalPages ?? []);
    this.mediaBroker = options.mediaBroker;
    this.deviceScaleFactor = options.deviceScaleFactor ?? 1;
    this.captureDeviceScaleFactor = options.captureDeviceScaleFactor ?? 1;
    if (!Number.isFinite(this.captureDeviceScaleFactor) || this.captureDeviceScaleFactor < 1 || this.captureDeviceScaleFactor > 4) throw new Error('invalid compositor capture scale');
    // Managed contexts must also scope events, init scripts, cookies and CDP to
    // this session. The host is the sole owner of page/context destruction;
    // none of its missing or failed operations may fall back to raw context.
    this.host = options.host;
    if (this.host !== undefined) {
      for (const method of ['ownsPage', 'newPage', 'closePage', 'setOffline', 'close']) {
        if (typeof this.host?.[method] !== 'function') throw new Error(`managed browser host requires ${method}`);
      }
      if (this.host.bindSession !== undefined && typeof this.host.bindSession !== 'function') throw new Error('managed browser host bindSession must be a function');
      if (this.host.getViewport !== undefined && typeof this.host.getViewport !== 'function') throw new Error('managed browser host getViewport must be a function');
    }
    this.contextRef = `ctx_${randomBytes(12).toString('hex')}`;
    this.contextDestroyed = false;
    this.control = 'agent';
    this.controlEpoch = 0;
    this.phase = 'ready';
    this.generation = 1;
    this.lease = null;
    this.active = null;
    this.pages = new Map();
    this.pageOwners = new WeakMap();
    this.refs = new Map();
    this.nextRef = 1;
    this.nextTab = 1;
    this.nextObservation = 1;
    this.quantities = new EvidenceStore({ maxObservations: 32, maxQuantitiesPerObservation: 100 });
    this.marker = `data-pogo-${randomBytes(8).toString('hex')}`;
    this.revisionKey = `__pogoRevision_${randomBytes(12).toString('hex')}`;
    this.evidenceKey = `__pogoEvidence_${randomBytes(12).toString('hex')}`;
    this.pointerKey = `__pogoPointer_${randomBytes(12).toString('hex')}`;
    this.activity = new BrowserActivity(this);
    this.uploads = new BrowserUploads(options.uploads);
    this.picker = new BrowserPicker(this, HTTPError);
    this.queue = Promise.resolve();
    this.operations = new Map();
    this.tabOperations = new Map();
    this.cachedTabIDs = [];
    // Pages per session: 0 means no cap — the container's own memory and pids
    // budgets are the practical ceiling. POGO_BROWSER_MAX_PAGES reinstates a
    // hard cap for deployments that want one.
    this.maxPages = options.maxPages ?? 0;
    this.cachedIDs = [];
    this.rawAction = null;
    this.observationReads = new Map();
    this.frameFlights = new Map();
    this.liveViews = new Set();
    this.liveActions = new Map();
    this.liveQueues = new Map();
    this.currentOperation = null;
    this.dialog = null;
    this.dialogWake = null;
    this.resetPromise = null;
    this.cancelPromise = null;
    this.lastScreenshotVersion = null;
    this.lastUsed = Date.now();
    this.actionTimeout = options.actionTimeout ?? DEFAULT_ACTION_TIMEOUT_MS;
    this.devtools = new BrowserDevTools(this);
    this.favicons = new BrowserFavicons(this);
    // After an agent action, wait up to this long for newly observed requests
    // in its page scope, then observe once. Bounded inside the action timeout.
    this.settleMs = Math.max(0, Math.min(options.settleMs ?? 2000, this.actionTimeout / 2));
    this.requestSeq = 0;
    this.inflight = new Map();
    this.settleScope = null;
    this.closed = false;
    this.inOperation = false;
    context.setDefaultTimeout(this.actionTimeout);
    context.setDefaultNavigationTimeout(this.actionTimeout);
    context.on('page', page => {
      if (this.mediaBroker?.observePage) this.mediaBroker.observePage(page, observed => this.attach(observed));
      else this.attach(page);
    });
    context.on('request', request => this.trackRequest(request));
    // A fetch/XHR counts as answered once its response arrives: Chromium may
    // never report a request finished when the page does not read its body
    // (fire-and-forget pings). Documents are parsed, so they wait for finish.
    context.on('response', response => {
      try { if (response.request().resourceType() !== 'document') this.inflight.delete(response.request()); } catch {}
    });
    context.on('requestfinished', request => this.inflight.delete(request));
    context.on('requestfailed', request => {
      this.inflight.delete(request);
      try {
        const meta = [...this.pages.values()].find(entry => entry.navigationRequest === request);
        // The attached CDP lifecycle has request/frame/loader identity and
        // correlates downloads. A second Playwright abort must not overwrite
        // its confirmed download outcome (or finish a newer navigation).
        if (meta && !this.devtools.clients.has(meta.id)) this.finishPageLoad(meta, /ERR_ABORTED/.test(request.failure()?.errorText ?? '') ? 'Page navigation was cancelled.' : 'Page navigation failed.');
      } catch {}
    });
    context.on('close', () => {
      this.uploads.close().catch(() => {});
      this.favicons.close();
      this.contextDestroyed = true;
      this.closed = true;
      this.generation++;
      this.fenceLiveInput().catch(() => {});
      this.quantities.clear();
      this.devtools.prepareClose({ terminatePaused: false });
      this.devtools.close().catch(() => {});
    });
  }

  trackRequest(request) {
    let frame;
    try {
      if (!SETTLE_REQUEST_TYPES.has(request.resourceType()) || request.serviceWorker?.()) return;
      frame = request.frame();
      if (!this.ownsPage(frame.page())) return;
      if (request.isNavigationRequest() && frame === frame.page().mainFrame()) {
        const meta = [...this.pages.values()].find(entry => entry.page === frame.page());
        if (meta) {
          if (!this.devtools.clients.has(meta.id)) this.startPageLoad(meta, { newCycle: true });
          meta.navigationRequest = request;
        }
      }
      // A navigation replaces the frame's document. Requests the old document
      // left behind may never report completion, so they stop counting here.
      if (request.isNavigationRequest()) this.dropRequests(entry => entry.frame === frame);
    } catch { return; }
    // Requests that never finish (long polls) must not grow without bound.
    if (this.inflight.size >= 512) this.inflight.delete(this.inflight.keys().next().value);
    this.inflight.set(request, { seq: ++this.requestSeq, at: Date.now(), frame, page: frame.page() });
  }

  dropRequests(matches) {
    for (const [request, entry] of this.inflight) if (matches(entry)) this.inflight.delete(request);
  }

  pendingSince(scope) {
    let count = 0;
    const stale = Date.now() - 120_000;
    for (const [request, entry] of this.inflight) {
      if (entry.at < stale) this.inflight.delete(request);
      else if (entry.seq > scope.startSeq && scope.pages.has(entry.page)) count++;
    }
    return count;
  }

  // Scope is a page boundary, not proof of causality: timer-driven work in the
  // same page can still count. Existing background tabs and their popups cannot.
  recordPopup(opener, popup) {
    const meta = [...this.pages.values()].find(meta => meta.page === popup);
    if (!meta || meta.popupRecorded) return;
    meta.opener = opener;
    // Host/Chrome attribution can arrive after explicit initialization or a
    // claim. It can describe the relationship, never undo completed ownership
    // or move the execution target back to a delayed popup.
    if (!meta.ownerInherited && meta.controlEpoch > 0) {
      meta.ownerInherited = true; meta.ownershipPending = false; meta.popupRecorded = true;
      return;
    }
    if (!meta.ownerInherited) {
      const parent = this.pageOwners.get(opener);
      if (parent?.ownershipPending) return;
      const owner = parent ? parent.controlTransitionPrevious ?? this.controlFor(parent) : { mode: 'human', lease: null, epoch: 0, phase: 'ready' };
      meta.control = owner.mode; meta.lease = owner.lease; meta.controlEpoch = owner.epoch;
      // Only the opener is transitioning. Its new child has no transition
      // promise, so copying "takeover" would leave that child stuck forever.
      meta.controlPhase = 'ready'; meta.ownershipPending = false; meta.ownerInherited = true;
      if (owner.mode === 'human' && owner.lease) {
        meta.inheritedHumanLease = owner.lease; meta.inheritedControlEpoch = owner.epoch;
      }
    }
    meta.popupRecorded = true;
    // A human-created popup inherits the human lease and never becomes the
    // Agent target, even if its opener happened to be the previous target.
    if (this.controlFor(meta).mode !== 'agent') return;
    const scope = this.settleScope;
    if (!scope || scope.epoch !== this.generation) {
      if (opener === this.pages.get(this.active)?.page) this.active = meta.id;
      return;
    }
    // A child popup event can arrive before its parent's opener was recorded.
    // Walk to a fixed point so only new descendants of the action page join.
    let changed;
    do {
      changed = false;
      for (const candidate of this.pages.values()) {
        if (!scope.before.has(candidate.page) && !scope.pages.has(candidate.page) && scope.pages.has(candidate.opener)) {
          scope.pages.add(candidate.page);
          this.currentOperation?.pages.add(candidate);
          this.currentOperation?.authority.set(candidate, this.controlFor(candidate).epoch);
          this.active = candidate.id;
          changed = true;
        }
      }
    } while (changed);
  }

  // Wait for related-page requests observed after the action began and a short
  // quiet window, bounded by deadline. Nothing is retried. Generation changes
  // fence this observation just as they fence the underlying action.
  async settle(scope, quietMs, deadline) {
    let quietSince = Date.now();
    for (;;) {
      if (this.dialog || this.closed || scope.epoch !== this.generation) break;
      const pending = this.pendingSince(scope);
      const now = Date.now();
      if (pending) quietSince = now;
      else if (now - quietSince >= quietMs) break;
      if (now >= deadline) break;
      await sleep(Math.min(25, deadline - now));
    }
  }

  async settleAfter(scope, last, until) {
    const started = Date.now();
    const deadline = Math.min(started + this.settleMs, until);
    // A batch's earlier steps only wait for work they visibly started; the
    // final observation also allows a short quiet window for late handlers.
    await this.settle(scope, last ? 150 : 40, deadline);
    const loaded = new Set();
    while (scope.epoch === this.generation && !this.dialog && !this.closed) {
      const active = this.pages.get(this.active);
      const existingNavigation = active?.page === scope.sourcePage && (active.version !== scope.sourceVersion || active.pageLoading);
      if (!active || scope.before.has(active.page) && !existingNavigation || !scope.pages.has(active.page) || loaded.has(active.page)) break;
      // A new document can arrive in this Page or a popup. Its document request
      // can finish before a parser-blocking script has completed the DOM.
      const remaining = Math.max(0, deadline - Date.now());
      if (!remaining) break;
      loaded.add(active.page);
      await active.page.waitForLoadState('domcontentloaded', { timeout: remaining }).catch(() => {});
    }
    // A popup can open another popup while parsing. Report the final live set,
    // including descendants discovered during the bounded load-state wait.
    const opened = [...this.pages.values()].filter(meta => !scope.before.has(meta.page) && scope.pages.has(meta.page));
    const pending = this.pendingSince(scope);
    return {
      settle_ms: Date.now() - started,
      ...(pending ? { pending_requests: pending } : {}),
      ...(opened.length ? { opened_tabs: opened.map(meta => ({ id: meta.id, url: meta.page.url() })), from_tab: scope.fromTab } : {}),
    };
  }

  async init() {
    await this.picker.init();
    await this.context.addInitScript(installLinkNavigationPolicy);
    // Install the fixed collector before site scripts can replace DOM APIs.
    // Source is owned by this module, never supplied by a page or model.
    await this.context.addInitScript({ content: `(${installEvidenceProbe.toString()})({key:${JSON.stringify(this.evidenceKey)},capture:(${captureDOMEvidence.toString()})});` });
    await this.context.addInitScript(({ key, marker, pointerKey }) => {
      const invoke = Function.prototype.call.bind(Function.prototype.call);
      const apply = Reflect.apply, resolvePromise = Promise.resolve.bind(Promise), then = Promise.prototype.then;
      const createWalker = Document.prototype.createTreeWalker.bind(document);
      const nextNode = TreeWalker.prototype.nextNode;
      const shadowRoot = Object.getOwnPropertyDescriptor(Element.prototype, 'shadowRoot').get;
      const isConnected = Object.getOwnPropertyDescriptor(Node.prototype, 'isConnected').get;
      const rootHost = Object.getOwnPropertyDescriptor(ShadowRoot.prototype, 'host').get;
      const animations = Document.prototype.getAnimations.bind(document);
      const playState = Object.getOwnPropertyDescriptor(Animation.prototype, 'playState').get;
      const Observer = MutationObserver, observe = MutationObserver.prototype.observe, takeRecords = MutationObserver.prototype.takeRecords, disconnect = MutationObserver.prototype.disconnect;
      const addListener = EventTarget.prototype.addEventListener, removeListener = EventTarget.prototype.removeEventListener;
      const observed = new WeakSet(), observers = new Map(), knownRoots = new WeakMap();
      const animationIDs = new WeakMap();
      let nextAnimation = 0, animationSignature = '', revision = 0;
      let revisionCovered = true, needsFullScan = false, hooksCovered = true;
      const bump = () => { revision++; };
      const events = ['scroll', 'animationstart', 'animationend', 'animationcancel', 'transitionrun', 'transitionend', 'transitioncancel', 'load', 'error'];
      const processRecords = (records, budget = { left: 20000 }) => {
        if (records.some(record => record.type !== 'attributes' || record.attributeName !== marker)) revision++;
        for (const record of records) {
          if (record.type !== 'childList') continue;
          // Existing-host declarative roots can appear without an added host.
          if (record.target?.nodeType === 1) discoverRoot(record.target, budget, false);
          for (const node of record.addedNodes) {
            if (!invoke(isConnected, node)) continue;
            if (node.nodeType === 1 || node.nodeType === 11) discoverRoot(node, budget);
            if (budget.left < 0) break;
          }
          if (budget.left < 0) break;
        }
        if (budget.left < 0) { needsFullScan = true; revisionCovered = false; }
      };
      const observeRoot = root => {
        if (observed.has(root)) return;
        if (observers.size >= 512) { needsFullScan = true; revisionCovered = false; return; }
        observed.add(root);
        if (root !== document) revision++;
        const observer = new Observer(records => processRecords(records));
        observers.set(root, observer);
        invoke(observe, observer, root, { subtree: true, attributes: true, childList: true, characterData: true });
        // Scroll/animation events within Shadow DOM need a listener on that
        // root: non-composed events cannot reach the document listener.
        for (const event of events) invoke(addListener, root, event, bump, true);
      };
      const inspectHost = (el, budget) => {
        if (--budget.left < 0) return;
        const root = invoke(shadowRoot, el) ?? knownRoots.get(el);
        if (root) { observeRoot(root); discoverRoot(root, budget); }
      };
      const discoverRoot = (root, budget, children = true) => {
        if (root === document || root.nodeType === 11) observeRoot(root);
        if (root.nodeType === 1) inspectHost(root, budget);
        if (!children || budget.left < 0) return;
        const walker = createWalker(root, NodeFilter.SHOW_ELEMENT);
        let node;
        while (budget.left >= 0 && (node = invoke(nextNode, walker))) inspectHost(node, budget);
      };
      const flushRecords = () => {
        for (const [root, observer] of observers) {
          if (root === document || invoke(isConnected, root)) continue;
          invoke(disconnect, observer);
          for (const event of events) invoke(removeListener, root, event, bump, true);
          observers.delete(root); observed.delete(root);
        }
        const budget = { left: 20000 };
        for (const observer of observers.values()) {
          processRecords(invoke(takeRecords, observer), budget);
          if (budget.left < 0) break;
        }
      };
      const wrapMethod = (prototype, name, after) => {
        const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
        if (typeof descriptor?.value !== 'function') return;
        const original = descriptor.value;
        try { Object.defineProperty(prototype, name, { ...descriptor, value: function(...args) {
          const result = apply(original, this, args);
          after(result, this, args); return result;
        } }); } catch { hooksCovered = false; }
      };
      // A root attached to an existing host creates no light-DOM mutation.
      // Preserve the original return value/throw behavior while observing it.
      wrapMethod(Element.prototype, 'attachShadow', root => {
        knownRoots.set(invoke(rootHost, root), root);
        revision++; observeRoot(root);
        const budget = { left: 20000 }; discoverRoot(root, budget);
        if (budget.left < 0) { needsFullScan = true; revisionCovered = false; }
      });
      // CSSOM mutators do not necessarily produce MutationObserver records.
      // These hooks conservatively dirty the revision without reading content.
      for (const name of ['insertRule', 'deleteRule', 'addRule', 'removeRule', 'replaceSync']) wrapMethod(CSSStyleSheet.prototype, name, bump);
      wrapMethod(CSSStyleSheet.prototype, 'replace', result => { bump(); invoke(then, resolvePromise(result), bump, bump); });
      for (const name of ['setProperty', 'removeProperty']) wrapMethod(CSSStyleDeclaration.prototype, name, bump);
      const cssText = Object.getOwnPropertyDescriptor(CSSStyleDeclaration.prototype, 'cssText');
      if (cssText?.set) {
        try { Object.defineProperty(CSSStyleDeclaration.prototype, 'cssText', { ...cssText, set(value) { invoke(cssText.set, this, value); bump(); } }); }
        catch { hooksCovered = false; }
      }
      const animationRevision = () => {
        const active = animations();
        if (active.length > 256) revisionCovered = false;
        const signature = active.slice(0, 256).map(animation => {
          if (!animationIDs.has(animation)) animationIDs.set(animation, ++nextAnimation);
          return `${animationIDs.get(animation)}:${invoke(playState, animation)}`;
        }).sort().join(',');
        if (signature !== animationSignature) { animationSignature = signature; revision++; }
      };
      const readRevision = () => {
        flushRecords();
        const budget = { left: 20000 };
        discoverRoot(document, budget);
        needsFullScan = budget.left < 0;
        revisionCovered = !needsFullScan && hooksCovered && observers.size < 512;
        animationRevision();
        return revision;
      };
      // Preview reads only accumulated mutations/new subtrees and bounded
      // animation state. Strict frame/commit reads retain the complete scan.
      const fastRevision = () => {
        flushRecords();
        revisionCovered = revisionCovered && !needsFullScan && hooksCovered;
        animationRevision();
        return revision;
      };
      readRevision();
      invoke(addListener, globalThis, 'resize', bump, true);
      document.fonts?.addEventListener('loadingdone', bump);
      document.fonts?.addEventListener('loadingerror', bump);
      Object.defineProperty(readRevision, 'covered', { value: () => revisionCovered });
      Object.defineProperty(readRevision, 'fast', { value: fastRevision });
      Object.defineProperty(readRevision, 'observes', { value: root => observed.has(root) });
      Object.defineProperty(globalThis, key, { value: readRevision, configurable: false, writable: false });
      let pointer = null, pointerSeq = 0;
      const observePointer = event => {
        if (!event.isTrusted) return;
        pointer = { x: event.clientX, y: event.clientY, kind: event.type === 'pointerdown' ? 'click' : 'move', seq: ++pointerSeq, at: Date.now() };
      };
      addEventListener('pointermove', observePointer, { capture: true, passive: true });
      addEventListener('pointerdown', observePointer, { capture: true, passive: true });
      Object.defineProperty(globalThis, pointerKey, { value: () => pointer, configurable: false, writable: false });
    }, { key: this.revisionKey, marker: this.marker, pointerKey: this.pointerKey });
    const initialPage = await this.createPage();
    this.active = [...this.pages.values()].find(meta => meta.page === initialPage)?.id ?? null;
    await this.page().devtoolsReady;
    return this;
  }
  touch() { this.lastUsed = Date.now(); }
  // Legacy internal accessors describe only the Agent-selected tab. Every
  // externally bound operation must use controlFor(meta), never these aliases.
  get control() { return this.pages?.get(this.active)?.control ?? this.initialControl ?? 'agent'; }
  set control(value) { const meta = this.pages?.get(this.active); if (meta) meta.control = value; else this.initialControl = value; }
  get lease() { return this.pages?.get(this.active)?.lease ?? this.initialLease ?? null; }
  set lease(value) { const meta = this.pages?.get(this.active); if (meta) meta.lease = value; else this.initialLease = value; }
  get controlEpoch() { return this.pages?.get(this.active)?.controlEpoch ?? this.initialControlEpoch ?? 0; }
  set controlEpoch(value) { const meta = this.pages?.get(this.active); if (meta) meta.controlEpoch = value; else this.initialControlEpoch = value; }
  controlFor(meta) {
    return { mode: meta?.control ?? this.initialControl ?? 'agent', lease: meta?.lease ?? this.initialLease ?? null,
      epoch: meta?.controlEpoch ?? this.initialControlEpoch ?? 0, phase: meta?.controlPhase ?? 'ready' };
  }
  operationTouches(meta) { return Boolean(meta && this.currentOperation?.pages.has(meta)); }
  contextBlocked(ownedAction) {
    return [...this.pages.values()].some(meta => meta.liveInputBlocked || this.devtools.isBlocked(meta)) ||
      [...this.liveActions.values()].some(action => action.uncertain) || Boolean(this.rawAction && this.rawAction !== ownedAction && !this.currentOperation && !this.dialog);
  }
  beginOperation(meta, mode) {
    let finish;
    const operation = { mode, pages: new Set(meta ? [meta] : []), authority: new Map(meta ? [[meta, this.controlFor(meta).epoch]] : []), done: new Promise(resolve => { finish = resolve; }) };
    operation.finish = () => { if (this.currentOperation === operation) this.currentOperation = null; finish(); };
    this.currentOperation = operation; return operation;
  }
  invalidatePage(meta) {
    meta.visual++;
    this.lastScreenshotVersion = null;
    for (const [ref, value] of this.refs) if (value.page === meta) this.refs.delete(ref);
  }
  requireAgentPage(meta) {
    const control = this.controlFor(meta);
    if (meta && (control.mode !== 'agent' || control.phase !== 'ready' || meta.ownershipPending)) throw new HTTPError(409,
      `tab ${meta.id} is under human control or changing ownership; use tabs, then explicitly claim the relevant page with tab_action:claim and its context_ref/control_epoch, switch to an agent-owned tab, or use tab_action:new for a separate task. Page actions never reclaim a human tab implicitly`, 'TAB_HUMAN_CONTROL');
  }
  ownsPage(page) {
    if (this.internalPages.has(page)) return false;
    if (!this.host) return true;
    try { return this.host.ownsPage(page) === true; } catch { return false; }
  }
  async createPage() {
    const epoch = this.generation;
    const page = await (this.host ? this.host.newPage() : this.context.newPage());
    if (!this.ownsPage(page)) throw new HTTPError(503, 'managed browser host returned a page outside this session');
    if (this.closed || epoch !== this.generation) {
      await this.closePage(page);
      throw new HTTPError(409, 'managed page creation was fenced by browser control change');
    }
    // Native hosts may finish registering ownership after the transport's page
    // event. Attach exactly once when the owned Page is returned to us.
    if (![...this.pages.values()].some(meta => meta.page === page)) this.attach(page);
    const meta = [...this.pages.values()].find(meta => meta.page === page);
    await meta?.ownershipReady;
    // Unattributed context events stay human. Only a Page returned by this
    // explicit creation may be promoted; a concurrent takeover still wins.
    if (meta && !meta.ownerInherited && meta.controlEpoch === 0 && meta.controlPhase === 'ready') meta.control = 'agent';
    await meta?.linkNavigationReady;
    await meta?.faviconReady;
    return page;
  }
  async closePage(page) {
    if (!this.ownsPage(page)) throw new HTTPError(409, 'browser page no longer belongs to this session');
    if (this.host) await this.host.closePage(page);
    else await page.close({ runBeforeUnload: false });
  }
  async setContextOffline(offline) {
    if (this.host) await this.host.setOffline(offline);
    else await this.context.setOffline(offline);
  }
  async destroyContext() {
    if (this.host) await this.host.close();
    else await this.context.close();
    this.contextDestroyed = true;
  }
  viewport(meta = this.pages.get(this.active)) {
    if (!meta) return { width: 1280, height: 800 };
    const value = this.host?.getViewport ? this.host.getViewport(meta.page ?? meta) : meta.page.viewportSize?.() ?? { width: 1280, height: 800 };
    if (!value || !['width', 'height'].every(key => Number.isInteger(value[key]) && value[key] > 0 && value[key] <= 8192)) {
      throw new HTTPError(503, 'browser returned an invalid viewport');
    }
    return { width: value.width, height: value.height };
  }
  assertLive() { if (this.closed) throw new HTTPError(410, 'browser session was closed; create a new session', 'BROWSER_SESSION_EXPIRED'); }
  page() {
    this.assertLive();
    const meta = this.pages.get(this.active);
    if (!meta || meta.page.isClosed()) {
      if (this.phase === 'ready') throw new HTTPError(409, this.pages.size ? 'no active browser tab; use tabs then tab to select a page before page actions' : 'no browser tabs; use tab with tab_action new before page actions');
      throw new HTTPError(409, 'browser page is resetting; retry state after cancellation acknowledgement');
    }
    return meta;
  }

  selectedPage(binding = {}) {
    const { tab_id, context_ref } = binding;
    this.assertLive();
    if (tab_id === undefined && context_ref === undefined) return this.page();
    if (typeof tab_id !== 'string' || !/^t[1-9]\d{0,9}$/.test(tab_id) || typeof context_ref !== 'string' || !/^ctx_[a-f0-9]{24}$/.test(context_ref)) throw new HTTPError(400, 'tab selection requires tab_id and context_ref');
    if (context_ref !== this.contextRef) throw new HTTPError(409, 'browser context changed; this saved tab binding is no longer current', 'BROWSER_CONTEXT_CHANGED');
    const meta = this.pages.get(tab_id);
    if (!meta || meta.page.isClosed()) throw new HTTPError(410, 'bound browser tab is closed', 'BROWSER_TAB_CLOSED');
    return meta;
  }

  attach(page) {
    if (this.closed || !this.ownsPage(page)) return null;
    const existing = [...this.pages.values()].find(meta => meta.page === page);
    if (existing) return existing;
    const id = `t${this.nextTab++}`;
    const meta = { id, page, title: '', favicon: '', version: 1, topDocumentVersion: 1, documentLoaderId: null, documentIdentityReady: false, visual: 1, revision: '', control: 'human', lease: null, controlEpoch: 0, controlPhase: 'ready',
      ownershipPending: true, pageLoading: /^https?:/.test(page.url()), pageLoadError: '', pageLoadEpoch: 0 };
    this.pages.set(id, meta);
    this.pageOwners.set(page, meta);
    meta.linkNavigationPending = true;
    meta.linkNavigationReady = (typeof page.addInitScript === 'function' && typeof page.frames === 'function'
      ? bounded(installLinkNavigationOnPage(page), 1500).catch(() => {}) : Promise.resolve()).finally(() => { meta.linkNavigationPending = false; });
    meta.ownershipReady = Promise.resolve().then(() => page.opener?.()).then(async opener => {
      if (opener) {
        await this.pageOwners.get(opener)?.ownershipReady;
        this.recordPopup(opener, page);
      } else { meta.ownershipPending = false; }
    }).catch(() => { meta.control = 'human'; meta.ownershipPending = true; });
    // Attach before explicit navigation so early requests and exceptions can
    // be observed. Popups may already have started loading: never claim those
    // missing events were captured. Basic browsing survives a CDP failure.
    meta.devtoolsReady = this.devtools.attach(meta).catch(error => { meta.devtoolsError = String(error.message); });
    meta.faviconReady = this.favicons.attach(meta).catch(() => {});
    // During an agent action, wait for a related opener before activating a
    // popup. A background tab must not replace the action's observation.
    // Creation alone does not choose the Agent target. init/tab.new explicitly
    // select their returned page, and actual popups use opener attribution.
    page.on('popup', popup => this.recordPopup(page, popup));
    page.on('framenavigated', frame => {
      meta.version++;
      // Query Chromium's real per-tab session history after top-level and SPA
      // navigation. Never infer it from URLs or evaluate website JavaScript.
      if (frame === page.mainFrame()) this.refreshNavigationHistory(meta);
      // Playwright also emits this event for pushState, replaceState and hash
      // changes. Only the root CDP loader commits a new document identity.
      if (frame === page.mainFrame()) this.favicons.navigated(meta);
      if (this.active === id) this.lastScreenshotVersion = null;
      for (const [ref, value] of this.refs) if (value.page === meta) this.refs.delete(ref);
      bounded(page.title(), 800).then(title => { meta.title = title; }).catch(() => {});
    });
    // Popups can be reported after loading began (or already finished). This
    // checks only Chromium's existing load state, without evaluating site JS.
    const initialDocument = meta.topDocumentVersion, initialLoadEpoch = meta.pageLoadEpoch;
    page.waitForLoadState('load', { timeout: 1 }).then(() => {
      if (meta.topDocumentVersion === initialDocument && meta.pageLoadEpoch === initialLoadEpoch) this.finishPageLoad(meta, '', initialLoadEpoch);
    }).catch(() => {});
    page.on('framedetached', frame => this.dropRequests(entry => entry.frame === frame));
    page.on('close', () => {
      this.favicons.detach(meta);
      this.devtools.detach(meta).catch(() => {});
      this.dropRequests(entry => { try { return entry.frame.page() === page; } catch { return true; } });
      this.pages.delete(id);
      for (const [ref, value] of this.refs) if (value.page === meta) this.refs.delete(ref);
      if (this.dialog?.page === page) this.dialog = null;
      if (this.active === id) this.active = [...this.pages.values()].find(meta => this.controlFor(meta).mode === 'agent')?.id ?? null;
    });
    // Registering a listener deliberately disables Playwright's auto-dismiss.
    // A confirm/prompt is a human decision, never an automatic acceptance.
    page.on('dialog', dialog => {
      this.dialog = { value: dialog, page, type: dialog.type(), message: dialog.message(), default_value: dialog.defaultValue() };
      this.dialogWake?.();
    });
    return meta;
  }

  startPageLoad(meta, { newCycle = false } = {}) {
    if (this.pages.get(meta.id) !== meta || this.closed) return;
    if (newCycle || !meta.pageLoading) {
      meta.pageLoadEpoch++;
      meta.navigationRequest = null;
      meta.loadingLoaderId = null;
      meta.pageLoadStartDocument = meta.topDocumentVersion;
      meta.pageLoadFailureNavigation = null;
    }
    meta.pageLoading = true;
    meta.pageLoadError = '';
  }

  finishPageLoad(meta, error = '', epoch = meta.pageLoadEpoch) {
    if (this.pages.get(meta.id) !== meta || this.closed || epoch !== meta.pageLoadEpoch) return;
    meta.pageLoading = false;
    if (error) { meta.pageLoadError = error; meta.pageLoadFailureNavigation = null; }
    this.publishLiveState(meta);
  }

  trackPageNavigation(meta, { frameId, url, requestId }) {
    if (this.pages.get(meta.id) !== meta || this.closed || !frameId || !requestId || typeof url !== 'string') return;
    // Keep only this retained document's attempts. Aborted attempts stay in
    // order: a late download of the same URL cannot bless a later navigation.
    const entries = (meta.downloadNavigations ?? []).filter(entry => entry.document === meta.topDocumentVersion && !entry.confirmedDownload);
    const existing = entries.find(entry => entry.requestId === requestId && entry.epoch === meta.pageLoadEpoch);
    const key = createHash('sha256').update(url).digest('hex');
    if (existing) { existing.urlKey = key; existing.frameId = frameId; }
    else entries.push({ requestId, frameId, urlKey: key, epoch: meta.pageLoadEpoch, document: meta.topDocumentVersion });
    // Fail closed if a single retained page accumulates an exceptional number
    // of unresolved navigations; never evict an old attempt and misattribute it.
    if (entries.length > 128) { meta.downloadNavigationOverflowDocument = meta.topDocumentVersion; entries.shift(); }
    meta.downloadNavigations = entries;
  }

  failPageNavigation(meta, { requestId, errorText, canceled, epoch = meta.pageLoadEpoch }) {
    if (this.pages.get(meta.id) !== meta || this.closed || epoch !== meta.pageLoadEpoch) return;
    const entry = meta.downloadNavigations?.find(entry => entry.requestId === requestId && entry.epoch === epoch);
    if (entry) {
      entry.failed = true; entry.aborted = errorText === 'net::ERR_ABORTED';
      if (entry.aborted && entry.confirmedDownload && entry.document === meta.topDocumentVersion) {
        meta.pageLoadError = ''; this.finishPageLoad(meta, '', epoch); return;
      }
    }
    this.finishPageLoad(meta, canceled ? 'Page navigation was cancelled.' : 'Page navigation failed.', epoch);
    meta.pageLoadFailureNavigation = entry ?? null;
  }

  confirmDownloadNavigation(meta, { frameId, url }) {
    if (this.pages.get(meta.id) !== meta || this.closed || typeof url !== 'string' || meta.downloadNavigationOverflowDocument === meta.topDocumentVersion) return;
    const key = createHash('sha256').update(url).digest('hex');
    const entry = meta.downloadNavigations?.find(entry => !entry.confirmedDownload && entry.frameId === frameId && entry.urlKey === key && entry.document === meta.topDocumentVersion);
    if (!entry) return;
    entry.confirmedDownload = true;
    // A genuine network failure, a newer cycle, or a committed replacement
    // document is never cleared by an earlier download notification.
    if (entry.epoch !== meta.pageLoadEpoch || !entry.failed || !entry.aborted || meta.pageLoadFailureNavigation !== entry) return;
    meta.pageLoadError = ''; this.finishPageLoad(meta, '', entry.epoch);
  }

  refreshNavigationHistory(meta) {
    if (this.closed || this.pages.get(meta.id) !== meta || meta.page.isClosed() || meta.historyFlight || meta.historyVersion === meta.version) return;
    const version = meta.version;
    const flight = this.devtools.attach(meta).then(state => state.client.send('Page.getNavigationHistory')).then(history => {
      if (this.closed || this.pages.get(meta.id) !== meta || meta.page.isClosed() || meta.version !== version) return;
      if (!Number.isInteger(history.currentIndex) || !Array.isArray(history.entries)) return;
      meta.canGoBack = history.currentIndex > 0 && history.currentIndex < history.entries.length;
      meta.historyVersion = version;
    });
    // Keep the underlying flight until it settles. A stuck protocol read must
    // not create another read on every viewer poll, nor block the state API.
    meta.historyFlight = flight;
    flight.finally(() => { if (meta.historyFlight === flight) meta.historyFlight = null; }).catch(() => {});
  }

  state(binding = {}) {
    this.touch();
    const meta = binding.tab_id !== undefined || binding.context_ref !== undefined ? this.selectedPage(binding) : this.pages.get(this.active);
    // Control changes retire debugger authority, but do not stop a preserved
    // page from loading. Restore passive collectors on this same page so its
    // eventual completion remains observable without another browser action.
    if (meta && this.phase === 'ready' && meta.controlPhase !== 'closing' && !this.closed) this.devtools.attach(meta).catch(error => { meta.devtoolsError = String(error.message); });
    if (meta && this.phase === 'ready' && meta.controlPhase !== 'closing') this.refreshNavigationHistory(meta);
    const control = this.controlFor(meta);
    return {
      backend: 'native', control_scope: 'tab', ...(this.host?.displayMode === 'desktop' ? { display_mode: 'desktop' } : {}), context_ref: this.contextRef, viewed_tab: meta?.id ?? '', control: control.mode, control_epoch: control.epoch, generation: this.generation,
      document_version: meta?.version ?? 0, version: this.version(meta), busy: this.phase !== 'ready' || control.phase !== 'ready' || Boolean(meta?.ownershipPending || meta?.linkNavigationPending) || this.operationTouches(meta) || this.contextBlocked(),
      pick_pending: Boolean(meta && this.picker.pending.has(meta)),
      page_loading: Boolean(meta?.pageLoading), ...(meta?.pageLoadError ? { page_load_error: meta.pageLoadError } : {}),
      ...(meta && meta.historyVersion === meta.version ? { can_go_back: Boolean(meta.canGoBack) } : {}),
      ...(meta ? { frame_identity: this.frameIdentity(meta) } : {}),
      url: meta?.page.url() ?? 'about:blank', title: meta?.title ?? '',
      ...(meta?.favicon ? { favicon: meta.favicon } : {}),
      tabs: this.tabMetadata(true),
      active_tab: this.active ?? '', viewport: this.viewport(meta),
      upload_capabilities: this.uploads.capabilities(),
      ...(meta?.id === this.active ? { activity: this.activity.state() } : {}),
      debugger_paused: Boolean(meta && this.devtools.isPaused(meta)),
      devtools_blocked: [...this.pages.values()].some(page => this.devtools.isBlocked(page)),
      ...(this.dialog && this.dialog.page === meta?.page ? { dialog: { type: this.dialog.type, message: this.dialog.message, default_value: this.dialog.default_value } } : {}),
    };
  }

  // Icon bytes are browser chrome metadata, never language-model evidence.
  tabMetadata(icons = false, bindings = false) {
    return [...this.pages.values()].map(p => ({ id: p.id, url: p.page.url(), title: p.title,
      ...(icons && p.favicon ? { favicon: p.favicon } : {}), control: this.controlFor(p).mode, control_epoch: this.controlFor(p).epoch,
      ...(bindings ? { context_ref: this.contextRef } : {}),
      ...(this.pageOwners.get(p.opener)?.id ? { opener_tab: this.pageOwners.get(p.opener).id } : {}),
      ...(p.control === 'human' && p.lease && p.lease === p.inheritedHumanLease && p.controlEpoch === p.inheritedControlEpoch ? { inherited_control_epoch: p.inheritedControlEpoch } : {}) }));
  }

  // A citation keeps the icon observed with this exact Page/URL. This is
  // display metadata only; it never waits for a still-loading icon resource.
  sourceFavicon(reply, meta, documentVersion = meta?.version) {
    if (!reply?.ok || reply.observation_pending || !meta?.favicon || this.pages.get(meta.id) !== meta || meta.version !== documentVersion ||
      meta.page.isClosed() || meta.pageLoading || meta.pageLoadError || reply.url !== meta.page.url()) return reply;
    return { ...reply, favicon: meta.favicon };
  }

  version(meta = this.pages.get(this.active)) {
    const size = meta ? this.viewport(meta) : null;
    return `${this.generation}:${meta?.id ?? ''}:${meta?.version ?? 0}:${meta?.visual ?? 0}:${meta?.revision ?? ''}:${this.controlFor(meta).epoch}${size ? `:${size.width}x${size.height}` : ''}`;
  }

  observeTopDocument(meta, frame) {
    if (this.pages.get(meta?.id) !== meta || this.closed || meta.page.isClosed()) return false;
    if (!frame || typeof frame.id !== 'string' || !frame.id || typeof frame.loaderId !== 'string' || !frame.loaderId) {
      this.loseDocumentIdentity(meta);
      return false;
    }
    const changed = meta.documentLoaderId !== null && meta.documentLoaderId !== undefined && meta.documentLoaderId !== frame.loaderId;
    meta.documentLoaderId = frame.loaderId;
    meta.documentFrameId = frame.id;
    meta.documentIdentityReady = true;
    if (changed) {
      meta.topDocumentVersion++;
      this.invalidatePage(meta);
      // Revoke coordinate authority synchronously at the real document commit.
      // Cleanup releases the old document's held keys/buttons before new input.
      this.fenceLiveInput(meta).catch(error => { meta.devtoolsError = String(error.message); });
    }
    this.publishLiveState(meta);
    return true;
  }

  loseDocumentIdentity(meta) {
    if (this.pages.get(meta?.id) !== meta || this.closed || meta.page.isClosed() || meta.documentIdentityReady === false) return;
    meta.documentIdentityReady = false;
    meta.topDocumentVersion++;
    this.invalidatePage(meta);
    this.fenceLiveInput(meta).catch(error => { meta.devtoolsError = String(error.message); });
  }

  frameIdentity(meta = this.pages.get(this.active)) {
    return `${this.generation}:${meta?.id ?? ''}:${meta?.topDocumentVersion ?? meta?.version ?? 0}`;
  }

  // Ownership and real navigation have authoritative events. Coalesce only
  // within this turn; do not add the fallback poll's 300ms to first-frame time.
  publishLiveState(meta) {
    if (!meta || this.closed || this.pages.get(meta.id) !== meta || meta.liveStateQueued) return;
    meta.liveStateQueued = true;
    queueMicrotask(() => {
      meta.liveStateQueued = false;
      if (this.closed || this.pages.get(meta.id) !== meta) return;
      for (const view of this.liveViews) if (view.meta === meta) view.publishState?.(true);
    });
  }

  // Live input owns no second executor: it dispatches CDP events on this exact
  // Page, under the same queue/lease/cancellation fence as Agent operations.
  // A view fences synchronously before its asynchronous key cleanup begins.
  async fenceLiveInput(meta) {
    await Promise.all([...this.liveViews].filter(view => !meta || view.meta === meta).map(view => view.fence()));
  }

  async dispatchLiveInput(binding, dispatch) {
    this.touch();
    const meta = this.selectedPage(binding);
    const check = () => {
      this.assertLive();
      const control = this.controlFor(meta);
      if (control.mode !== 'human' || !binding.lease_token || binding.lease_token !== control.lease) throw new HTTPError(403, "live input requires this tab\'s current human control lease");
      if (this.phase !== 'ready' || control.phase !== 'ready' || binding.generation !== this.generation || binding.control_epoch !== control.epoch || this.selectedPage(binding) !== meta) throw new HTTPError(409, 'live input was fenced by a browser control change');
      if (this.contextBlocked()) throw new HTTPError(409, 'browser operation outcome is unknown; cancel the browser before continuing');
      if (this.dialog || meta.ownershipPending || meta.linkNavigationPending || this.operationTouches(meta) || this.devtools.isPaused(meta)) throw new HTTPError(409, 'this browser page is busy; inspect current state before continuing', 'LIVE_BUSY');
    };
    check();
    const previous = this.liveQueues.get(meta) ?? Promise.resolve();
    const result = previous.then(async () => {
      check();
      const action = { uncertain: false };
      const raw = Promise.resolve().then(() => { check(); return dispatch(meta); });
      action.raw = raw; this.liveActions.set(meta, action);
      raw.finally(() => { if (this.liveActions.get(meta) === action) this.liveActions.delete(meta); }).catch(() => {});
      try { await bounded(raw, 2000, 'live input outcome is pending; cancel before continuing, do not replay'); }
      catch (error) {
        if (this.liveActions.get(meta) === action) { action.uncertain = true; throw new HTTPError(504, error.message); }
        throw error;
      }
      return { ok: true };
    });
    const drained = result.catch(() => {});
    this.liveQueues.set(meta, drained);
    drained.finally(() => { if (this.liveQueues.get(meta) === drained) this.liveQueues.delete(meta); });
    return result;
  }

  async resizeLiveViewport(binding, dispatch) {
    this.touch();
    const meta = this.selectedPage(binding);
    const check = () => {
      this.assertLive();
      const control = this.controlFor(meta);
      if (this.phase !== 'ready' || control.phase !== 'ready' || binding.generation !== this.generation || binding.control_epoch !== control.epoch || this.selectedPage(binding) !== meta) throw new HTTPError(409, 'viewport binding changed; inspect a current frame');
      if (control.mode === 'human' && (!binding.lease_token || binding.lease_token !== control.lease)) throw new HTTPError(403, "resizing a human tab requires that tab\'s lease");
      if (this.contextBlocked()) throw new HTTPError(409, 'browser operation outcome is unknown; cancel before resizing');
      if (this.currentOperation || this.inOperation || this.rawAction || this.liveActions.size || this.dialog || [...this.pages.values()].some(page => this.devtools.isPaused(page))) throw new HTTPError(409, 'browser is busy; resize can be retried after it is idle', 'LIVE_BUSY');
    };
    check();
    return this.enqueue(async () => {
      check();
      const raw = this.trackAction(Promise.resolve().then(() => dispatch(meta, raw)));
      await bounded(raw, 2000, 'viewport outcome is pending; cancel before continuing');
      return { ok: true };
    });
  }

  observation(epoch = this.generation, deadline = Date.now() + Math.min(this.actionTimeout, 4000), authority) {
    return { epoch, deadline, live: true, ...(authority ? { authority } : {}) };
  }

  checkObservation(observation) {
    if (observation.parent) this.checkObservation(observation.parent);
    if (!observation.live || observation.epoch !== this.generation || this.closed) throw new HTTPError(409, 'page observation was superseded; inspect the current page');
    if (observation.authority) {
      const { meta, mode, lease, controlEpoch } = observation.authority;
      const control = this.controlFor(meta);
      if (control.mode !== mode || control.epoch !== controlEpoch || control.phase !== 'ready' || mode === 'human' && lease !== control.lease) throw new HTTPError(409, 'page observation was fenced by tab ownership change');
    }
    if (Date.now() >= observation.deadline) throw new HTTPError(409, 'page observation timed out; the page may still be loading or busy');
  }

  async readObservation(owner, kind, work, observation, { unbounded = false } = {}) {
    this.checkObservation(observation);
    let reads = this.observationReads.get(owner);
    // Do not accumulate evaluations in an unresponsive renderer, and never
    // adopt the result belonging to an expired observation or another caller.
    if (reads?.has(kind)) throw new HTTPError(409, 'page observation is still pending; inspect another page or try a fresh observation later');
    if (!reads) {
      if (this.observationReads.size >= 64) throw new HTTPError(409, 'page observation capacity reached; wait for pending reads to settle');
      this.observationReads.set(owner, reads = new Map());
    }
    const raw = Promise.resolve().then(() => { this.checkObservation(observation); return work(); });
    reads.set(kind, raw);
    raw.finally(() => {
      if (reads.get(kind) === raw) reads.delete(kind);
      if (!reads.size && this.observationReads.get(owner) === reads) this.observationReads.delete(owner);
    }).catch(() => {});
    const result = unbounded ? await raw : await bounded(raw, Math.max(1, Math.min(1500, observation.deadline - Date.now())), 'page observation timed out; the page may still be loading or busy');
    this.checkObservation(observation);
    return result;
  }

  async refreshRevision(observation = this.observation(), meta = this.page(), { fast = false, unbounded = false } = {}) {
    const documentVersion = meta.version;
    const revisions = [];
    for (const frame of meta.page.frames()) {
      revisions.push(await this.readObservation(frame, 'revision', () => fast
        ? frame.evaluate(key => { const probe = globalThis[key]; return { r: probe?.fast?.() ?? 0, covered: typeof probe?.fast === 'function' && (probe.covered?.() ?? false), x: scrollX, y: scrollY, w: innerWidth, h: innerHeight }; }, this.revisionKey)
        : frame.evaluate(key => ({ r: globalThis[key]?.() ?? 0, covered: globalThis[key]?.covered?.() ?? false, x: scrollX, y: scrollY, w: innerWidth, h: innerHeight }), this.revisionKey), observation, { unbounded }));
    }
    this.checkObservation(observation);
    if (this.pages.get(meta.id) !== meta || meta.page.isClosed() || meta.version !== documentVersion) throw new HTTPError(409, 'page changed during revision observation; inspect the current page');
    meta.revision = createHash('sha256').update(JSON.stringify(revisions)).digest('hex').slice(0, 20);
    meta.revisionCovered = revisions.every(revision => revision.covered);
    return this.version(meta);
  }

  enqueue(task) {
    const result = this.queue.then(task);
    this.queue = result.catch(() => {});
    return result;
  }

  guard(epoch, mode = 'agent', lease, meta = this.pages.get(this.active), controlEpoch) {
    this.assertLive();
    controlEpoch ??= this.currentOperation?.mode === mode ? this.currentOperation.authority.get(meta) : undefined;
    const control = this.controlFor(meta);
    if (epoch !== this.generation || this.phase !== 'ready' || control.phase !== 'ready' || control.mode !== mode || (mode === 'human' && lease !== control.lease) || controlEpoch !== undefined && controlEpoch !== control.epoch) {
      throw new Error('operation fenced by cancellation or a change of browser control');
    }
    if (mode === 'agent') this.requireAgentPage(meta);
  }

  tabDiscovery(action) { return action.action === 'tabs' || action.action === 'tab' && ['new', 'switch', 'claim'].includes(action.tab_action ?? 'switch'); }

  async execute(args) {
    this.touch();
    this.assertLive();
    if (typeof args.op_id !== 'string' || !/^[A-Za-z0-9_.:-]{1,160}$/.test(args.op_id)) throw new HTTPError(400, 'execute requires a unique op_id');
    const debugAction = args.action === 'devtools';
    const hash = createHash('sha256').update(JSON.stringify(args)).digest('hex');
    const old = this.operations.get(args.op_id);
    if (old) {
      if (old.hash !== hash) throw new HTTPError(409, 'op_id is already reserved for different arguments; not replayed');
      if (old.result) return old.result;
      throw new HTTPError(409, 'op_id is pending or its result is no longer cached; outcome unknown, not replayed');
    }
    if (this.operations.size >= 10_000) throw new HTTPError(409, 'session operation limit reached; create a new session');
    try { validateAction(args); } catch (e) { return fail(e); }
    if (this.phase !== 'ready') throw new HTTPError(409, 'browser is being cancelled');
    if (!this.tabDiscovery(args)) this.requireAgentPage(this.pages.get(this.active));
    if ([...this.pages.values()].some(meta => meta.liveInputBlocked)) throw new HTTPError(409, 'live input release is uncertain; cancel the browser before continuing');
    if ([...this.liveActions.values()].some(action => action.uncertain)) throw new HTTPError(409, 'live input outcome is unknown; cancel the browser before continuing');
    if (this.rawAction && !this.inOperation && !debugRecovery(args)) throw new HTTPError(409, 'previous browser action is still pending; use devtools diagnostics/resume for a breakpoint, otherwise cancel before continuing');
    const epoch = this.generation;
    const originalMeta = this.pages.get(this.active), originalControlEpoch = this.controlFor(originalMeta).epoch;
    const record = { hash };
    this.operations.set(args.op_id, record); // Reserve before entering the queue.
    const result = await this.enqueue(async () => {
      try {
        if (this.tabDiscovery(args)) {
          this.assertLive();
          if (epoch !== this.generation || this.phase !== 'ready') throw new Error('tab action was fenced by context cancellation');
        } else this.guard(epoch, 'agent', undefined, originalMeta, originalControlEpoch);
        if (!this.tabDiscovery(args) && this.pages.get(this.active) !== originalMeta) throw new Error('Agent target changed while action was queued; inspect tabs before continuing');
        if ([...this.pages.values()].some(meta => meta.liveInputBlocked)) throw new Error('live input release is uncertain; cancel the browser before continuing');
        if ([...this.liveActions.values()].some(action => action.uncertain)) throw new Error('live input outcome is unknown; cancel the browser before continuing');
        if (this.dialog) throw new Error('page dialog is pending; take human control to accept or dismiss it');
        if ([...this.pages.values()].some(meta => this.devtools.isBlocked(meta))) throw new Error('debug operation outcome unknown; cancel the browser task before continuing; do not replay');
        if (this.rawAction) {
          if (!debugRecovery(args)) throw new Error('previous browser action is still pending; use devtools diagnostics/resume for a breakpoint, otherwise cancel before continuing');
          const reply = await this.devtools.execute(args, epoch);
          return { ...reply, backend: 'native', active_tab: this.active ?? '', version: this.version(),
            text: `${reply.text ?? ''}${this.rawAction ? '\nThe earlier page action is still pending; inspect its result after resuming. Do not repeat it.' : ''}` };
        }
        const meta = this.pages.get(this.active);
        if (meta && this.devtools.isPaused(meta) && !['devtools', 'tabs', 'tab'].includes(args.action)) throw new Error('debugger paused; use devtools Debugger.resume or Debugger.disable, or take human control');
        this.inOperation = true;
        const operation = this.beginOperation(this.tabDiscovery(args) ? null : this.pages.get(this.active), 'agent');
        try {
        const reply = await this.run(args, epoch);
        const claimed = args.action === 'tab' && args.tab_action === 'claim' ? this.pages.get(this.active) : null;
        return { ...reply, backend: 'native', active_tab: this.active ?? '', version: reply.version ?? this.version(),
          ...(reply.ok && claimed ? { context_ref: this.contextRef, control: this.controlFor(claimed).mode, control_epoch: this.controlFor(claimed).epoch } : {}) };
        } finally { operation.finish(); }
      } catch (e) { return { ...fail(e), url: this.pages.get(this.active)?.page.url() }; }
      finally { this.inOperation = false; }
    });
    // Browser internals may include authenticated business data. Preserve the
    // reservation for deduplication, but never replay cached debug responses.
    if (debugAction) return epoch === this.generation ? result : fail('debug operation interrupted; inspect current state; do not replay');
    record.result = result;
    this.cachedIDs.push(args.op_id);
    if (this.cachedIDs.length > 64) delete this.operations.get(this.cachedIDs.shift()).result;
    return result;
  }

  async run(args, epoch, mode = 'agent', lease) {
    const list = args.action === 'batch' ? args.actions : [args];
    const steps = [];
    let reply = { ok: true };
    for (const [index, action] of list.entries()) {
      const discovery = mode === 'agent' && this.tabDiscovery(action);
      if (!discovery) this.guard(epoch, mode, lease);
      else if (epoch !== this.generation || this.phase !== 'ready') throw new Error('tab action was fenced by cancellation');
      const activity = this.activity.begin(action, mode);
      const before = this.pages.get(this.active);
      const version = before?.version;
      // Human input is watched live through frames; only agent actions wait
      // for their own requests before the observation they return.
      reply = await this.perform(action, epoch, { settle: mode === 'agent', last: index === list.length - 1, mode, lease });
      await this.activity.finish(activity, reply.ok, Boolean(this.dialog && this.rawAction));
      if (args.action === 'batch') steps.push({ action: action.action, ok: reply.ok, ...(reply.error ? { error: reply.error } : {}) });
      if (!reply.ok) break;
      if (!discovery || action.action === 'tab') this.guard(epoch, mode, lease);
      if (list.length > 1 && (reply.observation_pending || this.active !== before?.id || before?.version !== version || ['navigate', 'tab'].includes(action.action))) {
        reply.text = `${reply.text ?? ''}\nBatch stopped after navigation/tab change; inspect the current page before continuing.`;
        break;
      }
    }
    const { observation_pending, ...result } = reply;
    return { ...result, ...(args.action === 'batch' ? { steps } : {}) };
  }

  async perform(action, epoch, options = {}) {
    const until = Date.now() + this.actionTimeout + 500;
    const startingMeta = this.pages.get(this.active);
    const authority = startingMeta && !this.tabDiscovery(action) ? { meta: startingMeta, mode: options.mode ?? 'agent', lease: options.lease, controlEpoch: this.controlFor(startingMeta).epoch } : undefined;
    const observation = this.observation(epoch, until, authority);
    if (['snapshot', 'find', 'extract'].includes(action.action)) observation.deadline = Math.min(until, Date.now() + Math.min(this.actionTimeout, 4000));
    let wake;
    const dialogPromise = new Promise(resolve => { wake = () => resolve({ dialog: true }); });
    this.dialogWake = wake;
    let debugWake;
    const sourceMeta = this.pages.get(this.active);
    const debugPromise = new Promise(resolve => { debugWake = () => {
      // A background tab may have its own paused target. Only this action's
      // page family can suspend its result or execution budget.
      if (sourceMeta && this.devtools.isPaused(sourceMeta)) resolve({ debugger: true });
    }; });
    const watchDebugger = !debugRecovery(action);
    if (watchDebugger) this.debuggerWake = debugWake;
    const sourcePage = options.settle && SETTLE_ACTIONS.has(action.action) ? this.page().page : null;
    const scope = sourcePage ? {
      epoch, startSeq: this.requestSeq, fromTab: this.active,
      before: new Set([...this.pages.values()].map(meta => meta.page)),
      sourcePage, sourceVersion: sourceMeta?.version, pages: new Set([sourcePage]),
    } : null;
    if (scope) this.settleScope = scope;
    let raw;
    const dispatched = () => { if (this.rawAction === raw) this.rawAction = null; };
    raw = this.action(action, epoch, { ...options, scope, until, observation, dispatched });
    if (!OBSERVATION_ACTIONS.has(action.action)) this.trackAction(raw);
    if (scope) raw.finally(() => {
      if (this.settleScope === scope) this.settleScope = null;
    }).catch(() => {});
    try {
      const outcome = await bounded(Promise.race([raw.then(value => ({ value })), dialogPromise, ...(watchDebugger ? [debugPromise] : [])]), this.actionTimeout + 1500);
      if (outcome.dialog) return { ok: false, error: 'page dialog requires human control; the action outcome is pending', dialogs: [`${this.dialog?.type}: ${this.dialog?.message}`] };
      if (outcome.debugger) return fail('debugger paused the dispatched action; its outcome is pending. Inspect devtools events.list and use Debugger.resume before verifying the result. Do not repeat the action.');
      if (epoch !== this.generation) return fail('operation cancelled or browser control changed; inspect state before continuing');
      if (observation.authority) {
        const { meta, mode, lease, controlEpoch } = observation.authority;
        this.guard(epoch, mode, lease, meta, controlEpoch);
      }
      return outcome.value;
    } catch (error) {
      // Keep the document for inspection. If a transport/evaluate promise is
      // still pending, rawAction blocks further input until an explicit cancel.
      return fail(error);
    } finally {
      // A late read may finish in Chromium, but must never publish old refs,
      // evidence, or revisions into a later operation.
      observation.live = false;
      if (this.dialogWake === wake) this.dialogWake = null;
      if (this.debuggerWake === debugWake) this.debuggerWake = null;
    }
  }

  trackAction(raw) {
    this.rawAction = raw;
    this.rawActionMeta = this.pages.get(this.active);
    raw.finally(() => { if (this.rawAction === raw) { this.rawAction = null; this.rawActionMeta = null; } }).catch(() => {});
    return raw;
  }

  async locator(target) {
    if (/^p\d+$/.test(target ?? '')) throw new Error('pN is a read-only picked-element observation; use snapshot or find to obtain an actionable eN reference');
    const meta = this.page();
    if (/^e\d+$/.test(target ?? '')) {
      const ref = this.refs.get(target);
      if (!ref || ref.page !== meta || ref.version !== meta.version || ref.frame.isDetached()) throw new Error(`stale ref ${target}; take a new snapshot`);
      const locator = ref.frame.locator(`[${this.marker}="${target}"]`);
      if (await locator.count() !== 1) throw new Error(`stale ref ${target}; take a new snapshot`);
      return locator;
    }
    return meta.page.locator(`css=${target}`);
  }

  // Only a coherent read enters the worker-owned quantity store. Navigation
  // and same-document changes fence the whole capture; no submitted action is
  // repeated to repair an observation. History remains explicitly as-observed.
  async captureEvidence(target = null, full = false, observation = this.observation()) {
    const meta = this.page(), documentVersion = meta.version, epoch = this.generation;
    const observationID = `o_${randomBytes(12).toString('hex')}_${this.nextObservation++}`;
    const unavailable = reason => ({ schema: 'dom-evidence-v1', status: 'unavailable', observation_id: observationID, reason, frames: [], truncated: false });
    try {
      const before = await this.refreshRevision(observation);
      if (!meta.revisionCovered) return unavailable('Document revision coverage is incomplete; structured evidence is unavailable.');
      const frames = target ? [null] : meta.page.frames().filter(frame => !frame.isDetached()).slice(0, 4);
      const captures = [];
      for (const [index, frame] of frames.entries()) {
        const options = { sourcePrefix: `${observationID}:f${index}`,
          maxStructureChars: Math.floor((full ? 16000 : 9000) / Math.max(frames.length, 1)),
          maxTextChars: Math.floor((full ? 6000 : 3000) / Math.max(frames.length, 1)) };
        const probeArgs = { key: this.evidenceKey, options };
        const value = await this.readObservation(frame ?? meta.page, 'evidence', () => target
          ? target.evaluate((el, { key, options }) => globalThis[key](el, options), probeArgs)
          : frame.evaluate(({ key, options }) => globalThis[key](null, options), probeArgs), observation);
        captures.push({ value, frameURL: frame ? frame.url() : value.url });
      }
      const after = await this.refreshRevision(observation);
      this.checkObservation(observation);
      if (epoch !== this.generation || this.active !== meta.id || meta.version !== documentVersion || before !== after || !meta.revisionCovered) return unavailable('Document changed or revision coverage was lost during evidence capture; observe again.');
      const observedAt = new Date().toISOString();
      const recorded = captures.map(({ value, frameURL }, index) => this.quantities.record(value, {
        observation_id: `${observationID}:f${index}`, generation: epoch, tab: meta.id,
        document_version: documentVersion, version: after, observed_at: observedAt,
        url: meta.page.url(), frame_url: frameURL,
      }));
      const truncated = recorded.some(value => value.truncated || value.quantities_truncated) || !target && meta.page.frames().length > 4;
      return { schema: 'dom-evidence-v1', status: truncated ? 'partial' : 'observed', observation_id: observationID,
        scope: target ? 'subtree' : 'document', version: after, observed_at: observedAt, frames: recorded, truncated };
    } catch {
      return unavailable('Visible DOM evidence could not be captured coherently; observe again.');
    }
  }

  async optionalEvidence(target, full, parent) {
    // Structured evidence is optional enrichment. Reserve publication time for
    // already captured text/refs instead of letting slow evidence discard them.
    const child = { ...this.observation(parent.epoch, Math.min(parent.deadline - 100, Date.now() + 750)), parent };
    try { return await this.captureEvidence(target, full, child); }
    finally { child.live = false; }
  }

  async snapshot(query = {}, observation = this.observation()) {
    const meta = this.page();
    const version = meta.version;
    const all = [];
    const capturedRefs = [];
    let observedTitle = meta.title;
    let body = '';
    for (const frame of meta.page.frames()) {
      if (frame.isDetached()) continue;
      const known = [...this.refs].filter(([, r]) => r.page === meta && r.version === version && r.frame === frame).map(([id]) => id);
      // Reserve a disjoint range before dispatch. A late timed-out collector
      // can still set DOM marker attributes, but cannot collide with new refs.
      const next = this.nextRef;
      this.nextRef += 1500;
      const result = await this.readObservation(frame, 'snapshot', () => frame.evaluate(({ marker, next, known }) => {
        const valid = new Set(known);
        const items = [];
        const tidy = value => String(value ?? '').replace(/\s+/g, ' ').trim().slice(0, 250);
        const roleOf = el => el.getAttribute('role') || ({ A: 'link', BUTTON: 'button', TEXTAREA: 'textbox', SELECT: 'combobox', INPUT: ['checkbox', 'radio'].includes(el.type) ? el.type : 'textbox' }[el.tagName] || 'element');
        const visit = root => {
          for (const el of root.querySelectorAll('*')) {
            if (items.length >= 1500) return;
            if (el.shadowRoot) visit(el.shadowRoot);
            if (items.length >= 1500) return;
            if (!el.matches('a[href],button,input:not([type="hidden"]),textarea,select,[role],[contenteditable="true"],[tabindex]')) continue;
            const box = el.getBoundingClientRect();
            const css = getComputedStyle(el);
            if (!box.width || !box.height || css.visibility === 'hidden' || css.display === 'none') continue;
            let ref = el.getAttribute(marker);
            if (!valid.has(ref)) { ref = `e${next++}`; el.setAttribute(marker, ref); }
            const label = tidy(Array.from(el.labels ?? []).map(n => n.textContent).join(' '));
            const labelled = tidy((el.getAttribute('aria-labelledby') || '').split(/\s+/).map(id => el.ownerDocument.getElementById(id)?.textContent).join(' '));
            const name = tidy(el.getAttribute('aria-label') || labelled || label || el.alt || el.title || el.placeholder || el.textContent);
            const role = roleOf(el);
            const ariaChecked = el.getAttribute('aria-checked');
            const checked = el.tagName === 'INPUT' && ['checkbox', 'radio'].includes(el.type) ? el.checked
              : ['checkbox', 'radio'].includes(role) && ['true', 'false', 'mixed'].includes(ariaChecked) ? ariaChecked : undefined;
            items.push({ ref, role, name, label, placeholder: tidy(el.getAttribute('placeholder')), testid: el.getAttribute('data-testid') || '',
              text: tidy(el.textContent), type: el.type || '', ...(el.type !== 'password' && 'value' in el ? { value: tidy(el.value) } : {}),
              ...(checked !== undefined ? { checked } : {}),
              ...(el.tagName === 'SELECT' ? { options: Array.from(el.options).slice(0, 40).map(o => ({ value: o.value, label: o.label })) } : {}),
              ...(el.href ? { href: el.href } : {}) });
          }
        };
        visit(document);
        return { next, items, text: (document.body?.innerText ?? '').slice(0, 32_000), title: document.title };
      }, { marker: this.marker, next, known }), observation);
      this.checkObservation(observation);
      if (meta.version !== version || this.active !== meta.id) throw new Error('page changed during snapshot; take a new snapshot');
      if (frame === meta.page.mainFrame()) { observedTitle = result.title; body = result.text; }
      for (const item of result.items) {
        capturedRefs.push([item.ref, { page: meta, version, frame }]);
        all.push(item);
      }
    }
    const filters = ['role', 'name', 'text', 'placeholder', 'label', 'testid'];
    const filtered = query.action === 'find' ? all.filter(item => filters.every(key => !query[key] || String(item[key] ?? '').toLowerCase().includes(String(query[key]).toLowerCase()))) : all;
    const lines = filtered.map(item => `[${item.ref}] ${item.role} ${JSON.stringify(item.name)}${item.value ? ` value=${JSON.stringify(item.value)}` : ''}${item.checked !== undefined ? ` checked=${item.checked}` : ''}${item.href ? ` href=${item.href}` : ''}${item.options ? ` options=${JSON.stringify(item.options)}` : ''}`);
    const budget = query.full ? 60_000 : 22_000;
    const text = `${query.action === 'find' ? '' : body + '\n\n'}${lines.join('\n')}`.slice(0, budget);
    const evidence = query.action === 'find' ? null : await this.optionalEvidence(null, query.full === true, observation);
    this.checkObservation(observation);
    if (meta.version !== version || this.active !== meta.id) throw new Error('page changed during snapshot; take a new snapshot');
    meta.title = observedTitle;
    for (const [ref, value] of capturedRefs) this.refs.set(ref, value);
    return this.sourceFavicon({ ok: true, url: meta.page.url(), title: meta.title, interactive: all.length, text: text || 'No matching elements or readable page text.', ...(evidence ? { evidence } : {}) }, meta, version);
  }

  async action(a, epoch, { scope = null, last = true, until = Infinity, observation = this.observation(epoch), dispatched = () => {} } = {}) {
    if (a.action === 'devtools') {
      const meta = this.page();
      const documentVersion = meta.version;
      await meta.devtoolsReady;
      const reply = await this.devtools.execute(a, epoch);
      // Protocol calls can change DOM/layout or runtime state without a normal
      // input event. Fence element/coordinate observations; don't run a page
      // snapshot here, which would hang while Debugger has JavaScript paused.
      if (/^[A-Z]/.test(a.method)) {
        meta.visual++;
        this.lastScreenshotVersion = null;
        for (const [ref, value] of this.refs) if (value.page === meta) this.refs.delete(ref);
        reply.text = [reply.text, 'CDP invalidated element refs and screenshot coordinates. Run snapshot/find before using element refs again; take a fresh screenshot before coordinate input.'].filter(Boolean).join('\n');
      }
      return a.target ? reply : this.sourceFavicon(reply, meta, documentVersion);
    }
    if (a.action === 'calculate') {
      const evidence = this.quantities.calculate({ operation: a.operation, quantity_refs: a.quantity_refs }, epoch);
      return { ok: evidence.status === 'calculated', evidence, url: this.page().page.url(),
        ...(evidence.status !== 'calculated' ? { error: evidence.reason ?? 'Observed quantities cannot be calculated; inspect the evidence limitations.' } : {}),
        text: 'Calculation concerns only the referenced observed quantities. It does not verify current state, all-site coverage, or the entire answer.' };
    }
    // A source window can close before Playwright reports its popup. Tab
    // discovery and explicit switching must work even without an active page.
    if (a.action === 'tabs') {
      const state = this.state();
      return { ok: true, url: state.url, title: state.title, text: JSON.stringify(this.tabMetadata(false, true)) };
    }
    if (a.action === 'tab') {
      await this.tab(a);
      dispatched();
      const target = this.pages.get(this.active);
      if (target) observation.authority = { meta: target, mode: 'agent', controlEpoch: this.controlFor(target).epoch };
      this.checkObservation(observation);
      const active = this.pages.get(this.active);
      if (active) active.visual++;
      this.lastScreenshotVersion = null;
      if (this.dialog) return fail('dialog pending; human input required');
      if (active && this.devtools.isPaused(active)) return { ok: true, url: active.page.url(), title: active.title, text: 'Selected tab is paused in the debugger; use devtools to inspect or resume before taking a page snapshot.' };
      return this.observe(observation);
    }
    const meta = this.page();
    const page = meta.page;
    let uploadReceipt;
    if (['click', 'press', 'type'].includes(a.action)) {
      await meta.linkNavigationReady;
      this.checkObservation(observation);
    }
    await this.devtools.attach(meta).catch(error => { meta.devtoolsError = String(error.message); });
    this.checkObservation(observation);
    try {
    switch (a.action) {
      case 'snapshot': case 'find': return this.snapshot(a, observation);
      case 'navigate': await page.goto(urlFor(a.url, page.url()), { waitUntil: 'domcontentloaded' }); break;
      case 'input_back':
        // Playwright implements native history traversal through Chromium's
        // Page.getNavigationHistory / Page.navigateToHistoryEntry, including
        // same-document routes and document commits. It is not a synthetic
        // keyboard shortcut or a website's potentially replaced history API.
        await page.goBack({ waitUntil: 'domcontentloaded' });
        break;
      case 'click':
        if (a.target) { const target = await this.locator(a.target); this.checkObservation(observation); await target.click(); }
        else {
          this.coordinates(a.x, a.y);
          await this.refreshRevision();
          if (a.version !== this.version() || (this.control === 'agent' && a.version !== this.lastScreenshotVersion)) throw new Error('stale screenshot version; capture a new screenshot before coordinate click');
          await page.mouse.click(a.x, a.y);
          this.activity.coordinateClick(a.x, a.y, epoch);
        }
        break;
      case 'type': {
        const target = await this.locator(a.target);
        this.checkObservation(observation);
        await target.fill(a.text ?? '');
        if (a.submit) {
          if (epoch !== this.generation || this.phase !== 'ready') throw new Error('submit fenced by browser control change');
          this.checkObservation(observation);
          await target.press('Enter');
        }
        break;
      }
      case 'select': {
        const target = await this.locator(a.target);
        this.checkObservation(observation);
        const byValue = await target.locator('option').evaluateAll((opts, text) => opts.some(o => o.value === text), a.text);
        this.checkObservation(observation);
        await target.selectOption(byValue ? { value: a.text } : { label: a.text });
        break;
      }
      case 'press': {
        const key = a.key || a.text;
        if (a.target) { const target = await this.locator(a.target); this.checkObservation(observation); await target.press(key); }
        else await page.keyboard.press(key);
        break;
      }
      case 'hover': { const target = await this.locator(a.target); this.checkObservation(observation); await target.hover(); break; }
      case 'upload': {
        const documentVersion = meta.version;
        const assertTarget = () => {
          this.checkObservation(observation);
          const binding = a.upload_binding;
          // Recheck after filesystem and locator awaits, directly before each
          // browser dispatch. Generic observation authority fences control but
          // does not itself fence a newly navigated document on the same Page.
          if (this.pages.get(this.active) !== meta || meta.version !== documentVersion || binding && (binding.context_ref !== this.contextRef || binding.tab_id !== meta.id || binding.generation !== this.generation || binding.control_epoch !== this.controlFor(meta).epoch || binding.document_version !== meta.version)) throw new HTTPError(409, 'upload target changed after file preparation; inspect the current page before uploading', 'UPLOAD_TARGET_CHANGED');
        };
        assertTarget();
        const staged = await this.uploads.acquire(a.files);
        try {
          const target = await this.locator(a.target);
          assertTarget();
          const mode = a.upload_mode ?? 'input';
          const timeout = Math.max(1, Math.min(this.actionTimeout, until - Date.now()));
          if (mode === 'input') {
            const input = await target.evaluate(el => ({ file: el.tagName === 'INPUT' && el.type === 'file', multiple: el.multiple }));
            assertTarget();
            if (!input.file) throw new Error('input upload target must be an input[type=file]; use chooser mode for its opening button');
            if (!input.multiple && staged.paths.length > 1) throw new Error('this file input accepts only one file');
            staged.retain();
            await target.setInputFiles(staged.paths, { timeout });
          } else if (mode === 'drop') {
            assertTarget();
            staged.retain();
            await target.drop({ files: staged.paths }, { timeout });
          }
          else {
            // Install interception before clicking a button whose handler may
            // create a hidden input and open the chooser synchronously.
            let listener, closeListener, timer, rejectChooser;
            const chooser = new Promise((resolve, reject) => {
              rejectChooser = reject;
              listener = value => { cleanup(); resolve(value); };
              closeListener = () => { cleanup(); reject(new Error('file chooser interrupted because the source page closed; do not repeat an unknown action')); };
              timer = setTimeout(() => { cleanup(); reject(new Error('file chooser did not open; inspect the page before retrying')); }, timeout);
              page.on('filechooser', listener);
              page.on('close', closeListener);
            });
            const cleanup = () => { clearTimeout(timer); page.off('filechooser', listener); page.off('close', closeListener); };
            chooser.catch(() => {});
            try {
              assertTarget();
              await target.click({ timeout });
              const selected = await chooser;
              assertTarget();
              if (!selected.isMultiple() && staged.paths.length > 1) throw new Error('this file chooser accepts only one file');
              staged.retain();
              await selected.setFiles(staged.paths, { timeout: Math.max(1, Math.min(this.actionTimeout, until - Date.now())) });
            } catch (error) { cleanup(); rejectChooser(error); throw error; }
            finally { cleanup(); }
          }
          uploadReceipt = { status: 'selected', mode, files: staged.files, site_parsing_verified: false };
        } finally { staged.release(); }
        break;
      }
      case 'scroll':
        if (a.target) { const target = await this.locator(a.target); this.checkObservation(observation); await target.scrollIntoViewIfNeeded(); }
        else if (a.direction === 'top' || a.direction === 'bottom') await page.evaluate(bottom => window.scrollTo(0, bottom ? document.documentElement.scrollHeight : 0), a.direction === 'bottom');
        else await page.mouse.wheel(0, (a.direction === 'up' ? -1 : 1) * Math.min(Math.max(a.amount || 600, 0), 50_000));
        break;
      case 'wait': {
        const timeout = Math.min(Math.max(a.seconds ? a.seconds * 1000 : this.actionTimeout, 1), this.actionTimeout, MAX_WAIT_TIMEOUT_MS);
        if (a.target) { const target = await this.locator(a.target); this.checkObservation(observation); await target.waitFor({ state: a.gone_text ? 'hidden' : 'visible', timeout }); }
        else if (a.gone_text) await page.getByText(a.gone_text).first().waitFor({ state: 'hidden', timeout });
        else if (a.text) await page.getByText(a.text).first().waitFor({ state: 'visible', timeout });
        else await page.waitForURL(a.url, { waitUntil: 'domcontentloaded', timeout });
        break;
      }
      case 'extract': {
        const documentVersion = meta.version;
        const target = await this.locator(a.target);
        this.checkObservation(observation);
        const text = await this.readObservation(meta.page, 'extract', () => target.innerText(), observation);
        const evidence = await this.optionalEvidence(target, a.full === true, observation);
        this.checkObservation(observation);
        return this.sourceFavicon({ ok: true, url: page.url(), title: meta.title, text: text.slice(0, 60_000), evidence }, meta, documentVersion);
      }
      case 'screenshot': {
        const { data, version, identity, viewport = this.viewport(meta) } = await this.frame();
        this.checkObservation(observation);
        if (identity !== this.frameIdentity() || meta !== this.pages.get(this.active) || (version && version !== this.version())) throw new HTTPError(409, 'page changed after screenshot; capture a fresh screenshot');
        this.lastScreenshotVersion = version || null;
        return this.sourceFavicon({ ok: true, url: page.url(), title: meta.title, version,
          ...(!version ? { text: 'View-only screenshot: the page changed during capture. Coordinate input is unavailable until a fresh stable screenshot provides a nonempty version.' } : {}),
          screenshot: { data: data.toString('base64'), mime: 'image/jpeg', ...viewport } }, meta);
      }
      case 'input_text': await page.keyboard.insertText(a.text); break;
      case 'input_scroll': await page.mouse.wheel(a.deltaX, a.deltaY); break;
    }
    } catch (error) {
      if (scope?.sourcePage.isClosed() && epoch === this.generation && /Target (?:page, context or browser has been closed|closed)/i.test(error.message)) {
        // Chromium may run a click handler that opens a page and closes its
        // source before Playwright receives its input acknowledgement. Closure
        // is confirmed, but successful input dispatch is not: retain this
        // reservation and inspect tabs without replaying or guessing a result.
        dispatched();
        return { ok: false, observation_pending: true,
          error: 'Input outcome could not be confirmed because its source tab closed; inspect tabs, do not repeat the action',
          text: `The source tab ${scope.fromTab} closed before the input result could be confirmed. ` +
            'New tab attribution is unavailable; the active fallback page is not a verified action result. ' +
            `Available tabs: ${JSON.stringify(this.tabMetadata())}. Use tabs and explicitly choose an agent-owned tab to inspect. Do not repeat the action.` };
      }
      throw error;
    }
    dispatched();
    // Input has returned: all remaining work is bounded observation. Preserve
    // its known dispatch without confusing an unavailable read with a pending
    // side effect. No failed observation ever repeats the input.
    const settled = scope ? await this.settleAfter(scope, last, until) : null;
    if (!observation.live || epoch !== this.generation) return fail('operation observation superseded; inspect current state without repeating the action');
    observation.deadline = Math.min(observation.deadline, Date.now() + Math.min(this.actionTimeout, 4000));
    meta.visual++;
    this.lastScreenshotVersion = null;
    if (this.dialog) return fail('dialog pending; human input required');
    const observed = this.closedSourceObservation(scope) || await this.observe(observation);
    // Closing can also race the snapshot itself. Do not present a fallback
    // background page as the result of the already-dispatched action.
    const verified = this.closedSourceObservation(scope) || observed;
    const result = settled ? { ...verified, ...settled } : verified;
    return uploadReceipt ? { ...result, data: { ...result.data, upload: uploadReceipt },
      text: `Files were selected on the website. Website parsing or server upload is not verified by this receipt; inspect the page result before continuing.\n${result.text ?? ''}` } : result;
  }

  closedSourceObservation(scope) {
    if (!scope?.sourcePage.isClosed() || scope.pages.has(this.pages.get(this.active)?.page)) return null;
    // Playwright omits the opener's popup event if it closes before the new
    // page is reported. Page timing is insufficient evidence of attribution.
    return { ok: true, observation_pending: true,
      text: `Action was dispatched, but the source tab ${scope.fromTab} closed and the resulting page could not be confirmed. ` +
        'New tab attribution is unavailable; the active fallback page is not a verified action result. ' +
        `Available tabs: ${JSON.stringify(this.tabMetadata())}. ` +
        'Use tabs if a page is still opening, then explicitly choose tab with target and inspect it. Do not repeat the action.' };
  }

  async observe(observation = this.observation()) {
    try { return await this.snapshot({}, observation); }
    catch {
      // Enter/click may finish dispatch before navigation destroys the old
      // execution context. Retry only observation, never the submitted action.
      try {
        this.checkObservation(observation);
        await this.page().page.waitForLoadState('domcontentloaded', { timeout: Math.max(1, Math.min(1500, observation.deadline - Date.now())) });
        this.checkObservation(observation);
        return await this.snapshot({}, observation);
      } catch {
        return { ok: true, url: this.pages.get(this.active)?.page.url(), title: this.pages.get(this.active)?.title,
          observation_pending: true, text: 'Action was dispatched. Page observation is incomplete because the page changed, is still loading, or is busy. Take a fresh snapshot or inspect another page before continuing. Do not repeat the action.' };
      }
    }
  }

  coordinates(x, y, meta = this.pages.get(this.active)) {
    const { width, height } = this.viewport(meta);
    if (!finite(x) || !finite(y) || x < 0 || y < 0 || x >= width || y >= height) throw new Error(`coordinates must be inside the ${width}×${height} viewport`);
  }

  async tab(a) {
    const generation = this.generation;
    a = { ...a, tab_action: a.tab_action ?? 'switch', tab_id: a.tab_id ?? a.target };
    if (a.tab_action === 'claim') {
      const target = this.selectedPage({ tab_id: a.tab_id, context_ref: a.context_ref });
      await this.transitionControl({ mode: 'agent', tab_id: target.id, context_ref: a.context_ref }, { expectedEpoch: a.control_epoch });
      const controlEpoch = this.controlFor(target).epoch;
      this.guard(generation, 'agent', undefined, target, controlEpoch);
      this.currentOperation?.pages.add(target);
      this.currentOperation?.authority.set(target, controlEpoch);
      this.active = target.id;
      await target.page.bringToFront();
      this.guard(generation, 'agent', undefined, target, controlEpoch);
      return;
    }
    if (a.tab_action === 'new') {
      if (this.maxPages > 0 && this.pages.size >= this.maxPages) throw new HTTPError(409, 'browser tab capacity reached');
      const url = a.url ? urlFor(a.url, this.pages.get(this.active)?.page.url()) : null;
      const page = await this.createPage();
      const meta = [...this.pages.values()].find(entry => entry.page === page);
      this.requireAgentPage(meta);
      const controlEpoch = this.controlFor(meta).epoch;
      this.active = meta?.id ?? null;
      if (meta) this.currentOperation?.pages.add(meta);
      if (meta) this.currentOperation?.authority.set(meta, controlEpoch);
      await meta?.devtoolsReady;
      this.guard(generation, 'agent', undefined, meta, controlEpoch);
      if (url) await page.goto(url, { waitUntil: 'domcontentloaded' });
      this.guard(generation, 'agent', undefined, meta, controlEpoch);
      return;
    }
    const target = this.pages.get(a.tab_id || this.active);
    if (!target) throw new Error('tab not found');
    await target.ownershipReady;
    this.requireAgentPage(target);
    const controlEpoch = this.controlFor(target).epoch;
    this.currentOperation?.pages.add(target);
    this.currentOperation?.authority.set(target, controlEpoch);
    if (a.tab_action === 'close') {
      await this.closePage(target.page);
      if (!this.pages.size) {
        const page = await this.createPage();
        this.active = [...this.pages.values()].find(meta => meta.page === page)?.id ?? null;
      }
    } else if (a.tab_action === 'switch') {
      this.guard(generation, 'agent', undefined, target, controlEpoch);
      this.active = target.id;
      await target.page.bringToFront();
      this.guard(generation, 'agent', undefined, target, controlEpoch);
    } else throw new Error('tab_action must be new, switch or close');
  }

  async manageTabs(body) {
    this.touch(); this.assertLive();
    if (!body || typeof body.op_id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(body.op_id) ||
      !['new', 'close'].includes(body.action) || typeof body.context_ref !== 'string' || !/^ctx_[a-f0-9]{24}$/.test(body.context_ref) ||
      Object.keys(body).some(key => !['op_id', 'context_ref', 'action', 'tab_id', 'lease_token', 'generation', 'control_epoch'].includes(key))) throw new HTTPError(400, 'tabs requires op_id, context_ref and new or close action');
    if (body.context_ref !== this.contextRef) throw new HTTPError(409, 'browser context changed; inspect current browser state');
    if (body.action === 'new' && body.tab_id !== undefined || body.action === 'close' && (typeof body.tab_id !== 'string' || !/^t[1-9]\d{0,9}$/.test(body.tab_id))) throw new HTTPError(400, 'close requires tab_id; new always creates about:blank');
    const boundClose = body.generation !== undefined || body.control_epoch !== undefined;
    if (body.action === 'new' && boundClose || boundClose && (!Number.isSafeInteger(body.generation) || body.generation < 1 || !Number.isSafeInteger(body.control_epoch) || body.control_epoch < 0)) throw new HTTPError(400, 'only close accepts generation and control_epoch; provide both as current integers');
    const hash = createHash('sha256').update(JSON.stringify(body)).digest('hex');
    const old = this.tabOperations.get(body.op_id);
    if (old) {
      if (old.hash !== hash) throw new HTTPError(409, 'tab op_id is reserved for different arguments', 'TAB_OPERATION_ID_CONFLICT');
      if (old.result) return old.result;
      if (old.error) throw old.error;
      throw new HTTPError(409, 'tab operation is pending or no longer cached; not replayed', old.pending ? 'TAB_OPERATION_PENDING' : 'TAB_OPERATION_OUTCOME_UNKNOWN');
    }
    if (this.tabOperations.size >= 2048) throw new HTTPError(429, 'tab operation limit reached', 'TAB_OPERATION_REJECTED');
    const epoch = this.generation, lease = body.lease_token;
    const record = { hash, pending: true }; this.tabOperations.set(body.op_id, record);
    let dispatched = false;
    try {
      const perform = async () => {
        this.assertLive();
        if (body.context_ref !== this.contextRef || epoch !== this.generation || this.phase !== 'ready') throw new HTTPError(409, 'tab operation was fenced by browser control change');
        let id;
        if (body.action === 'new') {
          if (this.maxPages > 0 && this.pages.size >= this.maxPages) throw new HTTPError(429, 'browser tab capacity reached');
          dispatched = true;
          const page = await this.createPage();
          const meta = [...this.pages.values()].find(entry => entry.page === page);
          if (!meta || epoch !== this.generation || body.context_ref !== this.contextRef || this.phase !== 'ready') {
            await this.closePage(page);
            dispatched = false;
            throw new HTTPError(409, 'tab creation was fenced by browser control change');
          }
          id = meta.id;
          // The UI's explicit + belongs to the human. A new blank page has no
          // old input to drain, and is never selected as the Agent target.
          meta.control = 'human'; meta.lease = randomBytes(24).toString('base64url'); meta.controlEpoch++;
          this.invalidatePage(meta);
          // The page is deliberately background and blank. Opening its Sheet is
          // neither navigation nor a change of the Agent's execution target.
        } else {
          const meta = this.selectedPage({ tab_id: body.tab_id, context_ref: body.context_ref });
          const control = this.controlFor(meta);
          if (control.phase !== 'ready' || meta.ownershipPending) throw new HTTPError(409, 'tab ownership is changing; refresh the bound tab before closing');
          if (boundClose && (body.generation !== this.generation || body.control_epoch !== control.epoch)) throw new HTTPError(409, 'tab identity or control changed; refresh the bound tab before closing');
          if (control.mode === 'human') {
            const unassigned = !control.lease && control.epoch === 0 && boundClose && lease === undefined;
            if (!unassigned && (!lease || lease !== control.lease)) throw new HTTPError(403, 'closing a human tab requires that tab\'s current control lease');
          } else if (control.mode !== 'agent' || !boundClose || lease !== undefined) {
            throw new HTTPError(403, 'closing an agent tab requires its current generation and control_epoch without a human lease');
          }
          id = meta.id;
          // Closing is a page-scoped interruption. Fence old authority before
          // any await, then let Page.close interrupt a pending navigation or
          // input instead of waiting behind the very action it must stop.
          meta.controlEpoch = control.epoch + 1;
          meta.controlPhase = 'closing';
          this.invalidatePage(meta);
          dispatched = true;
          const cleanup = this.fenceLiveInput(meta);
          // Attach a rejection handler immediately: Page.close can take longer
          // than a held-input release, and closure supersedes that release only
          // after Chromium has confirmed that this exact page is gone.
          const released = cleanup.catch(error => error);
          // Managed close includes peer/output registration cleanup. A closed
          // Page alone must not conceal a failure to retire those resources.
          await this.closePage(meta.page);
          if (!meta.page.isClosed()) throw new HTTPError(503, 'browser did not confirm that the requested tab closed');
          await bounded(released, 2500).catch(() => {});
        }
        return { tab_id: id, context_ref: this.contextRef,
          state: body.action === 'new' ? this.state({ tab_id: id, context_ref: this.contextRef }) : this.state(),
          ...(body.action === 'new' ? { lease_token: this.pages.get(id).lease } : {}) };
      };
      // New tabs retain the normal executor and capacity checks. An explicit
      // close has its own exact-page authority and must remain available while
      // this or another page has an action queued or is still loading.
      const result = await (body.action === 'close' ? perform() : this.enqueue(perform));
      dispatched = false; record.pending = false;
      record.result = result; this.cachedTabIDs.push(body.op_id);
      if (this.cachedTabIDs.length > 64) delete this.tabOperations.get(this.cachedTabIDs.shift()).result;
      return result;
    } catch (error) {
      record.pending = false;
      record.error = new HTTPError(dispatched ? 503 : error.status ?? 500, dispatched ? 'tab operation outcome is unknown; keep the same op_id and inspect state; do not repeat creation or closing' : error.message,
        dispatched ? 'TAB_OPERATION_OUTCOME_UNKNOWN' : 'TAB_OPERATION_REJECTED');
      throw record.error;
    }
  }

  async frame(binding = {}) {
    this.touch();
    const meta = this.selectedPage(binding), epoch = this.generation;
    if (this.phase !== 'ready') throw new HTTPError(409, 'browser control transition is in progress');
    if (this.dialog?.page === meta.page) throw new HTTPError(409, 'dialog pending; use state to display its message');
    if (this.devtools.isPaused(meta)) throw new HTTPError(409, 'debugger paused; resume debugging before capturing a fresh frame');
    if (this.devtools.isBlocked(meta)) throw new HTTPError(409, 'debug operation outcome unknown; cancel explicitly before capturing a fresh frame');
    const stamp = this.frameIdentity(meta), existing = this.frameFlights.get(meta), viewport = this.viewport(meta);
    if (existing) {
      if (existing.stamp !== stamp) throw new HTTPError(409, 'earlier frame observation is still pending; request a fresh frame later');
      return existing.promise;
    }
    const observation = this.observation(epoch, Date.now() + 4500);
    // Prefer first pixels quickly, then allow a stable but slower renderer to
    // regain interactive authority on subsequent captures of this document.
    const revisionBudget = meta.frameRevisionAttempt === stamp ? 500 : 80;
    meta.frameRevisionAttempt = stamp;
    const revision = async () => {
      const child = { ...this.observation(epoch, Math.min(observation.deadline, Date.now() + revisionBudget)), parent: observation };
      try { return await this.refreshRevision(child, meta); }
      catch { return null; }
      finally { child.live = false; }
    };
    const overlay = version => meta.id === this.active ? bounded(this.activity.overlay(version), 80).catch(() => null) : null;
    const raw = (async () => {
      // Page JS is optional for first pixels. A slow/blocked iframe must not
      // withhold the compositor image; only interactive authority needs a
      // complete before/after revision observation.
      const version = await revision();
      const sequence = this.activity.seq;
      const before = version ? await overlay(version) : null;
      this.checkObservation(observation);
      const data = await this.devtools.capturePixels(meta, Math.max(1, Math.min(2500, observation.deadline - Date.now())));
      const afterOverlay = version ? await overlay(version) : null;
      this.checkObservation(observation);
      const after = version ? await revision() : null;
      if (this.pages.get(meta.id) !== meta || meta.page.isClosed() || stamp !== this.frameIdentity(meta)) throw new HTTPError(409, 'page document changed while capturing frame; request a new frame');
      const currentViewport = this.viewport(meta);
      if (currentViewport.width !== viewport.width || currentViewport.height !== viewport.height) throw new HTTPError(409, 'page viewport changed while capturing frame; request a new frame');
      if (this.phase !== 'ready' || this.dialog?.page === meta.page || this.devtools.isPaused(meta) || this.devtools.isBlocked(meta)) throw new HTTPError(409, 'browser state changed while capturing frame; request a new frame');
      // Animations and frequently updating DOM can prevent a stable revision
      // indefinitely. Pixels from the same document remain useful to view,
      // but an empty version explicitly withholds all coordinate authority.
      const selected = { identity: stamp, context_ref: this.contextRef, viewed_tab: meta.id, viewport };
      if (!version || !after || version !== after) return { data, version: '', ...selected };
      const geometry = value => JSON.stringify([value.pointer, value.target, value.focus]);
      const stable = before?.tab_id === meta.id && afterOverlay?.tab_id === meta.id && sequence === this.activity.seq && geometry(before) === geometry(afterOverlay);
      // A pointer/focus change need not mutate the DOM. Keep the valid image,
      // but omit uncertain decorations instead of drawing them on wrong pixels.
      return { data, version, ...selected, ...(stable ? { overlay: afterOverlay } : {}) };
    })();
    const flight = { stamp, promise: null };
    flight.promise = bounded(raw, 5000).catch(error => {
      if (/page observation|browser operation timed out|FRAME_CAPTURE_(PENDING|TIMEOUT)/.test(error.message)) throw new HTTPError(409, 'page observation is temporarily unavailable; request a fresh frame later');
      throw error;
    }).finally(() => { observation.live = false; });
    this.frameFlights.set(meta, flight);
    raw.finally(() => { if (this.frameFlights.get(meta) === flight) this.frameFlights.delete(meta); }).catch(() => {});
    return flight.promise;
  }

  async resetPages() {
    if (this.resetPromise) return this.resetPromise;
    this.generation++;
    this.quantities.clear();
    this.activity.fence();
    this.phase = 'cancelling';
    this.resetPromise = (async () => {
      try {
        await this.fenceLiveInput();
        // Closing every page also interrupts background popup actions. Context
        // storage survives; the old document and refs intentionally do not.
        await bounded(Promise.all([...this.pages.values()].map(p => this.closePage(p.page))), 5000);
        if (this.rawAction) await bounded(this.rawAction.catch(() => {}), 1500, 'old browser action did not stop');
        if (this.liveActions.size) await bounded(Promise.allSettled([...this.liveActions.values()].map(action => action.raw)), 1500, 'old live browser input did not stop');
        this.dialog = null;
        this.refs.clear();
        if (!this.closed) {
          const page = await bounded(this.createPage(), 3000);
          this.active = [...this.pages.values()].find(meta => meta.page === page)?.id ?? null;
        }
      } catch (error) {
        this.closed = true;
        await bounded(this.destroyContext(), 3000).catch(() => {});
        throw new HTTPError(503, `cancellation could not preserve the context; session closed: ${error.message}`);
      } finally {
        this.resetPromise = null;
      }
    })();
    return this.resetPromise;
  }

  async cancel() {
    this.touch();
    this.assertLive();
    if (this.cancelPromise) return this.cancelPromise;
    this.generation++;
    this.quantities.clear();
    this.activity.fence();
    this.phase = 'cancelling';
    const liveCleanup = this.fenceLiveInput();
    this.cancelPromise = (async () => {
      // A human now owns a suspended modal. Cancelling the agent must not
      // dismiss that dialog, close its page, or wait for the human's answer.
      const interrupted = (this.inOperation || Boolean(this.rawAction) || this.liveActions.size > 0 || [...this.pages.values()].some(meta => meta.liveInputBlocked || this.devtools.isBlocked(meta) || this.devtools.isPaused(meta))) && !(this.control === 'human' && this.dialog);
      try {
        await liveCleanup;
        if (interrupted) {
          this.devtools.prepareClose({ terminatePaused: false });
          // A queued timer can run after its paused stack is terminated but
          // before Chromium destroys the owner. Block new HTTP activity in
          // this context during that interval; other sessions stay online.
          await bounded(this.setContextOffline(true), 2000, 'cancel acknowledgement withheld: network isolation did not complete');
          await this.devtools.prepareClose();
          await this.resetPages();
          await bounded(this.setContextOffline(false), 2000, 'cancel acknowledgement withheld: network restoration did not complete');
        }
        await this.devtools.reset();
        await bounded(this.queue, 2000, 'cancel acknowledgement withheld: queue did not drain');
        this.phase = 'ready';
        return { ok: true, ...this.state(), text: interrupted ? 'Cancelled. Active pages were closed; context cookies remain.' : 'Cancelled. No active browser action; page preserved.' };
      } catch (error) {
        // Never acknowledge uncertain cancellation. Mark closed so the bridge
        // can require a successful DELETE cleanup before creating a replacement.
        this.close().catch(() => {});
        throw new HTTPError(503, error.message);
      } finally { this.cancelPromise = null; }
    })();
    return this.cancelPromise;
  }

  async setControl(body) {
    return this.transitionControl(body);
  }

  // Agent claims enter only through the validated, deduplicated execute queue.
  // Returning control through the UI still requires its existing human lease.
  async transitionControl(body, claim) {
    this.touch(); this.assertLive();
    if (!body || typeof body.tab_id !== 'string' || typeof body.context_ref !== 'string') throw new HTTPError(400, 'control requires tab_id and context_ref');
    const meta = this.selectedPage({ tab_id: body.tab_id, context_ref: body.context_ref });
    await meta.ownershipReady;
    const control = this.controlFor(meta);
    // Chromium does not expose an opener for native modifier/middle-click
    // tabs. They stay human with no lease until an authenticated visible
    // viewer initializes input. This cannot replace any existing owner.
    const adoption = body.adopt_if_unowned === true;
    if (body.adopt_if_unowned !== undefined && !adoption || !adoption && (body.generation !== undefined || body.control_epoch !== undefined)) throw new HTTPError(400, 'input initialization requires adopt_if_unowned true with generation and control_epoch');
    if (adoption && (body.mode !== 'human' || body.force === true || body.lease_token || body.context_ref !== this.contextRef ||
      body.generation !== this.generation || body.control_epoch !== 0 || control.mode !== 'human' || control.lease || control.epoch !== 0 || meta.ownershipPending || control.phase !== 'ready')) {
      throw new HTTPError(409, 'unassigned human page changed; refresh its state before initializing input', 'TAB_CONTROL_CHANGED');
    }
    if (claim && (meta.ownershipPending || control.epoch !== claim.expectedEpoch)) throw new HTTPError(409, 'page ownership changed; inspect tabs before claiming again', 'TAB_CONTROL_CHANGED');
    if (this.phase !== 'ready' || control.phase !== 'ready') throw new HTTPError(409, 'browser control transition is in progress');
    if (!['human', 'agent'].includes(body.mode)) throw new HTTPError(400, 'control mode must be human or agent');
    if ([...this.pages.values()].some(page => page.liveInputBlocked || this.devtools.isBlocked(page)) || [...this.liveActions.values()].some(action => action.uncertain)) throw new HTTPError(409, 'browser operation outcome is unknown; cancel the browser before changing control');
    if (this.rawAction && !this.currentOperation && !(this.rawActionMeta === meta && this.devtools.isPaused(meta)) && !this.dialog) throw new HTTPError(409, 'previous browser action outcome is pending; cancel before changing control');
    const previous = { ...control };
    if (claim && control.mode === 'agent') return { ok: true, ...this.state({ tab_id: meta.id, context_ref: this.contextRef }) };
    if (body.mode === 'human') {
      if (control.mode === 'human' && body.force !== true && !adoption) throw new HTTPError(409, 'this tab is already controlled by a human');
      meta.control = 'human'; meta.lease = randomBytes(24).toString('base64url');
    } else {
      if (control.mode !== 'human' || !claim && (!body.lease_token || body.lease_token !== control.lease)) throw new HTTPError(403, 'human control lease does not match this tab');
      if (this.dialog?.page === meta.page) throw new HTTPError(409, "resolve this tab\'s pending dialog before returning control");
    }
    const generation = this.generation, epoch = control.epoch + 1;
    meta.controlTransitionPrevious = previous;
    meta.controlEpoch = epoch; meta.controlPhase = 'takeover';
    this.invalidatePage(meta); this.quantities.clear();
    const lease = meta.lease;
    const operation = this.operationTouches(meta) ? this.currentOperation : null;
    // A claim is itself the queued raw action. Waiting for that promise here
    // would make a claim of the currently selected human Page await itself.
    const raw = !claim && this.rawActionMeta === meta ? this.rawAction : null;
    const liveCleanup = this.fenceLiveInput(meta);
    try {
      await liveCleanup;
      await this.devtools.detach(meta);
      if (operation) await bounded(operation.done, this.actionTimeout + 2000);
      const liveQueue = this.liveQueues.get(meta);
      if (liveQueue) await bounded(liveQueue, 2500);
      if (raw && this.dialog?.page !== meta.page) await bounded(raw.catch(() => {}), this.actionTimeout + 2000);
    } catch {
      meta.controlPhase = 'ready'; meta.control = previous.mode; meta.lease = previous.lease;
      delete meta.controlTransitionPrevious;
      this.publishLiveState(meta);
      if (meta.liveInputBlocked) throw new HTTPError(409, 'live input release could not be confirmed; cancel the browser before continuing');
      throw new HTTPError(409, "this tab\'s current action has not stopped; takeover withheld, retry or cancel explicitly");
    }
    if (this.closed || this.pages.get(meta.id) !== meta) throw new HTTPError(410, 'browser tab closed during takeover');
    if (generation !== this.generation || meta.controlEpoch !== epoch || meta.controlPhase !== 'takeover') throw new HTTPError(409, 'takeover superseded by cancellation');
    meta.controlPhase = 'ready';
    delete meta.controlTransitionPrevious;
    if (body.mode === 'agent') { meta.control = 'agent'; meta.lease = null; }
    this.publishLiveState(meta);
    return { ok: true, ...this.state({ tab_id: meta.id, context_ref: this.contextRef }), ...(body.mode === 'human' ? { lease_token: lease } : {}) };
  }

  async input(body) {
    this.touch();
    this.assertLive();
    const bound = ['target_tab', 'context_ref', 'generation'].some(key => body[key] !== undefined);
    let target;
    if (bound) {
      if (!Number.isSafeInteger(body.generation) || body.generation < 1 || typeof body.target_tab !== 'string' || typeof body.context_ref !== 'string') throw new HTTPError(400, 'targeted input requires target_tab, context_ref and generation');
      target = this.selectedPage({ tab_id: body.target_tab, context_ref: body.context_ref });
      if (body.generation !== this.generation) throw new HTTPError(409, 'browser generation changed; refresh the bound tab before input');
      if (body.type === 'tab') throw new HTTPError(400, 'bound tab creation and closing use the tabs endpoint; viewing does not switch the Agent target');
    }
    target ??= this.page();
    const control = this.controlFor(target);
    if (control.mode !== 'human' || !body.lease_token || body.lease_token !== control.lease) throw new HTTPError(403, 'input requires this tab\'s current human control lease');
    if ([...this.pages.values()].some(meta => meta.liveInputBlocked) && body.type !== 'dialog') throw new HTTPError(409, 'live input release is uncertain; cancel the browser before continuing');
    if (this.phase !== 'ready' || control.phase !== 'ready') throw new HTTPError(409, 'browser control transition is in progress');
    if (this.rawAction && (!this.currentOperation || this.operationTouches(target)) && !this.dialog) throw new HTTPError(504, 'previous browser input is still pending; cancel before continuing');
    if ([...this.liveActions.values()].some(action => action.uncertain)) throw new HTTPError(409, 'live input outcome is unknown; cancel before continuing');
    const epoch = this.generation;
    const lease = control.lease, controlEpoch = control.epoch;
    return this.enqueue(async () => {
      if (bound && (body.context_ref !== this.contextRef || body.generation !== this.generation || this.phase !== 'ready')) throw new HTTPError(409, 'targeted input was fenced by browser context or generation change');
      if (this.controlFor(target).mode !== 'human' || lease !== this.controlFor(target).lease) throw new HTTPError(403, 'targeted input requires this tab\'s current human control lease');
      this.guard(epoch, 'human', lease, target, controlEpoch);
      if ([...this.pages.values()].some(meta => meta.liveInputBlocked) && body.type !== 'dialog') throw new HTTPError(409, 'live input release is uncertain; cancel the browser before continuing');
      if (bound) {
        if (body.generation !== this.generation || this.selectedPage({ tab_id: body.target_tab, context_ref: body.context_ref }) !== target) throw new HTTPError(409, 'targeted input was fenced by browser context change');
        if (body.type === 'dialog' && this.dialog?.page !== target.page) throw new HTTPError(409, 'dialog belongs to another browser tab');
      }
      if (this.dialog && body.type !== 'dialog') throw new HTTPError(409, 'resolve the pending dialog first');
      if (this.rawAction && body.type !== 'dialog') throw new HTTPError(504, 'previous browser input is still pending; cancel before continuing');
      const previousActive = this.active;
      const operation = this.beginOperation(target, 'human');
      this.active = target.id;
      try {
      const page = this.page().page;
      if (body.type === 'dialog') {
        if (!this.dialog) throw new HTTPError(409, 'there is no pending dialog');
        const activity = this.activity.begin({ action: 'dialog' }, 'human');
        const dialog = this.dialog.value;
        this.dialog = null;
        const suspended = this.rawAction;
        // Track both the human answer and the click it resumes. An early
        // rejection must not lose a still-running promise from either side.
        const answer = Promise.resolve().then(() => body.accept === true ? dialog.accept(body.text ?? '') : dialog.dismiss());
        const raw = this.trackAction(Promise.allSettled([answer, suspended]).then(results => {
          if (results[0].status === 'rejected') throw results[0].reason;
        }));
        try {
          await bounded(raw, this.actionTimeout + 1500);
          await this.activity.finish(activity, true);
        } catch (error) {
          await this.activity.finish(activity, false);
          throw new HTTPError(this.rawAction ? 504 : 409, error.message);
        }
      } else {
        let action;
        switch (body.type) {
          case 'click': action = { action: 'click', x: body.x, y: body.y, version: body.version }; break;
          case 'key': action = { action: 'press', key: body.key }; break;
          case 'navigate': action = { action: 'navigate', url: body.url }; break;
          case 'back': action = { action: 'input_back' }; break;
          case 'tab': action = { action: 'tab', tab_action: body.tab_action, tab_id: body.tab_id, url: body.url }; break;
          case 'text':
            if (typeof body.text !== 'string' || body.text.length > 100_000) throw new HTTPError(400, 'text must be a bounded string');
            action = { action: 'input_text', text: body.text }; break;
          case 'scroll':
            if (!finite(body.deltaY) || (body.deltaX !== undefined && !finite(body.deltaX))) throw new HTTPError(400, 'scroll needs numeric deltas');
            action = { action: 'input_scroll', deltaX: Math.max(-50_000, Math.min(body.deltaX || 0, 50_000)), deltaY: Math.max(-50_000, Math.min(body.deltaY, 50_000)) }; break;
          default: throw new HTTPError(400, 'unsupported human input type');
        }
        if (action) {
          if (!action.action.startsWith('input_')) {
            try { validateAction(action); } catch (error) { throw new HTTPError(400, error.message); }
          }
          const result = await this.run(action, epoch, 'human', lease);
          if (!result.ok && !this.dialog) throw new HTTPError(this.rawAction ? 504 : 409, result.error);
        }
      }
      this.guard(epoch, 'human', lease, target, controlEpoch);
      operation.finish();
      this.active = this.pages.has(previousActive) ? previousActive : [...this.pages.values()].find(meta => this.controlFor(meta).mode === 'agent')?.id ?? null;
      return { ok: true, ...this.state({ tab_id: target.id, context_ref: this.contextRef }) };
      } finally {
        operation.finish();
        this.active = this.pages.has(previousActive) ? previousActive : [...this.pages.values()].find(meta => this.controlFor(meta).mode === 'agent')?.id ?? null;
      }
    });
  }

  async close() {
    if (this.closePromise) return this.closePromise;
    this.closed = true;
    this.favicons.close();
    this.phase = 'closed';
    this.lease = null;
    this.generation++;
    const liveCleanup = this.liveViews.size ? this.fenceLiveInput() : null;
    this.quantities.clear();
    this.activity.fence();
    // A failed hard cancellation may already mark the session closed. Still
    // retry context cleanup when DELETE/shutdown is requested.
    this.closePromise = bounded((async () => {
      // Context destruction still runs when a renderer cannot acknowledge
      // key release; failed cosmetic cleanup must never skip isolation.
      if (liveCleanup) await liveCleanup.catch(() => {});
      let cleanupError;
      let stage = 'isolation';
      this.devtools.prepareClose({ terminatePaused: false });
      try {
        if (!this.contextDestroyed) {
          await bounded(this.setContextOffline(true), 2000, 'browser destruction could not confirm network isolation');
          stage = 'termination';
          const stopping = this.devtools.prepareClose();
          if (stopping) await stopping;
        }
      } catch (error) { cleanupError = error; }
      try { await this.destroyContext(); }
      finally { await this.devtools.close(); await this.uploads.close(); }
      // Playwright may concurrently destroy its contexts on process signals.
      // A closed-target failure of setOffline is then superseded by confirmed
      // destruction. Never hide a real termination or network-isolation error.
      const alreadyDestroyed = stage === 'isolation' && this.contextDestroyed
        && /Target page, context or browser has been closed|Target closed|Context (?:has been )?closed|Browser (?:has been )?closed/i.test(cleanupError?.message ?? '');
      if (cleanupError && !alreadyDestroyed) throw cleanupError;
    })(), 5000).finally(() => { this.closePromise = null; });
    await this.closePromise;
  }
}
