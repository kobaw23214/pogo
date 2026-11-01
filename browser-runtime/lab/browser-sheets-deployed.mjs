#!/usr/bin/env node
// Real built UI -> Go bridge -> Chromium. Uses only exclusively owned test
// sessions, isolated browser storage and a nonce-scoped synthetic website.
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { chromium } from 'playwright';
import { httpTransport, loopbackOrigin } from './native-only-deployed.mjs';

const cfg = JSON.parse(await readFile(process.argv[2], 'utf8'));
const output = process.argv[3];
assert.ok(output && !output.includes('/private/'), 'fresh evidence directory required');
const origin = loopbackOrigin(cfg.appURL);
await mkdir(output, { recursive: false, mode: 0o700 });
const report = { scope: 'Actual built UI, Go and Chromium; independent browser Sheets in an owned session; synthetic website only', passed: false, checks: [], cleanup: {} };
const check = (name, ok) => { assert.ok(ok, name); report.checks.push(name); };
const good = (r, label) => { assert.ok(r.status >= 200 && r.status < 300, `${label}: HTTP ${r.status}`); return r.body; };
const api = httpTransport(origin, { Authorization: `Bearer ${cfg.token}`, ...(cfg.releaseToken ? { 'X-Pogo-Release': cfg.releaseToken } : {}) });
const nonce = randomUUID();
const prefix = `/sheets-${nonce}`;
const counts = { A: { pages: 0, saves: 0, value: '' }, B: { pages: 0, saves: 0, value: '' } };
let site, browser, context, agent, sid, releaseControlResponse;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function eventually(predicate, label, timeout = 15000) {
  const until = Date.now() + timeout;
  while (Date.now() < until) { if (await predicate()) return; await sleep(80); }
  throw new Error(label);
}
try {
  site = http.createServer(async (req, res) => {
    const u = new URL(req.url, 'http://fixture.invalid');
    const lane = u.pathname === `${prefix}/A` ? 'A' : u.pathname === `${prefix}/B` ? 'B' : null;
    res.setHeader('Cache-Control', 'no-store');
    if (!lane) { res.writeHead(404).end(); return; }
    if (req.method === 'POST') {
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const value = Buffer.concat(chunks).toString();
      counts[lane].saves++; counts[lane].value = value;
      res.setHeader('Content-Type', 'application/json'); res.end('{"saved":true}'); return;
    }
    if (req.method !== 'GET') { res.writeHead(405).end(); return; }
    counts[lane].pages++;
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><title>Sheet ${lane}</title><style>html,body{margin:0;min-height:100%;background:${lane === 'A' ? '#d9f0e6' : '#f5decc'};font:24px system-ui}h1{position:absolute;top:20px;left:40px}input{position:absolute;left:40px;top:130px;width:320px;height:50px;font:24px system-ui}button{position:absolute;left:40px;top:230px;width:240px;height:60px;font:24px system-ui}</style><h1>Independent Sheet ${lane}</h1><input id="value" aria-label="Synthetic value"><button onclick="fetch(location.pathname,{method:'POST',body:document.getElementById('value').value}).then(()=>this.textContent='Saved ${lane}')">Save ${lane}</button>`);
  });
  await new Promise(resolve => site.listen(0, '0.0.0.0', resolve));
  const siteURL = lane => `http://host.docker.internal:${site.address().port}${prefix}/${lane}`;
  agent = good(await api('/api/manage/agents', 'POST', { name: `Sheets acceptance ${nonce.slice(0, 8)}`, description: 'Temporary isolated browser Sheet check', instructions: 'Controlled acceptance only', model: cfg.model, models: [cfg.model], skills: [], mcp_servers: [], status: 'ready' }), 'owned agent');
  sid = good(await api(`/api/apps/${agent.id}/sessions`, 'POST', {}), 'owned session').id;
  const base = `/api/sessions/${sid}/browser`;
  const discovery = () => api(base).then(r => good(r, 'discovery'));
  const initial = await discovery();
  const ref = initial.context_ref, a = initial.active_tab;
  check('context has a public stable identity and initial native page', /^ctx_[a-f0-9]{24}$/.test(ref) && /^t[1-9][0-9]*$/.test(a));
  const target = tab => `${base}?${new URLSearchParams({ tab_id: tab, context_ref: ref })}`;
  browser = await chromium.launch({ headless: true });
  context = await browser.newContext({ viewport: { width: 1540, height: 1000 }, locale: 'zh-CN' });
  const page = await context.newPage();
  const errors = [], denied = [], inputBindings = [], controls = [];
  let delayReturnControl = false, heldControlResponse = false;
  await context.addInitScript(({ app, token, key }) => {
    if (location.origin !== app) return;
    sessionStorage.setItem('pogo-token', token);
    if (!localStorage.getItem('sheets-controlled-seeded')) {
      localStorage.setItem(`pogo:studio:${key}:active-sheet`, 'hub');
      localStorage.setItem('sheets-controlled-seeded', '1');
    }
  }, { app: origin, token: cfg.token, key: agent.id });
  page.on('pageerror', () => errors.push('client exception'));
  await page.route('**/*', async route => {
    const request = route.request(), u = new URL(request.url());
    const session = /^\/api\/sessions\/([^/]+)/.exec(u.pathname);
    if (u.origin !== origin || (session && !['activity', 'search', 'sample'].includes(session[1]) && session[1] !== sid) || u.pathname.endsWith('/messages')) {
      denied.push('outside owned surface'); await route.abort(); return;
    }
    if (u.pathname === `${base}/input`) {
      const body = request.postDataJSON();
      inputBindings.push({ type: body.type, tab: body.target_tab, context: body.context_ref, generation: body.generation });
    }
    const headers = { ...request.headers(), ...(cfg.releaseToken ? { 'X-Pogo-Release': cfg.releaseToken } : {}) };
    if (u.pathname === `${base}/control`) {
      controls.push(request.postDataJSON().mode);
      if (delayReturnControl && request.postDataJSON().mode === 'agent') {
        delayReturnControl = false;
        const response = await route.fetch({ headers });
        await new Promise(resolve => { releaseControlResponse = resolve; heldControlResponse = true; });
        await route.fulfill({ response }); return;
      }
    }
    await route.continue({ headers });
  });
  const sheet = tab => page.locator(`button[data-browser-tab-id="${tab}"]`);
  const pane = () => page.locator('.native-browser:visible');
  const selected = async () => page.locator('button[data-browser-tab-id].selected,button[data-browser-tab-id].active,[aria-selected="true"][data-browser-tab-id]').first().getAttribute('data-browser-tab-id');
  const waitPixels = async lane => {
    await page.waitForFunction(which => {
      const img = [...document.querySelectorAll('.native-page img')].find(el => el.getBoundingClientRect().width > 0);
      if (!img?.complete || !img.naturalWidth) return false;
      const canvas = document.createElement('canvas'); canvas.width = img.naturalWidth; canvas.height = img.naturalHeight;
      const draw = canvas.getContext('2d'); draw.drawImage(img, 0, 0);
      const p = draw.getImageData(canvas.width - 30, canvas.height - 30, 1, 1).data;
      return which === 'A' ? p[0] > 205 && p[0] < 230 && p[1] > 235 && p[2] > 220 && p[2] < 240 : p[0] > 235 && p[1] > 210 && p[1] < 230 && p[2] > 190 && p[2] < 215;
    }, lane, { timeout: 20000 });
  };
  const navigate = async lane => {
    const input = pane().getByRole('textbox', { name: '原站地址', exact: true });
    await input.fill(siteURL(lane)); await input.press('Enter'); await waitPixels(lane);
    await pane().locator('.native-page:not(.readonly)').waitFor({ timeout: 15000 });
  };
  const add = async () => {
    const before = (await discovery()).tabs.map(t => t.id);
    await page.locator('.tab.add:visible').click();
    await page.getByRole('menuitem', { name: '浏览器', exact: true }).click();
    let added;
    await eventually(async () => { added = (await discovery()).tabs.find(t => !before.includes(t.id)); return !!added && await sheet(added.id).count() === 1; }, 'plus creates a real page and visible Sheet');
    await sheet(added.id).click();
    return added.id;
  };
  const select = async tab => { await sheet(tab).click(); await eventually(async () => await selected() === tab, 'Sheet selected'); };
  const pixelClick = async (x, y) => {
    await pane().locator('.native-page:not(.readonly)').waitFor({ timeout: 15000 });
    const image = pane().locator('.native-page img');
    const box = await image.boundingBox();
    const scale = Math.min(box.width / 1280, box.height / 800);
    await page.mouse.click(box.x + (box.width - 1280 * scale) / 2 + x * scale, box.y + (box.height - 800 * scale) / 2 + y * scale);
  };
  const close = async tab => { await sheet(tab).locator('.tab-close,.close,.x,[aria-label="关闭"]').first().click(); };
  await page.goto(`${origin}/manage/agents/${agent.id}/chat?c=${sid}`, { waitUntil: 'domcontentloaded' });
  await sheet(a).waitFor({ timeout: 30000 });
  await select(a);
  await pane().getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
  await navigate('A');
  const b = await add();
  check('plus adds an independent native page and Sheet', b !== a && (await discovery()).tabs.length === 2);
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
  check('same-window Sheet shares existing human control without retaking it', controls.length === 1);
  await navigate('B');
  await select(a); await waitPixels('A');
  check('viewing A does not change the Agent execution target B', (await discovery()).active_tab === b);
  const viewed = good(await api(target(a)), 'target A state');
  check('bound state separates viewed page A from active page B', viewed.viewed_tab === a && viewed.active_tab === b && viewed.url === siteURL('A'));
  await pixelClick(150, 155);
  await pane().locator('.native-page:not(.readonly)').waitFor({ timeout: 15000 });
  await pane().locator('.native-text textarea').fill('A-only-value');
  await pane().locator('.native-text button').click();
  await pixelClick(150, 260);
  await eventually(() => counts.A.saves === 1, 'actual click reaches A save button');
  check('text and click reach the viewed page only', counts.A.value === 'A-only-value' && counts.B.saves === 0);
  check('all human inputs explicitly bind tab, context and generation', inputBindings.length >= 5 && inputBindings.every(v => [a, b].includes(v.tab) && v.context === ref && Number.isSafeInteger(v.generation)));
  // Return A's real lease at the worker, but delay only its HTTP response. B
  // then takes a newer lease. A's late completion must not clear B's lease.
  delayReturnControl = true;
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).click();
  await eventually(() => heldControlResponse, 'delayed real control response is held');
  await select(b); await waitPixels('B');
  await pane().getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
  releaseControlResponse(); releaseControlResponse = undefined;
  await sleep(250);
  await pixelClick(150, 155);
  await pane().locator('.native-page:not(.readonly)').waitFor({ timeout: 15000 });
  await pane().locator('.native-text textarea').fill('B-newer-lease');
  await pane().locator('.native-text button').click();
  await pixelClick(150, 260);
  await eventually(() => counts.B.saves === 1, 'newer Sheet control remains usable after late return');
  check('late return from A cannot revoke B newer shared lease', counts.B.value === 'B-newer-lease' && counts.A.saves === 1);
  await select(a); await waitPixels('A');
  const controlCountBeforeAgent = controls.length;
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).click();
  await pane().getByRole('button', { name: '停止 Agent 并接管', exact: true }).waitFor();
  const agentState = await discovery();
  await select(b); await waitPixels('B');
  const c = await add();
  const afterAdd = await discovery();
  check('plus while Agent owns browser creates blank page without cancelling or selecting it', afterAdd.control === 'agent' && afterAdd.active_tab === agentState.active_tab && afterAdd.generation === agentState.generation && controls.length === controlCountBeforeAgent + 1);
  await close(c);
  await sleep(400);
  check('closing during Agent control does not silently take over or close page', (await discovery()).tabs.some(t => t.id === c) && controls.length === controlCountBeforeAgent + 1);
  const beforeReload = await page.locator('button[data-browser-tab-id]').evaluateAll(items => items.map(el => [el.getAttribute('data-sheet-id'), el.getAttribute('data-browser-tab-id'), el.getAttribute('data-browser-context-ref')]));
  await page.reload({ waitUntil: 'domcontentloaded' });
  await sheet(c).waitFor({ timeout: 30000 });
  const afterReload = await page.locator('button[data-browser-tab-id]').evaluateAll(items => items.map(el => [el.getAttribute('data-sheet-id'), el.getAttribute('data-browser-tab-id'), el.getAttribute('data-browser-context-ref')]));
  check('reload preserves all Sheet identities and page bindings', JSON.stringify(beforeReload) === JSON.stringify(afterReload));
  await select(a); await waitPixels('A');
  await select(b); await waitPixels('B');
  await page.screenshot({ path: `${output}/independent-sheets.png` });
  check('reload and viewing retain original page content without navigation replay', counts.A.pages === 1 && counts.B.pages === 1);
  const foreignRef = 'ctx_' + '0'.repeat(24);
  check('stale context read is rejected without recreating or rebinding', (await api(`${base}?tab_id=${a}&context_ref=${foreignRef}`)).status === 409 && (await discovery()).context_ref === ref);
  await pane().getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
  await pane().getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
  await close(b);
  await eventually(async () => !(await discovery()).tabs.some(t => t.id === b) && await sheet(b).count() === 0, 'closed Sheet removes its native page');
  check('closing B preserves A and its binding', (await discovery()).tabs.some(t => t.id === a) && (await api(target(a))).status === 200);
  check('closed page binding never falls back to another page', (await api(target(b))).status === 410);
  await select(a); await waitPixels('A');
  await close(a); await close(c);
  await eventually(async () => (await discovery()).tabs.length === 0, 'last page can close without phantom replacement');
  check('closing last Sheet leaves zero pages', !(await discovery()).active_tab && await page.locator('button[data-browser-tab-id]').count() === 0);
  const empty = await discovery();
  const op_id = `sheet-idempotence-${randomUUID()}`;
  const created = good(await api(`${base}/tabs`, 'POST', { action: 'new', context_ref: ref, op_id }), 'new page after empty');
  const replay = good(await api(`${base}/tabs`, 'POST', { action: 'new', context_ref: ref, op_id }), 'replayed new page');
  check('repeated creation operation adds exactly one page', created.tab_id === replay.tab_id && created.context_ref === ref && (await discovery()).tabs.length === 1 && empty.context_ref === ref);
  check('no browser exceptions or requests outside owned session', errors.length === 0 && denied.length === 0);
  report.passed = true;
} catch (error) {
  report.error = String(error.message).split('\n')[0].slice(0, 300);
  try { await context?.pages()[0]?.screenshot({ path: `${output}/failure.png` }); } catch {}
} finally {
  releaseControlResponse?.();
  await context?.close(); await browser?.close();
  if (sid) {
    try { good(await api(`/api/sessions/${sid}`, 'DELETE'), 'owned session cleanup'); assert.equal((await api(`/api/sessions/${sid}`)).status, 404); report.cleanup.session = 'deleted and verified'; }
    catch { report.cleanup.session = 'failed'; report.passed = false; }
  }
  if (agent) {
    try { good(await api(`/api/manage/agents/${agent.id}`, 'DELETE'), 'owned agent retirement'); report.cleanup.agent = 'retired'; }
    catch { report.cleanup.agent = 'failed'; report.passed = false; }
  }
  if (site) { site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); }
  report.counters = counts;
  await writeFile(`${output}/summary.json`, JSON.stringify(report, null, 2), { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output }));
process.exitCode = report.passed ? 0 : 1;
