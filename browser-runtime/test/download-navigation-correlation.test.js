import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import http from 'node:http';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';
import { BrowserDownloads, MAX_DOWNLOAD_BYTES } from '../src/downloads.js';

const downloadURL = 'https://fixture.invalid/report?case=owned';
const frameId = 'owned-top-frame';
function fixture() {
  const context = new EventEmitter();
  Object.assign(context, { setDefaultTimeout() {}, setDefaultNavigationTimeout() {} });
  const session = new BrowserSession(context);
  const page = { url: () => 'https://fixture.invalid/original', isClosed: () => false };
  const frame = { page: () => page }; page.mainFrame = () => frame;
  const meta = { id:'t1', page, version:1, topDocumentVersion:1, documentFrameId:frameId,
    documentIdentityReady:true, pageLoadEpoch:0, pageLoading:false, pageLoadError:'' };
  session.pages.set(meta.id, meta);
  const published = [];
  session.publishLiveState = current => published.push({loading:current.pageLoading, error:current.pageLoadError, epoch:current.pageLoadEpoch});
  const begin = (requestId = 'request-1', url = downloadURL) => {
    session.startPageLoad(meta, {newCycle:true});
    session.trackPageNavigation(meta, {frameId, url, requestId});
    return meta.pageLoadEpoch;
  };
  const fail = (requestId = 'request-1', extra = {}) => session.failPageNavigation(meta,
    {requestId, errorText:'net::ERR_ABORTED', canceled:true, epoch:meta.pageLoadEpoch, ...extra});
  const confirm = extra => session.confirmDownloadNavigation(meta, {frameId, url:downloadURL, ...extra});
  return {context, session, page, frame, meta, published, begin, fail, confirm};
}

for (const order of ['download-first', 'abort-first']) test(`confirmed download ${order} ends navigation without making the retained document unavailable`, () => {
  const h = fixture(); h.begin();
  if (order === 'download-first') { h.confirm(); h.fail(); }
  else { h.fail(); assert.equal(h.meta.pageLoadError, 'Page navigation was cancelled.'); h.confirm(); }
  assert.equal(h.meta.pageLoading, false);
  assert.equal(h.meta.pageLoadError, '');
  assert.equal(h.meta.topDocumentVersion, 1);
  assert.equal(h.page.url(), 'https://fixture.invalid/original');
  assert.deepEqual(h.published.at(-1), {loading:false, error:'', epoch:h.meta.pageLoadEpoch});
});

for (const order of ['download-first', 'failure-first']) test(`a real non-abort error is preserved with ${order} download confirmation`, () => {
  const h = fixture(); h.begin();
  if (order === 'download-first') h.confirm();
  h.fail('request-1', {errorText:'net::ERR_CONNECTION_RESET', canceled:false});
  if (order === 'failure-first') h.confirm();
  assert.equal(h.meta.pageLoadError, 'Page navigation failed.');
  assert.equal(h.meta.pageLoading, false);
});

test('late confirmation for an old same-URL attempt cannot clear a newer genuine failure', () => {
  const h = fixture(); const oldEpoch = h.begin('old'); h.fail('old');
  h.begin('new'); h.fail('new', {errorText:'net::ERR_NAME_NOT_RESOLVED', canceled:false});
  h.confirm();
  assert.equal(h.meta.pageLoadError, 'Page navigation failed.');
  h.fail('old', {epoch:oldEpoch});
  assert.equal(h.meta.pageLoadError, 'Page navigation failed.');
});

test('an old download cannot finish a newer same-URL navigation still loading', () => {
  const h = fixture(); h.begin('old'); h.fail('old'); h.begin('new'); h.confirm();
  assert.equal(h.meta.pageLoading, true);
  assert.equal(h.meta.pageLoadError, '');
  h.fail('new', {errorText:'net::ERR_CONNECTION_REFUSED', canceled:false});
  assert.equal(h.meta.pageLoadError, 'Page navigation failed.');
});

test('frame, full URL and retained document must all match the confirmed download', () => {
  for (const change of [{frameId:'other-frame'}, {url:'https://fixture.invalid/report?case=other'}]) {
    const h = fixture(); h.begin(); h.fail(); h.confirm(change);
    assert.equal(h.meta.pageLoadError, 'Page navigation was cancelled.');
  }
  const h = fixture(); h.begin(); h.fail(); h.meta.topDocumentVersion++;
  h.confirm(); assert.equal(h.meta.pageLoadError, 'Page navigation was cancelled.');
});

test('closed or detached pages cannot clear navigation failures', () => {
  for (const close of [h => { h.session.closed = true; }, h => h.session.pages.delete(h.meta.id)]) {
    const h = fixture(); h.begin(); h.fail(); close(h); h.confirm();
    assert.equal(h.meta.pageLoadError, 'Page navigation was cancelled.');
  }
});

test('Playwright duplicate requestfailed cannot overwrite the CDP-confirmed download', () => {
  const h = fixture(); h.begin(); h.confirm(); h.fail();
  h.session.devtools.clients.set(h.meta.id, {meta:h.meta});
  const request = {failure:() => ({errorText:'net::ERR_ABORTED'})}; h.meta.navigationRequest = request;
  h.context.emit('requestfailed', request);
  assert.equal(h.meta.pageLoadError, '');
});

test('without a CDP client, real Playwright navigation failures still finish the loading state', () => {
  const h = fixture(); h.session.startPageLoad(h.meta, {newCycle:true});
  const request = {failure:() => ({errorText:'net::ERR_CONNECTION_RESET'})}; h.meta.navigationRequest = request;
  h.context.emit('requestfailed', request);
  assert.equal(h.meta.pageLoadError, 'Page navigation failed.'); assert.equal(h.meta.pageLoading, false);
});

async function until(read, label) {
  const end = Date.now() + 6000;
  do { const value = await read(); if (value) return value; await new Promise(resolve => setTimeout(resolve, 15)); } while (Date.now() < end);
  assert.fail(label);
}

test('real Chromium download completion, quota rejection and cancellation preserve the original document and later clicks', {timeout:25000}, async t => {
  const directory = await mkdtemp(join(tmpdir(), 'pogo-download-navigation-'));
  const held = new Set();
  const site = http.createServer((req, res) => {
    if (req.url === '/ok') {
      res.writeHead(200, {'Content-Type':'application/octet-stream', 'Content-Disposition':'attachment; filename="ok.csv"'});
      res.end('a,b\n1,2\n'); return;
    }
    if (req.url === '/oversize' || req.url === '/cancel') {
      res.writeHead(200, {'Content-Type':'application/octet-stream', 'Content-Disposition':`attachment; filename="${req.url.slice(1)}.csv"`,
        'Content-Length':String(req.url === '/oversize' ? MAX_DOWNLOAD_BYTES + 1 : 1024)});
      res.write('a,b\n1,2\n'); held.add(res); res.on('close', () => held.delete(res)); return;
    }
    res.setHeader('Content-Type', 'text/html');
    res.end('<title>Owned download document</title><input id="text"><button id="count" onclick="this.textContent=String(++window.clicks)">Count</button><a id="ok" href="/ok">Download</a><a id="oversize" href="/oversize">Large</a><a id="cancel" href="/cancel">Cancel</a><script>window.documentNonce="original-owned-document";window.clicks=0</script>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  const context = await chromium.launchPersistentContext('', {headless:true, acceptDownloads:false});
  const browser = context.browser();
  let session, downloads;
  t.after(async () => {
    await downloads?.close(); await session?.close(); await browser.close();
    for (const response of held) response.destroy(); site.closeAllConnections();
    await new Promise(resolve => site.close(resolve)); await rm(directory, {recursive:true, force:true});
  });
  session = await new BrowserSession(context, {settleMs:0, actionTimeout:2000}).init();
  const meta = session.page(), page = meta.page;
  const root = session.devtools.clients.get(meta.id); await root.ready;
  const browserCDP = await browser.newBrowserCDPSession();
  let sequence = 0;
  const trace = [], outcomes = [];
  const record = (kind, details) => trace.push({seq:++sequence, at_ms:performance.now(), kind, epoch:meta.pageLoadEpoch, document:meta.topDocumentVersion, page_error:meta.pageLoadError, ...details});
  context.on('requestfailed', request => record('Playwright.requestfailed', {url:request.url(), errorText:request.failure()?.errorText}));
  root.client.on('Network.requestWillBeSent', event => { if (event.type === 'Document') record('Network.requestWillBeSent', {requestId:event.requestId, frameId:event.frameId, url:event.request.url}); });
  root.client.on('Network.loadingFailed', event => record('Network.loadingFailed', {requestId:event.requestId, errorText:event.errorText, canceled:event.canceled}));
  browserCDP.on('Browser.downloadWillBegin', event => record('Browser.downloadWillBegin', {guid:event.guid, frameId:event.frameId, url:event.url}));
  browserCDP.on('Browser.downloadProgress', event => record('Browser.downloadProgress', {guid:event.guid, state:event.state, receivedBytes:event.receivedBytes, totalBytes:event.totalBytes}));
  downloads = await new BrowserDownloads({client:browserCDP, directory,
    ownerForFrame:frame => frame === root.topFrameId ? {tab_id:meta.id, context_ref:session.contextRef, control:session.controlFor(meta).mode} : null,
    onBegin:(event, owner) => { assert.equal(owner.tab_id, meta.id); session.confirmDownloadNavigation(meta, {frameId:event.frameId, url:event.url}); },
  }).start();
  const origin = `http://127.0.0.1:${site.address().port}`;
  await page.goto(origin); await until(() => !meta.pageLoading, 'initial document did not finish loading');
  const document = meta.topDocumentVersion, frameIdentity = session.frameIdentity(meta);
  for (const action of ['ok', 'oversize', 'cancel']) {
    await page.locator('#' + action).click({noWaitAfter:true});
    const item = await until(() => downloads.list().downloads.find(item => item.filename === action + '.csv'), 'download metadata missing: ' + action);
    if (action === 'cancel') await downloads.cancel(item.id);
    await until(() => downloads.items.get(item.id)?.state === (action === 'ok' ? 'completed' : 'failed'), 'download did not settle: ' + action);
    await until(() => !meta.pageLoading && !meta.pageLoadError, 'download left original document unavailable: ' + action);
    const actual = downloads.items.get(item.id);
    assert.equal(actual.error_code, action === 'ok' ? undefined : action === 'oversize' ? 'DOWNLOAD_TOO_LARGE' : 'DOWNLOAD_CANCELED');
    assert.equal(meta.topDocumentVersion, document); assert.equal(session.frameIdentity(meta), frameIdentity);
    assert.equal(await page.evaluate(() => window.documentNonce), 'original-owned-document');
    assert.equal(session.state().page_load_error, undefined);
    await page.locator('#count').click(); await page.locator('#text').fill('after-' + action);
    assert.equal(await page.locator('#text').inputValue(), 'after-' + action);
    outcomes.push({action, state:actual.state, error_code:actual.error_code, document_preserved:true, later_clicks:await page.evaluate(() => window.clicks), input:await page.locator('#text').inputValue()});
    await downloads.acknowledge(item.id);
  }
  assert.equal(outcomes.at(-1).later_clicks, 3);
  assert.ok(trace.some(event => event.kind === 'Network.loadingFailed' && event.errorText === 'net::ERR_ABORTED'));
  assert.ok(trace.some(event => event.kind === 'Playwright.requestfailed' && event.errorText === 'net::ERR_ABORTED'));
  if (process.env.POGO_DOWNLOAD_NAV_TRACE) await writeFile(process.env.POGO_DOWNLOAD_NAV_TRACE,
    JSON.stringify({scope:'Owned loopback Chromium only; not a Wayland Docker acceptance', passed:true, outcomes, trace}, null, 2) + '\n', {mode:0o600, flag:'wx'});
});
