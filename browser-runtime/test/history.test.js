import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { BrowserSession } from '../src/session.js';
import { createWorker } from '../src/server.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label, ms = 5000) {
  const deadline = Date.now() + ms;
  do { const value = await read(); if (value) return value; await delay(20); } while (Date.now() < deadline);
  assert.fail(label);
}

test('history reads coalesce, reject stale replies, and never block ordinary browser state', async () => {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} });
  const page = { url: () => 'https://fixture.invalid/a', isClosed: () => false };
  const meta = { id: 't1', page, version: 1, visual: 1, title: '', control: 'agent', controlEpoch: 0, controlPhase: 'ready' };
  session.pages.set(meta.id, meta); session.active = meta.id;
  let reads = 0, resolve;
  session.devtools = {
    attach: async () => ({ client: { send: async method => {
      assert.equal(method, 'Page.getNavigationHistory'); reads++;
      return new Promise(done => { resolve = done; });
    } } }), isPaused: () => false, isBlocked: () => false,
  };
  for (let i = 0; i < 5; i++) assert.equal(session.state().can_go_back, undefined);
  await until(() => resolve, 'one native history read begins');
  assert.equal(reads, 1);
  meta.version++;
  resolve({ currentIndex: 1, entries: [{ id: 1 }, { id: 2 }] });
  await until(() => !meta.historyFlight, 'first read settles');
  assert.equal(meta.historyVersion, undefined, 'old document history cannot re-enable navigation');
  session.state(); await until(() => reads === 2, 'fresh document history read');
  resolve({ currentIndex: 0, entries: [{ id: 1 }] });
  await until(() => !meta.historyFlight, 'fresh history read settles');
  assert.equal(session.state().can_go_back, false);
  assert.equal(reads, 2, 'unchanged history is cached rather than queried on every poll');
});

test('native Back traverses Chromium history only on the leased tab, preserving Agent target and history boundaries', { timeout: 30000 }, async t => {
  let worker;
  const hits = new Map();
  const site = http.createServer((request, response) => {
    hits.set(request.url, (hits.get(request.url) || 0) + 1);
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
    response.end(`<!doctype html><title>History ${request.url}</title><h1>${request.url}</h1>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(async () => { await worker?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  worker = await createWorker({ token: 'history-local-fixture', settleMs: 0, actionTimeout: 3000 });
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/history-${randomUUID()}`;
  const origin = `http://127.0.0.1:${site.address().port}`;
  const request = async (suffix, method = 'GET', body) => {
    const response = await fetch(base + suffix, { method, headers: { Authorization: 'Bearer history-local-fixture', 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    return { status: response.status, body: await response.json() };
  };
  const initial = (await request('', 'PUT', {})).body;
  const target = { context_ref: initial.context_ref, target_tab: initial.active_tab, generation: initial.generation };
  const viewed = async () => (await request(`/state?context_ref=${target.context_ref}&tab_id=${target.target_tab}`)).body;
  await until(async () => (await viewed()).can_go_back === false, 'fresh blank tab has no Back history');
  const control = (await request('/control', 'POST', { mode: 'human', context_ref: target.context_ref, tab_id: target.target_tab })).body;
  const input = (body, patch = {}) => request('/input', 'POST', { ...target, lease_token: control.lease_token, ...body, ...patch });
  for (const path of ['/a', '/b']) {
    const opened = await input({ type: 'navigate', url: origin + path });
    assert.equal(opened.status, 200, JSON.stringify(opened.body));
    await until(async () => (await viewed()).url === origin + path && !(await viewed()).page_loading, 'navigation commits');
  }
  await until(async () => (await viewed()).can_go_back === true, 'B has real Chromium history');
  const sibling = await request('/execute', 'POST', { op_id: randomUUID(), action: 'tab', tab_action: 'new', url: origin + '/sibling' });
  assert.equal(sibling.status, 200); assert.equal(sibling.body.ok, true);
  assert.notEqual(sibling.body.active_tab, target.target_tab);
  for (const [patch, status] of [[{ lease_token: 'invalid' }, 403], [{ generation: target.generation + 1 }, 409], [{ context_ref: 'ctx_000000000000000000000000' }, 409], [{ target_tab: sibling.body.active_tab }, 403]]) {
    assert.equal((await input({ type: 'back' }, patch)).status, status, 'unowned or stale Back is refused before dispatch');
  }
  assert.equal((await viewed()).url, origin + '/b');
  const back = await input({ type: 'back' });
  assert.equal(back.status, 200, JSON.stringify(back.body));
  await until(async () => (await viewed()).url === origin + '/a' && !(await viewed()).page_loading, 'Back B returns to A');
  assert.equal((await viewed()).active_tab, sibling.body.active_tab, 'reading and history navigation never switch the Agent execution target');
  const siblingState = (await request(`/state?context_ref=${target.context_ref}&tab_id=${sibling.body.active_tab}`)).body;
  assert.equal(siblingState.url, origin + '/sibling'); assert.equal(siblingState.control, 'agent');
  assert.equal((await viewed()).control_epoch, control.control_epoch, 'Back keeps only its own page lease');

  // Same-document entries are browser history too, not extra page fetches.
  for (const hash of ['one', 'two']) assert.equal((await input({ type: 'navigate', url: origin + '/a#' + hash })).status, 200);
  const requestsBefore = hits.get('/a');
  assert.equal((await input({ type: 'back' })).status, 200);
  await until(async () => (await viewed()).url === origin + '/a#one', 'Back restores an SPA/hash route');
  assert.equal(hits.get('/a'), requestsBefore, 'same-document Back never refetches the page');
  for (let i = 0; i < 2; i++) assert.equal((await input({ type: 'back' })).status, 200);
  await until(async () => (await viewed()).url === 'about:blank' && (await viewed()).can_go_back === false, 'Back reaches the actual history boundary');
  assert.equal((await input({ type: 'back' })).status, 200, 'a race at the boundary is a safe no-op');
  assert.equal((await viewed()).url, 'about:blank');
});

test('a pending Back refuses concurrent input and a revoked lease fences its eventual result without replay', async () => {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} }, { settleMs: 0 });
  let resolveBack, traversals = 0;
  const page = { url: () => 'https://fixture.invalid/b', isClosed: () => false, goBack: async () => { traversals++; await new Promise(resolve => { resolveBack = resolve; }); } };
  const meta = { id: 't1', page, version: 1, visual: 1, title: '', control: 'human', lease: 'lease', controlEpoch: 1, controlPhase: 'ready' };
  session.pages.set(meta.id, meta); session.active = meta.id;
  session.devtools = { attach: async () => {}, isPaused: () => false, isBlocked: () => false };
  session.activity.finish = async () => {};
  session.observe = async () => ({ ok: true, url: page.url() });
  const body = { type: 'back', target_tab: meta.id, context_ref: session.contextRef, generation: session.generation, lease_token: meta.lease };
  const pending = session.input(body);
  await until(() => resolveBack, 'one native history traversal is pending');
  await assert.rejects(session.input(body), error => error.status === 504);
  meta.control = 'agent'; meta.lease = null; meta.controlEpoch++;
  resolveBack();
  await assert.rejects(pending, error => error.status === 409 || error.status === 403);
  assert.equal(traversals, 1, 'history traversal is never retried to repair an uncertain result');
});
