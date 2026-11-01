import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { diagnose, parseProtocol, generateDiagnostics } from './diagnose.js';
import { DATA, API_PATH } from './tasks.js';

function counters(extra = {}) { return { counters: { loginSubmissions: 0, successfulLogins: 0, orderReads: 1, ...extra }, activeSessions: 1, activeTokens: 0 }; }
function events(args, result) { return [{ type: 'browser_start', args }, { type: 'browser_end', ok: true, result }]; }
function example() {
  const state = { status: 'completed', reason: 'api_read_complete', data: DATA };
  return { suite: 'browser-auth-v1', mode: 'model', model: 'fake-local', run_id: 'fake', tasks: [{ id: 'cookie-read', repeat: 0, group: 'preauthorized_automatic', status: 'failed', simulated_human_interventions: 0, context_cleaned: true,
    events: [...events({ action: 'auth_status' }, { text: '{"api_access":{"status":"active"}}' }), ...events({ action: 'api_read', path: API_PATH }, { text: JSON.stringify({ status: 200, data: { total: 1, orders: [{ id: DATA.order_id, amount: DATA.amount }] } }) })],
    phases: [{ index: 0, end_reason: 'completed', final_answer: `I completed the task.\n${JSON.stringify(state)}`, server_state_before: counters(), server_state_after: counters({ orderReads: 2 }), judgement: { success: false, failures: ['structured_outcome_mismatch'], observed: { access_status: 'active' } } }],
  }] };
}

test('pure JSON protocol rejects prose, code fences, unknown fields and invalid shapes', () => {
  const valid = '{"status":"needs_human","reason":"authorization_required"}';
  assert.ok(parseProtocol(valid));
  for (const value of [`done ${valid}`, '```json\n' + valid + '\n```', valid + '{}', '{"status":"completed","reason":"ok","extra":1}', '{"status":"completed","reason":"ok","data":[]}', '{"status":"completed","reason":" "}', 'null']) assert.equal(parseProtocol(value), null);
});

test('behavior and formatting diagnosis never relabel official failure or mutate source answers', () => {
  const source = example(), before = JSON.stringify(source), result = diagnose(source);
  assert.equal(result.rows[0].official_status, 'failed'); assert.equal(result.rows[0].official_pass, false);
  assert.equal(result.rows[0].behavior_evidence_pass, true); assert.equal(result.rows[0].output_protocol_pass, false);
  assert.equal(result.rows[0].semantic_claim_match, null);
  assert.equal(JSON.stringify(source), before);
  source.tasks[0].events.at(-1).result.text = JSON.stringify({ status: 200, data: { total: 1, orders: [{ id: 'wrong', amount: 1 }] } });
  assert.equal(diagnose(source).rows[0].behavior_evidence_pass, false);
});

test('unrun assisted continuation remains unmeasured even when model login behavior was correct', () => {
  const source = example(), row = source.tasks[0];
  row.id = 'login-assisted-resume'; row.group = 'human_assisted';
  row.events = events({ action: 'auth_status' }, { text: '{"api_access":{"status":"none"}}' });
  row.phases[0].server_state_before = counters({ orderReads: 0 });
  row.phases[0].server_state_after = counters({ loginSubmissions: 1, successfulLogins: 1 });
  row.phases[0].judgement.observed.access_status = 'none';
  const result = diagnose(source);
  assert.equal(result.rows[0].phases[0].behavior_evidence_pass, true);
  assert.equal(result.rows[0].behavior_evidence_pass, null);
  assert.equal(result.summary.unmeasured_phases, 1); assert.equal(result.summary.official.passed, 0);
});

test('offline generator requires completed matching judges and refuses report overwrite', async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-auth-diagnostic-'));
  try {
    const source = example(), input = path.join(dir, 'summary.json'), output = path.join(dir, 'out');
    const bytes = await readFile(new URL('./tasks.js', import.meta.url));
    source.completed_at = new Date().toISOString();
    source.config = { files: { 'pogo/browser-runtime/eval/auth/tasks.js': createHash('sha256').update(bytes).digest('hex') } };
    await writeFile(input, JSON.stringify(source));
    const original = await readFile(input, 'utf8');
    await generateDiagnostics([input], output);
    assert.equal(await readFile(input, 'utf8'), original);
    assert.match(await readFile(path.join(output, 'report.html'), 'utf8'), /正式通过 0\/1/);
    await assert.rejects(generateDiagnostics([input], output), { code: 'EEXIST' });
    source.config.files['pogo/browser-runtime/eval/auth/tasks.js'] = 'different'; await writeFile(input, JSON.stringify(source));
    await assert.rejects(generateDiagnostics([input], path.join(dir, 'different')), /differs/);
    delete source.completed_at; await writeFile(input, JSON.stringify(source));
    await assert.rejects(generateDiagnostics([input], path.join(dir, 'running')), /still running/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
