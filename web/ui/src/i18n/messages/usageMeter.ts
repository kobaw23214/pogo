export default {
  namespace: "usageMeter",
  messages: {
    "zh-CN": {
      hint: "本会话累计 token：输入 {input}（缓存命中 {cachePct}%），输出 {output} — 点击查看明细",
      totalLabel: "本会话累计 tok",
      cacheTag: "缓存命中 {pct}%",
      rows: {
        inputTotal: "输入总计",
        cacheHit: "其中缓存命中",
        billedInput: "实付输入",
        output: "输出",
        reasoning: "其中思考",
      },
      foot: "缓存命中是输入的一部分，不是额外增加；每轮都会重发整段对话，所以累计量比对话本身增长得快。",
    },
    en: {
      hint: "Session total tokens: input {input} (cache hit {cachePct}%), output {output} — click for details",
      totalLabel: "session total tok",
      cacheTag: "cache hit {pct}%",
      rows: {
        inputTotal: "Total input",
        cacheHit: "of which cached",
        billedInput: "Billed input",
        output: "Output",
        reasoning: "of which thinking",
      },
      foot: "Cache hits are part of the input, not an addition; the whole conversation is resent every turn, so the total grows faster than the conversation itself.",
    },
  },
};
