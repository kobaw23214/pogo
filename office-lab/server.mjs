import http from 'node:http';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import fsNative from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const labRoot = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(labRoot, '../..');
const operationNames = new Set(['capabilities', 'word.read', 'word.replace', 'sheet.read', 'sheet.apply', 'history.undo', 'history.redo']);
const writes = new Set(['word.replace', 'sheet.apply', 'history.undo', 'history.redo']);
const mime = { '.html': 'text/html; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json; charset=utf-8' };
const idPattern = /^[A-Za-z0-9_-]{8,100}$/;
const random = () => crypto.randomBytes(32).toString('hex');
const digest = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');

function fail(code, message) { const error = new Error(message); error.status = code; throw error; }
function equal(a, b) {
  if (typeof a !== 'string' || typeof b !== 'string') return false;
  const left = Buffer.from(a), right = Buffer.from(b);
  return left.length === right.length && crypto.timingSafeEqual(left, right);
}
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map((k) => `${JSON.stringify(k)}:${canonical(value[k])}`).join(',')}}`;
  return JSON.stringify(value);
}
function signJWT(payload, secret) {
  const header = Buffer.from(JSON.stringify({ alg: 'HS256', typ: 'JWT' })).toString('base64url');
  const data = Buffer.from(JSON.stringify(payload)).toString('base64url');
  const input = `${header}.${data}`;
  return `${input}.${crypto.createHmac('sha256', secret).update(input).digest('base64url')}`;
}
function verifyJWT(token, secret) {
  if (typeof token !== 'string') fail(403, '缺少编辑服务签名。');
  const parts = token.split('.');
  if (parts.length !== 3 || parts[0].length > 200 || parts[1].length > 40000) fail(403, '编辑服务签名格式不正确。');
  let header, payload;
  try { header = JSON.parse(Buffer.from(parts[0], 'base64url')); payload = JSON.parse(Buffer.from(parts[1], 'base64url')); }
  catch { fail(403, '编辑服务签名无法读取。'); }
  if (header.alg !== 'HS256') fail(403, '编辑服务签名算法不匹配。');
  const expected = crypto.createHmac('sha256', secret).update(`${parts[0]}.${parts[1]}`).digest('base64url');
  if (!equal(expected, parts[2]) || payload.exp && payload.exp < Date.now() / 1000) fail(403, '编辑服务签名已失效。');
  return payload;
}
async function readJSON(request) {
  const chunks = []; let size = 0;
  for await (const chunk of request) { size += chunk.length; if (size > 1_000_000) fail(413, '请求超过原型的大小限制。'); chunks.push(chunk); }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}'); } catch { fail(400, '请求必须是有效 JSON。'); }
}
function json(response, code, body) {
  if (response.destroyed || response.writableEnded) return;
  response.writeHead(code, { 'Content-Type': mime['.json'], 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
  response.end(JSON.stringify(body));
}
async function atomic(file, bytes, mode = 0o600) {
  const temp = `${file}.${crypto.randomUUID()}.tmp`;
  await fs.writeFile(temp, bytes, { mode });
  await fs.rename(temp, file);
}

export async function createLab(options = {}) {
  const dataRoot = path.resolve(options.dataRoot || path.join(repositoryRoot, '.localagent/checks/onlyoffice-prototype-20261012'));
  const port = Number(options.port ?? 19895);
  const publicURL = options.publicURL || `http://127.0.0.1:${port}`;
  const engineURL = options.engineURL || 'http://127.0.0.1:19894';
  const fetchURL = options.fetchURL || `http://host.docker.internal:${port}`;
  const engineOrigin = new URL(engineURL).origin;
  const privateRoot = path.join(dataRoot, 'private');
  const storageRoot = path.join(dataRoot, 'saved');
  await fs.mkdir(privateRoot, { recursive: true, mode: 0o700 });
  await fs.mkdir(storageRoot, { recursive: true });
  const fixture = JSON.parse(await fs.readFile(path.join(dataRoot, 'fixtures/manifest.json'), 'utf8'));
  if (fixture.synthetic !== true) fail(400, '隔离原型只接收人工测试文档。');
  const secret = options.secret || (await fs.readFile(path.join(privateRoot, 'docserver-jwt'), 'utf8')).trim();
  if (secret.length < 32) fail(400, '请先配置隔离编辑服务的签名密钥。');
  const adminToken = random();
  const documents = new Map();
  const sessions = new Map();
  for (const [id, meta] of Object.entries(fixture.files)) {
    if (!['word', 'excel'].includes(id) || path.basename(meta.name) !== meta.name) fail(400, '测试文件清单不正确。');
    const file = path.join(storageRoot, meta.name);
    try { await fs.access(file); } catch { await fs.copyFile(path.join(dataRoot, 'fixtures', meta.name), file); }
    let previous = {};
    try { previous = JSON.parse(await fs.readFile(`${file}.json`, 'utf8')); } catch { /* New synthetic document. */ }
    documents.set(id, { id, file, name: meta.name, editor: id === 'word' ? 'word' : 'cell', fileType: id === 'word' ? 'docx' : 'xlsx', revision: previous.revision || 0, savedAt: previous.savedAt || null, saveState: previous.savedAt ? 'saved' : 'idle', active: null, generation: 0 });
  }
  const status = (doc) => ({ documentId: doc.id, revision: doc.revision, savedAt: doc.savedAt, saveState: doc.saveState, error: doc.error || null, activeSessionId: doc.active });
  const isActive = (session) => !session.closed && documents.get(session.documentId).active === session.id;
  function authenticate(request, body, session, url) {
    const header = request.headers.authorization;
    if (equal(header, `Bearer ${adminToken}`)) return;
    const token = body?.token || url.searchParams.get('token');
    if (!equal(token, session.token)) fail(403, '此请求不属于当前文档会话。');
  }
  function currentSession(id) {
    const session = sessions.get(id);
    if (!session || !isActive(session)) fail(409, '编辑会话已关闭或替换，请读取新的会话。');
    return session;
  }
  async function record(session, operationId, state, extra = {}) {
    await fs.appendFile(path.join(privateRoot, 'operations.jsonl'), `${JSON.stringify({ sessionId: session.id, documentId: session.documentId, operationId, state, time: new Date().toISOString(), ...extra })}\n`, { mode: 0o600 });
  }
  async function openDocument(doc) {
    if (doc.active) {
      const old = sessions.get(doc.active);
      if (old && !old.closed) fail(409, '请先保存并关闭当前编辑会话，再重新打开。');
    }
    const id = crypto.randomUUID();
    const session = { id, documentId: doc.id, token: random(), fileToken: random(), key: `${doc.id}-${id}`, generation: ++doc.generation, operations: new Map(), queue: [], ready: false, blocked: false, closed: false, dirtyEpoch: 0, persistedEpoch: 0, saveWaiters: new Set() };
    sessions.set(id, session); doc.active = id;
    const config = {
      documentType: doc.id === 'word' ? 'word' : 'cell', type: 'desktop',
      document: { key: session.key, fileType: doc.fileType, title: `Pogo 隔离测试 ${doc.name}`, url: `${fetchURL}/files/${id}?token=${session.fileToken}`, permissions: { edit: true, download: true, print: false, review: true } },
      editorConfig: { mode: 'edit', lang: 'zh-CN', user: { id: 'pogo-office-lab-human', name: 'Pogo 隔离验证用户' }, callbackUrl: `${fetchURL}/callbacks/${id}?token=${session.fileToken}`, customization: { autosave: true, forcesave: false, plugins: true }, plugins: { pluginsData: [`${publicURL}/plugin/session/${id}/${session.token}/config.json`], autostart: ['asc.{A6691D12-9A8F-4A33-91D4-548D3BB68D01}'] } },
    };
    config.token = signJWT(config, secret);
    return { session: { id, documentId: doc.id, editor: doc.editor, token: session.token }, config };
  }
  async function runCommand(session, body) {
    if (!session.ready) fail(409, '当前文档的真实插件尚未连接。');
    if (session.saving && writes.has(body.operation)) fail(409, '当前文件正在导出保存，请稍后修改。');
    if (!idPattern.test(body.operationId || '') || !operationNames.has(body.operation) || !body.args || typeof body.args !== 'object' || Array.isArray(body.args)) fail(400, '工具操作或参数不在隔离原型允许范围内。');
    const stamp = canonical({ operation: body.operation, args: body.args });
    let operation = session.operations.get(body.operationId);
    if (operation && operation.stamp !== stamp) fail(409, '相同操作 ID 不能用于不同请求。');
    if (operation) return operation.result || operation.promise;
    if (session.blocked) fail(409, '上一步结果未知，先核对并重建编辑会话。');
    if (!operation) {
      if (session.operations.size >= 512) fail(409, '本测试会话操作数已达上限，请保存后重开。');
      operation = { operationId: body.operationId, operation: body.operation, args: body.args, stamp, dispatched: false, done: false };
      operation.promise = new Promise((resolve) => { operation.resolve = resolve; });
      operation.timer = setTimeout(() => {
        if (operation.done) return;
        operation.done = true; session.blocked = true;
        operation.result = { ok: false, error: { code: 'OUTCOME_UNKNOWN', message: '编辑器没有及时返回结果；不会自动重复执行，请保留画布并核对。' } };
        void record(session, body.operationId, 'unknown'); operation.resolve(operation.result);
      }, options.commandTimeout ?? 35000);
      session.operations.set(body.operationId, operation);
      await record(session, body.operationId, 'queued', { operation: body.operation });
      session.queue.push(operation);
    }
    return operation.result || operation.promise;
  }
  function downloadURL(value) {
    const url = new URL(value);
    if (url.username || url.password || !['http:', 'https:'].includes(url.protocol)) fail(403, '保存文件地址不属于本次编辑服务。');
    if (url.origin === engineOrigin) return url;
    const internal = options.internalEngineOrigin || 'http://localhost';
    if (url.origin !== internal) fail(403, '保存文件地址不属于本次编辑服务。');
    return new URL(`${url.pathname}${url.search}`, engineURL);
  }
  async function callback(request, response, url, session) {
    if (!equal(url.searchParams.get('token'), session.fileToken)) fail(403, '保存回调不属于本次文档。');
    const body = await readJSON(request);
    const claims = verifyJWT(body.token || request.headers.authorization?.replace(/^Bearer /, ''), secret);
    const signed = claims.payload || claims;
    for (const field of ['key', 'status', 'url', 'userdata']) if (canonical(signed[field]) !== canonical(body[field])) fail(403, '保存回调与签名不匹配。');
    if (body.key !== session.key) fail(403, '保存回调的编辑器身份不匹配。');
    // The prototype has one canonical save path: editor downloadAs -> host
    // export receipt. A delayed automatic callback must not overwrite it.
    await record(session, 'callback', 'acknowledged-only', { callbackStatus: body.status, active: isActive(session) });
    json(response, 200, { error: 0 });
  }
  async function saveCurrentEditor(session) {
    if (!session.ready || session.blocked) fail(409, '当前编辑会话不能确认保存，请核对连接。');
    if (session.saving || session.queue.some((operation) => !operation.done)) fail(409, '当前文档还有未完成操作。');
    session.saving = true;
    const doc = documents.get(session.documentId); doc.saveState = 'saving'; doc.error = null;
    const operationId = crypto.randomUUID();
    const operation = { operationId, operation: 'host.export', args: { fileType: doc.fileType }, dispatched: false, done: false, epoch: session.dirtyEpoch };
    operation.promise = new Promise((resolve) => { operation.resolve = resolve; });
    operation.timer = setTimeout(() => {
      if (operation.done) return;
      operation.done = true; session.blocked = true;
      operation.abort?.abort();
      operation.result = { ok: false, error: { code: 'EXPORT_OUTCOME_UNKNOWN', message: '编辑器尚未返回可确认的当前文件；不会自动重复导出。' } };
      operation.resolve(operation.result);
      void record(session, operationId, 'export-unknown');
    }, options.saveTimeout ?? 35000);
    session.operations.set(operationId, operation); session.exportIntent = operation;
    await record(session, operationId, 'queued', { operation: 'host.export' });
    session.queue.push(operation);
    try {
      const result = await operation.promise;
      if (!result.ok) fail(502, result.error.message);
      return result.status;
    } catch (error) { doc.saveState = 'error'; doc.error = error.message; throw error; }
    finally { clearTimeout(operation.timer); session.saving = false; }
  }
  async function exportReceipt(session, body, response) {
    const operation = session.exportIntent;
    const doc = documents.get(session.documentId);
    if (!operation || !session.saving || operation.operationId !== body.operationId || !operation.dispatched || operation.done || session.exportBusy) fail(409, '导出回执不对应当前在途保存。');
    if (body.fileType !== doc.fileType) fail(400, '导出格式与当前文档不一致。');
    const source = downloadURL(body.url);
    session.exportBusy = true;
    operation.abort = new AbortController();
    const temp = `${doc.file}.${operation.operationId}.export.tmp`;
    try {
      const result = await fetch(source, { redirect: 'error', signal: AbortSignal.any([operation.abort.signal, AbortSignal.timeout(20000)]) });
      if (!result.ok) fail(502, '无法获取编辑器导出的当前文件。');
      const chunks = []; let size = 0;
      for await (const chunk of result.body) { size += chunk.length; if (size > 20_000_000) fail(413, '导出文件超过隔离原型限制。'); chunks.push(chunk); }
      const bytes = Buffer.concat(chunks);
      if (bytes.length < 4 || bytes[0] !== 0x50 || bytes[1] !== 0x4b) fail(502, '编辑器未返回有效 Office 文件。');
      await fs.writeFile(temp, bytes, { mode: 0o600 });
      if (operation.done || !isActive(session) || session.exportIntent !== operation) fail(409, '导出已经过期，文件不会写回。');
      // The final fence and atomic rename are one short, synchronous commit.
      // The timeout cannot interleave between them and persist an expired file.
      fsNative.renameSync(temp, doc.file);
      clearTimeout(operation.timer);
      doc.revision += 1; doc.savedAt = new Date().toISOString(); doc.error = null;
      session.persistedEpoch = Math.max(session.persistedEpoch, operation.epoch);
      doc.saveState = session.dirtyEpoch > session.persistedEpoch ? 'dirty' : 'saved';
      await atomic(`${doc.file}.json`, JSON.stringify({ revision: doc.revision, savedAt: doc.savedAt, sha256: digest(bytes), source: 'editor-downloadAs' }, null, 2));
      await record(session, operation.operationId, 'persisted', { revision: doc.revision, sha256: digest(bytes), bytes: bytes.length, source: 'editor-downloadAs' });
      operation.done = true; operation.result = { ok: true, status: status(doc) };
      operation.resolve(operation.result);
      json(response, 200, operation.result);
    } catch (error) {
      if (!operation.done) {
        operation.done = true; clearTimeout(operation.timer);
        operation.result = { ok: false, error: { code: 'EXPORT_FAILED', message: error.status ? error.message : '当前文件未能写回，编辑器内容仍保留。' } };
        operation.resolve(operation.result);
      }
      throw error;
    } finally { session.exportBusy = false; await fs.rm(temp, { force: true }); }
  }
  async function staticFile(response, file) {
    const bytes = await fs.readFile(file);
    response.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer' }); response.end(bytes);
  }
  const server = http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, publicURL);
      const origin = request.headers.origin;
      if (request.method === 'OPTIONS') {
        if (origin !== engineOrigin && origin !== publicURL) fail(403, '来源不在本次测试范围内。');
        response.writeHead(204, { 'Access-Control-Allow-Origin': origin, 'Access-Control-Allow-Methods': 'GET,POST', 'Access-Control-Allow-Headers': 'Content-Type,Authorization' }); response.end(); return;
      }
      if (origin === engineOrigin && url.pathname.startsWith('/plugin/')) response.setHeader('Access-Control-Allow-Origin', engineOrigin);
      if (request.method === 'POST' && !url.pathname.startsWith('/callbacks/') && origin && origin !== publicURL) fail(403, '工具请求必须来自本次原型页面。');
      if (request.method === 'GET' && url.pathname === '/api/bootstrap') {
        json(response, 200, { engineUrl: engineURL, fixture: { word: fixture.word, excel: fixture.excel }, documents: [...documents.values()].map((doc) => ({ id: doc.id, title: doc.id === 'word' ? 'Word 中文文档' : 'Excel 销售工作簿', fileType: doc.fileType, ...status(doc) })) }); return;
      }
      let match = url.pathname.match(/^\/api\/documents\/(word|excel)\/(open|status|export)$/);
      if (match) {
        const doc = documents.get(match[1]);
        if (match[2] === 'open' && request.method === 'POST') { await readJSON(request); json(response, 200, await openDocument(doc)); return; }
        if (match[2] === 'status' && request.method === 'GET') { json(response, 200, status(doc)); return; }
        if (match[2] === 'export' && request.method === 'GET') { const bytes = await fs.readFile(doc.file); response.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Disposition': `attachment; filename="${doc.name}"`, 'Cache-Control': 'no-store' }); response.end(bytes); return; }
      }
      match = url.pathname.match(/^\/api\/sessions\/([A-Za-z0-9_-]+)\/(next|ready|results|commands|state|save|export|close|describe)$/);
      if (match) {
        const session = currentSession(match[1]); const action = match[2];
        const body = request.method === 'POST' ? await readJSON(request) : {};
        authenticate(request, body, session, url);
        if (action === 'ready' && request.method === 'POST') { session.ready = true; session.capabilities = body.capabilities; json(response, 200, { ok: true }); return; }
        if (action === 'describe' && request.method === 'GET') { json(response, 200, { id: session.id, documentId: session.documentId, ready: session.ready, blocked: session.blocked, capabilities: session.capabilities || null }); return; }
        if (action === 'next' && request.method === 'GET') {
          const operation = session.blocked || session.queue.some((item) => item.dispatched && !item.done) ? null : session.queue.find((item) => !item.dispatched && !item.done);
          if (operation) { operation.dispatched = true; await record(session, operation.operationId, 'dispatched'); }
          json(response, 200, { command: operation ? { operationId: operation.operationId, operation: operation.operation, args: operation.args } : null }); return;
        }
        if (action === 'results' && request.method === 'POST') {
          const operation = session.operations.get(body.operationId);
          if (!operation || !operation.dispatched || operation.operation === 'host.export') fail(409, '回执不对应已派发的插件操作。');
          if (operation.done) { json(response, 200, { ok: true, ignored: true }); return; }
          if (typeof body.ok !== 'boolean') fail(400, '回执缺少明确结果。');
          operation.done = true; clearTimeout(operation.timer);
          operation.result = body.ok ? { ok: true, result: body.result, operationId: body.operationId } : { ok: false, error: body.error, operationId: body.operationId };
          if (body.ok && writes.has(operation.operation)) { session.dirtyEpoch += 1; documents.get(session.documentId).saveState = 'dirty'; }
          if (!body.ok && ['OUTCOME_UNKNOWN', 'EDITOR_OUTCOME_UNKNOWN'].includes(body.error?.code)) session.blocked = true;
          await record(session, body.operationId, body.ok ? 'executed' : 'failed', { code: body.error?.code });
          operation.resolve(operation.result); json(response, 200, { ok: true }); return;
        }
        if (action === 'commands' && request.method === 'POST') { const result = await runCommand(session, body); json(response, result.ok ? 200 : 409, result); return; }
        if (action === 'state' && request.method === 'POST') { if (body.dirty === true) { session.dirtyEpoch += 1; documents.get(session.documentId).saveState = 'dirty'; } json(response, 200, { ok: true }); return; }
        if (action === 'save' && request.method === 'POST') { json(response, 200, { ok: true, status: await saveCurrentEditor(session) }); return; }
        if (action === 'export' && request.method === 'POST') { await exportReceipt(session, body, response); return; }
        if (action === 'close' && request.method === 'POST') {
          if (session.saving || session.exportBusy || session.callbackBusy || session.queue.some((item) => !item.done) || session.dirtyEpoch > session.persistedEpoch) fail(409, '当前会话有未保存修改或未完成操作，请先保存。');
          session.closed = true; documents.get(session.documentId).active = null; json(response, 200, { ok: true }); return;
        }
      }
      match = url.pathname.match(/^\/(files|callbacks)\/([A-Za-z0-9_-]+)$/);
      if (match) {
        const session = sessions.get(match[2]); if (!session) fail(404, '文档会话不存在。');
        if (match[1] === 'callbacks' && request.method === 'POST') { await callback(request, response, url, session); return; }
        if (match[1] === 'files' && request.method === 'GET') {
          if (!isActive(session) || !equal(url.searchParams.get('token'), session.fileToken)) fail(403, '测试文件访问已经失效。');
          await staticFile(response, documents.get(session.documentId).file); return;
        }
      }
      if (request.method === 'GET' && url.pathname === '/plugin/config.json') {
        const config = JSON.parse(await fs.readFile(path.join(labRoot, 'plugin/config.json'), 'utf8'));
        // Official plugin SDK loads './config.json' without the variation URL's
        // query. This public manifest contains no session capability or data.
        if (!url.searchParams.has('session') && !url.searchParams.has('token')) { json(response, 200, config); return; }
        const session = currentSession(url.searchParams.get('session')); authenticate(request, {}, session, url);
        config.baseUrl = `${publicURL}/plugin/`;
        const identity = new URLSearchParams({ sessionId: session.id, documentId: session.documentId, token: session.token, editor: documents.get(session.documentId).editor });
        // Docs appends language/theme parameters to plugin URLs. Query fields
        // preserve the editor identity; a fragment would absorb '?lang=...'.
        config.variations[0].url = `index.html?${identity}`;
        json(response, 200, config); return;
      }
      match = url.pathname.match(/^\/plugin\/session\/([A-Za-z0-9_-]+)\/([a-f0-9]{64})\/(config\.json|scope\.js|sdk\.js|plugin\.js|index\.html)$/);
      if (request.method === 'GET' && match) {
        const session = currentSession(match[1]);
        if (!equal(match[2], session.token)) fail(403, '插件资源不属于当前会话。');
        if (match[3] === 'config.json') {
          const config = JSON.parse(await fs.readFile(path.join(labRoot, 'plugin/config.json'), 'utf8'));
          config.baseUrl = `${publicURL}/plugin/session/${session.id}/${session.token}/`;
          config.variations[0].url = 'index.html';
          json(response, 200, config); return;
        }
        if (match[3] === 'scope.js') {
          response.writeHead(200, { 'Content-Type': mime['.js'], 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' });
          response.end(`window.__pogoOfficeLabScope=${JSON.stringify({ sessionId: session.id, documentId: session.documentId, token: session.token, editor: documents.get(session.documentId).editor })};`); return;
        }
        await staticFile(response, match[3] === 'sdk.js' ? path.join(dataRoot, 'vendor/plugins.js') : path.join(labRoot, 'plugin', match[3])); return;
      }
      if (request.method === 'GET' && url.pathname === '/plugin/sdk.js') { await staticFile(response, path.join(dataRoot, 'vendor/plugins.js')); return; }
      const staticRoutes = { '/': 'public/index.html', '/app.mjs': 'public/app.mjs', '/style.css': 'public/style.css', '/plugin/index.html': 'plugin/index.html', '/plugin/plugin.js': 'plugin/plugin.js' };
      if (request.method === 'GET' && Object.hasOwn(staticRoutes, url.pathname)) { await staticFile(response, path.join(labRoot, staticRoutes[url.pathname])); return; }
      fail(404, '此原型没有该入口。');
    } catch (error) { json(response, error.status || 500, { ok: false, error: error.status ? error.message : '原型服务操作失败，请检查本地服务状态。' }); }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, options.bind || '127.0.0.1', resolve); });
  await atomic(path.join(privateRoot, 'runtime.json'), JSON.stringify({ url: publicURL, port, engineURL, dataRoot, adminToken, pid: process.pid }, null, 2));
  return { server, url: publicURL, dataRoot, adminToken, documents, sessions, close: async () => {
    for (const session of sessions.values()) for (const operation of session.operations.values()) { clearTimeout(operation.timer); if (!operation.done) operation.resolve({ ok: false, error: { code: 'SERVICE_STOPPED', message: '隔离服务已停止。' } }); }
    server.closeAllConnections(); await new Promise((resolve) => server.close(resolve));
  } };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const lab = await createLab({ port: process.env.OFFICE_LAB_PORT || 19895, dataRoot: process.env.OFFICE_LAB_DATA, engineURL: process.env.OFFICE_LAB_ENGINE_URL, fetchURL: process.env.OFFICE_LAB_FETCH_URL, publicURL: process.env.OFFICE_LAB_PUBLIC_URL, bind: process.env.OFFICE_LAB_BIND });
    console.log(JSON.stringify({ status: 'listening', url: lab.url, syntheticDocumentsOnly: true }));
    for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => { void lab.close().then(() => process.exit(0)); });
  } catch (error) { console.error(error.status ? error.message : '隔离服务未能启动；请确认测试文档、密钥文件和端口。'); process.exitCode = 1; }
}
