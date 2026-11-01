import { describe, expect, it } from "vitest";
import type { Block, Message } from "./types";
import { normalizeMessageContent, normalizeMessages } from "./messageContent";

describe("message content wire compatibility", () => {
  it("accepts null, absent, legacy string and single-block contents", () => {
    for (const content of [undefined, null, 1, false, {}]) expect(normalizeMessageContent(content)).toEqual([]);
    expect(normalizeMessageContent(null, "old event text")).toEqual([{ type: "text", text: "old event text" }]);
    expect(normalizeMessageContent("正文")).toEqual([{ type: "text", text: "正文" }]);
    expect(normalizeMessageContent({ type: "text", text: "单块" })).toEqual([{ type: "text", text: "单块" }]);
    expect(normalizeMessageContent([], "do not replace an intentional empty array")).toEqual([]);
  });

  it("does not coerce malformed data to prose or lose neighbouring valid blocks", () => {
    const text: Block = { type: "text", text: "证据 [1]" };
    const tool: Block = { type: "tool_use", id: "c1", name: "browser", input: "malformed arguments", input_invalid: true };
    const content = [null, 42, "not a block", {}, { type: "future_type" }, { type: "text", text: {} }, text, tool];
    expect(normalizeMessageContent(content)).toEqual([text, tool]);
    expect(normalizeMessageContent(content)[1]).toBe(tool);
    expect(normalizeMessageContent({ type: "thinking", text: null })).toEqual([{ type: "thinking", text: "" }]);
    expect(content).toHaveLength(8);
  });

  it("retains valid message and block identities and all provenance fields", () => {
    const blocks: Block[] = [
      { type: "text", text: "用户选择", input_office: { path: "a.xlsx", title: "a", editor: "cell", text: "A1", canvas_version: "v1" } },
      { type: "thinking", text: "reasoning", duration_ms: 100 },
      { type: "tool_use", id: "c1", name: "read", input: { path: "a.txt" } },
      { type: "image", image_ref: "uploads/a.png", image_alt: "attachment" },
      { type: "file", file_path: "a.txt", file_name: "a.txt" },
      { type: "tool_result", tool_use_id: "c1", text: "legacy result" },
    ];
    const message: Message = { id: "u1", role: "user", content: blocks, inbox_id: "inbox", run_id: "run", steering: true, input_layout: "inline", ts: 123 };
    const messages = [message];
    expect(normalizeMessageContent(blocks)).toBe(blocks);
    expect(normalizeMessages(messages)).toBe(messages);
    expect(normalizeMessages(messages)[0]).toBe(message);
  });

  it("repairs only display copies and retains empty messages and stable IDs", () => {
    const empty = { id: "m2", role: "", content: null, ts: 2 };
    const legacy = { id: "u3", role: "user", content: "continue", inbox_id: "q1", input_layout: "inline" };
    const result = normalizeMessages([null, false, { role: "assistant" }, empty, legacy]);
    expect(result).toEqual([
      { ...empty, role: "assistant", content: [] },
      { ...legacy, content: [{ type: "text", text: "continue" }] },
    ]);
    expect(empty.content).toBeNull();
    expect(legacy.content).toBe("continue");
    expect(normalizeMessages(null)).toEqual([]);
    expect(normalizeMessages({})).toEqual([]);
  });
});
