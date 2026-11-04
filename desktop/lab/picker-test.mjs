// Isolated real Electron acceptance: no-hover selection must never submit.
// Run from repository root with an absolute fresh output directory.
import assert from 'node:assert/strict';
import http from 'node:http';
import path from 'node:path';
import fs from 'node:fs/promises';
import {_electron} from '../../browser-runtime/node_modules/playwright/index.mjs';
const out=path.resolve(process.argv[2]);await fs.mkdir(out,{recursive:false});
let submissions=0;let slowEntered;const slowReady=new Promise(r=>slowEntered=r);
const site=http.createServer((req,res)=>{if(req.url==='/slow'){slowEntered();return;}if(req.url==='/submit')submissions++;res.writeHead(200,{'Content-Type':'text/html'});res.end(req.url==='/shell'?'<title>Picker probe shell</title>':'<!doctype html><title>Picker probe</title><form method=post action=/submit><button id=save style="position:absolute;left:20px;top:20px;width:180px;height:40px">Save</button></form>');});await new Promise(r=>site.listen(0,'127.0.0.1',r));
let app;
try{
 const origin='http://127.0.0.1:'+site.address().port;
 app=await _electron.launch({executablePath:path.resolve('pogo/desktop/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron'),args:[path.resolve('pogo/desktop/lab/popup-main.cjs')],env:{...process.env,POGO_POPUP_ORIGIN:origin,POGO_POPUP_DATA_DIR:path.join(out,'profile')},timeout:30000});
 app.process().stderr.on('data',b=>void fs.writeFile(path.join(out,'stderr.log'),b,{flag:'a'}));
 const base=await app.evaluate(async()=>{while(!global.__popupLab)await new Promise(r=>setTimeout(r,10));return global.__popupLab.url;});
 const req=async(suffix,method,body)=>{const r=await fetch(base+'/sessions/owner'+suffix,{method,headers:{Authorization:'Bearer isolated-popup-fixture-token','Content-Type':'application/json'},body:JSON.stringify(body)});const data=await r.json();assert.equal(r.status,200,JSON.stringify(data));return data;};
 await req('','PUT',{});await req('/execute','POST',{action:'navigate',url:origin+'/page',op_id:'nav'});
 const slow=req('/execute','POST',{action:'navigate',url:origin+'/slow',op_id:'slow'}).catch(()=>{});await slowReady;await req('/cancel','POST',{});await slow;await req('/execute','POST',{action:'navigate',url:origin+'/page',op_id:'resume'});
 await app.evaluate(async()=>{
  const {host,window}=global.__popupLab,owner=host.owners.get('owner'),r=owner.runtime,meta=r.page(),record=owner.records.get(meta.page),wc=record.view.webContents;
  await r.setControl({mode:'human',context_ref:r.contextRef});
  const surface={id:'probe',record,binding:{session_id:'owner',context_ref:r.contextRef,tab_id:meta.id},claim:{visible:true,blocked:false,lease_token:r.lease,generation:r.generation,control_epoch:r.controlEpoch}};record.surface=surface;host.surfaces.set(surface.id,surface);record.view.setBounds({x:0,y:0,width:1100,height:700});host.layout();
  global.__probe={record,events:[],messages:[],mouse:[],commands:[]};host.emit=(_surface,event)=>global.__probe.events.push(event);wc.debugger.on('message',(_e,method,params)=>{if(method.startsWith('Overlay.'))global.__probe.messages.push({method,params});});wc.on('before-mouse-event',(_e,input)=>global.__probe.mouse.push(input));
  const send=wc.debugger.sendCommand.bind(wc.debugger);wc.debugger.sendCommand=(method,params,...rest)=>{global.__probe.commands.push({method,params});return send(method,params,...rest);};
  window.show();window.focus();window.webContents.focus();await host.startPicker(record);
 });
 const results=[];
 for(const target of ['no-hover','with-hover']){
  results.push(await app.evaluate(async(_e,target)=>{
   const {record}=global.__probe,wc=record.view.webContents,host=global.__popupLab.host;
   if(!record.picking)await host.startPicker(record);
   const {x,y}={x:100,y:40};
   if(target==='with-hover')wc.sendInputEvent({type:'mouseMove',x,y});wc.sendInputEvent({type:'mouseDown',x,y,button:'left',clickCount:1});await new Promise(r=>setTimeout(r,500));wc.sendInputEvent({type:'mouseUp',x,y,button:'left',clickCount:1});await new Promise(r=>setTimeout(r,500));
   return {target,picking:record.picking,events:global.__probe.events,messages:global.__probe.messages,mouse:global.__probe.mouse,commands:global.__probe.commands};
  },target));
 }
 assert.equal(results[1].events.filter(e=>e.type==='pick').length,2,'native hover then click must also quote');results.push({submissions});assert.equal(submissions,0,'selection must not submit');assert.equal(results[0].events.filter(e=>e.type==='pick').length,1,'first no-hover click must quote');assert.equal(results[0].events.find(e=>e.type==='pick').pick.element.name,'Save');await fs.writeFile(path.join(out,'result.json'),JSON.stringify(results,null,2));console.log(JSON.stringify(results.filter(r=>r.events).map(r=>({target:r.target,picking:r.picking,eventTypes:r.events.map(e=>e.type),messageTypes:r.messages.map(m=>m.method),errors:r.events.filter(e=>e.type==='error'),submissions}))));
}finally{await app?.close().catch(()=>{});site.closeAllConnections();await new Promise(r=>site.close(r));}
