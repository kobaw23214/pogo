import { describe, expect, it } from "vitest";
import { isWorkflowAuthoringDetails, workflowInputKeys, workflowReceiptLocation } from "./workflowAuthoringReceipt";
describe("workflow authoring receipts", () => {
  it("accepts only host workflow receipt shapes", () => {
    expect(isWorkflowAuthoringDetails({ type: "workflow_authoring", action: "edit", result: { ok: true } })).toBe(true);
    expect(isWorkflowAuthoringDetails({ type: "workflow_authoring", action: "approve", result: {} })).toBe(false);
    expect(isWorkflowAuthoringDetails({ type: "workflow_authoring", action: "read", result: [] })).toBe(false);
  });
  it("constructs local navigation from verified IDs and never trusts returned URLs", () => {
    expect(workflowReceiptLocation({ type: "workflow_authoring", action: "test", result: { workflow_id: "wf_demo", id: "run_test", url: "https://example.invalid" } })).toBe("/manage/workflows/wf_demo?run=run_test");
    expect(workflowReceiptLocation({ type: "workflow_authoring", action: "edit", result: { workflow: { id: "../../admin" } } })).toBeNull();
  });
  it("collects only supported external input references across prompts and tasks", () => {
    expect(workflowInputKeys([{ task: "{{ inputs.order_id }} {{inputs.amount}} {{nodes.a.summary}}" }, { prompt: "{{inputs.order_id}} {{ inputs.bad.field }}" }])).toEqual(["amount", "order_id"]);
  });
});
