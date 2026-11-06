import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { ComposerPart, ComposerReferencePart } from "@/agent/composerParts";
import type { ShortcutSnapshot } from "@/api/shortcuts";

const host = vi.hoisted(() => ({ cleanups: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), inject: () => null, useId: () => "test", useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", async original => { const { ref } = await import("vue"); return { ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }; });
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

interface Controller {
  draft: Ref<ShortcutSnapshot>; draftParts: Ref<ComposerPart[]>; uploading: Ref<boolean>; saveError: Ref<string>;
  draftEditor: Ref<{ getSelection(): { from: number; to: number }; setSelection(value: { from: number; to: number }): void; insertParts(parts: ComposerPart[]): void } | null>;
  addInformation(): Promise<void>;
  webCreateOpen: Ref<boolean>; officeCreateOpen: Ref<boolean>; createReferenceError: Ref<string>;
  openReferenceCreator(kind: "web" | "office"): void;
  acceptCreatedReference(part: ComposerReferencePart, kind: "web" | "office"): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
const file = (): ComposerPart => ({ type: "file", id: "library-file", key: "uploads/source.csv", path: "uploads/source.csv", label: "资料.csv", status: "ready", asFile: true });
const web = (): ComposerReferencePart => ({ type: "element", id: "new-web", label: "运行", text: '<page-element source="系统" url="https://example.invalid/">\nelement: button "运行"\n</page-element>' });
function mount(pickInformation: () => Promise<ComposerPart[] | null>) {
  const props = reactive({ modelValue: true, item: { id: "draft-a", title: "流程", content: "前文 后文" }, existing: true, library: [], skills: [], sessionId: "session-a", scopeKey: "agent-a", disabled: false, pickInformation, save: vi.fn() });
  const scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => (ShortcutEditorDialog as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} }))!;
  return { c, props };
}
beforeEach(() => vi.stubGlobal("window", { addEventListener: vi.fn(), removeEventListener: vi.fn() }));
afterEach(() => { for (const fn of host.cleanups.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });

describe("Shortcut information-library insertion", () => {
  it("inserts a newly authored reference once at the original caret and ignores a cancelled creator", () => {
    const { c } = mount(vi.fn());
    const editor = { getSelection: () => ({ from: 4, to: 4 }), setSelection: vi.fn(), insertParts: vi.fn() };
    const part = web();
    c.draftEditor.value = editor;
    c.openReferenceCreator("web");
    expect(c.webCreateOpen.value).toBe(true);
    c.acceptCreatedReference(part, "web");
    expect(editor.setSelection).toHaveBeenCalledWith({ from: 4, to: 4 });
    expect(editor.insertParts).toHaveBeenCalledTimes(1);
    expect(c.webCreateOpen.value).toBe(false);
    c.openReferenceCreator("web"); c.webCreateOpen.value = false;
    c.acceptCreatedReference(part, "web");
    expect(editor.insertParts).toHaveBeenCalledTimes(1);
  });

  it("preserves the original content and creator on draft changes or insertion failure", () => {
    const { c } = mount(vi.fn());
    c.openReferenceCreator("web");
    c.acceptCreatedReference({ type: "element", id: "bad", label: "bad", text: "bad\u0000" }, "web");
    expect(c.webCreateOpen.value).toBe(true);
    expect(c.createReferenceError.value).toBe("nestedShortcuts.invalid");
    c.draftParts.value = [{ type: "text", text: "不能覆盖的新内容" }];
    c.acceptCreatedReference(web(), "web");
    expect(c.createReferenceError.value).toBe("nestedShortcuts.changedDraft");
    expect(c.draftParts.value).toEqual([{ type: "text", text: "不能覆盖的新内容" }]);
  });

  it("restores the original cursor and inserts a detached selection once", async () => {
    const selected = [file()];
    const { c, props } = mount(vi.fn().mockResolvedValue(selected));
    const editor = { getSelection: () => ({ from: 4, to: 4 }), setSelection: vi.fn(), insertParts: vi.fn() };
    c.draftEditor.value = editor;
    await c.addInformation();
    expect(editor.setSelection).toHaveBeenCalledWith({ from: 4, to: 4 });
    expect(editor.insertParts).toHaveBeenCalledTimes(1);
    expect(editor.insertParts.mock.calls[0][0]).toEqual(selected);
    expect(editor.insertParts.mock.calls[0][0][0]).not.toBe(selected[0]);
    expect(props.save).not.toHaveBeenCalled();
  });
  it("cancel and network failure preserve the complete original draft", async () => {
    const pick = vi.fn().mockResolvedValueOnce(null).mockRejectedValueOnce(new Error("File version unavailable"));
    const { c } = mount(pick); const original = JSON.stringify(c.draftParts.value);
    await c.addInformation(); expect(JSON.stringify(c.draftParts.value)).toBe(original);
    await c.addInformation(); expect(JSON.stringify(c.draftParts.value)).toBe(original);
    expect(c.saveError.value).toBe("File version unavailable"); expect(c.uploading.value).toBe(false);
  });
  it("does not open a second picker while one selection is pending", async () => {
    let finish!: (value: ComposerPart[] | null) => void;
    const pick = vi.fn(() => new Promise<ComposerPart[] | null>(resolve => { finish = resolve; }));
    const { c } = mount(pick); const pending = c.addInformation();
    await c.addInformation(); expect(pick).toHaveBeenCalledTimes(1);
    finish(null); await pending; expect(c.uploading.value).toBe(false);
  });
  it.each(["scope", "session", "reopen", "draft", "unmount", "disabled"])("rejects a late selection after %s changes", async change => {
    let finish!: (value: ComposerPart[]) => void;
    const { c, props } = mount(() => new Promise(resolve => { finish = resolve; }));
    const pending = c.addInformation();
    if (change === "scope") props.scopeKey = "agent-b";
    if (change === "session") props.sessionId = "session-b";
    if (change === "reopen") { props.modelValue = false; await nextTick(); props.modelValue = true; await nextTick(); }
    if (change === "draft") c.draftParts.value = [{ type: "text", text: "新输入不能被覆盖" }];
    if (change === "disabled") props.disabled = true;
    if (change === "unmount") for (const cleanup of host.cleanups.splice(0)) cleanup();
    const before = JSON.stringify(c.draftParts.value); finish([file()]); await pending;
    expect(JSON.stringify(c.draftParts.value)).toBe(before);
    expect(props.save).not.toHaveBeenCalled();
  });
  it("rejects invalid file data and shortcut cycles before insertion", async () => {
    const pick = vi.fn().mockResolvedValueOnce([{ ...file(), path: "" }]).mockResolvedValueOnce([{ type: "shortcut", id: "self", shortcutId: "draft-a", title: "自己", label: "自己", text: "self" }]);
    const { c } = mount(pick); const before = JSON.stringify(c.draftParts.value);
    await c.addInformation(); expect(c.saveError.value).toBe("nestedShortcuts.fileNotReady");
    await c.addInformation(); expect(c.saveError.value).toBe("nestedShortcuts.cycle");
    expect(JSON.stringify(c.draftParts.value)).toBe(before);
  });
});
