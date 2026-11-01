# MCP / Skill 监控与 MCP 接入

在 JSON 配置导出、MCP 调用监控和 Skill 监控之上，提供 CLI 采集、跨进程存储及标准 Trace / OTLP。普通 Skill 的“被发现”“说明被读取”“声明工具执行”分别记录；读取说明不会被解释为整个 Skill 工作流成功。MCP 接入使用原有的可选 Token 认证。

## 页面与入口

- 设置 → MCP → 接入（客户端）：配置外部服务器及可选的 Bearer Token，导入、导出 JSON 连接配置。
- 设置 → MCP → 建立（服务端）：启用本机 MCP 服务，选择 Token 认证方式，导出其他 Agent 访问本机的连接配置。调用监控查看入站和出站调用。
- 设置 → Skills → Skill 监控：按 discovery / load / execute、内部/外部调用和 pogo / mcp / cli 入口筛选。
- MCP / Skill 详情包含本地事件 ID、Trace ID、Span ID、父 Span、Session / Run / Task。对方自报的客户端名称仅作描述，不作为身份；旧记录中的调用方 ID 仅作为历史元数据显示。
- 采集状态展示本地持久化、租约错误与 OTLP 的导出、失败、丢弃、采样计数。远端返回的原始错误、Collector 地址和鉴权头不会通过状态 API 返回。

## CLI 与共享历史

Web 和 CLI 默认把元数据写入 session 目录旁的 `mcplog.db`；`POGO_MCP_LOG_DB` 可显式指定。多个进程必须指向同一文件，监控页面才能查到完整共享历史。`none` 使用进程内存，不共享历史。

每个记录进程使用随机实例 ID 和定期心跳。启动恢复和关闭回收区分自己的运行记录、仍存活的其他实例及过期实例，避免一次 CLI 启动把 Web 中的调用误标为中断。共享数据库使用 WAL 和有界忙等待；监控写入采用独立有界队列，不把 SQLite 或 Collector 的网络延迟放进工具执行路径。

历史数据的新增字段采用加列迁移。升级时应先结束旧版本记录进程，再让新版 Web / CLI 共用数据库；旧版本没有租约，无法参与跨版本存活判断。

## OTLP 配置

默认关闭网络导出。显式配置后使用标准 OTLP HTTP/JSON：

```powershell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
$env:OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = 'http/json'
$env:OTEL_SERVICE_NAME = 'pogo'
```

也可用 `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318` 自动附加 `/v1/traces`。traces 专用配置优先于通用 OTLP 配置。鉴权通过 `OTEL_EXPORTER_OTLP_TRACES_HEADERS`（或通用 `OTEL_EXPORTER_OTLP_HEADERS`）设置，按 `key=value,key=value` 编码；值中的保留字符使用百分号编码。配置应由部署环境注入，不放入 MCP 导出文件。

仅支持 `http/json`，不宣称支持 gRPC 或 protobuf。配置错误会关闭导出并保留本地采集。记录只包含工具/Skill 名称、状态、耗时及关联 ID，不导出 prompt、参数、结果、Skill 内容、原始报错和 Owner ID。TraceState 只在请求上下文中传播，不写数据库或 OTLP 属性；baggage 不传播。

导出有界排队、批处理、超时与有限重试；部分成功不重发已接受的 Span；重定向不携带凭据跟随。远端故障只影响导出健康状态。`sampled=00` 仍保留本地事件，但不向 Collector 导出。

## Token 认证与免认证接入

在“建立（服务端）”页面选择原有的 Token 认证方式：

- 不开启：允许免认证接入，无需注册调用方或申请凭据。
- 环境变量：填写保存共享 Token 的变量名；变量缺失时拒绝请求，不退化为免认证。
- 直接填写：保存共享 Token，接入方使用该 Token 认证。

`POGO_MCP_TOKEN` 环境覆盖继续生效，页面会提示配置被环境变量接管。标准 `/mcp` 和旧版 `/sse` 使用同一共享认证配置。JSON 导出按当前生效状态生成：免认证时不添加认证字段，启用 Token 时仅导出环境变量引用，不包含 Token 原文。

独立调用方凭据功能已移除，不再读取 `POGO_MCP_CLIENTS_FILE` 或旧 `mcp-clients.json`。已有凭据文件、任务历史和监控数据库不会被删除；旧注册记录不再影响当前认证方式。监控与认证独立，匿名调用仍可记录工具、耗时、状态及会话信息。

服务端工具白名单、审批策略、文件边界和全局并发、提交频率、等待额度继续生效。启用业务委派身份或私有会话模式的部署仍保留原有机器入口关闭规则；原多用户身份及任务归属设施保持不变。

`strict` 与操作方显式 `trust: true` 的优先级遵循已有 policy 合约；本轮不修改该授权语义，也不会把远端声明的 annotation 当成信任授权。

## 关闭与回退

| 配置 | 效果 |
| --- | --- |
| `POGO_OTLP=0` 或 `OTEL_SDK_DISABLED=true` | 关闭网络导出，保留本地采集 |
| `POGO_CLI_CALLS=0` | 关闭 CLI 的 MCP / Skill 采集 |
| `POGO_MCP_CALLS=0` | 关闭 MCP 工具调用元数据采集 |
| `POGO_SKILL_CALLS=0` | 关闭 Skill 元数据采集 |
| `POGO_MCP_LOG_DB=none` | 使用进程内存，关闭监控历史持久化 |

以上采集配置在进程启动时读取。旧 HTTP 诊断日志是独立功能，涉及载荷的保留策略继续由 `POGO_MCP_LOG_PAYLOAD` 控制。

## 标准与验收边界

跨 MCP 请求使用该次 `tools/call` 的 `_meta.traceparent` / `_meta.tracestate`，不会复用握手时的 Trace。标准 Span ID 为 16 位十六进制，本地事件 ID 保持原有 32 位格式。格式依据 [W3C Trace Context](https://www.w3.org/TR/trace-context/)、[MCP 基础协议](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/index.mdx) 和 [OTLP 规范](https://opentelemetry.io/docs/specs/otlp/)。[Langfuse 的 OpenTelemetry 入口](https://langfuse.com/integrations/native/opentelemetry) 支持此 HTTP/JSON 格式；实际部署需填自己的端点和鉴权。

OAuth、标准 MCP Tasks 和 Skills over MCP 属于单独的可选协议扩展，本轮默认范围不包含它们。这里的实现不会宣称普通 Skill 有可判定的工作流级成功率。

移除独立凭据后的运行测试由使用方执行；编译和静态检查不代替实际客户端接入验收。
