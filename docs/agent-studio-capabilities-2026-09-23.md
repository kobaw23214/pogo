# Agent Studio：模型、MCP 与 Skill 选择

> 历史实现与验收记录（2026-09-23）。下文镜像、测试数量、账号/会话数量与兼容行为仅对应当时版本。2026-09-29 当前实现及后续 Skill 注册表迁移边界见 [项目现状](project-status.md) 与 [Skill 包说明](skill-packages-2026-09-24.md)。

用户流程：先在工作空间管理模型、MCP 与 Skill，再创建 Agent，勾选需要的能力并设置默认模型，创建后进入真实 Pogo 对话。

## 页面结构

| 一级模块 | 用途 |
| --- | --- |
| 我的 Agents | 创建、编辑、进入对话、发布链接与 iframe |
| 模型配置 | 配置模型供应商和可用模型 |
| MCP 管理 | 管理连接、查看连接状态和调用记录 |
| Skill 管理 | 查看已安装 Skill、阅读说明、上传 Skill 包、查看使用记录 |
| 账号与安全 | 沿用 Pogo 的账号和认证管理 |
| 设置 | 外观、语言、存储、沙箱、附件等通用设置 |

全屏创建弹框继续包含模板、名称、简介、指令和实时预览，能力选择分成三个明确的组：

- **模型**：可多选，必须至少选择一个，并指定一个默认模型；默认模型必须在已选列表中。Kimi K3 和 GLM-5.3 沿用最高推理档 `max`。
- **MCP**：按服务器连接多选；显示协议、状态与工具数量。空选代表不启用 MCP 连接，不自动使用全局连接。
- **Skill**：从已安装库多选；显示名称、说明及工具数量。空选代表不启用 Skill，不自动加载整个库。

管理目录加载失败时显示错误与重试，不将失败伪装成“没有可选项”。修改模板不清空已选能力。已经选择但失效的资源保留提示，不能静默替换为其他资源。

## 配置与对话语义

每次创建会话记录该 Agent 配置版本。模型可在这次会话允许的模型之间切换；后台同样检查允许列表。后续编辑 Agent 影响新会话，现有会话继续使用创建时的选择。

为兼容本轮之前的 Agent，缺省的 Skill/MCP 字段保留旧的共享能力语义；新建和重新保存的 Agent 使用明确的勾选列表。旧单模型会话继续只允许原模型。原有记录无需清空或重建。

Skill 的选择用于指令目录、Skill 补全、手动调用、可读 Skill 资源和声明工具。子任务收到同一组选定的 Skill 与可读目录，并关闭自动发现，空选也不会在子进程中重新加载整个库。MCP 选择用于实际工具注册和新一轮运行开始时的目录刷新。正在执行的一轮保持既有工具快照。对话中的 Skill 面板显示该会话的选择，安装管理集中在 Studio。

这些选择管理 Agent 的模型和扩展能力。文件工作区与终端仍遵循现有账号、工作区和沙箱规则；本轮不新增每个 Agent 独立的操作系统环境。发布页与 iframe 继续沿用 Pogo 身份认证，不显示返回 Studio 或管理设置入口。

## 验收与部署

2026-09-23 10:41（Asia/Shanghai）已部署至 `pogo-base`，访问 [Agent Studio](http://127.0.0.1:19777/manage)。

| 验收层次 | 实际结果 |
| --- | --- |
| 前端类型与回归 | `vue-tsc` 通过；Vitest 63 个测试文件、3227 个用例通过 |
| 最终 Linux 回归 | 使用 `golang:1.26-bookworm`，`--init`，`go test -buildvcs=false -race ./web ./tools -count=1` 通过，耗时分别 94.211s / 35.671s |
| 相邻模块回归 | `skills`、`mcpclient`、`session` 的 Linux race 测试通过；此后未修改这些包 |
| 最终构建 | Docker 内前端生产构建和 Linux Go 编译通过 |
| API 验收 | A/B 不同 Skill 与 MCP、空选、旧配置继承、越界拒绝、允许模型切换、旧/新会话版本、重启恢复全部通过 |
| 真实模型调用 | 隔离实例中的 Kimi K3 实际调用已选 `studio_alpha_marker` 与 `mcp_alpha_read_marker`，两条工具回执均成功，并返回分别预置的验证标记 |
| 浏览器验收 | 实际上传 Skill 包，创建包含两种模型、一个 MCP、两个 Skill 的 Agent，进入真实 Pogo 会话；编辑回显、默认模型切换、会话能力展示通过 |
| 适配与最终镜像 | 390×844 无横向溢出、底部操作可用；最终镜像再次确认配置与会话恢复，正式环境核对模块、创建选项和旧会话 |

测试数据在独立 19779 实例中创建，未写入正式 Agent 列表。最终测试实例和 5179 开发服务已关闭，验收卷保留。工具进程回收测试最初因测试容器缺少 `init` 失败；对齐正式 Compose 的 `init: true` 后通过，最终全套 Web/工具检查也通过。Windows 的旧 MCP 测试遇到 `EvalSymlinks: Access is denied`，未将 Windows 全量测试记为通过；部署等效验证使用上述 Linux 结果。

正式服务保持 3 个现有 Agent 和 10 个账号可见会话；数据卷内 12 个历史 JSONL 文件逐一比对哈希不变，Agent 定义与会话绑定不变。账号库、部署凭据文件、模型供应商配置、身份配置哈希不变。19778 的委托服务容器 ID、启动时间及运行状态不变。

## 页面截图

正式环境：

![Studio 管理首页](assets/agent-studio-capabilities/studio-production.png)

![Skill 管理](assets/agent-studio-capabilities/skills-production.png)

隔离验收环境中的多选配置（图中的测试 Skill/MCP 不属于正式资源）：

![模型和 MCP 多选](assets/agent-studio-capabilities/capability-editor-desktop.png)

![390px 移动布局](assets/agent-studio-capabilities/mobile-models-mcp.png)

## 部署记录与回退

- 正式镜像：`pogo:studio-capabilities-20260923`。
- Docker 镜像标识：`sha256:fff0d5ba5ecc273c19358672e4df58b4a4a4fdccaf3def3a986f4bbdeb2ff425`。
- 上一版镜像保留为：`pogo:rollback-before-studio-capabilities-20260923`。
- 停机后创建的数据卷快照：`pogo_studio_capabilities_backup_20260923_104106`。
- 完整日志、API 回执、前后哈希与部署脚本：`C:/Users/14777/.codex/agent-worker/maintenance/pogo-studio-capabilities-20260923/`。

回退前先确认没有正在运行的对话，并另存当前数据卷。若更新后尚未保存新的 Agent 能力配置，可从 `pogo` 目录执行：

```powershell
docker compose -f compose.yml -f C:/Users/14777/.codex/agent-worker/maintenance/pogo-studio-capabilities-20260923/rollback.compose.yml up -d --no-build --no-deps pogo
```

若已经保存新配置，旧版的严格 JSON 读取不识别新增能力字段，不能仅切镜像；需要同时使用兼容的 Agent 注册表快照，并先确认如何保留更新后的 Agent 与会话记录。不要直接用旧卷覆盖新产生的用户数据。
