import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright';
import { createMockPlatform, DEMO_CREDENTIALS } from '../../mock-platform/server.js';
import { browserCalls, jsonPayload, assertLoginReadEvidence, expiryFixtureComplete } from './e2e-evidence.js';
import { API_PATH, DATA } from './tasks.js';

test('full-Pogo evidence requires matching completed calls, real payload and backend login', () => {
  const payload = { status: 200, data: { total: 1, orders: [{ id: DATA.order_id, amount: DATA.amount }] } };
  const events = [
    { type: 'tool_start', name: 'browser', call_id: 'a', args: { action: 'auth_status' } },
    { type: 'tool_start', name: 'browser', call_id: 'b', args: { action: 'api_read', path: API_PATH } },
    { type: 'tool_end', name: 'browser', call_id: 'b', details: { ok: true }, text: `runtime: native\npage: mock\n${JSON.stringify(payload)}` },
    { type: 'tool_end', name: 'browser', call_id: 'unknown', details: { ok: true }, text: JSON.stringify(payload) },
    { type: 'tool_end', name: 'browser', call_id: 'a', details: { ok: true }, text: 'page: mock\n{"api_access":{"mode":"automatic"}}' },
  ];
  const calls = browserCalls(events); assert.equal(calls.length, 2);
  assert.equal(jsonPayload('page: mock\nnot JSON', 'status'), null);
  const input = { calls, before: { counters: {} }, after: { scenario: 'cookie', activeSessions: 1, activeTokens: 0, counters: { loginSubmissions: 1, successfulLogins: 1, orderReads: 2 } }, path: API_PATH, orderID: DATA.order_id, amount: DATA.amount, scenario: 'cookie' };
  assert.equal(assertLoginReadEvidence(input).passed, true);
  assert.equal(assertLoginReadEvidence({ ...input, calls: [] }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, after: { counters: { orderReads: 2 } } }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, calls: calls.map(call => call.args.action === 'api_read' ? { ...call, text: JSON.stringify({ ...payload, status: 401 }) } : call) }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, calls: [...calls, { args: { action: 'navigate', url: 'http://mock/api/lab/status' }, ok: true }] }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, calls: calls.map(call => call.args.action === 'api_read' ? { ...call, text: '{"status":200,"data":{"total":1,"orders":{}}}' } : call) }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, scenario: 'bearer' }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, scenario: undefined }).passed, false);
  assert.equal(assertLoginReadEvidence({ ...input, after: { ...input.after, activeSessions: 0, activeTokens: 1 } }).passed, false);
  const bearer = { ...input, scenario: 'bearer', after: { ...input.after, scenario: 'bearer', activeSessions: 0, activeTokens: 1 } };
  assert.equal(assertLoginReadEvidence(bearer).passed, true);
  assert.equal(assertLoginReadEvidence({ ...bearer, after: { ...bearer.after, scenario: 'cookie', activeSessions: 1, activeTokens: 0 } }).passed, false);
  events[2].is_error = true;
  assert.equal(assertLoginReadEvidence({ ...input, calls: browserCalls(events) }).passed, false);
});

test('both retired auth entrypoints fail before starting worker or model in every live mode', () => {
  for (const profile of ['auth', 'auth-auto']) for (const mode of ['validate', 'model']) {
    const result = spawnSync(process.execPath, [`eval/${profile}/run.js`, '--mode', mode, '--agent', '/does/not/exist', '--model', 'unused'], { encoding: 'utf8' });
    assert.equal(result.status, 2); assert.match(result.stderr, /retired.*No worker or model was started/);
    assert.match(result.stderr, /eval\/browser-use-v2\/run.js/); assert.equal(result.stdout, '');
  }
});

test('expiry fixture completion requires exactly its own 401 and no active credentials', () => {
  const before = { counters: { labExpires: 0, unauthorizedRequests: 1 } };
  const pending = { counters: { labExpires: 1, unauthorizedRequests: 1 }, activeSessions: 0, activeTokens: 0 };
  assert.equal(expiryFixtureComplete(before, pending), false);
  const complete = { ...pending, counters: { ...pending.counters, unauthorizedRequests: 2 } };
  assert.equal(expiryFixtureComplete(before, complete), true);
  assert.equal(expiryFixtureComplete(before, { ...complete, activeTokens: 1 }), false);
  assert.equal(expiryFixtureComplete(before, { ...complete, activeSessions: undefined }), false);
  assert.throws(() => expiryFixtureComplete(before, { ...complete, counters: { ...complete.counters, unauthorizedRequests: 3 } }));
  assert.throws(() => expiryFixtureComplete(before, { ...complete, counters: { ...complete.counters, labExpires: 2 } }));
});

test('human keyboard expiry includes its asynchronous confirmation 401 in the baseline', { timeout: 20000 }, async () => {
  const platform = createMockPlatform({ port: 0 }), origin = await platform.listen();
  let browser;
  try {
    const env = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'LANG', 'DISPLAY', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
    browser = await chromium.launch({ headless: true, env });
    for (const scenario of ['cookie', 'bearer']) {
      const context = await browser.newContext(), page = await context.newPage(), run = `keyboard-${scenario}`;
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort('blockedbyclient'));
      const start = `${origin}/?run=${run}&scenario=${scenario}`;
      await page.goto(start); await page.locator('#password').fill(DEMO_CREDENTIALS.password);
      await page.locator('#login-form button[type="submit"]').click();
      await page.locator('#overview-orders tbody tr').first().waitFor({ state: 'visible' });
      await page.goto(start); await page.locator('#overview-orders tbody tr').first().waitFor({ state: 'visible' });
      const beforeExpiry = platform.inspect(run);
      let releaseConfirmation, confirmationSeen;
      const seen = new Promise(resolve => { confirmationSeen = resolve; });
      const release = new Promise(resolve => { releaseConfirmation = resolve; });
      await page.route('**/api/orders', async route => {
        if (platform.inspect(run).counters.labExpires === 1) { confirmationSeen(); await release; }
        await route.continue();
      });
      await page.keyboard.press('Tab'); await page.keyboard.press('Enter');
      await page.locator('#lab-dialog').waitFor({ state: 'visible' });
      await page.keyboard.press('Tab'); await page.keyboard.press('Enter');
      let confirmationTimer;
      try { await Promise.race([seen, new Promise((_, reject) => { confirmationTimer = setTimeout(() => reject(new Error('expiry confirmation request did not start')), 5000); })]); }
      finally { clearTimeout(confirmationTimer); }
      assert.equal(expiryFixtureComplete(beforeExpiry, platform.inspect(run)), false, 'expire POST alone cannot begin the model phase');
      releaseConfirmation();
      await page.locator('#lab-message').waitFor({ state: 'visible' });
      const result = platform.inspect(run);
      assert.equal(result.counters.labExpires, 1); assert.equal(result.activeSessions + result.activeTokens, 0);
      assert.equal(expiryFixtureComplete(beforeExpiry, result), true);
      const probe = await context.request.get(origin + '/api/orders');
      assert.equal(probe.status(), 401);
      assert.equal(platform.inspect(run).counters.unauthorizedRequests - result.counters.unauthorizedRequests, 1, 'later API phase sees exactly one new 401');
      await context.close();
    }
  } finally { await browser?.close(); await platform.close(); }
});
