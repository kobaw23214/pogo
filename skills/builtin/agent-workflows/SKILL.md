---
name: agent-workflows
description: 为当前 Agent 创建、局部修改、删除、试跑、启用和运行私人内部工作流，支持多入多出的 Agent 与代码节点、条件分支、并行汇聚、集合迭代和有界代码循环，等待本人确认并查看真实运行记录。
---

# Agent 内部工作流

把当前用户反复使用的做法保存为当前 Agent 的内部流程。流程归属当前用户和当前
Agent，同一个 Agent 的不同对话可以复用；创建它的聊天只是来源。它独立于管理端
平台工作流，使用 `agent_workflow_*` 工具，不能改用平台 `workflow_*` 创建。

管理员在 Agent 的能力配置中显式勾选本内置 Skill，会联动启用
`agent_workflow_read`、`agent_workflow_edit`、`agent_workflow_enable` 和
`agent_workflow_run`；取消勾选则一并停用。此关联由宿主定义，工具不能单独勾选。
旧配置可能保留部分授权，以 capabilities 的实际结果为准。阅读、引用或在对话中
调用 Skill 文档本身不增加权限；也不授予文件、账号或外部系统权限。
当前 Agent 没有对应工具时，说明缺口并请有权用户调整该 Agent 的 Skill 配置；
不调用其他 Agent 代建，不读取管理员凭据，不修改共享 Agent 的权限。
工具由宿主绑定真实用户、Agent 和会话，不填写 owner_id/agent_id。

## 按需阅读的参考

本文是所有操作都要遵守的规则，也足以搭建和修改由 agent、confirm 节点组成的顺序流程。
使用下列结构前，先读对应参考再保存：

- [references/graph.md](references/graph.md)：graph 连线、条件分支、并行与汇聚。
- [references/code-nodes.md](references/code-nodes.md)：代码节点、结构化数据、单节点试跑、代码重试和备用输出。
- [references/iteration-loop.md](references/iteration-loop.md)：集合迭代、有界循环及其数据上限。

## 先发现，再保存

1. `agent_workflow_read {"action":"capabilities"}` 查看当前 Agent 的真实有效能力；
   修改已有流程前先 list/get，记录真实 workflow_id、revision、definition_hash。
   需要指定 Skill、快捷方式或选择输入资料时，先调用
   `agent_workflow_read {"action":"resources"}` 发现当前 Agent 的授权目录。
   有明确来源对话时可附 source_session_id；不能借此查看其他用户或 Agent 的资料。
2. 将需求整理为明确输入、节点连接、等待本人确认的位置和最终产物。执行先后由
   流程控制，每个 agent 节点仍可使用所属 Agent 的已授权工具、Skill 和 MCP。
3. 新流程用 `agent_workflow_edit` 的 create；已保存流程用 patch。保存结构化定义，
   不用一大段提示词或任意脚本代替流程，不把建议或模型自述当成已保存的事实。
4. `agent_workflow_read {"action":"validate", ...}` 返回结构化诊断；逐项修正后
   再检查。校验不启动模型或工具；校验通过不等于实际试跑通过。

## 全流程默认工具审批

流程可保存 `tool_approval_mode`，所有 Agent 节点（包括并行分支和集合内 Agent）
继承同一默认值，和创建它的对话审批模式分开。新运行冻结该版本的模式；修改草稿
只影响之后采用新版本的运行，不修改历史运行、续跑和重试的权限快照。

- `standard`：与对话“替我审批”一致，文件等已有安全操作自动处理，Bash 等敏感工具仍需批准。
- `strict`：与对话“请求批准”一致，修改型工具需要批准。
- `auto`：与对话“完全访问”一致，已授权工具的常规调用不再逐次审批。

没有该字段的旧流程继续按 `standard` 执行，不回填或自动升级。只有用户明确选择
时才设置该字段；不能为了消除报错、卡片或等待而自行改成 auto。创建时用 create
顶层字段；修改时用 `{"op":"set_metadata","tool_approval_mode":"strict"}`，并保留
准确 expected_revision。编辑操作仍遵守当前对话的真实工具审批和既有编辑授权。
此设置只决定已有能力是否逐次询问，不授予缺少的工具、Skill、文件或外部系统权限。
本人确认节点始终等待本人操作；代码节点的隔离和失败重试策略不受此字段影响。

## 读取定义与运行记录

三个 ID 分开记录，不能混用：`workflow_id` 来自定义的 `workflow.id`，也可从运行回执
的 `run.workflow_id` 取得；`run_id` 来自 `run.id`；`step_id` 来自定义节点的 `step.id`
或运行记录的 `run.steps[].step_id`。只有 run_id 不能查询某次运行。
下面占位符必须替换为工具真实返回的值；每次调用发送一个 JSON 对象：

- 获取已保存定义：`agent_workflow_read {"action":"get","workflow_id":"WORKFLOW_ID"}`。
- 列出该流程运行：`agent_workflow_read {"action":"runs","workflow_id":"WORKFLOW_ID"}`。
- 查询某次运行：`agent_workflow_read {"action":"run","workflow_id":"WORKFLOW_ID","run_id":"RUN_ID"}`。
- 读取该次运行的节点产物：`agent_workflow_read {"action":"artifact","workflow_id":"WORKFLOW_ID","run_id":"RUN_ID","step_id":"STEP_ID"}`。
- 校验已保存流程：`agent_workflow_read {"action":"validate","workflow_id":"WORKFLOW_ID"}`；
  可同时把 get 返回的 revision 填入 expected_revision，检查指定草稿版本。

`capabilities`、`list`、`resources` 不需要流程 ID。校验未保存的候选时提供 `definition`；不要用
一个运行 ID 代替流程 ID，也不要把工具调用 ID 当作 workflow_id、run_id 或 step_id。

没有 graph 的旧定义保持顺序 agent/confirm 步骤，新定义也可增加 code 节点。显式 graph.version=1 支持
condition 条件、parallel 并行分出和 join 汇聚；有界循环通过 loop 容器表达，图本身不接受回边，也不支持跨 Agent 编排，
不能让节点故意失败模拟分支。输入值使用字符串，模板只做数据替换，不能执行表达式。
保存的是当前 Agent 的同一份定义；对话修改会同步到节点画布，画布布局不改变流程版本。

## 自然语言搭建与执行方式选择

用户描述目标后，主动规划明确的输入、数据传递和节点，不要求用户先手工添加节点。
需要已有能力时先 resources 发现真实授权目录，再选择与任务相关的 Skill、快捷方式。
理解、推理、多轮工具操作使用 agent 节点；固定计算、格式转换、规则校验使用 code 节点。
代码是已保存版本的一部分，运行时宿主直接执行它，不让模型每次重新生成和解释脚本。
画布用于查看实际定义及精确调整；对话保存、局部修改和校验均操作同一份定义。

code 节点不继承 Agent 工具、Skill、快捷方式、账号、文件系统或网络权限，不能通过代码
绕过工具审批。需要访问业务系统时由已有授权的 Agent 工具/MCP 完成，再把结果作为数据传入。
code 节点的字段、试跑和失败处理见 [references/code-nodes.md](references/code-nodes.md)。

## 输入表单与显式资源

输入按实际交互选择类型，不要求用户把所有参数手动输入成文字：

- `type:"text"` 用于报告主题等自由文本；旧定义不写 type 继续作为文本，不迁移历史。
- `type:"boolean"` 用于是否需要本人确认等是/否选择。实际输入仍是字符串
  `"yes"` / `"no"`，不是 JSON true/false，也不是显示文字“是/否”。不写 options
  时使用默认两项；指定中文标签时 options 必须完整包含 yes/no 两个值。
- `type:"select"` 用于报告读者等已知选项，填写
  `options:[{"value":"management","label":"管理层"},{"value":"team","label":"项目组"}]`。
  提交 value，不提交 label。`default` 也是字符串，必须是合法选项值；只在参数省略时
  使用，用户显式清空必填项仍会报错。
- 输入资料独立使用授权工作区的文件选择。运行工具的 files 仍传实际选中文件的相对
  路径，由宿主校验并保存本次资料快照；不把任意绝对路径包装成一个 select 参数。

需要某个节点明确使用 Skill 或快捷方式时，把 resources 返回的稳定 ID 写入该 agent
节点的 `skills:["实际SkillID"]` / `shortcuts:["实际快捷方式ID"]`。这些占位符不能直接
提交，必须换成当前授权目录的真实值。不能用显示名称、对话里的“按某Skill处理”或
手写 `/skill:名字` 代替结构化绑定；也不能把普通对话中的 Skill 引用偷渡成授权。
只允许 agent 节点绑定，confirm/condition/parallel/join 不配置这些字段。

已有节点用 update_step 的 patch 修改绑定；传空数组取消显式绑定。绑定只选择已经
授权的指导和材料，不授予工具、MCP、账号或外部系统权限。未显式绑定的旧节点保留
原有继承行为。试跑/正式开始时冻结本次所选资源，历史 Run 保留自己的材料；能力变化
不能让旧 Run 静默切换到新能力或把历史重试说成无影响续跑。

创建例子（名称、参数和文件应来自用户的实际需求）：

```json
{
  "action": "create",
  "request_id": "research-flow-create-1",
  "name": "资料整理与报告",
  "description": "整理选定资料，核对提纲后生成报告",
  "inputs": [
    {"key": "topic", "label": "报告主题", "type": "text", "required": true},
    {"key": "audience", "label": "报告读者", "type": "select", "required": true, "default": "team", "options": [{"value": "management", "label": "管理层"}, {"value": "team", "label": "项目组"}]}
  ],
  "steps": [
    {"id": "collect", "kind": "agent", "title": "整理要点和来源", "task": "阅读本次显式选定的资料，围绕 {{ inputs.topic }} 整理事实、来源及冲突，不编造未提供的材料。", "output_file": "notes.md"},
    {"id": "outline", "kind": "agent", "title": "生成提纲", "task": "依据整理后的资料和来源生成报告提纲。", "output_file": "outline.md"},
    {"id": "review", "kind": "confirm", "title": "核对提纲", "prompt": "请核对本次资料和提纲，确认或补充意见后继续生成报告。"},
    {"id": "report", "kind": "agent", "title": "生成报告", "task": "根据本次资料、提纲和用户确认意见，为 {{ inputs.audience }} 生成有来源说明的完整报告；明确资料缺口。", "output_file": "report.md"}
  ]
}
```

步骤不是其他 Agent 的引用，不传 agent/model/credentials；宿主使用当前所属 Agent
的运行环境。output_file 是相对本次 Run 工作目录的预期产物，不是任意绝对路径。
成功必须有真实执行结果和产物检查，不能只凭一句“完成了”。

## 局部修改

保留步骤 ID，将 get 返回的 revision 填入 expected_revision，按一次编辑意图使用唯一 request_id。
同一意图因网络等原因重试复用原键；不同意图换键。版本冲突时先读取当前定义，
解释变更再调整，不盲目覆盖整份流程。

```json
{
  "action": "patch",
  "workflow_id": "ID_FROM_GET",
  "expected_revision": 2,
  "request_id": "research-flow-check-sources-1",
  "operations": [
    {"op": "add_step", "index": 1, "step": {"id": "check", "kind": "agent", "title": "检查资料冲突", "task": "检查资料之间的冲突，区分事实和推测，补充到来源说明。", "output_file": "source-check.md"}},
    {"op": "update_step", "step_id": "outline", "patch": {"task": "结合资料要点与冲突检查结果生成提纲。"}}
  ]
}
```

其他操作为 `set_metadata`（name/description/inputs）、`remove_step(step_id)`、
`move_step(step_id,index)`。update_step 使用 step_id 和 patch，patch 不能修改 id；
不要给 update_step 填一个 step 对象。index 从 0 开始。

`patch.code` 保留局部更新：只改 source 时，未提供的 language、timeout_seconds、
inputs、outputs 保持不变。显式提供 inputs 或 outputs 时，该字段的整个映射替换旧值，
因此重命名或删除字段要提交修改后的完整映射；inputs:{} 可清空参数，outputs:{}
因缺少输出声明会被拒绝。同步修改 source 和引用该输出的后继节点，再校验最终定义。

图流程还支持 `{"op":"set_graph","graph":{"version":1,"edges":[...]}}`。
新增、删除或重新连接节点时，把节点操作和 set_graph 放在同一 operations 批次；
宿主会对最终完整定义校验，任一失败则不保存部分修改。graph 不能设为 null 来隐式
降回顺序流程。已有图中 move_step 只调整节点清单位置，改变执行先后必须同步改连接。
修改 condition 用 update_step.patch.condition；改成其他类型时明确清空 condition。

## 试跑、本人确认与启用

只有用户要求真实执行或试跑时才启动。使用明确选定的独立输入与文件；不默认复制
整段聊天或暗中使用真实业务资料。files 为授权的 Agent/来源聊天工作区相对路径，
由宿主验证并复制到本次 Run。先查看真实材料，不猜文件路径。

`agent_workflow_run` 的 trial 需要 workflow_id、request_id、expected_revision、
expected_hash（read/validate 返回的精确 64 位哈希）和可选 inputs/files。这是
**实际模型和工具执行**，不是无副作用的模拟；不改变启用版本。

启动回执只表示受理。保留回执中的 `run.workflow_id` 和 `run.id`，用
`agent_workflow_read {"action":"run","workflow_id":"WORKFLOW_ID","run_id":"RUN_ID"}`
跟踪真实状态；runs 也要提供 workflow_id。artifact 再加真实 step_id，读取该步骤
记录的产物，不接受任意路径。

`waiting_confirmation` 表示等待当前使用者在流程面板确认或补充意见，尚未完成。
并行图可能同时存在多个待确认节点；逐个展示真实待确认步骤，其他可执行分支可以继续。
提供运行入口和等待内容，不能自己确认、伪造用户决定或调用工具替本人确认继续。
这不同于组织审批；当前用户确认不需要另一个审批账号，也不免除原工具调用审批。
已完成步骤不重做；遇到 interrupted/结果待核查，先读取原记录，不自动重发操作。

用户要求启用时，重新核对试跑结果与所审阅版本，调用 `agent_workflow_enable` 的
enable，携带 workflow_id、expected_revision、expected_hash。草稿变更后不能拿旧
试跑结果当作新版本通过。结构校验与真实试跑分别报告，未试跑要说明。

启用只是 Agent 内部可执行版本，不是对外发布、嵌入其他系统或自动设置定时任务。
之后 start 从启用版本启动，可用 version_id 约束；disable 不隐式取消已在运行的
实例。cancel/retry 针对实际 run_id；显式 retry 创建有追溯关联的新 Run，可能再次
执行已经产生的外部效果，不能把重试说成撤销或无影响续跑。

完成时给出保存的流程、当前草稿与启用版本、真实 Run 状态、产物和待用户处理项。
运行中、排队和等待确认均不能称为报告已生成。执行会话不能创建或递归运行内部
流程；流程管理应由当前用户的正常 Agent 对话发起。

## 删除内部流程

用户明确要求删除某条流程时，先 list/get 定位当前 Agent 下唯一的真实 workflow_id 与
最新 revision，再调用：

```json
{"action":"delete","workflow_id":"真实workflow_id","expected_revision":1,"request_id":"delete-unique-intent"}
```

此调用使用 `agent_workflow_edit` 的现有编辑授权。删除会从正常列表移除并停用流程，
保留原有版本、运行、产物和审计记录，不物理清理历史。`agent_workflow_read` 的
`{"action":"list","include_deleted":true}` 可列出含已删除/归档记录；原 get/runs/run
仍可读取历史。已删除流程不能继续编辑、恢复为草稿、启用、试跑或重试。

存在排队、执行中、等待本人确认或尚未退出的取消执行时，宿主会拒绝删除。说明
当前运行，并请用户明确决定是否先结束运行；不能把“删除流程”自行解释为取消运行。
同一删除意图重发相同 request_id；冲突时重新读取，不覆盖新版本。不要用 bash、
数据库或平台工作流工具绕过该边界。
