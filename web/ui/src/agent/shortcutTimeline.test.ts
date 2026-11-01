import { afterEach, describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import { summarizeArgs, targetOf } from "./timeline";
afterEach(() => setLocale("zh-CN"));
describe("shortcut organization timeline summaries", () => {
  it("describes operations without dumping nested content or request payloads", () => {
    setLocale("zh-CN");
    expect(summarizeArgs("shortcut_list", {})).toBe("浏览快捷方式 · 根目录");
    expect(summarizeArgs("shortcut_folder_create", { name: "数据库" })).toBe("创建文件夹 · 数据库");
    expect(summarizeArgs("shortcut_folder_rename", { id: "folder_one", name: "分析" })).toBe("重命名文件夹 · 分析");
    expect(summarizeArgs("shortcut_update", { id: "sh_one", graph: { PRIVATE: "do not expose" } })).toBe("更新快捷方式 · sh_one");
    expect(summarizeArgs("shortcut_move", { ids: ["one", "two"], parent_id: "f_one" })).toBe("移动快捷方式 · 2 项");
    expect(summarizeArgs("shortcut_delete", { ids: ["one"] })).toBe("删除快捷方式 · 1 项");
    expect(summarizeArgs("shortcut_merge", {})).toBe("预览重复项");
    expect(summarizeArgs("shortcut_merge", { apply: true })).toBe("合并重复快捷方式");
  });
  it("supports English and stable identity targets independently of rename text", () => {
    setLocale("en"); expect(summarizeArgs("shortcut_merge", {})).toBe("Preview duplicates"); expect(summarizeArgs("shortcut_move", { ids: ["one"] })).toBe("Move shortcuts · 1 item");
    expect(targetOf("shortcut_update", { id: "one", title: "new" })).toBe("one");
    expect(targetOf("shortcut_folder_rename", { id: "folder_one", name: "new" })).toBe("folder_one");
    expect(targetOf("shortcut_move", { ids: ["b", "a"] })).toBe(targetOf("shortcut_move", { ids: ["a", "b"] }));
    expect(targetOf("shortcut_merge", { plan_id: "sm_plan" })).toBe("sm_plan");
  });
});
