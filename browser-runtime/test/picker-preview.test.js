import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';
import { createWorker } from '../src/server.js';

let browser, site, origin, childOrigin, mutations = 0;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(read, label = 'condition', timeout = 4000) {
  const end = Date.now() + timeout;
  do { const value = await read(); if (value) return value; await delay(15); } while (Date.now() < end);
  assert.fail(label + ' was not observed');
}
before(async () => {
  site = http.createServer((request, response) => {
    if (request.url === '/write') { mutations++; response.end('saved'); return; }
    response.setHeader('Content-Type', 'text/html');
    if (request.url === '/padding-child') { response.end('<style>body{margin:0}button{position:absolute;top:0;height:60px;border:0;padding:0}#a{left:0;width:30px;background:red}#b{left:30px;width:100px;background:blue}</style><button id="a">A</button><button id="b">B</button>'); return; }
    if (request.url === '/child') { response.end('<button style="position:absolute;left:10px;top:10px;width:180px;height:40px">Child label</button>'); return; }
    const count = request.url === '/large' ? 21000 : request.url === '/ten-thousand' ? 10000 : 0;
    response.end(`<!doctype html><title>Preview fixture</title><style>body{margin:0;min-height:1800px}#pick,#secret{position:absolute;left:20px;width:180px;height:40px}#pick{top:20px}#secret{top:90px}#host{position:absolute;left:320px;top:20px;width:240px;height:200px}iframe{position:absolute;left:650px;top:20px;width:280px;height:160px;border:2px solid}</style><button id="pick" onclick="fetch('/write')">Visible business label</button><input id="secret" aria-label="Password name" type="password" value="private-form-secret"><div id="host"></div><iframe src="${childOrigin}/child"></iframe><div hidden>${'<span></span>'.repeat(count)}</div><script>for(const type of ['pointerdown','pointermove','mousedown','click','focusin','submit'])document.addEventListener(type,()=>fetch('/write'));document.querySelector('#host').attachShadow({mode:'open'}).innerHTML='<button id="shadow" style="position:absolute;left:10px;top:10px;width:180px;height:40px">Shadow label</button>';</script>`);
  });
  await new Promise(resolve => site.listen(0, resolve));
  origin = `http://127.0.0.1:${site.address().port}`; childOrigin = `http://localhost:${site.address().port}`;
  browser = await chromium.launch({ headless: true, args: ['--site-per-process'] });
});
after(async () => { await browser?.close(); site?.closeAllConnections(); if (site) await new Promise(resolve => site.close(resolve)); });

async function fixture(path = '/') {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const session = await new BrowserSession(context, { actionTimeout: 2000, settleMs: 0 }).init();
  const page = context.pages()[0];
  await page.goto(origin + path);
  const fresh = async (x = 50, y = 40) => {
    const frame = await until(async () => { try { const result = await session.frame(); return result.version ? result : null; } catch (error) { if (error.status !== 409) throw error; } }, 'strict frame');
    return { x, y, context_ref: session.contextRef, tab_id: session.active, version: frame.version };
  };
  return { session, page, context, fresh, point: await fresh() };
}

test('10k-node preview reads only fast revision and lightweight geometry; only strict commit creates a reference', async () => {
  const f = await fixture('/ten-thousand');
  try {
    const before = mutations, calls = [], original = f.session.refreshRevision.bind(f.session);
    f.session.refreshRevision = (...args) => { calls.push(args[2]); return original(...args); };
    const preview = await f.session.picker.preview(f.point);
    assert.deepEqual(calls, [{ fast: true, unbounded: true }, { fast: true, unbounded: true }]);
    assert.deepEqual(Object.keys(preview.element).sort(), ['rect', 'role', 'tag']);
    assert.equal(preview.element.role, 'button'); assert.equal(f.session.picker.nextRef, 0);
    assert.match(preview.preview_token, /^pv_[a-f0-9]{48}$/); assert.equal(preview.expires_in_ms, 3000);
    assert.ok(!JSON.stringify(preview).includes('business label')); assert.ok(!JSON.stringify(preview).includes('private-form-secret'));
    assert.equal(preview.element.rect.x, 20); assert.equal(preview.element.rect.width, 180);
    const committed = await f.session.picker.pick({ ...f.point, preview_token: preview.preview_token });
    assert.match(committed.element.ref, /^p\d+$/); assert.equal(committed.element.name, 'Visible business label');
    assert.equal(calls.filter(options => !options.fast).length, 2, 'commit keeps both complete revision validations');
    assert.equal(mutations, before, 'preview and commit dispatch no site input');
    await assert.rejects(f.session.picker.pick({ ...f.point, preview_token: preview.preview_token }), error => error.status === 409);
    const field = await f.session.picker.preview({ ...f.point, y: 110 });
    assert.equal(field.element.role, 'textbox'); assert.ok(!JSON.stringify(field).includes('Password name'));
    const empty = await f.session.picker.preview({ ...f.point, x: 1100, y: 700 });
    assert.equal(empty.element, null); assert.equal(empty.preview_token, undefined);
  } finally { await f.session.close(); }
});

test('preview capabilities bind exact coordinates, node identity, page version, session and short bounded retention', async () => {
  const f = await fixture(), other = await fixture();
  try {
    let preview = await f.session.picker.preview(f.point);
    await assert.rejects(f.session.picker.pick({ ...f.point, x: f.point.x + 1, preview_token: preview.preview_token }), error => error.status === 409);
    preview = await f.session.picker.preview(f.point);
    await assert.rejects(other.session.picker.pick({ ...other.point, preview_token: preview.preview_token }), error => error.status === 409);
    await f.page.evaluate(() => { const element = document.querySelector('#pick'); element.replaceWith(element.cloneNode(true)); });
    await assert.rejects(f.session.picker.pick({ ...f.point, preview_token: preview.preview_token }), error => error.status === 409);
    const fresh = await f.fresh(); preview = await f.session.picker.preview(fresh);
    f.session.picker.previews.get(preview.preview_token).expires = Date.now() - 1;
    await assert.rejects(f.session.picker.pick({ ...fresh, preview_token: preview.preview_token }), error => error.status === 409);
    const first = await f.session.picker.preview(fresh);
    for (let index = 0; index < 33; index++) await f.session.picker.preview(fresh);
    assert.equal(f.session.picker.previews.size, 32); assert.equal(f.session.picker.previews.has(first.preview_token), false);
    preview = await f.session.picker.preview(fresh);
    // Even if an unrelated stale-revision bug were introduced, the actual
    // replacement node must not become the identity certified by this token.
    await f.page.evaluate(() => { const element = document.querySelector('#pick'); element.replaceWith(element.cloneNode(true)); });
    f.session.refreshRevision = async () => f.session.version();
    await assert.rejects(f.session.picker.pick({ ...fresh, preview_token: preview.preview_token }), /no longer matches/);
  } finally { await f.session.close(); await other.session.close(); }
});

test('preview handles open shadow and real cross-origin iframe geometry without exposing source text', async () => {
  const f = await fixture();
  try {
    for (const point of [{ ...f.point, x: 350, y: 45 }, { ...f.point, x: 690, y: 50 }]) {
      const preview = await f.session.picker.preview(point);
      assert.equal(preview.element.tag, 'button'); assert.ok(!JSON.stringify(preview).includes('label'));
      const commit = await f.session.picker.pick({ ...point, preview_token: preview.preview_token });
      assert.equal(commit.element.ref.startsWith('p'), true);
      assert.deepEqual(preview.element.rect, commit.element.rect, 'preview and strict iframe coordinates agree');
    }
    await f.page.locator('iframe').evaluate(el => {
      for (const name of ['clientLeft', 'clientTop', 'clientWidth', 'clientHeight']) Object.defineProperty(el, name, { get() { fetch('/write'); return 99999; } });
    });
    const before = mutations, guardedPoint = await f.fresh(690, 50);
    const guarded = await f.session.picker.preview(guardedPoint);
    assert.equal(guarded.element.rect.x, 662);
    assert.deepEqual((await f.session.picker.pick({ ...guardedPoint, preview_token: guarded.preview_token })).element.rect, guarded.element.rect);
    assert.equal(mutations, before, 'preview/commit use captured native iframe geometry getters');
    await f.page.locator('iframe').evaluate(el => { el.style.transform = 'scale(.8)'; el.style.transformOrigin = 'top left'; });
    const point = await f.fresh(690, 50), preview = await f.session.picker.preview(point);
    assert.equal(preview.element.tag, 'iframe'); assert.equal(preview.element.fallback, 'frame');
    assert.equal((await f.session.picker.pick({ ...point, preview_token: preview.preview_token })).element.fallback, 'frame');
  } finally { await f.session.close(); }
});

test('incremental discovery covers existing-host attachShadow, nested roots, and same-turn MutationObserver records', async () => {
  const f = await fixture();
  try {
    await f.page.evaluate(() => { const host = document.createElement('div'); host.id = 'newhost'; host.style.cssText = 'position:absolute;left:320px;top:300px;width:220px;height:100px'; document.body.append(host); });
    const beforeRoot = await f.fresh(360, 330);
    await f.page.evaluate(() => {
      const root = document.querySelector('#newhost').attachShadow({ mode: 'open' });
      const nested = document.createElement('div'); root.append(nested);
      nested.attachShadow({ mode: 'open' }).innerHTML = '<button style="width:180px;height:40px">Nested private label</button>';
    });
    await assert.rejects(f.session.picker.preview(beforeRoot), error => error.status === 409);
    const point = await f.fresh(360, 320), preview = await f.session.picker.preview(point);
    assert.equal(preview.element.role, 'button');
    assert.equal((await f.session.picker.pick({ ...point, preview_token: preview.preview_token })).element.name, 'Nested private label');
    const changedSynchronously = await f.page.evaluate(key => {
      const probe = globalThis[key], before = probe.fast();
      document.querySelector('#pick').textContent = 'Changed in same call';
      const after = probe.fast();
      return { changed: after > before, covered: probe.covered() };
    }, f.session.revisionKey);
    assert.deepEqual(changedSynchronously, { changed: true, covered: true }, 'takeRecords observes a change before callback delivery');
    await assert.rejects(f.session.picker.preview(point), error => error.status === 409);
  } finally { await f.session.close(); }
});

test('iframe padding uses the real content origin and cannot turn a point on A into adjacent B', async () => {
  const f = await fixture();
  try {
    await f.page.locator('iframe').evaluate((el, url) => {
      el.style.cssText = 'position:absolute;left:100px;top:80px;width:300px;height:150px;border:2px solid;padding:20px;box-sizing:content-box'; el.src = url;
    }, childOrigin + '/padding-child');
    await until(() => f.page.frames().find(frame => frame.url().endsWith('/padding-child')), 'padded iframe');
    const child = f.page.frames().find(frame => frame.url().endsWith('/padding-child'));
    await child.locator('#a').waitFor();
    // CSS content origin is (100+2+20, 80+2+20)=(122,102).
    // At (142,132), child x=20 is inside A[0,30), never B[30,130).
    const point = await f.fresh(142, 132);
    const preview = await f.session.picker.preview(point);
    assert.deepEqual(preview.element.rect, { x: 122, y: 102, width: 30, height: 60 });
    const commit = await f.session.picker.pick({ ...point, preview_token: preview.preview_token });
    assert.equal(commit.element.name, 'A'); assert.deepEqual(commit.element.rect, preview.element.rect);
    assert.equal((await f.session.picker.pick(point)).element.name, 'A', 'legacy strict path uses the same correct content box');
    const padding = await f.session.picker.preview({ ...point, x: 112, y: 112 });
    assert.equal(padding.element.tag, 'iframe'); assert.equal(padding.element.fallback, 'frame', 'padding belongs to the frame container');
  } finally { await f.session.close(); }
});

test('removed shadow roots release observers/listeners and reinsertion resumes observation without a full preview scan', async () => {
  const f = await fixture();
  try {
    const result = await f.page.evaluate(key => {
      const probe = globalThis[key];
      for (let index = 0; index < 600; index++) {
        const host = document.createElement('div'); document.body.append(host);
        host.attachShadow({ mode: 'open' }).innerHTML = '<span>transient</span>';
        host.remove(); probe.fast();
      }
      probe(); // Strict full validation may restore prior conservative coverage.
      const recovered = probe.covered();
      const host = document.createElement('div'); document.body.append(host);
      const root = host.attachShadow({ mode: 'open' }); root.innerHTML = '<span>reinserted</span>';
      probe.fast(); const attached = probe.observes(root);
      host.remove(); probe.fast(); const removed = !probe.observes(root);
      document.body.append(host); probe.fast(); const reinserted = probe.observes(root);
      const before = probe.fast(); root.querySelector('span').textContent = 'changed'; const changed = probe.fast() > before;
      host.remove(); probe.fast();
      return { recovered, attached, removed, reinserted, changed };
    }, f.session.revisionKey);
    assert.deepEqual(result, { recovered: true, attached: true, removed: true, reinserted: true, changed: true });
    assert.ok((await f.session.picker.preview(await f.fresh())).preview_token);
  } finally { await f.session.close(); }
});

test('scroll, viewport, font events, CSSOM changes, animations and iframe navigation invalidate the displayed preview version', async () => {
  const f = await fixture();
  try {
    const change = async work => {
      const before = await f.fresh();
      await work(); await delay(35);
      await assert.rejects(f.session.picker.preview(before), error => error.status === 409);
    };
    await change(() => f.page.evaluate(() => window.scrollTo(0, 50)));
    await f.page.evaluate(() => window.scrollTo(0, 0)); await delay(35);
    await change(() => f.page.setViewportSize({ width: 1400, height: 900 }));
    await change(() => f.page.evaluate(() => document.fonts.dispatchEvent(new Event('loadingdone'))));
    await change(() => f.page.evaluate(() => document.styleSheets[0].insertRule('#pick { left: 21px }', 0)));
    await change(() => f.page.evaluate(() => document.styleSheets[0].cssRules[0].style.setProperty('left', '22px')));
    await change(() => f.page.evaluate(() => { globalThis.testAnimation = document.querySelector('#pick').animate([{ transform: 'translateX(0)' }, { transform: 'translateX(50px)' }], { duration: 10000 }); }));
    const animated = await f.fresh();
    await assert.rejects(f.session.picker.preview(animated), /animating/);
    await f.page.evaluate(() => globalThis.testAnimation.cancel());
    await change(() => f.page.locator('iframe').evaluate(el => { el.src += '?new=1'; }));
  } finally { await f.session.close(); }
});

test('preview timeout retains actual single-flight until its renderer read drains, without late token/ref publication', async () => {
  const f = await fixture();
  try {
    const original = f.session.picker.previewHit.bind(f.session.picker);
    let release, calls = 0;
    f.session.picker.previewHit = () => { calls++; return new Promise(resolve => { release = resolve; }); };
    await assert.rejects(f.session.picker.preview(f.point), error => error.status === 504 && error.code === 'PICK_OBSERVATION_PENDING');
    assert.equal(f.session.state().pick_pending, true);
    await assert.rejects(f.session.picker.preview(f.point), error => error.status === 409 && error.code === 'PICK_OBSERVATION_PENDING');
    await assert.rejects(f.session.picker.pick(f.point), error => error.status === 409 && error.code === 'PICK_OBSERVATION_PENDING');
    assert.equal(calls, 1); assert.equal(f.session.picker.nextRef, 0); assert.equal(f.session.picker.previews.size, 0);
    release(null);
    await until(() => !f.session.state().pick_pending, 'preview read drain');
    f.session.picker.previewHit = original;
    assert.ok((await f.session.picker.preview(f.point)).preview_token);
  } finally { await f.session.close(); }
});

test('strict commit timeout also retains a stalled revision evaluation until its actual promise ends', async () => {
  const f = await fixture();
  try {
    const frame = f.page.mainFrame(), original = frame.evaluate.bind(frame);
    let release, call;
    frame.evaluate = (fn, arg) => { call = [fn, arg]; return new Promise(resolve => { release = resolve; }); };
    await assert.rejects(f.session.picker.pick(f.point), error => error.status === 504 && error.code === 'PICK_OBSERVATION_PENDING');
    await delay(180);
    assert.equal(f.session.state().pick_pending, true, 'an internal observation timeout must not release the picker lock');
    await assert.rejects(f.session.picker.preview(f.point), error => error.code === 'PICK_OBSERVATION_PENDING');
    frame.evaluate = original;
    release(await original(...call));
    await until(() => !f.session.state().pick_pending, 'strict revision read drain');
    assert.equal(f.session.picker.nextRef, 0);
  } finally { await f.session.close(); }
});

test('uncovered discovery fails closed, and the HTTP preview route requires normal worker authentication', async () => {
  const f = await fixture('/large');
  let worker;
  try {
    await assert.rejects(f.session.picker.preview(f.point), /coverage/);
    const token = 'preview-route-fixture'; worker = await createWorker({ token, browser, settleMs: 0 });
    const base = `http://127.0.0.1:${(await worker.listen(0)).port}/sessions/preview-route`;
    const request = async (suffix, body, bearer = token, method = 'POST') => {
      const response = await fetch(base + suffix, { method, headers: { Authorization: `Bearer ${bearer}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
      return { status: response.status, body: await response.json() };
    };
    const created = await request('', {}, token, 'PUT');
    await request('/execute', { op_id: 'navigate', action: 'navigate', url: origin + '/' });
    const frame = await fetch(base + '/frame', { headers: { Authorization: `Bearer ${token}` } });
    const body = { x: 50, y: 40, context_ref: created.body.context_ref, tab_id: created.body.active_tab, version: frame.headers.get('x-browser-version') };
    assert.equal((await request('/pick/preview', body, 'wrong')).status, 401);
    const result = await request('/pick/preview', body);
    assert.equal(result.status, 200, JSON.stringify(result)); assert.match(result.body.preview_token, /^pv_/);
    const committed = await request('/pick', { ...body, preview_token: result.body.preview_token });
    assert.equal(committed.status, 200); assert.match(committed.body.element.ref, /^p/);
    assert.equal((await request('/pick', body)).status, 200, 'legacy no-token strict observation remains available');
  } finally { await worker?.close(); await f.session.close(); }
});
