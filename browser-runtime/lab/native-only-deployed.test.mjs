import test from 'node:test';
import assert from 'node:assert/strict';
import { createWorker } from '../src/server.js';
import { createFixture, dockerWorkerTransport, httpTransport, loopbackOrigin, migrationChecks, verifyImagePair, workerChecks } from './native-only-deployed.mjs';

test('acceptance requires a loopback origin without credential-bearing URL', () => {
  assert.equal(loopbackOrigin('http://127.0.0.1:19777'), 'http://127.0.0.1:19777');
  for (const value of ['https://outside.invalid/', 'http://user:secret@localhost/', 'http://localhost/?token=secret', 'http://localhost/path']) assert.throws(() => loopbackOrigin(value));
});

test('image attestation checks both actual image IDs using only safe Docker projections', async () => {
  const image = `sha256:${'a'.repeat(64)}`, other = `sha256:${'b'.repeat(64)}`, calls = [];
  const config = { appContainer: 'pogo-app', workerContainer: 'pogo-browser', appImage: image, workerImage: other };
  const invoke = async (program, args) => {
    calls.push([program, args]);
    assert.equal(program, 'docker'); assert.equal(args[0], 'inspect');
    assert.ok(!args.join(' ').includes('Config'));
    return JSON.stringify({ image: args.at(-1) === 'pogo-app' ? image : other, running: true, health: 'healthy' });
  };
  const result = await verifyImagePair(config, invoke);
  assert.equal(calls.length, 2); assert.equal(result.worker.image, other);
  await assert.rejects(verifyImagePair({ ...config, workerImage: image }, invoke), /worker image/);
  await assert.rejects(verifyImagePair(config, async () => JSON.stringify({ image, running: true, health: 'unhealthy' })), /healthy/);
});

test('Docker worker transport refuses real session IDs and keeps credentials out of payload', async () => {
  const calls = [];
  const worker = dockerWorkerTransport('pogo-browser', async (program, args, input) => { calls.push({ program, args, input }); return '{"status":200,"body":{"ok":true}}'; });
  await worker('/sessions/native-only-accept-unique', 'PUT');
  assert.equal(calls[0].program, 'docker');
  assert.deepEqual(JSON.parse(calls[0].input), { path: '/sessions/native-only-accept-unique', method: 'PUT' });
  await assert.rejects(worker('/sessions/existing-user-session', 'DELETE'), /non-test/);
  assert.equal(calls.length, 1);
});

test('app without Docker healthcheck uses later API readiness, while worker health remains mandatory', async () => {
  const image = `sha256:${'a'.repeat(64)}`;
  const config = { appContainer: 'pogo-app', workerContainer: 'pogo-browser', appImage: image, workerImage: image };
  const inspect = health => async (_program, args) => {
    assert.match(args[2], /\{\{if \.State.Health\}\}/);
    return JSON.stringify({ image, running: true, health: args.at(-1) === 'pogo-app' ? null : health });
  };
  const result = await verifyImagePair(config, inspect('healthy'));
  assert.equal(result.app.health, null);
  assert.match(result.app.readiness, /authenticated API/);
  await assert.rejects(verifyImagePair(config, inspect(null)), /worker container not healthy/);
  await assert.rejects(verifyImagePair(config, inspect('starting')), /worker container not healthy/);
});

test('read-only migration checks existing registry without changing it', async () => {
  const requests = [], names = [];
  const app = async (path, method = 'GET', body) => {
    requests.push({ path, method });
    if (path === '/api/panels') return { status: 200, body: { panels: [{ path: '/panels/registered/', entry: '/panels/registered/home' }] } };
    return { status: 200, headers: { 'cache-control': 'no-store' }, body: { mappings: body.urls.map((url, i) => i === 1 ? { url, error: 'unresolved' } : { url, resolved_url: i === 2 ? 'https://private.invalid/home' : url }) } };
  };
  const result = await migrationChecks(app, (name, value) => { assert.ok(value, name); names.push(name); });
  assert.ok(result.report.registered_entry_checked); assert.equal(names.length, 5);
  assert.deepEqual(requests, [{ path: '/api/panels', method: 'GET' }, { path: '/api/browser/migrate-urls', method: 'POST' }, { path: '/api/panels', method: 'GET' }]);
  assert.ok(!JSON.stringify(result.report).includes('private.invalid'));
});

test('real Chromium smoke observes one authenticated request and reliably deletes its own test context', async t => {
  const fixture = await createFixture({ host: '127.0.0.1', advertisedHost: '127.0.0.1' });
  t.after(() => fixture.close());
  const worker = await createWorker({ token: 'controlled-native-only-token' });
  const address = await worker.listen(0);
  t.after(() => worker.close());
  const checks = [];
  const result = await workerChecks(httpTransport(`http://127.0.0.1:${address.port}`, { Authorization: 'Bearer controlled-native-only-token' }), fixture,
    (name, value) => { assert.ok(value, name); checks.push(name); });
  assert.equal(result.test_session_deleted, true);
  assert.equal(result.fixture.data, 1); assert.equal(result.fixture.authenticated, 1);
  assert.ok(checks.includes('test worker session deleted and cannot be reopened by state'));
});

test('worker smoke attempts test context cleanup after a failed assertion', async () => {
  const calls = [];
  const worker = async (path, method = 'GET') => {
    calls.push({ path, method });
    if (path.endsWith('/state')) return method === 'GET' && calls.some(call => call.method === 'DELETE') ? { status: 404, body: {} } : { status: 200, body: { backend: 'legacy' } };
    return { status: 200, body: { ok: true } };
  };
  await assert.rejects(workerChecks(worker, { url: () => 'http://fixture.invalid/' }, (name, value) => assert.ok(value, name)), /native without/);
  assert.equal(calls.filter(call => call.method === 'DELETE').length, 1);
  assert.match(calls.find(call => call.method === 'DELETE').path, /^\/sessions\/native-only-accept-/);
});
