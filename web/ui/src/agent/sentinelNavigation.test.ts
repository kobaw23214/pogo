import { describe, expect, it } from "vitest";
import { requestedSentinelId, sentinelDeepLink, sentinelTab } from "./sentinelNavigation";
import type { SentinelSessionSummary } from "@/api/sentinelSessions";
import { openTab, restoreTabs } from "@/components/dockSheets";
import { browserSheetVisible } from "./browserSheets";

const watch: SentinelSessionSummary = { id: "watch-1", task_id: "sentinel:watch-1", agent_id: "ag_one", parent_session_id: "main-1", session_id: "worker-1", title: "目录观察", state: "listening", enabled: true, pending_events: 0 };
describe("Sentinel session navigation", () => {
  it("opens one task sheet attached to the main conversation, never an ordinary worker conversation", () => {
    const tab = sentinelTab(watch, "ag_one")!;
    expect(tab).toMatchObject({ id: "sentinel:watch-1", kind: "sentinel", sentinelId: "watch-1", parentSession: "main-1", agentId: "ag_one" });
    expect(tab.childSession).toBeUndefined();
    expect(openTab(openTab([], tab), { ...tab, label: "更新的标题" })).toHaveLength(1);
    expect(openTab([tab], { ...tab, label: "更新的标题" })[0]?.label).toBe("更新的标题");
  });
  it("works before the worker exists and keeps an unverified legacy parent unassigned", () => {
    expect(sentinelTab({ ...watch, session_id: undefined }, "ag_one")?.sentinelId).toBe("watch-1");
    expect(sentinelTab({ ...watch, parent_session_id: undefined }, "ag_one")?.parentSession).toBeUndefined();
    expect(sentinelTab(watch, "ag_one", false)?.parentSession).toBeUndefined();
  });
  it("rejects foreign Agents, cross-source IDs, and malformed identities", () => {
    expect(sentinelTab(watch, "ag_other")).toBeNull();
    for (const edit of [{ task_id: "sentinel:other" }, { id: "../other" }, { agent_id: "ag/other" }, { parent_session_id: "../main" }]) expect(sentinelTab({ ...watch, ...edit }, "ag_one")).toBeNull();
  });
  it("restores only structured Sentinel identity and reveals it in its parent", () => {
    const tab = sentinelTab(watch, "ag_one")!;
    const restored = restoreTabs({ open: [{ ...tab, id: "forged" }], active: "forged" }, []);
    expect(restored.tabs).toEqual([tab]);
    expect(browserSheetVisible(tab, "main-1")).toBe(true);
    expect(browserSheetVisible(tab, "main-2")).toBe(false);
    expect(browserSheetVisible({ ...tab, parentSession: undefined }, "main-2")).toBe(true);
    expect(restoreTabs({ open: [{ ...tab, sentinelId: "../../bad" }] }, []).tabs).toEqual([]);
    expect(restoreTabs({ open: [{ ...tab, agentId: undefined }] }, []).tabs).toEqual([]);
  });
  it("keeps main, public embed and Sentinel identities separate in deep links", () => {
    const link = sentinelDeepLink("/a/ag_one?embed=1&c=main-1", "watch-1");
    expect(link).toBe("/a/ag_one?embed=1&c=main-1&sentinel=watch-1");
    expect(requestedSentinelId(link.slice(link.indexOf("?")))).toBe("watch-1");
    expect(requestedSentinelId("?sentinel=..%2Fsecret")).toBeNull();
    expect(requestedSentinelId("?c=worker-1")).toBeNull();
    expect(() => sentinelDeepLink("/a/ag_one", "bad/value")).toThrow();
  });
});
