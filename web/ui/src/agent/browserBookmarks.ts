import type { PanelInfo } from "@/api/types";
import { randomUUID } from "@/utils/uuid";
import type { DockTab, StoredTabs } from "@/components/dockSheets";
import { nativeNavigationURL } from "./nativeBrowser";

export const BOOKMARKS_KEY = "pogo:browser-bookmarks:v1";
export const LEGACY_CARDS_KEY = "pogo:nav-cards";
export const BROWSER_VIEW_ID = "browser:session";
export const BOOKMARKS_CHANGED = "pogo:browser-bookmarks-changed";
export interface BrowserBookmark {
  id: string;
  title: string;
  url: string;
  needsRepair?: boolean;
  restoredTab?: boolean;
}
const hasSheetBinding = (tab: DockTab) => tab.kind === "browser" && ("browserBinding" in tab || !!tab.browserUnavailable);
type StorageAccess = Pick<Storage, "getItem" | "setItem">;
type URLMapping = { url: string; resolved_url?: string; error?: string };
export type ResolveBrowserURLs = (urls: string[]) => Promise<{ mappings: URLMapping[] }>;

export function readBookmarks(storage: StorageAccess): BrowserBookmark[] {
  const raw = storage.getItem(BOOKMARKS_KEY);
  if (!raw) return [];
  const value: unknown = JSON.parse(raw);
  if (!Array.isArray(value)) throw new Error("Invalid browser bookmarks; the saved data has been preserved.");
  if (!value.every(item => !!item && typeof item === "object" && typeof item.id === "string" && typeof item.title === "string" && typeof item.url === "string")) throw new Error("Invalid browser bookmark entry; the saved data has been preserved.");
  return value as BrowserBookmark[];
}
export function writeBookmarks(storage: StorageAccess, bookmarks: BrowserBookmark[]) {
  storage.setItem(BOOKMARKS_KEY, JSON.stringify(bookmarks));
}
export function bookmarkURL(value: string, origin: string): string | null {
  // New bookmarks must describe an original site, never a Pogo-relative path.
  if (/^[/#]/.test(value.trim())) return null;
  return nativeNavigationURL(value, "", origin);
}
export function newBookmark(title: string, value: string, origin: string): BrowserBookmark | null {
  const url = bookmarkURL(value, origin);
  if (!title.trim() || !url) return null;
  return { id: randomUUID(), title: title.trim().slice(0, 120), url };
}
export function parseBookmarkImport(text: string, origin: string): { bookmarks: BrowserBookmark[]; invalid: number } | null {
  let value: unknown;
  try { value = JSON.parse(text); } catch { return null; }
  const entries = Array.isArray(value) ? value : value && typeof value === "object" && "shortcuts" in value ? value.shortcuts : null;
  if (!Array.isArray(entries)) return null;
  const bookmarks: BrowserBookmark[] = [];
  let invalid = 0;
  for (const entry of entries) {
    const result = entry && typeof entry.title === "string" && typeof entry.url === "string" ? newBookmark(entry.title, entry.url, origin) : null;
    if (result) bookmarks.push(result); else invalid++;
  }
  return { bookmarks, invalid };
}

/** Only our own old proxy paths are sent to the migration resolver. A site's
 * coincidentally named /panels route is a normal original URL. */
function legacyURL(value: string, origin: string): string | null {
  try {
    const url = new URL(value, origin);
    return url.origin === origin && url.pathname.startsWith("/panels/") ? `${url.pathname}${url.search}${url.hash}` : null;
  } catch { return null; }
}

/** Converts local saved entrypoints, never navigates or replays browser calls.
 * Backup precedes every mutation, marker is written last, and a retry merges
 * deterministic ids. Unresolvable entries remain editable and visible. */
export async function migrateBrowserStorage(storage: StorageAccess, tabsKey: string, panels: PanelInfo[], origin: string, resolve: ResolveBrowserURLs, legacyTenant?: string | null): Promise<StoredTabs> {
  const rawTabs = storage.getItem(tabsKey);
  const tabs: StoredTabs = rawTabs ? JSON.parse(rawTabs) : {};
  const marker = `${tabsKey}:native-only:v1`;
  if (storage.getItem(marker) === "done") return tabs;
  const rawCards = storage.getItem(LEGACY_CARDS_KEY);
  const rawBookmarks = storage.getItem(BOOKMARKS_KEY);
  const backup = `${tabsKey}:native-only:v1:backup`;
  if (!storage.getItem(backup)) storage.setItem(backup, JSON.stringify({ version: 1, tabs: rawTabs, cards: rawCards, bookmarks: rawBookmarks, legacyTenant: legacyTenant ?? null }));
  const bookmarks = readBookmarks(storage);
  const candidates: (BrowserBookmark & { source?: string })[] = [];
  // Import old cards only once globally, but each dock scope's old tabs still
  // gets migrated. Empty current bookmarks reflect an intentional deletion.
  if (!storage.getItem(`${BOOKMARKS_KEY}:legacy-imported`)) {
    const cards: unknown = rawCards ? JSON.parse(rawCards) : null;
    if (rawCards && !Array.isArray(cards)) throw new Error("Invalid legacy bookmarks; the backup has been preserved.");
    if (Array.isArray(cards)) cards.forEach((card, i) => {
      if (card && typeof card.title === "string" && typeof card.url === "string") candidates.push({ id: `legacy-card:${card.id || i}`, title: card.title, url: card.url, source: typeof card.source === "string" ? card.source : undefined });
    });
    panels.forEach(panel => candidates.push({ id: `legacy-panel:${panel.name}`, title: panel.name, url: panel.entry || panel.path }));
  }
  const oldTabs = Array.isArray(tabs.open) ? tabs.open : [];
  const browserTabs = oldTabs.filter(tab => tab && !hasSheetBinding(tab) && (tab.kind === "browser" || tab.kind === "panel"));
  const oldTenant = tabs.legacyTenant || legacyTenant;
  if (oldTenant?.startsWith("panel:") && !browserTabs.some(tab => tab.id === oldTenant)) {
    const name = oldTenant.slice(6), panel = panels.find(item => item.name === name);
    browserTabs.push({ id: oldTenant, kind: "panel", label: name, path: panel?.entry || panel?.path || `/panels/${encodeURIComponent(name)}/`, closable: true });
  }
  browserTabs.forEach(tab => {
    const url = tab.url || tab.path;
    if (url) candidates.push({ id: `legacy-tab:${tabsKey}:${tab.id}`, title: tab.label || url, url, restoredTab: true });
  });
  const requests = [...new Set(candidates.map(card => card.source ? null : legacyURL(card.url, origin)).filter((url): url is string => !!url))];
  const mappings = new Map<string, string>();
  for (let i = 0; i < requests.length; i += 256) {
    try {
      const result = await resolve(requests.slice(i, i + 256));
      for (const item of result.mappings) if (item.resolved_url && bookmarkURL(item.resolved_url, origin)) mappings.set(item.url, item.resolved_url);
    } catch { /* Preserve each unresolved URL for manual repair, without guessing. */ }
  }
  const migrated = new Map<string, BrowserBookmark>();
  for (const candidate of candidates) {
    const old = legacyURL(candidate.url, origin);
    const url = candidate.source ? bookmarkURL(candidate.source, origin) : old ? mappings.get(old) ?? null : bookmarkURL(candidate.url, origin);
    const card: BrowserBookmark = { id: candidate.id, title: candidate.title, url: url || candidate.source || candidate.url, ...(url ? {} : { needsRepair: true }), ...(candidate.restoredTab ? { restoredTab: true } : {}) };
    migrated.set(candidate.id, card);
    if (!bookmarks.some(existing => existing.id === card.id)) bookmarks.push(card);
  }
  const selected = browserTabs.find(tab => tab.id === (tabs.active || tabs.legacyTenant || legacyTenant)) ?? browserTabs[0];
  const selectedCard = selected && migrated.get(`legacy-tab:${tabsKey}:${selected.id}`);
  const collapsed: DockTab[] = [];
  for (const tab of oldTabs) {
    if (!tab) continue;
    if (hasSheetBinding(tab)) { collapsed.push(tab); continue; }
    if (tab.kind === "panel" || tab.kind === "browser") {
      if (!collapsed.some(item => item.kind === "browser" && !hasSheetBinding(item))) collapsed.push({ id: BROWSER_VIEW_ID, kind: "browser", label: selected?.label || "Browser", closable: true, url: selectedCard && !selectedCard.needsRepair ? selectedCard.url : undefined });
    } else collapsed.push(tab);
  }
  if (browserTabs.length && !collapsed.some(tab => tab.kind === "browser" && !hasSheetBinding(tab))) collapsed.push({ id: BROWSER_VIEW_ID, kind: "browser", label: selected?.label || "Browser", closable: true, url: selectedCard && !selectedCard.needsRepair ? selectedCard.url : undefined });
  const wanted = tabs.active || tabs.legacyTenant || legacyTenant;
  const result: StoredTabs = { ...(tabs.browserActive ? { browserActive: tabs.browserActive } : {}), open: collapsed, active: browserTabs.some(tab => tab.id === wanted) ? BROWSER_VIEW_ID : tabs.active };
  // A second window may have edited storage while the resolver was pending.
  // Leave its newer state intact; the next boot can retry from the backup.
  if (storage.getItem(tabsKey) !== rawTabs || storage.getItem(BOOKMARKS_KEY) !== rawBookmarks || storage.getItem(LEGACY_CARDS_KEY) !== rawCards) throw new Error("Browser entries changed in another window. Reload to finish migration.");
  writeBookmarks(storage, bookmarks);
  storage.setItem(tabsKey, JSON.stringify(result));
  storage.setItem(`${BOOKMARKS_KEY}:legacy-imported`, "done");
  storage.setItem(marker, "done");
  return result;
}
