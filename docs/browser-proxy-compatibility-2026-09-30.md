# Pogo 浏览器代理兼容性：调研、修复与验收

日期：2026-09-30。本文描述本地源代码与本地验收，不代表已部署，也不代表用户现场的两个系统已完成业务验收。

## 问题与判断

原系统可以直接访问，经 Pogo 代理后出现模块脚本收到 `text/html`、CSS 404 / `text/plain`。此前 Pogo 的 `/panels/<name>/` 只做请求转发：页面中的 `/assets/...`、`/api/...` 仍会访问 Pogo 根路径。缺失静态文件可能返回 404 文本；其他路径可能落入 Pogo SPA 的 HTML fallback，因此 MIME 报错通常是错误响应的结果。

另一个问题是入口与站点根地址混用：人工注册完整 `/app/?entry=...`，Agent 导航注册 origin；旧代理会把入口路径与参数拼到后续请求上。前端还会无条件添加 `#/`，覆盖系统真实的 hash 路由。

这解释了截图与现有源码之间的关系；没有现场原地址、代理地址及 Network 响应，不能据此排除登录重定向、上游虚拟主机、CSP 等额外因素。

## 领先 Agent / 浏览器方案采用的架构

这是一轮官方文档和源码调研，没有对产品做性能或成功率排名。

| 来源 | 可确认的实现方式 | 对 Pogo 的意义 |
| --- | --- | --- |
| [OpenAI Computer use](https://developers.openai.com/api/docs/guides/tools-computer-use) | 在浏览器或桌面环境执行操作，通过截图/执行结果继续决策；示例包含 Playwright 控制浏览器 | 保留浏览器原生页面加载、源站和执行语义，用观察—操作—复核循环完成任务 |
| [Microsoft Playwright MCP](https://github.com/microsoft/playwright-mcp) | 支持持久 profile、隔离上下文、连接已有 Chrome/Edge；支持 storage state 和初始化脚本 | 会话生命周期、登录态与隔离是浏览器能力的基础，不能只靠路径改写 |
| [Browser Use BrowserSession 源码](https://github.com/browser-use/browser-use/blob/main/browser_use/browser/session.py) | 使用 CDP 客户端和目标 session 管理浏览器、页面与生命周期 | 把真实浏览器作为运行时资源，有助于处理页面导航、跨域与重连 |
| [Browserbase Contexts](https://docs.browserbase.com/platform/browser/core-features/contexts) | 显式管理可跨浏览器 session 复用的上下文，包括 Cookie、localStorage、IndexedDB 等 | 登录态复用必须与用户/任务作用域、持久化策略一起设计 |
| [Stagehand 官方配置参考](https://github.com/browserbase/stagehand/blob/main/packages/docs/v3/references/stagehand.mdx) | 支持 Browserbase 和本地 Chrome 环境 | 浏览器后端可以抽象为可替换实现，业务工具协议不必绑定 iframe |

共同特点是控制真实浏览器，让页面在原地址加载。Pogo 当前 BrowserPane 通过同源 iframe 的 `contentDocument` 操作与选取元素，因此直接切成独立 origin 会破坏现有能力。本轮先修复常见 SPA 兼容性，后续再建设真实浏览器后端；本轮没有实现 CDP / Playwright 运行时。

## 已实现的修复

### 可选模式与一致的入口

- `proxy_mode: "compat"`：一个 mount 代表上游 origin；入口 path、query、hash 通过独立 `entry` 返回。
- `proxy_mode: "passthrough"` 或旧记录没有此字段：继续原始转发。
- 新建系统、JSON 导入、已授权的 Agent 自动注册在创建新 panel 时使用兼容模式；导入和 Agent 导航传 `preserve_existing: true`，复用已有系统时保留其模式。旧存储不会批量迁移；已有运行时系统可以在编辑表单中显式切换，保持同一 panel 名并持久化。
- API 省略 `proxy_mode` 时保留已有模式；命令行启动注册的面板不能由运行时 API 覆盖模式。
- 表单保留真实入口的 query 和 hash，注册失败显示原因，不静默退回跨域直开。直接打开的表单也可以正常保存。

### 转发与内容适配

- 只在已注册 mount 内适配。没有接管 Pogo 根目录 `/assets`、`/api`，也没有通过 Referer 猜测系统归属。
- HTML/CSS 使用词法分析器；JavaScript 使用 AST。处理常见资源属性、base、表单、srcset、CSS `url()` / `@import`、静态 ES module import/export、字面量动态 import，以及 import map。
- 对外部 origin 的资源保持原地址。来源 host 大小写和默认端口做归一化；中文 panel 名和编码路径保留。
- 页面脚本前注入有限范围的运行时适配，处理 fetch、XHR、WebSocket、EventSource、sendBeacon、History 和常见动态 URL 属性。
- `fetch(Request)` 保留 method、headers、credentials、signal 和 init 覆盖；重新定位带 body 的 Request 时限量缓冲，避免 Chromium 将其变成仅支持 HTTP/2 的流式上传。普通 `fetch(url, init)` 的 body 保持原生传递。
- 同源 Location、Origin、Referer 与 Cookie Path 适配；过滤上游请求中的 Pogo `pi_session`，并拒绝上游覆盖该 Cookie。
- 容器 loopback 映射只改变连接地址，保留原始虚拟主机与 TLS 证书校验名称，不关闭 TLS 校验。
- 文本请求优先 identity；若上游仍返回 gzip，限量解压。改写后删除失效的长度/压缩/校验标识，使用 no-store。二进制、附件和 SSE 不缓冲改写；保留 Range，部分文本响应则明确拒绝适配。

### 诊断与边界

响应包含 `X-Pogo-Panel-Mode: compat`。代理失败返回 502 与 `X-Pogo-Panel-Error`：

| 代码 | 含义 |
| --- | --- |
| `PANEL_UPSTREAM_UNAVAILABLE` | 连接、DNS、TLS 或上游读取失败；错误页不带上游响应正文、地址查询参数或凭据 |
| `PANEL_RESOURCE_TYPE_MISMATCH` | 脚本 / 样式请求收到成功状态的 HTML，需检查上游 fallback 或登录跳转 |
| `PANEL_COMPAT_UNSUPPORTED` | 编码、完整性、Cookie、脚本语法、文本大小或响应转换限制不满足 |
| `PANEL_UPSTREAM_RESOURCE_ERROR` | 上游对脚本 / 样式返回 HTTP 错误；保留原状态 |

兼容模式不是完整的站点虚拟化。以下边界仍需原始转发、系统原生子路径支持或独立浏览器：

- 强依赖真实 `location.pathname/origin` 的路由与 OAuth/SSO、跨域认证、独立 localStorage / IndexedDB；不同 panel 仍共享 Pogo origin，不是租户隔离。
- Worker / Service Worker、运行时计算的模块 import、动态 innerHTML/CSS 中未经过属性 setter 的根地址、iframe srcdoc、内联 SVG/MathML 等不做全量重写。
- localStorage/IndexedDB、document.domain、CSP 指定源站、哈希保护的内联脚本、Trusted Types 等保留原有语义；本轮不承诺透明兼容。
- 保留 CSP，只为注入脚本增加 nonce；当原策略仅依赖 unsafe-inline 时避免加入 nonce 使原脚本失效。没有移除 CSP 或 SRI。需改写的本地 SRI 资源明确报不支持；`__Host-*` Cookie 不能缩到子路径，同样明确报不支持。
- 支持 UTF-8/ASCII 文本；每个文本响应解压后最多 8 MiB。上游无视 identity 而返回非 gzip 压缩、非 UTF-8、no-transform 或部分文本响应时不尝试猜测。
- 需要改写 URL 的 `fetch(Request)` 请求体最多 16 MiB；更大或真正流式上传使用普通 `fetch(url, init)`、原始转发或真实浏览器。

## 使用与回退

1. 更新 Pogo 后，在右侧浏览器快捷方式上点击编辑，确认原地址是原系统能直接正常访问的完整入口。
2. 选择“兼容代理”并保存，再从更新后的卡片重新打开系统。旧的已打开标签不会自动改成新的入口。
3. 如果系统本身已经正确支持子路径或触发上述边界，切换“原始转发”可回退。本系统其他使用同一注册代理的标签也会受模式切换影响。
4. 注册与删除的权限保持原样；Agent 自动注册仍受原来的 `auto_register` 策略控制。

请求示例：

```json
{"name":"业务系统","url":"http://system.example/app/?lang=zh#/dashboard","proxy_mode":"compat"}
```

返回的 `path` 是 mount；导航使用 `entry`，不要再自行追加 `#/`。旧客户端不传模式时保持原行为。

## 本地验证

- Go panel 回归：35 个测试/子用例通过，0 失败；默认跳过需要显式启动的浏览器 fixture。覆盖原始模式、注册/持久化/切换/回退、入口参数、中文编码、原始 host、HTML/CSS/JS 重写、Cookie、gzip、边界诊断。
- 前端：类型检查通过，55 个相关测试通过，生产构建通过。覆盖导航 URL、运行时 bootstrap 的 Request body / init / abort / 限额 / 双重前缀 / 外部 origin / WebSocket 原型；构建有大 chunk 提示，不影响本次通过。
- 真实 Chromium 页面：两个独立 httptest 上游经实际 Go proxy 同时加载，验证 ES module、字面量 lazy import、CSS import 与图片、fetch(Request) POST、XHR、各自的 HTTP/JS Cookie、SSE、WebSocket、动态图片与 History。另检查重定向后仍留在各自 mount。
- Pogo 生产前端 + 隔离 API fixture：新建兼容代理、完整入口保存、切换原始转发、注册失败仍保留表单与错误信息。
- 证据位于仓库外的 `artifacts/pogo-panel-compat-20260930/`，包括构建日志、测试日志、浏览器截图与 fixture。生产前端构建输出也放在那里，没有覆盖工作区原有 `web/ui/dist` 改动。
- **没有部署，没有连接用户现场的两个系统，没有验证它们的实际登录与业务流程。**

可重跑浏览器 fixture（PowerShell，位于 pogo 目录）：

```powershell
$env:POGO_PANEL_BROWSER_FIXTURE = 'C:/path/to/browser-fixture.json'
go test ./web -run '^TestPanelCompatBrowserFixture$' -v -count=1 -timeout=22m
```

读取文件中的本地 URL 打开验收页；向该 URL 的 `/__stop` 发 POST 可停止。未设置环境变量时此测试跳过；运行上限 20 分钟。

## 后续真实浏览器后端建议

引入 BrowserSessionProvider，使现有 iframe 操作与后续 CDP/Playwright 后端共享导航、快照、点击、输入、截图等工具契约。每个用户/工作域显式管理 BrowserContext、登录态与存储持久化；在原系统 URL 上运行页面，前端通过截图或受控交互视图展示。元素选取结果由浏览器执行端转换成现有的元素摘要协议，不再跨 origin 读取 iframe DOM。

实施时验收完整业务流程、断线重连、标签生命周期、下载上传、上下文清理和授权边界。以可回放的网络/控制台错误、截图、动作结果形成诊断证据，保留用户接管登录入口。这才是解决复杂站点兼容性和真正存储隔离的下一阶段。
