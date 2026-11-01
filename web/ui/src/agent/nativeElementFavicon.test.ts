import { describe, expect, it } from "vitest";
import { nativeElementPick, type NativeBrowserPick, type NativeBrowserState } from "./nativeBrowser";

const icon = "data:image/png;base64,AAAA";
const activeIcon = "data:image/png;base64,AQID";
const viewport = { width: 1280, height: 800 };
const url = "http://private.test:8929/project?account=a&view=one";
const pick: NativeBrowserPick = { tab_id: "t1", version: "v1", viewport, url, title: "Private project", element: { ref: "p1", tag: "button", role: "button", name: "Save", text: "Save", rect: { x: 10, y: 10, width: 100, height: 40 } } };
const observed: NativeBrowserState = { backend: "native", control: "human", active_tab: "t2", viewed_tab: "t1", version: "v1", url, title: pick.title, viewport, favicon: activeIcon, tabs: [{ id: "t1", url, title: pick.title, favicon: icon }, { id: "t2", url: "https://other.test/", title: "Other", favicon: activeIcon }] };

describe("native element reference favicon binding", () => {
  it("uses the viewed non-active tab's own icon and preserves its document URL", () => {
    const selected = nativeElementPick(pick, observed)!;
    expect(selected.icon).toBe(icon);
    expect(selected.pageURL).toBe(url);
    expect(selected.block).not.toContain(icon);
    expect(nativeElementPick(pick, { ...observed, active_tab: "t1" })!.icon).toBe(icon);
  });

  it("does not borrow top-level active metadata when the bound tab has no icon", () => {
    expect(nativeElementPick(pick)!.icon).toBeUndefined();
    expect(nativeElementPick(pick, { ...observed, tabs: observed.tabs.map(tab => tab.id === "t1" ? { ...tab, favicon: undefined } : tab) })!.icon).toBeUndefined();
  });

  it("rejects newer navigation, stale frame, wrong tab and viewport metadata", () => {
    for (const state of [
      { ...observed, url: "https://new.test/" },
      { ...observed, tabs: observed.tabs.map(tab => tab.id === "t1" ? { ...tab, url: `${url}&navigation=new`, favicon: activeIcon } : tab) },
      { ...observed, version: "v2" },
      { ...observed, viewed_tab: "t2" },
      { ...observed, viewport: { width: 800, height: 1280 } },
      { ...observed, tabs: [] },
    ]) expect(nativeElementPick(pick, state)!.icon).toBeUndefined();
  });

  it("refuses unsafe icon and document URLs, including iframe-site icon guesses", () => {
    expect(nativeElementPick({ ...pick, element: { ...pick.element!, frame_url: "https://embedded.test/" } }, observed)!.icon).toBe(icon);
    for (const favicon of ["http://private.test/favicon.ico", "javascript:alert(1)", "data:text/html;base64,AAAA", "data:image/png;base64," + "A".repeat(90000)]) expect(nativeElementPick(pick, { ...observed, tabs: [{ ...observed.tabs[0], favicon }] })!.icon).toBeUndefined();
    const badURL = "javascript:alert(1)";
    expect(nativeElementPick({ ...pick, url: badURL }, { ...observed, url: badURL, tabs: [{ ...observed.tabs[0], url: badURL }] })!.icon).toBeUndefined();
  });

  it("freezes the viewed document icon when the verified selection frame has a newer visual revision", () => {
    const state = { ...observed, context_ref: "ctx_1234567890abcdef12345678", frame_identity: "1:t1:4", generation: 1 };
    const selected = { ...pick, version: "sampled-frame" };
    const frame = { context_ref: state.context_ref, tab_id: "t1", version: selected.version, identity: state.frame_identity, viewport };
    expect(nativeElementPick(selected, state)!.icon).toBeUndefined();
    expect(nativeElementPick(selected, state, frame)!.icon).toBe(icon);
    for (const invalid of [
      { ...frame, context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa" },
      { ...frame, tab_id: "t2" },
      { ...frame, identity: "1:t1:5" },
      { ...frame, identity: "" },
      { ...frame, version: "different-frame" },
      { ...frame, viewport: { width: 800, height: 1280 } },
    ]) expect(nativeElementPick(selected, state, invalid)!.icon).toBeUndefined();
    for (const changed of [
      { ...state, frame_identity: "1:t1:5" }, // Same URL, different document after reload.
      { ...state, frame_identity: "invalid" },
      { ...state, generation: 2 },
      { ...state, context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa" },
      { ...state, viewed_tab: "t2" },
      { ...state, url: `${url}&next=1` },
      { ...state, viewport: { width: 800, height: 1280 } },
    ]) expect(nativeElementPick(selected, changed, frame)!.icon).toBeUndefined();
  });

  it("never falls back to a matching state version when an explicit frame binding is wrong", () => {
    const state = { ...observed, context_ref: "ctx_1234567890abcdef12345678", frame_identity: "1:t1:4" };
    expect(nativeElementPick(pick, state)!.icon).toBe(icon);
    expect(nativeElementPick(pick, state, { context_ref: state.context_ref, tab_id: "t1", version: pick.version, identity: "1:t1:5", viewport })!.icon).toBeUndefined();
  });
});
