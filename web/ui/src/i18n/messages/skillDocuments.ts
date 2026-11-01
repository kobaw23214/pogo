export default {
  namespace: "skillDocuments",
  messages: {
    "zh-CN": {
      title: "Skill 文档", navigation: "文档导航", back: "返回", home: "使用说明", loading: "正在读取文档…",
      failed: "暂时无法预览此文档", retry: "重试", empty: "此文档暂无内容。",
      blocked: "此链接不属于当前 Skill 的可预览文档，已阻止打开。", truncated: "文档较长，当前展示前 256 KiB。",
      packageFiles: "包内文件", hideFiles: "收起文件树", showFiles: "展开文件树", treeLoading: "正在读取目录…", treeEmpty: "此目录为空。",
      treeTruncated: "此目录项目较多，当前仅展示前 1000 项。", lazyFiles: "按需展开目录。普通上传的 SKILL.md 可在线编辑，其他文件只读查看。",
      notPreviewable: "此文件暂不支持在线预览。", noExecution: "仅展示文件信息，不会执行、解压或自动下载。",
      displayMode: "文档显示方式", preview: "预览", edit: "编辑", sourceLabel: "SKILL.md 完整源码", sourceHint: "编辑完整 Markdown，包含顶部元数据。保存后供后续调用读取。",
      unsaved: "未保存", save: "保存更改", saving: "正在保存…", cancel: "取消编辑", readOnly: "此 Skill 只读。仅普通上传包中的完整 SKILL.md 支持在线编辑。",
      saveFailed: "保存未完成，草稿已保留。", conflict: "文档已更新，你的草稿尚未保存。", conflictHint: "可先复制当前草稿，再重新加载最新文档并合并更改；不会覆盖其他窗口的修改。", reloadLatest: "重新加载最新文档",
      discardTitle: "放弃未保存的 Skill 更改？", discardBody: "当前草稿尚未保存。离开或重新加载会放弃这些更改。", discard: "放弃更改", keepEditing: "继续编辑",
    },
    en: {
      title: "Skill documents", navigation: "Document navigation", back: "Back", home: "Instructions", loading: "Loading document…",
      failed: "Unable to preview this document", retry: "Retry", empty: "This document is empty.",
      blocked: "This link is outside the current Skill's previewable documents and was blocked.", truncated: "This document is long; showing the first 256 KiB.",
      packageFiles: "Package files", hideFiles: "Hide file tree", showFiles: "Show file tree", treeLoading: "Loading directory…", treeEmpty: "This directory is empty.",
      treeTruncated: "This directory is large; showing the first 1,000 entries.", lazyFiles: "Expand folders as needed. Ordinary uploaded SKILL.md files can be edited; other files are read-only.",
      notPreviewable: "This file cannot be previewed online.", noExecution: "File information only. Nothing is executed, extracted or automatically downloaded.",
      displayMode: "Document display mode", preview: "Preview", edit: "Edit", sourceLabel: "Complete SKILL.md source", sourceHint: "Edit the complete Markdown, including frontmatter. Subsequent calls read the saved version.",
      unsaved: "Unsaved", save: "Save changes", saving: "Saving…", cancel: "Cancel editing", readOnly: "This Skill is read-only. Only complete SKILL.md files in ordinary uploaded packages can be edited online.",
      saveFailed: "Save failed. Your draft is preserved.", conflict: "The document changed. Your draft has not been saved.", conflictHint: "Copy your draft before reloading the latest document and merging changes. Another window's edits will not be overwritten.", reloadLatest: "Reload latest document",
      discardTitle: "Discard unsaved Skill changes?", discardBody: "This draft has not been saved. Leaving or reloading will discard your changes.", discard: "Discard changes", keepEditing: "Keep editing",
    },
  },
};
