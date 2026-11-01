(async () => {
  const { initialMediaPolicy, adaptMediaPolicy, senderMeasurements, safeSenderStats, preferH264VideoCodec, withVideoBitrateHints } = await import('./media-policy.js');
  const peers = new Map(), sources = new Map();
  const emit = (record, event) => {
    if (!record.closed && typeof globalThis.__pogoMediaEvent === 'function') {
      Promise.resolve(globalThis.__pogoMediaEvent({ id: record.id, ...event })).catch(() => {});
    }
  };
  const stop = record => {
    if (!record || record.closed) return;
    record.closed = true;
    clearInterval(record.sampleTimer);
    record.pendingCandidates = [];
    record.pc?.close();
    record.stream?.getTracks().forEach(track => track.stop());
    const source = record.source;
    if (source) {
      source.users.delete(record);
      if (!source.users.size) {
        source.stream.getTracks().forEach(track => track.stop());
        if (sources.get(source.targetId) === source) sources.delete(source.targetId);
      } else applyCaptureProfile(source).catch(() => {});
    }
    if (peers.get(record.id) === record) peers.delete(record.id);
  };
  const current = id => {
    const record = peers.get(id);
    if (!record || record.closed) throw new Error('media epoch is closed');
    return record;
  };
  const diagnostics = record => ({
    ...record.measurements,
    profile: record.policy.profile, adaptation: record.policy.adaptation,
    max_framerate: record.applied?.max_framerate ?? null,
    max_bitrate_bps: record.applied?.max_bitrate_bps ?? null,
    width: record.settings?.width ?? null, height: record.settings?.height ?? null,
    sample_age_ms: record.sampledAt ? Math.round(performance.now() - record.sampledAt) : null,
    tuning_supported: record.tuningSupported,
    capture_fps: record.source?.captureSettings?.frameRate ?? null,
    capture_min_fps: record.source?.captureProfile ? record.source.captureProfile === 'interactive' ? 15 : 1 : null,
    capture_tuning_supported: record.source?.captureTuningSupported ?? false,
    content_hint: record.stream?.getVideoTracks()[0]?.contentHint === 'text' ? 'text' : '',
  });
  const applyCaptureProfile = source => {
    if (!source) return Promise.resolve();
    source.captureQueue = source.captureQueue.catch(() => {}).then(async () => {
      if (!source.users.size || !source.captureTuningSupported) return;
      // Capture belongs to the shared source. One reading viewer must never
      // lower the native cadence while a different viewer is interacting.
      const profile = [...source.users].some(user => !user.closed && user.policy.profile === 'interactive') ? 'interactive' : 'reading';
      if (source.captureProfile === profile) return;
      const track = source.stream.getVideoTracks()[0];
      try {
        await track.applyConstraints({ ...source.captureConstraints, frameRate: profile === 'interactive' ? { min: 15, ideal: 60, max: 60 } : { min: 1, ideal: 30, max: 30 } });
        if (!source.users.size || track.readyState !== 'live') return;
        const settings = track.getSettings();
        if (settings.width !== source.captureDimensions.width || settings.height !== source.captureDimensions.height || settings.deviceId !== source.captureDeviceID) {
          // A backend that changes pixels despite exact inherited constraints
          // cannot keep tuning a frame-bound stream. Restore or retire it.
          source.captureTuningSupported = false;
          await track.applyConstraints(source.captureConstraints).catch(() => {});
          const restored = track.getSettings();
          if (restored.width !== source.captureDimensions.width || restored.height !== source.captureDimensions.height || restored.deviceId !== source.captureDeviceID) {
            for (const user of [...source.users]) { emit(user, { type: 'state', state: 'closed' }); stop(user); }
          }
          return;
        }
        source.captureProfile = profile; source.captureSettings = settings;
      } catch { if (source.users.size) source.captureTuningSupported = false; }
    });
    return source.captureQueue;
  };
  const applyPolicy = record => {
    record.tuneQueue = record.tuneQueue.catch(() => {}).then(async () => {
      if (record.closed || !record.sender) return;
      await applyCaptureProfile(record.source);
      if (record.closed || !record.tuningSupported) return;
      const policy = record.policy;
      if (record.applied?.max_framerate === policy.max_framerate && record.applied?.max_bitrate_bps === policy.max_bitrate_bps && record.applied?.profile === policy.profile) return;
      try {
        const parameters = record.sender.getParameters();
        parameters.degradationPreference = 'maintain-resolution';
        if (!parameters.encodings?.length) return;
        parameters.encodings[0].maxBitrate = policy.max_bitrate_bps;
        parameters.encodings[0].maxFramerate = policy.max_framerate;
        // No resizing or scaleResolutionDownBy: a displayed pixel must remain
        // bound to the same authenticated CSS viewport for the entire epoch.
        await record.sender.setParameters(parameters);
        if (record.closed) return;
        // Keep Chromium's screen-content encoding path for both profiles.
        // A motion hint increased packet assembly and receiver buffering in
        // the measured browser UI despite an uncongested local connection.
        record.stream.getVideoTracks()[0].contentHint = 'text';
        record.applied = { ...policy };
      } catch { if (!record.closed) record.tuningSupported = false; }
    });
    return record.tuneQueue;
  };
  const sample = async record => {
    if (record.closed || record.sampling) return;
    record.sampling = true;
    try {
      const report = await record.pc.getStats();
      if (record.closed) return;
      const result = senderMeasurements(report, record.lastSample);
      record.lastSample = result.snapshot; record.measurements = result.metrics; record.sampledAt = performance.now();
      // Let the encoder and bandwidth estimator warm up before interpreting
      // their initial small/partial samples as a reason to lower quality.
      record.sampleCount++;
      record.policyMeasurements = record.sampleCount >= 3 ? record.measurements : {};
      record.policy = adaptMediaPolicy(record.policy, record.policyMeasurements);
      await applyPolicy(record);
    } catch { /* diagnostics must never terminate a healthy video stream */ }
    finally { record.sampling = false; }
  };
  const prepare = (targetId, sourceKey) => {
    let source = sources.get(targetId);
    if (source && source.key !== sourceKey) {
      // A viewport/document/control change supersedes every old source lease.
      // Each old viewer receives a stop; none can keep its former input grant.
      for (const record of [...source.users]) { emit(record, { type: 'state', state: 'closed' }); stop(record); }
      source = null;
    }
    return source ? source.stream.getVideoTracks()[0].getSettings() : null;
  };
  const create = async config => {
    if (peers.has(config.id)) throw new Error('media epoch already exists');
    const record = { id: config.id, targetId: config.targetId, closed: false, pc: null, stream: null, source: null, remoteReady: false, pendingCandidates: [], signalQueue: Promise.resolve(), tuneQueue: Promise.resolve(), policy: initialMediaPolicy(config), measurements: senderMeasurements([]).metrics, policyMeasurements: {}, sampleCount: 0, tuningSupported: true, bitrateHints: config.bitrateHints ?? null };
    peers.set(record.id, record);
    try {
      let source = sources.get(config.targetId);
      if (source && source.key !== config.sourceKey) throw new Error('media source identity changed');
      if (!source) {
        if (!config.streamId) throw new Error('fresh native capture authorization is required');
        const stream = await navigator.mediaDevices.getUserMedia({ audio: false, video: { mandatory: {
          chromeMediaSource: 'tab', chromeMediaSourceId: config.streamId,
          minWidth: config.width, maxWidth: config.width,
          minHeight: config.height, maxHeight: config.height,
          minFrameRate: 1, maxFrameRate: 60,
        } } });
        if (record.closed) { stream.getTracks().forEach(track => track.stop()); throw new Error('media epoch closed during capture'); }
        const captureTrack = stream.getVideoTracks()[0], captureSettings = captureTrack.getSettings();
        const captureConstraints = captureTrack.getConstraints();
        // Chromium tabCapture exposes its one-use authorization as a deviceId
        // constraint that cannot be applied a second time. Keep the same track
        // and exact pixel constraints; verify its source identity after tuning.
        delete captureConstraints.deviceId;
        source = { key: config.sourceKey, targetId: config.targetId, stream, users: new Set(), captureQueue: Promise.resolve(), captureProfile: null, captureSettings, captureConstraints, captureDeviceID: captureSettings.deviceId, captureDimensions: { width: captureSettings.width, height: captureSettings.height }, captureTuningSupported: true };
        sources.set(config.targetId, source);
        stream.getVideoTracks()[0].onended = () => {
          for (const user of [...source.users]) { emit(user, { type: 'state', state: 'closed' }); stop(user); }
        };
      }
      record.source = source; source.users.add(record);
      // Viewers share compositor capture, but each display epoch owns a clone,
      // new PeerConnection and new decoder. Closing one never stops the others.
      record.stream = new MediaStream([source.stream.getVideoTracks()[0].clone()]);
      const track = record.stream.getVideoTracks()[0];
      track.contentHint = 'text';
      const pc = record.pc = new RTCPeerConnection({ iceServers: config.iceServers, bundlePolicy: 'max-bundle' });
      pc.onicecandidate = event => { if (event.candidate) emit(record, { type: 'candidate', candidate: event.candidate.toJSON() }); };
      pc.onconnectionstatechange = () => emit(record, { type: 'state', state: pc.connectionState });
      record.sender = pc.addTrack(track, record.stream);
      // Codec choice belongs to this receiver, never to the shared capture.
      // Standard viewers retain Chromium's unmodified negotiation defaults.
      if (config.codecPreference === 'responsive') preferH264VideoCodec(pc, record.sender);
      record.settings = track.getSettings();
      record.policy = initialMediaPolicy({ ...record.settings, bitrate: config.bitrate });
      await applyPolicy(record);
      await pc.setLocalDescription(await pc.createOffer());
      if (record.closed) throw new Error('media epoch closed during negotiation');
      record.sampleTimer = setInterval(() => sample(record), 1000);
      return { offer: pc.localDescription.toJSON(), settings: track.getSettings() };
    } catch (error) { stop(record); throw error; }
  };
  globalThis.pogoMedia = Object.freeze({
    prepare, create,
    answer: async (id, answer) => {
      const record = current(id);
      record.signalQueue = record.signalQueue.then(async () => {
        if (record.closed) throw new Error('media epoch is closed');
        // Deployment-selected bandwidth estimate start/floor (LAN profile only)
        // belong in the remote description this sender reads them from.
        await record.pc.setRemoteDescription(record.bitrateHints ? { type: answer.type, sdp: withVideoBitrateHints(answer.sdp, record.bitrateHints) } : answer);
        if (record.closed) throw new Error('media epoch closed during answer');
        record.remoteReady = true;
        while (record.pendingCandidates.length) {
          if (record.closed) throw new Error('media epoch closed while applying candidates');
          await record.pc.addIceCandidate(record.pendingCandidates.shift());
        }
      });
      return record.signalQueue;
    },
    candidate: (id, candidate) => {
      const record = current(id);
      if (!record.remoteReady) {
        if (record.pendingCandidates.length >= 64) throw new Error('too many pending media ICE candidates');
        record.pendingCandidates.push(candidate); return;
      }
      record.signalQueue = record.signalQueue.then(() => {
        if (record.closed) throw new Error('media epoch is closed');
        return record.pc.addIceCandidate(candidate);
      });
      return record.signalQueue;
    },
    stats: async id => {
      const record = current(id), report = await record.pc.getStats();
      if (record.closed) throw new Error('media epoch is closed');
      return safeSenderStats(report);
    },
    diagnostics: id => diagnostics(current(id)),
    setProfile: async (id, profile) => {
      const record = current(id);
      if (!['interactive', 'reading'].includes(profile)) throw new Error('invalid media profile');
      if (record.policy.profile !== profile) {
        record.policy = adaptMediaPolicy(record.policy, record.policyMeasurements, profile);
        await applyPolicy(record);
      }
      if (record.closed) throw new Error('media epoch is closed');
      return diagnostics(record);
    },
    close: id => stop(peers.get(id)),
    closeAll: () => { for (const record of peers.values()) stop(record); },
  });
})();
