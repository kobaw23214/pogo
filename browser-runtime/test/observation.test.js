import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { createWorker } from '../src/server.js';

let worker, site, origin, base, op = 0;
const token = 'observation-fixture-only';
async function request(path, method = 'GET', body) {
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const reply = await response.json();
  assert.equal(response.status, 200, JSON.stringify(reply));
  assert.equal(reply.ok, true, JSON.stringify(reply));
  return reply;
}
const execute = (id, args) => request(`/sessions/${id}/execute`, 'POST', { op_id: `observation-${++op}`, ...args });
async function open(id) {
  await request(`/sessions/${id}`, 'PUT');
  return execute(id, { action: 'navigate', url: origin });
}
function elementLine(reply, name) {
  const line = reply.text.split('\n').find(line => /^\[e\d+\]/.test(line) && line.includes(JSON.stringify(name)));
  assert.ok(line, `Missing fixture element ${name}`);
  return line;
}
function elementRef(reply, name) { return /^\[(e\d+)\]/.exec(elementLine(reply, name))[1]; }
async function assertObservation(id, role, name, checked) {
  const snapshot = await execute(id, { action: 'snapshot' });
  const found = await execute(id, { action: 'find', role, name });
  const line = elementLine(snapshot, name);
  assert.equal(elementLine(found, name), line, 'snapshot and find must expose the same current state');
  assert.match(line, new RegExp(` checked=${checked}(?: |$)`));
  return found;
}

before(async () => {
  site = http.createServer((_req, response) => {
    response.setHeader('Content-Type', 'text/html; charset=utf-8');
    response.end(`<!doctype html><title>Observation fixture</title>
      <label>Standard login<input type="radio" name="method" value="standard"></label>
      <label>MFA login<input type="radio" name="method" value="mfa" checked></label>
      <label>Remember device<input type="checkbox"></label>
      <label>Native checked wins<input type="checkbox" checked aria-checked="false"></label>
      <button role="checkbox" aria-label="Custom tri-state" aria-checked="mixed"
        onclick="this.setAttribute('aria-checked',this.getAttribute('aria-checked')==='mixed'?'true':this.getAttribute('aria-checked')==='true'?'false':'mixed')">Custom tri-state</button>
      <button role="radio" aria-label="Custom radio" aria-checked="false"
        onclick="this.setAttribute('aria-checked',this.getAttribute('aria-checked')==='true'?'false':'true')">Custom radio</button>
      <button role="checkbox" aria-label="Unspecified state">Unspecified state</button>
      <button role="checkbox" aria-label="Invalid state" aria-checked="not-a-state">Invalid state</button>
      <button aria-label="Ordinary button" aria-checked="true">Ordinary button</button>
      <label>Password<input type="password" value="initial-observation-password"></label>`);
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${site.address().port}`;
  worker = await createWorker({ token, actionTimeout: 2500, idleMs: 60000 });
  base = `http://127.0.0.1:${(await worker.listen(0)).port}`;
});
after(async () => {
  await worker?.close();
  site?.closeAllConnections();
  if (site) await new Promise(resolve => site.close(resolve));
});

test('native radio selection is observable before and after a real group change', { timeout: 20000 }, async () => {
  await open('radio');
  const standard = await assertObservation('radio', 'radio', 'Standard login', false);
  await assertObservation('radio', 'radio', 'MFA login', true);
  await execute('radio', { action: 'click', target: elementRef(standard, 'Standard login') });
  await assertObservation('radio', 'radio', 'Standard login', true);
  await assertObservation('radio', 'radio', 'MFA login', false);
});

test('native checkboxes expose unchecked state and follow the DOM property, not an ARIA override', { timeout: 20000 }, async () => {
  await open('checkbox');
  const unchecked = await assertObservation('checkbox', 'checkbox', 'Remember device', false);
  await assertObservation('checkbox', 'checkbox', 'Native checked wins', true);
  await execute('checkbox', { action: 'click', target: elementRef(unchecked, 'Remember device') });
  const checked = await assertObservation('checkbox', 'checkbox', 'Remember device', true);
  await execute('checkbox', { action: 'click', target: elementRef(checked, 'Remember device') });
  await assertObservation('checkbox', 'checkbox', 'Remember device', false);
});

test('ARIA radio and checkbox states include mixed and update after actual clicks', { timeout: 20000 }, async () => {
  await open('aria');
  let observation = await assertObservation('aria', 'checkbox', 'Custom tri-state', 'mixed');
  for (const state of ['true', 'false', 'mixed']) {
    await execute('aria', { action: 'click', target: elementRef(observation, 'Custom tri-state') });
    observation = await assertObservation('aria', 'checkbox', 'Custom tri-state', state);
  }
  const radio = await assertObservation('aria', 'radio', 'Custom radio', false);
  await execute('aria', { action: 'click', target: elementRef(radio, 'Custom radio') });
  await assertObservation('aria', 'radio', 'Custom radio', true);
});

test('unknown or unrelated ARIA states are omitted and password values stay private in both observations', { timeout: 20000 }, async () => {
  const id = 'private', initial = await open(id);
  for (const name of ['Unspecified state', 'Invalid state', 'Ordinary button']) {
    assert.doesNotMatch(elementLine(initial, name), / checked=/);
    const found = await execute(id, { action: 'find', name });
    assert.doesNotMatch(elementLine(found, name), / checked=/);
  }
  assert.ok(!initial.text.includes('not-a-state'));
  assert.ok(!initial.text.includes('initial-observation-password'));
  const password = 'typed-observation-password';
  const typed = await execute(id, { action: 'type', target: elementRef(initial, 'Password'), text: password });
  for (const observation of [typed, await execute(id, { action: 'snapshot' }), await execute(id, { action: 'find', role: 'textbox', name: 'Password' })]) {
    assert.ok(!JSON.stringify(observation).includes(password));
    assert.doesNotMatch(elementLine(observation, 'Password'), / value=/);
  }
});
