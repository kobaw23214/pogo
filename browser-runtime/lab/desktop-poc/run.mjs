#!/usr/bin/env node
// Isolated, synthetic Electron baseline. Never connects to Pogo production.
import assert from 'node:assert/strict';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { readFile, writeFile, mkdir, rename } from 'node:fs/promises';
import { performance } from 'node:perf_hooks';
import { _electron } from '../../node_modules/playwright/index.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const output = path.resolve(process.argv[2]);
const electronPath = process.env.POGO_POC_ELECTRON;
assert.ok(electronPath && path.isAbsolute(electronPath), 'provide the separately installed Electron executable');
await mkdir(output, {recursive:false, mode:0o700});
const save = async (name, value) => {
  const target=path.join(output,name), temporary=target+'.tmp';
  await writeFile(temporary, JSON.stringify(value,null,2));
  await rename(temporary,target);
};
const report = {passed:false, scope:'isolated visible WebContentsView and local synthetic fixtures', checks:[], measurements:{}, limitations:[
  'No production integration or comparison against the deployed screenshot interface.',
  'Automation timings are command round trips, not physical keyboard or input-to-photon latency.',
  'Unicode insertion is not OS IME composition verification.',
  'No Rust implementation or Rust performance benefit has been measured.',
  'Resource readings sum process working sets; shared pages may be counted repeatedly.',
]};
const samples = [], counters = {A:0, B:0}, requests = [];
const check = (name, condition) => { assert.ok(condition, name); report.checks.push(name); };
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const stats = values => { const a=[...values].sort((x,y)=>x-y); return {n:a.length,p50_ms:a[Math.floor((a.length-1)*.5)],p95_ms:a[Math.floor((a.length-1)*.95)],max_ms:a.at(-1)}; };
const fixture = await readFile(path.join(here, 'fixture.html'));
const server = http.createServer(async (req,res) => {
  const u = new URL(req.url, 'http://fixture.invalid');
  if(req.method==='GET' && ['/fixture.html','/frame.html'].includes(u.pathname)) {
    res.writeHead(200, {'Content-Type':'text/html; charset=utf-8','Cache-Control':'no-store'}); res.end(fixture); return;
  }
  if(req.method==='POST' && u.pathname==='/save' && ['A','B'].includes(u.searchParams.get('tab'))) {
    let body=''; for await(const chunk of req) { body+=chunk; if(body.length>16000) {res.writeHead(413).end(); return;} }
    const tab=u.searchParams.get('tab'); requests.push({tab,payload:JSON.parse(body)});
    res.writeHead(200, {'Content-Type':'application/json'});res.end(JSON.stringify({counter:++counters[tab],tab}));return;
  }
  res.writeHead(404).end();
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const origin=`http://127.0.0.1:${server.address().port}`;
let application, samplePhase='startup', sampler, exiting=false;
const main = fn => application.evaluate(fn);
async function sample() {
  if(exiting) return;
  const metrics=await main(()=>global.__pogoPoc.getMetrics());
  samples.push({phase:samplePhase,at_ms:performance.now(),processes:metrics,controller_rss_bytes:process.memoryUsage().rss});
}
try {
  const started=performance.now();
  const launchEnv={...process.env,POGO_POC_ORIGIN:origin,POGO_POC_DATA_DIR:path.join(output,'profile')};
  delete launchEnv.ELECTRON_RUN_AS_NODE;
  application=await _electron.launch({executablePath:electronPath,args:[path.join(here,'main.cjs')],env:launchEnv,timeout:30000});
  application.process().stderr.on('data',chunk=>void writeFile(path.join(output,'electron-stderr.log'),chunk,{flag:'a'}));
  await application.evaluate(async()=>{while(!global.__pogoPoc || global.__pogoPoc.listTabs().some(t=>!t.url.includes('/fixture.html'))) await new Promise(r=>setTimeout(r,30));});
  report.measurements.startup_to_fixture_ready_ms=performance.now()-started;
  report.versions=await application.evaluate(()=>({...process.versions}));
  let pages=application.windows();
  const pageFor=tab=>pages.find(p=>p.url().startsWith(origin+'/fixture.html')&&new URL(p.url()).searchParams.get('tab')===tab);
  for(let i=0;i<100 && (!pageFor('A')||!pageFor('B'));i++){await delay(30);pages=application.windows();}
  const a=pageFor('A'),b=pageFor('B'); assert.ok(a&&b,'Playwright discovers both actual WebContentsViews');
  await Promise.all([a.locator('#text-input').waitFor(),b.locator('#text-input').waitFor()]);
  const bindings=await main(()=>global.__pogoPoc.listTabs()); report.bindings=bindings;
  for(const [id,page] of [['A',a],['B',b]]) {
    const binding=bindings.find(t=>t.id===id),nonce=`owned-${id}-${Date.now()}`;
    await application.evaluate(({webContents},{wc,nonce})=>webContents.fromId(wc).executeJavaScript(`window.__samePageNonce=${JSON.stringify(nonce)}`),{wc:binding.webContentsId,nonce});
    check(`Playwright ${id} controls exactly the registered visible WebContents`,await page.evaluate(()=>window.__samePageNonce)===nonce);
  }
  await sample(); samplePhase='two_views_idle';
  sampler=setInterval(()=>sample().catch(()=>{}),1000);
  await delay(10000);
  samplePhase='synthetic_interaction';
  await main(()=>global.__pogoPoc.selectTab('A'));
  const latin='native-browser-'.repeat(8);
  await a.locator('#text-input').pressSequentially(latin);
  check('continuous synthetic printable keys are delivered without dropped or repeated characters',await a.locator('#text-input').inputValue()===latin);
  await a.locator('#text-area').focus(); await a.keyboard.insertText('同一个真实网页，中文字符验证。');
  check('Unicode text reaches the visible page (not an OS IME test)',await a.locator('#text-area').inputValue()==='同一个真实网页，中文字符验证。');
  check('input does not leak into the other Sheet',await b.locator('#text-input').inputValue()==='');
  const handle=await a.locator('#drag-handle').boundingBox(),track=await a.locator('#drag-track').boundingBox();
  await a.mouse.move(handle.x+handle.width/2,handle.y+handle.height/2);await a.mouse.down();
  await a.mouse.move(track.x+track.width-25,handle.y+handle.height/2,{steps:25});await a.mouse.up();
  const drag=await a.evaluate(()=>window.__pocMetrics.drag);
  check('ordinary drag receives down move and up and reaches the end',drag.completed===1 && drag.value>75 && !drag.active);
  await a.frameLocator('#iframe').locator('#frame-input').fill('iframe works');
  check('same-origin iframe remains directly interactive',await a.frameLocator('#iframe').locator('#frame-input').inputValue()==='iframe works');
  await a.locator('#save').click();await a.locator('#save-result[data-status="ok"]').waitFor();
  check('save causes exactly one request on the bound tab',counters.A===1 && counters.B===0);
  const switchSamples=[];
  for(let i=0;i<20;i++) {const t=performance.now();await application.evaluate((_,id)=>global.__pogoPoc.selectTab(id),i%2?'A':'B');switchSamples.push(performance.now()-t);}
  report.measurements.tab_switch_command_roundtrip=stats(switchSamples);
  check('switching Sheets preserves WebContents identities and typed content',JSON.stringify(await main(()=>global.__pogoPoc.listTabs()))===JSON.stringify(bindings)&&await a.locator('#text-input').inputValue()===latin);
  const commandSamples=[];
  for(let i=0;i<60;i++){const t=performance.now();await a.evaluate(()=>document.querySelector('#text-input').value);commandSamples.push(performance.now()-t);}
  report.measurements.playwright_read_roundtrip=stats(commandSamples);
  await a.locator('#text-input').scrollIntoViewIfNeeded();
  await main(()=>global.__pogoPoc.setInspect('A',true));
  const box=await a.locator('#text-input').boundingBox();
  await a.mouse.move(box.x+box.width/2,box.y+box.height/2);
  await delay(150);await a.screenshot({path:path.join(output,'native-inspect.png')});
  await a.mouse.click(box.x+box.width/2,box.y+box.height/2);
  let inspect;
  for(let i=0;i<60;i++){inspect=await main(()=>global.__pogoPoc.snapshotInspectState());const state=inspect.tabs.find(t=>t.id==='A');if(state?.selected&&!state.enabled)break;await delay(30);}
  const selection=inspect.tabs.find(t=>t.id==='A').selected;
  check('Chromium inspect mode produces a selected backend node',selection?.backendNodeId>0);
  const finishedInspect=(await main(()=>global.__pogoPoc.snapshotInspectState())).tabs.find(t=>t.id==='A');
  check('selection exits inspect mode without a protocol error',!finishedInspect.enabled && !finishedInspect.lastError);
  const described=await application.evaluate(async({webContents},{id,node})=>webContents.fromId(id).debugger.sendCommand('DOM.describeNode',{backendNodeId:node.backendNodeId},node.sessionId||undefined),{id:bindings.find(t=>t.id==='A').webContentsId,node:selection});
  check('selected node is the requested input',described.node.attributes?.includes('text-input'));
  await main(()=>global.__pogoPoc.openDevtools('A'));
  check('Playwright retains access while visible DevTools is open',await a.locator('#text-input').inputValue()===latin);
  await main(()=>global.__pogoPoc.setInspect('A',true));await main(()=>global.__pogoPoc.setInspect('A',false));
  check('CDP inspect command succeeds with DevTools open',true);
  await main(()=>global.__pogoPoc.closeDevtools('A'));
  await main(()=>global.__pogoPoc.selectTab('A'));
  await a.screenshot({path:path.join(output,'native-page.png')});
  const shell=pages.find(p=>p.url().startsWith('file:'));
  if(shell)await shell.screenshot({path:path.join(output,'shell.png')});
  report.fixture=await a.evaluate(()=>window.__pocMetrics);
  report.savedRequests=requests;report.inspect=inspect;
  samplePhase='settled_idle';await delay(10000);await sample();
  report.passed=true;
  await save('ready-for-manual.json',{runnerPid:process.pid,pid:application.process().pid,origin,electronPath,output,bindings});
  await save('summary.json',report);await save('process-samples.json',samples);
  console.log(JSON.stringify({passed:true,checks:report.checks.length,measurements:report.measurements,output}));
  if(process.env.POGO_POC_KEEP_OPEN==='1') {
    samplePhase='manual_observation';
    clearInterval(sampler);
    console.log('Owned PoC ready. Write control.json {id,action:"snapshot"|"finish"} for a synthetic state snapshot or orderly shutdown. SIGINT is emergency cleanup.');
    // File-based lab control has no network listener and exposes no website API.
    // Capture only on demand so observation does not continuously poll the page.
    await new Promise(resolve=>{
      let busy=false, lastId=null;
      const finish=()=>{clearInterval(controlPoll);application.removeListener('close',finish);resolve();};
      const controlPoll=setInterval(async()=>{
        if(busy)return;busy=true;
        try {
          const request=JSON.parse(await readFile(path.join(output,'control.json'),'utf8'));
          if(typeof request.id!=='string'||request.id===lastId||!['snapshot','finish'].includes(request.action))return;
          lastId=request.id;
          const snapshots={};
          for(const [id,page] of [['A',a],['B',b]]) snapshots[id]=await page.evaluate(()=>({
            metrics:window.__pocMetrics, devicePixelRatio, innerWidth, innerHeight,
            visibility:document.visibilityState, focus:document.activeElement?.id,
            controls:Object.fromEntries(['drag-handle','drag-track','range'].map(id=>[id,document.getElementById(id).getBoundingClientRect().toJSON()])),
          }));
          await save('control-result.json',{id:request.id,action:request.action,snapshots,bounds:await main(()=>global.__pogoPoc.getBounds()),at:new Date().toISOString()});
          if(request.action==='finish')finish();
        }catch(error){if(error.code!=='ENOENT')await save('control-error.json',{id:lastId,error:String(error.message)});}
        finally{busy=false;}
      },250);
      application.once('close',finish);
    });
    report.manualFinalState=await a.evaluate(()=>window.__pocMetrics).catch(()=>null);
  }
}catch(error){report.error=String(error.stack||error).slice(0,5000);console.log(JSON.stringify({passed:false,error:String(error.message),output}));}
finally {
  exiting=true;clearInterval(sampler);
  await save('summary.json',report);await save('process-samples.json',samples);
  let closeError=null;
  await application?.close().catch(error=>{closeError=String(error.message);});
  server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
  await save('cleanup.json',{electron_closed:!closeError,closeError,fixture_server_closed:true,production_touched:false});
}
process.exitCode=report.passed?0:1;
