// Controlled UI checks: real Vue components, fictional browser responses only.
// Run: node dev/native-only-browser-qa.mjs http://127.0.0.1:5189 [output-dir]
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5189';
const output = process.argv[3] || '/tmp/pogo-native-only-ui';
await fs.mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true });
const checks = [];
const check = (name, condition) => { assert.ok(condition, name); checks.push(name); console.log(`PASS ${name}`); };
try {
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 960 } });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', err => errors.push(err.message));
    await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
    await page.goto(`${base}/dev/immersive-browser.html?migration=1`);
    await page.locator('.native-page img').waitFor();
    if (width === 390) {
      await page.locator('.immersion-toggle').click();
      await page.locator('.shell.browser-immersive').waitFor();
      await page.locator('.immersive-chat-head > button').first().click();
    }
    const bookmarks = page.locator('.browser-bookmarks');
    await bookmarks.waitFor();
    check(`${width}: one browser view after migrating multiple dock tabs`, await page.locator('.native-browser').count() === 1);
    await page.locator('.tab.add').click();
    await page.getByRole('menuitem', { name: '浏览器', exact: true }).click();
    check(`${width}: reopening browser reuses the same dock view`, await page.locator('.native-browser').count() === 1 && await page.evaluate(() => JSON.parse(localStorage.getItem('pogo:hub-tabs')).open.filter(tab => tab.kind === 'browser').length) === 1);
    check(`${width}: unresolved bookmark remains visible`, await bookmarks.getByText('待修复', { exact: true }).count() === 2);
    check(`${width}: migration has not navigated the browser`, await page.evaluate(() => window.__immersiveFixture.inputs.length) === 0);
    check(`${width}: original raw tabs backed up`, await page.evaluate(() => !!localStorage.getItem('pogo:hub-tabs:native-only:v1:backup')));
    const rect = await bookmarks.boundingBox();
    check(`${width}: bookmark manager stays inside viewport`, rect.x >= 0 && rect.x + rect.width <= width + 1);
    await page.screenshot({ path: `${output}/migration-${width}.png`, fullPage: false });
    const broken = bookmarks.locator('li').filter({ hasText: '待修复系统' });
    await broken.getByRole('button', { name: '编辑', exact: true }).click();
    await bookmarks.getByRole('textbox', { name: '原站地址', exact: true }).fill('https://repaired.invalid/app?q=1#detail');
    check(`${width}: edit form has no horizontal overflow`, await bookmarks.evaluate(node => node.scrollWidth <= node.clientWidth + 1));
    await page.screenshot({ path: `${output}/repair-${width}.png`, fullPage: false });
    await bookmarks.getByRole('button', { name: '保存', exact: true }).click();
    check(`${width}: edited original URL clears repair state`, await broken.getByText('待修复', { exact: true }).count() === 0);
    await bookmarks.getByRole('button', { name: '添加', exact: true }).click();
    await bookmarks.getByRole('textbox', { name: '书签名称', exact: true }).fill('Added site');
    await bookmarks.getByRole('textbox', { name: '原站地址', exact: true }).fill('https://added.invalid/');
    await bookmarks.getByRole('button', { name: '保存', exact: true }).click();
    check(`${width}: native bookmark add`, await bookmarks.locator('li').filter({ hasText: 'Added site' }).count() === 1);
    await bookmarks.locator('input[type=file]').setInputFiles({ name: 'bookmarks.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({ shortcuts: [{ title: 'Imported site', url: 'https://import.invalid/' }, { title: 'Rejected proxy', url: '/panels/retired/' }] })) });
    await bookmarks.getByText('已导入 1 项，跳过 1 项无效地址。').waitFor();
    await bookmarks.locator('li').filter({ hasText: 'Added site' }).getByRole('button', { name: '删除', exact: true }).click();
    check(`${width}: import and delete update native storage`, await page.evaluate(() => {
      const cards = JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1'));
      return cards.some(card => card.title === 'Imported site') && !cards.some(card => card.title === 'Added site');
    }));
    await page.getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
    await broken.locator('.bookmark-link').click();
    check(`${width}: bookmark opens through native human input only`, await page.evaluate(() => window.__immersiveFixture.inputs.some(input => input.type === 'navigate' && input.url === 'https://repaired.invalid/app?q=1#detail')));
    check(`${width}: no proxy registration or old browser-result request`, await page.evaluate(() => !window.__immersiveFixture.requests.some(req => req.path === '/api/panels' && req.method !== 'GET') && window.__immersiveFixture.denied.length === 0));
    check(`${width}: no client exceptions`, errors.length === 0);
    await page.screenshot({ path: `${output}/bookmarks-${width}.png`, fullPage: false });
    await context.close();
  }
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
  await page.goto(`${base}/dev/immersive-browser.html?unavailable=1`);
  await page.getByText('浏览器连接失败：Native browser service is not configured').waitFor();
  check('unconfigured service has no iframe or native executor view', await page.locator('.native-browser, .hub-pane iframe').count() === 0);
  await page.getByRole('button', { name: '书签', exact: true }).click();
  await page.locator('.browser-bookmarks').waitFor();
  check('bookmarks remain manageable while browser is unavailable', await page.locator('.browser-bookmarks').getByRole('button', { name: '添加', exact: true }).isEnabled());
  await page.screenshot({ path: `${output}/unavailable.png`, fullPage: false });
  await context.close();
  await fs.writeFile(`${output}/result.json`, JSON.stringify({ kind: 'controlled UI fixture; no real browser worker or model', checks }, null, 2));
} finally { await browser.close(); }
