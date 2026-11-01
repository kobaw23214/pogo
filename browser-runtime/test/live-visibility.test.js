import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { BrowserSession } from '../src/session.js';
import { LiveView } from '../src/live.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture(t) {
  const commands = [], calls = [], messages = [];
  const client = { async send(method, params) { commands.push({ method, params }); }, async detach() {} };
  const context = { setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {}, async newCDPSession() { return client; } };
  const session = new BrowserSession(context);
  session.devtools = { async attach() { return { client }; }, isPaused: () => false, isBlocked: () => false };
  const page = Object.assign(new EventEmitter(), { url: () => 'https://fixture.invalid', isClosed: () => false, viewportSize: () => ({ width: 800, height: 600 }) });
  const meta = { id: 't1', version: 1, visual: 1, topDocumentVersion: 1, historyVersion: 1, title: 'Fixture', documentIdentityReady: true, control: 'human', lease: 'fixture-lease', controlEpoch: 1, controlPhase: 'ready', page };
  session.pages.set(meta.id, meta); session.active = meta.id;
  let created = 0;
  const makeMedia = () => ({
    offer: { sdp: 'v=0\r\n' }, pixels: { width: 1600, height: 1200 },
    async close() { calls.push('close'); }, async setVisible(value) { calls.push(value ? 'resume' : 'pause'); },
    async requestFrame() { calls.push('frame'); },
  });
  session.mediaBroker = { runtime: 'wayland', async create() { created++; return makeMedia(); } };
  const socket = Object.assign(new EventEmitter(), { readyState: 1, bufferedAmount: 0, send(value) { const message = JSON.parse(value); messages.push(message); if (message.type === 'view_visibility') calls.push('ack:' + message.revision); }, close() { this.readyState = 3; } });
  const view = new LiveView(socket, session, { context_ref: session.contextRef, tab_id: meta.id });
  view.client = client; view.mediaScale = 2; view.mediaEpoch = 'epoch-one'; view.stamp = view.identity(); view.media = makeMedia();
  session.liveViews.add(view);
  const frame = { frame_id: 1, media_epoch: view.mediaEpoch, generation: session.generation, control_epoch: 1, frame_identity: session.frameIdentity(meta), viewport: session.viewport(meta), pixel_width: 1600, pixel_height: 1200, acked: true };
  view.frames.set(1, frame);
  const visibility = (visible, revision) => view.receive({ type: 'view_visibility', visible, revision });
  const ack = revision => { const current = [...view.frames.values()].at(-1); return view.receive({ type: 'presentation_ack', frame_id: current.frame_id, media_epoch: current.media_epoch, width: current.pixel_width, height: current.pixel_height, ...(revision === undefined ? {} : { view_revision: revision }) }); };
  const input = (seq, text = 'once') => { const current = [...view.frames.values()].at(-1); return view.receive({ type: 'input', seq, generation: session.generation, control_epoch: meta.controlEpoch, lease_token: meta.lease, frame_id: current.frame_id, media_epoch: current.media_epoch, event: { type: 'text', text } }); };
  t.after(() => view.close());
  return { view, session, meta, page, frame, calls, commands, messages, client, socket, visibility, ack, input, makeMedia, get created() { return created; } };
}

test('Wayland advertises visibility while existing clients keep their presentation protocol', async t => {
  const h = fixture(t); await h.view.start();
  assert.equal(h.messages.find(message => message.type === 'ready').capabilities.view_visibility, true);
  assert.equal(h.messages.some(message => message.type === 'view_visibility'), false);
  h.frame.acked = false; await h.ack(); assert.equal(h.frame.acked, true);
  await h.input(1); assert.equal(h.commands.filter(call => call.method === 'Input.insertText').length, 1);
  h.session.mediaBroker.runtime = 'legacy';
  await assert.rejects(h.visibility(false, 1), error => error.status === 400);
});

test('hide/show retains the same peer and epoch, and needs a new matching presentation before input', async t => {
  const h = fixture(t), media = h.view.media;
  h.view.awaitPresentation(); assert.ok(h.view.presentationTimer);
  await h.visibility(false, 1);
  assert.equal(h.view.media, media); assert.equal(h.view.mediaEpoch, 'epoch-one'); assert.equal(h.created, 0);
  assert.equal(h.view.presentationTimer, undefined); assert.equal(h.frame.acked, false);
  await h.ack(1); assert.equal(h.frame.acked, false);
  await assert.rejects(h.input(1), error => error.code === 'LIVE_BUSY');
  await h.visibility(true, 2);
  assert.equal(h.view.media, media); assert.equal(h.view.mediaEpoch, 'epoch-one'); assert.equal(h.created, 0);
  assert.ok(h.view.presentationTimer);
  await h.ack(); await h.ack(1); assert.equal(h.frame.acked, false);
  await assert.rejects(h.input(2), error => error.code === 'LIVE_FRAME_STALE');
  await h.ack(2); await h.input(3);
  assert.deepEqual(h.calls, ['pause', 'ack:1', 'resume', 'ack:2', 'frame']);
  assert.equal(h.commands.filter(call => call.method === 'Input.insertText').length, 1);
});

test('visibility is bounded and strictly ordered; rapid toggles acknowledge only the latest completed request', async t => {
  const h = fixture(t), release = deferred(), released = deferred();
  h.view.pressed.add('left'); h.meta.liveInputOwner = h.view;
  h.view.release = async () => { released.resolve(); await release.promise; h.view.pressed.clear(); };
  const hiding = h.visibility(false, 1); await released.promise;
  const showing = h.visibility(true, 2);
  await h.ack(2); assert.equal(h.frame.acked, false, 'early ACK cannot bypass an unfinished release');
  await assert.rejects(h.input(1), error => error.code === 'LIVE_BUSY');
  release.resolve(); await Promise.all([hiding, showing]);
  assert.equal(h.meta.liveInputOwner, null); assert.equal(h.view.pressed.size, 0);
  assert.deepEqual(h.messages.filter(message => message.type === 'view_visibility'), [{ type: 'view_visibility', visible: true, revision: 2 }]);
  for (const revision of [0, 1, 2, -1, Infinity, '3']) await assert.rejects(h.visibility(false, revision), error => error.status === 400);
  assert.equal(h.view.viewVisible, true); assert.equal(h.view.visibilityRevision, 2);
});

test('hiding drains already dispatched input and releases held controls after rejected queued input', async t => {
  const h = fixture(t), started = deferred(), finish = deferred();
  h.view.pressed.add('left'); h.meta.liveInputOwner = h.view;
  h.client.send = async (method, params) => { h.commands.push({ method, params }); if (method === 'Input.insertText') { started.resolve(); await finish.promise; } };
  const inFlight = h.input(1, 'accepted'); await started.promise;
  const queued = h.input(2, 'fenced'); const rejected = assert.rejects(queued, error => error.code === 'LIVE_BUSY');
  const hiding = h.visibility(false, 1);
  finish.resolve(); await Promise.all([inFlight, rejected, hiding]);
  assert.deepEqual(h.commands.filter(call => call.method === 'Input.insertText').map(call => call.params.text), ['accepted']);
  assert.equal(h.commands.filter(call => call.params?.type === 'mouseReleased').length, 1);
  assert.equal(h.view.pressed.size, 0); assert.equal(h.meta.liveInputOwner, null);
});

test('a pending media allocation is accounted for and paused when hiding wins', async t => {
  const h = fixture(t), creating = deferred(), complete = deferred();
  h.session.mediaBroker.create = async () => { creating.resolve(); await complete.promise; return h.makeMedia(); };
  const opening = h.view.restartMedia(); await creating.promise;
  const hiding = h.visibility(false, 1); complete.resolve(); await Promise.all([opening, hiding]);
  assert.ok(h.view.media); assert.equal(h.view.presentationTimer, undefined);
  assert.equal(h.calls.includes('pause'), true); assert.equal(h.calls.at(-1), 'ack:1');
  assert.equal(h.messages.filter(message => message.type === 'media_offer').length, 1);
  const epoch = h.view.mediaEpoch; await h.visibility(true, 2); assert.equal(h.view.mediaEpoch, epoch);
});

test('a real hidden document change retires old media and creates only on show', async t => {
  const h = fixture(t); await h.visibility(false, 1);
  h.meta.topDocumentVersion++; h.meta.version++;
  h.view.publishState(); await h.view.cleanup;
  assert.equal(h.view.mediaEpoch, null); assert.equal(h.view.media, null); assert.equal(h.created, 0);
  h.view.publishState(); await tick(); assert.equal(h.created, 0);
  await h.visibility(true, 2);
  assert.equal(h.created, 1); assert.notEqual(h.view.mediaEpoch, 'epoch-one');
  const frame = [...h.view.frames.values()].at(-1);
  assert.equal(frame.frame_identity, h.session.frameIdentity(h.meta)); assert.equal(frame.acked, false);
  await h.ack(2); assert.equal(frame.acked, true);
});

test('closing during media creation retires its late peer and never sends a visibility success', async t => {
  const h = fixture(t), started = deferred(), finish = deferred();
  h.session.mediaBroker.create = async () => { started.resolve(); await finish.promise; return h.makeMedia(); };
  const opening = h.view.restartMedia(); await started.promise;
  const hiding = h.visibility(false, 1), closing = h.view.close();
  finish.resolve(); await Promise.all([opening, hiding, closing]);
  assert.equal(h.view.closed, true); assert.equal(h.view.media, null);
  assert.equal(h.messages.some(message => message.type === 'view_visibility'), false);
  assert.equal(h.messages.some(message => message.type === 'media_offer'), false);
  assert.equal(h.calls.filter(call => call === 'close').length, 2, 'both the original peer and late allocation retire exactly once');
});
