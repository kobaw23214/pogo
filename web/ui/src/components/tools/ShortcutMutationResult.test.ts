import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import { i18n, setLocale } from "@/i18n";
import type { ShortcutDetails, ToolResult } from "@/api/types";
import type { TimelineCall } from "@/agent/timeline";
import ShortcutMutationResult from "./ShortcutMutationResult.vue";
vi.mock("../ShortcutReferenceDialog.vue", () => ({ default: defineComponent({ props: ["modelValue"], setup: props => () => h("aside", { "data-preview": "shortcut" }, props.modelValue ? JSON.stringify(props.modelValue) : "") }) }));
vi.mock("../CodeBlock.vue", () => ({ default: defineComponent({ props: ["code"], setup: props => () => h("pre", props.code) }) }));
import ToolCall from "../ToolCall.vue";
async function render(component: object, props: Record<string, unknown>) { const app = createSSRApp({ render: () => h(component, props) }); app.use(i18n); return renderToString(app); }
const change = () => ({ action: "move" as const, kind: "shortcut" as const, id: "sh_one", title: "合成数据库", from_parent_id: "folder_old", to_parent_id: "folder_new" });
const mutation = (): ShortcutDetails => ({ type: "shortcut", action: "move", saved: true, changed: true, agent_id: "ag_test", request_id: "move_once", revision: 4, applied_revision: 4, changes: [change()] });
const preview = (): ShortcutDetails => ({ type: "shortcut", action: "merge", saved: false, changed: false, agent_id: "ag_test", revision: 4, changes: [], plan: { id: "sm_plan", revision: 4, groups: [{ title: "合成数据库", keep_id: "sh_keep", duplicate_ids: ["sh_remove"], parent_id: "folder_one" }], removals: 1 } });
function call(name: TimelineCall["name"], details: ShortcutDetails, result: Partial<ToolResult> = {}): TimelineCall { return { callId: "call_one", name, args: {}, running: false, orphaned: false, result: { call_id: "call_one", text: "fallback host receipt", details, ...result } }; }
afterEach(() => { setLocale("zh-CN"); vi.unstubAllGlobals(); });

describe("compact shortcut operation results", () => {
  it("shows a concise status and native expandable change list without acting like an openable card", async () => {
    setLocale("zh-CN"); const fetch = vi.fn(), notify = vi.fn(); vi.stubGlobal("fetch", fetch); vi.stubGlobal("window", { dispatchEvent: notify });
    const value = mutation(), before = JSON.stringify(value), html = await render(ShortcutMutationResult, { details: value });
    expect(html).toContain("已移动 1 项"); expect(html).toContain('data-testid="shortcut-change-list"'); expect(html).toContain("合成数据库"); expect(html).toContain("folder_old"); expect(html).toContain("folder_new");
    expect(html).toContain("仅当前 Agent"); expect(html).toContain("资料版本 4"); expect(html).not.toContain("<button"); expect(html).not.toContain("<details open");
    expect(JSON.stringify(value)).toBe(before); expect(fetch).not.toHaveBeenCalled(); expect(notify).not.toHaveBeenCalled();
  });
  it("labels idempotent old acknowledgements honestly without implying another deletion", async () => {
    setLocale("zh-CN"); const html = await render(ShortcutMutationResult, { details: { ...mutation(), action: "delete", changed: false, reused: true, revision: 8, changes: [] } });
    expect(html).toContain("此前已执行，本次未重复修改"); expect(html).toContain("首次生效版本 4"); expect(html).not.toContain("已保存修改");
  });
  it("labels a first no-op as already consistent", async () => {
    setLocale("zh-CN"); const html = await render(ShortcutMutationResult, { details: { ...mutation(), changed: false, changes: [] } }); expect(html).toContain("内容已一致，无需修改"); expect(html).not.toContain("已保存修改");
  });
  it("displays a folder's actual title but does not fabricate an open-preview control", async () => {
    setLocale("zh-CN"); const html = await render(ShortcutMutationResult, { details: { ...mutation(), action: "folder_create", folder: { id: "f_new", name: "常用分析" }, changes: [{ ...change(), action: "folder_create", kind: "folder", id: "f_new", title: "常用分析" }] } }); expect(html).toContain("创建文件夹"); expect(html).toContain("常用分析"); expect(html).not.toContain("查看内容");
  });
  it("distinguishes preview from success and lists exact kept/removed IDs, with no apply button or API", async () => {
    setLocale("zh-CN"); const fetch = vi.fn(); vi.stubGlobal("fetch", fetch); const html = await render(ShortcutMutationResult, { details: preview() });
    expect(html).toContain('data-saved="false"'); expect(html).toContain("仅预览，尚未修改快捷方式"); expect(html).toContain("1 组重复项，可移除 1 项");
    for (const token of ["合成数据库", "保留", "sh_keep", "移除", "sh_remove"]) expect(html).toContain(token);
    expect(html).not.toContain("已保存修改"); expect(html).not.toContain("<button"); expect(fetch).not.toHaveBeenCalled();
    const none = await render(ShortcutMutationResult, { details: { ...preview(), plan: { id: "sm_empty", revision: 4, groups: [], removals: 0 } } }); expect(none).toContain("未发现完全相同的重复快捷方式"); expect(none).not.toContain('data-testid="shortcut-merge-plan"');
  });
  it("escapes reference titles and handles English labels", async () => {
    setLocale("en"); const value = preview(); value.plan!.groups[0].title = '<img src=x onerror="alert(1)">'; const html = await render(ShortcutMutationResult, { details: value });
    expect(html).toContain("Preview only; shortcuts have not changed"); expect(html).toContain("&lt;img"); expect(html).not.toContain("<img");
  });
});

describe("host result routing", () => {
  it("routes preview and valid operations but keeps malformed, wrong-tool and failed receipts in their fallback", async () => {
    setLocale("zh-CN");
    const shown = await render(ToolCall, { call: call("shortcut_move", mutation()), runActive: false }); expect(shown).toContain('data-testid="shortcut-management-result"');
    const planned = await render(ToolCall, { call: call("shortcut_merge", preview()), runActive: false }); expect(planned).toContain("仅预览，尚未修改快捷方式");
    for (const c of [call("shortcut_delete", mutation()), call("shortcut_move", { ...mutation(), revision: -1 }), call("shortcut_move", mutation(), { is_error: true })]) {
      const html = await render(ToolCall, { call: c, runActive: false }); expect(html).not.toContain('data-testid="shortcut-management-result"'); expect(html).toContain("fallback host receipt");
    }
  });
  it("preserves create/update ordered snapshot preview and rejects a delete masquerading as a card", async () => {
    setLocale("zh-CN"); const shortcut = { id: "sh_one", title: "有序资料", content: "开始嵌套结束", parts: [{ type: "text", text: "开始" }, { type: "shortcut", shortcut_id: "sh_child", title: "子项", text: "嵌套" }, { type: "text", text: "结束" }] };
    const create: ShortcutDetails = { type: "shortcut", action: "create", saved: true, agent_id: "ag_test", request_id: "create_once", revision: 1, shortcut: shortcut as ShortcutDetails["shortcut"] };
    for (const details of [create, { ...mutation(), action: "update", shortcut, changes: [{ ...change(), action: "update" }] }]) {
      const html = await render(ToolCall, { call: call(`shortcut_${details.action}` as TimelineCall["name"], details as ShortcutDetails), runActive: false }); expect(html).toContain('data-testid="shortcut-created-result"'); expect(html).toContain("有序资料");
    }
    const wrong = await render(ToolCall, { call: call("shortcut_delete", create), runActive: false }); expect(wrong).not.toContain('data-testid="shortcut-created-result"'); expect(wrong).toContain("fallback host receipt");
  });
});
