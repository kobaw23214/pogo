// Inspect the actual Agent transcript and reopened synthetic exports. This
// utility never sends commands to an editor or connects to a model.
import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';

const data = process.env.OFFICE_CHECK_DATA;
if (!data) throw new Error('OFFICE_CHECK_DATA must identify the isolated check directory.');
const runtime = JSON.parse(await fs.readFile(path.join(data, 'private/runtime.json'), 'utf8'));
assert.equal(runtime.model, 'deterministic integration fixture');
assert.equal(runtime.url, 'http://127.0.0.1:19897');
const rows = (await fs.readFile(path.join(data, 'sessions', runtime.conversationId + '.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse);
const messages = rows.filter(row => row.type === 'message').map(row => row.message);
const checks = [];
function check(name, value) { checks.push({name, passed: !!value}); }
const expected = ['open', 'word.read', 'word.replace', 'history.undo', 'word.read', 'history.redo', 'word.read', 'save'];
const scenarios = {};
for (const kind of ['word', 'excel']) {
  const start = messages.findIndex(message => message.role === 'user' && message.content?.some(block => block.type === 'text' && block.text === `OFFICE_CHECK:${kind}:run2b`));
  assert.ok(start >= 0, `Missing ${kind} scenario`);
  const next = messages.findIndex((message, index) => index > start && message.role === 'user' && message.content?.some(block => block.type === 'text' && block.text.startsWith('OFFICE_CHECK:')));
  const sequence = messages.slice(start, next < 0 ? undefined : next);
  const calls = sequence.flatMap(message => message.content || []).filter(block => block.type === 'tool_use');
  const results = sequence.flatMap(message => message.content || []).filter(block => block.type === 'tool_result');
  check(`${kind}: all calls use registered docs_office`, calls.length === 8 && calls.every(call => call.name === 'docs_office'));
  const operations = calls.map(call => call.input.operation || call.input.action);
  const operationsExpected = kind === 'word' ? expected : expected.map(operation => operation.replace('word.read', 'sheet.read').replace('word.replace', 'sheet.apply'));
  check(`${kind}: open/read/edit/undo/read/redo/read/save`, JSON.stringify(operations) === JSON.stringify(operationsExpected));
  const parsed = results.map(result => JSON.parse(result.text));
  check(`${kind}: eight successful real tool results`, parsed.length === 8 && parsed.every((result, i) => result.ok === true || i === 0 && result.session?.ready === true));
  const sessionID = parsed[0].session?.id;
  check(`${kind}: same mounted document throughout`, !!sessionID && calls.slice(1).every(call => call.input.document_session_id === sessionID));
  check(`${kind}: native undo and redo`, parsed[3].result?.native === true && parsed[5].result?.native === true);
  const readBefore = parsed[1].result, readUndo = parsed[4].result, readRedo = parsed[6].result;
  const human = kind === 'word' ? 'POGO_WORD_USER_EDIT_001' : 'POGO_EXCEL_USER_EDIT_001';
  check(`${kind}: unsaved human input preserved before/undo/redo`, [readBefore, readUndo, readRedo].every(result => JSON.stringify(result).includes(human)));
  if (kind === 'word') {
    check('word: original restored after undo', readUndo.text.includes('当前状态为草稿'));
    check('word: edited text restored after redo', readRedo.text.includes('Agent 已更新正文'));
  } else {
    check('excel: undo restores formula value 360', readUndo.values[0][1] === 3 && readUndo.values[0][2] === 360);
    check('excel: redo recalculates formula to 600', readRedo.values[0][1] === 5 && readRedo.values[0][2] === 600);
  }
  check(`${kind}: confirmed persistent save`, parsed[7].session?.saveState === 'saved' && parsed[7].session?.revision === 1 && parsed[7].session?.dirty === false);
  check(`${kind}: actual agent final response`, sequence.some(message => message.content?.some(block => block.type === 'text' && block.text.startsWith(`OFFICE_SCENARIO_OK: ${kind}`))));
  scenarios[kind] = {sessionID, calls, results: parsed};
}
const wordReopen = JSON.parse(await fs.readFile(path.join(data, 'verification/word-reopen.json'), 'utf8'));
check('word: new editor reads persisted human and agent text', wordReopen.result.ok && wordReopen.result.result.text.includes('POGO_WORD_USER_EDIT_001') && wordReopen.result.result.text.includes('Agent 已更新正文'));
const excelReopen = JSON.parse(await fs.readFile(path.join(data, 'verification/excel-reopen.json'), 'utf8'));
check('excel: new editor reads persisted quantity/formula/human input', excelReopen.sales.ok && excelReopen.sales.result.values[0][1] === 5 && excelReopen.sales.result.values[0][2] === 600 && excelReopen.sales.result.values[0][3] === 'POGO_EXCEL_USER_EDIT_001');
check('excel: cross-sheet recalculation survives reopen', excelReopen.summary.ok && excelReopen.summary.result.values[0][0] === 1060 && excelReopen.summary.result.values[1][0] === 3);
const exports = JSON.parse(await fs.readFile(path.join(data, 'verification/exports-check.json'), 'utf8'));
check('persisted OOXML content/styles/formulas', exports.passed === true);
const sourceBefore = JSON.parse(await fs.readFile(path.join(data, 'verification/sources-before.json'), 'utf8'));
const sourceAfter = {};
for (const file of Object.keys(sourceBefore)) sourceAfter[file] = createHash('sha256').update(await fs.readFile(path.resolve('pogo', file))).digest('hex');
const sourceDrift = Object.keys(sourceBefore).filter(file => sourceBefore[file] !== sourceAfter[file]);
const summary = {passed:checks.every(item => item.passed), checks, scenarios, sourceBefore, sourceAfter, sourceDrift, scope:'Deterministic model output driving the real Pogo Agent loop and native Office editor; not model autonomy or physical IME performance acceptance.'};
await fs.writeFile(path.join(data, 'verification/main-integration.json'), JSON.stringify(summary, null, 2));
console.log(JSON.stringify({passed:summary.passed,checks:checks.length,failed:checks.filter(item=>!item.passed),sourceDrift}));
if (!summary.passed) process.exitCode = 1;
