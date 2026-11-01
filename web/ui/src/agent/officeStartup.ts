/** Each Office startup stage gets five minutes, independent of other stages. */
export const OFFICE_STARTUP_TIMEOUT_MS = 300_000;
export type OfficeStartupPhase = "preparing" | "checking" | "opening" | "engine" | "document" | "bridge" | "recovering";
export interface OfficeStartupProgress { phase: OfficeStartupPhase; attempt: number; elapsedMs: number; timeoutMs: number; }
export interface OfficeStartupOptions {
  signal?: AbortSignal;
  /** A longer stage budget is allowed; shorter values retain the five-minute minimum. */
  timeoutMs?: number;
  maxAttempts?: number;
  onAttempt?: (progress: OfficeStartupProgress) => void;
}

export function officeStartupTimeout(phase: OfficeStartupPhase, timeoutMs = OFFICE_STARTUP_TIMEOUT_MS): Error & { code: "OFFICE_STARTUP_TIMEOUT"; phase: OfficeStartupPhase; timeoutMs: number } {
  return Object.assign(new Error(`Office ${phase} startup timed out after ${Math.ceil(timeoutMs / 60_000)} minutes`), { code: "OFFICE_STARTUP_TIMEOUT" as const, phase, timeoutMs });
}
function cancelled(): Error { return Object.assign(new Error("Office startup was cancelled"), { name: "AbortError", code: "OFFICE_STARTUP_CANCELLED" }); }
function abortReason(signal: AbortSignal): Error { return signal.reason instanceof Error ? signal.reason : cancelled(); }

/** Cancel one waiter without cancelling a shared SDK load or replaying work. */
export function waitForOfficeStartup<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const finish = (done: () => void) => { if (settled) return; settled = true; signal.removeEventListener("abort", aborted); done(); };
    const aborted = () => finish(() => reject(abortReason(signal)));
    // Attach both handlers even for an already cancelled waiter, so a shared
    // task that subsequently fails cannot become an unhandled rejection.
    promise.then(value => finish(() => resolve(value)), error => finish(() => reject(error)));
    if (signal.aborted) { aborted(); return; }
    signal.addEventListener("abort", aborted, { once: true });
  });
}

function canRetry(error: unknown): boolean {
  const value = error as { name?: string; code?: string; status?: number } | null;
  if (!value || value.name === "AbortError" || value.code === "OFFICE_STARTUP_TIMEOUT" || value.code === "OFFICE_STARTUP_CANCELLED") return false;
  if (value.code === "OFFICE_CONFIG_INVALID" || value.code === "OFFICE_OPEN_TIMEOUT") return false;
  if (typeof value.status === "number") return [408, 429, 502, 503, 504].includes(value.status);
  // The browser's fetch rejects network failures as TypeError. Never retry
  // arbitrary application/identity errors or permissions/conflicts (401/403/409).
  return value.name === "TypeError";
}
function pause(delay: number, signal: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const aborted = () => { clearTimeout(timer); signal.removeEventListener("abort", aborted); reject(abortReason(signal)); };
    if (signal.aborted) { aborted(); return; }
    signal.addEventListener("abort", aborted, { once: true });
    timer = setTimeout(() => { signal.removeEventListener("abort", aborted); resolve(); }, delay);
  });
}

/** For reads or server-idempotent startup requests only. The caller MUST keep
 * the same instance/operation identity in its task closure across attempts.
 * Retries share one stage deadline; this never retries document mutations.
 */
export async function retryOfficeStartup<T>(phase: OfficeStartupPhase, task: (signal: AbortSignal, attempt: number) => Promise<T>, options: OfficeStartupOptions = {}): Promise<T> {
  if (options.signal?.aborted) throw cancelled();
  const requested = options.timeoutMs;
  const timeoutMs = typeof requested === "number" && Number.isFinite(requested) ? Math.max(OFFICE_STARTUP_TIMEOUT_MS, requested) : OFFICE_STARTUP_TIMEOUT_MS;
  const count = options.maxAttempts;
  const attempts = typeof count === "number" && Number.isFinite(count) ? Math.min(3, Math.max(1, Math.floor(count))) : 3;
  const startedAt = Date.now(), controller = new AbortController();
  const abort = () => controller.abort(cancelled());
  options.signal?.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(() => controller.abort(officeStartupTimeout(phase, timeoutMs)), timeoutMs);
  try {
    for (let attempt = 1; ; attempt++) {
      if (controller.signal.aborted) throw abortReason(controller.signal);
      options.onAttempt?.({ phase, attempt, elapsedMs: Math.max(0, Date.now() - startedAt), timeoutMs });
      try { return await waitForOfficeStartup(Promise.resolve().then(() => { if (controller.signal.aborted) throw abortReason(controller.signal); return task(controller.signal, attempt); }), controller.signal); }
      catch (error) {
        if (controller.signal.aborted) throw abortReason(controller.signal);
        if (attempt >= attempts || !canRetry(error)) throw error;
        await pause(500 * 2 ** (attempt - 1), controller.signal);
      }
    }
  } finally {
    clearTimeout(timer);
    options.signal?.removeEventListener("abort", abort);
  }
}
