> 2026-09-30：本文包含历史设计。Git 管理、Git 上下文和自动 worktree 已退役；当前委派行为见 [subagent](subagent.md)，迁移边界见 [退役说明](git-retirement.md)。

# pogo harness 设计

> 文档范围：核心机制与历史设计演进，2026-09-29 补齐当前边界。平台完整现状、缺陷与验证见 [project-status.md](project-status.md)，依赖与 Go 版本以 `go.mod` 为准。
> Web 层的设计见 `web-ui-design.md`；使用说明见 `../README.md`。
> 行为有歧义时以本仓库当前源码和测试为准；pi 是早期设计参照，不是此 fork 的行为权威。本文的历史行数和研究记录不代表当前版本验收。

## 1. 目标与非目标

最初目标是用 Go 实现参考 pi 的 coding agent。当前项目已扩展为 Agent Studio、Skill Runtime、MCP 和工作流平台，完整工具执行的目标环境仍是 Linux 容器。本文主要保留核心 harness 的设计依据；[TUI 退役分析](tui-platform-direction.md)尚未实施。

刻意做减法，继承 pi 的哲学。**仍然不做**：plan mode、全屏 TUI（原有的 dock + 滚动区终端前端 `tui/` 已于 2026-11-01 移除，交互走 Web）。MCP 协议层已双向落地——server 在 `web/mcp.go`（`POGO_MCP=1`），client 在 `mcpclient/`，分别见 §13 表与 §13.1。

**已做的核心机制**：事件循环、按入口和配置组装工具、会话持久化、模型切换、重试退避、并行工具执行、审批接口、HTTP/SSE、skills、subagent 与 worktree、任务清单、上下文清理、溢出恢复及手动/可选自动摘要压缩。工具数量不是固定产品常量；工作流等入口的装配差异见现状文档。

**历史清单曾落后于实现。** todos、上下文清理、手动摘要和可选自动摘要都已逐步落地。当前摘要默认不自动，显式 `-compact auto` 可以启用自动路径，详见 [自动压缩](auto-compact.md)。剩余平台缺口以 [项目现状](project-status.md) 为准。

> 两次都是「做了但清单没改」，而不是「清单写了没做」。所以这里要说清哪个方向更危险：**一份把已完成项写成缺口的清单，会让读它的人去重做已经做完的东西**，判断「下一步做什么」的成本因此高于做那件事本身。§13 顶上那段记的是同一件事。

> 早期 `architecture-review.md` 已删除。当前入口为 [文档索引](README.md) 和 [项目现状](project-status.md)；设计历史不能替代代码、测试及明确日期的部署记录。

## 2. 心脏是一个 ~200 行的循环

先说口径，因为这个标题会被全文行数反驳：`agent/loop.go` 全文已 ~1270 行（2026-08 快照，`wc -l agent/loop.go`），核心的 `run()` 去注释空行约 175 行（`agent/loop.go:536-851`），其余是闸门插桩、预算、分段派发与事件上报。标题说的是后者。去掉这些之后，核心逻辑就是：

```
loop:
  resp = LLM(systemPrompt + messages + tools)   // 流式
  messages += resp
  if resp 无 toolCall: break
  for call in resp.toolCalls:
      result = tools[call.name].execute(call.args)
      // 工具失败不抛异常，错误文本作为 tool result 回传，让模型自我纠错
      messages += toolResult(call.id, result)
```

pi 的 `agent-loop.ts` 有 792 行，真正的循环只有几十行，其余是钩子、steering、事件上报。所以「200 行心脏」的判断成立，但**极简实现的风险不在循环本身**，在三个容易被低估的地方：协议边界、工具的错误语义、`tool_result` 的消息形状。

### 必须保留的设计：工具失败不中断 loop

错误文本作为结果回传给模型，模型自己纠正。这是 agent 可用性的分水岭。

实测链路：让模型在一个有两处 `x = 1` 的文件上盲改 → `edit` 报「matches 2 places，加上下文」→ 模型自己去 `read` 补上下文 → 重试成功。

同一个设计延伸到审批闸门：**拒绝是给模型的信息，不是运行的终止**（见 `web-ui-design.md` §8）。

## 3. 包结构

```
main.go          CLI：参数、一次性运行（-p / 管道）、会话与模型的装配
help.go          中英双语帮助，从实际 flag 定义生成
serve.go         -web 的接线：token 生成、绑定检查、优雅关闭
chain.go         链式运行（-max-runs）与判定者（-evaluate），见 §12.5
checkpoints.go   -checkpoints-prune
gatefd.go        子进程经继承 fd 把审批决定转发给父进程（§13 的闸门行）
jsonmode.go      -mode json：stdout 每行一个事件
textmode.go      -mode text：运行结束后只打印最终回答
subagent.go      子进程的装配（toolOptions、深度）
worktrees.go     -worktrees / -worktrees-prune

llm/             协议层（对应 pi 的 packages/ai）
  types.go         中立消息类型 + Client 接口
  openai.go        手写 SSE 客户端 + 重试（SSE 解析约 155 行，openai.go:257-411）
  wire.go          线上协议结构体（含 reasoning_content 等非标字段）
  convert.go       中立类型 ↔ 线上格式的唯一边界
  retry.go         退避策略
  price.go         Price 与 Cost：子集字段的算术放在类型旁边（§12.3）

agent/           核心（对应 agent-loop.ts）
  loop.go          主循环、并行执行、闸门插桩、预算与停滞判定、溢出后的强制清理（§9.5）——本包最大的一个文件
  contextedit.go   上下文清理：候选收集、淘汰顺序、占位符（§9）
  compact.go       摘要压缩原语；手动触发及显式 opt-in 自动路径（§9.6 / auto-compact.md）
  gate.go          ToolGate 接口
  event.go         事件定义
  prompt.go        system prompt
  endreason.go     EndReason 与 disposition 分类表（§12.1）
  persist.go       TurnPersister：事件流的 per-turn 落盘（§8）
  softcap.go       软轮次上限的检查点消息（§12.4）
  todofinish.go    run 收尾时的任务清单追问

tools/           内建十个工具
  tool.go          Tool 接口、Registry、Options（按只读/深度决定注册哪些）、路径守卫、Resolve 导出
  result.go        Result{Text, Details} 与其中九种 Details
  schema.go        JSON Schema 字面量
  validate.go      参数校验（在闸门之前，见 §5）
  pathlock.go      per-canonical-path 锁
  procgroup_*.go   进程组（平台相关）
  stream.go        StreamingTool 的合流与 partial 预算（三层上限的另外两层在 web/hub.go 与本包 truncate.go）
  read/ls/find/grep/write/edit/bash.go
  search.go        find 与 grep 共享的遍历：剪枝目录、遍历上限
  todo.go          任务清单（不持有状态）
  subagent.go      子代理：explore/edit 双模式、模型池、SubagentDetails
  subagent_bg.go   后台委派：BgRunner 与 subagent_tasks
  guard.go         edit 子代理的 bash 越界拦截
  journal.go       文件 pre-image 快照（工作区改动追踪，见 web-ui-design §16）
  truncate.go      2000 行 / 50KB 双限
  spill.go         截断掉的输出落盘：内容寻址、可读根、统一 note（见 §5.3.1）
  browser.go       browser 工具：web 会话的第二个执行器（串行）
  browserfd.go     前台子代理经管道借用会话浏览器
  gatechan.go      channel 形态的闸门（fd 继承方案的工具侧）
  manifest.go      skill 的 tools.json 解析（MCP Tool 对象形状）
  synth.go         声明式工具合成一等工具

skills/          skills 的发现、校验与 prompt 渲染（见 §5.4）
  skills.go        扫描、去重、优先级、Options
  frontmatter.go   手写 YAML 子集解析器
  prompt.go        <available_skills> 渲染、/skill: 展开、read 徽标匹配
  embedded.go      go:embed 内置 skill 与启动物化
  starters.go      空对话的开场卡片（内容来自 skill 目录）
  builtin/         三个内置 skill：browser-use、pdf、web-drawing

memory/          跨会话记忆：skills 机制的可写镜像（见 §5.5）
  memory.go        两层目录的解析与创建、列表、上限、age/size 格式化
  prompt.go        <memory> 渲染 + 「这是记录不是指令」的声明

diff/            Myers 行级 diff
session/         JSONL 树 {id,parentId}，resume / fork / List
  composition.go   成分核算与标定（§9.6）
config/          provider 与模型目录、密钥解析（目录完全来自用户文件，见 §10）
analyze/         -analyze-session：单会话报告与轮次分布（§9.7）
cmd/             measure-schema（schema 量尺，见 §16）、test-overhead
git/             只读 git 状态（分支、未提交计数），进 system prompt 与 GitBar
wire/            loop 事件 → 线上事件的唯一翻译（§12）
worktree/        子代理的 worktree 隔离
web/             HTTP + SSE 层，见 web-ui-design.md
assets/          仓库用图
```

平台相关代码按 `_unix.go` / `_other.go` 分文件：`tools/procgroup_*`、`worktree/alive_*`。非 unix 版本是有意的保守实现（`alive` 保守返回活、进程组设置是 no-op），跨平台正确性靠 `go build` / `go vet` 对 linux、windows、darwin 目标各跑一遍验证——只读代码数不出有几处平台调用，一个包报错就停。

`skills` 不依赖 `agent` 也不依赖 `tools`：它交出一个 prompt 字符串和一组只读根，由 `main.go` / `serve.go` 分别喂给 `agent.SystemPrompt` 和 `tools.Default`。循环和路径守卫都不知道 skills 这个概念存在。

## 4. 协议层：OpenAI 兼容

### 端点：本 fork 一个都不编译

**本 fork 不编译任何 provider 或模型**（`config/catalog.go:73-89` 两张表都是空的），目录完全来自用户的 `~/.pogo/providers.json`，缺文件拒绝启动（见 §10）。下面这张表是内置目录时期逐条 curl 实测留下的**外部服务事实**，如今的用途是写自己 `providers.json` 时的参照——它不是、也从未是这份 fork 的配置。

| provider | baseURL | 模型 |
|---|---|---|
| kimi（Kimi for Coding 套餐） | `https://api.kimi.com/coding/v1` | `k3`(1M)、`k3-256k`、`kimi-for-coding`、`-highspeed` |
| zhipu（GLM Coding Plan） | `https://open.bigmodel.cn/api/coding/paas/v4` | `glm-5.2` |

表里 glm-5.2 原本记的是 200K 窗口，后来被探针更正为 1M（那个教训见 §9.3 末尾）——**那也是内置目录时期的事**：现在窗口值是用户文件里的 `context_window`，pogo 无从替它担保。两个坑仍然是外部事实：

- **Kimi 套餐 key 不能用开放平台端点。** `api.moonshot.cn/v1` 和 `api.moonshot.ai/v1` 都返回 401，只有 `api.kimi.com/coding/v1` 认这个 key。它不是「备选」，是唯一入口。
- **模型 id 是 `k3`，不是 `kimi-k3`。** `/models` 返回的 id 就叫 `k3`（display name 才是 K3）。要写别名就在自己文件的 `aliases` 里写。

智谱 `/api/paas/v4` 也认同一个 key，但套餐该走 `/api/coding/paas/v4`，计费口径不同。

### 手写客户端而不用官方 SDK

设计初期定的是「用官方 SDK，别手写 SSE」，理由是 Anthropic 流式 `tool_use` 的增量 JSON 累积很难写。**换成 OpenAI 协议后这条不成立，已推翻：**

- OpenAI chat-completions 的 SSE 平坦得多：每帧一个 `data:` JSON，`[DONE]` 收尾，工具调用按 `index` 累加 `arguments` 字符串——解析函数约 155 行（`openai.go:257-411`，含重试判定与 usage 收尾）
- 两个 provider **都依赖非标字段**：thinking 走 `reasoning_content`（不在官方 schema 里），缓存命中走 `prompt_tokens_details.cached_tokens`。用生成式 SDK 取这些要绕 `ExtraFields`，比手写更啰嗦
- Chat Completions 的 HTTP/SSE 仍由本地客户端实现；这不等于整个协议层零依赖。当前工具协议兼容代码使用 schema/JSON 相关库，MCP 使用官方 SDK，持久化使用 SQLite；完整清单以 `go.mod` 为准。

代价是 SDK 白送的重试要自己写（见 §7）。

### 两处协议差异

**`tool_result` 的消息形状。** Anthropic 是一条 user 消息里装 N 个 `tool_result` block；OpenAI 是 N 条 `role:"tool"` 消息，紧跟在发起调用的 assistant 消息之后。中立类型保留「一条消息装 N 个 block」的形状（session 格式因此稳定），在 `convert.go` 的边界上展开。

**thinking 不回传。** Anthropic 的 thinking block 带 signature 必须原样回传；OpenAI 协议没有这个概念，`reasoning_content` 只存本地用于显示和 session。这也是**跨模型切换能保留历史**的原因——transcript 里没有任何 model-specific 的东西要翻译。

### usage 统计

`Input = prompt_tokens`，`CacheRead = prompt_tokens_details.cached_tokens`，**后者是前者的子集**（实测 `input:978, cache_read:768`）。cached 单独显示是为了让人看清缓存命中带来的成本差异，不是为了相加。

> 这一点与 Anthropic 协议相反：那边 `input_tokens` 不含 cache read，所以只显示 input 会看起来少一个数量级。迁移协议时踩过，早期的注释里还留着那个结论。

`agent.Agent.usage` 是**跨轮累加**的（每轮 `resp.Usage` 加进去），用于计费显示。**它不能表示上下文占用**——每轮都重发全部历史，所以累计值随轮次二次增长。占用要用单轮的 `resp.Usage.Input`，它随 `EventMessage` 一起发出，见 `web-ui-frontend.md`。

## 5. 工具

**数量口径**（2026-08 快照，`tools/tool.go:177-233`、`web/manager.go:720-783`）：内建**十个**工具。终端会话注册**九个**——`read` / `ls` / `find` / `grep` / `write` / `edit` / `bash` / `todo` / `subagent`；web 会话再加 `browser` 共**十个**注册，带后台委派（`subagent_tasks`）时**十一个**。skill 目录里 `tools.json` 声明式合成的工具另计（`tools/manifest.go`、`tools/synth.go`），它们作为一等工具走全部既有机制，不占这十个的名额。

### `Result{Text, Details}`

```go
Execute(ctx, args) (Result, error)

type Result struct {
    Text    string  // 回给模型，受 2000 行 / 50KB 截断
    Details any     // 只给 UI，不进 LLM 上下文，不截断
}
```

这条分离是浏览器界面的地基。`EditDetails.Diff` 就是前端 DiffView 的数据源。

两个要点：

- **`Details` 与 error 可以同时返回。** bash 非零退出时仍带 exit code 和输出——UI 正需要这些。
- **diff 绝不能进 `Text`。** 给模型看它刚写的 diff 是纯浪费 token。`tools_test.go` 有断言守着。

九种 Details 在 `tools/result.go`：`ReadDetails` / `ReadManyDetails`（`paths[]` 多文件读，含每个文件的字节区间）/ `LsDetails` / `FindDetails` / `GrepDetails` / `WriteDetails`（覆盖时才有 diff）/ `EditDetails` / `TodoDetails` / `BashDetails`。**第十种 `SubagentDetails` 刻意不在这个文件里**，它跟着 `tools/subagent.go`——那个工具连子进程的 usage 和 transcript 路径一起交回，形状与另外九种不同类。

两处与「Details 不截断」并存的上限，不矛盾：`maxDetailsDiff`（1 MiB，`WriteDetails` / `EditDetails` 的 display + unified 合计）超限后**只丢 diff 正文、保住 Added/Removed 统计**，因为一次全文件重写会把约两倍文件大小塞进每个 SSE 订阅者的 `tool_end`，再让浏览器在主线程上 `JSON.parse`。截断限的是**渲染**，不是模型看到的东西——那是 `truncate.go` 的事。

### Details 怎么活到会话文件里

`llm.Block` 有一个 `Details json.RawMessage`，loop 在构造 tool_result block 时把工具的结构化输出编码进去。它是**这个类型上唯一绝不能上行的字段**。

它上不去是结构性的，不是纪律：`convert.go` 的 `toWireMessages` **逐字段构造** wire 结构体，没提到的字段就没有到达 API 的路径。`TestDetailsNeverReachTheWire` 的意义是哪天有人把逐字段构造改成整体 marshal 时立刻叫出来。

pi 的做法是两类消息（富的进存储、瘦的上协议，边界处降级）。那更干净但要把第二个类型穿过 agent / session / web 三层，而用户可见的结果是一样的：刷新后 diff 还在。**如果以后还要往消息上挂第二个只给界面的东西（时间戳、耗时、per-message usage），就该停下来做那层分层，不要继续加字段。**

时间戳不需要这套：`session.Record.Time` 本来就逐条落盘。

代价实测过：小会话里 details 约占会话文件的 22%，其中 `patch` 字段（给 `git apply` 的那份 unified diff，`ChangesView.vue` 拿它渲染 `SideBySideDiff`）占 7%。没有为它单独做处理，因为要按工具区分该存什么就得让 `agent` 知道 details 的具体形状，而它现在把 details 当不透明的 `any` —— 为一个字段破坏这层不值。如果会话文件大小哪天成了问题，正确的做法是给 details 加一个 `Persisted() any` 之类的接口，让形状自己决定存什么。

### 执行模式：与 pi 不同，是有意的

| 工具 | 模式 | 理由 |
|---|---|---|
| `read` | 并行 **+ per-path 锁** | 并行的收益几乎全在这里：一轮读 3~8 个文件是最常见的模式，纯 I/O。锁是后加的，理由见下面那条更正 |
| `write` / `edit` | 并行 + per-path 锁 | 写是毫秒级，锁负责正确性 |
| `bash` | **串行** | 常见命令已内部并行（`go build`/`go test`/`make -j`），再并行只抢 build cache 锁；两条命令写同一输出文件会损坏且无从检测；而且一批 bash 逐个审批时，看过上一条的结果再批下一条才有意义 |
| `browser` | **串行** | 一串动作作用在同一个页面上：两个并发动作的先后顺序没有定义，而页面状态只有一份（`browser.go:134`）。sequential 工具因此是两个，不再只有 bash |

查过 `pi-mono/packages/agent/src/harness/tools/`：**pi 的四个内置工具都没声明 `executionMode: "sequential"`**，它全部并行包括 bash，只靠 `withFileMutationQueue` 保护写入。那对 pi 成立（TUI、成熟、用户在旁边看着），对要加闸门的 pogo 不成立。

`ExecutionMode()` 放在 `Tool` 接口里（不是可选接口），这样加新工具时**必须回答**这个问题。

**批次规则从「照 pi」改成了分段，两条旧论断都被自己的实现推翻过。** 原文是：一批里只要有一个 sequential，整批串行——这顺便防住「read 和改同一文件的 bash 同批」的竞态。

- **「顺便防住」防得没那么宽。** 那句话对 bash 成立，对 `write` / `edit` **从来不成立**——它们是并行的，而 per-path 锁当时只加在写的一侧。于是同批的 `read` 撞上 `write` 是敞开的：`os.WriteFile` 先 truncate 再写，读方落进那个窗口拿到的是一个**合法的短文件**，不报错。把锁从 `read` 里去掉实测，第 4~12 次读就能从一个 32000 字节的文件读回 **0 字节**。现在 `read` 走同一把锁（`tools/read.go`），测试是 `TestReadNeverObservesAHalfWrittenFile`。**bash 仍然不在锁的覆盖内**——它的写是任意 shell 命令，从不经过那把锁，所以 read-vs-bash 全靠 bash 独占一段。
- **「整批串行」太粗。** 一轮读三个文件加跑一次 build 是四个调用，只有一个不能重叠，旧规则让四个全部排队——那三个 read 是为跟自己无关的理由排的队。现在 `agent.segments` 把批次切成**连续同模式的段**：sequential 调用独占一段（所以它仍然不与任何东西重叠，也仍然是前后调用之间的屏障），parallel 调用照旧成段并发。**承诺的内容一个字没变**——「这个调用独自运行」，而不是「它的兄弟一个一个来」。
- **闸门一行没动，这是分段能落地的前提。** 两种派发形状原样保留：并行段仍然「整段按 `tool_use` 顺序逐个 review，然后并发 execute」，串行段仍然「review-then-run 交替」，所以「看过第一条命令的输出再批准第二条」对 bash 依然成立。审批预算仍然是**每批一个**（不按段重置），且仍然只作用于并行段——串行段一旦按预算拒绝，被拒的恰好是那条「要不要放行取决于上一条输出」的命令，那是拿等待换错答案。
- 单独一个 parallel 调用被标成串行段（`agent/loop.go:880-882`）：走并发路径一无所得，还会白白把它塞进审批预算。这也让「一个调用的批次」与改动前逐字节相同。

守着新规则的测试是 `TestSequentialCallRunsAloneAndParallelSiblingsStillOverlap`，**两条断言缺一不可**：只断言「sequential 不与兄弟重叠」，那把分段整个回退也能变绿；只断言「parallel 仍然重叠」，那删掉 bash 的 `Sequential` 也能变绿。实测回退成整批串行后，第二条报 `max concurrent = 1`。

### 一个批次分两个阶段：review 然后 execute

`runToolCall` 拆成了 `review()`（决定要不要跑、用什么参数跑；不发事件、不碰文件系统）和 `runToolCall()`（发 `tool_start`、执行、发 `tool_end`）。

**并行批次先把整批逐个 review 完，再并行 execute。** 闸门是一个人，同时问三个问题就得到三个顺序不定的问题；串行 review 让卡按 `tool_use` 的顺序出现，而执行照旧重叠。这也是 pi 的形状（`executeToolCallsParallel` 把 `prepareToolCall` 放在 for 循环里 `await`，只并行 execute）。

**顺序批次仍然逐个 review-then-run**，刻意不改：看过第一条命令的结果再批准第二条，比闭着眼批准两条好。

两个必须一起成立的性质：`tool_start` 携带的是**真正执行的参数**（所以它在 review 之后发，闸门改写过的参数才对得上），以及**每个 `tool_use` 一定拿到一个结果**（哪怕批次中途被取消，否则下一次请求会因为出现无主的 tool_use 被 API 拒绝，会话变得不可恢复）。

守着这条的测试有**三条断言，缺一不可**，而这正是它值得记下来的地方：没有并发检查，抓不到原来的问题；**只有**并发检查的话，光加一把锁就能通过，而审批顺序仍然是乱的；没有「工具仍然并发执行」那一条，把整批改成串行也能让测试变绿 —— 那是把功能删掉来修它的接缝。去掉修复后实测顺序是 `call2 call0 call1`。

> 顺带一条关于审查本身的：最初的审查结论说 pi 的闸门也是并发的，**那一半是错的** —— pi 的 `executeToolCallsParallel` 确实先在 for 循环里串行 `await prepareToolCall`（权限检查在其中），只并行 execute。对着源码重核之后这个方案才对。

**串行 review 让等待变成可加的，所以这个阶段有预算**（`Config.ReviewBudget`，web 侧取 `RunTimeout / 2`，`web/manager.go:791`）。没有它的话，7 个未应答的调用 × 5 分钟 gate 超时就超过 30 分钟的 run 超时，于是 run 自己的 deadline 把整轮杀掉：每个调用都变成 aborted，这一轮的工作全丢，而且没有东西解释原因。预算是**整批一个、不按段重置**；用尽后，还没审到的调用不送闸门，直接以一条拒绝文本作为工具结果落账（`settledCall`，isError，`loop.go:788-793`）——文本明说「该批次在等待同批先前调用的审批上花光了整个预算，重新发起它，或让人早点批」，模型读得懂、可以照做，run 活着。**第一个调用永远会被问**（`i > 0`）—— 一个能把所有调用都不经审查就拒掉的预算等于坏掉的闸门。

由此带来一个客户端约束：**「全部批准」必须做成随到随答。** 第二张卡在第一张被解决之前根本不存在，预先 POST 的决定会被静默忽略然后各自等满超时。

**也是这条审批粒度，否决了 Codex 形状的 `commands[]` 数组。** OpenAI 的 shell tool 是「`commands` 数组 + 逐条 outcome（exit code、输出）」，单看形状确实比一条命令串好——但闸门是**按工具调用**问的：一次调用只能问一次，N 条命令装进一次调用就是一次全批准，而「看过第一条命令的输出再批准第二条」是这一节写明的决定，也是分段规则刻意保住的那条。想在一次调用里逐条审批，得让工具回调闸门——那是一条新的管线，还要重做流式与 UI 的「一次调用一个结果」模型。另外两笔账：`AllowCommand` 是**精确字符串**匹配，数组化之后「总是允许 `go build ./...`」永不命中 `["go build ./...", "go test ./..."]`；`BashDetails` 是单值（一个 `command`、一个 `exit_code`），而它恰好是最常被渲染的结果组件，read 的 `paths[]` 能靠兜底组件降级，bash 这里降级的是主路。**OpenAI 没有审批闸门这个约束（沙箱代替了审批），抄形状不能只抄形状。** 链式 `&&` 因此保留，它的四条已知代价——exit 归因丢失（`ExitCode` 只有一个值）、尾部截断偏向后面的命令（先失败的那条最容易被切走）、审批粒度变粗（一次批准 = 批准整条链）、短路语义要模型自己拿准——写在这里，免得只记好处。不受影响的一条：链式不会绕过 subagent 的 git 守卫，`Guard.shellWords` 已按 `; | & ( ) \`` 切词。

### 增量输出：一个可选接口，而不是改 `Tool`

`bash` 跑 `go test` 一分钟，以前界面上只有一个转圈，没法区分「测试慢」和「卡死了」。

机制是 `tools.StreamingTool`，**可选接口**：

```go
type StreamingTool interface {
    Tool
    ExecuteStreaming(ctx context.Context, args json.RawMessage, onPartial func(Partial)) (Result, error)
}
```

**为什么不像 `ExecutionMode` 那样放进 `Tool`。** 那一条在接口里是因为搞错会造成 data race，所以每个工具都必须回答；「要不要流式」不是这种性质的问题 —— 不流式只是不好看，而内建十个工具里八个的输出本来就是最后一次性产生的（实现是 `bash` 与 `subagent` 两个）。放进 `Tool` 会让它们全都多带一个自己忽略的参数。代价是发现性：新加一个耗时工具的人得知道这个接口存在，所以 `Tool` 的注释里指了过去。

实现是 `Bash.ExecuteStreaming` 与 `Subagent.ExecuteStreaming`（`tools/subagent.go:599-602`）两个，各自的 `Execute` 都传 nil observer 代理过去 —— 每个工具只有一条代码路径要正确。

**合流策略：首次写入立刻发，之后按 100ms 或 4KB 合并。** 两个条件缺一不可：只按大小的话，每几秒打一行的命令会一直攒着；只按时间的话，疯狂输出的命令会把每几个字节都变成一个事件。

> 这里踩过一个自己造的坑：`lastFlush` 初始化成 `time.Now()`，于是**第一个片段要等第二次写入才发出去**。`echo start; sleep 4; echo end` 会整整四秒什么都不显示，恰好是这个功能最该管的情形。改成零值让首写必然触发。**单测全绿，只有带时间戳的真实运行才看得出来。**

**三层上限，理由各不相同**，不要合并成一个：

| 上限 | 值 | 为什么 |
|---|---|---|
| `partialBudget` | 256KB | 每个片段都会扇出给所有连接的客户端，这是**传输**预算 |
| `maxPendingOutput` | 32KB | live 副本进每一份快照，这是**重连成本**预算 |
| 结果截断（`truncate.go`） | 2000 行 / 50KB | 进对话，这是**上下文**预算；被砍掉的部分落盘，见 §5.3.1 |

超出 `partialBudget` 后会插一句「live output paused…」再停 —— 流突然静止然后跳到最终结果看着像 bug，得把这个断口说清楚。

**`tool_partial` 不进重放日志**，和 token delta 同一条规则，否则内存会与命令打印了多少成正比。它折进 `PendingTool.Output`，这样中途连上来的客户端**在快照里**看得到 —— 那是它唯一的来源，因为没有东西可重放。

**两个界面各自的取舍。** CLI 只在**单个工具在飞**时流式打印：两个命令写同一个终端会交织成没法读的东西，而这里没有 pane 可以分开放；跳过完全没有代价，因为结果照样会随 `tool_end` 到。结束时打一行 `[exit 0, 5.0s, 15 lines]` 而**不是**把已经滚过去的输出再打一遍。Web 侧在 running 调用上渲染终端样式块，`tool_end` 之后被 settled 结果取代，所以同一份输出不会画两次；跟随滚动，但用户往上翻之后不再抢回底部。

### 参数校验：在闸门之前

`review()` 在拿到工具之后、问闸门之前调 `tools.ValidateArgs`。顺序是有意的：一个缺字段的调用无论人怎么点都跑不起来，拿它去占用别人的注意力是浪费闸门唯一在花的东西。

校验必填字段、类型、以及数组元素（按下标定位，`edits[1].newText` 而不是「某个 edit 不对」）。**未知字段刻意容忍** —— 模型经常加些无害的额外键，为此废掉一轮不值得。错误信息里附上完整字段清单，模型不用再回头看一遍 schema 就能自己改对。

它替换掉的是这种东西：

```
json: cannot unmarshal number into Go struct field readArgs.path of type string
```

那句话讲的是 Go 的内部结构，模型没法据此行动；而**字段直接缺失时它连这句都没有** —— 参数以零值到达，工具在后面因为一个看起来不相关的原因失败。

`ArgsValidator` 是给工具的 opt-out 接口，现在有三个实现，各自接住一条 schema 表达不了的规则：`edit` 有意接受 schema 没描述的扁平 `oldText`/`newText` 形式，严格校验会在到 `Execute` 之前就把它拒掉；`read` 查「`path` 与 `paths[]` 恰好一个」（`read.go:91`）；`todo` 查「至多一个任务是 in_progress」（`todo.go:142`）。**宽容要写在想要它的那个工具里**，而不是在所有人依赖的检查上开一个洞。

> **诚实的实测结论：kimi 和智谱都在自己那一侧做 schema 校验，所以「缺必填字段」这条路径通过这两家基本走不到。** 反复要求 k3 只发 `{"file_path": ...}`，它每次都硬加上 `path`；glm-5.2 明说「我的 function-calling 层强制 schema，`file_path` 在到达工具之前就被过滤了」。
>
> 留着它的理由不是日常救场，而是它**不依赖 provider 的行为**（换一个不校验的 endpoint、代理或自建服务就有用），加上三件与 provider 无关的收益：校验在闸门之前、`edit` 的宽容第一次变成显式且有测试、`edits[0].oldText` 为空能在 read-modify-write 之前被拦住。

### per-path 锁

**包级全局**，不是 per-registry：Web 多会话时两个会话编辑同一文件也必须串行。key 是 **canonical path**（`EvalSymlinks` + 保留不存在的尾部），否则 `./a.go` 和 `a.go` 会拿到两把锁，锁就是装饰品。带引用计数，用完从 map 删除。

走这把锁的是 `write` / `edit` / `read` 三件（`read.go:198`，单文件与 `paths[]` 两条路径都在锁内读）——read 在锁内的理由见执行模式那条更正：撕裂缝实测存在，不是理论。「`read` 不需要锁」是本文先前的写法，已被那次实测推翻。`ls` / `find` / `grep` 不取锁。

### 路径守卫

`read` / `write` / `edit` 被限制在工作目录内，`../` 逃逸会被拒。`canonical()` 向上找到最近的存在的祖先解析符号链接，再把不存在的尾部原样接回——这样既拦住「root 内的符号链接指向外部」，又允许写入尚不存在的子目录。

> 这里修过一个 bug：原来直接对 `filepath.Dir(abs)` 调 `EvalSymlinks`，父目录不存在时失败 → canonical 化被跳过 → 而 macOS 上 `/var` 是符号链接 → 前缀比对对不上 → **合法路径被误拒**。`write` 明明有 `MkdirAll` 却写不进新建的子目录。测试抓出来的，手测跑不到。

**`bash` 没有路径限制**（它就是个 shell），但有 120 秒默认超时和输出截断。pi 默认不超时，无人值守的服务器场景需要天花板。

每次调用都是一个新的 `bash -c` 进程：**`cd`、环境变量、venv 都不跨调用持久**——模型的先验大概率相反（Anthropic 官方 bash tool 是持久 session），所以这条事实写进了 `bash` 自己的 Description 而不是 system prompt（只读 subagent 没有 bash，全局说这话是假话也是每轮都付的税）。同目录的活要么链进一条命令（`cd x && ...`），要么用 **`workdir` 参数**（形状抄 Codex）。`workdir` 带来两笔当初没算到的账：它必须过 subagent 守卫（`Guard.CheckDir`）——守卫拦 `cd` 靠解析命令文本，而 `workdir` 从不出现在那段文本里，不补检查就是给子代理开了一个长得像普通参数的越界出口；它还必须进「总是允许这条命令」的键（`web.grantKeyOf` 现在是 `命令 + \x00 + workdir`，两种形式互不覆盖）——否则当初那一次点击就被悄悄提升成「在 agent 说出的任何目录里」。它只校验「存在且是目录」而不走路径守卫：bash 从来没有路径限制可以加宽，加一道读起来像边界但不是边界的限制比没有更糟。链式命令另有一条结构性代价：**一条调用共享一份尾部截断预算**，先跑的输出先被切——`go build && go test` 里最该看的恰好是先失败的那条。

### 取消要杀整个进程组

`exec.CommandContext` 默认只信号直接子进程，所以 `bash -c "(sleep 60; ...) & wait"` 里的子 shell 会活下来并被 init 收养。**对 coding agent 这是常态而非边缘情况**：取消一轮的目的就是停掉它启动的 `go test`。

做法：`SysProcAttr{Setpgid: true}` + `cmd.Cancel` 里 `syscall.Kill(-pgid, SIGKILL)` + `WaitDelay`。平台相关的两个文件 `procgroup_unix.go` / `procgroup_other.go`。

`TestCancelKillsTheWholeProcessGroup` 守着这条，**且验证过去掉修复后该测试确实失败**。

### edit 的两个细节

- **接受扁平的 `oldText`/`newText`**：模型经常无视 schema 直接发单条形式，接受它比浪费一轮报错划算
- **BOM 处理**：Windows 编写的文件带 `\ufeff` 时，模型针对首行的 oldText 永远匹配不上，因为它无法知道有个隐形前缀

### read 的 `paths[]`：工具侧批量的第一个落地

模型侧批量（一轮多个 `tool_use`）框架只能引导；工具侧批量（一次调用做更多事）框架说了算。`read` 因此同时接受 `path` 与 `paths[]`，二选一（`required` 留空，「二选一」在 `ValidateArgs` 里查——能在 schema 里表达的只有 `oneOf`，而这个项目已经被 provider 的校验器咬过一次，宁可清楚地拒绝，不要聪明地描述）。几个决定：

- **预算均分，不是每个文件一份。** 五个文件各拿满额度就是五倍上限，而这个上限的存在意义就是防止一个 tool_result 吃掉上下文窗口。分段格式用 `head`/`tail` 的多文件约定 `==> path <==`，重复路径去重。
- **一个路径失败不让整次调用失败**——一个文件不存在就浪费掉这次调用本来要省的那次往返；全部失败才返回 error。
- **Details 不是 `ReadDetails` 形状**，多文件结果带自己的 `ReadManyDetails`：前端按**工具记录的字节区间**（`BodyOffset`/`BodyLength`，由写文本的同一段代码算出）切分各文件内容，不解析拼接文本——一个文件的内容里若出现 `==> other.go <==` 这样一行，解析会把它的片段挂到另一个文件名下，那是一个查看器唯一不能有的失败。

诚实的边界：这仍然依赖模型愿意填数组，只是不再依赖它一轮发多个 `tool_use`——后者的门槛在 provider 与模型双方，前者只需要标准的 JSON 生成能力。

### `ls` / `find` / `grep` 为什么是独立工具而不是交给 bash

pi 的 harness 也只有四个工具，但它的 coding-agent 层补了 `find`/`grep`/`ls`。pogo 三个都补了，理由不是省事而是**闸门**：`policy.go` 在 standard 模式下审 bash，所以「列个目录」「在仓库里找 X」这类纯读操作会弹审批卡。审批卡弹得越频繁，它就越接近一个必须点掉的弹窗，而 bash 那张卡是唯一值得中断的。

三个都是 `Parallel`，都吃同一组**只读额外根**（所以 skill bundle 也能搜），在 `reviewsLocked` 里与 `read` 同类不审。

| 工具 | 行为 | 上限 |
|---|---|---|
| `ls` | 一级深度、按名字不分大小写排序、目录带 `/`、含点文件 | 500 条 / 50KB |
| `find` | glob 匹配：不含 `/` 时匹配文件名，含 `/` 时匹配相对搜索根的路径 | 200 条 / 50KB |
| `grep` | Go 正则，输出 `path:line:text`；`include` 按文件名过滤 | 100 条匹配 / 50KB |

几个刻意的选择：

**只剪掉 `.git` 和 `node_modules`，不剪掉所有点目录。** 这两个是让遍历失控的，而 `.github/workflows`、`.pogo/skills` 里是真需要搜的东西。**不支持 `.gitignore`** —— 那要一个解析器，而且会让「故意去找一个构建产物」变得莫名其妙地搜不到。

**遍历本身有上限**（`maxWalkEntries`，20 万项）。只限制结果数不够：一次什么都没匹配上的搜索照样要走完整棵树，在文件系统根上跑一次 `find` 会几分钟没有任何输出。

**`grep` 跳过二进制文件**（前 1KB 出现 NUL 字节，和 grep 同一个启发式），并且**单行截断到 400 个 rune**（按 rune 不按字节，否则会把多字节字符切成两半）—— 一个压缩后的 bundle 就是一行几百 KB，一次匹配不该塞满上下文。

**非法 pattern 直接报错，而不是返回「无匹配」。** 后者会被读成「这个文件不存在」，把模型送到错误的方向去。

**搜不到也报告扫了多少项**：「扫描 3 项后无匹配」和「扫描 20 万项后无匹配」是两件不同的事。

## 5.3.1 截断掉的输出去哪了（`spill.go`）

`truncate.go` 回答「结果的上限是多少」，这一节回答**被砍掉的那部分去哪了**。这两个问题以前是分开的，而分开是错的：截断在一个共享函数里，「丢失的内容怎么办」却由每个调用方各自决定，于是同一个问题有了五个答案，其中三个是错的。

改完之后的规则一句话：**结果上限 2000 行 / 50KB，撞上限时，要么源还能再取，要么全文在磁盘上。**

### 两类工具，两个正确答案

| | 工具 | 撞上限时 |
|---|---|---|
| **源仍可寻址** | `read` / `ls` / `find` / `grep` | 不落盘。read 提示 `Use offset=N`，grep/find 收窄 pattern，ls 换子目录 |
| **源已经消失** | `bash` / synth / `browser` / `subagent` | 落盘。命令已经跑过了，砍掉的部分只能重跑一次才能再拿到 |

第一类**刻意不落盘**，三条理由都写在 `tools.New` 的注释里：每次大读都写一个文件是纯成本；文件是副本，原文被改了它就开始说谎；而 spill 文件本身是**用这四个工具读回来的**，给它们落盘就造出一条 truncate → spill → read → truncate 的级联，底下什么都没有。DeepAgents 豁免文件工具是同一个结论，这里是从 pogo 自己的事实推出来的。

第二类以前只有 `bash` 做对了。它从第一天就 spill 到 `os.CreateTemp`，理由完全正确却只惠及它自己：

- **synth（`tools.json` 声明的工具）是最严重的一个。** `renderOutput` 截断后什么都不做，所以每个 manifest 工具的行为都是「截断即丢」。malagent 的 `mal_tags` 是 CAPA 全量扫描、`mal_search` 是数据库查询——被砍掉的部分要重跑整个扫描、并且赌不同参数会吐得更少才能拿回来。
- **`browser` 根本没有上限。** `formatBrowserReply` 把页面状态原样写进结果，依据是一句「pane 已经算过预算了」。pane 确实算过——在浏览器里、用 TypeScript、普通快照 24,000 字符、full 快照 **120,000** 字符。那是「一块屏幕能显示多少」的预算，由不在这个进程里的代码设定，可以在没人察觉的情况下改；而 120,000 字符是本包对其他一切工具上限的两倍多。所以给它加界不是新政策，是把既有政策补给一个**意外豁免**的工具。
- **`subagent` 指向的是 transcript**，而那是 JSONL、要解析才能找到答案，并且住在 session 目录——一个设了 `POGO_READ_ROOTS` 的部署不会把它放进可读根，指针解析成一句拒绝。

### 落盘目录必须是可读根，而这是结构保证不是部署纪律

这是整个机制的关键，也是它以前**在真实部署下是坏的**的原因。

note 递给模型一个路径并让它去 read；`resolve()` 拒绝可读根之外的一切；而 `os.TempDir()` 恰好是唯一保证不在白名单里的地方。malagent 的白名单是 cwd `/home/agent` + skill 根 + `POGO_READ_ROOTS=/data:/opt/capa:/opt/ghidra:/opt/yarGen`——`/tmp` 一个都不沾。也就是说 bash 的 spill 指针**在那个部署里今天就是死链**，而 SKILL.md 还在教模型去那个文件里 grep。

所以目录不由部署方声明，由 `tools.New` 负责：它算可读根，也就顺手把 spill 目录并进去（`Options.SpillDir`，空则 `DefaultSpillDir()` = `~/.pogo/spill`）。两件事在同一个函数里决定，部署方就没有办法把它配错。

三个实现细节值得记：

- **只进 `readRoots`，不进 `writeRoots`。** 与 skill 目录同一条不对称：模型读命令打印了什么，没有任何理由让它改写这份记录。
- **`New` 不 `MkdirAll`。** 构造一个 registry 不该产生文件系统副作用——否则每个建 registry 的测试都会在开发者 home 下造目录（这是实测踩到的）。目录由 `Spill` 每次调用时按需创建，而根**绕过 `CanonicalRoots` 的 `os.Stat` 门**直接追加：那道门会丢弃还不存在的目录，而被丢弃的 spill 根会把保证一起带走。`canonical()` 本来就容忍不存在的尾部——那正是 `write`/`edit` 对将要创建的文件依赖的同一个性质。
- **`POGO_SPILL_DIR`**，形状同 `POGO_SESSION_DIR` / `POGO_MEMORY_DIR` / `POGO_WORKTREE_DIR`。子代理经 `os.Environ()` 继承，所以 `spawn` 一行都不用改，父子默认落在同一个目录——shared 隔离下这是个好处，父代理能直接读子代理落的盘。

### 文件名按内容寻址，这一个决定顺手解决三件事

`{tool}-{sha256(raw)[:6]}.log`。不是 `os.CreateTemp` 的随机名，代价一样，收益三份：

1. **修掉停滞检测。** `agent.hashToolResults` 哈希结果文本，而 note 带着路径在文本里。随机文件名 → 两次字节相同的运行哈希不同 → `detectStagnation` 永远不触发。这正是那个函数注释里专门警告过的 tool_use id 那个 bug，从另一个门回来了：**被截断的 bash 结果在这次改动之前从来不可能触发停滞检测。**
2. **prompt 前缀稳定。** 同 §9.4 占位符必须是纯函数的同一条约束。
3. **自动去重。** 一个反复跑同一条大命令的 agent 在目录里留一个文件，而不是每次尝试一个。

写入是 create-temp-then-rename（照 `config.Save`，去掉它那个只为单文件 bind mount 存在的原地回落）。同内容两个 writer 竞争同一个终名，两边都赢——因为写的东西一模一样。

### note 的措辞：路径不够，必须带回读指令

沿用 bash 原有形状加一句指令：

```
[Showing lines 1913-3912 of 3912 (50.0KB limit). Full output: /Users/x/.pogo/spill/bash-3f2a9c1d.log — read it with offset/limit, or grep it for what you need.]
```

指令不是装饰。一个只有路径没有指令的指针会被当成基础设施噪音读过去，内容就此静默不可见（Claude Code 有这个事故的公开记录）。措辞跟着工具们已经在说的方言——read 的截断结尾就是 `Use offset=N to continue`——所以这不是一个要重新学的新概念。

**写失败时绝不报路径。** 保留 bash 原有的严格纪律：`Write` 和 `Close` 都检查（缓冲刷新是在 `Close` 失败的，忽略它正是半个文件冒充完整文件的路径），失败就删掉临时文件并回落到「The rest could not be saved」。丢掉 spill 是**降级**的结果；宣布了截断却给一个指向空文件的路径是**错误**的结果——模型会从一个被告知是全文的片段里读出一个自信的答案。

### 顺手修掉的一个计量 bug

synth 的 stdout 和 stderr 以前**各自独立**过一遍 `TruncateTail`，所以一个两头都写的工具给自己买了两份预算——结果是本包上限的两倍。现在先合并再截断，尾部截断也就自然做对了事：stderr 在末尾，失败工具最该展示的那一半不需要特例就能留下。

## 5.4 skills

pi 的机制照搬，一处必须改。

### 磁盘格式与发现

一个 skill 是一个含 `SKILL.md` 的目录：frontmatter 里 `name` 与 `description`，正文是自由 markdown，同目录下的 `scripts/`、`references/` 用相对路径引用。

扫描位置：`~/.pogo/skills`（默认开）、`<cwd>/.pogo/skills`（**默认关**，`-project-skills` 开）、`-skill <path>`（可重复，`-no-skills` 也不影响它）。

规则照 pi：目录里出现 `SKILL.md` 就是 skill 根、**不再往下递归**；根层裸 `.md` 也算一个 skill；跳过 `.` 开头与 `node_modules`；按 realpath 去重；**重名先到先得，且 user 先于 project 扫描**，所以 user 级盖 project 级。`description` 缺失是唯一的硬失败，其余问题只 warn。

### 注入：渐进式披露

只有 name / description / location 进 system prompt 的 `<available_skills>`，正文留在磁盘上由模型用 `read` 取。XML 形状与前两句指令是 pi 的原文，好让同一个 skill 目录在两个 harness 下行为一致。

### 与 pi 的两处必要偏离

**一、read 需要只读的额外根。** pi 的 `path-utils.ts` 完全没有 cwd 限制，所以它的 `read` 天然能读到 `~/.pi/agent/skills/...`。pogo 有路径守卫，直译 pi 的方案会得到一个每一步都失败的功能：模型看到 location、`read` 它、拿到「outside the working directory」。更糟的是 `bash` 不受限制，模型会自己发现 `cat` 这条绕路，于是所有 skill 加载都绕过了 read 的截断保护和渲染管线。

所以 `resolve()` 增加了一组额外根，**`read` / `ls` / `find` / `grep` 四件都传**（`tools/tool.go:184-188`），`write` / `edit` 一个都不传 —— skill 目录可读可搜不可写，模型改不了自己的指令。根是**每个 skill 各自的目录**，不是扫描根，取最紧的那个集合。

**二、项目级 skills 默认关。** pi 加载 `.pi/skills` 的前提是 project trusted，pogo 没有信任机制。skill 是一个能改写 system prompt 的磁盘文件，clone 一个仓库进去跑第一轮就吃到别人的指令，是文件形态的 prompt injection。`-skills` 在项目目录存在但未加载时会明确提示。

**三、内置 skill 编译进二进制，走物化而不是虚拟路径。** 框架自身的能力契约（第一个是 `web-drawing`：web UI 渲染 ` ```mermaid ` / ` ```echarts ` 围栏的输出标准）以 skill 形态存在，`go:embed skills/builtin` 进二进制，web 启动时物化到系统缓存目录再走磁盘加载。物化是为了让整条管线（`read` 取正文、`/skill:` 重读、只读根）对内置 skill 零改动；换成 embed.FS 虚拟路径意味着给每个消费者教一遍「内存里的 skill」。加载顺序排在 user、project、显式路径**之后**，`-no-skills` 一并关闭——它是默认而非显式请求；部署覆盖内置的方式是写一个同名 skill，而不是重编二进制。配套的 `uiCapabilitiesSection`（`web/uiprompt.go`）常驻 web 模式的 system prompt，一到两行声明围栏渲染能力并指向该 skill；不放进基础 prompt 是因为 TUI 不渲染图，对终端会话这行是谎言。

### 显式调用

`/skill:name args` 在 `-p` 和 Web 两条路径上都展开成一条 user 消息（REPL 已移除），格式与 pi 逐字相同：

```
<skill name="..." location="...">
References are relative to <dir>.

<body>
</skill>

<args>
```

Web 侧的展开在服务端（`handleMessages`），因为文件在那边才读得到。`disable-model-invocation: true` 的 skill 不进 prompt，只能这样调。

### 没有 `skill_hit` 事件

原计划要加一个。实际做法是**给 read 调用打徽标**：加载一个 skill 就是一次 read，已经在时间线上了，再发一个事件是把同一个事实说两遍。

匹配用的是 `tool_use.Input` 里的 `path`，**不是 `ToolDetails`**。当初的理由是「参数进 JSONL、details 不进（G1），按 details 匹配刷新后徽标就没了」——**这个前提已经过时**：`llm.Block.Details` 现在会持久化进 JSONL 供 UI 回放（只是仍不进模型上下文，`llm/types.go:45-60`）。这条选择站在一个更简单的理由上（`skills/prompt.go:92-96`）：读的是哪个文件是调用本身的一部分，没有理由绕道工具的返回值去问。

### frontmatter 不引 YAML 依赖

手写解析 `key: value`、引号、`>` / `|` 块标量。避免为有限 frontmatter 语法引入完整 YAML 解析器是该模块的取舍，不代表全项目零依赖。代价要写明：**`metadata:` 这类嵌套 map 的子键被丢弃**，别名、flow mapping、多文档都不支持。缩进行一律跳过，这是唯一可能凭空造出字段的地方，测试里有专门的负例。

### 实测记录（真模型）

- 模型自己判断匹配 → `read` SKILL.md（徽标命中）→ 跑 bundle 里的脚本 → 按 skill 要求的格式输出。**只读根、渐进式披露、徽标三件事同时兑现。**
- `ls` + `read references/...` 在 skill 目录里并行执行成功。
- 两个诚实的观察：**第一版 SKILL.md 里「from the skill directory」把模型带偏了**，它 `cd` 进 skill 目录跑脚本、拿到 0 packages、然后自己纠正 —— 错误即信息这条链路是好的，但一轮就这么没了，改掉措辞后不再发生；**二级引用文件的加载比 SKILL.md 更不可靠**，`references/conventions.md` 在不点名要求时模型不会去读，于是它规定的格式没被遵守。渐进式披露越深越松，写 skill 时该把关键约定放进 SKILL.md 正文。

## 5.5 memory：同一个机制，反过来用一次

skills 交出「一个 prompt 字符串 + 一组只读根」。memory 交出的是同一对东西，只有一处不同：**根是可写的**。这不是巧合，是这一节唯一的设计动作 —— 一个机制用两次，读/写之分是全部差别。

### 为什么不是一个新工具

Anthropic 的 memory tool 是 `view` / `create` / `str_replace` / `insert` / `delete` / `rename`，也就是 `read` / `write` / `edit` 换了名字。他们自己的 harness 设计文章也把 skills、memory、programmatic tool calling 都说成 bash 与 text editor 两个原语的**组合**。

所以这里不加工具，只加一个**已有工具能到达的目录**。理由是可量化的：`prompt.go` 的注释写着 tool schema 约是 system prompt 的 5.8 倍，而 schema 在**每一轮**都付。一个复刻三个已有工具的新工具，会在每个会话的每一轮为此付钱。

顺带白拿四件事：不新增截断逻辑、写入自动进 `journal`（所以浏览器的「工作区改动」里看得到）、per-path 锁自动生效、`ValidateArgs` 自动生效。

### 读/写不对称是一个安全性质，所以是两个字段

`Options.ReadRoots` 给 read/ls/find/grep；`Options.WriteRoots` 给这四个**加上** write/edit。写根也必须可读 —— 读不回来的笔记不是笔记。

**刻意做成两个字段而不是「一个字段 + 一个布尔」**：后者意味着某个调用方可以传错一个 bool，就把 skill bundle 变成可写的。而「模型能改写自己的指令」等于没有指令。`New` 把一个字段发给四个工具、另一个发给六个，没有任何调用方能靠传参数把两者混起来。

**bash 两个都不给**，因为 bash 从来没有路径限制可以「加宽」。这句话必须写出来而不是留作隐含：**记忆对 bash 是不设防的**，而且有一条测试专门钉住这个事实，就是为了防止有人给 bash 加根之后以为记忆被保护了。

### 两层，项目层默认关

| 层 | 位置 | 默认 |
|---|---|---|
| user | `~/.pogo/memory`（`POGO_MEMORY_DIR` 可改） | **开** |
| project | `<cwd>/.pogo/memory` | **关**，`-project-memory` 开 |

项目层默认关是 project skills 那条决定的直接延伸：**一个能改变 agent 行为的文件不该随 `git clone` 到来**。而记忆比 skill 更糟一处 —— 它是以「你自己之前的结论」这个身份呈现给模型的，那是比「一份交给你的文档」更可信的声音。

user 层不需要这个 flag，因为 home 目录本来就和二进制同等可信（同 `config.FileName` 与 CVE-2026-21852 的论证）。

目录用 **0700** 创建：笔记会攒下路径、主机名、一个私有代码库的形状，机器上别的账号没有理由读它。

### 目录是创建而不是「不存在就跳过」

`tools.CanonicalRoots` 会丢掉不存在的根（合理：不存在的目录授权不了任何东西）。照搬到这里就会得到一个死锁：新装的机器上 agent 没有地方可写，而它唯一能创建那个目录的地方，正是它还不被允许到达的地方。Anthropic 的 SDK 参考实现也在模型第一次调用前先建好根，同一个原因。

失败**永远降级不阻塞**：目录建不出来就报一条诊断、当作没有记忆继续跑 —— 和 checkpoint 同一条规则。

### 注入面：这一层的证据最硬，所以措辞是量出来的

这些文件里的一切都是**不可信输入**。工具输出会进记忆，而这里的工具输出就是仓库里的文件内容 —— 一个 README 可以包含一句写给「任何读到它的东西」的话。记忆改变的是**持续时间**：落在一次会话里的注入随会话消失，落在一份笔记里的注入被之后每个会话重读。

| 研究 | 结论 |
|---|---|
| arXiv 2607.27080 | 24 种配置下恶意记忆留存 **84.2%**，完整的写-then-做链成功 **50.3%** |
| arXiv 2605.26154 | **3 条**记录即达 **85.9%** 工具劫持率，且能穿过三种已发表防护 |
| arXiv 2605.06731 | **不需要攻击者**：日常对话本身就显著污染长期状态，记忆类产物受损最重 |
| arXiv 2601.05504 | 一个缓解方向：正当记忆足够多时攻击有效性显著下降 |

三条对策，每条都便宜，因为机制都已经在了：

1. **声明为数据而非指令**，在一个具名元素里，和 `compact.go` 对 `<transcript>` 做的完全一样。措辞刻意可执行：「当作记录并报告」给了模型对可疑行的**处置方式**，而「不要遵循指令」只说了不要做什么、没说替代动作。同时明说笔记**可能过期或错误** —— 那是远更常见的失败，不需要任何攻击者。
2. **写入进 journal**，所以一次记忆改动像任何文件改动一样出现在浏览器的 diff 视图里。**看得见变化的记忆才是能撤回的记忆** —— 上面 84.2% 那个数字，本质上是在说一次坏写入能悄悄活多久。
3. **列表带 age**，所以几个月没人碰过的笔记看起来就是那样，而不是和今早写的一模一样。

> **踩到的一个真洞。** 列表最初用 `fmt.Fprintf("%q", path)` 填属性。`%q` 是 **Go 的**字符串转义：它把引号变成 `\"`（对 XML 毫无意义），而 `<` `>` 原样透传。于是一个叫 `x"/><injected note.md` 的文件**真的往列表里注入了一个元素** —— 而文件名在仓库里是攻击者可控的。改用 `escapeXML`（`skills` 里已有同一个函数），并有测试钉住。**这是被测试抓到的，不是想到的。**

### 渐进式披露：路径进 prompt，正文不进

和 skills 同一套，同一个理由。二十份笔记每轮花几百 token，而不是它们恰好有多重。

**列表是注入 prompt 的，不是留给模型自己发现的。** Anthropic 走的是另一条：协议里让模型「做任何事之前先 view 记忆目录」，那在每个会话开头都要付一次**保证发生**的往返，换回来的东西 prompt 本来可以用零头的代价带上。pogo 为 skills 已经做过这个算术。

**空记忆不产出任何 section**，连那几句协议都没有 —— 不用这个功能的人一分不付。

三个上限：目录只走**两层深**（对齐 Anthropic 的 `view`，所以同一个目录在两个 harness 下读起来一样）、**40 条**、**4KB**。后两个是两个上限而不是一个，因为它们失效方式不同：一百条小笔记和三条超长路径都能把这段撑大，而只有前者会被计数抓住。超限时报剩余条数并提示 `ls` —— **一个不说自己被截断的列表，会教模型「看不见的就不存在」**，而最先被砍掉的恰好是最旧的那些。

**列表必须逐字稳定**：它在缓存前缀里，所以 age 最细到「天」，且排序在 mtime 相同时按名字兜底。一个每轮都变的字段会每轮打掉缓存 —— 和 §9.4 占位符「不含时钟」是同一条约束。

### subagent 拿不到

按深度不授根，和 `todo` 同一个位置、同一条理由，几乎逐字：**子的笔记没有读者**。它活一次 run、10 分钟超时封顶，永远碰不到记忆唯一要解决的那个会话边界；剩下的只有「我对这个项目知道什么」多出第二个写者，而这个问题必须只有一个答案 —— 长期状态被量出来的退化方式就是日常使用，而两个互不可见的写者是它最快的版本。

**只读子也不给**，尽管它写不坏任何东西。风险不在于读，在于**被交给一组结论**：explore 子被问的是一个窄问题、答案应当来自代码，而它没写过、无法验证、也没要求过的笔记是它不需要的上下文。

### 已知缺口

- **写入没有大小上限。** 加了就意味着 `write` 必须知道哪些路径是记忆，而那正是 skills 小心避开的分层（循环和路径守卫都不知道 skills 存在）。暴露面由已有机制兜着：`read` 在 2000 行 / 50KB 截断，列表本身有上限，所以单个大文件和大量文件都撑不大 prompt。**记成缺口，不装作已解决。**
- **没有过期回收。** Anthropic 的安全清单把「定期删掉很久没访问的记忆文件」列为调用方责任，这里只做到「把 age 显示出来让人和模型看见」。真正的回收要一条策略，而策略需要先有真实使用数据 —— 和 §9.8 对 `Keep` / `ClearAtLeast` 的态度一致。
- **列表是 run 开始时的快照。** 模型本轮写的笔记要到下一个会话才出现在列表里。这是正确行为而非限制：它知道自己刚写了什么。但 system prompt 因此在会话中途不变，这正是缓存前缀需要的。

## 6. `diff` 包

pi 用的是 npm 的 `diff` 包，Go 标准库没有对等物。为保住 stdlib-only 自己实现了 **Myers O(ND) 行级 diff**：

- `diff.Lines(a, b)` → `[]Chunk{Equal|Delete|Insert}`，形状对齐 npm diff 的 parts
- `Display()` 给人看（行号 + 上下文折叠）、`Unified()` 给机器（`git apply` 能吃）、`Stat()` 给 `+3 -1` 徽标
- **先裁公共前后缀再跑 Myers**：3000 行文件改 1 行，实测只产出 1 增 1 删
- 差异区超 5000 行退化成整块替换，避免最坏情况爆内存

**验证方式值得沿用**：8 个 patch 交给真正的 `git apply` 执行并逐字节比对结果（`TestUnifiedPatchAppliesWithGit`）。这是能拿到的最强验证。

> 踩过的坑：Myers 回溯的起点写错了——`trace[d]` 存的是第 d 步**之前**的 V 向量，终点必须直接用 `(n, m)` 而不是从 trace 里读。第一次跑测试就 panic。

## 7. 重试与退避

参考实现取自 Stainless 生成的官方 OpenAI / Anthropic SDK（`requestconfig.go`），因为那套参数是对着真实服务调出来的。

| 项 | 取值 |
|---|---|
| 重试条件 | 连接错误、408、409、429、>=500 |
| 不重试 | 其余 4xx（400/401/403/404 是请求本身的问题，重试只白耗额度） |
| 服务端覆盖 | `x-should-retry: true/false` 优先于状态码 |
| 等待时长 | `Retry-After-Ms` → `Retry-After`（秒或 HTTP-date）→ 本地退避 |
| 本地退避 | `500ms × 2^n`，上限 8s，再**减去**最多 25% 抖动 |
| 默认次数 | 3 次重试，`-retries` 可调，`-1` 关闭 |

抖动是**减**而不是加，这样延迟永不超过上限；一批客户端同时撞 429 后不会又齐步走回来。

**通用 SDK 对流式调用做不对的一件事**，这是实现的重点：SDK 在 HTTP 层重试，但一旦有 delta 已经吐给上层（已经打到屏幕上），重放请求就会**重复输出**。所以 `Stream` 里有 `dirty` 标志，中途断流只在还没有任何输出时才允许重放。

另外两个细节：请求体是 `[]byte`，每次尝试重建 `bytes.Reader`（流式 body 不可重放，这正是 SDK 里 `req.GetBody == nil` 就放弃重试的原因）；退避期间 `select` 监听 `ctx.Done()`，撞上 `Retry-After: 30` 时 Ctrl-C 要立刻退出。

`OnRetry` 回调把重试情况报给界面，否则限流时看起来像卡死。

## 8. 会话

JSONL，记录形成**树**而不是列表，每条带 `{id, parentId}`：

```
c45d1bf3 <- root      meta
e9893029 <- c45d1bf3  user
1b3a5e73 <- e9893029  assistant
```

一条线性对话就是没有分支的树。保留 parent 链的意义是：「回到某轮重试」是读另一个叶子，而不是改写文件。`Fork(recordID)` 已实现但 CLI 从未暴露。

`List()` 流式扫描目录产出 `Info`（标题取首条 user 消息、消息数、model、skills、mtime），不加载完整历史。

### 损坏恢复

进程被 kill 时末行会残缺，所以这一层必须容错。三条规则：

| 情形 | 处理 |
|---|---|
| **末行**不完整 | 静默跳过。这是被 kill 的正常结果，后面没有东西，除了那条没写完的记录什么都没丢 |
| **中间行**不可解析 | 记入 `Diagnostics()`，CLI 打到 stderr、web 打到服务端控制台。这是真损坏，不能假装文件是健康的 |
| 父记录没保住 | `repairChain` 按文件顺序重接。**有分支的文件不猜**，只报告 |

追加前会**补上缺失的结尾换行**。少了这一步，新记录会被拼进残缺的那一行，于是刚写的好记录也一起损坏 —— 而那第二次损坏才是断开 parent 链、连带丢掉之前全部历史的原因。这条链实测过：健康 3 条 → 被 kill 后 2 条 → resume 追加两条后重开只剩 1 条。

> `session/` 包在修这个之前**一个测试文件都没有** —— 全项目唯一带持久化逻辑的包零覆盖。这不是巧合：没被测的地方就是缺陷所在的地方。

`repairChain` 拒绝在分支文件上动手，因为「文件顺序等于链顺序」只在线性文件里成立；缝错了会把两段对话拼到一起，而一份错的 transcript 比一份短的更糟。

### resume 时的元数据

`Meta{cwd, model, skills}` 在创建时写入，**resume 时会被读回来用**：

- `model`：`-model` 缺省时用会话记录的模型。所以先开会话、再建 client，顺序不能倒
- `cwd` / `skills`：变了就告警（stderr，`-p` 因此仍然可管道）。不阻止 —— 换个目录继续一个会话是合理需求，问题只在于它不该是静默的

这三个字段都会改变行为却不出现在任何消息里，所以没有它们就无法解释两个会话为什么不一样。**曾经有一段时间它们只写不读，等于白记**：写入做了，读取端完全不存在，于是 resume 一个换了 skill 目录的会话，system prompt 变了而没有比对、没有告警、连事后查的路径都没有 —— 当初记下它的那个理由，一条都没兑现。修法是把顺序倒过来：先开会话、读到 meta、再据此选模型。

**落盘粒度是 per-turn，经事件流增量落盘。** 曾经它是 per-run 的：CLI 的 `flush` 在一次 `a.Run()` 返回后调用，Web 的 `Session.finish` 在事件 channel 排空后调用，进程在 run 中途被 kill 就整轮丢失。原因是**运行中读 `agent.Messages()` 是 data race**：loop 的 goroutine 无锁追加，而 `a.mu` 只保护 steering。另一条修法（给 `Messages()` 加锁）会让 `a.mu` 开始保护 messages，于是 `Usage()` / `LastInput()` / `Delegated()` 一串标着「run 结束后才安全」的访问器全要重新论证 —— 那条路明显更大。

所以走的是事件流：loop 补发了 `EventToolResults`（工具结果那条聚合 user 消息，此前只被追加、没有任何事件带它），消费者用 `agent.TurnPersister` 把事件流落成 transcript。三个关键性质，各自有测试钉着：

- **单位是轮，不是消息。** 带 tool_use 的 assistant 消息被扣住，直到它的 results 一起落盘 —— 落盘的 tool_use 必须有配对的 tool_result，否则 resume 时 API 直接拒绝这份历史。这个等待是有界的：loop 自己保证每个 tool_use 都有结果（取消也不例外），而 abort 的响应干脆不带消息。
- **磁盘上永远是一个前缀。** 首个追加失败即闩锁，后续事件全部空转 —— 于是结尾的 `flush` / `finish` 从「唯一的写者」降级为「对账」：按计数补齐没落的尾巴，不会重复也不会缺序。
- **`EventToolResults` 不上 wire。** 不进 `wire.FromAgent` 的 loop 事件有两种（`wire/wire.go:134-137, 192-193` 的 default 分支）：`agent_start` 由消费者各自渲染更丰富的版本（web 的 `run_start`、JSON 模式的 Header），`tool_results` 是另一种——同样的结果浏览器已经从 `tool_end` 渲染过了，第二份是噪音。它的读者是两个进程内消费者（CLI 的 `consume`、Web 的 `Session.consume`），它们读的是 `agent.Event`，根本不经过 wire。

验收就是字面场景：`-p` 跑到第二轮时 `kill -9`，会话文件里有 prompt 和第一轮的完整往来，以 tool_result 结尾；`-resume` 加载后 API 照常接受，新 run 干净地续在后面。

---

## 9. 上下文管理

**这一章描述的是这个项目最大的一个子系统，而它曾经在本文里一个字都没有。** 那次遗漏值得记：`agent/contextedit.go` 有 600 多行、默认开启、会改变发给模型的每一个 prompt，而本文的「不做」清单里还写着「不做上下文压缩」。**默认开启的东西不写进设计文档，等于让下一个读者按一份错的模型去改它。**

分成三层，每一层解决的问题不同，不要合并：

| 层 | 机制 | 触发 | 花费 |
|---|---|---|---|
| 主动清理 | 旧的工具输出换成占位符 | prompt 过阈值 | 零模型调用 |
| 被动恢复 | provider 拒了之后强制清一次再重试 | 4xx 且判定为溢出 | 一次重试 |
| 摘要压缩 | 整段对话换成一份摘要 | 手动 `/compact`；显式 `-compact auto` 启用自动路径 | 一次模型调用 |

前两层自动，摘要压缩默认不自动。管理员或调用方显式启用 `-compact auto` 后可走运行间阈值、溢出恢复及熔断路径，见 [自动压缩](auto-compact.md)。下面保留早期采用手动默认值的理由；不能将“默认关闭”写成“没有自动实现”。

### 9.1 为什么是机械淘汰而不是摘要

**有对照实验支持，而且不是「省事」那种理由。** Lindenbauer 等人（*The Complexity Trap*，DL4C@NeurIPS'25，SWE-bench Verified 上五种模型配置）发现：单纯省略旧的观察结果，相对原始 agent 把成本砍半，同时**解决率与 LLM 摘要持平、有时略好**。而它在这里要便宜的每一个维度上都便宜：不多花一次模型调用、不产生一份「错了但没有测试抓得住」的摘要、行为可以被单测精确钉住。

**这个项目自己的数据也指向同一个方向。** 磁盘上 100 个真实会话里，7 个达到 5 万估算 token，而这 7 个**无一例外是 88%–99% 的工具侧**；工具字节里 read 占 89%，而 read/ls/find/grep/bash 合起来 94.7% 都能靠重新调用拿回来。摘要要解决的是「对话文本把上下文撑大」，那个形状在这 100 个会话里没有出现过。

### 9.2 这是一个视图，不是一次编辑

**transcript 永远不动。** 清理发生在为一次请求构造的副本上，这也是 Anthropic 划的同一条界（他们的策略跑在服务端，文档明确写客户端保留完整未修改的历史）。在这里它意味着：会话文件、`-resume`、web 的 diff 视图、以及任何事后审计，看到的都是原始输出，而模型看到的是占位符。

### 9.3 形状抄的是 `clear_tool_uses_20250919`

选它是因为**它是唯一一份有公开契约和默认值的设计**，而不是一篇博客：过阈值后从最旧的工具结果开始清、保留最近几条不动、**保留工具调用本身只清它的结果**、每条被清的换成占位符文本让模型知道发生了什么、以及一次清理必须释放得够多才值得它打掉的缓存。

四处按 pogo 的情况改了，每一处都有独立理由：

**一、`auto` 是「当前模型窗口的五分之四」，不是写死的 100,000。** 他们的默认值固定在 100,000，因为他们的模型窗口都在 200K 附近 —— 那个数恰好是一半。pogo 的窗口值完全来自用户文件的 `context_window`（内置目录时期是 262K 到 1M），照抄绝对值更没有立足点：让 1M 的模型在窗口的十分之一处就开始清，那不是同一个策略。切模型时阈值跟着走（`/model` 与浏览器的切换器都会重算）。常量在 `agent.AutoTriggerNumerator` / `AutoTriggerDenominator`，**导出的**，因为浏览器的占用条必须按同一个数着色（清理开着的时候占用会稳定在触发线下面一点，用固定百分比的色带会整场发黄）。

> **这份文档在这里写了一段时间的「一半」，而代码是五分之四。** 记下来是因为漂移的方向有信息量：那个分数**动过一次**，而动它的两个理由恰好就是本文别处已经写下的实测结果 —— 说明这里当时不是抄漏了，是同一份证据只更新了一半。

**为什么是五分之四而不是他们的一半。** 三个理由，两个是量出来的、一个是结构性的：

- **被比对的那个数偏高。** 对着两家自己的计数、25 个真实会话反推，ASCII 为主是 0.98、中文占比高的是 0.83（见 §9.6）。所以「估算 token 达到窗口一半」实际发生在真实占用的 42%–49% 处 —— 清理会在窗口有一半以上真正空着的时候就开始。
- **清理不免费。** 它重写 prompt 的一部分，缓存前缀因此失效；Kimi 的未命中约按命中价的十倍计，智谱约两倍。为没人需要的空间付这笔钱是纯损失，而且模型可能会去重读它本来还看得见的文件。
- **结构性的那条是后来才变的。** 选「一半」的时候，撞窗口是**致命且永久**的：provider 返 400，`retry.go` 不重试 4xx，而历史不会因此变小 —— 同一个会话之后每一句话都会以同样的方式失败。那时候「早一点」是对着一个悬崖买的保险。**那个悬崖现在没了**（§9.5 的 `forceClear`：被拒之后强制清一次并重试一次），所以触发线晚一点的代价从「一个死掉的会话」降成了「一次白花的调用」。

**留下的那五分之一是给三件事的**，这也是它为什么不是十分之九：Kimi 把 prompt + `max_tokens` 记在同一个上限里（错误原文就是「prompt tokens + max_tokens exceeds the model specification」），所以 `MaxTokens` 必须装得进这个余量；被比对的那个数一半实测一半估算（见 `Agent.promptTokens`），本来就不该信到几个百分点；而清理发生在**一次调用之前**，它放行的那一轮还会继续往历史里加工具输出。

每条目录的余量取决于用户文件里的 `context_window` / `max_tokens` 配比，`TestAutoTriggerLeavesRoomForEveryModelsOutput`（在**根包**，因为它是唯一同时 import 规则和目录的地方，`agent` 刻意不知道模型列表存在）钉着「每条余量都装得下 MaxTokens」这条。注意它跑的是**合成目录**（`contextedit_test.go:33,54` 的 `installScalingCatalog`：200K/8K 与 1M/32K 两条）——「目录里每一条余量都是 MaxTokens 的 3–12 倍」是内置目录时期的实测口径，目录来自用户文件之后，这个倍数只能逐部署回答，本文不再写死。它守的是「一个输出上限很大、窗口很小的新目录条目」——那正是这个余量唯一会被破坏的方式。另一条 `TestAutoTriggerScalesWithEachModelsWindow` 守的是这个分数没有被人换回一个常量：合成目录跨两种窗口大小，所以解析出来的触发线必须不全相等。

> **这里刻意没有 `DefaultContextEditTrigger` 常量，而且是删掉的。** 它曾经存在，注释写着「默认模型 200K 窗口的一半」—— 那个推导在 glm-5.2 的目录值被更正成 1M 之后就不成立了，于是那个常量指的是窗口的十分之一。它也从来没有调用方。删掉时在原地留了说明，因为「缺一个默认常量」最自然的修法就是加回来一个，而写死的阈值恰是这里要避免的东西。

**目录里没有的模型 `auto` 直接关掉**，不猜：没有窗口就没有可取分数的东西，而 run 无论如何都会跑完，沉默胜过一个编出来的阈值。

**二、失败的结果不清。** Anthropic 不做这个区分。从错误文本里自我纠正是这个项目的签名行为，整个时间线 UI 都建立在「把修复连到它修的那次失败」上；错误文本本来也很短，留着几乎不花钱。

**三、`write` / `edit` 的载荷参数也清。** 他们的 `clear_tool_inputs` 默认 false，理由是「调用发生过、针对什么」是模型判断要不要重来的依据。**那个理由对 `read`/`ls`/`find`/`grep`/`bash` 成立**（参数就是描述：路径、glob、命令行），**对 `write`/`edit` 不成立**：那里的大参数不是对调用的描述，它就是载荷，而且是全世界最确定还存在别处的那份载荷 —— 文件就在磁盘上。

这一条是量出来的，不是想出来的：100 个会话里有两个是 83% 和 96% 的工具**参数**，其中一个持有 42,328 估算 token 的 write 内容，而当时的清理能碰的是 0 —— 因为它只看结果。这两个会话既不是「工具输出大」也不是「对话长」，是成分报告当初没准备回答的第三种形状。

`edit` 的 `oldText` 一起清是有意的：一次**成功**的 edit 之后那段文本按定义已经不在文件里，永远匹配不上，留着等于专门保住这次调用里唯一已经失效的字符串。**失败**的 edit 反过来一律不动（上一条已经跳过错误结果），因为 `oldText` 恰是自我纠正唯一需要的东西。

结构一字不动：`edits` 仍是数组、每个元素仍是带两个字符串字段的对象、长度也在。两个理由 —— 改了形状的记录是一份假记录；而万一有 provider 校验历史 `tool_use` 参数，为几个字节赌这个不值。

**四、占位符结尾那句话按「重复这次调用要付什么」分四档。** Anthropic 对每条被清的结果说同一句话，那在「唯一可清的东西是读」时成立。pogo 有重复起来不免费的工具，而且真实历史里就有：`git add -A && git commit`、`git cherry-pick <sha> && go test`、以及一个覆写 `~/.pogo/providers.json` 的 heredoc。**告诉模型「再调一次」对这些不是重读，是第二次副作用。**

| 代价 | 工具 | 那句话说什么 |
|---|---|---|
| 一次调用，从当前状态重新得到 | `read` `ls` `find` `grep` | 再调一次就行（Anthropic 的原话） |
| 效果就是那个文件 | `write` `edit` | 改动在磁盘上，去读那个文件 |
| 会把命令做的事再做一遍 | `bash` | 只在重复是安全的时候再跑 |
| 要再付一个完整的子 run | `subagent` | 拿回来意味着再委派一次 |
| 没分类 | —— | 只在重复调用是安全的时候再调 |

三个决定：`bash` 那句**只陈述事实、不给指令**（命令原文就在紧上方的参数里没被动过，模型比这段文本更有资格判断能不能重复）；**缺省是谨慎的那句，而且查表显式** —— `reRunFree` 恰好是零值，靠零值兜底会让「没登记」静默等于「可以随便重跑」，那是唯一一个能把「清理」变成「动作」的缺省；`todo` **故意不在表里**，最新那份被钉住永不清，被取代的那份是过期状态、不该邀请任何人恢复它。

一条测试要求**每个注册的工具都在表里**，所以加工具时必须回答这个问题，而不是静默继承「再调一次」。

### 9.4 单调性：为什么它需要状态

`frozen` 是之前几趟已经清掉的那批 call id，**它们每一趟都会被再清一次，无论当前大小说什么**。没有这条，一次把 prompt 压回阈值以下的清理会让下一轮把原文还回去、再下一轮又清掉 —— prompt 前缀来回抖动，每个循环付一次缓存未命中。Anthropic 白拿这条（服务端一旦见过就冻结，且每次替换文本逐字相同）；pogo 得自己拿着那个集合。

由此得到一条**硬性质**：占位符必须是原文形状的纯函数 —— 只含行数、字节数、工具名，绝不含时钟、也绝不含「这是第几趟」。同一条结果在两轮里被清，必须产出逐字节相同的文本。

`ClearAtLeast` 这道地板**只管新增的清理，不管冻结集合**：拒绝重清已经清掉的东西等于把它还原，正是冻结集合要防的那个抖动。

### 9.5 被动恢复：唯一能解「永久卡死」的那条

这是整个上下文这条线上曾经真实存在的死法：上下文溢出返回 400，而 `llm/retry.go` 的重试判定只放 408/409/429/5xx 进去 —— **400 不重试，run 当场结束，而历史不会因此变小，所以同一个会话下一句话的第一次调用会再次失败，永久卡死。**

三件事让它可解：

**`llm.APIError` 保留结构。** 状态码、provider 自己的 `type`、message 都留着。之前非 2xx 被拍平成字符串，于是「prompt 太长」和「key 不对」—— 同一个 provider 上都是 400、需要相反处理 —— 只能靠匹配散文区分。而 Kimi 的错误码表暴露了关键约束：**溢出和「缺参数」「body 格式错」是同一个 `type`**，所以 type 分不开，判定只能看 message。

**判定限定在 4xx，短语只收「只可能是这个意思」的那些。** 误判的代价不对称：把一个格式错误的请求当成溢出，会让 pogo 用丢弃上下文去绕开请求里的 bug。一个 body 里恰好提到 token 上限的 500 是服务端故障，拿丢历史去绕开一次故障是错的。

**`forceClear` 推翻三处常规策略，理由都一样** —— 它们存在是为了避免为「可能不需要的清理」付钱，而一次拒绝已经回答了这个问题：

| 推翻的 | 为什么 |
|---|---|
| 触发线 | 它比对的数一半是估算的；provider 的计数不是 |
| `ClearAtLeast` | 它防的是「为一点点节省买一次缓存未命中」。这里的对照项不是缓存未命中，是一个死掉的会话 |
| 保留窗口降到 1 | **不是 0** —— 清掉模型正要据此推理的那条结果，等于让重试回答一个证据刚被拿走的问题 |

**`ExcludeTools` 不推翻。** 那是调用方明确说「永远不要清这个」，而「一旦不方便就丢掉明确指令」比「如实报告帮不上」更糟。

**有界，而且界来自已有性质：** 冻结集合只增不减，所以同一个 run 里第二次溢出找不到新东西可清、返回 false、错误直接上抛 —— 不需要计数器就不会打转。

**没有事件通知，这是有意的。** 为同一轮再发一个 `turn_start` 是错的，`web/hub.go` 正是在那里铸造这一轮的消息 id（契约：一轮一条 assistant 消息）。正确落点是一个新事件类型，那是改线上契约。最终也不静默 —— 每趟报告的是**整个**冻结集合，所以下一轮的 `turn_start` 在两个前端都会带上变大后的数字。

> **一个 provider 常量的教训（内置目录时期的事，教训保留）。** 探针发现智谱**接受并计费了一个 400,013 token 的 prompt**，`finish_reason` 是 `stop`，而当时的内置目录给 glm-5.2 写的是 200,000 —— 错了五倍。同一次探针把 kimi 的 262,144 确认为完全正确。现在这些值住在用户的 `providers.json` 里，pogo 无从替它们担保，但教训同向：`ContextWindow` 不只是显示：`auto` 取它的五分之四、占用条按它的比例变色，所以一个报小了的窗口会让 pogo 清得太早并过早建议开新会话。**这类常量要拿端点验，不是抄一次就算。**

### 9.6 成分核算与标定，以及摘要压缩为什么只手动

`session.Compose` 把一段历史拆成固定开销 / user 文本 / assistant 文本 / 工具参数 / 按工具分的结果，落进会话文件的 `stats` 记录。它存在的理由就是回答一个问题：**你的负载是工具输出大，还是对话长？** —— 因为这决定该用清理还是该用摘要。

`Calibration()` 报的是 `Measured / (Estimated − Cleared)`，也就是 4 字节/token 这个除数对这个会话真实文本的偏差。**那个减法不是修饰，是这个比值成立的前提**：`Estimated` 描述整个历史，而 `Measured` 数的是清理之后真正发出去的那个 prompt，两个数覆盖的文本不一样时相除就不再是分词器比值 —— 而那恰是这个数最该被信任的场合。分母 ≤0 时它拒绝报数而不是编一个。

拿 25 个真实多轮会话反推出来的结果，**推翻了原先三处一致的断言**：

```
以 ASCII 为主      中位数 0.98   (n=9)
中文占比 >30%      中位数 0.83   (n=11，最低 0.60)
总体               中位数 0.97   区间 0.51 – 1.11
```

原文说估算**偏低**（「对中文差了大约 2.5 倍」）。**实测是偏高，而且对中文偏得最多**：两家的分词器都在中文上训练过，一个两字词 6 字节常常只算 1 个 token，所以那部分文本真实的除数接近 6 而不是 4。**也就是 4 字节/token 在这两家上是保守的那一侧，不是危险的那一侧** —— 在有人据此把阈值往紧调之前，这一点要先知道。

**这个比值直接把触发线推走了一次。** `auto` 比对的是**估算** token，所以原来那条「窗口的一半」实际落在真实占用的 42%–49% 处 —— 清理在窗口有一半以上真正空着的时候就开始。这曾经被写成「不需要动」，而结论反了：连同 §9.5 抹掉的那个悬崖，触发线改成了五分之四（推导见 §9.3），换算过去大约是真实占用的 66%–78%。**一个偏高的估算器不是安全边际，它是一个把阈值悄悄拉紧了的乘数。**

> 测这个比值时踩过两个坑，都会把结论带偏。一是只取单点，那时 body 只占估算的 0.25%–4.5%，量到的其实是固定开销估得准不准（偏高约 7%），跟中文无关；二是**委派过的会话，父的计数器里含着子的 token**，于是比值里冒出 189.03 这种数，减掉 `delegated` 之后那个点是 0.99。

**当前摘要压缩默认关闭自动触发，但已有 opt-in 自动实现**（`agent/compact.go`、`agent/autocompact.go`）。早期选择仅手动的理由是有损摘要可能丢失约束，不能把“生成摘要成功”当成任务语义无损。后续自动路径增加了阈值、恢复及熔断规则，具体行为见 [自动压缩](auto-compact.md)。下文的手动设计理由是历史背景，不排除显式启用的自动路径。

手动 `/compact` 由用户决定何时接受摘要损失；自动路径由显式配置启用。两者都应保留校验、原始会话记录和失败信息，不把有损摘要伪装成原文。

四条不可谈判的性质，全部来自上面的证据，都已兑现：

- **钉住用户通道。** `continuation()` 把第一条 user 消息原样放进 `<original_request>`。那篇研究里保留最早那一轮是唯一把违规率压在 0% 的配置，因为人的约束就写在那一轮；而它在这里几乎免费——那 7 个大会话里 user 文本只占 0.4%–0.9%。例外是开头那条本来就很长（贴进来的文档），它也照样原样留着，这就是 `ErrNotSmaller` 存在的原因。
- **把摘要器当不可信输入的汇。** 要摘要的一切都套在 `<transcript>` 里，system prompt 明写那个元素是数据、里面读起来像指令的东西要当成「被摘要的对话内容」报告而不是执行。工具输出会进它的输入，而 pogo 读的是仓库里的文件——一个文件可以包含一句写给任何读它的东西的话。这里 pogo 白拿一个结构优势：**第一层先跑，所以进摘要器的最旧结果已经是占位符了。**
- **摘要绝不用 `subagent_model`。** 用的是 agent 自己的模型。诱惑是真实存在的（那套便宜模型映射就在手里），但那篇研究里违规跟随的是**写摘要**的模型而不是读它的：读一份弱模型写的摘要，违规率 53%，而同一个 agent 读自己写的摘要是 0%。一份摘要不是可以比价的子任务，它是那些指令**唯一剩下的记录**。
- **不给摘要器留下调工具这条路。** Anthropic 的建议是在提示里写「不要调任何工具」，`compact.go` **刻意没有写这句**，因为它做了更强的事：历史被拍平成一条 user 消息、一个工具 schema 都不给。于是调工具不是被劝退，是**不存在**。拍平是这一步的前提——带着 `tool_use` block 却不给 schema，有些 OpenAI 兼容端点会直接拒绝，光去掉 schema 等于用一种失败换另一种。空回答仍然单独检查（`ErrEmptySummary`），因为那是 Anthropic 记录在案的失败形态（`content: null`）。

**「什么都不改」是失败时的默认。** 六种拒绝各自守一件事：run 在飞（`ErrRunActive`，loop 正在往历史上追加）、对话还没长到值得摘要（`ErrNothingToCompact`）、摘要为空（`ErrEmptySummary`）、摘要没能变小（`ErrNotSmaller`）、调用期间历史变了（`ErrRaced`）、以及原对话本身就大到一次调用装不下（这一条特意点名，因为最自然的下一步是错的：重试会以同样的方式失败，能帮上的是**清理**，那是另一个机制）。一次失败的压缩最多花掉一次模型调用，对话一个字不变——**这个性质才是它敢摆在一个还没决定要不要用它的人面前的原因。**

两处只有从代码里才看得出来的连带动作，都不是整洁问题：

- **`cleared` / `clearedTokens` 必须清空。** 冻结集合记的是「已经清过的 tool result id」，而压缩之后那些结果根本不在历史里了。带着它不只是不整洁：`forceClear` 是靠**比对冻结集合压缩前后的大小**来判断「已经没有东西可清」的，残留的 id 会把基线抬高，于是被动恢复（§9.5）在这个会话余下的时间里失效。
- **会话文件是分叉，不是覆写。** `main.go` 的 `/compact` 在替换消息之后调 `store.Fork("")`：原对话作为一条不可达分支留在文件里，新链从摘要开始。所以 `-resume` 回放的是压缩后的历史，而完整原文照样可读——和撤回用的是同一棵树、同一个机制（见 `web-ui-design.md` §18）。`persisted` 偏移必须跟着一起动，否则下一次 flush 会用旧下标去切一份短得多的历史，在最不该 panic 的地方 panic。

还有一条性质今天白拿、往后得靠设计维持：**清理只碰工具载荷，从不动散文** —— 具体是 `BlockToolResult` 的正文，加上 `write` / `edit` 那条 `BlockToolUse.Input` 里的载荷字段（`contextedit.go:647-651`）；user 和 assistant 的文本一个字不动，也就是说它是「保头」的那个配置。而 compaction 是 pogo 第一个会从 prompt 里删掉人写的字的机制，`<original_request>` 那条钉子就是为这件事准备的。

### 9.7 轮次分布：上限从历史里量出来

`-analyze-session` 传文件时回答「这次会话花了多少」；传目录（或字面量 `sessions`）时回答另一个问题：**一次 run 实际需要几轮** —— 这是 `-max-turns` 该依据的分布（`analyze/runs.go`）。外部证据说固定上限设在基线分布的第 75 百分位是甜点、动态延期优于放宽上限（arXiv 2510.16786），而 pogo 有自己的 transcript，所以这个数自己量，不抄别人的。它和单文件报告是两条代码路径，因为它们**不是同一个单位**：`SessionStats.RoundCount` 数的是文件里全部 assistant 消息 —— 好几个 run 加在一起，还算上撤回放弃的分支，两个方向的虚高。

四个决定，各自对应一种「不这么做就量错」：

- **只走活分支。** 撤回放弃的 branch 上的 run 是人掐断的，不是活需要的轮次；混进去是把人的编辑当成任务的难度。沿 parent 链从叶回走，和 `session.summarise` 同一个理由。
- **右删失不进百分位。** 未完成的 run，轮数是下界（活至少需要这么多）而不是观测；当成观测会把每个百分位压低，尾部最狠，而上限恰恰从尾部选。但删失比例必须报出来：超过四分之一时报告明说「p75 是地板，不是答案」。
- **无工具调用的 run 被排除。** 它第一轮就结束，不携带任何关于上限的信息，而数量足以拖低整个分布：实测混进来 p75 从 5 变 4、p90 从 9 变 6 —— 上限会按「这段历史里有多少纯问答」来选。
- **steering 按位置识别，且有一种不可恢复。** 工具结果之后跟一条纯文本 user 消息只能是插话；但「模型已不再调工具之后 steer 进来的消息」和新 prompt 形状全同，会被算成新 run —— 它只能把长 run 劈短，方向也是压低，所以 steered 占比同样进报告。

删失的轮数**按值列出**而不是汇总成一个统计量：六个被掐断的 run 里四个停在 4，那是「这里有过一个上限」的唯一证据（transcript 不记 `-max-turns`）；而「一个停在 7、一个停在 50」取众数是在发明一个不存在的上限。点名判据写成：众数 ≥3 且占删失多数。

当前读数（102 个会话、88 个有效 run）：p75 = 5，p90 = 9，max = 25，mean = 4.3 —— 对照 `DefaultMaxTurns = 50`，是观测最大值的两倍、p75 的十倍。报告最后固定印着一句提醒，因为 p75 是唯一会被抄走的数：这个分布能用来选上限的前提，是这本历史长得像将来被上限约束的活；交互式问答的历史里不会出现无人值守任务，而后者才是上限真正约束的场景。

### 9.8 诚实的缺口

- **`Keep = 3` 和 `ClearAtLeast = 8000` 都没标定过。** 两个都是 Anthropic 的数（后者是猜的），而 100 个会话里**一条 composition 记录都没有** —— 不是写入方坏了（两个调用点都正确，都要求有一次成功的模型调用），而是那个功能上线晚于最后一次会话写入。要标定它们，先得跑几个真实会话。
- **`ExcludeTools` 在生产里够不着**：没有 flag、没有配置项，只有测试和 `forceClear` 的透传在用。它保护的那个场景（一次委派的答案被当成普通旧结果淘汰掉）在 100 个会话里没有观察到 —— 持有 subagent 结果的会话最大 15.5K，远够不着触发线，而那是因为**委派本身就是一个有效的上下文管理机制**，它把子的原始探索关在子的窗口里。所以这里刻意没有加 flag：一个为未观察到的场景加的旋钮，和之前因为同样理由被否掉的「按可恢复性排优先级淘汰」是同一个错误。
- **`promptTokens` 是混合的**：`lastInput + EstimateTokens(未测量的那段)`，一半实测一半估算。所以它不能拿去贴悬崖 —— 触发线远低于上限正是为了这个。

## 10. 配置与密钥

**本 fork 没有内置目录**：`config/catalog.go:73-89` 的两张表都是空的，`~/.pogo/providers.json` 从「扩展手段」变成了**必填**——缺文件拒绝启动，错误信息附一份可直接粘贴的最小示例（`config/file.go:181-205`）；文件存在但一个模型都不声明，同样拒绝。「一个正确的常量胜过一份可能填错的配置文件」是内置时期的决议，已被它的反面取代：不替用户断言任何端点。

配置文件只从 home 目录读，刻意不读 cwd——能决定「key 发往哪台主机」的文件绝不能随 checkout 到来（CVE-2026-21852）。`providers` 定义端点，`models` 定义目录条目（第一条是缺省模型，`default` 可另指），`key_env` 只收**环境变量名**，把密钥本身贴进去会被拒绝且不回显；JSON 是严格模式，拼错的字段直接报错而不是静默不生效。

密钥**只从环境变量读**，没有文件回退——读哪个变量由每条 provider 自己的 `key_env` 决定。`KIMI_API_KEY` / `ZHIPU_API_KEY` 不再是内置名，只是那份最小示例里的名字。一个只存在于一处的密钥才能在一处轮换掉；进程读不到磁盘上的密钥，也就泄不出去。

**`KIMI_BASE_URL` / `ZHIPU_BASE_URL` 不再是内置名**：没有任何代码读这两个变量，它们只是你的文件里 `base_url_env` 可以使用的普通变量名。覆盖机制是 per-provider 的 `base_url_env` 字段：文件里写的 `base_url` 默认钉死（`pinnedBaseURL`），环境变量绝不能改写一个明确的选择；但条目一旦自己声明了 `base_url_env`，就是同一份文件用同一份授权开了逃生门——该变量设了值，解析时就替换 `base_url`（载入处按 `BaseURLEnv == ""` 决定钉不钉，`config/file.go`）。这条路径一度是走不到的分支：内置目录删除后所有 provider 都来自文件、一律钉死；恢复它的动机是指向代理/镜像不该以改文件为代价——比如容器里挂着只读的 providers.json，只想 `export` 一个变量切到网关。没声明字段的 provider 照旧钉死：环境变量是公共环境，很可能本来就被别的客户端占用（见下方踩坑）。

`key_env` 可以**留空**：表示该端点不需要凭据（本地 ollama 之类）。此时客户端不发 `Authorization` 头，该 provider 视为始终已配置（`Configured` 恒真），模型直接可选、直接进 subagent 池。

**explore 子代理的模型分配**分两级。`subagent_model` 是 per-model 显式映射（父跑 A、只读子代理跑 B），优先级最高。`"subagent": true` 把模型标进**负载均衡池**：并发派多个只读子代理时，每个 fork 分给**在飞子代理数最少**的 provider（并列按目录顺序，即文件书写顺序），fork 时占槽、子进程退出时还槽——效果是一波并行子代理一家 provider 分一个，而不是全砸在同一个端点的并发限制上。池为空（没标记或 key 没配）时回退继承父模型，也就是没配任何映射时的行为。edit 子代理不进池：各家模型写代码的质量不一，换模型改变的是任务结果而不只是成本。实现见 `tools/subagent.go` 的 `pickExplore` / `releaseExplore`。

`followup_model` 是同一个形状的 per-model 映射，给回合结束后的追问建议生成用（web 模式，`-followups` 默认开）：那次调用小且不在回答的关键路径上，是便宜模型的典型场景。没配映射、或映射目标的 key 没导出时，由会话自己的模型生成；指向不存在的模型只警告并回落，不阻断（与 `subagent_model` 同一条规则）。实现见 `web/followups.go`。

> 踩过的坑：早期版本读 `~/.pi/agent/models.json` 并让 `ANTHROPIC_BASE_URL` 参与优先级。本机那个变量指向一个没启动的本地代理，第一次跑请求直接被劫走。**那类环境变量常被别的客户端占用，让它默默改写路由太危险。**

## 11. 两种运行模式

分叉只在 `run()` 的最后一步。两种模式共用同一个 `agent.Agent` 实例、同一个 `a.Run()`、同一个 renderer；区别只是**谁提供 prompt** 和**调几次**。

```
main()
 ├ -models / -sessions / -web → 各自处理后退出
 └ run(opts)
     ├ readPipedStdin() → buildPrompt()
     ├ newClient(model) → agent.New{client, tools.Default(cwd), SystemPrompt(cwd)}
     ├ openSession(resume)
     ├ signal.NotifyContext → ctx      ← Ctrl-C 取消链路
     └ prompt 非空 ? 一次性 : 用法提示，exit 1
```

**stdin 语义照 pi**：非 TTY 就读全部，内容放在指令**前面**（空一行分隔）。管道内容当数据、`-p` 当指令。

> 2026-11-01：REPL 与 `tui/` 已移除（见 [退役记录](tui-platform-direction.md)），交互使用走 `-web`；`-mode text` 现在只在运行结束后打印最终回答。下面三条及其后的细节是移除前的设计记录。

三处与 pi 的差异，都是已知的：

1. **REPL 不是 TUI**：自带单行编辑器（stty raw mode，零第三方依赖），有历史回溯和 Tab 补全；无多行输入。终端界面分三个阶段建成，全在 `tui/termui.go`，统一机制是 **DECSTBM 滚动区**（`\x1b[1;{bottom}r`）：终端自己把流式输出约束在滚动区内，无论软折行怎样都碰不到钉住的行；钉住行用绝对寻址 + `\x1b7/\x1b8` 重画。没选 inline 差分重绘是因为软折行会让「从光标往上数行」的记账漂移；没选 alt-screen 是因为要保住 scrollback。所有终端写在 dock 互斥锁下串行（消费者逐事件持锁、tick 逐次持锁、stderr 重试提示走 lockWriter），进程退出路径由 `close()` 兜底恢复滚动区。**布局**（整个会话钉住，一次性 `-p` 则按 run 钉住）：最底行恒为状态行——模型 · cwd（`~` 折叠）+ 右对齐 context 仪表（每轮 `EventMessage` 的本轮 prompt tokens ÷ 模型窗口；<10% 一位小数，>70% 黄 >90% 红；无数据不显示）。**prompt 时**（阶段 3）：输入行 + 实时候选列表是状态行上方的「zone 行」，绝对寻址绘制（取代了旧的相对光标舞步），输入超宽自动占多行（顺带修了旧版长输入残影），回车把输入行**重印进滚动区封存**、zone 归还；光标只在编辑区，run 时才隐藏。**run 时**（阶段 1+2）：状态行加 spinner + 已用时（10Hz，内容没变就跳过不画），在飞工具各占一个 live 行（`⠋ bash … · 1.2s`，标签取 command/path/`mode: task`，超 5 行折叠 `… +N more`，模型产文本先剥控制字符防转义注入），结束即蒸发——transcript 追加行一字不变，所以永不重排历史；「原地更新已封存内容」（可折叠完成框）才需要 pi 那种全文档渲染，明确不做。滚动区随 live/zone 行数动态再分裂（增长先 `\n` 滚出空白行再重设底界，缩减先擦释放行；DECSTBM 归位光标，全程 save/restore 包夹），SIGWINCH 重排并调编辑器重画钩子。命令可缩写：回车时 `/` 前缀解析成补全列表第一个命令（`/e` → `/exit`），光 `/` 不匹配，exact 先于 prefix 两遍扫描。终端不可测（stty size 失败）时整体回退到旧的滚动行为。**assistant 文本过流式 markdown 过滤器（阶段 4，`tui/mdstream.go`）**：transcript 是 append-only，所以散文全速流式（`**粗体**`/`*斜体*`/`` `行内代码` `` 是 ANSI 开关，一两个字符前瞻即决，遵守 markdown 空格规则，`2 * 3` 不会变斜体；行末未闭合的样式自动关掉，绝不漏进后续输出），行首构造只微缓冲几个字符分类（`#` 标题渲染成粗体、`---` 渲染成通宽 `─`、代码栅栏渲染成 `│ ` 前缀行、栅栏标记吸收），**表格是唯一按块缓冲的**（行被完全吸收、不留空行占位，块结束时用线框字符重绘：列宽按显示宽度算（CJK 免疫）、超终端宽度时最宽列逐次减半、分隔行吸收、单元格内联样式照走）。非交互（管道/重定向）时过滤器不挂，输出逐字节不变。**工具输出侧**：bash 流式行与 tool_end 摘要不套 dim、SGR 全强透传（`sgrOnly`，`tui/ansi.go`：保 SGR、丢光标移动/擦除/OSC——顺手堵死了命令输出里 `\033[2J` 拆 dock 的洞；`Summarize` 在截断**之后**才 sanitize，切断的半个转义直接丢弃）；流式行另有 `colorLongLine` 兜底——认出 `ls -l` 权限串行后渲染侧自己给名字列上色（目录天蓝、软链青、可执行绿；实测管道下 macOS BSD ls 对 `CLICOLOR_FORCE`/`-G` 均不上色，只能显示侧补），已带转义的行不碰；`ls` 结果渲染成单行宽度有界的名字列表（`lsSummary`），目录天蓝（`dirBlue` = `1;94`，34 在多数终端主题下发闷）、文件 dim，宽度按 `cellsWidth` 算（CJK 免疫），尾部 `[N entries limit …]` 元信息行不当条目
2. **错误处理不对称**：`-p` 一轮失败即 `exit 1`；REPL 打印红字后会话继续。有意为之
3. **两种模式都会写会话文件**，`-resume` 对两者都有效。所以区别在交互性，不在持久化

顺带两个细节：重定向时不输出 ANSI 转义码（`pogo -p "x" > out.txt` 不该往文件里塞颜色控制符）；`isCharDevice` 判断的是「没被重定向到文件或管道」而**不是**「一定是终端」——`/dev/null` 也是字符设备，要真正区分得走 ioctl（需要 `golang.org/x/term` 或平台相关的 unsafe 代码，都不划算）。REPL 首次读取即 EOF 时报错，一举覆盖 `/dev/null`、空管道、关闭的 stdin 三种情况。

## 12. 事件流

`agent.Run()` 返回 `<-chan Event`，12 种事件（`agent_start` / `turn_start` / `text_delta` / `thinking_delta` / `message` / `tool_args` / `tool_start` / `tool_partial` / `tool_end` / `steer` / `tool_results` / `agent_end`）。其中两种不上 wire：`agent_start` 由消费者各自渲染（web 的 `run_start`、JSON 模式的 Header），`tool_results` 为增量落盘而存在（§8），同样的结果浏览器已经从 `tool_end` 渲染过了。

> 这里写了一段时间的「10 种」，漏的是 `tool_args`（模型还在生成参数时的增量，早于 review、早于 `tool_start`，让消费者能尽早开一张预览卡）。**一个数错了的清单和一个漏了项的清单是同一类 bug**，而这一处是数完代码才发现的 —— 所以 §13 表尾那条建议对本节同样适用：每条断言都该能被一句 `grep` 核掉。

**这个设计的兑现点**：现在有三个消费者——终端 renderer、`web/` 的 Runner、测试。加浏览器界面没有改动循环的形状。

两条契约：

- **调用方必须排空 channel。** 它在 `EventAgentEnd` 之后关闭
- **终止事件用阻塞发送**（`emitFinal`）。普通 `emit` 在 ctx 取消后 `select` 两个 case 都就绪，Go 随机挑，**约一半概率把 `agent_end` 丢掉**。CLI 上只是少打一行 usage，Web 上是界面永远显示「运行中」

> 后一条是 Web 层的取消测试抓出来的，手测极难复现（要正好撞上那次随机）。

### 12.1 `agent_end` 为什么带两个原因字段

`Event.StopReason`（`llm.StopReason`）是 **provider** 结束**一次回复**的理由；`Event.EndReason`（`agent.EndReason`，`agent/endreason.go`）是 **harness** 结束**一次运行**的理由。两个都发，因为第一个没有词汇能表达 harness 自己的决定 —— OpenAI 协议里没有「轮次上限」。

**加它之前是这样的：** 轮次上限、预算超限、停滞三条退出路径只设 `Err`，所以线上 `stop_reason` 缺席（`omitempty`），唯一可读的信号是 `error` 里那句英文散文。**那意味着每次改措辞都是在改一个没人声明过的接口。**

这和 `llm.APIError` 保留结构（§9.5）是同一个判断的第二次应用，只是换了一层：那边是「prompt 太长」和「key 不对」在同一个 provider 上都是 400、需要相反处理，只能靠匹配散文区分；这边是「撞轮次上限」和「provider 挂了」都只有一句 `error`。**同一个错误不该在一个项目里犯第二次。**

十个取值加一张 disposition 分类表（`done` / `continue` / `intervene` / `halt`）。分类不是装饰，它是这个类型存在的**具体**理由 —— 「不是 `completed` 就重试」对四个取值都是错的：

| 取值 | disposition | 为什么不是 continue |
|---|---|---|
| `stagnation` | **intervene** | 判定看的是相同的工具结果，所以从同一份历史起的新 run 会在同一轮再次触发 |
| `context_overflow` | **intervene** | 清理已经跑过且释放为零，下一个 prompt 一样大。能帮上的是压缩，而它按 §9.6 是手动的 |
| `transport_error` | halt | provider 挂了不是任务的问题，而且没有任何东西说那个条件已经解除 |
| `aborted` | halt | 有人故意停的 |

两条守着它的性质，都跟 §15 的雷区同类:

- **没登记的取值按 `halt` 处理**，而登记仍然由测试强制（`TestEveryEndReasonHasADisposition` 双向比对 `AllEndReasons` 与 `dispositions`）。默认值是失误的地板,不是不回答问题的借口 —— 谨慎的方向不对称:读不懂而停下的代价是一个卡住的任务,继续下去的代价是无上界的运行次数。
- **没有任何退出路径可以留空这个字段**（`TestNoExitPathLeavesTheEndReasonEmpty` 覆盖八条路径）。空值会被消费者读成「未知原因」进而 halt，于是一次忘记赋值变成一个卡住的驱动，而不是一次测试失败。

`aborted` 有两条到达路径，都必须落在同一个取值上：客户端那条（**取消在这个接口上不是 error** —— `llm/openai.go` 在每个可中断点返回 `Response{StopReason: StopAborted}` 加 nil error），和 loop 自己在工具批次之后的 `ctx.Err()` 检查那条。分开测，因为搞错是看得见的：**一次 Ctrl-C 报成 `transport_error` 等于告诉操作者 provider 坏了。**

> 九条 mutation 逐个验过：把任一处赋值删掉、把溢出报成普通传输错误、把 `stagnation` 改判成 `continue`、把默认值改成 `continue`、从表里删掉一行、以及让 `wire` 丢掉这个字段 —— 九个全部有测试变红。

wire 侧是 `end_reason`，**追加而不是重命名**（`wire` 包的既有规则），`stop_reason` 一个字没动，改之前写好的消费者看不出区别。用法见 `../README.md`。

### 12.2 三个预算，以及为什么 `-cost-budget` 会拒绝启动

`-token-budget` / `-cost-budget` / `-time-budget` 都在每轮开头查（`checkBudgets`），各自对应一个 `EndReason`，全部是 `continue` —— 额度用尽而 transcript 完好。委派出去的 token 也算在内，延迟一轮结算（为了不破坏用量计数器的单写者）。

**`-cost-budget` 曾经是一个完整接好的空实现。** flag 定义了、中英双语帮助写了、经 `Config.CostBudget` 一路传到 `a.costBudget`，然后没有任何东西读它 —— 因为目录里没有单价可以乘 usage。设 `-cost-budget 5.0` 得到的是零执行、零警告。**比没有这个 flag 更糟，因为那个数字读起来像一道保险。**

它现在的形状是这样定下来的：

**一、pogo 不内置任何价格，理由和不内置 `subagent_model` 映射一字不差。** 一个模型多少钱是一句关于**别人的计费安排**的断言，pogo 没有依据替你做。这条写在还有内置目录的年代——当时内置的两家都是订阅套餐（Kimi for Coding、GLM Coding Plan），per-token 成本不是「未知」，是**不存在的量**，为它们编一个数不是近似，是虚构；目录改由用户提供之后，论证只是更强：pogo 对任何人的计费安排都没有依据，拿虚构算出来的上限比没有上限更糟。

**二、所以缺价格时拒绝启动，而不是警告。** 对照 `subagent_model` 指向不存在的模型那条：那里只降级+警告，因为它**有一个合理的降级态**（继承父模型，会话照跑）。花费上限没有降级态 —— 「继续跑但不设限」是所请求内容的精确反面，而这恰好是没人盯着的那种运行。错误信息里点名缺哪个模型的价格、给出可直接粘贴的 JSON、并指出 `-token-budget` / `-time-budget` 不需要价格。

**三、单位刻意不是货币。** 价格按**每百万 token**写（每家厂商都这么公布，抄价格页时不用挪六位小数），而那个数字的单位是**你自己被计费的单位**。pogo 从不打印货币符号、从不换算，`-cost-budget` 就按同一个单位比。断言 USD 会是叠在第一个无依据断言上的第二个 —— 内置年代的两家就都按人民币计费 —— 而一个贴错货币标签的正确数字属于没人会回头再读的那类错误。

**四、`nil` 与「声明为零」必须是两件事。** 本地模型服务确实不花钱、有权这么说（`llm.Price.Zero()`）；而没人定过价的模型不能被一个专门用来阻止花钱的 flag 悄悄当成免费。所以 `Model.Price` 是指针。

### 12.3 两个子集字段，两次踩到

`llm.Usage` 有两个字段是别人的子集，注释里都写着：`CacheRead ⊂ Input`、`Reasoning ⊂ Output`。**这个项目在两个地方各踩了一次，方向相反：**

- **`-token-budget` 加了 `Reasoning`。** `Input + Output + Reasoning` 把思考 token 算了两遍，于是一个思考重的运行**提前**撞上限 —— 预算看起来在工作，实际在掐掉还有余量的运行。而 `CacheRead` 同一行里是正确地没加的：**模式在这里是已知的，只被应用到了两个字段中的一个。**
- **成本计算差点把 `CacheRead` 加了两遍。** 直觉写法 `Input*rate + CacheRead*cacheRate` 会把缓存部分按全价再按缓存价各收一次。正确的是先减：`(Input − CacheRead)*input + CacheRead*cacheRead + Output*output`。

所以 `Price.Cost` **放在 `llm` 里、紧挨着 `Usage`**，而不是放在需要它的那个包里 —— 子集关系写在哪，处理它的算术就该在哪。`Reasoning` 在 `Cost` 里完全不出现，注释专门说明这不是遗漏：一个逐字段核对的读者应该在那里找到答案，而不是推断出思考是免费的。

> 缓存率不是可选的精细化。kimi 的未命中约按命中价的十倍计、智谱约两倍，所以前缀复用好与不好的两次会话，**成本差距远大于 token 差距** —— 而那恰好是上限被要求抓住的那个差距。

还有一条只有 mutation 测试才问得出来的：**`checkBudgets` 里那个 `a.price != nil` 只防 nil 解引用，别的什么都不防。** 拿一个零价格替代 nil 同样不会停下任何运行（零费率永不超过正的预算），两者从这个函数内部看无法区分。所以「未定价的运行不会不设限地跑」这条保证**不在这里**，在 `checkCostBudget`（拒绝启动）。原来的注释把这件事说成了「否则每次运行都会在第一轮前停下」—— **方向是反的**，而它是被一条没被抓住的 mutation 逼出来的。

### 12.4 软轮次上限：检查点而不是墙

`-max-turns` 回答「什么时候放弃」，回答不了「该不该放弃」—— §9.7 量出的分布（p75 = 5、p90 = 9、max = 25）说明 50 这堵墙离绝大多数运行很远，而走到墙附近的恰好是单一数字服务最差的那批：设小了切掉真活，设大了放任打转。

所以 `-soft-turns`（默认 10）把一堵墙改成一串检查点：每用掉 n 轮，loop 往对话里插一条 user 消息（`agent/softcap.go`）—— 已用轮数、软上限、硬上限，然后二选一：收尾，或者继续并用一句话说明还差什么。

三个决定，各自防一个具体的错：

- **没有解析器。** 模型的下一个动作就是决定本身：继续调工具 = 延期，纯文本回答 = 完工，走的是和没有软上限时一字不差的结束路径。「匹配散文做分支」是 §9.5 批评过一次的做法（那边是把非 2xx 拍平成字符串再匹配），不在这一层再犯。机制只保证两件事：决定是**知情**做出的，「还差什么」落在 transcript 里可审计 —— 下一个检查点能拿着它对账。
- **延期不需要自己的计数器。** `-max-turns` 就是延期次数的上限，所以软上限 ≥ 硬上限时永不触发 —— 不是配置错误，是自然失效。
- **检查点走 steering 的既有路径**（`EventSteer` → wire 的 `user_message`），因为那就是它的本质：一条在合法边界落进对话的 user 消息。JSON 模式和 web 的消费者本来就会渲染它，transcript 照存，`[pogo]` 前缀标记它是 harness 写的而不是人打的 —— **没有新事件类型，wire 契约一个字没动**。TUI 原本没有 steering 入口、也就不渲染这个事件，现在补了一个 Dim 单行（`tui/render.go`），否则模型突然说「我还差…」会显得在回答一个没人问过的问题。

**默认值只在 CLI 生效。** `New` 不给 `SoftTurns` 赋默认（`DefaultSoftTurns` 是 flag 默认值，不是 Config 默认值），所以直接构造 Config 的嵌入方 —— web（和预算一样显式钉 0）、测试 —— 保持零值，行为逐字不变。web 不给的理由和预算相同：浏览器里有人盯着，而这个机制的收益场景是无人值守。

默认 10 而不是 §9.7 量出的 p75 = 5：本机历史以交互式短任务为主（mean 4.3，40% 在 2 轮），按 p75 设会让接近一半的正常任务都走一遍询问。按 p90 取 10，让延期去覆盖真正的长尾。

### 12.5 链式运行：reset 而不是 compaction

`-max-runs <n>`（仅 `-p`，默认 1）是这条主线的最后一层：一次 run 因 **disposition 为 `continue`** 的 `EndReason` 结束（四个额度 + `max_tokens`）时，会话 `Fork("")` 出一个新分支，一次全新的 run 拿着空上下文接着做。三件事各有一个出处：

- **为什么是 reset 而不是自动压缩。** 三方证据的交集：Anthropic 两代长时程 harness 的结论都是 compaction 不够用、reset 才是解；§9.6 有自己的证据反对自动摘要（违规率 0%→30%）；而 reset 恰好绕开 §9.6 反对的那件事 —— 它不重写对话，它换一个新 run，旧 transcript 作为被放弃的分支一个字不改地留在同一个文件里，两端经 fork 记录可追溯。
- **接力载体是一个 Markdown 文件，不是 JSON 契约。** `.pogo/handoff.md` 由模型维护：任务一句话、已完成及验证方式、还剩什么。system prompt 在链式开启时多一节（`chain.go` 的 `chainSection`），告诉模型它唯一不可能自己发现的事：上下文不会带到下一棒。新 run 的开场 prompt 带 `<original_request>` 原文 —— 和 `/compact` 是同一颗钉子，因为 handoff 是模型关于任务的**转述**，两者冲突时以人写的为准。**不规定 JSON 形状**：机器校验的契约是 evaluator（§13 里那一行）的事，在没有检查者之前规定字段是仪式。
- **哪些结束原因接力，由 §12.1 的 dispositions 表决定，不在驱动代码里。** `chainContinues` 只是读表：`stagnation` / `context_overflow` 是 `intervene`（重跑原样失败），`transport_error` / `aborted` 是 `halt`（原因在任务之外）。这就是为什么 §12.1 当初值得做成结构化：外层驱动不用匹配散文。

两个刻意的边界：**每棒的预算独立**（`-token-budget` 等是 per-run 的，所以链式总花费 ≤ n × 预算；要更紧的总闸，就调小单棒预算，没有单独的链式旋钮）；**web 不接力**——有人在场时「继续」两个字比机制便宜，而 fork 别人的会话不该在人家没要求时发生。

实测（`-max-turns 4 -max-runs 3`，一个 6+ 轮的写文件任务）：三轮接力完成，handoff.md 按协议写出了 Task/Done/Left。同时实测到一条缝：**最后一棒如果把轮次花在干活上，收尾回答可能挤不进预算** —— 活干完了但 exit code 是 1。缝的答案不是把回答硬塞进去，是下面的判定者。

**判定者是 `-evaluate`（默认关）。** 没有它，驱动只能信 run 自己的说法：「完工」是模型说的，「没完工」只是上限打断了它。Anthropic 把这个失败模式点名为：agent 自评会自信地夸奖自己的产出，所以判定者必须不是干活的那一个。实现上是**进程内一个全新的只读 agent**（`chain.go` 的 `evaluateRun`）：read/ls/find/grep，没有 bash/write/edit/todo/subagent —— 和 explore 子代理同一形状（隔离靠工具缺席，不靠检查），但没有子进程、没有自己的 transcript：判定不是工作产物，不进会话文件，verdict 打在 stderr 上。没有 bash 是有意的：它的证据只能是**文件里实际有什么**，而不是哪条命令打印了什么。

它只在两个判定点发言，别处一句话不说：

- **某棒声称完工（`completed`）。** `PASS` 就接受；`NEEDS_WORK` 且还有 run 预算，就带着发现接力下一棒（`findingsPrompt`，含一条反拨条款：发现错了就拿证据说话，别瞎改）；预算用尽则以 evaluator 的发现报错退出。
- **run 数用尽而结束原因是额度类。** 核验属实就 exit 0 —— 上面那条缝就这样合上：活干完了，不该报失败。
- **evaluator 自己失败（读不出 verdict、跑满自己的 15 轮）时不改变任何结果**：没有它的行为就是兜底。判定者失效的最坏情况是退回老行为，这是它能放在无人值守路径上的原因。

判定协议是**一行 `PASS` / `NEEDS_WORK` + 每行一条具体发现**，解析只认第一行的精确两个词（`parseVerdict` 有测试，含「小写不算 verdict」—— 猜不是解析）。验收两条路径都真跑过：run 数用尽但活干完 → exit 0；run 数用尽且活没干完 → exit 1，发现具体到「少了哪两行、我看了哪个文件」。

**为什么不同时做 Default-FAIL 的 todo 门槛**（「标 completed 必须有过一次成功的 bash 调用」）：它会误伤合法的纯读写任务（改错别字不需要 bash），而绕开它只要一句 `echo`。一个既误伤又挡不住的门槛是负资产；提前宣布完工这个失败模式由 evaluator 在判定点上覆盖，比在每笔待办上盖戳有效。哪天 todo 的 completed 有了机器可查的判据（比如契约文件），这条要重新评估。

## 13. 未做的东西

这张表整个换过两遍。第一遍：原来的五行**五行都过时了**，其中两条的结论被后来的实现推翻。第二遍：上一版留在表里的**摘要式压缩**与 **`/compact` 手动命令**两行，也已经实现（`agent/compact.go`，三条路径都接好了，见 §9.6）—— 也就是说这张表在被重写过一次之后，又漂移了一次。记下这件事本身，因为一份「未做清单」一旦落后，读它的人会去重做已经做完的东西 —— 判断「下一步做什么」的成本会因此高于做那件事。

| 缺口 | 说明 |
|---|---|
| **摘要压缩的自动触发** | **v1 已落地（`agent/autocompact.go` + 驱动层接线，`-compact auto` 显式开启，默认 off 即原行为）**：运行之间阈值触发（窗口 − max(16384, 窗口/20)）、溢出时压缩并原文重放该条 prompt（每条至多一次）、连续 3 次失败熔断。触发点选在驱动层而不是轮内，是因为 `TurnPersister` 与 end-of-run flush 按消息数对账——轮内改写历史要先改 wire 契约，那是 §13 之外一份独立的设计变更。原「不自动」的两条依据（本项目 100 份转录的形态测量 + arXiv 2606.22528）没有被推翻：前者论证级联（清理先行，压缩兜底），后者给出了把违规率压回 0% 的工程解（Constraint Pinning / head_tail——`continuation()` 钉首条消息正是其骨架）。完整决议、阶段划分（v2 尾部保留/轮内触发、v3 约束钉住+`-compact-stress` 测试原语）见 `docs/auto-compact.md`。pi 上游（issue #92）已默认开启自动压缩是改主意的家谱证据 |
| MCP、plan mode | MCP 客户端和服务端均已实现，使用官方 Go SDK；实例与 Agent 出口、任务工具、鉴权、策略和配额见当前 `web/mcp.go`、`web/agentmcp.go`、`mcpclient/` 及 MCP 主题文档。接口数量与依赖数量以当前代码为准。独立 plan mode 尚未实现 |
| **OS 级沙箱** | `bash` 没有路径限制，闸门自称不是安全边界，而 `-p` 在终端里跑本来就没有闸门 —— 也就是「Linux 服务器无人值守」这个目标场景恰好是防护最少的那条路径。接缝已经在了（bash 是唯一执行入口，`procgroup_*.go` 已按平台分裂），缺的是 Linux 上的 Landlock/seccomp 那一层。**这是本文列出的缺口里唯一一条会改变安全性质的** |
| **记忆的过期回收** | 机制在了（§5.5），但只做到「把 age 显示出来」。真正的回收要一条策略，而策略要先有真实使用数据 —— 同 §9.8 对 `Keep` / `ClearAtLeast` 的态度 |
| 会话树的**可视化** | `Store.Fork()` 不再是死代码（撤回在用，见 `web-ui-design.md` §18），但被放弃的分支在界面上不可达。「让人看见并跳转」仍然没做 |
| 交互式 CLI 的闸门 | TTY 上的 y/n 仍然没做。**但原来那句「`-p` 永远不能有闸门」已经被推翻**：`-p` 的子进程有闸门，靠继承 fd 把决定转发给父进程（`gatefd.go`、`tools/gatechan.go`）；只有在终端里直接跑的 `-p` 保持无闸门以维持可脚本化 |
| CLI 侧读 `Undelivered` | `-mode text`（`textmode.go`）不读它，插话被丢时不出声。**现在够不着**（CLI 没有插话入口，只有 web 有），所以留着等 CLI 真的加入口时一起做 |

已经做完、而这张表的历史版本仍写着「未做」的：subagent（含 worktree 隔离与子进程闸门）、todo 工具、上下文清理与被动恢复、REPL 多行输入（行尾 `\`、Alt+Enter、bracketed paste）、**摘要压缩与 `/compact`**（CLI、`-p /compact`、Web 的 `control{action:"compact"}` 三条路径 + `EvCompacted` 事件）、**动态轮次**（`-soft-turns` 检查点，见 §12.4；数据前提在 §9.7）、**reset + 交接与判定者**（`-max-runs` / `-evaluate`，见 §12.5；todo 的 Default-FAIL 门槛是**有意不做**，理由在同节）、`-mode json`、后台委派（`subagent_tasks`）、`browser` 工具、密码登录（`POGO_USERS_FILE`）、主题系统、per-turn 落盘（`TurnPersister`，见 §8）。

> 这张表现在漂移过两次，所以值得给下一个人留一条操作性的建议而不是又一句自省：**它列的每一条都应该能被一句 `grep` 否掉。** 「MCP 协议」→ `grep -ri 'modelcontextprotocol' --include='*.go'` **命中 `web/mcp.go`**（2026-08 协议层落地，见上表行的四条决议；此前那句「为 0」的断言在落地当天同步翻转——这是这张表第一次跟着实现改，而不是被实现甩开）；「OS 级沙箱」→ `grep -ri 'landlock\|seccomp\|seatbelt'` 为 0。上面两次漂移的共同点恰恰是那两条**当时已经能被 grep 否掉**——「MCP 为 0」那句自己也是第三次漂移的实例：面板桥（`panelBridge.ts`）先用了 MCP Apps 的 `ui/message` 格式，之后 `tools.json` 又采用了 Tool 对象形状，而清单里那句 grep 断言一直没跟着改。（这里原来还有第三条例子：「跨会话记忆 → 没有任何可写根传给 `read` 之外的工具」。它自己就是这条建议要防的那种漂移——memory 的 `WriteRoots` 早已授予 `write` / `edit`，那句 grep 什么都否不掉，故删去。）

### 13.1 MCP client（`mcpclient/`）

上表「MCP 客户端仍不做」在本节落地当天翻转：连别人的 server 与被别人连（`web/mcp.go`）共用同一套协议词汇（同一个 go-sdk 依赖，协议细节不重复推导），而前者是多 agent 部署里 pogo 消费工具的方向。

**配置**：`~/.pogo/mcp.json`（`POGO_MCP_FILE` 搬家），CLI 与 web 读同一个文件——`/api/mcp` 编辑器改的正是 CLI 下次启动读的。严格解码（未知字段即错，与 providers.json 同款，prefs.json 的宽松在这里不适用）：这是「要执行什么命令、要连什么主机」的配置，一个写错的键是用户以为在生效的命令。凭据默认不落盘：`bearer_token_env` 存环境变量**名**，env/headers 里的 `$VAR` 在连接时展开；`bearer_token` 是刻意的例外（见表）。文件缺失是正常（空配置），存在而解析不了是错误且**不静默**：CLI 打警告继续（MCP 是附加件，不该拖垮主流程），web 在 GET 里带 `load_error`（坏文件正是用户需要编辑器的时候，prefs 的 LoadError 先例）。

| 字段 | 含义 |
|---|---|
| `enabled` | 缺省 true；显式 false 把条目留在文件里但不连接 |
| `command` / `args` / `env` / `cwd` | stdio server，与 `url` 二选一（同设即拒） |
| `url` / `transport` / `headers` / `bearer_token_env` | HTTP server（streamable 为默认，`sse` 是旧 2024-11-05 传输）；scheme 只认 http/https |
| `bearer_token` | 直填 token 值，与 `bearer_token_env` 互斥（同设即拒；`$` 开头或纯空白也拒——那是把 `$VAR` 当值粘贴的笔误）。与 providers.json「凭据不落盘」是刻意的分歧：headers 本就允许手写 `Authorization` 头，一等字段只是把既有可能性做正。文件是 0600 但仍是磁盘，能用 env 就用 env |
| `startup_timeout_ms` | 连接 + 首个 tools/list 的预算，默认 10s，夹到 1s..120s |
| `tool_timeout_ms` | 单次调用预算，默认 120s，夹到 5s..3600s |
| `include_tools` / `exclude_tools` | 工具名或单个尾置 `*` 前缀通配（`mal.*`）；exclude 恒赢——排除是安全方向 |
| `trust` | 该 server 的工具免审，语义见下 |
| `required` | 首轮连不上则 `WaitReady` 报错、CLI 拒启（Codex fail-fast）；不设则降级为后台重连 |

（上表是 `servers` 里**每个条目**的字段。mcp.json 还有第二个顶层节 `server`——本机**作为** MCP 服务端的配置，schema 与优先级见 §13.2。）

**命名与投影**：wire 名 `mcp_<server>_<tool>`（`tools.WireNameFor`），消毒到 wire 字母表，超 `MaxWireNameLength`（64）截断加 fnv32a 哈希尾巴，映射确定性。server 的 annotations（readOnlyHint 等）按 MCP 规范是**不可信提示**：只用于调度（并行资格）与只读子会话过滤，绝不构成免审。与 manifest 工具撞名时 MCP 条目被跳过并记日志——注册表后写覆盖但 order 留双份，且信任名单会把同名 manifest 工具错免审，跳过是唯一不说谎的答案；内置工具无需检查，`mcp_` 前缀天然避开全部内置名。断连中的 server 不投影（陈旧目录上的调用必然失败，缺席比报错是更清楚的信号）。

**trust 与审批**：`trust: true` 把该 server 全部 wire 名挂上新会话的 `AllowTool` 放行单——与 manifest 的 readOnlyHint 挣得 `MarkReadOnly` 同形，但授权方只能是写 mcp.json 的 operator（server 自己的声明不值钱）。这是 `AllowTool` 的既有语义，注意边界：**strict 模式下 trust 仍免审**——strict 收紧的是「作者声明换豁免」那条通道，operator 自己写下的授权不在其列。未 trust 的 server 工具按默认矩阵走人工审批（web 的闸门）；CLI 无闸门，照旧直跑。

**环境消毒**：stdio 子进程的环境 = 继承集 − 疑似凭据（名字含 TOKEN/SECRET/PASSWORD/KEY/AUTH/CREDENTIAL，或 `POGO_` 前缀）+ 配置里的显式 env（值从**未消毒**环境展开——写进文件即知情同意，Gemini CLI 同语义）。SDK 不动 `cmd.Env`，exec 的默认是把 pogo 自己的 API key 一并送人，所以剥离是默认、显式条目是唯一回传通道。误伤（MONKEY、KEYBOARD_LAYOUT）是便宜的方向：要回哪个变量，显式声明即可。

**连接模型**：池（`Pool`）每 server 一个 keeper goroutine——连接、持有、断了按指数退避（1s 起、封顶 30s、全抖动）永远重试，因为对端可能只是重启；`Reconcile` 按指纹对齐配置（增/删/改即重连），连接全部后台异步。工具对象在重连下热换会话指针，重连窗口内的调用 fail-fast 报「reconnecting」。`tools/list_changed` 去抖 500ms 后重拉目录；活连接上的目录变更（增/删/改描述或 schema）一律 WARN 落审计——server 中途改工具描述是经典的恶意 MCP 手法，日志是唯一看得见它的地方。单 server 超 20 个工具只告警，不按数量截断；已选且已连接服务器的有效工具全部保留，发给模型的工具声明由独立的元数据／发现设置控制。

**会话快照**：工具集在会话创建时取快照（web 的 `newSession`、CLI 的启动路径各取一次），**运行中的会话不感知新连接**——与 skills 同一模型，理由同一条：工具集喂 schema 声明，中途变化每轮都 invalidated 缓存前缀。启动路径的 `WaitReady` 等首轮尝试落定：required 失败拒启，optional 失败降级重连，两路都不挂死在网络上。

**编辑面**：`GET/PUT /api/mcp`、`GET /api/mcp/status`、`POST /api/mcp/probe`（`web/mcpclientapi.go`）。`/api` 前缀自动吃 token+Origin 中间件；裸 `/mcp` 是**反方向**的机器通道（别人连进来），勿混。PUT 严格解码 + Validate + 落盘 + 后台 Reconcile，不等连接结果。probe 是「不进池的临时连接」，走与真连接**同一** `buildTransport`（bearer 解析、header 展开、stdio 消毒同一代码路径——probe 的意义就是预演真实连接），失败返回 200 + `ok:false`：探测是诊断不是事故，与 `/api/config/probe` 同款。

**一期不做**：OAuth 流（server 侧 401 已留 `WWW-Authenticate` 挂钩）、elicitation、sampling、roots、progress 通知、**子代理继承**——连接是有状态会话，子进程要继承就得重连、重过滤、重付启动等待，为一次十分钟的委派不值；v1 直接不给（`run()` 只在 depth 0 建池）。

### 13.2 MCP server 的配置文件化（env > file，热生效）

§13 表行的 server 侧最初只认环境变量（`POGO_MCP`/`POGO_MCP_TOKEN`/`POGO_MCP_POLICY`/三个 `POGO_MCP_MAX_*`）；现在它读 mcp.json 的 `server` 节，由 `/api/mcp` 编辑，**环境变量仍在最高层**——malagent 一类容器部署靠 env 覆盖，行为不变。优先级只在 `web/mcpserve.go` 一处判断：

**env > file（mcp.json `server`，PUT 热换）> static（`ServerOptions.MCP`，测试/嵌入路径）> default**。

| `server` 节字段 | 生效语义 |
|---|---|
| `enabled` | `POGO_MCP=1` 强制 true，否则文件值，默认 false。**默认关**：无 server 节与无 `POGO_MCP` 的昨天等价——新的网络面是授予不是默认 |
| `token` / `token_env` | `POGO_MCP_TOKEN` 优先，否则文件（直填，或变量名解析）。互斥；`$` 前缀/纯空白拒（同 `bearer_token` 规则）。**都为空 = 明确的无认证**：客户端写不写 Bearer 都收，这不是疏漏是语义。`token_env` 变量缺失 = 配置错误，**503 fail-closed + 大声记日志**——「管理员以为有认证实际没有」是最危险的错向，绝不按「未配置」静默开门 |
| `policy` | `POGO_MCP_POLICY` 优先于文件，枚举 strict/standard/auto，缺省 standard；请求时读取，影响 synth 直通矩阵与 `agent_submit_task` 的策略重施 |
| `include_tools` | 工具白名单：空 = 全暴露；非空 = tools/list 只列匹配项、tools/call 拒绝未匹配项（协议错误，与调未知工具同形）。语法与 client 侧完全一致（纯名或尾置 `*` 前缀通配），**file-only**——不加 env/static 层，白名单是部署的编辑决定，多层只会更难审。服务端不加 exclude：白名单本身就是安全方向。非空白名单还**驱动外部工具转发**：已连接的 pool server 的工具以 `<server>.<tool>` 点号名（tool 半段把 MCP 名表外字符折成 `_`）注册进 tools/list，仅当白名单命中该名才出现——空白名单保持历史暴露面不变（opt-in）；handler 复用 synth 直通的同一道门（ValidateArgs + 无会话 policy 矩阵，只读提示须沙箱背书），冲突内建/synth 名则跳过并记 audit。同步路径：PUT 后即刻重建 + tools/list 时按目录签名自愈（pool 无回调，重连/刷新靠这里追上）。已安装的打包 runtime Skill 的 contract 工具走**同一条 opt-in 规则**：以会话内同款 `skill_*` 名（`skill_malware_sample_search`）注册，仅当白名单命中；直调门禁复用 synth 矩阵但把「沙箱背书」换成 **executor 就绪**（runtime 工具自带 bwrap 沙箱，InitSandbox 那条 bash 沙箱事实不适用），`auto` 全放、`standard` 放只读、`strict` 全拒，拒绝文案指向 `agent_submit_task`；Title 即 contract ID。无 executor 的主机（非 Linux）跳过注册、记 audit，但 serve_catalog 仍列行。另有两个管道 helper——`skill_upload_file`（base64 ≤32MiB → fileId）与 `skill_read_artifact`（artifactId → 分块 base64 + name/media_type）——归 builtin 类：存在已安装包即注册、豁免直调矩阵（与 `agent_*` 同待遇：字节搬运不是可审操作，policy 决定发生在消费 fileId/产物的 `skill_*` 工具上）、仍受白名单中间件过滤；二者以伪会话 `mcp-serve` 的 BoundSession 工作（包级 binding + MakeLock/Session，每包一个缓存会话，Call 每次全量重校验，Revoke/Delete 自动生效）。生命周期：安装/停用/删除成功后触发 `Manager.OnRuntimeSkillChange` 钩子 → resync 重建注册集，tools/list 中间件再按签名（白名单原文 + 每包 digest/revoked + executor 可用性）懒同步兜底 |

`GET /api/mcp` 另带 `serve_catalog`（layer 存在时）：前端白名单编辑器的勾选清单——`builtin`（9 个 `agent_*` 静态名/title，存在已安装 runtime 包时条件性追加 `skill_upload_file`/`skill_read_artifact` 两行）、`skills`（`SynthCatalog` 的 name/title/registered）、`servers`（`MCPCatalogs` 投影：name/state/last_error + 每个工具的 name（暴露名）/remote/title/description/read_only）、`runtime`（`SkillRuntime.Catalog()` 投影：id（`rs_` 前缀）/name/package_name/version/state（available/needs_configuration/revoked）+ 每个 contract 的 name（`skill_*` 暴露名）/contract/description/read_only/file_inputs/writes_artifacts；revoked 包保留行、tools 为空；SkillRuntime 未配置时为空数组），名字与 tools/list、include_tools 逐字一致。
| `max_concurrent` / `submit_rpm` / `max_waits` | 缺省 8/60/32，clamp [1,64]/[1,600]/[1,256]；env 或文件，**启动时定死** |

**热生效边界**：`mcpLayer` 持 `atomic.Pointer[ServeConfig]`，PUT 保存后换指针。enabled/token/policy/**include_tools** 每个请求读当前值（白名单挂在 `AddReceivingMiddleware` 上：tools/list 过滤、tools/call 拒绝，其余方法透传），PUT 即生效；三项配额在构造时定死（限流器的尺寸不是在飞调用者身下拧的旋钮），文件里的配额与进程生效值不一致时 `GET /api/mcp` 的 `server_state.restart_required=true`——enabled/token/policy/include_tools 的热改**不产生**此标记。`server_state` 全形：`{enabled, token_required, policy, source, restart_required, endpoints, include_tools}`，`source` 是 enabled/token 的生效来源（env/file/default）。

**闸的形状**：路由恒挂（有 store 时恒构造 layer），enable 闸在 `withToken` 里——关态答普通 404，与从未挂载无别（不泄露端点存在性，也不泄露 token 是否会被接受）。无 store 的旧路径（静态 `MCPOptions`）保留历史语义：空 token 回落 web token——文件通道则把「空」定义成刻意的无认证，两条语义在 gate 里分岔、注释写明。

## 14. 决议记录

| # | 问题 | 决议 |
|---|---|---|
| 1 | 用官方 SDK 还是手写协议 | Chat Completions 的 HTTP/SSE 客户端仍手写；MCP 使用官方 SDK。早期“换来零依赖”的理由已不适用于当前依赖清单和工具协议兼容层 |
| 2 | 密钥来源 | **只读环境变量**，无文件回退 |
| 3 | `Tool` 返回结构化 Details | **做**，接受它会碰 `loop.go` |
| 4 | 并行工具调用 | **做**：read/write/edit 并行 + per-path 锁（read 的锁是后补的），**bash 串行**（与 pi 不同，有意）；批次按模式**分段**，不是一个 sequential 拖垮整批 |
| 4b | 工具批次用 `errgroup` 吗 | 当前批次不采用“一处报错取消其余”的默认语义，避免缺失配对 tool_result；并发上限使用信号量。该取舍与项目第三方依赖数量无关 |
| 5 | bash 超时 | **加 120s 默认上限**（pi 不超时），无人值守场景需要天花板 |
| 6 | 取消时的进程处理 | **杀整个进程组**，不只是直接子进程 |

## 15. 雷区清单

### 协议

1. **`tool_result` 必须与 `tool_use` 一一配对且同序。** 缺一个，下一轮请求被 API 直接拒绝，而且这个会话之后 `-resume` 也用不了。取消时也必须凑齐（被取消的调用各自返回 "Operation aborted"）。`TestCancellationStillPairsEveryToolUse` 守着
2. **`Details` 绝不进 LLM 上下文**
3. **thinking 不回传**
4. **模型 id 是 `k3`**，`kimi-k3` 是别名。*（外部服务事实，写于内置目录时期；目录现完全来自用户文件，这条的价值是提醒：模型 id 以 `/models` 返回为准，不是以产品名为准——同类的窗口值教训见 §9.3 末尾，那类常量要拿端点验）*
5. **Kimi 套餐 key 只认 `api.kimi.com/coding/v1`**。*（同为外部服务事实：套餐 key 与开放平台端点不通用。内置目录已删，写进自己 `providers.json` 时仍要分清）*
6. **智谱的 input_tokens 在 `message_delta` 里**，缓存命中单独计
7. **工具 schema 里 `required` 必须是数组，不能是 JSON `null`。** Go 的 nil slice 就 marshal 成 `null`，而 moonshot/kimi 会拒收**整个请求**（`required must be an array`）—— 一个没有必填参数的工具会让这个 provider 上每一次调用都失败，不只是它自己。`object()` 因此把 nil 转成 `[]`，`TestToolSchemasMarshalAsProvidersRequire` 断言 **marshal 之后**的形状。这条踩过：`ls` 上线后 k3 全线 400，而测试全绿，因为没有一条测试看过序列化结果，而真实模型验证当时只用了容忍 `null` 的 zhipu。**多 provider 项目里，协议层至少要对最严格的那一家验证一次。**

### Go 细节

7. **不要引 `errgroup`**（见决议 4b）
8. **per-path 锁的 key 必须 canonical 化**
9. **`isCharDevice` ≠ `isTerminal`**（`/dev/null` 也是字符设备）
10. **流式重试只能在还没有任何输出时重放**（`dirty` 标志）
11. **可重置 timer 不要在 `for` 里 `defer t.Stop()`**，那会累积 defer 直到函数返回
12. **运行中不能读 `agent.Messages()`** —— data race
13. **终止事件必须阻塞发送**（`emitFinal`）
14. **杀子进程要杀整个进程组**

## 16. 测试

```bash
go test -race -count=1 ./...   # 必须全绿
go vet ./... && gofmt -l .
grep -rc '^func Test' --include='*_test.go' .   # 用例计数（按包汇总）
```

下表是 2026-08 快照，总数 836（工作区持续演进，发稿时已 837）。数字会漂移，统计命令即上文——本文不写死任何无法用一条命令复算的值。

| 包 | 用例 | 覆盖什么 |
|---|---|---|
| `agent` | 144 | 并行批次的顺序与完整性、sequential 混批、并发上限、取消时的配对、闸门六条、**并行批次串行审批且仍然并发执行**、EndReason 与退出路径、per-turn 落盘、软上限、schema 天花板（见下） |
| `tools` | 205 | 路径守卫（含只读额外根的双向断言）、`ls`、CRLF/BOM 保真、Details（含失败时）、并发编辑不丢更新、canonical key、锁不泄漏、进程组、撕裂读、workdir 守卫、声明式工具（manifest/synth）、browser、后台委派 |
| `web` | 174 | 见 `web-ui-design.md` |
| `tui` | 57 | SGR 过滤（保颜色丢光标/OSC）、截断后 sanitize、`ls` 单行渲染与长行上色、mdstream、行编辑器、usage 显示、dock 区域分裂 |
| `session` | 40 | 损坏恢复（末行残缺静默、中间行上报、分支文件不接链）、resume 元数据、List、成分核算与标定 |
| 根包 | 33 | 帮助与 Tab 补全、命令前缀解析、链式运行与判定者、checkpoints/worktrees 命令、auto 触发线与目录余量（合成目录）、`-cost-budget` 拒绝启动、JSON 模式、子进程装配 |
| `analyze` | 29 | 单会话报告、批次分布与 stalled 计数、委派花费分离、轮次分布（右删失、steering）、composition 往返 |
| `skills` | 28 | frontmatter 子集、发现规则与优先级、校验、prompt 渲染、`/skill:` 展开、read 徽标匹配、内置 skill 物化、starter 卡片 |
| `llm` | 28 | Retry-After 优先、401 只试一次、`x-should-retry` 压制、已有输出后不重放、耗尽报 attempt 数、退避可取消、Price.Cost 的子集字段算术 |
| `config` | 27 | 文件合并与按 id 替换、不读 cwd、危险 provider 形状拒绝、未知字段拒绝、缺文件与空目录的报错、subagent/followup 映射回落 |
| `worktree` | 20 | 非 git 目录拒绝、detached、git 文件校验、脏改动携带、include 只拷忽略文件、死 pid 回收 |
| `memory` | 18 | 目录创建、两层与列表上限、项目层默认关、空记忆零 prompt 成本、列表逐字稳定与 age 粗粒度 |
| `git` | 17 | 未提交路径与分类计数、非仓库/未 born/detached/上游分歧、prompt 段长度上限、rune 边界截断 |
| `diff` | 8 | 含 8 个 patch 交给真实 `git apply` 验证 |
| `wire` | 8 | event 包不依赖 web、事件名唯一定义、run_end 带 error 与 end_reason、`tool_results` 不上 wire、畸形 tool args 仍是合法 JSON |

**并行代码不跑 `-race` 等于没验证。**

### schema 预算：更大的那笔固定开销也要有人守

system prompt 有天花板（`TestSystemPromptStaysSmall`，1400 字节，刻意贴近现值——留了余量的天花板等于没人会注意它被推高），而 `agent/prompt.go` 的注释早指出 tool schema 是 system prompt 的数倍、**每一轮**都付：更大的那笔成本反而没人守。补上的守卫是 `TestToolSchemasStaySmall`（`agent/schema_test.go:32-48`）：天花板 **15000**，实测 **14900 字节 / 11 个工具**（2026-08 快照；浏览器会话形状，`browser` 的 4520 字节在内——它一直在每个 web 会话里被发送，天花板从 10600 提到 15000 的大头就是开始数这笔已经在付的钱）。

度量与请求路径走同一个函数 `llm.ToolsWireJSON`——量尺唯一不能做的事就是量到别的东西。量尺本身是 `cmd/measure-schema`（测试失败时它给 per-tool 拆分）：**注意它注册 10 个工具（终端九件 + `subagent_tasks`），不注册 `browser`**，所以它报 10381、量的是终端那一侧，与天花板测试的 11 件不同口径，两个数字不能直接相减。它的三段拆分另有信息量：散文约 **72%**、纯 JSON 结构约 **28%**——这笔钱主要花在措辞上而不是参数形状上。

核心天花板看不到 Skill 绑定的工具组（`browser` 除外），而 `agent-workflows` 一组曾因四个声明各重复一份 step schema 达到 89 KB。每个绑定组因此另有自己的天花板：`tools.TestSkillBindingGroupSchemasStaySmall`（2026-11-01 实测：agent-workflows 26119、background-tasks 7512、browser-use 7192、skill-creator 1853 字节，各留约两百字节余量），新增绑定组没有天花板时测试直接失败。运行时同一笔固定开销按分项上报（`agent.OverheadBreakdown`：提示词分段、单个工具、Skill 工具组、MCP server），上下文仪表展开「系统开销」即可看到是哪一项变大了。
