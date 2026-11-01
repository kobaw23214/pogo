import { createApp, h, nextTick, reactive } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import AnswerText from "../src/components/AnswerText.vue";
import FilePreview from "../src/components/FilePreview.vue";
import WorkLog from "../src/components/WorkLog.vue";
import { buildTimeline } from "../src/agent/timeline";
import { buildExchanges } from "../src/agent/exchanges";
import { citationFileTarget, citeNavigationKey, type CitationFileTarget } from "../src/agent/citeNavigation";
import { api } from "../src/api/client";
import { emptyLive, type CiteSource } from "../src/api/types";
import { i18n } from "../src/i18n";
import { applyTheme } from "../src/theme";
import { worklogExpandAll } from "../src/agent/worklogPrefs";

// Isolated developer fixture: all file responses are invented in memory.
// No model, API, command, user file or persistent preference is touched.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme("paper", false);
worklogExpandAll.value = false;
const codeLines = Array.from({ length: 90 }, (_, i) => `// 示例配置 · 第 ${i + 1} 行`);
codeLines.splice(11, 5, "export const requestPolicy = {", "  timeoutMs: 30_000,", "  maxRetries: 3,", "  retryOnFailure: true,", "};");
codeLines.splice(63, 3, "export function shouldRetry(attempt: number) {", "  return attempt < requestPolicy.maxRetries;", "}");
const files: Record<string, string> = {
  "fixtures/retry.ts": codeLines.join("\n"),
  "fixtures/release.md": "# 发布说明\n\n本文是虚构演示材料。\n\n## 发布准备\n\n核对版本。\n## 回滚检查\n发布前必须核对回滚步骤。\n确认上一版本制品可用。\n\n完成后记录发布负责人。\n",
};
const readDelays: number[] = [];
api.fileContent = async (path) => {
  if (!(path in files)) throw new Error("演示文件不存在");
  const text = files[path];
  const delay = readDelays.shift() ?? 0;
  if (delay) await new Promise<void>((resolve) => setTimeout(resolve, delay));
  return { path, text, size: new TextEncoder().encode(text).length, mtime_ms: 1789000000000, truncated: false };
};
api.saveFile = async () => { throw new Error("演示文件仅供阅读，不会保存到工作区。"); };
const stamp = (label: string, path: string, first: number, last: number): CiteSource => ({
  label, type: "file_read", title: path, locator: `file://${path}#L${first}-${last}`, call_id: `demo-${label}`,
  excerpt: files[path].split("\n").slice(first - 1, last).join("\n"),
  content: files[path].split("\n").slice(first - 1, last).join("\n"), hash: `demo-${label}`, ts: 1789000000000,
});
const sources = [stamp("S1", "fixtures/retry.ts", 12, 16), stamp("S2", "fixtures/retry.ts", 64, 66), stamp("S3", "fixtures/release.md", 8, 10)];
const answer = "配置中，请求超时为 **30 秒**，失败后最多重试 **3 次**。[1](#cite:S1)\n\n重试函数根据当前次数和配置上限，判断是否继续。[1](#cite:S2)\n\n发布说明要求在发布前核对回滚步骤，并确认上一版本制品可用。[1](#cite:S3)";
const exchange = buildExchanges(buildTimeline([
  { id: "demo-user", role: "user", content: [{ type: "text", text: "检查请求配置和发布准备。" }] },
  { id: "demo-tools", role: "assistant", content: sources.map((s) => ({ type: "tool_use" as const, id: s.call_id, name: "read", input: { path: s.title } })) },
  { id: "demo-answer", role: "assistant", content: [{ type: "text", text: answer }] },
], Object.fromEntries(sources.map((s) => [s.call_id, { call_id: s.call_id, text: s.content!, source: s }])), emptyLive()), false)[0];
const state = reactive({ target: null as CitationFileTarget | null, revision: 0, checks: [] as string[], running: false });
function openSource(source: CiteSource) {
  const target = citationFileTarget(source);
  if (target) { state.target = target; state.revision++; }
}
const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
async function flush() { await nextTick(); await pause(100); }
async function checks() {
  state.running = true;
  state.checks = [];
  const check = async (name: string, run: () => Promise<void>) => {
    try { await run(); state.checks.push(`PASS：${name}`); }
    catch (error) { state.checks.push(`FAIL：${name} — ${error instanceof Error ? error.message : String(error)}`); }
  };
  const assert = (ok: unknown, message: string) => { if (!ok) throw new Error(message); };
  const anchors = () => [...document.querySelectorAll<HTMLAnchorElement>(".demo-answer .worklog .answer a[data-cite-anchor]")];
  await check("正文显示文件名和记录行范围", async () => {
    assert(anchors()[0]?.textContent?.includes("retry.ts"), "文件名缺失");
    assert(anchors()[0]?.textContent?.includes("12–16"), "行范围缺失");
  });
  await check("单击文件引用直接打开对应行", async () => {
    anchors()[0].click(); await flush();
    assert(state.target?.path === "fixtures/retry.ts" && state.target.lineStart === 12 && state.target.lineEnd === 16, "导航未保留范围");
    assert(document.querySelector(".demo-preview .preview"), "真实文件预览未挂载");
    assert(document.querySelectorAll(".demo-preview .cited-line").length === 5, "没有高亮完整范围");
  });
  await check("同文件另一处引用更新定位", async () => {
    const before = state.revision;
    anchors()[1].click(); await flush();
    assert(state.target?.lineStart === 64 && state.revision > before, "同文件定位未更新");
    const pane = document.querySelector<HTMLElement>(".demo-preview .preview > .body");
    const row = document.querySelector<HTMLElement>('.demo-preview .cited-line[data-source-line="64"]');
    assert(pane && row && pane.scrollTop > 0 && row.getBoundingClientRect().top >= pane.getBoundingClientRect().top
      && row.getBoundingClientRect().bottom <= pane.getBoundingClientRect().bottom, "目标行没有滚动进预览可见范围");
  });
  await check("重新点击同文件引用会读取最新内容", async () => {
    const original = files["fixtures/retry.ts"];
    try {
      files["fixtures/retry.ts"] = original.replace("timeoutMs: 30_000", "timeoutMs: 45_000");
      anchors()[0].click(); await flush();
      assert(document.querySelector(".demo-preview .code-block")?.textContent?.includes("45_000"), "仍显示旧文件缓存");
    } finally { files["fixtures/retry.ts"] = original; }
  });
  await check("快速切换文件时旧读取不会覆盖新文件", async () => {
    readDelays.push(250);
    anchors()[0].click(); await nextTick();
    anchors()[2].click(); await flush(); await pause(220);
    const text = document.querySelector(".demo-preview .code-block")?.textContent ?? "";
    assert(state.target?.path.endsWith("release.md") && text.includes("回滚步骤") && !text.includes("requestPolicy"), "迟到响应覆盖了当前文件");
  });
  await check("Markdown 引用打开源码位置", async () => {
    anchors()[2].click(); await flush();
    assert(state.target?.path.endsWith("release.md") && state.target.lineStart === 8, "Markdown 范围缺失");
    assert(document.querySelector(".demo-preview .code-block"), "没有显示 Markdown 源码行");
  });
  await check("键盘可进入来源详情并返回引用", async () => {
    const anchor = anchors()[0];
    const before = state.revision;
    anchor.focus();
    anchor.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
    await flush();
    const card = document.querySelector<HTMLElement>(".cite-card");
    assert(card?.contains(document.activeElement) && state.revision === before, "向下箭头没有进入证据卡片");
    document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await flush();
    assert(!document.querySelector(".cite-card") && document.activeElement === anchor, "Escape 没有返回原引用");
  });
  await check("收起执行记录后保留来源总览", async () => {
    const log = document.querySelector(".demo-answer .worklog")!;
    assert(!log.classList.contains("open"), "执行记录未收起");
    const panel = log.querySelector<HTMLElement>(".sources-panel");
    assert(panel && panel.getBoundingClientRect().height > 0, "来源入口不在可见结论下方");
    const trigger = panel!.querySelector<HTMLButtonElement>(".trigger")!;
    if (trigger.getAttribute("aria-expanded") !== "true") trigger.click();
    await flush();
    const summary = panel!.querySelector<HTMLButtonElement>(".source-summary")!;
    if (summary.getAttribute("aria-expanded") !== "true") summary.click();
    await flush();
    assert(panel!.querySelector(".source-excerpt")?.textContent?.includes("timeoutMs"), "列表未展示已保存的原文摘录");
    panel!.querySelector<HTMLButtonElement>(".source-primary")!.click(); await flush();
    assert(state.target?.path === "fixtures/retry.ts" && state.target.lineStart === 12, "列表文件动作没有保留定位范围");
  });
  await check("分享上下文只显示证据，不打开主会话文件", async () => {
    const before = state.revision;
    document.querySelector<HTMLAnchorElement>(".demo-share a[data-cite-anchor]")!.click(); await flush();
    assert(state.revision === before, "分享引用错误打开本地文件");
    assert(document.querySelector(".cite-card"), "分享来源详情不可见");
    document.querySelector<HTMLButtonElement>(".cite-card .cc-close")!.click(); await flush();
  });
  openSource(sources[0]);
  state.running = false;
}
const app = createApp({ render: () => h("div", [
  h("header", { class: "demo-heading" }, [h("h1", "Pogo · 引用阅读体验"), h("small", "本地演示 · 虚构数据"),
    h("button", { onClick: () => applyTheme("paper", false) }, "浅色"),
    h("button", { onClick: () => applyTheme("graphite", false) }, "深色"),
    h("button", { onClick: checks, disabled: state.running }, state.running ? "检查中…" : "运行交互检查")]),
  h("div", { class: "demo-layout" }, [
    h("article", { class: "demo-answer conv-inner" }, [
      h("div", { class: "demo-caption" }, "检查请求配置和发布准备"),
      h(WorkLog, { exchange, runActive: false }),
      h("section", { class: "demo-share share-page" }, [h("div", { class: "demo-caption" }, "分享上下文 · 保留记录中的摘录"), h(AnswerText, { source: "原回答引用的请求配置。[1](#cite:S1)", sources })]),
    ]),
    h("aside", { class: "demo-preview", "aria-label": "引用文件预览" }, state.target ? [h(FilePreview, {
      path: state.target.path, lineStart: state.target.lineStart, lineEnd: state.target.lineEnd,
      citation: true, revealVersion: state.revision, backClose: true, onBack: () => { state.target = null; },
    })] : [h("p", { class: "demo-empty" }, "点击正文中的文件引用，查看对应行。")]),
  ]),
  h("ol", { class: "demo-checks", "aria-live": "polite" }, state.checks.map((line) => h("li", line))),
]) });
app.provide(citeNavigationKey, { canOpen: (src) => { const target = citationFileTarget(src); return !!target && target.path in files; }, open: openSource });
app.use(i18n).use(ElementPlus).mount("#app");
