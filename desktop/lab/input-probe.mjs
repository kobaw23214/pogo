import { _electron, chromium } from '../../browser-runtime/node_modules/playwright/index.mjs';
import { createServer } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
const root=path.resolve(process.argv[2]);await mkdir(root,{recursive:false});
const server=createServer((_,r)=>{r.writeHead(200,{'Content-Type':'text/html'});r.end('<!doctype html><input id="text"><button id="button">click</button>');});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const origin=`http://127.0.0.1:${server.address().port}`;
let app,connection;
try{
 app=await _electron.launch({executablePath:process.env.POGO_POC_ELECTRON,args:[path.resolve('pogo/browser-runtime/lab/desktop-poc/main.cjs')],env:{...process.env,POGO_POC_ORIGIN:origin,POGO_POC_DATA_DIR:path.join(root,'profile')}});
 await app.evaluate(async()=>{while(!global.__pogoPoc)await new Promise(r=>setTimeout(r,20));});
 const wc=await app.evaluate(({webContents})=>{const id=global.__pogoPoc.listTabs().find(t=>t.id==='A').webContentsId;const w=webContents.fromId(id);global.__probe=[];for(const name of ['before-input-event','before-mouse-event'])w.on(name,(e,input)=>{global.__probe.push({name,type:input.type});e.preventDefault();});return id;});
 const a=app.windows().find(p=>p.url().includes('tab=A'));await a.locator('#text').waitFor();
 await a.locator('#text').pressSequentially('abc');await a.locator('#button').click();
 const value=await a.locator('#text').inputValue(),events=await app.evaluate(()=>global.__probe);
 const lines=(await readFile(path.join(root,'profile/session-data/DevToolsActivePort'),'utf8')).trim().split('\n');
 connection=await chromium.connectOverCDP(`http://127.0.0.1:${lines[0]}`);
 const contextPages=connection.contexts().map(c=>c.pages().map(p=>p.url()));
 await writeFile(path.join(root,'result.json'),JSON.stringify({value,events,contextPages,wc},null,2));
 console.log(JSON.stringify({value,events,contextPages}));
}finally{await connection?.close();await app?.close().catch(()=>{});server.closeAllConnections();await new Promise(r=>server.close(r));}
