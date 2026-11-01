import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';
const base = process.argv[2] || 'http://127.0.0.1:5189';
const out = process.argv[3] || '/tmp/pogo-sheets-qa';
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
const checks = [];
const check = (name, passed) => { assert.ok(passed, name); checks.push(name); console.log(`PASS ${name}`); };
try {
 const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
 const errors=[]; page.on('pageerror',e=>errors.push(e.message));
 await page.route(url=>url.pathname.startsWith('/api/'),r=>r.abort());
 await page.goto(`${base}/dev/browser-sheets.html`);
 const pane=()=>page.locator('.native-browser:visible');
 const image=()=>pane().locator('.native-page img');
 const selected=()=>page.locator('[data-browser-tab-id][aria-selected=true]');
 const add=async()=>{await page.getByRole('button',{name:'添加工作表',exact:true}).click();await page.getByRole('menuitem',{name:'浏览器',exact:true}).click();};
 const session=async(name)=>{await page.getByRole('button',{name:new RegExp(`^${name}`)}).click();await image().waitFor();};
 await image().waitFor();
 await add();await page.locator('[data-browser-tab-id=t2][aria-selected=true]').waitFor();await image().waitFor();
 const a2=await selected().getAttribute('data-sheet-id');
 check('one + creates one bound Sheet without changing Agent active page',await page.evaluate(()=>window.__sheetsFixture.context().pages.length===2&&window.__sheetsFixture.context().active==='t1'&&window.__sheetsFixture.controls.length===0));
 // Lost receipt and then explicit pending response must preserve the op ID.
 await page.evaluate(()=>{window.__sheetsFixture.newOutcome='pending';});
 await add();await page.waitForFunction(()=>window.__sheetsFixture.tabCalls.length===2);
 const operation=await page.evaluate(()=>window.__sheetsFixture.tabCalls.at(-1).op_id);
 await add();await page.waitForFunction(()=>window.__sheetsFixture.tabCalls.length===3);
 check('unknown create retry keeps its exact op ID on pending409',await page.evaluate(id=>window.__sheetsFixture.tabCalls.at(-1).op_id===id&&window.__sheetsFixture.context().pages.length===3,operation));
 await session('第二会话');await add();await page.locator('[data-browser-tab-id=t2][aria-selected=true]').waitFor();
 const b2=await selected().getAttribute('data-sheet-id');
 check('different conversations have independent Sheet bindings',a2!==b2&&await page.locator('[data-browser-context-ref="ctx_111111111111111111111111"]').count()===0);
 await session('第一会话');
 check('returning to a conversation restores its selected Sheet',await selected().getAttribute('data-sheet-id')===a2);
 await page.evaluate(()=>{window.__sheetsFixture.pendingResolved=true;});
 await add();await page.locator('[data-browser-tab-id=t3][aria-selected=true]').waitFor();
 check('another conversation create does not overwrite the unresolved operation ID',await page.evaluate(id=>window.__sheetsFixture.tabCalls.at(-1).op_id===id&&window.__sheetsFixture.context().pages.length===3,operation));
 check('operation persistence contains IDs, not lease credentials',await page.evaluate(()=>!sessionStorage.getItem('pogo:browser-new-operations:v1')?.includes('lease')));
 // User selection remains B/t3 when the Agent opens a popup elsewhere.
 await page.evaluate(()=>window.__sheetsFixture.addPopup());
 await page.locator('[data-browser-tab-id=t4]').waitFor();
 check('Agent popup creates another Sheet without stealing selection',await selected().getAttribute('data-browser-tab-id')==='t3');
 // Delay target A's frame, switch to target B, then let A finish.
 await page.evaluate(()=>{window.__sheetsFixture.frameDelay=1000;});
 const oldFrameRequests=await page.evaluate(()=>window.__sheetsFixture.requests.filter(r=>r.path.endsWith('/frame')&&r.tab==='t1').length);
 await page.locator('[data-browser-tab-id=t1]').click();
 await page.waitForFunction(count=>window.__sheetsFixture.requests.filter(r=>r.path.endsWith('/frame')&&r.tab==='t1').length>count,oldFrameRequests);
 await page.locator('[data-browser-tab-id=t2]').click();await image().waitFor();
 await page.waitForTimeout(1300);
 check('late hidden Sheet frames never replace the selected page identity',await selected().getAttribute('data-browser-tab-id')==='t2'&&['','about:blank'].includes(await pane().getByRole('textbox',{name:'原站地址',exact:true}).inputValue()));
 await page.evaluate(()=>{window.__sheetsFixture.frameDelay=0;window.__sheetsFixture.frameWrongTarget=true;});
 await pane().getByRole('button',{name:'刷新画面',exact:true}).click();await pane().locator('.native-frame-status').waitFor();
 check('wrong context/tab frame headers fail closed',await pane().locator('.native-page.readonly').count()===1);
 await page.evaluate(()=>{window.__sheetsFixture.frameWrongTarget=false;});
 await pane().getByRole('button',{name:'刷新画面',exact:true}).click();
 // Save A2, then select B2. Reload directly opens A and must use A's saved id.
 await page.locator('[data-browser-tab-id=t2]').click();
 await session('第二会话');
 check('second conversation restores its own selected Sheet',await selected().getAttribute('data-sheet-id')===b2);
 await page.evaluate(()=>sessionStorage.setItem('qa:browser-contexts',JSON.stringify(window.__sheetsFixture.allContexts())));
 await page.goto(`${base}/dev/browser-sheets.html?restore`);await image().waitFor();
 check('direct reload of A restores A2 even though global last active was B2',await selected().getAttribute('data-sheet-id')===a2);
 check('reload restores bindings without navigation or acquiring a lease',await page.evaluate(()=>window.__sheetsFixture.inputs.length===0&&window.__sheetsFixture.controls.length===0));
 await page.screenshot({path:`${out}/session-restored.png`});
 // Invalid persisted target must stay quarantined rather than attach to t1.
 await page.evaluate(()=>{const key='pogo:hub-tabs';const value=JSON.parse(localStorage.getItem(key));const id='browser:invalid';value.open.push({id,kind:'browser',label:'Invalid binding',closable:true,browserBinding:{sessionId:'sheets-a',context_ref:'invalid',tab_id:'t1'}});value.browserActive['sheets-a']=id;localStorage.setItem(key,JSON.stringify(value));});
 await page.goto(`${base}/dev/browser-sheets.html?restore`);
 await page.locator('[data-sheet-id="browser:invalid"][aria-selected=true]').waitFor();
 await page.locator('.browser-connect:visible .connect-status').waitFor();
 check('invalid binding is retained as unavailable and never retargeted to current page',await page.locator('.native-browser:visible').count()===0&&await page.evaluate(()=>!window.__sheetsFixture.requests.some(r=>r.context==='invalid')));
 check('no escaped API traffic or browser exceptions',errors.length===0&&await page.evaluate(()=>window.__sheetsFixture.denied.length===0));
 await fs.writeFile(`${out}/summary.json`,JSON.stringify({scope:'controlled in-memory fixture only',passed:true,checks},null,2));
} finally { await browser.close(); }
