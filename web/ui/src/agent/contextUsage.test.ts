import { describe, expect, it } from "vitest";
import type { Message, ToolResult } from "@/api/types";
import { contextRingPercent, createContextUsageReader } from "./contextUsage";

const message = (text: string): Message => ({ id: "u1", role: "user", content: [{ type: "text", text }] });
const read = createContextUsageReader();
const empty = { used: 0, window: 100000, overhead: 0, messages: [] as Message[], results: {} as Record<string, ToolResult> };

describe("shared context reading", () => {
  it("keeps a provider measurement even when estimated overhead is much higher", () => {
    expect(read({ ...empty, used: 500, overhead: 5000, messages: [message("x".repeat(10000))] }))
      .toEqual({ used: 500, window: 100000, source: "measured", percent: 0.5, free: 99500 });
  });
  it("estimates a reopened API snapshot with context_tokens zero from overhead and history", () => {
    const reading = read({ ...empty, overhead: 10000, messages: [message("a".repeat(400))], results: { r1: { call_id: "r1", text: "b".repeat(800) } } });
    expect(reading).toMatchObject({ used: 10300, window: 100000, source: "estimated", free: 89700 });
    expect(reading.percent).toBeCloseTo(10.3);
  });
  it("does not pretend that an unknown reading is zero used or 100 percent free", () => {
    expect(read(empty)).toEqual({ used: 0, window: 100000, source: "unknown", percent: null, free: null });
  });
  it("a model switch retains the last provider count only as an estimate", () => {
    expect(read({ ...empty, used: 1234, estimated: true, window: 200000 })).toMatchObject({ used: 1234, source: "estimated", window: 200000 });
  });
  it("does not fabricate a ratio for an unknown model limit", () => {
    expect(read({ ...empty, used: 500, window: 0 })).toMatchObject({ used: 500, source: "measured", percent: null, free: null });
  });
  it("keeps over-window numeric usage visible and clamps only the drawing", () => {
    const reading = read({ ...empty, used: 125000 });
    expect(reading.percent).toBe(125);
    expect(contextRingPercent(reading.percent)).toBe(100);
    expect(reading.free).toBe(0);
  });
  it.each([null, undefined, Number.NaN, Number.POSITIVE_INFINITY])("unknown percentage %s draws no fabricated arc", value => {
    expect(contextRingPercent(value)).toBeNull();
  });
});
