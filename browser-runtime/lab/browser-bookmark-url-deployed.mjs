#!/usr/bin/env node
// Built UI -> real Go/Chromium; isolated storage and exclusively owned
// agent/session/nonce-scoped local fixture. No model calls or live user pages.
import assert from 'node:assert/strict';
import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { chromium } from 'playwright';
import { httpTransport, loopbackOrigin } from './native-only-deployed.mjs';
const cfg=JSON.parse(await readFile(process.argv[2],'utf8'));
const origin=loopbackOrigin(cfg.appURL),output=process.argv[3];
assert.ok(output&&!output.includes('/private/'),'fresh evidence directory required');
await mkdir(output,{recursive:false,mode:0o700});
const api=httpTransport(origin,{Authorization:`Bearer ${cfg.token}`,...(cfg.releaseToken?{'X-Pogo-Release':cfg.releaseToken}:{})});
const report={scope:'Real built UI/Go/Chromium; hostname validation and failed-document input; owned synthetic session only',passed:false,checks:[],cleanup:{}};
const check=(label,value)=>{assert.ok(value,label);report.checks.push(label);};
const good=(r,label)=>{assert.ok(r.status>=200&&r.status<300,`${label}: HTTP ${r.status}`);return r.body;};
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn,label,timeout=15000){const end=Date.now()+timeout;while(Date.now()<end){if(await fn())return;await sleep(80);}throw new Error(label);}
const nonce=randomUUID(),prefix=`/bookmark-url-${nonce}`,counts={site:0,drop:0,writes:0};
let site,browser,context,agent,sid;
try{
 site=http.createServer((req,res)=>{const path=new URL(req.url,'http://fixture.invalid').pathname;if(req.method!=='GET'){counts.writes++;res.writeHead(405).end();return;}if(path===`${prefix}/drop`){counts.drop++;req.socket.destroy();return;}if(path!==`${prefix}/503`){res.writeHead(404).end();return;}counts.site++;res.writeHead(503,{'Content-Type':'text/html; charset=utf-8','Cache-Control':'no-store'});res.end('<!doctype html><title>HTTP503 fixture</title><body style="background:#d9f0e6;font:24px system-ui"><h1>Real HTTP503 document</h1><button>Ordinary page button</button></body>');});
 await new Promise(resolve=>site.listen(0,'0.0.0.0',resolve));
 const validURL=`http://host.docker.internal:${site.address().port}${prefix}/503`,dropURL=`http://host.docker.internal:${site.address().port}${prefix}/drop`;
 agent=good(await api('/api/manage/agents','POST',{name:`Bookmark URL ${nonce.slice(0,8)}`,description:'Temporary owned UI validation',instructions:'Controlled acceptance only',model:cfg.model,models:[cfg.model],skills:[],mcp_servers:[],status:'ready'}),'owned agent');
 sid=good(await api(`/api/apps/${agent.id}/sessions`,'POST',{}),'owned session').id;
 const base=`/api/sessions/${sid}/browser`,initial=good(await api(base),'discovery'),ref=initial.context_ref,tab=initial.active_tab;
 const state=async()=>good(await api(`${base}?${new URLSearchParams({context_ref:ref,tab_id:tab})}`),'bound state');
 browser=await chromium.launch({headless:true});context=await browser.newContext({viewport:{width:1540,height:1000},locale:'zh-CN'});const page=await context.newPage();
 const errors=[],denied=[],inputs=[],controls=[];
 await context.addInitScript(({app,token,key})=>{if(location.origin!==app)return;sessionStorage.setItem('pogo-token',token);localStorage.setItem(`pogo:studio:${key}:active-sheet`,'hub');if(!localStorage.getItem('bookmark-url-seeded')){localStorage.setItem('pogo:browser-bookmarks:v1',JSON.stringify([{id:'invalid-host-fixture',title:'Invalid hostname fixture',url:'http://www,example.com/'}]));localStorage.setItem('bookmark-url-seeded','1');}},{app:origin,token:cfg.token,key:agent.id});
 page.on('pageerror',()=>errors.push('client exception'));
 await page.route('**/*',async route=>{const req=route.request(),u=new URL(req.url()),session=/^\/api\/sessions\/([^/]+)/.exec(u.pathname);if(u.origin!==origin||(session&&!['activity','search','sample'].includes(session[1])&&session[1]!==sid)||u.pathname.endsWith('/messages')){denied.push('outside owned surface');await route.abort();return;}if(u.pathname===`${base}/input`)inputs.push(req.postDataJSON());if(u.pathname===`${base}/control`)controls.push(req.postDataJSON().mode);await route.continue({headers:{...req.headers(),...(cfg.releaseToken?{'X-Pogo-Release':cfg.releaseToken}:{})}});});
 const pane=()=>page.locator('.native-browser:visible'),form=()=>pane().locator('.bookmark-form'),address=()=>pane().getByRole('textbox',{name:'原站地址',exact:true});
 await page.goto(`${origin}/manage/agents/${agent.id}/chat?c=${sid}`,{waitUntil:'domcontentloaded'});await pane().getByTestId('browser-new-tab').waitFor();
 await pane().locator('[data-testid="browser-bookmark-card"][data-bookmark-id="invalid-host-fixture"]').click();await form().waitFor();
 check('old malformed hostname opens its original value in edit without browser operations',await form().locator('input').nth(1).inputValue()==='http://www,example.com/'&&inputs.length===0&&controls.length===0);
 check('validation explains a hostname example and comma mistake',(await pane().locator('.browser-bookmarks').innerText()).includes('www.example.com')&&(await pane().locator('.browser-bookmarks').innerText()).includes('逗号'));
 await form().locator('button[type=submit]').click();
 check('invalid bookmark save neither closes form nor rewrites old data',await form().isVisible()&&await page.evaluate(()=>JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1')).find(c=>c.id==='invalid-host-fixture').url)==='http://www,example.com/');
 await form().locator('input').nth(1).fill(validURL);await form().locator('button[type=submit]').click();
 check('explicit correction preserves the chosen HTTP scheme and port',await page.evaluate(()=>JSON.parse(localStorage.getItem('pogo:browser-bookmarks:v1')).find(c=>c.id==='invalid-host-fixture').url)===validURL);
 await pane().locator('.browser-bookmarks').getByRole('button',{name:'收起',exact:true}).click();
 await pane().locator('[data-testid="browser-bookmark-card"][data-bookmark-id="invalid-host-fixture"]').click();await pane().getByRole('button',{name:'接管并打开',exact:true}).click();
 await until(async()=>{const s=await state();return s.url===validURL&&!s.page_loading;},'corrected bookmark reaches fixture');await pane().locator('.native-page:not(.readonly)').waitFor();
 check('real HTTP503 remains an ordinary interactive page',(await state()).page_load_error===undefined&&await pane().getByRole('button',{name:'选取页面元素',exact:true}).isEnabled()&&await pane().locator('.native-text textarea').isEnabled());
 check('corrected bookmark opens exactly once in its bound native page',inputs.length===1&&inputs[0].url===validURL&&inputs[0].target_tab===tab&&inputs[0].context_ref===ref&&counts.site===1);
 const before=inputs.length;
 await address().fill('http://www，example.com/');await address().press('Enter');await pane().locator('.native-error').waitFor();
 check('full-width comma in address bar is rejected before any input request',inputs.length===before);
 await page.screenshot({path:`${output}/hostname-validation.png`});
 await address().fill(dropURL);await address().press('Enter');
 await until(async()=>{const s=await state();return !!s.page_load_error||s.url.startsWith('chrome-error://');},'synthetic transport failure recorded');await pane().locator('.native-page.readonly').waitFor();
 await until(async()=>await pane().locator('.native-text textarea').isDisabled(),'failed document input state reaches UI');
 check('transport error document cannot be picked or typed into',await pane().getByRole('button',{name:'选取页面元素',exact:true}).isDisabled()&&await pane().locator('.native-text textarea').isDisabled());
 const failedInputs=inputs.length;
 const img=pane().locator('.native-page img');if(await img.count())await img.click();await pane().locator('.native-page').focus();await page.keyboard.press('Enter');await pane().locator('.native-page').hover();await page.mouse.wheel(0,50);await sleep(250);
 check('error page forwards no click key or scroll and shows no action overlays',inputs.length===failedInputs&&await pane().locator('.native-overlay > div').count()===0);
 check('address navigation remains available for explicit correction',await address().evaluate(el=>!el.readOnly));
 await page.screenshot({path:`${output}/error-document-readonly.png`});
 check('test performed no website writes, browser exceptions or outside-session requests',counts.writes===0&&errors.length===0&&denied.length===0);
 report.passed=true;
}catch(error){report.error=String(error.message).split('\n')[0].slice(0,300);try{await context?.pages()[0]?.screenshot({path:`${output}/failure.png`});}catch{}}
finally{await context?.close();await browser?.close();if(sid){try{good(await api(`/api/sessions/${sid}`,'DELETE'),'session cleanup');assert.equal((await api(`/api/sessions/${sid}`)).status,404);report.cleanup.session='deleted and verified';}catch{report.cleanup.session='failed';report.passed=false;}}if(agent){try{good(await api(`/api/manage/agents/${agent.id}`,'DELETE'),'agent cleanup');report.cleanup.agent='retired';}catch{report.cleanup.agent='failed';report.passed=false;}}if(site){site.closeAllConnections();await new Promise(resolve=>site.close(resolve));}report.counters=counts;await writeFile(`${output}/summary.json`,JSON.stringify(report,null,2),{mode:0o600});}
console.log(JSON.stringify({passed:report.passed,checks:report.checks.length,output}));process.exitCode=report.passed?0:1;
