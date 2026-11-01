import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { cp, mkdtemp, readFile, rm } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { patchPlaywright, originalBundleSHA256, sha256 } from '../scripts/patch-playwright-disposable.mjs';

const require = createRequire(import.meta.url);
const enabled = process.env.POGO_PLAYWRIGHT_RACE_REAL === '1';
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));

test('real pinned Playwright producer race and independent-profile containment', { skip: !enabled, timeout: 180000 }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'pogo-playwright-race-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const originalRoot = join(root, 'original/node_modules');
  const patchedRoot = join(root, 'patched/node_modules');
  for (const name of ['playwright', 'playwright-core']) {
    await cp(dirname(require.resolve(name + '/package.json')), join(originalRoot, name), { recursive: true });
  }
  // The unpatched control is meaningful only against the exact pinned upstream.
  assert.equal(sha256(await readFile(join(originalRoot, 'playwright-core/lib/coreBundle.js'))), originalBundleSHA256);
  await cp(originalRoot, patchedRoot, { recursive: true });
  await patchPlaywright(join(patchedRoot, 'playwright-core'));
  const { chromium } = await import('playwright');
  const executable = process.env.POGO_TEST_CHROMIUM || chromium.executablePath();
  const helper = fileURLToPath(new URL('./helpers/playwright-disposable-child.mjs', import.meta.url));

  async function chrome() {
    const directory = await mkdtemp(join(root, 'profile-'));
    const child = spawn(executable, ['--headless=new', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + directory, 'about:blank'], { stdio: 'ignore' });
    let spawnError;
    child.once('error', error => { spawnError = error; });
    const stop = async () => {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
      for (let i = 0; i < 60 && child.exitCode === null && child.signalCode === null; i++) await pause(25);
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    };
    try {
      for (let i = 0; i < 200; i++) {
        if (spawnError) throw spawnError;
        if (child.exitCode !== null || child.signalCode !== null) throw new Error('isolated Chromium exited during startup');
        try {
          const [port, path] = (await readFile(join(directory, 'DevToolsActivePort'), 'utf8')).trim().split('\n');
          if (/^\d+$/.test(port) && path.startsWith('/devtools/browser/')) return { endpoint: `ws://127.0.0.1:${port}${path}`, stop };
        } catch {}
        await pause(25);
      }
      throw new Error('isolated Chromium startup timed out');
    } catch (error) { await stop(); throw error; }
  }
  async function run({ patched = true, scenario = 'race', method = 'addInitScript', close = 'page', scope = 'page' } = {}) {
    const profiles = [];
    try {
      profiles.push(await chrome()); profiles.push(await chrome());
      const modulePath = join(patched ? patchedRoot : originalRoot, 'playwright/index.mjs');
      const child = spawn(process.execPath, [helper, modulePath, profiles[0].endpoint, profiles[1].endpoint, scenario, method, close, scope], { stdio: ['ignore', 'pipe', 'pipe'] });
      let stdout = '', stderr = '';
      child.stdout.on('data', data => { stdout += data; }); child.stderr.on('data', data => { stderr += data; });
      const timeout = setTimeout(() => child.kill('SIGKILL'), 15000);
      const exit = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', (code, signal) => resolve({ code, signal })); });
      clearTimeout(timeout);
      return { ...exit, stdout, stderr };
    } finally { await Promise.all(profiles.map(profile => profile.stop())); }
  }

  await t.test('unpatched real completed addInitScript after Page disposal crashes at missing-parent Disposable create', async () => {
    const result = await run({ patched: false });
    assert.notEqual(result.code, 0);
    assert.equal(result.signal, null);
    assert.match(result.stderr, /Cannot find parent object page@[^\s]+ to create disposable@/);
    assert.match(result.stderr, /Connection\._createRemoteObject/);
  });
  for (const method of ['addInitScript', 'exposeBinding']) {
    for (const [scope, close] of [['page', 'page'], ['page', 'context'], ['page', 'disconnect'], ['context', 'context']]) {
      await t.test(`patched ${scope}.${method} with ${close} closes only its own profile`, async () => {
        const result = await run({ method, scope, close });
        assert.equal(result.code, 0, result.stderr);
        const evidence = JSON.parse(result.stdout.trim());
        assert.equal(evidence.passed, true);
        assert.equal(evidence.terminalRejected, true);
        assert.equal(evidence.siblingTargetPreserved, true);
      });
    }
  }
  for (const scenario of ['unknown-protocol', 'unknown-exception']) {
    await t.test(`patched runtime keeps ${scenario} fatal`, async () => {
      const result = await run({ scenario });
      assert.notEqual(result.code, 0);
      assert.equal(result.signal, null);
      assert.match(result.stderr, scenario === 'unknown-protocol' ? /Cannot find parent object page@unknown-test-parent/ : /Pogo unknown application failure/);
    });
  }
});
