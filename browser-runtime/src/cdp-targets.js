// Page-owned CDP descendants, using only Playwright's public CDPSession API.
// The non-flat CDP tunnel is deprecated upstream; keep it behind this adapter
// so a future public flat-session transport can replace it without policy changes.
import { EventEmitter } from 'node:events';

const FILTER = [{ type: 'iframe' }, { type: 'worker' }, { exclude: true }];
const SUPPORTED = new Set(['iframe', 'worker']);
const validId = value => typeof value === 'string' && value.length > 0 && value.length <= 1024;
const failure = code => Object.assign(new Error(code), { code });

async function bounded(promise, timeoutMs, code = 'CDP_TARGET_TIMEOUT') {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(failure(code)), timeoutMs);
    })]);
  } finally { clearTimeout(timer); }
}

class ChildSession extends EventEmitter {
  constructor(tree, node) {
    super();
    this.tree = tree;
    this.node = node;
    this.sequence = 0;
    this.pending = new Map();
    this.closing = false;
    this.closed = false;
    this.paused = false;
  }

  send(method, params = {}, options = {}) { return this._send(method, params, options); }

  _send(method, params = {}, { timeoutMs = this.tree.commandTimeoutMs, cleanup = false } = {}) {
    if (this.closed || (this.closing && !cleanup)) return Promise.reject(failure('CDP_TARGET_DETACHED'));
    if (this.pending.size >= this.tree.maxPending) return Promise.reject(failure('CDP_TARGET_PENDING_LIMIT'));
    if (typeof method !== 'string' || !Number.isFinite(timeoutMs) || timeoutMs <= 0) return Promise.reject(failure('CDP_TARGET_INVALID_COMMAND'));
    const id = ++this.sequence;
    let message;
    try { message = JSON.stringify({ id, method, params }); }
    catch { return Promise.reject(failure('CDP_TARGET_INVALID_COMMAND')); }
    if (Buffer.byteLength(message) > this.tree.maxMessageBytes) return Promise.reject(failure('CDP_TARGET_MESSAGE_LIMIT'));
    return new Promise((resolve, reject) => {
      const startedPaused = this.tree.isPaused();
      let previous = performance.now();
      let remaining = timeoutMs;
      const entry = { resolve, reject, cleanup, timer: setInterval(() => {
        const now = performance.now();
        // A breakpoint stops the browser operation's active-time budget. Calls
        // made while already paused (inspection/resume) still have a deadline.
        if (cleanup || startedPaused || !this.tree.isPaused()) remaining -= now - previous;
        previous = now;
        if (remaining <= 0) this._finish(id, failure('CDP_TARGET_TIMEOUT'));
      }, Math.min(25, timeoutMs)) };
      this.pending.set(id, entry);
      this.tree._tunnel(this.node.parent, this.node.sessionId, message, cleanup)
        .catch(() => this._finish(id, failure('CDP_TARGET_TRANSPORT_ERROR')));
    });
  }

  _finish(id, cause, result) {
    const entry = this.pending.get(id);
    if (!entry) return;
    this.pending.delete(id);
    clearInterval(entry.timer);
    if (cause) entry.reject(cause);
    else entry.resolve(result ?? {});
  }

  _receive(message) {
    if (this.closed) return;
    if (Object.hasOwn(message, 'id')) {
      if (!Number.isSafeInteger(message.id)) return;
      let cause;
      if (message.error) {
        cause = failure('CDP_TARGET_PROTOCOL_ERROR');
        if (Number.isInteger(message.error.code)) cause.protocolCode = message.error.code;
        // Preserve useful standard protocol diagnostics for the caller's
        // redactor, while bounding untrusted browser-provided text here.
        const detail = typeof message.error.message === 'string' ? message.error.message.slice(0, 768) : '';
        cause.message += `${cause.protocolCode === undefined ? '' : ` (${cause.protocolCode})`}${detail ? `: ${detail}` : ''}`;
      }
      this._finish(message.id, cause, message.result);
      return;
    }
    if (typeof message.method !== 'string') return;
    if (this.closing) {
      // Nested cleanup replies still traverse a retiring ancestor. Ordinary
      // page events must stop, while owned transport lifecycle events drain.
      if (['Target.receivedMessageFromTarget', 'Target.attachedToTarget', 'Target.detachedFromTarget'].includes(message.method)) this.emit(message.method, message.params ?? {});
      return;
    }
    if (message.method === 'Debugger.paused') this.paused = true;
    if (message.method === 'Debugger.resumed') this.paused = false;
    this.emit(message.method, message.params ?? {});
    this.emit('event', { method: message.method, params: message.params ?? {} });
  }

  _beginClose() {
    this.closing = true;
    for (const [id, entry] of this.pending) if (!entry.cleanup) this._finish(id, failure('CDP_TARGET_DETACHED'));
  }

  _close() {
    if (this.closed) return;
    this.closed = this.closing = true;
    for (const id of this.pending.keys()) this._finish(id, failure('CDP_TARGET_DETACHED'));
    this.emit('close', this);
    this.removeAllListeners();
  }

  async detach() { await this.tree._retire(this.node, 'detached'); }
}

/**
 * Owns rootClient and only targets announced by its recursive auto-attach tree.
 * No browser-wide discovery, arbitrary target attachment, or browser endpoint.
 * onAttach must install collectors and enable their domains before it resolves;
 * only then is a paused new target allowed to execute its first script.
 */
export class TargetTree {
  constructor(rootClient, options = {}) {
    this.rootClient = rootClient;
    this.onAttach = options.onAttach ?? (() => {});
    this.onDetach = options.onDetach ?? (() => {});
    this.onInfo = options.onInfo ?? (() => {});
    this.onError = options.onError ?? (() => {});
    this.maxTargets = options.maxTargets ?? 32;
    this.maxPending = options.maxPending ?? 128;
    this.maxMessageBytes = options.maxMessageBytes ?? 16 * 1024 * 1024;
    this.commandTimeoutMs = options.commandTimeoutMs ?? 15000;
    this.startupTimeoutMs = options.startupTimeoutMs ?? 10000;
    this.isPaused = options.isPaused ?? (() => [...this.nodes.values()].some(node => node.descriptor.client.paused));
    for (const key of ['maxTargets', 'maxPending', 'maxMessageBytes', 'commandTimeoutMs', 'startupTimeoutMs']) {
      if (!Number.isSafeInteger(this[key]) || this[key] < 1) throw failure('CDP_TARGET_INVALID_OPTIONS');
    }
    this.nodes = new Map();
    this.root = null;
    this.rootNode = null;
    this.closed = false;
    this.initializing = new Set();
    this.rejected = { unsupported: 0, capacity: 0, malformed: 0, unknown_session: 0 };
  }

  start() {
    if (this.starting) return this.starting;
    this.starting = (async () => {
      if (this.closed) throw failure('CDP_TARGET_TREE_CLOSED');
      const { targetInfo } = await bounded(this.rootClient.send('Target.getTargetInfo'), this.startupTimeoutMs);
      if (this.closed) throw failure('CDP_TARGET_TREE_CLOSED');
      if (!targetInfo || !validId(targetInfo.targetId) || targetInfo.type !== 'page') throw failure('CDP_TARGET_ROOT_NOT_PAGE');
      this.root = { client: this.rootClient, targetId: targetInfo.targetId, type: 'page', url: targetInfo.url ?? '', parentTargetId: null, initialized: false };
      this.rootNode = this._node(this.root, null, null);
      this.nodes.set(this.root.targetId, this.rootNode);
      this._listen(this.rootNode);
      await bounded(this._autoAttach(this.rootClient), this.startupTimeoutMs, 'CDP_TARGET_AUTO_ATTACH_UNAVAILABLE');
      // Existing descendants are delivered before setAutoAttach completes.
      await bounded(Promise.allSettled([...this.initializing]), this.startupTimeoutMs);
      if (this.closed) throw failure('CDP_TARGET_TREE_CLOSED');
      this.root.initialized = true;
      return this;
    })().catch(async cause => { await this.close(); throw cause; });
    this.starting.catch(() => {});
    return this.starting;
  }

  list() { return [...this.nodes.values()].filter(node => !node.retiring).map(node => node.descriptor); }
  get(targetId) { const node = this.nodes.get(targetId); return node && !node.retiring ? node.descriptor : undefined; }

  _node(descriptor, parent, sessionId) {
    const node = { descriptor, parent, sessionId, children: new Map(), listeners: [], retiring: false, announced: false };
    node.cancelled = new Promise(resolve => { node.cancel = resolve; });
    return node;
  }

  _report(cause, descriptor) {
    try { Promise.resolve(this.onError(cause, descriptor)).catch(() => {}); }
    catch { /* Callback failures never escape CDP event dispatch. */ }
  }

  _listen(node) {
    const client = node.descriptor.client;
    const listeners = {
      'Target.attachedToTarget': event => this._attached(node, event),
      'Target.detachedFromTarget': event => {
        const child = node.children.get(event?.sessionId);
        if (child) this._retire(child, 'target_closed', false).catch(cause => this._report(cause, child.descriptor));
      },
      'Target.receivedMessageFromTarget': event => this._received(node, event),
      'Target.targetInfoChanged': event => {
        const child = this.nodes.get(event?.targetInfo?.targetId);
        if (child && (child === node || child.parent === node)) this._info(child, event.targetInfo.url);
      },
      'Page.frameNavigated': event => { if (!event?.frame?.parentId) this._info(node, event?.frame?.url); },
      close: () => {
        if (node === this.rootNode) this.close().catch(cause => this._report(cause));
        else this._retire(node, 'target_closed', false).catch(cause => this._report(cause, node.descriptor));
      },
    };
    for (const [event, listener] of Object.entries(listeners)) {
      client.on(event, listener);
      node.listeners.push([event, listener]);
    }
  }

  _info(node, url) {
    if (node.retiring || typeof url !== 'string' || node.descriptor.url === url) return;
    node.descriptor.url = url;
    try { Promise.resolve(this.onInfo(node.descriptor)).catch(cause => this._report(cause, node.descriptor)); }
    catch (cause) { this._report(cause, node.descriptor); }
  }

  _autoAttach(client) {
    return client.send('Target.setAutoAttach', { autoAttach: true, waitForDebuggerOnStart: true, flatten: false, filter: FILTER });
  }

  _tunnel(parent, sessionId, message, cleanup = false) {
    if (this.destroying) {
      let command;
      try {
        command = JSON.parse(message);
        while (command.method === 'Target.sendMessageToTarget') command = JSON.parse(command.params.message);
      } catch { return Promise.reject(failure('CDP_TARGET_DETACHED')); }
      if (command.method !== 'Debugger.resume' || command.params?.terminateOnResume !== true) return Promise.reject(failure('CDP_TARGET_DETACHED'));
    }
    const params = { sessionId, message };
    const client = parent.descriptor.client;
    return client instanceof ChildSession
      ? client._send('Target.sendMessageToTarget', params, { cleanup, timeoutMs: cleanup ? 500 : this.commandTimeoutMs })
      : client.send('Target.sendMessageToTarget', params);
  }

  _received(parent, event) {
    const child = parent.children.get(event?.sessionId);
    if (!child) { this.rejected.unknown_session++; return; }
    if (typeof event.message !== 'string' || Buffer.byteLength(event.message) > this.maxMessageBytes) {
      this._report(failure('CDP_TARGET_MESSAGE_LIMIT'), child.descriptor);
      this._retire(child, 'invalid_message').catch(() => {});
      return;
    }
    let message;
    try { message = JSON.parse(event.message); }
    catch { this.rejected.malformed++; return; }
    if (!message || typeof message !== 'object' || Array.isArray(message) || Object.hasOwn(message, 'sessionId')) {
      this.rejected.malformed++;
      return;
    }
    child.descriptor.client._receive(message);
  }

  _attached(parent, event) {
    // The owner is destroying the page/context. A newly attached worker must
    // stay suspended until destruction, never be resumed by reject cleanup.
    if (this.destroying) return;
    const info = event?.targetInfo;
    const sessionId = event?.sessionId;
    if (!validId(sessionId)) { this.rejected.malformed++; return; }
    if (parent.children.has(sessionId)) return;
    const existing = this.nodes.get(info?.targetId);
    const atCapacity = this.list().length >= this.maxTargets;
    if (this.closed || parent.retiring || !info || !validId(info.targetId) || !SUPPORTED.has(info.type) || (existing && !existing.retiring) || atCapacity) {
      const reason = atCapacity ? 'capacity' : !SUPPORTED.has(info?.type) ? 'unsupported' : 'malformed';
      this.rejected[reason]++;
      if (reason === 'capacity') this._report(failure('CDP_TARGET_LIMIT'), parent.descriptor);
      // Even a rejected new worker must not be left waiting for its debugger.
      this._releaseRejected(parent, sessionId).catch(() => {});
      return;
    }
    const descriptor = { client: null, targetId: info.targetId, type: info.type, url: info.url ?? '', parentTargetId: parent.descriptor.targetId, initialized: false };
    const node = this._node(descriptor, parent, sessionId);
    descriptor.client = new ChildSession(this, node);
    parent.children.set(sessionId, node);
    this.nodes.set(info.targetId, node);
    this._listen(node);
    node.announced = true;
    // The callback runs synchronously through its first await, before any child
    // domain can emit startup evidence or nested attachment events.
    let attaching;
    try { attaching = Promise.resolve(this.onAttach(descriptor)); }
    catch (cause) { attaching = Promise.reject(cause); }
    const initializing = (async () => {
      try {
        await bounded(Promise.race([attaching, node.cancelled.then(() => { throw failure('CDP_TARGET_DETACHED'); })]), this.startupTimeoutMs, 'CDP_TARGET_STARTUP_TIMEOUT');
        if (node.retiring || this.closed) return;
        await this._autoAttach(descriptor.client);
        if (node.retiring || this.closed) return;
        await descriptor.client.send('Runtime.runIfWaitingForDebugger');
        if (!node.retiring && !this.closed) descriptor.initialized = true;
      } catch (cause) {
        if (!node.retiring) this._report(cause, descriptor);
        await this._retire(node, 'initialization_failed');
      }
    })();
    descriptor.ready = initializing;
    this.initializing.add(initializing);
    initializing.finally(() => this.initializing.delete(initializing)).catch(() => {});
  }

  async _releaseRejected(parent, sessionId) {
    if (this.destroying) return;
    const message = JSON.stringify({ id: 1, method: 'Runtime.runIfWaitingForDebugger', params: {} });
    try { await bounded(this._tunnel(parent, sessionId, message, true), 500); } catch {}
    if (this.destroying) return;
    try {
      const client = parent.descriptor.client;
      const params = { sessionId };
      await bounded(client instanceof ChildSession ? client._send('Target.detachFromTarget', params, { cleanup: true, timeoutMs: 500 }) : client.send('Target.detachFromTarget', params), 500);
    } catch {}
  }

  _beginRetire(node) {
    if (node.retiring) return;
    node.retiring = true;
    node.descriptor.initialized = false;
    node.cancel();
    if (node.descriptor.client instanceof ChildSession) node.descriptor.client._beginClose();
    for (const child of node.children.values()) this._beginRetire(child);
  }

  _retire(node, reason, remote = true, end = performance.now() + 1000) {
    if (node.retirement) return node.retirement;
    this._beginRetire(node);
    node.retirement = (async () => {
      await Promise.all([...node.children.values()].map(child => this._retire(child, reason, remote, end)));
      const client = node.descriptor.client;
      const timeout = () => Math.max(1, Math.min(300, end - performance.now()));
      if (remote && !this.destroying) {
        // Chromium can leave a startup-paused target suspended after detach.
        // Resume before removing the session (also done by Playwright itself).
        await client._send('Runtime.runIfWaitingForDebugger', {}, { cleanup: true, timeoutMs: timeout() }).catch(() => {});
        if (client.paused && !this.destroying) await client._send('Debugger.resume', {}, { cleanup: true, timeoutMs: timeout() }).catch(() => {});
        const parentClient = node.parent.descriptor.client;
        const params = { sessionId: node.sessionId };
        if (!this.destroying) await bounded(parentClient instanceof ChildSession ? parentClient._send('Target.detachFromTarget', params, { cleanup: true, timeoutMs: timeout() }) : parentClient.send('Target.detachFromTarget', params), timeout()).catch(() => {});
      }
      for (const [event, listener] of node.listeners) client.off(event, listener);
      node.listeners.length = 0;
      client._close();
      node.parent.children.delete(node.sessionId);
      if (this.nodes.get(node.descriptor.targetId) === node) this.nodes.delete(node.descriptor.targetId);
      if (node.announced) {
        try { await bounded(Promise.resolve(this.onDetach(node.descriptor, reason)), timeout(), 'CDP_TARGET_DETACH_CALLBACK_TIMEOUT'); }
        catch (cause) { this._report(cause, node.descriptor); }
      }
    })();
    node.retirement.catch(() => {});
    return node.retirement;
  }

  prepareClose() {
    // Synchronous fence for destructive cancellation. Detaching a debugger or
    // disabling auto-attach can resume script execution, so do neither until
    // the owner has destroyed this tree's page/context.
    this.destroying = true;
    this.closed = true;
    if (this.rootNode) this._beginRetire(this.rootNode);
  }

  close() {
    if (this.closing) return this.closing;
    this.closed = true;
    if (this.rootNode) this._beginRetire(this.rootNode);
    this.closing = (async () => {
      if (this.rootNode) {
        await Promise.all([...this.rootNode.children.values()].map(child => this._retire(child, 'tree_closed', !this.destroying)));
        if (!this.destroying) await bounded(this.rootClient.send('Target.setAutoAttach', { autoAttach: false, waitForDebuggerOnStart: false, flatten: false }), 500).catch(() => {});
        for (const [event, listener] of this.rootNode.listeners) this.rootClient.off(event, listener);
        this.rootNode.listeners.length = 0;
      }
      if (!this.destroying) await bounded(this.rootClient.detach(), 500).catch(() => {});
      this.nodes.clear();
    })();
    return this.closing;
  }
}
