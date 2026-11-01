import { computed, inject, provide, ref, type InjectionKey, type Ref, type WritableComputedRef } from "vue";

/** Choices belong to the transcript on screen, not to component instances or
 * the session a still-pending navigation is trying to open. No transcript
 * content is retained here; this provider only lives as long as its page. */
export interface CardDisclosureState {
  version: Ref<number>;
  available(): boolean;
  choice(key: string | undefined): boolean | undefined;
  set(key: string | undefined, open: boolean): void;
  hasOpen(keys: Iterable<string>): boolean;
}

export const cardDisclosureKey: InjectionKey<CardDisclosureState> = Symbol("card-disclosure");

export function createCardDisclosureState(scope: () => string | undefined): CardDisclosureState {
  const sessions = new Map<string, Map<string, boolean>>();
  const version = ref(0);
  const choice = (key: string | undefined): boolean | undefined => {
    void version.value;
    const sid = scope();
    return sid && key ? sessions.get(sid)?.get(key) : undefined;
  };
  return {
    version,
    available: () => !!scope(),
    choice,
    set(key, open) {
      const sid = scope();
      if (!sid || !key) return;
      let entries = sessions.get(sid);
      if (!entries) sessions.set(sid, entries = new Map());
      if (entries.get(key) === open) return;
      entries.set(key, open);
      version.value++;
    },
    hasOpen: keys => [...keys].some(key => choice(key) === true),
  };
}

export function provideCardDisclosure(scope: () => string | undefined): CardDisclosureState {
  const state = createCardDisclosureState(scope);
  provide(cardDisclosureKey, state);
  return state;
}

export function useCardDisclosureState(): CardDisclosureState | undefined {
  return inject(cardDisclosureKey, undefined);
}

/** Missing provider/scope/identity keeps the component's ordinary local
 * behavior. A scoped component with no saved choice always uses its default,
 * never a previous session's local value. */
export function useCardDisclosure(key: () => string | undefined, fallback: boolean): WritableComputedRef<boolean> {
  const state = useCardDisclosureState();
  const local = new Map<string | undefined, boolean>();
  const localVersion = ref(0);
  return computed({
    get() {
      const id = key();
      if (id && state?.available()) return state.choice(id) ?? fallback;
      void localVersion.value;
      return local.get(id) ?? fallback;
    },
    set(open) {
      const id = key();
      if (id && state?.available()) state.set(id, open);
      else { local.set(id, open); localVersion.value++; }
    },
  });
}
