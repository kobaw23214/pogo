import { randomBytes, randomUUID, createHash } from 'node:crypto';
import { readFile, mkdir, writeFile, rename, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { createWorker } from '../../src/server.js';
import { createRecordingProxy, workerRequest, makeRedactor } from '../transport.js';
import { runAdapter } from '../adapter.js';
import { sourceManifest } from '../runner.js';
import { createGeneralWebFixtures, TASKS } from './fixtures/index.js';
import { summarize, renderReport } from './report.js';

const runtime = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const suiteDirectory = path.dirname(fileURLToPath(import.meta.url));
export const PROFILE = 'pogo-agent-general-web-v1';
const hash = value => createHash('sha256').update(value).digest('hex');
const sharedSkillFile = path.resolve(runtime, '../skills/builtin/browser-use/SKILL.md');
const integer = (value, min, max, name) => {
  const number = Number(value); if (!Number.isInteger(number) || number < min || number > max) throw new Error(`${name} must be ${min}..${max}`); return number;
};

export function parseOptions(argv) {
  const options = { mode: 'validate', control: 'reference', seed: 20261003, repeat: 1, model: null, agent: null, providers: null, outputDir: null,
    tasks: null, maxTurns: 40, tokenBudget: 60000, timeoutMs: 180000, maxSteps: 50 };
  for (let index = 0; index < argv.length; index++) {
    const name = argv[index];
    if (name === '--help' || name === '--list') { options[name.slice(2)] = true; continue; }
    const value = argv[++index]; if (!value || value.startsWith('--')) throw new Error(`${name} requires a value`);
    if (['--mode', '--control', '--model'].includes(name)) options[name.slice(2)] = value;
    else if (['--agent', '--providers', '--out'].includes(name)) options[name === '--out' ? 'outputDir' : name.slice(2)] = path.resolve(value);
    else if (name === '--tasks') options.tasks = value.split(',').filter(Boolean);
    else if (name === '--seed') options.seed = integer(value, 0, 2 ** 32 - 1, name);
    else if (name === '--repeat') options.repeat = integer(value, 1, 10, name);
    else if (name === '--max-turns') options.maxTurns = integer(value, 1, 100, name);
    else if (name === '--token-budget') options.tokenBudget = integer(value, 1, 200000, name);
    else if (name === '--timeout-ms') options.timeoutMs = integer(value, 1000, 600000, name);
    else if (name === '--max-steps') options.maxSteps = integer(value, 1, 100, name);
    else throw new Error(`unknown option ${name}`);
  }
  if (!['validate', 'model'].includes(options.mode) || !['reference', 'noop'].includes(options.control)) throw new Error('mode must be validate|model; control must be reference|noop');
  if (options.mode === 'model' && options.control !== 'reference') throw new Error('noop is only a validation control');
  if (!options.help && !options.list && options.mode === 'model' && (!options.model || !options.agent)) throw new Error('model mode requires explicit --agent and --model');
  if (options.tasks && (!options.tasks.length || new Set(options.tasks).size !== options.tasks.length || options.tasks.some(id => !TASKS.some(task => task.id === id)))) throw new Error('unknown, empty or duplicate task selection');
  return options;
}

// Redact keys as well as values. Website/model data can contain a secret in an
// object key, and screenshot bytes never belong in public JSON artifacts.
export function safeGeneralValue(value, redact) {
  if (Array.isArray(value)) return value.slice(0, 1000).map(item => safeGeneralValue(item, redact));
  if (value && typeof value === 'object') {
    const result = Object.create(null);
    for (const [key, item] of Object.entries(value)) {
      if (/^(?:authorization|api_?key|lease_token|worker_token|data_url|dataurl|headers)$/i.test(key)) continue;
      if (key === 'screenshot' && item && typeof item === 'object') {
        const { data, ...metadata } = item; result[key] = safeGeneralValue(metadata, redact);
      } else result[redact(key).slice(0, 2000)] = safeGeneralValue(item, redact);
    }
    return result;
  }
  return typeof value === 'string' ? redact(value).slice(0, 64000) : value;
}

export function requestAllowed(rawURL, task) {
  try {
    const url = new URL(rawURL);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return false;
    const pathname = decodeURIComponent(url.pathname);
    if (/(?:^|\/)(?:eval|oracle|controller|expected|judge|__[^/]*)(?:\/|$)/i.test(pathname)) return false;
    const origins = task.allowedOrigins ?? [new URL(task.startURL).origin];
    const prefixes = task.allowedPathPrefixes ?? [new URL(task.startURL).pathname];
    return origins.includes(url.origin) && prefixes.some(prefix => pathname.startsWith(prefix));
  } catch { return false; }
}

// Only worker acknowledgements are evidence. A failed/stopped batch does not
// make its unexecuted requested children real actions.
export function executedActions(events) {
  const actions = [], pending = [];
  for (const event of events) {
    if (event.type === 'browser_start') {
      // The shared proxy does not attach a call ID to browser_end. Refuse
      // overlapping requests rather than pairing an acknowledgement to the
      // wrong requested action. The model adapter uses serial browser calls.
      if (pending.length) return [];
      pending.push(event.args);
    }
    if (event.type !== 'browser_end') continue;
    const args = pending.shift(); if (!args) continue;
    const result = event.result ?? {};
    if (args.action === 'batch') {
      for (const [index, step] of (result.steps ?? []).entries()) {
        const item = args.actions?.[index];
        if (item && item.action === step.action) actions.push({ ...item, ok: step.ok === true, error: step.error ?? '',
          result: { ...step, ...(index === result.steps.length - 1 ? { text: result.text, url: result.url } : {}) } });
      }
    } else actions.push({ ...args, ok: event.ok === true, error: result.error ?? '', result });
  }
  return actions;
}

export async function suiteSourceManifest() {
  const files = {};
  async function collect(directory) {
    for (const item of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(directory, item.name);
      if (item.isDirectory()) await collect(file);
      else if (/\.(?:js|md|html|css)$/.test(item.name)) files[path.relative(suiteDirectory, file)] = hash(await readFile(file));
    }
  }
  await collect(suiteDirectory);
  return { suite_files_sha256: files, suite_source_sha256: hash(JSON.stringify(files)), task_catalog_sha256: hash(JSON.stringify(TASKS)) };
}

// Keep the body boundary byte-for-byte compatible with Go ParseFrontmatter.
// In particular, normalize CR/BOM and preserve the body's trailing newline.
export function sharedSkillBody(content) {
  const text = String(content).replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/^\uFEFF/, '');
  if (!text.startsWith('---\n') && text !== '---') return text;
  const lines = text.slice(3).replace(/^\n/, '').split('\n');
  const end = lines.findIndex(line => ['---', '...'].includes(line.replace(/[ \t]+$/, '')));
  return end < 0 ? text : lines.slice(end + 1).join('\n');
}

export async function expectedSkillManifest() {
  const bytes = await readFile(sharedSkillFile);
  return { shared_skill_file: 'pogo/skills/builtin/browser-use/SKILL.md', shared_skill_file_sha256: hash(bytes),
    expected_shared_skill_body_sha256: hash(sharedSkillBody(bytes.toString('utf8'))) };
}

export async function runSuite(options, { onProgress = () => {}, abortSignal } = {}) {
  const selected = options.tasks ? TASKS.filter(task => options.tasks.includes(task.id)) : TASKS;
  if (!selected.length || options.tasks?.some(id => !TASKS.some(task => task.id === id))) throw new Error('unknown or empty task selection');
  const runID = `general-web-${new Date().toISOString().replace(/[:.]/g, '-')}-${randomBytes(3).toString('hex')}`;
  const outputDir = options.outputDir ?? path.join(runtime, 'eval/runs', runID);
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 });
  await mkdir(outputDir, { mode: 0o700 }); // Refuse replacing an existing run.
  await mkdir(path.join(outputDir, 'traces'), { mode: 0o700 });
  const token = randomBytes(32).toString('hex'), redact = makeRedactor(options.environment ?? process.env, [token]);
  const source = await sourceManifest(), suiteSource = await suiteSourceManifest(), skillSource = await expectedSkillManifest();
  const report = { schema_version: 1, suite: 'general-web-v1', run_id: runID, mode: options.mode, control: options.mode === 'validate' ? options.control : null,
    model: options.model, seed: options.seed, started_at: new Date().toISOString(), config: { profile: PROFILE, task_ids: selected.map(task => task.id),
      repeat: options.repeat, concurrency: 1, seed_policy: '(seed + repeat_index) uint32', max_turns_per_task: options.maxTurns,
      token_budget_per_task: options.tokenBudget, time_budget_ms_per_task: options.timeoutMs, max_steps_per_task: options.maxSteps,
      capture_frames: false, fixture_scope: 'controlled_synthetic_three_origins', model_input: 'URL + business goal', final_answer: 'natural_language',
      network_scope: 'task_allowed_origins_and_case_paths_only', oracle_transport: 'controller_node_memory_only', human_assistance: 'none', ...source, ...suiteSource, ...skillSource }, tasks: [] };
  if (options.agent) report.config.adapter_sha256 = createHash('sha256').update(await readFile(options.agent)).digest('hex');
  for (let repeat = 0; repeat < options.repeat; repeat++) for (const task of selected) report.tasks.push({ id: task.id, title: task.title, category: task.category,
    site: task.site ?? task.family, repeat, seed: (options.seed + repeat) >>> 0, status: 'skipped', reason: 'not started', events: [], context_cleaned: null });
  const publish = async () => {
    report.config.observed_profiles = [...new Map(report.tasks.filter(row => row.profile).map(row => [row.profile_sha256 ?? row.system_prompt_sha256 ?? row.profile,
      { profile: row.profile, profile_sha256: row.profile_sha256, system_prompt_sha256: row.system_prompt_sha256,
        tool_schema_sha256: row.tool_schema_sha256, shared_skill_body_sha256: row.shared_skill_body_sha256 }])).values()];
    report.summary = summarize(report);
    const publicReport = safeGeneralValue(report, redact);
    for (const [name, body] of [['summary.json', JSON.stringify(publicReport, null, 2)], ['report.html', renderReport(publicReport)]]) {
      await writeFile(path.join(outputDir, `.${name}.tmp`), body, { mode: 0o600 });
      await rename(path.join(outputDir, `.${name}.tmp`), path.join(outputDir, name));
    }
  };
  await publish();
  let browser, worker, proxy, workerURL, platform, activeTask, activeBlocked, latestContext, halted;
  const closedContexts = new WeakSet();
  const browserEnv = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DISPLAY', 'XDG_RUNTIME_DIR', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
  try {
    if (!abortSignal?.aborted) {
      browser = await chromium.launch({ headless: true, chromiumSandbox: true, env: browserEnv }); report.config.chromium = browser.version();
      const isolatedBrowser = { on: (...args) => browser.on(...args), isConnected: () => browser.isConnected(), async newContext(settings) {
        if (!activeTask) throw new Error('task network boundary was not installed');
        const task = activeTask, blocked = activeBlocked;
        const context = await browser.newContext({ ...settings, serviceWorkers: 'block' });
        context.on('close', () => closedContexts.add(context));
        await context.route('**/*', route => {
          const url = route.request().url();
          if (requestAllowed(url, task)) return route.continue();
          blocked.push({ url: redact(url), method: route.request().method(), resource_type: route.request().resourceType() });
          return route.abort('blockedbyclient');
        });
        latestContext = context; return context;
      } };
      worker = await createWorker({ token, browser: isolatedBrowser, maxSessions: 2, actionTimeout: 10000, settleMs: 200, idleMs: 900000 });
      workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
      proxy = await createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames: false });
    }
    for (let repeat = 0; repeat < options.repeat; repeat++) {
      if (!abortSignal?.aborted && !halted) platform = await createGeneralWebFixtures({ seed: (options.seed + repeat) >>> 0 });
      for (const row of report.tasks.filter(item => item.repeat === repeat)) {
        if (abortSignal?.aborted || halted) { row.reason = abortSignal?.aborted ? 'cancelled before task start' : halted; continue; }
        const task = platform.tasks.find(item => item.id === row.id);
        if (!task) throw new Error(`fixture missing selected task ${row.id}`);
        const caseID = task.caseID ?? task.id, sessionId = `general-web-${randomUUID()}`, artifactID = `${row.id}-${row.repeat}`;
        const begin = Date.now(), deadline = begin + options.timeoutMs, cancellation = new AbortController();
        row.reason = 'running'; row.layout_variant = task.layout_variant; row.site = task.site ?? task.family; row.task_prompt = task.prompt;
        row.task_prompt_sha256 = hash(task.prompt); row.business_goal_sha256 = hash(task.goal);
        activeTask = task; activeBlocked = row.blocked_requests = []; latestContext = null;
        let record, context, timedOut = false, cancelPromise, answer = '', adapter;
        const interrupt = reason => {
          if (record) record.closed = true;
          cancellation.abort(reason ?? new Error('general web evaluation cancelled'));
          cancelPromise ??= workerRequest(workerURL, token, `/sessions/${sessionId}/cancel`, { method: 'POST', body: {}, timeout: 20000 }).catch(() => {});
        };
        const onAbort = () => interrupt(abortSignal.reason);
        const taskTimer = setTimeout(() => { timedOut = true; interrupt(new Error('task time budget exceeded')); }, options.timeoutMs);
        abortSignal?.addEventListener('abort', onAbort, { once: true });
        const direct = async (suffix, method = 'GET', body) => {
          cancellation.signal.throwIfAborted();
          if (Date.now() >= deadline) throw new Error('task time budget exceeded');
          const result = await workerRequest(workerURL, token, `/sessions/${sessionId}${suffix}`, { method, body, timeout: Math.max(1, Math.min(20000, deadline - Date.now())) });
          if (!result.ok || result.data?.ok === false) throw new Error(`fixture operation failed: ${result.data?.error ?? result.status}`);
          return result.data;
        };
        const execute = async args => {
          cancellation.signal.throwIfAborted(); if (Date.now() >= deadline) throw new Error('task time budget exceeded');
          const value = await proxy.execute(sessionId, args);
          if (!value.ok) throw new Error(value.error ?? 'reference browser action failed'); return value;
        };
        onProgress({ type: 'task_start', id: row.id, repeat, seed: row.seed, layout_variant: row.layout_variant });
        try {
          abortSignal?.throwIfAborted(); await platform.reset(caseID);
          await direct('', 'PUT', {}); context = latestContext;
          await direct('/execute', 'POST', { action: 'navigate', url: task.startURL, op_id: randomUUID() });
          row.server_state_before = await platform.inspect(caseID);
          record = proxy.register(sessionId, artifactID, options.maxSteps); row.trace_path = `traces/${artifactID}.json`;
          if (options.mode === 'validate') {
            if (options.control === 'noop') answer = await platform.expectedAnswer(caseID);
            else answer = await abortable(reference(task, platform.referenceData(caseID), execute), cancellation.signal);
          } else {
            const remainingMs = deadline - Date.now(); if (remainingMs < 1) throw new Error('task time budget exceeded');
            adapter = await runAdapter({ executable: options.agent, args: ['--profile', 'general-web', ...(options.providers ? ['--providers', options.providers] : [])],
              input: { prompt: task.prompt, start_url: task.startURL, worker_url: proxy.url, session_id: sessionId, model: options.model,
                max_turns: options.maxTurns, token_budget: options.tokenBudget, time_budget_ms: remainingMs },
              environment: { ...(options.environment ?? process.env), POGO_BROWSER_EVAL_TOKEN: token }, timeoutMs: remainingMs,
              redact, abortSignal: cancellation.signal, onEvent: event => record.events.push({ ...safeGeneralValue(event, redact), at_ms: Date.now() - record.started }) });
            answer = adapter.end?.answer ?? ''; row.usage = adapter.usage; row.usage_partial = adapter.usage_partial;
            for (const key of ['end_reason', 'profile', 'profile_sha256', 'skill_sha256', 'skill_body_sha256', 'shared_skill_body_sha256', 'neutral_skill_sha256', 'neutral_skill_profile',
              'system_prompt_sha256', 'tool_schema_sha256', 'schema_bytes', 'sees_images', 'provider', 'model']) row[key] = safeGeneralValue(adapter.end?.[key], redact);
            if (!row.profile_sha256 && row.profile && row.system_prompt_sha256 && row.tool_schema_sha256 && row.shared_skill_body_sha256) row.profile_sha256 = hash(JSON.stringify({
              profile: row.profile, system_prompt_sha256: row.system_prompt_sha256, tool_schema_sha256: row.tool_schema_sha256, shared_skill_body_sha256: row.shared_skill_body_sha256 }));
            if (adapter.diagnostic) record.events.push({ type: 'adapter_diagnostic', text: redact(adapter.diagnostic) });
          }
          row.final_answer = typeof answer === 'string' ? redact(answer) : '';
          cancellation.signal.throwIfAborted();
          row.server_state_after = await platform.inspect(caseID);
          row.judgement = await platform.judge(caseID, { answer: row.final_answer, actions: executedActions(record.events), before: row.server_state_before, after: row.server_state_after });
          row.judgement.failures ??= [];
          const fail = reason => { row.judgement.success = false; row.judgement.failures.push(reason); };
          if (!executedActions(record.events).some(action => action.ok)) fail('no_real_successful_browser_action');
          if (adapter && (adapter.error || adapter.exitCode !== 0 || adapter.timedOut || adapter.aborted || adapter.end?.error || adapter.end?.profile !== PROFILE || adapter.end?.end_reason !== 'completed')) fail(adapter.end?.profile !== PROFILE ? 'wrong_or_missing_adapter_profile' : 'adapter_not_completed');
          if (adapter && ['system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256'].some(key => !/^[a-f0-9]{64}$/.test(adapter.end?.[key] ?? ''))) fail('missing_or_invalid_profile_fingerprints');
          if (adapter && adapter.end?.shared_skill_body_sha256 !== skillSource.expected_shared_skill_body_sha256) fail('shared_skill_body_mismatch');
          if (adapter && (!adapter.usage || typeof adapter.usage.input !== 'number' || typeof adapter.usage.output !== 'number')) fail('missing_measured_token_usage');
          if (adapter && adapter.usage && (adapter.usage.input ?? 0) + (adapter.usage.output ?? 0) > options.tokenBudget) fail('token_budget_exceeded');
          if (timedOut || Date.now() >= deadline) fail('task_time_budget_exceeded');
          if (record.stepLimit) fail('step_budget_exceeded');
          row.status = row.judgement.success ? 'passed' : 'failed';
          row.reason = row.judgement.success ? 'natural answer, actual browser actions and server business state verified' : row.judgement.failures.join('; ');
        } catch (error) {
          row.status = 'error'; row.reason = timedOut ? 'task time budget exceeded' : abortSignal?.aborted ? 'general web evaluation cancelled' : redact(error.message);
          if (adapter) { row.usage = adapter.usage; row.usage_partial = true; }
          try { row.server_state_after = await platform.inspect(caseID); } catch { /* original execution error remains primary */ }
        } finally {
          clearTimeout(taskTimer); abortSignal?.removeEventListener('abort', onAbort); await cancelPromise;
          row.duration_ms = Date.now() - begin;
          if (record) { record.closed = true; row.events = safeGeneralValue(record.events, redact); row.steps = record.steps; row.requested_steps = record.requestedSteps; row.browser_requests = record.calls; row.step_budget_exceeded = record.stepLimit; }
          try { const cleaned = await workerRequest(workerURL, token, `/sessions/${sessionId}`, { method: 'DELETE', timeout: 20000 }); row.context_cleaned = cleaned.ok && cleaned.data?.ok !== false && (!context || closedContexts.has(context)); }
          catch { row.context_cleaned = false; }
          if (!row.context_cleaned) { halted = 'context cleanup failed'; row.status = 'error'; row.reason = halted; }
          proxy.unregister(sessionId); activeTask = null; latestContext = null;
          if (row.trace_path) await writeFile(path.join(outputDir, row.trace_path), JSON.stringify(row.events, null, 2), { mode: 0o600 });
          await publish(); onProgress({ type: 'task_end', id: row.id, repeat, seed: row.seed, status: row.status, reason: row.reason });
        }
      }
      await platform?.close(); platform = null;
    }
  } catch (error) { report.infrastructure_error = redact(error.message); }
  finally {
    report.cancelled = abortSignal?.aborted ?? false;
    await proxy?.close().catch(() => {}); await worker?.close().catch(() => {}); await browser?.close().catch(() => {}); await platform?.close().catch(() => {});
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

// Controller-owned reference data supplies only the deterministic positive
// control. It is never serialized into the adapter's model input.
export async function reference(task, data, execute) {
  if (typeof data?.solve === 'function') return data.solve(execute);
  if (Array.isArray(data?.actions)) {
    for (const action of data.actions) await execute(action);
    return data.answer;
  }
  throw new Error(`reference data is unavailable for ${task.id}`);
}
