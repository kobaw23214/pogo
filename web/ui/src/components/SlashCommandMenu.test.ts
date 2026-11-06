import { describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import SlashCommandMenu from "./SlashCommandMenu.vue";
import { composerSuggestions } from "@/agent/composerSuggestions";

vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));

describe("MCP slash menu", () => {
  it("gives methods their own group and retains a qualified, escaped name", async () => {
    const items = composerSuggestions("/mcp:", [], [], { label: "skill", description: "" }, [
      { server: "docs", name: "search<draft>", description: "Search drafts" },
    ]);
    const html = await renderToString(createSSRApp({ render: () => h(SlashCommandMenu, { id: "methods", items, selectedId: items[0].id }) }));
    expect(html).toContain('id="methods-mcp-heading"');
    expect(html).toContain("agentView.commands.mcpGroup");
    expect(html).not.toContain("agentView.commands.skillsGroup");
    expect(html).toContain("docs/search&lt;draft&gt;");
    expect(html).toContain('aria-selected="true"');
    expect(html).toContain('role="option"');
    expect(html).toContain("Search drafts");
  });

  it("keeps rendered groups in keyboard traversal order", async () => {
    const items = composerSuggestions("/", [{ name: "/compact", label: "Compact", description: "" }], [
      { name: "office", description: "Office", source: "embedded" },
    ], { label: "Skill", description: "" }, [], { label: "MCP", description: "" });
    const html = await renderToString(createSSRApp({ render: () => h(SlashCommandMenu, { id: "all", items, selectedId: null }) }));
    const rendered = Array.from(html.matchAll(/id="all-([^" ]+)"[^>]*role="option"/g), match => match[1]);
    expect(rendered).toEqual(items.map(item => item.id));
  });
});
