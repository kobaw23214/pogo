import { createApp, h, nextTick, reactive } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "../src/styles.scss";
import AnswerText from "../src/components/AnswerText.vue";
import FilePreview from "../src/components/FilePreview.vue";
import { openTab, type DockTab } from "../src/components/dockSheets";
import { attachmentWorkspacePath, reserveOfficeFile, sameWorkspaceFile } from "../src/agent/officeFiles";
import { workspaceFileTarget, type WorkspaceFileTarget } from "../src/agent/workspaceFileLinks";
import { workspaceFileNavigationKey, type WorkspaceFileScope } from "../src/agent/workspaceFileNavigation";
import { i18n } from "../src/i18n";
import { applyTheme } from "../src/theme";

// Development-only harness. The browser runner supplies invented HTTP file
// responses. These are real AnswerText/FilePreview components; this lightweight
// provider mirrors the existing dock helpers, not the full AgentView/router.
// It never contacts a model, actual website, filesystem or Office engine.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme("paper", false);
const initialScope: WorkspaceFileScope = { conversationId: "qa-chat-one", root: "/home/pogo", cwd: "/home/pogo/users/qa", workspace: "users/qa" };
const longPath = "exports/很长的中文目录/逐月分析与核验资料/中文结果报告.md";
const source = [
  `已经完成，文件保存在 \`/home/pogo/users/qa/${longPath}\`。`,
  "纯文本文件：exports/notes.txt。",
  "[查看中文结果](exports/很长的中文目录/逐月分析与核验资料/中文结果报告.md) · [第二至三行](exports/notes.txt#L2-L3)",
  "图片：`exports/picture.png`，PDF：`exports/guide.pdf`。",
  "工作簿：`exports/结果表.xlsx`。缺失文件：`exports/missing.txt`。",
  "[外部网站](https://example.invalid/exports/notes.txt) 应保持普通网页链接。",
  "```sh\ncat exports/notes.txt\n```",
].join("\n\n");
const state = reactive({ scope: { ...initialScope }, source, tabs: [] as DockTab[], selected: "", opens: [] as { target: WorkspaceFileTarget; conversation: string; full: string }[], focuses: [] as string[] });
let releaseReservation: (() => void) | null = null;
function open(target: WorkspaceFileTarget, origin: Pick<WorkspaceFileScope, "conversationId" | "cwd">) {
  if (origin.conversationId !== state.scope.conversationId || origin.cwd !== state.scope.cwd) return;
  const resolved = workspaceFileTarget(target.path, state.scope);
  if (!resolved || resolved.path !== target.path) return;
  const full = attachmentWorkspacePath(state.scope.workspace, resolved.path);
  const existing = state.tabs.find(tab => sameWorkspaceFile(tab.path, full));
  const id = existing?.id ?? `preview:${full}`;
  state.tabs = openTab(state.tabs, { id, kind: "preview", label: resolved.name, path: full, lineStart: target.lineStart, lineEnd: target.lineEnd, closable: true });
  state.selected = id;
  state.opens.push({ target: { ...target }, conversation: state.scope.conversationId, full });
}
const app = createApp({ render: () => h("div", [
  h("header", [h("h1", "回答中的文件链接"), h("p", "隔离开发验证 · 合成文件 API · 真实 Vue 阅读与预览组件；不代表完整 AgentView 或 Office 编辑引擎验收")]),
  h("div", { class: "qa-layout" }, [
    h("article", { class: "qa-answer conv-inner" }, [
      h(AnswerText, { class: "qa-main-answer", source: state.source }),
      h("section", { class: "qa-scope subsheet" }, [h("h2", "子记录范围"), h(AnswerText, { source: "不能操作主会话：`exports/notes.txt`。" })]),
      h("section", { class: "qa-scope share-page" }, [h("h2", "分享范围"), h(AnswerText, { source: "不能操作主会话：`exports/notes.txt`。" })]),
    ]),
    h("aside", { class: "qa-panel", "aria-label": "真实文件预览组件" }, [
      h("nav", { class: "qa-tabs", role: "tablist" }, state.tabs.map(tab => h("button", { role: "tab", "aria-selected": tab.id === state.selected, "data-path": tab.path, onClick: () => { state.selected = tab.id; } }, tab.label))),
      ...state.tabs.map(tab => h("div", { key: `${state.scope.conversationId}:${tab.id}`, class: "qa-preview", style: { display: tab.id === state.selected ? "block" : "none" } }, [h(FilePreview, {
        path: tab.path!, sessionId: state.scope.conversationId, backClose: true, lineStart: tab.lineStart, lineEnd: tab.lineEnd, revealVersion: tab.revealVersion, citation: tab.citation,
        onBack: () => { state.tabs = state.tabs.filter(entry => entry.id !== tab.id); state.selected = state.tabs[0]?.id ?? ""; },
        onFocusOffice: (path: string) => { state.focuses.push(path); },
      })])),
    ]),
  ]),
  h("pre", { id: "fixture-state" }, JSON.stringify({ conversation: state.scope.conversationId, cwd: state.scope.cwd, opened: state.opens.length, tabs: state.tabs.length, focusExisting: state.focuses.length }, null, 2)),
]) });
app.provide(workspaceFileNavigationKey, { scope: () => state.scope, open });
app.use(i18n).use(ElementPlus).mount("#app");

const qa = {
  state, longPath,
  async reset() { releaseReservation?.(); releaseReservation = null; state.tabs = []; state.selected = ""; state.opens = []; state.focuses = []; state.scope = { ...initialScope }; state.source = source; await nextTick(); },
  async reserveOffice() { releaseReservation?.(); releaseReservation = reserveOfficeFile("users/qa/exports/结果表.xlsx", state.scope.conversationId); if (!releaseReservation) throw new Error("Fixture reservation already held"); },
  async clearTabs() { state.tabs = []; state.selected = ""; await nextTick(); },
  async staleClick(kind: "conversation" | "cwd") {
    const button = document.querySelector<HTMLButtonElement>(".qa-main-answer button[data-workspace-file]");
    if (!button) throw new Error("No previously rendered button");
    const before = state.opens.length;
    if (kind === "conversation") state.scope = { ...state.scope, conversationId: "qa-chat-two" };
    else state.scope = { ...state.scope, cwd: "/home/pogo/users/other", workspace: "users/other" };
    button.click(); // Deliberately before the reactive render replaces old DOM.
    await nextTick(); return { before, after: state.opens.length };
  },
};
(window as unknown as { __answerFileQA: typeof qa }).__answerFileQA = qa;
