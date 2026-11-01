import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';

// Regression: dropping/closing a debugging session can resume Chromium before
// its owner is destroyed. Cancellation must terminate the paused continuation.
let site, worker, browser, origin, base, sequence = 0;
const saves = new Map();
const token = 'cancel-regression-test-only';
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function request(path, method = 'GET', body) {
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  return { status: response.status, data: await response.json() };
}
const execute = (id, args) => request(`/sessions/${id}/execute`, 'POST', { op_id: `cancel-regression-${++sequence}`, ...args });
function success(response) {
  assert.equal(response.status, 200, JSON.stringify(response.data));
  assert.equal(response.data.ok, true, JSON.stringify(response.data));
  return response.data.data;
}
async function until(read, accept) {
  const end = Date.now() + 5000;
  while (Date.now() < end) { const value = await read(); if (accept(value)) return value; await delay(15); }
  assert.fail('Timed out waiting for paused cancellation fixture');
}

before(async () => {
  site = createServer((req, res) => {
    const url = new URL(req.url, 'http://fixture.invalid');
    const id = url.searchParams.get('case');
    const queued = id?.includes('queued') ? `setTimeout(() => fetch('/save?case=' + ${JSON.stringify(id)} + '&queued=1',{method:'POST'}).catch(() => {}),0);` : '';
    if (url.pathname === '/save') {
      saves.set(id, (saves.get(id) ?? 0) + 1);
      res.writeHead(201, { 'Content-Type': 'application/json' }); res.end('{"saved":true}'); return;
    }
    if (url.pathname === '/worker.js') {
      res.writeHead(200, { 'Content-Type': 'application/javascript' });
      res.end(`onmessage = async () => { const localRecord = {caseId:${JSON.stringify(id)}}; ${queued} debugger; await fetch('/save?case=' + localRecord.caseId,{method:'POST'}); postMessage({saved:true}); };`); return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html' });
    res.end(`<!doctype html><title>Cancellation boundary</title><p>Cancellation fixture</p><script>
      const background = new Worker('/worker.js?case=' + ${JSON.stringify(id)});
      async function saveOnPage() { const localRecord = {caseId:${JSON.stringify(id)}}; ${queued} debugger; await fetch('/save?case=' + localRecord.caseId,{method:'POST'}); }
    </script>${url.pathname !== '/child' && id?.includes('iframe') ? `<iframe src="${origin.replace('127.0.0.1', 'localhost')}/child?case=${id}"></iframe>` : ''}`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
  worker = await createWorker({ token, browser, actionTimeout: 2000, settleMs: 20 });
  const address = await worker.listen(0);
  base = `http://127.0.0.1:${address.port}`;
});
after(async () => { await worker?.close(); await browser?.close(); await new Promise(resolve => site.close(resolve)); });

for (const scenario of [
  { id: 'idle-worker-cancel', type: 'worker', active: false, destroy: false },
  { id: 'active-worker-cancel', type: 'worker', active: true, destroy: false },
  { id: 'idle-worker-delete', type: 'worker', active: false, destroy: true },
  { id: 'idle-page-cancel', type: 'page', active: false, destroy: false },
  { id: 'idle-page-delete', type: 'page', active: false, destroy: true },
  { id: 'queued-worker-cancel', type: 'worker', active: false, destroy: false },
  { id: 'queued-worker-delete', type: 'worker', active: false, destroy: true },
  { id: 'queued-iframe-cancel', type: 'iframe', active: false, destroy: false },
  { id: 'queued-iframe-delete', type: 'iframe', active: false, destroy: true },
]) test(`${scenario.id} cannot release a paused business save`, { timeout: 10000 }, async () => {
  const { id } = scenario;
  let deleted = false;
  saves.set(id, 0);
  success(await request(`/sessions/${id}`, 'PUT'));
  try {
    success(await execute(id, { action: 'navigate', url: `${origin}/?case=${id}` }));
    const targets = await until(async () => success(await execute(id, { action: 'devtools', method: 'targets.list' })).items,
      items => items.some(item => item.type === scenario.type && item.ready));
    const target = targets.find(item => item.type === scenario.type && item.ready);
    success(await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.cookie="cancel_cookie=preserved; path=/"; true', returnByValue: true } }));
    success(await execute(id, { action: 'devtools', method: 'Debugger.enable', target: target.target_ref }));
    if (scenario.active) {
      const pending = await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'new Promise(resolve => { background.addEventListener("message", event => resolve(event.data)); background.postMessage({start:true}); })', awaitPromise: true, returnByValue: true } });
      assert.equal(pending.status, 200);
      assert.equal(pending.data.ok, false);
      assert.match(pending.data.error, /debugger.*pending/i);
    } else {
      // The scheduled task starts after its dispatching evaluation has ended,
      // exercising the missing rawAction case rather than only a busy action.
      success(await execute(id, { action: 'devtools', method: 'Runtime.evaluate', ...(scenario.type === 'iframe' ? { target: target.target_ref } : {}), params: {
        expression: scenario.type === 'worker' ? 'setTimeout(() => background.postMessage({start:true}), 80); true' : 'setTimeout(() => saveOnPage(), 80); true', returnByValue: true,
      } }));
    }
    const before = await until(async () => (await request(`/sessions/${id}/state`)).data,
      state => state.debugger_paused && state.busy === scenario.active);
    assert.equal(saves.get(id), 0);
    if (scenario.destroy) {
      success(await request(`/sessions/${id}`, 'DELETE')); deleted = true;
    } else {
      const cancelled = await request(`/sessions/${id}/cancel`, 'POST', {});
      success(cancelled);
      assert.equal(cancelled.data.debugger_paused, false);
      assert.notEqual(cancelled.data.active_tab, before.active_tab, 'paused owner must be replaced');
      assert.equal(cancelled.data.url, 'about:blank');
      success(await execute(id, { action: 'snapshot' }));
      success(await execute(id, { action: 'navigate', url: `${origin}/?case=restored-${id}` }));
      // Successful navigation proves offline isolation was lifted; cookies
      // must survive while document/target references are replaced.
      const observedCookie = success(await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.cookie.includes("cancel_cookie=preserved")', returnByValue: true } }));
      assert.equal(observedCookie.result.result.value, true);
    }
    await delay(100);
    assert.equal(saves.get(id), 0, 'cancellation must never run the paused save before destroying its owner');
  } finally { if (!deleted) await request(`/sessions/${id}`, 'DELETE'); }
});
