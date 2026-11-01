# Pogo Office 同文档历史桥

本目录是明确的、固定版本的 ONLYOFFICE Community SDK 扩展，**不是原封不动 Community 的官方插件 API**。它补充当前文档只读快照与原生操作历史；不改 native history 实现，不接受 agent JavaScript，不用覆盖旧文件或补偿写入模拟撤销。

Pogo 主界面的配置、文件面板用法、`docs.office` 工具及保存/重开边界见 [在线 Office 编辑使用说明](../docs/office-agent-integration.md)。代码集成与隔离验收不代表已部署到正式 Docker 服务。

## 固定引擎与加载

`engine-manifest.json` 固定 ONLYOFFICE `9.4.0` build `129`、镜像 digest、官方 `sdkjs v9.4.0.129` commit，以及 Word/Sheet 两个 `sdk-all-min.js` 和两个 `sdk-all.js` 的 SHA-256。另固定五份加载资源的原始摘要：DocsAPI `api.js`、原生 Service Worker、注册脚本及 Word/Sheet 两份编辑器 HTML。九份资源全部核对后，才生成扩展 SDK 和受控加载资源；任一资源或替换点变化时拒绝启动，不能静默继续用旧补丁。

隔离验证的 `engine-proxy.mjs` 是 loopback HTTP/WebSocket 反向代理。默认 `19898 → 19894`，先核对原始 SDK 全部 hash，再追加扩展；其他资源与 WebSocket 保持转发。它不修改 DocServer 容器或正式 Pogo 服务。浏览器 URL 可使用 `http://127.0.0.1:19898`，后台下载的 service URL 使用真实 DocServer `http://127.0.0.1:19894`；后台只准入这两个精确来源，不接受 agent 自选下载地址。

加载时 DocsAPI 的固定 vendor 版本路径改为 `<vendorVersion>-pogo-<extensionSHA256>`，代理把该虚拟版本路径映射回真实 vendor 目录。HTTP 与 WebSocket 使用同一映射，版本之外的运行环境仍为 `9.4.0` build `129`。明确加载资源除外，其他资源原样转发；代理不靠更换来源或清理用户缓存升级。

ONLYOFFICE 原生 Service Worker 对版本路径中的 SDK 使用 CacheStorage 的 `cacheFirst`；已有响应会绕过网络，HTTP `no-store` 不足以使旧 SDK 失效。虚拟版本让新 SDK 使用新的请求键和 worker scope。受控 worker 为每份扩展使用独立缓存名称前缀，原生激活逻辑不会匹配或删除旧缓存；注册脚本及两份 HTML 的旧注册清理只作用于同 scope，不撤销其他版本的 worker。原生缓存和文档文件缓存逻辑没有被禁用。

### Pogo Web 单入口

Pogo Web 的 Office 网关固定为 `/office/editor/`。浏览器加载同源 `.../office/editor/web-apps/apps/api/documents/api.js`，原生 DocsAPI 根据 script URL 计算完整 base path，再插入固定 runtime 版本；不按请求读写或替换完整 SDK。原生编辑器 HTML 中的 `../../../../document_editor_service_worker.js` 最终解析为 `/office/editor/<runtime>/document_editor_service_worker.js`，默认 scope 只覆盖该版本目录。适配器移除上游扩大范围的 `Service-Worker-Allowed`；gateway 和独立 Lab 的 worker 使用不同缓存命名空间，同源其他 Pogo API 及旧缓存不会受影响。

两层代理保留当前编辑实例经过验证的页面来源与固定路径，不使用静态 Public URL 作为客户端地址。Pogo Web 先校验访问权限与当前请求 Host，去掉请求路径前缀，向适配器发送签名合同：`X-Forwarded-Host: <viewer-host>/office/editor`、`X-Forwarded-Proto: http|https`、`X-Forwarded-Prefix: /office/editor`。浏览器原始 forwarding 值不能决定工具容器的来源。适配器验证后，按**固定 ONLYOFFICE 9.4** 的原生 nginx/DocService 合同规范化为 `X-Forwarded-Host: <viewer-host>` 和独立 `X-Forwarded-Prefix: /office/editor`。该版本已经把 `$the_host` 和 `$the_prefix` 合并，若把路径同时放在两者中，会产生重复 `/office/editor/office/editor/`；不能盲目套用旧版本只传 host+path 的配置。

适配器只接受签名过的 gateway 元数据。`X-Pogo-Office-Gateway` 为同一专用 Office JWT 密钥计算的 base64url HMAC-SHA256，消息精确为 `pogo-office-gateway/v1\n<proto>\n<hostWithPrefix>\n/office/editor`，使用独立域分隔符。它只在内部代理之间发送，进入原生 DocServer 前删除，不进入 URL、浏览器、日志或响应。Docker 入口在内存从同一专用文件读取密钥；隔离验证显式传 `gatewayPrefix` 和 `gatewaySecret`。普通 Lab/内部导出请求没有该签名，任意 forwarding 头都会被忽略，保持自己的 HTTP origin；伪造、缺失配置、非法域名或其他前缀均拒绝。

HTTP 与 WebSocket 使用相同元数据和版本映射。相对重定向、私网引擎绝对地址及公开来源重定向统一保留 `/office/editor/`；导出地址的签名 query 不变。原始 DocServer 上游始终为同容器 loopback，网关不会按用户输入切换 upstream。九份固定资源仍在启动时校验并缓存，服务请求时直接复用内存中的固定字节；传输和动态资源继续流式转发。

已经打开的 Pogo 页面会复用其 `window.DocsAPI`。升级时先保存并关闭当前文档，再重新加载宿主页面以取得新 DocsAPI；不能自动替换仍有未保存修改的编辑器。正式多版本在线升级及旧活跃画布保留仍需独立设计，此处只在已授权的隔离环境验证。

`install-extension.mjs` 只产生独立 SDK staging 目录，拒绝覆盖输入与已存在输出，不会部署：

```sh
node pogo/office-runtime/install-extension.mjs --input /isolated/original/sdkjs --output /isolated/staged/sdkjs
```

此命令只产生四份 SDK staging 资源，不是完整产品发布；它不能替代代理中的加载版本和 Service Worker 隔离。未来静态打包还需纳入同一份受控加载资源及完整升级验证。

未来正式打包需保留此目录源码与原始 SDK 许可、分发源代码，并满足 ONLYOFFICE AGPL-3.0 及附加品牌/许可条款。商业闭源集成仍需按研究报告核实相应授权。此原型不宣称已完成商业许可或正式发布。

## Docker 封装（待正式部署验收）

`Dockerfile` 由两套平台配置选择固定的 ONLYOFFICE 引擎和资产清单；默认保持已验证的 `linux/arm64` 引擎，`linux/amd64` 使用同版本的独立引擎 digest。Node 运行时来自固定的多架构镜像。构建会核对目标平台、基础镜像实际架构及资产清单的镜像身份，运行时仍逐项核对全部九份资源。官方 DocServer 内部监听 `80`，其原生 supervisor 同时管理 Pogo 扩展代理；唯一对外入口是代理 `8080`。代理仍只连接同容器 `127.0.0.1:80`，等待原始引擎就绪并核对全部九份原始资源后才监听。仅 `docker-proxy.mjs` 显式允许容器 `0.0.0.0`；隔离 CLI 默认入口和 upstream 限制不变。`/pogo-office/healthz` 同时核对原始引擎健康和已完成的资源验证，不把仅端口启动当作可编辑。

官方启动脚本默认按时间产生缓存版本，并从原始模板重新生成 DocsAPI；新容器不能沿用旧时间戳的完整 `api.js` 摘要。Docker 入口从 manifest 取得固定 vendor cache tag，经官方 `HASH` 参数交给原始生成流程。生成结果仍须与 manifest 的九份摘要逐一完全一致，不归一化或跳过 DocsAPI 校验。代理诊断日志写入专用 Office 日志卷的 `documentserver/pogo-office-proxy`，随官方进程输出查看，不包含 JWT 值。

专用 JWT 文件只读挂载到 `/run/secrets/pogo-office-jwt`。包装入口在内存读取并交给官方入口，不打印值、不把密钥写进镜像或 Compose environment。由于官方入口把值插入配置脚本，只接受 32–8192 个 ASCII 字母、数字、`_`、`-`，部署应生成新的随机十六进制密钥，不能复用隔离 Lab 密钥。Pogo 容器用户必须能读取此文件。四个 Office 命名卷必须新建，不能挂载原型的匿名卷、文件或人工编辑状态。

`w_deploy/compose.office.yml` 只公开 Pogo Web 入口；Office 适配器仅通过 Docker 私网 `expose:8080` 访问，不映射独立宿主机端口。客户端 Office URL 由 Pogo Web 固定为同源 `/office/editor/`，插件桥也使用相对路径。当前访问来源经过请求 Host、Origin 与实例身份校验后绑定；SSH 转发或更换访问端口不需要重配 Public URL。无需 `POGO_OFFICE_BROWSER_URL`，明确填写 Service / Fetch URL 时也无需 Public URL。内部下载/回调为 `http://pogo:7777`，后台导出为 `http://pogo-office:8080`，不会访问用户机器的 localhost。容器允许私有 IP 下载当前 Pogo 文件，禁止 metadata IP。原始 DocServer `80` 不应另行对外暴露。模板及代码改动不代表现有容器已迁移或部署。

```sh
docker build --platform linux/arm64 -t pogo-office:<reviewed-candidate> pogo/office-runtime
# AMD64 uses the matching immutable engine and its independently checked asset lock.
docker build --platform linux/amd64 \
  --build-arg OFFICE_ENGINE_IMAGE=onlyoffice/documentserver@sha256:8c114db7648a1654828d33e1f9f3b18e97c4caae6707d34bf04d793d260f7e67 \
  --build-arg OFFICE_ENGINE_MANIFEST=engine-manifest.amd64.json \
  -t pogo-office:<reviewed-amd64-candidate> pogo/office-runtime
node --test pogo/office-runtime/docker-packaging.test.mjs
node --test pogo/office-runtime/gateway-proxy.test.mjs
# Optional: reads the existing isolated engine; does not create/restart containers.
OFFICE_PROXY_TEST_UPSTREAM=http://127.0.0.1:19894 node --test pogo/office-runtime/docker-packaging.test.mjs
OFFICE_PROXY_TEST_UPSTREAM=http://127.0.0.1:19894 node --test pogo/office-runtime/gateway-proxy.test.mjs
```

这份封装与 Compose 模板自身不执行部署。现有发布器只支持 app/worker 镜像，首次加入 Office 是新增服务、环境与密钥挂载的配置迁移，不能直接写进现有 `compose.release.yml` 或绕过配置漂移门禁。部署前必须备份原配置、原镜像和实际数据挂载；失败时恢复这些配置和旧 app/worker 镜像，保留新 Office 卷供诊断，不删除原型与未保存编辑器。以后发布前还需检查全部身份下的 Office 活跃文档：Agent busy 检查不包含人工输入，主服务重启会撤销内存中的编辑会话，不能依赖冷备份保存未落盘画布。正式 Office 首次就绪后仍需在正式来源验证 Word/Excel 原生修改、历史、保存、重开与原有数据保留，封装测试不能代替该验收。

## 大文件只读选区的引擎限额

镜像入口的 `prepare-limits.py` 在官方 DocServer 启动前，仅合并 `local.json` 中的 FileConverter 拉取下载与 DOCX/XLSX XML 展开限额：下载至少 256 MiB、连接/空闲与整体下载至少 5 分钟、两种格式展开 XML 至少 2 GiB。更高的既有自定义限额和数字 `0` 表示的无限下载/无超时设置保持不变；其他格式、图片、普通临时上传、网关请求和 JWT 配置保持原值。混合类型组拆分后，仅提高 `.docx/.xlsx` 的限额，保留 `*.xml` 模板。非法配置拒绝启动并保留原配置；发布采用同目录原子替换，保留原文件权限和属主。

这些是文档引擎的准入上限，不保证所有 256 MiB 文档都能在默认 4 GiB 引擎内存中流畅打开。大表的行列数量、公式和展开体积仍影响转换时间与内存。此源码只有在以后重建并发布 Office 镜像后生效，不会自动修改已运行的容器。可用 `python3 pogo/office-runtime/limits_runtime_test.py` 运行隔离配置回归。

## 插件可见的固定扩展 API

| method | 输入 | JSON envelope result |
|---|---|---|
| `PogoOfficeCapabilities` | 无 | 扩展 id/version、准确引擎版本/build、editor、只读不扰动 history/selection、nativeUndo/Redo、`historyWithoutRead:true` |
| `PogoOfficeRead` | Word `{}`；Sheet `{sheet,range}` | 现有 `word.read` / `sheet.read` snapshot，无 history point |
| `PogoOfficeHistory` | `undo` 或 `redo`；Word `{}`，Sheet `{sheet,range}` 或无观察范围的 `{}` | `{native:true,historyScope:"editor-global",canUndo,canRedo,snapshot}`；没有 Sheet 观察范围时 `snapshot:null` |
| `PogoOfficeApply` | `word.replace` 或 `sheet.apply`，加原 typed mutation args | 同一 SDK 事件内先核对前置条件，再进入 native builder 事务，返回 `{before,after}` |

返回 envelope 为 JSON 字符串 `{ok:true,result}` 或 `{ok:false,error:{code,message}}`。不返回 SDK 对象，不运行传入脚本。读取使用 public Office API getter，直接固定 plugin method 不进入 `callCommand` 的 BuilderScript history action。原生历史调用 SDK 已有 Word `Undo/Redo`、Sheet `asc_Undo/asc_Redo`；它们是同一 native UI 路径。桥接动作是全局最后一步，**不是某个 agent 操作的选择性撤销**；既有人类输入也属于真实历史。

写入也不走插件 `callCommand`。固定 `PogoOfficeApply` 在同一 editor 事件中先校验全文/矩阵及唯一目标，失败时不开始 history action；校验与事务之间不 `await`，不会引入“先读取、稍后再写”的事件竞态。合法写入复用固定 SDK 原有 `canRunBuilderScript`、`_beforeEvalCommand`、`_afterEvalCommand`、`onEndBuilderScript` 与插件异步完成接口，结束原生事务/重算后读取结果。这些是**明确声明的引擎内部维护依赖**，不是未修改 Community 的公共插件 API，已受原始文件 hash 与入口形状 guard 保护；没有复制或改写原生 history 实现。

## 对 Pogo 的稳定合同

唯一 canonical 插件资源位于 `bridge/{config.json,index.html,plugin.js,sdk.js}`，Go 在本目录通过 `assets.go` embed。隔离 Lab 的对应入口引用这些相同文件。后台动态生成 `scope.js`，同源路径携带会话 capability，`index.html` 依次加载 scope、SDK、插件。GUID 为 `asc.{A6691D12-9A8F-4A33-91D4-548D3BB68D01}`，消息协议保持 `pogo-office-lab/v1`。

外部操作名保持 `word.read`、`word.replace`、`sheet.read`、`sheet.apply`、`history.undo`、`history.redo`、`capabilities`。Word snapshot 的 `text/selectedText/paragraphs` 和 Sheet snapshot 的 `sheet/range/inputs/values/display/sheets` 不变，因此 `expectedText` 与 `expectedInputs` 仍核对当前未保存内容。Sheet history 可传 `{sheet,range}` 决定**观察范围**；省略时用本插件最近一次 Sheet read/apply 范围。新编辑器尚未读写过范围时仍执行真实全局历史，返回 `snapshot:null`，不会自动猜测或读取选区。原生撤销本身不需要先读取；Agent 要核对内容时另发显式 `sheet.read`。不能把观察范围误解成选择性撤销范围。

能力只有真实 `PogoOfficeCapabilities` 握手成功时打开：`engineExtension: {id:"pogo-office-history",version:1,required:true,detected:true,...}`、`readPreservesHistory`、`readPreservesSelection`、`mutationPreconditionsPreserveHistory`、`nativeUndo/Redo` 和顶层 `historyWithoutRead:true`。插件与 Go 宿主都要求这项明确的新能力；仅匹配旧 `id/version:1` 不够。未扩展、缺少新能力或版本不匹配时 read/edit/history fail closed，不偷偷降级到会清除 redo 的只读或拒绝 mutation `callCommand`。

保存只有公开 `downloadAs → onDownloadAs → Host 下载持久化` 一条路径。原生历史修改成功与画布显示，不等于已保存；仍需保存回执与关闭重开核对。DocServer 自动回调只做签名校验并 ACK，不另行落盘。

## 验证与升级边界

```sh
node --test pogo/office-lab/plugin/contract.test.mjs pogo/office-lab/server.test.mjs
node pogo/office-runtime/verify-history.mjs
node pogo/office-runtime/verify-no-extension.mjs
node pogo/office-runtime/verify-cache-upgrade.mjs
```

真实验证从新 synthetic fixture 启动独立 `19896` Lab 和 `19898` SDK 代理，复用已授权的隔离 DocServer；不销毁用户在 `19895` 的编辑器。已有代理运行时设置 `OFFICE_ENGINE_PROXY_PORT=0` 使用随机空闲端口。验证必须覆盖：Word 用户选区及未保存输入、修改→撤销→重复读取→重做；Excel 首次原生用户输入后、从未 Agent 读取时点 UI 撤销/重做，返回 `snapshot:null` 且真实单元格变化正确；未保存用户输入、公式与跨工作表计算、程序化 native Undo/Redo 连续循环、读取不改变选区；保存后重新打开；运行前后源码 hash 相同。未扩展引擎的独立拒绝脚本默认占用 `19890`，Word/Sheet 的读、写、无范围历史仍应失败。键盘验证为真实 Chromium 输入，不等同物理中文输入法响应性能验收。

2026-10-13 当前版本完整验收为 **44/44**，允许 Service Worker，证据位于 `.localagent/checks/onlyoffice-history-v4-20261013-cache-version-final/verification/summary.json`；未扩展引擎的两类文档负向验收为 **11/11**，证据位于 `.localagent/checks/onlyoffice-history-no-extension-v4-20261013/verification.json`。新增历史结果中的 `snapshot:null` 明确表示未指定观察范围，不能作为当前文件内容或保存证明。

独立同来源已有缓存升级为 **13/13**，证据位于 `.localagent/checks/onlyoffice-cache-upgrade-20261013-final/verification.json`。新造的浏览器 context 先运行原始 worker，并缓存真实固定 SDK 加缺少新能力的旧 v1 适配器；实际 `fromServiceWorker` 与字节摘要证明缓存命中，旧握手被拒绝。随后在同端口、同来源切换新代理，验证新版本路径、新能力、首次无读取 UI 撤销/重做、旧缓存字节与旧注册保留、新 worker 独立 scope，以及源码摘要前后相同。该脚本默认使用 `19891` Lab 和随机引擎端口，只操作其新造测试 context，没有访问用户的 Chrome 缓存。

此前 41/41 和首次 UI 的 44/44（v3）证据均保留。v3 使用了 `serviceWorkers:'block'`，只证明编辑与历史行为，未覆盖持久浏览器的缓存升级；不能用它替代上述同来源升级证据。Pogo 主界面集成的实际 Chrome、Agent 链路和文件验收另见使用指南，不与独立 runtime 验收混计。

每次升级必须更新 manifest 的官方来源/精确 SDK hash，核对 SDK 导出的入口、原生事务完成与 plugin method dispatch，重跑上述真实行为验证，尤其是撤销→拒绝过期写入→读取→重做。不能仅以编译成功或 transport 单测代替引擎验收。

### 当前画布选区引用

固定只读操作 `selection.read` 通过 `PogoOfficeSelection` 读取当前用户选区，并以 `selectionRead: true` 单独声明能力。Word 使用 pinned `asc_GetSelectedText(false, options)`，不调用 `ApiRange.GetText` 的临时 Select / SaveDocumentState / LoadDocumentState 流程。Excel 从 `GetActiveSheet().GetSelection()` 读取唯一矩形选区、当前工作表名称和 A1 地址，再读取 values / formula inputs / display；最多 400 个单元格，文本最多 32,000 UTF-16 单位。空选区、空单元格、离散选区、整行列或超限均明确拒绝。只读 getter 不调用 BuilderScript、save、undo/redo 或 selection mutation。

宿主的 `selection.read` 参数固定为当前 `documentId / instanceId / canvasVersion / dirtyEpoch`。入队、真正分发、确认回执时都核对同一画布身份；期间任何文档、实例、保存基版或 dirty epoch 变化将拒绝旧引用。前端还核对 local edit epoch、生命周期和捕获目标对话，避免晚到内容进入其他会话。引用仅保存 path/title/editor/text、sheet/range 或只读 exact-text locator，以及版本、dirty、时间信息；不保存桥凭据或编辑实例句柄。`source_session` 为捕获时输入框所属 Pogo 对话 ID，并非 Office document session ID。

本次 upstream 镜像、SDK commit 和原资产 hash 保持不变，`engine-manifest.json.selectionRead.sourceFiles` 记录新增 getter 的审核来源；镜像仍通过 Dockerfile 同时复制 extension 和 manifest。代理现有 runtimeVersion 和服务工作线程缓存命名包含 extension SHA-256，所以修改后不会复用旧 adapter。发布时必须同时更新应用和文档服务镜像；旧引擎不声明 selectionRead，界面禁用此按钮并给出升级提示。仅更新前端不能使旧运行镜像拥有选区 getter。

真实隔离验证可运行 `.localagent/checks/nested-materials-20261016/office/verify-live.mjs`（开发工作区保留的验收脚本）。该脚本仅读取原隔离 Lab 的 SDK 资产，在新本机代理和独立合成文档中验证 Word/Excel 未保存内容、选区不变、读前后原生 Undo/Redo；不构建、部署或重启 Docker，不触碰正式文档。生产 Go 画布绑定和消息快照另由单测覆盖。
