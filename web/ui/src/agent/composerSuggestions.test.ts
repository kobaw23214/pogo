import { describe, expect, it } from "vitest";
import { composerSuggestionAction, composerSuggestions, moveComposerSuggestion } from "./composerSuggestions";

const commands = [{ name: "/compact", label: "压缩", description: "压缩当前对话" }];
const skills = [
  { name: "browser-use", description: "使用浏览器", source: "embedded" },
  { name: "docs", description: "处理文档", source: "project" },
];
const entry = { label: "使用技能", description: "选择技能" };
const options = (line: string) => composerSuggestions(line, commands, skills, entry);

describe("composer suggestions", () => {
  it("offers commands and the scoped skills together at slash", () => {
    expect(options("/").map(item => item.id)).toEqual(["command-compact", "skill-entry", "skill-browser-use", "skill-docs"]);
    expect(options("/").find(item => item.id === "skill-browser-use")?.origin).toBe("builtin");
    expect(options("/").find(item => item.id === "skill-docs")?.origin).toBe("external");
  });
  it("completes a command prefix and a direct skill prefix without execution", () => {
    expect(options("/COM").map(item => item.value)).toEqual(["/compact "]);
    expect(options("/bro").map(item => item.value)).toEqual(["/skill:browser-use "]);
  });
  it("preserves skill invocation syntax and its second selection step", () => {
    expect(options("/").find(item => item.id === "skill-entry")?.value).toBe("/skill:");
    expect(options("/skill").map(item => item.value)).toEqual(["/skill:browser-use ", "/skill:docs "]);
    expect(options("/skill:").length).toBe(2);
    expect(options("/skill:DO").map(item => item.value)).toEqual(["/skill:docs "]);
  });
  it.each(["", "ordinary text", "read /compact", "/compact ", "/skill:docs task", "/compact\n", "/compact\t", "/no-match"])("does not interrupt %j", line => {
    expect(options(line)).toEqual([]);
  });
  it("never invents a skill when the scoped list is empty", () => {
    expect(composerSuggestions("/skill:", commands, [], entry)).toEqual([]);
  });
  it("finds the selected background Skill by Chinese entry names and inserts its canonical card", () => {
    const scoped = [{ name: "background-tasks", aliases: ["后台任务", "哨兵"], description: "创建和管理后台任务", source: "embedded" }];
    for (const query of ["/后台", "/哨兵", "/skill:后台任务", "/skill:哨", "/back"]) {
      expect(composerSuggestions(query, commands, scoped, entry)).toEqual([
        { id: "skill-background-tasks", kind: "skill", label: "background-tasks", description: "创建和管理后台任务", value: "/skill:background-tasks ", origin: "builtin" },
      ]);
    }
    expect(composerSuggestions("/后台", commands, skills, entry)).toEqual([]);
  });
  it("wraps keyboard selection and recovers when a selected result disappears", () => {
    const items = options("/");
    expect(moveComposerSuggestion(items, items[0].id, -1)).toBe(items[items.length - 1].id);
    expect(moveComposerSuggestion(items, items[items.length - 1].id, 1)).toBe(items[0].id);
    expect(moveComposerSuggestion(items, "removed", 1)).toBe(items[0].id);
    expect(moveComposerSuggestion([], null, 1)).toBeNull();
  });
});

describe("composer suggestion keys", () => {
  it("uses Enter and Tab for the same completion action", () => {
    expect(composerSuggestionAction({ key: "Enter" }, true)).toBe("complete");
    expect(composerSuggestionAction({ key: "Tab" }, true)).toBe("complete");
  });
  it("selects with arrows and dismisses with Escape", () => {
    expect(composerSuggestionAction({ key: "ArrowDown" }, true)).toBe("next");
    expect(composerSuggestionAction({ key: "ArrowUp" }, true)).toBe("previous");
    expect(composerSuggestionAction({ key: "Escape" }, true)).toBe("dismiss");
  });
  it("allows native editing and focus traversal outside a menu", () => {
    expect(composerSuggestionAction({ key: "Tab" }, false)).toBeNull();
    expect(composerSuggestionAction({ key: "ArrowUp" }, false)).toBeNull();
    expect(composerSuggestionAction({ key: "Enter", shiftKey: true }, true)).toBeNull();
    expect(composerSuggestionAction({ key: "Tab", shiftKey: true }, true)).toBeNull();
    expect(composerSuggestionAction({ key: "ArrowUp", ctrlKey: true }, true)).toBeNull();
  });
  it("leaves IME confirmation alone", () => {
    expect(composerSuggestionAction({ key: "Enter", isComposing: true }, true)).toBeNull();
    expect(composerSuggestionAction({ key: "Enter", keyCode: 229 }, true)).toBeNull();
  });
  it("does not send a completed command when Enter is held", () => {
    expect(composerSuggestionAction({ key: "Enter", repeat: true }, true)).toBe("ignore");
    expect(composerSuggestionAction({ key: "Enter", repeat: true }, false)).toBe("ignore");
  });
});

describe("MCP composer suggestions", () => {
  const mcpEntry = { label: "使用 MCP 方法", description: "选择服务及方法" };
  const methods = [
    { server: "gitlab", name: "list_issues", description: "查询议题" },
    { server: "gitlab", name: "create_issue", description: "创建议题" },
    { server: "docs", name: "search", description: "Search documents" },
  ];
  const available = (line: string) => composerSuggestions(line, commands, skills, entry, methods, mcpEntry);

  it("adds a discoverable MCP entry without flooding the root menu with every method", () => {
    expect(available("/").map(item => item.id)).toEqual(["command-compact", "skill-entry", "skill-browser-use", "skill-docs", "mcp-entry"]);
    expect(available("/mcp")).toEqual([{ id: "mcp-entry", kind: "mcp-entry", label: mcpEntry.label, description: mcpEntry.description, value: "/mcp:", shortcut: "/mcp:" }]);
    expect(available("/MCP:").map(item => item.label)).toEqual(["gitlab/list_issues", "gitlab/create_issue", "docs/search"]);
    expect(composerSuggestions("/mcp", [], [{ name: "mcp-helper", description: "Skill", source: "project" }], entry, methods, mcpEntry).map(item => item.kind)).toEqual(["mcp-entry"]);
  });

  it("searches exact service identities and method or description fragments without executing", () => {
    expect(available("/mcp:GIT").map(item => item.value)).toEqual(["/mcp:gitlab/list_issues ", "/mcp:gitlab/create_issue "]);
    expect(available("/mcp:create")[0]?.mcp).toEqual({ server: "gitlab", name: "create_issue" });
    expect(available("/mcp:查询").map(item => item.value)).toEqual(["/mcp:gitlab/list_issues "]);
    expect(available("/mcp:gitlab/LIST").map(item => item.value)).toEqual(["/mcp:gitlab/list_issues "]);
    expect(available("/mcp:docs/").map(item => item.value)).toEqual(["/mcp:docs/search "]);
    expect(available("/mcp:docs/list")).toEqual([]);
  });

  it("never invents methods outside the scoped catalog or merges different servers", () => {
    expect(composerSuggestions("/mcp:", commands, skills, entry, [], mcpEntry)).toEqual([]);
    const sameName = [{ server: "one", name: "search", description: "one" }, { server: "two", name: "search", description: "two" }];
    const items = composerSuggestions("/mcp:", commands, [], entry, [...sameName, sameName[0]], mcpEntry);
    expect(items.map(item => item.mcp)).toEqual([{ server: "one", name: "search" }, { server: "two", name: "search" }]);
    expect(new Set(items.map(item => item.id)).size).toBe(2);
  });

  it("encodes names with separators unambiguously and handles incomplete escapes", () => {
    const scoped = [{ server: "内部/文档", name: "find item/%", description: "搜索" }];
    const get = (line: string) => composerSuggestions(line, [], [], entry, scoped, mcpEntry);
    const result = get("/mcp:")[0];
    expect(result?.value).toBe(`/mcp:${encodeURIComponent("内部/文档")}/${encodeURIComponent("find item/%")} `);
    expect(result?.mcp).toEqual({ server: "内部/文档", name: "find item/%" });
    expect(get(`/mcp:${encodeURIComponent("内部/文档")}/find%20item`)).toEqual([result]);
    expect(() => get("/mcp:%E")).not.toThrow();
    expect(get("/mcp:%E")).toHaveLength(1);
  });

  it.each(["read /mcp:", "/mcp:gitlab/list_issues task", "/mcp:gitlab/list_issues\n", "https://host/mcp:"])("leaves prose and completed invocations alone: %j", line => {
    expect(available(line)).toEqual([]);
  });
});
