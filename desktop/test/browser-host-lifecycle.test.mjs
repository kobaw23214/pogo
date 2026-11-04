import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { DesktopBrowserHost } from '../browser-host.mjs';

let nextID=0;
class FakeContents extends EventEmitter {
  constructor() { super(); this.id=++nextID; this.destroyed=false; this.debugger=new EventEmitter(); }
  setWindowOpenHandler(handler) { this.windowOpen=handler; }
  isDestroyed() { return this.destroyed; }
  close() { if(this.failClose)throw new Error('owned destruction failed');this.destroyed=true;this.emit('destroyed'); }
  isDevToolsOpened() { return false; }
  isFocused() { return false; }
  getZoomFactor() { return 1; }
}
class FakeView {
  constructor(options={}) { this.options=options;this.webContents=options.webContents??new FakeContents(); }
  setBounds(value) { this.bounds=value; }
  getBounds() { return this.bounds; }
  setVisible(value) { this.visible=value; }
  getVisible() { return Boolean(this.visible); }
}
function fixture() {
  const children=[],host=Object.create(DesktopBrowserHost.prototype),closed=[];
  Object.assign(host,{electron:{WebContentsView:FakeView},closed:false,owners:new Map(),surfaces:new Map(),
    shield:new FakeView(),window:{isDestroyed:()=>false,contentView:{children,
      addChildView:view=>{children.push(view);},removeChildView:view=>{const i=children.indexOf(view);if(i>=0)children.splice(i,1);}}}});
  const owner={id:'owned',records:new Map(),pendingRecords:new Set(),closed:false,closing:false,inputSuspended:0,
    runtime:{phase:'ready',generation:1,closed:false},electronSession:{cookies:{flushStore:async()=>{closed.push('cookies');}}},
    context:{unregister() {},close:async()=>{closed.push('context');}}};
  host.owners.set(owner.id,owner);children.push(host.shield);
  return {host,owner,children,closed};
}

test('native popup creation preserves the supplied WebContents and forces owned secure preferences', async () => {
  const {host,owner}=fixture(),opener=host.createRecord(owner),nativeChild=new FakeContents();
  let bound;host.bindRecord=async record=>{bound=record;};
  const permission=opener.view.webContents.windowOpen({url:'about:blank',disposition:'new-window'});
  assert.equal(permission.action,'allow');
  assert.equal(permission.overrideBrowserWindowOptions.webPreferences.session,owner.electronSession);
  assert.equal(permission.createWindow({webContents:nativeChild,webPreferences:{nodeIntegration:true,sandbox:false}}),nativeChild);
  assert.equal(bound.opener,opener);assert.equal(bound.view.options.webPreferences.nodeIntegration,false);
  assert.equal(bound.view.options.webPreferences.sandbox,true);assert.equal(bound.view.options.webPreferences.contextIsolation,true);
  assert.equal(owner.pendingRecords.size,2,'the not-yet-registered popup remains owned');
  await host.closeOwner(owner);
  assert.equal(nativeChild.isDestroyed(),true);
});

test('popup protocol and phase are checked before creating native contents, with no tab-count cap', async () => {
  const {host,owner}=fixture(),opener=host.createRecord(owner),handler=opener.view.webContents.windowOpen;
  for(const url of ['file:///private','javascript:alert(1)','data:text/html,hello','ftp://fixture.invalid/'])assert.equal(handler({url}).action,'deny');
  for(const url of ['http://fixture.invalid/','https://fixture.invalid/','about:blank'])assert.equal(handler({url}).action,'allow');
  owner.runtime.phase='cancelling';assert.equal(handler({url:'about:blank'}).action,'deny');owner.runtime.phase='ready';
  for(let i=1;i<20;i++)host.createRecord(owner);
  assert.equal(host.ownedRecords(owner).length,20,'creation past the old sixteen-page cap is allowed');
  assert.equal(handler({url:'about:blank'}).action,'allow');
  await host.closeOwner(owner);assert.equal(owner.pendingRecords.size,0);
});

test('failed pending-view destruction retains an owner tombstone and can be confirmed on retry', async () => {
  const {host,owner,children,closed}=fixture(),record=host.createRecord(owner);
  record.view.webContents.failClose=true;
  await assert.rejects(host.closeOwner(owner),/owned destruction failed/);
  assert.equal(owner.closed,false);assert.equal(owner.closing,true);
  assert.equal(host.owners.get(owner.id),owner);assert.equal(owner.pendingRecords.has(record),true);
  assert.deepEqual(closed,[],'context and profile cleanup cannot claim success while a View remains alive');
  record.view.webContents.failClose=false;
  await host.closeOwner(owner);await host.closeRecord(record);
  assert.equal(owner.closed,true);assert.equal(host.owners.has(owner.id),false);
  assert.equal(owner.pendingRecords.size,0);assert.deepEqual(children,[host.shield]);
  assert.deepEqual(closed,['context','cookies']);
});

function pickerFixture() {
  const {host,owner}=fixture(),record=host.createRecord(owner),commands=[],events=[];
  let interactive=true;
  host.interactive=()=>interactive;host.emit=(_surface,event)=>events.push(event);
  record.surface={binding:{}};
  record.view.webContents.debugger.isAttached=()=>true;
  record.view.webContents.debugger.sendCommand=async(method,params)=>{commands.push({method,params});return {};};
  return {host,owner,record,commands,events,revoke:()=>{interactive=false;}};
}

test('cancelling at every picker installation await leaves no late inspect mode', async () => {
  for(const delayed of ['DOM.enable','Overlay.enable','Overlay.setInspectMode']) {
    const f=pickerFixture(),send=f.record.view.webContents.debugger.sendCommand;
    let release,reached;
    const pending=new Promise(resolve=>{reached=resolve;});
    f.record.view.webContents.debugger.sendCommand=async(method,params)=>{
      const result=await send(method,params);
      if(method===delayed&&params?.mode!=='none'){reached();await new Promise(resolve=>{release=resolve;});}
      return result;
    };
    const start=f.host.startPicker(f.record),rejected=assert.rejects(start,/已变化/);
    await pending;await f.host.stopPicker(f.record);release();await rejected;
    assert.equal(f.record.picking,false,delayed);
    assert.deepEqual(f.commands.at(-1).params.mode,'none',delayed);
    assert.deepEqual(f.events,[{type:'picker-ended'}],delayed);
  }
});

test('control revocation during picker installation explicitly removes inspect mode', async () => {
  const f=pickerFixture(),send=f.record.view.webContents.debugger.sendCommand;
  f.record.view.webContents.debugger.sendCommand=async(method,params)=>{
    const result=await send(method,params);if(params?.mode==='searchForNode')f.revoke();return result;
  };
  await assert.rejects(f.host.startPicker(f.record),/已变化/);
  assert.equal(f.record.picking,false);assert.equal(f.commands.at(-1).params.mode,'none');
});

test('a cancelled late installation cannot stop a newer picker', async () => {
  const f=pickerFixture(),send=f.record.view.webContents.debugger.sendCommand;
  let release,reached,first=true;
  const pending=new Promise(resolve=>{reached=resolve;});
  f.record.view.webContents.debugger.sendCommand=async(method,params)=>{
    const result=await send(method,params);
    if(method==='DOM.enable'&&first){first=false;reached();await new Promise(resolve=>{release=resolve;});}
    return result;
  };
  const previous=f.host.startPicker(f.record),rejected=assert.rejects(previous,/已变化/);
  await pending;await f.host.stopPicker(f.record);await f.host.startPicker(f.record);
  const commandCount=f.commands.length;release();await rejected;
  assert.equal(f.record.picking,true);assert.equal(f.commands.length,commandCount);
  assert.equal(f.commands.at(-1).params.mode,'searchForNode');
  assert.deepEqual(f.events,[{type:'picker-ended'}]);
});

test('a cancelled node observation neither publishes a citation nor stops a newer picker', async () => {
  const f=pickerFixture(),runtime=f.owner.runtime,meta={id:'t1',version:1};
  let release;
  Object.assign(runtime,{refreshRevision:()=>new Promise(resolve=>{release=resolve;}),observation:()=>({}),version:()=> 'v1',picker:{key:'private-key'}});
  f.host.resolve=()=>({owner:f.owner,meta});
  f.record.view.webContents.debugger.sendCommand=async(method)=>method==='DOM.resolveNode'?{object:{objectId:'node'}}:method==='Runtime.callFunctionOn'?{result:{value:'document:node'}}:{};
  await f.host.startPicker(f.record);f.record.lastMouse={x:10,y:20};
  const selecting=f.host.selected(f.record,1),rejected=assert.rejects(selecting,/已变化/);
  await f.host.stopPicker(f.record);await f.host.startPicker(f.record);f.record.lastMouse={x:10,y:20};
  release();await rejected;
  assert.equal(f.record.picking,true);assert.equal(f.record.selecting,false);
  assert.deepEqual(f.events,[{type:'picker-ended'}]);
});

test('Escape from the focused native website ends inspection and notifies Studio', async () => {
  const f=pickerFixture(),wc=f.record.view.webContents;
  await f.host.startPicker(f.record);
  const epoch=f.record.pickerEpoch;let prevented=0;
  wc.emit('before-input-event',{preventDefault:()=>{prevented++;}},{type:'keyDown',key:'Escape'});
  assert.equal(prevented,1);assert.equal(f.record.picking,false);assert.equal(f.record.pickerEpoch,epoch+1);
  assert.equal(f.commands.at(-1).params.mode,'none');assert.deepEqual(f.events,[{type:'picker-ended'}]);
  wc.emit('before-input-event',{preventDefault:()=>{prevented++;}},{type:'keyUp',key:'Escape'});
  assert.equal(prevented,1,'after exiting inspect mode, ordinary native keyboard input is preserved');
  assert.equal(f.record.pickerEpoch,epoch+1);
});

test('first picker click hit-tests the native node and fences mouse-up after selection completes', async () => {
  const f=pickerFixture(),wc=f.record.view.webContents,meta={version:1},selected=[];
  f.host.resolve=()=>({owner:f.owner,meta});
  wc.debugger.sendCommand=async method=>method==='DOM.getNodeForLocation'?{backendNodeId:9,frameId:'root'}:method==='Page.getFrameTree'?{frameTree:{frame:{id:'root'}}}:{};
  f.host.selected=async(record,node,_session,point)=>{selected.push({node,point});await f.host.stopPicker(record);};
  await f.host.startPicker(f.record);let prevented=0;
  wc.emit('before-mouse-event',{preventDefault:()=>{prevented++;}},{type:'mouseDown',button:'left',x:40,y:30});
  await new Promise(resolve=>setImmediate(resolve));
  assert.deepEqual(selected,[{node:9,point:{x:40,y:30}}]);assert.equal(f.record.picking,false);
  wc.emit('before-mouse-event',{preventDefault:()=>{prevented++;}},{type:'mouseUp',button:'left',x:40,y:30});
  assert.equal(prevented,2,'both phases stay outside the underlying website');assert.equal(f.record.pickerPointerDown,false);
});

test('native coordinate selection refuses child-frame identity without publishing a quote', async () => {
  const f=pickerFixture(),meta={version:1};let selected=0;
  f.host.resolve=()=>({owner:f.owner,meta});f.host.selected=async()=>{selected++;};
  f.record.view.webContents.debugger.sendCommand=async method=>method==='DOM.getNodeForLocation'?{backendNodeId:9,frameId:'child'}:method==='Page.getFrameTree'?{frameTree:{frame:{id:'root'}}}:{};
  await f.host.startPicker(f.record);
  await assert.rejects(f.host.selectAtPoint(f.record,{x:40,y:30}),/跨框架/);
  assert.equal(selected,0);assert.equal(f.record.picking,false);assert.deepEqual(f.events.map(e=>e.type),['error','picker-ended']);
});

test('cancelled native hit-test cannot publish or disable a newer picker', async () => {
  const f=pickerFixture(),meta={version:1};let release,selected=0;
  f.host.resolve=()=>({owner:f.owner,meta});f.host.selected=async()=>{selected++;};
  f.record.view.webContents.debugger.sendCommand=async method=>method==='DOM.getNodeForLocation'?new Promise(resolve=>{release=resolve;}):{};
  await f.host.startPicker(f.record);
  const pending=f.host.selectAtPoint(f.record,{x:40,y:30}),rejected=assert.rejects(pending,/已变化/);
  await f.host.stopPicker(f.record);await f.host.startPicker(f.record);
  release({backendNodeId:9,frameId:'root'});await rejected;
  assert.equal(selected,0);assert.equal(f.record.picking,true);assert.equal(f.record.hitTestFlight,null);
  assert.deepEqual(f.events,[{type:'picker-ended'}]);
});
