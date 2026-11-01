# Wayland frame → WebRTC service

This private Go service consumes encoded native Wayland frames and sends them through Pion WebRTC. Node retains the existing authenticated browser live WebSocket facade and owns session, page, view, epoch, and frontend frame identity. This service has no browser navigation, CDP, tab capture, browser extension, or WS video fallback.

Production uses VP8. H.264 is accepted only when the answer permits the actual encoder profile and receive level. The Chromium 153 probe accepts a level 5.1 offer but answers level 3.1; that answer cannot authorize sending level 5.1. VP8 avoids that incompatible negotiation without changing or misrepresenting SPS bytes.

## Build and launch

Build with Go 1.26 or newer, using the exact module versions in `go.mod` and `go.sum`:

```sh
GOOS=linux GOARCH=arm64 go build -o build/pogo-wlstream .
go test -race ./...
```

Install the binary as `/opt/pogo/bin/pogo-wlstream`. The Node child launcher supplies:

| Setting | Meaning |
| --- | --- |
| `--dir ABSOLUTE_PATH` | Private owned directory, mode 0700; creates `ctl.sock`, `frames.sock`, mode 0600. |
| `--udp-range 19780-19811` | Deployment-selected UDP port range. One shared UDP mux binds an available port in this range. |
| `--advertise-ip IPV4` | Optional deployment-selected host address, never selected from an untrusted browser request. IPv6 returns `IPV6_UNSUPPORTED`. |
| `--network lan\|wan` | LAN sends directly; WAN enables TWCC/GCC and synchronous packet pacing. Default `wan`. |
| `--playout-delay zero\|default` | Independent negotiated RTP playout-delay hint. Default `default`. |
| `--max-peers 1..32` | Active plus currently gathering peers; default 4, Node supplies 32. |
| `POGO_WLSTREAM_TOKEN` | Private controller capability, 32–256 characters. Never inherit into Chrome. |
| `POGO_WLSTREAM_ICE_SERVERS` | Optional trusted JSON ICE list; at most 8 servers, each `urls` string/array of 1–8 URLs, optional string `username` and `credential`. No other fields. Never inherit into Chrome or print. |

Without ICE servers the sender is ICE-lite. With trusted STUN/TURN settings it uses full ICE. This still uses WebRTC UDP and does not claim to work with only a TCP application port. It does not add a TURN server or alter the deployment's network policy.

Startup emits only `{"ready":true,"version":1,"transport":"webrtc","udp":true}`. SIGINT/SIGTERM closes peers, sockets, controllers, hosts, and the UDP mux. Errors and diagnostic files contain safe codes/counters, not raw SDP, candidates, addresses, or capabilities.

## Controller IPC

Connect to `ctl.sock` as the service UID. Send one NDJSON hello:

```json
{"type":"hello","version":1,"token":"PRIVATE_CONTROLLER_CAPABILITY"}
```

The reply is `{"type":"ready","version":1}`. Each following request is `{"id":"request-id","op":"operation","args":{...}}`. Responses are `{"id":"request-id","ok":true,"result":...}` or `{"id":"request-id","ok":false,"error":"SAFE_CODE"}`. Maximum line length is 64 KiB; malformed, trailing, or unknown JSON fields are rejected. Only four controller connections may authenticate concurrently.

| Operation | Required args | Result |
| --- | --- | --- |
| `register_session` | `session`, private per-session `token` | `{registered:true}` |
| `register_page` | `session,page,display_id,generation` | `{registered:true}` |
| `unregister_page` | `session,page` | `{closed:true}`; closes only the owner's matching page peers. |
| `unregister_session` | `session` | `{closed:true}`; closes that session's peers and frame host. |
| `create` | `session,page,view,epoch,generation,codec,width,height,content_seq_min`; H.264 additionally `profile_level_id` | `{peer_id,offer:{type:"offer",sdp:...},settings:{width,height,frameRate:60}}` |
| `answer` | Full scope plus `peer_id,answer:{type:"answer",sdp:...}` | `null` |
| `candidate` | Full scope plus `peer_id,candidate` (standard RTCIceCandidateInit) | `null` |
| `stats` | Full scope plus `peer_id` | Sanitized counters described below. |
| `keyframe` | Full scope plus `peer_id` | `{requested:true}` |
| `close` | Full scope plus `peer_id` | `{closed:true}` |

Full scope means `session,page,view,epoch,generation` and must match exactly on every operation. The controller that registered a session owns it. Page IDs may repeat across sessions without aliasing. Session IDs match the worker HTTP API: `[A-Za-z0-9_-]{1,160}`; other IDs use `[A-Za-z0-9_.:-]{1,128}`. Epoch and generation are positive integers. Limits are 32 sessions and 64 pages per session. Source display IDs start at 2, are unique within the session, and cannot rebind within the same generation. Generation must never decrease. Node registers the current generation again before creating each peer, including when cancellation advances generation without destroying the page.

The offer contains completed sender ICE gathering. Receiver candidates may arrive before the answer, bounded to 64 entries. Node must keep offer/answer/candidate data inside its existing protected signaling channel. State events retain the complete scope:

```json
{"type":"event","peer_id":"opaque","session":"s","page":"t1","view":"v1","epoch":1,"generation":1,"event":{"type":"state","state":"connected"}}
```

The Node facade retains protocol 2 and a fresh RTCPeerConnection for each media epoch. The `settings.frameRate` value is an upper-bound hint, not measured capture FPS.

## Native frame IPC

Connect to `frames.sock` as the service UID after session registration. Send NDJSON `{"type":"hello","version":1,"session":"s","token":"PRIVATE_SESSION_CAPABILITY"}` (`type` may be omitted). Only one frame host may attach to a session. The reply is `{"type":"ready","version":1,"session":"s"}`.

Each frame then consists of a 4-byte big-endian JSON header length, that many header bytes, and exactly `payload_bytes` encoded bytes. Header length is 1..65536, encoded payload is 1..8 MiB:

```json
{"codec":"vp8","page":"t1","display_id":2,"generation":1,"seq":1,"content_seq":42,"capture_ns":123456789,"encode_start_ns":123456790,"encode_end_ns":123457000,"width":1600,"height":2000,"keyframe":true,"frame_id":1,"reference_frame_id":-2,"payload_bytes":10000,"profile_level_id":""}
```

The payload is a complete VP8 encoded frame, or one H.264 Annex B access unit. Encoded dimensions must be even, at least 2×2, at most 3840×2400 and 7,464,960 pixels. `capture_ns` and encode timestamps are native Linux CLOCK_MONOTONIC nanoseconds. Both encode timestamps may be absent; otherwise both must be present and the end must not precede the start. `encode_ns` remains an optional legacy field and is not used as a substitute for these timestamps.

`seq` is strictly increasing for each registered page/source generation. `content_seq` must come from the actual native rendered/encoded frame, not from a later Python callback reading a current counter. Node proves the marker-render/removal sequence and supplies `content_seq_min`; the service releases only clean frames at or after this fence, starting with a fresh keyframe. Frames from unknown/closed pages or an old display/generation are dropped without disrupting sibling pages. Malformed frames and sequence rollback disconnect the authenticated host. Host disconnect closes its session's peers and permits a fresh host connection.

VP8 keyframe magic and dimensions are checked against the declared header. H.264 keyframes require actual IDR plus in-band SPS/PPS; actual SPS profile and level are checked. `frame_id` and `reference_frame_id` are native encoder identities only. In particular `-2` means unknown dependency and never permits an independently decodable delta. Node independently increments the public `frame_id` per LiveView; it must not expose or reset this native identity as its public frame counter.

Reverse messages on the same frame socket are NDJSON:

```json
{"type":"request_idr","session":"s","page":"t1","display_id":2,"generation":1,"reason":"connected"}
{"type":"encoder_config","session":"s","page":"t1","display_id":2,"generation":1,"bitrate_kbps":4000,"fps":60,"cbr":true}
```

IDR requests are coalesced to one per 500 ms with a deferred request retained, including static pages during connection. WAN encoder budget is the minimum active peer budget for the page, clamped to 250..12000 kbps; the host applies it with the native encoder's rate update. FPS 60 is a ceiling, not a measurement. Queue overflow closes the host rather than retaining an unbounded backlog.

## Queueing and recovery

Each peer holds at most one pending encoded frame and one frame being packetized. It has its own RTP packetizer and sender. Replacing a pending delta breaks the prediction chain: subsequent deltas wait for a fresh keyframe. Dropped or fenced content never bypasses this rule, even when reference metadata is unknown. NACK retransmission uses a 1024-packet cache; NACK, PLI, and FIR also request a source keyframe. Packet payload is at most 1100 bytes. WAN packet pacing is synchronous and does not create a hidden unbounded RTP queue.

`stats` reports codec, dimensions, connection state, received/sent/dropped frames, packets/bytes, keyframes, PLI/NACK, write errors, keyframe waiting, pending frame count, target bitrate, last native capture timestamp, and last encode duration. `capture_fps` is null until real measurement exists. These service counters are not proof of viewer presentation or physical input latency. `diagnostics.json` is atomic mode 0600, contains hashed scopes/counters, and excludes capabilities and network details.

## Controlled evidence and remaining acceptance

`codecprobe-result.json` records the real Chromium 153 H.264 receive-level result without SDP or candidates. `decoded-probe-result-v2.json` records a controlled 64×64 VP8 frame sent through authenticated Unix IPC, real Pion DTLS/RTP, and real Chromium RTCPeerConnection; the decoded pixel was verified. `integration-evidence.json` freezes the source, module, test, probe, and ARM64 binary hashes.

These tests do not access production, use the actual native host, prove high-resolution rendering, or measure physical interaction latency. Full worker acceptance must still verify native-source clean-frame identity, real first-frame presentation, hover/drag/Chinese committed input, ownership/epoch recovery, multi-page isolation, and ordinary authenticated browser use before retiring the old product path.
