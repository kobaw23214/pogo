import { describe, expect, it } from 'vitest';
import type { AgentWorkflowDefinition, AgentWorkflowRun, AgentWorkflowStep } from '@/api/agentWorkflows';
import { cloneWorkflowDefinition, mergeWorkflowEdits, sameWorkflowContent, workflowChanges, workflowEditOperations, workflowEditorIssues, workflowVariables } from './agentWorkflowEditor';
import { connectWorkflowGraph, disconnectWorkflowGraphEdge, guaranteedWorkflowPredecessors, insertWorkflowGraphGroup, insertWorkflowGraphStep, moveWorkflowGraphStep, removeWorkflowGraphStep, upgradeWorkflowGraph, workflowEdgeHandle, workflowGraphProblems, workflowInsertionEdge } from './agentWorkflowDag';
import { buildAgentWorkflowGraph, layoutAgentWorkflow } from './agentWorkflowGraph';

function basic(): AgentWorkflowDefinition {
  return { id: 'flow', agent_id: 'research', name: 'Report', revision: 1, definition_hash: 'old', inputs: [{ key: 'amount', required: true }], steps: [
    { id: 'collect', kind: 'agent', task: 'Read source material', output_file: 'notes.md' },
    { id: 'report', kind: 'agent', task: 'Write report', output_file: 'report.md' },
  ] };
}
function group(kind: 'condition' | 'parallel'): [AgentWorkflowStep, AgentWorkflowStep, AgentWorkflowStep, AgentWorkflowStep] {
  return [
    { id: 'gate', kind, ...(kind === 'condition' ? { condition: { source: 'inputs.amount', operator: 'gt' as const, value: '1000' } } : {}) },
    { id: 'left', kind: 'agent', task: 'Analyze sources', output_file: 'left.md' },
    { id: 'right', kind: 'confirm', prompt: 'Confirm sources' },
    { id: 'merge', kind: 'join' },
  ];
}
function branched(kind: 'condition' | 'parallel' = 'condition') {
  const def = basic(); def.graph = upgradeWorkflowGraph(def);
  insertWorkflowGraphGroup(def, group(kind), 'collect'); return def;
}

describe('internal workflow DAG editor', () => {
  it('upgrades explicitly without mutating legacy content, and includes only set_graph in the patch', () => {
    const legacy = basic(), draft = cloneWorkflowDefinition(legacy); draft.graph = upgradeWorkflowGraph(draft);
    expect(legacy.graph).toBeUndefined(); expect(draft.steps).toEqual(legacy.steps);
    expect(sameWorkflowContent(legacy, draft)).toBe(false);
    expect(workflowEditOperations(legacy, draft)).toEqual([{ op: 'set_graph', graph: draft.graph }]);
    expect(workflowGraphProblems(draft)).toEqual([]);
  });

  it.each(['condition', 'parallel'] as const)('inserts a complete %s group with branches and a join', kind => {
    const def = branched(kind);
    expect(workflowGraphProblems(def)).toEqual([]);
    expect(workflowEditorIssues(def)).toEqual([]);
    expect(def.graph?.edges.filter(edge => edge.source === 'gate').map(edge => [edge.target, edge.branch])).toEqual(kind === 'condition' ? [['left', 'true'], ['right', 'false']] : [['left', undefined], ['right', undefined]]);
    expect(layoutAgentWorkflow(def).left.x).toBe(layoutAgentWorkflow(def).right.x);
    expect(layoutAgentWorkflow(def).left.y).not.toBe(layoutAgentWorkflow(def).right.y);
    expect(layoutAgentWorkflow(def).merge.x).toBeGreaterThan(layoutAgentWorkflow(def).left.x);
  });

  it('only offers guaranteed upstream values and the join summary after mutually exclusive branches', () => {
    const def = branched();
    expect(guaranteedWorkflowPredecessors(def, 'report')).toEqual(new Set(['collect', 'gate', 'merge']));
    expect(workflowVariables(def, 'report').map(value => value.value)).toContain('{{ steps.merge.summary }}');
    expect(workflowVariables(def, 'report').some(value => value.value.includes('steps.left.'))).toBe(false);
    def.steps.find(step => step.id === 'report')!.task = 'Use {{ steps.left.summary }}';
    expect(workflowEditorIssues(def)).toContainEqual({ code: 'reference', field: 'task', stepId: 'report' });
    def.steps.find(step => step.id === 'report')!.task = 'Use {{ steps.merge.summary }}';
    expect(workflowEditorIssues(def)).toEqual([]);
  });

  it('inserts on the selected true/false branch, preserving the branch marker', () => {
    const def = branched(), edge = def.graph!.edges.find(edge => edge.source === 'gate' && edge.branch === 'false')!;
    expect(insertWorkflowGraphStep(def, { id: 'extra', kind: 'agent', task: 'Check' }, 'gate')).toBe(false);
    expect(insertWorkflowGraphStep(def, { id: 'extra', kind: 'agent', task: 'Check' }, 'gate', edge)).toBe(true);
    expect(def.graph!.edges).toContainEqual({ source: 'gate', target: 'extra', branch: 'false' });
    expect(def.graph!.edges).toContainEqual({ source: 'extra', target: 'right' });
    expect(workflowGraphProblems(def)).toEqual([]);
  });

  it('moves an ordinary node through a selected branch port, never leaving a disconnected node', () => {
    const def = branched(), graph = moveWorkflowGraphStep(def, 'gate', 'report', 'true');
    expect(graph).toBeDefined();
    expect(graph!.edges).toContainEqual({ source: 'gate', target: 'report', branch: 'true' });
    expect(graph!.edges).toContainEqual({ source: 'report', target: 'left' });
    expect(graph!.edges).toContainEqual({ source: 'merge', target: '__output' });
    expect(workflowGraphProblems({ ...def, graph })).toEqual([]);
    expect(moveWorkflowGraphStep(def, 'left', 'gate', 'out')).toBeUndefined();
    expect(moveWorkflowGraphStep(def, '__output', 'left', 'out')).toBeUndefined();
    expect(moveWorkflowGraphStep(def, 'left', 'left', 'out')).toBeUndefined();
  });

  it('bridges only an unambiguous ordinary node and leaves explicit repair for control deletion', () => {
    const def = branched();
    expect(removeWorkflowGraphStep(def, 'left')).toBe(true);
    expect(def.graph!.edges).toContainEqual({ source: 'gate', target: 'merge', branch: 'true' });
    expect(removeWorkflowGraphStep(def, 'right')).toBe(true);
    expect(workflowGraphProblems(def)).toEqual([]);
    const parallel = branched('parallel');
    expect(removeWorkflowGraphStep(parallel, 'left')).toBe(true);
    expect(removeWorkflowGraphStep(parallel, 'right')).toBe(true);
    expect(workflowGraphProblems(parallel)).toContain('degree');
    const control = branched(), unaffected = control.graph!.edges.filter(edge => edge.source !== 'gate' && edge.target !== 'gate');
    expect(removeWorkflowGraphStep(control, 'gate')).toBe(true);
    expect(control.graph!.edges).toEqual(unaffected);
    expect(workflowGraphProblems(control)).toContain('unreachable');
  });

  it('saves rule and graph edits atomically and detects concurrent rule/connection conflicts', () => {
    const def = branched(), local = cloneWorkflowDefinition(def), remote = cloneWorkflowDefinition(def);
    local.steps.find(step => step.id === 'gate')!.condition!.value = '2000';
    remote.steps.find(step => step.id === 'gate')!.condition!.value = '3000';
    expect(sameWorkflowContent(def, local)).toBe(false);
    expect(workflowEditOperations(def, local)).toEqual([{ op: 'update_step', step_id: 'gate', patch: { condition: { source: 'inputs.amount', operator: 'gt', value: '2000' } } }]);
    expect(workflowChanges(def, local).some(change => change.field === 'condition')).toBe(true);
    expect(mergeWorkflowEdits(def, local, remote).conflicts).toContain('steps.gate.condition');
    local.graph = moveWorkflowGraphStep(def, 'gate', 'report', 'true');
    remote.graph = moveWorkflowGraphStep(def, 'gate', 'report', 'false');
    expect(mergeWorkflowEdits(def, local, remote).conflicts).toContain('graph');
  });

  it('rejects cycles, dangling edges and invalid condition data before saving', () => {
    const def = branched(); def.graph!.edges.push({ source: 'merge', target: 'gate' });
    expect(workflowGraphProblems(def)).toContain('cycle');
    const invalid = branched(); invalid.steps.find(step => step.id === 'gate')!.condition = { source: 'steps.left.summary', operator: 'gt', value: 'NaN' };
    expect(workflowEditorIssues(invalid).map(issue => issue.code)).toEqual(expect.arrayContaining(['condition', 'conditionNumber']));
  });

  it('uses graph snapshots and displays skipped routes and multiple active nodes', () => {
    const snapshot = branched(), current = basic();
    const run: AgentWorkflowRun = { id: 'r', agent_id: 'research', workflow_id: snapshot.id, status: 'running', definition: snapshot, steps: [
      { step_id: 'collect', status: 'completed' }, { step_id: 'gate', status: 'completed', branch: 'true' }, { step_id: 'left', status: 'running' }, { step_id: 'right', status: 'skipped' }, { step_id: 'merge', status: 'queued' },
    ] };
    const graph = buildAgentWorkflowGraph({ definition: current, run, editable: true });
    expect(graph.nodes.find(node => node.id === 'gate')?.data.branch).toBe('true');
    expect(graph.edges.find(edge => edge.source === 'gate' && edge.target === 'right')?.data.status).toBe('skipped');
    expect(graph.edges.find(edge => edge.source === 'gate' && edge.target === 'left')?.sourceHandle).toBe('true');
    expect(graph.nodes.every(node => !node.draggable && !node.connectable)).toBe(true);
    const parallel = branched('parallel'); run.definition = parallel; run.steps![1] = { step_id: 'gate', status: 'completed' }; run.steps![3] = { step_id: 'right', status: 'waiting_confirmation' };
    expect(buildAgentWorkflowGraph({ definition: current, run }).nodes.filter(node => ['running', 'waiting_confirmation'].includes(node.data.status)).map(node => node.id)).toEqual(['left', 'right']);
    expect(workflowEdgeHandle(parallel, parallel.graph!.edges.find(edge => edge.source === 'gate' && edge.target === 'right')!)).toBe('out');
  });

  it('adds fan-in and fan-out without silently removing old routes or moving nodes', () => {
    const def = basic(), before = JSON.stringify(def), original = upgradeWorkflowGraph(def);
    const graph = connectWorkflowGraph(def, '__input', 'report', 'out');
    expect(graph?.edges).toEqual([...original.edges, { source: '__input', target: 'report' }]);
    expect(JSON.stringify(def)).toBe(before);
    const merged = { ...def, graph };
    expect(workflowGraphProblems(merged)).toEqual([]);
    expect(guaranteedWorkflowPredecessors(merged, 'report')).toEqual(new Set(['collect']));
    expect(connectWorkflowGraph(merged, 'report', 'collect')).toBeUndefined();
    expect(connectWorkflowGraph(merged, '__input', 'report')).toBeUndefined();
    expect(connectWorkflowGraph(merged, 'collect', 'collect')).toBeUndefined();
    expect(connectWorkflowGraph(merged, '__output', 'collect')).toBeUndefined();
    expect(connectWorkflowGraph(merged, 'report', '__input')).toBeUndefined();
  });

  it('offers both unconditional parallel results at an ordinary multi-input node', () => {
    const def = basic(); def.steps.splice(1, 0, { id: 'other', kind: 'agent', task: 'Analyze independently', output_file: 'other.md' });
    def.graph = { version: 1, edges: [
      { source: '__input', target: 'collect' }, { source: '__input', target: 'other' },
      { source: 'collect', target: 'report' }, { source: 'other', target: 'report' }, { source: 'report', target: '__output' },
    ] };
    expect(workflowGraphProblems(def)).toEqual([]);
    expect(guaranteedWorkflowPredecessors(def, 'report')).toEqual(new Set(['collect', 'other']));
    def.steps.find(step => step.id === 'report')!.task = 'Read {{ steps.collect.summary }} and {{ steps.other.summary }}';
    expect(workflowEditorIssues(def)).toEqual([]);
    const graph = buildAgentWorkflowGraph({ definition: def, editable: true });
    expect(graph.nodes.find(node => node.id === 'report')!.data.incomingCount).toBe(2);
    expect(graph.nodes.find(node => node.id === '__input')!.data.outgoingCount).toBe(2);
    expect(graph.nodes.find(node => node.id === '__input')!.data.ports).toEqual([{ id: 'out', label: '' }]);
  });

  it('supports multiple exits per condition branch without making an optional sibling a required value', () => {
    const def = branched();
    const graph = connectWorkflowGraph(def, 'gate', 'report', 'true');
    expect(graph?.edges).toContainEqual({ source: 'gate', target: 'report', branch: 'true' });
    expect(workflowGraphProblems({ ...def, graph })).toEqual([]);
    expect(guaranteedWorkflowPredecessors({ ...def, graph }, 'report').has('left')).toBe(false);
    expect(connectWorkflowGraph(def, 'gate', 'report', 'out')).toBeUndefined();
    const port = buildAgentWorkflowGraph({ definition: { ...def, graph }, editable: true }).nodes.find(node => node.id === 'gate')!.data.ports;
    expect(port).toEqual([{ id: 'true', label: 'true' }, { id: 'false', label: 'false' }]);
  });

  it('removes exactly one connection and retains incomplete drafts for deliberate reconnection', () => {
    const def = branched(), edge = def.graph!.edges.find(item => item.source === 'gate' && item.branch === 'true')!;
    const before = JSON.stringify(def), graph = disconnectWorkflowGraphEdge(def, edge)!;
    expect(graph.edges).toEqual(def.graph!.edges.filter(item => item !== edge));
    expect(JSON.stringify(def)).toBe(before);
    expect(workflowGraphProblems({ ...def, graph })).toEqual(expect.arrayContaining(['degree', 'unreachable']));
    const repaired = connectWorkflowGraph({ ...def, graph }, edge.source, edge.target, 'true');
    expect(workflowGraphProblems({ ...def, graph: repaired })).toEqual([]);
    const output = connectWorkflowGraph(def, 'left', '__output')!;
    expect(workflowInsertionEdge({ ...def, graph: output }, '__output')).toBeUndefined();
    expect(buildAgentWorkflowGraph({ definition: { ...def, graph }, editable: true }).nodes.find(node => node.id === 'left')?.data.ports).toEqual([{ id: 'out', label: '' }]);
  });
});
