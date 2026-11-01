import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { WaylandRuntime, WaylandSessionHost, waylandCaptureScale } from '../src/wayland.js';
import { BrowserSession } from '../src/session.js';
import { LiveView } from '../src/live.js';

function fixture(t) {
  const client = { async send() {}, async detach() {} };
  const context = { setDefaultTimeout() {}, setDefaultNavigationTimeout() {}, on() {}, async newCDPSession() { return client; } };
  const session = new BrowserSession(context);
  session.devtools = { async attach() { return { client }; }, isPaused: () => false, isBlocked: () => false };
  const runtime = new WaylandRuntime({ deviceScaleFactor: 2 });
  const host = new WaylandSessionHost(runtime, 'resolution-unit'); session.host = host; host.session = session;
  const page = new EventEmitter(), native = { width:1280, height:800, scale:1.5 }, calls = [];
  Object.assign(page, { url: () => 'https://fixture.invalid', isClosed: () => false, async evaluate() { return { ...native }; } });
  const record = { page, pageId:'t1', displayId:2, viewport:{ width:1280, height:800 }, scale:1.5, proof:{ key:'old' } };
  host.records.set(page, record);
  host.rpc = { async request(op, args) {
    calls.push({ op, args });
    if (op === 'resize') {
      Object.assign(native, { width:args.width, height:args.height, scale:args.scale });
      return { ...args, pixel_width:args.width*args.scale, pixel_height:args.height*args.scale };
    }
    return { ack:true };
  } };
  const meta = { id:'t1', version:1, visual:1, topDocumentVersion:1, historyVersion:1, title:'Fixture', documentIdentityReady:true, control:'human', lease:'fixture-lease', controlEpoch:1, controlPhase:'ready', page };
  session.pages.set(meta.id, meta); session.active = meta.id;
  const messages = [], socket = new EventEmitter();
  Object.assign(socket, { readyState:1, bufferedAmount:0, send(value) { messages.push(JSON.parse(value)); }, close() { this.readyState=3; } });
  const view = new LiveView(socket, session, { context_ref:session.contextRef, tab_id:meta.id });
  view.client = client; view.mediaScale = 2; session.liveViews.add(view);
  const created = [];
  session.mediaBroker = { runtime:'wayland', async create(target, options) {
    created.push({ target, options });
    return { offer:{ sdp:'v=0\r\n' }, pixels:host.getCaptureGeometry(target), async close() {} };
  } };
  function present() {
    view.mediaEpoch = 'epoch-'+(messages.length+1); view.stamp = view.identity();
    const frame = { frame_id:1, media_epoch:view.mediaEpoch, generation:session.generation, control_epoch:1, frame_identity:session.frameIdentity(meta), viewport:session.viewport(meta), pixel_width:host.getCaptureGeometry(page).width, pixel_height:host.getCaptureGeometry(page).height, acked:true };
    view.frames.clear(); view.frames.set(1,frame); return frame;
  }
  present();
  const viewport = (seq, preferred, width=1280, height=800, extra={}) => ({ type:'viewport', seq, generation:session.generation, control_epoch:1, lease_token:'fixture-lease', frame_id:1, media_epoch:view.mediaEpoch, width,height, ...(preferred===undefined?{}:{preferred_scale:preferred}), ...extra });
  t.after(() => view.close());
  return { session,host,page,record,native,calls,view,meta,messages,created,present,viewport };
}

test('native resolution chooses the closest sufficient density within physical and deployment limits', () => {
  for (const [viewport, preferred, expected] of [
    [{ width:1280,height:800 },1,1], [{ width:1280,height:800 },1.1,1.25],
    [{ width:1280,height:800 },1.5,1.5], [{ width:1280,height:800 },1.6,2],
    [{ width:1600,height:900 },2,2], [{ width:1920,height:1080 },2,1.5],
    [{ width:1920,height:968 },2,2], [{ width:1920,height:976 },2,1.5],
    [{ width:2560,height:1600 },2,1.25],
  ]) assert.equal(waylandCaptureScale(viewport,preferred),expected,JSON.stringify({ viewport,preferred }));
  for (const maximum of [1,1.25,1.5,2]) {
    const runtime = new WaylandRuntime({ deviceScaleFactor:maximum });
    assert.equal(runtime.scale,Math.min(1.5,maximum));
    assert.equal(waylandCaptureScale({ width:1280,height:800 },2,maximum),maximum);
  }
  for (const preferred of [NaN,Infinity,-Infinity,0,3,'2',true,null,[],{}]) assert.throws(() => waylandCaptureScale({ width:1280,height:800 },preferred),error=>error.status===400);
  for (const maximum of [NaN,0,1.1,3,'2']) assert.throws(() => new WaylandRuntime({ deviceScaleFactor:maximum }),/maximum native output scale/);
});

test('a lower preference cannot downgrade the shared output until its CSS viewport changes', async t => {
  const h=fixture(t);
  await h.host.setViewport(h.page,{ width:1280,height:800,preferred_scale:2 });
  assert.equal(h.record.scale,2); assert.equal(h.record.proof,null);
  assert.equal(h.host.getTargetCaptureScale(h.page,{ width:1280,height:800 },1),2);
  assert.equal(h.host.getTargetCaptureScale(h.page,{ width:1280,height:800 }),2);
  assert.equal(h.host.getTargetCaptureScale(h.page,{ width:1600,height:904 },1.5),1.5);
  await h.host.setViewport(h.page,{ width:1600,height:904,preferred_scale:1.5 });
  assert.equal(h.record.scale,1.5); assert.deepEqual(h.record.viewport,{ width:1600,height:904 });
});

test('native realization validates pixel geometry before committing per-page metadata', async t => {
  const h=fixture(t), other={ viewport:{ width:1280,height:800 },scale:1.25,proof:{ key:'other' } };
  h.host.records.set({},other);
  h.host.rpc.request=async()=>({ width:1280,height:800,scale:2,pixel_width:1600,pixel_height:1000 });
  await assert.rejects(h.host.setViewport(h.page,{ width:1280,height:800,preferred_scale:2 }),/did not match/);
  assert.equal(h.record.scale,1.5); assert.deepEqual(h.record.proof,{ key:'old' });
  assert.equal(other.scale,1.25); assert.deepEqual(other.proof,{ key:'other' });
  const before=h.calls.length;
  await assert.rejects(h.host.setViewport(h.page,{ width:1280,height:800,preferred_scale:NaN }),error=>error.status===400);
  assert.equal(h.calls.length,before);
});

test('native output acknowledgment does not commit density before Chromium realizes the new DPR', async t => {
  const h=fixture(t); let reads=0;
  h.page.evaluate=async()=>{ reads++; return { ...h.native,scale:reads===1?1.5:h.native.scale }; };
  await h.host.setViewport(h.page,{ width:1280,height:800,preferred_scale:2 });
  assert.equal(reads,2); assert.equal(h.record.scale,2);
});

test('scale-only live resize rebuilds exactly one epoch and preserves CSS and document identity', async t => {
  const h=fixture(t), identity=h.session.frameIdentity(h.meta), stamp=h.view.identity(), oldEpoch=h.view.mediaEpoch;
  await h.view.receive(h.viewport(1,2));
  assert.equal(h.record.scale,2); assert.deepEqual(h.record.viewport,{ width:1280,height:800 });
  assert.equal(h.session.frameIdentity(h.meta),identity); assert.notEqual(h.view.identity(),stamp);
  assert.notEqual(h.view.mediaEpoch,oldEpoch);
  assert.equal(h.messages.filter(message=>message.type==='media_reset').length,1);
  const offer=h.messages.find(message=>message.type==='media_offer');
  assert.deepEqual(offer.frame.viewport,{ width:1280,height:800 });
  assert.equal(offer.frame.pixel_width,2560); assert.equal(offer.frame.pixel_height,1600);
  assert.equal(h.created.length,1);
  h.present(); await h.view.receive(h.viewport(2,1.5));
  assert.equal(h.created.length,1,'lower-density viewer does not rebuild a shared high-density output');
  assert.equal(h.record.scale,2);
});

test('an unchanged-CSS stale physical frame cannot authorize input after another viewer raises density', async t => {
  const h=fixture(t), oldStamp=h.view.stamp;
  h.record.scale=2;
  await assert.rejects(h.view.receive(h.viewport(1,2)),error=>error.code==='LIVE_FRAME_STALE');
  assert.notEqual(h.view.identity(),oldStamp);
  h.view.publishState(); await h.view.mediaQueue;
  assert.equal(h.created.length,1);
  assert.equal(h.messages.find(message=>message.type==='media_offer').frame.pixel_width,2560);
});

test('density requests retain lease, presentation and held-input guards', async t => {
  const h=fixture(t);
  await assert.rejects(h.view.receive(h.viewport(1,2,1280,800,{ lease_token:'wrong' })),error=>error.status===403);
  h.view.pressed.add('left');
  await assert.rejects(h.view.receive(h.viewport(2,2)),error=>error.code==='LIVE_BUSY');
  h.view.pressed.clear(); h.view.keys.set('ShiftLeft',{ key:'Shift',code:'ShiftLeft' });
  await assert.rejects(h.view.receive(h.viewport(3,2)),error=>error.code==='LIVE_BUSY');
  h.view.keys.clear();
  assert.equal(h.calls.some(call=>call.op==='resize'),false);
  for (const [index,preferred] of [NaN,Infinity,3,0,true,'2',null,{}].entries()) await assert.rejects(h.view.receive(h.viewport(4+index,preferred)),error=>error.status===400);
  assert.equal(h.created.length,0);
});

test('non-Wayland live views keep the old viewport protocol and reject native density injection', async t => {
  const h=fixture(t); h.session.host=undefined;
  await assert.rejects(h.view.receive(h.viewport(1,2)),error=>error.status===400);
  h.view.frames.get(1).pixel_width=1920;
  await h.view.receive(h.viewport(2,undefined));
  assert.equal(h.messages.find(message=>message.seq===2).type,'ack');
  assert.equal(h.created.length,0);
});
