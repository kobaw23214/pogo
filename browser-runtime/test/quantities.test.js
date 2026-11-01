import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { EvidenceStore, parseQuantity } from '../src/quantities.js';

function provenance(id = 'obs-1', generation = 1, document_version = 1) {
  return { observation_id: id, generation, tab: 't1', document_version, version: `${generation}:t1:${document_version}:0:observed-dom-revision`, observed_at: '2026-10-03T06:00:00.000Z', url: `https://example.test/${id}`, frame_url: 'https://example.test/frame' };
}
function definitions(values, fields = []) {
  return { schema: 'dom-evidence-v1', source_id: 'document', scope: 'document', text: 'Visible evidence', tables: [], headings: [], paragraphs: [],
    definitions: [{ source_id: 'dl-1', groups: values.map((value, index) => ({ source_id: `group-${index}`,
      terms: [{ source_id: `term-${index}`, text: fields[index] ?? '重量' }],
      descriptions: [{ source_id: `description-${index}`, text: value }],
    })) }] };
}
function quantities(store, values, id = 'obs-1', generation = 1, fields = []) {
  return store.record(definitions(values, fields), provenance(id, generation)).quantities;
}
function calculate(store, operation, values, generation = 1) {
  return store.calculate({ operation, quantity_refs: values.map(value => value.quantity_ref) }, generation);
}

test('parser accepts exact decimals with explicit units, including negatives and aliases', () => {
  for (const [raw, value, unit, dimension] of [
    ['0.100 CNY', '0.1', 'CNY', 'currency'], ['USD 12.50', '12.5', 'USD', 'currency'], ['人民币 12', '12', 'CNY', 'currency'],
    ['12 RMB', '12', 'CNY', 'currency'], ['12 元', '12', '元', 'currency'], ['-.50 kg', '-0.5', 'kg', 'mass'],
    ['+1.250 千克', '1.25', 'kg', 'mass'], ['-0.000 克', '0', 'g', 'mass'], ['.025 g', '0.025', 'g', 'mass'],
    ['1.5 min', '1.5', 'min', 'duration'], ['2小时', '2', 'h', 'duration'], ['12天', '12', '天', 'calendar_day'],
    ['2年', '2', '年', 'calendar_year'], ['3月', '3', '月', 'calendar_month'],
  ]) assert.deepEqual(parseQuantity(raw), { raw, supported: true, value, unit, dimension });
});

test('parser never turns bounds, locales, currency symbols, ranges or implicit units into scalars', () => {
  const unsupported = ['100', '¥100', '$12', '￥100', '1,234 g', '1.234,50 EUR', '1e3 g', '1E-3 kg', '1.2.3 g',
    '100-200元', '100–200元', '100 至 200元', '100元到200元', '约12天', '至少12天', '≥12 g', '<12 kg', '12天以上',
    '5%', '5％', '10 g/s', '每月100元', 'not 24天', '不是24天', 'NaN g', 'Infinity g', '100 克 和 2 元', '2026-02-03', '300 PPI'];
  for (const raw of unsupported) assert.equal(parseQuantity(raw).supported, false, raw);
  assert.equal(parseQuantity(null).reason, 'text_required');
  assert.equal(parseQuantity(`${'9'.repeat(81)}g`).reason, 'number_out_of_bounds');
});

test('0.1 plus 0.2 is exactly 0.3 and decimal arithmetic survives values above IEEE precision', () => {
  const store = new EvidenceStore();
  const refs = quantities(store, ['CNY 0.1', 'RMB 0.2', 'CNY 9007199254740993.01', '人民币 0.09'], 'money', 1, ['价格', '价格', '价格', '价格']);
  assert.deepEqual(calculate(store, 'sum', refs.slice(0, 2)).result, { value: '0.3', unit: 'CNY' });
  assert.deepEqual(calculate(store, 'sum', refs.slice(2)).result, { value: '9007199254740993.1', unit: 'CNY' });
  assert.deepEqual(calculate(store, 'difference', refs.slice(0, 2)).result, { value: '-0.1', unit: 'CNY', order: 'first_minus_second' });
});

test('g/kg and s/min/h conversions are exact; comparison identifies direction and difference identifies operand order', () => {
  const store = new EvidenceStore(), weight = quantities(store, ['1800 g', '1.4 kg']);
  assert.deepEqual(calculate(store, 'difference', weight).result, { value: '400', unit: 'g', order: 'first_minus_second' });
  assert.deepEqual(calculate(store, 'compare', weight).result, { relation: 'greater', unit: 'g' });
  const time = quantities(store, ['0.1 h', '6分钟', '360秒'], 'times', 1, ['续航', '续航', '续航']);
  assert.deepEqual(calculate(store, 'sum', time).result, { value: '1080', unit: 's' });
  assert.deepEqual(calculate(store, 'compare', time.slice(0, 2)).result, { relation: 'equal', unit: 's' });
});

test('sort groups equal values instead of manufacturing a unique order', () => {
  const store = new EvidenceStore(), refs = quantities(store, ['1 kg', '999 g', '1000 g', '-.001 kg']);
  const result = calculate(store, 'sort', refs).result;
  assert.equal(result.unit, 'g');
  assert.deepEqual(result.ascending_groups, [
    { value: '-1', quantity_refs: [refs[3].quantity_ref] },
    { value: '999', quantity_refs: [refs[1].quantity_ref] },
    { value: '1000', quantity_refs: [refs[0].quantity_ref, refs[2].quantity_ref] },
  ]);
});

test('calendar units and currency identities remain distinct rather than relying on hidden assumptions', () => {
  const store = new EvidenceStore(), refs = quantities(store, ['24天', '12天', '2年', '24月', '288 h', 'USD 2', 'EUR 2', 'CNY 2', '2元'], 'calendar', 1,
    ['操作日志', '备份', '归档', '归档', '运行时长', '价格', '价格', '价格', '价格']);
  assert.equal(calculate(store, 'compare', refs.slice(0, 2)).result.relation, 'greater');
  for (const pair of [[refs[0], refs[2]], [refs[2], refs[3]], [refs[0], refs[4]], [refs[5], refs[6]], [refs[7], refs[8]]]) {
    const result = calculate(store, 'compare', pair);
    assert.equal(result.status, 'unsupported'); assert.equal(result.reason, 'incompatible_units'); assert.equal(result.result, undefined);
  }
  const years = quantities(store, ['2年', '3年'], 'years', 1, ['归档', '归档']);
  assert.equal(calculate(store, 'difference', years).result.value, '-1');
});

test('table sources retain blank cells, original row paths and one explicit header binding without guessing entity IDs', () => {
  const evidence = { schema: 'dom-evidence-v1', definitions: [], headings: [], paragraphs: [], tables: [{ source_id: 'table-0', column_mapping: 'simple',
    header_rows: [{ source_id: 'header', cells: [{ source_id: 'h0', text: '名称' }, { source_id: 'h1', text: '价格' }, { source_id: 'h2', text: '重量' }] }],
    rows: [{ source_id: 'row0', cells: [{ source_id: 'c0', text: '' }, { source_id: 'c1', text: 'CNY 123.45' }, { source_id: 'c2', text: '385 g' }] }],
  }] };
  const result = new EvidenceStore().record(evidence, provenance());
  assert.equal(result.tables[0].rows[0].cells[0].text, '');
  assert.deepEqual(result.quantities.map(({ source_id, path, field, value }) => ({ source_id, path, field, value })), [
    { source_id: 'c1', path: ['tables', 0, 'rows', 0, 'cells', 1], field: '价格', value: '123.45' },
    { source_id: 'c2', path: ['tables', 0, 'rows', 0, 'cells', 2], field: '重量', value: '385' },
  ]);
  assert.ok(result.quantities.every(value => !('entity' in value) && /^q_[a-f0-9]{36}$/.test(value.quantity_ref)));
});

test('unknown column mappings and multiple DL terms never receive invented field bindings', () => {
  const store = new EvidenceStore(), evidence = definitions(['100 g']);
  evidence.definitions[0].groups[0].terms.push({ source_id: 'extra-term', text: '价格' });
  evidence.tables = [{ column_mapping: 'unknown', header_rows: [{ cells: [{ text: '重量' }] }], rows: [{ cells: [{ source_id: 'span-cell', text: '100 g' }] }] }];
  const values = store.record(evidence, provenance()).quantities;
  assert.equal(values.length, 2);
  assert.ok(values.every(value => value.supported === false && value.reason === 'field_binding_unknown'));
  assert.equal(calculate(store, 'sum', values).status, 'unsupported');
});

test('paragraph quantities need an observed heading and exactly one explicit value, retaining the entire excerpt', () => {
  const store = new EvidenceStore(), evidence = { schema: 'dom-evidence-v1', tables: [], definitions: [],
    headings: [{ source_id: 'logs', text: '操作日志', level: 2 }],
    paragraphs: [
      { source_id: 'p0', text: '操作日志保留24天，之后自动清理。', heading_source_id: 'logs' },
      { source_id: 'p1', text: '保留12天。', heading: '模型可以伪造这个字符串' },
      { source_id: 'p2', text: '日志24天，备份12天。', heading_source_id: 'logs' },
      { source_id: 'p3', text: '内存100gb。', heading_source_id: 'logs' },
      { source_id: 'p4', text: '记录第5项，重量100g。', heading_source_id: 'logs' },
    ] };
  const values = store.record(evidence, provenance()).quantities;
  assert.equal(values[0].supported, true); assert.equal(values[0].raw, '24天'); assert.equal(values[0].excerpt, evidence.paragraphs[0].text);
  assert.equal(values[1].reason, 'field_binding_unknown');
  assert.ok(values.slice(2).every(value => !value.supported));
  assert.ok(!values.some(value => value.source_id === 'p3'), 'unrecognized gb must not be shortened into g');
});

test('bare IDs, model numbers and ISO dates do not get quantity refs or exhaust their budget', () => {
  const store = new EvidenceStore({ maxQuantitiesPerObservation: 1 });
  const record = store.record(definitions(['123', 'P-166', '型号P300', '2026-11-12', '123ABC', '385 g'], ['编号', '编号', '型号', '截止日期', '名称', '重量']), provenance());
  assert.equal(record.quantities.length, 1); assert.equal(record.quantities[0].value, '385');
  assert.equal(record.quantities_truncated, false);
  assert.equal(record.quantity_capture_scope, 'explicit_units_in_bound_fields_only');
  assert.equal(record.definitions[0].groups[0].descriptions[0].text, '123');
});

test('truncated cell/dd/paragraph text is never treated as an exact scalar', () => {
  const store = new EvidenceStore(), evidence = definitions(['100 g']);
  evidence.definitions[0].groups[0].descriptions[0].text_truncated = true;
  evidence.tables = [{ column_mapping: 'simple', header_rows: [{ cells: [{ text: '重量' }] }],
    rows: [{ cells: [{ source_id: 'truncated-cell', text: '1 kg', truncated: true }] }] }];
  evidence.headings = [{ source_id: 'heading', text: '日志' }];
  evidence.paragraphs = [{ source_id: 'truncated-p', text: '保留24天', heading_source_id: 'heading', text_truncated: true }];
  const refs = store.record(evidence, provenance()).quantities;
  assert.equal(refs.length, 3);
  assert.ok(refs.every(ref => !ref.supported && ref.reason === 'source_text_truncated'));
  assert.equal(calculate(store, 'sort', refs).status, 'unsupported');
});

test('truncated field labels never create a supported binding for complete values', () => {
  const store = new EvidenceStore(), evidence = definitions(['100 g']);
  evidence.definitions[0].groups[0].terms[0].text_truncated = true;
  evidence.tables = [{ column_mapping: 'simple', header_rows: [{ cells: [{ text: '重量', text_truncated: true }] }],
    rows: [{ cells: [{ source_id: 'complete-cell', text: '1 kg' }] }] }];
  evidence.headings = [{ source_id: 'heading', text: '日志', text_truncated: true }];
  evidence.paragraphs = [{ source_id: 'complete-p', text: '保留24天', heading_source_id: 'heading' }];
  const refs = store.record(evidence, provenance()).quantities;
  assert.equal(refs.length, 3);
  assert.ok(refs.every(ref => !ref.supported && ref.reason === 'field_binding_unknown'));
});

test('scientific notation with an explicit unit remains an unsupported quantity, not a shorter suffix scalar', () => {
  const store = new EvidenceStore(), refs = quantities(store, ['1e3 g', '1E-3 kg']);
  assert.equal(refs.length, 2);
  assert.ok(refs.every(ref => !ref.supported && ref.reason === 'scientific_notation_not_supported'));
});

test('paragraph scalar matching respects ASCII words, identifier separators and complete signs', () => {
  const rejected = ['10 hardware errors', '5 gramsomething', 'T-19CNY', 'T-USD19', 'T-19 kg', 'T+19CNY', 'T+USD19',
    'T_19CNY', '--19CNY', '++19kg', 'CNY19Thing'];
  const accepted = [['重量 -19 kg', '-19', 'kg'], ['重量+19 kg', '19', 'kg'], ['Weight 10 h.', '10', 'h'],
    ['净重5 g。', '5', 'g'], ['售后预算CNY 19', '19', 'CNY'], ['保留24天', '24', '天']];
  for (const [index, text] of rejected.entries()) {
    const store = new EvidenceStore();
    const evidence = { schema: 'dom-evidence-v1', tables: [], definitions: [], headings: [{ source_id: 'heading', text: '字段' }],
      paragraphs: [{ source_id: 'paragraph', text, heading_source_id: 'heading' }] };
    const refs = store.record(evidence, provenance(`bad-${index}`)).quantities;
    assert.ok(refs.every(ref => !ref.supported), text);
  }
  for (const [index, [text, value, unit]] of accepted.entries()) {
    const store = new EvidenceStore();
    const evidence = { schema: 'dom-evidence-v1', tables: [], definitions: [], headings: [{ source_id: 'heading', text: '字段' }],
      paragraphs: [{ source_id: 'paragraph', text, heading_source_id: 'heading' }] };
    const refs = store.record(evidence, provenance(`good-${index}`)).quantities;
    assert.equal(refs.length, 1, text); assert.equal(refs[0].supported, true, text);
    assert.equal(refs[0].value, value, text); assert.equal(refs[0].unit, unit, text);
  }
});

test('unsupported operands do not get partial totals or forced rankings', () => {
  const store = new EvidenceStore(), refs = quantities(store, ['100 g', '至少200 g', '100–200 g']);
  for (const operation of ['sort', 'sum']) {
    const result = calculate(store, operation, refs);
    assert.equal(result.status, 'unsupported'); assert.equal(result.reason, 'source_quantity_not_supported'); assert.equal(result.result, undefined);
  }
});

test('source-backed values and provenance are immutable copies; inherited forged quantity metadata is removed', () => {
  const store = new EvidenceStore(), input = definitions(['100 g']), origin = provenance();
  input.quantities = [{ quantity_ref: 'q_forged', value: '9999' }]; input.provenance = { generation: 999 };
  input.observed_content_sha256 = 'forged'; input.data = { provenance: { generation: 999 }, quantities: [{ quantity_ref: 'q_forged' }] };
  const recorded = store.record(input, origin), ref = recorded.quantities[0];
  input.definitions[0].groups[0].descriptions[0].text = '999 g'; origin.generation = 2;
  assert.throws(() => { ref.value = '888'; }, TypeError);
  assert.throws(() => { recorded.provenance.url = 'https://fake.test'; }, TypeError);
  const result = calculate(store, 'sum', [ref]);
  assert.equal(result.result.value, '100'); assert.equal(result.sources[0].excerpt, '100 g');
  assert.equal(result.sources[0].provenance.url, 'https://example.test/obs-1');
  assert.equal(result.sources[0].provenance.frame_url, 'https://example.test/frame');
  assert.equal(result.sources[0].provenance.version, '1:t1:1:0:observed-dom-revision');
  const hashInput = structuredClone(input);
  hashInput.definitions[0].groups[0].descriptions[0].text = '100 g';
  delete hashInput.quantities; delete hashInput.provenance; delete hashInput.observed_content_sha256;
  assert.equal(recorded.observed_content_sha256, createHash('sha256').update(JSON.stringify(hashInput)).digest('hex'));
  assert.equal(result.sources[0].observed_content_sha256, recorded.observed_content_sha256);
  assert.equal(store.calculate({ operation: 'sum', quantity_refs: ['q_forged'] }, 1).reason, 'unknown_quantity_ref');
});

test('unknown, foreign-session and changed-generation refs are rejected and cannot be replaced by raw numbers', () => {
  const one = new EvidenceStore(), two = new EvidenceStore(), refs = quantities(one, ['100 g', '200 g']);
  assert.equal(two.calculate({ operation: 'sum', quantity_refs: refs.map(value => value.quantity_ref) }, 1).reason, 'unknown_quantity_ref');
  assert.equal(calculate(one, 'sum', refs, 2).reason, 'quantity_generation_changed');
  assert.equal(one.calculate({ operation: 'sum', quantity_refs: [refs[0].quantity_ref], values: [999] }, 1).reason, 'invalid_calculation_fields');
  assert.equal(one.calculate({ operation: 'sum', quantity_refs: ['e1'] }, 1).reason, 'unknown_quantity_ref');
  assert.equal(one.calculate({ operation: 'sum', quantity_refs: ['p1'] }, 1).reason, 'unknown_quantity_ref');
  assert.equal(one.calculate({ operation: 'sum', quantity_refs: [refs[0].quantity_ref, refs[0].quantity_ref] }, 1).status, 'error');
});

test('navigation history remains readable with explicit as-observed scope, without certifying current state or whole answers', () => {
  const store = new EvidenceStore(), a = quantities(store, ['100 g'], 'product-a');
  const b = store.record(definitions(['1 kg']), provenance('product-b', 1, 2)).quantities;
  const result = calculate(store, 'difference', [...b, ...a]);
  assert.equal(result.status, 'calculated'); assert.equal(result.result.value, '900');
  assert.equal(result.scope, 'as_observed'); assert.equal(result.current_state_verified, false); assert.equal(result.verifies_entire_answer, false);
  assert.equal(result.sources[0].provenance.document_version, 2);
  assert.equal(result.sources[1].provenance.observation_id, 'product-a');
  assert.ok(result.sources.every(source => source.path.length > 0 && source.source_id && source.excerpt));
});

test('bounded observation retention evicts complete old sources and clear invalidates all refs', () => {
  const store = new EvidenceStore({ maxObservations: 2 });
  const first = quantities(store, ['1 g'], 'a'), second = quantities(store, ['2 g'], 'b');
  const third = quantities(store, ['3 g'], 'c');
  assert.equal(calculate(store, 'sum', first).reason, 'unknown_quantity_ref');
  assert.equal(calculate(store, 'sum', [...second, ...third]).result.value, '5');
  store.clear();
  assert.deepEqual(store.stats, { observations: 0, quantities: 0 });
  assert.equal(calculate(store, 'sum', second).reason, 'unknown_quantity_ref');
  assert.equal(calculate(store, 'sum', third).reason, 'unknown_quantity_ref');
  const replacement = quantities(store, ['1 g'], 'a');
  assert.notEqual(replacement[0].quantity_ref, first[0].quantity_ref);
});

test('observations have at most 100 quantity refs and calculations accept at most 16 sources', () => {
  const store = new EvidenceStore();
  const result = store.record(definitions(Array.from({ length: 110 }, (_, index) => `${index} g`)), provenance());
  assert.equal(result.quantities.length, 100); assert.equal(result.quantities_truncated, true);
  assert.equal(calculate(store, 'sum', result.quantities.slice(0, 16)).result.value, '120');
  assert.equal(calculate(store, 'sum', result.quantities.slice(0, 17)).status, 'error');
  for (const options of [{ maxObservations: 33 }, { maxObservations: 0 }, { maxQuantitiesPerObservation: 101 }]) assert.throws(() => new EvidenceStore(options));
  assert.throws(() => store.record(definitions(['1 g']), provenance()), /already recorded/);
  assert.throws(() => store.record({}, provenance()), /dom-evidence-v1/);
});
