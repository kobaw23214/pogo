export default {
  namespace: "sentinelAccess",
  messages: {
    "zh-CN": {
      label: "哨兵", heading: "本会话的哨兵", openOne: "查看本会话的哨兵记录", openMany: "查看本会话的 {n} 个哨兵", refresh: "刷新哨兵列表",
      stale: "列表刷新失败，仍可打开已保存的哨兵记录", refreshFailed: "暂时无法刷新，以下保留本会话上次读取的记录。",
      state_listening: "监听中", state_running: "处理中", state_waiting_human: "等待审批", state_paused: "已暂停", state_expired: "已到期", state_completed: "已完成",
      state_error: "需要检查", state_blocked: "暂不可运行", state_unavailable: "暂不可用", state_waiting_capacity: "等待资源", state_removed: "已移除",
    },
    en: {
      label: "Sentinel", heading: "This conversation's Sentinels", openOne: "View this conversation's Sentinel activity", openMany: "View this conversation's {n} Sentinels", refresh: "Refresh Sentinel list",
      stale: "The list could not refresh. Saved Sentinel activity is still available.", refreshFailed: "Refresh is unavailable. Keeping the last records loaded for this conversation.",
      state_listening: "Listening", state_running: "Processing", state_waiting_human: "Awaiting approval", state_paused: "Paused", state_expired: "Expired", state_completed: "Completed",
      state_error: "Needs review", state_blocked: "Blocked", state_unavailable: "Unavailable", state_waiting_capacity: "Waiting for capacity", state_removed: "Removed",
    },
  },
};
