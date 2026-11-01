import { describe, expect, it, vi } from "vitest";
import { BOOKMARKS_KEY, type BrowserBookmark } from "./browserBookmarks";
import { BOOKMARKS_ACCOUNT_BACKUP, BOOKMARKS_ACCOUNT_CLAIM, createAccountBrowserBookmarks } from "./accountBrowserBookmarks";
import type { AccountBookmarksAPI, AccountBookmarksSnapshot } from "@/api/browserBookmarks";

const origin = "https://pogo.example";
const site = (id: string, title = id): BrowserBookmark => ({ id, title, url: `https://${id}.example/` });
function snapshot(account_id = "account-a", revision = 0, bookmarks: BrowserBookmark[] = [], initialized = true): AccountBookmarksSnapshot {
  return { source: "pogo_bookmarks", scope: "account", status: initialized ? "ready" : "not_initialized", account_id, revision, bookmarks, total: bookmarks.length };
}
function storage(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  return { getItem: (key: string) => map.get(key) ?? null, setItem: (key: string, value: string) => { map.set(key, value); } };
}
function server(initial: AccountBookmarksSnapshot = snapshot()) {
  let current = initial;
  const api = {
    read: vi.fn(async () => structuredClone(current)),
    write: vi.fn(async (input: { account_id: string; revision: number; bookmarks: BrowserBookmark[] }) => {
      if (input.account_id !== current.account_id || input.revision !== current.revision) throw Object.assign(new Error("conflict"), { status: 409 });
      current = snapshot(current.account_id, current.revision + 1, structuredClone(input.bookmarks));
      return structuredClone(current);
    }),
  } satisfies AccountBookmarksAPI;
  return { api, replace: (next: AccountBookmarksSnapshot) => { current = next; }, current: () => current };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(accept => { resolve = accept; });
  return { promise, resolve };
}

describe("account browser bookmarks", () => {
  it("keeps local source untouched and requires an explicit account import", async () => {
    const raw = JSON.stringify([site("local")]);
    const saved = storage({ [BOOKMARKS_KEY]: raw });
    const remote = server(snapshot("account-a", 0, [], false));
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    expect(await store.refresh()).toBe(true);
    expect(store.state.initialized).toBe(false);
    expect(store.state.bookmarks).toEqual([]);
    expect(store.state.localBookmarks).toEqual([site("local")]);
    expect(store.state.legacyNeedsClaim).toBe(true);
    expect(remote.api.write).not.toHaveBeenCalled();
    expect(saved.getItem(BOOKMARKS_KEY)).toBe(raw);
    expect(saved.getItem(BOOKMARKS_ACCOUNT_CLAIM)).toBeNull();
  });

  it("merges explicit imports with current server entries, backs up raw data, and never exposes the source to another account", async () => {
    const raw = JSON.stringify([site("local"), { id: "broken", title: "Old proxy", url: "/panels/gone/", needsRepair: true }]);
    const saved = storage({ [BOOKMARKS_KEY]: raw });
    const remote = server(snapshot("account-a", 4, [site("remote")]));
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    await store.refresh();
    expect(await store.importLocal()).toEqual({ success: true, added: 1, invalid: 1 });
    expect(store.state.bookmarks).toEqual([site("remote"), site("local")]);
    expect(store.state.localBookmarks).toEqual([{ id: "broken", title: "Old proxy", url: "/panels/gone/", needsRepair: true }]);
    expect(store.state.legacyNeedsClaim).toBe(false);
    expect(saved.getItem(BOOKMARKS_KEY)).toBe(raw);
    expect(JSON.parse(saved.getItem(BOOKMARKS_ACCOUNT_BACKUP)!)).toMatchObject({ bookmarks: raw, account_id: "account-a" });
    expect(JSON.parse(saved.getItem(BOOKMARKS_ACCOUNT_CLAIM)!)).toEqual({ account_id: "account-a", status: "done" });
    remote.replace(snapshot("account-b", 0, [], false));
    await store.refresh();
    expect(store.state.localBookmarks).toEqual([]);
    expect(store.state.legacyClaimedElsewhere).toBe(true);
    expect((await store.importLocal()).success).toBe(false);
    expect(remote.api.write).toHaveBeenCalledTimes(1);
  });

  it("does not upload if the raw backup cannot be written", async () => {
    const saved = storage({ [BOOKMARKS_KEY]: JSON.stringify([site("local")]) });
    const remote = server();
    const blocked = { getItem: saved.getItem, setItem: () => { throw new Error("quota"); } };
    const store = createAccountBrowserBookmarks(remote.api, blocked, origin);
    await store.refresh();
    expect((await store.importLocal()).success).toBe(false);
    expect(store.state.error).toContain("quota");
    expect(remote.api.write).not.toHaveBeenCalled();
    expect(saved.getItem(BOOKMARKS_ACCOUNT_CLAIM)).toBeNull();
  });

  it("preserves last successful account data on connection failure, but clears it when authentication is lost", async () => {
    const remote = server(snapshot("account-a", 3, [site("saved")]));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    await store.refresh();
    remote.api.read.mockRejectedValueOnce(new Error("offline"));
    expect(await store.refresh()).toBe(false);
    expect(store.state.bookmarks).toEqual([site("saved")]);
    expect(store.state.error).toBe("offline");
    remote.api.read.mockRejectedValueOnce(Object.assign(new Error("signed out"), { status: 401 }));
    expect(await store.refresh()).toBe(false);
    expect(store.state.bookmarks).toEqual([]);
    expect(store.state.loaded).toBe(false);
  });

  it("ignores a late read from a superseded account request", async () => {
    const oldRead = deferred<AccountBookmarksSnapshot>();
    const api = { read: vi.fn().mockReturnValueOnce(oldRead.promise).mockResolvedValueOnce(snapshot("account-b", 3, [site("b")])), write: vi.fn() };
    const store = createAccountBrowserBookmarks(api, storage(), origin);
    const first = store.refresh();
    expect(await store.refresh()).toBe(true);
    oldRead.resolve(snapshot("account-a", 2, [site("a")]));
    expect(await first).toBe(false);
    expect(store.state.accountId).toBe("account-b");
    expect(store.state.bookmarks).toEqual([site("b")]);
  });

  it("replays only the intended add after a revision conflict and preserves concurrent additions", async () => {
    const remote = server(snapshot("account-a", 5, [site("saved")]));
    const notify = vi.fn();
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin, notify);
    await store.refresh();
    remote.api.write.mockImplementationOnce(async () => {
      remote.replace(snapshot("account-a", 6, [site("saved"), site("concurrent")]));
      throw Object.assign(new Error("changed"), { status: 409 });
    });
    expect(await store.upsert(site("added"))).toBe(true);
    expect(remote.current().bookmarks).toEqual([site("saved"), site("concurrent"), site("added")]);
    expect(remote.api.write).toHaveBeenNthCalledWith(2, { account_id: "account-a", revision: 6, bookmarks: [site("saved"), site("concurrent"), site("added")] }, expect.any(AbortSignal));
    expect(notify).toHaveBeenCalledTimes(1);
  });

  it("does not overwrite a concurrently edited bookmark", async () => {
    const original = site("saved");
    const remote = server(snapshot("account-a", 5, [original]));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    await store.refresh();
    remote.api.write.mockImplementationOnce(async () => {
      remote.replace(snapshot("account-a", 6, [site("saved", "Changed elsewhere")]));
      throw Object.assign(new Error("changed"), { status: 409 });
    });
    expect(await store.upsert(site("saved", "My edit"), original)).toBe(false);
    expect(remote.api.write).toHaveBeenCalledTimes(1);
    expect(store.state.bookmarks[0]?.title).toBe("Changed elsewhere");
    expect(store.state.error).toContain("另一窗口");
  });

  it("checks account identity again before a write and never submits another account's bookmarks", async () => {
    const remote = server(snapshot("account-a", 1, [site("a")]));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    await store.refresh();
    remote.replace(snapshot("account-b", 1, [site("b")]));
    expect(await store.upsert(site("add"))).toBe(false);
    expect(remote.api.write).not.toHaveBeenCalled();
    expect(store.state.accountId).toBe("account-b");
    expect(store.state.bookmarks).toEqual([site("b")]);
    expect(store.state.error).toContain("账号已变化");
  });

  it("handles an account change during PUT without replaying the operation in the new account", async () => {
    const remote = server(snapshot("account-a", 1, [site("a")]));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    await store.refresh();
    remote.api.write.mockImplementationOnce(async () => {
      remote.replace(snapshot("account-b", 1, [site("b")]));
      throw Object.assign(new Error("account changed"), { status: 409 });
    });
    expect(await store.upsert(site("add"))).toBe(false);
    expect(remote.api.write).toHaveBeenCalledTimes(1);
    expect(store.state.accountId).toBe("account-b");
    expect(store.state.bookmarks).toEqual([site("b")]);
  });

  it("distinguishes an initialized empty account and skips duplicate URLs when importing", async () => {
    const remote = server(snapshot("account-a", 3, []));
    const saved = storage();
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    await store.refresh();
    expect(store.state.initialized).toBe(true);
    const result = await store.importBookmarks([site("one"), { ...site("one"), id: "duplicate" }, site("two")]);
    expect(result).toEqual({ success: true, added: 2 });
    expect(store.state.bookmarks).toEqual([site("one"), site("two")]);
    expect(saved.getItem(BOOKMARKS_KEY)).toBeNull();
    expect(remote.api.write.mock.calls[0]?.[0].revision).toBe(3);
  });

  it("explicitly initializes an unsynchronized account even when all imported local entries need repair", async () => {
    const broken = { id: "old-proxy", title: "Old proxy", url: "/panels/gone/", needsRepair: true };
    const saved = storage({ [BOOKMARKS_KEY]: JSON.stringify([broken]) });
    const remote = server(snapshot("account-a", 0, [], false));
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    await store.refresh();
    expect(await store.importLocal()).toEqual({ success: true, added: 0, invalid: 1 });
    expect(remote.api.write).toHaveBeenCalledWith({ account_id: "account-a", revision: 0, bookmarks: [] }, expect.any(AbortSignal));
    expect(store.state.initialized).toBe(true);
    expect(store.state.localBookmarks).toEqual([broken]);
    expect(JSON.parse(saved.getItem(BOOKMARKS_ACCOUNT_CLAIM)!)).toEqual({ account_id: "account-a", status: "done" });
  });

  it("treats an explicit empty import as initialization once and as a no-op thereafter", async () => {
    const remote = server(snapshot("account-a", 0, [], false));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    await store.refresh();
    expect(await store.importBookmarks([])).toEqual({ success: true, added: 0 });
    expect(store.state.initialized).toBe(true);
    expect(remote.api.write).toHaveBeenCalledTimes(1);
    expect(await store.importBookmarks([])).toEqual({ success: true, added: 0 });
    expect(remote.api.write).toHaveBeenCalledTimes(1);
  });

  it("leaves both server state and local source intact when a service response is invalid", async () => {
    const raw = JSON.stringify([site("local")]);
    const saved = storage({ [BOOKMARKS_KEY]: raw });
    const remote = server(snapshot("account-a", 4, [site("good")]));
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    await store.refresh();
    remote.replace(snapshot("account-a", 5, [{ id: "wrong", title: "Bad", url: "javascript:alert(1)" }]));
    expect(await store.refresh()).toBe(false);
    expect(store.state.bookmarks).toEqual([site("good")]);
    expect(store.state.error).toContain("无效数据");
    expect(saved.getItem(BOOKMARKS_KEY)).toBe(raw);
    expect(remote.api.write).not.toHaveBeenCalled();
  });

  it("does not reject an entire account list because a saved site matches this deployment's origin-specific proxy path", async () => {
    const card = { id: "saved-other-host", title: "Original page", url: `${origin}/panels/site/` };
    const remote = server(snapshot("account-a", 4, [card, site("good")]));
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin);
    expect(await store.refresh()).toBe(true);
    expect(store.state.bookmarks).toEqual([card, site("good")]);
  });

  it("binds a failed local import to its chosen account while preserving a retryable source", async () => {
    const saved = storage({ [BOOKMARKS_KEY]: JSON.stringify([site("local")]) });
    const remote = server();
    const store = createAccountBrowserBookmarks(remote.api, saved, origin);
    await store.refresh();
    remote.api.write.mockRejectedValueOnce(new Error("offline"));
    expect((await store.importLocal()).success).toBe(false);
    expect(JSON.parse(saved.getItem(BOOKMARKS_ACCOUNT_CLAIM)!)).toEqual({ account_id: "account-a", status: "pending" });
    expect(store.state.bookmarks).toEqual([]);
    expect((await store.importLocal()).success).toBe(true);
    expect(store.state.bookmarks).toEqual([site("local")]);
  });

  it("discards stale writes after disposal instead of announcing an obsolete save", async () => {
    const pending = deferred<AccountBookmarksSnapshot>();
    const remote = server();
    const notify = vi.fn();
    const store = createAccountBrowserBookmarks(remote.api, storage(), origin, notify);
    await store.refresh();
    remote.api.write.mockReturnValueOnce(pending.promise);
    const work = store.upsert(site("add"));
    await Promise.resolve(); await Promise.resolve();
    store.dispose();
    pending.resolve(snapshot("account-a", 1, [site("add")]));
    expect(await work).toBe(false);
    expect(store.state.bookmarks).toEqual([]);
    expect(notify).not.toHaveBeenCalled();
  });

  it("permits an explicit delete of a claimed repair item but preserves the migration backup", async () => {
    const broken = { id: "broken", title: "Old", url: "/panels/gone/", needsRepair: true };
    const raw = JSON.stringify([broken]);
    const saved = storage({ [BOOKMARKS_KEY]: raw });
    const store = createAccountBrowserBookmarks(server().api, saved, origin);
    await store.refresh(); await store.importLocal();
    const backup = saved.getItem(BOOKMARKS_ACCOUNT_BACKUP);
    expect(store.removeLocal(broken)).toBe(true);
    expect(store.state.localBookmarks).toEqual([]);
    expect(saved.getItem(BOOKMARKS_ACCOUNT_BACKUP)).toBe(backup);
  });
});
