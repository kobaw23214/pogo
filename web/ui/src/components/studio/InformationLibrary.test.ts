import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { webcrypto } from "node:crypto";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import { InformationAPIError, type InformationEntry, type InformationTree } from "@/api/information";
import type { ShortcutSnapshot } from "@/api/shortcuts";
const host = vi.hoisted(() => ({ tree: vi.fn(), entry: vi.fn(), update: vi.fn(), create: vi.fn(), skills: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), provide: vi.fn(), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: { value: "zh-CN" } }) }));
vi.mock("element-plus", () => ({ ElMessage: { success: vi.fn() } }));
vi.mock("@/api/information", async original => ({ ...await original<typeof import("@/api/information")>(), informationApi: { tree: host.tree, entry: host.entry, update: host.update, create: host.create } }));
vi.mock("@/api/client", async original => { const actual = await original<typeof import("@/api/client")>(); return { ...actual, api: { ...actual.api, skills: host.skills } }; });
vi.mock("@/agent/studioNavigation", () => ({ registerStudioNavigationGuard: () => vi.fn() }));
vi.mock("@/components/ShortcutEditorDialog.vue", () => ({ default: {} }));
vi.mock("@/components/ShortcutDetails.vue", () => ({ default: {} }));
vi.mock("@/components/InformationEntryDialog.vue", () => ({ default: {} }));
vi.mock("@/components/ConfirmDialog.vue", () => ({ default: {} }));
import InformationLibrary from "./InformationLibrary.vue";
interface Controller {
  tree: Ref<InformationTree | null>; folder: Ref<string>; flat: Ref<boolean>; query: Ref<string>; selected: Ref<string>; expanded: Ref<string[]>; previewOpen: Ref<boolean>; previewTarget: Ref<InformationEntry | null>; full: Ref<InformationEntry | null>; detailError: Ref<string>; detailLoading: Ref<boolean>;
  adopt(tree: InformationTree): void; selectItem(entry: InformationEntry): void; preview(entry: InformationEntry): void; reference(): void; loadDetail(): Promise<void>;
  busy: Ref<boolean>; loading: Ref<boolean>; moveDialog: Ref<boolean>; moveParent: Ref<string>; actionError: Ref<string>; moveCanSubmit: Ref<boolean>; confirm: Ref<{ ask: (options: unknown) => Promise<boolean> } | null>;
  askMove(entry: InformationEntry): void; move(): Promise<void>; closeMove(): Promise<void>; confirmLeave(): Promise<boolean>;
  editOpen: Ref<boolean>; editItem: Ref<ShortcutSnapshot>; openEditor(entry?: InformationEntry): Promise<void>; saveShortcut(item: ShortcutSnapshot): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const item = (id: string, kind: InformationEntry["kind"] = "file", parent_id = "", version = 1): InformationEntry => ({ id, parent_id, name: id + (kind === "file" ? ".py" : ""), kind, version, created_at: "2026-10-18T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" });
const folder = item("folder", "folder"), child = item("child", "folder", "folder"), python = item("python", "file", "", 7), second = item("second");
const tree = (scope_id = "scope-a"): InformationTree => ({ scope_id, revision: 1, can_write: true, entries: [folder, child, python, second] });
async function flush() { for (let i = 0; i < 6; i++) await Promise.resolve(); await nextTick(); }
function mount(picker = false) {
  const props = reactive({ picker, filesOnly: false, disabled: false, active: false, agents: [] }); const emit = vi.fn(); const scope = effectScope(); scopes.push(scope);
  const controller = scope.run(() => (InformationLibrary as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  controller.adopt(tree()); return { c: controller, props, emit };
}
beforeEach(() => { vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() }); vi.stubGlobal("crypto", { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) }); vi.stubGlobal("localStorage", { getItem: vi.fn(() => null), setItem: vi.fn() }); host.tree.mockReset(); host.entry.mockReset().mockImplementation(async (id: string, version: number) => ({ ...tree().entries.find(entry => entry.id === id), version })); host.update.mockReset().mockResolvedValue({ ...tree(), revision: 2 }); host.create.mockReset().mockResolvedValue({ ...tree(), revision: 2 }); host.skills.mockReset().mockResolvedValue({ skills: [] }); });
afterEach(() => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });
describe("Information library preview and selection", () => {
  it("opens and saves a new shortcut on ordinary HTTP while preserving its folder and rich content", async () => {
    expect(globalThis.crypto).not.toHaveProperty("randomUUID");
    const { c } = mount(); c.folder.value = folder.id; await c.openEditor();
    expect(c.editOpen.value).toBe(true); expect(c.busy.value).toBe(false);
    expect(c.editItem.value.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    const saved: ShortcutSnapshot = { ...c.editItem.value, title: "HTTP 资料", content: "先查询再整理", parts: [{ type: "text", text: "先 " }, { type: "element", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "查询"\n</page-element>' }, { type: "skill", name: "office" }, { type: "text", text: " 再整理" }] };
    await c.saveShortcut(saved);
    expect(host.create).toHaveBeenCalledExactlyOnceWith({ revision: 1, parent_id: folder.id, kind: "shortcut", name: saved.title, shortcut: saved });
    expect(c.tree.value?.revision).toBe(2); expect(host.update).not.toHaveBeenCalled();
  });
  it("opens Manager file previews on demand without fetching a hidden permanent detail pane", async () => {
    const { c } = mount(); c.selectItem(python); await flush();
    expect(c.selected.value).toBe(python.id); expect(c.previewOpen.value).toBe(true); expect(c.previewTarget.value).toMatchObject({ id: python.id, version: 7 }); expect(host.entry).not.toHaveBeenCalled();
  });
  it("opens folders directly, clears search and flattening, and expands their ancestor path", () => {
    const { c } = mount(); c.flat.value = true; c.query.value = "old search"; c.selected.value = python.id; c.selectItem(child);
    expect(c.folder.value).toBe(child.id); expect(c.selected.value).toBe(""); expect(c.flat.value).toBe(false); expect(c.query.value).toBe(""); expect(c.expanded.value).toEqual([folder.id, child.id]); expect(c.previewOpen.value).toBe(false);
  });
  it("keeps picker selection separate from preview, resolves the precise version, and emits a detached snapshot", async () => {
    const { c, emit } = mount(true); c.selectItem(python); await flush();
    expect(c.previewOpen.value).toBe(false); expect(host.entry).toHaveBeenCalledWith(python.id, 7, expect.any(AbortSignal)); c.preview(python); expect(c.previewOpen.value).toBe(true); c.reference();
    expect(emit).toHaveBeenCalledWith("select", python); expect(emit.mock.calls[0][1]).not.toBe(c.full.value); expect(c.selected.value).toBe(python.id);
  });
  it("does not emit a stale selection before its new detail request completes", async () => {
    const { c, emit } = mount(true); c.selectItem(python); await flush(); c.selectItem(second); c.reference(); expect(emit).not.toHaveBeenCalled(); await flush(); c.reference(); expect(emit).toHaveBeenCalledWith("select", second);
  });
  it("disables reference, preview and selection changes while the parent resolves an item", async () => {
    const { c, props, emit } = mount(true); c.selectItem(python); await flush(); props.disabled = true; c.reference(); c.selectItem(second); c.preview(second); expect(emit).not.toHaveBeenCalled(); expect(c.selected.value).toBe(python.id); expect(c.previewOpen.value).toBe(false);
  });
  it("closes preview and rejects an old workspace response after the library identity changes", async () => {
    let resolve!: (entry: InformationEntry) => void; host.entry.mockReturnValueOnce(new Promise<InformationEntry>(done => { resolve = done; }));
    const { c, emit } = mount(true); c.selectItem(python); c.preview(python); await flush(); expect(c.detailLoading.value).toBe(true); c.adopt({ ...tree("scope-b"), entries: [] }); await flush(); resolve(python); await flush(); c.reference();
    expect(c.previewOpen.value).toBe(false); expect(c.previewTarget.value).toBeNull(); expect(c.full.value).toBeNull(); expect(emit).not.toHaveBeenCalled();
  });
  it("keeps failed immutable selection reads visible for explicit retry", async () => {
    host.entry.mockRejectedValueOnce(new Error("offline")); const { c, emit } = mount(true); c.selectItem(python); await flush(); c.reference(); expect(c.detailError.value).toBe("information.previewError"); expect(emit).not.toHaveBeenCalled(); await c.loadDetail(); expect(c.full.value?.id).toBe(python.id); expect(c.detailError.value).toBe("");
  });
});
describe("Information library moves use the existing revision and preserve destination choices", () => {
  it("moves to a selected folder through the original CAS update API", async () => {
    const { c } = mount(); c.askMove(python); expect(c.moveParent.value).toBe(""); expect(c.moveCanSubmit.value).toBe(false);
    c.moveParent.value = child.id; expect(c.moveCanSubmit.value).toBe(true); await c.move();
    expect(host.update).toHaveBeenCalledExactlyOnceWith(python.id, { revision: 1, parent_id: child.id }); expect(c.tree.value?.revision).toBe(2); expect(c.moveDialog.value).toBe(false); expect(c.busy.value).toBe(false);
  });
  it("allows a nested item to move to root but refuses same-location, self, descendant and nonexistent destinations", async () => {
    const { c } = mount(); c.askMove(folder);
    for (const id of ["", folder.id, child.id, python.id, "missing"]) { c.moveParent.value = id; await c.move(); expect(c.moveCanSubmit.value).toBe(false); }
    expect(host.update).not.toHaveBeenCalled(); c.askMove(child); c.moveParent.value = ""; await c.move(); expect(host.update).toHaveBeenCalledWith(child.id, { revision: 1, parent_id: "" });
  });
  it("preserves the destination on CAS conflict and retries with the actual current revision", async () => {
    const current = { ...tree(), revision: 7 }; host.update.mockRejectedValueOnce(new InformationAPIError("conflict", 409, "INFORMATION_CONFLICT", current));
    const { c } = mount(); c.askMove(python); c.moveParent.value = child.id; await c.move();
    expect(c.moveDialog.value).toBe(true); expect(c.moveParent.value).toBe(child.id); expect(c.actionError.value).toBe("information.conflict"); expect(c.tree.value?.revision).toBe(7);
    await c.move(); expect(host.update).toHaveBeenLastCalledWith(python.id, { revision: 7, parent_id: child.id });
  });
  it("invalidates a destination that became a descendant or disappeared during a conflict without submitting it", async () => {
    const target = item("target", "folder"); const initial = { ...tree(), entries: [...tree().entries, target] };
    const current = { ...initial, revision: 8, entries: initial.entries.map(entry => entry.id === target.id ? { ...entry, parent_id: child.id } : entry) };
    host.update.mockRejectedValueOnce(new InformationAPIError("conflict", 409, "INFORMATION_CONFLICT", current));
    const { c } = mount(); c.adopt(initial); c.askMove(folder); c.moveParent.value = target.id; await c.move(); expect(c.moveParent.value).toBe(target.id); expect(c.moveCanSubmit.value).toBe(false);
    await c.move(); expect(host.update).toHaveBeenCalledOnce(); c.adopt({ ...current, entries: current.entries.filter(entry => entry.id !== target.id) }); expect(c.moveCanSubmit.value).toBe(false);
  });
  it.each(["picker", "disabled", "readonly", "loading"] as const)("does not open or submit moves in %s state", async state => {
    const { c, props } = mount(state === "picker"); if (state === "disabled") props.disabled = true; if (state === "readonly") c.adopt({ ...tree(), can_write: false }); if (state === "loading") c.loading.value = true;
    c.askMove(python); c.moveParent.value = child.id; await c.move(); expect(c.moveDialog.value).toBe(false); expect(host.update).not.toHaveBeenCalled();
  });
  it.each(["scope", "scope-roundtrip", "permission", "disabled", "unmount"] as const)("ignores a late move after %s changes", async change => {
    let resolve!: (value: InformationTree) => void; host.update.mockReturnValueOnce(new Promise<InformationTree>(done => { resolve = done; }));
    const { c, props } = mount(); c.askMove(python); c.moveParent.value = child.id; const pending = c.move();
    if (change === "scope" || change === "scope-roundtrip") c.adopt(tree("scope-b"));
    if (change === "scope-roundtrip") c.adopt(tree());
    if (change === "permission") { c.adopt({ ...tree(), can_write: false }); c.adopt(tree()); }
    if (change === "disabled") { props.disabled = true; props.disabled = false; }
    if (change === "unmount") for (const cleanup of host.cleanups.splice(0)) cleanup();
    resolve({ ...tree(), revision: 99 }); await pending; expect(c.tree.value?.revision).toBe(1); expect(c.moveDialog.value).toBe(true);
  });
  it("refuses a successful response from a different workspace", async () => {
    host.update.mockResolvedValueOnce({ ...tree("wrong-scope"), revision: 99 }); const { c } = mount(); c.askMove(python); c.moveParent.value = child.id; await c.move();
    expect(c.tree.value?.scope_id).toBe("scope-a"); expect(c.tree.value?.revision).toBe(1); expect(c.actionError.value).toBe("information.scopeChanged"); expect(c.moveDialog.value).toBe(true);
  });
  it("confirms before abandoning a chosen destination and cannot close during submission", async () => {
    const { c } = mount(); const ask = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true); c.confirm.value = { ask };
    c.askMove(python); c.moveParent.value = child.id; await c.closeMove(); expect(c.moveDialog.value).toBe(true); expect(c.moveParent.value).toBe(child.id); expect(ask).toHaveBeenCalledOnce();
    c.busy.value = true; await c.closeMove(); expect(c.moveDialog.value).toBe(true); expect(ask).toHaveBeenCalledOnce(); c.busy.value = false; await c.closeMove(); expect(c.moveDialog.value).toBe(false);
  });
});
