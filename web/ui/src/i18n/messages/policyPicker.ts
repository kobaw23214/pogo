export default {
  namespace: "policyPicker",
  messages: {
    "zh-CN": {
      tip: "审批模式：哪些操作需要你点头",
      menuTitle: "应如何批准 pogo 的操作？",
      modes: {
        strict: { label: "请求批准", desc: "编辑文件和运行命令前都要你点头" },
        standard: { label: "替我审批", desc: "默认：只有 bash 命令需要你点头" },
        auto: { label: "完全访问", desc: "所有操作都不再询问，直接执行" },
      },
    },
    en: {
      tip: "Approval mode: which actions need your approval",
      menuTitle: "How should pogo actions be approved?",
      modes: {
        strict: { label: "Ask for Approval", desc: "You approve every file edit and command before it runs" },
        standard: { label: "Approve for Me", desc: "Default: only bash commands need your approval" },
        auto: { label: "Full Access", desc: "Nothing is asked anymore; everything runs directly" },
      },
    },
  },
};
