// Verify real-model Office runs without calling a model or changing an editor.
// Evidence is bound to individual user turns and tool IDs. Assistant prose is
// intentionally ignored: a later successful repair cannot erase an earlier
// failed call, missing read-back, or unsafe history action in the same phase.
import fs from 'node:fs/promises';
import path from 'node:path';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';

const runFile = promisify(execFile);

const hash = value => createHash('sha256').update(value).digest('hex');
const sha = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const opID = value => typeof value === 'string' && /^[A-Za-z0-9_-]{8,100}$/.test(value);
const canon = value => value === null || typeof value !== 'object' ? JSON.stringify(value) :
  Array.isArray(value) ? '[' + value.map(canon).join(',') + ']' :
    '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canon(value[key])).join(',') + '}';
const promptText = message => (message.content || []).filter(block => block.type === 'text').map(block => block.text || '').join('\n');
const humanTurn = message => message.role === 'user' && promptText(message) !== '' &&
  !(message.content || []).some(block => block.type === 'tool_result');
const cleanSession = session => session && session.ready === true && session.closed === false && session.blocked === false;
const normalizePath = value => typeof value === 'string' ? path.posix.normalize(value.replaceAll('\\', '/')).replace(/^\.\//, '') : '';
const formula = value => typeof value === 'string' ? value.replace(/^=/, '').replace(/'([^']+)'!/g, '$1!').replaceAll(' ', '') : value;
const ref = value => typeof value === 'string' ? value.replaceAll('$', '').toUpperCase() : '';
function address(value) {
  const match = /^([A-Z]+)([1-9][0-9]*)$/.exec(ref(value));
  if (!match) throw new Error('Unsupported cell address: ' + value);
  let column = 0;
  for (const letter of match[1]) column = column * 26 + letter.charCodeAt(0) - 64;
  return {column, row:Number(match[2])};
}
function letters(column) {
  let result = '';
  while (column > 0) { column--; result = String.fromCharCode(65 + column % 26) + result; column = Math.floor(column / 26); }
  return result;
}
function cells(snapshot) {
  if (snapshot?.kind !== 'cell' || !Array.isArray(snapshot.values) || typeof snapshot.sheet !== 'string') return new Map();
  const start = address(snapshot.range.split(':')[0]);
  const result = new Map();
  snapshot.values.forEach((row, y) => row.forEach((value, x) => result.set(snapshot.sheet + '!' + letters(start.column + x) + (start.row + y),
    {value, input:snapshot.inputs?.[y]?.[x], display:snapshot.display?.[y]?.[x]})));
  return result;
}
function mergeCells(snapshots) {
  const result = new Map();
  for (const snapshot of snapshots) for (const [key, value] of cells(snapshot)) result.set(key, value);
  return result;
}
function unwrap(value) { return value?.ok === true ? value.result : value; }
function snapshotsFromReopen(reopen, kind) {
  if (Array.isArray(reopen.reads)) return reopen.reads.filter(read => read?.ok === true).map(unwrap);
  if (kind === 'word') return reopen.result?.ok === true ? [reopen.result.result] : [];
  return [reopen.sales, reopen.summary].filter(read => read?.ok === true).map(unwrap);
}
function transcript(rows) {
  return rows.flatMap((row, rowIndex) => {
    const message = row.type === 'message' ? row.message : row.role ? row : undefined;
    return message ? [{...message,rowIndex}] : [];
  });
}
function phaseWindow(messages, phase) {
  const matches = messages.flatMap((message, index) => humanTurn(message) &&
    (phase.exactPrompt !== undefined ? promptText(message) === phase.exactPrompt : message.id === phase.messageId) ? [index] : []);
  if (matches.length !== 1) throw new Error(`Phase ${phase.name}: expected exactly one matching user turn, found ${matches.length}`);
  const start = matches[0];
  const next = messages.findIndex((message, index) => index > start && humanTurn(message));
  return {start,end:next < 0 ? messages.length : next,messages:messages.slice(start,next < 0 ? undefined : next),prompt:promptText(messages[start])};
}
function pairedCalls(window) {
  const calls = [], results = new Map(), errors = [];
  let position = 0;
  for (const message of window.messages) for (const block of message.content || []) {
    position++;
    if (block.type === 'tool_use') calls.push({...block,position,rowIndex:message.rowIndex});
    if (block.type === 'tool_result') {
      if (results.has(block.tool_use_id)) errors.push('duplicate tool_result ID ' + block.tool_use_id);
      let parsed;
      try { parsed = JSON.parse(block.text); } catch (_) { /* Recorded below as failure, never silently skipped. */ }
      results.set(block.tool_use_id,{...block,position,parsed,rowIndex:message.rowIndex});
    }
  }
  const seen = new Set();
  for (const call of calls) {
    if (!call.id || seen.has(call.id)) errors.push('missing or duplicate tool_use ID ' + (call.id || '(missing)'));
    seen.add(call.id);
    call.response = results.get(call.id);
    if (!call.response) errors.push('unanswered tool_use ID ' + call.id);
    else if (call.response.position <= call.position) errors.push('tool_result precedes its call ' + call.id);
  }
  for (const id of results.keys()) if (!seen.has(id)) errors.push('orphan tool_result ID ' + id);
  return {calls,errors};
}
function reviewedSkillRead(call, reviewedSkillPath) {
  if (call.name !== 'read' || !call.input || typeof call.input !== 'object' || Array.isArray(call.input) || !reviewedSkillPath) return false;
  const args = call.input;
  if (Object.keys(args).some(key => !['path','paths','offset','limit'].includes(key))) return false;
  const hasPath = Object.hasOwn(args,'path'), hasPaths = Object.hasOwn(args,'paths');
  if (hasPath === hasPaths || hasPaths && (!Array.isArray(args.paths) || args.paths.length !== 1 || args.offset !== undefined || args.limit !== undefined)) return false;
  const requested = hasPath ? args.path : args.paths[0];
  if (typeof requested !== 'string' || !path.isAbsolute(requested) || path.resolve(requested) !== path.resolve(reviewedSkillPath)) return false;
  return ['offset','limit'].every(key => args[key] === undefined || Number.isInteger(args[key]) && args[key] >= 0);
}
function successful(call) {
  const response = call.response;
  if (!response || response.is_error === true) return false;
  if (call.auxiliarySkillRead === true) return typeof response.text === 'string' && response.text !== '';
  if (!response.parsed || response.parsed.ok === false || response.parsed.error) return false;
  const body = response.parsed;
  if (call.input?.action === 'list') return Array.isArray(body.sessions);
  if (call.input?.action === 'open') return !!body.session && body.session.closed === false && body.session.blocked === false;
  return body.ok === true;
}
const operation = call => call.input?.action === 'command' ? call.input.operation : call.input?.action;
const readOperation = kind => kind === 'word' ? 'word.read' : 'sheet.read';
const editOperation = kind => kind === 'word' ? 'word.replace' : 'sheet.apply';
const isMutation = call => ['save','close','word.replace','sheet.apply','history.undo','history.redo'].includes(operation(call));
function wordPrecondition(action, priorReads, sessionID) {
  const args = action?.input?.args;
  if (!args || typeof args !== 'object' || Array.isArray(args)) return {passed:false,kind:'missing'};
  const hasText = Object.hasOwn(args,'expectedText'), hasRead = Object.hasOwn(args,'expectedReadId');
  if (hasText === hasRead) return {passed:false,kind:'ambiguous-or-missing'};
  const successfulReads = priorReads.filter(call => call.name === 'docs_office' && operation(call) === 'word.read' &&
    call.input?.document_session_id === sessionID && successful(call) && call.response.parsed.result?.kind === 'word' &&
    typeof call.response.parsed.result.text === 'string');
  if (hasText) return {passed:typeof args.expectedText === 'string' && successfulReads.some(call => call.response.parsed.result.text === args.expectedText),kind:'exact-text'};
  const matches = successfulReads.filter(call => call.response.parsed.operationId === args.expectedReadId);
  return {passed:opID(args.expectedReadId) && matches.length > 0 && new Set(matches.map(call => canon(call.response.parsed.result))).size === 1,
    kind:'same-session-read-receipt',expectedReadId:args.expectedReadId,
    sourceToolUseIds:matches.map(call => call.id)};
}
function literalOccurrences(text, search) {
  if (typeof text !== 'string' || typeof search !== 'string' || search === '') return 0;
  return text.split(search).length - 1;
}
function limitedWordReplacement(args, before, word) {
  if (!args || typeof args.search !== 'string' || args.search === '' || typeof args.replacement !== 'string' ||
    args.expectedCount !== 1 || /[\r\n\t\x00]/.test(args.search + args.replacement)) return false;
  if (literalOccurrences(word.original,args.search) !== 1 || word.original.replace(args.search,args.replacement) !== word.edited) return false;
  const observed = before.filter(snapshot => snapshot?.kind === 'word' && typeof snapshot.text === 'string');
  // A unique suffix is as precise as replacing the whole marked paragraph. It
  // must occur inside the original target and only once in every actual read,
  // and reconstruct exactly the requested complete target without touching any
  // surrounding text. Later reads cannot excuse an earlier ambiguous match.
  return observed.length > 0 && observed.every(snapshot => literalOccurrences(snapshot.text,args.search) === 1 &&
    literalOccurrences(snapshot.text,word.original) === 1 &&
    snapshot.text.replace(args.search,args.replacement) === snapshot.text.replace(word.original,word.edited));
}
function singleCellA1(value) {
  if (typeof value !== 'string') return null;
  const match = /^\$?([A-Za-z]{1,3})\$?([1-9][0-9]{0,6})(?::\$?([A-Za-z]{1,3})\$?([1-9][0-9]{0,6}))?$/.exec(value);
  if (!match) return null;
  const start = address(match[1] + match[2]), end = match[3] ? address(match[3] + match[4]) : start;
  if (start.column > 16384 || start.row > 1048576 || end.column !== start.column || end.row !== start.row) return null;
  return letters(start.column) + start.row;
}
function uniqueMutations(calls) {
  const result = [], ids = new Map(), errors = [];
  for (const call of calls.filter(isMutation)) {
    const id = call.input?.operation_id;
    const fingerprint = canon(call.input);
    if (!opID(id)) errors.push('invalid/missing operation_id on ' + call.id);
    else if (ids.has(id) && ids.get(id) !== fingerprint) errors.push('operation_id reused for different request: ' + id);
    else if (!ids.has(id)) { ids.set(id,fingerprint); result.push(call); }
  }
  return {calls:result,errors};
}
function stateAssertions(check, prefix, snapshots, kind, manifest, state, humanMarker) {
  if (kind === 'word') {
    check(prefix + '.read-observed', snapshots.some(snapshot => snapshot?.kind === 'word' && typeof snapshot.text === 'string'));
    const word = snapshots.filter(snapshot => snapshot?.kind === 'word' && typeof snapshot.text === 'string');
    const expected = manifest.word[state === 'edited' ? 'edited' : 'original'];
    const unexpected = manifest.word[state === 'edited' ? 'original' : 'edited'];
    check(prefix + '.target-content', word.length > 0 && word.every(snapshot => snapshot.text.includes(expected) && !snapshot.text.includes(unexpected)));
    check(prefix + '.human-input-preserved', word.length > 0 && word.every(snapshot => snapshot.text.includes(humanMarker)));
    check(prefix + '.guard-paragraphs-preserved', word.length > 0 && word.every(snapshot => manifest.word.guards.every(text => snapshot.text.includes(text))));
  } else {
    const observed = mergeCells(snapshots);
    const expected = manifest.excel[state === 'edited' ? 'edited' : 'original'];
    const calculated = manifest.excel[state === 'edited' ? 'calculated_edited' : 'calculated_original'];
    const target = manifest.excel.target, human = manifest.excel.human_target;
    check(prefix + '.read-observed', snapshots.some(snapshot => snapshot?.kind === 'cell'));
    check(prefix + '.target-content', observed.get(target)?.value === expected, {expected,observed:observed.get(target)?.value});
    check(prefix + '.human-input-preserved', observed.get(human)?.value === humanMarker);
    // Merge disjoint agent reads while checking every observable copy: a later
    // corrective read cannot conceal a wrong quantity/marker in the same phase.
    check(prefix + '.no-conflicting-target-observation', snapshots.every(snapshot => !cells(snapshot).has(target) || cells(snapshot).get(target).value === expected));
    check(prefix + '.no-conflicting-human-observation', snapshots.every(snapshot => !cells(snapshot).has(human) || cells(snapshot).get(human).value === humanMarker));
    const amount = target.split('!')[0] + '!D2';
    check(prefix + '.dependent-formula-recalculated', observed.get(amount)?.value === calculated[amount], {expected:calculated[amount],observed:observed.get(amount)?.value});
    check(prefix + '.dependent-formula-preserved', formula(observed.get(amount)?.input) === formula(manifest.excel.formulas[amount]));
  }
}

export async function verifyEvidence(config, configDirectory = process.cwd()) {
  const resolve = file => path.isAbsolute(file) ? file : path.resolve(configDirectory,file);
  const loadJSON = async file => JSON.parse(await fs.readFile(resolve(file),'utf8'));
  const reviewedSkillPath = config.reviewedSkillPath ? resolve(config.reviewedSkillPath) : fileURLToPath(new URL('../../skills/builtin/docs/SKILL.md',import.meta.url));
  const checks = [], scenarioReports = [], errors = [], exportFiles = {};
  const check = (name,passed,detail) => checks.push({name,passed:!!passed,...(detail === undefined ? {} : {detail})});
  const guarded = async (name,fn) => { try { await fn(); } catch (error) { check(name,false,{error:error.message}); errors.push({name,error:error.message}); } };
  if (config.schemaVersion !== 1) throw new Error('Expected model-check-input schemaVersion 1');
  const manifest = await loadJSON(path.join(config.baselineDir,'manifest.json'));
  check('baseline.synthetic-files-only', manifest.synthetic === true);
  for (const kind of ['word','excel']) check('baseline.' + kind + '.recorded-file-sha256',
    sha(manifest.files[kind]?.sha256) && hash(await fs.readFile(resolve(path.join(config.baselineDir,manifest.files[kind].name)))) === manifest.files[kind].sha256);
  const provenance = await loadJSON(config.provenanceFile);
  const streams = provenance.calls || provenance.streams || [];
  check('model.real-provider-provenance', provenance.kind === 'real-provider-streams' && provenance.synthetic === false &&
    typeof provenance.model === 'string' && !/fixture|deterministic|scripted|stub/i.test(provenance.model) &&
    typeof provenance.provider === 'string' && provenance.provider !== '' && streams.length > 0);
  check('model.real-responses-recorded', streams.length > 0 && streams.every(stream =>
    sha(stream.responseSha256 || stream.response_sha256) && stream.completed === true && !stream.error &&
    stream.model === provenance.model && stream.provider === provenance.provider &&
    (stream.httpStatus === undefined || stream.httpStatus >= 200 && stream.httpStatus < 300)));
  check('scenarios.word-and-excel-present', Array.isArray(config.scenarios) &&
    ['word','excel'].every(kind => config.scenarios.some(scenario => scenario.kind === kind)));
  for (const scenario of config.scenarios || []) await guarded(scenario.kind + '.evidence-load', async () => {
    const kind = scenario.kind;
    if (!['word','excel'].includes(kind)) throw new Error('Unsupported scenario kind ' + kind);
    const rows = (await fs.readFile(resolve(scenario.transcriptFile || config.transcriptFile),'utf8')).trim().split('\n').filter(Boolean).map(JSON.parse);
    const messages = transcript(rows);
    const allPairs = pairedCalls({messages});
    check(kind + '.complete-transcript-tool-pairs', allPairs.errors.length === 0,allPairs.errors);
    const human = await loadJSON(scenario.humanInputFile);
    const id = scenario.documentSessionId || human.session?.id;
    const marker = scenario.humanMarker || manifest[kind].human_marker;
    check(kind + '.human-input-origin-recorded', human.method === 'trusted-browser-input' && typeof human.capturedAt === 'string');
    check(kind + '.human-input-not-yet-saved', human.session?.dirty === true && human.session?.saveState === 'dirty' &&
      sha(human.fileSha256) && human.fileSha256 === manifest.files[kind].sha256);
    check(kind + '.mounted-document-identity', !!id && human.session?.id === id && cleanSession(human.session) &&
      normalizePath(human.session.path) === normalizePath(scenario.path));
    const humanSnapshot = human.snapshot?.ok === true ? human.snapshot.result : human.snapshot;
    const initial = Array.isArray(humanSnapshot) ? humanSnapshot.map(unwrap) : [humanSnapshot];
    stateAssertions(check,kind + '.human-baseline',initial,kind,manifest,'original',marker);
    check(kind + '.four-separate-phases', canon((scenario.phases || []).map(phase => phase.name)) === canon(['edit','undo','redo','save']));
    const report = {kind,documentSessionId:id,phases:[]};
    scenarioReports.push(report);
    let previousEnd = -1;
    let initialRevision = human.session?.revision ?? 0;
    const mutationIDs = new Map();
    for (const phase of scenario.phases || []) await guarded(kind + '.' + phase.name + '.evidence', async () => {
      const prefix = kind + '.' + phase.name;
      const window = phaseWindow(messages,phase), paired = pairedCalls(window), calls = paired.calls;
      for (const call of calls) call.auxiliarySkillRead = reviewedSkillRead(call,reviewedSkillPath);
      check(prefix + '.turn-order', window.start >= previousEnd);
      previousEnd = window.end;
      check(prefix + '.complete-tool-pairs', paired.errors.length === 0, paired.errors);
      check(prefix + '.registered-office-and-reviewed-skill-tools-only', calls.length > 0 && calls.every(call => call.name === 'docs_office' || call.auxiliarySkillRead));
      check(prefix + '.no-failed-call-hidden-by-repair', calls.length > 0 && calls.every(successful), calls.filter(call => !successful(call)).map(call => ({id:call.id,operation:operation(call),error:call.response?.parsed?.error || call.response?.text || 'no result'})));
      check(prefix + '.same-mounted-document', calls.every(call => call.auxiliarySkillRead || call.input?.action === 'list' ||
        call.input?.action === 'open' && normalizePath(call.input.path) === normalizePath(scenario.path) && call.response?.parsed?.session?.id === id ||
        call.input?.document_session_id === id));
      const relevantStreams = streams.filter(stream => (stream.lastUserTextSha256 || stream.last_user_text_sha256 || stream.promptSha256) === hash(window.prompt));
      const linkedToolIds = new Set(relevantStreams.flatMap(stream => stream.toolUseIds || stream.tool_use_ids || []));
      check(prefix + '.real-model-response-linked-to-tools', relevantStreams.length > 0 && calls.every(call => linkedToolIds.has(call.id)));
      const mutations = uniqueMutations(calls);
      check(prefix + '.stable-mutation-operation-ids', mutations.errors.length === 0, mutations.errors);
      for (const call of calls.filter(isMutation)) {
        const key = call.input?.operation_id, payload = canon(call.input);
        if (mutationIDs.has(key) && mutationIDs.get(key) !== payload) check(prefix + '.cross-phase-operation-id-reuse',false,{id:key});
        mutationIDs.set(key,payload);
      }
      const expectedOperation = {edit:editOperation(kind),undo:'history.undo',redo:'history.redo',save:'save'}[phase.name];
      check(prefix + '.only-requested-mutation', mutations.calls.length === 1 && operation(mutations.calls[0]) === expectedOperation,
        mutations.calls.map(call => ({id:call.id,operation:operation(call)})));
      const action = mutations.calls.find(call => operation(call) === expectedOperation);
      const reads = calls.filter(call => operation(call) === readOperation(kind) && successful(call));
      const before = reads.filter(call => call.response.position < (action?.position ?? -1)).map(call => call.response.parsed.result);
      const after = reads.filter(call => call.position > (action?.response?.position ?? Infinity)).map(call => call.response.parsed.result);
      if (phase.name === 'edit') {
        stateAssertions(check,prefix + '.before',before,kind,manifest,'original',marker);
        if (kind === 'word') {
          const args = action?.input?.args;
          const actionInTranscript = allPairs.calls.find(call => call.id === action?.id);
          const priorReads = allPairs.calls.filter(call => call.response && call.response.position < (actionInTranscript?.position ?? -1));
          const precondition = wordPrecondition(action,priorReads,id);
          check(prefix + '.exact-read-precondition',precondition.passed,precondition);
          check(prefix + '.limited-target-replacement',limitedWordReplacement(args,before,manifest.word));
        } else {
          const args = action?.input?.args, target = manifest.excel.target.split('!');
          const observed = mergeCells(before).get(manifest.excel.target);
          check(prefix + '.exact-read-precondition', observed && canon(args?.expectedInputs) === canon([[observed.input]]));
          const targetCell = singleCellA1(target[1]);
          check(prefix + '.limited-target-replacement', args?.sheet === target[0] && targetCell !== null && singleCellA1(args?.range) === targetCell &&
            Array.isArray(args?.inputs) && args.inputs.length === 1 && Array.isArray(args.inputs[0]) && args.inputs[0].length === 1 &&
            (args.inputs[0][0] === manifest.excel.edited || args.inputs[0][0] === String(manifest.excel.edited)));
        }
        stateAssertions(check,prefix + '.after',after,kind,manifest,'edited',marker);
      } else if (phase.name === 'undo' || phase.name === 'redo') {
        check(prefix + '.native-editor-history', action?.response?.parsed?.result?.native === true &&
          action.response.parsed.result.historyScope === 'editor-global');
        stateAssertions(check,prefix + '.after',after,kind,manifest,phase.name === 'undo' ? 'original' : 'edited',marker);
      } else if (phase.name === 'save') {
        const saved = action?.response?.parsed?.session;
        check(prefix + '.confirmed-persistent-save', successful(action || {}) && saved?.id === id && saved?.saveState === 'saved' &&
          saved?.dirty === false && saved?.blocked === false && saved?.revision > initialRevision &&
          typeof saved?.savedAt === 'string' && saved?.persistedEpoch === saved?.dirtyEpoch);
        report.savedRevision = saved?.revision;
      }
      report.phases.push({name:phase.name,userRow:messages[window.start].rowIndex,toolCalls:calls.map(call => ({id:call.id,name:call.name,operation:operation(call),operationId:call.input?.operation_id,auxiliarySkillRead:call.auxiliarySkillRead,success:successful(call)})),
        linkedModelStreams:relevantStreams.length});
    });
    const reopen = await loadJSON(scenario.reopenFile);
    check(kind + '.new-editor-after-save', cleanSession(reopen.session) && reopen.session.id !== id &&
      normalizePath(reopen.session.path) === normalizePath(scenario.path) && reopen.session.dirty === false &&
      reopen.session.pending === false && reopen.session.saveState === 'saved');
    const reopened = snapshotsFromReopen(reopen,kind);
    stateAssertions(check,kind + '.reopen',reopened,kind,manifest,'edited',marker);
    if (kind === 'excel') {
      const values = mergeCells(reopened), calculated = manifest.excel.calculated_edited;
      for (const target of ['汇总!B2','汇总!B3']) check(kind + '.reopen.cross-sheet.' + target,
        values.get(target)?.value === calculated[target] && formula(values.get(target)?.input) === formula(manifest.excel.formulas[target]));
    }
    const exportedHash = hash(await fs.readFile(resolve(scenario.exportFile)));
    const liveHash = hash(await fs.readFile(resolve(scenario.liveFile)));
    check(kind + '.saved-and-reopened-file-identity', sha(reopen.fileSha256) && reopen.fileSha256 === exportedHash && liveHash === exportedHash);
    check(kind + '.save-changed-file-bytes', exportedHash !== manifest.files[kind].sha256);
    report.fileSha256 = exportedHash;
    exportFiles[kind] = {path:resolve(scenario.exportFile),sha256:exportedHash};
    if (scenario.guardPhase) await guarded(kind + '.interleaved-human-guard.evidence',async () => {
      const phase = scenario.guardPhase, prefix = kind + '.interleaved-human-guard';
      if (kind !== 'word') throw new Error('The optional interleaved guard currently requires Word whole-text evidence.');
      const window = phaseWindow(messages,{...phase,name:'interleaved-human-guard'}), paired = pairedCalls(window), calls = paired.calls;
      for (const call of calls) call.auxiliarySkillRead = reviewedSkillRead(call,reviewedSkillPath);
      const before = await loadJSON(phase.beforeFile), after = await loadJSON(phase.afterFile);
      const beforeSnapshot = unwrap(before.snapshot), afterSnapshot = unwrap(after.snapshot);
      const guardID = before.session?.id;
      const markers = phase.humanMarkers || [marker];
      check(prefix + '.separate-later-user-turn', window.start >= previousEnd);
      check(prefix + '.complete-tool-pairs', paired.errors.length === 0,paired.errors);
      check(prefix + '.same-mounted-live-document', cleanSession(before.session) && cleanSession(after.session) &&
        guardID === after.session.id && normalizePath(before.session.path) === normalizePath(scenario.path) &&
        normalizePath(after.session.path) === normalizePath(scenario.path));
      check(prefix + '.no-tool-mutation-or-save', calls.every(call => call.auxiliarySkillRead || !isMutation(call) && call.name === 'docs_office' &&
        ['list','open','capabilities','word.read'].includes(operation(call))));
      check(prefix + '.same-document-tool-scope', calls.every(call => call.auxiliarySkillRead || call.input?.action === 'list' ||
        call.input?.action === 'open' && normalizePath(call.input.path) === normalizePath(scenario.path) && call.response?.parsed?.session?.id === guardID ||
        call.input?.document_session_id === guardID));
      check(prefix + '.no-failed-call-hidden-by-repair', calls.every(successful));
      check(prefix + '.live-content-not-changed', beforeSnapshot?.kind === 'word' && typeof beforeSnapshot.text === 'string' &&
        afterSnapshot?.kind === 'word' && beforeSnapshot.text === afterSnapshot.text);
      check(prefix + '.all-human-input-preserved', markers.length >= 2 && markers.every(text =>
        typeof text === 'string' && text !== '' && beforeSnapshot?.text?.includes(text) && afterSnapshot?.text?.includes(text)));
      const reads = calls.filter(call => operation(call) === 'word.read' && successful(call));
      check(prefix + '.model-read-current-interleaved-content', reads.length > 0 && reads.every(call =>
        call.input.document_session_id === guardID && call.response.parsed.result?.text === beforeSnapshot.text));
      const text = window.messages.filter(message => message.role === 'assistant').map(promptText).join('\n');
      check(prefix + '.model-explains-global-history-risk', /全局|原生撤销|native|global/i.test(text) &&
        /人工|手动|用户|human|manual|user/i.test(text) && /无法|不能|没有|不支持|非选择|不.{0,5}撤销|一并|连同|non.?selective|cannot|can.t|not support/i.test(text));
      const real = streams.filter(stream => (stream.lastUserTextSha256 || stream.last_user_text_sha256 || stream.promptSha256) === hash(window.prompt));
      const ids = new Set(real.flatMap(stream => stream.toolUseIds || stream.tool_use_ids || []));
      check(prefix + '.real-model-response-linked', real.length > 0 && calls.every(call => ids.has(call.id)));
      report.interleavedHumanGuard = {toolCalls:calls.map(call => ({id:call.id,operation:operation(call)})),
        scope:'Safe read and explanation when human input is latest in global history; does not count as native undo/redo acceptance.'};
    });
  });
  if (config.exportReportFile) await guarded('exports.report-load',async () => {
    const exported = await loadJSON(config.exportReportFile);
    check('exports.actual-ooxml-inspection-passed', exported.kind === 'synthetic-ooxml-export-inspection' && exported.phase === 'edited' &&
      exported.passed === true && Array.isArray(exported.checks) && exported.checks.length > 0 && exported.checks.every(item => item.passed === true));
    const names = new Set((exported.checks || []).filter(item => item.passed).map(item => item.name));
    check('exports.human-markers-and-formulas-covered', ['word.human-edit-preserved','excel.human-edit-preserved','excel.recalculated-cache.销售!D2','excel.recalculated-cache.汇总!B2'].every(name => names.has(name)));
    if (!exportFiles.word || !exportFiles.excel) throw new Error('Both current exported files are required for independent OOXML inspection.');
    if (!['strict','light'].includes(exported.render_profile)) throw new Error('Unsupported export rendering profile.');
    const script = fileURLToPath(new URL('../../office-lab/fixtures/verify_exports.py',import.meta.url));
    const args = [script,'--baseline',resolve(config.baselineDir),'--word',exportFiles.word.path,'--excel',exportFiles.excel.path,
      '--phase','edited','--require-human-word','--require-human-excel','--require-formula-cache','--render-profile',exported.render_profile];
    let output;
    try { output = (await runFile(process.env.OFFICE_CHECK_PYTHON || 'python3',args,{maxBuffer:16 << 20})).stdout; }
    catch (error) { if (!error.stdout) throw error; output = error.stdout; }
    const inspected = JSON.parse(output);
    check('exports.independent-current-bytes-ooxml-inspection', inspected.passed === true &&
      canon(inspected.checks.map(({name,passed}) => ({name,passed}))) === canon(exported.checks.map(({name,passed}) => ({name,passed}))));
    for (const kind of ['word','excel']) check('exports.' + kind + '.bytes-stable-during-inspection',
      hash(await fs.readFile(exportFiles[kind].path)) === exportFiles[kind].sha256);
  });
  else check('exports.actual-ooxml-inspection-passed',false,{error:'Missing exportReportFile'});
  if (config.sourcesBeforeFile) await guarded('sources.identity',async () => {
    const before = await loadJSON(config.sourcesBeforeFile), root = resolve(config.sourceRoot || '.');
    const drift = [];
    for (const [file,expected] of Object.entries(before)) if (!sha(expected) || hash(await fs.readFile(path.resolve(root,file))) !== expected) drift.push(file);
    check('sources.no-change-during-real-model-validation', Object.keys(before).length > 0 && drift.length === 0,drift);
  });
  return {kind:'pogo-real-model-office-acceptance',schemaVersion:1,passed:checks.length > 0 && checks.every(item => item.passed),
    checkedAt:new Date().toISOString(),model:{provider:provenance.provider,model:provenance.model,streams:streams.length},checks,scenarios:scenarioReports,errors,
    scope:'Real provider responses driving registered docs_office tools against the same browser editor. Evidence is phase-scoped; final assistant prose is not proof. Only synthetic files and listed formula/style properties are covered.'};
}

async function selfTest() {
  const message = (role,content) => ({role,content});
  const prompt = text => message('user',[{type:'text',text}]);
  const use = (id,input) => message('assistant',[{type:'tool_use',id,name:'docs_office',input}]);
  const result = (id,body) => message('user',[{type:'tool_result',tool_use_id:id,text:JSON.stringify(body)}]);
  const m = [prompt('edit'),use('read1',{action:'command',operation:'word.read'}),result('read1',{ok:true,result:{text:'before'}}),prompt('repair'),use('read2',{action:'command',operation:'word.read'}),result('read2',{ok:true})];
  const w = phaseWindow(m,{name:'edit',exactPrompt:'edit'});
  assert.equal(w.messages.length,3,'later repair turn must be excluded');
  assert.equal(pairedCalls(w).calls.length,1);
  assert.equal(successful(pairedCalls(w).calls[0]),true);
  const bad = pairedCalls({messages:[use('wrong',{action:'command'}),result('wrong',{ok:false,error:'stale'}),use('repair',{action:'command'}),result('repair',{ok:true})]}).calls;
  assert.equal(bad.every(successful),false,'successful repair must not hide earlier error');
  assert.equal(pairedCalls({messages:[use('missing',{})]}).errors.length,1);
  assert.equal(pairedCalls({messages:[result('orphan',{ok:true})]}).errors.length,1);
  assert.equal(pairedCalls({messages:[use('same',{}),use('same',{}),result('same',{ok:true})]}).errors.length,1);
  assert.throws(() => phaseWindow([prompt('same'),prompt('same')],{name:'edit',exactPrompt:'same'}));
  const mutation = {input:{action:'command',operation:'history.undo',operation_id:'native_undo_001',document_session_id:'s',args:{}}};
  assert.equal(uniqueMutations([mutation,mutation]).calls.length,1,'identical retry should not count as two native undos');
  assert.equal(uniqueMutations([mutation,{input:{...mutation.input,operation:'history.redo'}}]).errors.length,1);
  const snapshot = {kind:'cell',sheet:'销售',range:'$B$2:$E$2',values:[[120,5,600,'human']],inputs:[['120','5','=B2*C2','human']]};
  assert.equal(cells(snapshot).get('销售!E2').value,'human');
  assert.equal(cells(snapshot).get('销售!D2').input,'=B2*C2');
  assert.equal(formula("=SUM('销售'!D2:D4)"),'SUM(销售!D2:D4)');
  const checks = [];
  const check = (name,passed) => checks.push({name,passed:!!passed});
  const manifest = {excel:{target:'销售!C2',human_target:'销售!E2',edited:5,original:3,calculated_edited:{'销售!D2':600},calculated_original:{'销售!D2':360},formulas:{'销售!D2':'B2*C2'}}};
  stateAssertions(check,'valid',[snapshot],'excel',manifest,'edited','human');
  assert.equal(checks.every(item => item.passed),true);
  checks.length = 0;
  stateAssertions(check,'bad',[{...snapshot,values:[[120,3,360,'lost']]},snapshot],'excel',manifest,'edited','human');
  assert.equal(checks.find(item => item.name.endsWith('no-conflicting-human-observation')).passed,false);
  assert.equal(checks.find(item => item.name.endsWith('no-conflicting-target-observation')).passed,false);
  const extra = [];
  const test = (name,fn) => { fn(); extra.push(name); };
  const receipt = {name:'docs_office',id:'model_call_read_001',input:{action:'command',operation:'word.read',document_session_id:'word_session_001'},
    response:{parsed:{ok:true,operationId:'host_read_receipt_001',result:{kind:'word',text:'original\n'}},text:'{}'}};
  const replacement = {input:{args:{expectedReadId:'host_read_receipt_001'}}};
  test('same-session successful read receipt',() => assert.equal(wordPrecondition(replacement,[receipt],'word_session_001').passed,true));
  test('tool-call ID is not host read receipt',() => assert.equal(wordPrecondition({input:{args:{expectedReadId:receipt.id}}},[receipt],'word_session_001').passed,false));
  test('different document session cannot bind receipt',() => assert.equal(wordPrecondition(replacement,[receipt],'other_session_001').passed,false));
  test('failed read cannot bind receipt',() => assert.equal(wordPrecondition(replacement,[{...receipt,response:{...receipt.response,is_error:true}}],'word_session_001').passed,false));
  test('unknown receipt cannot bind',() => assert.equal(wordPrecondition({input:{args:{expectedReadId:'unknown_receipt_001'}}},[receipt],'word_session_001').passed,false));
  test('reference and exact text are mutually exclusive',() => assert.equal(wordPrecondition({input:{args:{expectedReadId:'host_read_receipt_001',expectedText:'original\n'}}},[receipt],'word_session_001').passed,false));
  test('legacy exact text retains final newline',() => assert.equal(wordPrecondition({input:{args:{expectedText:'original\n'}}},[receipt],'word_session_001').passed,true));
  test('legacy omitted final newline is rejected',() => assert.equal(wordPrecondition({input:{args:{expectedText:'original'}}},[receipt],'word_session_001').passed,false));
  test('identical cached receipt retry is accepted',() => assert.equal(wordPrecondition(replacement,[receipt,receipt],'word_session_001').passed,true));
  test('same receipt cannot identify conflicting snapshots',() => assert.equal(wordPrecondition(replacement,[receipt,{...receipt,response:{...receipt.response,parsed:{...receipt.response.parsed,result:{kind:'word',text:'different\n'}}}}],'word_session_001').passed,false));
  const skillPath = '/reviewed/pogo/skills/builtin/docs/SKILL.md';
  const skill = {name:'read',input:{path:skillPath},response:{text:'1\t# Reviewed docs skill'}};
  test('exact reviewed skill path is permitted',() => assert.equal(reviewedSkillRead(skill,skillPath),true));
  test('other path is not permitted',() => assert.equal(reviewedSkillRead({...skill,input:{path:'/private/config.json'}},skillPath),false));
  test('one reviewed paths entry is permitted',() => assert.equal(reviewedSkillRead({...skill,input:{paths:[skillPath]}},skillPath),true));
  test('mixed multi-file read is refused',() => assert.equal(reviewedSkillRead({...skill,input:{paths:[skillPath,'/private/config.json']}},skillPath),false));
  test('relative path cannot evade review',() => assert.equal(reviewedSkillRead({...skill,input:{path:'skills/builtin/docs/SKILL.md'}},skillPath),false));
  test('skill read cannot carry unrelated arguments',() => assert.equal(reviewedSkillRead({...skill,input:{path:skillPath,action:'write'}},skillPath),false));
  test('plain successful reviewed skill read accepted',() => assert.equal(successful({...skill,auxiliarySkillRead:true}),true));
  test('errored reviewed skill read not hidden',() => assert.equal(successful({...skill,auxiliarySkillRead:true,response:{...skill.response,is_error:true}}),false));
  const word = {original:'POGO_WORD_TARGET_001：原始唯一正文。',edited:'POGO_WORD_TARGET_001：已更新唯一正文。'};
  const before = [{kind:'word',text:'保留人工内容。\n' + word.original + '\n'}];
  const full = {search:word.original,replacement:word.edited,expectedCount:1};
  const suffix = {search:'原始唯一正文。',replacement:'已更新唯一正文。',expectedCount:1};
  test('full marked Word target replacement remains valid',() => assert.equal(limitedWordReplacement(full,before,word),true));
  test('unique Word suffix reconstructs requested target',() => assert.equal(limitedWordReplacement(suffix,before,word),true));
  test('wrong Word suffix replacement is rejected',() => assert.equal(limitedWordReplacement({...suffix,replacement:'错误正文。'},before,word),false));
  test('Word suffix repeated elsewhere in live read is rejected',() => assert.equal(limitedWordReplacement(suffix,[{kind:'word',text:before[0].text + suffix.search}],word),false));
  test('Word suffix repeated in original target is rejected',() => assert.equal(limitedWordReplacement({search:'原始',replacement:'更新',expectedCount:1},[{kind:'word',text:'原始原始'}],{original:'原始原始',edited:'更新原始'}),false));
  test('Word replacement must request expectedCount one',() => assert.equal(limitedWordReplacement({...suffix,expectedCount:2},before,word),false));
  test('Word replacement cannot omit expectedCount',() => assert.equal(limitedWordReplacement({search:suffix.search,replacement:suffix.replacement},before,word),false));
  test('Word replacement cannot rely on later unambiguous read',() => assert.equal(limitedWordReplacement(suffix,[{kind:'word',text:before[0].text + suffix.search},...before],word),false));
  test('Word replacement cannot match an empty search',() => assert.equal(limitedWordReplacement({search:'',replacement:word.edited,expectedCount:1},before,word),false));
  test('single A1 cell accepted',() => assert.equal(singleCellA1('C2'),'C2'));
  test('degenerate A1 range is same single cell',() => assert.equal(singleCellA1('C2:C2'),'C2'));
  test('absolute A1 range is same single cell',() => assert.equal(singleCellA1('$c$2:$C$2'),'C2'));
  test('multi-row A1 range rejected',() => assert.equal(singleCellA1('C2:C3'),null));
  test('multi-column A1 range rejected',() => assert.equal(singleCellA1('C2:D2'),null));
  test('zero A1 row rejected',() => assert.equal(singleCellA1('C0'),null));
  test('A1 column beyond Excel bound rejected',() => assert.equal(singleCellA1('XFE1'),null));
  test('A1 row beyond Excel bound rejected',() => assert.equal(singleCellA1('XFD1048577'),null));
  test('last bounded Excel cell accepted',() => assert.equal(singleCellA1('XFD1048576:XFD1048576'),'XFD1048576'));
  test('A1 formula cannot masquerade as range',() => assert.equal(singleCellA1('=C2'),null));
  test('A1 comma list cannot masquerade as single cell',() => assert.equal(singleCellA1('C2,C2'),null));
  test('malformed A1 absolute markers rejected',() => assert.equal(singleCellA1('C$2$'),null));
  console.log(JSON.stringify({passed:true,tests:15 + extra.length,legacyGroups:15,extendedChecks:extra.length,
    scope:'Evidence parser and negative controls only; no model/editor calls'}));
}

if (process.argv.includes('--self-test')) await selfTest();
else if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  const data = process.env.OFFICE_CHECK_DATA;
  const input = process.argv[2] || (data && path.join(data,'verification/model-check-input.json'));
  if (!input) throw new Error('Pass a model-check-input.json path or OFFICE_CHECK_DATA.');
  const absoluteInput = path.resolve(input);
  const config = JSON.parse(await fs.readFile(absoluteInput,'utf8'));
  const output = config.outputFile ? path.resolve(path.dirname(absoluteInput),config.outputFile) : path.join(path.dirname(absoluteInput),'real-model-acceptance.json');
  const report = await verifyEvidence(config,path.dirname(absoluteInput));
  await fs.mkdir(path.dirname(output),{recursive:true});
  await fs.writeFile(output,JSON.stringify(report,null,2) + '\n');
  console.log(JSON.stringify({passed:report.passed,checks:report.checks.length,failed:report.checks.filter(check => !check.passed),report:output}));
  if (!report.passed) process.exitCode = 1;
}
