# Skill 监控

Skill 是一组按需交给模型的说明与资源，未必具有独立、可判定完成的执行函数。页面因此记录可验证的操作，分开显示目录可见、说明读取和声明工具执行；不会把目录出现或读取说明计为整项 Skill 成功。

## 三类事件

| 阶段 | 含义 | 统计边界 |
| --- | --- | --- |
| 目录可见 `discovery` | 某次实际运行向模型提供了该 Skill 的目录项 | 每个 Skill 每次运行一条；隐藏的手动调用 Skill 不计为模型可见 |
| 说明读取 `load` | 显式 `/skill:name` 展开，或读取已登记的 `SKILL.md` | 机制分别标记 `explicit` / `file_read`；普通文件读取可能只取部分说明，不等于完成整个工作流 |
| 声明工具执行 `execute` | 调用 `tools.json` 中明确属于某个 Skill 的工具 | 可统计该次工具执行的结果和耗时；普通 bash、外部 MCP 或参考文件读取不根据上下文猜测归属 |

纯说明型 Skill 可以有读取记录而没有声明工具执行记录。这是可观测范围的边界，不代表它未被使用。模型是否按说明正确完成业务任务，需要另外的任务验收。

目录事件在运行准备完成、目录快照交给 Agent 循环时记录；它表示本次运行可见，不证明模型选择或理解了该 Skill。显式展开发生在 run 分配之前，所以读取事件有真实会话 ID 和 Trace，但 run ID 留空；后续运行沿用同一 Trace。

## 内部与外部

执行入口和任务来源是两个维度：

- 内部：Pogo Agent 加载说明或执行 Skill 的声明工具。
- 外部：其他 MCP 客户端直接调用 Pogo 对外发布的 Skill 声明工具。
- 外部 MCP 提交任务后，由 Pogo Agent 在任务内使用 Skill：执行入口仍为内部，任务来源为 `mcp`。

直接 MCP 工具调用可能没有 Pogo 会话 / run / task，详情对此留空。关联标识来自本地可信上下文；名称、参数或提示词不能伪造所有者和执行归属。

工具原名保留声明形式。线上工具名记录实际入口的名字：直接 MCP 调用保留 `demo.tool`，模型工具接口可能使用 `demo_tool`，两者不会混写。

## 指标与记录

总事件及三个阶段计数使用整个筛选范围。成功、错误率、p50 / p95 等执行指标仅计算 `execute`；说明读取成功不稀释执行错误率。错误率为 `(error + timeout) / (ok + error + timeout)`，取消、运行中和中断不进入分母。耗时包含已结束的成功、错误、超时及取消，未知或无样本显示空值。

自动刷新使用已应用的查询，历史翻页固定时间窗口并暂停刷新。详情按同 Trace / 会话查询时清除其他收窄条件，便于看同一链路；也可以转到 MCP 监控查看相关协议调用。

只保存 Skill 标识、名字、来源、阶段、执行入口、工具名、关联 ID、时间和有限错误类别，不保存说明正文、文件路径、命令、参数、结果或凭据。Skill ID 由可信目录里的规范路径与名字生成摘要，不把路径暴露到监控接口。当前不提供完整 Skill 包版本追溯。

## 存储与关闭

Skill 历史使用现有观测数据库中的独立 `skillobserve_events` 表，不计入 MCP 调用总量。SQLite 不可用时明确显示有界内存降级；队列溢出及写入失败通过计数提示。重启后未结束事件标为 `interrupted`，不推断成功。

沿用 `POGO_MCP_LOG_DB`、`POGO_MCP_LOG_RING`、`POGO_MCP_LOG_RETAIN_DAYS`、`POGO_MCP_LOG_MAX_ROWS`，但表的行数上限和事件计数分别计算。默认内存容量 1,000、数据库上限 200,000；默认不按天过期，生产环境可显式设置保留天数。清理每 5 分钟执行，上限不是瞬时硬配额。一个数据目录仅供一个活动服务进程使用。

设置 `POGO_SKILL_CALLS=0` 并重启可关闭 Skill 监控；已有记录保留，MCP 监控不受该开关影响。监控 API 仅限管理员。当前范围是 Pogo Web 服务进程，不收集独立 CLI 进程的操作，也不提供 OTLP 或跨服务 W3C Trace 传播。

## 接口

- `GET /api/skills/events`
- `GET /api/skills/events/stats`
- `GET /api/skills/events/{id}`

筛选支持 `stage, caller, entrypoint, skill_id, skill_name, skill_source, mechanism, tool, status, session_id, run_id, task_id, trace_id, owner_id, q, from, to`。默认近 24 小时，时间为 UTC Unix 毫秒；列表支持 `before` 游标和 1–200 的 `limit`。无效参数返回 400，缺失详情 404，关闭或存储错误 503。错误不会伪装成零条成功结果。写入异步，单次统计内部采用一致快照，页面两次请求之间仍可能出现短暂可见性差异。

## 参考

阶段划分参考 [Agent Skills 的渐进加载与激活方式](https://github.com/agentskills/agentskills/blob/main/docs/client-implementation/adding-skills-support.mdx)，工具执行、来源与错误类别参考 [OpenTelemetry GenAI agent / tool spans](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md)。本实现不把 Skill 等同于 Agent 或固定 Workflow，也不声称实现完整 OpenTelemetry 协议。
