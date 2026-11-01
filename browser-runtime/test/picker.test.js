import test, { before, after, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';
let worker, site, origin, base, port, events = 0, op = 0;
const token = 'picker-test-only';
const ownedSessions = new Set();
const delay = ms => new Promise(r => setTimeout(r, ms));
async function request(path, method = 'GET', body, auth = token) {
  const r = await fetch(base + path, { method, headers: { Authorization: `Bearer ${auth}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const data = r.headers.get('content-type')?.includes('json') ? await r.json() : Buffer.from(await r.arrayBuffer());
  return { status: r.status, data, headers: r.headers };
}
const execute = (id, args) => request(`/sessions/${id}/execute`, 'POST', { op_id: `pick-${++op}`, ...args });
const pick = (id, body) => request(`/sessions/${id}/pick`, 'POST', body);
async function open(id, path = '/') {
  ownedSessions.add(id);
  const created = await request(`/sessions/${id}`, 'PUT');
  assert.equal(created.status, 200, JSON.stringify(created.data));
  assert.equal((await execute(id, { action: 'navigate', url: origin + path })).data.ok, true);
  let image;
  for (let tries = 0; tries < 8; tries++) {
    image = await request(`/sessions/${id}/frame`);
    if (image.status !== 409) break;
    await delay(40); // Observation retry only: startup resource/layout events.
  }
  assert.equal(image.status, 200, JSON.stringify(image.data));
  const state = (await request(`/sessions/${id}/state`)).data;
  return { x: 50, y: 40, version: image.headers.get('x-browser-version'), tab_id: state.active_tab };
}
before(async () => {
  site = http.createServer((req, res) => {
    if (req.url === '/event') { events++; res.end('ok'); return; }
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    if (req.url === '/large') { res.end('<!doctype html><button>Large page</button><div hidden>' + '<span></span>'.repeat(20010) + '</div>'); return; }
    if (req.url === '/child') { res.end('<!doctype html><button style="position:absolute;left:10px;top:10px;width:180px;height:40px">Cross-origin child</button>'); return; }
    res.end(`<!doctype html><title>Picker fixture</title><style>
    button,input,textarea,[contenteditable]{position:absolute;left:20px;width:180px;height:40px;box-sizing:border-box}
    #submit{top:20px}#password{top:90px}#area{top:145px}#edit{top:200px}#animated{top:265px;animation:pulse 1s infinite}
    @keyframes pulse{from{opacity:1}to{opacity:.9}}iframe{position:absolute;left:600px;top:20px;width:300px;height:180px;border:2px solid}
    #scaled{top:250px;transform:scale(.8);transform-origin:top left}#host{position:absolute;left:300px;top:20px;width:220px;height:100px}
    #group{position:absolute;top:500px;left:20px;width:500px;height:120px;background:#eee}#group input,#group textarea{position:static;height:20px}p1{position:absolute;top:700px}
    </style><button id="submit" onclick="fetch('/event')">Submit only if clicked</button>
    <input id="password" aria-label="Password" type="password" value="password-secret"><textarea id="area" aria-label="Notes">textarea-secret</textarea><div id="edit" contenteditable>editable-secret</div>
    <button id="animated">Animated</button><div id="host"></div><iframe src="http://localhost:${port}/child"></iframe><iframe id="scaled" src="http://localhost:${port}/child"></iframe>
    <div id="group">Visible label<input value="nested-secret"><textarea>nested-area-secret</textarea><span hidden>hidden-secret</span><details><summary>Summary only</summary>details-secret</details><div style="content-visibility:hidden">content-secret</div><div style="display:contents">Visible contents</div></div>
    <p1 onclick="fetch('/event')">Real p1 element</p1><script>
    for(const name of ['pointerdown','mousedown','click','focusin','submit'])document.addEventListener(name,()=>fetch('/event'));
    window.__pi_pick_data__=()=>{fetch('/event');return {private:'hook-secret'}};
    const root=document.querySelector('#host').attachShadow({mode:'open'});root.innerHTML='<button style="position:absolute;left:10px;top:10px;width:180px;height:40px">Shadow target</button>';
    if(location.pathname==='/shadow-change')setTimeout(()=>root.querySelector('button').textContent='Shadow changed',700);
    if(location.pathname==='/dom-change')setTimeout(()=>document.querySelector('#submit').style.top='65px',700);
    if(location.pathname==='/cover-animation'){
      const cover=document.createElement('div');cover.style='position:absolute;left:0;top:0;width:220px;height:80px;background:white;pointer-events:none';document.body.append(cover);
      cover.animate([{transform:'translateX(0)'},{transform:'translateX(300px)'}],{duration:650,fill:'forwards'});
    }
    if(location.pathname==='/override'){
      for(const [prototype,name] of [[Element.prototype,'getAttribute'],[Element.prototype,'getBoundingClientRect'],[Element.prototype,'getAnimations'],[Node.prototype,'getRootNode'],[Document.prototype,'elementFromPoint'],[Document.prototype,'getElementById'],[Document.prototype,'createRange'],[Range.prototype,'selectNodeContents'],[Range.prototype,'getClientRects']]){
        const original=prototype[name];prototype[name]=function(...args){fetch('/event');return Reflect.apply(original,this,args)};
      }
      const original=getComputedStyle;window.getComputedStyle=function(...args){fetch('/event');return Reflect.apply(original,window,args)};
    }
    </script>`);
  });
  await new Promise(r => site.listen(0, '127.0.0.1', r)); port = site.address().port; origin = `http://127.0.0.1:${port}`;
  worker = await createWorker({ token, actionTimeout: 2000 }); base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => { await worker?.close(); site?.closeAllConnections(); if(site) await new Promise(r => site.close(r)); });
afterEach(async () => {
  // Each test owns its sessions; tests that compare two live sessions retain
  // both until this hook. No native process may accumulate into the next case.
  const failures = [];
  for (const id of ownedSessions) {
    try {
      const removed = await request(`/sessions/${id}`, 'DELETE');
      if (![200, 404].includes(removed.status)) failures.push(`${id}: ${JSON.stringify(removed.data)}`);
    } catch (error) { failures.push(`${id}: ${error.message}`); }
  }
  ownedSessions.clear();
  assert.deepEqual(failures, [], 'every owned fixture session must confirm cleanup');
});

test('pick has no input side effects, stable pN identity and leaves agent refs usable', async () => {
  const point = await open('pure'), before = events;
  const one = await pick('pure', point), two = await pick('pure', point);
  assert.equal(one.status, 200, JSON.stringify(one.data)); assert.equal(one.data.element.role, 'button'); assert.match(one.data.element.ref, /^p\d+$/);
  assert.equal(two.data.element.ref, one.data.element.ref); assert.equal(events, before);
  assert.equal((await request('/sessions/pure/state')).data.control, 'agent');
  assert.equal((await request('/sessions/pure/frame')).headers.get('x-browser-version'), point.version);
  const snapshot = await execute('pure', { action: 'snapshot' });
  const ref = /\[(e\d+)\] button "Submit only if clicked"/.exec(snapshot.data.text)[1];
  await pick('pure', point); assert.equal((await execute('pure', { action: 'extract', target: ref })).data.ok, true);
  const bad = await execute('pure', { action: 'click', target: one.data.element.ref });
  assert.equal(bad.data.ok, false); assert.match(bad.data.error, /read-only.*snapshot or find/); assert.equal(events, before);
});
test('summaries omit form values and non-rendered text but preserve display:contents', async () => {
  const point = await open('privacy');
  for (const [x,y] of [[50,110],[50,165],[50,220],[500,610]]) {
    const r = await pick('privacy', { ...point,x,y }); assert.equal(r.status,200,JSON.stringify(r.data)); assert.ok(r.data.element); assert.ok(!JSON.stringify(r.data).includes('secret'),JSON.stringify(r.data));
  }
  const group = (await pick('privacy', { ...point,x:500,y:610 })).data.element;
  assert.match(group.text,/Visible contents/); assert.match(group.text,/Summary only/);
  const password = (await pick('privacy',{...point,x:50,y:110})).data.element; assert.equal(password.name,'Password'); assert.equal(password.text,'');
});
test('open shadow and cross-origin frames are selectable; transformed frames return their container', async () => {
  const point = await open('frames');
  const shadow = await pick('frames',{...point,x:350,y:45}); assert.equal(shadow.status,200,JSON.stringify(shadow.data)); assert.equal(shadow.data.element.name,'Shadow target');
  const child = await pick('frames',{...point,x:650,y:50}); assert.equal(child.status,200,JSON.stringify(child.data)); assert.equal(child.data.element.name,'Cross-origin child'); assert.match(child.data.element.frame_url,/localhost/);
  const scaled = await pick('frames',{...point,x:650,y:285}); assert.equal(scaled.status,200,JSON.stringify(scaled.data)); assert.equal(scaled.data.element.tag,'iframe'); assert.equal(scaled.data.element.fallback,'frame');
});
test('pick rejects animations, invalid coordinates, busy actions and stale main/shadow revisions', async () => {
  const point = await open('validation'); assert.equal((await pick('validation',{...point,y:285})).status,409);
  assert.equal((await pick('validation',{...point,x:-1})).status,400); assert.equal((await pick('validation',{...point,tab_id:'t99'})).status,409);
  assert.equal((await request('/sessions/validation/pick','POST',point,'wrong')).status,401);
  const waiting = execute('validation',{action:'wait',text:'not present'}); await delay(50); assert.equal((await pick('validation',point)).status,409);
  await request('/sessions/validation/cancel','POST'); await waiting;
  for (const [id,path] of [['shadow-stale','/shadow-change'],['dom-stale','/dom-change']]) { const old = await open(id,path); await delay(760); assert.equal((await pick(id,old)).status,409,path); }
});

test('incomplete shadow scan restricts picking without breaking ordinary large-page frames', async () => {
  const point = await open('large', '/large');
  const frame = await request('/sessions/large/frame');
  assert.equal(frame.status, 200);
  assert.equal(frame.headers.get('x-browser-version'), point.version);
  const result = await pick('large', point);
  assert.equal(result.status, 409);
  assert.match(result.data.error, /coverage/);
});

test('probe uses captured DOM intrinsics instead of website overrides', async () => {
  const point = await open('override','/override');
  await delay(80); // Drain effects of the fixture's ordinary agent snapshot.
  const before = events;
  const selected = await pick('override',point);
  assert.equal(selected.status,200,JSON.stringify(selected.data));
  assert.equal(selected.data.element.name,'Submit only if clicked');
  await delay(50);
  assert.equal(events,before,'read-only pick called a website override');
});

test('moving sibling layout animation blocks picking and its completion invalidates old frames', async () => {
  const point = await open('cover','/cover-animation');
  assert.equal((await pick('cover',point)).status,409);
  await delay(720);
  assert.equal((await pick('cover',point)).status,409);
  const image=await request('/sessions/cover/frame');
  assert.equal(image.status,200);
  assert.equal((await pick('cover',{...point,version:image.headers.get('x-browser-version')})).status,200);
});
