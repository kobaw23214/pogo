import { randomBytes, randomUUID, createHash } from 'node:crypto';
import { readFile, mkdir, writeFile, rename } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium } from 'playwright';
import { createWorker } from '../../src/server.js';
import { createRecordingProxy, workerRequest, makeRedactor } from '../transport.js';
import { runAdapter } from '../adapter.js';
import { sourceManifest } from '../runner.js';
import { safeGeneralValue, expectedSkillManifest, requestAllowed, PROFILE } from '../general-web/runner.js';
import { createEvidenceFixtures, TASK_IDS } from './fixtures.js';
import { EVIDENCE_CONTRACT, reference, actionsFrom, assessProtocol } from './contract.js';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const runtime = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const html = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const integer = (value, min, max) => { const n = Number(value); if (!Number.isInteger(n) || n < min || n > max) throw new Error(`integer must be ${min}..${max}`); return n; };

export function parseOptions(argv) {
  const o = { mode: 'validate', control: 'reference', seed: 811, repeat: 1, maxTurns: 40, tokenBudget: 200000, timeoutMs: 240000, maxSteps: 80, tasks: TASK_IDS };
  const names = { '--mode': 'mode', '--control': 'control', '--model': 'model', '--agent': 'agent', '--providers': 'providers', '--out': 'outputDir', '--seed': 'seed', '--repeat': 'repeat', '--tasks': 'tasks', '--max-turns': 'maxTurns', '--token-budget': 'tokenBudget', '--timeout-ms': 'timeoutMs', '--max-steps': 'maxSteps' };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--help') { o.help = true; continue; }
    const k = names[argv[i]], v = argv[++i]; if (!k || !v || v.startsWith('--')) throw new Error('unknown option or missing value'); o[k] = v;
  }
  if (o.help) return o;
  if (!['validate', 'model'].includes(o.mode) || !['reference', 'noop'].includes(o.control) || o.mode === 'model' && o.control !== 'reference') throw new Error('mode validate|model; noop only for validation');
  for (const [k, min, max] of [['seed', 0, 0xffffffff], ['repeat', 1, 3], ['maxTurns', 1, 100], ['tokenBudget', 1, 200000], ['timeoutMs', 1000, 600000], ['maxSteps', 1, 100]]) o[k] = integer(o[k], min, max);
  o.tasks = Array.isArray(o.tasks) ? o.tasks : o.tasks.split(','); if (!o.tasks.length || new Set(o.tasks).size !== o.tasks.length || o.tasks.some(t => !TASK_IDS.includes(t))) throw new Error('unknown/duplicate tasks');
  for (const k of ['agent', 'providers', 'outputDir']) if (o[k]) o[k] = path.resolve(o[k]);
  if (o.mode === 'model' && (!o.agent || !o.model)) throw new Error('model mode needs explicit --agent and --model');
  return o;
}

export function summarize(report) {
  return { planned: report.tasks.length, started: report.tasks.filter(t => t.status !== 'skipped').length,
    interface_verified: report.tasks.filter(t => t.protocol?.interface_verified && t.driver_failures?.length === 0 && t.context_cleaned === true).length,
    error: report.tasks.filter(t => t.status === 'error').length, skipped: report.tasks.filter(t => t.status === 'skipped').length,
    answer_pending_review: report.tasks.filter(t => t.status !== 'skipped').length,
    model_business_score: null, natural_answer_accuracy: 'pending_independent_review', control_only: report.mode === 'validate',
    measured_usage: report.tasks.reduce((sum, t) => ({ input: sum.input + (t.usage?.input ?? 0), output: sum.output + (t.usage?.output ?? 0) }), { input: 0, output: 0 }) };
}
export function renderReport(report) {
  return `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none';style-src 'unsafe-inline';script-src 'none';base-uri 'none';form-action 'none'"><title>可见证据与计算接口闭环</title><style>body{font:15px/1.6 system-ui;max-width:1100px;margin:30px auto;padding:20px}article{border:1px solid #ccd;padding:18px;margin:20px 0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}summary{cursor:pointer}</style><h1>可见证据与计算接口闭环</h1><p>独立受控合成资料。这里只诊断真实浏览器观察与计算接口；自然回答待独立逐项复核，不用正则强判，不作为模型成绩或泛化改善统计。参考/无动作仅为接口正负对照。</p><pre>${html(JSON.stringify(report.summary, null, 2))}</pre>${report.tasks.map(t => `<article><h2>${html(t.id)} · seed ${html(t.seed)} · ${html(t.status)}</h2><p>清理${t.context_cleaned === true ? '已确认' : '未确认'}；回答待复核</p><pre>${html(t.task_prompt)}\n\n${html(t.final_answer)}</pre><details><summary>协议、driver 与公开业务证据</summary><pre>${html(JSON.stringify({ protocol: t.protocol, driver_failures: t.driver_failures, requests: t.server_state_after, actions: t.actions, usage: t.usage }, null, 2))}</pre></details></article>`).join('')}<details><summary>配置与来源指纹</summary><pre>${html(JSON.stringify(report.config, null, 2))}</pre></details></html>`;
}

export async function runSuite(o, { signal, onProgress = () => {} } = {}) {
  const runID = `evidence-${new Date().toISOString().replace(/[:.]/g, '-')}-${randomBytes(3).toString('hex')}`;
  const outputDir = o.outputDir ?? path.join(runtime, 'eval/runs', runID);
  await mkdir(path.dirname(outputDir), { recursive: true, mode: 0o700 }); await mkdir(outputDir, { mode: 0o700 });
  await mkdir(path.join(outputDir, 'traces'), { mode: 0o700 });
  const token = randomBytes(32).toString('hex'), redact = makeRedactor(o.environment ?? process.env, [token]);
  const source = await sourceManifest(), skill = await expectedSkillManifest();
  const suiteFiles = {}; for (const name of ['run.js', 'fixtures.js', 'contract.js', 'evidence.test.js', 'README.md']) suiteFiles[name] = sha(await readFile(new URL(name, import.meta.url)));
  const report = { schema_version: 1, suite: 'evidence-interface-v1', run_id: runID, mode: o.mode, control: o.mode === 'validate' ? o.control : null, model: o.model ?? null,
    started_at: new Date().toISOString(), config: { ...source, ...skill, suite_files_sha256: suiteFiles, suite_source_sha256: sha(JSON.stringify(suiteFiles)), contract: EVIDENCE_CONTRACT,
      profile: PROFILE, node: process.version, task_ids: o.tasks, seeds: Array.from({ length: o.repeat }, (_, i) => (o.seed + i) >>> 0),
      model_input: 'URL + business goal only', answer_policy: 'natural_language_pending_review', oracle_transport: 'Node_memory_only', capture_frames: false,
      fixture_scope: 'controlled_synthetic_four_evidence_templates', network_scope: 'task_origin_and_case_path_only',
      max_turns_per_task: o.maxTurns, token_budget_per_task: o.tokenBudget, time_budget_ms_per_task: o.timeoutMs, max_steps_per_task: o.maxSteps }, tasks: [] };
  if (o.agent) report.config.adapter_sha256 = sha(await readFile(o.agent));
  for (let repeat = 0; repeat < o.repeat; repeat++) for (const id of o.tasks) report.tasks.push({ id, seed: (o.seed + repeat) >>> 0, repeat, status: 'skipped', events: [], context_cleaned: null });
  const publish = async () => { report.summary = summarize(report); const clean = safeGeneralValue(report, redact); for (const [name, text] of [['summary.json', JSON.stringify(clean, null, 2)], ['report.html', renderReport(clean)]]) { await writeFile(path.join(outputDir, `.${name}.tmp`), text, { mode: 0o600 }); await rename(path.join(outputDir, `.${name}.tmp`), path.join(outputDir, name)); } };
  let browser, worker, proxy, fixture, activeTask, activeRow, latestContext, halted; const closed = new WeakSet();
  await publish();
  try {
    signal?.throwIfAborted();
    const launchEnv = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DISPLAY', 'XDG_RUNTIME_DIR', 'SYSTEMROOT'].filter(k => process.env[k]).map(k => [k, process.env[k]]));
    browser = await chromium.launch({ headless: true, chromiumSandbox: true, env: launchEnv }); report.config.chromium = browser.version();
    const isolatedBrowser = { on: (...args) => browser.on(...args), isConnected: () => browser.isConnected(), async newContext(settings) {
      if (!activeTask) throw new Error('task network boundary missing'); const task = activeTask, row = activeRow;
      const context = await browser.newContext({ ...settings, serviceWorkers: 'block' }); context.on('close', () => closed.add(context));
      await context.route('**/*', route => { if (requestAllowed(route.request().url(), task)) return route.continue(); row.blocked_requests.push({ url: redact(route.request().url()), method: route.request().method() }); return route.abort('blockedbyclient'); });
      latestContext = context; return context;
    } };
    worker = await createWorker({ token, browser: isolatedBrowser, actionTimeout: 10000, settleMs: 200 });
    const workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
    proxy = await createRecordingProxy({ workerURL, token, outputDir, redact, captureFrames: false });
    for (let repeat = 0; repeat < o.repeat; repeat++) {
      fixture = await createEvidenceFixtures({ seed: (o.seed + repeat) >>> 0 });
      for (const row of report.tasks.filter(t => t.repeat === repeat)) {
        if (halted || signal?.aborted) { row.reason = halted ?? 'cancelled before start'; continue; }
        const task = fixture.tasks.find(t => t.id === row.id), session = `evidence-${randomUUID()}`, artifact = `${row.id}-${repeat}`;
        Object.assign(row, { task_prompt: task.prompt, task_prompt_sha256: sha(task.prompt), goal_sha256: sha(task.goal), layout_variant: task.layout_variant, blocked_requests: [], driver_failures: [], answer_review: 'pending_review' });
        activeTask = task; activeRow = row; latestContext = null; fixture.reset(row.id);
        const begin = Date.now(), deadline = begin + o.timeoutMs, cancel = new AbortController(); let record, context, adapter, cancellation;
        const interrupt = () => { cancel.abort(new Error('evidence task cancelled or deadline exceeded')); if (record) record.closed = true; cancellation ??= workerRequest(workerURL, token, `/sessions/${session}/cancel`, { method: 'POST', body: {}, timeout: 20000 }).catch(() => {}); };
        const timer = setTimeout(interrupt, o.timeoutMs); signal?.addEventListener('abort', interrupt, { once: true });
        const direct = async (suffix, method, body) => { cancel.signal.throwIfAborted(); const r = await workerRequest(workerURL, token, `/sessions/${session}${suffix}`, { method, body, timeout: Math.max(1, Math.min(20000, deadline - Date.now())) }); if (!r.ok || r.data?.ok === false) throw new Error('worker operation rejected'); return r.data; };
        onProgress({ type: 'task_start', id: row.id, seed: row.seed });
        try {
          await direct('', 'PUT', {}); context = latestContext;
          await direct('/execute', 'POST', { action: 'navigate', url: task.startURL, op_id: randomUUID() });
          row.server_state_before = fixture.inspect(row.id); record = proxy.register(session, artifact, o.maxSteps); row.trace_path = `traces/${artifact}.json`;
          const execute = async args => { cancel.signal.throwIfAborted(); const value = await proxy.execute(session, args); cancel.signal.throwIfAborted(); if (value.ok === false && value.evidence?.status !== 'unsupported') throw new Error(value.error ?? 'browser action rejected'); return value; };
          if (o.mode === 'validate') row.final_answer = o.control === 'noop' ? fixture.referenceData(row.id).answer : await reference(task, fixture.referenceData(row.id), execute);
          else {
            adapter = await runAdapter({ executable: o.agent, args: ['--profile', 'general-web', ...(o.providers ? ['--providers', o.providers] : [])],
              input: { prompt: task.prompt, start_url: task.startURL, worker_url: proxy.url, session_id: session, model: o.model, max_turns: o.maxTurns, token_budget: o.tokenBudget, time_budget_ms: Math.max(1, deadline - Date.now()) },
              environment: { ...(o.environment ?? process.env), POGO_BROWSER_EVAL_TOKEN: token }, timeoutMs: Math.max(1, deadline - Date.now()), redact, abortSignal: cancel.signal,
              onEvent: event => record.events.push({ ...safeGeneralValue(event, redact), at_ms: Date.now() - record.started }) });
            row.final_answer = redact(adapter.end?.answer ?? ''); row.usage = adapter.usage; row.usage_partial = adapter.usage_partial;
            for (const key of ['end_reason', 'profile', 'profile_sha256', 'system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256', 'sees_images']) row[key] = adapter.end?.[key];
            if (!row.profile_sha256 && row.profile && row.system_prompt_sha256 && row.tool_schema_sha256 && row.shared_skill_body_sha256) row.profile_sha256 = sha(JSON.stringify({ profile: row.profile, system_prompt_sha256: row.system_prompt_sha256, tool_schema_sha256: row.tool_schema_sha256, shared_skill_body_sha256: row.shared_skill_body_sha256 }));
            if (adapter.error || adapter.exitCode !== 0 || adapter.timedOut || adapter.aborted || adapter.end?.error || row.end_reason !== 'completed') row.driver_failures.push('adapter_not_completed');
            if (row.profile !== PROFILE || ['system_prompt_sha256', 'tool_schema_sha256', 'shared_skill_body_sha256'].some(k => !/^[a-f0-9]{64}$/.test(row[k] ?? '')) || row.shared_skill_body_sha256 !== skill.expected_shared_skill_body_sha256) row.driver_failures.push('adapter_metadata_or_skill_mismatch');
            if (!row.usage || row.usage_partial || typeof row.usage.input !== 'number' || typeof row.usage.output !== 'number') row.driver_failures.push('usage_not_fully_measured');
            else if (row.usage.input + row.usage.output > o.tokenBudget) row.driver_failures.push('token_budget_exceeded');
          }
          cancel.signal.throwIfAborted(); row.answer_present = Boolean(String(row.final_answer ?? '').trim()); row.actions = actionsFrom(record.events); row.server_state_after = fixture.inspect(row.id);
          row.protocol = assessProtocol(row.actions, fixture.referenceData(row.id), fixture.assessReads(row.id));
          if (record.stepLimit) row.driver_failures.push('step_budget_exceeded');
          row.status = row.protocol.interface_verified && !row.driver_failures.length ? 'interface_verified' : 'interface_failed';
        } catch (error) { row.status = 'error'; row.reason = redact(error.message); row.server_state_after = fixture.inspect(row.id); }
        finally {
          clearTimeout(timer); signal?.removeEventListener('abort', interrupt); await cancellation;
          row.duration_ms = Date.now() - begin; if (record) { record.closed = true; row.events = safeGeneralValue(record.events, redact); row.actions = actionsFrom(row.events); row.requested_steps = record.requestedSteps; row.step_budget_exceeded = record.stepLimit; }
          try { const cleanup = await workerRequest(workerURL, token, `/sessions/${session}`, { method: 'DELETE', timeout: 20000 }); row.context_cleaned = cleanup.ok && cleanup.data?.ok !== false && (!context || closed.has(context)); } catch { row.context_cleaned = false; }
          if (!row.context_cleaned) { row.status = 'error'; row.driver_failures.push('context_cleanup_failed'); halted = 'context cleanup failed'; }
          proxy.unregister(session); activeTask = null; activeRow = null;
          if (row.trace_path) await writeFile(path.join(outputDir, row.trace_path), JSON.stringify(row.events, null, 2), { mode: 0o600 });
          await publish(); onProgress({ type: 'task_end', id: row.id, seed: row.seed, status: row.status, reason: row.reason, interface_verified: row.protocol?.interface_verified });
        }
      }
      await fixture.close(); fixture = null;
    }
  } catch (error) { report.infrastructure_error = redact(error.message); }
  finally { report.cancelled = signal?.aborted ?? false; await proxy?.close().catch(() => {}); await worker?.close().catch(() => {}); await browser?.close().catch(() => {}); await fixture?.close().catch(() => {}); report.completed_at = new Date().toISOString(); await publish(); }
  return { report, outputDir };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const o = parseOptions(process.argv.slice(2));
    if (o.help) process.stdout.write('Independent native Evidence/calculate interface loop; natural answers pending review.\nnode eval/evidence/run.js --mode validate --control reference|noop --seed 811 --repeat 1 --out NEW_DIR\nnode eval/evidence/run.js --mode model --agent PUBLIC_BINARY --model MODEL [--providers CONFIG_PATH] --seed 811 --repeat 1 --out NEW_DIR\n');
    else { const cancellation = new AbortController(); const stop = () => cancellation.abort(); process.once('SIGINT', stop); process.once('SIGTERM', stop);
      const { report, outputDir } = await runSuite(o, { signal: cancellation.signal, onProgress: e => process.stdout.write(JSON.stringify(e) + '\n') });
      process.stdout.write(JSON.stringify({ output_dir: outputDir, summary: report.summary }) + '\n');
      process.exitCode = report.infrastructure_error || report.summary.error || report.summary.skipped || (o.mode === 'validate' && o.control === 'reference' && report.summary.interface_verified !== report.summary.planned) || (o.mode === 'validate' && o.control === 'noop' && report.summary.interface_verified !== 0) ? 1 : 0;
    }
  } catch (error) { process.stderr.write(`Evidence evaluation failed: ${error.message}\n`); process.exitCode = 2; }
}
