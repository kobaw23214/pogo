# 代码节点

使用 code 节点、代码 body 或代码失败处理时阅读本文。集合迭代与循环本身见
[iteration-loop.md](iteration-loop.md)。

## 代码节点和结构化数据

`kind:"code"` 的 `code` 包含：

- `language:"python"` 或 `"javascript"`，`source` 最多64KiB。
- `inputs` 是函数参数对象的字段映射，每个字段声明 `type`，并且只填 `source` 或 `value`
  其中一项。`source` 可用 `inputs.orders`、`steps.calculate.output.total`，以及既有
  `steps.ID.summary/artifact/feedback`。只读保证已完成的前序节点，不执行表达式。
- `outputs` 是返回对象的字段及类型，每个顶层字段都必填，不能返回未声明的字段。
  支持 string/number/integer/boolean/object/array；数组声明 `items`，对象可声明
  `properties` 和 `required`。结构schema最多8层。完整代码输入、输出各最多256KiB。
- `timeout_seconds` 为1–60秒，省略时默认10秒。执行环境不可用时如实报告，不能改用
  bash 或未隔离执行来冒充代码节点成功。

Python 使用 `def main(inputs):`，JavaScript 使用 `function main(inputs) { ... }`，返回JSON对象。
参数按数据传递，不把用户输入拼接进源代码；不要依赖运行时随意安装第三方库。
代码节点不设置 `output_file`，返回的结构化结果保存在运行的 `output` 中；需要文件产物时，
由后续有文件工具授权的 Agent 节点把该结果保存为文件，并声明对应的 output_file。

```json
{
  "id": "calculate", "kind": "code", "title": "计算订单总额",
  "code": {
    "language": "python",
    "source": "def main(inputs):\n    return {'total': sum(inputs['amounts'])}\n",
    "inputs": {"amounts": {"type": "array", "source": "inputs.amounts"}},
    "outputs": {"total": {"type": "number"}}
  }
}
```

流程输入可增加 `type:"number"`、`"integer"`、`"object"`、`"array"`；原生JSON值可直接
提交给运行工具，宿主仍使用兼容的字符串形式保存旧Run字段。旧 boolean 的 yes/no 表单和
存储不变，进入代码输入边界时才转为真实JSON布尔值。text/select 在代码中是字符串。
code 输出可被下游模板、条件和代码绑定引用，包括受限的 `.字段.数组下标` 路径。
含结构化 output 引用的模板展开合计最多1MiB；传递必要字段，避免反复插入整份大对象。
普通多入节点会等待各上游路径解析完成。对保证执行并已完成的并行上游，可以直接引用
`steps.CODE_ID.output.field`；可能被条件跳过的分支仍不能当作必有输入。需要汇集
实际运行的分支时用 join：`steps.JOIN.output.CODE_ID.field`，仅存在成功完成分支的数据。

## 单节点试跑与对话修复

先保存并读取精确 revision/hash，再调用：

```json
{"action":"test_node","workflow_id":"真实ID","request_id":"唯一试跑意图","expected_revision":1,"expected_hash":"真实64位hash","step_id":"calculate","node_inputs":{"amounts":[10,20.5]}}
```

本阶段仅支持单独试跑 code 节点。node_inputs 是已解析的函数参数，字段和类型必须与
code.inputs 完全一致；不会替你执行上游，也不会隐式借用其他Run的输出。
宿主保留独立试跑Run和冻结版本，其他节点标记跳过，不改变启用版本。
通过 read run 查看实际 `code_inputs`、`output` 和 `code_execution`：耗时、日志、退出信息、
超时和截断标记。根据真实错误用 update_step 的 `patch.code` 修改草稿，重新校验与试跑；
不声称已运行尚未执行的上下游，不把局部通过当成完整流程通过。本人确认仍必须由实际用户操作。

## 代码重试和备用输出

只有 code 节点（含代码 body）可以配置 `policy`：

```json
{"max_retries":1,"retry_delay_ms":200,"on_failure":"fallback","fallback":{"amount":0}}
```

max_retries 为 0–2，retry_delay_ms 为 0–5000。只重试实际脚本失败，不重试取消、
权限撤销、存储失败或 Agent 外部操作。on_failure 默认为 stop；fallback 需要提供
与 code.outputs 完全匹配的 JSON 对象。备用输出必须明确告知用户，不能把原脚本
失败报告成脚本成功；实际 attempt、日志、错误及 fallback_used 都在运行记录中保留。
