import { describe, expect, it } from "vitest";
import { taskTodoCounts } from "./taskTodoSummary";

describe("task todo summary", () => {
  it("reports blocked and cancelled separately from completed", () => {
    expect(taskTodoCounts([
      { task: "done", status: "completed" }, { task: "blocked", status: "blocked" }, { task: "cancelled", status: "cancelled" },
      { task: "next", status: "pending" }, { task: "now", status: "in_progress" },
    ])).toEqual({ total: 5, completed: 1, blocked: 1, cancelled: 1, pending: 2 });
  });
  it("represents a cleared list without pretending it was completed", () => {
    expect(taskTodoCounts([])).toEqual({ total: 0, completed: 0, blocked: 0, cancelled: 0, pending: 0 });
  });
});
