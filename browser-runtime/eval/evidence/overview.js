import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = fileURLToPath(import.meta.url);
const REPO = fileURLToPath(new URL('../../../../', import.meta.url));
const IDS = ['cross-detail-mass', 'record-price-difference', 'document-duration', 'paged-tied-minimum'];
const LABELS = ['跨详情质量比较', '同表记录价格差值', '文档期限与单位', '完整范围与并列最低'];
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const isSHA = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const hashEqual = (actual, expected, role) => assert.ok(isSHA(expected) && actual === expected, `${role}: source SHA mismatch`);
export { hashEqual as assertSourceSHA };
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c]));
const bool = value => value === true ? true : value === false ? false : null;
const count = value => Number.isSafeInteger(value) && value >= 0 ? value : null;
const sum = (rows, key) => rows.every(row => count(row[key]) !== null) ? rows.reduce((total, row) => total + row[key], 0) : null;
const publicPath = filename => {
  const relative = path.relative(REPO, filename).split(path.sep).join('/');
  return relative.startsWith('pogo/browser-runtime/eval/runs/') && !relative.includes('../') ? relative : null;
};
async function artifact(filename, role) {
  const bytes = await readFile(filename);
  return { filename: path.resolve(filename), bytes, data: JSON.parse(bytes), source: { role, sha256: sha(bytes) } };
}
function usage(value) {
  return { input: count(value?.input), output: count(value?.output), cache_read: count(value?.cache_read), reasoning: count(value?.reasoning) };
}
function sameTaskIDs(tasks, ids) {
  assert.deepEqual([...tasks.map(t => t.id)].sort(), [...ids].sort(), 'Unexpected/missing/duplicate task ID');
}
function metadata(config) {
  return {
    revision: config.git_revision ?? null, source_sha256: config.source_sha256 ?? null,
    suite_source_sha256: config.suite_source_sha256 ?? null,
    skill_file_sha256: config.shared_skill_file_sha256 ?? null,
    skill_body_sha256: config.expected_shared_skill_body_sha256 ?? null,
    quantities_sha256: config.files?.['pogo/browser-runtime/src/quantities.js'] ?? null,
    evidence_sha256: config.files?.['pogo/browser-runtime/src/evidence.js'] ?? null,
    session_sha256: config.files?.['pogo/browser-runtime/src/session.js'] ?? null,
    browser_go_sha256: config.files?.['pogo/tools/browser.go'] ?? null,
  };
}
function budget(config) {
  return { max_turns: count(config.max_turns_per_task), token_budget: count(config.token_budget_per_task), timeout_ms: count(config.time_budget_ms_per_task), max_steps: count(config.max_steps_per_task) };
}
async function cliRun(directory, reviewFile, sources) {
  const run = await artifact(path.join(directory, 'summary.json'), 'cli_model_summary');
  const review = await artifact(reviewFile, 'cli_independent_review');
  sources.push(run.source, review.source);
  hashEqual(run.source.sha256, review.data.original_summary_sha256, 'CLI review');
  assert.equal(review.data.official_model_score_recomputed, false, 'Review must retain official score');
  assert.ok(run.data.completed_at && !run.data.cancelled, 'CLI run incomplete');
  sameTaskIDs(run.data.tasks, IDS); sameTaskIDs(review.data.reviews, IDS);
  const rows = [];
  for (const task of run.data.tasks) {
    const reviewed = review.data.reviews.find(t => t.id === task.id);
    const integrity = review.data.source_integrity.find(t => t.id === task.id);
    const tracePath = path.resolve(directory, task.trace_path);
    assert.ok(tracePath.startsWith(path.resolve(directory, 'traces') + path.sep), 'Trace outside run');
    const trace = await artifact(tracePath, `cli_trace_${task.id}`); sources.push(trace.source);
    hashEqual(trace.source.sha256, integrity?.trace_sha256, 'CLI trace');
    assert.equal(reviewed.original_status, task.status, 'Original interface status differs');
    const whole = reviewed.whole_answer_accuracy === 'verified' ? 'verified' : reviewed.whole_answer_accuracy === 'failed' ? 'failed' : 'needs_review';
    rows.push({ id: task.id, seed: task.seed, interface_status: task.status,
      protocol_failures: task.protocol?.failures ?? reviewed.original_protocol_failures ?? [],
      requested_facts_verified: bool(reviewed.requested_facts_verified), whole_answer_review: whole,
      source_integrity_verified: bool(integrity.source_integrity_verified), context_cleanup_confirmed: bool(task.context_cleaned),
      native_finished_calls: count(integrity.native_browser_calls), server_request_count: integrity.server_requests?.length ?? null,
      usage: usage(task.usage) });
  }
  assert.equal(rows.filter(t => t.whole_answer_review === 'verified').length, review.data.summary.whole_answers_verified, 'CLI review count differs');
  return { phase: 'cli_protocol_v1', label: 'CLI · 原协议版本', model: run.data.model,
    completed_at: run.data.completed_at, seeds: run.data.config.seeds, metadata: metadata(run.data.config), budget: budget(run.data.config),
    original_model_business_score: run.data.summary.model_business_score,
    original_interface_verified: run.data.summary.interface_verified, total: rows.length,
    independent_whole_answers_verified: rows.filter(t => t.whole_answer_review === 'verified').length,
    requested_facts_verified: rows.filter(t => t.requested_facts_verified === true).length,
    contexts_cleanup_confirmed: rows.filter(t => t.context_cleanup_confirmed === true).length,
    measured_usage: usage(run.data.summary.measured_usage), rows,
    public_original_report: publicPath(path.join(directory, 'report.html')),
    protocol_note: '文档任务只有 difference 调用，缺少协议要求的独立 compare；12 天差值与自然答案经独立复核正确。原接口失败保留。',
    attribution_note: 'CLI 使用较早的量值解析器；后续解析器或 DOM probe 修复不计入这次运行。' };
}
async function actualRun(directory, reviewFile, phase, label, sources) {
  const run = await artifact(path.join(directory, 'summary.json'), `${phase}_summary`);
  const review = await artifact(reviewFile, `${phase}_independent_review`);
  sources.push(run.source, review.source);
  hashEqual(run.source.sha256, review.data.original_summary_sha256, `${phase} review`);
  assert.equal(review.data.original_artifacts_rewritten, false, 'Review rewrote originals');
  assert.equal(review.data.official_score_recomputed, false, 'Review must retain official score');
  assert.ok(run.data.finished_at, 'Actual run incomplete');
  sameTaskIDs(run.data.tasks, IDS.slice(0, 2)); sameTaskIDs(review.data.tasks, IDS.slice(0, 2));
  const rows = [];
  for (const task of run.data.tasks) {
    assert.equal(task.status, 'model_run_completed', 'Actual task incomplete');
    const reviewed = review.data.tasks.find(t => t.id === task.id);
    const file = await artifact(path.join(directory, `${task.id}.json`), `${phase}_task_${task.id}`);
    sources.push(file.source); hashEqual(file.source.sha256, reviewed.task_artifact_sha256, `${phase} task`);
    assert.ok(['verified', 'needs_review', 'failed'].includes(reviewed.whole_answer_review), 'Unknown review verdict');
    rows.push({ id: task.id, seed: run.data.seed, requested_facts_verified: bool(reviewed.requested_goal_facts_verified),
      whole_answer_review: reviewed.whole_answer_review,
      context_cleanup_confirmed: bool(reviewed.context_cleanup_confirmed), initial_history_zero: bool(reviewed.initial_history_zero),
      native_finished_calls: count(reviewed.native_finished_call_count), server_request_count: count(reviewed.served_request_count),
      quantity_paths_and_prior_refs_checked: bool(reviewed.quantity_paths_and_prior_refs_checked ?? reviewed.quantity_ref_paths_and_raw_excerpts_verified),
      other_tool_attempts: reviewed.other_tool_attempts?.length ?? null,
      other_tools_denied: count(reviewed.other_tools_denied) ?? reviewed.other_tool_attempts?.filter(t => t.gate_denied === true).length ?? null,
      other_tools_successfully_executed: count(reviewed.other_tools_successfully_executed), usage: usage(reviewed.reported_usage) });
  }
  const runtime = run.data.runtime_verification?.recorded;
  assert.equal(runtime?.passed, true, 'Recorded runtime verification missing/failed');
  const config = review.data.same_run_configuration;
  hashEqual(run.data.evaluated_shared_skill_version.body_sha256, config.live_skill_body_sha256, `${phase} Skill body`);
  hashEqual(run.data.evaluated_shared_skill_version.full_file_sha256, config.live_skill_full_sha256, `${phase} Skill file`);
  const checkedRuntime = review.data.runtime;
  hashEqual(runtime.expected_build_manifest.source_sha256, checkedRuntime.build_source_sha256, `${phase} deployed source`);
  hashEqual(run.data.runtime_verification.artifact_sha256, checkedRuntime.runtime_verification_artifact_sha256, `${phase} runtime artifact`);
  assert.equal(rows.filter(t => t.whole_answer_review === 'verified').length, review.data.summary.whole_answers_clearly_verified, `${phase} whole-answer count differs`);
  assert.equal(rows.filter(t => t.whole_answer_review === 'needs_review').length, review.data.summary.whole_answers_needing_wording_review, `${phase} pending-review count differs`);
  return { phase, label, model: run.data.model, completed_at: run.data.finished_at, seeds: [run.data.seed], total: rows.length,
    original_acceptance_pass_count: run.data.acceptance_pass_count, original_complete_answer_accuracy: run.data.complete_answer_accuracy,
    requested_facts_verified: rows.filter(t => t.requested_facts_verified === true).length,
    independent_whole_answers_verified: rows.filter(t => t.whole_answer_review === 'verified').length,
    whole_answers_needing_review: rows.filter(t => t.whole_answer_review === 'needs_review').length,
    whole_answers_failed: rows.filter(t => t.whole_answer_review === 'failed').length,
    contexts_cleanup_confirmed: rows.filter(t => t.context_cleanup_confirmed === true).length,
    native_finished_calls: sum(rows, 'native_finished_calls'), server_request_count: sum(rows, 'server_request_count'),
    other_tool_attempts: sum(rows, 'other_tool_attempts'), other_tools_denied: sum(rows, 'other_tools_denied'), other_tools_successfully_executed: sum(rows, 'other_tools_successfully_executed'),
    metadata: { revision: runtime.expected_build_manifest.revision, build_source_sha256: runtime.expected_build_manifest.source_sha256,
      skill_file_sha256: config.live_skill_full_sha256, skill_body_sha256: config.live_skill_body_sha256,
      quantities_sha256: runtime.worker_files?.['quantities.js']?.actual ?? null,
      evidence_sha256: runtime.worker_files?.['evidence.js']?.actual ?? null,
      session_sha256: runtime.worker_files?.['session.js']?.actual ?? null,
      harness_sha256: run.data.harness_sha256, fixture_source_sha256: run.data.fixture_source_sha256 },
    runtime_artifact_sha256: run.data.runtime_verification.artifact_sha256,
    runtime_verified_before_run: Date.parse(runtime.verified_at) <= Date.parse(run.data.started_at),
    raw_browser_false_preserved: rows.length > 0 && review.data.tasks.every(t => t.raw_browser_false_preserved === true),
    expanded_skill_body_unredacted_byte_hash_verified: config.unredacted_expanded_body_byte_hash_verified === true,
    rows };
}
async function control(directory, kind, sources) {
  const item = await artifact(path.join(directory, 'summary.json'), `final_${kind}_control`); sources.push(item.source);
  const d = item.data; assert.ok(d.completed_at && !d.cancelled, 'Control incomplete');
  assert.equal(d.summary.control_only, true, 'Reference/noop must be controls');
  assert.equal(d.control, kind, 'Control kind mismatch');
  assert.ok(d.tasks.length > 0 && d.tasks.every(t => t.context_cleaned === true), 'Control cleanup unconfirmed');
  return { kind, planned: d.summary.planned, started: d.summary.started, interface_verified: d.summary.interface_verified,
    completed_at: d.completed_at, seeds: d.config.seeds, contexts_cleanup_confirmed: d.tasks.length,
    model_calls: 0, metadata: metadata(d.config), public_original_report: publicPath(path.join(directory, 'report.html')) };
}
export async function buildOverview(options) {
  const sources = [];
  const cli = await cliRun(options.cli, options['cli-review'], sources);
  const pre = await actualRun(options['pre-probe'], options['pre-probe-review'], 'pre_native_probe_fix', '实际应用 · 修复前候选', sources);
  const final = await actualRun(options['final-probe'], options['final-probe-review'], 'native_probe_candidate', '实际应用 · 最终 probe 候选', sources);
  const runtime = await artifact(options['runtime-final'], 'final_runtime_verification'); sources.push(runtime.source);
  const rd = runtime.data;
  assert.equal(rd.passed, true, 'Final runtime failed');
  assert.equal(rd.daily_19777?.exact_unchanged, true, 'Daily instance unchanged not verified');
  assert.ok(rd.services.length === 3 && rd.services.every(s => s.health === 'healthy' && s.source_matches_manifest && s.image_matches_manifest), 'Final service/source verification failed');
  hashEqual(runtime.source.sha256, final.runtime_artifact_sha256, 'Final live runtime');
  const controls = [await control(options.reference, 'reference', sources), await control(options.noop, 'noop', sources)];
  for (const c of controls) {
    hashEqual(c.metadata.evidence_sha256, final.metadata.evidence_sha256, 'Control DOM module');
    hashEqual(c.metadata.quantities_sha256, final.metadata.quantities_sha256, 'Control quantities module');
    hashEqual(c.metadata.session_sha256, final.metadata.session_sha256, 'Control session module');
    hashEqual(c.metadata.skill_file_sha256, final.metadata.skill_file_sha256, 'Control Skill');
  }
  const linuxBytes = await readFile(options['linux-log']);
  const linux = { source_log_sha256: sha(linuxBytes), status: null, exit_code: null, total: null, passed: null, failed: null, scope: null };
  sources.push({ role: 'final_linux_test_log', sha256: linux.source_log_sha256 });
  if (options['linux-review']) {
    const checked = await artifact(options['linux-review'], 'final_linux_test_review'); sources.push(checked.source);
    const ld = checked.data;
    hashEqual(linux.source_log_sha256, ld.source_log_sha256, 'Linux log review');
    for (const k of ['total', 'passed', 'failed']) assert.ok(count(ld[k]) !== null, 'Linux count missing');
    assert.ok(ld.passed + ld.failed <= ld.total, 'Linux counts inconsistent');
    assert.ok(['passed','failed','unknown'].includes(ld.status), 'Linux status unknown');
    if (ld.status === 'passed') assert.ok(ld.exit_code === 0 && ld.failed === 0 && ld.passed === ld.total && ld.skipped === 0 && ld.cancelled === 0, 'Linux pass without every test passing');
    const worker = rd.services.find(s => s.service === 'worker');
    assert.equal(ld.image_id, worker.image_id, 'Linux tested another worker image');
    assert.equal(ld.revision, rd.expected_build_manifest.revision, 'Linux tested another revision');
    Object.assign(linux, { status: ld.status, exit_code: count(ld.exit_code), total: ld.total, passed: ld.passed, failed: ld.failed,
      cancelled: count(ld.cancelled), skipped: count(ld.skipped), scope: typeof ld.scope === 'string' ? ld.scope : null });
    if (options['linux-provenance']) {
      const provenance = await artifact(options['linux-provenance'], 'final_linux_test_provenance'); sources.push(provenance.source);
      hashEqual(provenance.source.sha256, ld.provenance_sha256, 'Linux provenance');
      assert.equal(ld.image_id, worker.image_id, 'Linux tested another worker image');
      assert.equal(provenance.data.image_id, worker.image_id, 'Linux provenance image differs');
      assert.equal(provenance.data.source_revision, rd.expected_build_manifest.revision, 'Linux revision differs');
      assert.equal(provenance.data.exit_code, ld.exit_code, 'Linux provenance exit differs');
      Object.assign(linux, { image_sha256: ld.image_id, revision: ld.revision,
        provenance_sha256: provenance.source.sha256, test_file_count: count(ld.test_file_count),
        preload_guard_harness_only: bool(ld.preload_guard_harness_only), fixture_mount_correct: bool(ld.fixture_mount_correct),
        production_source_changed: bool(ld.production_source_changed), preload_guard_sha256: ld.preload_guard_sha256 });
    }
  }
  return { schema: 'browser-evidence-delivery-overview-v1', generated_at: new Date().toISOString(),
    host_report_generator: { sha256: sha(await readFile(HERE)), scope: 'Host-only postprocess added after the final candidate image; not part of that deployed build digest.' },
    scope: 'Four controlled synthetic task templates; one CLI seed and two selected actual-app tasks per candidate. No pooled model rate or broad website-generalization claim.',
    cli, pre_probe: pre, final_probe: final, controls, linux,
    runtime: { revision: rd.expected_build_manifest.revision, build_source_sha256: rd.expected_build_manifest.source_sha256,
      services_healthy: rd.services.filter(s => s.health === 'healthy').length, services_verified: rd.services.length,
      daily_19777_exact_unchanged: rd.daily_19777.exact_unchanged, verified_at: rd.verified_at,
      image_sha256: rd.services.map(s => ({ service: s.service, sha256: s.image_id })) },
    source_artifacts: sources };
}
function fraction(value, total) { return value === null || value === undefined ? '未评定' : `${value}/${total}`; }
function verdict(value) { return ({ verified: '明确通过', needs_review: '待复核', failed: '不通过', interface_verified: '接口通过', interface_failed: '接口未通过' })[value] ?? '未知'; }
function link(relative, outDir) {
  if (!relative) return '';
  const href = path.relative(outDir, path.join(REPO, relative)).split(path.sep).join('/');
  return `<a href="${esc(href)}">原始 CLI 报告</a>`;
}
export function renderHTML(m, outDir) {
  const table = group => `<table><thead><tr><th>任务</th><th>原接口</th><th>独立完整回答</th><th>目标事实</th><th>context 清理</th></tr></thead><tbody>${group.rows.map(t => `<tr><td>${esc(LABELS[IDS.indexOf(t.id)] ?? t.id)}</td><td>${t.interface_status ? esc(verdict(t.interface_status)) : '原接受分未自动评定'}</td><td>${esc(verdict(t.whole_answer_review))}</td><td>${t.requested_facts_verified === true ? '已核验' : '未确认'}</td><td>${t.context_cleanup_confirmed === true ? '已确认' : '未确认'}</td></tr>`).join('')}</tbody></table>`;
  const provenance = group => `<details><summary>版本与来源指纹</summary><dl>${Object.entries(group.metadata).map(([k,v]) => `<dt>${esc(k)}</dt><dd><code>${esc(v ?? '未记录')}</code></dd>`).join('')}</dl></details>`;
  const actual = group => `<article><p class="eyebrow">${esc(group.label)} · ${esc((group.metadata.revision ?? '未记录').slice(0,7))} · seed ${esc(group.seeds.join(', '))}</p><div class="score">${esc(fraction(group.independent_whole_answers_verified, group.total))}</div><p>独立完整回答明确通过 · ${esc(fraction(group.whole_answers_needing_review, group.total))} 待复核</p><p>目标事实 ${esc(fraction(group.requested_facts_verified, group.total))}；${esc(group.native_finished_calls)} 次 native 完成调用、${esc(group.server_request_count)} 次业务请求。额外工具尝试 ${esc(group.other_tool_attempts)} 次，拒绝 ${esc(group.other_tools_denied)} 次，成功执行 ${esc(group.other_tools_successfully_executed)} 次。</p><p class="muted">原接受分 ${esc(group.original_acceptance_pass_count === null ? 'null（未自动评定）' : group.original_acceptance_pass_count)}；context 清理 ${esc(fraction(group.contexts_cleanup_confirmed, group.total))} 已确认。会话历史保留。</p>${table(group)}${provenance(group)}</article>`;
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><title>浏览器结构化证据 · 交付验证</title><style>body{margin:0;background:#f2f5f8;color:#172433;font:15px/1.65 system-ui,sans-serif}main{max-width:1160px;margin:auto;padding:40px 24px}h1{font-size:30px;line-height:1.2;margin:0 0 16px}h2{font-size:21px}p{margin:10px 0}.cards{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px}article,section{background:white;border:1px solid #dbe3eb;border-radius:14px;padding:22px;margin:20px 0}.cards article{margin:0}.eyebrow,.muted{color:#536779}.score{font-size:42px;font-weight:700;color:#155a73}table{border-collapse:collapse;width:100%;font-size:13px}th,td{text-align:left;padding:9px 6px;border-bottom:1px solid #e3e9ee}th{color:#536779}code{font:12px ui-monospace,monospace;overflow-wrap:anywhere}dl{font-size:12px}dt{font-weight:600}dd{margin:0 0 10px}details{margin-top:16px}a{color:#155a73}footer{color:#536779;font-size:13px}@media(max-width:850px){.cards{grid-template-columns:1fr}main{padding:28px 16px}}</style></head><body><main><p class="eyebrow">DiscoverAgent / Pogo · GLM-5.3 · 2026-10-03</p><h1>浏览器结构化证据与数值核验</h1><p>真实 DOM 字段、同记录归属和原始量值引用支持自然回答。calculate 核验观察到的数值运算，完整答案由独立复核判断。</p><p class="muted">本页覆盖 4 类受控合成任务，以及两个应用候选各 2 项抽检。版本、seed 与 Skill 变化分别列出；不合并分母，不代表真实网站泛化或统计性能力提升。</p><section><p class="eyebrow">${esc(m.cli.label)} · seed ${esc(m.cli.seeds.join(', '))}</p><div class="cards"><div><div class="score">${esc(fraction(m.cli.original_interface_verified,m.cli.total))}</div><p>原接口契约通过</p></div><div><div class="score">${esc(fraction(m.cli.independent_whole_answers_verified,m.cli.total))}</div><p>独立完整回答复核通过</p></div></div><p>${esc(m.cli.protocol_note)}</p><p class="muted">原模型业务分数保留为 ${esc(m.cli.original_model_business_score === null ? 'null' : m.cli.original_model_business_score)}。CLI context 清理 ${esc(fraction(m.cli.contexts_cleanup_confirmed,m.cli.total))} 已确认。${esc(m.cli.attribution_note)}</p>${table(m.cli)}${link(m.cli.public_original_report,outDir)}${provenance(m.cli)}</section><h2>实际应用抽检 · 两个独立候选</h2><div class="cards">${actual(m.pre_probe)}${actual(m.final_probe)}</div><section><h2>最终候选的工程验证与控制</h2><p>部署 revision <code>${esc(m.runtime.revision)}</code>；服务健康及来源核验 ${esc(fraction(m.runtime.services_healthy,m.runtime.services_verified))}。日常 19777 实例在记录的验证时点保持不变。</p><p>Linux 测试：${esc(m.linux.status ?? '未独立评定')}，${esc(fraction(m.linux.passed,m.linux.total))}；进程 exit ${esc(m.linux.exit_code ?? '未知')}，失败 ${esc(m.linux.failed ?? '未知')}。${esc(m.linux.scope ?? '')}</p>${m.controls.map(c=>`<p>${c.kind === 'reference' ? '参考正对照' : '无动作负对照'}：接口通过 ${esc(fraction(c.interface_verified,c.planned))}，context 清理 ${esc(fraction(c.contexts_cleanup_confirmed,c.planned))}；模型调用 0。${link(c.public_original_report,outDir)}</p>`).join('')}<p class="muted">这些是工程检查和判题控制，不是模型成绩。应用 context 清理未确认；不能用 CLI 的清理结果替代。原始业务请求的 browser 标记与展开 Skill 的未脱敏字节核验边界保留在独立复核中。</p></section><section><h2>来源与公开边界</h2><p>报告只公开白名单统计与 SHA。私有账号、登录 Cookie、会话身份、原始 SSE 与配置未复制。后加报告生成器仅在宿主运行，不属于上方候选镜像，也不宣称当前全仓库与镜像摘要一致。</p><p>生成器 SHA <code>${esc(m.host_report_generator.sha256)}</code></p><details><summary>输入 artifact SHA 清单</summary><dl>${m.source_artifacts.map(s=>`<dt>${esc(s.role)}</dt><dd><code>${esc(s.sha256)}</code></dd>`).join('')}</dl></details></section><footer>原始结果保持不变；独立复核不替换原接口或模型分数。生成时间 ${esc(m.generated_at)}。</footer></main></body></html>`;
}
export async function writeOverview(options) {
  const manifest = await buildOverview(options);
  return writeNewArtifacts(options.out, manifest);
}
export async function writeNewArtifacts(directory, manifest) {
  const out = path.resolve(directory);
  await mkdir(out, { recursive: false });
  await writeFile(path.join(out, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', { flag: 'wx' });
  await writeFile(path.join(out, 'overview.html'), renderHTML(manifest, out), { flag: 'wx' });
  return { out, source_artifacts: manifest.source_artifacts.length, host_generator_sha256: manifest.host_report_generator.sha256 };
}
const HELP = 'Host-only overview; reads immutable inputs and refuses an existing output directory.\n--cli DIR --cli-review FILE --pre-probe DIR --pre-probe-review FILE --final-probe DIR --final-probe-review FILE --runtime-final FILE --reference DIR --noop DIR --linux-log FILE [--linux-review FILE --linux-provenance FILE] --out NEW_DIR';
async function main() {
  const options = {};
  for (let i=2; i<process.argv.length; i++) {
    const key=process.argv[i]; if (key==='--help') { console.log(HELP); return; }
    assert.ok(/^--[a-z-]+$/.test(key) && process.argv[i+1] && !process.argv[i+1].startsWith('--'), 'Invalid option');
    options[key.slice(2)] = process.argv[++i];
  }
  for (const key of ['cli','cli-review','pre-probe','pre-probe-review','final-probe','final-probe-review','runtime-final','reference','noop','linux-log','out']) assert.ok(options[key], `Missing --${key}`);
  console.log(JSON.stringify(await writeOverview(options)));
}
if (process.argv[1] && path.resolve(process.argv[1]) === HERE) main().catch(error => { console.error(error.message); process.exitCode=1; });
