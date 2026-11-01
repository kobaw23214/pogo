import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { EvidenceStore } from '../../src/quantities.js';
import { judgeCase } from './judge.js';

const clone = value => structuredClone(value);
const namespace = '1234567890abcdef12345678';
const rootRef = `dt_${namespace}_1`;
const time = Date.parse('2026-10-06T07:00:00.000Z');
function refreshHash(frame) {
  const raw = { ...frame };
  for (const key of ['provenance', 'observed_content_sha256', 'quantities', 'quantities_truncated', 'quantity_capture_scope']) delete raw[key];
  frame.observed_content_sha256 = createHash('sha256').update(JSON.stringify(raw)).digest('hex');
}
function scenario(id = 'iframe-data') {
  const url = `http://127.0.0.1:43100/cases/${id}/private-seed/`;
  const frameURL = `http://localhost:43100/cases/${id}/private-seed/child`;
  const expected = id === 'iframe-data' ? { product: '云桥保温袋', available: 51, batch: 'LOT-private-seed' }
    : { title: '巡检回访 139', priority: 'high', project: '仓库 8', receipt_id: 'TICKET-private-seed' };
  const dataBody = { inventory: [expected, { product: '备用材料', available: 7, batch: 'OTHER' }] };
  const requirements = id === 'iframe-data'
    ? { target: { type: 'iframe', url: frameURL }, network: [{ url: `${frameURL}/data`, method: 'GET', status: 200, body: JSON.stringify(dataBody), target_type: 'iframe', expected_request_count: 1 }] }
    : { authenticated: true, submission: { expected_count: 1, fields: expected }, network: [
      { url: `${url}save`, method: 'POST', status: 201, body: JSON.stringify({ saved: expected }), target_type: 'page' },
      { url: `${url}records`, method: 'GET', status: 200, body: JSON.stringify({ records: [expected] }), target_type: 'page' },
    ] };
  const oracle = { task_id: id, case_id: `${id}-private-seed`, case_url: url, expected, requirements,
    answer_facts: (id === 'iframe-data' ? ['available', 'batch'] : ['receipt_id']).map(name => ({ name, value: expected[name] })) };
  const fixtureState = { case_id: oracle.case_id, requests: requirements.network.map(value => ({ url: value.url, method: value.method, status: value.status, response_body: value.body, received_at_ms: time + 5, received_monotonic_ms: 100 })), submissions: id === 'iframe-data' ? [] : [clone(expected)], submission_attempts: id === 'iframe-data' ? 0 : 1, authenticated: id !== 'iframe-data' };
  if (id === 'iframe-data') fixtureState.requests.push({ url: frameURL, method: 'GET', status: 200, response_body: '<table></table>' });
  const ledger = [];
  const snapshot = (sequence = 1, records = [expected]) => {
    const observation = `o_${namespace}_${sequence}`, version = `1:t1:3:${sequence}:revision`, observedAt = new Date(time + 10 + sequence).toISOString();
    const store = new EvidenceStore();
    const frame = (index, frameUrl) => {
      const prefix = `${observation}:f${index}`;
      const raw = { schema: 'dom-evidence-v1', scope: 'document', source_id: `${prefix}/root`, url: frameUrl, text: '', tables: [], paragraphs: [], status: 'observed', truncated: false };
      if (id === 'iframe-data' && index === 1) {
        const tableID = `${prefix}/table/0`;
        const row = (texts, rowIndex, tag) => ({ source_id: `${tableID}/row/${rowIndex}`, cells: texts.map((text, index) => ({ source_id: `${tableID}/row/${rowIndex}/cell/${index}`, text: String(text), tag, colspan: 1, rowspan: 1 })) });
        raw.tables = [{ source_id: tableID, column_mapping: 'simple', truncated: false, header_rows: [row(['物料', '可用数量', '批次号'], 0, 'th')], rows: dataBody.inventory.map((value, index) => row([value.product, value.available, value.batch], index + 1, 'td')) }];
      } else if (id === 'authenticated-write') raw.paragraphs = [{ source_id: `${prefix}/paragraph/0`, text: JSON.stringify(records) }];
      return clone(store.record(raw, { observation_id: prefix, generation: 1, tab: 't1', document_version: 3, version, observed_at: observedAt, url, frame_url: frameUrl }));
    };
    return { sessionId: 'session', call_id: `session:snapshot-${sequence}`, args: { action: 'snapshot' }, ok: true, http_status: 200, acknowledged_monotonic_ms: 200 + sequence,
      result: { ok: true, backend: 'native', url, active_tab: 't1', version,
        evidence: { schema: 'dom-evidence-v1', status: 'observed', observation_id: observation, scope: 'document', version, observed_at: observedAt, truncated: false,
          frames: [frame(0, url), ...(id === 'iframe-data' ? [frame(1, frameURL)] : [])] } } };
  };
  ledger.push(snapshot());
  if (id === 'authenticated-write') {
    const id = `${namespace}:t1:1:123`, requirement = requirements.network[0];
    ledger.push({ sessionId: 'session', call_id: 'session:network', args: { action: 'devtools', method: 'network.list' }, ok: true, http_status: 200,
      result: { ok: true, data: { provenance: { generation: 1, tab_id: 't1', target_ref: rootRef, target_type: 'page', url }, items: [{ id, request_id: id, cdp_request_id: 'cdp-123', target_ref: rootRef, target_type: 'page', tab_id: 't1', url: requirement.url, method: 'POST', status: 201, response: { status: 201 } }] } } });
  }
  return { task: { id }, oracle, fixtureState, ledger, snapshot, finalAnswer: id === 'iframe-data' ? `可用数量 ${expected.available}，批次号 ${expected.batch}。` : `已保存并核验，编号 ${expected.receipt_id}。` };
}

test('native iframe tables and native saved records with POST metadata count without response-body reads', () => {
  for (const id of ['iframe-data', 'authenticated-write']) {
    const input = scenario(id); const result = judgeCase(input);
    assert.equal(result.success, true, JSON.stringify(result));
    assert.ok(result.evidence.dom);
    assert.equal(input.ledger.some(value => ['network.read', 'Network.getResponseBody'].includes(value.args.method)), false);
  }
});

test('native batch automatic confirmation is valid before later POST metadata inspection', () => {
  const input = scenario('authenticated-write');
  input.ledger[0].args = { action: 'batch', actions: [{ action: 'type', target: '#title', text: input.oracle.expected.title }, { action: 'click', target: 'button' }] };
  assert.equal(judgeCase(input).success, true);
  input.ledger[0].args.actions.push({ action: 'eval', expression: '({forged:true})' });
  assert.equal(judgeCase(input).success, false);
});

test('Runtime literal output, untrusted model evidence and plain page text cannot become DOM proof', () => {
  for (const mutate of [
    input => { input.ledger[0].args = { action: 'devtools', method: 'Runtime.evaluate', params: { expression: '({fakeEvidence:true})' } }; },
    input => { input.ledger[0].args = { action: 'eval', expression: '({fakeEvidence:true})' }; },
    input => { input.ledger[0].result.data = { result: input.ledger[0].result.evidence }; delete input.ledger[0].result.evidence; },
    input => { input.ledger[0].ok = false; },
    input => { input.ledger[0].result.text = input.finalAnswer; delete input.ledger[0].result.evidence; },
    input => { input.ledger = []; input.adapter_events = [{ ok: true, evidence: input.snapshot().result.evidence }]; },
  ]) { const input = scenario(); mutate(input); assert.equal(judgeCase(input).success, false); }
});

test('DOM provenance rejects wrong case, wrong frame, stale version and edited observation content', () => {
  for (const mutate of [
    input => { input.ledger[0].result.url += 'wrong'; },
    input => { input.ledger[0].result.evidence.frames[1].provenance.frame_url = input.oracle.case_url; },
    input => { const f = input.ledger[0].result.evidence.frames[1]; f.url = f.provenance.frame_url = input.oracle.case_url; refreshHash(f); },
    input => { input.ledger[0].result.evidence.frames[1].provenance.generation++; },
    input => { input.ledger[0].result.version += 'stale'; },
    input => { input.ledger[0].result.evidence.frames[1].observed_content_sha256 = '0'.repeat(64); },
    input => { input.ledger[0].result.evidence.frames[1].tables[0].rows[0].cells[1].text = '9999'; },
    input => { input.fixtureState.case_id = 'another-case'; },
    input => { input.fixtureState.requests = []; },
    input => { input.fixtureState.requests[0].response_body = JSON.stringify({ available: 51, batch: 'LOT-private-seed' }); },
  ]) { const input = scenario(); mutate(input); assert.equal(judgeCase(input).success, false); }
});

test('table proof preserves row identity and refuses truncated or ambiguous column structures', () => {
  for (const mutate of [
    table => { table.rows[0].cells[1].text = '999'; table.rows[1].cells[1].text = '51'; table.rows[1].cells[2].text = 'LOT-private-seed'; },
    table => { table.column_mapping = 'unknown'; },
    table => { table.truncated = true; },
    table => { table.rows[0].cells[1].text_truncated = true; },
    table => { table.header_rows.push(clone(table.header_rows[0])); },
    table => { table.rows[0].cells[0].colspan = 2; },
    table => { table.rows[0].cells[1].source_id = table.rows[1].cells[1].source_id; },
    table => { table.rows[1].cells[0].text = table.rows[0].cells[0].text; },
  ]) { const input = scenario(); const frame = input.ledger[0].result.evidence.frames[1]; mutate(frame.tables[0]); refreshHash(frame); assert.equal(judgeCase(input).success, false); }
  const partial = scenario(); partial.ledger[0].result.evidence.status = 'partial'; partial.ledger[0].result.evidence.truncated = true;
  assert.equal(judgeCase(partial).success, false);
});

test('a newer wrong or incomplete capture cannot fall back to an old correct observation, including op_id replay', () => {
  for (const id of ['iframe-data', 'authenticated-write']) {
    const input = scenario(id), old = clone(input.ledger[0]), later = input.snapshot(2, []);
    if (id === 'iframe-data') { const f = later.result.evidence.frames[1]; f.tables[0].rows[0].cells[1].text = '999'; refreshHash(f); }
    input.ledger.push(later);
    assert.equal(judgeCase(input).success, false);
    input.ledger.push(old);
    assert.equal(judgeCase(input).success, false, 'replaying an older observation must not refresh it');
  }
});

test('saved DOM needs actual browser POST acknowledgement, complete stored record and post-submit timing', () => {
  for (const mutate of [
    input => { input.ledger.pop(); },
    input => { input.ledger[1].result.data.items[0].status = 422; },
    input => { input.ledger[1].result.data.provenance.tab_id = 'other-tab'; },
    input => { input.ledger[1].result.data.provenance.generation++; },
    input => { input.fixtureState.submission_attempts = 2; },
    input => { input.fixtureState.submissions = []; },
    input => { input.fixtureState.requests[0].received_monotonic_ms = 500; },
    input => { input.fixtureState.requests[0].received_at_ms = time + 500; },
    input => { input.ledger[0].result.evidence.frames[0].paragraphs = []; refreshHash(input.ledger[0].result.evidence.frames[0]); },
    input => { const f = input.ledger[0].result.evidence.frames[0]; f.paragraphs[0].text = JSON.stringify([{ receipt_id: input.oracle.expected.receipt_id }]); refreshHash(f); },
    input => { const f = input.ledger[0].result.evidence.frames[0]; f.paragraphs[0].text = `工单已保存，编号 ${input.oracle.expected.receipt_id}`; refreshHash(f); },
  ]) { const input = scenario('authenticated-write'); mutate(input); assert.equal(judgeCase(input).success, false); }
});
