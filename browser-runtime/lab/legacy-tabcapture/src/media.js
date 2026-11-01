import { chromium } from 'playwright';
import http from 'node:http';
import { createHash, randomUUID } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { MEDIA_DEVICE_SCALE_FACTORS } from './media-config.js';

const extensionPath = fileURLToPath(new URL('./media-extension/', import.meta.url));
const validID = id => typeof id === 'string' && /^[A-Za-z0-9_-]{1,160}$/.test(id);
const positive = (value, max) => Number.isSafeInteger(value) && value > 0 && value <= max;

// scale is the viewer's request (1-2 device pixels per CSS pixel); density is
// the remote compositor's device scale factor. Capturing above the raster
// density would only upsample it.
export function mediaCaptureSize(width, height, scale = 1, density = 2) {
  if (!positive(width, 8192) || !positive(height, 8192) || typeof scale !== 'number' || !Number.isFinite(scale) || scale < 1 || scale > 2) throw new Error('invalid media viewport or pixel scale');
  if (!MEDIA_DEVICE_SCALE_FACTORS.includes(density)) throw new Error('invalid media device scale factor');
  const factor = Math.min(scale, density, 3840 / width, 2160 / height, Math.sqrt(8_294_400 / (width * height)));
  // Codec dimensions are even; CSS layout and input coordinates stay untouched.
  return { width: Math.max(2, Math.floor(width * factor / 2) * 2), height: Math.max(2, Math.floor(height * factor / 2) * 2) };
}

export function normalizeMediaIceServers(value = []) {
  if (!Array.isArray(value) || value.length > 8) throw new Error('invalid media ICE servers');
  return value.map(server => {
    if (!server || typeof server !== 'object' || Array.isArray(server) || Object.keys(server).some(key => !['urls', 'username', 'credential'].includes(key))) throw new Error('invalid media ICE server');
    const urls = typeof server.urls === 'string' ? [server.urls] : server.urls;
    if (!Array.isArray(urls) || !urls.length || urls.length > 8 || urls.some(url => typeof url !== 'string' || url.length > 2048 || !/^(?:stun|stuns|turn|turns):[^\s]+$/.test(url))) throw new Error('invalid media ICE server URL');
    for (const key of ['username', 'credential']) if (server[key] !== undefined && (typeof server[key] !== 'string' || server[key].length > 1024)) throw new Error('invalid media ICE credential');
    return { urls: [...urls], ...(server.username === undefined ? {} : { username: server.username }), ...(server.credential === undefined ? {} : { credential: server.credential }) };
  });
}

export function normalizeMediaPortRange(value) {
  if (value === undefined || value === '') return undefined;
  const match = typeof value === 'string' && /^(\d{1,5})-(\d{1,5})$/.exec(value);
  if (!match || Number(match[1]) < 1024 || Number(match[2]) > 65535 || Number(match[1]) > Number(match[2])) throw new Error('invalid media UDP port range');
  return `${Number(match[1])}-${Number(match[2])}`;
}

// libwebrtc sizes libvpx VP8 threads from every host core (sysconf; neither the
// worker's CFS quota nor a cpuset changes it): 8 threads at 1080p+ on a large
// host. Their bursts use up the default 2-CPU quota early in each period and
// stall every worker thread. The cap is fixed and does not follow
// POGO_BROWSER_CPUS: measured at quotas 2 and 4 with captures up to 1728x2160,
// and at 4 CPUs encoding is not the bottleneck. Each viewer still has its own
// encoder, so a larger quota serves more viewers. OpenH264 stays
// single-threaded (one slice in packetization-mode 1), so only VP8 encoders
// are affected.
export const MEDIA_ENCODER_THREAD_LIMIT = 2;

// The only place that builds Chromium's --force-fieldtrials value; every
// trial is concatenated into one switch. Network-sensitive trials apply only
// to a LAN media path (POGO_BROWSER_WEBRTC_NETWORK=lan); zero playout delay
// follows it unless POGO_BROWSER_WEBRTC_PLAYOUT_DELAY sets it. The trials are
// browser-process wide: WebRTC started by a remote web page gets them too.
export function mediaFieldTrials(network = 'wan', playoutDelay = network === 'lan' ? 'zero' : 'default') {
  if (!['lan', 'wan'].includes(network)) throw new Error('invalid media network profile');
  if (!['zero', 'default'].includes(playoutDelay)) throw new Error('invalid media playout delay');
  const trials = [];
  // Screen content uses libwebrtc's ALR pacing defaults (factor 1.0, queue
  // limit 2875 ms): large scrolled VP8 frames wait hundreds of milliseconds
  // behind a low bandwidth estimate. Drain at 2.5x and bound the queue.
  if (network === 'lan') trials.push('WebRTC-ProbingScreenshareBwe/2.5,250,80,40,-60,3/');
  // Ask the viewer (playout-delay RTP header extension, negotiated by default)
  // to render each frame as soon as it is decoded instead of holding it for a
  // jitter-buffer target; safe only without real network jitter.
  if (playoutDelay === 'zero') trials.push('WebRTC-ForceSendPlayoutDelay/min_ms:0,max_ms:0/');
  // CPU only, so every network profile.
  trials.push(`WebRTC-VideoEncoderSettings/encoder_thread_limit:${MEDIA_ENCODER_THREAD_LIMIT}/`);
  return trials.join('');
}

// Every media epoch has a new PeerConnection whose bandwidth estimate starts at
// libwebrtc's 300 kbps. A LAN path starts near the text-quality budget and keeps
// a floor (kbps; written into the answer the sender applies). Others: defaults.
// Only standard viewers use them: on the responsive (single-threaded software
// H.264) path the measured hints raised encoder load and scrolled frame age.
export function mediaBitrateHints(network = 'wan', codecPreference = 'standard') {
  if (!['lan', 'wan'].includes(network)) throw new Error('invalid media network profile');
  return network === 'lan' && codecPreference === 'standard' ? { start_kbps: 8000, min_kbps: 4000 } : null;
}

function answerDescription(value) {
  const description = typeof value === 'string' ? { type: 'answer', sdp: value } : value;
  if (!description || description.type !== 'answer' || typeof description.sdp !== 'string' || description.sdp.length > 131072 || !description.sdp.startsWith('v=0') || Object.keys(description).some(key => !['type', 'sdp'].includes(key))) throw new Error('invalid media SDP answer');
  return { type: 'answer', sdp: description.sdp };
}

function candidateDescription(value) {
  if (value === null) return null;
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).some(key => !['candidate', 'sdpMid', 'sdpMLineIndex', 'usernameFragment'].includes(key)) || typeof value.candidate !== 'string' || value.candidate.length > 8192 || /[\r\n]/.test(value.candidate)) throw new Error('invalid media ICE candidate');
  if (value.sdpMid !== undefined && value.sdpMid !== null && (typeof value.sdpMid !== 'string' || value.sdpMid.length > 128)) throw new Error('invalid media candidate mid');
  if (value.sdpMLineIndex !== undefined && value.sdpMLineIndex !== null && (!Number.isSafeInteger(value.sdpMLineIndex) || value.sdpMLineIndex < 0 || value.sdpMLineIndex > 64)) throw new Error('invalid media candidate line');
  if (value.usernameFragment !== undefined && value.usernameFragment !== null && (typeof value.usernameFragment !== 'string' || value.usernameFragment.length > 256)) throw new Error('invalid media candidate username');
  return { ...value };
}

async function prepareMediaContext(context, extensionId, brokerURL, reserveCapture, network = 'wan', deviceScaleFactor = 2) {
  let capturePage, page, creatingInternalPage = false;
  const internalPages = new Set(), pageEvents = [], peers = new Map();
  const createInternalPage = async () => {
    creatingInternalPage = true;
    try { const created = await context.newPage(); internalPages.add(created); return created; }
    finally {
      creatingInternalPage = false;
      for (const [observed, attach] of pageEvents.splice(0)) attach(observed);
    }
  };
  let closed = false, creationQueue = Promise.resolve(), initialization = null, initialized = false;
  const notify = (record, type, value) => {
    if (!record || record.closed) return;
    try { Promise.resolve(type === 'candidate' ? record.onCandidate?.(value) : record.onState?.(value)).catch(() => {}); } catch { /* observers do not own capture cleanup */ }
  };
  const invalidate = () => {
    if (closed) return;
    closed = true;
    for (const record of peers.values()) { notify(record, 'state', 'closed'); record.closed = true; record.release(); record.source.off('close', record.sourceClosed); }
    peers.clear();
  };
  const closeRecord = async record => {
    if (!record || record.closed) return;
    record.closed = true;
    record.release();
    peers.delete(record.id);
    record.source.off('close', record.sourceClosed);
    if (page && !page.isClosed()) await page.evaluate(id => globalThis.pogoMedia?.close(id), record.id).catch(() => {});
  };
  const captureURL = `chrome-extension://${extensionId}/capture.html`;
  const ensureReady = () => {
    if (!initialization) initialization = (async () => {
      try {
        if (closed) throw new Error('media context is closed');
        capturePage = await createInternalPage();
        capturePage.on('close', () => { invalidate(); page?.close().catch(() => {}); });
        capturePage.on('crash', () => { invalidate(); page?.close().catch(() => {}); });
        capturePage.on('framenavigated', frame => { if (initialized && frame === capturePage.mainFrame() && capturePage.url() !== captureURL) { invalidate(); page?.close().catch(() => {}); capturePage.close().catch(() => {}); } });
        await capturePage.goto(captureURL, { waitUntil: 'load' });
        await capturePage.waitForFunction(() => typeof globalThis.pogoCapture?.streamId === 'function');
        page = await createInternalPage();
        page.on('close', invalidate);
        page.on('crash', invalidate);
        page.on('framenavigated', frame => { if (initialized && frame === page.mainFrame() && page.url() !== brokerURL) { invalidate(); page.close().catch(() => {}); } });
        await page.exposeBinding('__pogoMediaEvent', ({ frame }, event) => {
          if (closed || frame !== page.mainFrame() || page.url() !== brokerURL || !event || !validID(event.id)) return;
          const record = peers.get(event.id);
          if (event.type === 'candidate') notify(record, 'candidate', candidateDescription(event.candidate));
          else if (event.type === 'state' && ['new', 'connecting', 'connected', 'disconnected', 'failed', 'closed'].includes(event.state)) {
            notify(record, 'state', event.state);
            if (event.state === 'closed') closeRecord(record).catch(() => {});
          }
        });
        await page.goto(brokerURL, { waitUntil: 'load' });
        await page.waitForFunction(() => typeof globalThis.pogoMedia?.create === 'function');
        initialized = true;
      } catch (error) { invalidate(); await page?.close().catch(() => {}); await capturePage?.close().catch(() => {}); throw error; }
    })();
    return initialization;
  };
  return {
    get page() { return page; },
    internalPages,
    // Preload the two private broker pages without capturing a user tab or
    // consuming an encoder slot. The first visible stream can reuse readiness.
    warm: () => closed || page?.isClosed() ? Promise.reject(new Error('media context is closed')) : ensureReady(),
    // Buffer only while creating one known internal Page, then exclude that
    // exact object. Concurrent real popups are flushed and attached normally.
    observePage(observed, attach) {
      if (creatingInternalPage) pageEvents.push([observed, attach]);
      else attach(observed);
    },
    async create(source, { id = randomUUID(), width, height, scale = 1, sourceKey = 'initial', iceServers = [], bitrate = 12_000_000, codecPreference = 'standard', onCandidate, onState } = {}) {
      const preceding = creationQueue; let unlock;
      creationQueue = new Promise(resolve => { unlock = resolve; });
      await preceding;
      try {
        if (closed || page?.isClosed() || initialized && page.url() !== brokerURL) throw new Error('media context is closed');
        if (!validID(id) || peers.has(id)) throw new Error('invalid or duplicate media epoch');
        if (!source || source === page || source === capturePage || source.context() !== context || source.isClosed()) throw new Error('media page does not belong to this browser context');
        if (typeof sourceKey !== 'string' || !sourceKey.length || sourceKey.length > 512 || /[\r\n]/.test(sourceKey)) throw new Error('invalid media source identity');
        if (!positive(bitrate, 12_000_000) || bitrate < 1_000_000) throw new Error('invalid media bitrate');
        if (!['standard', 'responsive'].includes(codecPreference)) throw new Error('invalid media codec preference');
        const pixels = mediaCaptureSize(width, height, scale, deviceScaleFactor), servers = normalizeMediaIceServers(iceServers);
        const record = { id, source, closed: false, onCandidate, onState, sourceClosed: null, release: reserveCapture() };
        record.sourceClosed = () => { notify(record, 'state', 'closed'); closeRecord(record).catch(() => {}); };
        peers.set(id, record); source.once('close', record.sourceClosed);
        let client;
        try {
          await ensureReady();
          if (record.closed || closed) throw new Error('media page closed during capture setup');
          client = await context.newCDPSession(source);
          const { targetInfo } = await client.send('Target.getTargetInfo');
          await client.detach(); client = null;
          if (record.closed || closed || source.isClosed()) throw new Error('media page closed during capture setup');
          client = await context.newCDPSession(page);
          const { targetInfo: consumer } = await client.send('Target.getTargetInfo');
          await client.detach(); client = null;
          const existing = await page.evaluate(({ targetId, sourceKey }) => globalThis.pogoMedia.prepare(targetId, sourceKey), { targetId: targetInfo.targetId, sourceKey });
          const streamId = existing ? undefined : await capturePage.evaluate(({ targetId, consumerTargetId }) => globalThis.pogoCapture.streamId(targetId, consumerTargetId), { targetId: targetInfo.targetId, consumerTargetId: consumer.targetId });
          if (record.closed || closed) throw new Error('media epoch closed during capture setup');
          const result = await page.evaluate(config => globalThis.pogoMedia.create(config), { id, targetId: targetInfo.targetId, sourceKey, streamId, ...pixels, iceServers: servers, bitrate, codecPreference, bitrateHints: mediaBitrateHints(network, codecPreference) });
          if (record.closed || closed) { await page.evaluate(epoch => globalThis.pogoMedia?.close(epoch), id).catch(() => {}); throw new Error('media epoch closed during capture setup'); }
          const evaluate = (operation, value) => {
            if (closed || record.closed || source.isClosed()) return Promise.reject(new Error('media epoch is closed'));
            return page.evaluate(({ id, operation, value }) => globalThis.pogoMedia[operation](id, value), { id, operation, value });
          };
          return {
            offer: result.offer, settings: result.settings, pixels: { width: result.settings.width, height: result.settings.height },
            answer: answer => evaluate('answer', answerDescription(answer)),
            candidate: candidate => evaluate('candidate', candidateDescription(candidate)),
            stats: () => evaluate('stats'),
            diagnostics: () => evaluate('diagnostics'),
            setProfile: profile => {
              if (!['interactive', 'reading'].includes(profile)) return Promise.reject(new Error('invalid media profile'));
              return evaluate('setProfile', profile);
            },
            close: () => closeRecord(record),
          };
        } catch (error) { await client?.detach().catch(() => {}); await closeRecord(record); throw error; }
      } finally { unlock(); }
    },
    async close() {
      if (!closed) await Promise.allSettled([...peers.values()].map(closeRecord));
      invalidate();
      if (page && !page.isClosed()) await page.close().catch(() => {});
      if (capturePage && !capturePage.isClosed()) await capturePage.close().catch(() => {});
    },
  };
}

export async function launchMediaBrowser({ sandbox = true, env, udpPortRange, maxCaptures = 4, network = 'wan', playoutDelay, deviceScaleFactor = 2 } = {}) {
  if (!positive(maxCaptures, 16)) throw new Error('invalid active media capture limit');
  if (!MEDIA_DEVICE_SCALE_FACTORS.includes(deviceScaleFactor)) throw new Error('invalid media device scale factor');
  if (typeof sandbox !== 'boolean') throw new Error('invalid browser sandbox option');
  const portRange = normalizeMediaPortRange(udpPortRange);
  // An omitted playout delay follows the network profile (mediaFieldTrials).
  const fieldTrials = mediaFieldTrials(network, playoutDelay);
  const manifest = JSON.parse(await readFile(join(extensionPath, 'manifest.json'), 'utf8'));
  const extensionId = createHash('sha256').update(Buffer.from(manifest.key, 'base64')).digest('hex').slice(0, 32).replace(/[0-9a-f]/g, digit => String.fromCharCode(97 + parseInt(digit, 16)));
  const profile = await mkdtemp(join(tmpdir(), 'pogo-media-browser-'));
  let owner, server;
  const closeServer = async () => { if (server?.listening) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); } };
  try {
    await mkdir(join(profile, 'Default'));
    await writeFile(join(profile, 'Default', 'Preferences'), JSON.stringify({ extensions: { settings: { [extensionId]: { incognito: true } } }, ...(portRange ? { webrtc: { udp_port_range: portRange } } : {}) }));
    const nonce = randomUUID();
    const resources = new Map(await Promise.all(['broker.html', 'broker.js'].map(async name => [`/${nonce}/${name}`, { body: await readFile(join(extensionPath, name)), type: name.endsWith('.js') ? 'text/javascript' : 'text/html' }])));
    resources.set(`/${nonce}/media-policy.js`, { body: await readFile(new URL('./media-policy.js', import.meta.url)), type: 'text/javascript' });
    server = http.createServer((request, response) => {
      const resource = request.method === 'GET' && resources.get(request.url);
      if (!resource) { response.writeHead(404); response.end(); return; }
      response.writeHead(200, { 'Content-Type': resource.type, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Content-Security-Policy': "default-src 'none'; script-src 'self'; frame-ancestors 'none'" });
      response.end(resource.body);
    });
    await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
    const brokerURL = `http://127.0.0.1:${server.address().port}/${nonce}/broker.html`;
    owner = await chromium.launchPersistentContext(profile, {
      channel: 'chromium', headless: true, chromiumSandbox: sandbox, ...(env ? { env } : {}),
      args: ['--site-per-process', `--force-device-scale-factor=${deviceScaleFactor}`, `--disable-extensions-except=${extensionPath}`, `--load-extension=${extensionPath}`, `--allowlisted-extension-id=${extensionId}`, ...(fieldTrials ? [`--force-fieldtrials=${fieldTrials}`] : [])],
    });
    const browser = owner.browser();
    const captures = new Set();
    const reserveCapture = () => {
      if (captures.size >= maxCaptures) { const error = new Error('active browser video capacity reached'); error.status = 429; throw error; }
      const token = {}; captures.add(token);
      return () => captures.delete(token);
    };
    let closed = false;
    return {
      browser,
      // Every session context uses the same DPR (server.js). Chromium
      // compositor density is independent of per-context CDP DPR, so Agent
      // screenshots normalize by this launch value.
      deviceScaleFactor,
      captureDeviceScaleFactor: deviceScaleFactor,
      prepareContext: context => {
        if (closed || context === owner || context.browser() !== browser) return Promise.reject(new Error('media requires an isolated context of this browser'));
        return prepareMediaContext(context, extensionId, brokerURL, reserveCapture, network, deviceScaleFactor);
      },
      async close() {
        if (closed) return;
        closed = true;
        try { await owner.close(); } finally { await closeServer(); await rm(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 }); }
      },
    };
  } catch (error) {
    await owner?.close().catch(() => {});
    await closeServer().catch(() => {});
    await rm(profile, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 }).catch(() => {});
    throw error;
  }
}
