import { describe, expect, it, vi } from "vitest";
import { createLiveInputQueue, liveButton, liveKeyKind, liveModifiers, LiveTextCommit, type LiveInputEvent, type LivePointerEvent, type LiveWheelEvent } from "./browserLiveInput";

describe("live browser native input mapping", () => {
  it("maps Mac command shortcuts to the Linux browser without changing ordinary modifiers", () => {
    expect(liveModifiers({ metaKey: true, shiftKey: true })).toBe(10);
    expect(liveModifiers({ ctrlKey: true, altKey: true })).toBe(3);
    expect(liveModifiers({ metaKey: true }, false)).toBe(4);
    expect([0, 1, 2, -1].map(liveButton)).toEqual(["left", "middle", "right", "none"]);
  });
  it("separates printable and composed text from raw navigation and shortcuts", () => {
    const key = (key: string, extra = {}) => liveKeyKind({ key, code: "", ...extra });
    expect(key("a")).toBe("text"); expect(key("A", { shiftKey: true })).toBe("text");
    expect(key("a", { metaKey: true })).toBe("remote"); expect(key("Backspace")).toBe("remote");
    expect(key("Tab")).toBe("remote"); expect(key("Enter", { isComposing: true })).toBe("composition");
    expect(key("Process")).toBe("composition"); expect(key("l", { metaKey: true })).toBe("local");
    expect(key("v", { metaKey: true })).toBe("local");
  });
  it("commits Chinese IME exactly once, then allows repeated intentional text", () => {
    const input = new LiveTextCommit(); input.start();
    expect(input.input("ni", "insertCompositionText", true)).toBeNull();
    expect(input.end("你好")).toBe("你好");
    expect(input.input("你好", "insertText")).toBeNull();
    expect(input.input("你好", "insertText")).toBe("你好");
    input.start(); expect(input.end("")).toBeNull();
    expect(input.input("🔎", "insertText")).toBe("🔎");
  });
  it("drops interrupted composition, never manufacturing delete or enter text", () => {
    const input = new LiveTextCommit(); input.start(); input.reset();
    expect(input.input(null, "deleteContentBackward")).toBeNull();
    expect(input.input("\n", "insertLineBreak")).toBeNull();
    expect(input.input("pasted", "insertFromPaste")).toBe("pasted");
    input.end("同"); input.freshGesture(); expect(input.input("同", "insertText")).toBe("同");
  });
});

const wheel = (delta_y: number, extra = {}): LiveWheelEvent => ({ type: "wheel", x: 50, y: 60, delta_x: 0, delta_y, modifiers: 0, ...extra });
const pointer = (x: number, y = 10, extra = {}): LivePointerEvent => ({ type: "pointer", phase: "move", x, y, button: "none", buttons: 0, modifiers: 0, click_count: 0, ...extra });
const drag = (x: number, y = 10, extra = {}) => pointer(x, y, { button: "left", buttons: 1, click_count: 1, ...extra });
function harness() {
  let seq = 0, scheduled = 0;
  const callbacks = new Map<number, () => void>();
  const send = vi.fn((_event: LiveInputEvent, _binding: string): number | false => ++seq);
  const q = createLiveInputQueue(send, (a, b) => a === b, cb => { callbacks.set(++scheduled, cb); return scheduled; }, id => callbacks.delete(id));
  const tick = () => { const ready = [...callbacks.values()]; callbacks.clear(); ready.forEach(cb => cb()); };
  const drain = () => { for (let i = 1; i <= send.mock.calls.length && i < 1000; i++) { q.acknowledge(i); tick(); } };
  return { q, send, tick, drain };
}

describe("one acknowledged input queue", () => {
  it("never bypasses the total/wheel window through pointer motion or reliable boundaries", () => {
    const h = harness(); h.q.push(wheel(10), "a"); h.q.push(wheel(20), "a"); h.tick();
    h.q.push(wheel(30), "a"); h.q.push(pointer(80), "a"); h.q.push(drag(80, 10, { phase: "down" }), "a"); h.tick();
    expect(h.send).toHaveBeenCalledTimes(2); expect(h.q.stats).toMatchObject({ in_flight: 2, pending: 3 });
    h.q.acknowledge(1); expect(h.send).toHaveBeenCalledTimes(3);
    h.drain(); expect(h.send.mock.calls.map(([event]) => event)).toEqual([wheel(10), wheel(20), wheel(30), pointer(80), drag(80, 10, { phase: "down" })]);
    expect(h.q.stats.peak_in_flight).toBe(2);
  });
  it("coalesces only unsent adjacent hover; sent events are never replayed or replaced", () => {
    const h = harness(); h.q.push(pointer(0), "a"); h.q.push(pointer(1), "a"); h.tick();
    for (let x = 2; x < 200; x++) h.q.push(pointer(x), "a");
    expect(h.send.mock.calls.map(([event]) => (event as LivePointerEvent).x)).toEqual([0, 1]);
    expect(h.q.stats).toMatchObject({ pending: 1, coalesced_hover: 197 });
    h.q.acknowledge(1); expect(h.send.mock.calls[2]![0]).toEqual(pointer(199));
    h.q.acknowledge(1); expect(h.send).toHaveBeenCalledTimes(3);
  });
  it("keeps button/modifier/lease/epoch and key/text/scroll boundaries ordered", () => {
    const h = harness(); h.q.push(pointer(0), "a"); h.q.push(pointer(1), "a"); h.tick();
    const events: [LiveInputEvent, string][] = [
      [pointer(2), "a"], [pointer(3, 10, { modifiers: 8 }), "a"], [pointer(4, 10, { modifiers: 8 }), "b"],
      [drag(4, 10, { phase: "down" }), "b"], [drag(5), "b"], [wheel(7), "b"], [drag(6), "b"],
      [{ type: "key", phase: "down", key: "Shift", code: "ShiftLeft", key_code: 16, modifiers: 8, repeat: false }, "b"],
      [drag(7, 10, { modifiers: 8 }), "b"], [{ type: "text", text: "中文" }, "b"], [drag(7, 10, { phase: "up", buttons: 0 }), "b"],
    ];
    events.forEach(([event, binding]) => h.q.push(event, binding)); h.drain();
    expect(h.send.mock.calls.slice(2)).toEqual(events);
  });
  it("preserves both wheel axes, direction, coordinate and Ctrl/Meta boundaries under backpressure", () => {
    const h = harness(); const events = [wheel(10), wheel(20), wheel(7500, { delta_x: 2500 }), wheel(-40), wheel(-50, { x: 51 }), wheel(-60, { modifiers: 8 }), wheel(30, { modifiers: 2 }), wheel(30, { modifiers: 2 }), wheel(30, { modifiers: 4 })];
    events.forEach(event => h.q.push(event, "a")); h.drain();
    const sent = h.send.mock.calls.map(([event]) => event as LiveWheelEvent);
    expect(sent.reduce((sum, event) => sum + event.delta_y, 0)).toBe(events.reduce((sum, event) => sum + event.delta_y, 0));
    expect(sent.reduce((sum, event) => sum + event.delta_x, 0)).toBe(2500);
    expect(sent.every(event => Math.abs(event.delta_y) <= 3000)).toBe(true);
    expect(sent.slice(-6)).toEqual(events.slice(-6)); expect(h.q.wheelStats.in_flight).toBe(0);
  });
  it("clearing unsent work retains in-flight slots; reset follows sent actions without replay", () => {
    const h = harness(); h.q.push(pointer(0), "a"); h.q.push(pointer(1), "a"); h.tick(); h.q.push(pointer(2), "a");
    h.q.clearPending(); h.q.push({ type: "reset" }, "a"); h.q.push(pointer(3), "a"); h.tick(); expect(h.send).toHaveBeenCalledTimes(2);
    h.q.acknowledge(1); expect(h.send.mock.calls[2]![0]).toEqual({ type: "reset" });
    h.drain(); expect(h.send.mock.calls.map(([event]) => event)).toEqual([pointer(0), pointer(1), { type: "reset" }, pointer(3)]);
  });
  it("remembers a dispatched press when blur cancels an unsent release, and resets before a fresh gesture", () => {
    const h = harness(); h.q.push(drag(0, 0, { phase: "down" }), "a"); h.q.push(drag(10, 0), "a"); h.tick();
    h.q.push(drag(20, 0, { phase: "up", buttons: 0 }), "a"); h.q.clearPending();
    expect(h.q.needsReset).toBe(true); expect(h.q.idle).toBe(false);
    h.q.push({ type: "reset" }, "a"); h.q.acknowledge(1); expect(h.send.mock.calls[2]![0]).toEqual({ type: "reset" });
    expect(h.q.needsReset).toBe(true); h.q.acknowledge(2); h.q.acknowledge(3);
    expect(h.q.needsReset).toBe(false); expect(h.q.idle).toBe(true);
    expect(h.send.mock.calls.some(([event]) => event.type === "pointer" && event.phase === "up")).toBe(false);
  });
  it("never lets an old release ACK clear a newer button or repeated-key press", () => {
    const h = harness(); const key = { type: "key" as const, phase: "down" as const, key: "a", code: "KeyA", key_code: 65, modifiers: 2, repeat: false };
    h.q.push(key, "a"); h.q.push({ ...key, phase: "up" }, "a"); h.q.acknowledge(1);
    h.q.push(key, "a"); h.q.acknowledge(2); expect(h.q.needsReset).toBe(true);
    h.q.push({ ...key, phase: "up" }, "a"); h.q.acknowledge(3); h.q.acknowledge(4); expect(h.q.idle).toBe(true);
  });
  it("a rejected release retains remote press knowledge until a successful reset", () => {
    const h = harness(); h.q.push(drag(0, 0, { phase: "down" }), "a"); h.q.push(drag(10, 0, { phase: "up", buttons: 0 }), "a");
    h.q.acknowledge(1); h.q.reject(2); expect(h.q.needsReset).toBe(true);
    h.q.push({ type: "reset" }, "a"); h.q.acknowledge(3); expect(h.q.idle).toBe(true);
  });
  it("retiring a binding ignores old ACK/reject and does not advance the new queue", () => {
    const h = harness(); h.q.push(pointer(0), "a"); h.q.clear(); h.q.push(pointer(1), "b"); h.q.push(pointer(2), "b"); h.tick(); h.q.push(pointer(3), "b");
    h.q.acknowledge(1, { ms: 200, queue_ms: 0, dispatch_ms: 1 }); h.q.reject(1); h.tick();
    expect(h.send).toHaveBeenCalledTimes(3); expect(h.q.stats).toMatchObject({ in_flight: 2, pending: 1, window: 2 });
  });
  it("does not resurrect in-flight metadata after synchronous transport shutdown inside send", () => {
    const h = harness(); h.send.mockImplementationOnce(() => { h.q.clear(); return 1; });
    expect(h.q.push(pointer(1), "a")).toBe(false); expect(h.q.stats).toMatchObject({ in_flight: 0, pending: 0, dispatched: 0 });
    h.q.acknowledge(1); h.tick(); expect(h.send).toHaveBeenCalledTimes(1);
  });
  it("rejects a failed send once, drops pending work, and never reports an execution ACK", () => {
    const h = harness(); h.send.mockReturnValueOnce(false); expect(h.q.push(pointer(1), "a")).toBe(false);
    expect(h.q.stats).toMatchObject({ dispatched: 0, acknowledged: 0, pending: 0 }); h.tick(); expect(h.send).toHaveBeenCalledTimes(1);
  });
  it("bounds incompatible reliable work without dropping already accepted entries", () => {
    const h = harness(); h.q.push({ type: "text", text: "0" }, "a"); h.q.push({ type: "text", text: "1" }, "a");
    for (let i = 0; i < 256; i++) expect(h.q.push({ type: "text", text: String(i + 2) }, "a")).toBe(true);
    expect(h.q.push({ type: "text", text: "overflow" }, "a")).toBe(false); h.drain(); expect(h.send).toHaveBeenCalledTimes(258);
  });
  it("grows a high-RTT window one ACK at a time, shrinks for slow dispatch, and ignores unknown timing", () => {
    const h = harness(); for (let i = 0; i < 20; i++) h.q.push({ type: "text", text: String(i) }, "a");
    h.q.acknowledge(1, { ms: 107, queue_ms: 2, dispatch_ms: 5 }); expect(h.q.stats.window).toBe(3);
    for (let i = 2; i <= 6; i++) h.q.acknowledge(i, { ms: 107, queue_ms: 2, dispatch_ms: 5 });
    expect(h.q.stats.window).toBe(8);
    h.q.acknowledge(7, { ms: 140, queue_ms: 70, dispatch_ms: 65 }); expect(h.q.stats.window).toBe(3);
    h.q.acknowledge(8, { ms: 65, queue_ms: 0, dispatch_ms: 60 }); expect(h.q.stats.window).toBe(3);
    h.q.acknowledge(9, { ms: 99999, queue_ms: 0, dispatch_ms: 1 }); expect(h.q.stats.window).toBe(3);
    expect(h.q.stats.peak_in_flight).toBe(8);
  });
});

describe("unsent drag geometry", () => {
  function blockedDrag() { const h = harness(); h.q.push(drag(0, 0, { phase: "down" }), "a"); h.q.push(drag(1, 0), "a"); h.tick(); return h; }
  it("preserves first movement, exact endpoint and mouseup for a straight slider drag", () => {
    const h = blockedDrag(); for (let x = 2; x <= 600; x++) h.q.push(drag(x, 0), "a"); h.q.push(drag(600, 0, { phase: "up", buttons: 0 }), "a");
    expect(h.q.stats.pending).toBeLessThan(15); expect(h.q.stats.simplified_drag).toBeGreaterThan(580);
    h.drain(); const moves = h.send.mock.calls.map(([event]) => event).filter((event): event is LivePointerEvent => event.type === "pointer");
    expect(moves[1]).toEqual(drag(1, 0)); expect(moves[moves.length - 2]).toEqual(drag(600, 0)); expect(moves[moves.length - 1]?.phase).toBe("up");
    for (let i = 2; i < moves.length - 1; i++) expect(moves[i]!.x - moves[i - 1]!.x).toBeLessThanOrEqual(64);
  });
  it("keeps fine samples through the initial native drag threshold before simplifying a long path", () => {
    const h = blockedDrag(); for (let x = 2; x <= 600; x++) h.q.push(drag(x, 0), "a"); h.drain();
    const points = h.send.mock.calls.map(([event]) => event as LivePointerEvent);
    expect(points.slice(0, 4).map(event => event.x)).toEqual([0, 1, 5, 9]);
    expect(points[points.length - 1]?.x).toBe(600); expect(points.length).toBeLessThan(16);
  });
  it("preserves DnD turns, reversals and a closed drawing stroke rather than a single endpoint", () => {
    const h = blockedDrag(), path: Point[] = [];
    for (let x = 2; x <= 40; x++) path.push({ x, y: 0 });
    for (let y = 1; y <= 40; y++) path.push({ x: 40, y });
    for (let x = 39; x >= 0; x--) path.push({ x, y: 40 });
    for (let y = 39; y >= 0; y--) path.push({ x: 0, y });
    for (let x = 1; x <= 20; x++) path.push({ x, y: 0 });
    for (let x = 19; x >= 0; x--) path.push({ x, y: 0 });
    path.forEach(point => h.q.push(drag(point.x, point.y), "a")); h.drain();
    const points = h.send.mock.calls.map(([event]) => event as LivePointerEvent);
    for (const corner of [{ x: 40, y: 0 }, { x: 40, y: 40 }, { x: 0, y: 40 }, { x: 0, y: 0 }, { x: 20, y: 0 }]) expect(points).toContainEqual(expect.objectContaining(corner));
    expect(points[points.length - 1]).toEqual(drag(0, 0));
  });
  it("keeps every curve sample within 0.75 CSS px of its simplified path without accumulated error", () => {
    const h = blockedDrag(), raw: Point[] = [{ x: 1, y: 0 }];
    for (let x = 2; x <= 160; x++) { const point = { x, y: 30 * Math.sin((x - 1) / 25) }; raw.push(point); h.q.push(drag(point.x, point.y), "a"); }
    h.drain(); const points = h.send.mock.calls.slice(1).map(([event]) => event as LivePointerEvent);
    for (const point of raw) {
      const right = points.findIndex(next => next.x >= point.x); if (right <= 0) continue;
      const a = points[right - 1]!, b = points[right]!, dx = b.x - a.x, dy = b.y - a.y;
      expect(Math.abs((point.x - a.x) * dy - (point.y - a.y) * dx) / Math.hypot(dx, dy)).toBeLessThanOrEqual(.750001);
    }
    expect(points.length).toBeLessThan(40); expect(points.length).toBeGreaterThan(8);
  });
});
type Point = { x: number; y: number };

/** Deterministic network + serialized CDP service, so the test includes client
 * waiting, wire RTT and slow page handlers instead of merely counting calls. */
function simulate(handler: number, network: number, dragging: boolean) {
  let now = 0, seq = 0, id = 0, busyUntil = 0, lastAck = 0;
  const jobs = new Map<number, { at: number; run: () => void }>(), sent: { event: LiveInputEvent; at: number }[] = [];
  const clock = vi.spyOn(performance, "now").mockImplementation(() => now);
  const enqueue = (at: number, run: () => void) => { jobs.set(++id, { at, run }); return id; };
  const q = createLiveInputQueue<string>((event) => {
    const sequence = ++seq, at = now, start = Math.max(now + network / 2, busyUntil), done = start + handler; busyUntil = done;
    sent.push({ event, at }); enqueue(done + network / 2, () => { lastAck = now; q.acknowledge(sequence, { ms: now - at, queue_ms: start - at - network / 2, dispatch_ms: handler }); }); return sequence;
  }, (a, b) => a === b, callback => enqueue(now + 16, callback), job => jobs.delete(job));
  const advance = (until: number) => {
    while (true) { const next = [...jobs].sort((a, b) => a[1].at - b[1].at)[0]; if (!next || next[1].at > until) break; now = next[1].at; jobs.delete(next[0]); next[1].run(); }
    now = until;
  };
  try {
    if (dragging) q.push(drag(0, 0, { phase: "down" }), "a");
    for (let at = 0; at <= 1200; at += 8) { advance(at); expect(q.push(dragging ? drag(at / 2, 0) : pointer(at / 2, 0), "a")).toBe(true); }
    if (dragging) q.push(drag(600, 0, { phase: "up", buttons: 0 }), "a");
    advance(15000); return { sent, stats: q.stats, tail: lastAck - 1200 };
  } finally { clock.mockRestore(); }
}
describe("latency and queue curves", () => {
  it("keeps slow-handler hover and straight drag tails bounded instead of moving a FIFO to the client", () => {
    for (const dragging of [false, true]) {
      const result = simulate(60, 2, dragging);
      expect(result.stats.peak_in_flight).toBe(2); expect(result.stats.peak_pending).toBeLessThan(10);
      expect(result.tail).toBeLessThan(300); expect(result.sent.length).toBeLessThan(28);
      expect(result.stats).toMatchObject({ pending: 0, in_flight: 0 });
    }
  });
  it("preserves alternating scroll-target boundaries even when a slow handler cannot keep up", () => {
    const h = harness();
    for (let i = 0; i < 48; i++) { h.q.push(pointer(i * 15), "a"); h.q.push(wheel(24, { x: i * 15 }), "a"); }
    h.tick(); expect(h.q.stats.in_flight).toBe(2); expect(h.q.stats.pending).toBe(94);
    h.drain(); expect(h.send).toHaveBeenCalledTimes(96);
    expect(h.send.mock.calls.reduce((sum, [event]) => sum + (event.type === "wheel" ? event.delta_y : 0), 0)).toBe(1152);
    expect(h.q.stats).toMatchObject({ coalesced_hover: 0, simplified_drag: 0, peak_in_flight: 2 });
  });
  it("pipelines a 100 ms network without serializing each motion behind one ACK", () => {
    const result = simulate(2, 100, false);
    expect(result.stats.window).toBe(8); expect(result.stats.peak_in_flight).toBe(8);
    expect(result.sent.length).toBeGreaterThan(65); expect(result.stats.peak_pending).toBeLessThan(3); expect(result.tail).toBeLessThan(160);
  });
});
