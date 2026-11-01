// Actual Studio -> Go -> Docker picker latency and read-only correctness.
// The test owns every fixture, Agent, session and UI-only browser it creates.
import assert from 'node:assert/strict';
import http from 'node:http';
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {createHash, randomUUID} from 'node:crypto';
import path from 'node:path';
import {performance} from 'node:perf_hooks';
import {chromium} from 'playwright';
import {verifyImagePair} from './native-only-deployed.mjs';

const [configFile, output] = process.argv.slice(2);
assert.ok(configFile && output && process.argv.length === 4, 'PRIVATE_CONFIG NEW_OUTPUT required');
const cfg = JSON.parse(await readFile(configFile, 'utf8'));
assert.match(cfg.appURL, /^http:\/\/(?:127\.0\.0\.1|localhost):[0-9]+$/);
const mode = cfg.pickerMode === 'baseline' ? 'baseline' : 'optimized';
await mkdir(output, {mode: 0o700});
const report = {kind:'actual-docker-picker-performance',mode,passed:false,checks:[],requests:{preview:0,pick:0,frame:0,input:0},
  scope:'Owned static fixture with 10000 extra DOM elements, open Shadow DOM and a cross-origin iframe. Trusted synthetic pointer in a headless Studio client; pointer event to correct outline at requestAnimationFrame, not physical input-to-photon.'};
const headers={Authorization:'Bearer '+cfg.token,Origin:cfg.appURL,...(cfg.releaseToken?{'X-Pogo-Release':cfg.releaseToken}:{})};
const api=async(route,body,method=body===undefined?'GET':'POST')=>{
  const r=await fetch(cfg.appURL+route,{method,headers:{...headers,...(body===undefined?{}:{'Content-Type':'application/json'})},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(30000)});
  assert.ok(r.ok,`${method} ${route}: HTTP ${r.status}`);return r.status===204?null:r.json();
};
const check=(label,yes)=>{assert.ok(yes,label);report.checks.push(label);};
const hash=s=>createHash('sha256').update(s).digest('hex');
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const prefix='/picker-'+randomUUID();
let browser,page,session,agent,fixture,childFixture;
let receipt={clicks:0,moves:0};
const failures=[];
const queryRequests=new Set();
const requestDurations={preview:[],pick:[]};
const rects=Array.from({length:24},(_,i)=>({x:40+(i%4)*190,y:80+Math.floor(i/4)*72,width:150,height:44}));
function serve(server){return new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});}
async function close(server){if(server){server.closeAllConnections();await new Promise(r=>server.close(r));}}
try {
  report.images=await verifyImagePair(cfg);
  report.script_sha256=hash(await readFile(new URL(import.meta.url)));
  childFixture=http.createServer((req,res)=>{res.setHeader('Cache-Control','no-store');res.setHeader('Content-Type','text/html; charset=utf-8');res.end('<!doctype html><style>body{margin:0}button{position:absolute;left:20px;top:30px;width:150px;height:44px}</style><button>Frame target</button>');});
  await serve(childFixture);
  const childOrigin=`http://host.docker.internal:${childFixture.address().port}`;
  const buttons=rects.map((r,i)=>`<button id="target-${i}" style="left:${r.x}px;top:${r.y}px">Target ${i}</button>`).join('');
  const html=`<!doctype html><meta charset="utf-8"><title>Picker performance fixture</title><style>*{box-sizing:border-box}body{margin:0;background:#fff;color:#111;font:16px system-ui}button{position:absolute;width:150px;height:44px}h1{margin:15px 40px;font-size:22px}#shadow{position:absolute;left:850px;top:80px;width:150px;height:44px}iframe{position:absolute;left:850px;top:200px;width:230px;height:120px;border:0}</style><h1>Picker performance fixture</h1>${buttons}<div id="shadow"></div><iframe src="${childOrigin}/frame"></iframe><section hidden id="large">${'<i>row</i>'.repeat(10000)}</section><script>const s={clicks:0,moves:0};function emit(){fetch('${prefix}/receipt',{method:'POST',body:JSON.stringify(s)})}const root=document.querySelector('#shadow').attachShadow({mode:'open'});root.innerHTML='<style>button{box-sizing:border-box;width:150px;height:44px}</style><button>Shadow target</button>';addEventListener('click',()=>{s.clicks++;emit()});addEventListener('pointermove',()=>{s.moves++;emit()});emit();</script>`;
  report.fixture_content_sha256=hash(html.replaceAll(prefix,'/fixture').replaceAll(childOrigin,'http://child'));
  fixture=http.createServer(async(req,res)=>{res.setHeader('Cache-Control','no-store');if(req.method==='GET'&&req.url===prefix+'/page'){res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);return;}if(req.method==='POST'&&req.url===prefix+'/receipt'){let raw='';for await(const chunk of req){raw+=chunk;if(raw.length>2048){res.writeHead(413).end();return;}}try{receipt=JSON.parse(raw);res.end('ok');}catch{res.writeHead(400).end();}return;}res.writeHead(404).end();});
  await serve(fixture);
  const url=`http://host.docker.internal:${fixture.address().port}${prefix}/page`;
  const catalog=await api('/api/models');const model=catalog.models.find(m=>m.id===cfg.model&&m.configured)||catalog.models.find(m=>m.configured);assert.ok(model);
  agent=await api('/api/manage/agents',{name:'Picker check '+randomUUID().slice(0,8),description:'Owned synthetic read-only picker verification',instructions:'No model task.',model:model.id,models:[model.id],tools:['browser'],skills:[],mcp_servers:[],status:'ready'});
  session=await api(`/api/apps/${agent.id}/sessions`,{});
  const state=await api(`/api/sessions/${session.id}/browser`);
  const binding={context_ref:state.context_ref,tab_id:state.viewed_tab||state.active_tab};
  const viewedState=()=>api(`/api/sessions/${session.id}/browser?${new URLSearchParams(binding)}`);
  browser=await chromium.launch({headless:true});
  const context=await browser.newContext({viewport:{width:1700,height:1050},locale:'zh-CN',...(cfg.releaseToken?{extraHTTPHeaders:{'X-Pogo-Release':cfg.releaseToken}}:{})});
  page=await context.newPage();
  await page.route('**/*',async route=>{const req=route.request(),u=new URL(req.url()),bound=/^\/api\/sessions\/([^/]+)/.exec(u.pathname);if(u.origin!==cfg.appURL||u.pathname.endsWith('/messages')||bound&&!['activity','search','sample',session.id].includes(bound[1])){failures.push({kind:'unexpected-request',method:req.method(),path:u.pathname});await route.abort();return;}await route.continue();});
  page.on('pageerror',error=>failures.push({kind:'UI',error:String(error.message).slice(0,500)}));
  page.on('request',req=>{const p=new URL(req.url()).pathname;if(p.endsWith('/browser/pick/preview'))report.requests.preview++;else if(p.endsWith('/browser/pick'))report.requests.pick++;else if(p.endsWith('/browser/frame'))report.requests.frame++;else if(p.endsWith('/browser/input'))report.requests.input++;if(p.endsWith('/browser/pick/preview')||p.endsWith('/browser/pick')){queryRequests.add(req);report.max_inflight_queries=Math.max(report.max_inflight_queries||0,queryRequests.size);}});
  const queryFinished=req=>{if(!queryRequests.delete(req))return;const timing=req.timing(),kind=new URL(req.url()).pathname.endsWith('/preview')?'preview':'pick';if(timing.responseEnd>=0)requestDurations[kind].push(timing.responseEnd);};
  page.on('requestfinished',queryFinished);page.on('requestfailed',queryFinished);
  const targetURL=new URL(cfg.appURL+`/manage/agents/${agent.id}/chat`);targetURL.searchParams.set('c',session.id);targetURL.searchParams.set('token',cfg.token);await page.goto(targetURL.href);
  await page.getByRole('button',{name:/^面板(?: ·.*)?$|^Panels?(?: ·.*)?$/}).click();
  const pane=page.locator('.native-browser:visible');const tile=page.locator('.hub-empty .tile').filter({hasText:/浏览器|Browser/i});
  if(await tile.isVisible().catch(()=>false))try{await tile.click({timeout:1500});}catch(error){if(!await pane.isVisible().catch(()=>false))throw error;}
  await pane.waitFor({state:'visible'});await pane.getByRole('button',{name:/接管|Take.*control|Take over/i}).click();
  const address=pane.getByRole('textbox',{name:/原站地址|Original URL|URL/}).first();await address.fill(url);await address.press('Enter');
  const img=pane.locator('.native-page img');await img.waitFor({state:'visible'});await pane.locator('.native-page:not(.readonly)').waitFor();
  await page.waitForFunction(()=>{const i=document.querySelector('.native-page img');return i?.complete&&i.naturalWidth>0;});
  const projected=async r=>{const remote=await viewedState();return img.evaluate((el,{r,viewport})=>{const b=el.getBoundingClientRect(),scale=Math.min(b.width/viewport.width,b.height/viewport.height);return {x:b.x+(b.width-viewport.width*scale)/2+r.x*scale,y:b.y+(b.height-viewport.height*scale)/2+r.y*scale,width:r.width*scale,height:r.height*scale};},{r,viewport:remote.viewport});};
  async function measure(r){
    const box=await projected(r),p={x:box.x+box.width/2,y:box.y+box.height/2};
    await page.evaluate(({box,p})=>{
      globalThis.__pickerMeasureCleanup?.();
      const data={started:null,done:null};globalThis.__pickerMeasure=data;
      let waiting=false;
      const scan=()=>{if(waiting||data.started===null||data.done!==null)return;const el=document.querySelector('[data-testid="browser-pick-target"]');if(!el)return;const b=el.getBoundingClientRect();if(Math.abs(b.x-box.x)>2||Math.abs(b.y-box.y)>2||Math.abs(b.width-box.width)>2||Math.abs(b.height-box.height)>2)return;waiting=true;requestAnimationFrame(()=>{data.done=performance.now()-data.started;});};
      const move=e=>{if(Math.abs(e.clientX-p.x)<2&&Math.abs(e.clientY-p.y)<2){data.started=performance.now();scan();}};
      addEventListener('pointermove',move,true);const observer=new MutationObserver(scan);observer.observe(document.querySelector('.native-page'),{subtree:true,childList:true,attributes:true});
      globalThis.__pickerMeasureCleanup=()=>{removeEventListener('pointermove',move,true);observer.disconnect();};
    },{box,p});
    await page.mouse.move(p.x,p.y);
    await page.waitForFunction(()=>globalThis.__pickerMeasure?.done!==null&&globalThis.__pickerMeasure?.done!==undefined,null,{timeout:12000});
    return page.evaluate(()=>{const ms=globalThis.__pickerMeasure.done;globalThis.__pickerMeasureCleanup();return ms;});
  }
  const entry=performance.now();await pane.getByRole('button',{name:/选取页面元素|Pick.*element/i}).click();
  const first=await measure(rects[0]);report.first_mode_to_outline_ms=Math.round(performance.now()-entry);report.first_pointer_to_outline_ms=first;
  const samples=[];for(const r of rects.slice(1))samples.push(await measure(r));
  const sorted=[...samples].sort((a,b)=>a-b);report.hover={sample_ms:samples,p50_ms:sorted[Math.ceil(sorted.length*.5)-1],p95_ms:sorted[Math.ceil(sorted.length*.95)-1],max_ms:Math.max(...samples),n:samples.length};
  check('all hover positions match their actual fixture rectangle',samples.length===23);
  await measure({x:850,y:80,width:150,height:44});check('open Shadow DOM target has correct projected rectangle',true);
  await measure({x:870,y:230,width:150,height:44});check('cross-origin iframe target has correct projected rectangle',true);
  const beforeSweep=report.requests.preview;
  for(const r of rects.slice(0,16)){const b=await projected(r);await page.mouse.move(b.x+b.width/2,b.y+b.height/2);await pause(8);}
  await measure(rects[20]);
  await pause(350);const finalBox=await projected(rects[20]),actual=await pane.locator('[data-testid="browser-pick-target"]').boundingBox();
  check('rapid sweep settles on latest pointer without stale outline returning',actual&&Math.abs(actual.x-finalBox.x)<2&&Math.abs(actual.y-finalBox.y)<2);
  report.sweep_preview_requests=report.requests.preview-beforeSweep;
  const beforeCommit={...report.requests};
  await measure(rects[22]);const commitBox=await projected(rects[22]);
  const commitBegin=performance.now();await page.mouse.click(commitBox.x+commitBox.width/2,commitBox.y+commitBox.height/2);
  await page.waitForFunction(()=>document.querySelector('.native-browser .picker-toggle')?.getAttribute('aria-pressed')==='false',null,{timeout:12000});
  await page.getByText('button "Target 22"',{exact:false}).first().waitFor({timeout:12000});
  report.commit_to_chip_ms=Math.round(performance.now()-commitBegin);
  check('click produces the intended formal element citation',true);await pause(250);
  check('selection never dispatches a click or hover to the website',receipt.clicks===0&&receipt.moves===0);
  check('UI accesses only the owned test session and has no unhandled errors',failures.length===0);
  if(mode==='optimized'){
    check('hover uses lightweight preview rather than full pick',beforeCommit.preview>=23&&beforeCommit.pick===0);
    check('formal selection calls strict pick exactly once',report.requests.pick===1);
    check('preview and commit requests never accumulate concurrently',report.max_inflight_queries===1);
    check('local fixture hover P95 meets 150 ms target',report.hover.p95_ms<=150);
  }
  report.fixture_receipt=receipt;report.passed=true;
  await page.screenshot({path:path.join(output,'picker.png')});
}catch(error){report.error=String(error.message).replaceAll(cfg.token,'[redacted]').replace(/ticket=[^&\s"']+/g,'ticket=[redacted]');if(page)await page.screenshot({path:path.join(output,'failure.png')}).catch(()=>{});}
finally{
  report.failures=failures;report.request_duration_ms=requestDurations;await browser?.close();
  if(session)try{await api(`/api/sessions/${session.id}`,undefined,'DELETE');report.session_cleaned=true;}catch{report.session_cleaned=false;report.passed=false;}
  if(agent)try{await api(`/api/manage/agents/${agent.id}`,undefined,'DELETE');report.agent_cleaned=true;}catch{report.agent_cleaned=false;report.passed=false;}
  await close(fixture);await close(childFixture);
  await writeFile(path.join(output,'summary.json'),JSON.stringify(report,null,2)+'\n',{mode:0o600});
}
console.log(JSON.stringify({passed:report.passed,mode,checks:report.checks.length,hover:report.hover&&{p50_ms:report.hover.p50_ms,p95_ms:report.hover.p95_ms},error:report.error,output}));
if(!report.passed)process.exitCode=1;
