import { nativeDisplayRect, nativePickMatches, type NativeBrowserPick, type NativeBrowserPickRequest, type NativeBrowserRect, type NativeBrowserViewport } from "./nativeBrowser";

export interface BrowserPickPreview {
  context_ref: string; tab_id: string; version: string; viewport: NativeBrowserViewport;
  preview_token?: string; expires_in_ms?: number;
  element: { tag: string; role: string; rect: NativeBrowserRect; fallback?: "frame" } | null;
}
export interface BrowserPickerFrame { context_ref: string; tab_id: string; version: string; viewport: NativeBrowserViewport; pixels: string }
export interface BrowserPickerPoint { x: number; y: number }
export type BrowserPickCommitRequest = NativeBrowserPickRequest & { preview_token: string };
export function pickPreviewMatches(value: BrowserPickPreview, frame: BrowserPickerFrame | null): boolean {
  if (!frame || !value || value.context_ref !== frame.context_ref || value.tab_id !== frame.tab_id || value.version !== frame.version || !value.viewport || value.viewport.width !== frame.viewport.width || value.viewport.height !== frame.viewport.height) return false;
  if (value.element === null) return value.preview_token === undefined;
  const element = value.element;
  return !!element && typeof element.tag === "string" && element.tag.length <= 80 && typeof element.role === "string" && element.role.length <= 80 &&
    (element.fallback === undefined || element.fallback === "frame") && /^pv_[a-f0-9]{48}$/.test(value.preview_token ?? "") &&
    typeof value.expires_in_ms === "number" && value.expires_in_ms > 0 && value.expires_in_ms <= 3000 &&
    !!element.rect && [element.rect.x, element.rect.y, element.rect.width, element.rect.height].every(Number.isFinite) &&
    !!nativeDisplayRect(element.rect, frame.viewport, frame.viewport);
}
const samePoint = (a: BrowserPickerPoint | undefined, b: BrowserPickerPoint) => a?.x === b.x && a.y === b.y;
const sameFrame = (a: BrowserPickerFrame, b: BrowserPickerFrame | null) => !!b && a.context_ref === b.context_ref && a.tab_id === b.tab_id && a.version === b.version && a.pixels === b.pixels && a.viewport.width === b.viewport.width && a.viewport.height === b.viewport.height;
const inside = (point: BrowserPickerPoint, rect: NativeBrowserRect) => point.x >= rect.x && point.y >= rect.y && point.x < rect.x + rect.width && point.y < rect.y + rect.height;
interface Intent { serial: number; epoch: number; kind: "hover" | "commit"; point: BrowserPickerPoint; frame: BrowserPickerFrame; at: number }
export interface BrowserPickerStatus { loading: boolean; committing: boolean; pending: boolean }
export interface BrowserPickerOptions {
  frame: () => BrowserPickerFrame | null;
  preview: (request: NativeBrowserPickRequest) => Promise<BrowserPickPreview>;
  commit: (request: BrowserPickCommitRequest) => Promise<NativeBrowserPick>;
  previewed: (preview: BrowserPickPreview | null, point?: BrowserPickerPoint) => void;
  committed: (pick: NativeBrowserPick) => void;
  status: (status: BrowserPickerStatus) => void;
  error: (error: unknown, kind: "hover" | "commit") => void;
  now?: () => number;
  schedule?: (callback: () => void, milliseconds: number) => ReturnType<typeof setTimeout>;
  cancel?: (timer: ReturnType<typeof setTimeout>) => void;
}

/** One actual observation at a time, plus one newest pointer intent. Pointer
 * movement invalidates results, never aborts a backend read and pretends it
 * drained. A click preempts queued hovers and always obtains a fresh token. */
export function createBrowserPicker(options: BrowserPickerOptions) {
  const now = options.now ?? (() => performance.now()), schedule = options.schedule ?? setTimeout, cancel = options.cancel ?? clearTimeout;
  let epoch = 0, serial = 0, closed = false, paused = false, lastStarted = -Infinity;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let queued: Intent | undefined, flight: Intent | undefined;
  let pointer: BrowserPickerPoint | undefined, preview: BrowserPickPreview | null = null;
  let displayedFrame: BrowserPickerFrame | undefined;
  const publish = () => options.status({ loading: !!flight || !!queued, committing: flight?.kind === "commit" || queued?.kind === "commit", pending: paused });
  const clearPreview = () => { if (preview) { preview = null; displayedFrame = undefined; options.previewed(null); } };
  const active = (intent: Intent) => !closed && intent.epoch === epoch && intent.serial === serial && sameFrame(intent.frame, options.frame());
  const request = (intent: Intent): NativeBrowserPickRequest => ({ context_ref: intent.frame.context_ref, tab_id: intent.frame.tab_id, version: intent.frame.version, ...intent.point });
  const stopTimer = () => { if (timer !== undefined) cancel(timer); timer = undefined; };
  const pump = () => {
    if (closed || paused || flight || !queued) return;
    stopTimer();
    const wait = queued.kind === "commit" ? 0 : Math.max(0, 50 - (now() - lastStarted));
    if (wait) { timer = schedule(() => { timer = undefined; pump(); }, wait); return; }
    const intent = queued; queued = undefined;
    if (!active(intent)) { publish(); return; }
    flight = intent; lastStarted = now(); publish();
    void (async () => {
      try {
        const hit = await options.preview(request(intent));
        if (!active(intent)) return;
        if (!pickPreviewMatches(hit, intent.frame) || hit.element && !inside(intent.point, hit.element.rect)) throw Object.assign(new Error("Browser preview no longer matches the displayed frame"), { status: 409 });
        // A click remains an explicit selection if the pointer moves, but an
        // asynchronously returned outline must not jump back under it.
        if (samePoint(pointer, intent.point)) { preview = hit; displayedFrame = intent.frame; options.previewed(hit, intent.point); }
        if (intent.kind === "commit") {
          if (!hit.element || !hit.preview_token) throw Object.assign(new Error("There is no selectable element here"), { code: "PICK_EMPTY" });
          const pick = await options.commit({ ...request(intent), preview_token: hit.preview_token });
          if (!active(intent)) return;
          if (!nativePickMatches(pick, intent.frame)) throw Object.assign(new Error("Browser element changed before selection"), { status: 409 });
          options.committed(pick);
        }
      } catch (error) {
        // An old request can still reveal an underlying reader lock. Pause
        // all new observations until a fresh state read proves it drained.
        if (!closed && (error as { code?: string })?.code === "PICK_OBSERVATION_PENDING") {
          paused = true; queued = undefined; serial++; clearPreview(); options.error(error, intent.kind);
        } else if (active(intent)) { clearPreview(); options.error(error, intent.kind); }
      } finally { if (flight === intent) flight = undefined; if (!closed) { publish(); pump(); } }
    })();
  };
  function enqueue(kind: Intent["kind"], point: BrowserPickerPoint) {
    if (closed) return;
    if (preview?.element && (!inside(point, preview.element.rect) || !displayedFrame || !sameFrame(displayedFrame, options.frame()))) clearPreview();
    const previous = pointer; pointer = point;
    if (flight?.kind === "commit" || queued?.kind === "commit") return;
    const frame = options.frame();
    if (!frame || ![point.x, point.y].every(Number.isFinite) || point.x < 0 || point.y < 0 || point.x >= frame.viewport.width || point.y >= frame.viewport.height) { invalidate(); return; }
    if (paused) return;
    if (kind === "hover" && samePoint(previous, point) && (queued || flight || preview) && (!displayedFrame || sameFrame(displayedFrame, frame))) return;
    queued = { serial: ++serial, epoch, kind, point: { ...point }, frame: { ...frame, viewport: { ...frame.viewport } }, at: now() };
    if (kind === "commit") stopTimer();
    publish(); pump();
  }
  function invalidate() { epoch++; serial++; queued = undefined; pointer = undefined; stopTimer(); clearPreview(); if (!closed) publish(); }
  return {
    hover(point: BrowserPickerPoint) { enqueue("hover", point); },
    click(point: BrowserPickerPoint) { enqueue("commit", point); },
    invalidate,
    get pending() { return paused; },
    /** Call only after a newly requested bound state reports pending=false. */
    drained() {
      if (!paused || closed) return;
      paused = false;
      const point = pointer; pointer = undefined;
      publish(); if (point) enqueue("hover", point);
    },
    close() { if (closed) return; closed = true; invalidate(); },
  };
}
