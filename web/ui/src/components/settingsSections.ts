// How the Studio top-level settings modules map onto the sheets the settings
// dialog already renders. One source of truth, so the legacy dialog (every
// group, loads every store on open) and the embedded Studio pages (one module
// at a time) cannot drift apart: nav visibility, module switching and
// cross-module links all ask these functions instead of restating the map.

export type SettingsSection = "models" | "mcp" | "account" | "general" | "memory";

// The dialog's own sheets, unchanged — embedded mode only chooses which are
// reachable, it never invents parallel pages or stores.
export type SettingsSheet =
  | "service"
  | "storage"
  | "sandbox"
  | "attach"
  | "icons"
  | "websites"
  | "memory"
  | "account"
  | "mcp"
  | "mcpServe"
  | "mcpMonitor";

export const SETTINGS_SHEETS: readonly SettingsSheet[] = [
  "service",
  "storage",
  "sandbox",
  "attach",
  "icons",
  "websites",
  "memory",
  "account",
  "mcp",
  "mcpServe",
  "mcpMonitor",
];

// Which Studio module each sheet belongs to. The three service anchors
// (providers / models / raw JSON) are one sheet, so they travel together.
// skillMonitor used to live under general; it moved to the dedicated Skill
// management page (/manage/skills), so it is no longer a settings sheet.
const SHEET_SECTION: Record<SettingsSheet, SettingsSection> = {
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

// Where a module opens: its first sheet in dialog nav order.
const SECTION_DEFAULT_SHEET: Record<SettingsSection, SettingsSheet> = {
  models: "service",
  mcp: "mcp",
  account: "account",
  general: "storage",
  memory: "memory",
};

// An embed without a section shows general — the least privileged module —
// rather than guessing at config pages the host never named.
export const DEFAULT_EMBEDDED_SECTION: SettingsSection = "general";

export function sectionOfSheet(sheet: SettingsSheet): SettingsSection {
  return SHEET_SECTION[sheet];
}

export function sheetsInSection(section: SettingsSection): SettingsSheet[] {
  return SETTINGS_SHEETS.filter((s) => SHEET_SECTION[s] === section);
}

export function defaultSheetForSection(section: SettingsSection): SettingsSheet {
  return SECTION_DEFAULT_SHEET[section];
}

// The dialog passes no section: null means "no module filter", every sheet
// reachable. An embed resolves to its section (defaulting to general).
export function normalizeEmbeddedSection(
  embedded: boolean,
  section?: SettingsSection | null,
): SettingsSection | null {
  if (!embedded) return null;
  return section ?? DEFAULT_EMBEDDED_SECTION;
}

// Nav visibility: a dialog shows every entry; an embed shows only its own
// module's subnavigation, because Studio's sidebar owns the module level.
export function navShowsSheet(sheet: SettingsSheet, active: SettingsSection | null): boolean {
  return active === null || SHEET_SECTION[sheet] === active;
}

// Clamp a sheet to a module: keep it when it belongs there (the skill → MCP
// trace link sets mcpMonitor, then the parent aligns the section and this
// must not bounce it back to the client-servers page), otherwise fall to the
// module's default sheet. null keeps the dialog's freedom.
export function sheetForSection(sheet: SettingsSheet, active: SettingsSection | null): SettingsSheet {
  if (active === null) return sheet;
  return SHEET_SECTION[sheet] === active ? sheet : SECTION_DEFAULT_SHEET[active];
}
