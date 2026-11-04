import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, ref, type ComputedRef, type Ref } from "vue";
import { api } from "@/api/client";
import type { MemoryCatalog, MemoryNote, MemoryNoteType } from "@/api/types";
import MemoryPanel from "./MemoryPanel.vue";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref("zh-CN") }) }));
vi.mock("@/api/client", () => ({ api: { memoryNotes: vi.fn(), saveMemoryNote: vi.fn(), deleteMemoryNote: vi.fn() } }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));

const note: MemoryNote = { path: "preferences.md", type: "feedback", content: "Use pnpm.\n", modified: "2026-11-03T12:00:00Z", source_session: "session_1", origin: "auto-distill", size_bytes: 10 };
const catalog: MemoryCatalog = { scope: "agent", directory: "/agents/a/.pogo/memory", notes: [note] };
interface Controller {
  notes: Ref<MemoryNote[]>;
  selected: ComputedRef<MemoryNote | null>;
  draft: Ref<string>;
  draftType: Ref<MemoryNoteType>;
  creating: Ref<boolean>;
  newName: Ref<string>;
  dirty: ComputedRef<boolean>;
  notice: Ref<{ kind: string; text: string } | null>;
  leaveConfirm: Ref<{ ask: () => Promise<boolean> } | null>;
  load(): Promise<void>;
  createNote(): Promise<void>;
  save(): Promise<void>;
  remove(): Promise<void>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
function mount() {
  const props = reactive({ active: true, sessionId: "session_1" });
  const scope = effectScope(); scopes.push(scope);
  const controller = scope.run(() => (MemoryPanel as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} }))!;
  return { controller, props };
}
beforeEach(() => {
  vi.mocked(api.memoryNotes).mockReset().mockImplementation(async () => ({ ...catalog, notes: [{ ...note }] }));
  vi.mocked(api.saveMemoryNote).mockReset();
  vi.mocked(api.deleteMemoryNote).mockReset().mockResolvedValue({ deleted: true });
});
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); });

describe("memory panel", () => {
  it("loads the session scope and saves edited type and content with provenance", async () => {
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.notes.value).toHaveLength(1));
    expect(api.memoryNotes).toHaveBeenCalledExactlyOnceWith("session_1");
    controller.draft.value = "Use npm.\n";
    controller.draftType.value = "user";
    vi.mocked(api.saveMemoryNote).mockResolvedValue({ ...note, type: "user", content: "Use npm.\n", origin: "manual" });
    await controller.save();
    expect(api.saveMemoryNote).toHaveBeenCalledExactlyOnceWith("session_1", { path: "preferences.md", type: "user", content: "Use npm.\n" });
    expect(controller.selected.value?.origin).toBe("manual");
    expect(controller.dirty.value).toBe(false);
    expect(controller.notice.value?.text).toBe("memoryPanel.saved");
  });
  it("creates a new note and retains a rejected draft for correction", async () => {
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.notes.value).toHaveLength(1));
    await controller.createNote();
    controller.newName.value = "database.md";
    controller.draft.value = "api_key = \"abcdefghijklmnop\"";
    vi.mocked(api.saveMemoryNote).mockRejectedValue(new Error("memory note may contain credentials"));
    await controller.save();
    expect(controller.creating.value).toBe(true);
    expect(controller.draft.value).toContain("abcdefghijklmnop");
    expect(controller.notice.value?.kind).toBe("error");
  });
  it("deletes only after confirmation", async () => {
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.notes.value).toHaveLength(1));
    const ask = vi.fn().mockResolvedValue(true);
    controller.leaveConfirm.value = { ask };
    await controller.remove();
    expect(ask).toHaveBeenCalledOnce();
    expect(api.deleteMemoryNote).toHaveBeenCalledExactlyOnceWith("session_1", "preferences.md");
    expect(controller.notes.value).toEqual([]);
    expect(controller.notice.value?.text).toBe("memoryPanel.deleted");
  });
  it("does not let a late response cross a changed session", async () => {
    let resolveFirst!: (value: MemoryCatalog) => void;
    vi.mocked(api.memoryNotes).mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }))
      .mockResolvedValueOnce({ ...catalog, notes: [{ ...note, path: "other.md", source_session: "session_2" }] });
    const { controller, props } = mount();
    props.sessionId = "session_2";
    await vi.waitFor(() => expect(controller.notes.value[0]?.path).toBe("other.md"));
    resolveFirst(catalog);
    await Promise.resolve();
    expect(controller.notes.value[0]?.path).toBe("other.md");
    expect(controller.selected.value?.path).toBe("other.md");
    expect(api.memoryNotes).toHaveBeenNthCalledWith(2, "session_2");
  });
  it("refreshes an unchanged draft from an externally updated note without marking it dirty", async () => {
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.notes.value).toHaveLength(1));
    const updated = { ...note, type: "project" as const, content: "Use the updated project setup.\n" };
    vi.mocked(api.memoryNotes).mockResolvedValueOnce({ ...catalog, notes: [updated] });

    await controller.load();

    expect(controller.selected.value).toEqual(updated);
    expect(controller.draft.value).toBe(updated.content);
    expect(controller.draftType.value).toBe(updated.type);
    expect(controller.dirty.value).toBe(false);
  });
  it("preserves edits made while a refresh is waiting for the server", async () => {
    const { controller } = mount();
    await vi.waitFor(() => expect(controller.notes.value).toHaveLength(1));
    let resolveRefresh!: (value: MemoryCatalog) => void;
    vi.mocked(api.memoryNotes).mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve; }));
    const loading = controller.load();
    controller.draft.value = "My unsaved correction.\n";
    resolveRefresh({ ...catalog, notes: [{ ...note, content: "Updated by another session.\n" }] });

    await loading;

    expect(controller.draft.value).toBe("My unsaved correction.\n");
    expect(controller.dirty.value).toBe(true);
  });
});
