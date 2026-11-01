// Connection and stream error copy produced by agent/useAgentStream.ts. These
// end up in streamError / outage.message, which the unreachable page renders.
export default {
  namespace: "stream",
  messages: {
    "zh-CN": {
      unauthorized: "未授权：URL 里的 token 不对",
      connectFailed: "连接失败：HTTP {status}",
      interrupted: "连接中断",
      disconnected: "连接已断开",
      unknownError: "unknown error",
      outputLimited: "模型输出达到本次上限，任务尚未确认完成。可发送“继续”接着处理，已保存的结果会保留。",
      responseInterrupted: "模型连接中断，任务尚未确认完成。可发送“继续”接着处理，已保存的结果会保留。",
    },
    en: {
      unauthorized: "Unauthorized: the token in the URL is wrong",
      connectFailed: "Connection failed: HTTP {status}",
      interrupted: "Connection interrupted",
      disconnected: "Connection lost",
      unknownError: "unknown error",
      outputLimited: "The model reached its output limit before confirming completion. Send “continue” to pick up from the saved results.",
      responseInterrupted: "The model connection was interrupted before confirming completion. Send “continue” to pick up from the saved results.",
    },
  },
};
