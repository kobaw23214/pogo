export default { namespace: "modelMetadata", messages: {
  "zh-CN": {
    open: "检测配置", back: "返回模型请求", send: "发送检测", service: "检测服务", readonly: "仅查询展示", queryModel: "查询模型（仅本次检测）", modelPlaceholder: "输入 Ollama 中的模型名称", modelRequired: "请填写本次要查询的模型名称。", invalidURL: "请填写完整的 HTTP(S) Base URL，且不要包含查询参数、账号或密码。",
    notice: "只查询服务公开的元数据，不发送生成任务，也不修改或回填任何配置。检测结果仅供参考，配置仍由你手动填写。", loading: "正在查询服务元数据…", received: "已收到检测响应", model: "模型", context: "上下文（tokens）", output: "输出上限（tokens）", source: "数据来源", raw: "查看原始响应", unknown: "响应未提供可识别的上下文或输出上限，不推测数值；原有配置保持不变。", notJSON: "服务未返回有效 JSON，无法识别配置。可查看原始响应核对地址与权限。", truncated: "响应超过读取上限或未完整返回，仅展示原文，不从不完整内容识别配置。",
    presets: { vllm: "vLLM / OpenAI 兼容模型列表", "ollama-running": "Ollama · 当前运行实例", "ollama-show": "Ollama · 模型规格", llamacpp: "llama.cpp · 运行参数" },
    hints: { vllm: "GET /models，保留 Base URL 的路径前缀。优先读取 max_model_len；其他兼容服务未必返回此字段。", "ollama-running": "GET /api/ps，查看已加载实例实际分配的上下文。模型未运行时可能没有结果，不会自动加载模型。", "ollama-show": "POST /api/show，查询模型规格和 num_ctx 配置。标称窗口不代表当前实例实际分配的窗口。", llamacpp: "GET /props，读取默认运行参数 n_ctx。仅查询，不修改服务参数；不主动加载指定模型。" },
    kinds: { runtime: "运行实例", declared: "服务声明", configured: "配置参数" }
  },
  en: {
    open: "Inspect configuration", back: "Back to model request", send: "Send inspection", service: "Service preset", readonly: "Query and display only", queryModel: "Model to query (this inspection only)", modelPlaceholder: "Enter the model name used by Ollama", modelRequired: "Enter a model name for this query.", invalidURL: "Enter an absolute HTTP(S) Base URL without query parameters, usernames or passwords.",
    notice: "Queries service metadata only. No generation task is sent, and no configuration is changed or populated. Results are informational; settings remain manually configured.", loading: "Querying service metadata…", received: "Inspection response received", model: "Model", context: "Context (tokens)", output: "Max output (tokens)", source: "Source", raw: "View raw response", unknown: "The response contains no recognized context or output limit. No values are inferred or settings changed.", notJSON: "The service did not return valid JSON. Check the raw response, address and permissions.", truncated: "The response exceeded the read limit or was incomplete. Only raw content is shown; no configuration is inferred.",
    presets: { vllm: "vLLM / OpenAI-compatible model list", "ollama-running": "Ollama · Running instances", "ollama-show": "Ollama · Model specifications", llamacpp: "llama.cpp · Runtime parameters" },
    hints: { vllm: "GET /models, preserving the Base URL path prefix. Reads max_model_len when available; other compatible services may omit it.", "ollama-running": "GET /api/ps for context allocated to loaded models. Unloaded models may be absent; no models are loaded automatically.", "ollama-show": "POST /api/show for model specifications and num_ctx configuration. The advertised maximum may differ from the running instance.", llamacpp: "GET /props for the default runtime n_ctx. Does not change service parameters or request loading a specific model." },
    kinds: { runtime: "Running instance", declared: "Service declaration", configured: "Configuration" }
  }
} };
