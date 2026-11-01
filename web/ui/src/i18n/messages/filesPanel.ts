// Copy for src/components/FilesPanel.vue — the workspace file panel header:
// tabs, workspace-scope badge title and the header tool buttons.
export default {
  namespace: "filesPanel",
  messages: {
    "zh-CN": {
      tabFiles: "文件",
      tabChanges: "改动",
      workspaceTitle: "当前会话工作区：{ws}",
      quickOpen: "快速打开文件（⌘P）",
      refresh: "刷新",
      rename: "重命名", delete: "删除", renameTitle: "重命名文件或文件夹", deleteTitle: "删除此项目？", newName: "新名称", deleteConfirm: "确认删除",
      deleteFileBody: "此文件将被永久删除，无法撤销。历史消息里的文件引用也可能失效。",
      deleteFolderBody: "此文件夹及其中所有文件和子文件夹将被永久删除，无法撤销。请确认不再需要这些内容。",
      editingBlocked: "此项目仍有正在编辑的文档或文本。请先保存并关闭，或取消编辑，再执行此操作。",
    },
    en: {
      tabFiles: "Files",
      tabChanges: "Changes",
      workspaceTitle: "Current session workspace: {ws}",
      quickOpen: "Quick open file (⌘P)",
      refresh: "Refresh",
      rename: "Rename", delete: "Delete", renameTitle: "Rename file or folder", deleteTitle: "Delete this item?", newName: "New name", deleteConfirm: "Delete permanently",
      deleteFileBody: "This permanently deletes the file and cannot be undone. References in previous messages may stop working.",
      deleteFolderBody: "This permanently deletes the folder and all files and folders inside it. This cannot be undone.",
      editingBlocked: "A document or text file here is still being edited. Save and close it, or cancel editing, before continuing.",
    },
  },
};
