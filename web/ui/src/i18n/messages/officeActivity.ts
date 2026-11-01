export default {
  namespace: "officeActivity",
  messages: {
    "zh-CN": {
      title: "Agent 操作", open: "查看 Agent 操作记录", close: "收起操作记录", view: "查看", changed: "已改 {count} 项",
      reading: "正在读取", writing: "正在修改", verifying: "正在核对", saving: "正在保存", pending: "等待执行",
      succeeded: "已完成", rejected: "未执行", unknown: "结果待确认", running: "进行中",
      readDone: "已读取文档", writeDone: "修改已完成", verifyDone: "已回读修改范围", saveDone: "已保存到工作区",
      read: "读取文档", write: "修改文档", replace: "修改文字", apply: "修改单元格", sheets: "查看工作表", save: "保存到工作区", undo: "撤销", redo: "重做", verify: "核对内容",
      all: "全部操作", changes: "改动", records: "{count} 条记录", latest: "当前面板显示最近 {count} 条记录。",
      before: "原内容", after: "新内容", empty: "空值", diff: "对照修改前后", moreChanges: "本次共 {total} 项改动，展示前 {shown} 项。", excerpt: "显示部分改动，长文本为节选。",
      noChanges: "当前记录中没有已确认的改动。", noWrite: "本次修改未执行，原内容保留。", uncertain: "尚未确认结果，请先读取核对，不要重复修改。",
      note: "记录是当时的操作结果，后续编辑可能改变内容。", unsavedNote: "记录是当时的操作结果，后续编辑可能改变内容；修改完成不代表已保存到工作区。",
      rangeConfirmed: "该范围的输入/公式与修改回执一致。", rangeReadOnly: "已回读该范围，尚未确认与修改回执一致。", noDifference: "当前内容与目标相同，没有新增改动。", savedVersion: "已保存到工作区 · 版本 {version}", receiptUnknown: "操作结果尚未获得完整确认。",
    },
    en: {
      title: "Agent activity", open: "View agent activity", close: "Collapse activity", view: "Review", changed: "{count} changes",
      reading: "Reading", writing: "Editing", verifying: "Checking", saving: "Saving", pending: "Queued",
      succeeded: "Complete", rejected: "Not applied", unknown: "Unconfirmed", running: "In progress",
      readDone: "Document read", writeDone: "Edit complete", verifyDone: "Modified range read back", saveDone: "Saved to workspace",
      read: "Read document", write: "Edit document", replace: "Edit text", apply: "Edit cells", sheets: "List worksheets", save: "Save to workspace", undo: "Undo", redo: "Redo", verify: "Check content",
      all: "All activity", changes: "Changes", records: "{count} records", latest: "This panel shows the latest {count} records.",
      before: "Before", after: "After", empty: "Empty", diff: "Compare before and after", moreChanges: "Showing the first {shown} of {total} changes.", excerpt: "Only some changes are shown; long text is excerpted.",
      noChanges: "No confirmed changes in these records.", noWrite: "This edit was not applied; the original content is preserved.", uncertain: "The result is unconfirmed. Read and check before retrying.",
      note: "Records describe results at the time; later edits may change the content.", unsavedNote: "Records describe results at the time; later edits may change the content. Completing an edit does not confirm a workspace save.",
      rangeConfirmed: "The inputs/formulas in this range match the edit receipt.", rangeReadOnly: "This range was read back; a match with the edit receipt has not been confirmed.", noDifference: "The content already matches the target; no new change was made.", savedVersion: "Saved to workspace · version {version}", receiptUnknown: "The operation result has not been fully confirmed.",
    },
  },
};
