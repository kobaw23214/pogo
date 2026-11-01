import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { LIMITS, judgeModel, releasePrompt, runReleaseAcceptance, selectModel } from './native-only-release-model.mjs';

const marker = 'UNIQUE-RELEASE-MARKER', receipt = 'RECEIPT-ONCE', fixtureURL = 'http://host.docker.internal:19888/release/case-one';
const oracle = { marker, write_attempts: 1, writes: [{ marker, receipt_id: receipt }] };
function modelEvents({ wrongAnswer = false, excessUsage = false } = {}) {
  const events = [{ type: 'run_start', run_id: 'run-owned' }, { type: 'turn_start', turn: 1 }];
  for (const [i, args] of [
    { action: 'navigate', url: fixtureURL }, { action: 'snapshot' },
    { action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'window.__releaseMarker' } },
    { action: 'click', target: 'e1' }, { action: 'devtools', method: 'network.list' },
    { action: 'devtools', method: 'network.read', params: { request_id: 'net1' } },
  ].entries()) {
    events.push({ type: 'tool_start', name: 'browser', call_id: `c${i}`, args });
    events.push({ type: 'tool_end', name: 'browser', call_id: `c${i}`, details: { ok: true, action: args.action, devtools_data: args.method === 'Runtime.evaluate' ? { result: { value: marker } } : args.method === 'network.read' ? { body: JSON.stringify({ marker, receipt_id: receipt }) } : undefined } });
  }
  const usage = { input: excessUsage ? LIMITS.token_budget + 1 : 150, output: 30 };
  events.push({ type: 'message', role: 'assistant', message_id: 'm-owned', usage, content: [{ type: 'text', text: JSON.stringify({ marker: wrongAnswer ? 'guessed' : marker, receipt_id: receipt }) }] });
  events.push({ type: 'run_end', run_id: 'run-owned', end_reason: 'completed', usage });
  return events.map((event, i) => ({ seq: i + 1, ...event }));
}
function fakeTransport({ duplicateWrites = false, wrongAnswer = false, excessUsage = false, missingApproval = false, cleanupFails = false, contaminated = false } = {}) {
  const requests = [];
  let stream, modelStarted = false, decision;
  const ok = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const record = () => ({ id: 'wr-owned', workflow_id: 'wf-owned', status: decision && !missingApproval ? 'completed' : 'working', nodes: { release_gate: { status: decision && !missingApproval ? 'success' : 'waiting_human', human: { actions: ['approve', 'reject'], ...(decision ? { action: 'approve', values: decision.fields } : {}) } } } });
  const fetch = async (url, options = {}) => {
    const parsed = new URL(url), route = parsed.pathname, method = options.method ?? 'GET', body = options.body ? JSON.parse(options.body) : undefined;
    requests.push({ host: parsed.host, route, method, body, headers: options.headers });
    if (parsed.host === '127.0.0.1:19888') return ok(modelStarted || contaminated ? { ...oracle, ...(duplicateWrites && modelStarted ? { write_attempts: 2, writes: [oracle.writes[0], oracle.writes[0]] } : {}) } : { marker, write_attempts: 0, writes: [] });
    if (route === '/api/models') return ok({ models: [{ id: 'glm-5.3', provider: 'glm', configured: true }], default: 'glm-5.3' });
    if (route === '/api/manage/capabilities') return ok({ skills: [{ name: 'browser-use', builtin: true, id: 'builtin-browser' }] });
    if (['/api/manage/agents', '/api/manage/workflows'].includes(route) && method === 'POST' && typeof body.name === 'string' && [...body.name].length > 60) return ok({ error: 'name exceeds 60 characters' }, 400);
    if (route === '/api/manage/agents' && method === 'POST') return ok({ id: 'ag-owned' }, 201);
    if (route === '/api/apps/ag-owned/sessions') return ok({ id: 's-owned', model: 'glm-5.3' }, 201);
    if (route.endsWith('/capabilities')) return ok({ tools: ['browser'], skills: ['builtin-browser'], mcp_servers: [] });
    if (route === '/api/sessions/s-owned/browser') return ok({ backend: 'native' });
    if (route.endsWith('/stream')) return new Response(new ReadableStream({ start(controller) { stream = controller; options.signal.addEventListener('abort', () => controller.close(), { once: true }); } }), { headers: { 'Content-Type': 'text/event-stream' } });
    if (route.endsWith('/messages')) {
      modelStarted = true;
      for (const event of modelEvents({ wrongAnswer, excessUsage })) stream.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`));
      return ok({ run_id: 'run-owned' });
    }
    if (route.endsWith('/control')) return ok({ cancelled: false });
    if (route === '/api/manage/workflows' && method === 'POST') return ok({ id: 'wf-owned' }, 201);
    if (route.endsWith('/publish')) return ok({ version_id: 'v-owned' }, 201);
    if (route === '/api/workflows/wf-owned/runs' && method === 'POST') return ok({ id: 'wr-owned' }, 201);
    if (route === '/api/workflows/wf-owned/runs/wr-owned') return ok(record());
    if (route.endsWith('/resume')) { decision = body; return missingApproval ? ok({ error: 'not accepted' }, 503) : ok(record()); }
    if (route.endsWith('/artifact')) return ok({ node: 'release_gate', action: 'approve', fields: decision.fields });
    if (route.endsWith('/cancel')) return ok({ cancelled: true });
    if (method === 'DELETE') return cleanupFails && route === '/api/manage/agents/ag-owned' ? ok({}, 503) : ok(null, 204);
    throw new Error(`Unexpected mock API: ${method} ${route}`);
  };
  return { fetch, requests };
}
async function exercise(t, options, overrides = {}) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'native-release-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const transport = fakeTransport(options);
  const config = { output: path.join(dir, 'evidence'), appURL: 'http://127.0.0.1:19777', fixtureURL, oracleURL: 'http://127.0.0.1:19888/private-oracle', token: 'private-app-token', releaseToken: 'private-release-token', oracleHeaders: { 'X-Oracle-Token': 'private-oracle-token' }, fetch: transport.fetch, sleep: () => new Promise(resolve => setImmediate(resolve)), ...overrides };
  const report = await runReleaseAcceptance(config);
  return { ...transport, report, config, saved: await readFile(path.join(config.output, 'summary.json'), 'utf8') };
}

test('model selection requires configured preferred or configured default, records fallback', () => {
  assert.equal(selectModel({ models: [{ id: 'fallback', configured: true }], default: 'fallback' }).fallback, true);
  assert.equal(selectModel({ models: [{ id: 'glm-5.3', configured: false }, { id: 'fallback', configured: true }], default: 'fallback' }).id, 'fallback');
  assert.throws(() => selectModel({ models: [{ id: 'glm-5.3', configured: false }], default: 'glm-5.3' }));
});
test('prompt contains only public fixture URL, never hidden expected marker or oracle', () => {
  const prompt = releasePrompt(fixtureURL);
  assert.ok(prompt.includes(fixtureURL) && prompt.includes('Runtime.evaluate') && prompt.includes('network.read'));
  assert.ok(!prompt.includes(marker) && !prompt.includes(receipt) && !prompt.includes('private-oracle'));
});
test('judge requires actual paired CDP receipts, oracle single write and answer identity', () => {
  const events = modelEvents();
  assert.equal(judgeModel(events, oracle, fixtureURL, 'run-owned', { passed: true }).passed, true);
  assert.equal(judgeModel(events.filter(event => !(event.type === 'tool_end' && event.call_id === 'c2')), oracle, fixtureURL, 'run-owned', { passed: true }).passed, false);
  assert.equal(judgeModel(events, { ...oracle, write_attempts: 2 }, fixtureURL, 'run-owned', { passed: true }).passed, false);
  assert.equal(judgeModel(events, oracle, fixtureURL, 'wrong-run', { passed: true }).passed, false);
});
test('native navigation snapshot counts as page observation without forcing another read', () => {
  const events = modelEvents().filter(event => event.call_id !== 'c1');
  events.find(event => event.type === 'tool_end' && event.call_id === 'c0').text = 'Current page: button e1 Save once';
  assert.equal(judgeModel(events, oracle, fixtureURL, 'run-owned', { passed: true }).passed, true);
});
test('new Agent tab counts as one original navigation but never permits a second visit', () => {
  const events = modelEvents();
  const start = events.find(event => event.type === 'tool_start' && event.call_id === 'c0');
  start.args = { action: 'tab', tab_action: 'new', url: fixtureURL };
  assert.equal(judgeModel(events, oracle, fixtureURL, 'run-owned', { passed: true }).passed, true);
  events.push({ type: 'tool_start', name: 'browser', call_id: 'duplicate', args: { action: 'navigate', url: fixtureURL } });
  events.push({ type: 'tool_end', name: 'browser', call_id: 'duplicate', details: { ok: true } });
  assert.ok(judgeModel(events, oracle, fixtureURL, 'run-owned', { passed: true }).failures.includes('single_original_navigation'));
});
test('actual API path stays owned, uses one model request and one approval, redacts credentials', async t => {
  const { report, requests, saved } = await exercise(t);
  assert.equal(report.model.passed, true); assert.equal(report.workflow.passed, true); assert.equal(report.passed, true);
  assert.equal(requests.filter(item => item.route.endsWith('/messages')).length, 1);
  assert.equal(requests.filter(item => item.route.endsWith('/resume')).length, 1);
  assert.deepEqual(requests.filter(item => item.method === 'DELETE').map(item => item.route).sort(), ['/api/manage/agents/ag-owned', '/api/manage/workflows/wf-owned', '/api/sessions/s-owned']);
  assert.equal(report.workflow.model_calls, 0);
  assert.equal(report.cleanup.retained_run_history.run_id, 'wr-owned');
  for (const secret of ['private-app-token', 'private-release-token', 'private-oracle-token']) assert.ok(!saved.includes(secret));
  assert.ok(requests.filter(item => item.host.endsWith(':19777')).every(item => item.headers.Authorization === 'Bearer private-app-token' && item.headers['X-Pogo-Release'] === 'private-release-token'));
  assert.ok(requests.filter(item => item.host.endsWith(':19888')).every(item => !item.headers.Authorization && !item.headers['X-Pogo-Release']));
});
test('duplicate fixture writes cannot pass even when model claims success', async t => {
  const { report, requests } = await exercise(t, { duplicateWrites: true });
  assert.equal(report.model.passed, false); assert.equal(report.workflow.passed, true); assert.equal(report.passed, false);
  assert.ok(report.model.judgement.failures.includes('exactly_one_fixture_write'));
  assert.equal(requests.filter(item => item.route.endsWith('/messages')).length, 1);
});
test('wrong model answer cannot be promoted to acceptance by completed run state', async t => {
  const { report } = await exercise(t, { wrongAnswer: true });
  assert.equal(report.model.passed, false); assert.ok(report.model.judgement.failures.includes('final_answer_mismatch'));
});
test('observed token-budget overrun is failed and cancelled, never retried', async t => {
  const { report, requests } = await exercise(t, { excessUsage: true });
  assert.equal(report.model.passed, false); assert.ok(report.model.monitor.violations.includes('token_budget_exceeded'));
  assert.ok(requests.some(item => item.route.endsWith('/control') && item.body.action === 'cancel'));
  assert.equal(requests.filter(item => item.route.endsWith('/messages')).length, 1);
});
test('failed human approval remains false and triggers only owned workflow cancellation', async t => {
  const { report, requests } = await exercise(t, { missingApproval: true });
  assert.equal(report.model.passed, true); assert.equal(report.workflow.passed, false); assert.equal(report.passed, false);
  assert.equal(requests.filter(item => item.route.endsWith('/resume')).length, 1);
  assert.ok(requests.some(item => item.route === '/api/workflows/wf-owned/runs/wr-owned/cancel'));
});
test('cleanup failure prevents a green release result', async t => {
  const { report } = await exercise(t, { cleanupFails: true });
  assert.equal(report.model.passed, true); assert.equal(report.workflow.passed, true); assert.equal(report.passed, false);
  assert.equal(report.cleanup.agent_deleted, false);
});
test('contaminated fixture refuses paid call but still checks real human-node API path', async t => {
  const { report, requests } = await exercise(t, { contaminated: true });
  assert.equal(report.model.passed, false); assert.equal(report.workflow.passed, true);
  assert.equal(requests.some(item => item.route.endsWith('/messages')), false);
  assert.equal(requests.some(item => item.route === '/api/manage/agents'), false);
});

test('combined resource names satisfy the production 60-character maximum', async t => {
  const { report, requests } = await exercise(t);
  assert.equal(report.passed, true);
  const names = requests.filter(item => ['/api/manage/agents', '/api/manage/workflows'].includes(item.route) && item.method === 'POST').map(item => item.body.name);
  assert.equal(names.length, 2);
  assert.ok(names.every(name => [...name].length <= 60));
  assert.ok(names[1].endsWith('-human'));
});
test('workflow-only scope needs no fixture, never invokes model APIs and never claims full release success', async t => {
  const { report, requests } = await exercise(t, {}, { workflowOnly: true, fixtureURL: undefined, oracleURL: undefined });
  assert.equal(report.scope, 'workflow-only');
  assert.equal(report.model.passed, false); assert.equal(report.model.skipped, true);
  assert.equal(report.workflow.passed, true); assert.equal(report.scope_passed, true); assert.equal(report.passed, false);
  assert.ok(requests.every(item => item.route.startsWith('/api/manage/workflows') || item.route.startsWith('/api/workflows/')));
  assert.deepEqual(requests.filter(item => item.method === 'DELETE').map(item => item.route), ['/api/manage/workflows/wf-owned']);
});
test('workflow-only failure returns false scoped outcome and preserves the full-release false boundary', async t => {
  const { report } = await exercise(t, { missingApproval: true }, { workflowOnly: true });
  assert.equal(report.model.passed, false); assert.equal(report.workflow.passed, false);
  assert.equal(report.scope_passed, false); assert.equal(report.passed, false);
});
