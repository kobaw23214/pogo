import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive } from "vue";
import type { SkillDocumentContent } from "@/api/types";
const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => hooks.cleanup.push(fn) }));
import { resolveSkillDocumentLink, skillDocumentLanguage, useSkillDocumentNavigation } from "./skillDocumentNavigation";
const doc = (path = "SKILL.md", content = "# root"): SkillDocumentContent => ({ content, relative_path: path, truncated: false });
const pending = <T>() => { let resolve!: (value: T) => void; let reject!: (value: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const scopes: ReturnType<typeof effectScope>[] = [];
function mount() {
  const props = reactive({ scope: "skill-a:session-a", root: doc() }), load = vi.fn(async (path: string) => doc(path, `body ${path}`));
  const scope = effectScope(); scopes.push(scope);
  const c = scope.run(() => useSkillDocumentNavigation({ scope: () => props.scope, root: () => props.root, load }))!;
  return { c, props, load };
}
afterEach(() => { for (const fn of hooks.cleanup.splice(0)) fn(); for (const scope of scopes.splice(0)) scope.stop(); });

describe("Skill package document navigation", () => {
  it("resolves the clicked nested link from its document directory, never /manage/", () => {
    expect(resolveSkillDocumentLink("SKILL.md", "references/sql-reuse/workflow.md")).toEqual({ kind: "document", path: "references/sql-reuse/workflow.md", anchor: "" });
    expect(resolveSkillDocumentLink("references/sql-reuse/workflow.md", "../mcp-contract.json#schema")).toEqual({ kind: "document", path: "references/mcp-contract.json", anchor: "schema" });
    expect(resolveSkillDocumentLink("references/workflow.md", "./示例%20说明.txt")).toEqual({ kind: "document", path: "references/示例 说明.txt", anchor: "" });
  });
  it.each(["../../outside.md", "/etc/passwd", "//example.test/a", "file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "..\\outside.md", "%2e%2e/outside.md", "%252e%252e/outside.md", "file.md?path=secret", "nul%00.txt", "%E0%A4%A"])("blocks unsafe root link %s", href => {
    expect(resolveSkillDocumentLink("SKILL.md", href)).toEqual({ kind: "blocked" });
  });
  it("starts with the already loaded root, follows nested documents and returns without fetching the app route", async () => {
    const { c, load } = mount(); expect(c.document.value?.content).toBe("# root"); expect(load).not.toHaveBeenCalled();
    await c.open("references/sql/workflow.md"); await c.open("../contract.json");
    expect(load.mock.calls.map(row => row[0])).toEqual(["references/sql/workflow.md", "references/contract.json"]);
    expect(c.location.value.path).toBe("references/contract.json"); expect(c.history.value).toHaveLength(2);
    await c.back(); expect(c.location.value.path).toBe("references/sql/workflow.md");
    await c.home(); expect(c.location.value.path).toBe("SKILL.md"); expect(c.history.value).toHaveLength(0); expect(c.document.value?.content).toBe("# root");
  });
  it("keeps same-file anchors inside the preview without another request", async () => {
    const { c, load } = mount(); await c.open("#中文%20标题"); expect(c.location.value).toEqual({ path: "SKILL.md", anchor: "中文 标题" }); expect(load).not.toHaveBeenCalled(); expect(c.history.value).toHaveLength(0);
  });
  it("leaves current content visible when an external package traversal is blocked", async () => {
    const { c, load } = mount(); await c.open("../private.md"); expect(c.blocked.value).toBe(true); expect(c.document.value?.content).toBe("# root"); expect(c.location.value.path).toBe("SKILL.md"); expect(load).not.toHaveBeenCalled();
  });
  it("shows a useful error at the failed document and retries the exact path", async () => {
    const { c, load } = mount(); load.mockRejectedValueOnce(new Error("Document not found")); await c.open("references/missing.md");
    expect(c.error.value).toBe("Document not found"); expect(c.document.value).toBeNull(); expect(c.loading.value).toBe(false);
    await c.retry(); expect(load).toHaveBeenLastCalledWith("references/missing.md"); expect(c.document.value?.content).toBe("body references/missing.md"); expect(c.error.value).toBe("");
  });
  it("ignores a late response after returning to instructions", async () => {
    const { c, load } = mount(), late = pending<SkillDocumentContent>(); load.mockReturnValueOnce(late.promise);
    const request = c.open("references/slow.md"); expect(c.loading.value).toBe(true); await c.home();
    late.resolve(doc("references/slow.md", "stale")); await request;
    expect(c.document.value?.content).toBe("# root"); expect(c.location.value.path).toBe("SKILL.md"); expect(c.loading.value).toBe(false);
  });
  it("ignores previous Skill/session documents and errors after the scope changes", async () => {
    const { c, props, load } = mount(), late = pending<SkillDocumentContent>(); load.mockReturnValueOnce(late.promise);
    const request = c.open("references/private.md"); props.scope = "skill-b:session-b"; props.root = doc("SKILL.md", "new scope"); late.reject(new Error("stale error")); await request;
    expect(c.document.value?.content).toBe("new scope"); expect(c.error.value).toBe(""); expect(c.history.value).toHaveLength(0); expect(c.loading.value).toBe(false);
  });
  it("does not publish a completed request after the viewer unmounts", async () => {
    const { c, load } = mount(), late = pending<SkillDocumentContent>(); load.mockReturnValueOnce(late.promise);
    const request = c.open("references/late.md"); for (const fn of hooks.cleanup.splice(0)) fn(); late.resolve(doc("references/late.md", "late")); await request; expect(c.document.value).toBeNull();
  });
  it("identifies root Markdown and JSON/plain text without trusting source HTML", () => {
    expect(skillDocumentLanguage({ content: "", truncated: false }, "SKILL.md")).toBe("markdown");
    expect(skillDocumentLanguage(doc(), "contract.json")).toBe("json");
    expect(skillDocumentLanguage({ ...doc(), media_type: "text/markdown; charset=utf-8" }, "guide")).toBe("markdown");
    expect(skillDocumentLanguage(doc(), "document.html")).toBe("text");
  });
  it("opens tree selections as package-root paths even when the current document is nested", async () => {
    const { c, load } = mount(); await c.open("references/sql/workflow.md"); await c.openPath("scripts/helper.py");
    expect(load).toHaveBeenLastCalledWith("scripts/helper.py"); expect(c.location.value.path).toBe("scripts/helper.py"); await c.back(); expect(c.location.value.path).toBe("references/sql/workflow.md");
  });
});
