import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import { BrowserSession, validateAction } from '../src/session.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label) {
  const end = Date.now() + 5000;
  do { if (await read()) return; await delay(10); } while (Date.now() < end);
  assert.fail(label);
}
function stub() {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} }, { settleMs: 0 });
  const meta = { id: 't1', page: { url: () => 'https://fixture.invalid/page', isClosed: () => false, bringToFront: async () => {} }, title: '', version: 1, visual: 1, control: 'human', lease: 'human-lease', controlEpoch: 1, controlPhase: 'ready' };
  session.pages.set(meta.id, meta); session.active = meta.id;
  session.devtools = { attach: async () => {}, detach: async () => {}, isPaused: () => false, isBlocked: () => false };
  session.observe = async () => ({ ok: true, url: meta.page.url() });
  session.activity.finish = async () => {};
  return { session, meta, claim: () => ({ op_id: randomUUID(), action: 'tab', tab_action: 'claim', target: meta.id, context_ref: session.contextRef, control_epoch: meta.controlEpoch }) };
}

test('claim is an explicit standalone observed-page action, never an implicit click or batch transfer', () => {
  const { session, meta, claim } = stub();
  validateAction(claim());
  for (const patch of [{ context_ref: undefined }, { control_epoch: undefined }, { control_epoch: -1 }, { target: undefined }, { url: 'https://fixture.invalid' }]) assert.throws(() => validateAction({ ...claim(), ...patch }), /claim requires/);
  assert.throws(() => validateAction({ action: 'batch', actions: [claim()] }), /separately/);
  assert.throws(() => session.requireAgentPage(meta), /tab_action:claim/);
  assert.equal(meta.control, 'human');
});

test('claim fences outstanding human input, revokes its lease and is exactly once', async () => {
  const { session, meta, claim } = stub();
  let fenced = 0;
  session.liveViews.add({ meta, async fence() { fenced++; } });
  const action = claim();
  const result = await session.execute(action);
  assert.equal(result.ok, true, result.error);
  assert.equal(result.control, 'agent'); assert.equal(result.control_epoch, 2);
  assert.equal(meta.lease, null); assert.equal(fenced, 1);
  assert.deepEqual(await session.execute(action), result); assert.equal(fenced, 1);
  // Replaying a cached completed result cannot reclaim a later human takeover.
  const later = await session.setControl({ mode: 'human', context_ref: session.contextRef, tab_id: meta.id });
  assert.equal(later.control_epoch, 3);
  assert.deepEqual(await session.execute(action), result);
  assert.equal(meta.control, 'human'); assert.equal(fenced, 2);
  const stale = await session.execute({ ...action, op_id: randomUUID() });
  assert.equal(stale.ok, false); assert.match(stale.error, /ownership changed/);
  assert.equal(meta.control, 'human');
});

test('claim rejects stale context, changed ownership and uncertain live outcomes without transfer', async () => {
  const { session, meta, claim } = stub();
  const wrongContext = await session.execute({ ...claim(), context_ref: 'ctx_000000000000000000000000' });
  assert.equal(wrongContext.ok, false); assert.match(wrongContext.error, /context changed/);
  let open;
  session.queue = new Promise(resolve => { open = resolve; });
  const queued = session.execute(claim());
  await session.setControl({ mode: 'human', force: true, context_ref: session.contextRef, tab_id: meta.id });
  open();
  assert.equal((await queued).ok, false); assert.equal(meta.control, 'human');
  meta.liveInputBlocked = true;
  await assert.rejects(session.execute(claim()), /release is uncertain/);
  assert.equal(meta.control, 'human');
  meta.liveInputBlocked = false;
  await assert.rejects(session.setControl({ mode: 'agent', context_ref: session.contextRef, tab_id: meta.id }), error => error.status === 403);
});

test('claim waits for held-input cleanup and cancellation fences the eventual ownership transfer', async () => {
  const { session, meta, claim } = stub();
  let release, entered = false;
  session.liveViews.add({ meta, async fence() { entered = true; await new Promise(resolve => { release = resolve; }); } });
  const pending = session.execute(claim());
  await until(() => entered, 'claim starts fencing input');
  assert.equal(meta.controlPhase, 'takeover'); assert.equal(meta.control, 'human');
  session.generation++;
  release();
  const result = await pending;
  assert.equal(result.ok, false); assert.match(result.error, /superseded/);
  assert.equal(meta.control, 'human', 'late cleanup must not enable Agent input after cancellation');
});

test('a child opened during human-to-Agent handoff retains the old human authority and completes its own ownership', async () => {
  const { session, meta, claim } = stub();
  session.pageOwners.set(meta.page, meta);
  const child = { ...meta, id: 't2', page: { url: () => 'https://fixture.invalid/authorization', isClosed: () => false }, control: 'human', lease: null, controlEpoch: 0, ownershipPending: true };
  session.pages.set(child.id, child); session.pageOwners.set(child.page, child);
  let finish;
  session.liveViews.add({ meta, async fence() {
    session.recordPopup(meta.page, child.page);
    await new Promise(resolve => { finish = resolve; });
  } });
  const pending = session.execute(claim());
  await until(() => finish, 'popup created while opener transition is pending');
  assert.equal(child.control, 'human'); assert.equal(child.controlPhase, 'ready');
  assert.equal(child.lease, 'human-lease'); assert.equal(child.controlEpoch, 1);
  assert.equal(session.tabMetadata().find(tab => tab.id === child.id).inherited_control_epoch, 1);
  finish();
  assert.equal((await pending).ok, true); assert.equal(meta.control, 'agent');
  assert.equal(child.control, 'human'); assert.equal(child.controlPhase, 'ready');
});

test('human popup, explicit UI new Sheet and Agent claim preserve the real page, cookies and sibling ownership', { timeout: 25000 }, async t => {
  let browser, session;
  let saves = 0;
  const site = http.createServer((req, res) => {
    if (req.url === '/save') { saves++; res.end('saved'); return; }
    res.setHeader('Content-Type', 'text/html'); res.setHeader('Set-Cookie', 'shared_login=yes; Path=/; SameSite=Lax');
    res.end(`<title>${req.url}</title><h1>${req.url}</h1><a id="reading" href="/reading" target="_blank">Read article</a><button id="popup" onclick="window.open('/authorization')">Authorize</button><button id="save" onclick="fetch('/save')">Save once</button><input aria-label="Text">`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const origin = `http://127.0.0.1:${site.address().port}`;
  t.after(async () => { await session?.close(); await browser?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  browser = await chromium.launch({ channel: 'chromium', headless: true });
  const context = await browser.newContext();
  session = await new BrowserSession(context, { settleMs: 0, actionTimeout: 3000 }).init();
  const exec = args => session.execute({ op_id: randomUUID(), ...args });
  assert.equal((await exec({ action: 'navigate', url: origin + '/start' })).ok, true);
  const parent = session.page();
  const human = await session.setControl({ mode: 'human', context_ref: session.contextRef, tab_id: parent.id });
  const binding = { tab_id: parent.id, context_ref: session.contextRef, generation: session.generation, control_epoch: human.control_epoch, lease_token: human.lease_token };
  // A real CDP/Chromium click on a reading _blank link remains this Page.
  await session.dispatchLiveInput(binding, meta => meta.page.locator('#reading').click());
  await until(() => parent.page.url() === origin + '/reading', 'same-Sheet reading navigation');
  assert.equal(session.pages.size, 1); assert.equal(parent.control, 'human');
  await parent.page.goBack();
  assert.equal(parent.page.url(), origin + '/start');
  // Agent opens an independent task Page without claiming the user's page.
  assert.equal((await exec({ action: 'tab', tab_action: 'new', url: origin + '/agent' })).ok, true);
  const agent = session.page(); assert.notEqual(agent.id, parent.id);
  await session.dispatchLiveInput(binding, meta => meta.page.locator('#popup').click());
  await until(() => session.pages.size === 3, 'functional authorization popup is retained');
  const popup = [...session.pages.values()].find(meta => meta !== parent && meta !== agent);
  await popup.ownershipReady;
  assert.equal(popup.control, 'human'); assert.equal(popup.lease, human.lease_token);
  assert.equal(session.active, agent.id, 'human popup cannot select itself as Agent target');
  const tabs = JSON.parse((await exec({ action: 'tabs' })).text);
  const observed = tabs.find(tab => tab.id === popup.id);
  assert.equal(observed.opener_tab, parent.id); assert.equal(observed.inherited_control_epoch, human.control_epoch);
  assert.equal(observed.context_ref, session.contextRef);
  assert.equal(JSON.stringify(tabs).includes(human.lease_token), false, 'lease credentials never reach the model');
  const pageBefore = popup.page, cookiesBefore = await context.cookies();
  const claimed = await exec({ action: 'tab', tab_action: 'claim', target: popup.id, context_ref: observed.context_ref, control_epoch: observed.control_epoch });
  assert.equal(claimed.ok, true, claimed.error); assert.equal(claimed.active_tab, popup.id);
  assert.equal(claimed.control, 'agent'); assert.equal(claimed.control_epoch, human.control_epoch + 1);
  assert.equal(popup.page, pageBefore); assert.deepEqual(await context.cookies(), cookiesBefore);
  assert.equal(parent.control, 'human'); assert.equal(parent.lease, human.lease_token); assert.equal(agent.control, 'agent');
  assert.equal(session.tabMetadata().find(tab => tab.id === popup.id).inherited_control_epoch, undefined);
  await assert.rejects(session.input({ type: 'text', text: 'stale input', context_ref: session.contextRef, target_tab: popup.id, generation: session.generation, lease_token: human.lease_token }), error => error.status === 403);
  assert.equal((await exec({ action: 'click', target: '#save' })).ok, true);
  await until(() => saves === 1, 'claimed page accepts exactly one Agent operation');
  assert.equal(parent.page.url(), origin + '/start');
  const humanNew = await session.manageTabs({ op_id: randomUUID(), action: 'new', context_ref: session.contextRef });
  assert.equal(humanNew.state.control, 'human'); assert.ok(humanNew.lease_token); assert.notEqual(humanNew.lease_token, human.lease_token);
  assert.equal(session.active, popup.id); assert.equal(humanNew.state.viewed_tab, humanNew.tab_id);
  const openNew = await session.input({ type: 'navigate', url: origin + '/manual', context_ref: session.contextRef, target_tab: humanNew.tab_id, generation: session.generation, lease_token: humanNew.lease_token });
  assert.equal(openNew.control, 'human'); assert.equal(openNew.url, origin + '/manual');
  assert.equal(session.active, popup.id);
});
