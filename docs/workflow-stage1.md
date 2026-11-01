# 工作流第一阶段：权限与统一运行管理

本阶段保留现有画布、发布版本、定时任务、人工节点和历史记录，增加默认空间中的动作授权及统一运行受理。以下描述对应源码；本次没有部署 Docker，也没有修改正在使用的服务数据。测试使用独立临时目录。

## 身份与权限

- 空间固定为 `default`。本地账号关联原 `LocalScope`，不修改用户哈希、会话 ID、工作目录或 MCP 凭据映射。
- 主体区分本地用户、本地操作者、服务和外部用户；本阶段只提供现有本地账号的管理与授权入口，尚未接入外部认证协议。
- 本地运维管理员保持 `space_admin`；普通用户未配置覆盖时保留原有工作流读取和已发布运行能力。
- 可配置角色为 `space_admin`、`builder`、`user`、`disabled`；`builder` 默认可编辑，发布、删除、定时和授权管理需另行授权。空角色恢复兼容默认。运维管理员不能通过角色接口降级或停用。
- `disabled` 拒绝该主体的工作流操作及运行访问。服务或外部主体不继承单用户模式的管理员兼容权限。
- 工作流权限：`workflow.read/edit/publish/invoke/delete/schedule/manage_access`。
- 运行权限：`run.read/cancel/retry`、`approval.decide`、`artifact.read`；参与者管理由管理员或运行所有者负责。
- 查看、取消、重试、审批及产物分别检查。节点授权只允许 `run.read`、`approval.decide`、`artifact.read`，不扩大到其他节点的产物。

## 权限 API

所有接口沿用当前账号认证。先从响应的 `subjects` 获取 `principal_id`，不要按用户名猜测主体 ID。

| 接口 | 用途与请求 |
| --- | --- |
| `GET /api/manage/workflow-access/roles` | 空间管理员读取 `{space_id,roles,subjects}` |
| `PUT /api/manage/workflow-access/roles` | 空间管理员提交 `{principal_id,role}` |
| `GET /api/manage/workflows/{wid}/access` | 有 `workflow.manage_access` 者读取 `{grants,subjects}` |
| `GET /api/manage/workflows/{wid}/capabilities` | 有 `workflow.edit` 者读取编辑器所需的原生工具静态目录；不返回管理员 Skill／MCP 配置 |
| `PUT /api/manage/workflows/{wid}/access` | 提交 `{principal_id,actions:[...]}`；显式 `actions:[]` 撤回该流程的权限；`{principal_id,inherit:true}` 恢复默认 |
| `GET /api/workflows/{wid}/runs/{rid}/participants` | 有参与者管理权限者读取 `{participants,subjects}` |
| `PUT /api/workflows/{wid}/runs/{rid}/participants` | 提交 `{principal_id,actions:[...],node_id?}`；空 `actions` 撤销对应 Run／节点授权 |

工作流及运行响应附带 `capabilities`，用于显示可执行操作；服务端仍逐请求鉴权。普通运行所有者保留兼容操作权限，参与者通过显式授权获取对应能力。

后续新增的[审批策略](workflow-approval-policy.md)对明确启用规则的节点收紧审批：禁止自批先于所有者/管理员快捷授权，并要求当前明确的节点或运行审批授权。未配置策略的历史定义和运行保留本阶段兼容行为。

## 逻辑运行与执行段

- 一个稳定 `run_id` 表示整次工作流；队列每条记录表示一个执行段，两者通过 `WorkflowRunID`／关联表连接。
- 手动启动、定时触发、人工决定、超时和恢复进入持久队列。已有定时 worker 关联首段，避免再次入队等待造成容量死锁。
- `queued` 表示已受理、等待执行。人工等待仍保留同一逻辑 Run；执行段结束后释放容量，审批续跑生成新执行段。
- 原生 Run 在人工等待时保持 `working`，节点为 `waiting_human`；任务中心显示 `running` 与 `waiting_human:true`，不把整次办理误记完成。
- 同一 Run 的续段互斥，不把不同用户对同一工作流的调用全部串行；定时任务原有防重叠规则保留。
- 关闭页面、刷新或 SSE 重连不会取消运行。取消记录持久意图，实际完成回执仍决定最终执行结果。
- 失败或取消后的重试创建新 Run，保留 `retry_of`、原定义与执行身份；实际重试人另记为 initiator，并获得新 Run 的跟踪权限，不自动获得审批权。
- 运行中重启后，结果不明的工作不会自动重新执行。安全的尚未开始／人工等待边界通过恢复对账处理。
- 定时工作流沿逻辑 Run 的显式重试链判断是否恢复触发：重试仍排队、执行中或结果未知时继续阻挡；明确终态后可以恢复，原中断回执不被改写。旧定时 wrapper 与新续段按同一 Run 去重。

## 运行 API 与页面

| 接口 | 说明 |
| --- | --- |
| `POST /api/workflows/{wid}/runs` | 从已发布定义受理，返回 `{id}`；可传 `Idempotency-Key`，同主体同流程同键同内容返回原 Run，不同内容冲突 |
| `GET /api/workflows/{wid}/runs` | 按权限返回历史；支持 `limit=1..200` 与 `offset`，先授权过滤再分页 |
| `GET .../runs/{rid}`、`GET .../runs/{rid}/stream` | 精确读取／观察某次运行，包含当前动作能力 |
| `POST .../runs/{rid}/cancel` | 按取消权限提交取消意图 |
| `POST .../runs/{rid}/retry` | 按重试权限受理新 Run，返回 `{id}` |
| `POST .../runs/{rid}/nodes/{nid}/resume` | 按节点审批权限提交 `{action,fields}`；同一等待代次竞争决定只接受一次 |
| `GET .../runs/{rid}/nodes/{nid}/artifact` | 按该节点产物权限读取 |

编辑器增加运行历史、参与者授权与重试；历史视图使用该 Run 的流程快照。任务中心 API 对关联的首段／续段按逻辑 Run 去重，并保留旧队列 ID 控制别名；原有 Agent 任务中心仍按 Agent 过滤。

## 存储与历史兼容

控制表位于 `<SessionDir>/task-runs/runs.db`，复用现有 SQLite 持久队列及控制器锁：

| 表 | 职责 |
| --- | --- |
| `workflow_control_schema` | 本模块数据格式版本 |
| `workflow_run_catalog` | Run 的不可变归属、定义、来源、发起／执行身份及取消意图 |
| `workflow_admissions` | 启动请求幂等映射 |
| `workflow_executions` | Run 与队列执行段关联 |
| `workflow_commands` | 人工等待代次的已受理决定及执行段 |
| `workflow_space_roles`、`workflow_resource_grants`、`workflow_run_grants` | 空间角色、流程授权、Run／节点参与者 |

受理记录、幂等映射、首段入队和实际发起人授权在同一事务提交；人工决定与续段入队同样原子提交。节点执行状态继续以 `wf-runs/{id}/run.json` 为权威，使用状态 revision 和已应用命令 ID 对账。

旧文件只登记目录元数据，不因导入而重写原 ID、JSON 或队列请求摘要。带 owner 的历史继续关联原账号；无 owner 的历史默认仅管理员可见，标记为历史／系统记录，不自动分配给其他用户。损坏文件保留供管理员核查。

旧二进制不认识新增的工作流执行来源及控制语义，不能直接消费新队列。回退必须同时考虑程序和完整持久数据；仅换回旧镜像不足以保证兼容，应在停止控制器后保留一致的数据备份。

## 阶段边界

本阶段不包含发布入口 Publication、跨系统 iframe／启动码、外部用户认证、v2 Agent／Skill／MCP 依赖冻结，以及完整多租户进程隔离。原有发布版本仍只冻结流程定义，引用 Agent 的行为未因此变成完整可复现快照。

后续第二阶段已新增 Publication、一次性启动码、短时嵌入会话与受控账号映射，沿用本阶段权限和 Run。接入方式见[工作流嵌入其他系统](workflow-embedding.md)。

源码阶段没有重新构建部署静态产物、部署 Docker 或对现网数据执行迁移。2026-10-27 已补做以下独立本地页面验收；实际部署后的页面交互与真实业务验收仍需另行完成。

macOS 下验证使用 `TMPDIR=/private/tmp` 的规范临时路径；默认 `/var` 路径可能触发相邻 Skill／Office 测试的符号链接保护。规范路径复验通过，未为通过测试而放宽该保护。

## 2026-10-27 本地页面验收

使用 `TestWorkflowUIFixtureServer` 创建独立临时账号、Agent、工作流、历史记录和 SQLite 队列，浏览器操作真实管理页面及 HTTP 接口。模型使用本地脚本响应，不调用外部模型、工具或业务系统；未读取或写入现有服务数据。

- 权限：管理员、普通运行所有者、builder、只读参与者、节点审批者分别验证。无运行授权时不可查看；只读参与者不可审批、取消或重试；节点授权限制材料范围；撤销后活动运行的详情和审批表单关闭并显示明确提示。
- 审批：必填校验有效，文本和下拉选择不会被轮询清空；测试服务重启后授权和待审批状态保留，审批后沿同一 Run 续跑并完成，执行段为两段。
- 历史：既有测试历史和新审批记录可分别查看，展示各自流程快照及审批意见；刷新保留选中的 Run。
- 取消与重试：取消持久保存；失败或取消后的重试都生成新 Run、保留原记录及 `retry_of`，刷新仍显示新 Run。

验收中已修复普通用户脱敏字段缺失导致页面报错、发布状态误判、重试地址未更新、轮询清空表单、参与者候选漏载、撤权后残留详情，以及 builder 工具目录不可见。修复后完成页面复验。

必要回归通过：6 个前端测试文件共 55 项、`vue-tsc --noEmit`、相关 `TestWorkflowAccess`／`TestWorkflowEditorCapabilitiesAreScopedMetadata`／`TestWorkflowService` 后端测试及 `git diff --check`。两次页面 fixture 均正常退出，临时数据和验收服务已清理。本地截图与运行证据位于工作区 `.ci-artifacts/workflow-stage1-ui-KgGT3t/`（不纳入 Git）；该结果不代表真实模型业务或 Docker 部署已验收。
