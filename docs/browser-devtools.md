# 浏览器调试协议

日期：2026-10-06。本文描述当前源码的 Agent 调试入口，不代表已发布或部署，也不把历史受控评测成绩当成本轮验证。

## 执行方式

原生 `browser` 工具提供 `action:"devtools"`，用 `method`、可选对象 `params` 和可选 `target` 调用标准 Chrome DevTools Protocol（CDP）或日志读取辅助方法。Agent 直接调用协议；用户面板与 Agent 仍使用同一个 Chromium 页面及登录态。

链路保持为：

```text
Agent → browser → BrowserGate → Go 会话桥 → 私有 HTTP worker
       → 会话队列 / 控制租约 → 当前 Page 与所属 iframe/worker 的 CDP 连接
```

没有向调用方暴露浏览器级 WebSocket、worker token 或任意 Target/BrowserContext 路由。当前标签页、其跨进程 iframe（OOPIF）和 dedicated worker 构成同一个目标集合，包含 iframe 内的 worker；每个目标有独立协议连接，整个集合共享有界记录。切换标签后只能选择新活动页所属的目标。关闭目标、取消、接管、交还和会话销毁会使相应旧引用、记录或连接失效。

当前实现是协议入口与记录层，**尚未嵌入完整 Chrome DevTools frontend**。它递归附加由当前页面实际宣布的 OOPIF 和 dedicated worker；shared worker、service worker、其他标签页及附加前的历史均不在覆盖保证内。`target_coverage` / `coverage` 明确报告已附加数量、初始化中数量、拒绝数量及部分覆盖状态。

## 方法

```json
{"action":"devtools","method":"help"}
{"action":"devtools","method":"targets.list"}
{"action":"devtools","method":"network.list","params":{"limit":50}}
{"action":"devtools","method":"console.list","params":{"limit":50}}
{"action":"devtools","method":"events.list","params":{"method":"Runtime.exceptionThrown","limit":50}}
```

`help` 返回实际支持的辅助方法、CDP domain、禁止方法与容量限制。它只描述当前执行器能力，不赋予新的任务权限。

| 方法 | 参数 | 结果 |
|---|---|---|
| `targets.list` | 无 | 当前标签页所属目标的 `target_ref`、类型、URL、父目标引用、就绪和暂停状态，以及采集覆盖信息 |
| `network.list` | 可选 `cursor`、`limit`（1–100） | 当前标签页所属目标已记录请求的 URL、方法、状态、target/frame/loader、重定向关系和 body 状态；不在列表展开正文 |
| `network.read` | `request_id`，必须来自列表 | 读取对应已缓存请求与正文；不会重新向网站发请求 |
| `console.list` | 可选 `cursor`、`limit` | 实际 Console、Log 与异常记录，保留类型和可用的 execution context/frame 信息 |
| `events.list` | 可选 `cursor`、`limit`、精确 `method` 筛选 | 已订阅的网络、Runtime、Debugger、DOM、CSS、Page 等 CDP 事件 |
| 标准 `Domain.method` | 标准 CDP 参数对象 | 直接执行所选目标的 CDP，返回结构化 `result` 或明确截断/失败状态；不同目标类型支持的 domain 不同 |

### 选择 iframe 或 worker

`targets.list` 返回 worker 签发的 `target_ref`，格式为不透明的 `dt_…`。将其原样放在调用的顶层 `target`，不能使用 CSS、元素 ref、tab ID、原始 Chromium targetId 或其他会话的引用。没有指定 `target` 时，标准 CDP 操作默认作用于根页面；Network、Console、events 列表默认聚合该标签页所属目标。指定 `target` 后只读取该目标的记录。

```json
{"action":"devtools","method":"targets.list"}
{"action":"devtools","target":"<worker 的 target_ref>","method":"Runtime.evaluate","params":{"expression":"self.location.href","returnByValue":true}}
{"action":"devtools","target":"<iframe 的 target_ref>","method":"DOM.getDocument","params":{"depth":1}}
{"action":"devtools","target":"<目标的 target_ref>","method":"console.list","params":{"limit":30}}
```

只有 `ready:true` 的目标可执行命令。`parent_target_ref` 表示已验证的附加关系；无法建立连接、达到容量上限或尚未初始化的目标不得被当成已覆盖。失效或不属于当前标签页的引用返回明确错误，不会静默改为操作根页面。

日志分页按返回的 `next_cursor` 继续；到达末页后，保存 `cursor` 用于后续增量读取。`oldest_cursor`、`evicted_before` 与 `cursor_lost` 描述容量淘汰，不能把已淘汰记录当作未发生。调用不提供 cursor 时从当前保留记录开始，不保证完整覆盖页面打开以来的所有事件。

标准方法示例：

```json
{"action":"devtools","method":"DOM.getDocument","params":{"depth":1}}
{"action":"devtools","method":"Runtime.evaluate","params":{"expression":"({title:document.title,readyState:document.readyState})","returnByValue":true}}
{"action":"devtools","method":"Debugger.enable"}
{"action":"devtools","method":"Debugger.pause"}
{"action":"devtools","method":"events.list","params":{"method":"Debugger.paused","limit":20}}
{"action":"devtools","method":"Debugger.resume"}
```

标准 CDP 方法、参数、对象和事件语义沿用浏览器协议。节点、对象、脚本、frame 和执行上下文 ID 必须来自实际观察；不得猜测其他标签或会话的协议 ID。不是每个允许 domain 下的方法都保证当前 Chromium 状态支持，失败回执保留实际限制。

JavaScript 求值统一使用 `devtools` 的 `Runtime.evaluate`；旧 `eval` 动作已退役。`devtools` 必须独立调用，不能放进 batch。普通页面动作继续使用 ref、Playwright 和短 batch。

### 已提交动作命中断点

先启用 `Debugger.enable`，再设置断点或执行包含 `debugger` 的页面逻辑。普通 `click` 的事件处理函数以及`Runtime.evaluate` 都可能暂停。此时工具及时返回“已派发，结果仍 pending”的诊断；原动作仍在等待恢复，不能把这个回执理解为“未执行”并再次点击或求值。

1. 用 `targets.list` 和 `events.list` 筛选 `Debugger.paused`，记录事件的 `target_ref`，从真实 `params.callFrames` 取得 `callFrameId` 或局部作用域的 `objectId`。
2. 对该 `target` 用 `Debugger.evaluateOnCallFrame` 读取暂停点局部状态，或用 `Runtime.getProperties` 检查作用域对象。节点、对象、脚本和调用栈 ID 不能跨目标混用。
3. 对该 `target` 用 `Debugger.resume` 继续执行。原动作完成后再核验；无需也不得重放原动作。

```json
{"action":"devtools","method":"Debugger.evaluateOnCallFrame","params":{"callFrameId":"<暂停事件返回的 ID>","expression":"localValue","returnByValue":true}}
{"action":"devtools","method":"Runtime.getProperties","params":{"objectId":"<作用域返回的 ID>","ownProperties":true}}
{"action":"devtools","method":"Debugger.resume"}
```

CDP 执行预算计算非暂停时间；当前标签页所属目标中出现明确的 Debugger 暂停时，已在途的动作暂停计时，包括根页面等待 worker 回信的情况。另一个后台标签的暂停不影响当前动作；在暂停状态新发起的诊断仍有执行期限。`Runtime.evaluate` 的 timeout 上限为 10 秒，`0` 使用默认值。真正的执行超时仍按结果未知处理并要求取消。恢复后的结果若未通过原工具回执再次交付，应从页面/业务状态验证，不能为了取得返回值再运行一次表达式。

## 网络记录和正文

记录来自浏览器真实请求/响应事件，包含 Fetch/XHR 的成功、HTTP 失败和传输失败。HTTP 503 与连接失败分别保留，不能都改写成“登录失败”或成功。事件监听在新页面导航前建立，但连接建立前的历史与未附加子 target 的流量不在保证范围内。

已确认 dedicated worker 的启动采集存在竞态：Playwright 与 Pogo 使用不同 CDP 会话，Playwright 可能先调用 `Runtime.runIfWaitingForDebugger`，使 worker 在 Pogo 完成 `Network.enable` 前发出请求。Pogo 的启动暂停不是独占锁，不能保证捕获 worker 启动时的首条请求；目标已就绪或 Console 有记录也不能证明其启动网络记录完整。当前不通过自动刷新、重启 worker 或重发请求补齐历史。要关闭这个缺口，需要统一目标启动与采集的控制权，再做独立验收。

```json
{"action":"devtools","method":"network.read","params":{"request_id":"<network.list 返回的 request_id>"}}
```

读取使用缓存，不调用网站接口、不重放提交。`network.read` 根据 worker 签发的 `request_id` 在当前标签页所属目标中查找；指定 `target` 后还必须匹配该目标。不同目标即使拥有相同原始 CDP requestId，也不会混读。标准 `Network.getResponseBody` 只从所选目标的 `cdp_request_id` 取缓存，成功时保留 `result.body` 与 `result.base64Encoded` 结构。

生产浏览器工具现在为模型生成 `devtools-compact-v1` 视图。成功的 `network.list` 默认省略普通请求头和响应头字典，保留请求 ID、目标、导航／重定向关系、请求内容、状态、正文可用性、分页和丢失记录等字段；模型可按 `request_id` 调用 `network.read` 读取仍保留的详情。列表中的 `model_view` 会明确说明省略，不能把没有显示的请求头当成不存在。截断诊断头、未知结构、没有可读请求 ID 的条目以及失败回执保持原状。

仅当结构化 `target_coverage` 完整表达同一已知范围时，视图才省略重复的覆盖说明长文；动态覆盖状态、附加失败／初始化数量、排除类型和历史不可恢复等边界仍保留。CDP 的返回值、异常、对象句柄和数值不递归改写。原始有界、脱敏的 DevTools Data 另存于工具 `Details.devtools_data`，随会话记录保留且不进入模型上下文；格式异常的原文使用 `devtools_data_raw`。生产和通用模型评测直接共用这一输出层，worker、采集缓存和评测私有回执账本不受投影影响。请求记录被驱逐后，详情仍会如实返回不可用。

正文状态包括 `pending`、`available`、`truncated`、`unavailable`、`evicted`、`binary`、`streaming`。仅 `available` 表示这条保留的正文可用；其他状态不是空结果。重定向 hop、传输失败、浏览器已丢弃内容或长期流式响应可能没有完整正文。二进制不以正文字符串导出。

每个标签页最多附加 32 个目标（含根页面），全部目标共用 256 个请求、128 条 Console 记录、256 条事件；单个正文与通用 CDP 返回的导出上限为 64 KiB，正文缓存合计最多 1 MiB。超过目标上限或采集初始化失败时，会先释放新目标的启动暂停，确保网站脚本可以继续运行，并明确报告覆盖缺口。浏览器端还有各目标的独立网络缓冲限制，不能把 worker 导出上限当成 Chromium 整体内存限制。此版本没有完整 HAR 或无损长期 Trace 归档。

输出屏蔽敏感命名字段、敏感 URL 参数及已知字面凭据，也对结构化 JSON 正文和 Console 对象应用同一处理。此机制不能识别任意编码、改名字段或页面主动外发。截图、业务文本以及授权脚本自身的行为也不能仅凭“经过调试工具”视为安全。任务仍须限制在用户请求范围，不输出无关账号数据或凭据。

## 控制权与失败

- 所有 Agent 调试调用继续经过 BrowserGate、会话串行队列和 generation 校验。human 控制期间拒绝 Agent CDP 调用；标准协议不提供绕过人工接管的旁路。
- 调试动作 ID 仍在执行前认领，重复请求不重放；调试响应不会进入可复用结果缓存，避免旧操作 ID重新返回账号或页面内部数据。
- 标准协议可能修改 DOM、CSS、运行时或页面状态。`Runtime.evaluate`、`callFunctionOn`、断点和暂停不是只读沙箱；新调用能力不自动扩大用户授权。
- 调试暂停时，普通页面调用可能无法执行。worker 明确报告 `debugger_paused`；已派发的 click/Runtime.evaluate 及时返回 pending，允许读取调试事件、call frame/作用域或使用 `Debugger.resume` / `Debugger.disable`，不在调试回执后强行执行 DOM 快照，也不重放等待中的动作。
- 调试超时或执行结果未知会报告 `result_unknown_cancel_required` / `devtools_blocked`，封锁后续 Agent 操作直到显式取消清理。不能自动重试已经可能产生副作用的脚本。取消无法撤回网站已收到的业务请求。
- 已发送子目标命令遇到运输超时、连接错误或响应超限导致的 detach，也按结果未知处理；浏览器明确拒绝不支持的方法或参数则保留协议错误，不误判为必须取消。
- 人工接管和交还会停止调试会话、解除调试暂停并清理记录。取消涉及暂停或在途动作时，先临时隔离该 context 的网络，终止暂停栈，再销毁旧页面及调试连接；不能把取消当作普通恢复。后续动作按新代次重新观察，旧请求、节点、脚本和对象 ID 不可继续使用。

调用方仍不能直接使用 Browser/Target/Storage/IO/Tracing/Fetch/DOMStorage 等 domain；协议参数顶层的 `browserContextId`、`targetId`、`sessionId` 等路由字段也被拒绝。内部适配器仅通过已拥有根页面的 Target 自动附加事件建立子目标关系。`Runtime.callFunctionOn` 的业务数据可以包含同名字段，不将业务对象误判为目标路由。允许 domain 内仍拒绝 Cookie 管理、主机文件输入、下载、请求 interception、弹窗代答及关闭采集器等方法。

内部传输封装在 `cdp-targets.js`，使用公开 Playwright CDPSession 和当前 Chromium 支持的 non-flat CDP tunnel；没有使用 Playwright 私有连接对象。该 CDP tunnel 已被上游标记 deprecated，升级 Playwright/Chromium 时必须联合验证，并预留替换传输的接口。当前不将它当成永久兼容承诺。[CDP Target 协议](https://chromedevtools.github.io/devtools-protocol/tot/Target/#method-sendMessageToTarget)

## 单一路径与历史评测

`snapshot/find` 是轻量页面阅读与操作入口；HTML 结构和 `calculate` 是可选辅助。所有浏览器执行都经过同一原生 Chromium，DOM 操作、CDP 和人工接管共享上下文。旧 iframe/SSE 执行器已退役；服务缺失时明确不可用，不回退。

`api_read`、`auth_status`、`eval` 已退役。需要 JavaScript 时使用 `Runtime.evaluate`；先读真实网络证据，需要新增请求时明确执行，不能把新增 GET 当作原响应。没有复制 Cookie/token 后自行请求的辅助客户端。

旧书签和页签经过独立数据迁移，无法还原的条目保留修复入口。文件、引用、PDF 预览和独立业务面板不是 Agent 浏览器执行器。完整范围见 [单一路径迁移](browser-native-only.md)。

历史 `browser`、`auth`、`auth-auto` 评测协议与产物只解释当时版本；不能用新 CDP 偷偷适配旧动作或追认成绩。当前 `browser-use-v2` / `general-web` 使用当前生产 Skill 和能力，hash 改变必须形成新运行，不混合历史成绩。

## 验证

从 `pogo/browser-runtime` 执行：

```sh
node --test test/devtools.test.js
```

用例通过公开 HTTP worker 接口启动真实 Chromium 和本机合成网页，不读取用户账号、私有配置或外部网站。覆盖同页 DOM/Runtime 和 eval 兼容、真实 Fetch/XHR HTTP 与传输失败、日志/正文读取不重发、敏感输出脱敏、正文限额、Console/异常、Debugger 暂停与恢复、暂停期间人工接管并保留同页、真实 click 断点的 call-frame/局部状态检查与恰好一次恢复、native eval 断点超过 active-time 预算的暂停恢复、tab/session 隔离、危险方法拒绝、人工控制与取消后旧 ID 失效及重新采集，以及未知脚本结果封锁后的显式恢复。

2026-10-06 本机最终执行：`node --test --test-concurrency=2 test/*.test.js` 全部 143 项通过，包含本文件的 14 项真实 Chromium 场景以及 14 项 DevTools 策略/缓存测试。native eval 用例在 3 秒 active-time 预算下实际暂停 3.25 秒后检查局部变量、恢复并核验执行计数为一次；未知脚本超时仍有独立拒绝/恢复用例。脱敏回归同时验证 Cookie 不破坏 CDP 的数值 ID、类型、对象句柄及事件重读，业务内容仍按规则脱敏。

Go 的 tools、web、agent、browser-eval-agent 测试通过；另启动独立临时 worker，实际运行 `TestNativeBrowserRealRuntime`，验证模型工具、BrowserGate、Go 桥接、真实 Chromium CDP 和结构化结果返回完整链路。前端类型检查、24 项现有浏览器前端单测和生产构建通过，构建产物已更新。构建仍报告现有大 chunk 提示。

本记录没有调用付费模型、生产账号或部署服务；主服务与 worker 仍需独立发布验收。

上述第一阶段记录保持独立；本次多目标验收另见下方。测试仍属于执行层回归，不代表完整 DevTools frontend、全部 CDP domain、真实网站任务、生产部署或 Agent 完整答案质量验收。历史 browser/evidence/auth 成绩单独保留。

### 多目标回归

`test/devtools-targets.test.js` 通过公开 HTTP worker 验证真实 OOPIF、根页面和 iframe 内的 dedicated worker、目标归属、定向调用、聚合/过滤日志、缓存读取零重发、跨目标断点恢复、后台标签不干扰、销毁重建及控制权切换、超限目标继续运行，以及过大响应后的未知结果封锁。测试用独立 CDP 观察器确认实际存在 `type:iframe` 目标；仅测试浏览器显式启用 `--site-per-process`，生产启动参数没有因此改变。

`test/cdp-targets.test.js` 同时覆盖真实 Chromium 的递归附加与启动恢复，以及运输层的路由、容量、回执、错误、超时和销毁竞态。`TestNativeBrowserRealRuntime` 另验证目标发现、worker 引用透传和跨会话拒绝经过完整 Go 工具与 BrowserGate 链路。

2026-10-06 多目标阶段最终验收：运行时全套 **172/172 通过**，其中多目标真实 HTTP 场景 12 项、目标运输层 11 项、DevTools 策略/缓存 20 项；原有页面调试、登录、取消、人工接管、来源和数量回归继续通过。`go test ./tools ./agent ./web ./cmd/browser-eval-agent` 通过；独立临时 worker 下的 Go `TestNativeBrowserRealRuntime` 通过，实际定向读取 worker 并拒绝另一会话复用其引用。此轮没有改生产浏览器 flags、前端源码或部署服务，也没有执行付费模型或真实账号验收。

### 通用任务验收 v2

独立套件 [browser-use-v2](../browser-runtime/eval/browser-use-v2/README.md) 覆盖失败请求与 Console 诊断、真实 OOPIF 数据核验、已附加 worker 的采样任务、断点局部状态读取和恢复、复用登录态后恰好一次提交。生产 browser-use Skill 与生产工具 schema 由新的 `browser-use-v2` 模型 profile 加载；旧 profile、判题器及历史结果保持独立。

判题器只接受记录代理在私有回调中取得的真实 worker 回执，并与夹具服务端响应和保存记录比对。模型输出的工具事件、伪造 JSON 或只有正确答案均不能代替证据。恢复用例还绑定同一目标的暂停事件、真实局部变量读取、恢复动作与随后发生的保存请求。最终答案按目标明确要求的有限事实检查，仍需人工审阅自然语言含义。

采样夹具先建立 worker，由模型或参考脚本实际点击业务按钮启动一次采样；此项验证附加后的网络取证，**不验证上述 worker 启动首请求缺口已经解决**。登录准备由控制器在本机虚构账号上模拟人工接管、登录与交还，准备动作不计为模型行为。所有任务仍属于受控合成网站，不代表真实账号、互联网网站、付费模型或产品部署验收。

2026-10-06 v2 最终验证：新套件 **42/42** 测试通过，共享评测与旧 general-web 回归 **171/171** 通过；`go test ./cmd/browser-eval-agent` 通过，新 CLI 已编译并验证 `browser-use-v2` 入口。两组种子 `20261006`、`20261007` 各包含上述五项任务：参考脚本 **10/10 通过**，只提供正确答案和伪造工具事件两种负对照均 **0/10 误通过**；三轮都无基础设施错误、无跳过任务，并确认每个上下文已清理。保存的源码指纹与当前实现一致。

本机报告位于 `pogo/browser-runtime/eval/runs/browser-use-v2-{reference,noop,fabricated}-20261006-final/report.html`，对应目录包含脱敏调用记录和 JSON 汇总；生成目录为 Git 忽略的本地证据，不自动随源码交付。此轮没有付费模型调用或部署。下一阶段使用明确的模型及次数、token、时间和浏览器操作预算运行新的模型 profile，逐项人工复核答案与证据后，再进入候选环境及真实网站验收。

### 真实模型基线与取消修复（2026-10-06）

随后实际调用项目已有的 GLM-5.3 配置，使用文本观察、同一模型 profile、生产 Skill、工具 schema、源码和种子 `20261006`，完整运行五项任务两次。两轮均为 50 轮、180 秒、80 次浏览器操作上限；第二轮仅将每项累计 token 预算从 60,000 调整为 120,000。预算按模型响应返回后的实际用量核算，因此最后一轮可能超限；超限结果仍计失败。两轮源与二进制快照另行保存，原报告未改写。

| 运行 | 原自动判分 | 独立核对任务目标与预算 | 预算失败项 |
| --- | --- | --- | --- |
| v1，60,000 tokens/任务 | 1/5 | 2/5 | worker、断点恢复、工单 |
| v2，120,000 tokens/任务 | 2/5 | 4/5 | 断点恢复 |

独立复核由 Codex 对实际回执、服务端状态、最终答案和预算逐项检查，不是另一次模型执行，也不代表独立人类签字。iframe 的真实 DOM 证据、工单的 POST 响应加实际页面记录满足任务目标，但原自动规则限定 Network 正文路径，形成误拒。所有原自动分数保留；目标所需事实正确，也不代表附加措辞没有问题，例如把同一采样的内存、响应和日志称为三个独立来源。

第二轮断点任务实际读取了局部记录、恢复并保存一次，但累计 134,067 tokens 超过 120,000，最终答案仍是恢复前的进度句，不能计有效交付。下一步应缩短调试观察路径、减少猜测变量及重复取证，并完善 DOM 证据判据；不能靠改写历史成绩宣称问题已解决。

首轮另暴露了真实执行缺陷：模型没有发送恢复命令，预算终止后的取消清理却恢复 worker 并产生保存。独立公开 HTTP 复现确认，空闲但暂停、在途且暂停、直接 DELETE 三种情况都会产生此副作用。仅先关闭页面仍不足以阻止暂停代码与已排队的定时 HTTP 请求。

修复后的破坏性取消／销毁先对当前 BrowserContext 临时断网，对暂停目标使用 `Debugger.resume({terminateOnResume:true})` 原子终止当前暂停栈，再关闭旧页面或 context，最后清理调试连接；成功取消只在旧页面全部销毁后恢复网络，Cookie 保留。普通空闲取消、人工拥有的弹窗和主动人工接管保留原有语义。网络隔离与终止失败会报错，不退回普通 resume；无法撤销取消前已发出的请求。这使用的是 [CDP 定义的终止执行语义](https://chromedevtools.github.io/devtools-protocol/tot/Debugger/#method-resume)，不是普通恢复后再抢时间关闭。

**两轮真实模型基线均发生在上述取消修复之前**。修复后的执行层回归与控制组另行记录，不把旧模型成绩当成修复后部署验收。worker 启动首请求漏采的既有限制仍保留。原始模型报告位于 `browser-runtime/eval/runs/browser-use-v2-glm-5.3-20261006-{v1,v2}/report.html`，独立复核及修复证据位于根目录 `.localagent/checks/browser-use-v2-model-20261006/`。

修复后最终验证：运行时 **184/184**，新旧评测及共享组件 **218/218**；Go 的 tools、agent、web、browser-eval-agent 测试及独立真实 Chromium 的 `TestNativeBrowserRealRuntime` 通过。新增九个真实取消场景覆盖暂停 worker／OOPIF、排队 timer、直接 DELETE、取消后联网与 Cookie 保留；原样复现的三种取消场景均从一次保存降为零次保存。另修复进程退出时 context 已销毁却误报清理失败的边界，真正终止失败仍不确认成功。

最终源码下换用种子 `20261007`，参考控制 **5/5**，无动作和伪造事件控制均 **0/5 误通过**，所有 context 清理已确认。完整独立总览位于 `browser-runtime/eval/runs/browser-use-v2-glm-5.3-20261006-review/overview.html`，含原报告链接、逐项答案与证据、源码指纹和修复后验证。没有把修复前两轮模型成绩合并，没有新增修复后付费模型运行，也未部署服务。

### DOM 证据与调试策略改进（2026-10-06）

本阶段修改生产内置 `browser-use` Skill 和 `browser-use-v2` 验收规则，运行时保持上一阶段取消修复后的版本。Skill 在已知暂停时直接发现目标、读取实际调用帧和局部作用域，按真实对象句柄展开所需字段，再恢复原动作；不猜变量、不为补充来源而强制读取脚本、不重复派发原操作。最终答复要求逐项对应实际回执，区分同一次数据的多种观察与独立来源。没有写入夹具按钮、变量、接口地址或答案。

自动判题新增两种有限 DOM 证明：真实 iframe 表格中的完整同一行，以及提交后的完整保存记录。证明必须来自私有记录代理取得的原生结构化观察，核对 frame、代次、版本、source ID、内容哈希及服务端真实响应。工单路径还需同会话、同标签与代次的实际 POST 201 元数据，不要求再读取 POST/GET 正文。模型自述、Runtime 返回的仿造 JSON、错行拼接、错误 frame、后续错误观察及旧快照重放不能代替证明；这不表示任意 DOM 或 JavaScript 返回值均可自动判定。

调试夹具另将原始暂停目标、事件和局部作用域句柄私下交给判题器，核对模型实际读取的是原始暂停。该 anchor 不进入任务提示、网页、准备摘要或模型可读的服务端状态。原帧 URL 为空时不再强制模型额外读取脚本来满足判题器；任务所需局部字段、恢复及保存证明仍须完整。

验证结果：新旧评测及共享组件 **227/227** 通过，Go `skills` 与 `cmd/browser-eval-agent` 测试通过，两次模型执行均重新编译内嵌 Skill 的 CLI。新种子 `20261008` 下参考控制 **5/5**，无动作与伪造事件控制分别 **0/5 误通过**；额外真实 Chromium DOM 正负控制 **10/10** 符合预期（2 个正例、8 个负例）。控制组与模型源码、工具和 Skill 指纹分别保存，历史报告不改写。

随后使用 GLM-5.3 完整运行两个新种子；每项预算均为 120,000 累计 tokens、50 轮、180 秒和 80 个浏览器步骤：

| 运行 | 种子 | 原自动判分 | Codex 独立目标与预算复核 | 答案附加问题 |
| --- | --- | --- | --- | --- |
| v3 | 20261008 | 5/5 | 5/5 | worker 把只含采样 ID 的 Console 归为读数证明；断点答案补写了未发生的恢复后 targets.list |
| v4 | 20261009 | 4/5 | 4/5 | worker 仍将同次采样的派生观察称作独立证据；断点任务超预算 |

v3 断点任务以 8 次成功调用、101,891 tokens 完成；v4 以 9 次成功调用完成实际恢复、一次保存及完整答案，但累计已报告用量为 **121,646 tokens**，因此仍判失败，不能忽略超限的最后一轮。两轮的上下文均已清理。v4 相比 v3 只进一步收紧最终答复的来源归属规则，但种子、模型实际路径也不同，不能合并为同版本成功率或归因于单个提示修改；附加措辞问题也说明自动目标判分不等于整段答案无误。

本阶段材料位于根目录 `.localagent/checks/browser-use-v2-dom-debug-20261006/`；独立总览位于 `browser-runtime/eval/runs/browser-use-v2-dom-debug-review-20261006/overview.html`，原始两轮报告仍分别保留。下一步先降低调试任务每轮提示与观察的重复开销，并让来源表述准确，再进入候选应用链路验收。

候选环境只进行了只读检查：旧 app/worker/mock 健康，但镜像仍是旧源码；已有认证脚本指定了 API 路径，不能证明新的通用 CDP 路径。还需将两源夹具适配容器网络并接入真实 Studio 会话。此阶段没有部署或退役旧浏览器通道，仍未关闭 worker 启动首请求漏采缺口，也不代表真实网站或完整 DevTools frontend 验收。

### 模型输出压缩验证（2026-10-06）

本阶段在生产 `tools.Browser` 实现上述 `devtools-compact-v1`，通用模型评测直接使用同一代码。Skill、工具 schema、worker、夹具和判题器均保持 v4 版本，未调整预算。旧的 browser/auth profile 不具备 DevTools 动作，因此其原输出范围没有扩展。

将 v4 的 20 条实际 DevTools 回执离线送入当前生产格式化代码，模型文本从 **62,381 降至 52,079 UTF-8 字节（减少 16.5%）**；断点任务部分减少 **21.8%**。每条原始 Data 都逐字节保存在 Details 中，没有调用浏览器或模型。这是同一回执的字节测量，不能直接当作 token 节省率。完整诊断留存会增加会话记录体积，其范围仍受 worker 原有采集与脱敏边界限制。

Go 的 tools、llm、agent、web、skills、browser-eval-agent 六个包测试通过。额外启动临时真实 Chromium worker，`TestNativeBrowserRealRuntime` 通过生产工具、BrowserGate 和 Go 桥接验证压缩列表、原始诊断留存及 `network.read` 的完整响应头，并确认目标页面只有一次 GET，没有为获取详情重新请求网站。异常、未知结构、大整数、对象／请求／目标 ID、分页和截断状态保留均有行为回归。

冻结源码后，GLM-5.3 的 v5 连续运行种子 `20261009` 与 `20261010`，各包含完整五项任务，每项仍为 120,000 tokens、50 轮、180 秒和 80 个浏览器步骤。结果如下：

| 验收维度 | v5 结果 |
| --- | --- |
| 原自动判分 | 10/10 |
| Codex 独立核对实际任务、答案必要事实与预算 | 10/10 |
| 预算及完整答案的来源归属均符合要求 | 9/10 |
| 上下文清理、用量完整记录 | 10/10 |

同种子 `20261009` 的断点任务由 v4 的 9 次调用、121,646 tokens，变为 v5 的 8 次调用、100,461 tokens；新种子断点任务为 8 次调用、100,001 tokens。两项均真实恢复并保存一次。模型调用路径也发生变化，不能把全部用量差异归因于投影；这只是两个种子的受控任务结果。总已报告用量为 627,764 tokens（输入 616,947、输出 10,817），缓存与 reasoning 子集没有重复相加。

剩余问题是第二个种子的 worker 最终答案：数值正确、单次采样、预算有效，但仍把同一次 fetch 的响应、内存与 Console 称作独立通道，并将仅含 sample ID 的日志归为数值证明。独立报告保留这两处来源错误，没有用自动通过代替完整答案准确性。

原始 v5 报告位于 `browser-runtime/eval/runs/browser-use-v2-glm-5.3-20261006-v5/`，本阶段总览位于 `browser-runtime/eval/runs/browser-use-v2-compact-review-20261006/overview.html`；源码快照、离线测量、Go 日志及独立复核位于根目录 `.localagent/checks/browser-use-v2-compact-20261006/`。

下一步进入独立候选 Studio 链路适配：fixture 与私有 recording proxy 必须使用可比较的同进程单调时钟，提供容器可达的两个站点，并绑定真实 Studio／worker 会话与接管控制；SSE 只提供模型输出和用量，不代替可信 worker 回执。候选 Studio 目前不会自动继承 CLI 的 token 预算，浏览器隔离参数也有差异，须单独记录并验证。本阶段只保存接入计划，没有改候选服务或退役兼容通道。

### Studio 候选链路与默认跨站隔离

后续接线已单独实现于 [Studio 候选验收](../browser-runtime/lab/browser-use-v2-README.md)。它使用独立 Docker 项目、用户和卷，真实经过 Studio 会话、生产浏览器工具、私有 ACK 代理和 hosted worker；两源 fixture 与 ACK 记录共享单调时钟。SSE 运行 ID、唯一工具调用、起止区间及真实 worker 会话需要一一对应。准备与人工接管不计作模型行为，清理后重新核对最终站点状态，迟到的重复请求会撤销先前判分。

首次无模型接线发现 Linux hosted Chromium 默认启动虽能加载跨站 iframe，却没有创建可附加的 iframe target；原 CLI 与多目标回归已显式开启跨站进程隔离，不能据此认定生产默认启动也具备相同能力。同一容器的独立对照确认：默认启动等待后仍只有根 target，加入 `--site-per-process` 后出现 iframe target。

生产 worker 自行启动 Chromium 时现默认使用 `--site-per-process`，保证跨站渲染进程隔离与调试目标的基础条件。该设置不限定测试域名、不授予跨会话权限，也不保证每个同站 iframe 都具有独立 target；复杂站点可能使用更多渲染进程，现有目标数量和记录上限及覆盖缺口提示继续适用。外部注入的 browser 仍由调用方负责启动配置。`test/native-launch.test.js` 直接覆盖不注入 browser 的公开 worker 启动路径，避免仅测试预先加过 flags 的浏览器。

候选验收仍保留受控合成网站、固定预算与当前生产保护工具边界，独立报告与历史 CLI 成绩分开记录。模型完成、答案来源准确性、清理和真实部署范围是不同验收项；接线成功不代表所有真实网站通过，也不自动退役旧浏览器通道。
