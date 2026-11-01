import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
import { startLayoutProbe, finishLayoutProbe } from '../../../browser-runtime/lab/browser-layout-probe.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5189';
const out = process.argv[3] || '/tmp/pogo-browser-layout-stability';
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
const report = { scope: 'Real AgentView component and live protocol with synthetic states and pixels only', passed: false, checks: [], cases: [] };
const check = (name, pass) => { assert.ok(pass, name); report.checks.push(name); console.log('PASS ' + name); };
let current;
try {
  for (const immersive of [false, true]) for (const actor of ['agent', 'human']) {
    const context = await browser.newContext({ viewport: { width: 1900, height: 1050 } });
    const page = await context.newPage(); current = page;
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    await page.route(url => url.pathname.startsWith('/api/') || url.origin !== new URL(base).origin, r => r.abort());
    await page.goto(`${base}/dev/browser-sheets.html`);
    const pane = page.locator('.native-browser:visible');
    await pane.locator('.native-page img').waitFor();
    if (immersive) { await page.locator('.immersion-toggle').click(); await page.locator('.shell.browser-immersive').waitFor(); await page.locator('.immersive-chat-head > button').first().click(); }
    if (actor === 'human') { await pane.getByRole('button', { name: /接管此页|停止 Agent 并接管/ }).click(); await pane.locator('.native-page:not(.readonly)').waitFor(); }
    await page.waitForTimeout(900);
    const label = `${immersive ? 'immersive' : 'split'}/${actor}`;
    const beforeViewport = await page.evaluate(() => window.__sheetsFixture.liveViewports.length);
    await startLayoutProbe(page);
    const action = { seq: 1, id: 'layout-click', actor, action: 'click', phase: 'started', label: 'Clicking target', tab_id: 't1', version: '1:t1:1', started_at: Date.now(), updated_at: Date.now() };
    await page.evaluate(action => window.__sheetsFixture.patchState({ busy: true, activity: { last_action: action, recent_actions: [action] } }), action);
    await pane.locator('.native-activity').waitFor(); await page.waitForTimeout(350);
    await page.evaluate(action => window.__sheetsFixture.patchState({ busy: false, activity: { last_action: { ...action, phase: 'succeeded' }, recent_actions: [] } }), action);
    await pane.locator('.native-activity').waitFor({ state: 'hidden' }); await page.waitForTimeout(650);
    const result = await finishLayoutProbe(page); report.cases.push({ name: label, ...result });
    check(`${label}: busy and activity completion keep canvas and drawn pixels stable`, result.status_samples > 5 && result.max_delta <= .5);
    check(`${label}: status never publishes a remote viewport change`, await page.evaluate(n => window.__sheetsFixture.liveViewports.length === n, beforeViewport));
    await startLayoutProbe(page);
    await page.evaluate(action => window.__sheetsFixture.patchState({ reset: true, reset_reason: 'Fixture recovered safely', activity: { last_action: { ...action, phase: 'failed' }, recent_actions: [] } }), action);
    await pane.locator('.native-reset').waitFor(); await pane.locator('.native-activity.failed').waitFor(); await page.waitForTimeout(350);
    const notices = await finishLayoutProbe(page); report.cases.push({ name: label + '/notices', ...notices });
    check(`${label}: reset and failure notice do not resize the page`, notices.max_delta <= .5);
    check(`${label}: visible status text cannot intercept page pointer input`, await pane.locator('.native-statusbar').evaluate(el => {
      const r = el.getBoundingClientRect(), p = document.elementFromPoint(r.x + Math.min(r.width / 2, 40), r.y + Math.min(r.height / 2, 10));
      return r.width > 0 && r.height > 0 && !!el.textContent.trim() && !el.contains(p);
    }));
    await startLayoutProbe(page);
    await page.evaluate(() => window.__sheetsFixture.patchState({ dialog: { type: 'prompt', message: 'A readable prompt', default_value: '' } }));
    await pane.locator('.native-dialog').waitFor(); await page.waitForTimeout(350);
    const dialog = await finishLayoutProbe(page); report.cases.push({ name: label + '/dialog', ...dialog });
    check(`${label}: dialog stays readable without changing browser geometry`, dialog.max_delta <= .5 && await pane.locator('.native-dialog').evaluate(el => { const r = el.getBoundingClientRect(), p = document.elementFromPoint(r.x + 12, r.y + 12); return r.width > 150 && r.height > 50 && el.contains(p); }));
    await page.evaluate(() => window.__sheetsFixture.patchState({ dialog: undefined, reset: false, activity: undefined }));
    await pane.locator('.native-dialog').waitFor({ state: 'hidden' });
    // A real reset intentionally retires the old lease. Reclaim explicitly
    // before checking legitimate resize; never bypass the ownership fence.
    if (actor === 'human') { await pane.getByRole('button', { name: /重新接管|Reclaim/ }).click(); await pane.locator('.native-page:not(.readonly)').waitFor(); }
    const beforeResize = await page.evaluate(() => window.__sheetsFixture.liveViewports.length);
    if (immersive) await page.setViewportSize({ width: 2100, height: 1100 });
    else await page.locator('.immersion-toggle').click();
    await page.waitForFunction(n => window.__sheetsFixture.liveViewports.length > n, beforeResize);
    check(`${label}: real layout changes still update remote viewport`, true);
    check(`${label}: no browser errors or escaped API calls`, errors.length === 0 && await page.evaluate(() => window.__sheetsFixture.denied.length === 0));
    await page.screenshot({ path: `${out}/${label.replace('/', '-')}.png` }); await context.close();
  }
  report.passed = true;
} catch (error) { report.error = error.message; await current?.screenshot({ path: `${out}/failure.png` }).catch(() => {}); }
finally { await browser.close(); await fs.writeFile(`${out}/summary.json`, JSON.stringify(report, null, 2)); }
console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, error: report.error, out }));
if (!report.passed) process.exitCode = 1;
