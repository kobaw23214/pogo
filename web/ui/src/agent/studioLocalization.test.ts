import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { parse as parseSFC } from "@vue/compiler-sfc";
import { parse as parseTemplate, type TemplateChildNode } from "@vue/compiler-dom";

const copy = vi.hoisted(() => ({ locale: "zh-CN" }));
vi.mock("@/i18n", () => ({ gt: (key: string) => `${copy.locale}:${key}` }));
import { CATEGORY_ORDER, STUDIO_NAV, STUDIO_SECTION_DESCS, STUDIO_SECTION_LABELS, categoryLabel, categoryChips, modelLabel, visibleStudioNav } from "./studioPresentation";
import { skillStateLabel } from "./skillSelection";
import { anatomyFocusContent, landingScenarios } from "@/components/studio/studioLandingContent";

afterEach(() => { copy.locale = "zh-CN"; });

describe("Studio text uses the active locale rather than an import-time snapshot", () => {
  it("refreshes navigation, section headings, categories and state labels", () => {
    const before = [STUDIO_NAV[0]!.label, STUDIO_SECTION_LABELS.agents, STUDIO_SECTION_DESCS.skills, categoryLabel(CATEGORY_ORDER[0]), skillStateLabel("available")];
    copy.locale = "en";
    const after = [visibleStudioNav(true)[0]!.label, STUDIO_SECTION_LABELS.agents, STUDIO_SECTION_DESCS.skills, categoryLabel(CATEGORY_ORDER[0]), skillStateLabel("available")];
    expect(before.every(text => text.startsWith("zh-CN:"))).toBe(true);
    expect(after.every(text => text.startsWith("en:"))).toBe(true);
    expect(categoryChips([])[0]!.label).toMatch(/^en:/);
    expect(modelLabel([], "")).toMatch(/^en:/);
  });

  it("keeps localized illustration copy fresh while preserving stable scenario identities", () => {
    const before = landingScenarios.research;
    copy.locale = "ja";
    const after = landingScenarios.research;
    expect(before.role).toMatch(/^zh-CN:/);
    expect(after.role).toMatch(/^ja:/);
    expect(after.question).toMatch(/^ja:/);
    expect(after.agent).toBe(before.agent);
    expect(after.steps).toHaveLength(before.steps.length);
  });

  it("refreshes the diagram's captions, descriptions and tags after changing language", () => {
    const before = anatomyFocusContent.model;
    copy.locale = "fr";
    const after = anatomyFocusContent.model;
    expect(before.caption).toMatch(/^zh-CN:/);
    expect(after.caption).toMatch(/^fr:/);
    expect(after.lines[0]).toMatch(/^fr:/);
    expect(after.tag).toMatch(/^fr:/);
    expect(after.lines).toHaveLength(2);
  });

  it("keeps ordinary English landing and login copy in message catalogs", () => {
    const brands = new Set(["pogo", "THE POGO ATLAS", "/ POGO ATLAS", "Agent", "Skills", "MCP", "POGO / 02", "SKILL", "pogo /", "POGO /"]);
    for (const file of ["StudioLanding.vue", "StudioLogin.vue"]) {
      const source = readFileSync(new URL(`../components/studio/${file}`, import.meta.url), "utf8");
      const template = parseTemplate(parseSFC(source).descriptor.template!.content);
      const walk = (node: TemplateChildNode) => {
        if (node.type === 2 && /[A-Za-z]{3}/.test(node.content)) expect(brands.has(node.content.trim()), `${file}: ${node.content.trim()}`).toBe(true);
        if (node.type === 1) node.children.forEach(walk);
      };
      template.children.forEach(walk);
    }
  });

  it("keeps user-defined categories untouched", () => {
    copy.locale = "de";
    expect(categoryLabel("我的自定义分类")).toBe("我的自定义分类");
  });
});
