# Git / worktree 产品能力退役

2026-09-30：Pogo 不再提供用户仓库的 Git 管理功能或自动 worktree 管理。工作区可以是普通目录；聊天、文件工具、MCP 和 Skill 运行不要求先建立用户仓库。

## 已移除

- 仓库状态、分支及未提交数量的 UI 展示，以及模型上下文中的 Git 状态注入。
- 提交、生成提交信息和编辑提交规则的 UI 与 API。
- Git 为工作区变更列表补充的未提交文件，以及基于用户仓库 HEAD 的 diff。
- 自动创建、列出和清理编辑任务 worktree 的产品入口。

CLI 参数 `-worktrees`、`-worktrees-prune` 和 `-no-git-context` 已删除。旧启动脚本应移除这些参数；文件快照的 `-checkpoints-prune` 继续可用。

旧 `/api/workspace/git`、`/api/workspace/commit`、`/api/workspace/commit/message`、`/api/workspace/commit/rules` API 已移除；已认证请求返回 404。集成方应停止调用这些接口。

## 编辑型 subagent

编辑任务在当前工作区同步执行，不再要求 Git 仓库，也不返回待合并的 worktree。即使请求 `wait=false`，编辑任务也同步完成；只读探索仍可在后台并行。

同一操作系统用户发起的编辑委派通过跨进程锁串行，覆盖不同会话和 Pogo 进程。这把锁不阻止用户、外部程序或其他 shell 写入，不能当作文件系统沙箱。不支持该跨进程锁的平台会拒绝编辑委派，不会静默并发执行。

旧隔离配置的空值或 `shared` 保持当前工作区语义；显式 `worktree` 配置会收到迁移错误，需要移除该配置后再运行。

## 继续保留

- 文件 journal：记录主会话的 `edit` / `write` 和文件面板保存之前的内容，提供工作区变更、diff 和清空基线。Bash、外部编辑器及独立子进程的写入不自动进入主会话 journal；子任务结果仍可在其执行记录和工作目录中查看。
- 文件 checkpoint / rewind：每次运行前在会话目录下的独立影子仓保存文件快照。它不依赖工作区内的 `.git`，也不写用户仓库历史。
- 对话 rewind：继续通过会话记录分叉恢复上下文。

运行镜像仍安装 Git，用于内部文件快照。卸载 Git 会让文件 checkpoint / rewind 不可用；聊天及 journal 的变更查看仍可运行。

## 现有数据

升级不会删除项目 `.git`、提交历史、旧 worktree 目录、checkpoint 或原有 `.pogo/commit-rules.md` 文件。旧提交规则文件不再由产品读取。退役产品入口也不等于屏蔽通用 shell 中的 Git 命令；命令仍受 shell 工具的权限和审批规则约束。
