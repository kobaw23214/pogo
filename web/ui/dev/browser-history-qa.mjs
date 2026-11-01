import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

const base = process.argv[2] || 'http://127.0.0.1:19805';
const output = process.argv[3] || '/tmp/pogo-browser-history-ui';
assert.ok(['127.0.0.1', 'localhost'].includes(new URL(base).hostname));
await fs.mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true });
const checks = [];
const check = (name, condition) => { assert.ok(condition, name); checks.push(name); console.log('PASS', name); };
try {
  for (const width of [1440, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 960 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => localStorage.setItem('pogo-locale', 'zh-CN'));
    await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
    await page.goto(base + '/dev/desktop-browser.html?display=desktop');
    const pane = page.locator('.desktop-browser:visible');
    const back = pane.getByTestId('browser-back');
    await back.waitFor();
    check(`${width}: desktop Back precedes Reload`, await back.evaluate(button => button.nextElementSibling?.getAttribute('aria-label') === '重新载入网页'));
    check(`${width}: desktop fresh tab Back is disabled`, await back.isDisabled());
    await pane.getByRole('button', { name: '接管此页', exact: true }).click();
    await pane.getByRole('button', { name: '交还此页', exact: true }).waitFor();
    check(`${width}: desktop takeover does not invent history`, await back.isDisabled());
    const address = pane.getByRole('textbox', { name: '原站地址', exact: true });
    for (const suffix of ['/a', '/b']) {
      await address.fill('https://fixture.invalid' + suffix); await address.press('Enter');
      await page.waitForFunction(url => window.__desktopQA.model.tabs[0].url === url, 'https://fixture.invalid' + suffix);
    }
    await back.click();
    await page.waitForFunction(() => window.__desktopQA.model.tabs[0].url === 'https://fixture.invalid/a');
    check(`${width}: desktop Back issues one history input`, await page.evaluate(() => window.__desktopQA.counters.inputs.filter(input => input.type === 'back').length === 1));
    check(`${width}: desktop Back restores A and leaves sibling B alone`, await address.inputValue() === 'https://fixture.invalid/a' && await page.evaluate(() => window.__desktopQA.model.tabs[1].url === 'https://fixture.invalid/b'));
    await back.click();
    await page.waitForFunction(() => window.__desktopQA.model.tabs[0].url === 'about:blank');
    check(`${width}: desktop Back disables at the actual boundary`, await back.isDisabled());
    await page.screenshot({ path: `${output}/desktop-${width}.png` });
    check(`${width}: desktop no browser exception`, errors.length === 0);
    await page.close();
  }

  // Native toolbar component contract with synthetic state only. Real
  // Chromium A→B→Back is independently exercised by runtime/history.test.js.
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
  await page.goto(base + '/dev/browser-sheets.html');
  await page.waitForFunction(() => window.__sheetsFixture);
  await page.evaluate(() => window.__sheetsFixture.patchState({ url: 'about:blank', title: 'Blank fixture', can_go_back: false, page_loading: false }));
  const pane = page.locator('.native-browser:visible');
  const back = pane.getByTestId('browser-back');
  await back.waitFor();
  check('native Back precedes Reload', await back.evaluate(button => button.nextElementSibling?.querySelector('path')?.getAttribute('d') === 'M16 6.5a6.5 6.5 0 1 0 .3 6M16 2.5v4.5h-4.5'));
  check('native observing a page never grants history navigation', await back.isDisabled());
  await page.screenshot({ path: `${output}/native.png` });
  check('native no browser exception', errors.length === 0);
  await fs.writeFile(`${output}/summary.json`, JSON.stringify({ passed: true, scope: 'Desktop toolbar controlled A→B→Back; Native toolbar ordering and disabled states only. Real traversal independently tested in Chromium worker.', checks }, null, 2));
} finally { await browser.close(); }
