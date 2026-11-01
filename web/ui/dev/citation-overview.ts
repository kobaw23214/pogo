import { createApp, h, reactive } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AnswerText from "../src/components/AnswerText.vue";
import SourcesPanel from "../src/components/SourcesPanel.vue";
import { collectSources } from "../src/agent/cite";
import { citationFileTarget, citeNavigationKey } from "../src/agent/citeNavigation";
import type { CiteSource } from "../src/api/types";
import { i18n } from "../src/i18n";
import { applyTheme } from "../src/theme";

// Isolated real-component fixture; all material is invented and no request is
// made to a backend, model, user file, clipboard or external website.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme(new URLSearchParams(location.search).get("theme") === "dark" ? "graphite" : "paper", false);
const stamp = (label: string, title: string, locator: string, over: Partial<CiteSource> = {}): CiteSource => ({
  label, type: "web_page", title, locator, call_id: `demo-${label}`, excerpt: "请求限制为每分钟 30 次。原文记录保留完整上下文。",
  content: "请求限制为每分钟 30 次。原文记录保留完整上下文。", hash: label, ts: 1789000000000, ...over,
});
const operation = stamp("S6", "仅操作回执", "https://alpha.example.test/empty");
const [emptyReceipt] = collectSources([{ result: {
  source: operation, text: "runtime: native; tab: t1; version: v1\npage: https://alpha.example.test/empty · 仅操作回执\n",
  details: { action: "click", ok: true, url: operation.locator },
} }]);
const sources: CiteSource[] = [
  stamp("S1", "Alpha 请求限制的完整标题与设计说明", "https://alpha.example.test/limits"),
  stamp("S2", "Beta 独立参考材料的完整标题", "https://beta.example.test/reference"),
  stamp("S3", "fixtures/retry.ts", "file://fixtures/retry.ts#L12-16", { type: "file_read", excerpt: "timeoutMs: 30_000", content: "timeoutMs: 30_000" }),
  stamp("S4", "其他查阅文章", "https://alpha.example.test/other"),
  stamp("S5", "npm test", "cmd://npm test", { type: "command_exec", excerpt: "12 tests passed", content: "12 tests passed" }),
  emptyReceipt,
  stamp("S7", "点击后读取到实际页文", "https://alpha.example.test/detail"),
];
const browserEnvelope = (source: CiteSource, schema: string, tail: string) =>
  `runtime: native; tab: t1; version: v1\npage: ${source.locator} · ${source.title}\nvisible DOM evidence / as-observed calculation:\n${JSON.stringify({ schema, frames: [{ url: source.locator, text: source.content, tables: [] }] }, null, 2)}\n${tail}`;
sources[0].content = browserEnvelope(sources[0], "dom-evidence-v1", sources[0].content!);
const unsupported = stamp("S24", "尚不支持的浏览器结构", "https://diagnostic.example.test/page");
unsupported.content = browserEnvelope(unsupported, "future-schema", "");
const citedText = "请求限制为 **每分钟 30 次**。[1](#cite:s1)\n\n两份材料从不同角度解释请求限制。[1](#cite:S1) [1](#cite:S2)\n\n本地配置中的超时值为 **30 秒**。[1](#cite:S3)\n\n普通链接仍可使用：[完整文档](https://docs.example.test/guide)。代码示例不计入引用：`[1](#cite:S4)`。";
const state = reactive({ streamSources: [sources[0]], fileOpened: "" });
const streamText = "尚未到达的材料。[1](#cite:S8)\n\n已经到达的材料。[1](#cite:s1)";
const legacySources = [
  stamp("S20", "about:blank", "about:blank", { excerpt: "page: about:blank\n[]", content: 'runtime: native; tab: t1; version: v1\npage: about:blank\n[{"id":"t1","url":"about:blank","title":""}]' }),
  stamp("S21", "browser", "tool://browser/legacy-error", { type: "tool", error: true, excerpt: "Browser connection was unavailable.", content: "Browser connection was unavailable." }),
  stamp("S22", "Browser", "https://docs.example.test/browser", { excerpt: "这是 Browser 文档的实际内容。", content: "这是 Browser 文档的实际内容。" }),
  stamp("S23", "GitLab 项目说明", "http://192.0.2.42:8929/root/project", { excerpt: "项目有 10 次提交和 2 个分支。", content: "项目有 10 次提交和 2 个分支。" }),
];
const sections = [
  { id: "cited", title: "正文引用与总览并存", text: citedText, sources },
  { id: "uncited", title: "没有正文引用时如实显示查阅材料", text: "本回答没有标注具体材料，下面的查阅列表不能代替逐项引用。", sources },
  { id: "operation", title: "纯操作记录保留在工作记录", text: "页面已打开。", sources: [emptyReceipt] },
  { id: "legacy", title: "旧记录移除空白初始化和 browser 状态项", text: "查阅材料保留真实文档。", sources: legacySources },
  { id: "diagnostic", title: "明确引用的错误诊断仍可追溯", text: "本次访问遇到浏览器连接错误。[1](#cite:S21)", sources: legacySources },
  { id: "protocol", title: "无法识别的结构如实标为工具输出", text: "浏览器记录了请求限制。[1](#cite:S24)", sources: [unsupported] },
];
const app = createApp({ render: () => h("section", [
  h("h1", "Pogo · 引用阅读体验（隔离演示）"),
  ...sections.map(section => h("article", { class: "conv-inner", "data-answer": section.id }, [
    h("h2", section.title), h(AnswerText, { source: section.text, sources: section.sources }),
    h(SourcesPanel, { text: section.text, sources: section.sources }),
  ])),
  h("article", { class: "conv-inner", "data-answer": "streaming" }, [
    h("h2", "流式材料晚到仍保持编号与引用"),
    h(AnswerText, { source: streamText, sources: state.streamSources, streaming: true }),
    h(SourcesPanel, { text: streamText, sources: state.streamSources }),
  ]),
  h("output", { id: "fixture-file" }, state.fileOpened),
]) });
app.provide(citeNavigationKey, {
  canOpen: src => Boolean(citationFileTarget(src)),
  open: src => { const target = citationFileTarget(src); state.fileOpened = target ? `${target.path}:${target.lineStart}-${target.lineEnd}` : ""; },
});
app.use(i18n).use(ElementPlus).mount("#app");
declare global { interface Window { __citationOverviewFixture: { completeStream: () => void }; } }
window.__citationOverviewFixture = { completeStream: () => {
  state.streamSources = [sources[0], stamp("S8", "迟到的完整文章标题", "https://late.example.test/article")];
} };
