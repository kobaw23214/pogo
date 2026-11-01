import test from 'node:test';
import assert from 'node:assert/strict';
import { summarize, renderReport } from './report.js';

function report(overrides = {}) {
  return { mode: 'model', model: 'offline-test-adapter', seed: 1, config: {}, tasks: [
    { id: 'one', title: 'one', category: 'network', status: 'passed', ledger: [], usage: { input: 10, output: 4 } },
    { id: 'two', title: 'two', category: 'network', status: 'failed', ledger: [] },
    { id: 'three', title: 'three', category: 'target', status: 'error', ledger: [] },
    { id: 'four', title: 'four', category: 'target', status: 'skipped', ledger: [] }], ...overrides };
}

test('model denominator includes failures, errors and unstarted tasks; controls never publish model rates', () => {
  const result = summarize(report());
  assert.equal(result.planned, 4); assert.equal(result.model_judged_pass_rate, 0.25); assert.equal(result.run_valid, false);
  assert.deepEqual(result.usage, { input: 10, output: 4 }); assert.equal(result.usage_measured_tasks, 1);
  assert.equal(result.categories.target.planned, 2);
  for (const [control, rate] of [['reference', 'reference_control_pass_rate'], ['noop', 'noop_false_positive_rate'], ['fabricated', 'fabricated_false_positive_rate']]) {
    const summary = summarize(report({ mode: 'validate', control }));
    assert.equal(summary.model_judged_pass_rate, null); assert.equal(summary[rate], 0.25);
    for (const other of ['reference_control_pass_rate', 'noop_false_positive_rate', 'fabricated_false_positive_rate'].filter(key => key !== rate)) assert.equal(summary[other], null);
  }
});

test('infrastructure failure and cancellation invalidate otherwise passing runs', () => {
  const passing = { tasks: [{ id: 'one', category: 'network', status: 'passed' }] };
  assert.equal(summarize(report(passing)).run_valid, true);
  assert.equal(summarize(report({ ...passing, cancelled: true })).run_valid, false);
  assert.equal(summarize(report({ ...passing, infrastructure_error: 'cleanup failed' })).run_valid, false);
});

test('HTML escapes all source material and exposes evidence, provenance and bounded acceptance scope', () => {
  const injection = '<script>alert("x")</script>';
  const html = renderReport(report({ model: injection, config: { runtime_source_sha256: 'runtime-hash' }, tasks: [{
    id: 'task', title: injection, category: 'network', status: 'failed', reason: injection, final_answer: injection,
    judgement: { success: false, failures: [injection], evidence_ids: ['session:1'] },
    ledger: [{ call_id: 'session:1', result: { body: injection } }], adapter_events: [{ type: 'message', text: injection }],
    server_state_after: { read_count: 1 }, context_cleaned: true,
  }] }));
  assert.equal(html.includes('<script>'), false); assert.match(html, /&lt;script&gt;/);
  assert.match(html, /session:1/); assert.match(html, /runtime-hash/); assert.match(html, /read_count/);
  assert.match(html, /模型自报事件（不用于判分）/); assert.match(html, /控制器模拟 human lease/); assert.match(html, /不能证明真人登录/);
  assert.match(html, /Content-Security-Policy/);
});
