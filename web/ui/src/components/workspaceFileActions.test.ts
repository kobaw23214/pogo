import { afterEach, describe, expect, it, vi } from "vitest";
import { beginWorkspaceFileMutation, observeWorkspaceFileMutation, publishWorkspaceFileMutation, registerWorkspaceMutationGuard, workspaceMutationPending, workspacePathContains } from "./workspaceFileActions";
import { indexEpoch, treeEpoch, treeExpanded } from "./fileTreeStore";
const cleanup: (() => void)[] = [];
afterEach(() => { for (const fn of cleanup.splice(0)) fn(); treeExpanded.clear(); });
describe("workspace file actions", () => {
  it("uses exact case-sensitive path segments, never matching sibling prefixes", () => {
    expect(workspacePathContains("reports", "reports/a/b.txt")).toBe(true);
    for (const path of ["reports-old/a.txt", "Reports/a.txt", "else/reports/a.txt"]) expect(workspacePathContains("reports",path)).toBe(false);
  });
  it("blocks a parent mutation while a descendant draft or Office canvas is guarded", () => {
    cleanup.push(registerWorkspaceMutationGuard(path => !workspacePathContains(path,"reports/unsaved.xlsx")));
    expect(beginWorkspaceFileMutation("reports")).toBeNull();
    const release=beginWorkspaceFileMutation("reports-other"); expect(release).not.toBeNull(); cleanup.push(release!);
  });
  it("reserves descendants until success or failure cleanup and keeps siblings usable", () => {
    const release=beginWorkspaceFileMutation("reports")!; cleanup.push(release);
    expect(workspaceMutationPending("reports/a.txt")).toBe(true);
    expect(beginWorkspaceFileMutation("reports/a.txt")).toBeNull();
    const sibling=beginWorkspaceFileMutation("reports-other")!;cleanup.push(sibling);expect(sibling).toBeTypeOf("function");
    release();expect(workspaceMutationPending("reports/a.txt")).toBe(false);
  });
  it("updates expanded paths, closes stale previews through observers and invalidates tree plus quick-open", () => {
    treeExpanded.add("reports");treeExpanded.add("reports/quarter");treeExpanded.add("reports-other");
    const listener=vi.fn();cleanup.push(observeWorkspaceFileMutation(listener));
    const beforeTree=treeEpoch.value,beforeIndex=indexEpoch.value;
    const change={ok:true,kind:"rename",path:"reports",new_path:"评审",is_directory:true} as const;
    publishWorkspaceFileMutation(change);
    expect([...treeExpanded]).toEqual(["reports-other","评审","评审/quarter"]);
    expect(listener).toHaveBeenCalledExactlyOnceWith(change);expect(treeEpoch.value).toBe(beforeTree+1);expect(indexEpoch.value).toBe(beforeIndex+1);
    publishWorkspaceFileMutation({...change,kind:"delete",path:"评审",new_path:""});expect([...treeExpanded]).toEqual(["reports-other"]);
  });
});
