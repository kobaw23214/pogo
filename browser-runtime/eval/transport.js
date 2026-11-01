import http from 'node:http';
import { timingSafeEqual, randomUUID } from 'node:crypto';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

export function makeRedactor(environment = process.env, extraSecrets = []) {
  const values = [...extraSecrets, ...Object.entries(environment).filter(([name]) => /(?:KEY|TOKEN|SECRET|PASSWORD)/i.test(name)).map(([, value]) => value)]
    .filter(value => typeof value === 'string' && value.length >= 6).sort((a, b) => b.length - a.length);
  return function redact(value) {
    let text = typeof value === 'string' ? value : JSON.stringify(value);
    for (const secret of values) text = text.split(secret).join('[REDACTED]');
    return text.replace(/Bearer\s+[^\s"'<>]+/gi, 'Bearer [REDACTED]')
      .replace(/(https?:\/\/)[^\s/:]+:[^\s/@]+@/gi, '$1[REDACTED]@')
      .replace(/([?&](?:api_?key|token|secret|password)=)[^&#\s"']+/gi, '$1[REDACTED]');
  };
}

export function safeValue(value, redact) {
  if (Array.isArray(value)) return value.slice(0, 1000).map(item => safeValue(item, redact));
  if (value && typeof value === 'object') {
    const clean = {};
    for (const [key, item] of Object.entries(value)) {
      if (/^(?:authorization|api_?key|lease_token|worker_token|data_url|dataurl|headers)$/i.test(key)) continue;
      // Screenshot bytes are a transport concern, not a JSON trace field.
      if (key === 'screenshot' && item && typeof item === 'object') {
        const { data, ...metadata } = item;
        clean[key] = safeValue(metadata, redact);
      } else clean[key] = safeValue(item, redact);
    }
    return clean;
  }
  return typeof value === 'string' ? redact(value).slice(0, 64_000) : value;
}

export async function workerRequest(base, token, suffix, { method = 'GET', body, timeout = 20_000 } = {}) {
  const response = await fetch(base + suffix, {
    method, headers: { Authorization: `Bearer ${token}`, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(timeout),
  });
  const bytes = Buffer.from(await response.arrayBuffer());
  if (bytes.length > 24 * 1024 * 1024) throw new Error('worker response exceeds evaluation limit');
  const data = response.headers.get('content-type')?.includes('json') ? JSON.parse(bytes.toString()) : null;
  return { status: response.status, ok: response.ok, headers: response.headers, bytes, data };
}

/** Isolated recording proxy. Only registered benchmark sessions can use it.
 * Every execute, including batch children, consumes the declared step budget.
 * Image capture happens after execution and before its result is acknowledged. */
export async function createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames = true, onExchange,
  bindHost = '127.0.0.1', bindPort = 0, allowSessionControls = false, admitSession, onLifecycle }) {
  if (!Number.isInteger(bindPort) || bindPort < 0 || bindPort > 65535) throw new Error('invalid proxy bindPort');
  if (typeof bindHost !== 'string' || !bindHost) throw new Error('invalid proxy bindHost');
  const records = new Map();
  const auth = Buffer.from(`Bearer ${token}`);
  async function capture(record, step) {
    if (!captureFrames || record.closed) return;
    try {
      const frame = await workerRequest(workerURL, token, `/sessions/${record.sessionId}/frame`, { timeout: 6000 });
      if (!frame.ok || frame.bytes[0] !== 0xff || record.closed) return;
      const relative = `frames/${record.artifactId}/${String(record.frames.length).padStart(4, '0')}.jpg`;
      await mkdir(path.dirname(path.join(outputDir, relative)), { recursive: true, mode: 0o700 });
      await writeFile(path.join(outputDir, relative), frame.bytes, { mode: 0o600 });
      record.frames.push({ path: relative, step, version: frame.headers.get('x-browser-version') });
    } catch {
      if (!record.closed) record.events.push({ type: 'frame_unavailable', step, at_ms: Date.now() - record.started });
    }
  }
  const server = http.createServer(async (request, response) => {
    const provided = Buffer.from(request.headers.authorization ?? '');
    const json = (status, data) => { response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); response.end(JSON.stringify(data)); };
    if (provided.length !== auth.length || !timingSafeEqual(provided, auth)) { json(401, { error: 'unauthorized' }); return; }
    const match = /^\/sessions\/([a-zA-Z0-9_-]{1,160})(?:\/(execute|cancel|state|frame|control|input|pick|auth))?$/.exec(request.url);
    let record = match && records.get(match[1]);
    // Admission is opt-in, synchronous and only for the real application's PUT.
    // The caller must reserve the one expected session before returning.
    if (!record && match && !match[2] && request.method === 'PUT' && admitSession) {
      try {
        const admitted = admitSession(match[1]);
        if (admitted && typeof admitted.then !== 'function') record = register(match[1], admitted.artifactId, admitted.maxSteps, admitted);
      } catch { json(409, { error: 'session admission rejected' }); return; }
    }
    if (!record || record.closed) { json(404, { error: 'evaluation session not registered' }); return; }
    const endpoint = match[2] ?? '';
    const allowed = (!endpoint && (request.method === 'PUT' || (allowSessionControls && request.method === 'DELETE')))
      || (['execute', 'cancel', ...(allowSessionControls ? ['control', 'input', 'pick', 'auth'] : [])].includes(endpoint) && request.method === 'POST')
      || (['state', 'frame'].includes(endpoint) && request.method === 'GET');
    if (!allowed) { json(405, { error: 'method not allowed' }); return; }
    if (record.deleted && ((!endpoint && request.method === 'PUT') || endpoint === 'execute')) { json(409, { error: 'evaluation session has been deleted' }); return; }
    let executing = false;
    try {
      let size = 0;
      const chunks = [];
      for await (const chunk of request) {
        size += chunk.length;
        if (size > 1_048_576) { json(413, { error: 'request too large' }); return; }
        chunks.push(chunk);
      }
      const body = size ? JSON.parse(Buffer.concat(chunks).toString()) : undefined;
      if (body !== undefined && (!body || typeof body !== 'object' || Array.isArray(body))) { json(400, { error: 'object required' }); return; }
      let step = record.steps;
      let call;
      const started = Date.now();
      const startedMonotonic = performance.now();
      if (endpoint === 'execute') {
        if (!record.executionEnabled) { json(409, { error: 'evaluation recording has not started or is finished' }); return; }
        if (record.evidenceError) { json(409, { error: 'evaluation evidence recording failed; stop this task' }); return; }
        const amount = body?.action === 'batch' && Array.isArray(body.actions) ? Math.max(1, body.actions.length) : 1;
        if (record.requestedSteps + amount > record.maxSteps) {
          record.stepLimit = true;
          json(409, { error: 'evaluation step budget exceeded' });
          return;
        }
        record.requestedSteps += amount;
        record.calls++;
        record.inFlight++; executing = true;
        call = record.calls;
        step = record.steps + 1;
        record.events.push({ type: 'browser_start', step, call: record.calls, at_ms: started - record.started, args: safeValue(body, redact) });
      }
      const result = await workerRequest(workerURL, token, request.url, { method: request.method, body, timeout: endpoint === 'cancel' ? 18_000 : 25_000 });
      if (endpoint === 'execute') {
        const ok = result.ok && result.data?.ok !== false;
        // The optional assessor observes actual worker acknowledgements through
        // a private callback. Adapter stdout and model-authored JSON cannot add
        // entries to this ledger. Clone before passing data so observers cannot
        // rewrite the response returned to the caller or the historical trace.
        if (onExchange) {
          try {
            await onExchange(structuredClone({ sessionId: record.sessionId, artifactId: record.artifactId,
              call_id: `${record.sessionId}:${call}`, call, args: body, result: result.data,
              http_status: result.status, ok, at_ms: Date.now() - record.started, duration_ms: Date.now() - started,
              started_monotonic_ms: startedMonotonic, acknowledged_monotonic_ms: performance.now() }));
          } catch {
            record.evidenceError = 'trusted worker exchange could not be recorded';
            throw new Error(record.evidenceError);
          }
        }
        record.steps += result.ok ? Array.isArray(result.data?.steps) ? result.data.steps.length : 1 : 0;
        step = record.steps;
        record.events.push({ type: 'browser_end', step, at_ms: Date.now() - record.started, duration_ms: Date.now() - started,
          ok, result: safeValue(result.data, redact) });
        if (!ok) record.failures.push(redact(result.data?.error ?? `browser HTTP ${result.status}`));
        if (result.data?.dialogs?.length) record.humanRequests++;
        await capture(record, step);
      }
      if (!endpoint && request.method === 'DELETE' && result.ok && result.data?.ok !== false) { record.deleted = true; record.executionEnabled = false; }
      if (endpoint !== 'execute' && onLifecycle) await onLifecycle(structuredClone({ sessionId: record.sessionId, endpoint, method: request.method,
        http_status: result.status, ok: result.ok && result.data?.ok !== false, at_ms: Date.now() - record.started }));
      const headers = {};
      for (const name of ['content-type', 'cache-control', 'x-browser-version', 'x-browser-overlay']) if (result.headers.has(name)) headers[name] = result.headers.get(name);
      response.writeHead(result.status, headers);
      response.end(result.bytes);
    } catch (error) {
      if (!response.headersSent) json(502, { error: redact(error.message) });
      else response.destroy();
    } finally { if (executing) record.inFlight--; }
  });
  server.requestTimeout = 30_000;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(bindPort, bindHost, resolve); });
  function register(sessionId, artifactId, maxSteps, { executionEnabled = true } = {}) {
    if (records.has(sessionId)) throw new Error('duplicate evaluation session');
    if (!Number.isInteger(maxSteps) || maxSteps < 1 || maxSteps > 10000) throw new Error('invalid evaluation step budget');
    const record = { sessionId, artifactId, maxSteps, executionEnabled, deleted: false, inFlight: 0, started: Date.now(), steps: 0, requestedSteps: 0, calls: 0, events: [], frames: [], failures: [], humanRequests: 0, closed: false, stepLimit: false };
    records.set(sessionId, record); return record;
  }
  return {
    url: `http://${bindHost === '0.0.0.0' ? '127.0.0.1' : bindHost === '::' ? '[::1]' : bindHost.includes(':') ? `[${bindHost}]` : bindHost}:${server.address().port}`,
    listener: { host: bindHost, port: server.address().port },
    register,
    getRecord(sessionId) { return records.get(sessionId); },
    capture,
    async execute(sessionId, action) {
      const result = await workerRequest(this.url, token, `/sessions/${sessionId}/execute`, { method: 'POST', body: { ...action, op_id: randomUUID() }, timeout: 35_000 });
      if (!result.ok) throw new Error(result.data?.error ?? `browser HTTP ${result.status}`);
      return result.data;
    },
    unregister(sessionId) { const record = records.get(sessionId); if (record) record.closed = true; records.delete(sessionId); },
    async close() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); },
  };
}
