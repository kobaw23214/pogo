import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive } from "vue";
import type { SkillDocumentContent } from "@/api/types";
import { useAuthoredSkillEditor } from "./authoredSkillEditor";
const pending = <T>() => { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const a = "a".repeat(64), b = "b".repeat(64);
const doc = (revision = a): SkillDocumentContent => ({ content: "---\nname: sql-dates\ndescription: dates\n---\n# dates", editable: true, truncated: false, revision, version: revision === a ? 1 : 2 });
const history = () => ({ current_revision: b, versions: [{ revision: b, version: 2, created_at: "2026-10-22T00:00:00Z", enabled: true }, { revision: a, version: 1, created_at: "2026-10-21T00:00:00Z", enabled: true }] });
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
function setup() {
  const props = reactive({ scope: "ag-a:as-one", open: true });
  const content = vi.fn(async (_path?: string, _revision?: string) => doc(b)), versions = vi.fn(async () => history()), files = vi.fn(async (_path?: string, _rev?: string) => ({ relative_path: "", entries: [], truncated: false, limits: { max_entries: 32, max_depth: 8 } })), save = vi.fn(async (_input: { content: string; expected_revision: string }) => doc(b));
  const confirmLeave = vi.fn(async () => true), saved = vi.fn();
  const scope = effectScope(); scopes.push(scope);
  const editor = scope.run(() => useAuthoredSkillEditor({ scope: () => props.scope, open: () => props.open, content, versions, files, save, confirmLeave, saved }))!;
  return { props, editor, content, versions, files, save, confirmLeave, saved };
}
beforeEach(() => vi.spyOn(console, "warn").mockImplementation(() => {}));
afterEach(() => { scopes.splice(0).forEach(s => s.stop()); vi.restoreAllMocks(); });
describe("private Skill editor lifecycle", () => {
  it("loads the latest document and freezes reference/tree reads to its bundle revision", async () => {
    const c = setup(); await flush(); expect(c.editor.rootDocument.value?.revision).toBe(b);
    await c.editor.loadDocument("references/schema.md"); expect(c.content).toHaveBeenLastCalledWith("references/schema.md", b);
    await c.editor.loadFiles("references"); expect(c.files).toHaveBeenCalledWith("references", b);
    await c.editor.loadDocument("SKILL.md"); expect(c.content).toHaveBeenLastCalledWith("SKILL.md", undefined);
  });
  it("detects a newer concurrent save without relabelling or replacing the loaded document", async () => {
    const c = setup(); await flush(); c.content.mockResolvedValueOnce(doc(a)); await c.editor.load();
    expect(c.editor.newerVersion.value).toBe(true); expect(c.editor.rootDocument.value?.version).toBe(1); expect(c.editor.rootDocument.value?.revision).toBe(a);
    expect(c.editor.currentRevision.value).toBe(b);
  });
  it("keeps the current document and selection when leaving a dirty draft is rejected", async () => {
    const c = setup(); await flush(); c.confirmLeave.mockResolvedValue(false); const before = c.editor.rootDocument.value;
    await c.editor.selectRevision(a); expect(c.editor.selectedRevision.value).toBe(""); expect(c.editor.rootDocument.value).toEqual(before); expect(c.content).toHaveBeenCalledTimes(1);
  });
  it("past revisions remain read-only even if the response accidentally advertises editing", async () => {
    const c = setup(); await flush(); c.content.mockResolvedValue(doc(a)); await c.editor.selectRevision(a);
    expect(c.editor.historical.value).toBe(true); expect(c.editor.rootDocument.value?.editable).toBe(false);
    const referenced = await c.editor.loadDocument("SKILL.md"); expect(referenced.editable).toBe(false);
    await expect(c.editor.saveDocument({ content: "overwrite", expected_revision: a })).rejects.toThrow("read-only"); expect(c.save).not.toHaveBeenCalled();
  });
  it("does not apply an old version choice if its discard confirmation resolves in another Agent", async () => {
    const c = setup(); await flush(); const gate = pending<boolean>(); c.confirmLeave.mockReturnValueOnce(gate.promise);
    const selecting = c.editor.selectRevision(a); c.props.scope = "ag-b:as-other"; await flush(); gate.resolve(true); await selecting;
    expect(c.editor.selectedRevision.value).toBe(""); expect(c.editor.rootDocument.value?.revision).toBe(b);
    expect(c.content.mock.calls.every(call => call[1] !== a)).toBe(true);
  });
  it("drops an old Agent's pending content and version listing after a scope change", async () => {
    const c = setup(); await flush(); const old = pending<SkillDocumentContent>(); c.content.mockReturnValueOnce(old.promise); const reading = c.editor.load();
    c.props.scope = "ag-b:as-two"; await flush(); c.content.mockResolvedValueOnce({ ...doc(b), content: "new owner" });
    await c.editor.load(); old.resolve({ ...doc(a), content: "other owner secret" }); await reading;
    expect(c.editor.rootDocument.value?.content).toBe("new owner"); expect(c.editor.selectedRevision.value).toBe("");
  });
  it("does not adopt a save acknowledgment after its dialog closes", async () => {
    const c = setup(); await flush(); const result = pending<SkillDocumentContent>(); c.save.mockReturnValueOnce(result.promise);
    const saving = c.editor.saveDocument({ content: "new", expected_revision: b }); c.props.open = false; result.resolve(doc(a));
    await expect(saving).rejects.toThrow("scope changed"); expect(c.saved).not.toHaveBeenCalled(); expect(c.editor.rootDocument.value).toBeNull();
  });
  it("forwards the whole-bundle CAS unchanged and keeps conflict errors intact", async () => {
    const c = setup(); await flush(); const conflict = Object.assign(new Error("Changed in another window"), { status: 409 }); c.save.mockRejectedValueOnce(conflict);
    await expect(c.editor.saveDocument({ content: "new draft", expected_revision: b })).rejects.toBe(conflict);
    expect(c.save).toHaveBeenCalledWith({ content: "new draft", expected_revision: b }); expect(c.editor.rootDocument.value?.revision).toBe(b);
  });
  it("refreshes actual server versions only after a confirmed save", async () => {
    const c = setup(); await flush(); c.editor.documentSaved(doc(b)); await flush();
    expect(c.saved).toHaveBeenCalledWith(doc(b)); expect(c.versions).toHaveBeenCalledTimes(2); expect(c.editor.versions.value).toEqual(history().versions);
  });
});
