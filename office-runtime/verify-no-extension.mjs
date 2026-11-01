// Real unmodified-engine rejection checks. Uses new synthetic data only.
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { createLab } from '../office-lab/server.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(root, '../..');
const seed = path.resolve(process.env.OFFICE_HISTORY_SEED || path.join(repo, '.localagent/checks/onlyoffice-prototype-20261012'));
const dataRoot = path.resolve(process.env.OFFICE_NEGATIVE_DATA || path.join(repo, `.localagent/checks/onlyoffice-no-extension-${crypto.randomUUID()}`));
await fs.mkdir(path.join(dataRoot, 'fixtures'), { recursive: true });
await fs.mkdir(path.join(dataRoot, 'vendor'), { recursive: true });
const fixture = JSON.parse(await fs.readFile(path.join(seed, 'fixtures/manifest.json'), 'utf8'));
if (fixture.synthetic !== true) throw new Error('This validation accepts only synthetic fixtures.');
for (const name of ['manifest.json', ...Object.values(fixture.files).map((file) => file.name)]) {
  if (path.basename(name) !== name) throw new Error('Unexpected fixture path.');
  await fs.copyFile(path.join(seed, 'fixtures', name), path.join(dataRoot, 'fixtures', name));
}
await fs.copyFile(path.join(root, 'bridge/sdk.js'), path.join(dataRoot, 'vendor/plugins.js'));
const secret = (await fs.readFile(path.join(seed, 'private/docserver-jwt'), 'utf8')).trim();
const engineURL = process.env.OFFICE_ENGINE_ORIGINAL_URL || 'http://127.0.0.1:19894';
const lab = await createLab({ dataRoot, port: Number(process.env.OFFICE_NEGATIVE_PORT || 19890), engineURL, secret });
const pw = await import(process.env.OFFICE_LAB_PLAYWRIGHT || '/Users/wangy/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
const browser = await pw.chromium.launch({ executablePath: process.env.OFFICE_LAB_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const page = await browser.newPage({ viewport: { width: 1540, height: 1050 }, serviceWorkers: 'allow' });
const report = { suite: 'real-unmodified-engine-fail-closed', syntheticOnly: true, completed: false, engine: engineURL, checks: [] };
const sourceNames = ['bridge/plugin.js', 'engine-extension.js', 'engine-manifest.json', 'verify-no-extension.mjs'];
async function sources() { return Object.fromEntries(await Promise.all(sourceNames.map(async (name) => [name, crypto.createHash('sha256').update(await fs.readFile(path.join(root, name))).digest('hex')]))); }
async function api(route, body) {
  const response = await fetch(new URL(route, lab.url), { method: body ? 'POST' : 'GET', headers: {
    Authorization: `Bearer ${lab.adminToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(45000) });
  return { status: response.status, data: await response.json() };
}
async function connected(doc) {
  const deadline = Date.now() + 45000;
  while (Date.now() < deadline) {
    const active = (await api(`/api/documents/${doc}/status`)).data.activeSessionId;
    if (active) {
      const description = (await api(`/api/sessions/${active}/describe`)).data;
      if (description.capabilities) return { id: active, capabilities: description.capabilities };
    }
    await page.waitForTimeout(150);
  }
  throw new Error('The real unmodified editor plugin did not advertise its capability boundary.');
}
function check(name, passed) { report.checks.push({ name, passed: !!passed }); if (!passed) throw new Error(name); }
try {
  report.sourcesBefore = await sources();
  await page.goto(lab.url);
  for (const doc of ['word', 'excel']) {
    if (doc === 'excel') await page.locator('[data-document="excel"]').click();
    const observed = await connected(doc);
    check(`${doc}: unmodified engine advertises no native history or read`, observed.capabilities.read === false && observed.capabilities.nativeUndo === false &&
      observed.capabilities.nativeRedo === false && observed.capabilities.historyWithoutRead === false && observed.capabilities.engineExtension?.detected === false);
    const operations = doc === 'word' ? [
      ['word.read', {}], ['word.replace', { search: fixture.word.original, replacement: fixture.word.edited, expectedText: '', expectedCount: 1 }]
    ] : [
      ['sheet.read', { sheet: '销售', range: 'C2' }], ['sheet.apply', { sheet: '销售', range: 'C2', expectedInputs: [[3]], inputs: [[5]] }]
    ];
    operations.push(['history.undo', {}], ['history.redo', {}]);
    for (const [operation, args] of operations) {
      const receipt = await api(`/api/sessions/${observed.id}/commands`, { operationId: crypto.randomUUID(), operation, args });
      check(`${doc}: ${operation} fails closed without an extension`, receipt.status === 409 && receipt.data.ok === false && receipt.data.error?.code === 'ENGINE_EXTENSION_REQUIRED');
    }
  }
  report.sourcesAfter = await sources();
  check('Sources stayed fixed during the real unmodified-engine run', JSON.stringify(report.sourcesBefore) === JSON.stringify(report.sourcesAfter));
  report.completed = true;
} catch (failure) {
  report.failure = failure.message;
  process.exitCode = 1;
} finally {
  await fs.writeFile(path.join(dataRoot, 'verification.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ completed: report.completed, passed: report.checks.filter((item) => item.passed).length, checks: report.checks.length, dataRoot, failure: report.failure }));
  await browser.close(); await lab.close();
}
