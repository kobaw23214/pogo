import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { compareReports, renderComparison } from '../compare.js';

const run = (overrides = {}) => ({
  schema_version: 1, run_id: 'run-a', mode: 'model-evaluation', seed: 42, model: 'test-model', finished_at: '2026-10-01T00:01:00Z',
  config: { repeat: 1, profile: 'browser-only', max_turns: 40, token_budget: 100000, time_budget_ms: 180000,
    concurrency: 2, human_assistance: 'disabled', capture_frames: true, external_web_access: 'blocked',
    files: { 'pogo/browser-runtime/eval/fixtures/index.js': 'fixture-v1' }, runtime: { platform: 'darwin', node: '24', browser: 'test' },
    source_sha256: 'source-old', adapter_sha256: 'adapter-old' },
  tasks: [{ id: 'catalog-a', repeat: 0, title: '筛选商品', category: 'filter', status: 'passed', duration_ms: 1000, steps: 4,
    max_steps: 35, time_budget_ms: 180000, model: 'test-model', profile: 'browser-only', system_prompt_sha256: 'prompt',
    sees_images: false, compatibility: 'openai', max_tokens: 4096, context_window: 32000,
    usage: { input: 1000, output: 100 }, events: [{ type: 'tool_start', id: 'a' }, { type: 'tool_end', id: 'a', ok: true }] }],
  ...overrides,
});

test('pure comparison recalculates old summaries without editing input objects', () => {
  const before = run({ summary: { tool_calls: 999, pass_rate: 0 } }), after = run({ run_id: 'run-b' });
  const original = JSON.stringify(before);
  const result = compareReports(before, after);
  assert.equal(result.before.summary.tool_calls, 1);
  assert.equal(result.before.summary.pass_rate, 1);
  assert.equal(result.protocol_status, 'same');
  assert.equal(result.causal_claim_supported, false);
  assert.equal(JSON.stringify(before), original);
});

test('source/binary changes are implementation changes, not task protocol mismatches', () => {
  const before = run(), after = run();
  after.config = { ...after.config, source_sha256: 'new-source', adapter_sha256: 'new-adapter' };
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'same');
  assert.equal(result.configuration_checks.find(c => c.field === 'source_sha256').status, 'changed');
  assert.equal(result.configuration_checks.find(c => c.field === 'source_sha256').group, 'implementation');
});

test('fixture implementation changes are protocol changes even with identical task IDs and seed', () => {
  const before = run(), after = run();
  after.config.files['pogo/browser-runtime/eval/fixtures/index.js'] = 'fixture-v2';
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'changed');
  const fixture = result.configuration_checks.find(c => c.field === 'fixture_manifest');
  assert.equal(fixture.group, 'protocol');
  assert.equal(fixture.status, 'changed');
  after.config.files['pogo/browser-runtime/eval/fixtures/data.json'] = 'new-data';
  assert.equal(compareReports(before, after).configuration_checks.find(c => c.field === 'fixture_manifest').status, 'changed');
});

test('missing fixture manifests are unknown; repository prefixes and path separators are irrelevant', () => {
  const before = run(), after = run();
  after.config.files = { 'relocated\\browser-runtime\\eval\\fixtures\\index.js': 'fixture-v1' };
  assert.equal(compareReports(before, after).protocol_status, 'same');
  before.config.files = {};
  after.config.files = {};
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'unknown');
  assert.equal(result.configuration_checks.find(c => c.field === 'fixture_manifest').status, 'unknown');
});

test('frame capture and external access changes affect protocol comparability', () => {
  const before = run(), after = run();
  after.config.capture_frames = false;
  after.config.external_web_access = 'allowed';
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'changed');
  for (const field of ['capture_frames', 'external_web_access']) assert.equal(result.configuration_checks.find(c => c.field === field).status, 'changed');
  delete after.config.capture_frames;
  assert.equal(compareReports(before, after).configuration_checks.find(c => c.field === 'capture_frames').status, 'unknown');
});

test('metadata object key order does not create a protocol difference', () => {
  const before = run(), after = run();
  after.config.runtime = { browser: 'test', node: '24', platform: 'darwin' };
  assert.equal(compareReports(before, after).protocol_status, 'same');
});

test('seed, prompt, effective limits, model protocol and runtime differences are explicit', () => {
  const before = run(), after = run({ seed: 43 });
  after.tasks[0].system_prompt_sha256 = 'changed-prompt';
  after.tasks[0].max_steps = 50;
  after.tasks[0].time_budget_ms = 60000;
  after.tasks[0].compatibility = 'different';
  after.tasks[0].max_tokens = 8192;
  after.config.runtime.node = '25';
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'changed');
  for (const field of ['seed', 'system_prompt_sha256', 'max_steps', 'time_budget_ms', 'compatibility', 'max_tokens', 'runtime'])
    assert.equal(result.configuration_checks.find(c => c.field === field).status, 'changed', field);
});

test('missing protocol metadata is unknown even when both sides lack it', () => {
  const before = run(), after = run();
  delete before.tasks[0].system_prompt_sha256;
  delete after.tasks[0].system_prompt_sha256;
  const result = compareReports(before, after);
  assert.equal(result.protocol_status, 'unknown');
  assert.equal(result.configuration_checks.find(c => c.field === 'system_prompt_sha256').status, 'unknown');
});

test('task IDs and repeat indexes drive pairing; unmatched tasks stay visible', () => {
  const before = run(), after = run();
  after.tasks.push({ ...after.tasks[0], repeat: 1 });
  const result = compareReports(before, after);
  assert.equal(result.paired_tasks.length, 2);
  assert.equal(result.paired_tasks[1].before, null);
  assert.equal(result.paired_tasks[1].after.repeat, 1);
  assert.equal(result.protocol_status, 'changed');
  assert.match(renderComparison(before, after), /缺少任务/);
  after.tasks.push({ ...after.tasks[0] });
  assert.throws(() => compareReports(before, after), /duplicate/);
});

test('unknown measurements are not compared as zero; failures and final outcomes stay separate', () => {
  const before = run(), after = run();
  delete before.tasks[0].events;
  after.tasks[0].events = [{ type: 'tool_end', ok: false }, { type: 'tool_end', ok: true }];
  const result = compareReports(before, after);
  assert.equal(result.before.summary.tool_failures, null);
  assert.equal(result.after.summary.tool_failures, 1);
  assert.equal(result.after.summary.recovered_tasks, 1);
  const html = renderComparison(before, after);
  assert.match(html, /中途工具失败/);
  assert.match(html, /未测量/);
  assert.match(html, /不能证明改造导致提速/);
});

test('comparison HTML is offline, escaped, and labels reference/incomplete reports', () => {
  const attack = '<img src="https://remote.test" onerror="alert(1)"><script>alert(1)</script>';
  const before = run({ run_id: attack, mode: 'fixture-validation', finished_at: null }), after = run();
  before.tasks[0].title = attack;
  after.tasks[0].system_prompt_sha256 = attack;
  const html = renderComparison(before, after);
  assert.doesNotMatch(html, /<script|<img|<iframe|<form|fetch\(/i);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /connect-src 'none'/);
  assert.match(html, /至少一侧是参考脚本验证/);
  assert.match(html, /至少一侧尚无完成标记/);
});

test('postprocessing provenance distinguishes report code from the evaluated snapshot', () => {
  const html = renderComparison(run(), run(), { postprocessing: { generated_at: '2026-10-01T03:00:00Z', compare_modified_at: '2026-10-01T02:30:00Z', after_finished_at: '2026-10-01T02:00:00Z', compare_differs_from_after_snapshot: true } });
  assert.match(html, /评测结束后的离线后处理/);
  assert.match(html, /不能称整个当前工作树仍等同于执行快照/);
});

test('CLI writes a new standalone report and refuses overwriting history', async () => {
  const directory = await mkdtemp(path.join(tmpdir(), 'pogo-compare-'));
  try {
    const before = path.join(directory, 'before.json'), after = path.join(directory, 'after.json'), output = path.join(directory, 'comparison.html');
    await writeFile(before, JSON.stringify(run()));
    await writeFile(after, JSON.stringify(run({ run_id: 'run-b' })));
    const cli = new URL('../compare.js', import.meta.url).pathname;
    await promisify(execFile)(process.execPath, [cli, before, after, '--out', output]);
    assert.match(await readFile(output, 'utf8'), /浏览器评测改造前后对比/);
    await assert.rejects(promisify(execFile)(process.execPath, [cli, before, after, '--out', before]), /EEXIST/);
    assert.equal(JSON.parse(await readFile(before, 'utf8')).run_id, 'run-a');
  } finally { await rm(directory, { recursive: true, force: true }); }
});
