// Flash-check harness: serves the built dist plus just enough mocked /api
// endpoints to boot the Studio manage pages. GET /api/config answers slowly
// (default 1200ms) so an unmounted-then-remounted pane would be obvious.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const distDir = fileURLToPath(new URL("../dist", import.meta.url));
const port = Number(process.env.PORT || 4173);
const configDelay = Number(process.env.CONFIG_DELAY_MS ?? 1200);

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".woff2": "font/woff2",
  ".json": "application/json",
};

const providersFile = {
  default: "glm-5.3",
  providers: {
    zhipu: { base_url: "https://open.bigmodel.cn/api/paas/v4", key_env: "ZHIPU_API_KEY" },
    bailian: { base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1", key_env: "BAILIAN_API_KEY" },
  },
  models: [
    { id: "glm-5.3", provider: "zhipu", context_window: 1048576, max_tokens: 16384 },
    { id: "qwen-max", provider: "bailian", context_window: 32768, max_tokens: 8192 },
  ],
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
// MCP_FAIL_AFTER: fail GET /api/mcp with 500 once the nth request arrives (1-based).
const mcpFailAfter = Number(process.env.MCP_FAIL_AFTER ?? 0);
let mcpRequests = 0;
const json = (res, body, status = 200) => {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
};

const server = createServer(async (req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${port}`);
  const path = url.pathname;

  if (path.startsWith("/api/")) {
    if (req.method !== "GET") return json(res, { ok: true }, 200);
    switch (path) {
      case "/api/auth/status":
        return json(res, { mode: "token", password: false, user: "admin", admin: true });
      case "/api/manage/agents":
        return json(res, {
          can_manage: true,
          agents: [{
            id: "demo-agent", name: "演示 Agent", description: "用于闪烁测试", model: "glm-5.3",
            status: "ready", revision: 1, category: "general", conversation_count: 0,
          }],
        });
      case "/api/manage/templates":
        return json(res, { templates: [] });
      case "/api/models":
        return json(res, {
          default: "glm-5.3",
          models: [
            { id: "glm-5.3", provider: "zhipu", configured: true },
            { id: "qwen-max", provider: "bailian", configured: true },
          ],
        });
      case "/api/config":
        await sleep(configDelay);
        return json(res, {
          path: "providers.json",
          raw: JSON.stringify(providersFile, null, 2),
          file: providersFile,
          default: "glm-5.3",
          providers: [
            { name: "zhipu", key_value: "", configured: true },
            { name: "bailian", key_value: "", configured: true },
          ],
        });
      case "/api/prefs":
        return json(res, { prefs: { spill_retention_days: 14 }, spill: { files: 3, bytes: 1048576, dir: "/tmp/spill" }, last_sweep: null });
      case "/api/sandbox":
        return json(res, { env_mode: "degrade", override: "", effective: "degrade", active: false, proc: false, net: false });
      case "/api/attachments/policy":
        return json(res, { groups: [], default_exts: [".png", ".jpg"], allowed_exts: [".png", ".jpg"], max_mb: 10, min_mb: 1, cap_mb: 50 });
      case "/api/mcp":
        mcpRequests += 1;
        if (mcpFailAfter && mcpRequests > mcpFailAfter) return json(res, { error: "mock refresh failure" }, 500);
        return json(res, {
          path: "mcp.json",
          config: {
            servers: {
              "demo-stdio": { kind: "stdio", command: "npx", args: ["-y", "demo-server"], enabled: true },
              "demo-http": { kind: "http", url: "https://mcp.example.com/mcp", enabled: true },
            },
          },
          status: [],
        });
      default:
        return json(res, { error: "not mocked" }, 404);
    }
  }

  let file = join(distDir, normalize(path).replace(/^([.][.][/\\])+/, ""));
  if (path === "/" || !extname(file)) file = join(distDir, "index.html");
  try {
    const data = await readFile(file);
    res.writeHead(200, { "Content-Type": MIME[extname(file)] ?? "application/octet-stream" });
    res.end(data);
  } catch {
    res.writeHead(404); res.end("not found");
  }
});

server.listen(port, "127.0.0.1", () => console.log(`flash-check server: http://127.0.0.1:${port} (config delay ${configDelay}ms)`));
