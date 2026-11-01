import { describe, expect, it } from "vitest";
import type { MessageInputPart, OfficeReferenceSnapshot } from "@/api/types";
import { inputPartsBlocks } from "./orderedInput";
import { conversationPlanDraft, messagePlanDraft } from "./scheduleConversation";

const office: OfficeReferenceSnapshot = { path: "uploads/report.xlsx", title: "report", editor: "cell", text: "A1: 10", sheet: "Data", range: "A1", canvas_version: "v1" };

describe("conversation to reviewed schedule draft", () => {
  it("preserves repeated references, nested shortcuts, skills, Office and text in their original order", () => {
    const parts: MessageInputPart[] = [
      { type: "text", text: "Read " }, { type: "file", path: "uploads/report.xlsx", as_file: true },
      { type: "text", text: " then " }, { type: "shortcut", shortcut_id: "a", title: "Check", text: "", parts: [{ type: "skill", name: "office" }, { type: "office", office }] },
      { type: "text", text: " compare " }, { type: "file", path: "uploads/report.xlsx", as_file: true },
    ];
    const plan = messagePlanDraft({ id: "m1", text: "display text", orderedContent: inputPartsBlocks(parts), inputLayout: "inline" }, "session-one", "agent-one", "draft-one");
    expect(plan.parts).toEqual(parts);
    expect(plan).toMatchObject({ agent_id: "agent-one", source_session_id: "session-one", input_layout: "inline", key: "draft-one" });
    parts[3] = { type: "text", text: "changed later" };
    expect(plan.parts[3].type).toBe("shortcut");
  });

  it("converts a legacy skill invocation into a skill card instead of persisting expanded skill instructions", () => {
    const plan = messagePlanDraft({ id: "m1", text: "/skill:office Summarize this file", files: [{ path: "uploads/report.xlsx", name: "report.xlsx" }] }, "sid", "agent", "key");
    expect(plan.parts.map(part => part.type)).toEqual(["file", "skill", "text"]);
    expect(plan.parts[1]).toEqual({ type: "skill", name: "office" });
    expect(plan.parts[2]).toEqual({ type: "text", text: "Summarize this file" });
    expect(plan.input_layout).toBe("top");
  });

  it("preserves an image as an image and leaves both the source draft and source Office snapshot unchanged", () => {
    const image = messagePlanDraft({ id: "m", text: "Inspect", images: ["uploads/image.png"] }, "sid", "agent", "key");
    expect(image.parts[0]).toEqual({ type: "file", path: "uploads/image.png" });
    const original: MessageInputPart[] = [{ type: "office", office }];
    const plan = conversationPlanDraft(original, "sid", "agent", "inline", "key");
    if (plan.parts[0].type === "office") plan.parts[0].office.text = "edited plan";
    expect(office.text).toBe("A1: 10");
  });
});
