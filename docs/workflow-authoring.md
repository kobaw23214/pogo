# 平台工作流编排接口（兼容参考）

平台工作流当前从管理页面的“平台工作流”入口管理。Agent 不再注册或接受本文所列的七个 `workflow_*` 宿主工具，也不再加载 `workflow-authoring` Skill；既有 Agent 配置不能恢复这些授权。平台定义、发布版本、权限、运行历史及管理 HTTP 接口继续保留。Agent 对话中的自然语言建流请使用 [Agent 内部工作流](agent-internal-workflows.md)。

下文保留原编排服务的数据契约、权限和历史工具设计，供兼容维护参考，不是当前 Agent 的启用教程。

## 当前操作入口

有权用户在管理页面打开“平台工作流”，使用现有画布、版本、审批和运行记录功能。平台的创建、编辑、执行和发布仍按各自权限授权；这些权限不再转换为 Agent 对话工具权限。

旧 Agent 的存储配置和会话历史保留，运行时过滤已停用的平台工具与 Skill。内部工作流使用独立的 `agent_workflow_*` 工具，按当前用户与 Agent 隔离。

## 历史七个工具与权限

| 工具 | 作用 | 用户权限 |
| --- | --- | --- |
| `workflow_discover` | 查询可复用 Agent、模型、真实有效工具范围和审批账号，返回不支持能力的原因 | 全局创建/编辑权限，或指定 `workflow_id` 的 `workflow.edit` |
| `workflow_read` | `list/get/runs/run/artifact`，读取草稿、发布指针、统一历史及有权产物 | 沿用对应 `workflow.read`、`run.read`、`artifact.read`；保留节点级裁剪 |
| `workflow_validate` | 只读检查定义、模板、引用、样例输入与执行能力，返回诊断和 hash | 全局或指定工作流的 `workflow.edit` |
| `workflow_edit` | 创建草稿，或原子应用局部节点/连线修改 | `workflow.edit` |
| `workflow_test` | 冻结已保存草稿，带样例参数执行，不移动发布指针 | 同时需要 `workflow.edit` 和 `workflow.invoke` |
| `workflow_run` | 启动当前已发布版本，可约束 `version_id` | `workflow.invoke` |
| `workflow_publish` | 发布审阅过的 revision/hash | `workflow.publish`；编辑权限不自动包含发布权限 |

只读会话只可能注册 discover/read/validate；还必须满足该 Agent 的实际工具选择和用户权限。编辑、真实执行和发布分开选择、分开授权。工具不提供自动审批或修改参与者权限的入口；人工审批、禁止自批、自动分配、取消与重试继续使用现有机制。

工作流编辑者未必有创建或修改 Agent 的管理权限。资源不足时应返回缺口，使用已授权资源或由有权管理者配置。资源发现不返回 Agent 指令、凭据、连接地址、私有文件路径；返回的 ID、状态和 effective capabilities 才是选择依据。

## 推荐操作顺序

1. 用 `workflow_discover` 查找真实资源。修改现有流程时先 `workflow_read {action:"get",workflow_id:...}`，记录 `revision`、`definition_hash` 和权限。
2. 解释将增加、修改或删除的步骤、依赖、输入、产物和审批安排。只澄清阻止下一步的缺失信息。
3. `workflow_edit` 保存草稿。新建使用 `action:create`；后续使用 `action:patch`，保持原节点 ID。
4. `workflow_validate` 检查保存结果和样例 inputs。修复 diagnostics 后再检查；结构有效、能力可执行、实际试跑成功是不同结论。
5. 用户要求验证时，用 `workflow_test` 执行精确草稿，通过 `workflow_read` 跟踪同一 Run。人工等待时提供审批页面，不代替用户审批。
6. 用户要求发布时，展示最终内容与未解决项，用 `workflow_publish` 携带这次审阅的 revision/hash。发生冲突先重新读取、解释差异，再决定如何修改。

## 草稿创建与局部修改

创建示意（资源 ID 和审批账号必须先发现，以下占位值不是预置资源）：

```json
{
  "action": "create",
  "request_id": "create-application-review-1",
  "name": "申请资料审核",
  "nodes": [
    {
      "id": "prepare",
      "agent": "AGENT_ID_FROM_DISCOVERY",
      "task": "整理合成申请 {{ inputs.application_id }} 的摘要。",
      "tools": []
    },
    {
      "id": "review",
      "kind": "human",
      "after": ["prepare"],
      "prompt": "审核摘要：{{ nodes.prepare.summary }}",
      "approval_policy": {
        "prevent_self_approval": true,
        "assignment": "fixed",
        "approvers": ["USERNAME_FROM_DISCOVERY"]
      }
    }
  ]
}
```

`tools:[]` 只收窄内置工具，不关闭引用 Agent 的 Skill/MCP。此示例须使用资源发现确认适合试跑的 Agent；不能仅据这个字段推断“没有任何外部能力”。

局部修改示意：

```json
{
  "action": "patch",
  "workflow_id": "WORKFLOW_ID_FROM_CREATE",
  "expected_revision": 1,
  "request_id": "update-prepare-task-1",
  "operations": [
    {
      "op": "update_node",
      "node_id": "prepare",
      "patch": { "task": "按新的检查清单整理申请 {{ inputs.application_id }}。" }
    }
  ]
}
```

支持 `add_node`、`update_node`、`remove_node`、`connect`、`disconnect`、`set_metadata`。连线使用 `from/to/when`；`update_node.patch` 只改变指定字段，不能改 `id`。整批修改在同一次草稿保存中受理，任何结构或引用错误均不提交半张图。删除节点会移除与它相关的依赖边，但仍引用它的模板必须在同一批操作中修正。

patch 的 `expected_revision` 必须匹配当前草稿。创建和修改都需要 `request_id`：相同主体、相同请求重试返回原受理回执；同一键用于不同意图返回冲突。不同修改意图使用新键，不能用“拿最新 revision 后盲目重发旧整图”处理冲突。

## 结构化校验与参数

`workflow_validate` 可检查：

- 一个已保存的 `workflow_id`，可加 `expected_revision` 防止检查错版。
- 一个新 `definition:{name,description,nodes}`，需要全局编辑权限。
- 某已有工作流范围内的替代提案：同时提供 `workflow_id`、`expected_revision` 和 `definition`。沿用该流程的编辑权限，不保存候选。

诊断包含 `code`、`severity`、`node_id`、`field_path`、`message`、`fix_hint`。返回 `valid`、`ready_for_test`、`input_keys` 和规范化 `definition_hash`；校验不启动模型、业务工具或 Run。错误信息按节点/字段返回，Agent 应逐项修正，而非隐藏问题继续发布。

本阶段参数值使用字符串。声明依据是流程中 `{{ inputs.key }}` 的引用；这些 key 都需要非空运行值。单次最多 32 个输入，单字段最多 16 KiB，总计最多 64 KiB；未知字段拒绝。金额、对象、列表等类型化契约留待后续阶段。人工表单仍沿用原 text/select 字段与必填校验。

普通 `POST /api/workflows/{wid}/runs` 继续从发布版本启动，原空请求体可用；现在可以提供 `{"inputs":{"application_id":"TEST-001"}}`。原 `Idempotency-Key` 机制保留。嵌入入口继续使用自己的发布字段规范、业务单隔离和受信任审批映射，不接受通过普通输入修改业务归属或审批人。

## 草稿试跑与正式运行

```json
{
  "workflow_id": "WORKFLOW_ID_FROM_CREATE",
  "expected_revision": 2,
  "expected_hash": "HASH_FROM_READ_OR_VALIDATE",
  "request_id": "trial-application-1",
  "inputs": { "application_id": "TEST-001" }
}
```

这是 `workflow_test` 的请求，`expected_hash` 使用当前 read/validate 返回的 `definition_hash`，不由模型猜测。HTTP 对应入口为 `POST /api/manage/workflows/{wid}/test-run`。

试跑使用该份冻结草稿，不要求先发布，不修改正式发布指针。它会真实调用模型和预检允许的工具，并产生 Run 工作目录与历史；**不是模拟执行，也不会自动把外部业务数据替换成测试数据**。调用者必须选择独立输入、数据来源与可执行 Agent。

回执返回 `id/run_id`、状态、`test_run`、`draft_revision`、`definition_hash` 和原运行页面。试跑仍进入现有统一队列和历史，审批续跑保持同一 Run；显式重试遵循原新 Run 及追溯规则。取消、重试、审批继续做原权限检查，并保留试跑能力检查。维护暂停的队列会说明等待原因；queued/waiting_human 都不代表完成。

实际节点及动态分派候选执行前会再次核对捕获的能力。当前新试跑明确拒绝未接入的运行型 Skill、会话专用工具、无受控审批链的 shell/声明工具及未预授权 MCP。`readOnlyHint` 是描述信息，不能充当授权：

- MCP 只沿用宿主返回的明确 trusted 工具集合；服务端宣称只读不能免审。
- 声明工具的只读提示需要真实沙箱与审批上下文，本阶段试跑不据此放行。
- 工作目录内 write/edit 与真实外部调用的影响需要按诊断解释；不对老发布流程追加强制的新试跑门槛。

正式运行使用 `workflow_run {workflow_id,request_id,inputs?,version_id?}`；可选 version_id 必须匹配当前已发布版本。它不会发布草稿、改变审批权或变成嵌入入口。

## 精确发布

`workflow_publish` 必须提供 `workflow_id`、`expected_revision`、`expected_hash`，可提供版本 `name/desc`。服务在同一把草稿锁内比较版本、完成发布校验并快照，防止校验一版却发布另一版。

普通 publish HTTP 保留旧客户端省略两个期望字段的兼容调用；新 Agent 工具和新版画布携带完整期望值。版本内容、hash 计算规则和旧发布文件不被改写。版本回滚仍只移动原发布指针，不等于撤销草稿修改。

发布固定流程图，引用 Agent 的配置仍可能后续更新；本阶段没有实现所有 Agent/Skill/MCP 依赖版本锁定。发现不支持的业务条件、循环、组织角色来源或执行能力时，交付应列明未解决项，不能声称已完成完整业务接入。

## 数据兼容与回退边界

沿用 `workflows.json`、不可变发布版本、原 Run 文件和 `task-runs/runs.db`。试跑仅增加可选标记与追溯字段，不重写旧历史。旧注册表无需新增字段即可读取；也不自动更改现有 Agent 的能力选择。

只有实际使用新草稿工具后，注册表才增加可选 `authoring_receipts` 元数据。它与草稿在一次原子文件写入中提交，记录调用主体、受理时间、意图指纹和原回执，因此进程中断后的重试不会重复创建或重复应用修改。回执数量有上限，满时明确拒绝新请求，不自动淘汰幂等记录。

旧版二进制若使用严格 JSON 解码，可能不能读取新增的元数据或节点字段。未来 Docker 发布前必须将这个格式变化纳入回退兼容检查、保留冷备并选择兼容的新回退镜像；不能假定只切回旧镜像即可兼容所有新写入。当前阶段不部署 Docker，也不通过回写备份覆盖正在使用的数据。
