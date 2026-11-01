// No SDP, ICE, tokens or pages are emitted. Controlled offer/answer negotiation only.
import {spawn} from 'node:child_process';
import {pathToFileURL} from 'node:url';
const {chromium}=await import(pathToFileURL(process.env.POGO_STREAM_PLAYWRIGHT??'/Users/wangy/Documents/DiscoverAgent/pogo/browser-runtime/node_modules/playwright/index.mjs').href);
process.umask(0o077);
const offers=await new Promise((resolve,reject)=>{
 const child=spawn('go',['run','./cmd/codecprobe'],{cwd:new URL('.',import.meta.url),stdio:['ignore','pipe','pipe']});let raw='';
 child.stdout.on('data',b=>{raw+=b;if(raw.length>1024*1024){child.kill();reject(new Error('Probe IPC exceeded bound'))}});
 child.on('error',()=>reject(new Error('Go probe could not start')));child.on('close',code=>{if(code!==0)return reject(new Error('Go probe failed'));try{resolve(JSON.parse(raw))}catch{reject(new Error('Go probe response invalid'))}});
});
const browser=await chromium.launch({headless:true});
try{
 const page=await browser.newPage();
 const results=await page.evaluate(async offers=>{
  const out=[];
  for(const {profile,offer} of offers){const pc=new RTCPeerConnection();try{await pc.setRemoteDescription(offer);const answer=await pc.createAnswer();const h264=[];const media=answer.sdp.split('\r\nm=video ')[1]?.split('\r\nm=')[0]??'';
   for(const line of media.split('\r\n')){const match=/^a=fmtp:(\d+) (.*)$/.exec(line);if(match&&match[2].includes('profile-level-id'))h264.push({pt:Number(match[1]),fmtp:match[2]})}
   out.push({offer_profile:profile,accepted:h264.length>0,answer_h264:h264});
  }catch{out.push({offer_profile:profile,accepted:false,error:'NEGOTIATION_REJECTED'})}finally{pc.close()}}
  return {capabilities:RTCRtpReceiver.getCapabilities('video').codecs.filter(c=>c.mimeType.toLowerCase()==='video/h264').map(c=>({mimeType:c.mimeType,fmtp:c.sdpFmtpLine})),offers:out};
 },offers);
 console.log(JSON.stringify({viewer_browser:browser.version(),...results}));
}finally{await browser.close()}
