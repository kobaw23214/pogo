import test from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { launchMediaBrowser } from '../src/media.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, message, timeout = 10000) {
  const end = Date.now() + timeout;
  do { const value = await read(); if (value) return value; await delay(25); } while (Date.now() < end);
  assert.fail(message);
}
function firstVideoCodec(sdp) {
  const payload = /^m=video \S+ \S+ (\d+)/m.exec(sdp)?.[1];
  return new RegExp(`^a=rtpmap:${payload} ([^/]+)`, 'm').exec(sdp)?.[1];
}
async function fixture(t, { rejectPreferences = false, network } = {}) {
  const host = await launchMediaBrowser(network ? { network } : {}); t.after(() => host.close());
  const receiver = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true }); t.after(() => receiver.close());
  const context = await host.browser.newContext({ viewport: { width: 640, height: 480 }, deviceScaleFactor: 2 });
  await context.addInitScript(({ rejectPreferences }) => {
    if (!location.pathname.endsWith('/broker.html')) return;
    globalThis.codecPreferenceCalls = [];
    // Records only [start, min] bitrate hints per remote description the sender applies.
    globalThis.remoteBitrateHints = [];
    const setRemoteDescription = RTCPeerConnection.prototype.setRemoteDescription;
    RTCPeerConnection.prototype.setRemoteDescription = function (description, ...rest) {
      remoteBitrateHints.push([...String(description?.sdp ?? '').matchAll(/^a=fmtp:\d+ .*x-google-start-bitrate=(\d+);x-google-min-bitrate=(\d+)/gm)].map(match => [Number(match[1]), Number(match[2])]));
      return setRemoteDescription.call(this, description, ...rest);
    };
    const original = RTCRtpTransceiver.prototype.setCodecPreferences;
    RTCRtpTransceiver.prototype.setCodecPreferences = function (codecs) {
      codecPreferenceCalls.push(codecs.map(codec => codec.mimeType));
      if (rejectPreferences) throw new DOMException('Fixture rejects optional codec preference', 'NotSupportedError');
      return original.call(this, codecs);
    };
  }, { rejectPreferences });
  const broker = await host.prepareContext(context), source = await context.newPage();
  await source.goto('data:text/html,<style>body{margin:0;background:%230045aa}i{position:absolute;width:50px;height:50px;background:white}</style><h1>中文 Codec fixture</h1><i></i><script>let x=0;setInterval(()=>document.querySelector("i").style.left=(x++%500)+"px",33)</script>');
  return { host, receiver, broker, source, context };
}
async function connect(t, h, { id, codecPreference, receiverWithoutH264 = false }) {
  const context = await h.receiver.newContext(), page = await context.newPage();
  const outgoing = [], errors = [];
  let ready = false;
  const media = await h.broker.create(h.source, { id, width: 640, height: 480, scale: 1, ...(codecPreference === undefined ? {} : { codecPreference }),
    onCandidate(candidate) {
      if (!ready) outgoing.push(candidate);
      else page.evaluate(candidate => pc.addIceCandidate(candidate), candidate).catch(error => errors.push(error));
    },
  });
  t.after(async () => { await media.close(); await context.close(); });
  await page.exposeFunction('candidateToSource', candidate => media.candidate(candidate).catch(error => errors.push(error)));
  const answer = await page.evaluate(async ({ offer, receiverWithoutH264 }) => {
    document.body.innerHTML = '<video autoplay muted playsinline></video>';
    globalThis.pc = new RTCPeerConnection({ iceServers: [] });
    pc.onicecandidate = event => { if (event.candidate) candidateToSource(event.candidate.toJSON()); };
    pc.ontrack = event => { document.querySelector('video').srcObject = event.streams[0]; };
    await pc.setRemoteDescription(offer);
    if (receiverWithoutH264) {
      const codecs = RTCRtpReceiver.getCapabilities('video').codecs.filter(codec => codec.mimeType.toLowerCase() !== 'video/h264');
      for (const transceiver of pc.getTransceivers()) transceiver.setCodecPreferences(codecs);
    }
    await pc.setLocalDescription(await pc.createAnswer());
    return pc.localDescription.toJSON();
  }, { offer: media.offer, receiverWithoutH264 });
  await media.answer(answer);
  ready = true;
  for (const candidate of outgoing) await page.evaluate(candidate => pc.addIceCandidate(candidate), candidate);
  const inspect = () => page.evaluate(async () => {
    const rows = [...(await pc.getStats()).values()];
    const inbound = rows.find(row => row.type === 'inbound-rtp' && row.kind === 'video');
    const codec = rows.find(row => row.id === inbound?.codecId);
    const video = document.querySelector('video');
    return { codec: codec?.mimeType, frames: inbound?.framesDecoded ?? 0, width: video.videoWidth, height: video.videoHeight };
  });
  const first = await until(async () => { const stats = await inspect(); return stats.frames >= 2 && stats; }, 'native codec stream did not decode');
  assert.deepEqual(errors, []);
  return { media, page, inspect, first };
}

test('standard and responsive viewers independently negotiate codecs on the same native capture', { timeout: 45000 }, async t => {
  const h = await fixture(t);
  await assert.rejects(h.broker.create(h.source, { id: 'bad-preference', width: 640, height: 480, codecPreference: 'h264' }), /invalid media codec preference/);
  assert.equal(h.context.pages().length, 1, 'invalid preference cannot create internal pages or consume capture capacity');
  const standard = await connect(t, h, { id: 'standard' });
  assert.equal(firstVideoCodec(standard.media.offer.sdp), 'VP8');
  assert.equal(standard.first.codec, 'video/VP8');
  assert.deepEqual(await h.broker.page.evaluate(() => codecPreferenceCalls), [], 'omitted standard preference never calls codec tuning APIs');
  const responsive = await connect(t, h, { id: 'responsive', codecPreference: 'responsive' });
  assert.equal(firstVideoCodec(responsive.media.offer.sdp), 'H264');
  assert.equal(responsive.first.codec, 'video/H264');
  assert.deepEqual(responsive.media.pixels, standard.media.pixels);
  for (const viewer of [standard, responsive]) {
    assert.equal(viewer.first.width, 640); assert.equal(viewer.first.height, 480);
    assert.equal((await viewer.media.diagnostics()).content_hint, 'text');
  }
  assert.equal((await h.broker.page.evaluate(() => codecPreferenceCalls)).length, 1);
  assert.equal((await standard.inspect()).codec, 'video/VP8', 'a responsive peer cannot renegotiate the standard viewer');
  assert.deepEqual(await h.broker.page.evaluate(() => remoteBitrateHints), [[], []], 'the default network profile keeps the viewer answer unchanged');
  const before = (await standard.inspect()).frames;
  await responsive.media.close();
  await until(async () => (await standard.inspect()).frames > before, 'closing the responsive peer stopped the shared capture');
  assert.equal((await standard.inspect()).codec, 'video/VP8');
  assert.equal(h.source.isClosed(), false);
});

test('LAN network profile starts and floors each epoch sender bandwidth estimate', { timeout: 30000 }, async t => {
  const h = await fixture(t, { network: 'lan' });
  const viewer = await connect(t, h, { id: 'lan-bitrate-hints' });
  const [hints] = await h.broker.page.evaluate(() => remoteBitrateHints);
  assert.ok(hints.length >= 1 && hints.every(([start, min]) => start === 8000 && min === 4000), 'every primary video codec of the applied answer carries the deployment hints');
  const estimate = await until(async () => (await viewer.media.stats()).find(row => row.type === 'candidate-pair' && row.availableOutgoingBitrate > 0)?.availableOutgoingBitrate, 'no sender bandwidth estimate');
  assert.ok(estimate >= 4_000_000, `the estimate (${estimate} bps) starts at or above the LAN floor instead of 300 kbps`);
  assert.equal(viewer.first.codec, 'video/VP8');
  const responsive = await connect(t, h, { id: 'lan-responsive', codecPreference: 'responsive' });
  assert.equal(responsive.first.codec, 'video/H264');
  assert.deepEqual((await h.broker.page.evaluate(() => remoteBitrateHints))[1], [], 'responsive viewers keep their answer unchanged');
});

test('responsive sender retains native video fallback when the receiver does not support H264', { timeout: 30000 }, async t => {
  const h = await fixture(t);
  const viewer = await connect(t, h, { id: 'fallback-receiver', codecPreference: 'responsive', receiverWithoutH264: true });
  assert.equal(firstVideoCodec(viewer.media.offer.sdp), 'H264');
  assert.match(viewer.media.offer.sdp, /a=rtpmap:\d+ VP8\//);
  assert.equal(viewer.first.codec, 'video/VP8');
  assert.equal(viewer.first.width, 640); assert.equal(viewer.first.height, 480);
});

test('rejected sender codec preference falls back to normal native offer and decoded video', { timeout: 30000 }, async t => {
  const h = await fixture(t, { rejectPreferences: true });
  const viewer = await connect(t, h, { id: 'rejected-preference', codecPreference: 'responsive' });
  assert.equal(firstVideoCodec(viewer.media.offer.sdp), 'VP8');
  assert.equal(viewer.first.codec, 'video/VP8');
  assert.equal((await h.broker.page.evaluate(() => codecPreferenceCalls)).length, 1);
});
