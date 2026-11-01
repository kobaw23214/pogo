import { randomBytes, randomUUID, createHash } from 'node:crypto';
import { mkdir, writeFile, rename, readdir, readFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';
import { TASKS, createFixtureServer } from './fixtures/index.js';
import { createRecordingProxy, workerRequest, makeRedactor, safeValue } from './transport.js';
import { runAdapter, failureCategory } from './adapter.js';
import { renderReport, summarize } from './report.js';

const runtimeDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const repoDir = path.resolve(runtimeDir, '../..');
const boundedInt = (value, min, max, name) => {
  const result = Number(value);
  if (!Number.isInteger(result) || result < min || result > max) throw new Error(`${name} must be an integer from ${min} to ${max}`);
  return result;
};

export function parseOptions(argv) {
  const options = { mode: 'fixture-validation', model: null, seed: 20261001, repeat: 1, concurrency: 2,
    maxTurns: 40, tokenBudget: 100000, timeoutMs: 180000, totalTokens: 0, totalTimeMs: 0,
    captureFrames: true, control: 'reference', tasks: null, outputDir: null, agent: null, providers: null };
  const value = (at, name) => { if (argv[at + 1] === undefined || argv[at + 1].startsWith('--')) throw new Error(`${name} requires a value`); return argv[at + 1]; };
  for (let i = 0; i < argv.length; i++) {
    const name = argv[i];
    if (name === '--no-frames') { options.captureFrames = false; continue; }
    if (name === '--help' || name === '--list') { options[name.slice(2)] = true; continue; }
    const item = value(i, name); i++;
    switch (name) {
      case '--mode': options.mode = item === 'validate' ? 'fixture-validation' : item === 'model' ? 'model-evaluation' : item; break;
      case '--model': options.model = item; break;
      case '--agent': options.agent = path.resolve(item); break;
      case '--providers': options.providers = path.resolve(item); break;
      case '--out': options.outputDir = path.resolve(item); break;
      case '--tasks': options.tasks = item.split(',').filter(Boolean); break;
      case '--seed': options.seed = boundedInt(item, 0, 2 ** 32 - 1, name); break;
      case '--repeat': options.repeat = boundedInt(item, 1, 10, name); break;
      case '--concurrency': options.concurrency = boundedInt(item, 1, 4, name); break;
      case '--max-turns': options.maxTurns = boundedInt(item, 1, 200, name); break;
      case '--token-budget': options.tokenBudget = boundedInt(item, 1, 1000000, name); break;
      case '--timeout-ms': options.timeoutMs = boundedInt(item, 1000, 600000, name); break;
      case '--total-tokens': options.totalTokens = boundedInt(item, 0, 100000000, name); break;
      case '--total-time-ms': options.totalTimeMs = boundedInt(item, 0, 86400000, name); break;
      case '--control': options.control = item; break;
      default: throw new Error(`unknown option ${name}`);
    }
  }
  if (!['fixture-validation', 'model-evaluation'].includes(options.mode)) throw new Error('mode must be validate or model');
  if (!['reference', 'noop'].includes(options.control)) throw new Error('control must be reference or noop');
  if (!options.help && !options.list && options.mode === 'model-evaluation' && (!options.model || !options.agent)) throw new Error('model evaluation requires explicit --model and --agent');
  if (options.mode === 'model-evaluation' && options.control !== 'reference') throw new Error('control is only available in validation mode');
  return options;
}

export async function sourceManifest() {
  const entries = [];
  async function collect(directory) {
    for (const item of await readdir(directory, { withFileTypes: true })) {
      if (['runs', 'node_modules', '.git'].includes(item.name)) continue;
      const file = path.join(directory, item.name);
      if (item.isDirectory()) await collect(file);
      else if (/\.(js|go|json)$/.test(item.name)) entries.push(file);
    }
  }
  for (const relative of ['src', 'eval', '../cmd/browser-eval-agent']) await collect(path.resolve(runtimeDir, relative));
  // Include the compiled Agent and tool dependencies, which may have local
  // changes not represented by HEAD. Never read private provider JSON files.
  async function collectGo(directory) {
    for (const item of await readdir(directory, { withFileTypes: true })) {
      if (['node_modules', '.git', 'browser-runtime', 'ui', 'vendor'].includes(item.name)) continue;
      const file = path.join(directory, item.name);
      if (item.isDirectory()) await collectGo(file);
      else if (/\.go$/.test(item.name) || ['go.mod', 'go.sum'].includes(item.name)) entries.push(file);
    }
  }
  await collectGo(path.resolve(runtimeDir, '..'));
  entries.push(path.join(runtimeDir, 'package.json'), path.join(runtimeDir, 'package-lock.json'));
  const files = {};
  for (const file of entries.sort()) files[path.relative(repoDir, file)] = createHash('sha256').update(await readFile(file)).digest('hex');
  let gitRevision = null;
  try { gitRevision = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repoDir, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim(); } catch { /* source snapshots still identify this run */ }
  return { git_revision: gitRevision, source_sha256: createHash('sha256').update(JSON.stringify(files)).digest('hex'), files };
}

export async function runSuite(options, { onProgress = () => {}, abortSignal } = {}) {
  const selected = options.tasks ? TASKS.filter(task => options.tasks.includes(task.id)) : TASKS;
  if (!selected.length || options.tasks?.some(id => !TASKS.some(task => task.id === id))) throw new Error('task selection contains an unknown or empty task list');
  const runID = `${new Date().toISOString().replace(/[:.]/g, '-')}-${randomBytes(3).toString('hex')}`;
  const outputDir = options.outputDir ?? path.join(runtimeDir, 'eval/runs', runID);
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 });
  await mkdir(outputDir, { mode: 0o700 }); // Refuse overwriting an existing run.
  await mkdir(path.join(outputDir, 'traces'), { mode: 0o700 });
  const token = randomBytes(32).toString('hex');
  const redact = makeRedactor(options.environment ?? process.env, [token]);
  const source = await sourceManifest();
  const report = { schema_version: 1, run_id: runID, mode: options.mode, started_at: new Date().toISOString(), seed: options.seed, model: options.model,
    config: { profile: 'pogo-agent-browser-only', task_ids: selected.map(task => task.id), repeat: options.repeat, concurrency: options.concurrency,
      max_turns: options.maxTurns, token_budget: options.tokenBudget, time_budget_ms: options.timeoutMs,
      total_token_start_threshold: options.totalTokens || null, total_time_start_threshold_ms: options.totalTimeMs || null,
      runtime: { node: process.version, platform: process.platform, arch: process.arch,
        playwright: JSON.parse(await readFile(path.join(runtimeDir, 'node_modules/playwright/package.json'), 'utf8')).version },
      human_assistance: 'disabled', external_web_access: 'blocked', capture_frames: options.captureFrames,
      validation_control: options.mode === 'fixture-validation' ? options.control : null, ...source }, tasks: [] };
  if (options.agent) report.config.adapter_sha256 = createHash('sha256').update(await readFile(options.agent)).digest('hex');
  for (let repeat = 0; repeat < options.repeat; repeat++) for (const task of selected) report.tasks.push({ id: task.id, title: task.title, category: task.category, repeat,
    status: 'skipped', success: null, reason: 'not started', duration_ms: null, steps: null, human_interventions: null,
    duplicate_submissions: null, usage: null, cost: null, cost_unit: null, events: [], frames: [] });
  let writeQueue = Promise.resolve();
  const publish = () => {
    writeQueue = writeQueue.then(async () => {
      report.summary = summarize(report.tasks, report.mode);
      for (const [name, body] of [['summary.json', JSON.stringify(report, null, 2)], ['report.html', renderReport(report)]]) {
        await writeFile(path.join(outputDir, `.${name}.tmp`), body, { mode: 0o600 });
        await rename(path.join(outputDir, `.${name}.tmp`), path.join(outputDir, name));
      }
    });
    return writeQueue;
  };
  await publish();
  let fixture, browser, worker, proxy, workerURL, halted = null, next = 0, usedTokens = 0;
  const started = Date.now();
  const browserEnv = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DISPLAY', 'XDG_RUNTIME_DIR', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
  try {
    fixture = await createFixtureServer({ seed: options.seed });
    const port = new URL(fixture.origin).port;
    browser = await chromium.launch({ headless: true, chromiumSandbox: true, env: browserEnv });
    report.config.runtime.chromium = browser.version();
    const isolatedBrowser = {
      on: (...args) => browser.on(...args), isConnected: () => browser.isConnected(),
      async newContext(settings) {
        const context = await browser.newContext(settings);
        await context.route('**/*', route => {
          const url = new URL(route.request().url());
          return url.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) && url.port === port ? route.continue() : route.abort('blockedbyclient');
        });
        return context;
      },
    };
    worker = await createWorker({ token, browser: isolatedBrowser, maxSessions: options.concurrency + 1, actionTimeout: 12000, idleMs: 900000 });
    workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
    proxy = await createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames: options.captureFrames });
    const runCase = async row => {
      const task = selected.find(task => task.id === row.id);
      const item = await fixture.createCase(row.id, row.repeat);
      const sessionId = `eval-${randomUUID()}`;
      const artifactId = `${row.id}-${row.repeat}`;
      if (!/^[a-zA-Z0-9_-]+$/.test(artifactId)) throw new Error('task id is not a safe artifact name');
      const record = proxy.register(sessionId, artifactId, task.max_steps ?? 40);
      const begin = Date.now();
      const timeoutMs = Math.min(options.timeoutMs, task.timeout_ms ?? options.timeoutMs);
      row.reason = 'running';
      row.task_prompt = item.prompt;
      row.max_steps = record.maxSteps;
      row.time_budget_ms = timeoutMs;
      row.trace_path = `traces/${artifactId}.json`;
      let answer = '', runError = null, result = null, timedOut = false, referenceTimer, abortReference;
      onProgress({ type: 'task_start', id: row.id, repeat: row.repeat });
      try {
        abortSignal?.throwIfAborted();
        const created = await workerRequest(workerURL, token, `/sessions/${sessionId}`, { method: 'PUT', body: {} });
        if (!created.ok) throw new Error('evaluation browser context could not be created');
        const navigated = await workerRequest(workerURL, token, `/sessions/${sessionId}/execute`, { method: 'POST', body: { action: 'navigate', url: item.startURL, op_id: randomUUID() } });
        if (!navigated.ok || navigated.data?.ok !== true) throw new Error(`fixture setup failed: ${navigated.data?.error ?? navigated.status}`);
        await proxy.capture(record, 0);
        abortSignal?.throwIfAborted();
        if (options.mode === 'fixture-validation') {
          if (options.control === 'noop') answer = 'I completed every requested task successfully.';
          else {
            const solved = await Promise.race([item.solve(async args => {
              if (record.closed) throw new Error('evaluation stopped');
              return proxy.execute(sessionId, args);
            }), new Promise((_, reject) => { referenceTimer = setTimeout(() => { timedOut = true; record.closed = true; reject(new Error('reference plan exceeded its time budget')); }, timeoutMs); }),
            new Promise((_, reject) => {
              abortReference = () => { record.closed = true; reject(new Error('evaluation cancelled')); };
              abortSignal?.addEventListener('abort', abortReference, { once: true });
              if (abortSignal?.aborted) abortReference();
            })]);
            answer = typeof solved?.answer === 'string' ? solved.answer : JSON.stringify(solved?.answer ?? '');
          }
        } else {
          result = await runAdapter({ executable: options.agent, args: options.providers ? ['--providers', options.providers] : [],
            input: { prompt: item.prompt, start_url: item.startURL, worker_url: proxy.url, session_id: sessionId,
              max_turns: options.maxTurns, token_budget: options.tokenBudget, time_budget_ms: timeoutMs, model: options.model },
            environment: { ...(options.environment ?? process.env), POGO_BROWSER_EVAL_TOKEN: token }, timeoutMs: timeoutMs + 20000, redact, abortSignal,
            onEvent: event => record.events.push({ ...safeValue(event, redact), at_ms: Date.now() - record.started }),
          });
          answer = result.end?.answer ?? '';
          row.end_reason = result.end?.end_reason ?? null;
          row.usage = result.usage;
          row.usage_partial = result.usage_partial;
          const end = result.end;
          if (end) {
            for (const key of ['model', 'provider', 'profile', 'sees_images', 'system_prompt_sha256', 'context_window', 'max_tokens', 'compatibility', 'tool_schema_sha256', 'schema_bytes']) if (end[key] !== undefined) row[key] = safeValue(end[key], redact);
            if (typeof end.cost === 'number' && Number.isFinite(end.cost) && end.cost >= 0) { row.cost = end.cost; row.cost_unit = end.cost_unit ?? 'configured'; }
          }
          timedOut = result.timedOut || /time_budget|timeout/.test(row.end_reason ?? '');
          runError = result.aborted ? 'evaluation cancelled' : result.error || (!end ? 'model adapter ended without a final result' : null);
          if (result.exitCode !== 0 && !runError) runError = end?.error ?? `model adapter failed (${result.exitCode})`;
          if (result.diagnostic) record.events.push({ type: 'adapter_diagnostic', text: result.diagnostic });
        }
        const judgement = await item.judge({ answer });
        row.goal_satisfied = judgement.success === true;
        row.duplicate_submissions = Number(judgement.duplicate_submissions) || 0;
        const budget = record.stepLimit || /turn_limit|token_budget|cost_budget/.test(row.end_reason ?? '');
        row.status = timedOut ? 'timeout' : budget ? 'budget_exceeded' : runError ? 'error' : judgement.success ? 'passed' : 'failed';
        row.success = row.status === 'passed';
        row.reason = redact(timedOut ? 'task time limit exceeded' : budget ? 'declared task budget exceeded' : runError ?? judgement.reason ?? (row.success ? 'goal verified' : 'goal not achieved'));
      } catch (error) {
        row.status = timedOut ? 'timeout' : record.stepLimit ? 'budget_exceeded' : 'error';
        row.success = false;
        row.reason = redact(error.message);
      } finally {
        clearTimeout(referenceTimer);
        if (abortReference) abortSignal?.removeEventListener('abort', abortReference);
        row.duration_ms = Date.now() - begin;
        row.final_answer = redact(answer);
        row.steps = record.steps;
        row.requested_steps = record.requestedSteps;
        row.tool_calls = record.calls;
        row.human_interventions = 0;
        row.human_requests = record.humanRequests;
        row.failure_category = failureCategory({ ...row, failures: record.failures });
        row.events = record.events;
        row.frames = record.frames;
        record.closed = true;
        try {
          // Context deletion fences pending reads/actions. Never replay a failed
          // mutation just to complete the report or make a task look successful.
          const removed = await workerRequest(workerURL, token, `/sessions/${sessionId}`, { method: 'DELETE', timeout: 20000 });
          row.context_cleaned = removed.ok;
          if (!removed.ok) throw new Error('browser context cleanup not acknowledged');
        } catch {
          row.context_cleaned = false;
          halted = 'context cleanup failed; further tasks were not started';
          row.status = 'error'; row.success = false; row.reason = halted; row.failure_category = 'cleanup_error';
        }
        proxy.unregister(sessionId);
        usedTokens += (row.usage?.input ?? 0) + (row.usage?.output ?? 0);
        await writeFile(path.join(outputDir, row.trace_path), JSON.stringify(safeValue(row.events, redact), null, 2), { mode: 0o600 });
        await publish();
        onProgress({ type: 'task_end', id: row.id, repeat: row.repeat, status: row.status, steps: row.steps, duration_ms: row.duration_ms, reason: row.reason });
      }
    };
    await Promise.all(Array.from({ length: options.concurrency }, async () => {
      while (next < report.tasks.length) {
        const row = report.tasks[next++];
        if (abortSignal?.aborted || halted || options.totalTokens && usedTokens >= options.totalTokens || options.totalTimeMs && Date.now() - started >= options.totalTimeMs) {
          row.reason = abortSignal?.aborted ? 'evaluation cancelled before task start' : halted ?? 'overall evaluation start threshold reached before task start';
          continue;
        }
        try { await runCase(row); }
        catch (error) { row.status = 'error'; row.success = false; row.reason = redact(error.message); halted = 'evaluation infrastructure failure'; }
      }
    }));
  } catch (error) {
    report.infrastructure_error = redact(error.message);
    for (const row of report.tasks) if (row.reason === 'not started') row.reason = 'evaluation infrastructure unavailable';
  } finally {
    await Promise.allSettled([proxy?.close(), worker?.close()]);
    await browser?.close().catch(() => {});
    await fixture?.close().catch(error => { report.cleanup_error = redact(error.message); });
    if (abortSignal?.aborted) report.cancelled = true;
    report.finished_at = new Date().toISOString();
    report.duration_ms = Date.now() - started;
    await publish();
  }
  return { report, outputDir };
}
