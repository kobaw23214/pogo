import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { parseOptions, runSuite, createTrustedLedger, publicValue, requestAllowed, adapterFailures, PROFILE, suiteSourceManifest } from './runner.js';
import { TASKS, createFixtures } from './fixtures.js';
import { expectedSkillManifest } from '../general-web/runner.js';
import { makeRedactor } from '../transport.js';

const budgets = ['--max-turns', '20', '--token-budget', '1000', '--timeout-ms', '30000', '--max-steps', '40'];

test('model calls require an explicit executable, model and bounded budgets', () => {
  assert.equal(TASKS.length, 5); assert.equal(parseOptions([]).mode, 'validate');
  assert.equal(parseOptions(['--mode', 'model', '--agent', '/tmp/agent', '--model', 'offline', ...budgets]).mode, 'model');
  for (const args of [['--mode', 'model'], ['--mode', 'model', '--agent', '/tmp/agent', '--model', 'offline'],
    ['--max-turns', '0'], ['--token-budget', '200001'], ['--timeout-ms', '999'], ['--max-steps', '201'], ['--repeat', '11'], ['--seed', '-1'],
    ['--seed', '4294967296'], ['--tasks', 'missing'], ['--tasks', `${TASKS[0].id},`], ['--tasks', `${TASKS[0].id},${TASKS[0].id}`],
    ['--mode', 'model', '--agent', '/tmp/agent', '--model', 'offline', ...budgets, '--control', 'reference'], ['--seed', '1', '--seed', '2']]) assert.throws(() => parseOptions(args));
});

test('task network restriction admits declared OOPIF origin and case paths only', () => {
  const task = { startURL: 'http://127.0.0.1:1234/cases/a/', allowedOrigins: ['http://127.0.0.1:1234', 'http://localhost:1234'], allowedPathPrefixes: ['/cases/a/'] };
  for (const url of ['http://127.0.0.1:1234/cases/a/', 'http://localhost:1234/cases/a/worker.js']) assert.equal(requestAllowed(url, task), true);
  for (const url of ['http://localhost:4567/cases/a/', 'http://localhost:1234/cases/b/', 'http://localhost:1234/cases/a/oracle',
    'http://localhost:1234/cases/a/%65val/answer', 'https://example.com/', 'file:///tmp/result', 'http://user:pass@localhost:1234/cases/a/']) assert.equal(requestAllowed(url, task), false, url);
});

test('trusted ledger copies proxy acknowledgements, excludes preparation and rejects duplicate IDs', () => {
  const ledger = createTrustedLedger(), exchange = { sessionId: 's', artifactId: 'a', call_id: 's:1', call: 1, args: { action: 'snapshot' }, result: { ok: true, text: 'real' }, http_status: 200, ok: true };
  ledger.onExchange(exchange); assert.deepEqual(ledger.snapshot('s'), []);
  ledger.register('s', 'a'); ledger.onExchange({ ...exchange, artifactId: 'other' }); assert.deepEqual(ledger.snapshot('s'), []);
  ledger.onExchange(exchange); exchange.result.text = 'forged';
  const snapshot = ledger.snapshot('s'); assert.equal(snapshot[0].result.text, 'real'); snapshot[0].result.text = 'changed';
  assert.equal(ledger.snapshot('s')[0].result.text, 'real');
  assert.throws(() => ledger.onExchange(exchange), /duplicate/);
  ledger.unregister('s'); assert.deepEqual(ledger.snapshot('s'), []);
});

test('public artifacts strip credential keys, redact secret keys and discard screenshot bytes', () => {
  const redact = makeRedactor({ DUMMY_KEY: 'fixture-private-value' }, ['worker-private-value']);
  const clean = publicValue({ 'fixture-private-value': { cookie: 'session-value', password: 'pw-value', token: 'token-value', screenshot: { data: 'pixels', version: 1 }, text: 'Bearer bearer-value worker-private-value' } }, redact);
  const json = JSON.stringify(clean);
  for (const secret of ['fixture-private-value', 'worker-private-value', 'session-value', 'pw-value', 'token-value', 'bearer-value', 'pixels']) assert.equal(json.includes(secret), false);
  assert.match(json, /version/);
});

test('adapter validation fails closed for stale profiles, incomplete usage and budget failures', () => {
  const skill = { expected_shared_skill_body_sha256: 'c'.repeat(64) }, options = { tokenBudget: 100 };
  const good = { end: { profile: PROFILE, end_reason: 'completed', system_prompt_sha256: 'a'.repeat(64), tool_schema_sha256: 'b'.repeat(64), shared_skill_body_sha256: 'c'.repeat(64) }, exitCode: 0, usage: { input: 10, output: 10 }, usage_partial: false };
  assert.deepEqual(adapterFailures(good, options, skill), []);
  for (const [change, expected] of [[{ end: { ...good.end, profile: 'pogo-agent-general-web-v1' } }, 'wrong_or_missing_adapter_profile'],
    [{ end: { ...good.end, shared_skill_body_sha256: 'd'.repeat(64) } }, 'shared_skill_body_mismatch'], [{ end: { ...good.end, tool_schema_sha256: null } }, 'missing_or_invalid_profile_fingerprints'],
    [{ timedOut: true }, 'adapter_not_completed'], [{ usage_partial: true }, 'missing_complete_measured_token_usage'], [{ usage: { input: 101, output: 2 } }, 'token_budget_exceeded'],
    [{ usage: { input: NaN, output: 2 } }, 'missing_complete_measured_token_usage']]) assert.ok(adapterFailures({ ...good, ...change }, options, skill).includes(expected));
});

test('manifest identifies the separate fixtures and judge', async () => {
  const manifest = await suiteSourceManifest();
  for (const key of ['suite_source_sha256', 'fixture_sha256', 'judge_sha256', 'task_catalog_sha256']) assert.match(manifest[key], /^[a-f0-9]{64}$/);
  assert.ok(manifest.suite_files_sha256['runner.js']); assert.ok(manifest.suite_files_sha256['reference.js']);
});

test('pre-cancelled runs retain every planned task and refuse existing artifact directories', { timeout: 15000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-cancelled-'));
  try {
    const cancellation = new AbortController(); cancellation.abort();
    const options = parseOptions(['--out', path.join(dir, 'run')]);
    const { report } = await runSuite(options, { abortSignal: cancellation.signal });
    assert.equal(report.cancelled, true); assert.equal(report.summary.planned, 5); assert.equal(report.summary.skipped, 5);
    assert.equal(report.summary.run_valid, false); assert.ok(report.tasks.every(row => row.context_cleaned === null));
    await assert.rejects(runSuite(options), { code: 'EEXIST' });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('real Chromium reference passes while exact-answer and fabricated-event controls fail', { timeout: 180000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-controls-'));
  try {
    for (const control of ['reference', 'noop', 'fabricated']) {
      const { report, outputDir } = await runSuite(parseOptions(['--control', control, '--seed', '52719', '--out', path.join(dir, control)]));
      assert.equal(report.infrastructure_error, undefined);
      assert.equal(report.summary.planned, 5); assert.equal(report.summary.model_judged_pass_rate, null);
      assert.ok(report.tasks.every(row => row.context_cleaned === true));
      assert.ok(report.tasks.every(row => row.status === (control === 'reference' ? 'passed' : 'failed')),
        JSON.stringify(report.tasks.map(row => ({ id: row.id, status: row.status, reason: row.reason }))));
      assert.equal(report.config.profile, PROFILE); assert.equal(report.config.adapter_events_trusted, false);
      assert.equal(report.summary.run_valid, true);
      if (control === 'reference') assert.equal(report.summary.reference_control_pass_rate, 1);
      else {
        assert.ok(report.tasks.every(row => row.ledger.length === 0));
        assert.equal(report.summary[control === 'noop' ? 'noop_false_positive_rate' : 'fabricated_false_positive_rate'], 0);
      }
      if (control === 'fabricated') assert.ok(report.tasks.every(row => row.adapter_events.length > 0));
      const saved = JSON.parse(await readFile(path.join(outputDir, 'summary.json'), 'utf8'));
      assert.equal(saved.suite, 'browser-use-v2'); assert.match(saved.config.runtime_source_sha256, /^[a-f0-9]{64}$/);
      for (const row of saved.tasks) {
        const trace = JSON.parse(await readFile(path.join(outputDir, row.trace_path), 'utf8'));
        assert.deepEqual(trace.ledger, row.ledger); assert.deepEqual(trace.adapter_events, row.adapter_events);
      }
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('adapter stdout cannot manufacture worker evidence despite valid metadata and exact answer', { timeout: 40000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-fake-adapter-'));
  try {
    const seed = 813, fixture = await createFixtures({ seed });
    let answer;
    try { answer = await fixture.expectedAnswer(TASKS[0].id); } finally { await fixture.close(); }
    const skill = await expectedSkillManifest(), executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}
let input='';process.stdin.on('data',chunk=>input+=chunk);process.stdin.on('end',()=>{
 const request=JSON.parse(input); if(Object.keys(request).some(key=>/oracle|expected|selector|reference/.test(key)))process.exit(4);
 console.log(JSON.stringify({type:'tool_end',tool:'browser',ok:true,result:{ok:true,text:${JSON.stringify(answer)},origin:'worker',call_id:request.session_id+':1'}}));
 console.log(JSON.stringify({type:'end',profile:${JSON.stringify(PROFILE)},end_reason:'completed',answer:${JSON.stringify(answer)},system_prompt_sha256:'a'.repeat(64),tool_schema_sha256:'b'.repeat(64),shared_skill_body_sha256:${JSON.stringify(skill.expected_shared_skill_body_sha256)},usage_measured:true,usage:{input:11,output:7}}));
});
`, { mode: 0o700 });
    const { report } = await runSuite(parseOptions(['--mode', 'model', '--agent', executable, '--model', 'offline-test-adapter', ...budgets, '--seed', String(seed), '--tasks', TASKS[0].id, '--out', path.join(dir, 'run')]));
    assert.equal(report.infrastructure_error, undefined); assert.equal(report.tasks[0].status, 'failed');
    assert.equal(report.tasks[0].context_cleaned, true); assert.equal(report.tasks[0].ledger.length, 0);
    assert.ok(report.tasks[0].adapter_events.length > 0); assert.match(report.tasks[0].reason, /no_real_successful_browser_action/);
    assert.equal(report.summary.model_judged_pass_rate, 0);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('requested-step exhaustion fails closed and still confirms context cleanup', { timeout: 30000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-step-limit-'));
  try {
    const { report } = await runSuite(parseOptions(['--tasks', TASKS[0].id, '--max-steps', '1', '--out', path.join(dir, 'run')]));
    const row = report.tasks[0];
    assert.equal(row.context_cleaned, true); assert.equal(row.step_budget_exceeded, true);
    assert.notEqual(row.status, 'passed'); assert.match(row.reason, /step_budget_exceeded/);
    assert.equal(row.ledger.length, 1); assert.equal(report.summary.reference_control_pass_rate, 0);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('task deadline stops an offline adapter and excludes incomplete usage from success', { timeout: 15000, skip: process.platform === 'win32' }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-time-limit-'));
  try {
    const executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}\nprocess.on('SIGTERM',()=>{});setInterval(()=>{},1000);\n`, { mode: 0o700 });
    const settings = budgets.map((value, index) => budgets[index - 1] === '--timeout-ms' ? '1000' : value);
    const { report } = await runSuite(parseOptions(['--mode', 'model', '--agent', executable, '--model', 'offline-test-adapter', ...settings, '--tasks', TASKS[0].id, '--out', path.join(dir, 'run')]));
    const row = report.tasks[0];
    assert.equal(row.status, 'error'); assert.match(row.reason, /task_time_budget_exceeded/);
    assert.equal(row.context_cleaned, true); assert.equal(report.summary.model_judged_pass_rate, 0);
    assert.equal(report.summary.run_valid, false);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('active cancellation reaps an offline adapter and preserves later tasks as skipped', { timeout: 45000, skip: process.platform === 'win32' }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-browser-use-v2-active-cancel-'));
  const cancellation = new AbortController(); let pending, pid;
  try {
    const marker = path.join(dir, 'pid'), executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}\nimport {writeFileSync} from 'node:fs';writeFileSync(${JSON.stringify(marker)},String(process.pid));process.on('SIGTERM',()=>{});setInterval(()=>{},1000);\n`, { mode: 0o700 });
    pending = runSuite(parseOptions(['--mode', 'model', '--agent', executable, '--model', 'offline-test-adapter', ...budgets, '--tasks', TASKS.slice(0, 2).map(task => task.id).join(','), '--out', path.join(dir, 'run')]), { abortSignal: cancellation.signal });
    const until = Date.now() + 15000;
    while (!pid && Date.now() < until) { try { pid = Number(await readFile(marker, 'utf8')); } catch { await new Promise(resolve => setTimeout(resolve, 25)); } }
    assert.ok(pid, 'offline adapter did not start'); cancellation.abort();
    const { report } = await pending;
    assert.equal(report.cancelled, true); assert.equal(report.tasks[0].context_cleaned, true);
    assert.equal(report.tasks[0].status, 'error'); assert.equal(report.tasks[1].status, 'skipped');
    assert.equal(report.summary.planned, 2); assert.equal(report.summary.model_judged_pass_rate, 0);
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  } finally {
    cancellation.abort(); if (pending) await pending;
    if (pid) { try { process.kill(-pid, 'SIGKILL'); } catch { /* Already reaped by the adapter watchdog. */ } }
    await rm(dir, { recursive: true, force: true });
  }
});
