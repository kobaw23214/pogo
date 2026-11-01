import { describe, expect, it } from "vitest";
import { browserInputMetrics, browserVideoMetrics, browserVideoSample } from "./browserPerformance";

describe("safe live performance measurements", () => {
  it("uses interval deltas for bitrate, frame rate, decode and jitter instead of lifetime averages", () => {
    const first = { timestamp_ms: 1000, bytes_received: 100000, frames_decoded: 100, jitter_buffer_seconds: 10, jitter_buffer_frames: 100, decode_seconds: 2, packets_lost: 3 };
    const next = { timestamp_ms: 3000, bytes_received: 600000, frames_decoded: 200, jitter_buffer_seconds: 12, jitter_buffer_frames: 200, decode_seconds: 2.5, packets_lost: 4 };
    expect(browserVideoMetrics(next, first)).toMatchObject({ bitrate_mbps: 2, fps: 50, jitter_buffer_ms: 20, decode_ms: 5, packets_lost: 1 });
    expect(browserVideoMetrics({ ...next, bytes_received: 1, frames_decoded: 1 }, first).bitrate_mbps).toBeUndefined();
    expect(browserVideoMetrics(first, first).decode_ms).toBeUndefined();
  });
  it("reads only safe media fields and transport categories; never exposes candidate addresses", () => {
    const stats = new Map([
      ["video", { id: "video", type: "inbound-rtp", kind: "video", framesDecoded: 120, bytesReceived: 1000, frameWidth: 1600, frameHeight: 900, framesPerSecond: 60 }],
      ["transport", { id: "transport", type: "transport", selectedCandidatePairId: "pair", dtlsCipher: "secret" }],
      ["pair", { id: "pair", type: "candidate-pair", currentRoundTripTime: .03, localCandidateId: "local", remoteCandidateId: "remote" }],
      ["local", { id: "local", type: "local-candidate", address: "10.0.0.1", port: 1234, url: "turn:secret", usernameFragment: "secret", candidateType: "relay", protocol: "udp", relayProtocol: "tcp" }],
      ["remote", { id: "remote", type: "remote-candidate", address: "10.0.0.2", port: 19780, candidateType: "host", protocol: "udp" }],
    ]);
    expect(browserVideoSample(stats as unknown as RTCStatsReport, 123)).toEqual({ timestamp_ms: 123, bytes_received: 1000, frames_decoded: 120, fps: 60, width: 1600, height: 900, network_rtt_ms: 30, candidate_side: "local", candidate_type: "relay", protocol: "tcp", remote_candidate_type: "host", remote_protocol: "udp" });
  });
  it("reports the actual selected local TCP/TLS TURN hop despite a remote host/UDP candidate", () => {
    for (const relayProtocol of ["tcp", "tls"]) {
      const stats = new Map([
        ["transport", { id: "transport", type: "transport", selectedCandidatePairId: "selected" }],
        ["unused", { id: "unused", type: "candidate-pair", state: "succeeded", nominated: true, localCandidateId: "direct" }],
        ["direct", { id: "direct", type: "local-candidate", candidateType: "host", protocol: "udp" }],
        ["selected", { id: "selected", type: "candidate-pair", localCandidateId: "relay", remoteCandidateId: "worker" }],
        ["relay", { id: "relay", type: "local-candidate", candidateType: "relay", protocol: "udp", relayProtocol }],
        ["worker", { id: "worker", type: "remote-candidate", candidateType: "host", protocol: "udp" }],
      ]);
      const sample = browserVideoSample(stats as unknown as RTCStatsReport, 123);
      expect(sample).toMatchObject({ candidate_side: "local", candidate_type: "relay", protocol: relayProtocol, remote_candidate_type: "host", remote_protocol: "udp" });
      expect(browserVideoMetrics(sample)).toMatchObject({ candidate_side: "local", candidate_type: "relay", protocol: relayProtocol });
    }
  });
  it("does not reinterpret missing or unknown local relay transport as the remote worker's UDP leg", () => {
    const stats = new Map<string, Record<string, unknown>>([
      ["pair", { id: "pair", type: "candidate-pair", state: "succeeded", nominated: true, localCandidateId: "local", remoteCandidateId: "remote" }],
      ["remote", { id: "remote", type: "remote-candidate", candidateType: "host", protocol: "udp" }],
    ]);
    const missing = browserVideoSample(stats as unknown as RTCStatsReport, 123);
    expect(missing.candidate_type).toBeUndefined();
    expect(missing.protocol).toBeUndefined();
    expect(missing.remote_protocol).toBe("udp");
    for (const relayProtocol of [undefined, "unknown-private-value"]) {
      stats.set("local", { id: "local", type: "local-candidate", candidateType: "relay", protocol: "udp", relayProtocol });
      const sample = browserVideoSample(stats as unknown as RTCStatsReport, 123);
      expect(sample.candidate_type).toBe("relay");
      expect(sample.protocol).toBeUndefined();
      expect(JSON.stringify(sample)).not.toContain("unknown-private-value");
    }
  });
  it("retains a direct local candidate's transport without attributing a remote relay to this viewer", () => {
    const stats = new Map([
      ["pair", { id: "pair", type: "candidate-pair", state: "succeeded", nominated: true, localCandidateId: "local", remoteCandidateId: "remote" }],
      ["local", { id: "local", type: "local-candidate", candidateType: "host", protocol: "udp" }],
      ["remote", { id: "remote", type: "remote-candidate", candidateType: "relay", protocol: "udp" }],
    ]);
    expect(browserVideoSample(stats as unknown as RTCStatsReport, 123)).toMatchObject({ candidate_side: "local", candidate_type: "host", protocol: "udp", remote_candidate_type: "relay", remote_protocol: "udp" });
  });
  it("keeps unknown measurements unavailable and reports rolling percentiles separately", () => {
    expect(browserVideoMetrics({ timestamp_ms: 1 }).bitrate_mbps).toBeUndefined();
    expect(browserInputMetrics([])).toMatchObject({ count: 0, last_ms: undefined, p50_ms: undefined });
    const samples = Array.from({ length: 20 }, (_, i) => ({ ms: i + 1, queue_ms: 2, dispatch_ms: 3 }));
    expect(browserInputMetrics(samples)).toEqual({ count: 20, last_ms: 20, p50_ms: 10, p95_ms: 19, queue_ms: 2, dispatch_ms: 3 });
  });
});
