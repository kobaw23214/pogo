const $ = selector => document.querySelector(selector);
const scenarioNames = { cookie: '标准登录 · Cookie 会话', bearer: '令牌登录 · Bearer', mfa: '二次验证 · 人工接管' };
const names = { overview: '工作概览', orders: '订单管理', tokens: '访问令牌' };
const state = { runId: null, user: null, scenario: 'cookie', challenge: null, page: 'overview', orders: [] };
let toastTimer;
const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
const money = value => `¥ ${value.toLocaleString('zh-CN', { minimumFractionDigits: 2 })}`;
const date = timestamp => new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(timestamp);
const savedToken = () => localStorage.getItem('mock_access_run') === state.runId ? localStorage.getItem('mock_access_token') : null;
function clearToken() { localStorage.removeItem('mock_access_token'); localStorage.removeItem('mock_access_run'); }
function toast(message) { clearTimeout(toastTimer); $('#toast').textContent = message; $('#toast').hidden = false; toastTimer = setTimeout(() => { $('#toast').hidden = true; }, 3300); }
function errorAt(id, error) { const element = $(`#${id}`); element.textContent = error?.message || ''; element.hidden = !error; }

async function api(endpoint, { method = 'GET', data, expire = true } = {}) {
  const headers = {};
  const token = savedToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (data) headers['Content-Type'] = 'application/json';
  const response = await fetch(endpoint, { method, headers, body: data ? JSON.stringify(data) : undefined, credentials: 'same-origin' });
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 401 && expire && state.user) showLogin('登录已失效，请重新登录。你的模拟订单会保留。');
    throw Object.assign(new Error(result.error || '请求失败，请重试'), { status: response.status, code: result.code });
  }
  return result;
}
async function busy(form, action) {
  const button = form.querySelector('button[type="submit"]');
  if (button.disabled) return;
  button.disabled = true; form.setAttribute('aria-busy', 'true');
  try { await action(); } finally { button.disabled = false; form.removeAttribute('aria-busy'); }
}
function showLogin(message = '') {
  state.user = null; state.challenge = null; clearToken();
  $('#workspace-view').hidden = true; $('#login-view').hidden = false;
  $('#login-form').hidden = false; $('#verification-form').hidden = true;
  $('#password').value = ''; $('#verification-code').value = '';
  $('#login-title').textContent = '登录你的工作空间';
  $('#session-notice').textContent = message; $('#session-notice').hidden = !message;
  $('#new-token-result').hidden = true; $('#new-token-value').value = '';
  $('#order-dialog').close();
  document.title = '登录 · 北辰工作台';
}
async function showWorkspace(login) {
  state.user = login.user; state.scenario = login.scenario;
  $('#login-view').hidden = true; $('#workspace-view').hidden = false;
  $('#password').value = ''; $('#verification-code').value = '';
  $('#new-token-result').hidden = true; $('#new-token-value').value = '';
  $('#session-notice').hidden = true;
  await selectPage('overview');
  await refreshLab();
}
function acceptLogin(result) {
  if (result.access_token) { localStorage.setItem('mock_access_token', result.access_token); localStorage.setItem('mock_access_run', state.runId); }
  else clearToken();
  return showWorkspace(result);
}

$('#login-form').addEventListener('submit', event => {
  event.preventDefault();
  void busy(event.currentTarget, async () => {
    errorAt('login-error', null); $('#session-notice').hidden = true;
    const scenario = $('input[name=scenario]:checked').value;
    try {
      const result = await api('/api/auth/login', { method: 'POST', data: { username: $('#username').value, password: $('#password').value, scenario }, expire: false });
      $('#password').value = '';
      if (result.requires_verification) {
        state.challenge = result.challenge_id;
        $('#login-form').hidden = true; $('#verification-form').hidden = false;
        $('#login-title').textContent = '验证你的身份';
        $('#verification-code').focus();
      } else await acceptLogin(result);
    } catch (error) { errorAt('login-error', error); }
  });
});
$('#verification-form').addEventListener('submit', event => {
  event.preventDefault();
  void busy(event.currentTarget, async () => {
    errorAt('verification-error', null);
    try { const result = await api('/api/auth/verify', { method: 'POST', data: { challenge_id: state.challenge, code: $('#verification-code').value }, expire: false }); state.challenge = null; await acceptLogin(result); }
    catch (error) { errorAt('verification-error', error); }
  });
});
$('#verification-back').addEventListener('click', () => { showLogin(); errorAt('verification-error', null); });
$('#toggle-password').addEventListener('click', event => {
  const showing = $('#password').type === 'password';
  $('#password').type = showing ? 'text' : 'password';
  event.currentTarget.textContent = showing ? '隐藏' : '显示';
  event.currentTarget.setAttribute('aria-label', showing ? '隐藏密码' : '显示密码');
});
$('#logout').addEventListener('click', async () => {
  try { await api('/api/auth/logout', { method: 'POST' }); showLogin('你已安全退出当前登录。'); await refreshLab(); }
  catch (error) { toast(error.message); }
});

function statusBadge(status) {
  return `<span class="status-badge ${status === '待确认' ? 'pending' : status === '处理中' ? 'processing' : ''}">${escape(status)}</span>`;
}
function orderTable(orders) {
  if (!orders.length) return '<div class="empty-state">没有找到符合条件的订单，试试其他关键词。</div>';
  return `<table><thead><tr><th scope="col">客户 / 商品</th><th scope="col">订单编号</th><th scope="col">订单金额</th><th scope="col">状态</th><th scope="col">日期</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead><tbody>${orders.map(order => `<tr><td><div class="order-customer">${escape(order.customer)}</div><div class="order-product">${escape(order.item)}</div></td><td class="order-id">${escape(order.id)}</td><td class="order-amount">${money(order.amount)}</td><td>${statusBadge(order.status)}</td><td>${escape(order.date)}</td><td><button class="table-link" data-order="${escape(order.id)}" type="button" aria-label="查看订单 ${escape(order.id)}">查看 ↗</button></td></tr>`).join('')}</tbody></table>`;
}
async function loadOrders(query = '') {
  const result = await api(`/api/orders${query ? `?q=${encodeURIComponent(query)}` : ''}`);
  state.orders = result.orders;
  $('#all-orders').innerHTML = orderTable(result.orders);
  $('#order-count').textContent = result.total;
  if (!query) $('#overview-orders').innerHTML = orderTable(result.orders);
}
async function selectPage(page) {
  state.page = page;
  for (const section of document.querySelectorAll('.workspace-page')) section.hidden = section.id !== `page-${page}`;
  for (const item of document.querySelectorAll('.main-nav button')) { if (item.dataset.page === page) item.setAttribute('aria-current', 'page'); else item.removeAttribute('aria-current'); }
  $('#breadcrumb-current').textContent = names[page]; document.title = `${names[page]} · 北辰工作台`;
  try { if (page === 'tokens') await loadTokens(); else await loadOrders(page === 'orders' ? $('#order-query').value : ''); }
  catch (error) { toast(error.message); }
}
for (const button of document.querySelectorAll('[data-page]')) button.addEventListener('click', () => { void selectPage(button.dataset.page); });
$('#view-all-orders').addEventListener('click', () => { $('#order-query').value = ''; void selectPage('orders'); });
$('#goto-pending').addEventListener('click', () => { $('#order-query').value = '远山文化'; void selectPage('orders'); });
$('#overview-refresh').addEventListener('click', async () => { try { await loadOrders(); toast('工作概览已更新'); } catch (error) { toast(error.message); } });
$('#orders-refresh').addEventListener('click', async () => { try { await loadOrders($('#order-query').value); toast('订单已更新'); } catch (error) { toast(error.message); } });
$('#order-search').addEventListener('submit', event => { event.preventDefault(); void loadOrders($('#order-query').value).catch(error => toast(error.message)); });
document.addEventListener('click', event => {
  const button = event.target.closest('button[data-order]');
  if (!button) return;
  const order = state.orders.find(item => item.id === button.dataset.order);
  if (!order) return;
  $('#order-detail').innerHTML = `<span class="order-detail-id">${escape(order.id)}</span><dl class="order-detail-grid"><dt>客户</dt><dd>${escape(order.customer)}</dd><dt>商品</dt><dd>${escape(order.item)}</dd><dt>订单金额</dt><dd>${money(order.amount)}</dd><dt>状态</dt><dd>${statusBadge(order.status)}</dd><dt>负责人</dt><dd>${escape(order.owner)}</dd><dt>创建日期</dt><dd>${escape(order.date)}</dd></dl>`;
  $('#order-dialog').showModal();
});
$('#close-order').addEventListener('click', () => $('#order-dialog').close());

async function loadTokens() {
  const result = await api('/api/tokens');
  $('#token-list').innerHTML = result.tokens.length ? result.tokens.map(token => `<div class="token-row"><span class="token-card-icon" aria-hidden="true">⚿</span><div class="token-row-info"><strong>${escape(token.name)}</strong><p>读取订单 · ${date(token.created_at)} 创建 · ${date(token.expires_at)} 过期</p></div><span class="status-badge ${token.state === 'active' ? '' : 'pending'}">${token.state === 'active' ? '使用中' : token.state === 'expired' ? '已过期' : '已撤销'}</span>${token.state === 'active' ? `<button type="button" class="quiet-button" data-revoke-token="${escape(token.id)}" aria-label="撤销令牌 ${escape(token.name)}">撤销</button>` : ''}</div>`).join('') : '<div class="empty-state">还没有访问令牌。为你的第一个连接起个名字吧。</div>';
}
$('#token-form').addEventListener('submit', event => {
  event.preventDefault();
  void busy(event.currentTarget, async () => {
    errorAt('token-error', null);
    try {
      const token = await api('/api/tokens', { method: 'POST', data: { name: $('#token-name').value } });
      $('#new-token-value').value = token.access_token; $('#new-token-result').hidden = false;
      $('#token-name').value = '';
      await loadTokens(); toast('访问令牌已创建');
    } catch (error) { errorAt('token-error', error); }
  });
});
$('#token-list').addEventListener('click', async event => {
  const button = event.target.closest('button[data-revoke-token]');
  if (!button || button.disabled) return;
  button.disabled = true;
  try { await api(`/api/tokens/${button.dataset.revokeToken}`, { method: 'DELETE' }); $('#new-token-result').hidden = true; $('#new-token-value').value = ''; await loadTokens(); toast('令牌已撤销，无法继续访问订单'); }
  catch (error) { toast(error.message); button.disabled = false; }
});
$('#copy-token').addEventListener('click', async () => {
  try { await navigator.clipboard.writeText($('#new-token-value').value); toast('测试令牌已复制'); }
  catch { $('#new-token-value').focus(); $('#new-token-value').select(); toast('请复制已选中的测试令牌'); }
});

async function refreshLab() {
  const lab = await api('/api/lab/status', { expire: false });
  $('#lab-run').textContent = lab.runId; $('#lab-run').title = lab.runId;
  $('#lab-scenario').textContent = state.user ? scenarioNames[state.scenario] : '尚未登录';
  const labels = { loginSubmissions: '登录提交', successfulLogins: '登录成功', verificationSubmissions: '验证提交', orderReads: '订单读取', tokenCreates: '令牌创建', unauthorizedRequests: '未授权请求' };
  $('#lab-counters').innerHTML = Object.entries(labels).map(([key, label]) => `<div class="counter"><span>${label}</span><strong>${lab.counters[key]}</strong></div>`).join('');
  $('#lab-expire').disabled = !state.user; $('#lab-revoke').disabled = !state.user;
  return lab;
}
$('#open-lab').addEventListener('click', () => { $('#lab-dialog').showModal(); void refreshLab().catch(error => toast(error.message)); });
$('#close-lab').addEventListener('click', () => $('#lab-dialog').close());
$('#lab-refresh').addEventListener('click', () => { void refreshLab().catch(error => toast(error.message)); });
for (const action of ['expire', 'revoke']) $(`#lab-${action}`).addEventListener('click', async () => {
  try {
    await api(`/api/lab/${action}`, { method: 'POST' });
    // Confirm with a real protected request; do not merely imitate an expired screen.
    try { await api('/api/orders'); } catch (error) { if (error.status !== 401) throw error; }
    $('#lab-message').textContent = action === 'expire' ? '凭据已过期。受保护的订单接口已返回 401，重新登录后可继续。' : '本会话凭据已撤销。受保护的订单接口已返回 401，重新登录后可继续。';
    $('#lab-message').hidden = false;
    await refreshLab();
  } catch (error) { toast(error.message); }
});
for (const dialog of document.querySelectorAll('dialog')) dialog.addEventListener('click', event => { if (event.target === dialog) { const box = dialog.getBoundingClientRect(); if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close(); } });

async function start() {
  const scenario = new URLSearchParams(location.search).get('scenario');
  if (Object.hasOwn(scenarioNames, scenario)) $(`input[name=scenario][value="${scenario}"]`).checked = true;
  try {
    const lab = await api('/api/lab/status', { expire: false });
    state.runId = lab.runId;
    if (localStorage.getItem('mock_access_run') !== state.runId) clearToken();
    try { await showWorkspace(await api('/api/auth/session', { expire: false })); }
    catch (error) { if (error.status === 401) showLogin(); else throw error; }
    await refreshLab();
  } catch (error) { errorAt('login-error', new Error(`无法连接本地模拟服务：${error.message}`)); }
}
void start();
