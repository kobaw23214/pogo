import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, nextTick, reactive, ref, type ComputedRef, type Ref, type WritableComputedRef } from "vue";
import { renderToString } from "vue/server-renderer";
import { cardDisclosureKey, createCardDisclosureState, type CardDisclosureState } from "@/agent/cardDisclosure";
import { gt, i18n, setLocale } from "@/i18n";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useId: () => "thinking-test", useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({ ...await original<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => gt(key) }) }));
vi.mock("./Chevron.vue", () => ({ default: defineComponent({ props: ["open"], setup: props => () => h("span", { "data-chevron-open": props.open }) }) }));
import ThinkingBlock from "./ThinkingBlock.vue";
import StreamTailWindow from "./StreamTailWindow.vue";

const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); setLocale("zh-CN"); });
setLocale("zh-CN");
async function render(props: Record<string, unknown>, state?: CardDisclosureState) {
  const app = createSSRApp({ render: () => h(ThinkingBlock, { id: "m1", text: "first fragment", streaming: true, ...props }) }).use(i18n);
  if (state) app.provide(cardDisclosureKey, state);
  return renderToString(app);
}
interface Controller {
  expanded: WritableComputedRef<boolean>;
  fullOpen: ComputedRef<boolean>;
  body: Ref<HTMLElement | null>;
  toggle(): void;
  onScroll(): void;
  onWheel(event: Pick<WheelEvent, "deltaY">): void;
  onKeydown(event: Pick<KeyboardEvent, "key">): void;
}
function mount(extra: Record<string, unknown> = {}, state = createCardDisclosureState(() => "session-a")) {
  const props = reactive({ id: "m1", text: "first", streaming: true, compact: false, ...extra });
  const app = createSSRApp({}); app.provide(cardDisclosureKey, state);
  const scope = effectScope(); scopes.push(scope);
  const c = app.runWithContext(() => scope.run(() => (ThinkingBlock as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} })))!;
  return { props, c, state };
}

describe("thinking text presentation", () => {
  it("shows an unfinished first fragment immediately and labels it as a recent excerpt", async () => {
    const html = await render({ text: "正在" });
    expect(html).toContain("正在"); expect(html).toContain("最近片段"); expect(html).toContain('data-tool-kind="thinking"');
    expect(html).toContain('aria-expanded="false"'); expect(html).not.toContain("每句话");
  });
  it("renders the actual latest rows while streaming and settles to a single header line", async () => {
    const text = Array.from({ length: 25 }, (_, n) => `fixture row ${n}`).join("\n");
    const live = await render({ text, streaming: true });
    expect(live).toContain("fixture row 24"); expect(live).not.toContain("fixture row 0");
    expect(live).not.toContain('class="thinking-full"');
    const settled = await render({ text, streaming: false, durationMs: 3400 });
    expect(settled).not.toContain("fixture row 24"); expect(settled).not.toContain("最近片段");
    expect(settled).toContain('data-tool-kind="thinking"'); expect(settled).toContain("3.4s"); expect(settled).toContain('aria-expanded="false"');
  });
  it("exposes complete verbatim text in one expansion and escapes returned HTML", async () => {
    const state = createCardDisclosureState(() => "session-a"); state.set("m1:thinking", true);
    const text = '  original\ttext\n<script>alert(1)</script>\nFINAL_PART';
    const html = await render({ text, streaming: false, durationMs: 0 }, state);
    expect(html).toContain("完整思考原文"); expect(html).toContain('aria-expanded="true"');
    expect(html).toContain("  original\ttext\n&lt;script&gt;alert(1)&lt;/script&gt;\nFINAL_PART"); expect(html).toContain("0ms");
    expect(html).not.toContain("<script>"); expect(html).not.toContain('class="stw"');
  });
  it("keeps compact mode a recent preview without overwriting a chosen full view", async () => {
    const state = createCardDisclosureState(() => "session-a"); state.set("m1:thinking", true);
    const compact = await render({ compact: true }, state); expect(compact).toContain("最近片段"); expect(compact).not.toContain("aria-expanded");
    expect(state.choice("m1:thinking")).toBe(true);
    expect(await render({ compact: false }, state)).toContain('aria-expanded="true"');
  });
  it("does not publish temporary live ids, and preserves explicit choices after completion", async () => {
    const { props, c, state } = mount({ id: "live" }); c.toggle(); expect(c.fullOpen.value).toBe(true); expect(state.choice("live:thinking")).toBeUndefined();
    props.id = "m1"; await nextTick(); expect(c.fullOpen.value).toBe(false);
    c.toggle(); props.streaming = false; await nextTick(); expect(c.fullOpen.value).toBe(true); expect(state.choice("m1:thinking")).toBe(true);
    c.toggle(); props.text += " later"; await nextTick(); expect(c.fullOpen.value).toBe(false);
  });
  it("retains choices when remounting but isolates the session being displayed", async () => {
    const sid = ref("session-a"), state = createCardDisclosureState(() => sid.value); state.set("m1:thinking", true);
    expect(await render({}, state)).toContain('aria-expanded="true"'); sid.value = "session-b";
    expect(await render({}, state)).toContain('aria-expanded="false"'); sid.value = "session-a";
    expect(await render({}, state)).toContain('aria-expanded="true"');
  });
  it("keeps legacy StreamTailWindow props compatible without requiring playback timers", async () => {
    const html = await renderToString(createSSRApp({ render: () => h(StreamTailWindow, { text: "newest fragment", streaming: false, lines: 2, maxUnits: 80, paceMs: 420 }) }));
    expect(html).toContain("newest fragment"); expect(html).toContain("--stw-lines:2");
  });
  it("keeps the last unfinished fragment visible when the same live tail stops", async () => {
    const props = reactive({ text: "short first", streaming: true, lines: 3, maxUnits: 72, paceMs: 420 });
    const scope = effectScope(); scopes.push(scope);
    const controller = scope.run(() => (StreamTailWindow as unknown as {
      setup(props: unknown, context: unknown): { visible: ComputedRef<Array<{ text: string }>> };
    }).setup(props, { emit: vi.fn(), expose() {} }))!;
    expect(controller.visible.value.map(line => line.text)).toEqual(["short first"]);
    props.text += "\nfinal unfinished fragment"; props.streaming = false; await nextTick();
    const visible = controller.visible.value;
    expect(visible[visible.length - 1]?.text).toBe("final unfinished fragment");
  });
});

describe("full thinking scroll intent", () => {
  it("follows a large append when the reader was at the bottom before the DOM grows", async () => {
    const { props, c } = mount(); c.toggle(); await nextTick();
    const el = { scrollHeight: 1000, clientHeight: 200, scrollTop: 800 }; c.body.value = el as HTMLElement; c.onScroll();
    props.text += " a large append"; await nextTick(); el.scrollHeight = 2400; await nextTick();
    expect(el.scrollTop).toBe(2400);
  });
  it("does not pull a reader back down after they scroll up between patch and follow", async () => {
    const { props, c } = mount(); c.toggle(); await nextTick();
    const el = { scrollHeight: 1000, clientHeight: 200, scrollTop: 800 }; c.body.value = el as HTMLElement; c.onScroll();
    props.text += " append"; await nextTick(); el.scrollHeight = 2400; el.scrollTop = 450; c.onScroll(); await nextTick();
    expect(el.scrollTop).toBe(450);
  });
  it("preserves a manually selected position and does not collapse when streaming stops", async () => {
    const { props, c } = mount(); c.toggle(); await nextTick();
    const el = { scrollHeight: 1000, clientHeight: 200, scrollTop: 300 }; c.body.value = el as HTMLElement; c.onScroll();
    props.text += " append"; props.streaming = false; await nextTick(); await nextTick();
    expect(el.scrollTop).toBe(300); expect(c.fullOpen.value).toBe(true);
  });
  it("honors upward wheel and keyboard intentions before an actual scroll event arrives", async () => {
    for (const interrupt of [(c: Controller) => c.onWheel({ deltaY: -10 }), (c: Controller) => c.onKeydown({ key: "PageUp" })]) {
      const { props, c } = mount(); c.toggle(); await nextTick();
      const el = { scrollHeight: 1000, clientHeight: 200, scrollTop: 800 }; c.body.value = el as HTMLElement; c.onScroll();
      props.text += " append"; await nextTick(); el.scrollHeight = 2400; interrupt(c); await nextTick();
      expect(el.scrollTop).toBe(800);
    }
  });
});
