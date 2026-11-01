import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { ComposerPart } from "@/agent/composerParts";
import type { ShortcutDraftSource } from "@/agent/shortcutDraftSource";
import type { ShortcutSnapshot } from "@/api/shortcuts";
const host = vi.hoisted(() => ({ source: null as ShortcutDraftSource | null, cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), inject: () => () => host.source, useId: () => "test-workspace", useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/agent/studioNavigation", () => ({ registerStudioNavigationGuard: () => () => {} }));
vi.mock("./InlineComposer.vue", () => ({ default: {} }));
vi.mock("./OrderedMessageContent.vue", () => ({ default: {} }));
vi.mock("./CodeBlock.vue", () => ({ default: {} }));
vi.mock("./ShortcutContent.vue", () => ({ default: {} }));
vi.mock("./ShortcutLanguageSelect.vue", () => ({ default: {} }));
vi.mock("./studio/StarterIconPicker.vue", () => ({ default: {} }));
vi.mock("./OfficeReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ShortcutReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
import ShortcutEditorDialog from "./ShortcutEditorDialog.vue";
const scopes: ReturnType<typeof effectScope>[] = [];
const web = (): ComposerPart => ({ type: "element", id: "web-a", label: "查询", title: "样本系统", text: '<page-element source="样本系统" url="https://example.invalid/">\nelement: button "查询"\n</page-element>', favicon: "data:image/png;base64,AAAA" });
const file = (): Extract<ComposerPart, { type: "file" }> => ({ type: "file", id: "file-a", key: "pending", label: "资料.csv", status: "ready" });
const office = (): ComposerPart => ({ type: "office", id: "office-a", label: "分析.xlsx · A1:B2", office: { path: "uploads/分析.xlsx", title: "分析.xlsx", editor: "cell", text: "地区\t数量\n中国\t10", sheet: "Sheet1", range: "A1:B2", canvas_version: "v1", dirty: true, source_session: "session-a" } });
interface Controller {
  draft: Ref<ShortcutSnapshot>; draftParts: Ref<ComposerPart[]>; saveError: Ref<string>; uploading: Ref<boolean>; sourcesOpen: Ref<boolean>; contentMode: Ref<string>; sourceReferences: Ref<ComposerPart[]>;
  draftEditor: Ref<{ getSelection(): { from: number; to: number }; setSelection(value: { from: number; to: number }): void; insertParts(parts: ComposerPart[]): void } | null>;
  chooseNested(id: string): Promise<void>; insertSourceReference(id: string): Promise<void>; refreshSources(): void;
  saveDraft(): Promise<void>; importSourceDraft(): Promise<void>; confirmLeave(): Promise<boolean>; editorKeydown(event: KeyboardEvent): void;
  leaveConfirm: Ref<{ ask(value: unknown): Promise<boolean> } | null>;
  sourceShell: Ref<{ contains(element: unknown): boolean } | null>; sourceToggle: Ref<{ focus(options: unknown): void } | null>;
  setSourcesOpen(open: boolean): void;
}
function mount(extra: Record<string, unknown> = {}) {
  const props = reactive({ modelValue: true, item: { id: "draft-a", title: "流程", content: "前文 后文" }, existing: true, library: [] as ShortcutSnapshot[], skills: [], sessionId: "session-a", scopeKey: "agent-a", disabled: false, save: vi.fn().mockResolvedValue(undefined), resolveReference: undefined as ((id: string) => Promise<ShortcutSnapshot>) | undefined, ...extra });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (ShortcutEditorDialog as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
beforeEach(() => { host.source = null; vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() }); });
afterEach(() => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });

describe("Fullscreen shortcut reference workspace", () => {
  it("returns focus from a collapsed source panel to its one edge toggle without changing the draft", () => {
    const { c } = mount(); const sourceField = {}, focus = vi.fn();
    vi.stubGlobal("document", { activeElement: sourceField });
    c.sourceShell.value = { contains: element => element === sourceField }; c.sourceToggle.value = { focus };
    const before = JSON.stringify(c.draftParts.value);
    c.setSourcesOpen(false); expect(c.sourcesOpen.value).toBe(false); expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    c.setSourcesOpen(true); expect(c.sourcesOpen.value).toBe(true); expect(focus).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(c.draftParts.value)).toBe(before);
  });
  it("changing display syntax preserves the original raw text, card order and editing mode", () => {
    const { c } = mount(); c.draftParts.value = [{ type: "text", text: "## 标题\n" }, web(), { type: "text", text: "**后文**" }];
    const before = JSON.stringify(c.draftParts.value); c.draft.value.language = "markdown";
    expect(JSON.stringify(c.draftParts.value)).toBe(before); expect(c.contentMode.value).toBe("edit");
  });
  it("keeps a detached source snapshot and refreshes real references without changing content", () => {
    host.source = { sessionId: "session-a", parts: [web(), office()] };
    const { c } = mount(); const initial = JSON.stringify(c.draftParts.value);
    expect(c.sourceReferences.value.map(part => part.type)).toEqual(["element", "office"]);
    host.source.parts.push(file()); expect(c.sourceReferences.value).toHaveLength(2);
    c.refreshSources(); expect(c.sourceReferences.value).toHaveLength(3);
    expect(JSON.stringify(c.draftParts.value)).toBe(initial);
  });
  it("does not expose another conversation's references", () => {
    host.source = { sessionId: "session-b", parts: [web()] };
    const { c } = mount(); expect(c.sourceReferences.value).toEqual([]);
  });
  it("inserts one selected web or Office reference at the retained cursor and preserves snapshot metadata", async () => {
    host.source = { sessionId: "session-a", parts: [web(), office()] };
    const { c } = mount(); const editor = { getSelection: () => ({ from: 4, to: 4 }), setSelection: vi.fn(), insertParts: vi.fn() };
    c.draftEditor.value = editor; c.contentMode.value = "preview";
    await c.insertSourceReference("web-a");
    expect(editor.setSelection).toHaveBeenLastCalledWith({ from: 4, to: 4 });
    expect(editor.insertParts.mock.calls[0][0]).toMatchObject([{ type: "element", favicon: "data:image/png;base64,AAAA" }]);
    expect(editor.insertParts.mock.calls[0][0][0]).not.toBe(host.source.parts[0]);
    expect(c.contentMode.value).toBe("edit");
    await c.insertSourceReference("office-a");
    expect(editor.insertParts.mock.calls[1][0]).toMatchObject([{ type: "office", office: { range: "A1:B2", dirty: true, canvas_version: "v1" } }]);
  });
  it("prepares an unsent source file once and inserts only the chosen file", async () => {
    const prepare = vi.fn().mockResolvedValue([web(), { ...file(), path: "uploads/资料.csv" }]);
    host.source = { sessionId: "session-a", parts: [web(), file()], prepare };
    const { c } = mount(); await c.insertSourceReference("file-a");
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(c.draftParts.value.filter(part => part.type !== "text")).toMatchObject([{ type: "file", path: "uploads/资料.csv" }]);
  });
  it.each(["scope", "session", "reopen", "draft", "disabled"])("discards a late source file selection after %s changes without losing the draft", async change => {
    let finish!: (parts: ComposerPart[]) => void;
    host.source = { sessionId: "session-a", parts: [file()], prepare: () => new Promise(resolve => { finish = resolve; }) };
    const { c, props } = mount(); const pending = c.insertSourceReference("file-a");
    if (change === "scope") props.scopeKey = "agent-b";
    if (change === "session") props.sessionId = "session-b";
    if (change === "reopen") { props.modelValue = false; await nextTick(); props.modelValue = true; await nextTick(); }
    if (change === "draft") c.draftParts.value = [{ type: "text", text: "继续输入的新内容" }];
    if (change === "disabled") props.disabled = true;
    const before = JSON.stringify(c.draftParts.value);
    finish([{ ...file(), path: "uploads/资料.csv" }]); await pending;
    expect(JSON.stringify(c.draftParts.value)).toBe(before); expect(props.save).not.toHaveBeenCalled();
  });
  it("resolves a projected starter through its callback and does not overwrite typing while it loads", async () => {
    let finish!: (value: ShortcutSnapshot) => void;
    const resolveReference = vi.fn(() => new Promise<ShortcutSnapshot>(resolve => { finish = resolve; }));
    const { c } = mount({ referenceChoices: [{ id: "starter-hash", title: "快捷卡片" }], resolveReference });
    const pending = c.chooseNested("starter-hash"); c.draftParts.value = [{ type: "text", text: "稍后添加，先保留这行" }];
    finish({ id: "starter-hash", title: "快捷卡片", content: "内容" }); await pending;
    expect(resolveReference).toHaveBeenCalledWith("starter-hash");
    expect(c.draftParts.value).toEqual([{ type: "text", text: "稍后添加，先保留这行" }]);
    expect(c.saveError.value).toBe("nestedShortcuts.changedDraft");
  });
  it("preserves the complete ordered draft after a save error", async () => {
    const { c, props, emit } = mount({ save: vi.fn().mockRejectedValue(new Error("Version conflict")) });
    c.draftParts.value = [{ type: "text", text: "先 " }, web(), { type: "text", text: " 再 " }, office(), { type: "text", text: " 最后总结" }];
    await c.saveDraft();
    expect(c.saveError.value).toBe("Version conflict"); expect(props.modelValue).toBe(true); expect(emit).not.toHaveBeenCalled();
    expect(c.draftParts.value.map(part => part.type)).toEqual(["text", "element", "text", "office", "text"]);
  });
  it("does not close a reopened editor when the previous save finishes late", async () => {
    let finish!: () => void;
    const save = vi.fn(() => new Promise<void>(resolve => { finish = resolve; }));
    const { c, props, emit } = mount({ save });
    c.draftParts.value = [{ type: "text", text: "旧稿" }];
    const pending = c.saveDraft(); for (let i = 0; i < 6; i++) await Promise.resolve();
    expect(save).toHaveBeenCalledTimes(1);
    props.modelValue = false; await nextTick(); props.modelValue = true; await nextTick();
    c.draftParts.value = [{ type: "text", text: "重新打开后的新稿" }];
    finish(); await pending;
    expect(emit).not.toHaveBeenCalled(); expect(c.draftParts.value).toEqual([{ type: "text", text: "重新打开后的新稿" }]);
  });
  it("reuses the same ordered editor for a local schedule step without saving a shortcut", async () => {
    const callback = vi.fn().mockResolvedValue(undefined);
    const original: ShortcutSnapshot = { id: "schedule_content", title: "计划", content: "", parts: [{ type: "text", text: "比较 " }, { type: "file", path: "uploads/a.png", as_file: false }, { type: "file", path: "uploads/a.png", as_file: true }, { type: "shortcut", shortcut_id: "nested", title: "嵌套", text: "**资料**", language: "markdown" }] };
    const { c, props, emit } = mount({ purpose: "schedule", item: original, save: callback });
    await c.saveDraft();
    expect(callback).toHaveBeenCalledTimes(1); expect(callback.mock.calls[0][0].parts).toEqual(original.parts);
    expect(props.item).toEqual(original); expect(emit).toHaveBeenCalledWith("update:modelValue", false);
  });
  it("protects an already-committed unsaved schedule draft when returning from timing", async () => {
    const { c } = mount({ purpose: "schedule", externalDirty: true }); const ask = vi.fn().mockResolvedValue(false); c.leaveConfirm.value = { ask };
    expect(await c.confirmLeave()).toBe(false); expect(ask).toHaveBeenCalledWith(expect.objectContaining({ title: "scheduleDialog.discardTitle", body: "scheduleDialog.discardBody" }));
  });
  it("requires an explicit discard for a dirty editor; first Escape only collapses sources", async () => {
    const { c } = mount(); const ask = vi.fn().mockResolvedValue(false); c.leaveConfirm.value = { ask };
    c.draftParts.value = [{ type: "text", text: "未保存草稿" }];
    const event = { key: "Escape", preventDefault: vi.fn(), defaultPrevented: false } as unknown as KeyboardEvent;
    c.editorKeydown(event); expect(c.sourcesOpen.value).toBe(false); expect(ask).not.toHaveBeenCalled();
    expect(await c.confirmLeave()).toBe(false); expect(ask).toHaveBeenCalledTimes(1);
    expect(c.draftParts.value).toEqual([{ type: "text", text: "未保存草稿" }]);
  });
});

