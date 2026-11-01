import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

const base = process.argv[2] || 'http://127.0.0.1:5189';
const out = process.argv[3] || '/tmp/pogo-browser-start';
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true }), checks = [];
const check = (name, passed) => { assert.ok(passed, name); checks.push(name); console.log(`PASS ${name}`); };
const favorite = { id: 'chosen', title: 'Research portal', url: 'https://favorite.invalid/portal' };

async function fixture(theme = 'light') {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await context.newPage(), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route(url => url.pathname.startsWith('/api/') || url.origin !== new URL(base).origin, route => route.abort());
  await page.goto(`${base}/dev/browser-sheets.html?theme=${theme}`);
  await page.locator('.native-page img:visible').waitFor();
  const pane = () => page.locator('.native-browser:visible');
  const home = () => page.getByTestId('browser-new-tab').filter({ visible: true });
  const card = () => home().locator(`[data-bookmark-id="${favorite.id}"] .card-link`);
  const add = async () => {
    await page.getByRole('button', { name: '添加工作表', exact: true }).click();
    await page.getByRole('menuitem', { name: '浏览器', exact: true }).click();
    await home().waitFor();
  };
  const seed = async cards => page.evaluate(cards => {
    localStorage.setItem('pogo:browser-bookmarks:v1', JSON.stringify(cards));
    window.dispatchEvent(new Event('pogo:browser-bookmarks-changed'));
  }, cards);
  // Keep the real fixture semantics; only hold or fail the response to the
  // clicked Page's control request. No request reaches a deployed service.
  await page.evaluate(() => {
    const fetch = window.fetch.bind(window);
    window.__bookmarkQA = { hold: false, fail: false, attempts: 0, held: false, release: null, staleDuringTakeover: false, staleState: null, stalePushes: 0 };
    const Socket = window.WebSocket;
    window.WebSocket = new Proxy(Socket, { construct(Target, args) {
      const socket = Reflect.construct(Target, args), deliver = socket.deliver.bind(socket);
      socket.deliver = message => {
        const qa = window.__bookmarkQA;
        // Model a live observation arriving during the control POST before
        // its completed, fresh bound GET. The fixture's server state remains
        // human-owned; only this viewer sees the delayed old Agent state.
        if (qa.staleDuringTakeover && qa.staleState && message.type === 'state' && socket.tab === qa.staleState.viewed_tab) {
          qa.stalePushes++; deliver({ ...message, state: qa.staleState });
        } else deliver(message);
      };
      return socket;
    } });
    window.fetch = async (input, options = {}) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href);
      const body = options.body ? JSON.parse(String(options.body)) : {};
      if (url.pathname.endsWith('/browser/control') && body.mode === 'human') {
        const qa = window.__bookmarkQA; qa.attempts++;
        if (qa.fail) return new Response(JSON.stringify({ error: 'Fixture takeover failed' }), { status: 503, headers: { 'Content-Type': 'application/json' } });
        if (qa.staleDuringTakeover) qa.staleState = await (await fetch(`/api/sessions/sheets-a/browser?${new URLSearchParams({ context_ref: body.context_ref, tab_id: body.tab_id })}`)).json();
        const response = await fetch(input, options);
        if (qa.hold) await new Promise(resolve => { qa.release = resolve; qa.held = true; });
        return response;
      }
      return fetch(input, options);
    };
  });
  const settle = async () => page.waitForFunction(() => !document.querySelector('.native-browser button[aria-label="正在接管…"]'));
  return { context, page, errors, pane, home, card, add, seed, settle };
}

try {
  {
    const f = await fixture();
    try {
      await f.seed([favorite]); await f.add();
      await f.page.waitForFunction(() => window.__sheetsFixture.liveReady.some(r => r.tab === 't2'));
      await f.page.evaluate(() => { window.__bookmarkQA.staleDuringTakeover = true; });
      await f.card().click(); await f.settle();
      await fs.writeFile(`${out}/handoff-observation.json`, JSON.stringify(await f.page.evaluate(() => ({ control_attempts: window.__bookmarkQA.attempts, old_live_observations: window.__bookmarkQA.stalePushes, control_requests: window.__sheetsFixture.controls, navigation_requests: window.__sheetsFixture.inputs, server: window.__sheetsFixture.context() })), null, 2));
      check('delayed old Agent live observation occurs during the one takeover', await f.page.evaluate(() => window.__bookmarkQA.stalePushes > 0 && window.__bookmarkQA.attempts === 1));
      check('fresh bound handoff GET opens the clicked bookmark once despite an older live observation', await f.page.evaluate(url => window.__sheetsFixture.inputs.length === 1 && window.__sheetsFixture.inputs[0].url === url && window.__sheetsFixture.inputs[0].target_tab === 't2', favorite.url));
      check('stale takeover observations do not replay browser operations or send prompts', f.errors.length === 0 && await f.page.evaluate(() => window.__bookmarkQA.attempts === 1 && window.__sheetsFixture.controls.length === 1 && window.__sheetsFixture.inputs.length === 1 && window.__sheetsFixture.denied.length === 0));
    } finally { await f.context.close(); }
  }
  for (const [width, theme] of [[1440, 'light'], [1440, 'dark'], [390, 'light'], [390, 'dark']]) {
    const f = await fixture(theme), { page, pane, home } = f;
    try {
      await f.add();
      if (width === 390) {
        await page.locator('.immersion-toggle').click(); await page.locator('.shell.browser-immersive').waitFor();
        await page.setViewportSize({ width, height: 1000 }); await page.locator('.immersive-chat-head > button').first().click();
      }
      const label = `${theme}/${width}`;
      check(`${label}: blank Page has local empty favorite view and localized Sheet title`, await page.locator('[data-browser-tab-id=t2][aria-selected=true]').innerText() === '新标签页' && await pane().locator('.native-page img').count() === 0 && await home().getByRole('button', { name: '添加收藏', exact: true }).count() === 1);
      await page.waitForFunction(() => window.__sheetsFixture.liveReady.some(r => r.tab === 't2'));
      check(`${label}: blank live connection never captures a blank JPEG`, await page.evaluate(() => window.__sheetsFixture.liveFrames.every(r => r.tab !== 't2') && window.__sheetsFixture.requests.every(r => !r.path.endsWith('/frame'))));
      await page.screenshot({ path: `${out}/empty-${theme}-${width}.png` });
      await home().getByRole('button', { name: '添加收藏', exact: true }).click();
      const form = page.locator('.bookmark-form:visible');
      await form.getByRole('textbox', { name: '书签名称', exact: true }).fill(favorite.title);
      await form.getByRole('textbox', { name: '原站地址', exact: true }).fill(favorite.url);
      await form.getByRole('button', { name: '保存', exact: true }).click();
      await home().getByTestId('browser-bookmark-card').waitFor();
      check(`${label}: adding a favorite updates the home card without opening it`, await page.evaluate(() => JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1')).length === 1 && window.__sheetsFixture.controls.length === 0 && window.__sheetsFixture.inputs.length === 0));
      await page.screenshot({ path: `${out}/favorites-${theme}-${width}.png` });
      await home().locator('.card-link').click(); await pane().locator('.native-page img').waitFor();
      check(`${label}: one favorite click takes over and navigates its Page exactly once`, await page.evaluate(url => {
        const f = window.__sheetsFixture, c = f.context();
        return c.generation === 1 && c.active === 't1' && c.pages.find(p => p.id === 't1').control === 'agent' && c.pages.find(p => p.id === 't2').control === 'human' && f.controls.length === 1 && f.controls[0].tab_id === 't2' && f.inputs.length === 1 && f.inputs[0].target_tab === 't2' && f.inputs[0].url === url;
      }, favorite.url));
      check(`${label}: no confirmation, extra menu or favorites toolbar remains`, await home().count() === 0 && await pane().getByTestId('browser-bookmark-pending').count() === 0 && await pane().getByTestId('browser-bookmark-bar').count() === 0 && await pane().locator('.native-menu').count() === 0);
      // Return this already-owned Page to a blank document without changing
      // its lease, then exercise a second click through the same home UI.
      await page.evaluate(() => window.__sheetsFixture.navigate('t2', 'about:blank'));
      await pane().getByRole('button', { name: '刷新画面', exact: true }).click(); await home().waitFor();
      await home().locator('.card-link').click(); await pane().locator('.native-page img').waitFor();
      check(`${label}: already-owned favorite click navigates without another takeover`, await page.evaluate(() => window.__sheetsFixture.controls.length === 1 && window.__sheetsFixture.inputs.length === 2));
      check(`${label}: no errors, escaped API calls, or horizontal overflow`, f.errors.length === 0 && await page.evaluate(() => window.__sheetsFixture.denied.length === 0) && await pane().evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    } finally { await f.context.close(); }
  }

  for (const bad of [{ ...favorite, url: 'javascript:alert(1)' }, { ...favorite, needsRepair: true }]) {
    const f = await fixture();
    try {
      await f.seed([bad]); await f.add(); await f.card().click(); await f.page.locator('.bookmark-form:visible').waitFor();
      check(`favorite ${bad.needsRepair ? 'marked for repair' : 'with invalid URL'} opens only its editor`, await f.page.evaluate(() => window.__bookmarkQA.attempts === 0 && window.__sheetsFixture.inputs.length === 0));
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture();
    try {
      await f.seed([favorite]); await f.add(); await f.page.evaluate(() => { window.__bookmarkQA.fail = true; });
      await f.card().click(); await f.pane().locator('.native-error').waitFor(); await f.settle();
      check('failed takeover never navigates and allows an explicit new click', await f.page.evaluate(() => window.__bookmarkQA.attempts === 1 && window.__sheetsFixture.inputs.length === 0));
      await f.page.evaluate(() => { window.__bookmarkQA.fail = false; });
      await f.card().click(); await f.pane().locator('.native-page img').waitFor();
      check('retry after failed takeover opens once without replaying the failed click', await f.page.evaluate(() => window.__bookmarkQA.attempts === 2 && window.__sheetsFixture.inputs.length === 1));
    } finally { await f.context.close(); }
  }

  for (const during of ['double click', 'hidden Sheet', 'closed Page', 'changed document']) {
    const f = await fixture();
    try {
      await f.seed([favorite]); await f.add(); await f.page.evaluate(() => { window.__bookmarkQA.hold = true; });
      if (during === 'double click') await f.card().evaluate(button => { button.click(); button.click(); });
      else await f.card().click();
      await f.page.waitForFunction(() => window.__bookmarkQA.held);
      if (during === 'hidden Sheet') await f.page.locator('[data-browser-tab-id=t1]').click();
      if (during === 'closed Page') await f.page.evaluate(async () => {
        const context = window.__sheetsFixture.context();
        const response = await fetch('/api/sessions/sheets-a/browser/tabs', { method: 'POST', body: JSON.stringify({ action: 'close', tab_id: 't2', context_ref: context.ref, op_id: 'qa-close-pending-favorite', lease_token: 'lease-sheets-a-t2' }) });
        if (!response.ok) throw new Error('fixture Page close failed');
      });
      if (during === 'changed document') await f.page.evaluate(() => window.__sheetsFixture.navigate('t2', 'https://agent-opened.invalid/'));
      await f.page.evaluate(() => window.__bookmarkQA.release()); await f.settle();
      if (during === 'double click') {
        await f.pane().locator('.native-page img').waitFor();
        check('double click while takeover is delayed dispatches one takeover and one navigation', await f.page.evaluate(() => window.__bookmarkQA.attempts === 1 && window.__sheetsFixture.inputs.length === 1));
      } else {
        check(`late takeover after ${during} never navigates`, await f.page.evaluate(() => window.__bookmarkQA.attempts === 1 && window.__sheetsFixture.inputs.length === 0));
        if (during === 'hidden Sheet') {
          await f.page.locator('[data-browser-tab-id=t2]').click(); await f.home().waitFor();
          check('returning to a hidden Sheet does not resume its cancelled bookmark click', await f.page.evaluate(() => window.__sheetsFixture.inputs.length === 0) && await f.pane().getByTestId('browser-bookmark-pending').count() === 0);
        }
        if (during === 'changed document') check('newer document survives a delayed bookmark takeover', await f.page.evaluate(() => window.__sheetsFixture.context().pages.find(p => p.id === 't2').url === 'https://agent-opened.invalid/'));
      }
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture();
    try {
      await f.add(); await f.page.evaluate(() => { window.__sheetsFixture.setLoading('t2', true); });
      await f.pane().getByRole('button', { name: '刷新画面', exact: true }).click(); await f.pane().locator('.connection-dot.loading').waitFor();
      check('loading blank state hides the start page rather than hiding the load', await f.home().count() === 0);
      await f.page.evaluate(() => { window.__sheetsFixture.setLoading('t2', false); window.__sheetsFixture.offline = true; });
      await f.pane().getByRole('button', { name: '刷新画面', exact: true }).click(); await f.pane().locator('.native-error').waitFor();
      check('connection failure cannot fall back to the new-tab page', await f.home().count() === 0);
    } finally { await f.context.close(); }
  }
  await fs.writeFile(`${out}/summary.json`, JSON.stringify({ scope: 'isolated in-memory UI fixture only', passed: true, checks }, null, 2));
} catch (error) {
  await fs.writeFile(`${out}/summary.json`, JSON.stringify({ scope: 'isolated in-memory UI fixture only', passed: false, checks, error: error.message }, null, 2));
  throw error;
} finally { await browser.close(); }
