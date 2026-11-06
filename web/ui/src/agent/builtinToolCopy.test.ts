import { afterEach, describe, expect, it } from "vitest";
import { computed } from "vue";
import { setLocale } from "@/i18n";
import { builtinToolDescription, builtinSkillDescription } from "./builtinToolCopy";

afterEach(() => setLocale("zh-CN"));

describe("native tool catalog language", () => {
  it("reacts to locale changes even when the host returns a Chinese description", () => {
    setLocale("zh-CN");
    const description = computed(() => builtinToolDescription("read", "读取工作区文件内容"));
    expect(description.value).toBe("读取工作区文件内容");
    setLocale("en");
    expect(description.value).toBe("Read workspace file contents");
    expect(builtinToolDescription("shell_output", "分页读取本会话受管 Shell 输出；随 bash 权限提供")).toContain("Shell output");
  });

  it("preserves descriptions from unknown tools rather than rewriting external content", () => {
    setLocale("en");
    expect(builtinToolDescription("mcp__docs__read", "团队知识库查询说明")).toBe("团队知识库查询说明");
    expect(builtinToolDescription("custom_action", "User-authored description")).toBe("User-authored description");
  });

  it("localizes framework Skill summaries while preserving an installed Skill with the same name", () => {
    setLocale("en");
    expect(builtinSkillDescription("office", "Office 中文说明", true)).toContain("built-in Office");
    expect(builtinSkillDescription("office", "团队自己的说明", false)).toBe("团队自己的说明");
    expect(builtinSkillDescription("custom-skill", "Custom Skill", true)).toBe("Custom Skill");
  });
});
