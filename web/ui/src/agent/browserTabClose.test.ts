import { describe, expect, it, vi } from "vitest";
import type { api } from "@/api/client";
import { BrowserTabClosures } from "./browserTabClose";
import { browserControlMemory, installBrowserLease, reconcileBrowserSheets, type BrowserSheetBinding } from "./browserSheets";
import type { NativeBrowserControl, NativeBrowserState } from "./nativeBrowser";

let serial = 0;
function fixture() {
  const binding: BrowserSheetBinding = { sessionId: `close-${++serial}`, context_ref: "ctx_123456789012345678901234", tab_id: "t2" };
  const state: NativeBrowserState = { backend: "native", context_ref: binding.context_ref, control: "agent", control_epoch: 0, active_tab: "t1", viewed_tab: "t1", url: "https://a.invalid/", title: "A", viewport: { width: 1280, height: 800 }, tabs: [
    { id: "t1", url: "https://a.invalid/", title: "A", control: "agent", control_epoch: 0 },
    { id: "t2", url: "about:blank", title: "", control: "agent", control_epoch: 0 },
  ] };
  const acquired: NativeBrowserControl = { ...state, viewed_tab: "t2", control: "human", control_epoch: 1, lease_token: "close-lease", tabs: [state.tabs[0], { ...state.tabs[1], control: "human", control_epoch: 1 }] };
  const remaining = { ...state, tabs: [state.tabs[0]] };
  const client = {
    browserControl: vi.fn<typeof api.browserControl>(async () => acquired),
    browserTabs: vi.fn<typeof api.browserTabs>(async () => ({ context_ref: binding.context_ref, tab_id: "t2", state: remaining })),
  };
  return { binding, state, acquired, remaining, client, closings: new BrowserTabClosures() };
}

describe("explicit browser Sheet closure", () => {
  it("closes a loading Agent page directly with fresh metadata and merges repeated clicks", async () => {
    const { binding, client, closings, state } = fixture();
    const webClient = { ...client, browserState: vi.fn<typeof api.browserState>(async () => ({ ...state, viewed_tab: 't2', generation: 3, page_loading: true })) };
    const first = closings.close(binding, webClient);
    const second = closings.close(binding, webClient);
    expect(second).toBe(first);
    await first;
    expect(webClient.browserState).toHaveBeenCalledExactlyOnceWith(binding.sessionId, undefined, { context_ref: binding.context_ref, tab_id: binding.tab_id });
    expect(webClient.browserControl).not.toHaveBeenCalled();
    expect(webClient.browserTabs).toHaveBeenCalledExactlyOnceWith(binding.sessionId, expect.objectContaining({ action: 'close', tab_id: binding.tab_id, context_ref: binding.context_ref, generation: 3, control_epoch: 0 }));
    expect(webClient.browserTabs.mock.calls[0][1].lease_token).toBeUndefined();
  });

  it("takes control of only an Agent-owned new Page and closes it with that lease", async () => {
    const { binding, client, closings } = fixture();
    await closings.close(binding, client);
    expect(client.browserControl).toHaveBeenCalledExactlyOnceWith(binding.sessionId, "human", { context_ref: binding.context_ref, tab_id: "t2" });
    expect(client.browserTabs).toHaveBeenCalledExactlyOnceWith(binding.sessionId, expect.objectContaining({ action: "close", tab_id: "t2", context_ref: binding.context_ref, lease_token: "close-lease" }));
    expect(browserControlMemory(binding.sessionId, binding.context_ref, "t1").token).toBe("");
    expect(browserControlMemory(binding.sessionId, binding.context_ref, "t2").token).toBe("");
  });

  it("uses an existing own lease without another takeover", async () => {
    const { binding, client, closings } = fixture();
    installBrowserLease(binding.sessionId, binding.context_ref, "t2", 2, "own-lease");
    await closings.close(binding, client);
    expect(client.browserControl).not.toHaveBeenCalled();
    expect(client.browserTabs).toHaveBeenCalledWith(binding.sessionId, expect.objectContaining({ lease_token: "own-lease" }));
  });

  it("does not force another human's lease or replay a takeover with a lost receipt", async () => {
    const { binding, client, closings, state } = fixture();
    client.browserControl.mockRejectedValueOnce(new TypeError("takeover receipt lost"));
    await expect(closings.close(binding, client)).rejects.toThrow("takeover receipt lost");
    expect(client.browserControl).toHaveBeenCalledTimes(1);
    client.browserControl.mockRejectedValueOnce(Object.assign(new Error("already controlled by a human"), { status: 409 }));
    await expect(closings.close(binding, client)).rejects.toThrow("already controlled by a human");
    expect(client.browserControl.mock.calls.every(call => !call[2]?.force)).toBe(true);
    expect(client.browserTabs).not.toHaveBeenCalled();
    expect(closings.filterState(binding.sessionId, state)).toBe(state);
  });

  it("retries an uncertain close with the original operation and lease even after ownership changed", async () => {
    const { binding, client, closings } = fixture();
    client.browserTabs.mockRejectedValueOnce(new TypeError("close receipt lost"));
    await expect(closings.close(binding, client)).rejects.toThrow("close receipt lost");
    const sent = client.browserTabs.mock.calls[0][1];
    installBrowserLease(binding.sessionId, binding.context_ref, "t2", 3, "newer-lease");
    await closings.close(binding, client);
    expect(client.browserControl).toHaveBeenCalledTimes(1);
    expect(client.browserTabs.mock.calls[1][1]).toEqual(sent);
    expect(browserControlMemory(binding.sessionId, binding.context_ref, "t2").token).toBe("newer-lease");
  });

  it("fences late closed-page snapshots, but permits the same tab ID in another context/session", async () => {
    const { binding, client, closings, state } = fixture();
    await closings.close(binding, client);
    expect(closings.filterState(binding.sessionId, { ...state, viewed_tab: "t2" })).toBeUndefined();
    expect(closings.filterState(binding.sessionId, { ...state, active_tab: "t2" })).toMatchObject({ active_tab: "", tabs: [state.tabs[0]] });
    expect(closings.filterState(binding.sessionId, { ...state, context_ref: "ctx_999999999999999999999999" })?.tabs).toHaveLength(2);
    expect(closings.filterState("another-session", state)).toBe(state);
  });

  it("keeps later sibling Sheets when replaying a cached successful close snapshot", async () => {
    const { binding, client, closings, state } = fixture();
    const later = { ...state, tabs: [...state.tabs, { id: "t3", url: "https://new.invalid/", title: "Later" }] };
    const tabs = reconcileBrowserSheets([], binding.sessionId, later);
    const result = await closings.close(binding, client);
    const filtered = closings.filterState(binding.sessionId, result!);
    const surviving = tabs.filter(tab => tab.browserBinding?.tab_id !== "t2");
    expect(reconcileBrowserSheets(surviving, binding.sessionId, filtered!, "New tab", false).map(tab => tab.browserBinding?.tab_id)).toEqual(["t1", "t3"]);
  });

  it("uses fresh complete discovery to confirm a lost close without trusting a hidden Pane", async () => {
    const { binding, client, closings, state, remaining } = fixture();
    client.browserTabs.mockRejectedValueOnce(new TypeError("close receipt lost"));
    await expect(closings.close(binding, client)).rejects.toThrow("close receipt lost");
    closings.filterState(binding.sessionId, remaining);
    expect(closings.filterState(binding.sessionId, state)).toBe(state);
    closings.filterState(binding.sessionId, remaining, true);
    expect(closings.filterState(binding.sessionId, state)?.tabs).toHaveLength(1);
    expect(closings.filterState(binding.sessionId, { ...state, viewed_tab: "t2" })).toBeUndefined();
  });

  it("does not treat malformed or contradictory discovery as a closed Page", async () => {
    const { binding, client, closings, state, remaining } = fixture();
    client.browserTabs.mockRejectedValueOnce(new TypeError("close receipt lost"));
    await expect(closings.close(binding, client)).rejects.toThrow("close receipt lost");
    closings.filterState(binding.sessionId, { ...remaining, active_tab: "t2" }, true);
    closings.filterState(binding.sessionId, { ...remaining, tabs: [{ id: "t1" }] } as NativeBrowserState, true);
    expect(closings.filterState(binding.sessionId, state)).toBe(state);
  });

  it("treats a bound Page's 410 as closed and never deletes optimistically on a rejected close", async () => {
    const missing = fixture();
    missing.client.browserControl.mockRejectedValueOnce(Object.assign(new Error("Page closed"), { status: 410 }));
    await expect(missing.closings.close(missing.binding, missing.client)).resolves.toBeUndefined();
    expect(missing.closings.filterState(missing.binding.sessionId, missing.state)?.tabs).toHaveLength(1);
    const gone = fixture();
    installBrowserLease(gone.binding.sessionId, gone.binding.context_ref, "t2", 2, "gone-lease");
    gone.client.browserTabs.mockRejectedValueOnce(Object.assign(new Error("Page closed"), { status: 410 }));
    await expect(gone.closings.close(gone.binding, gone.client)).resolves.toBeUndefined();
    expect(browserControlMemory(gone.binding.sessionId, gone.binding.context_ref, "t2").token).toBe("");
    const rejected = fixture();
    rejected.client.browserTabs.mockRejectedValueOnce(Object.assign(new Error("action pending"), { status: 409, code: "TAB_OPERATION_REJECTED" }));
    rejected.client.browserControl.mockResolvedValueOnce(rejected.acquired).mockResolvedValueOnce({ ...rejected.state, viewed_tab: "t2", control_epoch: 2 });
    await expect(rejected.closings.close(rejected.binding, rejected.client)).rejects.toThrow("action pending");
    expect(rejected.client.browserControl).toHaveBeenLastCalledWith(rejected.binding.sessionId, "agent", expect.objectContaining({ tab_id: "t2", lease_token: "close-lease" }));
    expect(rejected.closings.filterState(rejected.binding.sessionId, rejected.state)).toBe(rejected.state);
  });

  it("rejects mismatched or superseded takeover replies before sending a close", async () => {
    const wrong = fixture();
    wrong.client.browserControl.mockResolvedValueOnce({ ...wrong.acquired, viewed_tab: "t1" });
    await expect(wrong.closings.close(wrong.binding, wrong.client)).rejects.toThrow("does not match");
    expect(wrong.client.browserTabs).not.toHaveBeenCalled();
    const stale = fixture();
    installBrowserLease(stale.binding.sessionId, stale.binding.context_ref, "t2", 0, "");
    stale.client.browserControl.mockImplementationOnce(async () => {
      installBrowserLease(stale.binding.sessionId, stale.binding.context_ref, "t2", 3, "later-lease");
      return stale.acquired;
    });
    await expect(stale.closings.close(stale.binding, stale.client)).rejects.toThrow("control changed");
    expect(stale.client.browserTabs).not.toHaveBeenCalled();
    expect(browserControlMemory(stale.binding.sessionId, stale.binding.context_ref, "t2").token).toBe("later-lease");
  });
});
