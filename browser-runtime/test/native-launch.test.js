import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { createWorker } from '../src/server.js';

// Exercise the production owned-browser launch. Injecting a preconfigured
// Playwright browser here would hide regressions in the actual service startup.
test('default Wayland worker exposes a cross-site iframe and retained network body without replay', { timeout: 60000 }, async () => {
  const token = 'native-launch-local-fixture', sessionID = 'native-launch-' + randomUUID();
  let worker, childOrigin, rootOrigin, businessReads = 0;
  const site = http.createServer((request, response) => {
    response.setHeader('Cache-Control', 'no-store');
    if (request.url === '/data') {
      businessReads++;
      response.setHeader('Content-Type', 'application/json');
      response.end(JSON.stringify({ source: 'cross-site-frame', inventory: 47 }));
    } else if (request.url === '/child') {
      response.setHeader('Content-Type', 'text/html');
      response.end('<title>Inventory frame</title><button id="load">Load inventory</button><output></output><script>document.querySelector("button").onclick=async()=>{const data=await fetch("/data").then(response=>response.json());document.querySelector("output").textContent=JSON.stringify(data)}</script>');
    } else if (request.url === '/') {
      response.setHeader('Content-Type', 'text/html');
      response.end(`<title>Native owner</title><iframe src="${childOrigin}/child"></iframe>`);
    } else { response.writeHead(404); response.end('not found'); }
  });
  // An unspecified bind supports localhost resolving to either IPv4 or IPv6.
  await new Promise(resolve => site.listen(0, resolve));
  rootOrigin = `http://127.0.0.1:${site.address().port}`;
  childOrigin = `http://localhost:${site.address().port}`;
  try {
    worker = await createWorker({ token, settleMs: 100, actionTimeout: 3000, idleMs: 60000 });
    const workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/${sessionID}`;
    const request = async (suffix, method = 'GET', body) => {
      const response = await fetch(workerURL + suffix, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
      return { status: response.status, body: await response.json() };
    };
    const execute = async args => {
      const response = await request('/execute', 'POST', { ...args, op_id: randomUUID() });
      assert.equal(response.status, 200, JSON.stringify(response.body));
      assert.equal(response.body.ok, true, JSON.stringify(response.body));
      return response.body;
    };
    const devtools = (method, params = {}, target) => execute({ action: 'devtools', method, params, ...(target ? { target } : {}) });
    const until = async read => {
      const deadline = Date.now() + 5000;
      while (Date.now() < deadline) {
        const value = await read();
        if (value) return value;
        await new Promise(resolve => setTimeout(resolve, 25));
      }
      assert.fail('expected production browser evidence was not observed');
    };
    assert.equal(worker.runtime?.constructor.name, 'WaylandRuntime', 'the exported worker default owns the production runtime');
    assert.equal((await request('', 'PUT', {})).status, 200);
    const host = worker.runtime.hosts.get(sessionID);
    assert.equal(host.displayMode, 'wayland');
    assert.ok(host.chromeArgs.includes('--disable-extensions'));
    assert.equal(host.chromeArgs.some(arg => arg.startsWith('--load-extension')), false);
    assert.equal([...host.records.values()].length, 1, 'one visible Sheet has one native window binding');
    const initialHistory = await [...host.records.values()][0].client.send('Page.getNavigationHistory');
    assert.equal(initialHistory.currentIndex, 0);
    assert.deepEqual(initialHistory.entries.map(entry => entry.url), ['about:blank'], 'private bootstrap must never enter visible Sheet back history');
    assert.equal((await request('/state')).body.can_go_back, false);
    await execute({ action: 'navigate', url: rootOrigin + '/' });
    const frame = await until(async () => (await devtools('targets.list')).data.items.find(item => item.type === 'iframe' && item.ready && item.url === childOrigin + '/child'));
    assert.equal(frame.is_root, false);
    const inspected = await devtools('Runtime.evaluate', { expression: 'document.querySelector("button").textContent', returnByValue: true }, frame.target_ref);
    assert.equal(inspected.data.result.result.value, 'Load inventory');
    assert.equal(businessReads, 0);
    // Start after attachment: this does not claim coverage of pre-attach traffic.
    await devtools('Runtime.evaluate', { expression: 'document.querySelector("button").click(); true', returnByValue: true }, frame.target_ref);
    const recorded = await until(async () => (await devtools('network.list', { limit: 20 }, frame.target_ref)).data.items.find(item => item.url === childOrigin + '/data' && item.status === 200));
    const read = await until(async () => {
      const result = await devtools('network.read', { request_id: recorded.request_id }, frame.target_ref);
      return result.data.request.body.state === 'available' ? result : null;
    });
    assert.equal(read.data.provenance.target_type, 'iframe');
    assert.equal(read.data.provenance.target_ref, frame.target_ref);
    assert.deepEqual(JSON.parse(read.data.request.body.text), { source: 'cross-site-frame', inventory: 47 });
    const repeated = await devtools('network.read', { request_id: recorded.request_id }, frame.target_ref);
    assert.equal(repeated.data.request.body.text, read.data.request.body.text);
    assert.equal(businessReads, 1, 'retained evidence reads must not issue additional business requests');
    const removed = await request('', 'DELETE');
    assert.equal(removed.status, 200); assert.equal(removed.body.ok, true);
    assert.equal((await request('/state')).status, 404, 'worker no longer retains the deleted session');
    assert.equal((await request('/execute', 'POST', { action: 'devtools', method: 'Runtime.evaluate', target: frame.target_ref, params: { expression: '1' }, op_id: randomUUID() })).status, 404);
    assert.equal(businessReads, 1);
  } finally {
    await worker?.close();
    site.closeAllConnections();
    await new Promise(resolve => site.close(resolve));
  }
});
