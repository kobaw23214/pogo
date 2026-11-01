import { TASKS } from './tasks.js';
const escape = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);
export const GROUPS = { existing_login: '已有登录态下自动查询', model_login: '模型登录后直接查询', correct_handoff: '正确停止或交接' };
export function summarize(report) {
  const result = { total: report.tasks.length, passed: 0, failed: 0, error: 0, skipped: 0, model_judged_pass_rate: null, groups: {}, simulated_human_interventions: 0, usage: null, usage_measured_phases: 0 };
  for (const row of report.tasks) {
    if (row.status in result) result[row.status]++;
    for (const phase of row.phases) if (phase.usage) {
      result.usage_measured_phases++; result.usage ??= { input: 0, output: 0 };
      result.usage.input += phase.usage.input ?? 0; result.usage.output += phase.usage.output ?? 0;
    }
  }
  if (report.mode === 'model' && result.total) result.model_judged_pass_rate = result.passed / result.total;
  for (const key of Object.keys(GROUPS)) {
    const rows = report.tasks.filter(row => row.group === key), passed = rows.filter(row => row.status === 'passed').length;
    result.groups[key] = { total: rows.length, passed, model_rate: report.mode === 'model' && rows.length ? passed / rows.length : null };
  }
  return result;
}
export function renderReport(report) {
  const summary = report.summary ?? summarize(report);
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>自动登录态复用评测 V2</title><style>body{font:15px/1.65 system-ui,sans-serif;background:#f3f5f8;color:#1a2433;margin:0;padding:32px}main{max-width:1100px;margin:auto}h1{font-size:28px}.muted{color:#64748b}.notice{padding:16px;border-left:4px solid #3576a8;background:#eaf1f7}article,table{background:white;border:1px solid #dce3eb;border-radius:10px;margin:20px 0}article{padding:20px}h2{font-size:20px}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f5f7fa;padding:16px;font-size:12px}.passed{color:#178260}.failed,.error{color:#b84337}summary{cursor:pointer}table{width:100%;border-collapse:collapse}td,th{text-align:left;padding:10px;border-bottom:1px solid #e2e8f0}</style></head><body><main><h1>自动登录态复用评测 V2</h1><p class="muted">${escape(report.mode === 'model' ? report.model : `确定性判分器验证 · ${report.control}`)} · ${escape(report.started_at)}</p><p class="notice">${report.mode === 'validate' ? '这是参考脚本与判分器验证，不是模型成功率。' : '已有登录态、模型真实登录、正确停止分组计分，混合通过率不等于自主任务完成率。'} 不存在 Pogo 手动接口授权阶段。MFA 只验证首因子后的人工交接，不代表自动处理真实验证码。新任务与历史手动授权 V1 及旧20项独立，不比较原分数。</p><table><thead><tr><th>范围</th><th>正式通过 / 全部</th><th>模型通过率</th></tr></thead><tbody>${[['all', '全部任务', summary], ...Object.entries(GROUPS).map(([key, label]) => [key, label, summary.groups[key]])].map(([key, label, value]) => `<tr><td>${label}</td><td>${value.passed}/${value.total}</td><td>${report.mode === 'model' && value.total ? `${(100 * value.passed / value.total).toFixed(1)}%` : '不适用'}</td></tr>`).join('')}</tbody></table><p class="muted">同一任务只有一个模型运行；模型登录组在同一轮中提交表单并读取 API。页面自动加载请求不能替代 api_read 证据。</p>${report.tasks.map(row => `<article><h2 class="${escape(row.status)}">${escape(TASKS.find(task => task.id === row.id)?.title ?? row.title)} · 第 ${row.repeat + 1} 轮 · ${escape(row.status)}</h2><p>${escape(row.reason)}</p><p class="muted">${escape(GROUPS[row.group])} · ${row.duration_ms ?? '—'} ms · 上下文清理 ${row.context_cleaned === true ? '已确认' : row.context_cleaned === false ? '失败' : '未启动'}</p>${row.phases.map(phase => `<details><summary>模型结果与服务端证据</summary><pre>${escape(JSON.stringify(phase, null, 2))}</pre></details>`).join('')}<details><summary>实际工具轨迹</summary><pre>${escape(JSON.stringify(row.events, null, 2))}</pre></details></article>`).join('')}<details><summary>统计与源码指纹</summary><pre>${escape(JSON.stringify({ suite: report.suite, config: report.config, summary }, null, 2))}</pre></details></main></body></html>`;
}
