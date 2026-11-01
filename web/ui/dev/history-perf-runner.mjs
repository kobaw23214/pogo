import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { build } from 'vite';
import vue from '@vitejs/plugin-vue';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

// prepare = production build + bounded correctness checks. measure = explicit
// formal repeated sampling, never run as a side effect of prepare.
const mode = process.argv[2] ?? 'prepare';
if (!['prepare', 'measure'].includes(mode)) throw new Error('Usage: node dev/history-perf-runner.mjs prepare|measure [output-root]');
const here = path.dirname(fileURLToPath(import.meta.url));
const ui = path.resolve(here, '..');
const repo = path.resolve(here, '../../../..');
const output = path.resolve(process.argv[3] ?? path.join(repo, '.localagent/checks/pogo-performance-baseline-20261008/frontend'));
const allowedOutput = path.join(repo, '.localagent/checks/pogo-performance-baseline-20261008/frontend');
if (output !== allowedOutput && !output.startsWith(allowedOutput + path.sep)) throw new Error('Output must stay inside this isolated frontend check directory');
await fs.mkdir(output, { recursive: true });
const built = path.join(output, 'build');
const stamp = new Date().toISOString().replaceAll(':', '-').replaceAll('.', '-');
const runDir = path.join(output, `${mode}-${stamp}`);
await fs.mkdir(runDir, { recursive: true });
async function filesAt(dir) {
  const result = [];
  for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) result.push(...await filesAt(full));
    else if (entry.isFile()) result.push(full);
  }
  return result.sort();
}
async function fingerprint(files, relativeTo) {
  const hash = createHash('sha256');
  for (const file of [...files].sort()) { hash.update(path.relative(relativeTo, file)); hash.update('\0'); hash.update(await fs.readFile(file)); hash.update('\0'); }
  return hash.digest('hex');
}
const sourceFiles = [...await filesAt(path.join(ui, 'src')), ...await filesAt(here).then(files => files.filter(file => path.basename(file).startsWith('history-perf'))), path.join(ui, 'package.json')];
const sourceHash = await fingerprint(sourceFiles, ui);
if (mode === 'prepare') {
  await build({ configFile: false, root: ui, logLevel: 'error', plugins: [vue()], resolve: { alias: { '@': path.join(ui, 'src') } }, build: { outDir: built, emptyOutDir: true, target: 'es2020', rollupOptions: { input: path.join(here, 'history-perf.html') } } });
} else {
  const prepared = JSON.parse(await fs.readFile(path.join(output, 'prepared.json'), 'utf8'));
  assert.equal(prepared.passed, true, 'prepare must pass first');
  assert.equal(prepared.sourceHash, sourceHash, 'source changed after preparation; prepare again');
  assert.equal(prepared.buildHash, await fingerprint(await filesAt(built), built), 'built assets changed after preparation');
}
const buildHash = await fingerprint(await filesAt(built), built);
const mime = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.json': 'application/json' };
const server = http.createServer(async (req, res) => {
  try {
    const pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname);
    const file = path.resolve(built, `.${pathname === '/' ? '/dev/history-perf.html' : pathname}`);
    if (!file.startsWith(built + path.sep)) { res.writeHead(403); res.end(); return; }
    const bytes = await fs.readFile(file);
    res.writeHead(200, { 'Content-Type': mime[path.extname(file)] ?? 'application/octet-stream', 'Cache-Control': 'no-store' }); res.end(bytes);
  } catch { res.writeHead(404); res.end('Not found'); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const url = `http://127.0.0.1:${server.address().port}/dev/history-perf.html`;
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, reducedMotion: 'reduce' });
const page = await context.newPage();
const pageErrors = [], blockedRequests = [], consoleErrors = [];
page.on('pageerror', error => pageErrors.push(error.message));
page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()); });
await page.route('**/*', route => {
  const parsed = new URL(route.request().url());
  if (parsed.origin !== new URL(url).origin) { blockedRequests.push(parsed.origin); return route.abort(); }
  return route.continue();
});
const environment = { node: process.version, platform: process.platform, arch: process.arch, osRelease: os.release(), cpu: os.cpus()[0]?.model, logicalCPUs: os.cpus().length, chromium: browser.version(), headless: true, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, productionBuild: true };
const write = async (name, data) => fs.writeFile(path.join(runDir, name), JSON.stringify(data, null, 2) + '\n');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
function stats(values) {
  const sorted = values.filter(Number.isFinite).sort((a, b) => a - b);
  if (!sorted.length) return { n: 0 };
  return { n: sorted.length, min: sorted[0], median: sorted.length % 2 ? sorted[(sorted.length - 1) / 2] : (sorted[sorted.length / 2 - 1] + sorted[sorted.length / 2]) / 2, p95: sorted[Math.ceil(sorted.length * .95) - 1], max: sorted.at(-1), mean: sorted.reduce((a, b) => a + b, 0) / sorted.length };
}
function verify(count, mount, stream, chunks) {
  assert.equal(mount.dom.renderedExchanges, count);
  assert.equal(mount.dom.markdownViews, count);
  assert.equal(mount.parse.history, count, 'each mounted historical answer must really parse');
  assert.equal(stream.dom.renderedExchanges, count + 1);
  assert.equal(stream.dom.markdownViews, count + 1);
  assert.equal(stream.dom.turnCards, 1);
  assert.equal(stream.updates.length, chunks);
  assert.equal(stream.parse.live, chunks, 'each distinct streamed update must reach real MarkdownView');
  assert.equal(stream.timeline.count, chunks, 'each live update must rebuild the actual timeline');
  assert(stream.finalLiveTextLength > chunks * 20);
}
try {
  await page.goto(url);
  await page.waitForFunction(() => window.__historyPerf?.ready === true);
  const workload = await page.evaluate(() => window.__historyPerf.workload);
  const supportedEntries = await page.evaluate(() => window.__historyPerf.supportedEntries);
  if (mode === 'prepare') {
    const checks = [];
    for (const count of [10, 100, 500]) {
      const mount = await page.evaluate(count => window.__historyPerf.mount(count), count);
      const stream = await page.evaluate(() => window.__historyPerf.stream({ chunks: 3, intervalMs: 0 }));
      verify(count, mount, stream, 3);
      checks.push({ count, historyMountParses: mount.parse.history, historicalParsesDuringThreeUpdates: stream.parse.history, liveParses: stream.parse.live, renderedExchanges: stream.dom.renderedExchanges, dom: stream.dom });
      console.log(`Functional check passed: ${count} exchanges (not a timing sample)`);
    }
    await page.locator('#history-perf-composer').focus();
    await page.keyboard.press('a');
    assert.equal(await page.locator('#history-perf-composer').inputValue(), 'a');
    await page.screenshot({ path: path.join(runDir, 'fixture-500.png') });
    assert.deepEqual(pageErrors, []); assert.deepEqual(blockedRequests, []);
    const summary = { passed: true, mode, measured: false, sourceHash, buildHash, sourceFileCount: sourceFiles.length, environment, workload, supportedEntries, checks, pageErrors, consoleErrors, blockedRequests, artifacts: runDir, boundary: 'Functional preparation only; no formal performance conclusions. Fresh component mount excludes module/resource loading.' };
    await write('summary.json', summary);
    await fs.writeFile(path.join(output, 'prepared.json'), JSON.stringify(summary, null, 2) + '\n');
    console.log(JSON.stringify({ readyForMeasurement: true, summary: path.join(runDir, 'summary.json') }));
  } else {
    const samples = [];
    // Warm the parser/component paths at every scale. These are discarded.
    for (const count of [10, 100, 500]) {
      await page.evaluate(count => window.__historyPerf.mount(count), count);
      await page.evaluate(() => window.__historyPerf.stream({ chunks: 4, intervalMs: 0 }));
    }
    console.log('Warmups complete; starting five alternating-order rounds');
    for (let round = 0; round < 5; round++) {
      for (const count of round % 2 ? [500, 100, 10] : [10, 100, 500]) {
        await sleep(150);
        const mount = await page.evaluate(count => window.__historyPerf.mount(count), count);
        await page.locator('#history-perf-composer').focus();
        let finished = false;
        const pending = page.evaluate(() => window.__historyPerf.stream({ chunks: 24, intervalMs: 80 })).finally(() => { finished = true; });
        let injectedKeys = 0;
        while (!finished && injectedKeys < 16) {
          await sleep(110);
          if (!finished) { await page.keyboard.press('a'); injectedKeys++; }
        }
        const stream = await pending;
        verify(count, mount, stream, 24);
        assert.equal((await page.locator('#history-perf-composer').inputValue()).length, injectedKeys);
        samples.push({ round: round + 1, count, mount, stream, injectedKeys });
        await write('samples.partial.json', samples);
        console.log(`Sample ${samples.length}/15 complete: ${count} exchanges, ${stream.parse.history} historical parses across 24 updates`);
      }
    }
    assert.deepEqual(pageErrors, []); assert.deepEqual(blockedRequests, []);
    const summary = { passed: true, mode, measured: true, sourceHash, buildHash, environment, workload, supportedEntries, repetitions: 5, warmups: 'One mount + four-update stream at each size; all discarded', streamCadenceMs: 80, chunksPerSample: 24, sampleMountDefinition: 'Fresh component instances with warmed JS/modules/assets; not application cold start', parseInstrumentation: 'Isolated wrapper of MarkdownIt.prototype.parse; counts all real calls and adds a small timer/counter overhead', clockBoundary: 'All rendering timing uses renderer performance.now; Playwright round-trip time is excluded. rAF is a scheduling proxy, not photon timing.', eventTimingBoundary: 'Event Timing includes only captured events >=16ms and duration quantization is 8ms; keyboard dispatch is synthetic, not OS/IME.', longTaskBoundary: 'Full durations of tasks overlapping the measured window; tasks may straddle phase boundaries and phases must not be summed as disjoint CPU time.', reports: [10, 100, 500].map(count => {
      const selected = samples.filter(sample => sample.count === count);
      const updates = selected.flatMap(sample => sample.stream.updates);
      const keys = selected.flatMap(sample => sample.stream.keyboard.handlers);
      return { count, mountToNextTickMs: stats(selected.map(sample => sample.mount.mountToNextTickMs)), mountToSecondRafMs: stats(selected.map(sample => sample.mount.mountToSecondRafMs)), mountTimelineMs: stats(selected.map(sample => sample.mount.timeline.durationMs)), mountMarkdownParseMs: stats(selected.map(sample => sample.mount.parse.durationMs)), streamFlushMs: stats(updates.map(update => update.flushMs)), streamNextRafMs: stats(updates.map(update => update.nextRafMs)), streamScheduleLagMs: stats(updates.map(update => update.scheduleLagMs)), streamTimelineMsPerUpdate: stats(updates.map(update => update.timelineMs)), historyParsesPerUpdate: stats(updates.map(update => update.historyParses)), historicalParseMsPerStream: stats(selected.map(sample => sample.stream.parse.historyDurationMs)), streamLongTaskCount: stats(selected.map(sample => sample.stream.longTaskCount)), streamLongTaskMs: stats(selected.map(sample => sample.stream.longTaskDurationMs)), syntheticEventToKeyHandlerMs: stats(keys.map(key => key.eventToHandlerMs)), syntheticKeyEvents: keys.length, slowEventTimingRecords: selected.reduce((n, sample) => n + sample.stream.keyboard.eventTiming.length, 0), dom: selected[0].stream.dom };
    }), artifacts: runDir, pageErrors, consoleErrors, blockedRequests };
    await write('samples.json', samples); await write('summary.json', summary);
    await page.screenshot({ path: path.join(runDir, 'last-sample.png') });
    console.log(JSON.stringify({ passed: true, summary: path.join(runDir, 'summary.json') }));
  }
} catch (error) {
  await write('failure.json', { passed: false, mode, sourceHash, buildHash, error: error.stack, pageErrors, consoleErrors, blockedRequests });
  throw error;
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
