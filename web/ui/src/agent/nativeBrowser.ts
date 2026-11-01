import { normalizeUrl } from "./browserUrl";
import { chipLabel, clipText, formatElementBlock, type ElementPick } from "./elementPicker";
import { browserFavicon } from "./browserFavicon";
import { citeFaviconURL } from "./citeFavicon";

export interface BrowserDownload {
  id: string;
  filename: string;
  state: "in_progress" | "completed" | "saved" | "failed";
  path?: string;
  received_bytes: number;
  total_bytes: number;
  size?: number;
  error?: string;
  error_code?: string;
  limit_bytes?: number;
  actual_bytes?: number;
  created_at: string;
}

export interface NativeBrowserState {
  backend: "native";
  /** Desktop sessions only expose their human surface in the trusted app shell. */
  display_mode?: "desktop";
  control: "agent" | "human";
  url: string;
  title: string;
  favicon?: string;
  tabs: { id: string; url: string; title: string; favicon?: string; control?: "agent" | "human"; control_epoch?: number;
    /** Nonsecret lineage; usable only with this window's exact opener lease. */
    opener_tab?: string; inherited_control_epoch?: number }[];
  active_tab: string;
  context_ref?: string;
  viewed_tab?: string;
  viewport: { width: number; height: number };
  dialog?: { type: string; message: string; default_value?: string };
  generation?: number;
  document_version?: number;
  version?: string;
  frame_identity?: string;
  page_loading?: boolean;
  page_load_error?: string;
  can_go_back?: boolean;
  control_epoch?: number;
  busy?: boolean;
  /** A read-only element observation still owns its underlying browser read. */
  pick_pending?: boolean;
  debugger_paused?: boolean;
  devtools_blocked?: boolean;
  reset?: boolean;
  reset_reason?: string;
  activity?: { last_action: NativeBrowserActivity | null; recent_actions: NativeBrowserActivity[] };
  /** Host-imported files; live video state may omit this session-level cache. */
  downloads?: BrowserDownload[];
  downloads_rejected_count?: number;
  downloads_last_rejection?: string;
  downloads_max_file_bytes?: number;
}

export interface NativeBrowserActivity {
  seq: number;
  id: string;
  actor: "agent" | "human";
  action: string;
  phase: "started" | "succeeded" | "failed";
  label: string;
  tab_id: string;
  version: string;
  started_at: number;
  updated_at: number;
}

export interface NativeBrowserOverlay {
  version: string;
  tab_id: string;
  viewport: NativeBrowserViewport;
  sampled_at: number;
  last_action?: NativeBrowserActivity;
  pointer?: { x: number; y: number; seq: number; action_seq: number; kind: "move" | "click"; actor: "agent" | "human" };
  target?: { rect: NativeBrowserRect };
  focus?: { rect: NativeBrowserRect; editable: boolean };
}

export type BrowserBackendState = NativeBrowserState | { backend: "unavailable"; error: string };
export interface NativeBrowserTarget { tab_id: string; context_ref: string }
export function nativeViewedTab(state: NativeBrowserState): string { return state.viewed_tab ?? state.active_tab; }
export type NativeBrowserControl = NativeBrowserState & { lease_token?: string };

// The screenshot must belong to both the state that requested it and the
// current viewer state. A version alone does not identify a tab/document.
export function nativeFrameMatches(identity: string, observed: NativeBrowserState, current: NativeBrowserState): boolean {
  return !!identity && identity === observed.frame_identity && identity === current.frame_identity && nativeViewedTab(observed) === nativeViewedTab(current) && observed.context_ref === current.context_ref && observed.generation === current.generation;
}

/** Stable document binding across a control handoff, which advances only
 * generation. Missing or mismatched identities never authorize a follow-up. */
export function nativeDocumentStamp(state: NativeBrowserState): string | null {
  const identity = state.frame_identity?.match(/^([1-9]\d*):(t[1-9]\d*):([1-9]\d*)$/);
  if (!/^ctx_[a-f0-9]{24}$/.test(state.context_ref ?? "") || !identity || identity[2] !== state.viewed_tab) return null;
  return `${state.context_ref}:${identity[2]}:${identity[3]}`;
}

// Transport navigation failures and Chromium's own error document are not
// an interactive website. HTTP 4xx/5xx pages keep their normal http(s) URL.
export function nativeDocumentUnavailable(state: NativeBrowserState): boolean {
  return !!state.page_load_error || /^chrome-error:\/\//i.test(state.url);
}

export function nativeNewTabPage(state: NativeBrowserState): boolean {
  const action = state.activity?.last_action;
  const navigating = action?.tab_id === nativeViewedTab(state) && action.phase === "started" && ["navigate", "tab"].includes(action.action);
  return state.url === "about:blank" && !state.page_loading && !state.page_load_error && !state.dialog && !state.debugger_paused && !state.devtools_blocked && !navigating;
}

export function nativePageLoading(state: NativeBrowserState, initialFramePending: boolean, navigationPending: boolean, failed: boolean): boolean {
  if (failed || state.page_load_error || state.dialog || state.debugger_paused || state.devtools_blocked) return false;
  return state.page_loading === true || navigationPending || (initialFramePending && !!state.url && state.url !== "about:blank");
}
export type NativeBrowserInput =
  | { type: "click"; x: number; y: number; version: string }
  | { type: "key"; key: string }
  | { type: "text"; text: string }
  | { type: "scroll"; deltaY: number; deltaX?: number }
  | { type: "navigate"; url: string }
  | { type: "back" }
  | { type: "dialog"; accept: boolean; text?: string }
  | { type: "tab"; tab_action: "new" | "switch" | "close"; tab_id?: string; url?: string };

export interface NativeBrowserRect { x: number; y: number; width: number; height: number }
export interface NativeBrowserViewport { width: number; height: number }

export interface NativeBrowserPickRequest { context_ref: string; x: number; y: number; version: string; tab_id: string }
export interface NativeBrowserPick {
  version: string;
  tab_id: string;
  url: string;
  title: string;
  viewport: NativeBrowserViewport;
  observation_scope?: "native-dom-accessibility";
  element: { ref: string; tag: string; role: string; name: string; text: string; rect: NativeBrowserRect; frame_url?: string; fallback?: "frame" | "shadow" } | null;
}

/** A pick describes the exact displayed frame, not whichever page happens to
 * be active when an asynchronous hit test returns. */
export function nativePickMatches(pick: NativeBrowserPick, frame: { version: string; tab_id: string; viewport: NativeBrowserViewport }): boolean {
  if (!pick || !frame.version || pick.version !== frame.version || pick.tab_id !== frame.tab_id || !pick.viewport || ![pick.viewport.width, pick.viewport.height].every((value) => Number.isFinite(value) && value > 0) || pick.viewport.width !== frame.viewport.width || pick.viewport.height !== frame.viewport.height || typeof pick.url !== "string" || typeof pick.title !== "string") return false;
  if (pick.observation_scope !== undefined && pick.observation_scope !== "native-dom-accessibility") return false;
  const element = pick.element;
  return element === null || !!element && /^p[1-9]\d{0,15}$/.test(element.ref) && [element.tag, element.role, element.name, element.text].every((value) => typeof value === "string") && (element.frame_url === undefined || typeof element.frame_url === "string") && (element.fallback === undefined || ["frame", "shadow"].includes(element.fallback)) && !!readRect(element.rect) && !!nativeDisplayRect(element.rect, frame.viewport, frame.viewport);
}

/** Reuse the existing composer exhibit, with plain, bounded observations only.
 * pN is deliberately never a selector or executable browser target. */
export function nativeElementPick(pick: NativeBrowserPick, state?: NativeBrowserState, selectionFrame?: { context_ref: string; tab_id: string; version: string; identity: string; viewport: NativeBrowserViewport }): ElementPick | null {
  const element = pick.element;
  if (!element) return null;
  const clean = (value: string, limit: number) => clipText(value, limit).replace(/[&<>\"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[char]!);
  const role = clipText(element.role || element.tag, 80);
  const name = clipText(element.name, 120);
  const rect = element.rect;
  const position = `${Math.round(rect.y / pick.viewport.height * 100)}% top ${Math.round(rect.x / pick.viewport.width * 100)}% left ${Math.round(rect.width)}×${Math.round(rect.height)}`;
  const facts = { tag: element.tag, selector: "", role: clean(role, 80), name: clean(name, 120), text: clean(element.text, 800), position, html: "" };
  const lines = formatElementBlock(facts, { title: "", url: "" }).split("\n");
  lines[0] = `<page-element source="${clean(pick.title, 240)}" url="${clean(pick.url, 2000)}">`;
  lines[1] = `observation_ref: ${element.ref}\nreference_kind: read-only observation; not an executable target or selector\nreference_hint: Locate this element again with a fresh browser snapshot or find before acting.\nbrowser_tab: ${clean(pick.tab_id, 256)}\nframe_version: ${clean(pick.version, 256)}`;
  if (element.frame_url) lines.splice(-1, 0, `frame_url: ${clean(element.frame_url, 2000)}`);
  if (element.fallback) lines.splice(-1, 0, `observation_scope: ${element.fallback} container`);
  if (pick.observation_scope === "native-dom-accessibility") lines.splice(-1, 0,
    "observation_scope: native-dom-accessibility",
    "observation_details: Native DOM tag, accessible role/name/description and element geometry only; not complete visible page text. Form values were not read.",
  );
  // A pane can display a non-active tab. Use that bound document's own icon,
  // never the browser-wide active tab or a newer navigation's metadata.
  // Sampling a selection screenshot can advance its visual revision beyond
  // state.version. Its verified document identity still binds this metadata;
  // never replace state.version or accept a URL-only match as that proof.
  const tab = state?.tabs.find(tab => tab.id === pick.tab_id);
  const sameFrame = state && (selectionFrame
    ? !!nativeDocumentStamp(state) && selectionFrame.context_ref === state.context_ref
      && selectionFrame.tab_id === pick.tab_id && selectionFrame.identity === state.frame_identity
      && selectionFrame.identity.split(":")[0] === String(state.generation)
      && selectionFrame.viewport.width === state.viewport.width && selectionFrame.viewport.height === state.viewport.height
      && nativePickMatches(pick, selectionFrame)
    : nativePickMatches(pick, { version: state.version ?? "", tab_id: pick.tab_id, viewport: state.viewport }));
  const icon = state && nativeViewedTab(state) === pick.tab_id && state.url === pick.url
    && tab?.url === pick.url && citeFaviconURL({ locator: pick.url })
    && sameFrame
    ? browserFavicon(tab.favicon) : undefined;
  return {
    key: `native:${pick.tab_id}:${pick.version}:${element.ref}`,
    label: chipLabel({ role, name, tag: element.tag, selector: element.tag }),
    block: lines.join("\n"), pageTitle: clipText(pick.title || pick.url, 240), pageURL: pick.url, icon,
    preview: `${role}${name ? ` "${name}"` : ""}${element.text ? `\n${clipText(element.text, 300)}` : ""}`,
  };
}

/** Geometry in CSS pixels, shared by hit testing and overlay rendering. Device
 * pixel ratio is deliberately absent: both the remote viewport and DOM rect
 * use CSS pixels. The blank bars belong to the viewer, not the remote page. */
export function nativeContentRect(box: NativeBrowserViewport, viewport: NativeBrowserViewport) {
  if (![box.width, box.height, viewport.width, viewport.height].every((n) => Number.isFinite(n) && n > 0)) return null;
  const scale = Math.min(box.width / viewport.width, box.height / viewport.height);
  const width = viewport.width * scale;
  const height = viewport.height * scale;
  return { x: (box.width - width) / 2, y: (box.height - height) / 2, width, height, scale };
}

export function nativeDisplayPoint(point: { x: number; y: number }, box: NativeBrowserViewport, viewport: NativeBrowserViewport) {
  const content = nativeContentRect(box, viewport);
  if (!content || ![point.x, point.y].every(Number.isFinite) || point.x < 0 || point.y < 0 || point.x >= viewport.width || point.y >= viewport.height) return null;
  return { x: content.x + point.x * content.scale, y: content.y + point.y * content.scale };
}

/** Clip off-screen target/focus geometry before projecting, so no outline is
 * accidentally drawn in the letterbox area or stretched across the viewer. */
export function nativeDisplayRect(rect: NativeBrowserRect, box: NativeBrowserViewport, viewport: NativeBrowserViewport): NativeBrowserRect | null {
  const content = nativeContentRect(box, viewport);
  if (!content || ![rect.x, rect.y, rect.width, rect.height].every(Number.isFinite) || rect.width <= 0 || rect.height <= 0) return null;
  const x = Math.max(0, rect.x), y = Math.max(0, rect.y);
  const right = Math.min(viewport.width, rect.x + rect.width), bottom = Math.min(viewport.height, rect.y + rect.height);
  if (right <= x || bottom <= y) return null;
  return { x: content.x + x * content.scale, y: content.y + y * content.scale, width: (right - x) * content.scale, height: (bottom - y) * content.scale };
}

/** State availability and screenshot availability are independent. A changing
 * frame is an optimistic-version conflict, never evidence of disconnection. */
export function nativeReadFailure(stage: "state" | "frame", status?: number): "connection" | "changing" | "unavailable" {
  if (stage === "state") return "connection";
  return status === 409 ? "changing" : "unavailable";
}

export function nativeRefreshDelay(active: boolean, failed: boolean): number {
  return failed ? 3000 : active ? 350 : 2000;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function finiteNumber(value: unknown): value is number { return typeof value === "number" && Number.isFinite(value); }
function readRect(value: unknown): NativeBrowserRect | undefined {
  if (!record(value) || ![value.x, value.y, value.width, value.height].every(finiteNumber)) return;
  const { x, y, width, height } = value as unknown as NativeBrowserRect;
  if (width <= 0 || height <= 0 || [x, y, width, height].some((v) => Math.abs(v) > 100000)) return;
  return { x, y, width, height };
}

/** Optional frame metadata must never prevent the image from being shown.
 * Explicit fields keep unexpected values (including input text) out of the
 * presentation model. A mismatched/invalid header draws no geometry. */
export function decodeNativeOverlay(encoded: string | null, version: string): NativeBrowserOverlay | null {
  if (!version || !encoded || encoded.length > 32768 || !/^[A-Za-z0-9_-]+$/.test(encoded)) return null;
  try {
    const raw = atob(encoded.replace(/-/g, "+").replace(/_/g, "/"));
    const input: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(raw, (c) => c.charCodeAt(0))));
    return readNativeOverlay(input, version);
  } catch { return null; }
}

/** Shared sanitization for optional screenshot and live action metadata. */
export function readNativeOverlay(input: unknown, version: string): NativeBrowserOverlay | null {
  if (!version || !record(input) || input.version !== version || typeof input.tab_id !== "string" || !input.tab_id || input.tab_id.length > 256 || !record(input.viewport)) return null;
  const { width, height } = input.viewport;
  if (!finiteNumber(width) || !finiteNumber(height) || width <= 0 || height <= 0 || width > 20000 || height > 20000 || !finiteNumber(input.sampled_at)) return null;
  const overlay: NativeBrowserOverlay = { version, tab_id: input.tab_id, viewport: { width, height }, sampled_at: input.sampled_at };
  const action = input.last_action;
  if (record(action) && finiteNumber(action.seq) && typeof action.id === "string" && typeof action.action === "string" && typeof action.tab_id === "string" && typeof action.version === "string" && ["agent", "human"].includes(String(action.actor)) && ["started", "succeeded", "failed"].includes(String(action.phase)) && finiteNumber(action.started_at) && finiteNumber(action.updated_at)) {
    overlay.last_action = {
      seq: action.seq, id: action.id.slice(0, 256), actor: action.actor as NativeBrowserActivity["actor"], action: action.action.slice(0, 40),
      phase: action.phase as NativeBrowserActivity["phase"], label: "", tab_id: action.tab_id, version: action.version,
      started_at: action.started_at, updated_at: action.updated_at,
    };
  }
  const pointer = input.pointer;
  if (record(pointer) && finiteNumber(pointer.x) && finiteNumber(pointer.y) && finiteNumber(pointer.seq) && finiteNumber(pointer.action_seq) && ["move", "click"].includes(String(pointer.kind)) && ["agent", "human"].includes(String(pointer.actor)) && pointer.x >= 0 && pointer.x < width && pointer.y >= 0 && pointer.y < height) {
    overlay.pointer = { x: pointer.x, y: pointer.y, seq: pointer.seq, action_seq: pointer.action_seq, kind: pointer.kind as "move" | "click", actor: pointer.actor as "agent" | "human" };
  }
  if (record(input.target)) { const rect = readRect(input.target.rect); if (rect) overlay.target = { rect }; }
  if (record(input.focus)) { const rect = readRect(input.focus.rect); if (rect) overlay.focus = { rect, editable: input.focus.editable === true }; }
  return overlay;
}

export function nativeOverlayMatches(overlay: NativeBrowserOverlay | null, version: string, tab: string): overlay is NativeBrowserOverlay {
  return !!overlay && !!version && overlay.version === version && overlay.tab_id === tab;
}

export function nativeConfirmedClick(overlay: NativeBrowserOverlay): boolean {
  const action = overlay.last_action;
  return overlay.pointer?.kind === "click" && action?.action === "click" && action.phase === "succeeded" && action.seq === overlay.pointer.action_seq && action.actor === overlay.pointer.actor && action.tab_id === overlay.tab_id;
}

/** The frame uses object-fit:contain. Reject letterbox clicks instead of
 * clamping them onto an unrelated control at the edge of the remote page. */
export function nativeViewportPoint(
  x: number,
  y: number,
  rect: { left: number; top: number; width: number; height: number },
  viewport: { width: number; height: number },
): { x: number; y: number } | null {
  if (![x, y, rect.left, rect.top, rect.width, rect.height, viewport.width, viewport.height].every(Number.isFinite)) return null;
  if (rect.width <= 0 || rect.height <= 0 || viewport.width <= 0 || viewport.height <= 0) return null;
  const content = nativeContentRect(rect, viewport);
  if (!content) return null;
  const px = (x - rect.left - content.x) / content.scale;
  const py = (y - rect.top - content.y) / content.scale;
  if (px < 0 || py < 0 || px >= viewport.width || py >= viewport.height) return null;
  return { x: Math.min(viewport.width - 1, Math.round(px)), y: Math.min(viewport.height - 1, Math.round(py)) };
}

export function ownsNativeControl(state: NativeBrowserState | null, lease: string, epoch?: number): boolean {
  return state?.control === "human" && lease.length > 0 && (epoch === undefined || state.control_epoch === undefined || epoch === state.control_epoch);
}

export function nativeLeaseRejected(status?: number): boolean {
  // A stale screenshot or a dialog conflict uses 409 and keeps the lease.
  return status === 401 || status === 403;
}

/** Printable text takes the explicit insertText path, including Chinese IME.
 * Keyboard forwarding is only for navigation/editing keys and shortcuts. */
export function nativeKey(event: {
  key: string; isComposing?: boolean; keyCode?: number;
  ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean; shiftKey?: boolean;
}): string | null {
  if (event.isComposing || event.keyCode === 229 || ["Process", "Dead", "Unidentified", "Shift", "Control", "Alt", "Meta"].includes(event.key)) return null;
  const modified = event.ctrlKey || event.metaKey || event.altKey;
  const named = ["Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"];
  if (!modified && !named.includes(event.key)) return null;
  return [event.ctrlKey && "Control", event.metaKey && "Meta", event.altKey && "Alt", event.shiftKey && "Shift", event.key === " " ? "Space" : event.key].filter(Boolean).join("+");
}

/** Navigation only accepts original site URLs. Proxy paths belong solely to
 * the one-time bookmark migration, never to browser execution. */
export function nativeNavigationURL(value: string, current: string, appOrigin: string): string | null {
  const raw = value.trim();
  if (!raw) return null;
  const url = normalizeUrl(raw, /^https?:\/\//i.test(current) ? current : appOrigin);
  if (!url) return null;
  try {
    const parsed = new URL(url);
    if (parsed.origin === appOrigin && parsed.pathname.startsWith("/panels/")) return null;
    if (parsed.username || parsed.password) return null;
    return parsed.href;
  } catch { return null; }
}
