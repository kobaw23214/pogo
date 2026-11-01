# MCP 调用监控

在 **设置 → MCP → 调用监控** 查看工具执行记录。页面覆盖外部 Agent 调用 Pogo（入站），以及 Pogo 的 Web 会话调用外部 MCP（出站）。原有 MCP 服务页的 HTTP 日志继续用于连接、认证和传输诊断。

## 页面与统计

- 按方向、近 1 小时 / 24 小时 / 7 天、状态、服务器、工具和元数据关键词筛选。
- 总调用、运行中、成功、错误率、p50/p95、取消和中断使用整个筛选范围的保留记录；不会拿当前 100 行当作总量。
- 详情显示工具原名与 wire name、传输、MCP 会话、HTTP 请求标识，以及本地会话、运行、任务和父子调用关系。可继续筛选同一 Trace 或会话。
- 自动刷新间隔 8 秒。加载历史页后暂停自动刷新，保持查询时间范围；重新开启后回到最新页。输入框按回车或点刷新应用。

每次工具执行只有一条记录，开始与结束更新同一 ID。`ok` 表示这次 MCP 工具调用成功返回，不表示后台 Agent 任务或研究结论正确。例如 `agent_submit_task` 返回成功，只表示任务已接收。

错误率为 `(error + timeout) / (ok + error + timeout)`，不含运行中、取消和中断；无有效样本时显示空值。耗时采用执行期间的单调时钟，百分位使用最近秩法，包含正常结束、错误、超时及已取消调用，不包含运行中或中断。SDK `isError`、协议错误与连接错误均按实际结果分类，不从截断后的 HTTP 正文猜测。

## 关联与数据边界

入站调用通过 SDK middleware 记录，出站调用在 `MCPTool.Execute` 记录。外部任务提交将本地 Trace 传入后台 run，随后出站调用携带会话 / 运行 / task / tool-call ID。后台 run 的生命周期仍由会话管理，不绑定到 HTTP 请求断开。Streamable HTTP 记录当前请求的诊断 ID；旧版 SSE 的 SDK 不提供每次 POST 的上下文，因此该字段留空，不能当作握手请求。入站调用在进入工具 middleware 之前被 HTTP / JSON 解码层拒绝时，只能在原 HTTP 日志中查看。

此版本只提供 **单个 Pogo 实例内的关联**，尚未实现 W3C `traceparent` 的跨进程传递、OTLP exporter 或 CLI 独立进程收集。[Skill 监控](skill-monitoring.md) 在独立页面和数据表中区分目录可见、读取说明与声明工具执行，并通过本地 Trace 与 MCP 记录关联；Skill 事件不计入 MCP 调用总量。

新执行记录没有工具参数、结果、URL、凭据、提示词或原始错误文本；只保存名称、标识和有限错误类别。客户端名称由远端自报，仅用于诊断。记录中的 ID 不是授权凭据。旧 HTTP 日志保留自己的正文采样设置；新页面的元数据边界不会自动改变旧日志。

监控 API 与配置页使用相同的管理员权限。共享 token 模式沿用现有信任范围；启用多用户时，普通账户及委派身份不能读取跨账户监控历史。

## 存储与运行

执行记录写入现有 MCP 日志数据库中的独立表 `mcpobserve_calls`。异步队列不阻塞工具调用；页面显示丢弃事件数和写入失败数。这些计数是当前进程累计的写入健康信息，不随查询筛选改变。丢失结束事件时不会推断成功。

SQLite 读取完整保留历史并使用稳定游标翻页。新进程启动时，旧的 `running` 改为 `interrupted`，表示结束状态未知。数据目录按一个服务进程独占设计，不能由两个运行中的 Pogo 实例共用。正常退出先停止新请求、排空会话，再限时写完队列；被强制终止或队列溢出仍可能丢记录。

SQLite 无法初始化时降级为有界内存记录，页面明确提示不持久；运行中数据库读取失败会显示加载错误。默认容量为 1,000 条内存记录、200,000 行数据库记录。沿用现有启动配置：未设置保留天数时为 0，不按天过期；生产环境可显式设置 `POGO_MCP_LOG_RETAIN_DAYS=30`。保留清理启动时和每 5 分钟执行，行数上限不是瞬时硬配额。

沿用启动参数：`POGO_MCP_LOG_DB` 指定数据库，`POGO_MCP_LOG_RING` 设置内存容量，`POGO_MCP_LOG_RETAIN_DAYS=0` 关闭按天过期，`POGO_MCP_LOG_MAX_ROWS` 设置行数上限。设置 **`POGO_MCP_CALLS=0` 并重启**可关闭本次执行监控，原 MCP 调用与 HTTP 诊断仍可使用；已有记录不删除。`POGO_MCP_LOG_PAYLOAD=0` 仅控制旧 HTTP 日志正文。

## 接口

- `GET /api/mcp/calls`：记录与 `next_cursor`。
- `GET /api/mcp/calls/stats`：全量筛选统计和最多 20 个工具分组，独立于分页大小。
- `GET /api/mcp/calls/{id}`：单条记录详情。

筛选参数：`direction, server, tool, status, session_id, run_id, task_id, trace_id, owner_id, client, q, from, to`。时间为 UTC Unix 毫秒；默认近 24 小时。列表支持 `before` 和 `limit`（1–200）。无效筛选 / 游标返回 400，缺失详情返回 404，监控关闭或存储故障返回 503。响应禁止缓存。写入异步，因此列表和统计在持续产生调用时可能有很短的可见性延迟；单次统计内部使用同一数据库快照。

## 对标边界

参考 [OpenTelemetry MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/mcp.md) 的工具执行层观测、client/server 方向、低基数错误类别与上下文关联。该约定仍处于 Development 状态。本实现是本地监控基础，并未声称实现完整 OpenTelemetry 协议或跨服务分布式追踪。
