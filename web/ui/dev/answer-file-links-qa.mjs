import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from '../../../browser-runtime/node_modules/playwright/index.mjs';

const base = process.argv[2] || 'http://127.0.0.1:5166';
assert.ok(['127.0.0.1', 'localhost'].includes(new URL(base).hostname));
assert.ok(!['19777', '5173'].includes(new URL(base).port), 'never use the formal app/dev port');
const output = process.argv[3] || '/tmp/pogo-answer-file-links-' + Date.now();
await fs.mkdir(output, { recursive: true, mode: 0o700 });
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1060 } });
const report = { passed: false, scope: 'Real Vue AnswerText and FilePreview; existing dock helpers with a development provider; isolated mocked HTTP APIs. No actual AgentView router, filesystem, model, website or Office-engine editing acceptance.', checks: [], requests: [], errors: [], blockedExternal: [] };
const check = (name, condition) => { assert.ok(condition, name); report.checks.push(name); console.log('PASS', name); };
const long = 'exports/很长的中文目录/逐月分析与核验资料/中文结果报告.md';
const notes = 'exports/notes.txt';
const text = '# 中文核验报告\n\n合成隔离文件。\n', noteText = 'first line\nsecond line\nthird line\nfourth line\n';
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aQYQAAAAASUVORK5CYII=', 'base64');
const pdf = Buffer.from('%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 100]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF');
let officeEnabled = false;
page.on('pageerror', error => report.errors.push(error.message));
await page.route('**/*', async route => {
  const request = route.request(), url = new URL(request.url());
  if (url.origin !== new URL(base).origin) { report.blockedExternal.push(url.origin); await route.abort(); return; }
  if (!url.pathname.startsWith('/api/')) { await route.continue(); return; }
  const row = { method: request.method(), route: url.pathname, path: url.searchParams.get('path'), body: request.postDataJSON() };
  report.requests.push(row);
  const json = async (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  if (url.pathname === '/api/office/config') { await json(200, { enabled: officeEnabled }); return; }
  if (url.pathname === '/api/office/sessions' && request.method() === 'POST') { await json(503, { error: '隔离测试：已到达 Office 打开入口，未启动真实编辑引擎。' }); return; }
  if (url.pathname === '/api/files/content') {
    const path = url.searchParams.get('path') || '', relative = path.replace(/^users\/qa\//, '');
    if (!path.startsWith('users/qa/')) { await json(403, { error: 'Fixture workspace scope mismatch' }); return; }
    if (relative === 'exports/missing.txt') { await json(404, { error: '文件不存在或已移走，请确认路径。' }); return; }
    const kind = relative === 'exports/picture.png' ? 'image' : relative === 'exports/guide.pdf' ? 'pdf' : 'text';
    if (url.searchParams.get('raw') === '1') { await route.fulfill({ status: 200, contentType: kind === 'image' ? 'image/png' : 'application/pdf', body: kind === 'image' ? png : pdf }); return; }
    if (![long, notes, 'exports/picture.png', 'exports/guide.pdf'].includes(relative)) { await json(404, { error: 'Unknown synthetic file' }); return; }
    await json(200, { path, text: kind === 'text' ? (relative === long ? text : noteText) : '', binary: kind !== 'text', mime: kind === 'image' ? 'image/png' : kind === 'pdf' ? 'application/pdf' : 'text/plain', size: 128, mtime_ms: 1791900000000, truncated: false }); return;
  }
  await json(400, { error: 'Unexpected fixture API route' });
});
const main = page.locator('.qa-main-answer');
const link = path => main.locator(`[data-workspace-file="${path}"]`);
const preview = page.locator('.qa-preview:visible');
const state = () => page.evaluate(() => ({ tabs: window.__answerFileQA.state.tabs, selected: window.__answerFileQA.state.selected, opens: window.__answerFileQA.state.opens, focuses: window.__answerFileQA.state.focuses }));
async function clearTabs() { await page.evaluate(() => window.__answerFileQA.clearTabs()); }
try {
  await page.goto(base + '/dev/answer-file-links.html');
  await link(long).first().waitFor();
  const label = await link(long).first().innerText();
  check('long Chinese inline path displays compact basename', label === '中文结果报告.md' && !label.includes('/'));
  check('full workspace-relative Chinese path remains in tooltip', (await link(long).first().getAttribute('title')).includes(long));
  check('plain-text and explicit Markdown paths render file actions', await link(notes).count() === 2 && await link(long).count() === 2);
  check('external URL retains an ordinary anchor', await main.locator('a[href="https://example.invalid/exports/notes.txt"]').count() === 1);
  check('fenced command remains code without file buttons', await main.locator('pre').innerText().then(value => value.includes('cat exports/notes.txt')) && await main.locator('pre button[data-workspace-file]').count() === 0);
  check('subsheet and shared answers have no navigable file actions', await page.locator('.subsheet button[data-workspace-file],.share-page button[data-workspace-file]').count() === 0);
  await link(long).first().click(); await preview.locator('.markdown-view').waitFor();
  check('click mounts actual FilePreview with correctly scoped API path', report.requests.some(r => r.route === '/api/files/content' && r.path === 'users/qa/' + long) && await preview.locator('.preview').count() === 1);
  await link(long).last().focus(); await page.keyboard.press('Enter');
  check('Enter follows the same file navigation', (await state()).opens.length === 2);
  check('repeated references focus a single existing preview tab', (await state()).tabs.length === 1);
  await link(notes).first().click(); await preview.locator('.code-block').waitFor();
  await link(notes).last().click();
  await page.waitForFunction(() => document.querySelectorAll('.qa-preview .cited-line').length === 2);
  check('Markdown line locator reaches the actual preview range', await preview.locator('.cited-line[data-source-line="2"]').count() === 1 && await preview.locator('.cited-line[data-source-line="3"]').count() === 1);
  await preview.locator('button[title="编辑"]').click();
  const draft = preview.locator('textarea.editor'); await draft.fill('未保存的人工修改');
  const reads = report.requests.filter(r => r.route === '/api/files/content' && r.path === 'users/qa/' + notes).length;
  await link(notes).first().click();
  check('repeat click preserves unsaved text draft', await draft.inputValue() === '未保存的人工修改');
  check('repeat click neither duplicates preview nor silently writes draft', (await state()).tabs.length === 2 && report.requests.filter(r => r.route === '/api/files/content' && r.path === 'users/qa/' + notes).length === reads && !report.requests.some(r => r.method === 'PUT'));
  await link('exports/picture.png').click(); await preview.locator('img').waitFor();
  await page.waitForFunction(() => [...document.querySelectorAll('.qa-preview img')].some(img => img.naturalWidth > 0));
  check('image uses existing raw preview route', report.requests.some(r => r.path === 'users/qa/exports/picture.png') && await preview.locator('img').evaluate(img => img.naturalWidth > 0));
  await link('exports/guide.pdf').click(); await preview.locator('iframe').waitFor();
  check('PDF enters existing sandboxed inline preview', (await preview.locator('iframe').getAttribute('src')).includes('guide.pdf'));
  await link('exports/missing.txt').click(); await preview.getByRole('alert').waitFor();
  check('missing file is explained inside existing preview panel', /找不到此文件|文件不存在/.test(await preview.getByRole('alert').innerText()));
  await link('exports/结果表.xlsx').click(); await preview.getByRole('alert').waitFor();
  check('Office-disabled file gets existing configuration diagnostic', (await preview.getByRole('alert').innerText()).includes('尚未配置在线 Office'));
  await clearTabs(); officeEnabled = true;
  await page.evaluate(() => window.__answerFileQA.reserveOffice());
  const officeCalls = () => report.requests.filter(r => r.route === '/api/office/sessions' && r.method === 'POST');
  await link('exports/结果表.xlsx').click(); await preview.getByRole('alert').waitFor();
  check('existing Office reservation focuses existing editor instead of creating another', (await state()).focuses.length === 1 && officeCalls().length === 0 && (await preview.getByRole('alert').innerText()).includes('另一个页面打开'));
  await link('exports/结果表.xlsx').click();
  check('repeat Office reference has one preview and no second edit claim', (await state()).tabs.length === 1 && officeCalls().length === 0 && await page.locator('.office-editor').count() === 0);
  await page.evaluate(() => window.__answerFileQA.reset());
  await link('exports/结果表.xlsx').click();
  await page.waitForFunction(() => document.querySelector('.qa-preview [role=alert]')?.textContent?.includes('未启动真实编辑引擎'));
  check('enabled Office route carries exact workspace path and conversation', officeCalls().length === 1 && officeCalls()[0].body.path === 'users/qa/exports/结果表.xlsx' && officeCalls()[0].body.conversation_id === 'qa-chat-one');
  await page.evaluate(() => window.__answerFileQA.reset());
  const switched = await page.evaluate(() => window.__answerFileQA.staleClick('conversation'));
  check('stale button cannot navigate after conversation changes before rerender', switched.before === switched.after);
  await page.evaluate(() => window.__answerFileQA.reset());
  const moved = await page.evaluate(() => window.__answerFileQA.staleClick('cwd'));
  check('stale button cannot open a different workspace after cwd changes', moved.before === moved.after);
  await page.evaluate(() => window.__answerFileQA.reset());
  await link(long).first().click(); await preview.locator('.markdown-view').waitFor();
  await page.screenshot({ path: output + '/desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 900 });
  check('long filename does not overflow mobile answer width', await main.evaluate(node => node.scrollWidth <= node.clientWidth + 1));
  await page.screenshot({ path: output + '/mobile.png', fullPage: true });
  check('no Vue runtime exception or external network request', report.errors.length === 0 && report.blockedExternal.length === 0);
  report.passed = true;
} catch (error) { report.error = String(error); await page.screenshot({ path: output + '/failure.png', fullPage: true }).catch(() => {}); }
finally { await browser.close(); await fs.writeFile(output + '/summary.json', JSON.stringify(report, null, 2)); console.log(JSON.stringify({ passed: report.passed, checks: report.checks.length, report: output + '/summary.json', error: report.error })); if (!report.passed) process.exitCode = 1; }
