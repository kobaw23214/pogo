import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';
import { MEDIA_ENCODER_THREAD_LIMIT, launchMediaBrowser, mediaBitrateHints, mediaCaptureSize, mediaFieldTrials, normalizeMediaIceServers, normalizeMediaPortRange } from '../src/media.js';
import { mediaConfiguration } from '../src/media-config.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

test('native video dimensions are bounded even pixels and never change CSS input coordinates', () => {
  assert.deepEqual(mediaCaptureSize(1712, 785, 2), { width: 3424, height: 1570 });
  assert.deepEqual(mediaCaptureSize(1280, 800), { width: 1280, height: 800 });
  const large = mediaCaptureSize(4096, 4096, 2);
  assert.ok(large.width <= 3840 && large.height <= 2160 && large.width * large.height <= 8_294_400);
  for (const args of [[0, 800], [1280, Infinity], [1280, 800, 0], [1280, 800, 3], [1280, 800, NaN], ['1280', 800]]) assert.throws(() => mediaCaptureSize(...args));
});

test('capture never exceeds the remote compositor density; the default density keeps today\'s sizes', () => {
  assert.deepEqual(mediaCaptureSize(1280, 1600, 2), mediaCaptureSize(1280, 1600, 2, 2));
  assert.deepEqual(mediaCaptureSize(1280, 1600, 2, 2), { width: 1728, height: 2160 });
  assert.deepEqual(mediaCaptureSize(1280, 1600, 2, 1), { width: 1280, height: 1600 }, 'a DPR-2 viewer of a DPR-1 worker gets CSS pixels, not an upsampled raster');
  assert.deepEqual(mediaCaptureSize(1280, 1600, 1, 1), { width: 1280, height: 1600 });
  assert.deepEqual(mediaCaptureSize(1280, 800, 2, 1.5), { width: 1920, height: 1200 });
  assert.deepEqual(mediaCaptureSize(1280, 1600, 2, 1.5), { width: 1728, height: 2160 }, 'the 2160-row limit is lower than 1.5x here');
  assert.deepEqual(mediaCaptureSize(1280, 800, 1, 1.5), { width: 1280, height: 800 }, 'never below CSS pixels');
  for (const density of [0, 0.5, 1.25, 3, '2', NaN, null]) assert.throws(() => mediaCaptureSize(1280, 800, 2, density), /device scale factor/);
});

test('a fractional density keeps even capture sizes for odd viewports within the shared limits', () => {
  assert.deepEqual(mediaCaptureSize(1281, 721, 2, 1.5), { width: 1920, height: 1080 });
  assert.deepEqual(mediaCaptureSize(1280, 1201, 2, 1.5), { width: 1920, height: 1800 });
  assert.deepEqual(mediaCaptureSize(1280, 1593, 2, 1.5), { width: 1734, height: 2160 });
  assert.deepEqual(mediaCaptureSize(2560, 1600, 2, 1.5), mediaCaptureSize(2560, 1600, 2, 2), 'the largest viewport hits the 2160-row limit at both densities');
  for (const [width, height] of [[1281, 721], [1279, 799], [1280, 1593], [1440, 957], [2559, 1599], [1281, 1601], [2560, 1600]]) for (const scale of [1, 1.5, 2]) {
    const size = mediaCaptureSize(width, height, scale, 1.5), label = `${width}x${height}@${scale}`;
    assert.equal(size.width % 2, 0, label); assert.equal(size.height % 2, 0, label);
    assert.ok(size.width <= width * 1.5 && size.height <= height * 1.5, `${label} never exceeds the raster`);
    assert.ok(size.width <= 3840 && size.height <= 2160 && size.width * size.height <= 8_294_400, label);
    assert.ok(size.width >= width - 1 && size.height >= height - 1, `${label} stays at CSS pixels or above (even rounding only)`);
  }
});

for (const deviceScaleFactor of [1, 1.5]) test(`a DPR-${deviceScaleFactor} worker rasterizes, captures and normalizes Agent screenshots at its configured density`, { timeout: 30000 }, async t => {
  await assert.rejects(launchMediaBrowser({ deviceScaleFactor: 3 }), /device scale factor/);
  const host = await launchMediaBrowser({ deviceScaleFactor }); t.after(() => host.close());
  assert.equal(host.deviceScaleFactor, deviceScaleFactor); assert.equal(host.captureDeviceScaleFactor, deviceScaleFactor);
  const context = await host.browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor });
  await context.addInitScript(() => {
    if (!location.pathname.endsWith('/broker.html')) return;
    globalThis.testNativeTracks = [];
    const originalClone = MediaStreamTrack.prototype.clone;
    MediaStreamTrack.prototype.clone = function (...args) { const track = originalClone.apply(this, args); testNativeTracks.push(track); return track; };
  });
  const broker = await host.prepareContext(context), source = await context.newPage();
  // Bars one device pixel wide (1/DPR CSS px): only a raster at this density
  // can carry them; an upsampled lower-density raster turns them grey.
  const bar = 1 / deviceScaleFactor;
  await source.goto(`data:text/html,<style>body{margin:0;background:%23fff}i{position:absolute;top:600px;left:0;width:20px;height:20px;background:%23000}</style><h1>Density fixture</h1><div style="position:absolute;left:0;top:300px;width:512px;height:20px;background:repeating-linear-gradient(to right,%23000 0px,%23000 ${bar}px,%23fff ${bar}px,%23fff ${2 * bar}px)"></div><i></i><script>let x=0;setInterval(()=>document.querySelector('i').style.left=(x++%25500)+'px',33)</script>`);
  assert.deepEqual(await source.evaluate(() => ({ dpr: devicePixelRatio, width: innerWidth, height: innerHeight })), { dpr: deviceScaleFactor, width: 1280, height: 800 });
  const raster = await source.screenshot({ type: 'png', scale: 'device' });
  assert.equal(raster.readUInt32BE(16), 1280 * deviceScaleFactor, 'the compositor surface follows the configured density');
  const session = await new BrowserSession(context, { mediaBroker: broker, deviceScaleFactor, captureDeviceScaleFactor: host.captureDeviceScaleFactor, internalPages: broker.internalPages, settleMs: 0 }).init();
  const shot = await session.execute({ op_id: `dpr${deviceScaleFactor}-model-screenshot`, action: 'screenshot' });
  assert.equal(shot.ok, true);
  assert.equal(shot.screenshot.width, 1280); assert.equal(shot.screenshot.height, 800);
  const jpeg = Buffer.from(shot.screenshot.data, 'base64');
  let dimensions;
  for (let offset = 2; offset < jpeg.length - 9;) {
    if (jpeg[offset] !== 255) { offset++; continue; }
    const marker = jpeg[offset + 1], length = jpeg.readUInt16BE(offset + 2);
    if ([192, 193, 194].includes(marker)) { dimensions = { height: jpeg.readUInt16BE(offset + 5), width: jpeg.readUInt16BE(offset + 7) }; break; }
    offset += length + 2;
  }
  assert.deepEqual(dimensions, { width: 1280, height: 800 }, 'Agent JPEG bytes stay CSS-sized at every density');
  const media = await broker.create(source, { id: `dpr${String(deviceScaleFactor).replace('.', '-')}`, width: 1280, height: 800, scale: 2 });
  assert.deepEqual(media.pixels, { width: 1280 * deviceScaleFactor, height: 800 * deviceScaleFactor }, 'a 2x viewer request is capped at the raster density');
  assert.equal(media.settings.width, 1280 * deviceScaleFactor);
  const nativeDetail = await broker.page.evaluate(async row => {
    const image = await new ImageCapture(testNativeTracks[0]).grabFrame();
    const canvas = document.createElement('canvas'); canvas.width = image.width; canvas.height = image.height;
    const ctx = canvas.getContext('2d'); ctx.drawImage(image, 0, 0); image.close();
    const values = [...ctx.getImageData(0, row, 128, 1).data].filter((_, index) => index % 4 === 0);
    return values.slice(1).reduce((sum, value, index) => sum + Math.abs(value - values[index]), 0) / (values.length - 1);
  }, Math.round(310 * deviceScaleFactor));
  assert.ok(nativeDetail > 240, `native capture keeps one-device-pixel bars at DPR ${deviceScaleFactor} (mean step ${nativeDetail})`);
  await media.close(); await broker.close();
});

test('media connection configuration rejects arbitrary URLs, credentials, and invalid port bounds', () => {
  assert.deepEqual(normalizeMediaIceServers([{ urls: 'turn:relay.test:3478?transport=udp', username: 'short-lived', credential: 'secret' }]), [{ urls: ['turn:relay.test:3478?transport=udp'], username: 'short-lived', credential: 'secret' }]);
  for (const value of [{}, [{ urls: 'https://relay.test' }], [{ urls: 'turn:relay.test\nX: forged' }], [{ urls: ['stun:ok'], password: 'unexpected' }], [{ urls: [] }]]) assert.throws(() => normalizeMediaIceServers(value));
  assert.equal(normalizeMediaPortRange('19820-19839'), '19820-19839');
  for (const value of ['1-2', '2000-1000', '65000-65536', '19820', '19820-19839\n']) assert.throws(() => normalizeMediaPortRange(value));
});

test('field trials are one switch value; network-sensitive ones only for a LAN media path', () => {
  const pacer = 'WebRTC-ProbingScreenshareBwe/2.5,250,80,40,-60,3/', playout = 'WebRTC-ForceSendPlayoutDelay/min_ms:0,max_ms:0/', threads = 'WebRTC-VideoEncoderSettings/encoder_thread_limit:2/';
  assert.equal(mediaFieldTrials(), threads, 'the VP8 encoder thread limit is CPU-only and applies on every network');
  assert.equal(mediaFieldTrials('wan'), threads);
  assert.equal(mediaFieldTrials('lan'), pacer + playout + threads, 'zero playout delay follows the LAN profile when not set');
  assert.equal(mediaFieldTrials('lan', 'zero'), pacer + playout + threads);
  assert.equal(mediaFieldTrials('lan', 'default'), pacer + threads, 'LAN sender tuning without zero playout delay');
  assert.equal(mediaFieldTrials('wan', 'zero'), playout + threads, 'zero playout delay without network-sensitive sender tuning');
  assert.equal(mediaFieldTrials('wan', 'default'), threads);
  for (const network of ['lan', 'wan']) for (const playoutDelay of ['zero', 'default']) {
    const value = mediaFieldTrials(network, playoutDelay);
    // Chromium's syntax: Name/Group/ pairs concatenated, each terminated by '/'.
    assert.match(value, /^(?:[A-Za-z0-9-]+\/[^/\s]+\/)+$/);
    const names = value.match(/[A-Za-z0-9-]+\/[^/\s]+\//g).map(pair => pair.split('/')[0]);
    assert.equal(new Set(names).size, names.length, 'each trial appears once');
  }
  for (const value of ['auto', 'LAN', '', null]) assert.throws(() => mediaFieldTrials(value), /network profile/);
  for (const value of ['auto', 'ZERO', '', null, 0]) assert.throws(() => mediaFieldTrials('lan', value), /playout delay/, String(value));
  assert.equal(mediaBitrateHints(), null);
  assert.equal(mediaBitrateHints('wan'), null);
  assert.equal(mediaBitrateHints('wan', 'standard'), null);
  assert.deepEqual(mediaBitrateHints('lan'), { start_kbps: 8000, min_kbps: 4000 });
  assert.deepEqual(mediaBitrateHints('lan', 'standard'), { start_kbps: 8000, min_kbps: 4000 });
  assert.equal(mediaBitrateHints('lan', 'responsive'), null, 'responsive viewers keep the libwebrtc start bitrate');
  for (const value of ['auto', 'LAN', '', null]) assert.throws(() => mediaBitrateHints(value), /network profile/);
});

test('the VP8 encoder thread cap is fixed at 2 on every profile and does not follow POGO_BROWSER_CPUS', () => {
  assert.equal(MEDIA_ENCODER_THREAD_LIMIT, 2);
  for (const network of ['lan', 'wan']) for (const playoutDelay of [undefined, 'zero', 'default']) {
    assert.ok(mediaFieldTrials(network, playoutDelay).endsWith('WebRTC-VideoEncoderSettings/encoder_thread_limit:2/'), `${network}/${playoutDelay}`);
  }
  // The CPU quota is a Compose resource limit, not a media setting: the
  // resolved configuration (and so the field trials) ignores it.
  for (const environment of [{}, { POGO_BROWSER_WEBRTC_ADVERTISE_IP: '127.0.0.1' }]) for (const cpus of ['1', '2', '4', '8']) {
    assert.deepEqual(mediaConfiguration({ ...environment, POGO_BROWSER_CPUS: cpus }), mediaConfiguration(environment), cpus);
  }
});

test('ARM-compatible native tab capture sends real video, preserves page identity and isolates contexts', { timeout: 45000 }, async t => {
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    const blue = request.url === '/blue';
    response.end(`<title>${blue ? 'Blue' : 'Red'} native fixture</title><style>body{margin:0;background:${blue ? '#0044ff' : '#ff2200'};color:white;font:24px sans-serif}i{position:absolute;top:200px;left:100px;width:50px;height:50px;background:white}</style><div style="position:absolute;left:0;top:300px;width:512px;height:20px;background:repeating-linear-gradient(to right,#000 0px,#000 .5px,#fff .5px,#fff 1px)"></div><h1>原生浏览器中文</h1><input id="text" value="中文输入"><i></i><script>let x=100;setInterval(()=>document.querySelector('i').style.left=(x++%500)+'px',33)</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => site.close(resolve)));
  const origin = `http://127.0.0.1:${site.address().port}`;
  const host = await launchMediaBrowser(); t.after(() => host.close());
  assert.equal(host.captureDeviceScaleFactor, 2);
  const context = await host.browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2 });
  await context.addInitScript(() => {
    if (!location.pathname.endsWith('/broker.html')) return;
    globalThis.testNativeTracks = [];
    const originalClone = MediaStreamTrack.prototype.clone;
    MediaStreamTrack.prototype.clone = function (...args) { const track = originalClone.apply(this, args); testNativeTracks.push(track); return track; };
  });
  const broker = await host.prepareContext(context);
  assert.equal(context.pages().length, 0, 'Agent-only sessions do not allocate media pages');
  const source = await context.newPage(); await source.goto(origin + '/red');
  assert.deepEqual(await source.evaluate(() => ({ dpr: devicePixelRatio, width: innerWidth, height: innerHeight })), { dpr: 2, width: 1280, height: 800 });
  const sourceRaster = await source.screenshot({ type: 'png', scale: 'device' });
  assert.equal(sourceRaster.readUInt32BE(16), 2560, 'the source itself rasterizes at 2x; video is not upscaling a 1x screenshot');
  assert.equal(sourceRaster.readUInt32BE(20), 1600);
  const session = await new BrowserSession(context, { mediaBroker: broker, deviceScaleFactor: 2, captureDeviceScaleFactor: host.captureDeviceScaleFactor, internalPages: broker.internalPages, settleMs: 0 }).init();
  const shot = await session.execute({ op_id: 'dpr2-model-screenshot', action: 'screenshot' });
  assert.equal(shot.ok, true);
  assert.equal(shot.screenshot.width, 1280); assert.equal(shot.screenshot.height, 800);
  const jpeg = Buffer.from(shot.screenshot.data, 'base64');
  let dimensions;
  for (let offset = 2; offset < jpeg.length - 9;) {
    if (jpeg[offset] !== 255) { offset++; continue; }
    const marker = jpeg[offset + 1], length = jpeg.readUInt16BE(offset + 2);
    if ([192, 193, 194].includes(marker)) { dimensions = { height: jpeg.readUInt16BE(offset + 5), width: jpeg.readUInt16BE(offset + 7) }; break; }
    offset += length + 2;
  }
  assert.deepEqual(dimensions, { width: 1280, height: 800 }, 'Agent JPEG bytes remain CSS-sized even though the human video source is DPR2');
  const other = await host.browser.newContext();
  const otherPage = await other.newPage(); await otherPage.goto(origin + '/red');
  await assert.rejects(broker.create(otherPage, { id: 'foreign', width: 1280, height: 800 }), /does not belong/);
  await assert.rejects(broker.create(broker.page, { id: 'internal', width: 1280, height: 800 }), /does not belong/);

  const receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true }); t.after(() => receiver.close());
  const receiverContext = await receiver.newContext(), receiverPage = await receiverContext.newPage();
  await receiverPage.goto(origin + '/receiver');
  const outgoing = [], incoming = [], states = [];
  let media;
  const create = id => broker.create(source, { id, width: 1280, height: 800, scale: 2, onCandidate: candidate => outgoing.push(candidate), onState: state => states.push(state) });
  const originalNewPage = context.newPage.bind(context); let concurrentPopup;
  context.newPage = async (...args) => {
    const internal = await originalNewPage(...args);
    if (!concurrentPopup) {
      const popup = source.waitForEvent('popup');
      await source.evaluate(() => { window.open('/popup', '_blank'); });
      concurrentPopup = await popup;
    }
    return internal;
  };
  media = await create('first');
  context.newPage = originalNewPage;
  assert.ok([...session.pages.values()].some(meta => meta.page === concurrentPopup), 'a real popup created during internal page setup is attached');
  assert.equal(session.pages.size, 2, 'only source and real popup are visible; neither media page is attached');
  assert.deepEqual(media.pixels, { width: 2560, height: 1600 });
  const nativeDetail = await broker.page.evaluate(async () => {
    const image = await new ImageCapture(testNativeTracks[0]).grabFrame();
    const canvas = document.createElement('canvas'); canvas.width = image.width; canvas.height = image.height;
    const ctx = canvas.getContext('2d'); ctx.drawImage(image, 0, 0); image.close();
    const pixels = ctx.getImageData(0, 610, 128, 1).data;
    const row = [...pixels].filter((_, index) => index % 4 === 0);
    return row.slice(1).reduce((sum, value, index) => sum + Math.abs(value - row[index]), 0) / (row.length - 1);
  });
  assert.ok(nativeDetail > 240, 'native capture must preserve alternating physical pixels, not upscale a DPR1 raster');
  assert.equal(media.settings.width, 2560); assert.equal(media.settings.height, 1600);
  const shared = await broker.create(source, { id: 'shared-viewer', width: 1280, height: 800, scale: 1 });
  assert.deepEqual(shared.pixels, media.pixels, 'another viewer uses actual shared native pixels without resizing the first');
  assert.equal((await shared.diagnostics()).max_bitrate_bps, 12000000, 'quality budget follows actual shared pixels');
  await media.setProfile('interactive');
  assert.equal((await media.diagnostics()).capture_min_fps, 15);
  await shared.setProfile('reading');
  assert.equal((await shared.diagnostics()).capture_min_fps, 15, 'a reading peer cannot slow an interacting shared source');
  assert.equal((await media.diagnostics()).content_hint, 'text', 'interaction retains screen-content encoding');
  await media.setProfile('reading');
  assert.equal((await shared.diagnostics()).capture_min_fps, 1);
  await shared.close();
  assert.throws(() => media.answer({ type: 'offer', sdp: 'v=0' }), /invalid media SDP/);
  assert.throws(() => media.candidate({ candidate: 'bad\nvalue' }), /invalid media ICE/);
  await receiverPage.exposeFunction('receiverCandidate', candidate => incoming.push(candidate));
  const answer = await receiverPage.evaluate(async offer => {
    document.body.innerHTML = '<video autoplay muted playsinline></video>';
    globalThis.pc = new RTCPeerConnection({ iceServers: [] });
    pc.ontrack = event => { document.querySelector('video').srcObject = event.streams[0]; };
    pc.onicecandidate = event => { if (event.candidate) receiverCandidate(event.candidate.toJSON()); };
    await pc.setRemoteDescription(offer);
    await pc.setLocalDescription(await pc.createAnswer());
    return pc.localDescription.toJSON();
  }, media.offer);
  await delay(30);
  while (incoming.length) await media.candidate(incoming.shift());
  await media.candidate(null); // ICE completion may also arrive before the answer.
  await media.answer(answer);
  const deadline = Date.now() + 12000;
  while (Date.now() < deadline) {
    while (outgoing.length) await receiverPage.evaluate(candidate => pc.addIceCandidate(candidate), outgoing.shift());
    while (incoming.length) await media.candidate(incoming.shift());
    if (await receiverPage.evaluate(() => document.querySelector('video').videoWidth > 0)) break;
    await delay(20);
  }
  const sample = () => receiverPage.evaluate(() => {
    const video = document.querySelector('video'), canvas = document.createElement('canvas');
    canvas.width = canvas.height = 1;
    canvas.getContext('2d').drawImage(video, 0, 0, 1, 1);
    return { width: video.videoWidth, height: video.videoHeight, pixel: [...canvas.getContext('2d').getImageData(0, 0, 1, 1).data] };
  });
  const red = await sample();
  assert.equal(red.width, 2560); assert.equal(red.height, 1600); assert.ok(red.pixel[0] > 180 && red.pixel[2] < 100);
  assert.ok(states.includes('connected'));
  await source.goto(origin + '/blue');
  const blueDeadline = Date.now() + 5000;
  while ((await sample()).pixel[2] < 180 && Date.now() < blueDeadline) await delay(20);
  const blue = await sample(); assert.ok(blue.pixel[2] > 180 && blue.pixel[0] < 100);
  const stats = await media.stats();
  assert.ok(stats.some(item => item.type === 'outbound-rtp' && item.framesEncoded > 0 && item.frameWidth === 2560 && item.frameHeight === 1600));
  assert.ok(stats.some(item => item.type === 'codec' && /^video\//.test(item.mimeType)));
  assert.doesNotMatch(JSON.stringify(stats), /localCandidateId|remoteCandidateId|transportId|sdpFmtpLine/);
  let reading = await media.diagnostics();
  const diagnosticDeadline = Date.now() + 2500;
  while (reading.sample_age_ms === null && Date.now() < diagnosticDeadline) { await delay(30); reading = await media.diagnostics(); }
  assert.equal(reading.profile, 'reading');
  assert.equal(typeof reading.sample_age_ms, 'number', 'the live sender sampler actually runs');
  assert.match(reading.codec, /^video\//);
  assert.equal(reading.width, 2560); assert.equal(reading.height, 1600);
  assert.equal(reading.tuning_supported, true);
  assert.equal(reading.capture_tuning_supported, true);
  const interactive = await media.setProfile('interactive');
  assert.equal(interactive.profile, 'interactive');
  assert.equal(interactive.width, reading.width); assert.equal(interactive.height, reading.height);
  assert.equal(interactive.capture_min_fps, 15);
  assert.ok(interactive.max_framerate <= 60); assert.ok(interactive.max_bitrate_bps <= 12000000);
  assert.equal((await media.setProfile('reading')).profile, 'reading');
  const unchanged = await sample();
  assert.equal(unchanged.width, reading.width); assert.equal(unchanged.height, reading.height);
  await assert.rejects(media.setProfile('unknown'), /invalid media profile/);
  // A new epoch gets a fresh track and PeerConnection; stale handles cannot
  // operate or continue a stream after it has been replaced.
  await media.close();
  await assert.rejects(media.stats(), /closed/);
  await assert.rejects(media.setProfile('interactive'), /closed/);
  await assert.rejects(media.diagnostics(), /closed/);
  const next = await create('second');
  assert.ok(next.offer.sdp.startsWith('v=0'));
  await source.close();
  await assert.rejects(next.stats(), /closed/);
  await broker.close();
  await assert.rejects(broker.warm(), /closed/);
  await assert.rejects(create('closed-broker'), /closed/);
  assert.ok([...broker.internalPages].every(page => page.isClosed()));
});


test('native encoders have a shared capacity limit and closing a stream releases its slot', { timeout: 30000 }, async t => {
  const host = await launchMediaBrowser({ maxCaptures: 2 }); t.after(() => host.close());
  const first = await host.browser.newContext({ viewport: { width: 640, height: 480 } });
  const second = await host.browser.newContext({ viewport: { width: 640, height: 480 } });
  const a = await host.prepareContext(first), b = await host.prepareContext(second);
  await Promise.all([a.warm(), a.warm(), b.warm()]);
  assert.equal(a.internalPages.size, 2, 'concurrent prewarming creates one private broker pair');
  assert.equal(b.internalPages.size, 2);
  const p = await first.newPage(), q = await second.newPage(), r = await second.newPage();
  for (const page of [p, q, r]) await page.goto('data:text/html,<h1>Capacity fixture</h1>');
  const one = await a.create(p, { id: 'one', width: 640, height: 480 });
  const two = await b.create(q, { id: 'two', width: 640, height: 480 });
  await assert.rejects(b.create(r, { id: 'over-limit', width: 640, height: 480 }), error => error.status === 429);
  await one.close();
  const replacement = await b.create(r, { id: 'released-slot', width: 640, height: 480 });
  await replacement.close(); await two.close();
});

test('unsupported sender tuning keeps native negotiation available', { timeout: 30000 }, async t => {
  const host = await launchMediaBrowser(); t.after(() => host.close());
  const context = await host.browser.newContext({ viewport: { width: 640, height: 480 } });
  await context.addInitScript(() => {
    if (!location.pathname.endsWith('/broker.html')) return;
    RTCRtpSender.prototype.setParameters = async () => { throw new DOMException('Fixture rejects optional tuning', 'NotSupportedError'); };
  });
  const broker = await host.prepareContext(context), source = await context.newPage();
  await source.goto('data:text/html,<h1>Optional tuning fixture</h1>');
  const media = await broker.create(source, { id: 'unsupported-tuning', width: 640, height: 480 });
  assert.ok(media.offer.sdp.startsWith('v=0'));
  assert.equal((await media.diagnostics()).tuning_supported, false);
  assert.equal((await media.setProfile('interactive')).profile, 'interactive');
  await media.close(); await broker.close();
});
