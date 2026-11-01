const escape = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);
const groups = { preauthorized_automatic: '预授权条件下自动完成', correct_handoff: '正确停止或交接', human_assisted: '模拟人工授权后完成' };
export function summarize(report) {
  const total = report.tasks.length, passed = report.tasks.filter(row => row.status === 'passed').length;
  const result = { total, passed, failed: report.tasks.filter(row => row.status === 'failed').length, errors: report.tasks.filter(row => row.status === 'error').length,
    skipped: report.tasks.filter(row => row.status === 'skipped').length, validation_pass_rate: report.mode === 'validate' ? passed / total : null,
    model_judged_pass_rate: report.mode === 'model' ? passed / total : null, groups: {}, simulated_human_interventions: report.tasks.reduce((n, row) => n + row.simulated_human_interventions, 0),
    usage: null, usage_measured_phases: 0 };
  for (const key of Object.keys(groups)) {
    const rows = report.tasks.filter(row => row.group === key), successes = rows.filter(row => row.status === 'passed').length;
    result.groups[key] = { total: rows.length, passed: successes, model_rate: report.mode === 'model' && rows.length ? successes / rows.length : null };
  }
  for (const row of report.tasks) for (const phase of row.phases) if (phase.usage) {
    result.usage_measured_phases++;
    result.usage ??= { input: 0, output: 0 };
    result.usage.input += phase.usage.input ?? 0; result.usage.output += phase.usage.output ?? 0;
  }
  return result;
}
export function renderReport(report) {
  const summary = summarize(report), percent = value => value === null ? '未测量' : `${(value * 100).toFixed(1)}%`;
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>浏览器认证独立评测</title><style>body{font:15px/1.65 system-ui,sans-serif;background:#f3f5f8;color:#1a2433;margin:0;padding:36px}main{max-width:1100px;margin:auto}h1{font-size:30px}.muted{color:#64748b}.notice{padding:16px;border-left:4px solid #3576a8;background:#eaf1f7}.cards{display:flex;gap:16px;flex-wrap:wrap}.card,article{background:white;border:1px solid #dce3eb;border-radius:12px;padding:20px;margin:16px 0}.card{flex:1;min-width:210px}.metric{font-size:28px;font-weight:650}h2{font-size:20px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f5f7fa;padding:16px;font-size:12px}.passed{color:#178260}.failed,.error{color:#b84337}a{color:#2468a0}summary{cursor:pointer}table{width:100%;border-collapse:collapse}td,th{text-align:left;padding:10px;border-bottom:1px solid #e2e8f0}</style></head><body><main><h1>浏览器认证独立评测</h1><p class="muted">${escape(report.mode === 'model' ? report.model : `确定性判分器验证 · ${report.control}`)} · ${escape(report.started_at)} · ${summary.passed}/${summary.total} 项任务判定通过</p><p class="notice">${report.mode === 'validate' ? '此报告只验证参考脚本与判分器，不是模型成功率。' : '这里分别报告预授权、正确停止和人工协助；混合通过率不等于完全自主完成率。'} 旧版20项浏览器评测保持独立。模拟验证码不代表真实 MFA 或验证码自动处理能力；人工授权由测试控制器明确模拟，不是模型自行授权。</p><div class="cards">${Object.entries(groups).map(([key, label]) => `<div class="card"><div>${label}</div><div class="metric">${percent(summary.groups[key].model_rate)}</div><span class="muted">${summary.groups[key].passed}/${summary.groups[key].total} 项通过</span></div>`).join('')}</div><h2>任务与实际证据</h2>${report.tasks.map(row => `<article><h2 class="${escape(row.status)}">${escape(row.title)} · 第 ${row.repeat + 1} 轮 · ${escape(row.status)}</h2><p>${escape(row.reason ?? '')}</p><p class="muted">${escape(groups[row.group])} · ${row.duration_ms ?? '—'} ms · 模拟人工介入 ${row.simulated_human_interventions} 次 · 上下文清理 ${row.context_cleaned === true ? '已确认' : row.context_cleaned === false ? '失败' : '未启动'}</p>${row.phases.map(phase => `<details><summary>阶段 ${phase.index + 1}：${escape(phase.role)} · ${phase.judgement?.success ? '判定通过' : '未通过'}</summary><pre>${escape(JSON.stringify(phase, null, 2))}</pre></details>`).join('')}<details><summary>完整失败与恢复轨迹</summary><pre>${escape(JSON.stringify(row.events, null, 2))}</pre></details></article>`).join('')}<details><summary>统计口径、源码指纹与运行配置</summary><pre>${escape(JSON.stringify({ config: report.config, summary }, null, 2))}</pre></details></main></body></html>`;
}
