import { describe, expect, it } from "vitest";
import { isStudioDestination, isStudioPath, parseStudioRoute, studioAgentId, workbenchRedirect } from "./studioRoutes";

describe("Studio entry surfaces", () => {
  it("gives workspace modules their own reloadable locations", () => {
    expect(parseStudioRoute("/manage/")).toEqual({ kind: "manage", section: "agents" });
    for (const section of ["information", "models", "mcp", "skills", "account", "settings", "memory"]) {
      expect(parseStudioRoute(`/manage/${section}`)).toEqual({ kind: "manage", section });
    }
  });



  it("distinguishes a managed preview from the published and embedded views", () => {
    expect(parseStudioRoute("/manage/agents/ag_1/chat", "?embed=1")).toEqual({
      kind: "chat", agentId: "ag_1", managedChat: true, embedded: false,
    });
    expect(parseStudioRoute("/a/ag_1", "?c=s1")).toEqual({
      kind: "chat", agentId: "ag_1", managedChat: false, embedded: false,
    });
    expect(parseStudioRoute("/a/ag_1", "?embed=1&c=s1")).toEqual({
      kind: "chat", agentId: "ag_1", managedChat: false, embedded: true,
    });
    expect(studioAgentId("/manage/agents/ag_1/chat")).toBe("ag_1");
  });

  it("keeps malformed Agent and Studio links out of the generic workbench", () => {
    for (const path of ["/a", "/a/", "/a/ag_1/other", "/manage/unknown", "/manage/agents/ag_1", "/manage/workflows/wf_1/extra"]) {
      expect(isStudioPath(path)).toBe(true);
      expect(parseStudioRoute(path)?.kind).toBe("invalid");
      expect(isStudioDestination(path)).toBe(false);
    }
    expect(parseStudioRoute("/share/123")).toBeNull();
  });

  it("treats the root as the home address of the agents surface", () => {
    expect(parseStudioRoute("/")).toEqual({ kind: "manage", section: "agents" });
    expect(parseStudioRoute("")).toEqual({ kind: "manage", section: "agents" });
    expect(parseStudioRoute("/manage")).toEqual({ kind: "manage", section: "agents" });
  });

  it("accepts only known local navigation destinations", () => {
    for (const path of ["/manage", "/manage/settings", "/manage/memory?agent=ag_1", "/manage/agents/ag_1/chat?c=s1", "/a/ag_1?embed=1"]) {
      expect(isStudioDestination(path)).toBe(true);
    }
    for (const path of ["//example.com/a/x", "https://example.com/manage", "javascript:alert(1)", "/manage\\evil", "/manage\n", "/manage/../a/x", "/a/a%2Fx", "/a/x#invalid"]) {
      expect(isStudioDestination(path)).toBe(false);
    }
  });

  it("closes the agent-less workbench: stray addresses land on the home route", () => {
    for (const path of ["/foo", "/foo/bar", "/index.html", "/embed", "/embed/foo", "/anything-else"]) {
      expect(workbenchRedirect(path)).toBe("/");
    }
  });

  it("keeps the surfaces that still own the app shell", () => {
    for (const path of [
      "/", "/manage", "/manage/mcp", "/manage/agents/ag_1/chat",
      "/a/ag_1", "/a/", "/manage/unknown", // studio 路由(含 invalid 兜底页)
      "/share/abc", // 公开分享页
      "/embed/", // 委托嵌入的精确挂载点
    ]) {
      expect(workbenchRedirect(path)).toBeNull();
    }
  });
});
