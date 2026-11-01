import { computed, createApp, defineComponent, h, reactive, ref } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import { i18n, setLocale } from "../src/i18n";
import { applyTheme } from "../src/theme";
import { api } from "../src/api/client";
import { toolsetWarning } from "../src/agent/studioPresentation";
import type { WorkspaceHTMLPreview } from "../src/api/types";
import HTMLBrowserPane from "../src/components/HTMLBrowserPane.vue";
import FilePreview from "../src/components/FilePreview.vue";

setLocale("zh-CN"); applyTheme("paper", false);
const Fixture = defineComponent({
  setup() {
    type Mode = "valid" | "invalid" | "held";
    const events = ref<string[]>([]);
    const record = (text: string) => { events.value = [...events.value, text]; };
    const modes = reactive<Record<string, Mode>>({ "fixture-first": "invalid" });
    const held = new Map<string, { owner: string; resolve: (value: WorkspaceHTMLPreview) => void }>();
    let sequence = 0;
    function preview(id: string, path: string, invalid = false): WorkspaceHTMLPreview {
      const token = id.padStart(64, "0");
      return { id: token.slice(-32), path, url: invalid ? "https://untrusted.invalid/preview" : `/api/html-previews/${token}/index.html`,
        browser_url: `http://127.0.0.1:5191/api/html-previews/${token}/index.html`, read_only: true,
        project_root: "reports", expires_at: "2026-10-28T00:00:00Z" };
    }
    api.createHTMLPreview = async (owner, path) => {
      const id = (++sequence).toString(16);
      const mode = modes[owner] ?? "valid";
      modes[owner] = "valid";
      record(`签发 ${owner} ${path} #${id} ${mode}`);
      if (mode === "held") return new Promise<WorkspaceHTMLPreview>(resolve => { held.set(owner, { owner, resolve }); });
      return preview(id, path, mode === "invalid");
    };
    api.revokeHTMLPreview = async (owner, id) => { record(`回收 ${owner} ${id}`); return { ok: true }; };
    const source = ref('<!doctype html>\n<h1>示例网页源码</h1>\n<script>document.body.dataset.fixture="source-is-not-executed"</script>\n');
    api.fileContent = async path => { record(`读取源码 ${path}`); return { path, text: source.value, binary: false, size: source.value.length, mtime_ms: 10 }; };
    api.saveFile = async (path, text, mtime, force) => { record(`保存示例 ${path} mtime=${mtime} force=${force}`); source.value = text; return { path, size: text.length, mtime_ms: 20 }; };

    const existingOwner = ref("fixture-existing");
    const existingVisible = ref(true);
    const existingInitial = preview("ee", "reports/existing.html");
    const sourceVisible = ref(true);
    const opened = ref<{ id: string; url: string; sessionId: string; path: string }>();
    const tools = ref<string[]>(["read", "todo", "subagent"]);
    const warning = computed(() => toolsetWarning({ tools: tools.value, skills: [], mcpServers: [] }, { skills: [], builtin_tools: [] }));
    const options = [["background_task", "后台任务"], ["watch", "Sentinel 管理"], ["watch_test", "Sentinel 试运行"], ["browser", "浏览器"]];
    const controls: Array<[string, () => void]> = [
      ["首个预览下次返回合法地址", () => { modes["fixture-first"] = "valid"; record("首个预览：下一次合法"); }],
      ["现有预览下次返回非法地址", () => { modes[existingOwner.value] = "invalid"; record("现有预览：下一次非法"); }],
      ["延迟现有预览下次响应", () => { modes[existingOwner.value] = "held"; record("现有预览：下一次暂停返回"); }],
      ["切换现有预览会话并等待", () => { modes["fixture-switched"] = "held"; existingOwner.value = "fixture-switched"; }],
      ["返回旧会话的非法地址", () => { const old = held.get("fixture-existing"); if (old) { held.delete(old.owner); old.resolve(preview("bad", "reports/existing.html", true)); record("返回旧会话非法地址"); } }],
      ["返回新会话的合法地址", () => { const next = held.get("fixture-switched"); if (next) { held.delete(next.owner); next.resolve(preview("f2", "reports/existing.html")); record("返回新会话合法地址"); } }],
      ["关闭现有预览", () => { existingVisible.value = false; }],
      ["关闭源码面板", () => { sourceVisible.value = false; }],
      ["深色", () => applyTheme("graphite", false)], ["浅色", () => applyTheme("paper", false)],
      ["English", () => setLocale("en")], ["中文", () => setLocale("zh-CN")],
    ];
    return () => h("main", [
      h("h1", "Pogo · HTML 与能力提示修复验证"), h("p", { class: "note" }, "真实组件 · 虚构接口响应与本地示例网页 · 不连接正式服务、不执行真实任务"),
      h("div", { class: "controls" }, controls.map(([label, onClick]) => h("button", { type: "button", onClick }, label))),
      h("section", { class: "panel", "data-testid": "first-preview" }, [h("h2", "首次打开：非法地址后可重试"),
        h("div", { class: "pane-box" }, [h(HTMLBrowserPane, { path: "reports/first.html", sessionId: "fixture-first", active: true })])]),
      h("section", { class: "panel", "data-testid": "existing-preview" }, [h("h2", "已有页面：刷新失败保留旧页"),
        h("p", { class: "note", "data-testid": "existing-owner" }, existingOwner.value),
        existingVisible.value ? h("div", { class: "pane-box" }, [h(HTMLBrowserPane, { path: "reports/existing.html", sessionId: existingOwner.value, initialId: existingOwner.value === "fixture-existing" ? existingInitial.id : undefined, initialUrl: existingOwner.value === "fixture-existing" ? existingInitial.url : undefined, active: true,
          onClaimCapability: id => record(`接管 ${id}`) })]) : h("p", "示例预览已关闭")]),
      h("section", { class: "panel", "data-testid": "source-preview" }, [h("h2", "源码面板：显式打开浏览器"),
        sourceVisible.value ? h("div", { class: "source-box" }, [h(FilePreview, { path: "reports/source.html", sessionId: "fixture-source", onOpenCurrentBrowser: request => { opened.value = request; record(`打开当前浏览器 ${request.sessionId} ${request.path}`); }, onOpenBrowser: request => record(`打开内置浏览器 ${request.sessionId} ${request.id}`) })]) : h("p", "示例源码已关闭"),
        opened.value ? h("div", { class: "pane-box", "data-testid": "opened-from-source" }, [h(HTMLBrowserPane, { key: opened.value.id, path: opened.value.path, sessionId: opened.value.sessionId, initialUrl: opened.value.url, initialId: opened.value.id, active: true, onClaimCapability: id => record(`源码浏览器接管 ${id}`) })]) : null]),
      h("section", { class: "panel", "data-testid": "warning-preview" }, [h("h2", "Agent 能力提示"), h("div", { class: "options" }, options.map(([name, label]) => h("label", [h("input", { type: "checkbox", checked: tools.value.includes(name), onChange: (event: Event) => { tools.value = (event.target as HTMLInputElement).checked ? [...tools.value, name] : tools.value.filter(tool => tool !== name); } }), label]))),
        h("p", { class: "warning", role: "note", "data-testid": "tool-warning" }, warning.value || "当前配置包含可执行能力")]),
      h("section", { class: "panel" }, [h("h2", "隔离请求与回收记录"), h("output", { class: "evidence", "data-testid": "evidence" }, events.value.join("\n"))]),
    ]);
  },
});
createApp(Fixture).use(ElementPlus).use(i18n).mount("#app");
