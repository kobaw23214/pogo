import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { chromium } from 'playwright';
import { BrowserSession } from '../src/session.js';

test('Overlay identity accepts its exact node and rejects replacements, reordered text, and another frame', async t => {
  const site = http.createServer((request, response) => {
    response.setHeader('Content-Type', 'text/html');
    response.end(request.url === '/child'
      ? '<!doctype html><button id="child">Repeated label</button>'
      : '<!doctype html><style>button{position:absolute;left:20px;width:140px;height:50px}#a{top:20px}#b{top:100px}iframe{position:absolute;left:300px;top:20px;width:200px;height:100px;border:0}</style><button id="a">Repeated label</button><button id="b">Repeated label</button><iframe src="/child"></iframe>');
  });
  await new Promise(resolve => site.listen(0, '127.0.0.1', resolve));
  t.after(() => { site.closeAllConnections(); site.close(); });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const session = await new BrowserSession(context, { actionTimeout: 3000 }).init(); t.after(() => session.close());
  const meta = session.page(), page = meta.page;
  await page.goto(`http://127.0.0.1:${site.address().port}/`, { waitUntil: 'load' });
  const identity = locator => locator.evaluate((node, key) => globalThis[key].summarize(node).identity, session.picker.key);
  const body = async () => ({ x: 50, y: 40, version: await session.refreshRevision(), tab_id: meta.id, context_ref: session.contextRef });

  // Reproduce the host's trusted path: resolve the Overlay backend node, then
  // ask only the fixed probe for identity. No page-selected script is run.
  const client = await context.newCDPSession(page); t.after(() => client.detach().catch(() => {}));
  const document = await client.send('DOM.getDocument');
  const { nodeId } = await client.send('DOM.querySelector', { nodeId: document.root.nodeId, selector: '#a' });
  const { node } = await client.send('DOM.describeNode', { nodeId });
  const { object } = await client.send('DOM.resolveNode', { backendNodeId: node.backendNodeId });
  const resolved = await client.send('Runtime.callFunctionOn', { objectId: object.objectId,
    functionDeclaration: 'function(key) { return globalThis[key].summarize(this).identity; }',
    arguments: [{ value: session.picker.key }], returnByValue: true });
  await client.send('Runtime.releaseObject', { objectId: object.objectId });
  const selectedIdentity = resolved.result.value;
  assert.match(selectedIdentity, /^[a-f0-9]{24}:1$/);
  assert.equal(await identity(page.locator('#a')), selectedIdentity);
  const otherIdentity = await identity(page.locator('#b'));
  const childIdentity = await identity(page.frames().find(frame => frame !== page.mainFrame()).locator('#child'));
  assert.match(childIdentity, /^[a-f0-9]{24}:1$/);
  assert.notEqual(childIdentity, selectedIdentity, 'frame-local sequence 1 must not collide with top-document sequence 1');

  const pick = await session.picker.pick(await body(), { expectedIdentity: selectedIdentity });
  assert.equal(pick.element.name, 'Repeated label');
  assert.equal('identity' in pick.element, false); assert.ok(!JSON.stringify(pick).includes(selectedIdentity));
  for (const expectedIdentity of [otherIdentity, childIdentity]) {
    await assert.rejects(session.picker.pick(await body(), { expectedIdentity }), error => error.status === 409 && /node no longer matches/.test(error.message));
  }
  await page.locator('#a').evaluate(node => { node.textContent = 'Updated selected text'; });
  assert.equal(await identity(page.locator('#a')), selectedIdentity, 'text edits preserve the actual node identity');
  assert.equal((await session.picker.pick(await body(), { expectedIdentity: selectedIdentity })).element.name, 'Updated selected text');

  await page.locator('#a').evaluate(node => node.replaceWith(node.cloneNode(true)));
  await assert.rejects(session.picker.pick(await body(), { expectedIdentity: selectedIdentity }), /node no longer matches/);
  const replacementIdentity = await identity(page.locator('#a'));
  await page.evaluate(() => { document.querySelector('#a').style.top = '100px'; document.querySelector('#b').style.top = '20px'; });
  await assert.rejects(session.picker.pick(await body(), { expectedIdentity: replacementIdentity }), /node no longer matches/);
  assert.equal((await session.picker.pick(await body(), { expectedIdentity: otherIdentity })).element.name, 'Repeated label');
  await page.locator('#b').evaluate(node => { node.hidden = true; });
  await assert.rejects(session.picker.pick(await body(), { expectedIdentity: otherIdentity }), error => error.status === 409);

  await page.reload({ waitUntil: 'load' });
  const afterNavigation = await identity(page.locator('#a'));
  assert.match(afterNavigation, /^[a-f0-9]{24}:1$/); assert.notEqual(afterNavigation, selectedIdentity);
  await assert.rejects(session.picker.pick(await body(), { expectedIdentity: selectedIdentity }), /node no longer matches/);
});
