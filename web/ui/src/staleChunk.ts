// Healing for a tab left open across a redeploy.
//
// Every chunk in the bundle is content-hashed, and a redeploy changes every
// hash at once. A page loaded before the redeploy still references the old
// names; whatever it lazy-loads afterwards — mermaid, echarts — asks for a
// file that no longer exists and the browser raises "Failed to fetch
// dynamically imported module" (or the Firefox/older-Chrome wordings). One
// reload heals it: fresh index.html, fresh names, same URL.
//
// The reload is once per page load, guarded in sessionStorage, so a genuinely
// broken deployment cannot turn into a reload loop; the second failure lands
// in the component's normal error display instead. The guard is cleared on a
// successful boot (see main.ts), so the next redeploy's stale tab heals too.

const GUARD = "pogo-reloaded-for-stale-chunk";

export function isStaleChunkError(cause: unknown): boolean {
  const msg = cause instanceof Error ? cause.message : String(cause ?? "");
  return (
    msg.includes("dynamically imported module") ||
    msg.includes("dynamically requested module") ||
    msg.includes("Importing a module script failed")
  );
}

export function reloadOnceForStaleChunk(): void {
  if (sessionStorage.getItem(GUARD)) return;
  sessionStorage.setItem(GUARD, "1");
  window.location.reload();
}

export function clearStaleChunkGuard(): void {
  sessionStorage.removeItem(GUARD);
}

// The runtimes' shared rejection handler: heal if the cause is a stale tab,
// then rethrow either way — the caller's error display still owns every
// failure that is not this one.
export function healStaleChunk(cause: unknown): never {
  if (isStaleChunkError(cause)) reloadOnceForStaleChunk();
  throw cause;
}
