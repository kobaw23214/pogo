import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer, request as httpRequest } from 'node:http';
import { chromium } from 'playwright';
import { createWorker } from '../../src/server.js';
import { createFixtures, TASKS } from './fixtures.js';
import { reference } from './reference.js';
import { judgeCase } from './judge.js';

test('explicit fixture listeners advertise two candidate origins without changing goals or exposing controller state', async () => {
  // Reserve an available fixed port; advertised Docker aliases need the same
  // port inside the network and must not depend on a host-assigned random port.
  const reservation = createServer();
  await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const advertisedOrigins = [`http://fixture-a.test:${port}`, `http://fixture-b.test:${port}`];
  const original = await createFixtures({ seed: 'candidate-listener' });
  const fixtures = await createFixtures({ seed: 'candidate-listener', bindHost: '0.0.0.0', bindPort: port, advertisedOrigins });
  const read = (url, options = {}) => {
    const expected = new URL(url);
    return new Promise((resolve, reject) => {
      const request = httpRequest({ host: '127.0.0.1', port, path: expected.pathname, headers: { Host: expected.host, ...options.headers } }, response => {
        const chunks = [];
        response.on('data', chunk => chunks.push(chunk));
        response.once('error', reject);
        response.once('end', () => resolve({ status: response.statusCode, text: async () => Buffer.concat(chunks).toString() }));
      });
      request.once('error', reject); request.end();
    });
  };
  try {
    assert.deepEqual(fixtures.listener, { host: '0.0.0.0', port });
    assert.equal(original.listener.host, '127.0.0.1');
    for (const task of fixtures.tasks) {
      const baseline = original.tasks.find(item => item.id === task.id);
      assert.equal(task.goal, baseline.goal);
      assert.equal(task.layout_variant, baseline.layout_variant);
      assert.deepEqual(fixtures.oracle(task.id).expected, original.oracle(task.id).expected);
      assert.equal(new URL(task.startURL).origin, advertisedOrigins[0]);
      assert.deepEqual(task.allowedOrigins, task.id === 'iframe-data' ? advertisedOrigins : [advertisedOrigins[0]]);
      assert.equal((await read(task.startURL)).status, 200);
      for (const origin of advertisedOrigins) for (const suffix of ['oracle', 'expected', 'controller', 'judge', 'inspect']) {
        assert.equal((await read(`${origin}${new URL(task.startURL).pathname}${suffix}`)).status, 404);
      }
      assert.ok(!JSON.stringify(task).includes('prepared_pause_anchor'));
    }
    const iframe = fixtures.tasks.find(item => item.id === 'iframe-data');
    const oracle = fixtures.oracle(iframe.id);
    const page = await (await read(iframe.startURL)).text();
    assert.ok(page.includes(oracle.requirements.target.url));
    assert.equal(new URL(oracle.requirements.target.url).origin, advertisedOrigins[1]);
    assert.equal((await read(oracle.requirements.target.url)).status, 200);
    const data = oracle.requirements.network[0];
    const before = performance.now();
    assert.equal(await (await read(data.url)).text(), data.body);
    const state = fixtures.inspect(iframe.id);
    const recorded = state.requests.find(item => item.kind === 'data');
    assert.equal(recorded.url, data.url, 'incoming advertised Host is retained in actual request evidence');
    assert.ok(recorded.received_monotonic_ms >= before && recorded.received_monotonic_ms <= performance.now());
    const unknown = await read(iframe.startURL, { headers: { Host: `unexpected.test:${port}` } });
    assert.equal(unknown.status, 421);
    assert.deepEqual(fixtures.inspect(iframe.id), state, 'unknown hosts cannot create misleading fixture evidence');
  } finally { await fixtures.close(); await original.close(); }
});

test('fixture listener configuration rejects invalid or ambiguous origins before binding', async () => {
  const badOptions = [
    { bindHost: '' }, { bindHost: 'http://localhost' }, { bindHost: 'localhost/path' }, { bindHost: 'bad host' },
    { bindPort: -1 }, { bindPort: 65536 }, { bindPort: 1.5 }, { bindPort: '5191' },
    { advertisedOrigins: [] }, { advertisedOrigins: ['http://fixture-a.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191', 'http://fixture-a.test:5191/'] },
    { advertisedOrigins: ['https://fixture-a.test:5191', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://user:password@fixture-a.test:5191', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191/path', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191/path/..', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191?foo', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191#foo', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://0.0.0.0:5191', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://[::]:5191', 'http://fixture-b.test:5191'] },
    { advertisedOrigins: ['http://fixture-a.test:5191', 'http://fixture-b.test:5192'] },
    { advertisedOrigins: ['http://fixture-a.test:5191', null] },
    { bindPort: 0, advertisedOrigins: ['http://fixture-a.test:5191', 'http://fixture-b.test:5191'] },
  ];
  for (const options of badOptions) await assert.rejects(createFixtures({ bindPort: 5191, ...options }), /Fixture (?:bindHost|bindPort|advertisedOrigins)/);
});

test('an occupied explicit fixture listener fails instead of leaving the controller pending', async () => {
  const occupied = await createFixtures();
  try { await assert.rejects(createFixtures({ bindPort: occupied.listener.port }), { code: 'EADDRINUSE' }); }
  finally { await occupied.close(); }
});

test('fixture seeds change business facts and layouts without putting oracle data in model tasks', async () => {
  const first = await createFixtures({ seed: 'seed-a' });
  const second = await createFixtures({ seed: 'seed-b' });
  try {
    assert.equal(TASKS.length, 5);
    assert.equal(first.tasks.length, 5);
    assert.notDeepEqual(first.tasks.map(task => task.layout_variant), second.tasks.map(task => task.layout_variant));
    for (const task of first.tasks) {
      const oracle = first.oracle(task.id);
      assert.notDeepEqual(oracle.expected, second.oracle(task.id).expected);
      assert.equal(task.prompt, `${task.goal}\n起始页面：${task.startURL}`);
      assert.equal(task.oracle, undefined);
      assert.equal(task.expected, undefined);
      assert.equal(task.selectors, undefined);
      assert.equal(oracle.prepared_pause_anchor, undefined, 'no anchor exists before an actual preparation');
      for (const endpoint of oracle.requirements.network) assert.ok(!task.prompt.includes(endpoint.url));
      for (const privateField of ['trace_id', 'batch', 'sample_id', 'receipt_id']) {
        if (oracle.expected[privateField]) assert.ok(!task.prompt.includes(oracle.expected[privateField]));
      }
      const inspected = first.inspect(task.id);
      inspected.submissions.push({ fake: true });
      assert.equal(first.inspect(task.id).submissions.length, 0, 'controller state is not externally mutable');
      for (const suffix of ['oracle', 'expected', 'controller', 'judge', 'inspect']) {
        const response = await fetch(`${task.startURL}${suffix}`);
        assert.equal(response.status, 404);
      }
    }
    const iframe = first.tasks.find(task => task.id === 'iframe-data');
    assert.equal(new URL(iframe.allowedOrigins[0]).hostname, '127.0.0.1');
    assert.equal(new URL(iframe.allowedOrigins[1]).hostname, 'localhost');
  } finally { await first.close(); await second.close(); }
});

test('prepared pause identity stays private and supports real scoped recovery without script-source calls', { timeout: 30000 }, async () => {
  const fixtures = await createFixtures({ seed: 'private-pause-anchor' });
  const task = fixtures.tasks.find(item => item.id === 'debugger-recovery');
  const browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
  const token = 'browser-use-v2-private-anchor-test-only';
  const worker = await createWorker({ token, browser, actionTimeout: 2500, settleMs: 25 });
  const address = await worker.listen(0), sessionId = 'private-pause-anchor';
  let sequence = 0;
  const preparationCalls = [], ledger = [];
  const direct = async (suffix, method = 'GET', body) => {
    const response = await fetch(`http://127.0.0.1:${address.port}/sessions/${sessionId}${suffix}`, {
      method, headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined,
    });
    const result = await response.json();
    assert.equal(response.status, 200, JSON.stringify(result));
    return result;
  };
  const privateExecute = async args => {
    const result = await direct('/execute', 'POST', { ...args, op_id: `anchor-${++sequence}` });
    preparationCalls.push({ args: structuredClone(args), result: structuredClone(result) });
    return result;
  };
  const execute = async args => {
    const call = ledger.length + 1, started = performance.now();
    const result = await direct('/execute', 'POST', { ...args, op_id: `anchor-${++sequence}` });
    ledger.push({ sessionId, artifactId: task.id, call_id: `${sessionId}:${call}`, call, args: structuredClone(args),
      result: structuredClone(result), http_status: 200, ok: result.ok === true, started_monotonic_ms: started });
    assert.equal(result.ok, true, JSON.stringify(result));
    return result.data;
  };
  const call = (method, params = {}, target) => execute({ action: 'devtools', method, params, ...(target ? { target } : {}) });
  try {
    await direct('', 'PUT');
    assert.equal((await privateExecute({ action: 'navigate', url: task.startURL })).ok, true);
    const preparation = await fixtures.prepare(task.id, { execute: privateExecute, direct });
    assert.deepEqual(preparation, { kind: 'controller_prepared_worker_breakpoint', paused: true });
    const oracle = fixtures.oracle(task.id), anchor = oracle.prepared_pause_anchor;
    assert.ok(anchor);
    const preparedReply = preparationCalls.filter(item => item.args.method === 'events.list').at(-1).result;
    const preparedEvent = preparedReply.data.items.find(item => item.method === 'Debugger.paused' && item.seq === anchor.pause_seq);
    assert.ok(preparedEvent, 'the anchor must come from the controller-observed original pause');
    assert.equal(anchor.generation, preparedReply.data.provenance.generation);
    assert.equal(anchor.tab_id, preparedEvent.tab_id);
    assert.equal(anchor.target_ref, preparedEvent.target_ref);
    assert.equal(anchor.target_type, preparedEvent.target_type);
    assert.deepEqual(anchor.call_frames, preparedEvent.params.callFrames.map(frame => ({ call_frame_id: frame.callFrameId,
      script_id: frame.location.scriptId, local_scope_object_ids: frame.scopeChain.filter(scope => scope.type === 'local').map(scope => scope.object.objectId) })));
    const publicContent = JSON.stringify({ task, preparation, state: fixtures.inspect(task.id) });
    for (const value of [anchor.target_ref, 'prepared_pause_anchor', ...anchor.call_frames.flatMap(frame => [frame.call_frame_id, ...frame.local_scope_object_ids])]) {
      assert.ok(!publicContent.includes(value), 'private pause identities must not leak through tasks, preparation summaries or inspect');
    }
    const changed = fixtures.oracle(task.id);
    changed.prepared_pause_anchor.generation++;
    changed.prepared_pause_anchor.call_frames[0].local_scope_object_ids.push('not-a-real-scope');
    assert.deepEqual(fixtures.oracle(task.id).prepared_pause_anchor, anchor, 'oracle callers cannot mutate the controller anchor');

    const targets = await call('targets.list');
    const target = targets.items.find(item => item.type === 'worker' && item.ready && item.paused);
    assert.ok(target);
    const paused = await call('events.list', { method: 'Debugger.paused', limit: 50 }, target.target_ref);
    const event = paused.items.filter(item => item.method === 'Debugger.paused').at(-1);
    const frame = event.params.callFrames.find(item => item.scopeChain.some(scope => scope.type === 'local'));
    const scope = frame.scopeChain.find(item => item.type === 'local');
    const properties = await call('Runtime.getProperties', { objectId: scope.object.objectId, ownProperties: true }, target.target_ref);
    const requiredLocal = Object.keys(oracle.requirements.debugger.locals)[0];
    const object = properties.result.result.find(item => item.name === requiredLocal).value;
    const values = await call('Runtime.getProperties', { objectId: object.objectId, ownProperties: true }, target.target_ref);
    const observed = Object.fromEntries(values.result.result.filter(item => item.value && Object.hasOwn(item.value, 'value')).map(item => [item.name, item.value.value]));
    await call('Debugger.resume', {}, target.target_ref);
    const requiredResponse = oracle.requirements.network[0], until = Date.now() + 6000;
    let body;
    while (!body && Date.now() < until) {
      const network = await call('network.list', { limit: 50 }, target.target_ref);
      const request = network.items.find(item => item.url === requiredResponse.url && item.method === requiredResponse.method && item.status === requiredResponse.status);
      if (request) {
        const read = await call('network.read', { request_id: request.request_id }, target.target_ref);
        if (read.request.body.state === 'available') body = JSON.parse(read.request.body.text);
      }
      if (!body) await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(body, 'the resumed action must produce an actual recorded response');
    assert.ok(ledger.every(exchange => exchange.args.method !== 'Debugger.getScriptSource' && exchange.args.params.method !== 'Debugger.scriptParsed'));
    const evidence = { task, oracle, fixtureState: fixtures.inspect(task.id), ledger, finalAnswer: JSON.stringify(observed) };
    const judgement = judgeCase(evidence);
    assert.equal(judgement.success, true, JSON.stringify(judgement));
    // Chromium currently returns an empty CallFrame.url. The controller anchor
    // covers that real case without treating the worker URL as a script URL.
    if (!frame.url) {
      const withoutAnchor = structuredClone(oracle); delete withoutAnchor.prepared_pause_anchor;
      assert.equal(judgeCase({ ...evidence, oracle: withoutAnchor }).success, false, 'old unanchored evidence must still prove its script source');
      const wrongAnchor = structuredClone(oracle); wrongAnchor.prepared_pause_anchor.generation++;
      assert.equal(judgeCase({ ...evidence, oracle: wrongAnchor }).success, false, 'a different pause generation cannot satisfy the anchor');
    }
    fixtures.reset(task.id);
    assert.equal(fixtures.oracle(task.id).prepared_pause_anchor, undefined, 'reset clears the old private pause identity');
  } finally {
    await direct('', 'DELETE').catch(() => {});
    await worker.close(); await browser.close(); await fixtures.close();
  }
});

test('unauthenticated writes fail without a saved record or exposed login credentials', async () => {
  const fixtures = await createFixtures({ seed: 'unauthenticated' });
  try {
    const task = fixtures.tasks.find(item => item.id === 'authenticated-write');
    const oracle = fixtures.oracle(task.id);
    const submission = oracle.requirements.network.find(item => item.method === 'POST');
    const response = await fetch(submission.url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(oracle.expected) });
    assert.equal(response.status, 401);
    const inspected = fixtures.inspect(task.id);
    assert.equal(inspected.submission_attempts, 1);
    assert.equal(inspected.submissions.length, 0);
    assert.equal(inspected.authenticated, false);
    assert.ok(!JSON.stringify(inspected).includes('private-login'));
    assert.ok(!JSON.stringify(inspected).includes('fixture_session='));
    const page = await (await fetch(task.startURL)).text();
    assert.match(page, /type="password"/);
    assert.ok(!page.includes('fixture-'));
    fixtures.reset(task.id);
    assert.equal(fixtures.inspect(task.id).submission_attempts, 0);
  } finally { await fixtures.close(); }
});

test('all five positive controls use recorded real Chromium evidence and write exactly once', { timeout: 45000 }, async t => {
  const fixtures = await createFixtures({ seed: 'reference-real-chromium' });
  const browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
  const token = 'browser-use-v2-fixture-test-only';
  const worker = await createWorker({ token, browser, actionTimeout: 2500, settleMs: 25 });
  const address = await worker.listen(0);
  const workerURL = `http://127.0.0.1:${address.port}`;
  let sequence = 0;
  const request = async (path, method = 'GET', body) => {
    const response = await fetch(`${workerURL}${path}`, { method, headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
    const result = await response.json();
    assert.equal(response.status, 200, JSON.stringify(result));
    return result;
  };
  try {
    for (const task of fixtures.tasks) await t.test(task.id, async () => {
      fixtures.reset(task.id);
      const sessionId = `v2-${task.id}`;
      const direct = (suffix, method, body) => request(`/sessions/${sessionId}${suffix}`, method, body);
      const privateExecute = args => direct('/execute', 'POST', { op_id: `fixture-${++sequence}`, ...args });
      await direct('', 'PUT');
      try {
        assert.equal((await privateExecute({ action: 'navigate', url: task.startURL })).ok, true);
        const preparation = await fixtures.prepare(task.id, { execute: privateExecute, direct });
        const before = fixtures.inspect(task.id);
        const ledger = [];
        const execute = async args => {
          const result = await privateExecute(args);
          ledger.push({ args: structuredClone(args), result: structuredClone(result) });
          return result;
        };
        const oracle = fixtures.oracle(task.id);
        const answer = await reference(task, oracle, execute);
        const state = fixtures.inspect(task.id);
        assert.equal(typeof answer.answer, 'string');
        for (const fact of oracle.answer_facts) assert.ok(answer.answer.includes(String(fact.value)), `answer must use observed ${fact.name}`);
        assert.ok(ledger.some(item => item.args.method === 'targets.list'));
        const targetRefs = new Set(ledger.filter(item => item.args.method === 'targets.list').flatMap(item => item.result.data.items.map(target => target.target_ref)));
        const issuedRequests = new Set();
        for (const exchange of ledger) {
          if (exchange.args.target) assert.ok(targetRefs.has(exchange.args.target), 'target reference must have been observed');
          if (exchange.args.method === 'network.list') exchange.result.data.items.forEach(item => issuedRequests.add(item.request_id));
          if (exchange.args.method === 'network.read') assert.ok(issuedRequests.has(exchange.args.params.request_id), 'request reference must have been observed');
        }
        for (const requirement of oracle.requirements.network) {
          assert.ok(state.requests.some(item => item.url === requirement.url && item.method === requirement.method && item.status === requirement.status && item.response_body === requirement.body), 'required response really came from the fixture server');
        }
        if (oracle.requirements.submission) {
          assert.equal(state.submission_attempts, 1);
          assert.equal(state.submissions.length, 1);
          assert.deepEqual(state.submissions[0], oracle.requirements.submission.fields);
        } else {
          assert.equal(state.submission_attempts, 0);
          assert.equal(state.requests.filter(item => item.kind === 'data').length, 1, 'reading evidence never replays the business request');
        }
        if (task.id === 'iframe-data') {
          assert.ok(ledger.some(item => item.args.method === 'targets.list' && item.result.data.items.some(target => target.type === 'iframe' && target.url === oracle.requirements.target.url)), 'real cross-site OOPIF is required');
        }
        if (task.id === 'worker-data') {
          assert.equal(preparation.kind, 'controller_waited_for_worker_attachment');
          assert.equal(before.requests.filter(item => item.kind === 'data').length, 0, 'controller must not start the sampling action');
        }
        if (task.id === 'debugger-recovery') {
          assert.equal(preparation.kind, 'controller_prepared_worker_breakpoint');
          assert.equal(before.submissions.length, 0);
          assert.ok(ledger.some(item => item.args.method === 'Debugger.evaluateOnCallFrame'));
          assert.equal(ledger.filter(item => item.args.method === 'Debugger.resume').length, 1);
        }
        if (task.id === 'authenticated-write') {
          assert.equal(preparation.kind, 'controller_simulated_human_login');
          assert.equal(before.authenticated, true);
          assert.equal(state.successful_logins, 1);
          assert.equal(state.login_attempts, 1);
          assert.ok(!JSON.stringify(state).includes('fixture_session='));
          assert.ok(!JSON.stringify(ledger).includes('type":"password","value":"fixture-'));
        }
      } finally { await direct('', 'DELETE'); }
    });
  } finally { await worker.close(); await browser.close(); await fixtures.close(); }
});
