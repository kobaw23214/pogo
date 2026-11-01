# Subagent 委派

2026-09-30：自动 worktree 和 Git 提交交付已退役，见 [迁移说明](git-retirement.md)。子任务不再要求工作目录是 Git 仓库。

Subagent 有独立上下文，执行时启动独立 Pogo 子进程。主 Agent 提供完整任务描述，子任务的结论、执行记录与用量返回主会话。Web 与普通 CLI 会话会保留子 Agent 身份和历史；执行结束后进程退出，逻辑 Agent 进入 `idle`，相关追问可继续使用原上下文。

## 保留上下文与继续任务

`subagent` 默认 `action=spawn`，兼容原有 `task`、`mode`、`wait` 调用。结果同时返回稳定的 `agent_id` 和本次执行的 `run_id`：

- `action=continue`：传入本父会话返回的 `agent_id` 和新的 `task`，在原 transcript 上继续；无需重复 `mode`，也不能改成更宽权限。
- `action=list`：列出此父会话保留的上下文，包括 `idle`、执行中、被中断和已归档的子 Agent。
- `action=archive`：归档空闲子 Agent，保留执行记录；相关上下文不再接受续跑，后续新工作使用 `spawn`。

每次续跑保持原模型、目录及技能权限范围。父会话的相关能力配置发生变化时拒绝静默扩大权限，应新建委派。不同父会话、不同用户不能通过拿到一个 `agent_id` 复用彼此上下文。同一个子 Agent 一次只能有一个排队或运行中的任务；运行中追加消息的双向协作尚未提供。

空闲子 Agent 不占用进程、并发名额或模型调用。续跑才加载历史；长上下文使用现有自动压缩机制，完整 transcript 继续保留。超时或取消只结束当前执行；重启时遗留的未完成记录标为 `interrupted`，需要明确继续，不自动重放可能产生副作用的操作。继续浏览器任务必须重新观察当前页面，旧的元素引用、坐标或授权不能作为新的操作依据。

并发和超时仍按每次执行计算。续跑用量按本次增量计入父会话；后台完成按 `run_id` 幂等记账，重发完成事件不会重复累计。

## 两种模式

| | `explore` | `edit` |
|---|---|---|
| 用途 | 搜索、阅读、解释 | 修改文件、运行命令、生成产物 |
| 目录 | 当前会话工作目录 | 当前会话工作目录 |
| 工具 | 只读文件工具及允许的只读 Skill 工具 | 增加 write、edit、bash |
| 并发 | 可并行；Web 可后台执行 | 同步执行；同一 OS 用户的可写委派跨会话、跨进程串行 |
| 结果 | 结论、子任务会话、用量 | 结论、工作目录、直接落在目录中的文件、用量 |

`mode` 必填。运行测试等会调用 Shell 的任务也属于 `edit`。两种模式都读取当前文件，包括尚未被版本控制跟踪的输入。

编辑子任务不生成提交，不返回等待合并的改动。父 Agent 应说明允许修改的文件和输出位置；不同分析任务优先约定不同输出文件。文件修改立即生效，跨进程租约只串行 Pogo 的编辑委派，不阻止外部编辑器、其他主会话或独立 Shell 写入，也不构成文件系统沙箱。

## 同步与后台

Web 下 `explore` 默认后台执行，立即返回任务 ID；`subagent_tasks` 查询仍未完成的任务，结果通过完成通知到达，界面可取消任务。`wait=true` 会同步等待探索任务。已完成任务的上下文用 `subagent action=list` 查看和继续。

`edit` 始终同步执行，即使传入 `wait=false`；它在当前工具批次中作为串行屏障，父 Agent 等待它结束后才继续。同一操作系统用户的编辑委派还通过 OS 文件锁互斥。取消等待会退出，不启动子进程；锁文件保留但锁随持有者退出而释放。当前支持 Unix 文件锁的平台；不支持的平台拒绝可写委派，仍可使用探索模式或由主 Agent 执行任务。

`POGO_SUBAGENT_CONCURRENCY` 控制每个会话的委派并发上限，默认 2；该上限不会放开可写委派的全局串行约束。

## 权限与生命周期

子任务没有进一步委派的工具，也没有主会话的长期 memory 或 todo。文件工具仍限制访问根；可写子任务的 Shell 守卫拒绝 Git 操作和明显的目录逃逸，OS 沙箱另行约束命令。Shell 守卫用于防止误操作，不能代替进程隔离。

Web 子任务的审批经管道交回父会话；浏览器操作也使用父会话授权的通道。只读子任务不会获得 write/edit/bash。默认最多一层子任务，默认执行超时十分钟。

## 会话与用量

子任务会话位于会话目录下的 `subagents/`，可以通过子任务卡片查看，或使用：

```bash
pogo -analyze-session <子任务会话路径>
```

主会话总用量已包含子任务用量，不能把两者再相加；子任务会话提供的是明细。

## 旧配置与数据

`POGO_SUBAGENT_ISOLATION` 已无模式切换作用。空值与旧 `shared` 值可用；显式 `worktree` 会在启动时提示迁移错误，避免升级后悄悄把隔离编辑变为原地编辑。移除该设置后使用上述执行方式。

`-worktrees`、`-worktrees-prune` 已移除。升级不会清理现有 worktree、Git refs 或提交。旧对话中的 commit/ref/worktree 字段仍可读取，新任务只报告当前工作目录和执行结果。

---

## English

Subagents run in a separate Pogo process with their own context. Both modes use the current workspace and work without a Git repository. `explore` is read-only, may run concurrently, and can run in the background on Web. `edit` retains file writes and shell commands, always waits synchronously, and serializes writable delegations across sessions/processes of the same OS user using a Unix file lock. Unsupported platforms refuse writable delegation rather than falling back without a lock.

Edits take effect directly in the workspace; there is no automatic checkout, commit, or merge. The lease does not stop external programs or independent main conversations from writing and is not a filesystem sandbox. File guards, OS sandboxing, parent approval and browser forwarding remain separate controls. Prefer distinct output files for analysis tasks.

The legacy isolation setting accepts unset/`shared`; `worktree` requires migration. Existing worktrees, refs, transcripts and checkpoint data are not deleted. Historical result fields remain readable. See [retirement notes](git-retirement.md) for the product boundary; internal shadow Git checkpoints and file rewind remain available.
