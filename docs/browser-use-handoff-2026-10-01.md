# Browser-use 改造交接文档

2026-10-02 后续：等待作用域、弹窗归属、回执事实描述与鼠标事件归因已提交为 `55af543`，Compose 参数透传为 `d96c337`，均尚未部署；验证与重复评测见[运行时补充](native-browser-runtime.md#等待范围与观察准确性2026-10-02源码改动)和[评测记录](browser-evaluation.md)。下表镜像仍指当前线上版本。更晚的 STU/WF 正确性修复、CI 与发布工具见根 [HANDOFF.md 第 12 节](../../HANDOFF.md#12-正确性与发布准备2026-10-02-后续)。

更新日期：2026-10-02。面向接手优化、修复和部署的 Agent。本文覆盖原生浏览器、沉浸模式、元素选取、能力声明与真实模型评测。

后续进展（同日）：浏览器改造已提交并推送到 GitLab 分支 `feat/browser-use-native`（未合入 `main`）。P1 的“异步动作后的观察”和“参数反馈与执行端一致性”已完成、通过隔离评测，并于 14:36 部署到本机 Docker，主服务与 worker 均已更新并通过验收。见下方表格、§9、[评测对照](browser-evaluation.md#异步动作观察改造后的对照2026-10-01)和[部署记录](native-browser-runtime.md#异步观察部署与验收2026-10-01)。

后续进展（10-02）：线上镜像所含的其余源码已按主题提交（`450e0ac`…`ebf4d15`），测试基线在 `2f58f40` 恢复全绿（含 `go test -race ./...`），分支已推送，尚未合入 `main`。工作流修复 `a04c4d5` 改了主服务代码，尚未部署。本机重启后 `/tmp` 下的部署与验收脚本已被清理。见 §9 与[项目现状](project-status.md#本次验证记录)。

## 1. 接手时先看这里

**当前交付状态：功能已实现，并部署到本机 Docker。** 目标网站由独立容器中的 Playwright/Chromium 直接访问，Pogo 展示画面并提供交互；网页内容不再经过网站反向代理。原生执行、人工接管、沉浸模式、小手选取和能力声明改造均已落地。已具备持续评测基础，但尚无证据宣称达到通用 browser-use 的 SOTA。

| 项目 | 交接时状态 |
| --- | --- |
| 本机源码 | `/Users/wangy/Documents/DiscoverAgent`；下文代码路径相对仓库根 |
| Git | GitLab 分支 `feat/browser-use-native`（基于 `main` `d937029`，未合入）：`e9fa5c9` 浏览器改造（混合文件按 hunk 拆分），`93a721b` w_deploy 浏览器叠加与双镜像平台预检，`6af3d07` 动作后有界等待与定向参数提示；`450e0ac`…`ebf4d15` 线上镜像所含的其余改动（Dockerfile 镜像源、声明式工具工作区、Skill 平台、Git 退役与子 Agent 串行、引用阅读、首页路由、gitignore、dist 重建）；`443c7f1`…`2f58f40` 测试基线修复；另有文档提交。工作区只剩本机部署状态 `w_deploy/.env`、`w_deploy/compose.yml` 未提交 |
| 访问入口 | <http://127.0.0.1:19777>；实际绑定 `0.0.0.0:19777 → 7777/tcp` |
| 主服务 | 容器 `pogo-base`，镜像 `pogo:browser-settle-20261001-arm64`，运行中 |
| 浏览器 worker | 容器 `pogo-pogo-browser-1`，镜像 `pogo-browser:settle-20261001-arm64`，健康；仅 Compose 网络开放 7788 |
| 本机已验证平台 | Linux/arm64 Docker；隔离模型评测运行于 macOS/arm64，不能混用性能结论 |
| 最新部署备份 | 仓库根 `backups/browser-settle-20261001-143544/`（含 `deployment.json`、`acceptance.json`） |
| 回滚镜像 | `pogo:rollback-browser-settle-20261001-143544`、`pogo-browser:rollback-browser-settle-20261001-143544`；即更新前的 `pogo:browser-capabilities-20261001-arm64` 与 `pogo-browser:picker-20261001-arm64` |
| 下一步优先级 | 经 MR 合入 `main`，在仓库根补 `.gitlab-ci.yml` 让 CI 真正运行；把发布与验收脚本写入 `w_deploy`，再用它部署工作流修复 `a04c4d5`（只更新主服务）；扩充浏览器评测（现有 20 项已连续四轮 20/20） |

接手前运行 `git status --short` 与 `git log --oneline main..feat/browser-use-native`。分支除浏览器改造外还包含 Git/worktree 退役、Skill 平台等改动，不能全部归为浏览器改造；不要批量回退、清理未跟踪文件或直接 `git add .`。线上主服务镜像对应 `ebf4d15` 的源码（文档除外），worker 镜像对应 `6af3d07` 的 `browser-runtime`，均可凭 Git 复现；`a04c4d5` 是之后唯一改动运行代码的提交，尚未部署。工作区未提交的 `w_deploy/.env`（`POGO_PLATFORM=linux/arm64`）与 `compose.yml` 镜像标签是本机部署状态，不要提交到共享分支。跨机器交接需保存对应源码和锁文件，单独迁移受控配置；评测报告和备份默认不进入 Git。

建议阅读顺序：本文 → [运行时与历次验收](native-browser-runtime.md) → [评测协议与历次结果](browser-evaluation.md) → 所改层的源码和测试。旧文档中的“尚未接回小手”“尚未部署”是特定阶段的历史状态，以后续部署段和本文当前状态为准。

## 2. 目标、架构与已经实现的能力

改造解决三个问题：网站反代对 Cookie/CSP/重定向等语义的破坏；执行过程缺少可见反馈和人工协作；模型收到的工具能力与实际执行端不一致。最终采用托管浏览器，不依赖把目标网站嵌入跨域 iframe。

```text
模型 / Agent
    ↓ browser 工具：按后端、图像链路生成 schema，预校验参数
BrowserGate：任务授权、取消和控制权约束
    ↓ nativeBrowser 会话桥
鉴权私有 HTTP → 独立 worker → Playwright / Chromium → 原始网站 URL
    ↑                          每个 Pogo 会话独立 BrowserContext
Go Web API：会话归属鉴权
    ↑
Vue：JPEG + version / 动作提示 / 人工输入 / 只读元素选取 / 悬浮对话
```

“不使用网站反代”指不重写目标网站 HTML、JS、CSS、Cookie 或 CSP。Go 仍转发浏览器控制请求和画面，这属于控制通道。它控制的是容器浏览器，不是用户本机 Chrome，不会自动继承本机登录态。

| 层面 | 已交付行为 |
| --- | --- |
| 原生执行 | snapshot/find、点击、type/select、按键、悬浮、滚动、条件等待、提取、多标签；具备图像链路时可截图和带版本坐标点击；native 不开放任意 eval |
| 执行可靠性 | 操作 ID 去重、文档/画面版本检查、导航后停止 batch、取消确认、独立会话上下文；没有网页观察窗口也可执行已授权任务 |
| 人工协作 | 私有租约接管同一页面，支持中文输入、键盘/滚轮、地址导航、标签和页面弹窗；交还后用户明确让 Agent 继续 |
| 画面反馈 | 当前动作、目标矩形、真实指针和输入焦点；按画面版本/页签匹配，无法确认时省略空间提示 |
| 沉浸模式 | 网页或文件预览铺满应用工作区，原对话悬浮；拖动、调整尺寸、收起、窄屏贴底；系统全屏独立于应用内沉浸 |
| 紧凑工具栏 | 单行地址与主要控制，次要操作进更多菜单；仅多个远程标签时显示标签行；刷新按钮获取画面，不重载网站 |
| 小手选取 | 只读悬停高亮、点击加入输入框，保留草稿/附件，不自动发送、不触发网页点击；普通跨域 iframe 与开放 Shadow DOM 可选取 |
| 评测 | 20 项受控任务、独立 worker/上下文、真实模型适配器、业务判分、trace/截图、离线报告和前后对照 |

未配置 `POGO_BROWSER_URL` 的部署继续走旧面板；配置了 native 而服务不可用时明确失败，不静默退回网站反代。旧路径是兼容分支，接手修改时需要保留其能力边界。

## 3. 代码地图

| 要改什么 | 主要入口及职责 |
| --- | --- |
| 工具参数/执行 | `pogo/tools/browser.go`：`Browser.Execute`、`browserArgs`；`browser_capabilities.go`：后端/图像枚举与整批能力检查；`browser_schema.go`：实际工具声明 |
| 工具注册与子 Agent | `pogo/tools/tool.go`；`pogo/tools/browserfd.go`：私有 FD 和 `POGO_BROWSER_BACKEND`；`pogo/main.go`：CLI 图像模式 |
| 模型切换 | `pogo/agent/browser_capabilities.go`：`SetBrowserModelVision`；`pogo/web/vision.go`、`manager.go`：初始化；`pogo/web/runner.go`：`SetModel` 刷新 |
| 授权与后端选择 | `pogo/web/browser.go`：`BrowserGate`、`subPane`；`pogo/web/manager.go`：选择 native executor，管理会话生命周期 |
| Go 原生桥 | `pogo/web/native_browser.go`：执行、状态、取消、清理；`native_browser_api.go`：Web API；`native_browser_activity.go`、`native_browser_pick.go`：反馈和只读选取 |
| Worker | `pogo/browser-runtime/src/server.js`：鉴权路由/生命周期；`session.js`：动作、引用、版本、租约、取消；`activity.js`：真实动作反馈；`picker.js`：受限只读命中测试 |
| 浏览器 UI | `pogo/web/ui/src/components/NativeBrowserPane.vue`；`src/agent/nativeBrowser.ts`：类型、坐标/overlay/pick 转换；`BrowserPane.vue`：后端接入 |
| 沉浸布局 | `pogo/web/ui/src/views/AgentView.vue`、`components/DockArea.vue`、`agent/useImmersiveBrowser.ts`、`agent/immersiveLayout.ts`、`styles.scss` |
| 引用加入聊天 | `pogo/web/ui/src/agent/elementPicker.ts`：共用 `ElementPick`；`views/AgentView.vue`：`onElementPick`/输入框 chips；`agent/useAgentStream.ts`、`platformTypes.ts`：流与类型；API 位于 `src/api/client.ts` |
| Native 面板自动展示 | `pogo/web/ui/src/agent/browserExec.ts`：`createBrowserReveal` 只展示面板，不能混回 legacy 前端 executor，避免同一动作执行两次 |
| Skill 使用约定 | `pogo/skills/builtin/browser-use/SKILL.md`，是产品内置 Skill，不应替代服务端校验 |
| 模型评测适配器 | `pogo/cmd/browser-eval-agent/`：复用现有 Agent/LLM/Browser，只注册 browser；图像在内存解析，结束事件记录实际 schema 指纹 |
| 评测调度/判分 | `pogo/browser-runtime/eval/runner.js`、`adapter.js`、`transport.js`、`fixtures/index.js`；`report.js`、`compare.js` 生成报告 |
| 构建与部署 | `pogo/Dockerfile`、`pogo/browser-runtime/Dockerfile`/`seccomp.json`、`w_deploy/compose.yml`/`compose.browser.yml`/`deploy.sh`；根 `.dockerignore`/`.gitignore` |

Go 对前端公开的路径位于 `/api/sessions/{sid}/browser`：状态为 GET/POST，画面为 `GET /frame`，接管为 `POST /control`，输入为 `POST /input`，选取为 `POST /pick`。worker 的 `/sessions/:id/*` 是私有 API；完整契约见 [worker README](../browser-runtime/README.md)。不要让前端直接拿 worker bearer token。

## 4. 最近一轮：能力声明改造的具体契约

### 后端与图像模式

`BrowserBackendProvider` 是可选接口，`BrowserBackendOf` 识别 `native` / `legacy`，未识别的旧连接保留原有兼容行为。`BrowserImageMode` 包含 `Direct`、`Helper`、`None`、`Unknown`，描述模型实际获取图像的路径。

| 路径 | 工具声明与执行限制 |
| --- | --- |
| Native + Direct | 有真实 ImageResolver，支持截图/坐标；不支持 eval |
| Native + Helper | 文本模型可通过已注册的真实 `ImageAnalyze` / `analyze_image` 看图，保留截图/坐标；不支持 eval |
| Native + None | 只有 DOM 交互与标签操作；隐藏并拒绝截图/坐标，不影响用户查看画面或评测归档截图 |
| Legacy | 不声明 native 截图、坐标、标签动作；保留旧面板行为 |
| 未标识后端 / Unknown | 兼容旧调用方的过渡状态；新接入必须明确能力，不应靠 Unknown 绕过限制 |

主会话初始化和模型切换均更新实际工具声明及 token 开销估计。`SetBrowserModelVision` 不允许在运行中变更；仅配置模型名称而没有注册可用图像工具，不算 Helper。`BrowserGate → subPane → FD` 传递后端信息；当前 CLI/FD 链路没有图像解析器，明确使用 None。**能力元数据不等于执行授权**，不能绕过 BrowserGate 或人工租约。

### 参数与 batch

- 每个动作必须明确给 `action`；batch 子项有字段类型和枚举，最多 12 步，禁止嵌套。
- 对已知后端，能力和参数在任何动作执行前检查整批，避免前半段已写入、后半段才发现参数不支持。
- `type` 的 `text` 用指针区分缺失与空字符串；`text:""` 表示明确清空，序列化不能省掉。
- `select` 选项放在 `text`；不自动把 `fill`、`value` 等错误别名改写成可执行动作。
- 坐标 click 要同时提供 `x/y/version`，不能再提供 `target`。坐标为原始 viewport CSS 像素，前端要扣除留白并换算缩放。
- `wait` 需要 `target`、`text`、`gone_text`、`url` 等有效条件；`seconds` 是等待上限，不能单独当 sleep 使用。
- 导航、切换页签、错误、取消和控制权变化会停止 batch 后续步骤。已经发生的动作不回滚；重新观察后再计划。

实际文本模型 browser 声明在对照评测中为 3,892 字节；未知后端兼容模式的整套 11 工具声明为 14,675 字节，仍受 15,000 字节测试上限约束。优化 schema 时不要靠抬高该上限掩盖膨胀。

当前 HTTP `/api/tools` 是工具目录，并不提供会话实际发送给模型的 schema 内省。核验应使用 Go/wire 测试和评测结束事件的 `tool_schema_sha256`、`schema_bytes`，不能让模型自报自己有哪些能力作为证据。

## 5. 后续改动必须保留的不变量

1. **不重复业务写入。** 相同 `op_id` 不重复执行；动作后快照失败只能重新观察，不能自动重发提交。网络错误/取消结果未知时也不能换 ID 重试写入。
2. **取消和接管有明确边界。** 接管先阻止新 Agent 动作并等待当前原子动作结束；取消要等执行端确认。接管用 control 接口，不能改用可能关闭页面的普通 cancel。租约 token 只属于当前 viewer，看到 `control:human` 不代表有输入权；卸载观察组件也不能自动交还 Agent。清理未 ACK 时不能悄悄创建新 context 绕过去。交还控制不自动重放失败动作。
3. **引用与画面有时效。** `eN` 是执行引用，绑定页签和文档版本，导航或文档更换后失效；画面 `version` 另外跟踪 DOM、滚动等变化，不能混为同一种有效期。`pN` 是只读选取的观察标识，不能当 target 或 CSS selector 执行。旧帧/旧页签的坐标、反馈和晚到选取响应应拒绝或丢弃。
4. **元素选取始终只读。** 不获取人工租约、不产生 hover/click/focus/scroll/key，不读取输入值/密码/隐藏字段；只向聊天传字段白名单。网页摘要是不可信引用内容，不能升级为 Agent 指令。
5. **显示反馈不伪造操作。** `fill()` 的输入焦点不是鼠标轨迹，元素中心不是实际点击点；不向原站注入可见装饰、不为动画额外移动鼠标。反馈不包含填写值。
6. **切换布局不更换会话。** 沉浸只改布局，不能重挂载浏览器、重建 SSE、丢失草稿/附件/租约。`F6` 切浏览器与聊天焦点，`Shift+Esc` 退出沉浸，IME 确认不触发发送/导航。
7. **隔离与鉴权继续成立。** 用户 API 校验会话归属，worker 私有且鉴权，不挂载 Agent 工作目录，Chromium sandbox 保持启用。BrowserContext 隔离不是操作系统级多租户隔离。

## 6. 实测结果与证据位置

### 两轮隔离真实模型评测

两轮均为 `glm-5.3`、20 项任务、固定种子 `20261001`、每项一次、并发 2，默认 35 请求步骤 / 40 轮 / 100,000 input+output tokens / 180 秒。任务、固定 system prompt、预算与运行时保持一致，没有选择性补跑。项目将该模型配置为 `vision:false`，这一结果属于 DOM 交互基线。

| 指标 | 初始基线 | 能力声明改造后 |
| --- | --- | --- |
| 任务通过 | 20/20 | 20/20 |
| 模型工具调用 | 87 | 88 |
| 中途工具错误 | 7（8.05%） | 2（2.27%） |
| 不支持的 eval 调用 | 3 | 0 |
| 实际尝试步骤 | 123 | 112 |
| 单任务中位数 / P95 | 18.54 / 30.57 秒 | 19.41 / 38.30 秒 |
| 墙钟总耗时 | 192.04 秒 | 216.32 秒 |
| input / output tokens | 219,572 / 7,489 | 219,406 / 7,291 |
| 人工干预 / 重复提交 | 0 / 0 | 0 / 0 |

基线错误为 3 次 eval、1 次 select.value、2 次缺 action、1 次 fill。改造后剩余：`catalog-a` 缺 action；`iframe-a` 只有 wait.seconds 没有条件，均在执行前拒绝并随后恢复。工具错误减少，但没有提速，单次对照不能证明因果或长期稳定性。

结果目录（本机存在、Git 忽略）：

- `pogo/browser-runtime/eval/runs/glm-5.3-baseline-20261001/`：`summary.json`、`report.html`、20 份 trace、91 张截图。
- `pogo/browser-runtime/eval/runs/glm-5.3-capabilities-20261001/`：`summary.json`、`report.html`、20 份 trace、95 张截图；`comparison.html` / `comparison.json` 是离线对比。
- summary 保存工作区源码和实际二进制指纹；结束事件保存模型/工具声明/提示词信息。对比器在模型评测后补充了协议核对，属于后处理，未重新调用模型或重写历史成绩。

本机可直接打开[能力声明对照报告](../browser-runtime/eval/runs/glm-5.3-capabilities-20261001/comparison.html)。新克隆不带这些被忽略的产物，需要重新生成或单独迁移。

异步观察改造后又按同一协议跑了两次（`glm-5.3-settle-20261001`、`glm-5.3-settle-v2-20261001`，均 20/20）。修正版相对能力声明轮：工具调用 88 → 75、中途错误 2 → 1、实际步骤 112 → 104、input tokens 219,406 → 191,000；中位数 / P95 为 18.84 / 37.33 秒，没有可确认的提速。首版暴露并修正了“页面不读取响应体的请求被误判为未完成”的问题。详见[评测文档](browser-evaluation.md#异步动作观察改造后的对照2026-10-01)和[修正版对照报告](../browser-runtime/eval/runs/glm-5.3-settle-v2-20261001/comparison-vs-capabilities.html)。

评测 profile 为 `pogo-agent-browser-only`，不加载生产 Skill、历史对话、其他工具或视觉辅助模型。它不等于完整生产会话评分，也不是公开网站基准。费用没有配置单价时为 null；不能据此写“免费”。

### 最新 Docker 端到端验收

最新部署通过真正的 Web/API 主会话调用 GLM-5.3，在临时受控网站完成：导航、batch 替换中文、显式空文本清空、仅一次提交，并返回页面独有回执。共 2 次 browser 调用，0 次工具错误；业务服务器记录与字段一致，无旧 `browser_request` 路径，JPEG/version 正常。

独立人工接管验收通过中文输入、一次提交、交还后保留结果页。临时会话已删除，验收前后原有 2 个会话仍在，原有 JSONL 文件哈希一致；五个挂载、环境指纹和端口保持一致。这是部署 smoke，不是再次跑完 20 任务评测。

证据为最新备份目录中的 `acceptance.json`、`deployment.json`。备份含私有配置，不应整体复制到交接消息或公开报告。之前 worker/Go race/真实 Chromium 集成、前端类型检查和构建、253 项界面交互验收见[历次记录](native-browser-runtime.md)；这些是各阶段记录，不代表今天重新执行了所有测试。

## 7. 开发与验证操作

以下测试使用独立 fixture，不应指向正在使用的生产 worker。Go 版本见 `pogo/go.mod`（交接时为 1.26.5）；worker 要求 Node ≥22，Playwright 锁定 1.63.0。安装依赖按已有锁文件执行，不顺手升级依赖。

### 基础回归

```sh
# 从仓库根开始：执行层 + 评测判分器，不调用付费模型
cd pogo/browser-runtime
npm ci
npx playwright install chromium
npm test
npm run eval:test
npm run eval:validate
npm run eval:validate -- --control noop
```

参考操作应 20/20；noop 故意不操作只说完成，必须 0/20，命令退出 0 表示判分器正确拒绝假完成。`npm test` 现包含 `test/settle.test.js`（动作后有界等待、弹出标签、未完成请求、缺 text 零执行）；等待上限由 `POGO_BROWSER_SETTLE_MS` 控制，见 [worker README](../browser-runtime/README.md)。

```sh
# 从仓库根开始：工具、模型切换、租约与桥接定向回归
cd pogo
go test -race ./agent ./llm ./cmd/browser-eval-agent
go test -race ./tools ./web -run 'Test(Browser|NativeBrowser|SessionBrowserContract|RegistryBrowserImage|ToolImage|ToolScreenshot)' -count=1
go vet ./agent ./llm ./tools ./web ./cmd/browser-eval-agent
```

真实 Go→worker 用例默认可能 SKIP。先按 [worker README](../browser-runtime/README.md) 在本机独立启动临时 worker，给测试进程设置同一服务的 `POGO_NATIVE_BROWSER_TEST_URL` 和 `POGO_NATIVE_BROWSER_TEST_TOKEN`，再执行：

```sh
# 在 pogo/，使用隔离 worker；必须检查日志确实运行，没有 SKIP
go test -race ./web -run '^TestNativeBrowserReal(Runtime|Picker)$' -v -count=1
```

```sh
# 从仓库根开始：前端；构建到临时目录以免覆盖工作区已有 dist
cd pogo/web/ui
npm ci
npm test -- src/agent/nativeBrowser.test.ts src/agent/browserPane.test.ts src/agent/immersiveLayout.test.ts src/agent/browserExec.test.ts
npm run check
npm run build -- --outDir /tmp/pogo-browser-handoff-ui-dist
```

界面回归：在 `pogo/web/ui/` 运行 `npm run dev -- --host 127.0.0.1 --port 5187 --strictPort`；另一个终端在同目录运行 `node dev/immersive-browser-qa.mjs http://127.0.0.1:5187`。入口 `/dev/immersive-browser.html` 使用真实组件与内存 API，涵盖沉浸/工具栏/选取；它不连接生产账户，也不能替代真实后端验收。默认截图输出 `/tmp/pogo-immersive-qa`。

早期的已知失败（4 个首页路由断言、Skill 夹具只读目录清理、`TestWorkflowRunEndToEnd`、Studio 模型切换用例在 `-race` 下失败）已于 2026-10-02 修复；`2f58f40` 上前端全量与 `go test -race ./...` 均通过（macOS arm64，见[项目现状](project-status.md#本次验证记录)）。此后出现的失败应视为回归并重新判定归属，不能以“历史问题”为由跳过。

### 真实模型对照

用户在本会话已明确指定 `glm-5.3`，允许使用项目现有 API key，预算充足；此授权针对本项目优化评测，不扩大为在真实业务网站自动执行任意写入。凭据通过现有配置与 `key_env` 注入进程，不打印密钥/请求头，不放在命令参数或文档中。

```sh
# 从 pogo/ 开始；使用新二进制和新输出目录，保留历史基线
go build -o /tmp/pogo-browser-eval-agent-next ./cmd/browser-eval-agent
cd browser-runtime
npm run eval:model -- --agent /tmp/pogo-browser-eval-agent-next --model glm-5.3 \
  --providers /absolute/path/to/providers.json --seed 20261001 --concurrency 2 \
  --out /absolute/path/to/new-run-directory
node eval/compare.js /absolute/baseline/summary.json /absolute/new-run/summary.json \
  --out /absolute/new-comparison.html --json /absolute/new-comparison.json
```

占位路径需替换；API key 需按 provider 的 `key_env` 已注入环境。运行器不主动读取生产 Docker 环境。先做无模型验证，再跑同协议对照；任务集、提示词或预算变化要另建实验组。除了通过率，同时报告中途工具错误、恢复、重复提交、耗时和 token，不能只挑最终 100%。完整选项见 [eval README](../browser-runtime/eval/README.md) 和 [Go 适配器说明](../cmd/browser-eval-agent/README.md)。

## 8. 部署、数据和回滚

当前使用 `w_deploy`，不是只启动 `pogo/compose.yml`。生产生命周期需要同时加载 `.env`、私有 `.browser.env` 和两份 Compose。`.browser.env` 保存 worker token/镜像等，权限 0600。不要直接输出解析后的完整 Compose 或 Docker Env，里面可能含凭据。

保留的主服务挂载为：`pogo_pogo-home → /home/pogo`、`pogo_pogo-skills → /var/lib/pogo-skills`、`w_deploy/panels-data → /data`、`w_deploy/init/providers.json → /home/pogo/.pogo/providers.json`、`w_deploy/init/users.json → /home/pogo/.pogo/users.json`。检查时同时比对读写属性，规范化 Docker Desktop `/host_mnt/Users/...` 与 `/Users/...` 别名。

最近备份含 home/skills/panels-data 归档、部署配置与账号/模型配置；目录 0700，文件 0600。现有 `deploy.sh backup` 没有覆盖全部 Skill 卷和配置绑定，不能直接视为完整备份。`deploy.sh restart` 会对项目执行 force-recreate；只改主服务时应定向更新 `pogo`，不要无意重建 worker。

后续部署流程：检查活动任务 → 保留旧镜像 → 构建匹配架构的新镜像 → 主服务停止写入后做完整备份 → 更新目标镜像配置 → 定向启动 → 认证 API/浏览器/业务记录验收 → 记录版本与证据。单纯源码修改不会更新 Docker。

```sh
# 仓库根构建主服务；标签应换成该次独有版本
docker buildx build --platform linux/arm64 --load \
  -f pogo/Dockerfile -t pogo:browser-next-arm64 .

# 完成备份并将 compose.yml 中主服务 image 改为新标签后，才运行这一段
DEPLOY_DIR="$PWD/w_deploy"
docker compose --project-directory "$DEPLOY_DIR" \
  --env-file "$DEPLOY_DIR/.env" --env-file "$DEPLOY_DIR/.browser.env" \
  -f "$DEPLOY_DIR/compose.yml" -f "$DEPLOY_DIR/compose.browser.yml" \
  up -d --no-deps --no-build --pull never pogo
```

上述命令只升级主服务。改了 `browser-runtime/src` 或 worker 依赖时，需单独构建 worker 并更新 `.browser.env` 的镜像标签，再协调升级；需保留 sandbox/seccomp/只读根/资源限制。本机已验收 ARM64；迁移 x86 应用 `linux/amd64` 构建两种匹配镜像并在目标机验收，不把本机通过视为 x86 已实测。

回滚主服务时，将主镜像改为上面的保留回滚标签，再用相同双 env/双 Compose 命令定向启动；保留当前数据卷。恢复备份会覆盖备份后数据，是另一个操作，不能将普通镜像回滚等同于自动恢复数据。版本历史位于 `w_deploy/.image-history`。

**主服务退出会关闭已有浏览器 context，即使 worker 容器没有重建，也不保证网页/登录态保留。** 对话、文件、账号和 Skill 数据持久化与浏览器登录态是两件事。发布说明应提示必要时重新打开页面、重新登录。

## 9. 已知边界和建议下一步

| 优先级 | 工作项 | 建议落点与验收标准 |
| --- | --- | --- |
| 已完成 | 已部署源码入库 | 2026-10-01 按主题拆分为 `450e0ac`…`ebf4d15`，逐个导出验证并推送。`web/ui/dist` 入库（`go:embed` 需要；镜像构建时从源码重建），与源码构建结果一致；`.image-history`、`panels-data/`、`compose.yml.bak` 加入忽略；`w_deploy/.env` 平台与 `compose.yml` 镜像标签是本机部署状态，保留为未提交改动 |
| 已完成 | 恢复绿色测试基线 | 2026-10-02 在 `2f58f40` 全绿：Vitest 74 文件 / 3627 用例，`go test -race -count=1 ./...` 30 包、0 个 DATA RACE，dist 与源码构建一致。修复为 `443c7f1`、`a04c4d5`（生产代码，未部署）、`3e00f8f`、`40ef898`、`2f58f40`（CI-01）。尚未在 Linux 上运行测试 |
| P0 | 先跑通 Linux CI，再经 MR 合入 | `pogo/.github/workflows/ci.yml` 未接入当前仓库的默认流水线入口。在分支上增加 GitLab CI 并配置 Linux runner；除 UI/Go 外，还要执行 worker、评测与部署配置测试，并显式启用真实 Chromium/沙箱集成，核对关键用例未 SKIP。通过后再经 MR 合入，不直接推 `main` |
| P1 | 部署工作流修复 | `a04c4d5` 只影响主服务：重建主服务镜像并定向更新 `pogo`，worker 不动；按 §8 备份与验收，覆盖一次带人工节点的工作流运行 |
| P1 | 扩大评测证据 | 现有 20 项已连续四轮 20/20，只能比较效率。加未见布局、更长任务、登录失效/人工交还、动态页面；每组多 seed、至少 3 次重复；另跑一个视觉模型验证截图/坐标链路；保持旧 20 项固定回归。独立公开基准需另立协议和业务授权范围 |
| P1 | 固化生产 smoke 与发布流程 | 10-01 部署所用的 `/tmp/pogo_settle_deploy.py`、`/tmp/pogo_settle_acceptance.py` 等已在 10-02 本机重启后被系统清理（已核实不存在），下一次部署前必须先补齐。在 `w_deploy` 重写为幂等、参数化脚本：双服务冷备份、定向切换、失败自动恢复，worker/人工/真实模型三段验收；去除个人路径和运行态依赖，配 mock 测试 |
| P1 | 继续压低调用与等待 | 线上仍是 10-01 settle；10-02 工作区已修等待范围与观察准确性，并补 Compose 参数传递，需发布后才生效。新的 `network pending` 只说明相关页面的网络观察，不证明业务未完成；调参前先核对慢正文/延迟 DOM 与真实业务回执。重复评测同时报告错误、调用、耗时和 token |
| P2 | 登录 profile 持久化 | 当前 context 内存存活期间可复用登录，主服务关闭、会话删除/回收、worker 重启会丢失。先设计用户/站点隔离、加密与退出清理，再验证恢复；不要直接共享全局 profile |
| P2 | 文件上传/下载交接 | 当前未接通，worker 下载禁用。需贯通工作区文件授权、传输、产物归属/限制与 UI 确认，再加入真实业务判分 |
| P2 | 画面流畅度与资源 | 当前是截图轮询，不是连续桌面流；快速动作可能只看到最新结果。先测截图 CPU、带宽、并发和延迟，再比较增量帧/视频传输，不先承诺“实时” |
| P2 | 能力诊断可见性 | 可增加受鉴权的会话能力摘要用于排错，明确 native/legacy、Direct/Helper/None 和缺失原因；不能泄露凭据，也不能把目录当真实 schema |

其他边界：复杂变换 iframe 不确定时降级为容器；封闭 Shadow DOM、canvas 内部对象不解析。布局动画/扫描超预算可拒绝选取，版本不能证明每个合成器像素被冻结。验证码、设备绑定认证、系统权限窗、剪贴板和本机浏览器扩展均未完整支持。不要通过关闭沙箱、放开任意 eval 或绕过控制权来补这些能力。

历史临时脚本 `/tmp/pogo_capabilities_acceptance_plan.py`、`/tmp/pogo_native_browser_acceptance.py`、`/tmp/deploy_pogo_capabilities.py`、`/tmp/pogo_settle_deploy.py`、`/tmp/pogo_settle_deploy_lib.py`、`/tmp/pogo_settle_acceptance.py` 及其状态文件已于 2026-10-02 本机重启后被清理（已核实均不存在），不再是可用线索。部署与验收流程以 §8 和[运行时部署记录](native-browser-runtime.md)为准，长期证据以文档、评测目录和私有备份为准。

## 10. 可直接交给下一位 Agent 的任务说明

> 先阅读根 `HANDOFF.md` 的最新补充及本专题文档，保留 `feat/browser-use-native` 工作区改动。10-01 settle 已部署；10-02 的等待/观察/指针准确性修复已提交为 `55af543`，Compose 透传为 `d96c337`，尚未部署，重复评测和快照存于 `browser-runtime/eval/runs/stability-20261002/`。工作流修复 `a04c4d5` 同样尚未部署。STU-01 已提交为 `3974d98`；工作流结束/持久化、Linux CI 与参数化发布工具的后续状态见根 `HANDOFF.md` 第 12 节。完成真实 worker/沙箱验证与现场发布验收接入，再经 MR 合入并发布；不要先合并再补测试。之后扩大未见布局、长任务、多 seed 与视觉模型评测。保留旧成绩，不选择性替换失败记录；保持不重复写入、整批预校验、租约/取消 ACK、只读选取与版本约束。源码、镜像和运行状态分开记录，受控任务通过不等于通用 SOTA。
