# Pogo 文档入口

本轮核对日期：2026-09-29。范围为当前工作区，包含未提交代码；不将文档更新视为代码修复或部署。

2026-09-30 功能调整：[Git/worktree 退役与迁移](git-retirement.md)。早期设计文档中的 worktree 和 Git 管理描述仅作历史记录。

2026-10-08 浏览器方向：[Docker Web 实时接管](browser-web-live.md)为主路线；Electron 保留作可选实验。实现、候选测试与正式部署分别记录，不复用桌面手感作为 Web 验收。

## 当前使用与边界

| 需要了解 | 阅读 |
| --- | --- |
| 项目当前能做什么、哪些有验证、哪些有缺口 | [项目现状](project-status.md) |
| 平台化后如何处理 TUI 和 CLI | [TUI 退役分析](tui-platform-direction.md)，仅建议，未实施 |
| 身份、账号、会话归属 | [多用户说明](multiuser.md) |
| 工作空间共用资料、目录树、快捷方式与 Agent 读取 | [信息库](information-library.md) |
| 文件、Shell、Skill 沙箱的实际边界 | [安全边界](security.md) |
| Agent 对话建流、画布、版本、本人确认与运行记录 | [Agent 内部工作流](agent-internal-workflows.md) |
| 后台执行、定时计划与条件监听 | [后台任务](background-tasks.md)、[定时任务](automation.md) |
| Skill ZIP、私有运行声明、安装和迁移 | [Skill 包](skill-packages-2026-09-24.md)、[组合语义](agent-runtime-composition-2026-09-24.md)、[运行规范](skill-runtime-v1/README.md) |
| 模型和工具调用协议兼容 | [配置](configuration.md)、[工具协议兼容](tool-protocol-compatibility-2026-09-29.md) |
| MCP 目录刷新、监控、配置导出 | [刷新](mcp-client-refresh.md)、[监控](mcp-monitoring.md)、[导出](mcp-export.md) |
| CLI、子进程与定时任务 | [CLI](cli.md)、[子 Agent](subagent.md)、[自动化](automation.md) |
| 对话前端与上下文 | [Web](web-ui.md)、[会话](sessions.md)、[自动压缩](auto-compact.md) |
| 单一浏览器执行路径、旧入口退役与数据迁移 | [原生浏览器单一路径](browser-native-only.md) |
| Docker Web 主路线、连续画面、拖动与输入、引用和候选验收边界 | [Docker Web 浏览器实时接管](browser-web-live.md) |
| 可选 Electron 实验：本地原生网页、完整 DevTools 与历史验收 | [Pogo Desktop 实验记录（2026-10-08）](desktop-browser-release.md)，不作为 Docker Web 的使用前提或当前部署状态 |
| 无网站反代的浏览器执行、画面和人工接管 | [原生浏览器运行时](native-browser-runtime.md) |
| 通用 CDP、多目标 iframe/worker、Network/Console、调试恢复与旧方式融合 | [浏览器调试协议与迁移](browser-devtools.md)，已实现并完成本机执行层验证 |
| 通用浏览器取证、断点恢复、单次提交的独立任务验收 | [Browser Use v2 验收](../browser-runtime/eval/browser-use-v2/README.md)，受控网站对照与真实模型运行入口 |
| 内置浏览器与完整 DevTools 的竞品调研、选型和迁移 | [DevTools 调研报告（2026-10-06）](browser-devtools-research-2026-10-06.md)，完整目标调研，当前实现见上项 |
| 接手 browser-use 改造、代码入口、部署与后续优化 | [Browser-use 交接文档（2026-10-01）](browser-use-handoff-2026-10-01.md) |
| 真实模型的浏览器任务成功率与失败回放 | [浏览器任务评测 v1](browser-evaluation.md) |

## 历史记录怎么读

`agent-studio-*-2026-09-23.md`、`agent-platform-research-2026-09-22.md`、各 phase validation 和设计调研文档记录当时的设计或验收。保留其中的时间、镜像和验证结果用于追溯；不能据此认定当前工作区或当前服务已经通过同一验收。

[harness-design.md](harness-design.md) 保留核心机制及设计演进，不能代替平台功能清单。`text-tool-call-compat-2026-09-29.md` 是问题提案，实际实现以工具协议兼容文档及源码为准。

维护规则：能力变化同步更新主题文档和现状表；缺陷修复后附对应验证再关闭限制；部署记录必须独立记载镜像标识及验收范围。不要用旧测试数量、旧依赖数量或示例配置推断当前状态。
