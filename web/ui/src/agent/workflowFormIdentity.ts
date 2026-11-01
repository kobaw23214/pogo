import { watch, type WatchOptions, type WatchStopHandle } from "vue";

type FormIdentityValue = string | number | boolean | null | undefined;

/** Compare individual scalar identities. A getter returning a fresh array
 * would reset the form whenever polling replaces the surrounding Run object. */
export function watchWorkflowFormIdentity(
  sources: readonly (() => FormIdentityValue)[],
  reset: () => void,
  options: Pick<WatchOptions, "immediate"> = {},
): WatchStopHandle {
  return watch([...sources], reset, options);
}
