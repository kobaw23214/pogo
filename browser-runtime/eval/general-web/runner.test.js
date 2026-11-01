import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { parseOptions, runSuite, safeGeneralValue, requestAllowed, executedActions, PROFILE, suiteSourceManifest, sharedSkillBody, expectedSkillManifest } from './runner.js';
import { TASKS, createGeneralWebFixtures } from './fixtures/index.js';
import { makeRedactor } from '../transport.js';
import { renderReport, summarize } from './report.js';

test('general web CLI rejects missing model settings, invalid budgets and duplicate selections', () => {
  assert.equal(TASKS.length, 9); assert.equal(parseOptions([]).mode, 'validate');
  assert.equal(parseOptions(['--seed', '7', '--repeat', '2']).seed, 7);
  assert.equal(parseOptions(['--mode', 'model', '--agent', './agent', '--model', 'glm-5.3']).mode, 'model');
  for (const args of [['--seed', '-1'], ['--seed', '4294967296'], ['--repeat', '11'], ['--max-turns', '0'], ['--token-budget', '200001'],
    ['--timeout-ms', '20'], ['--max-steps', '101'], ['--mode', 'model'], ['--control', 'fake'], ['--tasks', 'unknown'],
    ['--tasks', `${TASKS[0].id},${TASKS[0].id}`], ['--unknown', 'x'], ['--mode', 'model', '--agent', './agent', '--model', 'x', '--control', 'noop']]) assert.throws(() => parseOptions(args));
});

test('network boundary allows only the current site case and blocks eval/oracle/controller paths', () => {
  const task = { startURL: 'http://127.0.0.1:1234/cases/a/', allowedOrigins: ['http://127.0.0.1:1234'], allowedPathPrefixes: ['/cases/a/'] };
  for (const url of ['http://127.0.0.1:1234/cases/a/', 'http://127.0.0.1:1234/cases/a/details?q=good']) assert.equal(requestAllowed(url, task), true);
  for (const url of ['http://127.0.0.1:1234/cases/b/', 'http://127.0.0.1:5678/cases/a/', 'http://localhost:1234/cases/a/',
    'http://127.0.0.1:1234/cases/a/oracle/answer', 'http://127.0.0.1:1234/cases/a/controller', 'http://127.0.0.1:1234/cases/a/%65val/data',
    'http://127.0.0.1:1234/cases/a/__judge', 'http://127.0.0.1:1234/cases/a/../../oracle', 'https://example.com/', 'file:///tmp/a',
    'http://user:password@127.0.0.1:1234/cases/a/']) assert.equal(requestAllowed(url, task), false, url);
});

test('only acknowledged batch children count; requested submits are not successful evidence', () => {
  const result = executedActions([{ type: 'browser_start', args: { action: 'batch', actions: [{ action: 'snapshot' }, { action: 'click', target: '#submit' }] } },
    { type: 'browser_end', ok: false, result: { steps: [{ action: 'snapshot', ok: true }], text: 'stopped before submit', url: 'http://site/' } }]);
  assert.equal(result.length, 1); assert.equal(result[0].action, 'snapshot'); assert.equal(result[0].ok, true);
  assert.equal(executedActions([{ type: 'tool_start', args: { action: 'click' } }, { type: 'tool_end', ok: true }]).length, 0);
  assert.equal(executedActions([{ type: 'browser_start', args: { action: 'click' } }, { type: 'browser_end', ok: false, result: { error: 'failed' } }])[0].ok, false);
  assert.equal(executedActions([{ type: 'browser_start', args: { action: 'click' } }, { type: 'browser_start', args: { action: 'snapshot' } },
    { type: 'browser_end', ok: true, result: { text: 'ambiguous acknowledgement' } }]).length, 0);
});

test('reports distinguish controls and retain unstarted tasks in the model denominator', () => {
  const report = { mode: 'model', model: 'fake', seed: 7, config: {}, tasks: [
    { title: '<script>alert(1)</script>', category: 'write', repeat: 0, seed: 7, status: 'passed', events: [] },
    { title: 'cancelled task', category: 'read_only', repeat: 0, seed: 7, status: 'skipped', events: [] }] };
  const summary = summarize(report); assert.equal(summary.planned, 2); assert.equal(summary.model_judged_pass_rate, 0.5);
  assert.equal(summary.categories.read_only.model_rate, 0); assert.equal(summary.categories.read_only.skipped, 1);
  assert.equal(summarize({ ...report, mode: 'validate', control: 'reference' }).model_judged_pass_rate, null);
  assert.equal(summarize({ ...report, mode: 'validate', control: 'noop' }).noop_false_positive_rate, 0.5);
  const html = renderReport(report); assert.equal(html.includes('<script>'), false); assert.match(html, /&lt;script&gt;/);
  const redact = makeRedactor({ FAKE_API_KEY: 'dummy-secret-value' }, ['worker-secret-value']);
  const clean = safeGeneralValue({ 'dummy-secret-value': { header: 'Bearer private-value', authorization: 'x', screenshot: { data: 'pixels', version: 1 } } }, redact);
  assert.equal(JSON.stringify(clean).includes('dummy-secret-value'), false); assert.equal(JSON.stringify(clean).includes('private-value'), false);
  assert.equal(JSON.stringify(clean).includes('pixels'), false);
});

test('suite fingerprint includes controller fixtures, runner and judge source', async () => {
  const manifest = await suiteSourceManifest();
  assert.match(manifest.suite_source_sha256, /^[a-f0-9]{64}$/);
  assert.ok(manifest.suite_files_sha256['runner.js']); assert.ok(Object.keys(manifest.suite_files_sha256).some(file => file.startsWith('fixtures/')));
});

test('shared Skill body matches Go frontmatter boundaries and preserves body bytes', async () => {
  assert.equal(sharedSkillBody('\uFEFF---\r\nname: x\r\n--- \t\r\n\r\nbody\r\n'), '\nbody\n');
  assert.equal(sharedSkillBody('---\nname: x\n...\nbody\n'), 'body\n');
  assert.equal(sharedSkillBody('---\nname: x\nno closing delimiter\n'), '---\nname: x\nno closing delimiter\n');
  assert.equal(sharedSkillBody('\uFEFFplain\rbody\r'), 'plain\nbody\n');
  const manifest = await expectedSkillManifest();
  assert.match(manifest.expected_shared_skill_body_sha256, /^[a-f0-9]{64}$/); assert.match(manifest.shared_skill_file_sha256, /^[a-f0-9]{64}$/);
});

test('reference and correct-answer noop controls cover independent seed/layout variants', { timeout: 120000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-controls-'));
  try {
    for (const control of ['reference', 'noop']) {
      const options = parseOptions(['--control', control, '--seed', '71531', '--repeat', '2', '--out', path.join(dir, control)]);
      const { report } = await runSuite(options);
      assert.equal(report.infrastructure_error, undefined);
      assert.equal(report.tasks.length, 18); assert.deepEqual(report.summary.coverage.seeds, [71531, 71532]);
      assert.ok(report.tasks.every(row => row.context_cleaned === true));
      assert.ok(report.tasks.every(row => row.status === (control === 'reference' ? 'passed' : 'failed')),
        JSON.stringify(report.tasks.map(row => ({ id: row.id, seed: row.seed, status: row.status, reason: row.reason }))));
      assert.equal(report.summary.model_judged_pass_rate, null); assert.equal(report.config.profile, PROFILE);
      assert.equal(report.config.final_answer, 'natural_language');
      if (control === 'reference') assert.equal(report.summary.judge_control_pass_rate, 1);
      else { assert.equal(report.summary.noop_false_positive_rate, 0); assert.ok(report.tasks.every(row => row.events.length === 0)); }
      const saved = JSON.parse(await readFile(path.join(dir, control, 'summary.json'), 'utf8'));
      assert.equal(saved.suite, 'general-web-v1'); assert.ok(saved.config.suite_source_sha256);
    }
    await assert.rejects(runSuite(parseOptions(['--out', path.join(dir, 'reference')])), { code: 'EEXIST' });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('pre-cancelled tasks stay in the denominator without creating browser sessions', { timeout: 10000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-pre-cancel-'));
  try {
    const cancellation = new AbortController(); cancellation.abort(); const starts = [];
    const { report } = await runSuite(parseOptions(['--tasks', TASKS.slice(0, 2).map(task => task.id).join(','), '--out', path.join(dir, 'run')]),
      { abortSignal: cancellation.signal, onProgress: event => starts.push(event) });
    assert.equal(report.cancelled, true); assert.equal(report.summary.total, 2); assert.equal(report.summary.skipped, 2);
    assert.deepEqual(starts, []); assert.ok(report.tasks.every(row => row.reason.includes('cancelled')));
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('active cancellation reaps the owned adapter, cleans its context and preserves scheduled tasks', { timeout: 30000, skip: process.platform === 'win32' }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-cancel-'));
  const cancellation = new AbortController(); let pending, pid;
  try {
    const marker = path.join(dir, 'pid'), executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}\nimport {writeFileSync} from 'node:fs';writeFileSync(${JSON.stringify(marker)},String(process.pid));process.on('SIGTERM',()=>{});setInterval(()=>{},1000);\n`, { mode: 0o700 });
    const options = parseOptions(['--mode', 'model', '--agent', executable, '--model', 'fake', '--tasks', TASKS.slice(0, 2).map(task => task.id).join(','), '--out', path.join(dir, 'run')]);
    const starts = []; pending = runSuite(options, { abortSignal: cancellation.signal, onProgress: event => { if (event.type === 'task_start') starts.push(event.id); } });
    const until = Date.now() + 10000;
    while (!pid && Date.now() < until) { try { pid = Number(await readFile(marker, 'utf8')); } catch { await new Promise(resolve => setTimeout(resolve, 30)); } }
    assert.ok(pid, 'adapter did not start'); cancellation.abort();
    const { report } = await pending;
    assert.deepEqual(starts, [TASKS[0].id]); assert.equal(report.cancelled, true); assert.equal(report.tasks[0].context_cleaned, true);
    assert.equal(report.tasks[0].status === 'failed' || report.tasks[0].status === 'error', true); assert.equal(report.tasks[1].status, 'skipped');
    assert.equal(report.summary.model_judged_pass_rate, 0); assert.equal(report.summary.total, 2); assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  } finally {
    cancellation.abort(); if (pending) await pending;
    if (pid) { try { process.kill(-pid, 'SIGKILL'); } catch { /* reaped by watchdog */ } }
    await rm(dir, { recursive: true, force: true });
  }
});

async function scriptedAdapter(dir, { taskID, seed = 463, profile = PROFILE, measured = true, fingerprints = true, staleSkill = false, noop = false, twice = false }) {
  const fixture = await createGeneralWebFixtures({ seed });
  let data;
  try { data = fixture.referenceData(taskID); } finally { await fixture.close(); }
  const skillSource = await expectedSkillManifest();
  const executable = path.join(dir, `adapter-${profile}-${measured}-${fingerprints}-${staleSkill}-${noop}-${twice}`);
  await writeFile(executable, `#!${process.execPath}
let raw='';process.stdin.on('data',part=>raw+=part);process.stdin.on('end',async()=>{
  try {
    const i=JSON.parse(raw), original=${JSON.stringify(data.origin)}, current=new URL(i.start_url).origin;
    const adapt=value=>typeof value==='string'?value.replaceAll(original,current):Array.isArray(value)?value.map(adapt):value&&typeof value==='object'?Object.fromEntries(Object.entries(value).map(([k,v])=>[k,adapt(v)])):value;
    const plan=adapt(${JSON.stringify(noop ? [] : data.actions)});let op=0;
    const perform=async action=>{const r=await fetch(i.worker_url+'/sessions/'+i.session_id+'/execute',{method:'POST',headers:{Authorization:'Bearer '+process.env.POGO_BROWSER_EVAL_TOKEN,'Content-Type':'application/json'},body:JSON.stringify({op_id:'scripted-'+(++op),...action})});const result=await r.json();if(!r.ok||!result.ok)throw new Error(result.error||'scripted operation failed');};
    for(let repeat=0;repeat<${twice ? 2 : 1};repeat++){if(repeat)await perform({action:'navigate',url:i.start_url});for(const action of plan)await perform(action);}
    console.error(process.env.DUMMY_API_KEY||'');
    console.log(JSON.stringify({type:'message',text:process.env.DUMMY_API_KEY||''}));
    console.log(JSON.stringify({type:'end',answer:'\u0060\u0060\u0060text\\n'+adapt(${JSON.stringify(data.answer)})+'\\n\u0060\u0060\u0060',profile:${JSON.stringify(profile)},end_reason:'completed',system_prompt_sha256:${fingerprints ? "'a'.repeat(64)" : 'null'},tool_schema_sha256:${fingerprints ? "'b'.repeat(64)" : 'null'},shared_skill_body_sha256:${fingerprints ? JSON.stringify(staleSkill ? 'c'.repeat(64) : skillSource.expected_shared_skill_body_sha256) : 'null'},usage:${measured ? '{input:17,output:11}' : 'null'},usage_measured:${measured}}));
  }catch(error){console.error(error.message);process.exitCode=1;}
});
`, { mode: 0o700 });
  return executable;
}

test('natural fenced prose with real browser evidence passes without a final JSON outcome', { timeout: 30000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-natural-'));
  try {
    const executable = await scriptedAdapter(dir, { taskID: 'docs-retention' });
    const options = parseOptions(['--mode', 'model', '--model', 'scripted-local-control', '--agent', executable, '--tasks', 'docs-retention', '--seed', '463', '--out', path.join(dir, 'run')]);
    options.environment = { ...process.env, DUMMY_API_KEY: 'dummy-natural-answer-secret' };
    const { report } = await runSuite(options);
    assert.equal(report.infrastructure_error, undefined); assert.equal(report.tasks[0].status, 'passed', report.tasks[0].reason);
    assert.match(report.tasks[0].final_answer, /^```text/); assert.equal(report.tasks[0].outcome, undefined);
    assert.equal(report.tasks[0].profile, PROFILE); assert.match(report.tasks[0].profile_sha256, /^[a-f0-9]{64}$/);
    assert.equal(report.tasks[0].shared_skill_body_sha256, report.config.expected_shared_skill_body_sha256); assert.equal(report.tasks[0].context_cleaned, true);
    assert.equal((await readFile(path.join(dir, 'run', 'summary.json'), 'utf8')).includes('dummy-natural-answer-secret'), false);
    assert.equal((await readFile(path.join(dir, 'run', 'traces/docs-retention-0.json'), 'utf8')).includes('dummy-natural-answer-secret'), false);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('correct prose cannot replace profile identity, measured budget or actual browser actions', { timeout: 45000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-adapter-negative-'));
  try {
    for (const [name, settings, expectedFailure] of [
      ['profile', { profile: 'pogo-agent-browser-only' }, 'wrong_or_missing_adapter_profile'],
      ['fingerprints', { fingerprints: false }, 'missing_or_invalid_profile_fingerprints'],
      ['stale-skill', { staleSkill: true }, 'shared_skill_body_mismatch'],
      ['usage', { measured: false }, 'missing_measured_token_usage'],
      ['noop', { noop: true }, 'no_real_successful_browser_action']]) {
      const executable = await scriptedAdapter(dir, { taskID: 'docs-retention', ...settings });
      const { report } = await runSuite(parseOptions(['--mode', 'model', '--model', 'scripted-local-control', '--agent', executable,
        '--tasks', 'docs-retention', '--seed', '463', '--out', path.join(dir, name)]));
      assert.equal(report.infrastructure_error, undefined); assert.equal(report.tasks[0].status, 'failed', report.tasks[0].reason);
      assert.ok(report.tasks[0].judgement.failures.includes(expectedFailure), report.tasks[0].reason);
      assert.equal(report.tasks[0].context_cleaned, true);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('duplicate successful submits and extra mutations fail despite a correct receipt answer', { timeout: 30000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-duplicate-'));
  try {
    const executable = await scriptedAdapter(dir, { taskID: 'workbench-create', twice: true });
    const { report } = await runSuite(parseOptions(['--mode', 'model', '--model', 'scripted-local-control', '--agent', executable,
      '--tasks', 'workbench-create', '--seed', '463', '--out', path.join(dir, 'run')]));
    assert.equal(report.infrastructure_error, undefined); assert.equal(report.tasks[0].status, 'failed', report.tasks[0].reason);
    assert.ok(report.tasks[0].judgement.failures.includes('duplicate_submission'), report.tasks[0].reason);
    assert.equal(report.tasks[0].server_state_after.successfulSubmissions, 2); assert.equal(report.tasks[0].context_cleaned, true);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('measured token overruns and requested step overruns cannot pass', { timeout: 30000 }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-budget-'));
  try {
    const executable = await scriptedAdapter(dir, { taskID: 'docs-retention' });
    const { report: tokens } = await runSuite(parseOptions(['--mode', 'model', '--model', 'scripted-local-control', '--agent', executable,
      '--tasks', 'docs-retention', '--seed', '463', '--token-budget', '20', '--out', path.join(dir, 'tokens')]));
    assert.equal(tokens.tasks[0].status, 'failed'); assert.ok(tokens.tasks[0].judgement.failures.includes('token_budget_exceeded'));
    const { report: steps } = await runSuite(parseOptions(['--tasks', 'docs-retention', '--seed', '463', '--max-steps', '1', '--out', path.join(dir, 'steps')]));
    assert.equal(steps.tasks[0].status, 'error'); assert.equal(steps.tasks[0].step_budget_exceeded, true);
    assert.equal(steps.tasks[0].requested_steps, 1); assert.match(steps.tasks[0].reason, /step budget/);
    assert.equal(steps.tasks[0].context_cleaned, true);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('task deadline stops and reaps a silent adapter before the suite returns', { timeout: 15000, skip: process.platform === 'win32' }, async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-general-web-deadline-')); let pid;
  try {
    const marker = path.join(dir, 'pid'), executable = path.join(dir, 'adapter');
    await writeFile(executable, `#!${process.execPath}\nimport {writeFileSync} from 'node:fs';writeFileSync(${JSON.stringify(marker)},String(process.pid));process.on('SIGTERM',()=>{});setInterval(()=>{},1000);\n`, { mode: 0o700 });
    const { report } = await runSuite(parseOptions(['--mode', 'model', '--model', 'scripted-local-control', '--agent', executable,
      '--tasks', 'docs-retention', '--timeout-ms', '1000', '--out', path.join(dir, 'run')]));
    pid = Number(await readFile(marker, 'utf8')); assert.equal(report.tasks[0].status, 'error'); assert.match(report.tasks[0].reason, /time budget/);
    assert.equal(report.tasks[0].context_cleaned, true); assert.equal(report.summary.model_judged_pass_rate, 0);
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  } finally {
    if (pid) { try { process.kill(-pid, 'SIGKILL'); } catch { /* adapter watchdog already reaped it */ } }
    await rm(dir, { recursive: true, force: true });
  }
});
