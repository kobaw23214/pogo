import { describe, expect, it } from "vitest";
import { createSSRApp, h, type Component } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createI18n } from "vue-i18n";
import type { OverheadPart } from "@/api/types";
import contextMeter from "@/i18n/messages/contextMeter";
import ContextMeter from "./ContextMeter.vue";

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
    const overheadRow = (html: string) => html.match(/<div class="row[^"]*"[^>]*><span class="dot"[^>]*><\/span><span class="name"[^>]*>系统开销<\/span>/)?.[0] ?? "";
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
});
