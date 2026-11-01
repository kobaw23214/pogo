import { describe, expect, it } from "vitest";
import { AGENT_MARK_HOLD_MS, browserAgentOverlayGeometry, browserAgentOverlayMatches, readBrowserAgentOverlay, type BrowserAgentOverlay } from "./browserAgentOverlay";
import type { BrowserLiveFrame } from "./browserLive";
import { nativeConfirmedClick, nativeViewportPoint, type NativeBrowserState } from "./nativeBrowser";

const target = { context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa", tab_id: "t1" };
const viewport = { width: 1280, height: 800 };
const state: NativeBrowserState = { ...target, backend: "native", control: "agent", url: "https://fixture.test", title: "Fixture", tabs: [], active_tab: "t1", viewed_tab: "t1", viewport, generation: 2, control_epoch: 3, document_version: 4, frame_identity: "2:t1:4" };
const frame: BrowserLiveFrame = { ...target, type: "media", viewport, pixel_width: 2560, pixel_height: 1600, frame_id: 1, media_epoch: "media-1", generation: 2, control_epoch: 3, document_version: 4, frame_identity: "2:t1:4" };
const marks: BrowserAgentOverlay = { ...target, type: "action_overlay", seq: 1, frame_id: 1, media_epoch: "media-1", viewport, generation: 2, control_epoch: 3, document_version: 4, frame_identity: "2:t1:4", overlay: {
  version: "page-v1", tab_id: "t1", viewport, sampled_at: 1005,
  last_action: { seq: 9, id: "op9", action: "click", actor: "agent", phase: "succeeded", label: "", tab_id: "t1", version: "page-v1", started_at: 990, updated_at: 1000 },
  pointer: { x: 320, y: 200, seq: 8, action_seq: 9, actor: "agent", kind: "click" },
  target: { rect: { x: 300, y: 180, width: 100, height: 40 } },
  focus: { rect: { x: 300, y: 180, width: 100, height: 40 }, editable: true },
} };
const changedOverlay = (update: Record<string, unknown>) => ({ ...marks, overlay: { ...marks.overlay, ...update } });

describe("Agent-only browser action feedback", () => {
  it("accepts actual same-page click feedback independently of DOM screenshot metadata", () => {
    const parsed = readBrowserAgentOverlay(marks, target)!;
    expect(parsed).toEqual(marks);
    expect(browserAgentOverlayMatches(parsed, frame, state, target)).toBe(true);
    expect(nativeConfirmedClick(parsed.overlay!)).toBe(true);
    expect(readBrowserAgentOverlay({ ...marks, overlay: null }, target)?.overlay).toBeNull();
  });
  it("strips private text, field values and page-supplied labels", () => {
    const input = changedOverlay({ password: "secret", text: "secret", focus: { ...marks.overlay!.focus, value: "secret" }, last_action: { ...marks.overlay!.last_action, label: "secret", text: "secret" } });
    expect(JSON.stringify(readBrowserAgentOverlay(input, target))).not.toContain("secret");
  });
  it("never displays a synthetic human pointer after an Agent takeover", () => {
    const parsed = readBrowserAgentOverlay(changedOverlay({ pointer: { ...marks.overlay!.pointer, actor: "human" } }), target)!;
    expect(parsed.overlay?.pointer).toBeUndefined();
    expect(browserAgentOverlayMatches(marks, frame, { ...state, control: "human" }, target)).toBe(false);
    expect(readBrowserAgentOverlay(changedOverlay({ last_action: { ...marks.overlay!.last_action, actor: "human" } }), target)?.overlay).toBeNull();
  });
  it.each(["snapshot", "find", "extract", "wait", "screenshot", "devtools"])("does not turn a %s operation into a fictitious mouse or focus", action => {
    expect(readBrowserAgentOverlay(changedOverlay({ last_action: { ...marks.overlay!.last_action, action } }), target)?.overlay).toBeNull();
  });
  it("drops pointers from a preceding action and focus after a scroll", () => {
    const parsed = readBrowserAgentOverlay(changedOverlay({ pointer: { ...marks.overlay!.pointer, action_seq: 8 } }), target)!;
    expect(parsed.overlay?.pointer).toBeUndefined();
    expect(parsed.overlay?.focus).toBeDefined();
    expect(readBrowserAgentOverlay(changedOverlay({ last_action: { ...marks.overlay!.last_action, action: "scroll" } }), target)?.overlay?.focus).toBeUndefined();
  });
  it("keeps actual pending typing focus but suppresses failed and old finished actions", () => {
    const pending = readBrowserAgentOverlay(changedOverlay({ sampled_at: 8000, last_action: { ...marks.overlay!.last_action, action: "type", phase: "started" } }), target)!;
    expect(pending.overlay?.focus).toBeDefined();
    expect(nativeConfirmedClick(pending.overlay!)).toBe(false);
    expect(readBrowserAgentOverlay(changedOverlay({ sampled_at: 1001 + AGENT_MARK_HOLD_MS }), target)?.overlay).toBeNull();
    expect(readBrowserAgentOverlay(changedOverlay({ last_action: { ...marks.overlay!.last_action, phase: "failed" } }), target)?.overlay?.pointer).toBeUndefined();
  });
  it.each([{ context_ref: "another-context" }, { tab_id: "t2" }, { seq: 0 }, { seq: 1.5 }, { generation: 0 }, { media_epoch: "bad epoch" }, { frame_identity: "" }, { document_version: -1 }, { viewport: { width: 0, height: 800 } }, { input_text: "private" }])("rejects malformed or cross-page metadata %j without making it geometry", update => {
    expect(readBrowserAgentOverlay({ ...marks, ...update }, target)).toBeNull();
  });
  it.each([{ generation: 3 }, { control_epoch: 4 }, { frame_identity: "2:t1:5" }, { document_version: 5 }, { viewed_tab: "t2" }, { context_ref: "other-context" }, { viewport: { width: 800, height: 1280 } }])("fences action markers when current state changes %j", update => {
    expect(browserAgentOverlayMatches(marks, frame, { ...state, ...update }, target)).toBe(false);
  });
  it("fences retired media epochs and preceding action receipts", () => {
    expect(browserAgentOverlayMatches(marks, { ...frame, media_epoch: "media-2" }, state, target)).toBe(false);
    expect(browserAgentOverlayMatches(marks, { ...frame, frame_id: 2 }, state, target)).toBe(false);
    expect(browserAgentOverlayMatches(marks, frame, { ...state, activity: { last_action: { ...marks.overlay!.last_action!, seq: 10 }, recent_actions: [] } }, target)).toBe(false);
  });
  it.each([{ width: 640, height: 600 }, { width: 1000, height: 400 }, { width: 375, height: 700 }, { width: 2560, height: 1600 }])("projects actual CSS coordinates through containment and display scaling %j", box => {
    const geometry = browserAgentOverlayGeometry(marks, box);
    expect(geometry.pointer).not.toBeNull();
    expect(nativeViewportPoint(geometry.pointer!.x, geometry.pointer!.y, { ...box, left: 0, top: 0 }, viewport)).toEqual({ x: 320, y: 200 });
    expect(geometry.target!.width).toBeGreaterThan(0);
    expect(geometry.focus).toEqual(geometry.target);
  });
});
