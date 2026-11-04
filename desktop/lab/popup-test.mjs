import assert from 'node:assert/strict';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { mkdir,writeFile } from 'node:fs/promises';
import { _electron } from '../../browser-runtime/node_modules/playwright/index.mjs';

const here=path.dirname(fileURLToPath(import.meta.url)),out=path.resolve(process.argv[2]);
assert.ok(process.env.POGO_POC_ELECTRON&&path.isAbsolute(process.env.POGO_POC_ELECTRON),'Provide an explicitly installed Electron executable');
await mkdir(out,{recursive:false,mode:0o700});
const report={passed:false,scope:'isolated Electron native popups and local synthetic fixtures',checks:[],coverage:[],limitations:[]};
const requests=[];let app;
report.requests=requests;
const site=http.createServer(async(req,res)=>{
  let body='';for await(const chunk of req)body+=chunk;
  requests.push({path:req.url,method:req.method,body,referer:req.headers.referer??''});
  res.setHeader('Content-Type','text/html;charset=utf-8');
  if(req.url==='/shell'){res.end('<title>Isolated popup test</title>');return;}
  const early='<script>window.__earlyProbe=Object.getOwnPropertyNames(window).some(k=>k.startsWith("__pogoPick_"));window.__nodeAccess=typeof require+":"+typeof process;</script>';
  if(req.url==='/root')res.end(`<!doctype html>${early}<title>Popup owner</title><a id="link" target="_blank" rel="opener" href="/child">Open link</a><form method="post" action="/post" target="_blank"><input name="payload" value="native-post-一次"><button id="post">Submit once</button></form><button id="blank" onclick="const p=window.open('about:blank');p.document.write('<title>Written blank</title><button id=written>Written synchronously</button>');p.document.close();p.opener.__blankAck='opener-retained';">Open writable blank</button>`);
  else res.end(`<!doctype html>${early}<title>Native ${req.method}</title><button id="child">Native child</button><script>window.__hasOpener=Boolean(window.opener);if(window.opener)window.opener.__childAck='native-opener';</script>`);
});
await new Promise(resolve=>site.listen(0,'127.0.0.1',resolve));
const origin=`http://127.0.0.1:${site.address().port}`;
const check=(name,condition)=>{assert.ok(condition,name);report.checks.push(name);};
try{
  const env={...process.env,POGO_POPUP_ORIGIN:origin,POGO_POPUP_DATA_DIR:path.join(out,'profile')};delete env.ELECTRON_RUN_AS_NODE;
  app=await _electron.launch({executablePath:process.env.POGO_POC_ELECTRON,args:[path.join(here,'popup-main.cjs')],env,timeout:30000});
  app.process().stderr.on('data',chunk=>void writeFile(path.join(out,'electron-stderr.log'),chunk,{flag:'a'}));
  const base=await app.evaluate(async()=>{while(!global.__popupLab)await new Promise(r=>setTimeout(r,20));return global.__popupLab.url;});
  let op=0;
  const request=async(suffix,method='GET',body)=>{
    const response=await fetch(base+suffix,{method,headers:{Authorization:'Bearer isolated-popup-fixture-token','Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
    return {status:response.status,data:await response.json()};
  };
  const execute=async action=>{const reply=await request('/sessions/owner/execute','POST',{op_id:`popup-${++op}`,...action});assert.equal(reply.status,200,JSON.stringify(reply.data));assert.equal(reply.data.ok,true,JSON.stringify(reply.data));return reply.data;};
  check('owned session starts', (await request('/sessions/owner','PUT')).status===200);
  await execute({action:'navigate',url:origin+'/root'});
  const root=(await request('/sessions/owner/state')).data.active_tab;
  const inspect=async label=>{
    const info=await app.evaluate(async(_e,label)=>{
      const owner=global.__popupLab.host.owners.get('owner'),r=owner.runtime,meta=r.page(),record=owner.records.get(meta.page);
      return {label,active:meta.id,opener:r.pages.get(meta.id).opener===r.pages.get('t1')?.page,
        samePartition:record.view.webContents.session===owner.electronSession,
        content:await meta.page.evaluate(key=>({url:location.href,hasProbe:!!globalThis[key],earlyProbe:window.__earlyProbe??null,nodeAccess:typeof require+':'+typeof process,hasOpener:Boolean(window.opener),text:document.body.innerText}),r.picker.key)};
    },label);report.coverage.push(info);return info;
  };
  const initial=await inspect('root');check('root installs probe before fixture scripts',initial.content.earlyProbe===true);
  await execute({action:'click',target:'#link'});
  const linked=await inspect('native target blank');
  check('target blank becomes an owned native popup with actual opener',linked.active!==root&&linked.opener&&linked.samePartition&&linked.content.hasOpener);
  check('target blank navigates once',requests.filter(r=>r.path==='/child').length===1);
  check('popup keeps Node unavailable',linked.content.nodeAccess==='undefined:undefined');
  if(process.env.POGO_POPUP_NATIVE_INSPECT==='1'){
    const native=await app.evaluate(async(_electron,{origin})=>{
      const {inspectNativeNode}=global.__popupLab;
      const host=global.__popupLab.host,owner=host.owners.get('owner'),runtime=owner.runtime,meta=runtime.page();
      const record=owner.records.get(meta.page),cdp=record.view.webContents.debugger;
      const binding={session_id:owner.clientSessionId,context_ref:runtime.contextRef,tab_id:meta.id};
      const getNode=async()=>{const {root}=await cdp.sendCommand('DOM.getDocument');const {nodeId}=await cdp.sendCommand('DOM.querySelector',{nodeId:root.nodeId,selector:'#child'});return (await cdp.sendCommand('DOM.describeNode',{nodeId})).node.backendNodeId;};
      const args=async send=>({send,backendNodeId:await getNode(),runtime,meta,snapshotStamp:runtime.frameIdentity(meta),
        checkCurrent:stamp=>host.resolve(binding).record===record&&runtime.frameIdentity(meta)===stamp});
      const noProbe=await meta.page.evaluate(key=>!globalThis[key],runtime.picker.key);
      const methods=[];
      const selected=await inspectNativeNode(await args((method,params)=>{methods.push(method);return cdp.sendCommand(method,params);}));
      let replaced=false;
      try{
        let sampled=false;
        await inspectNativeNode(await args(async(method,params)=>{
          const result=await cdp.sendCommand(method,params);
          if(method==='Accessibility.getPartialAXTree'&&!sampled){sampled=true;await meta.page.locator('#child').evaluate(node=>node.replaceWith(node.cloneNode(true)));}
          return result;
        }));
      }catch(error){replaced=error.status===409;}
      let navigated=false;
      try{
        let sampled=false;
        await inspectNativeNode(await args(async(method,params)=>{
          const result=await cdp.sendCommand(method,params);
          if(method==='Accessibility.getPartialAXTree'&&!sampled){sampled=true;await meta.page.goto(origin+'/navigation-replacement');}
          return result;
        }));
      }catch(error){navigated=error.status===409;}
      return {noProbe,selected,methods,replaced,navigated};
    },{origin});
    report.nativeInspection=native;
    check('native inspector selects initial popup with no private probe',native.noProbe&&native.selected.element.name==='Native child');
    check('native receipt declares DOM/accessibility facts and hides backend identity',native.selected.observation_scope==='native-dom-accessibility'&&!JSON.stringify(native.selected).includes('backendNodeId'));
    check('native inspector dispatches no page evaluation or HTML/value read',native.methods.every(method=>['DOM.describeNode','DOM.getBoxModel','Accessibility.getPartialAXTree'].includes(method)));
    check('native inspector rejects same-text replacement during sampling',native.replaced);
    check('native inspector rejects navigation during sampling',native.navigated);
  }
  await execute({action:'tab',target:root});
  await execute({action:'click',target:'#post'});
  const posted=await inspect('native POST target blank');
  check('POST popup is owned and native',posted.active!==root&&posted.samePartition);
  const posts=requests.filter(r=>r.path==='/post');
  check('POST body reaches native popup exactly once without GET replay',posts.length===1&&posts[0].method==='POST'&&new URLSearchParams(posts[0].body).get('payload')==='native-post-一次');
  await execute({action:'tab',target:root});
  await execute({action:'click',target:'#blank'});
  const blank=await inspect('synchronous about blank');
  check('about blank preserves synchronous document.write and opener',blank.active!==root&&blank.opener&&blank.content.hasOpener&&blank.content.text.includes('Written synchronously'));
  check('about blank document.write causes no HTTP replay',requests.filter(r=>r.path==='/root').length===1);
  const ack=await app.evaluate(async()=>global.__popupLab.host.owners.get('owner').runtime.pages.get('t1').page.evaluate(()=>window.__blankAck));
  check('popup can communicate with its native opener',ack==='opener-retained');
  const gaps=report.coverage.filter(item=>item.label!=='root'&&!item.content.hasProbe);
  report.limitations.push(gaps.length?'Initial popup documents execute before private per-page probes register. No navigation is replayed. The optional native inspector reads current DOM geometry/accessibility facts; it does not restore the private probe or pre-attachment Network evidence.':'This fixture installed probes before all sampled popup scripts; this does not guarantee arbitrary popup startup coverage.');

  // Hold registration, not native popup creation, to prove DELETE also owns
  // windows that have not acquired a Playwright Page or a public tab ID yet.
  await execute({action:'tab',target:root});
  await app.evaluate(()=>{
    const host=global.__popupLab.host,original=host.bindRecord.bind(host);
    global.__popupLab.releaseBinding=null;
    host.bindRecord=record=>record.opener?new Promise(resolve=>{global.__popupLab.releaseBinding=()=>resolve(original(record));}):original(record);
  });
  await execute({action:'click',target:'#blank'});
  const pending=await app.evaluate(()=>global.__popupLab.host.owners.get('owner').pendingRecords.size);
  check('unregistered popup is tracked as pending',pending===1);
  check('DELETE confirms cleanup including unregistered popup',(await request('/sessions/owner','DELETE')).status===200);
  await app.evaluate(()=>global.__popupLab.releaseBinding?.());
  const remaining=await app.evaluate(({webContents})=>webContents.getAllWebContents().filter(w=>w.getURL().startsWith(global.__popupLab.window.webContents.getURL().replace('/shell',''))||w.getURL()==='about:blank').map(w=>w.getURL()));
  check('all owned popup Views were destroyed',remaining.every(url=>url.endsWith('/shell')));
  report.requests=requests;report.passed=true;
}catch(error){report.error=String(error.stack??error);throw error;}
finally{
  await writeFile(path.join(out,'summary.json'),JSON.stringify(report,null,2));
  await app?.close().catch(()=>{});site.closeAllConnections();await new Promise(resolve=>site.close(resolve));
  console.log(JSON.stringify({passed:report.passed,checks:report.checks.length,output:out,error:report.error?.split('\n')[0]}));
}
