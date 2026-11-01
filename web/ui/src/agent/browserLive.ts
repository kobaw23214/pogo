import type { NativeBrowserState, NativeBrowserTarget, NativeBrowserViewport } from "./nativeBrowser";
import { createLiveInputQueue, type LiveInputEvent } from "./browserLiveInput";
import { browserInputMetrics, browserVideoMetrics, browserVideoSample, type BrowserPerformance, type BrowserVideoSample } from "./browserPerformance";
import { readBrowserMediaRelayTicket } from "./browserMediaRelay";
import { browserAgentOverlayMatches, readBrowserAgentOverlay, type BrowserAgentOverlay } from "./browserAgentOverlay";

/** One immutable media epoch. This is display metadata, never DOM evidence. */
export interface BrowserLiveFrame {
  type: "media"; frame_id: number; media_epoch: string;
  viewport: NativeBrowserViewport; pixel_width: number; pixel_height: number;
  frame_identity: string; generation: number; control_epoch: number;
  document_version: number; context_ref: string; tab_id: string;
}
export type BrowserLiveEvent = LiveInputEvent;
export type BrowserCodecPreference = "standard" | "responsive";
export interface BrowserLiveAuthority { generation: number; control_epoch: number; lease_token: string; frame_id: number; media_epoch: string }
export type BrowserLiveDisplayAuthority = Omit<BrowserLiveAuthority, "lease_token"> & { lease_token?: string };
export interface BrowserLiveCallbacks {
  ready: (state: NativeBrowserState) => void;
  state: (state: NativeBrowserState) => void;
  /** Mount this epoch in a fresh video element. Only present() grants input. */
  media: (stream: MediaStream, frame: BrowserLiveFrame) => Promise<void>;
  mediaClosed: () => void;
  error: (error: Error & { code?: string; seq?: number }) => void;
  close: () => void;
  /** Input is suspended during a bounded ICE interruption; no events replay. */
  connection?: (available: boolean) => void;
  visibility?: (visible: boolean, revision: number) => void;
  /** Optional, page-bound action feedback; null immediately revokes marks. */
  overlay?: (overlay: BrowserAgentOverlay | null) => void;
  configured?: (configuration: { codecPreferences: BrowserCodecPreference[]; codecPreference: BrowserCodecPreference; runtime?: "wayland"; renderScaleLimit?: number; viewVisibility?: true }) => void;
}
export interface BrowserLiveOptions {
  scale?: number;
  codecPreference?: BrowserCodecPreference;
  /** A fresh, authenticated grant for this active live view and media epoch. */
  resolveRelayConfiguration?: (frame: BrowserLiveFrame, signal: AbortSignal) => Promise<unknown>;
  relayOrigin?: string;
  peerConnectionFactory?: (configuration: RTCConfiguration) => RTCPeerConnection;
}
const MAX_SIGNAL = 128 * 1024;
const MAX_CANDIDATES = 64;
function record(value: unknown): value is Record<string, unknown> { return !!value && typeof value === "object" && !Array.isArray(value); }
function exact(value: Record<string, unknown>, fields: string[]) { return Object.keys(value).every(key => fields.includes(key)); }
function integer(value: unknown, min: number, max: number) { return Number.isSafeInteger(value) && Number(value) >= min && Number(value) <= max; }
function epoch(value: unknown): value is string { return typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value); }
export function browserLiveURL(origin: string, sessionId: string, target: NativeBrowserTarget, ticket: string): string {
  const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/browser/live`, origin);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.search = new URLSearchParams({ context_ref: target.context_ref, tab_id: target.tab_id, ticket }).toString();
  return url.href;
}
export function liveFrameMatches(frame: BrowserLiveFrame, state: NativeBrowserState, target: NativeBrowserTarget): boolean {
  return frame.context_ref === target.context_ref && frame.tab_id === target.tab_id &&
    state.context_ref === target.context_ref && (state.viewed_tab ?? state.active_tab) === target.tab_id &&
    frame.generation === state.generation && frame.control_epoch === state.control_epoch &&
    frame.frame_identity === state.frame_identity &&
    frame.viewport.width === state.viewport.width && frame.viewport.height === state.viewport.height;
}
/** Display resizing never acquires input control or steals another human lease. */
export function liveDisplayAuthority(frame: BrowserLiveFrame | undefined, state: NativeBrowserState, target: NativeBrowserTarget, lease: string, controlEpoch?: number): BrowserLiveDisplayAuthority | null {
  if (!frame || !liveFrameMatches(frame, state, target)) return null;
  const authority = { generation: frame.generation, control_epoch: frame.control_epoch, frame_id: frame.frame_id, media_epoch: frame.media_epoch };
  if (state.control === "agent") return authority;
  return lease && controlEpoch === state.control_epoch ? { ...authority, lease_token: lease } : null;
}
export function readLiveFrame(value: unknown, target: NativeBrowserTarget): BrowserLiveFrame | null {
  if (!record(value) || !exact(value, ["type", "frame_id", "media_epoch", "viewport", "pixel_width", "pixel_height", "frame_identity", "generation", "control_epoch", "document_version", "context_ref", "tab_id"]) ||
      value.type !== "media" || value.context_ref !== target.context_ref || value.tab_id !== target.tab_id || !epoch(value.media_epoch) ||
      !integer(value.frame_id, 1, Number.MAX_SAFE_INTEGER) || typeof value.frame_identity !== "string" || !value.frame_identity || value.frame_identity.length > 256 ||
      !record(value.viewport) || !exact(value.viewport, ["width", "height"])) return null;
  if (!integer(value.generation, 1, Number.MAX_SAFE_INTEGER) || ![value.control_epoch, value.document_version].every(n => integer(n, 0, Number.MAX_SAFE_INTEGER))) return null;
  if (![value.viewport.width, value.viewport.height, value.pixel_width, value.pixel_height].every(n => integer(n, 1, 8192))) return null;
  return value as unknown as BrowserLiveFrame;
}
function liveState(value: unknown, target: NativeBrowserTarget): NativeBrowserState | null {
  if (!record(value) || value.backend !== "native" || value.context_ref !== target.context_ref || (value.viewed_tab ?? value.active_tab) !== target.tab_id || !record(value.viewport) || !Array.isArray(value.tabs) ||
      !integer(value.generation, 1, Number.MAX_SAFE_INTEGER) || !integer(value.control_epoch, 0, Number.MAX_SAFE_INTEGER) || !integer(value.document_version, 0, Number.MAX_SAFE_INTEGER) ||
      typeof value.frame_identity !== "string" || value.frame_identity.length > 256 || ![value.viewport.width, value.viewport.height].every(n => integer(n, 1, 8192))) return null;
  return value as unknown as NativeBrowserState;
}
function candidate(value: unknown): RTCIceCandidateInit | null | undefined {
  if (value === null) return null;
  if (!record(value) || !exact(value, ["candidate", "sdpMid", "sdpMLineIndex", "usernameFragment"]) || typeof value.candidate !== "string" || value.candidate.length > 4096 ||
      value.sdpMid !== undefined && value.sdpMid !== null && (typeof value.sdpMid !== "string" || value.sdpMid.length > 256) ||
      value.sdpMLineIndex !== undefined && value.sdpMLineIndex !== null && !integer(value.sdpMLineIndex, 0, 64) ||
      value.usernameFragment !== undefined && value.usernameFragment !== null && (typeof value.usernameFragment !== "string" || value.usernameFragment.length > 256)) return undefined;
  return value as RTCIceCandidateInit;
}
function iceServers(value: unknown): RTCIceServer[] | null {
  if (!Array.isArray(value) || value.length > 8) return null;
  for (const item of value) {
    if (!record(item) || !exact(item, ["urls", "username", "credential"])) return null;
    const urls = typeof item.urls === "string" ? [item.urls] : item.urls;
    if (!Array.isArray(urls) || !urls.length || urls.length > 8 || urls.some(url => typeof url !== "string" || url.length > 2048 || !/^(stun|stuns|turn|turns):[^\s]+$/.test(url))) return null;
    if ([item.username, item.credential].some(field => field !== undefined && (typeof field !== "string" || field.length > 1024))) return null;
  }
  return value as RTCIceServer[];
}
type MediaPeer = {
  pc: RTCPeerConnection; frame: BrowserLiveFrame; remoteReady: boolean;
  candidates: (RTCIceCandidateInit | null)[]; stream?: MediaStream;
  presented: boolean; receivedTrack: boolean; candidateCount: number;
  connected: boolean; inputRevision: number; disconnectTimer?: ReturnType<typeof setTimeout>; offeredAt: number; firstFrameMs?: number;
};

/** Media never buffers input. A fresh epoch has a fresh peer and receiver;
 * reconnection never transfers the previous epoch's presentation authority. */
export function createBrowserLive(socket: WebSocket, target: NativeBrowserTarget, callbacks: BrowserLiveCallbacks, options: BrowserLiveOptions = {}) {
  let closed = false, ready = false, sequence = 0, highestFrame = 0;
  let currentState: NativeBrowserState | undefined;
  let negotiatedRenderScaleLimit: number | undefined;
  let negotiatedViewVisibility = false, presentationActive = true, viewVisible = true, viewRevision = 0, acknowledgedViewRevision = 0;
  let negotiatedActionOverlay = false, highestOverlay = 0;
  let mediaTimer: ReturnType<typeof setTimeout> | undefined;
  let inputTimer: ReturnType<typeof setTimeout> | undefined;
  let peer: MediaPeer | undefined;
  let pendingOffer: { frame: BrowserLiveFrame; controller: AbortController } | undefined;
  const retired = new Set<string>();
  const earlyCandidates = new Map<string, (RTCIceCandidateInit | null)[]>();
  // Metadata only; never keep or replay input payloads.
  const sentBindings = new Map<number, { media_epoch: string; frame_id: number; sentAt: number; input: boolean; reset?: boolean; deadlineAt?: number; inputRevision: number }>();
  const idleWaiters = new Set<{ check: () => void; stop: () => void }>();
  const cancelIdleWaiters = () => { for (const waiter of [...idleWaiters]) waiter.stop(); };
  const notifyInputIdle = () => { for (const waiter of [...idleWaiters]) waiter.check(); };
  const inputSamples: { ms: number; queue_ms?: number; dispatch_ms?: number }[] = [];
  let previousSample: BrowserVideoSample | undefined;
  let serverPerformance: Record<string, number | string | boolean> | undefined;
  const factory = options.peerConnectionFactory ?? (configuration => new RTCPeerConnection(configuration));
  const isCurrent = (media: MediaPeer) => !closed && ready && peer === media && !!currentState && liveFrameMatches(media.frame, currentState, target);
  const remember = (mediaEpoch: string) => { retired.add(mediaEpoch); while (retired.size > 64) retired.delete(retired.values().next().value!); };
  const stopMedia = (mediaEpoch?: string) => {
    if (mediaEpoch) { remember(mediaEpoch); earlyCandidates.delete(mediaEpoch); }
    const opening = pendingOffer;
    if (opening && (!mediaEpoch || opening.frame.media_epoch === mediaEpoch)) {
      pendingOffer = undefined; opening.controller.abort();
      remember(opening.frame.media_epoch); earlyCandidates.delete(opening.frame.media_epoch);
    }
    const old = peer;
    if (!old || mediaEpoch && old.frame.media_epoch !== mediaEpoch) return;
    callbacks.overlay?.(null);
    cancelIdleWaiters();
    peer = undefined; remember(old.frame.media_epoch); earlyCandidates.delete(old.frame.media_epoch);
    clearTimeout(old.disconnectTimer); clearTimeout(inputTimer); inputTimer = undefined; inputs.reset(); previousSample = undefined; serverPerformance = undefined; inputSamples.length = 0;
    old.pc.ontrack = null; old.pc.onicecandidate = null; old.pc.onconnectionstatechange = null; old.pc.oniceconnectionstatechange = null;
    // Revoke authority first, but let the surface retain already presented
    // pixels before stopping the track or disconnecting its video element.
    try { callbacks.mediaClosed(); }
    finally { old.stream?.getTracks().forEach(track => track.stop()); old.pc.close(); }
  };
  const shutdown = (code: number, notify: boolean) => {
    if (closed) return;
    closed = true; ready = false; cancelIdleWaiters(); clearTimeout(mediaTimer); clearTimeout(inputTimer); inputTimer = undefined;
    callbacks.overlay?.(null);
    const hadMedia = !!peer; stopMedia(); earlyCandidates.clear(); sentBindings.clear();
    // Also revoke a Pane that is waiting for its first media offer.
    if (!hadMedia) callbacks.mediaClosed();
    socket.close(code);
    if (notify) callbacks.close();
  };
  const fail = (message: string, code = "LIVE_MEDIA_INVALID") => {
    // Browser WebSocket.close only accepts 1000 or application codes 3000-4999.
    // 1002 is a peer protocol code; using it here throws before local cleanup.
    callbacks.error(Object.assign(new Error(message), { code })); shutdown(4002, true);
  };
  const send = (message: unknown) => {
    if (closed || socket.readyState !== 1) return false;
    if (socket.bufferedAmount > 256 * 1024) {
      callbacks.error(Object.assign(new Error("Browser input connection is congested; reconnect before continuing"), { code: "input_backpressure" }));
      shutdown(1000, true); return false;
    }
    socket.send(JSON.stringify(message)); return true;
  };
  const awaitMedia = () => {
    clearTimeout(mediaTimer);
    if (!presentationActive || currentState?.url === "about:blank") { mediaTimer = undefined; return; }
    mediaTimer = setTimeout(() => { if (!closed) fail("Browser media did not become visible; reconnect before continuing", "LIVE_MEDIA_UNAVAILABLE"); }, 15000);
  };
  const mediaFailed = (media: MediaPeer, message: string) => {
    if (!isCurrent(media)) return;
    callbacks.error(Object.assign(new Error(message), { code: "LIVE_MEDIA_UNAVAILABLE" }));
    shutdown(1000, true);
  };
  const addCandidate = async (media: MediaPeer, item: RTCIceCandidateInit | null) => {
    if (!isCurrent(media)) return;
    try { await media.pc.addIceCandidate(item ?? undefined); }
    catch { mediaFailed(media, "Browser media candidate was rejected"); }
  };
  const offer = async (frame: BrowserLiveFrame, sdp: string) => {
    stopMedia(); awaitMedia();
    // Worker offers retain their protocol shape, but only Pogo's current
    // authenticated view can select the external relay. No direct fallback.
    const resolveRelay = options.resolveRelayConfiguration;
    if (!resolveRelay) { fail("Browser media relay is unavailable; reconnect before continuing", "LIVE_MEDIA_UNAVAILABLE"); return; }
    const opening = { frame, controller: new AbortController() }, requestedAt = Date.now(), offeredAt = performance.now();
    pendingOffer = opening;
    const current = () => !closed && ready && pendingOffer === opening && !opening.controller.signal.aborted &&
      !retired.has(frame.media_epoch) && highestFrame === frame.frame_id && !!currentState && currentState.url !== "about:blank" && liveFrameMatches(frame, currentState, target);
    let configuration: RTCConfiguration | null;
    try {
      const ticket = await resolveRelay(frame, opening.controller.signal);
      if (!current()) return;
      const origin = options.relayOrigin ?? (typeof globalThis.location === "object" ? globalThis.location.origin : "");
      configuration = readBrowserMediaRelayTicket(ticket, frame, origin, requestedAt);
      if (!configuration) { fail("Browser media relay grant is invalid or expired; reconnect before continuing", "LIVE_MEDIA_UNAVAILABLE"); return; }
    } catch {
      if (current()) fail("Browser media relay is unavailable; reconnect before continuing", "LIVE_MEDIA_UNAVAILABLE");
      return;
    }
    pendingOffer = undefined;
    let pc: RTCPeerConnection;
    try { pc = factory({ ...configuration, bundlePolicy: "max-bundle" }); }
    catch { fail("WebRTC is unavailable in this browser", "LIVE_MEDIA_UNAVAILABLE"); return; }
    const queued = earlyCandidates.get(frame.media_epoch) ?? []; earlyCandidates.delete(frame.media_epoch);
    const media: MediaPeer = { pc, frame, remoteReady: false, candidates: queued, presented: false, receivedTrack: false, candidateCount: queued.length, connected: true, inputRevision: 0, offeredAt };
    peer = media;
    pc.onicecandidate = event => {
      if (!isCurrent(media)) return;
      send({ type: "media_candidate", media_epoch: frame.media_epoch, candidate: event.candidate?.toJSON() ?? null });
    };
    const connectionChanged = () => {
      if (!isCurrent(media)) return;
      if (["failed", "closed"].includes(pc.connectionState) || ["failed", "closed"].includes(pc.iceConnectionState)) { mediaFailed(media, "Browser media connection was lost"); return; }
      const disconnected = pc.connectionState === "disconnected" || pc.iceConnectionState === "disconnected";
      if (disconnected) {
        callbacks.overlay?.(null);
        cancelIdleWaiters();
        if (media.connected) media.inputRevision++;
        media.connected = false; media.presented = false; clearTimeout(inputTimer); inputTimer = undefined; inputs.clear(); callbacks.connection?.(false);
        media.disconnectTimer ??= setTimeout(() => mediaFailed(media, "Browser media connection was lost"), 800);
      } else if (pc.connectionState === "connected" || ["connected", "completed"].includes(pc.iceConnectionState)) {
        clearTimeout(media.disconnectTimer); media.disconnectTimer = undefined;
        media.connected = true; callbacks.connection?.(media.presented);
      }
    };
    pc.onconnectionstatechange = connectionChanged; pc.oniceconnectionstatechange = connectionChanged;
    pc.ontrack = event => {
      if (!isCurrent(media)) { event.track.stop(); return; }
      if (event.track.kind !== "video" || media.receivedTrack) { event.track.stop(); mediaFailed(media, "Browser media stream changed unexpectedly"); return; }
      media.receivedTrack = true;
      const stream = event.streams[0] ?? new MediaStream([event.track]);
      if (stream.getTracks().some(track => track.kind !== "video") || stream.getVideoTracks().length !== 1) { stream.getTracks().forEach(track => track.stop()); mediaFailed(media, "Browser media stream is invalid"); return; }
      media.stream = stream;
      event.track.onended = () => mediaFailed(media, "Browser media track ended");
      void Promise.resolve().then(() => { if (isCurrent(media)) return callbacks.media(stream, frame); }).catch(() => mediaFailed(media, "Browser media could not be displayed"));
    };
    try {
      await pc.setRemoteDescription({ type: "offer", sdp });
      if (!isCurrent(media)) return;
      media.remoteReady = true;
      for (const item of media.candidates.splice(0)) { await addCandidate(media, item); if (!isCurrent(media)) return; }
      const answer = await pc.createAnswer();
      if (!isCurrent(media)) return;
      await pc.setLocalDescription(answer);
      if (!isCurrent(media)) return;
      const result = pc.localDescription?.sdp;
      if (!result || result.length > MAX_SIGNAL) { mediaFailed(media, "Browser media answer is invalid"); return; }
      send({ type: "media_answer", media_epoch: frame.media_epoch, sdp: result });
    } catch { mediaFailed(media, "Browser media negotiation failed"); }
  };
  socket.onmessage = event => {
    if (closed) return;
    let data: unknown;
    if (typeof event.data !== "string" || event.data.length > MAX_SIGNAL) { fail("Invalid browser stream message"); return; }
    try { data = JSON.parse(event.data); } catch { fail("Invalid browser stream message"); return; }
    if (!record(data)) { fail("Invalid browser stream message"); return; }
    if (data.type === "ready" || data.type === "state") {
      const state = liveState(data.state, target);
      if (!state || data.type === "ready" && (ready || data.protocol !== 2 || data.transport !== "webrtc") || data.type === "state" && !ready) { fail("Browser stream target changed"); return; }
      if (currentState && (state.generation! < currentState.generation! || state.generation === currentState.generation && (state.control_epoch! < currentState.control_epoch! || state.document_version! < currentState.document_version!))) return;
      const wasBlank = currentState?.url === "about:blank";
      const priorState = currentState;
      currentState = state;
      // A control handoff, navigation or changed page geometry revokes the
      // old marks before the Pane receives the new state. Ordinary video
      // reception never makes old action metadata valid again.
      if (!priorState || state.control !== "agent" || state.dialog || state.debugger_paused || state.devtools_blocked || state.control !== priorState.control || state.url !== priorState.url ||
          state.generation !== priorState.generation || state.control_epoch !== priorState.control_epoch || state.frame_identity !== priorState.frame_identity ||
          state.document_version !== priorState.document_version || state.viewport.width !== priorState.viewport.width || state.viewport.height !== priorState.viewport.height ||
          state.activity?.last_action?.seq !== priorState.activity?.last_action?.seq) callbacks.overlay?.(null);
      if (state.url === "about:blank") { stopMedia(); earlyCandidates.clear(); clearTimeout(mediaTimer); mediaTimer = undefined; }
      else if (wasBlank || peer && !liveFrameMatches(peer.frame, state, target) || pendingOffer && !liveFrameMatches(pendingOffer.frame, state, target)) { stopMedia(); awaitMedia(); }
      if (data.type === "ready") {
        ready = true;
        const requestedScale = options.scale ?? globalThis.devicePixelRatio ?? 1;
        const scale = Number.isFinite(requestedScale) ? Math.max(1, Math.min(2, requestedScale)) : 1;
        // Older workers validate media_config exactly; negotiate optional
        // diagnostics so either side can be upgraded without breaking video.
        const performanceMetrics = record(data.capabilities) && data.capabilities.performance === true;
        negotiatedActionOverlay = !!callbacks.overlay && record(data.capabilities) && data.capabilities.action_overlay === true;
        const advertised = record(data.capabilities) ? data.capabilities.codec_preferences : undefined;
        const runtime = record(data.capabilities) && data.capabilities.runtime === "wayland" ? "wayland" : undefined;
        negotiatedViewVisibility = !!runtime && record(data.capabilities) && data.capabilities.view_visibility === true;
        const advertisedLimit = record(data.capabilities) ? data.capabilities.render_scale_limit : undefined;
        const renderScaleLimit = runtime && typeof advertisedLimit === "number" && [1, 1.25, 1.5, 2].includes(advertisedLimit) ? advertisedLimit : undefined;
        const responsive = !runtime && Array.isArray(advertised) && advertised.length <= 8 && advertised.includes("responsive");
        const codecPreferences: BrowserCodecPreference[] = responsive ? ["standard", "responsive"] : ["standard"];
        const codecPreference = options.codecPreference === "responsive" && responsive ? "responsive" : "standard";
        // Standard sends the exact legacy configuration. Unsupported workers
        // must never see an optional field their strict validator cannot read.
        if (send({ type: "media_config", scale, ...(performanceMetrics ? { performance: true } : {}), ...(negotiatedActionOverlay ? { action_overlay: true } : {}), ...(codecPreference === "responsive" ? { codec_preference: codecPreference } : {}) })) {
          negotiatedRenderScaleLimit = renderScaleLimit;
          callbacks.configured?.({ codecPreferences, codecPreference, ...(runtime ? { runtime } : {}), ...(renderScaleLimit === undefined ? {} : { renderScaleLimit }), ...(negotiatedViewVisibility ? { viewVisibility: true as const } : {}) }); awaitMedia(); callbacks.ready(state);
        }
      } else callbacks.state(state);
    } else if (data.type === "action_overlay") {
      // Presentation-only feedback must not interrupt healthy video, even
      // when a stale or malformed sample races a document/control change.
      if (!ready || !negotiatedActionOverlay) return;
      const marks = readBrowserAgentOverlay(data, target);
      if (!marks || marks.seq <= highestOverlay) return;
      if (!presentationActive || !viewVisible || negotiatedViewVisibility && acknowledgedViewRevision !== viewRevision ||
          !peer || !isCurrent(peer) || !peer.connected || !peer.presented || !currentState ||
          !browserAgentOverlayMatches(marks, peer.frame, currentState, target)) return;
      highestOverlay = marks.seq;
      callbacks.overlay?.(marks.overlay ? marks : null);
    } else if (data.type === "view_visibility") {
      if (!ready || !negotiatedViewVisibility || !exact(data, ["type", "visible", "revision"]) || typeof data.visible !== "boolean" || !integer(data.revision, 1, Number.MAX_SAFE_INTEGER)) { fail("Invalid browser view visibility"); return; }
      if (data.revision !== viewRevision || data.visible !== viewVisible) return;
      acknowledgedViewRevision = viewRevision;
      callbacks.visibility?.(viewVisible, viewRevision);
    } else if (data.type === "media_offer") {
      const frame = readLiveFrame(data.frame, target), servers = iceServers(data.ice_servers);
      if (!ready || !frame || !servers || !exact(data, ["type", "frame", "sdp", "ice_servers"]) || typeof data.sdp !== "string" || !data.sdp || data.sdp.length > 100 * 1024) { fail("Invalid browser media offer"); return; }
      if (currentState?.url === "about:blank") { remember(frame.media_epoch); earlyCandidates.delete(frame.media_epoch); return; }
      if (retired.has(frame.media_epoch) || peer?.frame.media_epoch === frame.media_epoch || pendingOffer?.frame.media_epoch === frame.media_epoch || frame.frame_id <= highestFrame || !currentState || !liveFrameMatches(frame, currentState, target)) return;
      highestFrame = frame.frame_id; void offer(frame, data.sdp);
    } else if (data.type === "media_candidate") {
      const item = candidate(data.candidate);
      if (!ready || !exact(data, ["type", "media_epoch", "candidate"]) || !epoch(data.media_epoch) || item === undefined) { fail("Invalid browser media candidate"); return; }
      if (currentState?.url === "about:blank" || retired.has(data.media_epoch)) return;
      if (peer?.frame.media_epoch === data.media_epoch) {
        if (++peer.candidateCount > MAX_CANDIDATES) { fail("Too many browser media candidates"); return; }
        if (peer.remoteReady) void addCandidate(peer, item); else peer.candidates.push(item);
      } else {
        const queue = earlyCandidates.get(data.media_epoch) ?? [];
        if (queue.length >= MAX_CANDIDATES || !earlyCandidates.has(data.media_epoch) && earlyCandidates.size >= 4) { fail("Too many pending browser media candidates"); return; }
        queue.push(item); earlyCandidates.set(data.media_epoch, queue);
      }
    } else if (data.type === "media_reset") {
      if (!ready || !exact(data, ["type", "media_epoch", "reason"]) || !epoch(data.media_epoch) || data.reason !== undefined && (typeof data.reason !== "string" || data.reason.length > 500)) { fail("Invalid browser media reset"); return; }
      const current = peer?.frame.media_epoch === data.media_epoch || pendingOffer?.frame.media_epoch === data.media_epoch;
      stopMedia(data.media_epoch);
      if (current) awaitMedia();
    } else if (data.type === "error") {
      const seq = integer(data.seq, 1, Number.MAX_SAFE_INTEGER) ? Number(data.seq) : undefined;
      const sent = seq === undefined ? undefined : sentBindings.get(seq);
      if (seq !== undefined) sentBindings.delete(seq);
      const currentSent = sent && peer && sent.media_epoch === peer.frame.media_epoch && sent.frame_id === peer.frame.frame_id && sent.inputRevision === peer.inputRevision;
      if (sent?.input && !currentSent) return;
      // A rejected viewport is also a failed drain boundary. Do not leave a
      // mode switch waiting for an ACK that this error has already replaced.
      if (currentSent) cancelIdleWaiters();
      if (sent?.input && currentSent && seq !== undefined) {
        clearTimeout(inputTimer); inputTimer = undefined; inputs.reject(seq);
        // A rejected cleanup must not recursively retry reset on a busy page.
        // Closing delegates held-key cleanup to the worker connection fence.
        if (sent.reset) shutdown(1000, true); else watchInputProgress();
      }
      if (data.code === "LIVE_FRAME_STALE") {
        // An old pointer-up can fail after a newer media epoch already arrived.
        // Never stop that new video or release a new gesture for an old error.
        if (!sent || !peer || sent.media_epoch !== peer.frame.media_epoch || sent.frame_id !== peer.frame.frame_id) return;
        stopMedia(); awaitMedia();
      }
      callbacks.error(Object.assign(new Error(typeof data.error === "string" ? data.error.slice(0, 500) : "Browser action failed"), { code: typeof data.code === "string" ? data.code.slice(0, 100) : undefined, seq }));
    } else if (data.type === "ack") {
      if (integer(data.seq, 1, Number.MAX_SAFE_INTEGER)) {
        const seq = Number(data.seq), sent = sentBindings.get(seq); sentBindings.delete(seq);
        if (sent?.input && peer?.frame.media_epoch === sent.media_epoch && peer.inputRevision === sent.inputRevision) {
          const timing = record(data.timing) ? data.timing : {};
          const ms = (value: unknown) => typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= 60000 ? value : undefined;
          inputSamples.push({ ms: Math.max(0, performance.now() - sent.sentAt), queue_ms: ms(timing.queue_ms), dispatch_ms: ms(timing.dispatch_ms) });
          if (inputSamples.length > 100) inputSamples.shift();
          clearTimeout(inputTimer); inputTimer = undefined;
          inputs.acknowledge(seq, inputSamples[inputSamples.length - 1]); watchInputProgress();
        }
        notifyInputIdle();
      }
    } else if (data.type === "performance") {
      if (!ready || !epoch(data.media_epoch) || !record(data.stats)) { fail("Invalid browser performance message"); return; }
      if (peer?.frame.media_epoch !== data.media_epoch) return;
      const safe: Record<string, number | string | boolean> = {};
      const numeric = ["max_framerate", "max_bitrate_bps", "bitrate_bps", "fps", "encode_ms_per_frame", "send_delay_ms_per_packet", "rtt_ms", "available_bitrate_bps", "packet_loss_ratio", "width", "height", "sample_age_ms", "queue_ms", "dispatch_ms", "input_count", "restarts", "setup_ms", "capture_fps", "capture_min_fps"];
      for (const key of numeric) if (typeof data.stats[key] === "number" && Number.isFinite(data.stats[key]) && Number(data.stats[key]) >= 0) safe[key] = Number(data.stats[key]);
      const categories: Record<string, string[]> = { profile: ["reading", "interactive"], adaptation: ["balanced", "cpu", "bandwidth"], quality_limitation: ["none", "cpu", "bandwidth", "other"], codec: ["VP8", "VP9", "H264", "AV1", "video/VP8", "video/VP9", "video/H264", "video/AV1"], content_hint: ["text", "motion"] };
      for (const [key, allowed] of Object.entries(categories)) if (typeof data.stats[key] === "string" && allowed.includes(data.stats[key])) safe[key] = data.stats[key];
      if (typeof data.stats.capture_tuning_supported === "boolean") safe.capture_tuning_supported = data.stats.capture_tuning_supported;
      serverPerformance = safe;
    } else fail("Unsupported browser stream message");
  };
  socket.onclose = () => { shutdown(1000, true); };
  socket.onerror = () => { if (!closed) { callbacks.error(new Error("Browser live connection failed")); shutdown(1000, true); } };
  const authorized = (authority: BrowserLiveDisplayAuthority) => !!peer && isCurrent(peer) && peer.connected && peer.presented && (!negotiatedViewVisibility || viewVisible && acknowledgedViewRevision === viewRevision) && authority.media_epoch === peer.frame.media_epoch && authority.frame_id === peer.frame.frame_id && authority.generation === peer.frame.generation && authority.control_epoch === peer.frame.control_epoch;
  const watchInputProgress = () => {
    clearTimeout(inputTimer); inputTimer = undefined;
    if (closed || !peer || !peer.connected) return;
    const media = peer;
    const deadlines = [...sentBindings.values()].filter(sent => sent.input && sent.media_epoch === media.frame.media_epoch && sent.inputRevision === media.inputRevision).map(sent => sent.deadlineAt!);
    if (!deadlines.length) return;
    // Each dispatch has an absolute deadline: later input/ACKs cannot renew an
    // unknown outcome. Allow the worker's 2 s operation bound for each earlier
    // in-flight action plus network margin; queued unsent work has no deadline.
    inputTimer = setTimeout(() => {
      inputTimer = undefined;
      if (peer === media && !closed) fail("Browser input acknowledgements stopped; reconnect before continuing", "input_backpressure");
    }, Math.max(0, Math.min(...deadlines) - performance.now()));
  };
  const sendInput = (event: BrowserLiveEvent, authority: BrowserLiveAuthority) => {
    if (!authorized(authority) || !authority.lease_token) return false;
    if (sentBindings.size >= 512) { fail("Browser input acknowledgements are not progressing", "input_backpressure"); return false; }
    const seq = ++sequence;
    if (!send({ type: "input", seq, ...authority, event })) return false;
    sentBindings.set(seq, { media_epoch: authority.media_epoch, frame_id: authority.frame_id, sentAt: performance.now(), input: true, reset: event.type === "reset", deadlineAt: performance.now() + Math.max(5000, (inputs.stats.in_flight + 1) * 2000 + 2000), inputRevision: peer!.inputRevision });
    watchInputProgress(); return seq;
  };
  const useAnimationFrame = typeof globalThis.requestAnimationFrame === "function" && typeof globalThis.cancelAnimationFrame === "function";
  const inputs = createLiveInputQueue<BrowserLiveAuthority>(sendInput, (a, b) => a.media_epoch === b.media_epoch && a.generation === b.generation && a.frame_id === b.frame_id && a.control_epoch === b.control_epoch && a.lease_token === b.lease_token,
    callback => useAnimationFrame ? globalThis.requestAnimationFrame(callback) : globalThis.setTimeout(callback, 16) as unknown as number,
    id => { if (useAnimationFrame) globalThis.cancelAnimationFrame(id); else clearTimeout(id); });
  const inputDrained = () => !inputs.stats.pending && !inputs.stats.in_flight && ![...sentBindings.values()].some(sent => peer && sent.media_epoch === peer.frame.media_epoch && sent.inputRevision === peer.inputRevision);
  const inputIdle = () => inputs.idle && inputDrained();
  const waitForInput = (includeHeld: boolean, signal?: AbortSignal): Promise<boolean> => {
    if (closed || !ready || signal?.aborted) return Promise.resolve(false);
    const bound = peer, revision = peer?.inputRevision;
    return new Promise(resolve => {
      const finish = (value: boolean) => { idleWaiters.delete(waiter); signal?.removeEventListener("abort", waiter.stop); resolve(value); };
      const waiter = {
        check: () => { if (closed || !ready || peer !== bound || peer?.inputRevision !== revision) finish(false); else if (includeHeld ? inputIdle() : inputDrained()) finish(true); },
        stop: () => finish(false),
      };
      idleWaiters.add(waiter); signal?.addEventListener("abort", waiter.stop, { once: true }); waiter.check();
    });
  };
  return {
    get ready() { return ready && !closed && socket.readyState === 1; },
    get viewVisibilitySupported() { return negotiatedViewVisibility; },
    get viewRevision() { return viewRevision; },
    get viewAcknowledged() { return presentationActive && viewVisible && acknowledgedViewRevision === viewRevision; },
    get inputIdle() { return inputIdle(); },
    get acceptedInputDrained() { return inputDrained(); },
    /** Local suspension stops the presentation watchdog while accepted input
     * drains. It does not fence that already accepted queue. */
    suspendPresentation() { presentationActive = false; callbacks.overlay?.(null); clearTimeout(mediaTimer); mediaTimer = undefined; },
    /** Visibility is distinct from input authority. A reused video must be
     * presented again after the matching show ACK, even with the same epoch. */
    setViewVisibility(visible: boolean): number | false {
      if (!negotiatedViewVisibility || closed || !ready || socket.readyState !== 1 || viewRevision === Number.MAX_SAFE_INTEGER) return false;
      const revision = viewRevision + 1;
      if (!send({ type: "view_visibility", visible, revision })) return false;
      viewRevision = revision; viewVisible = visible; presentationActive = visible;
      callbacks.overlay?.(null);
      if (peer) peer.presented = false;
      clearTimeout(mediaTimer); mediaTimer = undefined;
      if (visible) awaitMedia();
      return revision;
    },
    /** Call only after inputIdle: reset itself intentionally discards unsent
     * input, so a blur/hide must drain first to preserve text and wheel. */
    releaseHeldInput(authority: BrowserLiveAuthority): boolean {
      if (!inputDrained()) return false;
      return !inputs.needsReset || authorized(authority) && !!authority.lease_token && inputs.push({ type: "reset" }, { ...authority });
    },
    /** Unlike idle, this boundary ignores a held button/key. The caller then
     * sends reset, so hiding while Shift/mouse-down is held cannot deadlock. */
    waitForAcceptedInput(signal?: AbortSignal): Promise<boolean> { return waitForInput(false, signal); },
    /** A mode change may close this receiver only after every accepted action
     * and release is acknowledged. Epoch changes or cancellation never replay. */
    waitForInputIdle(signal?: AbortSignal): Promise<boolean> {
      return waitForInput(true, signal);
    },
    /** Call only from the current video element's requestVideoFrameCallback. */
    present(frame: BrowserLiveFrame, width: number, height: number) {
      const media = peer;
      if (!presentationActive || !viewVisible || negotiatedViewVisibility && acknowledgedViewRevision !== viewRevision || !media || !isCurrent(media) || !media.connected || media.frame.frame_id !== frame.frame_id || media.frame.media_epoch !== frame.media_epoch || !liveFrameMatches(frame, currentState!, target) || !media.receivedTrack || width !== media.frame.pixel_width || height !== media.frame.pixel_height) return false;
      if (media.presented) return true;
      if (!send({ type: "presentation_ack", frame_id: frame.frame_id, media_epoch: frame.media_epoch, width, height, ...(negotiatedViewVisibility && viewRevision ? { view_revision: viewRevision } : {}) })) return false;
      clearTimeout(mediaTimer); media.presented = true; media.firstFrameMs ??= Math.max(0, performance.now() - media.offeredAt); callbacks.connection?.(media.connected); return true;
    },
    input(event: BrowserLiveEvent, authority: BrowserLiveAuthority) {
      if (!authorized(authority) || !authority.lease_token || socket.readyState !== 1) return false;
      if (inputs.push(event, { ...authority })) return true;
      if (!closed) fail("Browser input is congested; reconnect before continuing", "input_backpressure");
      return false;
    },
    clearPendingInput() { inputs.clearPending(); notifyInputIdle(); return inputs.needsReset; },
    viewport(width: number, height: number, authority: BrowserLiveDisplayAuthority, preferredScale?: number) {
      // Resizing must never discard a queued key-up, mouse-up or text commit.
      if (!authorized(authority) || !inputs.idle) return false;
      if (preferredScale !== undefined && (negotiatedRenderScaleLimit === undefined || !Number.isFinite(preferredScale) || preferredScale < 1 || preferredScale > 2)) return false;
      if (sentBindings.size >= 512) { fail("Browser input acknowledgements are not progressing", "input_backpressure"); return false; }
      const seq = ++sequence;
      if (!send({ type: "viewport", seq, width, height, ...(preferredScale === undefined ? {} : { preferred_scale: preferredScale }), ...authority })) return false;
      sentBindings.set(seq, { media_epoch: authority.media_epoch, frame_id: authority.frame_id, sentAt: performance.now(), input: false, inputRevision: peer!.inputRevision });
      return seq;
    },
    async diagnostics(): Promise<BrowserPerformance | null> {
      const media = peer;
      if (!media || !isCurrent(media)) return null;
      let stats: RTCStatsReport | null = null;
      try { if (typeof media.pc.getStats === "function") stats = await media.pc.getStats(); } catch { /* Diagnostics never interrupt the page. */ }
      if (!stats || !isCurrent(media)) return null;
      const video: Record<string, number | string>[] = [];
      const fields = ["bytesReceived", "packetsReceived", "packetsLost", "framesReceived", "framesDecoded", "framesDropped", "framesPerSecond", "frameWidth", "frameHeight", "jitter", "jitterBufferDelay", "jitterBufferEmittedCount", "totalDecodeTime", "freezeCount", "totalFreezesDuration"];
      stats.forEach(item => { if (item.type === "inbound-rtp" && (item.kind === "video" || item.mediaType === "video")) {
        const out: Record<string, number | string> = {};
        for (const key of fields) if (typeof item[key] === "number" && Number.isFinite(item[key])) out[key] = item[key];
        video.push(out);
      } });
      const sample = browserVideoSample(stats, performance.now());
      const metrics = browserVideoMetrics(sample, previousSample); previousSample = sample;
      return { transport: "webrtc", media_epoch: media.frame.media_epoch, connection: media.pc.connectionState, presented: media.presented, video, input: browserInputMetrics(inputSamples), wheel: inputs.wheelStats, scheduler: inputs.stats, metrics, first_frame_ms: media.firstFrameMs, server: serverPerformance };
    },
    close() { shutdown(1000, false); },
  };
}
