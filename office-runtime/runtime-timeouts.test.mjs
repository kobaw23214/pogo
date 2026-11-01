// Synthetic runtime contracts. No Docker engine, editor, or user file is used.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { officeEngineHealth, officeLivenessTimeoutMs, officeStartupTimeoutMs } from './engine-proxy.mjs';

test('document startup gets at least five minutes while a liveness sample stays cheap', () => {
  assert.ok(officeStartupTimeoutMs >= 300000);
  assert.ok(officeLivenessTimeoutMs > 0 && officeLivenessTimeoutMs <= 3000);
});

test('healthy and warming engine samples have separate outcomes without hidden retry', async () => {
  for (const [status, text, expected] of [
    [200, 'true\n', { ready: true, status: 200 }],
    [200, 'false', { ready: false, status: 503 }],
    [503, 'warming', { ready: false, status: 503 }],
    [401, 'bad configuration', { ready: false, status: 401 }],
  ]) {
    let calls = 0;
    const health = await officeEngineHealth(new URL('http://127.0.0.1:80'), {
      fetchImpl: async (url, options) => {
        calls++;
        assert.equal(url.href, 'http://127.0.0.1/healthcheck');
        assert.equal(options.redirect, 'error');
        assert.equal(options.signal.aborted, false);
        return new Response(text, { status });
      },
    });
    assert.equal(calls, 1);
    assert.deepEqual(health, expected);
  }
});

test('an abandoned readiness request cancels only its own fetch and body', async () => {
  const caller = new AbortController();
  let signal;
  const pending = officeEngineHealth('http://127.0.0.1:80', {
    signal: caller.signal,
    fetchImpl: async (_url, options) => {
      signal = options.signal;
      return { ok: true, status: 200, text: () => new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason), { once: true });
      }) };
    },
  });
  await Promise.resolve();
  const reason = new Error('The open request was canceled.');
  caller.abort(reason);
  await assert.rejects(pending, (error) => error === reason);
  assert.equal(signal.aborted, true);
  const next = await officeEngineHealth('http://127.0.0.1:80', { fetchImpl: async () => new Response('true') });
  assert.deepEqual(next, { ready: true, status: 200 });
});

test('engine errors return immediately instead of spending the open timeout', async () => {
  const error = new Error('Connection refused.');
  let calls = 0;
  await assert.rejects(officeEngineHealth('http://127.0.0.1:80', { fetchImpl: async () => { calls++; throw error; } }),
    (received) => received === error);
  assert.equal(calls, 1);
});

test('a health sample deadline covers the response body, not just headers', async () => {
  const pending = officeEngineHealth('http://127.0.0.1:80', {
    timeoutMs: 10,
    fetchImpl: async (_url, { signal }) => ({ ok: true, status: 200,
      text: () => new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason), { once: true });
      }) }),
  });
  await Promise.all([
    assert.rejects(pending, /Office health check timed out/),
    // Production timers are unref'ed; this one only keeps the synthetic test
    // event loop alive while its short sample deadline fires.
    new Promise((resolve) => setTimeout(resolve, 20)),
  ]);
});

test('a completed sample detaches caller cancellation', async () => {
  const caller = new AbortController();
  let signal;
  const result = await officeEngineHealth('http://127.0.0.1:80', {
    signal: caller.signal,
    fetchImpl: async (_url, options) => { signal = options.signal; return new Response('true'); },
  });
  caller.abort();
  assert.deepEqual(result, { ready: true, status: 200 });
  assert.equal(signal.aborted, false);
});

test('Docker startup uses the shared open budget with a fast, independent health probe', async () => {
  const startup = await fs.readFile(new URL('./docker-proxy.mjs', import.meta.url), 'utf8');
  const dockerfile = await fs.readFile(new URL('./Dockerfile', import.meta.url), 'utf8');
  assert.match(startup, /const deadline = Date\.now\(\) \+ officeStartupTimeoutMs/);
  assert.match(startup, /await officeEngineHealth\(upstream\)/);
  assert.doesNotMatch(startup, /180000/);
  assert.match(dockerfile, /HEALTHCHECK --interval=15s --timeout=5s --start-period=(\d+)s --retries=3/);
  const seconds = Number(dockerfile.match(/--start-period=(\d+)s/)[1]);
  assert.ok(seconds * 1000 >= officeStartupTimeoutMs);
});
