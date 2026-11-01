import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5198';
const out = process.argv[3] || '/tmp/pogo-desktop-bounds-qa';
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'no-preference' });
const checks = [], errors = [];
page.on('pageerror', error => errors.push(error.message));
const check = (name, value) => { assert.ok(value, name); checks.push(name); console.log(`PASS ${name}`); };
try {
  await page.addInitScript(() => localStorage.setItem('pogo-locale', 'zh-CN'));
  await page.goto(`${base}/dev/desktop-browser.html?dock=1`);
  await page.waitForFunction(() => window.__desktopQA);
  const entry = await page.evaluate(async () => {
    const qa = window.__desktopQA;
    qa.model.dockOpen = true;
    const samples = [], until = performance.now() + 380;
    while (performance.now() < until) {
      await new Promise(requestAnimationFrame);
      const slot = document.querySelector('[data-testid=desktop-browser-slot]');
      if (slot) { const rect = slot.getBoundingClientRect(); samples.push({ x: rect.x, width: rect.width, height: rect.height }); }
    }
    return { samples, updates: qa.counters.updates.slice() };
  });
  check('real DockArea opening produces transient narrow DOM viewport', entry.samples.some(rect => rect.width > 0 && rect.width < 160));
  check('transient dock widths are never published as a native viewport', entry.updates.length > 0 && entry.updates.every(update => update.bounds.width >= 160 && update.bounds.height >= 100));
  const stable = await page.locator('[data-testid=desktop-browser-slot]').boundingBox();
  check('stable actual DockArea flex layout fills its page instead of remaining narrow', stable.width > 300);
  check('settled native bounds match the complete DOM slot', await page.evaluate(rect => { const last = window.__desktopQA.counters.updates.at(-1); return last.visible && Math.abs(last.bounds.width - rect.width) < 1 && Math.abs(last.bounds.x - rect.x) < 1; }, stable));
  // Add a geometry-only CSS transition. The slot width remains constant, so a
  // ResizeObserver-only implementation misses the movement between endpoints.
  const movement = await page.evaluate(async () => {
    const dock = document.querySelector('.dock');
    dock.style.transition = 'width .18s ease, transform .3s linear';
    const before = window.__desktopQA.counters.updates.length;
    await new Promise(requestAnimationFrame);
    dock.style.transform = 'translateX(-180px)';
    await new Promise(resolve => setTimeout(resolve, 380));
    const rect = document.querySelector('[data-testid=desktop-browser-slot]').getBoundingClientRect();
    return { updates: window.__desktopQA.counters.updates.slice(before), rect: { x: rect.x, width: rect.width } };
  });
  check('position-only transition publishes intermediate positions each frame', new Set(movement.updates.filter(update => update.visible).map(update => Math.round(update.bounds.x))).size >= 4);
  check('position-only transition reaches final position without resizing page width', movement.updates.filter(update => update.visible).every(update => Math.abs(update.bounds.width - stable.width) < 1) && Math.abs(movement.updates.at(-1).bounds.x - movement.rect.x) < 1);
  await page.evaluate(() => { const dock = document.querySelector('.dock'); dock.style.transform = 'translateX(2200px)'; });
  await page.waitForTimeout(390);
  check('fully outside viewport hides the native view and preserves last usable dimensions', await page.evaluate(() => { const last = window.__desktopQA.counters.updates.at(-1); return last.visible === false && last.blocked === true && last.bounds.width >= 160; }));
  await page.evaluate(() => { document.querySelector('.dock').style.transform = ''; });
  await page.waitForTimeout(390);
  check('returning transition restores the native view at the correct rectangle', await page.evaluate(() => { const rect = document.querySelector('[data-testid=desktop-browser-slot]').getBoundingClientRect(), last = window.__desktopQA.counters.updates.at(-1); return last.visible && Math.abs(last.bounds.x - rect.x) < 1 && Math.abs(last.bounds.width - rect.width) < 1; }));
  const idleCount = await page.evaluate(() => window.__desktopQA.counters.updates.length);
  await page.waitForTimeout(300);
  check('settled layout stops emitting per-frame updates', await page.evaluate(count => window.__desktopQA.counters.updates.length === count, idleCount));
  await page.screenshot({ path: `${out}/real-dock-settled.png` });
  check('no page errors', errors.length === 0);
  await fs.writeFile(`${out}/summary.json`, JSON.stringify({ passed: true, checks, errors, entry, movement, stable, boundary: 'Real DockArea/BrowserPane DOM with mock native host. Native clipping and renderer resize remain host integration checks.' }, null, 2));
} catch (error) { await page.screenshot({ path: `${out}/failure.png` }); await fs.writeFile(`${out}/failure.json`, JSON.stringify({ passed: false, checks, errors, error: error.stack }, null, 2)); throw error; }
finally { await browser.close(); }
