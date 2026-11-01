// Create one explicitly docs-enabled Agent and conversation in the isolated
// check server. Credentials stay in its private runtime file, never stdout.
import fs from 'node:fs/promises';
import path from 'node:path';

const data = process.env.OFFICE_CHECK_DATA;
if (!data) throw new Error('OFFICE_CHECK_DATA must be the isolated directory.');
const file = path.join(data, 'private/runtime.json');
const runtime = JSON.parse(await fs.readFile(file, 'utf8'));
const origin = new URL(runtime.url);
if (origin.protocol !== 'http:' || origin.hostname !== '127.0.0.1' || !origin.port || origin.pathname !== '/' || origin.search || origin.hash || origin.username || origin.password || runtime.model !== 'deterministic integration fixture') throw new Error('This utility refuses non-fixture servers.');
async function request(method, pathname, body) {
  const response = await fetch(runtime.url + pathname, {method, headers:{Authorization:`Bearer ${runtime.token}`,...(body ? {'Content-Type':'application/json'} : {})},body:body ? JSON.stringify(body):undefined});
  const value = await response.json();
  if (!response.ok) throw new Error(`${method} ${pathname}: ${response.status} ${value.error || ''}`);
  return value;
}
const catalog = await request('GET','/api/manage/capabilities');
const docs = catalog.skills.find(item => item.name === 'docs');
if (!docs) throw new Error('The docs skill is missing from this fixture.');
const agent = await request('POST','/api/manage/agents',{
  name:'Office 集成验证', description:'隔离样本与确定性测试模型；不连接正式账号',
  instructions:'Exercise the live Office tools using only the artificial test documents.',
  model:'office-fixture', models:['office-fixture'], skills:[docs.id],
  mcp_servers:[], tools:[], status:'ready', color:'blue',icon:'spark',category:'general',starters:[]
});
const conversation = await request('POST',`/api/apps/${agent.id}/sessions`,{workspace:'.'});
runtime.agentId = agent.id;
runtime.conversationId = conversation.id;
runtime.previewURL = `${runtime.url}/manage/agents/${agent.id}/chat?c=${encodeURIComponent(conversation.id)}`;
await fs.writeFile(file,JSON.stringify(runtime,null,2),{mode:0o600});
console.log(JSON.stringify({isolated:true,agentId:agent.id,conversationId:conversation.id,previewURL:runtime.previewURL}));
