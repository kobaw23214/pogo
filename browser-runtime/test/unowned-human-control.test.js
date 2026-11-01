import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';

function stub() {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} }, { settleMs: 0 });
  const meta = { id: 't1', page: { url: () => 'https://fixture.invalid/page', isClosed: () => false, bringToFront: async () => {} },
    title: '', version: 1, visual: 1, control: 'human', lease: null, controlEpoch: 0, controlPhase: 'ready', ownershipPending: false };
  session.pages.set(meta.id, meta); session.active = meta.id;
  session.devtools = { attach: async () => {}, detach: async () => {}, isPaused: () => false, isBlocked: () => false };
  session.observe = async () => ({ ok: true, url: meta.page.url() });
  session.activity.finish = async () => {};
  const adopt = patch => session.setControl({ mode: 'human', tab_id: meta.id, context_ref: session.contextRef,
    generation: session.generation, control_epoch: 0, adopt_if_unowned: true, ...patch });
  return { session, meta, adopt };
}

test('initializing unassigned human input grants one lease without changing the Page or Agent target', async () => {
  const { session, meta, adopt } = stub();
  let fenced = 0; session.liveViews.add({ meta, async fence() { fenced++; } });
  const page = meta.page, active = session.active;
  const granted = await adopt();
  assert.equal(granted.control, 'human'); assert.equal(granted.control_epoch, 1);
  assert.ok(granted.lease_token); assert.equal(meta.lease, granted.lease_token);
  assert.equal(meta.page, page); assert.equal(session.active, active); assert.equal(fenced, 1);
  await assert.rejects(adopt(), error => error.status === 409);
  assert.equal(meta.lease, granted.lease_token, 'initialization cannot rotate an assigned lease');
  assert.equal(fenced, 1);
});

test('initialization rejects stale bindings, nonzero epochs, forced takeover and malformed activation flags', async () => {
  for (const patch of [{ context_ref: 'ctx_000000000000000000000000' }, { generation: 0 }, { generation: 2 },
    { generation: undefined }, { control_epoch: 1 }, { control_epoch: undefined }, { force: true },
    { mode: 'agent' }, { lease_token: 'existing-owner' }, { adopt_if_unowned: false }, { adopt_if_unowned: 'true' }, { adopt_if_unowned: null }]) {
    const { meta, adopt } = stub();
    await assert.rejects(adopt(patch));
    assert.equal(meta.control, 'human'); assert.equal(meta.lease, null); assert.equal(meta.controlEpoch, 0);
  }
});

test('initialization cannot replace an Agent, an existing human owner or uncertain pending ownership', async () => {
  for (const state of [{ control: 'agent' }, { lease: 'other-human' }, { controlEpoch: 1 },
    { ownershipPending: true }, { controlPhase: 'takeover' }, { liveInputBlocked: true }]) {
    const { meta, adopt } = stub(); Object.assign(meta, state);
    const previous = { mode: meta.control, lease: meta.lease, epoch: meta.controlEpoch };
    await assert.rejects(adopt(), error => error.status === 409);
    assert.deepEqual({ mode: meta.control, lease: meta.lease, epoch: meta.controlEpoch }, previous);
  }
});

test('a competing initializer or Agent claim wins once and a stale visible viewer cannot force takeover', async () => {
  const first = stub();
  const outcomes = await Promise.allSettled([first.adopt(), first.adopt()]);
  assert.equal(outcomes.filter(result => result.status === 'fulfilled').length, 1);
  assert.equal(outcomes.filter(result => result.status === 'rejected').length, 1);
  assert.equal(first.meta.controlEpoch, 1); assert.ok(first.meta.lease);
  const second = stub();
  const claimed = await second.session.execute({ op_id: randomUUID(), action: 'tab', tab_action: 'claim',
    target: second.meta.id, context_ref: second.session.contextRef, control_epoch: 0 });
  assert.equal(claimed.ok, true, claimed.error); assert.equal(second.meta.control, 'agent');
  await assert.rejects(second.adopt(), error => error.status === 409);
  assert.equal(second.meta.control, 'agent'); assert.equal(second.meta.lease, null); assert.equal(second.meta.controlEpoch, 1);
});

test('late native popup attribution records the opener without replacing an initialized human or explicit Agent claim', async () => {
  for (const mode of ['human', 'agent']) {
    const { session, meta, adopt } = stub();
    const parent = { id: 't2', page: { url: () => 'https://fixture.invalid/source', isClosed: () => false },
      control: 'agent', lease: null, controlEpoch: 7, controlPhase: 'ready', ownershipPending: false };
    session.pages.set(parent.id, parent); session.pageOwners.set(parent.page, parent); session.active = parent.id;
    if (mode === 'human') await adopt();
    else {
      const claimed = await session.execute({ op_id: randomUUID(), action: 'tab', tab_action: 'claim',
        target: meta.id, context_ref: session.contextRef, control_epoch: 0 });
      assert.equal(claimed.ok, true, claimed.error);
    }
    const owner = { mode: meta.control, lease: meta.lease, epoch: meta.controlEpoch };
    session.active = parent.id;
    session.recordPopup(parent.page, meta.page);
    assert.equal(meta.opener, parent.page, 'exact relationship is still recorded');
    assert.equal(meta.ownerInherited, true); assert.equal(meta.ownershipPending, false);
    assert.deepEqual({ mode: meta.control, lease: meta.lease, epoch: meta.controlEpoch }, owner,
      'later opener discovery cannot replace completed explicit ownership');
    assert.equal(session.active, parent.id, 'late relationship cannot select the child as the Agent target');
    session.recordPopup(parent.page, meta.page);
    assert.equal(meta.opener, parent.page);
    assert.deepEqual({ mode: meta.control, lease: meta.lease, epoch: meta.controlEpoch }, owner,
      'duplicate attribution remains harmless after explicit ownership');
    assert.equal(session.active, parent.id, 'duplicate popup events cannot reactivate the claimed child');
    if (mode === 'human') assert.ok(meta.lease, 'newly initialized human keeps its independent usable lease');
    else assert.equal(meta.lease, null, 'explicit Agent claim remains free of old human authority');
  }
});

test('real Ctrl and middle clicks create human unassigned Pages, then only the selected Page gets native input', { timeout: 15000 }, async t => {
  let session, browser;
  const site = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'text/html;charset=utf-8');
    res.end(`<title>Native ${req.url}</title><a id=new target=_blank href='/article'>Article</a><input id=text aria-label='Text'><input id=range type=range min=0 max=100 value=0 style='width:300px'>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(async () => { await session?.close(); await browser?.close(); site.closeAllConnections(); await new Promise(resolve => site.close(resolve)); });
  const origin = `http://127.0.0.1:${site.address().port}`;
  browser = await chromium.launch({ channel: 'chromium', headless: true });
  const context = await browser.newContext();
  session = await new BrowserSession(context, { settleMs: 0 }).init();
  const parent = session.page(); await parent.page.goto(origin + '/parent');
  const owner = await session.setControl({ mode: 'human', tab_id: parent.id, context_ref: session.contextRef });
  const children = [];
  for (const options of [{ modifiers: ['ControlOrMeta'] }, { button: 'middle' }]) {
    const opened = context.waitForEvent('page'); await parent.page.locator('#new').click(options);
    const page = await opened; await page.waitForLoadState();
    const meta = [...session.pages.values()].find(meta => meta.page === page);
    assert.ok(meta); await meta.ownershipReady; await meta.linkNavigationReady;
    assert.equal(await page.opener(), null, 'native modifier creation has no Playwright popup attribution');
    assert.equal(meta.control, 'human'); assert.equal(meta.lease, null); assert.equal(meta.controlEpoch, 0);
    children.push(meta);
  }
  const active = session.active, target = children[0], sibling = children[1], exactPage = target.page;
  const granted = await session.setControl({ mode: 'human', tab_id: target.id, context_ref: session.contextRef,
    generation: session.generation, control_epoch: 0, adopt_if_unowned: true });
  assert.equal(granted.control, 'human'); assert.equal(granted.control_epoch, 1); assert.ok(granted.lease_token);
  assert.notEqual(granted.lease_token, owner.lease_token);
  assert.equal(target.page, exactPage); assert.equal(session.active, active);
  assert.equal(sibling.control, 'human'); assert.equal(sibling.lease, null); assert.equal(sibling.controlEpoch, 0);
  assert.equal(parent.lease, owner.lease_token, 'source owner never rotates');
  const binding = { tab_id: target.id, context_ref: session.contextRef, generation: session.generation,
    control_epoch: granted.control_epoch, lease_token: granted.lease_token };
  await session.dispatchLiveInput(binding, async meta => { await meta.page.locator('#text').click(); await meta.page.keyboard.insertText('中文输入顺畅'); });
  assert.equal(await target.page.locator('#text').inputValue(), '中文输入顺畅');
  assert.equal(await sibling.page.locator('#text').inputValue(), ''); assert.equal(await parent.page.locator('#text').inputValue(), '');
  const range = await target.page.locator('#range').boundingBox();
  await session.dispatchLiveInput(binding, async meta => {
    await meta.page.mouse.move(range.x + 8, range.y + range.height / 2); await meta.page.mouse.down();
    await meta.page.mouse.move(range.x + range.width - 8, range.y + range.height / 2, { steps: 4 }); await meta.page.mouse.up();
  });
  assert.ok(Number(await target.page.locator('#range').inputValue()) >= 90, 'native dragging uses the selected real Page');
  await assert.rejects(session.dispatchLiveInput({ ...binding, tab_id: sibling.id }, () => assert.fail('unassigned sibling must not dispatch')), error => error.status === 403);
  assert.equal(session.active, active);
});
