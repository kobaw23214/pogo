// ShellPanel.vue: the terminal panel's chrome, plus the line written into the
// terminal itself when the shell exits.
export default {
  namespace: "shellPanel",
  messages: {
    "zh-CN": {
      workspace: "当前会话工作区：{ws}",
      noSession: "先开始一个会话，shell 会在它的工作区里打开",
      exited: "shell 已退出（code {code}），按任意键重启",
    },
    en: {
      workspace: "Current session workspace: {ws}",
      noSession: "Start a session first; the shell will open in its workspace",
      exited: "shell exited (code {code}); press any key to restart",
    },
  },
};
