import { describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import { liveActivity } from "./liveActivity";
import { emptyLive } from "@/api/types";

// The asserted copy is Chinese: pin the locale.
setLocale("zh-CN");

describe("liveActivity", () => {
  it("prioritises a waiting approval over everything else", () => {
    const live = {
      ...emptyLive(),
      pending_gates: [{ gate_id: "g1", call_id: "c1", tool: "bash" as const, deadline: 0 }],
      pending_tools: [{ call_id: "c1", name: "bash" as const, started_at: 0, args: { command: "rm -rf x" } }],
      thinking: "在想",
    };
    expect(liveActivity(live)).toBe("等待你的审批");
  });

  it("names the running tool with its arguments, the identifier not the bare name", () => {
    const live = {
      ...emptyLive(),
      pending_tools: [{ call_id: "c1", name: "bash" as const, started_at: 0, args: { command: "go build ./..." } }],
    };
    expect(liveActivity(live)).toBe("正在执行 go build ./...");

    // bash without a command has no identifier to show — the bare fallback.
    const bare = { ...emptyLive(), pending_tools: [{ call_id: "c2", name: "bash" as const, started_at: 0 }] };
    expect(liveActivity(bare)).toBe("正在调用 bash");
  });

  it("reads an incoming call before any stream text", () => {
    const live = { ...emptyLive(), incoming: [{ call_id: "c9", name: "write", bytes: 3 }] };
    expect(liveActivity(live)).toBe("正在准备 write 调用");
    const anon = { ...emptyLive(), incoming: [{ call_id: "c9", name: "", bytes: 0 }] };
    expect(liveActivity(anon)).toBe("正在生成调用参数");
  });

  it("falls back through thinking, answering, waiting", () => {
    expect(liveActivity({ ...emptyLive(), thinking: "嗯" })).toBe("思考中…");
    expect(liveActivity({ ...emptyLive(), text: "答" })).toBe("正在回复…");
    expect(liveActivity({ ...emptyLive(), thinking: "先核对", text: "已开始输出" })).toBe("正在回复…");
    expect(liveActivity(emptyLive())).toBe("等待模型响应…");
  });
});
