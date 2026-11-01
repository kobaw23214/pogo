import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import WebSocket from 'ws';
import { createWorker } from '../src/server.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, message, timeout = 10000) {
  const end = Date.now() + timeout;
  do { const value = await read(); if (value) return value; await delay(15); } while (Date.now() < end);
  assert.fail(message);
}
const pointer = (phase, x, y, buttons = 0) => ({ type: 'pointer', phase, x, y, button: phase === 'move' ? 'none' : 'left', buttons, modifiers: 0, click_count: phase === 'move' ? 0 : 1 });

test('SPA history changes retain video while real document commits fence old input without disturbing a newer gesture', { timeout: 90000 }, async t => {
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html;charset=utf-8');
    response.end(`<!doctype html><title>Navigation identity fixture</title><style>body{margin:0}button{height:45px}input{position:absolute;top:120px;left:20px;width:300px;height:40px}#range{top:220px}</style><button id=push onclick="history.pushState({},'', '?push='+Date.now())">push</button><button id=replace onclick="history.replaceState({},'', '?replace='+Date.now())">replace</button><button id=same onclick="history.replaceState({},'',location.href)">same URL</button><button id=samepush onclick="history.pushState({},'',location.href)">same URL push</button><button id=hash onclick="location.hash='section-'+Date.now()">hash</button><button id=back onclick="history.back()">back</button><button id=burst onclick="history.pushState({},'', '?burst=0');setTimeout(()=>history.replaceState({},'', '?burst=1'),400);setTimeout(()=>location.hash='burst',800)">burst</button><button id=next onclick="location.href='/next'">new document</button><input id=text><input id=range type=range min=0 max=100 value=0><script>window.nonce=crypto.randomUUID();window.keys=[];window.ups=0;addEventListener('keydown',e=>keys.push('down:'+e.key));addEventListener('keyup',e=>keys.push('up:'+e.key));addEventListener('mouseup',()=>ups++);</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  const worker = await createWorker({ token: 'live-navigation-test', settleMs: 0 });
  let viewer;
  t.after(async () => { await viewer?.close(); await receiver.close(); await worker.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/navigation`;
  const api = async (path, method = 'GET', body) => {
    const response = await fetch(base + path, { method, headers: { Authorization: 'Bearer live-navigation-test', 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    const value = await response.json(); assert.equal(response.ok, true, JSON.stringify(value)); return value;
  };
  await api('', 'PUT', {});
  const origin = `http://127.0.0.1:${site.address().port}`;
  await api('/execute', 'POST', { op_id: randomUUID(), action: 'navigate', url: origin + '/' });
  const host = worker.runtime.hosts.get('navigation');
  assert.equal(host.displayMode, 'wayland');
  const page = host.context.pages().find(page => page.url() === origin + '/');
  let state = await api('/state');
  state = await api('/control', 'POST', { mode: 'human', context_ref: state.context_ref, tab_id: state.active_tab });
  viewer = await createRTCViewer(receiver, base.replace('http:', 'ws:') + `/live?context_ref=${state.context_ref}&tab_id=${state.active_tab}`, 'live-navigation-test');
  let seq = 0;
  const send = async (event, frame = viewer.frames.at(-1)) => {
    const packet = { type: 'input', seq: ++seq, event, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: frame.frame_id, media_epoch: frame.media_epoch };
    viewer.ws.send(JSON.stringify(packet));
    return until(() => viewer.messages.find(message => message.seq === packet.seq), 'input acknowledgement');
  };
  const click = async selector => {
    const box = await page.locator(selector).boundingBox();
    for (const phase of ['down', 'up']) { const reply = await send(pointer(phase, box.x + box.width / 2, box.y + box.height / 2, phase === 'down' ? 1 : 0)); assert.equal(reply.type, 'ack', JSON.stringify(reply)); }
  };
  const original = viewer.frames.at(-1), nonce = await page.evaluate(() => window.nonce);
  const start = viewer.messages.length;
  for (const selector of ['#push', '#replace', '#same', '#samepush', '#hash', '#back', '#burst']) {
    await click(selector); await delay(selector === '#burst' ? 1150 : 370);
    assert.equal(await page.evaluate(() => window.nonce), nonce, selector + ' keeps the document');
    assert.equal((await api('/state')).frame_identity, original.frame_identity, selector + ' keeps authoritative loader identity');
    assert.equal(viewer.frames.at(-1).media_epoch, original.media_epoch, selector + ' keeps the same media peer');
  }
  assert.equal(viewer.messages.slice(start).some(message => ['media_reset', 'media_offer'].includes(message.type)), false, 'same-document routing never tears down media');

  // A true commit must release held keys from the previous document and deny
  // its queued coordinates immediately, while keeping this WebSocket usable.
  await send({ type: 'key', phase: 'down', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 8 });
  await click('#next');
  await until(async () => (await api('/state')).frame_identity !== original.frame_identity, 'new document identity');
  const stale = await send({ type: 'text', text: 'old document must not receive this' }, original);
  assert.equal(stale.code, 'LIVE_FRAME_STALE');
  assert.equal(viewer.ws.readyState, WebSocket.OPEN);
  await until(() => viewer.frames.at(-1).frame_identity !== original.frame_identity, 'new document video');
  assert.notEqual(await page.evaluate(() => window.nonce), nonce);
  assert.equal(await page.locator('#text').inputValue(), '');
  const fresh = viewer.frames.at(-1);
  const count = viewer.messages.filter(message => message.type === 'media_reset').length;
  assert.equal((await send({ type: 'key', phase: 'down', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 8 })).type, 'ack');
  assert.equal((await send(pointer('down', 28, 240, 1))).type, 'ack');
  const beforeLate = await page.evaluate(() => ({ keys: [...window.keys], ups: window.ups }));
  const late = await send(pointer('up', 28, 240), original);
  assert.equal(late.code, 'LIVE_FRAME_STALE');
  await delay(370);
  assert.equal(viewer.frames.at(-1).media_epoch, fresh.media_epoch, 'a late old packet cannot restart the fresh stream');
  assert.equal(viewer.messages.filter(message => message.type === 'media_reset').length, count);
  assert.deepEqual(await page.evaluate(() => ({ keys: [...window.keys], ups: window.ups })), beforeLate, 'a late old packet cannot release a new gesture');
  assert.equal((await send(pointer('move', 300, 240, 1))).type, 'ack');
  assert.equal((await send(pointer('up', 300, 240))).type, 'ack');
  assert.equal((await send({ type: 'key', phase: 'up', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 0 })).type, 'ack');
  assert.ok(Number(await page.locator('#range').inputValue()) >= 90, 'new drag remains active after rejection of old input');

  const beforeReload = await page.evaluate(() => window.nonce), oldIdentity = fresh.frame_identity;
  await page.reload();
  await until(() => viewer.frames.at(-1).frame_identity !== oldIdentity, 'same-URL reload commits a new document');
  assert.notEqual(await page.evaluate(() => window.nonce), beforeReload);
  assert.equal(viewer.ws.readyState, WebSocket.OPEN);
});
