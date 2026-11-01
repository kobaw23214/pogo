import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive } from "vue";
import type { SkillPackageEntry, SkillPackageFiles } from "@/api/types";
const hooks = vi.hoisted(() => ({ cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => hooks.cleanup.push(fn) }));
import { skillFileBytes, useSkillPackageFiles } from "./skillPackageFiles";
const entry = (path: string, kind: SkillPackageEntry["kind"] = "file", previewable = true): SkillPackageEntry => ({ path, name: path.split("/").pop()!, kind, previewable: kind === "file" && previewable, expandable: kind === "directory", size_bytes: kind === "directory" ? 0 : 2048 });
const list = (path: string, entries: SkillPackageEntry[], truncated = false): SkillPackageFiles => ({ relative_path: path, entries, truncated, limits: { max_entries: 1000, max_depth: 16 } });
const fixture: Record<string, SkillPackageFiles> = {
  "": list("", [entry("SKILL.md"), entry("references", "directory"), entry("runtime", "directory"), entry("tools.json"), entry("image.png", "file", false)]),
  references: list("references", [entry("references/sql", "directory"), entry("references/contract.json")]),
  "references/sql": list("references/sql", [entry("references/sql/workflow.md")]),
  runtime: list("runtime", [entry("runtime/huge.tar", "file", false)]),
};
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const scopes: ReturnType<typeof effectScope>[] = [];
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; };
function mount() { const props = reactive({ scope: "skill-a" }), load = vi.fn(async (path: string) => fixture[path]!); const scope = effectScope(); scopes.push(scope); const c = scope.run(() => useSkillPackageFiles({ scope: () => props.scope, load }))!; return { c, props, load }; }
afterEach(() => { for (const cleanup of hooks.cleanup.splice(0)) cleanup(); for (const scope of scopes.splice(0)) scope.stop(); });
describe("Lazy Skill package file tree", () => {
  it("loads only root and references by default, leaving runtime archives and nested folders untouched", async () => {
    const { c, load } = mount(); await flush(); expect(load.mock.calls.map(row => row[0])).toEqual(["", "references"]);
    expect(c.rows.value.flatMap(row => row.entry ? [row.entry.path] : [])).toContain("references/sql"); expect(c.rows.value.flatMap(row => row.entry ? [row.entry.path] : [])).not.toContain("runtime/huge.tar");
    expect(c.expanded.value.has("references")).toBe(true); expect(c.index.value.get("SKILL.md")?.previewable).toBe(true);
  });
  it("reveals ancestors for a nested internal link and keeps them expanded while navigating back", async () => {
    const { c, load } = mount(); await flush(); const file = await c.reveal("references/sql/workflow.md"); expect(file?.path).toBe("references/sql/workflow.md"); expect(c.expanded.value.has("references/sql")).toBe(true);
    await c.reveal("SKILL.md"); expect(c.expanded.value.has("references/sql")).toBe(true); expect(load).toHaveBeenCalledTimes(3);
  });
  it("collapses and reopens a loaded directory without losing its contents or making duplicate requests", async () => {
    const { c, load } = mount(); await flush(); await c.toggle(entry("runtime", "directory")); expect(c.rows.value.some(row => row.entry?.path === "runtime/huge.tar")).toBe(true);
    await c.toggle(entry("runtime", "directory")); expect(c.rows.value.some(row => row.entry?.path === "runtime/huge.tar")).toBe(false);
    await c.toggle(entry("runtime", "directory")); expect(load.mock.calls.filter(row => row[0] === "runtime")).toHaveLength(1);
  });
  it("does not recurse through symlinks or a directory at the backend depth limit", async () => {
    const { c, load } = mount(); await flush(); await c.toggle(entry("link", "symlink", false)); await c.toggle({ ...entry("deep", "directory"), expandable: false }); expect(load).toHaveBeenCalledTimes(2);
  });
  it("renders truncation and errors explicitly and retries only the requested directory", async () => {
    const { c, load } = mount(); await flush(); load.mockRejectedValueOnce(new Error("Directory unavailable")); await c.toggle(entry("runtime", "directory"));
    expect(c.rows.value.find(row => row.state === "error")?.message).toBe("Directory unavailable");
    load.mockResolvedValueOnce(list("runtime", [entry("runtime/a.txt")], true)); await c.ensure("runtime", true);
    expect(c.rows.value.some(row => row.state === "truncated")).toBe(true); expect(c.errors.value.runtime).toBeUndefined();
  });
  it("drops late listings and cannot retain a previous Skill's private tree", async () => {
    const { c, load, props } = mount(); await flush(); const late = deferred<SkillPackageFiles>(); load.mockReturnValueOnce(late.promise); const request = c.ensure("runtime"); await flush();
    props.scope = "skill-b"; await flush(); late.resolve(list("runtime", [entry("runtime/private-secret.txt")])); await request;
    expect(c.index.value.has("runtime/private-secret.txt")).toBe(false); expect(c.expanded.value.has("runtime")).toBe(false);
  });
  it("joins concurrent requests for the same directory and ignores work after unmount", async () => {
    const { c, load } = mount(); await flush(); const late = deferred<SkillPackageFiles>(); load.mockReturnValueOnce(late.promise); const a = c.ensure("runtime"), b = c.ensure("runtime"); await flush();
    expect(load.mock.calls.filter(row => row[0] === "runtime")).toHaveLength(1); for (const cleanup of hooks.cleanup.splice(0)) cleanup(); late.resolve(fixture.runtime!); await Promise.all([a, b]); expect(c.directories.value.runtime).toBeUndefined();
  });
  it("formats exact file sizes without expanding or reading the file", () => { expect(skillFileBytes(0)).toBe("0 B"); expect(skillFileBytes(2048)).toBe("2.0 KiB"); expect(skillFileBytes(104857600)).toBe("100.0 MiB"); });
});
