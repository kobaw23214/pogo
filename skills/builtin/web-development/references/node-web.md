# 标准库 Node.js 项目示例

仅在当前工具明确提供 `node-web` 时使用。将应用写入真实工作区文件，例如
`site/app.js`；不在普通 Bash 中启动服务器，不安装 npm 包。按用户已有项目
决定 CommonJS 或 ESM，下例是无需 package.json 的 CommonJS 应用。

```javascript
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

const file = path.join(process.env.POGO_DATA_DIR || '/data', 'counter.json');
let count = 0;
try { count = JSON.parse(fs.readFileSync(file, 'utf8')).count || 0; }
catch (error) { if (error.code !== 'ENOENT') throw error; }

http.createServer((request, response) => {
  if (request.url === '/api/counter' && ['GET', 'POST'].includes(request.method)) {
    if (request.method === 'POST') {
      count += 1;
      fs.writeFileSync(file + '.next', JSON.stringify({count}));
      fs.renameSync(file + '.next', file);
    }
    response.writeHead(200, {'Content-Type': 'application/json'});
    response.end(JSON.stringify({count}));
  } else if (request.url === '/' && request.method === 'GET') {
    response.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
    response.end(`<!doctype html><meta charset="utf-8"><title>计数示例</title>
<h1>Node.js 持久计数</h1><button id="add">加一</button><pre id="value"></pre>
<script>
async function show(method='GET') {
  const response=await fetch('./api/counter',{method});
  if(!response.ok) throw new Error('请求失败：'+response.status);
  document.querySelector('#value').textContent=JSON.stringify(await response.json());
}
function failed(error){document.querySelector('#value').textContent=error.message}
document.querySelector('#add').onclick=()=>show('POST').catch(failed);
show().catch(failed);
</script>`);
  } else {
    response.writeHead(404, {'Content-Type': 'text/plain'});
    response.end('Not found');
  }
}).listen(Number(process.env.PORT || 8080), process.env.HOST || '0.0.0.0');
```

使用真实 `web_service` 工具启动，示例参数：

```json
{"action":"start","operation_id":"node-counter-start-1","name":"node-counter","project_root":"site","profile":"node-web","start_argv":["node","app.js"]}
```

核对真实回执的状态达到 ready 后再 open。`HOST`、`PORT`、CPU、内存和镜像由
平台决定；网站期限使用平台默认值或用户指定时长，不能猜测固定的时长上限。
浏览器资源、API 和 WebSocket 使用相对路径，以兼容统一网关的路径前缀。

内核会阻止应用及其子进程主动建立 TCP、UDP、Unix 连接；服务端不能 fetch
外部 API、连接数据库或回调平台。已接收连接上的 HTTP/WebSocket 回复可用。
持久数据写 `/data`；`/workspace` 仅为运行快照，改动项目后需要显式 restart。
不生成含第三方依赖的 package.json、不启动 npm/npx、不把标准库运行环境称作
支持任意 Node 框架。遇到需要依赖安装或构建的项目时，如实说明当前能力边界。
