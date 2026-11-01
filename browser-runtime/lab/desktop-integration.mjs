#!/usr/bin/env node
// Owns only an isolated Go test server. Electron is started separately by the
// desktop host harness; this file never starts a browser or changes containers.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here=path.dirname(fileURLToPath(import.meta.url));
const pogo=path.resolve(here,'../..');
const configPath=path.resolve(process.argv[2]||'');
const output=path.resolve(process.argv[3]||'');
assert.ok(process.argv[2]&&process.argv[3],'usage: node desktop-integration.mjs PRIVATE_CONFIG NEW_OUTPUT');
const cfg=JSON.parse(await readFile(configPath,'utf8'));
const worker=new URL(cfg.workerURL||'http://127.0.0.1:19783');
assert.equal(worker.protocol,'http:');assert.equal(worker.hostname,'127.0.0.1');
assert.notEqual(worker.port,'19777','production app port is forbidden');
assert.ok(!worker.username&&!worker.password&&!worker.search&&!worker.hash&&worker.pathname==='/','direct loopback worker required');
assert.ok(/^127\.0\.0\.1:[0-9]+$/.test(cfg.listen||'127.0.0.1:19782'),'Go listen must be loopback');
assert.notEqual(cfg.listen,'127.0.0.1:19777','production app port is forbidden');
assert.ok(path.isAbsolute(cfg.workerTokenFile),'absolute private workerTokenFile required');
const secretStat=await stat(cfg.workerTokenFile);
assert.equal(secretStat.mode&0o077,0,'worker token permissions must be 0600');
assert.ok((await readFile(cfg.workerTokenFile,'utf8')).trim().length>=24,'fresh worker token required');
await mkdir(output,{recursive:false,mode:0o700});
await mkdir(path.join(output,'private'),{mode:0o700});
const json=async(name,data)=>writeFile(path.join(output,name),JSON.stringify(data,null,2)+'\n',{mode:0o600});
const capture=async(command,args)=>new Promise((resolve,reject)=>{
  const child=spawn(command,args,{cwd:pogo,stdio:['ignore','pipe','pipe']});let stdout='',stderr='';
  child.stdout.on('data',chunk=>stdout+=chunk);child.stderr.on('data',chunk=>stderr+=chunk);
  child.on('error',reject);child.on('exit',code=>code===0?resolve(stdout):reject(new Error(`${command} exited ${code}: ${stderr.slice(0,300)}`)));
});
async function containers(){
  try{
    const text=await capture('docker',['inspect','--format','{{json .Name}} {{json .Id}} {{json .Image}} {{json .State.StartedAt}} {{.RestartCount}} {{json .State.Status}}','pogo-base','pogo-pogo-browser-1']);
    return {available:true,identity:text.trim().split('\n').sort()};
  }catch{return {available:false,reason:'Docker production identity unavailable; no container operation attempted'};}
}
const before=await containers();await json('production-containers-before.json',before);
const normalized={workerURL:worker.origin,workerTokenFile:cfg.workerTokenFile,listen:cfg.listen||'127.0.0.1:19782',output,hold:cfg.hold!==false,workerWaitSeconds:cfg.workerWaitSeconds||120};
const privateConfig=path.join(output,'private','go-config.json');await writeFile(privateConfig,JSON.stringify(normalized),{mode:0o600});
const binary=path.join(output,'private','desktop-go.test');
const report={scope:'isolated Go test process; real native HTTP worker supplied externally; no browser launch and no model network',passed:false,output,started_at:new Date().toISOString()};
try{
  const index=await readFile(path.join(pogo,'web/ui/dist/index.html'));
  assert.ok(!index.toString().includes('pogo-ui-not-built'),'build real UI before launching integration');
  report.embedded_ui_index_sha256=createHash('sha256').update(index).digest('hex');
  await capture('go',['test','-c','-o',binary,'./web']);
  assert.equal(createHash('sha256').update(await readFile(path.join(pogo,'web/ui/dist/index.html'))).digest('hex'),report.embedded_ui_index_sha256,'UI index changed while compiling; rebuild in a fresh output directory');
  report.compiled=true;
  const log=path.join(output,'go.log');
  const child=spawn(binary,['-test.run','^TestDesktopBrowserGoIntegration$','-test.count=1','-test.timeout=60m','-test.v'],{cwd:path.join(pogo,'web'),env:{...process.env,POGO_DESKTOP_GO_TEST_CONFIG:privateConfig},stdio:['ignore','pipe','pipe']});
  report.pid=child.pid;await json('launcher.json',report);
  console.log(JSON.stringify({started:true,pid:child.pid,output,goReadyFile:path.join(output,'go-ready.json'),finishFile:path.join(output,'finish.json')}));
  let logWrites=Promise.resolve();
  const append=chunk=>{logWrites=logWrites.then(()=>writeFile(log,chunk,{flag:'a',mode:0o600}));};
  child.stdout.on('data',append);child.stderr.on('data',append);
  const result=await new Promise((resolve,reject)=>{child.on('error',reject);child.on('exit',(code,signal)=>resolve({code,signal}));});
  await logWrites;Object.assign(report,result);
  const summary=JSON.parse(await readFile(path.join(output,'summary.json'),'utf8'));
  report.passed=result.code===0&&summary.passed===true;
  const ready=JSON.parse(await readFile(path.join(output,'go-ready.json'),'utf8'));
  try{await stat(ready.state_root);report.temporary_go_state_removed=false;}catch(error){report.temporary_go_state_removed=error.code==='ENOENT';}
  if(!report.temporary_go_state_removed)report.passed=false;
}catch(error){report.error=String(error.message).slice(0,1000);}
finally{
  const after=await containers();await json('production-containers-after.json',after);
  report.production_containers_unchanged=before.available&&after.available&&JSON.stringify(before.identity)===JSON.stringify(after.identity);
  if(!report.production_containers_unchanged)report.passed=false;
  report.finished_at=new Date().toISOString();await json('launcher.json',report);
}
console.log(JSON.stringify({passed:report.passed,output,productionContainersUnchanged:report.production_containers_unchanged}));
if(!report.passed)process.exitCode=1;
