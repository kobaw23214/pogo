import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { BrowserSession } from '../src/session.js';
import { LiveView } from '../src/live.js';
import http from 'node:http';
import { chromium } from 'playwright';

const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture(t) {
  let observed = null;
  const main = { evaluate: async () => observed };
  const page = new EventEmitter();
  Object.assign(page, { url: () => 'https://fixture.invalid', isClosed: () => false,
    viewportSize: () => ({ width: 800, height: 600 }), mainFrame: () => main, frames: () => [] });
  const context = { setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {} };
  const session = new BrowserSession(context);
  session.devtools = { isPaused: () => false, isBlocked: () => false, async attach() {} };
  const meta = { id: 't1', page, version: 1, visual: 1, topDocumentVersion: 1,
    historyVersion: 1, control: 'agent', controlEpoch: 1, controlPhase: 'ready', documentIdentityReady: true };
  session.pages.set(meta.id, meta); session.active = meta.id;
  const messages = [], socket = new EventEmitter();
  Object.assign(socket, { readyState: 1, bufferedAmount: 0,
    send(value) { messages.push(JSON.parse(value)); }, close() { this.readyState = 3; } });
  const view = new LiveView(socket, session, { context_ref: session.contextRef, tab_id: meta.id });
  session.liveViews.add(view); view.overlayEnabled = true; view.mediaEpoch = 'epoch-one'; view.stamp = view.identity();
  const frame = { frame_id: 1, media_epoch: view.mediaEpoch, generation: session.generation,
    control_epoch: 1, frame_identity: session.frameIdentity(meta), viewport: session.viewport(meta), acked: true };
  view.frames.set(1, frame); t.after(() => view.close());
  return { session, meta, main, view, frame, messages, observe(value) { observed = value; } };
}

test('an observed agent click is pushed immediately with the displayed page binding and no capture', async t => {
  const h = fixture(t);
  h.session.frame = () => assert.fail('live markers must never capture screenshots');
  const action = h.session.activity.begin({ action: 'click' }, 'agent');
  h.session.activity.coordinateClick(123, 45, h.session.generation);
  await h.session.activity.finish(action, true);
  const event = h.messages.filter(message => message.type === 'action_overlay').at(-1);
  assert.equal(event.context_ref, h.session.contextRef); assert.equal(event.tab_id, 't1');
  assert.equal(event.generation, h.session.generation); assert.equal(event.control_epoch, 1);
  assert.equal(event.frame_identity, h.session.frameIdentity(h.meta)); assert.equal(event.document_version, 1);
  assert.equal(event.media_epoch, 'epoch-one'); assert.equal(event.frame_id, 1);
  assert.deepEqual(event.viewport, { width: 800, height: 600 });
  assert.equal(event.overlay.last_action.phase, 'succeeded');
  assert.equal(event.overlay.pointer.x, 123); assert.equal(event.overlay.pointer.action_seq, action.record.seq);
  assert.equal(event.overlay.pointer.actor, 'agent');
  const sequences = h.messages.filter(message => message.type === 'action_overlay').map(message => message.seq);
  assert.ok(sequences.every((value, index) => index === 0 || value > sequences[index - 1]));
  for (const [index, message] of h.messages.entries()) if (message.type === 'action_overlay' && message.overlay) {
    const state = h.messages.slice(0, index).filter(item => item.type === 'state').at(-1)?.state;
    assert.equal(state.activity.last_action.seq, message.overlay.last_action.seq, 'action status reaches the viewer before its marker');
  }
});

test('nonmouse actions do not fabricate or reuse a preceding click location', async t => {
  const h = fixture(t), click = h.session.activity.begin({ action: 'click' }, 'agent');
  h.session.activity.coordinateClick(120, 80, h.session.generation);
  await h.session.activity.finish(click, true);
  for (const action of ['type', 'select', 'press', 'snapshot', 'extract', 'navigate', 'devtools']) {
    const handle = h.session.activity.begin({ action }, 'agent');
    await h.session.activity.finish(handle, true);
    await h.view.overlayFlight;
    const event = h.messages.at(-1);
    assert.equal(event.overlay.last_action.action, action);
    assert.equal(event.overlay.pointer, undefined, `${action} must not imply mouse movement`);
  }
});

test('human takeover clears marks and refuses late agent geometry', async t => {
  const h = fixture(t), pending = deferred();
  h.session.activity.overlay = () => pending.promise;
  const action = h.session.activity.begin({ action: 'click' }, 'agent');
  await tick(); const flight = h.view.overlayFlight;
  h.meta.control = 'human'; h.meta.controlEpoch++;
  h.view.publishOverlay();
  assert.equal(h.messages.at(-1).overlay, null);
  pending.resolve({ ...h.session.activity.cachedOverlay(h.session.version()), last_action: action.record,
    pointer: { x: 300, y: 200, seq: 1, action_seq: action.record.seq, actor: 'agent', kind: 'click' } });
  await flight;
  assert.equal(h.messages.at(-1).overlay, null);
});

test('delayed geometry cannot cross media, document, viewport, or action fences', async t => {
  for (const mutate of [h => { h.view.mediaEpoch = 'epoch-two'; }, h => { h.meta.version++; },
    h => { h.meta.page.viewportSize = () => ({ width: 900, height: 600 }); },
    h => { h.session.activity.begin({ action: 'snapshot' }, 'agent'); }]) {
    const h = fixture(t), pending = deferred(); h.session.activity.overlay = () => pending.promise;
    const action = h.session.activity.begin({ action: 'click' }, 'agent');
    await tick(); const flight = h.view.overlayFlight, old = h.messages.length;
    mutate(h); const after = h.messages.length;
    pending.resolve({ ...h.session.activity.cachedOverlay(h.session.version()), last_action: action.record,
      pointer: { x: 123, y: 45, seq: 1, action_seq: action.record.seq, actor: 'agent', kind: 'click' } });
    await flight;
    assert.equal(h.messages.length, after);
    assert.ok(after >= old);
  }
});

test('optional geometry coalesces and does not join the input or video queue', async t => {
  const h = fixture(t), pending = deferred(); let reads = 0;
  h.session.activity.overlay = () => { reads++; return pending.promise; };
  h.session.activity.begin({ action: 'snapshot' }, 'agent');
  for (let index = 0; index < 20; index++) h.view.publishOverlay();
  await tick(); assert.equal(reads, 1);
  const action = h.session.activity.begin({ action: 'type' }, 'agent');
  await h.session.activity.finish(action, true);
  assert.equal(h.messages.at(-1).overlay.last_action.action, 'type');
  pending.resolve({}); await h.view.overlayFlight;
});

test('unpresented, hidden, stale and unnegotiated views receive no live marks', async t => {
  for (const mutate of [h => { h.frame.acked = false; }, h => { h.view.viewVisible = false; },
    h => { h.view.stamp = 'earlier-page'; }, h => { h.meta.control = 'human'; }]) {
    const h = fixture(t); mutate(h);
    h.session.activity.begin({ action: 'click' }, 'agent');
    assert.equal(h.messages.at(-1).overlay, null);
  }
  const h = fixture(t); h.view.overlayEnabled = false;
  h.session.activity.begin({ action: 'click' }, 'agent'); await tick();
  assert.equal(h.messages.some(message => message.type === 'action_overlay'), false);
});

test('idle markers expire and unchanged geometry is not sent repeatedly', async t => {
  const h = fixture(t), action = h.session.activity.begin({ action: 'click' }, 'agent');
  h.session.activity.coordinateClick(20, 30, h.session.generation);
  await h.session.activity.finish(action, true); await h.view.overlayFlight;
  const count = h.messages.length;
  h.view.publishOverlay(); await h.view.overlayFlight;
  assert.equal(h.messages.length, count);
  action.record.updated_at = Date.now() - 4001;
  h.view.publishOverlay(); assert.equal(h.messages.at(-1).overlay, null);
});

test('real iframe pointer coordinates are mapped through border and viewport scale', async t => {
  const h = fixture(t); let disposed = 0, observedAt = Date.now();
  const child = { isDetached: () => false, parentFrame: () => h.main,
    evaluate: async () => ({ x: 20, y: 30, seq: 1, kind: 'click', at: observedAt }),
    frameElement: async () => ({ evaluate: async () => ({ width: 200, height: 100, left: 2, top: 3 }),
      boundingBox: async () => ({ x: 100, y: 50, width: 400, height: 200 }), dispose: async () => { disposed++; } }) };
  h.session.refs.set('child-target', { frame: child });
  // Test the event conversion independently of optional target geometry.
  h.session.liveViews.delete(h.view);
  const handle = h.session.activity.begin({ action: 'click', target: 'child-target' }, 'agent');
  observedAt = handle.record.started_at;
  await h.session.activity.finish(handle, true);
  assert.equal(h.session.activity.pointer.value.x, 144);
  assert.equal(h.session.activity.pointer.value.y, 116);
  assert.equal(h.session.activity.pointer.value.action_seq, handle.record.seq);
  assert.ok(disposed > 0);
  const mainClick = h.session.activity.begin({ action: 'click' }, 'agent');
  h.observe({ x: 60, y: 70, seq: 1, kind: 'click', at: mainClick.record.started_at });
  await h.session.activity.finish(mainClick, true);
  assert.equal(h.session.activity.pointer.value.x, 60, 'different frames can reuse their own event sequence');
  assert.equal(h.session.activity.pointer.value.action_seq, mainClick.record.seq);
});

test('unsupported iframe transforms hide the pointer rather than inventing a centre', async t => {
  const h = fixture(t);
  const child = { isDetached: () => false, parentFrame: () => h.main,
    evaluate: async () => ({ x: 20, y: 30, seq: 1, kind: 'click', at: Date.now() }),
    frameElement: async () => ({ evaluate: async () => null,
      boundingBox: async () => assert.fail('unsafe frame transform'), dispose: async () => {} }) };
  h.session.refs.set('child-target', { frame: child }); h.session.liveViews.delete(h.view);
  const handle = h.session.activity.begin({ action: 'click', target: 'child-target' }, 'agent');
  await h.session.activity.finish(handle, true);
  assert.equal(h.session.activity.pointer, null);
});

test('overlay negotiation validates types and keeps legacy clients unchanged', async t => {
  const h = fixture(t); h.view.restartMedia = async () => {};
  await assert.rejects(h.view.receive({ type: 'media_config', scale: 1, action_overlay: 'true' }), error => error.status === 400);
  assert.equal(h.view.mediaScale, null);
  await h.view.receive({ type: 'media_config', scale: 1, action_overlay: true });
  assert.equal(h.view.overlayEnabled, true);
  const old = fixture(t); old.view.restartMedia = async () => {};
  await old.view.receive({ type: 'media_config', scale: 1 });
  assert.equal(old.view.overlayEnabled, false);
});

test('real Chromium iframe click and Chinese fill emit accurate, value-free live marks', { timeout: 15000 }, async t => {
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html; charset=utf-8');
    response.end(request.url === '/child' ?
      '<style>body{margin:0}button{position:absolute;left:40px;top:30px;width:80px;height:30px}input{position:absolute;left:40px;top:90px;width:120px}</style><button id="button" onclick="window.clicked=(window.clicked||0)+1">Click</button><input id="input">' :
      '<style>body{margin:0}iframe{position:absolute;left:100px;top:80px;width:240px;height:160px;border:4px solid;transform:scale(1.5);transform-origin:0 0}</style><textarea id="manual">人工备注保留</textarea><iframe src="/child"></iframe>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  t.after(async () => { await browser.close(); await new Promise(resolve => site.close(resolve)); });
  const context = await browser.newContext({ viewport: { width: 800, height: 600 } });
  const session = await new BrowserSession(context, { actionTimeout: 2500, settleMs: 0 }).init();
  const page = session.page().page;
  await page.goto(`http://127.0.0.1:${site.address().port}/`);
  const child = page.frames().find(frame => frame.url().endsWith('/child'));
  await child.locator('#button').waitFor();
  const meta = session.page(), messages = [], socket = new EventEmitter();
  Object.assign(socket, { readyState: 1, bufferedAmount: 0, send(value) { messages.push(JSON.parse(value)); }, close() {} });
  const view = new LiveView(socket, session, { context_ref: session.contextRef, tab_id: meta.id });
  view.overlayEnabled = true; view.mediaEpoch = 'real-chromium-fixture'; view.stamp = view.identity();
  view.frames.set(1, { frame_id: 1, media_epoch: view.mediaEpoch, generation: session.generation,
    control_epoch: session.controlFor(meta).epoch, frame_identity: session.frameIdentity(meta), viewport: session.viewport(meta), acked: true });
  session.liveViews.add(view); t.after(() => view.close());
  session.frame = () => assert.fail('live action feedback must not take a screenshot');
  async function reference(selector, key) {
    await child.locator(selector).evaluate((element, marker) => element.setAttribute(marker.name, marker.key), { name: session.marker, key });
    session.refs.set(key, { page: meta, version: meta.version, frame: child });
  }
  await reference('#button', 'e9001'); await reference('#input', 'e9002');
  const box = await child.locator('#button').boundingBox();
  const click = session.activity.begin({ action: 'click', target: 'e9001' }, 'agent');
  await child.locator('#button').click(); await session.activity.finish(click, true);
  await view.overlayFlight;
  const point = session.activity.pointer?.value;
  assert.ok(point, 'trusted iframe pointer event was observed');
  assert.ok(Math.abs(point.x - (box.x + box.width / 2)) < 1);
  assert.ok(Math.abs(point.y - (box.y + box.height / 2)) < 1);
  assert.equal(await child.evaluate(() => window.clicked), 1, 'observation never replays the website click');
  const type = session.activity.begin({ action: 'type', target: 'e9002' }, 'agent');
  await child.locator('#input').fill('中文输入测试隐私内容'); await session.activity.finish(type, true); await view.overlayFlight;
  const mark = messages.filter(message => message.type === 'action_overlay' && message.overlay?.last_action?.seq === type.record.seq && message.overlay.focus).at(-1);
  assert.ok(mark?.overlay.focus.editable, 'actual iframe input focus is visible');
  assert.equal(mark.overlay.pointer, undefined, 'fill does not pretend to click');
  assert.equal(JSON.stringify(messages).includes('中文输入测试隐私内容'), false);
  assert.equal(await page.locator('#manual').inputValue(), '人工备注保留');
  assert.equal(await child.locator('#input').inputValue(), '中文输入测试隐私内容');
  await page.locator('iframe').evaluate(element => { element.style.transform = 'rotate(7deg)'; });
  const rotated = session.activity.begin({ action: 'click', target: 'e9001' }, 'agent');
  await child.locator('#button').click(); await session.activity.finish(rotated, true);
  assert.equal(session.activity.pointer, null, 'rotated frame hides an unproven point');
});
