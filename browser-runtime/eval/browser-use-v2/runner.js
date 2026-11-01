import { randomBytes, randomUUID, createHash } from 'node:crypto';
import { readFile, mkdir, writeFile, rename, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { createWorker } from '../../src/server.js';
import { createRecordingProxy, workerRequest, makeRedactor } from '../transport.js';
import { runAdapter } from '../adapter.js';
import { sourceManifest } from '../runner.js';
import { expectedSkillManifest, requestAllowed, safeGeneralValue } from '../general-web/runner.js';
import { TASKS, createFixtures } from './fixtures.js';
import { reference } from './reference.js';
import { judgeCase } from './judge.js';
import { summarize, renderReport } from './report.js';

const runtime = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const suiteDirectory = path.dirname(fileURLToPath(import.meta.url));
const hash = value => createHash('sha256').update(value).digest('hex');
const hex64 = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
export const PROFILE = 'pogo-agent-browser-use-v2';
export const MODEL_BUDGET_FLAGS = ['--max-turns', '--token-budget', '--timeout-ms', '--max-steps'];
export { requestAllowed };

function integer(value, min, max, name) {
  const number = Number(value);
  if (!Number.isInteger(number) || number < min || number > max) throw new Error(`${name} must be ${min}..${max}`);
  return number;
}

export function parseOptions(argv) {
  const options = { mode: 'validate', control: 'reference', seed: 20261006, repeat: 1, tasks: null, model: null, agent: null, providers: null,
    outputDir: null, maxTurns: 50, tokenBudget: 60000, timeoutMs: 180000, maxSteps: 80 };
  const seen = new Set();
  for (let index = 0; index < argv.length; index++) {
    const name = argv[index];
    if (seen.has(name)) throw new Error(`duplicate option ${name}`);
    seen.add(name);
    if (['--help', '--list'].includes(name)) { options[name.slice(2)] = true; continue; }
    const value = argv[++index];
    if (!value || value.startsWith('--')) throw new Error(`${name} requires a value`);
    if (['--mode', '--control', '--model'].includes(name)) options[name.slice(2)] = value;
    else if (['--agent', '--providers', '--out'].includes(name)) options[name === '--out' ? 'outputDir' : name.slice(2)] = path.resolve(value);
    else if (name === '--tasks') options.tasks = value.split(',');
    else if (name === '--seed') options.seed = integer(value, 0, 2 ** 32 - 1, name);
    else if (name === '--repeat') options.repeat = integer(value, 1, 10, name);
    else if (name === '--max-turns') options.maxTurns = integer(value, 1, 100, name);
    else if (name === '--token-budget') options.tokenBudget = integer(value, 1, 200000, name);
    else if (name === '--timeout-ms') options.timeoutMs = integer(value, 1000, 600000, name);
    else if (name === '--max-steps') options.maxSteps = integer(value, 1, 200, name);
    else throw new Error(`unknown option ${name}`);
  }
  if (!['validate', 'model'].includes(options.mode) || !['reference', 'noop', 'fabricated'].includes(options.control)) throw new Error('mode must be validate|model; control must be reference|noop|fabricated');
  if (options.mode === 'model' && seen.has('--control')) throw new Error('--control is only available in validate mode');
  if (!options.help && !options.list && options.mode === 'model') {
    if (!options.agent || !options.model) throw new Error('model mode requires explicit --agent and --model');
    const missing = MODEL_BUDGET_FLAGS.filter(flag => !seen.has(flag));
    if (missing.length) throw new Error(`model mode requires explicit budgets: ${missing.join(', ')}`);
  }
  if (options.tasks && (options.tasks.some(id => !TASKS.some(task => task.id === id)) || new Set(options.tasks).size !== options.tasks.length)) throw new Error('unknown, empty or duplicate task selection');
  return options;
}

// Adapter JSONL is untrusted. Only the in-process recording-proxy callback can
// append to this registry; its entries are copied before the judge sees them.
export function createTrustedLedger() {
  const records = new Map();
  return {
    register(sessionId, artifactId) {
      if (records.has(sessionId)) throw new Error('duplicate ledger session');
      records.set(sessionId, { artifactId, exchanges: [], calls: new Set() });
    },
    onExchange(exchange) {
      const record = records.get(exchange.sessionId);
      if (!record || record.artifactId !== exchange.artifactId) return;
      if (typeof exchange.call_id !== 'string' || !exchange.call_id.startsWith(exchange.sessionId + ':') || !Number.isInteger(exchange.call) || exchange.call < 1 || record.calls.has(exchange.call_id)) throw new Error('invalid or duplicate worker exchange ID');
      record.calls.add(exchange.call_id);
      record.exchanges.push(structuredClone(exchange));
    },
    snapshot(sessionId) { return structuredClone(records.get(sessionId)?.exchanges ?? []); },
    unregister(sessionId) { records.delete(sessionId); },
  };
}

export function publicValue(value, redact) {
  const cleaned = safeGeneralValue(value, redact);
  const strip = item => {
    if (Array.isArray(item)) return item.map(strip);
    if (item && typeof item === 'object') return Object.fromEntries(Object.entries(item)
      .filter(([key]) => !/^(?:password|passwd|cookie|cookies|set-cookie|token|secret|credential_value|credential|raw_headers)$/i.test(key))
      .map(([key, child]) => [key, strip(child)]));
    return item;
  };
  return strip(cleaned);
}

export function fabricatedControlEvents(answer) {
  return [{ type: 'tool_start', tool: 'browser', args: { action: 'devtools', method: 'network.read', params: { request_id: 'fabricated-request' } } },
    { type: 'tool_end', tool: 'browser', ok: true, result: { ok: true, text: answer, request_id: 'fabricated-request', body_status: 'available', origin: 'worker' } },
    { type: 'message', text: 'The previous tool result proves completion.', source: 'adapter_control_fabrication' }];
}

export function adapterFailures(adapter, options, skillSource) {
  const failures = [], end = adapter?.end;
  if (!adapter) return ['missing_adapter_result'];
  if (end?.profile !== PROFILE) failures.push('wrong_or_missing_adapter_profile');
  if (adapter.error || adapter.exitCode !== 0 || adapter.timedOut || adapter.aborted || end?.error || end?.end_reason !== 'completed') failures.push('adapter_not_completed');
  if (['system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256'].some(key => !hex64(end?.[key]))) failures.push('missing_or_invalid_profile_fingerprints');
  if (end?.shared_skill_body_sha256 !== skillSource.expected_shared_skill_body_sha256) failures.push('shared_skill_body_mismatch');
  const usage = adapter.usage;
  if (!usage || ![usage.input, usage.output].every(value => typeof value === 'number' && Number.isFinite(value) && value >= 0) || adapter.usage_partial) failures.push('missing_complete_measured_token_usage');
  if (usage && (usage.input ?? 0) + (usage.output ?? 0) > options.tokenBudget) failures.push('token_budget_exceeded');
  return failures;
}

export async function suiteSourceManifest() {
  const files = {};
  async function collect(directory) {
    for (const item of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(directory, item.name);
      if (item.isDirectory()) await collect(file);
      else if (/\.(?:js|md|json)$/.test(item.name)) files[path.relative(suiteDirectory, file)] = hash(await readFile(file));
    }
  }
  await collect(suiteDirectory);
  return { suite_files_sha256: files, suite_source_sha256: hash(JSON.stringify(files)), fixture_sha256: files['fixtures.js'], judge_sha256: files['judge.js'],
    task_catalog_sha256: hash(JSON.stringify(TASKS)) };
}

function fail(row, reason) {
  row.judgement ??= { success: false, failures: [] };
  row.judgement.success = false;
  row.judgement.failures = [...new Set([...(row.judgement.failures ?? []), reason])];
  row.status = 'failed'; row.reason = row.judgement.failures.join('; ');
}

export async function runSuite(options, { onProgress = () => {}, abortSignal } = {}) {
  const selected = options.tasks ? TASKS.filter(task => options.tasks.includes(task.id)) : TASKS;
  if (!selected.length || options.tasks?.some(id => !TASKS.some(task => task.id === id))) throw new Error('unknown or empty task selection');
  const runID = `browser-use-v2-${new Date().toISOString().replace(/[:.]/g, '-')}-${randomBytes(3).toString('hex')}`;
  const outputDir = options.outputDir ?? path.join(runtime, 'eval/runs', runID);
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 });
  await mkdir(outputDir, { mode: 0o700 });
  await mkdir(path.join(outputDir, 'traces'), { mode: 0o700 });
  const token = randomBytes(32).toString('hex'), redact = makeRedactor(options.environment ?? process.env, [token]);
  const [source, suiteSource, skillSource] = await Promise.all([sourceManifest(), suiteSourceManifest(), expectedSkillManifest()]);
  const runtimeFiles = Object.fromEntries(Object.entries(source.files).filter(([file]) => /browser-runtime\/src\//.test(file)));
  const report = { schema_version: 2, suite: 'browser-use-v2', run_id: runID, mode: options.mode, control: options.mode === 'validate' ? options.control : null,
    model: options.model, seed: options.seed, started_at: new Date().toISOString(), config: { profile: PROFILE, task_ids: selected.map(task => task.id),
      repeat: options.repeat, concurrency: 1, max_turns_per_task: options.maxTurns, token_budget_per_task: options.tokenBudget,
      time_budget_ms_per_task: options.timeoutMs, max_steps_per_task: options.maxSteps, seed_policy: '(seed + repeat_index) uint32',
      fixture_scope: 'controlled_synthetic_sites', authentication_scope: 'controller_simulated_human_lease_login_not_real_accounts',
      model_input: 'URL + business goal', final_answer: 'natural_language', oracle_transport: 'controller_memory_only',
      judge_evidence: 'recording_proxy_worker_acknowledgements_only', adapter_events_trusted: false,
      network_scope: 'task_allowed_origins_and_case_paths_only', service_workers: 'blocked', chromium_args: ['--site-per-process'],
      capture_frames: false, ...source, ...suiteSource, ...skillSource, runtime_source_sha256: hash(JSON.stringify(runtimeFiles)) }, tasks: [] };
  if (options.agent) report.config.adapter_sha256 = hash(await readFile(options.agent));
  for (let repeat = 0; repeat < options.repeat; repeat++) for (const task of selected) report.tasks.push({ id: task.id, title: task.title, category: task.category,
    repeat, seed: (options.seed + repeat) >>> 0, status: 'skipped', reason: 'not started', ledger: [], adapter_events: [], context_cleaned: null });
  const publish = async () => {
    report.summary = summarize(report);
    const visible = publicValue(report, redact);
    for (const [name, body] of [['summary.json', JSON.stringify(visible, null, 2)], ['report.html', renderReport(visible)]]) {
      await writeFile(path.join(outputDir, `.${name}.tmp`), body, { mode: 0o600 });
      await rename(path.join(outputDir, `.${name}.tmp`), path.join(outputDir, name));
    }
  };
  await publish();
  const ledgers = createTrustedLedger(), closedContexts = new WeakSet();
  let browser, worker, proxy, workerURL, platform, activeTask, activeBlocked, latestContext, halted;
  const browserEnv = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DISPLAY', 'XDG_RUNTIME_DIR', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
  try {
    if (!abortSignal?.aborted) {
      browser = await chromium.launch({ headless: true, chromiumSandbox: true, args: ['--site-per-process'], env: browserEnv });
      report.config.chromium = browser.version();
      const isolatedBrowser = { on: (...args) => browser.on(...args), isConnected: () => browser.isConnected(), async newContext(settings) {
        if (!activeTask) throw new Error('task network boundary was not installed');
        const task = activeTask, blocked = activeBlocked;
        const context = await browser.newContext({ ...settings, serviceWorkers: 'block' });
        context.on('close', () => closedContexts.add(context));
        await context.route('**/*', route => {
          const request = route.request(), url = request.url();
          if (requestAllowed(url, task)) return route.continue();
          blocked.push({ url: redact(url), method: request.method(), resource_type: request.resourceType() });
          return route.abort('blockedbyclient');
        });
        latestContext = context; return context;
      } };
      worker = await createWorker({ token, browser: isolatedBrowser, maxSessions: 2, actionTimeout: 10000, settleMs: 200, idleMs: 900000 });
      workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
      proxy = await createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames: false, onExchange: exchange => ledgers.onExchange(exchange) });
    }
    for (let repeat = 0; repeat < options.repeat; repeat++) {
      if (!abortSignal?.aborted && !halted) platform = await createFixtures({ seed: (options.seed + repeat) >>> 0 });
      for (const row of report.tasks.filter(item => item.repeat === repeat)) {
        if (abortSignal?.aborted || halted) { row.reason = abortSignal?.aborted ? 'cancelled before task start' : halted; continue; }
        const task = platform.tasks.find(item => item.id === row.id);
        if (!task) throw new Error(`fixture missing selected task ${row.id}`);
        const caseID = task.caseID ?? task.id, sessionId = `browser-use-v2-${randomUUID()}`, artifactID = `${row.id}-${repeat}`;
        const begin = Date.now(), deadline = begin + options.timeoutMs, cancellation = new AbortController();
        let record, context, adapter, cancelPromise, timedOut = false;
        row.task_prompt = task.prompt; row.task_prompt_sha256 = hash(task.prompt); row.business_goal_sha256 = hash(task.goal ?? task.prompt);
        row.start_url = task.startURL; row.layout_variant = task.layout_variant; row.reason = 'running';
        activeTask = task; activeBlocked = row.blocked_requests = []; latestContext = null;
        const interrupt = reason => {
          if (record) record.closed = true;
          cancellation.abort(reason ?? new Error('browser-use-v2 cancelled'));
          cancelPromise ??= workerRequest(workerURL, token, `/sessions/${sessionId}/cancel`, { method: 'POST', body: {}, timeout: 20000 })
            .then(result => { row.cancel_acknowledged = result.ok && result.data?.ok !== false; }, () => { row.cancel_acknowledged = false; });
        };
        const onAbort = () => interrupt(abortSignal.reason);
        const timer = setTimeout(() => { timedOut = true; interrupt(new Error('task time budget exceeded')); }, options.timeoutMs);
        abortSignal?.addEventListener('abort', onAbort, { once: true });
        const direct = async (suffix, method = 'GET', body) => {
          cancellation.signal.throwIfAborted();
          if (Date.now() >= deadline) throw new Error('task time budget exceeded');
          const response = await workerRequest(workerURL, token, `/sessions/${sessionId}${suffix}`, { method, body, timeout: Math.max(1, Math.min(20000, deadline - Date.now())) });
          if (!response.ok) throw new Error(`fixture worker operation failed: ${response.data?.error ?? response.status}`);
          return response.data;
        };
        const privateExecute = args => direct('/execute', 'POST', { ...args, op_id: randomUUID() });
        const execute = async args => {
          cancellation.signal.throwIfAborted();
          if (Date.now() >= deadline) throw new Error('task time budget exceeded');
          return proxy.execute(sessionId, args);
        };
        onProgress({ type: 'task_start', id: row.id, repeat, seed: row.seed });
        try {
          abortSignal?.throwIfAborted();
          await platform.reset(caseID);
          await direct('', 'PUT', {}); context = latestContext;
          const opened = await privateExecute({ action: 'navigate', url: task.startURL });
          if (opened.ok === false) throw new Error(`initial navigation failed: ${opened.error}`);
          row.preparation = publicValue(await platform.prepare(caseID, { execute: privateExecute, direct }), redact);
          row.server_state_before = publicValue(await platform.inspect(caseID), redact);
          const oracle = await platform.oracle(caseID);
          ledgers.register(sessionId, artifactID);
          record = proxy.register(sessionId, artifactID, options.maxSteps);
          row.trace_path = `traces/${artifactID}.json`;
          let answer = '';
          if (options.mode === 'validate') {
            if (options.control === 'reference') {
              const result = await abortable(reference(task, oracle, execute), cancellation.signal);
              answer = typeof result === 'string' ? result : result?.answer;
            } else {
              answer = await platform.expectedAnswer(caseID);
              if (options.control === 'fabricated') row.adapter_events = fabricatedControlEvents(answer);
            }
          } else {
            const remainingMs = deadline - Date.now();
            if (remainingMs < 1) throw new Error('task time budget exceeded');
            adapter = await runAdapter({ executable: options.agent, args: ['--profile', 'browser-use-v2', ...(options.providers ? ['--providers', options.providers] : [])],
              input: { prompt: task.prompt, start_url: task.startURL, worker_url: proxy.url, session_id: sessionId, model: options.model,
                max_turns: options.maxTurns, token_budget: options.tokenBudget, time_budget_ms: remainingMs },
              environment: { ...(options.environment ?? process.env), POGO_BROWSER_EVAL_TOKEN: token }, timeoutMs: remainingMs,
              redact, abortSignal: cancellation.signal, onEvent: event => row.adapter_events.push(publicValue(event, redact)) });
            answer = adapter.end?.answer;
            row.usage = adapter.usage; row.usage_partial = adapter.usage_partial;
            for (const key of ['end_reason', 'profile', 'profile_sha256', 'system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256', 'schema_bytes', 'sees_images', 'provider', 'model']) row[key] = publicValue(adapter.end?.[key], redact);
            if (!row.profile_sha256 && row.profile) row.profile_sha256 = hash(JSON.stringify({ profile: row.profile, system_prompt_sha256: row.system_prompt_sha256,
              tool_schema_sha256: row.tool_schema_sha256, shared_skill_body_sha256: row.shared_skill_body_sha256 }));
            if (adapter.diagnostic) row.adapter_events.push({ type: 'adapter_diagnostic', text: redact(adapter.diagnostic) });
          }
          row.final_answer = typeof answer === 'string' ? redact(answer) : '';
          cancellation.signal.throwIfAborted();
          const state = await platform.inspect(caseID), ledger = ledgers.snapshot(sessionId);
          row.server_state_after = publicValue(state, redact);
          row.judgement = await judgeCase({ task, oracle, fixtureState: state, ledger, finalAnswer: row.final_answer });
          if (!row.judgement || typeof row.judgement.success !== 'boolean' || !Array.isArray(row.judgement.failures)) throw new Error('invalid judge result');
          row.status = row.judgement.success ? 'passed' : 'failed';
          row.reason = row.judgement.success ? 'worker evidence, service state and natural answer verified' : row.judgement.failures.join('; ');
          if (!ledger.some(exchange => exchange.ok === true)) fail(row, 'no_real_successful_browser_action');
          if (options.mode === 'model') for (const reason of adapterFailures(adapter, options, skillSource)) fail(row, reason);
          if (timedOut || Date.now() >= deadline) fail(row, 'task_time_budget_exceeded');
          if (record.stepLimit) fail(row, 'step_budget_exceeded');
          if (record.evidenceError) throw new Error('trusted_worker_evidence_unavailable');
          if (activeBlocked.length) fail(row, 'network_boundary_violation');
        } catch (error) {
          fail(row, timedOut ? 'task_time_budget_exceeded' : abortSignal?.aborted ? 'evaluation_cancelled' : redact(error.message));
          row.status = 'error';
          if (adapter) { row.usage = adapter.usage; row.usage_partial = true; }
          try { row.server_state_after = publicValue(await platform.inspect(caseID), redact); } catch { /* Preserve the original failure. */ }
        } finally {
          clearTimeout(timer); abortSignal?.removeEventListener('abort', onAbort); await cancelPromise;
          if (row.cancel_acknowledged === false) { fail(row, 'cancellation_not_acknowledged'); row.status = 'error'; halted = row.reason; }
          if (record) {
            record.closed = true;
            row.ledger = publicValue(ledgers.snapshot(sessionId), redact);
            row.ledger_sha256 = hash(JSON.stringify(row.ledger));
            row.steps = record.steps; row.requested_steps = record.requestedSteps; row.browser_requests = record.calls;
            row.step_budget_exceeded = record.stepLimit;
          }
          try {
            const cleaned = await workerRequest(workerURL, token, `/sessions/${sessionId}`, { method: 'DELETE', timeout: 20000 });
            row.context_cleaned = cleaned.ok && cleaned.data?.ok !== false && (!context || closedContexts.has(context));
          } catch { row.context_cleaned = false; }
          if (!row.context_cleaned) { fail(row, 'context_cleanup_failed'); row.status = 'error'; halted = 'context cleanup failed'; }
          if (record?.stepLimit) { const status = row.status; fail(row, 'step_budget_exceeded'); if (status === 'error') row.status = 'error'; }
          if (record?.evidenceError) { fail(row, 'trusted_worker_evidence_unavailable'); row.status = 'error'; halted = row.reason; report.infrastructure_error = row.reason; }
          proxy.unregister(sessionId); ledgers.unregister(sessionId); activeTask = null; latestContext = null;
          row.duration_ms = Date.now() - begin;
          if (row.trace_path) await writeFile(path.join(outputDir, row.trace_path), JSON.stringify({ ledger: row.ledger, adapter_events: publicValue(row.adapter_events, redact) }, null, 2), { mode: 0o600 });
          await publish();
          onProgress({ type: 'task_end', id: row.id, repeat, seed: row.seed, status: row.status, reason: row.reason });
        }
      }
      await platform?.close(); platform = null;
    }
  } catch (error) { report.infrastructure_error = redact(error.message); }
  finally {
    report.cancelled = abortSignal?.aborted ?? false;
    for (const [name, resource] of [['proxy', proxy], ['worker', worker], ['browser', browser], ['fixtures', platform]]) {
      try { await resource?.close(); }
      catch (error) { report.infrastructure_error = `${report.infrastructure_error ? report.infrastructure_error + '; ' : ''}${name} cleanup failed: ${redact(error.message)}`; }
    }
    report.completed_at = new Date().toISOString(); await publish();
  }
  return { report, outputDir };
}

async function abortable(promise, signal) {
  let interrupt;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      interrupt = () => reject(signal.reason ?? new Error('evaluation cancelled'));
      signal.addEventListener('abort', interrupt, { once: true }); if (signal.aborted) interrupt();
    })]);
  } finally { signal.removeEventListener('abort', interrupt); }
}
