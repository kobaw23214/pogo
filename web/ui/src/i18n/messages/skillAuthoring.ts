export default {
  namespace: "skillAuthoring",
  messages: {
    "zh-CN": {
      searchAction: "查找 Skill", createAction: "创建 Skill", updateAction: "更新 Skill", saved: "Skill 已保存", reused: "已保存，未重复创建", open: "查看与编辑", private: "仅当前 Agent", title: "Agent 创建的 Skill",
      enabled: "已绑定当前 Agent", pending: "本次保存时待生效，本轮结束后应用", draft: "已保存为草稿，尚未启用", structural: "格式已校验", behavior: "尚未进行行为测试",
      fileTreeHint: "按需展开参考文档。当前版本的 SKILL.md 可编辑，参考文档和历史版本只读。",
      newerVersion: "此 Skill 已有更新；当前展示的是已加载版本，可重新加载最新内容。",
      loading: "正在读取 Skill…", failed: "无法读取 Skill", retry: "重试", version: "版本", latest: "最新版本", historical: "历史版本只读；切回最新版本可继续编辑。",
      export: "导出 ZIP", exporting: "正在导出…", exportFailed: "导出失败", current: "当前", savedNotice: "更改已保存为新版本，后续调用会使用新内容。",
      library: "本 Agent 创建", libraryHint: "私有 Skill 与草稿", empty: "暂无创建的 Skill。可让 Agent 将常用流程保存为 Skill。", libraryFailed: "暂时无法读取本 Agent 的 Skill。",
      neverTested: "格式校验不代表实际执行效果；使用前可在隔离会话中验证。", notAvailable: "此 Skill 已不可用，请刷新列表。",
    },
    en: {
      searchAction: "Find Skills", createAction: "Create Skill", updateAction: "Update Skill", saved: "Skill saved", reused: "Already saved; no duplicate created", open: "View and edit", private: "This Agent only", title: "Agent-authored Skill",
      enabled: "Bound to this Agent", pending: "Pending when saved; applies after the current run", draft: "Saved as a draft; not enabled", structural: "Format validated", behavior: "Behavior not tested",
      fileTreeHint: "Expand reference folders as needed. The current SKILL.md is editable; references and past versions are read-only.",
      newerVersion: "A newer version is available. You are viewing the loaded revision; reload to see the latest content.",
      loading: "Loading Skill…", failed: "Unable to load Skill", retry: "Retry", version: "Version", latest: "Latest version", historical: "Past versions are read-only. Select the latest version to edit.",
      export: "Export ZIP", exporting: "Exporting…", exportFailed: "Export failed", current: "Current", savedNotice: "Changes saved as a new version. Subsequent calls use the updated content.",
      library: "Created for this Agent", libraryHint: "Private Skills and drafts", empty: "No authored Skills yet. Ask the Agent to save a repeatable workflow as a Skill.", libraryFailed: "Unable to load this Agent's Skills.",
      neverTested: "Format validation does not verify behavior. Try it in an isolated conversation before use.", notAvailable: "This Skill is no longer available. Refresh the list.",
    },
  },
};
