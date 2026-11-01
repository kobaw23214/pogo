import { describe, expect, it } from "vitest";
import { createSSRApp, defineComponent, h, ref, type WritableComputedRef } from "vue";
import { renderToString } from "vue/server-renderer";
import { cardDisclosureKey, createCardDisclosureState, provideCardDisclosure, useCardDisclosure } from "./cardDisclosure";

describe("page-local card disclosure choices", () => {
  it("keeps identical call ids isolated while switching back to a transcript", () => {
    const session = ref<string | undefined>("a");
    const state = createCardDisclosureState(() => session.value);
    const app = createSSRApp({}); app.provide(cardDisclosureKey, state);
    const open = app.runWithContext(() => useCardDisclosure(() => "call:c1:details", false));
    open.value = true;
    expect(state.hasOpen(["call:c1:details"])).toBe(true);
    session.value = "b";
    expect(open.value).toBe(false);
    open.value = false;
    session.value = "a";
    expect(open.value).toBe(true);
  });

  it("retains an explicit close instead of replacing it with a new default", () => {
    const state = createCardDisclosureState(() => "a");
    const app = createSSRApp({}); app.provide(cardDisclosureKey, state);
    const first = app.runWithContext(() => useCardDisclosure(() => "call:c1:details", true));
    first.value = false;
    const remounted = app.runWithContext(() => useCardDisclosure(() => "call:c1:details", true));
    expect(remounted.value).toBe(false);
    expect(state.choice("absent")).toBeUndefined();
  });

  it("uses local fallback without a provider and does not publish temporary ids", () => {
    const app = createSSRApp({});
    const id = ref<string | undefined>();
    const open = app.runWithContext(() => useCardDisclosure(() => id.value, false));
    open.value = true;
    expect(open.value).toBe(true);
    id.value = "m1:thinking";
    expect(open.value).toBe(false);

    const session = ref<string | undefined>();
    const state = createCardDisclosureState(() => session.value);
    app.provide(cardDisclosureKey, state);
    const beforeSnapshot = app.runWithContext(() => useCardDisclosure(() => "m1:thinking", false));
    beforeSnapshot.value = true;
    session.value = "accepted";
    expect(state.choice("m1:thinking")).toBeUndefined();
    expect(beforeSnapshot.value).toBe(false);
  });

  it("shadows a parent provider for child transcripts even before their identity arrives", async () => {
    let parent!: WritableComputedRef<boolean>, child!: WritableComputedRef<boolean>;
    const childSession = ref<string | undefined>();
    const ParentReader = defineComponent({ setup() {
      parent = useCardDisclosure(() => "m1:thinking", false); parent.value = true;
      return () => h("span", String(parent.value));
    } });
    const ChildReader = defineComponent({ setup() {
      child = useCardDisclosure(() => "m1:thinking", false);
      return () => h("span", String(child.value));
    } });
    const ChildBoundary = defineComponent({ setup() {
      provideCardDisclosure(() => childSession.value);
      return () => h(ChildReader);
    } });
    const Root = defineComponent({ setup() {
      provideCardDisclosure(() => "parent");
      return () => h("div", [h(ParentReader), h(ChildBoundary)]);
    } });
    await renderToString(createSSRApp(Root));
    expect(parent.value).toBe(true); expect(child.value).toBe(false);
    childSession.value = "child"; child.value = true; parent.value = false;
    expect(child.value).toBe(true); expect(parent.value).toBe(false);
    childSession.value = "another-child";
    expect(child.value).toBe(false);
    childSession.value = "child";
    expect(child.value).toBe(true);
  });
});
