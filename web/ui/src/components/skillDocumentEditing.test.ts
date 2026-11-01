import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive } from "vue";
import type { SkillDocumentContent } from "@/api/types";
const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => hooks.cleanup.push(fn) }));
import { useSkillDocumentEditing } from "./skillDocumentEditing";
const raw = "---\nname: demo\ndescription: 原说明\n---\n# 原说明\n[流程](references/workflow.md)\n";
const doc = (content = raw, revision = "revision-a"): SkillDocumentContent => ({ content, revision, relative_path: "SKILL.md", editable: true, truncated: false });
const pending = <T>() => { let resolve!: (value: T) => void; let reject!: (value: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const scopes: ReturnType<typeof effectScope>[] = [];
function mount() {
  const props = reactive({ scope: "skill-a", path: "SKILL.md", document: doc(), enabled: true });
  const save = vi.fn(async () => doc("# saved", "revision-b")), saved = vi.fn(), discard = vi.fn(async () => false);
  const scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => useSkillDocumentEditing({ scope: () => props.scope, path: () => props.path, document: () => props.document, enabled: () => props.enabled, save, saved, confirmDiscard: discard }))!;
  return { c, props, save, saved, discard };
}
afterEach(() => { for (const fn of hooks.cleanup.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); });
describe("managed Skill revision-bound draft", () => {
  it("starts in preview and edits complete raw frontmatter without writing while toggling preview", () => {
    const { c, save } = mount(); expect(c.mode.value).toBe("preview"); expect(c.editing.value).toBe(false);
    c.edit(); expect(c.draft.value).toBe(raw); expect(c.revision.value).toBe("revision-a");
    c.draft.value = raw.replace("# 原说明", "# 新说明"); c.preview(); c.edit();
    expect(c.draft.value).toContain("# 新说明"); expect(c.dirty.value).toBe(true); expect(save).not.toHaveBeenCalled();
  });
  it("saves exactly the draft and captured revision and adopts only the acknowledged response", async () => {
    const { c, save, saved } = mount(); c.edit(); c.draft.value = raw + "\n新增任务\n";
    expect(await c.save()).toBe(true); expect(save).toHaveBeenCalledExactlyOnceWith({ content: raw + "\n新增任务\n", expected_revision: "revision-a" });
    expect(saved).toHaveBeenCalledExactlyOnceWith(doc("# saved", "revision-b")); expect(c.dirty.value).toBe(false); expect(c.editing.value).toBe(false); expect(c.saving.value).toBe(false);
  });
  it("retains rejected drafts and the old revision for a normal corrected retry", async () => {
    const { c, save } = mount(); save.mockRejectedValueOnce(new Error("name cannot change")); c.edit(); c.draft.value = raw.replace("demo", "changed");
    expect(await c.save()).toBe(false); expect(c.draft.value).toContain("name: changed"); expect(c.error.value).toBe("name cannot change"); expect(c.editing.value).toBe(true);
    c.draft.value = raw + "合法修改\n"; expect(await c.save()).toBe(true); expect(save).toHaveBeenLastCalledWith({ content: raw + "合法修改\n", expected_revision: "revision-a" });
  });
  it("keeps a conflict draft readable and does not retry or overwrite the new server revision", async () => {
    const { c, props, save, saved, discard } = mount(); save.mockRejectedValueOnce(Object.assign(new Error("changed in another window"), { status: 409 }));
    c.edit(); c.draft.value = raw + "我的修改"; expect(await c.save()).toBe(false);
    props.document = doc("他人修改", "revision-c"); expect(c.draft.value).toBe(raw + "我的修改"); expect(c.revision.value).toBe("revision-a"); expect(c.conflict.value).toBe(true);
    expect(await c.save()).toBe(false); expect(save).toHaveBeenCalledTimes(1); expect(saved).not.toHaveBeenCalled();
    expect(await c.confirmLeave()).toBe(false); expect(c.draft.value).toBe(raw + "我的修改");
    discard.mockResolvedValueOnce(true); expect(await c.confirmLeave()).toBe(true); c.edit(); expect(c.draft.value).toBe("他人修改"); expect(c.revision.value).toBe("revision-c");
  });
  it("asks before dirty navigation and cancel but does not ask for unchanged source", async () => {
    const { c, discard } = mount(); c.edit(); expect(await c.confirmLeave()).toBe(true); expect(discard).not.toHaveBeenCalled();
    c.edit(); c.draft.value = "改动"; expect(await c.confirmLeave()).toBe(false); expect(c.editing.value).toBe(true);
    discard.mockResolvedValueOnce(true); expect(await c.confirmLeave()).toBe(true); expect(c.editing.value).toBe(false);
  });
  it.each(["disabled", "reference", "readonly", "truncated", "unversioned"])("cannot edit %s documents or invoke a writer", async kind => {
    const { c, props, save } = mount();
    if (kind === "disabled") props.enabled = false;
    if (kind === "reference") props.path = "references/workflow.md";
    if (kind === "readonly") props.document.editable = false;
    if (kind === "truncated") props.document.truncated = true;
    if (kind === "unversioned") props.document.revision = undefined;
    c.edit(); expect(c.editing.value).toBe(false); expect(await c.save()).toBe(false); expect(save).not.toHaveBeenCalled();
  });
  it("blocks leaving while a save is in flight and ignores its response after a scope change", async () => {
    const { c, props, save, saved } = mount(), late = pending<SkillDocumentContent>(); save.mockReturnValueOnce(late.promise);
    c.edit(); c.draft.value += "修改"; const request = c.save(); expect(c.saving.value).toBe(true); expect(await c.confirmLeave()).toBe(false);
    props.scope = "skill-b"; props.document = doc("new scope", "revision-b"); late.resolve(doc("late old content")); await request;
    expect(saved).not.toHaveBeenCalled(); expect(c.editing.value).toBe(false); expect(c.saving.value).toBe(false);
  });
  it("ignores a save result after unmount instead of dispatching a catalog change", async () => {
    const { c, save, saved } = mount(), late = pending<SkillDocumentContent>(); save.mockReturnValueOnce(late.promise); c.edit(); c.draft.value += "修改";
    const request = c.save(); for (const fn of hooks.cleanup.splice(0)) fn(); late.resolve(doc("late")); await request; expect(saved).not.toHaveBeenCalled();
  });
});
