export default {
  namespace: "readManyResult",
  messages: {
    "zh-CN": {
      files: "{n} 个文件",
      failed: "{n} 个读取失败",
      truncated: "{n} 个被截断（额度按 {ways} 个文件均分）",
      lines: "{n} 行",
      shown: "只读了 {shown}/{total} 行",
      error: "失败",
      bodyMissing: "内容对不上分段标记，只能看原始输出",
      continueFrom: "从第 {line} 行继续",
      suggestContinue: "继续读 {path}，从第 {line} 行开始",
    },
    en: {
      files: "{n} files",
      failed: "{n} failed",
      truncated: "{n} truncated (budget split {ways} ways)",
      lines: "{n} lines",
      shown: "{shown} of {total} lines",
      error: "failed",
      bodyMissing: "This file's section could not be located in the output.",
      continueFrom: "Continue from line {line}",
      suggestContinue: "Continue reading {path} from line {line}",
    },
  },
};
