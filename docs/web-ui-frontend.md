# pogo Web 前端（web/ui 实现现状）

> 本文从 `web-ui-design.md` 的 §10 拆出。那一节写于前端只有 2955 行的时代，已严重过时；这里按当前代码重写。harness（agent loop、工具、闸门）的设计见 `harness-design.md`；web 后端、事件协议、断线重连与缓存见 `web-ui-design.md`。本文只讲浏览器这一半。
> 文中的行数与计数均为 **2026-08 快照**（`find` / `wc -l` / `grep -c` 实测），代码继续长，数字以仓库为准。

## 1. 规模与结构

`web/ui/src` 下 `.vue`/`.ts` 共 **191 个文件、37,604 行**；排除 36 个测试文件后是 155 个文件、32,267 行（`.vue` 61 个、`.ts` 130 个）。对照旧文档写作时的 2955 行，前端已经大了十倍，§10 那种一张表说完的写法不再可行——这是拆出本文的直接原因。

| 目录 | 文件数 / 行数 | 职责 |
|---|---|---|
| `views/` | 1 / 3,991 | `AgentView.vue`，唯一的视图 |
| `components/`（顶层） | 66 / 17,612 | 时间线卡片、dock、各面板、picker、gate、图表等全部组件 |
| `components/tools/` | 16 / 2,188 | 9 个工具渲染器 + ToolRow / IncomingArgs / HiText / SubagentStepRow 等共享件 |
| `agent/` | 49 / 9,620 | 纯逻辑层（26 个源文件 + 23 个测试文件），零 Vue 依赖的部分都在这 |
| `api/` | 2 / 1,200 | `types.ts`（wire 镜像）+ `client.ts`（REST / 控制通道） |
| `theme/` | 8 / 1,064 | 15 套皮肤、token 派生、图表调色板 |
| `i18n/` | 47 / 1,883 | `index.ts` + `messages/` 下 45 个命名空间文件（zh-CN / en 双语言同文件携带） |

最大的文件是 `views/AgentView.vue`：**3,991 行**，一个文件装了整个外壳——会话侧栏、顶栏、时间线、输入区、dock 宿主、`/skill:` 补全、右缘定位条、登录门。`views/` 只有一个文件不是疏忽：这是单会话应用，没有第二个页面，连路由都不需要。

## 2. 数据层

- **`api/types.ts`（844 行）是 `web/wire.go` 的人工镜像。** 文件头注释自述同步纪律：*“Mirror of web/wire.go. That file is the source of truth; when the two disagree, it wins.”* 协议变了先改 wire.go。`ToolName` 现在是十个：`read / ls / find / grep / write / edit / bash / subagent / subagent_tasks / todo`。
- **`api/client.ts`**：token 从启动 URL 的 query（`?token=`）读入后写进 `sessionStorage`（键 `pogo-token`），刷新不带 query 也能进；缺席或被拒时启动停在 gate。`authStatus()` 问后端部署用的是 `token` 还是 `password` 通道（见 §9）。
- **`agent/sse.ts`**：SSE 帧解析，纯函数，零依赖，可离线单测。
- **`agent/useAgentStream.ts`（767 行）**：SSE 连接、事件折叠进 `live`、断线重连拿 snapshot 重建。`liveThrottleMs = 80`（`useAgentStream.ts:16`）：delta 逐个到达，但 `liveVersion` 最多每 80ms 自增一次，`buildTimeline` memo 在它上面——打字效果保留，时间线不会每 token 重建。`maxPendingOutput`（32KB）与 `maxPendingFrames`（400）与 `web/hub.go` 双侧对齐，注释自述理由：客户端留得比 snapshot 多，重连时画面会 visibly 缩。
- **`agent/timeline.ts`（999 行）：纯函数投影。** `buildTimeline(messages, results, live)` 返回扁平的 `UserItem | TurnItem` 列表——**没有递归树**，pogo 只有两层。投影之外另有几趟独立的处理：
  - `markSupersededTodos`：todo 清单是状态不是事件，只有最后一条**已结束且成功**的写入算当前计划，其余打 `superseded`、收成一行并降透明度。独立一趟而不是投影时顺手判断的理由：「这条是不是最新」是关于整条时间线的事实，边建边判在建完之前不可能知道。
  - `adoptOrphanGates`：把进程死掉或时序错位留下的孤儿闸门/调用归位到宿主轮次。
  - `linkCorrections`：错误自纠正的启发式连线——同工具 + 同目标 + 更晚 + 前者失败，判为「在纠正前面那次」。是启发式，所以只画一行虚线小字的弱提示，不画箭头也不断言因果。

## 3. 渲染层

- **`ToolCall.vue` 按工具名路由到 9 个渲染器**（`components/tools/` 实测）：`ReadResult` / `ReadManyResult` / `LsResult` / `SearchResult`（find 与 grep 共用）/ `EditResult` / `WriteResult` / `BashResult` / `TodoResult` / `SubagentResult`。
- **`DiffView.vue`**：edit/write 的主角，渲染 Go 侧算好的 `details.diff`（前端不在 JS 里重算，两个 diff 实现迟早不一致）。`collapseOver: 28`——超过 28 行起折。
- **`CodeBlock.vue`**：超过 8 行折叠（`collapsible = rowCount > 8`），`collapsedHeight` 140px / `expandedHeight` 460px；行号、复制、按扩展名着色。
- **语法高亮有两只同名片，分工不同**：
  - `components/highlight.ts`（480 行，零依赖）：手写 tokenizer（scanner 不是 parser），产出 `Token[]` 按文本节点渲染。不产 HTML 是安全决策而非体积决策：工具打印的任何内容都不可能变成标签。不变量 `tokenize(code).map(t => t.text).join("") === code` 恒成立，猜错只错颜色，不丢字不死循环。
  - `components/tools/highlight.ts`（42 行）：skills 面板过滤的命中分词（`highlightSegments`），刻意用子串而非 fuzzy——高亮的 run 必须字面上是用户打的那段。
- **`AnsiText.vue` + `components/ansi.ts`**：bash 输出里命令自己打印的 ANSI SGR 颜色解析成内联样式；光标移动/擦除/OSC 类转义一律丢弃，token 仍是文本节点，无注入面。settled 与运行中 live 输出共用同一份组件。
- **`MarkdownView.vue`**：消息定稿后跑一次 markdown-it；流式期间渲染纯文本——每 token 重跑 `md.render` 是参考实现里最大的性能错误。
- **图表两种，都懒加载**：`MermaidDiagram.vue` + `mermaidRuntime.ts`（mermaid 与 dompurify 均动态 `import()`；`theme: "base"` + `themeVariables` 从 CSS 变量取色，换肤需重渲染，`themeVersion` 为此存在；产出经 `sanitizeSVG` 过 DOMPurify）；`EChartsDiagram.vue` + `echartsRuntime.ts`（echarts 动态 `import()`；无全局 init，主题对象作为参数传给 `echarts.init`，换肤 = dispose + 重新 init；`sanitizeEChartsOption.ts` 清洗模型给的 option）。

## 4. 主题系统

- **`theme/themes.ts`：15 套皮肤**（`grep -c 'id: "'` 实测），**6 亮 9 暗**。两套自研——`paper`（默认亮，暖纸底 + clay 主色）与 `graphite`（默认暗，paper 的夜面）；其余 13 套借用成熟开发者工具调色板（Catppuccin、Rosé Pine、Everforest、Nord、Tokyo Night、Gruvbox、Dracula、Solarized、One Dark、Primer），每套在 `origin` 字段署名。
- **`theme/build.ts`：派生而不是手写。** 一套种子只有 12 个颜色 + 终端两个表面，`buildTokens` 从中派生出全部 token（六档填充、七档边框、五级文字、五个语义色各自的九级梯度、阴影、语法色）。小种子图的不是省字，是一致性：逐 token 手写的皮肤会漂移（hover 与 rest 的距离各套不一），派生保证每套皮肤内部比例相同，差异只在调色板。
- **`theme/index.ts`**：`THEMES` 在模块加载时全部构建（坏 hex 在 boot 就炸，而不是第一次选那套皮肤时）。`initTheme()` 在 `createApp` mount **之前**把皮肤写到 `<html>`（inline custom properties + `data-theme` + `class="dark"` + `color-scheme`），第一帧就是对的——从 `onMounted` 应用会在深色皮肤上白闪一下。选择存 `localStorage["pogo:theme"]`；**未选择时跟随系统**（`prefers-color-scheme` 变化也跟随），且跟随本身不落盘——否则一次暗色访问就把皮肤永久钉死。
- **`ThemePicker.vue`**：行是样本不是名字——三色 swatch（page / canvas / accent），按亮 / 暗分组，名字下方显示 `origin` 署名。
- **两条旧决议作废，如实记录**：
  - 旧文档「**整套 UI 是黑白配色**」已被取代：`styles.scss:44` 把 `--el-color-primary` 定为 clay `#bd5f3c`（界面唯一饱和色，只花在 live 状态上），外加本文这整套主题系统。
  - 旧文档「**`ElMessage` 已全部移除**」不再成立：`ElMessage` 目前在 `NavStart.vue`、`SettingsDialog.vue`、`SkillsPanel.vue`、`StarterCards.vue`、`AgentView.vue` 五处使用，`ElMessageBox` 在 `AgentView.vue` 一处。

## 5. dock 与面板

- **右侧 rail 恰好三颗：`files` / `hub` / `skills`**（`dockSheets.ts` 的 `SheetId`）。文件头注释自述为什么 rail 不随 `-web-panel` 注册数增长：rail 是 chrome，承载运行时自己的 affordance，不是领域内容——一个 panel 一个图标曾把数据应用摆到「files」旁边，错位感就是这么来的。
- **hub 是浏览器式 tab strip 而不是下拉**（`dockSheets.ts:11-16` 注释）：shell、每个 `-web-panel`、每个打开的 subagent transcript、每个浏览器页都是 strip 上的对等 tab，strip 才能一眼看出哪个在台前、哪些还在跑；下拉恰好藏掉这些。
- **tab id 四种**：`shell` / `panel:<name>` / `subagent:<child session id>` / `browser:<n>`（`dockSheets.ts:20-33`）。**subagent sheet 已实现**：点 subagent 卡片的锚点在 hub 开一个该子会话的 transcript tab（`SubagentSheet.vue`）；同一卡片点两次是聚焦已有 tab 而不是堆重复（`openTab`）。关 tab 后激活左邻——浏览器把你放在哪，这里就放在哪。
- **持久化键**（localStorage，`grep` 实测）：
  - `pogo:active-sheet` / `pogo:hub-tabs`：当前 sheet 与 hub 的 tab 集合。`pogo:hub-tenant` 是 tab strip 之前的单 tenant 记忆，只读一次做迁移；`migrateSheet` 每次 boot 都跑（幂等），所以降级再升级不会把人困在不存在的 sheet 上。
  - `pogo:files-ratio`：dock 宽度是侧栏之后剩余空间的**比例**（0.15–0.5，0.5 是硬锁——会话永远不窄于一半），拖的是比例、像素从实测父宽派生。旧键 `pogo:files-width` 已被取代并在 boot 时主动删除（`DockArea.vue:163-168`）。
  - 其余在用的键：`pogo:files-height`、`pogo:files-open`、`pogo:shell-open`、`pogo:sidebar-collapsed`、`pogo:worklog-open`、`pogo:theme`、`pogo:diagram-palette`、`pogo:files-sort`、`pogo:files-layout`、`pogo:nav-cards`、`pogo:closed-projects`、`pogo:telemetry`。

## 6. 输入区

- **可执行斜杠命令只剩 `/compact` 一个**（`AgentView.vue:655-658` 的 `COMMANDS`，带 `noAbbrev`：`/c` 不会扩成 `/compact` 把会话换掉）。注释自述理由：其余命令做过的事这里都有专属控件（策略、模型、上下文仪表），它们只是 TUI 习惯；TUI 自己的命令表不动。`/skill:` 不是命令，是服务器展开的 prompt（见 §9）。
- **输入栏三个入口**（模型、上下文、审批是发送前一刻做的决定，所以控件在发送按钮旁边）：
  - `PolicyPicker`：strict / standard / auto 三模式；auto（完全访问）常驻橙色——警示跟语义走，不跟选中态走；轮次受限的 auto 运行时按钮上显示剩余轮次（`·N`）。
  - `ModelPicker`：按钮 + popover 列表；未配置 key 的模型列出但不可选——隐藏会让人以为模型没了。
  - `ContextMeter`：常驻两段式细条 + `82K/200K · 41%` 式读数；占比 **≥70% 黄、≥85% 红**（`contextEstimate.ts:128-129` 的 `contextLevel`）；高档位时在输入框上方出撞线提示条。compaction 已实现，提示条与仪表都指向手动压缩（`/compact` 与仪表按钮同走 `control{action:"compact"}`），「构成是估算、总量是实测」的原则不变——`snapshot.usage` 是计费口径、`context_tokens` 是占用口径，协议侧见 `web-ui-design.md` §6 与 §7.3。
- **右缘轮次定位条**：内容高度超过视口 **1.2 倍**才出现（`AgentView.vue:1187` 的 `contentOverflow`）——短对话不需要地图。每条用户提问一个点，位置从渲染出的 `.ask` 元素量出来，不是按消息数估。
- **贴底跟随**：滚动事件里判断距底 **> 80px** 则 `stickToBottom = false`，回到底部恢复，只在 true 时自动滚（`AgentView.vue:1096`）——用户往上翻之后不抢回底部。脱离贴底时浮出居中「回到底部」pill（Codex / ZCode 同款位置，`.jump-bottom`），点击平滑滚回，`onScroll` 过阈值自动恢复跟随。

## 7. Element Plus 与包体

- **`main.ts` 全量导入 Element Plus**（含 `dark/css-vars`），注释自述理由：二进制从 localhost 服务，更小的包体不如更少的活动部件；Element 的 CSS 变量同时也是组件样式的基准。
- **实际使用面**（grep 模板实测）：`el-button`、`el-checkbox`、`el-config-provider`、`el-dialog`、`el-dropdown(-menu/-item)`、`el-form(-item)`、`el-icon`、`el-input`、`el-popover`、`el-radio(-group)`、`el-tooltip`，外加 `ElMessage`（5 处）与 `ElMessageBox`（1 处）；`@element-plus/icons` 在 22 个文件使用。
- **分包靠动态 `import()` 天然成 chunk**：mermaid、echarts、dompurify、xterm（`ShellPanel.vue`）都是首次使用时才加载；`vite.config.ts` 没有 `manualChunks`，只有 `chunkSizeWarningLimit: 1500`。
- **`dist` 入版本库并被 embed**：`git ls-files web/ui/dist` 实测 109 个产物文件，`.gitignore:19` 有注释说明原因——`web/ui.go` 的 `//go:embed all:ui/dist` 引用它，checkout 里没有 dist，`go build` 直接失败。推论不变：改完前端不重新 build，二进制里还是旧页面。

## 8. 测试现状

2026-08 快照：**36 个 `.test.ts`，472 个 `it(` 用例**（`grep -c` 实测）；其中 `agent/timeline.test.ts` 一个文件占 **100 个**。

分布说明取向：`agent/` 目录 23 个测试对 26 个源文件——纯逻辑（timeline 投影、contextEstimate、fuzzy、followups、面板桥接、SSE 录制回放等）接近全覆盖；`components/`、`theme/`、`i18n/` 各有一些纯函数测试（highlight、ansi、dockSheets、fileTreeStore、sanitizeEChartsOption、theme、diagramPalettes 等）。**已知缺口：没有组件渲染测试，没有 E2E**——9 个渲染器、gate 交互、dock 拖拽这些只有人工验证。

## 9. 杂项

- **`/skill:` 补全与 SkillBlock**：输入恰为 `/skill:` 前缀时列出候选（`GET /api/skills` 挂载时取一次——skills 启动即固定，无需刷新）。展开后的 `<skill name= location=>` 块由 `SkillBlock.vue` 渲染成默认折叠卡；`parseSkillBlock`（`agent/timeline.ts:931`）解析时去掉服务器加的 “References are relative to …” 首段——那是给模型的，读者不需要看两遍。用户在命令后面自己打的那段话始终可见：它是请求，skill 只是上下文。
- **置顶 / 改名前端**：会话项悬停 ⋮ 菜单走 `PATCH /api/sessions/{id}`（`client.ts:181`）；后端的 meta 追加记录机制见 `web-ui-design.md` §10 留下的指针，接口清单见其 §11。
- **登录门双形态**：`AgentView.vue:2190-2191`——`authStatus().mode === "password"` 时挂 `LoginGate`，否则挂 `TokenGate`（带 `:rejected` 态），对应后端的 token 与密码两条认证通道。
