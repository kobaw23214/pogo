export default {
  namespace: "backgroundShell",
  messages: {
    "zh-CN": {
      label: "后台 Bash", heading: "此会话的后台 Bash", open: "查看此会话的后台命令", runningCount: "{count} 运行中",
      refresh: "刷新", filter: "筛选后台命令", filter_recent: "最近", filter_running: "进行中", filter_finished: "已结束", filter_all: "全部",
      state_running: "运行中", state_queued: "排队中", state_completed: "已完成", state_failed: "失败", state_cancelled: "已停止", state_timed_out: "已超时", state_interrupted: "已中断",
      queuedCount: "{count} 排队中",
      loading: "正在读取…", empty: "暂无匹配的后台命令。", output: "命令输出", noOutput: "暂时没有输出。",
      disconnected: "连接已断开，显示上次已知状态；重新连接后更新。", loadFailed: "命令列表读取失败，请刷新重试。", outputFailed: "日志读取失败，请重试。", stopFailed: "未能确认命令已停止，请刷新后重试。",
      stop: "停止命令", stopping: "正在停止…", liveTail: "最新输出", followTail: "回到最新", readLog: "从头读取日志", nextPage: "下一页",
      logRange: "字节 {start}–{end} / {total}", tailHint: "这里只显示最后一段输出，可从头分页读取已保存的日志。", closeHint: "关闭此面板后，命令会继续运行。",
    },
    en: {
      label: "Background Bash", heading: "This conversation's background Bash", open: "View background commands in this conversation", runningCount: "{count} running",
      refresh: "Refresh", filter: "Filter background commands", filter_recent: "Recent", filter_running: "Running", filter_finished: "Finished", filter_all: "All",
      state_running: "Running", state_queued: "Queued", state_completed: "Completed", state_failed: "Failed", state_cancelled: "Stopped", state_timed_out: "Timed out", state_interrupted: "Interrupted",
      queuedCount: "{count} queued",
      loading: "Loading…", empty: "No matching background commands.", output: "Command output", noOutput: "No output yet.",
      disconnected: "Disconnected. Showing the last known state until reconnection.", loadFailed: "Could not load commands. Refresh to retry.", outputFailed: "Could not read the log. Try again.", stopFailed: "Could not confirm the command stopped. Refresh to retry.",
      stop: "Stop command", stopping: "Stopping…", liveTail: "Latest output", followTail: "Follow latest", readLog: "Read log from start", nextPage: "Next page",
      logRange: "Bytes {start}–{end} / {total}", tailHint: "Only the output tail is shown here. Read the saved log from the start in pages.", closeHint: "Closing this panel leaves commands running.",
    },
  },
};
