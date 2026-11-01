import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { EventEmitter } from 'node:events';
import { chromium } from 'playwright';
import WebSocket from 'ws';
import { createWorker } from '../src/server.js';
import { LiveView, validateLiveEvent } from '../src/live.js';
import { BrowserSession } from '../src/session.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, message = 'condition not observed', ms = 10000) {
  const end = Date.now() + ms;
  do { const result = await read(); if (result) return result; await delay(15); } while (Date.now() < end);
  assert.fail(message);
}
const pointer = (phase, x, y, buttons = 0) => ({ type: 'pointer', phase, x, y, button: phase === 'move' ? 'none' : 'left', buttons, modifiers: 0, click_count: phase === 'move' ? 0 : 1 });

test('live event protocol rejects arbitrary CDP, unbounded text, and malformed coordinates', () => {
  for (const event of [{ type: 'Runtime.evaluate', expression: '1' }, { type: 'text', text: 'x'.repeat(100001) }, { type: 'pointer', ...pointer('down', Infinity, 3, 1) }, { type: 'key', phase: 'down', key: 'a', code: 'KeyA', key_code: 65, expression: '1' }]) assert.throws(() => validateLiveEvent(event), error => error.status === 400);
  assert.equal(validateLiveEvent({ type: 'text', text: '中文🙂' }).text, '中文🙂');
});

test('an unconfirmed key/button release blocks human and Agent input until cancellation destroys the old page', async () => {
  const session = new BrowserSession({ setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {}, async setOffline() {} });
  session.control = 'human'; session.lease = 'test-lease'; session.active = 't1';
  session.devtools = { attach: async () => {}, isPaused: () => false, isBlocked: () => false, prepareClose() {}, reset: async () => {} };
  const meta = { id: 't1', version: 1, visual: 1, topDocumentVersion: 1, title: 'Fixture', page: { url: () => 'https://fixture.invalid', isClosed: () => false } };
  session.pages.set('t1', meta);
  const socket = new EventEmitter(); socket.readyState = 1; socket.bufferedAmount = 0; socket.send = () => {};
  const view = new LiveView(socket, session, { tab_id: 't1', context_ref: session.contextRef });
  session.liveViews.add(view);
  view.client = { async send(method) { if (method === 'Input.dispatchMouseEvent') throw new Error('transport lost after dispatch'); } };
  view.pressed.add('left');
  await assert.rejects(session.setControl({ mode: 'agent', tab_id: 't1', context_ref: session.contextRef, lease_token: session.lease }), /release could not be confirmed/);
  assert.equal(meta.liveInputBlocked, true);
  assert.equal(session.state().busy, true);
  await assert.rejects(session.input({ type: 'text', text: 'unsafe', lease_token: session.lease }), /cancel the browser/);
  await assert.rejects(session.setControl({ mode: 'agent', tab_id: 't1', context_ref: session.contextRef, lease_token: session.lease }), /cancel the browser/);
  session.control = 'agent';
  await assert.rejects(session.execute({ op_id: 'after-unknown-release', action: 'snapshot' }), /cancel the browser/);
  session.control = 'human';
  let reset = 0;
  session.resetPages = async () => { reset++; session.pages.clear(); session.active = null; };
  const cancelled = await session.cancel();
  assert.equal(reset, 1, 'idle cancellation must destroy a page with unconfirmed pressed state');
  assert.equal(cancelled.busy, false);
});

test('normal queued live input does not advertise busy and interrupt an ongoing drag; only an uncertain timeout does', async () => {
  const session = new BrowserSession({ setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {} });
  session.control = 'human'; session.lease = 'test-lease'; session.active = 't1';
  session.devtools = { attach: async () => {}, isPaused: () => false, isBlocked: () => false };
  session.pages.set('t1', { id: 't1', version: 1, title: 'Fixture', page: { url: () => 'https://fixture.invalid', isClosed: () => false } });
  const binding = { tab_id: 't1', context_ref: session.contextRef, generation: session.generation, control_epoch: session.controlEpoch, lease_token: session.lease };
  let resolve;
  const pending = session.dispatchLiveInput(binding, () => new Promise(done => { resolve = done; }));
  await until(() => resolve);
  assert.equal(session.liveActions.size, 1);
  assert.equal(session.state().busy, false, 'state pushes must not make a live drag release itself');
  await assert.rejects(pending, error => error.status === 504);
  assert.equal(session.state().busy, true, 'uncertain dispatched input is visibly blocked');
  await assert.rejects(session.dispatchLiveInput(binding, () => assert.fail('uncertain input must not be followed by another event')), error => error.status === 409);
  const raw = [...session.liveActions.values()][0].raw; resolve(); await raw;
  assert.equal(session.state().busy, false);
});

test('one source Chromium supports native video, drag, Unicode, resize, strict fencing and cancellation', { timeout: 90000 }, async () => {
  let worker, viewerBrowser, viewer, ws;
  let submits = 0;
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    if (request.url === '/submit') { submits++; response.end('saved'); return; }
    response.end(`<title>Live fixture</title><style>body{margin:0}input{position:absolute;left:20px;top:20px;width:300px;height:40px}#range{top:100px}button{position:absolute;left:20px;top:180px;width:120px;height:40px}</style><input id="text" aria-label="Text"><input id="range" type="range" min="0" max="100" value="0"><button onclick="fetch('/submit')">Save</button><textarea id="multi" style="position:absolute;left:400px;top:20px;width:300px;height:100px"></textarea><script>window.pressed=[];document.addEventListener('keydown',e=>pressed.push('down:'+e.key));document.addEventListener('keyup',e=>pressed.push('up:'+e.key));</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  try {
    viewerBrowser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
    const token = 'live-controlled-fixture', id = 'live-fixture';
    worker = await createWorker({ token, actionTimeout: 2500, settleMs: 0 });
    const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/${id}`;
    const request = async (suffix, method = 'GET', body) => {
      const response = await fetch(base + suffix, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
      return { status: response.status, body: await response.json() };
    };
    const execute = action => request('/execute', 'POST', { op_id: randomUUID(), ...action });
    await request('', 'PUT', {});
    assert.equal((await execute({ action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` })).body.ok, true);
    const host = worker.runtime.hosts.get(id);
    assert.equal(host.displayMode, 'wayland');
    const page = host.context.pages().find(page => page.url() === `http://127.0.0.1:${site.address().port}/`);
    let state = (await request('/state')).body;
    const url = base.replace('http:', 'ws:') + `/live?tab_id=${state.active_tab}&context_ref=${state.context_ref}`;
    viewer = await createRTCViewer(viewerBrowser, url, token); ws = viewer.ws;
    const { messages, frames } = viewer;
    const originalFrame = frames.at(-1);
    assert.equal(originalFrame.type, 'media');
    assert.equal(originalFrame.pixel_width, 1920); assert.equal(originalFrame.pixel_height, 1200);
    assert.equal(messages.find(message => message.type === 'ready').capabilities.runtime, 'wayland');
    assert.deepEqual(messages.find(message => message.type === 'ready').capabilities.codec_preferences, ['standard']);
    assert.match(messages.find(message => message.type === 'media_offer').sdp, /a=rtpmap:\d+ VP8\/90000/);
    assert.equal(messages.some(message => message.type === 'frame' || message.data), false, 'no JPEG frame payload is sent');
    assert.equal((await request('/state')).body.tabs.length, 1, 'internal capture and media pages must not become user tabs');
    let seq = 0;
    const send = async (event, overrides = {}) => {
      const frame = frames.at(-1), packet = { type: 'input', seq: ++seq, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token ?? '', frame_id: frame.frame_id, media_epoch: frame.media_epoch, event, ...overrides };
      ws.send(JSON.stringify(packet));
      return until(() => messages.find(message => message.seq === packet.seq), 'input was not acknowledged');
    };
    assert.equal((await send({ type: 'text', text: 'forbidden' })).code, 'LIVE_FORBIDDEN');
    viewer.autoAck = false;
    state = (await request('/control', 'POST', { mode: 'human', context_ref: state.context_ref, tab_id: state.viewed_tab ?? state.active_tab })).body;
    await until(() => frames.at(-1)?.generation === state.generation && frames.at(-1)?.control_epoch === state.control_epoch, 'no fresh human generation frame');
    assert.equal((await send({ type: 'text', text: 'unpresented' })).code, 'LIVE_FRAME_STALE', 'decoded but unacknowledged video cannot authorize input');
    viewer.acknowledge(frames.at(-1)); viewer.autoAck = true;
    { const reply = await send(pointer('down', 60, 40, 1)); assert.equal(reply.type, 'ack', JSON.stringify(reply)); }
    assert.equal((await send(pointer('up', 60, 40))).type, 'ack');
    assert.equal((await send({ type: 'text', text: '中文输入🙂一次' })).type, 'ack');
    assert.equal(await page.locator('#text').inputValue(), '中文输入🙂一次');
    await send(pointer('down', 450, 40, 1)); await send(pointer('up', 450, 40));
    await send({ type: 'text', text: 'line1' });
    await send({ type: 'key', phase: 'down', key: 'Enter', code: 'Enter', key_code: 13, modifiers: 0 });
    await send({ type: 'key', phase: 'up', key: 'Enter', code: 'Enter', key_code: 13, modifiers: 0 });
    await send({ type: 'text', text: 'line2' });
    assert.equal(await page.locator('#multi').inputValue(), 'line1\nline2');
    await send({ type: 'key', phase: 'down', key: 'Backspace', code: 'Backspace', key_code: 8, modifiers: 0 });
    await send({ type: 'key', phase: 'up', key: 'Backspace', code: 'Backspace', key_code: 8, modifiers: 0 });
    assert.equal(await page.locator('#multi').inputValue(), 'line1\nline');
    { const reply = await send(pointer('down', 30, 120, 1)); assert.equal(reply.type, 'ack', JSON.stringify(reply)); }
    const dragStarted = Date.now();
    const dragMessages = [];
    for (let x = 40; x <= 300; x += 10) {
      const packet = { type: 'input', seq: ++seq, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: frames.at(-1).frame_id, media_epoch: frames.at(-1).media_epoch, event: pointer('move', x, 120, 1) };
      dragMessages.push(packet.seq); ws.send(JSON.stringify(packet));
    }
    await until(() => dragMessages.every(value => messages.some(message => message.seq === value)), 'drag queue did not drain');
    assert.equal(messages.filter(message => dragMessages.includes(message.seq) && message.type === 'error').length, 0);
    await send(pointer('up', 300, 120));
    { const value = await page.locator('#range').inputValue(); assert.ok(Number(value) >= 90, 'real range thumb moved through pointer down/move/up: ' + value); }
    assert.ok(Date.now() - dragStarted < 2500, 'drag events do not each wait for a DOM observation');
    const oldFrame = frames.at(-1);
    const resizeStart = messages.length;
    const setViewport = host.setViewport.bind(host);
    host.setViewport = async (target, value) => {
      // Cross the 300ms state poll on both sides of the actual resize.
      await delay(350); await setViewport(target, value); await delay(350);
    };
    const resizeSeq = ++seq;
    ws.send(JSON.stringify({ type: 'viewport', seq: resizeSeq, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: oldFrame.frame_id, media_epoch: oldFrame.media_epoch, width: 900, height: 600 }));
    assert.equal((await until(() => messages.find(message => message.seq === resizeSeq))).type, 'ack');
    await until(() => frames.at(-1).viewport.width === 904, 'resize did not yield a new frame');
    host.setViewport = setViewport;
    await delay(350);
    assert.equal(messages.slice(resizeStart).filter(message => message.type === 'media_reset').length, 1, 'slow resize retires only its old epoch');
    assert.equal(messages.slice(resizeStart).filter(message => message.type === 'media_offer').length, 1, 'polling never starts an intermediate resize stream');
    assert.deepEqual(await page.evaluate(() => ({ width: innerWidth, height: innerHeight })), { width: 904, height: 600 });
    assert.equal((await request('/state')).body.viewport.width, 904);
    assert.equal((await send({ type: 'text', text: 'old-frame' }, { frame_id: oldFrame.frame_id })).code, 'LIVE_FRAME_STALE');
    await send(pointer('down', 60, 200, 1)); await send(pointer('up', 60, 200));
    await until(() => submits === 1);
    const duplicate = { type: 'input', seq, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: frames.at(-1).frame_id, media_epoch: frames.at(-1).media_epoch, event: pointer('down', 60, 200, 1) };
    const messageStart = messages.length;
    ws.send(JSON.stringify(duplicate));
    assert.equal((await until(() => messages.slice(messageStart).find(message => message.seq === seq))).code, 'LIVE_INVALID');
    assert.equal(submits, 1, 'no replay of a submitted mutation');
    const key = { type: 'key', phase: 'down', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 8 };
    await send(key);
    const oldState = state;
    const returned = await request('/control', 'POST', { mode: 'agent', context_ref: state.context_ref, tab_id: state.viewed_tab ?? state.active_tab, lease_token: state.lease_token });
    assert.equal(returned.status, 200);
    await until(async () => (await page.evaluate(() => window.pressed)).includes('up:Shift'), 'handoff left Shift pressed');
    assert.equal((await send({ type: 'text', text: 'stale-lease' })).code, 'LIVE_FORBIDDEN');
    state = returned.body;
    await until(() => frames.at(-1).generation === state.generation && frames.at(-1).control_epoch === state.control_epoch);
    state = (await request('/control', 'POST', { mode: 'human', context_ref: state.context_ref, tab_id: state.viewed_tab ?? state.active_tab })).body;
    await until(() => frames.at(-1).generation === state.generation && frames.at(-1).control_epoch === state.control_epoch);
    assert.notEqual(state.lease_token, oldState.lease_token);
    // Presentation acknowledgement is an input capability, not video flow
    // control: one acknowledgement unlocks this epoch; later frames keep
    // arriving without any per-frame application acknowledgement.
    viewer.autoAck = false;
    const decodedBefore = await viewer.decoded(), offeredBefore = frames.length;
    for (const color of ['red', 'blue', 'green', 'yellow']) { await page.evaluate(value => { document.body.style.background = value; }, color); await delay(80); }
    await until(async () => await viewer.decoded() > decodedBefore + 2, 'video stalled waiting for per-frame acknowledgements');
    assert.equal(frames.length, offeredBefore, 'ordinary repaints do not recreate the peer or grant a new epoch');
    assert.equal(messages.some(message => message.type === 'frame'), false);
    viewer.autoAck = true;
    await send(key);
    ws.close(); await until(() => ws.readyState === WebSocket.CLOSED);
    await until(async () => (await page.evaluate(() => window.pressed)).filter(value => value === 'up:Shift').length === 2, 'disconnect left a pressed key');
    assert.equal(submits, 1);
    assert.equal((await request('/cancel', 'POST', {})).status, 200);
  } finally {
    ws?.terminate(); await viewer?.close(); await viewerBrowser?.close(); await worker?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve));
  }
});

test('live upgrade rejects unauthenticated and mismatched bindings before opening a socket', { timeout: 60000 }, async () => {
  const worker = await createWorker({ token: 'live-auth-fixture', settleMs: 0 });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
  try {
    const response = await fetch(base + '/sessions/auth-test', { method: 'PUT', headers: { Authorization: 'Bearer live-auth-fixture', 'Content-Type': 'application/json' }, body: '{}' });
    const state = await response.json();
    const attempt = (query, token) => new Promise(resolve => {
      const ws = new WebSocket(base.replace('http:', 'ws:') + '/sessions/auth-test/live?' + query, { headers: { Authorization: `Bearer ${token}` } });
      ws.on('unexpected-response', (_, response) => { resolve(response.statusCode); response.resume(); ws.terminate(); });
      ws.on('error', () => {});
    });
    assert.equal(await attempt(`tab_id=t1&context_ref=${state.context_ref}`, 'wrong'), 401);
    assert.equal(await attempt('tab_id=t1&context_ref=ctx_' + '0'.repeat(24), 'live-auth-fixture'), 409);
    assert.equal(await attempt(`tab_id=t1&tab_id=t2&context_ref=${state.context_ref}`, 'live-auth-fixture'), 400);
  } finally { await worker.close(); }
});

test('a same-Sheet observer closing preserves the owner drag, held keys and decoded pixels', { timeout: 60000 }, async t => {
  const site = http.createServer((request, response) => { response.setHeader('Content-Type', 'text/html'); response.end('<title>Shared capture fixture</title><style>body{margin:0}input{position:absolute;left:20px;top:100px;width:300px;height:40px}</style><input id=range type=range min=0 max=100 value=0><script>window.ups=0;window.keyUps=[];addEventListener("pointerup",()=>ups++);addEventListener("keyup",e=>keyUps.push(e.key));</script>'); });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { site.closeAllConnections(); site.close(resolve); }));
  const worker = await createWorker({ token: 'live-two-viewers', settleMs: 0 });
  const receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
  const viewers = [];
  try {
    const response = await fetch(base + '/sessions/two', { method: 'PUT', headers: { Authorization: 'Bearer live-two-viewers', 'Content-Type': 'application/json' }, body: '{}' });
    const state = await response.json();
    assert.equal(state.tabs.length, 1, 'media authority and transport stay outside the visible tab list');
    const fixtureURL = `http://127.0.0.1:${site.address().port}/`;
    const navigated = await fetch(base + '/sessions/two/execute', { method: 'POST', headers: { Authorization: 'Bearer live-two-viewers', 'Content-Type': 'application/json' }, body: JSON.stringify({ op_id: randomUUID(), action: 'navigate', url: fixtureURL }) });
    assert.equal((await navigated.json()).ok, true);
    const page = worker.runtime.hosts.get('two').context.pages().find(page => page.url() === fixtureURL);
    assert.ok(page, 'owned user page exists');
    const url = base.replace('http:', 'ws:') + `/sessions/two/live?tab_id=${state.active_tab}&context_ref=${state.context_ref}`;
    const a = await createRTCViewer(receiver, url, 'live-two-viewers', { scale: 2 }); viewers.push(a);
    const b = await createRTCViewer(receiver, url, 'live-two-viewers', { scale: 1 }); viewers.push(b);
    assert.equal(b.frames.at(-1).pixel_width, a.frames.at(-1).pixel_width, 'different viewer scale must use actual shared source dimensions');
    const aCount = await a.decoded(), bCount = await b.decoded();
    await page.evaluate(() => { document.body.style.background = 'red'; });
    await until(async () => await a.decoded() > aCount && await b.decoded() > bCount, 'both viewers did not decode the same page repaint');
    await until(async () => { const p = await a.pixelAt(800, 700); return p?.[0] > 220 && p[1] < 40 && p[2] < 40; }, 'receiver pixels did not contain the red page');
    const leaseResponse = await fetch(base + '/sessions/two/control', { method: 'POST', headers: { Authorization: 'Bearer live-two-viewers', 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'human', context_ref: state.context_ref, tab_id: state.active_tab }) });
    const human = await leaseResponse.json(); assert.equal(leaseResponse.status, 200);
    await until(() => a.frames.at(-1)?.control_epoch === human.control_epoch && b.frames.at(-1)?.control_epoch === human.control_epoch, 'both viewers did not receive the ownership epoch');
    let sequence = 0;
    const sendOwner = async event => { const frame = a.frames.at(-1), seq = ++sequence; a.ws.send(JSON.stringify({ type: 'input', seq, generation: human.generation, control_epoch: human.control_epoch, lease_token: human.lease_token, frame_id: frame.frame_id, media_epoch: frame.media_epoch, event })); const reply = await until(() => a.messages.find(m => m.seq === seq), 'owner input acknowledgement'); assert.equal(reply.type, 'ack', JSON.stringify(reply)); };
    await sendOwner({ type: 'key', phase: 'down', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 8 });
    await sendOwner(pointer('down', 30, 120, 1));
    b.ws.close(); await until(() => b.ws.readyState === WebSocket.CLOSED); await delay(100);
    assert.equal(await page.evaluate(() => window.ups), 0, 'closing an observer cannot release the owner button');
    assert.equal(await page.evaluate(() => window.keyUps.includes('Shift')), false, 'closing an observer cannot release the owner modifier');
    await sendOwner(pointer('move', 300, 120, 1)); await sendOwner(pointer('up', 300, 120));
    await sendOwner({ type: 'key', phase: 'up', key: 'Shift', code: 'ShiftLeft', key_code: 16, modifiers: 0 });
    assert.ok(Number(await page.locator('#range').inputValue()) >= 90, 'the owner drag remained active after the observer closed');
    const remaining = await a.decoded();
    await page.evaluate(() => { document.body.style.background = 'blue'; });
    await until(async () => await a.decoded() > remaining, 'closing one viewer stopped the other native capture');
    await until(async () => { const p = await a.pixelAt(800, 700); return p?.[2] > 220 && p[0] < 40 && p[1] < 40; }, 'closing an observer stopped the returning blue pixels');
    assert.equal(a.errors.length, 0);
    assert.equal(a.messages.some(message => message.type === 'error'), false);
  } finally { for (const viewer of viewers) await viewer.close(); await receiver.close(); await worker.close(); }
});


test('a viewer that never acknowledges video presentation times out and releases encoder capacity', { timeout: 60000 }, async t => {
  const site = http.createServer((request, response) => { response.setHeader('Content-Type', 'text/html'); response.end('<h1>Presentation timeout fixture</h1>'); });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const worker = await createWorker({ token: 'presentation-timeout', settleMs: 0 });
  const receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  const viewers = [];
  t.after(async () => { for (const viewer of viewers) await viewer.close(); await receiver.close(); await worker.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/presentation`;
  const api = async (path, body) => (await fetch(base + path, { method: path ? 'POST' : 'PUT', headers: { Authorization: 'Bearer presentation-timeout', 'Content-Type': 'application/json' }, body: JSON.stringify(body) })).json();
  const state = await api('', {});
  assert.equal((await api('/execute', { op_id: randomUUID(), action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` })).ok, true);
  const url = base.replace('http:', 'ws:') + `/live?tab_id=${state.active_tab}&context_ref=${state.context_ref}`;
  const stalled = await createRTCViewer(receiver, url, 'presentation-timeout', { autoAcknowledge: false }); viewers.push(stalled);
  assert.ok(stalled.frames.length, 'actual video is decoded, but it was not acknowledged to the worker');
  const host = worker.runtime.hosts.get('presentation'), record = [...host.records.values()].find(row => row.page.url().startsWith('http:'));
  assert.equal(record.peers.size, 1);
  const frame = stalled.frames.at(-1);
  stalled.ws.send(JSON.stringify({ type: 'presentation_ack', media_epoch: frame.media_epoch, frame_id: frame.frame_id, width: frame.pixel_width + 2, height: frame.pixel_height }));
  await until(() => stalled.ws.readyState === WebSocket.CLOSED, 'unacknowledged viewer did not expire', 18000);
  assert.ok(stalled.messages.some(message => message.type === 'error' && /present|acknowledge|video/i.test(message.error)));
  await until(() => record.peers.size === 0, 'expired peer still owns native encoder state');
  const replacement = await createRTCViewer(receiver, url, 'presentation-timeout'); viewers.push(replacement);
  assert.ok(replacement.frames.length, 'a fresh native stream decodes after the expired peer releases its state');
});
