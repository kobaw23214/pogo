// Shared by the native media broker and unit tests. Only explicitly selected
// numeric/enum fields leave the broker; ICE addresses, IDs and SDP never do.
const finite = value => typeof value === 'number' && Number.isFinite(value);
const number = (value, max = 1e12) => finite(value) && value >= 0 && value <= max ? value : null;
// RTCStats timestamps are performance.timeOrigin + performance.now(), not
// page-relative durations. Modern epoch milliseconds already exceed 1e12.
const statNumber = (key, value) => number(value, key === 'timestamp' ? Number.MAX_SAFE_INTEGER : 1e12);
const rounded = value => value === null ? null : Math.round(value * 100) / 100;
const clamp = (value, min, max) => Math.max(min, Math.min(max, value));

export function preferH264VideoCodec(pc, sender, senderType = globalThis.RTCRtpSender) {
  try {
    if (typeof pc?.getTransceivers !== 'function' || typeof senderType?.getCapabilities !== 'function') return false;
    const transceiver = pc.getTransceivers().find(item => item.sender === sender);
    if (typeof transceiver?.setCodecPreferences !== 'function') return false;
    const codecs = senderType.getCapabilities('video')?.codecs;
    const isH264 = codec => typeof codec?.mimeType === 'string' && codec.mimeType.toLowerCase() === 'video/h264';
    if (!Array.isArray(codecs) || !codecs.some(isH264)) return false;
    // Prefer H.264 without deleting alternative codecs, RTX or FEC. Preserve
    // their relative order so a peer without H.264 can still negotiate normally.
    transceiver.setCodecPreferences([...codecs.filter(isH264), ...codecs.filter(codec => !isH264(codec))]);
    return true;
  } catch {
    // Capability lookup and preference validation are optional. A browser that
    // rejects them must still create its offer with its default codec order.
    return false;
  }
}

// Payload types that only repair or protect another payload.
const repairCodecs = new Set(['rtx', 'red', 'ulpfec', 'flexfec', 'flexfec-03']);
const bitrateHintKeys = new Set(['x-google-start-bitrate', 'x-google-min-bitrate']);

// libwebrtc reads x-google-start-bitrate / x-google-min-bitrate (kbps) from the
// sender's remote description and applies them as the transport's initial
// bandwidth estimate and floor. Only primary video codecs are changed; audio,
// repair codecs, candidates and every other line stay byte-identical. Existing
// values are replaced: the deployment, not the viewer, selects these hints.
export function withVideoBitrateHints(sdp, { start_kbps: start, min_kbps: min }) {
  const eol = sdp.includes('\r\n') ? '\r\n' : '\n';
  const hints = `x-google-start-bitrate=${start};x-google-min-bitrate=${min}`;
  const sections = [[]];
  for (const line of sdp.split(eol)) {
    if (line.startsWith('m=')) sections.push([]);
    sections.at(-1).push(line);
  }
  return sections.map(lines => {
    if (!lines[0]?.startsWith('m=video ')) return lines;
    const codecs = new Map(), fmtp = new Set();
    for (const line of lines) {
      const rtpmap = /^a=rtpmap:(\d+) ([^/\s]+)\//.exec(line), parameters = /^a=fmtp:(\d+) /.exec(line);
      if (rtpmap) codecs.set(rtpmap[1], rtpmap[2].toLowerCase());
      if (parameters) fmtp.add(parameters[1]);
    }
    const primary = type => codecs.has(type) && !repairCodecs.has(codecs.get(type));
    return lines.flatMap(line => {
      const parameters = /^a=fmtp:(\d+) (.*)$/.exec(line);
      if (parameters && primary(parameters[1])) {
        const kept = parameters[2].split(';').map(item => item.trim()).filter(item => item && !bitrateHintKeys.has(item.split('=')[0].trim().toLowerCase()));
        return [`a=fmtp:${parameters[1]} ${[...kept, hints].join(';')}`];
      }
      const rtpmap = /^a=rtpmap:(\d+) /.exec(line);
      return rtpmap && primary(rtpmap[1]) && !fmtp.has(rtpmap[1]) ? [line, `a=fmtp:${rtpmap[1]} ${hints}`] : [line];
    });
  }).flat().join(eol);
}

export function senderMeasurements(report, previous) {
  const rows = Array.from(report.values ? report.values() : report);
  const outbound = rows.find(row => row.type === 'outbound-rtp' && !row.isRemote && (row.kind === 'video' || row.mediaType === 'video'));
  const codec = rows.find(row => row.type === 'codec' && row.id === outbound?.codecId);
  const transport = rows.find(row => row.type === 'transport' && row.id === outbound?.transportId);
  const pair = rows.find(row => row.type === 'candidate-pair' && row.id === transport?.selectedCandidatePairId)
    ?? rows.find(row => row.type === 'candidate-pair' && row.state === 'succeeded' && (row.selected || row.nominated));
  const remote = rows.find(row => row.type === 'remote-inbound-rtp' && row.localId === outbound?.id);
  const snapshot = outbound ? Object.fromEntries(['timestamp', 'bytesSent', 'framesEncoded', 'packetsSent', 'totalEncodeTime', 'totalPacketSendDelay'].map(key => [key, statNumber(key, outbound[key])])) : null;
  const elapsed = snapshot?.timestamp !== null && previous?.timestamp !== null ? snapshot?.timestamp - previous?.timestamp : 0;
  const delta = key => snapshot?.[key] !== null && previous?.[key] !== null && finite(snapshot?.[key]) && finite(previous?.[key]) && snapshot[key] >= previous[key] ? snapshot[key] - previous[key] : null;
  const bytes = delta('bytesSent'), frames = delta('framesEncoded'), packets = delta('packetsSent'), encode = delta('totalEncodeTime'), send = delta('totalPacketSendDelay');
  const validInterval = elapsed >= 100 && elapsed <= 10000;
  const metrics = {
    bitrate_bps: validInterval && bytes !== null ? rounded(bytes * 8000 / elapsed) : null,
    fps: validInterval && frames !== null ? rounded(frames * 1000 / elapsed) : number(outbound?.framesPerSecond, 240),
    encode_ms_per_frame: validInterval && frames > 0 && encode !== null ? rounded(encode * 1000 / frames) : null,
    send_delay_ms_per_packet: validInterval && packets > 0 && send !== null ? rounded(send * 1000 / packets) : null,
    rtt_ms: rounded(number(remote?.roundTripTime ?? pair?.currentRoundTripTime, 60) === null ? null : (remote?.roundTripTime ?? pair?.currentRoundTripTime) * 1000),
    available_bitrate_bps: number(pair?.availableOutgoingBitrate, 1e10),
    packet_loss_ratio: number(remote?.fractionLost, 1),
    quality_limitation: ['none', 'cpu', 'bandwidth', 'other'].includes(outbound?.qualityLimitationReason) ? outbound.qualityLimitationReason : 'unknown',
    codec: typeof codec?.mimeType === 'string' && /^video\/[a-zA-Z0-9._-]{1,32}$/.test(codec.mimeType) ? codec.mimeType : '',
    width: number(outbound?.frameWidth, 8192), height: number(outbound?.frameHeight, 8192),
  };
  return { metrics, snapshot };
}

export function initialMediaPolicy({ width, height, bitrate = 12_000_000 }, profile = 'reading') {
  if (!['reading', 'interactive'].includes(profile)) throw new Error('invalid media profile');
  // High-density text needs more bits than a low-resolution video call. Keep a
  // hard deployment ceiling, while avoiding a fixed 8Mbps for every viewport.
  const ceiling = clamp(Math.round(width * height * 3.8), Math.min(2_000_000, bitrate), Math.min(12_000_000, bitrate));
  return { profile, adaptation: 'balanced', max_framerate: profile === 'interactive' ? 60 : 30, max_bitrate_bps: ceiling, ceiling, healthy_samples: 0 };
}

export function adaptMediaPolicy(previous, measurements, profile = previous.profile) {
  if (!['reading', 'interactive'].includes(profile)) throw new Error('invalid media profile');
  const maxFPS = profile === 'interactive' ? 60 : 30;
  let fps = maxFPS, bitrate = previous.ceiling, adaptation = 'balanced';
  const encoding = measurements?.encode_ms_per_frame;
  if (measurements?.quality_limitation === 'cpu' || finite(encoding) && encoding > 900 / maxFPS) {
    adaptation = 'cpu';
    fps = Math.min(maxFPS, finite(encoding) && encoding > 0 ? clamp(Math.floor(750 / encoding), 10, maxFPS) : 30);
  }
  const available = measurements?.available_bitrate_bps, loss = measurements?.packet_loss_ratio;
  // BWE starts small and can remain small on a static page. An estimate alone
  // is not congestion evidence: cutting the sender cap would prevent native
  // probing and blur otherwise healthy text. Require observed loss or actual
  // packet queueing while frames are being sent as corroborating pressure.
  const queued = finite(measurements?.send_delay_ms_per_packet) && measurements.send_delay_ms_per_packet >= 20 && finite(measurements?.fps) && measurements.fps >= 5;
  const bandwidthLimited = measurements?.quality_limitation === 'bandwidth' && (queued || finite(loss) && loss >= 0.02);
  if (bandwidthLimited && finite(available) && available > 0) bitrate = Math.min(bitrate, Math.max(500_000, Math.floor(available * 0.8)));
  if (finite(loss) && loss >= 0.05) bitrate = Math.min(bitrate, Math.max(500_000, Math.floor(previous.max_bitrate_bps * 0.75)));
  if (bitrate < previous.ceiling * 0.9 || bandwidthLimited) {
    if (adaptation === 'balanced') adaptation = 'bandwidth';
    // Reduce work before queues build. Resolution stays fixed, preserving the
    // display-to-input binding and text density throughout the media epoch.
    fps = Math.min(fps, Math.max(profile === 'interactive' ? 20 : 10, Math.floor(maxFPS * Math.sqrt(bitrate / previous.ceiling))));
  }
  const downshift = fps < previous.max_framerate || bitrate < previous.max_bitrate_bps;
  const healthy = downshift ? 0 : Math.min(10, previous.healthy_samples + 1);
  const changedProfile = profile !== previous.profile;
  // Downshifts are immediate; recovery requires three healthy samples, then
  // climbs gradually. Repeated input cannot override a measured CPU limit.
  if (!changedProfile && healthy < 3) {
    fps = Math.min(fps, previous.max_framerate);
    bitrate = Math.min(bitrate, previous.max_bitrate_bps);
  } else if (!changedProfile) {
    fps = Math.min(fps, previous.max_framerate + 10);
    bitrate = Math.min(bitrate, Math.ceil(previous.max_bitrate_bps * 1.25));
  }
  return { ...previous, profile, adaptation, max_framerate: fps, max_bitrate_bps: bitrate, healthy_samples: healthy };
}

export function safeSenderStats(report) {
  const numeric = ['timestamp', 'framesEncoded', 'frameWidth', 'frameHeight', 'framesPerSecond', 'bytesSent', 'packetsSent', 'totalEncodeTime', 'totalPacketSendDelay', 'roundTripTime', 'fractionLost', 'availableOutgoingBitrate', 'currentRoundTripTime'];
  return Array.from(report.values ? report.values() : report).filter(row => ['outbound-rtp', 'remote-inbound-rtp', 'codec', 'media-source', 'candidate-pair'].includes(row.type)).map(row => {
    const safe = { type: row.type };
    for (const key of numeric) if (statNumber(key, row[key]) !== null) safe[key] = row[key];
    if (['none', 'cpu', 'bandwidth', 'other'].includes(row.qualityLimitationReason)) safe.qualityLimitationReason = row.qualityLimitationReason;
    if (typeof row.mimeType === 'string' && /^video\/[a-zA-Z0-9._-]{1,32}$/.test(row.mimeType)) safe.mimeType = row.mimeType;
    return safe;
  });
}
