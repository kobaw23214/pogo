import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createOfficeAutosave } from "./officeAutosave";

beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(0); });
afterEach(() => vi.useRealTimers());

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

describe("Office canonical autosave scheduling", () => {
  it("waits three seconds after the most recent observed edit", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    autosave.changed(1);
    await vi.advanceTimersByTimeAsync(2_000); autosave.changed(2);
    await vi.advanceTimersByTimeAsync(2_999); expect(save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1); expect(save).toHaveBeenCalledExactlyOnceWith(2);
    expect(autosave.state()).toMatchObject({ pending: false, savedEpoch: 2, saving: false });
    expect(vi.getTimerCount()).toBe(0);
  });

  it("saves by the fifteen-second cap even if new edits keep postponing idle", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    autosave.changed(1);
    for (let epoch = 2; epoch <= 8; epoch++) { await vi.advanceTimersByTimeAsync(2_000); autosave.changed(epoch); }
    expect(save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1_000); expect(save).toHaveBeenCalledExactlyOnceWith(8);
  });

  it("does not postpone saving when polling repeats the same or older epoch", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    autosave.changed(3);
    await vi.advanceTimersByTimeAsync(2_900); autosave.changed(3); autosave.changed(2);
    await vi.advanceTimersByTimeAsync(100); expect(save).toHaveBeenCalledExactlyOnceWith(3);
  });

  it("can allocate private local epochs when the caller does not supply one", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    expect(autosave.changed()).toBe(1); expect(autosave.changed()).toBe(2);
    await autosave.saveNow(); expect(save).toHaveBeenCalledExactlyOnceWith(2);
  });

  it("flushes pending changes early without claiming an engine clean event saved the file", async () => {
    const result = deferred<boolean>(), save = vi.fn(() => result.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1); autosave.flush();
    expect(autosave.state().pending).toBe(true);
    await vi.advanceTimersByTimeAsync(0); expect(save).toHaveBeenCalledExactlyOnceWith(1);
    expect(autosave.state().savedEpoch).toBe(0);
    result.resolve(true); await vi.advanceTimersByTimeAsync(0);
    expect(autosave.state().savedEpoch).toBe(1);
  });

  it("retries an unavailable or composing editor without running a save or spinning", async () => {
    let available = false;
    const canSave = vi.fn(() => available), save = vi.fn(async () => true);
    const autosave = createOfficeAutosave({ canSave, save }); autosave.changed(1);
    await vi.advanceTimersByTimeAsync(5_500); expect(save).not.toHaveBeenCalled(); expect(canSave).toHaveBeenCalledTimes(3);
    available = true;
    await vi.advanceTimersByTimeAsync(500); expect(save).toHaveBeenCalledExactlyOnceWith(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a manual request while unavailable returns false and leaves automatic retry scheduled", async () => {
    let available = false;
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => available, save });
    autosave.changed(1); expect(await autosave.saveNow()).toBe(false); expect(save).not.toHaveBeenCalled();
    available = true; autosave.flush(); await vi.advanceTimersByTimeAsync(999); expect(save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1); expect(save).toHaveBeenCalledOnce();
  });

  it("uses bounded exponential backoff after failed host commits despite repeated flush events", async () => {
    const save = vi.fn(async () => false), autosave = createOfficeAutosave({ canSave: () => true, save, maxFailureRetryMs: 10_000 });
    autosave.changed(1); await vi.advanceTimersByTimeAsync(3_000); expect(save).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 49; i++) { autosave.flush(); await vi.advanceTimersByTimeAsync(100); }
    expect(save).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(100); expect(save).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(9_999); expect(save).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1); expect(save).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(10_000); expect(save).toHaveBeenCalledTimes(4);
    expect(autosave.state()).toMatchObject({ pending: true, savedEpoch: 0, failures: 4 });
    autosave.dispose();
  });

  it("a thrown save error remains unsaved and follows the same backoff", async () => {
    const save = vi.fn(async () => { throw new Error("network unknown"); });
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    expect(await autosave.saveNow()).toBe(false);
    await vi.advanceTimersByTimeAsync(4_999); expect(save).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1); expect(save).toHaveBeenCalledTimes(2);
    autosave.dispose();
  });

  it("joins manual save with an automatic flight instead of exporting twice", async () => {
    const result = deferred<boolean>(), save = vi.fn(() => result.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    await vi.advanceTimersByTimeAsync(3_000);
    const first = autosave.saveNow(), second = autosave.saveNow(); expect(first).toBe(second);
    expect(save).toHaveBeenCalledOnce(); result.resolve(true);
    expect(await first).toBe(true); expect(autosave.state().pending).toBe(false);
  });

  it("preserves edits arriving during export and starts a separate later save", async () => {
    const first = deferred<boolean>();
    const save = vi.fn().mockImplementationOnce(() => first.promise).mockResolvedValue(true);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    await vi.advanceTimersByTimeAsync(3_000); autosave.changed(2);
    first.resolve(true); await vi.advanceTimersByTimeAsync(0);
    expect(autosave.state()).toMatchObject({ savedEpoch: 1, dirtyEpoch: 2, pending: true });
    await vi.advanceTimersByTimeAsync(2_999); expect(save).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1); expect(save.mock.calls).toEqual([[1], [2]]);
  });

  it("a newer manual request waits for the current flight and then saves once", async () => {
    const first = deferred<boolean>(), second = deferred<boolean>();
    const save = vi.fn().mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    const old = autosave.saveNow(); await vi.advanceTimersByTimeAsync(0); autosave.changed(2);
    const manualA = autosave.saveNow(), manualB = autosave.saveNow(); expect(save).toHaveBeenCalledTimes(1);
    first.resolve(true); expect(await old).toBe(true); await vi.advanceTimersByTimeAsync(0);
    expect(save.mock.calls).toEqual([[1], [2]]); second.resolve(true);
    expect(await manualA).toBe(true); expect(await manualB).toBe(true); expect(save).toHaveBeenCalledTimes(2);
  });

  it("does not bypass an unknown failed flight to replay a queued manual export", async () => {
    const first = deferred<boolean>(), save = vi.fn(() => first.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    void autosave.saveNow(); await vi.advanceTimersByTimeAsync(0); autosave.changed(2);
    const manual = autosave.saveNow(); first.resolve(false); expect(await manual).toBe(false);
    await vi.advanceTimersByTimeAsync(4_999); expect(save).toHaveBeenCalledOnce();
    autosave.dispose();
  });

  it("acknowledges only the confirmed epoch and never clears a newer dirty epoch", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    autosave.changed(2); autosave.saved(1); expect(autosave.state().pending).toBe(true);
    autosave.saved(2); expect(autosave.state().pending).toBe(false);
    await vi.advanceTimersByTimeAsync(60_000); expect(save).not.toHaveBeenCalled();
    expect(() => autosave.saved(3)).toThrow(RangeError);
  });

  it("accepts a host commit receipt when the original response is lost", async () => {
    const result = deferred<boolean>(), save = vi.fn(() => result.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    const request = autosave.saveNow(); await vi.advanceTimersByTimeAsync(0);
    autosave.saved(1); result.resolve(false);
    expect(await request).toBe(true); expect(autosave.state()).toMatchObject({ failures: 0, pending: false });
  });

  it("dispose cancels timers and rejects a late save as a new clean-state confirmation", async () => {
    const result = deferred<boolean>(), save = vi.fn(() => result.promise);
    const autosave = createOfficeAutosave({ canSave: () => true, save }); autosave.changed(1);
    const request = autosave.saveNow(); await vi.advanceTimersByTimeAsync(0); autosave.dispose(); result.resolve(true);
    expect(await request).toBe(false); expect(autosave.state()).toMatchObject({ savedEpoch: 0, disposed: true, pending: true });
    autosave.changed(2); autosave.flush(); autosave.saved(1); expect(await autosave.saveNow()).toBe(false);
    await vi.advanceTimersByTimeAsync(60_000); expect(save).toHaveBeenCalledOnce(); expect(vi.getTimerCount()).toBe(0);
  });

  it("disposal before the deferred callback starts never invokes the host save", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => true, save });
    autosave.changed(1); const request = autosave.saveNow(); autosave.dispose();
    expect(await request).toBe(false); expect(save).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0);
  });

  it("retries a throwing eligibility check without treating it as a successful save", async () => {
    const save = vi.fn(async () => true), autosave = createOfficeAutosave({ canSave: () => { throw new Error("unmounted"); }, save });
    autosave.changed(1); await vi.advanceTimersByTimeAsync(4_000);
    expect(save).not.toHaveBeenCalled(); expect(autosave.state()).toMatchObject({ failures: 0, pending: true });
    autosave.dispose();
  });

  it("rejects invalid epoch and timing configuration", () => {
    const options = { canSave: () => true, save: async () => true };
    expect(() => createOfficeAutosave({ ...options, delayMs: 0 })).toThrow(RangeError);
    const autosave = createOfficeAutosave(options);
    for (const epoch of [-1, Number.NaN, Number.MAX_SAFE_INTEGER + 1, 1.5]) expect(() => autosave.changed(epoch)).toThrow(RangeError);
    autosave.dispose();
  });
});
