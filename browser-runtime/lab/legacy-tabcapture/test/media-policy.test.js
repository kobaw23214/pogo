import test from 'node:test';
import assert from 'node:assert/strict';
import { initialMediaPolicy, adaptMediaPolicy, senderMeasurements, safeSenderStats, preferH264VideoCodec, withVideoBitrateHints } from '../src/media-policy.js';

test('bandwidth estimate hints change only primary video codec parameters of the answer', () => {
  const lines = ['v=0', 'o=- 4611731400430051336 2 IN IP4 127.0.0.1', 's=-', 't=0 0', 'a=group:BUNDLE 0 1',
    'm=video 9 UDP/TLS/RTP/SAVPF 96 97 102 103 45 98 99 116 117', 'c=IN IP4 0.0.0.0', 'a=ice-ufrag:synthetic', 'a=ice-pwd:synthetic-password', 'a=fingerprint:sha-256 AA:BB', 'a=mid:0',
    'a=extmap:3 http://www.webrtc.org/experiments/rtp-hdrext/playout-delay',
    'a=rtpmap:96 VP8/90000', 'a=rtcp-fb:96 transport-cc', 'a=rtpmap:97 rtx/90000', 'a=fmtp:97 apt=96',
    'a=rtpmap:102 H264/90000', 'a=rtcp-fb:102 nack', 'a=fmtp:102 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f;x-google-min-bitrate=100',
    'a=rtpmap:103 rtx/90000', 'a=fmtp:103 apt=102', 'a=rtpmap:45 AV1/90000', 'a=fmtp:45 level-idx=5;profile=0;tier=0',
    'a=rtpmap:98 VP9/90000', 'a=fmtp:98 profile-id=0;X-Google-Start-Bitrate=1', 'a=rtpmap:99 rtx/90000', 'a=fmtp:99 apt=98',
    'a=rtpmap:116 red/90000', 'a=rtpmap:117 ulpfec/90000', 'a=candidate:1 1 udp 2113937151 192.0.2.1 29780 typ host generation 0',
    'm=audio 9 UDP/TLS/RTP/SAVPF 111', 'a=mid:1', 'a=rtpmap:111 opus/48000/2', 'a=fmtp:111 minptime=10;useinbandfec=1', ''];
  const hints = 'x-google-start-bitrate=8000;x-google-min-bitrate=4000';
  const expected = lines.flatMap(line => {
    if (line === 'a=rtpmap:96 VP8/90000') return [line, `a=fmtp:96 ${hints}`];
    if (line.startsWith('a=fmtp:102 ')) return [`a=fmtp:102 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f;${hints}`];
    if (line.startsWith('a=fmtp:45 ')) return [`${line};${hints}`];
    if (line.startsWith('a=fmtp:98 ')) return [`a=fmtp:98 profile-id=0;${hints}`];
    return [line];
  });
  const sdp = lines.join('\r\n'), result = withVideoBitrateHints(sdp, { start_kbps: 8000, min_kbps: 4000 });
  assert.equal(result, expected.join('\r\n'), 'repair codecs, audio, ICE/DTLS lines and framing are unchanged');
  assert.equal(withVideoBitrateHints(result, { start_kbps: 8000, min_kbps: 4000 }), result, 'reapplying the deployment values is idempotent');
  assert.equal(withVideoBitrateHints(lines.join('\n'), { start_kbps: 8000, min_kbps: 4000 }), expected.join('\n'), 'LF-only SDP keeps its separator');
  const audioOnly = ['v=0', 'm=audio 9 UDP/TLS/RTP/SAVPF 111', 'a=rtpmap:111 opus/48000/2', ''].join('\r\n');
  assert.equal(withVideoBitrateHints(audioOnly, { start_kbps: 8000, min_kbps: 4000 }), audioOnly);
});

test('H264 preference preserves all fallback codecs, repair codecs and their original order', () => {
  const codecs = Object.freeze([
    { mimeType: 'video/VP8', clockRate: 90000 },
    { mimeType: 'video/rtx', clockRate: 90000 },
    { mimeType: 'video/H264', clockRate: 90000, sdpFmtpLine: 'profile-level-id=42001f' },
    { mimeType: 'video/VP9', clockRate: 90000, sdpFmtpLine: 'profile-id=0' },
    { mimeType: 'video/h264', clockRate: 90000, sdpFmtpLine: 'profile-level-id=42e01f' },
    { mimeType: 'video/red', clockRate: 90000 },
    { mimeType: 'video/ulpfec', clockRate: 90000 },
    { mimeType: 'video/AV1', clockRate: 90000 },
  ].map(Object.freeze));
  const original = [...codecs], sender = {};
  let preferences, unrelatedCalls = 0;
  const pc = { getTransceivers: () => [
    { sender: {}, setCodecPreferences: () => unrelatedCalls++ },
    { sender, setCodecPreferences: value => { preferences = value; } },
  ] };
  const senderType = { getCapabilities(kind) { assert.equal(kind, 'video'); return { codecs }; } };
  assert.equal(preferH264VideoCodec(pc, sender, senderType), true);
  assert.deepEqual(preferences, [codecs[2], codecs[4], codecs[0], codecs[1], codecs[3], codecs[5], codecs[6], codecs[7]]);
  assert.equal(new Set(preferences).size, codecs.length);
  assert.deepEqual(codecs, original, 'browser capability objects are not mutated');
  assert.notEqual(preferences, codecs);
  assert.equal(unrelatedCalls, 0, 'only this video sender receives the preference');
});

test('missing codec preference capabilities leave native negotiation available', () => {
  const sender = {}, capabilities = { getCapabilities: () => ({ codecs: [{ mimeType: 'video/H264' }] }) };
  for (const [pc, senderType] of [
    [null, capabilities],
    [{}, capabilities],
    [{ getTransceivers: () => [] }, capabilities],
    [{ getTransceivers: () => [{ sender: {}, setCodecPreferences() { assert.fail('unrelated transceiver'); } }] }, capabilities],
    [{ getTransceivers: () => [{ sender }] }, capabilities],
    [{ getTransceivers: () => [{ sender, setCodecPreferences() { assert.fail('capabilities unavailable'); } }] }, null],
    [{ getTransceivers: () => [{ sender, setCodecPreferences() { assert.fail('capabilities unavailable'); } }] }, {}],
  ]) assert.equal(preferH264VideoCodec(pc, sender, senderType), false);
});

test('absent H264 support preserves the browser codec order without applying preferences', () => {
  const sender = {};
  const pc = { getTransceivers: () => [{ sender, setCodecPreferences() { assert.fail('unsupported preference'); } }] };
  for (const capabilities of [null, {}, { codecs: null }, { codecs: [] }, { codecs: [{ mimeType: 'video/VP8' }, { mimeType: 'video/rtx' }] }]) {
    assert.equal(preferH264VideoCodec(pc, sender, { getCapabilities: () => capabilities }), false);
  }
});

test('rejected codec lookup or preference does not abort normal offer creation', async () => {
  for (const failure of ['transceivers', 'capabilities', 'preferences']) {
    const sender = {}, offers = [];
    const pc = {
      getTransceivers() {
        if (failure === 'transceivers') throw new Error('unavailable');
        return [{ sender, setCodecPreferences() { throw new Error('unsupported preference'); } }];
      },
      createOffer: async () => { offers.push('default offer'); return { type: 'offer', sdp: 'default codecs' }; },
    };
    const senderType = { getCapabilities() {
      if (failure === 'capabilities') throw new Error('unavailable');
      return { codecs: [{ mimeType: 'video/VP8' }, { mimeType: 'video/H264' }] };
    } };
    assert.equal(preferH264VideoCodec(pc, sender, senderType), false);
    assert.deepEqual(await pc.createOffer(), { type: 'offer', sdp: 'default codecs' });
    assert.equal(offers.length, 1, 'a rejected preference neither retries nor prevents the normal offer');
  }
});

test('sender diagnostics derive interval costs and expose no peer addresses or SDP', () => {
  const previous = { timestamp: 1000, bytesSent: 1000, framesEncoded: 10, packetsSent: 10, totalEncodeTime: 0.1, totalPacketSendDelay: 0.01 };
  const rows = [
    { id: 'video-secret', type: 'outbound-rtp', kind: 'video', timestamp: 2000, bytesSent: 501000, framesEncoded: 60, packetsSent: 110, totalEncodeTime: 0.6, totalPacketSendDelay: 0.21, frameWidth: 2560, frameHeight: 1600, codecId: 'codec-secret', transportId: 'transport-secret', qualityLimitationReason: 'cpu', secret: 'hidden' },
    { id: 'codec-secret', type: 'codec', mimeType: 'video/VP8', sdpFmtpLine: 'private' },
    { id: 'transport-secret', type: 'transport', selectedCandidatePairId: 'selected-secret' },
    { id: 'selected-secret', type: 'candidate-pair', availableOutgoingBitrate: 9000000, currentRoundTripTime: 0.09, localCandidateId: 'ip-secret' },
    { id: 'ip-secret', type: 'local-candidate', address: '192.0.2.1', port: 1234 },
    { type: 'remote-inbound-rtp', localId: 'video-secret', roundTripTime: 0.07, fractionLost: 0.02 },
  ];
  const { metrics } = senderMeasurements(new Map(rows.map((row, i) => [i, row])), previous);
  assert.deepEqual(metrics, { bitrate_bps: 4000000, fps: 50, encode_ms_per_frame: 10, send_delay_ms_per_packet: 2, rtt_ms: 70, available_bitrate_bps: 9000000, packet_loss_ratio: 0.02, quality_limitation: 'cpu', codec: 'video/VP8', width: 2560, height: 1600 });
  const safe = safeSenderStats(rows);
  assert.ok(safe.some(row => row.mimeType === 'video/VP8'));
  assert.doesNotMatch(JSON.stringify({ metrics, safe }), /secret|192\.0\.2\.1|sdpFmtpLine|localCandidateId|transportId/);
});

test('modern epoch timestamps preserve interval metrics and encoding feedback', () => {
  const timestamp = Date.UTC(2026, 9, 10) + .125;
  const before = { type: 'outbound-rtp', kind: 'video', timestamp, bytesSent: 1000, framesEncoded: 10, packetsSent: 10, totalEncodeTime: .1, totalPacketSendDelay: .01 };
  const first = senderMeasurements([before]);
  assert.equal(first.snapshot.timestamp, timestamp);
  const after = { ...before, timestamp: timestamp + 1000, bytesSent: 501000, framesEncoded: 60, packetsSent: 110, totalEncodeTime: 1.1, totalPacketSendDelay: .21 };
  const second = senderMeasurements([after], first.snapshot);
  assert.equal(second.metrics.bitrate_bps, 4000000); assert.equal(second.metrics.fps, 50);
  assert.equal(second.metrics.encode_ms_per_frame, 20); assert.equal(second.metrics.send_delay_ms_per_packet, 2);
  const policy = adaptMediaPolicy(initialMediaPolicy({ width: 1600, height: 1200 }, 'interactive'), second.metrics);
  assert.equal(policy.adaptation, 'cpu'); assert.equal(policy.max_framerate, 37);
  assert.equal(safeSenderStats([after])[0].timestamp, after.timestamp);
  for (const invalid of [-1, Infinity, NaN, Number.MAX_SAFE_INTEGER + 1]) {
    const row = { ...after, timestamp: invalid };
    assert.equal(senderMeasurements([row], first.snapshot).metrics.bitrate_bps, null);
    assert.equal('timestamp' in safeSenderStats([row])[0], false);
  }
  assert.equal(senderMeasurements([{ ...after, bytesSent: 1e12 + 1 }], first.snapshot).metrics.bitrate_bps, null, 'counter bounds remain unchanged');
});

test('missing samples and reset counters are unknown, never fabricated latency', () => {
  assert.equal(senderMeasurements([]).metrics.encode_ms_per_frame, null);
  const rows = [{ type: 'outbound-rtp', kind: 'video', timestamp: 500, bytesSent: 10, framesEncoded: 1, totalEncodeTime: 0.1, frameWidth: Infinity, qualityLimitationReason: 'arbitrary secret' }];
  const { metrics } = senderMeasurements(rows, { timestamp: 1000, bytesSent: 500, framesEncoded: 10, totalEncodeTime: 1 });
  assert.equal(metrics.bitrate_bps, null); assert.equal(metrics.encode_ms_per_frame, null);
  assert.equal(metrics.width, null); assert.equal(metrics.quality_limitation, 'unknown');
});

test('pixel-aware baseline respects explicit deployment ceilings', () => {
  assert.ok(initialMediaPolicy({ width: 1280, height: 720 }).ceiling < initialMediaPolicy({ width: 2560, height: 1600 }).ceiling);
  assert.equal(initialMediaPolicy({ width: 3840, height: 2160 }).ceiling, 12000000);
  assert.equal(initialMediaPolicy({ width: 3840, height: 2160, bitrate: 1000000 }).ceiling, 1000000);
  assert.equal(initialMediaPolicy({ width: 1280, height: 720 }).max_framerate, 30);
  assert.throws(() => initialMediaPolicy({ width: 1280, height: 720 }, 'arbitrary'));
});

test('interactive profile raises cadence without changing dimensions', () => {
  const initial = initialMediaPolicy({ width: 2560, height: 1600 });
  const interactive = adaptMediaPolicy(initial, {}, 'interactive');
  assert.equal(interactive.max_framerate, 60);
  assert.equal(interactive.ceiling, initial.ceiling);
  assert.equal(interactive.max_bitrate_bps, initial.max_bitrate_bps);
  assert.equal(adaptMediaPolicy(interactive, {}, 'reading').max_framerate, 30);
  assert.equal('width' in interactive, false, 'policy never resizes the authenticated video surface');
});

test('observed slow encoding immediately bounds work and input cannot override it', () => {
  const initial = initialMediaPolicy({ width: 2560, height: 1600 }, 'interactive');
  const cpu = adaptMediaPolicy(initial, { encode_ms_per_frame: 40, quality_limitation: 'cpu' });
  assert.equal(cpu.adaptation, 'cpu'); assert.equal(cpu.max_framerate, 18);
  const reading = adaptMediaPolicy(cpu, { encode_ms_per_frame: 40, quality_limitation: 'cpu' }, 'reading');
  assert.equal(adaptMediaPolicy(reading, { encode_ms_per_frame: 40, quality_limitation: 'cpu' }, 'interactive').max_framerate, 18);
});

test('estimated bandwidth and packet loss reduce offered bitrate before queueing', () => {
  const initial = initialMediaPolicy({ width: 1920, height: 1080 }, 'interactive');
  const network = adaptMediaPolicy(initial, { available_bitrate_bps: 2000000, quality_limitation: 'bandwidth', fps: 30, send_delay_ms_per_packet: 25 });
  assert.equal(network.max_bitrate_bps, 1600000);
  assert.ok(network.max_framerate < initial.max_framerate); assert.equal(network.adaptation, 'bandwidth');
  const loss = adaptMediaPolicy(initial, { packet_loss_ratio: 0.1 });
  assert.equal(loss.max_bitrate_bps, Math.floor(initial.max_bitrate_bps * 0.75));
  assert.ok(adaptMediaPolicy(network, { available_bitrate_bps: 1000 }).max_bitrate_bps >= 500000);
});

test('recovery waits for stable samples and climbs gradually within current limits', () => {
  const initial = initialMediaPolicy({ width: 2560, height: 1600 }, 'interactive');
  const slow = adaptMediaPolicy(initial, { available_bitrate_bps: 1000000, quality_limitation: 'bandwidth', packet_loss_ratio: 0.03 });
  let next = slow;
  for (let i = 0; i < 2; i++) next = adaptMediaPolicy(next, { available_bitrate_bps: 8000000, quality_limitation: 'bandwidth', packet_loss_ratio: 0.03 });
  assert.equal(next.max_bitrate_bps, slow.max_bitrate_bps);
  next = adaptMediaPolicy(next, { available_bitrate_bps: 8000000, quality_limitation: 'bandwidth', packet_loss_ratio: 0.03 });
  assert.equal(next.max_bitrate_bps, Math.ceil(slow.max_bitrate_bps * 1.25));
  assert.ok(next.max_bitrate_bps <= 6400000);
  for (let i = 0; i < 30; i++) next = adaptMediaPolicy(next, { available_bitrate_bps: 8000000, quality_limitation: 'bandwidth', packet_loss_ratio: 0.03 });
  assert.equal(next.max_bitrate_bps, 6400000);
});

test('an estimate following the sender cap does not trap the stream at low quality', () => {
  const initial = initialMediaPolicy({ width: 2560, height: 1600 }, 'interactive');
  let next = adaptMediaPolicy(initial, { available_bitrate_bps: 1000000, quality_limitation: 'bandwidth', packet_loss_ratio: 0.03 });
  for (let i = 0; i < 20; i++) next = adaptMediaPolicy(next, { available_bitrate_bps: next.max_bitrate_bps * 1.1, quality_limitation: 'none' });
  assert.equal(next.max_bitrate_bps, initial.ceiling);
  assert.equal(next.max_framerate, initial.max_framerate);
});

test('low initial or static-page BWE without measured pressure preserves quality headroom', () => {
  const initial = initialMediaPolicy({ width: 2560, height: 1600 }, 'interactive');
  for (const reason of ['none', 'bandwidth']) {
    let next = initial;
    for (let i = 0; i < 20; i++) next = adaptMediaPolicy(next, { available_bitrate_bps: 300000, quality_limitation: reason, packet_loss_ratio: 0, send_delay_ms_per_packet: 1, fps: 1 });
    assert.equal(next.max_bitrate_bps, initial.ceiling);
    assert.equal(next.max_framerate, 60);
    assert.equal(next.adaptation, 'balanced');
  }
});
