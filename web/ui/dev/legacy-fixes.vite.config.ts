import { defineConfig, mergeConfig } from "vite";
import base from "../vite.config.ts";

// A local fixture page only: matching iframe URLs return invented HTML.
// No request is proxied to a Pogo server or to a real model.
export default mergeConfig(base, defineConfig({
  server: { host: "127.0.0.1", port: 5191, strictPort: true },
  plugins: [{
    name: "legacy-fix-preview-fixture",
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const match = request.url?.match(/^\/api\/html-previews\/([a-f0-9]{64})\/index\.html$/);
        if (!match) { next(); return; }
        response.setHeader("Content-Type", "text/html; charset=utf-8");
        response.setHeader("Cache-Control", "no-store");
        response.end(`<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><title>隔离示例网页</title><body style="font:16px/1.8 system-ui;padding:16px;background:#f5faf7;color:#24543e"><h2>隔离预览正常</h2><p>这是本地虚构网页，不连接正式服务。</p><p>版本 ${match[1].slice(-4)}</p></body></html>`);
      });
    },
  }],
}));
