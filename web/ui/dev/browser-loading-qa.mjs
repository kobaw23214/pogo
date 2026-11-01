// Controlled Vue/browser loading regression. All API traffic is in-memory;
// it never uses deployed sessions, models, or the user's browser.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5189';
const output = process.argv[3] || '/tmp/pogo-browser-loading';
await fs.mkdir(output, { recursive: true });
const checks = [];
const check = (label, condition) => { assert.ok(condition, label); checks.push(label); console.log(`PASS ${label}`); };
const browser = await chromium.launch({ headless: true });
const contexts = [];
const setup = async (width = 1440, theme = 'light', extra = '') => {
  const context = await browser.newContext({ viewport: { width, height: 900 } }); contexts.push(context);
  const page = await context.newPage(); const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
  await page.goto(`${base}/dev/immersive-browser.html?pane-only&theme=${theme}${extra}`);
  return { page, errors, context };
};
const refresh = page => page.getByRole('button', { name: '刷新画面', exact: true }).click();
const image = page => page.locator('.native-page img');
const loading = page => page.locator('.connection-dot.loading');
const address = page => page.getByRole('textbox', { name: '原站地址', exact: true });
const fresh = page => page.locator('.native-page:not(.readonly)').waitFor();
const frames = page => page.evaluate(() => window.__immersiveFixture.requests.filter(req => req.path.endsWith('/browser/frame')).length);
try {
  for (const [width, theme] of [[1440, 'light'], [1440, 'dark'], [390, 'light'], [390, 'dark']]) {
    const { page, errors, context } = await setup(width, theme, '&stateDelay=650&frameDelay=1000');
    const prefix = `${theme}/${width}`;
    await page.locator('.browser-connect').waitFor();
    check(`${prefix}: connection bootstrap has toolbar and theme canvas, no central waiting`, await page.locator('.connect-toolbar').count() === 1 && await page.locator('.connect-page').innerText() === '' && await page.locator('.connect-indicator.loading').count() === 1);
    await page.screenshot({ path: `${output}/connecting-${theme}-${width}.png` });
    await page.locator('.native-browser').waitFor();
    check(`${prefix}: first frame is loading in address bar only`, await loading(page).count() === 1 && await page.locator('.native-page').innerText() === '' && await image(page).count() === 0);
    check(`${prefix}: initial state is reused without a duplicate GET`, await page.evaluate(() => window.__immersiveFixture.requests.filter(req => req.path.endsWith('/browser')).length) === 1);
    check(`${prefix}: canvas uses the current theme`, await page.locator('.native-page').evaluate(el => getComputedStyle(el).backgroundColor === getComputedStyle(el.closest('.native-browser')).backgroundColor));
    await page.screenshot({ path: `${output}/first-frame-${theme}-${width}.png` });
    await image(page).waitFor();
    await page.waitForFunction(() => !document.querySelector('.connection-dot.loading'));
    await page.evaluate(() => { const f = window.__immersiveFixture; f.stateDelay = 0; f.frameDelay = 0; f.patch({ page_loading: true, busy: true }); });
    await refresh(page); await loading(page).waitFor();
    check(`${prefix}: loading shows already captured pixels and disables element picks`, await image(page).isVisible() && await page.locator('.native-page.readonly').count() === 1 && await page.getByRole('button', { name: '选取页面元素', exact: true }).isDisabled());
    await page.locator('.native-loading-status').waitFor();
    check(`${prefix}: slow loading uses one nonblocking status message without a duplicate busy banner`, await page.locator('.native-statusbar .native-loading-status').count() === 1 && await page.locator('.native-activity').count() === 0 && await page.locator('.native-statusbar').evaluate(el => getComputedStyle(el).pointerEvents === 'none'));
    await page.screenshot({ path: `${output}/loading-partial-${theme}-${width}.png` });
    await page.evaluate(() => { const f = window.__immersiveFixture; f.frameStatus = 502; f.frameError = 'Fixture screenshot timeout'; });
    await refresh(page); await page.locator('.native-frame-status.unavailable').waitFor();
    check(`${prefix}: frame failure does not hide real page loading or report disconnection`, await loading(page).count() === 1 && await page.locator('.native-error').count() === 0 && (await page.locator('.native-frame-status').getAttribute('title')).includes('Fixture screenshot timeout'));
    await page.evaluate(() => { const f = window.__immersiveFixture; f.frameStatus = 200; f.patch({ page_loading: false, busy: false, page_load_error: 'Page navigation failed.' }); });
    await refresh(page); await page.locator('.native-load-error').waitFor();
    check(`${prefix}: definite navigation error stops loading and exposes a localized error`, await loading(page).count() === 0 && (await page.locator('.native-load-error').innerText()).includes('网页加载失败') && (await page.locator('.native-load-error').getAttribute('title')) === 'Page navigation failed.');
    await page.screenshot({ path: `${output}/page-error-${theme}-${width}.png` });
    await page.evaluate(() => { const f = window.__immersiveFixture; f.patch({ page_load_error: undefined }); f.offline = true; });
    await refresh(page); await page.locator('.native-error').waitFor();
    check(`${prefix}: failed state reports disconnection, stops spinner, and keeps old pixels read-only`, (await page.locator('.connection-dot').getAttribute('aria-label')).includes('连接中断') && await loading(page).count() === 0 && await image(page).isVisible() && await page.locator('.native-page.readonly').count() === 1);
    check(`${prefix}: toolbar stays in viewport`, await page.locator('.native-toolbar').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    check(`${prefix}: no console errors or escaped API calls`, errors.length === 0 && await page.evaluate(() => window.__immersiveFixture.denied.length) === 0);
    await context.close();
  }

  const { page, errors, context } = await setup();
  await image(page).waitFor();
  await page.getByRole('button', { name: '停止 Agent 并接管', exact: true }).click(); await fresh(page);
  await page.evaluate(() => { const f = window.__immersiveFixture; f.patch({ busy: true, activity: { last_action: { seq: 1, id: 'read', actor: 'agent', action: 'snapshot', phase: 'started', tab_id: 't1', version: 'fixture:1', label: '', started_at: 1, updated_at: 1 }, recent_actions: [] } }); });
  await refresh(page); await page.locator('.native-activity').waitFor();
  check('snapshot activity does not animate page loading', await loading(page).count() === 0 && (await page.locator('.native-statusbar .native-activity').innerText()).includes('读取页面'));
  await page.evaluate(() => window.__immersiveFixture.patch({ busy: false, activity: undefined })); await refresh(page); await fresh(page);

  const old = await image(page).getAttribute('src'); const beforeFrames = await frames(page);
  await page.evaluate(() => { const f = window.__immersiveFixture; f.navigationPrecommitDelay = 1000; f.navigationDelay = 1700; f.inputDelay = 1400; f.frameDelay = 250; f.staleInputReply = true; });
  await address(page).fill('https://next.invalid/slow'); await address(page).press('Enter');
  await loading(page).waitFor();
  check('manual navigation retains same-tab old pixels while disabling input', await image(page).getAttribute('src') === old && await page.locator('.native-page.readonly').count() === 1);
  await page.waitForFunction(() => window.__immersiveFixture.state().page_loading === true);
  await page.waitForTimeout(500);
  check('loading true on the previous document does not overwrite the requested address', await address(page).inputValue() === 'https://next.invalid/slow');
  await page.waitForFunction(before => window.__immersiveFixture.requests.filter(req => req.path.endsWith('/browser/frame')).length >= before + 2, beforeFrames);
  check('state and frames continue while the single navigation POST is pending', await page.evaluate(() => window.__immersiveFixture.inputReplies === 0 && window.__immersiveFixture.inputs.length === 1));
  await page.waitForFunction(() => !window.__immersiveFixture.state().page_loading && window.__immersiveFixture.inputReplies === 0);
  await page.waitForFunction(() => !document.querySelector('.connection-dot.loading'));
  check('post-navigation evidence wait is not page loading and cannot unlock input', await page.locator('.native-page.readonly').count() === 1);
  await page.waitForFunction(() => window.__immersiveFixture.inputReplies === 1); await fresh(page);
  check('late POST state cannot restore an older URL or replay navigation', await address(page).inputValue() === 'https://next.invalid/slow' && await page.evaluate(() => window.__immersiveFixture.inputs.length) === 1);

  // The pending POST observes an old document, finishes after those images,
  // then the post-action state request is held. Even unversioned inputs must
  // stay disabled during that gap; an old image load cannot re-arm them.
  await page.evaluate(() => { const f = window.__immersiveFixture; f.navigationPrecommitDelay = 0; f.navigationDelay = 0; f.inputDelay = 1100; f.frameDelay = 0; f.staleInputReply = true; });
  await address(page).fill('https://next.invalid/final-fence'); await address(page).press('Enter');
  await page.waitForFunction(() => window.__immersiveFixture.inputs.length === 2);
  await page.waitForTimeout(550);
  await page.evaluate(() => { window.__immersiveFixture.stateDelay = 1600; });
  await page.waitForFunction(() => window.__immersiveFixture.inputReplies === 2);
  await image(page).dispatchEvent('load');
  const fenceBefore = await page.evaluate(() => window.__immersiveFixture.inputs.length);
  await page.locator('.native-page').focus(); await page.keyboard.press('Enter');
  await image(page).hover(); await page.mouse.wheel(0, 40);
  await page.locator('.native-text textarea').fill('Do not submit this while awaiting fresh state');
  check('POST completion and a late old img.load cannot unlock keyboard text or scroll', await page.locator('.native-page.readonly').count() === 1 && await page.getByRole('button', { name: '发送文字', exact: true }).isDisabled() && await page.evaluate(() => window.__immersiveFixture.inputs.length) === fenceBefore);
  await page.evaluate(() => { const f = window.__immersiveFixture; f.stateDelay = 0; f.staleInputReply = false; });
  await fresh(page);

  await page.evaluate(() => { const f = window.__immersiveFixture; f.frameDelay = 1300; f.patch({ version: 'changed-dom-within-same-document' }); });
  await refresh(page);
  await page.waitForTimeout(100);
  await image(page).dispatchEvent('load');
  check('new state version fences a late old image load even within the same top document', await page.locator('.native-page.readonly').count() === 1 && await page.getByRole('button', { name: '发送文字', exact: true }).isDisabled());
  await page.evaluate(() => { window.__immersiveFixture.frameDelay = 0; });
  await fresh(page);
  const currentFrame = await image(page).getAttribute('src');
  await page.evaluate(() => { const f = window.__immersiveFixture; f.frameIdentity = '9:t9:9'; }); await refresh(page);
  await page.locator('.native-frame-status').filter({ hasText: '画面更新中' }).waitFor();
  check('mismatched frame identity is discarded without connection failure', await image(page).getAttribute('src') === currentFrame && await page.locator('.native-page.readonly').count() === 1 && await page.locator('.native-error').count() === 0);
  await page.evaluate(() => { const f = window.__immersiveFixture; f.frameIdentity = ''; f.frameDelay = 1000; f.patch({ active_tab: 't2', url: 'https://tab2.invalid/', tabs: [...f.state().tabs, { id: 't2', url: 'https://tab2.invalid/', title: 'Second tab' }] }); });
  await refresh(page);
  await page.waitForFunction(() => !document.querySelector('.native-page img'));
  check('a confirmed tab change immediately hides the previous tab frame', await image(page).count() === 0);
  await image(page).waitFor(); await fresh(page);
  const tab2Frame = await image(page).getAttribute('src');

  // In-flight old-session frame is allowed to return after its signal aborts.
  const initialReplies = await page.evaluate(() => window.__immersiveFixture.frameReplies);
  const oldSessionFrames = await frames(page);
  await refresh(page);
  await page.waitForFunction(count => window.__immersiveFixture.requests.filter(req => req.path.endsWith('/browser/frame')).length > count, oldSessionFrames);
  await page.evaluate(() => { const f = window.__immersiveFixture; f.frameDelay = 2000; f.stateDelay = 1200; f.patch({ generation: 9, frame_identity: '9:t2:1', url: 'https://new-session.invalid/' }); f.switchSession('second-fixture'); });
  await page.locator('.browser-connect').waitFor();
  check('session switch removes the previous viewer and image before connecting', await image(page).count() === 0);
  await page.waitForFunction(count => window.__immersiveFixture.frameReplies > count, initialReplies);
  check('late old-session frame cannot reappear in the new viewer', await image(page).count() === 0);
  await image(page).waitFor();
  check('new session receives only its fresh image and no inherited human lease', await image(page).getAttribute('src') !== tab2Frame && await page.getByRole('button', { name: '重新接管', exact: true }).count() === 1);
  check('race suite has no client errors or escaped API requests', errors.length === 0 && await page.evaluate(() => window.__immersiveFixture.denied.length) === 0);
  await context.close();

  const blank = await setup(390, 'light', '&blank');
  await image(blank.page).waitFor();
  check('empty about:blank browser does not show indefinite loading', await loading(blank.page).count() === 0);
  await blank.page.evaluate(() => window.__immersiveFixture.switchSession(null));
  await blank.page.locator('.browser-connect').waitFor();
  check('no-session browser is idle with an address bar and no spinner', await blank.page.locator('.connect-toolbar').count() === 1 && await blank.page.locator('.connect-indicator.loading').count() === 0);
  await blank.context.close();
  await fs.writeFile(`${output}/after.json`, JSON.stringify({ kind: 'Controlled UI fixture; no deployed or user browser access', passed: true, checks }, null, 2));
} finally { for (const context of contexts) await context.close().catch(() => {}); await browser.close(); }
