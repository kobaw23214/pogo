import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';

// All requests and credentials below belong to this isolated, synthetic site.
// Exercise the public HTTP worker contract, not BrowserDevTools internals.
const token = 'devtools-test-only-worker-token';
const fixtureToken = 'synthetic-devtools-access-token';
const fixturePassword = 'synthetic-devtools-password';
const fixtureCookie = 'synthetic-devtools-cookie';
const fixtureHeader = 'synthetic-devtools-header';
let worker, site, base, origin, sequence = 0;
const requests = new Map();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function request(path, method = 'GET', body) {
  // These single-page fixtures bind ownership to the observed tab explicitly.
  if (path.endsWith('/control') && body) {
    const state = (await request(path.slice(0, -8) + '/state')).data;
    body = { tab_id: state.active_tab, context_ref: state.context_ref, ...body };
  }
  const response = await fetch(base + path, {
    method,
    headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: response.status, data: await response.json() };
}
const execute = (session, args) => request(`/sessions/${session}/execute`, 'POST', { op_id: `devtools-${++sequence}`, ...args });
const devtools = (session, method, params = {}) => execute(session, { action: 'devtools', method, params });
function successful(response) {
  assert.equal(response.status, 200, JSON.stringify(response.data));
  assert.equal(response.data.ok, true, JSON.stringify(response.data));
  assert.ok(response.data.data && typeof response.data.data === 'object', 'devtools returns structured data');
  return response.data.data;
}
function refused(response) {
  assert.ok(response.status >= 400 || response.data.ok === false, `unexpected success: ${JSON.stringify(response.data)}`);
  return response.data;
}
function unavailableRequest(response) {
  if (response.status >= 400 || response.data.ok === false) return;
  const value = successful(response);
  assert.equal(value.state, 'unavailable', JSON.stringify(value));
  assert.equal(value.request, undefined, 'an unavailable request must not carry another target\'s record');
}
async function waitFor(read, matches, description) {
  const deadline = Date.now() + 4000;
  let value;
  do {
    value = await read();
    if (matches(value)) return value;
    await delay(25);
  } while (Date.now() < deadline);
  assert.fail(`${description}: ${JSON.stringify(value)}`);
}
async function withSession(id, run) {
  assert.equal((await request(`/sessions/${id}`, 'PUT')).status, 200);
  try {
    // Attach before navigation so the document's original requests are observed.
    successful(await devtools(id, 'help'));
    const navigation = await execute(id, { action: 'navigate', url: `${origin}/fixture?case=${id}` });
    assert.equal(navigation.data.ok, true, JSON.stringify(navigation.data));
    await run(id);
  } finally {
    // A failed assertion while paused must not hang worker cleanup.
    await devtools(id, 'Debugger.resume').catch(() => {});
    const closed = await request(`/sessions/${id}`, 'DELETE');
    assert.equal(closed.status, 200, JSON.stringify(closed.data));
  }
}
async function records(id) {
  return successful(await devtools(id, 'network.list', { limit: 100 })).items;
}
async function findRequest(id, pathname) {
  return waitFor(async () => (await records(id)).find(item => {
    try { return new URL(item.url).pathname === pathname; } catch { return false; }
  }), Boolean, `request ${pathname} was not captured`);
}
async function readBody(id, record) {
  return waitFor(async () => successful(await devtools(id, 'network.read', { request_id: record.request_id })).request,
    item => item.body && !['pending', 'loading'].includes(item.body.state), 'response body did not settle');
}
async function loadRequests(id) {
  assert.equal((await execute(id, { action: 'click', target: '#requests' })).data.ok, true);
  const settled = await execute(id, { action: 'wait', text: 'requests-finished', seconds: 3 });
  assert.equal(settled.data.ok, true, JSON.stringify(settled.data));
}
function requestCount(caseID) {
  return [...requests].filter(([key]) => key.includes(`case=${caseID}`)).reduce((sum, [, count]) => sum + count, 0);
}

before(async () => {
  site = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://devtools-fixture.invalid');
    requests.set(req.url, (requests.get(req.url) ?? 0) + 1);
    if (url.pathname === '/api/ok') {
      res.writeHead(200, { 'Content-Type': 'application/json', 'X-Public-Fixture': 'observed', 'Set-Cookie': `fixture=${fixtureCookie}; HttpOnly; Path=/` });
      res.end(JSON.stringify({ message: 'observed-once', access_token: fixtureToken, nested: { password: fixturePassword } }));
      return;
    }
    if (url.pathname === '/api/unavailable') {
      res.writeHead(503, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ message: 'fixture temporarily unavailable' }));
      return;
    }
    if (url.pathname === '/api/disconnect') { req.socket.destroy(); return; }
    if (url.pathname === '/api/large') {
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      res.end('large-response-start\n' + 'x'.repeat(160_000) + '\nlarge-response-tail');
      return;
    }
    if (url.pathname === '/favicon.ico') { res.writeHead(204); res.end(); return; }
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(`<!doctype html><title>DevTools fixture</title><h1 id="heading">Standard browser protocol</h1>
      <button id="requests">Load responses</button><button id="large">Load large response</button>
      <button id="logs">Write diagnostics</button><output id="status">ready</output>
      <button id="ref-action">Count click</button><output id="ref-count">click-count: 0</output>
      <script>
        let refClicks = 0;
        document.querySelector('#ref-action').onclick = () => { document.querySelector('#ref-count').textContent = 'click-count: ' + (++refClicks); };
        const caseID = new URL(location.href).searchParams.get('case');
        const path = name => name + '?case=' + encodeURIComponent(caseID);
        document.querySelector('#requests').onclick = async () => {
          const xhr = new Promise(resolve => { const request = new XMLHttpRequest(); request.open('GET', path('/api/unavailable')); request.onload = request.onerror = resolve; request.send(); });
          await Promise.allSettled([
            fetch(path('/api/ok') + '&access_token=${fixtureToken}', { headers: { Authorization: 'Bearer ${fixtureHeader}' } }).then(response => response.json()),
            xhr, fetch(path('/api/disconnect')),
          ]);
          document.querySelector('#status').textContent = 'requests-finished';
        };
        document.querySelector('#large').onclick = async () => {
          await fetch(path('/api/large')).then(response => response.text());
          document.querySelector('#status').textContent = 'large-finished';
        };
        document.querySelector('#logs').onclick = () => {
          console.log('fixture-console-message', { safe: 'diagnostic', password: '${fixturePassword}' });
          setTimeout(() => { throw new Error('fixture-uncaught-error'); }, 0);
        };
        setInterval(() => { window.fixtureTicks = (window.fixtureTicks || 0) + 1; }, 30);
      </script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 3000, idleMs: 60_000 });
  const address = await worker.listen(0);
  base = `http://127.0.0.1:${address.port}`;
});

after(async () => {
  await worker?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

test('help, DOM and Runtime use standard CDP in the same current page; retired eval cannot execute', async () => {
  await withSession('cdp-standard', async id => {
    const help = successful(await devtools(id, 'help'));
    assert.equal(help.provenance.scope, 'tab_family');
    assert.equal(help.provenance.target_type, 'page');
    assert.match(JSON.stringify(help), /network\.list/);
    assert.match(JSON.stringify(help), /child|OOPIF|page CDP/i);
    const document = successful(await devtools(id, 'DOM.getDocument', { depth: 1 }));
    assert.equal(document.result.root.nodeName, '#document');
    const evaluation = successful(await devtools(id, 'Runtime.evaluate', {
      expression: '({title: document.title, heading: document.querySelector("#heading").textContent, answer: 6 * 7})', returnByValue: true,
    }));
    assert.deepEqual(evaluation.result.result.value, { title: 'DevTools fixture', heading: 'Standard browser protocol', answer: 42 });
    const compatible = await execute(id, { action: 'eval', expression: 'document.title' });
    assert.equal(compatible.data.ok, false);
    assert.match(compatible.data.error, /retired/);
  });
});

test('CDP receipt warns about invalidated refs; fresh observation restores safe ref actions', async () => {
  await withSession('cdp-ref-feedback', async id => {
    const observe = async () => {
      const snapshot = await execute(id, { action: 'snapshot' });
      assert.equal(snapshot.data.ok, true, JSON.stringify(snapshot.data));
      const ref = /\[(e\d+)\] button "Count click"/.exec(snapshot.data.text)?.[1];
      assert.ok(ref, snapshot.data.text);
      return { ref, text: snapshot.data.text };
    };
    const old = await observe();
    const evaluation = await devtools(id, 'Runtime.evaluate', { expression: 'document.title', returnByValue: true });
    successful(evaluation);
    assert.match(evaluation.data.text, /CDP invalidated element refs/);
    assert.match(evaluation.data.text, /snapshot\/find before using element refs/);
    const stale = refused(await execute(id, { action: 'click', target: old.ref }));
    assert.match(stale.error, /stale ref/);
    const fresh = await observe();
    assert.notEqual(fresh.ref, old.ref);
    assert.match(fresh.text, /click-count: 0/);
    const logs = await devtools(id, 'network.list', { limit: 10 });
    successful(logs);
    assert.doesNotMatch(logs.data.text ?? '', /CDP invalidated element refs/);
    const clicked = await execute(id, { action: 'click', target: fresh.ref });
    assert.equal(clicked.data.ok, true, JSON.stringify(clicked.data));
    assert.match(clicked.data.text, /click-count: 1/);
  });
});

test('network records real fetch, XHR errors and transport failures; list/read never replay a request', async () => {
  await withSession('cdp-network', async id => {
    await loadRequests(id);
    const ok = await findRequest(id, '/api/ok');
    const failure = await findRequest(id, '/api/unavailable');
    const disconnected = await findRequest(id, '/api/disconnect');
    assert.equal(ok.response?.status ?? ok.status, 200);
    assert.equal(failure.response?.status ?? failure.status, 503);
    assert.equal((await readBody(id, ok)).body.state, 'available');
    assert.match((await readBody(id, ok)).body.text, /observed-once/);
    assert.match((await readBody(id, failure)).body.text, /temporarily unavailable/);
    const transport = await readBody(id, disconnected);
    assert.equal(transport.body.state, 'unavailable');
    const before = requestCount(id);
    for (let i = 0; i < 3; i++) {
      await records(id);
      await readBody(id, ok);
      await readBody(id, failure);
      await readBody(id, disconnected);
    }
    const raw = successful(await devtools(id, 'Network.getResponseBody', { requestId: ok.cdp_request_id }));
    assert.equal(raw.result.base64Encoded, false);
    assert.match(raw.result.body, /observed-once/);
    assert.equal(requestCount(id), before, 'observing the log must not send another website request');
  });
});

test('network output redacts sensitive query, request/response headers and structured body fields', async () => {
  await withSession('cdp-redaction', async id => {
    await loadRequests(id);
    const record = await findRequest(id, '/api/ok');
    const body = await readBody(id, record);
    const serialized = JSON.stringify({ list: await records(id), body });
    assert.match(serialized, /observed-once/);
    assert.match(serialized, /REDACTED/i);
    for (const secret of [fixtureToken, fixturePassword, fixtureCookie, fixtureHeader]) {
      assert.ok(!serialized.includes(secret), 'a synthetic sensitive value leaked from the network export');
    }
  });
});

test('large response bodies are bounded and truthfully marked as truncated', async () => {
  await withSession('cdp-capacity', async id => {
    assert.equal((await execute(id, { action: 'click', target: '#large' })).data.ok, true);
    assert.equal((await execute(id, { action: 'wait', text: 'large-finished', seconds: 3 })).data.ok, true);
    const item = await readBody(id, await findRequest(id, '/api/large'));
    assert.equal(item.body.state, 'truncated');
    assert.ok(Buffer.byteLength(item.body.text ?? '') <= 64 * 1024);
    assert.match(item.body.text, /large-response-start/);
    assert.ok(!item.body.text.includes('large-response-tail'));
  });
});

test('console and uncaught exception observations are recorded without exporting object secrets', async () => {
  await withSession('cdp-console', async id => {
    assert.equal((await execute(id, { action: 'click', target: '#logs' })).data.ok, true);
    const logs = await waitFor(async () => successful(await devtools(id, 'console.list', { limit: 100 })),
      value => JSON.stringify(value).includes('fixture-console-message'), 'console event missing');
    assert.ok(!JSON.stringify(logs).includes(fixturePassword));
    const exceptions = await waitFor(async () => successful(await devtools(id, 'events.list', { method: 'Runtime.exceptionThrown', limit: 100 })),
      value => JSON.stringify(value).includes('fixture-uncaught-error'), 'uncaught exception missing');
    assert.ok(exceptions.items.some(item => item.method === 'Runtime.exceptionThrown'));
  });
});

test('Debugger pause/events/resume remain usable through the HTTP worker while page JavaScript is suspended', async () => {
  await withSession('cdp-debugger', async id => {
    successful(await devtools(id, 'Debugger.enable'));
    successful(await devtools(id, 'Debugger.pause'));
    const paused = await waitFor(async () => successful(await devtools(id, 'events.list', { method: 'Debugger.paused', limit: 100 })),
      value => value.paused && value.items.some(item => item.method === 'Debugger.paused'), 'debugger did not pause');
    assert.equal(paused.paused, true);
    successful(await devtools(id, 'Debugger.resume'));
    const value = successful(await devtools(id, 'Runtime.evaluate', { expression: '40 + 2', returnByValue: true }));
    assert.equal(value.result.result.value, 42);
  });
});

test('human takeover releases a debugger pause without hanging or replacing the live page', { timeout: 12_000 }, async () => {
  await withSession('cdp-paused-takeover', async id => {
    const original = (await request(`/sessions/${id}/state`)).data;
    successful(await devtools(id, 'Debugger.enable'));
    successful(await devtools(id, 'Debugger.pause'));
    await waitFor(async () => (await request(`/sessions/${id}/state`)).data,
      state => state.debugger_paused === true, 'debugger state did not report its pause');
    const owner = await request(`/sessions/${id}/control`, 'POST', { mode: 'human' });
    assert.equal(owner.status, 200, JSON.stringify(owner.data));
    assert.ok(owner.data.lease_token);
    assert.equal(owner.data.debugger_paused, false);
    assert.equal(owner.data.url, original.url);
    assert.equal(owner.data.active_tab, original.active_tab);
    assert.equal(owner.data.generation, original.generation);
    assert.ok(owner.data.control_epoch > original.control_epoch);
    const input = await request(`/sessions/${id}/input`, 'POST', { type: 'key', key: 'Tab', lease_token: owner.data.lease_token });
    assert.equal(input.status, 200, JSON.stringify(input.data));
    assert.equal((await request(`/sessions/${id}/control`, 'POST', { mode: 'agent', lease_token: owner.data.lease_token })).status, 200);
    const resumed = successful(await devtools(id, 'Runtime.evaluate', { expression: 'document.title', returnByValue: true }));
    assert.equal(resumed.result.result.value, 'DevTools fixture');
  });
});

test('a real click breakpoint returns pending promptly, exposes local state and resumes the dispatched click exactly once', { timeout: 15_000 }, async () => {
  await withSession('cdp-click-breakpoint', async id => {
    const installed = await devtools(id, 'Runtime.evaluate', { returnByValue: true, expression: `
      window.fixtureStarted = 0; window.fixtureFinished = 0;
      document.querySelector('#requests').onclick = function breakpointHandler() {
        const localFixture = { label: 'breakpoint-local', amount: 42 };
        window.fixtureStarted++;
        debugger;
        window.fixtureFinished++;
        document.querySelector('#status').textContent = 'breakpoint-finished';
        return localFixture.amount;
      };
      true;
    ` });
    assert.equal(installed.data.ok, true, JSON.stringify(installed.data));
    successful(await devtools(id, 'Debugger.enable'));
    const startedAt = performance.now();
    const click = await execute(id, { action: 'click', target: '#requests' });
    assert.equal(click.status, 200);
    assert.equal(click.data.ok, false);
    assert.match(click.data.error, /debugger.*pending|pending.*debugger/i);
    assert.ok(performance.now() - startedAt < 2000, 'a breakpoint must return before the 3-second action timeout');
    const paused = await waitFor(async () => successful(await devtools(id, 'events.list', { method: 'Debugger.paused', limit: 100 })),
      value => value.items.some(item => item.params.callFrames?.some(frame => frame.functionName === 'breakpointHandler')),
      'real click breakpoint did not expose its call frame');
    const frame = paused.items.flatMap(item => item.params.callFrames ?? []).find(frame => frame.functionName === 'breakpointHandler');
    const local = successful(await devtools(id, 'Debugger.evaluateOnCallFrame', {
      callFrameId: frame.callFrameId,
      expression: '({label:localFixture.label,amount:localFixture.amount,started:window.fixtureStarted,finished:window.fixtureFinished})', returnByValue: true,
    }));
    assert.deepEqual(local.result.result.value, { label: 'breakpoint-local', amount: 42, started: 1, finished: 0 });
    const scope = frame.scopeChain.find(item => item.type === 'local');
    assert.ok(scope?.object.objectId, 'paused frame has a standard local-scope object');
    const properties = successful(await devtools(id, 'Runtime.getProperties', { objectId: scope.object.objectId, ownProperties: true }));
    assert.ok(properties.result.result.some(item => item.name === 'localFixture'));
    successful(await devtools(id, 'Debugger.resume'));
    await waitFor(async () => (await request(`/sessions/${id}/state`)).data,
      state => !state.busy && !state.debugger_paused, 'the original click did not finish after resume');
    const snapshot = await execute(id, { action: 'snapshot' });
    assert.equal(snapshot.data.ok, true, JSON.stringify(snapshot.data));
    assert.match(snapshot.data.text, /breakpoint-finished/);
    const counts = successful(await devtools(id, 'Runtime.evaluate', {
      expression: '({started:window.fixtureStarted,finished:window.fixtureFinished})', returnByValue: true,
    }));
    assert.deepEqual(counts.result.result.value, { started: 1, finished: 1 });
  });
});

test('Runtime.evaluate can stop at debugger and paused time does not consume its execution deadline', { timeout: 15_000 }, async () => {
  await withSession('cdp-eval-breakpoint', async id => {
    successful(await devtools(id, 'Debugger.enable'));
    const operation = { op_id: 'native-eval-breakpoint-once', action: 'devtools', method: 'Runtime.evaluate', params: { returnByValue: true, awaitPromise: true, expression: `(() => {
      const nativeLocal = { answer: 42 };
      window.nativeEvalStarted = (window.nativeEvalStarted || 0) + 1;
      debugger;
      window.nativeEvalFinished = (window.nativeEvalFinished || 0) + 1;
      document.querySelector('#status').textContent = 'native-eval-finished';
      return nativeLocal.answer;
    })()` } };
    const startedAt = performance.now();
    const evaluation = await request(`/sessions/${id}/execute`, 'POST', operation);
    assert.equal(evaluation.data.ok, false, JSON.stringify(evaluation.data));
    assert.match(evaluation.data.error, /debugger.*pending|pending.*debugger/i);
    assert.ok(performance.now() - startedAt < 2000, 'a paused eval must not wait for the execution deadline');
    const paused = await waitFor(async () => successful(await devtools(id, 'events.list', { method: 'Debugger.paused', limit: 100 })),
      value => value.items.some(item => item.params.callFrames?.length), 'native eval breakpoint missing');
    const frame = paused.items.flatMap(item => item.params.callFrames ?? [])[0];
    assert.ok(frame.callFrameId);
    // Longer than this worker's 3-second active-action budget. Only the paused
    // interval is excluded; the existing unknown-outcome test retains timeout.
    await delay(3250);
    const state = (await request(`/sessions/${id}/state`)).data;
    assert.equal(state.debugger_paused, true);
    assert.equal(state.devtools_blocked, false, 'a deliberate breakpoint is not an unknown execution timeout');
    const inspected = successful(await devtools(id, 'Debugger.evaluateOnCallFrame', {
      callFrameId: frame.callFrameId, expression: 'nativeLocal.answer', returnByValue: true,
    }));
    assert.equal(inspected.result.result.value, 42);
    successful(await devtools(id, 'Debugger.resume'));
    await waitFor(async () => (await request(`/sessions/${id}/state`)).data,
      current => !current.busy && !current.debugger_paused, 'native eval did not complete after resume');
    const snapshot = await execute(id, { action: 'snapshot' });
    assert.equal(snapshot.data.ok, true, JSON.stringify(snapshot.data));
    assert.match(snapshot.data.text, /native-eval-finished/);
    const counts = successful(await devtools(id, 'Runtime.evaluate', {
      expression: '({started:window.nativeEvalStarted,finished:window.nativeEvalFinished})', returnByValue: true,
    }));
    assert.deepEqual(counts.result.result.value, { started: 1, finished: 1 });
    assert.equal((await request(`/sessions/${id}/execute`, 'POST', operation)).status, 409, 'the pending operation ID cannot replay a completed eval');
  });
});

test('records follow the selected tab and a different browser session cannot read their IDs', async () => {
  await withSession('cdp-tabs', async id => {
    await loadRequests(id);
    const original = (await request(`/sessions/${id}/state`)).data.active_tab;
    const recorded = await findRequest(id, '/api/ok');
    assert.equal((await execute(id, { action: 'tab', tab_action: 'new', url: `${origin}/fixture?case=cdp-second-tab` })).data.ok, true);
    const switched = successful(await devtools(id, 'network.list'));
    assert.notEqual(switched.provenance.tab_id, original);
    assert.ok(!switched.items.some(item => item.request_id === recorded.request_id));
    unavailableRequest(await devtools(id, 'network.read', { request_id: recorded.request_id }));
    assert.equal((await execute(id, { action: 'tab', target: original })).data.ok, true);
    assert.match((await readBody(id, recorded)).body.text, /observed-once/);
    await withSession('cdp-isolated', async isolated => {
      unavailableRequest(await devtools(isolated, 'network.read', { request_id: recorded.request_id }));
      assert.ok(!(await records(isolated)).some(item => item.request_id === recorded.request_id));
    });
  });
});

test('browser-wide, target-routing, host file and credential-management CDP methods are refused', async () => {
  await withSession('cdp-refusals', async id => {
    const denied = [
      ['Browser.close', {}], ['Target.getTargets', {}], ['Target.createTarget', { url: 'about:blank' }],
      ['Storage.getCookies', {}], ['Network.getAllCookies', {}], ['Network.getCookies', {}],
      ['Fetch.enable', {}], ['IO.read', { handle: 'not-a-host-handle' }], ['Tracing.start', {}],
      ['DOM.setFileInputFiles', { files: ['/fixture-not-a-real-file'] }],
      ['Page.setDownloadBehavior', { behavior: 'allow', downloadPath: '/fixture-not-a-real-path' }],
      ['Runtime.evaluate', { expression: '1', targetId: 'another-target' }],
      ['Runtime.evaluate', { expression: '1', sessionId: 'another-protocol-session' }],
      ['Runtime.evaluate', { expression: '1', browserContextId: 'another-context' }],
    ];
    for (const [method, params] of denied) refused(await devtools(id, method, params));
    assert.equal(successful(await devtools(id, 'Runtime.evaluate', { expression: 'document.title', returnByValue: true })).result.result.value, 'DevTools fixture');
  });
});

test('human ownership fences agent CDP and cancellation makes earlier observation IDs unusable', async () => {
  await withSession('cdp-control', async id => {
    await loadRequests(id);
    const earlier = await findRequest(id, '/api/ok');
    const takeover = await request(`/sessions/${id}/control`, 'POST', { mode: 'human' });
    assert.equal(takeover.status, 200, JSON.stringify(takeover.data));
    assert.equal((await devtools(id, 'Runtime.evaluate', { expression: 'document.title' })).status, 409);
    assert.equal((await request(`/sessions/${id}/control`, 'POST', { mode: 'agent', lease_token: takeover.data.lease_token })).status, 200);
    unavailableRequest(await devtools(id, 'network.read', { request_id: earlier.request_id }));
    await loadRequests(id);
    const beforeCancel = await findRequest(id, '/api/ok');
    const cancelled = await request(`/sessions/${id}/cancel`, 'POST');
    assert.equal(cancelled.status, 200, JSON.stringify(cancelled.data));
    unavailableRequest(await devtools(id, 'network.read', { request_id: beforeCancel.request_id }));
    const current = successful(await devtools(id, 'network.list'));
    assert.ok(!current.items.some(item => item.request_id === beforeCancel.request_id));
    const navigation = await execute(id, { action: 'navigate', url: `${origin}/fixture?case=${id}-after-cancel` });
    assert.equal(navigation.data.ok, true, JSON.stringify(navigation.data));
    await loadRequests(id);
    const afterCancel = await findRequest(id, '/api/ok');
    assert.notEqual(afterCancel.request_id, beforeCancel.request_id);
    assert.match((await readBody(id, afterCancel)).body.text, /observed-once/);
  });
});

test('two sessions following the same initial sequence receive distinct observation IDs', async () => {
  await withSession('cdp-id-left', async left => {
    await withSession('cdp-id-right', async right => {
      const leftRecord = await findRequest(left, '/fixture');
      const rightRecord = await findRequest(right, '/fixture');
      assert.notEqual(leftRecord.request_id, rightRecord.request_id, 'a page/attachment counter alone is not a session identity');
      unavailableRequest(await devtools(right, 'network.read', { request_id: leftRecord.request_id }));
      unavailableRequest(await devtools(left, 'network.read', { request_id: rightRecord.request_id }));
    });
  });
});

test('an uncertain script timeout blocks further agent actions until explicit cancellation, then recovers', async () => {
  await withSession('cdp-unknown-outcome', async id => {
    const before = requestCount(id);
    const timeout = await devtools(id, 'Runtime.evaluate', {
      expression: 'window.fixtureUnknownResult = 1; new Promise(() => {})', awaitPromise: true, returnByValue: true, timeout: 100,
    });
    assert.equal(timeout.data.ok, false, JSON.stringify(timeout.data));
    assert.equal(timeout.data.data?.state, 'result_unknown_cancel_required');
    assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, true);
    refused(await execute(id, { action: 'click', target: '#requests' }));
    refused(await devtools(id, 'Runtime.evaluate', { expression: '21 * 2', returnByValue: true }));
    assert.equal(requestCount(id), before, 'blocked calls must not send a website request');
    assert.equal((await request(`/sessions/${id}/cancel`, 'POST')).status, 200);
    assert.equal((await execute(id, { action: 'navigate', url: `${origin}/fixture?case=${id}-recovered` })).data.ok, true);
    const recovered = successful(await devtools(id, 'Runtime.evaluate', { expression: '21 * 2', returnByValue: true }));
    assert.equal(recovered.result.result.value, 42);
    assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, false);
  });
});
