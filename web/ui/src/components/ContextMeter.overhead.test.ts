import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, effectScope, h, nextTick, reactive, type Component, type ComputedRef } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createI18n } from "vue-i18n";
import type { OverheadPart, ToolResult } from "@/api/types";
import contextMeter from "@/i18n/messages/contextMeter";
import ContextMeter from "./ContextMeter.vue";

vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async original => ({
  ...(await original() as Record<string, unknown>),
  useI18n: () => {
    const composer = meterI18n("zh-CN").global;
    return {
      t: (key: string, params?: Record<string, string | number>) => params ? composer.t(key, params) : composer.t(key),
      te: (key: string) => composer.te(key),
    };
  },
}));

// The meter labels the server's itemized overhead from stable keys. Every key
// the server sends must have copy in both locales, and the overhead row only
// becomes expandable when there is something to expand.

// Every prompt section key agent.PromptPart* can produce.
const SECTION_KEYS = ["base", "project", "ui", "skills", "memory", "citations", "agent", "runtime", "other", "background", "mcp_deferred", "skill_tools"];

function leaves(value: unknown, prefix = ""): Record<string, string> {
  if (!value || typeof value !== "object") return {};
  return Object.fromEntries(Object.entries(value).flatMap(([key, child]) =>
    typeof child === "string" ? [[prefix + key, child]] : Object.entries(leaves(child, `${prefix}${key}.`))));
}
const parameters = (message: string) => [...message.matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)].map((match) => match[1]!).sort();

const meterI18n = (locale: "zh-CN" | "en") => createI18n({
  legacy: false, locale, fallbackLocale: false,
  messages: { "zh-CN": { contextMeter: contextMeter.messages["zh-CN"] }, en: { contextMeter: contextMeter.messages.en } },
});

// Element Plus is not installed here: the popover renders both of its slots,
// which is all these assertions need.
const slotsOnly: Component = { setup: (_props, { slots }) => () => h("div", [slots.reference?.(), slots.default?.()]) };
const iconStub: Component = { setup: (_props, { slots }) => () => h("i", slots.default?.()) };

async function renderMeter(overheadBreakdown?: OverheadPart[]): Promise<string> {
  const app = createSSRApp(ContextMeter, { used: 9000, window: 100000, overhead: 7000, overheadBreakdown, messages: [], results: {} });
  app.use(meterI18n("zh-CN"));
  app.component("ElPopover", slotsOnly);
  app.component("ElIcon", iconStub);
  return renderToString(app);
}

interface TestItem { id: string; name: string; tokens: number; detail?: string }
interface TestCat { key: "overhead" | "user" | "assistant" | "tools"; items?: TestItem[]; limit?: number }
interface Controller {
  cats: ComputedRef<TestCat[]>;
  showAll: Set<string>;
  visibleItems(cat: TestCat): TestItem[];
  toggleMore(key: TestCat["key"]): void;
  toggleDetail(cat: TestCat, item: TestItem): void;
  selectedDetail: ComputedRef<{ kind: string; body: string; content?: string } | null>;
  detailLimit: { value: number };
  visibleDetailContent: ComputedRef<string>;
  detailHasMore: ComputedRef<boolean>;
  appendDetailPage(): void;
  onDetailScroll(event: { currentTarget: { scrollHeight: number; scrollTop: number; clientHeight: number } }): void;
}
const scopes: ReturnType<typeof effectScope>[] = [];
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); });
function mountMeter(overheadBreakdown: OverheadPart[], results: Record<string, ToolResult> = {}): Controller {
  const app = createSSRApp({}); app.use(meterI18n("zh-CN"));
  const props = reactive({ used: 9000, window: 100000, overhead: 7000, overheadBreakdown, messages: [], results });
  const scope = effectScope(); scopes.push(scope);
  return app.runWithContext(() => scope.run(() => (ContextMeter as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit: vi.fn(), expose() {} })))!;
}

describe("context meter overhead breakdown", () => {
  it("ships matching Chinese and English part labels for every server key", () => {
    const chinese = leaves(contextMeter.messages["zh-CN"].overheadParts), english = leaves(contextMeter.messages.en.overheadParts);
    expect(Object.keys(chinese).sort()).toEqual(Object.keys(english).sort());
    for (const key of Object.keys(english)) {
      expect(chinese[key]?.trim(), `overheadParts.${key}`).toBeTruthy();
      expect(parameters(chinese[key]!), `overheadParts.${key} parameters`).toEqual(parameters(english[key]!));
    }
    for (const locale of ["zh-CN", "en"] as const) {
      const i18n = meterI18n(locale);
      const t = i18n.global.t as (key: string, params?: Record<string, string | number>) => string;
      for (const key of SECTION_KEYS) {
        expect(i18n.global.te(`contextMeter.overheadParts.sections.${key}`), `${locale} section ${key}`).toBe(true);
      }
      expect(t("contextMeter.overheadParts.skillTools", { name: "agent-workflows", n: 4 })).toMatch(/agent-workflows.*4/);
      expect(t("contextMeter.overheadParts.mcp", { name: "github", n: 12 })).toMatch(/github.*12/);
    }
  });

  it("makes the overhead row expandable only when the server itemized it", async () => {
    const overheadRow = (html: string) => {
      const label = html.indexOf(">系统开销</span>"), start = html.lastIndexOf("<button", label);
      return label >= 0 && start >= 0 ? html.slice(start, html.indexOf(">", start) + 1) : "";
    };
    const without = overheadRow(await renderMeter());
    expect(without).not.toBe("");
    expect(without).not.toContain("clickable");
    const parts: OverheadPart[] = [
      { kind: "prompt", name: "base", tokens: 500 },
      { kind: "skill_tools", name: "agent-workflows", tokens: 6500, count: 4 },
      { kind: "tool", name: "zero", tokens: 0 },
    ];
    expect(overheadRow(await renderMeter(parts))).toContain("clickable");
    // Zero-token parts are not items, so they alone do not make it expandable.
    expect(overheadRow(await renderMeter([{ kind: "tool", name: "zero", tokens: 0 }]))).not.toContain("clickable");
  });

  it("keeps every overflow item available when more is expanded", () => {
    const parts = Array.from({ length: 11 }, (_, i): OverheadPart => ({ kind: "tool", name: `tool_${i}`, tokens: (i + 1) * 100 }));
    const c = mountMeter(parts), cat = c.cats.value[0]!;
    expect(cat.items).toHaveLength(11);
    expect(c.visibleItems(cat)).toHaveLength(8);
    c.toggleMore("overhead");
    expect(c.showAll.has("overhead")).toBe(true);
    expect(c.visibleItems(cat)).toHaveLength(11);
    c.toggleMore("overhead");
    expect(c.visibleItems(cat)).toHaveLength(8);
  });

  it("explains a tool definition inline and toggles its detail closed", () => {
    const c = mountMeter([{ kind: "tool", name: "docs_office", tokens: 2200 }]);
    const cat = c.cats.value[0]!, item = cat.items![0]!;
    c.toggleDetail(cat, item);
    expect(c.selectedDetail.value?.kind).toBe("工具定义");
    expect(c.selectedDetail.value?.body).toContain("不是工具运行结果");
    expect(c.selectedDetail.value?.content).toBeUndefined();
    c.toggleDetail(cat, item);
    expect(c.selectedDetail.value).toBeNull();
  });

  it("shows the actual returned text for a tool result", () => {
    const c = mountMeter([], { call1: { call_id: "call1", name: "read", text: "document contents" } });
    const cat = c.cats.value.find(item => item.key === "tools")!, item = cat.items![0]!;
    c.toggleDetail(cat, item);
    expect(c.selectedDetail.value?.kind).toBe("工具结果");
    expect(c.selectedDetail.value?.content).toBe("document contents");
  });

  it("lazily renders the complete long result without an ellipsis truncation", async () => {
    const longText = "x".repeat(10_000);
    const c = mountMeter([], {
      long: { call_id: "long", name: "read", text: longText },
      short: { call_id: "short", name: "read", text: "short" },
    });
    const cat = c.cats.value.find(item => item.key === "tools")!;
    const longItem = cat.items!.find(item => item.id === "long")!;
    c.toggleDetail(cat, longItem);
    expect(c.selectedDetail.value?.content).toHaveLength(10_000);
    expect(c.visibleDetailContent.value).toHaveLength(4000);
    expect(c.detailHasMore.value).toBe(true);
    expect(c.visibleDetailContent.value.endsWith("…")).toBe(false);

    c.onDetailScroll({ currentTarget: { scrollHeight: 5000, scrollTop: 4900, clientHeight: 100 } });
    expect(c.detailLimit.value).toBe(8000);
    expect(c.visibleDetailContent.value).toHaveLength(8000);
    c.appendDetailPage();
    expect(c.visibleDetailContent.value).toHaveLength(10_000);
    expect(c.detailHasMore.value).toBe(false);

    const shortItem = cat.items!.find(item => item.id === "short")!;
    c.toggleDetail(cat, shortItem);
    await nextTick();
    expect(c.detailLimit.value).toBe(4000);
    expect(c.visibleDetailContent.value).toBe("short");
  });
});
