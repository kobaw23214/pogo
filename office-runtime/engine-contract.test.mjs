// Fixed adapter contract tests with synthetic SDK getters. These do not claim
// live ONLYOFFICE integration or document-format fidelity.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('./engine-extension.js', import.meta.url), 'utf8');

function harness(editor = 'cell') {
  const context = vm.createContext({ setTimeout });
  vm.runInContext(`
    let mutationCalls = 0;
    function Api() { this.names = ['明细', '汇总']; this.selection = 'C2'; this.selectedText = '尚未保存的选区'; this.value = '未保存单元格'; this.areaCount = 1; }
    Api.prototype.GetVersion = function() { return '9.4.0'; };
    Api.prototype.getJsApi = function() {
      const api = this;
      return { GetSheets() { return api.names.map(name => ({ GetName() { return name; } })); },
        GetActiveSheet() { return { GetName() { return api.names[0]; }, GetSelection() { return {
          GetAreas() { return { GetCount() { return api.areaCount; } }; },
          GetAddress() { return api.selection; },
          GetCells() { return { GetValue() { return api.value; }, GetFormula() { return '=1+2'; }, GetText() { return api.value; } }; }
        }; } }; }
      };
    };
    Api.prototype.asc_GetSelectedText = function(clear, options) {
      if (clear !== false || options.ParaSeparator !== '\\n') throw new Error('Selection must be observed without clearing or restoring it.');
      return this.selectedText;
    };
    ['asc_getCanUndo','asc_getCanRedo','Undo','Redo','asc_Undo','asc_Redo','canRunBuilderScript',
     '_beforeEvalCommand','_afterEvalCommand','onEndBuilderScript'].forEach(name => {
      Api.prototype[name] = function() { mutationCalls++; throw new Error('Read must not touch history or mutate.'); };
    });
    const window = { AscCommon: { g_cBuildNumber: '129' }, Asc: { ${editor === 'word' ? 'asc_docs_api' : 'spreadsheet_api'}: Api } };
    const api = new Api();
  `, context);
  vm.runInContext(source, context);
  return {
    read: () => JSON.parse(vm.runInContext('api.pluginMethod_PogoOfficeSheets()', context)),
    selection: () => JSON.parse(vm.runInContext('api.pluginMethod_PogoOfficeSelection()', context)),
    capabilities: () => JSON.parse(vm.runInContext('api.pluginMethod_PogoOfficeCapabilities()', context)),
    run: (code) => vm.runInContext(code, context),
  };
}

test('worksheet names observe unsaved renames without a history or selection operation', () => {
  const h = harness();
  assert.equal(h.capabilities().result.sheetList, true);
  assert.deepEqual(h.read(), { ok: true, result: { kind: 'cell', sheets: ['明细', '汇总'] } });
  h.run(`api.names[0] = '尚未保存的重命名'`);
  assert.deepEqual(h.read().result.sheets, ['尚未保存的重命名', '汇总']);
  assert.equal(h.run('mutationCalls'), 0);
  assert.equal(h.run('api.selection'), 'C2');
});

test('Word selection reads the current unsaved text without a range Select, Save, restore, or history', () => {
  const h = harness('word');
  assert.equal(h.capabilities().result.selectionRead, true);
  assert.deepEqual(h.selection(), { ok: true, result: { kind: 'word', text: '尚未保存的选区', locator: { kind: 'text', exact: '尚未保存的选区' } } });
  h.run(`api.selectedText = '新的未保存内容'`);
  assert.equal(h.selection().result.text, '新的未保存内容');
  assert.equal(h.run('mutationCalls'), 0);
  assert.equal(h.run('api.selection'), 'C2');
  h.run(`api.selectedText = ''`);
  assert.equal(h.selection().error.code, 'EMPTY_SELECTION');
  h.run(`api.selectedText = 'x'.repeat(32001)`);
  assert.equal(h.selection().error.code, 'SELECTION_LIMIT');
  h.run(`window.AscCommon.g_cBuildNumber = '128'`);
  assert.equal(h.selection().error.code, 'ENGINE_VERSION_MISMATCH');
});

test('Excel selection uses the active unsaved worksheet and preserves coordinates, history, and values', () => {
  const h = harness();
  h.run(`api.selection = 'B2:C3'; api.names[0] = '尚未保存的工作表'`);
  const result = h.selection();
  assert.equal(h.capabilities().result.selectionRead, true);
  assert.equal(result.ok, true);
  assert.equal(result.result.sheet, '尚未保存的工作表');
  assert.equal(result.result.range, 'B2:C3');
  assert.equal(result.result.text, '未保存单元格\t未保存单元格\n未保存单元格\t未保存单元格');
  assert.deepEqual(result.result.inputs, [['=1+2', '=1+2'], ['=1+2', '=1+2']]);
  assert.equal(h.run('mutationCalls'), 0);
  assert.equal(h.run('api.selection'), 'B2:C3');
  assert.equal(h.run('api.value'), '未保存单元格');
});

test('Excel rejects empty, disjoint, reversed, full-column, and oversized selections before reading cells', () => {
  for (const [change, code] of [
    [`api.value = ''`, 'EMPTY_SELECTION'], [`api.areaCount = 2`, 'SELECTION_SHAPE'],
    [`api.selection = 'B2:A1'`, 'INVALID_ARGUMENT'], [`api.selection = 'A:A'`, 'INVALID_ARGUMENT'],
    [`api.selection = 'A1:U20'`, 'INVALID_ARGUMENT'], [`api.value = 'x'.repeat(4001)`, 'SELECTION_LIMIT'],
  ]) {
    const h = harness(); h.run(change);
    assert.equal(h.selection().error.code, code);
    assert.equal(h.run('mutationCalls'), 0);
  }
});

test('worksheet discovery rejects Word, stale engines, and unbounded names', () => {
  const word = harness('word');
  assert.equal(word.capabilities().result.sheetList, false);
  assert.equal(word.read().error.code, 'WRONG_EDITOR');
  const stale = harness();
  stale.run(`window.AscCommon.g_cBuildNumber = '128'`);
  assert.equal(stale.read().error.code, 'ENGINE_VERSION_MISMATCH');
  const tooLong = harness();
  tooLong.run(`api.names = ['a'.repeat(129)]`);
  assert.equal(tooLong.read().error.code, 'DOCUMENT_LIMIT');
  const tooMany = harness();
  tooMany.run(`api.names = Array.from({ length: 4097 }, (_, i) => 'sheet' + i)`);
  assert.equal(tooMany.read().error.code, 'DOCUMENT_LIMIT');
  for (const h of [word, stale, tooLong, tooMany]) assert.equal(h.run('mutationCalls'), 0);
});
