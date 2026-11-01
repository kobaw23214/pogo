import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createEvidenceFixtures } from './fixtures.js';
import { actionsFrom, assessProtocol, quantities } from './contract.js';
import { parseOptions, runSuite, renderReport } from './run.js';
import { requestAllowed } from '../general-web/runner.js';

test('explicit options, safe HTML and acknowledged actions only', () => {
  assert.throws(() => parseOptions(['--mode', 'model']), /explicit/);
  assert.throws(() => parseOptions(['--tasks', 'document-duration,document-duration']), /duplicate/);
  assert.throws(() => parseOptions(['--repeat', '4']), /integer/);
  assert.throws(() => quantities({ evidence: { schema: 'dom-evidence-v1', status: 'unavailable', frames: [] } }), /interface_pending/);
  const boundary = { startURL: 'http://127.0.0.1:5000/cases/a/', allowedOrigins: ['http://127.0.0.1:5000'], allowedPathPrefixes: ['/cases/a/'] };
  assert(requestAllowed('http://127.0.0.1:5000/cases/a/details', boundary));
  for (const url of ['http://example.org/', 'http://127.0.0.1:5000/cases/b/', 'http://127.0.0.1:5000/cases/a/oracle', 'http://127.0.0.1:5000/cases/a/%5f%5fcontroller']) assert.equal(requestAllowed(url, boundary), false);
  const actions = actionsFrom([{ type: 'tool_start', args: { action: 'calculate' } }, { type: 'browser_start', args: { action: 'batch', actions: [{ action: 'type' }, { action: 'click' }] } }, { type: 'browser_end', ok: true, result: { steps: [{ action: 'type', ok: true }], evidence: { status: 'observed' } } }]);
  assert.equal(actions.length, 1); assert.equal(actions[0].action, 'type');
  const output = renderReport({ tasks: [{ id: '<img onerror=x>', seed: 1, status: 'error', final_answer: '<script>alert(1)</script>' }], config: {}, summary: {} });
  assert(!output.includes('<script>')); assert(!output.includes('<img')); assert(output.includes('&lt;script&gt;')); assert(output.includes("script-src 'none'"));
});

test('fixtures vary deterministically and expose no HTTP oracle/controller', async () => {
  const a = await createEvidenceFixtures({ seed: 811 }), b = await createEvidenceFixtures({ seed: 811 }), c = await createEvidenceFixtures({ seed: 812 });
  try {
    assert.deepEqual(a.referenceData('cross-detail-mass'), b.referenceData('cross-detail-mass'));
    assert.notDeepEqual(a.referenceData('cross-detail-mass'), c.referenceData('cross-detail-mass'));
    for (const suffix of ['/oracle', '/controller', '/cases/cross-detail-mass/inspect', '/cases/cross-detail-mass/expected']) assert.equal((await fetch(a.origin + suffix)).status, 404);
    assert(a.tasks.every(t => t.prompt.startsWith('起始网址：') && !/quantity_refs|calculate|selector|expected/.test(t.prompt)));
    assert.equal(a.assessReads('cross-detail-mass').verified, false); // Node fetches do not forge browser provenance.
  } finally { await Promise.all([a.close(), b.close(), c.close()]); }
});

test('correct text or declared calculations cannot replace actual source evidence', () => {
  const control = { operations: ['difference'], raws: ['CNY 20', 'CNY 10'], unit: 'CNY', absolute_difference: '10' };
  const fake = { action: 'calculate', ok: true, result: { evidence: { status: 'calculated', scope: 'as_observed', current_state_verified: false, verifies_entire_answer: false, operation: 'difference', sources: [{ quantity_ref: 'fabricated', raw: 'CNY 20' }], result: { value: '10', unit: 'CNY' } } } };
  assert.equal(assessProtocol([], control, { verified: true }).interface_verified, false);
  assert(assessProtocol([fake], control, { verified: true }).failures.includes('calculation_sources_not_observed'));
});

test('real Chromium reference/noop control and fresh-output refusal', { timeout: 180000 }, async () => {
  const scratch = await mkdtemp(path.join(os.tmpdir(), 'evidence-loop-'));
  try {
    const positive = await runSuite(parseOptions(['--seed', '811', '--repeat', '2', '--out', path.join(scratch, 'positive') ]));
    assert.equal(positive.report.infrastructure_error, undefined);
    assert.equal(positive.report.summary.planned, 8);
    assert.equal(positive.report.summary.interface_verified, 8, JSON.stringify(positive.report.tasks.map(t => ({ id: t.id, status: t.status, reason: t.reason, protocol: t.protocol?.failures }))));
    assert(positive.report.tasks.every(t => t.context_cleaned === true && t.answer_review === 'pending_review'));
    assert.equal(positive.report.summary.model_business_score, null);
    const docs = positive.report.tasks.find(t => t.id === 'document-duration');
    assert(docs.protocol.calculations.some(c => c.status === 'unsupported' && c.reason === 'incompatible_units'));
    const trace = JSON.parse(await readFile(path.join(positive.outputDir, docs.trace_path), 'utf8')); assert.equal(JSON.stringify(trace), JSON.stringify(docs.events));
    const negative = await runSuite(parseOptions(['--control', 'noop', '--seed', '811', '--out', path.join(scratch, 'negative') ]));
    assert.equal(negative.report.summary.interface_verified, 0); assert.equal(negative.report.summary.error, 0); assert(negative.report.tasks.every(t => t.context_cleaned === true && t.final_answer && !t.actions.length));
    await assert.rejects(runSuite(parseOptions(['--out', positive.outputDir])), { code: 'EEXIST' });
  } finally { await rm(scratch, { recursive: true, force: true }); }
});
