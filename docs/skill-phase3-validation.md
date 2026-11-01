# Skill 监控第三阶段验收

日期：2026-09-21。此次在已有 MCP 配置导出和 MCP 调用监控上增加独立 Skill 监控。

## 交付

- 设置 → Skills → Skill 监控；中英文页面、三个阶段、调用方和入口来源、状态与名称/工具/元数据筛选、历史翻页、详情、Skill 分组及同 Trace / 会话关联。
- `skillobserve` 独立事件表、异步队列、历史查询和统计；复用观测数据库但不计入 MCP 调用总量。
- 在运行目录快照、已登记 SKILL.md 读取、显式 `/skill` 展开、Skill 声明工具实际执行处记录可信元数据。
- 管理员权限、监控关闭开关 `POGO_SKILL_CALLS=0`、SQLite 不可用时的内存降级提示。
- MCP 与 Skill 的共享数据库关闭顺序：停止新事件、限时排空队列、取消阻塞数据库工作、等待后台 writer/sweeper 退出，最后关闭数据库。

阶段定义、统计口径与可观测范围见 [使用说明](skill-monitoring.md)。

## 实际集成和页面验收

使用临时 SQLite、测试令牌、脚本化模型响应以及真正运行的测试子进程。MCP 通过 Go SDK Streamable HTTP 实际调用，界面通过本地 Vite 与 Go HTTP 服务交互。没有访问真实模型、改动业务实例配置或部署服务。

| 场景 | 结果 |
| --- | --- |
| 外部直接执行 Skill 工具 | 106 条外部执行事件，105 成功、1 失败；没有伪造会话 / run / task |
| MCP 提交任务后由 Agent 读取并执行 | 同 Trace 中有目录可见、读取说明、声明工具执行各 1 条；caller=internal、entrypoint=mcp，带真实会话 / run / task / tool-call |
| 显式手动 Skill | 读取事件保留会话及 Trace；隐藏目录项不计入目录可见；未分配的 run ID 留空 |
| 全量统计与分页 | 浏览器匹配 111 条事件，首屏 100 条，加载更多后 111 条；统计为 2 目录、2 读取、107 执行；翻页暂停自动刷新 |
| 执行指标隔离 | 只筛选读取说明时，2 条读取、0 执行，执行错误率及 p50/p95 为空值 |
| 失败详情 | `execution_error`，有入口和父事件；无参数、结果、正文或绝对文件路径 |
| 同 Trace 与 MCP 跳转 | Skill 筛选恢复同链路 3 个阶段；跳转 MCP 后仅匹配关联的 `agent_submit_task`，未把 Skill 事件混入 MCP 总数 |
| 自动刷新 | 未按回车的关键词不进入轮询；提交无匹配关键词显示空结果，清空后恢复 111 条 |
| 服务不可用 | 关闭临时后端后页面显示加载失败，统计为空值，不显示成正常的 0 次调用 |
| 页面清理 | 临时浏览器页、测试后端和 15173 Vite 服务均已关闭 |

页面验收使用最终 UI 源码；随后对外部 MCP 工具的线上名称作后端修正，并以真实 SDK 集成测试断言其与实际协议名字一致。生产 UI 已构建并合入嵌入目录，保留已有无关资源。

## 自动验证

最终 Linux 的四个独立退出码均为 0；主 Agent 重新解析 JSONL，结果与最终源文件哈希核验相符。

| 检查 | 最终结果 |
| --- | --- |
| Linux 7 个受影响包全量测试 | 1,021 个顶层测试通过；含子测试共 1,278 个 pass；14 个条件跳过；0 失败 |
| 相同 7 个包全量 `-race` | 1,021 个顶层测试通过；含子测试共 1,278 个 pass；14 个条件跳过；0 失败，无 data race |
| `go vet` | 退出 0 |
| Linux / Windows 主程序构建 | 均退出 0 |

14 个跳过包括：9 项需要显式开启或提供 bubblewrap 的 Linux 沙箱集成测试、4 项平台 / 文件系统条件测试、1 项需要第二个不同窗口模型的目录测试。这些前提未在容器中补设，因此不将它们描述为通过；新增 Windows 路径大小写用例已在 Windows 单独通过。

- UI：57 个测试文件、3,095 项测试全部通过；`vue-tsc --noEmit` 和 Vite 生产构建退出 0。Vite 保留已有大 chunk 提示。
- Windows：真实内部 / 外部调用、显式展开、HTTP 统计、权限、关闭开关及路径大小写定向测试通过。新增日志关闭测试、迁移测试和目录事件测试连续 3 轮通过。
- Windows 额外的宽泛 `Close` 匹配包含旧调度器测试，因平台执行环境出现 `scheduled worker did not start`；日志保留，最终功能验收使用 Linux 的完整受影响包测试。
- 第一轮 Linux 全量发现两项问题：路径泄露测试把临时目录尾段 `001` 匹配进随机 ID；旧 MCP HTTP 日志关闭时未等待后台 SQL 操作退出。分别修正为检查完整转义路径和明确的后台任务退出协议，增加关闭排空及阻塞连接取消测试后重新验证最终快照。

验证命令：

```text
node node_modules/vitest/vitest.mjs run
node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
node node_modules/vite/bin/vite.js build --outDir <temporary-dist> --emptyOutDir
go test -json ./skillobserve ./mcpobserve ./mcpclient ./tools ./agent ./skills ./web -count=1 -timeout 300s
go test -race -json ./skillobserve ./mcpobserve ./mcpclient ./tools ./agent ./skills ./web -count=1 -timeout 360s
go vet ./skillobserve ./mcpobserve ./mcpclient ./tools ./agent ./skills ./web
go build -o <temporary-output> .
```

Linux 使用已有 `pogo:yw-multitenant-builder-20260911` 镜像、Go 1.26.5，网络关闭，源码快照独立挂载；没有修改真实运行目录。完整测试输出、独立退出码、工作树起始哈希、浏览器测试夹具和 Worker 结果保存在：

```text
C:/Users/14777/AppData/Local/Temp/pogo-skill-phase3-20260921-035656/
```

最终 Linux 证据使用 `final-linux-*`，第一轮失败证据使用 `linux-*`。实现由独立 Kimi K3 Workers 和主 Agent 完成；接入点 Worker 达到执行时限后，由主 Agent 完成修正和集成复核。Linux 清单验证由独立 GLM-5.3 Worker 执行，并检查实际测试结果而非仅看 Worker 包装器退出。

工作树起始哈希与当前文件对比：37 个相关源文件 / 文档新增或修改，无源文件删除；最终 Linux 快照与所有发生变化的非文档源文件哈希一致。嵌入页面的 index 与最终 Vite 构建一致，index 引用的资源全部存在。本次没有提交、部署或重启既有业务实例。

## 本期边界

覆盖 Pogo Web 服务进程。没有独立 CLI 收集、跨服务 W3C Trace 或 OTLP 导出，也不把读取说明等同于完整 Skill 工作流成功。普通 bash、外部 MCP、参考文件读取不从提示词上下文推测 Skill 归属；在实际工具 Execute 之前被拒绝的操作仍由原会话 / MCP 审计记录。现有会话保留创建时的工具绑定快照；显式 `/skill` 展开按本次实际读取的已登记 Skill 归属。
