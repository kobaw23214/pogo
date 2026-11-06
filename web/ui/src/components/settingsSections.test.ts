import { describe, expect, it } from "vitest";
import {
  DEFAULT_EMBEDDED_SECTION,
  SETTINGS_SHEETS,
  defaultSheetForSection,
  navShowsSheet,
  normalizeEmbeddedSection,
  sectionOfSheet,
  sheetForSection,
  sheetsInSection,
  type SettingsSection,
  type SettingsSheet,
} from "./settingsSections";

const SECTIONS: SettingsSection[] = ["models", "mcp", "account", "general", "memory"];

describe("sectionOfSheet", () => {
  it("maps every sheet to the Studio module that owns it", () => {
    const expected: Record<SettingsSheet, SettingsSection> = {
      service: "models",
      mcp: "mcp",
      mcpServe: "mcp",
      mcpMonitor: "mcp",
      account: "account",
      storage: "general",
      sandbox: "general",
      attach: "general",
      icons: "general",
      websites: "general",
      memory: "memory",
    };
    for (const sheet of SETTINGS_SHEETS) expect(sectionOfSheet(sheet)).toBe(expected[sheet]);
  });

  it("partitions the sheets: every sheet in exactly one section, none lost", () => {
    const grouped = SECTIONS.map(sheetsInSection);
    expect(grouped.flat().sort()).toEqual([...SETTINGS_SHEETS].sort());
    for (const list of grouped) {
      const set = new Set(list);
      expect(set.size).toBe(list.length);
    }
  });
});

describe("normalizeEmbeddedSection", () => {
  it("returns null for the legacy dialog no matter what section was passed", () => {
    expect(normalizeEmbeddedSection(false)).toBeNull();
    expect(normalizeEmbeddedSection(false, "mcp")).toBeNull();
  });

  it("keeps the embed's own section, defaulting to general when unnamed", () => {
    expect(normalizeEmbeddedSection(true, "models")).toBe("models");
    expect(normalizeEmbeddedSection(true)).toBe(DEFAULT_EMBEDDED_SECTION);
    expect(normalizeEmbeddedSection(true, null)).toBe("general");
  });
});

describe("navShowsSheet", () => {
  it("shows every sheet to the dialog (no active module)", () => {
    for (const sheet of SETTINGS_SHEETS) expect(navShowsSheet(sheet, null)).toBe(true);
  });

  it("shows an embed only its own module's subnavigation", () => {
    const cases: [SettingsSection, SettingsSheet[]][] = [
      ["models", ["service"]],
      ["mcp", ["mcp", "mcpServe", "mcpMonitor"]],
      ["account", ["account"]],
      ["general", ["storage", "sandbox", "attach", "icons", "websites"]],
      ["memory", ["memory"]],
    ];
    for (const [section, own] of cases) {
      const ownSet = new Set(own);
      for (const sheet of SETTINGS_SHEETS) {
        expect(navShowsSheet(sheet, section)).toBe(ownSet.has(sheet));
      }
    }
  });
});

describe("sheetForSection", () => {
  it("keeps a sheet that already belongs to the module", () => {
    for (const section of SECTIONS) {
      for (const sheet of sheetsInSection(section)) {
        expect(sheetForSection(sheet, section)).toBe(sheet);
      }
    }
  });

  it("keeps the skill trace target when the parent aligns the mcp module", () => {
    // The cross-link sets mcpMonitor first, then the section prop arrives.
    expect(sheetForSection("mcpMonitor", "mcp")).toBe("mcpMonitor");
  });

  it("falls back to the module's default sheet when the sheet is foreign", () => {
    expect(sheetForSection("mcpMonitor", "general")).toBe("storage");
    expect(sheetForSection("service", "account")).toBe("account");
    expect(sheetForSection("account", "models")).toBe("service");
  });

  it("keeps any sheet for the dialog (no active module)", () => {
    for (const sheet of SETTINGS_SHEETS) expect(sheetForSection(sheet, null)).toBe(sheet);
  });

  it("always lands on a sheet inside the target module", () => {
    for (const section of SECTIONS) {
      const inside = new Set(sheetsInSection(section));
      for (const sheet of SETTINGS_SHEETS) {
        expect(inside.has(sheetForSection(sheet, section))).toBe(true);
      }
      expect(inside.has(defaultSheetForSection(section))).toBe(true);
    }
  });
});
