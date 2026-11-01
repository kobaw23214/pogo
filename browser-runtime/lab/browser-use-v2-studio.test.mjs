import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { BUDGETS, BROWSER_HANDOFF, correlateEvidence, createSSEParser, createRunMonitor, finalAnswer, redactReport, runAcceptance, parseCLI, validateLoopback } from './browser-use-v2-studio.mjs';

const assistant = (input = 100, output = 20) => ({ type: 'message', role: 'assistant', message_id: 'm1', usage: { input, output }, content: [{ type: 'text', text: '实际结果 92，批次 LOT-EXAMPLE。' }] });
const ended = (input = 100, output = 20) => ({ type: 'run_end', end_reason: 'completed', usage: { input, output } });

test('SSE parses chunked CRLF, multiline data and comments without treating event text as authority', () => {
  const events = [], parser = createSSEParser(event => events.push(event));
  parser.push(': keep alive\r\n\r\nevent: message\r\nda');
  parser.push('ta: {"type":"message",\r\ndata: "text":"好"}\r\n\r');
  parser.push('\n'); parser.finish();
  assert.deepEqual(events, [{ type: 'message', text: '好' }]);
  assert.throws(() => createSSEParser(() => {}).push('data: invalid\n\n'));
  const partial = createSSEParser(() => {}); partial.push('data: {'); assert.throws(() => partial.finish(), /incomplete/);
  assert.throws(() => createSSEParser(() => {}, 5).push('123456'), /limit/);
});

test('usage monitoring sums per-message usage but never double counts run total, cache or replay', () => {
  const monitor = createRunMonitor();
  const first = { ...assistant(1000, 200), seq: 1, usage: { input: 1000, output: 200, cache_read: 900, reasoning: 100 } };
  monitor.observe(first); monitor.observe(first);
  monitor.observe({ ...assistant(1500, 300), seq: 2, message_id: 'm2' });
  monitor.observe({ ...ended(2500, 500), seq: 3 });
  assert.deepEqual(monitor.snapshot().usage, { input: 2500, output: 500 });
  assert.equal(monitor.snapshot().passed, true);
});

test('usage overrun is a hard failure even when final answer and run completion succeed', () => {
  const monitor = createRunMonitor();
  monitor.observe(assistant(119900, 101)); monitor.observe(ended(119900, 101));
  assert.equal(monitor.snapshot().passed, false);
  assert.deepEqual(monitor.snapshot().violations, ['token_budget_exceeded']);
});

test('incomplete or regressing usage cannot pass as full measured acceptance', () => {
  for (const events of [[{ ...assistant(), usage: null }, ended()], [assistant(1000, 20), ended(1, 1)], [assistant()]]) {
    const monitor = createRunMonitor(); events.forEach(event => monitor.observe(event));
    assert.equal(monitor.snapshot().passed, false);
  }
});

test('time, turn and browser-step caps include actual overrun', () => {
  let now = 0;
  const monitor = createRunMonitor({ now: () => now });
  monitor.observe({ type: 'turn_start', turn: 51 });
  for (let i = 0; i < 81; i++) monitor.observe({ type: 'tool_start', name: 'browser', call_id: `c${i}` });
  now = BUDGETS.timeout_ms + 1; monitor.check();
  assert.deepEqual(monitor.snapshot().violations.sort(), ['turn_budget_exceeded', 'browser_step_budget_exceeded', 'time_budget_exceeded'].sort());
});

test('protected read is allowed only for the exact selected builtin skill; every other tool fails', () => {
  const good = createRunMonitor();
  good.observe({ type: 'tool_start', name: 'read', args: { path: '/home/pogo/.cache/pogo/embedded-skills/browser-use/SKILL.md' } });
  assert.deepEqual(good.snapshot().violations, []);
  for (const event of [{ name: 'read', args: { path: '/data/recorder/oracle.json' } }, { name: 'subagent' }, { name: 'bash' }, { name: 'ls' }]) {
    const monitor = createRunMonitor(); monitor.observe({ type: 'tool_start', ...event });
    assert.match(monitor.snapshot().violations[0], /disallowed_tool/);
  }
});

test('redaction removes controller secrets and lease values without removing actual measured usage', () => {
  const cleaned = redactReport({ nested: { lease_token: 'lease-secret', password: 'pwd' }, prompt: 'value=controller-secret', usage: { input: 42, output: 6 } }, ['controller-secret']);
  assert.equal(JSON.stringify(cleaned).includes('controller-secret'), false);
  assert.equal(JSON.stringify(cleaned).includes('lease-secret'), false);
  assert.deepEqual(cleaned.usage, { input: 42, output: 6 });
});

test('already-redacted full worker ACK archives keep the original ledger hash after repeated host redaction', () => {
  const ledger = [{
    sessionId: 'owned-worker-session', artifactId: 'studio-authenticated-write', call_id: 'owned-worker-session:1', call: 1,
    args: { action: 'devtools', method: 'network.read', params: { request_id: 'net_41' }, op_id: 'op-1' },
    result: { ok: true, backend: 'native', active_tab: 't1', version: '1:t1:2:3', data: { request: {
      id: 'net_41', target_ref: 'dt_0123456789abcdef01234567_1', url: 'http://fixture-a.test/cases/saved', method: 'POST', status: 201,
      request_headers: { Cookie: '[REDACTED]', Accept: 'application/json' }, response_headers: { 'set-cookie': '[REDACTED]', 'content-type': 'application/json' },
      body: { state: 'available', text: '{"saved":{"receipt_id":"TICKET-EXAMPLE","priority":"high"}}', bytes: 68 },
      request_extra: { headers: { cookie: '[REDACTED]' } },
    } } }, http_status: 200, ok: true, at_ms: 24, duration_ms: 5, started_monotonic_ms: 1001, acknowledged_monotonic_ms: 1006,
  }];
  const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
  const report = { ledger, ledger_sha256: hash(ledger), usage: { input: 10000, output: 120 } };
  const once = redactReport(report, ['controller-secret', 'REDACTED']);
  const twice = redactReport(once, ['controller-secret', 'REDACTED']);
  assert.deepEqual(once, report);
  assert.deepEqual(twice, report);
  assert.equal(hash(twice.ledger), report.ledger_sha256);
});

test('redaction still masks real secrets, including escaped strings, without corrupting placeholders', () => {
  const secret = 'quoted"credential\nline';
  const report = { Cookie: 'real-cookie', Authorization: 'Bearer real-bearer', lease_token: 'real-lease', nested: { text: `old [REDACTED]; new ${secret}; literal a.*[value]`, [secret]: secret } };
  const secrets = [secret, 'a.*[value]', 'REDACTED'];
  const once = redactReport(report, secrets), twice = redactReport(once, secrets);
  assert.equal(once.Cookie, '[REDACTED]'); assert.equal(once.Authorization, '[REDACTED]'); assert.equal(once.lease_token, '[REDACTED]');
  assert.equal(once.nested.text, 'old [REDACTED]; new [REDACTED]; literal [REDACTED]');
  assert.equal(once.nested['[REDACTED]'], '[REDACTED]');
  assert.deepEqual(twice, once);
  assert.equal(JSON.stringify(once).includes('real-cookie'), false);
  assert.equal(JSON.stringify(once).includes('credential'), false);
});

test('controller addresses are loopback-only and CLI defaults to smoke on separate candidate ports', () => {
  assert.equal(validateLoopback('http://127.0.0.1:19779'), 'http://127.0.0.1:19779');
  for (const address of ['http://remote:80', 'http://user:pwd@localhost:1', 'http://localhost:1/oracle']) assert.throws(() => validateLoopback(address));
  const config = parseCLI(['/tmp/new'], {});
  assert.equal(config.mode, 'smoke'); assert.equal(config.appURL, 'http://127.0.0.1:19779');
  assert.equal(config.recorderURL, 'http://127.0.0.1:19780');
  assert.throws(() => parseCLI(['/tmp/new', '--seed', 'abc'], {}));
});

function fakeTransport({ badTool = false, cleanupFails = false } = {}) {
  const requests = [], streams = new Map(); let sid = 0, active, lastReport;
  const ok = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const api = async (url, options = {}) => {
    const parsed = new URL(url), route = parsed.pathname, method = options.method ?? 'GET', body = options.body ? JSON.parse(options.body) : undefined;
    requests.push({ host: parsed.host, route, method, body });
    if (route === '/api/login') return new Response('{}', { headers: { 'Set-Cookie': 'session=private-cookie; HttpOnly' } });
    if (route === '/api/manage/capabilities') return ok({ skills: [{ id: 'builtin-browser', name: 'browser-use', builtin: true }] });
    if (route === '/api/manage/agents' && method === 'POST') return ok({ id: 'a1' });
    if (route === '/api/manage/agents/a1' && method === 'DELETE') return ok(null, 204);
    if (route === '/api/apps/a1/sessions') return ok({ id: `s${++sid}`, model: 'glm-5.3' });
    if (route.endsWith('/capabilities')) return ok({ tools: ['browser'], skills: ['builtin-browser'], mcp_servers: [], model: 'glm-5.3' });
    if (route === '/runs/begin') { active = { ...body, worker_session_id: null }; return ok({ task: { id: body.task_id, prompt: '核对当前页面实际数据，并提供已观察到的证据。' } }); }
    if (/\/api\/sessions\/s\d+\/browser$/.test(route)) { active.worker_session_id = `w${sid}`; return ok({ backend: 'native', control: 'agent' }); }
    if (route === '/runs/state') return ok(active);
    if (route.endsWith('/browser/control')) return ok(body.mode === 'human' ? { lease_token: 'private-lease', control: 'human' } : { control: 'agent' });
    if (route === '/runs/prepare' || route === '/runs/start') return ok({ prepared: true });
    if (route === '/runs/execute') return ok({ ok: true, data: { items: [{ type: active.task_id === 'iframe-data' ? 'iframe' : 'worker', paused: active.task_id === 'debugger-recovery' }] } });
    if (route.endsWith('/stream')) {
      const id = route.split('/')[3];
      const stream = new ReadableStream({ start(controller) { streams.set(id, controller); options.signal?.addEventListener('abort', () => controller.close(), { once: true }); } });
      return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } });
    }
    if (route.endsWith('/messages')) {
      const id = route.split('/')[3], stream = streams.get(id);
      const events = [{ type: 'run_start', run_id: 'r1' }, { type: 'turn_start', turn: 1 }, { type: 'tool_start', call_id: 'c1', name: badTool ? 'read' : 'browser', args: badTool ? { path: '/private/oracle' } : { action: 'snapshot' } }, { type: 'tool_end', call_id: 'c1', name: badTool ? 'read' : 'browser', details: { action: 'snapshot', ok: true } }, assistant(), ended()];
      for (const [index, event] of events.entries()) stream.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ seq: index + 1, ...event })}\n\n`));
      return ok({ run_id: 'r1' });
    }
    if (route.endsWith('/control')) return ok({ cancelled: false });
    if (/\/api\/sessions\/s\d+$/.test(route) && method === 'DELETE') return ok(null, 204);
    if (route === '/runs/finish') { lastReport = { judgement: { success: true }, studio_run_id: body.run_id, studio_session_id: active.studio_session_id, worker_session_id: active.worker_session_id, ledger: [{ raw_worker_ack: 'not_sse', sessionId: active.worker_session_id, call_id: 'worker1', args: { action: 'snapshot', op_id: 'native-op' }, result: { ok: true } }], answer: body.answer }; return ok(lastReport); }
    if (route === '/runs/cleanup') { lastReport = { ...lastReport, context_cleaned: !cleanupFails, cancel_acknowledged: !cleanupFails, delete_acknowledged: !cleanupFails, server_state_after_cleanup: { submission_attempts: 0, submissions: [] } }; return ok(lastReport); }
    if (route === '/runs/report') return ok(lastReport);
    throw new Error(`Unexpected mock route ${method} ${route}`);
  };
  return { fetch: api, requests };
}

async function exercise(t, mode, transportOptions) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'studio-v2-test-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const transport = fakeTransport(transportOptions);
  const config = { mode, output: path.join(dir, 'fresh'), appURL: 'http://127.0.0.1:19779', recorderURL: 'http://127.0.0.1:19780', password: 'private-password', recorderToken: 'private-controller-token', fetch: transport.fetch };
  const report = await runAcceptance(config);
  return { config, report, ...transport, saved: await readFile(path.join(config.output, 'summary.json'), 'utf8') };
}

test('no-model wiring follows owned App mapping, controller preparation, native cleanup and never calls messages', async t => {
  const { report, requests, saved } = await exercise(t, 'smoke');
  assert.equal(report.passed, true); assert.equal(report.tasks.length, 2);
  assert.equal(requests.some(item => item.route.endsWith('/messages')), false);
  assert.equal(requests.filter(item => item.route === '/runs/execute').length, 2);
  assert.equal(requests.filter(item => item.method === 'DELETE' && item.route.startsWith('/api/sessions/')).length, 2);
  assert.equal(saved.includes('private-cookie'), false); assert.equal(saved.includes('private-lease'), false);
});

test('model route preserves natural goals, records separate raw ACK and SSE evidence, usage and final answer', async t => {
  const { report, requests, config } = await exercise(t, 'model');
  assert.equal(report.passed, true);
  assert.equal(requests.filter(item => item.route.endsWith('/messages')).length, 2);
  assert.equal(requests.some(item => item.route === '/runs/execute'), false);
  const phase = report.tasks[0];
  assert.equal(phase.driver.usage.input, 100); assert.equal(phase.driver.usage.output, 20);
  assert.equal(phase.answer, finalAnswer([assistant()]));
  assert.equal(phase.recorder_report.ledger[0].raw_worker_ack, 'not_sse');
  assert.equal(phase.filtered_application_events.length, 6);
  assert.equal(phase.evidence_correlation.passed, true);
  const original = '核对当前页面实际数据，并提供已观察到的证据。';
  const submitted = `/skill:browser-use ${BROWSER_HANDOFF}\n\n${original}`;
  assert.equal(requests.find(item => item.route.endsWith('/messages')).body.prompt, submitted);
  assert.equal(phase.original_task_prompt, original);
  assert.equal(phase.prompt, original);
  assert.equal(phase.submitted_prompt, submitted);
  assert.equal(phase.task_handoff, BROWSER_HANDOFF);
  assert.equal(phase.original_task_prompt_sha256, createHash('sha256').update(original).digest('hex'));
  assert.equal(phase.submitted_prompt_sha256, createHash('sha256').update(submitted).digest('hex'));
  assert.notEqual(phase.original_task_prompt_sha256, phase.submitted_prompt_sha256);
  assert.equal(report.tasks[1].task_handoff, BROWSER_HANDOFF);
  await assert.rejects(runAcceptance(config), /EEXIST/);
});

test('successful judge cannot mask a disallowed tool or failed cleanup', async t => {
  const violation = await exercise(t, 'model', { badTool: true });
  assert.equal(violation.report.passed, false);
  assert.ok(violation.report.tasks[0].driver.violations.includes('disallowed_tool:read'));
  const dirty = await exercise(t, 'smoke', { cleanupFails: true });
  assert.equal(dirty.report.passed, false);
});


test('SSE correlation rejects wrong run/session identity, injected ACKs, missing receipts and changed protocol data', () => {
  const events = [{ type: 'run_start', run_id: 'r1' }, { type: 'tool_start', name: 'browser', call_id: 'a1', args: { action: 'devtools', method: 'targets.list' } }, { type: 'tool_end', call_id: 'a1', details: { action: 'devtools', ok: true, devtools_data: { items: [] } } }, { type: 'run_end' }];
  const report = { studio_run_id: 'r1', studio_session_id: 's1', worker_session_id: 'w1', ledger: [{ sessionId: 'w1', call_id: 'w1:1', args: { action: 'devtools', method: 'targets.list', op_id: 'transport-only' }, result: { ok: true, data: { items: [] } } }] };
  const judge = (es = events, rs = report) => correlateEvidence(es, rs, 'r1', 's1', 'w1');
  assert.equal(judge().passed, true);
  assert.equal(judge([{ ...events[0], run_id: 'wrong' }, ...events.slice(1)]).passed, false);
  assert.equal(judge([...events.slice(0, -1), { type: 'run_end', run_id: 'wrong' }]).passed, false);
  assert.equal(judge(events, { ...report, worker_session_id: 'other' }).passed, false);
  assert.equal(judge(events, { ...report, ledger: [...report.ledger, report.ledger[0]] }).passed, false);
  assert.equal(judge(events.filter(event => event.type !== 'tool_end')).passed, false);
  const changed = structuredClone(events); changed[2].details.devtools_data.items.push('fabricated');
  assert.equal(judge(changed).passed, false);
});


test('correlation requires unique call IDs and exactly one receipt in the matching run interval', () => {
  const start = { type: 'tool_start', name: 'browser', call_id: 'a1', args: { action: 'snapshot' } };
  const end = { type: 'tool_end', call_id: 'a1', details: { action: 'snapshot', ok: true } };
  const begin = { type: 'run_start', run_id: 'r1' }, finish = { type: 'run_end' };
  const report = { studio_run_id: 'r1', studio_session_id: 's1', worker_session_id: 'w1', ledger: [{ sessionId: 'w1', call_id: 'w1:1', args: { action: 'snapshot' }, result: { ok: true } }] };
  const judge = (events, rs = report) => correlateEvidence(events, rs, 'r1', 's1', 'w1');
  assert.equal(judge([begin, start, end, finish]).passed, true);
  assert.equal(judge([begin, start, end, { type: 'tool_end', name: 'browser', call_id: 'orphan' }, finish]).passed, false);
  assert.equal(judge([begin, start, { ...end, name: 'read' }, finish]).passed, false);
  for (const events of [[start, begin, end, finish], [begin, start, finish, end], [begin, end, start, finish], [begin, start, end, end, finish], [begin, start, finish]]) assert.equal(judge(events).passed, false);
  const duplicated = { ...report, ledger: [report.ledger[0], { ...report.ledger[0], call_id: 'w1:2' }] };
  const reused = judge([begin, start, end, start, finish], duplicated);
  assert.equal(reused.passed, false);
  assert.ok(reused.failures.includes('duplicate_or_missing_application_call_id'));
  const secondStart = { ...start, call_id: 'a2' }, secondEnd = { ...end, call_id: 'a2' };
  assert.equal(judge([begin, start, end, secondStart, secondEnd, finish], duplicated).passed, true);
  assert.equal(judge([begin, start, end, secondStart, secondEnd, finish], { ...report, ledger: [report.ledger[0], report.ledger[0]] }).passed, false);
});
