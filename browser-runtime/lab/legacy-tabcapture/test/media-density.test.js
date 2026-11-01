import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';
import { launchMediaBrowser, mediaCaptureSize } from '../src/media.js';
import { createRTCViewer } from './helpers/rtc-viewer.js';

// Fractional remote densities (DPR 1.5) next to the DPR 2 control: input
// coordinates, first-frame presentation sizes, Agent screenshots and element
// references must stay in CSS pixels whatever the compositor density is.
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, message = 'condition not observed', ms = 10000) {
  const end = Date.now() + ms;
  do { const result = await read(); if (result) return result; await delay(15); } while (Date.now() < end);
  assert.fail(message);
}
const pointer = (phase, x, y, buttons = 0) => ({ type: 'pointer', phase, x, y, button: phase === 'move' ? 'none' : 'left', buttons, modifiers: 0, click_count: phase === 'move' ? 0 : 1 });
function jpegSize(jpeg) {
  for (let offset = 2; offset < jpeg.length - 9;) {
    if (jpeg[offset] !== 255) { offset++; continue; }
    const marker = jpeg[offset + 1], length = jpeg.readUInt16BE(offset + 2);
    if ([192, 193, 194].includes(marker)) return { width: jpeg.readUInt16BE(offset + 7), height: jpeg.readUInt16BE(offset + 5) };
    offset += length + 2;
  }
  return null;
}

// The top document and two cross-site children (localhost vs 127.0.0.1, so
// out-of-process with --site-per-process) record trusted pointer coordinates.
async function fixture(t) {
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    if (request.url === '/child') {
      response.end('<!doctype html><style>html,body{margin:0;height:100%;background:#cde}</style><script>window.downs=[];addEventListener("pointerdown",e=>{if(e.isTrusted)downs.push({x:e.clientX,y:e.clientY})},true)</script>');
      return;
    }
    response.end(`<!doctype html><title>Density fixture</title><style>body{margin:0;height:5000px;background:#fff}iframe{position:absolute;border:0;width:400px;height:200px}#a{left:400px;top:300px}#b{left:200px;top:1600px}
      #target{position:absolute;left:640px;top:520px;width:121px;height:41px;border:0;padding:0;background:#222;color:#222;font:12px sans-serif}</style>
      <iframe id="a" src="http://localhost:${site.address().port}/child"></iframe><iframe id="b" src="http://localhost:${site.address().port}/child"></iframe>
      <button id="target" onclick="clicks.push(event.isTrusted)">Density target</button>
      <script>window.downs=[];window.clicks=[];addEventListener("pointerdown",e=>{if(e.isTrusted)downs.push({x:e.clientX,y:e.clientY,pageX:e.pageX,pageY:e.pageY})},true)</script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { site.closeAllConnections(); site.close(resolve); }));
  return `http://127.0.0.1:${site.address().port}`;
}

async function startWorker(t, deviceScaleFactor, token) {
  const host = await launchMediaBrowser({ deviceScaleFactor }); t.after(() => host.close());
  const worker = await createWorker({ token, browser: host.browser, mediaHost: host, actionTimeout: 2500, settleMs: 0 }); t.after(() => worker.close());
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/density-${randomUUID()}`;
  const request = async (suffix, method = 'GET', body) => {
    const response = await fetch(base + suffix, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    return { status: response.status, headers: response.headers, body: response.headers.get('content-type')?.includes('json') ? await response.json() : Buffer.from(await response.arrayBuffer()) };
  };
  const execute = async action => {
    const response = await request('/execute', 'POST', { op_id: randomUUID(), ...action });
    assert.equal(response.status, 200, JSON.stringify(response.body)); assert.equal(response.body.ok, true, JSON.stringify(response.body));
    return response.body;
  };
  return { host, base, request, execute };
}

for (const deviceScaleFactor of [1.5, 2]) test(`DPR-${deviceScaleFactor} live input lands on the sent CSS pixels at the edges, in cross-site frames and after scrolling; first frames keep even, acknowledged sizes`, { timeout: 60000 }, async t => {
  const origin = await fixture(t), token = 'density-live-fixture';
  const { host, base, request, execute } = await startWorker(t, deviceScaleFactor, token);
  const viewerBrowser = await chromium.launch({ channel: 'chromium', headless: true, chromiumSandbox: true }); t.after(() => viewerBrowser.close());
  assert.equal((await request('', 'PUT', {})).status, 200);
  await execute({ action: 'navigate', url: origin + '/' });
  const page = host.browser.contexts().flatMap(context => context.pages()).find(candidate => candidate.url() === origin + '/');
  assert.equal(await page.evaluate(() => devicePixelRatio), deviceScaleFactor);
  let state = (await request('/state')).body;
  const viewer = await createRTCViewer(viewerBrowser, base.replace('http:', 'ws:') + `/live?tab_id=${state.active_tab}&context_ref=${state.context_ref}`, token);
  t.after(() => viewer.close());
  const { messages, frames } = viewer;
  state = (await request('/control', 'POST', { mode: 'human', context_ref: state.context_ref, tab_id: state.viewed_tab ?? state.active_tab })).body;
  await until(() => frames.at(-1)?.generation === state.generation && frames.at(-1)?.control_epoch === state.control_epoch, 'no human-control frame');
  let seq = 0;
  const send = async (event, overrides = {}) => {
    const frame = frames.at(-1), packet = { type: 'input', seq: ++seq, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: frame.frame_id, media_epoch: frame.media_epoch, event, ...overrides };
    viewer.ws.send(JSON.stringify(packet));
    const reply = await until(() => messages.find(message => message.seq === packet.seq), 'input was not acknowledged');
    assert.equal(reply.type, 'ack', JSON.stringify(reply));
    return reply;
  };
  const presented = viewport => {
    // The viewer helper only records a frame whose decoded size equals the
    // bound offer; any mismatch is an error and the epoch is never acknowledged.
    assert.deepEqual(viewer.errors, []);
    const frame = frames.at(-1), expected = mediaCaptureSize(viewport.width, viewport.height, 2, deviceScaleFactor);
    assert.deepEqual(frame.viewport, viewport);
    assert.deepEqual({ width: frame.pixel_width, height: frame.pixel_height }, expected);
    assert.equal(expected.width % 2 + expected.height % 2, 0);
  };
  const frameOf = async selector => (await page.$(selector)).contentFrame();
  const downs = async frame => frame.evaluate(() => downs.splice(0));
  const click = async (x, y) => { await send(pointer('down', x, y, 1)); await send(pointer('up', x, y)); };
  presented({ width: 1280, height: 800 });

  for (const [x, y] of [[0, 0], [1279, 0], [0, 799], [1279, 799], [640, 0], [0, 400], [1279, 400], [640, 799], [333, 222]]) {
    await click(x, y);
    assert.deepEqual(await until(async () => { const value = await downs(page); return value.length && value; }), [{ x, y, pageX: x, pageY: y }], `top ${x},${y}`);
  }
  const first = await frameOf('#a'), second = await frameOf('#b');
  for (const [x, y] of [[400, 300], [799, 499], [601, 401]]) {
    await click(x, y);
    assert.deepEqual(await until(async () => { const value = await downs(first); return value.length && value; }), [{ x: x - 400, y: y - 300 }], `frame ${x},${y}`);
  }
  assert.deepEqual(await downs(page), [], 'frame clicks do not reach the parent document');

  await send({ type: 'wheel', x: 100, y: 100, delta_x: 0, delta_y: 1234, modifiers: 0 });
  await until(async () => await page.evaluate(() => scrollY) === 1234, 'wheel did not scroll by 1234 CSS px');
  await click(100, 100);
  assert.deepEqual(await until(async () => { const value = await downs(page); return value.length && value; }), [{ x: 100, y: 100, pageX: 100, pageY: 1334 }]);
  await click(300, 400);
  assert.deepEqual(await until(async () => { const value = await downs(second); return value.length && value; }), [{ x: 100, y: 34 }], 'a cross-site frame moved by scrolling');

  // An odd CSS viewport: fractional raster rows at DPR 1.5, even capture.
  const oldFrame = frames.at(-1), resize = ++seq;
  viewer.ws.send(JSON.stringify({ type: 'viewport', seq: resize, generation: state.generation, control_epoch: state.control_epoch, lease_token: state.lease_token, frame_id: oldFrame.frame_id, media_epoch: oldFrame.media_epoch, width: 1281, height: 721 }));
  assert.equal((await until(() => messages.find(message => message.seq === resize))).type, 'ack');
  await until(() => frames.at(-1).viewport.width === 1281, 'resize did not present a new epoch');
  presented({ width: 1281, height: 721 });
  for (const [x, y] of [[1280, 720], [0, 720], [1280, 0]]) {
    await click(x, y);
    assert.deepEqual(await until(async () => { const value = await downs(page); return value.length && value; }), [{ x, y, pageX: x, pageY: y + 1234 }], `resized ${x},${y}`);
  }
  assert.equal(await page.evaluate(() => scrollY), 1234);
});

for (const deviceScaleFactor of [1.5, 2]) test(`DPR-${deviceScaleFactor} Agent screenshots, element-reference frames and picks stay in CSS pixels for an odd viewport`, { timeout: 60000 }, async t => {
  const origin = await fixture(t), token = 'density-reference-fixture';
  const { host, request, execute } = await startWorker(t, deviceScaleFactor, token);
  assert.equal((await request('', 'PUT', {})).status, 200);
  await execute({ action: 'navigate', url: origin + '/' });
  const page = host.browser.contexts().flatMap(context => context.pages()).find(candidate => candidate.url() === origin + '/');
  await page.setViewportSize({ width: 1281, height: 721 });
  const viewport = { width: 1281, height: 721 }, box = { x: 640, y: 520, width: 121, height: 41 };
  // Locate the dark target in each JPEG (decoded by Chromium) to compare its pixel box with its CSS box.
  const scratch = await host.browser.newContext(); t.after(() => scratch.close());
  const decoder = await scratch.newPage();
  const darkBox = jpeg => decoder.evaluate(async data => {
    const image = new Image(); image.src = 'data:image/jpeg;base64,' + data; await image.decode();
    const canvas = document.createElement('canvas'); canvas.width = image.naturalWidth; canvas.height = image.naturalHeight;
    const ctx = canvas.getContext('2d'); ctx.drawImage(image, 0, 0);
    // Search a window around the target only (the iframes are light).
    const x0 = 560, y0 = 460, w = 280, h = 160, pixels = ctx.getImageData(x0, y0, w, h).data;
    let left = Infinity, top = Infinity, right = -1, bottom = -1;
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) if (pixels[(y * w + x) * 4] < 96) { left = Math.min(left, x); right = Math.max(right, x); top = Math.min(top, y); bottom = Math.max(bottom, y); }
    return { x: x0 + left, y: y0 + top, width: right - left + 1, height: bottom - top + 1 };
  }, jpeg.toString('base64'));
  const near = (actual, expected, label, tolerance = 1) => {
    for (const key of Object.keys(expected)) assert.ok(Math.abs(actual[key] - expected[key]) <= tolerance, `${label} ${key}: ${JSON.stringify(actual)} vs ${JSON.stringify(expected)}`);
  };

  const shot = await execute({ action: 'screenshot' });
  assert.deepEqual({ width: shot.screenshot.width, height: shot.screenshot.height }, viewport);
  const shotJPEG = Buffer.from(shot.screenshot.data, 'base64');
  assert.deepEqual(jpegSize(shotJPEG), viewport, 'Agent JPEG bytes stay CSS-sized at a fractional density');
  near(await darkBox(shotJPEG), box, 'Agent screenshot');
  assert.equal(typeof shot.version, 'string'); assert.ok(shot.version);

  const frame = await request('/frame');
  assert.equal(frame.status, 200);
  assert.deepEqual(jpegSize(frame.body), viewport, 'the element-reference frame is CSS-sized');
  near(await darkBox(frame.body), box, 'reference frame');
  const version = frame.headers.get('x-browser-version'), state = (await request('/state')).body;
  assert.ok(version);
  const picked = await request('/pick', 'POST', { x: 700, y: 540, version, tab_id: state.active_tab, context_ref: state.context_ref });
  assert.equal(picked.status, 200, JSON.stringify(picked.body));
  assert.equal(picked.body.element.tag, 'button');
  assert.deepEqual(picked.body.viewport, viewport);
  near(picked.body.element.rect, box, 'picked element rect', 0.01);

  // Agent element reference and screenshot coordinate both hit the same element.
  const snapshot = await execute({ action: 'snapshot' });
  const ref = /\[(e\d+)\] button "Density target"/.exec(snapshot.text)[1];
  await execute({ action: 'click', target: ref });
  const fresh = await execute({ action: 'screenshot' });
  await execute({ action: 'click', x: box.x + box.width - 1, y: box.y + box.height - 1, version: fresh.version });
  await execute({ action: 'click', x: box.x, y: box.y, version: (await execute({ action: 'screenshot' })).version });
  assert.deepEqual(await page.evaluate(() => clicks), [true, true, true], 'ref, far-corner and near-corner clicks all hit the target');
  await execute({ action: 'click', x: box.x - 1, y: box.y - 1, version: (await execute({ action: 'screenshot' })).version });
  assert.deepEqual(await page.evaluate(() => clicks), [true, true, true], 'one CSS pixel outside misses it');
});
