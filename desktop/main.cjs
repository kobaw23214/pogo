'use strict';
const electron=require('electron');
const {app,BrowserWindow}=electron;
const fs=require('node:fs/promises');
const fsSync=require('node:fs');
const path=require('node:path');

app.setName('Pogo Desktop');
const dataDir=process.env.POGO_DESKTOP_DATA_DIR||path.join(app.getPath('appData'),'Pogo Desktop');
if(!path.isAbsolute(dataDir))throw new Error('POGO_DESKTOP_DATA_DIR must be an absolute private directory');
const configPath=process.env.POGO_DESKTOP_CONFIG||path.join(dataDir,'desktop.json');
const config=fsSync.existsSync(configPath)?JSON.parse(fsSync.readFileSync(configPath,'utf8')):{};
const readSecret=file=>{if(!path.isAbsolute(file))throw new Error('Desktop token file must be absolute');if(process.platform!=='win32'&&(fsSync.statSync(file).mode&0o077))throw new Error('Desktop token file must be private (0600)');return fsSync.readFileSync(file,'utf8').trim();};
const studio=new URL(process.env.POGO_DESKTOP_STUDIO_URL||config.studioURL||'http://127.0.0.1:19777/');
if(!['http:','https:'].includes(studio.protocol)||studio.username||studio.password)throw new Error('Invalid Studio URL');
if(config.studioTokenFile&&!studio.searchParams.has('token'))studio.searchParams.set('token',readSecret(config.studioTokenFile));
const token=process.env.POGO_DESKTOP_WORKER_TOKEN||(config.workerTokenFile?readSecret(config.workerTokenFile):'');
if(!token||token.length<24||/[\r\n]/.test(token))throw new Error('POGO_DESKTOP_WORKER_TOKEN must be a private random token');
delete process.env.POGO_DESKTOP_WORKER_TOKEN;
const port=Number(process.env.POGO_DESKTOP_WORKER_PORT??config.workerPort??0);
if(!Number.isInteger(port)||port<0||port>65535)throw new Error('Invalid worker port');
app.setPath('userData',dataDir);app.setPath('sessionData',path.join(dataDir,'session-data'));
if(!app.requestSingleInstanceLock())app.exit(0);
// A readiness marker belongs to a running process, never the previous launch.
fsSync.rmSync(path.join(dataDir,'ready.json'),{force:true});
fsSync.rmSync(path.join(dataDir,'session-data/DevToolsActivePort'),{force:true});
app.commandLine.appendSwitch('remote-debugging-port','0');app.commandLine.appendSwitch('remote-debugging-address','127.0.0.1');
let window,host,worker,browser,closing=false;
app.on('second-instance',()=>{if(window&&!window.isDestroyed()){if(window.isMinimized())window.restore();window.show();window.focus();}});
const pause=ms=>new Promise(r=>setTimeout(r,ms));
async function shutdown(exitCode=0){if(closing)return;closing=true;await worker?.close().catch(()=>{});await host?.close().catch(()=>{});await browser?.close().catch(()=>{});app.exit(exitCode);}
app.whenReady().then(async()=>{
  await fs.mkdir(path.join(dataDir,'session-data'),{recursive:true,mode:0o700});
  window=new BrowserWindow({title:'Pogo Desktop',width:1440,height:960,minWidth:800,minHeight:600,backgroundColor:'#202020',webPreferences:{preload:path.join(__dirname,'preload.cjs'),nodeIntegration:false,contextIsolation:true,sandbox:true}});
  window.removeMenu();window.on('page-title-updated',event=>event.preventDefault());
  window.webContents.setWindowOpenHandler(()=>({action:'deny'}));
  window.webContents.on('will-navigate',(event,url)=>{if(new URL(url).origin!==studio.origin)event.preventDefault();});
  window.webContents.on('will-redirect',(event,url)=>{if(new URL(url).origin!==studio.origin)event.preventDefault();});
  window.on('close',event=>{if(!closing){event.preventDefault();void shutdown();}});
  await window.loadURL('data:text/html;charset=utf-8,'+encodeURIComponent('<!doctype html><title>Pogo Desktop</title><body style="margin:0;background:#202020;color:#ddd;font:14px system-ui"><div style="padding:16px">Pogo · 正在连接桌面服务…</div></body>'));
  let debugging;
  for(let i=0;i<200;i++){try{const text=await fs.readFile(path.join(dataDir,'session-data/DevToolsActivePort'),'utf8');const lines=text.trim().split('\n');if(Number(lines[0])>0){debugging='http://127.0.0.1:'+lines[0];break;}}catch{}await pause(25);}
  if(!debugging)throw new Error('Desktop debugging transport did not become ready');
  const {chromium}=await import('../browser-runtime/node_modules/playwright/index.mjs');
  browser=await chromium.connectOverCDP(debugging);
  const {DesktopBrowserHost}=await import('./browser-host.mjs');
  host=new DesktopBrowserHost({electron,browser,window,studioOrigin:studio.origin,namespace:studio.origin});host.installIPC(electron.ipcMain);
  const {createWorker}=await import('../browser-runtime/src/server.js');
  worker=await createWorker({token,sessionFactory:host.factory,allowBrowserLaunch:false,isConnected:()=>!closing&&browser.isConnected()});
  const address=await worker.listen(port,'127.0.0.1');
  const ready={pid:process.pid,workerURL:'http://127.0.0.1:'+address.port,studioURL:studio.origin+studio.pathname,transport:'desktop-owned-webcontents',browser:'electron-chromium',tokenIncluded:false};
  await fs.writeFile(path.join(dataDir,'ready.json'),JSON.stringify(ready,null,2),{mode:0o600});
  console.log('Pogo Desktop ready at '+ready.workerURL);
  let loaded=false;
  for(let i=0;i<60&&!closing;i++){try{await window.loadURL(studio.href);loaded=true;break;}catch{await pause(500);}}
  if(!loaded&&!closing)throw new Error('Studio is unreachable; browser worker remains isolated');
  // Studio's lazy route imports may not have read the URL token at did-finish-load.
  // Preserve it in the same tab-scoped store used by client.ts before hiding it.
  if(loaded)await window.webContents.executeJavaScript(`(()=>{if(location.origin!==${JSON.stringify(studio.origin)})throw new Error('Unexpected Studio origin');const u=new URL(location.href),token=u.searchParams.get('token');if(token){sessionStorage.setItem('pogo-token',token);u.searchParams.delete('token');history.replaceState(history.state,'',u.href);}})()`);
  // Test harnesses request shutdown through an owned file; no browser IPC or
  // network shutdown endpoint exists.
  const control=setInterval(async()=>{try{await fs.unlink(path.join(dataDir,'finish'));clearInterval(control);await shutdown();}catch{}},500);control.unref();
}).catch(async error=>{console.error('Pogo Desktop: '+String(error.message));await shutdown(1);});
app.on('window-all-closed',()=>void shutdown());
process.on('SIGINT',()=>void shutdown());process.on('SIGTERM',()=>void shutdown());
