# 平台化后的 TUI 与 CLI 取舍

日期：2026-09-29 提出建议；2026-11-01 已实施，实施记录见下一节。下文「建议」及之后的内容保留为当时的分析。

## 实施记录（2026-11-01）

删除的部分：

- `tui/` 整个包（10 个生产文件 2,983 行、6 个测试文件 1,092 行）。
- `main.go` 里的 REPL 主循环、斜杠命令处理 `command()`、dock 状态栏调用和 `tilde()`；`help.go` 的交互命令列表与前缀补全 `resolveCommand`；`help_test.go` 里只验证这两项的测试。
- 服务端启动日志、运维命令和诊断输出里的颜色控制符与 OSC 8 链接，统一改为纯文本。

保留并调整的部分：

- `-mode json -p` 路径不变：子 Agent（前台、后台、retained）和普通定时任务照常以 `-mode json -C … -p …` 启动子进程。JSON 模式不再构造终端 Renderer。
- `-mode text`（默认）改由 `textmode.go` 输出：运行结束后只把最终回答（最后一条有文本的 assistant 消息）原样写到 stdout，并保证末尾换行；退出码规则与 JSON 模式相同。
- `-quiet` 仍可传入但不再有作用，避免旧脚本因未知参数失败。
- 没有 prompt 时（裸 `pogo`、空管道、`/dev/null`），两种模式都在解析模型和创建会话之前报错，打印 `-web` / `-p` / 管道 / `-h` 的用法提示，退出码 1。
- `-p` 里的斜杠命令：`/compact`（配合 `-resume`）和 `/skill:<name>` 继续可用；原交互命令词仍被拒绝而不是当作文本发给模型，命令表从 `tui.Commands` 移到 `help.go` 的 `slashCommands`。
- `Summarize`、`HumanCtx`、`IsCharDevice` 的少量逻辑并入 `main.go`；`summarize` 仍会去掉控制字符，防止 provider 错误或 skill 描述里的转义序列到达终端。

验证：除 `browser-runtime/eval/runs/` 下已有编译错误的评测快照外，50 个包构建通过；入口包 `go vet` 在 darwin 和 linux 通过，并能交叉编译到 linux 与 windows；入口包测试通过；`tools/` 中启动真实子进程的 subagent 测试和 `schedule/` 测试通过。用隔离的 HOME 和假模型服务实测了文本与 JSON 两种 `-p`、管道输入、无 prompt 提示、`-h`、`-models`、`-web` 启动（日志无转义字节）和定时任务 `run-now`。

## 建议

如果产品主入口确定为 Web/Agent 平台，建议停止扩展面向用户的交互 TUI，并在解除依赖后退役。保留平台需要的无界面执行、JSON 事件协议、取消和运维入口。浏览器 PTY 终端作为独立产品能力另行决定。

| 部分 | 当前职责 | 建议 |
| --- | --- | --- |
| 交互 TUI / REPL | 行编辑、Tab 补全、dock/spinner、终端 Markdown、颜色与终端重绘 | 可退役，先移走共享依赖 |
| `-mode json -p` | 子 Agent、普通定时任务和脚本的进程执行协议 | 保留；除非调用方已迁移到经过验收的新执行入口 |
| CLI 运维命令 | 账号密码摘要、Token 生成、模型/配置诊断、会话分析和清理、包工具 | 按能力保留，不依赖交互界面 |
| Web 浏览器终端 | `web/terminal.go`、WebSocket、PTY 和前端 xterm | 与 TUI 分开；优先明确权限/隔离/开关，再决定是否保留 |
| 核心运行 | `agent/llm/tools/session/wire`、审批转发、进程生命周期 | 保留并统一调用，不归入 TUI 删除范围 |

## 代码耦合证据

- `tui/` 有 10 个生产 Go 文件，共约 2983 行；6 个测试文件约 1092 行。行数包含注释与空行，不代表二进制大小或性能收益。
- `main.go` 在选择 JSON consumer 前仍构造 `tui.NewRenderer`，JSON 模式也调用 `tui.SetPlain`；后面的 REPL、状态行和命令处理继续依赖该包。
- `serve.go` 的 Web 启动日志引用 TUI 的颜色及链接函数；`help.go` 使用 `tui.Commands` 生成帮助；`cli_observe.go`、`schedule.go`、`worktrees.go` 也引用格式化函数。
- `tools/subagent.go` 用 `os.Executable()` 再次启动当前二进制，参数含 `-mode json -C ... -p ...`，并读取 JSONL 回执。Web 中的 subagent 依赖这条链路。
- `schedule/exec.go` 的普通 prompt 任务同样启动当前二进制。工作流定时任务走进程内执行，是另一种路径，不能用它推断所有 CLI 子进程都已取消。
- `jsonmode.go` 和 `wire/` 定义机器消费的事件协议；`gatefd.go` 与 `web/subagent.go` 承担审批转发，均不是终端界面代码。

因此，直接删除 `tui/` 会导致入口包无法编译；连带删除 CLI 会破坏平台已有的子 Agent 和普通定时任务。

## 收益与代价

退役交互 TUI 可以减少终端适配、屏幕重绘、Markdown 显示和双入口交互测试的维护量，让产品体验集中在 Web。当前没有测量二进制体积或启动性能收益，不据行数作估算。

它不会消除浏览器终端使用的 `creack/pty`、`nhooyr.io/websocket` 或前端 xterm，也不会解决工作流错误吞掉、Skill Runtime 装配缺失或跨用户进程隔离。上一轮 TUI 包测试通过，Windows 全量失败主要集中在其他模块；不能把那些失败计作删除 TUI 的收益。

代价是本地交互式使用者失去 REPL。若需要保留脚本兼容，可以继续提供一次性 plain text 输出，但不再维护 dock、补全和终端 Markdown。是否保留这种简洁输出应在退役变更中明确。

## 推荐实施顺序与验收

1. **明确删除范围。** 只列交互 REPL、行编辑、dock、终端 Markdown 和相关专属帮助/测试；记录需要继续兼容的参数和退出码。
2. **解除格式化耦合。** 将仍被服务器和运维入口使用的必要格式化函数移到小型独立模块，或直接使用普通日志；把运维帮助与交互命令表分离。
3. **保证无界面路径独立。** JSON 执行无需构造终端 Renderer；保留 stdout 纯 JSONL、stderr 诊断、事件顺序、取消、退出码、会话逐轮持久化和审批/浏览器管道转发。
4. **再删除交互实现。** 移除 REPL 路径、TUI 目录及只验证被删除界面的测试，更新 README 和 CLI 文档。当前不必为了删除界面立即增加多个独立发布包。
5. **验证平台依赖。** Linux build/vet/race；Web 启动、普通会话、前后台 subagent、取消及审批；普通定时任务和工作流定时任务分别验证；JSON 协议及运维命令回归。
6. **候选发布与回退。** 使用独立镜像和测试数据验证；这次界面退役不应附带数据格式迁移，保留旧镜像及配置。回退恢复旧二进制/镜像即可的前提必须在发布前核实。

优先级建议：先处理 [项目现状](project-status.md) 中的运行正确性与边界问题，再做 TUI 退役。两项可以分成独立变更，避免把平台功能修复和交互界面删除混成一次无法定位回归的重构。
