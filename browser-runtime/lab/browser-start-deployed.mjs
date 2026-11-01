#!/usr/bin/env node
// Actual built UI and native browser; exclusively owned objects/storage/site.
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { chromium } from 'playwright';
import { httpTransport, loopbackOrigin } from './native-only-deployed.mjs';

const cfg = JSON.parse(await readFile(process.argv[2], 'utf8'));
const origin = loopbackOrigin(cfg.appURL), output = process.argv[3];
assert.ok(output && !output.includes('/private/'), 'fresh evidence directory required');
await mkdir(output, { recursive: false, mode: 0o700 });
const api = httpTransport(origin, { Authorization: `Bearer ${cfg.token}`, ...(cfg.releaseToken ? { 'X-Pogo-Release': cfg.releaseToken } : {}) });
const report = { scope: 'Real built UI, Go and Chromium; synthetic bookmark sites; isolated storage and newly owned sessions', passed: false, checks: [], cleanup: {} };
const check = (label, value) => { assert.ok(value, label); report.checks.push(label); };
const good = (reply, label) => { assert.ok(reply.status >= 200 && reply.status < 300, `${label}: HTTP ${reply.status}`); return reply.body; };
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(test, label, timeout = 15000) { const end = Date.now() + timeout; while (Date.now() < end) { if (await test()) return; await sleep(80); } throw new Error(label); }
const nonce = randomUUID(), prefix = `/start-${nonce}`, counts = { one: 0, two: 0, writes: 0 };
let site, browser, context, agent, sid, releaseControl;
try {
  site = http.createServer((req, res) => {
    const lane = new URL(req.url, 'http://fixture.invalid').pathname === `${prefix}/one` ? 'one' : new URL(req.url, 'http://fixture.invalid').pathname === `${prefix}/two` ? 'two' : null;
    if (req.method !== 'GET') { counts.writes++; res.writeHead(405).end(); return; }
    if (!lane) { res.writeHead(404).end(); return; }
    counts[lane]++; res.setHeader('Cache-Control', 'no-store'); res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><title>Bookmark ${lane}</title><body style="margin:0;background:${lane === 'one' ? '#d9f0e6' : '#f5decc'}"><h1 style="font:32px system-ui;padding:36px">Bookmark opened: ${lane}</h1></body>`);
  });
  await new Promise(resolve => site.listen(0, '0.0.0.0', resolve));
  const url = lane => `http://host.docker.internal:${site.address().port}${prefix}/${lane}`;
  agent = good(await api('/api/manage/agents', 'POST', { name: `Start acceptance ${nonce.slice(0, 8)}`, description: 'Temporary browser new-tab acceptance', instructions: 'Controlled acceptance only', model: cfg.model, models: [cfg.model], skills: [], mcp_servers: [], status: 'ready' }), 'owned agent');
  sid = good(await api(`/api/apps/${agent.id}/sessions`, 'POST', {}), 'owned session').id;
  const base = `/api/sessions/${sid}/browser`, state = async () => good(await api(base), 'discovery');
  const initial = await state(), ref = initial.context_ref, a = initial.active_tab;
  browser = await chromium.launch({ headless: true });
  context = await browser.newContext({ viewport: { width: 1540, height: 1000 }, locale: 'zh-CN', ...(cfg.releaseToken ? { extraHTTPHeaders: { 'X-Pogo-Release': cfg.releaseToken } } : {}) });
  const page = await context.newPage(), errors = [], denied = [], requests = [];
  let holdNextTakeover = false, held = false;
  const bookmarks = [{ id: 'fixture-one', title: 'Fixture one', url: url('one') }, { id: 'fixture-two', title: 'Fixture two', url: url('two') }];
  await context.addInitScript(({ app, token, key, bookmarks }) => {
    if (location.origin !== app) return;
    sessionStorage.setItem('pogo-token', token);
    if (!localStorage.getItem('start-controlled-seeded')) {
      localStorage.setItem(`pogo:studio:${key}:active-sheet`, 'hub');
      localStorage.setItem('pogo:browser-bookmarks:v1', JSON.stringify(bookmarks));
      localStorage.setItem('start-controlled-seeded', '1');
    }
  }, { app: origin, token: cfg.token, key: agent.id, bookmarks });
  page.on('pageerror', () => errors.push('client exception'));
  await page.route('**/*', async route => {
    const request = route.request(), u = new URL(request.url()), session = /^\/api\/sessions\/([^/]+)/.exec(u.pathname);
    if (u.origin !== origin || (session && !['activity', 'search', 'sample'].includes(session[1]) && session[1] !== sid) || u.pathname.endsWith('/messages')) { denied.push('outside owned surface'); await route.abort(); return; }
    if (u.pathname.startsWith(base)) requests.push({ path: u.pathname, method: request.method(), ...(request.method() === 'POST' ? { body: request.postDataJSON() } : {}) });
    const headers = { ...request.headers(), ...(cfg.releaseToken ? { 'X-Pogo-Release': cfg.releaseToken } : {}) };
    if (holdNextTakeover && u.pathname === `${base}/control` && request.postDataJSON().mode === 'human') {
      holdNextTakeover = false; const response = await route.fetch({ headers });
      await new Promise(resolve => { releaseControl = resolve; held = true; });
      await route.fulfill({ response }); return;
    }
    await route.continue({ headers });
  });
  const pane = () => page.locator('.native-browser:visible');
  const start = () => pane().getByTestId('browser-new-tab');
  const sheet = tab => page.locator(`button[data-browser-tab-id="${tab}"]`);
  const controls = () => requests.filter(r => r.path === `${base}/control`);
  const inputs = () => requests.filter(r => r.path === `${base}/input`);
  const add = async () => {
    const before = (await state()).tabs.map(t => t.id);
    await page.locator('.tab.add:visible').click(); await page.getByRole('menuitem', { name: '浏览器', exact: true }).click();
    let created; await until(async () => { created = (await state()).tabs.find(t => !before.includes(t.id)); return created && await sheet(created.id).count() === 1; }, 'new Sheet appears');
    await sheet(created.id).click(); await start().waitFor(); return created.id;
  };
  const waitSite = async (tab, lane) => {
    await until(async () => { const s = good(await api(`${base}?${new URLSearchParams({ context_ref: ref, tab_id: tab })}`), 'bound state'); return s.url === url(lane) && !s.page_loading; }, 'bookmark opens in target page');
    await pane().locator('.native-page img').waitFor();
    await page.waitForFunction(lane => {
      const image = [...document.querySelectorAll('.native-page img')].find(el => el.getBoundingClientRect().width > 0);
      if (!image?.complete || !image.naturalWidth) return false;
      const canvas = document.createElement('canvas'); canvas.width = image.naturalWidth; canvas.height = image.naturalHeight;
      const draw = canvas.getContext('2d'); draw.drawImage(image, 0, 0);
      const pixel = draw.getImageData(canvas.width - 30, canvas.height - 30, 1, 1).data;
      return lane === 'one' ? pixel[0] > 205 && pixel[0] < 230 && pixel[1] > 235 && pixel[2] > 220 : pixel[0] > 235 && pixel[1] > 210 && pixel[1] < 230 && pixel[2] > 190 && pixel[2] < 215;
    }, lane);
  };
  await page.goto(`${origin}/manage/agents/${agent.id}/chat?c=${sid}`, { waitUntil: 'domcontentloaded' });
  await sheet(a).waitFor(); await start().waitFor();
  check('initial blank Page presents a local new-tab page without a white browser image', await pane().locator('.native-page img').count() === 0 && await start().isVisible());
  check('bookmarks are visible only on the new-tab page', await pane().getByTestId('browser-bookmark-bar').count() === 0 && await start().locator('[data-bookmark-id="fixture-one"]').count() === 1 && await start().locator('[data-bookmark-id="fixture-two"]').count() === 1);
  await sleep(2200);
  check('blank new-tab polling never captures blank JPEGs or visits bookmarks', requests.filter(r => r.path === `${base}/frame`).length === 0 && counts.one === 0 && counts.two === 0);
  const b = await add();
  check('new Sheet uses the bookmark start page while preserving Agent target', (await state()).active_tab === a && await start().isVisible());
  await page.screenshot({ path: `${output}/new-tab-bookmarks.png` });
  await start().locator('[data-bookmark-id="fixture-one"] .card-link').click();
  await waitSite(b, 'one');
  check('one bookmark click takes over and opens once in the selected Sheet', controls().length === 1 && controls()[0].body.tab_id === b && counts.one === 1 && (await state()).active_tab === a && inputs().length === 1 && inputs().at(-1).body.target_tab === b && inputs().at(-1).body.context_ref === ref);
  check('visited site has no favorite confirmation or bookmark bar', await start().count() === 0 && await pane().getByTestId('browser-bookmark-pending').count() === 0 && await pane().getByTestId('browser-bookmark-bar').count() === 0);
  const c = await add();
  await pane().getByRole('button', { name: '接管此页', exact: true }).click();
  await pane().getByRole('button', { name: '交还此页', exact: true }).waitFor();
  await start().locator('[data-bookmark-id="fixture-two"] .card-link').click(); await waitSite(c, 'two');
  check('already-owned bookmark click navigates without retaking control', controls().length === 2 && counts.two === 1 && inputs().at(-1).body.target_tab === c);
  const d = await add();
  await start().locator('[data-bookmark-id="fixture-one"] .card-link').click(); await waitSite(d, 'one');
  check('new Sheets share bookmarks but acquire their own Page control', counts.one === 2 && controls().length === 3 && controls().at(-1).body.tab_id === d && inputs().at(-1).body.target_tab === d && (await state()).tabs.find(tab => tab.id === a).control === 'agent');
  await pane().getByRole('button', { name: '交还此页', exact: true }).click();
  await pane().getByRole('button', { name: '接管此页', exact: true }).waitFor();
  const e = await add();
  holdNextTakeover = true;
  await start().locator('[data-bookmark-id="fixture-two"] .card-link').click();
  await until(() => held, 'real takeover response held');
  await sheet(d).click(); await waitSite(d, 'one');
  releaseControl(); releaseControl = undefined;
  await sleep(500);
  check('switching Sheet while takeover is pending cancels automatic bookmark navigation', counts.one === 2 && counts.two === 1 && good(await api(`${base}?tab_id=${e}&context_ref=${ref}`), 'blank target retained').url === 'about:blank');
  await sheet(e).click(); await start().waitFor();
  await page.reload({ waitUntil: 'domcontentloaded' }); await start().waitFor();
  check('reload restores bookmark start page without navigating saved favorites', counts.one === 2 && counts.two === 1 && await start().locator('[data-bookmark-id="fixture-one"]').count() === 1 && await start().locator('[data-bookmark-id="fixture-two"]').count() === 1);
  await page.evaluate(() => { localStorage.setItem('pogo:browser-bookmarks:v1', '[]'); window.dispatchEvent(new Event('pogo:browser-bookmarks-changed')); });
  await until(async () => await start().getByTestId('browser-bookmark-card').count() === 0, 'empty bookmark list reflected');
  check('no-bookmark start page retains add and transfer actions without a management menu', await start().getByRole('button', { name: '添加收藏', exact: true }).count() === 1 && await start().locator('.favorite-transfer button').count() === 2 && await start().getByRole('button', { name: '管理收藏', exact: true }).count() === 0);
  await page.screenshot({ path: `${output}/new-tab-empty.png` });
  await start().getByRole('button', { name: '添加收藏', exact: true }).click();
  const form = page.locator('.bookmark-form:visible'); await form.waitFor();
  await form.locator('input').nth(0).fill('Added favorite');
  await form.locator('input').nth(1).fill(url('one'));
  await form.locator('button[type="submit"]').click();
  await start().getByTestId('browser-bookmark-card').filter({ hasText: 'Added favorite' }).waitFor();
  check('adding a favorite updates the home card without opening the site', await start().getByTestId('browser-bookmark-card').filter({ hasText: 'Added favorite' }).count() === 1 && counts.one === 2 && counts.two === 1);
  check('test sends no website writes, browser exceptions or outside-session requests', counts.writes === 0 && errors.length === 0 && denied.length === 0);
  report.passed = true;
} catch (error) {
  report.error = String(error.message).split('\n')[0].slice(0, 300);
  try { await context?.pages()[0]?.screenshot({ path: `${output}/failure.png` }); } catch {}
} finally {
  releaseControl?.(); await context?.close(); await browser?.close();
  if (sid) { try { good(await api(`/api/sessions/${sid}`, 'DELETE'), 'session cleanup'); assert.equal((await api(`/api/sessions/${sid}`)).status, 404); report.cleanup.session = 'deleted and verified'; } catch { report.cleanup.session = 'failed'; report.passed = false; } }
  if (agent) { try { good(await api(`/api/manage/agents/${agent.id}`, 'DELETE'), 'agent retirement'); report.cleanup.agent = 'retired'; } catch { report.cleanup.agent = 'failed'; report.passed = false; } }
  if (site) { site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); }
  report.counters = counts;
  await writeFile(`${output}/summary.json`, JSON.stringify(report, null, 2), { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output }));
process.exitCode = report.passed ? 0 : 1;
