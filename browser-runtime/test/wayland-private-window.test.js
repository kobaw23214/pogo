import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { WaylandSessionHost } from '../src/wayland.js';

const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const nonceOf = url => /^<title>(pogo-private-[a-f0-9]{32})<\/title>$/.exec(decodeURIComponent(url.slice(url.indexOf(',') + 1)))[1];
function fixture() {
  const calls = [], pages = [], windows = new Map(), bounds = new Map();
  const runtime = { scale: 1.5, maxScale: 2, hosts: new Map(), rpc: { async request(op, args) { calls.push(['Go', op, args]); return { ack: true }; } } };
  const host = new WaylandSessionHost(runtime, 'private-window'); runtime.hosts.set(host.id, host);
  const h = { host, calls, pages, windows, bounds, titleReads: 0, targetReads: 0, windowReads: 0 };
  h.page = (url, windowId = 7) => {
    const page = new EventEmitter(); page.currentURL = url; page.target = 'target-' + windowId; page.windowId = windowId;
    page.url = () => page.currentURL; page.isClosed = () => page.closed === true; page.context = () => host.context;
    page.title = async () => { h.titleReads++; return ''; };
    page.evaluate = async () => { page.currentURL = 'about:blank'; };
    page.waitForURL = async url => assert.equal(page.currentURL, url);
    pages.push(page); return page;
  };
  host.context = { pages: () => pages, async newCDPSession(page) { return Object.assign(new EventEmitter(), { async detach() { calls.push(['CDP', 'detach', page.target]); }, async send(method) {
    if (method === 'Page.enable') return {};
    if (method === 'Page.getFrameTree') return { frameTree: { frame: { id: 'frame-' + page.windowId } } };
    assert.equal(method, 'Target.getTargetInfo'); h.targetReads++;
    const targetInfo = h.targetInfo?.(page, h.targetReads) ?? { targetId: page.target, url: page.url() };
    return { targetInfo };
  } }); } };
  host.browserCDP = { async send(method, args) {
    calls.push(['CDP', method, args]);
    if (method === 'Browser.getWindowForTarget') {
      const page = pages.find(page => page.target === args.targetId); h.windowReads++;
      return { windowId: h.windowId?.(page, h.windowReads) ?? page.windowId };
    }
    if (method === 'Browser.setWindowBounds') {
      if (args.bounds.width) bounds.set(args.windowId, { ...args.bounds });
      return {};
    }
    if (method === 'Browser.getWindowBounds') return { bounds: bounds.get(args.windowId) };
    assert.fail('Unexpected CDP call ' + method);
  } };
  host.rpc = { closed: false, async request(op, args) {
    calls.push(['native', op, args]);
    if (op === 'windows') return h.readWindows ? h.readWindows() : [...windows.values()].map(window => ({ ...window, ...(bounds.has(window.window_id) ? { outer_width: bounds.get(window.window_id).width, outer_height: bounds.get(window.window_id).height } : {}) }));
    if (op === 'bind_sheet') { windows.get(args.window_id).parked = false; return { display_id: args.window_id + 1 }; }
    return { ack: true };
  }, close() { this.closed = true; } };
  host.session = { generation: 1, liveViews: new Set(), attach(page) { calls.push(['session', 'attach', page.target]); return { id: 't' + page.windowId }; } };
  host.setViewport = async () => { calls.push(['host', 'setViewport']); };
  host.launchChrome = url => {
    h.created = h.page(url); windows.set(7, { window_id: 7, parked: true, title: nonceOf(url), outer_width: 640, outer_height: 480 });
    h.observed = host.adopt(h.created); h.observed.catch(() => {});
  };
  h.geometryCalls = () => calls.filter(call => call[1] === 'Browser.setWindowBounds');
  h.bound = () => calls.filter(call => call[1] === 'bind_sheet');
  return h;
}

test('private registration handles an early empty DOM title and a late compositor title through one adoption', async () => {
  const h = fixture(); let reads = 0;
  h.readWindows = () => {
    const window = { ...h.windows.get(7), ...(h.bounds.has(7) ? { outer_width: h.bounds.get(7).width, outer_height: h.bounds.get(7).height } : {}) };
    return ++reads === 1 ? [{ ...window, title: '' }] : [window];
  };
  const page = await h.host.newPage();
  assert.equal(h.geometryCalls().length, 0, 'worker-owned early data page must not take popup geometry challenges');
  assert.equal(h.titleReads, 0); assert.equal(reads, 2);
  assert.equal(h.host.pending.get(page), h.observed);
  assert.equal(h.bound().length, 1); assert.equal(h.host.records.get(page).nativeWindow, 7);
  assert.equal(page.url(), 'about:blank'); assert.equal(h.host.issuedPrivateURLs.size, 0);
});

test('a private nonce matching two parked native windows refuses capture instead of picking the first', async () => {
  const h = fixture(); h.readWindows = () => [h.windows.get(7), { ...h.windows.get(7), window_id: 8 }];
  await assert.rejects(h.host.newPage(), /matched multiple windows/);
  assert.equal(h.bound().length, 0); assert.equal(h.geometryCalls().length, 0); assert.equal(h.host.issuedPrivateURLs.size, 0);
});

test('an unissued data URL with a private-looking title still uses both popup geometry challenges', async () => {
  const h = fixture(), title = 'pogo-private-' + 'a'.repeat(32), url = 'data:text/html;charset=utf-8,' + encodeURIComponent('<title>' + title + '</title>');
  const page = h.page(url); page.title = async () => title;
  h.windows.set(7, { window_id: 7, parked: true, title, outer_width: 640, outer_height: 480 });
  const record = await h.host.adopt(page);
  assert.equal(h.geometryCalls().length, 3); assert.equal(h.geometryCalls()[0][2].bounds.windowState, 'normal');
  assert.notDeepEqual(h.geometryCalls()[1][2].bounds, h.geometryCalls()[2][2].bounds);
  assert.equal(record.nativeWindow, 7); assert.equal(h.bound().length, 1);
});

test('closing a host clears issued identities and cannot bind a late private native window', async () => {
  const h = fixture(), gate = deferred(), entered = deferred();
  h.readWindows = () => { entered.resolve(); return gate.promise; };
  const creating = h.host.newPage(), rejected = assert.rejects(creating, /closed while binding/);
  await entered.promise; await h.host.close();
  assert.equal(h.host.issuedPrivateURLs.size, 0); gate.resolve([...h.windows.values()]);
  await rejected; assert.equal(h.bound().length, 0); assert.equal(h.calls.some(call => call[1] === 'attach'), false);
});

test('private pairing rejects a target navigation after native nonce discovery', async () => {
  const h = fixture(); h.targetInfo = (page, read) => ({ targetId: page.target, url: read === 1 ? page.url() : 'https://untrusted.invalid/' });
  await assert.rejects(h.host.newPage(), /identity changed while binding/);
  assert.equal(h.bound().length, 0); assert.equal(h.host.issuedPrivateURLs.size, 0);
});

test('private pairing rejects a changed Chrome window even when its target and native nonce still match', async () => {
  const h = fixture(); h.windowId = (page, read) => read === 1 ? page.windowId : 8;
  await assert.rejects(h.host.newPage(), /identity changed while binding/);
  assert.equal(h.bound().length, 0); assert.equal(h.host.issuedPrivateURLs.size, 0);
});

test('a consumed private URL cannot authorize another page through nonce matching', async () => {
  const h = fixture(); await h.host.newPage();
  const nonce = [...h.windows.values()][0].title;
  const page = h.page('data:text/html;charset=utf-8,' + encodeURIComponent('<title>' + nonce + '</title>'), 8);
  h.windows.set(8, { window_id: 8, parked: true, title: nonce, outer_width: 640, outer_height: 480 });
  const record = await h.host.adopt(page);
  assert.equal(h.geometryCalls().length, 3); assert.equal(record.nativeWindow, 8); assert.equal(h.bound().length, 2);
});

test('a failed Chrome dispatch cannot retain an issued private URL capability', async () => {
  const h = fixture(); h.host.launchChrome = () => { throw new Error('fixture dispatch failed'); };
  await assert.rejects(h.host.newPage(), /dispatch failed/);
  assert.equal(h.host.issuedPrivateURLs.size, 0); assert.equal(h.bound().length, 0);
});

function viewportFixture() {
  const h = fixture(), gate = deferred(), entered = deferred(), page = h.page('about:blank');
  const proof = { marker: 'must-not-be-replaced' }, record = { page, pageId: 't7', viewport: { width: 1280, height: 800 }, scale: 1.5, proof, peers: new Map(), peerClosures: new Set(), gate: Promise.resolve() };
  const meta = { id: record.pageId, page, documentIdentityReady: true }, counters = { resizes: 0, layoutReads: 0 };
  h.host.session.pages = new Map([[meta.id, meta]]);
  h.host.session.frameIdentity = () => 'viewport-document';
  h.host.records.set(page, record);
  h.host.rpc.request = async op => {
    if (op !== 'resize') return { ack: true };
    counters.resizes++;
    return { width: 1280, height: 800, scale: 1.5, pixel_width: 1920, pixel_height: 1200 };
  };
  page.evaluate = () => { counters.layoutReads++; entered.resolve(); return gate.promise; };
  h.host.setViewport = WaylandSessionHost.prototype.setViewport.bind(h.host);
  return { ...h, gate, entered, page, record, proof, counters };
}

test('one unresolved SDK viewport predicate hits the original 15s budget without granting pixels or closing other Sheets', async t => {
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'] });
  const h = viewportFixture(); let finished = false;
  const resizing = h.host.setViewport(h.page, { width: 1280, height: 800 });
  resizing.then(() => { finished = true; }, () => { finished = true; });
  const rejected = assert.rejects(resizing, error => {
    assert.equal(error.status, 409); assert.equal(error.code, 'LIVE_NAVIGATION_TRANSITION');
    assert.match(error.cause?.message, /resource did not become ready/);
    return true;
  });
  await h.entered.promise;
  t.mock.timers.tick(14999); await Promise.resolve(); assert.equal(finished, false);
  t.mock.timers.tick(1); await rejected;
  assert.notEqual(h.host.failed, true); assert.equal(h.host.closed, false);
  assert.deepEqual(h.counters, { resizes: 1, layoutReads: 1 }, 'a pending display read must not repeat the native resize or enqueue another SDK read');
  assert.equal(h.record.proof, null); assert.equal(h.record.layoutPending, true);
  h.gate.resolve({ width: 1280, height: 800, scale: 1.5 }); await h.host.close(); await Promise.resolve();
  assert.equal(h.record.proof, null, 'a late SDK success cannot restore the old coordinate authority');
  assert.equal(h.record.layoutPending, true, 'a timed-out caller cannot publish late layout confirmation');
});

for (const signal of ['host-closed', 'runtime-failed', 'page-closed']) test('an unresolved viewport predicate cancels on ' + signal, async t => {
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'] });
  const h = viewportFixture(), resizing = h.host.setViewport(h.page, { width: 1280, height: 800 });
  const rejected = assert.rejects(resizing, /resource wait was canceled/);
  await h.entered.promise;
  if (signal === 'host-closed') h.host.closed = true;
  else if (signal === 'runtime-failed') h.host.runtime.failed = true;
  else h.page.closed = true;
  t.mock.timers.tick(25); await rejected;
  assert.notEqual(h.host.failed, true, 'a confirmed close must not fail other human Sheets');
  if (signal === 'page-closed') assert.equal(h.host.closed, false);
  assert.deepEqual(h.counters, { resizes: 1, layoutReads: 1 });
  h.gate.resolve({ width: 1280, height: 800, scale: 1.5 }); await h.host.close(); await Promise.resolve();
  assert.equal(h.record.proof, null); assert.equal(h.record.layoutPending, true); assert.equal(h.host.closed, true);
});

for (const stage of ['before-native-bind', 'after-native-bind', 'after-native-register']) test('public observePage preserves another Sheet when the Page closes ' + stage, async () => {
  const h = fixture(), other = await h.host.newPage(), otherRecord = h.host.records.get(other);
  const page = h.page('about:blank', 8); h.windows.set(8, { window_id: 8, parked: true, title: '', outer_width: 640, outer_height: 480 });
  const entered = deferred(), gate = deferred(), originalNative = h.host.rpc.request, originalGo = h.host.runtime.rpc.request;
  h.host.rpc.request = async (op, args) => {
    if (stage === 'before-native-bind' && op === 'windows') { entered.resolve(); await gate.promise; }
    const value = await originalNative(op, args);
    if (stage === 'after-native-bind' && op === 'bind_sheet') { entered.resolve(); await gate.promise; }
    return value;
  };
  h.host.runtime.rpc.request = async (op, args) => {
    const value = await originalGo(op, args);
    if (stage === 'after-native-register' && op === 'register_page') { entered.resolve(); await gate.promise; }
    return value;
  };
  let attached = false; const observing = h.host.observePage(page, () => { attached = true; });
  await entered.promise; page.closed = true; page.emit('close'); gate.resolve(); await observing;
  assert.equal(attached, false); assert.equal(h.host.closed, false); assert.notEqual(h.host.failed, true);
  assert.equal(h.host.records.get(other), otherRecord); assert.equal(h.host.records.has(page), false); assert.equal(h.host.pending.has(page), false);
  const unbind = h.calls.filter(call => call[1] === 'unbind_sheet'), unregister = h.calls.filter(call => call[1] === 'unregister_page');
  assert.equal(unbind.length, stage === 'before-native-bind' ? 0 : 1);
  assert.equal(unregister.length, stage === 'after-native-register' ? 1 : 0);
  for (const call of [...unbind, ...unregister]) assert.equal(call[2].page, 't8');
  assert.equal(h.calls.filter(call => call[1] === 'detach' && call[2] === page.target).length, 1);
});

test('public observePage still fences the owned host for an unknown failure on a live Page', async () => {
  const h = fixture(), page = h.page('about:blank'); h.windows.set(7, { window_id: 7, parked: true, title: '', outer_width: 640, outer_height: 480 });
  h.readWindows = () => { throw new Error('fixture native uncertainty'); };
  await h.host.observePage(page, () => assert.fail('uncertain Page must not attach'));
  assert.equal(h.host.failed, true); assert.equal(h.host.closed, true); await h.host.close();
});

test('download attribution requires a registered live native Sheet and keeps its ownership', async () => {
  const h = fixture(), page = await h.host.newPage(), record = h.host.records.get(page);
  const meta = { id: record.pageId, page };
  h.host.session.pages = new Map([[meta.id, meta]]); h.host.session.contextRef = 'ctx_fixture';
  h.host.session.controlFor = () => ({ mode: 'human' });
  assert.deepEqual(h.host.downloadOwner('frame-7'), { tab_id: 't7', context_ref: 'ctx_fixture', control: 'human' });
  assert.equal(h.host.downloadOwner('foreign-frame'), null);
  record.nativeRegistered = false; assert.equal(h.host.downloadOwner('frame-7'), null); record.nativeRegistered = true;
  page.closed = true; assert.equal(h.host.downloadOwner('frame-7'), null);
});
