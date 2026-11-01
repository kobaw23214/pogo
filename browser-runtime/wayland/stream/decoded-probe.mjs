// Controlled real Chromium/WebRTC presentation probe. No production endpoints.
import {spawn,execFileSync} from 'node:child_process';
import net from 'node:net';
import {mkdtemp,rm,writeFile} from 'node:fs/promises';
import {randomBytes} from 'node:crypto';
import {pathToFileURL,fileURLToPath} from 'node:url';
const {chromium}=await import(pathToFileURL(process.env.POGO_STREAM_PLAYWRIGHT??'/Users/wangy/Documents/DiscoverAgent/pogo/browser-runtime/node_modules/playwright/index.mjs').href);
process.umask(0o077);
const dir=await mkdtemp('/tmp/pogo-stream-decoded-'),token=randomBytes(32).toString('hex'),sessionToken=randomBytes(32).toString('hex');
let child,ctl,frames,browser,timer;let stderr='';
const report={kind:'controlled-pion-vp8-decoded-frame',passed:false,production_accessed:false,physical_latency:false};
try{
 const ivf=execFileSync('ffmpeg',['-v','error','-f','lavfi','-i','color=c=red:s=64x64:r=10','-frames:v','1','-c:v','libvpx','-deadline','realtime','-cpu-used','16','-g','1','-f','ivf','pipe:1'],{maxBuffer:1024*1024});
 const size=ivf.readUInt32LE(32),payload=ivf.subarray(44,44+size);
 // Spawn the executable directly so SIGTERM reaches the actual service;
 // killing the `go run` wrapper alone can leave its child alive.
 execFileSync('go',['build','-o','./build/pogo-wlstream-local','.'],{cwd:new URL('.',import.meta.url)});
 child=spawn(fileURLToPath(new URL('./build/pogo-wlstream-local',import.meta.url)),['--dir',dir,'--udp-range','22980-23011','--network','lan','--playout-delay','zero'],{cwd:new URL('.',import.meta.url),env:{...process.env,POGO_WLSTREAM_TOKEN:token,POGO_WLSTREAM_ICE_SERVERS:'[]'},stdio:['ignore','pipe','pipe']});
 child.stderr.on('data',b=>{stderr=(stderr+b).slice(-8000)});
 await new Promise((resolve,reject)=>{const timeout=setTimeout(()=>reject(new Error('GO_SERVICE_READY_TIMEOUT')),45000);child.stdout.once('data',b=>{clearTimeout(timeout);try{const row=JSON.parse(String(b));row.ready?resolve():reject(new Error('GO_SERVICE_NOT_READY'))}catch{reject(new Error('GO_SERVICE_RESPONSE_INVALID'))}});child.once('exit',()=>{clearTimeout(timeout);reject(new Error('GO_SERVICE_EXITED'))})});
 ctl=net.createConnection(dir+'/ctl.sock');await new Promise((resolve,reject)=>{ctl.once('connect',resolve);ctl.once('error',reject)});
 let buffer='',next=0,ready;const pending=new Map();const readyPromise=new Promise(r=>ready=r);
 ctl.on('data',chunk=>{buffer+=chunk;let pos;while((pos=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,pos);buffer=buffer.slice(pos+1);const row=JSON.parse(line);if(row.type==='ready')ready();if(row.id&&pending.has(row.id)){const entry=pending.get(row.id);pending.delete(row.id);row.ok?entry.resolve(row.result):entry.reject(new Error('RPC_'+row.error))}}});
 ctl.write(JSON.stringify({type:'hello',version:1,token})+'\n');await readyPromise;
 const rpc=(op,args)=>new Promise((resolve,reject)=>{const id='r'+(++next);pending.set(id,{resolve,reject});ctl.write(JSON.stringify({id,op,args})+'\n')});
 const scope={session:'probe-session',page:'t1',view:'v1',epoch:1,generation:1};
 await rpc('register_session',{session:scope.session,token:sessionToken});await rpc('register_page',{session:scope.session,page:scope.page,display_id:2,generation:1});
 frames=net.createConnection(dir+'/frames.sock');await new Promise((resolve,reject)=>{frames.once('connect',resolve);frames.once('error',reject)});frames.on('data',()=>{});frames.write(JSON.stringify({type:'hello',version:1,session:scope.session,token:sessionToken})+'\n');
 const media=await rpc('create',{...scope,codec:'vp8',width:64,height:64,content_seq_min:100});
 browser=await chromium.launch({headless:true});report.viewer_browser=browser.version();const page=await browser.newPage();
 const answer=await page.evaluate(async offer=>{document.body.innerHTML='<video autoplay muted playsinline></video>';const pc=window.pc=new RTCPeerConnection();pc.ontrack=event=>{document.querySelector('video').srcObject=event.streams[0]??new MediaStream([event.track])};await pc.setRemoteDescription(offer);await pc.setLocalDescription(await pc.createAnswer());if(pc.iceGatheringState!=='complete')await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(new Error('CLIENT_GATHER_TIMEOUT')),5000);pc.onicegatheringstatechange=()=>{if(pc.iceGatheringState==='complete'){clearTimeout(timer);resolve()}}});return pc.localDescription.toJSON()},media.offer);
 await rpc('answer',{...scope,peer_id:media.peer_id,answer});await page.waitForFunction(()=>pc.connectionState==='connected',null,{timeout:10000});
 let seq=0;timer=setInterval(()=>{seq++;const ns=process.platform==='darwin'?BigInt(Date.now())*1000000n:process.hrtime.bigint();const header=Buffer.from(JSON.stringify({codec:'vp8',page:scope.page,display_id:2,generation:1,seq,content_seq:100,capture_ns:Number(ns),encode_start_ns:Number(ns),encode_end_ns:Number(ns),width:64,height:64,keyframe:true,frame_id:seq,reference_frame_id:-1,payload_bytes:payload.length}));const length=Buffer.alloc(4);length.writeUInt32BE(header.length);frames.write(Buffer.concat([length,header,payload]))},100);
 await page.waitForFunction(()=>{const v=document.querySelector('video');return v.readyState>=2&&v.videoWidth===64&&v.videoHeight===64&&v.getVideoPlaybackQuality().totalVideoFrames>0},null,{timeout:10000});
 report.presented=await page.evaluate(()=>{const v=document.querySelector('video'),c=document.createElement('canvas');c.width=c.height=1;const ctx=c.getContext('2d');ctx.drawImage(v,32,32,1,1,0,0,1,1);return {width:v.videoWidth,height:v.videoHeight,decoded:v.getVideoPlaybackQuality().totalVideoFrames,pixel:[...ctx.getImageData(0,0,1,1).data]}});
 report.stats=await rpc('stats',{...scope,peer_id:media.peer_id});
 report.passed=report.presented.decoded>0&&report.presented.pixel[0]>180&&report.presented.pixel[1]<80&&report.presented.pixel[2]<80&&report.stats.frames_sent>0&&report.stats.codec==='vp8';
 await rpc('close',{...scope,peer_id:media.peer_id});await rpc('unregister_page',{session:scope.session,page:scope.page});await rpc('unregister_session',{session:scope.session});
}catch(error){report.error=String(error.message).replaceAll(token,'[redacted]').replaceAll(sessionToken,'[redacted]');report.passed=false}
finally{clearInterval(timer);await browser?.close();ctl?.destroy();frames?.destroy();if(child){child.kill('SIGTERM');await new Promise(resolve=>{if(child.exitCode!==null)return resolve();const t=setTimeout(()=>{child.kill('SIGKILL');resolve()},3000);child.once('exit',()=>{clearTimeout(t);resolve()})})};await rm(dir,{recursive:true,force:true});}
await writeFile(new URL(process.argv[2]??'./decoded-probe-result.json',import.meta.url),JSON.stringify(report,null,2)+'\n',{mode:0o600,flag:'wx'});
console.log(JSON.stringify(report));if(!report.passed)process.exitCode=1;
