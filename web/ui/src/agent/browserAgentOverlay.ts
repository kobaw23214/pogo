import { readNativeOverlay, nativeDisplayPoint, nativeDisplayRect, type NativeBrowserOverlay, type NativeBrowserState, type NativeBrowserTarget, type NativeBrowserViewport } from "./nativeBrowser";
import type { BrowserLiveFrame } from "./browserLive";

/** Action feedback travels beside video. It never authorizes DOM or input. */
export interface BrowserAgentOverlay {
  type: "action_overlay"; seq: number; context_ref: string; tab_id: string;
  generation: number; control_epoch: number; frame_identity: string;
  document_version: number; viewport: NativeBrowserViewport;
  media_epoch: string; frame_id: number; overlay: NativeBrowserOverlay | null;
}
const fields = ["type", "seq", "context_ref", "tab_id", "generation", "control_epoch", "frame_identity", "document_version", "viewport", "media_epoch", "frame_id", "overlay"];
const spatialActions = new Set(["click", "hover", "drag", "type", "text", "input_text", "press", "key", "select", "scroll", "input_scroll", "upload", "upload_file"]);
const focusActions = new Set(["click", "type", "text", "input_text", "press", "key", "select"]);
export const AGENT_MARK_HOLD_MS = 1800;
export const AGENT_PENDING_MARK_MS = 10000;
function record(value: unknown): value is Record<string, unknown> { return !!value && typeof value === "object" && !Array.isArray(value); }
function integer(value: unknown, minimum = 0): value is number { return Number.isSafeInteger(value) && Number(value) >= minimum; }

export function readBrowserAgentOverlay(value: unknown, target: NativeBrowserTarget): BrowserAgentOverlay | null {
  if (!record(value) || Object.keys(value).some(key => !fields.includes(key)) || value.type !== "action_overlay" ||
      value.context_ref !== target.context_ref || value.tab_id !== target.tab_id || !integer(value.seq, 1) ||
      !integer(value.generation, 1) || !integer(value.control_epoch) || !integer(value.document_version) || !integer(value.frame_id, 1) ||
      typeof value.media_epoch !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.media_epoch) ||
      typeof value.frame_identity !== "string" || !value.frame_identity || value.frame_identity.length > 256 ||
      !record(value.viewport) || Object.keys(value.viewport).some(key => !["width", "height"].includes(key)) ||
      !integer(value.viewport.width, 1) || !integer(value.viewport.height, 1) || value.viewport.width > 8192 || value.viewport.height > 8192) return null;
  let overlay: NativeBrowserOverlay | null = null;
  if (value.overlay !== null) {
    if (!record(value.overlay) || typeof value.overlay.version !== "string" || !value.overlay.version || value.overlay.version.length > 256) return null;
    overlay = readNativeOverlay(value.overlay, value.overlay.version);
    if (!overlay || overlay.tab_id !== target.tab_id || overlay.viewport.width !== value.viewport.width || overlay.viewport.height !== value.viewport.height || !integer(overlay.sampled_at)) return null;
    const action = overlay.last_action;
    // Reading/extracting a page must never invent a mouse trajectory. A
    // previous human's pointer/focus must not reappear after agent takeover.
    if (!action || action.actor !== "agent" || action.tab_id !== target.tab_id || !integer(action.seq, 1) ||
        !integer(action.started_at) || !integer(action.updated_at) || !spatialActions.has(action.action)) overlay = null;
    else {
      const pointer = overlay.pointer;
      if (pointer?.actor !== "agent" || pointer.action_seq !== action.seq || !integer(pointer.seq, 1)) overlay.pointer = undefined;
      if (!focusActions.has(action.action)) overlay.focus = undefined;
      if (action.phase === "failed") { overlay.pointer = undefined; overlay.target = undefined; overlay.focus = undefined; }
      const age = overlay.sampled_at - action.updated_at;
      if (age < 0 || action.phase !== "started" && age > AGENT_MARK_HOLD_MS) overlay = null;
    }
  }
  return { type: "action_overlay", seq: value.seq, context_ref: target.context_ref, tab_id: target.tab_id,
    generation: value.generation, control_epoch: value.control_epoch, document_version: value.document_version,
    frame_identity: value.frame_identity, viewport: { width: value.viewport.width, height: value.viewport.height },
    media_epoch: value.media_epoch, frame_id: value.frame_id, overlay };
}

export function browserAgentOverlayMatches(marks: BrowserAgentOverlay | null, frame: BrowserLiveFrame | undefined, state: NativeBrowserState, target: NativeBrowserTarget): marks is BrowserAgentOverlay {
  const action = state.activity?.last_action;
  return !!marks && !!frame && state.control === "agent" && !state.dialog && !state.debugger_paused && !state.devtools_blocked && marks.context_ref === target.context_ref && marks.tab_id === target.tab_id &&
    state.context_ref === target.context_ref && (state.viewed_tab ?? state.active_tab) === target.tab_id &&
    marks.generation === state.generation && marks.control_epoch === state.control_epoch && marks.frame_identity === state.frame_identity &&
    marks.document_version === state.document_version && marks.media_epoch === frame.media_epoch && marks.frame_id === frame.frame_id &&
    marks.generation === frame.generation && marks.control_epoch === frame.control_epoch && marks.frame_identity === frame.frame_identity &&
    marks.viewport.width === state.viewport.width && marks.viewport.height === state.viewport.height &&
    marks.viewport.width === frame.viewport.width && marks.viewport.height === frame.viewport.height &&
    (!marks.overlay || !action || action.actor === "agent" && action.tab_id === target.tab_id && action.seq === marks.overlay.last_action?.seq);
}

/** Both coordinate mapping and painting use the video element's contain box. */
export function browserAgentOverlayGeometry(marks: BrowserAgentOverlay | null, size: NativeBrowserViewport) {
  const overlay = marks?.overlay;
  return {
    pointer: overlay?.pointer ? nativeDisplayPoint(overlay.pointer, size, marks!.viewport) : null,
    target: overlay?.target ? nativeDisplayRect(overlay.target.rect, size, marks!.viewport) : null,
    focus: overlay?.focus ? nativeDisplayRect(overlay.focus.rect, size, marks!.viewport) : null,
  };
}
