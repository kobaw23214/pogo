import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';

const [modulePath, endpointA, endpointB, scenario, method, closeKind, scopeKind] = process.argv.slice(2);
const { chromium } = await import(pathToFileURL(modulePath));
const a = await chromium.connectOverCDP(endpointA);
const b = await chromium.connectOverCDP(endpointB);
const contextA = a.contexts()[0], contextB = b.contexts()[0];
const pageA = contextA.pages()[0], pageB = contextB.pages()[0];
const connection = pageA._connection;
assert.equal(connection, pageB._connection, 'the test must exercise the actual shared connection');
const server = connection.toImpl(connection);
const pageBGuid = pageB._guid, contextBGuid = contextB._guid;
const targetB = await contextB.newCDPSession(pageB);
const targetBId = (await targetB.send('Target.getTargetInfo')).targetInfo.targetId;
await pageB.goto('http://127.0.0.1:' + new URL(endpointB).port + '/json/version');
await pageB.evaluate(() => { localStorage.setItem('pogo-race-fixture', 'preserved'); document.body.innerHTML = '<input id="retained-input">'; });
await contextB.addCookies([{ name: 'pogo-race-fixture', value: 'preserved', url: pageB.url() }]);

if (scenario === 'unknown-protocol') {
  setImmediate(() => connection.dispatch({ guid: 'page@unknown-test-parent', method: '__create__', params: { type: 'Disposable', guid: 'disposable@unknown-test-child', initializer: {} } }));
  await new Promise(resolve => setTimeout(resolve, 2000));
  throw new Error('unknown protocol was incorrectly swallowed');
}
if (scenario === 'unknown-exception') {
  setImmediate(() => { throw new Error('Pogo unknown application failure'); });
  await new Promise(resolve => setTimeout(resolve, 2000));
  throw new Error('unknown application error was incorrectly swallowed');
}

const scope = scopeKind === 'context' ? contextA : pageA;
const implementation = connection.toImpl(scope);
const dispatcher = server._dispatcherByGuid.get(scope._guid);
assert.ok(dispatcher);
const initialCallbacks = connection._callbacks.size;
let resume, reached;
const paused = new Promise(resolve => { reached = resolve; });
const release = new Promise(resolve => { resume = resolve; });
const originalMethod = implementation[method].bind(implementation);
let disposableGuid;
implementation[method] = async (...args) => {
  // Use the real browser operation and real disposable. Only hold its completed
  // continuation so the existing parent disposal can deterministically overtake it.
  const disposable = await originalMethod(...args);
  disposableGuid = disposable.guid;
  reached();
  await release;
  return disposable;
};
let requestId, terminal, creates = 0;
const originalSend = connection.onmessage;
connection.onmessage = message => {
  if (message.guid === scope._guid && message.method === method) requestId = message.id;
  originalSend(message);
};
const originalReceive = server.onmessage;
server.onmessage = message => {
  if (message.guid === scope._guid && message.method === '__create__' && message.params.type === 'Disposable') creates++;
  if (message.id === requestId && requestId) terminal = message;
  originalReceive(message);
};
const pending = (method === 'addInitScript'
  ? scope.addInitScript(() => { window.__pogoRaceFixture = true; })
  : scope.exposeBinding('pogoRaceFixtureBinding', () => 'fixture'))
  .then(() => ({ success: true }), error => ({ success: false, name: error.name, message: error.message }));
await paused;
assert.ok(disposableGuid && requestId);
if (closeKind === 'disconnect') await a.close();
else if (closeKind === 'context') await contextA.close();
else await pageA.close();
// Closing a page/context yields until both real protocol dispatches completed.
for (let i = 0; i < 20 && connection._objects.has(scope._guid); i++) await new Promise(resolve => setImmediate(resolve));
assert.equal(dispatcher._disposed, true);
assert.equal(connection._objects.has(scope._guid), false);
assert.equal(server._dispatcherByGuid.has(scope._guid), false);
resume();
const timeout = setTimeout(() => { throw new Error('race request did not settle'); }, 3000);
const result = await pending;
clearTimeout(timeout);
for (let i = 0; i < 3; i++) await new Promise(resolve => setImmediate(resolve));
assert.equal(result.success, false);
// PW's public client TargetClosedError retains Error.name; the wire error has
// the precise type used by parseError and its normal request rejection path.
assert.match(result.message, /Target page, context or browser has been closed/);
assert.equal(terminal.error.error.name, 'TargetClosedError');
assert.match(terminal.error.error.stack, /new DisposableDispatcher/);
assert.equal(creates, 0, 'no missing-parent create may reach the client');
assert.equal(connection._callbacks.has(requestId), false);
assert.equal(connection._callbacks.size, initialCallbacks);
assert.equal(connection._objects.has(disposableGuid), false);
assert.equal(server._dispatcherByGuid.has(disposableGuid), false);
assert.equal(connection._closedError, undefined);
assert.equal(b.isConnected(), true);
assert.equal(pageB._guid, pageBGuid);
assert.equal(contextB._guid, contextBGuid);
assert.equal((await targetB.send('Target.getTargetInfo')).targetInfo.targetId, targetBId);
assert.equal(await pageB.evaluate(() => localStorage.getItem('pogo-race-fixture')), 'preserved');
assert.ok((await contextB.cookies()).some(cookie => cookie.name === 'pogo-race-fixture' && cookie.value === 'preserved'));
await pageB.locator('#retained-input').fill('still interactive');
assert.equal(await pageB.locator('#retained-input').inputValue(), 'still interactive');
await pageB.addInitScript(() => { window.__pogoHealthyInit = true; });
await pageB.exposeBinding('pogoHealthyBinding', () => 'healthy');
assert.equal(await pageB.evaluate(() => window.pogoHealthyBinding()), 'healthy');
console.log(JSON.stringify({ passed: true, sharedConnection: true, realOperation: method, scope: scopeKind, close: closeKind, parentDisposed: true, noOrphanCreate: true, terminalRejected: true, callbackSettled: true, siblingTargetPreserved: true, siblingStoragePreserved: true, siblingInputAndBindingUsable: true, processSurvived: true }));
await b.close();
if (a.isConnected()) await a.close();
