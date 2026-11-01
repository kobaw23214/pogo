import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserSession, DEFAULT_ACTION_TIMEOUT_MS, validateAction } from '../src/session.js';
import { createWorker } from '../src/server.js';

test('normal actions and navigation receive a minute while an explicit host budget remains authoritative', () => {
  for (const [options, expected] of [[{}, 60_000], [{ actionTimeout: 2500 }, 2500]]) {
    const configured = [];
    const session = new BrowserSession({ on() {}, setDefaultTimeout(ms) { configured.push(['action', ms]); }, setDefaultNavigationTimeout(ms) { configured.push(['navigation', ms]); } }, options);
    assert.equal(session.actionTimeout, expected);
    assert.deepEqual(configured, [['action', expected], ['navigation', expected]]);
    assert.equal(session.settleMs, Math.min(2000, expected / 2), 'the post-action settle remains a separate short observation');
  }
  assert.equal(DEFAULT_ACTION_TIMEOUT_MS, 60_000);
});

test('conditional waits retain their explicit shorter duration and reject unsupported durations at the worker boundary', async () => {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} });
  const calls = [], page = { getByText: () => ({ first: () => ({ waitFor: async options => calls.push(options) }) }), url: () => 'about:blank' };
  session.page = () => ({ page });
  session.devtools.attach = async () => {};
  session.checkObservation = () => {};
  session.observe = async () => ({ ok: true });
  for (const seconds of [undefined, 0, 10, 45, 60, 0.001]) {
    const action = { action: 'wait', text: 'ready', ...(seconds === undefined ? {} : { seconds }) };
    validateAction(action);
    assert.equal((await session.action(action, session.generation, { observation: { live: true, deadline: Infinity } })).ok, true);
  }
  assert.deepEqual(calls.map(call => call.timeout), [60_000, 60_000, 10_000, 45_000, 60_000, 1]);
  for (const seconds of [-1, 60.001, NaN, Infinity, '60', null]) assert.throws(() => validateAction({ action: 'wait', text: 'ready', seconds }), /between 0 and 60/);
  session.actionTimeout = 1500;
  await session.action({ action: 'wait', gone_text: 'Loading', seconds: 60 }, session.generation, { observation: { live: true, deadline: Infinity } });
  assert.equal(calls.at(-1).timeout, 1500, 'an injected shorter worker budget is never bypassed');
});

// Real Chromium crosses both former limits: navigation used to stop at 15 s,
// while a requested longer wait was additionally capped at 30 s. Independent
// sessions run concurrently so this regression costs one ~32-second interval.
test('real worker navigation and conditional waits survive the former 15/30-second limits without replay', { timeout: 50_000 }, async t => {
  const timers = new Set(), visits = new Map();
  const site = http.createServer((request, response) => {
    const path = new URL(request.url, 'http://fixture.invalid').pathname;
    visits.set(path, (visits.get(path) ?? 0) + 1);
    response.setHeader('Content-Type', 'text/html');
    if (path === '/slow-navigation') {
      const timer = setTimeout(() => { timers.delete(timer); response.end('<title>Slow navigation</title><h1>Navigation ready</h1>'); }, 16_000);
      timers.add(timer);
    } else response.end('<title>Delayed condition</title><p id="state">Loading</p><script>setTimeout(()=>document.querySelector("#state").textContent="Condition ready",31500)</script>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(async () => { for (const timer of timers) clearTimeout(timer); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const worker = await createWorker({ token: 'minute-budget-regression-only', browser, settleMs: 0 });
  t.after(() => worker.close());
  const base = `http://127.0.0.1:${(await worker.listen(0)).port}`, origin = `http://127.0.0.1:${site.address().port}`;
  let next = 0;
  const request = async (id, action) => {
    const response = await fetch(`${base}/sessions/${id}${action ? '/execute' : ''}`, { method: action ? 'POST' : 'PUT', headers: { Authorization: 'Bearer minute-budget-regression-only', 'Content-Type': 'application/json' }, body: JSON.stringify(action ? { op_id: `minute-${++next}`, ...action } : {}) });
    assert.equal(response.status, 200);
    const reply = await response.json(); assert.equal(reply.ok, true, JSON.stringify(reply)); return reply;
  };
  await Promise.all(['navigation', 'default-wait', 'explicit-wait'].map(id => request(id)));
  const began = Date.now();
  await Promise.all([
    request('navigation', { action: 'navigate', url: origin + '/slow-navigation' }).then(reply => assert.match(reply.text, /Navigation ready/)),
    ...['default-wait', 'explicit-wait'].map(async id => {
      await request(id, { action: 'navigate', url: origin + '/' + id });
      const reply = await request(id, { action: 'wait', text: 'Condition ready', ...(id === 'explicit-wait' ? { seconds: 45 } : {}) });
      assert.match(reply.text, /Condition ready/);
    }),
  ]);
  assert.ok(Date.now() - began >= 31_000, 'the actual conditions outlive both old caps');
  for (const path of ['/slow-navigation', '/default-wait', '/explicit-wait']) assert.equal(visits.get(path), 1, `${path} must not be replayed`);
});
