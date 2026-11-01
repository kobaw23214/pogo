// These tests cover HTTP/receipt boundaries with synthetic byte fixtures and a
// local transport stub. They do not prove ONLYOFFICE editing or OOXML validity.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { createLab } from './server.mjs';

const secret = 'synthetic-docserver-signing-secret-1234567890123456';
const original = Buffer.from('PK\x03\x04synthetic-original');
const saved = Buffer.from('PK\x03\x04synthetic-first-save');

function jwt(payload) {
  const header = Buffer.from(JSON.stringify({ alg: 'HS256', typ: 'JWT' })).toString('base64url');
  const data = Buffer.from(JSON.stringify(payload)).toString('base64url');
  const input = `${header}.${data}`;
  return `${input}.${crypto.createHmac('sha256', secret).update(input).digest('base64url')}`;
}

async function setup(t, options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'pogo-office-lab-contract-'));
  await fs.mkdir(path.join(root, 'fixtures'));
  await fs.writeFile(path.join(root, 'fixtures', 'manifest.json'), JSON.stringify({ synthetic: true,
    files: { word: { name: 'test.docx' }, excel: { name: 'test.xlsx' } } }));
  for (const name of ['test.docx', 'test.xlsx']) await fs.writeFile(path.join(root, 'fixtures', name), original);
  const engineCalls = [], downloadCalls = [];
  let releaseDownload, reportDownload;
  const downloadGate = new Promise((resolve) => { releaseDownload = resolve; });
  const downloadStarted = new Promise((resolve) => { reportDownload = resolve; });
  const engine = http.createServer(async (request, response) => {
    if (request.url === '/coauthoring/CommandService.ashx') {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      engineCalls.push(JSON.parse(Buffer.concat(chunks)));
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({ error: 0 }));
    } else if (request.url === '/synthetic-output') {
      downloadCalls.push(request.url);
      reportDownload();
      if (options.holdDownload) await downloadGate;
      response.writeHead(200, { 'Content-Type': 'application/octet-stream' });
      response.end(saved);
    } else {
      response.writeHead(404); response.end();
    }
  });
  await new Promise((resolve) => engine.listen(0, '127.0.0.1', resolve));
  const engineURL = `http://127.0.0.1:${engine.address().port}`;
  const lab = await createLab({ dataRoot: root, port: 0, secret, engineURL,
    commandTimeout: 2000, saveTimeout: 2000, ...options });
  const base = `http://127.0.0.1:${lab.server.address().port}`;
  t.after(async () => {
    releaseDownload();
    await lab.close();
    engine.closeAllConnections();
    await new Promise((resolve) => engine.close(resolve));
    await fs.rm(root, { recursive: true, force: true });
  });

  async function request(route, body, headers = {}) {
    const response = await fetch(`${base}${route}`, {
      method: body === undefined ? 'GET' : 'POST',
      headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json', Origin: lab.url }), ...headers },
      body: body === undefined ? undefined : JSON.stringify(body)
    });
    const result = await response.json();
    return { code: response.status, body: result };
  }
  async function open(id = 'word') {
    const reply = await request(`/api/documents/${id}/open`, {});
    assert.equal(reply.code, 200);
    return reply.body;
  }
  const route = (session, action) => `/api/sessions/${session.id}/${action}`;
  const sessionRequest = (session, action, body = {}) => request(route(session, action), { ...body, token: session.token });
  const next = (session) => request(`${route(session, 'next')}?token=${session.token}`);
  const ready = (session) => sessionRequest(session, 'ready', { capabilities: { read: true } });
  const command = (session, operationId, operation = 'word.read', args = {}) =>
    sessionRequest(session, 'commands', { operationId, operation, args });
  async function dispatched(session) {
    for (let i = 0; i < 50; i++) {
      const reply = await next(session);
      if (reply.body.command) return reply.body.command;
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
    assert.fail('No queued transport command was dispatched.');
  }
  async function callback(opened, fields = {}, signedOverride) {
    const config = opened.config;
    const body = { key: config.document.key, status: 6, url: `${engineURL}/synthetic-output`, ...fields };
    body.token = jwt(signedOverride || body);
    const url = new URL(config.editorConfig.callbackUrl);
    return request(`${url.pathname}${url.search}`, body);
  }
  const exportReceipt = (session, operation, fields = {}) => sessionRequest(session, 'export', {
    operationId: operation.operationId, url: `${engineURL}/synthetic-output`,
    fileType: session.documentId === 'word' ? 'docx' : 'xlsx', ...fields
  });
  async function startSave(session) {
    const promise = sessionRequest(session, 'save');
    const operation = await dispatched(session);
    assert.equal(operation.operation, 'host.export');
    return { promise, operation };
  }
  return { root, lab, base, engineURL, engineCalls, downloadCalls, request, open, ready,
    route, sessionRequest, next, command, dispatched, callback, exportReceipt, startSave,
    downloadStarted, releaseDownload };
}

test('concurrent duplicate IDs dispatch once and both callers receive one receipt', async (t) => {
  const h = await setup(t);
  const { session } = await h.open(); await h.ready(session);
  const a = h.command(session, 'duplicate-write-01', 'word.replace', { search: 'old', replacement: 'new', expectedText: 'old', expectedCount: 1 });
  const b = h.command(session, 'duplicate-write-01', 'word.replace', { expectedCount: 1, expectedText: 'old', replacement: 'new', search: 'old' });
  const first = await h.dispatched(session);
  assert.equal(first.operationId, 'duplicate-write-01');
  assert.equal((await h.next(session)).body.command, null);
  const changed = await h.command(session, 'duplicate-write-01', 'word.replace', { search: 'old', replacement: 'other', expectedText: 'old', expectedCount: 1 });
  assert.equal(changed.code, 409);
  assert.equal((await h.sessionRequest(session, 'results', { operationId: first.operationId, ok: true, result: { after: { text: 'new' } } })).code, 200);
  const replies = await Promise.all([a, b]);
  assert.equal(replies[0].code, 200); assert.deepEqual(replies[0].body, replies[1].body);
  const replay = await h.command(session, 'duplicate-write-01', 'word.replace', { search: 'old', replacement: 'new', expectedText: 'old', expectedCount: 1 });
  assert.deepEqual(replay.body, replies[0].body);
  assert.equal((await h.next(session)).body.command, null);
  assert.equal(h.lab.sessions.get(session.id).dirtyEpoch, 1);
});

test('wrong origin, session and token cannot enqueue or acknowledge edits', async (t) => {
  const h = await setup(t);
  const word = (await h.open()).session, excel = (await h.open('excel')).session;
  await h.ready(word); await h.ready(excel);
  const op = { operationId: 'wrong-scope-01', operation: 'word.replace', args: {}, token: word.token };
  assert.equal((await h.request(h.route(word, 'commands'), op, { Origin: 'https://unrelated.invalid' })).code, 403);
  assert.equal((await h.request(h.route(word, 'commands'), { ...op, token: 'wrong-token' })).code, 403);
  assert.equal((await h.request(h.route(word, 'commands'), { ...op, token: 'é'.repeat(word.token.length) })).code, 403);
  assert.equal((await h.request(h.route(excel, 'commands'), op)).code, 403);
  assert.equal((await h.request('/api/sessions/no-such-session/commands', op)).code, 409);
  assert.equal((await h.request(h.route(word, 'results'), { token: 'wrong', operationId: op.operationId, ok: true })).code, 403);
  assert.equal((await h.next(word)).body.command, null);
  assert.equal((await h.next(excel)).body.command, null);
  assert.equal(h.lab.sessions.get(word.id).dirtyEpoch, 0);
});

test('timeout records unknown, ignores late receipts and preserves identical retry result', async (t) => {
  const h = await setup(t, { commandTimeout: 60 });
  const { session } = await h.open(); await h.ready(session);
  const pending = h.command(session, 'unknown-write-01', 'word.replace', { search: 'x' });
  const dispatched = await h.dispatched(session);
  const unknown = await pending;
  assert.equal(unknown.code, 409); assert.equal(unknown.body.error.code, 'OUTCOME_UNKNOWN');
  const late = await h.sessionRequest(session, 'results', { operationId: dispatched.operationId, ok: true, result: { after: { text: 'late' } } });
  assert.equal(late.body.ignored, true);
  assert.equal(h.lab.sessions.get(session.id).dirtyEpoch, 0);
  const retry = await h.command(session, 'unknown-write-01', 'word.replace', { search: 'x' });
  assert.deepEqual(retry.body, unknown.body);
  assert.equal((await h.command(session, 'unknown-write-02', 'word.replace', { search: 'x' })).code, 409);
  assert.equal((await h.next(session)).body.command, null);
});

test('plugin unknown outcome blocks the host queue as well', async (t) => {
  const h = await setup(t);
  const { session } = await h.open(); await h.ready(session);
  const pending = h.command(session, 'plugin-unknown-01');
  await h.dispatched(session);
  await h.sessionRequest(session, 'results', { operationId: 'plugin-unknown-01', ok: false,
    error: { code: 'EDITOR_OUTCOME_UNKNOWN', message: 'Synthetic missing editor callback' } });
  assert.equal((await pending).body.error.code, 'EDITOR_OUTCOME_UNKNOWN');
  assert.equal(h.lab.sessions.get(session.id).blocked, true);
  assert.equal((await h.command(session, 'plugin-unknown-02', 'word.replace', {})).code, 409);
  assert.equal((await h.next(session)).body.command, null);
});

test('DocServer callback requires JWT but never persists a second save channel', async (t) => {
  const h = await setup(t);
  const opened = await h.open(); await h.ready(opened.session);
  const url = new URL(opened.config.editorConfig.callbackUrl);
  const fields = { key: opened.config.document.key, status: 6, url: `${h.engineURL}/synthetic-output` };
  assert.equal((await h.request(`${url.pathname}${url.search}`, { ...fields, token: 'invalid' })).code, 403);
  const signed = await h.callback(opened);
  assert.equal(signed.code, 200); assert.equal(signed.body.error, 0);
  assert.equal(h.downloadCalls.length, 0);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), original);
});

test('an old session callback cannot overwrite the newly opened document', async (t) => {
  const h = await setup(t);
  const old = await h.open(); await h.ready(old.session);
  assert.equal((await h.sessionRequest(old.session, 'close')).code, 200);
  const current = await h.open(); await h.ready(current.session);
  const receipt = await h.callback(old);
  assert.equal(receipt.code, 200); assert.equal(receipt.body.error, 0);
  assert.equal(h.downloadCalls.length, 0);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), original);
  assert.equal(h.lab.documents.get('word').active, current.session.id);
});

test('export is save-owned, authenticated and dispatched once before atomic persistence', async (t) => {
  const h = await setup(t);
  const opened = await h.open(); await h.ready(opened.session);
  assert.equal((await h.command(opened.session, 'unowned-export-01', 'host.export', {})).code, 400);
  await h.sessionRequest(opened.session, 'state', { dirty: true });
  const { promise, operation } = await h.startSave(opened.session);
  assert.equal((await h.next(opened.session)).body.command, null);
  assert.equal((await h.sessionRequest(opened.session, 'save')).code, 409);
  const payload = { token: opened.session.token, operationId: operation.operationId,
    url: `${h.engineURL}/synthetic-output`, fileType: 'docx' };
  assert.equal((await h.request(h.route(opened.session, 'export'), payload, { Origin: 'https://unrelated.invalid' })).code, 403);
  assert.equal((await h.request(h.route(opened.session, 'export'), { ...payload, token: 'wrong' })).code, 403);
  assert.equal((await h.exportReceipt(opened.session, operation, { operationId: 'wrong-export-id' })).code, 409);
  assert.equal((await h.sessionRequest(opened.session, 'results', { operationId: operation.operationId, ok: true, result: { url: payload.url } })).code, 409);
  assert.equal((await h.exportReceipt(opened.session, operation, { fileType: 'xlsx' })).code, 400);
  assert.equal(h.downloadCalls.length, 0);
  assert.equal((await h.exportReceipt(opened.session, operation)).code, 200);
  const savedReply = await promise;
  assert.equal(savedReply.code, 200); assert.equal(savedReply.body.status.saveState, 'saved');
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), saved);
  const metadata = JSON.parse(await fs.readFile(`${h.lab.documents.get('word').file}.json`, 'utf8'));
  assert.equal(metadata.revision, 1);
  assert.equal(metadata.sha256, crypto.createHash('sha256').update(saved).digest('hex'));
  assert.equal((await h.exportReceipt(opened.session, operation)).code, 409);
  assert.equal(h.downloadCalls.length, 1); assert.equal(h.lab.documents.get('word').revision, 1);
  assert.equal(h.engineCalls.length, 0);
});

test('export refuses non-allowlisted URLs before retrieving or overwriting files', async (t) => {
  const h = await setup(t);
  const opened = await h.open(); await h.ready(opened.session);
  const { promise, operation } = await h.startSave(opened.session);
  assert.equal((await h.exportReceipt(opened.session, operation, { url: 'http://127.0.0.1:9/private-output' })).code, 403);
  assert.equal((await h.exportReceipt(opened.session, operation, { url: `http://user:password@${new URL(h.engineURL).host}/synthetic-output` })).code, 403);
  assert.equal(h.downloadCalls.length, 0);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), original);
  assert.equal((await h.exportReceipt(opened.session, operation)).code, 200);
  assert.equal((await promise).code, 200);
});

test('closing or reopening is refused while an export is downloading its output', async (t) => {
  const h = await setup(t, { holdDownload: true });
  const opened = await h.open(); await h.ready(opened.session);
  const { promise, operation } = await h.startSave(opened.session);
  const pending = h.exportReceipt(opened.session, operation);
  await h.downloadStarted;
  assert.equal((await h.sessionRequest(opened.session, 'close')).code, 409);
  assert.equal((await h.request('/api/documents/word/open', {})).code, 409);
  h.releaseDownload();
  assert.equal((await pending).code, 200);
  assert.equal((await promise).code, 200);
  assert.equal((await h.sessionRequest(opened.session, 'close')).code, 200);
  assert.equal((await h.request('/api/documents/word/open', {})).code, 200);
});

test('export save acknowledges its captured epoch and keeps later human edits dirty', async (t) => {
  const h = await setup(t);
  const opened = await h.open(); await h.ready(opened.session);
  await h.sessionRequest(opened.session, 'state', { dirty: true });
  const { promise, operation } = await h.startSave(opened.session);
  await h.sessionRequest(opened.session, 'state', { dirty: true });
  const receipt = await h.exportReceipt(opened.session, operation);
  assert.equal(receipt.code, 200);
  const saveReply = await promise;
  assert.equal(saveReply.code, 200); assert.equal(saveReply.body.status.saveState, 'dirty');
  const session = h.lab.sessions.get(opened.session.id);
  assert.equal(session.persistedEpoch, 1); assert.equal(session.dirtyEpoch, 2);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), saved);
  assert.equal((await h.sessionRequest(opened.session, 'close')).code, 409);
});

test('unknown export accepts no late URL, replay or new save', async (t) => {
  const h = await setup(t, { commandTimeout: 80, saveTimeout: 80 });
  const opened = await h.open(); await h.ready(opened.session);
  const { promise, operation } = await h.startSave(opened.session);
  const unknown = await promise;
  assert.equal(unknown.code, 502);
  assert.equal(h.lab.sessions.get(opened.session.id).blocked, true);
  const late = await h.exportReceipt(opened.session, operation);
  assert.equal(late.code, 409);
  assert.equal((await h.exportReceipt(opened.session, operation)).code, 409);
  assert.equal((await h.sessionRequest(opened.session, 'save')).code, 409);
  assert.equal(h.downloadCalls.length, 0);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), original);
});

test('an export timeout during download cannot persist its late output', async (t) => {
  const h = await setup(t, { holdDownload: true, commandTimeout: 80, saveTimeout: 80 });
  const opened = await h.open(); await h.ready(opened.session);
  const { promise, operation } = await h.startSave(opened.session);
  const downloading = h.exportReceipt(opened.session, operation);
  await h.downloadStarted;
  assert.equal((await promise).code, 502);
  assert.equal(h.lab.sessions.get(opened.session.id).blocked, true);
  h.releaseDownload();
  const late = await downloading;
  assert.notEqual(late.code, 200);
  assert.equal(h.lab.documents.get('word').revision, 0);
  assert.deepEqual(await fs.readFile(h.lab.documents.get('word').file), original);
  assert.equal((await h.exportReceipt(opened.session, operation)).code, 409);
  assert.equal(h.downloadCalls.length, 1);
});

test('an old export receipt cannot cross into a reopened editor session', async (t) => {
  const h = await setup(t);
  const old = await h.open(); await h.ready(old.session);
  const { promise, operation } = await h.startSave(old.session);
  assert.equal((await h.exportReceipt(old.session, operation)).code, 200);
  assert.equal((await promise).code, 200);
  assert.equal((await h.sessionRequest(old.session, 'close')).code, 200);
  const current = await h.open(); await h.ready(current.session);
  assert.equal((await h.exportReceipt(old.session, operation)).code, 409);
  assert.equal((await h.exportReceipt(current.session, operation)).code, 409);
  assert.equal(h.downloadCalls.length, 1); assert.equal(h.lab.documents.get('word').revision, 1);
});

test('callback JWT still binds userdata even though callbacks do not persist', async (t) => {
  const h = await setup(t);
  const opened = await h.open(); await h.ready(opened.session);
  const signature = { key: opened.config.document.key, status: 6,
    url: `${h.engineURL}/synthetic-output`, userdata: 'some-other-save-request' };
  const forged = await h.callback(opened, { userdata: 'unsigned-other-request' }, signature);
  assert.equal(forged.code, 403);
  assert.equal(h.lab.sessions.get(opened.session.id).persistedEpoch, 0);
  assert.equal(h.downloadCalls.length, 0);
  assert.equal((await h.callback(opened, { userdata: signature.userdata })).code, 200);
});
