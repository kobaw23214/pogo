import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';
import { validateAction } from '../src/session.js';

test('retired actions fail validation, including before any batch step executes', () => {
  for (const action of ['eval', 'auth_status', 'api_read']) {
    const retired = { action, expression: '1', path: '/data' };
    assert.throws(() => validateAction(retired), /retired/);
    assert.throws(() => validateAction({ action: 'batch', actions: [{ action: 'click', target: '#load' }, retired] }), /retired/);
  }
});

test('website login and captured responses stay in Chromium; retired helpers send no requests', async t => {
  let dataReads = 0, authenticatedReads = 0;
  const site = http.createServer((req, res) => {
    if (req.url === '/data') {
      dataReads++;
      if (req.headers.cookie?.includes('native_session=synthetic-login')) authenticatedReads++;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ record: 'native-only-observation' }));
      return;
    }
    res.setHeader('Content-Type', 'text/html');
    res.setHeader('Set-Cookie', 'native_session=synthetic-login; HttpOnly; SameSite=Lax; Path=/');
    res.end('<button id="load" onclick="fetch(\'/data\').then(r=>r.json()).then(d=>document.querySelector(\'#result\').textContent=d.record)">Load</button><p id="result">Ready</p>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => { site.closeAllConnections(); site.close(); });
  const worker = await createWorker({ token: 'native-only-test-token' });
  await new Promise(resolve => worker.server.listen(0, '127.0.0.1', resolve));
  t.after(() => worker.close());
  const base = `http://127.0.0.1:${worker.server.address().port}`;
  let seq = 0;
  async function request(path, method = 'GET', body) {
    const response = await fetch(base + path, { method, headers: { Authorization: 'Bearer native-only-test-token', 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    return { status: response.status, data: await response.json() };
  }
  const execute = args => request('/sessions/native-only/execute', 'POST', { op_id: `native-only-${++seq}`, ...args });
  const good = response => { assert.equal(response.status, 200); assert.equal(response.data.ok, true, JSON.stringify(response.data)); return response.data; };
  good(await request('/sessions/native-only', 'PUT'));
  good(await execute({ action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` }));
  const initialState = (await request('/sessions/native-only/state')).data;
  assert.equal(initialState.backend, 'native');
  assert.equal('api_access' in initialState, false);
  for (const action of ['api_read', 'auth_status', 'eval']) {
    const retired = { action, path: '/data', expression: 'fetch("/data")' };
    const result = await execute(retired);
    assert.equal(result.data.ok, false); assert.match(result.data.error, /retired/);
    const batch = await execute({ action: 'batch', actions: [{ action: 'click', target: '#load' }, retired] });
    assert.equal(batch.data.ok, false); assert.match(batch.data.error, /retired/);
  }
  assert.equal((await request('/sessions/native-only/auth', 'POST', { action: 'grant' })).status, 410);
  assert.equal(dataReads, 0, 'retired routes must not contact the website or execute earlier batch actions');
  good(await execute({ action: 'click', target: '#load' }));
  good(await execute({ action: 'wait', text: 'native-only-observation', seconds: 3 }));
  assert.equal(dataReads, 1); assert.equal(authenticatedReads, 1, 'Chromium attaches its own HttpOnly session cookie');
  const list = good(await execute({ action: 'devtools', method: 'network.list', params: { limit: 100 } }));
  const observed = list.data.items.find(item => item.url.endsWith('/data'));
  assert.ok(observed, JSON.stringify(list));
  const body = good(await execute({ action: 'devtools', method: 'network.read', params: { request_id: observed.request_id } }));
  assert.match(JSON.stringify(body), /native-only-observation/);
  assert.equal(dataReads, 1, 'reading captured evidence must never replay an HTTP request');
  await request('/sessions/native-only', 'DELETE');
});
