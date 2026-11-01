const eventName = "pogo:starter-shortcuts-changed";
const storagePrefix = "pogo:starter-shortcuts-change:";

/** Notifications only. The source remains the server's Agent/card document. */
export function notifyStarterShortcutsChanged(agentId = "") {
  if (typeof window === "undefined") return;
  window.dispatchEvent?.(new CustomEvent(eventName, { detail: { agentId } }));
  try { window.localStorage?.setItem(storagePrefix + agentId, String(Date.now()) + ":" + Math.random()); } catch { /* Private storage mode: same-window and focus refresh still work. */ }
}

export function observeStarterShortcutsChanged(agentId: () => string, refresh: () => void) {
  if (typeof window === "undefined") return () => {};
  const changed = (event: Event) => { if ((event as CustomEvent).detail?.agentId === agentId()) refresh(); };
  const storage = (event: StorageEvent) => { if (event.key === storagePrefix + agentId()) refresh(); };
  window.addEventListener("pogo:starter-shortcuts-changed", changed);
  window.addEventListener("storage", storage);
  window.addEventListener("focus", refresh);
  return () => { window.removeEventListener(eventName, changed); window.removeEventListener("storage", storage); window.removeEventListener("focus", refresh); };
}
