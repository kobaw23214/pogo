import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { captureDOMEvidence, installEvidenceProbe } from '../src/evidence.js';

let browser, context, site, origin;
before(async () => {
  const env = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DISPLAY', 'XDG_RUNTIME_DIR', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
  browser = await chromium.launch({ headless: true, env });
  context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html; charset=utf-8');
    if (request.url === '/protected') {
      response.end(`<!doctype html><title>Protected source</title><h1>真实参数</h1><table><tr><th>ID</th><th>重量</th></tr><tr><td>A-1</td><td id="weight">450 g</td></tr></table><dl><dt>保留</dt><dd>12 天</dd></dl><p>日志保留 24 天。</p><script>
        globalThis.probeInstalledBeforeWebsite=typeof globalThis.__evidence_test_probe__==='function';
        globalThis.overriddenDOMCalls=0;
        const method=(prototype,name)=>{const original=prototype[name];prototype[name]=function(...args){overriddenDOMCalls++;return Reflect.apply(original,this,args)}};
        for(const [prototype,name] of [[Element.prototype,'getAttribute'],[Node.prototype,'getRootNode'],[Node.prototype,'contains'],[Document.prototype,'createTreeWalker'],[Document.prototype,'createRange'],[TreeWalker.prototype,'nextNode'],[Range.prototype,'selectNodeContents'],[Range.prototype,'getClientRects'],[CSSStyleDeclaration.prototype,'getPropertyValue']])method(prototype,name);
        const originalStyle=getComputedStyle;globalThis.getComputedStyle=function(...args){overriddenDOMCalls++;return Reflect.apply(originalStyle,this,args)};
        const cell=document.querySelector('#weight');
        Object.defineProperty(cell,'rowSpan',{get(){overriddenDOMCalls++;return 8}});
        Object.defineProperty(cell,'colSpan',{get(){overriddenDOMCalls++;return 8}});
        Object.defineProperty(cell,'textContent',{get(){overriddenDOMCalls++;return '9999 g'}});
        Object.defineProperty(cell.firstChild,'textContent',{get(){overriddenDOMCalls++;return '9999 g'}});
        for(const [prototype,name] of [[Node.prototype,'parentElement'],[Element.prototype,'tagName'],[Element.prototype,'children'],[Element.prototype,'shadowRoot'],[Document.prototype,'URL'],[Document.prototype,'title']]){
          const original=Object.getOwnPropertyDescriptor(prototype,name);Object.defineProperty(prototype,name,{...original,get(){overriddenDOMCalls++;return Reflect.apply(original.get,this,[])}});
        }
        globalThis.__pi_pick_data__=()=>{overriddenDOMCalls++;throw new Error('site hook called')};
      </script>`);
      return;
    }
    response.end(request.url === '/child'
      ? '<!doctype html><title>Child source</title><h1>Child record</h1><dl><dt>编号</dt><dd>C-2</dd><dt>重量</dt><dd>0.45 kg</dd></dl>'
      : '<!doctype html><title>Main source</title><h1>Main record</h1><p>正文 12 天。</p><iframe src="/child"></iframe>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
});
after(async () => {
  await browser?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

async function read(html, options = {}) {
  const page = await context.newPage();
  try {
    await page.setContent(`<!doctype html><title>Evidence fixture</title>${html}`);
    return await page.mainFrame().evaluate(captureDOMEvidence, { sourcePrefix: 'observation-1/frame-1', ...options });
  } finally { await page.close(); }
}

test('table evidence retains empty cells, headers, native positions and distinct row sources', async () => {
  const evidence = await read('<h1>商品</h1><table><caption>现货</caption><thead><tr><th>ID</th><th>姓名</th><th>价格</th></tr></thead><tbody><tr><td>P-1</td><td></td><td>240 元</td></tr><tr><td>P-2</td><td>云桥</td><td>320 元</td></tr></tbody></table>');
  const table = evidence.tables[0];
  assert.equal(evidence.scope, 'document');
  assert.equal(table.column_mapping, 'simple');
  assert.equal(table.caption, '现货');
  assert.deepEqual(table.header_rows[0].cells.map(cell => cell.text), ['ID', '姓名', '价格']);
  assert.deepEqual(table.rows[0].cells.map(cell => cell.text), ['P-1', '', '240 元']);
  assert.deepEqual(table.rows[1].cells.map(cell => cell.text), ['P-2', '云桥', '320 元']);
  assert.notEqual(table.rows[0].source_id, table.rows[1].source_id);
  assert.match(table.rows[1].cells[2].source_id, /^observation-1\/frame-1\/table\/0\/row\/2\/cell\/2$/);
  assert.equal(evidence.truncated, false);
});

test('spans including rowspan zero and multiple header rows stay ambiguous without synthesized cells', async () => {
  const spans = await read('<table><tr><th colspan="2">参数</th></tr><tr><td rowspan="0">A</td><td>12 天</td></tr><tr><td>24 天</td></tr></table>');
  assert.equal(spans.tables[0].column_mapping, 'unknown');
  assert.equal(spans.tables[0].header_rows[0].cells[0].colspan, 2);
  assert.equal(spans.tables[0].rows[0].cells[0].rowspan, 0);
  assert.equal(spans.tables[0].rows[1].cells.length, 1);
  const multi = await read('<table><thead><tr><th>分组</th><th>参数</th></tr><tr><th>ID</th><th>天数</th></tr></thead><tr><td>A</td><td>12</td></tr></table>');
  assert.equal(multi.tables[0].header_rows.length, 2);
  assert.equal(multi.tables[0].column_mapping, 'unknown');
  const trailing = await read('<table><tr><td>A</td><td>12 天</td></tr><tr><th>汇总</th><th>值</th></tr></table>');
  assert.equal(trailing.tables[0].column_mapping, 'unknown', 'a trailing all-th row is not inferred as a column header');
});

test('nested tables and definition lists retain separate ownership and never splice inner records into outer fields', async () => {
  const evidence = await read('<table><tr><th>外部</th></tr><tr><td>outer<table><tr><th>ID</th><th>值</th></tr><tr><td>inner</td><td>24 天</td></tr></table></td></tr></table><dl><dt>外字段</dt><dd>outer value<dl><dt>内字段</dt><dd>inner value</dd></dl></dd><dt>第二字段</dt><dd></dd></dl>');
  assert.equal(evidence.tables.length, 2);
  assert.equal(evidence.tables[0].rows.length, 1);
  assert.equal(evidence.tables[0].rows[0].cells[0].text, 'outer');
  assert.deepEqual(evidence.tables[1].rows[0].cells.map(cell => cell.text), ['inner', '24 天']);
  assert.equal(evidence.definitions.length, 2);
  assert.equal(evidence.definitions[0].groups[0].descriptions[0].text, 'outer value');
  assert.equal(evidence.definitions[1].groups[0].descriptions[0].text, 'inner value');
  assert.equal(evidence.definitions[0].groups[1].descriptions[0].text, '');
});

test('definition groups preserve multiple terms and descriptions without inventing one-to-one fields', async () => {
  const evidence = await read('<h1>当前记录</h1><dl><dt>任务编号</dt><dd>T-23</dd><dt>维护人</dt><dt>负责人</dt><dd>林遥</dd><dd>赵宁</dd><dt>备注</dt><dd>核验后保存</dd></dl>');
  const groups = evidence.definitions[0].groups;
  assert.equal(groups.length, 3);
  assert.deepEqual(groups[1].terms.map(item => item.text), ['维护人', '负责人']);
  assert.deepEqual(groups[1].descriptions.map(item => item.text), ['林遥', '赵宁']);
  assert.match(groups[0].descriptions[0].source_id, /definition\/0\/group\/0\/description\/0$/);
});

test('hidden content, closed details, input/password values, editing fields and data hooks are excluded', async () => {
  const page = await context.newPage();
  try {
    await page.setContent('<!doctype html><h1>Visible</h1><p>visible value<span hidden>hidden-secret</span><span style="display:none">display-secret</span><span style="opacity:0">opacity-secret</span><span aria-hidden="true">aria-secret</span><span style="content-visibility:hidden">content-secret</span></p><details><summary>summary only</summary><p>details-secret</p></details><dl><dt>Normal</dt><dd>safe<input value="input-secret"><input type="password" value="password-secret"><textarea>textarea-secret</textarea><div contenteditable>editable-secret</div></dd></dl><script>globalThis.__pi_pick_data__=()=>{throw new Error("hook-secret")};globalThis.inputEvents=0;for(const e of ["click","submit","focusin"])document.addEventListener(e,()=>inputEvents++);</script>');
    const evidence = await page.evaluate(captureDOMEvidence, { observationId: 'privacy' });
    assert.ok(!JSON.stringify(evidence).includes('secret'));
    assert.match(evidence.text, /visible value/);
    assert.match(evidence.text, /summary only/);
    assert.equal(evidence.definitions[0].groups[0].descriptions[0].text, 'safe');
    assert.equal(await page.evaluate(() => inputEvents), 0);
  } finally { await page.close(); }
});

test('headings and paragraph excerpts preserve raw quantities; current page comes only from an explicit marker', async () => {
  const evidence = await read('<h1>保留期限</h1><section><h2>日志</h2><p>日志保留 24 天。</p></section><section><h2>备份</h2><p>备份保留 12 天。</p></section><section><h2>档案</h2>档案保留 2 年。</section><nav><a href="?page=1" rel="prev">上一页</a><span aria-current="page">第 2 页</span><a href="?page=3" rel="next">下一页</a></nav>');
  assert.deepEqual(evidence.headings.map(item => item.text), ['保留期限', '日志', '备份', '档案']);
  assert.match(evidence.paragraphs[0].text, /24 天/);
  assert.equal(evidence.paragraphs[0].heading, '日志');
  assert.equal(evidence.paragraphs[1].heading, '备份');
  assert.match(evidence.paragraphs[2].text, /2 年/);
  assert.equal(evidence.paragraphs[2].heading, '档案');
  assert.ok(evidence.headings.some(heading => heading.source_id === evidence.paragraphs[2].heading_source_id));
  assert.equal(evidence.pagination.status, 'observed');
  assert.equal(evidence.pagination.current[0].text, '第 2 页');
  assert.equal(evidence.pagination.links.length, 2);
  const noMarker = await read('<p>共 8 款</p><button>加载更多</button><script>let page=5</script>');
  assert.equal(noMarker.pagination.status, 'unknown');
  assert.deepEqual(noMarker.pagination.current, []);
});

test('document and locator entry points preserve exact scope and frame source without mixing iframe contents', async () => {
  const page = await context.newPage();
  try {
    await page.goto(origin);
    const main = await page.mainFrame().evaluate(captureDOMEvidence, { sourcePrefix: 'main/f1' });
    const childFrame = page.frames().find(frame => frame !== page.mainFrame());
    await childFrame.waitForLoadState();
    const child = await childFrame.evaluate(captureDOMEvidence, { sourcePrefix: 'main/f2' });
    assert.equal(main.url, origin + '/');
    assert.equal(child.url, origin + '/child');
    assert.equal(child.title, 'Child source');
    assert.ok(!main.text.includes('C-2'));
    assert.deepEqual(child.definitions[0].groups[0].descriptions.map(item => item.text), ['C-2']);
    assert.match(child.definitions[0].groups[0].source_id, /^main\/f2\//);
    const subtree = await childFrame.locator('dl').evaluate(captureDOMEvidence, { sourcePrefix: 'extract/f2' });
    assert.equal(subtree.scope, 'subtree');
    assert.equal(subtree.headings.length, 0);
    assert.ok(!subtree.text.includes('Child record'));
    const field = await childFrame.locator('dd').first().evaluate(captureDOMEvidence, { observationId: 'field' });
    assert.equal(field.text, 'C-2');
  } finally { await page.close(); }
});

test('open shadow structure is observed without a page hook or actionable source refs', async () => {
  const page = await context.newPage();
  try {
    await page.setContent('<!doctype html><div id="host"></div>');
    await page.locator('#host').evaluate(host => { host.attachShadow({ mode: 'open' }).innerHTML = '<dl><dt>重量</dt><dd>450 g</dd></dl>'; });
    const evidence = await page.evaluate(captureDOMEvidence, { observationId: 'shadow' });
    assert.equal(evidence.definitions[0].groups[0].descriptions[0].text, '450 g');
    assert.match(evidence.definitions[0].source_id, /^shadow\/definition/);
    assert.ok(!evidence.definitions[0].source_id.match(/^e\d+$/));
  } finally { await page.close(); }
});

test('large structures expose partial coverage and respect every output limit', async () => {
  const cells = '<td>' + '长'.repeat(350) + '</td>';
  const rows = '<tr>' + cells.repeat(15) + '</tr>';
  const tables = '<table><tr>' + '<th>字段</th>'.repeat(15) + '</tr>' + rows.repeat(45) + '</table>';
  const evidence = await read(tables.repeat(10) + '<dl>' + '<dt>字段</dt><dd>值</dd>'.repeat(50) + '</dl>' + '<h2>标题</h2>'.repeat(20), { maxStructureChars: 16000 });
  assert.equal(evidence.truncated, true);
  assert.equal(evidence.status, 'partial');
  assert.ok(evidence.tables.length <= 8);
  assert.ok(evidence.headings.length <= 16);
  for (const table of evidence.tables) {
    assert.ok(table.header_rows.length + table.rows.length <= 40);
    for (const row of [...table.header_rows, ...table.rows]) {
      assert.ok(row.cells.length <= 12);
      for (const cell of row.cells) assert.ok(cell.text.length <= 300);
    }
  }
  assert.ok(evidence.text.length <= 8000);
  assert.ok(JSON.stringify(evidence).length < 150000);
  const limited = await read('<dl>' + '<dt>字段</dt><dd>值</dd>'.repeat(50) + '</dl>', { maxTextChars: 1 });
  assert.equal(limited.definitions[0].groups.length, 40);
  assert.equal(limited.definitions[0].truncated, true);
});

test('total structure budgets keep whole rows and field groups; individually shortened excerpts are explicitly marked', async () => {
  const table = await read('<table><tr><th>ID</th><th>重量</th></tr><tr><td>A</td><td>1250 g</td></tr></table>', { maxStructureChars: 9 });
  assert.equal(table.tables[0].header_rows.length, 1);
  assert.equal(table.tables[0].rows.length, 0, 'a row must be retained or omitted as a whole');
  assert.equal(table.tables[0].truncated, true);
  assert.equal(table.status, 'partial');
  const fields = await read('<dl><dt>重量</dt><dd>1250 g</dd></dl>', { maxStructureChars: 7 });
  assert.equal(fields.definitions[0].groups.length, 0, 'a definition group must be retained or omitted as a whole');
  assert.equal(fields.definitions[0].truncated, true);
  const shortened = await read('<table><tr><th>ID</th><th>重量</th></tr><tr><td>A</td><td>1250 g</td></tr></table>', { maxCellChars: 3 });
  assert.equal(shortened.tables[0].rows[0].cells[1].text, '125');
  assert.equal(shortened.tables[0].rows[0].cells[1].truncated, true, 'shortened text must never become an exact numeric source');
  assert.equal(shortened.tables[0].rows[0].cells[1].text_truncated, true);
  assert.equal(shortened.tables[0].column_mapping, 'unknown');
  const full = await read('<p>期限 12 天。</p>', { full: true });
  assert.equal(full.limits.structure_chars, 28000);
  assert.equal(full.status, 'observed');
});

test('metadata limits cover long source paths and empty-cell tables while retaining only whole rows', async () => {
  const row = '<tr>' + '<td></td>'.repeat(12) + '</tr>';
  const table = '<table><tr>' + '<th>列</th>'.repeat(12) + '</tr>' + row.repeat(39) + '</table>';
  const evidence = await read(table.repeat(8), { sourcePrefix: 'source'.repeat(32) });
  assert.equal(evidence.status, 'partial');
  assert.equal(evidence.truncated, true);
  assert.ok(JSON.stringify(evidence).length <= 64000);
  for (const table of evidence.tables) {
    for (const row of table.rows) assert.equal(row.cells.length, 12);
  }
});

test('pre-site captured DOM probe ignores website method and cell getter overrides without extra side effects', async () => {
  const protectedContext = await browser.newContext();
  const key = '__evidence_test_probe__';
  await protectedContext.addInitScript({ content: `(${installEvidenceProbe.toString()})({key:${JSON.stringify(key)},capture:(${captureDOMEvidence.toString()})});` });
  const page = await protectedContext.newPage();
  try {
    await page.goto(origin + '/protected');
    assert.equal(await page.evaluate(() => probeInstalledBeforeWebsite), true);
    const before = await page.evaluate(() => overriddenDOMCalls);
    const evidence = await page.mainFrame().evaluate(({ key }) => globalThis[key](null, { sourcePrefix: 'protected/main' }), { key });
    assert.equal(await page.evaluate(() => overriddenDOMCalls), before);
    assert.equal(evidence.url, origin + '/protected');
    assert.equal(evidence.title, 'Protected source');
    assert.deepEqual(evidence.tables[0].rows[0].cells.map(cell => cell.text), ['A-1', '450 g']);
    assert.equal(evidence.tables[0].rows[0].cells[1].rowspan, 1);
    assert.equal(evidence.tables[0].rows[0].cells[1].colspan, 1);
    assert.equal(evidence.tables[0].column_mapping, 'simple');
    assert.ok(!JSON.stringify(evidence).includes('9999'));
    assert.equal(evidence.definitions[0].groups[0].descriptions[0].text, '12 天');
    assert.equal(evidence.paragraphs[0].text, '日志保留 24 天。');
    const subtree = await page.locator('dl').evaluate((el, { key }) => globalThis[key](el, { sourcePrefix: 'protected/subtree' }), { key });
    assert.equal(subtree.scope, 'subtree');
    assert.equal(subtree.definitions[0].groups[0].descriptions[0].text, '12 天');
    assert.equal(await page.evaluate(() => overriddenDOMCalls), before);
    const descriptor = await page.evaluate(({ key }) => { const value=Object.getOwnPropertyDescriptor(globalThis,key);return {writable:value.writable,configurable:value.configurable}; }, { key });
    assert.deepEqual(descriptor, { writable: false, configurable: false });
  } finally { await protectedContext.close(); }
});

test('pre-site captured probe is initialized in child frames and captures their own native sources', async () => {
  const protectedContext = await browser.newContext();
  const key = '__evidence_test_probe__';
  await protectedContext.addInitScript({ content: `(${installEvidenceProbe.toString()})({key:${JSON.stringify(key)},capture:(${captureDOMEvidence.toString()})});` });
  const page = await protectedContext.newPage();
  try {
    await page.goto(origin);
    const child = page.frames().find(frame => frame !== page.mainFrame());
    await child.waitForLoadState();
    const evidence = await child.evaluate(({ key }) => globalThis[key](null, { sourcePrefix: 'protected/child' }), { key });
    assert.equal(evidence.url, origin + '/child');
    assert.equal(evidence.definitions[0].groups[1].descriptions[0].text, '0.45 kg');
  } finally { await protectedContext.close(); }
});
