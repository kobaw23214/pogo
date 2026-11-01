import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { assertSourceSHA, renderHTML, writeNewArtifacts } from './overview.js';

function manifest() {
  const group = { label: '应用候选', seeds: [7], metadata: { revision: 'a17a9d5' }, total: 2,
    independent_whole_answers_verified: 1, whole_answers_needing_review: 1, requested_facts_verified: 2,
    native_finished_calls: 7, server_request_count: 4, other_tool_attempts: 0, other_tools_denied: 0,
    other_tools_successfully_executed: 0, original_acceptance_pass_count: null, contexts_cleanup_confirmed: 0,
    rows: [{ id: 'cross-detail-mass', whole_answer_review: 'needs_review', requested_facts_verified: true, context_cleanup_confirmed: false }] };
  return { generated_at: '2026-10-03', host_report_generator: { sha256: 'a'.repeat(64) },
    cli: { ...group, label: 'CLI', original_interface_verified: 3, total: 4, original_model_business_score: null,
      independent_whole_answers_verified: 4, contexts_cleanup_confirmed: 4, protocol_note: '文档缺 compare', attribution_note: '独立版本', rows: [] },
    pre_probe: structuredClone(group), final_probe: structuredClone(group), controls: [],
    runtime: { revision: 'a17a9d5', services_healthy: 3, services_verified: 3 },
    linux: { status: null, total: null, passed: null, failed: null, exit_code: null, scope: null }, source_artifacts: [] };
}
test('review source must match a valid exact SHA', () => {
  assertSourceSHA('a'.repeat(64), 'a'.repeat(64), 'review');
  assert.throws(() => assertSourceSHA('a'.repeat(64), 'b'.repeat(64), 'review'), /source SHA mismatch/);
  assert.throws(() => assertSourceSHA('short', 'short', 'review'), /source SHA mismatch/);
});
test('all input text is escaped and the page has no script execution', () => {
  const m = manifest(), attack = '<script>"&\'alert(1)</script>';
  m.pre_probe.label = attack; m.cli.protocol_note = attack; m.source_artifacts = [{ role: attack, sha256: attack }];
  m.pre_probe.raw_events = [{ account: 'private-account-sentinel' }];
  const html = renderHTML(m, '/tmp/report');
  assert.ok(html.includes('&lt;script&gt;')); assert.ok(!html.includes('<script>'));
  assert.ok(html.includes("script-src 'none'")); assert.ok(!html.includes('private-account-sentinel'));
  assert.ok(html.includes('null')); assert.ok(html.includes('未独立评定')); assert.ok(html.includes('a17a9d5'));
});
test('output refuses an existing directory and leaves its bytes unchanged', async () => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'evidence-overview-'));
  try {
    const out = path.join(temp, 'new'); await mkdir(out); await writeFile(path.join(out, 'manifest.json'), 'original bytes');
    await assert.rejects(writeNewArtifacts(out, manifest()), { code: 'EEXIST' });
    assert.equal(await readFile(path.join(out, 'manifest.json'), 'utf8'), 'original bytes');
  } finally { await rm(temp, { recursive: true, force: true }); }
});
