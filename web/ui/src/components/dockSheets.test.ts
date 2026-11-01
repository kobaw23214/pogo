import { describe, expect, it } from "vitest";
import {
  closeTab,
  migrateSheet,
  openTab,
  resolveTab,
  restoreTabs,
  type DockTab,
} from "./dockSheets";

// The migration is the part that can silently ruin a returning user's layout,
// so every id any shipped build could have written gets a case.
describe("migrateSheet", () => {
  it("keeps the independent shortcuts and skills sheets", () => {
    expect(migrateSheet({ sheet: "files" })).toEqual({ sheet: "files", tab: null });
    expect(migrateSheet({ sheet: "hub" })).toEqual({ sheet: "hub", tab: null });
    expect(migrateSheet({ sheet: "skills" })).toEqual({ sheet: "skills", tab: null });
    expect(migrateSheet({ sheet: "shortcuts" })).toEqual({ sheet: "shortcuts", tab: null });
    expect(migrateSheet({ sheet: "workflows" })).toEqual({ sheet: "workflows", tab: null });
  });

  it("moves the pre-hub shell sheet into the hub, onto the shell tab", () => {
    expect(migrateSheet({ sheet: "shell" })).toEqual({ sheet: "hub", tab: "shell" });
  });

  it("moves a pre-hub panel sheet into the hub, keeping which panel", () => {
    expect(migrateSheet({ sheet: "panel:样本库" })).toEqual({
      sheet: "hub",
      tab: "panel:样本库",
    });
  });

  it("migrates the pre-sheet booleans", () => {
    expect(migrateSheet({ sheet: null, legacyFiles: true })).toEqual({
      sheet: "files",
      tab: null,
    });
    expect(migrateSheet({ sheet: null, legacyShell: true })).toEqual({
      sheet: "hub",
      tab: "shell",
    });
  });

  it("prefers the sheet id over the legacy booleans", () => {
    expect(migrateSheet({ sheet: "files", legacyShell: true }).sheet).toBe("files");
  });

  it("opens nothing for an unknown id or a clean slate", () => {
    expect(migrateSheet({ sheet: "from-a-newer-build" })).toEqual({ sheet: null, tab: null });
    expect(migrateSheet({ sheet: null })).toEqual({ sheet: null, tab: null });
  });

  it("is idempotent: migrating its own output changes nothing", () => {
    const once = migrateSheet({ sheet: "shell" });
    expect(migrateSheet({ sheet: once.sheet })).toEqual({ sheet: "hub", tab: null });
  });
});

const panels = [{ name: "样本库", path: "/panels/样本库/" }];
const ws: DockTab[] = [
  { id: "shell", label: "Shell", kind: "shell", closable: true },
  { id: "panel:样本库", label: "样本库", kind: "panel", path: "/panels/样本库/", closable: true },
];
const subTab: DockTab = {
  id: "subagent:20260101T000000Z-abcd1234",
  label: "探索核心架构",
  kind: "subagent",
  childSession: "20260101T000000Z-abcd1234",
  closable: true,
};

describe("openTab", () => {
  it("appends a new tab and focuses nothing by itself", () => {
    const next = openTab(ws, subTab);
    expect(next.map((t) => t.id)).toEqual([...ws.map((t) => t.id), subTab.id]);
  });

  it("is a no-op when the tab is already open: an anchor clicked twice must not stack", () => {
    const once = openTab(ws, subTab);
    expect(openTab(once, subTab)).toBe(once);
  });

  it("updates an existing citation preview and reveals the same range again on another click", () => {
    const preview: DockTab = { id: "preview:main.go", label: "main.go", kind: "preview", path: "main.go", closable: true, citation: true, lineStart: 10, lineEnd: 12 };
    const once = openTab(ws, preview);
    const moved = openTab(once, { ...preview, lineStart: 20, lineEnd: 24 });
    expect(moved).toHaveLength(once.length);
    expect(moved[moved.length - 1]).toMatchObject({ lineStart: 20, lineEnd: 24, revealVersion: 2 });
    const repeated = openTab(moved, { ...preview, lineStart: 20, lineEnd: 24 });
    expect(repeated[repeated.length - 1]).toMatchObject({ lineStart: 20, lineEnd: 24, revealVersion: 3 });
    expect(repeated[0]).toBe(ws[0]);
  });

  it("clears a prior citation range when the next source only identifies the file", () => {
    const preview: DockTab = { id: "preview:main.go", label: "main.go", kind: "preview", path: "main.go", closable: true, citation: true };
    const once = openTab(ws, { ...preview, lineStart: 10, lineEnd: 12 });
    const next = openTab(once, preview);
    expect(next[next.length - 1]?.lineStart).toBeUndefined();
    expect(next[next.length - 1]?.lineEnd).toBeUndefined();
  });

  it("reveals answer file line links in an existing preview without treating them as citations", () => {
    const file: DockTab = { id: "preview:notes.txt", label: "notes.txt", kind: "preview", path: "notes.txt", closable: true };
    const once = openTab(ws, file);
    const next = openTab(once, { ...file, lineStart: 2, lineEnd: 3 });
    expect(next).toHaveLength(once.length);
    expect(next[next.length - 1]).toMatchObject({ lineStart: 2, lineEnd: 3, revealVersion: 1 });
    expect(next[next.length - 1]?.citation).toBeUndefined();
    const repeated = openTab(next, { ...file, lineStart: 2, lineEnd: 3 });
    expect(repeated[repeated.length - 1]?.revealVersion).toBe(2);
  });
});

describe("closeTab", () => {
  const withSub = openTab(ws, subTab);

  it("removes the tab and moves the active one to the left neighbour", () => {
    const { tabs, active } = closeTab(withSub, subTab.id, subTab.id);
    expect(tabs.map((t) => t.id)).toEqual(ws.map((t) => t.id));
    expect(active).toBe("panel:样本库");
  });

  it("keeps the active tab when closing a background one", () => {
    const { tabs, active } = closeTab(withSub, "shell", subTab.id);
    expect(tabs).toHaveLength(2);
    expect(active).toBe("shell");
  });

  it("closing the last tab empties the workspace, it does not resurrect anything", () => {
    const { active, tabs } = closeTab([subTab], subTab.id, subTab.id);
    expect(active).toBeNull();
    expect(tabs).toEqual([]);
  });
});

describe("resolveTab", () => {
  it("returns the remembered tab", () => {
    expect(resolveTab(ws, "panel:样本库")?.label).toBe("样本库");
  });

  it("falls back to the first tab when the remembered one is gone", () => {
    expect(resolveTab(ws, "panel:removed")?.id).toBe("shell");
    expect(resolveTab(ws, null)?.id).toBe("shell");
  });

  it("returns null only when there is nothing to show — the empty state", () => {
    expect(resolveTab([], "shell")).toBeNull();
  });
});

describe("restoreTabs — every open sheet is remembered state", () => {
  it("brings back shell, browser, panel and subagent tabs in stored order", () => {
    const browser: DockTab = { id: "browser:2", label: "浏览器", kind: "browser", url: "/x/", closable: true };
    const { tabs, active } = restoreTabs(
      { open: [ws[0], browser, ws[1], subTab], active: browser.id },
      panels,
    );
    expect(tabs.map((t) => t.id)).toEqual(["shell", "browser:session", subTab.id]);
    expect(active).toBe("browser:session");
  });

  it("keeps a browser view for an old panel even when registration disappeared", () => {
    const gone: DockTab = { id: "panel:gone", label: "gone", kind: "panel", path: "/panels/gone/", closable: true };
    const { tabs } = restoreTabs({ open: [gone, ws[0]] }, panels);
    expect(tabs.map((t) => t.id)).toEqual(["browser:session", "shell"]);
  });

  it("restores a preview tab from its path, id rebuilt under the prefix", () => {
    // An older build stored the tab whole; the restore must rebuild the id
    // from the path so the next openAttachmentPreview focuses it, not a twin.
    const stored: DockTab = { id: "preview:old", label: "报告.docx", kind: "preview", path: "uploads/报告.docx", closable: true };
    const { tabs } = restoreTabs({ open: [stored] }, panels);
    expect(tabs[tabs.length - 1]).toEqual({
      id: "preview:uploads/报告.docx",
      label: "报告.docx",
      kind: "preview",
      path: "uploads/报告.docx",
      closable: true,
    });
  });

  it("drops a preview tab stored without a path — there is nothing to show", () => {
    const stored = { id: "preview:x", label: "x", kind: "preview", closable: true } as DockTab;
    const { tabs } = restoreTabs({ open: [stored, ws[0]] }, panels);
    expect(tabs.map((t) => t.id)).toEqual(["shell"]);
  });

  it("normalises a stored id back onto the subagent prefix", () => {
    const stored: DockTab = { ...subTab, id: "subagent:other" };
    const { tabs } = restoreTabs({ open: [stored] }, panels);
    expect(tabs[tabs.length - 1].id).toBe("subagent:20260101T000000Z-abcd1234");
  });

  it("normalises legacy non-closable entries into closable workspace tabs", () => {
    const legacy = { ...ws[0], closable: false };
    const { tabs } = restoreTabs({ open: [legacy] }, panels);
    expect(tabs[0].closable).toBe(true);
  });

  it("lets the legacy single tenant pick the active tab", () => {
    const { active } = restoreTabs({ legacyTenant: "panel:样本库", open: ws }, panels);
    expect(active).toBe("browser:session");
  });

  it("falls back to the first tab when the remembered active one is gone", () => {
    const { active } = restoreTabs({ open: ws, active: "subagent:gone" }, panels);
    expect(active).toBe("shell");
  });

  it("an empty workspace stays empty: nothing is force-pinned back", () => {
    const { tabs, active } = restoreTabs({}, panels);
    expect(tabs).toEqual([]);
    expect(active).toBeNull();
  });
});


describe("retained subagent tab identity", () => {
  it("preserves the parent binding across restore rather than borrowing the current conversation", () => {
    const retained: DockTab = { ...subTab, agentId: "agent_1", parentSession: "parent_1" };
    const restored = restoreTabs({open:[retained]}, []).tabs[0];
    expect(restored.agentId).toBe("agent_1");
    expect(restored.parentSession).toBe("parent_1");
  });
  it("upgrades a legacy open transcript once a retained identity is available", () => {
    const upgraded=openTab([subTab],{...subTab,agentId:"agent_1",parentSession:"parent_1"});
    expect(upgraded).toHaveLength(1);expect(upgraded[0].agentId).toBe("agent_1");
  });
  it("drops malformed persisted routing identities", () => {
    const restored=restoreTabs({open:[{...subTab,agentId:"../other",parentSession:"parent?token=bad"}]},[]).tabs[0];
    expect(restored.agentId).toBeUndefined();expect(restored.parentSession).toBeUndefined();
  });
});
