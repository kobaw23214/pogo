import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';
import { createNativeDiagnostics } from './helpers/native-diagnostics.js';

let worker, site, base, origin, partner, serial = 0;
const held = new Map(), token = 'page-loading-controlled-token';
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn, message = 'condition did not arrive') {
  const end = Date.now() + 4000;
  do { const value = await fn(); if (value) return value; await delay(15); } while (Date.now() < end);
  assert.fail(message);
}
async function request(path, method = 'GET', body) {
  // These single-page fixtures bind ownership to the observed tab explicitly.
  if (path.endsWith('/control') && body) {
    const state = (await request(path.slice(0, -8) + '/state')).data;
    body = { tab_id: state.active_tab, context_ref: state.context_ref, ...body };
  }
  const res = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const data = res.headers.get('content-type')?.includes('json') ? await res.json() : Buffer.from(await res.arrayBuffer());
  return { status: res.status, data, headers: res.headers };
}
const execute = (id, action) => request(`/sessions/${id}/execute`, 'POST', { op_id: `loading-${++serial}`, ...action });
const state = async id => (await request(`/sessions/${id}/state`)).data;
async function open(id) {
  assert.equal((await request(`/sessions/${id}`, 'PUT')).status, 200);
  assert.equal((await execute(id, { action: 'navigate', url: origin + '/' })).data.ok, true);
  await until(async () => !(await state(id)).page_loading);
}
async function release(path, content = '<h1>Loaded target</h1>') {
  const res = await until(() => held.get(path)); held.delete(path);
  res.setHeader('Content-Type', 'text/html'); res.end(content);
}
before(async () => {
  site = http.createServer((req, res) => {
    const path = new URL(req.url, 'http://fixture.invalid').pathname;
    if (path.startsWith('/hold/')) { held.set(path, res); return; }
    if (path === '/fail') { req.socket.destroy(); return; }
    if (path === '/redirect') { res.writeHead(302, { Location: '/hold/redirect' }); res.end(); return; }
    res.setHeader('Content-Type', 'text/html');
    if (path === '/child') { res.end('<p>Child frame</p><script>onmessage=()=>{console.log("controlled-child-busy");const until=Date.now()+6000;while(Date.now()<until){}}</script>'); return; }
    res.end(`<h1>Loading fixture</h1><a id="link" href="/hold/click">Slow link</a><a id="redirect" href="/redirect">Redirect</a><form action="/hold/form"><input id="query" name="q"><button>Submit</button></form><button id="fetch" onclick="fetch('/hold/fetch')">Fetch</button><button id="iframe" onclick="document.querySelector('iframe').src='/hold/iframe'">Change iframe</button><iframe src="${partner}/child"></iframe>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`; partner = `http://localhost:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 1800, settleMs: 0 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => {
  for (const res of held.values()) res.end('cleanup'); held.clear();
  await worker?.close(); site?.closeAllConnections(); if (site) await new Promise(resolve => site.close(resolve));
});

test('agent and human top-level navigation expose actual loading until their document load completes', async () => {
  for (const actor of ['agent', 'human']) {
    const id = `load-${actor}`; await open(id); let lease;
    if (actor === 'human') lease = (await request(`/sessions/${id}/control`, 'POST', { mode: 'human' })).data.lease_token;
    const path = `/hold/${actor}`;
    const operation = actor === 'agent' ? execute(id, { action: 'navigate', url: origin + path })
      : request(`/sessions/${id}/input`, 'POST', { type: 'navigate', url: origin + path, lease_token: lease });
    await until(() => held.has(path)); await until(async () => (await state(id)).page_loading);
    await release(path); assert.equal((await operation).status, 200);
    await until(async () => !(await state(id)).page_loading);
    assert.equal((await state(id)).page_load_error, undefined);
    await request(`/sessions/${id}`, 'DELETE');
  }
});

test('click, form Enter, and redirects use the same top-level loader lifecycle', async () => {
  for (const kind of ['click', 'form', 'redirect']) {
    const id = `action-${kind}`; await open(id);
    const action = kind === 'form' ? { action: 'press', target: '#query', key: 'Enter' }
      : { action: 'click', target: kind === 'redirect' ? '#redirect' : '#link' };
    const operation = execute(id, action), path = `/hold/${kind}`;
    await until(() => held.has(path)); await until(async () => (await state(id)).page_loading);
    await release(path); assert.equal((await operation).data.ok, true);
    await until(async () => !(await state(id)).page_loading);
    assert.equal((await state(id)).page_load_error, undefined);
    await request(`/sessions/${id}`, 'DELETE');
  }
});

test('iframe navigation, background fetch, and Agent observation do not start the page spinner', async () => {
  const id = 'non-page-work'; await open(id);
  assert.equal((await execute(id, { action: 'click', target: '#fetch' })).data.ok, true);
  await until(() => held.has('/hold/fetch')); assert.equal((await state(id)).page_loading, false);
  const identity = (await state(id)).frame_identity;
  const changing = execute(id, { action: 'click', target: '#iframe' });
  await until(() => held.has('/hold/iframe')); assert.equal((await state(id)).page_loading, false);
  await release('/hold/iframe', '<p>New child</p>'); await changing;
  assert.equal((await state(id)).frame_identity, identity, 'an iframe does not change top-document identity');
  const observation = execute(id, { action: 'snapshot' });
  assert.equal((await state(id)).page_loading, false);
  await observation; await release('/hold/fetch', 'ordinary fetch');
  assert.equal((await state(id)).page_loading, false);
  await request(`/sessions/${id}`, 'DELETE');
});

test('switching tabs exposes the selected tab loading state; failed and cancelled navigation finish cleanly', async () => {
  const id = 'switch-and-fail'; await open(id); const first = (await state(id)).active_tab;
  const nativeDiagnostic = createNativeDiagnostics(worker.runtime.hosts.get(id), { origin, paths: ['/', '/fail', '/hold/background', '/hold/cancel'] });
  await execute(id, { action: 'tab', tab_action: 'new' }); const second = (await state(id)).active_tab;
  const navigation = execute(id, { action: 'navigate', url: origin + '/hold/background' });
  await until(() => held.has('/hold/background')); await until(async () => (await state(id)).page_loading);
  // Explicit human takeover waits for the same operation, so first let its
  // navigation timeout; it does not terminate the still-loading document.
  assert.equal((await navigation).data.ok, false);
  assert.equal((await execute(id, { action: 'tab', target: first })).data.ok, true);
  assert.equal((await state(id)).page_loading, false);
  await execute(id, { action: 'tab', target: second });
  assert.equal((await state(id)).page_loading, true);
  await request(`/sessions/${id}/cancel`, 'POST');
  // This idle cancellation intentionally preserves an already timed-out
  // navigation. Complete it, then verify an active cancellation separately.
  await release('/hold/background');
  await until(async () => !(await state(id)).page_loading);
  const failing = await execute(id, { action: 'navigate', url: origin + '/fail' });
  assert.equal(failing.data.ok, false);
  await until(async () => !(await state(id)).page_loading);
  assert.equal((await state(id)).page_load_error, 'Page navigation failed.');
  await execute(id, { action: 'navigate', url: origin + '/' });
  assert.equal((await state(id)).page_load_error, undefined);
  const active = execute(id, { action: 'navigate', url: origin + '/hold/cancel' });
  await until(() => held.has('/hold/cancel')); await until(async () => (await state(id)).page_loading);
  const cancelling = request(`/sessions/${id}/cancel`, 'POST');
  // Let the controlled HTTP response settle while cancellation fences the
  // action. The separate cancellation safety suite covers withheld/failed
  // network-isolation acknowledgements; do not weaken that product boundary.
  await delay(80); await release('/hold/cancel');
  const cancelled = await cancelling;
  const cancellationEvidence = { response: { status: cancelled.status, ok: cancelled.data.ok, code: cancelled.data.code ?? null,
    error: typeof cancelled.data.error === 'string' ? cancelled.data.error.replaceAll(token, '[redacted]').replace(/(?:https?:\/\/|data:)[^\s"'<>]+/g, '[url]') : null }, native: nativeDiagnostic.snapshot() };
  assert.equal(cancelled.status, 200, JSON.stringify(cancellationEvidence));
  assert.equal(cancelled.data.ok, true, JSON.stringify(cancellationEvidence));
  await active;
  assert.equal((await state(id)).page_loading, false);
  assert.equal((await state(id)).page_load_error, undefined);
  await request(`/sessions/${id}`, 'DELETE');
});

test('busy OOPIF does not withhold first compositor pixels; response identity remains tied to top document', async () => {
  const id = 'first-pixels'; await open(id);
  await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: "document.querySelector('iframe').contentWindow.postMessage('busy','*');true", returnByValue: true } });
  await until(async () => JSON.stringify((await execute(id, { action: 'devtools', method: 'console.list', params: { limit: 10 } })).data).includes('controlled-child-busy'));
  const before = await state(id), started = Date.now();
  const frame = await request(`/sessions/${id}/frame`);
  assert.equal(frame.status, 200, JSON.stringify(frame.data));
  assert.ok(Date.now() - started < 1200, 'pixels arrive before the 1500ms ordinary DOM-read timeout');
  assert.equal(frame.data[0], 0xff); assert.equal(frame.headers.get('x-browser-version'), null);
  assert.equal(frame.headers.get('x-browser-overlay'), null);
  assert.equal(frame.headers.get('x-browser-frame-identity'), before.frame_identity);
  assert.equal((await state(id)).page_loading, false);
  await request(`/sessions/${id}`, 'DELETE');
});
