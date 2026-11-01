# MCP 第二阶段验收记录

日期：2026-09-21。范围为双向 MCP 工具调用监控，保留第一阶段 JSON 导出与 transport 修复。未部署、未重启业务实例，未接入真实业务 MCP 或付费模型。页面使用脚本模型驱动真实 Go SDK / HTTP MCP 调用。

## 已实现

- SDK 入站 middleware、出站 `MCPTool.Execute` 埋点，以及会话 / run / task / tool-call / trace 关联。
- 独立 SQLite 元数据表、异步有界写入、保留清理、重启后的 interrupted 恢复、内存降级与写入健康计数。
- 管理员记录 / 统计 / 详情 API；分页历史独立于统计总量；一次统计中的 counts / percentile / groups 使用同一数据库快照。
- 设置中的独立调用监控页：筛选、工具分组、详情、同 Trace / 会话查询、自动刷新及历史分页。
- `POGO_MCP_CALLS=0` 的启动关闭开关。使用和限制见 [mcp-monitoring.md](mcp-monitoring.md)。

## 实际检查结果

| 检查 | 结果 |
| --- | --- |
| Linux：`go test -json ./mcpobserve ./mcpclient ./tools ./agent ./web -count=1 -timeout 300s` | exit 0；942 个顶层测试通过，含子测试共 1,178 项通过、13 项跳过、0 失败 |
| Linux：上述五包，`go test -race`，筛选 `MCP\|Observe\|Observation\|Monitor\|Span\|Store\|HeaderTransport\|Sessionless` | exit 0；174 个顶层测试通过，含子测试共 242 项通过、0 跳过 / 失败 |
| Linux：`go test -race -json ./mcpobserve -count=1 -timeout 120s`，完整存储包无名称筛选 | exit 0；25 项通过、0 跳过 / 失败，包含并发关闭 / 写入 / 查询测试 |
| Linux：上述五包 `go vet`、主程序 `go build` | 均 exit 0 |
| Windows：主程序 `go build` | exit 0，包含最终前端资源 |
| 前端：`vue-tsc --noEmit` | exit 0 |
| 前端：`vitest run` | 56 个文件，2,867 项通过 |
| 前端：`vite build` | exit 0；仍有原有体积较大 chunk 的提示 |

Linux 使用已有镜像 `pogo:yw-multitenant-builder-20260911`，Go 1.26.5、关闭容器网络、挂载源码快照。全量测试及 race 的 Go 源码与最终 Go 源码一致。页面验收后只有 UI 标签、布局与同链路筛选交互调整，已重新执行前端检查、更新内嵌资源，并重新构建最终 Linux / Windows 程序。

Windows 常规全量测试包含现有 POSIX / WSL、路径与 PTY 失败；Windows race 可执行文件加载也受本机环境限制。因此未将 Windows 全量宣称为通过，功能回归与竞态结论以上述 Linux 实际结果为准。

## 关键语义验收

- 实际 SDK 入站提交 → 脚本 Agent → 实际上游 MCP 服务：父子 Trace、session / run / task / tool-call 标识正确，大于 4KB 返回正文不进入执行记录。
- `isError`、nil / typed-nil / 非工具结果、协议拒绝、返回的 context 错误、取消、超时和 panic 不被算成成功；panic 不被观测逻辑吞掉。
- 去重提交关联已有任务与 run；未知 `task_id` 参数不被保存为本地关联。会话间 metadata 隔离。
- Streamable HTTP 每次工具调用有不同的当前 HTTP 请求 ID。旧版 SSE 不伪造 POST 请求 ID；仍保留传输、客户端及 MCP 会话信息。
- SQLite 历史超过内存容量仍可分页读取；错误率排除 running / cancelled / interrupted；零保留天数禁用按天过期；锁住 DB 连接时 Close 可以终止等待的写入器。
- 并发统计过程中总量、方向、分组与耗时样本来自同一快照；队列溢出 / DB 故障显式计数；记录 API 鉴权与管理员边界、非法筛选、缺失详情、关闭监控均有检查。

## 实际浏览器验收

临时测试实例产生 118 条真实 MCP 调用（116 入站、2 出站；116 成功、2 错误）。前端读取真实监控 API，没有 mock 统计值。

1. 第一页显示 100 条，但总量为 118、错误率 1.7%；加载更多后 118 条，自动刷新暂停。
2. 出站筛选得到 2 条、1 成功、50% 错误率；工具分组下钻 fail 得到 1 条 / 100% 错误。
3. 从 fail 详情筛选同 Trace，显示入站提交 + 2 个出站调用，共 3 条；详情的父调用与会话 / 运行 / task ID 对应。
4. 自动刷新使用已应用筛选，不采用尚未按回车的输入。应用不存在的服务器后，显示零记录及空耗时，不显示伪造的 0% 错误率。
5. 关闭临时后端后手动刷新，显示加载失败，各统计显示未知值；没有当成零条成功结果。
6. 修正空字符串选项显示为“请选择”的问题，以及历史滚动导致统计 / 筛选栏移出视野的问题。

临时页面、Go 测试服务及 Vite 服务验收后关闭。

## 审计与证据

Kimi K3 分别承担存储、埋点、前端实现；埋点 Worker 在全量 Windows 检查后达到时间上限，由主 Agent 接手复核、补充异常与关联修复。GLM-5.3 运行既定 Linux 检查并汇总结果。主 Agent 检查真实退出码、JSONL 测试事件、关键代码与浏览器行为，未以 Worker 外层完成状态代替验收。

完整本地证据目录：`C:/Users/14777/AppData/Local/Temp/pogo-mcp-phase2-20260921-031147/`。包括 `baseline.json`、`source-audit.json`、源码和 dist 备份、`linux-full.jsonl`、`linux-race.jsonl`、各检查的独立 `.exit`、前端检查日志、临时协议实例日志与构建产物。

按开始时文件哈希比较，既有源码修改限于本阶段指定的 11 个文件；没有删除既有文件。新增监控包 / UI / 验收测试和文档单独列在审计清单中。已有工作区修改、既有删除状态与无关文件保持原样；没有提交 Git。内嵌前端更新保留已有哈希资产，最终 index 指向新构建。

尚未覆盖线上真实模型与业务服务负载、外部 W3C trace 传播、OTLP 导出、CLI 进程收集或 Skill 监控。
