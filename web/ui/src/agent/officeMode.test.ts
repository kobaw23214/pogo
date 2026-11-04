import { describe, expect, it } from "vitest";
import { withOfficeEditorMode } from "./officeMode";

describe("ONLYOFFICE view mode", () => {
  it("opens a read-only document without autosave and preserves the remaining config", () => {
    const original = {
      documentType: "word",
      editorConfig: { lang: "zh-CN", customization: { compactToolbar: true, autosave: true } },
    };
    const config = withOfficeEditorMode(original, true);
    expect(config).toEqual({
      documentType: "word",
      editorConfig: { lang: "zh-CN", mode: "view", customization: { compactToolbar: true, autosave: false } },
    });
    expect(original.editorConfig.customization.autosave).toBe(true);
    expect(original.editorConfig).not.toHaveProperty("mode");
  });
  it("restores edit mode for an explicit editing entry", () => {
    expect(withOfficeEditorMode({ editorConfig: { customization: {} } }, false)).toEqual({ editorConfig: { mode: "edit", customization: { autosave: true } } });
  });
});
