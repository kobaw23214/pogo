import { describe, expect, it } from "vitest";
import { useAgentStream } from "./useAgentStream";
import type { AgentEvent } from "@/api/types";

// The server-clock offset the live "working for" clock runs on. Every event
// carries the server's own stamp, so each arrival is one sample of the
// server−browser difference, biased low by delivery latency — the estimate is
// therefore the window's maximum, and a replayed old event can never drag it
// down.
function ev(partial: Partial<AgentEvent> & Pick<AgentEvent, "type">): AgentEvent {
  return { seq: 0, ts: 0, ...partial };
}

describe("server clock offset", () => {
  it("stays null until the first stamped event, falling back to browser time", () => {
    const s = useAgentStream();
    expect(s.serverOffset.value).toBeNull();
    expect(Math.abs(s.serverNow() - Date.now())).toBeLessThan(100);
  });

  it("takes a skewed server's stamp at face value", () => {
    const s = useAgentStream();
    const skew = 3_600_000; // the server's clock is an hour ahead of this browser
    s.apply(ev({ type: "user_message", message_id: "u1", text: "hi", ts: Date.now() + skew }));
    expect(s.serverOffset.value).toBeGreaterThan(skew - 1000);
    expect(s.serverNow()).toBeGreaterThan(Date.now() + skew - 1000);
  });

  it("ignores stale replayed events: latency can only lower a sample", () => {
    const s = useAgentStream();
    s.apply(ev({ type: "user_message", message_id: "u1", text: "now", ts: Date.now() }));
    const good = s.serverOffset.value;
    // A replay from an hour ago must not move the estimate.
    s.apply(ev({ type: "user_message", message_id: "u2", text: "old", ts: Date.now() - 3_600_000 }));
    expect(s.serverOffset.value).toBe(good);
  });
});
