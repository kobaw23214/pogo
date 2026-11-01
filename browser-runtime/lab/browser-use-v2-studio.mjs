// Candidate-only Studio acceptance. The recorder owns private judge state and
// raw worker acknowledgements; application SSE is never promoted to that ledger.
import assert from 'node:assert/strict';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';

export const BUDGETS = Object.freeze({ token_budget: 120000, max_turns: 50, timeout_ms: 180000, max_steps: 80 });
export const DEFAULT_TASKS = Object.freeze(['iframe-data', 'debugger-recovery']);
export const BROWSER_HANDOFF = '浏览器已经位于任务起始页面，并可能保留任务的准备状态。请继续当前状态完成任务；不要为了初始化而再次导航，也不要重新发起已经开始的操作。以下为原始任务：';
const ALLOWED_TASKS = new Set(['network-diagnosis', 'iframe-data', 'worker-data', 'debugger-recovery', 'authenticated-write']);
const SKILL_PATH = '/home/pogo/.cache/pogo/embedded-skills/browser-use/SKILL.md';
const INSTRUCTIONS = '使用已准备好的浏览器完成用户任务。只调用 browser 工具；如需读取技能，只允许读取已选择的内置 browser-use 的 SKILL.md。不要调用其他工具，不读取工作区、配置或测试文件，不委派子代理，不访问测试控制器。提供用户要求的事实与实际证据，区分直接观察与推断，不编造来源、调用或执行顺序。';
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const sha = value => createHash('sha256').update(value).digest('hex');
const finiteCount = value => Number.isSafeInteger(value) && value >= 0;

export function validateLoopback(address) {
  const url = new URL(address);
  if (!['http:', 'https:'].includes(url.protocol) || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname) || url.username || url.password || url.search || url.hash || url.pathname !== '/') throw new Error('Candidate controller URLs must be plain loopback origins');
  return url.origin;
}

export function createSSEParser(onEvent, maxBuffer = 4 * 1024 * 1024) {
  let buffer = '';
  return {
    push(chunk) {
      buffer += chunk;
      if (buffer.length > maxBuffer) throw new Error('Application SSE frame exceeds capture limit');
      let match;
      while ((match = /\r?\n\r?\n/.exec(buffer))) {
        const block = buffer.slice(0, match.index); buffer = buffer.slice(match.index + match[0].length);
        const data = block.split(/\r?\n/).filter(line => line.startsWith('data:')).map(line => line.slice(5).replace(/^ /, '')).join('\n');
        if (data) onEvent(JSON.parse(data));
      }
    },
    finish() { if (buffer.trim() && !buffer.trim().startsWith(':')) throw new Error('Application SSE ended with an incomplete frame'); },
  };
}

export function createRunMonitor({ budgets = BUDGETS, skillPath = SKILL_PATH, now = () => Date.now() } = {}) {
  const started = now(), calls = new Set(), messageIDs = new Set(), seqs = new Set();
  let input = 0, output = 0, turns = 0, end = null, invalidUsage = false;
  const violations = [];
  const fail = reason => { if (!violations.includes(reason)) violations.push(reason); };
  const usage = value => value && finiteCount(value.input) && finiteCount(value.output);
  return {
    observe(event) {
      if (event.seq && seqs.has(event.seq)) return;
      if (event.seq) seqs.add(event.seq);
      if (event.type === 'turn_start') {
        if (!finiteCount(event.turn)) fail('missing_turn_counter');
        else turns = Math.max(turns, event.turn);
      }
      if (event.type === 'message' && event.role === 'assistant') {
        if (!event.message_id || !messageIDs.has(event.message_id)) {
          if (event.message_id) messageIDs.add(event.message_id);
          if (usage(event.usage)) { input += event.usage.input; output += event.usage.output; }
          else invalidUsage = true;
        }
      }
      if (event.type === 'tool_start') {
        if (event.name === 'browser') calls.add(event.call_id || `missing-${calls.size}`);
        else if (!(event.name === 'read' && event.args?.path === skillPath)) fail(`disallowed_tool:${event.name || 'unknown'}`);
      }
      if (event.type === 'run_end') {
        end = event;
        if (usage(event.usage)) {
          if (event.usage.input < input || event.usage.output < output) fail('usage_total_below_observed_turns');
          input = Math.max(input, event.usage.input); output = Math.max(output, event.usage.output);
        } else invalidUsage = true;
      }
      this.check();
    },
    check() {
      const elapsed = now() - started;
      if (input + output > budgets.token_budget) fail('token_budget_exceeded');
      if (turns > budgets.max_turns) fail('turn_budget_exceeded');
      if (calls.size > budgets.max_steps) fail('browser_step_budget_exceeded');
      if (elapsed > budgets.timeout_ms) fail('time_budget_exceeded');
      return violations.length > 0;
    },
    fail,
    snapshot() {
      this.check();
      return { usage: { input, output }, usage_partial: invalidUsage || !end?.usage, turns, browser_calls: calls.size, duration_ms: now() - started, end_reason: end?.end_reason ?? null, ended: !!end, violations: [...violations], passed: !!end && end.end_reason === 'completed' && !end.error && !invalidUsage && !!end.usage && violations.length === 0 };
    },
  };
}

export function finalAnswer(events) {
  const messages = events.filter(event => event.type === 'message' && event.role === 'assistant');
  return (messages.at(-1)?.content ?? []).filter(block => block.type === 'text').map(block => block.text ?? '').join('\n');
}

// Correlation is a transport check, not an alternate judge. SSE cannot invent
// worker evidence: each private ACK must have an ordered application call pair.
export function correlateEvidence(events, recorderReport, runID, studioID, workerID) {
  const failures = [], pairs = [];
  const starts = events.filter(event => event.type === 'run_start');
  if (!runID || starts.length !== 1 || starts[0].run_id !== runID) failures.push('studio_run_start_identity_mismatch');
  const ends = events.filter(event => event.type === 'run_end');
  if (ends.length !== 1 || (ends[0].run_id && ends[0].run_id !== runID)) failures.push('studio_run_end_identity_mismatch');
  if (recorderReport?.studio_run_id !== runID || recorderReport?.studio_session_id !== studioID || recorderReport?.worker_session_id !== workerID) failures.push('recorder_session_run_identity_mismatch');
  const ledger = recorderReport?.ledger ?? [], calls = events.map((event, position) => ({ ...event, position })).filter(event => event.type === 'tool_start' && event.name === 'browser');
  const runStartIndex = events.findIndex(event => event.type === 'run_start'), runEndIndex = events.findIndex(event => event.type === 'run_end');
  const receipts = new Map(), seenCalls = new Set(), seenWorkerCalls = new Set();
  for (const [position, event] of events.entries()) if (event.type === 'tool_end') {
    const values = receipts.get(event.call_id) ?? []; values.push({ ...event, position }); receipts.set(event.call_id, values);
  }
  for (const ack of ledger) {
    if (!ack.call_id || seenWorkerCalls.has(ack.call_id)) failures.push('duplicate_or_missing_worker_call_id');
    seenWorkerCalls.add(ack.call_id);
  }
  const stable = value => JSON.stringify(value && typeof value === 'object' && !Array.isArray(value) ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, child]) => [key, JSON.parse(stable(child))])) : Array.isArray(value) ? value.map(child => JSON.parse(stable(child))) : value);
  const matches = (args, observed) => {
    if (!args || !observed || String(args.action).trim() !== observed.action) return false;
    // Go rebuilds the validated op and drops irrelevant/empty fields. Compare
    // every forwarded field against the original call; op_id is native routing.
    return Object.entries(observed).every(([key, value]) => key === 'op_id' || key === 'action' || stable(args[key] ?? (typeof value === 'number' ? 0 : typeof value === 'boolean' ? false : '')) === stable(value));
  };
  let index = 0;
  for (const call of calls) {
    if (!call.call_id || seenCalls.has(call.call_id)) { failures.push('duplicate_or_missing_application_call_id'); continue; }
    seenCalls.add(call.call_id);
    const matching = receipts.get(call.call_id) ?? [];
    if (matching.length !== 1) { failures.push('browser_call_receipt_not_unique'); continue; }
    const end = matching[0];
    if (end.name && end.name !== 'browser') failures.push('browser_call_receipt_tool_mismatch');
    if (call.position <= runStartIndex || call.position >= runEndIndex || end.position <= call.position || end.position >= runEndIndex) failures.push('browser_call_outside_matching_run_interval');
    const ack = ledger[index];
    if (ack && matches(call.args, ack.args)) {
      if (ack.sessionId !== workerID) failures.push('worker_ack_session_mismatch');
      if (end.details?.action && end.details.action !== ack.args.action) failures.push('application_ack_action_mismatch');
      if (end.details?.ok !== undefined && end.details.ok !== ack.result?.ok) failures.push('application_ack_status_mismatch');
      if (end.details?.devtools_data && stable(end.details.devtools_data) !== stable(ack.result?.data)) failures.push('application_raw_devtools_ack_mismatch');
      pairs.push({ app_call_id: call.call_id, worker_call_id: ack.call_id, action: ack.args.action, method: ack.args.method ?? null });
      index++;
    } else if (!end.is_error) failures.push('successful_application_call_has_no_worker_ack');
  }
  for (const event of events) if (event.type === 'tool_end' && event.name === 'browser' && !seenCalls.has(event.call_id)) failures.push('browser_receipt_has_no_application_start');
  if (index !== ledger.length) failures.push('worker_ack_has_no_application_call');
  return { passed: failures.length === 0 && pairs.length > 0, failures: [...new Set(failures)], pairs, run_end_id_policy: 'Current App run_end may omit run_id; require exactly one matching run_start in a new owned session and reject any present mismatched run_end ID.' };
}

export function redactReport(value, secrets = []) {
  // Match the worker/recorder marker exactly. Replacing an already-redacted
  // Cookie value with a different spelling would change the archived ledger
  // bytes and invalidate the recorder's hash even though no fact had changed.
  const marker = '[REDACTED]';
  const literals = [...new Set(secrets.filter(item => typeof item === 'string' && item.length >= 4 && item !== marker))].sort((a, b) => b.length - a.length);
  const pattern = literals.length ? new RegExp(literals.map(item => item.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'), 'g') : null;
  // Protect existing markers and replace all literals in one pass so a later
  // secret cannot mutate a marker inserted by an earlier replacement. Work on
  // decoded strings so quoted/newline-bearing credentials are covered too.
  const scrubString = text => pattern ? text.split(marker).map(part => part.replace(pattern, marker)).join(marker) : text;
  const scrub = (item, key = '') => {
    if (/^(lease_token|authorization|cookie|set-cookie|password|admin_token)$/i.test(key)) return marker;
    if (typeof item === 'string') return scrubString(item);
    if (Array.isArray(item)) return item.map(child => scrub(child));
    if (item && typeof item === 'object') return Object.fromEntries(Object.entries(item).map(([name, child]) => [scrubString(name), scrub(child, name)]));
    return item;
  };
  return scrub(JSON.parse(JSON.stringify(value)));
}

async function provenance(extraPath) {
  const runtime = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  const files = ['lab/browser-use-v2-studio.mjs', '../skills/builtin/browser-use/SKILL.md', '../tools/browser.go', '../tools/browser_devtools_view.go', '../tools/browser_schema.go'];
  const hashes = {};
  for (const relative of files) {
    try { hashes[relative] = sha(await readFile(path.join(runtime, relative))); }
    catch (error) { if (error.code !== 'ENOENT') throw error; hashes[relative] = null; }
  }
  return { local_source_sha256: hashes, candidate: extraPath ? JSON.parse(await readFile(extraPath, 'utf8')) : null, candidate_image_identity_verified_by_harness: false };
}

export async function runAcceptance(config) {
  const base = validateLoopback(config.appURL), recorder = validateLoopback(config.recorderURL);
  const mode = config.mode ?? 'smoke', tasks = config.tasks ?? DEFAULT_TASKS;
  assert.ok(['smoke', 'model'].includes(mode), 'Mode must be smoke or model');
  assert.ok(tasks.length && tasks.every(id => ALLOWED_TASKS.has(id)), 'Unknown or empty task selection');
  assert.ok(config.password && config.recorderToken, 'Candidate login and recorder credentials are required');
  await mkdir(config.output, { mode: 0o700 }); // No recursive: never overwrite an existing run.
  const request = config.fetch ?? fetch;
  const secrets = [config.password, config.recorderToken];
  const report = { schema_version: 1, kind: 'browser-use-v2-studio-candidate-acceptance', mode, model: mode === 'model' ? 'glm-5.3' : null, seed: config.seed ?? 20261012, started_at: new Date().toISOString(), app: base, recorder: recorder, budgets: BUDGETS, budget_enforcement: 'External SSE monitor plus recorder step limit; in-flight model usage can overrun. Any actual overrun fails.', tool_boundary: 'Only browser selected; protected read/ls/find/grep/todo/subagent/analyze_image can remain registered. Only browser and exact builtin Skill read are accepted; SSE cancellation cannot undo a tool already started.', evidence_boundary: 'Private recorder raw worker ACK ledger is judge evidence. Filtered application SSE carries run lifecycle, actual usage, final answer and tool receipts, not judge authority.', ui_layout_test: false, tasks: [], checks: [], passed: false };
  let cookie = '', agentID;
  const clean = value => redactReport(value, secrets);
  async function json(origin, route, body, method = body === undefined ? 'GET' : 'POST') {
    const headers = origin === base ? { Cookie: cookie, Origin: base } : { Authorization: `Bearer ${config.recorderToken}` };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await request(origin + route, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(65000) });
    if (!response.ok) throw new Error(`${origin === base ? 'App' : 'Recorder'} ${method} ${route} returned ${response.status}`);
    return response.status === 204 ? null : response.json();
  }
  const app = (route, body, method) => json(base, route, body, method);
  const control = (route, body, method) => json(recorder, route, body, method);
  try {
    report.provenance = await provenance(config.provenancePath);
    const login = await request(base + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: base }, body: JSON.stringify({ username: config.username ?? 'admin', password: config.password }), signal: AbortSignal.timeout(10000) });
    assert.ok(login.ok, `Candidate account login failed (${login.status})`);
    cookie = login.headers.getSetCookie().map(value => value.split(';')[0]).join('; ');
    assert.ok(cookie, 'Candidate account cookie unavailable');
    secrets.push(cookie, ...cookie.split('; ').map(value => value.slice(value.indexOf('=') + 1)));
    const capabilities = await app('/api/manage/capabilities');
    const skill = capabilities.skills?.find(item => item.name === 'browser-use' && item.builtin);
    assert.ok(skill?.id, 'Builtin browser-use is missing from candidate catalog');
    report.capability_catalog = capabilities;
    const createdAgent = await app('/api/manage/agents', { name: `通用浏览器候选验收 ${mode}`, description: '独立候选实例中的受控 Studio 链路验收', instructions: INSTRUCTIONS, model: 'glm-5.3', models: ['glm-5.3'], tools: ['browser'], skills: [skill.id], mcp_servers: [], status: 'ready' });
    agentID = createdAgent.id;
    assert.ok(agentID, 'Candidate agent ID missing');
    report.agent_id = agentID; report.agent_instructions_sha256 = sha(INSTRUCTIONS);
    for (const taskID of tasks) {
      const phase = { task_id: taskID, checks: [], passed: false, cleanup: {} };
      report.tasks.push(phase);
      let sid, armed = false, streamController, streamFlight, cancelled = false;
      const events = [], gateFlights = new Set();
      let streamError, captureBytes = 0;
      const check = (name, condition) => { assert.ok(condition, name); phase.checks.push(name); };
      const cancel = async () => {
        if (!sid || cancelled) return;
        cancelled = true;
        try { phase.cancellation = await app(`/api/sessions/${sid}/control`, { action: 'cancel' }); }
        catch (error) { phase.cancellation_error = error.message; }
      };
      try {
        const session = await app(`/api/apps/${agentID}/sessions`, {}); sid = session.id;
        phase.session_id = sid;
        check('Owned GLM-5.3 Studio session created', !!sid && session.model === 'glm-5.3');
        phase.capabilities = await app(`/api/apps/${agentID}/sessions/${sid}/capabilities`);
        check('Pinned native tool and Skill selections preserved', phase.capabilities.tools?.length === 1 && phase.capabilities.tools[0] === 'browser' && phase.capabilities.skills?.length === 1 && phase.capabilities.skills[0] === skill.id && phase.capabilities.mcp_servers?.length === 0);
        const begun = await control('/runs/begin', { task_id: taskID, seed: report.seed, studio_session_id: sid, ...BUDGETS }); armed = true;
        phase.original_task_prompt = begun.task?.prompt;
        check('Original fixture task prompt received', typeof phase.original_task_prompt === 'string' && phase.original_task_prompt.length > 10);
        phase.original_task_prompt_sha256 = sha(phase.original_task_prompt);
        // Compatibility aliases continue to mean the unchanged fixture prompt.
        phase.prompt = phase.original_task_prompt; phase.prompt_sha256 = phase.original_task_prompt_sha256;
        phase.initial_browser_state = await app(`/api/sessions/${sid}/browser`);
        phase.mapping = await control('/runs/state');
        check('App browser-state created actual private worker mapping', phase.mapping.studio_session_id === sid && !!phase.mapping.worker_session_id);
        const human = await app(`/api/sessions/${sid}/browser/control`, { mode: 'human' });
        if (human.lease_token) secrets.push(human.lease_token);
        check('App can take human control', !!human.lease_token);
        phase.control_return = await app(`/api/sessions/${sid}/browser/control`, { mode: 'agent', lease_token: human.lease_token });
        phase.preparation = await control('/runs/prepare', {});
        if (mode === 'smoke') {
          const targets = await control('/runs/execute', { action: 'devtools', method: 'targets.list' });
          phase.targets_ack = targets;
          const ack = targets.reply ?? targets.ack ?? targets;
          const items = ack.data?.items ?? ack.data?.targets ?? [];
          const expectedType = taskID === 'iframe-data' ? 'iframe' : taskID === 'debugger-recovery' || taskID === 'worker-data' ? 'worker' : 'page';
          check(`Real ${expectedType} target discovered`, ack.ok && items.some(item => item.type === expectedType));
          if (taskID === 'debugger-recovery') check('Worker is actually paused', items.some(item => item.type === 'worker' && item.paused === true));
          await cancel();
          check('Idle App cancel request returned without error', !phase.cancellation_error);
          phase.cancel_boundary = 'An idle App run cancel returns cancelled:false; real native cancel and DELETE are required below through session deletion.';
          phase.passed = true;
        } else {
          const monitor = createRunMonitor({ skillPath: config.skillPath ?? SKILL_PATH });
          streamController = new AbortController();
          const response = await request(`${base}/api/sessions/${sid}/stream`, { headers: { Cookie: cookie }, signal: streamController.signal });
          check('Application SSE connected', response.ok && !!response.body);
          let active = false;
          const parser = createSSEParser(event => {
            if (!active || ['thinking', 'token', 'tool_args', 'snapshot'].includes(event.type)) return;
            captureBytes += JSON.stringify(event).length;
            if (captureBytes > 50 * 1024 * 1024) throw new Error('Application event capture limit exceeded');
            events.push(event); monitor.observe(event);
            if (monitor.check()) void cancel();
            if (event.type === 'gate_request') {
              const allow = event.name === 'browser-arm';
              if (!allow) monitor.fail(`unexpected_gate:${event.name}`);
              const flight = app(`/api/sessions/${sid}/control`, { action: 'gate_decide', gate_id: event.gate_id, allow, reason: 'Owned synthetic browser acceptance only' }).catch(error => { streamError = error.message; }).finally(() => gateFlights.delete(flight));
              gateFlights.add(flight);
            }
          });
          streamFlight = (async () => {
            const decoder = new TextDecoder();
            for await (const chunk of response.body) parser.push(decoder.decode(chunk, { stream: true }));
            parser.push(decoder.decode()); parser.finish();
            if (!streamController.signal.aborted) streamError = 'Application SSE ended before controller cleanup';
          })().catch(error => { if (!streamController.signal.aborted) streamError = error.message; });
          phase.recording = await control('/runs/start', {});
          active = true;
          phase.task_handoff = BROWSER_HANDOFF;
          phase.submitted_prompt = `/skill:browser-use ${BROWSER_HANDOFF}\n\n${phase.original_task_prompt}`;
          phase.submitted_prompt_sha256 = sha(phase.submitted_prompt);
          const started = await app(`/api/sessions/${sid}/messages`, { prompt: phase.submitted_prompt });
          phase.run_id = started.run_id;
          if (!phase.run_id) monitor.fail('missing_studio_run_id');
          let cancelAt = null;
          while (!monitor.snapshot().ended) {
            if (streamError) monitor.fail('application_stream_error');
            if (monitor.check() || streamError) {
              if (cancelAt === null) { cancelAt = Date.now(); await cancel(); }
              if (Date.now() - cancelAt > 15000) { monitor.fail('run_end_missing_after_cancel'); break; }
            }
            await sleep(100);
          }
          phase.driver = monitor.snapshot();
          if (streamError) phase.stream_error = streamError;
          phase.answer = finalAnswer(events);
          phase.filtered_application_events = events;
          phase.recorder_finish = await control('/runs/finish', { answer: phase.answer, usage: phase.driver.usage, usage_partial: phase.driver.usage_partial, end_reason: phase.driver.end_reason ?? 'cancelled', run_id: phase.run_id, duration_ms: phase.driver.duration_ms });
          phase.passed = phase.driver.passed;
        }
      } catch (error) {
        phase.error = error.message;
        phase.passed = false;
        await cancel();
      } finally {
        streamController?.abort(); await streamFlight; await Promise.allSettled(gateFlights);
        if (sid) {
          // Session deletion is the real App -> native browser -> recorder DELETE path.
          try { await app(`/api/sessions/${sid}`, undefined, 'DELETE'); phase.cleanup.app_session_deleted = true; }
          catch (error) { await cancel(); await sleep(250); try { await app(`/api/sessions/${sid}`, undefined, 'DELETE'); phase.cleanup.app_session_deleted = true; } catch (again) { phase.cleanup.app_delete_error = again.message; } }
        }
        if (armed) {
          try { phase.cleanup.recorder = await control('/runs/cleanup', {}); }
          catch (error) { phase.cleanup.recorder_error = error.message; }
          try { phase.recorder_report = await control('/runs/report'); }
          catch (error) { phase.cleanup.report_error = error.message; }
        }
        if (!phase.cleanup.app_session_deleted || phase.cleanup.recorder_error || phase.cleanup.report_error || !phase.recorder_report?.context_cleaned || !phase.recorder_report?.cancel_acknowledged || !phase.recorder_report?.delete_acknowledged) phase.passed = false;
        const verdict = phase.recorder_report?.judgement ?? phase.recorder_finish?.judgement;
        if (mode === 'model') {
          phase.automatic_verdict = verdict ?? null;
          phase.evidence_correlation = correlateEvidence(events, phase.recorder_report, phase.run_id, sid, phase.mapping?.worker_session_id);
          if (!verdict?.success || !phase.evidence_correlation.passed) phase.passed = false;
        }
        if (mode === 'smoke' && taskID === 'debugger-recovery' && (phase.recorder_report?.server_state_after_cleanup?.submission_attempts !== 0 || phase.recorder_report?.server_state_after_cleanup?.submissions?.length !== 0)) { phase.passed = false; phase.cleanup.unexpected_submission_after_cancel = true; }
        await writeFile(path.join(config.output, `${taskID}.json`), JSON.stringify(clean(phase), null, 2) + '\n', { mode: 0o600 });
        console.log(JSON.stringify({ task_id: taskID, mode, passed: phase.passed, tokens: phase.driver ? phase.driver.usage.input + phase.driver.usage.output : 0, output: config.output }));
      }
      // Wiring must be trustworthy before any later task or model request.
      if (!phase.cleanup.app_session_deleted || phase.cleanup.recorder_error || !phase.recorder_report?.context_cleaned) break;
    }
    report.passed = report.tasks.length === tasks.length && report.tasks.every(item => item.passed);
  } catch (error) { report.error = error.message; }
  finally {
    if (agentID) {
      try { await app(`/api/manage/agents/${agentID}`, undefined, 'DELETE'); report.agent_deleted = true; }
      catch (error) { report.agent_delete_error = error.message; report.passed = false; }
    }
    report.finished_at = new Date().toISOString();
    const cleaned = clean(report);
    await writeFile(path.join(config.output, 'summary.json'), JSON.stringify(cleaned, null, 2) + '\n', { mode: 0o600 });
    return cleaned;
  }
}

export function parseCLI(argv, env = process.env) {
  if (argv.includes('--help')) return null;
  const [output, ...args] = argv, options = {};
  if (!output) throw new Error('Pass a new output directory followed by --mode smoke|model --seed N');
  for (let i = 0; i < args.length; i += 2) {
    if (!['--mode', '--seed', '--tasks'].includes(args[i]) || !args[i + 1]) throw new Error('Unknown or incomplete option');
    options[args[i].slice(2)] = args[i + 1];
  }
  const seed = Number(options.seed ?? 20261012);
  if (!Number.isSafeInteger(seed)) throw new Error('Seed must be an integer');
  return { output: path.resolve(output), mode: options.mode ?? 'smoke', tasks: options.tasks?.split(','), seed, appURL: env.LAB_APP_URL ?? 'http://127.0.0.1:19779', recorderURL: env.LAB_RECORDER_ADMIN_URL ?? 'http://127.0.0.1:19780', password: env.LAB_PASSWORD, username: env.LAB_USERNAME ?? 'admin', recorderToken: env.LAB_RECORDER_ADMIN_TOKEN, provenancePath: env.LAB_SOURCE_PROVENANCE, skillPath: env.LAB_BROWSER_SKILL_PATH };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const config = parseCLI(process.argv.slice(2));
    if (!config) console.log('node lab/browser-use-v2-studio.mjs NEW_OUTPUT_DIR --mode smoke|model --seed N [--tasks iframe-data,debugger-recovery]\nRequires LAB_APP_URL, LAB_RECORDER_ADMIN_URL, LAB_PASSWORD, LAB_RECORDER_ADMIN_TOKEN in the controller environment. Model mode invokes GLM-5.3. Output directories must not exist.');
    else { const result = await runAcceptance(config); console.log(JSON.stringify({ passed: result.passed, tasks: result.tasks.length, output: config.output })); if (!result.passed) process.exitCode = 1; }
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
