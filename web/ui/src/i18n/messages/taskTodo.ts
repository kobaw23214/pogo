export default {
  namespace: "taskTodo",
  messages: {
    "zh-CN": {
      title: "这条消息的任务清单", shortTitle: "任务清单",
      completed: "已完成 {n}/{total}", blocked: "{n} 项受阻", cancelled: "{n} 项已取消", pending: "{n} 项待完成",
      cleared: "清单已清空", clearedShort: "已清空", clearedHint: "Agent 已清空这项任务的清单。过程记录仍保留在对话中。",
      status: { pending: "待开始", in_progress: "进行中", completed: "已完成", cancelled: "已取消", blocked: "受阻" },
    },
    en: {
      title: "Task list for this message", shortTitle: "Task list",
      completed: "{n}/{total} completed", blocked: "{n} blocked", cancelled: "{n} cancelled", pending: "{n} remaining",
      cleared: "List cleared", clearedShort: "Cleared", clearedHint: "The agent cleared this task's list. Its earlier updates remain in the conversation.",
      status: { pending: "Pending", in_progress: "In progress", completed: "Completed", cancelled: "Cancelled", blocked: "Blocked" },
    },
  },
};
