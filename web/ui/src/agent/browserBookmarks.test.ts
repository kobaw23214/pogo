import { describe, expect, it, vi } from "vitest";
import { BOOKMARKS_KEY, LEGACY_CARDS_KEY, BROWSER_VIEW_ID, bookmarkURL, newBookmark, migrateBrowserStorage, parseBookmarkImport, readBookmarks } from "./browserBookmarks";

const origin = "https://pogo.example";
const tabsKey = "pogo:hub-tabs";
function storage(initial: Record<string, string> = {}) {
  const values = new Map(Object.entries(initial));
  return { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } };
}
const resolve = vi.fn(async (urls: string[]) => ({ mappings: urls.map(url => ({ url, ...(url.startsWith("/panels/site/") ? { resolved_url: url.replace("/panels/site", "https://site.example") } : { error: "registration missing" }) })) }));

describe("original-site bookmarks", () => {
  it("rejects proxy destinations, credentials and executable schemes while allowing external /panels routes", () => {
    for (const url of ["/panels/site/", "https://pogo.example/panels/site/", "/relative", "javascript:alert(1)", "file:///x", "https://u:p@site.example/"]) expect(bookmarkURL(url, origin)).toBeNull();
    expect(bookmarkURL("site.example:8080/app?q=1#detail", origin)).toBe("http://site.example:8080/app?q=1#detail");
    expect(bookmarkURL("https://other.example/panels/site/", origin)).toBe("https://other.example/panels/site/");
  });
  it("imports only bounded original-url entries without registering or visiting a proxy", () => {
    const imported = parseBookmarkImport(JSON.stringify({ shortcuts: [{ title: "Site", url: "https://site.example/a" }, { title: "Old", url: "/panels/site/" }, { title: "Bad", url: "javascript:alert(1)" }] }), origin);
    expect(imported?.bookmarks.map(card => card.url)).toEqual(["https://site.example/a"]);
    expect(imported?.invalid).toBe(2);
    expect(parseBookmarkImport("{}", origin)).toBeNull();
  });
});

describe("native-only saved entry migration", () => {
  it("backs up raw data, resolves old URLs including query/hash, and keeps missing targets for repair", async () => {
    const rawCards = JSON.stringify([
      { id: "source", title: "Original", url: "/panels/gone/", source: "https://original.example/a?q=2#x" },
      { id: "proxy", title: "Known", url: `${origin}/panels/site/app?q=1#route` },
      { id: "missing", title: "Missing", url: "/panels/gone/deep?q=3#x" },
    ]);
    const rawTabs = JSON.stringify({ open: [
      { id: "shell", kind: "shell", label: "Shell", closable: true },
      { id: "panel:site", kind: "panel", label: "Site", path: "/panels/site/app?q=1#route", closable: true },
      { id: "browser:2", kind: "browser", label: "Docs", url: "https://docs.example/", closable: true },
      { id: "preview:file", kind: "preview", label: "Report", path: "a.pdf", closable: true },
    ], active: "panel:site" });
    const saved = storage({ [tabsKey]: rawTabs, [LEGACY_CARDS_KEY]: rawCards });
    const migrated = await migrateBrowserStorage(saved, tabsKey, [], origin, resolve);
    expect(migrated.open?.map(tab => tab.id)).toEqual(["shell", BROWSER_VIEW_ID, "preview:file"]);
    expect(migrated.active).toBe(BROWSER_VIEW_ID);
    expect(migrated.open?.[1].url).toBe("https://site.example/app?q=1#route");
    const cards = readBookmarks(saved);
    expect(cards.find(card => card.id === "legacy-card:source")?.url).toBe("https://original.example/a?q=2#x");
    expect(cards.find(card => card.id === "legacy-card:missing")).toMatchObject({ title: "Missing", url: "/panels/gone/deep?q=3#x", needsRepair: true });
    expect(cards.some(card => card.url === "https://docs.example/" && card.restoredTab)).toBe(true);
    expect(JSON.parse(saved.getItem(`${tabsKey}:native-only:v1:backup`)!)).toMatchObject({ tabs: rawTabs, cards: rawCards });
    const resolver = vi.fn((urls: string[]) => resolve(urls));
    await expect(migrateBrowserStorage(saved, tabsKey, [], origin, resolver)).resolves.toEqual(migrated);
    expect(resolver).not.toHaveBeenCalled();
    expect(readBookmarks(saved)).toEqual(cards);
  });
  it("migrates the external pre-strip tenant even without a tabs record, preserving it in backup", async () => {
    const saved = storage();
    const migrated = await migrateBrowserStorage(saved, tabsKey, [], origin, resolve, "panel:gone");
    expect(migrated.open).toEqual([{ id: BROWSER_VIEW_ID, kind: "browser", label: "gone", closable: true }]);
    expect(migrated.active).toBe(BROWSER_VIEW_ID);
    expect(readBookmarks(saved)[0]).toMatchObject({ title: "gone", url: "/panels/gone/", needsRepair: true, restoredTab: true });
    expect(JSON.parse(saved.getItem(`${tabsKey}:native-only:v1:backup`)!)).toMatchObject({ tabs: null, legacyTenant: "panel:gone" });
  });
  it("retains every unresolved entry if the migration service is unavailable", async () => {
    const saved = storage({ [LEGACY_CARDS_KEY]: JSON.stringify([{ id: "x", title: "Site", url: "/panels/site/" }]) });
    await migrateBrowserStorage(saved, tabsKey, [], origin, async () => { throw new Error("offline"); });
    expect(readBookmarks(saved)).toEqual([{ id: "legacy-card:x", title: "Site", url: "/panels/site/", needsRepair: true }]);
  });
  it("does not resurrect deleted bookmarks when another dock scope is migrated", async () => {
    const saved = storage({ [LEGACY_CARDS_KEY]: JSON.stringify([{ id: "x", title: "Site", url: "https://site.example/" }]) });
    await migrateBrowserStorage(saved, tabsKey, [], origin, resolve);
    saved.setItem(BOOKMARKS_KEY, "[]");
    await migrateBrowserStorage(saved, "pogo:studio:other:hub-tabs", [], origin, resolve);
    expect(readBookmarks(saved)).toEqual([]);
  });
  it("leaves newer cross-window edits intact rather than committing a stale migration", async () => {
    const saved = storage({ [LEGACY_CARDS_KEY]: JSON.stringify([{ id: "x", title: "Site", url: "/panels/site/" }]) });
    await expect(migrateBrowserStorage(saved, tabsKey, [], origin, async urls => {
      saved.setItem(tabsKey, '{"open":[],"active":null}');
      return resolve(urls);
    })).rejects.toThrow("another window");
    expect(saved.getItem(tabsKey)).toBe('{"open":[],"active":null}');
    expect(saved.getItem(`${tabsKey}:native-only:v1`)).toBeNull();
    expect(saved.getItem(BOOKMARKS_KEY)).toBeNull();
  });
  it("does not mutate entrypoints if the backup cannot be written", async () => {
    const saved = storage({ [LEGACY_CARDS_KEY]: "[]" });
    const blocked = { getItem: saved.getItem, setItem: () => { throw new Error("quota exceeded"); } };
    await expect(migrateBrowserStorage(blocked, tabsKey, [], origin, resolve)).rejects.toThrow("quota exceeded");
    expect(saved.getItem(LEGACY_CARDS_KEY)).toBe("[]");
    expect(saved.getItem(tabsKey)).toBeNull();
  });
});

it("refuses malformed-host bookmarks without silently correcting existing saved data", () => {
  const raw = JSON.stringify([{ id: "typo", title: "Saved site", url: "http://www,example.com/" }]);
  const saved = storage({ [BOOKMARKS_KEY]: raw });
  const old = readBookmarks(saved)[0];
  expect(bookmarkURL(old.url, origin)).toBeNull();
  expect(newBookmark(old.title, old.url, origin)).toBeNull();
  expect(saved.getItem(BOOKMARKS_KEY)).toBe(raw);
  expect(newBookmark(old.title, "http://www.example.com/", origin)?.url).toBe("http://www.example.com/");
  expect(parseBookmarkImport(raw, origin)).toEqual({ bookmarks: [], invalid: 1 });
});
