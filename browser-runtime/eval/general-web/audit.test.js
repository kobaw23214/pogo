import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { parseAuditOptions, recordedActions, assessRunValidity, createOfflineAudit, writeOfflineAudit } from './audit.js';
import { renderOfflineAudit } from './audit-report.js';

const config = { profile: 'pogo-agent-general-web-v1', source_sha256: 'a'.repeat(64), suite_source_sha256: 'b'.repeat(64), adapter_sha256: 'c'.repeat(64),
  shared_skill_file_sha256: 'd'.repeat(64), expected_shared_skill_body_sha256: 'e'.repeat(64), token_budget_per_task: 100, max_steps_per_task: 5, time_budget_ms_per_task: 1000 };
const events = [{ type: 'browser_start', args: { action: 'click', target: 'e1' } }, { type: 'browser_end', ok: true,
  result: { ok: true, backend: 'native', url: 'http://127.0.0.1:1234/cases/workbench-create/tasks/T-1', text: '任务已保存 T-1' } }];
const row = extra => ({ id: 'workbench-create', title: '<script>alert(1)</script>', seed: 731, repeat: 0, status: 'failed', reason: 'visible_saved_task_evidence_missing',
  judgement: { success: false, failures: ['visible_saved_task_evidence_missing'] }, task_prompt: '起始网址：http://127.0.0.1:1234/cases/workbench-create/\n业务目标：创建并核验任务',
  final_answer: '已保存T-1', events, trace_path: 'traces/workbench-create-0.json', server_state_after: {}, end_reason: 'completed', profile: config.profile,
  system_prompt_sha256: 'f'.repeat(64), tool_schema_sha256: '1'.repeat(64), shared_skill_body_sha256: config.expected_shared_skill_body_sha256,
  usage: { input: 17, output: 11 }, usage_partial: false, duration_ms: 200, requested_steps: 1, step_budget_exceeded: false, context_cleaned: true, ...extra });

test('offline CLI requires explicit source and fresh output directory', () => {
  assert.throws(() => parseAuditOptions([])); assert.throws(() => parseAuditOptions(['--input', 'run']));
  assert.throws(() => parseAuditOptions(['--unknown', 'x'])); assert.equal(parseAuditOptions(['--help']).help, true);
  assert.equal(parseAuditOptions(['--input', './run', '--out', './new']).input, path.resolve('run'));
});

test('business success cannot cover driver failures or unknown evidence', () => {
  assert.equal(assessRunValidity(row(), config, { consistent: true }).verdict, 'passed');
  assert.equal(assessRunValidity(row({ status: 'error', reason: 'task time budget exceeded' }), config, { consistent: true }).verdict, 'failed');
  for (const change of [{ context_cleaned: false }, { step_budget_exceeded: true }, { usage: { input: 101, output: 1 } }, { shared_skill_body_sha256: '9'.repeat(64) }]) assert.equal(assessRunValidity(row(change), config, { consistent: true }).verdict, 'failed');
  assert.equal(assessRunValidity(row({ context_cleaned: null }), config, { consistent: true }).verdict, 'unknown');
  assert.equal(assessRunValidity(row({ usage_partial: true }), config, { consistent: true }).checks.measured_usage.verdict, 'unknown');
});

test('recorded batch acknowledgements include only actually executed children and final native observation', () => {
  const actions = recordedActions([{ type: 'browser_start', args: { action: 'batch', actions: [{ action: 'type' }, { action: 'click' }, { action: 'snapshot' }] } },
    { type: 'browser_end', ok: true, result: { ok: true, backend: 'native', url: 'http://site/cases/a/', text: 'saved', steps: [{ action: 'type', ok: true }, { action: 'click', ok: true }] } }]);
  assert.equal(actions.length, 2); assert.equal(actions[1].action, 'click'); assert.equal(actions[1].result.text, 'saved'); assert.equal(actions[1].result.backend, 'native');
});

test('immutable originals and SHA provenance remain alongside separate business verdict and validity', async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'pogo-offline-audit-'));
  try {
    const input = path.join(dir, 'run'); await mkdir(path.join(input, 'traces'), { recursive: true });
    const original = { suite: 'general-web-v1', mode: 'model', run_id: 'fixture-control', completed_at: '2026-10-03T00:00:00Z', config,
      summary: { passed: 0, failed: 1, total: 1, model_judged_pass_rate: 0 }, tasks: [row()] };
    const serialized = JSON.stringify(original); await writeFile(path.join(input, 'summary.json'), serialized); await writeFile(path.join(input, original.tasks[0].trace_path), JSON.stringify(events));
    const positive = () => ({ success: true, failures: [], reason: 'recorded business evidence verified', review_scope: 'business_evidence_only', run_validity: 'not_assessed' });
    const result = await createOfflineAudit({ inputDir: input, judge: positive });
    assert.deepEqual(result.original_summary, original.summary); assert.equal(result.tasks[0].original_status, 'failed'); assert.equal(result.tasks[0].audit_classification, 'confirmed_checker_false_negative');
    assert.equal(result.official_model_score_recomputed, false); assert.match(result.provenance.original_report_sha256, /^[a-f0-9]{64}$/); assert.match(result.provenance.checker_module_sha256, /^[a-f0-9]{64}$/);
    assert.equal(await readFile(path.join(input, 'summary.json'), 'utf8'), serialized);
    assert.equal(renderOfflineAudit(result).includes('<script>'), false); assert.match(renderOfflineAudit(result), /&lt;script&gt;/);
    await mkdir(path.join(dir, 'existing')); await assert.rejects(writeOfflineAudit({ input, outputDir: path.join(dir, 'existing') }), { code: 'EEXIST' });
    original.tasks[0].status = 'error'; original.tasks[0].reason = 'task time budget exceeded'; await writeFile(path.join(input, 'summary.json'), JSON.stringify(original));
    const invalid = await createOfflineAudit({ inputDir: input, judge: positive }); assert.equal(invalid.tasks[0].business_evidence.verdict, 'passed'); assert.equal(invalid.tasks[0].run_validity.verdict, 'failed'); assert.equal(invalid.tasks[0].audit_classification, 'run_invalid_business_evidence_separate');
    original.tasks[0] = row({ context_cleaned: null }); await writeFile(path.join(input, 'summary.json'), JSON.stringify(original));
    const unknown = await createOfflineAudit({ inputDir: input, judge: positive }); assert.equal(unknown.tasks[0].audit_classification, 'business_pass_with_unknown_run_validity');
    original.tasks[0] = row(); await writeFile(path.join(input, 'summary.json'), JSON.stringify(original));
    const incidental = await createOfflineAudit({ inputDir: input, judge: () => ({ success: false, failures: ['incidental_record_identity_error'],
      core_business_success: true, answer_accuracy: false, incidental_answer_errors: [{ record_id: 'T-1', expected_title: '正确名称', stated_title: '错误名称' }] }) });
    assert.equal(incidental.tasks[0].core_business_success, true); assert.equal(incidental.tasks[0].answer_accuracy, false);
    assert.equal(incidental.tasks[0].audit_classification, 'core_success_with_incidental_answer_error');
    assert.equal(incidental.summary.core_business_passed, 1); assert.equal(incidental.summary.answer_accuracy_failed, 1); assert.equal(incidental.summary.incidental_answer_error_tasks, 1);
    assert.equal(incidental.summary.confirmed_checker_false_negatives, 0); assert.equal(incidental.summary.core_business_checker_false_negatives, 1);
    assert.equal(incidental.summary.core_success_with_incidental_answer_error, 1); assert.equal(incidental.original_summary.model_judged_pass_rate, 0);
    delete original.completed_at; await writeFile(path.join(input, 'summary.json'), JSON.stringify(original)); await assert.rejects(createOfflineAudit({ inputDir: input, judge: positive }), /not complete/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
