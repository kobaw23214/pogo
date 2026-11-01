// A deterministic positive control. It uses private oracle knowledge only to
// choose the task's business record; every target/request/call-frame reference
// and every reported fact must come from the same recorded browser executor.
// Never include this module or an oracle in a model's input.

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
function replyOf(value) { return value?.ok !== undefined ? value : value?.data; }
function requireReply(value) {
  const reply = replyOf(value);
  if (!reply?.ok) throw new Error(`Reference browser action failed: ${reply?.error ?? 'invalid reply'}`);
  return reply.data;
}
async function waitFor(read, accept, label) {
  const end = Date.now() + 7000;
  while (Date.now() < end) { const result = await read(); if (accept(result)) return result; await sleep(30); }
  throw new Error(`Reference could not observe ${label}`);
}

export async function reference(task, oracle, execute) {
  const call = async (method, params = {}, target) => requireReply(await execute({ action: 'devtools', method, params, ...(target ? { target } : {}) }));
  const desired = oracle.requirements.target;
  const targets = await waitFor(() => call('targets.list'), data => data.items.some(item => item.ready && (desired ? item.type === desired.type && item.url === desired.url : item.is_root)), 'ready task target');
  const target = targets.items.find(item => item.ready && (desired ? item.type === desired.type && item.url === desired.url : item.is_root));
  const targetRef = target.target_ref;
  async function recordedResponse(requirement, acceptBody = () => true) {
    // Each opaque request ID is first learned through a recorded network.list.
    return waitFor(async () => {
      const list = await call('network.list', { limit: 100 }, targetRef);
      const request = list.items.filter(item => item.url === requirement.url && item.method === requirement.method && item.status === requirement.status).at(-1);
      if (!request) return null;
      const read = await call('network.read', { request_id: request.request_id }, targetRef);
      if (read.request?.body?.state !== 'available') return null;
      const body = JSON.parse(read.request.body.text);
      return acceptBody(body) ? { request: read.request, body } : null;
    }, Boolean, 'business network request and recorded response body');
  }

  if (task.id === 'network-diagnosis') {
    const response = await recordedResponse(oracle.requirements.network[0]);
    await call('console.list', { limit: 100 }, targetRef);
    return { answer: `失败状态 ${response.request.status}；业务错误 ${response.body.error.code}；原因：${response.body.error.message}；跟踪编号 ${response.body.error.trace_id}。依据已记录的请求响应和 Console 错误，未重发请求。` };
  }
  if (task.id === 'iframe-data') {
    const response = await recordedResponse(oracle.requirements.network[0]);
    const row = response.body.inventory.find(item => item.product === oracle.expected.product);
    if (!row) throw new Error('Reference inventory record absent');
    return { answer: `${row.product}：可用数量 ${row.available}，批次号 ${row.batch}。依据独立 iframe 目标实际读取的库存响应。` };
  }
  if (task.id === 'worker-data') {
    const root = targets.items.find(item => item.is_root && item.ready);
    const buttons = await call('Runtime.evaluate', { expression: 'Array.from(document.querySelectorAll("button")).map((button,index) => ({index,label:button.textContent,disabled:button.disabled}))', returnByValue: true }, root.target_ref);
    const button = buttons.result.result.value.find(item => item.label.includes('开始采样') && !item.disabled);
    if (!button) throw new Error('Reference sampling action is not ready');
    await call('Runtime.evaluate', { expression: `document.querySelectorAll("button")[${button.index}].click(); true`, returnByValue: true }, root.target_ref);
    const response = await recordedResponse(oracle.requirements.network[0]);
    const row = response.body.sample;
    return { answer: `${row.device}：读数 ${row.reading} ${row.unit}，采样编号 ${row.sample_id}。依据 dedicated worker 目标实际获取的监测响应。` };
  }
  if (task.id === 'debugger-recovery') {
    const paused = await call('events.list', { method: 'Debugger.paused', limit: 100 }, targetRef);
    // Chromium may leave CallFrame.url blank. Record its same-target script
    // identity so the judge can bind location.scriptId to the served worker.
    await call('events.list', { method: 'Debugger.scriptParsed', limit: 100 }, targetRef);
    const event = paused.items.filter(item => item.method === 'Debugger.paused').at(-1);
    const frame = event?.params?.callFrames?.[0];
    if (!frame?.callFrameId) throw new Error('Reference missing recorded paused frame');
    const evaluated = await call('Debugger.evaluateOnCallFrame', { callFrameId: frame.callFrameId, expression: 'settlement', returnByValue: true }, targetRef);
    const local = evaluated.result.result.value;
    if (!local || evaluated.result.exceptionDetails) throw new Error('Reference could not read actual settlement local');
    await call('Debugger.resume', {}, targetRef);
    const response = await recordedResponse(oracle.requirements.network[0]);
    return { answer: `暂停帧的实际局部记录：单据 ${local.invoice_id}，最终金额 ${local.total_cents} 分，币种 ${local.currency}。恢复后保存成功，保存编号 ${response.body.saved.receipt_id}，未重新发起结算。` };
  }
  if (task.id === 'authenticated-write') {
    // Inspect the current form before filling it, then submit the business
    // action exactly once. Polling below reads retained evidence only.
    const inspected = await call('Runtime.evaluate', { expression: 'Array.from(document.forms[0]?.elements || []).map(element => ({name:element.name,type:element.type,value:element.type === "password" ? "" : element.value}))', returnByValue: true }, targetRef);
    const names = new Set(inspected.result.result.value.map(item => item.name));
    if (!['title', 'priority', 'project'].every(name => names.has(name))) throw new Error('Reference authenticated business form absent');
    const input = { title: oracle.expected.title, priority: oracle.expected.priority, project: oracle.expected.project };
    await call('Runtime.evaluate', { expression: `(() => { const form = document.forms[0]; const input = ${JSON.stringify(input)}; for (const [name,value] of Object.entries(input)) { const element = form.elements.namedItem(name); if (!element.readOnly) { element.value=value; element.dispatchEvent(new Event('input',{bubbles:true})); element.dispatchEvent(new Event('change',{bubbles:true})); } } form.requestSubmit(); return true; })()`, returnByValue: true }, targetRef);
    const created = await recordedResponse(oracle.requirements.network.find(item => item.method === 'POST'));
    const verified = await recordedResponse(oracle.requirements.network.find(item => item.method === 'GET'), body => body.records?.some(item => item.receipt_id === created.body.saved.receipt_id));
    const record = verified.body.records.find(item => item.receipt_id === created.body.saved.receipt_id);
    if (!record) throw new Error('Reference could not verify persisted record');
    return { answer: `已复用登录会话创建并核验：${record.project}，标题“${record.title}”，优先级 ${record.priority}，工单编号 ${record.receipt_id}。只提交一次。` };
  }
  throw new Error(`Unknown reference case ${task.id}`);
}
