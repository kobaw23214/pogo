import http from 'node:http';
import { randomUUID, timingSafeEqual, createHash } from 'node:crypto';
import { mkdir, writeFile, rename } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { createFixtures, TASKS } from '../eval/browser-use-v2/fixtures.js';
import { judgeCase } from '../eval/browser-use-v2/judge.js';
import { createRecordingProxy, makeRedactor, workerRequest } from '../eval/transport.js';

const hash = value => createHash('sha256').update(value).digest('hex');
function checkedInteger(value, fallback, min, max, name) {
  value ??= fallback;
  if (!Number.isInteger(value) || value < min || value > max) throw Object.assign(new Error(`invalid ${name}`), { status: 400 });
  return value;
}
function requirePhase(active, ...phases) {
  if (!active || !phases.includes(active.phase)) throw Object.assign(new Error(`run must be ${phases.join(' or ')}`), { status: 409 });
}
async function jsonBody(request) {
  let size = 0; const chunks = [];
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 1024 * 1024) throw Object.assign(new Error('controller body exceeds 1 MiB'), { status: 413 });
    chunks.push(chunk);
  }
  try {
    const body = JSON.parse(Buffer.concat(chunks).toString() || '{}');
    if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error();
    return body;
  } catch { throw Object.assign(new Error('controller body must be a JSON object'), { status: 400 }); }
}

/** Candidate-only private controller. The fixtures and recorder deliberately share
 * one process: judge causality uses their common performance.now() clock. Never
 * mount controller routes on the fixture or application-facing proxy listener. */
export async function createCandidateRecorder({ workerURL, token, adminToken, outputDir,
  proxyHost = '127.0.0.1', proxyPort = 0, adminHost = '127.0.0.1', adminPort = 0, fixtureOptions = {}, environment = process.env } = {}) {
  if (!token || typeof token !== 'string' || /[\r\n]/.test(token)) throw new Error('worker token required');
  if (typeof adminToken !== 'string' || adminToken.length < 16 || /[\r\n]/.test(adminToken) || adminToken === token) throw new Error('distinct controller token of at least 16 characters required');
  const worker = new URL(workerURL);
  if (!['http:', 'https:'].includes(worker.protocol) || worker.username || worker.password || worker.search || worker.hash || worker.pathname !== '/') throw new Error('workerURL must be an HTTP origin');
  if (!outputDir) throw new Error('private outputDir required');
  await mkdir(outputDir, { recursive: true, mode: 0o700 });
  const redact = makeRedactor(environment, [token, adminToken]);
  // Preserve full worker payloads without the public report's array/string caps.
  // Credentials are redacted, but response bodies and provenance are not elided.
  const clean = value => JSON.parse(redact(JSON.stringify(value, (key, child) => /^(?:authorization|password|passwd|cookie|cookies|set-cookie|lease_token|worker_token)$/i.test(key) ? '[REDACTED]' : child)));
  let active, busy = false;
  const recordOf = () => active?.workerSessionId ? proxy.getRecord(active.workerSessionId) : undefined;
  const proxy = await createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames: false,
    bindHost: proxyHost, bindPort: proxyPort, allowSessionControls: true,
    admitSession(sessionId) {
      if (!active || active.phase !== 'armed' || active.workerSessionId) return null;
      active.workerSessionId = sessionId;
      active.phase = 'admitted';
      active.admittedAt = new Date().toISOString();
      return { artifactId: active.id, maxSteps: active.maxSteps, executionEnabled: false };
    },
    onExchange(exchange) {
      if (!active || active.phase !== 'recording' || exchange.sessionId !== active.workerSessionId || exchange.artifactId !== active.id) throw new Error('exchange outside active recording');
      if (active.callIds.has(exchange.call_id)) throw new Error('duplicate worker exchange');
      active.callIds.add(exchange.call_id);
      active.ledger.push(structuredClone(exchange));
    },
    onLifecycle(event) {
      if (active?.workerSessionId !== event.sessionId) return;
      active.lifecycle.push(event);
      if (event.endpoint === '' && event.method === 'DELETE' && event.ok) active.deleteAcknowledged = true;
      if (event.endpoint === 'cancel' && event.ok) active.cancelAcknowledged = true;
      if (active.phase === 'recording' && ['input', 'control', 'pick', 'auth'].includes(event.endpoint)) active.externalInput = true;
    },
  });
  const direct = async (suffix, method = 'GET', body) => {
    if (!active?.workerSessionId) throw new Error('worker session is not mapped');
    const result = await workerRequest(workerURL, token, `/sessions/${active.workerSessionId}${suffix}`, { method, body, timeout: 25_000 });
    if (!result.ok) throw new Error(`controller worker operation failed: ${result.data?.error ?? result.status}`);
    return result.data;
  };
  const execute = args => direct('/execute', 'POST', { ...args, op_id: randomUUID() });
  const snapshot = () => ({ run_id: active?.id ?? null, phase: active?.phase ?? 'idle', task_id: active?.task?.id ?? null,
    studio_session_id: active?.studioSessionId ?? null, worker_session_id: active?.workerSessionId ?? null,
    preparation: active?.preparation ?? null, steps: recordOf()?.steps ?? 0, browser_requests: recordOf()?.calls ?? 0,
    cancel_acknowledged: active?.cancelAcknowledged ?? false, delete_acknowledged: active?.deleteAcknowledged ?? false });
  const writeReport = async () => {
    const destination = path.join(outputDir, `${active.id}.json`), temporary = `${destination}.tmp`;
    await writeFile(temporary, JSON.stringify(clean(active.report), null, 2), { mode: 0o600 });
    await rename(temporary, destination);
  };
  async function cleanup() {
    if (!active) return { phase: 'idle', context_cleaned: true };
    if (active.phase === 'cleaned') return { ...snapshot(), context_cleaned: active.report.context_cleaned };
    const record = recordOf();
    if (record) record.executionEnabled = false;
    let cleaned = !active.workerSessionId;
    try {
      if (active.workerSessionId) {
        const response = await workerRequest(proxy.url, token, `/sessions/${active.workerSessionId}`, { method: 'DELETE', timeout: 25_000 });
        cleaned = response.ok && response.data?.ok !== false;
      }
    } catch { cleaned = false; }
    const serverState = active.platform.inspect(active.task.id);
    active.report ??= { schema_version: 1, kind: 'browser-use-v2-studio-smoke', run_id: active.id, task_id: active.task.id, seed: active.seed,
      studio_session_id: active.studioSessionId, worker_session_id: active.workerSessionId,
      preparation: active.preparation ?? null, ledger: active.ledger, judgement: null };
    Object.assign(active.report, { server_state_after_cleanup: serverState, context_cleaned: cleaned,
      lifecycle: active.lifecycle, cancel_acknowledged: active.cancelAcknowledged, delete_acknowledged: active.deleteAcknowledged,
      completed_at: new Date().toISOString() });
    if (active.report.judgement) {
      const finalJudgement = await judgeCase({ task: active.task, oracle: active.oracle, fixtureState: serverState,
        ledger: structuredClone(active.ledger), finalAnswer: active.report.final_answer });
      active.report.judgement_after_cleanup = finalJudgement;
      active.report.judgement.failures = [...new Set([...active.report.judgement.failures, ...finalJudgement.failures])];
      active.report.judgement.success = active.report.judgement.success && finalJudgement.success;
      if (!active.report.judgement.success) active.report.status = 'failed';
    }
    if (active.report.judgement && !cleaned) {
      active.report.judgement.success = false;
      active.report.judgement.failures.push('context_cleanup_failed');
      active.report.status = 'failed';
    }
    // Keep the fixture available when cleanup fails so a paused writer cannot be
    // mistaken for a completed teardown. The caller must retry or stop the worker.
    if (cleaned) {
      await active.platform.close();
      if (active.workerSessionId) proxy.unregister(active.workerSessionId);
      active.phase = 'cleaned';
    } else active.phase = 'cleanup_failed';
    await writeReport();
    return { ...snapshot(), context_cleaned: cleaned };
  }
  const api = async (route, body) => {
    if (route === '/runs/begin') {
      if (active && active.phase !== 'cleaned') throw Object.assign(new Error('previous run must be cleaned before beginning'), { status: 409 });
      if (!TASKS.some(task => task.id === body.task_id)) throw Object.assign(new Error('unknown task_id'), { status: 400 });
      if (typeof body.studio_session_id !== 'string' || !/^[A-Za-z0-9_-]{1,160}$/.test(body.studio_session_id)) throw Object.assign(new Error('invalid owned studio_session_id'), { status: 400 });
      const seed = checkedInteger(body.seed, undefined, 0, 2 ** 32 - 1, 'seed');
      const maxSteps = checkedInteger(body.max_steps, 80, 1, 200, 'max_steps');
      const tokenBudget = checkedInteger(body.token_budget, 120000, 1, 200000, 'token_budget');
      const timeoutMs = checkedInteger(body.timeout_ms, 180000, 1000, 600000, 'timeout_ms');
      const platform = await createFixtures({ ...fixtureOptions, seed });
      const task = platform.tasks.find(item => item.id === body.task_id);
      active = { id: `studio-${task.id}-${randomUUID()}`, phase: 'armed', studioSessionId: body.studio_session_id,
        seed, platform, task, maxSteps, tokenBudget, timeoutMs, ledger: [], callIds: new Set(), lifecycle: [],
        cancelAcknowledged: false, deleteAcknowledged: false, externalInput: false, beganAt: new Date().toISOString() };
      return { ...snapshot(), task: { id: task.id, prompt: task.prompt, start_url: task.startURL },
        budgets: { max_steps: maxSteps, token_budget: tokenBudget, timeout_ms: timeoutMs } };
    }
    if (route === '/runs/prepare') {
      requirePhase(active, 'admitted'); active.phase = 'preparing';
      try {
        active.platform.reset(active.task.id);
        const opened = await execute({ action: 'navigate', url: active.task.startURL });
        if (opened.ok === false) throw new Error(`initial navigation failed: ${opened.error}`);
        active.preparation = await active.platform.prepare(active.task.id, { execute, direct });
        active.oracle = active.platform.oracle(active.task.id);
        active.serverStateBefore = active.platform.inspect(active.task.id);
        active.phase = 'prepared';
        return snapshot();
      } catch (error) { active.phase = 'preparation_failed'; throw error; }
    }
    if (route === '/runs/execute') {
      requirePhase(active, 'admitted', 'prepared');
      return execute(body);
    }
    if (route === '/runs/start') {
      requirePhase(active, 'prepared');
      const record = recordOf();
      if (!record || active.deleteAcknowledged || active.cancelAcknowledged) throw Object.assign(new Error('prepared worker session no longer live'), { status: 409 });
      active.phase = 'recording'; active.started = performance.now(); active.startedAt = new Date().toISOString();
      record.executionEnabled = true; record.started = Date.now();
      return snapshot();
    }
    if (route === '/runs/finish') {
      requirePhase(active, 'recording');
      const record = recordOf();
      if (record.inFlight) throw Object.assign(new Error('browser action still in flight'), { status: 409 });
      record.executionEnabled = false;
      const state = active.platform.inspect(active.task.id);
      const finalAnswer = typeof body.answer === 'string' ? body.answer : '';
      const judgement = await judgeCase({ task: active.task, oracle: active.oracle, fixtureState: state,
        ledger: structuredClone(active.ledger), finalAnswer });
      const failures = judgement.failures;
      const usageComplete = body.usage_partial === false && ['input', 'output'].every(key => Number.isFinite(body.usage?.[key]) && body.usage[key] >= 0);
      if (!usageComplete) failures.push('missing_complete_measured_token_usage');
      else if (body.usage.input + body.usage.output > active.tokenBudget) failures.push('token_budget_exceeded');
      if (body.end_reason !== 'completed') failures.push('studio_run_not_completed');
      if (typeof body.run_id !== 'string' || !body.run_id) failures.push('missing_studio_run_id');
      if (record.stepLimit) failures.push('step_budget_exceeded');
      if (record.evidenceError) failures.push('trusted_worker_evidence_unavailable');
      if (active.externalInput) failures.push('external_browser_input_during_model_run');
      const duration = performance.now() - active.started;
      if (duration > active.timeoutMs) failures.push('task_time_budget_exceeded');
      if (!active.ledger.some(item => item.ok)) failures.push('no_real_successful_browser_action');
      judgement.failures = [...new Set(failures)]; judgement.success = judgement.failures.length === 0;
      active.report = { schema_version: 1, kind: 'browser-use-v2-studio-candidate', run_id: active.id,
        studio_run_id: body.run_id ?? null, studio_session_id: active.studioSessionId, worker_session_id: active.workerSessionId,
        task_id: active.task.id, seed: active.seed, task_prompt: active.task.prompt, start_url: active.task.startURL,
        task_prompt_sha256: hash(active.task.prompt), preparation: active.preparation,
        began_at: active.beganAt, started_at: active.startedAt, finished_at: new Date().toISOString(),
        duration_ms: Math.round(duration), reported_duration_ms: body.duration_ms ?? null,
        usage: body.usage ?? null, usage_partial: !usageComplete, end_reason: body.end_reason ?? null,
        budgets: { max_steps: active.maxSteps, token_budget: active.tokenBudget, timeout_ms: active.timeoutMs },
        steps: record.steps, requested_steps: record.requestedSteps, browser_requests: record.calls,
        final_answer: finalAnswer, ledger: active.ledger, ledger_sha256: hash(JSON.stringify(clean(active.ledger))),
        server_state_before: active.serverStateBefore, server_state_after: state, judgement,
        status: judgement.success ? 'passed' : 'failed', context_cleaned: null,
        acceptance_scope: { transport: 'Studio App browser tool -> private recording proxy -> hosted worker',
          fixture: 'controlled_synthetic_sites', evidence: 'private actual worker acknowledgements',
          oracle: 'controller process memory only', monotonic_clock: 'shared recorder and fixtures process',
          token_budget: 'external measured-usage cancellation; in-flight usage may exceed limit',
          controller_preparation_in_model_ledger: false } };
      active.phase = 'finished'; await writeReport();
      return { ...snapshot(), status: active.report.status, judgement: clean(judgement) };
    }
    if (route === '/runs/cleanup') return cleanup();
    throw Object.assign(new Error('controller route not found'), { status: 404 });
  };
  const auth = Buffer.from(`Bearer ${adminToken}`);
  const admin = http.createServer(async (request, response) => {
    const send = (status, value) => { response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); response.end(JSON.stringify(clean(value))); };
    const supplied = Buffer.from(request.headers.authorization ?? '');
    if (supplied.length !== auth.length || !timingSafeEqual(supplied, auth)) { send(401, { error: 'unauthorized' }); return; }
    if (request.method === 'GET' && request.url === '/healthz') { send(200, { ok: true, phase: active?.phase ?? 'idle' }); return; }
    if (request.method === 'GET' && request.url === '/runs/state') { send(200, snapshot()); return; }
    if (request.method === 'GET' && request.url === '/runs/report') {
      if (!active?.report) { send(409, { error: 'report not available until finish or cleanup' }); return; }
      send(200, active.report); return;
    }
    if (request.method !== 'POST') { send(404, { error: 'controller route not found' }); return; }
    if (busy) { send(409, { error: 'controller operation already in progress' }); return; }
    busy = true;
    try { send(200, await api(request.url, await jsonBody(request))); }
    catch (error) { send(error.status ?? 500, { error: redact(String(error.message)) }); }
    finally { busy = false; }
  });
  admin.requestTimeout = 60_000; admin.headersTimeout = 10_000;
  try { await new Promise((resolve, reject) => { admin.once('error', reject); admin.listen(adminPort, adminHost, resolve); }); }
  catch (error) { await proxy.close(); throw error; }
  return { proxyURL: proxy.url, adminURL: `http://${adminHost.includes(':') ? `[${adminHost}]` : adminHost}:${admin.address().port}`,
    proxyListener: proxy.listener, adminListener: { host: adminHost, port: admin.address().port },
    async close() {
      try { await cleanup(); }
      finally { admin.closeAllConnections(); await new Promise(resolve => admin.close(resolve)); await proxy.close(); }
    } };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const service = await createCandidateRecorder({ workerURL: process.env.V2_WORKER_URL ?? process.env.POGO_BROWSER_UPSTREAM_URL,
    token: process.env.POGO_BROWSER_TOKEN, adminToken: process.env.V2_RECORDER_ADMIN_TOKEN ?? process.env.POGO_RECORDER_ADMIN_TOKEN,
    outputDir: process.env.V2_RECORDER_OUTPUT_DIR ?? process.env.POGO_RECORDER_OUTPUT_DIR ?? '/tmp/v2-recorder',
    proxyHost: process.env.V2_RECORDER_PROXY_HOST ?? process.env.POGO_RECORDER_PROXY_HOST ?? '127.0.0.1',
    proxyPort: Number(process.env.V2_RECORDER_PROXY_PORT ?? process.env.POGO_RECORDER_PROXY_PORT ?? 7789),
    adminHost: process.env.V2_RECORDER_ADMIN_HOST ?? process.env.POGO_RECORDER_ADMIN_HOST ?? '127.0.0.1',
    adminPort: Number(process.env.V2_RECORDER_ADMIN_PORT ?? process.env.POGO_RECORDER_ADMIN_PORT ?? 7790),
    fixtureOptions: { bindHost: process.env.V2_FIXTURE_HOST ?? process.env.POGO_FIXTURE_HOST ?? '127.0.0.1',
      bindPort: Number(process.env.V2_FIXTURE_PORT ?? process.env.POGO_FIXTURE_PORT ?? 5191),
      advertisedOrigins: (process.env.V2_FIXTURE_ORIGINS ?? process.env.POGO_FIXTURE_ORIGINS ?? 'http://fixture-a.test:5191,http://fixture-b.test:5191').split(',') } });
  console.log(JSON.stringify({ service: 'browser-use-v2-recorder', proxy: service.proxyListener, admin: service.adminListener }));
  let closing = false;
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, async () => { if (closing) return; closing = true; await service.close(); process.exit(0); });
}
