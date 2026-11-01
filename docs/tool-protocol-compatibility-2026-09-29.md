# 模型工具协议兼容：配置、诊断与验收

初版：2026-09-29；更新：2026-09-30。调研依据、变更范围与验收记录见 [工具调用稳健性调研](tool-call-robustness-research-2026-09-30.md)。

本文描述当前实现。原 text-tool-call-compat-2026-09-29.md 保留为问题提案；其中“全局宽松解析、自动补全参数、在事件前修改消息”的规格不作为实现依据。

## 1. 操作顺序

1. 编译本次代码，在隔离环境使用待验收的模型配置。
2. 在 providers.json 的对应 models 条目中设置 compatibility，保留其他字段。
3. 先运行下方 toolprobe。它仅暴露本地随机值工具，不连接 Yw/MCP，也不执行文件、命令或业务操作。
4. 探针通过后，再用相同端点、模型、思考参数，在新的 Agent 会话中测试 Yw 只读查询。
5. 核对实际工具记录、参数、Yw 请求记录、最终回答、会话刷新后的工具结果。全部通过才进入正常发布流程。

本次实现不会自动切换任何现有模型配置，也不会自动部署。

## 2. 配置

### 原生路径

未配置 compatibility 时保持原生 Chat Completions。若模型把独立的调用标记写进正文，Pogo 只请求一次纠正；第二次失败以 tool_protocol 结束，不能作为任务完成。

DeepSeek 思考模式且端点要求回传推理内容时：

~~~json
{
  "compatibility": {
    "tool_calls": "native",
    "preserve_reasoning": true
  }
}
~~~

这是模型条目的配置片段，不是完整 providers.json。依据是 [DeepSeek 官方思考模式说明](https://api-docs.deepseek.com/guides/thinking_mode/)：带工具的后续请求需要回传 reasoning_content。不要仅凭模型名字自动套用此行为；GLM 或自建网关按实际端点验收。

preserve_reasoning 从现有 thinking 块构建 assistant.reasoning_content，不会把推理正文拼进 content。没有下发工具时不回传。已丢失或摘要掉的历史推理不能重建，切换配置后的首次验收使用新会话。

### 截图中的 XML 方言

确认端点会输出 XML，且能够接受原生 assistant.tool_calls / role:tool 历史时：

~~~json
{
  "compatibility": {
    "tool_calls": "xml",
    "tool_history": "native",
    "allow_mcp_aliases": true,
    "allow_text_preamble": true,
    "preserve_reasoning": true
  }
}
~~~

preserve_reasoning 根据端点选择，不能机械照抄。其他字段含义：

| 字段 | 行为 |
|---|---|
| tool_calls | native（默认）、xml、json；没有自动猜格式的全局模式 |
| tool_history | native（默认）发送标准工具声明和历史；text 显式发送文本声明、文本调用历史及结果 |
| allow_mcp_aliases | 默认 false；仅在本轮可见的 MCP 工具中，原始名称唯一时转换为规范名称 |
| allow_text_preamble | 默认 false；仅 xml/json 模式可开启，允许最多 4096 字节的普通前言后接独立、完整调用块；保留前言，不接受尾随说明、围栏、引用或内联示例 |
| preserve_reasoning | 默认 false；带工具的对话按端点要求回传已保存的推理内容 |

XML 接受三种已观察结构：`<tool_call><invoke name="..."><parameter name="...">...</parameter></invoke></tool_call>`、`<function_calls><invoke name="...">...</invoke></function_calls>`，以及 `<tool_call><tool_name>...</tool_name><parameters>{"参数":"值"}</parameters></tool_call>`。第三种结构的 parameters 必须包含严格 JSON 对象，保留 JSON 类型；XML 文本中的特殊字符需要转义。

已知外层 `</tool_action>` 错配仅在 tool_call 外层允许；未闭合的内层或外层块不会自动补全（下述 Qwen function 结构可省略外层 `</tool_call>` 除外）。string="true/false" 仅作为已知 invoke 方言提示忽略，参数类型由 schema 决定。function_calls 中可包含多个完整 invoke，无参数调用显式写成 `<invoke name="..."></invoke>`。

2026-10-31 起 XML 还接受 Qwen 模板的 function 结构：`<tool_call><function=名称><parameter=参数>值</parameter></function></tool_call>`。本地 Qwen 端点实测会把它和声明格式混用，因此同时接受 `<parameter name="参数">` 开标签、用 `</invoke>` 代替 `</function>`、省略外层 `</tool_call>`。省略只允许出现在 function 已闭合之后，后面只能是下一个调用块或响应结束。参数值按原文处理，不做 XML 反转义（与 Qwen 模板及 vLLM、llama.cpp 的解析一致），只去掉模板在值两侧各加的一个换行。值里出现下一个 parameter 开标签或 function 结束标签，说明漏写了 `</parameter>`，整批拒绝。schema 为 boolean 时，额外接受 Qwen 模板回放历史时写出的 `True`/`False`。

同一端点会在思考中第一次提到 `<tool_call>` 时提前结束推理通道，把剩余推理连同模型自己的 `</think>` 一起放进正文。xml/json 模式下，如果响应带推理内容，且正文中有单独成行、不在 Markdown 围栏内的 `</think>`，这一行之前的正文并入思考块，之后才按回答或调用处理。思考中起草的调用不会执行。

默认仍要求响应整体为调用块；有“我来查询……”前言时会请求一次纠正。仅在实际端点验收确实需要时显式开启 allow_text_preamble。该开关不能判断自然语言是否在举例，不能作为任意正文自动执行器；例子必须放在 Markdown 围栏中。无论开关如何，整个调用批次都先校验，失败时前半批也不会执行。

### 带标记 JSON / 纯文本端点

返回格式：

~~~text
<tool_call>{"name":"规范工具名","arguments":{"compact":true}}</tool_call>
~~~

对应配置为 tool_calls: json。若端点连原生工具声明和历史都不支持，再显式设 tool_history: text。该模式将声明写入系统提示，调用及结果编为文本，不再发送 tools 数组和 role:tool，必须单独验证完整闭环。

JSON 不修复单引号、尾逗号、缺失括号或缺失参数。复杂嵌套参数和复合 schema 优先使用 JSON。XML 只对直接声明的 boolean / integer / number / object / array / null 参数做明确转换；类型不明确时保留字符串并由 schema 判定，不能猜测。

## 3. 端点探针

在 pogo 仓库根目录运行，-model 必须是已有配置中的 ID 或别名：

~~~powershell
go run ./cmd/toolprobe -model deepseek-v4-flash -timeout 3m
go run ./cmd/toolprobe -model glm-5 -timeout 3m
# 完整契约检查：两个独立场景各重复 3 次，超时预算属于整个套件
go run ./cmd/toolprobe -model glm-5.3 -suite contract -repeat 3 -timeout 5m
~~~

如配置文件不在默认位置，沿用已有 POGO_CONFIG 设置。密钥从 provider 的 key_env 读取，不写入命令、配置片段或报告。

2026-09-30 的工具稳健性镜像同时内置 `pogo-toolprobe`，部署后无需 Go 环境即可沿用容器内现有配置运行，例如：

~~~sh
docker exec pogo-base pogo-toolprobe -model deepseek-v4-flash-channel-int8 -suite contract -repeat 3 -timeout 5m
~~~

上述 model ID 必须与现场配置一致；命令仅调用模型及本地诊断工具，不执行 Yw 业务操作。

探针使用同一个生产流式客户端、同一模型兼容配置。basic 场景：无参数工具 → 本地产生未知随机值 → 回填 → 检查答案是否使用该值。contract 套件在 basic 之外增加 tool 字符串、limit 整数、compact 布尔值的参数场景，可发现 tool_name 替代 tool、布尔值字符串化等问题。每个独立场景最多 4 次模型请求、1 次协议或参数纠正、1 次本地工具执行，不连接业务系统。

默认 basic 单次模式保持原报告结构，通过条件为退出码 0、passed=true、result_consumed=true。contract 或 repeat>1 返回套件报告：model、实际 compatibility、requested/completed、first_pass、recovered 和 cases；每个 case 都必须 passed/result_consumed=true，全部完成才整体通过。first_pass 指无需纠正的完整闭环数，recovered 指纠正后通过数。重复上限 20；超时未完成的场景不能算成功。

报告不输出密钥、业务数据、推理正文或参数。探针仍不验证大目录发现、Agent 系统提示、权限或业务语义；这些由本地 Agent 集成测试和真实只读业务验收补充。

退出码 1 表示闭环失败；退出码 2 表示配置或命令错误。探针验证流式协议，不证明网关非流式路径一致，也不证明 Yw 的业务权限和数据正确。Studio 若额外调整思考参数，最终必须在 Studio 相同设置中复验。

## 4. 执行与恢复边界

- 原生调用始终优先，同一响应中的文本不会再次解析成额外调用。
- 解析只针对本次 assistant 输出；Markdown 围栏、引用块和内联示例不会直接成为动作。诊断识别 tool_call/function_calls/tool_calls 及独立的残缺标记；识别不等于支持执行。普通前言需 allow_text_preamble 显式开启，默认混合响应只请求一次纠正。
- 只接受本轮已经下发的工具。MCP 别名不能激活隐藏工具，不能跨服务器猜测，不能绕过冻结绑定或原有审批。
- 一条文本调用响应整体校验；其中任何调用失败，整个批次都不执行，不做“执行成功解析的前半段”。
- 严格检查完整 JSON Schema（含嵌套、必填、范围和 additionalProperties）；禁止 schema 加载外部 URL/文件。继续经过原有执行层参数校验及审批。
- 保留 JSON 数字精度。输入限制 256 KiB、每响应最多 8 次文本调用、JSON 深度最多 32、数字字面量最多 128 字符、指数绝对值最多 1000。超出范围明确拒绝。
- 截断、取消、坏流和传输错误不能升级成可执行调用。已经收到的文本即使暂未显示，也会阻止 HTTP 自动重试。
- 文本调用 ID 使用 UUID，跨运行不重复，写入历史后保持稳定。保存 tool_origin / original_name 用于本地追踪；这些字段不发送给模型。
- 归一化在客户端返回前完成，早于 Agent 写历史和发消息事件。工具结果仍与调用按原有机制配对。
- 文本兼容模式暂缓正文显示直到回复完整，思考和原生工具预览仍可流式显示。这一版本会降低普通正文的首字展示速度，避免未确认的 XML 先显示为最终答案。
- 一次运行仅有一次协议纠正机会。仍失败时 end_reason=tool_protocol，disposition=intervene，不会静默完成或无限重试。
- 模型纠正不会重放已经发出的工具。业务调用超时后结果未知，仍按现有结果/错误链路处理，不能承诺跨网络故障的 exactly-once。
- extra_body 不允许覆盖 messages / tools / functions / function_call / stream / model，配置加载时即报错。采样参数等扩展仍保留原行为。

## 5. 回滚与停用

1. 只关闭文本兼容：将 tool_calls 改为 native，tool_history 改为 native，删除 allow_mcp_aliases 和 allow_text_preamble。
2. preserve_reasoning 独立管理：端点需要它时不要随文本兼容一起关闭。
3. 若要恢复全部旧配置，移除 compatibility 并使用原版本二进制/镜像；保留会话数据，不重放已执行调用。
4. 保存配置后按原有模型配置生效方式重新创建客户端/会话，并先跑探针再验证业务。

## 6. 本地验证

专项测试覆盖规范与错配 XML、JSON、多调用、唯一/歧义名称、隐藏工具、参数类型及精度、嵌套 schema、重复键、截断、取消保护、围栏和引用、原生优先、纠正预算、审批拒绝、结果回填、持久化重载，以及探针在原生/XML/JSON/纯文本模式下的闭环。

验收时分别报告：静态检查、Windows 本地测试、Linux 测试、竞态测试、构建、真实端点验证、部署。不能用其中一项替代其他项。

对应源码：llm/toolprotocol*.go、llm/toolprobe.go、llm/openai.go、llm/convert.go；Agent 接入在 agent/loop.go、agent/mcpview.go；配置在 config；诊断入口在 cmd/toolprobe。

### 2026-09-29 实际执行结果

| 检查 | 结果 |
|---|---|
| Windows：llm/config/agent/wire 全包及 toolprobe 编译 | 通过 |
| Windows：Web 配置/Studio/MCP 与 tools/MCP 定向回归 | 通过 |
| Linux（WSL Ubuntu、root、Go 1.26.5）：go test ./... -json -count=1 -timeout 180s | 通过；2242 个测试项（含子测试）通过、19 个跳过、0 失败 |
| 解析器 fuzz，10 秒、2 workers | 323206 次输入执行，通过 |
| go vet：llm/config/agent/wire/web/cmd/toolprobe | 通过 |
| Windows Go 主程序与 toolprobe 构建 | 通过 |
| Linux Go 主程序构建，CGO_ENABLED=0、-buildvcs=false | 通过；跨系统 Git 所有权限制导致 VCS 自动标记不可用，未改 Git 信任设置 |
| 前端 vue-tsc、Vite 生产构建 | 通过；产物输出到临时目录，没有覆盖工作区原有 dist |
| timeline、timeline.citations、useAgentStream.delegated | 130 条测试通过 |
| Windows -race | 未完成：测试进程因本机运行库错误 0xc0000139 退出，没有竞态通过结论 |
| 真实 DeepSeek/GLM 端点、Yw 线上请求、Docker 镜像与部署 | 本轮未执行 |

Linux 普通用户首次执行时，现有 Skill runtime 测试在清理只读目录阶段报 permission denied；改用 root 执行全量回归后通过。本次没有修改这些测试或目录权限策略。
