import http from 'node:http';
import { randomUUID } from 'node:crypto';

// This module is loaded by the evaluator, never served to the browser. Expected
// answers, reference scripts and judges stay in these server-side closures.
const definitions = [
  ['catalog', 'search_filter', 'Search and filter a product catalog', 'Find the cheapest available product matching the requested filters.'],
  ['compare', 'cross_page_compare', 'Compare supplier offers', 'Compare product and delivery costs on separate supplier pages.'],
  ['registration', 'unique_form', 'Register exactly once', 'Complete a registration and report its confirmation number.'],
  ['pages', 'pagination', 'Aggregate a paginated ledger', 'Read every ledger page and aggregate the requested records.'],
  ['spa', 'spa_state', 'Update a live task board', 'Complete the requested task and report the updated board state.'],
  ['tabs', 'multi_tab', 'Compare information in separate tabs', 'Open the quote and delivery references in separate tabs and combine their prices.'],
  ['iframe', 'cross_origin_iframe', 'Submit an embedded support request', 'Use the embedded partner form and report the resulting ticket.'],
  ['shadow', 'open_shadow', 'Submit a component request', 'Complete the project form inside the component and report its receipt.'],
  ['delayed', 'delayed_load', 'Reserve delayed inventory', 'Load the stock data, choose the requested item and reserve it once.'],
  ['checkout', 'checkout', 'Complete a cart checkout', 'Build the specified cart, apply the coupon and place exactly one order.'],
];

export const TASKS = Object.freeze(definitions.flatMap(([prefix, category, title, goal]) => ['a', 'b'].map(variant => Object.freeze({
  id: `${prefix}-${variant}`, title: `${title} (${variant.toUpperCase()})`, category, goal, max_steps: 35, timeout_ms: 180_000,
}))));

const escape = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const scriptJSON = value => JSON.stringify(value).replace(/</g, '\\u003c');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

function seeded(seed, taskId) {
  let state = 2166136261;
  for (const c of JSON.stringify([seed, taskId])) state = Math.imul(state ^ c.charCodeAt(0), 16777619) >>> 0;
  return (min, max) => { state = (Math.imul(1664525, state) + 1013904223) >>> 0; return min + state % (max - min + 1); };
}

function page(title, content, script = '') {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>${escape(title)}</title><style>
    body{font:17px system-ui,sans-serif;max-width:1000px;margin:24px auto;padding:0 20px;color:#142336;background:#fafbfc}
    h1{font-size:28px}label{display:block;margin:12px 0}input,select,button{font:inherit;padding:7px;margin:3px}button,a{cursor:pointer}
    table{border-collapse:collapse;width:100%;margin:18px 0}td,th{border:1px solid #ccd6e0;padding:9px;text-align:left}
    nav{display:flex;gap:20px;margin:16px 0}article{border:1px solid #ccd6e0;padding:12px;margin:12px 0}iframe{width:100%;height:530px;border:1px solid #ccd6e0}
    [role=status]{padding:10px;background:#eaf5ed;min-height:24px}small{color:#456}</style></head><body><h1>${escape(title)}</h1>${content}${script ? `<script>${script}</script>` : ''}</body></html>`;
}

function answerObject(answer) {
  if (answer && typeof answer === 'object' && !Array.isArray(answer)) return answer;
  if (typeof answer !== 'string') return null;
  try { return JSON.parse(answer.slice(answer.indexOf('{'), answer.lastIndexOf('}') + 1)); } catch { return null; }
}

function sameFields(actual, expected) {
  return actual && Object.entries(expected).every(([key, value]) => typeof value === 'number' ? Number(actual[key]) === value && actual[key] !== null && actual[key] !== '' : actual[key] === value);
}

async function body(request) {
  const chunks = []; let size = 0;
  for await (const chunk of request) { size += chunk.length; if (size > 16_384) throw new Error('request too large'); chunks.push(chunk); }
  const raw = Buffer.concat(chunks).toString();
  return request.headers['content-type']?.includes('application/json') ? JSON.parse(raw || '{}') : Object.fromEntries(new URLSearchParams(raw));
}

function scenario(task, seed, repeat, origin) {
  const rand = seeded(seed, task.id);
  const scenarioId = randomUUID();
  const base = `/case/${scenarioId}`;
  const startURL = origin + base + '/';
  const state = { visits: new Set(), records: [], filters: [], tabVisits: new Map(), cart: {}, coupon: '', tasks: [] };
  const prefix = task.id.split('-')[0];
  const n = rand(100, 900), person = { name: `Test User ${n}`, email: `user${n}@example.test` };
  const data = {};
  let goal, expected, ready = () => true;
  const money = value => `${value} credits`;

  if (prefix === 'catalog') {
    data.category = task.id.endsWith('a') ? 'office' : 'travel'; data.limit = 95; data.query = 'Nova';
    data.products = Array.from({ length: 15 }, (_, i) => ({ sku: `SKU-${n + i}`, name: `${i % 5 ? 'Nova' : 'Orbit'} ${i + 1}`, category: i % 2 ? 'office' : 'travel', price: 20 + i * 4 + rand(0, 2), stock: i % 4 !== 0 }));
    data.winner = data.products.filter(p => p.name.includes(data.query) && p.category === data.category && p.stock && p.price <= data.limit).sort((a, b) => a.price - b.price)[0];
    goal = `Use the catalog filters for name containing "${data.query}", category "${data.category}", in stock only, price at most ${data.limit}. Find the cheapest matching product. Return JSON {"sku":"...","price":0}.`;
    expected = { sku: data.winner.sku, price: data.winner.price };
    ready = () => state.filters.some(f => f.q === data.query && f.category === data.category && f.stock === 'yes' && Number(f.max) === data.limit);
  } else if (prefix === 'compare') {
    data.limit = 4;
    const lowTotal = rand(65, 75), highTotal = lowTotal + rand(5, 15);
    data.offers = [ { supplier: `Cedar-${n}`, price: (task.id.endsWith('a') ? lowTotal : highTotal) - 17, shipping: 17, days: 3 }, { supplier: `Birch-${n}`, price: task.id.endsWith('a') ? highTotal : lowTotal, shipping: 0, days: 2 }, { supplier: `Maple-${n}`, price: 30, shipping: 5, days: 7 } ];
    data.winner = data.offers.filter(p => p.days <= data.limit).sort((a, b) => (a.price + a.shipping) - (b.price + b.shipping))[0];
    goal = `Visit all three supplier offers. Choose the lowest total price (item plus delivery) that arrives in at most ${data.limit} days. Return JSON {"supplier":"...","total":0}.`;
    expected = { supplier: data.winner.supplier, total: data.winner.price + data.winner.shipping };
    ready = () => data.offers.every((_, i) => state.visits.has(`/offer/${i}`));
  } else if (['registration', 'iframe', 'shadow'].includes(prefix)) {
    data.choice = task.id.endsWith('a') ? 'morning' : 'afternoon'; data.quantity = rand(1, 3);
    const kind = prefix === 'registration' ? 'registration' : prefix === 'iframe' ? 'support ticket' : 'project request';
    goal = `Submit exactly one ${kind} for name "${person.name}", email "${person.email}", session "${data.choice}" and quantity ${data.quantity}. ${prefix === 'iframe' ? 'The form is in the embedded partner frame. ' : ''}Return JSON {"${prefix === 'iframe' ? 'ticket_id' : prefix === 'shadow' ? 'request_id' : 'registration_id'}":"..."} using the displayed receipt.`;
    ready = () => state.records.length === 1 && sameFields(state.records[0], { ...person, choice: data.choice, quantity: data.quantity });
  } else if (prefix === 'pages') {
    data.region = task.id.endsWith('a') ? 'North' : 'South';
    data.rows = Array.from({ length: 12 }, (_, i) => ({ id: `INV-${n + i}`, region: i % 2 ? 'North' : 'South', status: i % 3 ? 'paid' : 'pending', amount: rand(10, 90) }));
    const selected = data.rows.filter(r => r.region === data.region && r.status === 'paid');
    expected = { count: selected.length, total: selected.reduce((sum, r) => sum + r.amount, 0) };
    goal = `Read all three ledger pages. For ${data.region} records with status paid, report the number of records and their total amount. Return JSON {"count":0,"total":0}.`;
    ready = () => [1, 2, 3].every(i => state.visits.has(`/ledger?page=${i}`));
  } else if (prefix === 'spa') {
    state.tasks = Array.from({ length: 5 }, (_, i) => ({ id: `TASK-${n + i}`, title: ['Draft agenda', 'Review budget', 'Verify labels', 'Check report', 'Send summary'][i], done: i === 4 }));
    data.target = state.tasks[task.id.endsWith('a') ? 1 : 2].id;
    goal = `On the task board, mark only ${data.target} completed. Report its ID and the number of tasks still pending afterward. Return JSON {"task_id":"...","pending":0}.`;
    expected = { task_id: data.target, pending: 3 };
    ready = () => state.tasks.filter(t => t.done).length === 2 && state.tasks.find(t => t.id === data.target).done;
  } else if (prefix === 'tabs') {
    data.price = rand(40, 90); data.shipping = rand(6, 19); data.quantity = rand(2, 4); data.quote = `Q-${n}`;
    goal = `Open the quote and delivery references in two separate browser tabs using their links. For quote ${data.quote}, buy ${data.quantity} units and add the flat delivery fee once. Return JSON {"quote":"...","total":0}.`;
    expected = { quote: data.quote, total: data.price * data.quantity + data.shipping };
    ready = () => [...(state.tabVisits.get('quote') ?? [])].some(a => [...(state.tabVisits.get('delivery') ?? [])].some(b => a !== b));
  } else if (prefix === 'delayed') {
    data.quantity = rand(2, 4);
    data.stock = Array.from({ length: 4 }, (_, i) => ({ sku: `STOCK-${n + i}`, stock: 10 + i * 9 + rand(0, 4), region: i === 3 ? 'East' : 'West' }));
    data.winner = data.stock.filter(s => s.region === 'West').sort((a, b) => b.stock - a.stock)[0];
    goal = `Load the inventory and wait until it finishes. From the West warehouse, choose the SKU with the largest available stock and reserve exactly ${data.quantity} units once. Return JSON {"sku":"...","quantity":0,"reservation_id":"..."}.`;
    ready = () => state.visits.has('/inventory') && state.records.length === 1 && sameFields(state.records[0], { sku: data.winner.sku, quantity: data.quantity });
  } else {
    data.products = [{ sku: `PEN-${n}`, name: 'Blue pen', price: rand(10, 20) }, { sku: `PAD-${n}`, name: 'Grid pad', price: rand(25, 40) }, { sku: `BAG-${n}`, name: 'Canvas bag', price: 50 }];
    data.items = { [data.products[0].sku]: 2, [data.products[1].sku]: task.id.endsWith('a') ? 1 : 3 };
    data.coupon = 'SAVE10';
    data.total = Math.floor(data.products.reduce((sum, p) => sum + p.price * (data.items[p.sku] ?? 0), 0) * 0.9);
    goal = `Add ${data.items[data.products[0].sku]} units of ${data.products[0].sku} and ${data.items[data.products[1].sku]} units of ${data.products[1].sku}, and no other products. Apply coupon ${data.coupon} (10% off, rounded down to a whole credit), then place exactly one order. Return JSON {"order_id":"...","total":0}.`;
    ready = () => state.records.length === 1 && Object.keys(state.records[0].items).length === Object.keys(data.items).length && sameFields(state.records[0].items, data.items) && state.records[0].coupon === data.coupon;
  }

  function form(action, button = 'Submit once') {
    return `<form method="post" action="${base}/${action}"><label>Name<input name="name" id="name" required></label><label>Email<input name="email" id="email" type="email" required></label><label>Session<select name="choice" id="choice" aria-label="Session"><option value="morning">morning</option><option value="afternoon">afternoon</option></select></label><label>Quantity<input name="quantity" id="quantity" type="number" min="1" max="5" value="1" required></label><button id="submit">${button}</button></form>`;
  }
  function apiScript() { return `const endpoint=${scriptJSON(base)};async function send(path,data){const r=await fetch(endpoint+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});const v=await r.json();if(!r.ok)throw Error(v.error);return v}`; }
  const sendJSON = (res, value, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(value)); };
  const sendHTML = (res, content) => { res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }); res.end(content); };

  async function handle(req, res, path, url) {
    const isGet = req.method === 'GET';
    if (isGet) state.visits.add(path === '/ledger' ? `${path}?page=${url.searchParams.get('page') ?? '1'}` : path);
    if (prefix === 'catalog' && isGet && path === '/') {
      const filters = Object.fromEntries(url.searchParams); if (url.search) state.filters.push(filters);
      const rows = data.products.filter(p => (!filters.q || p.name.toLowerCase().includes(filters.q.toLowerCase())) && (!filters.category || p.category === filters.category) && (filters.stock !== 'yes' || p.stock) && (!filters.max || p.price <= Number(filters.max)));
      return sendHTML(res, page('Product catalog', `<form><label>Name contains<input id="q" name="q" value="${escape(filters.q ?? '')}"></label><label>Category<select id="category" name="category"><option value="">All</option>${['office', 'travel'].map(c => `<option ${filters.category === c ? 'selected' : ''}>${c}</option>`).join('')}</select></label><label>Maximum price<input id="max" name="max" type="number" value="${escape(filters.max ?? '')}"></label><label>Availability<select id="stock" name="stock"><option value="">All</option><option value="yes" ${filters.stock === 'yes' ? 'selected' : ''}>In stock only</option></select></label><button id="filter">Filter products</button></form><table id="results"><tr><th>SKU</th><th>Name</th><th>Category</th><th>Price</th><th>Stock</th></tr>${rows.map(p => `<tr><td>${p.sku}</td><td>${p.name}</td><td>${p.category}</td><td>${p.price}</td><td>${p.stock ? 'available' : 'sold out'}</td></tr>`).join('')}</table>`));
    }
    if (prefix === 'compare' && isGet) {
      if (path === '/') return sendHTML(res, page('Supplier directory', `<p>Prices are per item. Delivery fees are charged once.</p><nav>${data.offers.map((o, i) => `<a href="${base}/offer/${i}">${o.supplier}</a>`).join('')}</nav>`));
      const offer = data.offers[Number(/^\/offer\/(\d+)$/.exec(path)?.[1])];
      if (offer) return sendHTML(res, page(offer.supplier, `<article id="offer">Item price: ${money(offer.price)}<br>Delivery fee: ${money(offer.shipping)}<br>Delivery time: ${offer.days} days</article><a href="${base}/">Supplier directory</a>`));
    }
    if (['registration', 'iframe', 'shadow'].includes(prefix)) {
      if (isGet && path === '/') {
        if (prefix === 'iframe') {
          const partner = new URL(origin); partner.hostname = 'localhost';
          return sendHTML(res, page('Partner support portal', `<p>Complete the partner form below. Its confirmation will appear here.</p><div id="receipt" role="status">No ticket submitted.</div><iframe title="Partner support form" src="${partner.origin}${base}/widget"></iframe>`, `addEventListener('message',e=>{if(e.origin===${scriptJSON(partner.origin)}&&e.data?.kind==='receipt')document.querySelector('#receipt').textContent='Ticket confirmed '+e.data.id})`));
        }
        if (prefix === 'shadow') return sendHTML(res, page('Project request portal', '<p>The project component contains the request form.</p><project-form></project-form>', `document.querySelector('project-form').attachShadow({mode:'open'}).innerHTML=${scriptJSON(`<style>label{display:block;margin:10px}input,select,button{font:17px system-ui;padding:7px}</style>${form('submit', 'Send project request')}`)}`));
        return sendHTML(res, page('Workshop registration', form('submit', 'Register once')));
      }
      if (prefix === 'iframe' && isGet && path === '/widget') return sendHTML(res, page('Partner support form', form('submit', 'Send support request')));
      if (req.method === 'POST' && path === '/submit') {
        const input = await body(req); const quantity = Number(input.quantity);
        if (typeof input.name !== 'string' || !input.name.trim() || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(input.email ?? '') || !['morning', 'afternoon'].includes(input.choice) || !Number.isInteger(quantity) || quantity < 1 || quantity > 5) return sendJSON(res, { error: 'Invalid registration fields' }, 400);
        const record = { name: input.name, email: input.email, choice: input.choice, quantity, id: `${prefix.toUpperCase()}-${n}-${state.records.length + 1}` }; state.records.push(record);
        return sendHTML(res, page('Submission received', `<p id="receipt" role="status">Receipt: ${record.id}</p><p>Name: ${escape(record.name)}. Session: ${record.choice}. Quantity: ${record.quantity}.</p>`, prefix === 'iframe' ? `parent.postMessage({kind:'receipt',id:${scriptJSON(record.id)}},${scriptJSON(origin)})` : ''));
      }
    }
    if (prefix === 'pages' && isGet && (path === '/' || path === '/ledger')) {
      const p = Math.max(1, Math.min(3, Number(url.searchParams.get('page')) || 1)); state.visits.add(`/ledger?page=${p}`);
      return sendHTML(res, page('Invoice ledger', `<p>Page ${p} of 3</p><table id="ledger"><tr><th>Invoice</th><th>Region</th><th>Status</th><th>Amount</th></tr>${data.rows.slice((p - 1) * 4, p * 4).map(r => `<tr><td>${r.id}</td><td>${r.region}</td><td>${r.status}</td><td>${r.amount}</td></tr>`).join('')}</table><nav>${[1, 2, 3].map(i => `<a href="${base}/ledger?page=${i}">Page ${i}</a>`).join('')}</nav>`));
    }
    if (prefix === 'spa') {
      if (isGet && path === '/') return sendHTML(res, page('Project task board', '<div id="board"></div><p id="status" role="status">Loading board</p>', `${apiScript()};async function render(){const rows=await(await fetch(endpoint+'/tasks')).json();document.querySelector('#board').innerHTML=rows.map(t=>'<article>'+t.id+' — '+t.title+' — '+(t.done?'completed':'pending')+(t.done?'':' <button data-id="'+t.id+'">Complete '+t.id+'</button>')+'</article>').join('');document.querySelector('#status').textContent='Pending: '+rows.filter(t=>!t.done).length}document.querySelector('#board').onclick=async e=>{const id=e.target.dataset.id;if(id){await send('/complete',{id});await render()}};render()`));
      if (isGet && path === '/tasks') return sendJSON(res, state.tasks);
      if (req.method === 'POST' && path === '/complete') { const input = await body(req), row = state.tasks.find(t => t.id === input.id); if (!row) return sendJSON(res, { error: 'Unknown task' }, 400); row.done = true; return sendJSON(res, { ok: true }); }
    }
    if (prefix === 'tabs') {
      if (isGet && path === '/') return sendHTML(res, page('Purchase references', `<p>Keep the quote and delivery reference open in separate tabs.</p><nav><a id="quote" target="_blank" href="${base}/quote">Open quote reference</a><a id="delivery" target="_blank" href="${base}/delivery">Open delivery reference</a></nav>`));
      if (isGet && ['/quote', '/delivery'].includes(path)) {
        const kind = path.slice(1), content = kind === 'quote' ? `Quote ${data.quote}. Unit price: ${money(data.price)}.` : `Flat delivery fee per order: ${money(data.shipping)}.`;
        return sendHTML(res, page(kind === 'quote' ? 'Quote reference' : 'Delivery reference', `<article id="reference">${content}</article>`, `if(!window.name)window.name=crypto.randomUUID();fetch(${scriptJSON(base + '/visit')},{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({kind:${scriptJSON(kind)},tab:window.name})})`));
      }
      if (req.method === 'POST' && path === '/visit') { const input = await body(req); if (!['quote', 'delivery'].includes(input.kind) || typeof input.tab !== 'string' || input.tab.length > 100) return sendJSON(res, { error: 'Invalid visit' }, 400); if (!state.tabVisits.has(input.kind)) state.tabVisits.set(input.kind, new Set()); state.tabVisits.get(input.kind).add(input.tab); return sendJSON(res, { ok: true }); }
    }
    if (prefix === 'delayed') {
      if (isGet && path === '/') return sendHTML(res, page('Warehouse inventory', '<button id="load">Load inventory</button><label>Reservation quantity<input type="number" id="quantity" min="1" value="1"></label><div id="inventory"></div><p id="status" role="status">Inventory has not been loaded.</p>', `${apiScript()};document.querySelector('#load').onclick=async()=>{document.querySelector('#status').textContent='Loading inventory…';const rows=await(await fetch(endpoint+'/inventory')).json();document.querySelector('#inventory').innerHTML='<table><tr><th>SKU</th><th>Warehouse</th><th>Available</th><th>Reserve</th></tr>'+rows.map(r=>'<tr><td>'+r.sku+'</td><td>'+r.region+'</td><td>'+r.stock+'</td><td><button data-sku="'+r.sku+'">Reserve '+r.sku+'</button></td></tr>').join('')+'</table>';document.querySelector('#status').textContent='Inventory ready'};document.querySelector('#inventory').onclick=async e=>{if(e.target.dataset.sku){const r=await send('/reserve',{sku:e.target.dataset.sku,quantity:Number(document.querySelector('#quantity').value)});document.querySelector('#status').textContent='Reservation confirmed: '+r.id+'; '+r.sku+'; quantity '+r.quantity}}`));
      if (isGet && path === '/inventory') { await sleep(350); return sendJSON(res, data.stock); }
      if (req.method === 'POST' && path === '/reserve') { const input = await body(req), product = data.stock.find(p => p.sku === input.sku), quantity = Number(input.quantity); if (!product || !Number.isInteger(quantity) || quantity < 1 || quantity > product.stock) return sendJSON(res, { error: 'Insufficient stock or invalid quantity' }, 400); product.stock -= quantity; const record = { sku: product.sku, quantity, id: `RES-${n}-${state.records.length + 1}` }; state.records.push(record); return sendJSON(res, record); }
    }
    if (prefix === 'checkout') {
      if (isGet && path === '/') return sendHTML(res, page('Stationery store', `${data.products.map(p => `<article><h2>${p.name}</h2><p>${p.sku} — ${money(p.price)}</p><label>Quantity for ${p.sku}<input id="qty-${p.sku}" type="number" min="1" value="1"></label><button data-sku="${p.sku}">Add ${p.sku}</button></article>`).join('')}<p id="status" role="status">Cart ready.</p><a id="cart" href="${base}/cart">View cart</a>`, `${apiScript()};document.querySelectorAll('button[data-sku]').forEach(b=>b.onclick=async()=>{const sku=b.dataset.sku;await send('/add',{sku,quantity:Number(document.getElementById('qty-'+sku).value)});document.querySelector('#status').textContent='Added '+sku})`));
      if (req.method === 'POST' && path === '/add') { const input = await body(req), quantity = Number(input.quantity); if (!data.products.some(p => p.sku === input.sku) || !Number.isInteger(quantity) || quantity < 1 || quantity > 10) return sendJSON(res, { error: 'Invalid cart item' }, 400); state.cart[input.sku] = (state.cart[input.sku] ?? 0) + quantity; return sendJSON(res, { ok: true }); }
      if (isGet && path === '/cart') {
        const subtotal = data.products.reduce((sum, p) => sum + p.price * (state.cart[p.sku] ?? 0), 0), total = state.coupon === data.coupon ? Math.floor(subtotal * 0.9) : subtotal;
        return sendHTML(res, page('Your cart', `<table>${data.products.filter(p => state.cart[p.sku]).map(p => `<tr><td>${p.sku}</td><td>${state.cart[p.sku]}</td><td>${p.price}</td></tr>`).join('')}</table><p id="total">Total: ${total}</p><p>Coupon: ${escape(state.coupon || 'none')}</p><form method="post" action="${base}/coupon"><label>Coupon<input id="coupon" name="coupon"></label><button id="apply">Apply coupon</button></form><form method="post" action="${base}/order"><button id="order">Place order once</button></form>`));
      }
      if (req.method === 'POST' && path === '/coupon') { const input = await body(req); if (input.coupon !== data.coupon) return sendJSON(res, { error: 'Unknown coupon' }, 400); state.coupon = input.coupon; res.writeHead(303, { Location: base + '/cart' }); return res.end(); }
      if (req.method === 'POST' && path === '/order') { if (!Object.keys(state.cart).length) return sendJSON(res, { error: 'Cart empty' }, 400); const subtotal = data.products.reduce((sum, p) => sum + p.price * (state.cart[p.sku] ?? 0), 0); const record = { id: `ORDER-${n}-${state.records.length + 1}`, items: { ...state.cart }, coupon: state.coupon, total: state.coupon === data.coupon ? Math.floor(subtotal * 0.9) : subtotal }; state.records.push(record); return sendHTML(res, page('Order received', `<p id="receipt" role="status">Order: ${record.id}. Total: ${record.total}.</p>`)); }
    }
    res.writeHead(404); res.end('Not found');
  }

  async function judge({ answer } = {}) {
    const duplicates = Math.max(0, state.records.length - 1);
    if (duplicates) return { success: false, reason: 'duplicate_submission', duplicate_submissions: duplicates };
    if (!ready()) return { success: false, reason: 'business_state_or_required_observation_incomplete', duplicate_submissions: duplicates };
    let facts = expected;
    if (['registration', 'iframe', 'shadow'].includes(prefix)) facts = { [prefix === 'iframe' ? 'ticket_id' : prefix === 'shadow' ? 'request_id' : 'registration_id']: state.records[0].id };
    if (prefix === 'delayed') facts = { sku: data.winner.sku, quantity: data.quantity, reservation_id: state.records[0].id };
    if (prefix === 'checkout') facts = { order_id: state.records[0].id, total: data.total };
    return { success: Boolean(sameFields(answerObject(answer), facts)), reason: sameFields(answerObject(answer), facts) ? 'ok' : 'answer_mismatch', duplicate_submissions: duplicates };
  }

  async function solve(execute) {
    async function run(args) { const reply = await execute(args); if (!reply?.ok) throw new Error(`${task.id}: ${args.action} failed: ${reply?.error ?? JSON.stringify(reply)}`); return reply; }
    const nav = url => run({ action: 'navigate', url });
    const click = target => run({ action: 'click', target });
    const type = (target, text) => run({ action: 'type', target, text: String(text) });
    const wait = text => run({ action: 'wait', text });
    async function extract(target, mustInclude) { const reply = await run({ action: 'extract', target }); if (mustInclude && !reply.text.includes(mustInclude)) throw new Error(`${task.id}: missing visible fact ${mustInclude}`); return reply.text; }
    async function reference(name, role) { const reply = await run({ action: 'find', name, role }); const line = reply.text.split('\n').find(l => l.startsWith('[e') && l.includes(JSON.stringify(name))); if (!line) throw new Error(`${task.id}: missing ${role} ${name}: ${reply.text}`); return /^\[(e\d+)\]/.exec(line)[1]; }
    await nav(startURL);
    let answer = expected;
    if (prefix === 'catalog') {
      await type('#q', data.query); await run({ action: 'select', target: '#category', text: data.category }); await type('#max', data.limit); await run({ action: 'select', target: '#stock', text: 'yes' }); await click('#filter'); await extract('#results', data.winner.sku);
    } else if (prefix === 'compare') {
      for (let i = 0; i < data.offers.length; i++) { await nav(origin + base + `/offer/${i}`); await extract('#offer', String(data.offers[i].price)); }
    } else if (['registration', 'iframe', 'shadow'].includes(prefix)) {
      if (prefix === 'iframe') await wait('Partner support portal');
      const target = async (css, name, role) => prefix === 'registration' ? css : reference(name, role);
      await type(await target('#name', 'Name', 'textbox'), person.name);
      await type(await target('#email', 'Email', 'textbox'), person.email);
      await run({ action: 'select', target: await target('#choice', 'Session', 'combobox'), text: data.choice });
      await type(await target('#quantity', 'Quantity', 'textbox'), data.quantity);
      await click(await target('#submit', prefix === 'iframe' ? 'Send support request' : 'Send project request', 'button'));
      await wait(prefix === 'iframe' ? 'Ticket confirmed' : 'Receipt:');
      const receipt = await extract('#receipt');
      const id = new RegExp(`${prefix.toUpperCase()}-\\d+-\\d+`).exec(receipt)?.[0];
      if (!id) throw new Error(`${task.id}: receipt not visible`);
      answer = { [prefix === 'iframe' ? 'ticket_id' : prefix === 'shadow' ? 'request_id' : 'registration_id']: id };
    } else if (prefix === 'pages') {
      for (let i = 1; i <= 3; i++) { await nav(origin + base + `/ledger?page=${i}`); await extract('#ledger', data.rows[(i - 1) * 4].id); }
    } else if (prefix === 'spa') {
      await wait('Pending: 4'); await click(`button[data-id="${data.target}"]`); await wait('Pending: 3'); await extract('#board', `${data.target}`);
    } else if (prefix === 'tabs') {
      const home = (await run({ action: 'tabs' })).active_tab;
      async function openReference(kind, fact) {
        await click(`#${kind}`);
        // A popup event may arrive after click's observation. Inspect tabs until
        // it exists; never repeat the click to compensate for observation lag.
        let tab;
        for (let i = 0; i < 30 && !tab; i++) { const observed = await run({ action: 'tabs' }); tab = JSON.parse(observed.text).find(t => t.url === origin + base + `/${kind}`); if (!tab) await sleep(25); }
        if (!tab) throw new Error(`${task.id}: reference tab did not open`);
        await run({ action: 'tab', target: tab.id }); await extract('#reference', fact);
      }
      await openReference('quote', data.quote); await run({ action: 'tab', target: home }); await openReference('delivery', String(data.shipping));
    } else if (prefix === 'delayed') {
      await click('#load'); await wait('Inventory ready'); await extract('#inventory', data.winner.sku); await type('#quantity', data.quantity); await click(`button[data-sku="${data.winner.sku}"]`); await wait('Reservation confirmed:'); const receipt = await extract('#status'); answer = { sku: data.winner.sku, quantity: data.quantity, reservation_id: /RES-\d+-\d+/.exec(receipt)?.[0] };
    } else {
      for (const [sku, quantity] of Object.entries(data.items)) { await type(`#qty-${sku}`, quantity); await click(`button[data-sku="${sku}"]`); await wait(`Added ${sku}`); }
      await click('#cart'); await type('#coupon', data.coupon); await click('#apply'); await extract('#total', String(data.total)); await click('#order'); await wait('Order:'); const receipt = await extract('#receipt'); answer = { order_id: /ORDER-\d+-\d+/.exec(receipt)?.[0], total: data.total };
    }
    return { answer: JSON.stringify(answer) };
  }

  return { handle, public: { id: task.id, scenarioId, prompt: `${goal}\nStart at ${startURL}\nUse only this local fixture site. All identities and purchases are test data.`, startURL, judge, solve } };
}

export async function createFixtureServer({ seed = 1 } = {}) {
  if (!Number.isSafeInteger(seed)) throw new Error('seed must be a safe integer');
  const cases = new Map(); let origin;
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://fixture.local'), match = /^\/case\/([a-f0-9-]+)(\/.*)?$/.exec(url.pathname);
      const current = match && cases.get(match[1]);
      if (!current) { res.writeHead(404); res.end('Not found'); return; }
      await current.handle(req, res, match[2] || '/', url);
    } catch { if (!res.headersSent) res.writeHead(400); res.end('Invalid request'); }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  origin = `http://127.0.0.1:${server.address().port}`;
  return {
    origin,
    createCase(taskId, repeat = 0) {
      const task = TASKS.find(t => t.id === taskId);
      if (!task) throw new Error(`Unknown fixture task: ${taskId}`);
      if (!Number.isSafeInteger(repeat) || repeat < 0) throw new Error('repeat must be a non-negative safe integer');
      const current = scenario(task, seed, repeat, origin); cases.set(current.public.scenarioId, current); return current.public;
    },
    async close() { cases.clear(); server.closeAllConnections(); await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve())); },
  };
}
