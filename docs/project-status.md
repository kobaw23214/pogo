# Pogo 项目现状

核查日期：2026-09-29。依据是当前工作区源码、同日检查日志与本机只读部署查询。包含未提交实现；不是发布说明或全量生产验收。

2026-09-30 代码调整：Git 管理、自动 worktree 和 Git 上下文注入已退役；编辑委派改为当前目录同步执行、跨进程串行；文件快照与撤回保留。见 [迁移说明](git-retirement.md)。下文历史测试记录未因此自动更新。

2026-10-01 代码调整：Skill 运行器按 `POGO_PLATFORM` 部署声明与实际架构核对，支持 Linux/amd64、Linux/arm64 的对应系统库和 ELF 程序；安装、技能列表和选择预览统一报告平台不兼容。现有 MalAgent 1.3.4 仍是 amd64 包，不会自动转换为 ARM64。下文历史测试记录与线上部署状态不因此自动更新。

2026-10-02 测试基线：分支 `feat/browser-use-native` 的 `2f58f40` 上前端全量与 `go test -race ./...` 均通过（macOS arm64，见[验证记录](#本次验证记录)）。其中 `a04c4d5` 修改了工作流运行的生产代码（run.json 先落盘；Runner 运行前复制记录，消除数据竞争），尚未部署；其余为测试与 CI 配置修正。CI-01 已处理，新增 STU-01。

2026-10-02 后续正确性修复（尚未部署）：`3974d98` 修复 STU-01；`cec98ac` 修复 WF-01/WF-04（包括取消与保存失败的并发边界），UI 单独显示保存失败与停止后续调度，不覆盖最后保存的状态。发布执行器见 `0115bbe` 与 [RELEASE.md](../../w_deploy/RELEASE.md)；详细验证和 CI 状态见根 [HANDOFF.md](../../HANDOFF.md) 第 12 节。以下早期探针描述用于保留问题依据，不能再作为当前代码仍有这些缺陷的结论。

## 能力与实现边界

2026-10-01 浏览器升级：新增可配置的 Playwright/Chromium 托管运行时，直接访问原始网站，接入会话隔离、模型截图、前端画面与人工接管。未配置时仍使用旧面板。第一阶段能力、测试入口与未完成边界见 [原生浏览器运行时](native-browser-runtime.md)，不代表已部署或达到 SOTA。

| 模块 | 当前实现 | 边界 |
| --- | --- | --- |
| 核心运行 | `agent/` 事件循环、工具审批接口、预算、结束原因、上下文清理和可选摘要压缩 | 模型执行经 `llm/` 的 Chat Completions 客户端；真实端点需单独验收 |
| Agent Studio | 管理员维护 Agent；模型白名单、普通 Skill、工具型 Skill、MCP 和内置工具选择；会话绑定定义快照 | 管理不是普通用户任意自助创建；已有会话与新编辑定义分离 |
| 内置工具 | 文件、Shell、浏览器、todo、subagent、可选图像分析等 | `tools/builtin.go` 有不可移除的核心工具，显式空选不代表零工具 |
| Skill Runtime | ZIP 安装、契约和摘要验证、平台兼容性预检、服务端绑定、会话锁、工具/资源、停用与删除检查、workspaceData 物化 | 工具型 Skill 执行要求 Linux/amd64 或 Linux/arm64、匹配架构的 Skill 包与可用 bubblewrap；管理可用不等于执行可用；Pogo 运行声明是私有扩展 |
| MCP | 上游 stdio/HTTP/SSE、用户凭据覆盖、按 Agent 选择、普通会话运行前刷新；实例及 Agent 对外 MCP | MCP 机器凭据与 Web 用户身份不同，不能互换会话权限 |
| 工作流 | DAG、条件边、并行、人工节点、动态分派、发布/回滚、定时和恢复记录 | 仍有下列执行语义缺口，不能宣称与普通会话能力完全等价 |
| 身份和存储 | 本地账号/固定 Token、SQLite 会话归属、JSONL 对话、Studio/工作流文件注册表 | 旧业务 JWT 委托通道已移除；用户目录划分不等于租户进程隔离 |
| 运行入口 | Web、一次性 JSON/text CLI、交互 TUI、子进程与运维命令 | TUI 尚未退役；平台仍依赖无界面 CLI |

主要入口：[Studio](../web/studio.go)、[会话装配](../web/manager.go)、[Skill 装配](../web/skill_runtime.go)、[MCP 刷新](../web/mcprefresh.go)、[工作流装配](../web/workflowrun.go)、[运行结束原因](../agent/endreason.go)。

## 已确认问题与待验收边界

| 编号 | 现状 | 证据和影响 |
| --- | --- | --- |
| WF-01 | 已修复，待部署（`cec98ac`） | 节点与 dispatch 消费完整 Agent 终态；模型错误、取消和预算耗尽不再成功，旧产物不能掩盖失败。模型错误与全部 EndReason 回归通过 |
| WF-02 | 工作流另建 Agent，没有接入普通会话完整的 Skill Runtime 装配 | 静态对照 `web/manager.go` 的 `runtimeSkillTools` 与 `web/workflowrun.go` 的 `executor/dispatchTool.execute`；缺少会话式绑定/锁、运行工具与数据物化路径，不能保证同一 Agent 在两种入口能力相同 |
| WF-03 | 发布冻结工作流定义，没有固定引用的 Agent 定义 | overlay 探针发布后修改 Agent instructions，已发布工作流采用了新指令；节点执行时读取 `studio.get`，人工等待恢复也会读取当时的定义 |
| WF-04 | 已修复，待部署（`cec98ac`） | 状态写入返回错误贯穿 Runner；node_start 保存成功才执行，首次失败停调度、取消并等待在途任务，以最后保存状态展示独立 persistence_error；慢取消不再原地改未保存状态。进程内隔离与重启恢复避免重放结果未知节点 |
| ISO-01 | 会话/API 归属与进程隔离强度不同 | 普通会话 Bash 未装配严格 `CommandScope`；终端经 `startTerminal` 传 nil scope；通用沙箱有整个文件系统只读视图及 HOME 写入范围。未做真实用户数据越权实验，跨用户 Shell/终端隔离待专门验收 |
| CI-01 | 入口与本地门禁已补齐，真实 GitLab 执行待接入（`a01e9d0`） | 根 `.gitlab-ci.yml` 复用原生 Linux Docker 验证入口，UI/worker/eval/Go 与部署各组已实际通过；13 关键集成要求真实 PASS。先前失败与分组复验边界保留在 `.ci-artifacts/validation-summary.json`。本轮未注册 Runner/触发 GitLab pipeline，不能将本地通过写成线上 CI 通过 |
| STU-01 | 已修复，待部署（`3974d98`） | Session 同一锁内检查运行状态、保存 binding、更新 client/schema/cfg 并发布事件。拒绝/写失败/并发及重启一致性回归通过 |

WF-02、WF-03、ISO-01 仍待修复或专项验收；STU-01、WF-01、WF-04 已有回归修复，尚未部署。CI 配置与实际 Runner 执行仍需区分。

Studio 默认工作区为用户根下 `agents/<id>`；同一用户的 memory、journal 和 checkpoint 仍按用户共享。工作流各节点共享本次 run 的 workspace，通过文件传递产物。两种范围不得混为一谈。

## 本次验证记录

### 2026-10-02 测试基线

对象为 `feat/browser-use-native` 的 `2f58f40`：从提交导出到临时目录执行，不含工作区未提交改动。环境为 macOS 26.5.2 arm64、Go 1.26.5、Node 24.16.0，步骤与 CI 同序。

| 检查 | 结果 |
| --- | --- |
| `npm run check`（vue-tsc） | 通过 |
| `npm test`（Vitest 全量） | 74 个测试文件、3627 个用例通过 |
| `npm run build` | 构建到临时目录，与已提交的 `web/ui/dist` 逐文件一致 |
| `go build ./...`、`go vet ./...` | 通过 |
| `go test -race -count=1 ./...` | 30 个包通过，0 失败，0 个 DATA RACE；测试后临时目录无残留 |
| `CGO_ENABLED=0 GOOS=linux go vet ./...`（arm64、amd64） | 通过；只证明 Linux 专属测试可编译，未在 Linux 上运行 |

本轮修复的失败：

- 4 条首页路由前端断言仍按 `/` 跳转 `/manage` 的旧行为编写（`443c7f1`）。
- `TestWorkflowRunEndToEnd`（`main` 上也失败）：`wfRunSink.Publish` 先改实时记录、后写 run.json。`-race` 另报 Runner 改写 web 层正在编码的实时记录（`Human` 指针与 map），是既有数据竞争（`a04c4d5`，生产代码）。
- 31 个 Skill 相关测试在清理阶段失败：安装后的包目录被封存为 0o555，非 root 用户无法删除（`3e00f8f`，新增 `internal/testfs`，生产封存行为不变）。
- `TestStudioWebConversationUsesNativeSurfaceAndPinsModel` 在 `-race` 下 10 次失败 9 次，`main` 上同样复现：用例在回合结束前切换模型（`40ef898`）。排查中发现 STU-01。

未执行：在 Linux 上运行测试（含 Linux 专属集成测试与 bubblewrap）、Windows、线上 CI、镜像构建与部署。

### 2026-09-29

下表来自 2026-09-29 同一分析会话中的检查；后续文档编辑没有重跑业务测试。文档核对期间还检测到 4 个前端源码文件出现并行修改（AgentView、StarterCards、starterCards 文案和 AgentEditorDialog），本轮未编辑或回退它们；表内前端通过结果仅对应测试时的版本，不覆盖之后的改动。

| 检查 | 结果 |
| --- | --- |
| `vue-tsc --noEmit` | 通过 |
| Vitest 全量 | 71 个测试文件、3454 个用例通过；日志仍有 workflow i18n key 警告，测试通过不等于浏览器文案已逐项验收 |
| Windows `go test ./... -count=1 -timeout 90s` | 退出码 1；51 个测试函数报告失败，`tools`、`web` 包超时 |
| Go 部分包 | `agent`、`llm`、`config`、`catalog`、`identity`、`mcpclient`、`skillruntime`、`workflow` 等通过 |
| 隔离行为探针 | WF-01、WF-03 复现；测试源码在临时目录，使用 overlay，未加入业务仓库 |
| Linux race、镜像构建、真实模型业务链路 | 本次未执行，不沿用历史结果宣称当前通过 |
| 本机部署只读查询 | `pogo-base` 为 running，标签 `pogo:pub-note-20260929`；首页 HTTP 200；前端入口资源名与本地 dist 一致 |

运行镜像 ID：`sha256:6bd5d042d1505530cb5c142ec6d82d3f40c3dd54b7a1e44d583621595d2d385f`。这是当时的快照，不代表后续一直相同；前端资源名一致不证明最新后端代码全部进入镜像。

Windows 失败中存在 Unix 权限位、符号链接权限、Shell 脚本、路径与 CRLF 假设；不能全部归因于业务缺陷，也不能全部忽略。完整运行型 Skill 以 Linux 容器为验收环境。

## 下一次发布的执行顺序

1. 记录代码 revision、工作区补丁与构建输入；核对 CI 引用的脚本实际存在。
2. 验收已修复的 WF-01/WF-04，并继续处理 WF-02 能力装配与 WF-03 定义固定。
3. 明确运行快照固定哪些版本、停用和撤权如何即时检查；补齐 Agent 修改后、人工恢复后的回归。
4. 在隔离 Linux 环境执行前端 check/test/build、Go build/vet/race；验证 Bash、终端、子进程的跨用户边界。
5. 使用独立数据目录验收一个工具型 Skill 和一个 MCP 工作流，核对真实工具调用、错误分支、产物、取消及重启恢复。
6. 构建候选镜像，备份状态数据，再替换目标容器并验收登录、旧会话、新会话和工作流。记录镜像 ID、数据格式与兼容回退办法；存在注册表迁移时，不将切回旧镜像视为足够的回退。

TUI 方向见 [退役分析](tui-platform-direction.md)。它能减少界面维护，但不会自动修复以上运行一致性和隔离问题。
