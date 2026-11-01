import { readFile, writeFile, mkdir, mkdtemp, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const hex = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const key = task => `${task.id}:${task.seed}:${task.repeat ?? 0}`;
const requireValue = (condition, message) => { if (!condition) throw new Error(message); };
const count = (tasks, field, value) => tasks.filter(task => task[field] === value).length;
const maybeDate = value => typeof value === 'string' && Number.isFinite(Date.parse(value));
const safeName = name => /^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+$/.test(name);

export function parseOverviewOptions(argv) {
  const names = { '--v1': 'v1', '--v1-audit': 'v1Audit', '--v2': 'v2', '--v2-audit': 'v2Audit',
    '--v2-manual-review': 'v2ManualReview', '--supplement': 'supplement', '--supplement-manual-review': 'supplementManualReview', '--old-pogo-review': 'oldPogoReview',
    '--new-pogo-review': 'newPogoReview', '--out': 'outputDir' };
  const options = {};
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--help' || argv[i] === '--self-test') { options[argv[i].slice(2)] = true; continue; }
    const name = names[argv[i]], value = argv[++i];
    requireValue(name && value && !value.startsWith('--'), 'Unknown option or missing value. Use --help.');
    requireValue(!Object.hasOwn(options, name), `Duplicate option ${argv[i - 1]}.`);
    options[name] = path.resolve(value);
  }
  if (!options.help && !options['self-test']) for (const name of ['v1', 'v1Audit', 'v2', 'oldPogoReview', 'newPogoReview', 'outputDir']) requireValue(options[name], `${name} is required.`);
  return options;
}

function skillBody(bytes) {
  const text = bytes.toString('utf8').replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/^\uFEFF/, '');
  if (!text.startsWith('---\n') && text !== '---') return text;
  const lines = text.slice(3).replace(/^\n/, '').split('\n');
  const end = lines.findIndex(line => ['---', '...'].includes(line.replace(/[ \t]+$/, '')));
  return end < 0 ? text : lines.slice(end + 1).join('\n');
}

function nativeActions(events = []) {
  const actions = [], pending = [];
  for (const event of events) {
    if (event.type === 'browser_start') { if (pending.length) return []; pending.push(event.args); }
    if (event.type !== 'browser_end') continue;
    const args = pending.shift(); if (!args) continue;
    const result = event.result ?? {};
    if (args.action === 'batch') for (const [index, step] of (result.steps ?? []).entries()) {
      const child = args.actions?.[index]; if (!child || child.action !== step.action) continue;
      actions.push({ ...child, ok: step.ok === true, error: step.error ?? '', result: { ...step,
        ...(index === result.steps.length - 1 ? { ...result, steps: undefined } : {}) } });
    }
    else actions.push({ ...args, ok: event.ok === true, error: result.error ?? '', result });
  }
  return actions;
}

function metadata(run) {
  const c = run.config, fixture = 'pogo/browser-runtime/eval/general-web/fixtures/index.js';
  return { model: run.model, run_id: run.run_id, completed_at: run.completed_at,
    seeds: [...new Set(run.tasks.map(t => t.seed))], task_ids: [...new Set(run.tasks.map(t => t.id))],
    sites: [...new Set(run.tasks.map(t => t.site))], layouts: [...new Set(run.tasks.map(t => t.layout_variant))],
    profile: c.profile, source_sha256: c.source_sha256, suite_source_sha256: c.suite_source_sha256,
    grader_source_sha256: c.files?.[fixture] ?? c.suite_files_sha256?.['fixtures/index.js'] ?? null,
    checker_versions: [...new Set(run.tasks.map(t => t.judgement?.checker_version ?? '未记录'))],
    shared_skill_file_sha256: c.shared_skill_file_sha256, shared_skill_body_sha256: c.expected_shared_skill_body_sha256,
    adapter_sha256: c.adapter_sha256, git_revision: c.git_revision,
    system_prompt_sha256: [...new Set(run.tasks.map(t => t.system_prompt_sha256).filter(Boolean))],
    tool_schema_sha256: [...new Set(run.tasks.map(t => t.tool_schema_sha256).filter(Boolean))],
    budgets: { turns: c.max_turns_per_task, tokens: c.token_budget_per_task, deadline_ms: c.time_budget_ms_per_task, steps: c.max_steps_per_task },
    context_cleanup_confirmed: run.tasks.every(t => t.status === 'skipped' || t.context_cleaned === true) };
}

export function assertAuditMatches(audit, model, originalSHA) {
  requireValue(audit.official_model_score_recomputed === false && audit.original_run_id === model.run_id, 'Audit must preserve and identify its original model run.');
  requireValue(audit.provenance?.original_report_sha256 === originalSHA && equal(audit.original_summary, model.summary) && equal(audit.original_config, model.config), 'Audit source/config/summary does not match original model bytes.');
  requireValue(audit.tasks?.length === model.tasks.length, 'Audit task denominator differs from original run.');
  const originals = new Map(model.tasks.map(t => [key(t), t]));
  const seen = new Set();
  for (const task of audit.tasks) {
    const id = key(task), source = originals.get(id);
    requireValue(source && !seen.has(id), 'Audit task identity is missing or duplicated.'); seen.add(id);
    requireValue(task.original_status === source.status && task.original_reason === source.reason && equal(task.original_judgement, source.judgement), 'Audit changed original task result.');
    for (const field of ['task_prompt', 'final_answer', 'server_state_before', 'server_state_after']) requireValue(equal(task[field], source[field]), `Audit changed ${field}.`);
    requireValue(equal(task.actions, nativeActions(source.events)), 'Audit browser actions differ from archived native acknowledgements.');
    requireValue(task.original_context_cleaned === source.context_cleaned, 'Audit changed context cleanup evidence.');
  }
}

function auditCounts(audit) {
  const tasks = audit.tasks;
  return { denominator: tasks.length, core: count(tasks, 'core_business_success', true), accuracy: count(tasks, 'answer_accuracy', true),
    accuracy_failed: count(tasks, 'answer_accuracy', false), accuracy_unknown: tasks.filter(t => t.answer_accuracy !== true && t.answer_accuracy !== false).length,
    full_task_checker_false_negatives: count(tasks, 'audit_classification', 'confirmed_checker_false_negative'),
    core_business_checker_false_negatives: tasks.filter(t => t.original_status === 'failed' && t.core_business_success === true && t.run_validity?.verdict === 'passed').length,
    validity_passed: tasks.filter(t => t.run_validity?.verdict === 'passed').length, source: '机械离线复核',
    checker_source_sha256: audit.provenance.checker_module_sha256, audit_bundle_sha256: audit.provenance.audit_bundle_sha256,
    postprocess_version: audit.postprocess_version ?? '未记录', postprocess_source_sha256: audit.provenance.audit_source_sha256 };
}

export async function createOverview(options) {
  const artifacts = new Map();
  async function bytes(file, role) {
    const absolute = path.resolve(file), raw = await readFile(absolute), digest = sha(raw);
    const previous = artifacts.get(absolute); requireValue(!previous || previous.sha256 === digest, 'Input changed during overview generation.');
    artifacts.set(absolute, { path: absolute, sha256: digest, bytes: raw.length, roles: [...new Set([...(previous?.roles ?? []), role])] });
    return raw;
  }
  async function json(file, role) { return JSON.parse((await bytes(file, role)).toString('utf8')); }
  async function modelRun(dir, label) {
    const file = path.join(dir, 'summary.json'), run = await json(file, `${label}:model_summary`);
    requireValue(run.suite === 'general-web-v1' && run.mode === 'model' && maybeDate(run.completed_at) && Array.isArray(run.tasks), `${label} is not a completed general-web model run.`);
    requireValue(run.config?.fixture_scope === 'controlled_synthetic_three_origins', `${label} fixture scope is missing.`);
    for (const field of ['source_sha256', 'suite_source_sha256', 'expected_shared_skill_body_sha256', 'shared_skill_file_sha256', 'adapter_sha256']) requireValue(hex(run.config[field]), `${label} has missing ${field}.`);
    const statuses = ['passed', 'failed', 'error', 'skipped'], seen = new Set();
    for (const task of run.tasks) {
      const id = key(task); requireValue(!seen.has(id) && statuses.includes(task.status), `${label} task identity/status is invalid.`); seen.add(id);
      if (task.status === 'skipped') continue;
      requireValue(task.context_cleaned === true, `${label} CLI context cleanup is unconfirmed.`);
      if (['passed', 'failed'].includes(task.status)) requireValue(task.end_reason === 'completed' && task.shared_skill_body_sha256 === run.config.expected_shared_skill_body_sha256, `${label} task completion/Skill version mismatch.`);
      requireValue(/^traces\/[A-Za-z0-9_-]+\.json$/.test(task.trace_path), `${label} task trace path is invalid.`);
      const trace = await json(path.join(dir, task.trace_path), `${label}:native_trace`);
      requireValue(equal(trace, task.events), `${label} trace differs from archived report events.`);
    }
    requireValue(run.summary?.planned === run.tasks.length && run.summary.total === run.tasks.length, `${label} summary denominator mismatch.`);
    for (const status of statuses) requireValue(run.summary[status] === count(run.tasks, 'status', status), `${label} summary ${status} mismatch.`);
    return { run, file, sha256: artifacts.get(path.resolve(file)).sha256, summary: { passed: run.summary.passed, denominator: run.tasks.length, failed: run.summary.failed, error: run.summary.error, skipped: run.summary.skipped }, metadata: metadata(run) };
  }
  async function offline(dir, model, label) {
    const audit = await json(path.join(dir, 'audit.json'), `${label}:offline_audit`);
    assertAuditMatches(audit, model.run, model.sha256);
    for (const task of model.run.tasks.filter(t => t.status !== 'skipped')) requireValue(audit.provenance.original_trace_sha256?.[task.trace_path] === artifacts.get(path.resolve(path.dirname(model.file), task.trace_path)).sha256, 'Audit trace SHA does not match original trace.');
    requireValue(hex(audit.provenance.checker_module_sha256) && hex(audit.provenance.audit_bundle_sha256) && Object.values(audit.provenance.audit_source_sha256 ?? {}).every(hex), 'Offline checker/postprocess fingerprints are missing.');
    const result = auditCounts(audit);
    for (const [field, value] of Object.entries({ planned: result.denominator, core_business_passed: result.core, answer_accuracy_passed: result.accuracy, answer_accuracy_failed: result.accuracy_failed,
      confirmed_checker_false_negatives: result.full_task_checker_false_negatives, core_business_checker_false_negatives: result.core_business_checker_false_negatives })) requireValue(audit.summary?.[field] === value, `Offline audit ${field} mismatch.`);
    return { ...result, tasks: audit.tasks.map(t => ({ id: t.id, seed: t.seed, core: t.core_business_success, accuracy: t.answer_accuracy, classification: t.audit_classification, original_status: t.original_status })) };
  }
  async function manual(file, model, label) {
    const review = await json(file, `${label}:manual_review`);
    requireValue(review.run_id === model.run.run_id && review.completed_at === model.run.completed_at && review.original_report_sha256 === model.sha256, `Manual review does not identify the unchanged completed ${label} run.`);
    requireValue(equal(review.original_summary, model.run.summary) && review.source_sha256 === model.run.config.source_sha256 && review.suite_source_sha256 === model.run.config.suite_source_sha256 && review.expected_shared_skill_body_sha256 === model.run.config.expected_shared_skill_body_sha256, 'Manual review source/Skill metadata mismatch.');
    requireValue(review.tasks?.length === model.run.tasks.length, 'Manual review denominator mismatch.');
    const originals = new Map(model.run.tasks.map(t => [key(t), t])), seen = new Set();
    for (const task of review.tasks) {
      const id = key(task), original = originals.get(id); requireValue(original && !seen.has(id), 'Manual task identity mismatch.'); seen.add(id);
      for (const field of ['status', 'server_state_before', 'server_state_after', 'final_answer']) requireValue(equal(task[field], original[field]), `Manual review changed ${field}.`);
      requireValue(equal(task.canonical_actions, nativeActions(original.events)), 'Manual review actions differ from native evidence.');
      requireValue(task.trace_sha256 === artifacts.get(path.resolve(path.dirname(model.file), original.trace_path)).sha256, 'Manual review trace SHA mismatch.');
      requireValue(task.run_validity?.verdict === 'passed' && original.context_cleaned === true, 'Manual review run validity is not confirmed.');
    }
    return { source: '独立逐项复核', denominator: review.tasks.length, core: count(review.tasks, 'core_business_success', true), accuracy: count(review.tasks, 'answer_accuracy', true),
      accuracy_failed: count(review.tasks, 'answer_accuracy', false), accuracy_unknown: review.tasks.filter(t => typeof t.answer_accuracy !== 'boolean').length,
      full_task_checker_false_negatives: count(review.tasks, 'audit_conclusion', 'checker_false_negative'), validity_passed: review.tasks.length,
      metric_definition: review.metric_definition, automatic_natural_answer_grading: review.automatic_natural_answer_grading ?? 'not_assessed',
      tasks: review.tasks.map(t => ({ id: t.id, seed: t.seed, core: t.core_business_success, accuracy: t.answer_accuracy, classification: t.audit_conclusion, notes: t.notes, answer_errors: t.answer_errors, grader_root_causes: t.grader_root_causes })) };
  }
  async function pogoReview(file, expected, label) {
    const review = await json(file, `${label}:independent_app_review`), dir = path.dirname(file), summary = await json(path.join(dir, 'summary.json'), `${label}:raw_app_summary`);
    requireValue(review.kind === 'independent-full-pogo-provenance-and-business-review' && review.raw_data_unchanged === true && maybeDate(review.reviewed_at), `${label} independent review is invalid.`);
    requireValue(maybeDate(summary.finished_at) && summary.kind === 'candidate-full-pogo-general-web-glm-evidence' && summary.production_19777_touched === false, `${label} completed isolated app evidence is missing.`);
    requireValue(new URL(summary.app).port === '19778', `${label} is not the independent candidate application.`);
    const skill = summary.evaluated_shared_skill_version, frozen = await bytes(path.join(dir, 'evaluated-browser-use.SKILL.md'), `${label}:evaluated_skill`);
    requireValue(skill?.full_file_sha256 === expected.shared_skill_file_sha256 && skill.body_sha256 === expected.shared_skill_body_sha256 && sha(frozen) === skill.full_file_sha256 && sha(skillBody(frozen)) === skill.body_sha256, `${label} evaluated live/frozen/CLI Skill versions differ.`);
    if (review.evaluated_shared_skill_version) for (const field of ['full_file_sha256', 'body_sha256']) requireValue(review.evaluated_shared_skill_version[field] === skill[field], `${label} review Skill version mismatch.`);
    requireValue(review.task_count === summary.tasks?.length && review.tasks?.length === review.task_count && summary.model_completed_count === review.task_count, `${label} review/raw task denominator differs.`);
    const sourceTasks = new Map(summary.tasks.map(t => [t.id, t])), seen = new Set();
    for (const task of review.tasks) {
      requireValue(safeName(`${task.id}.json`) && !seen.has(task.id), `${label} task path/identity is invalid.`); seen.add(task.id);
      const raw = await json(path.join(dir, `${task.id}.json`), `${label}:raw_task`), source = sourceTasks.get(task.id);
      requireValue(source && equal(raw, source) && raw.session_id === task.session_id && raw.run_end?.end_reason === 'completed' && raw.status === 'model_run_completed', `${label} raw task completion/identity differs.`);
      requireValue(task.provenance?.model_run_completed === true && task.provenance.original_answer_retained === true && task.provenance.only_browser_model_tool_calls === true && task.review_status === 'verified_controlled_task', `${label} has unverified task provenance.`);
      requireValue(raw.actual_finished_actions?.some(a => a.ok === true && a.result?.backend === 'native'), `${label} native acknowledgements are missing.`);
    }
    requireValue(review.verified_task_count === count(review.tasks, 'review_status', 'verified_controlled_task'), `${label} verified count differs from tasks.`);
    for (const [name, digest] of Object.entries(review.source_artifact_sha256 ?? {})) {
      requireValue(safeName(name) && hex(digest), `${label} declared artifact reference is invalid.`);
      requireValue(sha(await bytes(path.join(dir, name), `${label}:declared_review_source`)) === digest, `${label} declared artifact SHA mismatch.`);
    }
    requireValue(sha(await bytes(path.join(dir, 'harness-source.mjs'), `${label}:harness_source`)) === summary.harness_sha256, `${label} harness SHA mismatch.`);
    if (review.runtime_artifact_sha256) requireValue(hex(review.runtime_artifact_sha256) && review.runtime_verification?.passed === true && sha(await bytes(review.runtime_verification.artifact, `${label}:runtime_verification`)) === review.runtime_artifact_sha256, `${label} runtime verification SHA mismatch.`);
    return { source: '独立真实应用复核；原始脚本分数未改', verified: review.verified_task_count, denominator: review.task_count,
      seed: summary.seed, model: summary.model, app: summary.app, skill_full_sha256: skill.full_file_sha256, skill_body_sha256: skill.body_sha256,
      fixture_source_sha256: summary.fixture_source_sha256, harness_sha256: summary.harness_sha256, production_19777_touched: false,
      context_cleanup_confirmed: review.context_cleanup_confirmed === true, raw_acceptance_pass_count: summary.acceptance_pass_count,
      tasks: review.tasks.map(t => ({ id: t.id, session_id: t.session_id, review_status: t.review_status, context_cleanup_confirmed: t.context_cleanup?.confirmed === true })) };
  }
  const v1 = await modelRun(options.v1, 'v1'), v2 = await modelRun(options.v2, 'v2');
  const v1Audit = await offline(options.v1Audit, v1, 'v1');
  const v2Audit = options.v2Audit ? await offline(options.v2Audit, v2, 'v2') : null;
  const v2Manual = options.v2ManualReview ? await manual(options.v2ManualReview, v2, 'v2') : null;
  const supplement = options.supplement ? await modelRun(options.supplement, 'supplement') : null;
  requireValue(!options.supplementManualReview || supplement, 'Supplement manual review requires --supplement.');
  const supplementManual = options.supplementManualReview ? await manual(options.supplementManualReview, supplement, 'supplement') : null;
  if (supplement) requireValue(supplement.run.tasks.length === 3 && equal([...new Set(supplement.run.tasks.map(t => t.id))].sort(), ['catalog-fit', 'catalog-paged', 'workbench-recover']), 'Supplement must remain the separate three-task component follow-up.');
  const oldPogo = await pogoReview(options.oldPogoReview, v1.metadata, 'old_pogo'), newPogo = await pogoReview(options.newPogoReview, v2.metadata, 'new_pogo');
  const generator = fileURLToPath(import.meta.url), generatorBytes = await bytes(generator, 'overview_generator');
  const manifest = { schema_version: 1, kind: 'general-web-independent-overview', created_at: new Date().toISOString(),
    official_scores_recomputed: false, combined_success_rate: 'not_defined', scope: 'three_controlled_synthetic_site_families_not_general_internet',
    generator: { path: generator, sha256: sha(generatorBytes), postprocess_version: 'general-web-overview-v3' },
    inputs: [...artifacts.values()], checks: { completed_model_runs: true, cli_context_cleanup_confirmed: true, audit_original_bytes_consistent: true,
      app_raw_review_skill_provenance_consistent: true, daily_19777_unchanged_in_app_evidence: true },
    v1: { original_score: v1.summary, metadata: v1.metadata, offline_review: v1Audit },
    v2: { original_score: v2.summary, metadata: v2.metadata, offline_review: v2Audit, manual_review: v2Manual },
    supplement: supplement ? { original_score: supplement.summary, metadata: supplement.metadata, manual_review: supplementManual } : { status: 'not_run', denominator: null },
    old_pogo: oldPogo, new_pogo: newPogo,
    interpretation: ['原始成绩、离线复核、真实应用验收和补测使用独立分母；不计算合并通用成功率。',
      '三个合成站点族的 seed / 模板覆盖，不等于真实网站泛化或通用 SOTA。',
      'grader、共享 Skill / adapter 和 seed 均发生变化，不能把轮次差异单独归因于模型改善。',
      '真实 Pogo 应用的 context cleanup 尚未确认；CLI 清理证据必须确认。日常 19777 未被应用验收修改。',
      ...(supplementManual?.automatic_natural_answer_grading === 'not_reliably_validated' ? ['三项补测仍出现自然答案判据误拒；自动自然答案判分尚未可靠验证。真实额外分页描述错误保留，不再追加特判或付费追分。'] : [])] };
  return { manifest, verifyInputs: async () => { for (const artifact of artifacts.values()) requireValue(sha(await readFile(artifact.path)) === artifact.sha256, 'Input changed before overview write.'); } };
}

function reviewLine(review) {
  return review ? escape(`${review.source}：核心 ${review.core}/${review.denominator}；完整答案 ${review.accuracy}/${review.denominator}，错误 ${review.accuracy_failed}，未知 ${review.accuracy_unknown}；完整任务判据误拒 ${review.full_task_checker_false_negatives}。`) : '未提供独立复核；不填入复核成绩。';
}

export function renderOverview(manifest) {
  const rows = [['v1 正式模型', manifest.v1], ['v2 正式模型', manifest.v2], ['v3 三项补测', manifest.supplement]];
  const models = rows.map(([name, run]) => `<tr><th>${escape(name)}</th><td>${run.original_score ? escape(`${run.original_score.passed}/${run.original_score.denominator}`) : '未运行'}</td><td>${escape(run.metadata?.seeds?.join('、') ?? '未运行')}</td><td>${escape(run.metadata?.checker_versions?.join('、') ?? '未运行')}</td><td>${escape(run.metadata?.shared_skill_body_sha256 ?? '未运行')}</td></tr>`).join('');
  const app = [['旧 Skill 应用验收', manifest.old_pogo], ['新 Skill 应用验收', manifest.new_pogo]].map(([name, r]) => `<tr><th>${escape(name)}</th><td>${escape(`${r.verified}/${r.denominator}`)}</td><td>${escape(r.seed)}</td><td>${escape(r.skill_body_sha256)}</td><td>${r.context_cleanup_confirmed ? '已确认' : '尚未确认；历史保留'}</td></tr>`).join('');
  const reviews = [manifest.v2.offline_review, manifest.v2.manual_review].filter(Boolean);
  const retention = reviews.flatMap(r => r.tasks).find(t => t.id === 'docs-retention' && t.accuracy === false);
  const supplement = manifest.supplement.manual_review;
  const pageError = supplement?.tasks.some(t => t.answer_errors?.some(e => e.type === 'incorrect_pagination_history'));
  const supplementReview = supplement ? `<section><h2>三项补测的独立逐项复核</h2><p>${reviewLine(supplement)}</p><table><thead><tr><th>任务</th><th>seed</th><th>核心业务</th><th>完整答案</th><th>复核分类</th></tr></thead><tbody>${supplement.tasks.map(t => `<tr><td>${escape(t.id)}</td><td>${escape(t.seed)}</td><td>${t.core === true ? '通过' : t.core === false ? '失败' : '未知'}</td><td>${t.accuracy === true ? '通过' : t.accuracy === false ? '失败' : '未知'}</td><td>${escape(t.classification)}</td></tr>`).join('')}</tbody></table>${pageError ? '<p>灯具目标与价格列表正确，但额外声称“第五页出现”有真实错误；完整答案错误保留。</p>' : ''}${supplement.automatic_natural_answer_grading === 'not_reliably_validated' ? '<p class="notice warn">自然答案自动判分仍未可靠验证：判据对货币列表、标题绑定、比较句及历史与当前结果的归属仍误判。本轮保留原分与真实答案错误，停止继续特判或付费追分。</p>' : ''}<p>这是三项独立补测的逐项证据复核，不与 v2 其他任务拼成 9/9，不覆盖正式补测分数。</p></section>` : '';
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none';style-src 'unsafe-inline';script-src 'none';base-uri 'none';form-action 'none';object-src 'none'"><title>通用 Web 验证总览</title><style>body{margin:0;padding:30px;font:15px/1.65 system-ui,sans-serif;color:#183044;background:#f4f6f8}main{max-width:1180px;margin:auto}section,.notice{background:white;border:1px solid #d9e3eb;border-radius:12px;padding:20px;margin:20px 0}h1,h2{line-height:1.3}table{border-collapse:collapse;width:100%;table-layout:fixed}th,td{padding:10px;text-align:left;border-bottom:1px solid #e1e7eb;overflow-wrap:anywhere;vertical-align:top}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}summary{cursor:pointer}.warn{border-left:5px solid #c08621}.muted{color:#65798a}</style></head><body><main><h1>通用 Web 验证总览</h1><p class="muted">${escape(manifest.created_at)} · 模型原始成绩与独立复核并列呈现</p><p class="notice warn">本次覆盖商品、知识库、工作台三个受控合成站点族。seed 和模板变化不等于真实网站泛化；各部分分母独立，不合并成通用 SOTA 成功率。</p><section><h2>正式模型成绩（原样保留）</h2><table><thead><tr><th>运行</th><th>原始通过数</th><th>seed</th><th>grader</th><th>共享 Skill 正文 SHA256</th></tr></thead><tbody>${models}</tbody></table><p>grader、共享 Skill / adapter 和 seed 并列变化；不能由通过率变化单独推断模型改善。三项补测仅针对修过的组件，与 v2 其他任务分开呈现。</p></section><section><h2>归档轨迹的独立复核</h2><p>v1 ${reviewLine(manifest.v1.offline_review)}</p><p>v1 核心业务判据误拒 ${escape(manifest.v1.offline_review.core_business_checker_false_negatives)}，其中完整任务误拒 ${escape(manifest.v1.offline_review.full_task_checker_false_negatives)}；存在真实附带答案错误的任务不会计入完整误拒。</p><p>v2 ${reviews.length ? reviews.map(reviewLine).join('<br>') : '未提供独立复核。'}</p>${retention ? '<p class="notice warn">v2 数据留存回答存在真实排序错误：把 24 天操作日志说成最短、12 天服务备份说成居中。所需期限值正确，完整答案仍不通过；修复判据不能抹去这项错误。</p>' : ''}<p>复核不覆盖原始正式分数，也不能覆盖预算、元数据、driver 或清理失败。歧义保留未知或人工复核。</p></section>${supplementReview}<section><h2>真实 Pogo 应用（独立验收）</h2><table><thead><tr><th>运行</th><th>独立复核</th><th>seed</th><th>运行 Skill 正文 SHA256</th><th>context cleanup</th></tr></thead><tbody>${app}</tbody></table><p>这是候选 19778 上的真实 Studio → GLM → 原生浏览器 → 合成业务验收，复核依据实际回执、服务端状态和自然回答；不是原始脚本 raw score。旧 Skill ${escape(manifest.old_pogo.verified)}/${escape(manifest.old_pogo.denominator)} 与新 Skill ${escape(manifest.new_pogo.verified)}/${escape(manifest.new_pogo.denominator)} 属于不同验收范围。</p><p class="notice warn">真实应用的 context cleanup 尚未确认，历史会话已保留；CLI 的全部已运行任务清理均已确认。应用验收记录声明日常 19777 保持未修改，不代表本总览重新探测了该服务。</p></section><details><summary>来源、原始 artifact SHA 与后处理 source SHA</summary><pre>${escape(JSON.stringify(manifest, null, 2))}</pre></details></main></body></html>`;
}

export async function writeOverviewFiles(outputDir, manifest) {
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 });
  await mkdir(outputDir, { mode: 0o700 }); // EEXIST is intentional: every overview is a new artifact.
  await writeFile(path.join(outputDir, 'overview.html'), renderOverview(manifest), { mode: 0o600 });
  await writeFile(path.join(outputDir, 'manifest.json'), JSON.stringify(manifest, null, 2), { mode: 0o600 });
}

export async function writeOverview(options) {
  const result = await createOverview(options), destination = path.resolve(options.outputDir);
  const inputDirs = [options.v1, options.v1Audit, options.v2, options.v2Audit, options.supplement,
    path.dirname(options.oldPogoReview), path.dirname(options.newPogoReview)].filter(Boolean).map(dir => path.resolve(dir));
  for (const dir of inputDirs) requireValue(destination !== dir && !destination.startsWith(dir + path.sep), 'Output cannot be inside an immutable input artifact directory.');
  requireValue(!result.manifest.inputs.some(artifact => artifact.path === destination), 'Output cannot replace an input file.');
  await result.verifyInputs();
  await writeOverviewFiles(destination, result.manifest);
  return result.manifest;
}

async function selfTest() {
  const assert = (await import('node:assert/strict')).default;
  const model = { run_id: 'test', config: {}, summary: {}, tasks: [] }, audit = { official_model_score_recomputed: false, original_run_id: 'test', original_config: {}, original_summary: {}, provenance: { original_report_sha256: 'a'.repeat(64) }, tasks: [] };
  assertAuditMatches(audit, model, 'a'.repeat(64));
  assert.throws(() => assertAuditMatches(audit, model, 'b'.repeat(64)), /source\/config\/summary/);
  const poison = '<script>alert("unsafe")</script><img src=x onerror="alert(1)">';
  const fake = { created_at: poison, v1: { original_score: { passed: 1, denominator: 18 }, metadata: { seeds: [1], checker_versions: [poison] }, offline_review: { source: poison, core: 18, accuracy: 17, denominator: 18, accuracy_failed: 1, accuracy_unknown: 0, full_task_checker_false_negatives: 16, core_business_checker_false_negatives: 17 } }, v2: { original_score: { passed: poison, denominator: 9 }, metadata: {}, offline_review: null, manual_review: null }, supplement: { status: 'not_run' }, old_pogo: { verified: 3, denominator: 3 }, new_pogo: { verified: 1, denominator: 1 } };
  fake.supplement.manual_review = { source: poison, core: 3, accuracy: 2, denominator: 3, accuracy_failed: 1, accuracy_unknown: 0, full_task_checker_false_negatives: 2,
    automatic_natural_answer_grading: 'not_reliably_validated', tasks: [{ id: poison, seed: poison, core: true, accuracy: false, classification: poison }] };
  const html = renderOverview(fake); assert(!html.includes('<script>')); assert(!html.includes('<img')); assert(html.includes('&lt;script&gt;')); assert(html.includes("script-src 'none'"));
  const scratch = await mkdtemp(path.join(os.tmpdir(), 'general-web-overview-'));
  try { const output = path.join(scratch, 'new'); await writeOverviewFiles(output, fake); const before = sha(await readFile(path.join(output, 'overview.html'))); await assert.rejects(writeOverviewFiles(output, fake), { code: 'EEXIST' }); assert.equal(sha(await readFile(path.join(output, 'overview.html'))), before); }
  finally { await rm(scratch, { recursive: true, force: true }); }
  return { source_consistency: 'passed', malicious_text_escaping: 'passed', refuse_overwrite: 'passed' };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const options = parseOverviewOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('Read-only general-web delivery overview; no model calls, scripts, or combined score.\nnode eval/general-web/overview.js --v1 V1_DIR --v1-audit AUDIT_R3_DIR --v2 V2_DIR --old-pogo-review OLD_REVIEW_JSON --new-pogo-review NEW_REVIEW_JSON --out NEW_DIR [--v2-audit AUDIT_DIR] [--v2-manual-review REVIEW_JSON] [--supplement THREE_TASK_DIR] [--supplement-manual-review REVIEW_JSON]\nnode eval/general-web/overview.js --self-test\n');
    else if (options['self-test']) process.stdout.write(JSON.stringify(await selfTest()) + '\n');
    else { const manifest = await writeOverview(options); process.stdout.write(JSON.stringify({ output_dir: options.outputDir, official_scores_recomputed: false, generator_sha256: manifest.generator.sha256, input_count: manifest.inputs.length }) + '\n'); }
  } catch (error) { process.stderr.write(`Overview failed: ${error.message}\n`); process.exitCode = 2; }
}
