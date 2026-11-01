import { describe, expect, it } from 'vitest';
import { workflowActivitySnapshot, workflowNodeActivities, type AgentWorkflowActivitySnapshot } from './agentWorkflowActivity';
import type { AgentWorkflowRun } from '@/api/agentWorkflows';

const activity = (): AgentWorkflowActivitySnapshot => ({ agent_id: 'agent-a', workflow_id: 'flow-a', run_id: 'run-a', status: 'running', updated_at: 100,
  nodes: [{ step_id: 'left', session_id: 'session-left', active_run_id: 'attempt-left', status: 'running', phase: 'tool', observed: true, turn: 3, tool_name: 'read', tool_count: 2, updated_at: 100 },
    { step_id: 'right', session_id: 'session-right', active_run_id: 'attempt-right', status: 'running', phase: 'approval', observed: true, approval_count: 1, updated_at: 100 }] });
const run = (): AgentWorkflowRun => ({ id: 'run-a', agent_id: 'agent-a', workflow_id: 'flow-a', status: 'running', definition: { id: 'flow-a', agent_id: 'agent-a', name: 'Research', revision: 1, definition_hash: 'a'.repeat(64), steps: [{ id: 'left', kind: 'agent' }, { id: 'right', kind: 'agent' }] },
  steps: [{ step_id: 'left', status: 'running', session_id: 'session-left', active_run_id: 'attempt-left' }, { step_id: 'right', status: 'running', session_id: 'session-right', active_run_id: 'attempt-right' }] });

describe('scoped workflow node activities', () => {
  it('accepts code execution activity without fabricating an Agent session or model turn', () => {
    const snapshot=activity(); snapshot.nodes=[{step_id:'calculate',status:'running',phase:'running',observed:true,updated_at:100}];
    const stored=run(); stored.definition.steps=[{id:'calculate',kind:'code'}]; stored.steps=[{step_id:'calculate',status:'running'}];
    const parsed=workflowActivitySnapshot(snapshot,'agent-a','flow-a','run-a');
    expect(parsed).not.toBeNull();
    expect(workflowNodeActivities(parsed,stored,true).calculate).toEqual({...snapshot.nodes[0],connected:true});
  });
  it('keeps simultaneous tool activity and approval on their actual nodes', () => {
    const parsed = workflowActivitySnapshot(activity(), 'agent-a', 'flow-a', 'run-a');
    const nodes = workflowNodeActivities(parsed, run(), true);
    expect(nodes.left).toMatchObject({ turn: 3, tool_name: 'read', connected: true });
    expect(nodes.right).toMatchObject({ phase: 'approval', approval_count: 1 });
    expect(workflowNodeActivities(parsed, run(), false).left?.connected).toBe(false);
  });
  it('rejects wrong scope, duplicate nodes and invalid event fields', () => {
    expect(workflowActivitySnapshot(activity(), 'agent-b', 'flow-a', 'run-a')).toBeNull();
    const duplicate = activity(); duplicate.nodes.push(duplicate.nodes[0]!);
    expect(workflowActivitySnapshot(duplicate, 'agent-a', 'flow-a', 'run-a')).toBeNull();
    const malformed = activity(); malformed.nodes[0]!.turn = -1;
    expect(workflowActivitySnapshot(malformed, 'agent-a', 'flow-a', 'run-a')).toBeNull();
  });
  it('does not reuse another attempt or another Run activity', () => {
    const stale = activity(); stale.nodes[0]!.active_run_id = 'old-attempt';
    expect(workflowNodeActivities(stale, run(), true).left).toBeUndefined();
    const retried = run(); retried.id = 'run-retry';
    expect(Object.keys(workflowNodeActivities(activity(), retried, true))).toHaveLength(0);
  });
});
