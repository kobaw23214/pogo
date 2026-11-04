import { describe, expect, it } from "vitest";
import { emptyLive, type AgentEvent, type CompactionView, type ContextPrompt, type Message } from "@/api/types";
import { useAgentStream } from "./useAgentStream";

const realMessages: Message[] = [
  { id: "u1", role: "user", content: [{ type: "text", text: "first" }] },
  { id: "m1", role: "assistant", content: [{ type: "text", text: "answer" }] },
];
const prompt: ContextPrompt = {
  composition: { fixed: 100, user: 25, estimated: 125, messages: 1 },
  messages: [{ role: "user", content: [{ type: "text", text: "compressed summary" }] }],
};
function snapshot(compactions: CompactionView[] = [], contextPrompt?: ContextPrompt): AgentEvent {
  return {
    type: "snapshot", seq: 1, ts: 1,
    snapshot: {
      seq: 1, messages: realMessages, compactions, results: {}, live: emptyLive(), run: { active: false },
      policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: contextPrompt?.composition.estimated ?? 0,
      context_estimated: !!contextPrompt, context_prompt: contextPrompt,
    },
  };
}
function card(id: string, startedAt = 100): CompactionView {
  return { id, after: 2, ts: 101, summary: "summary", before: 2, before_tokens: 100, after_tokens: 20, started_at: startedAt, finished_at: 101 };
}

describe("compaction stream state", () => {
  it("keeps the checkpoint anchored before messages that arrive after local completion", () => {
    const stream = useAgentStream();
    stream.apply(snapshot());
    stream.acceptCompaction(card("local-100"));
    stream.messages.value = [...realMessages,
      { id: "u2", role: "user", content: [{ type: "text", text: "next" }] },
      { id: "m2", role: "assistant", content: [{ type: "text", text: "working" }] },
    ];
    stream.apply({ type: "compacted", seq: 2, ts: 102, compaction: card("server-checkpoint", 105) });
    expect(stream.messages.value.map(message => message.id)).toEqual(["u1", "m1", "u2", "m2"]);
    expect(stream.compactions.value.map(view => [view.id, view.after])).toEqual([["server-checkpoint", 2]]);
    stream.disconnect();
  });

  it("replaces checkpoint state wholesale on a fresh snapshot", () => {
    const stream = useAgentStream();
    stream.apply(snapshot([card("old")], prompt));
    expect(stream.compactions.value).toHaveLength(1);
    expect(stream.contextPrompt.value).toEqual(prompt);
    stream.apply(snapshot([]));
    expect(stream.compactions.value).toEqual([]);
    expect(stream.contextPrompt.value).toBeNull();
    expect(stream.contextTokens.value).toBe(0);
    stream.disconnect();
  });

  it("restores the compacted estimate and preserves the next provider measurement", () => {
    const stream = useAgentStream();
    stream.apply(snapshot([card("server-checkpoint")], prompt));
    expect(stream.contextTokens.value).toBe(125);
    expect(stream.contextEstimated.value).toBe(true);
    expect(stream.contextPrompt.value).toEqual(prompt);
    stream.apply({ type: "compacted", seq: 2, ts: 102, compaction: card("server-checkpoint"), context_prompt: prompt });
    expect(stream.contextTokens.value).toBe(125);
    expect(stream.messages.value).toEqual(realMessages);
    stream.apply({ type: "message", seq: 3, ts: 103, message_id: "m2", content: [{ type: "text", text: "next answer" }], usage: { input: 80, output: 4 } });
    expect(stream.contextTokens.value).toBe(80);
    expect(stream.contextEstimated.value).toBe(false);
    const nextPrompt: ContextPrompt = { ...prompt, composition: { ...prompt.composition, estimated: 160, messages: 2 }, messages: [...prompt.messages, { role: "user", content: [{ type: "text", text: "next question" }] }] };
    stream.apply({ type: "run_end", seq: 4, ts: 104, context_prompt: nextPrompt });
    expect(stream.contextTokens.value).toBe(80);
    expect(stream.contextEstimated.value).toBe(false);
    expect(stream.contextPrompt.value).toEqual(nextPrompt);
    stream.disconnect();
  });
});
