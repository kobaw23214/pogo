// Finite DOM proofs from worker-owned native observations. This module never
// interprets Runtime expressions or treats model-authored JSON as a snapshot.
import { createHash } from 'node:crypto';
import { isDeepStrictEqual } from 'node:util';

const OBSERVATION = /^o_[a-f0-9]{24}_[1-9][0-9]*$/;
const NATIVE = new Set(['snapshot', 'navigate', 'wait', 'click', 'type', 'select', 'press', 'hover', 'scroll', 'extract', 'tab']);
const plain = value => Boolean(value && typeof value === 'object' && !Array.isArray(value));
const array = value => Array.isArray(value) ? value : [];
const canonical = value => Array.isArray(value) ? value.map(canonical) : plain(value) ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const same = (left, right) => isDeepStrictEqual(canonical(left), canonical(right));
const parse = text => { try { return JSON.parse(text); } catch { return undefined; } };
const complete = value => plain(value) && !value.truncated && !value.text_truncated;
const nativeAction = args => plain(args) && (NATIVE.has(args.action) || args.action === 'batch' && Array.isArray(args.actions) && args.actions.length > 0 && args.actions.every(child => plain(child) && NATIVE.has(child.action)));
const nativeAck = exchange => exchange?.ok === true && exchange.http_status >= 200 && exchange.http_status < 300 && exchange.result?.ok === true && exchange.result.backend === 'native' && nativeAction(exchange.args);

function hashMatches(frame) {
  if (!/^[a-f0-9]{64}$/.test(frame.observed_content_sha256 ?? '')) return false;
  const content = { ...frame };
  for (const key of ['provenance', 'observed_content_sha256', 'quantities', 'quantities_truncated', 'quantity_capture_scope']) delete content[key];
  return createHash('sha256').update(JSON.stringify(content)).digest('hex') === frame.observed_content_sha256;
}

function framesOf(exchange, oracle) {
  const reply = exchange.result, evidence = reply.evidence;
  if (!plain(evidence) || evidence.schema !== 'dom-evidence-v1' || evidence.status !== 'observed' || evidence.truncated !== false || reply.observation_pending
    || !OBSERVATION.test(evidence.observation_id ?? '') || !['document', 'subtree'].includes(evidence.scope)
    || evidence.version !== reply.version || !Number.isFinite(Date.parse(evidence.observed_at)) || array(evidence.frames).length === 0) return [];
  const version = /^([1-9][0-9]*):([^:]+):([1-9][0-9]*):/.exec(reply.version ?? '');
  if (!version || version[2] !== reply.active_tab) return [];
  const out = [];
  for (const [index, frame] of evidence.frames.entries()) {
    const provenance = frame?.provenance;
    const observation = `${evidence.observation_id}:f${index}`;
    if (!complete(frame) || frame.schema !== 'dom-evidence-v1' || frame.status !== 'observed' || frame.quantities_truncated === true || frame.scope !== evidence.scope
      || !plain(provenance) || provenance.observation_id !== observation || frame.source_id !== `${observation}/root`
      || provenance.generation !== Number(version[1]) || provenance.tab !== reply.active_tab || provenance.document_version !== Number(version[3])
      || provenance.version !== evidence.version || provenance.observed_at !== evidence.observed_at || provenance.url !== oracle.case_url
      || provenance.frame_url !== frame.url || typeof frame.url !== 'string' || !hashMatches(frame)) continue;
    out.push({ frame, provenance, observation, exchange, observed_at_ms: Date.parse(evidence.observed_at) });
  }
  return out;
}

function servedResponse(state, requirement) {
  return array(state?.requests).find(request => request.url === requirement.url && request.method === requirement.method && request.status === requirement.status
    && same(parse(request.response_body), typeof requirement.body === 'string' ? parse(requirement.body) : requirement.body));
}

function validRow(row, table, count) {
  if (!complete(row) || typeof row.source_id !== 'string' || !row.source_id.startsWith(`${table.source_id}/row/`)
    || !/^[0-9]+$/.test(row.source_id.slice(`${table.source_id}/row/`.length)) || !Array.isArray(row.cells) || row.cells.length !== count) return false;
  return row.cells.every((cell, index) => complete(cell) && cell.source_id === `${row.source_id}/cell/${index}` && typeof cell.text === 'string'
    && ['th', 'td'].includes(cell.tag) && cell.rowspan === 1 && cell.colspan === 1);
}

function inventoryProof(observations, oracle, state) {
  const requirement = array(oracle.requirements?.network).find(value => value.target_type === 'iframe');
  const frameURL = oracle.requirements?.target?.url;
  if (!requirement || !frameURL || !servedResponse(state, requirement)
    || !array(state?.requests).some(request => request.url === frameURL && request.method === 'GET' && request.status === 200)) return null;
  const expected = oracle.expected;
  const served = typeof requirement.body === 'string' ? parse(requirement.body) : requirement.body;
  if (array(served?.inventory).filter(row => same(row, expected)).length !== 1) return null;
  const candidates = [];
  for (const observation of observations.filter(value => value.frame.url === frameURL && value.frame.url !== oracle.case_url)) {
    for (const [tableIndex, table] of array(observation.frame.tables).entries()) {
      if (!complete(table) || table.source_id !== `${observation.observation}/table/${tableIndex}` || table.column_mapping !== 'simple'
        || array(table.header_rows).length !== 1 || !Array.isArray(table.rows)) continue;
      const header = table.header_rows[0], count = array(header?.cells).length;
      if (!validRow(header, table, count) || !count || header.cells.some(cell => cell.tag !== 'th')) continue;
      const headers = header.cells.map(cell => cell.text.trim());
      const fields = ['物料', '可用数量', '批次号'].map(label => headers.indexOf(label));
      if (fields.some(index => index < 0) || new Set(headers).size !== headers.length || table.rows.some(row => !validRow(row, table, count))) continue;
      if (new Set([header.source_id, ...table.rows.map(row => row.source_id)]).size !== table.rows.length + 1) continue;
      for (const row of table.rows) if (row.cells[fields[0]].text === expected.product) {
        candidates.push({ observation, source_id: row.source_id,
          valid: row.cells[fields[1]].text === String(expected.available) && row.cells[fields[2]].text === expected.batch });
      }
    }
  }
  if (candidates.length !== 1 || !candidates[0].valid) return null;
  return { kind: 'iframe_table_row', call_id: candidates[0].observation.exchange.call_id, source_id: candidates[0].source_id,
    session_id: candidates[0].observation.exchange.sessionId, observation_id: candidates[0].observation.observation, frame_url: frameURL, provenance: candidates[0].observation.provenance };
}

function savedRecordProof(observations, oracle, state) {
  const fields = oracle.requirements?.submission?.fields;
  const requirement = array(oracle.requirements?.network).find(value => value.method === 'POST');
  if (!fields || !requirement) return null;
  const served = servedResponse(state, requirement);
  if (!served || !Number.isFinite(served.received_monotonic_ms) || !Number.isFinite(served.received_at_ms)) return null;
  const matches = [];
  for (const observation of observations.filter(value => value.frame.url === oracle.case_url)) {
    if (observation.observed_at_ms < served.received_at_ms || !Number.isFinite(observation.exchange.acknowledged_monotonic_ms)
      || observation.exchange.acknowledged_monotonic_ms < served.received_monotonic_ms) continue;
    for (const [index, paragraph] of array(observation.frame.paragraphs).entries()) {
      if (!complete(paragraph) || paragraph.source_id !== `${observation.observation}/paragraph/${index}` || typeof paragraph.text !== 'string') continue;
      const records = parse(paragraph.text.trim());
      if (!Array.isArray(records) || records.length !== 1 || !same(records[0], fields)) continue;
      matches.push({ observation, paragraph });
    }
  }
  if (matches.length !== 1) return null;
  const match = matches[0];
  return { kind: 'saved_record_paragraph', call_id: match.observation.exchange.call_id, source_id: match.paragraph.source_id,
    session_id: match.observation.exchange.sessionId, observation_id: match.observation.observation, frame_url: oracle.case_url, provenance: match.observation.provenance,
    observed_at_ms: match.observation.observed_at_ms, acknowledged_monotonic_ms: match.observation.exchange.acknowledged_monotonic_ms, served };
}

export function nativeDOMProof({ task, oracle, fixtureState, ledger }) {
  if (!['iframe-data', 'authenticated-write'].includes(task?.id) || !oracle || fixtureState?.case_id !== oracle.case_id) return null;
  // Only the latest native observation of this case can establish current DOM
  // confirmation. A later partial/incorrect capture must not revive an older
  // correct row. Uninterpreted Runtime output does not replace a DOM capture.
  let latest;
  for (const exchange of array(ledger)) {
    if (!nativeAck(exchange) || exchange.result.url !== oracle.case_url || !Object.hasOwn(exchange.result, 'evidence')) continue;
    const sequence = Number(exchange.result.evidence?.observation_id?.split('_').at(-1));
    const previous = Number(latest?.result.evidence?.observation_id?.split('_').at(-1));
    // An op_id replay returns the original worker observation. Its later HTTP
    // acknowledgement cannot make it newer than a subsequent native capture.
    if (latest?.sessionId === exchange.sessionId && Number.isSafeInteger(sequence) && Number.isSafeInteger(previous) && sequence < previous) continue;
    latest = exchange;
  }
  if (!latest) return null;
  const observations = framesOf(latest, oracle);
  return task.id === 'iframe-data' ? inventoryProof(observations, oracle, fixtureState) : savedRecordProof(observations, oracle, fixtureState);
}
