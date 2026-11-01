import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import WebSocket from 'ws';
import { createWorker } from '../src/server.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label, ms = 15000) { const end = Date.now() + ms; do { if (await read()) return; await delay(20); } while (Date.now() < end); assert.fail(label); }
const color = (pixel, channel) => pixel?.[channel] > 220 && pixel.every((value, index) => index === channel || index === 3 || value < 40);

test('default Wayland sessions isolate real profiles, native outputs and received pixels through one session closing', { timeout: 90000 }, async t => {
  const site = http.createServer((request, response) => {
    if (!['/red', '/blue'].includes(request.url)) { response.writeHead(404); response.end('not found'); return; }
    const red = request.url === '/red', name = red ? 'red' : 'blue';
    response.setHeader('Content-Type', 'text/html;charset=utf-8'); response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Set-Cookie', `fixture_color=${name}; Path=/; SameSite=Lax`);
    response.end(`<!doctype html><title>Owned ${name} session</title><style>html,body{margin:0;width:100%;height:100%;background:${red ? '#f00' : '#00f'}}</style><h1>Owned ${name} session</h1><script>window.documentNonce=crypto.randomUUID();window.heartbeat=0;setInterval(function idleHeartbeat(){window.heartbeat++},20)</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  let worker, receiver; const viewers = [];
  t.after(async () => { for (const viewer of viewers) await viewer.close(); await receiver?.close(); await worker?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  const token = 'wayland-context-fixture'; worker = await createWorker({ token, settleMs: 0, actionTimeout: 4000 });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
  const api = async (id, path = '', method = 'GET', body) => { const response = await fetch(`${base}/sessions/${id}${path}`, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }); const value = await response.json(); assert.equal(response.ok, true, JSON.stringify(value)); return value; };
  const a = await api('profile-a', '', 'PUT', {}), b = await api('profile-b', '', 'PUT', {});
  const origin = `http://127.0.0.1:${site.address().port}`;
  for (const [id, name] of [['profile-a', 'red'], ['profile-b', 'blue']]) assert.equal((await api(id, '/execute', 'POST', { op_id: randomUUID(), action: 'navigate', url: `${origin}/${name}` })).ok, true);
  const hostA = worker.runtime.hosts.get('profile-a'), hostB = worker.runtime.hosts.get('profile-b');
  assert.equal(hostA.displayMode, 'wayland'); assert.equal(hostB.displayMode, 'wayland');
  assert.notEqual(hostA.profile, hostB.profile); assert.notEqual(hostA.chrome.pid, hostB.chrome.pid);
  assert.notEqual(a.context_ref, b.context_ref);
  assert.deepEqual((await hostA.context.cookies()).filter(c => c.name === 'fixture_color').map(c => c.value), ['red']);
  assert.deepEqual((await hostB.context.cookies()).filter(c => c.name === 'fixture_color').map(c => c.value), ['blue']);
  assert.equal(hostA.records.size, 1); assert.equal(hostB.records.size, 1);
  for (const host of [hostA, hostB]) { const record = [...host.records.values()][0]; assert.ok(Number.isSafeInteger(record.nativeWindow)); assert.ok(record.displayId >= 2); assert.deepEqual(await record.page.evaluate(() => ({ width: innerWidth, height: innerHeight })), { width: 1280, height: 800 }); assert.equal(await record.page.evaluate(() => devicePixelRatio), 1.5, 'the real Chrome compositor density matches encoded geometry'); }
  receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
  for (const [id, state] of [['profile-a', a], ['profile-b', b]]) viewers.push(await createRTCViewer(receiver, `${base.replace('http:', 'ws:')}/sessions/${id}/live?context_ref=${state.context_ref}&tab_id=${state.active_tab}`, token));
  const [viewA, viewB] = viewers;
  for (const [view, state] of [[viewA, a], [viewB, b]]) { assert.equal(view.frames.at(-1).context_ref, state.context_ref); assert.deepEqual({ width: view.frames.at(-1).pixel_width, height: view.frames.at(-1).pixel_height }, { width: 1920, height: 1200 }); assert.equal(view.messages.find(m => m.type === 'ready').capabilities.runtime, 'wayland'); }
  await until(async () => color(await viewA.pixelAt(800, 700), 0) && color(await viewB.pixelAt(800, 700), 2), 'session-bound red/blue pixels did not reach their receivers');
  const nonceB = await [...hostB.records.values()][0].page.evaluate(() => documentNonce);
  await api('profile-a', '', 'DELETE');
  await until(() => viewA.ws.readyState === WebSocket.CLOSED && !worker.runtime.hosts.has('profile-a'), 'deleted session retained a native peer or compositor');
  assert.equal(hostA.closed, true); assert.equal(hostB.closed, false); assert.equal(viewB.ws.readyState, WebSocket.OPEN);
  const pageB = [...hostB.records.values()][0].page; assert.equal(await pageB.evaluate(() => documentNonce), nonceB);
  await pageB.evaluate(() => { document.documentElement.style.background = '#0f0'; document.body.style.background = '#0f0'; });
  await until(async () => color(await viewB.pixelAt(800, 700), 1), 'closing A stopped B returning pixels');
  assert.equal(viewB.errors.length, 0); assert.equal(viewB.messages.some(m => m.type === 'error'), false);
  const executeB = action => api('profile-b', '/execute', 'POST', { op_id: randomUUID(), ...action });
  for (const method of ['Debugger.enable', 'Debugger.pause']) { const result = await executeB({ action: 'devtools', method, params: {} }); assert.equal(result.ok, true, JSON.stringify(result)); }
  await until(async () => (await api('profile-b', '/state')).debugger_paused, 'the real renderer did not report a debugger pause');
  const pausedEvents = await executeB({ action: 'devtools', method: 'events.list', params: { method: 'Debugger.paused', limit: 100 } });
  assert.equal(pausedEvents.ok, true, JSON.stringify(pausedEvents));
  assert.equal(pausedEvents.data.paused, true, JSON.stringify(pausedEvents));
  assert.ok(pausedEvents.data.items.some(event => event.method === 'Debugger.paused'), JSON.stringify(pausedEvents));
  const pausedView = await createRTCViewer(receiver, `${base.replace('http:', 'ws:')}/sessions/profile-b/live?context_ref=${b.context_ref}&tab_id=${b.active_tab}`, token); viewers.push(pausedView);
  assert.equal(pausedView.frames.at(-1).frame_identity, viewB.frames.at(-1).frame_identity, 'a paused known document keeps causal source identity');
  await until(async () => color(await pausedView.pixelAt(800, 700), 1), 'a new peer on a paused verified document did not receive clean pixels');
  const resumed = await executeB({ action: 'devtools', method: 'Debugger.resume', params: {} }); assert.equal(resumed.ok, true, JSON.stringify(resumed));
  await until(async () => !(await api('profile-b', '/state')).debugger_paused, 'the renderer did not resume');
  const disabled = await executeB({ action: 'devtools', method: 'Debugger.disable', params: {} }); assert.equal(disabled.ok, true, JSON.stringify(disabled));
  await pausedView.close();
  assert.equal((await executeB({ action: 'tab', tab_action: 'close', target: b.active_tab })).ok, true);
  const blank = await api('profile-b', '/state');
  assert.equal(blank.tabs.length, 1, 'closing the last Sheet creates one fresh local new-tab page');
  assert.equal(blank.url, 'about:blank'); assert.notEqual(blank.active_tab, b.active_tab); assert.equal(blank.context_ref, b.context_ref);
  await until(() => viewB.ws.readyState === WebSocket.CLOSED, 'the old last-Sheet peer was not fenced');
  assert.equal((await executeB({ action: 'navigate', url: origin + '/blue' })).ok, true);
  const reopened = await createRTCViewer(receiver, `${base.replace('http:', 'ws:')}/sessions/profile-b/live?context_ref=${blank.context_ref}&tab_id=${blank.active_tab}`, token); viewers.push(reopened);
  await until(async () => color(await reopened.pixelAt(800, 700), 2), 'the new Sheet did not recover real video after closing the last one');

});
