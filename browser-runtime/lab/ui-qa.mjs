// Real Vue -> owned Go session -> remote Chromium smoke; no model prompts or API mocks.
import assert from 'node:assert/strict';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const privateDir = process.env.LAB_PRIVATE_DIR || path.join(root, '.localagent/browser-auth-lab-private');
const base = process.env.LAB_APP_URL || 'http://127.0.0.1:19778';
const output = process.argv[2];
if (!output) throw new Error('Pass a new output directory for the real UI screenshots and report');
if (!['127.0.0.1', 'localhost'].includes(new URL(base).hostname)) throw new Error('UI QA is restricted to a loopback candidate');
await mkdir(output, { mode: 0o700 });
const report = { kind: 'candidate-real-vue-ui-smoke', app: base, started_at: new Date().toISOString(), paid_model_calls: 0, login_reuse_test: 'UI wiring only; actual automatic authenticated API reads are verified by the independent model E2E', checks: [], screenshots: [], passed: false };
const password = (await readFile(path.join(privateDir, 'admin-password.txt'), 'utf8')).trim();
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1680, height: 1050 }, locale: 'zh-CN', colorScheme: 'light' });
const page = await context.newPage();
const posts = [];
const errors = [];
page.on('pageerror', error => errors.push(error.message.replaceAll(password, '[REDACTED]')));
page.on('request', request => {
  const url = new URL(request.url());
  if (url.origin === base && ['POST', 'PATCH', 'DELETE'].includes(request.method())) posts.push({ path: url.pathname, method: request.method() });
});
const check = (name, condition) => { assert.ok(condition, name); report.checks.push(name); console.log(`PASS ${name}`); };
async function api(route, data) {
  const response = data === undefined ? await context.request.get(base + route) : await context.request.post(base + route, { data, headers: { Origin: base } });
  assert.ok(response.ok(), `Candidate API ${route} must succeed (${response.status()})`);
  return response.json();
}
async function screenshot(name) { await page.screenshot({ path: path.join(output, name), fullPage: true }); report.screenshots.push(name); }
const inputCount = () => posts.filter(item => item.path.endsWith('/browser/input')).length;
const messageCount = () => posts.filter(item => item.path.endsWith('/messages')).length;
async function until(test, timeout = 20000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) { if (await test()) return; await page.waitForTimeout(100); }
  throw new Error('UI condition timed out; no interaction was replayed');
}
let session;
try {
  await api('/api/login', { username: 'admin', password });
  const agent = await api('/api/manage/agents', { name: `浏览器界面验收 ${new Date().toISOString().slice(11, 19)}`, description: '仅使用合成数据的浏览器界面验收，不发送模型请求。', instructions: '这是独立 UI 验收 Agent。', model: 'glm-5.3', models: ['glm-5.3'], skills: [], mcp_servers: [], status: 'ready' });
  session = await api(`/api/apps/${agent.id}/sessions`, {});
  report.agent_id = agent.id; report.session_id = session.id;
  report.url = `${base}/manage/agents/${agent.id}/chat?c=${session.id}`;
  await page.goto(report.url);
  await page.locator('.input-card textarea').waitFor({ state: 'visible', timeout: 30000 });
  check('Opened a real Studio-bound owned conversation', page.url().includes(agent.id) && page.url().includes(session.id));
  await page.locator('.rail-btn[title^="面板"]').click();
  await page.locator('.hub-empty .tile').filter({ hasText: '浏览器' }).click();
  const native = page.locator('.native-browser');
  await native.waitFor({ state: 'visible' });
  await native.locator('img').waitFor({ state: 'visible', timeout: 30000 });
  check('Native browser connected and rendered a real frame', await native.locator('img').evaluate(image => image.complete && image.naturalWidth > 0));
  await native.getByRole('button', { name: '停止 Agent 并接管', exact: true }).click();
  await native.getByRole('button', { name: '交还 Agent', exact: true }).waitFor();
  check('Vue takeover button acquired human control', (await api(`/api/sessions/${session.id}/browser`)).control === 'human');
  const url = `http://mock-platform:5190/?scenario=cookie&run=vue-ui-${Date.now()}`;
  await native.getByRole('textbox', { name: '原站地址', exact: true }).fill(url);
  await native.getByRole('textbox', { name: '原站地址', exact: true }).press('Enter');
  await until(async () => (await api(`/api/sessions/${session.id}/browser`)).url === url);
  await until(async () => await native.getByRole('button', { name: '选取页面元素', exact: true }).isEnabled());
  check('Real address-bar navigation reached the isolated mock platform', true);
  await screenshot('01-native-browser.png');

  const composer = page.locator('.input-card textarea');
  const draft = '请分析我选择的这个元素，先保留这段草稿。';
  await composer.fill(draft);
  check('Browser toolbar has no retired overflow menu', await native.getByRole('button', { name: '浏览器菜单', exact: true }).count() === 0 && await native.locator('.native-menu-trigger, .native-more-menu').count() === 0);
  check('Browser chrome has no operation-help entry', await native.getByText('操作说明', { exact: true }).count() === 0 && await native.locator('.native-help').count() === 0);
  check('No retired account authorization dialog is mounted', await page.locator('.native-account-dialog').count() === 0);
  await screenshot('02-browser-toolbar-without-menu.png');
  const frame = native.locator('.native-page img');
  check('Browser chrome does not call the retired authorization endpoint', !posts.some(item => item.path.endsWith('/browser/auth')));

  await until(async () => await native.getByRole('button', { name: '选取页面元素', exact: true }).isEnabled());
  const beforePickInput = inputCount(), beforePickMessages = messageCount();
  await native.getByRole('button', { name: '选取页面元素', exact: true }).click();
  const pixels = await frame.evaluate(image => ({ width: image.naturalWidth, height: image.naturalHeight }));
  const box = await frame.boundingBox();
  const scale = Math.min(box.width / pixels.width, box.height / pixels.height);
  const contentLeft = box.x + (box.width - pixels.width * scale) / 2;
  const contentTop = box.y + (box.height - pixels.height * scale) / 2;
  // Hit the mock's clearly visible Test Console button. A real click would open
  // its modal; picking must only create a conversation reference.
  await page.mouse.move(contentLeft + (pixels.width - 80) * scale, contentTop + 19 * scale);
  await page.waitForTimeout(250);
  await page.mouse.click(contentLeft + (pixels.width - 80) * scale, contentTop + 19 * scale);
  await page.locator('.element-chips .echip').waitFor({ state: 'visible', timeout: 15000 });
  check('Small hand inserts a real page element chip', (await page.locator('.element-chips .echip').innerText()).trim().length > 0);
  check('Element picking preserves the existing composer draft', await composer.inputValue() === draft);
  check('Element picking does not forward a page click', inputCount() === beforePickInput);
  check('Browser chrome and element picking never auto-send a prompt', messageCount() === beforePickMessages && messageCount() === 0);
  check('No manual authorization request occurred during browser use', !posts.some(item => item.path.endsWith('/browser/auth')));
  check('Vue smoke generated no uncaught page errors', errors.length === 0);
  await native.getByRole('button', { name: '交还 Agent', exact: true }).click();
  await until(async () => (await api(`/api/sessions/${session.id}/browser`)).control === 'agent');
  await native.getByRole('button', { name: '停止 Agent 并接管', exact: true }).waitFor();
  check('Vue return-control button restores agent mode without starting a run', messageCount() === 0);
  await screenshot('03-picked-element-and-draft.png');
  await page.getByRole('button', { name: '沉浸模式', exact: true }).click();
  await page.locator('.shell.browser-immersive').waitFor();
  check('Immersive mode preserves the same element chip and composer draft', await composer.inputValue() === draft && await page.locator('.element-chips .echip').isVisible());
  await screenshot('04-immersive-picked-element.png');
  report.passed = true;
} catch (error) {
  report.error = String(error.message).replaceAll(password, '[REDACTED]');
  process.exitCode = 1;
  await screenshot('failure.png').catch(() => {});
  console.log(`FAIL ${report.error}`);
} finally {
  report.finished_at = new Date().toISOString(); report.page_errors = errors;
  report.post_counts = { browser_input: inputCount(), model_messages: messageCount(), picks: posts.filter(item => item.path.endsWith('/browser/pick')).length, account_changes: posts.filter(item => item.path.endsWith('/browser/auth')).length };
  await writeFile(path.join(output, 'summary.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 });
  await browser.close();
  console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, output, session_id: session?.id, url: report.url }));
}
