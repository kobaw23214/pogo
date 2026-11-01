import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

// Incremental coverage only. The full keyboard/theme matrix remains in the
// separate composer-suggestions QA. All transport here is fictional/in-memory.
process.umask(0o077);
const base = process.argv[2] || 'http://127.0.0.1:19793';
const output = path.resolve(process.argv[3]);
assert.match(base, /^http:\/\/127\.0\.0\.1:19793$/);
await fs.mkdir(path.dirname(output), { recursive: true, mode: 0o700 });
await fs.mkdir(output, { mode: 0o700 });
const report = { scope: 'Targeted context ratio in real AgentView; synthetic snapshot/message/run_end/model_changed SSE only, no real model/API or deployment', passed: false, checks: [], geometry: [], screenshots: [] };
const check = (name, value) => { report.checks.push({ name, passed: !!value }); assert.ok(value, name); console.log(`PASS ${name}`); };
const browser = await chromium.launch({ headless: true });
let currentPage;

async function fixture(theme, immersive) {
  const context = await browser.newContext({ viewport: { width: immersive ? 1440 : 390, height: 1000 }, locale: 'zh-CN', colorScheme: theme });
  const page = await context.newPage(), errors = [], escaped = [];
  currentPage = page;
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin !== base || url.pathname.startsWith('/api/')) { escaped.push(url.pathname); return route.abort(); }
    return route.continue();
  });
  await page.goto(`${base}/dev/composer-suggestions.html?theme=${theme}${immersive ? '&browser=1' : ''}`);
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
  const input = page.locator('[data-testid="composer-input"]:visible');
  await input.waitFor();
  const menu = page.locator('.slash-command-menu:visible');
  const description = menu.locator('[id$="-command-compact-description"]');
  const fill = async value => { await input.fill(''); await input.fill(value); };
  const counters = () => page.evaluate(() => ({ messages: window.__composerFixture.messages.length, controls: window.__composerFixture.controls.length, compact: window.__composerFixture.compactCalls }));
  let seq = 1;
  const emit = async event => page.evaluate(event => window.__composerFixture.emit(event), { seq: ++seq, ts: Date.now(), ...event });
  const snapshot = async (used, total = 100000) => {
    const next = ++seq;
    await page.evaluate(({ next, used, total }) => window.__composerFixture.emit({ type: 'snapshot', seq: next, ts: Date.now(), snapshot: {
      seq: next, messages: [
        { id: 'fixture-u1', role: 'user', content: [{ type: 'text', text: '请保留这段虚构会话，用于输入框验收。' }] },
        { id: 'fixture-a1', role: 'assistant', content: [{ type: 'text', text: '这个页面仅连接内存模拟数据，可以验证命令菜单与键盘补全。' }] },
      ], context_tokens: used, usage: { input: 4600, output: 30 },
      run: { active: false, model: 'fixture-model', context_window: total }, policy: { mode: 'standard' },
    } }), { next, used, total });
  };
  const ratio = async (used, total, label) => {
    const expected = `压缩此聊天的上下文（${used}/${total} tokens）`;
    await page.waitForFunction(expected => document.querySelector('[id$="-command-compact-description"]')?.textContent === expected, expected);
    check(label, await description.textContent() === expected);
  };
  return { context, page, input, menu, description, fill, counters, emit, snapshot, ratio, errors, escaped };
}

async function visibleRatio(f, label, expectedCardWidth) {
  await f.page.waitForFunction(() => {
    const menu = document.querySelector('.slash-command-menu'), card = [...document.querySelectorAll('.input-card')].find(el => el.getBoundingClientRect().width > 0);
    if (!menu || !card || document.documentElement.hasAttribute('data-pogo-immersive-motion') || document.querySelector('.shell[data-immersive-motion]')) return false;
    const m = menu.getBoundingClientRect(), c = card.getBoundingClientRect();
    return Math.abs(c.top - m.bottom - 8) <= 1 && Math.abs(m.left - c.left) <= 1 && Math.abs(m.width - c.width) <= 1;
  });
  const geometry = await f.description.evaluate(el => {
    const row = el.closest('[role=option]'), menu = el.closest('.slash-command-menu'), card = [...document.querySelectorAll('.input-card')].find(el => el.getBoundingClientRect().width > 0);
    const text = el.firstChild, start = el.textContent.indexOf('（');
    const range = document.createRange(); range.setStart(text, start); range.setEnd(text, el.textContent.length);
    const rect = value => ({ x: value.x, y: value.y, right: value.right, bottom: value.bottom, width: value.width, height: value.height });
    return { text: el.textContent, fragments: [...range.getClientRects()].map(rect), row: rect(row.getBoundingClientRect()), menu: rect(menu.getBoundingClientRect()), card: rect(card.getBoundingClientRect()), description: rect(el.getBoundingClientRect()), whiteSpace: getComputedStyle(el).whiteSpace, overflow: getComputedStyle(el).overflow, viewport: { width: innerWidth, height: innerHeight } };
  });
  report.geometry.push({ label, ...geometry });
  check(`${label}: command description wraps without ellipsis clipping`, geometry.whiteSpace === 'normal' && geometry.overflow === 'visible');
  check(`${label}: complete ratio fragments fit the option and viewport`, geometry.fragments.length > 0 && geometry.fragments.every(r => r.x >= geometry.row.x - 1 && r.right <= geometry.row.right + 1 && r.y >= geometry.row.y - 1 && r.bottom <= geometry.row.bottom + 1 && r.x >= 0 && r.right <= geometry.viewport.width + 1 && r.y >= 0 && r.bottom <= geometry.viewport.height + 1));
  check(`${label}: steady popup matches ${expectedCardWidth}px composer with 8px gap`, Math.abs(geometry.card.width - expectedCardWidth) <= 1 && Math.abs(geometry.card.y - geometry.menu.bottom - 8) <= 1 && Math.abs(geometry.card.x - geometry.menu.x) <= 1 && Math.abs(geometry.card.width - geometry.menu.width) <= 1);
}

try {
  const scenarios = process.argv[4] === '--immersive-only' ? [['light', true]] : [['dark', false], ['light', true]];
  for (const [theme, immersive] of scenarios) {
    const f = await fixture(theme, immersive), label = immersive ? 'light/immersive414' : 'dark/viewport390';
    try {
      await f.fill('/'); await f.menu.waitFor();
      await f.ratio('35,678', '100,000', `${label}: initial current prompt and total window show full separated digits`);
      await f.emit({ type: 'message', message_id: 'ratio-1', role: 'assistant', content: [{ type: 'text', text: '合成上下文用量一' }], usage: { input: 1200, output: 10 } });
      await f.ratio('1,200', '100,000', `${label}: first SSE message usage updates the open menu immediately`);
      await f.emit({ type: 'run_end', usage: { input: 1200, output: 10 } });
      await f.emit({ type: 'message', message_id: 'ratio-2', role: 'assistant', content: [{ type: 'text', text: '合成上下文用量二' }], usage: { input: 3400, output: 20 } });
      await f.ratio('3,400', '100,000', `${label}: next prompt replaces previous occupancy rather than accumulating`);
      await f.emit({ type: 'run_end', usage: { input: 4600, output: 30 } });
      // Queue barrier: model change follows run_end in the same actual SSE reader.
      await f.emit({ type: 'model_changed', model: 'fixture-large', provider: 'fixture', context_window: 200000 });
      await f.ratio('3,400', '200,000', `${label}: cumulative billed input 4,600 does not replace current prompt 3,400`);
      check(`${label}: live updates retain the slash draft and original SSE connection`, await f.input.inputValue() === '/' && await f.page.evaluate(() => window.__composerFixture.streamsOpened === 1));
      await f.snapshot(0);
      await f.ratio('—', '100,000', `${label}: unmeasured current context displays a dash`);
      await f.snapshot(35678);
      await f.ratio('35,678', '100,000', `${label}: snapshot restores measured context`);
      await f.emit({ type: 'model_changed', model: 'fixture-unknown', provider: 'fixture' });
      await f.ratio('35,678', '—', `${label}: omitted model window clears the prior limit and displays a dash`);
      await f.snapshot(35678);
      await f.ratio('35,678', '100,000', `${label}: known measured ratio restores after unknown window`);
      await visibleRatio(f, label, immersive ? 414 : 270);
      const name = `context-${immersive ? 'immersive-light-414' : 'dark-390'}.png`;
      await f.page.screenshot({ path: `${output}/${name}` }); report.screenshots.push(name);

      await f.fill('/com'); await f.menu.waitFor();
      await f.input.press('Tab'); await f.menu.waitFor({ state: 'detached' });
      check(`${label}: Tab only completes compact with trailing space`, await f.input.inputValue() === '/compact ' && JSON.stringify(await f.counters()) === JSON.stringify({ messages: 0, controls: 0, compact: 0 }));
      await f.fill('/com'); await f.menu.waitFor();
      await f.page.keyboard.down('Enter'); await f.menu.waitFor({ state: 'detached' });
      await f.input.evaluate(el => el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', repeat: true, bubbles: true, cancelable: true })));
      await f.page.keyboard.up('Enter');
      check(`${label}: Enter only completes and a held repeat cannot execute`, await f.input.inputValue() === '/compact ' && await f.page.locator('.el-message-box:visible').count() === 0 && JSON.stringify(await f.counters()) === JSON.stringify({ messages: 0, controls: 0, compact: 0 }));
      await f.input.press('Enter'); const dialog = f.page.locator('.el-message-box:visible'); await dialog.waitFor();
      await dialog.getByRole('button', { name: '取消', exact: true }).click(); await dialog.waitFor({ state: 'hidden' });
      check(`${label}: next deliberate Enter reaches compact confirmation without a model/control request`, JSON.stringify(await f.counters()) === JSON.stringify({ messages: 0, controls: 0, compact: 0 }));
      await f.fill('/'); await f.menu.waitFor(); await f.input.press('ArrowDown'); await f.input.press('Tab');
      await f.page.waitForFunction(() => document.querySelector('[data-testid="composer-input"]').value === '/skill:' && document.querySelectorAll('.slash-command-menu [role=option]').length === 12);
      await f.input.press('Enter'); await f.menu.waitFor({ state: 'detached' });
      check(`${label}: two-stage skill entry still completes without duplicate send`, await f.input.inputValue() === '/skill:analysis-lab ' && JSON.stringify(await f.counters()) === JSON.stringify({ messages: 0, controls: 0, compact: 0 }));
      check(`${label}: no page error or real API/network escape`, f.errors.length === 0 && f.escaped.length === 0 && await f.page.evaluate(() => window.__composerFixture.denied.length === 0));
    } finally { await f.context.close(); }
  }
  report.passed = true;
} catch (error) {
  report.error = String(error.stack || error); process.exitCode = 1;
  try { await currentPage?.screenshot({ path: `${output}/failure.png` }); } catch {}
  console.error(report.error);
} finally {
  await browser.close();
  await fs.writeFile(`${output}/summary.json`, JSON.stringify(report, null, 2), { mode: 0o600 });
}
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output }));
