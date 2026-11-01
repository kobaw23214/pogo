export default {
  namespace: "taskSessionResult",
  messages: {
    "zh-CN": {
      executionInput: "查看本次执行输入",
      title: "任务执行结果", back: "返回任务记录", refresh: "刷新结果", loading: "正在读取执行记录…", empty: "这次执行尚未生成消息。",
      unavailable: "无法读取此 Agent 的任务结果，请检查记录是否仍可访问。", scopeChanged: "这条记录不是当前 Agent 的后台执行结果。",
      readOnly: "在此查看执行结果和处理审批；不会进入普通聊天。", stop: "停止本次执行", stopping: "正在请求停止…", actionFailed: "操作未成功，请刷新后重试。",
      approvalBusy: "正在提交审批…", active: "执行中", done: "执行记录", child: "子任务记录", closeChild: "返回执行结果",
      disconnected: "实时连接已断开，正在重新连接；期间暂不可处理审批。", reconnect: "重新连接", isolationMissing: "这条旧记录缺少节点执行标识，无法单独展示该节点的过程。",
      connection: { idle: "尚未连接", validating: "正在确认执行记录…", connecting: "正在连接实时过程…", live: "实时连接", reconnecting: "正在重新连接…", error: "实时过程不可访问" },
    },
    en: {
      executionInput: "View execution input",
      title: "Task execution result", back: "Back to task records", refresh: "Refresh result", loading: "Loading execution record…", empty: "This execution has no messages yet.",
      unavailable: "This Agent's task result could not be loaded. Check that the record is still accessible.", scopeChanged: "This record is not a background execution of the current Agent.",
      readOnly: "Read the result and handle approvals here, without opening an ordinary chat.", stop: "Stop this execution", stopping: "Requesting stop…", actionFailed: "The action failed. Refresh and try again.",
      approvalBusy: "Submitting approval…", active: "Running", done: "Execution record", child: "Subtask record", closeChild: "Back to execution result",
      disconnected: "The live connection was lost. Reconnecting; approvals are temporarily unavailable.", reconnect: "Reconnect", isolationMissing: "This older record has no node execution identifier, so its node process cannot be shown separately.",
      connection: { idle: "Not connected", validating: "Checking execution record…", connecting: "Connecting to live activity…", live: "Live", reconnecting: "Reconnecting…", error: "Live activity is unavailable" },
    },
  },
};
