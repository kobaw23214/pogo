import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { mkdtemp, readFile, writeFile, symlink, access, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserDownloads, MAX_DOWNLOAD_BYTES } from '../src/downloads.js';
import { BrowserSession } from '../src/session.js';
const owner = { tab_id: 't1', context_ref: 'ctx_0123456789abcdef01234567', control: 'human' };
async function fixture(t, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'pogo-download-test-'));
  const client = new EventEmitter(); client.calls = []; client.send = async (method, params) => { client.calls.push({ method, params }); };
  const downloads = await new BrowserDownloads({ client, directory, ownerForFrame: frame => frame === 'known' ? owner : null, ...options }).start();
  t.after(() => downloads.close()); return { downloads, client, directory };
}
const begin = (downloads, id = randomUUID(), extra = {}) => { downloads.begin({ guid: id, frameId: 'known', suggestedFilename: '报表.xlsx', url: 'https://alice:secret@example.test/report?token=secret#frag', ...extra }); return id; };
async function bytes(stream) { const chunks = []; for await (const chunk of stream) chunks.push(chunk); return Buffer.concat(chunks); }
async function eventually(check) { for (let i = 0; i < 200; i++) { const result = await check(); if (result) return result; await new Promise(resolve => setTimeout(resolve, 25)); } throw new Error('download condition timed out'); }
test('download handoff is exactly once for accepted and capacity-rejected GUIDs, never for unknown owners', async t => {
  const callbacks = [];
  const { downloads, client } = await fixture(t, { maxItems: 1, onBegin: (event, actualOwner) => callbacks.push({ id: event.guid, owner: actualOwner }) });
  const first = begin(downloads); begin(downloads, first);
  const full = begin(downloads); await Promise.allSettled([...downloads.tasks]); begin(downloads, full);
  const unknown = begin(downloads, randomUUID(), { frameId: 'unbound' });
  await Promise.allSettled([...downloads.tasks]); begin(downloads, unknown, { frameId: 'known' });
  assert.deepEqual(callbacks.map(entry => entry.id), [first, full]);
  assert.ok(callbacks.every(entry => entry.owner === owner));
  assert.equal(downloads.items.size, 1); assert.equal(downloads.rejected, 2);
  assert.equal(client.calls.filter(call => call.method === 'Browser.cancelDownload' && call.params.guid === full).length, 1);
  assert.equal(client.calls.filter(call => call.method === 'Browser.cancelDownload' && call.params.guid === unknown).length, 1);
  await downloads.acknowledge(first); begin(downloads, first);
  assert.equal(downloads.items.size, 0); assert.equal(callbacks.length, 2, 'ACK never enables a replayed begin');
});
test('a capacity-rejected confirmed handoff preserves the initiating page without accepting the file', async t => {
  const context = new EventEmitter(); context.setDefaultTimeout = () => {}; context.setDefaultNavigationTimeout = () => {};
  const session = new BrowserSession(context); session.publishLiveState = () => {};
  const meta = { id: owner.tab_id, page: { url: () => 'https://owned.invalid/page', isClosed: () => false }, topDocumentVersion: 1, pageLoadEpoch: 0, pageLoading: false, pageLoadError: '' };
  session.pages.set(meta.id, meta);
  const { downloads } = await fixture(t, { maxItems: 1, onBegin: event => session.confirmDownloadNavigation(meta, { frameId: event.frameId, url: event.url }) });
  begin(downloads);
  const url = 'https://owned.invalid/file';
  session.startPageLoad(meta, { newCycle: true });
  session.trackPageNavigation(meta, { frameId: 'known', url, requestId: 'capacity-navigation' });
  session.failPageNavigation(meta, { requestId: 'capacity-navigation', errorText: 'net::ERR_ABORTED', canceled: true });
  assert.equal(meta.pageLoadError, 'Page navigation was cancelled.');
  const refused = begin(downloads, randomUUID(), { url });
  await Promise.allSettled([...downloads.tasks]);
  assert.equal(meta.pageLoadError, ''); assert.equal(meta.pageLoading, false); assert.equal(meta.topDocumentVersion, 1);
  assert.equal(downloads.lastRejection, 'DOWNLOAD_CAPACITY'); assert.equal(downloads.items.has(refused), false); assert.equal(downloads.items.size, 1);
});
test('download policy works without an optional handoff callback', async t => {
  const { downloads } = await fixture(t, { maxItems: 1 });
  assert.doesNotThrow(() => { begin(downloads); begin(downloads); });
  assert.equal(downloads.items.size, 1); assert.equal(downloads.lastRejection, 'DOWNLOAD_CAPACITY');
});
test('download event floods keep bounded recent receipts without losing active or recent deduplication', async t => {
  const callbacks = [];
  const { downloads } = await fixture(t, { maxItems: 1, onBegin: event => callbacks.push(event.guid) });
  const active = begin(downloads); let recent;
  for (let index = 0; index < 600; index++) recent = begin(downloads, randomUUID(), { frameId: 'unbound' });
  await Promise.allSettled([...downloads.tasks]);
  assert.equal(downloads.begun.size, 512); assert.equal(downloads.begun.has(active), false);
  const rejected = downloads.rejected;
  begin(downloads, active); begin(downloads, recent, { frameId: 'known' });
  assert.deepEqual(callbacks, [active]); assert.equal(downloads.rejected, rejected); assert.equal(downloads.items.size, 1);
  const capacity = begin(downloads); await Promise.allSettled([...downloads.tasks]);
  begin(downloads, capacity); assert.deepEqual(callbacks, [active, capacity]);
  assert.equal(downloads.begun.size, 512); assert.equal(downloads.rejected, rejected + 1);
});
test('completed downloads use GUID payloads, sanitized metadata, immutable source ownership and idempotent ack', async t => {
  const { downloads, directory, client } = await fixture(t); const first = begin(downloads), second = begin(downloads);
  await writeFile(join(directory, first), 'first'); await writeFile(join(directory, second), 'second');
  await downloads.progress({ guid: first, state: 'completed', receivedBytes: 5, totalBytes: 5, filePath: '/etc/passwd' });
  await downloads.progress({ guid: second, state: 'completed', receivedBytes: 6, totalBytes: 6 });
  const list = downloads.list().downloads;
  assert.deepEqual(list.map(item => item.filename), ['报表.xlsx', '报表.xlsx']);
  assert.equal(list[0].url, 'https://example.test/report'); assert.equal(list[0].control, 'human');
  assert.equal(list[0].context_ref, owner.context_ref); assert.equal('filePath' in list[0], false);
  const content = await downloads.content(first); assert.equal(content.size, 5); assert.equal((await bytes(content.stream)).toString(), 'first');
  await downloads.acknowledge(first); await downloads.acknowledge(first);
  await assert.rejects(access(join(directory, first))); assert.equal(downloads.list().downloads.length, 1);
  await assert.rejects(downloads.content('../../etc/passwd'), error => error.status === 400);
  await assert.rejects(downloads.content(randomUUID()), error => error.status === 404);
  assert.equal(client.calls[0].params.behavior, 'allowAndName');
});
test('unknown initiators, in-progress reads, missing files and symlink payloads are never delivered', async t => {
  const { downloads, directory, client } = await fixture(t);
  const unknown = begin(downloads, randomUUID(), { frameId: 'foreign-profile' });
  await Promise.allSettled([...downloads.tasks]); assert.equal(downloads.list().downloads.length, 0);
  assert.equal(downloads.list().last_rejection, 'DOWNLOAD_UNBOUND_PAGE'); assert.ok(client.calls.some(call => call.method === 'Browser.cancelDownload' && call.params.guid === unknown));
  const pending = begin(downloads); await assert.rejects(downloads.content(pending), error => error.status === 409);
  await downloads.progress({ guid: pending, state: 'completed' }); assert.equal(downloads.list().downloads[0].error_code, 'DOWNLOAD_FILE_UNAVAILABLE');
  const link = begin(downloads); const outside = join(directory, 'not-a-guid'); await writeFile(outside, 'private'); await symlink(outside, join(directory, link));
  await downloads.progress({ guid: link, state: 'completed' }); assert.equal(downloads.list().downloads[1].state, 'failed');
  assert.equal(await readFile(outside, 'utf8'), 'private');
});
test('reported size, unknown-length disk growth, total storage, capacity and timeout are bounded', async t => {
  let now = Date.now(); const { downloads, directory } = await fixture(t, { maxBytes: 10, maxTotal: 15, maxItems: 3, now: () => now });
  const large = begin(downloads); await downloads.progress({ guid: large, state: 'inProgress', receivedBytes: 1, totalBytes: 11 });
  assert.equal(downloads.list().downloads[0].error_code, 'DOWNLOAD_TOO_LARGE'); await downloads.acknowledge(large);
  const unknownSize = begin(downloads); await writeFile(join(directory, `${unknownSize}.crdownload`), '12345678901'); await downloads.sweep();
  assert.equal(downloads.list().downloads[0].error_code, 'DOWNLOAD_TOO_LARGE'); await downloads.acknowledge(unknownSize);
  const a = begin(downloads), b = begin(downloads); await downloads.progress({ guid: a, state: 'inProgress', receivedBytes: 8 }); await downloads.progress({ guid: b, state: 'inProgress', receivedBytes: 8 });
  assert.equal(downloads.list().downloads[1].state, 'failed'); begin(downloads); begin(downloads);
  assert.equal(downloads.list().last_rejection, 'DOWNLOAD_CAPACITY'); assert.equal(downloads.list().downloads.length, 3);
  now += 5 * 60_000 + 1; await downloads.sweep(); assert.equal(downloads.list().downloads[0].error_code, 'DOWNLOAD_TIMEOUT');
});
test('late duplicate progress, acknowledged downloads and expired completions cannot reappear as successful', async t => {
  let now = Date.now(); const { downloads, directory } = await fixture(t, { now: () => now });
  const id = begin(downloads); await downloads.acknowledge(id); await downloads.progress({ guid: id, state: 'completed' }); assert.equal(downloads.list().downloads.length, 0);
  const completed = begin(downloads); await writeFile(join(directory, completed), 'ok'); await downloads.progress({ guid: completed, state: 'completed' });
  await downloads.progress({ guid: completed, state: 'canceled' }); assert.equal(downloads.list().downloads[0].state, 'completed');
  now += 15 * 60_000 + 1; await downloads.sweep(); assert.equal(downloads.list().downloads[0].error_code, 'DOWNLOAD_EXPIRED');
  await assert.rejects(downloads.content(completed), error => error.status === 409);
});
for (const control of ['human', 'agent']) test(`${control} downloads accept 93.9 MiB headers and reject known lengths above 512 MiB before receiving bytes`, async t => {
  const { downloads, client } = await fixture(t, { ownerForFrame: () => ({ ...owner, control }) });
  assert.equal(MAX_DOWNLOAD_BYTES, 512 * 1024 * 1024);
  assert.equal(downloads.list().max_file_bytes, MAX_DOWNLOAD_BYTES);
  assert.equal(downloads.list().max_total_bytes, 2 * 1024 * 1024 * 1024);
  const normal = begin(downloads), size = Math.round(93.9 * 1024 * 1024);
  await downloads.progress({ guid: normal, state: 'inProgress', receivedBytes: 0, totalBytes: size });
  assert.equal(downloads.list().downloads[0].state, 'in_progress');
  assert.equal(downloads.list().downloads[0].control, control);
  assert.equal(downloads.total(), size);
  assert.equal(client.calls.filter(call => call.method === 'Browser.cancelDownload').length, 0);
  const tooLarge = begin(downloads);
  await downloads.progress({ guid: tooLarge, state: 'inProgress', receivedBytes: 0, totalBytes: MAX_DOWNLOAD_BYTES + 1 });
  const failed = downloads.list().downloads[1];
  assert.equal(failed.state, 'failed'); assert.equal(failed.error_code, 'DOWNLOAD_TOO_LARGE');
  assert.equal(failed.actual_bytes, MAX_DOWNLOAD_BYTES + 1); assert.equal(failed.limit_bytes, MAX_DOWNLOAD_BYTES);
  assert.equal(failed.received_bytes, 0); assert.equal(failed.control, control);
  assert.equal(downloads.list().rejected_count, undefined);
  assert.equal(downloads.total(), size, 'rejected oversize download no longer reserves storage');
});
test('known pending lengths reserve the full 2 GiB quota before receiving bytes and ACK releases it', async t => {
  const { downloads } = await fixture(t); const ids = [];
  for (let index = 0; index < 4; index++) {
    const id = begin(downloads); ids.push(id);
    await downloads.progress({ guid: id, state: 'inProgress', receivedBytes: 0, totalBytes: MAX_DOWNLOAD_BYTES });
    assert.equal(downloads.items.get(id).state, 'in_progress');
  }
  const full = downloads.list().max_total_bytes;
  assert.equal(downloads.total(), full);
  const overflow = begin(downloads);
  await downloads.progress({ guid: overflow, state: 'inProgress', receivedBytes: 0, totalBytes: 1 });
  const failed = downloads.items.get(overflow);
  assert.equal(failed.state, 'failed'); assert.equal(failed.error_code, 'DOWNLOAD_TOTAL_LIMIT');
  assert.equal(failed.actual_bytes, full + 1); assert.equal(failed.limit_bytes, full);
  assert.equal(failed.received_bytes, 0); assert.equal(downloads.total(), full);
  assert.equal(downloads.list().rejected_count, undefined);
  await downloads.acknowledge(ids[0]); await downloads.acknowledge(ids[0]);
  const replacement = begin(downloads);
  await downloads.progress({ guid: replacement, state: 'inProgress', receivedBytes: 0, totalBytes: MAX_DOWNLOAD_BYTES });
  assert.equal(downloads.items.get(replacement).state, 'in_progress'); assert.equal(downloads.total(), full);
});
for (const source of ['progress', 'disk', 'completion']) test(`unknown-length ${source} growth reports aggregate quota separately from per-file quota`, async t => {
  const { downloads, directory } = await fixture(t, { maxBytes: 10, maxTotal: 15 });
  const first = begin(downloads), next = begin(downloads);
  await downloads.progress({ guid: first, state: 'inProgress', receivedBytes: 8, totalBytes: 0 });
  if (source === 'progress') await downloads.progress({ guid: next, state: 'inProgress', receivedBytes: 8, totalBytes: 0 });
  else if (source === 'disk') { await writeFile(join(directory, `${next}.crdownload`), '12345678'); await downloads.sweep(); }
  else { await writeFile(join(directory, next), '12345678'); await downloads.progress({ guid: next, state: 'completed', receivedBytes: 0, totalBytes: 0 }); }
  const failed = downloads.items.get(next);
  assert.equal(failed.error_code, 'DOWNLOAD_TOTAL_LIMIT');
  assert.equal(failed.actual_bytes, 16); assert.equal(failed.limit_bytes, 15);
  assert.equal(downloads.items.get(first).state, 'in_progress'); assert.equal(downloads.total(), 8);
  assert.equal(downloads.list().rejected_count, undefined);
  await assert.rejects(access(join(directory, next))); await assert.rejects(access(join(directory, `${next}.crdownload`)));
});
test('completion replaces the advertised reservation with actual stored bytes without double counting', async t => {
  const { downloads, directory } = await fixture(t, { maxBytes: 10, maxTotal: 15 });
  const first = begin(downloads), next = begin(downloads);
  await downloads.progress({ guid: first, state: 'inProgress', receivedBytes: 0, totalBytes: 10 });
  await downloads.progress({ guid: next, state: 'inProgress', receivedBytes: 0, totalBytes: 5 });
  assert.equal(downloads.total(), 15);
  await writeFile(join(directory, first), 'abc');
  await downloads.progress({ guid: first, state: 'completed', receivedBytes: 0, totalBytes: 10 });
  assert.equal(downloads.items.get(first).state, 'completed'); assert.equal(downloads.total(), 8);
  await downloads.progress({ guid: next, state: 'inProgress', receivedBytes: 7, totalBytes: 5 });
  assert.equal(downloads.total(), 10, 'received bytes may outgrow an advertised length');
  assert.equal((await bytes((await downloads.content(first)).stream)).toString(), 'abc');
});
test('quota failure is recorded before cancel feedback and both payload names are removed without rejection', async t => {
  const { downloads, directory, client } = await fixture(t, { maxBytes: 10, maxTotal: 15 }); const id = begin(downloads);
  await writeFile(join(directory, id), '12345678901'); await writeFile(join(directory, `${id}.crdownload`), '12345678901');
  const send = client.send;
  client.send = async (method, params) => {
    if (method === 'Browser.cancelDownload') {
      assert.equal(downloads.items.get(id).state, 'failed');
      assert.equal(downloads.items.get(id).error_code, 'DOWNLOAD_TOO_LARGE');
      assert.equal(downloads.items.get(id).actual_bytes, 11);
      await downloads.progress({ guid: id, state: 'canceled', receivedBytes: 11 });
      await downloads.progress({ guid: id, state: 'completed', receivedBytes: 11 });
    }
    return send(method, params);
  };
  await downloads.progress({ guid: id, state: 'inProgress', receivedBytes: 0, totalBytes: 11 });
  assert.equal(downloads.items.get(id).error_code, 'DOWNLOAD_TOO_LARGE');
  assert.equal(client.calls.filter(call => call.method === 'Browser.cancelDownload').length, 1);
  assert.equal(downloads.list().rejected_count, undefined);
  await assert.rejects(access(join(directory, id))); await assert.rejects(access(join(directory, `${id}.crdownload`)));
});
test('oversize diagnostic byte counts remain safe integers even for an unsafe CDP count', async t => {
  const { downloads } = await fixture(t); const id = begin(downloads);
  await downloads.progress({ guid: id, state: 'inProgress', receivedBytes: 0, totalBytes: 1e30 });
  const failed = downloads.list().downloads[0];
  assert.equal(failed.error_code, 'DOWNLOAD_TOO_LARGE'); assert.equal(failed.actual_bytes, Number.MAX_SAFE_INTEGER);
  for (const field of ['received_bytes', 'total_bytes', 'actual_bytes', 'limit_bytes']) assert.equal(Number.isSafeInteger(failed[field]), true);
});
for (const persistent of [false, true]) test(`real Chromium ${persistent ? 'persistent default' : 'isolated'} context downloads authenticated HTTP and blob attachments`, async t => {
  const directory = await mkdtemp(join(tmpdir(), 'pogo-download-cdp-')); t.after(() => rm(directory, { recursive: true, force: true }));
  let authenticatedReads = 0;
  const site = http.createServer((req, res) => {
    if (req.url === '/report') { if (req.headers.cookie?.includes('test_session=yes')) authenticatedReads++; res.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'attachment; filename="report.xlsx"' }); res.end('synthetic-http-workbook'); return; }
    res.writeHead(200, { 'Content-Type': 'text/html', 'Set-Cookie': 'test_session=yes; HttpOnly; SameSite=Lax; Path=/' });
    res.end(`<a id="http" href="/report">HTTP</a><button id="blob" onclick="const a=document.createElement('a');a.href=window.URL.createObjectURL(new Blob(['synthetic-blob-workbook']));a.download='report.xlsx';a.click()">Blob</button>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve)); t.after(() => { site.closeAllConnections(); site.close(); });
  const context = persistent ? await chromium.launchPersistentContext('', { headless: true, acceptDownloads: false }) : await (await chromium.launch({ headless: true })).newContext({ acceptDownloads: true });
  const browser = context.browser(); t.after(() => browser.close());
  const page = context.pages()[0] || await context.newPage();
  const pageClient = await context.newCDPSession(page); const { targetInfo } = await pageClient.send('Target.getTargetInfo');
  await page.goto(`http://127.0.0.1:${site.address().port}/`);
  const { frameTree } = await pageClient.send('Page.getFrameTree'); const client = await browser.newBrowserCDPSession();
  const downloads = await new BrowserDownloads({ client, directory, contextId: persistent ? undefined : targetInfo.browserContextId, ownerForFrame: frame => frame === frameTree.frame.id ? owner : null }).start(); t.after(() => downloads.close());
  await page.locator('#http').click(); await eventually(() => downloads.list().downloads[0]?.state === 'completed');
  await page.locator('#blob').click(); await eventually(() => downloads.list().downloads[1]?.state === 'completed');
  const items = downloads.list().downloads; assert.equal(items.length, 2); assert.equal(authenticatedReads, 1, 'HTTP response is fetched only by the authenticated browser');
  assert.deepEqual(items.map(item => item.filename), ['report.xlsx', 'report.xlsx']);
  assert.equal((await bytes((await downloads.content(items[0].id)).stream)).toString(), 'synthetic-http-workbook');
  assert.equal((await bytes((await downloads.content(items[1].id)).stream)).toString(), 'synthetic-blob-workbook');
  assert.equal(page.url(), `http://127.0.0.1:${site.address().port}/`); assert.equal(context.pages().length, 1);
});
