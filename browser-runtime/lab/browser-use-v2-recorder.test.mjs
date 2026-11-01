import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm, readdir } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { chromium } from 'playwright';
import { createCandidateRecorder } from './browser-use-v2-recorder.mjs';
import { createWorker } from '../src/server.js';
import { workerRequest } from '../eval/transport.js';

const token = 'candidate-recorder-worker-token', adminToken = 'candidate-separate-controller-token';
async function harness(t, { real = false, reply, deleteHook } = {}) {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'pogo-v2-recorder-'));
  let browser, worker, workerURL; const requests = [];
  if (real) {
    browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
    worker = await createWorker({ token, browser, settleMs: 100 });
    workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
  } else {
    worker = http.createServer(async (request, response) => {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks)) : undefined;
      requests.push({ url: request.url, method: request.method, body });
      if (request.method === 'DELETE') await deleteHook?.();
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(await reply?.(request, body) ?? { ok: true, payload: { token_value: token, password: 'private-password' }, long_body: 'x'.repeat(70000) }));
    });
    await new Promise(resolve => worker.listen(0, '127.0.0.1', resolve));
    workerURL = `http://127.0.0.1:${worker.address().port}`;
  }
  const service = await createCandidateRecorder({ workerURL, token, adminToken, outputDir, environment: {} });
  t.after(async () => {
    await service.close();
    if (real) { await worker.close(); await browser.close(); }
    else { worker.closeAllConnections(); await new Promise(resolve => worker.close(resolve)); }
    await rm(outputDir, { recursive: true, force: true });
  });
  const admin = async (route, body, supplied = adminToken) => {
    const response = await fetch(service.adminURL + route, { method: body === undefined ? 'GET' : 'POST',
      headers: { Authorization: `Bearer ${supplied}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    return { status: response.status, data: await response.json() };
  };
  const app = (suffix, method = 'GET', body) => workerRequest(service.proxyURL, token, '/sessions/worker-random-id' + suffix, { method, body });
  const begin = async (task_id = 'network-diagnosis', options = {}) => {
    const response = await admin('/runs/begin', { task_id, seed: 20261012, studio_session_id: 'owned-studio-session', ...options });
    assert.equal(response.status, 200, JSON.stringify(response.data)); return response.data;
  };
  return { service, admin, app, begin, requests, outputDir };
}

test('controller, fixtures and worker proxy have separate authenticated route surfaces', async t => {
  const { service, admin, app, begin, requests } = await harness(t);
  assert.equal((await admin('/runs/state', undefined, token)).status, 401);
  assert.equal((await workerRequest(service.proxyURL, token, '/runs/begin', { method: 'POST', body: {} })).status, 404);
  assert.equal((await app('', 'PUT', {})).status, 404, 'unarmed session cannot reach worker');
  const started = await begin();
  const origin = new URL(started.task.start_url).origin;
  for (const route of ['/runs/state', '/runs/report', '/oracle', '/expectedAnswer']) assert.equal((await fetch(origin + route)).status, 404);
  assert.equal((await admin('/runs/begin', { task_id: 'iframe-data', seed: 1, studio_session_id: 'another' })).status, 409);
  assert.equal((await app('/execute', 'POST', { action: 'snapshot' })).status, 404, 'only actual PUT may claim armed session');
  assert.equal((await app('', 'PUT', {})).status, 200);
  assert.equal((await workerRequest(service.proxyURL, token, '/sessions/unexpected', { method: 'PUT', body: {} })).status, 404);
  assert.equal((await app('/execute', 'POST', { action: 'snapshot' })).status, 409);
  const state = await admin('/runs/state');
  assert.equal(state.data.worker_session_id, 'worker-random-id');
  assert.equal(state.data.studio_session_id, 'owned-studio-session');
  assert.equal(requests.length, 1);
  assert.doesNotMatch(JSON.stringify(state.data), /oracle|expected|password/);
});

test('preparation stays outside trusted ledger and full redacted ACKs retain checksum identity', async t => {
  const { admin, app, begin, outputDir } = await harness(t);
  await begin('network-diagnosis', { token_budget: 10 });
  await app('', 'PUT', {});
  assert.equal((await admin('/runs/prepare', {})).status, 200);
  assert.equal((await admin('/runs/execute', { action: 'snapshot' })).status, 200);
  assert.equal((await admin('/runs/state')).data.browser_requests, 0);
  await admin('/runs/start', {});
  assert.equal((await admin('/runs/execute', { action: 'snapshot' })).status, 409, 'controller execution cannot mint model evidence');
  await app('/execute', 'POST', { action: 'snapshot', op_id: 'real-browser-call' });
  const finish = await admin('/runs/finish', { answer: 'unsupported answer', usage: { input: 10, output: 1 }, usage_partial: false, end_reason: 'completed', run_id: 'studio-run-1' });
  assert.equal(finish.status, 200);
  assert.equal(finish.data.status, 'failed');
  assert.ok(finish.data.judgement.failures.includes('token_budget_exceeded'));
  const report = (await admin('/runs/report')).data;
  assert.equal(report.ledger.length, 1);
  assert.equal(report.ledger[0].args.op_id, 'real-browser-call');
  assert.equal(report.ledger[0].result.long_body.length, 70000, 'private evidence must not use public string truncation');
  assert.equal(report.ledger[0].result.payload.token_value, '[REDACTED]');
  assert.equal(report.ledger[0].result.payload.password, '[REDACTED]');
  assert.equal(report.ledger_sha256, createHash('sha256').update(JSON.stringify(report.ledger)).digest('hex'));
  assert.equal((await app('/execute', 'POST', { action: 'snapshot' })).status, 409);
  const persisted = JSON.parse(await readFile(path.join(outputDir, (await readdir(outputDir))[0]), 'utf8'));
  assert.deepEqual(persisted.ledger, report.ledger);
  assert.doesNotMatch(JSON.stringify(persisted), /candidate-recorder-worker-token|private-password|prepared_pause_anchor/);
  const clean = await admin('/runs/cleanup', {});
  assert.equal(clean.data.context_cleaned, true);
  assert.equal((await admin('/runs/report')).data.context_cleaned, true);
});

test('complete usage and Studio run provenance are mandatory and external input invalidates acceptance', async t => {
  const { admin, app, begin } = await harness(t);
  await begin(); await app('', 'PUT', {}); await admin('/runs/prepare', {}); await admin('/runs/start', {});
  await app('/control', 'POST', { mode: 'human' });
  const result = await admin('/runs/finish', { answer: '', usage: { input: 0, output: 0 } });
  for (const failure of ['missing_complete_measured_token_usage', 'missing_studio_run_id', 'studio_run_not_completed', 'external_browser_input_during_model_run', 'no_real_successful_browser_action']) assert.ok(result.data.judgement.failures.includes(failure));
});

test('real Chromium native iframe evidence passes through mapped app-facing session and teardown', async t => {
  const { admin, app, begin } = await harness(t, { real: true });
  await begin('iframe-data'); await app('', 'PUT', {});
  assert.equal((await admin('/runs/prepare', {})).status, 200);
  const targets = await admin('/runs/execute', { action: 'devtools', method: 'targets.list' });
  assert.ok(targets.data.data.items.some(item => item.type === 'iframe'));
  assert.equal((await admin('/runs/state')).data.browser_requests, 0);
  await admin('/runs/start', {});
  const observed = await app('/execute', 'POST', { action: 'snapshot', op_id: 'iframe-observation' });
  assert.equal(observed.data.ok, true);
  const finish = await admin('/runs/finish', { answer: JSON.stringify(observed.data), usage: { input: 20, output: 5 }, usage_partial: false, end_reason: 'completed', run_id: 'local-real-browser-control' });
  assert.equal(finish.data.status, 'passed', JSON.stringify(finish.data));
  await app('/cancel', 'POST', {}); await app('', 'DELETE');
  assert.equal((await admin('/runs/cleanup', {})).data.context_cleaned, true);
  const report = (await admin('/runs/report')).data;
  assert.equal(report.status, 'passed'); assert.equal(report.ledger.length, 1);
  assert.equal(report.cancel_acknowledged, true); assert.equal(report.delete_acknowledged, true);
  assert.equal(report.judgement_after_cleanup.success, true);
  const exchange = report.ledger[0];
  assert.ok(exchange.acknowledged_monotonic_ms >= exchange.started_monotonic_ms);
  assert.ok(report.server_state_before.requests.every(item => item.received_monotonic_ms < exchange.started_monotonic_ms));
  assert.equal((await admin('/runs/begin', { task_id: 'network-diagnosis', seed: 2, studio_session_id: 'next-studio-session' })).status, 200);
});

test('real paused-worker smoke cancellation cannot save and is excluded from scored exchanges', async t => {
  const { admin, app, begin } = await harness(t, { real: true });
  await begin('debugger-recovery'); await app('', 'PUT', {});
  const prepared = await admin('/runs/prepare', {});
  assert.equal(prepared.status, 200, JSON.stringify(prepared.data));
  assert.equal(prepared.data.preparation.paused, true);
  const targets = await admin('/runs/execute', { action: 'devtools', method: 'targets.list' });
  assert.ok(targets.data.data.items.some(item => item.type === 'worker' && item.paused));
  await app('/cancel', 'POST', {}); await app('', 'DELETE');
  assert.equal((await admin('/runs/cleanup', {})).data.context_cleaned, true);
  const report = (await admin('/runs/report')).data;
  assert.equal(report.ledger.length, 0);
  assert.equal(report.cancel_acknowledged, true);
  assert.equal(report.server_state_after_cleanup.submission_attempts, 0);
  assert.deepEqual(report.server_state_after_cleanup.submissions, []);
  assert.doesNotMatch(JSON.stringify(report), /prepared_pause_anchor|local_scope_object_ids/);
});

test('cleanup rechecks final service counters and revokes a pass after an extra business request', async t => {
  const { admin, app, begin } = await harness(t, { real: true });
  await begin('iframe-data'); await app('', 'PUT', {}); await admin('/runs/prepare', {}); await admin('/runs/start', {});
  const observed = await app('/execute', 'POST', { action: 'snapshot', op_id: 'iframe-before-late-read' });
  const finished = await admin('/runs/finish', { answer: JSON.stringify(observed.data), usage: { input: 5, output: 5 }, usage_partial: false, end_reason: 'completed', run_id: 'late-request-control' });
  assert.equal(finished.data.status, 'passed');
  const reportBefore = (await admin('/runs/report')).data;
  const actualDataURL = reportBefore.server_state_after.requests.find(item => item.kind === 'data').url;
  assert.equal((await fetch(actualDataURL)).status, 200, 'negative control adds a business request after provisional finish');
  await admin('/runs/cleanup', {});
  const report = (await admin('/runs/report')).data;
  assert.equal(report.context_cleaned, true);
  assert.equal(report.status, 'failed');
  assert.ok(report.judgement.failures.includes('NETWORK_REQUEST_COUNT_MISMATCH:0'));
  assert.equal(report.judgement_after_cleanup.success, false);
});
