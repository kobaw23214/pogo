import http from 'node:http';
import { pipeline } from 'node:stream/promises';
import { timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { BrowserSession, HTTPError } from './session.js';
import { attachLiveServer } from './live.js';
import { mediaConfiguration } from './media-config.js';
import { WaylandRuntime } from './wayland.js';

function json(response, status, body) {
  response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  response.end(JSON.stringify(body));
}

async function readJSON(request) {
  let size = 0;
  const chunks = [];
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 1_048_576) throw new HTTPError(413, 'request body exceeds 1 MiB');
    chunks.push(chunk);
  }
  try {
    const value = JSON.parse(Buffer.concat(chunks).toString() || '{}');
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error();
    return value;
  } catch { throw new HTTPError(400, 'request body must be a JSON object'); }
}

function sessionMetadata(body) {
  if (Object.keys(body).some(key => !['client_session_id', 'profile_key'].includes(key))) throw new HTTPError(400, 'unsupported browser session metadata');
  if (body.client_session_id === undefined && body.profile_key === undefined) return {};
  if (typeof body.client_session_id !== 'string' || !/^[A-Za-z0-9_-]{1,160}$/.test(body.client_session_id) ||
    typeof body.profile_key !== 'string' || !/^[a-f0-9]{64}$/.test(body.profile_key)) {
    throw new HTTPError(400, 'browser session metadata requires client_session_id and a SHA-256 profile_key');
  }
  return { clientSessionId: body.client_session_id, profileKey: body.profile_key };
}

export async function createWorker({ token, browser, mediaConfig = mediaConfiguration(), launchRuntime = options => new WaylandRuntime(options.mediaConfig, options).start(), sessionFactory, allowBrowserLaunch = true, isConnected, actionTimeout, settleMs, maxSessions = 32, maxPages = 0, idleMs = 30 * 60_000, sandbox = true, onBrowserDisconnect } = {}) {
  if (!token || typeof token !== 'string' || /[\r\n]/.test(token)) throw new Error('POGO_BROWSER_TOKEN is required');
  if (!Number.isInteger(maxSessions) || maxSessions < 1 || maxSessions > 256) throw new Error('POGO_BROWSER_MAX_SESSIONS must be 1–256');
  if (!Number.isInteger(maxPages) || maxPages < 0 || maxPages > 1024) throw new Error('POGO_BROWSER_MAX_PAGES must be 0 (no cap) –1024');
  if (!Number.isInteger(idleMs) || idleMs < 1000 || idleMs > 86_400_000) throw new Error('POGO_BROWSER_IDLE_MS must be 1000–86400000');
  if (settleMs !== undefined && (!Number.isInteger(settleMs) || settleMs < 0 || settleMs > 5000)) throw new Error('POGO_BROWSER_SETTLE_MS must be 0–5000');
  if (sessionFactory !== undefined && typeof sessionFactory !== 'function') throw new Error('sessionFactory must be a function');
  if (typeof allowBrowserLaunch !== 'boolean') throw new Error('allowBrowserLaunch must be a boolean');
  if (isConnected !== undefined && typeof isConnected !== 'function') throw new Error('isConnected must be a function');
  if (typeof launchRuntime !== 'function') throw new Error('launchRuntime must be a function');
  if (sessionFactory && !browser && (allowBrowserLaunch !== false || !isConnected)) throw new Error('a managed session factory without a browser requires allowBrowserLaunch:false and isConnected');
  if (!browser && !sessionFactory && !allowBrowserLaunch) throw new Error('browser launch is disabled; provide a browser or managed session factory');
  const expected = Buffer.from(`Bearer ${token}`);
  const ownRuntime = !browser && !sessionFactory;
  let runtime;
  if (ownRuntime) {
    runtime = await launchRuntime({ mediaConfig, sandbox, onFailure: onBrowserDisconnect });
    if (!['factory', 'connected', 'close'].every(method => typeof runtime?.[method] === 'function')) {
      if (typeof runtime?.close === 'function') await runtime.close().catch(() => {});
      throw new Error('native runtime must own session factory, health and cleanup');
    }
    sessionFactory = runtime.factory; isConnected = runtime.connected;
  }
  const sessions = new Map();
  const sessionBindings = new Map();
  let shuttingDown = false;
  const server = http.createServer(async (request, response) => {
    const supplied = Buffer.from(request.headers.authorization ?? '');
    if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) {
      json(response, 401, { ok: false, error: 'unauthorized' });
      return;
    }
    try {
      if (shuttingDown) throw new HTTPError(503, 'browser worker is shutting down');
      const parsed = new URL(request.url, 'http://worker.invalid'), path = parsed.pathname;
      if (request.method === 'GET' && path === '/healthz') {
        const connected = (isConnected ? await isConnected() : browser.isConnected()) === true;
        json(response, connected ? 200 : 503, { ok: connected, backend: 'native' });
        return;
      }
      const match = /^\/sessions\/([A-Za-z0-9_-]{1,160})(?:\/(state|frame|execute|cancel|control|input|pick|pick\/preview|auth|tabs|uploads(?:\/[a-fA-F0-9-]{36})?|downloads(?:\/[a-fA-F0-9-]{36}(?:\/content)?)?))?$/.exec(path);
      if (!match) throw new HTTPError(404, 'route not found');
      const [, id, endpoint] = match;
      if (!endpoint && request.method === 'PUT') {
        const metadata = sessionMetadata(await readJSON(request));
        const binding = JSON.stringify(metadata);
        if (sessions.has(id) && sessionBindings.get(id) !== binding) throw new HTTPError(409, 'browser session metadata cannot change while the session exists');
        if (!sessions.has(id)) {
          if (sessions.size >= maxSessions) throw new HTTPError(429, 'browser session capacity reached');
          const creating = (async () => {
            if (sessionFactory) {
              // A managed host owns visible pages and their isolated lifecycle.
              // Never create a hidden context or fall back after a host failure.
              const resource = await sessionFactory({ id, browser, actionTimeout, settleMs, ...metadata });
              let session;
              try {
                if (!resource?.context || !resource.host) throw new Error('sessionFactory must return context and host');
                session = new BrowserSession(resource.context, { actionTimeout, settleMs, host: resource.host, maxPages });
                await resource.host.bindSession?.(session);
                return await session.init();
              } catch (error) {
                if (session) await session.close().catch(() => {});
                else if (typeof resource?.host?.close === 'function') await Promise.resolve().then(() => resource.host.close()).catch(() => {});
                throw error;
              }
            }
            // Explicit browser injection serves CDP contract tests. Production
            // always uses the managed Wayland factory above, without fallback.
            const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1, acceptDownloads: false });
            try {
              const session = await new BrowserSession(context, { actionTimeout, settleMs, maxPages }).init();
              session.mediaConfig = mediaConfig;
              return session;
            }
            catch (error) { await context.close(); throw error; }
          })();
          sessions.set(id, creating);
          sessionBindings.set(id, binding);
          creating.catch(() => { if (sessions.get(id) === creating) { sessions.delete(id); sessionBindings.delete(id); } });
        }
        const session = await sessions.get(id);
        session.assertLive();
        json(response, 200, { ok: true, ...session.state() });
        return;
      }
      if (!endpoint && request.method === 'DELETE') {
        const pending = sessions.get(id);
        if (pending) {
          const session = await pending;
          await session.close();
          if (sessions.get(id) === pending) { sessions.delete(id); sessionBindings.delete(id); }
        }
        json(response, 200, { ok: true });
        return;
      }
      if (endpoint === 'cancel' && request.method === 'POST' && !sessions.has(id)) {
        json(response, 200, { ok: true });
        return;
      }
      if (!sessions.has(id)) throw new HTTPError(404, 'browser session not found', 'BROWSER_SESSION_EXPIRED');
      const session = await sessions.get(id);
      session.assertLive();
      const selection = {};
      for (const key of ['tab_id', 'context_ref']) {
        if (parsed.searchParams.getAll(key).length > 1) throw new HTTPError(400, 'duplicate browser tab selection');
        if (parsed.searchParams.has(key)) selection[key] = parsed.searchParams.get(key);
      }
      if (endpoint?.startsWith('uploads')) {
        session.lastUsed = Date.now();
        const [, uploadId] = endpoint.split('/');
        if (!uploadId && request.method === 'POST') {
          if (request.headers['content-type'] !== 'application/octet-stream') throw new HTTPError(415, 'upload body must be application/octet-stream', 'UPLOAD_INVALID_CONTENT_TYPE');
          const file = await session.uploads.receive(request, { filename: request.headers['x-upload-filename'], mimeType: request.headers['x-upload-mime-type'], contentLength: request.headers['content-length'] });
          session.assertLive(); session.lastUsed = Date.now();
          json(response, 200, { ok: true, file });
        } else if (uploadId && request.method === 'DELETE') { await session.uploads.delete(uploadId); json(response, 200, { ok: true }); }
        else throw new HTTPError(405, 'method not allowed');
      } else if (endpoint?.startsWith('downloads')) {
        const downloads = session.host?.downloads;
        if (!downloads) throw new HTTPError(501, 'this browser host does not support workspace downloads', 'DOWNLOAD_UNSUPPORTED');
        session.lastUsed = Date.now();
        const [, downloadId, content] = endpoint.split('/');
        if (!downloadId && request.method === 'GET') json(response, 200, downloads.list());
        else if (downloadId && content === 'content' && request.method === 'GET') {
          const payload = await downloads.content(downloadId);
          response.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Length': payload.size, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
          await pipeline(payload.stream, response);
        } else if (downloadId && !content && request.method === 'DELETE') { await downloads.acknowledge(downloadId); json(response, 200, { ok: true }); }
        else throw new HTTPError(405, 'method not allowed');
      } else if (endpoint === 'state' && request.method === 'GET') json(response, 200, session.state(selection));
      else if (endpoint === 'frame' && request.method === 'GET') {
        if (session.host?.displayMode === 'desktop') throw new HTTPError(410, 'Open this browser in Pogo Desktop; remote human-view frames are retired');
        const { data, version, overlay, identity, context_ref, viewed_tab } = await session.frame(selection);
        const meta = session.selectedPage({ tab_id: viewed_tab, context_ref });
        if (identity !== session.frameIdentity(meta) || version && version !== session.version(meta)) throw new HTTPError(409, 'page changed while capturing frame; request a new frame');
        // No version means display-only pixels, never a coordinate capability.
        response.writeHead(200, { 'Content-Type': 'image/jpeg', 'Cache-Control': 'no-store', 'X-Browser-Frame-Identity': identity,
          'X-Browser-Context-Ref': context_ref, 'X-Browser-Viewed-Tab': viewed_tab, ...(version ? { 'X-Browser-Version': version } : {}),
          ...(overlay ? { 'X-Browser-Overlay': Buffer.from(JSON.stringify(overlay)).toString('base64url') } : {}) });
        response.end(data);
      } else if (endpoint === 'execute' && request.method === 'POST') json(response, 200, await session.execute(await readJSON(request)));
      else if (endpoint === 'tabs' && request.method === 'POST') json(response, 200, await session.manageTabs(await readJSON(request)));
      else if (endpoint === 'auth' && request.method === 'POST') json(response, 410, { ok: false, error: 'browser credential helpers have been retired; use the current browser session' });
      else if (endpoint === 'pick' && request.method === 'POST') {
        if (session.host?.displayMode === 'desktop') throw new HTTPError(410, 'Use the native desktop element inspector');
        json(response, 200, await session.picker.pick(await readJSON(request)));
      }
      else if (endpoint === 'pick/preview' && request.method === 'POST') {
        if (session.host?.displayMode === 'desktop') throw new HTTPError(410, 'Use the native desktop element inspector');
        json(response, 200, await session.picker.preview(await readJSON(request)));
      }
      else if (endpoint === 'cancel' && request.method === 'POST') json(response, 200, await session.cancel());
      else if (endpoint === 'control' && request.method === 'POST') json(response, 200, await session.setControl(await readJSON(request)));
      else if (endpoint === 'input' && request.method === 'POST') {
        const input=await readJSON(request);
        if (session.host?.displayMode === 'desktop' && !['navigate','back','dialog'].includes(input.type)) throw new HTTPError(410, 'Use native desktop input; remote human input forwarding is retired');
        json(response, 200, await session.input(input));
      }
      else throw new HTTPError(405, 'method not allowed');
    } catch (error) {
      if (!response.headersSent) json(response, error.status ?? 500, { ok: false, error: String(error.message).slice(0, 2000), ...(error.code ? { code: error.code } : {}) });
      else response.destroy();
    }
  });
  const live = attachLiveServer(server, {
    authenticate(request) {
      const supplied = Buffer.from(request.headers.authorization ?? '');
      return supplied.length === expected.length && timingSafeEqual(supplied, expected);
    },
    async getSession(id) {
      if (shuttingDown) throw new HTTPError(503, 'browser worker is shutting down');
      if (!sessions.has(id)) throw new HTTPError(404, 'browser session not found', 'BROWSER_SESSION_EXPIRED');
      const session = await sessions.get(id); session.assertLive(); return session;
    },
  });
  server.requestTimeout = 40_000;
  server.headersTimeout = 10_000;
  const reaper = setInterval(async () => {
    for (const [id, pending] of sessions) {
      const session = await pending.catch(() => null);
      if (session && !session.rawAction && !session.uploads.tasks.size && session.phase === 'ready' && Date.now() - session.lastUsed > idleMs) {
        // Reserve removal before awaiting close, so no new request can revive it.
        sessions.delete(id);
        sessionBindings.delete(id);
        await session.close().catch(() => {});
      }
    }
  }, Math.min(idleMs, 60_000));
  reaper.unref();
  return {
    server, runtime,
    async listen(port = 7788, host = '127.0.0.1') {
      if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('POGO_BROWSER_PORT must be 1–65535 (0 only for test allocation)');
      await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, host, resolve); });
      return server.address();
    },
    async close() {
      shuttingDown = true;
      clearInterval(reaper);
      await live.close();
      await Promise.allSettled([...sessions.values()].map(async p => (await p).close()));
      sessions.clear();
      sessionBindings.clear();
      server.closeAllConnections();
      await new Promise(resolve => server.close(resolve));
      if (ownRuntime) await runtime.close();
    },
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const port = Number(process.env.POGO_BROWSER_PORT || 7788);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('POGO_BROWSER_PORT must be 1–65535');
  const mediaConfig = mediaConfiguration();
  const runtime = await new WaylandRuntime(mediaConfig, {
    sandbox: process.env.POGO_BROWSER_CHROMIUM_SANDBOX !== '0',
    onFailure: () => { process.stderr.write('Native media service disconnected; exiting for worker recovery.\n'); process.exit(1); },
  }).start();
  const worker = await createWorker({
    token: process.env.POGO_BROWSER_TOKEN,
    mediaConfig,
    sessionFactory: runtime.factory,
    allowBrowserLaunch: false,
    isConnected: runtime.connected,
    maxSessions: Number(process.env.POGO_BROWSER_MAX_SESSIONS || 32),
    maxPages: Number(process.env.POGO_BROWSER_MAX_PAGES || 0),
    idleMs: Number(process.env.POGO_BROWSER_IDLE_MS || 1_800_000),
    settleMs: process.env.POGO_BROWSER_SETTLE_MS === undefined ? undefined : Number(process.env.POGO_BROWSER_SETTLE_MS),
    sandbox: process.env.POGO_BROWSER_CHROMIUM_SANDBOX !== '0',
    onBrowserDisconnect: () => {
      // Health status alone does not trigger Docker's restart policy.
      process.stderr.write('Chromium disconnected unexpectedly; exiting so the supervisor can restart the worker.\n');
      process.exit(1);
    },
  });
  const address = await worker.listen(port, process.env.POGO_BROWSER_HOST || '127.0.0.1');
  process.stdout.write(`Pogo browser worker listening on ${address.address}:${address.port}\n`);
  // Only the resolved class is logged, never the advertised address.
  process.stdout.write(`WebRTC network profile: ${mediaConfig.network}\n`);
  process.stdout.write(`WebRTC playout delay: ${mediaConfig.playoutDelay}\n`);
  process.stdout.write(`Browser runtime: Wayland / WebRTC VP8; maximum output scale: ${mediaConfig.deviceScaleFactor}\n`);
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => worker.close().finally(() => runtime.close()).then(() => process.exit(0)));
}
