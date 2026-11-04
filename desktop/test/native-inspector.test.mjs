import test from 'node:test';
import assert from 'node:assert/strict';
import { inspectNativeNode } from '../native-inspector.mjs';

function fixture() {
  const page = { isClosed: () => false, url: () => 'https://fixture.invalid/' };
  const meta = { id: 't1', page, version: 1, title: 'Native selected node' };
  const runtime = { picker: { nextRef: 0 }, generation: 1, contextRef: 'ctx_'+'a'.repeat(24), control: 'human', controlEpoch: 1, lease: 'test-lease', phase: 'ready', pages: new Map([['t1', meta]]),
    assertLive() {}, viewport: () => ({ width: 800, height: 600 }), version: () => `1:t1:${meta.version}` };
  const calls = [];
  const send = async (method, params) => {
    calls.push({ method, params });
    if (method === 'DOM.describeNode') return { node: { backendNodeId: 7, nodeType: 1, localName: 'button', get attributes() { throw new Error('do not read form attributes'); }, get nodeValue() { throw new Error('do not read node value'); } } };
    if (method === 'DOM.getBoxModel') return { model: { border: [10,20,110,20,110,60,10,60] } };
    if (method === 'Accessibility.getPartialAXTree') return { nodes: [{ backendDOMNodeId: 7, ignored: false, role: { value: 'button' }, name: { value: 'Safe label' }, description: { value: 'Safe description' }, get value() { throw new Error('do not read AX value'); } }] };
    throw new Error('unexpected command');
  };
  return { runtime, meta, calls, send, backendNodeId: 7, snapshotStamp: 'trusted-selection', checkCurrent: stamp => stamp === 'trusted-selection' };
}

test('native facts use only the exact DOM/AX node and never expose values, HTML or backend IDs', async () => {
  const input = fixture(), pick = await inspectNativeNode(input);
  assert.equal(pick.element.name, 'Safe label'); assert.equal(pick.element.text, 'Safe label Safe description');
  assert.deepEqual(pick.element.rect, { x:10,y:20,width:100,height:40 });
  assert.equal(pick.element.ref, 'p1'); assert.equal(pick.observation_scope, 'native-dom-accessibility');
  assert.equal(JSON.stringify(pick).includes('backendNodeId'), false);
  assert.deepEqual(input.calls.map(call => call.method), ['DOM.describeNode','DOM.getBoxModel','Accessibility.getPartialAXTree','DOM.describeNode','DOM.getBoxModel','Accessibility.getPartialAXTree']);
  assert.ok(input.calls.every(call => call.params.backendNodeId === 7));
  assert.ok(input.calls.filter(call => call.method.startsWith('Accessibility')).every(call => call.params.fetchRelatives === false));
});

test('form observations omit text even when AX responses include editable values', async () => {
  const input = fixture(), send = input.send;
  input.send = async (method, params) => {
    const result = await send(method, params);
    if (result.node) result.node.localName = 'input';
    if (result.nodes) { result.nodes[0].role = { value: 'textbox' }; result.nodes[0].name = { value: 'Password' }; }
    return result;
  };
  const pick = await inspectNativeNode(input);
  assert.equal(pick.element.name, 'Password'); assert.equal(pick.element.text, '');
});

test('node replacement, geometry movement and owner/document changes refuse publication', async () => {
  for (const kind of ['node','geometry','document','owner','caller']) {
    const input = fixture(), send = input.send; let calls = 0;
    input.send = async (method, params) => {
      const result = await send(method, params); calls++;
      if (calls >= 4) {
        if (kind === 'node' && result.node) result.node.backendNodeId = 8;
        if (kind === 'geometry' && result.model) result.model.border[0] = 11;
        if (kind === 'document') input.meta.version++;
        if (kind === 'owner') input.runtime.lease = 'replaced';
        if (kind === 'caller') input.checkCurrent = () => false;
      }
      return result;
    };
    if (kind === 'caller') { const current = input.checkCurrent; input.checkCurrent = stamp => calls < 4 && current(stamp); }
    await assert.rejects(inspectNativeNode(input), error => error.status === 409, kind);
    assert.equal(input.runtime.picker.nextRef, 0, kind);
  }
});

test('a timed-out native read remains single-flight and late results cannot publish references', async () => {
  const input = fixture(); let resolve, calls = 0;
  input.send = () => { calls++; return new Promise(done => { resolve = done; }); };
  await assert.rejects(inspectNativeNode(input), error => error.status === 504);
  for(let i=0;i<10;i++) await assert.rejects(inspectNativeNode(input), /still pending/);
  assert.equal(calls,1); resolve({node:{backendNodeId:7,nodeType:1,localName:'button'}});
  await new Promise(done => setImmediate(done)); assert.equal(calls,1);assert.equal(input.runtime.picker.nextRef,0);
});
