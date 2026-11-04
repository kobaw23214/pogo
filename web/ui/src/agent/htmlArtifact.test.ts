import { describe, expect, it } from "vitest";
import { parseHTMLArtifact, resolveHTMLArtifact } from "./htmlArtifact";
import type { WorkspaceFileScope } from "./workspaceFileNavigation";

const scope: WorkspaceFileScope = { conversationId: "chat-a", cwd: "/home/pogo/users/alice/project", root: "/home/pogo", workspace: "users/alice/project" };

describe("HTML Artifact metadata", () => {
  it("accepts a path and optional title without executable HTML", () => {
    expect(parseHTMLArtifact('{"path":"artifacts/app.html","title":" Demo "}')).toEqual({ path: "artifacts/app.html", title: "Demo" });
    expect(parseHTMLArtifact('{"path":"artifacts/app.htm"}')).toEqual({ path: "artifacts/app.htm", title: undefined });
  });
  it("rejects malformed, empty and oversized metadata", () => {
    for (const source of ["", "null", "[]", "{}", '{"path":""}', '{"path":" app.html "}', '{"path":"app.html","title":" "}', '{"path":42}', "not json", `{"path":"${"a".repeat(5000)}.html"}`]) {
      expect(parseHTMLArtifact(source), source).toBeNull();
    }
  });
});

describe("HTML Artifact workspace resolution", () => {
  it("joins relative, absolute and workspace-prefixed entries to the current workspace", () => {
    for (const path of ["artifacts/app.html", "./artifacts/app.html", "/home/pogo/users/alice/project/artifacts/app.html", "users/alice/project/artifacts/app.html"]) {
      expect(resolveHTMLArtifact({ path, title: "Demo" }, scope)).toEqual({
        title: "Demo",
        target: { path: "artifacts/app.html", name: "app.html" },
        fullPath: "users/alice/project/artifacts/app.html",
      });
    }
    expect(resolveHTMLArtifact({ path: "app.html" }, { ...scope, workspace: "" })?.fullPath).toBe("app.html");
  });
  it("refuses non-HTML, external, traversing and unscoped entries", () => {
    for (const path of ["artifacts/app.txt", "https://example.invalid/app.html", "../app.html", "file:///etc/app.html"]) {
      expect(resolveHTMLArtifact({ path }, scope), path).toBeNull();
    }
    expect(resolveHTMLArtifact({ path: "app.html" }, null)).toBeNull();
  });
});
