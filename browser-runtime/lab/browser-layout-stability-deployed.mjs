// Actual Studio -> Go -> Docker Chromium, with owned synthetic site and session.
// Captures every animation-frame client geometry during a slow real WS click.
// No model turn, user session, browser tab or deployment configuration is edited.
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { createHash, randomUUID } from 'node:crypto';
import path from 'node:path';
import { chromium } from 'playwright';
import { verifyImagePair } from './native-only-deployed.mjs';
import { startLayoutProbe, finishLayoutProbe } from './browser-layout-probe.mjs';
const [file, output] = process.argv.slice(2);
assert.ok(file && output && process.argv.length === 4, 'PRIVATE_CONFIG NEW_OUTPUT required');
const cfg = JSON.parse(await readFile(file, 'utf8'));
assert.match(cfg.appURL, /^http:\/\/(?:127\.0\.0\.1|localhost):[0-9]+\/?$/);
cfg.appURL = new URL(cfg.appURL).origin;
const mode = cfg.layoutMode === 'baseline' ? 'baseline' : 'optimized';
await mkdir(output, { mode: 0o700 });
const report = { kind: 'actual-docker-browser-layout', mode, scope: 'Synthetic slow human click and bound keyboard operation via actual live WebSocket / HTTP, Studio, Go and Docker Chromium. Per-frame geometry and viewport stability; not physical input latency or a real-model action test.', passed: false, checks: [], samples: [], viewport_requests: [] };
const headers = { Authorization: 'Bearer ' + cfg.token, Origin: cfg.appURL, ...(cfg.releaseToken ? { 'X-Pogo-Release': cfg.releaseToken } : {}) };
const api = async (route, body, method = body === undefined ? 'GET' : 'POST') => {
  const response = await fetch(cfg.appURL + route, { method, headers: { ...headers, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000) });
  assert.ok(response.ok, `${method} ${route}: HTTP ${response.status}`); return response.status === 204 ? null : response.json();
};
const check = (label, pass) => { assert.ok(pass, label); report.checks.push(label); };
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const until = async (predicate, label, timeout = 15000) => { const end = Date.now() + timeout; while (Date.now() < end) { if (await predicate()) return; await pause(35); } throw new Error(label); };
let browser, page, session, agent, fixture, lease;
const failures = [], states = [];
const prefix = '/layout-' + randomUUID(); let latest = { clicks: 0, viewport: null };
const html = `<!doctype html><meta charset="utf-8"><title>Layout stability fixture</title><style>*{box-sizing:border-box}body{margin:0;background:#e9f1ea;font:20px system-ui;color:#111}h1{margin:20px}button{position:absolute;left:40px;top:160px;width:210px;height:60px}#dialog{top:260px}</style><h1>Stable browser layout</h1><button id="slow">Slow click</button><button id="dialog">Open prompt</button><script>let clicks=0;function emit(){fetch('${prefix}/receipt',{method:'POST',body:JSON.stringify({clicks,viewport:{width:innerWidth,height:innerHeight}})})}slow.onclick=()=>{const end=performance.now()+800;while(performance.now()<end){};clicks++;slow.textContent='Click '+clicks;emit()};dialog.onclick=()=>{prompt('Layout acceptance prompt','');emit()};addEventListener('resize',emit);emit();</script>`;
try {
  report.images = await verifyImagePair(cfg);
  report.script_sha256 = createHash('sha256').update(await readFile(new URL(import.meta.url))).digest('hex');
  report.probe_sha256 = createHash('sha256').update(await readFile(new URL('./browser-layout-probe.mjs', import.meta.url))).digest('hex');
  fixture = http.createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    if (req.method === 'GET' && req.url === prefix + '/page') { res.setHeader('Content-Type', 'text/html; charset=utf-8'); res.end(html); return; }
    if (req.method === 'POST' && req.url === prefix + '/receipt') { let raw = ''; for await (const chunk of req) { raw += chunk; if (raw.length > 2048) { res.writeHead(413).end(); return; } } try { latest = JSON.parse(raw); res.end('ok'); } catch { res.writeHead(400).end(); } return; }
    res.writeHead(404).end();
  });
  await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve));
  const url = `http://${cfg.fixtureHost || 'host.docker.internal'}:${fixture.address().port}${prefix}/page`;
  const models = await api('/api/models'), model = models.models.find(m => m.id === cfg.model && m.configured) || models.models.find(m => m.configured); assert.ok(model);
  agent = await api('/api/manage/agents', { name: 'Layout acceptance ' + randomUUID().slice(0, 8), description: 'Owned synthetic layout verification', instructions: 'No model task.', model: model.id, models: [model.id], tools: ['browser'], skills: [], mcp_servers: [], status: 'ready' });
  session = await api(`/api/apps/${agent.id}/sessions`, {});
  const state = await api(`/api/sessions/${session.id}/browser`), binding = { context_ref: state.context_ref, tab_id: state.viewed_tab || state.active_tab };
  const viewedState = () => api(`/api/sessions/${session.id}/browser?${new URLSearchParams(binding)}`);
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1700, height: 1050 }, locale: 'zh-CN', ...(cfg.releaseToken ? { extraHTTPHeaders: { 'X-Pogo-Release': cfg.releaseToken } } : {}) });
  page = await context.newPage();
  await page.route('**/*', async route => { const request = route.request(), u = new URL(request.url()), bound = /^\/api\/sessions\/([^/]+)/.exec(u.pathname); if (u.origin !== cfg.appURL || u.pathname.endsWith('/messages') || bound && !['activity', 'search', 'sample', session.id].includes(bound[1])) { failures.push({ kind: 'outside-owned-surface', path: u.pathname }); await route.abort(); return; } await route.continue(); });
  page.on('response', async response => { if (new URL(response.url()).pathname === `/api/sessions/${session.id}/browser/control` && response.ok()) { try { const body = await response.json(); if (body.lease_token) lease = body.lease_token; } catch {} } });
  page.on('pageerror', error => failures.push({ kind: 'UI', error: error.message.slice(0, 250) }));
  page.on('websocket', ws => { if (!ws.url().includes('/browser/live?')) return;
    ws.on('framesent', event => { try { const m = JSON.parse(String(event.payload)); if (m.type === 'viewport') report.viewport_requests.push({ width: m.width, height: m.height, t: Date.now() }); } catch {} });
    ws.on('framereceived', event => { try { const m = JSON.parse(String(event.payload)); if (m.state) states.push({ busy: !!m.state.busy, dialog: !!m.state.dialog, viewport: m.state.viewport, phase: m.state.activity?.last_action?.phase, actor: m.state.activity?.last_action?.actor, t: Date.now() }); if (m.type === 'error') failures.push({ kind: 'live', code: m.code }); } catch {} });
  });
  const target = new URL(cfg.appURL + `/manage/agents/${agent.id}/chat`); target.searchParams.set('c', session.id); target.searchParams.set('token', cfg.token); await page.goto(target.href);
  await page.getByRole('button', { name: /^面板(?: ·.*)?$|^Panels?(?: ·.*)?$/ }).click();
  const pane = page.locator('.native-browser:visible'), tile = page.locator('.hub-empty .tile').filter({ hasText: /浏览器|Browser/i });
  if (await tile.isVisible().catch(() => false)) try { await tile.click({ timeout: 1500 }); } catch (error) { if (!await pane.isVisible().catch(() => false)) throw error; }
  await pane.waitFor(); await pane.getByRole('button', { name: /接管|Take.*control|Take over/i }).click();
  const address = pane.getByRole('textbox', { name: /原站地址|Original URL|URL/ }).first(); await address.fill(url); await address.press('Enter');
  const img = pane.locator('.native-page img');
  const writable = async () => { await img.waitFor(); await pane.locator('.native-page:not(.readonly)').waitFor({ timeout: 20000 }); await page.waitForFunction(() => { const i = document.querySelector('.native-page img'); return i?.complete && i.naturalWidth > 0; }); };
  const point = async (x, y) => { const s = await viewedState(); return img.evaluate((el, { x, y, viewport }) => { const b = el.getBoundingClientRect(), k = Math.min(b.width / viewport.width, b.height / viewport.height); return { x: b.x + (b.width - viewport.width * k) / 2 + x * k, y: b.y + (b.height - viewport.height * k) / 2 + y * k }; }, { x, y, viewport: s.viewport }); };
  await writable(); await pause(900); assert.ok(lease, 'owned human lease receipt required');
  for (const layout of ['split', 'immersive']) {
    if (layout === 'immersive') {
      const before = (await viewedState()).viewport, count = report.viewport_requests.length;
      await page.locator('.immersion-toggle').click(); await page.locator('.shell.browser-immersive').waitFor(); await page.locator('.immersive-chat-head > button').first().click();
      await until(async () => (await viewedState()).viewport.width > before.width, 'immersive mode must reflow actual browser viewport');
      await writable(); await pause(1000);
      check('switching to immersive mode still reflows actual browser', report.viewport_requests.length > count && latest.viewport.width > before.width);
    }
    for (let n = 0; n < 2; n++) {
      await writable(); await pause(400);
      const before = (await viewedState()).viewport, vp = report.viewport_requests.length, st = states.length, clicks = latest.clicks;
      await startLayoutProbe(page); await pause(60);
      if (n === 0) { const p = await point(120, 190); await page.mouse.click(p.x, p.y); }
      else {
        // The direct WS path intentionally does not mark ordinary live input
        // busy. Exercise the public bound-operation path too: it drives the
        // same activity/busy status as Agent actions without running a model.
        const state = await viewedState();
        await api(`/api/sessions/${session.id}/browser/input`, { type: 'key', key: 'Enter', context_ref: binding.context_ref, target_tab: binding.tab_id, generation: state.generation, lease_token: lease });
      }
      await until(() => latest.clicks === clicks + 1, 'one synthetic click reaches the website once'); await writable(); await pause(650);
      const sample = await finishLayoutProbe(page), after = (await viewedState()).viewport;
      const summary = { name: `${layout}/${n === 0 ? 'live-click' : 'bound-key-operation'}`, ...sample, viewport_requests: report.viewport_requests.length - vp, remote_before: before, remote_after: after, busy_states: states.slice(st).filter(s => s.busy).length };
      report.samples.push(summary);
      if (n === 1) check(`${summary.name}: real busy state and status are observed`, summary.busy_states > 0 && summary.status_samples > 0);
      else check(`${summary.name}: trusted live input executes exactly once`, latest.clicks === clicks + 1);
      if (mode === 'optimized') {
        check(`${summary.name}: operation does not change canvas or drawn image`, summary.max_delta <= .5);
        check(`${summary.name}: operation does not resize remote viewport`, summary.viewport_requests === 0 && JSON.stringify(before) === JSON.stringify(after));
      }
    }
    // A native prompt is an independent controlled source of dialog feedback.
    await writable(); await pause(300); await startLayoutProbe(page);
    const p = await point(120, 290); await page.mouse.click(p.x, p.y); await pane.locator('.native-dialog').waitFor(); await pause(350);
    check(`${layout}: real native dialog is readable`, (await pane.locator('.native-dialog').innerText()).includes('Layout acceptance prompt'));
    await pane.locator('.native-dialog-actions button').first().click(); await pane.locator('.native-dialog').waitFor({ state: 'hidden' }); await writable(); await pause(500);
    const dialog = { name: `${layout}/dialog`, ...await finishLayoutProbe(page) }; report.samples.push(dialog);
    if (mode === 'optimized') check(`${layout}: native dialog does not change browser geometry`, dialog.max_delta <= .5);
  }
  const beforeResize = latest.viewport.width; await page.setViewportSize({ width: 2050, height: 1100 });
  await until(() => latest.viewport.width > beforeResize, 'a real window resize must still reflow the browser');
  check('actual window resize still reaches remote document', true);
  check('owned page receives exactly four slow clicks', latest.clicks === 4);
  check('no escaped requests or client/live errors', failures.length === 0);
  report.fixture = latest; report.passed = true; await page.screenshot({ path: path.join(output, 'layout.png') });
} catch (error) { report.error = String(error.message).replaceAll(cfg.token, '[redacted]').replace(/ticket=[^&\s"']+/g, 'ticket=[redacted]'); await page?.screenshot({ path: path.join(output, 'failure.png') }).catch(() => {}); }
finally {
  report.failures = failures; await browser?.close();
  if (session) try { await api(`/api/sessions/${session.id}`, undefined, 'DELETE'); report.session_cleaned = true; } catch { report.session_cleaned = false; report.passed = false; }
  if (agent) try { await api(`/api/manage/agents/${agent.id}`, undefined, 'DELETE'); report.agent_cleaned = true; } catch { report.agent_cleaned = false; report.passed = false; }
  if (fixture) { fixture.closeAllConnections(); await new Promise(resolve => fixture.close(resolve)); }
  await writeFile(path.join(output, 'summary.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, mode, checks: report.checks.length, max_delta: Math.max(...report.samples.map(s => s.max_delta)), error: report.error, output }));
if (!report.passed) process.exitCode = 1;
