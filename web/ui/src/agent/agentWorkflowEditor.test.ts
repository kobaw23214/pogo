import { describe, expect, it } from 'vitest';
import type { AgentWorkflowDefinition, AgentWorkflowLayout, AgentWorkflowNodeResources, AgentWorkflowResourceCatalog, AgentWorkflowStep } from '@/api/agentWorkflows';
import { cloneWorkflowDefinition, mergeWorkflowEdits, newestWorkflowDefinition, sameWorkflowContent, safeWorkflowOutput, uniqueWorkflowOutput, workflowChanges, workflowEditOperations, workflowEditorIssues, workflowInsertionIndex, workflowLayoutIsCurrent, workflowNodeResourceRows, workflowReloadTarget, workflowVariables } from './agentWorkflowEditor';
import { AGENT_WORKFLOW_MAX_STEPS } from './agentWorkflowLimits';
import { upgradeWorkflowGraph } from './agentWorkflowDag';

function fixture(): AgentWorkflowDefinition {
  return { id: 'aw_fixture', agent_id: 'agent_fixture', revision: 3, definition_hash: 'sha_fixture', enabled_version: 'version_kept', name: '资料整理', description: '保留说明', inputs: [{ key: 'topic', label: '主题', required: true }], steps: [
    { id: 'collect', kind: 'agent', title: '资料整理', task: '整理 {{ inputs.topic }}，保留来源。', output_file: 'notes.md' },
    { id: 'review', kind: 'confirm', title: '本人确认', prompt: '确认提纲 {{ steps.collect.summary }}' },
    { id: 'report', kind: 'agent', title: '生成报告', task: '根据 {{ steps.collect.artifact }} 和 {{ steps.review.feedback }} 生成报告。', output_file: 'report.md' },
  ] };
}

describe('typed workflow inputs and explicit node resources', () => {
  it('treats legacy unspecified types as text while retaining typed edits and binding removals in patches', () => {
    const base = fixture(), typed = cloneWorkflowDefinition(base);
    typed.inputs![0]!.type = 'text'; expect(sameWorkflowContent(base, typed)).toBe(true);
    typed.inputs = [{ key: 'review', type: 'boolean', default: 'yes' }, { key: 'format', type: 'select', options: [{ value: 'report', label: '报告' }, { value: 'notes' }], default: 'report' }];
    typed.steps[0]!.skills = ['builtin:research/reader']; typed.steps[0]!.shortcuts = ['shortcut:private-id'];
    expect(workflowEditOperations(base, typed)).toEqual([{ op: 'set_metadata', inputs: typed.inputs }, { op: 'update_step', step_id: 'collect', patch: { skills: ['builtin:research/reader'], shortcuts: ['shortcut:private-id'] } }]);
    expect(workflowEditorIssues(typed)).toEqual(expect.arrayContaining([expect.objectContaining({ code: 'reference' })]));
    expect(workflowChanges(base, typed).map(value => value.field)).toEqual(['inputs', 'skills', 'shortcuts']);
    const cleared = cloneWorkflowDefinition(typed); delete cleared.steps[0]!.skills;
    expect(workflowEditOperations(typed, cleared)).toEqual([{ op: 'update_step', step_id: 'collect', patch: { skills: [] } }]);
  });

  it('merges independent resource and typed-input changes without sharing mutable arrays', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.steps[0]!.skills = ['builtin:reader']; remote.inputs![0]!.type = 'boolean'; remote.inputs![0]!.default = 'yes'; remote.steps[0]!.shortcuts = ['quick_card:example'];
    const merged = mergeWorkflowEdits(base, local, remote);
    expect(merged.conflicts).toEqual([]); expect(merged.definition.inputs![0]).toEqual(remote.inputs![0]);
    expect(merged.definition.steps[0]).toMatchObject({ skills: ['builtin:reader'], shortcuts: ['quick_card:example'] });
    merged.definition.steps[0]!.shortcuts!.push('shortcut:next'); expect(remote.steps[0]!.shortcuts).toEqual(['quick_card:example']);
    remote.steps[0]!.skills = ['builtin:other']; expect(mergeWorkflowEdits(base, local, remote).conflicts).toContain('steps.collect.skills');
    const clone = cloneWorkflowDefinition(local); clone.steps[0]!.skills!.push('builtin:second'); expect(local.steps[0]!.skills).toEqual(['builtin:reader']);
  });

  it('validates real enum values, boolean defaults and resource bindings only on Agent nodes', () => {
    const flow = fixture();
    flow.inputs = [{ key: 'need_review', type: 'boolean', default: 'yes' }, { key: 'format', type: 'select', options: [{ value: 'short' }, { value: 'long', label: '详细' }], default: 'long' }];
    flow.steps = [{ id: 'work', kind: 'agent', task: '生成报告', skills: ['builtin:research/reader'], shortcuts: ['shortcut:abc'] }];
    expect(workflowEditorIssues(flow)).toEqual([]);
    flow.inputs[0]!.default = 'true'; flow.inputs[1]!.options!.push({ value: 'short' }); flow.inputs[1]!.default = 'other'; flow.steps.push({ id: 'confirm', kind: 'confirm', prompt: '确认', skills: ['builtin:reader'] });
    expect(workflowEditorIssues(flow).map(issue => issue.code)).toEqual(expect.arrayContaining(['inputDefault', 'inputOptions', 'nodeResources']));
  });

  it('keeps missing bindings selectable for removal and uses frozen names and versions for history', () => {
    const step: AgentWorkflowStep = { id: 'work', kind: 'agent', task: '研究', skills: ['builtin:reader', 'old:removed'] };
    const catalog: AgentWorkflowResourceCatalog = { skills: [{ id: 'builtin:reader', name: 'Current reader', version: 'current', available: true }, { id: 'builtin:blocked', name: 'Blocked', version: 'v1', available: false, reason: 'missing tool' }], shortcuts: [], files: [] };
    expect(workflowNodeResourceRows(step, 'skills', catalog)).toEqual(expect.arrayContaining([expect.objectContaining({ id: 'old:removed', selected: true, available: false, missing: true }), expect.objectContaining({ id: 'builtin:blocked', selected: false, reason: 'missing tool' })]));
    const snapshot: AgentWorkflowNodeResources = { skills: [{ id: 'builtin:reader', name: 'Frozen reader', version: 'frozen', path: 'resources/skills/reader/SKILL.md', sha256: 'abc' }] };
    const historical = workflowNodeResourceRows(step, 'skills', catalog, snapshot, true);
    expect(historical).toEqual([expect.objectContaining({ id: 'builtin:reader', title: 'Frozen reader', version: 'frozen', snapshot: true }), expect.objectContaining({ id: 'old:removed', title: 'old:removed', available: false, missing: true })]);
    expect(historical.some(item => item.title === 'Current reader' || item.title === 'Blocked')).toBe(false);
  });
});

describe('internal workflow local edit patch', () => {
  it('keeps legacy approval fields absent and sends a versioned metadata change only for a different effective mode', () => {
    const original = fixture(), draft = cloneWorkflowDefinition(original);
    expect(Object.prototype.hasOwnProperty.call(draft, 'tool_approval_mode')).toBe(false);
    draft.tool_approval_mode = 'standard';
    expect(sameWorkflowContent(original, draft)).toBe(true);
    expect(workflowEditOperations(original, draft)).toEqual([]);
    draft.tool_approval_mode = 'auto';
    expect(sameWorkflowContent(original, draft)).toBe(false);
    expect(workflowEditOperations(original, draft)).toEqual([{ op: 'set_metadata', tool_approval_mode: 'auto' }]);
    expect(workflowChanges(original, draft)).toContainEqual({ kind: 'changed', field: 'tool_approval_mode', before: 'standard', after: 'auto' });
    expect(original.enabled_version).toBe('version_kept');
    expect(original.tool_approval_mode).toBeUndefined();
    expect(workflowEditorIssues({ ...draft, tool_approval_mode: 'unknown' as 'auto' })).toContainEqual({ code: 'approvalMode', field: 'tool_approval_mode', stepId: undefined });
  });
  it('merges approval changes with independent edits and reports competing mode changes', () => {
    const original = fixture(), local = cloneWorkflowDefinition(original), remote = cloneWorkflowDefinition(original);
    local.tool_approval_mode = 'auto'; remote.name = 'Renamed remotely';
    const independent = mergeWorkflowEdits(original, local, remote);
    expect(independent.conflicts).toEqual([]); expect(independent.definition.tool_approval_mode).toBe('auto'); expect(independent.definition.name).toBe('Renamed remotely');
    remote.tool_approval_mode = 'strict';
    expect(mergeWorkflowEdits(original, local, remote).conflicts).toContain('tool_approval_mode');
    expect(mergeWorkflowEdits(original, original, remote).definition.tool_approval_mode).toBe('strict');
  });

  it.each([null, '__input'])('inserts immediately after input when the canvas emits %s', afterId => {
    const original = fixture(), draft = cloneWorkflowDefinition(original), step: AgentWorkflowStep = { id: 'first', kind: 'agent', task: '先读取资料' };
    draft.steps.splice(workflowInsertionIndex(draft, afterId), 0, step);
    expect(draft.steps.map(item => item.id)).toEqual(['first', 'collect', 'review', 'report']);
    expect(workflowEditOperations(original, draft)).toEqual([{ op: 'add_step', step, index: 0 }]);
  });
  it('inserts after the selected real step and appends at output or without a selection', () => {
    const original = fixture();
    expect(workflowInsertionIndex(original, 'review')).toBe(2);
    expect(workflowInsertionIndex(original, '__output')).toBe(3);
    expect(workflowInsertionIndex(original, '')).toBe(3);
  });
  it('updates only changed fields and preserves identity, confirmation and outputs', () => {
    const original = fixture(), draft = cloneWorkflowDefinition(original);
    draft.steps[0]!.title = '结构化整理';
    expect(workflowEditOperations(original, draft)).toEqual([{ op: 'update_step', step_id: 'collect', patch: { title: '结构化整理' } }]);
    expect(original.steps[0]!.title).toBe('资料整理');
    expect(draft.steps[1]).toEqual(original.steps[1]);
  });
  it('produces stable-ID remove, insert and reordering operations against changing indices', () => {
    const original = fixture(), draft = cloneWorkflowDefinition(original);
    draft.steps = [original.steps[2]!, { id: 'extra', kind: 'agent', task: '补充说明' }, original.steps[0]!];
    expect(workflowEditOperations(original, draft)).toEqual([
      { op: 'remove_step', step_id: 'review' }, { op: 'move_step', step_id: 'report', index: 0 },
      { op: 'add_step', step: { id: 'extra', kind: 'agent', task: '补充说明' }, index: 1 },
    ]);
  });
  it('sends explicit empty values when removing optional properties or changing node kind', () => {
    const original = fixture(), draft = cloneWorkflowDefinition(original);
    draft.description = ''; draft.inputs = [];
    draft.steps[0] = { id: 'collect', kind: 'confirm', title: '资料整理', prompt: '请确认' };
    expect(workflowEditOperations(original, draft)).toEqual([
      { op: 'set_metadata', description: '', inputs: [] },
      { op: 'update_step', step_id: 'collect', patch: { kind: 'confirm', task: '', prompt: '请确认', output_file: '' } },
    ]);
  });
  it('clears confirmation fields when changing back to Agent and waits for a valid task', () => {
    const original = fixture(), draft = cloneWorkflowDefinition(original);
    draft.steps[1] = { ...draft.steps[1]!, kind: 'agent', task: '', prompt: '', output_file: '' };
    expect(workflowEditorIssues(draft)).toContainEqual(expect.objectContaining({ code: 'instruction', stepId: 'review' }));
    draft.steps[1]!.task = '继续整理';
    expect(workflowEditorIssues(draft)).toEqual([]);
    expect(workflowEditOperations(original, draft)).toEqual([{ op: 'update_step', step_id: 'review', patch: { kind: 'agent', task: '继续整理', prompt: '' } }]);
  });
  it('accepts 200 nodes and rejects the 201st before saving', () => {
    const definition = fixture(); definition.steps = Array.from({ length: AGENT_WORKFLOW_MAX_STEPS }, (_, i) => ({ id: `step_${i}`, kind: 'agent', task: '整理资料' }));
    expect(workflowEditorIssues(definition)).toEqual([]);
    definition.graph = upgradeWorkflowGraph(definition);
    expect(workflowEditorIssues(definition)).toEqual([]);
    delete definition.graph;
    definition.steps.push({ id: 'overflow', kind: 'agent', task: '超出上限' });
    expect(workflowEditorIssues(definition)).toContainEqual({ code: 'stepCount', field: 'steps', stepId: undefined });
  });
  it('fits the 402-operation budget when replacing all 200 nodes, metadata and graph', () => {
    const original = fixture(); original.steps = Array.from({ length: AGENT_WORKFLOW_MAX_STEPS }, (_, i) => ({ id: `original_${i}`, kind: 'agent', task: '原任务' }));
    const replacement = cloneWorkflowDefinition(original); replacement.name = '新的流程';
    replacement.steps = Array.from({ length: AGENT_WORKFLOW_MAX_STEPS }, (_, i) => ({ id: `replacement_${i}`, kind: 'agent', task: '新任务' }));
    replacement.graph = upgradeWorkflowGraph(replacement);
    expect(workflowEditOperations(original, replacement)).toHaveLength(402);
    const changed = cloneWorkflowDefinition(original); changed.name = '重新编排'; changed.steps.reverse(); changed.steps.forEach(step => { step.task = '新任务'; });
    expect(workflowEditOperations(original, changed)).toHaveLength(400);
  });
  it('does not treat layout-independent metadata, enabled versions or revision changes as local edits', () => {
    const original = fixture(), newer = { ...cloneWorkflowDefinition(original), revision: 4, definition_hash: 'next_hash', enabled_version: 'v_next', updated_at: 999, archived: true };
    expect(sameWorkflowContent(original, newer)).toBe(true);
    expect(workflowEditOperations(original, newer)).toEqual([]);
  });
  it('does not roll a newer polled version back when a saved response arrives late', () => {
    const saved = fixture(), newer = { ...fixture(), revision: 5, name: '对话继续修改' };
    expect(newestWorkflowDefinition(saved, [null, newer, { ...newer, id: 'other', revision: 20 }, { ...newer, agent_id: 'other_agent', revision: 30 }])).toEqual(newer);
  });
});

describe('concurrent chat/canvas changes', () => {
  it('rechecks a reload confirmation after polling settles or advances its conflict', async () => {
    let base = fixture(), pending: AgentWorkflowDefinition | null = { ...fixture(), revision: 4 };
    let confirm!: () => void;
    const confirmed = new Promise<void>(resolve => { confirm = resolve; });
    const reload = async () => { await confirmed; return workflowReloadTarget(base, pending); };
    const result = reload();
    base = { ...fixture(), revision: 5 }; pending = null; confirm();
    expect(await result).toBeNull();
    expect(workflowReloadTarget(base, { ...fixture(), revision: 4 })).toBeNull();
    expect(workflowReloadTarget(base, { ...fixture(), revision: 6 }, { ...fixture(), revision: 7 })?.revision).toBe(7);
    expect(workflowReloadTarget(base, { ...fixture(), revision: 6, id: 'different_workflow' })).toBeNull();
  });
  it('rejects late layout reads and save receipts below either known revision', () => {
    const definition = fixture(), old: AgentWorkflowLayout = { workflow_id: definition.id, layout_revision: 2, definition_revision: 3, positions: {} };
    expect(workflowLayoutIsCurrent(old, definition, 3)).toBe(false);
    expect(workflowLayoutIsCurrent({ ...old, layout_revision: 4, definition_revision: 2 }, definition, 3)).toBe(false);
    expect(workflowLayoutIsCurrent({ ...old, layout_revision: 4 }, definition, 3, 5)).toBe(false);
    expect(workflowLayoutIsCurrent({ ...old, layout_revision: 4, definition_revision: 5 }, definition, 3, 5)).toBe(true);
    expect(workflowLayoutIsCurrent({ ...old, workflow_id: 'other' }, definition, 0)).toBe(false);
  });
  it('safely rebases independent edits on remote revision without changing the source draft', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.steps[0]!.title = '本地标题'; remote.steps[2]!.task = '对话的新报告要求'; remote.revision = 4; remote.definition_hash = 'remote';
    const merged = mergeWorkflowEdits(base, local, remote);
    expect(merged.conflicts).toEqual([]);
    expect(merged.definition.steps[0]!.title).toBe('本地标题'); expect(merged.definition.steps[2]!.task).toBe('对话的新报告要求');
    expect(merged.definition.revision).toBe(4); expect(merged.definition.definition_hash).toBe('remote');
    expect(local.steps[2]!.task).toBe(base.steps[2]!.task);
  });
  it('requires review for a same-field conflict instead of silently overwriting remote content', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.steps[0]!.task = '本地指令'; remote.steps[0]!.task = '远端指令';
    expect(mergeWorkflowEdits(base, local, remote).conflicts).toContain('steps.collect.task');
    expect(workflowChanges(base, local)).toEqual([expect.objectContaining({ stepId: 'collect', field: 'task', before: base.steps[0]!.task, after: '本地指令' })]);
  });
  it('requires review when one side deletes a node changed by the other', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.steps.splice(0, 1); remote.steps[0]!.title = '远端编辑';
    expect(mergeWorkflowEdits(base, local, remote).conflicts).toContain('steps.collect');
    expect(mergeWorkflowEdits(base, remote, local).conflicts).toContain('steps.collect');
  });
  it('rejects conflicting structural changes but retains independent metadata with one reorder', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.steps.reverse(); remote.description = '新的说明';
    const merged = mergeWorkflowEdits(base, local, remote);
    expect(merged.conflicts).toEqual([]); expect(merged.definition.steps.map(s => s.id)).toEqual(['report', 'review', 'collect']); expect(merged.definition.description).toBe('新的说明');
    remote.steps = [remote.steps[1]!, remote.steps[0]!, remote.steps[2]!];
    expect(mergeWorkflowEdits(base, local, remote).conflicts).toContain('order');
  });
  it('detects distinct input schemas and colliding newly added stable IDs', () => {
    const base = fixture(), local = cloneWorkflowDefinition(base), remote = cloneWorkflowDefinition(base);
    local.inputs = [{ key: 'ours' }]; remote.inputs = [{ key: 'theirs' }];
    local.steps.push({ id: 'added', kind: 'agent', task: 'ours' }); remote.steps.push({ id: 'added', kind: 'agent', task: 'theirs' });
    expect(mergeWorkflowEdits(base, local, remote).conflicts).toEqual(['inputs', 'steps.added']);
  });
});

describe('sequential workflow validation and variable selection', () => {
  it('accepts the research, confirmation, report path and offers only allowed earlier sources', () => {
    const workflow = fixture();
    expect(workflowEditorIssues(workflow)).toEqual([]);
    expect(workflowVariables(workflow, 'collect').map(item => item.value)).toEqual(['{{ inputs.topic }}']);
    expect(workflowVariables(workflow, 'review').map(item => item.value)).toEqual(['{{ inputs.topic }}', '{{ steps.collect.summary }}', '{{ steps.collect.artifact }}', '{{ steps.collect.feedback }}']);
  });
  it.each(['{{ steps.report.summary }}', '{{ steps.collect.task }}', '{{ inputs.missing }}', '{{ secrets.api_key }}'])('blocks forward/self/unknown/unsupported references: %s', task => {
    const workflow = fixture(); workflow.steps[0]!.task = task;
    expect(workflowEditorIssues(workflow)).toContainEqual(expect.objectContaining({ code: 'reference', stepId: 'collect' }));
  });
  it('detects a previously valid reference broken by reordering', () => {
    const workflow = fixture(); workflow.steps = [workflow.steps[1]!, workflow.steps[0]!, workflow.steps[2]!];
    expect(workflowEditorIssues(workflow)).toContainEqual(expect.objectContaining({ code: 'reference', stepId: 'review' }));
  });
  it('checks incomplete references, UTF-8 length, node kinds and duplicate output files', () => {
    const workflow = fixture(); workflow.name = '资'.repeat(86); workflow.steps[0]!.task = '整理 {{ inputs.topic'; workflow.steps[2]!.output_file = 'notes.md';
    workflow.steps.push({ id: 'bad', kind: 'branch', task: 'unsupported' } as unknown as AgentWorkflowStep);
    expect(workflowEditorIssues(workflow).map(issue => issue.code)).toEqual(expect.arrayContaining(['name', 'template', 'output', 'kind']));
  });
  it.each(['../escape.md', '/absolute.md', 'reports/../../escape.md', 'a//b.md', 'a/./b.md', 'C:\\a.md', 'foo\nbar.md'])('rejects unsafe artifact paths: %s', path => expect(safeWorkflowOutput(path)).toBe(false));
  it('duplicates output names without collisions and retains the directory/extension', () => {
    const steps: AgentWorkflowStep[] = [{ id: 'a', kind: 'agent', task: 'a', output_file: 'reports/output.md' }, { id: 'b', kind: 'agent', task: 'b', output_file: 'reports/output_copy2.md' }];
    expect(safeWorkflowOutput('reports/output.md')).toBe(true);
    expect(uniqueWorkflowOutput('reports/output.md', steps)).toBe('reports/output_copy3.md');
  });
});
