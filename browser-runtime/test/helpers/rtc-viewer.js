import assert from 'node:assert/strict';
import WebSocket from 'ws';
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, message = 'condition not observed', ms = 10000) { const end = Date.now() + ms; do { const result = await read(); if (result) return result; await delay(15); } while (Date.now() < end); assert.fail(message); }

// A separate real browser decodes media and confirms rVFC presentation. The
// worker remains the sole source Chromium; the viewer never visits its pages.
export async function createRTCViewer(browser, url, token, { scale = 2, autoAcknowledge = true } = {}) {
  const context = await browser.newContext(), page = await context.newPage();
  const messages = [], frames = [], errors = [];
  let ws;
  let queue = Promise.resolve(), epoch = null, answerSent = false;
  let outgoing = [], autoAck = autoAcknowledge;
  const send = value => { if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(value)); };
  const acknowledge = frame => send({ type: 'presentation_ack', media_epoch: frame.media_epoch, frame_id: frame.frame_id, width: frame.pixel_width, height: frame.pixel_height });
  await page.exposeFunction('mediaCandidate', value => {
    if (value.epoch !== epoch) return;
    const event = { type: 'media_candidate', media_epoch: epoch, candidate: value.candidate };
    if (answerSent) send(event); else outgoing.push(event);
  });
  await page.exposeFunction('mediaPresented', value => {
    if (value.frame.media_epoch !== epoch) return;
    if (value.width !== value.frame.pixel_width || value.height !== value.frame.pixel_height) { errors.push(new Error('decoded dimensions differ from bound media offer')); return; }
    frames.push(value.frame);
    if (autoAck) acknowledge(value.frame);
  });
  const handle = async value => {
    if (value.type === 'ready') { assert.equal(value.protocol, 2); assert.equal(value.transport, 'webrtc'); send({ type: 'media_config', scale }); }
    else if (value.type === 'media_reset') {
      epoch = null; answerSent = false; outgoing = [];
      await page.evaluate(() => { globalThis.pc?.close(); document.body.innerHTML = ''; });
    } else if (value.type === 'media_offer') {
      epoch = value.frame.media_epoch; answerSent = false; outgoing = [];
      const answer = await page.evaluate(async offer => {
        globalThis.pc?.close(); document.body.innerHTML = '<video autoplay muted playsinline></video>';
        const video = document.querySelector('video');
        globalThis.pc = new RTCPeerConnection({ iceServers: offer.ice_servers });
        const peer = pc;
        peer.onicecandidate = event => { if (event.candidate) mediaCandidate({ epoch: offer.frame.media_epoch, candidate: event.candidate.toJSON() }); };
        peer.ontrack = event => {
          video.srcObject = event.streams[0];
          video.requestVideoFrameCallback(() => mediaPresented({ frame: offer.frame, width: video.videoWidth, height: video.videoHeight }));
        };
        await peer.setRemoteDescription({ type: 'offer', sdp: offer.sdp });
        await peer.setLocalDescription(await peer.createAnswer());
        return peer.localDescription.sdp;
      }, value);
      send({ type: 'media_answer', media_epoch: epoch, sdp: answer });
      answerSent = true; for (const event of outgoing) send(event); outgoing = [];
    } else if (value.type === 'media_candidate' && value.media_epoch === epoch) await page.evaluate(candidate => pc.addIceCandidate(candidate), value.candidate);
  };
  ws = new WebSocket(url, { headers: { Authorization: `Bearer ${token}` } });
  ws.on('message', raw => {
    const value = JSON.parse(raw); messages.push(value);
    queue = queue.then(() => handle(value)).catch(error => { errors.push(error); });
  });
  ws.on('error', error => errors.push(error));
  await until(() => frames.length || errors.length || messages.some(message => message.type === 'error'), 'no decoded WebRTC frame');
  if (errors.length) throw errors[0];
  const firstError = messages.find(message => message.type === 'error');
  if (!frames.length && firstError) throw new Error(JSON.stringify(firstError));
  return { ws, page, context, messages, frames, errors, acknowledge,
    set autoAck(value) { autoAck = value; },
    decoded: () => page.evaluate(() => document.querySelector('video')?.getVideoPlaybackQuality().totalVideoFrames ?? 0),
    pixelAt: (x, y) => page.evaluate(({ x, y }) => {
      const video = document.querySelector('video'), canvas = document.createElement('canvas');
      if (!video?.videoWidth || !video.videoHeight) return null;
      canvas.width = video.videoWidth; canvas.height = video.videoHeight;
      const context = canvas.getContext('2d'); context.drawImage(video, 0, 0);
      return [...context.getImageData(x, y, 1, 1).data];
    }, { x, y }),
    async close() { ws.terminate(); await queue.catch(() => {}); await context.close(); },
  };
}

