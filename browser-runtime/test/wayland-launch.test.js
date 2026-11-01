import test from 'node:test';
import assert from 'node:assert/strict';
import { createWorker } from '../src/server.js';

// No native process is launched here. The real default process launch is
// exercised in native-launch and wayland-runtime inside the candidate image.
test('the default worker owns one runtime and carries deployment media settings into it', async t => {
  let connected = true, closes = 0; const launches = [];
  const runtime = { factory: async () => { assert.fail('health does not create a hidden browser session'); }, connected: () => connected, close: async () => { closes++; } };
  const mediaConfig = { advertisedIP: '192.0.2.40', udpPortRange: '21000-21015', iceServers: [{ urls: 'turn:relay.example:3478', username: 'synthetic', credential: 'synthetic-private' }], network: 'lan', playoutDelay: 'zero', deviceScaleFactor: 2 };
  const onFailure = () => {};
  const worker = await createWorker({ token: 'wayland-launch-fixture', mediaConfig, sandbox: false, onBrowserDisconnect: onFailure, launchRuntime: async options => { launches.push(options); return runtime; } });
  let closed = false; t.after(() => { if (!closed) return worker.close(); });
  assert.equal(launches.length, 1); assert.equal(worker.runtime, runtime);
  assert.deepEqual(launches[0].mediaConfig, mediaConfig);
  assert.equal(launches[0].sandbox, false); assert.equal(launches[0].onFailure, onFailure);
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
  const health = () => fetch(base + '/healthz', { headers: { Authorization: 'Bearer wayland-launch-fixture' } });
  let response = await health(); assert.equal(response.status, 200); assert.deepEqual(await response.json(), { ok: true, backend: 'native' });
  connected = false; response = await health(); assert.equal(response.status, 503); assert.deepEqual(await response.json(), { ok: false, backend: 'native' });
  await worker.close(); closed = true; assert.equal(closes, 1, 'worker cleanup owns the runtime lifecycle');
});

test('a failed native runtime is reported without launching an alternate media browser', async () => {
  const failure = new Error('native stream failed before session creation'); let calls = 0;
  await assert.rejects(createWorker({ token: 'wayland-no-fallback', launchRuntime: async () => { calls++; throw failure; } }), error => error === failure);
  assert.equal(calls, 1);
  await assert.rejects(createWorker({ token: 'wayland-no-fallback', launchRuntime: 'legacy launcher' }), /launchRuntime must be a function/);
});

test('a malformed allocated runtime is cleaned up before rejecting its launch contract', async () => {
  let closes = 0;
  await assert.rejects(createWorker({ token: 'wayland-invalid-runtime', launchRuntime: async () => ({ factory: 'invalid', connected: () => true, close: async () => { closes++; } }) }), /runtime must own session factory/);
  assert.equal(closes, 1);
});
