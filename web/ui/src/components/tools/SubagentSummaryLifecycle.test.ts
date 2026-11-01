import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, nextTick, reactive, type Ref } from "vue";
import { renderToString } from "vue/server-renderer";
import { cardDisclosureKey, createCardDisclosureState } from "@/agent/cardDisclosure";
import type { SubagentFrame, ToolResult } from "@/api/types";
import type { SubagentRow } from "@/agent/timeline";
import { i18n, setLocale } from "@/i18n";
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }), onMounted: () => {}, onBeforeUnmount: () => {} }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("../CodeBlock.vue", () => ({ default: defineComponent({ props: ["code"], setup: props => () => h("pre", props.code) }) }));
import SubagentResult from "./SubagentResult.vue";

type Explore = Extract<SubagentRow, { kind: "explore" }>;
type Controller = { rows: Ref<SubagentRow[]>; expanded: Ref<boolean>; isGroupOpen(row: Explore): boolean; toggleGroup(row: Explore): void };
const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => scopes.splice(0).forEach(scope => scope.stop()));
setLocale("zh-CN");

function mount(live = true, frames: SubagentFrame[] = [{ type: "turn_start", turn: 1 },
  { type: "tool_start", name: "read", call_id: "a", args: { path: "FIRST_FILE.txt" } },
  { type: "tool_end", name: "read", call_id: "a", text: "first payload" }]) {
  const props = reactive<{ callId: string; running: boolean; frames: SubagentFrame[]; result?: ToolResult }>({ callId: "parent", running: live, frames });
  const state = createCardDisclosureState(() => "session"), app = createSSRApp({}); app.provide(cardDisclosureKey, state);
  const scope = effectScope(); scopes.push(scope);
  const c = app.runWithContext(() => scope.run(() => (SubagentResult as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} })))!;
  c.expanded.value = true;
  const component = { ...SubagentResult, setup: () => c } as Parameters<typeof h>[0];
  const render = () => {
    const view = createSSRApp({ render: () => h(component, props) }).use(i18n);
    view.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
    return renderToString(view);
  };
  const group = () => c.rows.value.find((row): row is Explore => row.kind === "explore")!;
  return { c, props, group, state, render };
}

describe("live child activity visibility", () => {
  it("keeps the first completed read visible when a second starts, finishes, and the child ends", async () => {
    const { props, c, group, render } = mount();
    expect(c.isGroupOpen(group())).toBe(true);
    const before = await render(); expect(before).toContain("FIRST_FILE.txt"); expect(before).not.toMatch(/class="group-body" style="display:none/);
    props.frames.push({ type: "tool_start", name: "read", call_id: "b", args: { path: "SECOND_FILE.txt" } });
    await nextTick();
    props.frames.push({ type: "tool_end", name: "read", call_id: "b", text: "second payload" });
    await nextTick();
    expect(c.isGroupOpen(group())).toBe(true); expect(await render()).not.toMatch(/class="group-body" style="display:none/);
    props.frames.push({ type: "run_end", stop_reason: "end_turn" }); props.running = false;
    props.result = { call_id: "parent", text: "Final result", details: { id: "late-run", mode: "explore" } };
    await nextTick();
    const ended = await render(); expect(c.isGroupOpen(group())).toBe(true);
    expect(ended).toContain("FIRST_FILE.txt"); expect(ended).toContain("SECOND_FILE.txt"); expect(ended).not.toMatch(/class="group-body" style="display:none/);
  });

  it("uses a quiet history default without undoing a deliberate open or close", async () => {
    const frames: SubagentFrame[] = [{ type: "turn_start", turn: 1 },
      { type: "tool_start", name: "read", call_id: "a" }, { type: "tool_end", name: "read", call_id: "a", text: "a" },
      { type: "tool_start", name: "read", call_id: "b" }, { type: "tool_end", name: "read", call_id: "b", text: "b" }];
    const { props, c, group, render } = mount(false, frames);
    expect(c.isGroupOpen(group())).toBe(false); expect(await render()).toMatch(/class="group-body" style="display:none/);
    c.toggleGroup(group()); await nextTick(); expect(c.isGroupOpen(group())).toBe(true);
    props.frames.push({ type: "run_end", stop_reason: "end_turn" }); await nextTick();
    expect(c.isGroupOpen(group())).toBe(true); expect(await render()).not.toMatch(/class="group-body" style="display:none/);
    c.toggleGroup(group()); await nextTick(); expect(c.isGroupOpen(group())).toBe(false);
  });
});
