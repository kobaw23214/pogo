import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const run = promisify(execFile);
const root = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(process.env.OFFICE_LAB_DATA || path.join(root, '../../.localagent/checks/onlyoffice-prototype-20261012'));
const output = path.join(data, process.env.OFFICE_LAB_CHECK_NAME || 'live');
const runtime = JSON.parse(await fs.readFile(path.join(data, 'private/runtime.json'), 'utf8'));
const fixture = JSON.parse(await fs.readFile(path.join(data, 'fixtures/manifest.json'), 'utf8'));
const playwright = await import(process.env.OFFICE_LAB_PLAYWRIGHT || '/Users/wangy/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs');
await fs.mkdir(output, { recursive: true });
const report = { syntheticOnly: true, suite: 'real-onlyoffice-community-browser-and-cli', checks: [], keyboard: 'trusted Chromium input; physical IME responsiveness is not measured', completed: false };
const check = (name, okay, detail) => { report.checks.push({ name, passed: !!okay, ...(detail ? { detail } : {}) }); if (!okay) throw new Error(name); };
async function cli(action, doc = 'word', operation, args = {}, operationId) {
  const argv = [path.join(root, 'cli.mjs'), action, doc];
  if (operation) argv.push(operation, JSON.stringify(args), operationId || crypto.randomUUID());
  const result = await run(process.execPath, argv, { env: { ...process.env, OFFICE_LAB_DATA: data }, timeout: 50000, maxBuffer: 1_000_000 });
  return JSON.parse(result.stdout);
}
const command = async (doc, operation, args = {}, id) => (await cli('command', doc, operation, args, id)).result;
const sourcePaths = ['server.mjs', 'cli.mjs', 'plugin/plugin.js', 'plugin/config.json', 'public/app.mjs', 'public/index.html', 'public/style.css'];
const sources = async () => Object.fromEntries(await Promise.all(sourcePaths.map(async (name) => [name, crypto.createHash('sha256').update(await fs.readFile(path.join(root, name))).digest('hex')])));
report.sourcesBefore = await sources();
const browser = await playwright.chromium.launch({ executablePath: process.env.OFFICE_LAB_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const page = await browser.newPage({ viewport: { width: 1520, height: 1080 } });
const errors = [];
page.on('pageerror', (error) => errors.push(error.message.slice(0, 300)));
async function connected(doc) {
  await page.waitForFunction(() => document.getElementById('read-document')?.disabled === false, null, { timeout: 45000 });
  const session = await cli('describe', doc);
  check(`${doc}: actual editor plugin ready`, session.ready && !session.blocked);
  return session;
}
function editorFrame(doc) {
  const part = doc === 'word' ? 'documenteditor' : 'spreadsheeteditor';
  const frame = page.frames().find((item) => { try { return new URL(item.url()).pathname.includes(`/apps/${part}/main/`); } catch { return false; } });
  if (!frame) throw new Error(`Real ${part} frame was not found`);
  return frame;
}
async function screenshot(name) { await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true }); }
async function poll(read, accept, timeout = 10000) {
  const deadline = Date.now() + timeout; let value;
  do { value = await read(); if (accept(value)) return value; await page.waitForTimeout(300); } while (Date.now() < deadline);
  throw new Error('Actual editor content did not reach expected state');
}
try {
  await page.goto(runtime.url);
  const wordSession = await connected('word');
  check('Word exposes native plugin undo', wordSession.capabilities?.nativeUndo === true);
  const wordInitial = await command('word', 'word.read');
  check('Word original marker read from live editor', wordInitial.text.includes(fixture.word.original));
  check('Word starts with a fresh synthetic fixture', !wordInitial.text.includes(fixture.word.human_marker));
  check('Unverified programmatic redo is disabled', await page.locator('#redo-document').isDisabled());
  await screenshot('word-before');
  const wordFrame = editorFrame('word');
  const tutorial = wordFrame.getByText('知道了', { exact: true });
  if (await tutorial.count()) await tutorial.first().click().catch(() => {});
  await wordFrame.locator('#id_main_view').click({ position: { x: 420, y: 300 } });
  await page.keyboard.press('Meta+End');
  await page.keyboard.press('Enter');
  await page.keyboard.insertText(fixture.word.human_marker);
  const humanWord = await poll(() => command('word', 'word.read'), (value) => value.text.includes(fixture.word.human_marker));
  check('Word agent reads unsaved trusted-browser input', humanWord.text.includes(fixture.word.human_marker));
  const wordOp = `word-${crypto.randomUUID()}`;
  const wordArgs = { search: fixture.word.original, replacement: fixture.word.edited, expectedText: humanWord.text, expectedCount: 1 };
  const changedWord = await command('word', 'word.replace', wordArgs, wordOp);
  check('Word same-editor mutation preserves human input', changedWord.after.text.includes(fixture.word.edited) && changedWord.after.text.includes(fixture.word.human_marker));
  const replay = await command('word', 'word.replace', wordArgs, wordOp);
  check('Word identical operation ID returns prior receipt', JSON.stringify(replay) === JSON.stringify(changedWord));
  let staleRejected = false;
  try { await command('word', 'word.replace', { ...wordArgs, expectedText: wordInitial.text }); } catch { staleRejected = true; }
  check('Word refuses stale text precondition', staleRejected);
  await screenshot('word-agent-edited');
  const undoWord = await command('word', 'history.undo');
  check('Word native undo preserves earlier human edit', undoWord.native === true && undoWord.snapshot.text.includes(fixture.word.original) && undoWord.snapshot.text.includes(fixture.word.human_marker) && !undoWord.snapshot.text.includes(fixture.word.edited));
  await screenshot('word-native-undone');
  const reappliedWord = await command('word', 'word.replace', { ...wordArgs, expectedText: undoWord.snapshot.text });
  check('Word agent can apply a new checked change after undo', reappliedWord.after.text.includes(fixture.word.edited) && reappliedWord.after.text.includes(fixture.word.human_marker));
  await cli('save', 'word');
  const savedWord = await cli('status', 'word');
  check('Word current-editor export persisted a file', savedWord.saveState === 'saved' && savedWord.revision > 0 && !!savedWord.savedAt);
  await fs.copyFile(path.join(data, 'saved', fixture.files.word.name), path.join(output, 'word-edited.docx'));
  await page.waitForFunction(() => document.getElementById('reopen-document')?.disabled === false, null, { timeout: 10000 });
  await page.locator('#reopen-document').click();
  await connected('word');
  const reopenedWord = await command('word', 'word.read');
  check('Word reopened saved file contains agent and human changes', reopenedWord.text.includes(fixture.word.edited) && reopenedWord.text.includes(fixture.word.human_marker));
  await screenshot('word-reopened');
  await page.locator('[data-document="excel"]').click();
  const excelSession = await connected('excel');
  check('Excel plugin truthfully distinguishes API undo', excelSession.capabilities?.nativeUndo === false);
  const excelInitial = await command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' });
  check('Excel live range retains native formula', String(excelInitial.inputs[0][2]).replace(/^=/, '') === fixture.excel.formulas['销售!D2']);
  await screenshot('excel-before');
  const excelFrame = editorFrame('excel');
  const address = excelFrame.locator('#ce-cell-name');
  await address.fill('E2'); await address.press('Enter');
  // The name box retains focus after Enter in this version. Enter the cell
  // through its real formula bar rather than typing a new named range.
  await excelFrame.locator('#ce-cell-content').fill(fixture.excel.human_marker);
  await excelFrame.locator('#ce-cell-content').press('Enter');
  const humanExcel = await poll(() => command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' }), (value) => value.inputs[0][3] === fixture.excel.human_marker);
  check('Excel agent reads unsaved trusted-browser input', humanExcel.inputs[0][3] === fixture.excel.human_marker);
  const excelArgs = { sheet: '销售', range: 'C2', expectedInputs: [[humanExcel.inputs[0][1]]], inputs: [[fixture.excel.edited]] };
  const changedExcel = await command('excel', 'sheet.apply', excelArgs);
  check('Excel same-editor cell change', Number(changedExcel.after.inputs[0][0]) === fixture.excel.edited);
  const calculated = await poll(() => command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' }), (value) => Number(value.values[0][2]) === fixture.excel.calculated_edited['销售!D2']);
  check('Excel formula recalculates and preserves human edit', Number(calculated.values[0][2]) === 600 && calculated.inputs[0][3] === fixture.excel.human_marker);
  const summary = await command('excel', 'sheet.read', { sheet: '汇总', range: 'B2:B3' });
  check('Excel cross-sheet formulas recalculate', Number(summary.values[0][0]) === 1060 && Number(summary.values[1][0]) === 3);
  await screenshot('excel-agent-edited');
  // Focus a real grid cell: the name/formula inputs have their own text undo.
  await excelFrame.locator('#editor_sdk').click({ position: { x: 350, y: 100 } });
  await page.keyboard.press('Meta+z');
  const undoneExcel = await poll(() => command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' }), (value) => Number(value.inputs[0][1]) === fixture.excel.original);
  check('Excel real UI undo preserves earlier human edit', Number(undoneExcel.values[0][2]) === 360 && undoneExcel.inputs[0][3] === fixture.excel.human_marker);
  await screenshot('excel-native-undone');
  await command('excel', 'sheet.apply', { ...excelArgs, expectedInputs: [[undoneExcel.inputs[0][1]]] });
  await cli('save', 'excel');
  const savedExcel = await cli('status', 'excel');
  check('Excel current-editor export persisted a file', savedExcel.saveState === 'saved' && savedExcel.revision > 0 && !!savedExcel.savedAt);
  await fs.copyFile(path.join(data, 'saved', fixture.files.excel.name), path.join(output, 'excel-edited.xlsx'));
  await page.waitForFunction(() => document.getElementById('reopen-document')?.disabled === false, null, { timeout: 10000 });
  await page.locator('#reopen-document').click(); await connected('excel');
  const reopenedExcel = await command('excel', 'sheet.read', { sheet: '销售', range: 'B2:E2' });
  check('Excel reopened saved file contains agent and human changes', Number(reopenedExcel.inputs[0][1]) === 5 && reopenedExcel.inputs[0][3] === fixture.excel.human_marker && Number(reopenedExcel.values[0][2]) === 600);
  await screenshot('excel-reopened');
  check('No uncaught browser runtime errors', errors.length === 0, errors);
  report.sourcesAfter = await sources();
  check('Reviewed source did not change during real run', JSON.stringify(report.sourcesBefore) === JSON.stringify(report.sourcesAfter));
  report.completed = true;
  await cli('close', 'excel');
} catch (error) {
  report.failure = error.message; report.browserErrors = errors;
  await screenshot('failure').catch(() => {});
  const ui = (await page.locator('body').innerText().catch(() => '')).slice(0, 5000);
  await fs.writeFile(path.join(output, 'failure-ui.txt'), ui);
  console.error(error.message);
  process.exitCode = 1;
} finally {
  await fs.writeFile(path.join(output, 'summary.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ completed: report.completed, checks: report.checks.length, passed: report.checks.filter((item) => item.passed).length, output }));
  await browser.close();
}
