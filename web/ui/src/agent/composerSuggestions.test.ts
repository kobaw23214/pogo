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
