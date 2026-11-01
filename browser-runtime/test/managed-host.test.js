import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { BrowserSession } from '../src/session.js';
import { createWorker } from '../src/server.js';

function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }

// Deliberately keep raw context/page lifecycle unusable. A managed operation
// must not quietly fall back to the underlying shared browser on any failure.
function managedFixture() {
  const calls = [], owned = new Set(), scripts = [];
  let next = 0, session;
  const forbidden = name => () => { calls.push(`FORBIDDEN:${name}`); throw new Error(`raw ${name} must not be called`); };
  const context = Object.assign(new EventEmitter(), {
    setDefaultTimeout() {}, setDefaultNavigationTimeout() {},
    addInitScript: async (...args) => { scripts.push(args); },
    newPage: forbidden('newPage'), close: forbidden('close'), setOffline: forbidden('setOffline'),
  });
  function page() {
    const value = Object.assign(new EventEmitter(), {
      serial: ++next, closed: false, url: () => 'about:blank', isClosed() { return this.closed; },
      title: async () => '', waitForLoadState: async () => {}, bringToFront: async () => {},
      close: forbidden('page.close'),
    });
    value.mainFrame = () => ({ page: () => value });
    return value;
  }
  function finishPage(value) { value.closed = true; value.emit('close'); owned.delete(value); }
  const host = {
    ownsPage: value => owned.has(value),
    bindSession(value) {
      calls.push('bind'); session = value;
      assert.equal(value.pages.size, 0, 'bind precedes init and page creation');
      value.devtools.attach = async () => {};
    },
    async newPage() {
      const value = page(); calls.push(`new:${value.serial}`); owned.add(value); context.emit('page', value); return value;
    },
    async closePage(value) { assert.ok(owned.has(value)); calls.push(`closePage:${value.serial}`); finishPage(value); },
    async setOffline(offline) { calls.push(`offline:${offline}`); },
    async close() {
      calls.push('close'); for (const value of [...owned]) finishPage(value); context.emit('close');
    },
  };
  return { context, host, calls, owned, scripts, page, get session() { return session; },
    async init() { const value = new BrowserSession(context, { host, actionTimeout: 100, settleMs: 0 }); host.bindSession(value); return value.init(); } };
}

function assertNoFallback(fixture) { assert.deepEqual(fixture.calls.filter(call => call.startsWith('FORBIDDEN:')), []); }

test('managed hosts are explicit and cannot silently launch a hidden browser', async () => {
  await assert.rejects(createWorker({ token: 'managed-test', allowBrowserLaunch: false }), /browser launch is disabled/);
  await assert.rejects(createWorker({ token: 'managed-test', sessionFactory() {}, isConnected: () => true }), /allowBrowserLaunch:false/);
  await assert.rejects(createWorker({ token: 'managed-test', sessionFactory() {}, allowBrowserLaunch: false }), /isConnected/);
  const fixture = managedFixture();
  for (const method of ['ownsPage', 'newPage', 'closePage', 'setOffline', 'close']) {
    assert.throws(() => new BrowserSession(fixture.context, { host: { ...fixture.host, [method]: undefined } }), new RegExp(`requires ${method}`));
  }
  assertNoFallback(fixture);
});

test('managed attachment is owned, idempotent, and does not track another session requests', async () => {
  const fixture = managedFixture(), session = await fixture.init();
  try {
    assert.equal(fixture.scripts.length, 4, 'picker, link navigation, evidence and revision collectors retain scoped init scripts');
    const own = [...fixture.owned][0], meta = [...session.pages.values()][0];
    assert.equal(session.attach(own), meta);
    fixture.context.emit('page', own);
    const foreign = fixture.page(); fixture.context.emit('page', foreign);
    assert.equal(session.pages.size, 1); assert.equal(session.nextTab, 2);
    const request = { resourceType: () => 'fetch', frame: () => ({ page: () => foreign }), isNavigationRequest: () => false };
    fixture.context.emit('request', request);
    assert.equal(session.inflight.size, 0);
    assert.equal(foreign.listenerCount('dialog'), 0, 'foreign pages must not acquire a dialog listener or CDP collector');
    assert.equal(foreign.closed, false);
  } finally { await session.close(); }
  assertNoFallback(fixture);
});

test('a host can register ownership after the transport page event without losing the tab', async () => {
  const fixture = managedFixture();
  fixture.host.newPage = async () => { const page = fixture.page(); fixture.context.emit('page', page); fixture.owned.add(page); return page; };
  const session = await fixture.init();
  assert.equal(session.pages.size, 1); assert.equal(session.active, 't1');
  await session.close(); assertNoFallback(fixture);
});

test('agent/UI tab lifecycle and cancellation affect only the managed owner', async () => {
  const first = managedFixture(), second = managedFixture();
  const a = await first.init(), b = await second.init(), other = [...second.owned][0];
  try {
    await a.tab({ tab_action: 'new' }); assert.equal(a.active, 't2');
    await a.tab({ tab_action: 'close' }); assert.equal(a.active, 't1');
    const body = { op_id: 'owned-new', action: 'new', context_ref: a.contextRef };
    const created = await a.manageTabs(body);
    assert.equal(a.active, 't1'); assert.equal(created.tab_id, 't3');
    assert.deepEqual(await a.manageTabs(body), created); assert.equal(first.owned.size, 2);
    const human = { ...created.state, lease_token: created.lease_token };
    assert.equal(human.control, 'human');
    await a.manageTabs({ op_id: 'owned-close', action: 'close', context_ref: a.contextRef, tab_id: 't3', lease_token: human.lease_token });
    assert.equal(a.state({ tab_id: 't1', context_ref: a.contextRef }).control, 'agent');
    a.inOperation = true;
    await a.cancel(); a.inOperation = false;
    assert.equal(first.owned.size, 1); assert.equal(a.active, 't4');
    assert.ok(first.calls.includes('offline:true')); assert.ok(first.calls.includes('offline:false'));
    assert.deepEqual(second.calls, ['bind', 'new:1']); assert.equal(other.closed, false);
  } finally { await a.close(); await b.close(); }
  assertNoFallback(first); assertNoFallback(second);
});

test('late owned page creation is fenced and closed after a control generation change', async () => {
  const fixture = managedFixture(), session = await fixture.init(), pending = deferred();
  const original = fixture.host.newPage;
  fixture.host.newPage = async () => { await pending.promise; return original(); };
  const result = session.createPage(); session.generation++; pending.resolve();
  await assert.rejects(result, /creation was fenced/);
  assert.equal(fixture.owned.size, 1); assert.equal(session.pages.size, 1);
  await session.close(); assertNoFallback(fixture);
});

test('managed lifecycle errors never fall back to shared context operations', async () => {
  const fixture = managedFixture(), session = await fixture.init();
  fixture.host.setOffline = async () => { throw new Error('owned isolation failed'); };
  session.inOperation = true;
  await assert.rejects(session.cancel(), /owned isolation failed/);
  await session.close().catch(() => {});
  assert.equal(session.closed, true); assert.ok(fixture.calls.includes('close'));
  assertNoFallback(fixture);
});

test('factory worker creates managed sessions once and owns neither global browser nor context', async t => {
  const browser = new EventEmitter(); let newContexts = 0, browserCloses = 0, connected = true;
  browser.newContext = async () => { newContexts++; throw new Error('global newContext forbidden'); };
  browser.close = async () => { browserCloses++; };
  browser.isConnected = () => connected;
  const fixtures = new Map(); let creations = 0;
  const worker = await createWorker({ token: 'managed-test', browser, allowBrowserLaunch: false,
    sessionFactory: async ({ id, browser: actualBrowser }) => {
      creations++; assert.equal(actualBrowser, browser);
      const fixture = managedFixture(); fixtures.set(id, fixture);
      await new Promise(resolve => setImmediate(resolve));
      return fixture;
    } });
  await worker.listen(0); t.after(() => worker.close());
  const request = async (path, method = 'GET') => {
    const response = await fetch(`http://127.0.0.1:${worker.server.address().port}${path}`, { method, headers: { Authorization: 'Bearer managed-test' } });
    return { status: response.status, data: await response.json() };
  };
  const [one, duplicate] = await Promise.all([request('/sessions/a', 'PUT'), request('/sessions/a', 'PUT')]);
  assert.equal(one.status, 200); assert.equal(duplicate.status, 200); assert.equal(creations, 1);
  assert.equal((await request('/sessions/b', 'PUT')).status, 200); assert.equal(creations, 2);
  assert.equal((await request('/healthz')).status, 200); connected = false;
  assert.equal((await request('/healthz')).status, 503);
  assert.equal((await request('/sessions/a', 'DELETE')).status, 200);
  assert.equal(fixtures.get('b').session.closed, false);
  assert.equal(newContexts, 0); assert.equal(browserCloses, 0);
  for (const fixture of fixtures.values()) assertNoFallback(fixture);
});

test('browserless factory reports its host health and cleans failed init through the host only', async t => {
  let connected = true; const fixtures = [];
  const worker = await createWorker({ token: 'managed-test', allowBrowserLaunch: false, isConnected: () => connected,
    sessionFactory: async () => {
      const fixture = managedFixture(); fixtures.push(fixture);
      fixture.host.newPage = async () => { throw new Error('visible view creation failed'); };
      return fixture;
    } });
  await worker.listen(0); t.after(() => worker.close());
  const request = async (path, method = 'GET') => {
    const response = await fetch(`http://127.0.0.1:${worker.server.address().port}${path}`, { method, headers: { Authorization: 'Bearer managed-test' } });
    return { status: response.status, data: await response.json() };
  };
  assert.equal((await request('/healthz')).status, 200); connected = false;
  assert.equal((await request('/healthz')).status, 503);
  const failed = await request('/sessions/fail', 'PUT');
  assert.equal(failed.status, 500); assert.match(failed.data.error, /visible view creation failed/);
  assert.deepEqual(fixtures[0].calls, ['bind', 'offline:true', 'close']);
  assertNoFallback(fixtures[0]);
});

test('managed viewport controls state, pixels, coordinate capabilities, picker and overlays', async () => {
  const fixture = managedFixture(); let viewport = { width: 320, height: 240 };
  fixture.host.getViewport = () => viewport;
  const session = await fixture.init(), meta = session.page(), page = meta.page;
  const frame = { page: () => page, url: () => 'about:blank' };
  page.mainFrame = () => frame; page.frames = () => [];
  session.refreshRevision = async () => session.version(meta);
  const captures = []; let provideDocumentIdentity = false;
  session.devtools.attach = async () => {
    if (provideDocumentIdentity) session.observeTopDocument(meta, { id: `managed-frame-${page.serial}`, loaderId: `managed-document-${page.serial}` });
    return { client: { async send(method, params) { captures.push({ method, params }); return { data: Buffer.from([0xff, 0xd8, 0xff]).toString('base64') }; } } };
  };
  try {
    assert.deepEqual(session.state().viewport, viewport);
    assert.doesNotThrow(() => session.coordinates(319, 239));
    assert.throws(() => session.coordinates(320, 239), /320×240/);
    const version = session.version(), context_ref = session.contextRef;
    await assert.rejects(session.picker.pick({ x: 320, y: 10, tab_id: meta.id, context_ref, version }), /inside the selected page viewport/);
    session.picker.hit = async () => ({ frame, offset: { x: 0, y: 0 }, bounds: { x: 0, y: 0, ...viewport }, el: {
      evaluate: async () => ({ summary: { identity: 1, tag: 'button', role: 'button', name: 'Owned button' }, rect: { x: 300, y: 200, width: 100, height: 100 } }),
      dispose: async () => {},
    } });
    const pick = await session.picker.pick({ x: 310, y: 220, tab_id: meta.id, context_ref, version });
    assert.deepEqual(pick.viewport, viewport); assert.deepEqual(pick.element.rect, { x: 300, y: 200, width: 20, height: 40 });
    session.activity.target = { value: '#target', tab: meta.id, document: meta.version };
    session.locator = async () => ({ boundingBox: async () => ({ x: 300, y: 200, width: 100, height: 100 }) });
    const overlay = await session.activity.overlay(version);
    assert.deepEqual(overlay.viewport, viewport); assert.deepEqual(overlay.target.rect, pick.element.rect);
    const unavailable = await session.execute({ op_id: 'managed-screenshot-without-identity', action: 'screenshot' });
    assert.equal(unavailable.ok, false); assert.match(unavailable.error, /DOCUMENT_IDENTITY_UNAVAILABLE/);
    assert.equal(captures.filter(call => call.method === 'Page.captureScreenshot').length, 0, 'managed hosts also fail closed before document identity is established');
    provideDocumentIdentity = true;
    const screenshot = await session.execute({ op_id: 'managed-screenshot', action: 'screenshot' });
    assert.equal(screenshot.ok, true, screenshot.error); assert.equal(screenshot.screenshot.width, 320); assert.equal(screenshot.screenshot.height, 240);
    assert.deepEqual(captures.find(call => call.method === 'Page.captureScreenshot').params.clip, { x: 0, y: 0, width: 320, height: 240, scale: 1 });
    viewport = { width: 1600, height: 1000 };
    assert.notEqual(session.version(), version, 'resize immediately retires the old coordinate version');
    assert.doesNotThrow(() => session.coordinates(1500, 900));
    viewport = { width: 0, height: 240 };
    assert.throws(() => session.state(), /invalid viewport/);
  } finally { viewport = { width: 320, height: 240 }; await session.close(); }
  assertNoFallback(fixture);
});

test('factory metadata binds a private worker session to one client and profile without accepting paths or extra fields', async t => {
  const received = [], profile = 'a'.repeat(64);
  const worker = await createWorker({ token: 'managed-test', allowBrowserLaunch: false, isConnected: () => true,
    sessionFactory: async args => { received.push(args); return managedFixture(); } });
  await worker.listen(0); t.after(() => worker.close());
  const request = async (id, body, method = 'PUT') => {
    const response = await fetch(`http://127.0.0.1:${worker.server.address().port}/sessions/${id}`, {
      method, headers: { Authorization: 'Bearer managed-test', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    return { status: response.status, data: await response.json() };
  };
  const metadata = { client_session_id: 'studio-session_123', profile_key: profile };
  assert.equal((await request('private-worker', metadata)).status, 200);
  assert.equal(received[0].id, 'private-worker');
  assert.equal(received[0].clientSessionId, 'studio-session_123'); assert.equal(received[0].profileKey, profile);
  assert.equal((await request('private-worker', metadata)).status, 200); assert.equal(received.length, 1);
  assert.equal((await request('private-worker', { ...metadata, client_session_id: 'another-studio' })).status, 409);
  assert.equal((await request('private-worker', { ...metadata, profile_key: 'b'.repeat(64) })).status, 409);
  assert.equal((await request('private-worker', {})).status, 409);
  for (const invalid of [
    { client_session_id: 'studio' }, { profile_key: profile }, { ...metadata, extra: true },
    { ...metadata, profile_key: '/Users/private/profile' }, { ...metadata, profile_key: 'A'.repeat(64) },
    { ...metadata, client_session_id: '../another' }, { ...metadata, client_session_id: 'x'.repeat(161) },
    { ...metadata, client_session_id: null },
  ]) assert.equal((await request('invalid', invalid)).status, 400, JSON.stringify(invalid));
  assert.equal(received.length, 1, 'invalid or conflicting metadata must never create a session');
  assert.equal((await request('legacy-experiment', undefined)).status, 200);
  assert.equal(received[1].clientSessionId, undefined); assert.equal(received[1].profileKey, undefined);
  assert.equal((await request('private-worker', undefined, 'DELETE')).status, 200);
  assert.equal((await request('private-worker', { ...metadata, profile_key: 'b'.repeat(64) })).status, 200, 'confirmed deletion releases the old binding');
  assert.equal(received[2].profileKey, 'b'.repeat(64));
});

test('concurrent DELETE acknowledgements wait for confirmed managed source destruction', async t => {
  const fixture = managedFixture(), entered = deferred(), retiring = deferred(); let closes = 0;
  const close = fixture.host.close.bind(fixture.host);
  fixture.host.close = async () => { closes++; entered.resolve(); await retiring.promise; await close(); };
  const worker = await createWorker({ token: 'managed-delete-barrier', allowBrowserLaunch: false, isConnected: () => true, sessionFactory: async () => fixture });
  await worker.listen(0); t.after(() => worker.close());
  const url = `http://127.0.0.1:${worker.server.address().port}/sessions/delete-barrier`;
  const request = method => fetch(url, { method, headers: { Authorization: 'Bearer managed-delete-barrier' } });
  const created = await request('PUT'); assert.equal(created.status, 200, JSON.stringify(await created.json()));
  let firstDone = false, secondDone = false;
  const a = request('DELETE').then(response => { firstDone = true; return response; });
  await entered.promise;
  const b = request('DELETE').then(response => { secondDone = true; return response; });
  try {
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(firstDone, false, 'first DELETE must await source retirement');
    assert.equal(secondDone, false, 'a concurrent DELETE must not treat closing as destroyed');
    assert.equal(closes, 1, 'source cleanup is shared, not executed twice');
  } finally { retiring.resolve(); }
  for (const response of await Promise.all([a, b])) assert.equal(response.status, 200, JSON.stringify(await response.json()));
  const gone = await fetch(url + '/state', { headers: { Authorization: 'Bearer managed-delete-barrier' } });
  assert.equal(gone.status, 404);
});

test('workspace download routes require authorization, isolate session GUIDs and stream only completed payloads', async t => {
  const { BrowserDownloads } = await import('../src/downloads.js');
  const { mkdtemp, writeFile } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { join } = await import('node:path');
  const { randomUUID } = await import('node:crypto');
  const stores = new Map();
  const worker = await createWorker({ token: 'downloads-route-test', allowBrowserLaunch: false, isConnected: () => true, sessionFactory: async ({ id }) => {
    const fixture = managedFixture(), directory = await mkdtemp(join(tmpdir(), 'pogo-download-route-'));
    const client = Object.assign(new EventEmitter(), { send: async () => ({}) });
    const downloads = await new BrowserDownloads({ client, directory, ownerForFrame: () => ({ tab_id: 't1', context_ref: id, control: 'human' }) }).start();
    stores.set(id, { downloads, directory }); fixture.host.downloads = downloads;
    const close = fixture.host.close; fixture.host.close = async () => { await downloads.close(); await close(); }; return fixture;
  } });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`; t.after(() => worker.close());
  const request = (path, method = 'GET', authorized = true) => fetch(base + path, { method, headers: authorized ? { Authorization: 'Bearer downloads-route-test' } : {} });
  assert.equal((await request('/sessions/a', 'PUT')).status, 200); assert.equal((await request('/sessions/b', 'PUT')).status, 200);
  const { downloads, directory } = stores.get('a'), id = randomUUID();
  downloads.begin({ guid: id, frameId: 'owned', suggestedFilename: 'report.xlsx', url: 'https://example.test/report' });
  assert.equal((await request(`/sessions/a/downloads/${id}/content`)).status, 409);
  await writeFile(join(directory, id), 'synthetic-file'); await downloads.progress({ guid: id, state: 'completed' });
  assert.equal((await request('/sessions/a/downloads', 'GET', false)).status, 401);
  assert.equal((await request(`/sessions/b/downloads/${id}/content`)).status, 404);
  assert.equal((await request('/sessions/b/downloads')).status, 200);
  assert.equal((await (await request('/sessions/a/downloads')).json()).downloads[0].id, id);
  const content = await request(`/sessions/a/downloads/${id}/content`); assert.equal(content.status, 200); assert.equal(content.headers.get('content-length'), '14'); assert.equal(await content.text(), 'synthetic-file');
  assert.equal((await request('/sessions/a/downloads/%2e%2e%2fetc%2fpasswd/content')).status, 404);
  assert.equal((await request(`/sessions/a/downloads/${id}`, 'DELETE')).status, 200);
  assert.equal((await request(`/sessions/a/downloads/${id}/content`)).status, 404);
  assert.equal((await request(`/sessions/a/downloads/${id}`, 'DELETE')).status, 200);
});
