import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { parseOptions, runSuite, authRedactor, safeAuthValue } from './runner.js';
import { TASKS, DATA, API_PATH, expectedOutcome, judgePhase, executedActions } from './tasks.js';
import { renderReport, summarize } from './report.js';

const state = counters => ({ scenario: 'cookie', counters: { loginSubmissions: 0, successfulLogins: 0, verificationSubmissions: 0, tokenCreates: 0, tokenRevokes: 0, labExpires: 0, labRevokes: 0, logoutSubmissions: 0, orderReads: 1, unauthorizedRequests: 0, ...counters }, activeSessions: 1, activeTokens: 0, pendingChallenges: 0 });
function events(action, result, ok = true) { return [{ type: 'browser_start', args: action }, { type: 'browser_end', ok, result }]; }
const validEvents = () => [...events({ action: 'auth_status' }, { text: '{"api_access":{"mode":"automatic","status":"credentials_present"}}' }), ...events({ action: 'api_read', path: API_PATH }, { text: JSON.stringify({ status: 200, data: { total: 1, orders: [{ id: DATA.order_id, amount: DATA.amount }] } }) })];

test('auth CLI stays separate and rejects unbounded, duplicate and missing model inputs', () => {
  assert.equal(TASKS.length, 8);
  assert.equal(parseOptions([]).mode, 'validate');
  assert.equal(parseOptions(['--mode', 'model', '--agent', './agent', '--model', 'glm-5.3', '--repeat', '3']).repeat, 3);
  for (const args of [['--repeat', '11'], ['--max-turns', '0'], ['--timeout-ms', '20'], ['--mode', 'model'], ['--control', 'fake'], ['--tasks', 'cookie-read,cookie-read'], ['--tasks', 'unknown'], ['--unknown', 'x'], ['--mode', 'model', '--agent', './agent', '--model', 'glm-5.3', '--control', 'noop']]) assert.throws(() => parseOptions(args));
});

test('model claims and page auto-fetch counters cannot replace real automatic API evidence', () => {
  const task = TASKS[0], before = state(), after = state({ orderReads: 2 }), outcome = expectedOutcome(task);
  assert.equal(judgePhase(task, { before, after, outcome, events: [], access: { mode: 'automatic', status: 'credentials_present' } }).success, false);
  assert.equal(judgePhase(task, { before, after, outcome, events: validEvents(), access: { mode: 'automatic', status: 'credentials_present' } }).success, true);
  for (const text of ['not JSON', JSON.stringify({ status: 200, data: { total: 1, orders: [{ id: 'wrong-order', amount: 8400 }] } }), JSON.stringify({ status: 401, data: { total: 1, orders: [{ id: DATA.order_id, amount: DATA.amount }] } })]) {
    const trace = validEvents(); trace.at(-1).result.text = text;
    assert.equal(judgePhase(task, { before, after, outcome, events: trace, access: { mode: 'automatic', status: 'credentials_present' } }).success, false);
  }
  const observed = executedActions(events({ action: 'batch', actions: [{ action: 'auth_status' }, { action: 'api_read', path: API_PATH }] }, { steps: [{ action: 'auth_status', ok: true }], text: 'stopped before second step' }));
  assert.equal(observed.length, 1);
});

test('synthetic dynamic tokens and provider keys are excluded and HTML never executes traces', () => {
  const redact = authRedactor({ PRIVATE_PROVIDER_KEY: 'dummy-provider-credential' }, 'dummy-worker-credential');
  const clean = redact(`dummy-provider-credential dummy-worker-credential mock_access_${'a'.repeat(48)} mock_session_${'b'.repeat(48)} mock_pat_${'c'.repeat(48)}`);
  assert.equal(clean.includes('credential'), false); assert.equal(clean.includes('mock_'), false);
  const nested = safeAuthValue({ [`mock_pat_${'d'.repeat(48)}`]: { 'dummy-provider-credential': 'opaque' } }, redact);
  assert.equal(JSON.stringify(nested).includes('mock_pat_'), false); assert.equal(JSON.stringify(nested).includes('dummy-provider-credential'), false);
  const report = { mode: 'validate', control: 'reference', config: {}, tasks: [{ title: '<script>alert(1)</script>', group: 'correct_handoff', repeat: 0, status: 'passed', simulated_human_interventions: 0, events: [], phases: [] }] };
  const html = renderReport(report);
  assert.equal(html.includes('<script>'), false); assert.match(html, /&lt;script&gt;/);
  assert.equal(summarize(report).groups.correct_handoff.model_rate, null);
});

test('automatic reference validates all eight cases; noop predicts the right answers but still fails every judge', { timeout: 60000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-auth-controls-'));
  try {
    for (const control of ['reference', 'noop']) {
      const options = parseOptions(['--control', control, '--out', path.join(dir, control)]);
      const { report } = await runSuite(options);
      assert.equal(report.infrastructure_error, undefined);
      assert.equal(report.tasks.length, 8);
      assert.ok(report.tasks.every(row => row.context_cleaned === true));
      assert.ok(report.tasks.every(row => row.status === (control === 'reference' ? 'passed' : 'failed')), JSON.stringify(report.tasks.map(row => ({ id: row.id, status: row.status, reason: row.reason }))));
      assert.equal(report.summary.model_judged_pass_rate, null);
      assert.equal(report.summary.simulated_human_interventions, 0);
      assert.ok(report.tasks.every(row => row.phases.length === 1));
      assert.equal(report.suite, 'browser-auth-auto-v2');
      assert.equal(report.config.manual_pogo_authorization, 'absent');
      assert.equal(/mock_(?:access|session|pat)_[a-f0-9]{48}/.test(await readFile(path.join(dir, control, 'summary.json'), 'utf8')), false);
    }
    await assert.rejects(runSuite(parseOptions(['--out', path.join(dir, 'reference')])), { code: 'EEXIST' });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('model-mode adapter must use auth profile, actual browser requests and structured evidence', { timeout: 30000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-auth-fake-adapter-'));
  try {
    const executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}\nlet text='';process.stdin.on('data',part=>text+=part);process.stdin.on('end',async()=>{const i=JSON.parse(text);let op=0;const call=async args=>{const r=await fetch(i.worker_url+'/sessions/'+i.session_id+'/execute',{method:'POST',headers:{Authorization:'Bearer '+process.env.POGO_BROWSER_EVAL_TOKEN,'Content-Type':'application/json'},body:JSON.stringify({op_id:'fake-'+(++op),...args})});return r.json()};await call({action:'snapshot'});const s=JSON.parse((await call({action:'auth_status'})).text);const r=JSON.parse((await call({action:'api_read',path:${JSON.stringify(API_PATH)}})).text);const row=r.data.orders[0];const outcome={status:'completed',reason:'api_read_complete',data:{order_id:row.id,amount:row.amount}};console.error(process.env.DUMMY_API_KEY);console.log(JSON.stringify({type:'end',answer:JSON.stringify(outcome),outcome,end_reason:'completed',profile:'pogo-agent-browser-auth-auto-v2',usage:{input:20,output:10},usage_measured:true}));});\n`, { mode: 0o700 });
    const output = path.join(dir, 'output'), options = parseOptions(['--mode', 'model', '--agent', executable, '--model', 'fake-local-only', '--tasks', 'cookie-read', '--out', output]);
    options.environment = { PATH: process.env.PATH, HOME: process.env.HOME, DUMMY_API_KEY: 'fake-model-private-key' };
    const { report } = await runSuite(options);
    assert.equal(report.tasks[0].status, 'passed', report.tasks[0].reason);
    assert.equal(report.summary.groups.existing_login.model_rate, 1);
    assert.deepEqual(report.summary.usage, { input: 20, output: 10 });
    for (const file of ['summary.json', 'report.html', report.tasks[0].trace_path]) assert.equal((await readFile(path.join(output, file), 'utf8')).includes('fake-model-private-key'), false);
    // Metadata is untrusted too: a malformed adapter must not leak a secret
    // merely by placing it in profile/end_reason rather than answer text.
    const malformed = (await readFile(executable, 'utf8')).replace("profile:'pogo-agent-browser-auth-auto-v2'", 'profile:process.env.DUMMY_API_KEY').replace("end_reason:'completed'", 'end_reason:process.env.DUMMY_API_KEY');
    await writeFile(executable, malformed, { mode: 0o700 });
    const badOptions = { ...options, outputDir: path.join(dir, 'bad-metadata') };
    const rejected = (await runSuite(badOptions)).report;
    assert.equal(rejected.tasks[0].status, 'failed');
    for (const file of ['summary.json', 'report.html', rejected.tasks[0].trace_path]) assert.equal((await readFile(path.join(badOptions.outputDir, file), 'utf8')).includes('fake-model-private-key'), false);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('pre-cancellation keeps the selected denominator and never starts model work', { timeout: 10000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-auth-cancel-'));
  try {
    const controller = new AbortController(); controller.abort();
    const { report } = await runSuite(parseOptions(['--tasks', 'cookie-read,bearer-read', '--out', path.join(dir, 'result')]), { abortSignal: controller.signal });
    assert.equal(report.cancelled, true); assert.equal(report.summary.total, 2); assert.equal(report.summary.skipped, 2);
  } finally { await rm(dir, { recursive: true, force: true }); }
});


test('automatic judge rejects skipped login, unexpected PAT mutations and negative-case API retries', () => {
  const task = TASKS.find(item => item.id === 'cookie-login-read');
  const before = { ...state(), activeSessions: 0 }, after = state({ orderReads: 3, loginSubmissions: 1, successfulLogins: 1 });
  const evidence = { before, after, outcome: expectedOutcome(task), events: validEvents(), access: { mode: 'automatic' } };
  assert.equal(judgePhase(task, evidence).success, true);
  assert.equal(judgePhase(task, { ...evidence, after: state({ orderReads: 2 }) }).success, false);
  assert.equal(judgePhase(task, { ...evidence, after: { ...after, counters: { ...after.counters, tokenCreates: 1 } } }).success, false);
  for (const id of ['wrong-password', 'mfa-handoff']) {
    const negative = TASKS.find(item => item.id === id), start = { ...state(), activeSessions: 0, pendingChallenges: 0 };
    const finish = { ...start, pendingChallenges: id === 'mfa-handoff' ? 1 : 0, counters: { ...start.counters, loginSubmissions: 1 } };
    const base = { before: start, after: finish, outcome: expectedOutcome(negative), events: [] };
    assert.equal(judgePhase(negative, base).success, true);
    assert.equal(judgePhase(negative, { ...base, events: events({ action: 'api_read', path: API_PATH }, { error: 'AUTH_LOGIN_EXPIRED' }, false) }).success, false);
  }
});

test('correct business data cannot hide a switched login scenario or credential type', () => {
  for (const task of TASKS.filter(item => item.status === 'completed')) {
    const logins = task.setup === 'login' ? 1 : 0, bearer = task.scenario === 'bearer';
    const before = state(), after = { ...state({ orderReads: 3, loginSubmissions: logins, successfulLogins: logins }), scenario: task.scenario, activeSessions: bearer ? 0 : 1, activeTokens: bearer ? 1 : 0 };
    const base = { before, after, outcome: expectedOutcome(task), events: validEvents() };
    assert.equal(judgePhase(task, base).success, true, task.id);
    for (const changed of [{ scenario: bearer ? 'cookie' : 'bearer' }, { activeSessions: bearer ? 1 : 0, activeTokens: bearer ? 0 : 1 }, { scenario: undefined }]) {
      const result = judgePhase(task, { ...base, after: { ...after, ...changed } });
      assert.equal(result.success, false, task.id); assert.ok(result.failures.includes('login_scenario_or_credential_type_mismatch'));
    }
  }
});
