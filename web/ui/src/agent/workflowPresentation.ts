import type {
  DepWhen,
  OrchestrateConfig,
  WorkflowDep,
  WorkflowDepWire,
  WorkflowInput,
  WorkflowInputNode,
  WorkflowNode,
  WorkflowNodeKind,
  WorkflowNodeState,
  WorkflowRun,
  WorkflowRunCapabilities,
  Workflow,
  WorkflowStreamEvent,
} from "./workflowTypes";
import { cloneWorkflowApprovalPolicy } from "./workflowApprovalPolicy";

// Workflow canvas rules, pure and DOM-free so every transition the editor can
// make (connect, delete, drop, validate, lay out, overlay a run) is testable
// without mounting Vue Flow. The wire model is nodes + after[] dependencies
// with when conditions; the canvas only ever derives its edges and positions
// from it.

export const WORKFLOW_LIMITS = {
  name: 60,
  description: 240,
  task: 4000,
  nodes: 50,
  candidates: 20,
  dispatchTask: 2000,
} as const;

/** Layered DAG layout: depth down the y axis, siblings of one depth fanned
    out on x, each layer centered on the spine so a plain chain stays x=0. */
export const WORKFLOW_LAYOUT = { gapX: 300, gapY: 150 } as const;

/** Backend validNodeID (workflow/definition.go): session-safe, no "..". */
export const NODE_ID_PATTERN = /^[A-Za-z0-9._-]+$/;

export function validNodeId(id: string): boolean {
  return id !== "" && id.length <= 64 && NODE_ID_PATTERN.test(id) && !id.includes("..");
}

export const DEP_WHENS: readonly DepWhen[] = ["success", "failed", "rejected", "always"];

/* ------------------------------ dep wire form ------------------------------ */

export function normalizeDep(dep: WorkflowDepWire): WorkflowDep {
  return typeof dep === "string" ? { node: dep, when: "success" } : { node: dep.node, when: dep.when ?? "success" };
}

export function normalizeAfter(after: readonly WorkflowDepWire[] | undefined): WorkflowDep[] | undefined {
  return after?.map(normalizeDep);
}

/** Success deps degrade to plain strings, so untouched v1 data round-trips
    byte-identical and only explicit conditions grow object noise. */
export function serializeDep(dep: WorkflowDep): WorkflowDepWire {
  return dep.when === "success" ? dep.node : { node: dep.node, when: dep.when };
}

export function serializeAfter(after: readonly WorkflowDep[] | undefined): WorkflowDepWire[] | undefined {
  if (!after?.length) return undefined;
  return after.map(serializeDep);
}

/* ------------------------------ graph shape ------------------------------ */

/** Kahn topological order, stable among equals (declaration order breaks
    ties, so a graph the user built in order never shuffles). null on a cycle —
    callers treat that as the graph's one structural error. */
export function topoOrder(nodes: readonly WorkflowNode[]): string[] | null {
  const ids = nodes.map((n) => n.id);
  const indegree = new Map<string, number>(ids.map((id) => [id, 0]));
  const downstream = new Map<string, string[]>();
  for (const node of nodes) {
    for (const dep of node.after ?? []) {
      if (!indegree.has(dep.node)) continue; // dangling refs are validation's job
      indegree.set(node.id, (indegree.get(node.id) ?? 0) + 1);
      downstream.set(dep.node, [...(downstream.get(dep.node) ?? []), node.id]);
    }
  }
  const ready = ids.filter((id) => (indegree.get(id) ?? 0) === 0);
  const order: string[] = [];
  while (ready.length) {
    const id = ready.shift()!;
    order.push(id);
    for (const down of downstream.get(id) ?? []) {
      const d = (indegree.get(down) ?? 0) - 1;
      indegree.set(down, d);
      if (d === 0) ready.push(down);
    }
  }
  return order.length === nodes.length ? order : null;
}

export function hasCycle(nodes: readonly WorkflowNode[]): boolean {
  return topoOrder(nodes) === null;
}

/** Every node reachable by walking after[] upward — the closure a template
    reference (and the autocomplete) may legitimately name. Cycle-safe. */
export function upstreamClosure(nodes: readonly WorkflowNode[], id: string): string[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const seen = new Set<string>();
  const walk = (nid: string) => {
    for (const dep of byId.get(nid)?.after ?? []) {
      if (seen.has(dep.node)) continue;
      seen.add(dep.node);
      walk(dep.node);
    }
  };
  walk(id);
  seen.delete(id);
  return [...seen];
}

/** An edge source→target reads "target runs when source ends the when way".
    Connecting is the only structural edit with a refusal: self-loops,
    duplicates and anything closing a cycle are rejected before the model ever
    changes. Returns the refusal code, or null when the edge may be added. */
export function connectRefusal(
  nodes: readonly WorkflowNode[],
  source: string,
  target: string,
): "self" | "duplicate" | "cycle" | null {
  if (source === target) return "self";
  const to = nodes.find((n) => n.id === target);
  if (!to || !nodes.some((n) => n.id === source)) return "cycle";
  if ((to.after ?? []).some((d) => d.node === source)) return "duplicate";
  const next = nodes.map((n) =>
    n.id === target ? { ...n, after: [...(n.after ?? []), { node: source, when: "success" as const }] } : n,
  );
  return hasCycle(next) ? "cycle" : null;
}

/** Add source→target with a when, or null when connectRefusal refuses. */
export function addDependency(
  nodes: readonly WorkflowNode[],
  source: string,
  target: string,
  when: DepWhen = "success",
): WorkflowNode[] | null {
  if (connectRefusal(nodes, source, target) !== null) return null;
  return nodes.map((n) => (n.id === target ? { ...n, after: [...(n.after ?? []), { node: source, when }] } : n));
}

/** Change one edge's condition (the edge menu's edit). */
export function updateDependency(nodes: readonly WorkflowNode[], source: string, target: string, when: DepWhen): WorkflowNode[] {
  return nodes.map((n) =>
    n.id === target
      ? { ...n, after: (n.after ?? []).map((d) => (d.node === source ? { ...d, when } : d)) }
      : n,
  );
}

export function removeDependency(nodes: readonly WorkflowNode[], source: string, target: string): WorkflowNode[] {
  return nodes.map((n) =>
    n.id === target ? { ...n, after: (n.after ?? []).filter((d) => d.node !== source) } : n,
  );
}

/** Deleting a node cascades: every downstream after reference goes with it,
    so the surviving graph never points at a ghost. */
export function removeNode(nodes: readonly WorkflowNode[], id: string): WorkflowNode[] {
  return nodes
    .filter((n) => n.id !== id)
    .map((n) => ({ ...n, after: (n.after ?? []).filter((d) => d.node !== id) }));
}

/** The first free generated id; the drawer never edits ids, so generated
    ones simply stay out of the way of hand-named ones. */
export function nextNodeId(nodes: readonly WorkflowNode[]): string {
  const taken = new Set(nodes.map((n) => n.id));
  for (let i = 1; ; i++) {
    if (!taken.has(`n${i}`)) return `n${i}`;
  }
}

/* ------------------------------ derivation ------------------------------ */

export interface WorkflowEdge {
  id: string;
  source: string;
  target: string;
  when: DepWhen;
}

/** Dependency edges derive from after[]; there is no separate edge model. */
export function workflowEdges(nodes: readonly WorkflowNode[]): WorkflowEdge[] {
  const known = new Set(nodes.map((n) => n.id));
  const edges: WorkflowEdge[] = [];
  for (const node of nodes) {
    for (const dep of node.after ?? []) {
      if (known.has(dep.node)) edges.push({ id: `${dep.node}->${node.id}`, source: dep.node, target: node.id, when: dep.when });
    }
  }
  return edges;
}

/** An orchestrator's candidates as dashed annotation edges: from the
    orchestrator node to every canvas node whose Agent is a candidate. These
    are display furniture, not dependencies. */
export function candidateEdges(nodes: readonly WorkflowNode[]): WorkflowEdge[] {
  const edges: WorkflowEdge[] = [];
  for (const node of nodes) {
    const candidates = node.orchestrate?.candidates ?? [];
    if (!candidates.length) continue;
    const wanted = new Set(candidates.map((c) => c.agent));
    for (const target of nodes) {
      if (target.id !== node.id && wanted.has(target.agent)) {
        edges.push({ id: `cand:${node.id}->${target.id}`, source: node.id, target: target.id, when: "always" });
      }
    }
  }
  return edges;
}

/** Depth = longest path from a root (Kahn layers); one layer's siblings fan
    out centered on x=0, so a chain keeps its single spine. A cyclic graph
    (already refused by the editor) falls back to declaration order. */
export function layoutPositions(nodes: readonly WorkflowNode[]): Record<string, { x: number; y: number }> {
  const order = topoOrder(nodes);
  const positions: Record<string, { x: number; y: number }> = {};
  if (!order) {
    nodes.forEach((n, i) => {
      positions[n.id] = { x: 0, y: i * WORKFLOW_LAYOUT.gapY };
    });
    return positions;
  }
  const depth = new Map<string, number>();
  const byId = new Map(nodes.map((n) => [n.id, n]));
  for (const id of order) {
    const deps = (byId.get(id)?.after ?? []).filter((d) => depth.has(d.node));
    depth.set(id, deps.length ? Math.max(...deps.map((d) => depth.get(d.node)!)) + 1 : 0);
  }
  const layers = new Map<number, string[]>();
  for (const id of order) {
    const d = depth.get(id) ?? 0;
    layers.set(d, [...(layers.get(d) ?? []), id]);
  }
  for (const [d, ids] of layers) {
    ids.forEach((id, i) => {
      positions[id] = {
        x: (i - (ids.length - 1) / 2) * WORKFLOW_LAYOUT.gapX,
        y: d * WORKFLOW_LAYOUT.gapY,
      };
    });
  }
  return positions;
}

/* ------------------------------ templates ------------------------------ */

export interface TemplateRef {
  node: string;
  field: "artifact" | "summary";
}

const TEMPLATE_REF_RE = /\{\{\s*nodes\.([A-Za-z0-9._-]+)\.(artifact|summary)\s*\}\}/g;

/** The {{ nodes.<id>.artifact|summary }} references in one text, in order. */
export function templateRefs(text: string): TemplateRef[] {
  const refs: TemplateRef[] = [];
  for (const m of text.matchAll(TEMPLATE_REF_RE)) {
    refs.push({ node: m[1]!, field: m[2] as TemplateRef["field"] });
  }
  return refs;
}

/* ------------------------------ validation ------------------------------ */

export interface WorkflowValidation {
  /** Form-level issue codes: name/description/limits/cycle. */
  form: string[];
  /** Node-level issue codes keyed by node id (the red corner badge's list). */
  nodes: Record<string, string[]>;
  ok: boolean;
}

const TIMEOUT_RE = /^\d+(s|m|h)$/;

function validateTemplateText(
  text: string,
  all: readonly WorkflowNode[],
  closure: readonly string[],
  issues: string[],
) {
  for (const ref of templateRefs(text)) {
    const target = all.find((n) => n.id === ref.node);
    if (!target) {
      if (!issues.includes("ref_unknown")) issues.push("ref_unknown");
    } else if (!closure.includes(ref.node)) {
      if (!issues.includes("ref_not_upstream")) issues.push("ref_not_upstream");
    } else if (ref.field === "artifact" && !target.expect?.path) {
      if (!issues.includes("ref_no_expect")) issues.push("ref_no_expect");
    }
  }
}

function validateOrchestrate(orch: OrchestrateConfig, known: Set<string> | null, issues: string[]) {
  if (!orch.candidates.length) issues.push("orch_candidates");
  if (orch.candidates.length > WORKFLOW_LIMITS.candidates) issues.push("orch_too_many");
  for (const c of orch.candidates) {
    if (!c.agent) {
      if (!issues.includes("orch_agent")) issues.push("orch_agent");
    } else if (known && !known.has(c.agent)) {
      if (!issues.includes("orch_agent_unknown")) issues.push("orch_agent_unknown");
    }
  }
}

/**
 * Client-side mirror of the server's definition validation. Every issue is a
 * code (the view translates it); an empty `nodes[id]` entry is omitted. When
 * the Agent catalog is passed, references to unknown or deleted Agents are
 * flagged too — without it the check is shape-only.
 */
export function validateWorkflow(
  input: { name: string; description: string; nodes: readonly WorkflowNode[] },
  knownAgents?: readonly { id: string }[],
): WorkflowValidation {
  const form: string[] = [];
  const nodeIssues: Record<string, string[]> = {};
  const name = (input.name ?? "").trim();
  if (!name) form.push("name_required");
  else if (name.length > WORKFLOW_LIMITS.name) form.push("name_too_long");
  if ((input.description ?? "").trim().length > WORKFLOW_LIMITS.description) form.push("description_too_long");
  if (!input.nodes.length) form.push("nodes_required");
  if (input.nodes.length > WORKFLOW_LIMITS.nodes) form.push("nodes_too_many");

  const seen = new Set<string>();
  const known = knownAgents ? new Set(knownAgents.map((a) => a.id)) : null;
  for (const node of input.nodes) {
    const issues: string[] = [];
    const human = node.kind === "human";
    if (!validNodeId(node.id)) issues.push("id_invalid");
    else if (seen.has(node.id)) issues.push("id_duplicate");
    seen.add(node.id);
    if (human) {
      if (!node.prompt?.trim()) issues.push("prompt_required");
    } else {
      if (!node.agent) issues.push("agent_required");
      else if (known && !known.has(node.agent)) issues.push("agent_unknown");
      if (!node.task?.trim()) issues.push("task_required");
      else if (node.task.length > WORKFLOW_LIMITS.task) issues.push("task_too_long");
    }
    for (const dep of node.after ?? []) {
      if (!DEP_WHENS.includes(dep.when) && !issues.includes("after_when_invalid")) issues.push("after_when_invalid");
      if (!input.nodes.some((n) => n.id === dep.node) && !issues.includes("after_unknown")) issues.push("after_unknown");
    }
    const closure = upstreamClosure(input.nodes, node.id);
    validateTemplateText(node.task ?? "", input.nodes, closure, issues);
    if (human) validateTemplateText(node.prompt ?? "", input.nodes, closure, issues);
    if (node.orchestrate) validateOrchestrate(node.orchestrate, known, issues);
    if (node.approval_policy) {
      const policy = node.approval_policy;
      if (!human && !node.approval) issues.push("approval_policy_requires_review");
      if (!["fixed", "business"].includes(policy.assignment)) issues.push("approval_assignment_invalid");
      if (policy.assignment === "fixed") {
        if (!policy.approvers?.length) issues.push("approval_approvers_required");
        else if (policy.approvers.some(name => !name.trim() || name !== name.trim()) || new Set(policy.approvers).size !== policy.approvers.length) issues.push("approval_approver_invalid");
      }
    }
    if (human) {
      const keys = new Set<string>();
      for (const f of node.fields ?? []) {
        if (!f.key.trim()) {
          if (!issues.includes("field_key")) issues.push("field_key");
        } else if (keys.has(f.key)) {
          if (!issues.includes("field_dup")) issues.push("field_dup");
        }
        keys.add(f.key);
        if (f.type === "select" && !(f.options ?? []).some((o) => o.trim()) && !issues.includes("field_options"))
          issues.push("field_options");
      }
      if (node.timeout && !TIMEOUT_RE.test(node.timeout.trim())) issues.push("timeout_invalid");
    }
    if (issues.length) nodeIssues[node.id] = issues;
  }
  if (hasCycle(input.nodes)) form.push("cycle");
  return { form, nodes: nodeIssues, ok: form.length === 0 && Object.keys(nodeIssues).length === 0 };
}

/* ------------------------------ persistence ------------------------------ */

/** The wire shape a save sends: server fields (id, revision, published,
    dirty, stamps) stripped, empty optionals dropped, after serialized back
    to the dual wire form. */
export function toWorkflowInput(workflow: { name: string; description: string; nodes: readonly WorkflowNode[] }): WorkflowInput {
  return {
    name: workflow.name ?? "",
    description: workflow.description ?? "",
    nodes: workflow.nodes.map((n) => {
      const out: WorkflowInputNode = { id: n.id, agent: n.agent ?? "", task: n.task ?? "" };
      if (n.kind && n.kind !== "agent") out.kind = n.kind;
      if (n.expect?.path) out.expect = { ...n.expect };
      const after = serializeAfter(n.after);
      if (after) out.after = after;
      if (n.tools) out.tools = [...n.tools];
      if (n.model) out.model = n.model;
      if (n.max_turns) out.max_turns = n.max_turns;
      if (n.approval) out.approval = true;
      if (n.approval_policy) out.approval_policy = cloneWorkflowApprovalPolicy(n.approval_policy);
      if (n.orchestrate?.candidates.length)
        out.orchestrate = {
          candidates: n.orchestrate.candidates.map((c) => ({ ...c })),
          max_dispatches: n.orchestrate.max_dispatches || undefined,
          max_parallel: n.orchestrate.max_parallel || undefined,
        };
      if (n.prompt) out.prompt = n.prompt;
      if (n.fields?.length) out.fields = n.fields.map((f) => ({ ...f, ...(f.options ? { options: [...f.options] } : {}) }));
      if (n.actions?.length) out.actions = [...n.actions];
      if (n.timeout) out.timeout = n.timeout;
      if (n.on_timeout) out.on_timeout = n.on_timeout;
      return out;
    }),
  };
}

/** The leave gate's question: does the draft differ from the last save? */
export function workflowDirty(saved: WorkflowInput, draft: WorkflowInput): boolean {
  return JSON.stringify(saved) !== JSON.stringify(draft);
}

/* ------------------------------ run overlay ------------------------------ */

/** Per-node run state for the canvas overlay; nodes the record does not know
    yet read as no-dot (a node added after the run started has no state). */
export function runNodeStates(run: WorkflowRun | null, nodeIds: readonly string[]): Record<string, WorkflowNodeState | ""> {
  const states: Record<string, WorkflowNodeState | ""> = {};
  for (const id of nodeIds) states[id] = run?.nodes[id]?.status ?? "";
  return states;
}

export function runLive(run: WorkflowRun | null): boolean {
  return !!run && ["queued", "working", "running", "waiting_human"].includes(run.status) && !run.persistence_error;
}

/** Display the storage stop separately without inventing a persisted status. */
export function runDisplayStatus(run: WorkflowRun): WorkflowRun["status"] | "persistence_error" {
  if (run.persistence_error) return "persistence_error";
  return runSuspended(run) ? "waiting_human" : run.status;
}

/** A suspended run: working, parked on at least one human wait. */
export function runSuspended(run: WorkflowRun | null): boolean {
  if (!run || !runLive(run)) return false;
  if (run.status === "waiting_human") return true;
  const nodes = Object.values(run.nodes);
  return nodes.some((n) => n.status === "waiting_human") && !nodes.some((n) => n.status === "running");
}

/** Explicit server capabilities take precedence over legacy internal UI defaults. */
export function workflowRunCan(run: WorkflowRun | null, action: Exclude<keyof WorkflowRunCapabilities, "can_approve_nodes" | "can_read_artifact_nodes">, legacyAllowed = true, nodeId?: string): boolean {
  if (!run) return false;
  if (action === "can_approve") {
    const assignment = nodeId ? run.approvals?.[nodeId] : undefined;
    const configured = nodeId ? !!assignment || !!run.definition.nodes.find(node => node.id === nodeId)?.approval_policy : !!Object.keys(run.approvals ?? {}).length;
    if (configured) {
      if (assignment?.blocked_reason || !run.capabilities?.can_approve) return false;
      return nodeId ? run.capabilities.can_approve_nodes?.includes(nodeId) === true : true;
    }
  }
  if (!run.capabilities) return legacyAllowed;
  if (!run.capabilities[action]) return false;
  if (!nodeId) return true;
  if (action === "can_approve" && run.capabilities.can_approve_nodes) return run.capabilities.can_approve_nodes.includes(nodeId);
  if (action === "can_read_artifacts" && run.capabilities.can_read_artifact_nodes) return run.capabilities.can_read_artifact_nodes.includes(nodeId);
  return true;
}

/** Historical results use their own graph; wire dependencies may be strings. */
export function workflowRunNodes(run: WorkflowRun | null): WorkflowNode[] {
  return (run?.definition.nodes ?? []).map((node) => ({
    ...node,
    after: normalizeAfter(node.after as unknown as WorkflowDepWire[] | undefined),
  }));
}

/** Deleted definitions remain readable through the immutable run snapshot. */
export function workflowHistoryDefinition(run: WorkflowRun): Workflow {
  return {
    ...run.definition,
    id: run.workflow_id,
    nodes: workflowRunNodes(run),
    published: null,
    dirty: false,
    created_at: run.created_at,
    updated_at: run.created_at,
    capabilities: { can_edit: false, can_publish: false, can_invoke: false },
  };
}

export function sortWorkflowRuns(runs: readonly WorkflowRun[]): WorkflowRun[] {
  return [...runs].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id));
}

/** Poll cadence while SSE is unavailable (or between streams): tight while
    agents run, relaxed while only a person is being waited on. */
export function pollIntervalFor(run: WorkflowRun | null): number {
  return runSuspended(run) ? 5000 : 1500;
}

/** The run record a stream frame carries, or null for keepalives, node
    frames and dispatch-only events. */
export function streamEventRun(event: unknown): WorkflowRun | null {
  const e = event as WorkflowStreamEvent | null;
  return e?.workflow_run ?? null;
}

export function streamEventType(event: unknown): string {
  return (event as WorkflowStreamEvent | null)?.type ?? "";
}

/* ------------------------------ canvas view ------------------------------ */

/** What the canvas's custom node renders. Kept Vue-Flow-free: the editor's
    Node objects are built from this shape, and AgentFlowNode's props mirror
    it, so the whole overlay (run states, validation badges, selection ring)
    is testable without mounting anything. */
export interface AgentFlowNodeData {
  label: string;
  color: string;
  kind: WorkflowNodeKind | "";
  task: string;
  readOnly: boolean;
  expectPath: string;
  orchestrator: boolean;
  approval: boolean;
  state: WorkflowNodeState | "";
  issues: string[];
  missing: boolean;
  active: boolean;
  skippedBy: string;
  branch: string;
}

export interface AgentFlowNodeView {
  id: string;
  type: "agent";
  position: { x: number; y: number };
  data: AgentFlowNodeData;
}

export interface FlowNodeBuild {
  nodes: readonly WorkflowNode[];
  /** Resolves an Agent reference to its display identity; undefined = deleted. */
  agentOf: (agentId: string) => { name: string; color: string } | undefined;
  states: Record<string, WorkflowNodeState | "">;
  issues: Record<string, string[]>;
  /** Fully resolved positions (drag overrides + layered layout, caller's merge). */
  positions: Record<string, { x: number; y: number }>;
  /** Selected node id ("" = none); the ring follows it. */
  activeId: string;
  fallbackColor: string;
  /** The run being overlaid (null = none): skipped_by/branch tooltips. */
  run?: WorkflowRun | null;
  readOnly?: boolean;
}

export function buildFlowNodes(build: FlowNodeBuild): AgentFlowNodeView[] {
  return build.nodes.map((n) => {
    const agent = build.agentOf(n.agent);
    const record = build.run?.nodes[n.id];
    return {
      id: n.id,
      type: "agent",
      position: build.positions[n.id] ?? { x: 0, y: 0 },
      data: {
        label: n.kind === "human" ? n.id : agent?.name ?? n.agent ?? "",
        color: agent?.color ?? build.fallbackColor,
        kind: n.kind ?? "",
        task: n.kind === "human" ? n.prompt ?? "" : n.task ?? "",
        readOnly: !!build.readOnly,
        expectPath: n.expect?.path ?? "",
        orchestrator: !!n.orchestrate?.candidates.length,
        approval: !!n.approval,
        state: build.states[n.id] ?? "",
        issues: build.readOnly ? [] : build.issues[n.id] ?? [],
        missing: n.kind !== "human" && !agent,
        active: n.id === build.activeId,
        skippedBy: record?.skipped_by ?? "",
        branch: record?.branch ?? "",
      },
    };
  });
}
