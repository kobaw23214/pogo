import { after, before, test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';
import { FAVICON_BYTES, safeFaviconSource } from '../src/favicon.js';

const token = 'favicon-controlled-test-token';
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==', 'base64');
const svg = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16" fill="red"/></svg>');
let worker, site, base, origin, serial = 0;
const counts = new Map(), held = new Map();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn) {
  const end = Date.now() + 5000;
  do { const value = await fn(); if (value) return value; await delay(25); } while (Date.now() < end);
  assert.fail('favicon state did not settle');
}
async function request(path, method = 'GET', body) {
  const result = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  return { status: result.status, data: await result.json() };
}
const execute = (id, action) => request(`/sessions/${id}/execute`, 'POST', { op_id: `favicon-${++serial}`, ...action });
const state = async id => (await request(`/sessions/${id}/state`)).data;
async function navigate(id, path) {
  assert.equal((await execute(id, { action: 'navigate', url: origin + path })).data.ok, true);
}
before(async () => {
  site = http.createServer((req, res) => {
    const path = new URL(req.url, 'http://fixture.invalid').pathname;
    counts.set(path, (counts.get(path) ?? 0) + 1);
    if (path === '/assets/one.svg') { res.setHeader('Content-Type', 'image/svg+xml'); res.setHeader('Cache-Control', 'max-age=3600'); res.end(svg); return; }
    if (path === '/two.png') { res.setHeader('Content-Type', 'image/png'); res.setHeader('Cache-Control', 'max-age=3600'); res.end(png); return; }
    if (path === '/private.svg') {
      if (!/favicon-session=(?:private|other)/.test(req.headers.cookie ?? '')) { res.writeHead(401); res.end('unauthorized'); return; }
      res.setHeader('Content-Type', 'image/svg+xml'); res.setHeader('Cache-Control', 'no-store');
      res.end(req.headers.cookie.includes('favicon-session=other') ? Buffer.from(svg.toString().replace('red', 'blue')) : svg); return;
    }
    if (path === '/huge.svg') { res.setHeader('Content-Type', 'image/svg+xml'); res.write('<svg>'); res.end(' '.repeat(FAVICON_BYTES + 1) + '</svg>'); return; }
    if (path === '/slow.svg') { held.set(path, res); return; }
    if (path === '/favicon.ico' || path === '/missing.png') { res.writeHead(404); res.end('missing'); return; }
    res.setHeader('Content-Type', 'text/html');
    if (path === '/relative') res.end('<base href="/assets/"><link rel="shortcut icon" href="one.svg"><h1>Relative icon</h1>');
    else if (path === '/cookie') { res.setHeader('Set-Cookie', 'favicon-session=private; HttpOnly; Path=/'); res.end('<link rel="icon" href="/private.svg"><h1>Cookie icon</h1>'); }
    else if (path === '/cookie-other') { res.setHeader('Set-Cookie', 'favicon-session=other; HttpOnly; Path=/'); res.end('<link rel="icon" href="/private.svg"><h1>Different cookie icon</h1>'); }
    else if (path === '/large') res.end('<link rel="icon" href="/huge.svg"><h1>Bounded icon</h1>');
    else if (path === '/slow') res.end('<link rel="icon" href="/slow.svg"><h1>Delayed icon</h1>');
    else if (path === '/none') res.end('<h1>No icon</h1>');
    else res.end('<link rel="icon" href="/two.png"><h1>PNG icon</h1>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 2000, settleMs: 0 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => {
  for (const response of held.values()) response.end('cleanup');
  await worker?.close(); site?.closeAllConnections(); if (site) await new Promise(resolve => site.close(resolve));
});

test('favicon URLs resolve metadata safely and only image data crosses the surface', () => {
  assert.equal(safeFaviconSource('../icon.png', origin + '/assets/page'), origin + '/icon.png');
  assert.equal(safeFaviconSource('/favicon.ico', 'http://user:pass@site.test/page'), '');
  assert.equal(safeFaviconSource('data:image/svg+xml,' + encodeURIComponent(svg.toString())), 'data:image/svg+xml;base64,' + svg.toString('base64'));
  for (const value of ['javascript:alert(1)', 'file:///etc/passwd', 'http://user:pass@site.test/icon.png', 'data:text/html,<h1>unsafe</h1>', 'data:image/svg+xml,<svg onload="alert(1)"/>', 'data:image/svg+xml,<svg><image href="https://remote.test/i"/></svg>', 'data:image/png;base64,' + 'A'.repeat(FAVICON_BYTES * 2)]) assert.equal(safeFaviconSource(value, origin), '');
});

test('relative and dynamic page icons arrive per tab without polling DOM or refetching unchanged metadata', async () => {
  const id = 'favicon-metadata'; await request(`/sessions/${id}`, 'PUT'); await navigate(id, '/relative');
  const first = await until(async () => { const value = await state(id); return value.favicon?.startsWith('data:image/svg+xml;base64,') && value; });
  assert.equal(first.tabs[0].favicon, first.favicon);
  for (const action of [{ action: 'snapshot' }, { action: 'find', role: 'heading' }, { action: 'extract', target: 'h1' }, { action: 'screenshot' }]) {
    const observation = await execute(id, action);
    assert.equal(observation.data.ok, true, JSON.stringify(observation.data));
    assert.equal(observation.data.url, origin + '/relative');
    assert.equal(observation.data.favicon, first.favicon, 'the citation observation keeps its actual page icon');
    assert.ok(!observation.data.text?.includes('data:image/') && !JSON.stringify(observation.data.evidence ?? {}).includes('data:image/'), 'icon bytes never enter evidence or model-facing observation text');
  }
  const discovery = await execute(id, { action: 'tabs' });
  assert.equal(discovery.data.ok, true);
  assert.equal(discovery.data.favicon, undefined, 'tab discovery is not a page observation');
  assert.ok(!discovery.data.text.includes('data:image/') && !discovery.data.text.includes('favicon'), 'icon bytes are display metadata and must not spend model context');
  const requests = counts.get('/assets/one.svg');
  for (let i = 0; i < 12; i++) await state(id);
  await delay(550); assert.equal(counts.get('/assets/one.svg'), requests, 'state polling must not issue icon requests');
  await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.querySelector("link").href="/two.png";true', returnByValue: true } });
  const changed = await until(async () => { const value = await state(id); return value.favicon?.startsWith('data:image/png;base64,') && value; });
  assert.notEqual(changed.favicon, first.favicon);
  const pngRequests = counts.get('/two.png');
  await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'history.pushState(null,"","/spa#route");true', returnByValue: true } });
  await until(async () => { const value = await state(id); return value.url.endsWith('/spa#route') && value.favicon === changed.favicon; });
  assert.equal(counts.get('/two.png'), pngRequests, 'same-document routing must reuse its page icon');
  await execute(id, { action: 'tab', tab_action: 'new', url: origin + '/relative' });
  const multi = await until(async () => { const value = await state(id); return value.tabs.length === 2 && value.tabs[1].favicon && value; });
  assert.equal(multi.tabs[0].favicon, changed.favicon); assert.equal(multi.tabs[1].favicon, first.favicon);
  assert.equal(counts.get('/assets/one.svg'), requests, 'same session favicon bytes must be cached across tabs');
  const secondObservation = (await execute(id, { action: 'snapshot' })).data;
  assert.equal(secondObservation.active_tab, multi.tabs[1].id); assert.equal(secondObservation.favicon, first.favicon);
  await execute(id, { action: 'tab', target: multi.tabs[0].id });
  const firstObservation = (await execute(id, { action: 'snapshot' })).data;
  assert.equal(firstObservation.favicon, changed.favicon);
  await execute(id, { action: 'tab', tab_action: 'close', target: multi.tabs[0].id });
  assert.equal(firstObservation.favicon, changed.favicon, 'an already captured source icon survives its tab closing');
  assert.equal((await execute(id, { action: 'snapshot' })).data.favicon, first.favicon, 'the remaining tab cannot inherit the closed tab icon');
  await request(`/sessions/${id}`, 'DELETE');
});

test('favicon resource reads use the original Chromium cookie context', async () => {
  const id = 'favicon-private'; await request(`/sessions/${id}`, 'PUT'); await navigate(id, '/cookie');
  const value = await until(async () => (await state(id)).favicon);
  assert.equal(value, 'data:image/svg+xml;base64,' + svg.toString('base64'));
  assert.ok(counts.get('/private.svg') > 0);
  await navigate(id, '/cookie-other');
  const other = 'data:image/svg+xml;base64,' + Buffer.from(svg.toString().replace('red', 'blue')).toString('base64');
  await until(async () => (await state(id)).favicon === other);
  assert.ok(counts.get('/private.svg') >= 2, 'no-store account icons must be read in the updated cookie context');
  await request(`/sessions/${id}`, 'DELETE');
});

test('failed, over-budget and new documents remove the previous page icon', async () => {
  const id = 'favicon-fallback'; await request(`/sessions/${id}`, 'PUT'); await navigate(id, '/');
  await until(async () => (await state(id)).favicon);
  await navigate(id, '/large'); await until(() => counts.get('/huge.svg'));
  await delay(250); assert.equal((await state(id)).favicon, undefined);
  await navigate(id, '/none'); await delay(150); assert.equal((await state(id)).favicon, undefined);
  await execute(id, { action: 'tab', tab_action: 'new' });
  assert.equal((await state(id)).favicon, undefined);
  await request(`/sessions/${id}`, 'DELETE');
});

test('a late icon result cannot reappear after its tab navigates', async () => {
  const id = 'favicon-late'; await request(`/sessions/${id}`, 'PUT'); await navigate(id, '/slow'); await until(() => held.has('/slow.svg'));
  await navigate(id, '/none'); const response = held.get('/slow.svg'); held.delete('/slow.svg'); response.setHeader('Content-Type', 'image/svg+xml'); response.end(svg);
  await delay(300); assert.equal((await state(id)).favicon, undefined);
  assert.ok((await state(id)).url.endsWith('/none'));
  await request(`/sessions/${id}`, 'DELETE');
});

test('missing sessions, changed contexts and closed tabs report distinct lifecycle codes', async () => {
  const id = 'lifecycle-codes';
  const missing = await request(`/sessions/${id}/state`);
  assert.equal(missing.status, 404); assert.equal(missing.data.code, 'BROWSER_SESSION_EXPIRED');
  await request(`/sessions/${id}`, 'PUT');
  const current = await state(id), target = { context_ref: current.context_ref, tab_id: current.active_tab };
  const wrong = await request(`/sessions/${id}/state?context_ref=ctx_000000000000000000000000&tab_id=${target.tab_id}`);
  assert.equal(wrong.status, 409); assert.equal(wrong.data.code, 'BROWSER_CONTEXT_CHANGED');
  const control = await request(`/sessions/${id}/control`, 'POST', { ...target, mode: 'human' });
  assert.equal(control.status, 200);
  const closed = await request(`/sessions/${id}/tabs`, 'POST', { ...target, action: 'close', op_id: 'lifecycle-close', lease_token: control.data.lease_token });
  assert.equal(closed.status, 200);
  const gone = await request(`/sessions/${id}/state?context_ref=${target.context_ref}&tab_id=${target.tab_id}`);
  assert.equal(gone.status, 410); assert.equal(gone.data.code, 'BROWSER_TAB_CLOSED');
  await request(`/sessions/${id}`, 'DELETE');
});
