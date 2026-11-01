import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createHash } from 'node:crypto';
import { createWorker } from '../src/server.js';
import { BrowserSession } from '../src/session.js';

let worker, site, base, origin, serial = 0, authenticatedB = 0;
const token = 'tab-binding-controlled-token', delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function request(path, method = 'GET', body) {
  const res = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  return { status: res.status, headers: res.headers, data: res.headers.get('content-type')?.includes('json') ? await res.json() : Buffer.from(await res.arrayBuffer()) };
}
const execute = (id, body) => request(`/sessions/${id}/execute`, 'POST', { op_id: `agent-${++serial}`, ...body });
const state = async (id, tab, context) => (await request(`/sessions/${id}/state${tab ? `?tab_id=${tab}&context_ref=${context}` : ''}`)).data;
const tabs = (id, context, action, extra = {}) => request(`/sessions/${id}/tabs`, 'POST', { op_id: `ui-${++serial}`, context_ref: context, action, ...extra });
async function until(fn) { const end = Date.now() + 4000; do { const value = await fn(); if (value) return value; await delay(20); } while (Date.now() < end); assert.fail('expected browser state did not arrive'); }
async function frame(id, tab, context) {
  return until(async () => { const res = await request(`/sessions/${id}/frame?tab_id=${tab}&context_ref=${context}`); assert.equal(res.status, 200, JSON.stringify(res.data)); return res.headers.get('x-browser-version') ? res : null; });
}
before(async () => {
  site = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://fixture.invalid'), name = url.pathname === '/a' ? 'A' : url.pathname === '/b' ? 'B' : 'Popup';
    if (name === 'A') res.setHeader('Set-Cookie', 'sheet_login=shared; HttpOnly; SameSite=Lax; Path=/');
    if (name === 'B' && req.headers.cookie?.includes('sheet_login=shared')) authenticatedB++;
    res.setHeader('Content-Type', 'text/html');
    res.end(`<title>Sheet ${name}</title><body style="margin:0;background:${name === 'A' ? '#ffd0d0' : '#c8e0ff'}"><button style="width:180px;height:60px">Button ${name}</button><input id="entry" style="position:absolute;left:0;top:80px;width:180px;height:40px"><p id="later"></p>${url.searchParams.has('popup') ? '<script>setTimeout(()=>window.open("/popup"),900)</script>' : ''}`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve)); origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 2500, settleMs: 0 }); base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => { await worker?.close(); site?.closeAllConnections(); if (site) await new Promise(resolve => site.close(resolve)); });

test('background tabs share context cookies, but bound state/frame/pick never change the Agent target', async () => {
  const id = 'two-sheets'; await request(`/sessions/${id}`, 'PUT');
  const original = await state(id), context = original.context_ref, a = original.active_tab;
  assert.match(context, /^ctx_[a-f0-9]{24}$/);
  await execute(id, { action: 'navigate', url: origin + '/a' });
  const created = await tabs(id, context, 'new'), b = created.data.tab_id;
  assert.equal(created.status, 200); assert.equal(created.data.state.viewed_tab, b);
  assert.equal(created.data.state.active_tab, a); assert.equal(created.data.state.url, 'about:blank');
  const human = { ...created.data.state, lease_token: created.data.lease_token };
  assert.equal(human.context_ref, context);
  assert.equal(human.control, 'human', 'UI new Sheet starts with human ownership');
  const target = { target_tab: b, context_ref: context, generation: human.generation, lease_token: human.lease_token };
  const navigated = await request(`/sessions/${id}/input`, 'POST', { ...target, type: 'navigate', url: origin + '/b' });
  assert.equal(navigated.status, 200); assert.equal(navigated.data.viewed_tab, b); assert.equal(navigated.data.title, 'Sheet B');
  assert.ok(authenticatedB > 0, 'both pages use the same BrowserContext cookie jar');
  const bframe = await frame(id, b, context);
  assert.equal(bframe.headers.get('x-browser-context-ref'), context); assert.equal(bframe.headers.get('x-browser-viewed-tab'), b);
  await request(`/sessions/${id}/input`, 'POST', { ...target, type: 'click', x: 60, y: 100, version: bframe.headers.get('x-browser-version') });
  await request(`/sessions/${id}/input`, 'POST', { ...target, type: 'text', text: 'B only' });
  await request(`/sessions/${id}/control`, 'POST', { mode: 'agent', context_ref: context, tab_id: b, lease_token: human.lease_token });
  await execute(id, { action: 'tab', target: a });
  const aValue = await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.getElementById("entry").value', returnByValue: true } });
  assert.equal(aValue.data.data.result.result.value, '', 'targeted human input did not type in A');
  const aFrame = await frame(id, a, context), observedB = await state(id, b, context), freshB = await frame(id, b, context);
  assert.equal(observedB.viewed_tab, b); assert.equal(observedB.active_tab, a); assert.equal(observedB.title, 'Sheet B');
  assert.equal(observedB.activity, undefined);
  assert.notEqual(createHash('sha256').update(aFrame.data).digest('hex'), createHash('sha256').update(freshB.data).digest('hex'));
  await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'setTimeout(()=>document.getElementById("later").textContent="ready",900);true', returnByValue: true } });
  const waiting = execute(id, { action: 'wait', text: 'ready', seconds: 2 });
  await until(async () => (await state(id)).busy);
  const picked = await request(`/sessions/${id}/pick`, 'POST', { context_ref: context, tab_id: b, version: freshB.headers.get('x-browser-version'), x: 50, y: 30 });
  assert.equal(picked.status, 200, JSON.stringify(picked.data)); assert.equal(picked.data.element.name, 'Button B'); assert.equal(picked.data.context_ref, context);
  assert.equal((await state(id)).active_tab, a); await waiting;
  await request(`/sessions/${id}`, 'DELETE');
});

test('tab lifecycle is idempotent, supports zero pages, and context renewal never reuses a saved binding', async () => {
  const id = 'lifecycle'; await request(`/sessions/${id}`, 'PUT'); const initial = await state(id), context = initial.context_ref;
  const body = { op_id: 'new-once', action: 'new', context_ref: context };
  const created = await request(`/sessions/${id}/tabs`, 'POST', body);
  assert.deepEqual((await request(`/sessions/${id}/tabs`, 'POST', body)).data, created.data);
  assert.equal((await state(id)).tabs.length, 2);
  assert.equal((await request(`/sessions/${id}/tabs`, 'POST', { ...body, action: 'close', tab_id: created.data.tab_id })).data.code, 'TAB_OPERATION_ID_CONFLICT');
  assert.equal((await tabs(id, context, 'close', { tab_id: created.data.tab_id })).status, 403);
  for (const tab of (await state(id)).tabs) {
    const human = tab.id === created.data.tab_id ? { lease_token: created.data.lease_token }
      : (await request(`/sessions/${id}/control`, 'POST', { mode: 'human', context_ref: context, tab_id: tab.id })).data;
    assert.equal((await tabs(id, context, 'close', { tab_id: tab.id, lease_token: human.lease_token })).status, 200);
  }
  const empty = await state(id); assert.equal(empty.active_tab, ''); assert.equal(empty.viewed_tab, ''); assert.equal(empty.context_ref, context);
  const replacement = await tabs(id, context, 'new'); const tab = replacement.data.tab_id;
  assert.equal(replacement.data.state.active_tab, ''); assert.equal(replacement.data.state.viewed_tab, tab);
  assert.equal((await frame(id, tab, context)).headers.get('x-browser-context-ref'), context);
  const replacementOwner = { ...replacement.data.state, lease_token: replacement.data.lease_token };
  assert.equal((await request(`/sessions/${id}/input`, 'POST', { type: 'navigate', target_tab: tab, context_ref: context, generation: empty.generation, lease_token: replacementOwner.lease_token, url: origin + '/b' })).status, 200);
  assert.equal((await state(id)).active_tab, '', 'human input does not select an Agent tab');
  assert.equal((await request(`/sessions/${id}/state?tab_id=${initial.active_tab}&context_ref=${context}`)).status, 410);
  await request(`/sessions/${id}`, 'DELETE'); await request(`/sessions/${id}`, 'PUT');
  const renewed = await state(id); assert.notEqual(renewed.context_ref, context); assert.equal(renewed.active_tab, initial.active_tab);
  assert.equal((await request(`/sessions/${id}/state?tab_id=${initial.active_tab}&context_ref=${context}`)).status, 409);
  assert.equal((await request(`/sessions/${id}/control`, 'POST', { mode: 'human', context_ref: context, tab_id: initial.active_tab })).status, 409);
  assert.equal((await state(id)).control, 'agent'); assert.equal((await state(id)).generation, renewed.generation);
  await request(`/sessions/${id}`, 'DELETE');
});

test('a background Sheet popup is registered without stealing the active Agent page', async () => {
  const id = 'background-popup'; await request(`/sessions/${id}`, 'PUT'); const first = await state(id), context = first.context_ref, a = first.active_tab;
  await execute(id, { action: 'navigate', url: origin + '/a' });
  const created = (await tabs(id, context, 'new')).data, b = created.tab_id;
  const human = { ...created.state, lease_token: created.lease_token };
  await request(`/sessions/${id}/input`, 'POST', { type: 'navigate', target_tab: b, context_ref: context, generation: human.generation, lease_token: human.lease_token, url: origin + '/b?popup=1' });
  await request(`/sessions/${id}/control`, 'POST', { mode: 'agent', context_ref: context, tab_id: b, lease_token: human.lease_token });
  await execute(id, { action: 'tab', target: a });
  await until(async () => (await state(id)).tabs.length === 3);
  assert.equal((await state(id)).active_tab, a);
  await request(`/sessions/${id}`, 'DELETE');
});

function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; }
function fakeSession() {
  const session = new BrowserSession({ setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {} });
  const meta = { id: 't1', version: 1, visual: 0, page: { isClosed: () => false, url: () => 'about:blank' } };
  session.pages.set(meta.id, meta); session.active = meta.id;
  session.control = 'human'; session.lease = 'owned-lease'; session.devtools.attach = async () => {};
  return { session, meta };
}
for (const type of ['key', 'text', 'scroll']) test(`queued targeted ${type} rechecks generation before dispatch`, async () => {
  const { session } = fakeSession(), gate = deferred(); let calls = 0;
  session.queue = gate.promise; session.run = async () => { calls++; return { ok: true }; };
  const input = session.input({ type, key: 'Enter', text: 'no write', deltaY: 10, lease_token: session.lease, target_tab: 't1', context_ref: session.contextRef, generation: session.generation });
  session.generation++; gate.resolve();
  await assert.rejects(input, error => error.status === 409); assert.equal(calls, 0);
});

test('pending/unknown tab creation keeps its op_id, while confirmed rejections replay the same error', async () => {
  const { session } = fakeSession(), gate = deferred(); session.control = 'agent'; session.queue = gate.promise;
  const body = { op_id: 'same-id', action: 'new', context_ref: session.contextRef };
  const creating = session.manageTabs(body);
  await assert.rejects(session.manageTabs(body), error => error.code === 'TAB_OPERATION_PENDING');
  session.generation++; gate.resolve();
  await assert.rejects(creating, error => error.code === 'TAB_OPERATION_REJECTED');
  await assert.rejects(session.manageTabs(body), error => error.code === 'TAB_OPERATION_REJECTED');
  session.context.newPage = async () => { throw new Error('transport response lost after dispatch'); };
  const uncertain = { ...body, op_id: 'unknown-id' };
  await assert.rejects(session.manageTabs(uncertain), error => error.code === 'TAB_OPERATION_OUTCOME_UNKNOWN');
  await assert.rejects(session.manageTabs(uncertain), error => error.code === 'TAB_OPERATION_OUTCOME_UNKNOWN');
  session.maxPages = 1;
  const capped = { ...body, op_id: 'capacity-id' };
  await assert.rejects(session.manageTabs(capped), error => error.status === 429 && error.code === 'TAB_OPERATION_REJECTED');
  // The default is no cap: past sixteen pages the request fails only at
  // transport, never at a capacity check.
  session.maxPages = 0;
  for (let i = 0; i < 20; i++) session.pages.set(`x${i}`, { id: `x${i}` });
  const uncapped = { ...body, op_id: 'uncapped-id' };
  await assert.rejects(session.manageTabs(uncapped), error => error.status !== 429 && error.code === 'TAB_OPERATION_OUTCOME_UNKNOWN');
});

test('a bound frame may finish after the Agent changes target, but never after its own tab closes', async () => {
  const { session, meta: a } = fakeSession();
  const b = { ...a, id: 't2', page: { isClosed: () => false, url: () => 'https://fixture.invalid/b', frames: () => [] } };
  session.pages.set(b.id, b);
  session.refreshRevision = async (_observation, meta) => session.version(meta);
  const pending = deferred(); session.devtools.capturePixels = () => pending.promise;
  const bound = { tab_id: b.id, context_ref: session.contextRef };
  const capturing = session.frame(bound);
  await delay(0);
  session.active = null;
  pending.resolve(Buffer.from([0xff, 0xd8, 0xff]));
  const result = await capturing;
  assert.equal(result.viewed_tab, b.id); assert.equal(result.context_ref, session.contextRef);
  assert.equal(session.active, null); assert.equal(result.overlay, undefined);
  const closing = deferred(); session.devtools.capturePixels = () => closing.promise;
  const stale = session.frame(bound); await delay(0);
  session.pages.delete(b.id); closing.resolve(Buffer.from([0xff, 0xd8, 0xff]));
  await assert.rejects(stale, error => error.status === 409);
});

test('explicit close interrupts a loading page without waiting for its queued navigation', async () => {
  const { session, meta } = fakeSession(), gate = deferred(); let closes = 0, closed = false;
  const oldLease = session.lease;
  session.lease = 'replacement-lease';
  meta.pageLoading = true;
  meta.page.isClosed = () => closed;
  session.closePage = async page => {
    assert.equal(page, meta.page);
    assert.equal(meta.controlPhase, 'closing');
    closes++; closed = true; session.pages.delete(meta.id); session.active = null;
  };
  await assert.rejects(session.manageTabs({ op_id: 'stale-close', action: 'close', tab_id: meta.id, context_ref: session.contextRef, lease_token: oldLease }),
    error => error.status === 403 && error.code === 'TAB_OPERATION_REJECTED');
  assert.equal(closes, 0, 'a stale lease never acquires close authority');
  session.queue = gate.promise;
  session.rawAction = gate.promise;
  const body = { op_id: 'loading-close', action: 'close', tab_id: meta.id, context_ref: session.contextRef, lease_token: session.lease };
  const closing = session.manageTabs(body);
  try {
    assert.equal(await Promise.race([closing.then(() => true), delay(100).then(() => false)]), true, 'close must not await navigation');
    assert.equal((await closing).state.tabs.length, 0);
    await session.manageTabs(body);
    assert.equal(closes, 1, 'retrying the same operation never closes twice');
  } finally { gate.resolve(); }
});

test('direct Agent-page closure requires fresh ownership and preserves a sibling page', async () => {
  const { session, meta } = fakeSession();
  session.control = 'agent'; session.lease = null;
  const generation = session.generation, controlEpoch = session.controlFor(meta).epoch;
  const sibling = { ...meta, id: 't2', control: 'human', lease: 'sibling-lease', page: { isClosed: () => false, url: () => 'about:blank' } };
  session.pages.set(sibling.id, sibling);
  meta.pageLoading = true;
  let closed = false;
  meta.page.isClosed = () => closed;
  session.closePage = async page => {
    assert.equal(page, meta.page); closed = true; session.pages.delete(meta.id); session.active = null;
  };
  const body = { op_id: 'agent-close', action: 'close', tab_id: meta.id, context_ref: session.contextRef, generation, control_epoch: controlEpoch };
  await assert.rejects(session.manageTabs({ ...body, op_id: 'stale-epoch-close', control_epoch: controlEpoch + 1 }), error => error.code === 'TAB_OPERATION_REJECTED');
  assert.equal(closed, false);
  const result = await session.manageTabs(body);
  assert.deepEqual(result.state.tabs.map(tab => tab.id), ['t2']);
  assert.equal(session.generation, generation);
  assert.equal(sibling.lease, 'sibling-lease');
  assert.equal(sibling.control, 'human');
});
