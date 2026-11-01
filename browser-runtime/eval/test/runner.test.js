import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { parseOptions, runSuite } from '../runner.js';
import { TASKS } from '../fixtures/index.js';
import { makeRedactor, safeValue } from '../transport.js';

let temporary;
before(async () => { temporary = await mkdtemp(path.join(tmpdir(), 'pogo-eval-runner-test-')); });
after(async () => { if (temporary) await rm(temporary, { recursive: true, force: true }); });

test('parseOptions defaults to reference validation and normalizes explicit model configuration', () => {
  const defaults = parseOptions([]);
  assert.equal(defaults.mode, 'fixture-validation');
  assert.equal(defaults.control, 'reference');
  assert.equal(defaults.model, null);
  assert.equal(defaults.repeat, 1);
  assert.equal(defaults.captureFrames, true);
  const model = parseOptions(['--mode', 'model', '--model', 'fake-local-model', '--agent', './fake-agent', '--providers', './fake-providers', '--tasks', 'spa-a,pages-b', '--repeat', '2', '--concurrency', '1', '--max-turns', '12', '--token-budget', '200', '--total-tokens', '1000', '--timeout-ms', '3000', '--total-time-ms', '5000', '--seed', '9', '--no-frames']);
  assert.equal(model.mode, 'model-evaluation');
  assert.equal(model.agent, path.resolve('fake-agent'));
  assert.equal(model.providers, path.resolve('fake-providers'));
  assert.deepEqual(model.tasks, ['spa-a', 'pages-b']);
  assert.equal(model.repeat, 2);
  assert.equal(model.tokenBudget, 200);
  assert.equal(model.captureFrames, false);
});

test('parseOptions rejects missing configuration and out-of-bound or fractional budgets', () => {
  for (const argv of [
    ['--mode', 'model'], ['--mode', 'model', '--model', 'fake'], ['--mode', 'unknown'],
    ['--control', 'pretend-success'], ['--model'], ['--no-such-option', 'x'],
    ['--repeat', '0'], ['--repeat', '11'], ['--repeat', '1.5'], ['--concurrency', '5'],
    ['--seed', '-1'], ['--token-budget', 'NaN'], ['--timeout-ms', '999'], ['--total-tokens', '-1'],
    ['--mode', 'model', '--model', 'fake', '--agent', '/fake', '--control', 'noop'],
  ]) assert.throws(() => parseOptions(argv), undefined, argv.join(' '));
  assert.equal(parseOptions(['--mode', 'model', '--help']).help, true);
  assert.equal(parseOptions(['--list']).list, true);
});

test('trace redaction removes environment secrets, authorization, image bytes and URL credentials', () => {
  const secret = 'local-test-provider-secret-29403';
  const redact = makeRedactor({ EXAMPLE_API_KEY: secret, NORMAL_CONFIG: 'visible' }, ['worker-test-secret-88002']);
  const value = safeValue({
    text: `key ${secret}; Bearer unknown-test-bearer; http://testuser:testpass@localhost/path?token=query-secret&api_key=another-secret`,
    nested: { authorization: 'Bearer hidden', lease_token: 'hidden', headers: { 'X-Key': 'hidden' }, text: 'worker-test-secret-88002' },
    screenshot: { data: 'secret-image-payload', mime: 'image/jpeg', width: 1280, height: 800 },
  }, redact);
  assert.doesNotMatch(JSON.stringify(value), /local-test-provider-secret|worker-test-secret|unknown-test-bearer|testuser|testpass|query-secret|another-secret|secret-image-payload/);
  assert.deepEqual(value.nested, { text: '[REDACTED]' });
  assert.equal(value.screenshot.mime, 'image/jpeg');
  assert.equal(value.screenshot.data, undefined);
});

test('noop negative control cannot pass any task by claiming success', { timeout: 30_000 }, async () => {
  const options = parseOptions(['--control', 'noop', '--no-frames', '--concurrency', '2', '--out', path.join(temporary, 'noop')]);
  const { report } = await runSuite(options);
  assert.equal(report.infrastructure_error, undefined);
  assert.equal(report.tasks.length, TASKS.length);
  assert.ok(report.tasks.every(row => row.status === 'failed' && row.success === false && row.context_cleaned));
  assert.ok(report.tasks.every(row => row.steps === 0));
  assert.equal(report.summary.passed, 0);
  assert.equal(report.summary.pass_rate, 0);
  assert.equal(report.summary.success_rate, null, 'fixture runs must never be presented as model scores');
  assert.equal(report.summary.total, 20);
});

test('total time budget retains every unstarted task in the denominator', { timeout: 30_000 }, async () => {
  const options = parseOptions(['--no-frames', '--total-time-ms', '1', '--tasks', 'catalog-a,registration-a', '--repeat', '2', '--out', path.join(temporary, 'time-budget')]);
  const { report } = await runSuite(options);
  assert.equal(report.tasks.length, 4);
  assert.ok(report.tasks.every(row => row.status === 'skipped'));
  assert.equal(report.summary.total, 4);
  assert.equal(report.summary.skipped, 4);
  assert.equal(report.summary.pass_rate, 0);
  assert.equal(report.summary.attempted_success_rate, null);
});

test('fake model claims fail business judging and token stop preserves skipped denominator', { timeout: 30_000 }, async () => {
  const executable = path.join(temporary, 'fake-adapter');
  const secret = 'runner-test-provider-key-778501';
  await writeFile(executable, `#!${process.execPath}\nlet input='';process.stdin.on('data',c=>input+=c);process.stdin.on('end',()=>{JSON.parse(input);console.error('diagnostic '+process.env.DUMMY_API_KEY);console.log(JSON.stringify({type:'message',text:'trace '+process.env.DUMMY_API_KEY,usage:{input:80,output:20}}));console.log(JSON.stringify({type:'end',answer:'I completed the task '+process.env.DUMMY_API_KEY,end_reason:'complete',usage:{input:80,output:20},usage_measured:true}))});\n`, { mode: 0o700 });
  const output = path.join(temporary, 'fake-model');
  const options = parseOptions(['--mode', 'model', '--model', 'fake-local-model', '--agent', executable, '--tasks', 'catalog-a,registration-a', '--concurrency', '1', '--total-tokens', '50', '--no-frames', '--out', output]);
  options.environment = { PATH: process.env.PATH, HOME: process.env.HOME, DUMMY_API_KEY: secret };
  const { report } = await runSuite(options);
  assert.equal(report.infrastructure_error, undefined);
  assert.equal(report.tasks[0].status, 'failed');
  assert.equal(report.tasks[0].goal_satisfied, false);
  assert.equal(report.tasks[1].status, 'skipped');
  assert.equal(report.summary.total, 2);
  assert.equal(report.summary.eligible, 1);
  assert.equal(report.summary.success_rate, 0);
  assert.equal(report.summary.usage.input, 80);
  assert.equal(report.summary.usage.output, 20);
  for (const filename of ['summary.json', 'report.html', report.tasks[0].trace_path]) {
    const content = await readFile(path.join(output, filename), 'utf8');
    assert.equal(content.includes(secret), false, `${filename} leaked a dummy environment secret`);
  }
  assert.equal(JSON.stringify(report).includes(secret), false);
  await assert.rejects(runSuite(options), { code: 'EEXIST' }, 'existing reports must never be overwritten');
});

test('unknown task IDs fail before creating a run directory', async () => {
  const options = parseOptions(['--tasks', 'does-not-exist', '--out', path.join(temporary, 'invalid')]);
  await assert.rejects(runSuite(options), /unknown or empty task list/);
});

async function waitForFile(filename, timeoutMs = 10_000, checkProcess) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await checkProcess?.();
    try { return await readFile(filename, 'utf8'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  throw new Error(`test process did not become ready: ${path.basename(filename)}`);
}

async function waitingAdapter(name) {
  const executable = path.join(temporary, name), marker = executable + '.ready';
  await writeFile(executable, `#!${process.execPath}\nconst fs=require('node:fs');let input='';process.stdin.on('data',c=>input+=c);process.stdin.on('end',()=>{JSON.parse(input);process.on('SIGTERM',()=>{});console.log(JSON.stringify({type:'message',usage:{input:6,output:2}}));fs.writeFileSync(${JSON.stringify(marker)},String(process.pid));setInterval(()=>{},1000)});\n`, { mode: 0o700 });
  return { executable, marker };
}

test('runner cancellation terminates its active model, skips new tasks and saves the report', { timeout: 20_000 }, async () => {
  const { executable, marker } = await waitingAdapter('cancel-adapter');
  const output = path.join(temporary, 'cancelled-run');
  const options = parseOptions(['--mode', 'model', '--model', 'fake-local-model', '--agent', executable, '--tasks', 'catalog-a,registration-a', '--concurrency', '1', '--no-frames', '--out', output]);
  const controller = new AbortController(), starts = [];
  const pending = runSuite(options, { abortSignal: controller.signal, onProgress: event => { if (event.type === 'task_start') starts.push(event.id); } });
  let pid;
  try {
    pid = Number(await waitForFile(marker));
    controller.abort(new Error('test cancellation'));
    const { report } = await pending;
    assert.deepEqual(starts, ['catalog-a']);
    assert.equal(report.cancelled, true);
    assert.equal(report.tasks[0].status, 'error');
    assert.match(report.tasks[0].reason, /cancelled/);
    assert.equal(report.tasks[0].context_cleaned, true);
    assert.equal(report.tasks[0].usage_partial, true);
    assert.equal(report.tasks[1].status, 'skipped');
    assert.match(report.tasks[1].reason, /cancelled before task start/);
    assert.equal(report.summary.total, 2);
    assert.equal(report.summary.success_rate, 0);
    assert.equal(JSON.parse(await readFile(path.join(output, 'summary.json'), 'utf8')).cancelled, true);
    assert.match(await readFile(path.join(output, 'report.html'), 'utf8'), /evaluation cancelled/);
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  } finally {
    controller.abort();
    await pending;
    if (pid) { try { process.kill(pid, 'SIGKILL'); } catch {} }
  }
});

test('pre-cancelled runner preserves all tasks as skipped without starting a case', { timeout: 10_000 }, async () => {
  const options = parseOptions(['--tasks', 'catalog-a,registration-a', '--no-frames', '--out', path.join(temporary, 'pre-cancelled')]);
  const controller = new AbortController(); controller.abort();
  const starts = [];
  const { report } = await runSuite(options, { abortSignal: controller.signal, onProgress: event => starts.push(event) });
  assert.deepEqual(starts, []);
  assert.equal(report.cancelled, true);
  assert.equal(report.summary.total, 2);
  assert.equal(report.summary.skipped, 2);
  assert.ok(report.tasks.every(row => row.status === 'skipped' && row.reason.includes('cancelled')));
});

test('CLI SIGTERM drains the active adapter and writes a cancelled report before exiting', { timeout: 25_000, skip: process.platform === 'win32' }, async () => {
  const { executable, marker } = await waitingAdapter('cli-cancel-adapter');
  const output = path.join(temporary, 'cli-cancelled-run');
  const cli = fileURLToPath(new URL('../run.js', import.meta.url));
  const child = spawn(process.execPath, [cli, '--mode', 'model', '--model', 'fake-local-model', '--agent', executable, '--tasks', 'catalog-a,registration-a', '--concurrency', '1', '--no-frames', '--out', output], { stdio: ['ignore', 'pipe', 'pipe'], detached: true,
    // The official Linux image installs browsers outside HOME. Preserve that
    // runtime location without inheriting provider credentials or model config.
    env: Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'SYSTEMROOT', 'PLAYWRIGHT_BROWSERS_PATH'].filter(key => process.env[key]).map(key => [key, process.env[key]])) });
  let stdout = '', stderr = '', adapterPID, childOutcome;
  child.stdout.setEncoding('utf8'); child.stderr.setEncoding('utf8');
  child.stdout.on('data', part => { stdout += part; }); child.stderr.on('data', part => { stderr += part; });
  const exited = new Promise(resolve => {
    child.once('error', error => { childOutcome = { error: error.message }; resolve(childOutcome); });
    child.once('close', (code, signal) => { childOutcome ??= { code, signal }; resolve(childOutcome); });
  });
  let watchdog;
  try {
    adapterPID = Number(await waitForFile(marker, 10_000, async () => {
      if (!childOutcome) return;
      // Browser setup failures are written to the report, sometimes with no
      // stderr. Do not obscure an early CLI exit as an adapter-ready timeout.
      let infrastructureError = '';
      try { infrastructureError = JSON.parse(await readFile(path.join(output, 'summary.json'), 'utf8')).infrastructure_error ?? ''; } catch {}
      throw new Error(`CLI exited before adapter readiness: ${JSON.stringify(childOutcome)}\nCLI stderr: ${stderr.slice(-8000) || '(empty)'}\nInfrastructure error: ${String(infrastructureError).slice(-8000) || '(none recorded)'}`);
    }));
    child.kill('SIGTERM');
    const outcome = await Promise.race([exited, new Promise((_, reject) => { watchdog = setTimeout(() => reject(new Error('CLI did not finish cancellation')), 10_000); })]);
    assert.deepEqual(outcome, { code: 1, signal: null }, stderr);
    const report = JSON.parse(await readFile(path.join(output, 'summary.json'), 'utf8'));
    assert.equal(report.cancelled, true);
    assert.equal(report.tasks[0].context_cleaned, true);
    assert.equal(report.tasks[1].status, 'skipped');
    assert.equal(report.summary.total, 2);
    assert.match(stdout, /"type":"run_end"/);
    assert.throws(() => process.kill(adapterPID, 0), { code: 'ESRCH' });
  } finally {
    clearTimeout(watchdog);
    if (child.exitCode === null) { try { process.kill(-child.pid, 'SIGKILL'); } catch {} }
    if (adapterPID) { try { process.kill(-adapterPID, 'SIGKILL'); } catch {} }
    await exited;
  }
});
