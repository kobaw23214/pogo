import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { LiveView } from '../src/live.js';
import { BrowserSession } from '../src/session.js';
import { WaylandSessionHost } from '../src/wayland.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture(t) {
  const client = { async send() {}, async detach() {} };
  const context = { setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {}, async newCDPSession() { return client; } };
  const session = new BrowserSession(context);
  session.devtools = { async attach() { return { client }; }, isPaused: () => false, isBlocked: () => false };
  const page = new EventEmitter();
  Object.assign(page, { url: () => 'https://fixture.invalid', isClosed: () => false, viewportSize: () => ({ width: 800, height: 600 }) });
  const meta = { id: 't1', version: 1, visual: 1, topDocumentVersion: 1, historyVersion: 1, title: 'Fixture', documentIdentityReady: true, control: 'human', lease: 'fixture-lease', controlEpoch: 1, controlPhase: 'ready', page };
  session.pages.set(meta.id, meta); session.active = meta.id;
  const messages = [];
  const socket = new EventEmitter();
  Object.assign(socket, { readyState: 1, bufferedAmount: 0, send(value) { messages.push(JSON.parse(value)); }, close() { this.readyState = 3; } });
  const binding = { context_ref: session.contextRef, tab_id: meta.id };
  const view = new LiveView(socket, session, binding); view.client = client; session.liveViews.add(view);
  view.mediaEpoch = 'epoch-one'; view.stamp = view.identity();
  const frame = { frame_id: 1, media_epoch: view.mediaEpoch, generation: session.generation, control_epoch: 1, frame_identity: session.frameIdentity(meta), viewport: session.viewport(meta), pixel_width: 1600, pixel_height: 1200, acked: true };
  view.frames.set(1, frame);
  const input = (seq, event = { type: 'text', text: 'once' }, extra = {}) => ({ type: 'input', seq, generation: session.generation, control_epoch: 1, lease_token: 'fixture-lease', frame_id: 1, media_epoch: 'epoch-one', event, ...extra });
  t.after(() => view.close());
  return { session, meta, view, client, messages, frame, input };
}

test('ACK timing separates waiting in the per-page queue from actual dispatch', async t => {
  let now = 100; t.mock.method(performance, 'now', () => now);
  const h = fixture(t), queue = deferred(), dispatched = deferred(), complete = deferred();
  h.session.liveQueues.set(h.meta, queue.promise);
  h.client.send = async () => { dispatched.resolve(); await complete.promise; };
  const pending = h.view.receive(h.input(1));
  now = 130; queue.resolve(); await dispatched.promise;
  now = 143; complete.resolve(); await pending;
  assert.deepEqual(h.messages.find(message => message.type === 'ack'), { type: 'ack', seq: 1, timing: { queue_ms: 30, dispatch_ms: 13 } });
  assert.equal(h.view.performance.input_count, 1);
  assert.equal('visual_latency_ms' in h.view.performance, false, 'dispatch acknowledgement cannot claim returned-frame latency');
});

test('only the current opted-in epoch publishes backend metrics, never presentation authority', async t => {
  const h = fixture(t), delayed = deferred(); h.view.performanceEnabled = true;
  h.frame.acked = false;
  // The Go media peer owns diagnostics filtering; this unit tests epoch delivery.
  const metrics = { fps: 30, codec: 'video/VP8', width: 1000, height: 750 };
  h.view.media = { async diagnostics() { return metrics; }, async close() {} };
  await h.view.publishPerformance();
  const message = h.messages.find(message => message.type === 'performance');
  assert.equal(message.media_epoch, 'epoch-one'); assert.equal(message.stats.fps, 30);
  assert.equal(JSON.stringify(message).includes('private-'), false);
  assert.equal(h.frame.acked, false);
  await assert.rejects(h.view.receive(h.input(1)), error => error.code === 'LIVE_FRAME_STALE');
  h.view.media.diagnostics = () => delayed.promise;
  const pending = h.view.publishPerformance();
  h.view.mediaEpoch = 'epoch-two'; delayed.resolve(metrics); await pending;
  assert.equal(h.messages.filter(message => message.type === 'performance').length, 1, 'late old-epoch measurements cannot appear in the new epoch');
});

test('existing viewers keep the original media_config shape and receive no unsolicited message type', async t => {
  const old = fixture(t); old.view.restartMedia = async () => {};
  old.view.media = { async diagnostics() { assert.fail('legacy viewer must not request stats'); }, async close() {} };
  await old.view.receive({ type: 'media_config', scale: 2 }); await old.view.publishPerformance();
  assert.equal(old.view.performanceEnabled, false);
  assert.equal(old.view.codecPreference, 'standard');
  assert.equal(old.messages.some(message => message.type === 'performance'), false);
  const modern = fixture(t); modern.view.restartMedia = async () => {};
  modern.view.media = { async diagnostics() { return { fps: 30 }; }, async close() {} };
  await modern.view.receive({ type: 'media_config', scale: 2, performance: true }); await modern.view.publishPerformance();
  assert.equal(modern.messages.find(message => message.type === 'performance').stats.fps, 30);
  const malformed = fixture(t);
  await assert.rejects(malformed.view.receive({ type: 'media_config', scale: 2, performance: 'true' }), error => error.status === 400);
});

test('Wayland ready exposes one standard media path without allocating a peer', async t => {
  const h = fixture(t); h.session.mediaBroker = { runtime: 'wayland' };
  await h.view.start();
  const ready = h.messages.find(message => message.type === 'ready');
  assert.deepEqual(ready.capabilities.codec_preferences, ['standard']);
  assert.equal(ready.capabilities.runtime, 'wayland');
  assert.equal(ready.capabilities.render_scale_limit, 2);
  assert.equal(ready.capabilities.performance, true);
  assert.equal(h.view.codecPreference, 'standard');
  assert.equal(h.view.mediaScale, null, 'capability advertisement does not allocate a stream');
});

test('Wayland ready advertises an explicit native density ceiling without resizing the page', async t => {
  for (const maximum of [1, 1.25, 1.5, 2]) {
    const h = fixture(t); h.session.mediaBroker = { runtime: 'wayland' };
    h.session.mediaConfig = { deviceScaleFactor: maximum };
    await h.view.start();
    assert.equal(h.messages.find(message => message.type === 'ready').capabilities.render_scale_limit, maximum);
    assert.equal(h.view.mediaEpoch, 'epoch-one');
  }
});

test('the W2 live route preserves internal ICE and media identity while retiring legacy external grants', async t => {
  const h = fixture(t), calls = [];
  const candidate = { candidate: 'candidate:internal 1 udp 2113937151 172.20.0.3 19780 typ host', sdpMid: '0', sdpMLineIndex: 0, usernameFragment: 'fixture-ufrag' };
  const sdp = 'v=0\r\na=ice-ufrag:fixture-ufrag\r\na=ice-pwd:fixture-pwd\r\na=fingerprint:sha-256 AA:BB\r\na=candidate:internal 1 udp 2113937151 172.20.0.3 19780 typ host\r\n';
  h.session.mediaConfig = { advertisedIP: '203.0.113.10', iceServers: [{ urls: 'turn:old-relay.example:3478', username: 'old-user', credential: 'synthetic-old-secret' }] };
  h.session.mediaBroker = { runtime: 'wayland', async create(page, options) {
    calls.push({ page, ...options }); options.onCandidate(candidate);
    return { offer: { sdp }, pixels: { width: 1600, height: 1200 }, async close() {} };
  } };
  const identity = h.view.identity();
  await h.view.receive({ type: 'media_config', scale: 2 });
  assert.equal(calls.length, 1); assert.deepEqual(calls[0].iceServers, []);
  assert.equal(calls[0].sourceKey, identity); assert.equal(calls[0].page, h.meta.page);
  assert.equal(calls[0].scale, 2); assert.equal(calls[0].width, 800); assert.equal(calls[0].height, 600);
  const offer = h.messages.find(message => message.type === 'media_offer');
  assert.equal(offer.sdp, sdp); assert.deepEqual(offer.ice_servers, []);
  assert.equal(offer.frame.pixel_width, 1600); assert.equal(offer.frame.pixel_height, 1200);
  assert.equal(offer.frame.frame_identity, h.session.frameIdentity(h.meta));
  assert.deepEqual(h.messages.find(message => message.type === 'media_candidate').candidate, candidate);
  assert.equal(JSON.stringify(h.messages).includes('synthetic-old-secret'), false);
  assert.equal(JSON.stringify(h.messages).includes('203.0.113.10'), false);
  assert.equal(JSON.stringify(h.messages).includes('old-relay.example'), false);
});

test('each standard Wayland epoch retains exact source identity and CSS dimensions', async t => {
  for (const preference of [undefined, 'standard']) {
    const h = fixture(t), calls = [];
    h.meta.page.evaluate = async () => {};
    h.session.mediaBroker = { runtime: 'wayland', async create(page, config) {
      calls.push({ page, ...config });
      return { offer: { sdp: 'v=0\r\n' }, pixels: { width: 1600, height: 1200 }, async close() {} };
    } };
    const identity = h.view.identity();
    const config = { type: 'media_config', scale: 2, ...(preference === undefined ? {} : { codec_preference: preference }) };
    await h.view.receive(config);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].codecPreference, preference ?? 'standard');
    assert.equal(calls[0].sourceKey, identity);
    assert.equal(calls[0].page, h.meta.page);
    assert.equal(calls[0].width, 800); assert.equal(calls[0].height, 600); assert.equal(calls[0].scale, 2);
    await h.view.restartMedia();
    assert.equal(calls[1].codecPreference, preference ?? 'standard', 'a fresh media epoch retains this viewer preference');
    assert.equal(calls[1].sourceKey, identity, 'a peer restart never changes the shared source identity');
    assert.notEqual(calls[1].id, calls[0].id);
    await assert.rejects(h.view.receive({ ...config, codec_preference: 'responsive' }), error => error.status === 400);
    assert.equal(calls.length, 2, 'a second configuration is rejected rather than renegotiating a live peer');
    assert.equal(h.view.codecPreference, preference ?? 'standard');
  }
});

test('an old responsive request maps to the sole Wayland standard contract', async t => {
  const h = fixture(t); h.session.mediaBroker = { runtime: 'wayland' };
  h.view.restartMedia = async () => {};
  await h.view.receive({ type: 'media_config', scale: 2, codec_preference: 'responsive' });
  assert.equal(h.view.codecPreference, 'standard');
});

test('malformed codec preferences leave display configuration unclaimed', async t => {
  for (const preference of [null, '', 'h264', 'H264', true, [], {}, ['responsive']]) {
    const h = fixture(t); h.view.restartMedia = async () => assert.fail('invalid codec preference must not start media');
    await assert.rejects(h.view.receive({ type: 'media_config', scale: 2, codec_preference: preference }), error => error.status === 400);
    assert.equal(h.view.mediaScale, null); assert.equal(h.view.codecPreference, 'standard');
  }
});

test('event-driven state observations coalesce within a turn and reach only the exact live page', async t => {
  const h = fixture(t), sibling = { ...h.meta, id: 't2' };
  h.session.pages.set(sibling.id, sibling);
  const observed = [], other = { meta: sibling, publishState() { observed.push('sibling'); } };
  h.session.liveViews.add(other); t.after(() => h.session.liveViews.delete(other));
  h.view.publishState = force => observed.push(['target', force]);
  h.session.publishLiveState(h.meta); h.session.publishLiveState(h.meta); h.session.publishLiveState(h.meta);
  assert.equal(observed.length, 0);
  await tick(); assert.deepEqual(observed, [['target', true]]);
  h.session.publishLiveState(sibling); h.session.pages.delete(sibling.id); await tick();
  assert.deepEqual(observed, [['target', true]], 'a removed page cannot publish a delayed state');
});

test('transient disconnection fences queued inputs and releases held controls before allowing fresh input', async t => {
  const h = fixture(t), textStarted = deferred(), finishText = deferred(), releaseStarted = deferred(), finishRelease = deferred();
  const calls = [];
  h.client.send = async (method, params) => {
    calls.push({ method, params });
    if (method === 'Input.insertText' && params.text === 'in-flight') { textStarted.resolve(); await finishText.promise; }
    if (method === 'Input.dispatchMouseEvent' && params.type === 'mouseReleased') { releaseStarted.resolve(); await finishRelease.promise; }
  };
  await h.view.receive(h.input(1, { type: 'pointer', phase: 'down', x: 10, y: 10, button: 'left', buttons: 1, modifiers: 0, click_count: 1 }));
  const inFlight = h.view.receive(h.input(2, { type: 'text', text: 'in-flight' })); await textStarted.promise;
  const queued = h.view.receive(h.input(3, { type: 'text', text: 'must-not-replay' }));
  const outcome = queued.then(() => assert.fail('old queued input dispatched'), error => { assert.equal(error.code, 'LIVE_BUSY'); });
  h.view.mediaState('disconnected', () => !h.view.closed);
  h.view.mediaState('connected', () => !h.view.closed);
  await assert.rejects(h.view.receive(h.input(4)), error => error.code === 'LIVE_BUSY');
  finishText.resolve(); await inFlight; await outcome; await releaseStarted.promise;
  assert.equal(h.view.mediaDisconnected, true, 'connection recovery does not bypass an unfinished key/button release');
  await assert.rejects(h.view.receive(h.input(5)), error => error.code === 'LIVE_BUSY');
  finishRelease.resolve(); await h.view.cleanup; await tick();
  assert.equal(h.view.mediaDisconnected, false); assert.equal(h.view.pressed.size, 0);
  await h.view.receive(h.input(6, { type: 'text', text: 'fresh' }));
  assert.deepEqual(calls.filter(call => call.method === 'Input.insertText').map(call => call.params.text), ['in-flight', 'fresh']);
  assert.equal(h.messages.some(message => message.type === 'ack' && message.seq === 3), false);
});

test('a stale ICE callback cannot suspend or close the current media epoch', async t => {
  const h = fixture(t);
  h.view.mediaState('disconnected', () => false); h.view.mediaState('failed', () => false);
  assert.equal(h.view.closed, false); assert.equal(h.view.connectionRevision, 0);
  await h.view.receive(h.input(1)); assert.equal(h.messages.find(message => message.seq === 1).type, 'ack');
});

test('a second disconnect during held-input cleanup gets a bounded recovery deadline without duplicate release', async t => {
  const h = fixture(t), releaseStarted = deferred(), finishRelease = deferred();
  let releases = 0;
  h.view.release = async () => { releases++; releaseStarted.resolve(); await finishRelease.promise; };
  h.view.mediaState('disconnected', () => !h.view.closed); await releaseStarted.promise;
  h.view.mediaState('connected', () => !h.view.closed);
  assert.equal(h.view.disconnectTimer, undefined);
  assert.equal(h.view.mediaDisconnected, true, 'input remains blocked while release is unfinished');
  h.view.mediaState('disconnected', () => !h.view.closed);
  const deadline = h.view.disconnectTimer;
  assert.ok(deadline, 'the second disconnect must start a new deadline');
  h.view.mediaState('disconnected', () => !h.view.closed);
  assert.equal(h.view.disconnectTimer, deadline, 'duplicate notifications cannot postpone that deadline');
  assert.equal(h.view.connectionRevision, 1, 'no accepted input intervened while cleanup was pending');
  assert.equal(releases, 1, 'cleanup is not queued twice');
  finishRelease.resolve(); await h.view.cleanup;
  await new Promise(resolve => setTimeout(resolve, 850));
  assert.equal(h.view.closed, true, 'a disconnected view cannot remain open and input-blocked indefinitely');
  assert.ok(h.messages.some(message => message.type === 'error' && message.error.includes('media disconnected')));
});

function nativeFixture(h) {
  const host = new WaylandSessionHost({}, 'native-unit');
  host.pending.set(h.meta.page, Promise.resolve({ pageId: h.meta.id }));
  host.rpc = { async request() { return { ack: true }; } };
  host.inputSync = async () => {};
  h.session.host = host;
  h.meta.liveInputOwner = h.view;
  return host;
}

test('native seat queue rechecks revision, connection and page ownership before dispatch', async t => {
  for (const changed of ['revision', 'connection', 'ownership']) {
    const h = fixture(t), host = nativeFixture(h), gate = deferred(), calls = [];
    host.inputQueue = gate.promise;
    h.client.send = async (...args) => { calls.push(args); };
    const pending = h.view.dispatch({ type: 'text', text: 'stale native queue' }, h.frame.viewport);
    if (changed === 'revision') h.view.revision++;
    if (changed === 'connection') h.view.connectionRevision++;
    if (changed === 'ownership') { h.meta.control = 'agent'; h.meta.controlEpoch++; }
    const rejected = assert.rejects(pending, error => error.code === 'LIVE_FRAME_STALE');
    gate.resolve(); await rejected;
    assert.deepEqual(calls, [], changed + ' must be checked after the native seat becomes available');
  }
});

test('fencing during native sync denies the late key before tracking or CDP dispatch', async t => {
  const h = fixture(t), host = nativeFixture(h), syncing = deferred(), complete = deferred(), calls = [];
  host.inputSync = async () => { syncing.resolve(); await complete.promise; };
  h.client.send = async (...args) => { calls.push(args); };
  const pending = h.view.dispatch({ type: 'key', phase: 'down', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 8 }, h.frame.viewport);
  await syncing.promise;
  await h.view.fence();
  const rejected = assert.rejects(pending, error => error.code === 'LIVE_FRAME_STALE');
  complete.resolve(); await rejected;
  assert.equal(h.view.keys.size, 0, 'a late native sync cannot restore a key after release');
  assert.equal(host.seatKeys.size, 0);
  assert.equal(calls.some(([method]) => method === 'Input.dispatchKeyEvent'), false);
});

test('fencing during native sync denies late text and modified wheel without replay', async t => {
  for (const event of [{ type: 'text', text: 'late commit' }, { type: 'wheel', x: 10, y: 10, delta_x: 0, delta_y: 40, modifiers: 2 }]) {
    const h = fixture(t), host = nativeFixture(h), syncing = deferred(), complete = deferred(), calls = [];
    host.inputSync = async () => { syncing.resolve(); await complete.promise; };
    h.client.send = async (...args) => { calls.push(args); };
    const pending = h.view.dispatch(event, h.frame.viewport); await syncing.promise;
    await h.view.fence();
    const rejected = assert.rejects(pending, error => error.code === 'LIVE_FRAME_STALE');
    complete.resolve(); await rejected;
    assert.deepEqual(calls, [], 'a stale action must never cross the final CDP boundary');
  }
});

test('an observer closing the same Sheet cannot release another view held controls', async t => {
  const h = fixture(t), host = nativeFixture(h); let releases = 0;
  host.release = async () => { releases++; };
  h.view.pressed.add('left'); h.view.keys.set('ShiftLeft', { key: 'Shift', code: 'ShiftLeft' });
  const observerSocket = new EventEmitter();
  Object.assign(observerSocket, { readyState: 1, bufferedAmount: 0, send() {}, close() {} });
  const observer = new LiveView(observerSocket, h.session, { context_ref: h.session.contextRef, tab_id: h.meta.id });
  observer.client = { async send() { assert.fail('observer cannot dispatch owner key releases'); }, async detach() {} };
  h.session.liveViews.add(observer);
  await observer.close();
  assert.equal(releases, 0);
  assert.equal(h.meta.liveInputOwner, h.view);
  assert.equal(h.view.pressed.has('left'), true); assert.equal(h.view.keys.has('ShiftLeft'), true);
});

test('native seat preserves a different Sheet gesture and clears only its owner release', async () => {
  const host = new WaylandSessionHost({}, 'native-owner-unit'), a = {}, b = {};
  host.pending.set(a, Promise.resolve({ pageId: 'a' })); host.pending.set(b, Promise.resolve({ pageId: 'b' }));
  const releases = []; host.rpc = { async request(op, args) { releases.push({ op, ...args }); return { ack: true }; } };
  await host.runInput(a, { type: 'pointer', phase: 'down', button: 'left' }, async () => {});
  let bDispatch = 0;
  await assert.rejects(host.runInput(b, { type: 'text' }, async () => { bDispatch++; }), error => error.code === 'LIVE_BUSY');
  await host.release(b);
  assert.equal(host.seatOwner, a); assert.equal(host.seatButtons.has('left'), true);
  await host.release(a);
  assert.equal(host.seatOwner, null); assert.equal(host.seatButtons.size, 0);
  await host.runInput(b, { type: 'text' }, async () => { bDispatch++; });
  assert.equal(bDispatch, 1);
  assert.deepEqual(releases.map(row => row.page), ['b', 'a']);
});
