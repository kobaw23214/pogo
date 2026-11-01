# Office 真实模型隔离验证

该命令运行真实 Pogo 主界面、Agent 循环和已配置的模型客户端。模型自行决定工具调用；程序不提供固定响应、不替模型安排执行序列，也不在启动时发送模型请求。与 `office-integration-check` 的确定性执行链测试分开。

仅复制人工生成的 `pogo-office-word.docx` / `pogo-office-excel.xlsx`，要求 manifest 标记为 `synthetic:true`，以及一个没有既存 `workspace/` 的新检查目录。不会更新正式账号、文档、部署或容器。

在 `pogo/` 目录使用已有的隔离 Office 服务和前端开发服务：

```sh
OFFICE_MODEL_DATA=/absolute/new-check-directory \
OFFICE_MODEL_FIXTURES=/absolute/synthetic-fixtures \
OFFICE_MODEL_DOCS_SKILL=/absolute/pogo/skills/builtin/docs \
OFFICE_MODEL_CONFIG=/absolute/operator/providers.json \
OFFICE_MODEL=explicit-model-id \
OFFICE_MODEL_DEV_PROXY=http://127.0.0.1:19899 \
POGO_OFFICE_SERVICE_URL=http://127.0.0.1:19898 \
POGO_OFFICE_PUBLIC_URL=http://127.0.0.1:19893 \
POGO_OFFICE_FETCH_URL=http://host.docker.internal:19893 \
POGO_OFFICE_JWT_SECRET_FILE=/absolute/private/docserver-jwt \
go run ./cmd/office-model-check
```

使用 `providers.json` 声明的 `key_env` 环境变量。可选 `OFFICE_MODEL_CREDENTIALS_FILE` 指向运营者提供的私有环境文件；程序只读取选中 provider 的那一项，按字面值解析，不执行 shell，不加载或导出其他字段，也不会将该文件复制到验证目录。文件需只有当前用户可读写。不要把密钥放进命令参数、用户提示或报告。

Service URL 指向带固定 SDK 扩展的 Office adapter，客户端只访问 Pogo 的 `/office/editor/` 同源网关。前端开发资源通过 Pogo 提供；单端口验收还应关闭 Vite HMR 或代理其长连接，不能用主页面同源代替完整网络审计。

默认端口 `19893`；可设 `OFFICE_MODEL_PORT`，并相应调整 Office Public / Fetch URL。端口被占用时拒绝启动，不替换任何服务。`OFFICE_MODEL_MAX_TURNS` 默认为 24，范围 5–100；每次 Agent 运行默认限时十分钟，`OFFICE_MODEL_RUN_TIMEOUT_SECONDS` 范围为 60–600 秒。测试客户端输出上限为配置值与 8192 中的较小者，不修改原配置文件。模型客户端不自动重试失败请求。接口、工具及身份授权沿用 Pogo 实际实现。

初始化一个明确选中 docs 技能的隔离 Agent，以及 Word / Excel 两个独立会话：

```sh
OFFICE_MODEL_DATA=/absolute/new-check-directory node cmd/office-model-check/bootstrap.mjs
```

浏览器使用 `private/runtime.json` 的本次测试 token 和 `previewURLs.word` / `previewURLs.excel`。在各自会话的主界面打开文件，再在编辑器内手动输入独有标记并保持未保存。之后分别向真实模型提出修改、撤销、重做和保存的要求。可分轮验收每一步，必须检查真实工具返回和当前内容，不能仅依赖模型的文字回答。撤销和重做是文档全局历史；有新的人工编辑插入时必须重新确认最后一步，不能将其解释为只撤销 Agent 操作。

`verification/model-calls.jsonl` 仅记录真实请求时间、模型与 provider 名称、时长、用量、工具名/ID 和是否报错。`model-provenance.json` 另记录实际响应 JSON 的 SHA-256、最后一条用户正文的 SHA-256、真实工具调用 ID、时间与完成状态，用于关联每阶段的模型响应和工具记录。`synthetic:false` 表示模型是真实配置的 provider；`syntheticDocuments:true` 明确文档是人工样本。两份记录均不保存凭据、请求头、endpoint、完整模型响应或推理。公开工具参数和回执从普通会话 transcript 中单独筛选；`private/runtime.json` 含测试 token，不放进报告。模型服务错误的原始诊断不透传到测试报告。

验收应包括：人工未保存输入在修改、撤销、重做之后均仍存在；目标修改确实恢复；Excel 公式与跨表结果正确；保存有持久化回执；实际 DOCX/XLSX 文件与重新打开的编辑器内容一致。用量大于零和真实 provider 元数据证明已调用配置的模型；它们本身不证明任务成功，也不证明任意模型或复杂文件的兼容性。

## 结果验收器

`verify-results.mjs` 只读取记录与文件，不调用模型、不发送编辑命令。输入文件结构为 `schemaVersion:1`，包含 `baselineDir`、`provenanceFile`、`exportReportFile`，以及可选的 `sourcesBeforeFile` / `sourceRoot`。`scenarios` 分别描述 Word 和 Excel：`kind`、`path`、可选 `documentSessionId`、`humanInputFile`、`reopenFile`、`exportFile`、`liveFile`，以及四个 `phases`。每个 scenario 指定自己的 `transcriptFile`；也可使用顶层 `transcriptFile` 作为共用记录。

每个 phase 使用 `name:edit|undo|redo|save` 与 `exactPrompt`，必须对应真实的独立用户轮次。`humanInputFile` 记录 `{method:'trusted-browser-input',capturedAt,session,snapshot,fileSha256}`；snapshot 保存人工输入后、模型请求前的真实编辑器返回。`reopenFile` 使用重开读回结构并附 `fileSha256`。可选 `guardPhase:{exactPrompt,beforeFile,afterFile,humanMarkers}` 用于交错输入后的安全撤销判断：先由 Agent 修改，再通过编辑器追加人工内容，要求在整体撤销会影响新输入时停止。核对真实模型读取、无写入或历史操作、前后全文和标记一致。另一个会话的 guard 应有单独的证据审查，不能混淆文档 session；这项保护检查不证明模型对历史分组或后续操作建议的解释正确。

```sh
node cmd/office-model-check/verify-results.mjs /absolute/verification/input.json
```

结果写入 `real-model-acceptance.json`。验收器将各轮工具 ID 与真实 provider stream 的用户正文摘要关联，核对人工输入、编辑历史、确认保存与磁盘内容，而不以模型的成功宣称代替证据。
