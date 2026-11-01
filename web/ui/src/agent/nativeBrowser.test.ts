import { describe, expect, it } from "vitest";
import { decodeNativeOverlay, nativeConfirmedClick, nativeContentRect, nativeDisplayPoint, nativeDisplayRect, nativeDocumentStamp, nativeDocumentUnavailable, nativeElementPick, nativeFrameMatches, nativeKey, nativeLeaseRejected, nativeNavigationURL, nativeNewTabPage, nativeOverlayMatches, nativePickMatches, nativePageLoading, nativeReadFailure, nativeRefreshDelay, nativeViewportPoint, ownsNativeControl, type NativeBrowserOverlay, type NativeBrowserPick, type NativeBrowserState } from "./nativeBrowser";
import { composePickedMessage, parsePageElementBlocks } from "./elementPicker";

const viewport = { width: 1280, height: 800 };
describe("read-only native element picks", () => {
  const pick: NativeBrowserPick = {
    version: "frame-1", tab_id: "tab-1", viewport, title: "测试页面", url: "https://site.example/form",
    element: { ref: "p4", tag: "button", role: "button", name: "提交", text: "提交表单", rect: { x: 120, y: 200, width: 100, height: 40 } },
  };
  const frame = { version: pick.version, tab_id: pick.tab_id, viewport };
  it("accepts only the exact visible frame, tab and viewport including a valid empty hit", () => {
    expect(nativePickMatches(pick, frame)).toBe(true);
    expect(nativePickMatches({ ...pick, element: null }, frame)).toBe(true);
    for (const changed of [{ version: "frame-2" }, { tab_id: "tab-2" }, { viewport: { width: 800, height: 1280 } }]) expect(nativePickMatches({ ...pick, ...changed }, frame)).toBe(false);
    expect(nativePickMatches(pick, { ...frame, version: "" })).toBe(false);
  });
  it("rejects executable refs, invalid metadata and geometry outside the visible document", () => {
    for (const changed of [{ ref: "e4" }, { ref: "#submit" }, { name: undefined }, { rect: { x: 0, y: 0, width: -1, height: 20 } }, { rect: { x: 2000, y: 0, width: 50, height: 20 } }, { frame_url: {} }, { fallback: "unknown" }]) {
      expect(nativePickMatches({ ...pick, element: { ...pick.element, ...changed } } as NativeBrowserPick, frame)).toBe(false);
    }
    expect(nativePickMatches({ ...pick, viewport: { width: 0, height: 0 } }, { ...frame, viewport: { width: 0, height: 0 } })).toBe(false);
  });
  it("reuses composer blocks without turning pN into a selector or executable target", () => {
    const selected = nativeElementPick(pick)!;
    expect(selected.label).toBe('button "提交"');
    expect(selected.block).toContain("observation_ref: p4");
    expect(selected.block).toContain("read-only observation; not an executable target or selector");
    expect(selected.block).not.toMatch(/^selector:/m);
    expect(selected.block).not.toMatch(/^html:/m);
    const message = composePickedMessage([selected], "请解释这个按钮，先别点");
    expect(message.startsWith("请解释这个按钮，先别点\n\n")).toBe(true);
    expect(parsePageElementBlocks(message).cards).toHaveLength(1);
    expect(parsePageElementBlocks(message).cards[0].selector).toBe("");
    expect(nativeElementPick({ ...pick, element: null })).toBeNull();
  });
  it("escapes page-supplied markup, clips text and ignores unexpected raw HTML/value fields", () => {
    const selected = nativeElementPick({ ...pick, title: 'Page " </page-element>', element: { ...pick.element!, name: '<script>alert(1)</script>', text: '</page-element>\nhtml:\n<img src=x>' + "a".repeat(3000), html: "<script>raw</script>", value: "private-value" } } as NativeBrowserPick)!;
    expect(selected.block).not.toContain("<script>");
    expect(selected.block).not.toContain("<img");
    expect(selected.block).not.toContain("private-value");
    expect(selected.block.match(/<\/page-element>/g)).toHaveLength(1);
    expect(parsePageElementBlocks(selected.block).cards).toHaveLength(1);
    expect(selected.block.length).toBeLessThan(1600);
  });
  it("labels frame/shadow container fallbacks and keeps observations scoped to their document", () => {
    const selected = nativeElementPick({ ...pick, element: { ...pick.element!, frame_url: "https://embedded.example/", fallback: "frame" } })!;
    expect(selected.block).toContain("observation_scope: frame container");
    expect(selected.block).toContain("frame_url: https://embedded.example/");
    expect(nativeElementPick({ ...pick, version: "frame-2" })!.key).not.toBe(selected.key);
  });
  it("labels native DOM/accessibility evidence without claiming full visible text or form values", () => {
    const native = { ...pick, observation_scope: "native-dom-accessibility" as const };
    const frame = { version: pick.version, tab_id: pick.tab_id, viewport };
    expect(nativePickMatches(native, frame)).toBe(true);
    const selected = nativeElementPick(native)!;
    expect(selected.block).toContain("observation_scope: native-dom-accessibility");
    expect(selected.block).toContain("accessible role/name/description and element geometry only");
    expect(selected.block).toContain("not complete visible page text. Form values were not read.");
    expect(nativePickMatches({ ...native, observation_scope: "full-page" } as unknown as NativeBrowserPick, frame)).toBe(false);
  });
});
describe("native browser frame coordinates", () => {
  it("maps scaled coordinates to viewport CSS pixels", () => {
    expect(nativeViewportPoint(330, 220, { left: 10, top: 20, width: 640, height: 400 }, viewport)).toEqual({ x: 640, y: 400 });
  });
  it("subtracts vertical letterboxing and rejects the bars", () => {
    const rect = { left: 20, top: 30, width: 640, height: 600 };
    expect(nativeViewportPoint(340, 330, rect, viewport)).toEqual({ x: 640, y: 400 });
    expect(nativeViewportPoint(340, 100, rect, viewport)).toBeNull();
    expect(nativeViewportPoint(340, 600, rect, viewport)).toBeNull();
  });
  it("subtracts horizontal letterboxing and rejects outside coordinates", () => {
    const rect = { left: 10, top: 20, width: 1000, height: 400 };
    expect(nativeViewportPoint(510, 220, rect, viewport)).toEqual({ x: 640, y: 400 });
    expect(nativeViewportPoint(100, 220, rect, viewport)).toBeNull();
    expect(nativeViewportPoint(1001, 220, rect, viewport)).toBeNull();
  });
  it("rejects unmeasured or invalid frames and clamps rounding within the viewport", () => {
    expect(nativeViewportPoint(0, 0, { left: 0, top: 0, width: 0, height: 0 }, viewport)).toBeNull();
    expect(nativeViewportPoint(NaN, 0, { left: 0, top: 0, width: 640, height: 400 }, viewport)).toBeNull();
    expect(nativeViewportPoint(639.9, 399.9, { left: 0, top: 0, width: 640, height: 400 }, viewport)).toEqual({ x: 1279, y: 799 });
  });
});

describe("frame-bound action overlays", () => {
  const marks: NativeBrowserOverlay = {
    version: "v1", tab_id: "tab1", viewport, sampled_at: 1005,
    last_action: { seq: 4, id: "op4", actor: "agent", action: "click", phase: "succeeded", label: "Click", tab_id: "tab1", version: "v1", started_at: 990, updated_at: 1000 },
    pointer: { x: 320, y: 200, seq: 9, action_seq: 4, kind: "click", actor: "agent" },
    target: { rect: { x: 300, y: 180, width: 100, height: 40 } },
    focus: { rect: { x: 300, y: 180, width: 100, height: 40 }, editable: true },
  };
  const encode = (value: unknown) => btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(value)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

  it("maps geometry back onto landscape and portrait viewers with the same letterboxing as hit testing", () => {
    for (const box of [{ width: 640, height: 600 }, { width: 1000, height: 400 }, { width: 375, height: 700 }]) {
      const display = nativeDisplayPoint({ x: 320, y: 200 }, box, viewport)!;
      expect(nativeViewportPoint(display.x + 15, display.y + 25, { ...box, left: 15, top: 25 }, viewport)).toEqual({ x: 320, y: 200 });
    }
    expect(nativeContentRect({ width: 640, height: 600 }, viewport)).toEqual({ x: 0, y: 100, width: 640, height: 400, scale: .5 });
    expect(nativeDisplayPoint({ x: 320, y: 200 }, { width: 640, height: 600 }, viewport)).toEqual({ x: 160, y: 200 });
  });
  it("clips partial focus/target rectangles to the page and never paints in black bars", () => {
    expect(nativeDisplayRect({ x: -20, y: 760, width: 100, height: 100 }, { width: 640, height: 600 }, viewport)).toEqual({ x: 0, y: 480, width: 40, height: 20 });
    expect(nativeDisplayRect({ x: 1300, y: 0, width: 50, height: 50 }, { width: 640, height: 600 }, viewport)).toBeNull();
    expect(nativeDisplayRect({ x: 0, y: 0, width: Infinity, height: 50 }, { width: 640, height: 600 }, viewport)).toBeNull();
    expect(nativeDisplayPoint({ x: 1280, y: 100 }, { width: 640, height: 600 }, viewport)).toBeNull();
    expect(nativeContentRect({ width: 0, height: 600 }, viewport)).toBeNull();
  });
  it("decodes UTF-8 base64url metadata and strips text values and arbitrary labels", () => {
    const decoded = decodeNativeOverlay(encode({ ...marks, last_action: { ...marks.last_action, label: "密码: 不应显示", text: "private" }, focus: { ...marks.focus, value: "private", password: "private", name: "private" } }), "v1")!;
    expect(decoded.pointer).toEqual(marks.pointer);
    expect(decoded.last_action?.label).toBe("");
    expect(decoded.focus).toEqual(marks.focus);
    expect(JSON.stringify(decoded)).not.toContain("private");
    expect(JSON.stringify(decoded)).not.toContain("密码");
  });
  it("requires the image version and current tab to match; invalid headers don't become geometry", () => {
    const decoded = decodeNativeOverlay(encode(marks), "v1");
    expect(nativeOverlayMatches(decoded, "v1", "tab1")).toBe(true);
    expect(nativeOverlayMatches(decoded, "v2", "tab1")).toBe(false);
    expect(nativeOverlayMatches(decoded, "v1", "tab2")).toBe(false);
    for (const value of [null, "bad-json", "A".repeat(40000), encode({ ...marks, viewport: { width: 0, height: 800 } }), encode({ ...marks, version: "old" })]) expect(decodeNativeOverlay(value, "v1")).toBeNull();
  });
  it("drops invalid points or rectangles without losing valid focus metadata", () => {
    const decoded = decodeNativeOverlay(encode({ ...marks, pointer: { ...marks.pointer, x: -1 }, target: { rect: { x: 0, y: 0, width: -1, height: 20 } } }), "v1")!;
    expect(decoded.pointer).toBeUndefined();
    expect(decoded.target).toBeUndefined();
    expect(decoded.focus).toEqual(marks.focus);
  });
  it("shows a successful click only with matching actual pointer evidence and a successful click receipt", () => {
    expect(nativeConfirmedClick(marks)).toBe(true);
    for (const phase of ["started", "failed"] as const) expect(nativeConfirmedClick({ ...marks, last_action: { ...marks.last_action!, phase } })).toBe(false);
    expect(nativeConfirmedClick({ ...marks, pointer: { ...marks.pointer!, action_seq: 3 } })).toBe(false);
    expect(nativeConfirmedClick({ ...marks, pointer: { ...marks.pointer!, actor: "human" } })).toBe(false);
    expect(nativeConfirmedClick({ ...marks, pointer: { ...marks.pointer!, kind: "move" } })).toBe(false);
    expect(nativeConfirmedClick({ ...marks, last_action: undefined })).toBe(false);
    expect(nativeConfirmedClick({ ...marks, last_action: { ...marks.last_action!, action: "type" } })).toBe(false);
  });
  it("bounds active refresh frequency and backs off when idle or disconnected", () => {
    expect(nativeRefreshDelay(true, false)).toBe(350);
    expect(nativeRefreshDelay(false, false)).toBe(2000);
    expect(nativeRefreshDelay(true, true)).toBe(3000);
    expect(nativeRefreshDelay(false, true)).toBe(3000);
  });
});

describe("native browser control and IME", () => {
  const state = { backend: "native", control: "human" } as NativeBrowserState;
  it("requires both human mode and a local lease; an observing window cannot input", () => {
    expect(ownsNativeControl(state, "")).toBe(false);
    expect(ownsNativeControl(state, "lease")).toBe(true);
    expect(ownsNativeControl({ ...state, control: "agent" }, "old-lease")).toBe(false);
    expect(ownsNativeControl(null, "lease")).toBe(false);
    expect(ownsNativeControl({ ...state, control_epoch: 4 }, "lease", 3)).toBe(false);
    expect(ownsNativeControl({ ...state, control_epoch: 4 }, "lease", 4)).toBe(true);
  });
  it("does not discard a valid lease for an outdated screenshot or dialog conflict", () => {
    expect(nativeLeaseRejected(409)).toBe(false);
    expect(nativeLeaseRejected(500)).toBe(false);
    expect(nativeLeaseRejected(403)).toBe(true);
    expect(nativeLeaseRejected(401)).toBe(true);
  });
  it("does not forward IME composition, its confirming Enter, or printable text", () => {
    for (const event of [{ key: "Enter", isComposing: true }, { key: "Enter", keyCode: 229 }, { key: "Process" }, { key: "你" }, { key: "a" }]) expect(nativeKey(event)).toBeNull();
  });
  it("forwards editing/navigation keys and explicit shortcuts in Playwright format", () => {
    expect(nativeKey({ key: "Enter" })).toBe("Enter");
    expect(nativeKey({ key: "Tab", shiftKey: true })).toBe("Shift+Tab");
    expect(nativeKey({ key: "a", ctrlKey: true })).toBe("Control+a");
    expect(nativeKey({ key: "ArrowLeft", metaKey: true })).toBe("Meta+ArrowLeft");
    expect(nativeKey({ key: "Control", ctrlKey: true })).toBeNull();
  });
});

describe("native navigation and existing shortcuts", () => {
  const origin = "https://pogo.example";
  it("uses real destinations and resolves relative paths against the remote page", () => {
    expect(nativeNavigationURL("intranet.example:8080/app", "", origin)).toBe("http://intranet.example:8080/app");
    expect(nativeNavigationURL("/detail?id=1", "https://site.example/app", origin)).toBe("https://site.example/detail?id=1");
    expect(nativeNavigationURL("/panels/app", "https://site.example/app", origin)).toBe("https://site.example/panels/app");
  });
  it("rejects retired proxy paths during ordinary navigation", () => {
    expect(nativeNavigationURL("/panels/样本库/#/detail", "", origin)).toBeNull();
    expect(nativeNavigationURL("https://pogo.example/panels/site/app?q=1#x", "", origin)).toBeNull();
    expect(nativeNavigationURL("https://other.example/panels/site/", "", origin)).toBe("https://other.example/panels/site/");
  });
  it("refuses executable schemes and credentials embedded in URLs", () => {
    for (const value of ["javascript:alert(1)", "file:///etc/passwd", "data:text/html,a", "https://user:pass@site.example/", ""]) expect(nativeNavigationURL(value, "", origin)).toBeNull();
  });
});

describe("native browser state and frame availability", () => {
  it("classifies frame version conflicts as changing, not a disconnected browser", () => {
    expect(nativeReadFailure("frame", 409)).toBe("changing");
    expect(nativeReadFailure("state", 409)).toBe("connection");
  });
  it("keeps screenshot failures separate from state connection failures", () => {
    for (const status of [undefined, 401, 403, 404, 500, 502, 503, 504]) {
      expect(nativeReadFailure("frame", status)).toBe("unavailable");
      expect(nativeReadFailure("state", status)).toBe("connection");
    }
  });
});


describe("browser loading and frame identity", () => {
  const state: NativeBrowserState = { backend: "native", control: "agent", url: "https://example.invalid", title: "Fixture", tabs: [], active_tab: "t1", viewport, generation: 1, frame_identity: "1:t1:1" };
  it("accepts frames only for the observed and still-current document", () => {
    expect(nativeFrameMatches("1:t1:1", state, state)).toBe(true);
    for (const identity of ["", "1:t1:2", "1:t2:1", "2:t1:1"]) expect(nativeFrameMatches(identity, state, state)).toBe(false);
    expect(nativeFrameMatches("1:t1:1", state, { ...state, frame_identity: "1:t1:2" })).toBe(false);
    expect(nativeFrameMatches("1:t1:1", { ...state, frame_identity: "1:t1:2" }, state)).toBe(false);
    expect(nativeFrameMatches("1:t1:1", state, { ...state, active_tab: "t2" })).toBe(false);
    expect(nativeFrameMatches("1:t1:1", state, { ...state, generation: 2 })).toBe(false);
    expect(nativeFrameMatches("1:t1:1", { ...state, frame_identity: undefined }, state)).toBe(false);
  });
  it("distinguishes navigation and first-frame loading from ordinary agent work", () => {
    expect(nativePageLoading(state, true, false, false)).toBe(true);
    expect(nativePageLoading(state, false, true, false)).toBe(true);
    expect(nativePageLoading({ ...state, page_loading: true }, false, false, false)).toBe(true);
    expect(nativePageLoading({ ...state, busy: true }, false, false, false)).toBe(false);
    expect(nativePageLoading({ ...state, url: "about:blank" }, true, false, false)).toBe(false);
    expect(nativePageLoading({ ...state, url: "" }, true, false, false)).toBe(false);
  });
  it("stops loading animation on definite failure and blocked pages", () => {
    expect(nativePageLoading({ ...state, page_loading: true }, true, true, true)).toBe(false);
    for (const patch of [{ page_load_error: "Page navigation failed." }, { debugger_paused: true }, { devtools_blocked: true }, { dialog: { type: "alert", message: "Wait" } }]) {
      expect(nativePageLoading({ ...state, ...patch, page_loading: true }, true, true, false)).toBe(false);
    }
  });
});

describe("local browser new-tab view", () => {
  const blank: NativeBrowserState = { backend: "native", control: "agent", url: "about:blank", title: "", tabs: [], active_tab: "t1", viewed_tab: "t1", context_ref: "ctx_111111111111111111111111", viewport };
  it("shows only the exact idle blank URL, not a loading or failed website", () => {
    expect(nativeNewTabPage(blank)).toBe(true);
    for (const url of ["", "https://example.invalid/", "chrome-error://chromewebdata/", "about:blank#content"]) expect(nativeNewTabPage({ ...blank, url })).toBe(false);
    for (const patch of [{ page_loading: true }, { page_load_error: "Page navigation failed." }, { debugger_paused: true }, { devtools_blocked: true }, { dialog: { type: "alert", message: "pending" } }]) expect(nativeNewTabPage({ ...blank, ...patch })).toBe(false);
  });
  it("does not confuse generic agent reads with an in-progress page navigation", () => {
    expect(nativeNewTabPage({ ...blank, busy: true })).toBe(true);
    const action = { id: "a", seq: 1, actor: "agent" as const, action: "navigate", phase: "started" as const, tab_id: "t1", version: "v", label: "", started_at: 1, updated_at: 1 };
    expect(nativeNewTabPage({ ...blank, activity: { last_action: action, recent_actions: [] } })).toBe(false);
    expect(nativeNewTabPage({ ...blank, activity: { last_action: { ...action, action: "snapshot" }, recent_actions: [] } })).toBe(true);
    expect(nativeNewTabPage({ ...blank, activity: { last_action: { ...action, tab_id: "t2" }, recent_actions: [] } })).toBe(true);
  });
});

it("binds bookmark follow-ups to the document while permitting only a control generation change", () => {
  const state: NativeBrowserState = { backend: "native", control: "agent", url: "about:blank", title: "", tabs: [], active_tab: "t1", viewed_tab: "t1", context_ref: "ctx_111111111111111111111111", frame_identity: "1:t1:7", viewport };
  const stamp = nativeDocumentStamp(state);
  expect(stamp).not.toBeNull();
  expect(nativeDocumentStamp({ ...state, generation: 2, frame_identity: "2:t1:7" })).toBe(stamp);
  expect(nativeDocumentStamp({ ...state, frame_identity: "2:t1:8" })).not.toBe(stamp);
  expect(nativeDocumentStamp({ ...state, context_ref: "ctx_222222222222222222222222" })).not.toBe(stamp);
  for (const patch of [{ frame_identity: undefined }, { frame_identity: "invalid" }, { viewed_tab: "t2" }, { context_ref: undefined }]) expect(nativeDocumentStamp({ ...state, ...patch })).toBeNull();
});

it("applies hostname validation to direct and relative native navigation", () => {
  expect(nativeNavigationURL("http://www,example.com/", "https://valid.example/", "https://pogo.example")).toBeNull();
  expect(nativeNavigationURL("/next", "http://www,example.com/", "https://pogo.example")).toBeNull();
  expect(nativeNavigationURL("http://[::1]:8080/", "chrome-error://chromewebdata/", "https://pogo.example")).toBe("http://[::1]:8080/");
});
it("blocks navigation-failure and internal-error documents without classifying normal HTTP error pages as browser failures", () => {
  const page: NativeBrowserState = { backend: "native", control: "human", url: "https://site.example/503", title: "Service unavailable", tabs: [], active_tab: "t1", viewport };
  expect(nativeDocumentUnavailable(page)).toBe(false);
  expect(nativeDocumentUnavailable({ ...page, url: "https://site.example/404", title: "Not found" })).toBe(false);
  expect(nativeDocumentUnavailable({ ...page, page_load_error: "Page navigation failed." })).toBe(true);
  expect(nativeDocumentUnavailable({ ...page, url: "chrome-error://chromewebdata/" })).toBe(true);
  expect(nativeDocumentUnavailable({ ...page, url: "about:blank" })).toBe(false);
});
