import { onBeforeUnmount, ref, watch } from "vue";
import type { InformationEntry } from "@/api/information";
import type { ComposerPart } from "./composerParts";

/** The caller chooses the destination namespace; this only owns the picker lifetime. */
export function useInformationFilePicker(options: {
  scope: () => string;
  allowedKinds?: readonly InformationEntry["kind"][];
  resolve: (entry: InformationEntry) => Promise<ComposerPart[]>;
  errorText: (error: unknown) => string;
}) {
  const open = ref(false), busy = ref(false), error = ref("");
  let pending: ((parts: ComposerPart[] | null) => void) | undefined;
  let generation = 0;
  function finish(parts: ComposerPart[] | null) {
    generation++;
    const complete = pending;
    pending = undefined; open.value = false; busy.value = false; error.value = "";
    complete?.(parts);
  }
  function cancel() { finish(null); }
  function choose(): Promise<ComposerPart[] | null> {
    if (pending) return Promise.resolve(null);
    error.value = ""; open.value = true;
    return new Promise(resolve => { pending = resolve; });
  }
  async function select(entry: InformationEntry) {
    if (!pending || busy.value || !(options.allowedKinds || ["file"]).includes(entry.kind) || entry.kind === "folder" || entry.kind === "file" && !entry.file) return;
    const ticket = ++generation, scope = options.scope();
    const current = () => !!pending && open.value && generation === ticket && options.scope() === scope;
    busy.value = true; error.value = "";
    try {
      const parts = await options.resolve(entry);
      if (current()) finish(parts);
    } catch (value) { if (current()) error.value = options.errorText(value); }
    finally { if (current()) busy.value = false; }
  }
  watch(options.scope, cancel, { flush: "sync" });
  onBeforeUnmount(cancel);
  return { open, busy, error, choose, select, cancel };
}
