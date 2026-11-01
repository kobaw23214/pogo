// Actual NativeBrowserPane and AgentView, synthetic API/video only.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5198', output = process.argv[3] || '/tmp/pogo-human-adoption-qa';
await fs.mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true }), checks = [], errors = [];
const check = (label, passed) => { assert.ok(passed, label); checks.push(label); console.log('PASS ' + label); };
try {
  for (const race of ['', 'agent', 'human', 'error']) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await context.newPage();
    try {
      page.on('pageerror', error => errors.push(error.message));
      await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
      await page.goto(`${base}/dev/browser-sheets.html?theme=dark`);
      const pane = () => page.locator('.native-browser:visible'), owned = () => pane().getByRole('button', { name: '交还此页', exact: true });
      await pane().waitFor();
      const target = await page.evaluate(race => { const f = window.__sheetsFixture; f.adoptionRace = race; return f.addUnownedPage(); }, race);
      await page.locator(`[data-browser-tab-id=${target}]`).waitFor();
      check(`${race || 'success'}: hidden unknown human Page is never initialized by discovery alone`, await page.evaluate(() => window.__sheetsFixture.controls.length === 0));
      const before = await page.evaluate(tab => window.__sheetsFixture.state('sheets-a', tab), target);
      await page.locator(`[data-browser-tab-id=${target}]`).click();
      await page.waitForFunction(() => window.__sheetsFixture.controls.length === 1);
      check(`${race || 'success'}: initialization binds visible Page and exact generation/epoch without force`, await page.evaluate(tab => {
        const request = window.__sheetsFixture.controls[0];
        return request.tab_id === tab && request.mode === 'human' && request.adopt_if_unowned === true && request.generation === 1 && request.control_epoch === 0 && request.force === undefined;
      }, target));
      if (!race) {
        await owned().waitFor();
        const address = pane().getByRole('textbox', { name: '原站地址', exact: true });
        check('successful initialization makes the existing native Sheet immediately human-owned', await address.getAttribute('readonly') === null && await page.evaluate(tab => {
          const c = window.__sheetsFixture.context();
          return c.pages.find(page => page.id === tab).control === 'human' && c.pages.find(page => page.id === tab).epoch === 1 && c.pages.find(page => page.id === 't1').control === 'agent';
        }, target));
        await address.fill('https://native-new.invalid/typed'); await address.press('Enter');
        await page.waitForFunction(() => window.__sheetsFixture.inputs.length === 1);
        check('initialized Page uses its returned exact human lease for native input', await page.evaluate(tab => window.__sheetsFixture.inputs[0].target_tab === tab && window.__sheetsFixture.inputs[0].lease_token.endsWith('-' + tab), target));
        await page.evaluate(state => window.__sheetsFixture.lateState(state), before);
        await page.waitForTimeout(150);
        check('stale epoch-zero state does not initialize again or erase the established lease', await owned().count() === 1 && await page.evaluate(() => window.__sheetsFixture.controls.length === 1));
        for (const [control, epoch] of [['agent', 0], ['human', 1]]) {
          const next = await page.evaluate(({ control, epoch }) => window.__sheetsFixture.addUnownedPage(control, epoch), { control, epoch });
          await page.locator(`[data-browser-tab-id=${next}]`).waitFor(); await page.locator(`[data-browser-tab-id=${next}]`).click();
          await pane().getByRole('button', { name: control === 'agent' ? '接管此页' : '重新接管此页', exact: true }).waitFor();
          check(`observing ${control} epoch ${epoch} never silently changes its existing owner`, await page.evaluate(() => window.__sheetsFixture.controls.length === 1));
        }
      } else {
        await pane().getByRole('button', { name: race === 'agent' ? '接管此页' : '重新接管此页', exact: true }).waitFor();
        await pane().getByRole('button', { name: '刷新画面', exact: true }).click();
        await page.waitForTimeout(350);
        check(`${race}: changed or uncertain owner is observed without force or automatic retry`, await owned().count() === 0 && await page.evaluate(() => window.__sheetsFixture.controls.length === 1 && window.__sheetsFixture.inputs.length === 0));
      }
      await page.screenshot({ path: `${output}/${race || 'success'}.png` });
    } finally { await context.close(); }
  }
  check('actual UI initialization checks produce no unhandled client errors', errors.length === 0);
  await fs.writeFile(`${output}/summary.json`, JSON.stringify({ passed: true, scope: 'real AgentView/NativeBrowserPane, synthetic API and pixels, no Docker mutation', checks, errors }, null, 2));
} finally { await browser.close(); }
