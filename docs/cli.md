# 命令行参考

> 2026-11-01：交互 TUI（REPL、行编辑、状态栏、终端 Markdown）已移除，见 [退役记录](tui-platform-direction.md)。交互使用走 `-web`；`-p` 和管道仍可一次性运行，`-mode json -p` 仍用于平台子进程，运维命令保留。不带 prompt 运行 `pogo` 会打印用法提示并以 1 退出。

## 命令行参数

`pogo -h` 有完整的中英双语帮助（参数、一次性命令、环境变量、示例）。

| 参数 | 说明 |
|---|---|
| `-p <prompt>` | 跑一个 prompt 就退出 |
| `-model <name>` | 模型 id 或别名；默认取 providers.json 的 `default`，没写就是目录第一条 |
| `-models` | 列出已知模型并退出 |
| `-C <dir>` | 工作目录，默认当前目录 |
| `-resume <last\|path>` | 恢复会话：`last` 或 `.jsonl` 文件路径 |
| `-mode <text\|json>` | 输出模式，默认 `text`：运行结束后只打印最终回答；`json` 让 stdout 每行一个事件（见下节） |
| `-quiet` | 已无作用，为兼容旧脚本保留：`text` 模式现在只打印最终回答 |
| `-max-turns <n>` | 单次运行的轮次上限，默认 300 |
| `-soft-turns <n>` | 每 n 轮插一次检查点（模型被告知进度，然后要么收尾、要么说明还差什么），而不是静默跑到上限。默认 10，0 关闭；≥ `-max-turns` 时不会触发。web 会话固定关闭 |
| `-max-runs <n>` | 让一个任务最多跨 n 次运行（仅 `-p`）：撞 continue 类上限就 fork 会话、由新 run 从 `.pogo/handoff.md` 接着做（见「链式运行」）。默认 1 即单次运行 |
| `-evaluate` | 接受结果前过一个全新的只读 evaluator（仅 `-p`）：声称完工但没通过核验的带着发现接力下一棒；run 数用尽但活核验属实的仍算成功（见「链式运行」） |
| `-retries <n>` | 每次 LLM 调用的重试次数，默认 3，`-1` 关闭 |
| `-token-budget <n>` | 本次运行的 token 上限，0 关闭。委派出去的部分也算在内。数 input+output，**不重复计入** cache_read（它是 input 的子集）和 reasoning（output 的子集） |
| `-cost-budget <f>` | 本次运行的花费上限，单位与模型声明的价格一致，0 关闭。**需要 providers.json 里给该模型声明 `price`，否则拒绝启动**（见下） |
| `-time-budget <dur>` | 本次运行的挂钟上限，0 关闭 |
| `-stagnation-threshold <n>` | 连续多少轮产生相同的工具结果就判定为打转，默认 3 |
| `-analyze-session <path>` | 分析一份 transcript 并退出（见「上下文构成」）；传目录或字面量 `sessions` 则报告其中所有 run 的轮次分布（见「轮次分布」） |
| `-analyze-format <text\|json>` | `-analyze-session` 的输出格式，默认 `text` |
| `-analyze-output <path>` | 把分析结果写到文件而不是 stdout |
| `-sessions` | 列出已保存的会话并退出 |
| `-skills` | 列出已发现的 skills 并退出 |
| `-skill <path>` | 从指定路径加载 skill（文件或目录），可重复 |
| `-no-skills` | 不扫描默认 skill 目录（`-skill` 指定的仍然加载） |
| `-project-skills` | 同时加载 `./.pogo/skills`，默认关闭 |
| `-memory` | 列出 agent 的记忆笔记后退出 |
| `-no-memory` | 不给 agent 记忆目录 |
| `-project-memory` | 同时使用 `./.pogo/memory`，默认关闭 |
| `-read-root <dir>` | 只读工具（read/ls/find/grep）额外可访问的目录，在工作目录之外（工具安装、数据挂载等），可重复；与 `POGO_READ_ROOTS` 合并生效，子代理正是通过该变量继承 |
| `-checkpoints-prune` | 清理本工作区超出保留策略的撤回点并退出 |
| `-spill-prune` | 按保留策略清理过期的大输出落盘文件（spill）并退出（见「spill 目录的自动清理」） |
| `-spill-prune-days <n>` | 临时指定 `-spill-prune` 的保留天数，不写回 prefs.json |
| `-schedule` | 管理定时任务：`-schedule <list\|add\|remove\|enable\|disable\|run-now\|logs>`（见「定时任务」） |
| `-no-citations` | 不给工具结果盖来源编号，也不要求模型在回答里标注出处 |
| `-web` | 启动浏览器界面（见下节） |
| `-listen <addr>` | `-web` 的监听地址，默认 `127.0.0.1:7777` |
| `-gate-timeout <dur>` | 工具调用等待人工审批的时长，默认 5m，超时按拒绝处理 |
| `-followups` | 回合结束后由模型异步生成追问建议 chips（仅 `-web`），默认开；关闭后只剩 starters.json 的规则匹配兜底 |
| `-context-edit <auto\|off\|n>` | prompt 超过这个大小就丢弃旧的工具输出，默认 `auto`（模型窗口的五分之四）。见「上下文清理」 |
| `-compact <off\|auto\|auto:N%\|auto:N>` | 武装自动压缩（默认 `off`，即仅手动 `/compact`）：`auto` 在运行之间阈值触发（窗口减去 max(16384, 窗口/20)），上下文溢出时压缩并原文重放该条 prompt，连续 3 次失败熔断。见 `docs/auto-compact.md` |
| `-web-dev <url>` | 把非 API 路由反代到 vite 开发服务器 |
| `-web-panel <名称=url>` | 把一个外部 web 应用挂进 dock 的面板容器，可重复；应用被反代到 `/panels/<名称>/`，与页面同源（见「浏览器界面」） |
| `-hash-password` | 从 stdin 读一个密码，输出可直接粘进 users.json 的账号行后退出（见「密码登录」） |

## 给程序读的输出：`-mode json`

`-mode text`（默认）在运行结束后把最终回答（最后一条有文本的 assistant 消息）原样打印到 stdout，不显示思考过程、工具输出，也不带颜色控制符。`-mode json` 换掉的只是这个消费者：**stdout 每行一个 JSON 事件，第一行是 session 头**，其余是 loop 事件。

```bash
pogo -mode json -p "把 README 里的死链找出来" | jq -c 'select(.type=="tool_start") | {name, args}'
```

两条规则决定了它能不能被程序信任：

**stdout 只有数据。** 所有给人读的东西——`resumed` 提示、drift 告警、retry 通知——一律走 stderr。这在 `text` 模式下也成立：`resumed` 那行以前打在 stdout，`pogo -resume last -p ... | jq` 拿到的第一行不是数据，那是个 bug，顺手修掉了。

**事件名和浏览器界面用的是同一份契约**（`wire` 包）。`turn_start` / `thinking` / `token` / `message` / `tool_args` / `tool_start` / `tool_partial` / `tool_end` / `user_message` / `run_end` 这 10 个名字只有一处定义，SSE 流和 JSONL 流不会讲两种方言。工具参数即使模型发的是坏 JSON，也会被包成字符串再出去——一次坏的工具调用不该让整条流不可解析。

两种模式都需要 `-p` 或管道输入，否则打印用法提示并以 1 退出。`-quiet` 在 `json` 模式下无效并在 stderr 告警——过滤事件会让消费者拿到一份有洞的流。

### 一次运行为什么结束：`run_end.end_reason`

`run_end` 上有两个原因字段，它们回答的**不是同一个问题**：

| 字段 | 谁的词汇 | 回答什么 |
|---|---|---|
| `stop_reason` | provider 的 | 这**一次回复**为什么结束（`end_turn` / `tool_use` / `max_tokens` / `aborted` / `error`） |
| `end_reason` | pogo 自己的 | 这**一次运行**为什么结束 |

只有后者能说出「轮次上限」这种事——那是 harness 自己的决定，OpenAI 协议里没有对应的词。所以撞 `-max-turns` 的运行以前在线上是这样的：`stop_reason` 缺席，只有一句英文散文躺在 `error` 里。

```bash
pogo -mode json -p "..." | jq -r 'select(.type=="run_end") | .end_reason'
```

十个取值，以及**看到它该做什么**：

| `end_reason` | 含义 | 建议动作 |
|---|---|---|
| `completed` | 模型答完了，没有待发的插话 | 完成 |
| `turn_limit` | 撞 `-max-turns` | 继续：transcript 完好，起一个新 run 接着做 |
| `token_budget` | 撞 `-token-budget` | 继续 |
| `cost_budget` | 撞 `-cost-budget` | 继续 |
| `time_budget` | 撞 `-time-budget` | 继续 |
| `max_tokens` | 模型自己的输出上限把回复截断了 | 继续（是在接一句话，不是接一个任务） |
| `stagnation` | 连续 N 轮相同的工具结果 | **介入**：什么都不改地重跑会在同一轮再次触发 |
| `context_overflow` | prompt 过大，而强制清理已经无可清 | **介入**：能帮上的是 `/compact`，那是另一个机制 |
| `transport_error` | 模型调用失败（网络、鉴权、5xx） | 停：这不是任务的问题 |
| `aborted` | ctx 被取消（Ctrl-C、run 超时、关服务） | 停：有人故意停的 |

后四个是这个字段存在的理由。「凡是不等于 completed 就重试」这个最自然的读法对它们四个都是错的，而且错法各不相同：两个会原地打转，两个会在一个没有清除的条件上反复烧钱。分类表在 `agent/endreason.go`，一条测试要求每个取值都在表里登记，所以加新取值时必须回答「驱动脚本该拿它怎么办」，不会静默继承一个默认值。

**没登记的取值一律按「停」处理。** 谨慎的方向是不对称的：一个无人值守的驱动因为读不懂而停下，代价是一个卡住的任务；继续下去，代价是无上界的运行次数。

`stop_reason` 一个字没改，也没被这个字段取代——两个都发。`end_reason` 是**追加**的，改这个字段之前写好的消费者看不出区别。

## 一次性命令

终端交互模式的斜杠命令随 TUI 一起移除。`-p` 里仍有两种斜杠写法：

| 写法 | 说明 |
|---|---|
| `-resume <path> -p /compact` | 用一份摘要替换该会话的对话。花一次模型调用；完整记录仍留在会话文件里。见「压缩」 |
| `-p "/skill:<name> [args]"` | 把某个 skill 的完整说明注入 prompt |

原来的其他命令词（`/model`、`/models`、`/usage`、`/skills`、`/auto`、`/strict`、`/standard`、`/help`、`/exit`、`/quit`）作为 `-p` 的 prompt 时会被拒绝并以 1 退出，不会当成普通文本发给模型。对应的能力用参数：`-model`、`-models`、`-skills`、`-h`；审批模式只在 `-web` 下有。

## 重试

408 / 409 / 429 / 5xx / 连接错误会自动重试，默认 3 次（`llm/retry.go` 的白名单；408/409 在名单上是因为一次请求超时或瞬时冲突不是调用方的错）。参数取自官方 SDK 的实现：

- 优先用服务端的 `Retry-After-Ms` / `Retry-After`，没有才本地退避
- 本地退避 `500ms × 2^n`，上限 8 秒，再**减去**最多 25% 抖动（保证不超上限）
- 其余 4xx 不重试（400/401/403/404 是请求本身的问题，重试只白耗额度）
- 退避期间按 Ctrl-C 立即返回，不会干等

重试会打到 stderr，限流时不会看起来像卡死：

```
… retry 1/3 in 1s: 429 Too Many Requests: rate_limit_error: concurrency limit reached
```

**一个流式场景的细节**：一旦有内容已经输出到屏幕，中途断流就不会重放请求了，否则会重复输出。通用 SDK 在 HTTP 层重试，做不到这一点。


---

<a id="english"></a>

# Command-Line Reference

## Command-Line Flags

> 2026-11-01: the interactive TUI (REPL, line editor, status dock, terminal markdown) has been removed; see the [retirement record](tui-platform-direction.md). Use `-web` for interactive work. `-p` and piped input still run one-shot, `-mode json -p` still serves platform subprocesses, and the operations flags remain. Running `pogo` without a prompt prints a usage hint and exits 1.

`pogo -h` prints the full bilingual help (flags, one-shot commands, environment variables, examples).

| Flag | Description |
|---|---|
| `-p <prompt>` | Run a single prompt and exit |
| `-model <name>` | Model id or alias; default is the providers.json `default`, or the first catalog entry when none is named |
| `-models` | List known models and exit |
| `-C <dir>` | Working directory; default is the current directory |
| `-resume <last\|path>` | Resume a session: `last` or a path to a `.jsonl` file |
| `-mode <text\|json>` | Output mode, default `text`: only the final answer, printed when the run ends; `json` makes stdout emit one event per line (see next section) |
| `-quiet` | No effect, kept so existing scripts still run: `text` mode now prints only the final answer |
| `-max-turns <n>` | Turn cap for a single run; default 300 |
| `-soft-turns <n>` | Insert a checkpoint every n turns (the model is told its progress, then either wraps up or states what's left) instead of running silently to the cap. Default 10, 0 disables; never triggers when ≥ `-max-turns`. Always off for web sessions |
| `-max-runs <n>` | Let a task span up to n runs (`-p` only): on hitting a continue-class limit, fork the session and let a new run pick up from `.pogo/handoff.md` (see "Chained runs"). Default 1, i.e. a single run |
| `-evaluate` | Run a fresh read-only evaluator before accepting the result (`-p` only): claims of "done" that fail verification hand off findings to the next leg; if runs are exhausted but live verifications still pass, it still counts as success (see "Chained runs") |
| `-retries <n>` | Retry count per LLM call; default 3, `-1` disables |
| `-token-budget <n>` | Token cap for this run; 0 disables. Delegated work counts too. Counts input+output; **does not double-count** cache_read (a subset of input) or reasoning (a subset of output) |
| `-cost-budget <f>` | Spend cap for this run, in the same unit as the model's declared price; 0 disables. **Requires `price` to be declared for that model in providers.json, otherwise startup is refused** (see below) |
| `-time-budget <dur>` | Wall-clock cap for this run; 0 disables |
| `-stagnation-threshold <n>` | How many consecutive turns producing identical tool results count as spinning; default 3 |
| `-analyze-session <path>` | Analyze a transcript and exit (see "Context composition"); pass a directory or the literal `sessions` to report the turn distribution across all its runs (see "Turn distribution") |
| `-analyze-format <text\|json>` | Output format for `-analyze-session`; default `text` |
| `-analyze-output <path>` | Write the analysis to a file instead of stdout |
| `-sessions` | List saved sessions and exit |
| `-skills` | List discovered skills and exit |
| `-skill <path>` | Load a skill from the given path (file or directory); repeatable |
| `-no-skills` | Do not scan default skill directories (`-skill` ones still load) |
| `-project-skills` | Also load `./.pogo/skills`; off by default |
| `-memory` | Print the agent's memory notes and exit |
| `-no-memory` | Do not give the agent a memory directory |
| `-project-memory` | Also use `./.pogo/memory`; off by default |
| `-read-root <dir>` | Extra directory the read-only tools (read/ls/find/grep) may reach, outside the working directory (tool installs, data mounts); repeatable; composed with `POGO_READ_ROOTS`, which is how subagent children inherit the roots |
| `-checkpoints-prune` | Discard this workspace's rewind points beyond the retention policy; exit |
| `-spill-prune` | Discard spilled tool output older than the retention policy; exit (see "Automatic spill cleanup") |
| `-spill-prune-days <n>` | Override `-spill-prune`'s retention in days without saving it to prefs.json |
| `-no-citations` | Do not stamp tool results with source labels, and do not ask the model to cite them |
| `-web` | Launch the browser UI (see section below) |
| `-listen <addr>` | Listen address for `-web`; default `127.0.0.1:7777` |
| `-gate-timeout <dur>` | How long a tool call waits for human approval; default 5m, timeout is treated as denied |
| `-followups` | Generate follow-up suggestion chips with an asynchronous model call after a run ends (`-web` only); default on. Off leaves the starters.json rule matching as the fallback |
| `-context-edit <auto\|off\|n>` | Discard older tool output once the prompt exceeds this size; default `auto` (four-fifths of the model window). See "Context cleaning" |
| `-compact <off\|auto\|auto:N%\|auto:N>` | Arm automatic compaction (default `off`: manual `/compact` only): `auto` triggers between runs at the window minus max(16384, a twentieth of the window), compacts and replays the same prompt once on context overflow, and trips a breaker after 3 consecutive failures. See `docs/auto-compact.md` |
| `-web-dev <url>` | Reverse-proxy non-API routes to a vite dev server |
| `-web-panel <name=url>` | Show an external web app in the dock's panel container (repeatable); reverse-proxied at `/panels/<name>/`, same origin as the page |
| `-hash-password` | Read a password from stdin, print a users.json account line, and exit (see "Password login") |

## JSON Output: `-mode json`

`-mode text` (the default) prints the final answer (the last assistant message with text) to stdout as-is once the run ends: no thinking, no tool output, no colour codes. `-mode json` only swaps that consumer: **stdout emits one JSON event per line, the first line being a session header**, the rest being loop events.

```bash
pogo -mode json -p "find dead links in the README" | jq -c 'select(.type=="tool_start") | {name, args}'
```

Two rules decide whether this can be trusted by programs:

**stdout carries only data.** Everything meant for humans — the `resumed` notice, drift warnings, retry notifications — goes to stderr. This holds in `text` mode too: the `resumed` line used to print on stdout, so `pogo -resume last -p ... | jq` got a non-data first line; that was a bug, fixed along the way.

**Event names share one contract with the browser UI** (the `wire` package). `turn_start` / `thinking` / `token` / `message` / `tool_args` / `tool_start` / `tool_partial` / `tool_end` / `user_message` / `run_end` — these 10 names have a single definition; the SSE stream and the JSONL stream do not speak two dialects. Tool arguments, even when the model emits broken JSON, are wrapped as a string before going out — one bad tool call should not make the whole stream unparseable.

Both modes require `-p` or piped input; otherwise pogo prints a usage hint and exits 1. `-quiet` is a no-op in `json` mode and warns on stderr — filtering events would hand consumers a stream with holes.

### Why a run ends: `run_end.end_reason`

`run_end` carries two reason fields, and they **do not answer the same question**:

| Field | Whose vocabulary | What it answers |
|---|---|---|
| `stop_reason` | the provider's | why **this one reply** ended (`end_turn` / `tool_use` / `max_tokens` / `aborted` / `error`) |
| `end_reason` | pogo's own | why **this one run** ended |

Only the latter can express "turn limit" — that is a harness-level decision with no counterpart in the OpenAI protocol. So a run hitting `-max-turns` used to look like this in production: `stop_reason` was absent, and only a line of English prose sat in `error`.

```bash
pogo -mode json -p "..." | jq -r 'select(.type=="run_end") | .end_reason'
```

Ten values, and **what to do when you see each**:

| `end_reason` | Meaning | Suggested action |
|---|---|---|
| `completed` | the model finished, no pending injection | done |
| `turn_limit` | hit `-max-turns` | continue: transcript is intact, start a new run to pick up |
| `token_budget` | hit `-token-budget` | continue |
| `cost_budget` | hit `-cost-budget` | continue |
| `time_budget` | hit `-time-budget` | continue |
| `max_tokens` | the model's own output cap truncated the reply | continue (you are continuing a sentence, not a task) |
| `stagnation` | N consecutive turns with identical tool results | **intervene**: re-running with nothing changed will trigger it again at the same turn |
| `context_overflow` | prompt too large, and forced cleaning has nothing left to clean | **intervene**: what helps is `/compact`, which is a separate mechanism |
| `transport_error` | the model call failed (network, auth, 5xx) | stop: this is not a task problem |
| `aborted` | ctx cancelled (Ctrl-C, run timeout, server shutdown) | stop: someone stopped it on purpose |

The last four are the reason this field exists. The most natural reading — "retry on anything not equal to completed" — is wrong for all four, and wrong in different ways: two spin in place, two burn money repeatedly on a condition that was never cleared. The classification table lives in `agent/endreason.go`, and one test requires every value to be registered in the table, so adding a new value forces you to answer "what should a driving script do with it" — it cannot silently inherit a default.

**Unregistered values are always treated as "stop".** The cautious direction is asymmetric: an unattended driver that stops because it cannot read a value costs one stuck task; continuing costs an unbounded number of runs.

`stop_reason` is unchanged and is not replaced by this field — both are emitted. `end_reason` is **additive**; consumers written before this field existed see no difference.

## One-Shot Commands

The slash commands of the interactive terminal mode were removed with the TUI. Two slash forms still work in `-p`:

| Form | Description |
|---|---|
| `-resume <path> -p /compact` | replace the session's conversation with a summary. Costs one model call; the full record stays in the session file. See "Compaction" |
| `-p "/skill:<name> [args]"` | inject a skill's full instructions into the prompt |

The other former command words (`/model`, `/models`, `/usage`, `/skills`, `/auto`, `/strict`, `/standard`, `/help`, `/exit`, `/quit`) are refused as a `-p` prompt with exit 1 rather than sent to the model as text. Their capabilities are flags: `-model`, `-models`, `-skills`, `-h`; approval modes exist only under `-web`.

## Retry

408 / 409 / 429 / 5xx / connection errors are retried automatically, default 3 times (the whitelist in `llm/retry.go`; 408/409 are on it because a request timeout or a transient conflict is not the caller's fault). Parameters taken from the official SDK's implementation:

- Prefer the server's `Retry-After-Ms` / `Retry-After`; fall back to local backoff only if absent
- Local backoff `500ms × 2^n`, capped at 8 seconds, then **minus** up to 25% jitter (guarantees it stays under the cap)
- Other 4xx are not retried (400/401/403/404 are problems with the request itself; retrying only burns quota)
- Ctrl-C during backoff returns immediately, no dry waiting

Retries are logged to stderr, so rate limiting doesn't look like a hang:

```
… retry 1/3 in 1s: 429 Too Many Requests: rate_limit_error: concurrency limit reached
```

**One streaming detail**: once content has already been output to the screen, a mid-stream break does not replay the request, otherwise output would be duplicated. Generic SDKs retry at the HTTP layer and cannot do this.

