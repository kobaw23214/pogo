import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label, ms = 15000) {
  const end = Date.now() + ms;
  do { const value = await read(); if (value) return value; await delay(25); } while (Date.now() < end);
  assert.fail(label);
}

test('real Wayland warm viewers pause only the last capture, reuse WS/peer/epoch, and retain the live document', { timeout: 90000 }, async t => {
  let documentRequests = 0, worker, receiver;
  const viewers = [];
  const site = http.createServer((request, response) => {
    if (request.url !== '/page') { response.writeHead(404); response.end(); return; }
    documentRequests++;
    response.setHeader('Content-Type', 'text/html;charset=utf-8'); response.setHeader('Cache-Control', 'no-store');
    response.end(`<!doctype html><title>Warm Sheet fixture</title><style>html,body{margin:0;background:#008000;height:4000px}input{position:fixed;left:20px;top:20px}output{position:fixed;left:20px;top:80px;color:white}</style><input id="draft"><output id="clock"></output><script>window.documentNonce=crypto.randomUUID();window.transientState={answer:42};window.ticks=0;setInterval(()=>{document.querySelector('output').textContent=++window.ticks},40)</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(async () => {
    for (const viewer of viewers) await viewer.close();
    await receiver?.close(); await worker?.close();
    site.closeAllConnections(); await new Promise(resolve => site.close(resolve));
  });
  const token = 'warm-native-fixture'; worker = await createWorker({ token, settleMs: 0, actionTimeout: 4000 });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`, id = 'warm-native';
  const api = async (path, method = 'GET', body) => {
    const response = await fetch(`${base}/sessions/${id}${path}`, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    const value = await response.json(); assert.equal(response.ok, true, JSON.stringify(value)); return value;
  };
  await api('', 'PUT', {});
  const origin = `http://127.0.0.1:${site.address().port}`;
  await api('/execute', 'POST', { op_id: randomUUID(), action: 'navigate', url: origin + '/page' });
  const state = await api('/state'), host = worker.runtime.hosts.get(id), record = [...host.records.values()][0], source = record.page;
  assert.equal(host.displayMode, 'wayland');
  await source.evaluate(() => { document.querySelector('input').value = '尚未提交的中文草稿'; scrollTo(0, 720); transientState.answer = 99; });
  const saved = await source.evaluate(() => ({ nonce: documentNonce, draft: document.querySelector('input').value, scroll: scrollY, transient: transientState.answer }));
  const sourceIdentity = { page: source, nativeWindow: record.nativeWindow, displayId: record.displayId, frameIdentity: state.frame_identity };
  receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  const url = `${base.replace('http:', 'ws:')}/sessions/${id}/live?context_ref=${state.context_ref}&tab_id=${state.viewed_tab}`;
  const a = await createRTCViewer(receiver, url, token); viewers.push(a);
  const b = await createRTCViewer(receiver, url, token); viewers.push(b);
  for (const viewer of viewers) {
    assert.equal(viewer.messages.find(message => message.type === 'ready').capabilities.view_visibility, true);
    viewer.autoAck = false;
    await viewer.page.evaluate(() => { globalThis.originalPeer = pc; });
  }
  const epochs = viewers.map(viewer => viewer.frames.at(-1).media_epoch);
  const visibility = async (viewer, visible, revision) => {
    viewer.ws.send(JSON.stringify({ type: 'view_visibility', visible, revision }));
    await until(() => viewer.messages.find(message => message.type === 'view_visibility' && message.revision === revision && message.visible === visible), 'visibility transition was not acknowledged');
  };
  const encoded = async () => (await host.rpc.request('status')).stats.encoded;
  await visibility(a, false, 1);
  const oneHidden = await encoded(); await delay(300);
  assert.ok(await encoded() > oneHidden, 'a second visible viewer keeps native capture and video moving');
  await visibility(b, false, 1); await delay(150);
  const bothHidden = await encoded(); await delay(300);
  assert.equal(await encoded(), bothHidden, 'warm hidden viewers allocate no encoded frames');
  assert.equal(record.peers.size, 2, 'stopping capture retains both RTP peers');
  const before = await a.decoded();
  await visibility(a, true, 2);
  await until(async () => await a.decoded() > before, 'the original receiver did not decode fresh pixels after resume');
  const frame = a.frames.at(-1);
  a.ws.send(JSON.stringify({ type: 'presentation_ack', frame_id: frame.frame_id, media_epoch: frame.media_epoch, width: frame.pixel_width, height: frame.pixel_height, view_revision: 2 }));
  for (const [index, viewer] of viewers.entries()) {
    assert.equal(viewer.frames.at(-1).media_epoch, epochs[index]);
    assert.equal(viewer.messages.filter(message => message.type === 'media_offer').length, 1);
    assert.equal(viewer.messages.some(message => message.type === 'media_reset'), false);
    assert.equal(await viewer.page.evaluate(() => originalPeer === pc && pc.connectionState === 'connected'), true);
    assert.equal(viewer.errors.length, 0);
    assert.equal(viewer.messages.some(message => message.type === 'error'), false);
  }
  assert.deepEqual(await source.evaluate(() => ({ nonce: documentNonce, draft: document.querySelector('input').value, scroll: scrollY, transient: transientState.answer })), saved);
  assert.equal(documentRequests, 1); assert.equal(record.page, sourceIdentity.page);
  assert.equal(record.nativeWindow, sourceIdentity.nativeWindow); assert.equal(record.displayId, sourceIdentity.displayId);
  assert.equal((await api('/state')).frame_identity, sourceIdentity.frameIdentity);
  await a.close(); viewers.splice(viewers.indexOf(a), 1); await until(() => record.peers.size === 1, 'closed foreground peer remained registered');
  await Promise.all([...record.peerClosures]);
  await delay(150); const siblingWarm = await encoded(); await delay(300);
  assert.equal(await encoded(), siblingWarm, 'closing the last visible viewer pauses a remaining hidden sibling');
  const priorB = await b.decoded(); await visibility(b, true, 2);
  await until(async () => await b.decoded() > priorB, 'the remaining warm sibling did not resume');
  assert.equal(b.messages.filter(message => message.type === 'media_offer').length, 1);
  assert.equal(await b.page.evaluate(() => originalPeer === pc), true);
});
