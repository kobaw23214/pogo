import http from 'node:http';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const SCENARIOS = new Set(['cookie', 'bearer', 'mfa']);
export const DEMO_CREDENTIALS = Object.freeze({ username: 'demo', password: 'pogo-demo-2026', verificationCode: '246810' });
const USER = Object.freeze({ id: 'demo-user', name: '林沐', role: '运营管理员', workspace: '北辰工作空间' });
const ORDERS = Object.freeze([
  { id: 'BC-20261002-1048', customer: '青禾设计工作室', item: '团队协作订阅 · 专业版', amount: 2680, status: '已完成', date: '2026-10-02', owner: '林沐' },
  { id: 'BC-20261002-1047', customer: '远山文化', item: '品牌资产空间 · 年度', amount: 8400, status: '待确认', date: '2026-10-02', owner: '苏禾' },
  { id: 'BC-20261001-1046', customer: '木石建筑事务所', item: '团队协作订阅 · 基础版', amount: 1280, status: '已完成', date: '2026-10-01', owner: '陈可' },
  { id: 'BC-20261001-1045', customer: '南风咖啡', item: '门店运营空间 · 季度', amount: 3960, status: '处理中', date: '2026-10-01', owner: '林沐' },
  { id: 'BC-20260930-1044', customer: '轻舟科技', item: '团队协作订阅 · 专业版', amount: 2680, status: '已完成', date: '2026-09-30', owner: '苏禾' },
]);
const ASSETS = new Map([
  ['/', ['index.html', 'text/html; charset=utf-8']],
  ['/app.js', ['app.js', 'text/javascript; charset=utf-8']],
  ['/styles.css', ['styles.css', 'text/css; charset=utf-8']],
]);

const randomID = prefix => `${prefix}_${randomBytes(24).toString('hex')}`;
const cookieOptions = 'Path=/; HttpOnly; SameSite=Strict';
const same = (left, right) => {
  if (typeof left !== 'string' || typeof right !== 'string') return false;
  const a = Buffer.from(left), b = Buffer.from(right);
  return a.length === b.length && timingSafeEqual(a, b);
};
function cookies(request) {
  return Object.fromEntries((request.headers.cookie || '').split(';').map(item => item.trim().split('=')).filter(item => item.length === 2));
}
async function body(request) {
  let raw = '';
  for await (const chunk of request) {
    raw += chunk;
    if (Buffer.byteLength(raw) > 8192) throw Object.assign(new Error('请求内容过长'), { status: 413 });
  }
  if (!raw) return {};
  try { return JSON.parse(raw); } catch { throw Object.assign(new Error('请求格式无效'), { status: 400 }); }
}
function respond(response, status, data, headers = {}) {
  response.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'same-origin', ...headers,
  });
  response.end(JSON.stringify(data));
}

/** Isolated local fixture. No state or listener is created merely by importing. */
export function createMockPlatform({ host = '127.0.0.1', port = 5190, now = Date.now, sessionTTLMS = 15 * 60_000, challengeTTLMS = 5 * 60_000 } = {}) {
  const runs = new Map();
  const getRun = id => runs.get(id);
  const newRun = id => {
    const run = {
      id, scenario: 'cookie', sessions: new Map(), tokens: new Map(), challenges: new Map(),
      counters: { loginSubmissions: 0, successfulLogins: 0, verificationSubmissions: 0, orderReads: 0, unauthorizedRequests: 0, tokenCreates: 0, tokenRevokes: 0, logoutSubmissions: 0, labExpires: 0, labRevokes: 0 },
      events: [], nextSequence: 1,
    };
    runs.set(id, run);
    return run;
  };
  const event = (run, action, outcome) => {
    run.events.push({ sequence: run.nextSequence++, at: now(), action, outcome });
    if (run.events.length > 200) run.events.shift();
  };
  const alive = credential => credential && !credential.revoked && credential.expiresAt > now();
  const inspect = id => {
    const run = getRun(id);
    if (!run) return null;
    return structuredClone({
      runId: run.id, scenario: run.scenario, counters: run.counters,
      activeSessions: [...run.sessions.values()].filter(alive).length,
      activeTokens: [...run.tokens.values()].filter(alive).length,
      pendingChallenges: [...run.challenges.values()].filter(ch => !ch.used && ch.expiresAt > now()).length,
      events: run.events,
    });
  };
  const authorize = (request, run) => {
    const authorization = request.headers.authorization;
    // A supplied bearer credential must stand on its own; an invalid token never silently falls back to a cookie.
    if (authorization) {
      if (!authorization.startsWith('Bearer ')) return null;
      const credential = run.tokens.get(authorization.slice(7));
      return alive(credential) ? { credential, kind: 'bearer' } : null;
    }
    const credential = run.sessions.get(cookies(request).mock_session);
    return alive(credential) ? { credential, kind: 'cookie' } : null;
  };
  const issueLogin = (run, scenario, response) => {
    const secret = randomID(scenario === 'bearer' ? 'mock_access' : 'mock_session');
    const credential = { id: randomID('cred'), name: '当前登录', purpose: 'login', createdAt: now(), expiresAt: now() + sessionTTLMS, revoked: false };
    (scenario === 'bearer' ? run.tokens : run.sessions).set(secret, credential);
    run.scenario = scenario;
    run.counters.successfulLogins++;
    event(run, 'login', 'success');
    const data = { user: USER, scenario, expires_at: credential.expiresAt, run_id: run.id };
    if (scenario === 'bearer') {
      data.access_token = secret;
      data.token_type = 'Bearer';
      respond(response, 200, data, { 'Set-Cookie': `mock_session=; ${cookieOptions}; Max-Age=0` });
    } else respond(response, 200, data, { 'Set-Cookie': `mock_session=${secret}; ${cookieOptions}` });
  };

  const server = http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://localhost');
      // Reject cross-origin writes and fetches before touching fixture state. No permissive CORS.
      if (request.headers.origin && request.headers.origin !== `http://${request.headers.host}`) {
        respond(response, 403, { error: '仅允许本页面发起请求', code: 'ORIGIN_MISMATCH' }); return;
      }
      if (request.headers['sec-fetch-site'] === 'cross-site') {
        respond(response, 403, { error: '不接受跨站请求', code: 'CROSS_SITE' }); return;
      }
      if (request.method === 'GET' && ASSETS.has(url.pathname)) {
        const [file, contentType] = ASSETS.get(url.pathname);
        const headers = {
          'Content-Type': contentType, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff',
          'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'",
          'Referrer-Policy': 'same-origin',
        };
        if (url.pathname === '/') {
          const requested = url.searchParams.get('run');
          if (requested && !/^[a-zA-Z0-9_-]{1,80}$/.test(requested)) {
            respond(response, 400, { error: '测试标识仅支持 1–80 位字母、数字、下划线或连字符' }); return;
          }
          const id = requested || cookies(request).mock_run || randomID('run');
          const run = getRun(id) || newRun(id);
          headers['Set-Cookie'] = `mock_run=${run.id}; ${cookieOptions}`;
        }
        response.writeHead(200, headers);
        response.end(await readFile(path.join(ROOT, 'public', file))); return;
      }
      if (!url.pathname.startsWith('/api/')) { respond(response, 404, { error: '页面不存在' }); return; }
      const requestedRun = cookies(request).mock_run;
      const bearer = request.headers.authorization?.startsWith('Bearer ') ? request.headers.authorization.slice(7) : null;
      // External read-only integrations send only a bearer token. Its owner identifies the
      // run; an explicitly supplied different run cookie must never be silently replaced.
      const run = requestedRun ? getRun(requestedRun) : bearer ? [...runs.values()].find(candidate => candidate.tokens.has(bearer)) : null;
      if (!run) { respond(response, 401, { error: '请先打开模拟平台页面', code: 'RUN_REQUIRED' }); return; }
      if (request.method === 'GET' && url.pathname === '/api/lab/status') {
        respond(response, 200, { ...inspect(run.id), demo: true }); return;
      }
      if (request.method === 'POST' && url.pathname === '/api/auth/login') {
        const data = await body(request);
        run.counters.loginSubmissions++;
        if (!SCENARIOS.has(data.scenario)) { respond(response, 400, { error: '请选择登录场景' }); return; }
        if (!same(data.username, DEMO_CREDENTIALS.username) || !same(data.password, DEMO_CREDENTIALS.password)) {
          event(run, 'login', 'rejected'); respond(response, 401, { error: '账号或密码不正确', code: 'INVALID_CREDENTIALS' }); return;
        }
        if (data.scenario === 'mfa') {
          const challenge = randomID('challenge');
          run.challenges.set(challenge, { expiresAt: now() + challengeTTLMS, used: false });
          event(run, 'login', 'verification_required');
          respond(response, 202, { requires_verification: true, challenge_id: challenge, expires_at: now() + challengeTTLMS }); return;
        }
        issueLogin(run, data.scenario, response); return;
      }
      if (request.method === 'POST' && url.pathname === '/api/auth/verify') {
        const data = await body(request);
        run.counters.verificationSubmissions++;
        const challenge = run.challenges.get(data.challenge_id);
        if (!challenge || challenge.used || challenge.expiresAt <= now()) {
          event(run, 'verification', 'invalid_challenge'); respond(response, 401, { error: '验证已失效，请重新登录', code: 'INVALID_CHALLENGE' }); return;
        }
        if (!same(data.code, DEMO_CREDENTIALS.verificationCode)) {
          event(run, 'verification', 'rejected'); respond(response, 401, { error: '验证码不正确', code: 'INVALID_CODE' }); return;
        }
        challenge.used = true;
        issueLogin(run, 'mfa', response); return;
      }
      const auth = authorize(request, run);
      if (!auth) {
        run.counters.unauthorizedRequests++;
        event(run, 'protected_request', 'unauthorized');
        respond(response, 401, { error: '登录已失效，请重新登录', code: 'UNAUTHENTICATED' }); return;
      }
      if (auth.credential.purpose !== 'login' && !(request.method === 'GET' && url.pathname === '/api/orders')) {
        respond(response, 403, { error: '此令牌仅支持读取订单', code: 'SCOPE_DENIED' }); return;
      }
      if (request.method === 'GET' && url.pathname === '/api/auth/session') {
        respond(response, 200, { user: USER, scenario: run.scenario, auth_kind: auth.kind, expires_at: auth.credential.expiresAt, run_id: run.id }); return;
      }
      if (request.method === 'GET' && url.pathname === '/api/orders') {
        run.counters.orderReads++;
        event(run, 'orders.read', 'success');
        const query = (url.searchParams.get('q') || '').toLowerCase();
        const rows = ORDERS.filter(order => `${order.id} ${order.customer} ${order.item}`.toLowerCase().includes(query));
        respond(response, 200, { orders: rows, total: rows.length, currency: 'CNY', demo: true }); return;
      }
      if (request.method === 'GET' && url.pathname === '/api/tokens') {
        respond(response, 200, { tokens: [...run.tokens.values()].filter(t => t.purpose === 'personal').map(t => ({ id: t.id, name: t.name, created_at: t.createdAt, expires_at: t.expiresAt, state: t.revoked ? 'revoked' : t.expiresAt <= now() ? 'expired' : 'active', scope: 'orders:read' })) }); return;
      }
      if (request.method === 'POST' && url.pathname === '/api/tokens') {
        if (auth.credential.purpose !== 'login') { respond(response, 403, { error: '请通过账号登录后管理访问令牌' }); return; }
        const data = await body(request);
        const name = typeof data.name === 'string' ? data.name.trim() : '';
        if (!name || name.length > 40) { respond(response, 400, { error: '请输入 1–40 个字符的令牌名称' }); return; }
        const secret = randomID('mock_pat');
        const token = { id: randomID('pat'), name, purpose: 'personal', createdAt: now(), expiresAt: now() + sessionTTLMS, revoked: false };
        run.tokens.set(secret, token);
        run.counters.tokenCreates++;
        event(run, 'token.create', 'success');
        respond(response, 201, { id: token.id, name, access_token: secret, token_type: 'Bearer', scope: 'orders:read', expires_at: token.expiresAt }); return;
      }
      if (request.method === 'DELETE' && url.pathname.startsWith('/api/tokens/')) {
        if (auth.credential.purpose !== 'login') { respond(response, 403, { error: '请通过账号登录后管理访问令牌' }); return; }
        const token = [...run.tokens.values()].find(t => t.id === url.pathname.slice('/api/tokens/'.length) && t.purpose === 'personal');
        if (!token) { respond(response, 404, { error: '令牌不存在' }); return; }
        if (!token.revoked) { token.revoked = true; run.counters.tokenRevokes++; event(run, 'token.revoke', 'success'); }
        respond(response, 200, { revoked: true }); return;
      }
      // Personal tokens are deliberately read-only and cannot log out or manipulate the lab.
      if (auth.credential.purpose !== 'login') { respond(response, 403, { error: '此令牌仅支持读取订单', code: 'SCOPE_DENIED' }); return; }
      if (request.method === 'POST' && url.pathname === '/api/auth/logout') {
        auth.credential.revoked = true;
        run.counters.logoutSubmissions++;
        event(run, 'logout', 'success');
        respond(response, 200, { logged_out: true }, { 'Set-Cookie': `mock_session=; ${cookieOptions}; Max-Age=0` }); return;
      }
      if (request.method === 'POST' && ['/api/lab/expire', '/api/lab/revoke'].includes(url.pathname)) {
        const expire = url.pathname.endsWith('/expire');
        for (const credential of [...run.sessions.values(), ...run.tokens.values()]) {
          if (expire) credential.expiresAt = now() - 1;
          else credential.revoked = true;
        }
        run.counters[expire ? 'labExpires' : 'labRevokes']++;
        event(run, expire ? 'lab.expire' : 'lab.revoke', 'success');
        respond(response, 200, { invalidated: true }); return;
      }
      respond(response, 404, { error: '接口不存在' });
    } catch (error) {
      if (!response.headersSent) respond(response, error.status || 500, { error: error.status ? error.message : '模拟服务暂时不可用' });
      else response.end();
    }
  });
  return {
    server, inspect,
    judge(runId, expected = {}) {
      const state = inspect(runId);
      if (!state) return { passed: false, failures: ['测试会话不存在'], state: null };
      const failures = Object.entries(expected).filter(([key, value]) => (key in state.counters ? state.counters[key] : state[key]) !== value).map(([key, value]) => `${key}: expected ${value}, got ${key in state.counters ? state.counters[key] : state[key]}`);
      return { passed: failures.length === 0, failures, state };
    },
    async listen() {
      await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, host, () => { server.off('error', reject); resolve(); }); });
      const address = server.address();
      return `http://${host.includes(':') ? `[${host}]` : host}:${address.port}`;
    },
    async close() {
      server.closeAllConnections();
      if (!server.listening) return;
      await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    },
  };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const portArg = args.find(arg => arg.startsWith('--port='))?.slice(7) ?? process.env.MOCK_PLATFORM_PORT ?? '5190';
  const port = Number(portArg);
  const host = args.find(arg => arg.startsWith('--host='))?.slice(7) ?? '127.0.0.1';
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('端口必须为 0–65535 的整数');
  if (!['127.0.0.1', '0.0.0.0', '::1'].includes(host)) throw new Error('监听地址只支持 127.0.0.1、0.0.0.0 或 ::1');
  const platform = createMockPlatform({ host, port });
  const address = await platform.listen();
  console.log(`北辰模拟平台已启动：${address}`);
  console.log('仅含合成测试数据。账号 demo；密码 pogo-demo-2026；模拟验证码 246810。');
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => { void platform.close().then(() => process.exit(0)); });
}
