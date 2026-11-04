import { describe, expect, it } from "vitest";
import { BrowserNewOperations, browserControlMemory, clearBrowserLease, installBrowserLease, browserOperationRetrySame, browserSheetVisible, observeBrowserControl, reconcileBrowserSheets, reconciledBrowserSelection, retireBrowserControls, targetMatches, validBrowserBinding } from "./browserSheets";
import { restoreTabs, type DockTab } from "@/components/dockSheets";
import type { NativeBrowserState } from "./nativeBrowser";
const context = "ctx_123456789012345678901234";
const otherContext = "ctx_999999999999999999999999";
const state: NativeBrowserState = { backend: "native", control: "agent", control_epoch: 0, context_ref: context, viewed_tab: "t1", active_tab: "t1", url: "https://a.invalid/", title: "A", viewport: { width: 1280, height: 800 }, tabs: [{ id: "t1", url: "https://a.invalid/", title: "A" }, { id: "t2", url: "https://b.invalid/", title: "B" }] };
const shell: DockTab = { id: "shell", kind: "shell", label: "Shell", closable: true };
describe("native browser Sheet identities", () => {
  it("updates and clears each page favicon without fetching saved or remote URLs", () => {
    const favicon = "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=";
    const tabs = reconcileBrowserSheets([], "s1", { ...state, tabs: [{ ...state.tabs[0], favicon }, state.tabs[1]] });
    expect(tabs[0].favicon).toBe(favicon);
    expect(tabs[1].favicon).toBeUndefined();
    expect(restoreTabs({ open: tabs }, []).tabs[0].favicon).toBe(favicon);
    const unsafe = reconcileBrowserSheets(tabs, "s1", { ...state, tabs: [{ ...state.tabs[0], favicon: "http://private.test/favicon.ico" }, state.tabs[1]] });
    expect(unsafe[0].favicon).toBeUndefined();
    const blank = reconcileBrowserSheets(tabs, "s1", { ...state, tabs: [{ ...state.tabs[0], url: "about:blank", favicon }, state.tabs[1]] });
    expect(blank[0].favicon).toBeUndefined();
    expect(reconcileBrowserSheets(tabs, "s1", state)[0].favicon).toBeUndefined();
  });
  it("shows a Skill document only in the session whose selection authorized it", () => {
    const tab: DockTab = { id: "skill:s1:browser-use:SKILL.md", kind: "skill", label: "SKILL.md", skill: "browser-use", path: "SKILL.md", parentSession: "s1", closable: true };
    expect(browserSheetVisible(tab, "s1")).toBe(true);
    expect(browserSheetVisible(tab, "s2")).toBe(false);
    expect(browserSheetVisible(tab, null)).toBe(false);
  });
  it("binds independent Sheets to pages in one context and keeps unrelated Sheets", () => {
    const tabs = reconcileBrowserSheets([shell], "s1", state);
    expect(tabs).toHaveLength(3);
    expect(tabs[0]).toBe(shell);
    expect(new Set(tabs.map(t => t.id)).size).toBe(3);
    expect(tabs.slice(1).map(t => t.browserBinding)).toEqual(["t1", "t2"].map(tab_id => ({ sessionId: "s1", context_ref: context, tab_id })));
    expect(reconcileBrowserSheets(tabs, "s1", state)).toEqual(tabs);
  });
  it("adopts the legacy viewer once without replaying its saved URL", () => {
    const tabs = reconcileBrowserSheets([{ id: "browser:session", kind: "browser", label: "Old", url: "https://do-not-visit.invalid/", closable: true }], "s1", state);
    expect(tabs).toHaveLength(2);
    expect(tabs[0]).toMatchObject({ id: "browser:session", url: state.url, browserBinding: { tab_id: "t1" } });
  });
  it("adds background/pop-up pages without changing IDs or the user's surviving selection", () => {
    const tabs = reconcileBrowserSheets([], "s1", state);
    const popup = { ...state, active_tab: "t3", viewed_tab: "t3", tabs: [...state.tabs, { id: "t3", url: "https://popup.invalid", title: "Popup" }] };
    const result = reconcileBrowserSheets(tabs, "s1", popup);
    expect(result.slice(0, 2)).toEqual(tabs);
    expect(result[2].browserBinding?.tab_id).toBe("t3");
    expect(reconciledBrowserSelection(result, tabs[0].id, "s1", popup)).toBe(tabs[0].id);
    expect(reconciledBrowserSelection(result, tabs[1].id, "s1", popup)).toBe(tabs[1].id);
  });
  it("removes a cancelled Page and selects its surviving replacement without replaying URLs", () => {
    const initial = { ...state, tabs: [state.tabs[0]] };
    const tabs = reconcileBrowserSheets([shell], "s1", initial);
    const previousId = tabs[1].id;
    const replacement = { ...state, generation: 2, active_tab: "t2", viewed_tab: "t2", tabs: [{ id: "t2", url: "about:blank", title: "" }] };
    const next = reconcileBrowserSheets(tabs, "s1", replacement);
    expect(next).toHaveLength(2);
    expect(next.find(tab => tab.id === previousId)).toBeUndefined();
    expect(next[1]).toMatchObject({ url: "about:blank", browserBinding: { tab_id: "t2" } });
    expect(next[1].browserDraft).toBeUndefined();
    expect(reconciledBrowserSelection(next, previousId, "s1", replacement)).toBe(next[1].id);
    expect(reconciledBrowserSelection(next, shell.id, "s1", replacement)).toBe(shell.id);
  });
  it("prunes only this session and context while preserving unavailable old bindings", () => {
    const current = reconcileBrowserSheets([], "s1", state);
    const old = { ...current[0], id: "old-context", browserUnavailable: true, browserBinding: { ...current[0].browserBinding!, context_ref: otherContext } };
    const otherSession = { ...current[0], id: "other-session", browserBinding: { ...current[0].browserBinding!, sessionId: "s2" } };
    const empty = { ...state, tabs: [], active_tab: "", viewed_tab: "" };
    const next = reconcileBrowserSheets([shell, old, ...current, otherSession], "s1", empty);
    expect(next).toEqual([shell, old, otherSession]);
    expect(reconciledBrowserSelection(next, old.id, "s1", empty)).toBe(old.id);
    expect(reconciledBrowserSelection(next, current[0].id, "s1", empty)).toBe(shell.id);
    expect(reconciledBrowserSelection([], current[0].id, "s1", empty)).toBeNull();
  });
  it("late Pane observations cannot delete a newly discovered popup", () => {
    const popup = { ...state, active_tab: "t3", viewed_tab: "t3", tabs: [...state.tabs, { id: "t3", url: "https://popup.invalid", title: "Popup" }] };
    const tabs = reconcileBrowserSheets([], "s1", popup);
    const next = reconcileBrowserSheets(tabs, "s1", state, "New tab", false);
    expect(next.map(tab => tab.id)).toEqual(tabs.map(tab => tab.id));
    expect(reconciledBrowserSelection(next, tabs[2].id, "s1", state)).toBe(tabs[2].id);
  });
  it("a malformed or contradictory page list cannot prove a page closed", () => {
    const tabs = reconcileBrowserSheets([], "s1", state);
    expect(reconcileBrowserSheets(tabs, "s1", { ...state, tabs: [] })).toBe(tabs);
    expect(reconcileBrowserSheets(tabs, "s1", { ...state, tabs: [state.tabs[0], state.tabs[0]] })).toBe(tabs);
  });
  it("keeps context reincarnations distinct even when tN is reused", () => {
    const tabs = reconcileBrowserSheets([], "s1", state);
    const result = reconcileBrowserSheets(tabs, "s1", { ...state, context_ref: otherContext });
    expect(result).toHaveLength(4);
    expect(result[0].browserBinding?.context_ref).toBe(context);
    expect(result[2].browserBinding?.context_ref).toBe(otherContext);
    expect(result[0].browserUnavailable).toBe(true);
    expect(reconciledBrowserSelection(result, tabs[0].id, "s1", { ...state, context_ref: otherContext })).toBe(result[2].id);
    expect(reconciledBrowserSelection(result, tabs[0].id, "s1", { ...state, context_ref: otherContext }, true)).toBe(tabs[0].id);
    expect(targetMatches({ ...state, viewed_tab: "t2" }, { context_ref: context, tab_id: "t1" })).toBe(false);
    expect(targetMatches({ ...state, active_tab: "t2" }, { context_ref: context, tab_id: "t1" })).toBe(true);
  });
  it("restores an expired context as a recovery record without reviving its lease or replaying its URL", () => {
    const tabs = reconcileBrowserSheets([], "s1", state);
    const changed = { ...state, context_ref: otherContext, tabs: [], active_tab: "", viewed_tab: "" };
    const retired = reconcileBrowserSheets(tabs, "s1", changed);
    expect(retired.every(tab => tab.browserUnavailable)).toBe(true);
    expect(retired.map(tab => tab.url)).toEqual(tabs.map(tab => tab.url));
    const restored = restoreTabs({ open: retired, active: tabs[0].id }, []);
    expect(restored.tabs.every(tab => tab.browserUnavailable)).toBe(true);
    expect(reconciledBrowserSelection(restored.tabs, tabs[0].id, "s1", changed)).toBe(tabs[0].id);
    const old = browserControlMemory("recovery-lease", context, "t1");
    installBrowserLease("recovery-lease", context, "t1", 1, "old-control");
    installBrowserLease("recovery-other-session", context, "t1", 1, "other-control");
    retireBrowserControls("recovery-lease", otherContext);
    expect(old.token).toBe("");
    expect(browserControlMemory("recovery-lease", otherContext, "t1").token).toBe("");
    expect(browserControlMemory("recovery-other-session", context, "t1").token).toBe("other-control");
  });
  it("restores all bound Sheets and filters them by conversation, never collapsing them", () => {
    const tabs = reconcileBrowserSheets(reconcileBrowserSheets([shell], "s1", state), "s2", state);
    const restored = restoreTabs({ open: tabs, active: tabs[2].id }, []);
    expect(restored.tabs).toEqual(tabs);
    expect(restored.active).toBe(tabs[2].id);
    expect(restored.tabs.filter(t => browserSheetVisible(t, "s1"))).toEqual(tabs.slice(0, 3));
    expect(restored.tabs.filter(t => browserSheetVisible(t, null))).toEqual([shell]);
  });
  it("supports zero native pages without making a replacement page", () => {
    expect(reconcileBrowserSheets([shell], "s1", { ...state, tabs: [], active_tab: "", viewed_tab: "" })).toEqual([shell]);
  });
  it("validates bindings without trusting malformed saved identifiers", () => {
    expect(validBrowserBinding({ sessionId: "s1", context_ref: context, tab_id: "t1" })).toBe(true);
    for (const value of [null, {}, { sessionId: "s1", context_ref: "", tab_id: "t1" }, { sessionId: "s1", context_ref: context, tab_id: "../t1" }]) expect(validBrowserBinding(value)).toBe(false);
  });
});
describe("window-local browser control", () => {
  it("inherits the local human lease only for authenticated popup lineage and resolves nested popups", () => {
    const sid = "human-popup-lineage";
    installBrowserLease(sid, context, "t1", 3, "opener-human");
    const tabs: NativeBrowserState["tabs"] = [
      { id: "t4", url: "https://auth.invalid/", title: "Child popup", control: "human", control_epoch: 3, opener_tab: "t3", inherited_control_epoch: 3 },
      { ...state.tabs[0], control: "human", control_epoch: 3 },
      { ...state.tabs[1], control: "human", control_epoch: 3 },
      { id: "t3", url: "https://auth.invalid/", title: "Popup", control: "human", control_epoch: 3, opener_tab: "t1", inherited_control_epoch: 3 },
    ];
    observeBrowserControl(sid, { ...state, control: "human", control_epoch: 3, tabs });
    expect(browserControlMemory(sid, context, "t3").token).toBe("opener-human");
    expect(browserControlMemory(sid, context, "t4").token).toBe("opener-human");
    expect(browserControlMemory(sid, context, "t2").token).toBe("");
    expect(browserControlMemory("popup-other-session", context, "t3").token).toBe("");
    expect(browserControlMemory(sid, otherContext, "t3").token).toBe("");
  });
  it("does not fabricate human authority from a new Sheet, an Agent opener or a mismatched inherited epoch", () => {
    const sid = "human-popup-denied-lineage";
    installBrowserLease(sid, context, "t1", 3, "opener-human");
    for (const child of [
      { control: "human" as const, control_epoch: 3 },
      { control: "human" as const, control_epoch: 3, opener_tab: "t1", inherited_control_epoch: 2 },
      { control: "agent" as const, control_epoch: 3, opener_tab: "t1", inherited_control_epoch: 3 },
      { control: "human" as const, control_epoch: 3, opener_tab: "t2", inherited_control_epoch: 3 },
    ]) {
      observeBrowserControl(sid, { ...state, control: "human", control_epoch: 3,
        tabs: [{ ...state.tabs[0], control: "human", control_epoch: 3 }, { ...state.tabs[1], control: "agent", control_epoch: 3 },
          { id: "t3", url: "https://popup.invalid/", title: "Popup", ...child }] });
      expect(browserControlMemory(sid, context, "t3").token).toBe("");
    }
  });
  it("an Agent claim revokes only that popup and stale inheritance cannot reinstall its lease", () => {
    const sid = "human-popup-agent-claim";
    installBrowserLease(sid, context, "t1", 2, "human-token");
    const inherited: NativeBrowserState = { ...state, control: "human", control_epoch: 2,
      tabs: [{ ...state.tabs[0], control: "human", control_epoch: 2 },
        { ...state.tabs[1], control: "human", control_epoch: 2, opener_tab: "t1", inherited_control_epoch: 2 }] };
    observeBrowserControl(sid, inherited);
    expect(browserControlMemory(sid, context, "t2").token).toBe("human-token");
    observeBrowserControl(sid, { ...inherited, tabs: [inherited.tabs[0], { ...inherited.tabs[1], control: "agent", control_epoch: 3 }] });
    observeBrowserControl(sid, inherited);
    expect(browserControlMemory(sid, context, "t2")).toMatchObject({ token: "", mode: "agent", epoch: 3 });
    expect(browserControlMemory(sid, context, "t1").token).toBe("human-token");
    expect(installBrowserLease(sid, context, "t2", 3, "late-token")).toBe(false);
  });
  it("a child first observed after its opener claim still receives its exact inherited human lease", () => {
    const sid = "human-popup-before-opener-claim";
    installBrowserLease(sid, context, "t1", 2, "old-opener-token");
    const claimed: NativeBrowserState = { ...state, control: "agent", control_epoch: 3,
      tabs: [{ ...state.tabs[0], control: "agent", control_epoch: 3 },
        { ...state.tabs[1], control: "human", control_epoch: 2, opener_tab: "t1", inherited_control_epoch: 2 }] };
    observeBrowserControl(sid, claimed);
    expect(browserControlMemory(sid, context, "t1").token).toBe("");
    expect(browserControlMemory(sid, context, "t2")).toMatchObject({ token: "old-opener-token", epoch: 2, mode: "human" });
    retireBrowserControls(sid, otherContext);
    observeBrowserControl(sid, claimed);
    expect(browserControlMemory(sid, context, "t2").token).toBe("");
  });
  it("a closed opener grants only a worker-proven child its locally known lease", () => {
    const sid = "human-popup-after-opener-close";
    installBrowserLease(sid, context, "t1", 2, "closed-opener-token");
    const closed: NativeBrowserState = { ...state, active_tab: "t2", viewed_tab: "t2", control: "human", control_epoch: 2,
      tabs: [{ ...state.tabs[1], control: "human", control_epoch: 2, opener_tab: "t1", inherited_control_epoch: 2 },
        { id: "t3", url: "https://unknown.invalid/", title: "Unknown opener", control: "human", control_epoch: 2, opener_tab: "t99", inherited_control_epoch: 2 }] };
    observeBrowserControl(sid, closed);
    expect(browserControlMemory(sid, context, "t2").token).toBe("closed-opener-token");
    expect(browserControlMemory(sid, context, "t3").token).toBe("");
    observeBrowserControl("closed-opener-other-session", closed);
    expect(browserControlMemory("closed-opener-other-session", context, "t2").token).toBe("");
  });
  it("shares a lease only across views of the same Page and never another tab/session", () => {
    const memory = browserControlMemory("shared-lease-test", context, "t1");
    Object.assign(memory, { token: "private-lease", context, epoch: 3 });
    expect(browserControlMemory("shared-lease-test", context, "t1")).toBe(memory);
    expect(browserControlMemory("another-session", context, "t1").token).toBe("");
    expect(browserControlMemory("shared-lease-test", context, "t2").token).toBe("");
    observeBrowserControl("shared-lease-test", { ...state, control_epoch: 2 });
    expect(memory.token).toBe("private-lease");
    observeBrowserControl("shared-lease-test", { ...state, context_ref: otherContext, control_epoch: 7 });
    expect(memory.token).toBe("private-lease");
    observeBrowserControl("shared-lease-test", { ...state, control_epoch: 4 });
    expect(memory.token).toBe("");
  });
});

it("keeps the same op ID after uncertain results, pending and conflicting operations", () => {
  for (const err of [new Error("network lost"), { status: 504 }, { status: 409 }, { status: 409, code: "TAB_OPERATION_PENDING" }, { status: 400, message: "TAB_OPERATION_OUTCOME_UNKNOWN: inspect before retry" }, { status: 409, code: "TAB_OPERATION_ID_CONFLICT" }]) expect(browserOperationRetrySame(err)).toBe(true);
  for (const err of [{ status: 429 }, { status: 403 }, { status: 409, code: "TAB_OPERATION_REJECTED" }, { status: 409, message: "TAB_OPERATION_REJECTED: page was reclaimed" }]) expect(browserOperationRetrySame(err)).toBe(false);
});

describe("per-tab lease response ordering", () => {
  it("late takeover cannot overwrite a newer takeover of the same Page", () => {
    const sid = "lease-reordering";
    expect(installBrowserLease(sid, context, "t1", 4, "B-new-token")).toBe(true);
    expect(installBrowserLease(sid, context, "t1", 2, "A-old-token")).toBe(false);
    expect(browserControlMemory(sid, context, "t1")).toMatchObject({ token: "B-new-token", epoch: 4 });
  });
  it("keeps independent epochs and tokens when two tabs are taken over", () => {
    const sid = "independent-tab-leases";
    expect(installBrowserLease(sid, context, "t1", 8, "A-token")).toBe(true);
    expect(installBrowserLease(sid, context, "t2", 1, "B-token")).toBe(true);
    expect(clearBrowserLease(sid, context, "t2", "A-token", 8)).toBe(false);
    observeBrowserControl(sid, { ...state, viewed_tab: "t2", control: "agent", control_epoch: 2 });
    expect(browserControlMemory(sid, context, "t1")).toMatchObject({ token: "A-token", epoch: 8 });
    expect(browserControlMemory(sid, context, "t2")).toMatchObject({ token: "", epoch: 2 });
  });
  it("uses explicit tab status to revoke a sibling lease without copying the viewed tab's owner", () => {
    const sid = "per-tab-observed-reclaim";
    installBrowserLease(sid, context, "t1", 1, "A-token");
    installBrowserLease(sid, context, "t2", 1, "B-token");
    observeBrowserControl(sid, { ...state, control: "human", control_epoch: 1, tabs: [{ ...state.tabs[0]!, control: "human", control_epoch: 1 }, { ...state.tabs[1]!, control: "agent", control_epoch: 2 }] });
    expect(browserControlMemory(sid, context, "t1").token).toBe("A-token");
    expect(browserControlMemory(sid, context, "t2").token).toBe("");
  });
  it("late return and rejected input clear only the exact lease sent by that request", () => {
    const sid = "return-reordering";
    installBrowserLease(sid, context, "t1", 1, "A-token");
    installBrowserLease(sid, context, "t1", 3, "B-token");
    expect(clearBrowserLease(sid, context, "t1", "A-token", 1)).toBe(false);
    expect(clearBrowserLease(sid, context, "t1", "B-token", 1)).toBe(false);
    observeBrowserControl(sid, { ...state, control_epoch: 2, control: "agent" });
    expect(browserControlMemory(sid, context, "t1").token).toBe("B-token");
    expect(clearBrowserLease(sid, context, "t1", "B-token", 3)).toBe(true);
  });
  it("late authority from a discarded context cannot replace the current context", () => {
    const sid = "context-reordering";
    installBrowserLease(sid, otherContext, "t1", 2, "current");
    expect(installBrowserLease(sid, context, "t1", 99, "stale-context")).toBe(true);
    expect(clearBrowserLease(sid, context, "t1", "current", 2)).toBe(false);
    expect(browserControlMemory(sid, otherContext, "t1").token).toBe("current");
  });
});
it("preserves unresolved new operation identities per session/context and through reload without storing a lease", () => {
  const values = new Map<string, string>();
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } };
  const operations = new BrowserNewOperations(storage);
  const first = operations.get("session-A", context);
  const second = operations.get("session-B", context);
  expect(second).not.toBe(first);
  expect(operations.get("session-A", context)).toBe(first);
  const restored = new BrowserNewOperations(storage);
  expect(restored.get("session-A", context)).toBe(first);
  expect(restored.get("session-B", context)).toBe(second);
  expect([...values.values()].join()).not.toMatch(/lease|token|https:/);
  restored.complete("session-A", context);
  expect(restored.get("session-A", context)).not.toBe(first);
  expect(restored.get("session-B", context)).toBe(second);
});
it("quarantines invalid saved bindings instead of rebinding them to the active page", () => {
  const bad = { id: "browser:bad-binding", label: "Unavailable", kind: "browser", closable: true, browserBinding: { sessionId: "s1", context_ref: "broken", tab_id: "t1" } } as DockTab;
  const restored = restoreTabs({ open: [bad] }, []);
  expect(restored.tabs[0]).toMatchObject({ id: bad.id, browserUnavailable: true, browserBinding: bad.browserBinding });
  const result = reconcileBrowserSheets(restored.tabs, "s1", state);
  expect(result[0]).toEqual(restored.tabs[0]);
  expect(result).toHaveLength(3);
});

it("observed newer authority fences a late takeover even when its new lease is owned elsewhere", () => {
  const sid = "other-window-control";
  installBrowserLease(sid, context, "t1", 1, "first-lease");
  observeBrowserControl(sid, { ...state, control: "human", control_epoch: 3 });
  expect(browserControlMemory(sid, context, "t1")).toMatchObject({ token: "", epoch: 3 });
  expect(installBrowserLease(sid, context, "t1", 2, "late-lease")).toBe(false);
  expect(browserControlMemory(sid, context, "t1").token).toBe("");
});

it("uses a localized title for an about:blank Page without changing its binding", () => {
  const blank = { ...state, active_tab: "t2", viewed_tab: "t2", tabs: [{ id: "t2", title: "about:blank", url: "about:blank" }] };
  const tabs = reconcileBrowserSheets([], "s1", blank, "新标签页");
  expect(tabs[0]).toMatchObject({ label: "新标签页", url: "about:blank", browserBinding: { tab_id: "t2" } });
});
