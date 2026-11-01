export default {
  namespace: "spillBrowser",
  messages: {
    "zh-CN": {
      browse: "浏览文件", title: "Spill 文件", rootLabel: "工作目录：", readOnly: "只读浏览主目录及其中的文件。文件仅以文本显示，不能编辑、删除或上传。",
      breadcrumb: "Spill 目录路径", files: "目录与文件", preview: "文件预览", parent: "上一级", refresh: "刷新", retry: "重试", loading: "正在加载…",
      notCreated: "工作目录尚未创建；产生 Spill 文件后会自动出现。", empty: "此目录没有可浏览的文件。", selectFile: "选择文件以查看只读内容。", emptyFile: "（空文件）", binary: "此文件不是可预览的 UTF-8 文本，无法在这里打开。",
      previous: "上一页", next: "下一页", entriesCount: "本页 {n} 项", byteRange: "字节 {start}–{end} / {total}", directoryPartial: "目录尚未列完，可翻页继续查看。", filePartial: "当前仅显示文件的一部分，可翻页继续查看。", skipped: "已跳过 {n} 个不可读取的条目。", scanLimit: "已达到目录扫描上限，当前列表不代表全部文件。",
      continuedLine: "当前页从第 {n} 行的中间继续显示。", fileChanged: "文件已变化，旧的分页和行号已重置。请点击重试，从文件开头重新查看。",
    },
    en: {
      browse: "Browse files", title: "Spill files", rootLabel: "Working directory:", readOnly: "Browse the root directory and its files in read-only mode. Files are displayed as text; editing, deleting and uploading are unavailable.",
      breadcrumb: "Spill directory path", files: "Directories and files", preview: "File preview", parent: "Up", refresh: "Refresh", retry: "Retry", loading: "Loading…",
      notCreated: "The working directory has not been created yet. It will appear when a Spill file is saved.", empty: "There are no browsable files in this directory.", selectFile: "Select a file to view its read-only contents.", emptyFile: "(Empty file)", binary: "This file is not previewable UTF-8 text and cannot be opened here.",
      previous: "Previous", next: "Next", entriesCount: "{n} entries on this page", byteRange: "Bytes {start}–{end} / {total}", directoryPartial: "This directory listing is partial. Continue to the next page.", filePartial: "Only part of this file is shown. Continue to the next page.", skipped: "Skipped {n} unreadable entries.", scanLimit: "The directory scan limit was reached. This list does not include every file.",
      continuedLine: "This page continues partway through line {n}.", fileChanged: "The file changed. Previous pages and line numbers were reset. Retry to reopen it from the beginning.",
    },
  },
};
