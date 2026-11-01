export default {
  namespace: "immersiveBrowser",
  messages: {
    "zh-CN": {
      enter: "沉浸模式", exit: "退出沉浸", exitTip: "退出沉浸模式（Shift + Esc）",
      chat: "会话", minimize: "收起会话", restore: "展开会话", move: "拖动会话窗口，或聚焦后用方向键移动",
      windowControls: "会话窗口操作", minimizeTip: "收起会话，继续浏览当前网页",
      resize: "调整对话大小，或聚焦后用方向键调整", fullscreen: "系统全屏", leaveFullscreen: "退出系统全屏",
      fullscreenFailed: "浏览器未允许系统全屏，仍可继续使用沉浸模式。",
      working: "正在工作", ready: "随时告诉我下一步", attention: "需要你的确认", switchFocus: "F6 切换网页和对话焦点",
      mini: {
        label: "收起的会话与进度", move: "拖动到任意位置；聚焦后用方向键移动，Shift 加速", working: "工作中", attention: "待确认", ready: "已就绪", disconnected: "连接中断",
        needsReview: "需查看", limitReached: "达到限制", stoppedShort: "已停止", endedShort: "已结束",
        recent: "最近事件", recentHint: "最近 8 条 · 新到旧", idle: "暂无新事件，可展开会话继续", connectionLost: "连接中断，正在尝试重新连接",
        started: "已开始处理", toolRunning: "正在使用{tool}", toolDone: "{tool}操作已结束", toolFailed: "{tool}操作失败，展开会话查看", approval: "等待你的确认，展开会话处理", approved: "已收到授权，继续执行", denied: "未获授权，请查看会话", thinking: "正在思考下一步", answering: "正在整理回复", retry: "正在重试，请稍候", ended: "本次运行已结束", stopped: "本次运行已停止", limited: "运行达到限制，请查看会话", failed: "运行出现问题，展开会话查看",
        tools: { browser: "浏览器", office: "Office", read: "文件读取", write: "文件写入", edit: "文件编辑", bash: "命令", subagent: "子 Agent", todo: "任务计划", synth: "技能", skill: "技能", mcp: "连接工具", unknown: "工具" },
      },
    },
    en: {
      enter: "Immersive mode", exit: "Exit immersive", exitTip: "Exit immersive mode (Shift + Esc)",
      chat: "Conversation", minimize: "Collapse chat", restore: "Open conversation", move: "Drag conversation, or focus and use arrow keys to move",
      windowControls: "Conversation window controls", minimizeTip: "Collapse the conversation and continue browsing",
      resize: "Resize conversation, or focus and use arrow keys", fullscreen: "Fullscreen", leaveFullscreen: "Exit fullscreen",
      fullscreenFailed: "Fullscreen is unavailable. You can continue in immersive mode.",
      working: "Working", ready: "Tell me what to do next", attention: "Your confirmation is needed", switchFocus: "F6 switches focus between page and conversation",
      mini: {
        label: "Minimized conversation and progress", move: "Drag anywhere, or focus and use arrow keys; Shift moves faster", working: "Working", attention: "Confirm", ready: "Ready", disconnected: "Disconnected",
        needsReview: "Review", limitReached: "Limit reached", stoppedShort: "Stopped", endedShort: "Ended",
        recent: "Recent events", recentHint: "Latest 8 · newest first", idle: "No new events. Open the conversation to continue.", connectionLost: "Connection lost. Reconnecting…",
        started: "Started working", toolRunning: "Using {tool}", toolDone: "{tool} action ended", toolFailed: "{tool} action failed. Open conversation.", approval: "Waiting for your confirmation. Open conversation.", approved: "Authorization received. Continuing.", denied: "Authorization declined. Open conversation.", thinking: "Considering the next step", answering: "Preparing the response", retry: "Retrying. Please wait.", ended: "This run has ended", stopped: "This run was stopped", limited: "Run limit reached. Open conversation.", failed: "Run encountered a problem. Open conversation.",
        tools: { browser: "browser", office: "Office", read: "file reader", write: "file writer", edit: "file editor", bash: "command", subagent: "subagent", todo: "task plan", synth: "skill", skill: "skill", mcp: "connected tool", unknown: "tool" },
      },
    },
  },
};
