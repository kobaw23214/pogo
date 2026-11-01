/** Browser-native input is captured locally; only committed text crosses the
 * transport. No clipboard reads, polling, input replay or DOM injection. */
export interface LiveModifiers { altKey?: boolean; ctrlKey?: boolean; metaKey?: boolean; shiftKey?: boolean }
export function liveModifiers(event: LiveModifiers, remoteLinux = true): number {
  return (event.altKey ? 1 : 0) | (event.ctrlKey || remoteLinux && event.metaKey ? 2 : 0) | (!remoteLinux && event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}
export function liveButton(button: number): "left" | "middle" | "right" | "none" {
  return button === 0 ? "left" : button === 1 ? "middle" : button === 2 ? "right" : "none";
}
export interface LiveKeyEvent extends LiveModifiers {
  key: string; code: string; repeat?: boolean; location?: number; isComposing?: boolean; keyCode?: number;
}
/** Printable input comes from beforeinput so dead keys, emoji, paste and IME
 * cannot be doubled by a second keydown path. Browser chrome shortcuts such
 * as Cmd+L/R/T/W are deliberately left local. */
export function liveKeyKind(event: LiveKeyEvent): "remote" | "text" | "local" | "composition" {
  if (event.isComposing || event.keyCode === 229 || ["Process", "Dead", "Unidentified"].includes(event.key)) return "composition";
  const shortcut = event.ctrlKey || event.metaKey;
  if (shortcut && !event.altKey && ["l", "r", "t", "w", "n", "+", "-", "="].includes(event.key.toLowerCase())) return "local";
  // The trusted paste event carries the chosen content. Copy/cut are handled
  // by the remote page; reading its selection requires a separate capability.
  if (shortcut && event.key.toLowerCase() === "v") return "local";
  if (event.key.length === 1 && !shortcut && !event.altKey) return "text";
  return "remote";
}

/** Some engines deliver the same IME commit once in compositionend and again
 * as insertFromComposition/input. Consume that trailing event exactly once. */
export class LiveTextCommit {
  composing = false;
  private duplicate: string | undefined;
  start() { this.composing = true; this.duplicate = undefined; }
  end(text: string): string | null {
    this.composing = false;
    this.duplicate = text || undefined;
    return text || null;
  }
  input(text: string | null, inputType: string, isComposing = false): string | null {
    if (this.composing || isComposing || inputType === "insertCompositionText") return null;
    const duplicate = this.duplicate;
    this.duplicate = undefined;
    if (duplicate !== undefined && text === duplicate && ["insertText", "insertFromComposition"].includes(inputType)) return null;
    return text && ["insertText", "insertFromPaste", "insertFromDrop", "insertReplacementText", "insertFromComposition"].includes(inputType) ? text : null;
  }
  /** A new key/pointer gesture marks the end of an optional trailing commit. */
  freshGesture() { if (!this.composing) this.duplicate = undefined; }
  reset() { this.composing = false; this.duplicate = undefined; }
}

export interface LivePointerEvent {
  type: "pointer"; phase: "down" | "move" | "up" | "cancel"; x: number; y: number;
  button: "left" | "middle" | "right" | "none"; buttons: number; modifiers: number; click_count: number;
}
export interface LiveWheelEvent { type: "wheel"; x: number; y: number; delta_x: number; delta_y: number; modifiers: number }
export type LiveInputEvent = LivePointerEvent | LiveWheelEvent
  | { type: "key"; phase: "down" | "up"; key: string; code: string; key_code: number; modifiers: number; repeat: boolean }
  | { type: "text"; text: string } | { type: "reset" };
export interface LiveInputTiming { ms: number; queue_ms?: number; dispatch_ms?: number }
export interface LiveInputQueueStats {
  received: number; dispatched: number; acknowledged: number; coalesced_hover: number; simplified_drag: number;
  pending: number; in_flight: number; window: number; max_window: number; peak_pending: number; peak_in_flight: number;
  last_wait_ms?: number; oldest_pending_ms?: number;
}
const MAX_PENDING = 256, MAX_WINDOW = 8, DRAG_ERROR = .75, DRAG_SPAN = 64;
type Point = { x: number; y: number };
const samePointer = (a: LivePointerEvent, b: LivePointerEvent) => a.phase === "move" && b.phase === "move" && a.buttons === b.buttons && a.button === b.button && a.modifiers === b.modifiers && a.click_count === b.click_count;
const sameDirection = (a: number, b: number) => !a || !b || Math.sign(a) === Math.sign(b);
/** One ordered, bounded transport queue. Only unsent adjacent motion is reduced;
 * reliable boundaries never flush past the ACK window. Queued acceptance is not
 * execution: sequence numbers and ACK metrics belong to actual sends only. */
export function createLiveInputQueue<T>(send: (event: LiveInputEvent, binding: T) => number | false, sameBinding: (a: T, b: T) => boolean,
  schedule: (callback: () => void) => number, cancel: (id: number) => void) {
  type Entry = { event: LiveInputEvent; binding: T; queuedAt: number; path?: { anchor: Point; points: Point[]; span: number } };
  const pressed = new Map<string, number>();
  const pending: Entry[] = [], inFlight = new Map<number, { type: LiveInputEvent["type"]; releases: Map<string, number> }>();
  let previous: { event: LiveInputEvent; binding: T } | undefined;
  let dragStart: { point: Point; binding: T } | undefined;
  let scheduled: number | undefined, revision = 0, window = 2, pumping = false;
  let received = 0, dispatched = 0, acknowledged = 0, coalescedHover = 0, simplifiedDrag = 0, peakPending = 0, peakInFlight = 0;
  let wheelReceived = 0, wheelDispatched = 0, lastWaitMs: number | undefined, wheelWaitMs: number | undefined;
  let networkEstimate: number | undefined, dispatchEstimate: number | undefined;
  const wheelInFlight = () => [...inFlight.values()].filter(item => item.type === "wheel").length;
  const cancelScheduled = () => { if (scheduled !== undefined) cancel(scheduled); scheduled = undefined; };
  const clearPending = () => { revision++; cancelScheduled(); pending.length = 0; previous = undefined; dragStart = undefined; };
  const clear = () => { clearPending(); inFlight.clear(); pressed.clear(); window = 2; networkEstimate = dispatchEstimate = undefined; };
  const canSend = () => pending.length > 0 && inFlight.size < window && (pending[0]!.event.type !== "wheel" || wheelInFlight() < 2);
  const pump = () => {
    if (pumping) return true;
    cancelScheduled(); pumping = true;
    try {
      while (canSend()) {
        const next = pending[0]!, original = next.event, before = revision;
        let event = original, split = false;
        if (original.type === "wheel") {
          const largest = Math.max(Math.abs(original.delta_x), Math.abs(original.delta_y)), scale = largest > 3000 ? 3000 / largest : 1;
          event = { ...original, delta_x: original.delta_x * scale, delta_y: original.delta_y * scale }; split = scale < 1;
        }
        const seq = send(event, next.binding);
        // send() can synchronously close the transport and clear this queue.
        if (before !== revision) return false;
        if (seq === false) { clearPending(); return false; }
        lastWaitMs = Math.max(0, performance.now() - next.queuedAt);
        // Keep pressed state until its matching release ACK. A queued or
        // rejected mouse/key-up cannot make blur forget the remote gesture.
        const releases = new Map<string, number>();
        const release = (key: string) => { const down = pressed.get(key); if (down !== undefined) releases.set(key, down); };
        if (event.type === "pointer" && event.phase === "down") pressed.set(`pointer:${event.button}`, seq);
        if (event.type === "pointer" && event.phase === "up") release(`pointer:${event.button}`);
        if (event.type === "pointer" && event.phase === "cancel") for (const key of pressed.keys()) release(key);
        if (event.type === "key") { const key = `key:${event.code || event.key}`; if (event.phase === "down") pressed.set(key, seq); else release(key); }
        if (event.type === "reset") for (const key of pressed.keys()) release(key);
        dispatched++; inFlight.set(seq, { type: event.type, releases }); peakInFlight = Math.max(peakInFlight, inFlight.size);
        if (event.type === "wheel") { wheelDispatched++; wheelWaitMs = lastWaitMs; }
        if (split && event.type === "wheel" && original.type === "wheel") { original.delta_x -= event.delta_x; original.delta_y -= event.delta_y; }
        else pending.shift();
      }
      return true;
    } finally { pumping = false; }
  };
  const queue = () => { if (canSend() && scheduled === undefined) scheduled = schedule(() => { scheduled = undefined; pump(); }); };
  const simplify = (last: Entry, event: LivePointerEvent) => {
    if (!last.path || last.event.type !== "pointer") return false;
    const { anchor, points } = last.path, dx = event.x - anchor.x, dy = event.y - anchor.y, length2 = dx * dx + dy * dy;
    if (length2 > last.path.span * last.path.span || points.length >= 64) return false;
    // Test every retained raw point against the proposed segment, including
    // projection order. This prevents cumulative error, reversals and loops.
    let projection = 0;
    for (const point of points) {
      const px = point.x - anchor.x, py = point.y - anchor.y;
      if (!length2) { if (px || py) return false; continue; }
      const t = (px * dx + py * dy) / length2;
      if (t < projection || t > 1 || Math.abs(px * dy - py * dx) > DRAG_ERROR * Math.sqrt(length2)) return false;
      projection = t;
    }
    if (event.x !== last.event.x || event.y !== last.event.y) points.push({ x: event.x, y: event.y });
    last.event = event; simplifiedDrag++; return true;
  };
  return {
    push(input: LiveInputEvent, binding: T) {
      const event = { ...input };
      if ((event.type === "pointer" || event.type === "wheel") && ![event.x, event.y].every(Number.isFinite)) return false;
      if (event.type === "wheel" && ![event.delta_x, event.delta_y].every(Number.isFinite)) return false;
      if (event.type === "wheel" && !event.delta_x && !event.delta_y) return true;
      if (event.type === "reset") clearPending();
      received++; if (event.type === "wheel") wheelReceived++;
      if (event.type === "pointer" && event.phase === "down") dragStart = { point: { x: event.x, y: event.y }, binding };
      if (event.type === "pointer" && ["up", "cancel"].includes(event.phase)) dragStart = undefined;
      const last = pending[pending.length - 1];
      let merged = false;
      if (last && sameBinding(last.binding, binding)) {
        if (event.type === "pointer" && last.event.type === "pointer" && samePointer(last.event, event)) {
          if (!event.buttons) { last.event = event; coalescedHover++; merged = true; }
          else merged = simplify(last, event);
        } else if (event.type === "wheel" && last.event.type === "wheel" && !(event.modifiers & 6) && last.event.x === event.x && last.event.y === event.y && last.event.modifiers === event.modifiers && sameDirection(last.event.delta_x, event.delta_x) && sameDirection(last.event.delta_y, event.delta_y)) {
          const x = last.event.delta_x + event.delta_x, y = last.event.delta_y + event.delta_y;
          if (!Number.isFinite(x) || !Number.isFinite(y)) return false;
          last.event.delta_x = x; last.event.delta_y = y; merged = true;
        }
      }
      if (!merged) {
        if (pending.length >= MAX_PENDING) return false;
        const entry: Entry = { event, binding, queuedAt: performance.now() };
        // The first move after a button/key/wheel/binding boundary stays intact.
        if (event.type === "pointer" && event.phase === "move" && event.buttons && previous?.event.type === "pointer" && samePointer(previous.event, event) && sameBinding(previous.binding, binding)) {
          // Keep fine initial motion near mouse-down so a native drag threshold
          // is crossed early, even when its very first move was only one pixel.
          const starting = dragStart && sameBinding(dragStart.binding, binding) && Math.hypot(previous.event.x - dragStart.point.x, previous.event.y - dragStart.point.y) < 8;
          entry.path = { anchor: { x: previous.event.x, y: previous.event.y }, points: [{ x: event.x, y: event.y }], span: starting ? 4 : DRAG_SPAN };
        }
        pending.push(entry); peakPending = Math.max(peakPending, pending.length);
      }
      previous = { event, binding };
      if (!inFlight.size || event.type !== "wheel" && !(event.type === "pointer" && event.phase === "move")) return pump();
      queue(); return true;
    },
    acknowledge(seq: number, timing?: LiveInputTiming) {
      const flight = inFlight.get(seq);
      if (!flight) return;
      inFlight.delete(seq);
      for (const [key, down] of flight.releases) if (pressed.get(key) === down) pressed.delete(key);
      acknowledged++;
      if (timing && [timing.ms, timing.queue_ms, timing.dispatch_ms].every(n => typeof n === "number" && Number.isFinite(n) && n >= 0 && n <= 60000)) {
        const network = Math.max(0, timing.ms - timing.queue_ms! - timing.dispatch_ms!);
        networkEstimate = networkEstimate === undefined ? network : networkEstimate * .8 + network * .2;
        dispatchEstimate = dispatchEstimate === undefined ? timing.dispatch_ms! : dispatchEstimate * .8 + timing.dispatch_ms! * .2;
        const desired = Math.max(2, Math.min(MAX_WINDOW, Math.ceil(networkEstimate / Math.max(16, dispatchEstimate, timing.dispatch_ms!)) + 1));
        window = desired < window ? desired : Math.min(desired, window + 1);
      }
      pump();
    },
    reject(seq: number) { if (inFlight.delete(seq)) clearPending(); },
    clearPending,
    clear,
    get needsReset() { return pressed.size > 0; },
    get idle() { return !pending.length && !inFlight.size && !pressed.size; },
    reset() { clear(); received = dispatched = acknowledged = coalescedHover = simplifiedDrag = peakPending = peakInFlight = wheelReceived = wheelDispatched = 0; lastWaitMs = wheelWaitMs = undefined; },
    get stats(): LiveInputQueueStats { return { received, dispatched, acknowledged, coalesced_hover: coalescedHover, simplified_drag: simplifiedDrag, pending: pending.length, in_flight: inFlight.size, window, max_window: MAX_WINDOW, peak_pending: peakPending, peak_in_flight: peakInFlight, last_wait_ms: lastWaitMs, oldest_pending_ms: pending.length ? Math.max(0, performance.now() - pending[0]!.queuedAt) : undefined }; },
    get wheelStats() { const wheels = pending.filter(item => item.event.type === "wheel"); return { received: wheelReceived, dispatched: wheelDispatched, pending_groups: wheels.length, in_flight: wheelInFlight(), last_wait_ms: wheelWaitMs, oldest_pending_ms: wheels.length ? Math.max(0, performance.now() - wheels[0]!.queuedAt) : undefined }; },
  };
}
