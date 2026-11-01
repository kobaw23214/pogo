import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';
import { createWorker } from '../src/server.js';

function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

function fakeSession(frames, actionTimeout = 80) {
  const session = new BrowserSession({ setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {} }, { actionTimeout, settleMs: 0 });
  const page = { url: () => 'https://fixture.invalid/', frames: () => frames, mainFrame: () => frames[0], isClosed: () => false };
  const meta = { id: 't1', page, version: 1, visual: 0, title: 'Current title', revision: 'current-revision' };
  session.active = 't1'; session.pages.set(meta.id, meta);
  session.devtools.attach = async () => {};
  session.devtools.capturePixels = async () => Buffer.from([0xff, 0xd8, 0xff]);
  return { session, meta, page };
}

test('timed-out snapshot is not a pending mutation; repeated reads remain single-flight and late refs are ignored', async () => {
  const pending = deferred(); let reads = 0, navigations = 0;
  const frame = { isDetached: () => false, evaluate: () => { reads++; return pending.promise; } };
  const { session, meta, page } = fakeSession([frame]);
  let id = 0; const execute = action => session.execute({ op_id: `bounded-${++id}`, ...action });
  try {
    const first = await execute({ action: 'snapshot' });
    assert.equal(first.ok, false); assert.match(first.error, /observation timed out/);
    assert.equal(session.rawAction, null);
    for (let i = 0; i < 12; i++) {
      const result = await execute({ action: 'snapshot' });
      assert.equal(result.ok, false); assert.match(result.error, /observation is still pending/);
    }
    assert.equal(reads, 1, 'polling must not enqueue additional renderer evaluations');
    assert.equal(session.observationReads.size, 1);
    assert.equal([...session.observationReads.values()][0].size, 1);
    page.goto = async () => { navigations++; meta.version++; };
    session.observe = async () => ({ ok: true, text: 'Fresh page' });
    assert.equal((await execute({ action: 'navigate', url: 'https://fixture.invalid/new' })).ok, true);
    assert.equal(navigations, 1, 'navigation is accepted while only the expired read remains pending');
    session.refs.set('eFresh', { page: meta, version: meta.version });
    const refs = [...session.refs.keys()];
    pending.resolve({ next: 999, title: 'STALE TITLE', text: 'stale text', items: [{ ref: 'eLate', role: 'link', name: 'stale' }] });
    await sleep(0);
    assert.deepEqual([...session.refs.keys()], refs);
    assert.equal(meta.title, 'Current title');
    assert.equal(meta.revision, 'current-revision');
    assert.equal(session.quantities.stats.observations, 0);
    assert.equal(session.observationReads.size, 0);
  } finally { pending.resolve({ items: [], next: 1 }); }
});

test('snapshot shares one total budget across frames and publishes no partial ref table', async () => {
  let reads = 0;
  const frames = Array.from({ length: 8 }, () => ({ isDetached: () => false, evaluate: async () => {
    reads++; await sleep(50); return { next: 2, title: 'Uncommitted', text: 'partial', items: [{ ref: `e${reads}`, role: 'button', name: 'partial' }] };
  } }));
  const { session, meta } = fakeSession(frames);
  const started = Date.now();
  const result = await session.execute({ op_id: 'total-budget', action: 'snapshot' });
  assert.equal(result.ok, false);
  assert.ok(Date.now() - started < 250, 'total deadline cannot multiply by frame count');
  assert.equal(reads, 2);
  await sleep(60);
  assert.equal(session.refs.size, 0); assert.equal(meta.title, 'Current title');
  assert.equal(session.observationReads.size, 0);
});

test('a revision result arriving after its deadline cannot replace current metadata', async () => {
  const pending = deferred(); let reads = 0;
  const frame = { evaluate: () => { reads++; return pending.promise; } };
  const { session, meta } = fakeSession([frame]);
  await assert.rejects(session.refreshRevision(), /observation timed out/);
  for (let i = 0; i < 10; i++) await assert.rejects(session.refreshRevision(), /observation is still pending/);
  assert.equal(reads, 1);
  session.generation++; meta.revision = 'new-document';
  pending.resolve({ r: 45, covered: true, x: 0, y: 0, w: 1280, h: 800 });
  await sleep(0);
  assert.equal(meta.revision, 'new-document'); assert.equal(meta.revisionCovered, undefined);
});

test('a control fence before the read microtask prevents starting the expired collector', async () => {
  const { session } = fakeSession([]); let reads = 0;
  const observation = session.observation();
  const result = session.readObservation({}, 'snapshot', () => { reads++; return {}; }, observation);
  session.generation++;
  await assert.rejects(result, /superseded/); assert.equal(reads, 0);
});

test('a debugger event cannot interrupt stored diagnostics as if they were a pending page action', async () => {
  const { session } = fakeSession([]);
  session.devtools.isPaused = () => true;
  session.action = async () => { session.debuggerWake?.(); return { ok: true, data: { paused: true, events: [] } }; };
  const result = await session.execute({ op_id: 'diagnostic-during-pause', action: 'devtools', method: 'events.list' });
  assert.equal(result.ok, true); assert.equal(result.data.paused, true);
});

test('optional structured evidence times out without discarding complete text and refs or publishing late quantities', async () => {
  const pending = deferred(); let evidenceReads = 0;
  const frame = { isDetached: () => false, url: () => 'https://fixture.invalid/', evaluate: async (_fn, args) => {
    if (typeof args === 'string') return { r: 1, covered: true, x: 0, y: 0, w: 1280, h: 800 };
    if (args.marker) return { title: 'Observed title', text: 'Observed body', next: 2, items: [{ ref: 'e1', role: 'button', name: 'Observed button' }] };
    evidenceReads++; return pending.promise;
  } };
  const { session } = fakeSession([frame], 350);
  try {
    const result = await session.execute({ op_id: 'optional-evidence', action: 'snapshot' });
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.match(result.text, /Observed body/); assert.match(result.text, /Observed button/);
    assert.equal(result.evidence.status, 'unavailable'); assert.ok(session.refs.has('e1'));
    const next = await session.execute({ op_id: 'optional-evidence-again', action: 'snapshot' });
    assert.equal(next.ok, true); assert.equal(evidenceReads, 1);
    pending.resolve({ schema: 'dom-evidence-v1', tables: [], definitions: [], headings: [], paragraphs: [], truncated: false });
    await sleep(0);
    assert.equal(session.quantities.stats.observations, 0);
  } finally { pending.resolve({}); }
});

test('a busy cross-site iframe cannot turn a completed one-time submit into a pending mutation', { timeout: 20000 }, async t => {
  let writes = 0, port;
  const site = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'text/html');
    if (req.url === '/write') { writes++; res.end('Saved once'); return; }
    if (req.url === '/child') { res.end(`<p>Child</p><script>addEventListener('message', e => { if(e.data === 'busy') { const end = Date.now()+6000; while(Date.now()<end) {} } });</script>`); return; }
    if (req.url === '/clean') { res.end('<h1>Clean page</h1>'); return; }
    res.end(`<button id="save" onclick="fetch('/write',{method:'POST'});document.getElementById('done').textContent='Saved';document.querySelector('iframe').contentWindow.postMessage('busy','*')">Save once</button><p id="done">Ready</p><iframe src="http://localhost:${port}/child"></iframe>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve)); port = site.address().port;
  t.after(() => { site.closeAllConnections(); site.close(); });
  const browser = await chromium.launch({ args: ['--site-per-process'] }); t.after(() => browser.close());
  const worker = await createWorker({ browser, token: 'observation-fixture-token', actionTimeout: 1200, settleMs: 0 });
  const address = await worker.listen(0); t.after(() => worker.close());
  let op = 0;
  const request = async (path, method = 'GET', body) => {
    const response = await fetch(`http://127.0.0.1:${address.port}${path}`, { method, headers: { Authorization: 'Bearer observation-fixture-token', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    return { status: response.status, body: await response.json() };
  };
  const execute = body => request('/sessions/iframe/execute', 'POST', { op_id: `frame-${++op}`, ...body });
  await request('/sessions/iframe', 'PUT');
  assert.equal((await execute({ action: 'navigate', url: `http://127.0.0.1:${port}/` })).body.ok, true);
  const clicked = await execute({ action: 'click', target: '#save' });
  assert.equal(clicked.body.ok, true, JSON.stringify(clicked)); assert.match(clicked.body.text, /observation is incomplete/);
  assert.equal(writes, 1);
  const state = (await request('/sessions/iframe/state')).body;
  assert.equal(state.busy, false); assert.equal(state.debugger_paused, false);
  const wait = await execute({ action: 'wait', text: 'Saved', seconds: 1 });
  assert.equal(wait.status, 200); assert.equal(wait.body.ok, true);
  const navigated = await execute({ action: 'navigate', url: `http://127.0.0.1:${port}/clean` });
  assert.equal(navigated.body.ok, true, JSON.stringify(navigated));
  assert.match(navigated.body.text, /Clean page/); assert.equal(writes, 1);
  // Deterministically move the document boundary after frame() resolves but
  // before the HTTP route receives it. Empty version cannot bypass this check.
  const originalFrame = BrowserSession.prototype.frame;
  BrowserSession.prototype.frame = async function () {
    const result = await originalFrame.call(this);
    this.page().topDocumentVersion++;
    return { ...result, version: '', overlay: undefined };
  };
  try { assert.equal((await request('/sessions/iframe/frame')).status, 409); }
  finally { BrowserSession.prototype.frame = originalFrame; }
});

test('continuous DOM animation yields view-only pixels, rejects empty coordinate versions, and restores stable frames', { timeout: 15000 }, async t => {
  const browser = await chromium.launch(); t.after(() => browser.close());
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const session = await new BrowserSession(context, { actionTimeout: 3000 }).init(); t.after(() => session.close());
  await session.page().page.setContent('<button>Do not click</button><p id="tick">0</p>');
  await session.page().page.evaluate(() => { globalThis.animationID = setInterval(() => document.getElementById('tick').textContent = String(performance.now()), 1); });
  let view;
  for (let i = 0; i < 6; i++) { const result = await session.frame(); if (!result.version) { view = result; break; } }
  assert.ok(view, 'a changing document should remain viewable');
  assert.equal(view.data[0], 0xff); assert.equal(view.overlay, undefined);
  const rejected = await session.execute({ op_id: 'empty-coordinate-version', action: 'click', x: 20, y: 20, version: '' });
  assert.equal(rejected.ok, false); assert.match(rejected.error, /stale screenshot version/);
  await session.page().page.evaluate(() => clearInterval(globalThis.animationID));
  const stable = await session.frame();
  assert.ok(stable.version); assert.ok(stable.overlay);
  assert.equal(stable.version, session.version());
});

test('document changes during screenshot discard pixels instead of returning a view-only frame from the wrong document', async () => {
  const { session, meta, page } = fakeSession([], 1000);
  session.devtools.capturePixels = async () => { meta.version++; return Buffer.from([0xff, 0xd8, 0xff]); };
  await assert.rejects(session.frame(), /document changed while capturing frame/);
});

test('screenshot tool checks the document identity again when a view-only frame is delivered', async () => {
  const { session, meta } = fakeSession([], 1000);
  session.frame = async () => {
    const identity = session.frameIdentity(); meta.version++;
    return { data: Buffer.from([0xff, 0xd8, 0xff]), version: '', identity };
  };
  const result = await session.execute({ op_id: 'changed-view-only-delivery', action: 'screenshot' });
  assert.equal(result.ok, false); assert.match(result.error, /changed after screenshot/);
  assert.equal(result.screenshot, undefined); assert.equal(session.lastScreenshotVersion, null);
});

test('the public screenshot reply preserves an explicitly empty view-only version', async () => {
  const { session } = fakeSession([], 1000);
  session.frame = async () => ({ data: Buffer.from([0xff, 0xd8, 0xff]), version: '', identity: session.frameIdentity() });
  const result = await session.execute({ op_id: 'view-only-public-reply', action: 'screenshot' });
  assert.equal(result.ok, true); assert.equal(result.version, '');
  assert.equal(session.lastScreenshotVersion, null);
  assert.match(result.text, /View-only screenshot/); assert.ok(result.screenshot.data);
});

test('a stable but slower revision regains a version after the fast first-frame attempt', async () => {
  const frame = { evaluate: async () => { await sleep(150); return { r: 1, covered: true, x: 0, y: 0, w: 1280, h: 800 }; } };
  const { session } = fakeSession([frame], 1000);
  const first = await session.frame();
  assert.equal(first.version, '', 'first pixels do not wait for slow page JS');
  await sleep(180);
  const next = await session.frame();
  assert.ok(next.version, 'later same-document captures allow a bounded longer revision read');
  assert.equal(next.version, session.version());
});

test('iframe revision changes permit display-only pixels while top-document and control changes remain fenced', async () => {
  const { session, meta } = fakeSession([], 1000); meta.topDocumentVersion = 1;
  const identity = session.frameIdentity();
  session.devtools.capturePixels = async () => { meta.version++; return Buffer.from([0xff, 0xd8, 0xff]); };
  const frame = await session.frame();
  assert.equal(frame.version, ''); assert.equal(frame.identity, identity); assert.equal(frame.overlay, undefined);
  session.devtools.capturePixels = async () => { session.generation++; return Buffer.from([0xff, 0xd8, 0xff]); };
  await assert.rejects(session.frame(), error => error.status === 409);
});
