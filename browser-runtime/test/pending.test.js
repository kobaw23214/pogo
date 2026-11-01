import test from 'node:test';
import assert from 'node:assert/strict';
import { BrowserSession } from '../src/session.js';
import { chromium } from 'playwright';

function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}
function session() {
  const s = new BrowserSession({ setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, setOffline: async () => {}, on() {} }, { actionTimeout: 1 });
  s.control = 'human';
  s.lease = 'test-lease';
  s.active = 't1';
  s.pages.set('t1', { id: 't1', page: { isClosed: () => false, url: () => 'https://fixture.invalid' }, version: 1 });
  return s;
}
const input = s => s.input({ type: 'key', key: 'Enter', lease_token: 'test-lease' });

test('uncertain human input keeps its promise and fences both queued and subsequent input', async () => {
  const s = session();
  const pending = deferred();
  let started = 0;
  s.action = () => { started++; return pending.promise; };
  try {
    // Both enter before the first queue task starts: the second must recheck
    // the unresolved raw action after the first deadline expires.
    const first = input(s);
    const queued = input(s);
    const results = await Promise.allSettled([first, queued]);
    assert.deepEqual(results.map(r => r.reason?.status), [504, 504]);
    assert.equal(s.rawAction, pending.promise);
    await assert.rejects(input(s), error => error.status === 504);
    assert.equal(started, 1);
    pending.resolve({ ok: true });
    await s.rawAction;
    await input(s);
    assert.equal(started, 2);
  } finally { pending.resolve({ ok: true }); }
});

test('dialog handling tracks the answer and its suspended click until both complete', async () => {
  const s = session();
  const click = deferred();
  const answer = deferred();
  s.rawAction = click.promise;
  s.dialog = { value: { accept: () => answer.promise } };
  const handling = s.input({ type: 'dialog', accept: true, lease_token: 'test-lease' });
  await Promise.resolve();
  await Promise.resolve();
  const tracked = s.rawAction;
  assert.notEqual(tracked, click.promise);
  answer.resolve();
  await Promise.resolve();
  await assert.rejects(input(s), error => error.status === 504);
  assert.equal(s.rawAction, tracked);
  click.resolve({ ok: true });
  assert.equal((await handling).ok, true);
  assert.equal(s.rawAction, null);
});

test('closed marker does not acknowledge cleanup; failed context close can be retried', async () => {
  const s = session();
  s.closed = true;
  let attempts = 0;
  const pending = deferred();
  const retryStarted = deferred();
  s.context.close = () => {
    if (++attempts === 1) return Promise.reject(new Error('close failed'));
    retryStarted.resolve();
    return pending.promise;
  };
  await assert.rejects(s.close(), /close failed/);
  const first = s.close();
  const duplicate = s.close();
  await retryStarted.promise;
  assert.equal(attempts, 2);
  pending.resolve();
  await Promise.all([first, duplicate]);
});

test('confirmed context destruction permits cleanup retry after network isolation failed', async () => {
  const s = session();
  let offlineAttempts = 0, closes = 0;
  s.context.setOffline = async () => { offlineAttempts++; throw new Error('offline unavailable'); };
  s.context.close = async () => { closes++; };
  await assert.rejects(s.close(), /offline unavailable/);
  assert.equal(s.contextDestroyed, true, 'only a successful context close confirms destruction');
  await s.close();
  assert.equal(offlineAttempts, 1, 'do not require a command on an already destroyed context');
  assert.equal(closes, 2, 'the idempotent close acknowledgement is still obtained');
});

test('real external and concurrent Chromium context destruction are acknowledged without hiding termination failures', async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const concurrent of [false, true]) {
      const context = await browser.newContext();
      const s = await new BrowserSession(context, { actionTimeout: 1000 }).init();
      let offlineAttempts = 0;
      const originalOffline = context.setOffline.bind(context);
      context.setOffline = async offline => {
        offlineAttempts++;
        if (concurrent) await context.close();
        return originalOffline(offline);
      };
      if (!concurrent) {
        await context.close();
        assert.equal(s.contextDestroyed, true, 'the public context close event is authoritative');
      }
      await s.close();
      assert.equal(s.contextDestroyed, true);
      assert.equal(offlineAttempts, concurrent ? 1 : 0);
    }
  } finally { await browser.close(); }

  const s = session();
  s.context.close = async () => {};
  s.devtools.prepareClose = options => {
    if (options?.terminatePaused === false) return;
    throw new Error('DEVTOOLS_CANCEL_TERMINATION_FAILED');
  };
  await assert.rejects(s.close(), /DEVTOOLS_CANCEL_TERMINATION_FAILED/);
  assert.equal(s.contextDestroyed, true, 'confirmed destruction must not hide failed paused-execution termination');
});

test('unconfirmed cancellation closes the session instead of leaving an unrecoverable busy state', async () => {
  const s = session();
  const pending = deferred();
  s.queue = pending.promise;
  let closes = 0;
  s.context.close = async () => { closes++; };
  try {
    await assert.rejects(s.cancel(), error => error.status === 503);
    assert.throws(() => s.assertLive(), error => error.status === 410);
    assert.equal(closes, 1);
  } finally { pending.resolve(); }
});

test('viewer polls share unresolved telemetry reads and a late sample cannot cross a control fence', async () => {
	const s = session();
	const pending = deferred();
	let reads = 0;
	const main = { evaluate: () => { reads++; return pending.promise; } };
	Object.assign(s.pages.get('t1'), { id: 't1', visual: 1 });
	Object.assign(s.pages.get('t1').page, { mainFrame: () => main, frames: () => [] });
	s.activity.begin({ action: 'click' }, 'human');
	await Promise.all([s.activity.overlay(s.version()), s.activity.overlay(s.version())]);
	await s.activity.overlay(s.version());
	assert.equal(reads, 1, 'polling must not accumulate unresolved page evaluations');
	const stale = s.activity.capturePointer();
	s.generation++;
	s.activity.fence();
	pending.resolve({ x: 123, y: 45, seq: 1, kind: 'click', at: Date.now() });
	await stale;
	assert.equal(s.activity.pointer, null, 'late pre-takeover pointer must not reappear');
});

test('only mouse actions claim pointer events and their source document must still match', async () => {
  const s = session(), meta = s.pages.get('t1');
  let observed = null;
  const main = { evaluate: async () => observed };
  Object.assign(meta, { id: 't1', visual: 1 });
  Object.assign(meta.page, { mainFrame: () => main, frames: () => [] });
  const navigate = s.activity.begin({ action: 'navigate' }, 'agent');
  observed = { x: 0, y: 0, seq: 1, kind: 'move', at: navigate.record.started_at };
  await s.activity.finish(navigate, true);
  assert.equal(s.activity.pointer, null, 'trusted initialization move is not agent navigation input');

  const click = s.activity.begin({ action: 'click' }, 'human');
  observed = { x: 100, y: 80, seq: 2, kind: 'click', at: click.record.started_at };
  await s.activity.finish(click, true);
  assert.equal(s.activity.pointer.value.actor, 'human');
  assert.equal(s.activity.pointer.value.kind, 'click');
  const pointer = structuredClone(s.activity.pointer);
  for (const action of ['type', 'press', 'input_text']) {
    const handle = s.activity.begin({ action }, 'agent');
    observed = { x: 0, y: 0, seq: ++observed.seq, kind: 'move', at: handle.record.started_at };
    await s.activity.finish(handle, true);
    assert.deepEqual(s.activity.pointer, pointer, `${action} must preserve the last real mouse input`);
  }
  const hover = s.activity.begin({ action: 'hover' }, 'agent');
  observed = { x: 120, y: 90, seq: ++observed.seq, kind: 'move', at: hover.record.started_at };
  await s.activity.finish(hover, true);
  assert.equal(s.activity.pointer.value.action_seq, hover.record.seq);
  assert.equal(s.activity.pointer.value.actor, 'agent');
  assert.equal(s.activity.pointer.value.kind, 'move');
  const moved = structuredClone(s.activity.pointer);
  const navigatingClick = s.activity.begin({ action: 'click' }, 'human');
  meta.version++;
  observed = { x: 0, y: 0, seq: 1, kind: 'move', at: navigatingClick.record.started_at };
  s.activity.coordinateClick(500, 400, s.generation);
  await s.activity.finish(navigatingClick, true);
  assert.deepEqual(s.activity.pointer, moved, 'neither click completion nor initialization may claim the next document');
  assert.equal((await s.activity.overlay(s.version())).pointer, undefined);
});

test('pick timeout keeps one outstanding read and never cancels or closes the page', async () => {
  const s = session();
  const pending = deferred();
  let reads = 0, cancellations = 0;
  const main = { evaluateHandle: () => { reads++; return pending.promise; } };
  Object.assign(s.pages.get('t1'), { id: 't1', visual: 1, title: '' });
  Object.assign(s.pages.get('t1').page, { mainFrame: () => main });
  s.refreshRevision = async () => s.version();
  s.cancel = async () => { cancellations++; };
  const body = { x: 10, y: 10, tab_id: 't1', version: s.version() };
  try {
    await assert.rejects(s.picker.pick(body), error => error.status === 504);
    await assert.rejects(s.picker.pick(body), error => error.status === 409);
    assert.equal(reads, 1);
    assert.equal(cancellations, 0);
    assert.equal(s.closed, false);
  } finally {
    pending.resolve({ asElement: () => null, dispose: async () => {} });
    await s.picker.pending.values().next().value.catch(() => {});
  }
});
