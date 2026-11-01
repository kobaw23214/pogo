import type { AgentWorkflowDAG, AgentWorkflowDefinition, AgentWorkflowEdge, AgentWorkflowStep } from '@/api/agentWorkflows';
import { AGENT_WORKFLOW_MAX_STEPS } from './agentWorkflowLimits';

export const DAG_INPUT = '__input', DAG_OUTPUT = '__output';
export const workflowEdgeKey = (edge: AgentWorkflowEdge) => `${edge.source}:${edge.branch || ''}:${edge.target}`;
export const workflowGraphKey = (graph?: AgentWorkflowDAG) => graph ? JSON.stringify({ version: graph.version, edges: graph.edges.map(workflowEdgeKey).sort() }) : '';
export function workflowEdges(definition: AgentWorkflowDefinition): AgentWorkflowEdge[] {
  if (definition.graph) return definition.graph.edges;
  const ids = [DAG_INPUT, ...definition.steps.map(step => step.id), DAG_OUTPUT];
  return ids.slice(1).map((target, index) => ({ source: ids[index]!, target }));
}
export const outgoingWorkflowEdges = (definition: AgentWorkflowDefinition, id: string) => workflowEdges(definition).filter(edge => edge.source === id);
export const incomingWorkflowEdges = (definition: AgentWorkflowDefinition, id: string) => workflowEdges(definition).filter(edge => edge.target === id);
export function workflowEdgeHandle(_definition: AgentWorkflowDefinition, edge: AgentWorkflowEdge): string {
  return edge.branch || 'out';
}
export function upgradeWorkflowGraph(definition: AgentWorkflowDefinition): AgentWorkflowDAG {
  return { version: 1, edges: workflowEdges(definition).map(edge => ({ ...edge })) };
}

/** Topological ordering is only for presentation and validation, never an execution claim. */
export function workflowTopologicalIds(definition: AgentWorkflowDefinition): string[] {
  const ids = [DAG_INPUT, ...definition.steps.map(step => step.id), DAG_OUTPUT];
  const known = new Set(ids), remaining = new Map(ids.map(id => [id, 0]));
  for (const edge of workflowEdges(definition)) if (known.has(edge.source) && known.has(edge.target)) remaining.set(edge.target, remaining.get(edge.target)! + 1);
  const queue = ids.filter(id => remaining.get(id) === 0), result: string[] = [];
  for (let i = 0; i < queue.length; i++) {
    const id = queue[i]!; result.push(id);
    for (const edge of workflowEdges(definition).filter(edge => edge.source === id)) {
      if (!known.has(edge.target)) continue;
      const count = remaining.get(edge.target)! - 1; remaining.set(edge.target, count);
      if (count === 0) queue.push(edge.target);
    }
  }
  return result;
}

/** All active paths are awaited. Ordinary fan-out therefore guarantees both
 * branch results at a merge, while a conditional alternative may be skipped. */
export function guaranteedWorkflowPredecessors(definition: AgentWorkflowDefinition, stepId: string): Set<string> {
  if (!definition.graph) return new Set(definition.steps.slice(0, Math.max(0, definition.steps.findIndex(step => step.id === stepId))).map(step => step.id));
  const dominators = new Map<string, Set<string>>([[DAG_INPUT, new Set([DAG_INPUT])]]);
  const order = workflowTopologicalIds(definition), facts = new Map<string, Map<string, string>>([[DAG_INPUT, new Map()]]);
  for (const id of order) {
    if (id === DAG_INPUT) continue;
    const incoming = incomingWorkflowEdges(definition, id), parents = incoming.map(edge => dominators.get(edge.source));
    if (!parents.length || parents.some(parent => !parent)) continue;
    const common = new Set([...parents[0]!].filter(parent => parents.every(values => values!.has(parent))));
    common.add(id); dominators.set(id, common);
    const candidates = incoming.map(edge => {
      const value = new Map(facts.get(edge.source));
      if (definition.steps.find(step => step.id === edge.source)?.kind === 'condition' && edge.branch) value.set(edge.source, edge.branch);
      return value;
    });
    facts.set(id, new Map([...candidates[0]!].filter(([key, value]) => candidates.every(candidate => candidate.get(key) === value))));
  }
  const ancestors = new Set<string>(), pending = [stepId];
  while (pending.length) {
    const id = pending.pop()!; if (ancestors.has(id)) continue; ancestors.add(id);
    pending.push(...incomingWorkflowEdges(definition, id).map(edge => edge.source));
  }
  const result = new Set(dominators.get(stepId));
  for (const id of order) {
    if (!result.has(id)) continue;
    const conditional = definition.steps.find(step => step.id === id)?.kind === 'condition';
    for (const edge of outgoingWorkflowEdges(definition, id)) {
      if (ancestors.has(edge.target) && (!conditional || facts.get(stepId)?.get(id) === edge.branch)) result.add(edge.target);
    }
  }
  result.delete(stepId); result.delete(DAG_INPUT); return result;
}

export type WorkflowGraphProblem = 'version' | 'edge' | 'degree' | 'cycle' | 'unreachable';
export function workflowGraphProblems(definition: AgentWorkflowDefinition): WorkflowGraphProblem[] {
  if (!definition.graph) return [];
  const problems = new Set<WorkflowGraphProblem>(), ids = [DAG_INPUT, ...definition.steps.map(step => step.id), DAG_OUTPUT], known = new Set(ids);
  if (definition.graph.version !== 1) problems.add('version');
  const keys = new Set<string>();
  if (definition.graph.edges.length > AGENT_WORKFLOW_MAX_STEPS * 3) problems.add('edge');
  for (const edge of definition.graph.edges) {
    const key = workflowEdgeKey(edge), source = definition.steps.find(step => step.id === edge.source);
    if (!known.has(edge.source) || !known.has(edge.target) || edge.source === edge.target || edge.source === DAG_OUTPUT || edge.target === DAG_INPUT || keys.has(key) || (source?.kind === 'condition' ? !['true', 'false'].includes(edge.branch || '') : !!edge.branch)) problems.add('edge');
    keys.add(key);
  }
  for (const id of ids) {
    const incoming = incomingWorkflowEdges(definition, id), outgoing = outgoingWorkflowEdges(definition, id), kind = definition.steps.find(step => step.id === id)?.kind;
    if (id === DAG_INPUT ? incoming.length !== 0 || outgoing.length < 1 : id === DAG_OUTPUT ? incoming.length < 1 || outgoing.length !== 0 : kind === 'join' ? incoming.length < 2 || outgoing.length < 1 : kind === 'parallel' ? incoming.length < 1 || outgoing.length < 2 : kind === 'condition' ? incoming.length < 1 || !outgoing.some(edge => edge.branch === 'true') || !outgoing.some(edge => edge.branch === 'false') : incoming.length < 1 || outgoing.length < 1) problems.add('degree');
  }
  if (workflowTopologicalIds(definition).length !== ids.length) problems.add('cycle');
  for (const reverse of [false, true]) {
    const visited = new Set<string>(), pending = [reverse ? DAG_OUTPUT : DAG_INPUT];
    while (pending.length) {
      const id = pending.pop()!; if (visited.has(id)) continue; visited.add(id);
      for (const edge of workflowEdges(definition)) if ((reverse ? edge.target : edge.source) === id) pending.push(reverse ? edge.source : edge.target);
    }
    if (ids.some(id => !visited.has(id))) problems.add('unreachable');
  }
  return [...problems];
}

export function workflowInsertionEdge(definition: AgentWorkflowDefinition, afterId: string | null, edge?: AgentWorkflowEdge): AgentWorkflowEdge | undefined {
  if (edge) return workflowEdges(definition).find(item => workflowEdgeKey(item) === workflowEdgeKey(edge));
  if (afterId === DAG_OUTPUT || afterId === '') { const incoming = incomingWorkflowEdges(definition, DAG_OUTPUT); return incoming.length === 1 ? incoming[0] : undefined; }
  const outgoing = outgoingWorkflowEdges(definition, afterId || DAG_INPUT);
  return outgoing.length === 1 ? outgoing[0] : undefined;
}
function replaceEdge(definition: AgentWorkflowDefinition, edge: AgentWorkflowEdge, replacement: AgentWorkflowEdge[]) {
  definition.graph = { version: 1, edges: workflowEdges(definition).filter(item => workflowEdgeKey(item) !== workflowEdgeKey(edge)).concat(replacement) };
}
/** Mutates only a local editor copy. Persist the complete operation batch atomically. */
export function insertWorkflowGraphStep(definition: AgentWorkflowDefinition, step: AgentWorkflowStep, afterId: string | null, selectedEdge?: AgentWorkflowEdge): boolean {
  const edge = workflowInsertionEdge(definition, afterId, selectedEdge); if (!edge) return false;
  const at = definition.steps.findIndex(item => item.id === edge.target);
  definition.steps.splice(at < 0 ? definition.steps.length : at, 0, step);
  replaceEdge(definition, edge, [{ ...edge, target: step.id }, { source: step.id, target: edge.target }]); return true;
}
export function insertWorkflowGraphGroup(definition: AgentWorkflowDefinition, steps: [AgentWorkflowStep, AgentWorkflowStep, AgentWorkflowStep, AgentWorkflowStep], afterId: string | null, selectedEdge?: AgentWorkflowEdge): boolean {
  const edge = workflowInsertionEdge(definition, afterId, selectedEdge); if (!edge) return false;
  const [control, left, right, join] = steps, at = definition.steps.findIndex(item => item.id === edge.target);
  definition.steps.splice(at < 0 ? definition.steps.length : at, 0, ...steps);
  replaceEdge(definition, edge, [
    { ...edge, target: control.id },
    { source: control.id, target: left.id, ...(control.kind === 'condition' ? { branch: 'true' as const } : {}) },
    { source: control.id, target: right.id, ...(control.kind === 'condition' ? { branch: 'false' as const } : {}) },
    { source: left.id, target: join.id }, { source: right.id, target: join.id }, { source: join.id, target: edge.target },
  ]); return true;
}
export function removeWorkflowGraphStep(definition: AgentWorkflowDefinition, id: string): boolean {
  const step = definition.steps.find(item => item.id === id), incoming = incomingWorkflowEdges(definition, id), outgoing = outgoingWorkflowEdges(definition, id);
  if (!step || definition.steps.length <= 1) return false;
  const edges = workflowEdges(definition).filter(edge => edge.source !== id && edge.target !== id);
  // The simple sequential case has one unambiguous continuation. A multi-path
  // node or control node has no implicit replacement: keep other connections
  // exactly as arranged and let the user explicitly reconnect the draft.
  if (['agent', 'code', 'iteration', 'loop', 'confirm'].includes(step.kind) && incoming.length === 1 && outgoing.length === 1) {
    const bridge = { ...incoming[0]!, target: outgoing[0]!.target };
    if (!edges.some(edge => workflowEdgeKey(edge) === workflowEdgeKey(bridge))) edges.push(bridge);
  }
  const next = { ...definition, steps: definition.steps.filter(item => item.id !== id), graph: { version: 1 as const, edges } };
  definition.steps = next.steps; definition.graph = next.graph; return true;
}

/** Port gestures add one connection; they never move nodes or remove a prior
 * route. Incomplete degrees are allowed while editing, but not when saving. */
export function connectWorkflowGraph(definition: AgentWorkflowDefinition, source: string, target: string, sourceHandle?: string | null): AgentWorkflowDAG | undefined {
  const known = new Set([DAG_INPUT, ...definition.steps.map(step => step.id), DAG_OUTPUT]);
  if (!known.has(source) || !known.has(target) || source === target || source === DAG_OUTPUT || target === DAG_INPUT) return undefined;
  const step = definition.steps.find(item => item.id === source);
  if (step?.kind === 'condition' && sourceHandle !== 'true' && sourceHandle !== 'false') return undefined;
  const edge: AgentWorkflowEdge = { source, target, ...(step?.kind === 'condition' ? { branch: sourceHandle as 'true' | 'false' } : {}) };
  if (workflowEdges(definition).some(item => workflowEdgeKey(item) === workflowEdgeKey(edge))) return undefined;
  const graph = { version: 1 as const, edges: [...workflowEdges(definition).map(item => ({ ...item })), edge] };
  const issues = workflowGraphProblems({ ...definition, graph });
  return issues.some(issue => ['edge', 'version', 'cycle'].includes(issue)) ? undefined : graph;
}

/** Remove exactly the selected edge. Reachability/degree errors remain visible
 * in the draft until the user reconnects it; saving still requires validity. */
export function disconnectWorkflowGraphEdge(definition: AgentWorkflowDefinition, edge: AgentWorkflowEdge): AgentWorkflowDAG | undefined {
  const edges = workflowEdges(definition), key = workflowEdgeKey(edge);
  if (!edges.some(item => workflowEdgeKey(item) === key)) return undefined;
  return { version: 1, edges: edges.filter(item => workflowEdgeKey(item) !== key).map(item => ({ ...item })) };
}

export function moveWorkflowGraphStep(definition: AgentWorkflowDefinition, source: string, target: string, sourceHandle?: string | null): AgentWorkflowDAG | undefined {
  const incoming = incomingWorkflowEdges(definition, target), outgoing = outgoingWorkflowEdges(definition, target), step = definition.steps.find(item => item.id === target);
  if (!step || !['agent', 'code', 'iteration', 'loop', 'confirm'].includes(step.kind) || source === target || incoming.length !== 1 || outgoing.length !== 1) return undefined;
  const insertion = outgoingWorkflowEdges(definition, source).find(edge => workflowEdgeHandle(definition, edge) === (sourceHandle || 'out'));
  if (!insertion || insertion.target === target) return undefined;
  const removed = new Set([workflowEdgeKey(incoming[0]!), workflowEdgeKey(outgoing[0]!), workflowEdgeKey(insertion)]);
  const graph: AgentWorkflowDAG = { version: 1, edges: workflowEdges(definition).filter(edge => !removed.has(workflowEdgeKey(edge))).concat(
    { ...incoming[0]!, target: outgoing[0]!.target }, { ...insertion, target }, { source: target, target: insertion.target },
  ) };
  return workflowGraphProblems({ ...definition, graph }).length ? undefined : graph;
}
