import { describe, expect, it } from "vitest";
import { openTab, restoreTabs, type DockTab } from "./dockSheets";
import { browserSheetVisible } from "@/agent/browserSheets";
const website: DockTab = { id: "website:session_one:ws_one", kind: "website", parentSession: "session_one", webServiceId: "ws_one", label: "Counter", closable: true };
describe("website dock references", () => {
  it.each(["https://bearer.preview.test/", `http://192.168.1.10:19770/_pogo/web/${"a".repeat(48)}/`])("restores the reference but drops credentials and the explicit-open instruction: %s", url => {
    const stored = { open: [{ ...website, url, preview_url: url, websiteOpenRequest: 123 }] };
    const restored = restoreTabs(stored, []);
    expect(restored.tabs).toEqual([website]); expect(JSON.stringify(restored)).not.toContain(url); expect(restored.tabs[0]?.websiteOpenRequest).toBeUndefined();
  });
  it("reuses the existing tab for a new explicit click and does not expose another conversation's tab", () => {
    const next = openTab([website], { ...website, websiteOpenRequest: 2 });
    expect(next).toHaveLength(1); expect(next[0]?.websiteOpenRequest).toBe(2);
    expect(browserSheetVisible(website, "session_one")).toBe(true); expect(browserSheetVisible(website, "session_two")).toBe(false);
    expect(restoreTabs({ open: [{ ...website, parentSession: "session_two" }] }, []).tabs).toEqual([]);
  });
});
