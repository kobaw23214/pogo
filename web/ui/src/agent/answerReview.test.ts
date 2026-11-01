import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AnswerReview, type AgentEvent } from "@/api/types";
import { useAgentStream } from "./useAgentStream";
import { buildTimeline } from "./timeline";
import { answerReviewNoticeText, showAnswerReviewNotice } from "./answerReview";
import { setLocale } from "@/i18n";

const running: AnswerReview = { kind: "citation", state: "running", turn: 2, message_id: "m2", started_at: 1000 };

describe("visible answer review lifecycle", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => { vi.useFakeTimers(); setLocale("zh-CN"); });
  afterEach(() => { for (const stream of streams.splice(0)) stream.disconnect(); vi.clearAllTimers(); vi.useRealTimers(); });
  function create() { const stream = useAgentStream({ attachments: false }); streams.push(stream); return stream; }

  it("shows the real running stage before first token and retains every thinking fragment through commit", () => {
    const stream = create();
    const feed = (e: AgentEvent) => stream.apply(e);
    feed({ type: "run_start", seq: 1, ts: 1000, run_id: "run" });
    feed({ type: "turn_start", seq: 2, ts: 1001, turn: 2, message_id: "m2" });
    feed({ type: "answer_review", seq: 3, ts: 1002, answer_review: running });
    let turns = buildTimeline(stream.messages.value, stream.results.value, stream.live.value, {}, stream.answerReview.value).filter(item => item.kind === "turn");
    expect(turns).toHaveLength(1); expect(turns[0]!.answerReview).toEqual(running);
    expect(turns[0]!.thinking).toBe("");
    feed({ type: "thinking", seq: 4, ts: 1003, text: "检查第一条依据。" });
    feed({ type: "thinking", seq: 5, ts: 1004, text: "继续检查第二条依据。" });
    expect(stream.live.value.thinking).toBe("检查第一条依据。继续检查第二条依据。");
    const completed: AnswerReview = { ...running, state: "completed", finished_at: 5000, duration_ms: 4000 };
    feed({ type: "answer_review", seq: 6, ts: 5000, answer_review: completed });
    feed({ type: "message", seq: 7, ts: 5001, role: "assistant", message_id: "m2", content: [{ type: "thinking", text: stream.live.value.thinking! }, { type: "text", text: "完整的引用回答。" }], answer_review: completed });
    turns = buildTimeline(stream.messages.value, stream.results.value, stream.live.value, {}, stream.answerReview.value).filter(item => item.kind === "turn");
    expect(turns).toHaveLength(1); expect(turns[0]!.answerReview?.state).toBe("completed");
    expect(turns[0]!.thinking).toBe("检查第一条依据。继续检查第二条依据。");
    feed({ type: "run_end", seq: 8, ts: 5002 });
    expect(stream.answerReview.value?.state).toBe("completed");
  });

  it("restores running state and full thinking from a reconnect snapshot, then shows timeout fallback", () => {
    const stream = create();
    stream.apply({ type: "snapshot", seq: 1, ts: 2000, snapshot: { seq: 1, messages: [], results: {}, run: { active: true }, policy: { mode: "standard" }, usage: { input: 10, output: 0 }, context_tokens: 10,
      live: { ...emptyLive(), active: true, turn: 2, message_id: "m2", thinking: "已收到的全部思考。" }, answer_review: running } });
    expect(stream.answerReview.value).toEqual(running); expect(stream.live.value.thinking).toBe("已收到的全部思考。");
    stream.apply({ type: "answer_review", seq: 2, ts: 31000, answer_review: { ...running, state: "fallback", reason: "timeout", duration_ms: 30000 } });
    expect(stream.answerReview.value).toMatchObject({ state: "fallback", reason: "timeout" });
    stream.apply({ type: "run_end", seq: 3, ts: 31001 });
    expect(stream.answerReview.value?.state).toBe("fallback");
    stream.apply({ type: "run_start", seq: 4, ts: 32000, run_id: "next" });
    expect(stream.answerReview.value).toBeNull();
  });

  it("never leaves running state on cancelled/ended runs and accepts legacy snapshots without review metadata", () => {
    const stream = create();
    stream.apply({ type: "answer_review", seq: 1, ts: 1000, answer_review: running });
    stream.apply({ type: "run_end", seq: 2, ts: 3000, stop_reason: "aborted" });
    expect(stream.answerReview.value).toMatchObject({ state: "cancelled", duration_ms: 2000 });
    const terminal = buildTimeline(stream.messages.value, stream.results.value, stream.live.value, {}, stream.answerReview.value);
    expect(terminal).toHaveLength(1);
    expect(terminal[0]).toMatchObject({ kind: "turn", id: "m2", thinking: "", text: "", streaming: false, answerReview: { state: "cancelled" } });
    stream.apply({ type: "snapshot", seq: 3, ts: 4000, snapshot: { seq: 3, messages: [], results: {}, live: emptyLive(), run: { active: false }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0 } });
    expect(stream.answerReview.value).toBeNull();
  });

  it("preserves per-turn settled review state in history and localizes historical internal notices without faking activity", () => {
    const settled: AnswerReview = { ...running, state: "fallback", reason: "timeout", duration_ms: 30000 };
    const items = buildTimeline([{ id: "m2", role: "assistant", content: [{ type: "text", text: "保留的原回答" }], answer_review: settled }], {}, emptyLive());
    expect(items[0]).toMatchObject({ kind: "turn", answerReview: settled, streaming: false });
    const text = "[pogo] citation check: this task successfully read source material";
    expect(answerReviewNoticeText(text)).toBe("系统曾请求补充正文引用。");
    expect(showAnswerReviewNotice(text, [{ answerReview: settled }])).toBe(false);
    expect(showAnswerReviewNotice(text, [])).toBe(true);
    expect(answerReviewNoticeText("[pogo] verbatim quotation check: missing quote")).toBe("系统曾请求核对引文。");
  });
});
