<a id="top"></a>

# pogo

[![CI](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml/badge.svg)](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kobaw23214/pogo)](https://goreportcard.com/report/github.com/kobaw23214/pogo)
[![License](https://img.shields.io/github/license/kobaw23214/pogo?color=blue)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)

**简体中文** | [English](#english)

用 Go 与 Vue 构建的 Agent 工作台。当前包括 Agent Studio、多用户会话、模型与工具配置、Skill Runtime、双向 MCP 接入，以及正在整合的工作流编排；最初的 CLI 保留一次性和无界面运行，终端界面（TUI）已于 2026-11-01 移除。

**状态核对：2026-09-29，包含当前工作区未提交实现。** 代码能力、测试通过和实际部署是不同状态；完整清单、已确认缺口和验证范围见 [项目现状](docs/project-status.md)。普通对话与工作流尚未完全共用运行装配，不能将工作流发布快照或用户目录划分解读为完整的运行版本固定或租户进程隔离。

Go 版本以 [go.mod](go.mod) 为准（本次为 1.26.5）。直接依赖包括 PTY、WebSocket、MCP SDK、JSON Schema 和 SQLite；不再使用“只有两个依赖”或“固定九个工具”的历史描述。

2026-09-30：Git 管理界面、自动 worktree 和仓库上下文注入已退役；保留文件变更与内部快照。详见 [迁移说明](docs/git-retirement.md)。

## 平台入口与边界

| 入口 | 当前用途 |
| --- | --- |
| Web / Agent Studio | 管理员配置 Agent、模型、Skill、MCP；用户进入会话 |
| Workflow | 引用 Agent 编排任务，支持人工节点、动态分派、发布和定时；已知缺口见现状文档 |
| MCP | 连接上游工具，也能对外提供 Pogo/Agent 能力 |
| 无界面 CLI | 子 Agent、普通定时任务、JSON 事件流及运维命令仍在使用 |
| TUI / REPL | 已于 2026-11-01 移除（见[退役记录](docs/tui-platform-direction.md)）；交互使用走 Web，`-p` 仍可一次性运行并打印最终回答 |

工具型 Skill 执行支持 Linux/amd64、Linux/arm64 与 bubblewrap，Skill 包内程序必须匹配当前运行架构。部署变量 `POGO_PLATFORM` 同时选择镜像平台并声明预期架构，运行器会与实际架构核对；直接启动未设置该变量时自动检测。当前 MalAgent 1.3.4 包仅提供 amd64 运行时。`Dockerfile` 从 DiscoverAgent 父目录构建，`compose.yml` 依赖部署者准备的模型目录、账号文件和环境文件；仓库内的部署示例不等于开箱即用的生产配置。启动与验证应按 [项目现状](docs/project-status.md) 的发布检查执行。

## 文档

细读之前，本 README 只保留上手所需的内容；每个主题的完整说明在 `docs/` 下：

| 文档 | 内容 |
|---|---|
| [docs/README.md](docs/README.md) | 当前文档入口与历史记录的阅读范围 |
| [docs/project-status.md](docs/project-status.md) | 平台现状、已确认问题、验证与发布边界 |
| [docs/tui-platform-direction.md](docs/tui-platform-direction.md) | TUI 退役的分析与实施记录、无界面 CLI 保留范围 |
| [docs/cli.md](docs/cli.md) | 命令行参数、`-mode json`、一次性命令、重试 |
| [docs/tools.md](docs/tools.md) | 内置工具说明；实际目录随入口和配置变化 |
| [docs/subagent.md](docs/subagent.md) | subagent：只读并行、可写串行与直接文件交付 |
| [docs/skills.md](docs/skills.md) | skills、声明式工具 `tools.json`、跨会话记忆 |
| [docs/office-managed-transforms.md](docs/office-managed-transforms.md) | Office 受控 Python 修改、文件版本记录与恢复；本轮仅代码实现，尚未编译或测试 |
| [docs/web-ui.md](docs/web-ui.md) | 浏览器界面：SSE、审批、token 与密码登录、面板 |
| [docs/sessions.md](docs/sessions.md) | 会话文件、上下文清理（context editing）、`/compact` |
| [docs/automation.md](docs/automation.md) | 定时任务（schedule） |
| [docs/workflow.md](docs/workflow.md) | 工作流编排：定义格式、运行语义、状态机、API |
| [docs/configuration.md](docs/configuration.md) | provider 与模型目录 `providers.json` |
| [docs/security.md](docs/security.md) | 安全边界 |
| [docs/harness-design.md](docs/harness-design.md) | harness 设计文档：每个机制的设计决策 |
| [docs/web-ui-design.md](docs/web-ui-design.md) | Web UI 设计文档：事件协议、审批闸门、rewind |
| [docs/web-ui-frontend.md](docs/web-ui-frontend.md) | 前端实现说明 |
| [docs/theme-research.md](docs/theme-research.md) | 主题调研 |

## 联系方式

- 邮箱：<a href="mailto:wycc2077@qq.com">wycc2077@qq.com</a>
- 微信：扫码加作者交流

<p align="center">
  <img src="assets/wechat-qr.jpg" alt="作者微信二维码" width="240" />
</p>

## 安装

需要 Go 1.26 以上。

```bash
git clone https://github.com/kobaw23214/pogo.git
go build -o pogo .
```

想装到 `PATH` 里（确保目标目录已在 PATH 中）：

```bash
cd web/ui && npm run build && cd ../.. && go build -o ~/.local/bin/pogo .
```

## 配置

三件事：provider/model 目录、API key、会话目录。

**1. provider 和模型目录：`~/.pogo/providers.json`（必须先写）。** 这个 fork **不编译进任何 provider 和模型**（`config/catalog.go` 里两张表都是空的），所以没有这个文件 pogo **拒绝启动**，报错里会带一个最小示例。本地模型服务、公司网关、镜像、订阅端点，都在这个文件里声明；给 `-cost-budget` 用的模型价格也在这里。完整字段和规则见[「模型 → provider 和模型目录」](docs/configuration.md#provider-和模型目录pogoprovidersjson)。

```json
{
  "providers": {
    "zhipu": { "base_url": "https://open.bigmodel.cn/api/coding/paas/v4", "key_env": "ZHIPU_API_KEY" }
  },
  "models": [
    { "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576, "max_tokens": 16384 }
  ]
}
```

文件只从 home 目录读（`POGO_CONFIG` 可改位置），理由见[「为什么只读 home」](docs/configuration.md#为什么只读-home不读项目目录)。几条校验都会直接拒绝启动：`key_env` 只收环境变量**名**（粘贴 key 本身会被拒，且错误不回显那个值）、远端必须 https（loopback/内网例外）、不认识的字段直接报错。

**2. API key（环境变量）**。密钥只从环境变量读，**不读任何配置文件**；读哪个变量名由文件里每个 provider 的 `key_env` 决定——`KIMI_API_KEY`、`ZHIPU_API_KEY` 只是常用名，pogo 本身不认识任何写死的变量名。`key_env` 留空表示该端点不需要凭据（本地模型服务），客户端连 `Authorization` 头都不发。

```bash
export ZHIPU_API_KEY='...'        # 你的 providers.json 里 key_env 写的那个名字
```

想让它永久生效，推荐单独放一个文件而不是直接写进 `.zshrc`（`.zshrc` 经常被提交到 dotfiles 仓库或贴给别人看）：

```bash
mkdir -p ~/.pogo
umask 077
cat > ~/.pogo/env.sh <<'EOF'
export ZHIPU_API_KEY='...'
EOF
chmod 600 ~/.pogo/env.sh

echo '[ -f "$HOME/.pogo/env.sh" ] && source "$HOME/.pogo/env.sh"' >> ~/.zshrc
```

哪个 provider 的 `key_env` 没配，它的模型会在 `-models` 里标成 `not set` 并点名缺哪个变量。

```bash
$POGO_CONFIG / ~/.pogo/providers.json
```

**3. 会话目录**。默认在 `~/.pogo/sessions/`，用 `POGO_SESSION_DIR` 改；`-resume last` 找的就是这里最新的文件。详见[「会话」](docs/sessions.md#会话)。

## 快速开始

```bash
pogo -p "统计当前目录有多少行 Go 代码"
```

交互使用请启动浏览器界面：

```bash
pogo -web
```

终端交互模式（REPL）已移除。不带 prompt 运行 `pogo` 只会打印用法提示并以 1 退出。默认模型来自你的 providers.json：文件的 `default`，没写就是目录第一条。

## CLI 的一次性运行

CLI 只剩一次性运行：有 prompt 就跑，跑完退出。prompt 可以来自 `-p`，也可以来自管道：

```bash
pogo -p "这个项目的入口在哪"

# 管道内容当作数据，-p 当作指令
cat meeting-notes.md | pogo -p "提取待办事项，标出负责人"
git diff | pogo -p "review 这个 diff，只说有问题的地方"

# 只有管道、没有 -p，管道内容本身就是 prompt
echo "解释 Go 的 defer 执行时机" | pogo
```

管道内容放在指令**前面**，中间空一行。指令在最后，模型对它的注意力最高。

默认的 `-mode text` 只在运行结束后把**最终回答**打印到 stdout，不显示思考过程和工具输出，也不带颜色控制符；诊断信息走 stderr。需要完整事件流时用 `-mode json`，子 Agent 和定时任务走的就是这条路。

## 项目结构

```
llm/       协议层：中立类型 + Client 接口、手写 SSE 客户端、重试（retry.go）、价格（price.go）
agent/     loop.go 核心循环、gate.go 审批钩子、event.go 事件、prompt.go system prompt、
           endreason.go 结束分类、persist.go 逐轮落盘、softcap.go 软上限、todofinish.go run 收尾的清单关闭
tools/     read / ls / find / grep / write / edit / bash / subagent / todo + browser.go（web 页面操作）
           + subagent_bg.go（后台委派）+ manifest.go / synth.go（tools.json 合成）+ 截断 + per-path 锁 + 路径守卫
skills/    skill 的发现、frontmatter 解析、prompt 渲染、/skill: 展开、embedded.go 内置物化、
           starters.go 空态引导、builtin/ 内置的三个 skill
diff/      零依赖 Myers 行级 diff（给人看的 / 给 git apply 的 / +3 -1 徽标）
session/   JSONL 树，resume 与 fork；composition.go 上下文构成
config/    provider 目录与密钥解析（file-only，无内置）
prefs/     界面设置存储（spill 保留天数；读不出时清扫暂停而非退回默认）
memory/    跨会话记忆目录
analyze/   -analyze-session 的统计与轮次分布
workflow/  工作流编排：Definition 校验与依赖图、Runner 并行调度（条件边/人工节点/动态分派；节点 = 引用 Agent 定义的 fresh agent，产物走文件）
wire/      事件契约（SSE 流与 JSONL 流共用的一份定义）
web/       HTTP + SSE：事件日志与快照、run 生命周期、审批闸门、策略、密码登录、浏览器面板与终端
web/ui/    Vue3 + TS 前端，构建产物 embed 进二进制
cmd/       measure-schema（schema 字节测量）、test-overhead、toolprobe/mcp-probe（工具协议与 MCP 探测）；
           pogo-malagent-provider / pogo-yara-provider / tools/knowledge-provider 是恶意代码分析
           skill（malagent 仓库，1.3.4 已自包含发布）的 provider 源码样例——演示如何基于
           skillruntime 契约为 pogo 开发领域插件，平台内核不依赖它们；全量打包器已退役
           （重建方式见仓库根 skill-packages/README.md）
assets/    README 用的图片
main.go    CLI：参数、一次性运行（-p / 管道）、会话与模型装配
serve.go   -web 启动装配（内置 skill 物化、面板、密码登录）
chain.go   -max-runs 链式运行
checkpoints.go  撤回点快照与保留策略
spillprune.go  -spill-prune：spill 目录的手动清理（自动清扫在 web/ 的常驻进程里）
gatefd.go  子 agent 的审批闸门（逐次转发给父裁决）
jsonmode.go  事件流的 JSON 消费者（-mode json）
textmode.go  事件流的纯文本消费者（-mode text，只打印最终回答）
subagent.go  子进程的 CLI 编排
help.go    -h 的中英帮助，以及 -p 里斜杠命令的筛查
```

`agent.Run()` 返回 `<-chan Event`，CLI 的两种输出只是事件的消费者。`web/` 是第二个消费者，它的存在没有改动 loop 的形状——唯一碰到 loop 的是工具返回值加了结构化通道和一个可选的审批钩子。

## 有意不做的东西

独立 plan mode 尚未实现。MCP 客户端和服务端均已实现，分别在 `mcpclient/` 与 `web/`；服务端开关和连接设置见 MCP 管理文档。自动摘要压缩默认关闭，显式 `-compact auto` 可启用，见 [自动压缩](docs/auto-compact.md)。平台级缺口统一记录在 [项目现状](docs/project-status.md)，下列条目主要描述原有 CLI 体验边界。

（子 agent 和 todos 原本也在这份名单上，都已经做了。终端 TUI 曾经做过，已于 2026-11-01 移除。**上下文清理**也做了，但它不是 compaction——见下方两者的分工线。**摘要压缩现在有了 `/compact`**，默认只在你主动要求时发生——理由见「压缩」。）

已知缺口（`docs/harness-design.md` §13 是同一份清单的设计侧版本，带上了每一条为什么还没做）：
- 只有 `bash` 和 `subagent` 有增量输出，只在 Web 和 `-mode json` 里可见
- diff 只按增删上色，不做语法着色（两层颜色会互相打架）
- 自动摘要压缩**默认关闭**：手动 `/compact` 之外，`-compact auto` 才武装自动路径（运行之间阈值触发、溢出时压缩并重放该条 prompt、连续 3 次失败熔断；见 `docs/auto-compact.md`）。不开它时行为与从前完全一致——被对话文本撑大的会话仍会在你不压缩时撞墙，强制清理如实报告帮不上，把 provider 的错误原样返回
- `-p` 没有审批闸门（所有工具直接执行），也不能插话；审批模式和插话只在 `-web` 下有
- 会话按轮落盘（一轮的 assistant 消息和它的工具结果一起写入才算数），进程被 kill 最多丢掉正在进行的那一轮，已完成的轮次都在
- `-p` 一轮失败就 `exit 1`

一处与 pi 有意不同：`read` / `write` / `edit` 并行执行（写有 per-path 锁），**`bash` 串行**。`go build` 之类的命令本身已经内部并行，再并行只是抢 build cache 锁；两条命令写同一个输出文件会损坏且无从检测；而且闸门下 N 个并行 bash 会同时弹 N 张卡。

## 开发

```bash
# Go 侧
go build ./...
go vet ./...
go test -race ./...   # 并行执行、事件扇出、闸门都是并发代码，不跑 race 等于没验证。
                      # 用例数自己数：grep -rc '^func Test' --include='*_test.go' .（2026-08 快照：836 个）
gofmt -l .

# 前端
cd web/ui
npm ci
npm test              # vitest，含一段真实 SSE 录制的回放。
                      # 用例数自己数：grep -rE '(^|[^A-Za-z_.])it\(' src（2026-08 快照：约 470 个，36 个文件）
npm run check         # vue-tsc 类型检查
npm run build         # 产物进 dist/，被 embed 进二进制
```

依赖清单以 `go.mod` 和 `web/ui/package.json` 为准。**`web/ui/dist` 是被 embed 的，所以它进了版本库**：改完前端记得重新 build，否则二进制里还是旧页面。上述为检查命令，不代表本次全部通过；Windows 与 Linux 的验证结果分别记录在 [项目现状](docs/project-status.md)。

### 窗口值是实测过的，不是抄一次就算

`ContextWindow` 不只是显示用：`-context-edit auto` 取它的五分之四，浏览器的上下文占用条按它的比例变黄变红。一个报小了的窗口会让 pogo **清得太早**（白付缓存未命中和重读），并且在窗口还剩一大半的时候就建议你开新会话。**现在目录在你的 providers.json 里，这几个数由你声明**——所以下面这段内置目录时期的记录留作方法示例：这类常量该拿你实际用的那个端点验，而不是从文档抄一次就信。

（历史记录）glm-5.2 原来在内置目录里写的是 200,000，**错了五倍**。厂商自己的文档和模型卡都写 1M 上下文，而拿 pogo 实际用的那个端点（coding plan，不是通用 API）探了一次：**一个 400,013 token 的 prompt 被接受并计费，`finish_reason` 是 `stop`**——所以它肯定不止 200,000。

`MaxTokens` 保持 16384 不动：那是**输出**上限而不是窗口。模型允许 131,072，但一个 coding agent 一轮吐出那么多就是跑偏了；而且在 kimi 上输出上限和 prompt 记在同一个限额里（「prompt tokens + max_tokens exceeds the model specification」）。

同一次探针把 kimi 的数字**确认为完全正确**：`kimi-for-coding` 对同一个 body 回的是「Your request exceeded model token limit: 262144」。一个 provider 的目录值是对的、另一个错了五倍——这就是为什么这类常量该被重新量，而不是被信任。

这条不写测试：一个常量的值没法靠单测来辩护，能辩护它的是上面那次探针和这段记录。

---

<!-- ============================ ENGLISH ============================ -->


---

<a id="english"></a>

# pogo

[简体中文](#top) | **English**

A Go/Vue Agent workbench with Agent Studio, local-account sessions, model/tool configuration, Skill Runtime, MCP clients and servers, and workflow orchestration under integration. The original CLI remains for one-shot and headless runs; its terminal UI was removed on 2026-11-01.

**Reviewed on 2026-09-29 against the working tree, including uncommitted code.** See [project status](docs/project-status.md) for confirmed gaps, tests and deployment boundaries, and the [documentation index](docs/README.md) for historical records. Workflow execution does not yet share the complete conversation runtime; directory separation is not proof of tenant process isolation.

Use `go.mod` for the required Go version (1.26.5 at this review) and dependencies. Direct dependencies now include PTY, WebSocket, the MCP SDK, JSON Schema and SQLite. The old two-dependency comparison no longer describes this fork. The interactive TUI was [retired](docs/tui-platform-direction.md) on 2026-11-01; headless CLI execution remains, because platform subprocesses require it.

## Documentation

This README keeps only what you need to get started; each topic lives under `docs/`:

| Doc | Contents |
|---|---|
| [docs/cli.md](docs/cli.md) | Command-line flags, `-mode json`, one-shot commands, retries |
| [docs/tools.md](docs/tools.md) | Built-in tools; the active registry depends on entrypoint and configuration |
| [docs/subagent.md](docs/subagent.md) | Subagents: parallel exploration and serialized workspace edits |
| [docs/skills.md](docs/skills.md) | skills, declarative `tools.json` tools, cross-session memory |
| [docs/web-ui.md](docs/web-ui.md) | Browser UI: SSE, approvals, token & password login, panels |
| [docs/sessions.md](docs/sessions.md) | Session files, context editing, `/compact` |
| [docs/automation.md](docs/automation.md) | Scheduled tasks |
| [docs/workflow.md](docs/workflow.md) | Workflow orchestration: definition format, run semantics, state machine, API |
| [docs/configuration.md](docs/configuration.md) | Providers and the model catalog, `providers.json` |
| [docs/security.md](docs/security.md) | Security boundary |
| [docs/harness-design.md](docs/harness-design.md) | Harness design doc: the decision behind every mechanism |
| [docs/web-ui-design.md](docs/web-ui-design.md) | Web UI design doc: event protocol, gates, rewind |
| [docs/web-ui-frontend.md](docs/web-ui-frontend.md) | Frontend implementation notes |
| [docs/theme-research.md](docs/theme-research.md) | Theme research |

## Contact

- Email: <a href="mailto:wycc2077@qq.com">wycc2077@qq.com</a>
- WeChat: scan to add the author

<p align="center">
  <img src="assets/wechat-qr.jpg" alt="Author WeChat QR code" width="240" />
</p>

## Installation

Requires Go 1.26+.

```bash
git clone https://github.com/kobaw23214/pogo.git
go build -o pogo .
```

To install it into your `PATH` (make sure the target directory is on your `PATH`):

```bash
cd web/ui && npm run build && cd ../.. && go build -o ~/.local/bin/pogo .
```

## Configuration

Three things: the provider/model catalog, API keys, and the session directory.

**1. Provider and model catalog: `~/.pogo/providers.json` (required — write it first).** This fork **compiles in no providers or models** (both tables in `config/catalog.go` are empty), so without this file pogo **refuses to start**, and the error prints a minimal example. A local model server, a corporate gateway, a mirror, a subscription endpoint — all are declared in this file; so are the model prices `-cost-budget` reads. Full field reference and rules in [Models → The provider and model catalog](docs/configuration.md#the-provider-and-model-catalog-pogoprovidersjson).

```json
{
  "providers": {
    "zhipu": { "base_url": "https://open.bigmodel.cn/api/coding/paas/v4", "key_env": "ZHIPU_API_KEY" }
  },
  "models": [
    { "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576, "max_tokens": 16384 }
  ]
}
```

The file is read only from the home directory (`POGO_CONFIG` relocates it); see [Why it only reads home](docs/configuration.md#why-it-only-reads-home-not-the-project-dir). Each of these validations refuses to start: `key_env` takes only an environment variable **name** (a pasted key is refused, and the error does not echo the value), remote endpoints must be https (loopback/private addresses excepted), and unknown fields are an error.

**2. API key (environment variables).** Keys are read **only** from environment variables — **no config file is read**. Which variable is read is decided by each provider's `key_env` in your file: `KIMI_API_KEY` and `ZHIPU_API_KEY` are merely conventional names; pogo itself knows no hardcoded variable name. An empty `key_env` means the endpoint needs no credential (a local model server), and the client then sends no `Authorization` header at all.

```bash
export ZHIPU_API_KEY='...'        # whatever name key_env declares in your providers.json
```

To make this permanent, prefer a dedicated file over editing `.zshrc` directly (`.zshrc` is often committed to a dotfiles repo or shared with others):

```bash
mkdir -p ~/.pogo
umask 077
cat > ~/.pogo/env.sh <<'EOF'
export ZHIPU_API_KEY='...'
EOF
chmod 600 ~/.pogo/env.sh

echo '[ -f "$HOME/.pogo/env.sh" ] && source "$HOME/.pogo/env.sh"' >> ~/.zshrc
```

A provider whose `key_env` is not set has its models shown as `not set` under `-models`, with the missing variable named.

```bash
$POGO_CONFIG / ~/.pogo/providers.json
```

**3. Session directory.** Defaults to `~/.pogo/sessions/`, overridable with `POGO_SESSION_DIR`; `-resume last` looks here for the newest file. See [Sessions](docs/sessions.md#sessions).

## Quick Start

```bash
pogo -p "count how many lines of Go code are in the current directory"
```

For interactive use, serve the browser UI:

```bash
pogo -web
```

The interactive terminal mode (REPL) has been removed. Running `pogo` without a prompt prints a usage hint and exits 1. The default model comes from your providers.json: the file's `default`, or the first catalog entry when none is named.

## One-Shot Runs

The CLI only runs one-shot: anything with a prompt runs once and exits. The prompt can come from `-p` or from a pipe:

```bash
pogo -p "where is the entry point of this project"

# piped content as data, -p as the instruction
cat meeting-notes.md | pogo -p "extract action items and call out owners"
git diff | pogo -p "review this diff, only flag problems"

# pipe with no -p: the piped content itself is the prompt
echo "explain when Go's defer runs" | pogo
```

The piped content is placed **before** the instruction, separated by a blank line. The instruction comes last, where the model pays the most attention to it.

The default `-mode text` prints only the **final answer** to stdout once the run ends: no thinking, no tool output, no colour codes. Diagnostics go to stderr. Use `-mode json` for the full event stream; subagents and scheduled jobs use that path.

## Project Structure

```
llm/       protocol layer: neutral types + Client interface, hand-written SSE client, retry (retry.go), pricing (price.go)
agent/     loop.go core loop, gate.go approval hook, event.go events, prompt.go system prompt,
           endreason.go end classification, persist.go per-turn persistence, softcap.go soft cap, todofinish.go task-list close-out at run end
tools/     read / ls / find / grep / write / edit / bash / subagent / todo + browser.go (web page operations)
           + subagent_bg.go (background delegation) + manifest.go / synth.go (tools.json synthesis) + truncation + per-path lock + path guard
skills/    skill discovery, frontmatter parsing, prompt rendering, /skill: expansion, embedded.go built-in materialization,
           starters.go empty-state starters, builtin/ the three built-in skills
diff/      zero-dependency Myers line diff (human-readable / git-apply-able / +3 -1 badge)
session/   JSONL tree, resume and fork; composition.go context composition
config/    provider catalog and key resolution (file-only, nothing built in)
prefs/     UI preferences store (spill retention days; unreadable prefs pause the sweep rather than fall back)
memory/    cross-session memory directory
analyze/   -analyze-session statistics and turn distribution
workflow/  workflow orchestration: Definition validation and the dependency graph, the Runner's parallel scheduler (conditional edges / human nodes / dynamic dispatch; a node is a fresh agent assembled from an Agent definition, artifacts are files)
wire/      the event contract (one definition shared by the SSE stream and the JSONL stream)
web/       HTTP + SSE: event log and snapshot, run lifecycle, approval gate, policy, password login, browser pane and terminal
web/ui/    Vue3 + TS front end, build output embedded into the binary
cmd/       measure-schema (schema byte measurement), test-overhead
assets/    images for the README
main.go    CLI: flags, one-shot runs (-p / pipe), session and model assembly
serve.go   -web startup assembly (built-in skill materialization, panels, password login)
chain.go   -max-runs chained runs
checkpoints.go  rewind-point snapshots and retention
spillprune.go  -spill-prune: manual spill-directory cleanup (the timed sweep lives in web/'s long-lived process)
gatefd.go  a subagent's approval gate (forwards each call to the parent for adjudication)
jsonmode.go  the JSON consumer of the event stream (-mode json)
textmode.go  the plain-text consumer of the event stream (-mode text, final answer only)
subagent.go  CLI orchestration of child processes
help.go    -h bilingual help, and the screening of slash commands in -p
```

`agent.Run()` returns `<-chan Event`; the CLI's two outputs are just consumers of events. `web/` is the second consumer, and its existence did not change the shape of the loop — the only thing that touched the loop is that tool return values gained a structured channel and an optional approval hook.

## Things Deliberately Not Done

A separate plan mode has not been implemented. Both MCP client and server support exist (`mcpclient/` and `web/`). Automatic summary compaction remains opt-in through `-compact auto`; see `docs/auto-compact.md`. Platform gaps live in [project status](docs/project-status.md); the list below primarily describes the original CLI experience.

(Subagents and todos used to be on this list; both have been done. A terminal TUI was built and was removed on 2026-11-01. **Context cleaning is also done**, but it is not compaction — see the dividing line between the two below. **Summary compaction now exists as `/compact`**, and by default happens only when you ask — see "Compaction" for why.)

Known gaps (`docs/harness-design.md` §13 is the design-side version of the same list, with a "why not yet" on each):
- Only `bash` and `subagent` have incremental output, visible in the web UI and in `-mode json`
- Diff colors only additions/deletions, no syntax highlighting (two layers of color fight each other)
- Automatic summary compaction is **off by default**: besides manual `/compact`, only `-compact auto` arms the automatic paths (a between-runs threshold, a compact-and-replay retry on context overflow, a 3-failure circuit breaker; see `docs/auto-compact.md`). Without it the behaviour is exactly as before — a session bloated by conversation text still hits the wall if you never compact, and forced cleaning honestly reports it cannot help and returns the provider's error as-is
- `-p` has no approval gate (all tools execute directly) and cannot be interjected; approval modes and interjection exist only under `-web`
- Sessions persist per turn (a turn's assistant message and its tool results are written together to count), so a killed process loses at most the in-flight turn; completed turns are all there
- `-p` exits 1 on a failed turn

One intentional difference from pi: `read` / `write` / `edit` run in parallel (writes have a per-path lock), **`bash` is serial**. Commands like `go build` are already internally parallel, and parallelizing further only contends for the build cache lock; two commands writing the same output file would corrupt it undetectably; and under the gate, N parallel bash calls would pop N cards at once.

## Development

```bash
# Go side
go build ./...
go vet ./...
go test -race ./...   # Parallel execution, event fan-out, and the gate are concurrent code; not running
                      # -race means not validating. Count the cases yourself:
                      # grep -rc '^func Test' --include='*_test.go' . (2026-08 snapshot: 836)
gofmt -l .

# Front end
cd web/ui
npm ci
npm test              # vitest, including a replay of a real SSE recording. Count the cases yourself:
                      # grep -rE '(^|[^A-Za-z_.])it\(' src (2026-08 snapshot: ~470, across 36 files)
npm run check         # vue-tsc type check
npm run build         # output into dist/, embedded into the binary
```

Dependency manifests are authoritative. **`web/ui/dist` is embedded, so it is checked into the repo**: after editing the front end, remember to rebuild, otherwise the binary still carries the old page. These commands are a checklist, not a passing result; Windows and Linux verification must be reported separately in [project status](docs/project-status.md).

### The window values are measured, not copied once

`ContextWindow` is not just for display: `-context-edit auto` takes four-fifths of it, and the browser's context usage bar turns yellow/red in proportion to it. A window reported too small would make pogo **clean too early** (paying for cache misses and rereads for nothing) and suggest you start a new session while more than half the window is still free. **The catalog now lives in your providers.json, so these numbers are yours to declare** — which is why the record below, from the built-in-catalog era, is kept as an example of method: constants like this should be validated against the endpoint you actually use, not copied from a doc once and trusted.

(Historical record) glm-5.2 used to be listed in the built-in catalog as 200,000, **off by five times**. The vendor's own docs and model card both say 1M context, and probing the endpoint pogo actually uses (the coding plan, not the general API) once: **a 400,013-token prompt was accepted and billed, with `finish_reason` of `stop`** — so it is definitely more than 200,000.

`MaxTokens` stays at 16384 unchanged: that is the **output** cap, not the window. The model allows 131,072, but a coding agent producing that much in one turn has gone off the rails; and on kimi the output cap and the prompt share one quota ("prompt tokens + max_tokens exceeds the model specification").

The same probe **confirmed kimi's numbers as exactly correct**: `kimi-for-coding` replied to the same body with "Your request exceeded model token limit: 262144." One provider's catalog value is right, the other's off by five times — which is why this kind of constant should be re-measured rather than trusted.

No test for this one: a constant's value cannot be defended by a unit test; what defends it is the probe above and this record.
