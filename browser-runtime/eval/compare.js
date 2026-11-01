import { readFile, writeFile, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { summarize, summarizeToolEvents } from './report.js';

const finite = value => typeof value === 'number' && Number.isFinite(value);
const escape = value => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
const number = value => finite(value) ? new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value) : '未测量';
const percent = value => finite(value) ? `${number(value * 100)}%` : '未测量';
const duration = value => finite(value) ? `${number(value / 1000)} 秒` : '未测量';
const delta = (before, after) => finite(before) && finite(after) ? after - before : null;
const signed = value => finite(value) ? `${value > 0 ? '+' : ''}${number(value)}` : '—';
const keyOf = task => JSON.stringify([task.id, task.repeat ?? null]);
const canonical = value => Array.isArray(value) ? value.map(canonical) : value && typeof value === 'object'
  ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const stableJSON = value => JSON.stringify(canonical(value));
const statusLabel = value => ({ passed: '通过', failed: '未通过', error: '执行错误', timeout: '超时', budget_exceeded: '达到预算', skipped: '跳过' })[value] ?? value;

function taskMap(report) {
  const rows = new Map();
  for (const task of report.tasks ?? []) {
    const key = keyOf(task);
    if (rows.has(key)) throw new TypeError(`duplicate task id/repeat: ${key}`);
    rows.set(key, task);
  }
  return rows;
}

function observedValues(report, key, fallback) {
  const values = (report.tasks ?? []).filter(task => task.status !== 'skipped').map(task => task[key] ?? fallback ?? null);
  if (!values.length) values.push(fallback ?? null);
  return [...new Set(values.map(stableJSON))].sort().map(value => JSON.parse(value));
}

function fixtureManifest(report) {
  const files = report.config?.files;
  if (!files || typeof files !== 'object' || Array.isArray(files)) return null;
  const entries = Object.entries(files).flatMap(([file, hash]) => {
    const match = /(?:^|\/)eval\/fixtures\/(.+)$/.exec(file.replaceAll('\\', '/'));
    return match ? [[match[1], typeof hash === 'string' && hash ? hash : null]] : [];
  }).sort(([a], [b]) => a.localeCompare(b));
  // The fixture defines both site behavior and judging. Missing source metadata
  // must not be treated as an empty, matching benchmark implementation.
  return entries.length ? Object.fromEntries(entries) : null;
}

function configuration(report) {
  const config = report.config ?? {};
  const rows = [...taskMap(report)].sort(([a], [b]) => a.localeCompare(b));
  const budget = key => rows.map(([task, value]) => ({ task, value: value[key] ?? config[key] ?? null }));
  return {
    mode: report.mode ?? null,
    task_repeats: rows.map(([key]) => key),
    fixture_manifest: fixtureManifest(report),
    seed: report.seed ?? null,
    repeat: config.repeat ?? null,
    requested_model: report.model ?? null,
    resolved_models: observedValues(report, 'model', report.model),
    profile: observedValues(report, 'profile', config.profile),
    system_prompt_sha256: observedValues(report, 'system_prompt_sha256', config.system_prompt_sha256),
    max_turns: config.max_turns ?? null,
    token_budget: config.token_budget ?? null,
    time_budget_ms: budget('time_budget_ms'),
    max_steps: budget('max_steps'),
    concurrency: config.concurrency ?? null,
    human_assistance: config.human_assistance ?? null,
    capture_frames: config.capture_frames ?? null,
    external_web_access: config.external_web_access ?? null,
    sees_images: observedValues(report, 'sees_images', config.sees_images),
    compatibility: observedValues(report, 'compatibility', config.compatibility),
    max_tokens: observedValues(report, 'max_tokens', config.max_tokens),
    context_window: observedValues(report, 'context_window', config.context_window),
    runtime: config.runtime ?? null,
    source_sha256: config.source_sha256 ?? null,
    adapter_sha256: config.adapter_sha256 ?? null,
    tool_schema_sha256: observedValues(report, 'tool_schema_sha256', config.tool_schema_sha256),
  };
}

const LABELS = {
  mode: '评测模式', task_repeats: '任务 ID 与重复次数配对', fixture_manifest: '任务环境与判题器指纹', seed: '随机种子', repeat: '重复执行配置',
  requested_model: '请求模型', resolved_models: '实际模型', profile: 'Agent profile', system_prompt_sha256: '系统提示词指纹',
  max_turns: '每任务轮数上限', token_budget: '每任务 token 预算', time_budget_ms: '实际任务时间上限', max_steps: '实际任务步骤上限',
  concurrency: '并发数', human_assistance: '人工协助', capture_frames: '动作后截图记录', external_web_access: '外部网页访问边界', sees_images: '模型视觉能力', compatibility: '模型兼容协议',
  max_tokens: '模型输出上限', context_window: '模型上下文上限', runtime: 'Node / 平台 / 浏览器运行时',
  source_sha256: '源码指纹', adapter_sha256: 'Adapter 二进制指纹', tool_schema_sha256: '工具 schema 指纹',
};
const IMPLEMENTATION = new Set(['source_sha256', 'adapter_sha256', 'tool_schema_sha256']);
const containsUnknown = value => value === null || value === undefined || (Array.isArray(value) ? value.some(containsUnknown) : typeof value === 'object' && Object.values(value).some(containsUnknown));

function resultRow(task) {
  if (!task) return null;
  return { id: task.id, title: task.title, repeat: task.repeat, category: task.category, status: task.status,
    duration_ms: task.duration_ms ?? null, steps: task.steps ?? null, usage: task.usage ?? null,
    usage_partial: task.usage_partial === true, ...summarizeToolEvents(task) };
}

/** Pure comparison: derive both summaries from saved tasks, never run a browser/model. */
export function compareReports(before, after) {
  const beforeMetrics = summarize(before.tasks ?? [], before.mode);
  const afterMetrics = summarize(after.tasks ?? [], after.mode);
  const left = configuration(before), right = configuration(after);
  const configurationChecks = Object.keys(LABELS).map(field => {
    const unknown = containsUnknown(left[field]) || containsUnknown(right[field]);
    const equal = stableJSON(left[field]) === stableJSON(right[field]);
    return { field, label: LABELS[field], group: IMPLEMENTATION.has(field) ? 'implementation' : 'protocol',
      status: unknown ? 'unknown' : equal ? 'same' : 'changed', before: left[field], after: right[field] };
  });
  const beforeRows = taskMap(before), afterRows = taskMap(after);
  const paired = [...new Set([...beforeRows.keys(), ...afterRows.keys()])].sort().map(key => ({
    key, before: resultRow(beforeRows.get(key)), after: resultRow(afterRows.get(key)),
  }));
  const changed = configurationChecks.filter(check => check.group === 'protocol' && check.status === 'changed');
  const unknown = configurationChecks.filter(check => check.group === 'protocol' && check.status === 'unknown');
  return {
    schema_version: 1,
    before: { run_id: before.run_id ?? null, mode: before.mode, finished_at: before.finished_at ?? null, summary: beforeMetrics },
    after: { run_id: after.run_id ?? null, mode: after.mode, finished_at: after.finished_at ?? null, summary: afterMetrics },
    configuration_checks: configurationChecks,
    protocol_status: changed.length ? 'changed' : unknown.length ? 'unknown' : 'same',
    paired_tasks: paired,
    complete: Boolean(before.finished_at && after.finished_at),
    causal_claim_supported: false,
  };
}

function formatConfig(value) {
  if (value === null || value === undefined) return '未记录';
  if (typeof value === 'object') return escape(JSON.stringify(value, null, 2));
  return escape(value);
}

function pairBars(left, right, format, ceiling) {
  const maximum = ceiling ?? Math.max(finite(left) ? left : 0, finite(right) ? right : 0);
  return [left, right].map((value, index) => `<div class="bar-row"><span class="bar-label">${index ? '后' : '前'}</span><span class="bar-track"><i class="${index ? 'after' : 'before'}" style="width:${finite(value) && maximum > 0 ? Math.min(100, Math.max(0, value / maximum * 100)).toFixed(2) : '0'}%"></i></span><b>${format(value)}</b></div>`).join('');
}

export function renderComparison(before, after, { postprocessing = null } = {}) {
  const comparison = compareReports(before, after);
  const left = comparison.before.summary, right = comparison.after.summary;
  const fixture = before.mode !== 'model-evaluation' || after.mode !== 'model-evaluation';
  const metricRows = [
    ['任务通过率', left.pass_rate, right.pass_rate, percent, 'pp'],
    ['最终未通过任务', left.total - left.passed, right.total - right.passed, number],
    ['模型工具调用', left.tool_calls, right.tool_calls, number],
    ['中途工具失败', left.tool_failures, right.tool_failures, number],
    ['工具调用失败率', left.tool_failure_rate, right.tool_failure_rate, percent, 'pp'],
    ['受工具失败影响的任务', left.affected_tasks, right.affected_tasks, number],
    ['失败后最终恢复通过', left.recovered_tasks, right.recovered_tasks, number],
    ['未结束工具调用', left.tool_pending_calls, right.tool_pending_calls, number],
    ['任务耗时中位数', left.duration_ms.median, right.duration_ms.median, duration, 'ms'],
    ['任务耗时 P95', left.duration_ms.p95, right.duration_ms.p95, duration, 'ms'],
    ['已记录输入 token', left.usage?.input, right.usage?.input, number],
    ['已记录输出 token', left.usage?.output, right.usage?.output, number],
    ['浏览器步骤合计', left.steps.total, right.steps.total, number],
  ].map(([label, a, b, format, unit]) => {
    const value = delta(a, b);
    const difference = value === null ? '—' : unit === 'pp' ? `${signed(value * 100)} 个百分点` : unit === 'ms' ? `${signed(value / 1000)} 秒` : signed(value);
    return `<tr><th scope="row">${label}</th><td>${format(a)}</td><td>${format(b)}</td><td>${difference}</td></tr>`;
  }).join('');
  const configRows = group => comparison.configuration_checks.filter(check => check.group === group).map(check => `<tr><th scope="row">${escape(check.label)}</th><td><span class="tag ${check.status}">${({ same: '一致', changed: '有差异', unknown: '信息不足' })[check.status]}</span></td><td><pre>${formatConfig(check.before)}</pre></td><td><pre>${formatConfig(check.after)}</pre></td></tr>`).join('');
  const pairedRows = comparison.paired_tasks.map(({ before: a, after: b }) => {
    const title = b?.title || a?.title || b?.id || a?.id;
    const repeat = (b?.repeat ?? a?.repeat);
    return `<tr><th scope="row">${escape(title)}<small>${escape(b?.id ?? a?.id)} · 第 ${Number.isInteger(repeat) ? repeat + 1 : '未记录'} 次</small></th><td>${escape(statusLabel(a?.status) ?? '缺少任务')}</td><td>${escape(statusLabel(b?.status) ?? '缺少任务')}</td><td>${number(a?.tool_failures)} → ${number(b?.tool_failures)}</td><td>${duration(a?.duration_ms)} → ${duration(b?.duration_ms)}</td><td>${number(a?.usage?.input)} → ${number(b?.usage?.input)}</td></tr>`;
  }).join('');
  const checkText = comparison.protocol_status === 'same' ? '已记录的任务与协议配置一致' : comparison.protocol_status === 'changed' ? '任务或协议配置存在差异' : '部分关键配置未记录，无法确认完全一致';
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; connect-src 'none'"><meta name="color-scheme" content="light dark"><title>浏览器评测 · 改造前后对比</title><style>
:root{font:14px/1.6 ui-sans-serif,system-ui,-apple-system,sans-serif;color:#172626;background:#f3f6f5;--surface:#fff;--line:#dce5e2;--muted:#647571;--accent:#16765d;--soft:#edf5f1;--before:#929cb6;--after:#16765d;--notice:#fff5df;--warning:#80500f}*{box-sizing:border-box}body{margin:0}main{max-width:1220px;margin:auto;padding:36px 26px 60px}h1{font-size:30px;line-height:1.3;margin:8px 0 14px;letter-spacing:-.7px}h2{font-size:19px;margin:0 0 14px}h3{font-size:15px}.eyebrow{font-size:12px;letter-spacing:1px;font-weight:750;color:var(--accent)}.runs{display:flex;gap:10px 28px;flex-wrap:wrap;color:var(--muted);overflow-wrap:anywhere}.notice{border:1px solid #dfc699;background:var(--notice);color:var(--warning);border-radius:12px;padding:15px 18px;margin:20px 0}.notice p{margin:4px 0 0}.charts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin:22px 0}.chart,section{border:1px solid var(--line);border-radius:13px;background:var(--surface);padding:22px}section{margin:20px 0}.bar-row{display:flex;align-items:center;gap:10px;margin:12px 0;font-size:12px}.bar-label{color:var(--muted)}.bar-track{flex:1;height:9px;background:var(--soft);border-radius:3px;overflow:hidden}.bar-track i{display:block;height:100%;border-radius:3px}.bar-track .before{background:var(--before)}.bar-track .after{background:var(--after)}.bar-row b{min-width:86px;text-align:right}.note,small{color:var(--muted);font-size:12px}.note{margin-top:-6px}small{display:block;font-weight:400}.table-wrap{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:13px;text-align:left}th,td{padding:12px;border-bottom:1px solid var(--line);vertical-align:top}thead th{color:var(--muted);background:var(--soft);font-weight:550;white-space:nowrap}tbody th{min-width:150px;font-weight:550}tr:last-child>*{border-bottom:0}.tag{display:inline-block;font-size:11px;padding:2px 7px;border-radius:5px;white-space:nowrap;background:var(--soft);color:var(--muted)}.tag.changed,.tag.unknown{background:var(--notice);color:var(--warning)}.tag.same{color:var(--accent)}pre{font:11px/1.55 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:180px;overflow:auto;min-width:140px;max-width:370px;margin:0}.config-table td{max-width:390px}summary{cursor:pointer;font-weight:650}summary:focus-visible{outline:2px solid var(--accent)}footer{font-size:12px;color:var(--muted);margin-top:22px}.legend{font-size:12px;color:var(--muted)}
@media(max-width:720px){main{padding:24px 14px 40px}h1{font-size:25px}.charts{grid-template-columns:1fr}.chart,section{padding:17px}.runs{display:block}.runs span{display:block;margin-top:5px}}
@media(prefers-color-scheme:dark){:root{color:#e2ece7;background:#111a17;--surface:#19241f;--line:#32413a;--muted:#a0b4a9;--accent:#8dd3b2;--soft:#243a2e;--after:#8dd3b2;--before:#a5b1ce;--notice:#30291b;--warning:#e7c280}.notice{border-color:#685534}}
@media print{main{max-width:none;padding:10px}body{background:white}.chart,section{break-inside:avoid}.table-wrap{overflow:visible}}
</style></head><body><main><header><div class="eyebrow">BROWSER-USE · BEFORE / AFTER</div><h1>浏览器评测改造前后对比</h1><div class="runs"><span>前：${escape(before.run_id || '未记录')}</span><span>后：${escape(after.run_id || '未记录')}</span></div></header>
<aside class="notice"><strong>${checkText}</strong><p>这是一组已保存运行的描述性对比。单轮或未受控的多轮结果，不能证明改造导致提速、降本或成功率提升。源码和二进制指纹变化单独列为预期实现变化，不自动判定任务协议不可比。</p>${fixture ? '<p><strong>至少一侧是参考脚本验证，不能解释为模型成功率对比。</strong></p>' : ''}${!comparison.complete ? '<p><strong>至少一侧尚无完成标记；当前仅为部分结果。</strong></p>' : ''}</aside>
<div class="legend">灰紫色：改造前 · 绿色：改造后。每组图单独缩放；数值越低是否更好取决于指标。</div><div class="charts"><div class="chart"><h2>任务通过率</h2>${pairBars(left.pass_rate, right.pass_rate, percent, 1)}<p class="note">${left.passed}/${left.total} → ${right.passed}/${right.total}；分母始终包含失败与跳过。</p></div><div class="chart"><h2>中途工具失败</h2>${pairBars(left.tool_failures, right.tool_failures, number)}<p class="note">模型工具调用 ${number(left.tool_calls)} → ${number(right.tool_calls)}；与最终任务异常分开。</p></div><div class="chart"><h2>任务耗时中位数</h2>${pairBars(left.duration_ms.median, right.duration_ms.median, duration)}<p class="note">有耗时记录 ${left.duration_ms.measured} → ${right.duration_ms.measured} 次；不是整个运行的墙钟时间。</p></div><div class="chart"><h2>已记录输入 token</h2>${pairBars(left.usage?.input, right.usage?.input, number)}<p class="note">输入字段有记录 ${left.usage_field_measured_tasks.input} → ${right.usage_field_measured_tasks.input} 次。缓存可能属于输入子集，不重复相加。</p></div></div>
<section><h2>指标对照</h2><p class="note">差值为“后 − 前”，只描述这两次运行。未知值不按零处理；不同任务覆盖率的合计不直接比较。</p><div class="table-wrap"><table><thead><tr><th>指标</th><th>改造前</th><th>改造后</th><th>差值</th></tr></thead><tbody>${metricRows}</tbody></table></div><p class="note">工具事件覆盖：${left.tool_metrics_measured_tasks}/${left.eligible} → ${right.tool_metrics_measured_tasks}/${right.eligible} 次已尝试。部分用量任务：${left.usage_partial_tasks} → ${right.usage_partial_tasks}。费用未记录时不据 token 推算账单。</p></section>
<section><h2>逐任务配对</h2><p class="note">按任务 ID 与重复索引配对；缺失的一侧明确显示，不删除不匹配任务。</p><div class="table-wrap"><table><thead><tr><th>任务</th><th>前：结果</th><th>后：结果</th><th>中途工具失败</th><th>耗时</th><th>输入 token</th></tr></thead><tbody>${pairedRows}</tbody></table></div></section>
<section><h2>任务与协议配置核对</h2><p class="note">缺少关键元数据标为信息不足，即使两侧都缺少也不判为一致。配置一致也不等于受控因果实验。</p><div class="table-wrap"><table class="config-table"><thead><tr><th>配置</th><th>检查</th><th>改造前</th><th>改造后</th></tr></thead><tbody>${configRows('protocol')}</tbody></table></div></section>
<section><details><summary>实现版本变化（与任务协议分开）</summary><p class="note">此类变化通常正是改造内容；报告模块自身变化也会影响源码指纹。</p><div class="table-wrap"><table class="config-table"><thead><tr><th>版本信息</th><th>检查</th><th>改造前</th><th>改造后</th></tr></thead><tbody>${configRows('implementation')}</tbody></table></div></details></section>
<footer>纯离线对比 · 仅读取已保存 JSON · 不运行模型、不访问网站、不重放操作 · 不修改原始结果${postprocessing ? `<p>比较报告生成：${escape(postprocessing.generated_at)}。比较器源码修改：${escape(postprocessing.compare_modified_at)}。后轮评测结束：${escape(postprocessing.after_finished_at ?? '未记录')}。</p><p>${postprocessing.compare_differs_from_after_snapshot === true ? '当前比较器与后轮运行时保存的源码指纹不同；这是评测结束后的离线后处理，不能称整个当前工作树仍等同于执行快照。' : postprocessing.compare_differs_from_after_snapshot === false ? '当前比较器源码与后轮保存的对应指纹一致。' : '后轮未保存比较器指纹，无法核对后处理工具版本。'}</p><details><summary>离线后处理版本记录</summary><pre>${escape(JSON.stringify(postprocessing, null, 2))}</pre></details>` : ''}</footer></main></body></html>`;
}

async function main(argv) {
  if (argv.includes('--help')) {
    process.stdout.write('Usage: node eval/compare.js BEFORE_SUMMARY.json AFTER_SUMMARY.json --out NEW_REPORT.html [--json NEW_COMPARISON.json]\nReads saved data only. Existing output files are never overwritten.\n');
    return;
  }
  const [beforePath, afterPath, ...flags] = argv;
  let output, jsonOutput;
  for (let i = 0; i < flags.length; i += 2) {
    if (!flags[i + 1]) throw new Error(`missing value for ${flags[i]}`);
    if (flags[i] === '--out') output = flags[i + 1];
    else if (flags[i] === '--json') jsonOutput = flags[i + 1];
    else throw new Error(`unknown option ${flags[i]}`);
  }
  if (!beforePath || !afterPath || !output) throw new Error('two summary paths and --out NEW_REPORT.html are required');
  const before = JSON.parse(await readFile(beforePath, 'utf8'));
  const after = JSON.parse(await readFile(afterPath, 'utf8'));
  const ownPath = new URL(import.meta.url);
  const [ownSource, ownStat] = await Promise.all([readFile(ownPath), stat(ownPath)]);
  const ownSHA = createHash('sha256').update(ownSource).digest('hex');
  const savedSHA = Object.entries(after.config?.files ?? {}).find(([file]) => /(?:^|\/)eval\/compare\.js$/.test(file.replaceAll('\\', '/')))?.[1] ?? null;
  const postprocessing = { generated_at: new Date().toISOString(), compare_modified_at: ownStat.mtime.toISOString(),
    after_finished_at: after.finished_at ?? null, compare_sha256: ownSHA, after_snapshot_compare_sha256: savedSHA,
    compare_differs_from_after_snapshot: typeof savedSHA === 'string' ? ownSHA !== savedSHA : null };
  const html = renderComparison(before, after, { postprocessing });
  await writeFile(output, html, { flag: 'wx', mode: 0o600 });
  if (jsonOutput) await writeFile(jsonOutput, JSON.stringify({ ...compareReports(before, after), postprocessing }, null, 2), { flag: 'wx', mode: 0o600 });
  process.stdout.write(JSON.stringify({ report: resolve(output), comparison: jsonOutput ? resolve(jsonOutput) : null }) + '\n');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(error => { process.stderr.write(`Comparison failed: ${error.message}\n`); process.exitCode = 1; });
}
