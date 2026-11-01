import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

process.umask(0o077);
const base = process.argv[2] || 'http://127.0.0.1:19793';
const output = path.resolve(process.argv[3] || '/Users/wangy/Documents/DiscoverAgent/.localagent/checks/composer-suggestions-20261009/ui-1');
assert.match(base, /^http:\/\/127\.0\.0\.1:19793$/);
await fs.mkdir(path.dirname(output), { recursive: true, mode: 0o700 });
await fs.mkdir(output, { mode: 0o700 });
const report = { scope: 'Real AgentView, fictional Agent/session/capabilities/skills and in-memory transport only; synthetic IME events, no real model, deployed API or browser', passed: false, checks: [], screenshots: [], anchors: [] };
const check = (name, value) => { report.checks.push({ name, passed: !!value }); assert.ok(value, name); console.log(`PASS ${name}`); };
const browser = await chromium.launch({ headless: true });
let currentPage;

async function fixture(theme, width = 1440, immersive = false) {
  const context = await browser.newContext({ viewport: { width, height: 1000 }, locale: 'zh-CN', colorScheme: theme === 'dark' ? 'dark' : 'light' });
  const page = await context.newPage(), errors = [], escaped = [];
  currentPage = page;
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin !== base || url.pathname.startsWith('/api/')) { escaped.push(url.pathname); return route.abort(); }
    return route.continue();
  });
  await page.goto(`${base}/dev/composer-suggestions.html?theme=${theme}${immersive ? '&browser=1' : ''}`);
  const input = () => page.locator('.compose-col textarea').filter({ visible: true });
  const menu = () => page.locator('.slash-command-menu:visible');
  const rows = () => menu().getByRole('option');
  const row = id => rows().filter({ has: page.locator(`[id$="-${id}-label"]`) });
  const selected = () => rows().filter({ hasNot: page.locator('[aria-selected="false"]') }).locator('xpath=self::*[@aria-selected="true"]');
  const activeID = async () => menu().locator('[role=option][aria-selected=true]').getAttribute('id');
  const fill = async value => { await input().fill(''); await input().fill(value); };
  const counters = () => page.evaluate(() => ({ messages: window.__composerFixture.messages.length, controls: window.__composerFixture.controls.length, compact: window.__composerFixture.compactCalls }));
  await page.waitForFunction(() => window.__composerFixture?.streamsOpened === 1 && window.__composerFixture.requests.some(request => request.path === '/api/skills' && request.query.includes('session_id=composer-fixture-session')));
  if (immersive) {
    const pane = page.locator('.native-browser:visible');
    const toggle = page.locator('.immersion-toggle');
    if (!await pane.isVisible() && !await toggle.isVisible()) await page.getByRole('button', { name: /^面板(?: ·.*)?$/ }).click();
    await pane.waitFor();
    if (await page.locator('.shell.browser-immersive').count() === 0) { await toggle.waitFor(); await toggle.click(); }
    await page.locator('.shell.browser-immersive').waitFor();
    const restore = page.locator('.immersive-chat-restore');
    if (await restore.isVisible()) await restore.click();
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-pogo-immersive-motion') && !document.querySelector('.shell[data-immersive-motion]'));
  }
  await input().waitFor();
  return { context, page, errors, escaped, input, menu, rows, row, selected, activeID, fill, counters };
}
async function waitSelected(f, suffix) {
  await f.page.waitForFunction(suffix => [...document.querySelectorAll('.slash-command-menu [role=option]')].some(row => row.getAttribute('aria-selected') === 'true' && row.id.endsWith('-' + suffix)), suffix);
}
async function verifyARIA(f, label) {
  const id = await f.activeID();
  check(`${label}: selected row is the textarea active descendant`, !!id && await f.input().getAttribute('aria-activedescendant') === id && await f.rows().filter({ has: f.page.locator('kbd') }).count() === 1);
}
async function screenshot(f, name) {
  await f.page.screenshot({ path: `${output}/${name}.png` }); report.screenshots.push(`${name}.png`);
}
async function verifyAnchor(f, label) {
  const measure = () => f.page.evaluate(() => {
    const menu = document.querySelector('.slash-command-menu'), card = [...document.querySelectorAll('.input-card')].find(card => card.getBoundingClientRect().width > 0);
    if (!menu || !card) return null;
    const m = menu.getBoundingClientRect(), c = card.getBoundingClientRect();
    return { menu: { x: m.x, y: m.y, width: m.width, height: m.height, bottom: m.bottom }, card: { x: c.x, y: c.y, width: c.width, height: c.height }, gap: c.top - m.bottom, viewport: { width: innerWidth, height: innerHeight }, motion: document.documentElement.hasAttribute('data-pogo-immersive-motion') || !!document.querySelector('.shell[data-immersive-motion]') };
  });
  try {
    await f.page.waitForFunction(() => {
      const menu = document.querySelector('.slash-command-menu'), card = [...document.querySelectorAll('.input-card')].find(card => card.getBoundingClientRect().width > 0);
      if (!menu || !card || document.documentElement.hasAttribute('data-pogo-immersive-motion') || document.querySelector('.shell[data-immersive-motion]')) return false;
      const m = menu.getBoundingClientRect(), c = card.getBoundingClientRect();
      return Math.abs(c.top - m.bottom - 8) <= 1 && Math.abs(m.left - c.left) <= 1 && Math.abs(m.width - c.width) <= 1 && m.left >= 0 && m.top >= 0 && m.right <= innerWidth + 1 && m.bottom <= innerHeight + 1;
    });
  } catch (error) {
    report.anchors.push({ label, ...(await measure()) });
    await f.page.screenshot({ path: `${output}/anchor-failure.png` });
    throw error;
  }
  report.anchors.push({ label, ...(await measure()) });
  check(`${label}: steady popup matches the real composer card width and left edge with an 8px gap`, true);
}

try {
  for (const [theme, width] of [['light', 1440], ['dark', 1440], ['light', 390], ['dark', 390]]) {
    const f = await fixture(theme, width), label = `${theme}/${width}`;
    try {
      await f.fill('/'); await f.menu().waitFor();
      check(`${label}: slash shows compact, skill entry and all 12 scoped skills`, await f.rows().count() === 14 && await f.row('command-compact').count() === 1 && await f.row('skill-entry').count() === 1 && await f.rows().filter({ has: f.page.locator('.suggestion-origin') }).count() === 12);
      check(`${label}: builtin and external provenance are both present`, await f.menu().getByText('内置', { exact: true }).count() === 4 && await f.menu().getByText('扩展', { exact: true }).count() === 8);
      await waitSelected(f, 'command-compact'); await verifyARIA(f, label);
      await verifyAnchor(f, label);
      await screenshot(f, `menu-${theme}-${width}`);
      const bounds = await f.menu().boundingBox();
      check(`${label}: popup fits the visible viewport without horizontal overflow`, bounds.x >= -1 && bounds.x + bounds.width <= width + 1 && await f.menu().evaluate(el => el.scrollWidth <= el.clientWidth + 1));

      await f.fill('/com'); await f.menu().waitFor();
      check(`${label}: compact prefix filters to compact only`, await f.rows().count() === 1 && await f.row('command-compact').count() === 1);
      await f.input().press('Tab'); await f.menu().waitFor({ state: 'detached' });
      check(`${label}: Tab completes compact with a trailing space and retains focus`, await f.input().inputValue() === '/compact ' && await f.input().evaluate(el => el === document.activeElement));
      check(`${label}: completion starts neither messages nor compact`, JSON.stringify(await f.counters()) === JSON.stringify({ messages: 0, controls: 0, compact: 0 }));

      await f.fill('/skill:d'); await f.menu().waitFor();
      check(`${label}: legacy skill prefix filters correctly`, (await f.rows().locator('.suggestion-label').allTextContents()).join(',') === 'data-table,diagram-kit,docs');
      await f.input().press('ArrowDown'); await waitSelected(f, 'skill-diagram-kit');
      await f.input().press('Enter'); await f.menu().waitFor({ state: 'detached' });
      check(`${label}: Enter completes the highlighted skill with a trailing space`, await f.input().inputValue() === '/skill:diagram-kit ' && await f.input().evaluate(el => el === document.activeElement));
      check(`${label}: highlighted skill completion does not send`, (await f.counters()).messages === 0);

      await f.fill('/skill:BROWSER'); await f.menu().waitFor();
      check(`${label}: skill syntax matching is case insensitive`, await f.rows().count() === 1 && await f.row('skill-browser-use').count() === 1);
      await f.input().press('Tab'); await f.menu().waitFor({ state: 'detached' });
      check(`${label}: Tab and Enter use the same concrete skill completion`, await f.input().inputValue() === '/skill:browser-use ' && (await f.counters()).messages === 0);

      await f.fill('/'); await f.menu().waitFor(); await f.input().press('ArrowDown'); await waitSelected(f, 'skill-entry');
      await f.input().press('Enter');
      await f.page.waitForFunction(() => document.querySelector('.compose-col textarea').value === '/skill:' && document.querySelectorAll('.slash-command-menu [role=option]').length === 12);
      check(`${label}: skill entry opens the 12 actual skill choices without sending`, await f.input().inputValue() === '/skill:' && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      await f.input().press('Escape'); await f.menu().waitFor({ state: 'detached' });
      check(`${label}: Escape closes the popup and preserves the draft`, await f.input().inputValue() === '/skill:');
      await f.input().press('End'); await f.input().press('r'); await f.menu().waitFor();
      check(`${label}: changing the draft reopens matching suggestions`, await f.rows().count() === 1 && await f.row('skill-research').count() === 1);

      await f.fill('/com'); await f.menu().waitFor(); await f.input().press('Shift+Enter');
      await f.menu().waitFor({ state: 'detached' });
      check(`${label}: Shift Enter inserts a normal newline without completing or sending`, await f.input().inputValue() === '/com\n' && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      await f.fill('普通文本保持正常');
      check(`${label}: ordinary prose has no slash popup`, await f.menu().count() === 0);
      check(`${label}: skill catalog stays Agent/session scoped and no network escapes`, f.errors.length === 0 && f.escaped.length === 0 && await f.page.evaluate(() => window.__composerFixture.denied.length === 0 && window.__composerFixture.requests.every(request => request.path !== '/api/sessions' && !(request.path === '/api/skills' && !request.query.includes('session_id=')))));
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture('light'), label = 'keyboard/mouse/IME';
    try {
      await f.fill('/'); await f.menu().waitFor(); await f.input().press('ArrowUp'); await waitSelected(f, 'skill-web-drawing');
      await f.page.waitForFunction(() => {
        const menu = document.querySelector('.slash-command-menu .menu-scroll'), row = menu?.querySelector('[aria-selected=true]');
        if (!menu || !row) return false;
        const m = menu.getBoundingClientRect(), r = row.getBoundingClientRect();
        return menu.scrollTop > 0 && r.top >= m.top - 1 && r.bottom <= m.bottom + 1;
      });
      check(`${label}: Arrow Up wraps from first to last and scrolls the selected row into view`, true);
      await f.input().press('ArrowDown'); await waitSelected(f, 'command-compact');
      check(`${label}: Arrow Down wraps from last to first`, (await f.activeID()).endsWith('-command-compact'));
      await f.input().press('ArrowDown'); await f.input().press('ArrowDown'); await waitSelected(f, 'skill-analysis-lab');
      await f.fill('/skill:d'); await f.menu().waitFor(); await waitSelected(f, 'skill-data-table');
      check(`${label}: input change resets highlight to the first new match`, (await f.activeID()).endsWith('-skill-data-table'));
      const beforeModified = await f.activeID();
      await f.input().evaluate(el => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', ctrlKey: true, bubbles: true, cancelable: true })));
      check(`${label}: a modified arrow does not move menu highlight`, await f.activeID() === beforeModified);
      await f.fill('/'); await f.menu().waitFor(); await f.row('skill-code-review').hover(); await waitSelected(f, 'skill-code-review');
      check(`${label}: mouse hover changes the keyboard selection without executing`, (await f.activeID()).endsWith('-skill-code-review') && (await f.counters()).messages === 0);
      await f.row('skill-code-review').click(); await f.menu().waitFor({ state: 'detached' });
      check(`${label}: mouse click only completes and leaves textarea focused`, await f.input().inputValue() === '/skill:code-review ' && await f.input().evaluate(el => el === document.activeElement) && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);

      await f.fill('/com'); await f.menu().waitFor();
      for (const options of [{ isComposing: true }, { keyCode: 229 }]) {
        await f.input().evaluate((el, options) => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...options })), options);
        check(`${label}: Enter with ${options.isComposing ? 'isComposing' : 'keyCode 229'} leaves draft and requests unchanged`, await f.input().inputValue() === '/com' && await f.menu().count() === 1 && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      }
      await f.input().evaluate(el => { el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); });
      check(`${label}: active composition blocks Enter even without the key event flag`, await f.input().inputValue() === '/com' && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      await f.input().evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));
      await f.input().focus(); await f.page.keyboard.down('Enter');
      await f.menu().waitFor({ state: 'detached' });
      await f.input().evaluate(el => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', repeat: true, bubbles: true, cancelable: true })));
      await f.page.keyboard.up('Enter');
      check(`${label}: held Enter after completion neither sends nor opens compact confirmation`, await f.input().inputValue() === '/compact ' && await f.page.locator('.el-message-box:visible').count() === 0 && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      await f.input().press('Enter'); const dialog = f.page.locator('.el-message-box:visible'); await dialog.waitFor();
      check(`${label}: a second deliberate Enter invokes existing compact confirmation`, await dialog.getByText('压缩对话', { exact: true }).count() === 1 && (await f.counters()).compact === 0 && (await f.counters()).messages === 0);
      await dialog.getByRole('button', { name: '取消', exact: true }).click(); await dialog.waitFor({ state: 'hidden' });
      check(`${label}: cancelling compact confirmation still makes no control or model call`, (await f.counters()).controls === 0 && (await f.counters()).messages === 0);

      await f.fill('/no-such-suggestion'); check(`${label}: no-match prefix has no menu`, await f.menu().count() === 0);
      await f.input().press('Tab');
      check(`${label}: no-match Tab keeps native focus movement`, await f.input().inputValue() === '/no-such-suggestion' && !await f.input().evaluate(el => el === document.activeElement));
      await f.fill('/com'); await f.menu().waitFor(); await f.input().press('Shift+Tab');
      check(`${label}: Shift Tab remains native focus movement`, await f.input().inputValue() === '/com' && !await f.input().evaluate(el => el === document.activeElement));
      check(`${label}: all keyboard edge cases remain within the fictional fixture`, f.errors.length === 0 && f.escaped.length === 0 && await f.page.evaluate(() => window.__composerFixture.denied.length === 0));
    } finally { await f.context.close(); }
  }

  for (const theme of ['light', 'dark']) {
    const f = await fixture(theme, 1440, true);
    try {
      await f.fill('/'); await f.menu().waitFor(); await verifyAnchor(f, `${theme}/immersive`); await screenshot(f, `menu-immersive-${theme}`);
      const bounds = await f.menu().boundingBox(), inputBounds = await f.input().boundingBox();
      check(`${theme}/immersive: full popup fits above the composer`, bounds.y >= -1 && bounds.y + bounds.height <= inputBounds.y + 1 && bounds.x >= -1 && bounds.x + bounds.width <= 1441 && await f.rows().count() === 14);
      await f.input().press('ArrowDown'); await f.input().press('ArrowDown'); await f.input().press('Tab');
      check(`${theme}/immersive: current highlight completes without a backend action`, await f.input().inputValue() === '/skill:analysis-lab ' && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      check(`${theme}/immersive: no fixture errors or network escapes`, f.errors.length === 0 && f.escaped.length === 0 && await f.page.evaluate(() => window.__composerFixture.denied.length === 0));
    } finally { await f.context.close(); }
  }
  report.passed = true;
} catch (error) {
  report.error = String(error.stack || error);
  try { await currentPage?.screenshot({ path: `${output}/failure.png` }); } catch {}
  console.error(report.error);
  process.exitCode = 1;
} finally {
  await browser.close();
  await fs.writeFile(`${output}/summary.json`, JSON.stringify(report, null, 2), { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output }));
