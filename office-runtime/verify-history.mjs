// Real ONLYOFFICE/Chromium validation with a fresh synthetic fixture workspace.
// Starts isolated loopback services, never controls the user's 19895 editor.
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { createLab } from '../office-lab/server.mjs';
import { createEngineProxy } from './engine-proxy.mjs';

const runtimeRoot = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(runtimeRoot, '../..');
const seed = path.resolve(process.env.OFFICE_HISTORY_SEED || path.join(repo, '.localagent/checks/onlyoffice-prototype-20261012'));
const dataRoot = path.resolve(process.env.OFFICE_HISTORY_DATA || path.join(repo, `.localagent/checks/onlyoffice-history-${crypto.randomUUID()}`));
const output = path.join(dataRoot, 'verification');
const pw = await import(process.env.OFFICE_LAB_PLAYWRIGHT || '/Users/wangy/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
await fs.mkdir(path.join(dataRoot, 'fixtures'), { recursive: true });
await fs.mkdir(path.join(dataRoot, 'vendor'), { recursive: true });
await fs.mkdir(output, { recursive: true });
const fixture = JSON.parse(await fs.readFile(path.join(seed, 'fixtures/manifest.json'), 'utf8'));
if (fixture.synthetic !== true) throw new Error('This validation accepts only synthetic fixtures.');
for (const name of ['manifest.json', ...Object.values(fixture.files).map((file) => file.name)]) {
  if (path.basename(name) !== name) throw new Error('Unexpected fixture path.');
  await fs.copyFile(path.join(seed, 'fixtures', name), path.join(dataRoot, 'fixtures', name));
}
await fs.copyFile(path.join(runtimeRoot, 'bridge/sdk.js'), path.join(dataRoot, 'vendor/plugins.js'));
const secret = (await fs.readFile(path.join(seed, 'private/docserver-jwt'), 'utf8')).trim();
const proxy = await createEngineProxy({ upstream: process.env.OFFICE_ENGINE_UPSTREAM || 'http://127.0.0.1:19894', port: Number(process.env.OFFICE_ENGINE_PROXY_PORT || 19898) });
const lab = await createLab({ dataRoot, port: Number(process.env.OFFICE_HISTORY_PORT || 19896), engineURL: proxy.state.url,
  secret, internalEngineOrigin: proxy.state.upstream });
const report = { suite: 'real-onlyoffice-native-history', syntheticOnly: true, completed: false, checks: [], engine: proxy.state,
  interaction: 'Trusted Chromium input; physical IME latency is not measured.' };
const check = (name, success, detail) => {
  report.checks.push({ name, passed: !!success, ...(detail ? { detail } : {}) });
  if (!success) throw new Error(name);
};
const sourcePaths = ['engine-extension.js', 'engine-manifest.json', 'engine-proxy.mjs', 'verify-history.mjs', 'bridge/plugin.js', 'bridge/config.json', 'bridge/index.html', 'bridge/sdk.js'];
async function sources() { return Object.fromEntries(await Promise.all(sourcePaths.map(async (name) => [name, crypto.createHash('sha256').update(await fs.readFile(path.join(runtimeRoot, name))).digest('hex')]))); }
report.sourcesBefore = await sources();
const browser = await pw.chromium.launch({ executablePath: process.env.OFFICE_LAB_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const page = await browser.newPage({ viewport: { width: 1540, height: 1050 }, serviceWorkers: 'allow' });
const errors = [];
page.on('pageerror', (error) => errors.push(error.message.slice(0, 300)));
async function api(route, body) {
  const response = await fetch(new URL(route, lab.url), { method: body ? 'POST' : 'GET',
    headers: { Authorization: `Bearer ${lab.adminToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(45000) });
  const result = await response.json();
  if (!response.ok || result.ok === false) {
    const failure = new Error(typeof result.error === 'string' ? result.error : result.error?.message || 'Current editor operation failed');
    failure.code = result.error?.code; throw failure;
  }
  return result;
}
async function session(doc) { const status = await api(`/api/documents/${doc}/status`); return status.activeSessionId; }
async function command(doc, operation, args = {}, operationId = crypto.randomUUID()) {
  return (await api(`/api/sessions/${await session(doc)}/commands`, { operationId, operation, args })).result;
}
async function staleWriteRejected(doc, operation, args) {
  try { await command(doc, operation, args); return false; }
  catch (failure) { return failure.code === 'PRECONDITION_FAILED'; }
}
async function connected(doc) {
  await page.waitForFunction(() => document.getElementById('read-document')?.disabled === false, null, { timeout: 45000 });
  const description = await api(`/api/sessions/${await session(doc)}/describe`);
  check(`${doc}: real pinned extension handshake`, description.ready && description.capabilities?.engineExtension?.detected === true &&
    description.capabilities.nativeUndo && description.capabilities.nativeRedo && description.capabilities.historyWithoutRead === true &&
    description.capabilities.readPreservesHistory && description.capabilities.readPreservesSelection);
}
function editorFrame(doc) {
  const part = doc === 'word' ? 'documenteditor' : 'spreadsheeteditor';
  const frame = page.frames().find((item) => { try { return new URL(item.url()).pathname.includes(`/apps/${part}/main/`); } catch { return false; } });
  if (!frame) throw new Error(`Real ${part} frame not found`);
  return frame;
}
async function poll(read, accept) {
  const deadline = Date.now() + 10000;
  do { const value = await read(); if (accept(value)) return value; await page.waitForTimeout(200); } while (Date.now() < deadline);
  throw new Error('The real editor did not reach the expected state.');
}
async function screenshot(name) { await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true }); }
const sales = () => command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' });
try {
  await page.goto(lab.url);
  await connected('word');
  const initial = await command('word', 'word.read');
  check('Word fresh current document snapshot', initial.text.includes(fixture.word.original) && !initial.text.includes(fixture.word.human_marker));
  const wordFrame = editorFrame('word');
  const tutorial = wordFrame.getByText('知道了', { exact: true });
  if (await tutorial.count()) await tutorial.first().click().catch(() => {});
  await wordFrame.locator('#id_main_view').click({ position: { x: 420, y: 300 } });
  await page.keyboard.press('Meta+End'); await page.keyboard.press('Enter');
  await page.keyboard.insertText(fixture.word.human_marker);
  await page.keyboard.press('Shift+Home');
  const selected = await command('word', 'word.read');
  check('Word reads a user selection without consuming it', selected.text.includes(fixture.word.human_marker) && selected.selectedText.length > 0);
  const observedAgain = await command('word', 'word.read');
  check('Word repeated reads preserve selection and content', observedAgain.selectedText === selected.selectedText && observedAgain.text === selected.text);
  await screenshot('word-selection-preserved');
  await page.keyboard.press('ArrowRight');
  const caret = await command('word', 'word.read');
  check('Word collapsed caret is read without selecting new text', caret.selectedText === '');
  await page.keyboard.insertText('_CARET_SAFE');
  const humanCaret = await command('word', 'word.read');
  check('Word read preserves the caret insertion position', humanCaret.text.includes(`${fixture.word.human_marker}_CARET_SAFE`));
  const args = { search: fixture.word.original, replacement: fixture.word.edited, expectedText: humanCaret.text, expectedCount: 1 };
  const changed = await command('word', 'word.replace', args);
  check('Word agent mutation preserves the human draft', changed.after.text.includes(fixture.word.edited) && changed.after.text.includes(fixture.word.human_marker));
  for (let i = 1; i <= 2; i++) {
    const undone = await command('word', 'history.undo');
    check(`Word cycle ${i}: native undo retains earlier human input`, undone.native && undone.canRedo && undone.snapshot.text.includes(fixture.word.original) && undone.snapshot.text.includes(fixture.word.human_marker));
    check(`Word cycle ${i}: stale write rejected before a native history action`, await staleWriteRejected('word', 'word.replace', { ...args, expectedText: initial.text }));
    const a = await command('word', 'word.read'), b = await command('word', 'word.read');
    check(`Word cycle ${i}: intervening reads are observational`, a.text === undone.snapshot.text && b.text === a.text && b.selectedText === a.selectedText);
    const redone = await command('word', 'history.redo');
    check(`Word cycle ${i}: native redo survives reads`, redone.native && redone.snapshot.text.includes(fixture.word.edited) && redone.snapshot.text.includes(fixture.word.human_marker));
  }
  await screenshot('word-redo-after-read');
  await api(`/api/sessions/${await session('word')}/save`, {});
  check('Word history result saved through the current editor', (await api('/api/documents/word/status')).saveState === 'saved');
  await page.waitForFunction(() => !document.getElementById('reopen-document')?.disabled);
  await page.locator('#reopen-document').click(); await connected('word');
  const reopenedWord = await command('word', 'word.read');
  check('Word saved history result survives reopen', reopenedWord.text.includes(fixture.word.edited) && reopenedWord.text.includes(fixture.word.human_marker));
  await page.locator('[data-document="excel"]').click(); await connected('excel');
  const excelFrame = editorFrame('excel');
  await excelFrame.locator('#ce-cell-name').fill('E2'); await excelFrame.locator('#ce-cell-name').press('Enter');
  const originalRemark = await excelFrame.locator('#ce-cell-content').inputValue();
  await excelFrame.locator('#ce-cell-content').fill(fixture.excel.human_marker); await excelFrame.locator('#ce-cell-content').press('Enter');
  await excelFrame.locator('#ce-cell-name').fill('E2'); await excelFrame.locator('#ce-cell-name').press('Enter');
  check('Excel fresh native user input is visible before any Agent read', (await excelFrame.locator('#ce-cell-content').inputValue()) === fixture.excel.human_marker);
  let freshHistoryReceipt;
  const recordReceipt = (response) => {
    if (!response.url().endsWith('/results') || response.request().method() !== 'POST') return;
    const body = response.request().postDataJSON();
    if (body?.ok === true && body?.result?.historyScope === 'editor-global') freshHistoryReceipt = body.result;
  };
  page.on('response', recordReceipt);
  await page.locator('#undo-document').click();
  await page.waitForFunction(() => !document.getElementById('undo-document')?.disabled);
  await excelFrame.locator('#ce-cell-name').fill('E2'); await excelFrame.locator('#ce-cell-name').press('Enter');
  check('Excel first UI native undo works without a prior read or invented snapshot', freshHistoryReceipt?.native === true &&
    freshHistoryReceipt.canRedo === true && freshHistoryReceipt.snapshot === null && (await excelFrame.locator('#ce-cell-content').inputValue()) === originalRemark);
  freshHistoryReceipt = null;
  await page.locator('#redo-document').click();
  await page.waitForFunction(() => !document.getElementById('redo-document')?.disabled);
  await excelFrame.locator('#ce-cell-name').fill('E2'); await excelFrame.locator('#ce-cell-name').press('Enter');
  check('Excel first UI native redo works without a prior read', freshHistoryReceipt?.native === true &&
    freshHistoryReceipt.snapshot === null && (await excelFrame.locator('#ce-cell-content').inputValue()) === fixture.excel.human_marker);
  page.off('response', recordReceipt);
  const excelInitial = await sales();
  check('Excel original formula and calculated value', String(excelInitial.inputs[0][2]).replace(/^=/, '') === fixture.excel.formulas['销售!D2'] && Number(excelInitial.values[0][2]) === 360);
  const human = await poll(sales, (value) => value.inputs[0][3] === fixture.excel.human_marker);
  check('Excel current snapshot includes unsaved human input', human.inputs[0][3] === fixture.excel.human_marker);
  await excelFrame.locator('#ce-cell-name').fill('B2:D3'); await excelFrame.locator('#ce-cell-name').press('Enter');
  const selectedName = await excelFrame.locator('#ce-cell-name').inputValue();
  await sales(); await sales();
  check('Excel repeated snapshots preserve native range selection', (await excelFrame.locator('#ce-cell-name').inputValue()) === selectedName);
  const cell = { sheet: '销售', range: 'C2', expectedInputs: [[human.inputs[0][1]]], inputs: [[5]] };
  await command('excel', 'sheet.apply', cell);
  const calculated = await poll(sales, (value) => Number(value.values[0][2]) === 600);
  check('Excel native formula recalculation preserves human input', Number(calculated.values[0][2]) === 600 && calculated.inputs[0][3] === fixture.excel.human_marker);
  const summary = await command('excel', 'sheet.read', { sheet: '汇总', range: 'B2:B3' });
  check('Excel cross-sheet formulas recalculate', Number(summary.values[0][0]) === 1060 && Number(summary.values[1][0]) === 3);
  for (let i = 1; i <= 2; i++) {
    const undone = await command('excel', 'history.undo', { sheet: '销售', range: 'B2:E2' });
    check(`Excel cycle ${i}: programmatic native undo`, undone.native && undone.canRedo && Number(undone.snapshot.inputs[0][1]) === 3 && undone.snapshot.inputs[0][3] === fixture.excel.human_marker);
    check(`Excel cycle ${i}: stale write rejected before a native history action`, await staleWriteRejected('excel', 'sheet.apply', { ...cell, expectedInputs: [[999]] }));
    const a = await sales(), b = await command('excel', 'sheet.read', { sheet: '汇总', range: 'B2:B3' });
    check(`Excel cycle ${i}: read after undo preserves formulas`, Number(a.values[0][2]) === 360 && Number(b.values[0][0]) === 820);
    const redone = await command('excel', 'history.redo', { sheet: '销售', range: 'B2:E2' });
    check(`Excel cycle ${i}: programmatic native redo survives reads`, redone.native && Number(redone.snapshot.inputs[0][1]) === 5 && redone.snapshot.inputs[0][3] === fixture.excel.human_marker);
    const final = await poll(sales, (value) => Number(value.values[0][2]) === 600);
    check(`Excel cycle ${i}: redo recalculates native formulas`, Number(final.values[0][2]) === 600);
  }
  const step1 = await command('excel', 'history.undo', { sheet: '销售', range: 'B2:E2' });
  const step2 = await command('excel', 'history.undo', { sheet: '销售', range: 'B2:E2' });
  check('Excel consecutive undo traverses two actual native history steps', Number(step1.snapshot.inputs[0][1]) === 3 &&
    step1.snapshot.inputs[0][3] === fixture.excel.human_marker && step2.snapshot.inputs[0][3] === originalRemark);
  await sales();
  const redo1 = await command('excel', 'history.redo', { sheet: '销售', range: 'B2:E2' });
  await sales();
  const redo2 = await command('excel', 'history.redo', { sheet: '销售', range: 'B2:E2' });
  check('Excel consecutive redo restores human then agent history steps', Number(redo1.snapshot.inputs[0][1]) === 3 &&
    redo1.snapshot.inputs[0][3] === fixture.excel.human_marker && Number(redo2.snapshot.inputs[0][1]) === 5 &&
    redo2.snapshot.inputs[0][3] === fixture.excel.human_marker);
  await screenshot('excel-programmatic-redo');
  await api(`/api/sessions/${await session('excel')}/save`, {});
  check('Excel native history result saved through the current editor', (await api('/api/documents/excel/status')).saveState === 'saved');
  await page.waitForFunction(() => !document.getElementById('reopen-document')?.disabled);
  await page.locator('#reopen-document').click(); await connected('excel');
  const reopenedExcel = await sales();
  check('Excel saved native history result survives reopen', Number(reopenedExcel.inputs[0][1]) === 5 && Number(reopenedExcel.values[0][2]) === 600 && reopenedExcel.inputs[0][3] === fixture.excel.human_marker);
  check('No uncaught real browser runtime errors', errors.length === 0, errors);
  report.sourcesAfter = await sources();
  check('Adapter and bridge sources stayed fixed during the real run', JSON.stringify(report.sourcesBefore) === JSON.stringify(report.sourcesAfter));
  report.completed = true;
} catch (failure) {
  report.failure = failure.message; report.browserErrors = errors;
  await screenshot('failure').catch(() => {});
  process.exitCode = 1;
} finally {
  await fs.writeFile(path.join(output, 'summary.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ completed: report.completed, checks: report.checks.length, passed: report.checks.filter((item) => item.passed).length, output, failure: report.failure }));
  await browser.close(); await lab.close(); await proxy.close();
}
