import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createIncomingSampler } from "./incomingSampler";

describe("Incoming preview sampler", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("emits the first preview immediately and coalesces later updates into one-second samples", () => {
    const values: string[] = [];
    const sampler = createIncomingSampler<string>(value => values.push(value));
    sampler.push("first");
    sampler.push("second");
    sampler.push("third");
    expect(values).toEqual(["first"]);
    vi.advanceTimersByTime(999);
    expect(values).toEqual(["first"]);
    sampler.push("fourth");
    vi.advanceTimersByTime(1);
    expect(values).toEqual(["first", "fourth"]);
    sampler.push("fifth");
    vi.advanceTimersByTime(1000);
    expect(values).toEqual(["first", "fourth", "fifth"]);
    sampler.cancel();
  });

  it("flushes the latest suffix and drops it when the preview is cancelled", () => {
    const values: number[] = [];
    const sampler = createIncomingSampler<number>(value => values.push(value));
    sampler.push(1);
    sampler.push(2);
    sampler.cancel();
    vi.advanceTimersByTime(2000);
    expect(values).toEqual([1]);
    sampler.push(3);
    expect(values).toEqual([1]);
  });
});
