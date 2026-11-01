import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';

// Real Chromium, two isolated local sites, and the public HTTP worker contract.
// Site isolation is a test launch option, never a production default. An
// independent browser-level observer proves the iframe is an actual OOPIF;
// every product capability below is exercised through the HTTP worker.
const token = 'devtools-target-family-test-only';
let site, browser, worker, observer, base, origin, childOrigin, sequence = 0;
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
const execute = (id, args) => request(`/sessions/${id}/execute`, 'POST', { op_id: `family-${++sequence}`, ...args });
const devtools = (id, method, params = {}, target) => execute(id, {
  action: 'devtools', method, params, ...(target === undefined ? {} : { target }),
});
function successful(response) {
  assert.equal(response.status, 200, JSON.stringify(response.data));
  assert.equal(response.data.ok, true, JSON.stringify(response.data));
  assert.ok(response.data.data && typeof response.data.data === 'object');
  return response.data.data;
}
function refused(response, message) {
  assert.ok(response.status >= 400 || response.data.ok === false, `unexpected success: ${JSON.stringify(response.data)}`);
  if (message) assert.match(response.data.error ?? '', message);
}
async function waitFor(read, matches, description) {
  const deadline = Date.now() + 6000;
  let value;
  do {
    value = await read();
    if (matches(value)) return value;
    await delay(25);
  } while (Date.now() < deadline);
  assert.fail(`${description}: ${JSON.stringify(value)}`);
}
const targets = async id => successful(await devtools(id, 'targets.list')).items;
const rootOf = items => items.find(item => item.is_root);
const iframeOf = items => items.find(item => item.type === 'iframe');
const workerOf = items => items.find(item => item.type === 'worker' && new URL(item.url).searchParams.get('owner') === 'root');
const nestedWorkerOf = items => items.find(item => item.type === 'worker' && new URL(item.url).searchParams.get('owner') === 'iframe');
async function readyFamily(id) {
  return waitFor(() => targets(id), items => [rootOf(items), iframeOf(items), workerOf(items), nestedWorkerOf(items)].every(item => item?.ready),
    'root, real OOPIF and their dedicated workers did not become ready');
}
async function withSession(id, run) {
  assert.equal((await request(`/sessions/${id}`, 'PUT')).status, 200);
  try {
    successful(await devtools(id, 'help'));
    const navigation = await execute(id, { action: 'navigate', url: `${origin}/fixture?case=${id}` });
    assert.equal(navigation.data.ok, true, JSON.stringify(navigation.data));
    await run(id, await readyFamily(id));
  } finally {
    // Closing the session must also release a failed test's child breakpoint.
    assert.equal((await request(`/sessions/${id}`, 'DELETE')).status, 200);
  }
}
async function evaluate(id, expression, target) {
  const data = successful(await devtools(id, 'Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, target));
  assert.equal(data.result.exceptionDetails, undefined, JSON.stringify(data));
  return data.result.result.value;
}
function caseRequestCount(id) {
  return [...requests].filter(([key]) => new URL(key).searchParams.get('case') === id).reduce((sum, [, n]) => sum + n, 0);
}
async function bodyOf(id, requestID, target) {
  return waitFor(async () => successful(await devtools(id, 'network.read', { request_id: requestID }, target)).request,
    item => item.body && !['pending', 'loading'].includes(item.body.state), 'recorded response body did not settle');
}

before(async () => {
  site = http.createServer((req, res) => {
    const url = new URL(req.url, `http://${req.headers.host}`);
    requests.set(url.href, (requests.get(url.href) ?? 0) + 1);
    const caseID = url.searchParams.get('case') ?? '';
    if (url.pathname.startsWith('/api/')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ message: `observed-${url.pathname.slice(5)}`, case: caseID }));
      return;
    }
    if (url.pathname === '/worker.js') {
      res.writeHead(200, { 'Content-Type': 'application/javascript' });
      res.end(`self.fixtureWorker = true; self.fixtureStarted = 0; self.fixtureFinished = 0;
        self.onmessage = function workerMessage(event) {
          if (event.data.kind !== 'pause') return;
          const localFixture = { label: 'worker-local', answer: 42 };
          self.fixtureStarted++;
          debugger;
          self.fixtureFinished++;
          self.postMessage({ kind: 'paused-work-finished', answer: localFixture.answer });
        };`);
      return;
    }
    if (url.pathname === '/favicon.ico') { res.writeHead(204); res.end(); return; }
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    if (url.pathname === '/child') {
      res.end(`<!doctype html><title>Independent iframe target</title><h1 id="child-heading">OOPIF evidence</h1>
        <script>
          window.fixtureFrame = true; window.fixtureStarted = 0; window.fixtureFinished = 0;
          window.nestedWorker = new Worker('/worker.js?case=' + encodeURIComponent(${JSON.stringify(caseID)}) + '&owner=iframe');
          addEventListener('message', function iframeMessage(event) {
            if (event.origin !== ${JSON.stringify(origin)} || event.data.kind !== 'pause') return;
            const localFixture = { label: 'iframe-local', answer: 42 };
            window.fixtureStarted++;
            debugger;
            window.fixtureFinished++;
            event.source.postMessage({ kind: 'paused-work-finished', answer: localFixture.answer }, event.origin);
          });
        </script>`);
      return;
    }
    res.end(`<!doctype html><title>Target family fixture</title><h1 id="root-heading">Root page evidence</h1>
      <output id="status">ready</output><div id="frame-slot"></div>
      <script>
        window.fixtureRevision = 0; window.fixtureAwaited = 0;
        window.fixtureRecreateChildren = function() {
          window.fixtureWorker?.terminate();
          document.querySelector('#child')?.remove();
          window.fixtureRevision++;
          window.fixtureWorker = new Worker('/worker.js?case=' + encodeURIComponent(${JSON.stringify(caseID)}) + '&owner=root&rev=' + fixtureRevision);
          const child = document.createElement('iframe'); child.id = 'child';
          child.src = ${JSON.stringify(childOrigin)} + '/child?case=' + encodeURIComponent(${JSON.stringify(caseID)}) + '&rev=' + fixtureRevision;
          document.querySelector('#frame-slot').append(child);
        };
        window.fixtureAwaitChild = function(kind) {
          return new Promise(resolve => {
            const source = kind === 'worker' ? fixtureWorker : window;
            const listener = event => {
              if (event.data.kind !== 'paused-work-finished') return;
              source.removeEventListener('message', listener);
              window.fixtureAwaited++;
              document.querySelector('#status').textContent = kind + '-finished';
              resolve(event.data.answer);
            };
            source.addEventListener('message', listener);
            if (kind === 'worker') fixtureWorker.postMessage({ kind: 'pause' });
            else document.querySelector('#child').contentWindow.postMessage({ kind: 'pause' }, ${JSON.stringify(childOrigin)});
          });
        };
        fixtureRecreateChildren();
      </script>`);
  });
  // Unspecified bind address accepts localhost's IPv4 or IPv6 resolution.
  await new Promise(resolve => site.listen(0, resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  childOrigin = `http://localhost:${site.address().port}`;
  browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
  observer = await browser.newBrowserCDPSession();
  worker = await createWorker({ token, browser, actionTimeout: 3000, idleMs: 60_000 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});

after(async () => {
  await worker?.close();
  await observer?.detach().catch(() => {});
  await browser?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

test('public target discovery reflects a real OOPIF and nested dedicated workers with scoped opaque references', async () => {
  await withSession('family-discovery', async (id, family) => {
    const root = rootOf(family), iframe = iframeOf(family), dedicated = workerOf(family), nested = nestedWorkerOf(family);
    const actual = (await observer.send('Target.getTargets')).targetInfos.filter(item => item.url.includes(`case=${id}`));
    assert.ok(actual.some(item => item.type === 'iframe' && item.url === iframe.url), 'the fixture must be an independent iframe target, not just a cross-origin frame');
    for (const item of [root, iframe, dedicated, nested]) {
      assert.equal(typeof item.target_ref, 'string');
      assert.ok(item.target_ref.length > 10);
      assert.ok(!actual.some(info => info.targetId === item.target_ref), 'raw global CDP target IDs are not public handles');
    }
    assert.equal(new Set(family.map(item => item.target_ref)).size, family.length);
    assert.equal(root.parent_target_ref, null);
    assert.equal(iframe.parent_target_ref, root.target_ref);
    assert.equal(dedicated.parent_target_ref, root.target_ref);
    assert.equal(nested.parent_target_ref, iframe.target_ref);
    assert.equal(await evaluate(id, 'document.title'), 'Target family fixture');
    assert.deepEqual(await evaluate(id, '({title:document.title,frame:window.fixtureFrame,heading:document.querySelector("#child-heading").textContent})', iframe.target_ref),
      { title: 'Independent iframe target', frame: true, heading: 'OOPIF evidence' });
    assert.deepEqual(await evaluate(id, '({worker:self.fixtureWorker,document:typeof document,owner:new URL(self.location.href).searchParams.get("owner")})', dedicated.target_ref),
      { worker: true, document: 'undefined', owner: 'root' });
    assert.equal(await evaluate(id, 'new URL(self.location.href).searchParams.get("owner")', nested.target_ref), 'iframe');
    const document = successful(await devtools(id, 'DOM.getDocument', { depth: 1 }, iframe.target_ref));
    const node = successful(await devtools(id, 'DOM.querySelector', { nodeId: document.result.root.nodeId, selector: '#child-heading' }, iframe.target_ref));
    assert.ok(node.result.nodeId > 0, 'DOM node lookup is scoped to the OOPIF document');
  });
});

test('unsupported worker commands fail on that target instead of falling back to the root document', async () => {
  await withSession('family-no-fallback', async (id, family) => {
    const target = workerOf(family).target_ref;
    refused(await devtools(id, 'DOM.getDocument', { depth: 1 }, target));
    refused(await devtools(id, 'Runtime.fixtureMissingMethod', {}, target));
    assert.equal(await evaluate(id, 'typeof document', target), 'undefined');
    assert.equal(await evaluate(id, 'document.title'), 'Target family fixture');
    assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, false);
  });
});

test('losing an oversized worker evaluation reply fences the family until cancellation', { timeout: 15_000 }, async () => {
  await withSession('family-unknown-reply', async (id, family) => {
    const target = workerOf(family).target_ref;
    // The mutation executes before the >16 MiB transport response is rejected.
    // Detaching the debugging connection does not itself terminate the worker.
    const outcome = await devtools(id, 'Runtime.evaluate', {
      expression: 'self.fixtureStarted++; "x".repeat(17 * 1024 * 1024)', returnByValue: true,
    }, target);
    assert.equal(outcome.data.ok, false, JSON.stringify(outcome.data));
    assert.equal(outcome.data.data?.state, 'result_unknown_cancel_required', JSON.stringify(outcome.data));
    assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, true);
    refused(await devtools(id, 'Runtime.evaluate', { expression: '1', returnByValue: true }));
    const cancelled = await request(`/sessions/${id}/cancel`, 'POST');
    assert.equal(cancelled.status, 200, JSON.stringify(cancelled.data));
    assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, false);
  });
});

test('target capacity overflow resumes excess real workers so observing a busy page cannot hang its startup', { timeout: 15_000 }, async () => {
  await withSession('family-overflow', async id => {
    assert.equal(await evaluate(id, `(() => {
      window.fixtureWorkerArrivals = 0;
      const url = URL.createObjectURL(new Blob(['self.postMessage("started"); self.onmessage = () => {};'], {type:'application/javascript'}));
      window.fixtureOverflowWorkers = Array.from({length:40}, () => {
        const worker = new Worker(url);
        worker.onmessage = () => { window.fixtureWorkerArrivals++; };
        return worker;
      });
      return true;
    })()`), true);
    await waitFor(() => evaluate(id, 'fixtureWorkerArrivals'), count => count === 40,
      'workers beyond the target cap remained suspended by auto-attachment');
    const listing = successful(await devtools(id, 'targets.list'));
    assert.equal(listing.items.length, 32, 'the advertised target cap applies to the entire family');
    assert.ok(listing.items.every(item => item.ready));
    assert.ok(listing.coverage.rejected.capacity > 0, 'the coverage report must disclose skipped targets');
  });
});

test('network, console and protocol events aggregate the active family and targeted reads never replay requests', async () => {
  await withSession('family-observation', async (id, family) => {
    const chosen = [rootOf(family), iframeOf(family), workerOf(family), nestedWorkerOf(family)];
    for (const [index, target] of chosen.entries()) {
      const expression = `console.log('family-diagnostic-${index}'); fetch('/api/member-${index}?case=${id}').then(response => response.json())`;
      assert.equal((await evaluate(id, expression, target.target_ref)).message, `observed-member-${index}`);
    }
    const combined = await waitFor(async () => successful(await devtools(id, 'network.list', { limit: 100 })),
      value => chosen.every((_, index) => value.items.some(item => item.url.includes(`/api/member-${index}?`))), 'family request aggregation missing');
    const combinedConsole = successful(await devtools(id, 'console.list', { limit: 100 }));
    const combinedEvents = successful(await devtools(id, 'events.list', { method: 'Runtime.consoleAPICalled', limit: 100 }));
    const before = caseRequestCount(id);
    for (const [index, target] of chosen.entries()) {
      const record = combined.items.find(item => item.url.includes(`/api/member-${index}?`));
      assert.equal(record.target_ref, target.target_ref);
      assert.equal(record.target_type, target.type);
      const body = await bodyOf(id, record.request_id);
      assert.equal(body.target_ref, target.target_ref);
      assert.equal(body.body.state, 'available');
      assert.match(body.body.text, new RegExp(`observed-member-${index}`));
      assert.equal((await bodyOf(id, record.request_id, target.target_ref)).request_id, record.request_id);
      assert.ok(combinedConsole.items.some(item => item.target_ref === target.target_ref && JSON.stringify(item).includes(`family-diagnostic-${index}`)));
      assert.ok(combinedEvents.items.some(item => item.target_ref === target.target_ref && JSON.stringify(item).includes(`family-diagnostic-${index}`)));
      for (const method of ['network.list', 'console.list', 'events.list']) {
        const filtered = successful(await devtools(id, method, { limit: 100 }, target.target_ref));
        assert.ok(filtered.items.length > 0);
        assert.ok(filtered.items.every(item => item.target_ref === target.target_ref));
      }
      const wrong = chosen[(index + 1) % chosen.length];
      refused(await devtools(id, 'network.read', { request_id: record.request_id }, wrong.target_ref));
    }
    assert.equal(caseRequestCount(id), before, 'list/read operations must only observe requests that already occurred');
  });
});

for (const kind of ['worker', 'iframe']) {
  test(`a ${kind} breakpoint exposes local state and resumes the original waiting native eval exactly once`, { timeout: 20_000 }, async () => {
    await withSession(`family-pause-${kind}`, async (id, family) => {
      const child = kind === 'worker' ? workerOf(family) : iframeOf(family);
      successful(await devtools(id, 'Debugger.enable', {}, child.target_ref));
      const operation = { op_id: `family-wait-${kind}-once`, action: 'devtools', method: 'Runtime.evaluate', params: { expression: `fixtureAwaitChild('${kind}')`, returnByValue: true, awaitPromise: true } };
      const start = performance.now();
      const result = await request(`/sessions/${id}/execute`, 'POST', operation);
      assert.equal(result.data.ok, false, JSON.stringify(result.data));
      assert.match(result.data.error, /debugger.*pending|pending.*debugger/i);
      assert.ok(performance.now() - start < 2000, 'child debugger pause must return before the parent action deadline');
      const paused = await waitFor(async () => successful(await devtools(id, 'events.list', { method: 'Debugger.paused', limit: 100 }, child.target_ref)),
        value => value.paused && value.items.some(item => item.params.callFrames?.some(frame => frame.functionName === `${kind}Message`)), 'child breakpoint did not expose its local frame');
      const event = paused.items.find(item => item.params.callFrames?.some(frame => frame.functionName === `${kind}Message`));
      assert.equal(event.target_ref, child.target_ref);
      const frame = event.params.callFrames.find(item => item.functionName === `${kind}Message`);
      assert.equal((await request(`/sessions/${id}/state`)).data.debugger_paused, true);
      assert.ok((await targets(id)).find(item => item.target_ref === child.target_ref).paused);
      if (kind === 'worker') await delay(3250); // More than the parent action's active budget.
      assert.equal((await request(`/sessions/${id}/state`)).data.devtools_blocked, false);
      const local = successful(await devtools(id, 'Debugger.evaluateOnCallFrame', {
        callFrameId: frame.callFrameId, expression: '({label:localFixture.label,answer:localFixture.answer,started:globalThis.fixtureStarted,finished:globalThis.fixtureFinished})', returnByValue: true,
      }, child.target_ref));
      assert.deepEqual(local.result.result.value, { label: `${kind}-local`, answer: 42, started: 1, finished: 0 });
      successful(await devtools(id, 'Debugger.resume', {}, child.target_ref));
      await waitFor(async () => (await request(`/sessions/${id}/state`)).data, value => !value.busy && !value.debugger_paused,
        'parent evaluation did not finish after resuming its child');
      assert.equal(await evaluate(id, 'fixtureAwaited'), 1);
      assert.deepEqual(await evaluate(id, '({started:globalThis.fixtureStarted,finished:globalThis.fixtureFinished})', child.target_ref), { started: 1, finished: 1 });
      const snapshot = await execute(id, { action: 'snapshot' });
      assert.equal(snapshot.data.ok, true, JSON.stringify(snapshot.data));
      assert.match(snapshot.data.text, new RegExp(`${kind}-finished`));
      assert.equal((await request(`/sessions/${id}/execute`, 'POST', operation)).status, 409, 'a pending operation ID cannot replay its mutation');
    });
  });
}

test('destroyed iframe and worker targets never regain their previous references when recreated', async () => {
  await withSession('family-recreate', async (id, family) => {
    const earlier = family.filter(item => !item.is_root);
    assert.equal(await evaluate(id, 'fixtureRecreateChildren(); true'), true);
    const current = await waitFor(() => readyFamily(id), items => earlier.every(old => !items.some(item => item.target_ref === old.target_ref)),
      'destroyed target references remained active');
    assert.equal(rootOf(current).target_ref, rootOf(family).target_ref, 'the live root target retains its reference');
    for (const old of earlier) refused(await devtools(id, 'Runtime.evaluate', { expression: '1' }, old.target_ref), /DEVTOOLS_TARGET_NOT_AVAILABLE/);
    assert.equal(await evaluate(id, 'self.fixtureWorker', workerOf(current).target_ref), true);
    assert.equal(await evaluate(id, 'window.fixtureFrame', iframeOf(current).target_ref), true);
  });
});

test('target and observation references are confined to the selected tab and owning browser session', async () => {
  await withSession('family-isolation', async (id, family) => {
    const originalTab = (await request(`/sessions/${id}/state`)).data.active_tab;
    const child = workerOf(family);
    await evaluate(id, `fetch('/api/private-member?case=${id}').then(response => response.text())`, child.target_ref);
    const recorded = successful(await devtools(id, 'network.list', { limit: 100 })).items.find(item => item.url.includes('/api/private-member?'));
    assert.ok(recorded);
    const opened = await execute(id, { action: 'tab', tab_action: 'new', url: `${origin}/fixture?case=family-other-tab` });
    assert.equal(opened.data.ok, true, JSON.stringify(opened.data));
    const other = await readyFamily(id);
    assert.ok(!other.some(item => item.target_ref === child.target_ref));
    refused(await devtools(id, 'Runtime.evaluate', { expression: 'self.fixtureWorker' }, child.target_ref), /DEVTOOLS_TARGET_NOT_AVAILABLE/);
    refused(await devtools(id, 'network.read', { request_id: recorded.request_id }));
    assert.equal((await execute(id, { action: 'tab', target: originalTab })).data.ok, true);
    assert.equal(await evaluate(id, 'self.fixtureWorker', child.target_ref), true);
    assert.match((await bodyOf(id, recorded.request_id)).body.text, /observed-private-member/);
    await withSession('family-other-session', async isolated => {
      refused(await devtools(isolated, 'Runtime.evaluate', { expression: 'self.fixtureWorker' }, child.target_ref), /DEVTOOLS_TARGET_NOT_AVAILABLE/);
      refused(await devtools(isolated, 'network.read', { request_id: recorded.request_id }));
      assert.ok(!(await targets(isolated)).some(item => family.some(old => old.target_ref === item.target_ref)));
    });
  });
});

test('a background tab worker breakpoint does not interrupt the active tab evaluation', { timeout: 12_000 }, async () => {
  await withSession('family-background', async (id, family) => {
    const originalTab = (await request(`/sessions/${id}/state`)).data.active_tab;
    const child = workerOf(family);
    successful(await devtools(id, 'Debugger.enable', {}, child.target_ref));
    assert.equal(await evaluate(id, 'setTimeout(() => { debugger; }, 1500); true', child.target_ref), true);
    const opened = await execute(id, { action: 'tab', tab_action: 'new', url: `${origin}/fixture?case=family-foreground` });
    assert.equal(opened.data.ok, true, JSON.stringify(opened.data));
    const foreground = await evaluate(id, `new Promise(resolve => {
      const started = Date.now();
      setTimeout(() => resolve({started, finished:Date.now(), answer:42}), 2000);
    })`);
    assert.equal(foreground.answer, 42);
    assert.equal((await request(`/sessions/${id}/state`)).data.debugger_paused, false);
    assert.equal((await execute(id, { action: 'tab', target: originalTab })).data.ok, true);
    const paused = successful(await devtools(id, 'events.list', { method: 'Debugger.paused', limit: 100 }, child.target_ref));
    assert.equal(paused.paused, true, 'the background worker really did pause');
    assert.ok(paused.items.some(item => item.timestamp >= foreground.started && item.timestamp <= foreground.finished),
      'the background pause must overlap the foreground evaluation to exercise the isolation boundary');
    successful(await devtools(id, 'Debugger.resume', {}, child.target_ref));
  });
});

for (const control of ['human', 'cancel']) {
  test(`${control} control change releases child debugger state and invalidates old family references`, { timeout: 15_000 }, async () => {
    await withSession(`family-control-${control}`, async (id, family) => {
      const child = workerOf(family);
      successful(await devtools(id, 'Debugger.enable', {}, child.target_ref));
      const pending = await execute(id, { action: 'devtools', method: 'Runtime.evaluate', params: { expression: "fixtureWorker.postMessage({kind:'pause'}); true", returnByValue: true, awaitPromise: true } });
      assert.ok(pending.status === 200, JSON.stringify(pending.data));
      await waitFor(async () => (await request(`/sessions/${id}/state`)).data, value => value.debugger_paused,
        'worker breakpoint was not registered before control change');
      if (control === 'human') {
        const takeover = await request(`/sessions/${id}/control`, 'POST', { mode: 'human' });
        assert.equal(takeover.status, 200, JSON.stringify(takeover.data));
        assert.equal(takeover.data.debugger_paused, false);
        assert.ok(takeover.data.lease_token);
        assert.equal((await devtools(id, 'targets.list')).status, 409);
        const handback = await request(`/sessions/${id}/control`, 'POST', { mode: 'agent', lease_token: takeover.data.lease_token });
        assert.equal(handback.status, 200, JSON.stringify(handback.data));
        await readyFamily(id);
      } else {
        const cancelled = await request(`/sessions/${id}/cancel`, 'POST');
        assert.equal(cancelled.status, 200, JSON.stringify(cancelled.data));
        assert.equal(cancelled.data.debugger_paused, false);
        const navigated = await execute(id, { action: 'navigate', url: `${origin}/fixture?case=${id}-recovered` });
        assert.equal(navigated.data.ok, true, JSON.stringify(navigated.data));
        await readyFamily(id);
      }
      for (const old of family) refused(await devtools(id, 'Runtime.evaluate', { expression: '1' }, old.target_ref), /DEVTOOLS_TARGET_NOT_AVAILABLE/);
      assert.equal((await request(`/sessions/${id}/state`)).data.debugger_paused, false);
      assert.equal(await evaluate(id, 'document.title'), 'Target family fixture');
    });
  });
}
