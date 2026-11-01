// Copy for src/components/ChangesView.vue — the session/workspace changes tab.
// Badge labels stay English in every locale: they are tool-style status words
// (create/edit/delete), matching the original hardcoded text.
export default {
  namespace: "changesView",
  messages: {
    "zh-CN": {
      scopeSession: "本会话",
      scopeWorkspace: "工作区",
      modeUnified: "统一",
      modeSplit: "双栏",
      emptySession: "本次会话还没有文件改动",
      writing: "写入中",
      filesChanged: "{n} 个文件被改动",
      turnHead: "第 {turn} 轮 · {n} 个文件",
      viewFile: "查看文件",
      diffTooBig: "diff 过大，仅显示统计",
      newFileNoDiff: "新建文件，没有可对比的旧内容",
      emptyWorkspace: "工作区还没有被记录的文件改动（首次 edit/write 后开始累积）",
      sinceBaseline: "自基线以来 {n} 个文件",
      clearBaseline: "清空基线",
      confirmClear: "确认清空？",
      baseUnavailable: "首次改动前的内容未保存（超过 1MB 上限），只有统计可见",
      badge: {
        added: "create",
        modified: "edit",
        deleted: "delete",
      },
    },
    en: {
      scopeSession: "Session",
      scopeWorkspace: "Workspace",
      modeUnified: "Unified",
      modeSplit: "Split",
      emptySession: "No file changes in this session yet",
      writing: "Writing",
      filesChanged: "{n} files changed",
      turnHead: "Turn {turn} · {n} files",
      viewFile: "View file",
      diffTooBig: "Diff too large; stats only",
      newFileNoDiff: "New file, no previous content to compare",
      emptyWorkspace: "No recorded file changes in the workspace yet (they accumulate after the first edit/write)",
      sinceBaseline: "{n} files since baseline",
      clearBaseline: "Clear baseline",
      confirmClear: "Confirm clear?",
      baseUnavailable: "Content before the first change was not saved (over the 1MB limit); only stats are visible",
      badge: {
        added: "create",
        modified: "edit",
        deleted: "delete",
      },
    },
  },
};
