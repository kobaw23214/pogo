import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { WaylandSessionHost } from '../src/wayland.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture(t) {
  const calls = []; let serial = 0;
  const runtime = { scale: 1.5, hosts: new Map(), rpc: new EventEmitter() };
  runtime.rpc.request = async (op, args) => { calls.push({ service: 'stream', op, args }); return op === 'create' ? { peer_id: 'peer-' + ++serial, offer: { sdp: 'v=0\r\n' }, settings: { width: args.width, height: args.height } } : { ack: true }; };
  const host = new WaylandSessionHost(runtime, 'visibility-source'); host.session = { generation: 1 };
  const page = { isClosed: () => false };
  const record = { page, pageId: 't1', displayId: 2, viewport: { width: 800, height: 600 }, scale: 1.5, nativeBound: true, nativeRegistered: true, peers: new Map(), peerClosures: new Set(), creating: 0, captureRevision: 0, gate: Promise.resolve() };
  host.records.set(page, record); host.pending.set(page, Promise.resolve(record));
  host.rpc = { async request(op, args) { calls.push({ service: 'native', op, args }); return { ack: true }; } };
  host.sourceFence = async () => 77;
  const create = (extra = {}) => host.createMedia(page, { generation: 1, sourceKey: 'same-document-owner-size', documentKey: 'same-document-size', ...extra });
  t.after(async () => { for (const peer of [...record.peers.values()]) await peer.close(); });
  return { host, runtime, page, record, calls, create };
}

test('warm peers retain identity while only the last hidden viewer stops the shared page encoder', async t => {
  const h = fixture(t), a = await h.create(), b = await h.create(); h.calls.length = 0;
  await a.setVisible(false);
  assert.equal(h.calls.some(call => call.op === 'capture_stop'), false, 'B remains visible');
  await b.setVisible(false);
  assert.equal(h.calls.at(-1).op, 'capture_stop'); assert.equal(h.record.peers.size, 2);
  assert.equal(h.calls.some(call => call.op === 'close'), false);
  h.calls.length = 0;
  await a.setVisible(true);
  assert.deepEqual(h.calls.map(call => call.op), ['capture_start', 'request_idr']);
  await a.requestFrame(); assert.equal(h.calls.at(-1).op, 'request_idr');
  assert.equal(h.record.peers.get('peer-1'), a); assert.equal(h.record.peers.get('peer-2'), b);
  assert.equal(a.visible, true); assert.equal(b.visible, false);
  assert.equal(h.calls.some(call => call.op === 'create'), false, 'resume allocates no new RTP peer');
});

test('closing the last visible peer pauses capture but preserves a hidden sibling and native page', async t => {
  const h = fixture(t), a = await h.create(), b = await h.create();
  await b.setVisible(false); h.calls.length = 0; await a.close();
  assert.equal(h.record.peers.size, 1); assert.equal(h.record.peers.get('peer-2'), b);
  assert.deepEqual(h.calls.map(call => call.op), ['close', 'capture_stop']);
  assert.equal(h.host.records.get(h.page), h.record);
  await b.setVisible(true); assert.equal(h.calls.at(-2).op, 'capture_start');
  assert.equal(h.calls.some(call => call.op === 'unbind_sheet'), false);
});

test('pause is serialized behind source creation and uses the latest visible peer set', async t => {
  const h = fixture(t), a = await h.create(), source = deferred(), releaseSource = deferred();
  h.host.sourceFence = async () => { source.resolve(); await releaseSource.promise; return 88; };
  const creating = h.create({ visible: false }); await source.promise;
  const pause = a.setVisible(false); h.calls.length = 0;
  releaseSource.resolve(); const b = await creating; await pause;
  assert.equal(a.visible, false); assert.equal(b.visible, false);
  assert.equal(h.calls.some(call => call.op === 'capture_start'), false);
  assert.equal(h.calls.filter(call => call.op === 'create').length, 1);
  assert.equal(h.calls.at(-1).op, 'capture_stop');
});

test('rapid native pause/resume cannot let an older transaction stop the resumed page', async t => {
  const h = fixture(t), a = await h.create(); h.calls.length = 0;
  const pause = a.setVisible(false), resume = a.setVisible(true); await Promise.all([pause, resume]);
  assert.equal(a.visible, true); assert.equal(h.calls.some(call => call.op === 'capture_stop'), false);
  assert.equal(h.calls.at(-1).op, 'request_idr');
  await assert.rejects(a.setVisible('true'), /invalid/); assert.equal(a.visible, true);
});

test('native page removal waits for an already dispatched pause and closes the warm peer before unbinding', async t => {
  const h = fixture(t), a = await h.create(), stopping = deferred(), finish = deferred();
  h.host.rpc.request = async (op, args) => { h.calls.push({ service: 'native', op, args }); if (op === 'capture_stop') { stopping.resolve(); await finish.promise; } return { ack: true }; };
  const pausing = a.setVisible(false); await stopping.promise;
  const removing = h.host.removePage(h.record); await tick();
  assert.equal(h.calls.some(call => call.op === 'unbind_sheet'), false);
  finish.resolve(); await Promise.all([pausing, removing]);
  assert.equal(h.record.peers.size, 0); assert.equal(h.host.records.has(h.page), false);
  const ops = h.calls.map(call => call.op);
  assert.ok(ops.indexOf('close') < ops.indexOf('unbind_sheet'));
  const before = h.calls.length; await a.setVisible(true); await a.requestFrame(); assert.equal(h.calls.length, before, 'late resume cannot address an unbound page');
});
