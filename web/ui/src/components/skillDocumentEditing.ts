import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { SkillDocumentContent, SkillDocumentUpdate } from "@/api/types";

/** Revision-bound drafts never adopt refreshed server text until the user
 * saves or explicitly discards them. Previewing a draft does not save it. */
export function useSkillDocumentEditing(options: {
  scope: () => string;
  document: () => SkillDocumentContent | null;
  path: () => string;
  enabled: () => boolean;
  save: (input: SkillDocumentUpdate) => Promise<SkillDocumentContent>;
  saved: (document: SkillDocumentContent) => void;
  confirmDiscard: () => Promise<boolean>;
}) {
  const editing = ref(false), mode = ref<"preview" | "edit">("preview");
  const draft = ref(""), original = ref(""), revision = ref("");
  const saving = ref(false), confirming = ref(false), error = ref(""), conflict = ref(false);
  const dirty = computed(() => editing.value && draft.value !== original.value);
  const canEdit = computed(() => options.enabled() && options.path() === "SKILL.md" && options.document()?.editable === true && !!options.document()?.revision && !options.document()?.truncated);
  let generation = 0, disposed = false;
  function reset() { generation++; editing.value = false; mode.value = "preview"; draft.value = original.value = revision.value = error.value = ""; saving.value = confirming.value = conflict.value = false; }
  watch(options.scope, reset, { flush: "sync" });
  function edit() {
    if (saving.value || confirming.value || !canEdit.value) return;
    if (!editing.value) {
      const current = options.document()!;
      draft.value = original.value = current.content;
      revision.value = current.revision!;
      error.value = ""; conflict.value = false; editing.value = true;
    }
    mode.value = "edit";
  }
  function preview() { if (!saving.value && !confirming.value) mode.value = "preview"; }
  async function confirmLeave(): Promise<boolean> {
    if (saving.value || confirming.value) return false;
    if (!dirty.value) { reset(); return true; }
    const current = generation, scope = options.scope();
    confirming.value = true;
    try {
      const discard = await options.confirmDiscard();
      if (disposed || current !== generation || scope !== options.scope() || !discard) return false;
      reset(); return true;
    } finally { if (current === generation) confirming.value = false; }
  }
  async function save(): Promise<boolean> {
    if (!editing.value || !dirty.value || saving.value || confirming.value || !canEdit.value || conflict.value) return false;
    const current = ++generation, scope = options.scope();
    const input = { content: draft.value, expected_revision: revision.value };
    saving.value = true; error.value = "";
    try {
      const document = await options.save(input);
      if (disposed || current !== generation || scope !== options.scope()) return false;
      reset(); options.saved(document); return true;
    } catch (value) {
      if (!disposed && current === generation && scope === options.scope()) {
        error.value = value instanceof Error ? value.message : String(value);
        conflict.value = (value as { status?: number })?.status === 409;
      }
      return false;
    } finally { if (current === generation) saving.value = false; }
  }
  onBeforeUnmount(() => { disposed = true; generation++; });
  return { editing, mode, draft, revision, dirty, canEdit, saving, confirming, error, conflict, edit, preview, save, confirmLeave };
}
