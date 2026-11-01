import test, { before, after, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';

let worker, site, base, origin;
let mutations = 0;
let submissions = 0;
let observedPointer = null;
let op = 0;
const token = 'smoke-test-only-browser-token';
const ownedSessions = new Set();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function request(path, method = 'GET', body, bearer = token) {
  // These single-page fixtures bind ownership to the observed tab explicitly.
  if (path.endsWith('/control') && body) {
    const state = (await request(path.slice(0, -8) + '/state', 'GET', undefined, bearer)).data;
    body = { tab_id: state.active_tab, context_ref: state.context_ref, ...body };
  }
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${bearer}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  const data = response.headers.get('content-type')?.includes('json') ? await response.json() : Buffer.from(await response.arrayBuffer());
  return { status: response.status, data, headers: response.headers };
}
const execute = (id, action) => request(`/sessions/${id}/execute`, 'POST', { op_id: `op-${++op}`, ...action });
async function open(id, path = '/fixture') {
  ownedSessions.add(id);
  const created = await request(`/sessions/${id}`, 'PUT');
  assert.equal(created.status, 200, JSON.stringify(created.data));
  const result = await execute(id, { action: 'navigate', url: origin + path });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  return result.data;
}
function ref(snapshot, name) {
  const line = snapshot.text.split('\n').find(s => s.startsWith('[e') && s.includes(JSON.stringify(name)));
  assert.ok(line, `missing ${name} in ${snapshot.text}`);
  return /^\[(e\d+)\]/.exec(line)[1];
}

before(async () => {
  site = http.createServer((req, res) => {
    if (req.url.startsWith('/observed-pointer?')) {
      const query = new URL(req.url, 'http://fixture').searchParams;
      observedPointer = { x: Number(query.get('x')), y: Number(query.get('y')) };
      res.end('ok'); return;
    }
    if (req.url === '/telemetry-child') {
      res.setHeader('Content-Type', 'text/html');
      res.end('<!doctype html><label>Child input<input id="child-input"></label><button id="child-button">Child button</button>'); return;
    }
    if (req.url === '/mutate') { mutations++; res.end('ok'); return; }
    if (req.url === '/login') {
      if (req.method === 'POST') submissions++;
      res.setHeader('Set-Cookie', 'signed_in=yes; HttpOnly; Path=/; SameSite=Lax');
      res.end('<h1>Signed in</h1>'); return;
    }
    if (req.url === '/cookie') { res.end(`<h1>${req.headers.cookie?.includes('signed_in=yes') ? 'Authenticated' : 'Anonymous'}</h1>`); return; }
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.setHeader('X-Frame-Options', 'DENY');
    res.setHeader('Content-Security-Policy', "frame-ancestors 'none'");
    res.end(`<!doctype html><title>Native fixture</title><h1>原站页面</h1>
      <form action="/login" method="post"><label>Username<input id="user" name="user"></label><label>Password<input id="password" type="password"></label><button>Sign in</button></form>
      <button id="count" onclick="fetch('/mutate');this.textContent='Counted'">Count</button>
      <button id="confirm" onclick="if(confirm('Approve this change?')){fetch('/mutate');this.textContent='Approved'}">Confirm</button>
      <button id="nav" onclick="location.href='/other'">Navigate</button>
      <select id="choice" aria-label="Choice"><option value="a">Alpha</option><option value="b">Beta</option></select>
      <button id="move" style="position:absolute;left:700px;top:300px" onclick="fetch('/mutate')">Moving</button>
      <button id="synthetic" onclick="document.dispatchEvent(new PointerEvent('pointerdown',{clientX:999,clientY:777,bubbles:true}))">Synthetic event</button>
      ${req.url === '/telemetry' ? '<iframe src="/telemetry-child" style="position:absolute;left:600px;top:450px;width:500px;height:150px"></iframe>' : ''}
      <div id="dynamic"></div><div style="height:1600px">Scrollable content</div>
      <script>document.addEventListener('pointerdown',e=>{if(e.isTrusted)fetch('/observed-pointer?x='+e.clientX+'&y='+e.clientY)});
      if(location.pathname==='/moving')setTimeout(()=>{document.querySelector('#move').style.left='850px'},600)</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 2500, idleMs: 60_000 });
  const address = await worker.listen(0);
  base = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  await worker?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

afterEach(async () => {
  // Each test owns its sessions; tests that compare two live sessions retain
  // both until this hook. No native process may accumulate into the next case.
  const failures = [];
  for (const id of ownedSessions) {
    try {
      const removed = await request(`/sessions/${id}`, 'DELETE');
      if (![200, 404].includes(removed.status)) failures.push(`${id}: ${JSON.stringify(removed.data)}`);
    } catch (error) { failures.push(`${id}: ${error.message}`); }
  }
  ownedSessions.clear();
  assert.deepEqual(failures, [], 'every owned fixture session must confirm cleanup');
});

test('bearer required, idempotent session create, direct original URL and DOM actions', async () => {
  assert.equal((await request('/sessions/one', 'PUT', undefined, 'wrong')).status, 401);
  const snapshot = await open('one');
  assert.equal(snapshot.url, origin + '/fixture');
  assert.match(snapshot.text, /原站页面/);
  const original = (await request('/sessions/one/state')).data.active_tab;
  assert.equal((await request('/sessions/one', 'PUT')).data.active_tab, original);
  const typed = await execute('one', { action: 'type', target: ref(snapshot, 'Username'), text: '中文用户' });
  assert.equal(typed.data.ok, true, JSON.stringify(typed.data));
  assert.match(typed.data.text, /中文用户/);
  const choice = await execute('one', { action: 'select', target: '#choice', text: 'Beta' });
  assert.equal(choice.data.ok, true);
  const found = await execute('one', { action: 'find', role: 'button', name: 'Count' });
  assert.match(found.data.text, /\[e\d+\] button "Count"/);
});

test('real JPEG frame and explicit model screenshot carry a viewport version', async () => {
  await open('screen');
  const frame = await request('/sessions/screen/frame');
  assert.equal(frame.status, 200, JSON.stringify(frame.data));
  assert.equal(frame.data.readUInt16BE(), 0xffd8);
  assert.ok(frame.headers.get('x-browser-version'));
  const shot = await execute('screen', { action: 'screenshot' });
  assert.equal(shot.data.ok, true, JSON.stringify(shot.data));
  assert.equal(shot.data.screenshot.width, 1280);
  assert.equal(shot.data.screenshot.height, 800);
  assert.equal(Buffer.from(shot.data.screenshot.data, 'base64').readUInt16BE(), 0xffd8);
  assert.ok(shot.data.version);
});

function overlay(frame) {
  assert.equal(frame.status, 200, JSON.stringify(frame.data));
  const value = JSON.parse(Buffer.from(frame.headers.get('x-browser-overlay'), 'base64url').toString());
  assert.equal(value.version, frame.headers.get('x-browser-version'));
  assert.equal(value.viewport.width, 1280);
  assert.equal(value.viewport.height, 800);
  return value;
}

async function stableOverlay(path) {
  const until = Date.now() + 2000;
  do {
    const frame = await request(path);
    assert.ok([200, 409].includes(frame.status), JSON.stringify(frame.data));
    if (frame.status === 200 && frame.headers.get('x-browser-version') && frame.headers.get('x-browser-overlay')) return overlay(frame);
    await delay(30); // Only reacquire observation, never repeat the action.
  } while (Date.now() < until);
  assert.fail('a stable page did not restore an interactive frame and overlay');
}

test('viewer telemetry is passive, reports real pointer events and keeps fill focus separate', async () => {
  await open('telemetry');
  const initial = overlay(await request('/sessions/telemetry/frame'));
  const repeated = overlay(await request('/sessions/telemetry/frame'));
  assert.equal(initial.version, repeated.version, 'sampling must not mutate page revision');
  assert.equal(initial.pointer, undefined, 'no invented mouse position before a real pointer event');
  const clicked = await execute('telemetry', { action: 'click', target: '#synthetic' });
  assert.equal(clicked.data.ok, true);
  const click = overlay(await request('/sessions/telemetry/frame'));
  assert.equal(click.last_action.phase, 'succeeded');
  assert.equal(click.last_action.action, 'click');
  assert.equal(click.pointer.action_seq, click.last_action.seq);
  assert.equal(click.pointer.kind, 'click');
  assert.deepEqual({ x: click.pointer.x, y: click.pointer.y }, observedPointer);
  assert.notEqual(click.pointer.x, 999, 'synthetic pointer event must be ignored');
  const box = click.target.rect;
  assert.ok(click.pointer.x >= box.x && click.pointer.x <= box.x + box.width);
  const pointer = click.pointer;
  const typed = await execute('telemetry', { action: 'type', target: '#password', text: 'do-not-export-this-password' });
  assert.equal(typed.data.ok, true);
  const fill = overlay(await request('/sessions/telemetry/frame'));
  assert.equal(fill.last_action.action, 'type');
  assert.equal(fill.focus.editable, true);
  assert.deepEqual(fill.pointer, pointer, 'fill must not fabricate a mouse move');
  assert.ok(fill.target.rect.width > 0 && fill.focus.rect.width > 0);
  assert.ok(!JSON.stringify(fill).includes('do-not-export'));
  const state = (await request('/sessions/telemetry/state')).data;
  assert.ok(!JSON.stringify(state.activity).includes('do-not-export'));
  const again = overlay(await request('/sessions/telemetry/frame'));
  assert.equal(fill.version, again.version, 'focus/pointer overlays must not change screenshot version');
  assert.equal((await execute('telemetry', { action: 'hover', target: '#move' })).data.ok, true);
  const hovered = overlay(await request('/sessions/telemetry/frame'));
  assert.equal(hovered.pointer.kind, 'move');
  assert.equal(hovered.pointer.actor, 'agent');
  assert.equal(hovered.pointer.action_seq, hovered.last_action.seq);
  assert.equal((await execute('telemetry', { action: 'press', key: 'Tab' })).data.ok, true);
  const keyboard = overlay(await request('/sessions/telemetry/frame'));
  assert.deepEqual(keyboard.pointer, hovered.pointer, 'keyboard focus changes cannot claim the mouse');
});

test('batch child activity has phases, iframe targets have boxes without guessed pointers, cancellation clears geometry', async () => {
  const snapshot = await open('telemetry-iframe', '/telemetry');
  const child = ref(snapshot, 'Child input');
  const result = await execute('telemetry-iframe', { action: 'batch', actions: [
    { action: 'type', target: child, text: 'iframe text' },
    { action: 'snapshot' },
  ] });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  const state = (await request('/sessions/telemetry-iframe/state')).data;
  const recent = state.activity.recent_actions.slice(-2);
  assert.deepEqual(recent.map(a => [a.action, a.phase]), [['type', 'succeeded'], ['snapshot', 'succeeded']]);
  assert.ok(recent[1].seq > recent[0].seq);
  await execute('telemetry-iframe', { action: 'click', target: child });
  const frame = overlay(await request('/sessions/telemetry-iframe/frame'));
  assert.equal(frame.pointer, undefined, 'frame-local coordinates must not be drawn in main viewport');
  assert.ok(frame.target.rect.x > 600 && frame.target.rect.y > 450);
  assert.equal(frame.focus.editable, true);
  const waiting = execute('telemetry-iframe', { action: 'wait', text: 'does not appear' });
  await delay(60);
  const started = (await request('/sessions/telemetry-iframe/state')).data.activity.last_action;
  assert.equal(started.phase, 'started');
  await request('/sessions/telemetry-iframe/cancel', 'POST');
  await waiting;
  const cancelled = await stableOverlay('/sessions/telemetry-iframe/frame');
  assert.equal(cancelled.last_action.phase, 'failed');
  assert.equal(cancelled.target, undefined);
  assert.equal(cancelled.pointer, undefined);
  assert.equal(cancelled.focus, undefined);
});

test('op_id deduplicates a real mutation and rejects changed replay', async () => {
  await open('dedup');
  const before = mutations;
  const body = { op_id: 'exactly-once', action: 'click', target: '#count' };
  assert.equal((await request('/sessions/dedup/execute', 'POST', body)).data.ok, true);
  assert.equal((await request('/sessions/dedup/execute', 'POST', body)).data.ok, true);
  assert.equal(mutations, before + 1);
  assert.equal((await request('/sessions/dedup/execute', 'POST', { ...body, target: '#confirm' })).status, 409);
});

test('navigation fences remaining batch and invalidates old element refs', async () => {
  const snapshot = await open('batch');
  const oldRef = ref(snapshot, 'Count');
  const before = mutations;
  const result = await execute('batch', { action: 'batch', actions: [{ action: 'click', target: '#nav' }, { action: 'click', target: '#count' }] });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  assert.equal(result.data.steps.length, 1);
  assert.equal(mutations, before);
  const stale = await execute('batch', { action: 'click', target: oldRef });
  assert.equal(stale.data.ok, false);
  assert.match(stale.data.error, /stale ref/);
});

test('cancellation interrupts a real wait, fences queued mutation, preserves cookies', async () => {
  await open('cancel', '/login');
  await execute('cancel', { action: 'navigate', url: origin + '/fixture' });
  const before = mutations;
  const waiting = execute('cancel', { action: 'wait', text: 'will never appear', seconds: 30 });
  await delay(80);
  const queued = execute('cancel', { action: 'click', target: '#count' });
  await delay(30);
  const cancelled = await request('/sessions/cancel/cancel', 'POST');
  assert.equal(cancelled.status, 200, JSON.stringify(cancelled.data));
  assert.equal(cancelled.data.url, 'about:blank');
  assert.equal((await waiting).data.ok, false);
  assert.equal((await queued).data.ok, false);
  assert.equal(mutations, before);
  const cookie = await execute('cancel', { action: 'navigate', url: origin + '/cookie' });
  assert.match(cookie.data.text, /Authenticated/);
  const isolated = await open('isolated', '/cookie');
  assert.match(isolated.text, /Anonymous/);
});

test('human lease is exclusive, supports input and idle agent cancel preserves page', async () => {
  await open('human');
  const owner = await request('/sessions/human/control', 'POST', { mode: 'human' });
  assert.equal(owner.status, 200, JSON.stringify(owner.data));
  const lease_token = owner.data.lease_token;
  assert.ok(lease_token);
  assert.equal((await request('/sessions/human/control', 'POST', { mode: 'human' })).status, 409);
  assert.equal((await execute('human', { action: 'click', target: '#count' })).status, 409);
  assert.equal((await request('/sessions/human/input', 'POST', { type: 'text', text: 'wrong', lease_token: 'wrong' })).status, 403);
  const cancelled = await request('/sessions/human/cancel', 'POST');
  assert.equal(cancelled.data.url, origin + '/fixture');
  const frame = await request('/sessions/human/frame');
  const click = await request('/sessions/human/input', 'POST', { type: 'click', x: 130, y: 89, version: frame.headers.get('x-browser-version'), lease_token });
  assert.equal(click.status, 200, JSON.stringify(click.data));
  const human = overlay(await request('/sessions/human/frame'));
  assert.equal(human.pointer.actor, 'human');
  assert.equal(human.pointer.kind, 'click');
  assert.deepEqual({ x: human.pointer.x, y: human.pointer.y }, { x: 130, y: 89 });
  assert.equal((await request('/sessions/human/input', 'POST', { type: 'text', text: '接管输入', lease_token })).status, 200);
  assert.deepEqual(overlay(await request('/sessions/human/frame')).pointer, human.pointer);
  assert.equal((await request('/sessions/human/control', 'POST', { mode: 'agent', lease_token })).status, 200);
  assert.equal((await request('/sessions/human/state')).data.lease_token, undefined);
});

test('confirm stays pending; takeover plus agent cancel preserves dialog for explicit human acceptance', async () => {
  await open('dialog');
  const before = mutations;
  const result = await execute('dialog', { action: 'click', target: '#confirm' });
  assert.equal(result.data.ok, false);
  assert.match(result.data.error, /human/);
  assert.equal(mutations, before);
  const owner = await request('/sessions/dialog/control', 'POST', { mode: 'human' });
  assert.equal(owner.status, 200, JSON.stringify(owner.data));
  assert.equal(owner.data.dialog.type, 'confirm');
  const cancelled = await request('/sessions/dialog/cancel', 'POST');
  assert.equal(cancelled.status, 200, JSON.stringify(cancelled.data));
  assert.equal(cancelled.data.dialog.message, 'Approve this change?');
  const handled = await request('/sessions/dialog/input', 'POST', { type: 'dialog', accept: true, lease_token: owner.data.lease_token });
  assert.equal(handled.status, 200, JSON.stringify(handled.data));
  assert.equal(mutations, before + 1);
  assert.equal(handled.data.dialog, undefined);
});

test('human Enter form navigation repeatedly submits once without replaying on observation races', async () => {
  await open('keyboard-nav');
  const owner = await request('/sessions/keyboard-nav/control', 'POST', { mode: 'human' });
  assert.equal(owner.status, 200, JSON.stringify(owner.data));
  const lease_token = owner.data.lease_token;
  for (let i = 0; i < 10; i++) {
    if (i) {
      const navigation = await request('/sessions/keyboard-nav/input', 'POST', { type: 'navigate', url: origin + '/fixture', lease_token });
      assert.equal(navigation.status, 200, `iteration ${i} navigate: ${JSON.stringify(navigation.data)}`);
    }
    const focused = await request('/sessions/keyboard-nav/input', 'POST', { type: 'key', key: 'Tab', lease_token });
    assert.equal(focused.status, 200, `iteration ${i} Tab: ${JSON.stringify(focused.data)}`);
    const typed = await request('/sessions/keyboard-nav/input', 'POST', { type: 'text', text: `测试${i}`, lease_token });
    assert.equal(typed.status, 200, `iteration ${i} text: ${JSON.stringify(typed.data)}`);
    const before = submissions;
    const result = await request('/sessions/keyboard-nav/input', 'POST', { type: 'key', key: 'Enter', lease_token });
    assert.equal(result.status, 200, `iteration ${i} Enter: ${JSON.stringify(result.data)}`);
    // Receiving the POST is not a navigation commit. Human input deliberately
    // returns before background navigation settles; observe the destination
    // before the next iteration's goto can race the previous form submission.
    const presented = state => state.url === origin + '/login' && state.document_version > typed.data.document_version && !state.busy;
    const deadline = Date.now() + 50 * 20;
    let state = result.data;
    while ((submissions !== before + 1 || !presented(state)) && Date.now() < deadline) {
      await delay(20);
      const observation = await request('/sessions/keyboard-nav/state');
      assert.equal(observation.status, 200, `iteration ${i} state: ${JSON.stringify(observation.data)}`);
      state = observation.data;
    }
    assert.equal(submissions, before + 1);
    assert.ok(presented(state), `iteration ${i} submitted page did not commit: ${JSON.stringify(state)}`);
  }
});

test('async site layout changes invalidate screenshot coordinate clicks', async () => {
  await open('moving', '/moving');
  const shot = await execute('moving', { action: 'screenshot' });
  assert.equal(shot.data.ok, true, JSON.stringify(shot.data));
  await delay(700);
  const before = mutations;
  const stale = await execute('moving', { action: 'click', x: 720, y: 310, version: shot.data.version });
  assert.equal(stale.data.ok, false);
  assert.match(stale.data.error, /stale screenshot/);
  assert.equal(mutations, before);
});

test('retired eval in a batch is rejected before any side effects and deletion is idempotent', async () => {
  await open('unsafe');
  const before = mutations;
  const reply = await execute('unsafe', { action: 'batch', actions: [{ action: 'click', target: '#count' }, { action: 'eval', expression: 'while(true){}' }] });
  assert.equal(reply.data.ok, false);
  assert.match(reply.data.error, /retired/);
  assert.equal(mutations, before);
  assert.equal((await request('/sessions/unsafe', 'DELETE')).status, 200);
  assert.equal((await request('/sessions/unsafe', 'DELETE')).status, 200);
  assert.equal((await request('/sessions/unsafe/cancel', 'POST')).status, 200);
  assert.equal((await request('/sessions/unsafe/state')).status, 404);
});
