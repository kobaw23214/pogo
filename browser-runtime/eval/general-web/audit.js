import { readFile, writeFile, mkdir, readdir } from 'node:fs/promises';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { judgeRecordedGeneralWebCase } from './fixtures/index.js';
import { renderOfflineAudit } from './audit-report.js';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const hex64 = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const finite = value => typeof value === 'number' && Number.isFinite(value) && value >= 0;
const DRIVER_FAILURES = new Set(['wrong_or_missing_adapter_profile', 'adapter_not_completed', 'missing_or_invalid_profile_fingerprints',
  'shared_skill_body_mismatch', 'missing_measured_token_usage', 'token_budget_exceeded', 'task_time_budget_exceeded', 'step_budget_exceeded']);

export function parseAuditOptions(argv) {
  const options = { input: null, outputDir: null };
  for (let i = 0; i < argv.length; i++) {
    const name = argv[i]; if (name === '--help') { options.help = true; continue; }
    const value = argv[++i]; if (!value || value.startsWith('--')) throw new Error(`${name} requires a value`);
    if (name === '--input') options.input = path.resolve(value);
    else if (name === '--out') options.outputDir = path.resolve(value);
    else throw new Error(`unknown option ${name}`);
  }
  if (!options.help && (!options.input || !options.outputDir)) throw new Error('--input RUN_DIR and --out NEW_DIR are required');
  return options;
}

export function recordedActions(events = []) {
  const actions = [], pending = [];
  for (const event of events) {
    if (event.type === 'browser_start') { if (pending.length) return []; pending.push(event.args); }
    if (event.type !== 'browser_end') continue;
    const args = pending.shift(); if (!args) continue;
    const result = event.result ?? {};
    if (args.action === 'batch') {
      for (const [index, step] of (result.steps ?? []).entries()) {
        const child = args.actions?.[index]; if (!child || child.action !== step.action) continue;
        actions.push({ ...child, ok: step.ok === true, error: step.error ?? '', result: { ...step,
          ...(index === result.steps.length - 1 ? { ...result, steps: undefined } : {}) } });
      }
    } else actions.push({ ...args, ok: event.ok === true, error: result.error ?? '', result });
  }
  return actions;
}

export function assessRunValidity(row, config, traceEvidence) {
  const checks = {}, failures = row.judgement?.failures ?? [];
  const set = (name, verdict, detail) => { checks[name] = { verdict, detail }; };
  set('driver_execution', row.status === 'error' ? 'failed' : row.status === 'skipped' ? 'unknown' : 'passed', row.reason ?? 'No archived driver reason.');
  set('adapter_completed', row.end_reason === 'completed' ? 'passed' : failures.includes('adapter_not_completed') || row.end_reason ? 'failed' : 'unknown', row.end_reason ?? 'Final adapter event unavailable.');
  set('profile_identity', row.profile && config.profile ? row.profile === config.profile ? 'passed' : 'failed' : 'unknown', `${row.profile ?? 'missing'}; expected ${config.profile ?? 'unknown'}`);
  const keys = ['system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256'];
  set('profile_fingerprints', keys.every(key => hex64(row[key])) ? 'passed' : failures.includes('missing_or_invalid_profile_fingerprints') ? 'failed' : 'unknown', 'Requires archived SHA256 prompt, tool schema and shared Skill body fingerprints.');
  set('skill_freshness', hex64(row.shared_skill_body_sha256) && hex64(config.expected_shared_skill_body_sha256) ? row.shared_skill_body_sha256 === config.expected_shared_skill_body_sha256 ? 'passed' : 'failed' : 'unknown', 'Compared only with the original run’s archived expected production Skill body hash.');
  set('source_fingerprints_recorded', ['source_sha256', 'suite_source_sha256', 'adapter_sha256', 'shared_skill_file_sha256'].every(key => hex64(config[key])) ? 'passed' : 'unknown', 'Source and adapter fingerprints are archived; no claim of independent binary reconstruction.');
  const measured = finite(row.usage?.input) && finite(row.usage?.output);
  set('measured_usage', measured && row.usage_partial === false ? 'passed' : failures.includes('missing_measured_token_usage') ? 'failed' : 'unknown', 'Partial or absent usage remains unknown.');
  set('token_budget', failures.includes('token_budget_exceeded') ? 'failed' : measured && finite(config.token_budget_per_task) ? row.usage.input + row.usage.output <= config.token_budget_per_task ? 'passed' : 'failed' : 'unknown', `Usage ${measured ? row.usage.input + row.usage.output : 'unknown'}; budget ${config.token_budget_per_task ?? 'unknown'}.`);
  set('step_budget', row.step_budget_exceeded === true || failures.includes('step_budget_exceeded') ? 'failed' : Number.isInteger(row.requested_steps) && finite(config.max_steps_per_task) ? row.requested_steps <= config.max_steps_per_task ? 'passed' : 'failed' : 'unknown', `Requested ${row.requested_steps ?? 'unknown'}; budget ${config.max_steps_per_task ?? 'unknown'}.`);
  const timeFailed = failures.includes('task_time_budget_exceeded') || /time budget|time_budget|deadline/i.test(row.reason ?? '') && row.status === 'error';
  set('deadline', timeFailed ? 'failed' : finite(row.duration_ms) && finite(config.time_budget_ms_per_task) && row.duration_ms <= config.time_budget_ms_per_task ? 'passed' : 'unknown', 'An archived duration beyond the deadline without an explicit driver failure remains unknown because post-task cancellation time may be included.');
  set('context_cleanup', row.context_cleaned === true ? 'passed' : row.context_cleaned === false ? 'failed' : 'unknown', 'Requires the original worker’s confirmed browser-context close.');
  set('trace_consistency', traceEvidence?.consistent === true ? 'passed' : traceEvidence?.consistent === false ? 'failed' : 'unknown', traceEvidence?.reason ?? 'No per-task trace artifact available.');
  set('original_driver_failures', failures.some(reason => DRIVER_FAILURES.has(reason)) ? 'failed' : row.status === 'skipped' ? 'unknown' : 'passed', failures.filter(reason => DRIVER_FAILURES.has(reason)).join('; ') || 'No original protected driver failure recorded.');
  const verdicts = Object.values(checks).map(check => check.verdict);
  return { verdict: verdicts.includes('failed') ? 'failed' : verdicts.includes('unknown') ? 'unknown' : 'passed', checks,
    scope: 'archived_driver_evidence_only', independent_binary_reconstruction: 'not_performed' };
}

function classification(row, business, validity, coreSuccess, answerAccuracy) {
  if (business.verdict === 'not_assessed') return 'not_assessed';
  if (validity.verdict === 'failed') return 'run_invalid_business_evidence_separate';
  if (validity.verdict === 'unknown') return business.verdict === 'passed' ? 'business_pass_with_unknown_run_validity' : 'business_failure_with_unknown_run_validity';
  if (coreSuccess === true && answerAccuracy === false) return 'core_success_with_incidental_answer_error';
  if (row.status === 'passed' && business.verdict === 'passed') return 'verified_original_pass';
  if (row.status === 'passed' && business.verdict === 'failed') return 'checker_false_positive';
  if (row.status === 'failed' && business.verdict === 'passed') return 'confirmed_checker_false_negative';
  return 'business_evidence_failure';
}

export async function createOfflineAudit({ inputDir, judge = judgeRecordedGeneralWebCase, requireComplete = true }) {
  const summaryPath = path.join(inputDir, 'summary.json'), bytes = await readFile(summaryPath), original = JSON.parse(bytes);
  if (original.suite !== 'general-web-v1' || original.mode !== 'model' || !Array.isArray(original.tasks)) throw new Error('input is not an archived general-web-v1 model report');
  if (requireComplete && !original.completed_at) throw new Error('original run is not complete; refusing to produce a final audit');
  const report = { schema_version: 1, audit_suite: 'general-web-business-evidence-offline-v2', postprocess_version: 'general-web-offline-summary-v2', created_at: new Date().toISOString(), original_run_id: original.run_id,
    original_summary: structuredClone(original.summary), original_config: structuredClone(original.config), official_model_score_recomputed: false,
    scope: 'business_evidence_review_preserving_original_driver_and_score', provenance: { original_report_path: summaryPath, original_report_sha256: sha(bytes),
      original_source_sha256: original.config.source_sha256, original_suite_source_sha256: original.config.suite_source_sha256,
      original_adapter_sha256: original.config.adapter_sha256, original_completed_at: original.completed_at,
      original_trace_sha256: {}, audit_source_sha256: {}, checker_regression_files_sha256: {}, checker_module_sha256: null }, tasks: [] };
  for (const source of [fileURLToPath(import.meta.url), fileURLToPath(new URL('./audit-report.js', import.meta.url)), fileURLToPath(new URL('./audit.test.js', import.meta.url)), fileURLToPath(new URL('./fixtures/index.js', import.meta.url))]) {
    const digest = sha(await readFile(source)); report.provenance.audit_source_sha256[path.basename(source)] = digest;
    if (path.basename(source) === 'index.js') report.provenance.checker_module_sha256 = digest;
  }
  const fixtures = fileURLToPath(new URL('./fixtures/', import.meta.url));
  report.provenance.checker_regression_files_sha256['fixtures/index.test.js'] = sha(await readFile(path.join(fixtures, 'index.test.js')));
  for (const name of (await readdir(path.join(fixtures, 'regressions'))).sort()) {
    if (!/^[A-Za-z0-9_-]+\.json$/.test(name)) continue;
    report.provenance.checker_regression_files_sha256[`fixtures/regressions/${name}`] = sha(await readFile(path.join(fixtures, 'regressions', name)));
  }
  report.provenance.audit_bundle_sha256 = sha(JSON.stringify({ source: report.provenance.audit_source_sha256, regressions: report.provenance.checker_regression_files_sha256 }));
  for (const row of original.tasks) {
    let traceEvidence = { consistent: null, reason: 'No task trace path recorded.' };
    if (row.trace_path) {
      if (!/^traces\/[A-Za-z0-9_-]+\.json$/.test(row.trace_path)) throw new Error(`unsafe trace path for ${row.id}`);
      try {
        const trace = await readFile(path.join(inputDir, row.trace_path));
        report.provenance.original_trace_sha256[row.trace_path] = sha(trace);
        traceEvidence = { consistent: JSON.stringify(JSON.parse(trace)) === JSON.stringify(row.events), reason: 'Compared complete archived task trace with report events.' };
      } catch (error) { traceEvidence = { consistent: false, reason: `Archived trace unavailable or invalid (${error.code ?? 'parse error'}).` }; }
    }
    const actions = recordedActions(row.events), startURL = row.task_prompt?.match(/^起始网址：([^\n]+)/)?.[1] ?? row.task_prompt?.match(/https?:\/\/[^\s]+/)?.[0];
    let business = { verdict: 'not_assessed', success: null, failures: [], reason: 'Task has no complete archived browser/server evidence.', review_scope: 'business_evidence_only', run_validity: 'not_assessed' };
    if (row.status !== 'skipped' && row.server_state_after && startURL) {
      try {
        const result = judge(row.id, row.seed, startURL, row.final_answer, actions, row.server_state_after);
        business = { ...result, verdict: result.success === true ? 'passed' : 'failed' };
      } catch (error) { business = { ...business, reason: `Independent checker could not assess (${error.message}).` }; }
    }
    const validity = assessRunValidity(row, original.config, traceEvidence);
    const coreSuccess = Object.hasOwn(business, 'core_business_success') ? business.core_business_success : business.success;
    const answerAccuracy = Object.hasOwn(business, 'answer_accuracy') ? business.answer_accuracy : business.success;
    const verdict = value => value === true ? 'passed' : value === false ? 'failed' : 'unknown';
    report.tasks.push({ id: row.id, title: row.title, category: row.category, site: row.site, repeat: row.repeat, seed: row.seed, layout_variant: row.layout_variant,
      original_status: row.status, original_reason: row.reason, original_judgement: structuredClone(row.judgement), task_prompt: row.task_prompt, final_answer: row.final_answer,
      original_usage: structuredClone(row.usage), original_usage_partial: row.usage_partial, original_duration_ms: row.duration_ms, original_requested_steps: row.requested_steps,
      original_context_cleaned: row.context_cleaned, business_evidence: business, core_business_success: coreSuccess, core_business_verdict: verdict(coreSuccess),
      answer_accuracy: answerAccuracy, answer_accuracy_verdict: verdict(answerAccuracy), incidental_answer_errors: business.incidental_answer_errors ?? [],
      run_validity: validity, audit_classification: classification(row, business, validity, coreSuccess, answerAccuracy),
      actions, server_state_before: structuredClone(row.server_state_before), server_state_after: structuredClone(row.server_state_after) });
  }
  report.summary = { planned: report.tasks.length, business_assessed: report.tasks.filter(row => row.business_evidence.verdict !== 'not_assessed').length,
    business_passed: report.tasks.filter(row => row.business_evidence.verdict === 'passed').length, business_failed: report.tasks.filter(row => row.business_evidence.verdict === 'failed').length,
    business_not_assessed: report.tasks.filter(row => row.business_evidence.verdict === 'not_assessed').length,
    core_business_passed: report.tasks.filter(row => row.core_business_success === true).length, core_business_failed: report.tasks.filter(row => row.core_business_success === false).length,
    core_business_unknown: report.tasks.filter(row => row.core_business_success !== true && row.core_business_success !== false).length,
    answer_accuracy_passed: report.tasks.filter(row => row.answer_accuracy === true).length, answer_accuracy_failed: report.tasks.filter(row => row.answer_accuracy === false).length,
    answer_accuracy_unknown: report.tasks.filter(row => row.answer_accuracy !== true && row.answer_accuracy !== false).length,
    incidental_answer_error_tasks: report.tasks.filter(row => row.incidental_answer_errors.length > 0).length,
    confirmed_checker_false_negatives: report.tasks.filter(row => row.audit_classification === 'confirmed_checker_false_negative').length,
    core_business_checker_false_negatives: report.tasks.filter(row => row.original_status === 'failed' && row.core_business_success === true && row.run_validity.verdict === 'passed').length,
    core_success_with_incidental_answer_error: report.tasks.filter(row => row.audit_classification === 'core_success_with_incidental_answer_error').length,
    checker_false_positives: report.tasks.filter(row => row.audit_classification === 'checker_false_positive').length,
    run_validity_passed: report.tasks.filter(row => row.run_validity.verdict === 'passed').length, run_validity_failed: report.tasks.filter(row => row.run_validity.verdict === 'failed').length,
    run_validity_unknown: report.tasks.filter(row => row.run_validity.verdict === 'unknown').length };
  return report;
}

export async function writeOfflineAudit(options) {
  const source = path.resolve(options.input), destination = path.resolve(options.outputDir);
  if (destination === source || destination.startsWith(source + path.sep)) throw new Error('audit output cannot be inside the immutable original run');
  const report = await createOfflineAudit({ inputDir: options.input });
  await mkdir(path.dirname(options.outputDir), { recursive: true, mode: 0o700 });
  await mkdir(options.outputDir, { mode: 0o700 }); // Never overwrite either an old audit or the original run.
  await writeFile(path.join(options.outputDir, 'audit.json'), JSON.stringify(report, null, 2), { mode: 0o600 });
  await writeFile(path.join(options.outputDir, 'report.html'), renderOfflineAudit(report), { mode: 0o600 });
  return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const options = parseAuditOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('Independent general-web business-evidence audit; does not call a model or overwrite original scores.\nnode eval/general-web/audit.js --input COMPLETED_V1_RUN_DIR --out NEW_AUDIT_DIR\n');
    else { const result = await writeOfflineAudit(options); process.stdout.write(JSON.stringify({ output_dir: options.outputDir, summary: result.summary, official_model_score_recomputed: false }) + '\n'); }
  } catch (error) { process.stderr.write(`Offline audit failed: ${error.message}\n`); process.exitCode = 2; }
}
