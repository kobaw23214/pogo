import { computed, ref, watch, type Ref } from "vue";

export function createStudioEntryState(options: {
  enabled: boolean;
  selected: Ref<string | null>;
  displayed: Ref<string | null>;
  resolving?: Ref<boolean>;
  switching?: Ref<boolean>;
}) {
  const { enabled, selected, displayed, resolving, switching } = options;
  const contentReady = ref(!enabled);
  const draftReady = ref(false);
  const snapshotReady = computed(() => !!selected.value && selected.value === displayed.value && !switching?.value);
  const destinationReady = computed(() => !resolving?.value && (
    selected.value ? snapshotReady.value : draftReady.value
  ));

  watch(destinationReady, ready => {
    if (enabled && ready) contentReady.value = true;
  }, { flush: "sync" });

  return {
    // Keep settled transcripts during later switches, but never expose an
    // empty page while the entry route is still choosing its destination.
    loading: computed(() => enabled && (!contentReady.value || !!resolving?.value)),
    emptyReady: computed(() => !enabled || destinationReady.value),
    reset() {
      draftReady.value = false;
      if (enabled) contentReady.value = !resolving?.value && snapshotReady.value;
    },
    finishWithoutSession() {
      if (enabled && !selected.value) draftReady.value = true;
    },
  };
}

export function createStudioCatalogLoading(loading: Ref<boolean>, succeeded?: Ref<boolean>) {
  const resolved = ref(false);
  watch(() => [loading.value, succeeded?.value ?? true] as const, ([pending, success]) => {
    // A failed first request leaves the catalog unknown. Its retry needs the
    // same placeholder; only refreshes after a successful load retain content.
    if (!pending && success) resolved.value = true;
  }, { immediate: true, flush: "sync" });
  return computed(() => loading.value && !resolved.value);
}
