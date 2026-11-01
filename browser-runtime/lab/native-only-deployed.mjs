#!/usr/bin/env node
// Deployed image-pair smoke. All writes belong to newly created test objects.
// Does not run models, change deployment configuration, or register panels.
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID, createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { chromium } from 'playwright';

const sha = value => createHash('sha256').update(value).digest('hex');
export const RETIRED = ['api_read', 'auth_status', 'eval'];

export function loopbackOrigin(raw) {
  const u = new URL(raw);
  assert.ok(['http:', 'https:'].includes(u.protocol) && ['127.0.0.1', 'localhost', '[::1]'].includes(u.hostname)
    && !u.username && !u.password && !u.search && !u.hash && u.pathname === '/', 'app URL must be a plain loopback origin');
  return u.origin;
}

export async function command(program, args, input = '') {
  return new Promise((resolve, reject) => {
    const child = spawn(program, args, { stdio: ['pipe', 'pipe', 'pipe'] });
    const chunks = []; let size = 0;
    const timer = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('bounded command timed out')); }, 60000);
    child.on('error', () => { clearTimeout(timer); reject(new Error('required executable unavailable')); });
    child.stdout.on('data', chunk => { size += chunk.length; if (size > 16 * 1024 * 1024) child.kill('SIGKILL'); else chunks.push(chunk); });
    child.stderr.resume(); // Never print Docker errors containing private runtime details.
    child.on('close', code => { clearTimeout(timer); code === 0 ? resolve(Buffer.concat(chunks).toString()) : reject(new Error(`bounded command failed (${code})`)); });
    child.stdin.on('error', () => {});
    child.stdin.end(input);
  });
}

export async function verifyImagePair(config, invoke = command) {
  const result = {};
  for (const key of ['app', 'worker']) {
    const name = config[`${key}Container`], expected = config[`${key}Image`];
    assert.match(name || '', /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/, `${key} container required`);
    assert.match(expected || '', /^sha256:[a-f0-9]{64}$/, `${key} immutable expected image required`);
    // Explicit projection: never retrieve container environment/mount configuration.
    const row = JSON.parse(await invoke('docker', ['inspect', '--format', '{"image":{{json .Image}},"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}null{{end}}}', name]));
    assert.equal(row.image, expected, `${key} image is not the candidate image`);
    assert.equal(row.running, true, `${key} container not running`);
    // App images may have no Docker HEALTHCHECK. Their authenticated API is
    // exercised below; a declared unhealthy check is never ignored.
    assert.ok(row.health === 'healthy' || (key === 'app' && row.health === null), `${key} container not healthy`);
    result[key] = { image: row.image, running: true, health: row.health, ...(row.health === null ? { readiness: 'authenticated API checked by subsequent smoke steps' } : {}) };
  }
  return result;
}

export function httpTransport(base, headers = {}) {
  return async (path, method = 'GET', data) => {
    const res = await fetch(base + path, { method, headers: { ...headers, 'Content-Type': 'application/json', Origin: base },
      body: data === undefined ? undefined : JSON.stringify(data), signal: AbortSignal.timeout(45000) });
    const text = await res.text();
    let body; try { body = text ? JSON.parse(text) : null; } catch { body = null; }
    return { status: res.status, body, headers: Object.fromEntries(res.headers) };
  };
}

// Credentials remain inside the existing worker container. No compose/env file
// is inspected and no worker port is published by this harness.
export function dockerWorkerTransport(container, invoke = command) {
  assert.match(container || '', /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/);
  const source = `let input='';for await(const c of process.stdin)input+=c;const q=JSON.parse(input);const r=await fetch('http://127.0.0.1:'+(process.env.POGO_BROWSER_PORT||7788)+q.path,{method:q.method,headers:{Authorization:'Bearer '+process.env.POGO_BROWSER_TOKEN,'Content-Type':'application/json'},body:q.data===undefined?undefined:JSON.stringify(q.data),signal:AbortSignal.timeout(45000)});console.log(JSON.stringify({status:r.status,body:await r.json()}));`;
  return async (path, method = 'GET', data) => {
    assert.ok(path === '/healthz' || /^\/sessions\/native-only-accept-[A-Za-z0-9_-]+(?:\/(?:state|execute|auth))?$/.test(path), 'worker transport refuses non-test session paths');
    return JSON.parse(await invoke('docker', ['exec', '-i', container, 'node', '--input-type=module', '-e', source], JSON.stringify({ path, method, data })));
  };
}

export async function createFixture({ host = '0.0.0.0', advertisedHost = 'host.docker.internal' } = {}) {
  const nonce = randomUUID(), cookie = randomUUID(), counters = { app: { pages: 0, data: 0, authenticated: 0 }, worker: { pages: 0, data: 0, authenticated: 0 } };
  const prefix = `/native-only-${nonce}`;
  const server = http.createServer((req, res) => {
    const path = new URL(req.url, 'http://fixture.invalid').pathname;
    const match = new RegExp(`^${prefix}/(app|worker)/(page|data)$`).exec(path);
    if (!match || req.method !== 'GET') { res.writeHead(404).end(); return; }
    const [, lane, endpoint] = match, count = counters[lane];
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Frame-Options', 'DENY');
    if (endpoint === 'data') {
      count.data++;
      if (req.headers.cookie?.includes(`native_accept=${cookie}`)) count.authenticated++;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ record: `native-only-${lane}-evidence`, observed: 73 })); return;
    }
    count.pages++;
    res.setHeader('Set-Cookie', `native_accept=${cookie}; HttpOnly; SameSite=Lax; Path=${prefix}/${lane}/`);
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><title>Native-only controlled fixture</title><button id="load" style="width:160px;height:48px" onclick="fetch('data').then(r=>r.json()).then(d=>document.getElementById('result').textContent=d.record)">Read fixture</button><p id="result">Ready</p>`);
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, host, resolve); });
  const port = server.address().port;
  return { url: lane => `http://${advertisedHost}:${port}${prefix}/${lane}/page`, port, counters,
    async close() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); } };
}

function successful(response, message) {
  assert.ok(response.status >= 200 && response.status < 300, `${message}: HTTP ${response.status}`);
  return response.body;
}

export async function workerChecks(worker, fixture, check) {
  const id = `native-only-accept-${randomUUID()}`, base = `/sessions/${id}`;
  let seq = 0;
  const execute = args => worker(`${base}/execute`, 'POST', { op_id: `${id}-${++seq}`, ...args });
  const good = (r, message) => { const value = successful(r, message); assert.equal(value.ok, true, message); return value; };
  let created = false, cleaned = false;
  try {
    successful(await worker('/healthz'), 'worker health');
    // Record ownership before requesting creation: an uncertain reply still
    // requires cleanup of this exclusively generated ID.
    created = true;
    good(await worker(base, 'PUT'), 'worker session creation');
    good(await execute({ action: 'navigate', url: fixture.url('worker') }), 'original-site navigation');
    const state = successful(await worker(`${base}/state`), 'worker state');
    check('worker state is native without credential-helper surface', state.backend === 'native' && !('api_access' in state));
    for (const action of RETIRED) {
      const retired = { action, path: '/data', expression: 'fetch("data")' };
      const single = await execute(retired);
      check(`worker rejects retired ${action}`, single.body?.ok === false && /retired/i.test(single.body.error));
      const batch = await execute({ action: 'batch', actions: [{ action: 'click', target: '#load' }, retired] });
      check(`batch prevalidation rejects ${action} before earlier click`, batch.body?.ok === false && /retired/i.test(batch.body.error));
    }
    check('retired worker auth route is gone', (await worker(`${base}/auth`, 'POST', { action: 'grant' })).status === 410);
    check('retired helpers produced no website data request', fixture.counters.worker.data === 0);
    good(await execute({ action: 'click', target: '#load' }), 'native click');
    good(await execute({ action: 'wait', text: 'native-only-worker-evidence', seconds: 3 }), 'fixture result');
    const dom = good(await execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.title', returnByValue: true } }), 'real CDP evaluation');
    check('real CDP evaluates the same native page', JSON.stringify(dom.data).includes('Native-only controlled fixture'));
    const network = good(await execute({ action: 'devtools', method: 'network.list', params: { limit: 100 } }), 'captured Network inventory');
    const recorded = network.data?.items?.find(item => item.url === fixture.url('worker').replace(/page$/, 'data'));
    check('website data request has recorded request identity', typeof recorded?.request_id === 'string');
    const detail = good(await execute({ action: 'devtools', method: 'network.read', params: { request_id: recorded.request_id } }), 'captured Network response');
    check('Network evidence contains the observed response body', JSON.stringify(detail.data).includes('native-only-worker-evidence'));
    check('observing evidence never replayed the website request', fixture.counters.worker.data === 1 && fixture.counters.worker.authenticated === 1);
  } finally {
    if (created) {
      successful(await worker(base, 'DELETE'), 'worker session cleanup');
      cleaned = (await worker(`${base}/state`)).status === 404;
      check('test worker session deleted and cannot be reopened by state', cleaned);
    }
  }
  return { test_session_deleted: cleaned, fixture: { ...fixture.counters.worker }, scope: 'deployed worker transport; full agent bridge is a separate model acceptance' };
}

export async function migrationChecks(app, check) {
  const before = successful(await app('/api/panels'), 'registered entry inventory');
  const panels = before.panels || [];
  const registered = panels[0]?.entry || panels[0]?.path;
  const original = 'https://synthetic.example.invalid/keep?q=1#part';
  const missing = `/panels/native-only-missing-${randomUUID()}/a?q=1#part`;
  const inputs = [original, missing, ...(registered ? [registered] : [])];
  const reply = await app('/api/browser/migrate-urls', 'POST', { urls: inputs });
  const mappings = successful(reply, 'saved URL migration').mappings;
  check('original bookmark query and fragment survive conversion', mappings[0].resolved_url === original);
  check('unresolved saved entry is explicitly repairable', !!mappings[1].error && !mappings[1].resolved_url);
  if (registered) check('existing registered shortcut resolves without registration', /^https?:\/\//.test(mappings[2]?.resolved_url || ''));
  check('migration endpoint never mutates registered entries', JSON.stringify(before) === JSON.stringify(successful(await app('/api/panels'), 'unchanged entry inventory')));
  check('migration response is not cached', reply.headers?.['cache-control'] === 'no-store');
  // Do not write site names, original addresses, or other user registry data to reports.
  return { registered, missing, report: { registered_entry_checked: !!registered, registered_count: panels.length, registry_digest: sha(JSON.stringify(before)), original_url_preserved: true } };
}

async function uiMigration({ config, agentID, sessionID, migration, fixture, check }) {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1360, height: 900 }, locale: 'zh-CN' });
  const page = await context.newPage();
  const violations = [], exceptions = [], requests = [];
  const tabsKey = `pogo:studio:${agentID}:hub-tabs`;
  const tabs = { open: [
    { id: 'browser:old-first', kind: 'browser', label: 'Synthetic saved page', url: fixture.url('app'), closable: true },
    { id: 'panel:old-second', kind: 'panel', label: 'Synthetic unresolved tab', path: migration.missing, closable: true },
  ], active: 'browser:old-first' };
  const cards = [{ id: 'synthetic-original', title: 'Synthetic original', url: fixture.url('app') },
    { id: 'synthetic-missing', title: 'Synthetic needs repair', url: migration.missing },
    ...(migration.registered ? [{ id: 'synthetic-registered', title: 'Synthetic registered entry', url: migration.registered }] : [])];
  const rawTabs = JSON.stringify(tabs), rawCards = JSON.stringify(cards);
  try {
    if (config.cookie) {
      const cookies = config.cookie.split(';').map(value => value.trim()).filter(Boolean).map(pair => {
        const index = pair.indexOf('='); assert.ok(index > 0, 'cookie pair must have a name');
        return { name: pair.slice(0, index), value: pair.slice(index + 1), url: config.app };
      });
      await context.addCookies(cookies);
    }
    await context.addInitScript(({ token, origin, tabsKey, sheetKey, rawTabs, rawCards }) => {
      if (location.origin !== origin) return;
      if (token) sessionStorage.setItem('pogo-token', token);
      // Seed once; the reload below verifies idempotence rather than reseeding.
      if (!localStorage.getItem('native-only-controlled-seeded')) {
        localStorage.setItem('pogo:nav-cards', rawCards);
        localStorage.setItem(tabsKey, rawTabs);
        localStorage.setItem(sheetKey, 'hub');
        localStorage.setItem('native-only-controlled-seeded', '1');
      }
    }, { token: config.token || '', origin: config.app, tabsKey, sheetKey: `pogo:studio:${agentID}:active-sheet`, rawTabs, rawCards });
    // Real APIs and built assets only. Refuse accidental prompts, writes to a
    // pre-existing conversation, or registration; do not fabricate responses.
    await page.route('**/*', async route => {
      const req = route.request(), u = new URL(req.url()), method = req.method();
      // Maintenance credentials must never reach the fixture or another site.
      if (u.origin !== config.app) { await route.continue(); return; }
      const headers = { ...req.headers(), ...(config.releaseToken ? { 'X-Pogo-Release': config.releaseToken } : {}) };
      if (!u.pathname.startsWith('/api/')) { await route.continue({ headers }); return; }
      requests.push({ path: u.pathname, method });
      const session = /^\/api\/sessions\/([^/]+)/.exec(u.pathname);
      const staticSessions = ['activity', 'search', 'sample'];
      const denied = u.pathname.endsWith('/messages') || (session && !staticSessions.includes(session[1]) && session[1] !== sessionID)
        || (method !== 'GET' && u.pathname === '/api/panels') || u.pathname.endsWith('/browser/input')
        || u.pathname.endsWith('/browser/auth') || u.pathname.endsWith('/control');
      if (denied) { violations.push(`${method} ${u.pathname}`); await route.abort(); }
      else await route.continue({ headers });
    });
    page.on('pageerror', () => exceptions.push('uncaught page exception'));
    await page.goto(`${config.app}/manage/agents/${agentID}/chat?c=${sessionID}`, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(key => localStorage.getItem(`${key}:native-only:v1`) === 'done', tabsKey, { timeout: 30000 });
    await page.locator('.native-browser img').waitFor({ state: 'visible', timeout: 30000 });
    const storage = await page.evaluate(key => ({ tabs: JSON.parse(localStorage.getItem(key)), backup: JSON.parse(localStorage.getItem(`${key}:native-only:v1:backup`)), bookmarks: JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1')) }), tabsKey);
    check('fresh real UI backs up synthetic legacy entries before migration', storage.backup.tabs === rawTabs && storage.backup.cards === rawCards);
    check('fresh real UI restores one page from legacy entries without reopening saved sites', storage.tabs.open.every(tab => tab.kind !== 'panel') && await page.locator('.native-browser:visible').count() === 1);
    check('fresh real UI keeps unresolved saved bookmarks repairable', storage.bookmarks.some(card => card.id === 'legacy-card:synthetic-missing' && card.needsRepair));
    if (migration.registered) check('fresh real UI migrates a registered legacy bookmark', storage.bookmarks.some(card => card.id === 'legacy-card:synthetic-registered' && !card.needsRepair && /^https?:/.test(card.url)));
    check('fresh real UI displays worker pixels for its already navigated page', await page.locator('.native-browser img').evaluate(image => image.complete && image.naturalWidth > 0));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.locator('.native-browser img').waitFor({ state: 'visible', timeout: 30000 });
    const after = await page.evaluate(() => JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1')));
    check('real UI migration is idempotent on reload', JSON.stringify(after) === JSON.stringify(storage.bookmarks));
    check('UI migration does not navigate, register proxies, or send prompts', violations.length === 0);
    check('UI migration uses real API conversion without JavaScript exceptions', requests.some(req => req.path === '/api/browser/migrate-urls') && exceptions.length === 0);
    return { isolated_browser_context: true, synthetic_storage: true, actual_user_local_storage_accessed: false, registered_entry_checked: !!migration.registered,
      native_views: 1, errors: exceptions.length, forbidden_requests: violations.length };
  } finally { await context.close(); await browser.close(); }
}

export async function runAcceptance(config) {
  config.app = loopbackOrigin(config.app);
  assert.ok(config.token || config.cookie, 'app token or login cookie is required');
  assert.ok(config.model, 'configured model ID required for the temporary Agent; no model is called');
  assert.ok(!config.workerURL || config.workerToken, 'direct worker transport requires its token');
  await mkdir(config.output, { mode: 0o700 }); // Refuse to overwrite an earlier report.
  const report = { schema: 'native-only-deployed-smoke-v1', started_at: new Date().toISOString(), paid_model_calls: 0, passed: false, checks: [], cleanup: {},
    scope: 'deployed app human-control bridge, isolated real UI migration, and worker CDP; no real-business-site or model acceptance',
    release_hook: { worker: false, human_takeover: false, model: false, workflow: false } };
  const check = (name, value) => { assert.ok(value, name); report.checks.push(name); };
  const maintenance = config.releaseToken ? { 'X-Pogo-Release': config.releaseToken } : {};
  const headers = { ...maintenance, ...(config.token ? { Authorization: `Bearer ${config.token}` } : {}), ...(config.cookie ? { Cookie: config.cookie } : {}) };
  const app = httpTransport(config.app, headers), unauthenticated = httpTransport(config.app, maintenance);
  const worker = config.workerURL ? httpTransport(config.workerURL, { Authorization: `Bearer ${config.workerToken}` }) : dockerWorkerTransport(config.workerContainer);
  const sessionIDs = [];
  let agentID, fixture;
  try {
    report.images = await verifyImagePair(config);
    check('running app and healthy worker match the immutable candidate image pair', true);
    fixture = await createFixture({ advertisedHost: config.fixtureHost || 'host.docker.internal' });
    const migration = await migrationChecks(app, check); report.migration_endpoint = migration.report;
    const agent = successful(await app('/api/manage/agents', 'POST', { name: `Native-only acceptance ${randomUUID().slice(0, 8)}`, description: 'Temporary synthetic release smoke; no model prompts.', instructions: 'Synthetic browser acceptance only.', model: config.model, models: [config.model], skills: [], mcp_servers: [], status: 'ready' }), 'temporary Agent creation');
    agentID = agent.id; assert.match(agentID, /^ag_/); report.test_agent_id = agentID;
    for (let i = 0; i < 2; i++) {
      const session = successful(await app(`/api/apps/${agentID}/sessions`, 'POST', {}), 'temporary session creation');
      assert.ok(session.id, 'created session ID'); sessionIDs.push(session.id);
    }
    report.test_session_ids = sessionIDs;
    const [sid, other] = sessionIDs, base = `/api/sessions/${sid}/browser`;
    const state = successful(await app(base), 'native state without SSE');
    check('app obtains native worker state before any SSE client exists', state.backend === 'native');
    check('public browser state hides worker routing and credential helpers', !['worker_id', 'session_id', 'lease_token', 'api_access'].some(key => key in state));
    check('anonymous request cannot inspect the owned session', [401, 403, 404].includes((await unauthenticated(base)).status));
    if (config.otherToken) check('different account cannot inspect the owned session', (await httpTransport(config.app, { ...maintenance, Authorization: `Bearer ${config.otherToken}` })(base)).status === 404);
    report.multiuser_owner_test = config.otherToken ? 'passed' : 'not run; no second account token supplied';
    check('old browser credential endpoint returns retirement error', (await app(`${base}/auth`, 'POST', { action: 'grant' })).status === 410);
    check('old SSE browser result cannot complete native operations', (await app(`/api/sessions/${sid}/control`, 'POST', { action: 'browser_result', id: 'synthetic-retired', ok: true })).status === 410);
    const human = successful(await app(`${base}/control`, 'POST', { mode: 'human', context_ref: state.context_ref }), 'human control');
    check('human takeover obtains an opaque native lease', human.control === 'human' && !!human.lease_token);
    const otherInitial = successful(await app(`/api/sessions/${other}/browser`), 'second session context');
    successful(await app(`/api/sessions/${other}/browser/control`, 'POST', { mode: 'human', context_ref: otherInitial.context_ref }), 'second session human control');
    const ownerState = successful(await app(base), 'owned input binding');
    const otherState = successful(await app(`/api/sessions/${other}/browser`), 'other input binding');
    const binding = s => ({ target_tab: s.active_tab, context_ref: s.context_ref, generation: s.generation });
    const foreign = await app(`/api/sessions/${other}/browser/input`, 'POST', { type: 'navigate', url: fixture.url('app'), lease_token: human.lease_token, ...binding(otherState) });
    check('human lease cannot control another session', [403, 409].includes(foreign.status));
    const navigated = successful(await app(`${base}/input`, 'POST', { type: 'navigate', url: fixture.url('app'), lease_token: human.lease_token, ...binding(ownerState) }), 'human navigation through app');
    check('app bridge navigates the original URL without iframe execution', navigated.url === fixture.url('app') && fixture.counters.app.pages === 1);
    const legacy = await app(`${base}/input`, 'POST', { type: 'navigate', url: migration.missing, lease_token: human.lease_token, ...binding(ownerState) });
    check('live navigation rejects legacy panel shortcuts', legacy.status === 400);
    successful(await app(`${base}/control`, 'POST', { mode: 'agent', context_ref: ownerState.context_ref, lease_token: human.lease_token }), 'return agent control');
    report.release_hook.human_takeover = true;
    report.ui_migration = await uiMigration({ config, agentID, sessionID: sid, migration, fixture, check });
    check('migration never contacts saved original sites automatically', fixture.counters.app.pages === 1 && fixture.counters.app.data === 0);
    report.worker = await workerChecks(worker, fixture, check);
    report.release_hook.worker = true;
    report.fixture = fixture.counters;
    report.passed = true;
  } catch (error) {
    // Assertion labels are authored here; transport responses are never logged.
    report.error = String(error.message).split('\n')[0].slice(0, 400);
  } finally {
    for (const sid of sessionIDs) {
      try {
        successful(await app(`/api/sessions/${sid}`, 'DELETE'), 'temporary session deletion');
        assert.equal((await app(`/api/sessions/${sid}`)).status, 404);
        report.cleanup[sid] = 'deleted and verified';
      } catch { report.cleanup[sid] = 'cleanup failed; operator must inspect test ID'; report.passed = false; }
    }
    if (agentID) {
      try { successful(await app(`/api/manage/agents/${agentID}`, 'DELETE'), 'temporary Agent retirement'); report.cleanup.agent = 'retired (normal soft-delete)'; }
      catch { report.cleanup.agent = 'cleanup failed; operator must inspect test ID'; report.passed = false; }
    }
    await fixture?.close();
    report.finished_at = new Date().toISOString();
    await writeFile(`${config.output}/smoke.json`, JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
  }
  return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const e = process.env;
  runAcceptance({ app: e.POGO_ACCEPT_APP_URL, token: e.POGO_ACCEPT_APP_TOKEN, cookie: e.POGO_ACCEPT_APP_COOKIE,
    otherToken: e.POGO_ACCEPT_OTHER_TOKEN, model: e.POGO_ACCEPT_MODEL, appContainer: e.POGO_ACCEPT_APP_CONTAINER,
    workerContainer: e.POGO_ACCEPT_WORKER_CONTAINER, appImage: e.POGO_ACCEPT_APP_IMAGE, workerImage: e.POGO_ACCEPT_WORKER_IMAGE,
    workerURL: e.POGO_ACCEPT_WORKER_URL, workerToken: e.POGO_ACCEPT_WORKER_TOKEN, fixtureHost: e.POGO_ACCEPT_FIXTURE_HOST,
    releaseToken: e.RELEASE_TOKEN, output: process.argv[2] }).then(report => {
    console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, report: `${process.argv[2]}/smoke.json`, ...report.release_hook }));
    if (!report.passed) process.exitCode = 1;
  }).catch(() => { console.error('Native-only deployed smoke could not start; check required inputs.'); process.exitCode = 1; });
}
