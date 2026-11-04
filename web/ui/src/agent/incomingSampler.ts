export function createIncomingSampler<T>(emit: (value: T) => void, intervalMs = 1000) {
  let last = 0;
  let pending: T | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;

  function clear() {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  }
  function flush() {
    if (disposed || pending === undefined) return;
    const value = pending;
    pending = undefined;
    clear();
    last = Date.now();
    emit(value);
  }
  function push(value: T) {
    if (disposed) return;
    pending = value;
    if (last === 0 || Date.now() - last >= intervalMs) flush();
    else if (timer === undefined) timer = setTimeout(flush, Math.max(0, intervalMs - (Date.now() - last)));
  }
  return {
    push,
    cancel() {
      disposed = true;
      pending = undefined;
      clear();
    },
  };
}
