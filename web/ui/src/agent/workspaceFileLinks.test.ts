import { describe, expect, it } from "vitest";
import { resolvedWorkspaceFileTarget, workspaceFileMatches, workspaceFileTarget, type WorkspaceFileContext } from "./workspaceFileLinks";

const context: WorkspaceFileContext = { cwd: "/home/pogo/users/alice", workspace: "users/alice", root: "/home/pogo" };
const target = (raw: string, mode: "literal" | "href" = "literal") => workspaceFileTarget(raw, context, mode);

describe("workspaceFileTarget", () => {
  it("maps only absolute paths below the exact declared cwd", () => {
    expect(target("/home/pogo/users/alice/reports/result.xlsx")).toEqual({ path: "reports/result.xlsx", name: "result.xlsx" });
    for (const raw of ["/home/pogo/users/alice2/result.xlsx", "/home/pogo/users/bob/result.xlsx", "/tmp/result.xlsx", "/etc/passwd", "/home/pogo/users/alice"]) expect(target(raw), raw).toBeNull();
  });

  it("keeps relative paths inside cwd and normalizes only ordinary path segments", () => {
    expect(target("./reports//draft/../result.xlsx")).toEqual({ path: "reports/result.xlsx", name: "result.xlsx" });
    for (const raw of ["../result.xlsx", "reports/../../result.xlsx", "/home/pogo/users/alice/../bob/result.xlsx", "users/alice/../bob/result.xlsx", "reports/result.xlsx/", "reports/result.xlsx/.", "reports/result.xlsx/subdir/.."]) expect(target(raw), raw).toBeNull();
  });

  it("strips only exact declared service-relative prefixes, once", () => {
    expect(target("users/alice/reports/result.xlsx")?.path).toBe("reports/result.xlsx");
    expect(target("users/alice/users/alice/result.xlsx")?.path).toBe("users/alice/result.xlsx");
    for (const raw of ["users/bob/result.xlsx", "users/alice2/result.xlsx"]) expect(target(raw), raw).toBeNull();
    expect(target("./users/bob/result.xlsx")?.path).toBe("users/bob/result.xlsx");
    expect(workspaceFileTarget("users/alice/result.xlsx", { cwd: context.cwd })?.path).toBe("users/alice/result.xlsx");
    expect(workspaceFileTarget("users/alice/result.xlsx", { cwd: context.cwd, root: "/home/pogo" })?.path).toBe("result.xlsx");
    expect(workspaceFileTarget("users/alice/result.xlsx", { cwd: context.cwd, root: "/different" })?.path).toBe("users/alice/result.xlsx");
    const nested = { cwd: "/home/pogo/users/alice/project", root: "/home/pogo" };
    expect(workspaceFileTarget("users/alice/project/result.xlsx", nested)?.path).toBe("result.xlsx");
    expect(workspaceFileTarget("users/bob/result.xlsx", nested)).toBeNull();
    expect(workspaceFileTarget("users/alice/other/result.xlsx", nested)).toBeNull();
  });

  it("never guesses ownership from a shared path suffix", () => {
    const scoped = { cwd: "/srv/workspaces/team-one", workspace: "workspaces/team-one" };
    expect(workspaceFileTarget("workspaces/team-two/output/report.docx", scoped)).toBeNull();
    expect(workspaceFileTarget("/other/workspaces/team-one/output/report.docx", scoped)).toBeNull();
    expect(workspaceFileTarget("/srv/workspaces/team-one/output/report.docx", scoped)?.path).toBe("output/report.docx");
  });

  it("preserves Chinese names, spaces, parentheses, percent signs, hash, case and Unicode identity", () => {
    for (const name of ["中文目录/报告 (最终版).xlsx", "reports/100%完成#原样.xlsx", "reports/report.xlsx#原样", "reports/My Report.PDF", "reports/cafe\u0301.txt", "reports/café.txt", "reports/%E4%B8%AD.xlsx", "reports/%2F..%2F.xlsx"]) {
      expect(target(name)?.path, name).toBe(name);
    }
    expect(target("reports/Case.txt")?.path).not.toBe(target("reports/case.txt")?.path);
    expect(target("reports/cafe\u0301.txt")?.path).not.toBe(target("reports/café.txt")?.path);
  });

  it("accepts complete inline-code basenames and dotfiles without inventing an index", () => {
    for (const raw of ["report.xlsx", ".env", ".gitignore", ".config.json", "Dockerfile", "README", "src/main.ts", "archive.tar.gz", "diagrams/flow.mmd", "diagrams/flow.mermaid"]) expect(target(raw), raw).not.toBeNull();
    for (const raw of ["README-ish", "some-directory", ".", "..", "version.123", "domain.example"]) expect(target(raw), raw).toBeNull();
  });

  it("keeps bare filename lookup distinct from explicit paths that normalize to that name", () => {
    expect(target("sample-a.txt")).toEqual({ path: "sample-a.txt", name: "sample-a.txt", lookupByName: true });
    for (const raw of ["./sample-a.txt", "testdir/../sample-a.txt", "/home/pogo/users/alice/sample-a.txt", "users/alice/sample-a.txt"]) {
      expect(target(raw), raw).toEqual({ path: "sample-a.txt", name: "sample-a.txt" });
    }
    expect(target("testdir/sample-a.txt")).toEqual({ path: "testdir/sample-a.txt", name: "sample-a.txt" });
    expect(target("file:///home/pogo/users/alice/sample-a.txt", "href")).toEqual({ path: "sample-a.txt", name: "sample-a.txt" });
  });

  it("preserves lookup provenance after decoding a bare href and separating its line locator", () => {
    expect(target("My%20Report.xlsx", "href")).toEqual({ path: "My Report.xlsx", name: "My Report.xlsx", lookupByName: true });
    expect(target("%E4%B8%AD%E6%96%87.ts#L2-L8", "href")).toEqual({ path: "中文.ts", name: "中文.ts", lookupByName: true, lineStart: 2, lineEnd: 8 });
    expect(target("./%E4%B8%AD%E6%96%87.ts#L2-L8", "href")).toEqual({ path: "中文.ts", name: "中文.ts", lineStart: 2, lineEnd: 8 });
    expect(target("notes.txt%23L12", "href")).toEqual({ path: "notes.txt#L12", name: "notes.txt#L12", lookupByName: true });
  });

  it("supports unambiguous positive inclusive line locators", () => {
    expect(target("src/main.ts:12")).toEqual({ path: "src/main.ts", name: "main.ts", lineStart: 12 });
    expect(target("main.ts:12")).toEqual({ path: "main.ts", name: "main.ts", lookupByName: true, lineStart: 12 });
    expect(target("src/main.ts:12-20")).toEqual({ path: "src/main.ts", name: "main.ts", lineStart: 12, lineEnd: 20 });
    for (const suffix of ["#L12-L20", "#L12-20"]) expect(target(`src/main.ts${suffix}`)).toEqual({ path: "src/main.ts", name: "main.ts", lineStart: 12, lineEnd: 20 });
    expect(target("src/.env#L2")?.lineStart).toBe(2);
    expect(target("src/note#L12.txt")?.path).toBe("src/note#L12.txt");
  });

  it("rejects invalid, reversed or unsafe line ranges without silently choosing another line", () => {
    for (const raw of ["src/main.ts:0", "src/main.ts:20-12", "src/main.ts#L0", "src/main.ts#L20-L12", "src/main.ts:9007199254740992", "src/main.ts:2:4", "src/main.ts:1.5", "src/main.ts:-2"]) expect(target(raw), raw).toBeNull();
  });

  it("decodes href path segments once, preserving literal and URL semantics separately", () => {
    expect(target("reports/My%20Report%23final.xlsx", "href")?.path).toBe("reports/My Report#final.xlsx");
    expect(target("reports/My%20Report%23final.xlsx")?.path).toBe("reports/My%20Report%23final.xlsx");
    expect(target("reports/%252e%252e.xlsx", "href")?.path).toBe("reports/%2e%2e.xlsx");
    expect(target("reports/%E4%B8%AD%E6%96%87.xlsx", "href")?.path).toBe("reports/中文.xlsx");
    expect(target("src/main%2Ets#L3-L5", "href")).toEqual({ path: "src/main.ts", name: "main.ts", lineStart: 3, lineEnd: 5 });
    expect(target("reports/name%23L12.xlsx", "href")?.path).toBe("reports/name#L12.xlsx");
  });

  it("only accepts local scoped file hrefs without remote authorities or URL traversal", () => {
    expect(target("file:///home/pogo/users/alice/reports/My%20Report.xlsx", "href")?.path).toBe("reports/My Report.xlsx");
    for (const raw of ["file:///home/pogo/users/bob/a.txt", "file://localhost/home/pogo/users/alice/a.txt", "file://host/share/a.txt", "file:relative.txt", "file:////home/pogo/users/alice/a.txt", "//host/a.txt", "file:///home/pogo/users/alice/%2e%2e/bob/a.txt"]) expect(target(raw, "href"), raw).toBeNull();
    expect(target("file:///home/pogo/users/alice/a.txt")).toBeNull();
  });

  it("rejects encoded separators, encoded dot traversal, malformed escapes and URL control bytes", () => {
    for (const raw of ["reports%2fa.xlsx", "reports%5Ca.xlsx", "reports/%2E%2e/a.xlsx", "reports/.%2e/a.xlsx", "%2e/a.xlsx", "reports/a%00.xlsx", "reports/a%0A.xlsx", "reports/a%ZZ.xlsx", "reports/a%EF.xlsx", "reports/a.xlsx?download=1", "reports/a.xlsx#other"]) expect(target(raw, "href"), raw).toBeNull();
  });

  it("rejects protocols, foreign OS paths, command text, operators, XSS and truncated paths", () => {
    for (const raw of [
      "https://example.com/a.txt", "http://example.com/a.txt", "javascript:alert(1).txt", "data:text/plain,a.txt", "mailto:user@example.txt", "tool://files/a.txt",
      "C:/Users/alice/report.xlsx", "C:\\Users\\alice\\report.xlsx", "\\\\server\\share\\file.txt", "~/report.xlsx",
      "cat ./src/main.ts", "python src/main.py", "sudo rm reports/result.xlsx", "npm run docs/report.txt", "src/a.txt && cat src/b.txt", "src/a.txt;echo", "$(pwd)/report.xlsx", "src/*.txt", "<img src=x onerror=alert(1)>.txt",
      ".../report.xlsx", "reports/…/report.xlsx", "reports/.../report.xlsx", "reports/a\n.xlsx", "reports/a\u0000.xlsx", "reports/a\u202E.xlsx",
    ]) expect(target(raw), raw).toBeNull();
  });

  it("requires a declared absolute POSIX cwd", () => {
    for (const cwd of ["", "relative", "C:/workspace", "//server/share", "/home/../other", "/home\u0000/other"]) expect(workspaceFileTarget("report.xlsx", { cwd }), cwd).toBeNull();
    expect(workspaceFileTarget("/report.txt", { cwd: "/" })?.path).toBe("report.txt");
  });
});

describe("resolvedWorkspaceFileTarget", () => {
  it("preserves already-resolved local nested namespaces and decoded filename characters", () => {
    for (const path of ["users/alice/report.xlsx", "users/bob/report.xlsx", "notes.txt#L12", "notes%23L12.txt", "reports/%2e%2e.xlsx"]) {
      expect(resolvedWorkspaceFileTarget(path, context)).toEqual({ path, name: path.slice(path.lastIndexOf("/") + 1) });
    }
    expect(target("./users/alice/report.xlsx")?.path).toBe("users/alice/report.xlsx");
    expect(target("notes.txt%23L12", "href")?.path).toBe("notes.txt#L12");
  });

  it("rejects noncanonical, absolute and escaping destinations without normalizing them", () => {
    for (const path of ["/home/pogo/users/alice/a.txt", "../a.txt", "a/../b.txt", "./a.txt", "a//b.txt", "a.txt/", "a.txt:12", "javascript:alert.txt", "a\u0000.txt", "cat a.txt"]) {
      expect(resolvedWorkspaceFileTarget(path, context), path).toBeNull();
    }
    expect(resolvedWorkspaceFileTarget("a.txt", { cwd: "relative" })).toBeNull();
  });
});

describe("workspaceFileMatches", () => {
  it("recognizes explicit paths while leaving ordinary basename mentions alone", () => {
    const text = "结果在 reports/分析.xlsx，源码见 src/main.ts:12。另一个 report.xlsx 只是文件名。";
    const matches = workspaceFileMatches(text, context);
    expect(matches.map(m => text.slice(m.start, m.end))).toEqual(["reports/分析.xlsx", "src/main.ts:12"]);
    expect(matches.map(m => m.target.path)).toEqual(["reports/分析.xlsx", "src/main.ts"]);
  });

  it("recognizes quoted paths with spaces and balanced filename parentheses", () => {
    const text = '保存到“reports/季度 报告(最终版).xlsx”，或者 "新报告.docx"；参考 (src/readme(final).md)。';
    const matches = workspaceFileMatches(text, context);
    expect(matches.map(m => text.slice(m.start, m.end))).toEqual(["reports/季度 报告(最终版).xlsx", "新报告.docx", "src/readme(final).md"]);
  });

  it("uses UTF-16 offsets and preserves the complete original prose", () => {
    const text = "😀 文件：src/中文.ts#L2-L4 和 ./输出/报告.pdf!";
    const matches = workspaceFileMatches(text, context);
    expect(matches.map(m => text.slice(m.start, m.end))).toEqual(["src/中文.ts#L2-L4", "./输出/报告.pdf"]);
    expect(matches[0]?.target).toEqual({ path: "src/中文.ts", name: "中文.ts", lineStart: 2, lineEnd: 4 });
  });

  it("does not extract a URL suffix, foreign path or embedded HTML attribute", () => {
    const text = 'https://host/src/a.ts /home/pogo/users/bob/report.xlsx users/bob/report.xlsx <img src="reports/a.png">';
    expect(workspaceFileMatches(text, context)).toEqual([]);
  });

  it("rejects shell command lines and does not parse raw fenced or inline code", () => {
    const text = 'cat ./src/a.ts\n```ts\nsrc/b.ts\n```\n`src/c.ts`\n~~~sh\nsrc/d.ts\n~~~\nreports/a.xlsx && reports/b.xlsx\n结果在 src/real.ts';
    expect(workspaceFileMatches(text, context).map(m => m.target.path)).toEqual(["src/real.ts"]);
  });

  it("keeps Markdown parsing responsibility at the integration boundary", () => {
    // The plugin invokes this helper on text tokens only. Code uses target()
    // directly; existing links/fences/HTML are never passed through the plugin.
    expect(target("报告.xlsx")?.path).toBe("报告.xlsx");
    expect(workspaceFileMatches("报告.xlsx", context)).toEqual([]);
    expect(workspaceFileMatches("[label](https://example.com/a.txt)", context)).toEqual([]);
  });

  it("handles long nonmatches and many segments without changing identity", () => {
    const path = `${"folder/".repeat(5000)}report.xlsx`;
    expect(target(path, "href")?.path).toBe(path);
    const text = `${"ordinary ".repeat(5000)}${path}`;
    expect(workspaceFileMatches(text, context)).toEqual([{ start: text.length - path.length, end: text.length, target: { path, name: "report.xlsx" } }]);
  });
});
