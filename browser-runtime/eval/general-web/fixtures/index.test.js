import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { createWorker } from '../../../src/server.js';
import { createGeneralWebFixtures, TASKS, judgeRecordedGeneralWebCase } from './index.js';

let browser, worker, workerURL, workerTask;
const workerToken = randomUUID();
before(async () => {
  browser = await chromium.launch({ headless: true });
  const isolatedBrowser = { on: (...args) => browser.on(...args), isConnected: () => browser.isConnected(), async newContext(options) {
    const task = workerTask, context = await browser.newContext({ ...options, serviceWorkers: 'block' });
    await context.route('**/*', route => new URL(route.request().url()).origin === task.allowedOrigins[0] && new URL(route.request().url()).pathname.startsWith(task.allowedPathPrefixes[0]) ? route.continue() : route.abort());
    return context;
  } };
  worker = await createWorker({ token: workerToken, browser: isolatedBrowser, actionTimeout: 10000, settleMs: 100, idleMs: 60000 });
  workerURL = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => { await worker?.close(); await browser?.close(); });

async function workerReference(fixture, taskID, { onlyCompared = false } = {}) {
  workerTask = fixture.tasks.find(t => t.id === taskID);
  const sessionID = randomUUID(), plan = fixture.referenceData(taskID), actions = [];
  const request = async (suffix, method = 'GET', body) => {
    const response = await fetch(`${workerURL}/sessions/${sessionID}${suffix}`, { method, headers: { Authorization: `Bearer ${workerToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
    const data = await response.json();
    assert.equal(response.status, 200);
    assert.equal(data.ok, true, data.error);
    return data;
  };
  await request('', 'PUT', {});
  try {
    await request('/execute', 'POST', { op_id: randomUUID(), action: 'navigate', url: workerTask.startURL });
    for (const [index, args] of plan.actions.entries()) {
      if (index > 0 && args.action === 'snapshot') continue;
      if (onlyCompared && args.action === 'navigate' && args.url.includes('/products/') && !plan.compared.some(p => args.url.endsWith('/products/' + p.id))) continue;
      const result = await request('/execute', 'POST', { op_id: randomUUID(), ...args });
      actions.push({ ...args, ok: result.ok, result });
    }
  } catch (error) { await request('', 'DELETE'); throw error; }
  return { actions, answer: plan.answer, context: { close: () => request('', 'DELETE') } };
}

async function reference(fixture, taskID, { automaticOnly = false } = {}) {
  if (automaticOnly) return workerReference(fixture, taskID);
  const context = await browser.newContext({ serviceWorkers: 'block' });
  const task = fixture.tasks.find(t => t.id === taskID);
  await context.route('**/*', route => new URL(route.request().url()).origin === task.allowedOrigins[0] && new URL(route.request().url()).pathname.startsWith(task.allowedPathPrefixes[0]) ? route.continue() : route.abort());
  const page = await context.newPage(), actions = [], plan = fixture.referenceData(taskID);
  await page.goto(task.startURL);
  for (const [index, args] of plan.actions.entries()) {
    if (automaticOnly && index > 0 && args.action === 'snapshot') continue;
    if (args.action === 'snapshot') {
      actions.push({ ...args, ok: true, result: { ok: true, url: page.url(), text: await page.locator('body').innerText() } });
      continue;
    }
    if (args.action === 'navigate') await page.goto(args.url);
    else if (args.action === 'type') await page.locator(args.target).fill(args.text);
    else if (args.action === 'select') await page.locator(args.target).selectOption(args.text);
    else if (args.action === 'click') {
      if (args.target === '#more') {
        await Promise.all([page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/catalog')), page.locator(args.target).click()]);
        await page.locator(`a[href="${plan.base}/products/${plan.chosen.id}"]`).first().waitFor();
      } else await page.locator(args.target).click();
    } else throw new Error('unsupported fixture reference action');
    await page.waitForLoadState('load');
    actions.push({ ...args, ok: true, result: { ok: true, url: page.url(), text: await page.locator('body').innerText() } });
  }
  return { context, page, actions, answer: plan.answer };
}

test('manifest exposes only a URL and business goal; each family owns a distinct origin', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 });
  try {
    assert.equal(TASKS.length, 9);
    assert.deepEqual(Object.values(fixture.origins).length, 3);
    assert.equal(new Set(Object.values(fixture.origins)).size, 3);
    assert.equal(await fixture.listen().then(value => Object.values(value).length), 3);
    for (const task of fixture.tasks) {
      assert.match(task.prompt, /^起始网址：http:\/\//);
      assert.equal(task.prompt, `起始网址：${task.startURL}\n业务目标：${task.goal}`);
      assert.equal(new URL(task.startURL).origin, fixture.origins[task.family]);
      assert.deepEqual(task.allowedOrigins, [fixture.origins[task.family]]);
      for (const hidden of ['expected', 'oracle', 'solve', 'actions', 'selectors', 'token', 'api_path']) assert.equal(hidden in task, false);
      assert.doesNotMatch(task.goal, /JSON|#[a-zA-Z]|\/api\/|token|selector/);
    }
  } finally { await fixture.close(); }
});

test('seed reproduces business data but changes names, values, controls and layout across seeds', async () => {
  const a = await createGeneralWebFixtures({ seed: 731 }), b = await createGeneralWebFixtures({ seed: 731 }), c = await createGeneralWebFixtures({ seed: 732 });
  try {
    for (const task of a.tasks) {
      const htmlA = await (await fetch(task.startURL)).text(), htmlB = await (await fetch(b.startURLFor(task.id))).text();
      assert.equal(htmlA.replaceAll(a.origins[task.family], '<origin>'), htmlB.replaceAll(b.origins[task.family], '<origin>'));
      const dataA = a.referenceData(task.id), dataC = c.referenceData(task.id);
      assert.notEqual(dataA.answer.replaceAll(a.origins[task.family], '<origin>'), dataC.answer.replaceAll(c.origins[task.family], '<origin>'), 'business facts change for every task');
      assert.notEqual(htmlA, await (await fetch(c.startURLFor(task.id))).text(), 'names, labels or layout change');
    }
  } finally { await Promise.all([a.close(), b.close(), c.close()]); }
});

test('business HTTP cannot disclose evaluator code, answers or controller state', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 831 });
  try {
    for (const task of fixture.tasks) {
      const origin = new URL(task.startURL).origin;
      for (const suffix of ['/oracle', '/judge', '/expected', '/controller', '/eval', '/__inspect', '/fixtures/index.js', '/general-web/fixtures/index.js']) {
        assert.equal((await fetch(origin + suffix)).status, 404);
        assert.equal((await fetch(task.startURL.slice(0, -1) + suffix)).status, 404);
      }
      const source = await (await fetch(task.startURL)).text();
      assert.doesNotMatch(source, /expectedAnswer|referenceActions|judgeCase|expectedRecord|cited_semantic_fact|successfulSubmissions/);
      assert.equal(fixture.judge(task.id, { answer: fixture.expectedAnswer(task.id), actions: [] }).success, false);
    }
    assert.throws(() => fixture.inspect('unknown'));
  } finally { await fixture.close(); }
});

for (const seed of [731, 732]) for (const task of TASKS) test(`real Chromium reference / seed ${seed} / ${task.id}`, { timeout: 60000 }, async () => {
  const fixture = await createGeneralWebFixtures({ seed });
  let result;
  try {
    result = await reference(fixture, task.id);
    const judged = fixture.judge(task.id, result);
    assert.equal(judged.success, true, judged.reason);
    const inspected = fixture.inspect(task.id);
    assert.ok(inspected.audit.some(a => a.browser));
    assert.equal(inspected.duplicateSubmissions, 0);
    if (task.family === 'workbench') {
      assert.equal(inspected.successfulSubmissions, 1);
      assert.equal(inspected.submissions, task.id === 'workbench-recover' ? 2 : 1);
      assert.equal(inspected.validationErrors.length, task.id === 'workbench-recover' ? 1 : 0);
    }
    assert.equal(fixture.judge(task.id, { ...result, actions: [] }).success, false, 'state and a correct answer cannot replace actual observed actions');
    assert.equal(fixture.judge(task.id, { ...result, actions: result.actions.map(a => ({ ...a, result: { ...a.result, text: '' } })) }).success, false, 'empty explicit and automatic reads cannot prove business observations');
    assert.equal(fixture.judge(task.id, { ...result, answer: '已完成任务，一切正确。' }).success, false);
    fixture.reset(task.id);
    assert.equal(fixture.judge(task.id, result).success, false, 'fresh case rejects old evidence and state');
  } finally { await result?.context.close(); await fixture.close(); }
});

for (const seed of [731, 732]) for (const task of TASKS) test(`automatic browser observations without redundant snapshots / seed ${seed} / ${task.id}`, { timeout: 60000 }, async () => {
  const fixture = await createGeneralWebFixtures({ seed }); let result;
  try {
    result = await reference(fixture, task.id, { automaticOnly: true });
    assert.equal(result.actions.filter(a => a.action === 'snapshot').length, 1);
    assert.equal(fixture.judge(task.id, result).success, true);
    const recordedState = fixture.inspect(task.id), startURL = fixture.startURLFor(task.id);
    const replay = judgeRecordedGeneralWebCase(task.id, seed, startURL, result.answer, result.actions, recordedState);
    assert.equal(replay.success, true);
    assert.equal(replay.run_validity, 'not_assessed');
    assert.equal(replay.review_scope, 'business_evidence_only');
    assert.equal(judgeRecordedGeneralWebCase(task.id, seed, startURL, result.answer, result.actions.map(a => ({ ...a, ok: false })), recordedState).success, false);
    assert.equal(judgeRecordedGeneralWebCase(task.id, seed, startURL, result.answer, result.actions.map(a => ({ ...a, result: { ...a.result, text: '' } })), recordedState).success, false);
    assert.equal(judgeRecordedGeneralWebCase(task.id, seed, startURL, result.answer, result.actions.map(a => ({ ...a, result: { ...a.result, url: 'http://127.0.0.1:1/cases/other/' } })), recordedState).success, false);
  } finally { await result?.context.close(); await fixture.close(); }
});

test('offline recorded-case review rejects case/seed/layout/audit mismatch without HTTP', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await reference(fixture, 'catalog-compare', { automaticOnly: true });
    const id = 'catalog-compare', state = fixture.inspect(id), startURL = fixture.startURLFor(id);
    await fixture.close();
    assert.equal(judgeRecordedGeneralWebCase(id, 731, startURL, result.answer, result.actions, state).success, true);
    for (const corrupted of [{ ...state, seed: 732 }, { ...state, caseID: 'catalog-fit' }, { ...state, family: 'docs' }, { ...state, layout_variant: (state.layout_variant + 1) % 6 }]) {
      assert.equal(judgeRecordedGeneralWebCase(id, 731, startURL, result.answer, result.actions, corrupted).success, false);
    }
    assert.equal(judgeRecordedGeneralWebCase(id, 731, startURL.replace(id, 'catalog-fit'), result.answer, result.actions, state).success, false);
    assert.equal(judgeRecordedGeneralWebCase(id, 731, startURL, result.answer, result.actions, { ...state, audit: state.audit.map(a => ({ ...a, browser: false })) }).success, false);
  } finally { await result?.context.close(); await fixture.close(); }
});

test('absolute product prices and comparison deltas have different meanings', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await reference(fixture, 'catalog-compare', { automaticOnly: true });
    const d = fixture.referenceData('catalog-compare'), [a, b] = d.compared, delta = b.price - a.price;
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${a.name}价格便宜¥${delta}，价差${delta}元。` }).success, true);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${a.name}价格更低（¥${a.price} 比 ¥${b.price} 便宜${delta}元）。` }).success, true);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${a.name}价格便宜¥${delta + 1}。` }).success, false);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${a.name}价格999元。` }).success, false);
    const shortA = a.name.split(' ')[0], shortB = b.name.split(' ')[0];
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n预算：${shortA}¥${a.price}，${shortB}¥${b.price}。\n重量：${shortA}${a.weight}克<${shortB}${b.weight}克（轻${b.weight - a.weight}克）。` }).success, true);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${shortA}¥999。` }).success, false);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${shortA}999克。` }).success, false);
  } finally { await result?.context.close(); await fixture.close(); }
});

test('cheapest-catalog proof rejects an unfiltered 8-of-9 partial catalogue even with correct chosen facts', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }), context = await browser.newContext();
  try {
    const id = 'catalog-paged', data = fixture.referenceData(id), page = await context.newPage(), actions = [];
    const observe = async args => actions.push({ ...args, ok: true, result: { ok: true, url: page.url(), text: await page.locator('body').innerText() } });
    await page.goto(data.startURL); await observe({ action: 'snapshot' });
    await Promise.all([page.waitForResponse(r => new URL(r.url()).pathname.endsWith('/catalog')), page.locator('#more').click()]);
    await page.locator(`a[href="${data.base}/products/${data.chosen.id}"]`).first().waitFor();
    await observe({ action: 'click', target: '#more' });
    assert.equal(await page.locator('#more').count(), 1, 'one unseen product remains');
    await page.goto(data.origin + data.base + '/products/' + data.chosen.id); await observe({ action: 'navigate', url: page.url() });
    const judged = fixture.judge(id, { answer: data.answer, actions });
    assert.equal(judged.success, false);
    assert.ok(judged.failures.includes('catalog_candidates_not_fully_observed'));
  } finally { await context.close(); await fixture.close(); }
});

test('reader proof requires observing all budget-eligible details, not only oracle winners', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await workerReference(fixture, 'catalog-fit', { onlyCompared: true });
    const judged = fixture.judge('catalog-fit', result);
    assert.equal(judged.success, false);
    assert.ok(judged.failures.includes('eligible_reader_detail_coverage_missing'));
    assert.ok(!judged.failures.includes('catalog_candidates_not_fully_observed'));
  } finally { await result?.context.close(); await fixture.close(); }
});

for (const id of ['catalog-compare', 'catalog-paged']) test(`archived second seed catalogue answer regression / ${id}`, async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/catalog-seed04.json', import.meta.url), 'utf8'));
  const record = records.cases.find(c => c.id === id);
  const reviewed = judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, record.answer, record.actions, record.server_state);
  assert.equal(reviewed.success, true, reviewed.reason);
  assert.equal(record.original_status, 'failed');
});

test('archived transposed-reader answer preserves entity/field associations', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/transposed-reader.json', import.meta.url), 'utf8'));
  const record = records.cases[0];
  const review = answer => judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, answer, record.actions, record.server_state);
  assert.equal(review(record.answer).success, true);
  assert.equal(review(record.answer.replace('| **价格** | ¥264 | ¥312 |', '| **价格** | ¥312 | ¥264 |')).success, false);
  assert.equal(review(record.answer.replace('| **重量** | 313 克 | 403 克 |', '| **重量** | 403 克 | 313 克 |')).success, false);
  assert.equal(review(record.answer.replace('| **防水** | 支持 | 支持 |', '| **防水** | 不支持 | 支持 |')).success, false);
});

test('historical validation rejection is not final failure; explicit wrong old task mappings remain answer errors', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/recovery-incidental-error.json', import.meta.url), 'utf8'));
  const record = records.cases[0];
  const review = answer => judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, answer, record.actions, record.server_state);
  const original = review(record.answer);
  assert.equal(original.core_business_success, true, original.reason);
  assert.equal(original.answer_accuracy, false);
  assert.equal(original.success, false);
  assert.equal(original.incidental_rule_version, 'task-record-reference-v1');
  assert.deepEqual(original.incidental_answer_errors.map(error => error.record_id).sort(), ['T-581', 'T-582']);
  const correctMappings = record.server_state.records.filter(r => ['T-581', 'T-582', 'T-583'].includes(r.id)).map(r => `${r.id} ${r.title}`).join('、');
  const corrected = record.answer.replace(/- 列表页核验：[^\n]*/, `- 列表页核验：原先三条任务为${correctMappings}；只有一条新增任务。`);
  assert.equal(review(corrected).success, true);
  assert.equal(review(corrected).answer_accuracy, true);
  const omitted = record.answer.replace(/- 列表页核验：[^\n]*/, '- 已核验新任务详情和列表，只有一条新增记录。');
  assert.equal(review(omitted).success, true);
  assert.equal(review(omitted + '\n当前任务尚未保存成功。').success, false);
  assert.equal(review(omitted.replace('最终截止日期：2026-11-21', '最终截止日期：2026-11-18')).success, false);
});

test('a closed list of old task IDs does not attribute a later new title to its last ID', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/create-id-list.json', import.meta.url), 'utf8'));
  const record = records.cases[0];
  const review = answer => judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, answer, record.actions, record.server_state);
  assert.equal(review(record.answer).success, true);
  assert.equal(review(record.answer).answer_accuracy, true);
  assert.deepEqual(review(record.answer).incidental_answer_errors, []);
  const old = record.server_state.records.filter(r => !r.title.startsWith('客户资料核对'));
  assert.equal(review(record.answer + `\n旧任务映射：${old[0].id}（${old[0].title}）。`).success, true);
  const wrong = review(record.answer + `\n旧任务映射：${old[0].id}（${old[1].title}）。`);
  assert.equal(wrong.core_business_success, true);
  assert.equal(wrong.answer_accuracy, false);
  assert.equal(wrong.success, false);
  assert.equal(wrong.incidental_answer_errors[0].record_id, old[0].id);
});

test('candidate price ranges describe the observed set rather than one recommended product', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/model-v2-claims.json', import.meta.url), 'utf8')), row = records.cases.find(row => row.id === 'catalog-paged');
  const review = answer => judgeRecordedGeneralWebCase(row.id, row.seed, row.startURL, answer, row.actions, row.server_state);
  assert.equal(review(row.answer).success, true);
  assert.equal(review(row.answer).checker_version, 'general-web-checker-v3');
  assert.equal(review(row.answer.replace('¥206–¥298', '¥206–¥999')).success, false);
  assert.equal(review(row.answer + '\n暮山 营地灯 8售价999元。').success, false);
  const unknown = review(row.answer + '\n暮山 营地灯 8\n数据概要：¥10–¥999。');
  assert.equal(unknown.core_business_success, true);
  assert.equal(unknown.answer_accuracy, null);
  assert.equal(unknown.success, false);
  assert.equal(unknown.answer_claim_assessment.needs_review, true);
});

test('transposed tables retain empty internal header cells and reject crossed parameter cells', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/model-v2-claims.json', import.meta.url), 'utf8')), row = records.cases.find(row => row.id === 'catalog-fit');
  const review = answer => judgeRecordedGeneralWebCase(row.id, row.seed, row.startURL, answer, row.actions, row.server_state);
  assert.equal(review(row.answer).success, true);
  assert.equal(review(row.answer.replace('| 价格 | **¥269** | ¥317 |', '| 价格 | **¥317** | ¥269 |')).success, false);
  assert.equal(review(row.answer.replace('| 重量 | 314 克 | 404 克 |', '| 重量 | 404 克 | 314 克 |')).success, false);
  assert.equal(review(row.answer.replace('| 防水 | 支持 | 支持 |', '| 防水 | 不支持 | 支持 |')).success, false);
});

test('historical rejection and an edit-before owner are distinct from verified current fields', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/model-v2-claims.json', import.meta.url), 'utf8'));
  for (const id of ['workbench-recover', 'workbench-update']) {
    const row = records.cases.find(row => row.id === id), review = answer => judgeRecordedGeneralWebCase(row.id, row.seed, row.startURL, answer, row.actions, row.server_state);
    assert.equal(review(row.answer).success, true, review(row.answer).reason);
    assert.equal(review(row.answer + '\n当前任务未保存成功。').success, false);
    if (id === 'workbench-update') {
      assert.equal(review(row.answer + '\n保存后负责人赵宁。').success, false);
      const badHistory = review(row.answer.replace('编辑前为：负责人赵宁', '编辑前为：负责人陈舟'));
      assert.equal(badHistory.core_business_success, true);
      assert.equal(badHistory.answer_accuracy, false);
    }
  }
});

test('retention numbers can be correct while explicit three-entity ordering remains inaccurate', async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/model-v2-claims.json', import.meta.url), 'utf8')), row = records.cases.find(row => row.id === 'docs-retention');
  const review = answer => judgeRecordedGeneralWebCase(row.id, row.seed, row.startURL, answer, row.actions, row.server_state);
  const actual = review(row.answer);
  assert.equal(actual.core_business_success, true, actual.reason);
  assert.equal(actual.answer_accuracy, false);
  assert.equal(actual.success, false);
  assert.ok(actual.incidental_answer_errors.some(error => error.type === 'retention_order_mismatch'));
  const corrected = row.answer.replace('操作日志最短（24 天），审计归档最长（2 年），服务备份居中（12 天）', '操作日志居中（24 天），审计归档最长（2 年），服务备份最短（12 天）');
  assert.equal(review(corrected).success, true, review(corrected).reason);
  assert.equal(review(row.answer.replace('三类期限互不相同：操作日志最短（24 天），审计归档最长（2 年），服务备份居中（12 天）。', '')).success, true);
});

for (const id of ['catalog-compare', 'catalog-paged', 'catalog-fit']) test(`archived first model answer regression / ${id}`, async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/catalog-first3.json', import.meta.url), 'utf8'));
  const record = records.cases.find(c => c.id === id);
  assert.ok(record);
  const reviewed = judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, record.answer, record.actions, record.server_state);
  assert.equal(reviewed.success, true, reviewed.reason);
  assert.equal(record.original_status, 'failed');
  assert.equal(reviewed.run_validity, 'not_assessed');
});

for (const id of ['workbench-recover', 'workbench-update']) test(`archived workbench answer regression / ${id}`, async () => {
  const records = JSON.parse(await readFile(new URL('./regressions/workbench-regressions.json', import.meta.url), 'utf8'));
  const record = records.cases.find(c => c.id === id);
  const reviewed = judgeRecordedGeneralWebCase(record.id, record.seed, record.startURL, record.answer, record.actions, record.server_state);
  assert.equal(reviewed.success, true, reviewed.reason);
  assert.equal(record.original_status, 'failed');
  assert.equal(reviewed.run_validity, 'not_assessed');
});

test('quoted rejection dates and no unfinished items differ from wrong final dates and an unverified task', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let recovered, updated;
  try {
    recovered = await reference(fixture, 'workbench-recover', { automaticOnly: true });
    const r = fixture.referenceData('workbench-recover');
    const quote = `任务编号${r.expectedRecord.id}，最终截止日期${r.correctedDate}（系统提示“排期容量已满：${r.unavailableDate}不可用。最近可用日期为${r.correctedDate}”，按指引调整保存）。`;
    assert.equal(fixture.judge('workbench-recover', { ...recovered, answer: quote }).success, true);
    const wrongFinal = `任务编号${r.expectedRecord.id}，最终截止日期${r.unavailableDate}，系统建议日期为${r.correctedDate}。`;
    assert.equal(fixture.judge('workbench-recover', { ...recovered, answer: wrongFinal }).success, false);
    updated = await reference(fixture, 'workbench-update', { automaticOnly: true });
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: updated.answer + '所有授权修改项均与目标一致，无未核验事项。' }).success, true);
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: updated.answer + '任务尚未核验。' }).success, false);
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: updated.answer + '任务未经核验。' }).success, false);
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: updated.answer + '任务没有完成。' }).success, false);
  } finally { await recovered?.context.close(); await updated?.context.close(); await fixture.close(); }
});

test('catalog values must be associated with their correct product; swapped numbers fail', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await reference(fixture, 'catalog-compare');
    const [a, b] = fixture.referenceData('catalog-compare').compared;
    const swapped = result.answer.replace(`${a.weight}克`, 'SWAP_WEIGHT').replace(`${b.weight}克`, `${a.weight}克`).replace('SWAP_WEIGHT', `${b.weight}克`);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: swapped }).success, false);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer + `\n${a.name}的价格是999元，重量999克。` }).success, false);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: result.answer.replace(`推荐选择${a.name}`, `不推荐选择${a.name}；应选择${b.name}`) }).success, false);
    const oneParagraph = `推荐选择${a.name}。${[a, b].map(p => `${p.name}：价格${p.price}元，重量${p.weight}克，续航${p.battery}小时；详情${fixture.referenceData('catalog-compare').origin + fixture.referenceData('catalog-compare').base}/products/${p.id}`).join('；')}。`;
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: oneParagraph }).success, true, 'semicolon-separated product facts stay associated with each product');
    const crossedParagraph = oneParagraph.replace(`${a.weight}克`, 'CROSS_WEIGHT').replace(`${b.weight}克`, `${a.weight}克`).replace('CROSS_WEIGHT', `${b.weight}克`);
    assert.equal(fixture.judge('catalog-compare', { ...result, answer: crossedParagraph }).success, false, 'crossed facts remain invalid within the same paragraph');
  } finally { await result?.context.close(); await fixture.close(); }
});

test('natural Markdown tables preserve product facts and the recommendation', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let compare, reader;
  try {
    compare = await reference(fixture, 'catalog-compare');
    const d = fixture.referenceData('catalog-compare');
    const answer = `推荐选择${d.chosen.name}。\n\n| 型号 | 价格 | 重量 | 续航 | 详情 |\n| --- | --- | --- | --- | --- |\n${d.compared.map(p => `| ${p.name} | ${p.price}元 | ${p.weight}克 | ${p.battery}小时 | [详情](${d.origin + d.base}/products/${p.id}) |`).join('\n')}`;
    assert.equal(fixture.judge('catalog-compare', { ...compare, answer }).success, true);
    reader = await reference(fixture, 'catalog-fit');
    const r = fixture.referenceData('catalog-fit');
    const readerAnswer = `推荐选择${r.chosen.name}。\n| 名称 | 价格(元) | 重量(g) | PPI | 防水 | 详情 |\n| --- | --- | --- | --- | --- | --- |\n${r.compared.map(p => `| ${p.name} | ${p.price} | ${p.weight} | ${p.resolution} | 支持 | [详情](${r.origin + r.base}/products/${p.id}) |`).join('\n')}`;
    assert.equal(fixture.judge('catalog-fit', { ...reader, answer: readerAnswer }).success, true);
    assert.equal(fixture.judge('catalog-fit', { ...reader, answer: r.answer.replaceAll('防水支持', '防水支持是错误的，两款都不支持防水') }).success, false);
  } finally { await compare?.context.close(); await reader?.context.close(); await fixture.close(); }
});

test('docs accept leading Markdown links, lists and a separate reference list while rejecting material negation', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await reference(fixture, 'docs-policy');
    const d = fixture.referenceData('docs-policy');
    const leading = d.required.map(doc => `[${doc.title}](${d.origin + d.base}/documents/${doc.id})：${doc.sections.map(s => s.text).join(' ')}`).join('\n');
    assert.equal(fixture.judge('docs-policy', { ...result, answer: leading }).success, true);
    const listed = d.required.flatMap(doc => doc.sections.map(s => `- ${s.text}`)).join('\n') + '\n\n参考文档：\n' + d.required.map(doc => `- [${doc.title}](${d.origin + d.base}/documents/${doc.id})`).join('\n');
    assert.equal(fixture.judge('docs-policy', { ...result, answer: listed }).success, true);
    assert.equal(fixture.judge('docs-policy', { ...result, answer: d.answer.replace('需要订单号与物流轨迹截图', '不必提供订单号与物流轨迹截图') }).success, false);
  } finally { await result?.context.close(); await fixture.close(); }
});

test('receipt answer requirements follow the stated business goal and permit original-to-final date explanation', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let recovered, updated;
  try {
    recovered = await reference(fixture, 'workbench-recover');
    const r = fixture.referenceData('workbench-recover');
    assert.equal(fixture.judge('workbench-recover', { ...recovered, answer: `任务编号${r.expectedRecord.id}。原目标截止日期${r.unavailableDate}不可用，最终截止日期${r.correctedDate}；已保存并核验。` }).success, true);
    updated = await reference(fixture, 'workbench-update');
    const u = fixture.referenceData('workbench-update');
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: `任务编号${u.expectedRecord.id}，状态进行中，版本号2。` }).success, true);
    assert.equal(fixture.judge('workbench-update', { ...updated, answer: u.answer.replace(`负责人${u.expectedRecord.owner}`, `负责人不是${u.expectedRecord.owner}，而是其他人`) }).success, false);
  } finally { await recovered?.context.close(); await updated?.context.close(); await fixture.close(); }
});

test('docs reject swapped facts, swapped citations and contradicted rules even when all expected numbers appear', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let result;
  try {
    result = await reference(fixture, 'docs-retention');
    const data = fixture.referenceData('docs-retention'), log = data.facts[0], backup = data.facts[2];
    const swapped = result.answer.replace(`${log.number}天`, 'SWAP_DAYS').replace(`${backup.number}天`, `${log.number}天`).replace('SWAP_DAYS', `${backup.number}天`);
    assert.equal(fixture.judge('docs-retention', { ...result, answer: swapped }).success, false);
    const swappedSources = result.answer.replace(data.required[0].id, 'SWAP_SOURCE').replace(data.required[1].id, data.required[0].id).replace('SWAP_SOURCE', data.required[1].id);
    assert.equal(fixture.judge('docs-retention', { ...result, answer: swappedSources }).success, false);
    assert.equal(fixture.judge('docs-retention', { ...result, answer: result.answer + '\n服务备份永久保留，操作日志留存999天。' }).success, false);
  } finally { await result?.context.close(); await fixture.close(); }
});

test('direct non-browser fetches and fabricated successful reads cannot satisfy browser visits', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 });
  try {
    const data = fixture.referenceData('docs-policy');
    await fetch(data.startURL);
    for (const doc of data.required) await fetch(data.origin + data.base + '/documents/' + doc.id);
    const actions = [{ action: 'navigate', ok: true }, { action: 'snapshot', ok: true, result: { text: data.required.map(d => d.title + d.sections.map(s => s.text).join('')).join('') } }];
    assert.equal(fixture.judge('docs-policy', { answer: data.answer, actions }).success, false);
    assert.ok(fixture.inspect('docs-policy').audit.every(a => !a.browser));
  } finally { await fixture.close(); }
});

test('repeated creates are rejected despite the original valid receipt', async () => {
  const fixture = await createGeneralWebFixtures({ seed: 731 }); let first, second;
  try {
    first = await reference(fixture, 'workbench-create');
    second = await reference(fixture, 'workbench-create');
    const judged = fixture.judge('workbench-create', first);
    assert.equal(judged.success, false);
    assert.equal(judged.duplicate_submissions, 1);
    assert.equal(fixture.inspect('workbench-create').successfulSubmissions, 2);
  } finally { await first?.context.close(); await second?.context.close(); await fixture.close(); }
});
