# 标准库 Python 项目示例

仅在需要后端/API 时使用。将代码写入真实工作区文件，例如 `site/app.py`；
不在普通 Bash 中启动服务器。下例提供入口和浏览器 POST API，计数保存在 `/data`。
按用户需求改动示例，不要覆盖已有应用。

```python
import json
import os
import sqlite3
from http.server import BaseHTTPRequestHandler, HTTPServer

DB = os.path.join(os.environ.get("POGO_DATA_DIR", "/data"), "counter.sqlite")

def count(increment=False):
    with sqlite3.connect(DB) as db:
        db.execute("CREATE TABLE IF NOT EXISTS counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)")
        db.execute("INSERT OR IGNORE INTO counter VALUES (1, 0)")
        if increment:
            db.execute("UPDATE counter SET value = value + 1 WHERE id = 1")
        return db.execute("SELECT value FROM counter WHERE id = 1").fetchone()[0]

class App(BaseHTTPRequestHandler):
    def reply(self, body, content_type, status=200):
        data = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/api/counter":
            self.reply(json.dumps({"count": count()}), "application/json")
        elif self.path == "/":
            self.reply('''<!doctype html><meta charset="utf-8">
<title>计数示例</title><h1>持久计数</h1><button id="add">加一</button><pre id="value"></pre>
<script>
async function refresh(method = 'GET') {
  const response = await fetch('./api/counter', {method});
  if (!response.ok) throw new Error('请求失败：' + response.status);
  document.querySelector('#value').textContent = JSON.stringify(await response.json());
}
document.querySelector('#add').onclick = () => refresh('POST').catch(showError);
function showError(error) { document.querySelector('#value').textContent = error.message; }
refresh().catch(showError);
</script>''', "text/html; charset=utf-8")
        else:
            self.reply("Not found", "text/plain", 404)

    def do_POST(self):
        if self.path == "/api/counter":
            self.reply(json.dumps({"count": count(True)}), "application/json")
        else:
            self.reply("Not found", "text/plain", 404)

if __name__ == "__main__":
    HTTPServer((os.environ.get("HOST", "0.0.0.0"), int(os.environ.get("PORT", "8080"))), App).serve_forever()
```

使用实际 `web_service` 工具启动，示例参数：

```json
{"action":"start","operation_id":"counter-start-1","name":"counter","project_root":"site","profile":"python-web","start_argv":["python3","app.py"]}
```

这是调用参数示例，不是启动成功的证据。`service_id` 必须使用实际回执；
依次核对 status 为 ready 后 open。需要更新代码就修改 `site/app.py`，
再使用新的 operation_id 对同一 service_id 调用 restart；新代次就绪后重新 open。

此 profile 没有依赖安装或 build；不要为标准库示例生成非空 requirements.txt。
应用读取 HOST/PORT 监听平台指定的内网端口，不为每个网站选择宿主端口。不从服务端请求本机 API，
浏览器通过同一平台 URL 访问相对 `./api/counter`；服务端仍匹配 `/api/counter`，
gateway 会剥离 `/_pogo/web/<48hex>/` 前缀。不使用根绝对路径 `/api/counter`。
不要为了消除 opaque-origin
限制而改变 sandbox、引入登录 Cookie 或放宽主站的认证策略。
