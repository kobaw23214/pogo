import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { ScopedContext } from '../scoped-context.mjs';

function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
function page() {
  const result = Object.assign(new EventEmitter(), {
    closed: false, scripts: [], timeouts: [], isClosed() { return this.closed; },
    async addInitScript(...args) { this.scripts.push(args); },
    setDefaultTimeout(timeout) { this.timeouts.push(['default', timeout]); },
    setDefaultNavigationTimeout(timeout) { this.timeouts.push(['navigation', timeout]); },
  });
  return result;
}
function scope(cookieValue = 'local-partition') {
  const calls = [];
  const browserContext = { async newCDPSession(target) { calls.push(target); return { detach: async () => {} }; } };
  const electronSession = { cookies: { async get(filter) { assert.deepEqual(filter, {}); return [{ name: 'session', value: cookieValue, domain: '.fixture.invalid', session: true, httpOnly: true, secure: true, sameSite: 'strict' }]; } } };
  return { value: new ScopedContext({ browserContext, electronSession }), calls, browserContext };
}

test('scoped page events, scripts, timeouts and cookies never spill into another owner', async () => {
  const a = scope('owner-a'), b = scope('owner-b'), first = page(), second = page(), events = [];
  a.value.setDefaultTimeout(1200); a.value.setDefaultNavigationTimeout(2000);
  await a.value.addInitScript({ content: 'owned A only' });
  a.value.on('page', value => { assert.equal(value.scripts.length, 1); events.push('page'); });
  for (const event of ['request', 'response', 'requestfinished', 'requestfailed']) a.value.on(event, value => events.push([event, value]));
  await Promise.all([a.value.register(first), b.value.register(second)]);
  assert.deepEqual(first.timeouts, [['default', 1200], ['navigation', 2000]]);
  assert.equal(second.scripts.length, 0); assert.equal(second.timeouts.length, 0);
  for (const event of ['request', 'response', 'requestfinished', 'requestfailed']) { first.emit(event, 'A'); second.emit(event, 'B'); }
  assert.deepEqual(events, ['page', ['request', 'A'], ['response', 'A'], ['requestfinished', 'A'], ['requestfailed', 'A']]);
  await a.value.addInitScript('later A'); assert.equal(first.scripts.length, 2); assert.equal(second.scripts.length, 0);
  assert.equal((await a.value.cookies())[0].value, 'owner-a'); assert.equal((await b.value.cookies())[0].value, 'owner-b');
  assert.deepEqual((await a.value.cookies())[0], { name: 'session', value: 'owner-a', domain: '.fixture.invalid', path: '/', expires: -1, httpOnly: true, secure: true, sameSite: 'Strict' });
  const copy = a.value.pages(); copy.length = 0; assert.equal(a.value.pages().length, 1);
  await a.value.close();
  first.emit('request', 'late A'); assert.equal(events.length, 5);
  assert.equal(first.closed, false); assert.equal(second.closed, false, 'scope close must not close a global browser or visible views');
  assert.equal(b.value.pages().length, 1); await b.value.close();
});

test('CDP requires an owned Page or owned frame, including after an asynchronous attach', async () => {
  const a = scope(), first = page(), foreign = page();
  first.context = () => a.browserContext;
  await a.value.register(first);
  await a.value.newCDPSession(first);
  const frame = { page: () => first }; await a.value.newCDPSession(frame);
  await assert.rejects(a.value.newCDPSession(foreign), /outside/);
  await assert.rejects(a.value.newCDPSession({ page: () => foreign }), /outside/);
  assert.deepEqual(a.calls, [first, frame]);
  const pending = deferred(); let detached = false;
  a.browserContext.newCDPSession = () => pending.promise;
  const attachment = a.value.newCDPSession(first); a.value.unregister(first);
  pending.resolve({ detach: async () => { detached = true; } });
  await assert.rejects(attachment, /no longer registered/); assert.equal(detached, true);
  await a.value.close();
});

test('an owned page delegates CDP to its actual partition context without a default-context fallback', async () => {
  const a = scope(), first = page(); let attached = false;
  first.context = () => ({ newCDPSession: async target => { assert.equal(target, first); attached = true; return { detach: async () => {} }; } });
  await a.value.register(first); await a.value.newCDPSession(first);
  assert.equal(attached, true); assert.equal(a.calls.length, 0);
  first.context = () => { throw new Error('partition context unavailable'); };
  await assert.rejects(a.value.newCDPSession(first), /partition context unavailable/);
  assert.equal(a.calls.length, 0); await a.value.close();
});

test('concurrent register and init scripts publish one ready page with every script installed once', async () => {
  const a = scope(), first = page(), pending = deferred(); let published = 0;
  await a.value.addInitScript('first');
  first.addInitScript = async (...args) => { first.scripts.push(args); if (args[0] === 'first') await pending.promise; };
  a.value.on('page', () => { published++; });
  const registration = a.value.register(first), duplicate = a.value.register(first);
  const laterScript = a.value.addInitScript('second'); pending.resolve();
  await Promise.all([registration, duplicate, laterScript]);
  assert.equal(published, 1); assert.deepEqual(first.scripts.map(args => args[0]), ['first', 'second']);
  await a.value.close();
});

test('unregister or close while scripts install cannot publish a late page', async () => {
  const a = scope(), first = page(), pending = deferred(); let published = false;
  await a.value.addInitScript('first'); first.addInitScript = () => pending.promise;
  a.value.on('page', () => { published = true; });
  const registration = a.value.register(first); await a.value.close(); pending.resolve();
  await assert.rejects(registration, /closed/); assert.equal(published, false); assert.equal(a.value.pages().length, 0);
});
