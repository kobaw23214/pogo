import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import { emptyLive, type PendingUserInput } from "@/api/types";
import { useAgentStream } from "./useAgentStream";
import { reduceTaskSessionEvent } from "./taskSessionSnapshot";
import { i18n, setLocale } from "@/i18n";
import UserInputBar from "@/components/UserInputBar.vue";
import UserInputResult from "@/components/tools/UserInputResult.vue";

setLocale("zh-CN");
const request: PendingUserInput = {
  request_id: "q-async", call_id: "call-async", run_id: "run-original", revision: 1, deadline: 0, async: true,
  questions: [{ id: "choice", header: "偏好", question: "方便时选择一种方案", multi_select: false, options: [{ label: "A", description: "局部调整" }, { label: "B", description: "完整迁移" }] }],
};
const ack = { request_id: request.request_id, call_id: request.call_id, status: "pending", async: true, questions: request.questions };
const answered = { ...ack, status: "answered", answers: { choice: ["A"] } };

describe("asynchronous user input lifecycle", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  afterEach(() => { for (const stream of streams.splice(0)) stream.disconnect(); vi.clearAllTimers(); });

  it("keeps optional questions across run boundaries and resolves their history", () => {
    vi.useFakeTimers();
    const stream = useAgentStream();
    streams.push(stream);
    stream.apply({ type: "tool_start", seq: 1, ts: 1, call_id: request.call_id, name: "request_user_input_async" });
    stream.apply({ type: "user_input_request", seq: 2, ts: 2, user_input: request });
    stream.apply({ type: "tool_end", seq: 3, ts: 3, call_id: request.call_id, name: "request_user_input_async", text: "accepted", details: ack });
    stream.apply({ type: "run_end", seq: 4, ts: 4 });
    expect(stream.run.value.active).toBe(false);
    expect(stream.live.value.pending_user_inputs).toEqual([request]);
    stream.apply({ type: "run_start", seq: 5, ts: 5, run_id: "other-run" });
    expect(stream.live.value.pending_user_inputs).toEqual([request]);
    stream.apply({ type: "user_input_resolved", seq: 6, ts: 6, user_input_resolution: { request_id: request.request_id, call_id: request.call_id, status: "answered", async: true, answers: { choice: ["A"] } } });
    expect(stream.live.value.pending_user_inputs).toEqual([]);
    expect(stream.results.value[request.call_id]?.details).toMatchObject({ status: "answered", answers: { choice: ["A"] } });
  });

  it("restores answered history from a snapshot and keeps the task mirror consistent", () => {
    vi.useFakeTimers();
    const stream = useAgentStream();
    streams.push(stream);
    const snapshot = { seq: 1, messages: [], results: { [request.call_id]: { call_id: request.call_id, text: "accepted", details: ack } }, user_input_answers: { [request.call_id]: answered }, live: emptyLive(), run: { active: false }, policy: { mode: "standard" as const }, usage: { input: 0, output: 0 }, context_tokens: 0 };
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot });
    expect(stream.results.value[request.call_id]?.details).toEqual(answered);
    let task = reduceTaskSessionEvent(null, { type: "snapshot", seq: 1, ts: 1, snapshot });
    task = reduceTaskSessionEvent(task, { type: "user_input_request", seq: 2, ts: 2, user_input: request });
    task = reduceTaskSessionEvent(task, { type: "run_end", seq: 3, ts: 3 });
    expect(task?.live.pending_user_inputs).toEqual([request]);
  });

  it("distinguishes the optional card and its pending acknowledgement in accessible output", async () => {
    const card = await renderToString(createSSRApp({ render: () => h(UserInputBar, { requests: [request], pending: {}, errors: {} }) }).use(i18n));
    expect(card).toContain("可稍后回答");
    expect(card).toContain("不阻塞当前任务");
    expect(card).toContain("作为补充信息交给 Agent");
    const pending = await renderToString(createSSRApp({ render: () => h(UserInputResult, { args: { questions: request.questions }, result: { call_id: request.call_id, text: "accepted", details: ack } }) }).use(i18n));
    expect(pending).toContain("问题已发出，可在方便时回答");
    expect(pending).not.toContain("未提供选择");
    const settled = await renderToString(createSSRApp({ render: () => h(UserInputResult, { args: { questions: request.questions }, result: { call_id: request.call_id, text: "accepted", details: answered } }) }).use(i18n));
    expect(settled).toContain("你的回答");
    expect(settled).toContain("A");
  });
});
