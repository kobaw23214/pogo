import { describe, expect, it } from "vitest";
import { detectLanguage } from "./highlight";
import { shortcutLanguages, shortcutLanguage, validShortcutLanguage, shortcutSearchPreview, shortcutSearchSegments, shortcutTextMatches } from "./shortcutPresentation";

describe("shortcut syntax presentation", () => {
  it("defaults old snapshots to text and only offers real existing scanners", () => {
    expect(shortcutLanguage(undefined)).toBe("text");
    expect(shortcutLanguage("<script>")).toBe("text");
    expect(validShortcutLanguage("<script>")).toBe(false);
    for (const language of shortcutLanguages) {
      expect(validShortcutLanguage(language)).toBe(true);
      expect(detectLanguage(language)).toBe(language === "text" ? "plain" : language === "yml" ? "yaml" : language === "bash" ? "shell" : language);
    }
  });
});
describe("literal shortcut search", () => {
  it("highlights English without case sensitivity, Chinese and regex punctuation literally", () => {
    for (const [text, query, expected] of [["SQL select SQL", "sql", ["SQL", "SQL"]], ["资料中的数据库", "数据库", ["数据库"]], ["host=[a+b].invalid [axb]", "[a+b].", ["[a+b]."]]]) {
      const segments = shortcutSearchSegments(text as string, query as string);
      expect(segments.filter(part => part.match).map(part => part.text)).toEqual(expected);
      expect(segments.map(part => part.text).join("")).toBe(text);
    }
  });
  it("shows a late matching content excerpt and preserves Unicode text boundaries", () => {
    const content = "😀前言".repeat(150) + "深处的检索命中" + "🙂后文".repeat(150);
    const preview = shortcutSearchPreview(content, "检索命中");
    expect(preview).toContain("检索命中");
    expect(preview.startsWith("…")).toBe(true);
    expect(preview.endsWith("…")).toBe(true);
    expect([...preview.slice(0, preview.indexOf("检索命中"))].length).toBeLessThanOrEqual(13);
    expect([...preview].length).toBeLessThanOrEqual(152);
    expect(preview).not.toMatch(/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/u);
  });
  it("returns plain text segments for empty search and never turns content into markup", () => {
    const text = '<img src=x onerror="window.injected=true">';
    expect(shortcutSearchSegments(text, "img")).toEqual([{ text: "<", match: false }, { text: "img", match: true }, { text: ' src=x onerror="window.injected=true">', match: false }]);
    expect(shortcutSearchSegments(text, "  ")).toEqual([{ text, match: false }]);
    expect(shortcutTextMatches("one\n two", "ONE two")).toBe(true);
    expect(shortcutTextMatches("data", "missing")).toBe(false);
    expect(shortcutTextMatches("data", "")).toBe(true);
  });
});
