# 文本协议工具调用的兼容层（`<tool_call>` 解析器）设计与实施说明

- 日期：2026-09-29
- 状态：待评审 / 待实现
- 交付对象：负责评审与实现的 agent（本文档自包含，不依赖会话上下文）

---

## 1. TL;DR

线上一个接了 MCP 工具的 agent，模型把工具调用以 `<tool_call>` XML **文本**形式输出在 `content` 里，而 API 响应中没有原生 `tool_calls` 字段。pogo 引擎只认原生字段，于是把这段 XML 当成最终回答渲染出来后正常结束回合——表现为"打印了 MCP 调用后就停止"，无任何报错。

根因在**模型推理端点的响应侧**（请求侧把 tools 渲染进了提示词、响应侧没有把模型的文本协议解析回 `tool_calls`），MCP 服务器无辜且从未被调用到。本文档给出三个修复方案，并详细规格化了推荐实施的 **pogo 侧通用兼容层**：收到无原生 `tool_calls` 的回复时，宽容解析 `content` 中的文本协议调用，转成 `BlockToolUse` 走现有执行路径。

---

## 2. 问题现象

线上（pogo Agent Studio，agent「yw情报研判助手」，接入 Yw 平台 MCP 的 6 个 `yw_platform_*` 工具）对话中，模型先输出一句"我来查询 yw 平台的数据汇聚情况。"，随后正文出现如下文本（自屏幕截图转录，个别字符可能有 OCR 误差，解析器设计不依赖精确原文）：

```xml
<tool_call>
<invoke name="yw_platform_overview">
<parameter name="compact" string="true">true</parameter>
</invoke>
</tool_action>
```

然后回合结束（UI 显示"已工作 4.8s"），**没有工具执行记录、没有报错**。三个特征：

1. 工具调用以**正文文本**形式出现，而非引擎执行；
2. 闭合标签是畸形的（`<tool_call>` 配 `</tool_action>`），参数带多余的类型属性 `string="true"`——模型对这套文本协议本身半生不熟；
3. 全程无 error 日志（对 pogo 来说这是"正常收到一条纯文本回复"）。

本地用 glm-5.3（原生 function calling 完整）从未复现。

---

## 3. 链路事实（代码证据）

以下均在 `pogo/` 仓库中核实：

| 环节 | 位置 | 事实 |
|---|---|---|
| 工具下发 | `llm/wire.go:13`、`llm/convert.go:149`（`toWireTools`） | 工具**只**通过 OpenAI 标准 `tools` 数组下发，不注入提示词文本 |
| 响应解析 | `llm/openai.go:351`（`Delta.ToolCalls`）、`:407-473`（finish_reason 映射） | **只**解析原生 `delta.tool_calls`；无任何文本格式回退 |
| MCP 说明词 | `mcpclient/refresh.go:112-138`、`tools/mcp_describe.go:350` | 服务器 instructions 不进提示词，只作为 describe 工具的**元数据**输出（且标注 untrusted，见 `mcp_describe.go:127-142`） |
| 回合分支 | `agent/loop.go:872-927` | `resp.Message.ToolCalls()` 为空 → 走收尾逻辑后 `break`；非空 → 审查/执行/配对结果/继续循环 |
| 消息形态 | `llm/types.go:124`（`Message.ToolCalls()`）、`BlockToolUse` | 引擎的中立消息类型：工具调用 = assistant 消息里的 `tool_use` 块 |
| 持久化 | `agent/persist.go:92` | 会话记录按 `ToolCalls()` 识别工具轮次 |

外部事实（Yw 项目 `C:\Users\14777\Desktop\Pjt_Work\Yw`，供交叉验证，**无需修改**）：

- MCP 工具定义干净：`web/src/main/java/cn/iie/web/modules/agentaccess/mcp/McpServerSettings.java:20`（instructions）、`McpAdminController.java:261-269`（描述），没有任何"用文本 XML 格式调用"的指令；全项目搜不到 `<tool_call>` 协议的定义。
- 那次故障中 MCP 服务器**零调用**（请求从未到达，审计无记录）。
- 前代系统（`agent/src/agent_qa`，LangChain 1.2 + LangGraph）之所以"什么模型都兼容"：plan-execute 架构，全项目无 `bind_tools`，模型只输出 JSON 计划文本（`planner.py:124`），由 executor 程序化分发工具；结构化全靠客户端容错（`config/llm.py:237+` 的 `ainvoke_structured`+`json_repair`、`planner.py:95` 的 `_normalize_plan_keys`、失败降级单任务）。它从不依赖、也从不触发模型的 function calling。

**关键推理**：模型准确写出了 `yw_platform_overview` 及 `compact` 参数，而这两个信息只存在于 `tools` 数组的工具描述里（pogo 不注入提示词文本）。说明该端点**请求侧**把 tools 渲染进了提示词（模型看得见），**响应侧**没有把模型输出的文本协议解析回 `tool_calls` 字段。这是自建推理栈的典型半实现（如 vLLM/SGLang 开了工具模板但缺 `--tool-call-parser`，或网关只做了请求侧透传）。

---

## 4. 根因结论（三层责任判定）

1. **pogo**：按 OpenAI 协议办事，行为正确；但只支持原生 function calling，对"文本协议模型"零兼容——这是要补的能力缺口。
2. **yx（Yw）MCP 服务器**：无辜，未参与故障，不需要改。
3. **模型端点**：断点所在。请求侧渲染 tools、响应侧不解析文本协议。

---

## 5. 方案对比

| 方案 | 落点 | 性质 | 适用条件 |
|---|---|---|---|
| A. 端点补响应解析（如 vLLM `--enable-auto-tool-choice --tool-call-parser <模型对应 parser>`） | 推理服务 | 根治 | 端点归你管且可重启 |
| B. 换 function calling 完整的模型 | 线上模型配置 | 规避 | 有现成端点（注意：官方 API 可用 ≠ 公司网关路由可用，需实测） |
| C. **pogo 文本工具调用兼容层**（本文档主角） | pogo 引擎 | 通用兜底 | 总是可行；一次实现，所有文本协议模型不再踩坑 |

A 与 C 不冲突，建议 C 落地（A 取决于网关归属，不归本项目控制）。C 的本质是把前代系统"把一切模型当文本进文本出、客户端容错结构化"的兼容哲学，以最小侵入移植到 pogo 的原生调用循环上。

---

## 6. 方案 C 实施规格

### 6.1 触发条件（全部满足才启用）

1. `resp.Message` 中**无** `BlockToolUse` 块（原生路径优先，绝不重复解析）；
2. 本轮请求携带了工具（`len(tools) > 0`）；
3. 文本块中检出显式工具调用标记（见 6.2）。

### 6.2 标记识别（白名单，宁缺勿滥）

只认显式标记，**不做裸 JSON 猜测**（避免把普通正文误判成调用）：

- `<tool_call>` 开标记；闭合宽容：`</tool_call>`、`</tool_action>`（线上模型实测会写错）、或文本结束仍未闭合；
- 载荷两种风格都要支持：
  - **XML 风格**（线上实测）：`<invoke name="工具名">` + 任意个 `<parameter name="p">值</parameter>`；`<parameter>` 上的类型属性（如 `string="true"`）**忽略**；
  - **hermes/Qwen JSON 风格**：`<tool_call>{"name": "...", "arguments": {...}}</tool_call>`，按 json-repair 语义容错（尾逗号、单引号、未闭合尝试补全）；
- markdown 代码围栏包裹的同样处理（先剥围栏再解析）；
- 一条消息含多个调用片段时按出现顺序全部解析。

### 6.3 参数宽松转换

- 按目标工具 `ToolSchema.InputSchema` 的类型做最小转换：schema 说 boolean → `"true"/"false"` 转 bool；schema 说 number → 数字字符串转数值；其余保持字符串；schema 缺失/无类型信息时一律保持字符串；
- 解析结果序列化后的体积设上限（建议 256KiB），超限按解析失败处理，原文保留。

### 6.4 安全与防循环

- **工具名白名单**：只接受本轮请求 `tools` 列表里**精确匹配**存在的名字。未知名字 → 该条不执行、对应文本原样保留、计入失败预算。这是硬边界：任何文本里伪造的 `<tool_call>` 都只能调用本来就在本轮授权集合里的工具；
- 信任模型论证：解析对象是模型自身输出，与原生 `tool_calls` 同级信任；执行仍走现有 review 门禁（`agent/loop.go:950+`）与审批预算，**不新增权限面**。注入面等价性：网页/文档内容诱导模型输出 `<tool_call>` 文本，等价于诱导其发起原生调用，现有防护不变；
- **连续解析轮次上限**（建议 16，或复用现有 turn budget）：防止模型反复吐畸形调用文本导致死循环；超限后按普通 end_turn 收尾，并通过 `EventSteer` 附加一条说明（模式参考 `loop.go:885-916` 的 todoFinish/citation 单次重试）；
- 确定性 call ID：`call:text-<turn>-<seq>`，保证 tool_result 配对与会话可恢复（`agent/persist.go:92` 依赖 `ToolCalls()`，配对完整性见 `loop.go:929-937` 的注释约束——每个 tool_use 必须有 result）。

### 6.5 接入点（关键设计决策）

在 `agent/loop.go` 构造 `EventMessage` **之前**（即 `loop.go:851` 附近、emit 之前）对 `resp.Message` 做一次性改写：

1. 从 text 块中剥除已识别并转化为调用的片段（剩余正文保留）；
2. 追加对应 `llm.Block{Type: BlockToolUse, ID, Name, Input}`；
3. 将 `stop` 覆盖为 `llm.StopToolUse`（文本路径下 finish_reason 是 `stop` → 原本映射为 `StopEndTurn`，不覆盖会在 `loop.go:873` 提前收尾）。

这样存储、事件流、UI、审查门禁、并行分段、结果配对、持久化**全部走现有机制，零下游改动**——`loop.go:872` 的 `calls := resp.Message.ToolCalls()` 自然拿到解析结果。

建议实现布局：

- 新文件 `agent/texttoolcall.go`：纯函数解析器 + 常量；
- 建议签名：`parseTextToolCalls(text string, known map[string]llm.ToolSchema) (cleaned string, calls []llm.Block, parsed int)`；
- `loop.go` 接线约 10 行；连续轮次计数挂在 Agent 结构上（对齐 `todoReminded`/`citeRetried` 等既有单次/计数器的写法）。

**已知外观限制（v1 接受）**：流式 delta 在解析发生前已把原文推给前端，正文完成后 UI 会有一次"XML 闪现后消失"的跳变。可选优化（不阻塞 v1）：`llm/openai.go` 转发 text delta 时对疑似 `<tool_call>` 前缀做缓冲抑制。

### 6.6 不做什么

- 不改 MCP 客户端、不改 skill 运行时、不改 web 层；
- 不在无工具的会话里启用（触发条件 2 挡住）；
- 不支持无显式标记的"智能猜测"调用。

---

## 7. 测试要求

**单元测试**（`agent/texttoolcall_test.go`）：

- 规范 XML 风格、畸形闭合（`</tool_action>`、未闭合）、hermes JSON 风格、围栏包裹、多调用混合；
- 参数类型转换（bool/number/字符串/超限）；
- 未知工具名 → 不执行不剥除；无标记文本 → 原样返回零调用；
- 剥除后剩余正文的拼接正确性。

**集成测试**（仿 `agent/completion_test.go` 的假 Client 模式）：

- 假 Client 返回含 `<tool_call>` 文本的 assistant 消息 → 断言：工具被执行、消息含 `tool_use` + 对应 `tool_result`、展示文本已剥离 XML、循环继续到模型给出最终回答；
- 原生 `tool_calls` 路径回归：不受影响、不重复解析；
- 连续文本解析上限：达到阈值后正常收尾且带说明事件。

**回归**：`go test ./...`（agent、llm、web 相关套件）。Windows 下已知的无关 flaky（symlink 特权/pty/调度时序）按既有惯例隔离验证。

---

## 8. 验收清单

- [ ] 单测与集成测试全绿（含上述全部用例类目）；
- [ ] 用一个会输出文本协议的 mock OpenAI 端点（或录制响应回放）端到端跑通：提问 → 文本调用被解析执行 → 结果回填 → 最终回答；会话重载后工具轮次可正确恢复；
- [ ] 原生 function calling 模型（glm 系列）无行为变化；
- [ ] 线上复验同一 agent 同一问法：工具真实执行、Yw MCP 审计出现调用记录、前端不再把 XML 当正文渲染。

---

## 9. 附录

### 9.1 线上问题的确认方法（给排查人）

- 前端 F12 → Network → 对话的 SSE 请求：若流中 `content` 含 `<tool_call>` 文本且全程无 `tool_calls` 字段，即本文问题，一条证据定案；
- docker 日志**看不出异常**（该路径无错误发生）；yx MCP 审计无记录可佐证"请求从未到达 MCP"。

### 9.2 相关文件索引

pogo 侧（实现落点）：

- `llm/wire.go` / `llm/convert.go` / `llm/openai.go` —— 协议构造与解析（只读参考）
- `llm/types.go` —— `Block`/`BlockToolUse`/`ToolSchema` 中立类型
- `agent/loop.go:851-927` —— 接入点与回合分支
- `agent/persist.go:92` —— 会话持久化对工具轮次的依赖

Yw 侧（交叉验证，不修改）：

- `web/.../agentaccess/mcp/McpServerSettings.java` / `McpAdminController.java` —— MCP 工具与说明词
- `agent/src/agent_qa/config/llm.py` / `graph/planner.py` —— 前代容错式结构化的参照实现
