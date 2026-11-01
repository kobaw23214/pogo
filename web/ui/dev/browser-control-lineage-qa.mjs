// Real AgentView and native Pane against synthetic API/pixels. This checks UI
// authority/focus only; browser popup semantics are verified by runtime tests.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5198';
const out = process.argv[3] || '/tmp/pogo-control-lineage-qa';
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
const checks = [], errors = [];
let page;
const check = (name, passed) => { assert.ok(passed, name); checks.push(name); console.log(`PASS ${name}`); };
try {
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.on('pageerror', error => errors.push(error.message));
  await page.route(url => url.pathname.startsWith('/api/'), route => route.abort());
  await page.goto(`${base}/dev/browser-sheets.html?theme=dark`);
  const pane = () => page.locator('.native-browser:visible');
  const address = () => pane().getByRole('textbox', { name: '原站地址', exact: true });
  const owned = () => pane().getByRole('button', { name: '交还此页', exact: true });
  const selected = () => page.locator('[data-browser-tab-id][aria-selected=true]');
  const select = async id => { await page.locator(`[data-browser-tab-id=${id}]`).click(); await page.waitForFunction(tab => document.querySelector(`[data-browser-tab-id=${tab}][aria-selected=true]`), id); };
  await pane().waitFor();
  await page.getByRole('button', { name: '添加工作表', exact: true }).click();
  await page.getByRole('menuitem', { name: '浏览器', exact: true }).click();
  await page.locator('[data-browser-tab-id=t2][aria-selected=true]').waitFor();
  await owned().waitFor();
  check('explicit user + creates one human Sheet without takeover or Agent focus', await page.evaluate(() => {
    const f = window.__sheetsFixture, c = f.context();
    return c.pages.length === 2 && c.active === 't1' && c.pages.find(p => p.id === 't2').control === 'human' && f.controls.length === 0 && f.inputs.length === 0;
  }));
  check('new human Page has an editable native address field', await address().getAttribute('readonly') === null);
  await address().fill('https://reading.invalid/'); await address().press('Enter');
  await page.waitForFunction(() => window.__sheetsFixture.inputs.length === 1);
  check('new human Page navigation sends its create lease once', await page.evaluate(() => {
    const f = window.__sheetsFixture, request = f.inputs[0];
    return request.target_tab === 't2' && request.lease_token === `lease-sheets-a-${f.context().ref}-t2` && f.controls.length === 0;
  }));
  await page.evaluate(() => window.__sheetsFixture.addHumanPopup('t2'));
  await page.locator('[data-browser-tab-id=t3]').waitFor();
  check('human popup adds a Sheet without changing the human current selection', await selected().getAttribute('data-browser-tab-id') === 't2');
  await select('t3'); await owned().waitFor();
  check('human popup is immediately owned without another takeover request', await address().getAttribute('readonly') === null && await page.evaluate(() => window.__sheetsFixture.controls.length === 0));
  await address().fill('https://auth.invalid/continue'); await address().press('Enter');
  await page.waitForFunction(() => window.__sheetsFixture.inputs.some(input => input.type === 'navigate' && input.target_tab === 't3'));
  await fs.writeFile(`${out}/synthetic-inputs.json`, JSON.stringify(await page.evaluate(() => ({ inputs: window.__sheetsFixture.inputs, denied: window.__sheetsFixture.denied })), null, 2));
  check('popup input uses exactly its proven opener lease', await page.evaluate(() => {
    const f = window.__sheetsFixture, request = f.inputs.at(-1);
    return request.target_tab === 't3' && request.lease_token === f.inputs[0].lease_token && f.denied.length === 0;
  }));
  await page.evaluate(() => window.__sheetsFixture.addHumanPopup('t3'));
  await page.locator('[data-browser-tab-id=t4]').waitFor(); await select('t4'); await owned().waitFor();
  check('nested human popup inherits input authority through the same lineage', await address().getAttribute('readonly') === null && await page.evaluate(() => window.__sheetsFixture.controls.length === 0));
  const before = await page.evaluate(() => window.__sheetsFixture.state('sheets-a', 't3'));
  await page.evaluate(state => {
    const f = window.__sheetsFixture;
    f.emit({ type: 'run_start', seq: 10, run_id: 'claim-run', ts: Date.now() });
    f.emit({ type: 'tool_start', seq: 11, name: 'browser', call_id: 'claim-popup', args: { action: 'tab', tab_action: 'claim', target: 't3', context_ref: state.context_ref, control_epoch: state.control_epoch }, ts: Date.now() });
  }, before);
  check('claim start alone never selects or changes ownership', await selected().getAttribute('data-browser-tab-id') === 't4');
  await page.evaluate(() => {
    const f = window.__sheetsFixture, state = f.claim('t3');
    f.emit({ type: 'tool_end', seq: 12, name: 'browser', call_id: 'claim-popup', is_error: false, details: { ...state, ok: true, action: 'tab' }, ts: Date.now() });
  });
  await page.locator('[data-browser-tab-id=t3][aria-selected=true]').waitFor();
  await pane().getByRole('button', { name: '接管此页', exact: true }).waitFor();
  check('successful explicit Agent claim follows only the claimed Sheet and revokes its local human input', await address().getAttribute('readonly') !== null && await page.evaluate(() => {
    const c = window.__sheetsFixture.context();
    return c.pages.find(p => p.id === 't3').control === 'agent' && c.pages.find(p => p.id === 't2').control === 'human' && c.pages.find(p => p.id === 't4').control === 'human';
  }));
  await page.evaluate(state => window.__sheetsFixture.lateState(state), before);
  await page.waitForTimeout(100);
  check('late human observations cannot resurrect a lease after Agent claim', await address().getAttribute('readonly') !== null && await owned().count() === 0);
  await select('t2'); await owned().waitFor();
  await page.waitForFunction(() => document.querySelector('.native-browser:has(button[aria-label="交还此页"]) input[aria-label="原站地址"]')?.getAttribute('readonly') === null);
  check('claim of a popup leaves the opener human-owned and usable', await address().getAttribute('readonly') === null);
  const next = await page.evaluate(() => window.__sheetsFixture.state('sheets-a', 't4'));
  await page.evaluate(state => window.__sheetsFixture.emit({ type: 'tool_start', seq: 13, name: 'browser', call_id: 'claim-nested', args: { action: 'tab', tab_action: 'claim', target: 't4', context_ref: state.context_ref, control_epoch: state.control_epoch }, ts: Date.now() }), next);
  await select('t3'); await select('t2');
  await page.evaluate(() => {
    const f = window.__sheetsFixture, state = f.claim('t4');
    f.emit({ type: 'tool_end', seq: 14, name: 'browser', call_id: 'claim-nested', is_error: false, details: { ...state, ok: true, action: 'tab' }, ts: Date.now() });
  });
  await page.waitForTimeout(600);
  check('manual Sheet selection during an Agent claim cancels automatic follow', await selected().getAttribute('data-browser-tab-id') === 't2' && await owned().count() === 1);
  await page.evaluate(() => { const f = window.__sheetsFixture; f.addHumanPopup('t2', false); f.claim('t2'); });
  await page.locator('[data-browser-tab-id=t5]').waitFor();
  await select('t5'); await owned().waitFor();
  check('a human child first discovered after its opener claim remains immediately human-owned', await address().getAttribute('readonly') === null && await page.evaluate(() => {
    const c = window.__sheetsFixture.context();
    return c.pages.find(p => p.id === 't2').control === 'agent' && c.pages.find(p => p.id === 't5').control === 'human';
  }));
  await address().fill('https://auth.invalid/late-child'); await address().press('Enter');
  await page.waitForFunction(() => window.__sheetsFixture.inputs.some(input => input.type === 'navigate' && input.target_tab === 't5'));
  check('delayed popup discovery keeps its original opener token without reauthorizing the Agent-owned opener', await page.evaluate(() => {
    const f = window.__sheetsFixture;
    return f.inputs.at(-1).target_tab === 't5' && f.inputs.at(-1).lease_token === f.inputs[0].lease_token && f.controls.length === 0;
  }));
  await page.evaluate(() => { const f = window.__sheetsFixture; f.addHumanPopup('t5', false); f.closePage('t5'); });
  await page.locator('[data-browser-tab-id=t6]').waitFor();
  await select('t6'); await owned().waitFor();
  check('a human popup remains immediately owned when its opener closes before discovery', await address().getAttribute('readonly') === null && await page.evaluate(() => {
    const c = window.__sheetsFixture.context();
    return !c.pages.some(p => p.id === 't5') && c.pages.find(p => p.id === 't6').control === 'human';
  }));
  check('credentials stay window-local and no real API or model request is made', await page.evaluate(() => {
    const f = window.__sheetsFixture;
    return !sessionStorage.getItem('pogo:browser-new-operations:v1')?.includes('lease') && !localStorage.getItem('pogo:hub-tabs')?.includes('lease')
      && !JSON.stringify(f.state()).includes('lease') && f.denied.length === 0 && f.requests.every(r => !r.path.endsWith('/messages'));
  }));
  check('real AgentView and Pane complete without client runtime errors', errors.length === 0);
  await page.screenshot({ path: `${out}/human-popup-control.png`, fullPage: true });
  await fs.writeFile(`${out}/summary.json`, JSON.stringify({ scope: 'isolated real AgentView, synthetic API and live pixels, no deployed session', passed: checks.length, checks, errors }, null, 2));
} catch (error) {
  if (page) {
    await fs.writeFile(`${out}/failure-synthetic-state.json`, JSON.stringify(await page.evaluate(() => ({ context: window.__sheetsFixture.context(), inputs: window.__sheetsFixture.inputs, denied: window.__sheetsFixture.denied, stateRequests: window.__sheetsFixture.requests.slice(-20), address: [...document.querySelectorAll('.native-browser input')].map(input => ({ value: input.value, readonly: input.readOnly })), paneText: [...document.querySelectorAll('.native-browser')].map(pane => pane.innerText.slice(0, 400)) })), null, 2));
    await page.screenshot({ path: `${out}/failure.png`, fullPage: true });
  }
  throw error;
} finally { await browser.close(); }
