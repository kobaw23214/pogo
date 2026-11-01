import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type AgentEvent } from "@/api/types";
import { useAgentStream } from "./useAgentStream";

describe("model context window updates", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const stream of streams.splice(0)) stream.disconnect();
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it.each([undefined, 0])("clears a known window when the next model reports %s and restores a later known window", unknownWindow => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: {
      seq: 1, messages: [], results: {}, live: emptyLive(),
      run: { active: false, model: "known-first", context_window: 128000 },
      policy: { mode: "standard" }, usage: { input: 999999, output: 0 }, context_tokens: 4567,
    } });
    expect(stream.run.value.context_window).toBe(128000);
    expect(stream.contextEstimated.value).toBe(false);

    const unknown: AgentEvent = {
      type: "model_changed", seq: 2, ts: 2, model: "unknown-window", provider: "fixture",
      ...(unknownWindow === undefined ? {} : { context_window: unknownWindow }),
    };
    stream.apply(unknown);
    expect(stream.run.value).toMatchObject({ model: "unknown-window", context_window: 0 });
    expect(stream.contextTokens.value).toBe(4567);
    expect(stream.contextEstimated.value).toBe(true);

    stream.apply({ type: "model_changed", seq: 3, ts: 3, model: "known-again", provider: "fixture", context_window: 256000 });
    expect(stream.run.value).toMatchObject({ model: "known-again", context_window: 256000 });
    expect(stream.contextTokens.value).toBe(4567);
  });

  it("keeps snapshot provenance through reconnect and replaces estimates with the next measured input", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: {
      seq: 1, messages: [], results: {}, live: emptyLive(), run: { active: false, model: "next", provider: "fixture", context_window: 100000 },
      policy: { mode: "standard" }, usage: { input: 999999, output: 0 }, context_tokens: 1234, context_estimated: true,
    } });
    expect(stream.contextEstimated.value).toBe(true);
    stream.apply({ type: "model_changed", seq: 2, ts: 2, model: "next", provider: "fixture", context_window: 100000 });
    expect(stream.contextEstimated.value).toBe(true);
    stream.apply({ type: "message", seq: 3, ts: 3, message_id: "m1", role: "assistant", content: [], usage: { input: 4321, output: 20 } });
    expect(stream.contextEstimated.value).toBe(false);
    expect(stream.contextTokens.value).toBe(4321);
    stream.apply({ type: "run_end", seq: 4, ts: 4, usage: { input: 1004320, output: 20 } });
    expect(stream.contextTokens.value).toBe(4321);
  });

  it("announces policy commits even when the current model stays the same", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "run_end", seq: 1, ts: 1, stop_reason: "done" });
    expect(stream.modelChangeVersion.value).toBe(0);
    stream.apply({ type: "model_changed", seq: 2, ts: 2, model: "same-model", provider: "fixture", context_window: 128000 });
    expect(stream.modelChangeVersion.value).toBe(1);
    stream.apply({ type: "model_changed", seq: 3, ts: 3, model: "same-model", provider: "fixture", context_window: 128000 });
    expect(stream.modelChangeVersion.value).toBe(2);
    expect(stream.run.value).toMatchObject({ active: false, model: "same-model", context_window: 128000 });
  });

  it("retains effective settings while pending and updates the same model after the run", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: {
      seq: 1, messages: [{ id: "saved-message", role: "user", content: [{ type: "text", text: "Keep this conversation" }] }],
      results: {}, live: emptyLive(), run: { active: true, run_id: "current-run", model: "same-model", provider: "fixture", context_window: 128000 },
      policy: { mode: "standard" }, usage: { input: 9000, output: 200 }, context_tokens: 4321, clear_trigger: 64000, overhead_tokens: 300,
    } });
    stream.apply({ type: "model_config_pending", seq: 2, ts: 2, model_config_pending: true });
    expect(stream.run.value).toMatchObject({ active: true, run_id: "current-run", model: "same-model", context_window: 128000, model_config_pending: true });
    expect(stream.modelChangeVersion.value).toBe(0);

    stream.apply({ type: "run_end", seq: 3, ts: 3 });
    expect(stream.run.value.model_config_pending).toBe(true);
    stream.apply({ type: "model_changed", seq: 4, ts: 4, model: "same-model", provider: "fixture", context_window: 256000, clear_trigger: 128000, overhead_tokens: 400 });
    expect(stream.run.value).toMatchObject({ active: false, model: "same-model", context_window: 256000, model_config_pending: false });
    expect(stream.messages.value[0]?.id).toBe("saved-message");
    expect(stream.contextTokens.value).toBe(4321);
    expect(stream.contextEstimated.value).toBe(false);
    expect(stream.usage.value).toEqual({ input: 9000, output: 200 });
    expect(stream.modelChangeVersion.value).toBe(1);
    expect(stream.clearTrigger.value).toBe(128000);
    expect(stream.overheadTokens.value).toBe(400);
    stream.apply({ type: "model_changed", seq: 5, ts: 5, model: "same-model", provider: "fixture", context_window: 0, clear_trigger: 0, overhead_tokens: 0 });
    expect(stream.run.value.context_window).toBe(0);
    expect(stream.clearTrigger.value).toBe(0);
    expect(stream.overheadTokens.value).toBe(0);
  });

  it("restores pending state on reconnect and clears it when saved settings revert", () => {
    const stream = useAgentStream({ attachments: false });
    streams.push(stream);
    stream.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: {
      seq: 1, messages: [], results: {}, live: emptyLive(),
      run: { active: true, model: "same-model", context_window: 128000, model_config_pending: true },
      policy: { mode: "standard" }, usage: { input: 4321, output: 0 }, context_tokens: 4321,
    } });
    expect(stream.run.value.model_config_pending).toBe(true);
    stream.apply({ type: "model_config_pending", seq: 2, ts: 2, model_config_pending: false });
    expect(stream.run.value).toMatchObject({ active: true, model: "same-model", context_window: 128000, model_config_pending: false });
    expect(stream.contextTokens.value).toBe(4321);
  });
});
