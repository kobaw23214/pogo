import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, access, rm } from 'node:fs/promises';
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

test('retired live runners reject validate, model and cancelled runs before creating artifacts', async () => {
  const { runSuite: runHistoricalManualSuite } = await import('../auth/runner.js');
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-auth-retired-'));
  try {
    for (const [profile, run] of [['auth', runHistoricalManualSuite], ['auth-auto', runSuite]]) {
      for (const mode of ['validate', 'model']) {
        const outputDir = path.join(dir, profile + '-' + mode);
        let progress = false;
        const controller = new AbortController(); controller.abort();
        await assert.rejects(run({ mode, outputDir, agent: '/does/not/exist', model: 'unused' }, {
          onProgress: () => { progress = true; }, abortSignal: controller.signal,
        }), { code: 'RETIRED_PROFILE' });
        assert.equal(progress, false);
        await assert.rejects(access(outputDir), { code: 'ENOENT' });
      }
    }
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
