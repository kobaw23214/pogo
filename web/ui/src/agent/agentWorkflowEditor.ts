import { workflowRepeatIssues, workflowPolicyValid } from './agentWorkflowRepeat';
import { workflowSchemaValid, workflowValueMatches } from './agentWorkflowCode';
import { AGENT_WORKFLOW_MAX_STEPS } from './agentWorkflowLimits';
import type { AgentWorkflowCondition, AgentWorkflowDefinition, AgentWorkflowInput, AgentWorkflowLayout, AgentWorkflowNodeResources, AgentWorkflowResourceCatalog, AgentWorkflowStep, AgentWorkflowToolApprovalMode } from '@/api/agentWorkflows';
import { guaranteedWorkflowPredecessors, workflowGraphKey, workflowGraphProblems } from './agentWorkflowDag';

export type WorkflowEditOperation = Record<string, unknown>;
export interface WorkflowEditorIssue { stepId?: string; field: string; code: string }
export interface WorkflowChange { kind: 'changed' | 'added' | 'removed' | 'reordered'; field: string; stepId?: string; title?: string; before: string; after: string }
export type WorkflowResourceField = 'skills' | 'shortcuts';
export interface WorkflowResourceRow { id: string; title: string; version?: string; description?: string; selected: boolean; available: boolean; reason?: string; missing?: boolean; snapshot?: boolean; pending?: boolean }
/** Historical labels and versions always come from the run snapshot, never today's catalog. */
export function workflowNodeResourceRows(step: AgentWorkflowStep, field: WorkflowResourceField, catalog?: AgentWorkflowResourceCatalog, snapshot?: AgentWorkflowNodeResources, historical = false): WorkflowResourceRow[] {
  const selected = new Set(step[field] || []);
  if (historical) {
    const recorded = snapshot?.[field] || [];
    return [...new Set([...selected, ...recorded.map(item => item.id)])].map(id => {
      const item = recorded.find(value => value.id === id);
      return { id, title: item ? 'name' in item ? item.name : item.title : id, version: item?.version, selected: true, available: !!item, missing: !item, snapshot: !!item };
    });
  }
  const available = catalog?.[field] || [];
  return [...available.map(item => ({ id: item.id, title: 'name' in item ? item.name : item.title, version: item.version, description: 'description' in item ? item.description : undefined, selected: selected.has(item.id), available: item.available, reason: item.reason })), ...[...selected].filter(id => !available.some(item => item.id === id)).map(id => ({ id, title: id, selected: true, available: false, missing: !!catalog, pending: !catalog }))];
}
const stepFields = ['kind', 'title', 'task', 'prompt', 'output_file'] as const;
const identifier = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/;
const refPattern = /^(?:inputs\.([A-Za-z][A-Za-z0-9_-]{0,63})(?:\.[A-Za-z0-9_-]+)*|steps\.([A-Za-z][A-Za-z0-9_-]{0,63})\.(summary|artifact|feedback|output(?:\.[A-Za-z0-9_-]+)*))$/;
const normalizedInput = (i: AgentWorkflowInput) => ({ key: i.key, label: i.label || '', required: i.required === true, type: i.type || 'text', options: (i.options || []).map(option => ({ value: option.value, label: option.label || '' })), default: i.default || '' });
const normalizedCondition = (condition?: AgentWorkflowCondition) => condition ? { source: condition.source, operator: condition.operator, value: condition.value || '' } : null;
const conditionKey = (condition?: AgentWorkflowCondition) => JSON.stringify(normalizedCondition(condition));
const resourceFields = ['skills', 'shortcuts'] as const;
const normalizedStep = (s: AgentWorkflowStep) => ({ id: s.id, kind: s.kind, title: s.title || '', task: s.task || '', prompt: s.prompt || '', output_file: s.output_file || '', condition: normalizedCondition(s.condition), code: s.code || null, iteration: s.iteration || null, loop: s.loop || null, policy: s.policy || null, skills: s.skills || [], shortcuts: s.shortcuts || [] });
export const cloneWorkflowDefinition = (definition: AgentWorkflowDefinition): AgentWorkflowDefinition => JSON.parse(JSON.stringify(definition));
/** Missing legacy fields remain absent on disk and mean the established standard mode. */
export const workflowToolApprovalMode = (definition: AgentWorkflowDefinition): AgentWorkflowToolApprovalMode => definition.tool_approval_mode || 'standard';
export function workflowSemanticKey(definition: AgentWorkflowDefinition): string {
  return JSON.stringify({ name: definition.name, description: definition.description || '', tool_approval_mode: workflowToolApprovalMode(definition), inputs: (definition.inputs || []).map(normalizedInput), steps: definition.steps.map(normalizedStep), graph: workflowGraphKey(definition.graph) });
}
export function sameWorkflowContent(a: AgentWorkflowDefinition, b: AgentWorkflowDefinition): boolean { return workflowSemanticKey(a) === workflowSemanticKey(b); }
/** A delayed save acknowledgement must never roll back a newer polled revision. */
export function newestWorkflowDefinition(saved: AgentWorkflowDefinition, candidates: (AgentWorkflowDefinition | null | undefined)[]): AgentWorkflowDefinition {
  return candidates.reduce<AgentWorkflowDefinition>((latest, value) => value && value.id === saved.id && (!saved.agent_id || !value.agent_id || value.agent_id === saved.agent_id) && value.revision > latest.revision ? value : latest, saved);
}
/** Resolve again after a confirmation dialog; polling may have settled the conflict. */
export function workflowReloadTarget(base: AgentWorkflowDefinition, pending: AgentWorkflowDefinition | null, latest?: AgentWorkflowDefinition): AgentWorkflowDefinition | null {
  if (!pending || pending.id !== base.id || base.agent_id && pending.agent_id && base.agent_id !== pending.agent_id || pending.revision < base.revision) return null;
  return newestWorkflowDefinition(pending, [base, latest]);
}
/** Both GETs and delayed idempotent PUT receipts must respect the known revision floor. */
export function workflowLayoutIsCurrent(layout: AgentWorkflowLayout, definition: AgentWorkflowDefinition, minimumLayoutRevision: number, minimumDefinitionRevision = definition.revision): boolean {
  return layout.workflow_id === definition.id && layout.layout_revision >= minimumLayoutRevision && layout.definition_revision >= Math.max(definition.revision, minimumDefinitionRevision);
}
/** Canvas emits null for insertion after the virtual input boundary. */
export function workflowInsertionIndex(definition: AgentWorkflowDefinition, afterId: string | null): number {
  if (afterId === null || afterId === '__input') return 0;
  const index = definition.steps.findIndex(step => step.id === afterId);
  return index < 0 ? definition.steps.length : index + 1;
}
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const ids = (definition: AgentWorkflowDefinition) => definition.steps.map(step => step.id);
const stepValue = (step: AgentWorkflowStep, field: typeof stepFields[number]) => step[field] || '';
function inputText(value?: AgentWorkflowInput[]): string { return (value || []).map(i => `${i.key}${i.label ? ` (${i.label})` : ''}${i.required ? ' *' : ''} · ${i.type || 'text'}${i.options?.length ? ` [${i.options.map(option => `${option.label}: ${option.value}`).join(', ')}]` : ''}${i.default ? ` = ${i.default}` : ''}`).join('\n'); }

/** Exact local edits: retain stable IDs and never replace another editor's whole graph. */
export function workflowEditOperations(base: AgentWorkflowDefinition, draft: AgentWorkflowDefinition): WorkflowEditOperation[] {
  const operations: WorkflowEditOperation[] = [];
  const metadata: WorkflowEditOperation = { op: 'set_metadata' };
  if (base.name !== draft.name) metadata.name = draft.name;
  if ((base.description || '') !== (draft.description || '')) metadata.description = draft.description || '';
  if (workflowToolApprovalMode(base) !== workflowToolApprovalMode(draft)) metadata.tool_approval_mode = workflowToolApprovalMode(draft);
  if (!same((base.inputs || []).map(normalizedInput), (draft.inputs || []).map(normalizedInput))) metadata.inputs = draft.inputs || [];
  if (Object.keys(metadata).length > 1) operations.push(metadata);
  const target = new Set(ids(draft));
  const working = base.steps.map(step => ({ ...step }));
  for (let i = working.length - 1; i >= 0; i--) {
    if (!target.has(working[i]!.id)) { operations.push({ op: 'remove_step', step_id: working[i]!.id }); working.splice(i, 1); }
  }
  for (const [index, step] of draft.steps.entries()) {
    const at = working.findIndex(item => item.id === step.id);
    if (at < 0) { operations.push({ op: 'add_step', step: { ...step }, index }); working.splice(index, 0, { ...step }); continue; }
    if (at !== index) { operations.push({ op: 'move_step', step_id: step.id, index }); working.splice(index, 0, working.splice(at, 1)[0]!); }
    const previous = working[index]!;
    const patch: Record<string, unknown> = {};
    for (const field of stepFields) if (stepValue(previous, field) !== stepValue(step, field)) patch[field] = stepValue(step, field);
    for (const field of resourceFields) if (!same(previous[field] || [], step[field] || [])) patch[field] = [...(step[field] || [])];
    if (conditionKey(previous.condition) !== conditionKey(step.condition)) patch.condition = step.condition || null;
    for (const field of ['code','iteration','loop','policy'] as const) if (!same(previous[field] || null, step[field] || null)) patch[field] = step[field] || null;
    if (Object.keys(patch).length) operations.push({ op: 'update_step', step_id: step.id, patch });
    working[index] = { ...step };
  }
  if (workflowGraphKey(base.graph) !== workflowGraphKey(draft.graph)) operations.push({ op: 'set_graph', graph: draft.graph || null });
  return operations;
}

export function workflowChanges(base: AgentWorkflowDefinition, next: AgentWorkflowDefinition): WorkflowChange[] {
  const changes: WorkflowChange[] = [];
  for (const field of ['name', 'description'] as const) {
    if ((base[field] || '') !== (next[field] || '')) changes.push({ kind: 'changed', field, before: base[field] || '', after: next[field] || '' });
  }
  if (workflowToolApprovalMode(base) !== workflowToolApprovalMode(next)) changes.push({ kind: 'changed', field: 'tool_approval_mode', before: workflowToolApprovalMode(base), after: workflowToolApprovalMode(next) });
  if (!same((base.inputs || []).map(normalizedInput), (next.inputs || []).map(normalizedInput))) changes.push({ kind: 'changed', field: 'inputs', before: inputText(base.inputs), after: inputText(next.inputs) });
  if (workflowGraphKey(base.graph) !== workflowGraphKey(next.graph)) changes.push({ kind: 'changed', field: 'graph', before: workflowGraphKey(base.graph), after: workflowGraphKey(next.graph) });
  const old = new Map(base.steps.map(step => [step.id, step]));
  const current = new Map(next.steps.map(step => [step.id, step]));
  for (const step of base.steps) if (!current.has(step.id)) changes.push({ kind: 'removed', field: 'step', stepId: step.id, title: step.title || step.id, before: step.task || step.prompt || '', after: '' });
  for (const step of next.steps) {
    const previous = old.get(step.id);
    if (!previous) { changes.push({ kind: 'added', field: 'step', stepId: step.id, title: step.title || step.id, before: '', after: step.task || step.prompt || '' }); continue; }
    for (const field of stepFields) if (stepValue(previous, field) !== stepValue(step, field)) changes.push({ kind: 'changed', field, stepId: step.id, title: step.title || step.id, before: stepValue(previous, field), after: stepValue(step, field) });
    for (const field of resourceFields) if (!same(previous[field] || [], step[field] || [])) changes.push({ kind: 'changed', field, stepId: step.id, title: step.title || step.id, before: (previous[field] || []).join('\n'), after: (step[field] || []).join('\n') });
    for (const field of ['code','iteration','loop','policy'] as const) if (!same(previous[field] || null, step[field] || null)) changes.push({ kind: 'changed', field, stepId: step.id, title: step.title || step.id, before: JSON.stringify(previous[field] || null, null, 2), after: JSON.stringify(step[field] || null, null, 2) });
    if (conditionKey(previous.condition) !== conditionKey(step.condition)) changes.push({ kind: 'changed', field: 'condition', stepId: step.id, title: step.title || step.id, before: JSON.stringify(previous.condition || null), after: JSON.stringify(step.condition || null) });
  }
  const common = new Set(base.steps.filter(step => current.has(step.id)).map(step => step.id));
  if (!same(ids(base).filter(id => common.has(id)), ids(next).filter(id => common.has(id)))) changes.push({ kind: 'reordered', field: 'order', before: base.steps.map(s => s.title || s.id).join(' → '), after: next.steps.map(s => s.title || s.id).join(' → ') });
  return changes;
}

/** Merge independent field edits; concurrent structural changes require review. */
export function mergeWorkflowEdits(base: AgentWorkflowDefinition, local: AgentWorkflowDefinition, remote: AgentWorkflowDefinition): { definition: AgentWorkflowDefinition; conflicts: string[] } {
  const merged = cloneWorkflowDefinition(remote);
  const conflicts: string[] = [];
  const oldGraph = workflowGraphKey(base.graph), localGraph = workflowGraphKey(local.graph), remoteGraph = workflowGraphKey(remote.graph);
  if (localGraph !== oldGraph) {
    if (remoteGraph !== oldGraph && remoteGraph !== localGraph) conflicts.push('graph');
    else if (local.graph) merged.graph = JSON.parse(JSON.stringify(local.graph));
    else delete merged.graph;
  }
  for (const field of ['name', 'description'] as const) {
    const previous = base[field] || '', ours = local[field] || '', theirs = remote[field] || '';
    if (ours === previous) continue;
    if (theirs !== previous && ours !== theirs) conflicts.push(field);
    else merged[field] = ours;
  }
  const previousMode = workflowToolApprovalMode(base), localMode = workflowToolApprovalMode(local), remoteMode = workflowToolApprovalMode(remote);
  if (localMode !== previousMode) {
    if (remoteMode !== previousMode && remoteMode !== localMode) conflicts.push('tool_approval_mode');
    else merged.tool_approval_mode = localMode;
  }
  const previousInputs = (base.inputs || []).map(normalizedInput), localInputs = (local.inputs || []).map(normalizedInput), remoteInputs = (remote.inputs || []).map(normalizedInput);
  if (!same(previousInputs, localInputs)) {
    if (!same(previousInputs, remoteInputs) && !same(localInputs, remoteInputs)) conflicts.push('inputs');
    else merged.inputs = JSON.parse(JSON.stringify(local.inputs || []));
  }
  const localStructure = !same(ids(base), ids(local)), remoteStructure = !same(ids(base), ids(remote));
  if (localStructure && remoteStructure && !same(ids(local), ids(remote))) conflicts.push('order');
  const baseSteps = new Map(base.steps.map(step => [step.id, step]));
  const localSteps = new Map(local.steps.map(step => [step.id, step]));
  const remoteSteps = new Map(remote.steps.map(step => [step.id, step]));
  const resolved = new Map<string, AgentWorkflowStep>();
  for (const id of new Set([...baseSteps.keys(), ...localSteps.keys(), ...remoteSteps.keys()])) {
    const previous = baseSteps.get(id), ours = localSteps.get(id), theirs = remoteSteps.get(id);
    if (!previous) {
      if (ours && theirs && !same(normalizedStep(ours), normalizedStep(theirs))) conflicts.push(`steps.${id}`);
      if (ours || theirs) resolved.set(id, JSON.parse(JSON.stringify((ours || theirs)!)));
    } else if (!ours) {
      if (theirs && !same(normalizedStep(previous), normalizedStep(theirs))) conflicts.push(`steps.${id}`);
    } else if (!theirs) {
      if (!same(normalizedStep(previous), normalizedStep(ours))) conflicts.push(`steps.${id}`);
    } else {
      const step: AgentWorkflowStep = JSON.parse(JSON.stringify(theirs));
      for (const field of stepFields) {
        const old = stepValue(previous, field), mine = stepValue(ours, field), latest = stepValue(theirs, field);
        if (mine === old) continue;
        if (latest !== old && mine !== latest) conflicts.push(`steps.${id}.${field}`);
        else Object.assign(step, { [field]: mine });
      }
      for (const field of resourceFields) {
        const old = previous[field] || [], mine = ours[field] || [], latest = theirs[field] || [];
        if (same(mine, old)) continue;
        if (!same(latest, old) && !same(mine, latest)) conflicts.push(`steps.${id}.${field}`);
        else step[field] = [...mine];
      }
      for (const field of ['code','iteration','loop','policy'] as const) {
        if (same(previous[field] || null, ours[field] || null)) continue;
        if (!same(previous[field] || null, theirs[field] || null) && !same(ours[field] || null, theirs[field] || null)) conflicts.push(`steps.${id}.${field}`);
        else if (ours[field]) Object.assign(step,{[field]:JSON.parse(JSON.stringify(ours[field]))});
        else delete step[field];
      }
      if (conditionKey(previous.condition) !== conditionKey(ours.condition)) {
        if (conditionKey(previous.condition) !== conditionKey(theirs.condition) && conditionKey(ours.condition) !== conditionKey(theirs.condition)) conflicts.push(`steps.${id}.condition`);
        else if (ours.condition) step.condition = { ...ours.condition };
        else delete step.condition;
      }
      resolved.set(id, step);
    }
  }
  const order = localStructure && !remoteStructure ? ids(local) : ids(remote);
  merged.steps = order.flatMap(id => resolved.has(id) ? [resolved.get(id)!] : []);
  return { definition: merged, conflicts: [...new Set(conflicts)] };
}

const byteLength = (value: string) => new TextEncoder().encode(value).length;
const validText = (value: string, max: number) => !value.includes('\0') && byteLength(value) <= max;
export function safeWorkflowOutput(value: string): boolean {
  return value.length > 0 && byteLength(value) <= 1024 && !/[\\\0\r\n:]/.test(value) && !value.startsWith('/') && value.split('/').every(part => part !== '' && part !== '.' && part !== '..');
}
/** Mirrors the supported editing contract only; the backend remains authoritative. */
export function workflowEditorIssues(definition: AgentWorkflowDefinition): WorkflowEditorIssue[] {
  const issues: WorkflowEditorIssue[] = [];
  const add = (code: string, field: string, stepId?: string) => issues.push({ code, field, stepId });
  if (!definition.name.trim() || !validText(definition.name, 256)) add('name', 'name');
  if (!validText(definition.description || '', 8192)) add('description', 'description');
  if (!['standard', 'strict', 'auto'].includes(workflowToolApprovalMode(definition))) add('approvalMode', 'tool_approval_mode');
  if ((definition.inputs || []).length > 32) add('inputCount', 'inputs');
  const inputs = new Set<string>();
  for (const input of definition.inputs || []) {
    if (!identifier.test(input.key) || inputs.has(input.key) || !validText(input.label || '', 256)) add('input', 'inputs');
    const type = input.type || 'text', options = input.options || [], values = new Set<string>();
    if (!['text', 'boolean', 'select', 'number', 'integer', 'object', 'array'].includes(type)) add('inputType', 'inputs');
    if (type === 'select' && !options.length || !['boolean', 'select'].includes(type) && options.length || options.length > 64) add('inputOptions', 'inputs');
    for (const option of options) {
      if (!option.value.trim() || !validText(option.value, 256) || !validText(option.label || '', 256) || values.has(option.value)) add('inputOptions', 'inputs');
      values.add(option.value);
    }
    if (type === 'boolean' && options.length && (options.length !== 2 || !values.has('yes') || !values.has('no'))) add('inputOptions', 'inputs');
    if (!validText(input.default || '', 16384) || input.default && (type === 'boolean' && !['yes', 'no'].includes(input.default) || type === 'select' && !values.has(input.default))) add('inputDefault', 'inputs');
    if (input.default && ['number','integer','object','array'].includes(type)) { try { if (!workflowValueMatches(type as 'number'|'integer'|'object'|'array', JSON.parse(input.default))) add('inputDefault', 'inputs'); } catch { add('inputDefault', 'inputs'); } }
    inputs.add(input.key);
  }
  if (definition.steps.length < 1 || definition.steps.reduce((n,s)=>n+1+(s.iteration?.body||s.loop?.body?1:0),0) > AGENT_WORKFLOW_MAX_STEPS) add('stepCount', 'steps');
  const previous = new Set<string>(), outputs = new Set<string>();
  for (const step of definition.steps) {
    if (!(definition.graph ? ['agent', 'code', 'iteration', 'loop', 'confirm', 'condition', 'parallel', 'join'] : ['agent', 'code', 'iteration', 'loop', 'confirm']).includes(step.kind)) add('kind', 'kind', step.id);
    if (!identifier.test(step.id) || previous.has(step.id)) add('stepId', 'id', step.id);
    if (!validText(step.title || '', 256)) add('title', 'title', step.id);
    for (const field of resourceFields) if (step[field]?.length && (step.kind !== 'agent' || step[field]!.length > 32 || new Set(step[field]).size !== step[field]!.length || step[field]!.some(id => !id || /[\s\p{Cc}]/u.test(id) || !validText(id, 256)))) add('nodeResources', field, step.id);
    if (step.kind === 'agent' && (!(step.task || '').trim() || !!step.prompt) || step.kind === 'confirm' && (!(step.prompt || '').trim() || !!step.task || !!step.output_file)) add('instruction', step.kind === 'agent' ? 'task' : 'prompt', step.id);
    if (['condition', 'parallel', 'join'].includes(step.kind) && (step.task || step.prompt || step.output_file)) add('controlFields', 'kind', step.id);
    if (step.kind !== 'condition' && step.condition) add('condition', 'condition', step.id);
    const predecessors = guaranteedWorkflowPredecessors(definition, step.id);
    for (const issue of workflowRepeatIssues(definition, step)) add(issue, step.kind, step.id);
    if (step.policy && (step.kind !== 'code' || !workflowPolicyValid(step.policy,step.code?.outputs))) add('codePolicy','policy',step.id);
    if (step.kind === 'code') {
      const code = step.code;
      if (!code || !['python', 'javascript'].includes(code.language) || !code.source.trim() || !validText(code.source, 65536) || step.task || step.prompt || step.output_file || (code.timeout_seconds !== undefined && (!Number.isInteger(code.timeout_seconds) || code.timeout_seconds < 1 || code.timeout_seconds > 60))) add('code', 'code', step.id);
      if (code) {
        for (const [key, binding] of Object.entries(code.inputs || {})) {
          const literal = Object.prototype.hasOwnProperty.call(binding, 'value'), source = binding.source?.match(refPattern);
          if (!identifier.test(key) || literal === !!binding.source || literal && !workflowValueMatches(binding.type, binding.value) || binding.source && (!source || source[1] && !inputs.has(source[1]) || source[2] && !predecessors.has(source[2]))) add('codeBinding', 'code', step.id);
        }
        if (!Object.keys(code.outputs || {}).length || Object.entries(code.outputs || {}).some(([key, schema]) => !identifier.test(key) || !workflowSchemaValid(schema))) add('codeOutput', 'code', step.id);
      }
    } else if (step.code) add('code', 'code', step.id);
    if (step.kind === 'condition') {
      const condition = step.condition, source = condition?.source.match(refPattern), operators = ['equals', 'not_equals', 'contains', 'not_contains', 'is_empty', 'not_empty', 'gt', 'gte', 'lt', 'lte'];
      if (!condition || !source || source[1] && !inputs.has(source[1]) || source[2] && !predecessors.has(source[2]) || !operators.includes(condition.operator) || !validText(condition.value || '', 16384) || condition && ['is_empty', 'not_empty'].includes(condition.operator) && !!condition.value) add('condition', 'condition', step.id);
      if (condition && ['gt', 'gte', 'lt', 'lte'].includes(condition.operator) && (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test((condition.value || '').trim()) || !Number.isFinite(Number(condition.value)))) add('conditionNumber', 'condition', step.id);
    }
    if (!validText(step.task || '', 16384) || !validText(step.prompt || '', 16384)) add('instructionLength', 'task', step.id);
    if (step.output_file) {
      if (!safeWorkflowOutput(step.output_file) || outputs.has(step.output_file)) add('output', 'output_file', step.id);
      if (definition.graph && step.output_file.split('/')[0]!.toLowerCase() === 'inputs') add('reservedOutput', 'output_file', step.id);
      outputs.add(step.output_file);
    }
    for (const field of ['task', 'prompt'] as const) {
      const text = step[field] || '';
      if (/[{}]{2}/.test(text.replace(/\{\{\s*([^{}]*?)\s*\}\}/g, ''))) add('template', field, step.id);
      for (const match of text.matchAll(/\{\{\s*([^{}]*?)\s*\}\}/g)) {
        const reference = match[1]!.trim().match(refPattern);
        if (!reference || reference[1] && !inputs.has(reference[1]) || reference[2] && !predecessors.has(reference[2])) add('reference', field, step.id);
      }
    }
    previous.add(step.id);
  }
  for (const problem of workflowGraphProblems(definition)) add(`graph_${problem}`, 'graph');
  return issues;
}

export function workflowVariables(definition: AgentWorkflowDefinition, stepId: string): { value: string; label: string }[] {
  const previous = guaranteedWorkflowPredecessors(definition, stepId);
  return [
    ...(definition.inputs || []).filter(input => identifier.test(input.key)).map(input => ({ value: `{{ inputs.${input.key} }}`, label: input.label || input.key })),
    ...definition.steps.filter(step => previous.has(step.id)).flatMap(step => [...(step.kind === 'join' ? ['summary'] : ['summary', 'artifact', 'feedback']), ...(step.kind === 'code' ? Object.keys(step.code?.outputs || {}).map(key => `output.${key}`) : step.kind === 'iteration' ? ['output.items','output.errors','output.total','output.completed','output.failed'] : step.kind === 'loop' ? ['output.state','output.iterations','output.limit_reached'] : step.kind === 'join' ? ['output'] : [])].map(field => ({ value: `{{ steps.${step.id}.${field} }}`, label: `${step.title || step.id} · ${field}` }))),
  ];
}

export function uniqueWorkflowOutput(value: string, steps: AgentWorkflowStep[]): string {
  if (!value) return '';
  const used = new Set(steps.map(step => step.output_file));
  const slash = value.lastIndexOf('/'), dot = value.lastIndexOf('.');
  const suffixAt = dot > slash + 1 ? dot : value.length;
  for (let i = 2; i < 1000; i++) {
    const candidate = `${value.slice(0, suffixAt)}_copy${i}${value.slice(suffixAt)}`;
    if (!used.has(candidate)) return candidate;
  }
  return '';
}
