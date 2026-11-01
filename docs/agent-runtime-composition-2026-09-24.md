# Agent 的 Skill 与 MCP 组合

> 当前边界核对：2026-09-29。下述绑定/锁描述普通 Studio 会话；工作流尚未复用完整装配，见 [工作流限制](workflow.md) 与 [项目现状](project-status.md)。

创建 Agent 统一提交 skills 与 mcp_servers 数组，可同时选择普通 Skill、带工具的 Skill 和外部 MCP 服务。
服务端根据带工具 Skill 的已验证声明生成 skill_binding；客户端不能提交原始绑定、单独工具授权或主机路径。
新会话固定 skill_lock，工具与规则仍在同一 Pogo 容器的隔离进程运行。

Skill 的流程说明、运行工具和数据归于同一个安装单位。普通 Skill 不需要运行扩展 JSON。
不再有第二套安装页面、发行包兼容接口、外部 SQLite 导入或旧工具选择。

说明见 [Skill 管理与完整工具运行包](skill-packages-2026-09-24.md)。2026-09-24 的实现记录为只改代码、未测试/构建/部署；这不是之后所有版本的状态。本次核查结果集中在项目现状文档，未进行新部署。
