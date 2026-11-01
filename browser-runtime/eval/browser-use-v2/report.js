const escape = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);
const rate = value => value === null || value === undefined ? '不适用' : `${(value * 100).toFixed(1)}%`;
const detail = (label, value) => `<details><summary>${escape(label)}</summary><pre>${escape(JSON.stringify(value, null, 2))}</pre></details>`;

export function summarize(report) {
  const summary = { planned: report.tasks.length, started: 0, passed: 0, failed: 0, error: 0, skipped: 0,
    model_judged_pass_rate: null, reference_control_pass_rate: null, noop_false_positive_rate: null, fabricated_false_positive_rate: null,
    run_valid: !report.cancelled && !report.infrastructure_error, usage: null, usage_measured_tasks: 0, categories: {} };
  for (const row of report.tasks) {
    if (['passed', 'failed', 'error', 'skipped'].includes(row.status)) summary[row.status]++;
    if (row.status !== 'skipped') summary.started++;
    if (row.usage && Number.isFinite(row.usage.input) && Number.isFinite(row.usage.output)) {
      summary.usage ??= { input: 0, output: 0 };
      summary.usage.input += row.usage.input; summary.usage.output += row.usage.output; summary.usage_measured_tasks++;
    }
    const category = summary.categories[row.category] ??= { planned: 0, passed: 0, failed: 0, error: 0, skipped: 0 };
    category.planned++; if (Object.hasOwn(category, row.status)) category[row.status]++;
  }
  if (summary.error || summary.skipped) summary.run_valid = false;
  if (summary.planned) {
    const key = report.mode === 'model' ? 'model_judged_pass_rate' : ({ reference: 'reference_control_pass_rate', noop: 'noop_false_positive_rate', fabricated: 'fabricated_false_positive_rate' })[report.control];
    if (key) summary[key] = summary.passed / summary.planned;
  }
  summary.coverage = { task_ids: [...new Set(report.tasks.map(row => row.id))], seeds: [...new Set(report.tasks.map(row => row.seed))] };
  return summary;
}

export function renderReport(report) {
  const summary = report.summary ?? summarize(report);
  const label = report.mode === 'model' ? `模型运行 · ${report.model}` : `判题器对照 · ${report.control}`;
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>Browser Use v2 验收</title><style>body{font:15px/1.65 system-ui,sans-serif;margin:0;padding:30px;color:#203047;background:#f4f6f8}main{max-width:1120px;margin:auto}h1{font-size:28px}h2{font-size:20px}article,table{background:white;border:1px solid #d9e0e7;border-radius:10px;margin:20px 0}article{padding:20px}table{width:100%;border-collapse:collapse}td,th{padding:10px;text-align:left;border-bottom:1px solid #d9e0e7}.notice{background:#e7eff8;border-left:4px solid #3872aa;padding:16px}.muted{color:#5b6c80}.passed{color:#14764d}.failed,.error{color:#a9342a}summary{cursor:pointer}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f4f6f8;padding:15px;font-size:12px}</style></head><body><main><h1>Browser Use v2 验收</h1><p class="muted">${escape(label)} · ${escape(report.started_at)} · ${escape(report.run_id)}</p><p class="notice">独立受控合成站点验收，包含真实 Chromium 的跨进程 iframe、dedicated worker 和 DevTools 操作。登录准备由控制器模拟 human lease 完成，不能证明真人登录、真实账号或互联网网站验收。模型仅收到 URL 与业务目标；判题只信任代理记录的 worker 确认结果和服务端状态。模型自报事件不构成证据。${report.mode === 'validate' ? ' 当前结果是参考脚本/判题器对照，不是模型成功率。' : ' 自动规则判断需结合逐任务证据复核，不能单独作为发布门禁。'} 旧套件和历史分数保持独立。</p><table><thead><tr><th>计划任务</th><th>通过</th><th>失败</th><th>错误</th><th>未运行</th><th>运行完整性</th></tr></thead><tbody><tr><td>${summary.planned}</td><td>${summary.passed}</td><td>${summary.failed}</td><td>${summary.error}</td><td>${summary.skipped}</td><td>${summary.run_valid ? '完整' : '未完成或无效'}</td></tr></tbody></table><p>模型自动规则通过率 ${rate(summary.model_judged_pass_rate)} · 参考对照 ${rate(summary.reference_control_pass_rate)} · 无动作误通过率 ${rate(summary.noop_false_positive_rate)} · 伪造事件误通过率 ${rate(summary.fabricated_false_positive_rate)}。全部计划任务保留在分母。</p>${report.infrastructure_error ? `<p class="error">基础设施错误：${escape(report.infrastructure_error)}</p>` : ''}${report.tasks.map(row => `<article><h2 class="${escape(row.status)}">${escape(row.title)} · ${escape(row.status)}</h2><p>${escape(row.reason)}</p><p class="muted">${escape(row.id)} · seed ${escape(row.seed)} · 第 ${(row.repeat ?? 0) + 1} 轮 · ${escape(row.duration_ms ?? '—')} ms · 已确认调用 ${escape(row.ledger?.length ?? 0)} · 上下文清理 ${row.context_cleaned === true ? '已确认' : row.context_cleaned === false ? '失败' : '未启动'}</p>${detail('业务目标与最终答案', { prompt: row.task_prompt, final_answer: row.final_answer })}${detail('判分与证据 ID', row.judgement)}${detail('准备方式与服务端状态', { preparation: row.preparation, before: row.server_state_before, after: row.server_state_after, blocked_requests: row.blocked_requests })}${detail('可信 worker 调用与返回', { ledger_sha256: row.ledger_sha256, ledger: row.ledger })}${detail('模型用量与指纹', { usage: row.usage, usage_partial: row.usage_partial, duration_ms: row.duration_ms, profile: row.profile, profile_sha256: row.profile_sha256, system_prompt_sha256: row.system_prompt_sha256, tool_schema_sha256: row.tool_schema_sha256, shared_skill_body_sha256: row.shared_skill_body_sha256 })}${detail('模型自报事件（不用于判分）', row.adapter_events ?? [])}</article>`).join('')}${detail('配置、源码指纹与汇总', { config: report.config, summary })}</main></body></html>`;
}
