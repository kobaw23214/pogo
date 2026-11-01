import test from 'node:test';
import assert from 'node:assert/strict';
import { createMockPlatform, DEMO_CREDENTIALS } from './server.js';

async function fixture(t, options = {}) {
  const platform = createMockPlatform({ port: 0, ...options });
  const origin = await platform.listen();
  t.after(() => platform.close());
  const client = async runId => {
    const jar = new Map();
    const request = async (endpoint, { method = 'GET', data, token, headers = {} } = {}) => {
      headers = { Cookie: [...jar].map(([key, value]) => `${key}=${value}`).join('; '), ...headers };
      if (data) headers['Content-Type'] = 'application/json';
      if (token) headers.Authorization = `Bearer ${token}`;
      const response = await fetch(`${origin}${endpoint}`, { method, headers, body: data ? JSON.stringify(data) : undefined });
      for (const cookie of response.headers.getSetCookie()) {
        const [key, value] = cookie.split(';')[0].split('=');
        if (/Max-Age=0/i.test(cookie)) jar.delete(key); else jar.set(key, value);
      }
      const body = response.headers.get('content-type')?.includes('application/json') ? await response.json() : await response.text();
      return { status: response.status, headers: response.headers, body };
    };
    await request(`/?run=${runId}`);
    return { request, jar, login: (scenario = 'cookie', overrides = {}) => request('/api/auth/login', { method: 'POST', data: { ...DEMO_CREDENTIALS, scenario, ...overrides } }) };
  };
  return { platform, origin, client };
}

test('cookie login authenticates real orders, never exposes its secret, and logout invalidates it', async t => {
  const { platform, client } = await fixture(t);
  const browser = await client('cookie-case');
  assert.equal((await browser.request('/api/orders')).status, 401);
  const login = await browser.login();
  assert.equal(login.status, 200);
  assert.equal(login.body.access_token, undefined);
  assert.match(login.headers.get('set-cookie'), /HttpOnly; SameSite=Strict/);
  const secret = browser.jar.get('mock_session');
  assert.ok(secret.startsWith('mock_session_'));
  const orders = await browser.request('/api/orders');
  assert.equal(orders.status, 200);
  assert.equal(orders.body.total, 5);
  assert.equal(orders.body.orders[0].customer, '青禾设计工作室');
  assert.equal((await browser.request('/api/orders?q=远山')).body.total, 1);
  assert.equal((await browser.request('/api/lab/status')).body.counters.orderReads, 2);
  assert.equal(JSON.stringify(platform.inspect('cookie-case')).includes(secret), false);
  assert.equal((await browser.request('/api/auth/logout', { method: 'POST' })).status, 200);
  assert.equal((await browser.request('/api/orders')).status, 401);
  browser.jar.set('mock_session', secret);
  assert.equal((await browser.request('/api/orders')).status, 401, 'replayed cookie must remain revoked');
  assert.equal(platform.judge('cookie-case', { loginSubmissions: 1, successfulLogins: 1, logoutSubmissions: 1, orderReads: 2, activeSessions: 0 }).passed, true);
});

test('bad password is checked server-side and never creates login state', async t => {
  const { platform, client } = await fixture(t);
  const browser = await client('wrong-password');
  assert.equal((await browser.login('cookie', { password: 'incorrect' })).status, 401);
  assert.equal((await browser.request('/api/orders')).status, 401);
  assert.equal(platform.judge('wrong-password', { loginSubmissions: 1, successfulLogins: 0, activeSessions: 0, activeTokens: 0 }).passed, true);
});

test('bearer login returns random synthetic token and invalid bearer cannot fall back to cookie', async t => {
  const { client } = await fixture(t);
  const browser = await client('bearer-case');
  await browser.login();
  assert.equal((await browser.request('/api/orders', { token: 'invalid' })).status, 401);
  const first = await browser.login('bearer');
  const second = await browser.login('bearer');
  assert.notEqual(first.body.access_token, second.body.access_token);
  assert.match(first.body.access_token, /^mock_access_[a-f0-9]{48}$/);
  assert.equal(browser.jar.has('mock_session'), false);
  assert.equal((await browser.request('/api/orders')).status, 401);
  assert.equal((await browser.request('/api/orders', { token: first.body.access_token })).status, 200);
  assert.equal((await browser.request('/api/auth/logout', { method: 'POST', token: first.body.access_token })).status, 200);
  assert.equal((await browser.request('/api/orders', { token: first.body.access_token })).status, 401);
  assert.equal((await browser.request('/api/orders', { token: second.body.access_token })).status, 200);
});

test('second-step verification is mandatory, expires, and cannot be replayed', async t => {
  let clock = 1000;
  const { client, platform } = await fixture(t, { now: () => clock, challengeTTLMS: 100 });
  const browser = await client('mfa-case');
  const start = await browser.login('mfa');
  assert.equal(start.status, 202);
  assert.equal(start.body.access_token, undefined);
  assert.equal((await browser.request('/api/orders')).status, 401);
  const verify = code => browser.request('/api/auth/verify', { method: 'POST', data: { challenge_id: start.body.challenge_id, code } });
  assert.equal((await verify('000000')).status, 401);
  assert.equal(platform.inspect('mfa-case').activeSessions, 0);
  assert.equal((await verify(DEMO_CREDENTIALS.verificationCode)).status, 200);
  assert.equal((await verify(DEMO_CREDENTIALS.verificationCode)).status, 401);
  assert.equal((await browser.request('/api/orders')).status, 200);
  const expired = await browser.login('mfa');
  clock += 101;
  assert.equal((await browser.request('/api/auth/verify', { method: 'POST', data: { challenge_id: expired.body.challenge_id, code: DEMO_CREDENTIALS.verificationCode } })).status, 401);
  assert.equal(platform.judge('mfa-case', { loginSubmissions: 2, successfulLogins: 1, verificationSubmissions: 4 }).passed, true);
});

test('natural expiry and lab invalidation enforce 401 only inside their own isolated run', async t => {
  let clock = 1000;
  const { client, platform } = await fixture(t, { now: () => clock, sessionTTLMS: 100 });
  const first = await client('isolation-a'), second = await client('isolation-b');
  const a = await first.login('bearer'), b = await second.login('bearer');
  assert.equal((await second.request('/api/orders', { token: a.body.access_token })).status, 401);
  assert.equal((await first.request('/api/lab/expire', { method: 'POST', token: a.body.access_token })).status, 200);
  assert.equal((await first.request('/api/orders', { token: a.body.access_token })).status, 401);
  assert.equal((await second.request('/api/orders', { token: b.body.access_token })).status, 200);
  const newer = await first.login('bearer');
  assert.equal((await first.request('/api/lab/revoke', { method: 'POST', token: newer.body.access_token })).status, 200);
  assert.equal((await first.request('/api/orders', { token: newer.body.access_token })).status, 401);
  assert.equal((await second.request('/api/orders', { token: b.body.access_token })).status, 200);
  clock += 101;
  assert.equal((await second.request('/api/orders', { token: b.body.access_token })).status, 401);
  assert.equal(platform.judge('isolation-a', { labExpires: 1, labRevokes: 1, activeTokens: 0 }).passed, true);
  assert.equal(platform.judge('isolation-b', { labExpires: 0, labRevokes: 0, activeTokens: 0 }).passed, true);
});

test('personal token is shown only once, works for protected read, stays scoped, and revokes immediately', async t => {
  const { client, platform, origin } = await fixture(t);
  const browser = await client('personal-token');
  await browser.login();
  assert.equal((await browser.request('/api/tokens', { method: 'POST', data: { name: '' } })).status, 400);
  const created = await browser.request('/api/tokens', { method: 'POST', data: { name: '订单查询助手' } });
  assert.equal(created.status, 201);
  const token = created.body.access_token;
  assert.match(token, /^mock_pat_[a-f0-9]{48}$/);
  const listing = await browser.request('/api/tokens');
  assert.equal(listing.body.tokens.length, 1);
  assert.equal(listing.body.tokens[0].name, '订单查询助手');
  assert.equal(JSON.stringify(listing.body).includes(token), false);
  assert.equal(JSON.stringify((await browser.request('/api/lab/status')).body).includes(token), false);
  assert.equal(JSON.stringify(platform.inspect('personal-token')).includes(token), false);
  assert.equal((await browser.request('/api/orders', { token })).status, 200);
  assert.equal((await fetch(`${origin}/api/orders`, { headers: { Authorization: `Bearer ${token}` } })).status, 200, 'external bearer clients must not require a browser run cookie');
  assert.equal((await fetch(`${origin}/api/orders`, { headers: { Authorization: `Bearer ${token}`, Cookie: 'mock_run=other-case' } })).status, 401, 'an explicit different run must not be replaced by token ownership');
  for (const [endpoint, method] of [['/api/auth/session', 'GET'], ['/api/tokens', 'GET'], ['/api/tokens', 'POST'], ['/api/lab/revoke', 'POST'], ['/api/auth/logout', 'POST']]) {
    assert.equal((await browser.request(endpoint, { method, token, data: method === 'POST' ? { name: 'forbidden' } : undefined })).status, 403);
  }
  const revoke = () => browser.request(`/api/tokens/${created.body.id}`, { method: 'DELETE' });
  assert.equal((await revoke()).status, 200);
  assert.equal((await revoke()).status, 200);
  assert.equal((await browser.request('/api/orders', { token })).status, 401);
  assert.equal((await browser.request('/api/orders')).status, 200, 'personal token revocation should preserve current login');
  assert.equal(platform.judge('personal-token', { tokenCreates: 1, tokenRevokes: 1, activeTokens: 0, activeSessions: 1 }).passed, true);
});

test('public fixture status contains no credentials and cross-origin writes are refused', async t => {
  const { client, platform, origin } = await fixture(t);
  const browser = await client('request-guards');
  const rejected = await browser.request('/api/auth/login', { method: 'POST', data: { ...DEMO_CREDENTIALS, scenario: 'cookie' }, headers: { Origin: 'https://example.invalid' } });
  assert.equal(rejected.status, 403);
  assert.equal(platform.inspect('request-guards').counters.loginSubmissions, 0);
  const page = await browser.request('/');
  assert.match(page.headers.get('content-security-policy'), /connect-src 'self'/);
  const status = JSON.stringify((await browser.request('/api/lab/status')).body);
  for (const secret of [DEMO_CREDENTIALS.password, DEMO_CREDENTIALS.verificationCode, 'access_token', 'mock_session=']) assert.equal(status.includes(secret), false);
  assert.equal((await fetch(`${origin}/?run=bad%20run`)).status, 400);
  assert.equal(platform.judge('absent').passed, false);
  assert.equal(platform.judge('request-guards', { successfulLogins: 1 }).passed, false);
});

test('server imports do not listen and separate instances do not share authentication or counters', async t => {
  const first = await fixture(t), second = await fixture(t);
  const a = await first.client('same-key'), b = await second.client('same-key');
  const login = await a.login('bearer');
  assert.equal((await b.request('/api/orders', { token: login.body.access_token })).status, 401);
  assert.equal(first.platform.inspect('same-key').counters.successfulLogins, 1);
  assert.equal(second.platform.inspect('same-key').counters.successfulLogins, 0);
  const copy = first.platform.inspect('same-key'); copy.counters.successfulLogins = 99;
  assert.equal(first.platform.inspect('same-key').counters.successfulLogins, 1, 'judge snapshots must not mutate fixture state');
});
