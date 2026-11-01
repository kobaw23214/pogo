// Verify the real human-facing hostname without DNS overrides or touching production.
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const privateDir = process.env.LAB_PRIVATE_DIR || path.join(root, '.localagent/browser-auth-lab-private');
const output = process.argv[2];
if (!output) throw new Error('Pass a new report JSON file path');
const base = 'http://pogo-auth-lab.localhost:19778';
const password = (await readFile(path.join(privateDir, 'admin-password.txt'), 'utf8')).trim();
const report = { kind: 'candidate-hostname-cookie-isolation', url: base + '/manage', started_at: new Date().toISOString(), checks: [], passed: false };
const browser = await chromium.launch({ headless: true });
const check = (name, condition) => { assert.ok(condition, name); report.checks.push(name); };
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const sentinel = 'synthetic-cookie-isolation-sentinel';
  await context.addCookies([{ name: 'pi_session', value: sentinel, url: 'http://127.0.0.1:19777/', httpOnly: true, sameSite: 'Strict' }]);
  const page = await context.newPage();
  const response = await page.goto(report.url);
  check('Chromium resolves the localhost subdomain without host overrides', response.ok());
  if (!await page.locator('#studio-login-username').isVisible()) await page.getByRole('button', { name: '进入工作空间' }).first().click();
  await page.locator('#studio-login-username').fill('admin');
  await page.locator('#studio-login-password').fill(password);
  const loginResponse = page.waitForResponse(response => new URL(response.url()).pathname === '/api/login');
  await page.locator('.login-submit').click();
  check('Real Vue login succeeds on the isolated hostname', (await loginResponse).status() === 200);
  const status = await page.evaluate(async () => (await fetch('/api/sessions')).status);
  check('Same-origin authenticated API works on the isolated hostname', status === 200);
  const original = (await context.cookies('http://127.0.0.1:19777/')).find(cookie => cookie.name === 'pi_session');
  const candidate = (await context.cookies(base)).find(cookie => cookie.name === 'pi_session');
  check('Candidate login keeps the same-context production-host sentinel unchanged', original?.value === sentinel);
  check('Candidate cookie is scoped to its independent hostname', candidate?.domain === 'pogo-auth-lab.localhost');
  report.passed = true;
} catch (error) {
  report.error = String(error.message).replaceAll(password, '[REDACTED]');
  process.exitCode = 1;
} finally {
  report.finished_at = new Date().toISOString();
  await writeFile(output, JSON.stringify(report, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  await browser.close();
  console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, url: report.url, output }));
}
