/** Retain a few recently viewed receivers without encoding every open Sheet.
 * The Pane owns input draining and capture suspension before calling hide(). */
export function createBrowserWarmViewPool(maxHidden = 2, idleMs = 5 * 60_000) {
  type Entry = { owner: object; evict: () => void; timer?: ReturnType<typeof setTimeout> };
  const sessions = new Map<string, Map<object, Entry>>();
  function remove(session: string, entry: Entry) {
    clearTimeout(entry.timer); entry.timer = undefined;
    const views = sessions.get(session);
    if (views?.get(entry.owner) !== entry) return;
    views.delete(entry.owner);
    if (!views.size) sessions.delete(session);
  }
  function evict(session: string, entry: Entry) {
    if (sessions.get(session)?.get(entry.owner) !== entry) return;
    remove(session, entry);
    entry.evict();
  }
  return {
    register(session: string, owner: object, callbacks: { evict: () => void }) {
      const entry: Entry = { owner, evict: callbacks.evict };
      let disposed = false;
      return {
        show() { if (!disposed) remove(session, entry); },
        hide() {
          if (disposed || sessions.get(session)?.has(owner)) return;
          const views = sessions.get(session) ?? new Map<object, Entry>();
          sessions.set(session, views); views.set(owner, entry);
          entry.timer = setTimeout(() => evict(session, entry), idleMs);
          while (views.size > maxHidden) evict(session, views.values().next().value!);
        },
        dispose() { remove(session, entry); disposed = true; },
      };
    },
  };
}

const browserWarmViews = createBrowserWarmViewPool();
export const registerBrowserWarmView = browserWarmViews.register;
