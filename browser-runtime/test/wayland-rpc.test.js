import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { LineRPC } from '../src/wayland-rpc.js';
import { waylandViewport } from '../src/wayland.js';

function channel() {
  const input = new PassThrough(), output = new PassThrough();
  const requests = [];
  output.on('data', bytes => requests.push(JSON.parse(bytes)));
  const rpc = new LineRPC(input, output, { timeout: 100 });
  return { input, output, requests, rpc, reply: value => input.write(JSON.stringify(value) + '\n') };
}
test('an expired local operation is never replayed or accepted as the next request', async () => {
  const c = channel();
  await assert.rejects(c.rpc.request('pointer', { page: 't1' }, 5), /unknown; do not replay/);
  const second = c.rpc.request('release', { page: 't1' });
  c.reply({ id: c.requests[0].id, result: { accepted: true } });
  c.reply({ id: c.requests[1].id, result: { released: true } });
  assert.deepEqual(await second, { released: true });
  assert.deepEqual(c.requests.map(request => request.op), ['pointer', 'release']);
  c.rpc.close();
});
test('IPC disconnect rejects every pending operation instead of confirming cleanup', async () => {
  const c = channel();
  const pending = c.rpc.request('release');
  c.input.end();
  await assert.rejects(pending, /closed/);
  await assert.rejects(c.rpc.request('release'), /closed/);
});
test('invalid IPC and scope rejection cannot authorize a media peer', async () => {
  const c = channel();
  const pending = c.rpc.request('create');
  c.reply({ id: c.requests[0].id, ok: false, error: 'SCOPE_MISMATCH' });
  await assert.rejects(pending, /SCOPE_MISMATCH/);
  const next = c.rpc.request('create'); c.input.write('not json\n');
  await assert.rejects(next, /invalid local IPC/);
});
test('native rejection diagnostics preserve operation and fixed reason without exposing exception content', async () => {
  const c = channel(), events = [], secret = 'synthetic-private-capability-7d24b837';
  c.rpc.on('rejected', value => events.push(value));
  try {
    for (const [payload, expectedCode, expectedReason] of [
      [{ code: 'HOST_FAILED', message: 'Unknown window 71 ' + secret }, 'HOST_FAILED', 'WINDOW_GONE'],
      [{ code: 'HOST_FAILED', message: secret }, 'HOST_FAILED', 'REJECTED'],
      [secret, 'FAILED', 'REJECTED'],
      [{ code: 'invalid code ' + secret, message: secret }, 'FAILED', 'REJECTED'],
    ]) {
      const pending = c.rpc.request('unbind_sheet', { page: 't1', private_value: secret });
      c.reply({ id: c.requests.at(-1).id, ok: false, error: payload });
      const error = await pending.then(() => assert.fail('rejected operation must fail'), error => error);
      assert.equal(error.localOperation, 'unbind_sheet'); assert.equal(error.localCode, expectedCode); assert.equal(error.localReason, expectedReason);
      assert.deepEqual(events.at(-1), { operation: 'unbind_sheet', code: expectedCode, reason: expectedReason });
      assert.equal(JSON.stringify({ message: error.message, error, event: events.at(-1) }).includes(secret), false, 'request capability and arbitrary exception text must remain private');
    }
  } finally { c.rpc.close(); }
});
test('viewport preserves CSS bounds and produces even encoded pixels at native 1.25 density', () => {
  for (const [request, expected] of [[{width:320,height:200},{width:320,height:200}], [{width:1317,height:783},{width:1320,height:784}], [{width:2560,height:1600},{width:2560,height:1600}]]) {
    const actual = waylandViewport(request); assert.deepEqual(actual, expected);
    assert.equal(actual.width * 1.25 % 2, 0); assert.equal(actual.height * 1.25 % 2, 0);
  }
});
test('unterminated oversized IPC cannot grow an unbounded response buffer', async () => {
  const input = new PassThrough(), output = new PassThrough();
  const rpc = new LineRPC(input, output, { maxLine: 32 });
  const pending = rpc.request('probe_present'); input.write('x'.repeat(33));
  await assert.rejects(pending, /exceeds limit/); assert.equal(rpc.closed, true);
});

test('a paused document without verified native pixels refuses a new media source', async () => {
  const { WaylandSessionHost } = await import('../src/wayland.js');
  const host = new WaylandSessionHost({}, 'paused-source-unit'), calls = [];
  host.rpc = { async request(op) { calls.push(op); return { ack: true }; } };
  const record = { pageId: 't1', page: { isClosed: () => false, async evaluate() { assert.fail('a paused source must not inject or wait for JavaScript'); } } };
  host.session = { pages: new Map([[record.pageId, { documentIdentityReady: true }]]), frameIdentity: () => 'document-known' };
  await assert.rejects(host.sourceFence(record, 'source-new', 'document-new', true), /no verified native pixels/);
  assert.deepEqual(calls, ['capture_stop']);
  record.proof = { key: 'source-old', documentKey: 'document-known', seq: 77 };
  assert.equal(await host.sourceFence(record, 'source-new-owner', 'document-known', true), 77, 'only a verified same document and geometry can authorize its clean paused buffer');
  await assert.rejects(host.sourceFence(record, 'source-new', 'document-new-size', true), /no verified native pixels/);
});
