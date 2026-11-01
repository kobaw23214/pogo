import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OFFICE_STARTUP_TIMEOUT_MS, officeStartupTimeout, retryOfficeStartup, waitForOfficeStartup } from "./officeStartup";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("Office startup stage budgets and safe retries", () => {
  it("allows a slow stage its entire five-minute budget and clears the deadline after success", async () => {
    expect(OFFICE_STARTUP_TIMEOUT_MS).toBe(300_000);
    let finish!: (value: string) => void;
    const request = retryOfficeStartup("opening", () => new Promise<string>(resolve => { finish = resolve; }));
    await vi.advanceTimersByTimeAsync(299_999);
    finish("same-session");
    expect(await request).toBe("same-session");
    expect(vi.getTimerCount()).toBe(0);
  });
  it("aborts a stalled request at the stage deadline, even if the task ignores its signal", async () => {
    let signal!: AbortSignal;
    const request = retryOfficeStartup("bridge", value => { signal = value; return new Promise(() => {}); }, { timeoutMs: 25_000 });
    const rejected = expect(request).rejects.toMatchObject({ code: "OFFICE_STARTUP_TIMEOUT", phase: "bridge", timeoutMs: 300_000 });
    await vi.advanceTimersByTimeAsync(300_000);
    await rejected;
    expect(signal.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("shares one deadline and stable task identity across transient retries", async () => {
    const signals: AbortSignal[] = [], progress: { attempt: number; elapsedMs: number }[] = [];
    const instanceId = "same-executor";
    const task = vi.fn((signal: AbortSignal) => {
      signals.push(signal);
      if (signals.length === 1) return new Promise<string>((_, reject) => setTimeout(() => reject(Object.assign(new Error("gateway"), { status: 503 })), 200_000));
      expect(instanceId).toBe("same-executor");
      return new Promise<string>(() => {});
    });
    const request = retryOfficeStartup("opening", task, { onAttempt: value => progress.push(value) });
    const rejected = expect(request).rejects.toMatchObject({ code: "OFFICE_STARTUP_TIMEOUT", phase: "opening" });
    await vi.advanceTimersByTimeAsync(300_000);
    await rejected;
    expect(task).toHaveBeenCalledTimes(2);
    expect(signals[1]).toBe(signals[0]);
    expect(progress.map(value => value.attempt)).toEqual([1, 2]);
    expect(progress[1]!.elapsedMs).toBe(200_500);
    expect(vi.getTimerCount()).toBe(0);
  });
  it.each([400, 401, 403, 404, 409, 422])("never retries permissions, identity or application rejection HTTP %s", async status => {
    const error = Object.assign(new Error("rejected"), { status });
    const task = vi.fn(async () => { throw error; });
    await expect(retryOfficeStartup("opening", task)).rejects.toBe(error);
    expect(task).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("retries network failures but caps automatic attempts at three", async () => {
    const task = vi.fn(async () => { throw new TypeError("Failed to fetch"); });
    const request = retryOfficeStartup("checking", task, { maxAttempts: 20 });
    const rejected = expect(request).rejects.toThrow("Failed to fetch");
    await vi.advanceTimersByTimeAsync(1500);
    await rejected;
    expect(task).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(0);
  });
  it.each(["OFFICE_CONFIG_INVALID", "OFFICE_OPEN_TIMEOUT"])("does not replay a definitive %s response merely because its HTTP status is transient", async code => {
    const error = Object.assign(new Error("opening refused before claiming"), { code, status: 503 });
    const task = vi.fn(async () => { throw error; });
    await expect(retryOfficeStartup("opening", task)).rejects.toBe(error);
    expect(task).toHaveBeenCalledTimes(1);
  });
  it("preserves a longer configured budget and reports the actual phase on timeout", async () => {
    const request = retryOfficeStartup("document", () => new Promise(() => {}), { timeoutMs: 600_000 });
    const rejected = expect(request).rejects.toMatchObject({ code: "OFFICE_STARTUP_TIMEOUT", phase: "document", timeoutMs: 600_000 });
    await vi.advanceTimersByTimeAsync(600_000);
    await rejected;
    expect(officeStartupTimeout("engine")).toMatchObject({ code: "OFFICE_STARTUP_TIMEOUT", phase: "engine", timeoutMs: 300_000 });
  });
  it("cancels a pending stage without another attempt and ignores a late success", async () => {
    const controller = new AbortController();
    let finish!: (value: string) => void;
    const task = vi.fn(() => new Promise<string>(resolve => { finish = resolve; }));
    const request = retryOfficeStartup("recovering", task, { signal: controller.signal });
    const rejected = expect(request).rejects.toMatchObject({ name: "AbortError", code: "OFFICE_STARTUP_CANCELLED" });
    await vi.advanceTimersByTimeAsync(0);
    controller.abort();
    await rejected;
    finish("too-late");
    await vi.advanceTimersByTimeAsync(1000);
    expect(task).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("cancels backoff promptly and never starts a request for an already cancelled stage", async () => {
    const controller = new AbortController(), task = vi.fn(async () => { throw Object.assign(new Error("gateway"), { status: 502 }); });
    const request = retryOfficeStartup("opening", task, { signal: controller.signal });
    const rejected = expect(request).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(1);
    controller.abort(); await rejected;
    expect(task).toHaveBeenCalledTimes(1);
    await expect(retryOfficeStartup("opening", task, { signal: controller.signal })).rejects.toMatchObject({ name: "AbortError" });
    expect(task).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
  it("lets one cancelled waiter leave a shared operation without changing another waiter", async () => {
    let resolve!: (value: string) => void;
    const shared = new Promise<string>(done => { resolve = done; }), controller = new AbortController();
    const a = waitForOfficeStartup(shared, controller.signal), b = waitForOfficeStartup(shared);
    const rejected = expect(a).rejects.toMatchObject({ name: "AbortError" });
    controller.abort(); await rejected;
    resolve("shared-api"); expect(await b).toBe("shared-api");
  });
});
