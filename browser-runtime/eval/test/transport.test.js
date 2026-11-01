import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createRecordingProxy, makeRedactor, safeValue, workerRequest } from '../transport.js';

test('recording proxy budgets requested batch steps but counts only returned attempts', async () => {
  const token = 'local-fixture-token';
  let forwarded = 0;
  const worker = http.createServer(async (request, response) => {
    for await (const _ of request) { /* drain */ }
    forwarded++;
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify(request.url.endsWith('/execute') ? {
      ok: false, error: 'navigation interrupted batch', steps: [{ ok: true }, { ok: false }],
    } : { ok: true }));
  });
  await new Promise(resolve => worker.listen(0, '127.0.0.1', resolve));
  const proxy = await createRecordingProxy({ workerURL: `http://127.0.0.1:${worker.address().port}`, token,
    outputDir: '/not-used', redact: makeRedactor({}, [token]), captureFrames: false });
  try {
    const record = proxy.register('eval-test', 'test-0', 4);
    await proxy.execute('eval-test', { action: 'batch', actions: [{ action: 'snapshot' }, { action: 'navigate' }, { action: 'click' }, { action: 'snapshot' }] });
    assert.equal(record.requestedSteps, 4);
    assert.equal(record.steps, 2);
    assert.equal(record.calls, 1);
    assert.equal(record.failures.length, 1);
    await assert.rejects(proxy.execute('eval-test', { action: 'snapshot' }), /step budget/);
    assert.equal(record.stepLimit, true);
    assert.equal(forwarded, 1, 'over-budget operation must never reach worker');
    const cancel = await workerRequest(proxy.url, token, '/sessions/eval-test/cancel', { method: 'POST', body: {} });
    assert.equal(cancel.ok, true, 'cancellation remains available after exhausting steps');
    assert.equal(forwarded, 2);
    const other = await workerRequest(proxy.url, token, '/sessions/not-registered/state');
    assert.equal(other.status, 404);
    const unauthenticated = await fetch(`${proxy.url}/sessions/eval-test/state`);
    assert.equal(unauthenticated.status, 401);
    assert.equal(forwarded, 2);
  } finally {
    await proxy.close();
    worker.closeAllConnections();
    await new Promise(resolve => worker.close(resolve));
  }
});

test('trace serialization strips credentials and screenshot bytes while retaining useful metadata', () => {
  const value = safeValue({ authorization: 'private', headers: { private: 'header' }, lease_token: 'private',
    screenshot: { data: 'private-base64', mime_type: 'image/jpeg', version: 'v1' },
    url: 'http://user:pass@localhost/path?token=private-token', text: 'dummy-private-api-key' },
  makeRedactor({ TEST_API_KEY: 'dummy-private-api-key' }));
  assert.deepEqual(value.screenshot, { mime_type: 'image/jpeg', version: 'v1' });
  assert.doesNotMatch(JSON.stringify(value), /private|user:pass/);
  assert.match(value.url, /\[REDACTED\]/);
});

test('private exchange observer binds acknowledgements to calls and cannot alter the response', async () => {
  const token = 'trusted-exchange-fixture';
  const worker = http.createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const args = JSON.parse(Buffer.concat(chunks));
    if (args.params?.index === 1) await new Promise(resolve => setTimeout(resolve, 40));
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({ ok: true, data: { index: args.params?.index, origin: 'actual-worker' } }));
  });
  await new Promise(resolve => worker.listen(0, '127.0.0.1', resolve));
  const ledger = [];
  const proxy = await createRecordingProxy({ workerURL: `http://127.0.0.1:${worker.address().port}`, token,
    outputDir: '/not-used', redact: makeRedactor({}), captureFrames: false,
    onExchange(exchange) { ledger.push(structuredClone(exchange)); exchange.result.data.origin = 'observer-mutated'; },
  });
  try {
    const record = proxy.register('trusted', 'case-0', 3);
    record.events.push({ type: 'browser_end', ok: true, result: { data: { origin: 'model-forged' } } });
    assert.equal(ledger.length, 0, 'public trace text cannot mint trusted exchanges');
    const replies = await Promise.all([1, 2].map(index => proxy.execute('trusted', { action: 'devtools', method: 'Runtime.evaluate', params: { index } })));
    assert.equal(ledger.length, 2);
    assert.equal(new Set(ledger.map(item => item.call_id)).size, 2);
    for (const exchange of ledger) {
      assert.equal(exchange.args.params.index, exchange.result.data.index);
      assert.equal(exchange.result.data.origin, 'actual-worker');
      assert.equal(exchange.sessionId, 'trusted');
      assert.equal(exchange.artifactId, 'case-0');
      assert.equal(exchange.http_status, 200);
      assert.equal(exchange.ok, true);
      assert.ok(Number.isFinite(exchange.started_monotonic_ms));
      assert.ok(exchange.acknowledged_monotonic_ms >= exchange.started_monotonic_ms);
    }
    assert.ok(replies.every(reply => reply.data.origin === 'actual-worker'));
  } finally {
    await proxy.close(); worker.closeAllConnections(); await new Promise(resolve => worker.close(resolve));
  }
});

test('exchange recording failure refuses further actions while permitting cancellation', async () => {
  const token = 'exchange-failure-fixture'; let forwarded = 0;
  const worker = http.createServer(async (request, response) => {
    for await (const _ of request) { /* drain */ }
    forwarded++;
    response.writeHead(200, { 'Content-Type': 'application/json' }); response.end('{"ok":true}');
  });
  await new Promise(resolve => worker.listen(0, '127.0.0.1', resolve));
  const proxy = await createRecordingProxy({ workerURL: `http://127.0.0.1:${worker.address().port}`, token,
    outputDir: '/not-used', redact: makeRedactor({}), captureFrames: false,
    onExchange() { throw new Error('private collector detail'); },
  });
  try {
    const record = proxy.register('failed-ledger', 'case-0', 5);
    await assert.rejects(proxy.execute('failed-ledger', { action: 'snapshot' }), /could not be recorded/);
    assert.equal(forwarded, 1);
    assert.ok(record.evidenceError);
    await assert.rejects(proxy.execute('failed-ledger', { action: 'click', target: '#submit' }), /recording failed/);
    assert.equal(forwarded, 1, 'failed evidence collection must not permit unrecorded writes');
    assert.equal((await workerRequest(proxy.url, token, '/sessions/failed-ledger/cancel', { method: 'POST', body: {} })).ok, true);
    assert.equal(forwarded, 2);
    assert.equal(JSON.stringify(record).includes('private collector detail'), false);
  } finally {
    await proxy.close(); worker.closeAllConnections(); await new Promise(resolve => worker.close(resolve));
  }
});

test('candidate admission and extra routes are opt-in and execution requires an explicit recording start', async () => {
  const token = 'candidate-transport-token', forwarded = [], lifecycle = [];
  const worker = http.createServer(async (request, response) => {
    for await (const _ of request) { /* drain */ }
    forwarded.push(`${request.method} ${request.url}`);
    response.writeHead(200, { 'Content-Type': 'application/json' }); response.end('{"ok":true}');
  });
  await new Promise(resolve => worker.listen(0, '127.0.0.1', resolve));
  const workerURL = `http://127.0.0.1:${worker.address().port}`;
  const defaults = await createRecordingProxy({ workerURL, token, outputDir: '/not-used', redact: makeRedactor({}), captureFrames: false });
  let admitted;
  const proxy = await createRecordingProxy({ workerURL, token, outputDir: '/not-used', redact: makeRedactor({}), captureFrames: false,
    allowSessionControls: true, admitSession(id) {
      if (admitted) return null;
      admitted = id; return { artifactId: 'owned-studio-task', maxSteps: 2, executionEnabled: false };
    }, onLifecycle(event) { lifecycle.push(event); } });
  try {
    defaults.register('default', 'default', 2);
    for (const [suffix, method] of [['/control', 'POST'], ['/input', 'POST'], ['/pick', 'POST'], ['/auth', 'POST'], ['', 'DELETE']]) {
      assert.equal((await workerRequest(defaults.url, token, `/sessions/default${suffix}`, { method, body: method === 'POST' ? {} : undefined })).status, 405);
    }
    assert.equal((await workerRequest(proxy.url, token, '/sessions/not-armed/execute', { method: 'POST', body: { action: 'snapshot' } })).status, 404);
    assert.equal(admitted, undefined);
    assert.equal((await workerRequest(proxy.url, token, '/sessions/owned', { method: 'PUT', body: {} })).status, 200);
    assert.equal((await workerRequest(proxy.url, token, '/sessions/other', { method: 'PUT', body: {} })).status, 404);
    assert.equal((await workerRequest(proxy.url, token, '/sessions/owned/execute', { method: 'POST', body: { action: 'snapshot' } })).status, 409);
    assert.equal(forwarded.length, 1);
    proxy.getRecord('owned').executionEnabled = true;
    await proxy.execute('owned', { action: 'snapshot' });
    assert.equal(proxy.getRecord('owned').calls, 1);
    assert.equal(proxy.getRecord('owned').inFlight, 0);
    for (const endpoint of ['control', 'input', 'pick', 'auth']) assert.equal((await workerRequest(proxy.url, token, `/sessions/owned/${endpoint}`, { method: 'POST', body: {} })).status, 200);
    assert.equal((await workerRequest(proxy.url, token, '/sessions/owned', { method: 'DELETE' })).status, 200);
    assert.deepEqual(lifecycle.map(item => item.endpoint), ['', 'control', 'input', 'pick', 'auth', '']);
    assert.equal(lifecycle.at(-1).method, 'DELETE');
    assert.equal(lifecycle.at(-1).ok, true);
    assert.equal((await workerRequest(proxy.url, token, '/sessions/owned', { method: 'PUT', body: {} })).status, 409, 'deleted session must not be recreated by app polling');
    assert.equal((await workerRequest(proxy.url, token, '/sessions/owned', { method: 'DELETE' })).status, 200, 'cleanup retry remains idempotent');
  } finally {
    await Promise.all([proxy.close(), defaults.close()]); worker.closeAllConnections(); await new Promise(resolve => worker.close(resolve));
  }
});
