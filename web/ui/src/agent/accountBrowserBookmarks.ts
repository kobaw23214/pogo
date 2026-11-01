import { computed, inject, onBeforeUnmount, onMounted, reactive, readonly, type InjectionKey } from "vue";
import { accountBookmarksAPI, agentBookmarksAPI, type AccountBookmarksAPI, type AccountBookmarksSnapshot } from "@/api/browserBookmarks";
import { BOOKMARKS_CHANGED, BOOKMARKS_KEY, bookmarkURL, readBookmarks, type BrowserBookmark } from "./browserBookmarks";
import { normalizeUrl } from "./browserUrl";

export const browserBookmarkAgentKey: InjectionKey<string> = Symbol("pogo:browser-bookmark-agent");

export const ACCOUNT_BOOKMARKS_CHANGED = "pogo:account-browser-bookmarks-changed";
export const ACCOUNT_BOOKMARKS_SYNC_KEY = "pogo:account-browser-bookmarks-update:v1";
export const BOOKMARKS_ACCOUNT_CLAIM = `${BOOKMARKS_KEY}:account-claim:v1`;
export const BOOKMARKS_ACCOUNT_BACKUP = `${BOOKMARKS_KEY}:account-sync-backup:v1`;
export const MAX_ACCOUNT_BOOKMARKS = 256;
type StorageAccess = Pick<Storage, "getItem" | "setItem">;
type Claim = { account_id: string; status: "pending" | "done" };
type Mutation = (latest: BrowserBookmark[]) => BrowserBookmark[];
const clone = (items: readonly BrowserBookmark[]) => items.map(item => ({ ...item }));
const sameBookmark = (a: BrowserBookmark, b: BrowserBookmark) => a.title === b.title && a.url === b.url;
const changedAccount = () => new Error("账号已变化，请刷新收藏后重试。");
function validAccountURL(value: string, origin: string) {
  if (!/^https?:\/\//i.test(value)) return false;
  const normalized = normalizeUrl(value, origin);
  if (!normalized) return false;
  const url = new URL(normalized);
  // The account list may have been saved from another Pogo deployment origin.
  // Origin-specific proxy rejection belongs to adding/opening a bookmark, not
  // to validation of the whole synchronized account snapshot.
  return !url.username && !url.password;
}

/** Server state is authoritative. Local storage remains a one-time, explicit
 * import source and never caches another account's synchronized bookmarks. */
export function createAccountBrowserBookmarks(api: AccountBookmarksAPI, storage: StorageAccess, origin: string, notify: () => void = () => {}, agentId = "") {
  const state = reactive({
    accountId: "", ownerAccountId: "", revision: 0, initialized: false,
    bookmarks: [] as BrowserBookmark[], localBookmarks: [] as BrowserBookmark[],
    loading: false, saving: false, loaded: false, error: "", localError: "", legacyClaimedElsewhere: false, legacyNeedsClaim: false,
  });
  let epoch = 0;
  let reader: AbortController | undefined;
  let disposed = false;
  let writeController: AbortController | undefined;

  function localSource() {
    state.localError = "";
    if (agentId) { state.localBookmarks = []; state.legacyNeedsClaim = false; state.legacyClaimedElsewhere = false; return; }
    if (!state.accountId) { state.localBookmarks = []; state.legacyNeedsClaim = false; return; }
    try {
      const rawClaim = storage.getItem(BOOKMARKS_ACCOUNT_CLAIM);
      const claim: Claim | null = rawClaim ? JSON.parse(rawClaim) : null;
      if (claim && (typeof claim.account_id !== "string" || !["pending", "done"].includes(claim.status))) throw new Error("本机收藏同步记录无效，原数据已保留。");
      state.legacyClaimedElsewhere = !!claim && claim.account_id !== state.accountId;
      state.legacyNeedsClaim = !claim;
      state.localBookmarks = state.legacyClaimedElsewhere ? [] : readBookmarks(storage).filter(item =>
        !state.bookmarks.some(saved => saved.id === item.id || saved.url === item.url));
    } catch (error) {
      state.localBookmarks = [];
      state.localError = error instanceof Error ? error.message : String(error);
    }
  }
  function validate(snapshot: AccountBookmarksSnapshot) {
    if (!snapshot || snapshot.source !== "pogo_bookmarks" || (agentId ? snapshot.scope !== "agent" || snapshot.agent_id !== agentId || !snapshot.owner_account_id || snapshot.scope_id !== snapshot.account_id : snapshot.scope !== "account") || !["ready", "not_initialized"].includes(snapshot.status) || !snapshot.account_id || !Number.isSafeInteger(snapshot.revision) || snapshot.revision < 0 || !Array.isArray(snapshot.bookmarks) || snapshot.total !== snapshot.bookmarks.length || snapshot.bookmarks.length > MAX_ACCOUNT_BOOKMARKS ||
      !snapshot.bookmarks.every(item => item && typeof item.id === "string" && !!item.id && typeof item.title === "string" && !!item.title.trim() && typeof item.url === "string" && validAccountURL(item.url, origin))) {
      throw new Error("收藏服务返回了无效数据，请重试。原收藏未被覆盖。");
    }
  }
  function apply(snapshot: AccountBookmarksSnapshot) {
    validate(snapshot);
    state.accountId = snapshot.account_id;
    state.ownerAccountId = snapshot.owner_account_id || snapshot.account_id;
    state.revision = snapshot.revision;
    state.initialized = snapshot.status === "ready";
    state.bookmarks = clone(snapshot.bookmarks);
    state.loaded = true;
    state.error = "";
    localSource();
  }
  function clearIdentity() {
    state.accountId = ""; state.ownerAccountId = ""; state.revision = 0; state.initialized = false;
    state.bookmarks = []; state.localBookmarks = []; state.loaded = false;
  }
  async function refresh(): Promise<boolean> {
    if (disposed || state.saving) return false;
    reader?.abort();
    const requestEpoch = ++epoch;
    const controller = reader = new AbortController();
    state.loading = true;
    state.error = "";
    try {
      const snapshot = await api.read(controller.signal);
      if (disposed || controller.signal.aborted || requestEpoch !== epoch) return false;
      apply(snapshot);
      return true;
    } catch (error) {
      if (disposed || controller.signal.aborted || requestEpoch !== epoch) return false;
      if ([401, 403].includes((error as { status?: number }).status ?? 0)) clearIdentity();
      state.error = error instanceof Error ? error.message : String(error);
      return false;
    } finally {
      if (requestEpoch === epoch) { state.loading = false; reader = undefined; }
    }
  }

  async function mutate(transform: Mutation): Promise<boolean> {
    if (disposed || state.saving || !state.loaded || !state.accountId) return false;
    const accountId = state.accountId;
    reader?.abort(); reader = undefined;
    const requestEpoch = ++epoch;
    const controller = writeController = new AbortController();
    state.loading = false; state.saving = true; state.error = "";
    try {
      // Re-read before writing: focus polling is a convenience, never an
      // authority to replace an account snapshot with stale local contents.
      for (let attempt = 0; attempt < 2; attempt++) {
        const snapshot = await api.read(controller.signal);
        if (disposed || controller.signal.aborted || requestEpoch !== epoch) return false;
        validate(snapshot);
        if (snapshot.account_id !== accountId) { apply(snapshot); throw changedAccount(); }
        apply(snapshot);
        const next = transform(clone(snapshot.bookmarks));
        if (next.length > MAX_ACCOUNT_BOOKMARKS) throw new Error(`最多可保存 ${MAX_ACCOUNT_BOOKMARKS} 个收藏。`);
        // An explicit empty import still initializes an account whose local
        // collection has never been synchronized. Only an already-ready
        // identical snapshot is a genuine no-op.
        if (snapshot.status === "ready" && JSON.stringify(next) === JSON.stringify(snapshot.bookmarks)) return true;
        try {
          const saved = await api.write({ account_id: accountId, revision: snapshot.revision, bookmarks: next }, controller.signal);
          if (disposed || controller.signal.aborted || requestEpoch !== epoch) return false;
          if (saved.account_id !== accountId) { apply(saved); throw changedAccount(); }
          apply(saved); notify();
          return true;
        } catch (error) {
          if ((error as { status?: number }).status === 409 && attempt === 0) continue;
          throw error;
        }
      }
      return false;
    } catch (error) {
      if (disposed || controller.signal.aborted || requestEpoch !== epoch) return false;
      if ([401, 403].includes((error as { status?: number }).status ?? 0)) clearIdentity();
      state.error = error instanceof Error ? error.message : String(error);
      return false;
    } finally {
      if (requestEpoch === epoch) { state.saving = false; writeController = undefined; }
    }
  }
  async function upsert(card: BrowserBookmark, original?: BrowserBookmark) {
    return mutate(latest => {
      const current = latest.find(item => item.id === card.id);
      if (original && (!current || !sameBookmark(current, original))) throw new Error("这个收藏已在另一窗口修改或删除，请重新打开编辑。");
      if (!current && latest.some(item => item.url === card.url)) throw new Error("这个网址已经收藏。");
      return current ? latest.map(item => item.id === card.id ? { ...card } : item) : [...latest, { ...card }];
    });
  }
  async function remove(card: BrowserBookmark) {
    return mutate(latest => {
      const current = latest.find(item => item.id === card.id);
      if (current && !sameBookmark(current, card)) throw new Error("这个收藏已在另一窗口修改，请刷新后再删除。");
      return latest.filter(item => item.id !== card.id);
    });
  }
  function removeLocal(card: BrowserBookmark): boolean {
    try {
      const rawClaim = storage.getItem(BOOKMARKS_ACCOUNT_CLAIM);
      const claim: Claim | null = rawClaim ? JSON.parse(rawClaim) : null;
      if (!claim || claim.account_id !== state.accountId) throw changedAccount();
      const next = readBookmarks(storage);
      const current = next.find(item => item.id === card.id);
      if (current && !sameBookmark(current, card)) throw new Error("这个本机收藏已在另一窗口修改，请刷新后再删除。");
      storage.setItem(BOOKMARKS_KEY, JSON.stringify(next.filter(item => item.id !== card.id)));
      localSource();
      return true;
    } catch (error) { state.error = error instanceof Error ? error.message : String(error); return false; }
  }
  async function importBookmarks(items: BrowserBookmark[]) {
    let added = 0;
    const success = await mutate(latest => {
      added = 0;
      for (const item of items) if (!latest.some(saved => saved.url === item.url || saved.id === item.id)) { latest.push({ ...item }); added++; }
      return latest;
    });
    return { success, added };
  }
  async function importLocalUnlocked() {
    if (agentId || !state.loaded || !state.accountId || state.saving) return { success: false, added: 0, invalid: 0 };
    const accountId = state.accountId;
    let items: BrowserBookmark[];
    let invalid = 0;
    try {
      localSource();
      if (state.localError) throw new Error(state.localError);
      if (state.legacyClaimedElsewhere) throw new Error("本机旧收藏已绑定其他账号。请通过导出文件自行选择要导入的收藏。");
      const raw = storage.getItem(BOOKMARKS_KEY);
      if (!storage.getItem(BOOKMARKS_ACCOUNT_BACKUP)) storage.setItem(BOOKMARKS_ACCOUNT_BACKUP, JSON.stringify({ version: 1, account_id: accountId, bookmarks: raw }));
      items = state.localBookmarks.flatMap(item => {
        const url = bookmarkURL(item.url, origin);
        if (!url || item.needsRepair || !item.title.trim() || [...item.title].length > 120 || !item.id || new TextEncoder().encode(item.id).length > 160 || /[\u0000-\u001f\u007f]/.test(item.title + item.id) || new TextEncoder().encode(url).length > 4096) { invalid++; return []; }
        return [{ id: item.id, title: item.title, url }];
      });
      // Bind this unscoped import source before the first asynchronous step.
      // A failed upload remains retryable only by this account; the original
      // source and its backup never change, and another account cannot claim it.
      storage.setItem(BOOKMARKS_ACCOUNT_CLAIM, JSON.stringify({ account_id: accountId, status: "pending" } satisfies Claim));
    } catch (error) {
      state.error = error instanceof Error ? error.message : String(error);
      return { success: false, added: 0, invalid };
    }
    const result = await importBookmarks(items);
    if (result.success && state.accountId === accountId) {
      try {
        storage.setItem(BOOKMARKS_ACCOUNT_CLAIM, JSON.stringify({ account_id: accountId, status: "done" } satisfies Claim));
        localSource();
      } catch (error) { state.localError = `账号收藏已保存，本机迁移标记未保存：${String(error)}`; }
    }
    return { ...result, invalid };
  }
  async function importLocal() {
    // Chromium's origin-wide lock serializes the one-time legacy claim across
    // tabs. It is only used for an explicit import, never for browsing input.
    if (typeof navigator !== "undefined" && navigator.locks) {
      return navigator.locks.request("pogo:browser-bookmarks:legacy-import", importLocalUnlocked);
    }
    return importLocalUnlocked();
  }
  function dispose() { disposed = true; epoch++; reader?.abort(); writeController?.abort(); }
  return { state: readonly(state), refresh, upsert, remove, removeLocal, importBookmarks, importLocal, localSource, dispose };
}

export type AccountBookmarkStatus = ReturnType<typeof createAccountBrowserBookmarks>["state"];

type BookmarkStore = ReturnType<typeof createAccountBrowserBookmarks>;
const sharedStores = new Map<string, { store: BookmarkStore; consumers: number }>();
let consumers = 0;
let poll: ReturnType<typeof setInterval> | undefined;
function sharedStore(agentId: string) {
  let entry = sharedStores.get(agentId);
  if (!entry) {
    const store = createAccountBrowserBookmarks(agentId ? agentBookmarksAPI(agentId) : accountBookmarksAPI, localStorage, window.location.origin, () => {
      window.dispatchEvent(new Event(ACCOUNT_BOOKMARKS_CHANGED));
      // Invalidation carries no account/Agent identity or bookmark content.
      try { localStorage.setItem(ACCOUNT_BOOKMARKS_SYNC_KEY, `${Date.now()}:${Math.random()}`); } catch { /* Focus polling will refresh. */ }
    }, agentId);
    entry = { store, consumers: 0 }; sharedStores.set(agentId, entry);
  }
  return entry;
}
function reloadAccountBookmarks() { if (!document.hidden) for (const entry of sharedStores.values()) if (entry.consumers) void entry.store.refresh(); }
function reloadLocalBookmarks() { sharedStores.get("")?.store.localSource(); }
function storageChanged(event: StorageEvent) {
  if ([BOOKMARKS_KEY, BOOKMARKS_ACCOUNT_CLAIM].includes(event.key ?? "")) reloadLocalBookmarks();
  if (event.key === ACCOUNT_BOOKMARKS_SYNC_KEY) reloadAccountBookmarks();
}
export function useAccountBrowserBookmarks() {
  // AgentView is keyed by Agent ID; its descendants inherit an immutable scope.
  const agentId = inject(browserBookmarkAgentKey, "");
  const entry = sharedStore(agentId), store = entry.store;
  onMounted(() => {
    if (entry.consumers++ === 0) void store.refresh();
    if (consumers++ === 0) {
      window.addEventListener("focus", reloadAccountBookmarks);
      document.addEventListener("visibilitychange", reloadAccountBookmarks);
      window.addEventListener("storage", storageChanged);
      window.addEventListener(BOOKMARKS_CHANGED, reloadLocalBookmarks);
      poll = setInterval(reloadAccountBookmarks, 30_000);
    }
  });
  onBeforeUnmount(() => {
    entry.consumers--;
    if (--consumers === 0) {
      window.removeEventListener("focus", reloadAccountBookmarks);
      document.removeEventListener("visibilitychange", reloadAccountBookmarks);
      window.removeEventListener("storage", storageChanged);
      window.removeEventListener(BOOKMARKS_CHANGED, reloadLocalBookmarks);
      clearInterval(poll); poll = undefined;
    }
    queueMicrotask(() => {
      if (entry.consumers === 0 && sharedStores.get(agentId) === entry) { store.dispose(); sharedStores.delete(agentId); }
    });
  });
  return { ...store, agentId, bookmarks: computed(() => store.state.bookmarks) };
}
