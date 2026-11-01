import { describe, expect, it } from "vitest";
import {
  STUDIO_LAST_SESSION,
  chooseBootSession,
  requestedSessionId,
  studioKey,
  studioPath,
  studioStarters,
} from "./studioContext";
import type { PlatformAgent } from "./platformTypes";
import type { SessionInfo } from "@/api/types";

const sess = (id: string): SessionInfo => ({ path: `/x/${id}.jsonl`, id, title: id, messages: 0 });

const AGENT: PlatformAgent = {
  id: "a1",
  name: "写作助手",
  description: "帮你把想法写成文章",
  instructions: "…",
  model: "gpt-5",
  color: "#5965e8",
  icon: "",
  category: "写作",
  starters: ["  帮我拟三个标题  ", "", "把这段文字改得更简洁", "检查逻辑与语气", "第四条不应被采用"],
  status: "ready",
  revision: 1,
  created_at: "",
  updated_at: "",
  conversation_count: 0,
};

describe("studioKey", () => {
  it("namespaces per Agent and strips the pogo: prefix", () => {
    expect(studioKey("a1", "pogo:hub-tabs")).toBe("pogo:studio:a1:hub-tabs");
    expect(studioKey("a2", "pogo:hub-tabs")).toBe("pogo:studio:a2:hub-tabs");
    expect(studioKey("a1", STUDIO_LAST_SESSION)).toBe("pogo:studio:a1:last-session");
  });
});

describe("requestedSessionId", () => {
  it("reads the c= deep link and rejects empty values", () => {
    expect(requestedSessionId("?c=s-1")).toBe("s-1");
    expect(requestedSessionId("?c=")).toBeNull();
    expect(requestedSessionId("")).toBeNull();
    expect(requestedSessionId("?x=1")).toBeNull();
  });
});

describe("studioPath", () => {
  it("keeps /a/{id} bare without a session and carries ?c= with one", () => {
    expect(studioPath("a1")).toBe("/a/a1");
    expect(studioPath("a1", null)).toBe("/a/a1");
    expect(studioPath("a1", "s-1")).toBe("/a/a1?c=s-1");
  });

  it("keeps the internal preview route across session changes", () => {
    expect(studioPath("a1", "s-1", { managedChat: true })).toBe("/manage/agents/a1/chat?c=s-1");
    expect(studioPath("a1", null, { managedChat: true })).toBe("/manage/agents/a1/chat");
  });

  it("retains iframe mode after selecting and clearing a conversation", () => {
    expect(studioPath("a1", "s-1", { embedded: true })).toBe("/a/a1?embed=1&c=s-1");
    expect(studioPath("a1", null, { embedded: true })).toBe("/a/a1?embed=1");
    expect(studioPath("a1", "s-1", { managedChat: true, embedded: true })).toBe("/manage/agents/a1/chat?c=s-1");
  });
});

describe("chooseBootSession", () => {
  const list = [sess("s-1"), sess("s-2")];

  it("prefers the deep link, then this Agent's memory, then the first row", () => {
    expect(chooseBootSession(list, "s-2", "s-1")).toBe("s-2");
    expect(chooseBootSession(list, null, "s-2")).toBe("s-2");
    expect(chooseBootSession(list, null, null)).toBe("s-1");
  });

  it("never adopts a session that is not in THIS Agent's list", () => {
    // Another Agent's remembered session or a forged deep link falls through.
    expect(chooseBootSession(list, "other-agent-session", "also-foreign")).toBe("s-1");
    expect(chooseBootSession(list, null, "other-agent-session")).toBe("s-1");
  });

  it("returns null on an empty list so the caller creates a session", () => {
    expect(chooseBootSession([], "x", "y")).toBeNull();
  });
});

describe("studioStarters", () => {
  it("maps the definition's starters to trimmed prompt cards", () => {
    const set = studioStarters(AGENT);
    expect(set.heading).toBe("帮你把想法写成文章");
    expect(set.send).toBe(false);
    expect(set.cards.map((c) => c.prompt)).toEqual([
      "帮我拟三个标题",
      "把这段文字改得更简洁",
      "检查逻辑与语气",
      "第四条不应被采用",
    ]);
    expect(set.cards.every((c) => c.title === c.prompt && !c.panel)).toBe(true);
  });

  it("keeps a card entry's title, label and icon", () => {
    const set = studioStarters({
      ...AGENT,
      starters: [{ icon: "graph", title: "操作样本库", label: "/skill:browser-use", prompt: "打开样本库并筛出零检出" }],
    });
    expect(set.cards).toEqual([
      { icon: "graph", title: "操作样本库", label: "/skill:browser-use", prompt: "打开样本库并筛出零检出" },
    ]);
  });

  it("falls back to the Agent's name when there is no description", () => {
    expect(studioStarters({ ...AGENT, description: "", starters: [] }).heading).toBe("写作助手");
  });

  it("keeps cards beyond the first page and their mixed ordered content", () => {
    const parts = [{ type: "text" as const, text: "读取" }, { type: "file" as const, path: "starter-assets/a/模板.xlsx", as_file: true }];
    const starters = Array.from({ length: 25 }, (_, i) => ({ title: `步骤 ${i + 1}`, prompt: `执行 ${i + 1}`, ...(i === 24 ? { parts, input_layout: "inline" as const } : {}) }));
    const set = studioStarters({ ...AGENT, starters });
    expect(set.cards).toHaveLength(25);
    expect(set.cards[24]).toMatchObject({ title: "步骤 25", parts, input_layout: "inline" });
    expect(set.cards[24].parts).not.toBe(parts);
  });

  it("retains attachment-only cards instead of silently dropping them", () => {
    const parts = [{ type: "file" as const, path: "starter-assets/a/模板.xlsx" }];
    expect(studioStarters({ ...AGENT, starters: [{ title: "模板", prompt: "", parts }] }).cards).toHaveLength(1);
  });
});
