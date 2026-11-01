// Real Vue against a fictional fixture: automatic browser-session reuse has no grant UI.
// Run: node dev/browser-auth-qa.mjs [http://127.0.0.1:5187]
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

const base = process.argv[2] || 'http://127.0.0.1:5187';
const output = process.env.POGO_BROWSER_AUTH_QA_DIR || '/tmp/pogo-browser-auto-session-qa';
await fs.mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true });
const checks = [], screenshots = [], metrics = [];
function check(name, value, detail = '') { assert.ok(value, `${name}: ${detail}`); checks.push(name); console.log(`PASS ${name}`); }
try {
  for (const [surface, theme] of [['desktop', 'light'], ['desktop', 'dark'], ['mobile', 'light'], ['mobile', 'dark']]) {
    const context = await browser.newContext({ viewport: { width: surface === 'mobile' ? 390 : 1440, height: 900 }, hasTouch: surface === 'mobile' });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route(url => url.pathname.startsWith('/api/'), route => route.abort('blockedbyclient'));
    await page.goto(`${base}/dev/immersive-browser.html?theme=${theme}`);
    await page.locator('.native-page img').waitFor();
    const label = `${surface}/${theme}`;
    const snapshot = () => page.evaluate(() => ({ requests: window.__immersiveFixture.requests, picks: window.__immersiveFixture.picks, controls: window.__immersiveFixture.controls, inputs: window.__immersiveFixture.inputs, denied: window.__immersiveFixture.denied }));
    const capture = async name => { const path = `${output}/${surface}-${theme}-${name}.png`; await page.screenshot({ path }); screenshots.push(path); };
    const toggle = page.getByRole('button', { name: '选取页面元素', exact: true });
    const menuButton = page.getByRole('button', { name: '浏览器菜单', exact: true });
    const composer = page.locator('.input-card textarea');
    const draft = '保留这段草稿：请解释选中的网页元素。';
    await page.locator('.immersion-toggle').click();
    await page.locator('.shell.browser-immersive').waitFor();
    await composer.fill(draft);
    await page.locator('.immersive-chat-head > button').first().click();
    const point = async () => {
      const image = page.locator('.native-page img');
      const box = await image.boundingBox();
      const size = await image.evaluate(node => ({ width: node.naturalWidth, height: node.naturalHeight }));
      const scale = Math.min(box.width / size.width, box.height / size.height);
      return { x: box.x + (box.width - size.width * scale) / 2 + 220 * scale, y: box.y + (box.height - size.height * scale) / 2 + 330 * scale };
    };

    check(`${label}: idle agent browser can reference elements without takeover`, await toggle.isEnabled() && (await snapshot()).controls === 0);
    await menuButton.click();
    await page.getByRole('menu').waitFor();
    const items = await page.getByRole('menuitem').allTextContents();
    check(`${label}: browser menu has no manual API authorization option`, !items.some(text => /授权|grant|revoke|token/i.test(text)));
    check(`${label}: tab and help actions remain available in the menu`, ['新标签', '关闭当前标签', '操作说明'].every(text => items.some(item => item.includes(text))));
    check(`${label}: no manual scope or credential dialog remains mounted`, await page.locator('.native-account-dialog').count() === 0);
    await capture('menu-without-grant');
    const beforeOutside = (await snapshot()).inputs.length;
    const target = await point();
    await page.mouse.move(target.x, target.y);
    await page.mouse.wheel(0, 75);
    await page.mouse.click(target.x, target.y);
    await page.getByRole('menu').waitFor({ state: 'hidden' });
    check(`${label}: menu dismissal and wheel never pass through to the webpage`, (await snapshot()).inputs.length === beforeOutside);
    await page.getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
    await page.getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
    check(`${label}: takeover only changes browser control`, (await snapshot()).controls === 1 && (await snapshot()).inputs.length === 0);
    await menuButton.click();
    await page.getByRole('menu').waitFor();
    check(`${label}: new-tab action still works with human control`, await page.getByRole('menuitem', { name: '新标签', exact: true }).getAttribute('aria-disabled') !== 'true');
    await page.keyboard.press('Escape');
    await page.getByRole('menu').waitFor({ state: 'hidden' });
    const beforePick = await snapshot();
    await toggle.click();
    const pick = await point();
    if (surface === 'mobile') await page.touchscreen.tap(pick.x, pick.y);
    else await page.mouse.click(pick.x, pick.y);
    await page.locator('.element-chips .echip').waitFor();
    const afterPick = await snapshot();
    check(`${label}: small hand creates a real composer reference`, afterPick.picks.length > beforePick.picks.length && await page.locator('.echip').count() === 1);
    check(`${label}: selection preserves the composer draft`, await composer.inputValue() === draft);
    check(`${label}: reference selection does not click, navigate or change control`, afterPick.inputs.length === beforePick.inputs.length && afterPick.controls === beforePick.controls);
    check(`${label}: selection leaves pick mode`, await toggle.getAttribute('aria-pressed') === 'false');
    await capture('picked-element');
    await page.getByRole('button', { name: '交还 Agent', exact: true }).click();
    await page.getByRole('button', { name: '停止 Agent 并接管', exact: true }).waitFor();
    const final = await snapshot();
    check(`${label}: return control preserves the reference and draft`, final.controls === 2 && await page.locator('.echip').count() === 1 && await composer.inputValue() === draft);
    check(`${label}: no retired browser/auth request is sent`, final.requests.every(request => !request.path.endsWith('/browser/auth')));
    check(`${label}: browser chrome never sends a model prompt`, final.requests.every(request => !request.path.endsWith('/messages')));
    const layout = await page.locator('.native-toolbar').evaluate(node => ({ scrollWidth: node.scrollWidth, clientWidth: node.clientWidth }));
    metrics.push({ surface, theme, ...layout });
    check(`${label}: toolbar fits the viewport`, layout.scrollWidth <= layout.clientWidth + 1);
    check(`${label}: no page errors or escaped API`, errors.length === 0 && final.denied.length === 0, errors.join('; '));
    await context.close();
  }
  await fs.writeFile(`${output}/summary.json`, JSON.stringify({ kind: 'automatic-session-ui-fixture', authenticated_api_verified: false, checks, metrics, screenshots }, null, 2));
  console.log(`PASS ${checks.length} checks; screenshots and summary: ${output}`);
} finally { await browser.close(); }
