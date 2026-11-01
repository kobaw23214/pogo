import { afterEach, describe, expect, it, vi } from "vitest";
import { browserLiveURL, createBrowserLive, liveDisplayAuthority, liveFrameMatches, readLiveFrame, type BrowserLiveFrame, type BrowserLiveOptions } from "./browserLive";
import { api } from "../api/client";
import type { NativeBrowserState } from "./nativeBrowser";
const target = { context_ref: "ctx_aaaaaaaaaaaaaaaaaaaaaaaa", tab_id: "t1" };
const state: NativeBrowserState = { ...target, backend: "native", control: "human", url: "https://example.test", title: "Fixture", tabs: [], active_tab: "t1", viewed_tab: "t1", viewport: { width: 800, height: 600 }, generation: 2, control_epoch: 3, document_version: 4, frame_identity: "2:t1:4" };
const frame: BrowserLiveFrame = { ...target, type: "media", frame_id: 1, media_epoch: "media-1", viewport: state.viewport, pixel_width: 1600, pixel_height: 1200, generation: 2, control_epoch: 3, document_version: 4, frame_identity: "2:t1:4" };
class Socket {
  readyState = 1; bufferedAmount = 0; onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null; onerror: (() => void) | null = null;
  send = vi.fn(); close = vi.fn((code?: number) => {
    if (code !== undefined && code !== 1000 && (!Number.isInteger(code) || code < 3000 || code > 4999)) throw new DOMException("Invalid local WebSocket close code", "InvalidAccessError");
  });
  deliver(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
  messages() { return this.send.mock.calls.map(call => JSON.parse(call[0])); }
}
class Peer {
  connectionState = "new"; iceConnectionState = "new";
  ontrack: ((event: RTCTrackEvent) => void) | null = null;
  onicecandidate: ((event: RTCPeerConnectionIceEvent) => void) | null = null;
  onconnectionstatechange: (() => void) | null = null; oniceconnectionstatechange: (() => void) | null = null;
  localDescription: RTCSessionDescriptionInit | null = null;
  close = vi.fn(() => { this.connectionState = "closed"; });
  setRemoteDescription = vi.fn(async (_description: RTCSessionDescriptionInit) => {});
  createAnswer = vi.fn(async (): Promise<RTCSessionDescriptionInit> => ({ type: "answer", sdp: "answer-sdp" }));
  setLocalDescription = vi.fn(async (description: RTCSessionDescriptionInit) => { this.localDescription = description; });
  addIceCandidate = vi.fn(async (_candidate?: RTCIceCandidateInit) => {});
  getStats = vi.fn(async (): Promise<Map<string, Record<string, unknown>>> => new Map());
  track() {
    const track = { kind: "video", stop: vi.fn(), onended: null };
    const stream = { getTracks: () => [track], getVideoTracks: () => [track] };
    this.ontrack?.({ track, streams: [stream] } as unknown as RTCTrackEvent);
    return { track, stream };
  }
}
const cleanups: (() => void)[] = [];
afterEach(() => { for (const cleanup of cleanups.splice(0)) cleanup(); vi.useRealTimers(); });
const relayGrant = (next: BrowserLiveFrame) => ({ context_ref: next.context_ref, tab_id: next.tab_id, generation: next.generation, media_epoch: next.media_epoch,
  ice_transport_policy: "relay", expires_in: 60, ice_servers: [{ urls: "turns:pogo.example:443?transport=tcp", username: "fixture-view", credential: "synthetic-relay-secret" }] });
function harness(scale = 2, options: Omit<BrowserLiveOptions, "peerConnectionFactory" | "scale"> = {}) {
  const socket = new Socket(), peers: Peer[] = [];
  const callbacks = { configured: vi.fn(), visibility: vi.fn(), overlay: vi.fn(), ready: vi.fn(), state: vi.fn(), media: vi.fn(async (_stream: MediaStream, _frame: BrowserLiveFrame) => {}), mediaClosed: vi.fn(), error: vi.fn(), close: vi.fn() };
  const factory = vi.fn((_configuration: RTCConfiguration) => { const peer = new Peer(); peers.push(peer); return peer as unknown as RTCPeerConnection; });
  const live = createBrowserLive(socket as unknown as WebSocket, target, callbacks, { scale, relayOrigin: "https://pogo.example", resolveRelayConfiguration: async next => relayGrant(next), ...options, peerConnectionFactory: factory });
  cleanups.push(() => live.close());
  const ready = (next = state) => socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", state: next });
  const offer = (next = frame) => socket.deliver({ type: "media_offer", frame: next, sdp: "offer-sdp", ice_servers: [] });
  const display = async (next = frame) => {
    offer(next); await vi.waitFor(() => expect(socket.messages().some(item => item.type === "media_answer" && item.media_epoch === next.media_epoch)).toBe(true));
    const calls = callbacks.media.mock.calls.length;
    peers[peers.length - 1]!.track(); await vi.waitFor(() => expect(callbacks.media).toHaveBeenCalledTimes(calls + 1));
    const presented = callbacks.media.mock.calls[callbacks.media.mock.calls.length - 1]![1];
    expect(live.present(presented, presented.pixel_width, presented.pixel_height)).toBe(true);
    return presented;
  };
  return { socket, callbacks, live, peers, factory, ready, offer, display };
}
const authority = { generation: 2, control_epoch: 3, lease_token: "in-memory-only", frame_id: 1, media_epoch: "media-1" };
const ice = { candidate: "candidate:fixture 1 udp 1 127.0.0.1 9000 typ host", sdpMid: "0", sdpMLineIndex: 0 };

describe("Live Agent action telemetry", () => {
  const agentState = { ...state, control: "agent" as const };
  const message = (seq = 1) => ({ ...target, type: "action_overlay", seq, frame_id: frame.frame_id, media_epoch: frame.media_epoch,
    generation: state.generation, control_epoch: state.control_epoch, document_version: state.document_version, frame_identity: state.frame_identity, viewport: state.viewport,
    overlay: { version: "v1", tab_id: target.tab_id, viewport: state.viewport, sampled_at: 1010,
      last_action: { seq: 9, id: "op9", action: "click", actor: "agent", phase: "succeeded", label: "", tab_id: target.tab_id, version: "v1", started_at: 990, updated_at: 1000 },
      pointer: { x: 320, y: 200, seq: 8, action_seq: 9, actor: "agent", kind: "click" },
      focus: { rect: { x: 300, y: 180, width: 100, height: 40 }, editable: true },
    } });
  const ready = (h: ReturnType<typeof harness>, extra = {}) => h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { action_overlay: true, ...extra }, state: agentState });
  it("opts in only to advertised optional telemetry without changing older workers", async () => {
    const legacy = harness(); legacy.ready(agentState); await legacy.display(); legacy.callbacks.overlay.mockClear();
    legacy.socket.deliver(message());
    expect(legacy.socket.messages()[0]).toEqual({ type: "media_config", scale: 2 });
    expect(legacy.callbacks.overlay).not.toHaveBeenCalled(); expect(legacy.callbacks.error).not.toHaveBeenCalled();
    const modern = harness(); ready(modern);
    expect(modern.socket.messages()[0]).toEqual({ type: "media_config", scale: 2, action_overlay: true });
  });
  it("shows marks only after this exact Agent page's video is presented", async () => {
    const h = harness(); ready(h); h.callbacks.overlay.mockClear(); h.socket.deliver(message());
    expect(h.callbacks.overlay).not.toHaveBeenCalled();
    await h.display(); h.callbacks.overlay.mockClear(); h.socket.deliver(message());
    expect(h.callbacks.overlay).toHaveBeenCalledWith(message());
    h.socket.deliver(message()); expect(h.callbacks.overlay).toHaveBeenCalledOnce();
    h.socket.deliver({ ...message(2), overlay: null }); expect(h.callbacks.overlay).toHaveBeenLastCalledWith(null);
    expect(h.callbacks.error).not.toHaveBeenCalled();
  });
  it.each([{ seq: 0 }, { context_ref: "other-context" }, { tab_id: "t2" }, { frame_id: 2 }, { media_epoch: "retired" }, { generation: 3 }, { control_epoch: 4 }, { document_version: 5 }, { frame_identity: "other-document" }, { viewport: { width: 900, height: 600 } }])("ignores stale/invalid presentation metadata without stopping video %j", async update => {
    const h = harness(); ready(h); await h.display(); h.callbacks.overlay.mockClear(); h.socket.deliver({ ...message(), ...update });
    expect(h.callbacks.overlay).not.toHaveBeenCalled(); expect(h.callbacks.error).not.toHaveBeenCalled(); expect(h.peers[0]!.close).not.toHaveBeenCalled();
    h.socket.deliver(message(2)); expect(h.callbacks.overlay).toHaveBeenCalledWith(message(2));
  });
  it.each(["human", "document", "viewport", "navigation", "reset", "suspend", "disconnect", "close"])("revokes Agent feedback immediately on %s", async transition => {
    const h = harness(); ready(h); await h.display(); h.socket.deliver(message()); h.callbacks.overlay.mockClear();
    if (transition === "human") h.socket.deliver({ type: "state", state: { ...agentState, control: "human" } });
    if (transition === "document") h.socket.deliver({ type: "state", state: { ...agentState, document_version: 5 } });
    if (transition === "viewport") h.socket.deliver({ type: "state", state: { ...agentState, viewport: { width: 900, height: 600 } } });
    if (transition === "navigation") h.socket.deliver({ type: "state", state: { ...agentState, url: "https://another.test", frame_identity: "2:t1:5" } });
    if (transition === "reset") h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch });
    if (transition === "suspend") h.live.suspendPresentation();
    if (transition === "disconnect") { h.peers[0]!.connectionState = "disconnected"; h.peers[0]!.onconnectionstatechange?.(); }
    if (transition === "close") h.live.close();
    expect(h.callbacks.overlay).toHaveBeenCalledWith(null); h.callbacks.overlay.mockClear(); h.socket.deliver(message(2));
    expect(h.callbacks.overlay).not.toHaveBeenCalled();
  });
  it("preserves current marks across unchanged state updates and drops preceding action feedback", async () => {
    const h = harness(); ready(h); await h.display(); h.socket.deliver(message()); h.callbacks.overlay.mockClear();
    h.socket.deliver({ type: "state", state: { ...agentState } }); expect(h.callbacks.overlay).not.toHaveBeenCalled();
    h.socket.deliver({ type: "state", state: { ...agentState, activity: { last_action: { ...message().overlay.last_action, seq: 10 }, recent_actions: [] } } });
    expect(h.callbacks.overlay).toHaveBeenLastCalledWith(null); h.callbacks.overlay.mockClear(); h.socket.deliver(message(2));
    expect(h.callbacks.overlay).not.toHaveBeenCalled();
  });
  it("hides marks when a warm Sheet is hidden and requires a fresh visible presentation", async () => {
    const h = harness(); ready(h, { runtime: "wayland", view_visibility: true });
    expect(h.live.setViewVisibility(true)).toBe(1); h.socket.deliver({ type: "view_visibility", visible: true, revision: 1 });
    await h.display(); h.socket.deliver(message()); h.callbacks.overlay.mockClear();
    expect(h.live.setViewVisibility(false)).toBe(2); expect(h.callbacks.overlay).toHaveBeenLastCalledWith(null);
    h.callbacks.overlay.mockClear(); h.socket.deliver(message(2)); expect(h.callbacks.overlay).not.toHaveBeenCalled();
    expect(h.live.setViewVisibility(true)).toBe(3); h.socket.deliver({ type: "view_visibility", visible: true, revision: 3 });
    h.callbacks.overlay.mockClear(); h.socket.deliver(message(3)); expect(h.callbacks.overlay).not.toHaveBeenCalled();
    expect(h.live.present(frame, frame.pixel_width, frame.pixel_height)).toBe(true);
    h.socket.deliver(message(4)); expect(h.callbacks.overlay).toHaveBeenCalledWith(message(4));
  });
});

describe("Web-port relay-only receiver", () => {
  it("fails closed before constructing a peer when the relay provider is missing", () => {
    const h = harness(2, { resolveRelayConfiguration: undefined }); h.ready(); h.offer();
    expect(h.factory).not.toHaveBeenCalled(); expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_MEDIA_UNAVAILABLE" }));
    expect(h.socket.close).toHaveBeenCalled();
  });
  it("replaces worker ICE settings with a fresh same-origin TCP relay grant, never sending it back to the worker", async () => {
    const provider = vi.fn(async next => relayGrant(next));
    const h = harness(2, { resolveRelayConfiguration: provider }); h.ready();
    h.socket.deliver({ type: "media_offer", frame, sdp: "offer-sdp", ice_servers: [{ urls: "turn:old-worker.example:3478", username: "worker-credential", credential: "unused-worker-secret" }] });
    await vi.waitFor(() => expect(h.factory).toHaveBeenCalledOnce());
    expect(provider).toHaveBeenCalledWith(frame, expect.any(AbortSignal));
    expect(h.factory).toHaveBeenCalledWith({ iceServers: relayGrant(frame).ice_servers, iceTransportPolicy: "relay", bundlePolicy: "max-bundle" });
    expect(JSON.stringify(h.socket.messages())).not.toMatch(/synthetic-relay-secret|fixture-view|unused-worker-secret|worker-credential/);
    const next = { ...frame, frame_id: 2, media_epoch: "media-2" }; await h.display(next);
    expect(provider).toHaveBeenCalledTimes(2); expect(provider.mock.calls[1]?.[0]).toEqual(next);
  });
  it("does not disclose a failed credential request or fall back to worker direct media", async () => {
    const h = harness(2, { resolveRelayConfiguration: async () => { throw new Error("synthetic-private-credential"); } }); h.ready(); h.offer();
    await vi.waitFor(() => expect(h.callbacks.error).toHaveBeenCalledOnce());
    expect(h.factory).not.toHaveBeenCalled(); expect(h.callbacks.error.mock.calls[0]?.[0]?.message).not.toContain("synthetic-private-credential");
    expect(h.socket.close).toHaveBeenCalled();
  });
  it.each(["other-origin", "other-page", "other-generation", "other-epoch", "direct-policy"])("rejects a %s grant before starting ICE", async change => {
    const provider = async (next: BrowserLiveFrame) => {
      const grant = relayGrant(next);
      if (change === "other-origin") grant.ice_servers[0]!.urls = "turns:other.example:443?transport=tcp";
      if (change === "other-page") grant.tab_id = "t2";
      if (change === "other-generation") grant.generation++;
      if (change === "other-epoch") grant.media_epoch = "another-media";
      if (change === "direct-policy") grant.ice_transport_policy = "all";
      return grant;
    };
    const h = harness(2, { resolveRelayConfiguration: provider }); h.ready(); h.offer();
    await vi.waitFor(() => expect(h.callbacks.error).toHaveBeenCalledOnce()); expect(h.factory).not.toHaveBeenCalled();
  });
  it.each(["reset", "blank", "generation", "control", "viewport", "close"])("cancels pending credential authority on %s and ignores a late reply", async transition => {
    let resolve!: (value: unknown) => void, signal!: AbortSignal;
    const h = harness(2, { resolveRelayConfiguration: (_frame, nextSignal) => { signal = nextSignal; return new Promise(done => { resolve = done; }); } });
    h.ready(); h.offer(); expect(h.factory).not.toHaveBeenCalled(); expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    if (transition === "reset") h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch });
    if (transition === "blank") h.socket.deliver({ type: "state", state: { ...state, url: "about:blank" } });
    if (transition === "generation") h.socket.deliver({ type: "state", state: { ...state, generation: 3 } });
    if (transition === "control") h.socket.deliver({ type: "state", state: { ...state, control_epoch: 4 } });
    if (transition === "viewport") h.socket.deliver({ type: "state", state: { ...state, viewport: { width: 900, height: 600 } } });
    if (transition === "close") h.live.close();
    expect(signal.aborted).toBe(true); resolve(relayGrant(frame)); await Promise.resolve(); await Promise.resolve();
    expect(h.factory).not.toHaveBeenCalled(); expect(h.callbacks.error).not.toHaveBeenCalled();
  });
  it("lets only the newest epoch use its own grant and candidates while a request is pending", async () => {
    let oldResolve!: (value: unknown) => void, oldSignal!: AbortSignal;
    const provider = vi.fn((next: BrowserLiveFrame, signal: AbortSignal) => next.media_epoch === frame.media_epoch ? new Promise(done => { oldResolve = done; oldSignal = signal; }) : Promise.resolve(relayGrant(next)));
    const h = harness(2, { resolveRelayConfiguration: provider }); h.ready(); h.offer();
    h.socket.deliver({ type: "media_candidate", media_epoch: frame.media_epoch, candidate: ice });
    const next = { ...frame, frame_id: 2, media_epoch: "media-2" }; h.offer(next);
    h.socket.deliver({ type: "media_candidate", media_epoch: next.media_epoch, candidate: null });
    expect(oldSignal.aborted).toBe(true); oldResolve(relayGrant(frame));
    await vi.waitFor(() => expect(h.peers).toHaveLength(1));
    await vi.waitFor(() => expect(h.peers[0]!.addIceCandidate).toHaveBeenCalledOnce());
    expect(h.peers[0]!.addIceCandidate.mock.calls).toEqual([[undefined]]); expect(h.callbacks.error).not.toHaveBeenCalled();
    expect(h.socket.messages().filter(message => message.type === "media_answer").map(message => message.media_epoch)).toEqual([next.media_epoch]);
  });
  it("keeps the media deadline active during credential acquisition", async () => {
    vi.useFakeTimers();
    const started = performance.now();
    const h = harness(2, { resolveRelayConfiguration: () => new Promise(() => {}) }); h.ready(); h.offer();
    await vi.advanceTimersByTimeAsync(15000);
    expect(h.factory).not.toHaveBeenCalled(); expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_MEDIA_UNAVAILABLE" }));
    expect(performance.now() - started).toBeGreaterThanOrEqual(15000);
  });
  it("includes credential acquisition in the measured time to the first presented frame", async () => {
    let resolve!: (value: unknown) => void, clock = 100;
    const now = vi.spyOn(performance, "now").mockImplementation(() => clock);
    try {
      const h = harness(2, { resolveRelayConfiguration: () => new Promise(done => { resolve = done; }) }); h.ready(); h.offer();
      clock = 500; resolve(relayGrant(frame));
      await vi.waitFor(() => expect(h.peers[0]!.setLocalDescription).toHaveBeenCalled());
      h.peers[0]!.track(); await vi.waitFor(() => expect(h.callbacks.media).toHaveBeenCalled());
      clock = 700; expect(h.live.present(frame, frame.pixel_width, frame.pixel_height)).toBe(true);
      expect((await h.live.diagnostics())?.first_frame_ms).toBe(600);
    } finally { now.mockRestore(); }
  });
});

describe("browser live binding and media contracts", () => {
  it("negotiates per-page density before sending an optional viewport field", async () => {
    const h = harness();
    h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { runtime: "wayland", render_scale_limit: 2 }, state });
    await h.display();
    expect(h.callbacks.configured).toHaveBeenCalledWith({ runtime: "wayland", renderScaleLimit: 2, codecPreferences: ["standard"], codecPreference: "standard" });
    expect(h.live.viewport(800, 600, authority, 2)).toBe(1);
    expect(h.socket.messages()[h.socket.messages().length - 1]).toMatchObject({ type: "viewport", width: 800, height: 600, preferred_scale: 2 });
    const old = harness(); old.ready(); await old.display();
    expect(old.live.viewport(800, 600, authority, 2)).toBe(false);
    expect(old.live.viewport(800, 600, authority)).toBe(1);
    expect(old.socket.messages()[old.socket.messages().length - 1]).not.toHaveProperty("preferred_scale");
  });
  it.each([0, 3, NaN, "2"])("ignores an invalid negotiated density limit (%s)", limit => {
    const h = harness(); h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { runtime: "wayland", render_scale_limit: limit }, state });
    expect(h.callbacks.configured.mock.calls[h.callbacks.configured.mock.calls.length - 1]?.[0]).not.toHaveProperty("renderScaleLimit");
  });
  it("negotiates one Wayland media path without requesting a stale codec preference", () => {
    const h = harness(2, { codecPreference: "responsive" });
    h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { runtime: "wayland", performance: true, codec_preferences: ["standard", "responsive"] }, state });
    expect(h.socket.messages()).toEqual([{ type: "media_config", scale: 2, performance: true }]);
    expect(h.callbacks.configured).toHaveBeenCalledWith({ runtime: "wayland", codecPreferences: ["standard"], codecPreference: "standard" });
    const sibling = harness(); sibling.ready();
    expect(sibling.callbacks.configured).toHaveBeenCalledWith({ codecPreferences: ["standard"], codecPreference: "standard" });
  });
  it("opts in to performance telemetry only when the worker advertises it", () => {
    const older = harness(); older.ready();
    expect(older.socket.messages()).toEqual([{ type: "media_config", scale: 2 }]);
    const modern = harness(); modern.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { performance: true }, state });
    expect(modern.socket.messages()).toEqual([{ type: "media_config", scale: 2, performance: true }]);
    const unknown = harness(); unknown.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { performance: "true" }, state });
    expect(unknown.socket.messages()).toEqual([{ type: "media_config", scale: 2 }]);
  });
  it("leaves standard media_config unchanged and only requests Responsive after advertised support", () => {
    const standard = harness(); standard.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { performance: true, codec_preferences: ["standard", "responsive"] }, state });
    expect(standard.socket.messages()).toEqual([{ type: "media_config", scale: 2, performance: true }]);
    expect(standard.callbacks.configured).toHaveBeenCalledWith({ codecPreferences: ["standard", "responsive"], codecPreference: "standard" });
    const responsive = harness(2, { codecPreference: "responsive" }); responsive.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { codec_preferences: ["standard", "responsive"] }, state });
    expect(responsive.socket.messages()).toEqual([{ type: "media_config", scale: 2, codec_preference: "responsive" }]);
    expect(responsive.callbacks.configured).toHaveBeenCalledWith({ codecPreferences: ["standard", "responsive"], codecPreference: "responsive" });
    // Selection belongs to this receiver, not a module-global or origin setting.
    const anotherTab = harness(); anotherTab.ready(); expect(anotherTab.socket.messages()).toEqual([{ type: "media_config", scale: 2 }]);
  });
  it.each([undefined, "responsive", [], ["standard"]])("falls back to Standard on an older/unsupported worker (%s)", codecPreferences => {
    const h = harness(2, { codecPreference: "responsive" }); h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { codec_preferences: codecPreferences }, state });
    expect(h.socket.messages()).toEqual([{ type: "media_config", scale: 2 }]);
    expect(h.callbacks.configured).toHaveBeenCalledWith({ codecPreferences: ["standard"], codecPreference: "standard" });
    expect(h.callbacks.error).not.toHaveBeenCalled();
  });
  it("uses a bound short-lived ticket, never a service credential", () => {
    const url = new URL(browserLiveURL("https://pogo.example", "session/a", { ...target, sessionId: "internal-sheet-only" } as typeof target, "once"));
    expect(url.protocol).toBe("wss:"); expect(url.pathname).toBe("/api/sessions/session%2Fa/browser/live");
    expect(url.searchParams.get("ticket")).toBe("once"); expect([...url.searchParams.keys()].sort()).toEqual(["context_ref", "tab_id", "ticket"]);
  });
  it("binds control changes and live tickets to the exact Page", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(state), { headers: { "Content-Type": "application/json" } }));
    try {
      await api.browserControl("session-a", "human", { ...target, force: true });
      expect(JSON.parse(fetch.mock.calls[0]![1]!.body as string)).toEqual({ mode: "human", ...target, force: true });
      fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ticket: "one", expires_in: 30 }), { headers: { "Content-Type": "application/json" } }));
      await api.browserLiveTicket("session-a", { ...target, sessionId: "internal-sheet-only" } as typeof target);
      expect(JSON.parse(fetch.mock.calls[1]![1]!.body as string)).toEqual(target);
    } finally { fetch.mockRestore(); }
  });
  it("separates layout pixels from encoded pixels without claiming DOM evidence", () => {
    expect(liveFrameMatches(frame, state, target)).toBe(true);
    expect(liveFrameMatches(frame, { ...state, version: "different-dom-revision", document_version: 5 }, target)).toBe(true);
    for (const update of [{ generation: 3 }, { control_epoch: 4 }, { frame_identity: "other" }, { context_ref: "other" }, { viewed_tab: "t2" }, { viewport: { width: 801, height: 600 } }]) expect(liveFrameMatches(frame, { ...state, ...update }, target)).toBe(false);
    expect(readLiveFrame(frame, target)).toEqual(frame);
    for (const update of [{ type: "frame" }, { data: "AQ==" }, { frame_id: 0 }, { frame_id: "1" }, { tab_id: "t2" }, { generation: 2.2 }, { media_epoch: "bad epoch" }, { pixel_width: 0 }, { pixel_height: 9000 }, { viewport: { width: 9000, height: 600 } }]) expect(readLiveFrame({ ...frame, ...update }, target)).toBeNull();
  });
  it.each([[0, 1], [3, 2], [1.5, 1.5], [Number.NaN, 1]])("bounds scale %s", (scale, expected) => {
    const h = harness(scale); h.ready(); expect(h.socket.messages()).toEqual([{ type: "media_config", scale: expected }]); expect(h.callbacks.ready).toHaveBeenCalledExactlyOnceWith(state);
  });
  it("rejects protocol 1 without silently falling back to images", () => {
    const h = harness(); h.socket.deliver({ type: "ready", protocol: 1, state });
    expect(h.callbacks.ready).not.toHaveBeenCalled(); expect(h.socket.close).toHaveBeenCalledWith(4002);
  });
  it("never treats negotiation, reception, or mounting as presentation", async () => {
    const h = harness(); h.ready(); h.offer(); await vi.waitFor(() => expect(h.peers[0]!.setLocalDescription).toHaveBeenCalled());
    h.peers[0]!.track(); await vi.waitFor(() => expect(h.callbacks.media).toHaveBeenCalled());
    expect(h.socket.messages().some(item => item.type === "presentation_ack")).toBe(false);
    expect(h.live.input({ type: "text", text: "too early" }, authority)).toBe(false);
    const received = h.callbacks.media.mock.calls[0]![1];
    expect(h.live.present(received, 800, 600)).toBe(false); expect(h.live.present({ ...received, media_epoch: "wrong" }, 1600, 1200)).toBe(false);
    expect(h.live.present(received, 1600, 1200)).toBe(true);
    expect(h.socket.messages().slice(-1)[0]).toEqual({ type: "presentation_ack", frame_id: 1, media_epoch: "media-1", width: 1600, height: 1200 });
    expect(h.live.present(received, 1600, 1200)).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "presentation_ack")).toHaveLength(1);
    expect(h.live.input({ type: "text", text: "中文" }, authority)).toBe(true);
  });
});

describe("isolated media epoch lifecycle", () => {
  it("uses a fresh peer after reset and rejects late ontrack/presentation", async () => {
    const h = harness(); h.ready(); const oldFrame = await h.display(); const old = h.peers[0]!, lateTrack = old.ontrack!;
    h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch });
    expect(old.close).toHaveBeenCalled(); expect(h.callbacks.mediaClosed).toHaveBeenCalled(); expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    expect(h.live.present(oldFrame, 1600, 1200)).toBe(false);
    const late = { kind: "video", stop: vi.fn() }; lateTrack({ track: late, streams: [] } as unknown as RTCTrackEvent);
    expect(late.stop).toHaveBeenCalled(); expect(h.callbacks.media).toHaveBeenCalledTimes(1);
    h.offer(frame); expect(h.peers).toHaveLength(1);
    await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" }); expect(h.peers).toHaveLength(2);
    expect(h.live.input({ type: "reset" }, authority)).toBe(false); expect(h.live.input({ type: "reset" }, { ...authority, frame_id: 2, media_epoch: "media-2" })).toBe(true);
  });
  it("reset retires only the named old epoch, then a fresh epoch can negotiate and present", async () => {
    const h = harness(); h.ready(); await h.display();
    h.socket.deliver({ type: "media_reset", media_epoch: "media-1" });
    const next = await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" });
    expect(h.peers).toHaveLength(2); expect(h.live.present(next, 1600, 1200)).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "presentation_ack").map(item => item.media_epoch)).toEqual(["media-1", "media-2"]);
  });
  it("a reset for a future epoch retires it and must not be used as a prepare-offer message", async () => {
    const h = harness(); h.ready();
    h.socket.deliver({ type: "media_reset", media_epoch: "future-epoch" });
    h.offer({ ...frame, frame_id: 2, media_epoch: "future-epoch" });
    expect(h.peers).toHaveLength(0); expect(h.socket.messages().some(item => item.type === "media_answer")).toBe(false);
    h.offer({ ...frame, frame_id: 3, media_epoch: "genuinely-new" });
    await vi.waitFor(() => expect(h.peers).toHaveLength(1));
  });
  it("ignores stale offers and old reset/candidates without disturbing the current epoch", async () => {
    const h = harness(); h.ready(); await h.display({ ...frame, frame_id: 3, media_epoch: "media-3" });
    h.offer({ ...frame, frame_id: 2, media_epoch: "media-2" }); h.socket.deliver({ type: "media_reset", media_epoch: "media-1" }); h.socket.deliver({ type: "media_candidate", media_epoch: "media-1", candidate: ice });
    expect(h.peers).toHaveLength(1); expect(h.peers[0]!.close).not.toHaveBeenCalled(); expect(h.live.input({ type: "reset" }, { ...authority, frame_id: 3, media_epoch: "media-3" })).toBe(true);
  });
  it.each([{ generation: 3 }, { control_epoch: 4 }, { frame_identity: "2:t1:5", document_version: 5 }, { viewport: { width: 900, height: 600 } }])("revokes presentation as state changes %j", async change => {
    const h = harness(); h.ready(); const oldFrame = await h.display(); h.socket.deliver({ type: "state", state: { ...state, ...change } });
    expect(h.peers[0]!.close).toHaveBeenCalled(); expect(h.live.present(oldFrame, 1600, 1200)).toBe(false); expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    h.offer({ ...frame, frame_id: 2, media_epoch: "old-state-offer" }); expect(h.peers).toHaveLength(1);
  });
  it("does not roll state backward or fence media for mutation-only revisions", async () => {
    const h = harness(); h.ready(); await h.display();
    h.socket.deliver({ type: "state", state: { ...state, document_version: 5, version: "new-dom" } });
    h.socket.deliver({ type: "state", state: { ...state, control_epoch: 2 } }); h.socket.deliver({ type: "state", state });
    expect(h.callbacks.state).toHaveBeenCalledTimes(1); expect(h.peers[0]!.close).not.toHaveBeenCalled(); expect(h.live.input({ type: "reset" }, authority)).toBe(true);
  });
  it("queues early ICE until remote description is installed", async () => {
    const h = harness(); h.ready(); h.socket.deliver({ type: "media_candidate", media_epoch: frame.media_epoch, candidate: ice }); h.offer();
    h.socket.deliver({ type: "media_candidate", media_epoch: frame.media_epoch, candidate: null });
    await vi.waitFor(() => expect(h.peers).toHaveLength(1)); const pc = h.peers[0]!;
    await vi.waitFor(() => expect(pc.addIceCandidate).toHaveBeenCalledTimes(2)); expect(pc.addIceCandidate.mock.calls).toEqual([[ice], [undefined]]);
  });
  it("never sends a late answer when an asynchronous negotiation is reset", async () => {
    const h = harness(), pc = new Peer(); let resolve!: (value: RTCSessionDescriptionInit) => void;
    pc.createAnswer.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    h.factory.mockImplementationOnce(() => { h.peers.push(pc); return pc as unknown as RTCPeerConnection; });
    h.ready(); h.offer(); await vi.waitFor(() => expect(pc.createAnswer).toHaveBeenCalled());
    h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch }); resolve({ type: "answer", sdp: "late" }); await Promise.resolve(); await Promise.resolve();
    expect(pc.setLocalDescription).not.toHaveBeenCalled(); expect(h.socket.messages().some(item => item.type === "media_answer")).toBe(false);
  });
  it.each(["failed", "closed"])("revokes media %s while the WebSocket is open", async connectionState => {
    const h = harness(); h.ready(); await h.display(); h.peers[0]!.connectionState = connectionState; h.peers[0]!.onconnectionstatechange?.();
    expect(h.live.ready).toBe(false); expect(h.live.input({ type: "reset" }, authority)).toBe(false); expect(h.socket.close).toHaveBeenCalled(); expect(h.callbacks.mediaClosed).toHaveBeenCalled(); expect(h.callbacks.close).toHaveBeenCalledOnce();
  });
  it("requires a fresh connection and fresh presentation after socket loss", async () => {
    const h = harness(); h.ready(); const old = await h.display(); h.socket.onclose?.(); h.ready(); h.offer({ ...frame, frame_id: 2, media_epoch: "replay" });
    expect(h.peers).toHaveLength(1); expect(h.live.present(old, 1600, 1200)).toBe(false); expect(h.live.ready).toBe(false);
    const other = harness(); other.ready(); expect(other.live.input({ type: "text", text: "not replayed" }, authority)).toBe(false); await other.display(); expect(other.live.input({ type: "text", text: "fresh" }, authority)).toBe(true);
  });
  it("rejects another track being spliced into the same epoch", async () => {
    const h = harness(); h.ready(); await h.display(); h.peers[0]!.track(); expect(h.callbacks.close).toHaveBeenCalled(); expect(h.live.ready).toBe(false);
  });
  it("returns counters without SDP, candidates, or credentials", async () => {
    const h = harness(); h.ready(); await h.display();
    h.peers[0]!.getStats.mockResolvedValue(new Map([["video", { type: "inbound-rtp", kind: "video", framesDecoded: 9, bytesReceived: 1000, codecId: "secret", address: "private", sdp: "secret" }], ["candidate", { type: "local-candidate", address: "private" }]]));
    expect(await h.live.diagnostics()).toMatchObject({ transport: "webrtc", media_epoch: "media-1", connection: "new", presented: true, video: [{ framesDecoded: 9, bytesReceived: 1000 }], input: { count: 0 }, wheel: { received: 0, dispatched: 0 } }); h.live.close(); expect(await h.live.diagnostics()).toBeNull();
  });
  it("pauses input during a brief ICE interruption without rebuilding the presented peer or replaying scroll", async () => {
    const h = harness(); h.ready(); await h.display(); vi.useFakeTimers();
    const wheel = { type: "wheel" as const, x: 1, y: 2, delta_x: 0, delta_y: 30, modifiers: 0 };
    h.live.input(wheel, authority); h.live.input(wheel, authority);
    const pc = h.peers[0]!; pc.connectionState = "disconnected"; pc.onconnectionstatechange?.();
    expect(h.live.input({ type: "text", text: "not buffered" }, authority)).toBe(false);
    await vi.advanceTimersByTimeAsync(700); expect(h.socket.close).not.toHaveBeenCalled();
    pc.connectionState = "connected"; pc.onconnectionstatechange?.();
    await vi.advanceTimersByTimeAsync(200); expect(h.socket.close).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 1 }); await vi.advanceTimersByTimeAsync(30);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(1);
    expect(h.live.input({ type: "text", text: "before resumed frame" }, authority)).toBe(false);
    expect(h.live.present(frame, frame.pixel_width, frame.pixel_height)).toBe(true);
    expect(h.live.input({ type: "text", text: "fresh" }, authority)).toBe(true);
  });
  it("closes a disconnected peer after a bounded grace period", async () => {
    const h = harness(); h.ready(); await h.display(); vi.useFakeTimers();
    h.peers[0]!.iceConnectionState = "disconnected"; h.peers[0]!.oniceconnectionstatechange?.();
    await vi.advanceTimersByTimeAsync(801); expect(h.live.ready).toBe(false); expect(h.callbacks.mediaClosed).toHaveBeenCalled();
  });
  it("ignores a rejected pre-interruption input without clearing a fresh wheel gesture on the recovered peer", async () => {
    const h = harness(); h.ready(); await h.display(); vi.useFakeTimers();
    const wheel = { type: "wheel" as const, x: 1, y: 2, delta_x: 0, delta_y: 30, modifiers: 0 };
    h.live.input(wheel, authority);
    const pc = h.peers[0]!; pc.connectionState = "disconnected"; pc.onconnectionstatechange?.();
    pc.connectionState = "connected"; pc.onconnectionstatechange?.(); h.live.present(frame, frame.pixel_width, frame.pixel_height);
    h.live.input(wheel, authority); h.live.input(wheel, authority);
    h.socket.deliver({ type: "error", seq: 1, code: "LIVE_BUSY", error: "old connection input discarded" });
    await vi.advanceTimersByTimeAsync(20);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(3);
    expect(h.callbacks.error).not.toHaveBeenCalled();
  });
  it("measures only matching input ACKs, accepts safe server metrics and never labels ACK as frame latency", async () => {
    const h = harness(); h.ready(); await h.display();
    const now = vi.spyOn(performance, "now"); now.mockReturnValue(100);
    h.live.input({ type: "text", text: "measured" }, authority); now.mockReturnValue(140);
    h.socket.deliver({ type: "ack", seq: 1, timing: { queue_ms: 3, dispatch_ms: 7 } });
    h.socket.deliver({ type: "ack", seq: 999, timing: { queue_ms: 900 } });
    h.socket.deliver({ type: "performance", media_epoch: frame.media_epoch, stats: { profile: "interactive", adaptation: "bandwidth", encode_ms_per_frame: 5, codec: "VP8", fps: 50, sdp: "secret", width: null, rtt_ms: -1 } });
    const result = await h.live.diagnostics();
    expect(result?.input).toEqual({ count: 1, last_ms: 40, p50_ms: 40, p95_ms: 40, queue_ms: 3, dispatch_ms: 7 });
    expect(result?.server).toEqual({ profile: "interactive", adaptation: "bandwidth", encode_ms_per_frame: 5, codec: "VP8", fps: 50 });
    h.socket.deliver({ type: "performance", media_epoch: "old", stats: { fps: 1 } }); expect((await h.live.diagnostics())?.server?.fps).toBe(50);
    now.mockRestore();
  });
  it("keeps optional capture settings distinct from observed received frame rate and unknown values", async () => {
    const h = harness(); h.ready(); await h.display();
    h.socket.deliver({ type: "performance", media_epoch: frame.media_epoch, stats: { capture_fps: 60, capture_min_fps: 15, capture_tuning_supported: true, content_hint: "motion" } });
    const result = await h.live.diagnostics();
    expect(result?.server).toEqual({ capture_fps: 60, capture_min_fps: 15, capture_tuning_supported: true, content_hint: "motion" });
    expect(result?.metrics.fps).toBeUndefined();
    h.socket.deliver({ type: "performance", media_epoch: frame.media_epoch, stats: { capture_fps: null, capture_min_fps: null, capture_tuning_supported: false, content_hint: "unexpected-private-text" } });
    expect((await h.live.diagnostics())?.server).toEqual({ capture_tuning_supported: false });
  });
  it("measures first frame from its media offer, excluding arbitrary blank-page idle time", async () => {
    let clock = 100; const now = vi.spyOn(performance, "now").mockImplementation(() => clock);
    try {
      const h = harness(); h.ready({ ...state, url: "about:blank" });
      clock = 20100; // The user leaves the new-tab page open for twenty seconds.
      h.socket.deliver({ type: "state", state }); h.offer();
      await vi.waitFor(() => expect(h.peers[0]!.setLocalDescription).toHaveBeenCalled());
      h.peers[0]!.track(); await vi.waitFor(() => expect(h.callbacks.media).toHaveBeenCalled());
      expect((await h.live.diagnostics())?.first_frame_ms).toBeUndefined();
      clock = 20675;
      expect(h.live.present(frame, frame.pixel_width, frame.pixel_height)).toBe(true);
      expect((await h.live.diagnostics())?.first_frame_ms).toBe(575);
    } finally { now.mockRestore(); }
  });
  it("preserves wheel-before-click order within the ACK window and never replays after an epoch change", async () => {
    const h = harness(); h.ready(); await h.display();
    const wheel = { type: "wheel" as const, x: 10, y: 20, delta_x: 0, delta_y: 10, modifiers: 0 };
    h.live.input(wheel, authority); h.live.input(wheel, authority); h.live.input(wheel, authority);
    const click = { type: "pointer" as const, phase: "down" as const, x: 10, y: 20, button: "left" as const, buttons: 1, modifiers: 0, click_count: 1 };
    h.live.input(click, authority);
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event)).toEqual([wheel, { ...wheel, delta_y: 20 }]);
    h.socket.deliver({ type: "ack", seq: 1 });
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event)).toEqual([wheel, { ...wheel, delta_y: 20 }, click]);
    h.live.input(wheel, authority); h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch });
    h.socket.deliver({ type: "ack", seq: 1 }); h.socket.deliver({ type: "ack", seq: 2 });
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(3);
  });
});

describe("safe media preference restart boundary", () => {
  it("waits for all accepted wheel/text/release ACKs without clearing or replaying their queue", async () => {
    const h = harness(); h.ready(); await h.display();
    const down = { type: "pointer" as const, phase: "down" as const, x: 10, y: 20, button: "left" as const, buttons: 1, modifiers: 0, click_count: 1 };
    const events = [down, { type: "wheel" as const, x: 10, y: 20, delta_x: 0, delta_y: 45, modifiers: 0 }, { type: "text" as const, text: "不丢输入" }, { ...down, phase: "up" as const, buttons: 0 }];
    events.forEach(event => h.live.input(event, authority));
    const done = vi.fn(); const pending = h.live.waitForInputIdle().then(done);
    expect(h.live.inputIdle).toBe(false);
    for (let seq = 1; seq < 4; seq++) { h.socket.deliver({ type: "ack", seq }); await Promise.resolve(); expect(done).not.toHaveBeenCalled(); }
    h.socket.deliver({ type: "ack", seq: 4 }); await pending;
    expect(done).toHaveBeenCalledExactlyOnceWith(true); expect(h.live.inputIdle).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event)).toEqual(events);
    expect(h.socket.close).not.toHaveBeenCalled();
  });
  it("keeps waiting after keydown ACK until its keyup is also acknowledged", async () => {
    const h = harness(); h.ready(); await h.display();
    const key = { type: "key" as const, phase: "down" as const, key: "Shift", code: "ShiftLeft", key_code: 16, modifiers: 8, repeat: false };
    h.live.input(key, authority); h.socket.deliver({ type: "ack", seq: 1 });
    const done = vi.fn(); const pending = h.live.waitForInputIdle().then(done); await Promise.resolve(); expect(done).not.toHaveBeenCalled();
    h.live.input({ ...key, phase: "up", modifiers: 0 }, authority); await Promise.resolve(); expect(done).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 2 }); await pending; expect(done).toHaveBeenCalledExactlyOnceWith(true);
  });
  it("also waits for a pending display resize and never takes control of a readonly page", async () => {
    const h = harness(); h.ready({ ...state, control: "agent" }); await h.display();
    const display = { generation: 2, control_epoch: 3, frame_id: 1, media_epoch: "media-1" };
    expect(h.live.viewport(1440, 900, display)).toBe(1);
    const done = vi.fn(); const pending = h.live.waitForInputIdle().then(done); await Promise.resolve(); expect(done).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 1 }); await pending; expect(done).toHaveBeenCalledWith(true);
    expect(h.socket.messages().some(item => item.type === "input")).toBe(false);
  });
  it("cancels a mode-switch wait when the last current viewport is rejected", async () => {
    const h = harness(); h.ready(); await h.display(); expect(h.live.viewport(1440, 900, authority)).toBe(1);
    const pending = h.live.waitForInputIdle(); h.socket.deliver({ type: "error", seq: 1, code: "LIVE_BUSY", error: "viewport is busy" });
    expect(await pending).toBe(false); expect(h.live.inputIdle).toBe(true); expect(h.socket.close).not.toHaveBeenCalled();
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_BUSY", seq: 1 }));
  });
  it("does not cancel a new receiver's drain for a late old-epoch viewport error", async () => {
    const h = harness(); h.ready(); await h.display(); h.live.viewport(1440, 900, authority);
    const old = h.live.waitForInputIdle(); h.socket.deliver({ type: "media_reset", media_epoch: "media-1" }); expect(await old).toBe(false);
    await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" });
    h.live.input({ type: "text", text: "current input" }, { ...authority, frame_id: 2, media_epoch: "media-2" });
    const done = vi.fn(), pending = h.live.waitForInputIdle().then(done);
    h.socket.deliver({ type: "error", seq: 1, code: "LIVE_BUSY", error: "old viewport failed" }); await Promise.resolve(); expect(done).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 2 }); await pending; expect(done).toHaveBeenCalledWith(true);
  });
  it("aborting mode selection leaves the current queue intact", async () => {
    const h = harness(); h.ready(); await h.display(); for (let i = 0; i < 4; i++) h.live.input({ type: "text", text: String(i) }, authority);
    const controller = new AbortController(), pending = h.live.waitForInputIdle(controller.signal); controller.abort(); expect(await pending).toBe(false);
    h.socket.deliver({ type: "ack", seq: 1 }); h.socket.deliver({ type: "ack", seq: 2 });
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["0", "1", "2", "3"]);
    expect(h.socket.close).not.toHaveBeenCalled();
  });
  it("cancels an old receiver's wait on retirement and ignores old ACKs for the next receiver", async () => {
    const h = harness(); h.ready(); await h.display(); h.live.input({ type: "text", text: "old" }, authority);
    const pending = h.live.waitForInputIdle(); h.socket.deliver({ type: "media_reset", media_epoch: "media-1" }); expect(await pending).toBe(false);
    await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" }); h.live.input({ type: "text", text: "new" }, { ...authority, frame_id: 2, media_epoch: "media-2" });
    const done = vi.fn(), next = h.live.waitForInputIdle().then(done); h.socket.deliver({ type: "ack", seq: 1 }); await Promise.resolve(); expect(done).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 2 }); await next; expect(done).toHaveBeenCalledWith(true);
  });
  it("cancels a mode switch during an ICE interruption without letting its late ACK restart it", async () => {
    const h = harness(); h.ready(); await h.display(); h.live.input({ type: "text", text: "old connection" }, authority);
    const pending = h.live.waitForInputIdle(); const peer = h.peers[0]!; peer.connectionState = "disconnected"; peer.onconnectionstatechange?.();
    expect(await pending).toBe(false); peer.connectionState = "connected"; peer.onconnectionstatechange?.();
    h.socket.deliver({ type: "ack", seq: 1 }); expect(h.socket.close).not.toHaveBeenCalled();
    expect(h.live.present(frame, frame.pixel_width, frame.pixel_height)).toBe(true); expect(await h.live.waitForInputIdle()).toBe(true);
  });
  it("fails the wait on an input error or closed connection, including a blank page", async () => {
    const h = harness(); h.ready(); await h.display(); h.live.input({ type: "text", text: "once" }, authority);
    const pending = h.live.waitForInputIdle(); h.socket.deliver({ type: "error", seq: 1, code: "LIVE_BUSY", error: "busy" }); expect(await pending).toBe(false);
    const blank = harness(); blank.ready({ ...state, url: "about:blank" }); expect(await blank.live.waitForInputIdle()).toBe(true); blank.live.close(); expect(await blank.live.waitForInputIdle()).toBe(false);
  });
});

describe("warm Sheet visibility and reliable release", () => {
  const enable = (h: ReturnType<typeof harness>) => h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { runtime: "wayland", view_visibility: true }, state });
  it("negotiates visibility only with the Wayland capability, preserving old wire messages", () => {
    const old = harness(); old.ready(); expect(old.live.setViewVisibility(false)).toBe(false);
    expect(old.socket.messages()).toEqual([{ type: "media_config", scale: 2 }]);
    const wrongRuntime = harness(); wrongRuntime.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { view_visibility: true }, state });
    expect(wrongRuntime.live.setViewVisibility(false)).toBe(false);
    const modern = harness(); enable(modern);
    expect(modern.callbacks.configured).toHaveBeenCalledWith({ runtime: "wayland", codecPreferences: ["standard"], codecPreference: "standard", viewVisibility: true });
  });
  it("reuses the same receiver but requires latest show ACK and a new presentation", async () => {
    const h = harness(); enable(h); await h.display();
    h.live.suspendPresentation(); expect(h.live.present(frame, 1600, 1200)).toBe(false);
    expect(h.live.setViewVisibility(false)).toBe(1);
    h.socket.deliver({ type: "view_visibility", visible: false, revision: 1 });
    expect(h.live.input({ type: "text", text: "hidden" }, authority)).toBe(false);
    expect(h.live.setViewVisibility(true)).toBe(2);
    expect(h.live.present(frame, 1600, 1200)).toBe(false);
    h.socket.deliver({ type: "view_visibility", visible: false, revision: 1 });
    expect(h.live.viewAcknowledged).toBe(false);
    h.socket.deliver({ type: "view_visibility", visible: true, revision: 2 });
    expect(h.live.input({ type: "text", text: "before fresh frame" }, authority)).toBe(false);
    expect(h.live.present(frame, 1600, 1200)).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "presentation_ack").slice(-1)[0]).toEqual({ type: "presentation_ack", frame_id: 1, media_epoch: "media-1", width: 1600, height: 1200, view_revision: 2 });
    expect(h.live.input({ type: "text", text: "current" }, authority)).toBe(true);
    expect(h.factory).toHaveBeenCalledTimes(1); expect(h.peers[0]!.close).not.toHaveBeenCalled();
  });
  it("ignores superseded visibility ACKs across rapid hide/show/hide", async () => {
    const h = harness(); enable(h); await h.display();
    expect(h.live.setViewVisibility(false)).toBe(1); expect(h.live.setViewVisibility(true)).toBe(2); expect(h.live.setViewVisibility(false)).toBe(3);
    h.socket.deliver({ type: "view_visibility", visible: true, revision: 2 });
    expect(h.callbacks.visibility).not.toHaveBeenCalled(); expect(h.live.present(frame, 1600, 1200)).toBe(false);
    h.socket.deliver({ type: "view_visibility", visible: false, revision: 3 });
    expect(h.callbacks.visibility).toHaveBeenCalledExactlyOnceWith(false, 3);
    expect(h.socket.close).not.toHaveBeenCalled();
  });
  it("suspends the media watchdog for hidden document changes without trusting cached frames", async () => {
    const h = harness(); enable(h); await h.display(); vi.useFakeTimers();
    h.live.suspendPresentation(); h.live.setViewVisibility(false);
    h.socket.deliver({ type: "state", state: { ...state, frame_identity: "2:t1:5", document_version: 5 } });
    await vi.advanceTimersByTimeAsync(16000);
    expect(h.socket.close).not.toHaveBeenCalled(); expect(h.live.present(frame, 1600, 1200)).toBe(false);
    h.live.setViewVisibility(true);
    h.socket.deliver({ type: "view_visibility", visible: true, revision: 2 });
    await vi.advanceTimersByTimeAsync(15000);
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_MEDIA_UNAVAILABLE" }));
  });
  it("drains accepted text/wheel before resetting an unreleased key, so hiding cannot deadlock", async () => {
    const h = harness(); enable(h); await h.display();
    const key = { type: "key" as const, phase: "down" as const, key: "Shift", code: "ShiftLeft", key_code: 16, modifiers: 8, repeat: false };
    const wheel = { type: "wheel" as const, x: 10, y: 20, delta_x: 0, delta_y: 60, modifiers: 0 };
    const events = [key, wheel, { type: "text" as const, text: "保留输入" }, { type: "text" as const, text: "第二条" }];
    events.forEach(event => expect(h.live.input(event, authority)).toBe(true));
    h.live.suspendPresentation(); expect(h.live.releaseHeldInput(authority)).toBe(false);
    const pending = h.live.waitForAcceptedInput();
    for (let seq = 1; seq <= 4; seq++) h.socket.deliver({ type: "ack", seq });
    expect(await pending).toBe(true); expect(h.live.inputIdle).toBe(false);
    expect(h.live.releaseHeldInput(authority)).toBe(true);
    const idle = h.live.waitForInputIdle(); h.socket.deliver({ type: "ack", seq: 5 });
    expect(await idle).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event)).toEqual([...events, { type: "reset" }]);
    h.live.setViewVisibility(false); expect(h.peers[0]!.close).not.toHaveBeenCalled();
  });
  it("does not reset an Agent-owned readonly tab while suspending its receiver", async () => {
    const h = harness(); h.socket.deliver({ type: "ready", protocol: 2, transport: "webrtc", capabilities: { runtime: "wayland", view_visibility: true }, state: { ...state, control: "agent" } });
    await h.display(); expect(await h.live.waitForAcceptedInput()).toBe(true); h.live.suspendPresentation(); h.live.setViewVisibility(false);
    expect(h.socket.messages().some(item => item.type === "input")).toBe(false);
  });
  it("preserves the accepted queue if a visibility drain is cancelled or its peer is retired", async () => {
    const h = harness(); enable(h); await h.display();
    for (let i = 0; i < 4; i++) h.live.input({ type: "text", text: String(i) }, authority);
    const controller = new AbortController(), drain = h.live.waitForAcceptedInput(controller.signal); controller.abort(); expect(await drain).toBe(false);
    for (let seq = 1; seq <= 4; seq++) h.socket.deliver({ type: "ack", seq });
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["0", "1", "2", "3"]);
    h.live.input({ type: "text", text: "old receiver" }, authority);
    const retired = h.live.waitForAcceptedInput(); h.socket.deliver({ type: "media_reset", media_epoch: "media-1" }); expect(await retired).toBe(false);
    expect(h.live.present(frame, 1600, 1200)).toBe(false);
  });
});

describe("display and input authority", () => {
  it("lets a presented Agent-owned page resize without a lease", async () => {
    const agentState = { ...state, control: "agent" as const }, display = liveDisplayAuthority(frame, agentState, target, "old-token", 3);
    expect(display).toEqual({ generation: 2, control_epoch: 3, frame_id: 1, media_epoch: "media-1" }); const h = harness(); h.ready(agentState);
    expect(h.live.viewport(1440, 900, display!)).toBe(false); await h.display(); expect(h.live.viewport(1440, 900, display!)).toBe(1);
    expect(h.socket.messages().slice(-1)[0]).toEqual({ type: "viewport", seq: 1, width: 1440, height: 900, ...display }); expect(h.live.input({ type: "text", text: "forbidden" }, { ...authority, lease_token: "" })).toBe(false);
  });
  it("defers resize until reliable mouse-up and text have actually been acknowledged", async () => {
    const h = harness(); h.ready(); await h.display();
    const down = { type: "pointer" as const, phase: "down" as const, x: 10, y: 20, button: "left" as const, buttons: 1, modifiers: 0, click_count: 1 };
    h.live.input(down, authority); h.live.input({ type: "text", text: "保存输入" }, authority); h.live.input({ ...down, phase: "up", buttons: 0 }, authority);
    expect(h.live.viewport(1440, 900, authority)).toBe(false);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(2);
    h.socket.deliver({ type: "ack", seq: 1 }); expect(h.live.viewport(1440, 900, authority)).toBe(false);
    h.socket.deliver({ type: "ack", seq: 2 }); expect(h.live.viewport(1440, 900, authority)).toBe(false);
    h.socket.deliver({ type: "ack", seq: 3 }); expect(h.live.viewport(1440, 900, authority)).toBe(4);
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event)).toEqual([down, { type: "text", text: "保存输入" }, { ...down, phase: "up", buttons: 0 }]);
  });
  it("reports remote held input even after local mouseup so blur can cancel safely", async () => {
    const h = harness(); h.ready(); await h.display();
    const down = { type: "pointer" as const, phase: "down" as const, x: 10, y: 20, button: "left" as const, buttons: 1, modifiers: 0, click_count: 1 };
    h.live.input(down, authority); h.live.input({ type: "text", text: "queued" }, authority); h.live.input({ ...down, phase: "up", buttons: 0 }, authority);
    expect(h.live.clearPendingInput()).toBe(true); h.live.input({ type: "reset" }, authority);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(2);
    h.socket.deliver({ type: "ack", seq: 1 }); expect(h.socket.messages().filter(item => item.type === "input")[2]?.event).toEqual({ type: "reset" });
    h.socket.deliver({ type: "ack", seq: 2 }); h.socket.deliver({ type: "ack", seq: 3 }); expect(h.live.clearPendingInput()).toBe(false);
  });
  it("cancels unsent reliable input on an epoch change and ignores its late ACK/error", async () => {
    const h = harness(); h.ready(); await h.display();
    for (let i = 0; i < 5; i++) h.live.input({ type: "text", text: `old-${i}` }, authority);
    h.socket.deliver({ type: "media_reset", media_epoch: "media-1" }); await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" });
    const next = { ...authority, frame_id: 2, media_epoch: "media-2" };
    for (let i = 0; i < 3; i++) h.live.input({ type: "text", text: `new-${i}` }, next);
    h.socket.deliver({ type: "ack", seq: 1 }); h.socket.deliver({ type: "error", seq: 2, code: "LIVE_BUSY", error: "old failed" });
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["old-0", "old-1", "new-0", "new-1"]);
    expect(h.callbacks.error).not.toHaveBeenCalled(); expect((await h.live.diagnostics())?.scheduler).toMatchObject({ pending: 1, in_flight: 2 });
    h.socket.deliver({ type: "ack", seq: 3 }); expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["old-0", "old-1", "new-0", "new-1", "new-2"]);
  });
  it("requires this human Page's matching lease and frame", () => {
    expect(liveDisplayAuthority(frame, state, target, "", 3)).toBeNull(); expect(liveDisplayAuthority(frame, state, target, "token", 2)).toBeNull(); expect(liveDisplayAuthority({ ...frame, tab_id: "t2" }, state, target, "token", 3)).toBeNull();
    expect(liveDisplayAuthority(frame, state, target, "token", 3)).toEqual({ generation: 2, control_epoch: 3, frame_id: 1, media_epoch: "media-1", lease_token: "token" });
  });
  it("serializes input and refuses stale or disconnected events", async () => {
    const h = harness(); expect(h.live.input({ type: "text", text: "before" }, authority)).toBe(false); h.ready(); await h.display();
    expect(h.live.input({ type: "text", text: "中文" }, { ...authority, lease_token: "" })).toBe(false); expect(h.live.input({ type: "text", text: "stale" }, { ...authority, generation: 1 })).toBe(false);
    expect(h.live.input({ type: "text", text: "中文" }, authority)).toBe(true); expect(h.socket.messages().slice(-1)[0]).toEqual({ type: "input", seq: 1, ...authority, event: { type: "text", text: "中文" } });
    h.socket.readyState = 3; expect(h.live.input({ type: "reset" }, authority)).toBe(false); h.live.close(); h.socket.readyState = 1; expect(h.live.input({ type: "reset" }, authority)).toBe(false); expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(1);
  });
  it("closes on a rejected cleanup instead of retrying reset against a busy page", async () => {
    const h = harness(); h.ready(); await h.display();
    const down = { type: "pointer" as const, phase: "down" as const, x: 10, y: 20, button: "left" as const, buttons: 1, modifiers: 0, click_count: 1 };
    h.live.input(down, authority); h.socket.deliver({ type: "ack", seq: 1 }); h.live.input({ type: "reset" }, authority);
    h.socket.deliver({ type: "error", seq: 2, code: "LIVE_BUSY", error: "reset rejected while busy" });
    expect(h.socket.close).toHaveBeenCalled(); expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(2); expect(h.live.clearPendingInput()).toBe(false);
  });
  it("bounds a silent ACK stall without replaying queued input or confusing fresh epochs", async () => {
    const h = harness(); h.ready(); await h.display(); vi.useFakeTimers();
    for (let i = 0; i < 10; i++) h.live.input({ type: "text", text: `once-${i}` }, authority);
    await vi.advanceTimersByTimeAsync(4900); expect(h.socket.close).not.toHaveBeenCalled();
    h.socket.deliver({ type: "ack", seq: 1 });
    await vi.advanceTimersByTimeAsync(1000); expect(h.socket.close).not.toHaveBeenCalled();
    // seq 2 was sent at t=0 with a 6 s deadline; ACK 1 cannot renew it.
    await vi.advanceTimersByTimeAsync(101); expect(h.socket.close).toHaveBeenCalled();
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["once-0", "once-1", "once-2"]);
    h.socket.deliver({ type: "ack", seq: 2 }); await vi.advanceTimersByTimeAsync(5000);
    expect(h.socket.messages().filter(item => item.type === "input")).toHaveLength(3);
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "input_backpressure" }));
  });
  it("fails closed on backpressure without delaying a gesture", async () => {
    const h = harness(); h.ready(); await h.display(); h.socket.bufferedAmount = 300000; expect(h.live.input({ type: "text", text: "late" }, authority)).toBe(false);
    expect(h.socket.close).toHaveBeenCalledWith(1000); expect(h.peers[0]!.close).toHaveBeenCalled(); expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "input_backpressure" }));
  });
  it("rejects a switched target and bounds signaling", () => {
    const h = harness(); h.ready({ ...state, viewed_tab: "t2" }); expect(h.callbacks.ready).not.toHaveBeenCalled(); expect(h.socket.close).toHaveBeenCalledWith(4002);
    const large = harness(); large.ready(); large.socket.onmessage?.({ data: "x".repeat(128 * 1024 + 1) }); expect(large.socket.close).toHaveBeenCalledWith(4002);
    const excessive = harness(); excessive.ready(); for (let i = 0; i < 65; i++) excessive.socket.deliver({ type: "media_candidate", media_epoch: "early", candidate: ice }); expect(excessive.socket.close).toHaveBeenCalledWith(4002);
  });
  it("keeps the local blank-page UI connected without media and starts video after navigation", async () => {
    vi.useFakeTimers(); const h = harness();
    h.ready({ ...state, url: "about:blank" });
    h.offer(); // A late unnecessary blank media offer cannot start a peer.
    await vi.advanceTimersByTimeAsync(30001);
    expect(h.live.ready).toBe(true); expect(h.peers).toHaveLength(0); expect(h.callbacks.close).not.toHaveBeenCalled();
    vi.useRealTimers();
    const next = { ...state, frame_identity: "2:t1:5", document_version: 5 };
    h.socket.deliver({ type: "state", state: next });
    const nextFrame = { ...frame, frame_id: 2, media_epoch: "navigated", frame_identity: next.frame_identity, document_version: 5 };
    await h.display(nextFrame);
    expect(h.live.input({ type: "text", text: "new page" }, { ...authority, frame_id: 2, media_epoch: "navigated" })).toBe(true);
  });
  it("retires current media and cancels its timeout when navigating back to about:blank", async () => {
    const h = harness(); h.ready(); const old = await h.display();
    vi.useFakeTimers();
    h.socket.deliver({ type: "state", state: { ...state, url: "about:blank", frame_identity: "2:t1:5", document_version: 5 } });
    h.socket.deliver({ type: "media_reset", media_epoch: frame.media_epoch });
    await vi.advanceTimersByTimeAsync(30001);
    expect(h.peers[0]!.close).toHaveBeenCalled(); expect(h.callbacks.mediaClosed).toHaveBeenCalled();
    expect(h.live.present(old, 1600, 1200)).toBe(false); expect(h.live.ready).toBe(true); expect(h.callbacks.close).not.toHaveBeenCalled();
  });
  it("restores the first-presentation deadline when leaving a local blank page", async () => {
    vi.useFakeTimers(); const h = harness(); h.ready({ ...state, url: "about:blank" });
    await vi.advanceTimersByTimeAsync(30001); expect(h.live.ready).toBe(true);
    h.socket.deliver({ type: "state", state: { ...state, frame_identity: "2:t1:5", document_version: 5 } });
    await vi.advanceTimersByTimeAsync(15001); expect(h.live.ready).toBe(false);
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_MEDIA_UNAVAILABLE" }));
  });
  it("times out a connection that never produces a presented frame", async () => {
    vi.useFakeTimers(); const h = harness(); h.ready(); h.offer();
    await vi.advanceTimersByTimeAsync(15001);
    expect(h.live.ready).toBe(false); expect(h.peers[0]!.close).toHaveBeenCalled();
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_MEDIA_UNAVAILABLE" }));
  });
  it("revokes authority and retains pixels before stopping the old media track", async () => {
    const h = harness(); h.ready(); await h.display();
    const stream = h.callbacks.media.mock.calls[0]![0];
    const track = stream.getTracks()[0]!;
    h.callbacks.mediaClosed.mockImplementationOnce(() => {
      expect(track.stop).not.toHaveBeenCalled();
      expect(h.peers[0]!.close).not.toHaveBeenCalled();
      expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    });
    h.socket.deliver({ type: "media_reset", media_epoch: "media-1" });
    expect(track.stop).toHaveBeenCalled(); expect(h.peers[0]!.close).toHaveBeenCalled();
  });
  it("only retires affected old media for LIVE_FRAME_STALE and keeps the signaling connection", async () => {
    const h = harness(); h.ready(); const old = await h.display();
    expect(h.live.input({ type: "text", text: "one attempt" }, authority)).toBe(true);
    h.socket.deliver({ type: "error", code: "LIVE_FRAME_STALE", seq: 1, error: "old displayed frame" });
    expect(h.live.ready).toBe(true); expect(h.socket.close).not.toHaveBeenCalled();
    expect(h.peers[0]!.close).toHaveBeenCalled(); expect(h.live.present(old, 1600, 1200)).toBe(false);
    expect(h.live.input({ type: "reset" }, authority)).toBe(false);
    await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" });
    expect(h.live.input({ type: "text", text: "fresh gesture" }, { ...authority, frame_id: 2, media_epoch: "media-2" })).toBe(true);
    expect(h.socket.messages().filter(item => item.type === "input").map(item => item.event.text)).toEqual(["one attempt", "fresh gesture"]);
  });
  it("ignores a late stale error from an older epoch without interrupting the new gesture", async () => {
    const h = harness(); h.ready(); await h.display();
    h.live.input({ type: "text", text: "old" }, authority);
    await h.display({ ...frame, frame_id: 2, media_epoch: "media-2" });
    const current = { ...authority, frame_id: 2, media_epoch: "media-2" };
    h.live.input({ type: "text", text: "new" }, current);
    h.callbacks.mediaClosed.mockClear(); h.callbacks.error.mockClear();
    h.socket.deliver({ type: "error", code: "LIVE_FRAME_STALE", seq: 1, error: "late old frame" });
    expect(h.peers[1]!.close).not.toHaveBeenCalled(); expect(h.callbacks.mediaClosed).not.toHaveBeenCalled(); expect(h.callbacks.error).not.toHaveBeenCalled();
    expect(h.live.input({ type: "reset" }, current)).toBe(true);
  });
  it("does not accept unknown or already acknowledged stale errors as new authority changes", async () => {
    const h = harness(); h.ready(); await h.display(); h.live.input({ type: "reset" }, authority);
    h.socket.deliver({ type: "ack", seq: 1 });
    for (const seq of [1, 999]) h.socket.deliver({ type: "error", code: "LIVE_FRAME_STALE", seq, error: "unbound" });
    expect(h.peers[0]!.close).not.toHaveBeenCalled(); expect(h.callbacks.error).not.toHaveBeenCalled();
  });
  it("reports input errors without replaying an action", async () => {
    const h = harness(); h.ready(); await h.display(); h.socket.deliver({ type: "error", seq: 8, code: "LIVE_FORBIDDEN", error: "expired" });
    expect(h.callbacks.error).toHaveBeenCalledWith(expect.objectContaining({ code: "LIVE_FORBIDDEN", seq: 8 })); expect(h.socket.messages().some(item => item.type === "input")).toBe(false); h.live.close(); h.socket.deliver({ type: "state", state }); expect(h.callbacks.state).not.toHaveBeenCalled();
  });
});
