'use strict';
// Isolated integration harness: only temporary profiles and a caller-owned
// loopback fixture. No Studio, Go server, production profile or provider.
const electron=require('electron');
const {app,BrowserWindow}=electron;
const fs=require('node:fs/promises');
const path=require('node:path');
const dir=process.env.POGO_POPUP_DATA_DIR,origin=new URL(process.env.POGO_POPUP_ORIGIN);
if(!path.isAbsolute(dir)||origin.protocol!=='http:'||origin.hostname!=='127.0.0.1')throw new Error('Isolated directory and loopback fixture required');
app.setPath('userData',dir);app.setPath('sessionData',path.join(dir,'session-data'));
app.commandLine.appendSwitch('remote-debugging-port','0');app.commandLine.appendSwitch('remote-debugging-address','127.0.0.1');
let host,worker,browser,window,stopping=false;
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function close(){if(stopping)return;stopping=true;await worker?.close();await host?.close();await browser?.close();app.exit(0);}
app.whenReady().then(async()=>{
  await fs.mkdir(path.join(dir,'session-data'),{recursive:true});
  window=new BrowserWindow({title:'Pogo isolated native popup test',width:1280,height:900,webPreferences:{sandbox:true,nodeIntegration:false,contextIsolation:true}});
  window.on('close',event=>{if(!stopping){event.preventDefault();close().catch(()=>app.exit(1));}});
  await window.loadURL(origin.origin+'/shell');
  let endpoint;
  for(let i=0;i<200&&!endpoint;i++){try{const port=Number((await fs.readFile(path.join(dir,'session-data/DevToolsActivePort'),'utf8')).split('\n')[0]);if(port)endpoint='http://127.0.0.1:'+port;}catch{}if(!endpoint)await wait(20);}
  if(!endpoint)throw new Error('Owned CDP endpoint unavailable');
  const {chromium}=await import('../../browser-runtime/node_modules/playwright/index.mjs');
  browser=await chromium.connectOverCDP(endpoint);
  const {DesktopBrowserHost}=await import('../browser-host.mjs');
  host=new DesktopBrowserHost({electron,browser,window,studioOrigin:origin.origin,namespace:'isolated-popup-lab'});
  const {createWorker}=await import('../../browser-runtime/src/server.js');
  const {inspectNativeNode}=await import('../native-inspector.mjs');
  worker=await createWorker({token:'isolated-popup-fixture-token',sessionFactory:host.factory,allowBrowserLaunch:false,isConnected:()=>browser.isConnected(),actionTimeout:5000,settleMs:500});
  const address=await worker.listen(0);
  global.__popupLab={host,worker,browser,window,url:'http://127.0.0.1:'+address.port,close,inspectNativeNode};
}).catch(error=>{console.error(error);app.exit(1);});
