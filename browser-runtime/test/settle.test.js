import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { createWorker } from '../src/server.js';

// Post-action observation: an agent action waits (bounded) for the requests it
// started, so one reply reports an asynchronous result. Nothing is retried.
let worker, browser, site, base, origin, partner;
let saves = 0, tickets = 0, hangs = 0, mutations = 0, slowSaves = 0, replacements = 0;
const held = [];
let op = 0;
const token = 'settle-test-only-browser-token';
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function request(path, method = 'GET', body) {
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  return { status: response.status, data: await response.json() };
}
const execute = (id, action) => request(`/sessions/${id}/execute`, 'POST', { op_id: `settle-${++op}`, ...action });
async function open(id, url) {
  assert.equal((await request(`/sessions/${id}`, 'PUT')).status, 200);
  const result = await execute(id, { action: 'navigate', url });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  return result.data;
}
function ref(snapshot, name) {
  const line = snapshot.text.split('\n').find(s => s.startsWith('[e') && s.includes(JSON.stringify(name)));
  assert.ok(line, `missing ${name} in ${snapshot.text}`);
  return /^\[(e\d+)\]/.exec(line)[1];
}
const html = (res, body) => { res.setHeader('Content-Type', 'text/html; charset=utf-8'); res.end(`<!doctype html><title>Settle fixture</title>${body}`); };

before(async () => {
  site = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://fixture');
    if (url.pathname === '/save') { saves++; await delay(300); res.end(`R-${saves}`); return; }
    if (url.pathname === '/mutate') { mutations++; res.end('ok'); return; }
    if (url.pathname === '/hang') { hangs++; held.push(res); return; }
    if (url.pathname === '/slow-body') {
      const receipt = `S-${++slowSaves}`;
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      res.flushHeaders();
      await delay(700);
      res.end(receipt); return;
    }
    if (url.pathname === '/delayed-script') {
      await delay(700);
      res.setHeader('Content-Type', 'text/javascript'); res.end(''); return;
    }
    if (url.pathname === '/slow-parser') { html(res, '<script src="/delayed-script"></script><h1>Parsed popup</h1>'); return; }
    if (url.pathname === '/background') {
      html(res, `<h1>Background</h1><script>const channel=new BroadcastChannel('settle-scope');channel.onmessage=()=>window.open('/background-popup')</script>`); return;
    }
    if (url.pathname === '/background-popup') { html(res, `<h1>Unrelated popup</h1><script>fetch('/hang')</script>`); return; }
    if (url.pathname === '/chain') { html(res, `<h1>Middle popup</h1><script>window.open('/quote')</script>`); return; }
    if (url.pathname === '/closing-source') {
      html(res, `<button id="replace" onclick="window.open('/replacement');window.close()">Open and close source</button>`); return;
    }
    if (url.pathname === '/replacement') {
      replacements++;
      await delay(100); // The opener closes before Chromium reports this page.
      html(res, '<h1>Replacement receipt</h1>'); return;
    }
    if (url.pathname === '/ping') {
      for await (const _ of req);
      res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
      res.end('{"ok":true}'); return;
    }
    // A fire-and-forget ping whose body the page never reads, as the tabs
    // fixture does: Chromium may never report such a request as finished.
    if (url.pathname === '/quote') { html(res, `<h1>Quote Q-7</h1><p>Unit price 42</p><script>fetch('/ping',{method:'POST',body:'{}'})</script>`); return; }
    if (url.pathname === '/widget') {
      html(res, '<form method="post" action="/ticket"><label>Name<input name="name"></label><button>Send ticket</button></form>'); return;
    }
    if (url.pathname === '/ticket' && req.method === 'POST') {
      for await (const _ of req);
      tickets++;
      html(res, `<p>Sent</p><script>parent.postMessage({kind:'receipt',id:'T-${tickets}'}, ${JSON.stringify(origin)})</script>`); return;
    }
    if (url.pathname === '/portal') {
      html(res, `<h1>Portal</h1><div id="receipt" role="status">No ticket submitted.</div><iframe title="Partner form" src="${partner}/widget"></iframe>
        <script>addEventListener('message',e=>{if(e.origin===${JSON.stringify(partner)}&&e.data?.kind==='receipt')document.querySelector('#receipt').textContent='Ticket confirmed '+e.data.id})</script>`);
      return;
    }
    html(res, `<h1>Settle</h1><p id="status" role="status">Idle</p>
      <button id="save" onclick="const s=document.querySelector('#status');s.textContent='Saving…';fetch('/save').then(r=>r.text()).then(t=>{s.textContent='Saved '+t})">Save</button>
      <button id="hang" onclick="document.querySelector('#status').textContent='Waiting';fetch('/hang')">Hang</button>
      <button id="leave" onclick="fetch('/hang');location.href='/quote'">Leave</button>
      <button id="count" onclick="fetch('/mutate')">Count</button>
      <button id="noop" onclick="document.querySelector('#status').textContent='Clicked'">Noop</button>
      <button id="background-signal" onclick="new BroadcastChannel('settle-scope').postMessage('open');fetch('/save').then(r=>r.text()).then(t=>document.querySelector('#status').textContent='Saved '+t)">Save with background</button>
      <button id="slow-body" onclick="document.querySelector('#status').textContent='Saving slow';fetch('/slow-body').then(r=>r.text()).then(t=>document.querySelector('#status').textContent='Slow receipt '+t)">Slow save</button>
      <button id="delayed-dom" onclick="document.querySelector('#status').textContent='Saving delayed';fetch('/save').then(r=>r.text()).then(t=>setTimeout(()=>document.querySelector('#status').textContent='Delayed receipt '+t,600))">Delayed save</button>
      <button id="chain" onclick="document.querySelector('#status').textContent='Original tab changed';window.open('/chain')">Open chain</button>
      <!-- These fixtures deliberately require a popup/opener relationship.
           A plain reading link now navigates the current Page instead. -->
      <a id="parser" target="_blank" rel="opener" href="/slow-parser">Open slow parser</a>
      <a id="closing-source" target="_blank" rel="opener" href="/closing-source">Open closing source</a>
      <a id="quote" target="_blank" rel="opener" href="/quote">Open quote</a>
      <a id="reading-parser" target="_blank" href="/slow-parser">Read slow article</a>
      <label>Name<input id="name"></label>
      <select id="choice" aria-label="Choice"><option value="a">Alpha</option></select>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  partner = `http://localhost:${site.address().port}`;
  browser = await chromium.launch();
  worker = await createWorker({ token, browser, actionTimeout: 2500, idleMs: 60_000 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});

after(async () => {
  for (const res of held.splice(0)) res.end('late');
  await worker?.close();
  await browser?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

test('click observation includes a fetch-driven result started by the click, sent once', async () => {
  await open('async', origin + '/');
  const before = saves;
  const result = await execute('async', { action: 'click', target: '#save' });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  assert.match(result.data.text, /Saved R-\d+/, 'the reply must observe the completed update, not "Saving…"');
  assert.equal(result.data.pending_requests, undefined);
  assert.ok(result.data.settle_ms >= 250, `settle_ms=${result.data.settle_ms}`);
  assert.equal(saves, before + 1);
});

test('cross-origin iframe submit reports the parent receipt in the same observation', async () => {
  const snapshot = await open('iframe', origin + '/portal');
  const typed = await execute('iframe', { action: 'type', target: ref(snapshot, 'Name'), text: 'Test User' });
  assert.equal(typed.data.ok, true, JSON.stringify(typed.data));
  const before = tickets;
  const sent = await execute('iframe', { action: 'click', target: ref(typed.data, 'Send ticket') });
  assert.equal(sent.data.ok, true, JSON.stringify(sent.data));
  assert.match(sent.data.text, /Ticket confirmed T-\d+/);
  assert.equal(tickets, before + 1);
});

test('a popup link is observed as the new active tab and named in the reply', async () => {
  await open('popup', origin + '/');
  const home = (await request('/sessions/popup/state')).data.active_tab;
  const result = await execute('popup', { action: 'click', target: '#quote' });
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  assert.notEqual(result.data.active_tab, home);
  assert.deepEqual(result.data.opened_tabs.map(t => t.id), [result.data.active_tab]);
  assert.equal(result.data.from_tab, home);
  assert.match(result.data.text, /Quote Q-7/);
  assert.equal(result.data.pending_requests, undefined, 'an answered request whose body is never read is not pending');
  assert.ok(result.data.settle_ms < 1000, `settle_ms=${result.data.settle_ms}`);
  const back = await execute('popup', { action: 'tab', target: home });
  assert.equal(back.data.ok, true);
  assert.equal(back.data.opened_tabs, undefined, 'switching tabs opens nothing');
});

test('settle is bounded, reports unfinished requests and never repeats the action', async () => {
  await open('hang', origin + '/');
  const before = hangs;
  const started = Date.now();
  const result = await execute('hang', { action: 'click', target: '#hang' });
  const elapsed = Date.now() - started;
  assert.equal(result.data.ok, true, JSON.stringify(result.data));
  assert.equal(result.data.pending_requests, 1);
  assert.ok(elapsed >= 1000 && elapsed < 2400, `elapsed=${elapsed}`);
  assert.equal(hangs, before + 1);
  // Requests abandoned by a navigation stop counting immediately.
  const left = await execute('hang', { action: 'click', target: '#leave' });
  assert.equal(left.data.ok, true, JSON.stringify(left.data));
  assert.match(left.data.text, /Quote Q-7/);
  assert.equal(left.data.pending_requests, undefined);
  assert.ok(left.data.settle_ms < 1000, `settle_ms=${left.data.settle_ms}`);
});

test('type and select require text at the executor; an invalid later step runs no earlier step', async () => {
  await open('strict', origin + '/');
  const before = mutations;
  const batch = await execute('strict', { action: 'batch', actions: [{ action: 'click', target: '#count' }, { action: 'type', target: '#name' }] });
  assert.equal(batch.data.ok, false);
  assert.match(batch.data.error, /type requires text/);
  const select = await execute('strict', { action: 'select', target: '#choice' });
  assert.equal(select.data.ok, false);
  assert.match(select.data.error, /select requires text/);
  await delay(100);
  assert.equal(mutations, before, 'no step of a refused batch may run');
  const cleared = await execute('strict', { action: 'type', target: '#name', text: '' });
  assert.equal(cleared.data.ok, true, 'an explicit empty string still clears');
});

async function withBackground(id) {
  const snapshot = await open(id, origin + '/?case=' + id);
  const home = snapshot.active_tab;
  await execute(id, { action: 'tab', tab_action: 'new', url: origin + '/background?case=' + id });
  const page = browser.contexts().flatMap(context => context.pages()).find(page => page.url() === origin + '/background?case=' + id);
  assert.ok(page, 'fixture background page exists');
  assert.equal((await execute(id, { action: 'tab', target: home })).data.ok, true);
  return { page, home };
}

test('existing background tab requests do not delay or contaminate the action observation', async () => {
  const { page, home } = await withBackground('background-requests');
  await page.evaluate(() => { window.poll = setInterval(() => fetch('/hang'), 25); });
  try {
    const result = (await execute('background-requests', { action: 'click', target: '#noop' })).data;
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(result.active_tab, home);
    assert.match(result.text, /Clicked/);
    assert.equal(result.pending_requests, undefined, 'background requests are outside this action scope');
    assert.ok(result.settle_ms < 700, `unrelated requests consumed settle budget: ${result.settle_ms}`);
  } finally { await page.evaluate(() => clearInterval(window.poll)); }
});

test('an unrelated background popup is not attributed to the action or made its observation', async () => {
  const { page, home } = await withBackground('background-popup');
  const popup = page.waitForEvent('popup');
  const result = (await execute('background-popup', { action: 'click', target: '#background-signal' })).data;
  await popup;
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(result.active_tab, home, 'a background popup must not steal the observed page');
  assert.equal(result.opened_tabs, undefined);
  assert.equal(result.pending_requests, undefined);
  assert.match(result.text, /Saved R-/);
});

test('related popup chains are observed while the original page may also change', async () => {
  const home = (await open('popup-chain', origin + '/')).active_tab;
  const result = (await execute('popup-chain', { action: 'click', target: '#chain' })).data;
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(result.opened_tabs.length, 2);
  assert.equal(result.from_tab, home);
  assert.match(result.text, /Quote Q-7/);
  const returned = (await execute('popup-chain', { action: 'tab', target: home })).data;
  assert.match(returned.text, /Original tab changed/, 'opening a popup never implies the source tab stayed unchanged');
});

test('settle_ms includes the popup DOM loading wait', async () => {
  await open('popup-parser', origin + '/');
  const result = (await execute('popup-parser', { action: 'click', target: '#parser' })).data;
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.match(result.text, /Parsed popup/);
  assert.ok(result.settle_ms >= 500, `popup load time was omitted: ${result.settle_ms}`);
});

test('current-Sheet reading waits for parser completion without presenting an empty DOM or opening another Page', async () => {
  const home = (await open('reading-parser', origin + '/')).active_tab;
  const result = (await execute('reading-parser', { action: 'click', target: '#reading-parser' })).data;
  assert.equal(result.ok, true, JSON.stringify(result));
  assert.equal(result.active_tab, home, 'plain reading navigation keeps the Page');
  assert.equal(result.opened_tabs, undefined, 'a same-Sheet navigation cannot report a popup');
  assert.match(result.text, /Parsed popup/, 'a successful observation must include the parsed new document');
  assert.ok(result.settle_ms >= 500, `same-Page parser load time was omitted: ${result.settle_ms}`);
});

test('slow response bodies and delayed DOM results converge with explicit waits without resubmitting', async () => {
  await open('explicit-wait', origin + '/');
  const slowBefore = slowSaves;
  const slow = (await execute('explicit-wait', { action: 'click', target: '#slow-body' })).data;
  assert.equal(slow.ok, true);
  assert.equal(slow.pending_requests, undefined, 'response headers end fetch tracking, not business verification');
  assert.ok(slow.settle_ms < 500, `fetch body must not cause an unbounded requestfinished wait: ${slow.settle_ms}`);
  const slowDone = (await execute('explicit-wait', { action: 'wait', text: 'Slow receipt S-', seconds: 2 })).data;
  assert.equal(slowDone.ok, true, JSON.stringify(slowDone));
  assert.match(slowDone.text, /Slow receipt S-/);
  assert.equal(slowSaves, slowBefore + 1);
  const before = saves;
  assert.equal((await execute('explicit-wait', { action: 'click', target: '#delayed-dom' })).data.ok, true);
  const delayed = (await execute('explicit-wait', { action: 'wait', text: 'Delayed receipt R-', seconds: 2 })).data;
  assert.equal(delayed.ok, true, JSON.stringify(delayed));
  assert.match(delayed.text, /Delayed receipt R-/);
  assert.equal(saves, before + 1);
});

test('settleMs zero disables the wait even when a related request remains pending', async () => {
  const immediate = await createWorker({ token, browser, actionTimeout: 2500, settleMs: 0 });
  const endpoint = `http://127.0.0.1:${(await immediate.listen(0)).port}`;
  const call = async (path, method, body) => (await fetch(endpoint + path, {
    method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })).json();
  try {
    await call('/sessions/zero', 'PUT');
    await call('/sessions/zero/execute', 'POST', { op_id: 'zero-nav', action: 'navigate', url: origin + '/' });
    const result = await call('/sessions/zero/execute', 'POST', { op_id: 'zero-click', action: 'click', target: '#hang' });
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.match(result.text, /Waiting/);
    assert.ok(result.settle_ms < 25, `disabled settle waited ${result.settle_ms} ms`);
  } finally { await immediate.close(); }
});

async function closingSource(id, { only = false, batch = false } = {}) {
  const home = (await open(id, origin + '/')).active_tab;
  const source = (await execute(id, { action: 'click', target: '#closing-source' })).data;
  assert.notEqual(source.active_tab, home);
  if (only) assert.equal((await execute(id, { action: 'tab', tab_action: 'close', target: home })).data.ok, true);
  const before = replacements, writes = mutations;
  const result = (await execute(id, batch ? {
    action: 'batch', actions: [{ action: 'click', target: '#replace' }, { action: 'click', target: '#count' }],
  } : { action: 'click', target: '#replace' })).data;
  if (!result.ok) assert.match(result.error, /Input outcome could not be confirmed because its source tab closed/);
  assert.match(result.text, /source tab .* closed/);
  assert.match(result.text, /attribution is unavailable/);
  assert.match(result.text, /Do not repeat the action/);
  assert.equal(result.opened_tabs, undefined, 'do not guess which window came from the closed opener');
  if (batch) assert.equal(result.steps.length, 1, 'unconfirmed observation must stop the rest of the batch');
  assert.equal(mutations, writes, 'the following batch write did not execute');
  if (only) assert.equal(result.active_tab, '', 'fixture exercises discovery without an active page');
  let tabs, replacement;
  const deadline = Date.now() + 1500;
  do {
    const listed = (await execute(id, { action: 'tabs' })).data;
    assert.equal(listed.ok, true, JSON.stringify(listed));
    tabs = JSON.parse(listed.text);
    replacement = tabs.find(tab => tab.url === origin + '/replacement');
    if (!replacement) await delay(25); // Discovery only: the page can be reported after the batch stops.
  } while (!replacement && Date.now() < deadline);
  if (only) assert.equal(tabs.length, 1, 'the source closed with only its unattributed replacement left');
  assert.ok(replacement, JSON.stringify(tabs));
  if (replacement.control === 'human') {
    const refused = (await execute(id, { action: 'tab', target: replacement.id })).data;
    assert.equal(refused.ok, false, 'a page with missing opener attribution must not be silently adopted');
    const claimed = (await execute(id, { action: 'tab', tab_action: 'claim', target: replacement.id,
      context_ref: replacement.context_ref, control_epoch: replacement.control_epoch })).data;
    assert.equal(claimed.ok, true, JSON.stringify(claimed));
    assert.match(claimed.text, /Replacement receipt/);
  }
  const chosen = (await execute(id, { action: 'tab', target: replacement.id })).data;
  assert.equal(chosen.ok, true, JSON.stringify(chosen));
  assert.match(chosen.text, /Replacement receipt/);
  assert.equal(replacements, before + 1, 'recovery observed the existing new tab without replaying the action');
}

test('a closing opener does not present an old homepage as the action result', () => closingSource('closing-with-home'));
test('tabs and explicit switching recover when a closing opener leaves no active page', () => closingSource('closing-only-new', { only: true }));
test('an unattributed replacement after source closure stops the rest of the batch', () => closingSource('closing-batch', { batch: true }));

test('a human popup that closes its opener before attribution remains human until explicitly claimed', async () => {
  const id = 'human-closing-source';
  const home = (await open(id, origin + '/')).active_tab;
  const source = (await execute(id, { action: 'click', target: '#closing-source' })).data;
  assert.equal(source.ok, true, JSON.stringify(source)); assert.notEqual(source.active_tab, home);
  const human = await request(`/sessions/${id}/control`, 'POST', { mode: 'human',
    context_ref: (await request(`/sessions/${id}/state`)).data.context_ref, tab_id: source.active_tab });
  assert.equal(human.status, 200, JSON.stringify(human.data));
  const page = browser.contexts().flatMap(context => context.pages()).find(page => page.url() === origin + '/closing-source');
  assert.ok(page, 'human source is the existing real Page');
  const before = replacements;
  try { await page.locator('#replace').click(); }
  catch (error) { assert.match(error.message, /Target (?:page, context or browser has been closed|closed)/); }
  let replacement;
  const deadline = Date.now() + 2000;
  do {
    const listed = (await execute(id, { action: 'tabs' })).data;
    assert.equal(listed.ok, true, JSON.stringify(listed));
    replacement = JSON.parse(listed.text).find(tab => tab.url === origin + '/replacement');
    if (!replacement) await delay(25);
  } while (!replacement && Date.now() < deadline);
  assert.ok(replacement, 'late human child is discoverable');
  assert.equal(replacement.opener_tab, undefined, 'fixture exercises a child whose source closed before native opener attribution');
  assert.equal(replacement.control, 'human', 'missing native opener information cannot grant Agent ownership');
  assert.notEqual((await request(`/sessions/${id}/state`)).data.active_tab, replacement.id, 'human child never selects itself for Agent');
  const refused = (await execute(id, { action: 'tab', target: replacement.id })).data;
  assert.equal(refused.ok, false);
  const claimed = (await execute(id, { action: 'tab', tab_action: 'claim', target: replacement.id,
    context_ref: replacement.context_ref, control_epoch: replacement.control_epoch })).data;
  assert.equal(claimed.ok, true, JSON.stringify(claimed)); assert.match(claimed.text, /Replacement receipt/);
  assert.equal(replacements, before + 1, 'claim observes the existing child without repeating its opening action');
});
