import type { LiveInputQueueStats } from "./browserLiveInput";
/** Only numeric counters and transport categories leave WebRTC statistics.
 * Candidate addresses, SDP, URLs and credentials must not enter diagnostics. */
export interface BrowserVideoSample {
  timestamp_ms: number; bytes_received?: number; frames_decoded?: number;
  packets_lost?: number; jitter_buffer_seconds?: number; jitter_buffer_frames?: number;
  decode_seconds?: number; fps?: number; width?: number; height?: number;
  network_rtt_ms?: number; candidate_type?: string; protocol?: string; candidate_side?: "local";
  remote_candidate_type?: string; remote_protocol?: string;
}
export interface BrowserVideoMetrics {
  fps?: number; bitrate_mbps?: number; width?: number; height?: number;
  network_rtt_ms?: number; jitter_buffer_ms?: number; decode_ms?: number;
  packets_lost?: number; candidate_type?: string; protocol?: string; candidate_side?: "local";
  remote_candidate_type?: string; remote_protocol?: string;
}
export interface BrowserInputMetrics { count: number; last_ms?: number; p50_ms?: number; p95_ms?: number; queue_ms?: number; dispatch_ms?: number }
export interface BrowserPerformance {
  transport: "webrtc"; connection: string; presented: boolean; media_epoch: string;
  video: Record<string, number | string>[];
  input: BrowserInputMetrics;
  scheduler: LiveInputQueueStats;
  wheel: { received: number; dispatched: number; pending_groups: number; in_flight: number; last_wait_ms?: number; oldest_pending_ms?: number };
  metrics: BrowserVideoMetrics;
  first_frame_ms?: number;
  server?: Record<string, number | string | boolean>;
}
const number = (value: unknown): number | undefined => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;
export function browserVideoSample(stats: RTCStatsReport, timestamp: number): BrowserVideoSample {
  const result: BrowserVideoSample = { timestamp_ms: timestamp };
  let pair: Record<string, unknown> | undefined;
  const records = new Map<string, Record<string, unknown>>();
  stats.forEach(item => records.set(item.id, item));
  for (const item of records.values()) {
    if (item.type === "transport" && typeof item.selectedCandidatePairId === "string") pair = records.get(item.selectedCandidatePairId);
    if (item.type !== "inbound-rtp" || item.kind !== "video" && item.mediaType !== "video") continue;
    const mapping = { bytesReceived: "bytes_received", framesDecoded: "frames_decoded", packetsLost: "packets_lost", jitterBufferDelay: "jitter_buffer_seconds", jitterBufferEmittedCount: "jitter_buffer_frames", totalDecodeTime: "decode_seconds", framesPerSecond: "fps", frameWidth: "width", frameHeight: "height" } as const;
    for (const [key, dest] of Object.entries(mapping)) { const value = number(item[key]); if (value !== undefined) result[dest] = value; }
  }
  if (!pair) pair = [...records.values()].find(item => item.type === "candidate-pair" && item.state === "succeeded" && item.nominated === true);
  if (pair) {
    const rtt = number(pair.currentRoundTripTime); if (rtt !== undefined) result.network_rtt_ms = rtt * 1000;
    const local = typeof pair.localCandidateId === "string" ? records.get(pair.localCandidateId) : undefined;
    const remote = typeof pair.remoteCandidateId === "string" ? records.get(pair.remoteCandidateId) : undefined;
    if (local?.type === "local-candidate") {
      result.candidate_side = "local";
      if (typeof local.candidateType === "string" && ["host", "srflx", "prflx", "relay"].includes(local.candidateType)) result.candidate_type = local.candidateType;
      // A relay candidate's protocol describes the allocation's UDP leg.
      // relayProtocol describes the viewer -> TURN hop (TCP or TLS here).
      const protocol = local.candidateType === "relay" ? local.relayProtocol : local.protocol;
      if (typeof protocol === "string" && ["udp", "tcp", "tls"].includes(protocol)) result.protocol = protocol;
    }
    // Preserve the worker-side categories explicitly; never use them to claim
    // how this viewer reaches Pogo when local transport data is unavailable.
    if (remote?.type === "remote-candidate") {
      if (typeof remote.candidateType === "string" && ["host", "srflx", "prflx", "relay"].includes(remote.candidateType)) result.remote_candidate_type = remote.candidateType;
      if (typeof remote.protocol === "string" && ["udp", "tcp"].includes(remote.protocol)) result.remote_protocol = remote.protocol;
    }
  }
  return result;
}
function delta(current: number | undefined, previous: number | undefined) { return current !== undefined && previous !== undefined && current >= previous ? current - previous : undefined; }
export function browserVideoMetrics(sample: BrowserVideoSample, previous?: BrowserVideoSample): BrowserVideoMetrics {
  const result: BrowserVideoMetrics = { fps: sample.fps, width: sample.width, height: sample.height, network_rtt_ms: sample.network_rtt_ms, candidate_type: sample.candidate_type, protocol: sample.protocol, candidate_side: sample.candidate_side, remote_candidate_type: sample.remote_candidate_type, remote_protocol: sample.remote_protocol };
  if (!previous || sample.timestamp_ms <= previous.timestamp_ms) return result;
  const elapsed = sample.timestamp_ms - previous.timestamp_ms;
  const bytes = delta(sample.bytes_received, previous.bytes_received), frames = delta(sample.frames_decoded, previous.frames_decoded);
  if (bytes !== undefined) result.bitrate_mbps = bytes * 8 / elapsed / 1000;
  if (result.fps === undefined && frames !== undefined) result.fps = frames * 1000 / elapsed;
  const bufferFrames = delta(sample.jitter_buffer_frames, previous.jitter_buffer_frames), buffer = delta(sample.jitter_buffer_seconds, previous.jitter_buffer_seconds), decode = delta(sample.decode_seconds, previous.decode_seconds);
  if (bufferFrames && buffer !== undefined) result.jitter_buffer_ms = buffer * 1000 / bufferFrames;
  if (frames && decode !== undefined) result.decode_ms = decode * 1000 / frames;
  result.packets_lost = delta(sample.packets_lost, previous.packets_lost);
  return result;
}
export function browserInputMetrics(samples: { ms: number; queue_ms?: number; dispatch_ms?: number }[]): BrowserInputMetrics {
  const ordered = samples.map(sample => sample.ms).sort((a, b) => a - b), last = samples[samples.length - 1];
  return { count: samples.length, last_ms: last?.ms, p50_ms: ordered.length ? ordered[Math.ceil(ordered.length * .5) - 1] : undefined, p95_ms: ordered.length ? ordered[Math.ceil(ordered.length * .95) - 1] : undefined, queue_ms: last?.queue_ms, dispatch_ms: last?.dispatch_ms };
}
