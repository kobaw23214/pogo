export default {
  namespace: "modelPicker",
  messages: {
    "zh-CN": {
      tip: "切换模型，对话历史原样保留",
      tipDisabled: "运行中不能切换模型",
      selectModel: "选择模型",
      needsKey: "需要 {key}",
      subagentLabel: "子 agent: {model}",
      subagentTip: "只读（explore）subagent 会跑 {model} 而不是 {current}；在 ~/.pogo/providers.json 里配置",
    },
    en: {
      tip: "Switch model — conversation history is kept as-is",
      tipDisabled: "Cannot switch models while a run is in flight",
      selectModel: "Select Model",
      needsKey: "needs {key}",
      subagentLabel: "sub-agent: {model}",
      subagentTip: "Read-only (explore) subagents run {model} instead of {current}; configure it in ~/.pogo/providers.json",
    },
  },
};
