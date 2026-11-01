export default {
  namespace: "answerReview",
  messages: {
    "zh-CN": {
      elapsed: "{seconds} 秒",
      legacyCitation: "系统曾请求补充正文引用。",
      legacyQuote: "系统曾请求核对引文。",
      citation: { running: "正在整理引用", completed: "引用检查完成", fallback: "引用未更新，已保留原回答", timeout: "引用整理超时，已保留原回答", cancelled: "引用整理已停止，已保留原回答" },
      quote: { running: "正在核对引文", completed: "引文检查完成", fallback: "引文未更新，已保留原回答", timeout: "引文核对超时，已保留原回答", cancelled: "引文核对已停止，已保留原回答" },
    },
    en: {
      elapsed: "{seconds}s",
      legacyCitation: "The system requested inline source citations.",
      legacyQuote: "The system requested a quotation check.",
      citation: { running: "Organizing citations", completed: "Citation check complete", fallback: "Citations unchanged; original answer kept", timeout: "Citation review timed out; original answer kept", cancelled: "Citation review stopped; original answer kept" },
      quote: { running: "Checking quotations", completed: "Quotation check complete", fallback: "Quotations unchanged; original answer kept", timeout: "Quotation review timed out; original answer kept", cancelled: "Quotation review stopped; original answer kept" },
    },
  },
};
