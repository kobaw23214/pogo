// Unreachable.vue: the cannot-connect page that takes over the viewport when
// the event stream is down.
export default {
  namespace: "unreachable",
  messages: {
    "zh-CN": {
      title: "无法连接到 pogo",
      subtitle: "页面与后端的事件流断开了，agent 的更新到不了这里。",
      causeProcess: "{cmd} 进程还在运行吗？终端里 {keys} 会结束它",
      causeNetwork: "网络或代理设置是否发生了变化",
      causeToken: "地址里的 token 是否为本次启动打印的那个",
      gaveUp: "自动重试已停止 —— 这个问题重试解决不了",
      retryingNth: "正在自动重试（第 {n} 次）…",
      retrying: "正在自动重试…",
      reconnectNow: "立即重连",
      reload: "刷新页面",
    },
    en: {
      title: "Cannot connect to pogo",
      subtitle: "The page lost its event stream to the backend, so agent updates can no longer reach it.",
      causeProcess: "Is the {cmd} process still running? {keys} in the terminal ends it",
      causeNetwork: "Did your network or proxy settings change?",
      causeToken: "Is the token in the address the one printed by this launch?",
      gaveUp: "Auto-retry stopped — retrying won't fix this",
      retryingNth: "Auto-retrying (attempt {n})…",
      retrying: "Auto-retrying…",
      reconnectNow: "Reconnect now",
      reload: "Reload page",
    },
  },
};
