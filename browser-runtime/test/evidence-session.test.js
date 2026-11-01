import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';
import { BrowserSession, validateAction } from '../src/session.js';
import { EvidenceStore } from '../src/quantities.js';

let worker, site, base, origin, op = 0;
const token = 'evidence-session-fixture-only';
async function request(path, method = 'GET', body) {
  // These single-page fixtures bind ownership to the observed tab explicitly.
  if (path.endsWith('/control') && body) {
    const state = (await request(path.slice(0, -8) + '/state'));
    body = { tab_id: state.active_tab, context_ref: state.context_ref, ...body };
  }
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const reply = await response.json();
  assert.equal(response.status, 200);
  return reply;
}
const execute = (id, args) => request(`/sessions/${id}/execute`, 'POST', { op_id: `evidence-session-${++op}`, ...args });
async function open(id, path = '/a') {
  await request(`/sessions/${id}`, 'PUT');
  return execute(id, { action: 'navigate', url: origin + path });
}
function quantity(reply, field) {
  assert.equal(reply.ok, true, reply.error);
  assert.ok(['observed', 'partial'].includes(reply.evidence?.status), JSON.stringify(reply.evidence));
  const value = reply.evidence.frames.flatMap(frame => frame.quantities).find(item => item.field === field && item.supported);
  assert.ok(value, `Missing observed ${field} quantity`);
  return value;
}

before(async () => {
  site = http.createServer((req, response) => {
    response.setHeader('Content-Type', 'text/html; charset=utf-8');
    const second = req.url.startsWith('/b');
    response.end(`<!doctype html><title>${second ? '青岑' : '溪岸'}详情</title>
      <h1>${second ? '青岑' : '溪岸'}</h1><dl><dt>重量</dt><dd>${second ? '400 g' : '0.385 kg'}</dd></dl>
      <table><thead><tr><th>编号</th><th>名称</th><th>价格</th></tr></thead><tbody>
        <tr><td>K-91</td><td>溪岸</td><td>0.10 CNY</td></tr>
        <tr><td>K-19</td><td>青岑</td><td>0.20 CNY</td></tr>
      </tbody></table><a href="/b">下一详情</a>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 5000, idleMs: 60000 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => {
  await worker?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

test('cross-page arithmetic uses immutable observed fields and retains each original source', { timeout: 25000 }, async () => {
  const first = await open('cross-page');
  const a = quantity(first, '重量');
  const second = await execute('cross-page', { action: 'navigate', url: origin + '/b' });
  const b = quantity(second, '重量');
  const compared = await execute('cross-page', { action: 'calculate', operation: 'compare', quantity_refs: [a.quantity_ref, b.quantity_ref] });
  assert.equal(compared.ok, true);
  assert.equal(compared.evidence.result.relation, 'less');
  const difference = await execute('cross-page', { action: 'calculate', operation: 'difference', quantity_refs: [b.quantity_ref, a.quantity_ref] });
  assert.equal(difference.ok, true);
  assert.equal(difference.evidence.result.value, '15');
  assert.equal(difference.evidence.result.unit, 'g');
  assert.equal(difference.evidence.current_state_verified, false);
  assert.equal(difference.evidence.verifies_entire_answer, false);
  assert.equal(difference.evidence.scope, 'as_observed');
  assert.deepEqual(difference.evidence.sources.map(source => source.provenance.url), [origin + '/b', origin + '/a']);
  assert.ok(difference.evidence.sources.every(source => source.provenance.version && source.source_id && source.field === '重量'));
});

test('real row and field bindings remain aligned and decimal arithmetic is exact', { timeout: 20000 }, async () => {
  const snapshot = await open('table');
  const frame = snapshot.evidence.frames[0], table = frame.tables[0];
  assert.equal(table.rows[0].cells[0].text, 'K-91');
  assert.equal(table.rows[0].cells[1].text, '溪岸');
  const prices = frame.quantities.filter(item => item.field === '价格' && item.supported);
  assert.equal(prices.length, 2);
  assert.ok(prices.every(price => table.rows.some(row => row.cells.some(cell => cell.source_id === price.source_id))));
  const sum = await execute('table', { action: 'calculate', operation: 'sum', quantity_refs: prices.map(item => item.quantity_ref) });
  assert.equal(sum.ok, true);
  assert.equal(sum.evidence.result.value, '0.3');
  const extracted = await execute('table', { action: 'extract', target: 'table' });
  assert.equal(extracted.evidence.scope, 'subtree');
  assert.equal(extracted.evidence.frames[0].tables.length, 1);
  assert.equal(extracted.evidence.frames[0].definitions.length, 0);
});

test('forged, other-session and cancelled references never become numeric facts', { timeout: 25000 }, async () => {
  const a = quantity(await open('owned'), '重量');
  await open('other');
  for (const [id, ref] of [['owned', 'q_invented'], ['other', a.quantity_ref]]) {
    const reply = await execute(id, { action: 'calculate', operation: 'sum', quantity_refs: [ref] });
    assert.equal(reply.ok, false);
    assert.equal(reply.evidence.status, 'error');
  }
  await request('/sessions/owned/cancel', 'POST', {});
  const stale = await execute('owned', { action: 'calculate', operation: 'sum', quantity_refs: [a.quantity_ref] });
  assert.equal(stale.ok, false);
  const fresh = quantity(await execute('owned', { action: 'snapshot' }), '重量');
  assert.notEqual(fresh.quantity_ref, a.quantity_ref);
  const control = await request('/sessions/owned/control', 'POST', { mode: 'human' });
  await request('/sessions/owned/control', 'POST', { mode: 'agent', lease_token: control.lease_token });
  const takeover = await execute('owned', { action: 'calculate', operation: 'sum', quantity_refs: [fresh.quantity_ref] });
  assert.equal(takeover.ok, false);
});

test('capture refuses inconsistent revisions and stores no quantities from the failed observation', async () => {
  const session = Object.create(BrowserSession.prototype);
  session.generation = 1; session.active = 't1'; session.nextObservation = 1;
  session.actionTimeout = 15000; session.observationReads = new Map();
  session.quantities = new EvidenceStore();
  const frame = { isDetached: () => false, url: () => 'https://fixture.invalid', evaluate: async () => ({ schema: 'dom-evidence-v1', tables: [], definitions: [], headings: [], paragraphs: [], truncated: false }) };
  const meta = { id: 't1', version: 1, revisionCovered: true, page: { url: frame.url, frames: () => [frame] } };
  session.page = () => meta;
  let revision = 0; session.refreshRevision = async () => `revision-${++revision}`;
  const result = await session.captureEvidence();
  assert.equal(result.status, 'unavailable');
  assert.equal(result.frames.length, 0);
  assert.equal(session.quantities.stats.observations, 0);
  assert.equal(revision, 2, 'Both capture boundaries must be checked');
});

test('uncovered document revisions cannot certify a structured observation', async () => {
  const session = Object.create(BrowserSession.prototype);
  session.generation = 1; session.active = 't1'; session.nextObservation = 1;
  session.actionTimeout = 15000; session.observationReads = new Map();
  session.quantities = new EvidenceStore();
  const meta = { id: 't1', version: 1, revisionCovered: false };
  session.page = () => meta;
  session.refreshRevision = async () => 'uncovered';
  const result = await session.captureEvidence();
  assert.equal(result.status, 'unavailable');
  assert.equal(result.frames.length, 0);
  assert.equal(session.quantities.stats.observations, 0);
});

test('worker calculation validation cannot accept raw numbers or extra fields', () => {
  for (const value of [
    { action: 'calculate', operation: 'sum', quantity_refs: ['q1'], values: [5] },
    { action: 'calculate', operation: 'sum', quantity_refs: ['q1'], unit: 'kg' },
    { action: 'calculate', operation: 'compare', quantity_refs: ['q1'] },
    { action: 'calculate', operation: 'sum', quantity_refs: ['q1', 'q1'] },
  ]) assert.throws(() => validateAction(value));
});
