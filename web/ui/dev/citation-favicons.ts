import { createApp, h, reactive } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AnswerText from "../src/components/AnswerText.vue";
import SourcesPanel from "../src/components/SourcesPanel.vue";
import { createCiteFavicons, citeFaviconKey } from "../src/agent/citeFavicon";
import type { CiteSource } from "../src/api/types";
import { i18n } from "../src/i18n";
import { applyTheme } from "../src/theme";

// Isolated real-component fixture: invented stamps and bounded icon bytes only.
// No backend, model, user storage preference or external icon URL is accessed.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme(new URLSearchParams(location.search).get("theme") === "dark" ? "graphite" : "paper", false);
const icon = (letter: string, color: string) => "data:image/svg+xml;base64," + btoa(
  `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><rect width="16" height="16" rx="3" fill="${color}"/><text x="8" y="12" text-anchor="middle" font-family="sans-serif" font-size="12" font-weight="bold" fill="white">${letter}</text></svg>`,
);
const icons = { alpha: icon("A", "#258c6a"), beta: icon("B", "#7a4bc0"), gamma: icon("C", "#d67725"), history: icon("H", "#317bb8"), recovered: icon("R", "#2c8554"), broken: "data:image/png;base64,AAAA" };
const registry = createCiteFavicons();
const stamp = (label: string, title: string, locator: string, favicon?: string, type = "web_page"): CiteSource => ({
  label, title, locator, type, call_id: `fixture-first-${label}`, excerpt: `${title}：仅供图标回归的独立原文记录。`,
  content: `${title}：仅供图标回归的独立原文记录。所有网站和数据均为虚构，原文不得因图标补齐而改变。`,
  hash: `fixture-first-${label}`, ts: 1789000000000, ...(favicon ? { favicon } : {}),
});
const initialFirst: CiteSource[] = [
  stamp("S1", "Alpha 第一篇记录", "https://alpha.example.test/news?edition=one", icons.alpha),
  stamp("S2", "Beta 第二篇记录", "https://beta.example.test/report", icons.beta),
  stamp("S3", "Broken 图标解码失败", "https://broken.example.test/article", icons.broken),
  stamp("S4", "Historical 历史记录", "https://history.example.test/story?view=one#old-section"),
  stamp("S5", "Unsafe 外部图标地址", "https://unsafe.example.test/article", "https://unrelated-icon.example.test/tracker.png"),
  stamp("S6", "fixtures/demo.ts", "file://fixtures/demo.ts#L12-16", undefined, "file_read"),
  stamp("S7", "Uncited 尚未引用", "https://uncited.example.test/search", icons.beta, "search_result"),
];
const initialSecond: CiteSource[] = [{ ...stamp("S1", "Gamma 第二回答专用记录", "https://gamma.example.test/independent", icons.gamma), call_id: "fixture-second-S1", hash: "fixture-second-S1" }];
const textFirst = "直接引用 Alpha 第一篇记录。[1](#cite:S1)\n\n分组引用 Alpha 和 Beta，两份原文各自保留网站身份。[1](#cite:S1) [1](#cite:S2)\n\n破图应回退为地球图标。[1](#cite:S3)\n\n历史记录最初没有图标，只能按精确 URL 从同一会话的浏览器元数据补齐。[1](#cite:S4)\n\n外部图片地址不能触发图标网络请求。[1](#cite:S5)\n\n文件记录保留原有文件 glyph 和行范围。[1](#cite:S6)";
const textSecond = "这是第二个独立回答；其 S1 属于 Gamma 网站，不得使用第一回答的 Alpha 图标或原文。[1](#cite:S1)";
const state = reactive({ first: initialFirst.map(source => ({ ...source })), second: initialSecond.map(source => ({ ...source })), status: "初始图标 · 历史记录尚未补齐" });
const publishHistorical = () => {
  registry.observe([{ url: "https://history.example.test/story?view=one#new-section", favicon: icons.history }]);
  state.status = "按精确 URL（query 保留、hash 忽略）补齐历史网站图标";
};
const replaceIcon = (answer: "first" | "second", label: string, value?: string) => {
  const source = state[answer].find(item => item.label === label);
  if (!source) throw new Error("Unknown fixture source");
  source.favicon = value; state[answer] = [...state[answer]];
};
const clearCache = () => { registry.clear(); state.status = "会话图标缓存已清空；显式来源图标保留"; };
const app = createApp({ render: () => h("section", [
  h("header", { class: "fixture-heading" }, [
    h("h1", "Pogo · 引用网站图标"), h("small", "隔离真实组件 · 虚构材料"),
    h("button", { onClick: () => applyTheme("paper", false) }, "浅色"),
    h("button", { onClick: () => applyTheme("graphite", false) }, "深色"),
    h("button", { onClick: publishHistorical }, "补齐历史图标"),
    h("button", { onClick: clearCache }, "清空会话图标缓存"),
  ]),
  ...(["first", "second"] as const).map(answer => h("article", { class: "fixture-answer conv-inner", "data-answer": answer }, [
    h("h2", answer === "first" ? "第一个回答 · 多网站和分组来源" : "第二个回答 · 同 label，另一网站"),
    h("p", { class: "fixture-caption" }, answer === "first" ? "正文、悬浮证据卡和来源列表使用相同的来源字节。" : "独立回答的 S1 与上方 S1 没有关联。"),
    h(AnswerText, { source: answer === "first" ? textFirst : textSecond, sources: state[answer] }),
    h(SourcesPanel, { sources: state[answer], text: answer === "first" ? textFirst : textSecond }),
  ])),
  h("output", { id: "fixture-status", role: "status" }, state.status),
]) });
app.use(i18n).use(ElementPlus).provide(citeFaviconKey, registry).mount("#app");

declare global {
  interface Window {
    __citationFaviconFixture: {
      icons: typeof icons;
      publishHistorical: typeof publishHistorical;
      replaceIcon: typeof replaceIcon;
      clearCache: typeof clearCache;
      observe: typeof registry.observe;
      snapshot: () => { first: CiteSource[]; second: CiteSource[] };
    };
  }
}
window.__citationFaviconFixture = {
  icons, publishHistorical, replaceIcon, clearCache, observe: pages => registry.observe(pages),
  snapshot: () => ({ first: state.first.map(source => ({ ...source })), second: state.second.map(source => ({ ...source })) }),
};
