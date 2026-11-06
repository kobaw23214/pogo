import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, ref, type ComputedRef, type Ref } from "vue";
import { api } from "@/api/client";
import type { ManagedMemoryCatalog, ManagedMemoryScope, MemoryNote, MemoryNoteType } from "@/api/types";
import MemoryManager from "./MemoryManager.vue";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref("en") }) }));
vi.mock("@/api/client", () => ({ api: {
  managedMemoryCatalog: vi.fn(), saveManagedMemoryNote: vi.fn(), deleteManagedMemoryNote: vi.fn(), setMemoryAuto: vi.fn(),
} }));
const uuid = vi.hoisted(() => vi.fn(() => "12345678-1234-4123-8123-123456789abc"));
vi.mock("@/utils/uuid", () => ({ randomUUID: uuid }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
vi.mock("./MarkdownView.vue", () => ({ default: {} }));

const userNote: MemoryNote = { path: "preferences.md", type: "user", content: "# My writing preferences\nKeep replies concise.\n", modified: "2026-11-05T01:00:00Z", origin: "manual", size_bytes: 55 };
const agentNote: MemoryNote = { ...userNote, type: "project", content: "# Server architecture\nUse ARM Linux.\n", source_session: "old-session", origin: "auto-distill" };
function catalog(): ManagedMemoryCatalog {
  return { auto_generate: false, auto_generate_editable: true, scopes: [
    { id: "user", scope: "user", directory: "/owner/.pogo/memory", notes: [{ ...userNote }], writable: true },
    { id: "agent:ag_a", scope: "agent", agent_id: "ag_a", label: "Research assistant", directory: "/owner/agents/ag_a/.pogo/memory", notes: [{ ...agentNote }], writable: true },
    { id: "agent:ag_b", scope: "agent", agent_id: "ag_b", label: "Writing assistant", directory: "/owner/agents/ag_b/.pogo/memory", notes: [], writable: true },
  ] };
}
type Entry = { key: string; scope: ManagedMemoryScope; note: MemoryNote };
interface Controller {
  scopes: Ref<ManagedMemoryScope[]>;
  entries: ComputedRef<Entry[]>;
  filtered: ComputedRef<Entry[]>;
  selected: ComputedRef<Entry | null>;
  scopeFilter: Ref<string>;
  search: Ref<string>;
  draft: Ref<string>;
  draftType: Ref<MemoryNoteType>;
  draftScope: Ref<string>;
  creating: Ref<boolean>;
  editing: Ref<boolean>;
  dirty: ComputedRef<boolean>;
  notice: Ref<{ kind: string; text: string } | null>;
  leaveConfirm: Ref<{ ask: () => Promise<boolean> } | null>;
  autoGenerate: Ref<boolean>;
  originalModified: Ref<string>;
  conflicted: Ref<boolean>;
  conflictRefreshed: Ref<boolean>;
  latestStored: ComputedRef<MemoryNote | null>;
  title(note: MemoryNote): string;
  load(): Promise<void>;
  select(entry: Entry): Promise<void>;
  changeScope(value: string): Promise<void>;
  createNote(): Promise<void>;
  editNote(): void;
  save(): Promise<void>;
  forget(): Promise<void>;
  confirmLeave(): Promise<boolean>;
  setAuto(value: boolean): Promise<void>;
  continueWithLatest(): void;
  saveDraftAsNew(): Promise<void>;
}
const effectScopes: ReturnType<typeof effectScope>[] = [];
function mount(initialAgentId?: string, active = true) {
  const props = reactive({ active, initialAgentId });
  const emit = vi.fn();
  const scope = effectScope(); effectScopes.push(scope);
  const controller = scope.run(() => (MemoryManager as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { controller, props, emit };
}
async function ready(initialAgentId?: string) {
  const mounted = mount(initialAgentId);
  await vi.waitFor(() => expect(mounted.controller.scopes.value).toHaveLength(3));
  return mounted;
}
beforeEach(() => {
  uuid.mockReset().mockReturnValue("12345678-1234-4123-8123-123456789abc");
  vi.mocked(api.managedMemoryCatalog).mockReset().mockImplementation(async () => catalog());
  vi.mocked(api.saveManagedMemoryNote).mockReset();
  vi.mocked(api.deleteManagedMemoryNote).mockReset().mockResolvedValue({ deleted: true });
  vi.mocked(api.setMemoryAuto).mockReset().mockResolvedValue({ memory_auto: true });
});
afterEach(() => { effectScopes.splice(0).forEach(scope => scope.stop()); });

describe("platform memory management", () => {
  it("loads personal and Agent memories without creating a conversation", async () => {
    const { controller } = await ready();
    expect(api.managedMemoryCatalog).toHaveBeenCalledExactlyOnceWith();
    expect(controller.filtered.value).toHaveLength(2);
    expect(controller.editing.value).toBe(false);
    expect(controller.title(userNote)).toBe("My writing preferences");
  });

  it("shows the Agent and its readable personal memories through the Agent shortcut", async () => {
    const { controller } = await ready("ag_a");
    expect(controller.scopeFilter.value).toBe("agent:ag_a");
    expect(controller.filtered.value.map(entry => entry.scope.id).sort()).toEqual(["agent:ag_a", "user"]);
    expect(controller.selected.value?.scope.id).toBe("agent:ag_a");
    await controller.changeScope("");
    expect(controller.filtered.value).toHaveLength(2);
  });

  it("includes project memories for the selected Agent while keeping personal-only filters narrow", async () => {
    const result = catalog();
    result.scopes.push({ id: "project", scope: "project", directory: "/project/.pogo/memory", writable: true, notes: [{ ...agentNote, content: "Project convention" }] });
    result.scopes[2].notes = [{ ...agentNote, content: "Another Agent's fact" }];
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(result);
    const { controller } = mount("ag_a");
    await vi.waitFor(() => expect(controller.scopes.value).toHaveLength(4));
    expect(controller.filtered.value.map(entry => entry.scope.id).sort()).toEqual(["agent:ag_a", "project", "user"]);
    await controller.changeScope("user");
    expect(controller.filtered.value.map(entry => entry.scope.id)).toEqual(["user"]);
    await controller.changeScope("project");
    expect(controller.filtered.value.map(entry => entry.scope.id)).toEqual(["project"]);
  });

  it("falls back to the complete catalog for an unavailable Agent shortcut", async () => {
    const { controller } = await ready("unavailable-agent");
    expect(controller.scopeFilter.value).toBe("");
    expect(controller.filtered.value).toHaveLength(2);
  });

  it("searches readable contents and scope labels without treating equal file names as the same memory", async () => {
    const { controller } = await ready();
    expect(new Set(controller.entries.value.map(entry => entry.key)).size).toBe(2);
    controller.search.value = "ARM LINUX";
    expect(controller.filtered.value.map(entry => entry.scope.id)).toEqual(["agent:ag_a"]);
    controller.search.value = "Research assistant";
    expect(controller.filtered.value).toHaveLength(1);
  });

  it("creates a memory with an automatic safe filename in the explicitly chosen scope", async () => {
    const { controller } = await ready("ag_b");
    await controller.createNote();
    controller.draft.value = "# Delivery format\nSend the final text in Markdown.";
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValue({ ...agentNote, path: "memory-123456781234412381231234.md", content: controller.draft.value });
    await controller.save();
    expect(api.saveManagedMemoryNote).toHaveBeenCalledExactlyOnceWith("agent:ag_b", {
      path: "memory-123456781234412381231234.md", type: "project", content: "# Delivery format\nSend the final text in Markdown.", create_only: true,
    });
    expect(controller.selected.value?.scope.id).toBe("agent:ag_b");
    expect(controller.editing.value).toBe(false);
    expect(controller.dirty.value).toBe(false);
  });

  it("edits only the selected scope and does not send source metadata from the client", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote();
    controller.draft.value = "Use x86 Linux now.\n";
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValue({ ...agentNote, content: controller.draft.value });
    await controller.save();
    expect(api.saveManagedMemoryNote).toHaveBeenCalledExactlyOnceWith("agent:ag_a", {
      path: "preferences.md", type: "project", content: "Use x86 Linux now.\n", expected_modified: agentNote.modified,
    });
    expect(controller.entries.value.find(entry => entry.scope.id === "user")?.note.content).toBe(userNote.content);
  });

  it("keeps a rejected save editable", async () => {
    const { controller } = await ready();
    controller.editNote();
    controller.draft.value = "My corrected preference.";
    vi.mocked(api.saveManagedMemoryNote).mockRejectedValue(new Error("Memory changed on the server"));
    await controller.save();
    expect(controller.editing.value).toBe(true);
    expect(controller.dirty.value).toBe(true);
    expect(controller.draft.value).toBe("My corrected preference.");
    expect(controller.notice.value?.text).toBe("Memory changed on the server");
  });

  it("asks before discarding and does not switch scope when the user keeps editing", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote();
    controller.draft.value = "An unsaved correction";
    const ask = vi.fn().mockResolvedValue(false);
    controller.leaveConfirm.value = { ask };
    await controller.changeScope("user");
    expect(controller.scopeFilter.value).toBe("agent:ag_a");
    expect(controller.draft.value).toBe("An unsaved correction");
    expect(ask).toHaveBeenCalledOnce();
    ask.mockResolvedValue(true);
    await controller.changeScope("user");
    expect(controller.scopeFilter.value).toBe("user");
    expect(controller.editing.value).toBe(false);
  });

  it("does not overwrite a draft when the catalog is refreshed externally", async () => {
    const { controller } = await ready();
    controller.editNote();
    controller.draft.value = "My in-progress edit.";
    const fresh = catalog(); fresh.scopes[0].notes[0].content = "A different server edit.";
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(fresh);
    await controller.load();
    expect(controller.draft.value).toBe("My in-progress edit.");
    expect(controller.dirty.value).toBe(true);
    expect(controller.selected.value?.note.content).toBe("A different server edit.");
  });

  it("uses the version frozen when editing began, even after the catalog changes", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote(); controller.draft.value = "My in-progress correction";
    const fresh = catalog(); fresh.scopes[1].notes[0].modified = "2026-11-05T02:00:00.123456789Z";
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(fresh);
    await controller.load();
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValueOnce({ ...agentNote, content: controller.draft.value });
    await controller.save();
    expect(api.saveManagedMemoryNote).toHaveBeenCalledWith("agent:ag_a", expect.objectContaining({ expected_modified: agentNote.modified }));
  });

  it("preserves the draft on a conflict and only adopts a newer version after an explicit review action", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote(); controller.draft.value = "My correction to retain";
    const fresh = catalog();
    fresh.scopes[1].notes[0] = { ...agentNote, content: "The latest saved content", modified: "2026-11-05T02:00:00.123456789Z" };
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(fresh);
    vi.mocked(api.saveManagedMemoryNote).mockRejectedValueOnce(Object.assign(new Error("memory note changed"), { status: 409 }));
    await controller.save();
    expect(controller.draft.value).toBe("My correction to retain");
    expect(controller.editing.value).toBe(true);
    expect(controller.conflicted.value).toBe(true);
    expect(controller.notice.value?.text).toBe("memoryPanel.conflictHint");
    expect(controller.latestStored.value?.content).toBe("The latest saved content");
    expect(controller.originalModified.value).toBe(agentNote.modified);
    await controller.save();
    expect(api.saveManagedMemoryNote).toHaveBeenCalledOnce();
    controller.continueWithLatest();
    expect(controller.originalModified.value).toBe("2026-11-05T02:00:00.123456789Z");
    expect(controller.draft.value).toBe("My correction to retain");
    expect(api.saveManagedMemoryNote).toHaveBeenCalledOnce();
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValueOnce({ ...agentNote, content: controller.draft.value, modified: "2026-11-05T03:00:00Z" });
    await controller.save();
    expect(api.saveManagedMemoryNote).toHaveBeenLastCalledWith("agent:ag_a", expect.objectContaining({ expected_modified: "2026-11-05T02:00:00.123456789Z" }));
    expect(controller.editing.value).toBe(false);
  });

  it("can explicitly save a deleted memory's retained draft as a new file without resurrecting the old path", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote(); controller.draft.value = "My draft survives deletion";
    const deleted = catalog(); deleted.scopes[1].notes = [];
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(deleted);
    vi.mocked(api.saveManagedMemoryNote).mockRejectedValueOnce(Object.assign(new Error("memory note changed"), { status: 409 }));
    await controller.save();
    expect(controller.latestStored.value).toBeNull();
    expect(controller.conflictRefreshed.value).toBe(true);
    controller.continueWithLatest();
    expect(controller.conflicted.value).toBe(true);
    expect(controller.originalModified.value).toBe(agentNote.modified);
    expect(api.saveManagedMemoryNote).toHaveBeenCalledOnce();
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValueOnce({ ...agentNote, path: "memory-123456781234412381231234.md", content: controller.draft.value });
    await controller.saveDraftAsNew();
    expect(api.saveManagedMemoryNote).toHaveBeenLastCalledWith("agent:ag_a", {
      path: "memory-123456781234412381231234.md", type: "project", content: "My draft survives deletion", create_only: true,
    });
    expect(controller.entries.value.some(entry => entry.scope.id === "agent:ag_a" && entry.note.path === "preferences.md")).toBe(false);
  });

  it("does not turn a new-name collision into an overwrite", async () => {
    const { controller } = await ready("ag_a");
    await controller.createNote(); controller.draft.value = "My new memory";
    const collidedPath = "memory-123456781234412381231234.md";
    const collided = catalog(); collided.scopes[1].notes.push({ ...agentNote, path: collidedPath, content: "Another saved memory" });
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(collided);
    vi.mocked(api.saveManagedMemoryNote).mockRejectedValueOnce(Object.assign(new Error("memory note changed"), { status: 409 }));
    await controller.save();
    expect(controller.latestStored.value?.content).toBe("Another saved memory");
    controller.continueWithLatest();
    expect(controller.conflicted.value).toBe(true);
    uuid.mockReturnValueOnce("87654321-4321-4321-8321-123456789abc");
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValueOnce({ ...agentNote, path: "memory-876543214321432183211234.md", content: controller.draft.value });
    await controller.saveDraftAsNew();
    expect(api.saveManagedMemoryNote).toHaveBeenLastCalledWith("agent:ag_a", expect.objectContaining({ path: "memory-876543214321432183211234.md", create_only: true }));
    expect(controller.entries.value.find(entry => entry.scope.id === "agent:ag_a" && entry.note.path === collidedPath)?.note.content).toBe("Another saved memory");
  });

  it("refreshes a delete conflict without replaying the deletion", async () => {
    const { controller } = await ready("ag_a");
    controller.leaveConfirm.value = { ask: vi.fn().mockResolvedValue(true) };
    const fresh = catalog(); fresh.scopes[1].notes[0] = { ...agentNote, content: "Updated after the delete dialog opened", modified: "2026-11-05T02:00:00Z" };
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(fresh);
    vi.mocked(api.deleteManagedMemoryNote).mockRejectedValueOnce(Object.assign(new Error("memory note changed"), { status: 409 }));
    await controller.forget();
    expect(api.deleteManagedMemoryNote).toHaveBeenCalledExactlyOnceWith("agent:ag_a", "preferences.md", agentNote.modified);
    expect(controller.selected.value?.note.content).toBe("Updated after the delete dialog opened");
    expect(controller.notice.value?.text).toBe("memoryPanel.conflictHint");
  });

  it("forgets only after confirmation and preserves a same-named file in another scope", async () => {
    const { controller } = await ready("ag_a");
    const ask = vi.fn().mockResolvedValue(false);
    controller.leaveConfirm.value = { ask };
    await controller.forget();
    expect(api.deleteManagedMemoryNote).not.toHaveBeenCalled();
    ask.mockResolvedValue(true);
    await controller.forget();
    expect(api.deleteManagedMemoryNote).toHaveBeenCalledExactlyOnceWith("agent:ag_a", "preferences.md", agentNote.modified);
    expect(controller.entries.value.map(entry => entry.scope.id)).toEqual(["user"]);
    expect(ask).toHaveBeenLastCalledWith(expect.objectContaining({ body: "memoryPanel.forgetBody" }));
  });

  it("blocks navigation while a save is pending", async () => {
    const { controller } = await ready("ag_a");
    controller.editNote(); controller.draft.value = "A saved correction";
    let resolve!: (note: MemoryNote) => void;
    vi.mocked(api.saveManagedMemoryNote).mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    const pending = controller.save();
    expect(await controller.confirmLeave()).toBe(false);
    await controller.changeScope("user");
    expect(controller.scopeFilter.value).toBe("agent:ag_a");
    resolve({ ...agentNote, content: "A saved correction" });
    await pending;
    expect(await controller.confirmLeave()).toBe(true);
  });

  it("does not let an older refresh roll back a completed save", async () => {
    const { controller } = await ready("ag_a");
    let resolveRefresh!: (result: ManagedMemoryCatalog) => void;
    vi.mocked(api.managedMemoryCatalog).mockImplementationOnce(() => new Promise(done => { resolveRefresh = done; }));
    const refresh = controller.load();
    controller.editNote(); controller.draft.value = "A newer saved correction";
    vi.mocked(api.saveManagedMemoryNote).mockResolvedValueOnce({ ...agentNote, content: controller.draft.value });
    await controller.save();
    resolveRefresh(catalog());
    await refresh;
    expect(controller.selected.value?.note.content).toBe("A newer saved correction");
    expect(controller.editing.value).toBe(false);
  });

  it("does not write empty content or read-only scopes", async () => {
    const result = catalog(); result.scopes[1].writable = false;
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(result);
    const { controller } = await ready("ag_a");
    controller.editNote();
    expect(controller.editing.value).toBe(false);
    await controller.forget();
    expect(api.deleteManagedMemoryNote).not.toHaveBeenCalled();
    await controller.createNote();
    controller.draft.value = "  \n";
    await controller.save();
    expect(api.saveManagedMemoryNote).not.toHaveBeenCalled();
  });

  it("discards old catalog responses after a change to the requested Agent", async () => {
    let resolve!: (catalog: ManagedMemoryCatalog) => void;
    vi.mocked(api.managedMemoryCatalog).mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    const { controller, props } = mount("ag_a");
    await vi.waitFor(() => expect(api.managedMemoryCatalog).toHaveBeenCalledOnce());
    props.initialAgentId = "ag_b";
    await vi.waitFor(() => expect(controller.scopeFilter.value).toBe("agent:ag_b"));
    const stale = catalog(); stale.scopes[1].notes[0].content = "Stale response";
    resolve(stale);
    await Promise.resolve(); await Promise.resolve();
    expect(controller.scopeFilter.value).toBe("agent:ag_b");
    expect(controller.entries.value.some(entry => entry.note.content === "Stale response")).toBe(false);
  });

  it("updates the automatic-generation setting only for permitted users and emits persisted state", async () => {
    const { controller, emit } = await ready();
    await controller.setAuto(true);
    expect(api.setMemoryAuto).toHaveBeenCalledExactlyOnceWith(true);
    expect(controller.autoGenerate.value).toBe(true);
    expect(emit).toHaveBeenCalledWith("auto-change", true);
    const result = catalog(); result.auto_generate_editable = false;
    vi.mocked(api.managedMemoryCatalog).mockResolvedValueOnce(result);
    await controller.load();
    vi.mocked(api.setMemoryAuto).mockClear();
    await controller.setAuto(true);
    expect(api.setMemoryAuto).not.toHaveBeenCalled();
  });

  it("does not fetch before the settings page becomes active", async () => {
    const { controller, props } = mount(undefined, false);
    expect(api.managedMemoryCatalog).not.toHaveBeenCalled();
    props.active = true;
    await vi.waitFor(() => expect(controller.scopes.value).toHaveLength(3));
  });
});
