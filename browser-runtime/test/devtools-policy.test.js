import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { BrowserDevTools, validateDevToolsAction } from '../src/devtools.js';

class Client extends EventEmitter {
  calls = [];
  bodies = new Map();
  children = new Map();
  async send(method, params = {}) {
    this.calls.push({ method, params });
    if (method === 'Target.sendMessageToTarget') {
      const child = this.children.get(params.sessionId);
      if (child) {
        const command = JSON.parse(params.message);
        Promise.resolve(child.send(command.method, command.params)).then(
          result => this.emit('Target.receivedMessageFromTarget', { sessionId: params.sessionId, message: JSON.stringify({ id: command.id, result }) }),
          cause => this.emit('Target.receivedMessageFromTarget', { sessionId: params.sessionId, message: JSON.stringify({ id: command.id, error: { code: -32601, message: cause.message } }) }),
        );
      }
      return {};
    }
    if (this.override) {
      const answer = this.override(method, params);
      if (answer !== undefined) return answer;
    }
    if (method === 'Network.getResponseBody') return this.bodies.get(params.requestId) ?? { body: 'body', base64Encoded: false };
    if (method === 'Target.getTargetInfo') return { targetInfo: { targetId: 'root-target', type: 'page', url: 'https://example.test/page' } };
    if (method === 'Page.getFrameTree') return { frameTree: { frame: { id: 'root-frame', loaderId: 'root-loader', url: 'https://example.test/page' } } };
    if (method === 'DOMStorage.getDOMStorageItems') return { entries: [] };
    if (method === 'Debugger.resume' || method === 'Debugger.disable') this.emit('Debugger.resumed', {});
    return {};
  }
  async detach() { this.detached = true; }
}

function fixture() {
  const clients = [];
  const meta = { id: 't1', title: 'Fixture', page: { url: () => 'https://example.test/page', isClosed: () => false } };
  const session = {
    generation: 1, actionTimeout: 100,
    context: { cookies: async () => [], newCDPSession: async () => { const client = new Client(); clients.push(client); return client; } },
    guard(epoch) { if (epoch !== this.generation) throw new Error('stale_generation'); },
    page: () => meta,
    viewport: () => ({ width: 1280, height: 800 }),
  };
  const tools = new BrowserDevTools(session);
  const execute = (method, params = {}, target) => tools.execute({ action: 'devtools', method, params, ...(target === undefined ? {} : { target }) }, session.generation);
  return { clients, meta, session, tools, execute };
}

const turn = () => new Promise(resolve => setImmediate(resolve));
function request(client, id, extra = {}) {
  client.emit('Network.requestWillBeSent', { requestId: id, loaderId: 'navigation-1', frameId: 'frame-1', type: 'Fetch', request: { url: `https://example.test/api/${id}`, method: 'GET', headers: {} }, ...extra });
}
function finish(client, id, mimeType = 'text/plain') {
  client.emit('Network.responseReceived', { requestId: id, response: { status: 200, mimeType, headers: {} } });
  client.emit('Network.loadingFinished', { requestId: id });
}

async function attachChild(f, id, type = 'worker') {
  await f.tools.attach(f.meta);
  const root = f.clients[0];
  const child = new Client();
  root.children.set(id, child);
  const originalEmit = child.emit.bind(child);
  child.emit = (method, params) => {
    root.emit('Target.receivedMessageFromTarget', { sessionId: id, message: JSON.stringify({ method, params }) });
    return originalEmit(method, params);
  };
  root.emit('Target.attachedToTarget', { sessionId: id, targetInfo: { targetId: id, type, url: `https://example.test/${id}` }, waitingForDebugger: true });
  for (let count = 0; count < 50; count++) {
    const target = (await f.execute('targets.list')).data.items.find(item => item.url.endsWith(`/${id}`) && item.ready);
    if (target) return { client: child, target, detach: () => root.emit('Target.detachedFromTarget', { sessionId: id, targetId: id }) };
    await turn();
  }
  throw new Error('test child did not attach');
}

test('policy admits page CDP and helpers while refusing host, browser, credential and routing capabilities', () => {
  for (const method of ['DOM.getDocument', 'CSS.getComputedStyleForNode', 'DOMSnapshot.captureSnapshot', 'Accessibility.getFullAXTree', 'Runtime.evaluate', 'Debugger.resume', 'Network.getResponseBody', 'Performance.getMetrics', 'Profiler.start', 'HeapProfiler.takeHeapSnapshot', 'Emulation.setEmulatedMedia', 'Schema.getDomains', 'help']) {
    assert.doesNotThrow(() => validateDevToolsAction({ action: 'devtools', method }));
  }
  for (const method of ['Browser.close', 'Target.getTargets', 'Storage.getCookies', 'DOMStorage.getDOMStorageItems', 'IO.read', 'Tracing.start', 'Fetch.enable', 'DOM.setFileInputFiles', 'Page.handleJavaScriptDialog', 'Page.setDownloadBehavior', 'Page.setInterceptFileChooserDialog', 'Network.getCookies', 'Network.getAllCookies', 'Network.setCookie', 'Network.setCookies', 'Network.deleteCookies', 'Network.clearBrowserCache', 'Network.clearBrowserCookies', 'Network.loadNetworkResource', 'Network.disable']) {
    assert.throws(() => validateDevToolsAction({ action: 'devtools', method }), /DENIED/);
  }
  for (const key of ['browserContextId', 'sessionId', 'targetId']) {
    assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'Runtime.evaluate', params: { [key]: 'foreign' } }), /CROSS_SESSION/);
  }
  assert.doesNotThrow(() => validateDevToolsAction({ action: 'devtools', method: 'Runtime.callFunctionOn', params: {
    functionDeclaration: 'function(data) { return data.sessionId }', objectId: 'page-object', arguments: [{ value: { sessionId: 'business-record', targetId: 'website-row', browserContextId: 'application-data' } }],
  } }));
});

test('navigation and bounded list parameters are validated without contacting Chromium', () => {
  for (const url of ['file:///etc/passwd', 'javascript:alert(1)', 'data:text/html,x', 'https://user:pass@example.test', '/relative']) {
    assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'Page.navigate', params: { url } }), /HTTP_NAVIGATION/);
  }
  assert.doesNotThrow(() => validateDevToolsAction({ action: 'devtools', method: 'Page.navigate', params: { url: 'https://example.test/page' } }));
  for (const params of [{ cursor: -1 }, { cursor: 1.5 }, { limit: 101 }, { limit: 0 }, { unknown: 1 }]) {
    assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'network.list', params }));
  }
  assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'Page.printToPDF', params: { transferMode: 'ReturnAsStream' } }), /STREAM_HANDLE/);
  for (const timeout of [-1, NaN, Infinity, '100']) assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'Runtime.evaluate', params: { timeout } }), /INVALID_TIMEOUT/);
  for (const target of ['', null, 1, {}, 'x'.repeat(161)]) assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'Runtime.evaluate', target }), /INVALID_TARGET/);
  assert.throws(() => validateDevToolsAction({ action: 'devtools', method: 'targets.list', params: { targetId: 'foreign' } }), /CROSS_SESSION/);
});

test('short cookie values cannot corrupt numeric CDP IDs, values, or protocol domain names', async () => {
  const f = fixture();
  f.session.context.cookies = async () => [{ name: 'sid', value: '1' }, { name: 'scope', value: 'DOM' }];
  await f.tools.attach(f.meta);
  f.clients[0].override = method => method === 'DOM.getDocument' ? { root: { nodeId: 1, backendNodeId: 1, nodeType: 1 } }
    : method === 'Schema.getDomains' ? { domains: [{ name: 'DOM', version: '1.3' }] }
      : method === 'Runtime.evaluate' ? { result: { type: 'number', value: 1 }, other: { type: 'string', value: '1' }, cookieText: { type: 'string', value: 'sid=1; scope=DOM' } } : undefined;
  assert.equal((await f.execute('DOM.getDocument')).data.result.root.nodeId, 1);
  assert.equal((await f.execute('Schema.getDomains')).data.result.domains[0].name, 'DOM');
  const result = (await f.execute('Runtime.evaluate', { expression: '1' })).data.result;
  assert.equal(result.result.value, 1);
  assert.equal(result.other.value, '[REDACTED]');
  assert.ok(!JSON.stringify(result).includes('sid=1'));
  assert.ok(!JSON.stringify(result).includes('scope=DOM'));
  await f.tools.close();
});

test('protocol handles survive credential collisions while business payloads remain redacted through execute and event replay', async () => {
  const f = fixture();
  f.session.context.cookies = async () => [{ name: 'sid', value: 'object' }];
  await f.tools.attach(f.meta);
  const client = f.clients[0];
  const business = { id: 'object', type: 'object', name: 'object', nested: { objectId: 'object' } };
  const remote = { type: 'object', objectId: 'fixture-object-handle', value: business };
  client.override = method => method === 'Runtime.evaluate' ? { result: remote }
    : method === 'Runtime.getProperties' ? { result: [{ name: 'object', value: { type: 'object', objectId: 'nested-object-handle' } }] } : undefined;
  const evaluated = (await f.execute('Runtime.evaluate', { expression: 'fixture' })).data.result.result;
  assert.equal(evaluated.type, 'object');
  assert.equal(evaluated.objectId, 'fixture-object-handle');
  assert.deepEqual(JSON.parse(JSON.stringify(evaluated.value)), { id: '[REDACTED]', type: '[REDACTED]', name: '[REDACTED]', nested: { objectId: '[REDACTED]' } });
  const property = (await f.execute('Runtime.getProperties', { objectId: evaluated.objectId })).data.result.result[0];
  assert.equal(property.name, 'object');
  assert.equal(property.value.type, 'object');
  assert.equal(property.value.objectId, 'nested-object-handle');

  client.emit('Runtime.consoleAPICalled', { type: 'log', args: [remote], executionContextId: 1 });
  const consoleArgument = (await f.execute('console.list')).data.items.at(-1).args[0];
  assert.equal(consoleArgument.objectId, 'fixture-object-handle');
  assert.equal(consoleArgument.type, 'object');
  assert.equal(consoleArgument.value.type, '[REDACTED]');
  const event = (await f.execute('events.list', { method: 'Runtime.consoleAPICalled' })).data.items.at(-1).params;
  assert.equal(event.args[0].objectId, 'fixture-object-handle');
  assert.equal(event.args[0].value.id, '[REDACTED]');

  request(client, 'payload', { request: { url: 'https://example.test/api', method: 'GET', headers: { type: 'object', id: 'object', name: 'object' } } });
  client.bodies.set('payload', { body: JSON.stringify(business), base64Encoded: false });
  finish(client, 'payload', 'application/json'); await turn();
  const item = (await f.execute('network.list')).data.items[0];
  assert.equal(item.headers.type, '[REDACTED]');
  assert.equal(item.headers.id, '[REDACTED]');
  assert.equal(item.headers.name, '[REDACTED]');
  const body = (await f.execute('network.read', { request_id: item.request_id })).data.request.body;
  assert.equal(JSON.parse(body.text).type, '[REDACTED]');
  assert.equal(JSON.parse(body.text).id, '[REDACTED]');
  assert.equal(JSON.parse(body.text).nested.objectId, '[REDACTED]');
  await f.tools.close();
});

test('a breakpoint suspends the active execution budget while paused diagnostics still have deadlines', async () => {
  const f = fixture(); f.session.actionTimeout = 50;
  let wake = 0;
  f.session.debuggerWake = () => wake++;
  await f.tools.attach(f.meta);
  const client = f.clients[0];
  let finishEvaluation;
  client.override = method => method === 'Runtime.evaluate' ? new Promise(resolve => { finishEvaluation = resolve; }) : undefined;
  const executing = f.execute('Runtime.evaluate', { expression: 'debugger; 42', timeout: 50 });
  await turn();
  client.emit('Debugger.paused', { reason: 'other', callFrames: [] });
  await new Promise(resolve => setTimeout(resolve, 90));
  assert.equal(f.tools.isBlocked(f.meta), false); assert.equal(wake, 1);
  assert.equal((await f.execute('events.list')).data.paused, true);
  assert.equal((await f.execute('Debugger.resume')).ok, true);
  finishEvaluation({ result: { type: 'number', value: 42 } });
  assert.equal((await executing).data.result.result.value, 42);
  client.emit('Debugger.paused', { reason: 'other', callFrames: [] });
  client.override = method => method === 'Debugger.evaluateOnCallFrame' ? new Promise(() => {}) : undefined;
  const stuck = await f.execute('Debugger.evaluateOnCallFrame', { callFrameId: 'frame-call', expression: 'while(true){}' });
  assert.equal(stuck.ok, false); assert.equal(f.tools.isBlocked(f.meta), true);
  await f.tools.close();
});

test('attachment is idempotent and enables collectors before readiness', async () => {
  const f = fixture();
  const first = f.tools.attach(f.meta);
  assert.equal(f.tools.attach(f.meta), first);
  await first;
  assert.equal(f.clients.length, 1);
  assert.deepEqual(new Set(f.clients[0].calls.map(call => call.method)), new Set(['Network.enable', 'Runtime.enable', 'Log.enable', 'Page.enable', 'Page.getFrameTree', 'Page.setLifecycleEventsEnabled', 'Target.getTargetInfo', 'Target.setAutoAttach']));
  await f.tools.close();
});

test('top-level loading uses only the existing root frame and matching document request', async () => {
  const f = fixture(), updates = [];
  f.session.startPageLoad = meta => updates.push(['start', meta.id]);
  f.session.finishPageLoad = (meta, error = '') => updates.push(['stop', meta.id, error]);
  await f.tools.attach(f.meta); const client = f.clients[0];
  client.emit('Page.frameStartedLoading', { frameId: 'child-frame' });
  request(client, 'fetch', { frameId: 'root-frame', type: 'Fetch' });
  client.emit('Network.loadingFailed', { requestId: 'fetch', errorText: 'private diagnostic' });
  assert.equal(updates.length, 0);
  client.emit('Page.frameStartedLoading', { frameId: 'root-frame' });
  request(client, 'older', { frameId: 'root-frame', type: 'Document' });
  request(client, 'newer', { frameId: 'root-frame', type: 'Document' });
  client.emit('Network.loadingFailed', { requestId: 'older', canceled: true });
  assert.deepEqual(updates, [['start', 't1'], ['start', 't1'], ['start', 't1']]);
  client.emit('Network.loadingFailed', { requestId: 'newer', errorText: 'https://private.test/?token=secret' });
  assert.deepEqual(updates.at(-1), ['stop', 't1', 'Page navigation failed.']);
  client.emit('Page.lifecycleEvent', { name: 'load', frameId: 'root-frame', loaderId: 'navigation-1' });
  assert.deepEqual(updates.at(-1), ['stop', 't1', '']);
  assert.equal(f.clients.length, 1);
  await f.tools.close();
});

test('old load, stop, and failed requests cannot finish a newer navigation before it commits', async () => {
  const f = fixture(); f.meta.pageLoadEpoch = 0; f.meta.pageLoading = false;
  f.session.startPageLoad = (meta, { newCycle = false } = {}) => { if (newCycle || !meta.pageLoading) meta.pageLoadEpoch++; meta.pageLoading = true; meta.pageLoadError = ''; };
  f.session.finishPageLoad = (meta, message, epoch) => { if (epoch === meta.pageLoadEpoch) { meta.pageLoading = false; if (message) meta.pageLoadError = message; } };
  await f.tools.attach(f.meta); const client = f.clients[0];
  client.emit('Page.frameStartedLoading', { frameId: 'root-frame' });
  request(client, 'older', { frameId: 'root-frame', type: 'Document', loaderId: 'loader-a' });
  client.emit('Page.frameStartedLoading', { frameId: 'root-frame' });
  client.emit('Page.frameStoppedLoading', { frameId: 'root-frame' });
  client.emit('Page.lifecycleEvent', { name: 'load', frameId: 'root-frame', loaderId: 'loader-a' });
  client.emit('Network.loadingFailed', { requestId: 'older', canceled: true });
  assert.equal(f.meta.pageLoading, true); assert.equal(f.meta.pageLoadError, '');
  request(client, 'newer', { frameId: 'root-frame', type: 'Document', loaderId: 'loader-b' });
  client.emit('Page.lifecycleEvent', { name: 'load', frameId: 'root-frame', loaderId: 'loader-a' });
  assert.equal(f.meta.pageLoading, true);
  client.emit('Page.lifecycleEvent', { name: 'load', frameId: 'root-frame', loaderId: 'loader-b' });
  assert.equal(f.meta.pageLoading, false);
  await f.tools.close();
});

test('compositor capture timeouts retain one underlying request and validate bounded JPEG data', async () => {
  const f = fixture(); await f.tools.attach(f.meta); const client = f.clients[0];
  let resolve;
  client.override = method => method === 'Page.captureScreenshot' ? new Promise(done => { resolve = done; }) : undefined;
  await assert.rejects(f.tools.capturePixels(f.meta, 10), /FRAME_CAPTURE_TIMEOUT/);
  for (let i = 0; i < 12; i++) await assert.rejects(f.tools.capturePixels(f.meta, 10), /FRAME_CAPTURE_PENDING/);
  const sent = client.calls.filter(call => call.method === 'Page.captureScreenshot');
  assert.equal(sent.length, 1); assert.equal(sent[0].params.captureBeyondViewport, false);
  assert.deepEqual(sent[0].params.clip, { x: 0, y: 0, width: 1280, height: 800, scale: 1 });
  resolve({ data: Buffer.from([0xff, 0xd8, 0xff]).toString('base64') }); await turn();
  client.override = method => method === 'Page.captureScreenshot' ? { data: 'not a JPEG' } : undefined;
  await assert.rejects(f.tools.capturePixels(f.meta, 10), /FRAME_CAPTURE_INVALID/);
  client.override = method => method === 'Page.captureScreenshot' ? { data: Buffer.from([0xff, 0xd8, 0xff]).toString('base64') } : undefined;
  assert.equal((await f.tools.capturePixels(f.meta, 10))[0], 0xff);
  await f.tools.close();
});

test('agent captures normalize each Sheet with its own realized native density', async () => {
  const f=fixture(); await f.tools.attach(f.meta); const client=f.clients[0];
  const sibling={ ...f.meta,id:'t2',page:{ url:()=> 'https://example.test/other',isClosed:()=>false } };
  const scales=new Map([[f.meta.page,2],[sibling.page,1.25]]);
  f.session.captureDeviceScaleFactor=1.5;
  f.session.host={ getCaptureScale:page=>scales.get(page) };
  client.override=method=>method==='Page.captureScreenshot'?{ data:Buffer.from([0xff,0xd8,0xff]).toString('base64') }:undefined;
  await f.tools.capturePixels(f.meta,100);
  await f.tools.attach(sibling); const siblingClient=f.clients.at(-1);
  siblingClient.override=client.override;
  await f.tools.capturePixels(sibling,100);
  assert.equal(client.calls.find(call=>call.method==='Page.captureScreenshot').params.clip.scale,0.5);
  assert.equal(siblingClient.calls.find(call=>call.method==='Page.captureScreenshot').params.clip.scale,0.8);
  assert.deepEqual(client.calls.find(call=>call.method==='Page.captureScreenshot').params.clip,{ x:0,y:0,width:1280,height:800,scale:0.5 });
  await f.tools.close();
});

test('network evidence uses a private ID namespace, preserves redirects, and reads do not refetch', async () => {
  const first = fixture(), second = fixture();
  await first.tools.attach(first.meta); await second.tools.attach(second.meta);
  const client = first.clients[0];
  request(client, 'same'); request(second.clients[0], 'same');
  const original = (await first.execute('network.list')).data.items[0];
  const foreign = (await second.execute('network.list')).data.items[0];
  assert.notEqual(original.request_id, foreign.request_id);
  assert.equal((await second.execute('network.read', { request_id: original.request_id })).ok, false);
  request(client, 'same', { redirectResponse: { status: 302, headers: { location: '/final' } }, request: { url: 'https://example.test/final', method: 'GET', headers: {} } });
  client.bodies.set('same', { body: 'captured only once', base64Encoded: false });
  finish(client, 'same'); await turn();
  const records = (await first.execute('network.list')).data.items;
  assert.equal(records.length, 2);
  assert.equal(records[1].redirect_from, original.request_id);
  assert.equal(records[1].redirect_index, 1);
  assert.equal(records[0].body.reason, 'redirect_response');
  assert.equal((await first.execute('network.read', { request_id: records[1].request_id })).data.request.body.text, 'captured only once');
  const raw = await first.execute('Network.getResponseBody', { requestId: 'same' });
  assert.deepEqual(raw.data.result, { body: 'captured only once', base64Encoded: false });
  assert.equal(client.calls.filter(call => call.method === 'Network.getResponseBody').length, 1);
  await first.tools.close(); await second.tools.close();
});

test('sensitive headers, queries, body fields and echoed cookie values are scrubbed without corrupting URLs', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  request(client, 'secret', { request: { url: 'https://example.test/api?access_token=query-credential', method: 'GET', headers: { Authorization: 'Bearer header-credential' } } });
  client.emit('Network.responseReceivedExtraInfo', { requestId: 'secret', statusCode: 200, headers: { 'Set-Cookie': 'sid=cookie-credential; Path=/; SameSite=Lax' } });
  client.bodies.set('secret', { body: JSON.stringify({ ok: true, token: 'body-credential', echo: 'cookie-credential query-credential header-credential' }), base64Encoded: false });
  finish(client, 'secret', 'application/json'); await turn();
  const list = await f.execute('network.list');
  assert.equal(new URL(list.data.items[0].url).pathname, '/api');
  const read = await f.execute('network.read', { request_id: list.data.items[0].request_id });
  for (const value of ['query-credential', 'header-credential', 'cookie-credential', 'body-credential']) assert.ok(!JSON.stringify({ list, read }).includes(value));
  assert.match(read.data.request.body.text, /REDACTED/);
  assert.equal(read.data.request.response.headers['Set-Cookie'], '[REDACTED]');
  await f.tools.close();
});

test('extra request headers arriving before the regular event are retained and redacted', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  client.emit('Network.requestWillBeSentExtraInfo', { requestId: 'before', headers: { 'X-Observed': 'yes', Cookie: 'sid=private-cookie' } });
  request(client, 'before');
  const entry = (await f.execute('network.list')).data.items[0];
  assert.equal(entry.headers['X-Observed'], 'yes');
  assert.equal(entry.headers.Cookie, '[REDACTED]');
  assert.equal(entry.request_headers_complete, true);
  await f.tools.close();
});

test('single bodies and total retention are bounded; binary and streaming states stay explicit', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  for (let i = 0; i < 18; i++) {
    const id = `large-${i}`;
    request(client, id); client.bodies.set(id, { body: 'x'.repeat(80 * 1024), base64Encoded: false }); finish(client, id); await turn();
  }
  const items = (await f.execute('network.list')).data.items;
  assert.equal(items[0].body.state, 'evicted');
  const last = (await f.execute('network.read', { request_id: items.at(-1).request_id })).data.request;
  assert.equal(last.body.state, 'truncated'); assert.equal(Buffer.byteLength(last.body.text), 64 * 1024);
  request(client, 'binary'); client.bodies.set('binary', { body: Buffer.from('binary secret').toString('base64'), base64Encoded: true }); finish(client, 'binary', 'application/octet-stream');
  request(client, 'stream'); client.emit('Network.responseReceived', { requestId: 'stream', response: { status: 200, mimeType: 'text/event-stream', headers: {} } });
  await turn();
  const after = (await f.execute('network.list')).data.items;
  assert.equal(after.find(item => item.cdp_request_id === 'binary').body.state, 'binary');
  assert.equal(after.find(item => item.cdp_request_id === 'stream').body.state, 'streaming');
  await f.tools.close();
});

test('bounded rings expose pagination, incremental cursors and evicted observations', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  request(client, 'initial');
  const start = (await f.execute('network.list')).data.cursor;
  for (let i = 0; i < 270; i++) request(client, `n-${i}`);
  const page = (await f.execute('network.list', { cursor: start, limit: 2 })).data;
  assert.equal(page.items.length, 2); assert.equal(page.cursor_lost, true); assert.equal(page.next_cursor, page.cursor);
  const next = (await f.execute('network.list', { cursor: page.cursor, limit: 2 })).data;
  assert.notEqual(next.items[0].request_id, page.items[0].request_id);
  const latest = f.tools.clients.get(f.meta.id).network.at(-1).seq;
  const empty = (await f.execute('network.list', { cursor: latest })).data;
  assert.equal(empty.cursor, latest); assert.equal(empty.next_cursor, null); assert.deepEqual(empty.items, []);
  await f.tools.close();
});

test('raw output is bounded and paused diagnostic reads do not execute page JavaScript', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  client.override = method => method === 'DOM.getDocument' ? { huge: 'x'.repeat(100000) } : undefined;
  const result = await f.execute('DOM.getDocument');
  assert.equal(result.data.state, 'truncated'); assert.ok(Buffer.byteLength(result.data.text) <= 64 * 1024);
  client.emit('Debugger.paused', { reason: 'debugCommand', callFrames: [] });
  assert.equal(f.tools.isPaused(f.meta), true);
  const events = await f.execute('events.list');
  assert.equal(events.data.paused, true);
  assert.equal(client.calls.filter(call => call.method === 'Runtime.evaluate').length, 0);
  assert.equal((await f.execute('Runtime.evaluate', { expression: '1' })).ok, false);
  assert.equal((await f.execute('Debugger.resume')).ok, true); assert.equal(f.tools.isPaused(f.meta), false);
  await f.tools.close();
});

test('a timed-out command blocks the page across detach/reset until the owner closes that page', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  const client = f.clients[0];
  client.override = method => method === 'Runtime.evaluate' ? new Promise(() => {}) : undefined;
  const reply = await f.execute('Runtime.evaluate', { expression: 'new Promise(() => {})', awaitPromise: true });
  assert.equal(reply.ok, false); assert.equal(reply.data.state, 'result_unknown_cancel_required');
  assert.equal(f.tools.isBlocked(f.meta), true); assert.equal(client.detached, true);
  assert.equal(client.calls.filter(call => call.method === 'Runtime.evaluate').length, 1);
  await f.tools.reset(); assert.equal(f.tools.isBlocked(f.meta), true);
  assert.equal((await f.execute('Runtime.evaluate', { expression: '1' })).ok, false);
  assert.equal(client.calls.filter(call => call.method === 'Runtime.evaluate').length, 1);
  const replacement = { ...f.meta, page: { ...f.meta.page } };
  assert.equal(f.tools.isBlocked(replacement), false);
  await f.tools.attach(replacement); await f.tools.close();
});

test('reset detaches its page clients, releases a normal pause, and never reuses evidence IDs', async () => {
  const f = fixture(); await f.tools.attach(f.meta);
  request(f.clients[0], 'old'); const old = (await f.execute('network.list')).data.items[0].request_id;
  f.clients[0].emit('Debugger.paused', { reason: 'debugCommand' });
  await f.tools.reset();
  assert.equal(f.clients[0].detached, true); assert.equal(f.tools.isPaused(f.meta), false);
  assert.ok(f.clients[0].calls.some(call => call.method === 'Debugger.resume'));
  await f.tools.attach(f.meta); request(f.clients[1], 'new');
  const current = (await f.execute('network.list')).data.items[0].request_id;
  assert.notEqual(current, old); assert.equal((await f.execute('network.read', { request_id: old })).ok, false);
  await f.tools.close();
});

test('target selection cannot cross families, fall back to the root, or survive detach and reset', async () => {
  const first = fixture(), second = fixture();
  const worker = await attachChild(first, 'worker');
  await second.tools.attach(second.meta);
  const root = (await first.execute('targets.list')).data.items.find(item => item.is_root);
  assert.equal(worker.target.parent_target_ref, root.target_ref);
  assert.equal(worker.target.type, 'worker');
  assert.match(worker.target.target_ref, /^dt_[0-9a-f]{24}_[1-9][0-9]*$/);
  for (const ref of [worker.target.target_ref, root.target_ref, 'foreign-id']) {
    const answer = await second.execute('Runtime.evaluate', { expression: '1' }, ref);
    assert.equal(answer.ok, false); assert.equal(answer.error, 'DEVTOOLS_TARGET_NOT_AVAILABLE');
  }
  assert.equal(second.clients[0].calls.filter(call => call.method === 'Runtime.evaluate').length, 0);
  worker.detach(); await turn();
  assert.equal((await first.execute('Runtime.evaluate', { expression: '1' }, worker.target.target_ref)).error, 'DEVTOOLS_TARGET_NOT_AVAILABLE');
  await first.tools.reset();
  assert.equal((await first.execute('Runtime.evaluate', { expression: '1' }, root.target_ref)).error, 'DEVTOOLS_TARGET_NOT_AVAILABLE');
  assert.equal(first.clients.reduce((sum, client) => sum + client.calls.filter(call => call.method === 'Runtime.evaluate').length, 0), 0);
  await first.tools.close(); await second.tools.close();
});

test('duplicate raw request IDs stay target-scoped while private evidence IDs can be read across the family', async () => {
  const f = fixture();
  const worker = await attachChild(f, 'worker'), iframe = await attachChild(f, 'frame', 'iframe');
  for (const [client, text] of [[f.clients[0], 'root body'], [worker.client, 'worker body'], [iframe.client, 'frame body']]) {
    request(client, 'same'); client.bodies.set('same', { body: text, base64Encoded: false }); finish(client, 'same');
  }
  await turn();
  const listed = await f.execute('network.list');
  assert.equal(listed.data.items.length, 3);
  assert.equal(new Set(listed.data.items.map(item => item.request_id)).size, 3);
  assert.equal(new Set(listed.data.items.map(item => item.target_ref)).size, 3);
  assert.equal((await f.execute('Network.getResponseBody', { requestId: 'same' })).data.result.body, 'root body');
  assert.equal((await f.execute('Network.getResponseBody', { requestId: 'same' }, worker.target.target_ref)).data.result.body, 'worker body');
  const workerRow = listed.data.items.find(item => item.target_ref === worker.target.target_ref);
  const read = await f.execute('network.read', { request_id: workerRow.request_id });
  assert.equal(read.data.request.body.text, 'worker body');
  assert.equal(read.data.request.target_type, 'worker');
  assert.equal((await f.execute('network.read', { request_id: workerRow.request_id }, iframe.target.target_ref)).error, 'DEVTOOLS_REQUEST_NOT_RECORDED_OR_EVICTED');
  assert.deepEqual((await f.execute('network.list', {}, worker.target.target_ref)).data.items.map(item => item.request_id), [workerRow.request_id]);
  const event = (await f.execute('events.list', { method: 'Network.requestWillBeSent' }, worker.target.target_ref)).data.items[0];
  assert.equal(event.request_id, workerRow.request_id); assert.equal(event.target_ref, worker.target.target_ref);
  for (const client of [f.clients[0], worker.client, iframe.client]) assert.equal(client.calls.filter(call => call.method === 'Network.getResponseBody').length, 1);
  await f.tools.close();
});

test('family collectors share retention limits and redact credentials observed on other targets', async () => {
  const f = fixture();
  const worker = await attachChild(f, 'worker'), iframe = await attachChild(f, 'frame', 'iframe');
  request(worker.client, 'credential', { request: { url: 'https://example.test/auth', method: 'GET', headers: { Authorization: 'Bearer shared-private-credential' } } });
  iframe.client.emit('Runtime.consoleAPICalled', { type: 'log', args: [{ type: 'string', value: 'shared-private-credential' }] });
  const logged = (await f.execute('console.list')).data.items.at(-1);
  assert.equal(logged.target_ref, iframe.target.target_ref); assert.equal(logged.args[0].value, '[REDACTED]');
  for (let index = 0; index < 18; index++) {
    const client = index % 2 ? worker.client : iframe.client;
    const id = `large-${index}`;
    request(client, id); client.bodies.set(id, { body: 'x'.repeat(80 * 1024), base64Encoded: false }); finish(client, id); await turn();
  }
  const bodyRows = (await f.execute('network.list')).data.items.filter(item => item.cdp_request_id.startsWith('large-'));
  assert.equal(bodyRows[0].body.state, 'evicted');
  assert.equal(bodyRows.at(-1).body.state, 'truncated');
  for (let index = 0; index < 270; index++) request(index % 2 ? worker.client : iframe.client, `row-${index}`);
  assert.equal(f.tools.clients.get(f.meta.id).network.length, 256);
  assert.equal(f.tools.clients.get(f.meta.id).family.bodyBytes, 0);
  await f.tools.close();
});

test('a paused child freezes root execution and a timed-out child blocks the whole tab family', async () => {
  const f = fixture(); f.session.actionTimeout = 50;
  const worker = await attachChild(f, 'worker');
  const root = f.clients[0];
  let resolveRoot;
  root.override = method => method === 'Runtime.evaluate' ? new Promise(resolve => { resolveRoot = resolve; }) : undefined;
  const evaluating = f.execute('Runtime.evaluate', { expression: 'waitForWorker()' });
  await turn();
  worker.client.emit('Debugger.paused', { reason: 'other', callFrames: [] });
  await new Promise(resolve => setTimeout(resolve, 90));
  assert.equal(f.tools.isPaused(f.meta), true); assert.equal(f.tools.isBlocked(f.meta), false);
  assert.equal((await f.execute('Debugger.resume', {}, worker.target.target_ref)).ok, true);
  resolveRoot({ result: { type: 'number', value: 42 } });
  assert.equal((await evaluating).data.result.result.value, 42);
  worker.client.override = method => method === 'Runtime.evaluate' ? new Promise(() => {}) : undefined;
  const failed = await f.execute('Runtime.evaluate', { expression: 'new Promise(() => {})', awaitPromise: true }, worker.target.target_ref);
  assert.equal(failed.data.state, 'result_unknown_cancel_required'); assert.equal(f.tools.isBlocked(f.meta), true);
  assert.equal(root.detached, true);
  assert.equal((await f.execute('targets.list')).ok, false);
  await f.tools.close();
});

test('a lost child transport fences dispatched commands but protocol rejections remain ordinary redacted errors', async () => {
  const f = fixture();
  const worker = await attachChild(f, 'worker');
  f.session.context.cookies = async () => [{ name: 'sid', value: 'private-error-credential' }];
  worker.client.override = method => method === 'DOM.getDocument' ? Promise.reject(new Error('Not supported: private-error-credential')) : undefined;
  const rejected = await f.execute('DOM.getDocument', {}, worker.target.target_ref);
  assert.equal(rejected.ok, false); assert.match(rejected.error, /CDP_TARGET_PROTOCOL_ERROR/);
  assert.ok(!rejected.error.includes('private-error-credential')); assert.equal(f.tools.isBlocked(f.meta), false);
  let sent;
  const dispatched = new Promise(resolve => { sent = resolve; });
  worker.client.override = method => method === 'Runtime.evaluate' ? (sent(), new Promise(() => {})) : undefined;
  const evaluating = f.execute('Runtime.evaluate', { expression: 'sideEffectThenWait()' }, worker.target.target_ref);
  await dispatched;
  worker.detach();
  const failed = await evaluating;
  assert.equal(failed.ok, false); assert.equal(failed.data.state, 'result_unknown_cancel_required');
  assert.equal(f.tools.isBlocked(f.meta), true);
  await f.tools.close();
});

test('intentional reset releases a pending child command without poisoning a preserved page', async () => {
  const f = fixture();
  const worker = await attachChild(f, 'worker');
  let sent;
  const dispatched = new Promise(resolve => { sent = resolve; });
  worker.client.override = method => method === 'Runtime.evaluate' ? (sent(), new Promise(() => {})) : undefined;
  const evaluating = f.execute('Runtime.evaluate', { expression: 'sideEffectThenWait()' }, worker.target.target_ref);
  await dispatched;
  f.session.generation++;
  await f.tools.reset();
  assert.equal((await evaluating).ok, false);
  assert.equal(f.tools.isBlocked(f.meta), false);
  assert.equal((await f.execute('targets.list')).ok, true);
  await f.tools.close();
});

test('a delayed initial frame tree cannot overwrite a newer authoritative root loader', async () => {
  const f = fixture(), loaders = [];
  let resolveTree;
  f.session.observeTopDocument = (_meta, frame) => { loaders.push(frame.loaderId); return true; };
  f.session.context.newCDPSession = async () => {
    const client = new Client(); f.clients.push(client);
    client.override = method => method === 'Page.getFrameTree' ? new Promise(resolve => { resolveTree = resolve; }) : undefined;
    return client;
  };
  const attached = f.tools.attach(f.meta);
  while (!resolveTree) await turn();
  f.clients[0].emit('Page.frameNavigated', { frame: { id: 'new-root', loaderId: 'new-loader', url: 'https://example.test/new' } });
  resolveTree({ frameTree: { frame: { id: 'old-root', loaderId: 'old-loader', url: 'https://example.test/old' } } });
  const root = await attached;
  assert.deepEqual(loaders, ['new-loader']);
  assert.equal(root.topFrameId, 'new-root');
  await f.tools.close();
});

test('a lost root debugging transport revokes document authority before a reconnect can establish it again', async () => {
  const f = fixture(), authority = [];
  f.session.observeTopDocument = (_meta, frame) => { authority.push('ready:' + frame.loaderId); return true; };
  f.session.loseDocumentIdentity = () => authority.push('lost');
  await f.tools.attach(f.meta);
  f.clients[0].emit('close');
  assert.deepEqual(authority, ['ready:root-loader', 'lost']);
  await f.tools.attach(f.meta);
  assert.deepEqual(authority, ['ready:root-loader', 'lost', 'ready:root-loader']);
  await f.tools.close();
});
