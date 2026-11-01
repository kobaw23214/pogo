import test from 'node:test';
import assert from 'node:assert/strict';
import { Readable, PassThrough } from 'node:stream';
import { readFile, unlink, symlink, access } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserUploads, UploadBudget, MAX_UPLOAD_BYTES, uploadFilename } from '../src/uploads.js';
import { createWorker } from '../src/server.js';
import { BrowserSession, validateAction } from '../src/session.js';

const bytes = value => Readable.from([Buffer.from(value)]);
const stage = (store, text, name = '测试.csv', options = {}) => store.receive(bytes(text), { filename: encodeURIComponent(name), ...options });
function fixture(t, options = {}) {
  const store = new BrowserUploads({ budget: new UploadBudget(1024), maxBytes: 512, maxTotal: 768, ...options });
  t.after(() => store.close()); return store;
}

test('staging is streamed, retains basename and MIME, rejects unsafe metadata without accepting paths', async t => {
  const store = fixture(t);
  const file = await stage(store, '姓名,数量\n张三,12\n');
  assert.equal(file.name, '测试.csv'); assert.equal(file.mime_type, 'text/csv'); assert.equal(file.size, Buffer.byteLength('姓名,数量\n张三,12\n'));
  assert.deepEqual(Object.keys(file), ['id', 'name', 'size', 'mime_type']);
  const held = await store.acquire([file.id]);
  assert.equal((await readFile(held.paths[0])).toString(), '姓名,数量\n张三,12\n');
  assert.ok(held.paths[0].endsWith('/测试.csv')); held.release();
  for (const name of ['', '..', '../escape.xlsx', 'folder/file.xlsx', 'folder\\file.xlsx', 'a\u0000b', ' trailing ', '文'.repeat(100)]) assert.throws(() => uploadFilename(encodeURIComponent(name)), /UPLOAD_INVALID_FILENAME/);
  assert.throws(() => uploadFilename('%ZZ'), /UPLOAD_INVALID_FILENAME/);
  await assert.rejects(stage(store, 'x', 'test.csv', { mimeType: 'text/plain\r\nheader' }), /UPLOAD_INVALID_MIME/);
  await assert.rejects(stage(store, '', 'empty.csv'), /UPLOAD_EMPTY/);
  await assert.rejects(stage(store, '', 'empty.csv', { contentLength: '0' }), /UPLOAD_EMPTY/);
  assert.equal(store.items.size, 1);
  await store.delete(file.id); assert.equal(store.total, 0);
});

test('single-file, session, shared-worker and item quotas include concurrent receiving reservations', async t => {
  const budget = new UploadBudget(12), store = fixture(t, { maxBytes: 8, maxTotal: 10, maxItems: 2, budget });
  assert.equal(MAX_UPLOAD_BYTES, 256 * 1024 * 1024);
  await assert.rejects(stage(store, '123456789'), /UPLOAD_TOO_LARGE/); assert.equal(store.total, 0); assert.equal(budget.used, 0);
  await assert.rejects(stage(store, 'short', 'bad.csv', { contentLength: '6' }), /UPLOAD_INVALID_LENGTH/); assert.equal(store.total, 0);
  const receiving = new PassThrough(), pending = store.receive(receiving, { filename: 'first.csv', contentLength: '8' });
  assert.equal(store.total, 8);
  await assert.rejects(stage(store, '123', 'second.csv', { contentLength: '3' }), /UPLOAD_TOTAL_LIMIT/);
  const other = fixture(t, { maxBytes: 8, maxTotal: 10, budget });
  await assert.rejects(stage(other, '12345', 'other.csv', { contentLength: '5' }), /UPLOAD_STORAGE_LIMIT/);
  receiving.end('12345678'); const first = await pending;
  const second = await stage(store, '12', 'second.csv');
  await assert.rejects(stage(store, '1', 'third.csv'), /UPLOAD_CAPACITY/);
  assert.equal(store.items.size, 2); assert.equal(budget.used, 10);
  await store.delete(first.id); await store.delete(second.id); assert.equal(budget.used, 0);
});

test('session IDs cannot borrow foreign bytes, in-use files are protected, and selected bytes remain for delayed FileReader', async t => {
  let now = 100;
  const store = fixture(t, { now: () => now }), other = fixture(t);
  const first = await stage(store, 'first'), second = await stage(store, 'second');
  await assert.rejects(other.acquire([first.id]), /UPLOAD_NOT_FOUND/);
  await assert.rejects(store.acquire(['/etc/passwd']), /UPLOAD_INVALID_ID/);
  await assert.rejects(store.acquire([first.id, first.id]), /UPLOAD_INVALID_ID/);
  const held = await store.acquire([first.id]);
  await assert.rejects(store.delete(first.id), /UPLOAD_BUSY/);
  held.retain(); held.release(); held.release();
  await assert.rejects(store.delete(first.id), /UPLOAD_RETAINED/);
  now += 31 * 60_000; await store.sweep();
  assert.equal(store.items.has(first.id), true); assert.equal(store.items.has(second.id), false);
  assert.equal((await readFile(held.paths[0])).toString(), 'first');
  const directory = await store.directory(); await store.close();
  await assert.rejects(access(directory)); assert.equal(store.total, 0);
});

test('interrupted upload and session close release reservations and remove incomplete files', async t => {
  const store = fixture(t), source = new PassThrough();
  const pending = store.receive(source, { filename: 'interrupted.csv', contentLength: '32' });
  source.write('partial'); await store.close();
  await assert.rejects(pending, /UPLOAD_SESSION_CLOSED|UPLOAD_INCOMPLETE/);
  assert.equal(store.items.size, 0); assert.equal(store.total, 0); assert.equal(store.budget.used, 0);
});

test('repeated selections have a larger staging budget than one eight-file selection', async t => {
  const store = fixture(t), ids = [];
  assert.equal(store.capabilities().max_files, 8); assert.equal(store.capabilities().max_staged_files, 128);
  for (let i = 0; i < 12; i++) {
    const file = await stage(store, `row ${i}`, `repeat-${i}.csv`); ids.push(file.id);
    const selected = await store.acquire([file.id]); selected.retain(); selected.release();
  }
  assert.equal(store.items.size, 12);
  await assert.rejects(store.acquire(ids.slice(0, 9)), /UPLOAD_INVALID_ID/);
  const selected = await store.acquire(ids.slice(0, 8)); assert.equal(selected.files.length, 8); selected.release();
});

test('a staged path replaced by a symlink is never read or exposed in an error', async t => {
  const store = fixture(t), file = await stage(store, 'safe');
  const path = store.items.get(file.id).path;
  await unlink(path); await symlink('/etc/passwd', path);
  await assert.rejects(store.acquire([file.id]), failure => failure.code === 'UPLOAD_FILE_UNAVAILABLE' && !failure.message.includes(path));
});

test('upload action accepts only opaque IDs, rejects batches and arbitrary paths before any page mutation', () => {
  const good = { action: 'upload', target: '#file', files: [randomUUID()] };
  validateAction(good);
  for (const patch of [{ files: ['/etc/passwd'] }, { files: [] }, { files: [good.files[0], good.files[0]] }, { upload_mode: 'mouse' }, { target: undefined }, { file_path: '/secret' }]) assert.throws(() => validateAction({ ...good, ...patch }), /upload requires/);
  assert.throws(() => validateAction({ action: 'batch', actions: [{ action: 'click', target: '#do' }, good] }), /upload must run separately/);
  const binding = { context_ref: 'ctx_0123456789abcdef01234567', tab_id: 't1', generation: 1, control_epoch: 0, document_version: 1 };
  validateAction({ ...good, upload_binding: binding });
  for (const patch of [{ tab_id: undefined }, { generation: 0 }, { control_epoch: -1 }, { document_version: undefined }, { context_ref: '' }, { extra: true }]) assert.throws(() => validateAction({ ...good, upload_binding: { ...binding, ...patch } }), /complete internal/);
});

test('navigation during staged-file acquisition or target inspection is fenced before file dispatch', async t => {
  for (const stage of ['acquire', 'inspect']) {
    const session = new BrowserSession({ on() {}, setDefaultTimeout() {}, setDefaultNavigationTimeout() {} });
    t.after(() => session.uploads.close());
    const meta = { id: 't1', version: 1, visual: 1, page: { isClosed: () => false, url: () => 'https://fixture.invalid/' } };
    session.active = meta.id; session.pages.set(meta.id, meta); session.devtools.attach = async () => {};
    let dispatched = 0, released = 0;
    session.uploads.acquire = async () => { if (stage === 'acquire') meta.version++; return { paths: ['/private/staged.csv'], files: [], retain() {}, release() { released++; } }; };
    session.locator = async () => ({ evaluate: async () => { if (stage === 'inspect') meta.version++; return { file: true, multiple: true }; }, setInputFiles: async () => { dispatched++; } });
    const binding = { context_ref: session.contextRef, tab_id: meta.id, generation: 1, control_epoch: 0, document_version: 1 };
    await assert.rejects(session.action({ action: 'upload', target: '#file', files: [randomUUID()], upload_binding: binding }, 1), /UPLOAD_TARGET_CHANGED/);
    assert.equal(dispatched, 0); assert.equal(released, 1);
  }
});

test('real browser input, dynamic chooser, iframe and file drop preserve frontend parsing, deduplication and page control', { timeout: 45000 }, async t => {
  const site = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(`<!doctype html><title>Upload validation</title><h1>Upload fixture</h1>
      <input id="file" type="file" multiple hidden><button id="choose">Choose dynamic files</button><button id="no-chooser">Does not open a chooser</button>
      <div id="drop" style="padding:30px;border:1px solid">Drop files here</div><div id="reject-drop" style="padding:30px">No drop handler</div>
      <pre id="result">Waiting</pre>${req.url === '/' ? '<iframe title="Child upload" src="/child" style="width:500px;height:200px"></iframe>' : ''}
      <script>
      window.changes=0; window.parsed=[];
      async function parse(files){window.changes++;const captured=[...files];await new Promise(r=>setTimeout(r,120));window.parsed=await Promise.all(captured.map(async f=>({name:f.name,text:await f.text()})));document.querySelector('#result').textContent=JSON.stringify(window.parsed);}
      document.querySelector('#file').addEventListener('change',e=>parse(e.target.files));
      document.querySelector('#choose').onclick=()=>{const el=document.createElement('input');el.type='file';el.multiple=true;el.hidden=true;el.addEventListener('change',()=>parse(el.files));document.body.append(el);el.click();};
      document.querySelector('#drop').addEventListener('dragover',e=>e.preventDefault());
      document.querySelector('#drop').addEventListener('drop',e=>{e.preventDefault();parse(e.dataTransfer.files);});
      </script>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => { site.closeAllConnections(); site.close(); });
  const native = process.env.POGO_UPLOAD_TEST_NATIVE === '1';
  const browser = native ? null : await chromium.launch({ headless: true });
  if (browser) t.after(() => browser.close());
  const worker = await createWorker({ token: 'upload-test-only', ...(browser ? { browser } : {}), actionTimeout: 3500, settleMs: 0 });
  const address = await worker.listen(0); const base = `http://127.0.0.1:${address.port}`; t.after(() => worker.close());
  async function request(path, method = 'GET', body, extra = {}) {
    const response = await fetch(base + path, { method, headers: { Authorization: 'Bearer upload-test-only', ...(body && !Buffer.isBuffer(body) ? { 'Content-Type': 'application/json' } : {}), ...extra }, body: body === undefined ? undefined : Buffer.isBuffer(body) ? body : JSON.stringify(body) });
    return { status: response.status, data: await response.json() };
  }
  const execute = action => request('/sessions/upload/execute', 'POST', { op_id: randomUUID(), ...action });
  const good = result => { assert.equal(result.status, 200, JSON.stringify(result.data)); assert.equal(result.data.ok, true, JSON.stringify(result.data)); return result.data; };
  good(await request('/sessions/upload', 'PUT')); good(await request('/sessions/other', 'PUT'));
  good(await execute({ action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` }));
  const context = native ? worker.runtime.hosts.get('upload').context : browser.contexts()[0];
  const main = context.pages().find(page => page.url() === `http://127.0.0.1:${site.address().port}/`), expected = '姓名,数量\n张三,12\n';
  assert.ok(main, 'the real selected browser page must own the fixture');
  async function transfer(name = '前端配置.csv', text = expected) {
    return good(await request('/sessions/upload/uploads', 'POST', Buffer.from(text), { 'Content-Type': 'application/octet-stream', 'X-Upload-Filename': encodeURIComponent(name) })).file;
  }
  const csv = await transfer(), txt = await transfer('备注.txt', '人工输入需要保留');
  const identity = (await request('/sessions/upload/state')).data;
  const binding = { context_ref: identity.context_ref, tab_id: identity.active_tab, generation: identity.generation, control_epoch: identity.control_epoch, document_version: identity.document_version };
  for (const stale of [{ context_ref: 'ctx_000000000000000000000000' }, { tab_id: 't999' }, { generation: binding.generation + 1 }, { control_epoch: binding.control_epoch + 1 }, { document_version: binding.document_version + 1 }]) {
    const rejected = await execute({ action: 'upload', target: '#file', files: [csv.id], upload_binding: { ...binding, ...stale } });
    assert.equal(rejected.data.ok, false); assert.match(rejected.data.error, /UPLOAD_TARGET_CHANGED/);
    assert.equal(await main.evaluate(() => changes), 0, 'a stale final-dispatch binding must never select a file');
  }
  const action = { op_id: 'upload-once', action: 'upload', target: '#file', files: [csv.id, txt.id] };
  const uploaded = good(await request('/sessions/upload/execute', 'POST', action));
  assert.equal(uploaded.data.upload.status, 'selected'); assert.equal(uploaded.data.upload.site_parsing_verified, false);
  await main.waitForFunction(() => parsed.length === 2);
  assert.deepEqual(await main.evaluate(() => parsed), [{ name: csv.name, text: expected }, { name: txt.name, text: '人工输入需要保留' }]);
  assert.equal((await request('/sessions/upload/uploads/' + csv.id, 'DELETE')).status, 409, 'selected File must still support delayed frontend reads');
  good(await request('/sessions/upload/execute', 'POST', action)); assert.equal(await main.evaluate(() => changes), 1, 'same operation must not trigger a second file change');
  assert.equal((await request('/sessions/other/execute', 'POST', { ...action, op_id: randomUUID() })).data.ok, false, 'foreign session ID cannot select staged bytes');
  const unused = await transfer('unused.txt', 'unselected');
  const wrongTarget = await execute({ action: 'upload', target: '#choose', files: [unused.id] });
  assert.equal(wrongTarget.data.ok, false); assert.match(wrongTarget.data.error, /input\[type=file\]/);
  good(await request('/sessions/upload/uploads/' + unused.id, 'DELETE'));
  good(await execute({ action: 'upload', upload_mode: 'chooser', target: '#choose', files: [csv.id] }));
  await main.waitForFunction(() => changes === 2 && parsed.length === 1); assert.equal(await main.evaluate(() => parsed[0].text), expected);
  good(await execute({ action: 'upload', upload_mode: 'drop', target: '#drop', files: [txt.id] }));
  await main.waitForFunction(() => changes === 3 && parsed[0]?.name === '备注.txt'); assert.equal(await main.evaluate(() => parsed[0].text), '人工输入需要保留');
  const rejected = await execute({ action: 'upload', upload_mode: 'drop', target: '#reject-drop', files: [csv.id] });
  assert.equal(rejected.data.ok, false); assert.match(rejected.data.error, /reject|dragover|preventDefault/i); assert.equal(await main.evaluate(() => changes), 3);
  const child = main.frames().find(frame => frame !== main.mainFrame());
  const childFile = await child.locator('#file').evaluateHandle(el => el);
  // The normal snapshot reference layer supports uploads into child frames;
  // a CSS selector remains intentionally scoped to the selected main page.
  await childFile.evaluate(el => { el.hidden = false; el.setAttribute('aria-label', 'Child file upload'); }); await childFile.dispose();
  const snapshot = good(await execute({ action: 'snapshot' }));
  const line = snapshot.text.split('\n').find(line => /Child file upload/.test(line));
  assert.ok(line, snapshot.text); const target = /^\[(e\d+)\]/.exec(line)?.[1]; assert.ok(target, line);
  good(await execute({ action: 'upload', target, files: [csv.id] }));
  await child.waitForFunction(() => parsed.length === 1); assert.equal(await child.evaluate(() => parsed[0].text), expected);
  const waiting = execute({ action: 'upload', upload_mode: 'chooser', target: '#no-chooser', files: [csv.id] });
  await main.waitForFunction(() => true);
  const until = Date.now() + 2000;
  while (!main.listenerCount('filechooser') && Date.now() < until) await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(main.listenerCount('filechooser'), 1);
  good(await request('/sessions/upload/cancel', 'POST'));
  assert.equal((await waiting).data.ok, false);
  assert.equal(main.listenerCount('filechooser'), 0, 'cancelled chooser interception must be removed');
  // Cancellation replaced the agent page. Use a fresh page/observation before
  // demonstrating the page-scoped human ownership guard.
  good(await execute({ action: 'navigate', url: `http://127.0.0.1:${site.address().port}/` }));
  const current = context.pages().find(page => page.url() === `http://127.0.0.1:${site.address().port}/`);
  const stateReply = await request('/sessions/upload/state'); assert.equal(stateReply.status, 200); const state = stateReply.data;
  good(await request('/sessions/upload/control', 'POST', { mode: 'human', tab_id: state.active_tab, context_ref: state.context_ref }));
  const denied = await execute({ action: 'upload', target: '#file', files: [csv.id] });
  assert.equal(denied.status, 409); assert.match(denied.data.error, /human|claim/);
  assert.equal(await current.evaluate(() => changes), 0, 'Agent cannot upload into a human-owned page');
  const invalid = await request('/sessions/upload/uploads', 'POST', Buffer.from('x'), { 'Content-Type': 'application/octet-stream', 'X-Upload-Filename': encodeURIComponent('../escape') });
  assert.equal(invalid.status, 400); assert.equal(invalid.data.code, 'UPLOAD_INVALID_FILENAME');
  const wrongType = await request('/sessions/upload/uploads', 'POST', Buffer.from('x'), { 'Content-Type': 'text/plain', 'X-Upload-Filename': 'test.txt' }); assert.equal(wrongType.status, 415);
  good(await request('/sessions/upload', 'DELETE')); good(await request('/sessions/other', 'DELETE'));
});
