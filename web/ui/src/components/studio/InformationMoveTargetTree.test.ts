import { afterEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { InformationEntry } from "@/api/information";
import type { InformationMoveTargetRow } from "./informationMoveTargets";
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
import Tree from "./InformationMoveTargetTree.vue";
const entry = (id: string, parent_id = ""): InformationEntry => ({ id, parent_id, name: id, kind: "folder", version: 1, created_at: "2026-10-18", updated_at: "2026-10-18" });
interface Controller { expanded: Ref<string[]>; focused: Ref<string>; rows: Ref<InformationMoveTargetRow[]>; destinationValid: Ref<boolean>; select(id: string): void; toggle(id: string): void; expandAll(): void; collapseAll(): void; onKey(event: KeyboardEvent, id: string): void }
const scopes: ReturnType<typeof effectScope>[] = [];
function mount() {
  const props = reactive({ entries: [entry("project"), entry("child", "project"), entry("other")], movingId: "child", currentParent: "project", modelValue: "project", disabled: false });
  const scope = effectScope(); scopes.push(scope); const emit = vi.fn();
  const c = scope.run(() => (Tree as unknown as { setup(props: unknown, ctx: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); });
describe("Information move tree interaction", () => {
  it("starts with current ancestry expanded, selects root separately, and refuses a blocked folder", () => {
    const { c, emit } = mount(); expect(c.expanded.value).toEqual(["project"]); expect(c.focused.value).toBe("project");
    c.select(""); expect(emit).toHaveBeenCalledExactlyOnceWith("update:modelValue", ""); c.select("child"); expect(emit).toHaveBeenCalledOnce();
  });
  it("offers keyboard traversal, separate expansion and explicit Enter selection", () => {
    const { c, emit } = mount(); const key = (value: string, id: string) => { const event = { key: value, preventDefault: vi.fn() } as unknown as KeyboardEvent; c.onKey(event, id); return event; };
    key("ArrowDown", ""); expect(c.focused.value).toBe("other"); expect(emit).not.toHaveBeenCalled();
    key("ArrowLeft", "project"); expect(c.expanded.value).toEqual([]); key("ArrowRight", "project"); expect(c.expanded.value).toEqual(["project"]);
    key("End", ""); expect(c.focused.value).toBe("child"); key("Home", "child"); expect(c.focused.value).toBe("");
    key("Enter", "other"); expect(emit).toHaveBeenCalledExactlyOnceWith("update:modelValue", "other");
  });
  it("disables all interaction while an operation is pending and reports removed destinations", () => {
    const { c, props, emit } = mount(); props.disabled = true; c.select(""); c.toggle("project"); c.collapseAll(); c.expandAll(); c.onKey({ key: "Enter", preventDefault: vi.fn() } as unknown as KeyboardEvent, "other");
    expect(c.expanded.value).toEqual(["project"]); expect(emit).not.toHaveBeenCalled(); props.modelValue = "missing"; expect(c.destinationValid.value).toBe(false);
  });
});
