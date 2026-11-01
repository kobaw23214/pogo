// Reproduce a same-origin SDK cache upgrade in a new synthetic browser context.
// No user's browser, CacheStorage, service worker or documents are accessed.
import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { createLab } from '../office-lab/server.mjs';
import { createEngineProxy } from './engine-proxy.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(root, '../..');
const seed = path.resolve(process.env.OFFICE_HISTORY_SEED || path.join(repo, '.localagent/checks/onlyoffice-prototype-20261012'));
const dataRoot = path.resolve(process.env.OFFICE_CACHE_DATA || path.join(repo, `.localagent/checks/onlyoffice-cache-upgrade-${crypto.randomUUID()}`));
const upstream = new URL(process.env.OFFICE_ENGINE_UPSTREAM || 'http://127.0.0.1:19894');
const manifest = JSON.parse(await fs.readFile(path.join(root, 'engine-manifest.json'), 'utf8'));
const sha = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');
const extension = await fs.readFile(path.join(root, 'engine-extension.js'), 'utf8');
const feature = ', historyWithoutRead: true';
if (extension.split(feature).length !== 2) throw new Error('The synthetic legacy fixture must omit precisely the no-read history feature.');
// This is a historical capability fixture over the real pinned SDK, not an
// editor substitute. The current bridge must reject its otherwise-valid v1.
const legacyExtension = extension.replace(feature, '');
const legacySDK = [];
for (const entry of manifest.assets) {
  const response = await fetch(new URL(entry.path, upstream), { redirect: 'error' });
  const original = Buffer.from(await response.arrayBuffer());
  if (!response.ok || sha(original) !== entry.sha256) throw new Error('The cache fixture requires the real pinned native SDK.');
  const body = entry.appendExtension ? Buffer.concat([original, Buffer.from('\n;\n' + legacyExtension)]) : original;
  legacySDK.push({ path: entry.path, body: body.toString('utf8'), sha256: sha(body) });
}
await fs.mkdir(path.join(dataRoot, 'fixtures'), { recursive: true });
await fs.mkdir(path.join(dataRoot, 'vendor'), { recursive: true });
const fixture = JSON.parse(await fs.readFile(path.join(seed, 'fixtures/manifest.json'), 'utf8'));
if (fixture.synthetic !== true) throw new Error('This validation accepts only synthetic fixtures.');
for (const name of ['manifest.json', ...Object.values(fixture.files).map((file) => file.name)]) {
  if (path.basename(name) !== name) throw new Error('Unexpected fixture path.');
  await fs.copyFile(path.join(seed, 'fixtures', name), path.join(dataRoot, 'fixtures', name));
}
await fs.copyFile(path.join(root, 'bridge/sdk.js'), path.join(dataRoot, 'vendor/plugins.js'));
const secret = (await fs.readFile(path.join(seed, 'private/docserver-jwt'), 'utf8')).trim();
const sockets = new Set();
let legacySDKNetworkRequests = 0;
const legacyServer = http.createServer((request, response) => {
  const target = new URL(request.url, upstream);
  if (target.pathname.endsWith('/pogo-cache-probe.html')) {
    response.writeHead(200, { 'Content-Type': 'text/html', 'Cache-Control': 'no-store' });
    response.end('<!doctype html><title>Synthetic cache fixture</title>'); return;
  }
  if (manifest.assets.some((entry) => target.pathname.endsWith(entry.path))) legacySDKNetworkRequests++;
  const proxy = http.request(target, { method: request.method, headers: { ...request.headers, 'x-forwarded-host': request.headers.host, 'x-forwarded-proto': 'http' } }, (result) => {
    response.writeHead(result.statusCode || 502, result.headers); result.pipe(response);
  });
  proxy.on('error', () => { if (!response.headersSent) response.writeHead(502); response.end(); });
  request.pipe(proxy);
});
legacyServer.on('connection', (socket) => { sockets.add(socket); socket.once('close', () => sockets.delete(socket)); });
legacyServer.on('upgrade', (request, socket, head) => {
  const proxy = http.request(new URL(request.url, upstream), { method: request.method, headers: { ...request.headers, 'x-forwarded-host': request.headers.host, 'x-forwarded-proto': 'http' } });
  proxy.on('upgrade', (result, peer, peerHead) => {
    socket.write(`HTTP/1.1 ${result.statusCode} ${result.statusMessage}\r\n${Object.entries(result.headers).map(([key, value]) => `${key}: ${Array.isArray(value) ? value.join(', ') : value}`).join('\r\n')}\r\n\r\n`);
    if (head.length) peer.write(head); if (peerHead.length) socket.write(peerHead);
    peer.pipe(socket); socket.pipe(peer); socket.once('close', () => peer.destroy()); peer.once('close', () => socket.destroy());
  });
  proxy.on('error', () => socket.destroy()); proxy.end();
});
await new Promise((resolve, reject) => { legacyServer.once('error', reject); legacyServer.listen(0, '127.0.0.1', resolve); });
const enginePort = legacyServer.address().port;
const engineOrigin = `http://127.0.0.1:${enginePort}`;
const lab = await createLab({ dataRoot, port: Number(process.env.OFFICE_CACHE_PORT || 19891), engineURL: engineOrigin, secret, internalEngineOrigin: upstream.origin });
const pw = await import(process.env.OFFICE_LAB_PLAYWRIGHT || '/Users/wangy/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
const browser = await pw.chromium.launch({ executablePath: process.env.OFFICE_LAB_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const context = await browser.newContext({ viewport: { width: 1540, height: 1050 }, serviceWorkers: 'allow' });
const page = await context.newPage();
let proxy;
const report = { suite: 'real-onlyoffice-same-origin-cache-upgrade', syntheticOnly: true, completed: false, checks: [], engineOrigin };
const sourceNames = ['engine-extension.js', 'engine-manifest.json', 'engine-proxy.mjs', 'bridge/plugin.js', 'bridge/config.json', 'bridge/index.html', 'bridge/sdk.js', 'verify-cache-upgrade.mjs'];
async function sources() { return Object.fromEntries(await Promise.all(sourceNames.map(async (name) => [name, sha(await fs.readFile(path.join(root, name)))]))); }
function check(name, passed) { report.checks.push({ name, passed: !!passed }); if (!passed) throw new Error(name); }
async function api(route, body) {
  const response = await fetch(new URL(route, lab.url), { method: body ? 'POST' : 'GET', headers: { Authorization: `Bearer ${lab.adminToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}) });
  const data = await response.json(); if (!response.ok) throw new Error(typeof data.error === 'string' ? data.error : data.error?.message || 'Synthetic host operation failed.'); return data;
}
async function connected(doc) {
  const deadline = Date.now() + 45000;
  while (Date.now() < deadline) {
    const active = (await api(`/api/documents/${doc}/status`)).activeSessionId;
    if (active) { const description = await api(`/api/sessions/${active}/describe`); if (description.capabilities) return description; }
    await page.waitForTimeout(150);
  }
  throw new Error('The real editor did not report its capabilities.');
}
const legacyCache = 'document_editor_static_' + manifest.vendorVersion;
const legacyScope = engineOrigin + '/' + manifest.vendorVersion + '/';
try {
  report.sourcesBefore = await sources();
  await page.goto(legacyScope + 'pogo-cache-probe.html');
  await page.evaluate(async ({ vendor, cacheName, assets }) => {
    await navigator.serviceWorker.register('/' + vendor + '/document_editor_service_worker.js');
    await navigator.serviceWorker.ready;
    const cache = await caches.open(cacheName);
    for (const asset of assets) await cache.put('/' + vendor + asset.path, new Response(asset.body, { headers: { 'Content-Type': 'text/javascript', 'Content-Length': String(new TextEncoder().encode(asset.body).length) } }));
  }, { vendor: manifest.vendorVersion, cacheName: legacyCache, assets: legacySDK });
  await page.waitForFunction(() => !!navigator.serviceWorker.controller);
  const cachedResponse = page.waitForResponse((response) => response.url().endsWith('/sdkjs/cell/sdk-all.js') && response.fromServiceWorker(), { timeout: 15000 });
  // Explicitly request the seeded asset through the active native worker.
  const cachedProbe = await page.evaluate(async (url) => { const response = await fetch(url); return response.text(); }, legacyScope + 'sdkjs/cell/sdk-all.js');
  check('The old native worker serves the seeded legacy SDK bytes', (await cachedResponse).fromServiceWorker() && sha(cachedProbe) === legacySDK.find((asset) => asset.path === '/sdkjs/cell/sdk-all.js').sha256);
  const networkBefore = legacySDKNetworkRequests;
  await page.goto(lab.url);
  const stale = await connected('word');
  check('An actual cached adapter v1 without historyWithoutRead fails closed', stale.capabilities.historyWithoutRead === false && stale.capabilities.nativeUndo === false && stale.capabilities.engineExtension?.detected === false);
  check('The old editor loaded cached native SDK without a replacement network fetch', legacySDKNetworkRequests === networkBefore);
  await api(`/api/sessions/${stale.id}/close`, {});
  await page.goto('about:blank');
  for (const socket of sockets) socket.destroy();
  await new Promise((resolve) => legacyServer.close(resolve));
  proxy = await createEngineProxy({ upstream: upstream.origin, port: enginePort });
  report.runtime = proxy.state;
  check('The upgraded runtime keeps exactly the original browser origin', proxy.state.url === engineOrigin);
  await page.goto(lab.url);
  const current = await connected('word');
  check('The new runtime capability handshake is explicit', current.capabilities.historyWithoutRead === true && current.capabilities.nativeUndo === true && current.capabilities.engineExtension?.detected === true);
  await page.locator('[data-document="excel"]').click();
  const cell = await connected('excel');
  check('Excel loads the new no-read history capability through the same origin', cell.capabilities.historyWithoutRead === true && cell.capabilities.nativeRedo === true);
  const frame = page.frames().find((item) => item.url().includes('/apps/spreadsheeteditor/main/'));
  if (!frame) throw new Error('The real Excel frame was not created.');
  check('The real editor frame uses the extension fingerprint version', new URL(frame.url()).pathname.startsWith('/' + proxy.state.runtimeVersion + '/'));
  await frame.locator('#ce-cell-name').fill('E2'); await frame.locator('#ce-cell-name').press('Enter');
  const before = await frame.locator('#ce-cell-content').inputValue();
  await frame.locator('#ce-cell-content').fill(fixture.excel.human_marker); await frame.locator('#ce-cell-content').press('Enter');
  await page.locator('#undo-document').click(); await page.waitForFunction(() => !document.getElementById('undo-document')?.disabled);
  await frame.locator('#ce-cell-name').fill('E2'); await frame.locator('#ce-cell-name').press('Enter');
  check('First native UI undo survives the preexisting cache without an Agent read', (await frame.locator('#ce-cell-content').inputValue()) === before);
  await page.locator('#redo-document').click(); await page.waitForFunction(() => !document.getElementById('redo-document')?.disabled);
  await frame.locator('#ce-cell-name').fill('E2'); await frame.locator('#ce-cell-name').press('Enter');
  check('First native UI redo survives the preexisting cache without an Agent read', (await frame.locator('#ce-cell-content').inputValue()) === fixture.excel.human_marker);
  const storage = await frame.evaluate(async ({ oldCache, oldAsset }) => {
    await navigator.serviceWorker.ready;
    const old = await (await caches.open(oldCache)).match(oldAsset);
    return { cacheNames: await caches.keys(), registrations: (await navigator.serviceWorker.getRegistrations()).map((item) => ({ scope: item.scope, scriptURL: item.active?.scriptURL })), oldBody: old ? await old.text() : null };
  }, { oldCache: legacyCache, oldAsset: legacyScope + 'sdkjs/cell/sdk-all.js' });
  const { oldBody, ...publicStorage } = storage; report.storage = publicStorage;
  check('The legacy cache and its SDK bytes remain unchanged', storage.cacheNames.includes(legacyCache) && oldBody !== null && sha(oldBody) === legacySDK.find((asset) => asset.path === '/sdkjs/cell/sdk-all.js').sha256);
  check('The legacy worker registration is preserved', storage.registrations.some((item) => item.scope === legacyScope));
  check('The new worker owns a separate version scope', storage.registrations.some((item) => item.scope === engineOrigin + '/' + proxy.state.runtimeVersion + '/'));
  report.sourcesAfter = await sources();
  check('Runtime and bridge sources stayed fixed during the actual cache upgrade', JSON.stringify(report.sourcesBefore) === JSON.stringify(report.sourcesAfter));
  report.completed = true;
} catch (failure) {
  report.failure = failure.message; process.exitCode = 1;
  await page.screenshot({ path: path.join(dataRoot, 'failure.png'), fullPage: true }).catch(() => {});
} finally {
  await fs.writeFile(path.join(dataRoot, 'verification.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ completed: report.completed, passed: report.checks.filter((item) => item.passed).length, checks: report.checks.length, dataRoot, failure: report.failure }));
  await browser.close(); await lab.close();
  if (proxy) await proxy.close();
  if (legacyServer.listening) { for (const socket of sockets) socket.destroy(); await new Promise((resolve) => legacyServer.close(resolve)); }
}
