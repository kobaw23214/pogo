export default {
  namespace: "citationFile",
  messages: {
    "zh-CN": {
      currentFile: "当前文件，内容可能与引用时不同。",
      line: "引用位置：第 {line} 行",
      lines: "引用位置：第 {start}–{end} 行",
      invalid: "引用行号无效，无法定位。",
      nonText: "此文件无法按源码行号定位。",
      truncated: "引用范围超出完整预览，无法定位。可下载原文件核查。",
      outOfRange: "引用行号超出当前文件，无法定位。",
    },
    en: {
      currentFile: "Current file; content may differ from when it was cited.",
      line: "Cited location: line {line}",
      lines: "Cited location: lines {start}–{end}",
      invalid: "The cited line range is invalid and cannot be located.",
      nonText: "This file does not support source-line navigation.",
      truncated: "The cited range is outside the complete preview. Download the original to check it.",
      outOfRange: "The cited lines are outside the current file and cannot be located.",
    },
  },
};
