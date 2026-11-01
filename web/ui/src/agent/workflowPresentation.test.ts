import { describe, expect, it } from "vitest";
import type { DepWhen, WorkflowDep, WorkflowNode, WorkflowRun } from "./workflowTypes";
import {
  addDependency,
  buildFlowNodes,
  candidateEdges,
  connectRefusal,
  hasCycle,
  layoutPositions,
  nextNodeId,
  normalizeAfter,
  normalizeDep,
  pollIntervalFor,
  removeDependency,
  removeNode,
  runLive,
  runDisplayStatus,
  runNodeStates,
  runSuspended,
  serializeAfter,
  serializeDep,
  streamEventRun,
  streamEventType,
  templateRefs,
  topoOrder,
  toWorkflowInput,
  updateDependency,
  upstreamClosure,
  validateWorkflow,
  validNodeId,
  workflowDirty,
  workflowEdges,
  workflowRunCan,
  workflowRunNodes,
  workflowHistoryDefinition,
  WORKFLOW_LIMITS,
} from "./workflowPresentation";

const dep = (node: string, when: DepWhen = "success"): WorkflowDep => ({ node, when });

const node = (id: string, extra: Partial<WorkflowNode> = {}): WorkflowNode => ({
  id,
  agent: "ag_1",
  task: `task for ${id}`,
  ...extra,
});

const runFixture = (extra: Partial<WorkflowRun> = {}): WorkflowRun => ({
  id: "run_1",
  workflow_id: "wf_1",
  revision: 1,
  definition: {
    id: "wf_1",
    name: "",
    description: "",
    revision: 1,
    nodes: [],
    published: null,
    dirty: false,
    created_at: "",
    updated_at: "",
  },
  status: "working",
  nodes: {},
  created_at: "2026-09-25T00:00:00Z",
  ...extra,
});

describe("unified workflow run permissions and snapshots", () => {
  it("renders and validates the actual redacted wire shape when omitempty removes text", () => {
    // Ordinary users receive a scrubbed definition. Go's omitempty removes
    // empty task/prompt/description keys rather than sending empty strings.
    const redacted = JSON.parse(`{"name":"审批流程","nodes":[{"id":"review","kind":"human"},{"id":"finish","agent":"ag_1","after":[{"node":"review","when":"success"}]}]}`);
    const before = JSON.stringify(redacted);
    const validation = validateWorkflow(redacted, [{ id: "ag_1" }]);
    expect(validation.nodes.review).toContain("prompt_required");
    expect(validation.nodes.finish).toContain("task_required");
    expect(validation.form).toEqual([]);
    const saved = toWorkflowInput(redacted);
    expect(saved.description).toBe("");
    expect(saved.nodes.map(item => item.task)).toEqual(["", ""]);
    const graph = buildFlowNodes({
      nodes: redacted.nodes, agentOf: () => undefined, states: {}, issues: {},
      positions: {}, activeId: "", fallbackColor: "gray",
    });
    expect(graph.map(item => item.data.task)).toEqual(["", ""]);
    expect(JSON.stringify(redacted)).toBe(before);
  });

  it("follows queued runs and distinguishes a pure human wait from parallel execution", () => {
    expect(runLive(runFixture({ status: "queued" }))).toBe(true);
    expect(runLive(runFixture({ status: "waiting_human" }))).toBe(true);
    expect(runLive(runFixture({ status: "interrupted" }))).toBe(false);
    const parallel = runFixture({ nodes: { review: { status: "waiting_human" }, analyze: { status: "running" } } });
    expect(runDisplayStatus(parallel)).toBe("working");
    expect(pollIntervalFor(parallel)).toBe(1500);
    expect(runDisplayStatus(runFixture({ nodes: { review: { status: "waiting_human" } } }))).toBe("waiting_human");
  });

  it("uses explicit capabilities and checks node-scoped approvals and artifacts", () => {
    const run = runFixture({ capabilities: {
      can_read: true, can_cancel: false, can_approve: true, can_retry: false,
      can_read_artifacts: true, can_manage_participants: false,
      can_approve_nodes: ["review"], can_read_artifact_nodes: ["report"],
    } });
    expect(workflowRunCan(run, "can_cancel", true)).toBe(false);
    expect(workflowRunCan(run, "can_approve", true, "review")).toBe(true);
    expect(workflowRunCan(run, "can_approve", true, "other")).toBe(false);
    expect(workflowRunCan(run, "can_read_artifacts", true, "report")).toBe(true);
    expect(workflowRunCan(run, "can_read_artifacts", true, "private")).toBe(false);
    expect(workflowRunCan(runFixture(), "can_cancel", true)).toBe(true);
    expect(workflowRunCan(runFixture(), "can_manage_participants", false)).toBe(false);
  });

  it("normalizes the selected run's own graph without using or mutating the current draft", () => {
    const run = runFixture();
    run.definition.nodes = [node("old"), node("old-review", { after: ["old"] as unknown as WorkflowDep[] })];
    const snapshot = workflowRunNodes(run);
    expect(snapshot.map(item => item.id)).toEqual(["old", "old-review"]);
    expect(snapshot[1]!.after).toEqual([dep("old")]);
    expect(run.definition.nodes[1]!.after).toEqual(["old"]);
    const archived = workflowHistoryDefinition(run);
    expect(archived.id).toBe(run.workflow_id);
    expect(archived.nodes.map(item => item.id)).toEqual(["old", "old-review"]);
    expect(archived.capabilities).toEqual({ can_edit: false, can_publish: false, can_invoke: false });
  });
});

describe("dep wire form", () => {
  it("reads both the v1 string and the v2 object form", () => {
    expect(normalizeDep("a")).toEqual({ node: "a", when: "success" });
    expect(normalizeDep({ node: "a" })).toEqual({ node: "a", when: "success" });
    expect(normalizeDep({ node: "a", when: "failed" })).toEqual({ node: "a", when: "failed" });
    expect(normalizeAfter(["a", { node: "b", when: "always" }])).toEqual([dep("a"), dep("b", "always")]);
    expect(normalizeAfter(undefined)).toBeUndefined();
  });

  it("serializes success back to a string and keeps non-success as objects", () => {
    expect(serializeDep(dep("a"))).toBe("a");
    expect(serializeDep(dep("a", "rejected"))).toEqual({ node: "a", when: "rejected" });
    expect(serializeAfter([dep("a"), dep("b", "failed")])).toEqual(["a", { node: "b", when: "failed" }]);
    expect(serializeAfter([])).toBeUndefined();
  });
});

describe("node ids", () => {
  it("accepts the backend validNodeID alphabet and rejects path shapes", () => {
    for (const id of ["analyze", "n1", "step-2", "a_b.c", "A9"]) expect(validNodeId(id)).toBe(true);
    for (const id of ["", "a/b", "a\\b", "..", "a..b", "a b", "分析"]) expect(validNodeId(id)).toBe(false);
  });

  it("generates the first free sequential id", () => {
    expect(nextNodeId([])).toBe("n1");
    expect(nextNodeId([node("n2")])).toBe("n1");
    expect(nextNodeId([node("n1"), node("n3")])).toBe("n2");
  });
});

describe("topoOrder / cycles", () => {
  it("orders a chain and keeps declaration order among independents", () => {
    expect(topoOrder([node("a"), node("b", { after: [dep("a")] }), node("c", { after: [dep("b")] })])).toEqual(["a", "b", "c"]);
    expect(topoOrder([node("x"), node("y")])).toEqual(["x", "y"]);
  });

  it("detects direct and indirect cycles regardless of when", () => {
    expect(hasCycle([node("a", { after: [dep("a")] })])).toBe(true);
    expect(
      hasCycle([node("a", { after: [dep("c", "failed")] }), node("b", { after: [dep("a")] }), node("c", { after: [dep("b", "always")] })]),
    ).toBe(true);
    expect(hasCycle([node("a"), node("b", { after: [dep("a", "failed")] })])).toBe(false);
  });
});

describe("upstreamClosure", () => {
  it("collects the transitive upstream set, cycle-safe, excluding self", () => {
    const nodes = [node("a"), node("b", { after: [dep("a")] }), node("c", { after: [dep("b"), dep("x")] }), node("x")];
    expect(upstreamClosure(nodes, "c").sort()).toEqual(["a", "b", "x"]);
    expect(upstreamClosure(nodes, "a")).toEqual([]);
  });
});

describe("connectRefusal / dependency edits", () => {
  const chain = [node("a"), node("b", { after: [dep("a")] }), node("c", { after: [dep("b")] })];

  it("refuses self-loops, duplicates and cycle-closing edges", () => {
    expect(connectRefusal(chain, "a", "a")).toBe("self");
    expect(connectRefusal(chain, "a", "b")).toBe("duplicate");
    expect(connectRefusal(chain, "c", "a")).toBe("cycle");
    expect(connectRefusal(chain, "a", "c")).toBeNull();
  });

  it("adds with a when, updates the when, and removes", () => {
    const added = addDependency(chain, "a", "c", "failed");
    expect(added?.find((n) => n.id === "c")?.after).toEqual([dep("b"), dep("a", "failed")]);
    const updated = updateDependency(added!, "a", "c", "always");
    expect(updated.find((n) => n.id === "c")?.after).toEqual([dep("b"), dep("a", "always")]);
    const removed = removeDependency(updated, "a", "c");
    expect(removed.find((n) => n.id === "c")?.after).toEqual([dep("b")]);
    expect(addDependency(chain, "c", "a")).toBeNull();
  });
});

describe("removeNode", () => {
  it("cascades: downstream after references go with the node", () => {
    const next = removeNode(
      [node("a"), node("b", { after: [dep("a")] }), node("c", { after: [dep("a", "failed"), dep("b")] })],
      "a",
    );
    expect(next.map((n) => n.id)).toEqual(["b", "c"]);
    expect(next.find((n) => n.id === "c")?.after).toEqual([dep("b")]);
  });
});

describe("derivation", () => {
  it("derives dependency edges with when, skipping dangling refs", () => {
    expect(workflowEdges([node("a"), node("b", { after: [dep("a"), dep("ghost", "failed")] })])).toEqual([
      { id: "a->b", source: "a", target: "b", when: "success" },
    ]);
  });

  it("derives dashed candidate edges from orchestrator nodes to matching agents", () => {
    const nodes = [
      node("orch", { orchestrate: { candidates: [{ agent: "ag_1" }, { agent: "ag_2" }] } }),
      node("w1", { agent: "ag_1" }),
      node("w2", { agent: "ag_2" }),
      node("w3", { agent: "ag_3" }),
    ];
    expect(candidateEdges(nodes).map((e) => e.id).sort()).toEqual(["cand:orch->w1", "cand:orch->w2"]);
    expect(candidateEdges([node("plain")])).toEqual([]);
  });

  it("lays out layers: depth down y, siblings centered on the spine", () => {
    const nodes = [node("a"), node("b", { after: [dep("a")] }), node("c", { after: [dep("a")] }), node("d", { after: [dep("b"), dep("c")] })];
    const pos = layoutPositions(nodes);
    expect(pos.a).toEqual({ x: 0, y: 0 });
    expect(pos.b!.y).toBe(150);
    expect(pos.c!.y).toBe(150);
    expect(pos.b!.x).toBe(-150);
    expect(pos.c!.x).toBe(150);
    expect(pos.d).toEqual({ x: 0, y: 300 });
  });

  it("keeps a plain chain on the x=0 spine", () => {
    const pos = layoutPositions([node("a"), node("b", { after: [dep("a")] })]);
    expect(pos.a).toEqual({ x: 0, y: 0 });
    expect(pos.b).toEqual({ x: 0, y: 150 });
  });
});

describe("templates", () => {
  it("extracts nodes.<id>.artifact|summary references in order", () => {
    expect(templateRefs("看 {{ nodes.a.artifact }} 和 {{nodes.b.summary}}，以及 {{ run.workspace }}")).toEqual([
      { node: "a", field: "artifact" },
      { node: "b", field: "summary" },
    ]);
    expect(templateRefs("没有引用")).toEqual([]);
  });
});

describe("validateWorkflow", () => {
  const valid = { name: "流水线", description: "", nodes: [node("a"), node("b", { after: [dep("a")] })] };

  it("passes a sound definition", () => {
    expect(validateWorkflow(valid).ok).toBe(true);
  });

  it("flags form-level issues", () => {
    expect(validateWorkflow({ ...valid, name: "" }).form).toContain("name_required");
    expect(validateWorkflow({ ...valid, name: "x".repeat(WORKFLOW_LIMITS.name + 1) }).form).toContain("name_too_long");
    expect(validateWorkflow({ ...valid, description: "x".repeat(WORKFLOW_LIMITS.description + 1) }).form).toContain("description_too_long");
    expect(validateWorkflow({ ...valid, nodes: [] }).form).toContain("nodes_required");
    expect(validateWorkflow({ ...valid, nodes: [node("a", { after: [dep("a")] })] }).form).toContain("cycle");
  });

  it("flags node-level issues keyed by node id", () => {
    const out = validateWorkflow({
      ...valid,
      nodes: [node("a", { agent: "" }), node("b", { task: "  " }), node("c", { after: [dep("ghost")] })],
    });
    expect(out.nodes.a).toEqual(["agent_required"]);
    expect(out.nodes.b).toEqual(["task_required"]);
    expect(out.nodes.c).toEqual(["after_unknown"]);
    expect(out.ok).toBe(false);
  });

  it("flags an illegal when", () => {
    const out = validateWorkflow({ ...valid, nodes: [node("a"), node("b", { after: [{ node: "a", when: "nope" as DepWhen }] })] });
    expect(out.nodes.b).toContain("after_when_invalid");
  });

  it("flags unknown agent references only when the catalog is given", () => {
    const input = { ...valid, nodes: [node("a", { agent: "ag_gone" })] };
    expect(validateWorkflow(input).ok).toBe(true);
    expect(validateWorkflow(input, [{ id: "ag_1" }]).nodes.a).toContain("agent_unknown");
  });

  it("validates template references against the upstream closure", () => {
    const withRef = (task: string) => validateWorkflow({ name: "w", description: "", nodes: [node("a", { expect: { path: "a.md" } }), node("b", { after: [dep("a")], task })] });
    expect(withRef("读 {{ nodes.a.artifact }}").ok).toBe(true);
    expect(withRef("读 {{ nodes.a.summary }}").ok).toBe(true);
    expect(validateWorkflow({ name: "w", description: "", nodes: [node("a"), node("b", { task: "{{ nodes.a.summary }}" })] }).nodes.b).toContain("ref_not_upstream");
    expect(validateWorkflow({ name: "w", description: "", nodes: [node("a"), node("b", { after: [dep("a")], task: "{{ nodes.a.artifact }}" })] }).nodes.b).toContain("ref_no_expect");
    expect(validateWorkflow({ name: "w", description: "", nodes: [node("a", { task: "{{ nodes.ghost.summary }}" })] }).nodes.a).toContain("ref_unknown");
  });

  it("validates human nodes: prompt, fields, timeout", () => {
    const human = (extra: Partial<WorkflowNode>) =>
      validateWorkflow({ name: "w", description: "", nodes: [node("h", { kind: "human", agent: "", task: "", ...extra })] });
    expect(human({}).nodes.h).toContain("prompt_required");
    expect(human({ prompt: "审一下" }).ok).toBe(true);
    expect(human({ prompt: "x", fields: [{ key: "" }] }).nodes.h).toContain("field_key");
    expect(human({ prompt: "x", fields: [{ key: "a" }, { key: "a" }] }).nodes.h).toContain("field_dup");
    expect(human({ prompt: "x", fields: [{ key: "a", type: "select" }] }).nodes.h).toContain("field_options");
    expect(human({ prompt: "x", fields: [{ key: "a", type: "select", options: ["y"] }] }).ok).toBe(true);
    expect(human({ prompt: "x", timeout: "72" }).nodes.h).toContain("timeout_invalid");
    expect(human({ prompt: "x", timeout: "72h" }).ok).toBe(true);
  });

  it("validates orchestrate candidates", () => {
    const orch = (o: WorkflowNode["orchestrate"]) => validateWorkflow({ name: "w", description: "", nodes: [node("a", { orchestrate: o })] });
    expect(orch({ candidates: [] }).nodes.a).toContain("orch_candidates");
    expect(orch({ candidates: [{ agent: "" }] }).nodes.a).toContain("orch_agent");
    expect(validateWorkflow({ name: "w", description: "", nodes: [node("a", { orchestrate: { candidates: [{ agent: "ag_gone" }] } })] }, [{ id: "ag_1" }]).nodes.a).toContain("orch_agent_unknown");
    expect(orch({ candidates: [{ agent: "ag_1", hint: "复核" }] }).ok).toBe(true);
  });
});

describe("persistence helpers", () => {
  it("serializes the wire shape: dual-form after, server fields stripped", () => {
    const saved = toWorkflowInput({
      name: "w",
      description: "d",
      nodes: [node("a", { after: [] }), node("b", { after: [dep("a"), dep("x", "failed")] })],
    });
    expect(saved).toEqual({
      name: "w",
      description: "d",
      nodes: [
        { id: "a", agent: "ag_1", task: "task for a" },
        { id: "b", agent: "ag_1", task: "task for b", after: ["a", { node: "x", when: "failed" }] },
      ],
    });
    expect(workflowDirty(saved, saved)).toBe(false);
    expect(workflowDirty(saved, { ...saved, name: "w2" })).toBe(true);
  });

  it("keeps human/orchestrate fields and drops empties", () => {
    const saved = toWorkflowInput({
      name: "w",
      description: "",
      nodes: [
        node("h", { kind: "human", agent: "", task: "", prompt: "审", actions: ["approve", "reject"], timeout: "72h", on_timeout: "reject" }),
        node("o", { approval: true, orchestrate: { candidates: [{ agent: "ag_1", hint: "x" }], max_dispatches: 0, max_parallel: 2 } }),
      ],
    });
    expect(saved.nodes[0]).toMatchObject({ kind: "human", prompt: "审", actions: ["approve", "reject"], timeout: "72h", on_timeout: "reject" });
    expect(saved.nodes[1]).toMatchObject({ approval: true, orchestrate: { candidates: [{ agent: "ag_1", hint: "x" }], max_parallel: 2 } });
    expect(saved.nodes[1]!.orchestrate?.max_dispatches).toBeUndefined();
  });
});

describe("run overlay", () => {
  const run = runFixture({
    status: "working",
    nodes: {
      a: { status: "success" },
      b: { status: "running" },
    },
  });

  it("maps record states and defaults unknown nodes to no-dot", () => {
    expect(runNodeStates(run, ["a", "b", "c"])).toEqual({ a: "success", b: "running", c: "" });
    expect(runNodeStates(null, ["a"])).toEqual({ a: "" });
  });

  it("knows live, suspended, and the poll cadence for each", () => {
    expect(runLive(run)).toBe(true);
    expect(runLive(runFixture({ status: "completed" }))).toBe(false);
    expect(runSuspended(run)).toBe(false);
    const suspended = runFixture({ nodes: { h: { status: "waiting_human" } } });
    expect(runSuspended(suspended)).toBe(true);
    expect(pollIntervalFor(run)).toBe(1500);
    expect(pollIntervalFor(suspended)).toBe(5000);
    expect(pollIntervalFor(null)).toBe(1500);
  });

  it("shows a persistence stop without rewriting durable states or treating it as a human wait", () => {
    const saved = runFixture({ persistence_error: "disk full", nodes: { a: { status: "running" }, h: { status: "waiting_human" } } });
    const adopted = streamEventRun({ type: "error", error: "disk full", workflow_run: saved });
    expect(adopted).toBe(saved);
    expect(runDisplayStatus(saved)).toBe("persistence_error");
    expect(runLive(adopted)).toBe(false);
    expect(runSuspended(adopted)).toBe(false);
    expect(saved.status).toBe("working");
    expect(runNodeStates(saved, ["a", "h"])).toEqual({ a: "running", h: "waiting_human" });
  });
});

describe("stream frames", () => {
  it("picks the record off structural frames and ignores the rest", () => {
    const rec = runFixture();
    expect(streamEventRun({ type: "workflow_node_end", workflow_run: rec })).toBe(rec);
    expect(streamEventRun({ type: "workflow_node_frame", node_id: "a" })).toBeNull();
    expect(streamEventRun(null)).toBeNull();
    expect(streamEventType({ type: "workflow_dispatch_start" })).toBe("workflow_dispatch_start");
    expect(streamEventType(null)).toBe("");
  });
});

describe("buildFlowNodes", () => {
  const catalog: Record<string, { name: string; color: string }> = {
    ag_1: { name: "分析师", color: "#5965e8" },
  };
  const base = {
    agentOf: (id: string) => catalog[id],
    states: { a: "running" as const },
    issues: { b: ["task_required"] },
    positions: { a: { x: 0, y: 0 }, b: { x: 0, y: 150 } },
    activeId: "b",
    fallbackColor: "#5965e8",
  };

  it("uses neutral read-only nodes for redacted descriptions without relaxing editor validation", () => {
    const redacted = { name: "Published", description: "", nodes: [node("b", { task: "" })] };
    const invalid = validateWorkflow(redacted);
    expect(invalid.nodes.b).toContain("task_required");
    const readonly = buildFlowNodes({ ...base, nodes: redacted.nodes, issues: invalid.nodes, readOnly: true });
    expect(readonly[0]!.data).toMatchObject({ task: "", readOnly: true, issues: [] });
    const editor = buildFlowNodes({ ...base, nodes: redacted.nodes, issues: invalid.nodes, readOnly: false });
    expect(editor[0]!.data.readOnly).toBe(false);
    expect(editor[0]!.data.issues).toContain("task_required");
  });

  it("assembles display data: identity, kind flags, badges, run state", () => {
    const run = runFixture({ nodes: { c: { status: "skipped", skipped_by: "a", branch: "success" } } });
    const out = buildFlowNodes({
      ...base,
      positions: { ...base.positions, c: { x: 0, y: 300 } },
      nodes: [
        node("a"),
        node("b", { agent: "ag_gone", task: "", expect: { path: "b.md" }, approval: true, orchestrate: { candidates: [{ agent: "ag_1" }] } }),
        node("c", { kind: "human", agent: "", task: "", prompt: "审批" }),
      ],
      run,
    });
    expect(out).toHaveLength(3);
    expect(out[0]).toMatchObject({
      id: "a",
      type: "agent",
      position: { x: 0, y: 0 },
      data: { label: "分析师", color: "#5965e8", state: "running", issues: [], missing: false, active: false },
    });
    expect(out[1]!.data).toMatchObject({
      label: "ag_gone",
      expectPath: "b.md",
      orchestrator: true,
      approval: true,
      state: "",
      issues: ["task_required"],
      missing: true,
      active: true,
    });
    expect(out[2]!.data).toMatchObject({
      label: "c",
      kind: "human",
      task: "审批",
      missing: false,
      skippedBy: "a",
      branch: "success",
    });
  });

  it("falls back to a default position and the fallback color", () => {
    const out = buildFlowNodes({ ...base, nodes: [node("z", { agent: "ag_gone" })], positions: {} });
    expect(out[0]!.position).toEqual({ x: 0, y: 0 });
    expect(out[0]!.data.color).toBe("#5965e8");
  });
});
