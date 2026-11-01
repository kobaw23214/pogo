import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { createServer } from 'node:http';
import { chromium } from 'playwright';
import { TargetTree } from '../src/cdp-targets.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, accept = Boolean, ms = 5000) {
  const end = performance.now() + ms;
  while (performance.now() < end) {
    const value = await read();
    if (accept(value)) return value;
    await delay(10);
  }
  assert.fail('Expected target condition before deadline');
}

// Test the trust boundary using hostile/stale envelopes as well as normal CDP.
class FakeRoot extends EventEmitter {
  constructor() { super(); this.calls = []; this.detaches = 0; this.hold = new Set(); }
  async send(method, params = {}) {
    this.calls.push({ path: [], method, params });
    if (method === 'Target.getTargetInfo') return { targetInfo: { targetId: 'root', type: 'page', url: 'https://example.test/' } };
    if (method === 'Target.sendMessageToTarget') this.dispatch([params.sessionId], JSON.parse(params.message));
    return {};
  }
  dispatch(path, message) {
    this.calls.push({ path, ...message });
    if (message.method === 'Target.sendMessageToTarget') this.dispatch([...path, message.params.sessionId], JSON.parse(message.params.message));
    if (this.hold.has(message.method)) return;
    const result = message.method === 'Runtime.evaluate' ? { value: path.join('/') } : {};
    queueMicrotask(() => this.message(path, { id: message.id, result }));
  }
  message(path, message) {
    for (let index = path.length - 1; index > 0; index--) message = { method: 'Target.receivedMessageFromTarget', params: { sessionId: path[index], message: JSON.stringify(message) } };
    this.emit('Target.receivedMessageFromTarget', { sessionId: path[0], message: JSON.stringify(message) });
  }
  event(path, method, params) {
    if (path.length) this.message(path, { method, params });
    else this.emit(method, params);
  }
  attach(path, sessionId, targetId, type = 'worker') {
    this.event(path, 'Target.attachedToTarget', { sessionId, targetInfo: { targetId, type, url: `https://example.test/${targetId}` }, waitingForDebugger: true });
  }
  async detach() { this.detaches++; this.emit('close', this); }
}

test('owned descendant sessions route independently and refuse forged session envelopes', async () => {
  const root = new FakeRoot();
  const attached = [];
  const tree = new TargetTree(root, { onAttach: descriptor => { attached.push(descriptor); } });
  await tree.start();
  try {
    assert.deepEqual(root.calls.find(call => call.method === 'Target.setAutoAttach').params.filter, [{ type: 'iframe' }, { type: 'worker' }, { exclude: true }]);
    root.attach([], 'iframe-session', 'iframe', 'iframe');
    await tree.get('iframe').ready;
    root.attach(['iframe-session'], 'worker-session', 'worker');
    await tree.get('worker').ready;
    assert.deepEqual(await tree.get('iframe').client.send('Runtime.evaluate'), { value: 'iframe-session' });
    assert.deepEqual(await tree.get('worker').client.send('Runtime.evaluate'), { value: 'iframe-session/worker-session' });
    assert.equal(tree.get('worker').parentTargetId, 'iframe');
    let received = 0;
    tree.get('worker').client.on('Runtime.consoleAPICalled', () => received++);
    root.event(['unknown-session'], 'Runtime.consoleAPICalled', {});
    root.event(['iframe-session', 'unknown-session'], 'Runtime.consoleAPICalled', {});
    root.message(['iframe-session', 'worker-session'], { sessionId: 'foreign-session', method: 'Runtime.consoleAPICalled', params: {} });
    assert.equal(received, 0);
    root.event(['iframe-session', 'worker-session'], 'Runtime.consoleAPICalled', {});
    assert.equal(received, 1);
    assert.equal(tree.rejected.unknown_session, 2);
    assert.equal(tree.rejected.malformed, 1);
    assert.equal(attached.length, 2);
  } finally { await tree.close(); }
  assert.equal(root.detaches, 1);
  assert.equal(tree.list().length, 0);
  assert.equal(root.listenerCount('Target.attachedToTarget'), 0);
});

test('capacity and unsupported targets are resumed and detached without becoming routable', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root, { maxTargets: 2 });
  await tree.start();
  try {
    root.attach([], 'one', 'one');
    await tree.get('one').ready;
    root.attach([], 'two', 'two');
    root.attach([], 'shared', 'shared', 'shared_worker');
    await until(() => root.calls.filter(call => call.method === 'Target.detachFromTarget').length, count => count === 2);
    assert.equal(tree.get('two'), undefined);
    assert.equal(tree.get('shared'), undefined);
    assert.equal(tree.list().length, 2, 'limit includes the root page');
    for (const sessionId of ['two', 'shared']) {
      const resume = root.calls.findIndex(call => call.path.join('/') === sessionId && call.method === 'Runtime.runIfWaitingForDebugger');
      const detach = root.calls.findIndex(call => call.method === 'Target.detachFromTarget' && call.params.sessionId === sessionId);
      assert.ok(resume >= 0 && detach > resume, 'resume startup pause before detaching rejected target');
    }
  } finally { await tree.close(); }
});

test('parent detach clears descendant commands and prevents late response resurrection', async () => {
  const root = new FakeRoot();
  const detached = [];
  const tree = new TargetTree(root, { onDetach: descriptor => detached.push(descriptor.targetId) });
  await tree.start();
  root.attach([], 'parent-session', 'parent', 'iframe');
  await tree.get('parent').ready;
  root.attach(['parent-session'], 'child-session', 'child');
  await tree.get('child').ready;
  const parent = tree.get('parent').client;
  const child = tree.get('child').client;
  root.hold.add('Runtime.evaluate');
  const parentCall = assert.rejects(parent.send('Runtime.evaluate'), /CDP_TARGET_DETACHED/);
  const childCall = assert.rejects(child.send('Runtime.evaluate'), /CDP_TARGET_DETACHED/);
  root.event([], 'Target.detachedFromTarget', { sessionId: 'parent-session' });
  await Promise.all([parentCall, childCall]);
  await until(() => detached.length, size => size === 2);
  assert.equal(parent.pending.size, 0);
  assert.equal(child.pending.size, 0);
  assert.deepEqual(new Set(detached), new Set(['parent', 'child']));
  root.event(['parent-session', 'child-session'], 'Target.attachedToTarget', { sessionId: 'foreign', targetInfo: { targetId: 'foreign', type: 'worker' } });
  assert.equal(tree.get('foreign'), undefined);
  await assert.rejects(child.send('Runtime.enable'), /CDP_TARGET_DETACHED/);
  await tree.close();
});

test('collector initialization failure and timeout release paused children', async () => {
  for (const hang of [false, true]) {
    const root = new FakeRoot();
    const errors = [];
    const tree = new TargetTree(root, { startupTimeoutMs: 40, onAttach: () => hang ? new Promise(() => {}) : Promise.reject(new Error('collector failed')), onError: cause => errors.push(cause) });
    await tree.start();
    root.attach([], 'worker-session', 'worker');
    await until(() => tree.get('worker'), value => !value);
    await until(() => root.calls.some(call => call.method === 'Target.detachFromTarget'));
    assert.equal(errors.length, 1);
    assert.ok(root.calls.some(call => call.method === 'Runtime.runIfWaitingForDebugger' && call.path[0] === 'worker-session'));
    await tree.close();
  }
});

test('command deadlines and protocol errors remove pending calls; breakpoint time is excluded', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root, { commandTimeoutMs: 40 });
  await tree.start();
  try {
    root.attach([], 'worker-session', 'worker');
    await tree.get('worker').ready;
    const client = tree.get('worker').client;
    root.hold.add('Runtime.evaluate');
    await assert.rejects(client.send('Runtime.evaluate'), /CDP_TARGET_TIMEOUT/);
    assert.equal(client.pending.size, 0);
    const protocolFailure = assert.rejects(client.send('Runtime.evaluate'), cause => cause.message === 'CDP_TARGET_PROTOCOL_ERROR (-32000): No execution context found' && cause.protocolCode === -32000);
    const failedId = root.calls.at(-1).id;
    root.message(['worker-session'], { id: failedId, error: { code: -32000, message: 'No execution context found' } });
    await protocolFailure;
    assert.equal(client.pending.size, 0);
    const paused = client.send('Runtime.evaluate');
    const pausedId = root.calls.at(-1).id;
    root.event(['worker-session'], 'Debugger.paused', {});
    await delay(80);
    assert.ok(client.pending.has(pausedId));
    // New inspection requests still expire even if the debuggee remains paused.
    await assert.rejects(client.send('Runtime.evaluate'), /CDP_TARGET_TIMEOUT/);
    root.event(['worker-session'], 'Debugger.resumed', {});
    root.message(['worker-session'], { id: pausedId, result: { resumed: true } });
    assert.deepEqual(await paused, { resumed: true });
  } finally { await tree.close(); }
});

test('protocol failures retain bounded code and reason and duplicate replies cannot settle another call', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root);
  await tree.start();
  try {
    root.attach([], 'worker-session', 'worker');
    await tree.get('worker').ready;
    const client = tree.get('worker').client;
    root.hold.add('Runtime.evaluate');
    let firstSettlements = 0;
    const first = client.send('Runtime.evaluate').catch(cause => { firstSettlements++; return cause; });
    const firstId = root.calls.at(-1).id;
    root.message(['worker-session'], { id: firstId, error: { code: -32601, message: 'Method not found' } });
    const firstError = await first;
    assert.match(firstError.message, /-32601.*Method not found/);
    const second = client.send('Runtime.evaluate').catch(cause => cause);
    const secondId = root.calls.at(-1).id;
    root.message(['worker-session'], { id: firstId, result: { stale: true } });
    root.message(['worker-session'], { id: firstId, error: { code: -32602, message: 'Duplicate old reply' } });
    assert.equal(client.pending.size, 1);
    root.message(['worker-session'], { id: secondId, error: { code: -32602, message: `Invalid parameters: ${'x'.repeat(2000)}` } });
    const secondError = await second;
    assert.match(secondError.message, /-32602.*Invalid parameters/);
    assert.ok(secondError.message.length < 850);
    assert.equal(firstSettlements, 1);
    assert.equal(client.pending.size, 0);
  } finally { await tree.close(); }
});

test('reset closes pending startup and commands with no retained event listeners', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root, { onAttach: () => new Promise(() => {}) });
  await tree.start();
  root.attach([], 'worker-session', 'worker');
  const descriptor = tree.get('worker');
  root.hold.add('Runtime.evaluate');
  const call = assert.rejects(descriptor.client.send('Runtime.evaluate'), /CDP_TARGET_DETACHED/);
  await tree.close();
  await call;
  await descriptor.ready;
  assert.equal(tree.initializing.size, 0);
  assert.equal(descriptor.client.pending.size, 0);
  assert.equal(descriptor.client.eventNames().length, 0);
  await tree.close();
  assert.equal(root.detaches, 1);
});

test('destructive close rejects ordinary cleanup and permits only atomic paused execution termination', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root);
  await tree.start();
  root.attach([], 'parent-session', 'parent', 'iframe');
  await tree.get('parent').ready;
  root.attach(['parent-session'], 'worker-session', 'worker');
  await tree.get('worker').ready;
  const child = tree.get('worker').client;
  root.event(['parent-session', 'worker-session'], 'Debugger.paused', {});
  const cutoff = root.calls.length;
  tree.prepareClose();
  await assert.rejects(child.send('Debugger.resume', {}, { cleanup: true }), /CDP_TARGET_TRANSPORT_ERROR/);
  await child.send('Debugger.resume', { terminateOnResume: true }, { cleanup: true });
  root.attach([], 'late-session', 'late-worker');
  await tree.close();
  const commands = root.calls.slice(cutoff).filter(call => call.method !== 'Target.sendMessageToTarget');
  assert.deepEqual(commands.map(call => [call.method, call.params]), [['Debugger.resume', { terminateOnResume: true }]]);
  assert.equal(root.detaches, 0, 'owner destruction must remove the real root session, not debugger detach');
});

test('sibling breakpoint and external family pause suspend commands until inspection resumes', async () => {
  const root = new FakeRoot();
  let familyPaused = false;
  const tree = new TargetTree(root, { commandTimeoutMs: 30, isPaused: () => familyPaused });
  await tree.start();
  try {
    root.attach([], 'iframe-session', 'iframe', 'iframe');
    await tree.get('iframe').ready;
    root.hold.add('Runtime.evaluate');
    const client = tree.get('iframe').client;
    const execution = client.send('Runtime.evaluate');
    const id = root.calls.at(-1).id;
    familyPaused = true;
    await delay(70);
    assert.ok(client.pending.has(id));
    familyPaused = false;
    root.message(['iframe-session'], { id, result: { resumed: true } });
    assert.deepEqual(await execution, { resumed: true });
  } finally { await tree.close(); }
});

test('retiring ancestors continue to drain nested cleanup and reattachment retains the new descriptor', async () => {
  const root = new FakeRoot();
  const tree = new TargetTree(root);
  await tree.start();
  root.attach([], 'old-parent', 'parent', 'iframe');
  await tree.get('parent').ready;
  root.attach(['old-parent'], 'child-session', 'child');
  await tree.get('child').ready;
  const old = tree.get('parent');
  const retirement = old.client.detach();
  root.attach([], 'new-parent', 'parent', 'iframe');
  const current = tree.get('parent');
  assert.notEqual(current, old);
  await Promise.all([retirement, current.ready]);
  assert.equal(tree.get('parent'), current);
  assert.equal(tree.get('child'), undefined);
  assert.equal(old.client.pending.size, 0);
  await tree.close();
});

test('real Chromium captures OOPIF and nested dedicated-worker startup through public CDPSession', { timeout: 20000 }, async () => {
  const server = createServer((request, response) => {
    const origin = `http://127.0.0.1:${server.address().port}`;
    if (request.url === '/worker.js') {
      response.setHeader('content-type', 'application/javascript');
      response.end('console.log("first-worker-log"); globalThis.marker = 42; globalThis.nested = new Worker("/nested.js");');
    } else if (request.url === '/nested.js') {
      response.setHeader('content-type', 'application/javascript');
      response.end('console.log("first-nested-log"); globalThis.marker = 84;');
    } else {
      response.setHeader('content-type', 'text/html');
      response.end(request.url === '/child' ? '<title>OOPIF child</title><p id="answer">child evidence</p>' : `<title>Owner</title><iframe src="${origin.replace('127.0.0.1', 'localhost')}/child"></iframe><script>globalThis.worker = new Worker('/worker.js')</script>`);
    }
  });
  await new Promise(resolve => server.listen(0, '0.0.0.0', resolve));
  const browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
  let tree;
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    const logs = [];
    const errors = [];
    tree = new TargetTree(await context.newCDPSession(page), {
      onAttach: async descriptor => {
        descriptor.client.on('Runtime.consoleAPICalled', event => logs.push({ targetId: descriptor.targetId, args: event.args }));
        await descriptor.client.send('Runtime.enable');
        if (descriptor.type === 'iframe') await descriptor.client.send('Page.enable');
      },
      onError: cause => errors.push(cause.message),
    });
    await tree.start();
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const worker = await until(() => tree.list().find(target => target.url.endsWith('/worker.js')));
    const nested = await until(() => tree.list().find(target => target.url.endsWith('/nested.js')));
    const iframe = await until(() => tree.list().find(target => target.type === 'iframe' && target.url.endsWith('/child')));
    await Promise.all([worker.ready, nested.ready, iframe.ready]);
    assert.equal(nested.parentTargetId, worker.targetId);
    assert.equal((await worker.client.send('Runtime.evaluate', { expression: 'globalThis.marker', returnByValue: true })).result.value, 42);
    assert.equal((await nested.client.send('Runtime.evaluate', { expression: 'globalThis.marker', returnByValue: true })).result.value, 84);
    assert.equal((await iframe.client.send('Runtime.evaluate', { expression: 'document.querySelector("#answer").textContent', returnByValue: true })).result.value, 'child evidence');
    await until(() => logs.filter(item => item.args.some(arg => arg.value === 'first-worker-log' || arg.value === 'first-nested-log')).length, size => size === 2);
    assert.deepEqual(errors, []);
    await page.evaluate(() => worker.terminate());
    await until(() => tree.get(worker.targetId) || tree.get(nested.targetId), value => !value);
    assert.equal(tree.get(iframe.targetId).type, 'iframe');
    await page.locator('iframe').evaluate(element => element.remove());
    await until(() => tree.get(iframe.targetId), value => !value);
  } finally {
    await tree?.close();
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
});

test('real Chromium capacity refusal and collector failure never leave startup workers suspended', { timeout: 15000 }, async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const refuseByCapacity of [true, false]) {
      const context = await browser.newContext();
      const page = await context.newPage();
      const failures = [];
      const tree = new TargetTree(await context.newCDPSession(page), {
        maxTargets: refuseByCapacity ? 1 : 8,
        onAttach: () => { throw new Error('Collector unavailable'); },
        onError: cause => failures.push(cause.message),
      });
      try {
        await tree.start();
        await page.evaluate(() => {
          globalThis.workerReady = false;
          const url = URL.createObjectURL(new Blob(['postMessage("ready")'], { type: 'application/javascript' }));
          globalThis.worker = new Worker(url);
          worker.onmessage = () => { globalThis.workerReady = true; URL.revokeObjectURL(url); };
        });
        await page.waitForFunction(() => globalThis.workerReady, undefined, { timeout: 3000 });
        await until(() => tree.list().length, size => size === 1);
        assert.ok(failures.includes(refuseByCapacity ? 'CDP_TARGET_LIMIT' : 'Collector unavailable'));
      } finally { await tree.close(); await context.close(); }
    }
  } finally { await browser.close(); }
});
