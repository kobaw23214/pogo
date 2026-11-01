import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

process.umask(0o077);
const base = process.argv[2] || 'http://127.0.0.1:19794';
const output = path.resolve(process.argv[3] || '/Users/wangy/Documents/DiscoverAgent/.localagent/checks/composer-tokens-20261009/ui-1');
assert.match(base, /^http:\/\/127\.0\.0\.1:19794$/);
await fs.mkdir(output, { recursive: true, mode: 0o700 });
const report = { scope: 'Actual AgentView and command-token UI using a fictional Agent/session, in-memory SSE/fetch/clipboard and synthetic pick/drop/IME events only; no deployed API, model, real page or browser operations', passed: false, checks: [], screenshots: [] };
const check = (name, value) => { report.checks.push({ name, passed: !!value }); assert.ok(value, name); console.log(`PASS ${name}`); };
const browser = await chromium.launch({ headless: true });
let currentPage;

async function fixture(theme = 'light', width = 1440, immersive = false) {
  const context = await browser.newContext({ viewport: { width, height: 1100 }, locale: 'zh-CN', colorScheme: theme });
  const page = await context.newPage(), errors = [], escaped = [];
  currentPage = page;
  await page.addInitScript(() => {
    Object.assign(window, { __fixtureClipboard: [] });
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async text => { window.__fixtureClipboard.push(text); } }, configurable: true });
  });
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin !== base || url.pathname.startsWith('/api/')) { escaped.push(url.pathname); return route.abort(); }
    return route.continue();
  });
  await page.goto(`${base}/dev/composer-suggestions.html?theme=${theme}${immersive ? '&browser=1' : ''}`);
  const input = () => page.getByTestId('composer-input').filter({ visible: true });
  const token = () => page.getByTestId('composer-token').filter({ visible: true });
  const status = () => page.getByTestId('composer-token-status').filter({ visible: true });
  const menu = () => page.getByTestId('composer-command-menu').filter({ visible: true });
  const rows = () => menu().getByRole('option');
  const row = id => rows().filter({ has: page.locator(`[id$="-${id}-label"]`) });
  const counters = () => page.evaluate(() => ({ messages: window.__composerFixture.messages.length, controls: window.__composerFixture.controls.length, compact: window.__composerFixture.compactCalls }));
  const clear = async () => {
    if (await token().count()) await token().getByRole('button', { name: /^移除/ }).click();
    await input().fill('');
  };
  const fill = async value => { await clear(); await input().fill(value); };
  const emit = async event => page.evaluate(event => window.__composerFixture.emit({ seq: ++window.__fixtureSeq, ts: Date.now(), ...event }), event);
  await page.evaluate(() => { window.__fixtureSeq = 10; });
  await page.waitForFunction(() => window.__composerFixture?.streamsOpened === 1 && window.__composerFixture.requests.some(request => request.path === '/api/skills' && request.query.includes('session_id=composer-fixture-session')));
  if (immersive) {
    const pane = page.locator('.native-browser:visible'), toggle = page.locator('.immersion-toggle');
    if (!await pane.isVisible() && !await toggle.isVisible()) await page.getByRole('button', { name: /^面板(?: ·.*)?$/ }).click();
    await pane.waitFor();
    if (await page.locator('.shell.browser-immersive').count() === 0) { await toggle.waitFor(); await toggle.click(); }
    await page.locator('.shell.browser-immersive').waitFor();
    const restore = page.locator('.immersive-chat-restore');
    if (await restore.isVisible()) await restore.click();
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-pogo-immersive-motion') && !document.querySelector('.shell[data-immersive-motion]'));
  }
  await input().waitFor();
  return { context, page, errors, escaped, input, token, status, menu, rows, row, counters, clear, fill, emit };
}
async function complete(f, prefix, key = 'Tab') {
  await f.fill(prefix); await f.menu().waitFor(); await f.input().press(key); await f.token().waitFor(); await f.menu().waitFor({ state: 'hidden' });
}
async function screenshot(f, name) {
  await f.page.screenshot({ path: `${output}/${name}.png` }); report.screenshots.push(`${name}.png`);
}
async function verifySafe(f, label) {
  check(`${label}: no runtime errors or network escape`, f.errors.length === 0 && f.escaped.length === 0 && await f.page.evaluate(() => window.__composerFixture.denied.length === 0));
}
async function waitSelected(f, suffix) {
  await f.page.waitForFunction(suffix => [...document.querySelectorAll('.slash-command-menu [role=option]')].some(row => row.getAttribute('aria-selected') === 'true' && row.id.endsWith('-' + suffix)), suffix);
}

try {
  for (const [theme, width] of [['light', 1440], ['dark', 1440], ['light', 390], ['dark', 390]]) {
    const f = await fixture(theme, width), label = `${theme}/${width}`;
    try {
      await complete(f, '/skill:BROWSER', 'Tab');
      check(`${label}: Tab creates scoped browser-use skill token and leaves empty native textarea focused`, await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '' && await f.input().evaluate(el => el === document.activeElement));
      check(`${label}: completed token is a distinguishable colored/icon control with remove and change`, await f.token().locator('svg').count() > 0 && await f.token().getByRole('button', { name: /^更换/ }).count() === 1 && await f.token().getByRole('button', { name: /^移除/ }).count() === 1 && await f.token().evaluate(el => getComputedStyle(el).backgroundColor !== 'rgba(0, 0, 0, 0)'));
      await f.input().fill('帮我打开 GitLab，保留这段正文。');
      await screenshot(f, `skill-token-${theme}-${width}`);
      const bounds = await f.token().boundingBox();
      check(`${label}: chip fits composer row and viewport`, bounds.x >= 0 && bounds.x + bounds.width <= width + 1 && await f.token().evaluate(el => { const row = el.closest('.composer-token-row'); return row && el.getBoundingClientRect().right <= row.getBoundingClientRect().right + 1; }));
      await f.token().getByRole('button', { name: /^移除/ }).click();
      check(`${label}: removing skill keeps task and focus`, await f.token().count() === 0 && await f.input().inputValue() === '帮我打开 GitLab，保留这段正文。' && await f.input().evaluate(el => el === document.activeElement));

      await complete(f, '/skill:d', 'Enter');
      check(`${label}: Enter completes first matching data-table into token without sending`, await f.token().getByText('data-table', { exact: true }).count() === 1 && (await f.counters()).messages === 0);
      await f.input().fill('这是一段不能丢失的任务');
      await f.token().getByRole('button', { name: /^更换/ }).click(); await f.menu().waitFor();
      const search = f.page.getByTestId('composer-token-search');
      await search.fill('d'); await waitSelected(f, 'skill-data-table');
      await search.press('ArrowDown'); await waitSelected(f, 'skill-diagram-kit');
      await search.press('Enter'); await f.menu().waitFor({ state: 'hidden' });
      check(`${label}: keyboard changes selected skill and preserves task`, await f.token().getByText('diagram-kit', { exact: true }).count() === 1 && await f.input().inputValue() === '这是一段不能丢失的任务' && (await f.counters()).messages === 0);

      await f.input().press('Home'); await f.input().press('Backspace');
      check(`${label}: first Backspace at body start focuses token and preserves body`, await f.token().count() === 1 && await f.token().evaluate(el => el.contains(document.activeElement)) && await f.input().inputValue() === '这是一段不能丢失的任务');
      await f.page.keyboard.press('Backspace');
      check(`${label}: second Backspace removes selected token only`, await f.token().count() === 0 && await f.input().inputValue() === '这是一段不能丢失的任务');

      await complete(f, '/com');
      check(`${label}: compact is a distinct command token with unchanged current/total`, await f.token().getByText('压缩上下文', { exact: true }).count() === 1 && /35,678\/100,000/.test(await f.page.locator('.input-card').textContent()) && await f.input().inputValue() === '');
      await screenshot(f, `compact-token-${theme}-${width}`);
      await f.input().press('Enter'); const dialog = f.page.locator('.el-message-box:visible'); await dialog.waitFor();
      check(`${label}: deliberate second Enter still requires compact confirmation`, await dialog.getByText('压缩对话', { exact: true }).count() === 1 && (await f.counters()).compact === 0 && (await f.counters()).messages === 0);
      await dialog.getByRole('button', { name: '取消', exact: true }).click(); await dialog.waitFor({ state: 'hidden' });
      check(`${label}: cancel compact sends no control request`, (await f.counters()).controls === 0);
      await verifySafe(f, label);
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture('dark'), label = 'paste/IME/unknown/busy';
    try {
      await f.fill('/skill:browser-use 请检查这个页面'); await f.token().waitFor();
      check(`${label}: manually typed or pasted known prefix resolves to token and task`, await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '请检查这个页面');
      for (const options of [{ isComposing: true }, { keyCode: 229 }]) {
        await f.input().evaluate((el, options) => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...options })), options);
        check(`${label}: IME ${options.isComposing ? 'composing' : '229'} preserves skill/task and starts no request`, await f.input().inputValue() === '请检查这个页面' && await f.token().count() === 1 && (await f.counters()).messages === 0);
      }
      await f.input().evaluate(el => { el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace', bubbles: true, cancelable: true })); el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); });
      check(`${label}: composition lifecycle protects task and token`, await f.input().inputValue() === '请检查这个页面' && await f.token().count() === 1 && (await f.counters()).messages === 0);
      await f.input().evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));

      await f.fill('/skill:not-in-this-agent 仍保留任务'); await f.status().waitFor();
      await f.input().press('Enter');
      check(`${label}: unknown skill reports actionable warning and cannot send`, await f.token().count() === 0 && (await f.status().textContent()).includes('not-in-this-agent') && (await f.counters()).messages === 0 && (await f.counters()).controls === 0 && await f.input().inputValue() === '/skill:not-in-this-agent 仍保留任务');
      await screenshot(f, 'unknown-token-dark');

      await complete(f, '/skill:browser'); await f.input().fill('运行期间保留任务');
      await f.emit({ type: 'run_start', run_id: 'synthetic-busy-run', model: 'fixture-model' });
      await f.status().waitFor();
      check(`${label}: running skill shows wait message and disabled send`, /当前任务结束后/.test(await f.status().textContent()) && await f.page.locator('.send-btn').isDisabled());
      await f.input().press('Enter');
      check(`${label}: busy Enter cannot expand skill through steering and retains draft`, (await f.counters()).controls === 0 && (await f.counters()).messages === 0 && await f.input().inputValue() === '运行期间保留任务' && await f.token().getByText('browser-use', { exact: true }).count() === 1);
      await f.token().getByRole('button', { name: /^移除/ }).click(); await f.input().fill('普通运行中指导');
      await f.input().press('Enter');
      await f.page.waitForFunction(() => window.__composerFixture.controls.some(request => request.action === 'steer'));
      await f.page.waitForFunction(() => document.querySelector('[data-testid="composer-input"]').value === '');
      check(`${label}: ordinary prose still steers current run`, await f.page.evaluate(() => window.__composerFixture.controls.at(-1).action === 'steer' && window.__composerFixture.controls.at(-1).prompt === '普通运行中指导') && await f.input().inputValue() === '' && (await f.counters()).messages === 0);
      await verifySafe(f, label);
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture('light'), label = 'chips/pending/history/copy/edit';
    try {
      await complete(f, '/skill:browser');
      await f.page.evaluate(() => window.__composerFixture.addPick({ key: 'fixture-exhibit', label: 'button "搜索"', block: '<page-element>\n虚构引用元素\n</page-element>', pageTitle: '隔离测试页', preview: 'fictional exhibit' }));
      await f.page.locator('.echip').waitFor();
      await f.page.locator('.input-card').evaluate(el => { const transfer = new DataTransfer(); transfer.items.add(new File(['fictional attachment'], 'fixture-note.txt', { type: 'text/plain' })); el.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer })); });
      await f.page.locator('.attach-chip').waitFor();
      await f.input().focus(); await f.input().press('Backspace'); await f.page.keyboard.press('Backspace');
      check(`${label}: two-step token deletion preserves page pick and ready attachment`, await f.token().count() === 0 && await f.page.locator('.echip').count() === 1 && await f.page.locator('.attach-chip.ready').count() === 1);
      await f.page.locator('.echip-x').click(); await f.page.locator('.attach-x').click();

      await complete(f, '/skill:browser'); await f.input().fill('请打开 GitLab 并看看项目');
      await f.page.evaluate(() => { window.__composerFixture.holdMessages = true; });
      await f.input().press('Enter'); await f.page.waitForFunction(() => window.__composerFixture.messages.length === 1);
      const pending = f.page.locator('.ask').last();
      check(`${label}: message transport preserves exact canonical skill invocation`, await f.page.evaluate(() => window.__composerFixture.messages.at(-1).prompt === '/skill:browser-use 请打开 GitLab 并看看项目'));
      check(`${label}: optimistic message uses same skill chip and visible task`, await pending.getByText('browser-use', { exact: true }).count() === 1 && (await pending.textContent()).includes('请打开 GitLab 并看看项目') && !(await pending.textContent()).includes('/skill:'));
      await screenshot(f, 'pending-skill-light');
      await f.page.evaluate(() => { window.__composerFixture.resolveMessage(); });
      await f.page.waitForFunction(() => document.querySelector('[data-testid="composer-input"]').value === '');
      const clientId = await f.page.evaluate(() => window.__composerFixture.messages.at(-1).client_id);
      await f.emit({ type: 'user_message', message_id: 'fixture-expanded-skill', client_id: clientId, content: [{ type: 'text', text: '<skill name="browser-use" location="/fixture/builtin/browser-use/SKILL.md">\n# Browser use\n\nFictional instructions for display tests.\n</skill>\n\n请打开 GitLab 并看看项目' }] });
      const history = f.page.locator('.ask[data-mid="fixture-expanded-skill"]'); await history.waitFor();
      check(`${label}: expanded stored message retains matching token and task without expanded instructions`, await history.getByText('browser-use', { exact: true }).count() === 1 && (await history.textContent()).includes('请打开 GitLab 并看看项目') && !(await history.textContent()).includes('Fictional instructions'));
      await screenshot(f, 'history-skill-light');
      await history.hover(); await history.getByRole('button', { name: '复制', exact: true }).click();
      check(`${label}: copying expanded history returns canonical invocation rather than instructions`, await f.page.evaluate(() => window.__fixtureClipboard.at(-1) === '/skill:browser-use 请打开 GitLab 并看看项目'));
      await history.getByRole('button', { name: /编辑/ }).click(); await f.token().waitFor();
      check(`${label}: editing expanded history restores editable token and task`, await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '请打开 GitLab 并看看项目');
      await f.page.evaluate(() => { window.__composerFixture.holdMessages = false; window.__composerFixture.failMessages = true; });
      await f.input().press('Enter'); await f.page.waitForFunction(() => window.__composerFixture.messages.length === 2);
      await f.page.waitForFunction(() => !document.querySelector('.send-btn')?.disabled);
      check(`${label}: failed send restores matching command token and body without a phantom message`, await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '请打开 GitLab 并看看项目' && await f.page.locator('.ask').count() === 2);
      await verifySafe(f, label);
    } finally { await f.context.close(); }
  }

  {
    const f = await fixture('dark', 390), label = 'IME search/NBSP/attachment restoration';
    try {
      await complete(f, '/skill:browser'); await f.input().fill('更换时保留中文任务');
      await f.token().getByRole('button', { name: /^更换/ }).click(); await f.menu().waitFor();
      const search = f.page.getByTestId('composer-token-search'); await search.waitFor();
      await search.evaluate(el => { window.__fixtureSearchNode = el; el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); });
      check(`${label}: starting IME in token search preserves the same mounted focused input`, await search.count() === 1 && await search.evaluate(el => el === window.__fixtureSearchNode && el.isConnected && el === document.activeElement));
      for (const key of ['Enter', 'Tab', 'ArrowDown']) {
        await search.evaluate((el, key) => el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })), key);
        check(`${label}: ${key} during active search composition does not complete or change token`, await search.count() === 1 && await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '更换时保留中文任务' && (await f.counters()).messages === 0 && (await f.counters()).controls === 0);
      }
      await search.evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));
      await search.fill('docs'); await waitSelected(f, 'skill-docs'); await search.press('Tab'); await f.menu().waitFor({ state: 'hidden' });
      check(`${label}: after composition finishes search can complete normally without losing task`, await f.token().getByText('docs', { exact: true }).count() === 1 && await f.input().inputValue() === '更换时保留中文任务' && await f.input().evaluate(el => el === document.activeElement));

      await f.fill('/skill:docs\u00a0任务'); await f.status().waitFor();
      check(`${label}: NBSP cannot silently become a known docs skill or valid separator`, await f.token().count() === 0 && /未找到技能/.test(await f.status().textContent()) && await f.input().inputValue() === '/skill:docs\u00a0任务' && await f.page.locator('.send-btn').isDisabled());
      await f.input().press('Enter');
      check(`${label}: NBSP malformed invocation sends no backend operation and retains raw draft`, (await f.counters()).messages === 0 && (await f.counters()).controls === 0 && await f.input().inputValue() === '/skill:docs\u00a0任务');

      await complete(f, '/skill:docs');
      await f.page.locator('.input-card').evaluate(el => { const transfer = new DataTransfer(); transfer.items.add(new File(['fictional attachment for failed upload'], 'fixture-fail.txt', { type: 'text/plain' })); el.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer })); });
      await f.page.locator('.attach-chip.ready').waitFor();
      await f.page.evaluate(() => { window.__composerFixture.failUploads = true; });
      await f.input().focus(); await f.input().press('Enter');
      await f.page.waitForFunction(() => window.__composerFixture.uploads.length === 1 && document.querySelector('.attach-chip.error') && document.querySelector('[data-testid="composer-token"][data-name="docs"]') && !document.querySelector('.send-btn')?.disabled);
      check(`${label}: failed attachment upload restores selected docs token with empty body`, await f.token().getByText('docs', { exact: true }).count() === 1 && await f.input().inputValue() === '' && await f.page.locator('.attach-chip.error').count() === 1);
      check(`${label}: upload failure never submits message and drops optimistic placeholder`, (await f.counters()).messages === 0 && (await f.counters()).controls === 0 && await f.page.locator('.ask').count() === 1 && await f.page.evaluate(() => window.__composerFixture.uploads[0].name === 'fixture-fail.txt'));
      await screenshot(f, 'upload-failed-empty-skill-dark-390');
      await verifySafe(f, label);
    } finally { await f.context.close(); }
  }

  for (const theme of ['light', 'dark']) {
    const f = await fixture(theme, 1440, true), label = `${theme}/immersive`;
    try {
      await complete(f, '/skill:browser'); await f.input().fill('沉浸模式也保持原生输入与技能标签');
      await screenshot(f, `token-immersive-${theme}`);
      check(`${label}: token layout and body remain visible in actual immersive composer`, await f.token().getByText('browser-use', { exact: true }).count() === 1 && await f.input().inputValue() === '沉浸模式也保持原生输入与技能标签' && await f.token().evaluate(el => { const row = el.closest('.composer-token-row'); return row && el.getBoundingClientRect().right <= row.getBoundingClientRect().right + 1; }));
      await verifySafe(f, label);
    } finally { await f.context.close(); }
  }
  report.passed = true;
} catch (error) {
  report.error = String(error.stack || error);
  try { await currentPage?.screenshot({ path: `${output}/failure.png` }); } catch {}
  console.error(report.error); process.exitCode = 1;
} finally {
  await browser.close();
  await fs.writeFile(`${output}/summary.json`, JSON.stringify(report, null, 2), { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output }));
