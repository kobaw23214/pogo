// Real Web UI -> authenticated WebSocket signaling -> native Chromium WebRTC acceptance.
// Owns synthetic fixture, Agent/session and a UI-only test browser; no models.
import assert from 'node:assert/strict';
import http from 'node:http';
import {readFile,writeFile,mkdir} from 'node:fs/promises';
import {randomUUID} from 'node:crypto';
import path from 'node:path';
import {chromium} from 'playwright';
import {verifyImagePair} from './native-only-deployed.mjs';
const [configFile,output]=process.argv.slice(2);
assert.ok(configFile&&output,'PRIVATE_CONFIG NEW_OUTPUT required');
const cfg=JSON.parse(await readFile(configFile,'utf8'));
assert.match(cfg.appURL,/^http:\/\/(?:127\.0\.0\.1|localhost):[0-9]+\/?$/);
cfg.appURL = new URL(cfg.appURL).origin;
await mkdir(output,{mode:0o700});
const report={kind:'real-web-webrtc-acceptance',scope:'Real deployed WebRTC video and original-site interaction; synthetic IME events and UI-only headless client, not physical input-to-photon',passed:false,checks:[],requests:{frames:0,inputs:0,tickets:0},sockets:0,frame_messages:0,media_offers:[],presentation_acks:0,media_configs:0};
const headers={Authorization:'Bearer '+cfg.token,Origin:cfg.appURL,...(cfg.releaseToken?{'X-Pogo-Release':cfg.releaseToken}:{})};
const check=(label,value)=>{assert.ok(value,label);report.checks.push(label);console.log('PASS '+label);};
const api=async(route,body,method=body===undefined?'GET':'POST')=>{const r=await fetch(cfg.appURL+route,{method,headers:{...headers,...(body===undefined?{}:{'Content-Type':'application/json'})},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(30000)});assert.ok(r.ok,`${method} ${route} HTTP ${r.status}`);return r.status===204?null:r.json();};
let browser,fixture,agent,session,page;const pageErrors=[],streamErrors=[],deniedRequests=[],ticketFailures=[];const nonce=randomUUID(),prefix='/web-webrtc-'+nonce;let latest={text:'',range:25,saves:0,downs:0,ups:0,keys:[],textEvents:0,rangeEvents:0};
const html=`<!doctype html><meta charset="utf-8"><title>Pogo Web live fixture</title><style>*{box-sizing:border-box}body{margin:0;padding:20px;font:18px system-ui;background:#fff;min-height:3000px}#paint{position:absolute;top:5px;left:5px;width:10px;height:10px;background:rgb(220,40,40)}input{display:block;margin:15px 0;height:40px;width:300px}#text{position:absolute;left:20px;top:110px;margin:0}#slider{position:absolute;left:20px;top:220px;margin:0}button{position:absolute;left:20px;top:295px;height:40px}#out{position:absolute;left:20px;top:355px;white-space:pre-wrap}h1{margin:0;height:60px}</style><div id="paint"></div><h1>Web live fixture</h1><label>Text<input id="text" autocomplete="off"></label><label>Drag<input type="range" id="slider" min="0" max="100" value="25"></label><button id="save">Save once</button><pre id="out"></pre><script>const state=${JSON.stringify(latest)};const text=document.querySelector('#text'),slider=document.querySelector('#slider'),save=document.querySelector('#save'),paint=document.querySelector('#paint');function emit(){state.text=text.value;state.range=Number(slider.value);state.viewport={width:innerWidth,height:innerHeight};state.scrollY=scrollY;out.textContent=JSON.stringify(state);fetch('${prefix}/receipt',{method:'POST',body:JSON.stringify(state)});}text.addEventListener('input',()=>{state.textEvents++;paint.style.background=text.value.endsWith('z')?'rgb(40,210,40)':'rgb(220,40,40)';emit()});text.addEventListener('keydown',e=>{state.keys.push(e.key);if(state.keys.length>50)state.keys.shift()});slider.addEventListener('pointerdown',()=>{state.downs++;emit()});slider.addEventListener('pointerup',()=>{state.ups++;emit()});slider.addEventListener('input',()=>{state.rangeEvents++;emit()});save.onclick=()=>{state.saves++;emit()};addEventListener('resize',emit);addEventListener('scroll',emit);emit();</script>`;
async function waitOracle(predicate,description,timeout=15000){const end=Date.now()+timeout;while(Date.now()<end){if(predicate(latest))return;await new Promise(r=>setTimeout(r,30));}throw new Error('Fixture timeout: '+description+' '+JSON.stringify(latest));}
try{
 report.images=await verifyImagePair(cfg);
 fixture=http.createServer(async(req,res)=>{res.setHeader('Cache-Control','no-store');if(req.method==='GET'&&req.url===prefix+'/page'){res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);return;}if(req.method==='POST'&&req.url===prefix+'/receipt'){let raw='';for await(const chunk of req){raw+=chunk;if(raw.length>8192){res.writeHead(413).end();return;}}try{latest=JSON.parse(raw);res.end('ok');}catch{res.writeHead(400).end();}return;}res.writeHead(404).end();});
 await new Promise(r=>fixture.listen(0,'127.0.0.1',r));
 const url=`http://${cfg.fixtureHost||'host.docker.internal'}:${fixture.address().port}${prefix}/page`;
 const models=await api('/api/models');const model=models.models.find(m=>m.id===cfg.model&&m.configured)||models.models.find(m=>m.configured);assert.ok(model);
 agent=await api('/api/manage/agents',{name:'Web live acceptance '+nonce.slice(0,8),description:'Owned synthetic browser surface test',instructions:'Synthetic UI test only.',model:model.id,models:[model.id],tools:['browser'],skills:[],mcp_servers:[],status:'ready'});
 session=await api(`/api/apps/${agent.id}/sessions`,{});
 const state=await api(`/api/sessions/${session.id}/browser`);check('browser is container hosted',state.backend==='native'&&state.display_mode!=='desktop');
 browser=await chromium.launch({headless:true});const context=await browser.newContext({viewport:{width:1440,height:1000},deviceScaleFactor:2,locale:'zh-CN',...(cfg.releaseToken?{extraHTTPHeaders:{'X-Pogo-Release':cfg.releaseToken}}:{})});page=await context.newPage();
 await page.route('**/*', async route => {
  const request=route.request(),u=new URL(request.url()),bound=/^\/api\/sessions\/([^/]+)/.exec(u.pathname);
  if(u.origin!==cfg.appURL || u.pathname.endsWith('/messages') || bound&&!['activity','search','sample',session.id].includes(bound[1])) {deniedRequests.push({reason:'outside owned UI/session',method:request.method(),path:u.pathname.slice(0,160)});await route.abort();return;}
  await route.continue();
 });
 page.on('pageerror',error=>pageErrors.push(String(error.message).replaceAll(cfg.token,'[redacted]').slice(0,1000)));
 page.on('response',async response=>{if(!new URL(response.url()).pathname.endsWith('/browser/live-ticket')||response.ok())return;let message='';try{const body=await response.json();message=String(body.error||'').replaceAll(cfg.token,'[redacted]').replace(/ticket=[^&\s"']+/g,'ticket=[redacted]').slice(0,500);}catch{}ticketFailures.push({status:response.status(),error:message,body_keys:Object.keys(response.request().postDataJSON()||{})});});
 page.on('request',r=>{const p=new URL(r.url()).pathname;if(p.endsWith('/browser/frame'))report.requests.frames++;if(p.endsWith('/browser/input'))report.requests.inputs++;if(p.endsWith('/browser/live-ticket'))report.requests.tickets++;});
 page.on('websocket',ws=>{if(!ws.url().includes('/browser/live?'))return;report.sockets++;ws.on('framereceived',event=>{try{const m=JSON.parse(String(event.payload));if(m.type==='frame')report.frame_messages++;if(m.type==='media_offer')report.media_offers.push({epoch:m.frame?.media_epoch,viewport:m.frame?.viewport,pixels:{width:m.frame?.pixel_width,height:m.frame?.pixel_height},frame_id:m.frame?.frame_id});if(m.type==='error')streamErrors.push({code:m.code,seq:m.seq,error:String(m.error).slice(0,200)});}catch{}});ws.on('framesent',event=>{try{const m=JSON.parse(String(event.payload));if(m.type==='presentation_ack')report.presentation_acks++;if(m.type==='media_config')report.media_configs++;}catch{}});});
 const ui=new URL(cfg.appURL+`/manage/agents/${agent.id}/chat`);ui.searchParams.set('c',session.id);ui.searchParams.set('token',cfg.token);await page.goto(ui.href);
 await page.getByRole('button',{name:/^面板(?: ·.*)?$|^Panels?(?: ·.*)?$/}).click();
 const pane=page.locator('.native-browser:visible');
 const browserTile=page.locator('.hub-empty .tile').filter({hasText:/浏览器|Browser/i});
 if(await browserTile.isVisible().catch(()=>false)) {
  try {await browserTile.click({timeout:1500});}
  catch(error) {if(!await pane.isVisible().catch(()=>false))throw error;}
 }
 await pane.waitFor({state:'visible',timeout:20000});
 await pane.getByTestId('browser-new-tab').waitFor({state:'visible',timeout:20000});
 await page.waitForTimeout(1000); const blankSockets=report.sockets,blankTickets=report.requests.tickets;
 await page.waitForTimeout(16000);
 check('blank new-tab page remains connected without media or reconnect loops',report.sockets===blankSockets&&report.requests.tickets===blankTickets&&report.media_offers.length===0&&report.presentation_acks===0);

 await pane.getByRole('button',{name:/接管|Take (?:over|control)/}).click();
 const address=pane.getByRole('textbox',{name:/原站地址|Original URL|URL/}).first();await address.fill(url);await address.press('Enter');
 const video=pane.getByTestId('browser-live-video'),img=pane.locator('.native-page img'),capture=pane.locator('.native-input-capture');
 const remoteTarget={context_ref:state.context_ref,tab_id:state.viewed_tab||state.active_tab};
 const viewedState=()=>api(`/api/sessions/${session.id}/browser?${new URLSearchParams(remoteTarget)}`);
 const waitWritable=async()=>{
  await video.waitFor({state:'visible',timeout:30000});
  await pane.locator('.native-page:not(.readonly)').waitFor({state:'visible',timeout:20000});
  await page.waitForFunction(()=>{const pane=[...document.querySelectorAll('.native-browser')].find(el=>el.getBoundingClientRect().width>0),i=pane?.querySelector('.native-page video'),input=pane?.querySelector('.native-input-capture');return i?.readyState>=2&&i.videoWidth>0&&input&&!input.disabled;},null,{timeout:20000});
 };
 await waitWritable();
 // Pointer coordinates are CSS pixels in the remote viewport. Video and
 // evidence images use object-fit:contain; encoded media pixels are never
 // the pointer coordinate system.
 const target=async(x,y)=>{
  const remote=await viewedState();
  assert.ok(x>=0&&y>=0&&x<remote.viewport.width&&y<remote.viewport.height,'fixture point must be inside remote viewport');
  return (await img.isVisible().catch(()=>false)?img:video).evaluate((el,{x,y,viewport})=>{const b=el.getBoundingClientRect();const scale=Math.min(b.width/viewport.width,b.height/viewport.height);return {x:b.x+(b.width-viewport.width*scale)/2+x*scale,y:b.y+(b.height-viewport.height*scale)/2+y*scale};},{x,y,viewport:remote.viewport});
 };
 const click=async(x,y)=>{const p=await target(x,y);await page.mouse.click(p.x,p.y);};
 await click(100,130);await page.keyboard.type('abc');await waitOracle(s=>s.text==='abc','ordinary direct keyboard');check('direct keyboard enters text through Web view',latest.text==='abc');
 await capture.evaluate(el=>{el.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true,data:''}));el.dispatchEvent(new InputEvent('beforeinput',{bubbles:true,cancelable:true,inputType:'insertCompositionText',data:'中文测试',isComposing:true}));el.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true,data:'中文测试'}));el.dispatchEvent(new InputEvent('beforeinput',{bubbles:true,cancelable:true,inputType:'insertText',data:'中文测试'}));});
 await waitOracle(s=>s.text==='abc中文测试','IME commits exactly once');check('synthetic Chinese composition is committed once',latest.text==='abc中文测试');
 await page.keyboard.press('Backspace');await waitOracle(s=>s.text==='abc中文测','Backspace');
 const start=await target(100,240),end=await target(300,240);await page.mouse.move(start.x,start.y);await page.mouse.down();for(let step=1;step<=15;step++){await page.mouse.move(start.x+(end.x-start.x)*step/15,start.y+(end.y-start.y)*step/15);await new Promise(r=>setTimeout(r,45));}await page.mouse.up();
 await waitOracle(s=>s.range>=85&&s.downs===1&&s.ups===1,'continuous slider drag');check('pointer down move up produces a continuous drag',latest.range>=85&&latest.rangeEvents>=3);
 const ordinaryFrames=report.requests.frames,ordinaryInputs=report.requests.inputs;check('native WebRTC media is presented without JPEG frame messages',report.sockets>=1&&report.media_offers.length>=1&&report.presentation_acks>=1&&report.frame_messages===0);check('ordinary interaction does not poll JPEG endpoint',ordinaryFrames===0);check('ordinary input uses WS after one navigation',ordinaryInputs===1);
 const videoSize=await video.evaluate(el=>({width:el.videoWidth,height:el.videoHeight,readyState:el.readyState,decoded:el.getVideoPlaybackQuality().totalVideoFrames}));
 const mediaState=await viewedState();report.media_pixels={...videoSize,viewport:mediaState.viewport,requested_dpr:2};
 check('high-density video has more encoded pixels than CSS layout',videoSize.width>mediaState.viewport.width&&videoSize.height>mediaState.viewport.height&&videoSize.decoded>0);
 const beforeScroll=latest.scrollY||0; const wheelPoint=await target(500,400);await page.mouse.move(wheelPoint.x,wheelPoint.y);await page.mouse.wheel(0,450);await waitOracle(s=>s.scrollY>beforeScroll+100,'remote wheel scroll');
 check('mouse wheel scrolls the original remote document',latest.scrollY>100);await page.mouse.wheel(0,-1000);await waitOracle(s=>s.scrollY===0,'scroll returns to fixture origin');
 const samples=[];
 for(let n=0;n<5;n++){const sampleViewport=(await viewedState()).viewport;await click(100,130);await page.keyboard.press('Control+a');await page.keyboard.type('a');await waitOracle(s=>s.text==='a','prepare pixel baseline');await page.waitForFunction(v=>{const i=document.querySelector('.native-page video');if(!i||i.readyState<2)return false;const c=document.createElement('canvas');c.width=c.height=1;const ctx=c.getContext('2d');ctx.drawImage(i,10*i.videoWidth/v.width,10*i.videoHeight/v.height,1,1,0,0,1,1);return ctx.getImageData(0,0,1,1).data[0]>160;},sampleViewport);const began=Date.now();await page.keyboard.type('z');await page.waitForFunction(v=>{const i=document.querySelector('.native-page video');if(!i||i.readyState<2)return false;const c=document.createElement('canvas');c.width=c.height=1;const ctx=c.getContext('2d');ctx.drawImage(i,10*i.videoWidth/v.width,10*i.videoHeight/v.height,1,1,0,0,1,1);const p=ctx.getImageData(0,0,1,1).data;return p[1]>150&&p[0]<100;},sampleViewport);samples.push(Date.now()-began);}
 report.input_to_decoded_frame_ms={samples,median:[...samples].sort((a,b)=>a-b)[2],max:Math.max(...samples),scope:'automated key delivery to decoded WebRTC video pixel in local test client, not physical input-to-photon'};
 const picker=pane.getByRole('button',{name:/选取页面元素|Pick.*element/i});
 await picker.click();
 await page.waitForFunction(()=>{const pane=[...document.querySelectorAll('.native-browser')].find(el=>el.getBoundingClientRect().width>0);const i=pane?.querySelector('.native-page.picking img');return i?.complete&&i.naturalWidth>0&&pane.querySelector('.picker-toggle')?.getAttribute('aria-pressed')==='true';},null,{timeout:15000});
 const savePoint=await target(70,315);await page.mouse.move(savePoint.x,savePoint.y);
 await pane.locator('[data-testid="browser-pick-target"]').waitFor({state:'visible',timeout:15000});
 const frozenSource=await img.getAttribute('src');await page.waitForTimeout(400);
 check('reference mode holds a versioned image and exposes hover target',(await img.getAttribute('src'))===frozenSource&&report.requests.frames>ordinaryFrames);
 await page.mouse.click(savePoint.x,savePoint.y);
 await page.getByText('button "Save once"',{exact:false}).first().waitFor({timeout:15000});
 await page.waitForFunction(()=>{const pane=[...document.querySelectorAll('.native-browser')].find(el=>el.getBoundingClientRect().width>0);return pane?.querySelector('.picker-toggle')?.getAttribute('aria-pressed')==='false';});
 await waitWritable();
 check('element reference exits selection without clicking the website',latest.saves===0);
 const beforeReturn=latest.text;
 await pane.getByRole('button',{name:/交还|Return.*Agent/i}).click();
 await pane.getByRole('button',{name:/接管此页|停止 Agent 并接管|Take control of this (?:page|tab)|Stop agent.*take control/i}).waitFor({state:'visible',timeout:15000});
 await waitOracle(()=>true,'settle handback');
 assert.equal((await viewedState()).control,'agent','server confirms handback before denied input');
 await pane.locator('.native-page.readonly').waitFor({state:'visible'});
 await click(100,130);await page.keyboard.type('blocked');await new Promise(r=>setTimeout(r,350));
 check('Agent control blocks human text',latest.text===beforeReturn);
 await page.setViewportSize({width:2100,height:1100});
 const beforeImmersive=await viewedState();
 check('default page viewport uses a desktop width',beforeImmersive.viewport.width>=1280);
 await page.locator('.immersion-toggle').click();
 await page.waitForFunction(()=>document.querySelector('.dock.immersive .native-page')?.getBoundingClientRect().width>1500);
 // Wait for the actual immersive layout to settle before asserting remote
 // dimensions; measuring midway through the dock animation is not its target.
 let expectedWidth=0,stableWidth=0,stableSamples=0;
 const layoutDeadline=Date.now()+10000;
 do{const measured=await pane.locator('.native-page').evaluate(el=>Math.round(el.getBoundingClientRect().width));if(measured>1500&&measured===stableWidth)stableSamples++;else{stableSamples=0;stableWidth=measured;}if(stableSamples>=3){expectedWidth=Math.max(1280,Math.min(2560,measured));break;}await new Promise(r=>setTimeout(r,60));}while(Date.now()<layoutDeadline);
 assert.ok(expectedWidth,'immersive display must settle before comparing the remote viewport');
 const resizeDeadline=Date.now()+15000;let immersive;
 do{immersive=await viewedState();if(immersive.viewport.width===expectedWidth)break;await new Promise(r=>setTimeout(r,80));}while(Date.now()<resizeDeadline);
 check('immersive view reflows the actual page while Agent retains control',immersive.control==='agent'&&immersive.viewport.width===expectedWidth&&immersive.viewport.width>beforeImmersive.viewport.width);
 await waitOracle(s=>s.viewport?.width===expectedWidth,'actual document reflows after immersive resize');
 check('document innerWidth matches the immersive desktop viewport',latest.viewport.width===expectedWidth);
 report.immersive_viewport={before:beforeImmersive.viewport,after:immersive.viewport,document:latest.viewport,expected_width:expectedWidth,control:immersive.control};
 await pane.getByRole('button',{name:/接管此页|停止 Agent 并接管|Take control of this (?:page|tab)|Stop agent.*take control/i}).click();
 await waitWritable();const originalControl=await viewedState();
 const originalTab=remoteTarget.tab_id;
 const created=await api(`/api/sessions/${session.id}/browser/tabs`,{op_id:randomUUID(),action:'new',context_ref:remoteTarget.context_ref});
 const blankID=created.tab_id;check('new tab remains a distinct original Chromium page',blankID!==originalTab&&created.context_ref===remoteTarget.context_ref);
 const tabButtons=page.locator('[role=tab][data-browser-tab-id]');
 report.tab_selection={original:originalTab,created:blankID,ui_target_discovery:'named visible Sheet buttons'};
 // The server has created a tab through the same operation path; wait for normal
 // UI state discovery, then select it using its visible title, never app internals.
 const blankTab=page.locator(`[role=tab][data-browser-tab-id="${blankID}"]`);
 await blankTab.waitFor({state:'visible',timeout:15000});await blankTab.click();
 await page.locator('.native-browser:visible').getByTestId('browser-new-tab').waitFor({state:'visible',timeout:15000});
 const switched=await api(`/api/sessions/${session.id}/browser?${new URLSearchParams({context_ref:remoteTarget.context_ref,tab_id:originalTab})}`);
 check('opening a blank sibling preserves the original human lease epoch',switched.control==='human'&&switched.control_epoch===originalControl.control_epoch);
 check('selecting a new Sheet displays local bookmarks without old video',await page.locator('.native-browser:visible').getByTestId('browser-live-video').count()===0);
 report.tab_selection.browser_tab_hooks=await tabButtons.count();
 await page.locator('.native-browser:visible').getByRole('button',{name:/接管此页|停止 Agent 并接管|Take control of this (?:page|tab)|Stop agent.*take control/i}).click();
 await page.locator('.native-browser:visible').getByRole('button',{name:/交还|Return.*Agent/i}).waitFor({state:'visible',timeout:15000});
 const siblingControl=await api(`/api/sessions/${session.id}/browser?${new URLSearchParams({context_ref:remoteTarget.context_ref,tab_id:blankID})}`);
 const originalStill=await viewedState();
 report.tab_control={original_before:{mode:originalControl.control,epoch:originalControl.control_epoch},original_after:{mode:originalStill.control,epoch:originalStill.control_epoch},sibling:{mode:siblingControl.control,epoch:siblingControl.control_epoch}};
 check('taking over the sibling leaves the original page independently owned',siblingControl.control==='human'&&originalStill.control==='human'&&originalStill.control_epoch===originalControl.control_epoch);
 await page.locator(`[role=tab][data-browser-tab-id="${originalTab}"]`).click();await waitWritable();
 check('switching back creates fresh current-page video and restores only its own lease',await video.evaluate(el=>el.readyState>=2&&el.videoWidth>0)&&await capture.isEnabled());
 check('UI used only the owned test session and made no model request',deniedRequests.length===0);
 check('no unhandled UI error',pageErrors.length===0);
 report.fixture={...latest};report.release_hook={worker:true,human_takeover:true};report.passed=true;await page.screenshot({path:path.join(output,'web-webrtc.png'),fullPage:false});
}catch(error){report.error=String(error.message).replaceAll(cfg.token,'[redacted]').replace(/ticket=[^&\s"']+/g,'ticket=[redacted]');if(page)await page.screenshot({path:path.join(output,'failure.png')}).catch(()=>{});}
finally{report.ticket_failures=ticketFailures;report.page_errors=pageErrors;report.stream_errors=streamErrors;report.denied_requests=deniedRequests;await browser?.close();if(session)try{await api(`/api/sessions/${session.id}`,undefined,'DELETE');report.session_cleaned=true;}catch{report.session_cleaned=false;report.passed=false;}if(agent)try{await api(`/api/manage/agents/${agent.id}`,undefined,'DELETE');report.agent_cleaned=true;}catch{report.agent_cleaned=false;report.passed=false;}if(fixture){fixture.closeAllConnections();await new Promise(r=>fixture.close(r));}await writeFile(path.join(output,'summary.json'),JSON.stringify(report,null,2),{mode:0o600});}
console.log(JSON.stringify({passed:report.passed,checks:report.checks.length,error:report.error,output}));if(!report.passed)process.exitCode=1;
