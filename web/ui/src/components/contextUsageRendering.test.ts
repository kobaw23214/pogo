import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import CommandTokenIcon from "./CommandTokenIcon.vue";
import ContextMeter from "./ContextMeter.vue";
import SlashCommandMenu from "./SlashCommandMenu.vue";

vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));

async function render(component: unknown, props: Record<string, unknown>) {
  const app = createSSRApp({ render: () => h(component as Parameters<typeof h>[0], props) });
  app.component("el-popover", defineComponent({ setup(_, { slots }) { return () => h("div", [slots.reference?.(), slots.default?.()]); } }));
  app.component("el-icon", defineComponent({ setup(_, { slots }) { return () => h("i", slots.default?.()); } }));
  return renderToString(app);
}

describe("actual context displays", () => {
  it("draws the supplied percentage instead of the previous fixed three-quarter arc", async () => {
    const html = await render(CommandTokenIcon, { kind: "command", contextPercent: 12.5 });
    expect(html).toContain('stroke-dasharray="12.5 100"');
    expect(html).not.toContain("M12 3.5a8.5");
  });
  it.each([null, undefined])("an unknown reading %s renders the neutral ring without a progress segment", async value => {
    const html = await render(CommandTokenIcon, { kind: "command", contextPercent: value });
    expect(html).toContain('opacity=".3"');
    expect(html).not.toContain("stroke-dasharray");
  });
  it("the open slash menu forwards the live reading to its icon", async () => {
    const html = await render(SlashCommandMenu, { id: "commands", selectedId: "compact", contextPercent: 36,
      items: [{ id: "compact", kind: "command", label: "压缩", description: "36,000/100,000 tokens", value: "/compact " }] });
    expect(html).toContain('stroke-dasharray="36 100"');
    expect(html).toContain("36,000/100,000 tokens");
  });
  it("the context meter preserves a 500-token measured input despite a 5,000-token overhead estimate", async () => {
    const html = await render(ContextMeter, { used: 500, window: 100000, overhead: 5000, messages: [], results: {} });
    expect(html).toMatch(/class="big"[^>]*>500<\/span>/);
    expect(html).toContain("contextMeter.sources.measured");
    expect(html).not.toMatch(/class="big"[^>]*>5.0K/);
  });
  it("a reopened session with a zero API count visibly labels its estimate", async () => {
    const html = await render(ContextMeter, { used: 0, window: 100000, overhead: 10000, messages: [], results: {} });
    expect(html).toMatch(/class="big"[^>]*>~10.0K<\/span>/);
    expect(html).toContain("contextMeter.sources.estimated");
  });
  it("empty unknown data does not claim all capacity is free", async () => {
    const html = await render(ContextMeter, { used: 0, window: 100000, overhead: 0, messages: [], results: {} });
    expect(html).toMatch(/class="big"[^>]*>—<\/span>/);
    expect(html).toContain("contextMeter.sources.unknown");
    expect(html).not.toMatch(/100%<\/em>/);
  });
});
