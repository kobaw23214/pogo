// A synthetic message/clock harness; no editor runtime or document is opened.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import vm from 'node:vm';

const source = await fs.readFile(new URL('./bridge/plugin.js', import.meta.url), 'utf8');
const scope = { sessionId: 'session-startup-test', documentId: 'document-startup-test',
  token: 'synthetic-startup-token-with-more-than-32-characters', editor: 'word' };
const protocol = 'pogo-office-lab/v1';
const origin = 'http://127.0.0.1:19896';
const detected = { id: 'pogo-office-history', version: 1, editor: 'word',
  readPreservesHistory: true, readPreservesSelection: true, nativeUndo: true, nativeRedo: true,
  historyWithoutRead: true, mutationPreconditionsPreserveHistory: true };
const envelope = (value) => JSON.stringify({ ok: true, result: value });
const flush = async () => { for (let index = 0; index < 12; index++) await Promise.resolve(); };

function harness({ deferCapabilities = false } = {}) {
  let now = 0, serial = 0;
  const timers = new Map(), messages = [], calls = [], callbacks = [], listeners = new Map();
  const top = { postMessage(data, target) { messages.push({ data: JSON.parse(JSON.stringify(data)), target }); } };
  const plugin = { info: { editorType: 'word' }, executeMethod(name, args, callback) {
    calls.push({ name, args });
    if (name === 'PogoOfficeCapabilities') {
      callbacks.push(callback);
      if (!deferCapabilities) callback(envelope(detected));
    } else throw new Error('Startup must never call a native document operation.');
  } };
  const context = vm.createContext({ URLSearchParams, location: { origin },
    setTimeout(fn, delay) { const id = ++serial; timers.set(id, { at: now + delay, fn }); return id; },
    clearTimeout(id) { timers.delete(id); },
    window: { top, Asc: { plugin }, __pogoOfficeLabScope: scope,
      addEventListener(type, handler) { listeners.set(type, handler); } },
  });
  vm.runInContext(source, context);
  function send(type, fields = {}, event = {}) {
    const value = { protocol, type, ...scope, ...fields };
    const data = vm.runInContext(`JSON.parse(${JSON.stringify(JSON.stringify(value))})`, context);
    return listeners.get('message')?.({ source: top, origin, data, ...event });
  }
  async function advance(ms) {
    const target = now + ms;
    while (true) {
      const due = [...timers].filter(([, value]) => value.at <= target).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      now = due[1].at; timers.delete(due[0]); due[1].fn(); await flush();
    }
    now = target; await flush();
  }
  return { plugin, send, advance, messages, calls, timers, finish: (index = 0, value = detected) => callbacks[index](envelope(value)),
    pagehide: () => listeners.get('pagehide')?.() };
}

test('lost ready messages are repeated every five seconds until a scoped host acknowledgement', async () => {
  const h = harness();
  await h.plugin.init();
  assert.equal(h.messages.length, 1);
  assert.equal(h.messages[0].data.type, 'ready');
  assert.equal(h.messages[0].data.capabilities.read, true);
  await h.advance(15000);
  assert.equal(h.messages.length, 4);
  for (const message of h.messages) assert.deepEqual(message, h.messages[0]);
  await h.send('ready-ack');
  await h.advance(300000);
  assert.equal(h.messages.length, 4);
  assert.equal(h.calls.length, 1);
  assert.equal(h.timers.size, 0);
});

test('forged ready acknowledgement and hello cannot stop retry or touch the editor', async () => {
  const h = harness();
  await h.plugin.init();
  await h.send('ready-ack', {}, { origin: 'https://wrong.invalid' });
  await h.send('ready-ack', {}, { source: {} });
  for (const key of ['sessionId', 'documentId', 'token']) await h.send('ready-ack', { [key]: 'wrong' });
  await h.send('retry-bootstrap', { token: 'wrong' });
  await h.advance(5000);
  assert.equal(h.messages.length, 2);
  assert.equal(h.calls.length, 1);
  await h.send('ready-ack');
  h.pagehide();
});

test('hello and retry on a verified ready plugin resend cached capabilities without native calls', async () => {
  const h = harness();
  await h.plugin.init();
  await h.send('ready-ack');
  await h.send('hello');
  await h.send('ready-ack');
  await h.send('retry-bootstrap');
  await h.send('ready-ack');
  assert.equal(h.messages.length, 3);
  assert.deepEqual(h.messages[2], h.messages[0]);
  assert.deepEqual(h.calls.map((call) => call.name), ['PogoOfficeCapabilities']);
  h.pagehide();
});

test('slow bootstrap keeps a five-minute budget, never advertises unsupported, and accepts late success', async () => {
  const h = harness({ deferCapabilities: true });
  const init = h.plugin.init();
  await flush();
  await h.advance(299999);
  assert.equal(h.messages.length, 0);
  await h.advance(1);
  await init;
  assert.equal(h.messages.length, 1);
  assert.equal(h.messages[0].data.type, 'bootstrap-error');
  assert.equal(h.messages[0].data.recoverable, true);
  assert.equal(h.messages[0].data.error.code, 'ENGINE_BOOTSTRAP_TIMEOUT');
  assert.equal(h.messages[0].data.capabilities, undefined);
  h.finish(); await flush();
  assert.equal(h.messages[1].data.type, 'ready');
  assert.equal(h.messages[1].data.capabilities.read, true);
  await h.send('ready-ack');
  assert.equal(h.timers.size, 0);
});

test('pending bootstrap is single-flight and explicit retry after timeout is read-only and generation safe', async () => {
  const h = harness({ deferCapabilities: true });
  const first = h.plugin.init(), duplicate = h.plugin.init();
  const earlyRetry = h.send('retry-bootstrap');
  await flush();
  assert.equal(h.calls.length, 1);
  await h.advance(300000);
  await Promise.all([first, duplicate, earlyRetry]);
  const retried = h.send('retry-bootstrap');
  await flush();
  assert.equal(h.calls.length, 2);
  const beforeHello = h.messages.length;
  await h.send('hello');
  assert.equal(h.messages.length, beforeHello, 'A new probe must not replay a previous bootstrap timeout.');
  // A late result from the previous read must not disable a newer connection.
  h.finish(0, { id: 'outdated-adapter' }); await flush();
  assert.equal(h.messages.at(-1).data.type, 'bootstrap-error');
  h.finish(1); await retried; await flush();
  assert.equal(h.messages.at(-1).data.type, 'ready');
  assert.equal(h.messages.at(-1).data.capabilities.read, true);
  assert.deepEqual(h.calls.map((call) => call.name), ['PogoOfficeCapabilities', 'PogoOfficeCapabilities']);
  await h.send('ready-ack');
});

test('closing the plugin stops ready retransmission', async () => {
  const h = harness();
  await h.plugin.init();
  h.pagehide();
  await h.advance(300000);
  assert.equal(h.messages.length, 1);
  assert.equal(h.timers.size, 0);
});
