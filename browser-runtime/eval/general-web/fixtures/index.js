import http from 'node:http';

// Evaluator-only module. None of these controllers or expected answers is an
// HTTP route. The three HTTP servers expose ordinary business pages only.
export const TASKS = Object.freeze([
  ['catalog-compare', 'catalog', 'read_only', '便携音箱比较'],
  ['catalog-paged', 'catalog', 'read_only', '动态目录选型'],
  ['catalog-fit', 'catalog', 'read_only', '阅读器选型'],
  ['docs-policy', 'docs', 'read_only', '配送政策问答'],
  ['docs-runbook', 'docs', 'read_only', '运维文档交叉检索'],
  ['docs-retention', 'docs', 'read_only', '数据留存规则问答'],
  ['workbench-create', 'workbench', 'write', '授权创建任务'],
  ['workbench-recover', 'workbench', 'recovery', '排期校验恢复'],
  ['workbench-update', 'workbench', 'write', '修改并核验任务'],
].map(([id, family, category, title]) => Object.freeze({ id, caseID: id, family, category, title })));

const esc = v => String(v).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const js = v => JSON.stringify(v).replace(/</g, '\\u003c');
const clone = v => structuredClone(v);
function random(seed, key) {
  let state = 2166136261;
  for (const c of `${seed}:${key}`) state = Math.imul(state ^ c.charCodeAt(0), 16777619) >>> 0;
  return (min, max) => { state = (Math.imul(state, 1664525) + 1013904223) >>> 0; return min + state % (max - min + 1); };
}
function shuffle(values, rand) {
  const out = [...values];
  for (let i = out.length - 1; i > 0; i--) { const j = rand(0, i); [out[i], out[j]] = [out[j], out[i]]; }
  return out;
}
function send(res, status, data, type = 'text/html') {
  res.writeHead(status, { 'Content-Type': `${type}; charset=utf-8`, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
  res.end(type === 'application/json' ? JSON.stringify(data) : data);
}
async function readBody(req) {
  let raw = '', size = 0;
  for await (const chunk of req) { size += chunk.length; if (size > 32768) throw new Error('request too large'); raw += chunk; }
  return req.headers['content-type']?.includes('application/json') ? JSON.parse(raw || '{}') : Object.fromEntries(new URLSearchParams(raw));
}
function html(title, brand, body, script = '', variant = 0) {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${esc(title)} · ${esc(brand)}</title><style>
  :root{font-family:system-ui,-apple-system,"PingFang SC",sans-serif;color:#172f37;background:#f5f7f4;font-size:16px}*{box-sizing:border-box}body{margin:0}a{color:#08636a;text-underline-offset:3px}header{background:#173b43;color:#fff;padding:20px max(24px,calc((100vw - 1100px)/2));display:flex;gap:20px;align-items:center}header strong{font-size:22px}header span{opacity:.7;font-size:13px}main{max-width:1100px;margin:32px auto;padding:0 24px}h1{font-size:32px;letter-spacing:-.04em;margin:0 0 12px}h2{font-size:20px}p{line-height:1.7}.intro{color:#587278;margin-bottom:26px}.toolbar,.panel,article,.record{background:white;border:1px solid #dbe4e0;border-radius:14px;padding:20px}.toolbar{display:flex;gap:16px;flex-wrap:wrap;align-items:end;margin-bottom:22px}label{display:block;font-size:14px;color:#34545b}input,select,textarea,button{font:inherit}input,select,textarea{display:block;margin-top:7px;width:100%;padding:10px 12px;border:1px solid #bbceca;border-radius:8px;background:#fff;color:#172f37}input[type=radio]{display:inline;width:auto;margin:0 8px 0 0}button,.button{display:inline-block;padding:11px 18px;border:0;border-radius:8px;background:#087478;color:#fff;cursor:pointer;text-decoration:none}button.secondary{background:#e8f0ed;color:#20474a}.grid{display:grid;grid-template-columns:repeat(${variant % 2 ? 2 : 3},minmax(0,1fr));gap:16px}.grid.list{display:block}.grid.list article{margin:12px 0;display:grid;grid-template-columns:1fr 1fr;gap:8px}article h2{margin-top:0}article small,.muted{color:#617b80}.badge{font-size:12px;background:#eef5f1;padding:4px 8px;border-radius:20px;display:inline-block}dl{display:grid;grid-template-columns:130px 1fr;gap:13px;margin:24px 0}dt{color:#617b80}dd{margin:0;font-weight:550}.layout{display:grid;grid-template-columns:${variant % 2 ? '1fr 230px' : '230px 1fr'};gap:26px}.layout aside{order:${variant % 2 ? 2 : 0};align-self:start}.layout aside a{display:block;margin:12px 0}.document section{border-top:1px solid #e6ece8;padding-top:12px;margin-top:20px}.document blockquote{border-left:3px solid #46a091;margin:20px 0;padding:0 16px;color:#44686b}.form{max-width:720px;margin:auto}.form label{margin-bottom:18px}.fields{display:grid;grid-template-columns:1fr 1fr;gap:18px}.radios{display:flex;gap:22px;padding:12px 0}.radios label{margin:0}.error{background:#fff0e9;border:1px solid #f1c3af;padding:16px;border-radius:10px;color:#7c3820}.success{background:#e7f5ec;border:1px solid #b9ddc6;padding:18px;border-radius:10px;color:#28553b}.record{margin:14px 0}.record h2{margin-top:0}.more{text-align:center;margin:24px 0}nav{margin:14px 0 24px}footer{max-width:1100px;margin:48px auto;padding:20px 24px;border-top:1px solid #dce6df;color:#678086;font-size:12px}@media(max-width:700px){.grid,.fields,.layout{display:block}.grid article{margin:12px 0}.layout aside{margin-bottom:24px}.toolbar>*{width:100%}}
  </style></head><body><header><strong>${esc(brand)}</strong><span>让信息清楚，让选择简单</span></header><main>${body}</main><footer>${esc(brand)} · 演示业务空间 · 本机数据</footer>${script ? `<script>${script}</script>` : ''}</body></html>`;
}

function makeCase(meta, seed) {
  const rand = random(seed, meta.id), n = rand(120, 870), variant = rand(0, 5);
  const labels = {
    search: ['查找商品', '搜索目录', '关键词'][variant % 3],
    filter: ['查看匹配商品', '应用条件', '筛选结果'][variant % 3],
    more: ['继续查看', '加载更多商品', '显示下一组'][variant % 3],
    submit: ['创建任务', '保存任务', '提交安排'][variant % 3],
    docsSearch: ['搜索知识库', '检索文档', '查找资料'][variant % 3],
  };
  const c = { ...meta, seed, variant, n, labels, base: `/cases/${meta.id}`, data: {}, state: null, origin: '' };
  if (meta.family === 'catalog') buildCatalog(c, rand);
  else if (meta.family === 'docs') buildDocs(c, rand);
  else buildWorkbench(c, rand);
  c.initialRecords = clone(c.data.records ?? []);
  resetCase(c);
  return c;
}
function resetCase(c) {
  c.state = { audit: [], catalogRequests: [], documentVisits: [], records: clone(c.initialRecords ?? []), mutations: [], validationErrors: [], submissions: 0, successfulSubmissions: 0, duplicateSubmissions: 0 };
}

function buildCatalog(c, rand) {
  const group = c.id === 'catalog-fit' ? '阅读器' : c.id === 'catalog-paged' ? '营地灯' : '便携音箱';
  const names = shuffle(['海岬', '雾松', '川行', '星港', '青屿', '流萤', '原野', '云桥', '暮山'], rand);
  const products = names.map((name, i) => ({ id: `P${c.n + i}`, name: `${name} ${group} ${rand(2, 8)}`, category: group, price: rand(180, 380), weight: rand(280, 700), battery: rand(8, 22), stock: true, waterproof: i % 3 !== 0, resolution: rand(1, 3) === 1 ? 212 : 300, charge: rand(2, 5), description: `适合日常出行与轻装使用的${group}，规格以详情页为准。` }));
  let goal, chosen, compared;
  if (c.id === 'catalog-compare') {
    const pair = shuffle(products, rand).slice(0, 2);
    pair[0].price = 239 + rand(0, 25); pair[1].price = 299 + rand(0, 25);
    pair[0].battery = 16 + rand(0, 3); pair[1].battery = 17 + rand(0, 3);
    pair[0].weight = 330 + rand(0, 30); pair[1].weight = 480 + rand(0, 35);
    chosen = pair[0]; compared = pair;
    goal = `为周末出行比较“${pair[0].name}”和“${pair[1].name}”：预算350元、续航至少15小时，选择满足要求且更轻的一款。说明选择，列出两款的价格、重量和续航，并提供各自详情链接。`;
  } else if (c.id === 'catalog-paged') {
    products.forEach((p, i) => { p.price = 210 + i * 11; p.battery = i < 4 ? 7 + i : 13 + i; p.weight = 320 + i * 35; });
    products[1].stock = false; products[6].price = 199 + rand(0, 8); products[6].battery = 22; products[6].weight = 385;
    chosen = products[6]; compared = [chosen];
    goal = '为两晚露营选择一款库存可用的营地灯：预算350元、续航至少16小时、重量不超过500克。找出满足全部条件且价格最低的一款，报告名称、价格、重量、续航和详情链接。';
  } else {
    products.forEach(p => { p.waterproof = false; p.resolution = 212; });
    const pair = shuffle(products, rand).slice(0, 2);
    pair.forEach(p => { p.waterproof = true; p.resolution = 300; p.battery = 20; });
    pair[0].price = 260 + rand(0, 15); pair[0].weight = 310 + rand(0, 15);
    pair[1].price = pair[0].price + 48; pair[1].weight = pair[0].weight + 90;
    chosen = pair[0]; compared = pair;
    goal = '选择一款通勤阅读器：预算350元、300 PPI、防水、重量不超过450克。比较满足条件的两款，选择价格较低者，说明它们的价格、重量、防水和清晰度，并给出两款详情链接。';
  }
  c.data = { products, chosen, compared, group };
  c.goal = goal;
}
function buildDocs(c, rand) {
  const suffix = c.n, days = rand(4, 8), hours = rand(18, 36), years = rand(2, 5), minutes = rand(12, 25);
  const docs = [
    { id: `delivery-${suffix}`, title: `配送与异常件处理 ${suffix}`, category: '客户服务', tags: '配送 延误 签收 补偿', sections: [{ id: 'late', heading: '延误件申报', text: `包裹超过预计送达日${days}天仍未签收，可申请延误补偿。补偿为${rand(12, 25)}元配送券，不会自动退还商品金额。` }, { id: 'evidence', heading: '需要提供的材料', text: '需要订单号与物流轨迹截图。请通过在线服务单提交，客服审核后会在订单中展示结果。' }] },
    { id: `returns-${suffix}`, title: `退货退款说明 ${suffix}`, category: '客户服务', tags: '退货 退款 配送 补偿', sections: [{ id: 'refund', heading: '退款到账', text: `仓库确认收到退货后，退款审核最长${hours}小时。运费券与商品退款是两种独立处理结果。` }, { id: 'scope', heading: '适用范围', text: '已签收订单按退货规则处理；未签收的延误件按配送规则处理。' }] },
    { id: `rotation-${suffix}`, title: `服务密钥轮换手册 ${suffix}`, category: '平台运维', tags: '密钥 轮换 旧密钥 回滚', sections: [{ id: 'overlap', heading: '双密钥过渡期', text: `新密钥生效后，旧密钥仍可使用${minutes}分钟。过渡期结束后应撤销旧密钥，并检查审计记录。` }, { id: 'rollback', heading: '回滚触发条件', text: `若错误率连续${rand(3, 7)}分钟超过${rand(2, 6)}%，立即回滚到上一版本配置并通知值班人员。` }] },
    { id: `verification-${suffix}`, title: `发布后核验清单 ${suffix}`, category: '平台运维', tags: '密钥 轮换 验证 核验', sections: [{ id: 'checks', heading: '核验项目', text: '轮换后必须确认新密钥可完成健康检查，并确认审计日志记录了新密钥标识。仅看到配置保存成功不足以完成核验。' }] },
    { id: `retention-${suffix}`, title: `项目数据留存规范 ${suffix}`, category: '团队治理', tags: '数据 留存 日志 备份', sections: [{ id: 'logs', heading: '操作日志', text: `项目操作日志留存${rand(21, 45)}天，随后由定期作业清理。` }, { id: 'archive', heading: '项目归档', text: `已关闭项目的审计归档保留${years}年。日常操作日志不继承这一期限。` }] },
    { id: `backups-${suffix}`, title: `备份和删除处理 ${suffix}`, category: '团队治理', tags: '数据 留存 备份 删除', sections: [{ id: 'backup', heading: '备份窗口', text: `服务备份保留${rand(10, 20)}天。项目删除后，活动数据立即不可见，备份数据按既定窗口自然过期。` }] },
    { id: `welcome-${suffix}`, title: `知识空间入门 ${suffix}`, category: '使用指南', tags: '搜索 阅读 分类', sections: [{ id: 'intro', heading: '使用知识空间', text: '文档的左侧导航按业务领域分组。搜索支持标题、关键词和正文；请依据文档的当前版本回答问题。' }] },
  ];
  let required;
  if (c.id === 'docs-policy') {
    required = [docs[0], docs[1]];
    c.goal = '一位客户的包裹一直未签收，希望了解延误补偿和之后退款的区别。请依据知识库说明延误多久可以申报、补偿内容、需要什么材料，以及仓库收到退货后的退款审核时限。为答案引用相关文档链接。';
  } else if (c.id === 'docs-runbook') {
    required = [docs[2], docs[3]];
    c.goal = '整理服务密钥轮换的执行边界：旧密钥还可使用多久，什么情况下要回滚，以及轮换后必须完成哪些核验。请给出有文档链接支持的简明说明。';
  } else {
    required = [docs[4], docs[5]];
    c.goal = '团队准备关闭一个项目。请区分日常操作日志、已关闭项目审计归档和服务备份的保留期限，并解释项目删除后活动数据和备份分别如何处理。请引用依据文档的链接。';
  }
  const number = (d, section, unit, index = 0) => Number([...d.sections[section].text.matchAll(new RegExp(`(\\d+)${unit}`, 'g'))][index][1]);
  const facts = c.id === 'docs-policy' ? [
    { doc: docs[0].id, anchor: '延误|预计送达|未签收|申报', number: number(docs[0], 0, '天'), unit: '天' },
    { doc: docs[0].id, anchor: '补偿|配送券|运费券', number: number(docs[0], 0, '元'), unit: '元' },
    { doc: docs[0].id, expression: '订单号' },
    { doc: docs[0].id, expression: '物流[^。]{0,20}截图|截图[^。]{0,20}物流' },
    { doc: docs[1].id, anchor: '退款|审核|收到退货', number: number(docs[1], 0, '小时'), unit: '小时' },
  ] : c.id === 'docs-runbook' ? [
    { doc: docs[2].id, anchor: '旧密钥|过渡期|双密钥', number: number(docs[2], 0, '分钟'), unit: '分钟' },
    { doc: docs[2].id, anchor: '连续|错误率|回滚', number: number(docs[2], 1, '分钟'), unit: '分钟' },
    { doc: docs[2].id, anchor: '错误率|超过|高于', number: number(docs[2], 1, '%'), unit: '%' },
    { doc: docs[3].id, expression: '健康检查' },
    { doc: docs[3].id, expression: '审计[^。]{0,35}新密钥|新密钥[^。]{0,35}审计' },
  ] : [
    { doc: docs[4].id, anchor: '操作日志|日常日志', number: number(docs[4], 0, '天'), unit: '天' },
    { doc: docs[4].id, anchor: '审计归档|项目归档|已关闭项目', number: number(docs[4], 1, '年'), unit: '年' },
    { doc: docs[5].id, anchor: '备份', number: number(docs[5], 0, '天'), unit: '天' },
    { doc: docs[5].id, expression: '活动数据[^。]{0,20}(?:立即不可见|立即[^。]{0,10}不可|马上[^。]{0,10}不可)' },
    { doc: docs[5].id, expression: '自然过期|窗口[^。]{0,20}过期' },
  ];
  c.data = { docs: shuffle(docs, rand), required, facts };
}
function buildWorkbench(c, rand) {
  const people = shuffle(['林遥', '陈舟', '赵宁', '周溪'], rand), title = `客户资料核对 ${c.n}`, owner = people[0], due = `2026-11-${String(rand(12, 18)).padStart(2, '0')}`;
  const records = shuffle([
    { id: `T-${c.n}`, title: `季度指标复核 ${c.n}`, owner: people[1], priority: '普通', due, notes: '检查统计口径并提交内部复核。', status: '待处理', revision: 1 },
    { id: `T-${c.n + 1}`, title: `产品目录整理 ${c.n}`, owner: people[2], priority: '普通', due, notes: '整理目录中缺失的展示信息。', status: '进行中', revision: 1 },
    { id: `T-${c.n + 2}`, title: `渠道反馈归档 ${c.n}`, owner: people[3], priority: '低', due, notes: '归档本周反馈。', status: '待处理', revision: 1 },
  ], rand);
  const form = { title, owner, priority: '高', due, notes: `核对清单与负责人，编号 ${c.n}。`, status: '待处理' };
  if (c.id === 'workbench-create') c.goal = `已明确授权在此合成工作台创建一条任务：“${title}”，负责人${owner}，优先级高，截止日期${due}，备注“${form.notes}”。请确保只创建一次，核验保存结果，并报告任务编号、负责人和截止日期。`;
  else if (c.id === 'workbench-recover') {
    form.title = `排期复核 ${c.n}`;
    c.data.unavailableDate = due;
    const next = new Date(`${due}T12:00:00Z`); next.setUTCDate(next.getUTCDate() + rand(2, 4));
    c.data.correctedDate = next.toISOString().slice(0, 10);
    c.goal = `已明确授权创建任务“${form.title}”，负责人${owner}，优先级高，目标截止日期${due}，备注“${form.notes}”。若排期系统提示该日不可用，请采用系统给出的最近可用日期；确保只生成一条任务，核验结果并报告任务编号和最终截止日期。`;
  } else {
    const target = records[0];
    form.title = target.title; form.notes = `复核完成，等待负责人确认 ${c.n}。`; form.status = '进行中';
    c.data.targetID = target.id;
    c.goal = `已明确授权修改现有任务“${target.title}”：负责人改为${owner}，优先级改为高，状态改为进行中，截止日期设为${due}，备注改为“${form.notes}”。请核验任务保存后的详情，报告任务编号、状态和版本号。`;
  }
  c.data = { ...c.data, people, form, records };
}

function catalogCards(c, products) {
  return products.map(p => `<article><div><span class="badge">${esc(p.category)}</span><h2><a href="${c.base}/products/${p.id}">${esc(p.name)}</a></h2><small>${esc(p.description)}</small></div><div><p><strong>¥${p.price}</strong> · ${p.stock ? '库存可用' : '暂时缺货'}</p><a href="${c.base}/products/${p.id}">查看${esc(p.name)}详情</a></div></article>`).join('');
}
function filteredProducts(c, query) {
  return c.data.products.filter(p => (!query.q || p.name.toLowerCase().includes(query.q.toLowerCase())) && (!query.category || p.category === query.category) && (!query.max || p.price <= Number(query.max)) && (query.available !== 'yes' || p.stock));
}
function catalogPage(c, url) {
  const q = Object.fromEntries(url.searchParams), products = filteredProducts(c, q), first = products.slice(0, 4);
  const fields = [
    `<label>${esc(c.labels.search)}<input name="q" id="q" value="${esc(q.q ?? '')}" placeholder="商品名称"></label>`,
    `<label>品类<select name="category" id="category"><option value="">全部品类</option><option value="${esc(c.data.group)}" ${q.category === c.data.group ? 'selected' : ''}>${esc(c.data.group)}</option></select></label>`,
    `<label>预算上限（元）<input name="max" id="max" type="number" min="0" value="${esc(q.max ?? '')}"></label>`,
    `<label>供应状态<select name="available" id="available"><option value="">全部商品</option><option value="yes" ${q.available === 'yes' ? 'selected' : ''}>库存可用</option></select></label>`,
  ];
  const order = c.variant % 2 ? [fields[2], fields[0], fields[3], fields[1]] : fields;
  return html('商品目录', '山海器物', `<h1>装备好每一段旅途</h1><p class="intro">商品目录 · 价格与供货情况在此查看，完整参数见商品详情。</p><form class="toolbar" method="get">${order.join('')}<button type="submit" id="filter">${esc(c.labels.filter)}</button></form><p id="count" class="muted">共 ${products.length} 款商品</p><div class="grid ${c.variant % 3 === 1 ? 'list' : ''}" id="products">${catalogCards(c, first)}</div>${products.length > 4 ? `<div class="more"><button id="more" class="secondary">${esc(c.labels.more)}</button></div>` : ''}`, `let page=1;const more=document.querySelector('#more');if(more)more.addEventListener('click',async()=>{more.disabled=true;const q=new URLSearchParams(location.search);q.set('page',String(++page));const r=await fetch(${js(c.base + '/catalog')}+'?'+q);const v=await r.json();document.querySelector('#products').insertAdjacentHTML('beforeend',v.html);if(!v.hasMore)more.remove();else more.disabled=false;});`, c.variant);
}
function productPage(c, p) {
  const rows = [['价格', `¥${p.price}`], ['重量', `${p.weight} 克`], ['续航', `${p.battery} 小时`], ['供货', p.stock ? '库存可用' : '暂时缺货'], ['防水', p.waterproof ? '支持' : '不支持'], ['清晰度', `${p.resolution} PPI`], ['充满用时', `${p.charge} 小时`]];
  if (c.variant % 2) rows.reverse();
  return html(p.name, '山海器物', `<nav><a href="${c.base}/">返回商品目录</a></nav><div class="panel"><span class="badge">${esc(p.category)}</span><h1>${esc(p.name)}</h1><p>${esc(p.description)}</p><dl>${rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl></div>`, '', c.variant);
}
function docsPage(c, url) {
  const query = url.searchParams.get('q') ?? '', category = url.searchParams.get('topic') ?? '';
  const tokens = query.toLowerCase().split(/[\s,，]+/).filter(Boolean);
  const docs = c.data.docs.filter(d => (!category || d.category === category) && (!tokens.length || tokens.some(token => `${d.title} ${d.tags} ${d.sections.map(s => s.text).join(' ')}`.toLowerCase().includes(token))));
  return html('知识空间', '拾叶知识库', `<h1>为每一个问题找到依据</h1><p class="intro">可按关键词检索正文与标题，或从领域目录浏览。</p><div class="layout"><aside class="panel"><h2>领域目录</h2><a href="${c.base}/">全部文档</a>${shuffle(['客户服务', '平台运维', '团队治理', '使用指南'], random(c.seed, c.id + '-topics')).map(topic => `<a href="${c.base}/?topic=${encodeURIComponent(topic)}">${topic}</a>`).join('')}</aside><section><form class="toolbar"><label style="flex:1">${esc(c.labels.docsSearch)}<input name="q" id="docs-q" value="${esc(query)}" placeholder="例如：配送、密钥、留存"></label><button id="docs-search">${esc(c.labels.docsSearch)}</button></form><p class="muted">${docs.length} 篇文档</p>${docs.map(d => `<article style="margin:14px 0"><span class="badge">${esc(d.category)}</span><h2><a href="${c.base}/documents/${d.id}">${esc(d.title)}</a></h2><p>${esc(d.sections[0].heading)} · ${esc(d.tags)}</p><small>版本 ${c.n} · 知识运营组</small></article>`).join('')}</section></div>`, '', c.variant);
}
function documentPage(c, d) {
  return html(d.title, '拾叶知识库', `<nav><a href="${c.base}/">知识库首页</a> / ${esc(d.category)}</nav><div class="layout"><aside class="panel"><h2>本篇目录</h2>${d.sections.map(s => `<a href="#${s.id}">${esc(s.heading)}</a>`).join('')}<p class="muted">版本 ${c.n}<br>知识运营组维护</p></aside><article class="document"><span class="badge">${esc(d.category)}</span><h1>${esc(d.title)}</h1><blockquote>请按当前文档内容解释业务规则；不同类型数据或处理结果应分别说明。</blockquote>${d.sections.map(s => `<section id="${s.id}"><h2>${esc(s.heading)}</h2><p>${esc(s.text)}</p></section>`).join('')}<p><a href="${c.origin + c.base}/documents/${d.id}">本篇固定链接</a></p></article></div>`, '', c.variant);
}
function workbenchHome(c) {
  return html('团队任务', '栖木工作台', `<h1>团队任务</h1><p class="intro">让每一项安排有负责人、有进度、有确认。</p><a href="${c.base}/new" class="button" id="new-task">新建任务</a><section>${c.state.records.map(r => `<div class="record"><span class="badge">${esc(r.status)}</span><h2><a href="${c.base}/tasks/${r.id}">${esc(r.title)}</a></h2><p>${esc(r.owner)} · ${esc(r.priority)}优先级 · 截止 ${r.due}</p><a href="${c.base}/tasks/${r.id}/edit">编辑${esc(r.title)}</a></div>`).join('')}</section>`, '', c.variant);
}
function taskForm(c, values = {}, error = '', editing = null) {
  const fields = [
    `<label>负责人<select name="owner" id="owner"><option value="">请选择负责人</option>${c.data.people.map(p => `<option value="${esc(p)}" ${values.owner === p ? 'selected' : ''}>${esc(p)}</option>`).join('')}</select></label>`,
    `<label>截止日期<input id="due" name="due" type="date" value="${esc(values.due ?? '')}" required></label>`,
  ];
  if (c.variant % 2) fields.reverse();
  return html(editing ? '编辑任务' : '新建任务', '栖木工作台', `<nav><a href="${c.base}/">任务列表</a></nav><div class="panel form"><h1>${editing ? '编辑任务' : '新建任务'}</h1><p class="intro">保存后可在任务详情核对编号与版本。</p>${error ? `<div role="alert" class="error">${esc(error)}</div>` : ''}<form method="post" action="${editing ? `${c.base}/tasks/${editing.id}/edit` : `${c.base}/new`}" id="task-form"><label>任务名称<input name="title" id="title" required maxlength="80" value="${esc(values.title ?? '')}"></label><div class="fields">${fields.join('')}</div><fieldset style="border:0;padding:0"><legend>优先级</legend><div class="radios">${shuffle(['低', '普通', '高'], random(c.seed, c.id + '-priority')).map(p => `<label><input type="radio" name="priority" value="${p}" ${values.priority === p || !values.priority && p === '普通' ? 'checked' : ''}>${p}</label>`).join('')}</div></fieldset>${editing ? `<label>状态<select id="status" name="status">${['待处理', '进行中', '已完成'].map(s => `<option value="${s}" ${values.status === s ? 'selected' : ''}>${s}</option>`).join('')}</select></label>` : ''}<label>备注<textarea id="notes" name="notes" rows="3">${esc(values.notes ?? '')}</textarea></label><button id="save-task">${editing ? '保存修改' : esc(c.labels.submit)}</button></form></div>`, '', c.variant);
}
function taskDetail(c, record, saved) {
  return html(record.title, '栖木工作台', `<nav><a href="${c.base}/">任务列表</a></nav>${saved ? '<div role="status" class="success">任务已保存。以下为当前记录，请核对安排。</div>' : ''}<div class="panel"><span class="badge">${esc(record.status)}</span><h1>${esc(record.title)}</h1><dl><dt>任务编号</dt><dd>${record.id}</dd><dt>负责人</dt><dd>${esc(record.owner)}</dd><dt>优先级</dt><dd>${esc(record.priority)}</dd><dt>截止日期</dt><dd>${record.due}</dd><dt>状态</dt><dd>${esc(record.status)}</dd><dt>版本号</dt><dd>${record.revision}</dd><dt>备注</dt><dd>${esc(record.notes)}</dd></dl><a href="${c.base}/tasks/${record.id}/edit" class="button">编辑任务</a></div>`, '', c.variant);
}

function recordRequest(c, req, url, path) {
  const event = { method: req.method, path, query: Object.fromEntries(url.searchParams), at: Date.now(), user_agent: req.headers['user-agent'] ?? '', fetch_dest: req.headers['sec-fetch-dest'] ?? '', fetch_mode: req.headers['sec-fetch-mode'] ?? '' };
  event.browser = /Chrome|Chromium/.test(event.user_agent) && !!event.fetch_mode;
  c.state.audit.push(event);
  return event;
}
async function handle(c, req, res, url, path) {
  const event = recordRequest(c, req, url, path), isGet = req.method === 'GET';
  if (c.family === 'catalog' && isGet) {
    if (path === '/') { c.state.catalogRequests.push({ ...event, page: 1 }); return send(res, 200, catalogPage(c, url)); }
    if (path === '/catalog') {
      const page = Math.max(1, Number(url.searchParams.get('page') ?? 1));
      c.state.catalogRequests.push({ ...event, page });
      const list = filteredProducts(c, Object.fromEntries(url.searchParams));
      return send(res, 200, { html: catalogCards(c, list.slice((page - 1) * 4, page * 4)), hasMore: list.length > page * 4 }, 'application/json');
    }
    const p = c.data.products.find(p => path === `/products/${p.id}`);
    if (p) return send(res, 200, productPage(c, p));
  }
  if (c.family === 'docs' && isGet) {
    if (path === '/') return send(res, 200, docsPage(c, url));
    const d = c.data.docs.find(d => path === `/documents/${d.id}`);
    if (d) { c.state.documentVisits.push(event); return send(res, 200, documentPage(c, d)); }
  }
  if (c.family === 'workbench') {
    if (isGet && path === '/') return send(res, 200, workbenchHome(c));
    if (isGet && path === '/new') return send(res, 200, taskForm(c));
    const match = path.match(/^\/tasks\/([^/]+)(\/edit)?$/), record = match && c.state.records.find(r => r.id === match[1]);
    if (isGet && record) return send(res, 200, match[2] ? taskForm(c, record, '', record) : taskDetail(c, record, url.searchParams.has('saved')));
    if (req.method === 'POST' && (path === '/new' || record && match[2])) {
      const data = await readBody(req), values = Object.fromEntries(['title', 'owner', 'priority', 'due', 'notes', 'status'].map(k => [k, String(data[k] ?? '')]));
      c.state.submissions++;
      let error = !values.title.trim() || !c.data.people.includes(values.owner) || !['高', '普通', '低'].includes(values.priority) || !/^2026-\d\d-\d\d$/.test(values.due) ? '请补全任务名称、负责人、优先级和有效的截止日期。' : '';
      if (!error && c.id === 'workbench-recover' && values.due === c.data.unavailableDate) error = `排期容量已满：${values.due}不可用。最近可用日期为${c.data.correctedDate}，请调整截止日期后保存。`;
      if (error) { c.state.validationErrors.push({ message: error, values, browser: event.browser }); return send(res, 422, taskForm(c, values, error, record)); }
      if (record) {
        Object.assign(record, values, { revision: record.revision + 1 });
        if (c.state.mutations.some(m => m.record_id === record.id)) c.state.duplicateSubmissions++;
      } else {
        if (c.state.records.some(r => r.title === values.title)) c.state.duplicateSubmissions++;
        const created = { ...values, status: '待处理', id: `T-${c.n + 10 + c.state.successfulSubmissions}`, revision: 1 };
        c.state.records.push(created);
      }
      const target = record ?? c.state.records.at(-1);
      c.state.successfulSubmissions++;
      c.state.mutations.push({ record_id: target.id, browser: event.browser, type: record ? 'update' : 'create', fields: clone(values), revision: target.revision });
      res.writeHead(303, { Location: `${c.base}/tasks/${target.id}?saved=1`, 'Cache-Control': 'no-store' }); return res.end();
    }
  }
  return send(res, 404, html('找不到页面', c.family === 'docs' ? '拾叶知识库' : c.family === 'catalog' ? '山海器物' : '栖木工作台', '<h1>页面不存在</h1><p>请返回业务首页。</p>'));
}

const normalized = answer => String(answer ?? '').normalize('NFKC').replace(/[*_`#]/g, '').replace(/[ \t]+/g, ' ').replace(/\n+/g, '\n').trim();
function answerFor(c) {
  if (c.family === 'catalog') {
    return `推荐选择${c.data.chosen.name}。${c.data.compared.map(p => `${p.name}：价格${p.price}元，重量${p.weight}克，续航${p.battery}小时${c.id === 'catalog-fit' ? `，${p.resolution} PPI，防水支持` : ''}；详情 ${c.origin + c.base}/products/${p.id}。`).join('')}`;
  }
  if (c.family === 'docs') return c.data.required.map(d => `${d.sections.map(s => s.text).join(' ')} 依据：[${d.title}](${c.origin + c.base}/documents/${d.id})。`).join('\n');
  const record = expectedRecord(c);
  return `已保存任务${record.id}，负责人${record.owner}，截止日期${record.due}，状态${record.status}，版本号${record.revision}。已核验详情 ${c.origin + c.base}/tasks/${record.id}。`;
}
function expectedRecord(c) {
  return { ...c.data.form, due: c.id === 'workbench-recover' ? c.data.correctedDate : c.data.form.due, id: c.id === 'workbench-update' ? c.data.targetID : `T-${c.n + 10}`, revision: c.id === 'workbench-update' ? 2 : 1 };
}
function referenceActions(c) {
  const actions = [{ action: 'snapshot' }];
  if (c.family === 'catalog') {
    actions.push({ action: 'type', target: '#max', text: '350' });
    if (c.id === 'catalog-paged') actions.push({ action: 'select', target: '#available', text: 'yes' });
    actions.push({ action: 'click', target: '#filter' }, { action: 'snapshot' });
    if (c.id === 'catalog-paged') actions.push({ action: 'click', target: '#more' }, { action: 'snapshot' });
    if (c.id === 'catalog-fit' && c.data.products.filter(p => p.price <= 350).length > 4) actions.push({ action: 'click', target: '#more' }, { action: 'snapshot' });
    if (c.id === 'catalog-fit' && c.data.products.filter(p => p.price <= 350).length > 8) actions.push({ action: 'click', target: '#more' }, { action: 'snapshot' });
    for (const p of c.id === 'catalog-fit' ? c.data.products.filter(p => p.price <= 350) : c.data.compared) actions.push({ action: 'navigate', url: c.origin + c.base + '/products/' + p.id }, { action: 'snapshot' });
  } else if (c.family === 'docs') {
    const query = c.id === 'docs-policy' ? '配送' : c.id === 'docs-runbook' ? '密钥' : '留存';
    actions.push({ action: 'type', target: '#docs-q', text: query }, { action: 'click', target: '#docs-search' }, { action: 'snapshot' });
    for (const d of c.data.required) actions.push({ action: 'navigate', url: c.origin + c.base + '/documents/' + d.id }, { action: 'snapshot' });
  } else {
    actions.push(c.id === 'workbench-update' ? { action: 'navigate', url: c.origin + c.base + '/tasks/' + c.data.targetID + '/edit' } : { action: 'click', target: '#new-task' });
    const f = c.data.form;
    actions.push({ action: 'type', target: '#title', text: f.title }, { action: 'select', target: '#owner', text: f.owner }, { action: 'click', target: 'input[name="priority"][value="高"]' }, { action: 'type', target: '#due', text: f.due }, { action: 'type', target: '#notes', text: f.notes });
    if (c.id === 'workbench-update') actions.push({ action: 'select', target: '#status', text: f.status });
    actions.push({ action: 'click', target: '#save-task' }, { action: 'snapshot' });
    if (c.id === 'workbench-recover') actions.push({ action: 'type', target: '#due', text: c.data.correctedDate }, { action: 'click', target: '#save-task' }, { action: 'snapshot' });
  }
  return actions;
}
function nearbyNumber(text, label, value, unit = '') {
  const pattern = new RegExp(`${label}[^。；;]{0,30}?([0-9]+)\\s*${unit}`, 'g');
  const found = [...text.matchAll(pattern)].map(m => Number(m[1]));
  return found.length > 0 && found.every(n => n === value);
}
function tableFacts(answer, products) {
  // Interpret ordinary Markdown table headers, retaining entity/field
  // relationships rather than making answers use an evaluator format.
  const lines = normalized(answer).split('\n'), facts = [];
  const field = (header, cell = '') => {
    const n = cell.match(/\d+(?:\.\d+)?/)?.[0] ?? '';
    if (/价格|售价|费用/.test(header)) return `价格${n}元`;
    if (/重量|克|\(g\)/i.test(header)) return `重量${n}克`;
    if (/续航/.test(header)) return `续航${n}小时`;
    if (/防水/.test(header)) return `防水${/^(是|有|支持|✓|yes)$/i.test(cell) ? '支持' : cell}`;
    if (/清晰度|PPI/.test(header)) return `${n} PPI`;
    return cell;
  };
  let table = [], context = '';
  const flush = () => {
    if (table.length < 2) { table = []; return; }
    const [headers, ...rows] = table;
    const entities = headers.slice(1).map(header => products.find(p => header.includes(p.name)));
    if (entities.length && entities.every(Boolean)) {
      entities.forEach((product, column) => facts.push([product.name, ...rows.map(row => field(row[0], row[column + 1]))].join('，')));
    } else if (rows.some(row => /价格|售价/.test(row[0])) && rows.some(row => /重量/.test(row[0]))) {
      const product = products.map(p => ({ p, at: context.lastIndexOf(p.name) })).sort((a, b) => b.at - a.at)[0];
      if (product?.at >= 0) facts.push([product.p.name, ...rows.map(row => field(row[0], row[1]))].join('，'));
    } else rows.forEach(row => facts.push(row.map((cell, i) => field(headers[i] ?? '', cell)).join('，')));
    table = [];
  };
  for (const [index, line] of lines.entries()) {
    if (!line.includes('|')) { flush(); continue; }
    if (!table.length) context = lines.slice(0, index).join('\n');
    const cells = line.trim().split('|');
    if (line.trim().startsWith('|')) cells.shift();
    if (line.trim().endsWith('|')) cells.pop();
    for (let i = 0; i < cells.length; i++) cells[i] = cells[i].trim();
    if (!cells.every(v => /^:?-+:?$/.test(v))) table.push(cells);
  }
  flush();
  return facts.join('\n');
}
function entitySegments(text, product, products) {
  const aliases = new Map();
  for (const p of products) { const alias = p.name.split(' ')[0]; aliases.set(alias, (aliases.get(alias) ?? 0) + 1); }
  const positions = new Map();
  for (const p of products) for (const token of [p.name, ...(aliases.get(p.name.split(' ')[0]) === 1 ? [p.name.split(' ')[0]] : [])]) {
    let at = text.indexOf(token);
    while (at >= 0) {
      if (!positions.has(at) || positions.get(at).token.length < token.length) positions.set(at, { at, id: p.id, token });
      at = text.indexOf(token, at + token.length);
    }
  }
  const matches = [...positions.values()].sort((a, b) => a.at - b.at);
  return matches.flatMap((match, index) => {
    if (match.id !== product.id) return [];
    const period = text.indexOf('。', match.at + match.token.length), next = matches[index + 1]?.at ?? text.length;
    return [text.slice(match.at, period < 0 ? next : Math.min(next, period))];
  });
}
function priceStatements(segment) {
  const absolute = [], peers = [], differences = [], aggregates = [], ranges = [];
  for (const clause of segment.split(/[。\n]/)) {
    const rangePattern = /[¥￥]\s*(\d+)\s*(?:[-–—~～]|至|到)\s*[¥￥]?\s*(\d+)/g;
    for (const match of clause.matchAll(rangePattern)) {
      const before = clause.slice(0, match.index);
      const scope = /候选|共(?:得|有)?\s*\d+\s*款|全部商品|筛选结果/.test(before) ? 'candidate_set' : /(?:价格|售价|报价)(?:为|是|范围|在|[:：]|\s)*$/.test(before) ? 'entity' : 'unresolved';
      ranges.push({ lower: Number(match[1]), upper: Number(match[2]), scope, text: match[0], context: clause.trim().slice(0, 240) });
    }
    const scalarClause = clause.replace(rangePattern, ' ');
    if (/(?:价格|售价|报价)(?:分别|依次|列表|序列)/.test(clause)) { aggregates.push([...clause.matchAll(/[¥￥]\s*(\d+)/g)].map(m => Number(m[1]))); continue; }
    absolute.push(...[...scalarClause.matchAll(/(?:价格|售价)(?:为|是|[:：]|\s|¥|￥)*(\d+)/g)].map(m => Number(m[1])));
    for (const match of scalarClause.matchAll(/[¥￥]\s*(\d+)/g)) {
      const before = scalarClause.slice(Math.max(0, match.index - 24), match.index);
      if (/(?:价差|差价|价格(?:便宜|更低|低|少)|便宜|相差|节省|省)(?:了|约|为|是|[:：]|\s)*$/.test(before)) differences.push(Number(match[1]));
      else if (/(?:比|vs\.?)\s*$/i.test(before)) peers.push(Number(match[1]));
      else absolute.push(Number(match[1]));
    }
    for (const match of scalarClause.matchAll(/(?:价差|差价|便宜|相差|节省)(?:了|约|为|是|[:：]|\s)*(\d+)\s*元/g)) differences.push(Number(match[1]));
  }
  return { absolute, peers, differences, aggregates, ranges };
}
function weightStatements(segment) {
  const absolute = [], peers = [], differences = [];
  for (const match of segment.matchAll(/(\d+)\s*(?:克|g)(?![A-Za-z])/g)) {
    const before = segment.slice(Math.max(0, match.index - 24), match.index), after = segment.slice(match.index + match[0].length, match.index + match[0].length + 8);
    if (/(?:至少|至多|不超过|上限|低于|小于|最多|≥|≤|<=|>=)\s*$/.test(before) || /^(?:以内|以下|以上)/.test(after)) continue;
    if (/(?:轻|重了|重量差|相差|减少)(?:了|约|[:：]|\s)*$/.test(before)) differences.push(Number(match[1]));
    else if (/(?:比|vs\.?|<|>)\s*$/i.test(before)) peers.push(Number(match[1]));
    else absolute.push(Number(match[1]));
  }
  return { absolute, peers, differences };
}
function claimScope(text, index) {
  const start = Math.max(text.lastIndexOf('。', index) + 1, text.lastIndexOf('\n', index) + 1), before = text.slice(start, index);
  if (/(?:无|没有|不存在|未发现)(?:[^。；;\n]{0,24}(?:或|和|及|与|、))?(?:任何)?$/.test(before)) return 'negated_absence';
  const opening = text.lastIndexOf('(', index), closing = text.indexOf(')', opening);
  if (opening >= start && closing >= index && /编辑前|修改前|保存前|原先|历史/.test(text.slice(opening, Math.min(index, opening + 18)))) return 'historical';
  if (/(?:编辑前|修改前|保存前)(?:或|还是|可能)[^。；;\n]{0,24}(?:保存后|编辑后|修改后)/.test(before)) return 'unresolved';
  const past = [...before.matchAll(/编辑前|修改前|保存前|首次|第一次|初次|原日期|原先|被系统拒绝|系统明确拒绝/g)].at(-1)?.index ?? -1;
  const current = [...before.matchAll(/编辑后|修改后|保存后|当前|现在|目前|最终|至今|仍然?/g)].at(-1)?.index ?? -1;
  return past > current ? 'historical' : 'current';
}
function retentionSegments(text, entity) {
  const patterns = { logs: /操作日志|日常日志/g, archive: /审计归档|项目归档/g, backups: /服务备份|备份数据|备份/g }, positions = new Map();
  for (const [kind, pattern] of Object.entries(patterns)) for (const match of text.matchAll(pattern)) if (!positions.has(match.index) || positions.get(match.index).length < match[0].length) positions.set(match.index, { kind, at: match.index, length: match[0].length });
  const matches = [...positions.values()].sort((a, b) => a.at - b.at);
  return matches.flatMap((match, i) => { if (match.kind !== entity) return []; const period = text.indexOf('。', match.at + match.length), next = matches[i + 1]?.at ?? text.length; return [text.slice(match.at, period < 0 ? next : Math.min(next, period))]; });
}
function retentionRanks(c, answer) {
  if (c.id !== 'docs-retention') return { errors: [], unresolved: [] };
  const values = { logs: c.data.facts[0].number, archive: c.data.facts[1].number * 365, backups: c.data.facts[2].number }, ordered = Object.keys(values).sort((a, b) => values[a] - values[b]);
  const expected = Object.fromEntries(ordered.map((entity, i) => [entity, ['shortest', 'middle', 'longest'][i]])), errors = [], unresolved = [];
  for (const sentence of normalized(answer).split(/[。\n]/)) {
    const scope = ['logs', 'archive', 'backups'].filter(entity => retentionSegments(sentence, entity).length);
    for (const entity of scope) for (const segment of retentionSegments(sentence, entity)) {
      const match = segment.match(/^(?:操作日志|日常日志|审计归档|项目归档|服务备份|备份数据|备份)[^。]{0,8}?(最短|最长|居中)/);
      if (!match) continue;
      const claimed = { 最短: 'shortest', 居中: 'middle', 最长: 'longest' }[match[1]];
      if (scope.length !== 3) unresolved.push({ type: 'retention_order_scope_unclear', entity, claim: match[0] });
      else if (claimed !== expected[entity]) errors.push({ type: 'retention_order_mismatch', entity, claimed_rank: claimed, expected_rank: expected[entity], comparison_scope: 'all_three_retention_types' });
    }
  }
  return { errors, unresolved };
}
function answerFailures(c, answer) {
  const text = normalized(answer), failures = [];
  if (!text) return ['natural_answer_missing'];
  const denials = [...text.matchAll(/(?:未|没有|尚未|无法)(?:完成|保存|创建|核验)|未经(?:核验|验证)/g)];
  if (denials.some(m => {
    const scope = claimScope(text, m.index);
    if (scope === 'negated_absence' || scope === 'unresolved') return false;
    const before = text.slice(Math.max(text.lastIndexOf('\n', m.index) + 1, m.index - 170), m.index), after = text.slice(m.index + m[0].length, text.indexOf('\n', m.index) < 0 ? text.length : text.indexOf('\n', m.index));
    if (/(?:当前|现在|目前|最终|至今|仍然?)[^。；;\n]{0,8}$/.test(before)) return true;
    return !(c.id === 'workbench-recover' && c.state.successfulSubmissions === 1 && c.state.validationErrors.some(e => e.browser) && (scope === 'historical' || /首次|第一次|初次|系统明确拒绝|系统拒绝/.test(before) && /保存成功|已保存|创建成功|已创建/.test(after)));
  }) || /(?:猜测|虚构|随便填写)/.test(text)) failures.push('answer_denies_completion_or_evidence');
  if (c.family === 'catalog') {
    const chosen = c.data.chosen;
    const recommendations = c.data.products.filter(p => text.split(/[。\n]/).some(part => {
      const pattern = new RegExp(`(?:推荐|首选|建议选择|建议选用|选择|选用|选购)[^。；;]{0,15}${p.name}|${p.name}[^。；;]{0,12}(?:是首选|值得推荐|更适合)`, 'g');
      return [...part.matchAll(pattern)].some(m => !/(?:不|别|勿|不要|无需|不能|不应|不建议)(?:推荐|建议)?$/.test(part.slice(Math.max(0, m.index - 8), m.index)));
    }));
    if (!recommendations.some(p => p.id === chosen.id) || recommendations.some(p => p.id !== chosen.id)) failures.push('recommendation_missing_or_wrong');
    if (new RegExp(`(?:不推荐|不建议|不应选择|不能选择|不要选择)[^。\n]{0,20}${chosen.name}`).test(text)) failures.push('recommendation_denies_chosen_product');
    const entityText = text.split('\n').filter(line => !line.includes('|')).join('\n') + '\n' + tableFacts(answer, c.data.products);
    for (const p of c.data.compared) {
      const candidates = entitySegments(entityText, p, c.data.products);
      const chunk = candidates.find(part => part.includes(String(p.price)) && nearbyNumber(part, '(?:重量|重)', p.weight, '(?:克|g)') && (c.id === 'catalog-fit' || nearbyNumber(part, '续航', p.battery, '(?:小时|h)'))) ?? '';
      if (!chunk) failures.push('product_facts_missing_or_wrong');
      const statements = candidates.map(priceStatements), peers = c.data.products.filter(other => other.id !== p.id);
      if (statements.some(s => s.absolute.some(value => value !== p.price) || s.peers.some(value => !peers.some(other => other.price === value)))) failures.push('contradictory_product_price');
      const comparisonPeers = c.data.compared.length > 1 ? c.data.compared.filter(other => other.id !== p.id) : peers;
      if (statements.some(s => s.differences.some(value => !comparisonPeers.some(other => Math.abs(other.price - p.price) === value)))) failures.push('contradictory_price_difference');
      const aggregatePrices = [c.data.compared.map(other => other.price), ...c.state.catalogRequests.filter(r => r.browser).map(r => filteredProducts(c, r.query ?? {}).map(other => other.price))].map(values => [...values].sort((a, b) => a - b).join(','));
      if (statements.some(s => s.aggregates.some(values => values.length < 2 || !aggregatePrices.includes([...values].sort((a, b) => a - b).join(','))))) failures.push('aggregate_price_list_mismatch');
      if (statements.some(s => s.ranges.some(range => range.scope === 'entity' && (range.lower !== p.price || range.upper !== p.price)))) failures.push('contradictory_product_price_range');
      const observedCandidatePrices = [c.data.compared.map(other => other.price), ...c.state.catalogRequests.filter(r => r.browser).map(r => filteredProducts(c, r.query ?? {}).map(other => other.price))].filter(values => values.length);
      if (statements.some(s => s.ranges.some(range => range.scope === 'candidate_set' && !observedCandidatePrices.some(values => Math.min(...values) === range.lower && Math.max(...values) === range.upper)))) failures.push('aggregate_price_range_mismatch');
      const weights = candidates.map(weightStatements);
      if (weights.some(s => s.absolute.some(value => value !== p.weight) || s.peers.some(value => !peers.some(other => other.weight === value)))) failures.push('contradictory_product_weight');
      if (weights.some(s => s.differences.some(value => !comparisonPeers.some(other => Math.abs(other.weight - p.weight) === value)))) failures.push('contradictory_weight_difference');
      if (c.id === 'catalog-fit' && (!chunk.includes('300 PPI') && !chunk.includes('300PPI') || !/(?:防水支持|支持防水|防水[:：]?是|防水[:：]?支持)/.test(chunk))) failures.push('reader_facts_missing_or_wrong');
      if (c.id === 'catalog-fit' && (candidates.some(part => /不支持防水|不防水|防水(?:[:：]|为|是)?(?:否|不支持)|防水支持(?:是|为)(?:错误|不正确)/.test(part)) || /两款(?:都)?不支持防水/.test(text))) failures.push('contradictory_reader_waterproof');
      if (!text.includes(c.origin + c.base + '/products/' + p.id)) failures.push('product_source_missing');
    }
  } else if (c.family === 'docs') {
    for (const d of c.data.required) {
      if (!text.includes(c.origin + c.base + '/documents/' + d.id)) failures.push('document_source_missing');
    }
    // Bind each business fact to its own semantic label and citation. A bag
    // of all expected numbers (or swapped labels/citations) cannot pass.
    const paragraphs = text.split('\n').filter(Boolean);
    const clauses = text.split(/[。；;\n]|(?<=小时|分钟|天|年|元|%)，/);
    for (const fact of c.data.facts) {
      const source = c.origin + c.base + '/documents/' + fact.doc;
      const relevant = part => fact.expression ? new RegExp(fact.expression).test(part) : new RegExp(fact.anchor).test(part) && [...part.matchAll(new RegExp(`(\\d+)\\s*${fact.unit}`, 'g'))].some(m => Number(m[1]) === fact.number);
      const matches = clauses.some(relevant) && text.includes(source);
      if (!matches) failures.push('cited_semantic_fact_missing_or_wrong');
      // Inline explicit attribution to another document is a contradiction;
      // separate reference lists remain a valid citation presentation.
      if (paragraphs.some(p => p.split(/[。；;]/).some(relevant) && !p.includes(source) && c.data.required.some(d => p.includes(c.origin + c.base + '/documents/' + d.id)))) failures.push('explicit_document_attribution_wrong');
      if (fact.number !== undefined) {
        const retentionEntity = /操作日志|日常日志/.test(fact.anchor) ? 'logs' : /审计归档|项目归档/.test(fact.anchor) ? 'archive' : 'backups';
        const labeled = c.id === 'docs-retention' ? retentionSegments(text, retentionEntity) : clauses.filter(part => new RegExp(fact.anchor).test(part));
        if (labeled.some(part => [...part.matchAll(new RegExp(`(\\d+)\\s*${fact.unit}`, 'g'))].some(m => Number(m[1]) !== fact.number))) failures.push('contradictory_semantic_fact');
      }
    }
    if (c.id === 'docs-policy' && (!/配送券|运费券/.test(text) || !/订单号/.test(text) || !/物流.*截图|截图.*物流/.test(text) || !/不(?:会)?(?:自动)?退还商品|独立|两种|区别/.test(text))) failures.push('policy_distinction_or_material_missing');
    if (c.id === 'docs-runbook' && (!/连续/.test(text) || !/错误率/.test(text) || !/回滚/.test(text) || !/健康检查/.test(text) || !/审计.*新密钥|新密钥.*审计/.test(text))) failures.push('runbook_rule_or_checks_missing');
    if (c.id === 'docs-retention' && (!/操作日志/.test(text) || !/审计归档/.test(text) || !/备份/.test(text) || !/活动数据.*(?:立即不可见|立即.*不可|马上.*不可)/.test(text) || !/自然过期|窗口.*过期/.test(text))) failures.push('retention_distinction_missing');
    const expectedNumbers = new Set(c.data.required.flatMap(d => d.sections.flatMap(s => [...s.text.matchAll(/(\d+)(天|小时|分钟|年|元|%)/g)].map(m => m[1] + m[2]))));
    if ([...text.matchAll(/(\d+)\s*(天|小时|分钟|年|元|%)/g)].some(m => !expectedNumbers.has(m[1] + m[2]))) failures.push('contradictory_document_number');
    if (/(?:不需要|不必|不用|无需)(?:提供|完成|进行)?(?:订单号|物流|健康检查|核验)|(?:旧密钥|备份|审计归档).*永久/.test(text)) failures.push('contradictory_document_rule');
  } else {
    const expected = expectedRecord(c);
    if (!text.includes(expected.id) || c.id !== 'workbench-update' && !text.includes(expected.due) || c.id === 'workbench-create' && !text.includes(expected.owner)) failures.push('task_receipt_facts_missing');
    if (c.id === 'workbench-update' && (!text.includes(expected.status) || !nearbyNumber(text, '版本(?:号)?', expected.revision))) failures.push('task_revision_or_status_missing');
    if (new RegExp(`(?:负责人|截止日期|状态|任务编号)(?:不(?:是|为)|并非)[^。\n]{0,6}(?:${expected.owner}|${expected.due}|${expected.status}|${expected.id})`).test(text)) failures.push('contradictory_task_field');
    const ownerPattern = new RegExp(`负责人(?:为|是|[:：])?(${c.data.people.filter(p => p !== expected.owner).join('|')})`, 'g');
    if ([...text.matchAll(ownerPattern)].some(m => claimScope(text, m.index) === 'current')) failures.push('contradictory_task_owner');
    const dates = [...text.matchAll(/2026-\d\d-\d\d/g)];
    if (dates.some(m => {
      if (m[0] === expected.due) return false;
      if (c.id !== 'workbench-recover' || m[0] !== c.data.unavailableDate) return true;
      const before = text.slice(Math.max(0, m.index - 45), m.index), after = text.slice(m.index + m[0].length, m.index + m[0].length + 24);
      if (/(?:最终|最后|实际|已保存的?)(?:截止日期|日期)?[^。\n]{0,8}$/.test(before)) return true;
      return !(/原|目标|期望/.test(before) || /首次|第一次|初次/.test(before) && /提交|保存|安排/.test(after) && c.state.validationErrors.some(e => e.browser && e.values.due === m[0]) || /(?:排期容量已满|系统提示)[^。\n]{0,35}$/.test(before) && /不可用|不可安排|容量已满/.test(after));
    })) failures.push('contradictory_task_due_date');
  }
  return [...new Set(failures)];
}
function judgeCase(c, { answer, actions = [] } = {}) {
  const answerErrors = answerFailures(c, answer), failures = [...answerErrors], executed = actions.filter(a => a && a.ok === true);
  const nativePageActions = new Set(['snapshot', 'extract', 'find', 'navigate', 'click', 'type', 'select', 'press', 'scroll', 'wait', 'tab', 'tabs', 'screenshot']);
  const observed = executed.filter(a => {
    if (!nativePageActions.has(a.action) || a.result?.ok === false || typeof (a.result?.text ?? a.result?.data?.text) !== 'string' || !(a.result?.text ?? a.result?.data?.text).trim()) return false;
    try { const url = new URL(a.result?.url ?? a.result?.data?.url); return url.origin === c.origin && url.pathname.startsWith(c.base + '/'); } catch { return false; }
  });
  const observedText = observed.map(a => a.result?.text ?? a.result?.data?.text ?? '').join('\n');
  if (!observed.length) failures.push('actual_visible_read_missing');
  if (!executed.some(a => ['navigate', 'click'].includes(a.action))) failures.push('actual_browser_navigation_missing');
  if (!c.state.audit.some(a => a.browser && a.method === 'GET' && a.path === '/')) failures.push('browser_start_visit_missing');
  const visited = path => c.state.audit.some(a => a.browser && a.method === 'GET' && a.path === path && a.fetch_dest === 'document');
  if (c.family === 'catalog') {
    if (!c.data.compared.every(p => visited('/products/' + p.id))) failures.push('product_detail_visits_missing');
    if (!c.data.compared.every(p => observedText.includes(p.name) && observedText.includes(String(p.weight)))) failures.push('visible_product_evidence_missing');
    if (c.id === 'catalog-paged' && !c.state.catalogRequests.some(a => a.browser && a.page >= 2)) failures.push('dynamic_catalog_load_missing');
    if (['catalog-paged', 'catalog-fit'].includes(c.id) && !catalogCoverage(c, observedText)) failures.push('catalog_candidates_not_fully_observed');
    if (c.id === 'catalog-fit' && !c.data.products.filter(p => p.price <= 350).every(p => visited('/products/' + p.id) && observedText.includes(p.name) && observedText.includes(String(p.weight)))) failures.push('eligible_reader_detail_coverage_missing');
  } else if (c.family === 'docs') {
    if (!c.data.required.every(d => visited('/documents/' + d.id))) failures.push('document_detail_visits_missing');
    if (!c.data.required.every(d => observedText.includes(d.title) && d.sections.every(s => observedText.includes(s.text)))) failures.push('visible_document_evidence_missing');
  } else {
    const expected = expectedRecord(c), actual = c.state.records.find(r => r.id === expected.id);
    if (!actual || !Object.entries(expected).every(([k, v]) => actual[k] === v)) failures.push('business_record_missing_or_wrong');
    if (c.state.successfulSubmissions !== 1 || !c.state.mutations.every(m => m.browser)) failures.push('actual_unique_browser_mutation_missing');
    if (!visited('/tasks/' + expected.id)) failures.push('saved_task_verification_missing');
    if (!observedText.includes(expected.id) || !observedText.includes(expected.due)) failures.push('visible_saved_task_evidence_missing');
    if (c.id === 'workbench-recover' && !c.state.validationErrors.some(e => e.browser && e.values.due === c.data.unavailableDate && e.message.includes(c.data.correctedDate))) failures.push('validation_recovery_missing');
    if (c.state.duplicateSubmissions > 0) failures.push('duplicate_submission');
  }
  const coreBusinessSuccess = failures.length === 0, incidentalErrors = incidentalAnswerErrors(c, answer), unresolvedClaims = [...unresolvedPriceClaims(c, answer), ...retentionRanks(c, answer).unresolved, ...unresolvedTemporalClaims(c, answer)];
  const incidentalFailures = [...new Set(incidentalErrors.map(error => error.type === 'retention_order_mismatch' ? 'incidental_retention_order_mismatch' : error.type === 'historical_task_owner_mismatch' ? 'incidental_historical_task_owner_mismatch' : 'incidental_task_reference_mismatch'))];
  failures.push(...incidentalFailures);
  if (unresolvedClaims.length) failures.push('answer_claim_review_required');
  const definiteClaimFailures = [...answerErrors, ...incidentalFailures];
  const answerAccuracy = definiteClaimFailures.length || c.family === 'workbench' && c.state.successfulSubmissions !== 1 ? false : unresolvedClaims.length ? null : true;
  return { success: failures.length === 0, core_business_success: coreBusinessSuccess, answer_accuracy: answerAccuracy, incidental_answer_errors: incidentalErrors, answer_claim_assessment: { definite_claim_failures: [...new Set(definiteClaimFailures)], unresolved_claims: unresolvedClaims, needs_review: unresolvedClaims.length > 0 }, checker_version: 'general-web-checker-v3', incidental_rule_version: 'task-record-reference-v1', reason: failures.length ? [...new Set(failures)].join('; ') : 'business state, browser actions and natural answer verified', failures: [...new Set(failures)], duplicate_submissions: c.state.duplicateSubmissions };
}

function unresolvedPriceClaims(c, answer) {
  if (c.family !== 'catalog') return [];
  const text = normalized(answer).split('\n').filter(line => !line.includes('|')).join('\n') + '\n' + tableFacts(answer, c.data.products);
  const unresolved = c.data.compared.flatMap(product => entitySegments(text, product, c.data.products).flatMap(segment => priceStatements(segment).ranges.filter(range => range.scope === 'unresolved').map(range => ({ type: 'unattributed_price_range', text: range.context, lower: range.lower, upper: range.upper }))));
  return unresolved.filter((item, index) => unresolved.findIndex(other => JSON.stringify(other) === JSON.stringify(item)) === index);
}
function unresolvedTemporalClaims(c, answer) {
  if (c.family !== 'workbench') return [];
  const text = normalized(answer), pattern = new RegExp(`负责人(?:为|是|[:：])?(?:${c.data.people.join('|')})|(?:未|没有|尚未|无法)(?:完成|保存|创建|核验)`, 'g');
  return [...text.matchAll(pattern)].filter(match => claimScope(text, match.index) === 'unresolved').map(match => ({ type: 'temporal_claim_scope_unclear', claim: match[0] }));
}

function incidentalAnswerErrors(c, answer) {
  if (c.id === 'docs-retention') return retentionRanks(c, answer).errors;
  if (c.family !== 'workbench') return [];
  const text = normalized(answer), records = c.state.records, errors = [];
  if (c.id === 'workbench-update') {
    const initial = c.initialRecords.find(record => record.id === c.data.targetID), ownerPattern = new RegExp(`负责人(?:为|是|[:：])?(${c.data.people.join('|')})`, 'g');
    for (const match of text.matchAll(ownerPattern)) if (claimScope(text, match.index) === 'historical' && match[1] !== initial.owner) errors.push({ type: 'historical_task_owner_mismatch', record_id: initial.id, expected_owner: initial.owner, stated_owner: match[1] });
  }
  for (const record of records) {
    let at = text.indexOf(record.id);
    while (at >= 0) {
      if (/\d/.test(text[at + record.id.length] ?? '')) { at = text.indexOf(record.id, at + record.id.length); continue; }
      let end = text.indexOf('。', at + record.id.length); if (end < 0) end = text.length;
      for (const next of records) { const boundary = text.indexOf(next.id, at + record.id.length); if (boundary >= 0 && boundary < end) end = boundary; }
      const afterID = at + record.id.length, boundary = text.slice(afterID, end).search(/[，,、；;）)\]\n]/);
      if (boundary >= 0) end = afterID + boundary;
      const segment = text.slice(at, Math.min(end, at + record.id.length + 60)), expectedTitle = record.title.replace(/\s+\d+$/, '');
      const stated = records.map(r => ({ record: r, at: segment.indexOf(r.title.replace(/\s+\d+$/, '')) })).filter(r => r.at >= 0).sort((a, b) => a.at - b.at)[0]?.record;
      if (stated && stated.title.replace(/\s+\d+$/, '') !== expectedTitle) errors.push({ type: 'task_reference_mismatch', record_id: record.id, expected_title: record.title, stated_title: stated.title });
      at = text.indexOf(record.id, at + record.id.length);
    }
  }
  return errors.filter((error, i) => errors.findIndex(other => other.record_id === error.record_id && other.stated_title === error.stated_title) === i);
}

function catalogCoverage(c, observedText) {
  const groups = new Map();
  for (const request of c.state.catalogRequests) {
    if (!request.browser || !Number.isInteger(request.page) || request.page < 1) continue;
    const q = request.query ?? {};
    if (q.q || q.category && q.category !== c.data.group || q.max && (!Number.isFinite(Number(q.max)) || Number(q.max) < 350) || c.id === 'catalog-fit' && q.available === 'yes' && c.data.products.some(p => !p.stock && p.price <= 350)) continue;
    const filters = { q: q.q ?? '', category: q.category ?? '', max: q.max ?? '', available: q.available ?? '' }, key = JSON.stringify(filters);
    if (!groups.has(key)) groups.set(key, { pages: new Set(), products: filteredProducts(c, filters) });
    groups.get(key).pages.add(request.page);
  }
  return [...groups.values()].some(group => {
    const last = Math.ceil(group.products.length / 4);
    return group.products.length > 0 && Array.from({ length: last }, (_, i) => i + 1).every(page => group.pages.has(page)) && group.products.every(product => observedText.includes(product.name));
  });
}

/** Pure Node controller review. No servers, browser, HTTP oracle or runtime
 * calls are created. This reviews only business evidence; caller must retain
 * independent completion, budget, metadata, network and cleanup failures.
 * Answer checking is bounded to explicit entity fields, candidate price
 * sets/ranges, current versus historical task claims, and explicit ranking
 * of all three retention types. It is not a general semantic/NLP grader.
 * Unresolved supported claims require review: accuracy is null and overall
 * success is false; an unknown claim never supplies business evidence.
 */
export function judgeRecordedGeneralWebCase(id, seed, startURL, answer, actions, recordedState) {
  const invalid = reason => ({ success: false, core_business_success: false, answer_accuracy: null, incidental_answer_errors: [], answer_claim_assessment: { definite_claim_failures: [], unresolved_claims: [{ type: 'unverifiable_case_identity', reason }], needs_review: true }, checker_version: 'general-web-checker-v3', incidental_rule_version: 'task-record-reference-v1', reason, failures: [reason], duplicate_submissions: 0, review_scope: 'business_evidence_only', run_validity: 'not_assessed' });
  const meta = TASKS.find(task => task.id === id);
  if (!meta || !Number.isInteger(seed) || seed < 0 || seed > 0xffffffff) return invalid('recorded_case_or_seed_invalid');
  let url;
  try { url = new URL(startURL); } catch { return invalid('recorded_start_url_invalid'); }
  if (url.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) || !url.port || url.pathname !== `/cases/${id}/` || url.search || url.hash) return invalid('recorded_start_url_case_mismatch');
  const c = makeCase(meta, seed);
  if (!recordedState || recordedState.caseID !== id || recordedState.family !== meta.family || recordedState.seed !== seed || recordedState.layout_variant !== c.variant) return invalid('recorded_identity_mismatch');
  for (const field of ['audit', 'catalogRequests', 'documentVisits', 'records', 'mutations', 'validationErrors']) if (!Array.isArray(recordedState[field])) return invalid('recorded_server_state_invalid');
  for (const field of ['submissions', 'successfulSubmissions', 'duplicateSubmissions']) if (!Number.isInteger(recordedState[field]) || recordedState[field] < 0) return invalid('recorded_server_state_invalid');
  if (!Array.isArray(actions) || recordedState.audit.some(event => !event || typeof event.path !== 'string' || !event.path.startsWith('/') || event.path.includes('..') || !['GET', 'POST'].includes(event.method) || event.browser !== (/Chrome|Chromium/.test(event.user_agent ?? '') && !!event.fetch_mode))) return invalid('recorded_audit_or_actions_invalid');
  c.origin = url.origin;
  c.state = clone(recordedState);
  return { ...judgeCase(c, { answer, actions }), review_scope: 'business_evidence_only', run_validity: 'not_assessed' };
}

/** Three isolated synthetic origins; seed controls facts, names and layout.
 * listen is idempotent; create already listens on ephemeral loopback ports.
 * All inspect/reference/expected methods are in-process controller functions.
 */
export async function createGeneralWebFixtures({ seed = 731, host = '127.0.0.1', ports = [0, 0, 0] } = {}) {
  if (!Number.isInteger(seed) || seed < 0 || seed > 0xffffffff) throw new Error('seed must be an unsigned 32-bit integer');
  if (!['127.0.0.1', 'localhost', '::1'].includes(host) || !Array.isArray(ports) || ports.length !== 3 || ports.some(port => !Number.isInteger(port) || port < 0 || port > 65535)) throw new Error('three valid loopback ports are required');
  const cases = new Map(TASKS.map(meta => [meta.id, makeCase(meta, seed)])), servers = new Map();
  let listening = false, closed = false;
  const get = caseID => { const c = cases.get(caseID); if (!c) throw new Error('unknown general-web case'); return c; };
  const fixture = {
    seed,
    tasks: [],
    origins: {},
    async listen() {
      if (closed) throw new Error('fixture is closed');
      if (listening) return clone(this.origins);
      for (const [index, family] of ['catalog', 'docs', 'workbench'].entries()) {
        const server = http.createServer(async (req, res) => {
          try {
            const url = new URL(req.url, 'http://127.0.0.1'), match = url.pathname.match(/^\/cases\/([^/]+)(\/.*)?$/);
            const c = match && cases.get(match[1]);
            if (!c || c.family !== family || /\/(?:oracle|expected|judge|controller|eval|__[^/]*)\b/i.test(url.pathname)) return send(res, 404, 'Page not found', 'text/plain');
            await handle(c, req, res, url, match[2] || '/');
          } catch { if (!res.headersSent) send(res, 400, 'Invalid business request', 'text/plain'); else res.end(); }
        });
        servers.set(family, server);
        await new Promise((resolve, reject) => { server.once('error', reject); server.listen(ports[index], host, resolve); });
        this.origins[family] = `http://${host === '::1' ? '[::1]' : host}:${server.address().port}`;
        for (const c of cases.values()) if (c.family === family) c.origin = this.origins[family];
      }
      this.tasks = [...cases.values()].map(c => Object.freeze({ id: c.id, caseID: c.caseID, family: c.family, category: c.category, title: c.title, goal: c.goal, seed, layout_variant: c.variant, startURL: c.origin + c.base + '/', prompt: `起始网址：${c.origin + c.base}/\n业务目标：${c.goal}`, allowedOrigins: [c.origin], allowedPathPrefixes: [c.base + '/'], max_steps: 60, timeout_ms: 180000 }));
      listening = true;
      return clone(this.origins);
    },
    startURLFor(caseID) { const c = get(caseID); return c.origin + c.base + '/'; },
    reset(caseID) { if (caseID === undefined) for (const c of cases.values()) resetCase(c); else resetCase(get(caseID)); },
    inspect(caseID) { const c = get(caseID); return clone({ caseID, family: c.family, seed, layout_variant: c.variant, ...c.state }); },
    referenceData(caseID) {
      const c = get(caseID);
      return { ...clone({ caseID, family: c.family, base: c.base, origin: c.origin, startURL: c.origin + c.base + '/', labels: c.labels, ...c.data, expectedRecord: c.family === 'workbench' ? expectedRecord(c) : undefined }), actions: referenceActions(c), answer: answerFor(c) };
    },
    expectedAnswer(caseID) { return answerFor(get(caseID)); },
    judge(caseID, evidence) { return judgeCase(get(caseID), evidence); },
    async close() {
      closed = true;
      await Promise.all([...servers.values()].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
    },
  };
  try { await fixture.listen(); } catch (error) { await fixture.close(); throw error; }
  return fixture;
}
