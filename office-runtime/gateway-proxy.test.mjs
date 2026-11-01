// Gateway unit tests are entirely synthetic. Optional integration reads pinned
// assets from an already running isolated engine; no containers are changed.
import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import http from 'node:http';
import { createEngineProxy, officeForwardingContext, officeGatewaySignature, officeLocation, officeUpstreamHeaders } from './engine-proxy.mjs';

const secret = 'synthetic-office-gateway-test-secret-'.repeat(2);
const options = { gatewayPrefix: '/office/editor', gatewaySecret: secret };
const upstream = new URL('http://127.0.0.1:19894');
const vendorPrefix = '/9.4.0-synthetic';
const runtimePrefix = vendorPrefix + '-pogo-' + 'a'.repeat(64);
function signed(proto = 'https', host = 'pogo.example:19777') {
  const headers = { host: '127.0.0.1:8080', 'x-forwarded-host': host + '/office/editor',
    'x-forwarded-proto': proto, 'x-forwarded-prefix': '/office/editor' };
  headers['x-pogo-office-gateway'] = officeGatewaySignature(secret, proto, headers['x-forwarded-host']);
  return headers;
}

test('ordinary Lab ignores untrusted forwarding values and retains its origin', () => {
  const input = { host: '127.0.0.1:19898', 'x-forwarded-host': 'attacker.example/other', 'x-forwarded-proto': 'https',
    'x-forwarded-prefix': '/', 'x-real-ip': '203.0.113.8', forwarded: 'host=attacker.example' };
  const context = officeForwardingContext(input, options);
  assert.deepEqual(context, { host: '127.0.0.1:19898', proto: 'http', prefix: '', gateway: false });
  const out = officeUpstreamHeaders(input, context, upstream.host);
  assert.equal(out.host, upstream.host);
  assert.equal(out['x-forwarded-host'], input.host);
  assert.equal(out['x-forwarded-proto'], 'http');
  assert.equal(out['x-forwarded-prefix'], undefined);
  assert.equal(out.forwarded, undefined);
  assert.equal(out['x-real-ip'], undefined);
});

test('authenticated HTTP and HTTPS virtual paths adapt once to the pinned upstream prefix convention', () => {
  for (const proto of ['http', 'https']) {
    for (const host of ['localhost:19777', 'pogo.example', 'POGO.example', '[::1]:19777', proto === 'http' ? 'pogo.example:80' : 'pogo.example:443']) {
      const headers = signed(proto, host);
      const context = officeForwardingContext(headers, options);
      assert.deepEqual(context, { host, proto, prefix: '/office/editor', gateway: true });
      const out = officeUpstreamHeaders(headers, context, upstream.host);
      assert.equal(out['x-forwarded-host'], host);
      assert.equal(out['x-forwarded-proto'], proto);
      assert.equal(out['x-forwarded-prefix'], '/office/editor');
      assert.equal(out['x-pogo-office-gateway'], undefined);
      assert.ok(!JSON.stringify(out).includes(secret));
    }
  }
});

test('tampering with signed metadata, malformed hosts, alternate prefixes and invalid marker fail closed', () => {
  for (const altered of [
    { 'x-forwarded-host': 'attacker.example/office/editor' }, { 'x-forwarded-proto': 'http' },
    { 'x-forwarded-prefix': '/other' }, { 'x-pogo-office-gateway': 'v1' },
    { 'x-pogo-office-gateway': 'a'.repeat(43) }, { 'x-forwarded-host': ['pogo.example/office/editor'] },
  ]) assert.throws(() => officeForwardingContext({ ...signed(), ...altered }, options));
  for (const host of ['user@pogo.example', 'pogo.example/other', 'pogo.example?secret', 'pogo.example#secret',
    'pogo.example\\other', 'pogo.example\nother', 'pogo.example:bad']) {
    assert.throws(() => officeForwardingContext(signed('https', host), options));
  }
  assert.throws(() => officeForwardingContext(signed(), {}));
  assert.throws(() => officeForwardingContext(signed(), { ...options, gatewayPrefix: '/other' }));
  assert.throws(() => officeForwardingContext(signed(), { ...options, gatewaySecret: 'wrong' }));
});

test('redirects stay under the gateway and private engine addresses are rewritten', () => {
  const context = officeForwardingContext(signed(), options);
  for (const location of [vendorPrefix + '/web-apps/index.html?x=1#part',
    upstream.origin + vendorPrefix + '/web-apps/index.html?x=1#part',
    'https://pogo.example:19777/office/editor' + vendorPrefix + '/web-apps/index.html?x=1#part']) {
    const expected = (location.startsWith('/') ? '' : 'https://pogo.example:19777') + '/office/editor' + runtimePrefix + '/web-apps/index.html?x=1#part';
    assert.equal(officeLocation(location, context, upstream, vendorPrefix, runtimePrefix), expected);
  }
  assert.equal(officeLocation('/cache/files/document/output.xlsx?md5=token&expires=20', context, upstream, vendorPrefix, runtimePrefix),
    '/office/editor/cache/files/document/output.xlsx?md5=token&expires=20');
  assert.equal(officeLocation('https://external.example/intentional', context, upstream, vendorPrefix, runtimePrefix), 'https://external.example/intentional');
  const lab = officeForwardingContext({ host: '127.0.0.1:19898' });
  assert.equal(officeLocation(vendorPrefix + '/web-apps/index.html', lab, upstream, vendorPrefix, runtimePrefix), runtimePrefix + '/web-apps/index.html');
});

test('gateway configuration is explicit and cannot turn Lab into an arbitrary-path proxy', async () => {
  for (const configuration of [{ gatewayPrefix: '/' }, { gatewayPrefix: '/office/editor' },
    { gatewayPrefix: '/other', gatewaySecret: secret }]) {
    await assert.rejects(createEngineProxy(configuration), /fixed prefix and shared secret/);
  }
});

test('real pinned assets are loaded once while HTTP and WebSocket preserve trusted virtual path',
  { skip: !process.env.OFFICE_PROXY_TEST_UPSTREAM }, async () => {
    const originalUpstream = new URL(process.env.OFFICE_PROXY_TEST_UPSTREAM);
    const manifest = JSON.parse(await fs.readFile(new URL('./engine-manifest.json', import.meta.url), 'utf8'));
    const originalAssets = new Map();
    for (const entry of [...manifest.assets, ...manifest.loaderAssets]) {
      const pathname = entry.transform ? '/' + manifest.vendorVersion + entry.path : entry.path;
      const response = await fetch(new URL(pathname, originalUpstream), { redirect: 'error' });
      assert.ok(response.ok, 'Original pinned asset unavailable');
      originalAssets.set(pathname, Buffer.from(await response.arrayBuffer()));
    }
    let assetReads = 0, lastRequest, lastUpgrade;
    const sockets = new Set();
    const fixture = http.createServer((request, response) => {
      if (originalAssets.has(request.url)) { assetReads++; response.end(originalAssets.get(request.url)); return; }
      lastRequest = { url: request.url, headers: request.headers };
      if (request.url === '/redirect') { response.writeHead(302, { Location: '/' + manifest.vendorVersion + '/web-apps/example.html' }); response.end(); return; }
      if (request.url === '/redirect-absolute') { response.writeHead(302, { Location: `http://127.0.0.1:${fixture.address().port}/cache/files/synthetic/output.xlsx?md5=sample` }); response.end(); return; }
      response.end(JSON.stringify(lastRequest));
    });
    fixture.on('connection', (socket) => { sockets.add(socket); socket.once('close', () => sockets.delete(socket)); });
    fixture.on('upgrade', (request, socket, head) => {
      lastUpgrade = { url: request.url, headers: request.headers };
      socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n');
      if (head.length) socket.write(head);
      socket.on('data', (bytes) => socket.write(bytes));
    });
    await new Promise((resolve) => fixture.listen(0, '127.0.0.1', resolve));
    const proxy = await createEngineProxy({ ...options, port: 0, upstream: `http://127.0.0.1:${fixture.address().port}` });
    try {
      assert.equal(assetReads, 9, 'All pinned resources are verified exactly once at startup');
      const forwarded = signed('https', 'pogo.example:19777');
      const echo = await (await fetch(proxy.state.url + '/echo?key=synthetic', { headers: forwarded })).json();
      assert.equal(echo.headers['x-forwarded-host'], 'pogo.example:19777');
      assert.equal(echo.headers['x-forwarded-proto'], 'https');
      assert.equal(echo.headers['x-pogo-office-gateway'], undefined);
      const redirects = await fetch(proxy.state.url + '/redirect', { headers: forwarded, redirect: 'manual' });
      assert.equal(redirects.headers.get('location'), '/office/editor/' + proxy.state.runtimeVersion + '/web-apps/example.html');
      const absolute = await fetch(proxy.state.url + '/redirect-absolute', { headers: forwarded, redirect: 'manual' });
      assert.equal(absolute.headers.get('location'), 'https://pogo.example:19777/office/editor/cache/files/synthetic/output.xlsx?md5=sample');
      const before = lastRequest;
      assert.equal((await fetch(proxy.state.url + '/must-not-reach-upstream', { headers: { ...forwarded, 'x-forwarded-host': 'attacker.example/office/editor' } })).status, 400);
      assert.equal(lastRequest, before);
      const version = '/' + proxy.state.runtimeVersion;
      const api = await (await fetch(proxy.state.url + '/web-apps/apps/api/documents/api.js', { headers: forwarded })).text();
      assert.ok(api.includes(`const ver = '${version}';`));
      assert.ok(api.includes("path.slice(0, pos), ver, path.slice(pos)"), 'The native loader preserves its script path prefix');
      const gatewayWorker = await fetch(proxy.state.url + version + '/document_editor_service_worker.js', { headers: forwarded });
      const gatewayWorkerBytes = await gatewayWorker.text();
      const labWorkerBytes = await (await fetch(proxy.state.url + version + '/document_editor_service_worker.js')).text();
      assert.notEqual(gatewayWorkerBytes, labWorkerBytes, 'Gateway and Lab caches have separate namespaces');
      const prefixHash = crypto.createHash('sha256').update('/office/editor').digest('hex').slice(0, 16);
      assert.ok(gatewayWorkerBytes.includes(`pogo_office_static_${proxy.state.extensionSha256}_${prefixHash}_`));
      assert.equal(gatewayWorker.headers.get('service-worker-allowed'), null);
      for (const editor of ['documenteditor', 'spreadsheeteditor']) {
        const html = await (await fetch(proxy.state.url + version + `/web-apps/apps/${editor}/main/index.html`, { headers: forwarded })).text();
        assert.ok(html.includes('"../../../../"+serviceWorkerName'));
        const url = new URL('../../../../document_editor_service_worker.js', `https://pogo.example/office/editor${version}/web-apps/apps/${editor}/main/index.html`);
        assert.equal(url.pathname, '/office/editor' + version + '/document_editor_service_worker.js');
        assert.ok(html.includes('registration.scope===reg.scope'), 'Cleanup is restricted to the same worker scope');
      }
      assert.equal(assetReads, 9, 'Serving SDK/HTML/worker does not reread the original SDK');
      await new Promise((resolve, reject) => {
        const request = http.request(proxy.state.url + version + '/doc/synthetic-key/c/?EIO=4&transport=websocket',
          { headers: { ...forwarded, Connection: 'Upgrade', Upgrade: 'websocket' } });
        request.once('upgrade', (_response, socket, head) => {
          assert.equal(lastUpgrade.url, '/' + manifest.vendorVersion + '/doc/synthetic-key/c/?EIO=4&transport=websocket');
          assert.equal(lastUpgrade.headers['x-forwarded-host'], 'pogo.example:19777');
          assert.equal(lastUpgrade.headers['x-forwarded-proto'], 'https');
          assert.equal(lastUpgrade.headers['x-pogo-office-gateway'], undefined);
          const expected = Buffer.from('synthetic-websocket-traffic');
          const timeout = setTimeout(() => { socket.destroy(); reject(new Error('WebSocket tunnel timed out')); }, 3000);
          socket.once('data', (bytes) => { clearTimeout(timeout); assert.deepEqual(bytes, expected); socket.destroy(); resolve(); });
          assert.equal(head.length, 0); socket.write(expected);
        });
        request.once('error', reject); request.end();
      });
    } finally {
      await proxy.close();
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => fixture.close(resolve));
    }
  });
