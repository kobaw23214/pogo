import { describe, expect, it, vi } from "vitest";
import { prepareWorkflowRun, saveWorkflowDraft } from "./workflowEditorSave";
import { toWorkflowInput, workflowDirty } from "./workflowPresentation";
import type { Workflow, WorkflowInput } from "./workflowTypes";

describe("workflow editor acknowledgement and publication order", () => {
  it("keeps edits made during an outstanding save dirty, including form options", async () => {
    const draft = {
      name: "Review", description: "", nodes: [{ id: "review", kind: "human" as const, agent: "", task: "", fields: [{ key: "answer", type: "select" as const, options: ["yes"] }] }],
    };
    let resolve!: (workflow: Workflow) => void;
    let sent!: WorkflowInput;
    const update = vi.fn((_id: string, input: WorkflowInput) => {
      sent = input;
      return new Promise<Workflow>(done => { resolve = done; });
    });
    const saving = saveWorkflowDraft({ id: "wf", revision: 1 }, draft, update);
    draft.name = "Newer edit";
    draft.nodes[0]!.fields[0]!.options.push("no");
    resolve({ id: "wf", revision: 2, ...sent, nodes: draft.nodes, published: null, dirty: true, created_at: "", updated_at: "" });
    const result = await saving;
    expect(result.savedInput.name).toBe("Review");
    expect(result.savedInput.nodes[0]!.fields?.[0]?.options).toEqual(["yes"]);
    expect(workflowDirty(result.savedInput, toWorkflowInput(draft))).toBe(true);
    expect(update).toHaveBeenCalledWith("wf", result.savedInput, 1);
  });

  it("requires publication when saving an immediate edit makes the pointer stale", async () => {
    let dirty = false;
    const decision = await prepareWorkflowRun(async () => { dirty = true; return true; }, () => dirty);
    expect(decision).toBe("publish");
  });

  it("does not start or publish after a failed save", async () => {
    const check = vi.fn(() => false);
    expect(await prepareWorkflowRun(async () => false, check)).toBe("blocked");
    expect(check).not.toHaveBeenCalled();
  });
});
