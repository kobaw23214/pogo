const escape = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);

export const CATEGORIES = Object.freeze({ read_only: '只读查询', write: '写入操作', recovery: '失败恢复' });

export function summarize(report) {
  const result = { planned: report.tasks.length, total: report.tasks.length, started: 0, passed: 0, failed: 0, error: 0, skipped: 0,
    model_judged_pass_rate: null, judge_control_pass_rate: null, noop_false_positive_rate: null, categories: {}, usage: null, usage_measured_tasks: 0 };
  for (const row of report.tasks) {
    if (['passed', 'failed', 'error', 'skipped'].includes(row.status)) result[row.status]++;
    if (row.status !== 'skipped') result.started++;
    if (row.usage) {
      result.usage ??= { input: 0, output: 0 }; result.usage_measured_tasks++;
      for (const field of ['input', 'output']) result.usage[field] += row.usage[field] ?? 0;
    }
  }
  if (result.planned) {
    if (report.mode === 'model') result.model_judged_pass_rate = result.passed / result.planned;
    else if (report.control === 'reference') result.judge_control_pass_rate = result.passed / result.planned;
    else if (report.control === 'noop') result.noop_false_positive_rate = result.passed / result.planned;
  }
  for (const category of Object.keys(CATEGORIES)) {
    const rows = report.tasks.filter(row => row.category === category), passed = rows.filter(row => row.status === 'passed').length;
    result.categories[category] = { planned: rows.length, passed, failed: rows.filter(row => row.status === 'failed').length,
      error: rows.filter(row => row.status === 'error').length, skipped: rows.filter(row => row.status === 'skipped').length,
      model_rate: report.mode === 'model' && rows.length ? passed / rows.length : null };
  }
  result.coverage = { sites: [...new Set(report.tasks.map(row => row.site).filter(Boolean))], seeds: [...new Set(report.tasks.map(row => row.seed))],
    layouts: [...new Set(report.tasks.map(row => row.layout_variant).filter(value => value !== undefined))] };
  return result;
}

const rate = value => value === null || value === undefined ? '不适用' : `${(100 * value).toFixed(1)}%`;

export function renderReport(report) {
  const summary = report.summary ?? summarize(report);
  const label = report.mode === 'model' ? `模型 · ${report.model}` : `判题器对照 · ${report.control}`;
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>通用 Web 任务评测</title><style>body{font:15px/1.65 system-ui,sans-serif;background:#f3f5f8;color:#1a2433;margin:0;padding:32px}main{max-width:1160px;margin:auto}h1{font-size:28px}.muted{color:#64748b}.notice{padding:16px;border-left:4px solid #3576a8;background:#eaf1f7}article,table{background:white;border:1px solid #dce3eb;border-radius:10px;margin:20px 0}article{padding:20px}h2{font-size:20px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f5f7fa;padding:16px;font-size:12px}.passed{color:#178260}.failed,.error{color:#b84337}summary{cursor:pointer}table{width:100%;border-collapse:collapse}td,th{text-align:left;padding:10px;border-bottom:1px solid #e2e8f0}</style></head><body><main><h1>通用 Web 任务评测</h1><p class="muted">${escape(label)} · ${escape(report.started_at)} · seed ${escape(report.seed)}</p><p class="notice">三个独立 origin 的受控合成站点。模型输入为 URL 与业务目标，自然语言答案由有限规则判定，并核对实际浏览器轨迹与服务端状态；规则仍会误判，需要独立复核，不能单独作为发布门禁。此结果不能证明真实互联网或未见网站的可靠性；与旧浏览器、认证套件和历史分数独立。${report.mode === 'validate' ? ' 当前运行仅验证参考脚本和判题器，不是模型成功率。' : ''}</p><table><thead><tr><th>范围</th><th>通过 / 计划任务</th><th>失败 / 错误 / 未运行</th><th>自动规则通过率</th></tr></thead><tbody>${[['全部', summary], ...Object.entries(CATEGORIES).map(([key, value]) => [value, summary.categories[key]])].map(([name, value]) => `<tr><td>${escape(name)}</td><td>${value.passed}/${value.planned}</td><td>${value.failed} / ${value.error} / ${value.skipped}</td><td>${rate(value.model_rate ?? (name === '全部' ? summary.model_judged_pass_rate : null))}</td></tr>`).join('')}</tbody></table><p class="muted">判题器正对照通过率 ${rate(summary.judge_control_pass_rate)} · 无动作对照误通过率 ${rate(summary.noop_false_positive_rate)}。未运行任务保留在分母。自然答案的排版和代码围栏不单独导致失败。</p>${report.infrastructure_error ? `<p class="error">基础设施错误：${escape(report.infrastructure_error)}</p>` : ''}${report.tasks.map(row => `<article><h2 class="${escape(row.status)}">${escape(row.title)} · 第 ${row.repeat + 1} 轮 · ${escape(row.status)}</h2><p>${escape(row.reason)}</p><p class="muted">${escape(CATEGORIES[row.category] ?? row.category)} · ${escape(row.site)} · seed ${escape(row.seed)} · layout ${escape(row.layout_variant)} · ${row.duration_ms ?? '—'} ms · 上下文清理 ${row.context_cleaned === true ? '已确认' : row.context_cleaned === false ? '失败' : '未启动'}</p><details><summary>业务目标与自然答案</summary><pre>${escape(row.task_prompt)}\n\n${escape(row.final_answer)}</pre></details><details><summary>判定与服务端证据</summary><pre>${escape(JSON.stringify({ judgement: row.judgement, before: row.server_state_before, after: row.server_state_after, blocked_requests: row.blocked_requests, profile: row.profile, profile_sha256: row.profile_sha256, skill_sha256: row.skill_sha256, system_prompt_sha256: row.system_prompt_sha256, tool_schema_sha256: row.tool_schema_sha256, usage: row.usage, usage_partial: row.usage_partial }, null, 2))}</pre></details><details><summary>实际工具轨迹</summary><pre>${escape(JSON.stringify(row.events, null, 2))}</pre></details></article>`).join('')}<details><summary>配置与源码指纹</summary><pre>${escape(JSON.stringify({ suite: report.suite, config: report.config, summary }, null, 2))}</pre></details></main></body></html>`;
}
