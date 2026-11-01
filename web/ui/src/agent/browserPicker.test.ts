import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createBrowserPicker, pickPreviewMatches, type BrowserPickPreview, type BrowserPickerFrame } from "./browserPicker";
import { api } from "../api/client";
import type { NativeBrowserPick } from "./nativeBrowser";
const original: BrowserPickerFrame = { context_ref: `ctx_${"a".repeat(24)}`, tab_id: "t1", version: "v1", viewport: { width: 1280, height: 800 }, pixels: "blob:frame1" };
const token = `pv_${"a".repeat(48)}`;
const preview = (update: Partial<BrowserPickPreview> = {}): BrowserPickPreview => ({ context_ref: original.context_ref, tab_id: "t1", version: "v1", viewport: original.viewport, preview_token: token, expires_in_ms: 3000, element: { tag: "button", role: "button", rect: { x: 0, y: 0, width: 100, height: 100 } }, ...update });
const picked: NativeBrowserPick = { tab_id: "t1", version: "v1", viewport: original.viewport, url: "https://fixture.test", title: "Fixture", element: { ref: "p1", tag: "button", role: "button", name: "Save", text: "Save", rect: { x: 0, y: 0, width: 100, height: 100 } } };
function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function harness() {
  let frame: BrowserPickerFrame | null = { ...original };
  const previews: ReturnType<typeof deferred<BrowserPickPreview>>[] = [], commits: ReturnType<typeof deferred<NativeBrowserPick>>[] = [];
  const options = {
    frame: () => frame,
    preview: vi.fn(() => { const request = deferred<BrowserPickPreview>(); previews.push(request); return request.promise; }),
    commit: vi.fn(() => { const request = deferred<NativeBrowserPick>(); commits.push(request); return request.promise; }),
    previewed: vi.fn(), committed: vi.fn(), status: vi.fn(), error: vi.fn(), now: () => Date.now(),
  };
  const picker = createBrowserPicker(options);
  return { picker, options, previews, commits, frame(value: BrowserPickerFrame | null) { frame = value; } };
}
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(0); });
afterEach(() => { vi.useRealTimers(); });

it("validates a light preview without accepting mismatched tokens, viewport or document", () => {
  expect(pickPreviewMatches(preview(), original)).toBe(true);
  expect(pickPreviewMatches(preview({ element: null, preview_token: undefined, expires_in_ms: undefined }), original)).toBe(true);
  for (const update of [{ context_ref: "other" }, { tab_id: "t2" }, { version: "v2" }, { preview_token: "p1" }, { expires_in_ms: 0 }, { expires_in_ms: 3001 }, { viewport: { width: 1, height: 800 } }, { element: { tag: "button", role: "button", rect: { x: NaN, y: 0, width: 5, height: 5 } } }]) expect(pickPreviewMatches(preview(update), original)).toBe(false);
  expect(pickPreviewMatches(preview(), null)).toBe(false);
});

it("uses separate sparse preview and token-bound commit HTTP contracts", async () => {
  const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(preview()), { headers: { "Content-Type": "application/json" } }));
  const point = { context_ref: original.context_ref, tab_id: "t1", version: "v1", x: 10, y: 10 };
  try {
    await api.browserPickPreview("session-a", { ...point, sessionId: "must-not-be-sent" } as typeof point);
    expect(fetch.mock.calls[0]![0]).toBe("/api/sessions/session-a/browser/pick/preview");
    expect(JSON.parse(fetch.mock.calls[0]![1]!.body as string)).toEqual(point);
    fetch.mockResolvedValueOnce(new Response(JSON.stringify(picked), { headers: { "Content-Type": "application/json" } }));
    await api.browserPick("session-a", { ...point, preview_token: token });
    expect(fetch.mock.calls[1]![0]).toBe("/api/sessions/session-a/browser/pick");
    expect(JSON.parse(fetch.mock.calls[1]![1]!.body as string)).toEqual({ ...point, preview_token: token });
  } finally { fetch.mockRestore(); }
});

describe("bounded latest-pointer picker scheduling", () => {
  it("starts promptly, keeps one in flight and one latest pointer, and throttles at 50 ms", async () => {
    const h = harness(); h.picker.hover({ x: 1, y: 1 });
    expect(h.options.preview).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10); h.picker.hover({ x: 2, y: 2 });
    await vi.advanceTimersByTimeAsync(10); h.picker.hover({ x: 3, y: 3 });
    h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.previewed).not.toHaveBeenCalled(); expect(h.options.preview).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(29); expect(h.options.preview).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1); expect(h.options.preview).toHaveBeenLastCalledWith(expect.objectContaining({ x: 3, y: 3 }));
    h.previews[1]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.previewed).toHaveBeenCalledTimes(1);
    expect(h.options.commit).not.toHaveBeenCalled();
  });
  it("prioritizes a click over queued hover but waits for the actual previous read", async () => {
    const h = harness(); h.picker.hover({ x: 1, y: 1 });
    h.picker.hover({ x: 2, y: 2 }); h.picker.click({ x: 3, y: 3 });
    expect(h.options.preview).toHaveBeenCalledTimes(1); expect(h.options.commit).not.toHaveBeenCalled();
    h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.preview).toHaveBeenCalledTimes(2);
    expect(h.options.preview).toHaveBeenLastCalledWith(expect.objectContaining({ x: 3, y: 3 }));
    h.previews[1]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.commit).toHaveBeenCalledExactlyOnceWith({ context_ref: original.context_ref, tab_id: "t1", version: "v1", x: 3, y: 3, preview_token: token });
    h.commits[0]!.resolve(picked); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.committed).toHaveBeenCalledExactlyOnceWith(picked);
  });
  it("always obtains a new preview for a click, even at the currently highlighted coordinate", async () => {
    const h = harness(); h.picker.hover({ x: 5, y: 5 }); h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    h.picker.click({ x: 5, y: 5 }); expect(h.options.preview).toHaveBeenCalledTimes(2); expect(h.options.commit).not.toHaveBeenCalled();
  });
  it("retains the current box for inner movement while checking children again, and removes it immediately outside", async () => {
    const h = harness(); h.picker.hover({ x: 5, y: 5 }); h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    h.picker.hover({ x: 15, y: 15 }); expect(h.options.previewed).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(50); expect(h.options.preview).toHaveBeenCalledTimes(2);
    const child = preview({ element: { tag: "span", role: "", rect: { x: 10, y: 10, width: 20, height: 20 } } });
    h.previews[1]!.resolve(child); await vi.advanceTimersByTimeAsync(0); expect(h.options.previewed).toHaveBeenLastCalledWith(child, { x: 15, y: 15 });
    h.picker.hover({ x: 80, y: 80 }); expect(h.options.previewed).toHaveBeenLastCalledWith(null);
  });
  it("ignores an obsolete response after leave or document/viewport invalidation without starting a concurrent read", async () => {
    const h = harness(); h.picker.hover({ x: 5, y: 5 }); h.picker.invalidate();
    h.frame({ ...original, version: "v2", viewport: { width: 1440, height: 900 }, pixels: "blob:frame2" }); h.picker.hover({ x: 20, y: 20 });
    await vi.advanceTimersByTimeAsync(200); expect(h.options.preview).toHaveBeenCalledTimes(1);
    h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.previewed).not.toHaveBeenCalled(); expect(h.options.preview).toHaveBeenLastCalledWith(expect.objectContaining({ version: "v2", x: 20 }));
  });
  it("never generates a reference for an empty, expired, or mismatched preview", async () => {
    const h = harness(); h.picker.click({ x: 5, y: 5 });
    h.previews[0]!.resolve(preview({ element: null, preview_token: undefined })); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.commit).not.toHaveBeenCalled(); expect(h.options.error).toHaveBeenCalledWith(expect.objectContaining({ code: "PICK_EMPTY" }), "commit");
  });
  it("invalidates a late final quote when the pointer leaves the page", async () => {
    const h = harness(); h.picker.click({ x: 5, y: 5 }); h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0);
    h.picker.invalidate(); h.commits[0]!.resolve(picked); await vi.advanceTimersByTimeAsync(0); expect(h.options.committed).not.toHaveBeenCalled();
  });
  it("holds the queue on a real pending observation and resumes only the latest hover, never its old click", async () => {
    const h = harness(); h.picker.hover({ x: 1, y: 1 }); h.picker.click({ x: 2, y: 2 });
    h.previews[0]!.reject(Object.assign(new Error("still pending"), { code: "PICK_OBSERVATION_PENDING", status: 504 })); await vi.advanceTimersByTimeAsync(0);
    expect(h.picker.pending).toBe(true); h.picker.hover({ x: 10, y: 10 }); await vi.advanceTimersByTimeAsync(5000);
    expect(h.options.preview).toHaveBeenCalledTimes(1); expect(h.options.commit).not.toHaveBeenCalled();
    h.picker.drained(); await vi.advanceTimersByTimeAsync(0);
    expect(h.options.preview).toHaveBeenLastCalledWith(expect.objectContaining({ x: 10, y: 10 }));
    h.previews[1]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0); expect(h.options.commit).not.toHaveBeenCalled();
  });
  it("does not publish late results after disposal", async () => {
    const h = harness(); h.picker.hover({ x: 5, y: 5 }); h.picker.close();
    h.previews[0]!.resolve(preview()); await vi.advanceTimersByTimeAsync(0); expect(h.options.previewed).not.toHaveBeenCalled();
  });
});
