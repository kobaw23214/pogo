// Deployed release acceptance. This file never starts Docker or reads deployment
// credentials itself; the caller supplies a private JSON config explicitly.
import assert from 'node:assert/strict';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID, createHash } from 'node:crypto';
import { createSSEParser, createRunMonitor, finalAnswer, redactReport } from './browser-use-v2-studio.mjs';

export const LIMITS = Object.freeze({ token_budget: 120000, timeout_ms: 120000, max_turns: 12, max_steps: 24 });
const SKILL_PATH = '/home/pogo/.cache/pogo/embedded-skills/browser-use/SKILL.md';
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const enc = value => encodeURIComponent(value);
const hash = value => createHash('sha256').update(value).digest('hex');

function webURL(value, originOnly = false) {
  const url = new URL(value);
  assert.ok(['http:', 'https:'].includes(url.protocol) && !url.username && !url.password, 'Use http(s) URLs without embedded credentials');
  if (originOnly) assert.ok(url.pathname === '/' && !url.search && !url.hash, 'appURL must be a plain origin');
  return originOnly ? url.origin : url.href;
}
export function selectModel(catalog, preferred = 'glm-5.3') {
  const models = (catalog.models ?? []).filter(model => model.configured === true);
  const match = id => models.find(model => model.id === id || model.aliases?.includes(id));
  const selected = match(preferred) ?? match(catalog.default);
  assert.ok(selected?.id, 'Neither preferred nor default model is configured');
  return { id: selected.id, provider: selected.provider, preferred, fallback: !match(preferred) };
}
export function releasePrompt(fixtureURL) {
  return `/skill:browser-use 在浏览器中验收这个合成测试页面：${fixtureURL}\n` +
    '仅使用 browser 工具（如需读技能，只允许读取内置 browser-use）。打开上述原站地址一次，读取页面定位按钮。使用 devtools Runtime.evaluate 读取 window.__releaseMarker；它是本次随机标记。点击文本为 Save once 的按钮一次且仅一次。用 devtools network.list 和 network.read 查看这一次保存 POST 的响应，核对 marker 和 receipt_id。不要重新点击、重放、刷新或自行构造保存请求；失败就停止并说明。不要访问其他站点、工作区或任何测试控制器。成功时最终只返回 JSON：{"marker":"实际读到的标记","receipt_id":"响应里的编号"}。成功回复必须直接以 { 开头、以 } 结束，只包含这两个字段。不要添加引文、Markdown、代码块、核验摘要或解释文字；核验过程留在工具记录中。';
}
function browserSteps(args) {
  if (!args || typeof args !== 'object') return [];
  return args.action === 'batch' ? (args.actions ?? []).flatMap(browserSteps) : [args];
}
export function judgeModel(events, oracle, fixtureURL, runID, monitor) {
  const failures = [];
  const check = (ok, why) => { if (!ok) failures.push(why); };
  const starts = events.filter(event => event.type === 'run_start');
  const ends = events.filter(event => event.type === 'run_end');
  check(!!runID && starts.length === 1 && starts[0].run_id === runID, 'run_start_identity');
  check(ends.length === 1 && (!ends[0].run_id || ends[0].run_id === runID), 'run_end_identity');
  check(monitor.passed, 'run_or_budget_failed');
  const calls = events.filter(event => event.type === 'tool_start' && event.name === 'browser');
  const steps = calls.flatMap(call => browserSteps(call.args));
  const pairs = calls.map(call => ({ call, ends: events.filter(event => event.type === 'tool_end' && event.call_id === call.call_id) }));
  check(new Set(calls.map(call => call.call_id)).size === calls.length && calls.every(call => !!call.call_id), 'unique_browser_call_ids');
  check(pairs.every(pair => pair.ends.length === 1 && !pair.ends[0].is_error && pair.ends[0].details?.ok !== false), 'browser_receipt_pairs');
  const navigations = steps.filter(step => step.action === 'navigate' || step.action === 'tab' && step.tab_action === 'new' && step.url);
  check(navigations.length === 1 && webURL(navigations[0].url) === fixtureURL, 'single_original_navigation');
  check(steps.some(step => ['snapshot', 'find'].includes(step.action)) || pairs.some(({ call, ends }) => (call.args?.action === 'navigate' || call.args?.action === 'tab' && call.args?.tab_action === 'new') && ends.length === 1 && typeof ends[0].text === 'string' && ends[0].text.length > 0), 'page_observation_missing');
  check(steps.filter(step => step.action === 'click').length === 1, 'single_click');
  check(steps.some(step => step.action === 'devtools' && step.method === 'network.list'), 'network_list_missing');
  const observed = (method, expected) => pairs.some(({ call, ends }) => browserSteps(call.args).some(step => step.action === 'devtools' && step.method === method) && ends.length === 1 && !ends[0].is_error && JSON.stringify(ends[0]).includes(expected));
  check(typeof oracle.marker === 'string' && oracle.marker.length >= 8, 'oracle_marker_missing');
  check(oracle.write_attempts === 1 && Array.isArray(oracle.writes) && oracle.writes.length === 1, 'exactly_one_fixture_write');
  const write = oracle.writes?.[0];
  check(write?.marker === oracle.marker && typeof write?.receipt_id === 'string' && write.receipt_id.length > 0, 'fixture_write_identity');
  check(observed('Runtime.evaluate', oracle.marker), 'cdp_marker_receipt_missing');
  check(observed('network.read', write?.receipt_id ?? '\u0000'), 'network_write_receipt_missing');
  const answer = finalAnswer(events);
  let parsed;
  try { parsed = JSON.parse(answer.trim().replace(/^```(?:json)?\s*\n?/, '').replace(/\n?```$/, '')); } catch { /* strict final JSON is part of this controlled task */ }
  check(parsed?.marker === oracle.marker && parsed?.receipt_id === write?.receipt_id, 'final_answer_mismatch');
  return { passed: failures.length === 0, failures, answer, browser_calls: calls.length, cdp_methods: steps.filter(step => step.action === 'devtools').map(step => step.method), write_attempts: oracle.write_attempts, writes: oracle.writes?.length ?? null };
}

export async function runReleaseAcceptance(config) {
  const workflowOnly = config.workflowOnly === true;
  const base = webURL(config.appURL, true);
  const fixtureURL = workflowOnly ? null : webURL(config.fixtureURL), oracleURL = workflowOnly ? null : webURL(config.oracleURL);
  assert.ok(config.token || config.cookie, 'An explicitly supplied app token or cookie is required');
  const limits = { ...LIMITS, ...config.limits };
  for (const key of Object.keys(LIMITS)) assert.ok(Number.isSafeInteger(limits[key]) && limits[key] > 0 && limits[key] <= LIMITS[key], `Invalid or excessive ${key}`);
  const request = config.fetch ?? fetch, pause = config.sleep ?? sleep;
  const owned = {}, uid = randomUUID(), name = `native-release-${uid}`;
  const secrets = [config.token, config.cookie, config.releaseToken, ...Object.values(config.headers ?? {}), ...Object.values(config.oracleHeaders ?? {})];
  const report = { schema_version: 1, kind: 'actual-deployment-synthetic-release-acceptance', id: uid, owned_name_prefix: name, scope: workflowOnly ? 'workflow-only' : 'browser-model-and-human-workflow', app: base, started_at: new Date().toISOString(), budgets: limits, budget_boundary: 'External observed-usage monitor; an in-flight completion can exceed the cap and will fail. No automatic retry of model task, business write, or workflow approval.', model: { passed: false, ...(workflowOnly ? { skipped: true, reason: 'Explicit no-model workflow-only scope' } : {}) }, workflow: { passed: false, model_calls: 0 }, cleanup: {}, passed: false };
  await mkdir(config.output, { mode: 0o700 }); // Existing output refuses, so no earlier evidence is overwritten.
  const appHeaders = { ...(config.headers ?? {}), ...(config.releaseToken ? { "X-Pogo-Release": config.releaseToken } : {}), ...(config.token ? { Authorization: `Bearer ${config.token}` } : {}), ...(config.cookie ? { Cookie: config.cookie } : {}), Origin: base };
  async function api(route, body, method = body === undefined ? 'GET' : 'POST') {
    const response = await request(base + route, { method, headers: { ...appHeaders, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000), redirect: 'error' });
    assert.ok(response.ok, `App ${method} ${route}: HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  }
  async function oracle() {
    const response = await request(oracleURL, { headers: config.oracleHeaders ?? {}, signal: AbortSignal.timeout(10000), redirect: 'error' });
    assert.ok(response.ok, `Fixture oracle: HTTP ${response.status}`);
    return response.json();
  }
  const save = async (file, value) => writeFile(path.join(config.output, file), JSON.stringify(redactReport(value, secrets), null, 2) + '\n', { mode: 0o600 });
  async function cleanup(key, fn) {
    try { await fn(); report.cleanup[key] = true; } catch (error) { report.cleanup[key] = false; report.cleanup[`${key}_error`] = error.message; }
  }
  if (!workflowOnly) try {
    // A contaminated/reused fixture must fail before any paid call is started.
    const initial = await oracle();
    assert.ok(typeof initial.marker === 'string' && initial.marker.length >= 8 && initial.write_attempts === 0 && Array.isArray(initial.writes) && initial.writes.length === 0, 'Fixture is not a fresh unique case');
    report.fixture_marker_sha256 = hash(initial.marker);
    const selected = selectModel(await api('/api/models'), config.model);
    report.model.selection = selected;
    const catalog = await api('/api/manage/capabilities');
    const skill = catalog.skills?.find(item => item.name === 'browser-use' && item.builtin && item.id);
    assert.ok(skill, 'Builtin browser-use skill is unavailable');
    const agent = await api('/api/manage/agents', { name, description: 'Temporary owned synthetic native browser release check', instructions: 'Complete only the supplied synthetic browser check. Use browser and, if required, read only the selected builtin browser-use skill. Do not use shell, fetch, files, delegation, external sites or private controllers. Never retry a save action.', model: selected.id, models: [selected.id], tools: ['browser'], skills: [skill.id], mcp_servers: [], status: 'ready' });
    assert.ok(agent.id, 'Created agent ID missing'); owned.agent = agent.id;
    const session = await api(`/api/apps/${enc(owned.agent)}/sessions`, {});
    assert.ok(session.id, 'Created session ID missing'); owned.session = session.id;
    report.model.agent_id = owned.agent; report.model.session_id = owned.session;
    assert.equal(session.model, selected.id, 'Actual session model differs from selected configured model');
    const caps = await api(`/api/apps/${enc(owned.agent)}/sessions/${enc(owned.session)}/capabilities`);
    assert.deepEqual(caps.tools, ['browser'], 'Session browser tool selection changed');
    assert.deepEqual(caps.skills, [skill.id], 'Session builtin Skill selection changed');
    assert.deepEqual(caps.mcp_servers, [], 'Unexpected MCP selection');
    const state = await api(`/api/sessions/${enc(owned.session)}/browser`);
    assert.equal(state.backend, 'native', 'Actual deployed browser service is unavailable');
    let held;
    if (config.heldTabURL) {
      const url = webURL(config.heldTabURL);
      const binding = { context_ref: state.context_ref, tab_id: state.viewed_tab || state.active_tab };
      assert.ok(binding.tab_id && binding.context_ref, 'Held page needs a real page binding');
      const control = await api(`/api/sessions/${enc(owned.session)}/browser/control`, { mode: 'human', ...binding });
      assert.ok(control.lease_token); secrets.push(control.lease_token);
      await api(`/api/sessions/${enc(owned.session)}/browser/input`, { type: 'navigate', url, lease_token: control.lease_token, context_ref: binding.context_ref, target_tab: binding.tab_id, generation: control.generation });
      held = { ...binding, url, control_epoch: control.control_epoch };
      report.model.held_page = { tab_id: held.tab_id, control: 'human', preserved: false };
    }
    await runModel();
    if (held) {
      const after = await api(`/api/sessions/${enc(owned.session)}/browser?${new URLSearchParams({ context_ref: held.context_ref, tab_id: held.tab_id })}`);
      const preserved = after.control === 'human' && after.control_epoch === held.control_epoch && after.url === held.url && after.active_tab !== held.tab_id;
      report.model.held_page.preserved = preserved;
      report.model.held_page.agent_tab = after.active_tab;
      report.model.passed &&= preserved;
      assert.ok(preserved, 'Model task changed the held page/control or did not move to a separate Agent tab');
    }
  } catch (error) { report.model.error = error.message; }
  // Workflow lifecycle is independent; a failed browser/model check must not
  // manufacture a workflow success or prevent this no-model path being tested.
  try { await runWorkflow(); } catch (error) { report.workflow.error = error.message; }
  finally {
    if (owned.session) {
      await cleanup('session_cancelled', () => api(`/api/sessions/${enc(owned.session)}/control`, { action: 'cancel' }));
      await cleanup('session_deleted', () => api(`/api/sessions/${enc(owned.session)}`, undefined, 'DELETE'));
    }
    if (owned.agent) await cleanup('agent_deleted', () => api(`/api/manage/agents/${enc(owned.agent)}`, undefined, 'DELETE'));
    if (owned.workflow && owned.run && !report.workflow.passed) await cleanup('workflow_cancelled', () => api(`/api/workflows/${enc(owned.workflow)}/runs/${enc(owned.run)}/cancel`, {}));
    if (owned.workflow) await cleanup('workflow_deleted', () => api(`/api/manage/workflows/${enc(owned.workflow)}`, undefined, 'DELETE'));
    // The product has no DELETE workflow-run route. Never claim its historical
    // artifact directory vanished, and never remove unrelated storage by path.
    if (owned.run) report.cleanup.retained_run_history = { workflow_id: owned.workflow, run_id: owned.run, reason: 'No workflow run deletion API; temporary definition removed, historical decision artifact retained.' };
    if (!workflowOnly) try { report.final_fixture = await oracle(); } catch (error) { report.oracle_after_cleanup_error = error.message; }
    const final = report.final_fixture;
    if (report.model.passed && (!final || final.marker !== report.model.marker || final.write_attempts !== 1 || final.writes?.length !== 1 || final.writes[0].receipt_id !== report.model.receipt_id)) { report.model.passed = false; report.model.late_write_or_oracle_failure = true; }
    report.passed = report.model.passed && report.workflow.passed && report.cleanup.session_deleted === true && report.cleanup.agent_deleted === true && report.cleanup.workflow_deleted === true;
    report.scope_passed = workflowOnly ? report.workflow.passed && report.cleanup.workflow_deleted === true : report.passed;
    report.finished_at = new Date().toISOString();
    await save('summary.json', report);
  }
  return redactReport(report, secrets);

  async function runModel() {
    const route = `/api/sessions/${enc(owned.session)}`;
    const events = [], gates = new Set(), controller = new AbortController();
    let streamFlight, streamError, captureBytes = 0, cancelled = false;
    const monitor = createRunMonitor({ budgets: limits, skillPath: config.skillPath ?? SKILL_PATH });
    const cancel = async () => { if (cancelled) return; cancelled = true; try { await api(`${route}/control`, { action: 'cancel' }); } catch (error) { streamError = error.message; } };
    const streamCap = setTimeout(() => controller.abort(), limits.timeout_ms + 20000);
    try {
      const response = await request(base + route + '/stream', { headers: appHeaders, signal: controller.signal, redirect: 'error' });
      assert.ok(response.ok && response.body, 'Actual application SSE did not connect');
      const parser = createSSEParser(event => {
        if (['snapshot', 'thinking', 'token', 'tool_args'].includes(event.type)) return;
        captureBytes += JSON.stringify(event).length;
        if (captureBytes > 8 * 1024 * 1024) throw new Error('Bounded SSE evidence limit exceeded');
        events.push(event); monitor.observe(event);
        if (monitor.check()) void cancel();
        if (event.type === 'gate_request') {
          const allow = event.name === 'browser-arm';
          if (!allow) monitor.fail(`unexpected_gate:${event.name}`);
          const flight = api(`${route}/control`, { action: 'gate_decide', gate_id: event.gate_id, allow, reason: 'Owned synthetic release fixture only' }).catch(error => { streamError = error.message; }).finally(() => gates.delete(flight));
          gates.add(flight);
        }
      });
      streamFlight = (async () => {
        const decoder = new TextDecoder();
        for await (const chunk of response.body) parser.push(decoder.decode(chunk, { stream: true }));
        parser.push(decoder.decode()); parser.finish();
        if (!controller.signal.aborted) streamError = 'Application SSE ended unexpectedly';
      })().catch(error => { if (!controller.signal.aborted) streamError = error.message; });
      const prompt = releasePrompt(fixtureURL) + (config.heldTabURL ? '\n我正在接管另一个标签页。请保留那一页及其控制权，用可用的 Agent 页面或新标签完成上述任务，不要要求我交还无关页面。' : '');
      report.model.prompt_sha256 = hash(prompt);
      const begun = await api(route + '/messages', { prompt }); // Deliberately one attempt.
      report.model.run_id = begun.run_id;
      let cancelAt;
      while (!monitor.snapshot().ended) {
        if (streamError) monitor.fail('application_stream_error');
        if (controller.signal.aborted) monitor.fail('application_stream_deadline');
        if (monitor.check() || streamError) {
          if (cancelAt === undefined) { cancelAt = Date.now(); await cancel(); }
          if (Date.now() - cancelAt > 15000) { monitor.fail('missing_run_end_after_cancel'); break; }
        }
        await pause(100);
      }
      report.model.monitor = monitor.snapshot();
      if (streamError) report.model.stream_error = streamError;
      const observed = await oracle();
      report.model.judgement = judgeModel(events, observed, fixtureURL, begun.run_id, report.model.monitor);
      report.model.marker = observed.marker; report.model.receipt_id = observed.writes?.[0]?.receipt_id;
      report.model.passed = report.model.judgement.passed;
    } finally {
      if (!monitor.snapshot().ended) await cancel();
      controller.abort(); clearTimeout(streamCap); await streamFlight; await Promise.allSettled(gates);
      await save('model-events.json', events);
    }
  }
  async function runWorkflow() {
    const workflow = await api('/api/manage/workflows', { name: `${name}-human`, description: 'Temporary owned no-model approval lifecycle check', status: 'ready', nodes: [{ id: 'release_gate', kind: 'human', prompt: 'Approve this synthetic release check only.', fields: [{ key: 'check_id', label: 'Check ID', required: true }], timeout: '5m', on_timeout: 'fail' }] });
    assert.ok(workflow.id, 'Created workflow ID missing'); owned.workflow = workflow.id;
    const root = `/api/workflows/${enc(owned.workflow)}`;
    await api(`/api/manage/workflows/${enc(owned.workflow)}/publish`, { name: 'Synthetic acceptance', desc: 'Owned single human node' });
    const run = await api(root + '/runs', {});
    assert.ok(run.id, 'Created workflow run ID missing'); owned.run = run.id;
    report.workflow.workflow_id = owned.workflow; report.workflow.run_id = owned.run;
    const runPath = root + '/runs/' + enc(owned.run);
    async function waitFor(predicate) {
      const deadline = Date.now() + 30000;
      while (Date.now() < deadline) {
        const value = await api(runPath);
        assert.ok(!value.persistence_error && !value.error, 'Workflow reported persistence/runtime failure');
        if (predicate(value)) return value;
        assert.ok(!['failed', 'cancelled'].includes(value.status), `Workflow ended ${value.status}`);
        await pause(200);
      }
      throw new Error('Workflow phase exceeded 30s');
    }
    const waiting = await waitFor(value => value.nodes?.release_gate?.status === 'waiting_human');
    assert.equal(waiting.status, 'working');
    assert.ok(waiting.nodes.release_gate.human?.actions?.includes('approve'), 'Workflow approval action missing');
    report.workflow.waiting_observed = true;
    await api(runPath + '/nodes/release_gate/resume', { action: 'approve', fields: { check_id: uid } }); // Exactly one approval request.
    const finished = await waitFor(value => value.status === 'completed');
    const gate = finished.nodes?.release_gate;
    assert.ok(gate?.status === 'success' && gate.human?.action === 'approve' && gate.human?.values?.check_id === uid, 'Persisted workflow approval does not match this check');
    const artifact = await api(runPath + '/nodes/release_gate/artifact');
    assert.ok(artifact.node === 'release_gate' && artifact.action === 'approve' && artifact.fields?.check_id === uid, 'Actual human decision artifact missing or mismatched');
    report.workflow.completed_observed = true; report.workflow.decision_artifact_verified = true;
    report.workflow.passed = true;
    await save('workflow.json', { waiting, finished, artifact });
  }
}

export async function loadConfig(argv) {
  assert.ok(argv.length === 1 || (argv.length === 3 && argv[1] === '--workflow-only'), 'Usage: node lab/native-only-release-model.mjs PRIVATE_CONFIG.json [--workflow-only NEW_OUTPUT_DIR]');
  const config = JSON.parse(await readFile(path.resolve(argv[0]), 'utf8'));
  const output = argv[1] === '--workflow-only' ? argv[2] : config.output;
  assert.ok(typeof output === 'string', 'Private config must set a new output directory');
  return { ...config, ...(argv[1] === '--workflow-only' ? { workflowOnly: true } : {}), output: path.resolve(output) };
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const config = await loadConfig(process.argv.slice(2));
    const result = await runReleaseAcceptance(config);
    console.log(JSON.stringify({ scope: result.scope, model: result.model.passed, workflow: result.workflow.passed, passed: result.passed, scope_passed: result.scope_passed, output: config.output }));
    if (!result.scope_passed) process.exitCode = 1;
  } catch { console.error('Release acceptance failed before a report could be saved; check private configuration and output directory.'); process.exitCode = 1; }
}
