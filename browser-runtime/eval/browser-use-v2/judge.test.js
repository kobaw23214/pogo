import test from 'node:test';
import assert from 'node:assert/strict';
import { judgeCase } from './judge.js';

const namespace = '1234567890abcdef12345678';
const rootRef = `dt_${namespace}_1`, childRef = `dt_${namespace}_2`;
const clone = value => structuredClone(value);

function fixture(id = 'network-diagnosis') {
  const url = `http://127.0.0.1:43100/case/${id}-private-seed/`;
  const worker = ['worker-data', 'debugger-recovery'].includes(id);
  const type = worker ? 'worker' : id === 'iframe-data' ? 'iframe' : 'page';
  const targetRef = type === 'page' ? rootRef : childRef;
  const targetURL = type === 'page' ? url : `${url}${worker ? 'worker.js' : 'frame'}`;
  const expected = id === 'network-diagnosis' ? { status: 503, code: 'UPSTREAM_BUSY', trace: 'TRACE-this-case' }
    : id === 'debugger-recovery' ? { invoice_id: 'INV-this-case', total_cents: 2350, currency: 'USD' }
      : id === 'authenticated-write' ? { title: '复核到货数量', receipt_id: 'RECEIPT-this-case' }
        : { label: 'ROW-this-case', count: 17 };
  const body = { ...expected };
  const requirements = { network: [{ url: `${url}data`, method: 'GET', status: id === 'network-diagnosis' ? 503 : 200, body: JSON.stringify(body), target_type: type }], target: { type, url: targetURL } };
  if (id === 'network-diagnosis') requirements.console = [{ target_type: type, includes: ['UPSTREAM_BUSY', 'TRACE-this-case'] }];
  if (id === 'debugger-recovery') requirements.debugger = { target_type: 'worker', script_url: targetURL, locals: { settlement: expected } };
  if (id === 'debugger-recovery') { requirements.network[0].method = 'POST'; requirements.network[0].status = 201; }
  if (['debugger-recovery', 'authenticated-write'].includes(id)) requirements.submission = { expected_count: 1, fields: expected };
  if (id === 'authenticated-write') requirements.authenticated = true;
  const oracle = { task_id: id, case_id: `${id}-private-seed`, case_url: url, expected, requirements, answer_facts: Object.entries(expected).map(([name, value]) => ({ name, value })) };
  const fixtureState = { case_id: oracle.case_id, requests: [{ url: requirements.network[0].url, method: requirements.network[0].method, status: requirements.network[0].status, response_body: JSON.stringify(body), received_monotonic_ms: 1000 }], submissions: requirements.submission ? [expected] : [], submission_attempts: requirements.submission ? 1 : 0, authenticated: id === 'authenticated-write' };
  const ledger = [];
  const action = (method, data, params = {}, selected = targetRef) => {
    const ownerType = selected === rootRef ? 'page' : type;
    const index = ledger.length + 1;
    const exchange = { sessionId: 'session', artifactId: String(index), call_id: `session:${index}`, call: index, started_monotonic_ms: index * 10, args: { action: 'devtools', method, params, ...(selected ? { target: selected } : {}) }, result: { ok: true, data: { provenance: { generation: 1, tab_id: 'tab-1', target_ref: selected ?? rootRef, target_type: ownerType, url: selected === rootRef || !selected ? url : targetURL }, ...clone(data) } }, http_status: 200, ok: true };
    ledger.push(exchange);
    return exchange;
  };
  action('targets.list', { items: [
    { target_ref: rootRef, type: 'page', url, is_root: true, parent_target_ref: null, ready: true },
    ...(type === 'page' ? [] : [{ target_ref: childRef, type, url: targetURL, is_root: false, parent_target_ref: rootRef, ready: true }]),
  ] }, {}, rootRef);
  if (requirements.debugger) {
    // This partial action result is retained in the ledger but is not evidence
    // until a separate actual paused event is observed.
    ledger.push({ sessionId: 'session', call_id: 'partial-pause', args: { action: 'click', ref: 'start' }, result: { ok: false, error: 'debugger_paused' }, http_status: 200, ok: false });
    action('events.list', { items: [{ seq: 1, tab_id: 'tab-1', target_ref: targetRef, target_type: type, method: 'Debugger.paused', params: { callFrames: [{ callFrameId: 'call-frame-1', url: targetURL, location: { scriptId: 'script-1', lineNumber: 1 }, scopeChain: [{ type: 'local', object: { objectId: 'local-scope' } }] }] } }] });
    action('Debugger.evaluateOnCallFrame', { method: 'Debugger.evaluateOnCallFrame', state: 'available', result: { result: { type: 'object', value: expected } } }, { callFrameId: 'call-frame-1', expression: 'settlement', returnByValue: true });
    action('Debugger.resume', { method: 'Debugger.resume', state: 'available', result: {} });
  }
  const requestId = `${namespace}:tab-1:${type === 'page' ? 1 : 2}:123`;
  const row = { id: requestId, request_id: requestId, cdp_request_id: 'request-1', target_ref: targetRef, target_type: type, tab_id: 'tab-1', seq: 123, frame_id: 'frame-1', loader_id: 'loader-1', navigation_id: 'loader-1', url: requirements.network[0].url, method: requirements.network[0].method, status: requirements.network[0].status, response: { status: requirements.network[0].status }, body: { state: 'available' } };
  action('network.list', { items: [row] });
  action('network.read', { request: { ...clone(row), body: { state: 'available', text: JSON.stringify(body) } } }, { request_id: requestId });
  if (requirements.console) action('console.list', { items: [{ seq: 2, tab_id: 'tab-1', target_ref: targetRef, target_type: type, args: [{ type: 'string', value: 'UPSTREAM_BUSY TRACE-this-case' }] }] });
  return { task: { id }, oracle, fixtureState, ledger, finalAnswer: Object.entries(expected).map(([name, value]) => `${name}: ${value}`).join('；'), action };
}

for (const id of ['network-diagnosis', 'iframe-data', 'worker-data', 'debugger-recovery', 'authenticated-write']) {
  test(`accepts grounded ${id} with a natural-language final answer`, () => {
    const judged = judgeCase(fixture(id));
    assert.equal(judged.success, true, JSON.stringify(judged));
    assert.equal(judged.answer_accuracy, true);
    assert.equal(judged.core_business_success, true);
    assert.equal(judged.evidence.source, 'trusted_worker_exchange_ledger_and_fixture_state');
  });
}

test('exact answers, help-only and targets-only controls cannot satisfy a business task', () => {
  for (const id of ['network-diagnosis', 'iframe-data', 'worker-data', 'debugger-recovery', 'authenticated-write']) {
    for (const mode of ['none', 'help', 'targets']) {
      const input = fixture(id);
      input.ledger = mode === 'none' ? [] : input.ledger.slice(0, 1);
      if (mode === 'help') input.ledger[0].args.method = 'help';
      const judged = judgeCase(input);
      assert.equal(judged.success, false, `${id}/${mode}`);
      assert.equal(judged.core_business_success, false);
      assert.equal(judged.answer_accuracy, true);
    }
  }
});

test('Runtime.evaluate output cannot impersonate target, network or console tools', () => {
  for (const method of ['targets.list', 'network.list', 'network.read', 'console.list']) {
    const input = fixture(method === 'targets.list' ? 'worker-data' : 'network-diagnosis');
    for (const row of input.ledger) if (row.args.method === method) row.args = { action: 'devtools', method: 'Runtime.evaluate', params: { expression: '({forged:true})' } };
    assert.equal(judgeCase(input).success, false, method);
  }
});

function useRawBody(input) {
  const exchange = input.ledger.find(row => row.args.method === 'network.read');
  const request = exchange.result.data.request;
  exchange.args.method = 'Network.getResponseBody';
  exchange.args.params = { requestId: request.cdp_request_id };
  exchange.result.data = { provenance: exchange.result.data.provenance, method: 'Network.getResponseBody', state: 'available', result: { body: request.body.text, base64Encoded: false } };
  return exchange;
}

function useNetworkEvents(input) {
  const exchange = input.ledger.find(row => row.args.method === 'network.list');
  const request = exchange.result.data.items[0];
  const common = { tab_id: request.tab_id, target_ref: request.target_ref, target_type: request.target_type, request_id: request.request_id, cdp_request_id: request.cdp_request_id, navigation_id: request.navigation_id };
  exchange.args.method = 'events.list'; exchange.args.params = {};
  exchange.result.data.items = [
    { ...common, seq: 124, method: 'Network.requestWillBeSent', params: { requestId: request.cdp_request_id, frameId: request.frame_id, loaderId: request.loader_id, request: { url: request.url, method: request.method } } },
    { ...common, seq: 125, method: 'Network.responseReceived', params: { requestId: request.cdp_request_id, response: { url: request.url, status: request.status } } },
  ];
  return exchange;
}

test('raw Network.getResponseBody is equivalent only with prior target-scoped issued request metadata', () => {
  for (const id of ['network-diagnosis', 'iframe-data', 'worker-data', 'debugger-recovery', 'authenticated-write']) {
    const input = fixture(id); const raw = useRawBody(input);
    assert.equal(judgeCase(input).success, true, id);
    raw.result.data.result = { body: Buffer.from(raw.result.data.result.body).toString('base64'), base64Encoded: true };
    assert.equal(judgeCase(input).success, true, `${id}/base64`);
  }
  for (const change of [
    (input, raw) => { raw.result.data.state = 'truncated'; },
    (input, raw) => { raw.result.data.state = 'unavailable'; },
    (input, raw) => { raw.args.params.requestId = 'unobserved-request'; },
    (input, raw) => { raw.args.target = rootRef; },
    (input, raw) => { raw.args.method = 'Runtime.evaluate'; },
    (input, raw) => { raw.result.data.result.body = '{"fabricated":true}'; },
    input => { input.ledger = input.ledger.filter(row => row.args.method !== 'network.list'); },
    input => { input.fixtureState.requests = []; },
  ]) { const input = fixture('worker-data'); change(input, useRawBody(input)); assert.equal(judgeCase(input).success, false); }
});

test('raw body reads cannot borrow a same-named request from another target or an older redirect hop', () => {
  const input = fixture('worker-data');
  const otherRef = `dt_${namespace}_3`;
  input.ledger[0].result.data.items.push({ ...clone(input.ledger[0].result.data.items[1]), target_ref: otherRef });
  const raw = useRawBody(input);
  raw.args.target = raw.result.data.provenance.target_ref = otherRef;
  assert.equal(judgeCase(input).success, false);
  raw.args.target = raw.result.data.provenance.target_ref = childRef;
  const list = input.ledger.find(row => row.args.method === 'network.list');
  list.result.data.items.push({ ...clone(list.result.data.items[0]), seq: 150, id: `${namespace}:tab-1:2:150`, request_id: `${namespace}:tab-1:2:150`, url: `${input.oracle.case_url}redirected-elsewhere` });
  assert.equal(judgeCase(input).success, false);
});

test('root page evidence can use genuine provenance without an unnecessary targets.list', () => {
  for (const raw of [false, true]) {
    const input = fixture(); if (raw) useRawBody(input);
    input.ledger = input.ledger.filter(row => row.args.method !== 'targets.list');
    for (const row of input.ledger) delete row.args.target;
    assert.equal(judgeCase(input).success, true);
    input.ledger[0].result.data.provenance.url = undefined;
    assert.equal(judgeCase(input).success, false);
  }
  const child = fixture('worker-data'); child.ledger = child.ledger.filter(row => row.args.method !== 'targets.list');
  assert.equal(judgeCase(child).success, false);
});

test('real Network and Console events support equivalent evidence paths in either log retrieval order', () => {
  for (const raw of [false, true]) for (const reverse of [false, true]) for (const consoleMethod of ['Runtime.consoleAPICalled', 'Log.entryAdded']) {
    const input = fixture();
    const events = useNetworkEvents(input); if (reverse) events.result.data.items.reverse();
    if (raw) useRawBody(input);
    const logged = input.ledger.find(row => row.args.method === 'console.list');
    logged.args.method = 'events.list';
    logged.result.data.items = logged.result.data.items.map(row => ({ ...row, method: consoleMethod, params: consoleMethod === 'Runtime.consoleAPICalled' ? { args: row.args } : { entry: { text: 'UPSTREAM_BUSY TRACE-this-case' } } }));
    input.ledger = input.ledger.filter(row => row.args.method !== 'targets.list');
    assert.equal(judgeCase(input).success, true, `${raw}/${reverse}/${consoleMethod}`);
  }
  for (const change of [
    events => { events.args.method = 'Runtime.evaluate'; },
    events => { events.result.data.items[1].params.requestId = 'foreign-raw-id'; },
    events => { events.result.data.items[1].request_id = `${namespace}:tab-1:1:500`; },
    events => { events.result.data.items[1].target_ref = childRef; },
    events => { events.result.data.items[0].params.request.url = 'http://127.0.0.1/foreign/data'; },
    events => { events.result.data.items[1].params.response.status = 404; },
  ]) { const input = fixture(); change(useNetworkEvents(input)); useRawBody(input); assert.equal(judgeCase(input).success, false); }
});

test('network evidence requires opaque issued IDs, available bodies and matching served responses', () => {
  const modifications = [
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.body.state = 'truncated'; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.body.state = 'unavailable'; },
    input => { input.ledger = input.ledger.filter(row => row.args.method !== 'network.list'); },
    input => { input.ledger.find(row => row.args.method === 'network.read').args.params.request_id = 'request-1'; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.body.text = JSON.stringify({ ...input.oracle.expected, status: 500 }); },
    input => { input.fixtureState.requests[0].status = 500; },
    input => { input.fixtureState.requests[0].response_body = JSON.stringify({ forged: true }); },
    input => { input.fixtureState.requests = []; },
  ];
  for (const modify of modifications) { const input = fixture(); modify(input); assert.equal(judgeCase(input).success, false); }
});

test('case provenance, target type, stale generations and selected envelopes must agree', () => {
  const modifications = [
    input => { input.fixtureState.case_id = 'foreign-case'; },
    input => { input.oracle.task_id = 'foreign-task'; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.url = 'http://127.0.0.1/foreign/data'; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.target_type = 'page'; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.request.target_ref = rootRef; },
    input => { input.ledger.find(row => row.args.method === 'network.read').args.target = rootRef; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.provenance.target_ref = rootRef; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.provenance.generation = 2; },
    input => { input.ledger[0].result.data.items[1].url = 'http://127.0.0.1/foreign/worker.js'; },
    input => { input.ledger[0].result.data.items[1].parent_target_ref = childRef; },
    input => { input.ledger[0].result.data.items[1].ready = false; },
    input => { input.ledger.find(row => row.args.method === 'network.read').result.data.provenance.tab_id = 'foreign-tab'; },
  ];
  for (const modify of modifications) { const input = fixture('worker-data'); modify(input); assert.equal(judgeCase(input).success, false); }
  const stale = fixture('worker-data');
  const newer = clone(stale.ledger[0]); newer.result.data.provenance.generation = 2;
  stale.ledger.splice(1, 0, newer);
  assert.equal(judgeCase(stale).success, false);
});

test('Console text alone or from another target cannot establish network diagnosis', () => {
  for (const mutate of [
    input => { input.fixtureState.requests = []; },
    input => { input.ledger.find(row => row.args.method === 'console.list').result.data.items[0].target_ref = childRef; },
    input => { input.ledger.find(row => row.args.method === 'console.list').result.data.items[0].args[0].value = 'UPSTREAM_BUSY TRACE-another-case'; },
  ]) { const input = fixture(); mutate(input); assert.equal(judgeCase(input).success, false); }
});

test('debugger evidence binds the actual paused frame, local read, target and successful resume', () => {
  const modifications = [
    input => { input.ledger = input.ledger.filter(row => row.args.method !== 'events.list'); },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').args.method = 'Runtime.evaluate'; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').args.params.expression = JSON.stringify(input.oracle.expected); },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').args.params.callFrameId = 'foreign-frame'; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').args.target = rootRef; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').result.data.result.result.value.total_cents++; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.resume').args.target = rootRef; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.resume').result.ok = false; },
    input => { input.ledger.find(row => row.args.method === 'events.list').result.data.items[0].params.callFrames[0].url = 'http://127.0.0.1/foreign/worker.js'; },
    input => { input.fixtureState.submissions = []; input.fixtureState.submission_attempts = 0; },
  ];
  for (const modify of modifications) { const input = fixture('debugger-recovery'); modify(input); assert.equal(judgeCase(input).success, false); }
});

test('Runtime.getProperties counts only a chain rooted in the paused local scope', () => {
  const input = fixture('debugger-recovery');
  const evalIndex = input.ledger.findIndex(row => row.args.method === 'Debugger.evaluateOnCallFrame');
  const original = input.ledger[evalIndex];
  const scope = clone(original);
  scope.args.method = scope.result.data.method = 'Runtime.getProperties';
  scope.args.params = { objectId: 'local-scope' };
  scope.result.data.result = { result: [{ name: 'settlement', value: { type: 'object', objectId: 'settlement-object' } }] };
  const properties = clone(scope);
  properties.call_id += ':properties'; properties.args.params = { objectId: 'settlement-object' };
  properties.result.data.result.result = Object.entries(input.oracle.expected).map(([name, value]) => ({ name, value: { type: typeof value, value } }));
  input.ledger.splice(evalIndex, 1, scope, properties);
  assert.equal(judgeCase(input).success, true);
  scope.args.params.objectId = 'guessed-or-foreign-scope';
  assert.equal(judgeCase(input).success, false);
});

test('a real paused identifier can return an objectId and be inspected through Runtime.getProperties', () => {
  const prepare = () => {
    const input = fixture('debugger-recovery');
    const index = input.ledger.findIndex(row => row.args.method === 'Debugger.evaluateOnCallFrame');
    const evaluated = input.ledger[index];
    delete evaluated.args.params.returnByValue;
    evaluated.result.data.result.result = { type: 'object', objectId: 'actual-settlement' };
    const properties = clone(evaluated);
    properties.call_id += ':properties';
    properties.args.method = properties.result.data.method = 'Runtime.getProperties';
    properties.args.params = { objectId: 'actual-settlement' };
    properties.result.data.result.result = Object.entries(input.oracle.expected).map(([name, value]) => ({ name, value: { type: typeof value, value } }));
    input.ledger.splice(index + 1, 0, properties);
    return { input, evaluated, properties };
  };
  const valid = prepare();
  const judged = judgeCase(valid.input);
  assert.equal(judged.success, true);
  assert.ok(judged.evidence.call_ids.includes(valid.evaluated.call_id));
  assert.ok(judged.evidence.call_ids.includes(valid.properties.call_id));
  for (const change of [
    ({ evaluated }) => { evaluated.args.params.expression = '({invoice_id:"guessed",total_cents:2350,currency:"USD"})'; },
    ({ evaluated }) => { evaluated.args.method = 'Runtime.evaluate'; },
    ({ evaluated }) => { evaluated.args.params.callFrameId = 'foreign-frame'; },
    ({ properties }) => { properties.args.target = rootRef; },
    ({ properties }) => { properties.args.params.objectId = 'unobserved-object'; },
    ({ properties }) => { properties.result.data.result.result[0].value.value = 'wrong'; },
    ({ input, properties }) => { input.ledger.splice(input.ledger.indexOf(properties), 1); input.ledger.push(properties); },
  ]) { const scenario = prepare(); change(scenario); assert.equal(judgeCase(scenario.input).success, false); }
  const stale = prepare();
  const event = clone(stale.input.ledger.find(row => row.args.method === 'events.list'));
  event.result.data.items[0].seq = 50;
  stale.input.ledger.splice(stale.input.ledger.indexOf(stale.properties), 0, event);
  assert.equal(judgeCase(stale.input).success, false);
});

test('empty Chromium call-frame URLs need a script identity observed on the same target', () => {
  const input = fixture('debugger-recovery');
  const paused = input.ledger.find(row => row.args.method === 'events.list');
  paused.result.data.items[0].params.callFrames[0].url = '';
  assert.equal(judgeCase(input).success, false);
  const scripts = input.action('events.list', { items: [{ seq: 10, tab_id: 'tab-1', target_ref: childRef, target_type: 'worker', method: 'Debugger.scriptParsed', params: { scriptId: 'script-1', url: input.oracle.requirements.debugger.script_url } }] });
  assert.equal(judgeCase(input).success, true);
  scripts.result.data.items[0].target_ref = rootRef;
  assert.equal(judgeCase(input).success, false);
  input.ledger.pop();
  const source = 'function actualWorkerSource() {}';
  input.fixtureState.requests.push({ method: 'GET', url: input.oracle.requirements.debugger.script_url, status: 200, response_body: source });
  const fetched = input.action('Debugger.getScriptSource', { method: 'Debugger.getScriptSource', state: 'available', result: { scriptSource: source } }, { scriptId: 'script-1' });
  assert.equal(judgeCase(input).success, true);
  fetched.result.data.result.scriptSource = 'function forgedSource() {}';
  assert.equal(judgeCase(input).success, false);
});

test('a private controller-observed original pause anchors empty frame URLs without an extra source read', () => {
  const prepare = () => {
    const input = fixture('debugger-recovery');
    const event = input.ledger.find(row => row.args.method === 'events.list').result.data.items[0];
    event.params.callFrames[0].url = '';
    input.oracle.prepared_pause_anchor = { generation: 1, tab_id: 'tab-1', target_ref: childRef, target_type: 'worker', pause_seq: event.seq,
      call_frames: [{ call_frame_id: 'call-frame-1', script_id: 'script-1', local_scope_object_ids: ['local-scope'] }] };
    return input;
  };
  assert.equal(judgeCase(prepare()).success, true);
  for (const change of [
    input => { delete input.oracle.prepared_pause_anchor; },
    input => { input.oracle.prepared_pause_anchor.generation++; },
    input => { input.oracle.prepared_pause_anchor.tab_id = 'other-tab'; },
    input => { input.oracle.prepared_pause_anchor.target_ref = rootRef; },
    input => { input.oracle.prepared_pause_anchor.target_type = 'page'; },
    input => { input.oracle.prepared_pause_anchor.pause_seq++; },
    input => { input.oracle.prepared_pause_anchor.call_frames[0].call_frame_id = 'another-frame'; },
    input => { input.oracle.prepared_pause_anchor.call_frames[0].script_id = 'another-script'; },
    input => { input.oracle.prepared_pause_anchor.call_frames[0].local_scope_object_ids = ['another-scope']; },
    input => { input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame').args.params.expression = JSON.stringify(input.oracle.expected); },
    input => { input.ledger = input.ledger.filter(row => row.args.method !== 'events.list'); },
  ]) { const input = prepare(); change(input); assert.equal(judgeCase(input).success, false); }
  const forged = prepare(); delete forged.oracle.prepared_pause_anchor;
  forged.ledger[0].result.data.prepared_pause_anchor = prepare().oracle.prepared_pause_anchor;
  assert.equal(judgeCase(forged).success, false, 'a tool/model-supplied anchor is not controller authority');
});

test('explicit no-replay tasks reject extra real requests even if their recorded answer is correct', () => {
  const input = fixture(); input.oracle.requirements.network[0].expected_request_count = 1;
  assert.equal(judgeCase(input).success, true);
  input.fixtureState.requests.push(clone(input.fixtureState.requests[0]));
  assert.equal(judgeCase(input).success, false);
});

test('duplicated or incorrect writes and missing authentication fail despite correct final text', () => {
  for (const id of ['debugger-recovery', 'authenticated-write']) {
    for (const modify of [
      input => { input.fixtureState.submission_attempts = 2; },
      input => { input.fixtureState.submissions.push(clone(input.fixtureState.submissions[0])); },
      input => { input.fixtureState.submissions[0] = { wrong: true }; },
    ]) { const input = fixture(id); modify(input); assert.equal(judgeCase(input).success, false); }
  }
  const unauthenticated = fixture('authenticated-write'); unauthenticated.fixtureState.authenticated = false;
  assert.equal(judgeCase(unauthenticated).success, false);
});

test('debugger recovery rejects an independently completed save that preceded resume', () => {
  const input = fixture('debugger-recovery');
  const resume = input.ledger.find(row => row.args.method === 'Debugger.resume');
  input.fixtureState.requests[0].received_monotonic_ms = resume.started_monotonic_ms - 1;
  assert.ok(judgeCase(input).failures.some(value => value.startsWith('DEBUGGER_SAVE_NOT_AFTER_RESUME')));
  input.fixtureState.requests[0].received_monotonic_ms = resume.started_monotonic_ms + 1;
  assert.equal(judgeCase(input).success, true);
  delete resume.started_monotonic_ms;
  assert.equal(judgeCase(input).success, false);
});

test('identical call-frame and raw request IDs on separate targets cannot be mixed', () => {
  const input = fixture('debugger-recovery');
  const otherRef = `dt_${namespace}_3`;
  const items = input.ledger[0].result.data.items;
  items.push({ ...clone(items[1]), target_ref: otherRef });
  const events = input.ledger.find(row => row.args.method === 'events.list');
  delete events.args.target;
  events.result.data.provenance.target_ref = rootRef; events.result.data.provenance.target_type = 'page';
  events.result.data.items.push({ ...clone(events.result.data.items[0]), seq: 2, target_ref: otherRef });
  const evaluated = input.ledger.find(row => row.args.method === 'Debugger.evaluateOnCallFrame');
  evaluated.args.target = evaluated.result.data.provenance.target_ref = otherRef;
  assert.ok(judgeCase(input).failures.some(value => value.startsWith('DEBUGGER_CHAIN_MISSING')));
  const resume = input.ledger.find(row => row.args.method === 'Debugger.resume');
  resume.args.target = resume.result.data.provenance.target_ref = otherRef;
  assert.ok(judgeCase(input).failures.some(value => value.startsWith('DEBUGGER_SAVE_NOT_AFTER_RESUME')));
  const read = input.ledger.find(row => row.args.method === 'network.read');
  read.args.target = read.result.data.provenance.target_ref = read.result.data.request.target_ref = otherRef;
  assert.ok(judgeCase(input).failures.some(value => value.startsWith('NETWORK_EVIDENCE_MISSING')));
});

test('a concise Chinese answer only needs the requested facts while saved business fields remain fully checked', () => {
  const input = fixture('authenticated-write');
  input.oracle.answer_facts = [{ name: 'receipt_id', value: input.oracle.expected.receipt_id }];
  input.finalAnswer = `已保存并核验，编号 ${input.oracle.expected.receipt_id}。`;
  assert.equal(judgeCase(input).success, true);
  input.fixtureState.submissions[0] = { receipt_id: input.oracle.expected.receipt_id, title: '错误的标题' };
  assert.equal(judgeCase(input).success, false);
});

test('bounded final-fact checks reject absent, wrong, negated and explicitly contradictory answers', () => {
  const input = fixture('worker-data');
  for (const answer of ['', 'Done.', 'label: ROW-this-case; count: 18', 'label: ROW-this-case; count: 17; count: 18', 'label: ROW-this-case; count is not 17', 'label: ROW-this-case-other; count: 17', 'label: ROW-this-case; count: 170']) {
    const judged = judgeCase({ ...input, finalAnswer: answer });
    assert.equal(judged.success, false, answer); assert.equal(judged.core_business_success, true);
  }
  const oversized = judgeCase({ ...input, finalAnswer: `${input.finalAnswer}${'x'.repeat(17_000)}` });
  assert.equal(oversized.success, false); assert.equal(oversized.needs_review, true);
});
