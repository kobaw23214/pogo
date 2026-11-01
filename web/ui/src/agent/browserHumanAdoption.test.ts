import { describe, expect, it } from "vitest";
import { BrowserHumanAdoptions } from "./browserHumanAdoption";
import type { NativeBrowserState } from "./nativeBrowser";
const context = "ctx_123456789012345678901234", target = { context_ref: context, tab_id: "t2" };
const state: NativeBrowserState = { backend: "native", context_ref: context, active_tab: "t1", viewed_tab: "t2", generation: 1,
  control: "human", control_epoch: 0, url: "https://reading.invalid/", title: "Reading", viewport: { width: 1280, height: 800 },
  tabs: [{ id: "t1", url: "about:blank", title: "", control: "agent", control_epoch: 0 }, { id: "t2", url: "https://reading.invalid/", title: "Reading", control: "human", control_epoch: 0 }] };
const view = { sessionId: "owned", target, state, visible: true, connected: true, busy: false, lease: "" };
describe("initializing an unowned human browser Page", () => {
  it("requests one exact generation-bound initialization without forcing or claiming an Agent Page", () => {
    const adoption = new BrowserHumanAdoptions();
    expect(adoption.request(view)).toEqual({ adopt_if_unowned: true, generation: 1, control_epoch: 0 });
    expect(adoption.request(view)).toBeNull();
    expect(adoption.request({ ...view, busy: true })).toBeNull();
    expect(adoption.request(view)).toBeNull();
  });
  it("requires a visible, current, idle bound observation and never adopts a known owner", () => {
    for (const change of [{ visible: false }, { connected: false }, { busy: true }, { lease: "existing-human-lease" },
      { knownEpoch: 1 }, { target: { ...target, tab_id: "t1" } }, { sessionId: "" },
      ...[{ control: "agent" as const }, { control_epoch: 1 }, { busy: true }, { generation: undefined }, { generation: 0 },
        { generation: 1.5 }, { dialog: { type: "confirm", message: "Resolve first" } }, { debugger_paused: true }, { devtools_blocked: true }, { reset: true },
        { context_ref: "ctx_999999999999999999999999" }].map(change => ({ state: { ...state, ...change } }))]) {
      expect(new BrowserHumanAdoptions().request({ ...view, ...change })).toBeNull();
    }
  });
  it("keeps observation stamps separate by Page, session and generation without retrying a lost old request", () => {
    const adoption = new BrowserHumanAdoptions();
    expect(adoption.request(view)).not.toBeNull();
    expect(adoption.request({ ...view, state: { ...state, generation: 2 } })).toEqual({ adopt_if_unowned: true, generation: 2, control_epoch: 0 });
    expect(adoption.request(view)).toBeNull();
    expect(adoption.request({ ...view, sessionId: "another-owned-session" })).not.toBeNull();
    expect(adoption.request({ ...view, target: { ...target, tab_id: "t3" }, state: { ...state, viewed_tab: "t3" } })).not.toBeNull();
  });
});
