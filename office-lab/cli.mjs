import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import crypto from 'node:crypto';

const defaultData = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../.localagent/checks/onlyoffice-prototype-20261012');
async function main() {
  const [action = 'list', documentId = 'word', operation, argsJSON = '{}', operationId = crypto.randomUUID()] = process.argv.slice(2);
  const runtime = JSON.parse(await fs.readFile(path.join(process.env.OFFICE_LAB_DATA || defaultData, 'private/runtime.json'), 'utf8'));
  const request = async (route, body) => {
    const result = await fetch(new URL(route, runtime.url), { method: body ? 'POST' : 'GET', headers: { Authorization: `Bearer ${runtime.adminToken}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(45000) });
    const data = await result.json(); if (!result.ok || data.ok === false) throw new Error(typeof data.error === 'string' ? data.error : data.error?.message || 'Office 工具未完成。'); return data;
  };
  if (action === 'list') { console.log(JSON.stringify(await request('/api/bootstrap'), null, 2)); return; }
  if (!['word', 'excel'].includes(documentId)) throw new Error('测试文档必须是 word 或 excel。');
  const status = await request(`/api/documents/${documentId}/status`);
  if (action === 'status') { console.log(JSON.stringify(status, null, 2)); return; }
  const sessionId = status.activeSessionId;
  if (!sessionId) throw new Error('先在隔离网页打开指定文档。');
  const routes = { describe: 'describe', save: 'save', close: 'close', command: 'commands' };
  if (!Object.hasOwn(routes, action)) throw new Error('命令应为 list/status/describe/save/close/command。');
  const body = action === 'command' ? { operationId, operation, args: JSON.parse(argsJSON) } : action === 'describe' ? undefined : {};
  console.log(JSON.stringify(await request(`/api/sessions/${sessionId}/${routes[action]}`, body), null, 2));
}
main().catch((error) => { console.error(error.message); process.exitCode = 1; });
