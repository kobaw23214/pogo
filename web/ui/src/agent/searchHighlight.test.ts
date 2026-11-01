import { describe, expect, it, vi } from "vitest";
import type { Message } from "@/api/types";
import { applySearchHighlight, findAnchorMessage, findAnchorMessages, firstHighlightRangeIn, messageText } from "./searchHighlight";

const msg = (id: string, ...texts: string[]): Message => ({
  id,
  role: texts.length % 2 ? "user" : "assistant",
  content: texts.map((text) => ({ type: "text" as const, text })),
});

describe("messageText", () => {
  it("joins text blocks and skips the rest, mirroring llm.Message.Text()", () => {
    const m: Message = {
      id: "m1",
      role: "assistant",
      content: [
        { type: "text", text: "先说一半" },
        { type: "thinking", text: "内心独白不算" },
        { type: "text", text: "再说一半" },
      ],
    };
    expect(messageText(m)).toBe("先说一半再说一半");
  });
});

describe("findAnchorMessage", () => {
  it("returns the first message holding the anchor verbatim", () => {
    const messages = [msg("u1", "无关问题"), msg("m2", "答案提到 beacon 端点"), msg("m4", "beacon 又一次")];
    expect(findAnchorMessage(messages, "beacon 端点")).toBe("m2");
  });

  it("returns empty when nothing holds it, and for an empty anchor", () => {
    const messages = [msg("u1", "无关问题")];
    expect(findAnchorMessage(messages, "缺席")).toBe("");
    expect(findAnchorMessage(messages, "")).toBe("");
  });
});

describe("findAnchorMessages", () => {
  it("returns every message holding the anchor, in conversation order", () => {
    const messages = [
      msg("u1", "无关问题"),
      msg("m2", "先说一遍 beacon 端点"),
      msg("m4", "结论里重复 beacon 端点"),
      msg("m5", "无关回答"),
    ];
    expect(findAnchorMessages(messages, "beacon 端点")).toEqual(["m2", "m4"]);
    expect(findAnchorMessages(messages, "缺席")).toEqual([]);
    expect(findAnchorMessages(messages, "")).toEqual([]);
  });
});

describe("applySearchHighlight", () => {
  it("is a no-op rather than an error where CSS.highlights does not exist", () => {
    // The test runner has no HighlightRegistry; the feature detection must
    // swallow that, DOM or no DOM.
    expect(() => applySearchHighlight(null, "quokka")).not.toThrow();
  });

  it("firstHighlightRangeIn reports nothing where no registry exists", () => {
    // No DOM in the runner either: the registry check comes first, so the
    // element is never touched — a stand-in is enough.
    expect(firstHighlightRangeIn({} as HTMLElement)).toBeNull();
  });

  it("locates visible prose instead of an identical hit in a folded tool", () => {
    class FakeRange {
      commonAncestorContainer = {};
      constructor(private visible: boolean) {}
      getClientRects() { return this.visible ? [{ width: 40, height: 16 }] : []; }
    }
    const hidden = new FakeRange(false);
    const visible = new FakeRange(true);
    vi.stubGlobal("Range", FakeRange);
    vi.stubGlobal("CSS", { highlights: new Map([["search", [hidden, visible]]]) });
    try {
      expect(firstHighlightRangeIn({ contains: () => true } as unknown as HTMLElement)).toBe(visible);
    } finally { vi.unstubAllGlobals(); }
  });
});
