import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { createWorker } from '../src/server.js';
import { launchMediaBrowser, mediaFieldTrials } from '../src/media.js';

const PACER = 'WebRTC-ProbingScreenshareBwe/2.5,250,80,40,-60,3/', PLAYOUT = 'WebRTC-ForceSendPlayoutDelay/min_ms:0,max_ms:0/', THREADS = 'WebRTC-VideoEncoderSettings/encoder_thread_limit:2/';

// The switches the real browser process received. CDP Browser.getBrowserCommandLine
// needs --enable-automation, which the product launch does not pass; chrome://version
// shows the same command line in a separate, short-lived context.
async function launchedSwitches(host, ...names) {
  const context = await host.browser.newContext();
  try {
    const page = await context.newPage();
    await page.goto('chrome://version');
    const line = await page.locator('#command_line').textContent();
    assert.match(line, /--user-data-dir=/, 'chrome://version shows the browser process command line');
    return Object.fromEntries(names.map(name => [name, [...line.matchAll(new RegExp(`(?:^|\\s)${name}=(\\S*)`, 'g'))].map(match => match[1])]));
  } finally { await context.close(); }
}

test('the media browser receives exactly one --force-fieldtrials matching each network and playout profile', { timeout: 60000 }, async () => {
  for (const [options, trials] of [
    [{ network: 'lan' }, PACER + PLAYOUT + THREADS],
    [{ network: 'lan', playoutDelay: 'default', deviceScaleFactor: 1 }, PACER + THREADS],
    [{ network: 'wan', deviceScaleFactor: 1.5 }, THREADS],
    [{ network: 'wan', playoutDelay: 'zero' }, PLAYOUT + THREADS],
  ]) {
    const host = await launchMediaBrowser(options);
    try {
      assert.deepEqual(await launchedSwitches(host, '--force-fieldtrials', '--force-device-scale-factor'), {
        '--force-fieldtrials': [trials],
        '--force-device-scale-factor': [String(options.deviceScaleFactor ?? 2)],
      }, JSON.stringify(options));
    } finally { await host.close(); }
  }
  // The launcher takes a resolved value and fails before starting Chromium.
  for (const playoutDelay of ['auto', 'ZERO', '']) await assert.rejects(launchMediaBrowser({ network: 'lan', playoutDelay }), /playout delay/);
});

test('the worker passes its network profile, playout delay and density to the launch and the session DPR', { timeout: 60000 }, async t => {
  const token = 'media-launch-local-fixture';
  const site = http.createServer((request, response) => { response.setHeader('Content-Type', 'text/html'); response.end('<title>Density</title><p>Launch fixture</p>'); });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { site.closeAllConnections(); site.close(resolve); }));
  await assert.rejects(createWorker({ token, launchMedia: 'launchMediaBrowser' }), /launchMedia must be a function/);
  // Two deployments that differ in every value, so a dropped or hard-coded
  // option cannot satisfy both (the playout delay is not the network default).
  for (const media of [{ network: 'lan', playoutDelay: 'default', deviceScaleFactor: 1.5 }, { network: 'wan', playoutDelay: 'zero', deviceScaleFactor: 2 }]) {
    const launches = [];
    const launchMedia = async options => { const host = await launchMediaBrowser(options); launches.push({ options, host }); return host; };
    const worker = await createWorker({ token, settleMs: 0, idleMs: 60000, launchMedia, mediaConfig: { advertisedIP: '', udpPortRange: '29900-29919', iceServers: [], ...media } });
    try {
      assert.equal(launches.length, 1);
      const [{ options, host }] = launches;
      const { network, playoutDelay, deviceScaleFactor, udpPortRange } = options;
      assert.deepEqual({ network, playoutDelay, deviceScaleFactor, udpPortRange }, { ...media, udpPortRange: '29900-29919' });
      assert.deepEqual(await launchedSwitches(host, '--force-fieldtrials', '--force-device-scale-factor'), {
        '--force-fieldtrials': [mediaFieldTrials(media.network, media.playoutDelay)],
        '--force-device-scale-factor': [String(media.deviceScaleFactor)],
      });
      const sessionURL = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/media-launch-${randomUUID()}`;
      const request = async (suffix, method, body) => {
        const response = await fetch(sessionURL + suffix, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
        return { status: response.status, body: await response.json() };
      };
      const execute = async args => {
        const response = await request('/execute', 'POST', { ...args, op_id: randomUUID() });
        assert.equal(response.status, 200, JSON.stringify(response.body));
        assert.equal(response.body.ok, true, JSON.stringify(response.body));
        return response.body;
      };
      assert.equal((await request('', 'PUT', {})).status, 200);
      await execute({ action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` });
      const evaluated = await execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'devicePixelRatio', returnByValue: true } });
      assert.equal(evaluated.data.result.result.value, media.deviceScaleFactor, 'the session DPR equals the compositor density');
    } finally { await worker.close(); }
  }
});
