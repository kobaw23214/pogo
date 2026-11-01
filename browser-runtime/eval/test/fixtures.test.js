import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { TASKS, createFixtureServer } from '../fixtures/index.js';
import { createWorker } from '../../src/server.js';

let fixture, worker, workerURL;
const token = randomUUID();
let sequence = 0;

async function request(path, method = 'GET', data) {
  const response = await fetch(workerURL + path, { method, headers: { Authorization: `Bearer ${token}`, ...(data ? { 'Content-Type': 'application/json' } : {}) }, body: data ? JSON.stringify(data) : undefined });
  const result = await response.json();
  assert.equal(response.status, 200, JSON.stringify(result));
  return result;
}

before(async () => {
  fixture = await createFixtureServer({ seed: 731 });
  worker = await createWorker({ token, actionTimeout: 5000, idleMs: 60_000 });
  const address = await worker.listen(0);
  workerURL = `http://127.0.0.1:${address.port}`;
});
after(async () => { await worker?.close(); await fixture?.close(); });

test('manifest has twenty bounded tasks across ten categories', () => {
  assert.equal(TASKS.length, 20);
  assert.equal(new Set(TASKS.map(t => t.id)).size, 20);
  assert.equal(new Set(TASKS.map(t => t.category)).size, 10);
  for (const task of TASKS) {
    for (const field of ['id', 'title', 'category', 'goal']) assert.equal(typeof task[field], 'string');
    assert.ok(task.max_steps > 5 && task.max_steps <= 60);
    assert.ok(task.timeout_ms > 0);
    assert.equal('solve' in task, false);
    assert.equal('expected' in task, false);
  }
});

test('same seed/task/repeat reproduces data while cases remain isolated', async () => {
  const first = fixture.createCase('catalog-a', 2), second = fixture.createCase('catalog-a', 2);
  assert.notEqual(first.scenarioId, second.scenarioId);
  assert.equal(first.prompt.replaceAll(first.startURL, '<URL>'), second.prompt.replaceAll(second.startURL, '<URL>'));
  const html1 = await (await fetch(first.startURL)).text(), html2 = await (await fetch(second.startURL)).text();
  assert.equal(html1, html2);
  const another = await createFixtureServer({ seed: 731 });
  try {
    const third = another.createCase('catalog-a', 2);
    assert.equal(await (await fetch(third.startURL)).text(), html1);
    const repeated = another.createCase('catalog-a', 3);
    assert.equal(await (await fetch(repeated.startURL)).text(), html1, 'repeat must preserve business data');
  } finally { await another.close(); }
});

test('HTTP never exposes judges, scripts, expected facts or evaluator source', async () => {
  const sample = fixture.createCase('registration-a');
  for (const path of ['/judge', '/expected', '/solve', '/eval/fixtures/index.js', new URL(sample.startURL).pathname + 'judge', new URL(sample.startURL).pathname + 'expected', new URL(sample.startURL).pathname + 'solve']) {
    assert.equal((await fetch(fixture.origin + path)).status, 404);
  }
  assert.equal((await sample.judge({ answer: 'I completed the task successfully.' })).success, false);
  assert.throws(() => fixture.createCase('not-a-task'));
  assert.throws(() => fixture.createCase('catalog-a', -1));
});

for (const task of TASKS) test(`reference browser solution: ${task.id}`, { timeout: 60_000 }, async () => {
  const instance = fixture.createCase(task.id), id = `fixture-${++sequence}`;
  await request(`/sessions/${id}`, 'PUT');
  try {
    const execute = args => request(`/sessions/${id}/execute`, 'POST', { op_id: `solve-${++sequence}`, ...args });
    const result = await instance.solve(execute);
    assert.deepEqual(await instance.judge(result), { success: true, reason: 'ok', duplicate_submissions: 0 });
    assert.equal((await instance.judge({ answer: '{}' })).success, false, 'the business state alone does not prove the answer');
    const isolated = fixture.createCase(task.id);
    assert.equal((await isolated.judge(result)).success, false, 'facts alone cannot satisfy a fresh scenario');
  } finally { await request(`/sessions/${id}`, 'DELETE'); }
});

// Asynchronous results (iframe receipt, delayed fetches, SPA re-render, popup
// tab) must already be in the reply of the action that caused them, so a model
// does not spend another turn waiting or re-observing. The reference scripts
// keep their explicit waits; this inspects the replies they received.
test('post-action replies already contain the asynchronous results tasks depend on', { timeout: 90_000 }, async () => {
  const checks = {
    'iframe-a': log => {
      const at = log.findIndex(e => e.args.action === 'wait' && e.args.text === 'Ticket confirmed');
      assert.equal(log[at - 1].args.action, 'click');
      assert.match(log[at - 1].reply.text, /Ticket confirmed IFRAME-\d+-1/);
    },
    'delayed-a': log => {
      assert.match(log.find(e => e.args.action === 'click' && e.args.target === '#load').reply.text, /Inventory ready/);
      assert.match(log.find(e => e.args.action === 'click' && e.args.target.startsWith('button[data-sku=')).reply.text, /Reservation confirmed: RES-/);
    },
    'spa-a': log => assert.match(log.find(e => e.args.action === 'click').reply.text, /Pending: 3/),
    'tabs-a': log => {
      const { reply } = log.find(e => e.args.action === 'click' && e.args.target === '#quote');
      assert.deepEqual(reply.opened_tabs.map(t => t.id), [reply.active_tab]);
      assert.match(reply.text, /Quote Q-\d+/);
      // The quote page's /visit ping is never read; it must not look pending.
      assert.equal(reply.pending_requests, undefined);
    },
  };
  for (const [taskId, check] of Object.entries(checks)) {
    const instance = fixture.createCase(taskId), id = `settle-${++sequence}`;
    await request(`/sessions/${id}`, 'PUT');
    try {
      const log = [];
      const execute = async args => {
        const reply = await request(`/sessions/${id}/execute`, 'POST', { op_id: `settle-${++sequence}`, ...args });
        log.push({ args, reply });
        return reply;
      };
      const result = await instance.solve(execute);
      assert.equal((await instance.judge(result)).success, true, taskId);
      check(log);
    } finally { await request(`/sessions/${id}`, 'DELETE'); }
  }
});

test('duplicate business submissions fail despite a valid first receipt', { timeout: 60_000 }, async () => {
  const instance = fixture.createCase('registration-a'), id = `duplicate-${++sequence}`;
  await request(`/sessions/${id}`, 'PUT');
  try {
    const execute = args => request(`/sessions/${id}/execute`, 'POST', { op_id: `duplicate-${++sequence}`, ...args });
    const first = await instance.solve(execute);
    assert.equal((await instance.judge(first)).success, true);
    await instance.solve(execute);
    assert.deepEqual(await instance.judge(first), { success: false, reason: 'duplicate_submission', duplicate_submissions: 1 });
  } finally { await request(`/sessions/${id}`, 'DELETE'); }
});
