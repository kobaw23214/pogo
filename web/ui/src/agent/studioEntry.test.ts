import { afterEach, describe, expect, it } from "vitest";
import { effectScope, ref, type EffectScope } from "vue";
import { createStudioCatalogLoading, createStudioEntryState } from "./studioEntry";

const scopes: EffectScope[] = [];
afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop();
});

function mount(enabled = true) {
  const selected = ref<string | null>(null);
  const displayed = ref<string | null>(null);
  const scope = effectScope();
  scopes.push(scope);
  const state = scope.run(() => createStudioEntryState({ enabled, selected, displayed }))!;
  return { state, selected, displayed };
}

describe("Studio entry readiness", () => {
  it("does not mistake an unknown transcript for an empty conversation", () => {
    const { state, selected, displayed } = mount();
    expect(state.loading.value).toBe(true);

    selected.value = "session-a";
    expect(state.loading.value).toBe(true);
    displayed.value = "another-session";
    expect(state.loading.value).toBe(true);

    displayed.value = "session-a";
    expect(state.loading.value).toBe(false);
  });

  it("keeps the settled transcript visible during later session switches", () => {
    const { state, selected, displayed } = mount();
    selected.value = displayed.value = "session-a";
    expect(state.loading.value).toBe(false);

    selected.value = "session-b";
    expect(state.loading.value).toBe(false);
    displayed.value = "session-b";
    expect(state.loading.value).toBe(false);
  });

  it("reveals a deliberate local draft but never overrides a pending snapshot", () => {
    const { state, selected, displayed } = mount();
    selected.value = "session-a";
    state.finishWithoutSession();
    expect(state.loading.value).toBe(true);

    displayed.value = "session-a";
    expect(state.loading.value).toBe(false);
    state.reset();
    expect(state.loading.value).toBe(false);

    selected.value = null;
    displayed.value = null;
    state.reset();
    expect(state.loading.value).toBe(true);
    state.finishWithoutSession();
    expect(state.loading.value).toBe(false);
  });

  it("does not change the ordinary workbench", () => {
    const { state, selected, displayed } = mount(false);
    expect(state.loading.value).toBe(false);
    state.reset();
    state.finishWithoutSession();
    selected.value = displayed.value = "session-a";
    expect(state.loading.value).toBe(false);
  });
});

describe("Studio catalog loading", () => {
  it("separates the first unresolved load from later refreshes", () => {
    const loading = ref(true);
    const scope = effectScope();
    scopes.push(scope);
    const initial = scope.run(() => createStudioCatalogLoading(loading))!;
    expect(initial.value).toBe(true);

    loading.value = false;
    expect(initial.value).toBe(false);
    loading.value = true;
    expect(initial.value).toBe(false);
  });

  it("does not add a skeleton when cached management data is already available", () => {
    const scope = effectScope();
    scopes.push(scope);
    const initial = scope.run(() => createStudioCatalogLoading(ref(false)))!;
    expect(initial.value).toBe(false);
  });
});
