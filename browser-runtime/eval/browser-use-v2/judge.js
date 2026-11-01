// This judge accepts only the runner's private recording-proxy exchange ledger.
// Model output, adapter events and self-reported evidence are never a ledger.
import { isDeepStrictEqual } from 'node:util';
import { nativeDOMProof } from './dom-evidence.js';

const TARGET = /^dt_[0-9a-f]{24}_[1-9][0-9]*$/;
const REQUEST = /^[0-9a-f]{24}:[^:\s]{1,80}:[1-9][0-9]*:[1-9][0-9]*$/;
const MAX_ANSWER = 16_384;
const plain = value => Boolean(value && typeof value === 'object' && !Array.isArray(value));
const array = value => Array.isArray(value) ? value : [];
const canonical = value => Array.isArray(value) ? value.map(canonical) : plain(value) ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const same = (left, right) => isDeepStrictEqual(canonical(left), canonical(right));
const parse = value => { if (typeof value !== 'string') return value; try { return JSON.parse(value); } catch { return value; } };
const escape = value => String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const normalized = value => String(value).normalize('NFKC').toLowerCase().replace(/(?<=\d),(?=\d{3}(?:\D|$))/g, '').replace(/\s+/g, ' ');
const fieldsMatch = (value, fields) => plain(value) && Object.entries(fields ?? {}).every(([key, expected]) => same(value[key], expected));
const targetURL = (target, expected) => !expected || target.url === expected;
const responseStatus = entry => entry?.status ?? entry?.response?.status;
const remoteValue = value => plain(value) && Object.hasOwn(value, 'value') ? parse(value.value) : undefined;
function issuedId(item, owner) {
  if (!REQUEST.test(item?.request_id ?? '') || item.id !== item.request_id) return false;
  const [namespace, tab, serial] = item.request_id.split(':');
  const [, targetNamespace, targetSerial] = owner.target_ref.split('_');
  return namespace === targetNamespace && serial === targetSerial && tab === item.tab_id;
}

function answerCheck(answer, facts) {
  const raw = typeof answer === 'string' ? answer : plain(answer) ? JSON.stringify(answer) : '';
  if (!raw || raw.length > MAX_ANSWER) return { success: false, needs_review: raw.length > MAX_ANSWER, method: 'bounded_literal_facts', missing: ['answer'], conflicts: [] };
  const text = normalized(raw);
  const missing = [], conflicts = [];
  for (const fact of facts) {
    const expected = normalized(typeof fact.value === 'object' ? JSON.stringify(fact.value) : fact.value);
    const number = typeof fact.value === 'number';
    const pattern = number ? new RegExp(`(?<![\\d.])${escape(expected)}(?![\\d.])`, 'u')
      : new RegExp(`${/^[a-z0-9]/.test(expected) ? '(?<![a-z0-9_.-])' : ''}${escape(expected)}${/[a-z0-9]$/.test(expected) ? '(?![a-z0-9_.-])' : ''}`, 'u');
    if (!pattern.test(text)) missing.push(fact.name);
    if (new RegExp(`(?:\\bnot|不是|并非|不等于)\\s*["']?${escape(expected)}(?:["']|\\b|$)`, 'u').test(text)) conflicts.push(fact.name);
    // This is deliberately not a general semantic evaluator. Reject explicit
    // contradictory labeled values; retain a finite literal-fact boundary.
    for (const alias of [fact.name, ...array(fact.aliases)].filter(Boolean)) {
      const label = normalized(alias);
      const matches = text.matchAll(new RegExp(`${escape(label)}\\s*(?::|=|是|为|\\bis\\b)\\s*["']?(-?\\d+(?:\\.\\d+)?|[a-z][a-z0-9_.-]*)`, 'gu'));
      for (const match of matches) {
        const actual = match[1];
        if (number ? Number.isFinite(Number(actual)) && Number(actual) !== fact.value : actual !== expected && /^[a-z0-9_.-]+$/.test(expected)) conflicts.push(fact.name);
      }
    }
  }
  return { success: !missing.length && !conflicts.length, needs_review: false, method: 'bounded_literal_facts', missing: [...new Set(missing)], conflicts: [...new Set(conflicts)] };
}

function validAck(exchange) {
  return exchange?.ok === true && Number.isInteger(exchange.http_status) && exchange.http_status >= 200 && exchange.http_status < 300 && exchange.result?.ok === true && plain(exchange.result.data);
}

function preparedFrameMatches(record, anchor) {
  if (!plain(anchor) || anchor.generation !== record.paused.generation || anchor.tab_id !== record.paused.tab_id
    || anchor.target_ref !== record.owner.target_ref || anchor.target_type !== record.owner.type || anchor.pause_seq !== record.paused.seq) return false;
  const frame = array(anchor.call_frames).find(value => value?.call_frame_id === record.frame.callFrameId && value.script_id === record.frame.location?.scriptId);
  const scopes = array(record.frame.scopeChain).filter(value => value.type === 'local').map(value => value.object?.objectId);
  return Boolean(frame && scopes.length > 0 && scopes.every(value => typeof value === 'string')
    && scopes.length === array(frame.local_scope_object_ids).length && scopes.every(value => frame.local_scope_object_ids.includes(value)));
}

/** Finite fixture judge; does not score arbitrary natural-language semantics. */
export function judgeCase({ task, oracle, fixtureState, ledger, finalAnswer }) {
  const failures = [];
  const fail = code => { if (!failures.includes(code)) failures.push(code); };
  if (!oracle || oracle.task_id !== task?.id) fail('ORACLE_TASK_MISMATCH');
  if (!oracle?.case_id || fixtureState?.case_id !== oracle.case_id) fail('FIXTURE_CASE_MISMATCH');
  const requirements = oracle?.requirements ?? {};
  const dom = nativeDOMProof({ task, oracle, fixtureState, ledger });
  const familyTargets = new Map();
  const latestGeneration = new Map();
  const issuedRequests = new Map();
  const eventRequests = new Map();
  const network = [], consoles = [], paused = new Map(), inspected = [], resumed = [];
  const objectOrigins = new Map(), seenEvents = new Set(), scriptURLs = new Map();
  const evidenceCalls = new Set();
  const exchanges = array(ledger);
  let validActions = 0;
  const familyKey = (exchange, provenance) => `${exchange.sessionId ?? ''}:${provenance.generation}:${provenance.tab_id}`;
  const observeRoot = (exchange, data) => {
    const provenance = data.provenance;
    if (provenance.target_type !== 'page' || !TARGET.test(provenance.target_ref ?? '') || typeof provenance.url !== 'string'
      || exchange.args.target !== undefined && exchange.args.target !== provenance.target_ref) return;
    const key = familyKey(exchange, provenance);
    let family = familyTargets.get(key);
    const oldRoot = family && [...family.values()].find(target => target.is_root);
    // A fresh root attachment invalidates the old descendant attachment set.
    if (!family || oldRoot?.target_ref !== provenance.target_ref) familyTargets.set(key, family = new Map());
    family.set(provenance.target_ref, { target_ref: provenance.target_ref, type: 'page', url: provenance.url, is_root: true, parent_target_ref: null, ready: true });
  };
  const resolveTarget = (exchange, data, ref, type) => {
    const provenance = data.provenance;
    if (!plain(provenance) || !Number.isSafeInteger(provenance.generation) || typeof provenance.tab_id !== 'string' || !TARGET.test(ref ?? '')) return null;
    const family = familyTargets.get(familyKey(exchange, provenance));
    const envelope = family?.get(provenance.target_ref);
    if (!envelope || envelope.type !== provenance.target_type || (exchange.args.target === undefined ? !envelope.is_root : exchange.args.target !== envelope.target_ref)) return null;
    const target = family?.get(ref);
    if (!target || target.ready !== true || target.type !== type || exchange.args.target !== undefined && exchange.args.target !== ref) return null;
    return { ...target, family: familyKey(exchange, provenance) };
  };
  const recordNetwork = (item, owner, text, exchange, index) => {
    if (item.response?.status !== undefined && item.response.status !== responseStatus(item)) return;
    const served = array(fixtureState?.requests).find(request => request.url === item.url && request.method === item.method && request.status === responseStatus(item) && same(parse(request.response_body), parse(text)));
    if (!served) return;
    network.push({ item, owner, body: parse(text), served, call_id: exchange.call_id, index });
    evidenceCalls.add(exchange.call_id);
  };
  for (let index = 0; index < exchanges.length; index++) {
    const exchange = exchanges[index];
    if (!validAck(exchange) || exchange.args?.action !== 'devtools') continue;
    const { method, params = {}, target: selected } = exchange.args;
    if (typeof method !== 'string' || !plain(params)) continue;
    const data = exchange.result.data;
    const provenance = data.provenance;
    if (!plain(provenance) || !Number.isSafeInteger(provenance.generation) || provenance.generation < 1 || typeof provenance.tab_id !== 'string') continue;
    const previousGeneration = latestGeneration.get(exchange.sessionId ?? '') ?? 0;
    if (provenance.generation < previousGeneration) continue;
    latestGeneration.set(exchange.sessionId ?? '', provenance.generation);
    validActions++;
    if (method !== 'targets.list') observeRoot(exchange, data);
    if (method === 'targets.list') {
      const provenance = data.provenance;
      if (!plain(provenance) || !Number.isSafeInteger(provenance.generation) || typeof provenance.tab_id !== 'string') continue;
      const entries = array(data.items).filter(item => TARGET.test(item?.target_ref ?? '') && ['page', 'iframe', 'worker'].includes(item.type) && item.ready === true && typeof item.url === 'string');
      const roots = entries.filter(item => item.is_root === true && item.type === 'page' && item.parent_target_ref === null);
      if (roots.length !== 1 || roots[0].target_ref !== provenance.target_ref && selected === undefined) continue;
      const targets = new Map(entries.map(item => [item.target_ref, item]));
      if (targets.size !== entries.length) continue;
      const envelope = targets.get(provenance.target_ref);
      if (!envelope || envelope.type !== provenance.target_type || (selected === undefined ? !envelope.is_root : selected !== envelope.target_ref)) continue;
      for (const [ref, item] of targets) {
        if (ref.split('_')[1] !== roots[0].target_ref.split('_')[1]) { targets.delete(ref); continue; }
        let ancestor = item;
        const visited = new Set([ref]);
        while (!ancestor.is_root) {
          if (!targets.has(ancestor.parent_target_ref) || visited.has(ancestor.parent_target_ref)) break;
          visited.add(ancestor.parent_target_ref);
          ancestor = targets.get(ancestor.parent_target_ref);
        }
        if (!ancestor.is_root) targets.delete(ref);
      }
      familyTargets.set(familyKey(exchange, provenance), targets);
      continue;
    }
    if (method === 'network.list') {
      for (const item of array(data.items)) {
        const owner = resolveTarget(exchange, data, item?.target_ref, item?.target_type);
        if (!owner || !issuedId(item, owner) || item.tab_id !== data.provenance.tab_id) continue;
        issuedRequests.set(`${owner.family}:${item.request_id}`, { item, owner, index, call_id: exchange.call_id });
      }
      continue;
    }
    if (method === 'network.read') {
      const item = data.request;
      const owner = resolveTarget(exchange, data, item?.target_ref, item?.target_type);
      if (!owner || !issuedId(item, owner) || item.request_id !== params.request_id || item.body?.state !== 'available' || typeof item.body.text !== 'string' || item.tab_id !== data.provenance.tab_id) continue;
      const prior = issuedRequests.get(`${owner.family}:${item.request_id}`);
      if (!prior || prior.index >= index || prior.item.target_ref !== item.target_ref || prior.item.url !== item.url || prior.item.method !== item.method || responseStatus(prior.item) !== undefined && responseStatus(prior.item) !== responseStatus(item)) continue;
      if (['cdp_request_id', 'frame_id', 'loader_id', 'navigation_id', 'seq'].some(key => prior.item[key] !== item[key])) continue;
      recordNetwork(item, owner, item.body.text, exchange, index);
      continue;
    }
    if (method === 'Network.getResponseBody') {
      const owner = resolveTarget(exchange, data, provenance.target_ref, provenance.target_type);
      if (!owner || data.state !== 'available' || data.method !== method || typeof params.requestId !== 'string' || typeof data.result?.body !== 'string' || typeof data.result.base64Encoded !== 'boolean') continue;
      // Raw IDs can repeat across targets and redirects. Select only the latest
      // observed hop on this target, never a same-named ID from another target.
      const prior = [...issuedRequests.values()].filter(record => record.owner.family === owner.family && record.owner.target_ref === owner.target_ref && record.item.cdp_request_id === params.requestId && record.index < index)
        .sort((left, right) => Number(right.item.request_id.split(':').at(-1)) - Number(left.item.request_id.split(':').at(-1)))[0];
      if (!prior) continue;
      let text = data.result.body;
      if (data.result.base64Encoded) {
        if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(text)) continue;
        text = Buffer.from(text, 'base64').toString('utf8');
      }
      recordNetwork(prior.item, owner, text, exchange, index);
      continue;
    }
    if (method === 'console.list') {
      for (const item of array(data.items)) {
        const owner = resolveTarget(exchange, data, item?.target_ref, item?.target_type);
        if (!owner || item.tab_id !== data.provenance.tab_id || !Number.isSafeInteger(item.seq)) continue;
        consoles.push({ item, owner, text: JSON.stringify({ args: item.args, details: item.details }), call_id: exchange.call_id });
      }
      continue;
    }
    if (method === 'events.list') {
      for (const item of array(data.items)) {
        const owner = resolveTarget(exchange, data, item?.target_ref, item?.target_type);
        if (!owner || item.tab_id !== data.provenance.tab_id || !Number.isSafeInteger(item.seq)) continue;
        const key = `${owner.family}:${item.seq}`;
        if (seenEvents.has(key)) continue;
        seenEvents.add(key);
        if (['Runtime.consoleAPICalled', 'Runtime.exceptionThrown', 'Log.entryAdded'].includes(item.method) && plain(item.params)) {
          consoles.push({ item, owner, text: JSON.stringify(item.params), call_id: exchange.call_id });
          continue;
        }
        if (['Network.requestWillBeSent', 'Network.responseReceived'].includes(item.method) && typeof item.params?.requestId === 'string') {
          const evidenceKey = `${owner.family}:${item.request_id}`;
          const identity = { id: item.request_id, request_id: item.request_id, tab_id: item.tab_id };
          if (!issuedId(identity, owner)) continue;
          const previous = eventRequests.get(evidenceKey);
          if (previous && (previous.owner.target_ref !== owner.target_ref || previous.cdp_request_id !== item.params.requestId)) continue;
          const record = previous ?? { ...identity, owner, cdp_request_id: item.params.requestId };
          if (item.method === 'Network.requestWillBeSent' && typeof item.params.request?.url === 'string' && typeof item.params.request?.method === 'string') record.requestEvent = item;
          if (item.method === 'Network.responseReceived' && typeof item.params.response?.status === 'number') record.responseEvent = item;
          eventRequests.set(evidenceKey, record);
          if (record.requestEvent) {
            const request = record.requestEvent.params;
            const row = { ...identity, seq: Number(item.request_id.split(':').at(-1)), target_ref: owner.target_ref, target_type: owner.type, cdp_request_id: record.cdp_request_id,
              url: request.request.url, method: request.request.method, frame_id: request.frameId, loader_id: request.loaderId,
              navigation_id: request.loaderId || record.requestEvent.navigation_id,
              ...(record.responseEvent ? { status: record.responseEvent.params.response.status, response: record.responseEvent.params.response } : {}),
            };
            issuedRequests.set(evidenceKey, { item: row, owner, index, call_id: exchange.call_id });
          }
          continue;
        }
        if (item.method === 'Debugger.scriptParsed' && typeof item.params?.scriptId === 'string' && typeof item.params?.url === 'string') {
          scriptURLs.set(`${owner.family}:${owner.target_ref}:${item.params.scriptId}`, { url: item.params.url, call_id: exchange.call_id });
          continue;
        }
        if (item.method === 'Debugger.resumed') { paused.delete(`${owner.family}:${owner.target_ref}`); continue; }
        if (item.method !== 'Debugger.paused') continue;
        const frames = array(item.params?.callFrames).filter(frame => typeof frame.callFrameId === 'string' && frame.callFrameId);
        if (!frames.length) continue;
        const event = { owner, frames, index, call_id: exchange.call_id, seq: item.seq, generation: provenance.generation, tab_id: provenance.tab_id };
        paused.set(`${owner.family}:${owner.target_ref}`, event);
        for (const frame of frames) for (const scope of array(frame.scopeChain)) {
          if (scope.type === 'local' && typeof scope.object?.objectId === 'string') objectOrigins.set(`${owner.family}:${owner.target_ref}:${scope.object.objectId}`, { event, frame, path: [] });
        }
      }
      continue;
    }
    if (data.state !== 'available' || data.method !== method) continue;
    const owner = resolveTarget(exchange, data, data.provenance?.target_ref, data.provenance?.target_type);
    if (!owner) continue;
    const active = paused.get(`${owner.family}:${owner.target_ref}`);
    if (method === 'Debugger.getScriptSource' && typeof params.scriptId === 'string' && typeof data.result?.scriptSource === 'string') {
      const served = array(fixtureState?.requests).find(request => request.url === requirements.debugger?.script_url && request.method === 'GET' && request.status === 200 && request.response_body === data.result.scriptSource);
      if (served) scriptURLs.set(`${owner.family}:${owner.target_ref}:${params.scriptId}`, { url: served.url, call_id: exchange.call_id });
    } else if (method === 'Debugger.evaluateOnCallFrame' && active) {
      const frame = active.frames.find(value => value.callFrameId === params.callFrameId);
      const expression = String(params.expression ?? '').trim();
      const name = /^(?:\(\s*)?([A-Za-z_$][\w$]*)(?:\s*\))?$/.exec(expression)?.[1]
        ?? /^JSON\.stringify\(\s*([A-Za-z_$][\w$]*)\s*\)$/.exec(expression)?.[1];
      if (frame && name && !data.result?.exceptionDetails && Object.hasOwn(requirements.debugger?.locals ?? {}, name)) {
        const remote = data.result?.result;
        const value = remoteValue(remote);
        if (same(value, requirements.debugger.locals[name])) inspected.push({ owner, frame, name, value, index, paused: active, call_id: exchange.call_id });
        else if (remote?.type === 'object' && typeof remote.objectId === 'string' && remote.objectId) {
          objectOrigins.set(`${owner.family}:${owner.target_ref}:${remote.objectId}`, { event: active, frame, path: [name], source_call_ids: [exchange.call_id] });
        }
      }
    } else if (method === 'Runtime.getProperties' && active) {
      const origin = objectOrigins.get(`${owner.family}:${owner.target_ref}:${params.objectId}`);
      if (!origin || origin.event !== active || data.result?.exceptionDetails) continue;
      const properties = array(data.result?.result).filter(property => typeof property.name === 'string' && plain(property.value));
      for (const property of properties) {
        const path = [...origin.path, property.name];
        if (typeof property.value.objectId === 'string') objectOrigins.set(`${owner.family}:${owner.target_ref}:${property.value.objectId}`, { ...origin, path, source_call_ids: [...array(origin.source_call_ids), exchange.call_id] });
        if (path.length === 1 && Object.hasOwn(requirements.debugger?.locals ?? {}, path[0]) && same(remoteValue(property.value), requirements.debugger.locals[path[0]])) inspected.push({ owner, frame: origin.frame, name: path[0], value: remoteValue(property.value), index, paused: active, call_id: exchange.call_id });
      }
      if (origin.path.length === 1 && Object.hasOwn(requirements.debugger?.locals ?? {}, origin.path[0])) {
        const value = Object.fromEntries(properties.filter(property => Object.hasOwn(property.value, 'value')).map(property => [property.name, remoteValue(property.value)]));
        if (same(value, requirements.debugger.locals[origin.path[0]])) inspected.push({ owner, frame: origin.frame, name: origin.path[0], value, index, paused: active, call_id: exchange.call_id, source_call_ids: origin.source_call_ids });
      }
    } else if (method === 'Debugger.resume' && active) {
      resumed.push({ owner, paused: active, index, call_id: exchange.call_id, started_monotonic_ms: exchange.started_monotonic_ms });
      paused.delete(`${owner.family}:${owner.target_ref}`);
    }
  }

  let domSubmissionResponse;
  if (dom?.kind === 'saved_record_paragraph') {
    const required = array(requirements.network).find(value => value.method === 'POST');
    domSubmissionResponse = [...issuedRequests.values()].find(record => required && record.owner.family === `${dom.session_id}:${dom.provenance.generation}:${dom.provenance.tab}`
      && record.owner.type === required.target_type && record.owner.url === oracle.case_url && record.item.url === required.url && record.item.method === 'POST'
      && responseStatus(record.item) === required.status && (record.item.response?.status === undefined || record.item.response.status === required.status)
      && fixtureState.requests.some(request => request.url === required.url && request.method === 'POST' && request.status === required.status && same(parse(request.response_body), parse(required.body))));
  }
  let usedDOM = false;
  const networkMatches = array(requirements.network).map((required, index) => {
    if (required.expected_request_count !== undefined && array(fixtureState?.requests).filter(request => request.url === required.url && request.method === required.method).length !== required.expected_request_count) fail(`NETWORK_REQUEST_COUNT_MISMATCH:${index}`);
    const match = network.find(record => record.item.url === required.url && record.item.method === required.method && responseStatus(record.item) === required.status && record.owner.type === required.target_type && same(record.body, parse(required.body))
      && (requirements.target?.type !== required.target_type || targetURL(record.owner, requirements.target.url)));
    if (match) return match;
    if (dom?.kind === 'iframe_table_row' && required.target_type === 'iframe' || domSubmissionResponse && task.id === 'authenticated-write') {
      usedDOM = true;
      if (required.method === 'POST' && domSubmissionResponse) {
        evidenceCalls.add(domSubmissionResponse.call_id);
        return { ...domSubmissionResponse, evidence_kind: 'response_metadata_and_dom' };
      }
      return null;
    }
    fail(`NETWORK_EVIDENCE_MISSING:${index}`);
    return null;
  });
  if (usedDOM) evidenceCalls.add(dom.call_id);
  for (const [index, required] of array(requirements.console).entries()) {
    const match = consoles.find(record => record.owner.type === required.target_type && array(required.includes).every(part => record.text.includes(String(part)))
      && networkMatches.some(networkRecord => networkRecord && networkRecord.owner.family === record.owner.family && networkRecord.owner.target_ref === record.owner.target_ref));
    if (!match) fail(`CONSOLE_EVIDENCE_MISSING:${index}`);
    else evidenceCalls.add(match.call_id);
  }
  if (requirements.debugger) {
    const required = requirements.debugger;
    for (const name of Object.keys(required.locals ?? {})) {
      const match = inspected.find(record => record.name === name && record.owner.type === required.target_type && targetURL(record.owner, requirements.target?.url)
        && (oracle.prepared_pause_anchor ? preparedFrameMatches(record, oracle.prepared_pause_anchor)
          : !required.script_url || record.frame.url === required.script_url || !record.frame.url && scriptURLs.get(`${record.owner.family}:${record.owner.target_ref}:${record.frame.location?.scriptId}`)?.url === required.script_url)
        && resumed.some(resume => resume.owner.family === record.owner.family && resume.owner.target_ref === record.owner.target_ref && resume.paused === record.paused && resume.index > record.index));
      if (!match) fail(`DEBUGGER_CHAIN_MISSING:${name}`);
      else {
        evidenceCalls.add(match.paused.call_id); evidenceCalls.add(match.call_id); evidenceCalls.add(resumed.find(resume => resume.paused === match.paused && resume.index > match.index).call_id);
        for (const callId of array(match.source_call_ids)) evidenceCalls.add(callId);
        if (!match.frame.url) evidenceCalls.add(scriptURLs.get(`${match.owner.family}:${match.owner.target_ref}:${match.frame.location?.scriptId}`)?.call_id);
      }
    }
    // A correct early save plus a later unrelated resume is not recovery. The
    // private proxy and fixture server share this Node monotonic clock; bind
    // the actual POST arrival to the successful resume dispatch, not a later
    // network.read of an old cached response.
    for (const [index, requirement] of array(requirements.network).entries()) {
      if (requirement.method !== 'POST') continue;
      const saved = networkMatches[index];
      const recovery = saved && resumed.find(resume => resume.owner.family === saved.owner.family && resume.owner.target_ref === saved.owner.target_ref
        && resume.index < saved.index && Number.isFinite(resume.started_monotonic_ms) && Number.isFinite(saved.served.received_monotonic_ms)
        && saved.served.received_monotonic_ms >= resume.started_monotonic_ms
        && inspected.some(record => record.paused === resume.paused && record.index < resume.index));
      if (!recovery) fail(`DEBUGGER_SAVE_NOT_AFTER_RESUME:${index}`);
    }
  }
  if (requirements.submission) {
    const required = requirements.submission;
    const submissions = array(fixtureState?.submissions);
    if (submissions.length !== required.expected_count || fixtureState?.submission_attempts !== required.expected_count) fail('SUBMISSION_COUNT_MISMATCH');
    if (!submissions.every(record => fieldsMatch(record, required.fields))) fail('SUBMISSION_FIELDS_MISMATCH');
  }
  if (requirements.authenticated && fixtureState?.authenticated !== true) fail('AUTHENTICATED_STATE_REQUIRED');
  if (!array(requirements.network).length && !requirements.debugger && !requirements.submission) fail('CASE_HAS_NO_BUSINESS_REQUIREMENT');
  if (!validActions && !usedDOM) fail('NO_VERIFIED_DEVTOOLS_ACTION');
  const coreBusinessSuccess = failures.length === 0;
  const answer = answerCheck(finalAnswer, array(oracle?.answer_facts));
  for (const field of answer.missing) fail(`ANSWER_FACT_MISSING:${field}`);
  for (const field of answer.conflicts) fail(`ANSWER_FACT_CONFLICT:${field}`);
  return {
    success: coreBusinessSuccess && answer.success,
    core_business_success: coreBusinessSuccess, answer_accuracy: answer.success,
    needs_review: answer.needs_review, failures,
    evidence: {
      source: 'trusted_worker_exchange_ledger_and_fixture_state',
      call_ids: [...evidenceCalls].filter(value => value !== undefined),
      network_request_ids: networkMatches.filter(Boolean).map(record => record.item.request_id),
      network_target_refs: networkMatches.filter(Boolean).map(record => record.owner.target_ref),
      network_evidence: networkMatches.filter(Boolean).map(record => ({ request_id: record.item.request_id, kind: record.evidence_kind ?? 'response_body' })),
      ...(usedDOM ? { dom: { kind: dom.kind, call_id: dom.call_id, source_id: dom.source_id, observation_id: dom.observation_id, frame_url: dom.frame_url, provenance: dom.provenance } } : {}),
      verified_devtools_calls: validActions, debugger_inspections: inspected.length,
      answer,
    },
  };
}
