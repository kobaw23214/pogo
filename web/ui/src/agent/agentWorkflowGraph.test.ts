import { describe, expect, it } from "vitest";
import type { AgentWorkflowDefinition, AgentWorkflowRun } from "@/api/agentWorkflows";
import type { AgentWorkflowNodeActivity } from './agentWorkflowActivity';
import { AGENT_WORKFLOW_INPUT as INPUT, AGENT_WORKFLOW_OUTPUT as OUTPUT, agentWorkflowAfterId, agentWorkflowConnectionMove, agentWorkflowStepDisplayStatus, agentWorkflowStepElapsedMs, buildAgentWorkflowGraph, layoutAgentWorkflow, mergeAgentWorkflowPositions, validWorkflowPosition, validWorkflowViewport, workflowCanvasBoundsViewport, workflowCanvasVisibleRect } from "./agentWorkflowGraph";

describe('canvas workspace visible area', () => {
  it('excludes the floating header, tools, settings and execution drawer', () => {
    expect(workflowCanvasVisibleRect(1200, 800, { top: 88, left: 72, right: 380, bottom: 240 })).toEqual({ x: 72, y: 88, width: 748, height: 472 });
  });
  it('keeps a finite usable area on narrow screens and rejects invalid inset sizes', () => {
    for (const [width, height] of [[320, 240], [40, 20], [0, 0]]) {
      const area = workflowCanvasVisibleRect(width, height, { left: -10, top: NaN, right: 10000, bottom: Infinity });
      const view = workflowCanvasBoundsViewport({ x: -200, y: 90, width: 248, height: 168 }, area, { maxZoom: 0.85 });
      expect(area.width).toBeGreaterThan(0); expect(area.height).toBeGreaterThan(0);
      expect(Object.values(view).every(Number.isFinite)).toBe(true);
      expect(view.zoom).toBeGreaterThanOrEqual(0.1); expect(view.zoom).toBeLessThanOrEqual(0.85);
    }
  });
});

function definition(): AgentWorkflowDefinition {
  return { id: "research", name: "Research report", revision: 2, definition_hash: "hash", inputs: [{ key: "topic", label: "Topic" }], steps: [
    { id: "collect", kind: "agent", title: "Collect", task: "Collect source material." },
    { id: "review", kind: "confirm", title: "Review", prompt: "Confirm the sources." },
    { id: "report", kind: "agent", title: "Report", task: "Write a report.", output_file: "report.md" },
  ] };
}
function run(snapshot: AgentWorkflowDefinition): AgentWorkflowRun {
  return { id: "historical-run", workflow_id: snapshot.id, agent_id: "test-agent", status: "waiting_confirmation", definition: snapshot, steps: [
    { step_id: "collect", status: "completed", started_at: 1000, finished_at: 4000, artifact: "sources.md" },
    { step_id: "review", status: "waiting_confirmation", started_at: 5000 },
  ] };
}

describe("internal workflow canvas projection", () => {
  it('projects per-item completion and failure counts without attributing an Agent session to the container',()=>{
    const def:AgentWorkflowDefinition={id:'flow',name:'Batch',revision:1,definition_hash:'x',steps:[{id:'batch',kind:'iteration'}]};
    const record:AgentWorkflowRun={id:'run',workflow_id:'flow',agent_id:'a',status:'running',definition:def,steps:[{step_id:'batch',status:'running',repeat:{kind:'iteration',total:3,items:[{index:0,status:'completed'},{index:1,status:'failed'},{index:2,status:'running',session_id:'item-session'}]}}]};
    const graph=buildAgentWorkflowGraph({definition:def,run:record});expect(graph.nodes.find(node=>node.id==='batch')?.data).toMatchObject({kind:'iteration',repeatProgress:{completed:1,failed:1,total:3},inspectable:false});
  });
  it('projects actual node activity only onto its matching Agent session and attempt', () => {
    const def = definition(), current = run(def); current.status = 'running';
    current.steps = [{ step_id: 'collect', status: 'running', session_id: 'session-a', active_run_id: 'attempt-a' }];
    const activity: AgentWorkflowNodeActivity = { step_id: 'collect', session_id: 'session-a', active_run_id: 'attempt-a', status: 'running', phase: 'tool', observed: true, turn: 3, tool_name: 'read', tool_count: 2, updated_at: 9000, connected: true };
    const before = JSON.stringify(current);
    const graph = buildAgentWorkflowGraph({ definition: def, run: current, activities: { collect: activity } });
    expect(graph.nodes.find(node => node.id === 'collect')?.data).toMatchObject({ status: 'running', inspectable: true, activity });
    expect(graph.nodes.find(node => node.id === 'review')?.data.inspectable).toBe(false);
    expect(JSON.stringify(current)).toBe(before);
    for (const patch of [{ session_id: 'another-session' }, { active_run_id: 'another-attempt' }, { step_id: 'report' }]) {
      const rejected = buildAgentWorkflowGraph({ definition: def, run: current, activities: { collect: { ...activity, ...patch } } });
      expect(rejected.nodes.find(node => node.id === 'collect')?.data.activity).toBeUndefined();
    }
    expect(buildAgentWorkflowGraph({ definition: def, activities: { collect: activity } }).nodes.every(node => !node.data.activity && !node.data.inspectable)).toBe(true);
  });

  it('keeps completed stored results final when an older activity frame is still in flight', () => {
    const def = definition(), current = run(def); current.status = 'completed';
    const activity: AgentWorkflowNodeActivity = { step_id: 'collect', status: 'running', phase: 'model', observed: true, turn: 4, tool_count: 3, updated_at: 6000 };
    const graph = buildAgentWorkflowGraph({ definition: def, run: current, activities: { collect: activity } });
    expect(graph.nodes.find(node => node.id === 'collect')?.data).toMatchObject({ status: 'completed', activity: { status: 'completed', phase: 'completed', observed: true, turn: 4, tool_count: 3 } });
    expect(activity.phase).toBe('model');
  });
  it("always derives exactly one complete chain and leaves the definition unchanged", () => {
    const def = definition(), before = JSON.stringify(def);
    const graph = buildAgentWorkflowGraph({ definition: def, editable: true, selectedId: "review" });
    expect(graph.nodes.map(node => node.id)).toEqual([INPUT, "collect", "review", "report", OUTPUT]);
    expect(graph.edges.map(edge => [edge.source, edge.target])).toEqual([[INPUT, "collect"], ["collect", "review"], ["review", "report"], ["report", OUTPUT]]);
    expect(graph.nodes.find(node => node.id === "review")).toMatchObject({ selected: true, data: { kind: "confirm", active: true, status: "" } });
    expect(graph.edges.every(edge => !edge.deletable && !edge.updatable)).toBe(true);
    expect(JSON.stringify(def)).toBe(before);
  });

  it("keeps saved positions, rejects invalid coordinates, and discards removed-node coordinates", () => {
    const def = definition();
    const positions = mergeAgentWorkflowPositions(def, { collect: { x: 25, y: 40 }, review: { x: NaN, y: 0 }, removed: { x: 500, y: 100 } });
    expect(positions.collect).toEqual({ x: 25, y: 40 });
    expect(positions.review).toEqual(layoutAgentWorkflow(def).review);
    expect(positions.removed).toBeUndefined();
    expect(Object.values(positions).every(validWorkflowPosition)).toBe(true);
  });

  it("inserts a new node without moving or overlapping existing user positions", () => {
    const def = definition(), saved = layoutAgentWorkflow(def);
    def.steps.splice(1, 0, { id: "outline", kind: "agent", task: "Create an outline." });
    const next = mergeAgentWorkflowPositions(def, saved);
    for (const [id, position] of Object.entries(saved)) expect(next[id]).toEqual(position);
    expect(next.outline).toEqual({ x: saved.review.x, y: saved.review.y + 210 });
  });

  it("renders the frozen run graph and status, never the current draft graph or positions", () => {
    const snapshot = definition(), current = definition(), historical = run(snapshot);
    current.steps.push({ id: "newStep", kind: "agent", task: "Added after this run." });
    const graph = buildAgentWorkflowGraph({ definition: current, run: historical, positions: { collect: { x: 9000, y: 9000 } }, updatedIds: ['collect', 'newStep'], editable: true, now: 8000 });
    expect(graph.nodes.map(node => node.id)).not.toContain("newStep");
    expect(graph.nodes.find(node => node.id === "collect")).toMatchObject({ position: layoutAgentWorkflow(snapshot).collect, data: { status: "completed", elapsedMs: 3000 } });
    expect(graph.nodes.find(node => node.id === "review")?.data).toMatchObject({ status: "waiting_confirmation", elapsedMs: 3000 });
    expect(graph.nodes.find(node => node.id === "report")?.data.status).toBe("pending");
    expect(graph.nodes.find(node => node.id === OUTPUT)?.data.status).toBe("pending");
    expect(graph.nodes.every(node => !node.draggable && !node.connectable && !node.data.editable)).toBe(true);
    expect(graph.nodes.every(node => !node.data.updated)).toBe(true);
  });

  it("only marks the output completed when the run completes", () => {
    const def = definition(), history = run(def);
    history.status = "failed";
    expect(buildAgentWorkflowGraph({ definition: def, run: history }).nodes[4].data.status).toBe("skipped");
    history.status = "completed";
    history.steps?.push({ step_id: "report", status: "completed", artifact: "result/final-report.md" });
    expect(buildAgentWorkflowGraph({ definition: def, run: history }).nodes[4].data).toMatchObject({ status: "completed", outputFile: "result/final-report.md" });
  });

  it.each(["cancelled", "failed", "interrupted"])("projects a %s run without changing its preserved step records", status => {
    const def = definition(), history = run(def);
    history.status = status; history.current_step = 1; history.updated_at = 9000;
    history.steps?.push({ step_id: "report", status: "queued" });
    const before = JSON.stringify(history);
    const graph = buildAgentWorkflowGraph({ definition: def, run: history, now: 100000 });
    expect(graph.nodes.map(node => node.data.status)).toEqual(["completed", "completed", status, "skipped", "skipped"]);
    expect(graph.nodes[2].data.elapsedMs).toBe(4000);
    expect(graph.edges.every(edge => !edge.animated)).toBe(true);
    expect(JSON.stringify(history)).toBe(before);
  });

  it("ends a current queued step and a previously started step, preserving terminal results", () => {
    const history = run(definition());
    history.status = "cancelled"; history.current_step = 1;
    history.steps = [{ step_id: "collect", status: "running", started_at: 1000 }, { step_id: "review", status: "queued" }, { step_id: "report", status: "failed", finished_at: 5000 }];
    expect(agentWorkflowStepDisplayStatus(history, "collect")).toBe("cancelled");
    expect(agentWorkflowStepDisplayStatus(history, "review")).toBe("cancelled");
    expect(agentWorkflowStepDisplayStatus(history, "report")).toBe("failed");
  });

  it("retains live confirmation/queue states while the run is active", () => {
    const history = run(definition()); history.current_step = 1;
    history.steps?.push({ step_id: "report", status: "queued" });
    expect(agentWorkflowStepDisplayStatus(history, "review")).toBe("waiting_confirmation");
    expect(agentWorkflowStepDisplayStatus(history, "report")).toBe("queued");
    expect(agentWorkflowStepDisplayStatus(null, "review")).toBe("");
  });

  it("caps an unfinished step's displayed elapsed time at a terminal run update", () => {
    const history = run(definition()); history.status = "interrupted"; history.updated_at = 9000;
    expect(agentWorkflowStepElapsedMs(history, "collect", 100000)).toBe(3000);
    expect(agentWorkflowStepElapsedMs(history, "review", 100000)).toBe(4000);
    expect(agentWorkflowStepElapsedMs(history, "review", 200000)).toBe(4000);
    delete history.updated_at;
    expect(agentWorkflowStepElapsedMs(history, "review", 200000)).toBeUndefined();
    expect(agentWorkflowStepElapsedMs(history, "report", 200000)).toBeUndefined();
  });
});

describe("single-chain connection gestures", () => {
  it.each([
    [INPUT, "report", "report", 0],
    ["collect", OUTPUT, "collect", 2],
    ["collect", "report", "report", 1],
    ["report", "collect", "collect", 2],
    ["review", "collect", "collect", 1],
  ])("interprets %s -> %s as one stable-ID move", (source, target, stepId, index) => {
    const def = definition(), before = JSON.stringify(def);
    expect(agentWorkflowConnectionMove(def, source as string, target as string)).toEqual({ ok: true, stepId, index });
    expect(JSON.stringify(def)).toBe(before);
  });

  it.each([
    ["collect", "collect", "self"],
    [OUTPUT, "collect", "boundary"],
    ["report", INPUT, "boundary"],
    [INPUT, OUTPUT, "boundary"],
    ["missing", "collect", "unknown"],
    ["collect", "missing", "unknown"],
    [INPUT, "collect", "unchanged"],
    ["collect", "review", "unchanged"],
    ["report", OUTPUT, "unchanged"],
  ])("does not create an invalid or duplicate edge for %s -> %s", (source, target, reason) => {
    expect(agentWorkflowConnectionMove(definition(), source, target)).toEqual({ ok: false, reason });
  });

  it("maps insert buttons to an existing step or before the first step", () => {
    expect(agentWorkflowAfterId(definition(), INPUT)).toBeNull();
    expect(agentWorkflowAfterId(definition(), "review")).toBe("review");
    expect(agentWorkflowAfterId(definition(), OUTPUT)).toBeUndefined();
    expect(agentWorkflowAfterId(definition(), "missing")).toBeUndefined();
  });

  it("only restores finite, bounded viewports", () => {
    expect(validWorkflowViewport({ x: -80, y: 10, zoom: 0.8 })).toBe(true);
    expect(validWorkflowViewport({ x: Infinity, y: 0, zoom: 1 })).toBe(false);
    expect(validWorkflowViewport({ x: 0, y: 0, zoom: 0 })).toBe(false);
    expect(validWorkflowViewport({ x: 0, y: 0, zoom: 5 })).toBe(false);
    expect(validWorkflowViewport(null)).toBe(false);
  });
});
