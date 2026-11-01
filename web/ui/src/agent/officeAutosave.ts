export interface OfficeAutosaveOptions {
  canSave: () => boolean;
  /** Resolve true only after the canonical file commit is confirmed by Pogo. */
  save: (epoch: number) => Promise<boolean>;
  delayMs?: number;
  maxDelayMs?: number;
  retryMs?: number;
  failureRetryMs?: number;
  maxFailureRetryMs?: number;
}

export interface OfficeAutosaveState {
  dirtyEpoch: number;
  savedEpoch: number;
  pending: boolean;
  saving: boolean;
  failures: number;
  nextRetryAt: number | null;
  disposed: boolean;
}

export interface OfficeAutosave {
  /** Omit epoch to allocate a new local change; repeated explicit epochs are ignored. */
  changed: (epoch?: number) => number;
  /** Flush a known pending change; an engine "clean" event is not a saved receipt. */
  flush: () => void;
  /** Join an existing save or save the latest epoch once the active save completes. */
  saveNow: () => Promise<boolean>;
  /** Acknowledge only an epoch independently confirmed as committed by the host. */
  saved: (epoch: number) => void;
  state: () => OfficeAutosaveState;
  dispose: () => void;
}

function budget(value: number | undefined, fallback: number): number {
  if (value === undefined) return fallback;
  if (!Number.isFinite(value) || value <= 0) throw new RangeError("Office autosave delays must be positive finite numbers");
  return value;
}

function validEpoch(value: number): void {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError("Office autosave epoch must be a non-negative safe integer");
}

/** Per-view scheduler. It never reads a document, clears host dirty state, or retries a save in parallel. */
export function createOfficeAutosave(options: OfficeAutosaveOptions): OfficeAutosave {
  const delay = budget(options.delayMs, 3_000);
  const maxDelay = Math.max(delay, budget(options.maxDelayMs, 15_000));
  const retry = budget(options.retryMs, 1_000);
  const failureRetry = budget(options.failureRetryMs, 5_000);
  const maxFailureRetry = Math.max(failureRetry, budget(options.maxFailureRetryMs, 60_000));
  let dirtyEpoch = 0, savedEpoch = 0, failures = 0;
  let firstDirtyAt: number | null = null, lastDirtyAt = 0, firstAfterFlight: number | null = null;
  let urgentEpoch = 0, failureUntil = 0, unavailableUntil = 0, disposed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let flight: { epoch: number; promise: Promise<boolean> } | undefined;
  const pending = () => dirtyEpoch > savedEpoch;
  function clearTimer() { clearTimeout(timer); timer = undefined; }
  function schedule() {
    clearTimer();
    if (disposed || flight || !pending()) return;
    const now = Date.now();
    if (firstDirtyAt === null) firstDirtyAt = now;
    const due = urgentEpoch > savedEpoch ? now : Math.min(lastDirtyAt + delay, firstDirtyAt + maxDelay);
    const waitUntil = Math.max(due, failureUntil, unavailableUntil);
    timer = setTimeout(() => { timer = undefined; void start(false); }, Math.max(0, waitUntil - now));
  }
  function canSave() {
    try { return options.canSave() === true; }
    catch { return false; }
  }
  function start(manual: boolean): Promise<boolean> {
    if (disposed) return Promise.resolve(false);
    if (flight) return flight.promise;
    if (!pending()) { clearTimer(); return Promise.resolve(true); }
    const now = Date.now();
    if (!manual && Math.max(failureUntil, unavailableUntil) > now) { schedule(); return Promise.resolve(false); }
    if (!canSave()) {
      unavailableUntil = now + retry; schedule();
      return Promise.resolve(false);
    }
    clearTimer(); unavailableUntil = 0;
    const target = dirtyEpoch;
    firstAfterFlight = null;
    if (urgentEpoch <= target) urgentEpoch = 0;
    const active = { epoch: target, promise: Promise.resolve(false) };
    // Install the flight before invoking a host callback which may synchronously report another change.
    flight = active;
    active.promise = Promise.resolve().then(() => {
      if (disposed) return false;
      return options.save(target);
    }).catch(() => false).then(result => {
      if (disposed) return false;
      const success = result === true || savedEpoch >= target;
      if (success) {
        savedEpoch = Math.max(savedEpoch, target); failures = 0; failureUntil = 0;
        firstDirtyAt = pending() ? firstAfterFlight ?? Date.now() : null;
        if (urgentEpoch <= savedEpoch) urgentEpoch = 0;
      } else {
        failures += 1;
        failureUntil = Date.now() + Math.min(maxFailureRetry, failureRetry * 2 ** Math.min(failures - 1, 20));
      }
      return success;
    }).finally(() => {
      if (flight === active) flight = undefined;
      schedule();
    });
    return active.promise;
  }
  function saveNow(): Promise<boolean> {
    if (disposed) return Promise.resolve(false);
    const target = dirtyEpoch;
    if (flight) {
      const current = flight;
      if (target <= current.epoch) return current.promise;
      // A newer manual request joins the first flight and needs at most one following flight.
      return current.promise.then(success => {
        if (disposed) return false;
        if (savedEpoch >= target) return true;
        return success ? saveNow() : false;
      });
    }
    return start(true);
  }
  return {
    changed(epoch = dirtyEpoch + 1) {
      if (disposed) return dirtyEpoch;
      validEpoch(epoch);
      if (epoch <= dirtyEpoch) return dirtyEpoch;
      const now = Date.now();
      if (!pending()) firstDirtyAt = now;
      dirtyEpoch = epoch; lastDirtyAt = now;
      if (flight && epoch > flight.epoch && firstAfterFlight === null) firstAfterFlight = now;
      schedule(); return dirtyEpoch;
    },
    flush() { if (!disposed && pending()) { urgentEpoch = dirtyEpoch; schedule(); } },
    saveNow,
    saved(epoch) {
      if (disposed) return;
      validEpoch(epoch);
      if (epoch > dirtyEpoch) throw new RangeError("Cannot acknowledge an unobserved Office epoch");
      savedEpoch = Math.max(savedEpoch, epoch);
      if (!pending()) { firstDirtyAt = null; failures = 0; failureUntil = 0; unavailableUntil = 0; }
      if (urgentEpoch <= savedEpoch) urgentEpoch = 0;
      schedule();
    },
    state: () => ({ dirtyEpoch, savedEpoch, pending: pending(), saving: !!flight, failures,
      nextRetryAt: Math.max(failureUntil, unavailableUntil) > Date.now() ? Math.max(failureUntil, unavailableUntil) : null, disposed }),
    dispose() { disposed = true; clearTimer(); },
  };
}
