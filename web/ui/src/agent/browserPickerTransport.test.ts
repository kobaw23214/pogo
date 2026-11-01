import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { browserPickerTransport, PICKER_TRANSPORT_DEADLINE_MS } from "./browserPickerTransport";
import { createBrowserPicker, type BrowserPickPreview } from "./browserPicker";
import type { NativeBrowserPick } from "./nativeBrowser";
const never = () => new Promise<never>(() => {});
function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const frame = { context_ref: `ctx_${"a".repeat(24)}`, tab_id: "t1", version: "v1", viewport: { width: 1280, height: 800 }, pixels: "blob:frame" };
const preview: BrowserPickPreview = { ...frame, element: { tag: "button", role: "button", rect: { x: 0, y: 0, width: 100, height: 100 } }, preview_token: `pv_${"a".repeat(48)}`, expires_in_ms: 3000 };
const pick: NativeBrowserPick = { ...frame, url: "https://fixture.test", title: "Fixture", element: { ...preview.element!, ref: "p1", name: "Save", text: "Save" } };
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(0); });
afterEach(() => { vi.useRealTimers(); });

describe("finite picker transport without assuming server cancellation", () => {
  it("settles preview/commit even when fetch ignores abort, classifying them as unknown pending", async () => {
    for (const kind of ["preview", "commit"] as const) {
      let signal!: AbortSignal;
      const result = browserPickerTransport(input => { signal = input; return never(); }, kind).catch(error => error);
      await vi.advanceTimersByTimeAsync(PICKER_TRANSPORT_DEADLINE_MS[kind]);
      expect(await result).toMatchObject({ status: 504, code: "PICK_OBSERVATION_PENDING" });
      expect(signal.aborted).toBe(true);
    }
  });
  it("keeps network failure and gateway 502/504 pending, preserving definite rejection codes", async () => {
    for (const failure of [new TypeError("Failed to fetch"), Object.assign(new Error("upstream lost"), { status: 502 }), Object.assign(new Error("gateway timeout"), { status: 504 })]) {
      expect(await browserPickerTransport(async () => { throw failure; }, "preview").catch(error => error)).toMatchObject({ code: "PICK_OBSERVATION_PENDING" });
    }
    for (const status of [400, 403, 409]) {
      const failure = Object.assign(new Error("definite rejection"), { status });
      expect(await browserPickerTransport(async () => { throw failure; }, "commit").catch(error => error)).toBe(failure);
    }
  });
  it("bounds status reads and lets a later fresh read complete", async () => {
    const stalled = browserPickerTransport(never, "state").catch(error => error);
    await vi.advanceTimersByTimeAsync(PICKER_TRANSPORT_DEADLINE_MS.state);
    expect(await stalled).toMatchObject({ code: "PICK_STATE_TIMEOUT" });
    expect(await browserPickerTransport(async () => ({ pick_pending: false }), "state")).toEqual({ pick_pending: false });
  });
  it("supersedes an old status read immediately and ignores its late false response", async () => {
    const old = deferred<{ pick_pending: boolean }>(), controller = new AbortController(), applyOld = vi.fn();
    const retired = browserPickerTransport(() => old.promise, "state", { signal: controller.signal }).then(applyOld, error => error);
    await vi.advanceTimersByTimeAsync(0); controller.abort(); await vi.advanceTimersByTimeAsync(0);
    expect(await retired).toMatchObject({ code: "PICK_STATE_CANCELLED" });
    const fresh = await browserPickerTransport(async () => ({ pick_pending: true }), "state"); expect(fresh.pick_pending).toBe(true);
    old.resolve({ pick_pending: false }); await vi.advanceTimersByTimeAsync(0); expect(applyOld).not.toHaveBeenCalled();
  });
  it("does not publish a late successful preview after its transport deadline", async () => {
    const old = deferred<BrowserPickPreview>(), publish = vi.fn();
    const result = browserPickerTransport(() => old.promise, "preview").then(publish, error => error);
    await vi.advanceTimersByTimeAsync(PICKER_TRANSPORT_DEADLINE_MS.preview);
    expect(await result).toMatchObject({ code: "PICK_OBSERVATION_PENDING" });
    old.resolve(preview); await vi.advanceTimersByTimeAsync(0); expect(publish).not.toHaveBeenCalled();
  });
});

it("a blackholed preview pauses its scheduler, then fresh false restores only a new hover", async () => {
  const published = vi.fn(), committed = vi.fn();
  let calls = 0;
  const picker = createBrowserPicker({ frame: () => frame,
    preview: () => browserPickerTransport(() => ++calls === 1 ? never() : Promise.resolve(preview), "preview"),
    commit: async () => pick, previewed: published, committed, status: vi.fn(), error: vi.fn(), now: () => Date.now() });
  picker.hover({ x: 1, y: 1 }); picker.click({ x: 2, y: 2 });
  await vi.advanceTimersByTimeAsync(PICKER_TRANSPORT_DEADLINE_MS.preview);
  expect(picker.pending).toBe(true); picker.hover({ x: 3, y: 3 }); expect(calls).toBe(1);
  const fresh = await browserPickerTransport(async () => ({ pick_pending: false }), "state"); if (!fresh.pick_pending) picker.drained();
  await vi.advanceTimersByTimeAsync(0); expect(calls).toBe(2); expect(published).toHaveBeenCalledWith(preview, { x: 3, y: 3 }); expect(committed).not.toHaveBeenCalled();
});

it("a blackholed final commit never replays or adds its late citation after recovery", async () => {
  const old = deferred<NativeBrowserPick>(), committed = vi.fn();
  const picker = createBrowserPicker({ frame: () => frame, preview: async () => preview,
    commit: () => browserPickerTransport(() => old.promise, "commit"), previewed: vi.fn(), committed, status: vi.fn(), error: vi.fn(), now: () => Date.now() });
  picker.click({ x: 2, y: 2 }); await vi.advanceTimersByTimeAsync(0);
  await vi.advanceTimersByTimeAsync(PICKER_TRANSPORT_DEADLINE_MS.commit);
  expect(picker.pending).toBe(true); picker.drained(); await vi.advanceTimersByTimeAsync(0);
  old.resolve(pick); await vi.advanceTimersByTimeAsync(0); expect(committed).not.toHaveBeenCalled();
});
