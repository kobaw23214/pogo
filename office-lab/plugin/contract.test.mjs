// Protocol boundary tests only: these do not substitute for an ONLYOFFICE run.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('./plugin.js', import.meta.url), 'utf8');
const token = 'synthetic-isolated-test-token-1234567890';
const wordSnapshot = { kind: 'word', text: 'Human draft\n', selectedText: '', paragraphs: [] };

async function harness(editor = 'word', timeout = false, locationOverride = {}, bootstrapScope, adapterAvailable = true, historyWithoutRead = true, sheetList = true, selectionRead = true) {
  const messages = [];
  let listener, count = 0, pending;
  const pendingMethods = [];
  const methodCalls = [];
  const methodArguments = [];
  const top = { postMessage(data, target) { messages.push({ data: JSON.parse(JSON.stringify(data)), target }); } };
  const plugin = {
    info: { editorType: editor },
    callCommand(fn, close, recalc, callback) {
      count++;
      assert.equal(typeof fn, 'function');
      assert.equal(close, false);
      assert.equal(recalc, true);
      pending = callback;
      if (!timeout) callback(JSON.stringify({ ok: true, result: wordSnapshot }));
    },
    executeMethod(name, args, callback) {
      methodCalls.push(name);
      methodArguments.push(JSON.parse(JSON.stringify(args)));
      if (name === 'PogoOfficeCapabilities') {
        callback(adapterAvailable ? JSON.stringify({ ok: true, result: { id: 'pogo-office-history', version: 1, editor,
          engineVersion: '9.4.0', engineBuild: '129', readPreservesHistory: true, readPreservesSelection: true,
          sheetList: editor === 'cell' && sheetList,
          selectionRead,
          nativeUndo: true, nativeRedo: true, historyWithoutRead: historyWithoutRead ? true : undefined,
          mutationPreconditionsPreserveHistory: true } }) : undefined);
        return;
      }
      if (name === 'PogoOfficeSheets') {
        count++;
        callback(JSON.stringify({ ok: true, result: { kind: 'cell', sheets: ['明细', '尚未保存的重命名'] } }));
        return;
      }
      if (name === 'PogoOfficeSelection' && !timeout) {
        count++;
        callback(JSON.stringify({ ok: true, result: { kind: editor, text: '未保存选区', ...(editor === 'cell' ? { sheet: '当前表', range: 'B2' } : {}) } }));
        return;
      }
      count++; pending = callback;
      if (timeout) pendingMethods.push(callback);
      if (!timeout) callback(JSON.stringify({ ok: true, result: name === 'PogoOfficeHistory' ?
        { native: true, historyScope: 'editor-global', canUndo: true, canRedo: true,
          snapshot: editor === 'cell' && Object.keys(args[1]).length === 0 ? null : wordSnapshot } : wordSnapshot }));
    }
  };
  const context = vm.createContext({
    URLSearchParams,
    location: { origin: 'http://127.0.0.1:19896',
      search: `?sessionId=test-session&documentId=test-document&token=${token}&editor=${editor}&lang=zh-CN`,
      hash: '#theme-type=light', ...locationOverride },
    setTimeout: timeout ? (fn) => setTimeout(fn, 1) : setTimeout,
    clearTimeout,
    window: { top, Asc: { plugin }, __pogoOfficeLabScope: bootstrapScope,
      addEventListener(name, handler) { if (name === 'message') listener = handler; } }
  });
  vm.runInContext(source, context);
  await plugin.init?.();
  async function request(operation, args = {}, overrides = {}, eventOverrides = {}) {
    const payload = { protocol: 'pogo-office-lab/v1', type: 'request', sessionId: 'test-session',
      documentId: 'test-document', token, operationId: 'unique-request-01', operation, args, ...overrides };
    // postMessage would structured-clone data into the plugin's JavaScript realm.
    const data = vm.runInContext(`JSON.parse(${JSON.stringify(JSON.stringify(payload))})`, context);
    await listener?.({ source: top, origin: context.location.origin, data, ...eventOverrides });
    return messages.at(-1)?.data;
  }
  return { request, messages, methodCalls, methodArguments, count: () => count,
    allowNative: () => { timeout = false; },
    finish: () => (pendingMethods.shift() || pending)?.(JSON.stringify({ ok: true, result: wordSnapshot })) };
}

test('native loader query additions preserve exact scope without accepting malformed fragment identities', async () => {
  const current = await harness();
  assert.equal(current.messages[0].data.type, 'ready');
  assert.equal(current.messages[0].data.documentId, 'test-document');
  const legacyHash = `#sessionId=test-session&documentId=test-document&token=${token}&editor=word`;
  assert.equal((await harness('word', false, { search: '', hash: legacyHash })).messages[0].data.type, 'ready');
  assert.equal((await harness('word', false, { search: '', hash: `${legacyHash}?lang=zh-CN&theme-type=light` })).messages.length, 0);
  assert.equal((await harness('word', false, { search: '?editor=word&lang=zh-CN', hash: legacyHash })).messages.length, 0);
});

test('path-scoped bootstrap survives loader parameters and fails closed on a malformed bootstrap', async () => {
  const identity = { sessionId: 'test-session', documentId: 'test-document', token, editor: 'word' };
  const polluted = { search: '?lang=zh-CN?lang=zh-CN', hash: '#theme-type=light' };
  const scoped = await harness('word', false, polluted, identity);
  assert.equal(scoped.messages[0].data.type, 'ready');
  assert.equal(scoped.messages[0].data.token, token);
  assert.equal((await harness('word', false, {}, { ...identity, editor: 'word?lang=zh-CN' })).messages.length, 0);
  assert.equal((await harness('word', false, {}, { ...identity, token: 'short' })).messages.length, 0);
  assert.equal((await harness('word', false, {}, { sessionId: 'test-session' })).messages.length, 0);
  assert.equal((await harness('word', false, {}, { ...identity, arbitrary: true })).messages.length, 0);
});

test('binding rejects different sender/origin/token/document/session before touching the editor', async () => {
  const h = await harness();
  assert.equal(h.messages[0].data.type, 'ready');
  assert.equal(h.messages[0].target, 'http://127.0.0.1:19896');
  const initial = h.messages.length;
  await h.request('word.read', {}, {}, { source: {} });
  await h.request('word.read', {}, {}, { origin: 'https://another-origin.invalid' });
  for (const field of ['token', 'documentId', 'sessionId']) await h.request('word.read', {}, { [field]: 'wrong' });
  assert.equal(h.count(), 0);
  assert.equal(h.messages.length, initial);
});

test('same operation is replay-safe; changing payload with its ID is refused', async () => {
  const h = await harness();
  const first = await h.request('word.read');
  assert.equal(first.ok, true);
  assert.deepEqual(first.result, wordSnapshot);
  assert.deepEqual(await h.request('word.read'), first);
  assert.equal(h.count(), 1);
  assert.equal((await h.request('capabilities')).error.code, 'OPERATION_ID_CONFLICT');
  assert.equal(h.count(), 1);
});

test('unknown operations and target ambiguities cannot call the native editor', async () => {
  const h = await harness();
  assert.equal((await h.request('eval', { script: 'arbitrary' })).error.code, 'UNSUPPORTED_OPERATION');
  assert.equal((await h.request('word.replace', { search: 'draft', replacement: 'x', expectedText: 'Human draft\n', expectedCount: 2 },
    { operationId: 'unique-request-02' })).error.code, 'INVALID_ARGUMENT');
  assert.equal(h.count(), 0);
});

test('Word native history uses the explicit native adapter without a readback callCommand', async () => {
  const h = await harness();
  const result = await h.request('history.undo');
  assert.equal(result.ok, true);
  assert.deepEqual(h.methodCalls, ['PogoOfficeCapabilities', 'PogoOfficeHistory']);
  assert.equal(result.result.native, true);
  assert.equal(result.result.historyScope, 'editor-global');
  assert.equal(h.count(), 1);
});

test('an unmodified engine advertises no history or read and fails closed', async () => {
  const h = await harness('cell', false, {}, undefined, false);
  assert.equal(h.messages[0].data.capabilities.nativeUndo, false);
  assert.equal(h.messages[0].data.capabilities.readPreservesHistory, false);
  assert.equal((await h.request('history.undo')).error.code, 'ENGINE_EXTENSION_REQUIRED');
  assert.equal((await h.request('sheet.read', { sheet: 'Test', range: 'A1' }, { operationId: 'unique-request-02' })).error.code, 'ENGINE_EXTENSION_REQUIRED');
  assert.equal(h.count(), 0);
  assert.deepEqual(h.methodCalls, ['PogoOfficeCapabilities']);
});

test('a stale adapter version without the no-read history feature also fails closed', async () => {
  const h = await harness('cell', false, {}, undefined, true, false);
  assert.equal(h.messages[0].data.capabilities.historyWithoutRead, false);
  assert.equal(h.messages[0].data.capabilities.read, false);
  assert.equal(h.messages[0].data.capabilities.nativeUndo, false);
  assert.equal((await h.request('history.undo')).error.code, 'ENGINE_EXTENSION_REQUIRED');
  assert.equal(h.count(), 0);
});

test('verified native sheet history works before an Agent read and optional observations stay explicit', async () => {
  const h = await harness();
  assert.equal(h.messages[0].data.capabilities.nativeUndo, true);
  assert.equal(h.messages[0].data.capabilities.nativeRedo, true);
  assert.equal((await h.request('history.redo')).ok, true);
  assert.equal(h.count(), 1);
  const sheet = await harness('cell');
  const unobserved = await sheet.request('history.undo');
  assert.equal(unobserved.ok, true);
  assert.equal(unobserved.result.native, true);
  assert.equal(unobserved.result.snapshot, null);
  assert.deepEqual(sheet.methodArguments[1], ['undo', {}]);
  assert.equal((await sheet.request('history.undo', { sheet: 'Test', range: 'A1' }, { operationId: 'unique-request-02' })).ok, true);
  assert.deepEqual(sheet.methodArguments[2], ['undo', { sheet: 'Test', range: 'A1' }]);
  assert.equal((await sheet.request('sheet.read', { sheet: 'Another', range: 'B2:C3' }, { operationId: 'unique-request-03' })).ok, true);
  assert.equal((await sheet.request('history.redo', {}, { operationId: 'unique-request-04' })).ok, true);
  assert.deepEqual(sheet.methodArguments[4], ['redo', { sheet: 'Another', range: 'B2:C3' }]);
  const beforeInvalid = sheet.count();
  assert.equal((await sheet.request('history.undo', { sheet: 'Another' }, { operationId: 'unique-request-05' })).error.code, 'INVALID_ARGUMENT');
  assert.equal(sheet.count(), beforeInvalid);
});

test('read timeout does not block later reads or turn a late read into a write', async () => {
  const h = await harness('word', true);
  assert.equal((await h.request('word.read')).error.code, 'READ_TIMEOUT');
  h.allowNative();
  assert.equal((await h.request('word.read', {}, { operationId: 'unique-request-02' })).ok, true);
  h.finish();
  await Promise.resolve();
  assert.equal((await h.request('capabilities', {}, { operationId: 'unique-request-03' })).result.blocked, false);
  assert.equal(h.count(), 2);
  const replay = await h.request('word.read');
  assert.equal(replay.error.code, 'READ_TIMEOUT');
  assert.equal(h.count(), 2);
});

test('unknown writes resume only after the original native promise and a real barrier settle', async () => {
  const h = await harness('word', true);
  const args = { search: 'draft', replacement: 'updated', expectedText: 'Human draft\n', expectedCount: 1 };
  const unknown = await h.request('word.replace', args);
  assert.equal(unknown.error.code, 'EDITOR_OUTCOME_UNKNOWN');
  const control = { type: 'reconcile', barrierId: 'recovery-barrier-01', recoveryRevision: 1, operationIds: ['unique-request-01'] };
  assert.equal((await h.request('', {}, control)).settled, false);
  assert.equal((await h.request('word.replace', args, { operationId: 'unique-request-02' })).error.code, 'SESSION_BLOCKED');
  h.allowNative();
  assert.equal((await h.request('word.read', {}, { operationId: 'unique-request-03' })).ok, true);
  assert.equal((await h.request('', {}, control)).settled, false);
  h.finish();
  await new Promise(resolve => setImmediate(resolve));
  const settled = await h.request('', {}, control);
  assert.equal(settled.type, 'reconciled');
  assert.equal(settled.settled, true);
  assert.equal(settled.recoveryRevision, 1);
  assert.equal(settled.operations.length, 1);
  assert.equal(settled.operations[0].operation, 'word.replace');
  assert.equal(settled.operations[0].ok, true);
  assert.equal((await h.request('word.replace', args)).error.code, 'EDITOR_OUTCOME_UNKNOWN');
  assert.equal((await h.request('word.replace', args, { operationId: 'unique-request-04' })).ok, true);
  assert.equal(h.methodCalls.filter(name => name === 'PogoOfficeApply').length, 2);
  assert.equal(h.methodCalls.filter(name => name === 'PogoOfficeCapabilities').length, 2);
});

test('pending duplicate writes and recovery control messages never execute a second write', async () => {
  const h = await harness('word', true);
  const args = { search: 'draft', replacement: 'updated', expectedText: 'Human draft\n', expectedCount: 1 };
  const first = h.request('word.replace', args);
  await Promise.resolve(); await Promise.resolve();
  assert.equal((await h.request('word.replace', args)).error.code, 'OPERATION_PENDING');
  assert.equal((await h.request('word.replace', { ...args, replacement: 'other' })).error.code, 'OPERATION_ID_CONFLICT');
  assert.equal((await first).error.code, 'EDITOR_OUTCOME_UNKNOWN');
  const before = h.messages.length;
  await h.request('', {}, { type: 'reconcile', barrierId: 'valid-barrier-01', recoveryRevision: 1, operationIds: ['unique-request-01'] }, { origin: 'https://other.example' });
  assert.equal(h.messages.length, before);
  assert.equal(h.methodCalls.filter(name => name === 'PogoOfficeApply').length, 1);
});

test('live sheet discovery uses fixed native getters and respects optional engine support', async () => {
  const h = await harness('cell');
  assert.equal(h.messages[0].data.capabilities.sheetList, true);
  const result = await h.request('sheet.list');
  assert.equal(result.ok, true);
  assert.deepEqual(result.result, { kind: 'cell', sheets: ['明细', '尚未保存的重命名'] });
  assert.deepEqual(h.methodCalls, ['PogoOfficeCapabilities', 'PogoOfficeSheets']);
  assert.deepEqual(h.methodArguments[1], []);
  assert.equal(h.count(), 1);
  const old = await harness('cell', false, {}, undefined, true, true, false);
  assert.equal(old.messages[0].data.capabilities.sheetList, false);
  assert.equal((await old.request('sheet.list')).error.code, 'UNSUPPORTED_CAPABILITY');
  assert.equal(old.count(), 0);
  const word = await harness('word');
  assert.equal((await word.request('sheet.list')).error.code, 'WRONG_EDITOR');
  assert.equal(word.count(), 0);
  const unexpected = await harness('cell');
  assert.equal((await unexpected.request('sheet.list', { sheet: 'Sheet1' })).error.code, 'INVALID_ARGUMENT');
  assert.equal(unexpected.count(), 0);
});

test('selection.read is an explicit bound read-only getter and never arbitrary or historical code', async () => {
  const binding = { documentId: 'test-document', instanceId: 'instance_selection_001', canvasVersion: 'a'.repeat(64), dirtyEpoch: 3 };
  const h = await harness();
  assert.equal(h.messages[0].data.capabilities.selectionRead, true);
  const reply = await h.request('selection.read', binding);
  assert.equal(reply.ok, true);
  assert.equal(reply.result.text, '未保存选区');
  assert.deepEqual(h.methodCalls, ['PogoOfficeCapabilities', 'PogoOfficeSelection']);
  assert.deepEqual(h.methodArguments[1], []);
  for (const [index, args] of [
    { ...binding, documentId: 'different-document' }, { ...binding, dirtyEpoch: -1 },
    { ...binding, canvasVersion: 'old' }, { ...binding, script: 'anything' }, {}
  ].entries()) assert.equal((await h.request('selection.read', args, { operationId: `bad-selection-${index}` })).error.code, 'INVALID_ARGUMENT');
  assert.equal(h.count(), 1);
  const old = await harness('word', false, {}, undefined, true, true, true, false);
  assert.equal(old.messages[0].data.capabilities.selectionRead, false);
  assert.equal((await old.request('selection.read', binding)).error.code, 'UNSUPPORTED_CAPABILITY');
  assert.equal(old.count(), 0);
});

test('selection timeout remains read-only and does not block or dirty a later read', async () => {
  const h = await harness('word', true);
  const binding = { documentId: 'test-document', instanceId: 'instance_selection_001', canvasVersion: 'a'.repeat(64), dirtyEpoch: 3 };
  assert.equal((await h.request('selection.read', binding)).error.code, 'READ_TIMEOUT');
  h.allowNative();
  assert.equal((await h.request('selection.read', binding, { operationId: 'fresh-selection-02' })).ok, true);
  const caps = await h.request('capabilities', {}, { operationId: 'selection-status-03' });
  assert.equal(caps.result.blocked, false);
});
