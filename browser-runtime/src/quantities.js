import { createHash, randomBytes } from 'node:crypto';

// This module proves what a bounded DOM observation said and performs exact
// arithmetic on those observed values. It does not prove a site's truth,
// entity identity, complete pagination, or the accuracy of an entire answer.
const UNITS = new Map([
  ['元', { unit: '元', dimension: 'currency', base: '元', factor: 1n }],
  ...['CNY', 'RMB', '人民币'].map(alias => [alias, { unit: 'CNY', dimension: 'currency', base: 'CNY', factor: 1n }]),
  ...['USD', 'EUR'].map(alias => [alias, { unit: alias, dimension: 'currency', base: alias, factor: 1n }]),
  ...['g', '克'].map(alias => [alias, { unit: 'g', dimension: 'mass', base: 'g', factor: 1n }]),
  ...['kg', '千克'].map(alias => [alias, { unit: 'kg', dimension: 'mass', base: 'g', factor: 1000n }]),
  ...['s', '秒'].map(alias => [alias, { unit: 's', dimension: 'duration', base: 's', factor: 1n }]),
  ...['min', '分钟'].map(alias => [alias, { unit: 'min', dimension: 'duration', base: 's', factor: 60n }]),
  ...['h', '小时'].map(alias => [alias, { unit: 'h', dimension: 'duration', base: 's', factor: 3600n }]),
  ['天', { unit: '天', dimension: 'calendar_day', base: '天', factor: 1n }],
  ['月', { unit: '月', dimension: 'calendar_month', base: '月', factor: 1n }],
  ['年', { unit: '年', dimension: 'calendar_year', base: '年', factor: 1n }],
]);
const NUMBER = '[+-]?(?:\\d+(?:\\.\\d+)?|\\.\\d+)';
const ALIAS = [...UNITS.keys()].sort((a, b) => b.length - a.length).join('|');
const SUFFIX = new RegExp(`^(${NUMBER})\\s*(${ALIAS})$`, 'u');
const PREFIX = new RegExp(`^(${ALIAS})\\s*(${NUMBER})$`, 'u');
// Include signs in the left fence: after rejecting T-19CNY at its '-' start,
// matching must not restart at '19' and silently discard an identifier/sign.
const CANDIDATE = new RegExp(`(?<![A-Za-z0-9_.+-])(?:(?:${NUMBER})\\s*(?:${ALIAS})(?![A-Za-z])|(?:${ALIAS})\\s*(?:${NUMBER})(?![A-Za-z0-9_.]))`, 'gu');
const UNIT_MARKER = /(?:^|[^A-Za-z])(?:kg|g|min|s|h|CNY|RMB|USD|EUR)(?![A-Za-z])|千克|克|分钟|秒|小时|天|月|年|人民币|元/u;
const MAX_TEXT = 4096;

function unsupported(raw, reason) { return { raw, supported: false, reason }; }
function formatDecimal(coefficient, scale) {
  const negative = coefficient < 0n;
  let digits = (negative ? -coefficient : coefficient).toString().padStart(scale + 1, '0');
  if (scale) digits = `${digits.slice(0, -scale)}.${digits.slice(-scale)}`.replace(/0+$/, '').replace(/\.$/, '');
  return `${negative && coefficient !== 0n ? '-' : ''}${digits}`;
}
function decimal(text) {
  const negative = text.startsWith('-');
  const unsigned = text.replace(/^[+-]/, '');
  const [whole, fraction = ''] = unsigned.split('.');
  const coefficient = BigInt(`${whole || '0'}${fraction}`) * (negative ? -1n : 1n);
  return { coefficient, scale: fraction.length };
}
function qualifier(text) {
  if (/[\$¥￥]/u.test(text)) return 'ambiguous_currency_symbol';
  if (/%|％/u.test(text)) return 'percentage_not_supported';
  if (/\d[,.，]\d*[,.，]\d|\d[,，]\d/u.test(text)) return 'ambiguous_number_separator';
  if (/[+-]?(?:\d+(?:\.\d+)?|\.\d+)[eE][+-]?\d/u.test(text)) return 'scientific_notation_not_supported';
  if (/(?:\d|克|g|元|年|月|天|小时|分钟)\s*[-–—~～至到]\s*[+-]?\d/u.test(text)) return 'range_not_supported';
  if (/[<>≤≥≈~～]|(?:大约|约|大概|至少|至多|不低于|不超过|以上|以下|超过|少于|不足|最多|最少|起步|起价)|\b(?:about|approximately|at least|at most|more than|less than)\b/iu.test(text)) return 'qualified_value_not_supported';
  if (/(?:不是|并非|不为|不等于)|\b(?:not|isn't|is not)\b/iu.test(text)) return 'negated_value_not_supported';
  if (/每|\/|\bper\b/iu.test(text)) return 'composite_unit_not_supported';
  return null;
}

// A scalar must contain one explicit supported unit and nothing else. In
// particular bare digits, ambiguous symbols, bounds, locales and percentages
// never become exact quantities by silently discarding their punctuation.
export function parseQuantity(input) {
  if (typeof input !== 'string') return unsupported('', 'text_required');
  const raw = input;
  const text = raw.trim();
  if (!text || text.length > MAX_TEXT) return unsupported(raw, 'text_out_of_bounds');
  const reason = qualifier(text);
  if (reason) return unsupported(raw, reason);
  const suffix = SUFFIX.exec(text), prefix = PREFIX.exec(text);
  if (!suffix && !prefix) return unsupported(raw, 'not_one_explicit_scalar');
  const valueText = suffix ? suffix[1] : prefix[2];
  const alias = suffix ? suffix[2] : prefix[1];
  if (valueText.replace(/\D/g, '').length > 80) return unsupported(raw, 'number_out_of_bounds');
  const exact = decimal(valueText), unit = UNITS.get(alias);
  return { raw, supported: true, value: formatDecimal(exact.coefficient, exact.scale), unit: unit.unit, dimension: unit.dimension };
}

function oneQuantity(text, { paragraph = false, field = '' } = {}) {
  if (typeof text !== 'string' || !/\d/u.test(text)) return null;
  // Record IDs, bare numbers, model names and ISO dates stay in their DOM
  // record. They do not consume the quantity budget or gain a numeric ref.
  CANDIDATE.lastIndex = 0;
  const hasUnit = CANDIDATE.test(text) || UNIT_MARKER.test(text) || /[%％$¥￥]|\b(?:PPI|Hz|MB|MiB|GB|GiB)\b|°[CF]/u.test(text);
  CANDIDATE.lastIndex = 0;
  if (!hasUnit) return null;
  if (!field) return unsupported(text, 'field_binding_unknown');
  if (text.length > MAX_TEXT) return unsupported(text.slice(0, MAX_TEXT), 'text_out_of_bounds');
  const reason = qualifier(text);
  if (reason) return unsupported(text, reason);
  if (!paragraph) return parseQuantity(text);
  const candidates = [...text.matchAll(CANDIDATE)];
  // Any other digits may be a record ID, another fact, or a date. Do not join
  // facts by their order of appearance or guess which number a heading means.
  if (candidates.length !== 1 || [...text.matchAll(/[+-]?(?:\d+(?:\.\d+)?|\.\d+)/gu)].length !== 1) {
    return unsupported(text, 'not_one_explicit_scalar');
  }
  return parseQuantity(candidates[0][0]);
}
function deepFreeze(value) {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(deepFreeze);
    Object.freeze(value);
  }
  return value;
}
function boundedString(value, name, maximum = 4096, allowEmpty = false) {
  if (typeof value !== 'string' || (!value && !allowEmpty) || value.length > maximum) throw new TypeError(`invalid ${name}`);
  return value;
}
function validateProvenance(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new TypeError('provenance required');
  const observation_id = boundedString(value.observation_id, 'observation_id', 256);
  if (!Number.isSafeInteger(value.generation) || value.generation < 1) throw new TypeError('invalid generation');
  if (!Number.isSafeInteger(value.document_version) || value.document_version < 0) throw new TypeError('invalid document_version');
  return {
    observation_id, generation: value.generation, tab: boundedString(value.tab, 'tab', 128),
    document_version: value.document_version, version: boundedString(value.version, 'version', 512), observed_at: boundedString(value.observed_at, 'observed_at', 128),
    url: boundedString(value.url, 'url'), ...(value.frame_url !== undefined ? { frame_url: boundedString(value.frame_url, 'frame_url') } : {}),
  };
}
function sourceValues(evidence) {
  const values = [];
  for (const [tableIndex, table] of (evidence.tables ?? []).entries()) {
    const header = table.column_mapping === 'simple' && table.header_rows?.length === 1 ? table.header_rows[0].cells : [];
    for (const [rowIndex, row] of (table.rows ?? []).entries()) {
      for (const [cellIndex, cell] of (row.cells ?? []).entries()) {
        const heading = header[cellIndex];
        const field = heading && !heading.text_truncated && !heading.truncated ? heading.text : '';
        values.push({ source_id: cell.source_id, path: ['tables', tableIndex, 'rows', rowIndex, 'cells', cellIndex], field,
          text: cell.text, excerpt: cell.text, binding: field ? 'table_column' : 'unknown', text_truncated: cell.text_truncated === true || cell.truncated === true });
      }
    }
  }
  for (const [definitionIndex, definition] of (evidence.definitions ?? []).entries()) {
    for (const [groupIndex, group] of (definition.groups ?? []).entries()) {
      // Several terms/descriptions are a valid DL, but not an unambiguous
      // one-to-one field. Preserve it, without inventing a binding.
      const field = group.terms?.length === 1 && group.descriptions?.length === 1 && !group.terms[0].text_truncated && !group.terms[0].truncated ? group.terms[0].text : '';
      for (const [descriptionIndex, description] of (group.descriptions ?? []).entries()) {
        values.push({ source_id: description.source_id, path: ['definitions', definitionIndex, 'groups', groupIndex, 'descriptions', descriptionIndex], field,
          text: description.text, excerpt: description.text, binding: field ? 'definition_pair' : 'unknown', text_truncated: description.text_truncated === true || description.truncated === true });
      }
    }
  }
  for (const [index, paragraph] of (evidence.paragraphs ?? []).entries()) {
    const heading = (evidence.headings ?? []).find(item => item.source_id === paragraph.heading_source_id);
    const field = heading && !heading.text_truncated && !heading.truncated ? heading.text : '';
    values.push({ source_id: paragraph.source_id, path: ['paragraphs', index], field, text: paragraph.text,
      excerpt: paragraph.text, binding: field ? 'section_heading' : 'unknown', paragraph: true, text_truncated: paragraph.text_truncated === true || paragraph.truncated === true });
  }
  return values;
}
function canonical(quantity) {
  const unit = UNITS.get(quantity.unit);
  const exact = decimal(quantity.value);
  return { ...exact, coefficient: exact.coefficient * unit.factor, base: unit.base, dimension: unit.dimension };
}
function aligned(values) {
  const scale = Math.max(...values.map(value => value.scale));
  return { scale, coefficients: values.map(value => value.coefficient * 10n ** BigInt(scale - value.scale)) };
}

export class EvidenceStore {
  #observations = new Map();
  #quantities = new Map();
  #maxObservations;
  #maxQuantitiesPerObservation;

  constructor({ maxObservations = 32, maxQuantitiesPerObservation = 100 } = {}) {
    if (!Number.isSafeInteger(maxObservations) || maxObservations < 1 || maxObservations > 32) throw new TypeError('maxObservations must be 1..32');
    if (!Number.isSafeInteger(maxQuantitiesPerObservation) || maxQuantitiesPerObservation < 1 || maxQuantitiesPerObservation > 100) throw new TypeError('maxQuantitiesPerObservation must be 1..100');
    this.#maxObservations = maxObservations;
    this.#maxQuantitiesPerObservation = maxQuantitiesPerObservation;
  }

  record(evidence, suppliedProvenance) {
    if (!evidence || evidence.schema !== 'dom-evidence-v1') throw new TypeError('dom-evidence-v1 required');
    const provenance = validateProvenance(suppliedProvenance);
    if (this.#observations.has(provenance.observation_id)) throw new TypeError('observation_id already recorded');
    const copy = structuredClone(evidence);
    // These fields are worker-owned outputs, never inherited inputs.
    delete copy.quantities; delete copy.provenance; delete copy.observed_content_sha256;
    const observed_content_sha256 = createHash('sha256').update(JSON.stringify(copy)).digest('hex');
    const quantities = [], entries = [];
    let available = 0;
    for (const source of sourceValues(copy)) {
      let parsed = oneQuantity(source.text, source);
      if (!parsed) continue;
      if (source.text_truncated) parsed = unsupported(source.text.slice(0, MAX_TEXT), 'source_text_truncated');
      available++;
      if (quantities.length >= this.#maxQuantitiesPerObservation) continue;
      boundedString(source.source_id, 'source_id', 1024);
      const quantity_ref = `q_${randomBytes(18).toString('hex')}`;
      const quantity = { quantity_ref, source_id: source.source_id, path: source.path,
        field: source.field.slice(0, 512), binding: source.binding, excerpt: source.excerpt.slice(0, MAX_TEXT), ...parsed };
      quantities.push(quantity);
      entries.push([quantity_ref, deepFreeze({ quantity, provenance, observed_content_sha256 })]);
    }
    const result = deepFreeze({ ...copy, provenance, observed_content_sha256, quantities, quantities_truncated: available > quantities.length,
      quantity_capture_scope: 'explicit_units_in_bound_fields_only' });
    this.#observations.set(provenance.observation_id, { result, refs: entries.map(([ref]) => ref) });
    for (const [ref, entry] of entries) this.#quantities.set(ref, entry);
    while (this.#observations.size > this.#maxObservations) {
      const oldest = this.#observations.keys().next().value;
      for (const ref of this.#observations.get(oldest).refs) this.#quantities.delete(ref);
      this.#observations.delete(oldest);
    }
    return result;
  }

  calculate(args, generation) {
    const base = { scope: 'as_observed', current_state_verified: false, verifies_entire_answer: false };
    const error = reason => ({ ...base, status: 'error', reason });
    if (!args || typeof args !== 'object' || Array.isArray(args) || Object.keys(args).some(key => !['operation', 'quantity_refs'].includes(key))) return error('invalid_calculation_fields');
    const { operation, quantity_refs: refs } = args;
    if (!['compare', 'sort', 'difference', 'sum'].includes(operation)) return error('unsupported_operation');
    if (!Array.isArray(refs) || !refs.length || refs.length > 16 || refs.some(ref => typeof ref !== 'string') || new Set(refs).size !== refs.length) return error('quantity_refs_require_1_to_16_unique_refs');
    if (['compare', 'difference'].includes(operation) && refs.length !== 2) return error('operation_requires_two_refs');
    if (!Number.isSafeInteger(generation) || generation < 1) return error('invalid_generation');
    const entries = refs.map(ref => this.#quantities.get(ref));
    if (entries.some(entry => !entry)) return error('unknown_quantity_ref');
    if (entries.some(entry => entry.provenance.generation !== generation)) return error('quantity_generation_changed');
    const sources = entries.map(({ quantity, provenance, observed_content_sha256 }) => ({ ...quantity, provenance, observed_content_sha256 }));
    if (sources.some(source => !source.supported)) return { ...base, status: 'unsupported', operation, reason: 'source_quantity_not_supported', sources };
    const values = sources.map(canonical);
    if (values.some(value => value.base !== values[0].base || value.dimension !== values[0].dimension)) {
      return { ...base, status: 'unsupported', operation, reason: 'incompatible_units', sources };
    }
    const { coefficients, scale } = aligned(values), unit = values[0].base;
    let result;
    if (operation === 'compare') {
      result = { relation: coefficients[0] < coefficients[1] ? 'less' : coefficients[0] > coefficients[1] ? 'greater' : 'equal', unit };
    } else if (operation === 'difference') {
      result = { value: formatDecimal(coefficients[0] - coefficients[1], scale), unit, order: 'first_minus_second' };
    } else if (operation === 'sum') {
      result = { value: formatDecimal(coefficients.reduce((total, value) => total + value, 0n), scale), unit };
    } else {
      const sorted = refs.map((ref, index) => ({ ref, value: coefficients[index] })).sort((a, b) => a.value < b.value ? -1 : a.value > b.value ? 1 : 0);
      const groups = [];
      for (const item of sorted) {
        const previous = groups.at(-1);
        if (previous?.value === item.value) previous.refs.push(item.ref);
        else groups.push({ value: item.value, refs: [item.ref] });
      }
      result = { ascending_groups: groups.map(group => ({ value: formatDecimal(group.value, scale), quantity_refs: group.refs })), unit };
    }
    return deepFreeze({ ...base, status: 'calculated', operation, sources, result });
  }

  clear() { this.#observations.clear(); this.#quantities.clear(); }

  // Read-only diagnostics expose counts, never the maps that issue authority
  // to a quantity ref. Useful for checking rejected captures and cleanup.
  get stats() { return Object.freeze({ observations: this.#observations.size, quantities: this.#quantities.size }); }
}
