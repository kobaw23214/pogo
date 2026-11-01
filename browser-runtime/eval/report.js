// Offline report rendering. Artifact paths are relative to report.html; no scripts
// or remote resources are included, and opening a report never replays actions.
const MODES = new Set(['fixture-validation', 'model-evaluation']);
const STATUSES = new Set(['passed', 'failed', 'error', 'timeout', 'budget_exceeded', 'skipped']);
const USAGE_FIELDS = ['input', 'output', 'cache_read', 'reasoning'];
const LABELS = {
  passed: '通过', failed: '未通过', error: '执行错误', timeout: '超时',
  budget_exceeded: '达到预算', skipped: '跳过',
};

function finite(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0;
}

function status(task) {
  return STATUSES.has(task.status) ? task.status : 'error';
}

function ratio(numerator, denominator) {
  return denominator > 0 ? numerator / denominator : null;
}

function distribution(values) {
  const measured = values.filter(finite).sort((a, b) => a - b);
  if (!measured.length) return { total: null, median: null, p95: null, measured: 0 };
  const middle = Math.floor(measured.length / 2);
  return {
    total: measured.reduce((sum, value) => sum + value, 0),
    median: measured.length % 2 ? measured[middle] : (measured[middle - 1] + measured[middle]) / 2,
    p95: measured[Math.ceil(measured.length * 0.95) - 1],
    measured: measured.length,
  };
}

/** Model tool calls only. Browser proxy events/row.tool_calls use another unit. */
export function summarizeToolEvents(task) {
  const events = Array.isArray(task.events) ? task.events : [];
  const observed = events.some(event => event && ['tool_start', 'tool_end', 'message', 'end'].includes(event.type));
  if (!observed) return { tool_calls: null, tool_failures: null, tool_pending_calls: null, tool_unknown_outcomes: null };
  const starts = events.filter(event => event?.type === 'tool_start');
  const ends = events.filter(event => event?.type === 'tool_end');
  // Old traces sometimes retain only end events. Do not count both halves of a
  // call, and preserve unmatched ends when a partial trace lost its start.
  let toolCalls = Math.max(starts.length, ends.length);
  let pending = Math.max(0, starts.length - ends.length);
  if ([...starts, ...ends].every(event => typeof event.id === 'string' && event.id)) {
    const remaining = new Map();
    for (const event of starts) remaining.set(event.id, (remaining.get(event.id) ?? 0) + 1);
    let unmatchedEnds = 0;
    for (const event of ends) {
      if (remaining.get(event.id)) remaining.set(event.id, remaining.get(event.id) - 1);
      else unmatchedEnds++;
    }
    pending = [...remaining.values()].reduce((sum, count) => sum + count, 0);
    toolCalls = starts.length + unmatchedEnds;
  }
  return {
    tool_calls: toolCalls,
    tool_failures: ends.filter(event => event.ok === false).length,
    tool_pending_calls: pending,
    tool_unknown_outcomes: pending + ends.filter(event => typeof event.ok !== 'boolean').length,
  };
}

function aggregate(tasks, mode) {
  const counts = Object.fromEntries([...STATUSES].map(key => [key, 0]));
  for (const task of tasks) counts[status(task)]++;
  const total = tasks.length;
  const eligible = total - counts.skipped;
  const completed = counts.passed + counts.failed;
  const attempted = tasks.filter(task => status(task) !== 'skipped');
  const recordedUsage = attempted.filter(task => USAGE_FIELDS.some(key => finite(task.usage?.[key])));
  const usage = recordedUsage.length ? Object.fromEntries(USAGE_FIELDS.map(key => {
    const values = recordedUsage.map(task => task.usage?.[key]).filter(finite);
    return [key, values.length ? values.reduce((sum, value) => sum + value, 0) : null];
  })) : null;
  const recordedCost = attempted.filter(task => finite(task.cost) && typeof task.cost_unit === 'string' && task.cost_unit.trim());
  const costUnits = [...new Set(recordedCost.map(task => task.cost_unit.trim()))];
  const cost = costUnits.length === 1 ? recordedCost.reduce((sum, task) => sum + task.cost, 0) : null;
  const toolRows = attempted.map(task => ({ task, ...summarizeToolEvents(task) })).filter(row => row.tool_calls !== null);
  const sumTools = key => toolRows.length ? toolRows.reduce((sum, row) => sum + row[key], 0) : null;
  const affected = toolRows.filter(row => row.tool_failures > 0);
  return {
    mode, total, eligible, completed,
    passed: counts.passed, failed: counts.failed, errors: counts.error,
    timeouts: counts.timeout, budget_exceeded: counts.budget_exceeded, skipped: counts.skipped,
    // Every selected task remains in the primary denominator, including skipped
    // tasks. Stopping after one easy success must not inflate the headline score.
    success_rate: mode === 'model-evaluation' ? ratio(counts.passed, total) : null,
    pass_rate: ratio(counts.passed, total),
    attempted_success_rate: ratio(counts.passed, eligible),
    completion_rate: ratio(completed, total),
    duration_ms: distribution(attempted.map(task => task.duration_ms)),
    steps: distribution(attempted.map(task => task.steps)),
    human_interventions: distribution(attempted.map(task => task.human_interventions)).total,
    duplicate_submissions: distribution(attempted.map(task => task.duplicate_submissions)).total,
    human_interventions_measured_tasks: attempted.filter(task => finite(task.human_interventions)).length,
    duplicate_submissions_measured_tasks: attempted.filter(task => finite(task.duplicate_submissions)).length,
    usage, usage_measured_tasks: recordedUsage.length,
    usage_partial_tasks: attempted.filter(task => task.usage_partial === true).length,
    usage_field_measured_tasks: Object.fromEntries(USAGE_FIELDS.map(key => [key, attempted.filter(task => finite(task.usage?.[key])).length])),
    cost, cost_unit: costUnits.length === 1 ? costUnits[0] : null,
    cost_measured_tasks: recordedCost.length,
    cost_units: costUnits,
    tool_calls: sumTools('tool_calls'),
    tool_failures: sumTools('tool_failures'),
    tool_failure_rate: toolRows.length ? ratio(sumTools('tool_failures'), sumTools('tool_calls')) : null,
    affected_tasks: toolRows.length ? affected.length : null,
    recovered_tasks: toolRows.length ? affected.filter(row => status(row.task) === 'passed').length : null,
    tool_pending_calls: sumTools('tool_pending_calls'),
    tool_unknown_outcomes: sumTools('tool_unknown_outcomes'),
    tool_metrics_measured_tasks: toolRows.length,
  };
}

/**
 * Primary rates use all selected task repeats, including skipped tasks.
 * Unknown metrics remain null. Observed usage/cost totals include measured tasks
 * only; coverage fields make incomplete accounting explicit.
 */
export function summarize(tasks, mode) {
  if (!Array.isArray(tasks)) throw new TypeError('tasks must be an array');
  if (!MODES.has(mode)) throw new TypeError('unsupported evaluation mode');
  if (tasks.some(task => !task || typeof task !== 'object')) throw new TypeError('each task must be an object');
  const categories = new Map();
  for (const task of tasks) {
    const name = String(task.category || '未分类');
    if (!categories.has(name)) categories.set(name, []);
    categories.get(name).push(task);
  }
  return {
    ...aggregate(tasks, mode),
    categories: [...categories].map(([category, entries]) => ({ category, ...aggregate(entries, mode) })),
  };
}

function escape(value) {
  return String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
}

function json(value) {
  try { return escape(JSON.stringify(value, null, 2) ?? ''); } catch { return '无法序列化'; }
}

// Encode each filename segment instead of trusting URI-like text. No scheme,
// origin, parent traversal, fragments, or remote resources may escape the run.
function artifactPath(value, image = false) {
  if (typeof value !== 'string' || !value || /[\\:\u0000-\u001f\u007f]/.test(value)) return null;
  const segments = value.split('/');
  if (segments.some(part => !part || part === '.' || part === '..')) return null;
  const extension = image ? /\.(?:png|jpe?g|webp)$/i : /\.(?:jsonl?|txt|log)$/i;
  if (!extension.test(value)) return null;
  return segments.map(encodeURIComponent).join('/');
}

function number(value) {
  return finite(value) ? new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value) : '未测量';
}

function percentage(value) {
  return finite(value) ? `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 }).format(value * 100)}%` : '—';
}

function costNumber(value) {
  if (!finite(value)) return '未测量';
  if (value > 0 && value < 0.00000001) return value.toExponential(4);
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 8 }).format(value);
}

function duration(value) {
  if (!finite(value)) return '未测量';
  if (value < 1000) return `${number(value)} ms`;
  if (value < 60_000) return `${number(value / 1000)} 秒`;
  return `${Math.floor(value / 60_000)} 分 ${number((value % 60_000) / 1000)} 秒`;
}

function badge(task) {
  const key = status(task);
  return `<span class="badge ${key}">${LABELS[key]}</span>`;
}

function repeatLabel(task) {
  // The runner stores a zero-based repeat index for deterministic fixture IDs.
  return Number.isInteger(task.repeat) && task.repeat >= 0 ? task.repeat + 1 : '未记录';
}

function bar(value, maximum) {
  if (!finite(value)) return '';
  const width = maximum > 0 ? Math.min(100, Math.max(0, value / maximum * 100)) : 0;
  return `<span class="bar" aria-hidden="true"><i style="width:${width.toFixed(2)}%"></i></span>`;
}

function taskDetail(task, index) {
  const events = Array.isArray(task.events) ? task.events : [];
  const frames = Array.isArray(task.frames) ? task.frames : [];
  const traceHref = artifactPath(task.trace_path);
  const toolMetrics = summarizeToolEvents(task);
  const eventHTML = events.map((event, eventIndex) => {
    const item = event && typeof event === 'object' ? event : { text: event };
    const action = typeof item.action === 'string' ? item.action : item.type || '事件';
    const eventNumber = item.seq ?? item.step ?? eventIndex + 1;
    return `<details class="event"><summary><span class="event-number">${escape(eventNumber)}</span><strong>${escape(action)}</strong><span class="muted">${finite(item.at_ms) ? `+${duration(item.at_ms)}` : ''}${finite(item.duration_ms) ? ` · ${duration(item.duration_ms)}` : ''}</span>${item.ok === false ? '<span class="event-error">失败</span>' : ''}</summary><pre>${json(event)}</pre></details>`;
  }).join('');
  const frameHTML = frames.map(frame => {
    const href = artifactPath(frame?.path, true);
    if (!href) return `<p class="muted">已忽略非本地截图路径：${escape(frame?.path)}</p>`;
    return `<figure><a href="${escape(href)}" target="_blank" rel="noopener noreferrer"><img src="${escape(href)}" alt="${escape(task.title || task.id)} · 步骤 ${escape(frame.step ?? '—')}" loading="lazy"></a><figcaption>步骤 ${escape(frame.step ?? '—')} · <a href="${escape(href)}" target="_blank" rel="noopener noreferrer">查看原图</a></figcaption></figure>`;
  }).join('');
  return `<details class="task-detail" id="task-${index}"><summary>${badge(task)}<strong>${escape(task.title || task.id || `任务 ${index + 1}`)}</strong><span class="muted">${escape(task.id)} · 第 ${escape(repeatLabel(task))} 次</span></summary><div class="task-body">
    <p class="reason">${escape(task.reason || '未提供判定说明')}</p>
    <div class="task-facts"><span>耗时 <b>${duration(task.duration_ms)}</b></span><span>浏览器步骤 <b>${number(task.steps)}</b></span><span>模型工具调用 / 中途失败 <b>${number(toolMetrics.tool_calls)} / ${number(toolMetrics.tool_failures)}</b></span><span>人工介入 <b>${number(task.human_interventions)}</b></span><span>重复提交 <b>${number(task.duplicate_submissions)}</b></span></div>
    ${task.usage_partial ? '<p class="muted">用量记录不完整：当前 token 和费用仅为已返回的记录，取消或失败请求可能有尚未结算的用量。</p>' : ''}
    ${task.final_answer ? `<details><summary>最终答复</summary><pre>${escape(task.final_answer)}</pre></details>` : ''}
    ${task.trace_path ? `<p>${traceHref ? `<a href="${escape(traceHref)}" target="_blank" rel="noopener noreferrer">打开已保存的原始轨迹</a>` : `<span class="muted">不可打开的轨迹路径：${escape(task.trace_path)}</span>`}</p>` : ''}
    <h3>步骤记录 <span class="muted">${events.length}</span></h3>${events.length ? eventHTML : '<p class="muted">未内嵌步骤记录。可查看上方本地轨迹文件（如有）。</p>'}
    ${frames.length ? `<h3>已保存画面</h3><div class="frames">${frameHTML}</div>` : ''}
  </div></details>`;
}

/** Render a self-contained, script-free report. Screenshot/trace files stay local. */
export function renderReport(report) {
  if (!report || typeof report !== 'object') throw new TypeError('report must be an object');
  const tasks = report.tasks ?? [];
  const metrics = summarize(tasks, report.mode);
  const fixture = report.mode === 'fixture-validation';
  const headline = fixture ? '参考脚本通过率' : '模型任务成功率';
  const maxDuration = Math.max(0, ...tasks.map(task => finite(task.duration_ms) ? task.duration_ms : 0));
  const maxSteps = Math.max(0, ...tasks.map(task => finite(task.steps) ? task.steps : 0));
  const categoryRows = metrics.categories.map(category => `<tr><th scope="row">${escape(category.category)}</th><td>${category.total}</td><td>${category.passed}</td><td>${category.failed}</td><td>${category.errors + category.timeouts + category.budget_exceeded}</td><td>${category.skipped}</td><td class="score">${percentage(category.pass_rate)}</td></tr>`).join('');
  const taskRows = tasks.map((task, index) => `<tr><th scope="row"><a href="#task-${index}">${escape(task.title || task.id || `任务 ${index + 1}`)}</a><small>${escape(task.id)} · 第 ${escape(repeatLabel(task))} 次</small></th><td>${escape(task.category || '未分类')}</td><td>${badge(task)}</td><td class="measure">${duration(task.duration_ms)}${bar(task.duration_ms, maxDuration)}</td><td class="measure">${number(task.steps)}${bar(task.steps, maxSteps)}</td><td class="reason-cell">${escape(task.reason || '—')}</td></tr>`).join('');
  const observedCost = metrics.cost !== null ? `${costNumber(metrics.cost)} ${escape(metrics.cost_unit)}` : metrics.cost_units.length > 1 ? '多币种，不合并' : '未测量';
  const usageHTML = USAGE_FIELDS.map((key, index) => `<div><dt>${['输入 token', '输出 token', '缓存读取 token', '推理 token'][index]}</dt><dd>${number(metrics.usage?.[key])}</dd><small>${metrics.usage_field_measured_tasks[key]} / ${metrics.eligible} 次已尝试有记录</small></div>`).join('');
  const runAlerts = [
    report.infrastructure_error ? `<aside class="notice"><strong>运行遇到基础设施错误</strong><p>${escape(report.infrastructure_error)}</p><p>未执行的任务仍保留在分母中；本轮结果不能单独归因于模型能力。</p></aside>` : '',
    report.cleanup_error ? `<aside class="notice"><strong>运行清理未正常完成</strong><p>${escape(report.cleanup_error)}</p></aside>` : '',
    report.started_at && !report.finished_at ? '<aside class="notice"><strong>运行中 · 当前为部分结果</strong><p>等待执行的任务暂列为跳过。请以运行完成后保存的最终报告为准。</p></aside>' : '',
  ].join('');
  const toolRows = tasks.map((task, index) => ({ task, index, ...summarizeToolEvents(task) })).filter(row => row.tool_failures > 0 || row.tool_unknown_outcomes > 0);
  const toolsHTML = `<section><h2>工具调用与恢复</h2><p class="section-note">最终任务异常与中途工具失败分别统计。模型发起一次工具调用计一次；浏览器 execute 请求和 batch 子动作不混入该次数。失败只认 tool_end 的 ok:false。</p><dl class="accounting"><div><dt>模型工具调用</dt><dd>${number(metrics.tool_calls)}</dd><small>${metrics.tool_metrics_measured_tasks} / ${metrics.eligible} 次已尝试有模型事件</small></div><div><dt>中途工具失败</dt><dd>${number(metrics.tool_failures)}</dd><small>占已记录模型工具调用 ${percentage(metrics.tool_failure_rate)}</small></div><div><dt>受影响任务 / 最终恢复通过</dt><dd>${number(metrics.affected_tasks)} / ${number(metrics.recovered_tasks)}</dd><small>每次任务重复最多计一次</small></div><div><dt>未收到结束事件的调用</dt><dd>${number(metrics.tool_pending_calls)}</dd><small>不视为调用成功</small></div><div><dt>未知结果的调用</dt><dd>${number(metrics.tool_unknown_outcomes)}</dd><small>包含未结束或缺少 ok 的事件</small></div></dl>${toolRows.length ? `<div class="table-wrap"><table><thead><tr><th>任务</th><th>模型调用</th><th>中途失败</th><th>未知结果</th><th>最终任务结果</th></tr></thead><tbody>${toolRows.map(row => `<tr><th scope="row"><a href="#task-${row.index}">${escape(row.task.title || row.task.id)}</a><small>第 ${escape(repeatLabel(row.task))} 次</small></th><td>${number(row.tool_calls)}</td><td>${number(row.tool_failures)}</td><td>${number(row.tool_unknown_outcomes)}</td><td>${badge(row.task)}${row.tool_failures && status(row.task) === 'passed' ? ' · 失败后恢复' : ''}</td></tr>`).join('')}</tbody></table></div>` : `<p class="muted">${metrics.tool_calls === null ? '没有可用的模型工具事件；旧数据及参考脚本不推断为零失败。' : '已记录的模型工具调用中，未发现中途失败或未知结果。'}</p>`}</section>`;
  return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; connect-src 'none'; object-src 'none'">
<meta name="color-scheme" content="light dark"><title>浏览器任务评测 · ${escape(report.run_id || '未命名运行')}</title>
<style>
:root{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#172626;background:#f3f6f5;line-height:1.6;font-size:14px;--surface:#fff;--border:#dce5e2;--muted:#647571;--accent:#16765d;--soft:#edf5f1;--code:#f5f7f6;--warn:#8a5410;--warning-bg:#fff5df}
*{box-sizing:border-box}body{margin:0}main{max-width:1260px;margin:0 auto;padding:40px 28px 64px}h1{font-size:30px;line-height:1.3;margin:8px 0 12px;letter-spacing:-.7px}h2{font-size:19px;margin:0 0 16px}h3{font-size:15px;margin:22px 0 10px}p{margin:10px 0}.eyebrow{font-size:12px;letter-spacing:1.4px;font-weight:750;color:var(--accent);text-transform:uppercase}.muted,small{color:var(--muted)}.metadata{display:flex;gap:8px 22px;flex-wrap:wrap;color:var(--muted);font-size:13px;overflow-wrap:anywhere}.notice{border:1px solid #e3c997;border-radius:12px;background:var(--warning-bg);color:var(--warn);padding:15px 18px;margin:22px 0}.notice strong{display:block}.notice p{margin:5px 0 0}.cards{display:grid;grid-template-columns:1.45fr repeat(3,1fr);gap:14px;margin:22px 0}.card,section{background:var(--surface);border:1px solid var(--border);border-radius:14px}.card{padding:20px}.card-label{color:var(--muted);font-size:13px}.card-value{display:block;font-size:30px;font-weight:750;line-height:1.5;letter-spacing:-.7px}.card:first-child{border-top:3px solid var(--accent)}.card:first-child .card-value{color:var(--accent)}.card small{display:block;font-size:12px}section{padding:24px;margin-top:20px}.section-note{color:var(--muted);margin-top:-8px;font-size:13px}.table-wrap{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:13px;text-align:left}th,td{padding:13px 12px;border-bottom:1px solid var(--border);vertical-align:top}thead th{font-size:12px;font-weight:600;color:var(--muted);white-space:nowrap;background:var(--soft)}tbody th{font-weight:550;min-width:170px}tbody th small{display:block;font-weight:400;font-size:11px}tbody tr:last-child>*{border-bottom:0}.score{font-weight:700;color:var(--accent)}a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}a:focus-visible,summary:focus-visible{outline:2px solid var(--accent);outline-offset:4px;border-radius:3px}.badge{display:inline-flex;white-space:nowrap;border-radius:5px;font-size:11px;font-weight:650;padding:2px 7px;background:var(--code);color:var(--muted)}.badge.passed{background:#e0f3e9;color:#196945}.badge.failed,.badge.error,.badge.timeout,.badge.budget_exceeded{background:#fceae7;color:#ab3b2e}.badge.skipped{background:#edf0ef;color:#63706c}.measure{min-width:110px;font-variant-numeric:tabular-nums}.bar{display:block;width:100%;height:4px;margin-top:9px;background:var(--soft);border-radius:3px;overflow:hidden}.bar i{display:block;height:100%;background:var(--accent);opacity:.65}.reason-cell{min-width:160px;max-width:360px;overflow-wrap:anywhere}.accounting{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:18px;margin:0}.accounting dt{font-size:12px;color:var(--muted)}.accounting dd{margin:2px 0;font-size:20px;font-weight:650;overflow-wrap:anywhere}.accounting small{font-size:11px}.task-detail{border:1px solid var(--border);border-radius:10px;margin-top:10px;scroll-margin-top:20px}.task-detail>summary{padding:14px;display:flex;align-items:center;gap:10px;flex-wrap:wrap;list-style:none}.task-detail>summary:before{content:'›';color:var(--muted);font-size:20px;line-height:1}.task-detail[open]>summary:before{transform:rotate(90deg)}summary{cursor:pointer;overflow-wrap:anywhere}summary::-webkit-details-marker{display:none}.task-detail[open]>summary{border-bottom:1px solid var(--border)}.task-detail>summary .muted{font-size:12px}.task-body{padding:4px 18px 18px}.reason{white-space:pre-wrap;overflow-wrap:anywhere}.task-facts{display:flex;gap:12px 24px;flex-wrap:wrap;font-size:12px;color:var(--muted);margin:14px 0}.task-facts b{color:inherit}.event{border:1px solid var(--border);border-radius:7px;margin:7px 0}.event summary{display:flex;gap:12px;align-items:baseline;padding:8px 12px;list-style:none;font-size:12px}.event-number{font-variant-numeric:tabular-nums;color:var(--muted);min-width:16px}.event-error{color:#ad4034}pre{font:12px/1.65 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;background:var(--code);padding:14px;border-radius:7px;max-height:520px;overflow:auto}details pre{margin:10px}.frames{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,300px),1fr));gap:16px}figure{margin:0}figure img{display:block;max-width:100%;width:100%;height:auto;border-radius:8px;border:1px solid var(--border);background:var(--code)}figcaption{font-size:12px;color:var(--muted);margin-top:5px}.empty{padding:25px 0;color:var(--muted)}footer{margin-top:22px;color:var(--muted);font-size:12px}footer p{margin:4px 0}
@media(max-width:760px){main{padding:24px 14px 40px}h1{font-size:25px}.cards{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.card{padding:15px}.card-value{font-size:25px}section{padding:17px}.accounting{grid-template-columns:repeat(2,minmax(0,1fr))}.event summary{flex-wrap:wrap;gap:5px 10px}}
@media(prefers-color-scheme:dark){:root{color:#e2ece7;background:#111a17;--surface:#19241f;--border:#32413a;--muted:#a0b4a9;--accent:#8dd3b2;--soft:#243a2e;--code:#111c17;--warn:#e7c280;--warning-bg:#30291b}.notice{border-color:#685534}.badge.passed{background:#234534;color:#ace2c2}.badge.failed,.badge.error,.badge.timeout,.badge.budget_exceeded{background:#492a25;color:#ffb6a9}.badge.skipped{background:#303a34;color:#bdc9c1}.event-error{color:#ffb6a9}}
@media print{body{background:white}main{max-width:none;padding:12px}.card,section,.task-detail{break-inside:avoid}a{color:inherit}.table-wrap{overflow:visible}.notice{color:#614200}footer{color:#444}}
</style></head><body><main>
<header><div class="eyebrow">Browser-use · Evaluation v1</div><h1>浏览器任务评测报告</h1><div class="metadata"><span>运行 ${escape(report.run_id || '未命名')}</span><span>${escape(report.started_at || '未记录时间')}</span><span>种子 ${escape(report.seed ?? '未记录')}</span><span>模型 ${escape(report.model || (fixture ? '未调用模型' : '未记录'))}</span></div></header>
<aside class="notice"><strong>${fixture ? '环境验证 · 此报告不是模型成功率，也不代表 SOTA 能力' : '模型评测 · 分数仅代表本次任务集与配置'}</strong><p>${fixture ? '本轮使用参考脚本验证任务环境、浏览器链路和判题器。参考脚本知道完成路径；通过率不能用于证明 Agent 自主完成任务的能力。' : '每个已选择的任务重复都计入总数。失败、错误、超时、预算耗尽和跳过均保留在主分母；不得据此直接推断外部基准排名。'}</p></aside>
${runAlerts}
<div class="cards"><div class="card"><span class="card-label">${headline}</span><strong class="card-value">${percentage(metrics.pass_rate)}</strong><small>${metrics.passed} / ${metrics.total} 次计划执行 · 包含 ${metrics.skipped} 次跳过</small></div><div class="card"><span class="card-label">完成判题</span><strong class="card-value">${metrics.completed} <span class="muted">/ ${metrics.total}</span></strong><small>通过 ${metrics.passed} · 未通过 ${metrics.failed}</small></div><div class="card"><span class="card-label">最终任务异常</span><strong class="card-value">${metrics.errors + metrics.timeouts + metrics.budget_exceeded}</strong><small>错误 ${metrics.errors} · 超时 ${metrics.timeouts} · 预算 ${metrics.budget_exceeded}</small></div><div class="card"><span class="card-label">任务耗时中位数</span><strong class="card-value">${duration(metrics.duration_ms.median)}</strong><small>P95 ${duration(metrics.duration_ms.p95)} · ${metrics.duration_ms.measured} 次有记录</small></div></div>
${toolsHTML}
<section><h2>分类表现</h2><p class="section-note">主分数以全部计划执行为分母；仅已尝试的辅助通过率为 ${percentage(metrics.attempted_success_rate)}（${metrics.passed} / ${metrics.eligible}），不作为主分数。重复任务按每次执行计数。</p>${metrics.total ? `<div class="table-wrap"><table><thead><tr><th>类别</th><th>计划</th><th>通过</th><th>未通过</th><th>异常</th><th>跳过</th><th>${fixture ? '脚本通过率' : '任务成功率'}</th></tr></thead><tbody>${categoryRows}</tbody></table></div>` : '<p class="empty">没有选择任务。本轮没有可计算的通过率。</p>'}</section>
<section><h2>任务结果</h2><p class="section-note">横条分别比较任务耗时与步骤数，不混用单位。点击任务名称定位下方详情；展开后可查看已保存的步骤与画面。</p>${metrics.total ? `<div class="table-wrap"><table><thead><tr><th>任务</th><th>类别</th><th>结果</th><th>耗时</th><th>步骤</th><th>判定说明</th></tr></thead><tbody>${taskRows}</tbody></table></div>` : '<p class="empty">暂无任务结果。</p>'}</section>
<section><h2>成本与执行记录</h2><p class="section-note">以下是已记录值的合计；未测量保留为空，不按零计算。token 字段按提供方原始口径展示，缓存与推理可能属于输入/输出的子集，不重复相加。</p>${metrics.usage_partial_tasks ? `<p class="notice">${metrics.usage_partial_tasks} 次任务的用量记录不完整。下列合计可能遗漏已取消或失败请求的用量，不能当作最终账单。</p>` : ''}<dl class="accounting">${usageHTML}<div><dt>已记录费用</dt><dd>${observedCost}</dd><small>${metrics.cost_measured_tasks} / ${metrics.eligible} 次已尝试有记录</small></div><div><dt>人工介入 / 重复提交</dt><dd>${number(metrics.human_interventions)} / ${number(metrics.duplicate_submissions)}</dd><small>分别 ${metrics.human_interventions_measured_tasks} / ${metrics.eligible}、${metrics.duplicate_submissions_measured_tasks} / ${metrics.eligible} 次有记录</small></div><div><dt>已记录任务耗时合计</dt><dd>${duration(metrics.duration_ms.total)}</dd><small>非整个运行的墙钟时间</small></div><div><dt>步骤中位数 / P95</dt><dd>${number(metrics.steps.median)} / ${number(metrics.steps.p95)}</dd><small>${metrics.steps.measured} 次已尝试有记录</small></div></dl></section>
<section><h2>轨迹回放</h2><p class="section-note">仅查看本次保存的步骤和截图，不重放操作，不向原网站发送请求。截图与原始轨迹链接需要和报告一起保留。</p>${tasks.map(taskDetail).join('') || '<p class="empty">暂无轨迹。</p>'}</section>
<section><details><summary><strong>运行配置与统计口径</strong></summary><p class="muted">通过必须由任务判题器确认，不能仅依据模型答复。跨版本对比应固定任务集、种子、模型、预算与初始状态。此处统计按任务重复聚合，尚不提供置信区间。</p><pre>${json({ schema_version: report.schema_version ?? 1, run_id: report.run_id, mode: report.mode, started_at: report.started_at, seed: report.seed, model: report.model ?? null, config: report.config ?? {}, summary: metrics })}</pre></details></section>
<footer><p>独立离线报告 · 无外部脚本、字体或网络资源。</p><p>任务描述、网页内容、模型答复和轨迹均以文本显示，内容不作为报告指令执行。</p></footer>
</main></body></html>`;
}
