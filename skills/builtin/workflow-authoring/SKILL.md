---
name: workflow-authoring
description: 已停用的平台工作流对话入口。平台工作流由管理页面操作；Agent 对话中的可复用流程使用 agent-workflows 内部工作流能力。
---

# 平台工作流入口已迁移

此文件只保留旧 Agent 配置和历史引用的兼容身份，不再向 Agent 提供平台工作流操作能力。
平台工作流由用户在管理页面的“平台工作流”中创建、编辑、运行和发布。

Agent 对话只能操作当前用户、当前 Agent 的内部工作流。用户要求在当前 Agent
创建流程时，使用 `agent-workflows` Skill，以及 `agent_workflow_read`、
`agent_workflow_edit`、`agent_workflow_run` 和 `agent_workflow_enable`。
缺少这些工具时说明需要在当前 Agent 的能力配置中启用；不要把旧的 `workflow_*`
配置误认为仍有效，也不要把旧平台流程迁移或覆盖成内部流程。

请求涉及平台工作流时，说明应由用户进入管理页面操作。不得借用管理员密钥、
浏览器自动操作、直接修改宿主数据、调用管理 HTTP 接口或委派其他 Agent 绕过此边界。
内部流程仍沿用当前用户与 Agent 的权限，人工确认由流程所属用户在页面处理。
