import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { WaylandRuntime, WaylandSessionHost } from '../src/wayland.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fixture() {
  const calls = [], runtime = { scale: 1.25, hosts: new Map(), rpc: new EventEmitter() };
  runtime.rpc.request = async op => { calls.push('Go:' + op); return { ack: true }; };
  const host = new WaylandSessionHost(runtime, 'lifecycle'); runtime.hosts.set(host.id, host);
  host.rpc = { closed: false, async request(op) { calls.push('native:' + op); return { ack: true }; }, close() { this.closed = true; calls.push('native:channel_closed'); } };
  host.browser = { async close() { calls.push('browser:close'); } };
  host.session = { generation: 1 };
  const page = { isClosed: () => false };
  const record = { page, pageId: 't1', displayId: 2, nativeBound: true, nativeRegistered: true, viewport: { width: 1280, height: 800 }, peers: new Map(), peerClosures: new Set(), gate: Promise.resolve(), creating: 0, captureRevision: 0, client: { async detach() { calls.push('CDP:detach'); } } };
  host.records.set(page, record); host.pending.set(page, Promise.resolve(record));
  return { calls, runtime, host, page, record };
}

test('concurrent page removal waits for source retirement, every peer and both native/Go barriers', async () => {
  const h = fixture(), source = deferred(), peer = deferred(), alreadyClosing = deferred(), unbind = deferred(), unregister = deferred();
  h.record.gate = source.promise; h.record.peerClosures.add(alreadyClosing.promise);
  let closes = 0;
  h.record.peers.set('p1', { close() { closes++; return peer.promise; } });
  h.host.rpc.request = async op => { h.calls.push('native:' + op); if (op === 'unbind_sheet') await unbind.promise; return { ack: true }; };
  h.runtime.rpc.request = async op => { h.calls.push('Go:' + op); if (op === 'unregister_page') await unregister.promise; return { ack: true }; };
  let done = false; const a = h.host.removePage(h.record), b = h.host.removePage(h.record); a.then(() => { done = true; });
  assert.equal(a, b); await tick(); assert.equal(done, false); assert.equal(closes, 0);
  source.resolve(); await tick(); assert.equal(closes, 1); assert.equal(h.calls.includes('native:unbind_sheet'), false);
  peer.resolve(); await tick(); assert.equal(h.calls.includes('native:unbind_sheet'), false, 'a previously removed peer is still retiring');
  alreadyClosing.resolve(); await tick(); assert.equal(h.calls.includes('native:unbind_sheet'), true); assert.equal(h.calls.includes('Go:unregister_page'), false);
  unbind.resolve(); await tick(); assert.equal(h.calls.includes('Go:unregister_page'), true); assert.equal(done, false); assert.equal(h.host.records.has(h.page), true);
  unregister.resolve(); await Promise.all([a, b]); assert.equal(done, true); assert.equal(h.host.records.has(h.page), false);
  assert.equal(h.calls.filter(call => call === 'CDP:detach').length, 1);
});

test('host and runtime concurrent close callers wait for the same underlying cleanup', async () => {
  const h = fixture(), browser = deferred(), unregister = deferred(); h.host.registered = true; h.host.records.clear();
  h.host.browser.close = async () => { h.calls.push('browser:close'); await browser.promise; };
  h.runtime.rpc.request = async op => { h.calls.push('Go:' + op); if (op === 'unregister_session') await unregister.promise; return { ack: true }; };
  let hostDone = false; const a = h.host.close(), b = h.host.close(); a.then(() => { hostDone = true; });
  assert.equal(a, b); await tick(); assert.equal(hostDone, false); assert.equal(h.calls.includes('Go:unregister_session'), false);
  browser.resolve(); await tick(); assert.equal(h.calls.includes('Go:unregister_session'), true); assert.equal(hostDone, false);
  unregister.resolve(); await Promise.all([a, b]); assert.equal(hostDone, true); assert.equal(h.calls.filter(call => call === 'browser:close').length, 1);
  const runtime = new WaylandRuntime({}), hostGate = deferred(); let hostCalls = 0, channelCloses = 0;
  runtime.hosts.set('owned', { close() { hostCalls++; return hostGate.promise; } }); runtime.rpc = { close() { channelCloses++; } };
  let runtimeDone = false; const first = runtime.close(), second = runtime.close(); first.then(() => { runtimeDone = true; });
  assert.equal(first, second); await tick(); assert.equal(runtimeDone, false); assert.equal(channelCloses, 0);
  hostGate.resolve(); await Promise.all([first, second]); assert.equal(hostCalls, 1); assert.equal(channelCloses, 1);
});

async function mediaFixture(scale) {
  const h = fixture(), peer = deferred();
  if (scale !== undefined) h.record.scale = scale;
  h.host.sourceFence = async () => 77;
  h.runtime.rpc.request = async (op, args) => { h.calls.push('Go:' + op); if (op === 'create') return { peer_id: 'peer-owned', offer: { sdp: 'v=0\r\n' }, settings: { width: args.width, height: args.height } }; if (op === 'close') await peer.promise; return { ack: true }; };
  const media = await h.host.createMedia(h.page, { generation: 1, sourceKey: 'known', documentKey: 'known-document' });
  return { ...h, media, peer };
}

test('a native media peer uses its Sheet density independently of the session startup density', async () => {
  const h = await mediaFixture(2);
  assert.deepEqual(h.media.pixels, { width: 2560, height: 1600 });
  assert.equal(h.runtime.scale, 1.25);
  h.peer.resolve(); await h.media.close();
});

test('an in-flight media close is awaited before unbind and can never capture-stop a retired Sheet', async () => {
  const h = await mediaFixture();
  const a = h.media.close(), b = h.media.close(); assert.equal(a, b); assert.equal(h.record.peers.size, 0); assert.equal(h.record.peerClosures.size, 1);
  const removing = h.host.removePage(h.record); await tick(); assert.equal(h.calls.includes('native:unbind_sheet'), false);
  h.peer.resolve(); await Promise.all([a, b, removing]);
  assert.equal(h.calls.filter(call => call === 'Go:close').length, 1);
  assert.equal(h.calls.includes('native:capture_stop'), false, 'late peer cleanup cannot address an unbound output');
  await h.media.close(); assert.equal(h.calls.includes('native:capture_stop'), false);
});

test('host destruction does not confirm while a peer removed from its live map is still closing', async () => {
  const h = await mediaFixture(); h.host.registered = true;
  const peerClosing = h.media.close();
  let done = false; const closing = h.host.close(); closing.then(() => { done = true; });
  try {
    await tick();
    assert.equal(done, false, 'an outstanding peer closure is part of destruction');
    assert.equal(h.calls.includes('Go:unregister_session'), false, 'source registration remains until its old peer retires');
  } finally { h.peer.resolve(); await Promise.all([peerClosing, closing]); }
  assert.equal(done, true);
});

test('runtime destruction waits for an admitted host startup and never publishes its late context', async () => {
  const runtime = new WaylandRuntime({}), startup = deferred(), cleanup = deferred();
  const originalStart = WaylandSessionHost.prototype.start, originalClose = WaylandSessionHost.prototype.close;
  let started = 0, cleanupCalls = 0, contextPublished = false, runtimeClosed = false, channelClosed = 0;
  runtime.connected = () => !runtime.closed; runtime.checkCapacity = async () => {};
  runtime.rpc = { close() { channelClosed++; } };
  WaylandSessionHost.prototype.start = async function() { started++; await startup.promise; this.context = { marker: 'must-not-be-published' }; };
  WaylandSessionHost.prototype.close = function() {
    if (this.closePromise) return this.closePromise;
    cleanupCalls++; this.closed = true;
    this.closePromise = cleanup.promise.then(() => { this.runtime.hosts.delete(this.id); }); return this.closePromise;
  };
  const factory = runtime.factory({ id: 'closing-startup' }).then(result => { contextPublished = true; return result; });
  // Attach the rejection consumer before resolving either controlled barrier.
  const rejected = assert.rejects(factory, /closed during session startup/);
  try {
    await tick(); assert.equal(started, 1); assert.equal(runtime.hosts.size, 1);
    const closing = runtime.close(); closing.then(() => { runtimeClosed = true; });
    await tick(); assert.equal(runtimeClosed, false); assert.equal(channelClosed, 0); assert.equal(cleanupCalls, 0);
    startup.resolve(); await tick(); assert.equal(contextPublished, false); assert.equal(cleanupCalls, 1); assert.equal(runtimeClosed, false);
    cleanup.resolve(); await Promise.all([rejected, closing]);
    assert.equal(contextPublished, false); assert.equal(runtimeClosed, true); assert.equal(cleanupCalls, 1); assert.equal(channelClosed, 1); assert.equal(runtime.hosts.size, 0);
  } finally {
    startup.resolve(); cleanup.resolve(); await rejected.catch(() => {}); await runtime.close();
    WaylandSessionHost.prototype.start = originalStart; WaylandSessionHost.prototype.close = originalClose;
  }
});
