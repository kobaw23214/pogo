import http from 'node:http';

export const TASK_IDS = ['cross-detail-mass', 'record-price-difference', 'document-duration', 'paged-tied-minimum'];
const escape = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const cents = n => `${Math.floor(n / 100)}.${String(n % 100).padStart(2, '0')}`;
const decimal = n => cents(n).replace(/0+$/, '').replace(/\.$/, '');
function rng(seed) { let x = seed >>> 0; return n => { x = (Math.imul(x, 1664525) + 1013904223) >>> 0; return x % n; }; }
const words = ['霁禾', '砚溪', '潮栗', '石棠', '舟白', '榆青', '岚砾', '竹眠'];
function table(headers, rows, layout) {
  const order = headers.map((_, i) => i); if (layout % 2) order.reverse();
  return `<table><thead><tr>${order.map(i => `<th>${escape(headers[i])}</th>`).join('')}</tr></thead><tbody>${rows.map(row => `<tr>${order.map(i => `<td>${escape(row[i])}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
}
function definitions(pairs, layout) {
  const fields = layout % 2 ? [...pairs].reverse() : pairs;
  return `<dl>${fields.map(([label, value]) => `<dt>${escape(label)}</dt><dd>${escape(value)}</dd>`).join('')}</dl>`;
}
function document(title, content, layout) {
  return `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>${escape(title)}</title><style>body{font:17px/1.6 system-ui;max-width:850px;margin:40px auto;padding:20px}table{border-collapse:collapse;width:100%}td,th{padding:12px;border:1px solid #ccd8d2;text-align:left}dt{font-weight:600}dd{margin-bottom:15px}a,button{margin:8px}</style><body><header>${layout % 2 ? '竹简信息所' : '潮汐资料屋'} · 受控本机演示</header><h1>${escape(title)}</h1>${content}<footer>受控合成资料；页面只提供当前展示的事实。</footer></body></html>`;
}

/** All oracle/reference data stays behind these Node functions; HTTP exposes only the business UI. */
export async function createEvidenceFixtures({ seed = 811, port = 0 } = {}) {
  if (!Number.isSafeInteger(seed) || seed < 0 || seed > 0xffffffff) throw new Error('seed must be uint32');
  const random = rng(seed), suffix = 100 + random(800), layout = random(4), cases = new Map();
  const shuffled = [...words]; for (let i = shuffled.length - 1; i > 0; i--) { const j = random(i + 1); [shuffled[i], shuffled[j]] = [shuffled[j], shuffled[i]]; }
  const names = shuffled.slice(0, 6).map((w, i) => `${w}${suffix + i}`);
  const heavy = 1600 + random(8) * 100, light = 900 + random(5) * 100;
  const costA = 12000 + random(800), costB = costA + 251 + random(700);
  const low = 4100 + random(500), archive = 2 + random(3);
  const records = [
    { id: `凭-${suffix}`, name: `${names[0]}检测件`, price: `CNY ${cents(costA)}` },
    { id: `凭-${suffix + 1}`, name: `${names[1]}定位件`, price: `CNY ${cents(costB)}` },
    { id: `凭-${suffix + 2}`, name: `${names[2]}校准件`, price: `CNY ${cents(costB + 919)}` },
  ];
  const lots = names.slice(0, 6).map((name, i) => ({ id: `批-${suffix + 20 + i}`, name: `${name}配料包`, cost: i === 1 || i === 4 ? low : low + 317 + i * 109 }));
  const pageOrder = [0, 1, 2, 3, 4, 5]; if (layout % 2) pageOrder.reverse();
  const first = pageOrder.slice(0, 3).map(i => lots[i]), second = pageOrder.slice(3).map(i => lots[i]);
  const add = (id, title, goal, pages, control) => cases.set(id, { id, title, goal, pages, control, audit: [], layout_variant: layout });
  add('cross-detail-mass', '跨详情净重核对', `比较“${names[0]}器材包”和“${names[1]}器材包”的净重，指出较轻者，给出两者净重和相差多少克，并引用各自详情。`, {
    '/': document('器材详情入口', `<p>两款器材的规格分别在详情中。</p><a href="details/first">${escape(names[0])}器材包详情</a><a href="details/second">${escape(names[1])}器材包详情</a>`, layout),
    '/details/first': document(`${names[0]}器材包`, definitions([['器材编号', `器-${suffix}`], ['名称', `${names[0]}器材包`], ['净重', `${heavy} g`]], layout), layout),
    '/details/second': document(`${names[1]}器材包`, definitions([['器材编号', `器-${suffix + 1}`], ['名称', `${names[1]}器材包`], ['净重', `${light / 1000} kg`]], layout + 1), layout),
  }, { pages: ['/details/first', '/details/second'], raws: [`${heavy} g`, `${light / 1000} kg`], operations: ['compare', 'difference'], absolute_difference: String(heavy - light), unit: 'g', answer: `${names[1]}器材包较轻。两者净重为${heavy} g和${light / 1000} kg，相差${heavy - light}克。` });
  add('record-price-difference', '同表记录票额核对', `核对表中“${records[0].id} ${records[0].name}”与“${records[1].id} ${records[1].name}”的票额：报告两条记录各自的编号、名称和票额，以及后者比前者多多少 CNY。保持记录字段对应，并引用该表。`, {
    '/': document('检测件凭条', table(['编号', '名称', '票额'], records.map(r => [r.id, r.name, r.price]), layout), layout),
  }, { pages: ['/'], raws: [records[1].price, records[0].price], operations: ['difference'], absolute_difference: decimal(costB - costA), unit: 'CNY', answer: `${records[0].id}为${records[0].name}，${records[0].price}；${records[1].id}为${records[1].name}，${records[1].price}。后者多CNY ${decimal(costB - costA)}。` });
  add('document-duration', '留存周期与单位边界', `说明“${names[3]}业务日志”“${names[4]}服务副本”和“${names[5]}封存归档”的留存周期。比较前两者哪个更短、相差多少天；归档按文档中的年原样说明，不将年换成固定天数。引用文档。`, {
    '/': document('留存周期说明', `<h2>${escape(names[3])}业务日志</h2><p>留存24天。</p><h2>${escape(names[4])}服务副本</h2><p>留存12天。</p><h2>${escape(names[5])}封存归档</h2><p>留存${archive}年。</p>`, layout),
  }, { pages: ['/'], raws: ['24天', '12天'], operations: ['compare', 'difference'], absolute_difference: '12', unit: '天', incompatible_raw: `${archive}年`, answer: `${names[3]}业务日志24天，${names[4]}服务副本12天，副本更短12天；${names[5]}封存归档${archive}年，不换算固定天数。` });
  const lotTable = rows => table(['批次编号', '名称', '标价'], rows.map(r => [r.id, r.name, `CNY ${cents(r.cost)}`]), layout);
  add('paged-tied-minimum', '分批展示范围与并列最低', `收集当前目录中全部配料包的标价，报告完成的观察范围、所有并列最低价的批次编号与名称、最低价和依据。只依据页面确实展示的分页信息，不虚构页码。`, {
    '/': document('配料目录', `<p id="range">已展示3项，共6项</p><section id="items">${lotTable(first)}</section><button id="continue">查看剩余配料</button><script>document.querySelector('#continue').onclick=async()=>{const r=await fetch('remaining');document.querySelector('#items').insertAdjacentHTML('beforeend',await r.text());document.querySelector('#range').textContent='已展示6项，共6项；全部结果已展示';document.querySelector('#continue').remove()}</script>`, layout),
    '/remaining': lotTable(second),
  }, { pages: ['/', '/remaining'], raws: lots.map(r => `CNY ${cents(r.cost)}`), operations: ['sort'], minimum: decimal(low), tie_count: 2, unit: 'CNY', answer: `已观察页面展示的全部6项；并列最低为${lots[1].id} ${lots[1].name}与${lots[4].id} ${lots[4].name}，均CNY ${cents(low)}。页面没有明确页码。` });
  let origin;
  const server = http.createServer((request, response) => {
    const url = new URL(request.url, origin ?? 'http://127.0.0.1');
    const match = /^\/cases\/([a-z-]+)(\/.*)$/.exec(url.pathname), c = match && cases.get(match[1]);
    const content = c?.pages[match?.[2]];
    if (request.method !== 'GET' || content === undefined) { response.writeHead(404); response.end('Not found'); return; }
    c.audit.push({ method: request.method, path: match[2], query: Object.fromEntries(url.searchParams), at: Date.now(),
      user_agent: request.headers['user-agent'] ?? '', fetch_dest: request.headers['sec-fetch-dest'] ?? '', fetch_mode: request.headers['sec-fetch-mode'] ?? '',
      browser: /(?:HeadlessChrome|Chrome)\//.test(request.headers['user-agent'] ?? '') && Boolean(request.headers['sec-fetch-mode']) });
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }); response.end(content);
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
  origin = `http://127.0.0.1:${server.address().port}`;
  const tasks = [...cases.values()].map(c => ({ id: c.id, title: c.title, goal: c.goal, seed, layout_variant: c.layout_variant,
    startURL: `${origin}/cases/${c.id}/`, allowedOrigins: [origin], allowedPathPrefixes: [`/cases/${c.id}/`],
    prompt: `起始网址：${origin}/cases/${c.id}/\n业务目标：${c.goal}` }));
  return { tasks, origin, reset(id) { cases.get(id).audit = []; }, inspect(id) { return { case_id: id, seed, layout_variant: layout, requests: structuredClone(cases.get(id).audit) }; },
    referenceData(id) { return structuredClone(cases.get(id).control); },
    assessReads(id) { const c = cases.get(id), missing = c.control.pages.filter(p => !c.audit.some(r => r.browser && r.path === p)); return { verified: missing.length === 0, missing_paths: missing }; },
    async close() { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); } };
}
