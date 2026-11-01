// Isolated native host smoke, not production transport/performance acceptance.
import { spawn } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { randomBytes } from 'node:crypto';
import { chromium } from '../node_modules/playwright/index.mjs';
import { LineRPC } from '../src/wayland-rpc.js';
const delay = ms => new Promise(r => setTimeout(r, ms));
const wait = async (fn, ms = 15000) => {const until=Date.now()+ms;while(Date.now()<until){const x=await fn();if(x)return x;await delay(20);}throw new Error('smoke readiness timeout');};
const nonce=()=>randomBytes(16).toString('hex');
const dir='/tmp/pogo-proto-native-smoke'; await mkdir(dir,{mode:0o700,recursive:true});
const frames=[];let parseError='';
const server=createServer(socket=>{
 let buf=Buffer.alloc(0),hello=false;
 socket.on('data',b=>{buf=Buffer.concat([buf,b]);try{
  if(!hello){const n=buf.indexOf(10);if(n<0)return;const m=JSON.parse(buf.subarray(0,n));if(m.session!=='pogo-proto-smoke'||m.version!==1)throw Error('bad hello');buf=buf.subarray(n+1);hello=true;}
  while(buf.length>=4){const n=buf.readUInt32BE(0);if(n<1||n>65536)throw Error('bad header size');if(buf.length<4+n)return;const h=JSON.parse(buf.subarray(4,4+n));if(h.payload_bytes<1||h.payload_bytes>8388608)throw Error('bad payload size');if(buf.length<4+n+h.payload_bytes)return;const bytes=buf.subarray(4+n,4+n+h.payload_bytes);if(h.codec!=='vp8'||h.keyframe!==(bytes[0]%2===0))throw Error('bad VP8 metadata');if(h.keyframe&&(bytes.subarray(3,6).toString('hex')!=='9d012a'||(bytes.readUInt16LE(6)&16383)!==h.width||(bytes.readUInt16LE(8)&16383)!==h.height))throw Error('bad VP8 access unit');frames.push({...h});buf=buf.subarray(4+n+h.payload_bytes);}
 }catch(e){parseError=e.message;socket.destroy();}});
});
await new Promise(resolve=>server.listen(dir+'/frames.sock',resolve));
const env=Object.fromEntries(['PATH','HOME','TMPDIR','LANG'].filter(k=>process.env[k]).map(k=>[k,process.env[k]]));
const child=spawn('/opt/wl/bin/python3',['-u','/opt/project/wayland/host.py','--session','pogo-proto-smoke','--runtime-dir',dir+'/wl','--frames-socket',dir+'/frames.sock'],{env:{...env,POGO_WAYLAND_SESSION_CAP:nonce()+nonce()},stdio:['pipe','pipe','pipe']});
let errorLog='';child.stderr.on('data',b=>{errorLog=(errorLog+b.toString()).slice(-4000)});
const rpc=new LineRPC(child.stdout,child.stdin,{timeout:20000});
let browser,chrome;
const result={scope:'isolated native capture/input/fence smoke; mock UDS receiver; not WebRTC or performance acceptance',steps:[]};
try {
 const init=await rpc.request('init',{scale:1.25,network:'lan'});result.init=init;
 const profile=dir+'/profile';await mkdir(profile,{mode:0o700,recursive:true});
 const token='pogo-private-'+nonce(), url='data:text/html,'+encodeURIComponent('<title>'+token+'</title>');
 chrome=spawn(chromium.executablePath(),['--disable-infobars','--ozone-platform=wayland','--enable-features=WaylandFractionalScaleV1,WaylandPerSurfaceScale','--disable-background-timer-throttling','--disable-backgrounding-occluded-windows','--disable-renderer-backgrounding','--disable-gpu-compositing','--use-angle=swiftshader','--enable-unsafe-swiftshader','--no-first-run','--disable-popup-blocking','--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1','--user-data-dir='+profile,'--app='+url],{env:{...env,XDG_RUNTIME_DIR:init.xdg_runtime_dir,WAYLAND_DISPLAY:init.wayland_display},stdio:['ignore','ignore','pipe']});
 chrome.stderr.on('data',b=>{errorLog=(errorLog+b.toString()).slice(-4000)});
 const port=await wait(async()=>{try{return Number((await readFile(profile+'/DevToolsActivePort','utf8')).split('\n')[0])}catch{return false}});
 browser=await chromium.connectOverCDP('http://127.0.0.1:'+port);const context=browser.contexts()[0];
 const page=await wait(()=>context.pages().find(p=>p.url()===url));
 const native=await wait(async()=>{const rows=await rpc.request('windows');result.native_windows=rows;return rows.find(w=>(w.title===token||w.title.startsWith(token+' - '))&&w.parked)});
 result.parked_window={width:native.width,height:native.height};
 const bind=await rpc.request('bind_sheet',{page:'t1',window_id:native.window_id,generation:1,width:1280,height:800,scale:1.25});result.binding=bind;

 await wait(async()=>{const g=await page.evaluate(()=>({width:innerWidth,height:innerHeight,dpr:devicePixelRatio}));result.last_geometry=g;return g.width===1280&&g.height===800&&g.dpr===1.25?g:false});
 await page.setContent('<!doctype html><meta charset="utf-8"><title>Owned fixture</title><style>body{margin:0;background:#234;height:6400px;color:white}input{margin:40px;width:500px;height:40px}</style><input id="text"><input id="slider" type="range" min="0" max="100" value="0"><script>window.log=[];for(const k of ["pointerdown","pointermove","pointerup","wheel"])addEventListener(k,e=>log.push({type:k,trusted:e.isTrusted,dy:e.deltaY}));</script>');
 const marker=nonce().slice(0,16);
 await page.evaluate(({marker})=>{const c=document.createElement('canvas');c.id='probe';c.width=c.height=16;c.style.cssText='position:fixed;left:1.6px;top:1.6px;width:12.8px;height:12.8px;z-index:2147483647;image-rendering:pixelated;pointer-events:none';const ctx=c.getContext('2d'),bits=BigInt('0x'+marker);for(let i=0;i<64;i++){ctx.fillStyle=(bits>>BigInt(63-i))&1n?'white':'black';ctx.fillRect(i%8*2,Math.floor(i/8)*2,2,2)}document.body.append(c)},{marker});
 const seen=await rpc.request('probe_present',{page:'t1',nonce:marker,pixel_x:2,pixel_y:2});
 await page.evaluate(()=>document.querySelector('#probe').remove());
 const clean=await rpc.request('probe_absent',{page:'t1',nonce:marker,min_seq:seen.content_seq,pixel_x:2,pixel_y:2});
 if(frames.length)throw Error('private marker capture leaked to IPC');
 result.fence={seen:seen.content_seq,clean:clean.content_seq,private_frames_sent:frames.length};
 await rpc.request('capture_start',{page:'t1',generation:1});
 await wait(()=>frames.length>=3);
 if(frames.some(f=>f.content_seq<clean.content_seq))throw Error('older content escaped clean fence');
 result.stream={frames:frames.length,first:frames[0],last:frames.at(-1)};
 const rect=await page.locator('#slider').boundingBox();
 await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:true,x:rect.x+8,y:rect.y+rect.height/2});
 for(let i=1;i<=15;i++)await rpc.request('pointer',{page:'t1',kind:'move',x:rect.x+8+(rect.width-16)*i/15,y:rect.y+rect.height/2});
 await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:false,x:rect.x+rect.width-8,y:rect.y+rect.height/2});
 result.drag=await page.locator('#slider').inputValue();if(Number(result.drag)<95)throw Error('native drag did not move slider');
 const text=await page.locator('#text').boundingBox();await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:true,x:text.x+40,y:text.y+20});await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:false,x:text.x+40,y:text.y+20});
 const cdp=await context.newCDPSession(page);await cdp.send('Input.insertText',{text:'你好，Pogo'});result.committed_text=await page.locator('#text').inputValue();if(result.committed_text!=='你好，Pogo')throw Error('CDP committed Chinese text failed');
 await rpc.request('pointer',{page:'t1',kind:'wheel',x:1000,y:400,dx:0,dy:120,modifiers:{}});await delay(800);
 result.inputs=await page.evaluate(()=>({scrollY,events:log}));if(result.inputs.scrollY<=0||!result.inputs.events.some(e=>e.type==='wheel'&&e.trusted))throw Error('native trusted wheel did not scroll');

 // Second Sheet stays alive without capture; releasing it cannot steal A's drag.
 const token2='pogo-private-'+nonce(),url2='data:text/html,'+encodeURIComponent('<title>'+token2+'</title>');
 const second=spawn(chromium.executablePath(),['--disable-infobars','--user-data-dir='+profile,'--app='+url2],{env:{...env,XDG_RUNTIME_DIR:init.xdg_runtime_dir,WAYLAND_DISPLAY:init.wayland_display},stdio:['ignore','ignore','pipe']});second.stderr.on('data',()=>{});
 const page2=await wait(()=>context.pages().find(p=>p.url()===url2));
 const native2=await wait(async()=>{const rows=await rpc.request('windows');return rows.find(w=>w.title===token2&&w.parked)});
 const bind2=await rpc.request('bind_sheet',{page:'t2',window_id:native2.window_id,generation:1,width:1280,height:800,scale:1.25});
 await wait(async()=>{const g=await page2.evaluate(()=>({w:innerWidth,h:innerHeight,r:devicePixelRatio}));return g.w===1280&&g.h===800&&g.r===1.25});
 await page2.setContent('<title>Background fixture</title><script>window.raf=0;function tick(){raf++;requestAnimationFrame(tick)}requestAnimationFrame(tick)</script>');
 const before=await page2.evaluate(()=>raf);await delay(400);const after=await page2.evaluate(()=>raf);if(after-before<5)throw Error('uncaptured background frame callbacks stalled');
 await page.evaluate(()=>scrollTo(0,0));await delay(300);const dr=await page.locator('#slider').boundingBox();
 await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:true,x:dr.x+8,y:dr.y+dr.height/2});
 await rpc.request('release',{page:'t2'});
 await rpc.request('pointer',{page:'t1',kind:'move',x:dr.x+dr.width-8,y:dr.y+dr.height/2});
 await rpc.request('pointer',{page:'t1',kind:'button',button:'left',pressed:false,x:dr.x+dr.width-8,y:dr.y+dr.height/2});
 if(Number(await page.locator('#slider').inputValue())<95)throw Error('unrelated Sheet release broke active drag');
 result.multisheet={background_raf_delta:after-before,second_display:bind2.display_id,unrelated_release_drag_passed:true};
 const bc=await browser.newBrowserCDPSession(),popToken='pogo-private-'+nonce(),popURL='data:text/html,'+encodeURIComponent('<title>'+popToken+'</title>');
 const target=await bc.send('Target.createTarget',{url:popURL,newWindow:true});
 const popup=await wait(()=>context.pages().find(p=>p.url()===popURL));
 const win=await bc.send('Browser.getWindowForTarget',{targetId:target.targetId});
 const parked=await wait(async()=>{const rows=await rpc.request('windows');return rows.find(w=>(w.title===popToken||w.title.startsWith(popToken+' - '))&&w.parked)});
 for(const dims of [{width:823,height:487},{width:911,height:559}]){
  await bc.send('Browser.setWindowBounds',{windowId:win.windowId,bounds:{windowState:'normal'}});
  await bc.send('Browser.setWindowBounds',{windowId:win.windowId,bounds:dims});
  await wait(async()=>{const rows=await rpc.request('windows');result.popup_challenge={desired:dims,actual:rows.find(w=>w.window_id===parked.window_id),cdp:await bc.send('Browser.getWindowBounds',{windowId:win.windowId})};return rows.find(w=>w.window_id===parked.window_id&&w.parked&&w.outer_width===dims.width&&w.outer_height===dims.height)});
 }
 const bind3=await rpc.request('bind_sheet',{page:'t3',window_id:parked.window_id,generation:1,width:1280,height:800,scale:1.25});
 await wait(async()=>{const g=await popup.evaluate(()=>({w:innerWidth,h:innerHeight,r:devicePixelRatio}));return g.w===1280&&g.h===800&&g.r===1.25});
 result.popup={double_geometry_challenge:true,bound_display:bind3.display_id,fullscreen_exact_viewport:true};
 await page2.close();await rpc.request('unbind_sheet',{page:'t2'});
 await popup.close();await rpc.request('unbind_sheet',{page:'t3'});
 await rpc.request('capture_stop',{page:'t1'});
 result.status=await rpc.request('status');result.passed=true;
} catch(e) {result.passed=false;result.error=e.message;result.native_stderr=errorLog;throw e}
finally {await writeFile('/evidence/native-smoke-v10.json',JSON.stringify(result,null,2));await rpc.request('shutdown').catch(()=>{});await browser?.close().catch(()=>{});chrome?.kill('SIGTERM');child.kill('SIGTERM');server.close();}
