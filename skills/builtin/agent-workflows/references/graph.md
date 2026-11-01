# 通用节点连线、条件与并行

需要分支、并行或多入多出时阅读本文。修改已有图的 set_graph 规则见
[../SKILL.md](../SKILL.md) 的“局部修改”。

需要分支时，在定义中显式添加 `graph`。`steps` 仍保存稳定节点 ID 和配置，执行关系
由 `graph.edges` 决定，不能靠数组次序或移动画布位置暗示分支。入口为 `__input`，
出口为 `__output`；所有真实节点都必须可从入口到达并连接到出口，图不能有环。

- agent/code/confirm/iteration/loop 节点至少一入一出，均可多入多出；开始可多出，
  输出可多入。condition 至少一入，`branch:"true"` 和 `branch:"false"` 各至少
  一条出边，每个条件分支可连接多个后继；parallel 至少一入两出，join 至少两入一出。
- 一条节点的多个出口同时分发；多条路径可直接连接到同一个普通节点，无需为了
  连接两入一出而强制增加 join。每个节点在同一 Run 中只执行一次。
- condition 只接受 `condition:{source,operator,value?}`。source 是不带花括号的
  `inputs.key` 或保证已完成的 `steps.id.summary/artifact/feedback`。
  operator 为 equals/not_equals、contains/not_contains、is_empty/not_empty，
  或 gt/gte/lt/lte。比较值是字面字符串，数值比较须能解析为有限数；无效数值会报错，
  不能把它解释为 false。空值判断不填 value。不要传任意代码、路径、模型判断提示词。
- 未选中的条件路径记录为 skipped；所有多入节点等待每条输入路径有明确结果，
  有实际激活的输入才执行，全部未激活则继续传播 skipped；失败不能被当成 skipped。
  join 还会汇聚实际已完成的分支。未选路径不会启动模型或工具。并行节点使用同一 Agent 的既有授权，
  不创建其他 Agent，也不扩大工具或审批权限。
- 普通引用只能读取保证完成的上游。无条件并行汇入同一节点时，可引用双方已完成的
  输出；条件分支可能跳过的数据，应由 join 汇集，再引用 `{{ steps.join_id.summary }}`
  或相应结构化字段。复杂分支分析会保守拒绝不能保证存在的引用，不要绕过校验。
  节点产物文件名仍须全图唯一，避免分支写到同一个预期输出。图流程保留 `inputs/`
  目录存放冻结资料，产物不能使用该目录或其大小写变体。

这个例子并行整理事实与风险，按输入决定是否等本人确认，最后生成报告：

```json
{
  "action": "create",
  "request_id": "parallel-research-create-1",
  "name": "并行资料整理与条件确认",
  "inputs": [{"key":"need_review","label":"是否需要本人确认","type":"boolean","required":true,"default":"yes","options":[{"value":"yes","label":"需要确认"},{"value":"no","label":"无需确认"}]}],
  "steps": [
    {"id":"gather","kind":"parallel","title":"并行整理"},
    {"id":"facts","kind":"agent","title":"整理事实","task":"阅读显式选定资料，整理事实和出处。","output_file":"facts.md"},
    {"id":"risks","kind":"agent","title":"整理风险","task":"阅读显式选定资料，整理风险和资料缺口。","output_file":"risks.md"},
    {"id":"gathered","kind":"join","title":"汇总整理结果"},
    {"id":"route","kind":"condition","title":"是否需要本人确认","condition":{"source":"inputs.need_review","operator":"equals","value":"yes"}},
    {"id":"review","kind":"confirm","title":"本人确认","prompt":"请核对资料和风险：{{ steps.gathered.summary }}"},
    {"id":"reviewed","kind":"join","title":"汇总确认路径"},
    {"id":"report","kind":"agent","title":"生成报告","task":"根据资料汇总 {{ steps.gathered.summary }} 和确认路径结果 {{ steps.reviewed.summary }} 生成报告，保留来源。","output_file":"report.md"}
  ],
  "graph": {"version":1,"edges":[
    {"source":"__input","target":"gather"},
    {"source":"gather","target":"facts"}, {"source":"gather","target":"risks"},
    {"source":"facts","target":"gathered"}, {"source":"risks","target":"gathered"},
    {"source":"gathered","target":"route"},
    {"source":"route","target":"review","branch":"true"},
    {"source":"route","target":"reviewed","branch":"false"},
    {"source":"review","target":"reviewed"},
    {"source":"reviewed","target":"report"}, {"source":"report","target":"__output"}
  ]}
}
```

条件可以直接连到 join，表达这条路径没有业务节点；并行的两条完全重复连接不合法。
给多个并行节点设置本人确认时，以 Run 中各个待确认 step_id 为准，不能只盯一个顺序位置。
