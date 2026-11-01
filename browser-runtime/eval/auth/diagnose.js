// Offline diagnostics only. Never changes a model run, judge, prompt or score.
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { TASKS, expectedOutcome, judgePhase } from './tasks.js';
import { makeRedactor } from '../transport.js';

const taskPath = fileURLToPath(new URL('./tasks.js', import.meta.url));
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);

// Mirrors the auth adapter's public output contract. In particular, no prose,
// fences, extra top-level fields or trailing JSON are silently recovered.
export function parseProtocol(answer) {
  if (typeof answer !== 'string' || Buffer.byteLength(answer) > 64 * 1024) return null;
  let result;
  try { result = JSON.parse(answer); } catch { return null; }
  if (!record(result) || Object.keys(result).some(key => !['status', 'reason', 'data'].includes(key))) return null;
  if (!['completed', 'needs_human', 'blocked'].includes(result.status) || typeof result.reason !== 'string' || !result.reason.trim() || Buffer.byteLength(result.reason) > 256) return null;
  if (Object.hasOwn(result, 'data') && !record(result.data)) return null;
  return result;
}

function segments(row) {
  const parts = [[]];
  for (const event of row.events ?? []) {
    if (event.type === 'simulated_human_authorization') parts.push([]);
    else parts.at(-1).push(event);
  }
  return parts;
}
const everyMeasured = (values, required) => values.some(value => value === false) ? false : values.length !== required || values.some(value => value === null) ? null : true;
const counts = values => ({ total: values.length, passed: values.filter(value => value === true).length, failed: values.filter(value => value === false).length, not_measured: values.filter(value => value === null).length });

export function diagnose(input) {
  if (input.suite !== 'browser-auth-v1' || !Array.isArray(input.tasks)) throw new Error('expected browser-auth-v1 summary');
  const rows = input.tasks.map(row => {
    const task = TASKS.find(item => item.id === row.id);
    if (!task) throw new Error('unknown task in source report');
    const eventParts = segments(row), required = task.setup === 'assisted' ? 2 : 1;
    const phases = (row.phases ?? []).map((phase, index) => {
      const expected = expectedOutcome(task, index), parsed = parseProtocol(phase.final_answer);
      const completeEvidence = record(phase.server_state_before) && record(phase.server_state_after) && typeof phase.judgement?.observed?.access_status === 'string' && Array.isArray(eventParts[index]);
      // Oracle substitution is ONLY for behavior diagnosis on a cloned view.
      // It does not claim the model supplied this answer or passed the task.
      const behavior = completeEvidence ? judgePhase(task, {
        phase: index, outcome: structuredClone(expected), events: structuredClone(eventParts[index]),
        before: structuredClone(phase.server_state_before), after: structuredClone(phase.server_state_after),
        access: { status: phase.judgement.observed.access_status },
      }) : null;
      const claimMatches = parsed ? parsed.status === expected.status && parsed.reason === expected.reason && (!expected.data || parsed.data?.order_id === expected.data.order_id && parsed.data?.amount === expected.data.amount) : null;
      return { index, behavior_evidence_pass: behavior?.success ?? null, behavior_failures: behavior?.failures ?? ['phase_evidence_missing'],
        output_protocol_pass: typeof phase.final_answer === 'string' ? parsed !== null : null,
        semantic_claim_match: claimMatches, transport_completed: phase.end_reason === undefined ? null : phase.end_reason === 'completed',
        official_phase_pass: phase.judgement?.success ?? null, official_failures: [...(phase.judgement?.failures ?? [])],
        observed: behavior?.observed ?? null };
    });
    return { id: row.id, title: task.title, repeat: row.repeat, group: task.group, official_status: row.status,
      official_pass: row.status === 'passed', behavior_evidence_pass: everyMeasured(phases.map(phase => phase.behavior_evidence_pass), required),
      output_protocol_pass: everyMeasured(phases.map(phase => phase.output_protocol_pass), required),
      semantic_claim_match: everyMeasured(phases.map(phase => phase.semantic_claim_match), required),
      required_phases: required, observed_phases: phases.length, unmeasured_phases: Math.max(0, required - phases.length),
      simulated_human_interventions: row.simulated_human_interventions, context_cleaned: row.context_cleaned, phases };
  });
  const phaseRows = rows.flatMap(row => row.phases);
  return { suite: input.suite, mode: input.mode, model: input.model, run_id: input.run_id,
    interpretation: 'Offline diagnosis only. Behavior substitutes the oracle outcome on a COPY to check actual API/backend evidence independently of the model answer. Original official failures are never relabeled. Missing assisted phases remain unmeasured; a correct handoff is not a completed business query.',
    summary: { official: { total: rows.length, passed: rows.filter(row => row.official_pass).length },
      task_behavior: counts(rows.map(row => row.behavior_evidence_pass)), phase_behavior: counts(phaseRows.map(phase => phase.behavior_evidence_pass)),
      phase_output_protocol: counts(phaseRows.map(phase => phase.output_protocol_pass)), phase_semantic_claim: counts(phaseRows.map(phase => phase.semantic_claim_match)),
      required_phases: rows.reduce((total, row) => total + row.required_phases, 0), observed_phases: phaseRows.length, unmeasured_phases: rows.reduce((total, row) => total + row.unmeasured_phases, 0) }, rows };
}

const escape = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);
const verdict = value => value === null ? '未测量' : value ? '通过' : '未通过';
export function renderDiagnostics(result) {
  const groups = ['preauthorized_automatic', 'correct_handoff', 'human_assisted'];
  const score = (run, group) => {
    const rows = run.rows.filter(row => row.group === group);
    return `${rows.filter(row => row.official_pass).length}/${rows.length}`;
  };
  const version = (run, index) => /-baseline$/.test(run.label) ? '基线' : /-v(\d+)$/.exec(run.label)?.[1] ? `V${/-v(\d+)$/.exec(run.label)[1]}` : `运行 ${index + 1}`;
  // Only trusted, static catalog titles bypass the generic Bearer scrubber.
  // Every dynamic field still uses the already-sanitized diagnostic payload.
  const displayRows = run => run.rows.map(row => ({ ...row, title: TASKS.find(task => task.id === row.id)?.title ?? '未知任务' }));
  const overview = result.runs.map((run, index) => `<tr${index === result.runs.length - 1 ? ' class="latest"' : ''}><th scope="row"><a href="#run-${index}">${escape(version(run, index))}<span class="jump">查看逐项 ↓</span></a></th><td class="total-score">${run.summary.official.passed}/${run.summary.official.total}</td>${groups.map(group => `<td>${score(run, group)}</td>`).join('')}</tr>`).join('');
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>认证评测 · 三版本结果概览</title><style>
  html{scroll-behavior:smooth}body{font:15px/1.6 system-ui,sans-serif;background:#f3f5f8;color:#1a2635;margin:0;padding:32px}main{max-width:1200px;margin:auto}h1{font-size:29px;line-height:1.3;margin:0 0 8px}h2{font-size:21px;line-height:1.4;margin-top:0;overflow-wrap:anywhere}.intro{margin:0 0 24px;color:#637386}.notice{background:#e6eff8;border-left:4px solid #3175ad;padding:14px 18px;margin:20px 0}section{background:white;border:1px solid #dce4eb;border-radius:12px;padding:24px;margin:24px 0;scroll-margin-top:20px}section.overview{margin-top:20px}.overview h2{margin-bottom:4px}.overview .muted{margin-top:0}.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:12px;border-bottom:1px solid #e3e9ef}thead th{font-size:13px;color:#53677c;font-weight:600}.comparison-table{min-width:690px}.comparison-table tbody th{width:125px}.comparison-table td{font-size:21px;font-weight:600;font-variant-numeric:tabular-nums}.comparison-table .total-score{font-size:25px;color:#173c65}.latest{background:#eef8f3}.latest .total-score{color:#176a48}.jump{display:block;font-size:12px;font-weight:400;color:#637386}a{color:#235f98;text-decoration:none}a:hover{text-decoration:underline}a:focus-visible{outline:2px solid #235f98;outline-offset:4px}.run-head{display:flex;gap:16px;align-items:baseline;justify-content:space-between}.run-head a{font-size:13px;white-space:nowrap}summary{cursor:pointer}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;background:#f6f8fa;padding:14px}.muted{color:#637386;font-size:13px}.hash{overflow-wrap:anywhere}details{margin-top:18px}.group-note{margin:16px 0 0;font-size:13px;color:#53677c}@media(max-width:650px){body{padding:18px}section{padding:16px}h1{font-size:25px}.run-head{display:block}.run-head a{display:inline-block;margin-bottom:10px}}
  </style></head><body><main id="overview"><h1>认证评测 · 正式结果概览</h1><p class="intro">先看所有版本的正式得分，再查看各阶段行为与输出诊断。</p><section class="overview" aria-labelledby="comparison-title"><h2 id="comparison-title">${result.runs.length} 个版本并列对比</h2><p class="muted">保留原始严格判分；预授权、正确交接和人工辅助分别展示。</p><div class="table-scroll"><table class="comparison-table"><thead><tr><th scope="col">版本</th><th scope="col">正式总分</th><th scope="col">预授权自动查询</th><th scope="col">正确停止 / 交接</th><th scope="col">模拟人工授权后继续</th></tr></thead><tbody>${overview}</tbody></table></div><p class="group-note">总分包括三种不同条件，不代表完全自主成功率。人工授权为测试控制器明确模拟；MFA任务只验证正确停下。</p></section><p class="notice">“行为通过”不等于模型答案正确或正式任务通过。未执行的辅助阶段标为未测量，不推定成功。以下各版本明细保留历史失败。</p>
  ${result.runs.map((run, index) => { const rows = displayRows(run); return `<section id="run-${index}"><div class="run-head"><h2>${escape(version(run, index))} · ${escape(run.model)}</h2><a href="#overview">返回版本对比 ↑</a></div><p class="muted">${escape(run.label)}</p><p><strong>正式通过 ${run.summary.official.passed}/${run.summary.official.total}</strong>；已执行阶段行为通过 ${run.summary.phase_behavior.passed}/${run.summary.phase_behavior.total}；输出协议通过 ${run.summary.phase_output_protocol.passed}/${run.summary.phase_output_protocol.total}；后续未执行 ${run.summary.unmeasured_phases} 阶段。</p><p class="muted hash">任务来源 SHA-256：${escape(run.input_sha256)}。行为诊断使用相同判题器和实际 API/服务端证据，替换答案仅用于独立检查行为，不计入正式结果。</p><div class="table-scroll"><table><thead><tr><th>任务 / 轮次</th><th>正式结果</th><th>行为证据</th><th>输出协议</th><th>阶段</th></tr></thead><tbody>${rows.map(row => `<tr><td>${escape(row.title)} / ${row.repeat + 1}</td><td>${escape(row.official_status)}</td><td>${verdict(row.behavior_evidence_pass)}</td><td>${verdict(row.output_protocol_pass)}</td><td>${row.observed_phases}/${row.required_phases}</td></tr>`).join('')}</tbody></table></div><details><summary>逐阶段诊断与原始失败原因</summary><pre>${escape(JSON.stringify(rows, null, 2))}</pre></details></section>`; }).join('')}
  <p class="muted">此工具不运行模型，不重放浏览器操作，不修改源报告。不同运行的并列展示不自动证明因果改进；任务、提示词、预算和初始化条件仍需核对。</p></main></body></html>`;
}

export async function generateDiagnostics(files, outputDir) {
  if (!files.length || files.length > 4) throw new Error('provide 1 to 4 completed summary.json inputs');
  const taskSHA = sha(await readFile(taskPath));
  const result = { schema_version: 1, generated_at: new Date().toISOString(), task_source_sha256: taskSHA, runs: [] };
  for (const filename of files) {
    const bytes = await readFile(filename);
    if (bytes.length > 64 * 1024 * 1024) throw new Error('summary exceeds diagnostic size limit');
    const input = JSON.parse(bytes);
    if (!input.completed_at) throw new Error('source evaluation is still running');
    const savedHash = Object.entries(input.config?.files ?? {}).find(([name]) => name.endsWith('/eval/auth/tasks.js'))?.[1];
    if (savedHash !== taskSHA) throw new Error('task source differs from the recorded judge; refusing retrospective reinterpretation');
    result.runs.push({ ...diagnose(input), label: path.basename(path.dirname(filename)), input_sha256: sha(bytes) });
  }
  // Sources should already be sanitized, but preserve the whole-artifact
  // boundary for diagnostic metadata and error fields as well.
  const baseRedact = makeRedactor();
  const redact = value => baseRedact(value).replace(/\bmock_(?:session|access|pat)_[a-f0-9]{48}\b/g, '[REDACTED_MOCK_CREDENTIAL]');
  const clean = JSON.parse(redact(JSON.stringify(result)));
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 });
  await mkdir(outputDir, { mode: 0o700 });
  await writeFile(path.join(outputDir, 'diagnostic.json'), JSON.stringify(clean, null, 2), { mode: 0o600 });
  await writeFile(path.join(outputDir, 'report.html'), renderDiagnostics(clean), { mode: 0o600 });
  return clean;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2), outputAt = args.indexOf('--out');
    if (outputAt < 1 || outputAt !== args.length - 2) throw new Error('Usage: node eval/auth/diagnose.js SUMMARY.json [SECOND.json] --out NEW_DIRECTORY');
    const outputDir = path.resolve(args.at(-1)), result = await generateDiagnostics(args.slice(0, outputAt).map(file => path.resolve(file)), outputDir);
    process.stdout.write(JSON.stringify({ output_dir: outputDir, runs: result.runs.map(run => ({ label: run.label, summary: run.summary })) }) + '\n');
  } catch (error) { process.stderr.write(`Auth diagnostics failed: ${makeRedactor()(error.message)}\n`); process.exitCode = 1; }
}
