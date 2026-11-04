import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, effectScope, h, reactive, type ComputedRef } from "vue";
import { renderToString } from "@vue/server-renderer";
import type { CompactionView, ContextMessage, Message, ToolResult } from "@/api/types";
import ContextMeter from "./ContextMeter.vue";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }));

const visibleMessages: Message[] = [
  { id: "old-user", role: "user", content: [{ type: "text", text: "old question" }] },
  { id: "old-assistant", role: "assistant", content: [{ type: "tool_use", id: "old-call", name: "read", input: { path: "old.txt" } }] },
];
const visibleResults: Record<string, ToolResult> = { "old-call": { call_id: "old-call", name: "read", text: "removed tool result" } };
const contextMessages: ContextMessage[] = [{ role: "user", content: [{ type: "text", text: "compressed summary" }] }];
const latestCompaction: CompactionView = { id: "c1", after: 2, ts: 100, summary: "compressed summary", before: 115, before_tokens: 10000, after_tokens: 500 };
const props = () => ({ used: 10500, estimated: true, window: 100000, overhead: 10000, messages: visibleMessages, results: visibleResults, contextMessages, latestCompaction });

interface Category { key: string; tokens: number; items?: { id: string; name: string; detail?: string }[] }
interface Controller {
  cats: ComputedRef<Category[]>;
  compactionStatus: ComputedRef<{ current: boolean; text: string } | null>;
  alreadyCompacted: ComputedRef<boolean>;
}
const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); });
function mount(overrides: Record<string, unknown> = {}): Controller {
  const scope = effectScope(); scopes.push(scope);
  return scope.run(() => (ContextMeter as unknown as { setup(props: unknown, context: unknown): Controller }).setup(reactive({ ...props(), ...overrides }), { emit: vi.fn(), expose() {} }))!;
}
async function render(overrides: Record<string, unknown> = {}) {
  const app = createSSRApp(ContextMeter, { ...props(), ...overrides });
  app.component("el-popover", defineComponent({ setup(_, { slots }) { return () => h("div", [slots.reference?.(), slots.default?.()]); } }));
  app.component("el-icon", defineComponent({ setup(_, { slots }) { return () => h("i", slots.default?.()); } }));
  return renderToString(app);
}

describe("compacted context breakdown", () => {
  it("uses the model-facing summary instead of counting removed visible tool results", () => {
    const controller = mount();
    const user = controller.cats.value.find(cat => cat.key === "user")!;
    const tools = controller.cats.value.find(cat => cat.key === "tools")!;
    expect(user.items).toHaveLength(1);
    expect(user.items?.[0]?.detail).toBe("compressed summary");
    expect(tools.tokens).toBe(0);
    expect(tools.items).toBeUndefined();
    expect(controller.compactionStatus.value?.current).toBe(true);
    expect(controller.alreadyCompacted.value).toBe(true);
  });
  it("shows the estimated compacted total and status without obsolete tool-output advice", async () => {
    const html = await render();
    expect(html).toMatch(/class="big"[^>]*>~10.5K<\/span>/);
    expect(html).toContain("contextMeter.compact.statusCurrent");
    expect(html).toContain("contextMeter.compact.currentAdvice");
    expect(html).toContain("contextMeter.compact.doneLabel");
    expect(html).not.toContain("contextMeter.advice.toolsPost");
  });
  it("marks an earlier compaction as historical once new conversation arrives", async () => {
    const controller = mount({
      messages: [...visibleMessages, { id: "new-user", role: "user", content: [{ type: "text", text: "new question" }] }],
      contextMessages: [...contextMessages, { role: "user", content: [{ type: "text", text: "new question" }] }],
    });
    expect(controller.compactionStatus.value?.current).toBe(false);
    expect(controller.alreadyCompacted.value).toBe(false);
    const html = await render({
      messages: [...visibleMessages, { id: "new-user", role: "user", content: [{ type: "text", text: "new question" }] }],
      contextMessages: [...contextMessages, { role: "user", content: [{ type: "text", text: "new question" }] }],
    });
    expect(html).toContain("contextMeter.compact.statusPrevious");
    expect(html).toContain("contextMeter.compact.label");
  });
});
