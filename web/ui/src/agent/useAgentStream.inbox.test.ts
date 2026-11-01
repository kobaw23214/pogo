import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { emptyLive, type InboxItem, type Snapshot } from "@/api/types";
import { useAgentStream } from "./useAgentStream";
import { buildTimeline } from "./timeline";
import { buildExchanges } from "./exchanges";

const item: InboxItem = { id: "i1", text: "补充要求", mode: "steer", status: "steering", revision: 2, created_at: 1, updated_at: 2 };
const snapshot = (inbox: InboxItem[] = []): Snapshot => ({ seq: 1, messages: [{ id: "u1", role: "user", content: [{ type: "text", text: "原任务" }] }], results: {}, live: { ...emptyLive(), active: true }, run: { active: true, run_id: "r1" }, policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0, inbox });
describe("follow-up stream and replay", () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  const create = () => { const s = useAgentStream({ attachments: false }); streams.push(s); return s; };
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => { for (const s of streams.splice(0)) s.disconnect(); vi.clearAllTimers(); vi.useRealTimers(); });
  it("restores pending follow-ups and applies versioned receipts", () => {
    const s = create(); s.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: snapshot([item]) });
    s.apply({ type: "inbox_updated", seq: 2, ts: 3, item: { ...item, status: "delivered", revision: 3 } });
    s.acceptInboxItem(item);
    expect(s.inbox.value).toHaveLength(1); expect(s.inbox.value[0].status).toBe("delivered");
    s.apply({ type: "snapshot", seq: 4, ts: 4, snapshot: snapshot() });
    expect(s.inbox.value).toEqual([]);
  });
  it("keeps the original task active after a delivered follow-up, including replay", () => {
    const s = create(); s.apply({ type: "snapshot", seq: 1, ts: 1, snapshot: snapshot([item]) });
    s.apply({ type: "user_message", seq: 2, ts: 2, message_id: "u2", text: item.text, inbox_id: item.id, steering: true, run_id: "r1" });
    expect(s.messages.value[1]).toMatchObject({ steering: true, inbox_id: "i1", run_id: "r1" });
    const project = () => buildExchanges(buildTimeline(s.messages.value, {}, s.live.value), true);
    expect(project()).toHaveLength(1); expect(project()[0].finished).toBe(false);
    const messages = s.messages.value;
    s.apply({ type: "snapshot", seq: 3, ts: 3, snapshot: { ...snapshot(), messages } });
    expect(project()[0].steering?.[0].text).toBe(item.text);
  });
});
