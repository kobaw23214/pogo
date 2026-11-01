import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { webcrypto } from "node:crypto";
import type { ComposerPart } from "@/agent/composerParts";
import { effectScope, nextTick, reactive, watch, type Ref } from "vue";
import { ShortcutsAPIError, type ShortcutSnapshot, type ShortcutsView } from "@/api/shortcuts";
import { shortcutDraftSourceKey, type ShortcutDraftSource } from "@/agent/shortcutDraftSource";

const host = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), assets: vi.fn(), resolveStarter: vi.fn(), success: vi.fn(), skills: vi.fn(), sourceKey: null as unknown, readSource: null as (() => unknown) | null, cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => {
  const actual = await original<typeof import("vue")>();
  return { ...actual, inject: (key: unknown, fallback?: unknown) => key === host.sourceKey ? host.readSource : actual.inject(key as Parameters<typeof actual.inject>[0], fallback), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn), useSSRContext: () => ({ modules: new Set() }) };
});
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string, data?: unknown) => key + (data ? JSON.stringify(data) : "") }) }));
vi.mock("element-plus", () => ({ ElMessage: { success: host.success } }));
vi.mock("@/api/shortcuts", async original => ({ ...await original<typeof import("@/api/shortcuts")>(), shortcutsApi: { list: host.list, save: host.save }, shortcutAssetsApi: { save: host.assets }, resolveStarterShortcut: host.resolveStarter }));
vi.mock("@/api/client", async original => {
  const actual = await original<typeof import("@/api/client")>();
  return { ...actual, api: { ...actual.api, skills: host.skills } };
});
vi.mock("./studio/StarterIconPicker.vue", () => ({ default: {} }));
import ShortcutsPanel from "./ShortcutsPanel.vue";
import ShortcutEditorDialog from "./ShortcutEditorDialog.vue";
interface Controller {
  draftParts: Ref<ComposerPart[]>;
  uploading: Ref<boolean>;
  view: Ref<ShortcutsView | null>; loading: Ref<boolean>; editing: Ref<boolean>; editingExisting: Ref<boolean>; saving: Ref<boolean>; saveError: Ref<string>; deleteError: Ref<string>; draft: Ref<ShortcutSnapshot>; contentMode: Ref<"edit" | "preview">; draftLanguage: Ref<string>; query: Ref<string>; visible: Ref<{ item: ShortcutSnapshot; titleSegments: { text: string; match: boolean }[]; previewSegments: { text: string; match: boolean }[] }[]>; deleteTarget: Ref<ShortcutSnapshot | null>; formError: Ref<string>;
  resolveReference(id: string): Promise<ShortcutSnapshot>; openAdd(): void; openEdit(item: ShortcutSnapshot): void; insert(item: ShortcutSnapshot): void; importSourceDraft(): Promise<void>; saveDraft(): Promise<void>; askDelete(item: ShortcutSnapshot): void; remove(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const item = (id = "one", content = "原始资料"): ShortcutSnapshot => ({ id, title: `标题 ${id}`, content, icon: "" });
const view = (agent_id = "agent-a", revision = 3, shortcuts = [item()]): ShortcutsView => ({ scope_id: "scope-" + agent_id, agent_id, revision, shortcuts });
async function flush() { for (let i = 0; i < 6; i++) await Promise.resolve(); await nextTick(); }
function mount(agentId = "agent-a", sessionId = "") {
  const props = reactive({ agentId, sessionId, disabled: false }); const emit = vi.fn();
  const scope = effectScope(); scopes.push(scope);
  const panel = scope.run(() => (ShortcutsPanel as unknown as { setup(props: unknown, ctx: unknown): Controller & { prepareParts(parts: unknown): Promise<unknown>; uploadFile: Ref<unknown> } }).setup(props, { emit, expose() {} }))!;
  let editor: Controller | undefined;
  function openEditor(open: () => void) {
    open();
    const editorProps = reactive({ modelValue: true, item: panel.draft.value, existing: panel.editingExisting.value, library: panel.view.value?.shortcuts || [], skills: [], sessionId: props.sessionId, scopeKey: props.agentId, disabled: false, save: (item: ShortcutSnapshot) => (panel.saveDraft as unknown as (item: ShortcutSnapshot) => Promise<void>)(item), prepareParts: (parts: unknown) => panel.prepareParts(parts), uploadFile: undefined });
    scope.run(() => watch(() => [props.sessionId, props.agentId], () => { editorProps.sessionId = props.sessionId; editorProps.scopeKey = props.agentId; }, { flush: "sync" }));
    editor = scope.run(() => (ShortcutEditorDialog as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(editorProps, { emit(name: string, value: boolean) { if (name === "update:modelValue") { editorProps.modelValue = value; panel.editing.value = value; } }, expose() {} }))!;
  }
  // Exercise the same parent + shared-editor responsibilities as the rendered
  // component. Draft/CAS assertions stay intact after extracting the form.
  const editorFields = new Set(["draftParts", "uploading", "saveError", "draft", "contentMode", "draftLanguage", "formError", "importSourceDraft", "saveDraft"]);
  const controller = new Proxy(panel, { get(target, key) {
    if (key === "openAdd") return () => openEditor(() => panel.openAdd());
    if (key === "openEdit") return (item: ShortcutSnapshot) => openEditor(() => panel.openEdit(item));
    if (typeof key === "string" && editorFields.has(key) && editor) return Reflect.get(editor, key);
    return Reflect.get(target, key);
  } });
  return { props, controller, emit };
}
beforeEach(() => { vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() }); vi.stubGlobal("crypto", { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) }); host.list.mockReset().mockImplementation(async agent => view(agent)); host.save.mockReset().mockImplementation(async (_agent, value) => ({ ...value, revision: value.revision + 1 })); host.assets.mockReset().mockImplementation(async (_session, value) => ({ parts: value.parts })); host.success.mockClear(); host.skills.mockReset().mockResolvedValue({ skills: [] }); host.sourceKey = shortcutDraftSourceKey; host.readSource = null; });
afterEach(() => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });

describe("Reference shortcut panel", () => {
  it("opens and saves the real shared editor on ordinary HTTP without dropping mixed references", async () => {
    expect(globalThis.crypto).not.toHaveProperty("randomUUID");
    const { controller: c } = mount("agent-a", "session-a"); await flush(); c.openAdd();
    const id = c.draft.value.id;
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(c.editing.value).toBe(true);
    c.draft.value.title = "HTTP 流程";
    c.draftParts.value = [
      { type: "text", text: "先点击 " },
      { type: "element", id: "web", label: "查询", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "查询"\n</page-element>' },
      { type: "text", text: " 再读取 " },
      { type: "file", id: "file", key: "file", path: "uploads/数据.csv", label: "数据.csv", status: "ready", asFile: true },
      { type: "skill", id: "skill", name: "office", label: "office" },
      { type: "shortcut", id: "nested", shortcutId: "child", title: "子资料", label: "子资料", text: "保留完整子资料" },
      { type: "office", id: "office", label: "表格.xlsx · A1", office: { path: "uploads/表格.xlsx", title: "表格.xlsx", editor: "cell", sheet: "Sheet1", range: "A1", text: "保留选区", canvas_version: "v1", dirty: false, source_session: "session-a" } },
      { type: "text", text: " 然后总结。" },
    ];
    await c.saveDraft();
    expect(c.saveError.value).toBe(""); expect(c.editing.value).toBe(false);
    const saved = c.view.value!.shortcuts.find(value => value.id === id)!;
    expect(saved.parts!.map(part => part.type)).toEqual(["text", "element", "text", "file", "skill", "shortcut", "office", "text"]);
    expect(saved.parts![1]).toMatchObject({ type: "element", text: expect.stringContaining('button "查询"') });
    expect(saved.parts![3]).toEqual({ type: "file", path: "uploads/数据.csv", as_file: true });
    expect(saved.parts![5]).toMatchObject({ type: "shortcut", text: "保留完整子资料" });
    expect(saved.parts![6]).toMatchObject({ type: "office", office: { text: "保留选区", range: "A1" } });
    expect(host.assets).toHaveBeenCalledExactlyOnceWith("session-a", expect.objectContaining({ input_layout: "inline" }));
    expect(c.view.value!.shortcuts[0]).toEqual(item());
  });
  it("shows and searches all homepage items without allowing the private editor to mutate their source", async () => {
    const source = { ...item("starter_source", "首页流程关键词"), source: "starter" as const };
    host.list.mockResolvedValueOnce({ ...view(), starter_shortcuts: [source] });
    const { controller: c, emit } = mount(); await flush();
    expect(c.visible.value.map(entry => entry.item.id)).toEqual(["one", "starter_source"]);
    c.query.value = "关键词";
    expect(c.visible.value).toHaveLength(1);
    expect(c.visible.value[0].previewSegments.some(segment => segment.match && segment.text === "关键词")).toBe(true);
    c.askDelete(source); expect(c.deleteTarget.value).toBeNull();
    c.insert(source);
    expect(emit).toHaveBeenCalledWith("insert", expect.objectContaining({ id: source.id, source: "starter", content: source.content }));
    expect(host.save).not.toHaveBeenCalled();
  });
  it("resolves a homepage nested selection through the exact source endpoint and rejects late Agent results", async () => {
    const source = { ...item("starter_source"), source: "starter" as const };
    host.list.mockResolvedValueOnce({ ...view(), starter_shortcuts: [source] });
    const { controller: c, props } = mount("agent-a", "session-a"); await flush(); c.openAdd();
    let finish!: (value: ShortcutSnapshot) => void;
    host.resolveStarter.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
    const pending = c.resolveReference(source.id);
    expect(host.resolveStarter).toHaveBeenCalledWith("session-a", expect.objectContaining({ id: source.id }));
    props.agentId = "agent-b"; await flush(); finish({ ...source, parts: [{ type: "text", text: "frozen" }] });
    await expect(pending).rejects.toThrow("shortcuts.scopeChanged");
  });
  it("switches edit/view without losing the draft and persists syntax only as snapshot metadata", async () => {
    const { controller: c, emit } = mount(); await flush(); c.openAdd();
    expect(c.contentMode.value).toBe("edit"); expect(c.draftLanguage.value).toBe("text");
    c.draft.value.title = "连接"; c.draftParts.value = [{ type: "text", text: "SELECT * FROM records;\n" }];
    c.draftLanguage.value = "sql"; c.contentMode.value = "preview"; c.contentMode.value = "edit";
    expect(c.draftParts.value).toEqual([{ type: "text", text: "SELECT * FROM records;\n" }]);
    await c.saveDraft(); const saved = c.view.value!.shortcuts[1];
    expect(saved.language).toBe("sql"); c.insert(saved);
    const snapshot = emit.mock.calls[0][1] as ShortcutSnapshot;
    saved.language = "json"; saved.content = "changed";
    expect(snapshot).toMatchObject({ language: "sql", content: "SELECT * FROM records;\n" });
    c.openEdit(item()); expect(c.contentMode.value).toBe("edit"); expect(c.draftLanguage.value).toBe("text");
  });
  it("prepares local files once and inserts the complete source draft in its original order", async () => {
    let finish!: (parts: ComposerPart[]) => void;
    const prepare = vi.fn<() => Promise<ComposerPart[]>>().mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    const parts: ComposerPart[] = [
      { type: "text", text: "先查看 " },
      { type: "element", id: "page", label: "查询", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "查询"\n</page-element>', favicon: "data:image/png;base64,AAAA" },
      { type: "text", text: " 再读取 " },
      { type: "file", id: "local-file", key: "local-upload", label: "未发送.csv", status: "ready", asFile: true },
      { type: "text", text: " 然后 " },
      { type: "skill", id: "skill", name: "office", label: "office" },
      { type: "shortcut", id: "nested", shortcutId: "child", title: "子资料", label: "子资料", text: "子资料正文", language: "bash" },
      { type: "office", id: "selection", label: "报告.xlsx · A1:B2", office: { path: "uploads/报告.xlsx", title: "报告.xlsx", editor: "cell", text: "地区\t数量\n中国\t10", sheet: "Sheet1", range: "A1:B2", canvas_version: "v1", dirty: true, source_session: "session-a" } },
      { type: "text", text: " 最后总结。" },
    ];
    const source: ShortcutDraftSource = { sessionId: "session-a", parts, prepare };
    host.readSource = () => source;
    const { controller: c, emit } = mount("agent-a", "session-a"); await flush(); c.openAdd();
    c.draftParts.value = [{ type: "text", text: "原编辑内容\n" }];
    const original = JSON.parse(JSON.stringify(parts));
    const importing = c.importSourceDraft();
    expect(c.uploading.value).toBe(true);
    expect(c.draftParts.value).toEqual([{ type: "text", text: "原编辑内容\n" }]);
    expect(prepare).toHaveBeenCalledTimes(1);
    await c.importSourceDraft();
    expect(prepare).toHaveBeenCalledTimes(1);
    const ready = parts.map(part => part.type === "file" ? { ...part, path: "uploads/未发送.csv", status: "ready" as const } : part);
    finish(ready); await importing;
    expect(c.uploading.value).toBe(false);
    const references = c.draftParts.value.filter(part => part.type !== "text");
    expect(references.map(part => part.type)).toEqual(["element", "file", "skill", "shortcut", "office"]);
    expect(c.draftParts.value.filter(part => part.type === "text").map(part => part.text).join("")).toBe("原编辑内容\n先查看  再读取  然后  最后总结。");
    expect(references[0]).toMatchObject({ favicon: "data:image/png;base64,AAAA" });
    expect(references[1]).toMatchObject({ label: "未发送.csv", path: "uploads/未发送.csv", asFile: true });
    expect(references[3]).toMatchObject({ shortcutId: "child", text: "子资料正文", language: "bash" });
    expect(references[4]).toMatchObject({ office: { dirty: true, text: "地区\t数量\n中国\t10", source_session: "session-a" } });
    expect(parts).toEqual(original);
    expect(host.save).not.toHaveBeenCalled();
    expect(emit).not.toHaveBeenCalled();
  });
  it("keeps the original editor content after source file preparation fails", async () => {
    const prepare = vi.fn<() => Promise<ComposerPart[]>>().mockRejectedValue(new Error("upload interrupted"));
    host.readSource = () => ({ sessionId: "session-a", parts: [{ type: "file", id: "local-file", key: "local", label: "未发送.xlsx", status: "ready" }], prepare } satisfies ShortcutDraftSource);
    const { controller: c } = mount("agent-a", "session-a"); await flush(); c.openAdd();
    const existing: ComposerPart[] = [{ type: "text", text: "不能丢失的编辑内容" }, { type: "skill", id: "old-skill", name: "office", label: "office" }];
    c.draftParts.value = existing;
    await c.importSourceDraft();
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(c.draftParts.value).toEqual(existing);
    expect(c.saveError.value).toContain("upload interrupted");
    expect(c.uploading.value).toBe(false);
    expect(c.editing.value).toBe(true);
    expect(host.save).not.toHaveBeenCalled();
  });
  it("rejects a late prepared source after the current conversation changes", async () => {
    let finish!: (parts: ComposerPart[]) => void;
    const prepare = vi.fn<() => Promise<ComposerPart[]>>().mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    host.readSource = () => ({ sessionId: "session-a", parts: [{ type: "file", id: "local-file", key: "local", label: "未发送.csv", status: "ready" }], prepare } satisfies ShortcutDraftSource);
    const { controller: c, props } = mount("agent-a", "session-a"); await flush(); c.openAdd();
    c.draftParts.value = [{ type: "text", text: "保留原编辑内容" }];
    const importing = c.importSourceDraft();
    expect(prepare).toHaveBeenCalledTimes(1);
    props.sessionId = "session-b"; await flush();
    finish([{ type: "file", id: "local-file", key: "local", label: "未发送.csv", path: "uploads/未发送.csv", status: "ready" }]);
    await importing;
    expect(c.draftParts.value).toEqual([{ type: "text", text: "保留原编辑内容" }]);
    expect(c.uploading.value).toBe(false);
    expect(host.save).not.toHaveBeenCalled();
  });
  it("shows and marks content hits after the initial summary, with clear/no-result consistency", async () => {
    const { controller: c } = mount(); await flush();
    c.view.value = view("agent-a", 1, [item("late", "前言".repeat(200) + " <script>检索[+]</script> 后文")]);
    c.query.value = "检索[+]";
    expect(c.visible.value).toHaveLength(1);
    expect(c.visible.value[0].previewSegments.some(part => part.match && part.text === "检索[+]")).toBe(true);
    c.query.value = "does not exist"; expect(c.visible.value).toHaveLength(0);
    c.query.value = ""; expect(c.visible.value).toHaveLength(1); expect(c.visible.value[0].previewSegments.every(part => !part.match)).toBe(true);
  });
  it("searches frozen nested and Office bodies while keeping unfiltered summaries compact", async () => {
    const { controller: c } = mount(); await flush();
    c.view.value = view("agent-a", 1, [{ id: "outer", title: "外层", content: "〔内层资料〕〔选区〕", parts: [{ type: "shortcut", shortcut_id: "inner", title: "内层", text: "深层独有检索", language: "text" }, { type: "office", office: { path: "uploads/a.xlsx", title: "a.xlsx", editor: "cell", text: "Office独有数据", canvas_version: "v1" } }] }]);
    expect(c.visible.value[0].previewSegments.map(part => part.text).join("")).toBe("〔内层资料〕〔选区〕");
    for (const query of ["深层独有", "Office独有"]) { c.query.value = query; expect(c.visible.value).toHaveLength(1); expect(c.visible.value[0].previewSegments.some(part => part.match && part.text === query)).toBe(true); }
  });
  it("inserts a detached saved snapshot without saving or sending anything", async () => {
    const { controller: c, emit } = mount(); await flush();
    c.insert(c.view.value!.shortcuts[0]); expect(emit).toHaveBeenCalledWith("insert", item()); expect(host.save).not.toHaveBeenCalled();
    const inserted = emit.mock.calls[0][1] as ShortcutSnapshot; inserted.content = "changed externally";
    expect(c.view.value!.shortcuts[0].content).toBe("原始资料");
    c.openAdd(); expect(c.draft.value.icon).toBe(""); expect(c.draft.value.content).toBe("");
  });
  it("saves title, complete content and optional empty icon with compare-and-swap", async () => {
    const { controller: c } = mount(); await flush(); c.openAdd();
    c.draft.value.title = "  固定流程  "; c.draftParts.value = [{ type: "text", text: "第一步\n/skill:看作资料，不直接运行\n第三步" }];
    const id = c.draft.value.id; await c.saveDraft();
    expect(host.save).toHaveBeenCalledWith("agent-a", expect.objectContaining({ scope_id: "scope-agent-a", revision: 3, shortcuts: [item(), { id, title: "固定流程", content: "第一步\n/skill:看作资料，不直接运行\n第三步", icon: "" }] }));
    expect(c.editing.value).toBe(false); expect(c.view.value!.revision).toBe(4);
  });
  it("preserves unsaved edits on a conflict and only retries after another explicit save", async () => {
    const { controller: c } = mount(); await flush(); c.openEdit(item()); c.draftParts.value = [{ type: "text", text: "用户的新内容" }];
    const current = view("agent-a", 5, [item("one", "远端变化"), item("remote")]);
    host.save.mockRejectedValueOnce(new ShortcutsAPIError("Changed", 409, "SHORTCUT_REVISION_CONFLICT", current));
    await c.saveDraft(); expect(host.save).toHaveBeenCalledTimes(1); expect(c.editing.value).toBe(true); expect(c.draftParts.value).toEqual([{ type: "text", text: "用户的新内容" }]); expect(c.saveError.value).toBe("shortcuts.conflict");
    await c.saveDraft(); expect(host.save).toHaveBeenCalledTimes(2);
    expect(host.save.mock.calls[1][1]).toMatchObject({ revision: 5, shortcuts: [{ ...item(), content: "用户的新内容" }, item("remote")] });
  });
  it("keeps a stable new ID after an ambiguous response and conflict, avoiding duplicate cards", async () => {
    const { controller: c } = mount(); await flush(); c.openAdd(); c.draft.value.title = "新增"; c.draftParts.value = [{ type: "text", text: "保留" }];
    const created = { ...c.draft.value, content: "保留" };
    host.save.mockRejectedValueOnce(new Error("network lost")); await c.saveDraft();
    host.save.mockRejectedValueOnce(new ShortcutsAPIError("Changed", 409, "SHORTCUT_REVISION_CONFLICT", view("agent-a", 4, [item(), created]))); await c.saveDraft();
    await c.saveDraft(); expect(c.view.value!.shortcuts.filter(value => value.id === created.id)).toHaveLength(1);
  });
  it("ignores stale list responses and prevents an old edit from writing a new Agent", async () => {
    let first!: (value: ShortcutsView) => void;
    host.list.mockImplementationOnce(() => new Promise(resolve => { first = resolve; }));
    const { controller: c, props } = mount(); props.agentId = "agent-b"; await flush();
    first(view("agent-a")); await flush(); expect(c.view.value?.agent_id).toBe("agent-b");
    c.openEdit(item()); props.agentId = "agent-c"; await flush(); await c.saveDraft();
    expect(host.save).not.toHaveBeenCalled(); expect(c.saveError.value).toBe("shortcuts.scopeChanged"); expect(c.draft.value.content).toBe("原始资料");
  });
  it("does not re-create an edited card deleted elsewhere and searches the full content", async () => {
    const { controller: c } = mount(); await flush(); c.openEdit(item()); c.view.value = view("agent-a", 9, [item("different", "埋在正文中的关键词")]);
    await c.saveDraft(); expect(host.save).not.toHaveBeenCalled(); expect(c.saveError.value).toBe("shortcuts.itemDeleted");
    c.query.value = "关键词"; expect(c.visible.value.map(value => value.item.id)).toEqual(["different"]);
  });
  it("deletes by stable identity, preserves concurrent additions and blocks empty/oversize content", async () => {
    const { controller: c } = mount(); await flush(); c.askDelete(item()); c.view.value = view("agent-a", 10, [item("new"), item()]); await c.remove();
    expect(host.save.mock.calls[0][1]).toMatchObject({ revision: 10, shortcuts: [item("new")] });
    c.openAdd(); c.draft.value.title = "资料"; c.draftParts.value = [{ type: "text", text: " " }]; expect(c.formError.value).toBe("shortcuts.contentRequired");
    c.draftParts.value = [{ type: "text", text: "中".repeat(22000) }]; expect(c.formError.value).toBe("shortcuts.contentLong");
    c.draftParts.value = [{ type: "text", text: "保留" }]; c.draft.value.title = "中".repeat(121); expect(c.formError.value).toBe("shortcuts.titleLong");
  });
});
