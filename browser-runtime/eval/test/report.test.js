import test from 'node:test';
import assert from 'node:assert/strict';
import { renderReport, summarize, summarizeToolEvents } from '../report.js';

const task = (overrides = {}) => ({
  id: 'task-01', title: '筛选订单并核对结果', category: '表单', repeat: 0,
  status: 'passed', success: true, reason: '判题器确认状态正确', duration_ms: 1000,
  steps: 4, human_interventions: 0, duplicate_submissions: 0,
  usage: null, cost: null, cost_unit: null, ...overrides,
});

test('model tool failures are independent of final task failures and browser actions', () => {
  const events = [
    { type: 'tool_start', id: 'one' }, { type: 'tool_end', id: 'one', ok: false },
    { type: 'tool_start', id: 'two' }, { type: 'tool_end', id: 'two', ok: true },
    { type: 'browser_start' }, { type: 'browser_end', ok: false },
  ];
  const summary = summarize([task({ events, tool_calls: 999, steps: 200 }), task({ status: 'failed', events: [{ type: 'tool_end', ok: false }] })], 'model-evaluation');
  assert.equal(summary.tool_calls, 3);
  assert.equal(summary.tool_failures, 2);
  assert.equal(summary.affected_tasks, 2);
  assert.equal(summary.recovered_tasks, 1);
  assert.equal(summary.failed, 1);
  assert.equal(summary.errors, 0);
  assert.equal(summary.tool_failure_rate, 2 / 3);
  assert.equal(summary.tool_metrics_measured_tasks, 2);
  const html = renderReport({ mode: 'model-evaluation', tasks: [task({ events })] });
  assert.match(html, /最终任务异常/);
  assert.match(html, /中途工具失败/);
  assert.match(html, /失败后恢复/);
});

test('missing/empty/browser-only traces never imply zero model tool errors', () => {
  for (const events of [undefined, [], [{ type: 'browser_end', ok: true }]]) {
    const metrics = summarize([task({ events, tool_calls: 10 })], 'model-evaluation');
    for (const field of ['tool_calls', 'tool_failures', 'affected_tasks', 'recovered_tasks']) assert.equal(metrics[field], null);
    assert.equal(metrics.tool_metrics_measured_tasks, 0);
  }
  const zero = summarize([task({ events: [{ type: 'message' }, { type: 'end' }] })], 'model-evaluation');
  assert.equal(zero.tool_calls, 0);
  assert.equal(zero.tool_failures, 0);
});

test('pending and unclassified model tool outcomes are not silently counted as success', () => {
  assert.deepEqual(summarizeToolEvents(task({ events: [
    { type: 'tool_start', id: 'a' }, { type: 'tool_start', id: 'b' }, { type: 'tool_end', id: 'a' },
  ] })), { tool_calls: 2, tool_failures: 0, tool_pending_calls: 1, tool_unknown_outcomes: 2 });
  assert.deepEqual(summarizeToolEvents(task({ events: [{ type: 'tool_start', id: 'pending' }, { type: 'tool_end', id: 'other', ok: false }] })),
    { tool_calls: 2, tool_failures: 1, tool_pending_calls: 1, tool_unknown_outcomes: 1 });
});

test('baseline-shaped events yield 87 calls, 7 failures, 7 affected and recovered tasks', () => {
  const tasks = Array.from({ length: 20 }, (_, index) => task({
    events: Array.from({ length: index < 7 ? 5 : 4 }, (_, call) => [
      { type: 'tool_start', id: `${index}-${call}` },
      { type: 'tool_end', id: `${index}-${call}`, ok: !(index < 7 && call === 0) },
    ]).flat(),
  }));
  const summary = summarize(tasks, 'model-evaluation');
  assert.equal(summary.tool_calls, 87);
  assert.equal(summary.tool_failures, 7);
  assert.equal(summary.affected_tasks, 7);
  assert.equal(summary.recovered_tasks, 7);
  assert.equal(summary.tool_metrics_measured_tasks, 20);
});

test('primary model rate retains skipped, budget, error and timeout tasks in denominator', () => {
  const tasks = ['passed', 'failed', 'error', 'timeout', 'budget_exceeded', 'skipped'].map(status => task({ status }));
  const result = summarize(tasks, 'model-evaluation');
  assert.equal(result.total, 6);
  assert.equal(result.eligible, 5);
  assert.equal(result.completed, 2);
  assert.equal(result.success_rate, 1 / 6);
  assert.equal(result.pass_rate, 1 / 6);
  assert.equal(result.attempted_success_rate, 1 / 5);
  assert.equal(result.completion_rate, 2 / 6);
  assert.equal(result.errors, 1);
  assert.equal(result.timeouts, 1);
  assert.equal(result.budget_exceeded, 1);
  assert.equal(result.skipped, 1);
});

test('early stop cannot turn one success and three skipped tasks into 100%', () => {
  const tasks = [task(), ...Array.from({ length: 3 }, () => task({ status: 'skipped', success: null }))];
  assert.equal(summarize(tasks, 'model-evaluation').success_rate, 0.25);
  const html = renderReport({ mode: 'model-evaluation', tasks });
  assert.match(html, /1 \/ 4 次计划执行/);
  assert.match(html, /25%/);
});

test('fixture mode reports script pass rate while model success remains unmeasured', () => {
  const result = summarize([task(), task({ status: 'failed' })], 'fixture-validation');
  assert.equal(result.success_rate, null);
  assert.equal(result.pass_rate, 0.5);
  const html = renderReport({ mode: 'fixture-validation', tasks: [task()] });
  assert.match(html, /此报告不是模型成功率，也不代表 SOTA 能力/);
  assert.match(html, /参考脚本通过率/);
  assert.match(html, /未调用模型/);
});

test('empty or wholly skipped runs do not invent measurements', () => {
  const empty = summarize([], 'model-evaluation');
  assert.equal(empty.success_rate, null);
  assert.equal(empty.duration_ms.total, null);
  assert.equal(empty.usage, null);
  assert.equal(empty.cost, null);
  const skipped = summarize([task({ status: 'skipped' })], 'model-evaluation');
  assert.equal(skipped.success_rate, 0);
  assert.equal(skipped.attempted_success_rate, null);
  assert.equal(skipped.steps.total, null);
  assert.equal(skipped.human_interventions, null);
  assert.match(renderReport({ mode: 'model-evaluation', tasks: [] }), /没有选择任务/);
});

test('unknown usage and cost are not zero; explicitly recorded zeros remain zero', () => {
  const unknown = summarize([task()], 'model-evaluation');
  assert.equal(unknown.usage, null);
  assert.equal(unknown.cost, null);
  const measured = summarize([task({ usage: { input: 0, output: 0 }, cost: 0, cost_unit: 'USD' })], 'model-evaluation');
  assert.deepEqual(measured.usage, { input: 0, output: 0, cache_read: null, reasoning: null });
  assert.equal(measured.cost, 0);
  assert.equal(measured.cost_unit, 'USD');
  assert.equal(measured.usage_measured_tasks, 1);
  assert.equal(measured.cost_measured_tasks, 1);
});

test('partial telemetry retains field coverage and does not sum incompatible currencies', () => {
  const result = summarize([
    task({ usage: { input: 100, output: 15, cache_read: 40 }, cost: 1, cost_unit: 'USD' }),
    task({ usage: { input: 200, reasoning: 20 }, cost: 2, cost_unit: 'CNY' }),
    task(),
  ], 'model-evaluation');
  assert.deepEqual(result.usage, { input: 300, output: 15, cache_read: 40, reasoning: 20 });
  assert.deepEqual(result.usage_field_measured_tasks, { input: 2, output: 1, cache_read: 1, reasoning: 1 });
  assert.equal(result.usage_measured_tasks, 2);
  assert.equal(result.cost, null);
  assert.equal(result.cost_unit, null);
  assert.deepEqual(result.cost_units, ['USD', 'CNY']);
  assert.equal(result.cost_measured_tasks, 2);
});

test('small recorded costs are not rounded down to zero', () => {
  const html = renderReport({ mode: 'model-evaluation', tasks: [task({ cost: 0.00000123, cost_unit: 'USD' })] });
  assert.match(html, /0\.00000123 USD/);
});

test('partial usage remains visibly incomplete even when recorded values exist', () => {
  const tasks = [task({ status: 'timeout', usage: { input: 100, output: 20 }, usage_partial: true }), task()];
  assert.equal(summarize(tasks, 'model-evaluation').usage_partial_tasks, 1);
  const html = renderReport({ mode: 'model-evaluation', tasks });
  assert.match(html, /1 次任务的用量记录不完整/);
  assert.match(html, /不能当作最终账单/);
  assert.match(html, /用量记录不完整：/);
});

test('infrastructure and cleanup failures are prominent escaped text', () => {
  const html = renderReport({ mode: 'model-evaluation', tasks: [], infrastructure_error: '<img src=x onerror=alert(1)>', cleanup_error: 'context could not be closed' });
  assert.match(html, /运行遇到基础设施错误/);
  assert.match(html, /运行清理未正常完成/);
  assert.match(html, /context could not be closed/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<img src=x/);
});

test('intermediate reports are labelled and zero-based repeats display from one', () => {
  const report = { mode: 'model-evaluation', started_at: '2026-10-01T00:00:00Z', tasks: [task()] };
  const html = renderReport(report);
  assert.match(html, /运行中 · 当前为部分结果/);
  assert.match(html, /第 1 次/);
  assert.doesNotMatch(html, /第 0 次/);
  assert.doesNotMatch(renderReport({ ...report, finished_at: '2026-10-01T00:01:00Z' }), /运行中 · 当前为部分结果/);
});

test('distribution uses measured nonnegative finite numbers and correct median/p95', () => {
  const result = summarize([1000, 2000, 3000, 4000, null, -1, Infinity, '8000'].map(duration_ms => task({ duration_ms })), 'model-evaluation');
  assert.deepEqual(result.duration_ms, { total: 10000, median: 2500, p95: 4000, measured: 4 });
});

test('category metrics include repeated and skipped tasks without prototype collisions', () => {
  const result = summarize([
    task({ category: '__proto__', repeat: 1 }),
    task({ category: '__proto__', repeat: 2, status: 'skipped' }),
    task({ category: '导航', status: 'error' }),
  ], 'model-evaluation');
  assert.equal(result.categories.length, 2);
  assert.equal(result.categories[0].category, '__proto__');
  assert.equal(result.categories[0].total, 2);
  assert.equal(result.categories[0].success_rate, 0.5);
  assert.equal(result.categories[1].success_rate, 0);
});

test('unknown task status fails closed and invalid modes are rejected', () => {
  const result = summarize([task({ status: '<script>passed</script>' })], 'model-evaluation');
  assert.equal(result.passed, 0);
  assert.equal(result.errors, 1);
  assert.throws(() => summarize([], 'something-else'), /unsupported/);
  assert.throws(() => summarize({}, 'model-evaluation'), /array/);
  assert.throws(() => summarize([null], 'model-evaluation'), /object/);
});

test('report escapes all task, metadata, config and trace content', () => {
  const attack = '</pre><script>alert("x")</script><img src=x onerror=alert(1)>';
  const html = renderReport({
    run_id: attack, model: attack, started_at: attack, seed: attack,
    mode: 'model-evaluation', config: { input: attack },
    tasks: [task({ id: attack, title: attack, category: attack, reason: attack, final_answer: attack, events: [{ type: attack, args: { input: attack }, text: attack }] })],
  });
  assert.doesNotMatch(html, /<script/i);
  assert.doesNotMatch(html, /<img src=x/);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /&quot;x&quot;/);
  assert.match(html, /default-src 'none'/);
  assert.match(html, /connect-src 'none'/);
});

test('only encoded local artifact paths become links or image sources', () => {
  const frames = [
    { path: 'frames/筛选 结果.png', step: 1 },
    ...['https://example.com/a.png', '//example.com/a.png', 'data:image/png;base64,AAA', 'javascript:alert(1).png', '../private.png', '/private.png', 'frames\\bad.png', 'frames/a.svg'].map(path => ({ path, step: 2 })),
  ];
  const html = renderReport({ mode: 'model-evaluation', tasks: [task({ frames, trace_path: 'traces/task 1.json' })] });
  const sources = [...html.matchAll(/<img src="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(sources, ['frames/%E7%AD%9B%E9%80%89%20%E7%BB%93%E6%9E%9C.png']);
  assert.match(html, /href="traces\/task%201.json"/);
  assert.doesNotMatch(html, /(?:src|href)="(?:https?:|javascript:|data:|\/\/|\.\.\/|\/private)/);
});

test('unsafe trace paths remain inert text and double-encoded paths cannot traverse', () => {
  const html = renderReport({ mode: 'fixture-validation', tasks: [
    task({ trace_path: 'javascript:alert(1).json' }),
    task({ trace_path: '../private.json' }),
    task({ trace_path: 'traces/%2e%2e%2fprivate.json' }),
  ] });
  assert.match(html, /不可打开的轨迹路径/);
  assert.doesNotMatch(html, /href="javascript/);
  assert.doesNotMatch(html, /href="\.\.\//);
  assert.match(html, /href="traces\/%252e%252e%252fprivate.json"/);
});

test('step replay is static evidence and supplied summary cannot override actual denominator', () => {
  const html = renderReport({
    mode: 'model-evaluation', summary: { pass_rate: 1, total: 1 },
    tasks: [task({ events: [{ type: 'browser-action', seq: 7, at_ms: 400, action: 'click', ok: false, duration_ms: 20, error: '目标已失效' }] }), task({ status: 'skipped' })],
  });
  assert.match(html, /50%/);
  assert.match(html, /1 \/ 2 次计划执行/);
  assert.match(html, /目标已失效/);
  assert.match(html, /不向原网站发送请求/);
  assert.match(html, /<details class="event">/);
  assert.doesNotMatch(html, /<script|<iframe|<form|<object|fetch\(/i);
});
