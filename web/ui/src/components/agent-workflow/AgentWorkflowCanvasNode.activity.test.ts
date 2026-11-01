import { describe, expect, it, vi } from 'vitest';
import { createSSRApp, h } from 'vue';
import { renderToString } from 'vue/server-renderer';
import type { AgentWorkflowCanvasNodeData } from '@/agent/agentWorkflowGraph';

vi.mock('@vue-flow/core', () => ({ Handle: { render: () => null }, Position: { Left: 'left', Right: 'right' } }));
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, args?: Record<string, unknown>) => key + (args ? ' ' + JSON.stringify(args) : ''), locale: { value: 'zh-CN' } }) }));
import AgentWorkflowCanvasNode from './AgentWorkflowCanvasNode.vue';

const data = (): AgentWorkflowCanvasNodeData => ({ id: 'node-a', kind: 'agent', title: 'Actual node', description: 'The fixed node instruction', status: 'running', active: false, editable: false, inspectable: true });
const render = (node: AgentWorkflowCanvasNodeData) => renderToString(createSSRApp({ render: () => h(AgentWorkflowCanvasNode, { data: node }) }));

describe('runtime Agent node activity', () => {
  it('does not invent turns, tool counts or an observation timestamp without session evidence', async () => {
    const node = data(); node.activity = { step_id: node.id, status: 'running', phase: 'model', observed: false, turn: 0, tool_count: 0, updated_at: 1000 };
    const html = await render(node);
    expect(html).toContain('activityAwaiting'); expect(html).toContain('workflow-node-inspect');
    expect(html).not.toContain('activityTurn'); expect(html).not.toContain('activityToolCount'); expect(html).not.toContain('activityUpdatedAt');
  });

  it('shows recorded work and marks a disconnected feed while retaining its last activity', async () => {
    const node = data(); node.activity = { step_id: node.id, status: 'running', phase: 'tool', observed: true, turn: 3, tool_name: 'read', tool_count: 2, pending_tool_count: 1, updated_at: 1000, connected: false };
    const html = await render(node);
    expect(html).toContain('activityDoingTools.read'); expect(html).toContain('activityTurn'); expect(html).toContain('activityToolCount'); expect(html).toContain('activityDisconnected');
    expect(html).not.toContain('aw-node-description');
  });

  it('distinguishes real pending tool approval and retains the execution entry after completion', async () => {
    const node = data(); node.activity = { step_id: node.id, status: 'running', phase: 'approval', observed: true, approval_count: 2, updated_at: 1000 };
    expect(await render(node)).toContain('activityApprovals');
    node.status = 'completed'; node.activity = { ...node.activity, status: 'completed', phase: 'completed', tool_count: 4 };
    const html = await render(node); expect(html).toContain('activityPhases.completed'); expect(html).toContain('workflow-node-inspect'); expect(html).toContain('activityUpdatedAt');
  });
});
