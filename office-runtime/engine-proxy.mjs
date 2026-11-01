// Isolated, loopback-only SDK extension loader. It does not modify a container.
import http from 'node:http';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(await fs.readFile(path.join(root, 'engine-manifest.json'), 'utf8'));
const hash = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');

export const officeGatewayPrefix = '/office/editor';
// Opening a document can include cold SDK loads and document conversion. This
// budget applies to work in flight, not the cheap Docker liveness sample.
export const officeStartupTimeoutMs = 5 * 60 * 1000;
export const officeLivenessTimeoutMs = 2000;

function proxyTimeout(message) {
  return Object.assign(new Error(message), { code: 'ETIMEDOUT' });
}

// A sample is deliberately fast: the Go document-open coordinator retries
// transient false/unavailable samples for at least five minutes. Docker and
// background availability checks keep using this same lightweight endpoint.
export async function officeEngineHealth(upstream, { timeoutMs = officeLivenessTimeoutMs, signal, fetchImpl = fetch } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error('Office health check timed out.')), timeoutMs);
  timer.unref?.();
  const abort = () => controller.abort(signal.reason);
  if (signal?.aborted) abort();
  else signal?.addEventListener('abort', abort, { once: true });
  try {
    const result = await fetchImpl(new URL('/healthcheck', upstream), { signal: controller.signal, redirect: 'error' });
    const ready = result.ok && (await result.text()).trim() === 'true';
    // Return an explicit protocol/configuration rejection immediately. The Go
    // caller can retry 503/network warm-up failures within its own open budget.
    return { ready, status: ready ? 200 : (result.status >= 400 && result.status < 500 ? result.status : 503) };
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', abort);
  }
}

async function fetchStartupAsset(url) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error('Office SDK asset load timed out.')), officeStartupTimeoutMs);
  timer.unref?.();
  try {
    const response = await fetch(url, { headers: { 'Accept-Encoding': 'identity' }, redirect: 'error', signal: controller.signal });
    return { ok: response.ok, body: response.ok ? Buffer.from(await response.arrayBuffer()) : undefined };
  } finally { clearTimeout(timer); }
}

// This is an internal, authenticated proxy contract, not a browser-controlled
// forwarding header. The domain separator keeps it separate from Office JWTs.
export function officeGatewaySignature(secret, proto, hostWithPrefix, prefix = officeGatewayPrefix) {
  return crypto.createHmac('sha256', secret).update(`pogo-office-gateway/v1\n${proto}\n${hostWithPrefix}\n${prefix}`).digest('base64url');
}

function checkedHost(host, proto) {
  if (typeof host !== 'string' || !host || /[\s/@\\?#]/.test(host)) throw new Error('Invalid Office proxy host.');
  const address = new URL(`${proto}://${host}`);
  const defaultPort = proto === 'https' ? ':443' : ':80';
  const comparableHost = host.endsWith(defaultPort) ? host.slice(0, -defaultPort.length) : host;
  if (address.host.toLowerCase() !== comparableHost.toLowerCase() || address.pathname !== '/' || address.username || address.password) throw new Error('Invalid Office proxy host.');
  return host;
}

export function officeForwardingContext(headers, options = {}) {
  const marker = headers['x-pogo-office-gateway'];
  if (!marker) {
    // The loopback Lab and private backend exports do not use the gateway.
    // Arbitrary X-Forwarded-* values on these requests are discarded.
    return { host: checkedHost(headers.host, 'http'), proto: 'http', prefix: '', gateway: false };
  }
  const prefix = options.gatewayPrefix;
  const proto = headers['x-forwarded-proto'];
  const forwarded = headers['x-forwarded-host'];
  if (prefix !== officeGatewayPrefix || !options.gatewaySecret || headers['x-forwarded-prefix'] !== prefix ||
      !['http', 'https'].includes(proto) || typeof forwarded !== 'string' || !forwarded.endsWith(prefix) ||
      typeof marker !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(marker)) throw new Error('Invalid Office gateway metadata.');
  const host = checkedHost(forwarded.slice(0, -prefix.length), proto);
  const signature = officeGatewaySignature(options.gatewaySecret, proto, forwarded, prefix);
  if (!crypto.timingSafeEqual(Buffer.from(marker), Buffer.from(signature))) throw new Error('Untrusted Office gateway metadata.');
  return { host, proto, prefix, gateway: true };
}

export function officeUpstreamHeaders(headers, context, upstreamHost) {
  const result = Object.fromEntries(Object.entries(headers).filter(([name]) =>
    !name.startsWith('x-forwarded-') && name !== 'x-pogo-office-gateway' && name !== 'forwarded' && name !== 'x-real-ip'));
  result.host = upstreamHost;
  // Pogo's authenticated contract includes the mount in its signed host. The
  // pinned 9.4 nginx/DocService instead has a separate X-Forwarded-Prefix map:
  // sending the mount in both headers would produce /office/editor twice.
  result['x-forwarded-host'] = context.host;
  result['x-forwarded-proto'] = context.proto;
  result['x-forwarded-for'] = '127.0.0.1';
  if (context.prefix) result['x-forwarded-prefix'] = context.prefix;
  return result;
}

export function officeLocation(location, context, upstream, vendorPrefix, runtimePrefix) {
  if (typeof location !== 'string' || !location) return location;
  const externalOrigin = `${context.proto}://${context.host}`;
  let locationURL;
  try { locationURL = new URL(location, upstream); } catch { return location; }
  if (locationURL.origin !== upstream.origin && locationURL.origin !== new URL(externalOrigin).origin) return location;
  let pathname = locationURL.pathname;
  if (context.prefix && (pathname === context.prefix || pathname.startsWith(context.prefix + '/'))) pathname = pathname.slice(context.prefix.length);
  if (pathname.startsWith(vendorPrefix + '/')) pathname = runtimePrefix + pathname.slice(vendorPrefix.length);
  pathname = context.prefix + pathname;
  // Keep relative redirects relative; never expose the private engine origin.
  return (location.startsWith('/') && !location.startsWith('//') ? '' : externalOrigin) + pathname + locationURL.search + locationURL.hash;
}

export async function createEngineProxy(options = {}) {
  const upstream = new URL(options.upstream || 'http://127.0.0.1:19894');
  if (upstream.protocol !== 'http:' || !['127.0.0.1', 'localhost'].includes(upstream.hostname)) throw new Error('The isolated upstream must be a loopback HTTP service.');
  const listenHost = options.listenHost || '127.0.0.1';
  if (listenHost !== '127.0.0.1' && !(listenHost === '0.0.0.0' && options.containerListen === true)) throw new Error('Non-loopback binding requires the explicit container entry point.');
  if (options.gatewayPrefix !== undefined && (options.gatewayPrefix !== officeGatewayPrefix || !options.gatewaySecret)) throw new Error('Office gateway requires its fixed prefix and shared secret.');
  const extension = await fs.readFile(path.join(root, 'engine-extension.js'));
  const extensionSha256 = hash(extension);
  const vendorPrefix = '/' + manifest.vendorVersion;
  const runtimeVersion = manifest.vendorVersion + '-pogo-' + extensionSha256;
  const runtimePrefix = '/' + runtimeVersion;
  const assets = new Map();
  for (const entry of manifest.assets) {
    const response = await fetchStartupAsset(new URL(entry.path, upstream));
    if (!response.ok) throw new Error(`Pinned SDK asset was not available: ${entry.path}`);
    const original = response.body;
    if (hash(original) !== entry.sha256) throw new Error(`SDK hash mismatch; no extension installed: ${entry.path}`);
    const body = entry.appendExtension ? Buffer.concat([original, Buffer.from('\n;\n'), extension]) : original;
    assets.set(entry.path, { body, contentType: 'text/javascript; charset=utf-8', originalSha256: entry.sha256, servedSha256: hash(body), appended: entry.appendExtension });
  }
  function replaceOnce(text, before, after, label) {
    if (text.split(before).length !== 2) throw new Error(`Pinned loader shape mismatch: ${label}`);
    return text.replace(before, after);
  }
  for (const entry of manifest.loaderAssets) {
    const result = await fetchStartupAsset(new URL(vendorPrefix + entry.path, upstream));
    if (!result.ok) throw new Error(`Pinned loader asset was not available: ${entry.path}`);
    const original = result.body;
    if (hash(original) !== entry.sha256) throw new Error(`Loader hash mismatch; no extension installed: ${entry.path}`);
    let text = original.toString('utf8');
    if (entry.transform === 'api-version') {
      text = replaceOnce(text, `const ver = '${vendorPrefix}';`, `const ver = '${runtimePrefix}';`, entry.path);
    } else if (entry.transform === 'cache-namespace') {
      // Upstream activation removes caches sharing these prefixes. Give this
      // exact runtime its own namespace, preserving every older/user cache.
      text = replaceOnce(text, "const g_cacheNamePrefix = 'document_editor_static_';", `const g_cacheNamePrefix = 'pogo_office_static_${extensionSha256}_';`, entry.path);
      text = replaceOnce(text, "const g_fifoCachePrefix = 'document_editor_dynamic_';", `const g_fifoCachePrefix = 'pogo_office_dynamic_${extensionSha256}_';`, entry.path);
    } else if (entry.transform === 'registration-scope') {
      // Other runtime/vendor scopes can still control existing documents.
      const spaced = 'registration !== reg && registration.active &&';
      const compact = 'registration!==reg&&registration.active&&';
      const before = text.includes(spaced) ? spaced : compact;
      const after = text.includes(spaced) ? 'registration !== reg && registration.scope === reg.scope && registration.active &&' : 'registration!==reg&&registration.scope===reg.scope&&registration.active&&';
      text = replaceOnce(text, before, after, entry.path);
    } else throw new Error(`Unapproved loader transformation: ${entry.path}`);
    const body = Buffer.from(text);
    assets.set(entry.path, { body, contentType: entry.contentType, originalSha256: entry.sha256, servedSha256: hash(body), appended: false, transform: entry.transform });
  }
  const gatewayAssets = new Map(assets);
  if (options.gatewayPrefix) {
    const workerPath = '/document_editor_service_worker.js';
    const worker = assets.get(workerPath);
    const scopeHash = hash(Buffer.from(options.gatewayPrefix)).slice(0, 16);
    const body = Buffer.from(worker.body.toString('utf8')
      .replace(`pogo_office_static_${extensionSha256}_`, `pogo_office_static_${extensionSha256}_${scopeHash}_`)
      .replace(`pogo_office_dynamic_${extensionSha256}_`, `pogo_office_dynamic_${extensionSha256}_${scopeHash}_`));
    gatewayAssets.set(workerPath, { ...worker, body, servedSha256: hash(body) });
  }
  function targetFor(request) {
    const target = new URL(request.url, upstream);
    if (target.origin !== upstream.origin) throw new Error('Proxy requests must target the configured engine origin.');
    if (target.pathname.startsWith(runtimePrefix + '/')) target.pathname = vendorPrefix + target.pathname.slice(runtimePrefix.length);
    else if (target.pathname.startsWith(vendorPrefix + '-pogo-')) throw new Error('The requested runtime identity is not active on this proxy.');
    return target;
  }
  const connections = new Set();
  const server = http.createServer((request, response) => {
    if (options.healthEndpoint === true && request.url === '/pogo-office/healthz' && request.method === 'GET') {
      const canceled = new AbortController();
      const abort = () => { if (!response.writableEnded) canceled.abort(); };
      response.once('close', abort);
      void officeEngineHealth(upstream, { signal: canceled.signal })
        .then(({ ready, status }) => {
          if (response.destroyed) return;
          response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
          response.end(JSON.stringify({ ready, adapter: manifest.adapterId, engineVersion: manifest.engineVersion, extensionSha256, verifiedAssets: assets.size }));
        }).catch(() => {
          if (!response.destroyed) { response.writeHead(503, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); response.end(JSON.stringify({ ready: false })); }
        }).finally(() => response.off('close', abort));
      return;
    }
    let target, context;
    try { target = targetFor(request); context = officeForwardingContext(request.headers, options); }
    catch { response.writeHead(400, { 'Cache-Control': 'no-store' }); response.end(); return; }
    const pathname = target.pathname;
    const key = [...assets.keys()].find((candidate) => pathname === candidate || pathname === vendorPrefix + candidate);
    if (key && request.method === 'GET') {
      const asset = (context.gateway ? gatewayAssets : assets).get(key);
      response.writeHead(200, { 'Content-Type': asset.contentType, 'Content-Length': asset.body.length,
        'Cache-Control': 'no-store', 'X-Pogo-Office-Adapter': `${manifest.adapterId}/${manifest.adapterVersion}` });
      response.end(asset.body); return;
    }
    const headers = officeUpstreamHeaders(request.headers, context, upstream.host);
    const proxy = http.request(target, { method: request.method, headers }, (result) => {
      const responseHeaders = { ...result.headers };
      if (typeof responseHeaders.location === 'string') responseHeaders.location = officeLocation(responseHeaders.location, context, upstream, vendorPrefix, runtimePrefix);
      // A worker may only control its default runtime directory. Do not let an
      // upstream header broaden it to Pogo's root or another application path.
      if (pathname.endsWith('/document_editor_service_worker.js')) delete responseHeaders['service-worker-allowed'];
      result.once('error', () => response.destroy());
      response.writeHead(result.statusCode || 502, responseHeaders); result.pipe(response);
    });
    // Count inactivity during an actual request/response, never ordinary idle
    // keep-alive gaps. Slow document conversion/download gets five minutes.
    proxy.setTimeout(officeStartupTimeoutMs, () => proxy.destroy(proxyTimeout('Office upstream request timed out.')));
    proxy.on('error', (error) => {
      if (response.destroyed) return;
      if (!response.headersSent) { response.writeHead(error.code === 'ETIMEDOUT' ? 504 : 502); response.end(); }
      else response.destroy();
    });
    request.once('aborted', () => proxy.destroy());
    response.once('close', () => { if (!response.writableEnded) proxy.destroy(); });
    request.pipe(proxy);
  });
  server.requestTimeout = officeStartupTimeoutMs;
  server.headersTimeout = officeStartupTimeoutMs;
  server.on('connection', (socket) => { connections.add(socket); socket.once('close', () => connections.delete(socket)); });
  server.on('upgrade', (request, socket, head) => {
    let target, context;
    try { target = targetFor(request); context = officeForwardingContext(request.headers, options); } catch { socket.destroy(); return; }
    const proxy = http.request(target, { method: request.method,
      headers: officeUpstreamHeaders(request.headers, context, upstream.host) });
    proxy.setTimeout(officeStartupTimeoutMs, () => proxy.destroy(proxyTimeout('Office WebSocket handshake timed out.')));
    proxy.on('upgrade', (response, upstreamSocket, upstreamHead) => {
      // A document's collaboration channel is long lived. The five-minute
      // handshake deadline must not become an idle socket deadline afterward.
      upstreamSocket.setTimeout(0); socket.setTimeout(0);
      socket.write(`HTTP/1.1 ${response.statusCode} ${response.statusMessage}\r\n${Object.entries(response.headers).map(([key, value]) => `${key}: ${Array.isArray(value) ? value.join(', ') : value}`).join('\r\n')}\r\n\r\n`);
      if (head.length) upstreamSocket.write(head);
      if (upstreamHead.length) socket.write(upstreamHead);
      upstreamSocket.pipe(socket); socket.pipe(upstreamSocket);
      socket.once('close', () => upstreamSocket.destroy()); upstreamSocket.once('close', () => socket.destroy());
      socket.once('error', () => upstreamSocket.destroy()); upstreamSocket.once('error', () => socket.destroy());
    });
    proxy.on('response', () => socket.destroy());
    proxy.on('error', () => socket.destroy()); proxy.end();
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(options.port ?? 19898, listenHost, resolve); });
  const state = { adapter: `${manifest.adapterId}/${manifest.adapterVersion}`, upstream: upstream.origin,
    url: `http://127.0.0.1:${server.address().port}`, extensionSha256, vendorVersion: manifest.vendorVersion, runtimeVersion,
    assets: Object.fromEntries([...assets].map(([key, value]) => [key, { originalSha256: value.originalSha256, servedSha256: value.servedSha256, appended: value.appended, ...(value.transform ? { transform: value.transform } : {}) }])) };
  return { server, state, close: async () => { for (const socket of connections) socket.destroy(); await new Promise((resolve) => server.close(resolve)); } };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const proxy = await createEngineProxy({ upstream: process.env.OFFICE_ENGINE_UPSTREAM, port: Number(process.env.OFFICE_ENGINE_PROXY_PORT || 19898) });
  console.log(JSON.stringify(proxy.state));
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => { void proxy.close().then(() => process.exit(0)); });
}
