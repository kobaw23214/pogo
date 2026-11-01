export type BrowserPickerRequestKind = "preview" | "commit" | "state";
export const PICKER_TRANSPORT_DEADLINE_MS = { preview: 2000, commit: 4000, state: 3000 } as const;

/** A transport deadline is not a browser cancellation acknowledgement. Even
 * when fetch ignores AbortSignal, the caller settles and treats an unknown
 * preview/commit outcome as pending until a new bound state proves draining. */
export function browserPickerTransport<T>(work: (signal: AbortSignal) => Promise<T>, kind: BrowserPickerRequestKind, options: { signal?: AbortSignal; timeoutMs?: number } = {}): Promise<T> {
  const controller = new AbortController();
  const budget = options.timeoutMs ?? PICKER_TRANSPORT_DEADLINE_MS[kind];
  let timer: ReturnType<typeof setTimeout> | undefined;
  let abort: (() => void) | undefined;
  const deadline = new Promise<T>((_resolve, reject) => {
    timer = setTimeout(() => {
      const error = Object.assign(new Error(kind === "state" ? "Browser observation status request timed out" : `Element ${kind} transport timed out; observation outcome is pending`), {
        status: 504,
        code: kind === "state" ? "PICK_STATE_TIMEOUT" : "PICK_OBSERVATION_PENDING",
      });
      // Settle with the explicit uncertainty classification before aborting
      // fetch, which may reject with a less informative generic AbortError.
      reject(error); controller.abort(error);
    }, budget);
    if (options.signal) {
      abort = () => {
        const error = Object.assign(new Error(kind === "state" ? "Browser observation status request was superseded" : "Element observation transport was interrupted; outcome is pending"), { name: "AbortError", code: kind === "state" ? "PICK_STATE_CANCELLED" : "PICK_OBSERVATION_PENDING" });
        reject(error); controller.abort(error);
      };
      if (options.signal.aborted) abort(); else options.signal.addEventListener("abort", abort, { once: true });
    }
  });
  const request = Promise.resolve().then(() => {
    if (controller.signal.aborted) throw controller.signal.reason;
    return work(controller.signal);
  }).catch(cause => {
    const error = cause as { status?: number; code?: string; name?: string };
    // A broken response path cannot prove that the worker did not dispatch
    // the read. Keep definite request/auth/stale-page rejections unchanged.
    if (kind !== "state" && error?.code !== "PICK_OBSERVATION_PENDING" && (typeof error?.status !== "number" || error.status === 502 || error.status === 504 || error.name === "AbortError")) {
      throw Object.assign(new Error("Element observation response was lost; outcome is pending"), { status: error?.status ?? 504, code: "PICK_OBSERVATION_PENDING", cause });
    }
    throw cause;
  });
  return Promise.race([request, deadline]).finally(() => {
    clearTimeout(timer);
    if (abort) options.signal?.removeEventListener("abort", abort);
  });
}
