import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import WebSocket from 'ws';
import { createWorker } from '../src/server.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';
import { BrowserSession } from '../src/session.js';
import { createNativeDiagnostics } from './helpers/native-diagnostics.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label = 'condition', timeout = 5000) {
  const end = Date.now() + timeout;
  do { const value = await read(); if (value) return value; await delay(15); } while (Date.now() < end);
  assert.fail(label + ' was not observed');
}
const pointer = (phase, x, y, buttons = 0) => ({ type: 'pointer', phase, x, y, button: phase === 'move' ? 'none' : 'left', buttons, modifiers: 0, click_count: phase === 'move' ? 0 : 1 });

test('taking a newly created tab while DevTools attaches prevents its late navigation', async () => {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} });
  session.devtools = { attach: async () => {}, detach: async () => {}, isPaused: () => false, isBlocked: () => false };
  const page = () => ({ url: () => 'about:blank', isClosed: () => false });
  session.pages.set('t1', { id: 't1', page: page(), control: 'agent', controlEpoch: 0, controlPhase: 'ready', version: 1, visual: 1, title: '' }); session.active = 't1';
  let ready, navigations = 0;
  const meta = { id: 't2', page: { ...page(), goto: async () => { navigations++; } }, control: 'agent', controlEpoch: 0, controlPhase: 'ready', version: 1, visual: 1, title: '', devtoolsReady: new Promise(resolve => { ready = resolve; }) };
  session.createPage = async () => { session.pages.set(meta.id, meta); return meta.page; };
  const opening = session.execute({ op_id: 'new-race', action: 'tab', tab_action: 'new', url: 'https://fixture.invalid/should-not-navigate' });
  await until(() => session.operationTouches(meta), 'pending new tab attachment');
  const takeover = session.setControl({ mode: 'human', context_ref: session.contextRef, tab_id: meta.id });
  await until(() => meta.controlPhase === 'takeover');
  ready();
  assert.equal((await opening).ok, false);
  assert.equal((await takeover).control, 'human');
  assert.equal(navigations, 0, 'a late attachment cannot navigate a page already taken over');
});

test('a Docker page creation arriving after cancellation is closed before any navigation', async () => {
  let resolve, closed = 0;
  const context = { on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, newPage: () => new Promise(done => { resolve = done; }) };
  const session = new BrowserSession(context);
  const creating = session.createPage();
  session.generation++;
  resolve({ async close() { closed++; } });
  await assert.rejects(creating, /fenced/);
  assert.equal(closed, 1); assert.equal(session.pages.size, 0);
});

test('a click that closes its source before acknowledgement stays uncertain and is never replayed', async () => {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} }, { settleMs: 0 });
  session.devtools = { attach: async () => {}, isPaused: () => false, isBlocked: () => false };
  let closed = false, clicks = 0;
  const page = { url: () => 'https://fixture.invalid/source', isClosed: () => closed };
  session.pages.set('t1', { id: 't1', page, control: 'agent', controlEpoch: 0, controlPhase: 'ready', version: 1, visual: 1, title: '' }); session.active = 't1';
  session.locator = async () => ({ async click() { clicks++; closed = true; throw new Error('locator.click: Target page, context or browser has been closed'); } });
  const action = { op_id: 'close-before-ack', action: 'click', target: '#close' };
  const result = await session.execute(action);
  assert.equal(result.ok, false); assert.match(result.error, /Input outcome could not be confirmed/);
  assert.match(result.text, /Do not repeat the action/);
  assert.deepEqual(await session.execute(action), result);
  assert.equal(clicks, 1); assert.equal(session.rawAction, null);
});

test('human tab stays isolated while Agent creates another shared-login tab; display resize needs no input lease', { timeout: 90000 }, async () => {
  let worker, receiver;
  const viewers = [];
  const sockets = [];
  let sharedLogin = false, saves = 0;
  const site = http.createServer((request, response) => {
    if (request.url === '/save') { saves++; response.end('saved'); return; }
    if (request.url === '/b') sharedLogin ||= request.headers.cookie?.includes('login=fixture');
    response.setHeader('Content-Type', 'text/html');
    response.setHeader('Set-Cookie', 'login=fixture; SameSite=Lax; Path=/');
    response.end(`<title>Tab ${request.url}</title><style>body{margin:0}input{position:absolute;left:20px;top:20px;width:300px;height:40px}#range{top:100px}button{position:absolute;left:20px;top:180px;width:120px;height:40px}#popup{left:400px}</style><h1 style="margin-top:250px">Tab ${request.url}</h1><input id="text" aria-label="Text"><input id="range" type="range" min="0" max="100" value="0"><button onclick="fetch('/save')">Save</button><button id="popup" onclick="openOwnedPopup(event)">Popup</button><output id="ready"></output><script>
      window.__popupDiagnostic={events:[],invoked:0,returned:0,nullReturns:0,returnedClosed:null,exceptions:[]};
      for(const type of ['pointerdown','pointerup','click']) addEventListener(type,event=>{
        const d=window.__popupDiagnostic;d.events.push({type,trusted:event.isTrusted,target:event.target?.id||'',x:event.clientX,y:event.clientY,buttons:event.buttons,button:event.button,time:performance.now()});if(d.events.length>30)d.events.shift();
      },true);
      function openOwnedPopup(event){const d=window.__popupDiagnostic;d.invoked++;d.invocationTrusted=event.isTrusted;d.invocationTime=performance.now();try{const child=window.open('/popup');d.returned++;d.nullReturns+=child===null?1:0;d.returnedClosed=child?.closed??null;}catch(error){d.exceptions.push(error.name);throw error;}}
    </script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const origin = `http://127.0.0.1:${site.address().port}`;
  try {
    receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true });
    const token = 'scoped-control-fixture';
    worker = await createWorker({ token, actionTimeout: 3000, settleMs: 0 });
    const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/scoped`;
    const request = async (suffix, method = 'GET', body) => {
      const response = await fetch(base + suffix, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
      return { status: response.status, body: await response.json() };
    };
    const execute = action => request('/execute', 'POST', { op_id: randomUUID(), ...action });
    let initial = (await request('', 'PUT', {})).body;
    const context = initial.context_ref, a = initial.active_tab;
    const read = async tab => (await request('/state?context_ref=' + context + '&tab_id=' + tab)).body;
    assert.equal((await execute({ action: 'navigate', url: origin + '/a' })).body.ok, true);
    assert.equal((await request('/control', 'POST', { mode: 'human', context_ref: context })).status, 400, 'control without tab is rejected');
    let human = (await request('/control', 'POST', { mode: 'human', context_ref: context, tab_id: a })).body;
    assert.equal(human.generation, initial.generation, 'tab takeover does not fence another tab generation');
    assert.equal(human.control, 'human'); assert.equal(human.control_epoch, 1);
    const denied = await execute({ action: 'snapshot' });
    assert.equal(denied.status, 409); assert.match(denied.body.error, /tab_action:new/);
    const tabs = await execute({ action: 'tabs' }); assert.equal(tabs.body.ok, true);
    assert.equal(JSON.parse(tabs.body.text)[0].control, 'human');
    const created = await execute({ action: 'tab', tab_action: 'new', url: origin + '/b' });
    assert.equal(created.body.ok, true, JSON.stringify(created));
    const b = created.body.active_tab;
    assert.notEqual(b, a); assert.equal(sharedLogin, true);
    assert.equal((await read(a)).control, 'human'); assert.equal((await read(a)).control_epoch, human.control_epoch);
    assert.equal((await read(b)).control, 'agent');
    const host = worker.runtime.hosts.get('scoped');
    assert.equal(host.displayMode, 'wayland');
    const pageA = host.context.pages().find(page => page.url() === origin + '/a');
    const pageB = host.context.pages().find(page => page.url() === origin + '/b');
    const nativeDiagnostic = createNativeDiagnostics(host, { origin, paths: ['/a', '/b', '/a2', '/popup', '/recovered'] });
    const connect = async tab => {
      const viewer = await createRTCViewer(receiver, base.replace('http:', 'ws:') + `/live?tab_id=${tab}&context_ref=${context}`, token);
      viewers.push(viewer); const { ws, messages, frames } = viewer; sockets.push(ws);
      let seq = 0;
      const send = async (packet, state, lease = '') => {
        const frame = frames.at(-1), current = ++seq;
        ws.send(JSON.stringify({ seq: current, generation: state.generation, control_epoch: state.control_epoch, lease_token: lease, frame_id: frame.frame_id, media_epoch: frame.media_epoch, ...packet }));
        return until(() => messages.find(message => message.seq === current), 'live acknowledgement');
      };
      return { ws, frames, messages, send };
    };
    const liveA = await connect(a), liveB = await connect(b);
    const inputA = event => liveA.send({ type: 'input', event }, human, human.lease_token);
    const stateB = await read(b);
    await pageB.evaluate(() => { setTimeout(() => { document.querySelector('#ready').textContent = 'B finished waiting'; }, 1600); });
    let waitDone = false;
    const waiting = execute({ action: 'wait', text: 'B finished waiting', seconds: 3 }).finally(() => { waitDone = true; });
    await until(async () => (await read(b)).busy, 'Agent B wait');
    assert.equal((await read(a)).busy, false, 'B busy cannot disable human A');
    const busyResize = await liveB.send({ type: 'viewport', width: 1600, height: 1000 }, stateB);
    assert.equal(busyResize.code, 'LIVE_BUSY'); assert.equal(liveB.ws.readyState, WebSocket.OPEN);
    for (const event of [pointer('down', 60, 40, 1), pointer('up', 60, 40), { type: 'text', text: '人工 A 中文🙂' }, pointer('down', 30, 120, 1), pointer('move', 290, 120, 1), pointer('up', 290, 120)]) {
      const reply = await inputA(event); assert.equal(reply.type, 'ack', JSON.stringify(reply));
    }
    assert.equal(await pageA.locator('#text').inputValue(), '人工 A 中文🙂');
    assert.ok(Number(await pageA.locator('#range').inputValue()) >= 90);
    assert.equal(waitDone, false, 'human typing/drag completed while Agent B was still waiting');
    const previousEpoch = human.control_epoch;
    human = (await request('/control', 'POST', { mode: 'human', force: true, context_ref: context, tab_id: a })).body;
    assert.equal(waitDone, false, 'taking A must not wait for or cancel B');
    assert.equal(human.control_epoch, previousEpoch + 1);
    assert.equal((await waiting).body.ok, true);
    await until(() => liveA.frames.at(-1).control_epoch === human.control_epoch, 'new A lease frame');
    const resized = await liveB.send({ type: 'viewport', width: 1600, height: 1000 }, await read(b));
    assert.equal(resized.type, 'ack', JSON.stringify(resized));
    await until(() => liveB.frames.at(-1).viewport.width === 1600);
    assert.deepEqual(await pageB.evaluate(() => ({ width: innerWidth, height: innerHeight })), { width: 1600, height: 1000 });
    assert.deepEqual(await pageA.evaluate(() => ({ width: innerWidth, height: innerHeight })), { width: 1280, height: 800 });
    assert.equal((await liveB.send({ type: 'input', event: { type: 'text', text: 'forbidden' } }, await read(b))).code, 'LIVE_FORBIDDEN');
    const navigated = await request('/input', 'POST', { type: 'navigate', url: origin + '/a2', context_ref: context, target_tab: a, generation: human.generation, lease_token: human.lease_token });
    assert.equal(navigated.body.ok, true, JSON.stringify(navigated));
    assert.equal(navigated.body.active_tab, b, 'human HTTP navigation response keeps the Agent target');
    assert.equal((await request('/state')).body.active_tab, b);
    await until(() => liveA.frames.at(-1).frame_identity === navigated.body.frame_identity, 'A navigation frame');
    const popupBefore = await pageA.evaluate(() => {
      const rect = document.querySelector('#popup').getBoundingClientRect();
      return { viewport: { width: innerWidth, height: innerHeight, dpr: devicePixelRatio }, rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height }, target_at_450_200: document.elementFromPoint(450, 200)?.id ?? null };
    });
    const popupReplies = [];
    const safeReply = reply => ({ type: reply.type, seq: reply.seq, code: reply.code ?? null });
    let popup;
    try {
      for (const event of [pointer('down', 450, 200, 1), pointer('up', 450, 200)]) {
        const reply = await inputA(event); popupReplies.push(safeReply(reply));
        assert.equal(reply.type, 'ack', JSON.stringify(safeReply(reply)));
      }
      popup = await until(async () => (await read(a)).tabs.find(tab => tab.url === origin + '/popup'), 'human popup');
      const clicked = await pageA.evaluate(() => window.__popupDiagnostic);
      assert.equal(clicked.invoked, 1, JSON.stringify(clicked));
      assert.equal(clicked.nullReturns, 0, JSON.stringify(clicked));
      assert.equal(clicked.invocationTrusted, true, JSON.stringify(clicked));
      assert.equal(clicked.events.filter(event => event.type === 'click' && event.target === 'popup' && event.trusted).length, 1, JSON.stringify(clicked));
    } catch (error) {
      let dom;
      try { dom = await Promise.race([pageA.evaluate(() => window.__popupDiagnostic), delay(500).then(() => ({ unavailable: 'bounded diagnostic read' }))]); }
      catch { dom = { unavailable: 'source closed or read failed' }; }
      const frame = liveA.frames.at(-1);
      const evidence = { before: popupBefore, dom, replies: popupReplies, presented: { identity: frame.frame_identity, width: frame.viewport.width, height: frame.viewport.height, pixels: [frame.pixel_width, frame.pixel_height] }, native: nativeDiagnostic.snapshot() };
      assert.fail('human popup failed without replay: ' + error.name + '\n' + JSON.stringify(evidence));
    }
    assert.equal(popup.control, 'human'); assert.equal((await request('/state')).body.active_tab, b);
    const popupPage = host.context.pages().find(page => page.url() === origin + '/popup');
    const popupRecord = host.records.get(popupPage), aRecord = host.records.get(pageA);
    assert.ok(popupRecord, 'the actual popup has a native window binding');
    assert.notEqual(popupRecord.nativeWindow, aRecord.nativeWindow); assert.notEqual(popupRecord.displayId, aRecord.displayId);
    assert.deepEqual(await popupPage.evaluate(() => ({ width: innerWidth, height: innerHeight })), { width: 1280, height: 800 });
    const popupViewer = await connect(popup.id);
    assert.equal(popupViewer.frames.at(-1).tab_id, popup.id);
    assert.equal(popupViewer.frames.at(-1).pixel_width, 1920); assert.equal(popupViewer.frames.at(-1).pixel_height, 1200);
    const switchHuman = await execute({ action: 'tab', tab_action: 'switch', target: popup.id });
    assert.equal(switchHuman.body.ok, false); assert.match(switchHuman.body.error, /human control/);
    assert.equal((await request('/state')).body.active_tab, b);
    const timeout = await execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'new Promise(() => {})', awaitPromise: true, timeout: 30, returnByValue: true } });
    assert.equal(timeout.body.ok, false);
    const blockedHuman = await inputA({ type: 'text', text: 'blocked everywhere' });
    assert.equal(blockedHuman.type, 'error'); assert.match(blockedHuman.error, /unknown|cancel/);
    assert.equal((await execute({ action: 'tab', tab_action: 'new' })).body.ok, false, 'unknown outcome cannot be bypassed by a new tab');
    const cancelled = await request('/cancel', 'POST', {});
    assert.equal(cancelled.status, 200, JSON.stringify(cancelled.body));
    assert.equal(cancelled.body.ok, true, JSON.stringify(cancelled.body));
    const recovered = (await request('/state')).body;
    assert.equal(recovered.tabs.length, 1); assert.equal(recovered.control, 'agent');
    assert.ok(recovered.generation > human.generation, 'cancel fences old native pages and media generation');
    assert.deepEqual((await host.context.cookies()).filter(cookie => cookie.name === 'login').map(cookie => cookie.value), ['fixture'], 'cancel retains the real Chromium profile cookie');
    assert.equal((await execute({ action: 'navigate', url: origin + '/recovered' })).body.ok, true);
    const recoveredViewer = await connect(recovered.active_tab);
    assert.equal(recoveredViewer.frames.at(-1).generation, recovered.generation);
    assert.equal(recoveredViewer.frames.at(-1).tab_id, recovered.active_tab);
    assert.equal(saves, 0);
  } finally { for (const socket of sockets) socket.terminate(); for (const viewer of viewers) await viewer.close(); await receiver?.close(); await worker?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); }
});
