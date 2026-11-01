# 工具调用稳健性：调研、设计与验证

调研日期：2026-09-30。本文对比公开的一手文档与源码，不对产品作性能排名。线上模型的原始请求/响应、服务引擎与旧 Agent 指令尚未取得，不能据截图认定网关缺少解析器或模型本身是唯一原因。

## 1. 当前证据

- 三种正文调用：`tool_call/invoke/parameter`、`function_calls/invoke`、`tool_call/tool_name/parameters(JSON)`；可能带自然语言前言。旧实现只覆盖第一种且要求整段是调用块。
- 原生路径识别到正文调用时，`text_in_native` 在执行前发生，不是 Yw 业务调用错误。
- `mcp_describe` 声明 `tool`，截图模型却传 `tool_name`。旧通用反序列化忽略未知字段，使请求变成空参数目录查询，返回 47 个工具而非业务任务数。
- 本地模拟回放确认下一轮保留原用户问题及配对的工具结果；这不证明线上网关没有改写。
- 本地 Yw 已有 `yw_ingest_task_page`，支持日期过滤并返回分页 total；`yw_platform_overview` 统计目录对象，不能替代业务查询。线上版本/授权仍待核对。

## 2. 主流方案的做法及适用边界

| 来源 | 已核实做法 | 对 Pogo 的启示 |
|---|---|---|
| [OpenAI Function calling](https://developers.openai.com/api/docs/guides/function-calling) | 支持 strict schema；启用时要求对象禁止额外属性，并对必填/可选字段表示有约束。 | 参数定义应准确，但不能向所有兼容端点无条件发送 strict=true，或把业务可选字段机械变必填。客户端仍需验证。 |
| [Anthropic 工具错误处理](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls) | 工具结果与调用 ID 配对；错误以 is_error 回传，说明问题及可纠正方式。 | 参数拼错应给模型准确反馈，不能静默产生另一种成功结果。 |
| [Anthropic 工具搜索](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool) | 搜索目录并按需载入完整声明；发现与执行是两个阶段。 | 保留 Pogo 冻结目录和按需声明，用现有 filter 搜索、tool 精确详情；索引返回不代表工具激活。 |
| [LangGraph ToolNode 源码](https://github.com/langchain-ai/langgraph/blob/main/libs/prebuilt/langgraph/prebuilt/tool_node.py) | ToolInvocationError 包含调用参数的校验细节，工具节点按错误策略返回 ToolMessage。 | 定义错误、调用错误、执行错误应区分；避免把未知参数吞掉。main 是调研当日读取的移动分支。 |
| [PydanticAI 工具重试](https://pydantic.dev/docs/ai/tools-toolsets/tools-advanced/) | 校验失败和 ModelRetry 回传给模型，受重试预算限制。 | 让模型根据证据纠正，不由宿主猜参数；避免无限重试。 |
| [OpenAI Agent 循环](https://developers.openai.com/api/docs/guides/agents/running-agents) | 根据工具调用、交接、最终回答继续或结束。 | 无工具调用时结束是常见控制流，本身不是已证实的业务完成判定缺陷。 |
| [Claude Code Stop hooks](https://code.claude.com/docs/en/hooks) | 可在停止时检查并阻止结束，提供 stop_hook_active 避免永远不能满足的循环。 | 业务验收需要明确契约；不能增加无条件“继续执行”或靠关键词判定任务完成。 |
| [vLLM Tool Calling](https://docs.vllm.ai/en/latest/features/tool_calling/) | 工具输出解析器、聊天模板与模型配套，某些解析器分离普通正文和调用。 | 先验证服务原生工具闭环；Pogo 的文本适配是显式端点配置，不能代替服务正确配置。不能据此断言线上使用 vLLM。 |

## 3. 本轮设计

1. `mcp_describe` 使用专用严格参数检查：未知键、重复键、错误类型、无效组合明确报错；`tool_name` 返回使用 `tool` 的提示。保留合法空对象列目录，不增加全局字段改名规则。错误经原有 tool_result 回填，让模型自行纠正。
2. 把疑似协议检测与可执行解析分开。诊断识别三种明确外层标记及孤立闭合标记；原生调用继续优先，不从同一响应再执行正文。
3. XML 模式接受已观察的完整结构；保留名称唯一性、可见声明、全批次 schema 校验、输入限制和审批。无参数的 invoke 仍要求显式完整闭合；不补参数、不补缺失内层标签。
4. 新增模型配置 `allow_text_preamble`，默认 false，仅在 xml/json 模式允许显式启用。开启后接受有界的普通前言加独立调用块，保留前言；禁止围栏、引用、额外尾随文字等混合结构。此选项不能判断自然语言执行意图，不是对任意不可信正文的自动执行授权。
5. 发现提示使用真实参数键与简短例子，明确索引/说明与业务数据的区别，完成原用户任务或具体报告阻碍。
6. 探针保留无参数闭环，增加带 string/integer/boolean 参数的契约测试，以及脱敏的模型/兼容配置和重复运行统计。以模型确实使用新生成结果为成功条件，不以 API 200 或出现一次调用为成功。

不在本轮引入：全局宽松 XML/JSON 抽取、模糊工具名映射、按业务关键词猜参数、强制所有模型 strict、无条件多轮催促、Yw 业务统计的重复实现、自动修改模型配置或部署。

## 4. 验收要求

- 对完整截图结构回归，而非只测试去掉前言的理想调用片段。
- 错参数必须产生失败结果且不激活隐藏工具；模型修正后应能取得详情、执行一次、收到结果，并保留原问题。
- XML 正反例覆盖原生优先、不同方言、前言开关、围栏/引用、截断、重复参数、未知工具、混合批次、审批拒绝、历史重载。
- 探针针对实际端点测试。基础通过仍不能证明 Yw 统计正确；随后使用只读查询核对 total、时间口径、最终答案和会话重载。
- 本地测试、构建、真实模型、业务验收、部署分别记录，禁止相互代替。

## 5. 执行状态

调研与本轮修复已完成。改动集中在 Pogo，Yw 业务代码和现有模型配置未修改，现有服务未替换或重启。

| 验证 | 结果与边界 |
|---|---|
| Windows 专项回归 | 协议、MCP 参数校验、错误后的发现恢复、历史与配置往返通过。 |
| Windows 包测试 | llm、config、cmd/toolprobe 全部通过。 |
| Linux race | llm、agent、tools、config、cmd/toolprobe 共 903 项通过、13 项跳过，0 失败。容器使用 --init、禁止网络、源码只读挂载。 |
| 解析器 fuzz | 15 秒、2,222,602 次执行，未发现失败；覆盖三种格式和前言开关。 |
| 构建和静态检查 | Windows 主程序、Linux 主程序与探针构建通过；相关 Go 包 vet 和前端 vue-tsc 通过。前端仅类型声明扩展，未重新生成 UI 资源。 |
| 真实模型 | 复用本地 Pogo 的只读 providers 配置及既有凭据，在一次性容器运行新探针；glm-5.3 的 contract 套件 2/2 场景首次通过、0 纠正，均为原生调用，均使用了返回的新随机值。单次基础验收，不是长期稳定率或业务完成证明。 |
| 线上问题端点 | deepseek-v4-flash-channel-int8 未获得同一实际端点配置，尚未验收；没有用其他同名/近似模型代替。 |
| Yw 与部署 | 未执行真实 Yw 业务查询，未部署。 |

验证环境记录：首次离线 Linux 测试缺少已锁定依赖的缓存，改用主机现有模块下载缓存，无需开放网络；未使用 --init 的容器在已有子进程回收测试中失败，加 --init 后单项及完整相关包 race 均通过。未修改该子进程测试或生产逻辑。宿主 shell 没有默认 provider 配置，之后从 compose 挂载关系找到实际配置完成 GLM 探针；没有生成或覆盖用户配置。

本次证据目录：`C:/Users/14777/AppData/Local/Temp/pogo-tool-robustness-ebe1da74c8774f1682925f6ea9746e08/`。其中 `linux-race.jsonl` 是测试事件，`glm-5.3-contract.json` 是脱敏模型探针报告，`changed-paths.json` 与 `baseline.json` 用于区分本轮修改和原有工作区改动；目录是本机临时产物，不作为持久部署记录。

下一步：先在隔离验收环境部署候选版本，对实际线上模型运行 contract 探针；用其实际 model ID、相同思考参数和新会话验证 Yw 只读查询，核对工具真实返回的 total、日期口径、最终回答及重载。协议错误需先判断服务模板/解析器和兼容配置；探针通过后仍不直接宣称业务验收通过。
