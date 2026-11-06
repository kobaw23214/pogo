import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, effectScope, h, nextTick, reactive, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { ComposerReferencePart } from "@/agent/composerParts";
import ShortcutSourcesPanel from "./ShortcutSourcesPanel.vue";
import messages from "@/i18n/messages/shortcutEditor";
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => {
  if (key === "nestedShortcuts.fromInformation") return "从信息库添加";
  const value = key.replace(/^shortcutEditor\./, "").split(".").reduce<unknown>((obj, name) => obj && typeof obj === "object" ? (obj as Record<string, unknown>)[name] : undefined, messages.messages["zh-CN"]);
  return typeof value === "string" ? value : key;
} }) }));
vi.mock("./OfficeReference.vue", () => ({ default: {} }));
vi.mock("./ElementCard.vue", () => ({ default: {} }));
const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });
const web: ComposerReferencePart = { type: "element", id: "web-a", label: "查询", title: "样本系统", text: '<page-element source="样本系统" url="https://example.invalid/">\nelement: button "查询"\n</page-element>' };
const office: ComposerReferencePart = { type: "office", id: "office-a", label: "分析.xlsx · A1:B2", office: { path: "uploads/分析.xlsx", title: "分析.xlsx", editor: "cell", text: "地区\t数量\n中国\t10", sheet: "Sheet1", range: "A1:B2", canvas_version: "v1", dirty: true, source_session: "session-a" } };
function props(extra: Record<string, unknown> = {}) { return reactive({ references: [web, office], shortcuts: [{ id: "one", title: "报表流程" }], library: [{ id: "one", title: "报表流程", content: "数据库背景" }], skills: [{ name: "office", description: "Office 编辑", path: "", dir: "", source: "builtin" }], currentId: "draft-a", disabled: false, canPickInformation: true, canRefresh: true, ...extra }); }
interface Controller {
  source: Ref<string>; query: Ref<string>; resultCount: Ref<number>;
  visibleReferences: Ref<ComposerReferencePart[]>; count(source: string): number;
  hasReusableReferences: Ref<boolean>; createReference(): void;
}
function setup(extra: Record<string, unknown> = {}) {
  const values = props(extra), scope = effectScope(), emit = vi.fn(); scopes.push(scope);
  const c = scope.run(() => (ShortcutSourcesPanel as unknown as { setup(props: unknown, context: unknown): Controller }).setup(values, { emit, expose() {} }))!;
  return { c, values, emit };
}
async function render(extra: Record<string, unknown> = {}) {
  return renderToString(createSSRApp({ render: () => h(ShortcutSourcesPanel, props(extra)) }));
}
describe("Shortcut source selection", () => {
  it("starts with all-purpose library references and one clear action, without a private-shortcut tab or stale file-empty state", async () => {
    const html = await render();
    expect(html).toContain("信息库引用"); expect(html).toContain("文件、快捷方式及其内容");
    expect(html).toContain('data-testid="shortcut-add-information"');
    expect(html).not.toContain('data-testid="shortcut-source-shortcut"');
    expect(html).not.toContain("还没有可复用的文件引用");
    expect(html).not.toContain('class="source-search-row"'); expect(html).not.toContain('class="source-empty"');
    expect(html).not.toContain('class="sources-footer"');
    expect((html.match(/data-testid="shortcut-source-/g) || [])).toHaveLength(4);
  });
  it.each([{ disabled: true }, { canPickInformation: false }])("disables library insertion when selection is unavailable: %j", async extra => {
    const html = await render(extra);
    expect(html).toMatch(/<button[^>]*class="source-create-action"[^>]*disabled[^>]*data-testid="shortcut-add-information"/);
  });
  it("keeps existing web and Office snapshots separate and clears search when changing source", async () => {
    const { c } = setup(); expect(c.source.value).toBe("information"); expect(c.visibleReferences.value).toEqual([]);
    c.source.value = "element"; await nextTick();
    expect(c.visibleReferences.value.map(part => part.id)).toEqual(["web-a"]); expect(c.hasReusableReferences.value).toBe(true);
    c.query.value = "不存在"; expect(c.resultCount.value).toBe(0);
    c.source.value = "office"; await nextTick();
    expect(c.query.value).toBe(""); expect(c.visibleReferences.value.map(part => part.id)).toEqual(["office-a"]);
    expect(c.count("element")).toBe(1); expect(c.count("office")).toBe(1);
    c.source.value = "skill"; await nextTick(); expect(c.resultCount.value).toBe(1); expect(c.hasReusableReferences.value).toBe(false);
  });
  it("can create web and Office references without any existing captured references", async () => {
    const { c, emit } = setup({ references: [] });
    c.source.value = "element"; await nextTick(); expect(c.resultCount.value).toBe(0); c.createReference();
    expect(emit).toHaveBeenLastCalledWith("createWeb");
    c.source.value = "office"; await nextTick(); c.createReference(); expect(emit).toHaveBeenLastCalledWith("createOffice");
  });
  it("does not launch creation from unavailable controls or the unrelated source categories", async () => {
    const { c, values, emit } = setup({ disabled: true });
    c.source.value = "element"; await nextTick(); c.createReference(); expect(emit).not.toHaveBeenCalled();
    values.disabled = false; c.source.value = "information"; await nextTick(); c.createReference();
    c.source.value = "skill"; await nextTick(); c.createReference(); expect(emit).not.toHaveBeenCalled();
  });
});
