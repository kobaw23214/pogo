import test from 'node:test';
import assert from 'node:assert/strict';
import { BrowserSession } from '../src/session.js';

const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

// Exercise the real transaction, binding and collection code. Only browser I/O
// is replaced, so these cases need neither Chromium nor a Wayland compositor.
function fixture() {
  const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} }, { actionTimeout: 2000, settleMs: 0 });
  session.devtools = { attach: async () => {}, isPaused: () => false, isBlocked: () => false };
  const navigations = [], reads = [];
  function addPage(control = 'agent', controlEpoch = 0) {
    const id = `t${session.pages.size + 1}`;
    let url = 'about:blank';
    const page = { url: () => url, isClosed: () => false, viewportSize: () => ({ width: 1280, height: 800 }),
      async goto(next) { navigations.push({ id, url: next }); url = next; } };
    const meta = { id, page, title: id, version: 1, visual: 0, control, controlEpoch, controlPhase: 'ready', devtoolsReady: Promise.resolve() };
    session.pages.set(id, meta); return meta;
  }
  addPage(); session.active = 't1';
  session.createPage = async () => addPage().page;
  session.snapshot = async (_query, observation) => {
    const meta = session.observationPage(observation);
    reads.push(meta.id);
    return { ok: true, text: `Observed ${meta.id}`, url: meta.page.url(), title: meta.title };
  };
  const binding = meta => ({ tab_id: meta.id, context_ref: session.contextRef, control_epoch: meta.controlEpoch });
  return { session, addPage, binding, navigations, reads };
}

test('eight openings are bounded to three concurrent navigations, retain partial results and never replay', { timeout: 5000 }, async () => {
  const h = fixture(), gate = deferred(), firstWave = deferred();
  let active = 0, peak = 0, started = 0;
  const create = h.session.createPage;
  h.session.createPage = async () => {
    const page = await create(), navigate = page.goto;
    page.goto = async url => {
      active++; peak = Math.max(peak, active);
      if (++started === 3) firstWave.resolve();
      try { await gate.promise; await navigate(url); if (url.endsWith('/fail')) throw new Error('fixture navigation refused'); }
      finally { active--; }
    };
    return page;
  };
  const action = { op_id: 'open-eight', action: 'open_pages', urls: Array.from({ length: 8 }, (_, i) => `https://fixture.invalid/${i === 3 ? 'fail' : i}`) };
  const pending = h.session.execute(action);
  try {
    await firstWave.promise;
    assert.equal(started, 3, 'remaining pages cannot navigate before a worker becomes available');
  } finally { gate.resolve(); }
  const result = await pending;
  assert.equal(result.ok, true);
  assert.equal(peak, 3);
  assert.deepEqual(result.data.coverage, { requested: 8, observed: 7, failed: 1, pending: 0, skipped: 0 });
  assert.equal(result.data.pages[3].status, 'failed');
  assert.ok(result.data.pages[3].tab_id, 'a failed navigation retains its concrete new tab');
  assert.equal(h.session.pages.size, 9);
  assert.equal(h.session.active, 't1', 'collection creation must not change the Agent target');
  assert.equal(h.navigations.length, 8);
  assert.deepEqual(await h.session.execute(action), result);
  assert.equal(h.navigations.length, 8, 'same operation ID cannot reopen completed pages');
});

test('invalid collections fail whole before creating or navigating any page', async () => {
  const h = fixture(); let creates = 0;
  h.session.createPage = async () => { creates++; throw new Error('must not create'); };
  for (const [index, fields] of [
    { urls: Array.from({ length: 9 }, (_, i) => `https://fixture.invalid/${i}`) },
    { urls: ['https://fixture.invalid/a'], concurrency: 4 },
    { urls: ['https://fixture.invalid', 'https://fixture.invalid/'] },
    { urls: ['https://user:password@fixture.invalid/'] },
  ].entries()) {
    const result = await h.session.execute({ op_id: `invalid-${index}`, action: 'open_pages', ...fields });
    assert.equal(result.ok, false);
  }
  assert.equal(creates, 0); assert.deepEqual(h.navigations, []);
});

test('observing bound pages leaves the active human page alone and isolates stale or human bindings', async () => {
  const h = fixture(), agent = h.addPage(), human = h.addPage('human', 2), stale = h.addPage('agent', 4);
  h.session.pages.get('t1').control = 'human';
  const result = await h.session.execute({ op_id: 'bound-observations', action: 'observe_pages', pages: [
    h.binding(agent), h.binding(human), { ...h.binding(stale), control_epoch: 3 },
  ] });
  assert.equal(result.ok, true);
  assert.deepEqual(result.data.coverage, { requested: 3, observed: 1, failed: 2, pending: 0, skipped: 0 });
  assert.deepEqual(h.reads, [agent.id]);
  assert.equal(result.data.pages[0].text, `Observed ${agent.id}`);
  assert.equal(h.session.active, 't1');
  assert.equal(human.control, 'human'); assert.equal(human.controlEpoch, 2);
  assert.deepEqual(h.navigations, []);
});

test('an explicit action selects its Agent page without claiming a human page or accepting an old epoch', async () => {
  const h = fixture(), target = h.addPage('agent', 3), human = h.session.pages.get('t1');
  human.control = 'human'; human.controlEpoch = 1;
  const result = await h.session.execute({ op_id: 'bound-snapshot', action: 'snapshot', ...h.binding(target) });
  assert.equal(result.ok, true); assert.deepEqual(h.reads, [target.id]);
  assert.equal(result.active_tab, target.id); assert.equal(human.control, 'human');
  await assert.rejects(h.session.execute({ op_id: 'old-binding', action: 'snapshot', ...h.binding(target), control_epoch: 2 }), /control changed/);
  await assert.rejects(h.session.execute({ op_id: 'human-binding', action: 'snapshot', ...h.binding(human) }), /TAB_HUMAN_CONTROL/);
  assert.deepEqual(h.reads, [target.id], 'refused bindings cannot dispatch a read');
});

test('a takeover during one collection read discards that page text while preserving its sibling', async () => {
  const h = fixture(), changed = h.addPage(), sibling = h.addPage(), entered = deferred(), release = deferred();
  const snapshot = h.session.snapshot;
  h.session.snapshot = async (query, observation) => {
    if (h.session.observationPage(observation) === changed) { entered.resolve(); await release.promise; }
    return snapshot(query, observation);
  };
  const pending = h.session.execute({ op_id: 'takeover-during-read', action: 'observe_pages', pages: [h.binding(changed), h.binding(sibling)] });
  await entered.promise;
  changed.control = 'human'; changed.controlEpoch++;
  release.resolve();
  const result = await pending;
  assert.equal(result.ok, true);
  assert.equal(result.data.pages[0].ok, false);
  assert.equal(result.data.pages[0].text, undefined, 'late text cannot escape after authority changes');
  assert.equal(result.data.pages[1].text, `Observed ${sibling.id}`);
  assert.equal(h.session.active, 't1');
});

test('cancellation while a new page arrives prevents its late navigation', async () => {
  const h = fixture(), entered = deferred(), release = deferred();
  h.session.createPage = async () => { entered.resolve(); await release.promise; return h.addPage().page; };
  const pending = h.session.execute({ op_id: 'cancel-opening', action: 'open_pages', urls: ['https://fixture.invalid/late'] });
  await entered.promise;
  h.session.generation++;
  release.resolve();
  const result = await pending;
  assert.equal(result.ok, false);
  assert.deepEqual(h.navigations, [], 'a late page may not navigate under a cancelled operation');
  assert.equal(h.session.rawAction, null);
  assert.equal(h.session.active, 't1');
});
