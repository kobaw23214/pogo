import type { AgentWorkflowDefinition, AgentWorkflowRun } from "@/api/agentWorkflows";
import type { AgentWorkflowNodeActivity } from './agentWorkflowActivity';
import { workflowNodeCount } from './agentWorkflowRepeat';
import { AGENT_WORKFLOW_MAX_STEPS } from './agentWorkflowLimits';
import { incomingWorkflowEdges, outgoingWorkflowEdges, workflowEdges, workflowEdgeHandle, workflowEdgeKey, workflowTopologicalIds } from './agentWorkflowDag';

export const AGENT_WORKFLOW_INPUT = "__input";
export const AGENT_WORKFLOW_OUTPUT = "__output";
export type AgentWorkflowPosition = { x: number; y: number };
export type AgentWorkflowPositions = Record<string, AgentWorkflowPosition>;
export type AgentWorkflowViewport = AgentWorkflowPosition & { zoom: number };
export type AgentWorkflowCanvasInsets = Partial<Record<'top' | 'right' | 'bottom' | 'left', number>>;
export type AgentWorkflowCanvasRect = AgentWorkflowPosition & { width: number; height: number };

/** The unobscured part of a canvas; panel movement is not a viewport gesture. */
export function workflowCanvasVisibleRect(width: number, height: number, insets: AgentWorkflowCanvasInsets = {}): AgentWorkflowCanvasRect {
  const w = Number.isFinite(width) ? Math.max(1, width) : 1;
  const h = Number.isFinite(height) ? Math.max(1, height) : 1;
  const edge = (value: number | undefined, max: number) => Math.min(Math.max(0, max), Math.max(0, Number.isFinite(value) ? value! : 0));
  // A narrow screen still needs a finite place to focus a node. Responsive
  // panels can cover most of the view, but never produce NaN or negative size.
  const left = edge(insets.left, w - Math.min(64, w));
  const right = edge(insets.right, w - left - Math.min(64, w));
  const top = edge(insets.top, h - Math.min(64, h));
  const bottom = edge(insets.bottom, h - top - Math.min(64, h));
  return { x: left, y: top, width: w - left - right, height: h - top - bottom };
}

/** Fit measured graph bounds into the area not covered by workspace panels. */
export function workflowCanvasBoundsViewport(bounds: AgentWorkflowCanvasRect, area: AgentWorkflowCanvasRect, options: { padding?: number; minZoom?: number; maxZoom?: number; alignStart?: boolean } = {}): AgentWorkflowViewport {
  const padding = options.padding ?? 24;
  const zoom = Math.max(options.minZoom ?? 0.1, Math.min(options.maxZoom ?? 1,
    Math.max(1, area.width - padding * 2) / Math.max(1, bounds.width),
    Math.max(1, area.height - padding * 2) / Math.max(1, bounds.height)));
  return {
    x: options.alignStart ? area.x + padding - bounds.x * zoom : area.x + area.width / 2 - (bounds.x + bounds.width / 2) * zoom,
    y: area.y + area.height / 2 - (bounds.y + bounds.height / 2) * zoom,
    zoom,
  };
}
export type AgentWorkflowNodeKind = "input" | "output" | "agent" | "code" | "confirm" | "condition" | "parallel" | "join" | "iteration" | "loop";

export interface AgentWorkflowCanvasNodeData {
  id: string;
  kind: AgentWorkflowNodeKind;
  title: string;
  description: string;
  outputFile?: string;
  codeLanguage?: string;
  repeatProgress?: {kind:string;completed:number;failed:number;total:number};
  status: string;
  active: boolean;
  editable: boolean;
  index?: number;
  elapsedMs?: number;
  ports?: { id: string; label: string }[];
  canInsert?: boolean;
  branch?: 'true' | 'false';
  updated?: boolean;
  inspectable?: boolean;
  activity?: AgentWorkflowNodeActivity;
  incomingCount?: number;
  outgoingCount?: number;
}

export interface AgentWorkflowGraphNode {
  id: string;
  type: "procedure";
  position: AgentWorkflowPosition;
  data: AgentWorkflowCanvasNodeData;
  selected: boolean;
  draggable: boolean;
  connectable: boolean;
  deletable: false;
  focusable: true;
}

export interface AgentWorkflowGraphEdge {
  id: string;
  source: string;
  target: string;
  sourceHandle: string;
  targetHandle: "in";
  type: "sequence";
  animated: boolean;
  selectable: boolean;
  deletable: false;
  updatable: false;
  data: { status: string; branch?: 'true' | 'false' };
}

export function agentWorkflowNodeIds(definition: AgentWorkflowDefinition): string[] {
  return [AGENT_WORKFLOW_INPUT, ...definition.steps.map(step => step.id), AGENT_WORKFLOW_OUTPUT];
}

export function validWorkflowPosition(position: unknown): position is AgentWorkflowPosition {
  if (!position || typeof position !== "object") return false;
  const point = position as AgentWorkflowPosition;
  return Number.isFinite(point.x) && Number.isFinite(point.y) && Math.abs(point.x) <= 1_000_000 && Math.abs(point.y) <= 1_000_000;
}

export function validWorkflowViewport(viewport: unknown): viewport is AgentWorkflowViewport {
  return validWorkflowPosition(viewport) && Number.isFinite((viewport as AgentWorkflowViewport).zoom) && (viewport as AgentWorkflowViewport).zoom >= 0.1 && (viewport as AgentWorkflowViewport).zoom <= 4;
}

/** Execution order is the step array. Coordinates never change that order. */
export function layoutAgentWorkflow(definition: AgentWorkflowDefinition): AgentWorkflowPositions {
  if (definition.graph) {
    const levels = new Map<string, number>([[AGENT_WORKFLOW_INPUT, 0]]), columns = new Map<number, string[]>();
    for (const id of workflowTopologicalIds(definition)) {
      const level = levels.get(id) || 0;
      columns.set(level, [...(columns.get(level) || []), id]);
      for (const edge of outgoingWorkflowEdges(definition, id)) levels.set(edge.target, Math.max(levels.get(edge.target) || 0, level + 1));
    }
    const result: AgentWorkflowPositions = {};
    for (const [level, ids] of columns) ids.forEach((id, row) => { result[id] = { x: 48 + level * 310, y: 92 + (row - (ids.length - 1) / 2) * 230 }; });
    for (const id of agentWorkflowNodeIds(definition)) result[id] ||= { x: 48, y: 92 + Object.keys(result).length * 210 };
    return result;
  }
  return Object.fromEntries(agentWorkflowNodeIds(definition).map((id, index) => [id, { x: 48 + index * 310, y: 92 }]));
}

/** Keep known valid coordinates; stale positions do not become phantom nodes. */
export function mergeAgentWorkflowPositions(definition: AgentWorkflowDefinition, positions: AgentWorkflowPositions = {}): AgentWorkflowPositions {
  const result = layoutAgentWorkflow(definition);
  const occupied: AgentWorkflowPosition[] = [];
  for (const id of Object.keys(result)) {
    if (validWorkflowPosition(positions[id])) { result[id] = { ...positions[id] }; occupied.push(result[id]); }
  }
  // Inserting a step must not move existing user-arranged nodes. Place a new
  // node on a free row if its default location is already occupied instead.
  for (const id of Object.keys(result)) {
    if (validWorkflowPosition(positions[id])) continue;
    while (occupied.some(point => Math.abs(point.x - result[id].x) < 280 && Math.abs(point.y - result[id].y) < 190)) result[id].y += 210;
    occupied.push(result[id]);
  }
  return result;
}

const states = new Set(["queued", "running", "waiting_confirmation", "completed", "failed", "cancelled", "interrupted", "pending", "waiting", "skipped"]);
function safeState(value?: string): string { return value && states.has(value) ? value : "pending"; }
const stoppedRunStates = new Set(["failed", "cancelled", "interrupted"]);
const terminalRunStates = new Set(["completed", ...stoppedRunStates]);
const terminalStepStates = new Set(["completed", "failed", "cancelled", "interrupted", "skipped"]);

/** Project a stopped run without rewriting its preserved per-step records. */
export function agentWorkflowStepDisplayStatus(run: AgentWorkflowRun | null | undefined, stepId: string): string {
  if (!run) return "";
  const record = run.steps?.find(step => step.step_id === stepId);
  const status = safeState(record?.status);
  if (!stoppedRunStates.has(run.status) || terminalStepStates.has(status)) return status;
  const index = run.definition.steps.findIndex(step => step.id === stepId);
  if (index < 0) return status;
  const current = run.current_step ?? 0;
  // Older records can lack timestamps; their active state is still evidence
  // that the step began. A final run can no longer be waiting or queued.
  const started = !!record?.started_at || ["running", "waiting_confirmation", "waiting"].includes(status);
  if (run.definition.graph) return started ? run.status : 'skipped';
  if (index === current || started) return run.status;
  return index > current ? "skipped" : status;
}

/** A terminal run's duration stops at its recorded update, never today's time. */
export function agentWorkflowStepElapsedMs(run: AgentWorkflowRun | null | undefined, stepId: string, now = Date.now()): number | undefined {
  if (!run) return undefined;
  const record = run.steps?.find(step => step.step_id === stepId);
  if (!record?.started_at || !Number.isFinite(record.started_at)) return undefined;
  const end = record.finished_at || (terminalRunStates.has(run.status) ? run.updated_at : now);
  return end && Number.isFinite(end) ? Math.max(0, end - record.started_at) : undefined;
}

export function buildAgentWorkflowGraph(options: {
  definition: AgentWorkflowDefinition;
  run?: AgentWorkflowRun | null;
  positions?: AgentWorkflowPositions;
  selectedId?: string;
  editable?: boolean;
  updatedIds?: string[];
  activities?: Record<string, AgentWorkflowNodeActivity>;
  now?: number;
}): { nodes: AgentWorkflowGraphNode[]; edges: AgentWorkflowGraphEdge[] } {
  const definition = options.run?.definition ?? options.definition;
  const run = options.run;
  const editable = !!options.editable && !run;
  // History deliberately uses its frozen definition and an automatic layout,
  // never today's draft positions or a newly added draft node.
  const positions = mergeAgentWorkflowPositions(definition, run ? {} : options.positions);
  const stepRuns = new Map(run?.steps?.map(step => [step.step_id, step]) ?? []);
  const now = options.now ?? Date.now();
  const data: AgentWorkflowCanvasNodeData[] = [{
    id: AGENT_WORKFLOW_INPUT, kind: "input", title: "",
    description: (definition.inputs ?? []).map(input => input.label || input.key).join(" · "),
    status: run ? run.status === "queued" ? "queued" : "completed" : "", active: options.selectedId === AGENT_WORKFLOW_INPUT, editable,
  }];
  for (const [index, step] of definition.steps.entries()) {
    const stored = stepRuns.get(step.id);
    const projectedStatus = agentWorkflowStepDisplayStatus(run, step.id);
    const observed = run && step.kind === 'agent' ? options.activities?.[step.id] : undefined;
    // Activity belongs to one node session/attempt. A completed stored result
    // also wins over an earlier stream frame that is still in flight.
    const matching = observed?.step_id === step.id && (!stored?.session_id || !observed.session_id || stored.session_id === observed.session_id) &&
      (!stored?.active_run_id || !observed.active_run_id || stored.active_run_id === observed.active_run_id);
    const terminal = !!run && (terminalRunStates.has(run.status) || terminalStepStates.has(projectedStatus));
    const activity = matching && observed ? { ...observed, ...(terminal ? { status: projectedStatus, phase: projectedStatus } : {}) } : undefined;
    data.push({
      id: step.id, kind: step.kind, title: step.title || step.id, codeLanguage: step.code?.language,
      repeatProgress: stored?.repeat ? {kind:step.kind,completed:stored.repeat.items.filter(item=>item.status==='completed').length,failed:stored.repeat.items.filter(item=>['failed','interrupted','cancelled'].includes(item.status) || item.status==='running' && (['failed','interrupted','cancelled'].includes(stored.status)||['failed','interrupted','cancelled'].includes(run?.status||''))).length,total:stored.repeat.total} : undefined,
      description: ['iteration','loop'].includes(step.kind) ? stored?.summary || '' : step.kind === 'code' ? `${step.code?.language === 'javascript' ? 'JavaScript' : 'Python'} · ${stored?.summary || step.code?.source.split('\n').find(line => line.trim()) || ''}` : step.kind === 'condition' ? [step.condition?.source, step.condition?.operator, step.condition?.value].filter(value => value !== undefined).join(' ') : step.task || step.prompt || "", outputFile: step.output_file,
      status: activity && !terminal ? safeState(activity.status) : projectedStatus, active: options.selectedId === step.id, editable, index: index + 1,
      elapsedMs: agentWorkflowStepElapsedMs(run, step.id, now),
      branch: stepRuns.get(step.id)?.branch,
      inspectable: !!run && step.kind === 'agent', activity,
    });
  }
  const last = definition.graph ? [...definition.steps].reverse().find(step => step.output_file) : definition.steps[definition.steps.length - 1];
  data.push({
    id: AGENT_WORKFLOW_OUTPUT, kind: "output", title: "", description: "",
    outputFile: (last && stepRuns.get(last.id)?.artifact) || last?.output_file,
    status: run ? run.status === "completed" ? "completed" : ["failed", "cancelled", "interrupted"].includes(run.status) ? "skipped" : "pending" : "",
    active: options.selectedId === AGENT_WORKFLOW_OUTPUT, editable,
  });
  const updated = new Set(run ? [] : options.updatedIds);
  const nodes = data.map(node => ({
    id: node.id, type: "procedure" as const, position: positions[node.id], data: {
      ...node,
      updated: updated.has(node.id),
      incomingCount: incomingWorkflowEdges(definition, node.id).length,
      outgoingCount: outgoingWorkflowEdges(definition, node.id).length,
      // Stable shared ports can accept additional connections even after every
      // existing edge has been removed from a partially edited draft.
      ports: node.kind === 'output' ? [] : node.kind === 'condition' ? [{ id: 'true', label: 'true' }, { id: 'false', label: 'false' }] : [{ id: 'out', label: '' }],
      canInsert: workflowNodeCount(definition) < AGENT_WORKFLOW_MAX_STEPS && outgoingWorkflowEdges(definition, node.id).length === 1,
    },
    selected: node.active, draggable: editable, connectable: editable,
    deletable: false as const, focusable: true as const,
  }));
  const edges = workflowEdges(definition).map(edge => {
    const source = stepRuns.get(edge.source), target = nodes.find(node => node.id === edge.target)!;
    const skipped = run && (source?.status === 'skipped' || !!edge.branch && source?.branch !== undefined && source.branch !== edge.branch);
    const status = skipped ? 'skipped' : target?.data.status || '';
    return {
    id: definition.graph ? 'dag:' + workflowEdgeKey(edge) : "chain:" + edge.source + ":" + edge.target,
    source: edge.source, target: edge.target, sourceHandle: workflowEdgeHandle(definition, edge), targetHandle: "in" as const,
    type: "sequence" as const, animated: status === "running",
    selectable: editable, deletable: false as const, updatable: false as const,
    data: { status, branch: edge.branch },
  }; });
  return { nodes, edges };
}

export type AgentWorkflowConnectionMove =
  | { ok: true; stepId: string; index: number }
  | { ok: false; reason: "unknown" | "self" | "boundary" | "unchanged" };

/** A connection expresses an insertion in one chain, never another edge. */
export function agentWorkflowConnectionMove(definition: AgentWorkflowDefinition, source: string, target: string): AgentWorkflowConnectionMove {
  const ids = definition.steps.map(step => step.id);
  const known = new Set([AGENT_WORKFLOW_INPUT, ...ids, AGENT_WORKFLOW_OUTPUT]);
  if (!known.has(source) || !known.has(target)) return { ok: false, reason: "unknown" };
  if (source === target) return { ok: false, reason: "self" };
  if (source === AGENT_WORKFLOW_OUTPUT || target === AGENT_WORKFLOW_INPUT || source === AGENT_WORKFLOW_INPUT && target === AGENT_WORKFLOW_OUTPUT) return { ok: false, reason: "boundary" };
  let stepId: string, index: number;
  if (source === AGENT_WORKFLOW_INPUT) { stepId = target; index = 0; }
  else if (target === AGENT_WORKFLOW_OUTPUT) { stepId = source; index = ids.length - 1; }
  else {
    stepId = target;
    index = ids.filter(id => id !== target).indexOf(source) + 1;
  }
  if (ids.indexOf(stepId) === index) return { ok: false, reason: "unchanged" };
  return { ok: true, stepId, index };
}

export function agentWorkflowAfterId(definition: AgentWorkflowDefinition, nodeId: string): string | null | undefined {
  if (nodeId === AGENT_WORKFLOW_INPUT) return null;
  return definition.steps.some(step => step.id === nodeId) ? nodeId : undefined;
}
