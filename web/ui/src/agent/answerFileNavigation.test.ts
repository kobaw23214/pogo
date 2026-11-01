import { describe, expect, it, vi } from "vitest";
import type { SessionFileLinkResolution } from "@/api/types";
import { createAnswerFileNavigation } from "./answerFileNavigation";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";

const initial: WorkspaceFileScope = {
  conversationId: "chat-a", cwd: "/home/pogo/users/alice/agents/a",
  root: "/home/pogo/users/alice", workspace: "agents/a",
};
function result(status: SessionFileLinkResolution["status"], paths: string[], complete = true): SessionFileLinkResolution {
  return { status, complete, candidates: paths.map(path => ({ path: `agents/a/${path}`, relative_path: path, name: path.split("/").pop()! })) };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
function setup() {
  let scope: WorkspaceFileScope | null = { ...initial };
  const resolve = vi.fn<(_: string, path: string, byName: boolean, signal: AbortSignal) => Promise<SessionFileLinkResolution>>();
  const openFile = vi.fn();
  const navigation = createAnswerFileNavigation({ scope: () => scope, resolve, openFile, failureMessage: () => "Lookup failed; check the workspace" });
  return { navigation, resolve, openFile, setScope: (next: WorkspaceFileScope | null) => { scope = next; } };
}
const named = (path: string) => ({ path, name: path.split("/").pop()!, lookupByName: true as const });

describe("answer file navigation", () => {
  it.each(["sample-a.txt", "sample-b.txt", "test-data.xlsx", "季度 报告.docx"])("opens the missing-directory regression for %s through the shared opener", async name => {
    const f = setup();
    f.resolve.mockResolvedValue(result("unique", [`testdir/${name}`]));
    await f.navigation.open(named(name), initial);
    expect(f.resolve).toHaveBeenCalledWith("chat-a", name, true, expect.any(AbortSignal));
    expect(f.openFile).toHaveBeenCalledOnce();
    expect(f.openFile.mock.calls[0]![0]).toMatchObject({ path: `agents/a/testdir/${name}`, name });
    expect(f.navigation.choice.value).toBeNull();
  });

  it("uses the exact root file when the server found it and retains a line locator", async () => {
    const f = setup();
    f.resolve.mockResolvedValue(result("exact", ["sample-a.txt"]));
    await f.navigation.open({ ...named("sample-a.txt"), lineStart: 2, lineEnd: 5 }, initial);
    expect(f.openFile.mock.calls[0]![0]).toMatchObject({ path: "agents/a/sample-a.txt", lineStart: 2, lineEnd: 5 });
  });

  it("leaves explicit paths exact, including a local folder shaped like the workspace", () => {
    const f = setup();
    for (const path of ["testdir/sample-a.txt", "agents/a/sample-a.txt", "sample-a.txt"]) {
      const returned = f.navigation.open({ path, name: "sample-a.txt" }, initial);
      expect(returned).toBeUndefined();
      expect(f.openFile.mock.lastCall![0].path).toBe(`agents/a/${path}`);
    }
    expect(f.resolve).not.toHaveBeenCalled();
  });

  it("asks which same-name file to open and never accepts an arbitrary picker path", async () => {
    const f = setup();
    f.resolve.mockResolvedValue(result("ambiguous", ["one/report.xlsx", "two/report.xlsx"]));
    await f.navigation.open(named("report.xlsx"), initial);
    expect(f.openFile).not.toHaveBeenCalled();
    expect(f.navigation.choice.value?.candidates.map(x => x.path)).toEqual(["one/report.xlsx", "two/report.xlsx"]);
    f.navigation.choose("other/report.xlsx");
    expect(f.openFile).not.toHaveBeenCalled();
    f.navigation.choose("two/report.xlsx");
    expect(f.openFile.mock.lastCall![0].path).toBe("agents/a/two/report.xlsx");
    expect(f.navigation.choice.value).toBeNull();
  });

  it("does not auto-open a single partial result or claim an incomplete empty search found nothing", async () => {
    const f = setup();
    f.resolve.mockResolvedValueOnce(result("incomplete", ["one/report.xlsx"], false));
    await f.navigation.open(named("report.xlsx"), initial);
    expect(f.openFile).not.toHaveBeenCalled();
    expect(f.navigation.choice.value?.incomplete).toBe(true);
    f.navigation.choose("one/report.xlsx");
    expect(f.openFile.mock.lastCall![0].path).toBe("agents/a/one/report.xlsx");
    f.resolve.mockResolvedValueOnce(result("incomplete", [], false));
    await f.navigation.open(named("absent.txt"), initial);
    expect(f.navigation.choice.value).toMatchObject({ name: "absent.txt", candidates: [], incomplete: true });
  });

  it("shows missing or failed lookup without opening a broken text or Office tab", async () => {
    const f = setup();
    f.resolve.mockResolvedValueOnce(result("not_found", []));
    await f.navigation.open(named("missing.xlsx"), initial);
    expect(f.navigation.choice.value).toMatchObject({ name: "missing.xlsx", candidates: [], incomplete: false });
    expect(f.openFile).not.toHaveBeenCalled();
    f.resolve.mockRejectedValueOnce(new Error("network"));
    await f.navigation.open(named("missing.txt"), initial);
    expect(f.navigation.choice.value?.error).toBe("Lookup failed; check the workspace");
    expect(f.openFile).not.toHaveBeenCalled();
  });

  it("drops delayed results when the session or its workspace has changed", async () => {
    for (const next of [null, { ...initial, conversationId: "chat-b" }, { ...initial, cwd: "/other", workspace: "other" }]) {
      const f = setup(), query = deferred<SessionFileLinkResolution>();
      f.resolve.mockReturnValue(query.promise);
      const done = f.navigation.open(named("sample-a.txt"), initial);
      f.setScope(next);
      query.resolve(result("unique", ["testdir/sample-a.txt"]));
      await done;
      expect(f.openFile).not.toHaveBeenCalled();
      expect(f.navigation.choice.value).toBeNull();
    }
  });

  it("cancels the prior click and ignores its late response even if the transport ignores abort", async () => {
    const f = setup(), first = deferred<SessionFileLinkResolution>();
    f.resolve.mockReturnValueOnce(first.promise).mockResolvedValueOnce(result("unique", ["second/sample-a.txt"]));
    const old = f.navigation.open(named("sample-a.txt"), initial);
    const signal = f.resolve.mock.calls[0]![3];
    await f.navigation.open(named("sample-a.txt"), initial);
    expect(signal.aborted).toBe(true);
    first.resolve(result("unique", ["first/sample-a.txt"]));
    await old;
    expect(f.openFile).toHaveBeenCalledOnce();
    expect(f.openFile.mock.lastCall![0].path).toBe("agents/a/second/sample-a.txt");
  });

  it("cancels on close and will not open an old selection after switching sessions", async () => {
    const f = setup(), query = deferred<SessionFileLinkResolution>();
    f.resolve.mockReturnValueOnce(query.promise);
    const done = f.navigation.open(named("a.txt"), initial);
    f.navigation.cancel();
    query.resolve(result("unique", ["testdir/a.txt"]));
    await done;
    expect(f.navigation.choice.value).toBeNull();
    f.resolve.mockResolvedValueOnce(result("ambiguous", ["one/a.txt", "two/a.txt"]));
    await f.navigation.open(named("a.txt"), initial);
    f.setScope({ ...initial, conversationId: "chat-b" });
    f.navigation.choose("one/a.txt");
    expect(f.openFile).not.toHaveBeenCalled();
    expect(f.navigation.choice.value).toBeNull();
  });

  it("does not steal focus back after a manual file open and supports the shared opener cancelling lookups", async () => {
    const f = setup(), query = deferred<SessionFileLinkResolution>();
    f.resolve.mockReturnValueOnce(query.promise);
    const old = f.navigation.open(named("old.txt"), initial);
    // The attachment, workspace and dock navigation entries cancel before
    // they open a different file. The old transport may still finish later.
    f.navigation.cancel();
    f.openFile({ path: "agents/a/manual.txt", name: "manual.txt" });
    query.resolve(result("unique", ["testdir/old.txt"]));
    await old;
    expect(f.openFile).toHaveBeenCalledOnce();
    expect(f.openFile.mock.lastCall![0].name).toBe("manual.txt");

    f.openFile.mockImplementation(() => f.navigation.cancel());
    f.resolve.mockResolvedValueOnce(result("unique", ["testdir/new.xlsx"]));
    await f.navigation.open(named("new.xlsx"), initial);
    expect(f.openFile.mock.lastCall![0].path).toBe("agents/a/testdir/new.xlsx");
    expect(f.navigation.choice.value).toBeNull();
  });

  it("rejects stale origins and noncanonical destinations before any lookup", () => {
    const f = setup();
    f.navigation.open(named("a.txt"), { ...initial, conversationId: "old-chat" });
    f.navigation.open(named("../a.txt"), initial);
    f.navigation.open(named("/etc/a.txt"), initial);
    expect(f.resolve).not.toHaveBeenCalled();
    expect(f.openFile).not.toHaveBeenCalled();
  });

  it("refuses escaping, mismatched and noncanonical response paths", async () => {
    for (const path of ["../other/a.txt", "/other/a.txt", "one/../a.txt", "wrong.txt"]) {
      const f = setup();
      f.resolve.mockResolvedValue(result("unique", [path]));
      await f.navigation.open(named("a.txt"), initial);
      expect(f.openFile).not.toHaveBeenCalled();
      expect(f.navigation.choice.value?.error).toBeTruthy();
    }
  });
});
