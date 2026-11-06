import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, ref, type Ref } from "vue";
import { useWebsitePopoverDismiss } from "./useWebsitePopoverDismiss";
const scopes: ReturnType<typeof effectScope>[] = [];
function mount() {
  const page = new EventTarget(), frameWindow = new EventTarget(); vi.stubGlobal("document", page); vi.stubGlobal("window", frameWindow);
  const expanded = ref(true), trigger = ref({ focus: vi.fn() }) as unknown as Ref<HTMLElement | null>, content = ref({ focus: vi.fn() }) as unknown as Ref<HTMLElement | null>;
  const scope = effectScope(); scopes.push(scope); scope.run(() => useWebsitePopoverDismiss(expanded, trigger, content));
  const input = (name: string, path: EventTarget[] = [], key?: string) => {
    const event = new Event(name, { cancelable: true }); Object.defineProperty(event, "composedPath", { value: () => path });
    if (key) Object.defineProperty(event, "key", { value: key }); page.dispatchEvent(event); return event;
  };
  return { page, frameWindow, scope, expanded, trigger, content, input };
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); });
describe("website card dismissal beside an Office iframe", () => {
  it("does not turn iframe autofocus or window blur into a card dismissal or steal focus", () => {
    const { frameWindow, expanded, trigger, content, input } = mount();
    frameWindow.dispatchEvent(new Event("blur")); input("focusin"); input("blur");
    expect(expanded.value).toBe(true); expect(trigger.value!.focus).not.toHaveBeenCalled(); expect(content.value!.focus).not.toHaveBeenCalled();
  });
  it("keeps inside input working and closes on deliberate outside input or Escape without consuming it", () => {
    const { expanded, trigger, content, input } = mount();
    input("pointerdown", [trigger.value!]); input("click", [content.value!]); expect(expanded.value).toBe(true);
    const outside = input("pointerdown"); expect(expanded.value).toBe(false); expect(outside.defaultPrevented).toBe(false);
    expanded.value = true; const escape = input("keydown", [], "Escape"); expect(expanded.value).toBe(false); expect(escape.defaultPrevented).toBe(false);
    expanded.value = true; input("click"); expect(expanded.value).toBe(false);
  });
  it("removes its page listeners when the owning widget unmounts", () => {
    const { scope, expanded, input } = mount(); scope.stop(); input("pointerdown"); input("keydown", [], "Escape"); expect(expanded.value).toBe(true);
  });
});
