import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getLocale, setLocale } from "@/i18n";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { InformationEntry } from "@/api/information";
import type { OfficeReferenceSnapshot } from "@/api/types";
const host = vi.hoisted(() => ({ tree: vi.fn(), cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/information", () => ({ informationApi: { tree: host.tree } }));
vi.mock("./fileIcons", () => ({ fileIcon: () => "test" }));
import InformationOfficeReferenceDialog from "./InformationOfficeReferenceDialog.vue";
const id = (n: string) => "inf_" + n.repeat(32), sha = "b".repeat(64);
const file = (n: string, name: string): InformationEntry => ({ id: id(n), parent_id: "", name, kind: "file", version: 2, created_at: "now", updated_at: "now", file: { name, mime: "application/octet-stream", size: 200, sha256: sha, asset: `information-assets/${sha}/${name}` } });
const snapshot = (n = "a"): OfficeReferenceSnapshot => ({ path: `information-assets/${sha}/报告.xlsx`, title: "报告.xlsx", editor: "cell", text: "国家\t数量\n中国\t12", sheet: "明细", range: "A1:B2", canvas_version: `disk:${sha}`, dirty: false, captured_at: "2026-10-18T00:00:00Z", information: { entry_id: id(n), version: 2, snapshot_sha256: "c".repeat(64) } });
interface Controller {
  files: Ref<InformationEntry[]>; visibleFiles: Ref<InformationEntry[]>; query: Ref<string>; chosen: Ref<InformationEntry | null>; selected: Ref<OfficeReferenceSnapshot | null>; viewerKey: Ref<string>; viewerError: Ref<string>; filesCollapsed: Ref<boolean>; canInsert: Ref<boolean>;
  choose(entry: InformationEntry): void; acceptSelection(value: OfficeReferenceSnapshot, key: string): void; failed(message: string, key: string): void; retry(): void; insert(): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick(); };
function mount() {
  const props = reactive({ modelValue: true, disabled: false, error: "" }); const emit = vi.fn(), scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (InformationOfficeReferenceDialog as unknown as { setup(p: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
beforeEach(() => { host.tree.mockReset().mockResolvedValue({ entries: [file("a", "报告.xlsx"), file("d", "说明.docx"), file("e", "代码.py"), { ...file("f", "folder"), kind: "folder", file: undefined }] }); vi.stubGlobal("window", { innerWidth: 1440 }); });
afterEach(() => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });
describe("Information Office native read-only selection workspace", () => {
  it("reorders file names for the selected locale without restarting the selected Office viewer", async () => {
    const previous = getLocale();
    try {
      setLocale("zh-CN");
      host.tree.mockResolvedValueOnce({ entries: [file("a", "東京.xlsx"), file("b", "大阪.xlsx"), file("c", "京都.xlsx")] });
      const { c } = mount(); await flush();
      expect(c.files.value.map(value => value.name)).toEqual(["大阪.xlsx", "東京.xlsx", "京都.xlsx"]);
      c.choose(c.files.value[0]!);
      const binding = c.viewerKey.value;
      setLocale("ja"); await nextTick();
      expect(c.files.value.map(value => value.name)).toEqual(["京都.xlsx", "大阪.xlsx", "東京.xlsx"]);
      expect(c.viewerKey.value).toBe(binding);
      expect(c.chosen.value?.name).toBe("大阪.xlsx");
      expect(host.tree).toHaveBeenCalledTimes(1);
    } finally { setLocale(previous); }
  });

  it("lists only saved Word/Excel files and filters their names", async () => {
    const { c } = mount(); await flush(); expect(c.files.value.map(f => f.name)).toEqual(["报告.xlsx", "说明.docx"]); c.query.value = "说明"; expect(c.visibleFiles.value.map(f => f.name)).toEqual(["说明.docx"]);
  });
  it("stages and inserts the native selection with immutable source metadata without closing", async () => {
    const { c, emit } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); const original = snapshot(); c.acceptSelection(original, c.viewerKey.value); original.text = "later mutation"; expect(c.selected.value?.text).toContain("中国"); expect(c.canInsert.value).toBe(true); c.insert();
    expect(emit).toHaveBeenCalledExactlyOnceWith("select", expect.objectContaining({ editor: "cell", sheet: "明细", range: "A1:B2", canvas_version: `disk:${sha}`, information: { entry_id: id("a"), version: 2, snapshot_sha256: "c".repeat(64) } }));
    expect(emit).not.toHaveBeenCalledWith("update:modelValue", false);
  });
  it("clears the candidate on file switch and rejects late references or errors from the retired viewer", async () => {
    const { c } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); const old = c.viewerKey.value; c.acceptSelection(snapshot(), old); c.choose(file("d", "说明.docx")); expect(c.selected.value).toBeNull(); c.acceptSelection(snapshot(), old); c.failed("late failure", old); expect(c.selected.value).toBeNull(); expect(c.viewerError.value).toBe("");
  });
  it("does not allow references from another version, dirty canvas or mismatched information entry", async () => {
    const { c, emit } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); c.acceptSelection(snapshot("d"), c.viewerKey.value); c.acceptSelection({ ...snapshot(), dirty: true }, c.viewerKey.value); c.acceptSelection({ ...snapshot(), information: { ...snapshot().information!, version: 3 } }, c.viewerKey.value); c.insert(); expect(emit).not.toHaveBeenCalled();
  });
  it("preserves a captured snapshot on connection failure and gives an explicit replacement a new identity", async () => {
    const { c } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); const old = c.viewerKey.value; c.acceptSelection(snapshot(), old); c.failed("service interrupted", old); expect(c.canInsert.value).toBe(true); c.retry(); expect(c.viewerKey.value).not.toBe(old); expect(c.selected.value).toBeNull(); expect(c.viewerError.value).toBe("");
  });
  it("closes the current viewer binding, clears its candidate and rejects a late capture", async () => {
    const { c, props } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); const old = c.viewerKey.value; c.acceptSelection(snapshot(), old); props.modelValue = false; await flush(); expect(c.chosen.value).toBeNull(); expect(c.selected.value).toBeNull(); c.acceptSelection(snapshot(), old); expect(c.selected.value).toBeNull();
  });
  it("collapses the file rail after choosing a document on a narrow screen", async () => { vi.stubGlobal("window", { innerWidth: 390 }); const { c } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); expect(c.filesCollapsed.value).toBe(true); });
  it("blocks capture and insertion while the parent is busy", async () => { const { c, props, emit } = mount(); await flush(); c.choose(file("a", "报告.xlsx")); props.disabled = true; c.acceptSelection(snapshot(), c.viewerKey.value); c.insert(); expect(emit).not.toHaveBeenCalled(); expect(c.canInsert.value).toBe(false); });
});
