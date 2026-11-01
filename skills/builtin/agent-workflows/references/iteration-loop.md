# 集合迭代与有界循环

使用 iteration 或 loop 时阅读本文。代码 body 的字段、试跑和 policy 见
[code-nodes.md](code-nodes.md)。

先根据目标选择执行结构：固定的几条独立任务用 parallel；对数组中的每一项重复同一
做法用 iteration；让一个结构化状态反复更新直到满足条件用 loop。保持图为 DAG，
不要手写回边或用失败来模拟循环。创建前先发现 capabilities 和需要的资源；所有配置
都通过 create/patch 保存到同一份定义，画布只是这份定义的视图。

## 集合迭代

`kind:"iteration"` 配置 `iteration:{items,body,concurrency?,max_items?,on_item_error?}`：

- items 必须为 array 类型的 CodeBinding，填写唯一的 source 或 value。source 只引用
  声明的流程输入或保证已完成的上游数据；数组超过 max_items 时拒绝，不暗中截断。
- body 只能是单个 agent 或 code，不嵌套确认、分支、迭代或循环。body.id 可以省略；
  宿主以外层节点 ID 归属资源和运行记录。容器与 body 都计入 200 个定义节点上限。
- body 内可引用 `iteration.item`、`iteration.item.字段/数字下标` 和零基
  `iteration.index`，也可引用外层 inputs 和保证完成的前序结果。局部引用仅在 body
  有效。模板中的数据只替换一次，资料中的 `{{ ... }}` 不会被当作下一轮模板执行。
- concurrency 默认为 1，最大 10，与整条 Run 的其他执行节点共享并发上限。
  max_items 默认为 100，最大 100；单项输入不超过 64KiB。
- on_item_error 默认为 stop；continue 会记录失败项，继续处理其他项。输出
  `items` 按原输入顺序排列，失败位置为 null，另有 `errors:[{index,error}]`、
  total、completed、failed。查看失败明细后再声称批处理全部成功。
- 每个成功项保留 body 返回的完整 JSON 对象，不会自动展开为数字。例如 body 返回
  `{"line_total":40}`，下游收到的是 `[{"line_total":40},null,...]`；汇总时先跳过
  null，再读取每项声明的 `line_total` 字段，不能直接对 items 求和。失败条数使用
  聚合输出的 failed 或 errors 的长度，失败明细仍保留在 errors。
- Agent body 沿用当前 Agent 的工具审批、Skill、快捷方式和 MCP 权限；显式资源仍用
  先发现后绑定的稳定 ID。每项有独立会话/工作目录和真实记录。失败不会自动重试
  Agent 的外部工具操作。

代码批处理示例（创建或 patch 中的一个节点）：

```json
{
  "id": "calculate_orders",
  "kind": "iteration",
  "title": "逐单计算金额",
  "iteration": {
    "items": {"type":"array","source":"inputs.orders"},
    "concurrency": 2,
    "max_items": 100,
    "on_item_error": "stop",
    "body": {
      "kind": "code",
      "code": {
        "language": "python",
        "source": "def main(inputs):\n    return {'amount': inputs['quantity'] * inputs['price']}\n",
        "inputs": {
          "quantity": {"type":"number","source":"iteration.item.quantity"},
          "price": {"type":"number","source":"iteration.item.price"}
        },
        "outputs": {"amount":{"type":"number"}}
      }
    }
  }
}
```

下游可用 `steps.calculate_orders.output.items` 取得结果数组。Agent body 例如
`{"kind":"agent","task":"核对第{{ iteration.index }}项资料：{{ iteration.item }}，说明缺口。"}`；
如需资源，在这个 body 上绑定 skills/shortcuts，不把能力放在容器上。Agent逐项产物的输出引用使用
`iteration-results/外层节点ID/下标/产物相对路径`，宿主将真实快照复制给后继节点。
含iteration的新定义把iteration-results作为保留输入目录，外层Agent和body都不能
将output_file声明到这个目录；旧无iteration定义的原路径与Hash保持不变。读取逐项快照时用
`agent_workflow_read {"action":"artifact","workflow_id":"实际流程ID","run_id":"实际RunID","step_id":"外层迭代节点ID","item_index":0}`，
item_index来自对应Repeat.Items的零基index；不猜快照路径或借此读其它Run的文件。

## 有界循环

`kind:"loop"` 配置 `loop:{initial,body,until,max_iterations}`。本期 body **只支持 code**，
不把 Agent 自述当成结构化状态或循环停止依据。initial 是 object 类型 CodeBinding；
body 输入可绑定 `loop.state`、其字段/数字下标和零基 `loop.index`。每轮成功返回的
完整对象替换当前 state，所以要返回下一轮仍需要的全部状态字段。
单次 state 最多64KiB，所有轮次的执行记录合计最多2MiB；只向下游传递最终 state。

until 使用与条件节点相同的字面量比较，source 只能为 `loop.state.字段` 或
`loop.index`；在每轮 body 成功之后判断。max_iterations 必填且为 1–100。
达到上限仍未满足退出条件时 Run 失败，并保留最终 state 与每轮记录；不能称循环成功。
成功时输出 `{state,iterations,limit_reached:false}`，下游引用
`steps.循环节点ID.output.state.字段`。不设置无限循环，不调用任意表达式作为退出条件。

## 试跑与数据上限

本期单节点 test_node 仍只支持外层 code 节点。验证 iteration/loop 时发起完整草稿
trial，不声称它们已做单节点试跑。聚合 output 不超过 256KiB，单项所有 attempt
日志合计 8KiB并标记截断，单 repeat 记录 2MiB，完整 Run 沿用 8MiB边界；Agent
逐项摘要超过 8KiB会明确失败。需要大文件时用授权的 Agent 工具生成产物，代码节点
仍只接收显式数据，不因新增编排结构获得文件、联网或平台工作流权限。
