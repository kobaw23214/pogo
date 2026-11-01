import test from 'node:test';
import assert from 'node:assert/strict';
import { WebSocketServer } from 'ws';
import { chromium } from 'playwright';
import { BootstrapTransport, connectNativeBootstrap, initialBootstrapTarget } from '../src/native-bootstrap.js';

const initialURL = 'data:text/html;charset=utf-8,' + encodeURIComponent('<title>pogo-private-' + 'a'.repeat(32) + '</title>');
const target = { type: 'page', targetId: 'owned-initial-target', url: initialURL };
const internalUI = [1, 2].map(index => ({ type: 'browser_ui', targetId: `owned-ui-${index}`, url: 'chrome://omnibox-popup.top-chrome/' }));
const componentWorkers = ['chrome-extension://nkeimhogjdpnpccoofpliimaahmaaome/thunk.js', 'chrome-extension://glbjnfimcajjenihimblfaponejbkoph/background.js'].map((url, index) => ({ type: 'service_worker', targetId: `owned-component-${index}`, url, browserContextId: 'owned-context' }));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function fixture(t, { targets = () => [target] } = {}) {
  const server = new WebSocketServer({ port: 0, host: '127.0.0.1' });
  await new Promise(resolve => server.once('listening', resolve));
  let opened = 0, closed = 0; const messages = [];
  server.on('connection', socket => {
    const attempt = ++opened;
    socket.once('close', () => { closed++; });
    socket.on('message', bytes => {
      const message = JSON.parse(String(bytes)); messages.push(message);
      socket.send(JSON.stringify({ id: message.id, result: message.method === 'Target.getTargets' ? { targetInfos: targets(attempt) } : {} }));
    });
  });
  t.after(async () => { for (const socket of server.clients) socket.terminate(); await new Promise(resolve => server.close(resolve)); });
  const endpoint = `ws://127.0.0.1:${server.address().port}/devtools/browser/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa`;
  const browser = transport => {
    const page = { url: () => initialURL, isClosed: () => false }, context = { pages: () => [page] };
    return { contexts: () => [context], newBrowserCDPSession: async () => ({ send: method => transport.read(method) }), close: () => transport.closeConfirmed() };
  };
  return { endpoint, browser, messages, opened: () => opened, closed: () => closed };
}

test('initial target validation permits one private keeper and rejects other pages/targets/identity changes', () => {
  assert.equal(initialBootstrapTarget([target], initialURL), target.targetId);
  assert.equal(initialBootstrapTarget([{ ...target, url: 'about:blank' }], initialURL), target.targetId);
  for (const rows of [[], [target, target], [{ ...target, url: 'https://business.invalid' }], [target, { type: 'worker' }], [{ ...target, targetId: '' }]]) assert.throws(() => initialBootstrapTarget(rows, initialURL), /TARGETS_CHANGED/);
  assert.throws(() => initialBootstrapTarget([target], initialURL, 'different-original-target'), /TARGETS_CHANGED/);
});

test('Chrome 153 internal browser UI targets coexist with the same single private keeper', () => {
  assert.equal(initialBootstrapTarget([target, ...internalUI], initialURL, target.targetId), target.targetId);
});

test('browser UI target classification rejects network, extension and malformed internal URLs', () => {
  for (const url of ['http://business.invalid/', 'https://business.invalid/', 'chrome-extension://extension/', 'chrome:omnibox-popup.top-chrome', 'chrome:///', 'chrome://user@omnibox-popup.top-chrome/', 'chrome://omnibox-popup.top-chrome:1234/']) {
    assert.throws(() => initialBootstrapTarget([target, { ...internalUI[0], url }], initialURL), /TARGETS_CHANGED/);
  }
});

test('internal UI targets never authorize an additional page, foreign keeper or changed identity', () => {
  for (const rows of [[target, ...internalUI, { ...target, targetId: 'extra' }], [{ ...target, url: 'https://business.invalid/' }, ...internalUI]]) {
    assert.throws(() => initialBootstrapTarget(rows, initialURL, target.targetId), /TARGETS_CHANGED/);
  }
  assert.throws(() => initialBootstrapTarget([target, ...internalUI], initialURL, 'different-original-target'), /TARGETS_CHANGED/);
});

test('only observed built-in component workers in the private keeper context are admitted', () => {
  const page = { ...target, browserContextId: 'owned-context' };
  assert.equal(initialBootstrapTarget([page, ...internalUI, ...componentWorkers], initialURL, target.targetId, 'owned-context'), target.targetId);
  for (const worker of [{ ...componentWorkers[0], url: 'chrome-extension://' + 'b'.repeat(32) + '/thunk.js' }, { ...componentWorkers[0], url: 'https://business.invalid/thunk.js' }, { ...componentWorkers[0], type: 'worker' }, { ...componentWorkers[0], browserContextId: 'foreign-context' }, ...['?token=value', '#fragment', '/'].map(suffix => ({ ...componentWorkers[0], url: componentWorkers[0].url + suffix })), { ...componentWorkers[0], url: componentWorkers[0].url.replace('://', '://user@') }, { ...componentWorkers[0], url: componentWorkers[0].url.replace('/thunk.js', ':1234/thunk.js') }]) {
    assert.throws(() => initialBootstrapTarget([page, ...internalUI, worker], initialURL, target.targetId, 'owned-context'), /TARGETS_CHANGED/);
  }
  assert.throws(() => initialBootstrapTarget([page, ...componentWorkers, { ...page, targetId: 'second-page' }], initialURL, target.targetId, 'owned-context'), /TARGETS_CHANGED/);
  assert.throws(() => initialBootstrapTarget([{ ...page, browserContextId: 'changed-context' }, ...internalUI], initialURL, target.targetId, 'owned-context'), /TARGETS_CHANGED/);
});

test('trusted internal UI may appear before the private page only within the fixed preflight budget', async t => {
  let reads = 0, calls = 0;
  const f = await fixture(t, { targets: () => ++reads === 1 ? internalUI : [target, ...internalUI] });
  const boot = await connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true,
    connectOverCDP: async transport => { calls++; assert.ok(reads >= 2); return f.browser(transport); } });
  assert.equal(calls, 1); assert.equal(f.opened(), 1); await boot.transport.closeConfirmed();
  const foreign = await fixture(t, { targets: () => [{ ...internalUI[0], url: 'https://business.invalid/' }] });
  await assert.rejects(connectNativeBootstrap({ wsEndpoint: foreign.endpoint, initialURL, isLive: () => true,
    connectOverCDP: async () => { throw new Error('foreign UI must never reach attachment'); } }), /TARGETS_CHANGED/);
  assert.equal(foreign.opened(), 1);
});

test('successful bootstrap uses public custom transport and preserves all default connection options', async t => {
  const f = await fixture(t), diagnostics = []; let calls = 0;
  const boot = await connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true, diagnostics,
    connectOverCDP: async (transport, options) => { calls++; assert.ok(options.timeout > 0 && options.timeout <= 5000); assert.deepEqual(Object.keys(options), ['timeout']); return f.browser(transport); } });
  assert.equal(calls, 1); assert.equal(boot.keeper.url(), initialURL); assert.equal(f.opened(), 1);
  assert.ok(f.messages.every(message => message.method === 'Target.getTargets'), 'bootstrap never navigates, launches or replays an action');
  assert.ok(diagnostics.every(row => Object.keys(row).every(key => ['attempt', 'event', 'elapsed_ms', 'method', 'id'].includes(key))));
  assert.ok(!JSON.stringify(diagnostics).includes('pogo-private')); await boot.transport.closeConfirmed();
});

test('coalesced CDP lifecycle messages yield to execution-context microtasks between callbacks', async t => {
  const f = await fixture(t), transport = new BootstrapTransport(f.endpoint, [], 1), events = [];
  await transport.openPromise; let contextWaiterReady = false;
  transport.onmessage = message => {
    events.push(message.method);
    if (message.method === 'Page.frameNavigated') Promise.resolve().then(() => { contextWaiterReady = true; events.push('context-waiter-microtask'); });
    if (message.method === 'Runtime.executionContextCreated') assert.equal(contextWaiterReady, true, 'new context must arrive after the prior lifecycle microtask');
  };
  for (const method of ['Page.frameNavigated', 'Runtime.executionContextCreated']) transport.socket.emit('message', Buffer.from(JSON.stringify({ method, params: {} })));
  assert.deepEqual(events, [], 'socket delivery never invokes the SDK synchronously');
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(events, ['Page.frameNavigated', 'context-waiter-microtask', 'Runtime.executionContextCreated']);
  await transport.closeConfirmed();
});

test('buffered SDK messages and later socket messages dispatch in FIFO with one microtask turn each', async t => {
  const f = await fixture(t), transport = new BootstrapTransport(f.endpoint, [], 1), events = [];
  await transport.openPromise;
  const deliver = id => transport.socket.emit('message', Buffer.from(JSON.stringify({ id, result: {} })));
  deliver(1); deliver(2);
  transport.onmessage = message => { events.push(message.id); Promise.resolve().then(() => events.push(`microtask-${message.id}`)); };
  deliver(3); assert.deepEqual(events, []);
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(events, [1, 'microtask-1', 2, 'microtask-2', 3, 'microtask-3']);
  assert.equal(transport.inbox.length, 0); assert.equal(transport.inboxBytes, 0);
  await transport.closeConfirmed();
});

test('closing an owned transport suppresses queued and late SDK callbacks and notifies close once', async t => {
  const f = await fixture(t), transport = new BootstrapTransport(f.endpoint, [], 1); let messages = 0, closes = 0;
  await transport.openPromise; transport.onmessage = () => { messages++; }; transport.onclose = () => { closes++; };
  const event = Buffer.from(JSON.stringify({ method: 'Runtime.executionContextCreated', params: {} }));
  transport.socket.emit('message', event); await transport.closeConfirmed(); transport.socket.emit('message', event);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(messages, 0); assert.equal(closes, 1); assert.equal(transport.dispatches.size, 0);
});

test('a throwing SDK callback fails and closes only its owned transport before the next queued callback', async t => {
  const f = await fixture(t), transport = new BootstrapTransport(f.endpoint, [], 1); let callbacks = 0;
  await transport.openPromise;
  transport.onmessage = () => { callbacks++; throw new Error('synthetic SDK callback failure'); };
  for (let id = 1; id <= 2; id++) transport.socket.emit('message', Buffer.from(JSON.stringify({ id, result: {} })));
  await assert.rejects(transport.failure, /PROTOCOL_INVALID/); await transport.closedPromise;
  assert.equal(callbacks, 1); assert.equal(transport.closed, true); assert.equal(transport.dispatches.size, 0);
});

test('a genuine first handshake timeout closes its socket before a second attachment to the same endpoint', async t => {
  const f = await fixture(t); let calls = 0, resolveOld, lateCloses = 0; const transports = [];
  const started = Date.now();
  const boot = await connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true,
    connectOverCDP: async transport => { transports.push(transport); if (++calls === 1) return new Promise(resolve => { resolveOld = resolve; }); assert.equal(transports[0].closed, true); return f.browser(transport); } });
  assert.equal(calls, 2); assert.equal(f.opened(), 2); assert.ok(Date.now() - started < 15000); assert.equal(transports[0].socket.url, transports[1].socket.url);
  resolveOld({ close: async () => { lateCloses++; boot.transport.close(); } }); await pause(10);
  assert.equal(lateCloses, 0, 'obsolete handles cannot close Chrome or the new attachment'); assert.equal(boot.transport.closed, false);
  await boot.transport.closeConfirmed();
});

test('cancellation fences a late successful attachment and cannot publish or retry it', async t => {
  const f = await fixture(t); let live = true, calls = 0, resolve;
  const connecting = connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => live,
    connectOverCDP: transport => { calls++; const value = f.browser(transport); return new Promise(done => { resolve = () => done(value); }); } });
  while (!resolve) await pause(5); live = false;
  await assert.rejects(connecting, /CANCELLED/); assert.equal(calls, 1); resolve(); await pause(10); assert.equal(f.opened(), 1);
});

test('a changed target between attempts refuses attachment instead of navigating a replacement keeper', async t => {
  const f = await fixture(t, { targets: attempt => [{ ...target, targetId: attempt === 1 ? target.targetId : 'replacement' }] }); let calls = 0;
  await assert.rejects(connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true,
    connectOverCDP: async () => { calls++; const error = new Error('synthetic timeout'); error.name = 'TimeoutError'; throw error; } }), /TARGETS_CHANGED/);
  assert.equal(calls, 1); assert.equal(f.opened(), 2);
});

test('unconfirmed old socket close never authorizes a second connection', async t => {
  const f = await fixture(t), original = BootstrapTransport.prototype.closeConfirmed; let calls = 0;
  BootstrapTransport.prototype.closeConfirmed = async function() { this.socket.terminate(); throw Object.assign(new Error('NATIVE_BOOTSTRAP_CLOSE_UNKNOWN'), { code: 'NATIVE_BOOTSTRAP_CLOSE_UNKNOWN' }); };
  try {
    await assert.rejects(connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true,
      connectOverCDP: async () => { calls++; const error = new Error('synthetic timeout'); error.name = 'TimeoutError'; throw error; } }), /CLOSE_UNKNOWN/);
    assert.equal(calls, 1); assert.equal(f.opened(), 1);
  } finally { BootstrapTransport.prototype.closeConfirmed = original; }
});

test('bootstrap ends within its shared remaining budget and cannot accept unsafe endpoints/pages', async t => {
  const f = await fixture(t); let calls = 0; const started = Date.now();
  await assert.rejects(connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL, isLive: () => true, deadlineMs: 100,
    connectOverCDP: async () => { calls++; return new Promise(() => {}); } }), /TIMEOUT|CLOSE_UNKNOWN/);
  assert.ok(Date.now() - started < 500); assert.equal(calls, 1);
  for (const endpoint of ['ws://public.invalid:1234/devtools/browser/aaa', f.endpoint + '?token=secret', f.endpoint.replace('ws:', 'wss:')]) await assert.rejects(connectNativeBootstrap({ wsEndpoint: endpoint, initialURL, isLive: () => true, connectOverCDP() {} }), /ENDPOINT_INVALID/);
  await assert.rejects(connectNativeBootstrap({ wsEndpoint: f.endpoint, initialURL: 'https://business.invalid', isLive: () => true, connectOverCDP() {} }), /INITIAL_PAGE_INVALID/);
});

test('installed Playwright public custom transport completes a real CDP handshake without browser launch or target creation', async t => {
  const server = new WebSocketServer({ port: 0, host: '127.0.0.1' });
  await new Promise(resolve => server.once('listening', resolve));
  const commands = [], workerContexts = [], info = { ...target, browserContextId: 'owned-context', title: 'Private keeper', attached: true };
  server.on('connection', socket => socket.on('message', bytes => {
    const request = JSON.parse(String(bytes)); commands.push(request.method);
    const send = value => socket.send(JSON.stringify({ ...value, ...(request.sessionId ? { sessionId: request.sessionId } : {}) }));
    let result = {};
    if (request.method === 'Target.getTargets') result = { targetInfos: [info, ...internalUI, ...(request.id < 0 ? [] : componentWorkers)] };
    if (request.method === 'Browser.getVersion') result = { product: 'Chrome/153.0.0.0', revision: 'controlled', userAgent: 'Mozilla/5.0 Chrome/153.0.0.0', protocolVersion: '1.3' };
    if (request.method === 'Target.getTargetInfo') result = { targetInfo: request.sessionId ? info : { type: 'browser', targetId: 'owned-browser', url: '', browserContextId: 'owned-context' } };
    if (request.method === 'Target.setAutoAttach' && !request.sessionId && request.params.autoAttach) {
      // One socket write coalesces attachment and child Runtime events. The
      // installed SDK creates each child session in an attachment microtask;
      // a synchronous transport drops that child's sole context event.
      socket._socket.cork();
      socket.send(JSON.stringify({ method: 'Target.attachedToTarget', params: { sessionId: 'owned-page-session', targetInfo: info, waitingForDebugger: true } }));
      for (const [index, worker] of componentWorkers.entries()) {
        const sessionId = `owned-component-session-${index}`;
        socket.send(JSON.stringify({ method: 'Target.attachedToTarget', params: { sessionId, targetInfo: worker, waitingForDebugger: true } }));
        socket.send(JSON.stringify({ sessionId, method: 'Runtime.executionContextCreated', params: { context: { id: 71 + index, origin: worker.url, name: '', uniqueId: `owned-worker-${index}` } } }));
        socket.send(JSON.stringify({ sessionId, method: 'Inspector.workerScriptLoaded', params: {} }));
      }
      socket._socket.uncork();
    }
    if (request.method === 'Page.getFrameTree') result = { frameTree: { frame: { id: target.targetId, loaderId: 'owned-loader', url: initialURL, mimeType: 'text/html', securityOrigin: 'null' } } };
    if (request.method === 'Browser.getWindowForTarget') result = { windowId: 1, bounds: { left: 0, top: 0, width: 1280, height: 800, windowState: 'normal' } };
    if (request.method === 'Target.attachToBrowserTarget') result = { sessionId: 'owned-browser-session' };
    if (request.method === 'Page.createIsolatedWorld') result = { executionContextId: 2 };
    const isWorker = request.sessionId?.startsWith('owned-component-session-');
    if (request.method === 'Runtime.enable' && !isWorker) send({ method: 'Runtime.executionContextCreated', params: { context: { id: 1, origin: 'null', name: '', uniqueId: 'owned-main', auxData: { isDefault: true, type: 'default', frameId: target.targetId } } } });
    if (request.method === 'Runtime.evaluate' && isWorker) { workerContexts.push(request.params.contextId); result = { result: { type: 'object', objectId: `worker-utility-${request.sessionId}` } }; }
    if (request.method === 'Runtime.callFunctionOn' && isWorker) result = { result: { type: 'number', value: 42 } };
    send({ id: request.id, result });
  }));
  t.after(async () => { for (const socket of server.clients) socket.terminate(); await new Promise(resolve => server.close(resolve)); });
  const boot = await connectNativeBootstrap({ wsEndpoint: `ws://127.0.0.1:${server.address().port}/devtools/browser/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa`, initialURL,
    isLive: () => true, connectOverCDP: (transport, options) => chromium.connectOverCDP(transport, options) });
  assert.equal(boot.keeper.url(), initialURL); assert.ok(commands.includes('Browser.getVersion')); assert.ok(commands.includes('Runtime.runIfWaitingForDebugger'));
  assert.equal(commands.includes('Target.createTarget'), false); assert.equal(commands.includes('Page.navigate'), false);
  try {
    const workers = boot.context.serviceWorkers(); assert.equal(workers.length, 2);
    for (const worker of workers) {
      let timer;
      try { assert.equal(await Promise.race([worker.evaluate(() => 42), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('sole coalesced worker execution context was lost')), 1000); })]), 42); }
      finally { clearTimeout(timer); }
    }
    assert.deepEqual(workerContexts, [71, 72], 'actual SDK used both sole early child contexts');
  } finally { await boot.browser.close(); await boot.transport.closeConfirmed(); }
  assert.equal(commands.includes('Browser.close'), false);
});
