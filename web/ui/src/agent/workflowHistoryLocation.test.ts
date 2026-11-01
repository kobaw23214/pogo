import { describe, expect, it } from "vitest";
import { workflowHistoryLocation } from "./workflowHistoryLocation";

describe("workflow run deep links", () => {
  it("reloads the retry result instead of the failed run while preserving authentication", () => {
    const failed = "http://127.0.0.1:17891/manage/workflows/wf_review?run=run_failed&token=alice%2Bfixture&theme=paper#result";
    const current = new URL(workflowHistoryLocation(failed, "run_retry"));
    expect(current.searchParams.get("run")).toBe("run_retry");
    expect(current.searchParams.get("token")).toBe("alice+fixture");
    expect(current.searchParams.get("theme")).toBe("paper");
    expect(current.pathname).toBe("/manage/workflows/wf_review");
    expect(current.hash).toBe("#result");
  });

  it("links a newly started run from the draft or a previous history selection", () => {
    const page = "https://pogo.example/manage/workflows/wf_review?token=fixture";
    expect(new URL(workflowHistoryLocation(page, "run_first")).searchParams.get("run")).toBe("run_first");
    expect(new URL(workflowHistoryLocation(`${page}&run=run_old`, "run_second")).searchParams.getAll("run")).toEqual(["run_second"]);
  });

  it("returns to the draft without discarding other query parameters", () => {
    const current = new URL(workflowHistoryLocation("https://pogo.example/manage/workflows/wf_review?token=fixture&run=run_old&theme=paper", null));
    expect(current.searchParams.has("run")).toBe(false);
    expect(current.searchParams.get("token")).toBe("fixture");
    expect(current.searchParams.get("theme")).toBe("paper");
  });
});
