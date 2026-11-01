// Run through stdin in the built image; no source or business volume mounts.
import http from 'node:http';
import assert from 'node:assert/strict';
import { createWorker } from './src/server.js';
const token = 'temporary-container-smoke';
const fixture = http.createServer((request, response) => {
  response.setHeader('X-Frame-Options', 'DENY');
  response.setHeader('Content-Security-Policy', "frame-ancestors 'none'");
  response.end('<title>Native container fixture</title><button>Hello native browser</button>');
});
await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve));
let worker;
try {
  assert.notEqual(process.getuid(), 0, 'container must run unprivileged');
  worker = await createWorker({ token }); // Chromium sandbox stays enabled.
  const address = await worker.listen(0);
  const base = `http://127.0.0.1:${address.port}/sessions/smoke`;
  const request = async (path, method, body) => {
    const response = await fetch(base + path, {
      method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
    });
    assert.equal(response.status, 200);
    return response;
  };
  await request('', 'PUT');
  const url = `http://127.0.0.1:${fixture.address().port}/`;
  const result = await (await request('/execute', 'POST', { op_id: 'one', action: 'navigate', url })).json();
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(result.url, url);
  assert.match(result.text, /Hello native/);
  const frame = await request('/frame', 'GET');
  assert.equal(frame.headers.get('content-type'), 'image/jpeg');
  assert.ok(frame.headers.get('x-browser-version'));
  const bytes = Buffer.from(await frame.arrayBuffer());
  assert.equal(bytes.readUInt16BE(), 0xffd8);
  console.log(JSON.stringify({ ok: true, uid: process.getuid(), arch: process.arch, sandbox: true, jpegBytes: bytes.length }));
} finally {
  if (worker) await worker.close();
  fixture.closeAllConnections();
  await new Promise(resolve => fixture.close(resolve));
}
