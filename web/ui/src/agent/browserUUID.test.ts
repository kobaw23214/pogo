import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { api } from "@/api/client";
import { restoreTabs } from "@/components/dockSheets";
import { newBookmark, parseBookmarkImport } from "./browserBookmarks";
import { BrowserNewOperations, browserSheetID, reconcileBrowserSheets } from "./browserSheets";
import { BrowserTabClosures } from "./browserTabClose";
import type { NativeBrowserState } from "./nativeBrowser";

const secureRandomValues = globalThis.crypto.getRandomValues.bind(globalThis.crypto);
const uuidV4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const context = "ctx_123456789012345678901234";
const state: NativeBrowserState = {
  backend: "native", context_ref: context, control: "agent", control_epoch: 0,
  active_tab: "t1", viewed_tab: "t1", url: "about:blank", title: "",
  viewport: { width: 1280, height: 800 }, tabs: [{ id: "t1", url: "about:blank", title: "" }],
};
let serial = 0;

describe("browser UUID operations without the secure-context randomUUID API", () => {
  beforeEach(() => {
    // Use the real cryptographic random source available on HTTP origins.
    vi.stubGlobal("crypto", { getRandomValues: secureRandomValues });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("discovers and restores native Sheets without changing surviving identifiers", () => {
    expect(browserSheetID().slice("browser:".length)).toMatch(uuidV4);
    const tabs = reconcileBrowserSheets([], "http-sheets", state);
    expect(tabs[0].id.slice("browser:".length)).toMatch(uuidV4);
    expect(reconcileBrowserSheets(tabs, "http-sheets", state)[0].id).toBe(tabs[0].id);
    const restored = restoreTabs({ open: [tabs[0], { ...tabs[0], id: "", browserBinding: { ...tabs[0].browserBinding!, tab_id: "t2" } }, { ...tabs[0], id: "", browserUnavailable: true, browserBinding: undefined }] }, []);
    expect(restored.tabs[0].id).toBe(tabs[0].id);
    expect(restored.tabs[1].id.slice("browser:".length)).toMatch(uuidV4);
    expect(restored.tabs[1].browserBinding?.tab_id).toBe("t2");
    expect(restored.tabs[2].id.slice("browser:".length)).toMatch(uuidV4);
    expect(restored.tabs[2].browserUnavailable).toBe(true);
    expect(new Set(restored.tabs.map(tab => tab.id)).size).toBe(3);
  });

  it("preserves new-operation identities across retries and reloads before allocating the next", () => {
    const data = new Map<string, string>();
    const storage = { getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => { data.set(key, value); } };
    const operations = new BrowserNewOperations(storage);
    const first = operations.get("http-new", context);
    expect(first).toMatch(uuidV4);
    expect(operations.get("http-new", context)).toBe(first);
    const restored = new BrowserNewOperations(storage);
    // A persisted retry does not require new random generation.
    vi.stubGlobal("crypto", undefined);
    expect(restored.get("http-new", context)).toBe(first);
    restored.complete("http-new", context);
    vi.stubGlobal("crypto", { getRandomValues: secureRandomValues });
    const next = restored.get("http-new", context);
    expect(next).toMatch(uuidV4);
    expect(next).not.toBe(first);
  });

  it("creates and imports bookmarks with strong UUIDs on HTTP", () => {
    const first = newBookmark("Example", "https://example.test/", "http://pogo.test:8080")!;
    const imported = parseBookmarkImport('[{"title":"HTTP site","url":"http://site.test/"},{"title":"HTTPS site","url":"https://site.test/"}]', "http://pogo.test:8080")!;
    const ids = [first.id, ...imported.bookmarks.map(bookmark => bookmark.id)];
    expect(ids.every(id => uuidV4.test(id))).toBe(true);
    expect(new Set(ids).size).toBe(3);
    expect(imported.invalid).toBe(0);
  });

  it("retries a close with the original ID and lease even if random generation later disappears", async () => {
    const binding = { sessionId: `http-close-${++serial}`, context_ref: context, tab_id: "t1" };
    const acquired = { ...state, control: "human" as const, control_epoch: 1, lease_token: "http-lease" };
    const client = {
      browserControl: vi.fn<typeof api.browserControl>(async () => acquired),
      browserTabs: vi.fn<typeof api.browserTabs>().mockRejectedValueOnce(new TypeError("receipt lost")).mockResolvedValueOnce({ context_ref: context, tab_id: "t1", state: { ...state, active_tab: "", viewed_tab: "", tabs: [] } }),
    };
    const closures = new BrowserTabClosures();
    await expect(closures.close(binding, client)).rejects.toThrow("receipt lost");
    const sent = client.browserTabs.mock.calls[0][1];
    expect(sent.op_id).toMatch(uuidV4);
    vi.stubGlobal("crypto", undefined);
    await closures.close(binding, client);
    expect(client.browserControl).toHaveBeenCalledTimes(1);
    expect(client.browserTabs.mock.calls[1][1]).toEqual(sent);
  });

  it("fails before takeover or close dispatch when strong random generation is unavailable", async () => {
    vi.stubGlobal("crypto", undefined);
    const binding = { sessionId: `http-unavailable-${++serial}`, context_ref: context, tab_id: "t1" };
    const client = { browserControl: vi.fn<typeof api.browserControl>(), browserTabs: vi.fn<typeof api.browserTabs>() };
    await expect(new BrowserTabClosures().close(binding, client)).rejects.toThrow();
    expect(client.browserControl).not.toHaveBeenCalled();
    expect(client.browserTabs).not.toHaveBeenCalled();
  });
});
