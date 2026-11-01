// SubagentSheet.vue: the dock tab that reads one delegated run's transcript
// as its own conversation.
export default {
  namespace: "subagentSheet",
  messages: {
    "zh-CN": {
      loading: "正在读取…",
      starting: "子代理正在启动，转录马上出现…",
      unavailable: "这份转录不可用",
      empty: "这个子代理还没有任何记录",
    },
    en: {
      loading: "Loading…",
      starting: "The subagent is starting; its transcript will appear shortly…",
      unavailable: "This transcript is unavailable",
      empty: "This subagent has nothing on record yet",
    },
  },
};
