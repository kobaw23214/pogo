import { createServer } from 'node:http';
import { isIP } from 'node:net';

// This module is an evaluator controller, never an HTTP oracle. Every served
// URL is an ordinary page, worker script, login, or business request.
export const TASKS = Object.freeze([
  { id: 'network-diagnosis', title: '失败请求诊断', category: 'diagnosis' },
  { id: 'iframe-data', title: '嵌入库存数据核验', category: 'read' },
  { id: 'worker-data', title: '后台监测数据核验', category: 'read' },
  { id: 'debugger-recovery', title: '暂停结算恢复', category: 'debugger' },
  { id: 'authenticated-write', title: '复用登录保存工单', category: 'write' },
].map(Object.freeze));

const clone = value => structuredClone(value);
const quote = value => JSON.stringify(value).replace(/</g, '\\u003c');
const escape = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
function hash(value) {
  let n = 2166136261;
  for (const c of String(value)) n = Math.imul(n ^ c.charCodeAt(0), 16777619) >>> 0;
  return n;
}
function document(title, body, script = '', variant = 0) {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${escape(title)}</title><style>
  *{box-sizing:border-box}body{font:16px system-ui,sans-serif;color:#183740;background:#f0f5f3;margin:0}header{background:#1e454e;color:white;padding:22px 7vw}main{max-width:${variant % 2 ? 850 : 1050}px;margin:28px auto;padding:24px;background:white;border-radius:14px}h1{font-size:27px}p{line-height:1.7}label{display:block;margin:18px 0}input,select,button{font:inherit;padding:10px;border:1px solid #9cbbb3;border-radius:6px}input,select{display:block;margin-top:8px;min-width:280px}button{background:#13756c;color:white;cursor:pointer}table{border-collapse:collapse;width:100%;margin:20px 0}td,th{text-align:left;border-bottom:1px solid #dce6e2;padding:12px}iframe{border:1px solid #b8cbc5;width:100%;height:420px}output{display:block;background:#eef6f2;margin:20px 0;padding:14px}.layout{display:flex;flex-direction:${variant % 2 ? 'row-reverse' : 'row'};gap:22px}.layout aside{min-width:130px;color:#56756d}.layout section{flex:1}.muted{color:#617d75;font-size:14px}
  </style></head><body><header>${escape(['青屿业务台', '云桥运营台', '川行工作台'][variant % 3])}</header><main><h1>${escape(title)}</h1><div class="layout"><aside>${escape(['当前工作', '业务概览', '进行中的事项'][variant % 3])}</aside><section>${body}</section></div></main>${script ? `<script>${script}</script>` : ''}</body></html>`;
}

function build(meta, seed) {
  const n = hash(`${seed}:${meta.id}`);
  const nonce = n.toString(16).padStart(8, '0');
  const base = `/cases/${meta.id}/${nonce}`;
  const c = { ...meta, nonce, base, variant: n % 6, paths: {}, expected: {}, origin: '', childOrigin: '' };
  for (const name of ['data', 'worker', 'child', 'save', 'records', 'login']) c.paths[name] = `${base}/${name}-${hash(`${nonce}:${name}`).toString(36)}${name === 'worker' ? '.js' : ''}`;
  if (meta.id === 'network-diagnosis') {
    c.expected = { status: 503, code: `QUEUE_BUSY_${200 + n % 700}`, reason: `批处理通道 ${1 + n % 9} 的队列容量已满`, trace_id: `TRACE-${nonce.toUpperCase()}` };
    c.goal = '查明当前页面数据加载失败的 HTTP 状态、业务原因和跟踪编号，结合浏览器中的请求与错误记录提供证据。不要重试或重新加载页面。';
  } else if (meta.id === 'iframe-data') {
    c.expected = { product: ['青屿周转箱', '云桥保温袋', '川行储物盒'][n % 3], available: 21 + n % 137, batch: `LOT-${nonce.toUpperCase()}` };
    c.goal = `核对嵌入库存面板中“${c.expected.product}”的可用数量与批次号，并提供面板实际读取的数据证据。`;
  } else if (meta.id === 'worker-data') {
    c.expected = { device: `监测站 ${11 + n % 80}`, reading: 310 + n % 490, unit: 'ppm', sample_id: `SAMPLE-${nonce.toUpperCase()}` };
    c.goal = `为“${c.expected.device}”启动一次后台采样，核对任务实际获取的读数、单位和采样编号，并提供后台任务的数据证据。页面上的完成提示不足以证明这些数值。`;
  } else if (meta.id === 'debugger-recovery') {
    c.job = { invoice_id: `INV-${nonce.toUpperCase()}`, subtotal_cents: 10000 + n % 45000, discount_cents: 500 + n % 1500 };
    c.expected = { invoice_id: c.job.invoice_id, total_cents: c.job.subtotal_cents - c.job.discount_cents, currency: 'CNY', receipt_id: `SETTLED-${nonce.toUpperCase()}` };
    c.goal = '结算任务已经启动，当前停在后台 worker 的断点。读取暂停位置的实际局部结算记录，报告单据编号、最终金额（分）与币种，再恢复执行并核验恰好保存一次。不要重新发起结算。';
  } else {
    c.expected = { title: `巡检回访 ${100 + n % 800}`, priority: ['normal', 'high'][n % 2], project: `仓库 ${1 + n % 8}`, receipt_id: `TICKET-${nonce.toUpperCase()}` };
    c.credentials = { username: `operator-${nonce}`, password: `fixture-${hash(`${seed}:${nonce}:private-login`).toString(36)}-only` };
    c.cookie = `sid-${hash(`${seed}:${nonce}:private-cookie`).toString(36)}`;
    c.goal = `复用当前已经登录的会话，为“${c.expected.project}”创建标题为“${c.expected.title}”、优先级为“${c.expected.priority === 'high' ? '高' : '普通'}”的工单。只提交一次，核验实际保存结果并报告工单编号。`;
  }
  reset(c);
  return c;
}

function reset(c) {
  c.state = { case_id: `${c.id}-${c.nonce}`, requests: [], submissions: [], submission_attempts: 0, authenticated: false, login_attempts: 0, successful_logins: 0 };
  // Controller-only debugger identity, deliberately outside inspectable state.
  c.preparedPauseAnchor = null;
}
const urlOf = (c, name, child = false) => `${child ? c.childOrigin : c.origin}${c.paths[name]}`;
function dataBody(c) {
  if (c.id === 'network-diagnosis') return { error: { code: c.expected.code, message: c.expected.reason, trace_id: c.expected.trace_id } };
  if (c.id === 'iframe-data') return { inventory: [c.expected, { product: '备用包装材料', available: 7, batch: `OTHER-${c.nonce}` }] };
  if (c.id === 'worker-data') return { sample: c.expected };
  if (c.id === 'debugger-recovery') return c.job;
  return {};
}
function savedRecord(c) {
  if (c.id === 'debugger-recovery') return clone(c.expected);
  return { title: c.expected.title, priority: c.expected.priority, project: c.expected.project, receipt_id: c.expected.receipt_id };
}
function oracle(c) {
  const requirements = { network: [], console: [] };
  const aliases = { status: ['HTTP 状态', '状态'], available: ['可用数量'], reading: ['读数'], total_cents: ['最终金额', '金额（分）'] };
  const answerFields = {
    'network-diagnosis': ['status', 'reason', 'trace_id'],
    'iframe-data': ['available', 'batch'],
    'worker-data': ['reading', 'unit', 'sample_id'],
    'debugger-recovery': ['invoice_id', 'total_cents', 'currency'],
    'authenticated-write': ['receipt_id'],
  };
  const body = value => JSON.stringify(value);
  if (['network-diagnosis', 'iframe-data', 'worker-data'].includes(c.id)) {
    const target_type = c.id === 'iframe-data' ? 'iframe' : c.id === 'worker-data' ? 'worker' : 'page';
    requirements.network.push({ url: urlOf(c, 'data', c.id === 'iframe-data'), method: 'GET', status: c.expected.status ?? 200, body: body(dataBody(c)), target_type, expected_request_count: 1 });
    requirements.target = { type: target_type, url: target_type === 'page' ? `${c.origin}${c.base}/` : urlOf(c, target_type === 'iframe' ? 'child' : 'worker', target_type === 'iframe') };
    if (c.id === 'network-diagnosis') requirements.console.push({ target_type: 'page', includes: [c.expected.code, c.expected.reason, c.expected.trace_id] });
  }
  if (c.id === 'debugger-recovery') {
    requirements.target = { type: 'worker', url: urlOf(c, 'worker') };
    requirements.debugger = { target_type: 'worker', script_url: urlOf(c, 'worker'), locals: { settlement: { invoice_id: c.expected.invoice_id, total_cents: c.expected.total_cents, currency: c.expected.currency } } };
    requirements.submission = { expected_count: 1, fields: savedRecord(c) };
    requirements.network.push({ url: urlOf(c, 'save'), method: 'POST', status: 201, body: body({ saved: savedRecord(c) }), target_type: 'worker' });
  }
  if (c.id === 'authenticated-write') {
    requirements.authenticated = true;
    requirements.submission = { expected_count: 1, fields: savedRecord(c) };
    requirements.network.push({ url: urlOf(c, 'save'), method: 'POST', status: 201, body: body({ saved: savedRecord(c) }), target_type: 'page' });
    requirements.network.push({ url: urlOf(c, 'records'), method: 'GET', status: 200, body: body({ records: [savedRecord(c)] }), target_type: 'page' });
  }
  return {
    task_id: c.id, case_id: c.state.case_id, case_url: `${c.origin}${c.base}/`, expected: clone(c.expected), requirements,
    ...(c.preparedPauseAnchor ? { prepared_pause_anchor: clone(c.preparedPauseAnchor) } : {}),
    // Match only facts explicitly requested in the natural-language goal.
    // Other business fields are verified against persisted server state.
    answer_facts: answerFields[c.id].map(name => ({ name, value: c.expected[name], ...(aliases[name] ? { aliases: aliases[name] } : {}) })),
  };
}

function page(c, authenticated) {
  const title = c.title;
  const header = `<p class="muted">${escape(c.goal)}</p>`;
  if (c.id === 'network-diagnosis') return document(title, `${header}<output role="status">正在读取数据…</output>`, `fetch(${quote(c.paths.data)}).then(async response => { const result = await response.json(); if (!response.ok) { console.error('业务数据加载失败', result.error.code, result.error.message, result.error.trace_id); document.querySelector('output').textContent = '数据加载失败，请检查错误记录。'; } });`, c.variant);
  if (c.id === 'iframe-data') return document(title, `${header}<iframe title="独立库存面板" src="${escape(urlOf(c, 'child', true))}"></iframe>`, '', c.variant);
  if (c.id === 'worker-data') return document(title, `${header}<button type="button" disabled>开始采样</button><output role="status">后台监测器正在准备…</output>`, `const monitor = new Worker(${quote(c.paths.worker)}); monitor.onmessage = event => { if(event.data.kind === 'ready') { document.querySelector('output').textContent = '后台监测器已准备'; document.querySelector('button').disabled = false; } else if(event.data.kind === 'complete') document.querySelector('output').textContent = '后台采样已完成，原始读数保存在后台任务结果中。'; }; document.querySelector('button').onclick = event => { event.target.disabled = true; document.querySelector('output').textContent = '后台采样进行中'; monitor.postMessage({kind:'sample'}); };`, c.variant);
  if (c.id === 'debugger-recovery') return document(title, `${header}<button type="button">启动结算</button><output role="status">后台计算器正在准备…</output>`, `const calculator = new Worker(${quote(c.paths.worker)}); let ready = false; calculator.onmessage = event => { if (event.data.kind === 'ready') { ready = true; document.querySelector('output').textContent = '计算器已准备'; } else if (event.data.kind === 'saved') document.querySelector('output').textContent = '结算已保存，编号 ' + event.data.saved.receipt_id; }; document.querySelector('button').onclick = () => { if (!ready) return; document.querySelector('output').textContent = '结算处理中'; calculator.postMessage({kind:'settle'}); };`, c.variant);
  if (!authenticated) return document('业务账号登录', `<p>请由操作人员登录后继续工作。</p><form action="${escape(c.paths.login)}" method="post"><label>账号<input name="username" autocomplete="off" autofocus required></label><label>密码<input name="password" type="password" autocomplete="off" required></label><button type="submit">登录</button></form>`, '', c.variant);
  return document(title, `${header}<p>当前会话已登录 · ${escape(c.expected.project)}</p><form><label>工单标题<input name="title" required></label><label>优先级<select name="priority">${(c.variant % 2 ? ['high', 'normal'] : ['normal', 'high']).map(value => `<option value="${value}">${value === 'high' ? '高' : '普通'}</option>`).join('')}</select></label><label>所属项目<input name="project" value="${escape(c.expected.project)}" readonly></label><button type="submit">${escape(['保存工单', '创建工单', '提交工单'][c.variant % 3])}</button></form><output role="status">尚未提交新工单</output><section aria-label="保存结果"></section>`, `async function refreshRecords(){const response = await fetch(${quote(c.paths.records)});const data = await response.json();document.querySelector('section[aria-label="保存结果"]').textContent = JSON.stringify(data.records);} document.querySelector('form').onsubmit = async event => {event.preventDefault();const input = Object.fromEntries(new FormData(event.target));const response = await fetch(${quote(c.paths.save)}, {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(input)});const data = await response.json();document.querySelector('output').textContent = response.ok ? '工单已保存，编号 ' + data.saved.receipt_id : data.error;await refreshRecords();}; refreshRecords();`, c.variant);
}

function worker(c) {
  if (c.id === 'worker-data') return `onmessage = event => { if(event.data.kind !== 'sample') return; fetch(${quote(c.paths.data)}).then(response => response.json()).then(data => { globalThis.lastSample = data.sample; console.info('后台采样接收完成', data.sample.sample_id); postMessage({kind:'complete'}); }); }; postMessage({kind:'ready'});`;
  return `let currentJob; fetch(${quote(c.paths.data)}).then(response => response.json()).then(data => {currentJob = data; postMessage({kind:'ready'});});
  onmessage = async function processSettlement(event) { if (event.data.kind !== 'settle' || !currentJob) return; const settlement = {invoice_id:currentJob.invoice_id,total_cents:currentJob.subtotal_cents-currentJob.discount_cents,currency:'CNY'}; debugger; const response = await fetch(${quote(c.paths.save)}, {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(settlement)}); const data = await response.json(); if (response.ok) postMessage({kind:'saved', saved:data.saved}); };`;
}

async function readBody(request) {
  const parts = []; let bytes = 0;
  for await (const chunk of request) { bytes += chunk.length; if (bytes > 16384) throw new Error('request body too large'); parts.push(chunk); }
  const body = Buffer.concat(parts).toString();
  return request.headers['content-type']?.includes('application/json') ? JSON.parse(body || '{}') : Object.fromEntries(new URLSearchParams(body));
}

function replyOf(response) { return response?.ok !== undefined ? response : response?.data; }
function successful(response) {
  const reply = replyOf(response);
  if (!reply?.ok) throw new Error(`Fixture preparation failed: ${reply?.error ?? 'invalid worker reply'}`);
  return reply;
}
async function waitFor(read, accept, label) {
  const end = Date.now() + 6000;
  while (Date.now() < end) { const result = await read(); if (accept(result)) return result; await new Promise(resolve => setTimeout(resolve, 25)); }
  throw new Error(`Fixture preparation timed out: ${label}`);
}

function preparedPauseAnchor(reply, targetRef) {
  const data = reply?.data, provenance = data?.provenance;
  const fail = () => { throw new Error('Fixture preparation missing a valid original paused-frame identity'); };
  if (data?.paused !== true || !Number.isSafeInteger(provenance?.generation) || provenance.generation < 1
    || typeof provenance.tab_id !== 'string' || !provenance.tab_id
    || provenance.target_ref !== targetRef || provenance.target_type !== 'worker') fail();
  const event = data.items?.filter(item => item.method === 'Debugger.paused' && item.target_ref === targetRef
    && item.target_type === provenance.target_type && item.tab_id === provenance.tab_id).at(-1);
  if (!event || !Number.isSafeInteger(event.seq) || event.seq < 1 || !Array.isArray(event.params?.callFrames)) fail();
  const frames = event.params.callFrames.filter(frame => typeof frame.callFrameId === 'string' && frame.callFrameId
    && typeof frame.location?.scriptId === 'string' && frame.location.scriptId).map(frame => ({
      call_frame_id: frame.callFrameId,
      script_id: frame.location.scriptId,
      local_scope_object_ids: [...new Set((frame.scopeChain ?? []).filter(scope => scope.type === 'local'
        && typeof scope.object?.objectId === 'string' && scope.object.objectId).map(scope => scope.object.objectId))],
    }));
  if (!frames.length || !frames.some(frame => frame.local_scope_object_ids.length)) fail();
  // No target URL is substituted for an executing script URL. The controller
  // attests the actual prepared pause and its real frame/scope handles instead.
  return { generation: provenance.generation, tab_id: provenance.tab_id, target_ref: targetRef,
    target_type: provenance.target_type, pause_seq: event.seq, call_frames: frames };
}

function listenerConfiguration(bindHost, bindPort, advertisedOrigins) {
  if (typeof bindHost !== 'string' || !bindHost || bindHost.length > 253
    || (!isIP(bindHost) && !/^(?:[a-z\d](?:[a-z\d-]*[a-z\d])?)(?:\.[a-z\d](?:[a-z\d-]*[a-z\d])?)*$/i.test(bindHost))) {
    throw new Error('Fixture bindHost must be an IP address or hostname');
  }
  if (!Number.isInteger(bindPort) || bindPort < 0 || bindPort > 65535) throw new Error('Fixture bindPort must be an integer from 0 to 65535');
  if (advertisedOrigins === undefined) return undefined;
  if (!Array.isArray(advertisedOrigins) || advertisedOrigins.length !== 2) throw new Error('Fixture advertisedOrigins must contain exactly two HTTP origins');
  const origins = advertisedOrigins.map(value => {
    let url;
    try { if (typeof value !== 'string') throw new Error(); url = new URL(value); } catch { throw new Error('Fixture advertisedOrigins must contain valid HTTP origins'); }
    if (url.protocol !== 'http:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash
      || !url.hostname || ['0.0.0.0', '[::]'].includes(url.hostname) || url.port === '0'
      || (value !== url.origin && value !== `${url.origin}/`)) {
      throw new Error('Fixture advertisedOrigins must be reachable HTTP origins without credentials, paths, queries or fragments');
    }
    return url.origin;
  });
  if (new Set(origins).size !== 2) throw new Error('Fixture advertisedOrigins must be distinct');
  if (origins.some(origin => Number(new URL(origin).port || 80) !== bindPort)) {
    throw new Error('Fixture advertisedOrigins must use the explicit nonzero bindPort');
  }
  return origins;
}

export async function createFixtures({ seed = 1, bindHost = '127.0.0.1', bindPort = 0, advertisedOrigins } = {}) {
  const configuredOrigins = listenerConfiguration(bindHost, bindPort, advertisedOrigins);
  const cases = new Map(TASKS.map(meta => [meta.id, build(meta, seed)]));
  const get = id => { const c = cases.get(id); if (!c) throw new Error(`Unknown browser-use-v2 fixture: ${id}`); return c; };
  let requestOrigins = new Set();
  const server = createServer(async (request, response) => {
    const received_monotonic_ms = performance.now();
    const received_at_ms = Date.now();
    let url;
    try {
      if (!request.headers.host || !request.url.startsWith('/') || request.url.startsWith('//')) throw new Error();
      url = new URL(request.url, `http://${request.headers.host}`);
      if (!requestOrigins.has(url.origin)) throw new Error();
    } catch {
      response.writeHead(421, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
      response.end(JSON.stringify({ error: 'Unknown fixture origin' }));
      return;
    }
    const c = [...cases.values()].find(item => url.pathname === item.base || url.pathname.startsWith(`${item.base}/`));
    const send = (status, value, kind, type = 'application/json', headers = {}) => {
      const body = type === 'application/json' ? JSON.stringify(value) : String(value);
      if (c) c.state.requests.push({ url: url.href, method: request.method, status, response_body: body, kind, received_monotonic_ms, received_at_ms });
      response.writeHead(status, { 'Content-Type': `${type}; charset=utf-8`, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', ...headers });
      response.end(body);
    };
    if (!c) { send(404, { error: 'Not found' }, 'not_found'); return; }
    const authorized = c.state.authenticated && (request.headers.cookie ?? '').split(';').some(value => value.trim() === `fixture_session=${c.cookie}`);
    try {
      if (request.method === 'GET' && [c.base, `${c.base}/`].includes(url.pathname)) { send(200, page(c, authorized), 'page', 'text/html'); return; }
      if (request.method === 'GET' && url.pathname === c.paths.data) { send(c.expected.status ?? 200, dataBody(c), 'data'); return; }
      if (request.method === 'GET' && url.pathname === c.paths.worker && ['worker-data', 'debugger-recovery'].includes(c.id)) { send(200, worker(c), 'worker_script', 'application/javascript'); return; }
      if (request.method === 'GET' && url.pathname === c.paths.child && c.id === 'iframe-data') {
        const script = `fetch(${quote(c.paths.data)}).then(response => response.json()).then(data => { document.querySelector('tbody').innerHTML = data.inventory.map(row => '<tr><td>' + row.product + '</td><td>' + row.available + '</td><td>' + row.batch + '</td></tr>').join(''); });`;
        send(200, document('独立库存面板', '<table><thead><tr><th>物料</th><th>可用数量</th><th>批次号</th></tr></thead><tbody></tbody></table>', script, c.variant + 1), 'iframe_page', 'text/html'); return;
      }
      if (request.method === 'POST' && url.pathname === c.paths.login && c.id === 'authenticated-write') {
        c.state.login_attempts++;
        const input = await readBody(request);
        if (input.username !== c.credentials.username || input.password !== c.credentials.password) { send(401, { error: '账号或密码错误' }, 'login'); return; }
        c.state.authenticated = true; c.state.successful_logins++;
        send(303, '', 'login', 'text/plain', { Location: `${c.base}/`, 'Set-Cookie': `fixture_session=${c.cookie}; HttpOnly; SameSite=Strict; Path=${c.base}/` }); return;
      }
      if (request.method === 'POST' && url.pathname === c.paths.save && ['authenticated-write', 'debugger-recovery'].includes(c.id)) {
        c.state.submission_attempts++;
        if (c.id === 'authenticated-write' && !authorized) { send(401, { error: '请先登录' }, 'save'); return; }
        const input = await readBody(request);
        const fields = c.id === 'authenticated-write' ? ['title', 'priority', 'project'] : ['invoice_id', 'total_cents', 'currency'];
        if (fields.some(key => input[key] !== c.expected[key])) { send(422, { error: '业务字段校验未通过' }, 'save'); return; }
        if (c.state.submissions.length) { send(409, { error: '该业务已保存，请勿重复提交' }, 'save'); return; }
        const record = savedRecord(c); c.state.submissions.push(record);
        send(201, { saved: record }, 'save'); return;
      }
      if (request.method === 'GET' && url.pathname === c.paths.records && c.id === 'authenticated-write') {
        if (!authorized) { send(401, { error: '请先登录' }, 'records'); return; }
        send(200, { records: c.state.submissions }, 'records'); return;
      }
      send(404, { error: 'Not found' }, 'not_found');
    } catch { send(400, { error: 'Invalid request' }, 'invalid'); }
  });
  await new Promise((resolve, reject) => {
    const onError = error => reject(error);
    server.once('error', onError);
    server.listen(bindPort, bindHost, () => { server.removeListener('error', onError); resolve(); });
  });
  const port = server.address().port;
  const origins = configuredOrigins ?? [`http://127.0.0.1:${port}`, `http://localhost:${port}`];
  requestOrigins = new Set(origins);
  for (const c of cases.values()) { [c.origin, c.childOrigin] = origins; }
  const tasks = [...cases.values()].map(c => ({
    id: c.id, caseID: c.id, title: c.title, category: c.category, goal: c.goal,
    startURL: `${c.origin}${c.base}/`, prompt: `${c.goal}\n起始页面：${c.origin}${c.base}/`,
    allowedOrigins: c.id === 'iframe-data' ? [c.origin, c.childOrigin] : [c.origin],
    allowedPathPrefixes: [`${c.base}/`], layout_variant: c.variant,
  }));
  return {
    tasks, TASKS, listener: Object.freeze({ host: bindHost, port }),
    reset(id) { reset(get(id)); },
    inspect(id) { return clone(get(id).state); },
    oracle(id) { return oracle(get(id)); },
    expectedAnswer(id) { return JSON.stringify(get(id).expected); },
    async prepare(id, { execute, direct }) {
      const c = get(id);
      if (id === 'worker-data') {
        // This case measures a task started after attachment. Another CDP
        // client (including Playwright) can resume a brand-new worker before
        // this collector enables Network, so startup coverage is not assumed.
        await waitFor(() => execute({ action: 'devtools', method: 'targets.list' }).then(successful), reply => reply.data.items.some(item => item.type === 'worker' && item.ready), 'worker attachment');
        await waitFor(() => execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.querySelector("output").textContent', returnByValue: true } }).then(successful), reply => reply.data.result.result.value.includes('已准备'), 'monitor ready');
        return { kind: 'controller_waited_for_worker_attachment', sample_started: false };
      }
      if (id === 'authenticated-write') {
        const owner = successful(await direct('/control', 'POST', { mode: 'human' }));
        const lease_token = owner.lease_token;
        if (!lease_token) throw new Error('Human lease missing');
        for (const body of [{ type: 'text', text: c.credentials.username }, { type: 'key', key: 'Tab' }, { type: 'text', text: c.credentials.password }, { type: 'key', key: 'Enter' }]) successful(await direct('/input', 'POST', { ...body, lease_token }));
        await waitFor(() => c.state.successful_logins, count => count === 1, 'simulated human login');
        successful(await direct('/control', 'POST', { mode: 'agent', lease_token }));
        return { kind: 'controller_simulated_human_login', authenticated: true };
      }
      if (id === 'debugger-recovery') {
        c.preparedPauseAnchor = null;
        const list = () => execute({ action: 'devtools', method: 'targets.list' }).then(successful).then(reply => reply.data.items);
        const target = await waitFor(list, items => items.some(item => item.type === 'worker' && item.ready), 'worker ready').then(items => items.find(item => item.type === 'worker' && item.ready));
        await waitFor(() => execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.querySelector("output").textContent', returnByValue: true } }).then(successful), reply => reply.data.result.result.value.includes('已准备'), 'worker job loaded');
        successful(await execute({ action: 'devtools', method: 'Debugger.enable', target: target.target_ref }));
        // The ordinary business action is intentionally dispatched once; a
        // pause response is valid and the evaluator must never retry it.
        await execute({ action: 'devtools', method: 'Runtime.evaluate', params: { expression: 'document.querySelector("button").click(); true', returnByValue: true } });
        const paused = await waitFor(() => execute({ action: 'devtools', method: 'events.list', target: target.target_ref, params: { method: 'Debugger.paused', limit: 100 } }).then(successful), reply => reply.data.items.some(item => item.method === 'Debugger.paused'), 'worker breakpoint');
        c.preparedPauseAnchor = preparedPauseAnchor(paused, target.target_ref);
        return { kind: 'controller_prepared_worker_breakpoint', paused: true };
      }
      return { kind: 'none' };
    },
    async close() { server.closeIdleConnections?.(); await new Promise(resolve => server.close(resolve)); },
  };
}
