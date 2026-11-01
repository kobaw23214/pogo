import { afterEach, describe, expect, it, vi } from "vitest";
import { createBrowserWarmViewPool } from "./browserWarmViews";

afterEach(() => vi.useRealTimers());
describe("warm browser Sheets", () => {
  it("evicts only the least recently hidden view in the same session", () => {
    vi.useFakeTimers();
    const pool = createBrowserWarmViewPool(), evict = [vi.fn(), vi.fn(), vi.fn(), vi.fn()];
    const views = evict.map(callback => pool.register("session-a", {}, { evict: callback }));
    const other = vi.fn(); pool.register("session-b", {}, { evict: other }).hide();
    views[0]!.hide(); views[1]!.hide(); views[0]!.show(); views[0]!.hide();
    views[2]!.hide();
    expect(evict.map(callback => callback.mock.calls.length)).toEqual([0, 1, 0, 0]);
    expect(other).not.toHaveBeenCalled();
    views[3]!.show();
    expect(evict[3]).not.toHaveBeenCalled();
  });
  it("returning to a Sheet cancels its expiry, repeated hide does not renew it", () => {
    vi.useFakeTimers();
    const pool = createBrowserWarmViewPool(2, 1000), evict = vi.fn();
    const view = pool.register("s", {}, { evict });
    view.hide(); vi.advanceTimersByTime(500); view.hide(); vi.advanceTimersByTime(500);
    expect(evict).toHaveBeenCalledOnce();
    view.show(); view.hide(); vi.advanceTimersByTime(900); view.show(); vi.advanceTimersByTime(1000);
    expect(evict).toHaveBeenCalledOnce();
  });
  it("disposal removes only its own timer and cannot retain a closed pane", () => {
    vi.useFakeTimers();
    const pool = createBrowserWarmViewPool(2, 1000), first = vi.fn(), second = vi.fn();
    const a = pool.register("s", {}, { evict: first }), b = pool.register("s", {}, { evict: second });
    a.hide(); b.hide(); a.dispose(); a.hide(); vi.advanceTimersByTime(1000);
    expect(first).not.toHaveBeenCalled(); expect(second).toHaveBeenCalledOnce();
  });
});
