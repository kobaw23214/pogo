import { describe, expect, it } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createI18n } from "vue-i18n";
import StudioEntryPage from "./StudioEntryPage.vue";
import StudioEntrySkeleton from "./StudioEntrySkeleton.vue";

async function renderPage(kind: "chat" | "management" | "share") {
  const i18n = createI18n({
    legacy: false,
    locale: "zh-CN",
    fallbackLocale: false,
    messages: { "zh-CN": { common: { loading: "加载中…" }, uiCoverage: { app: { openingAgent: "正在打开 Agent…" } } } },
  });
  const app = createSSRApp({ render: () => h(StudioEntryPage, { kind }) });
  app.use(i18n);
  return renderToString(app);
}

describe("Studio entry skeletons", () => {
  it.each([
    ["agent", "entry-agent"],
    ["sessions", "studio-session-skeleton"],
    ["conversation", "studio-conversation-skeleton"],
    ["cards", "studio-card-skeleton"],
    ["settings", "studio-settings-skeleton"],
    ["document", "studio-document-skeleton"],
  ] as const)("renders the %s region as decorative structure", async (area, marker) => {
    const html = await renderToString(createSSRApp(StudioEntrySkeleton, { area }));
    expect(html).toContain(marker);
    expect(html).toContain('aria-hidden="true"');
    expect(html).not.toContain("role=\"status\"");
  });

  it("fills the asynchronous chat frame without a spinner or a fake empty conversation", async () => {
    const html = await renderPage("chat");
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('role="status"');
    expect(html).toContain("正在打开 Agent…");
    expect(html).toContain("studio-session-skeleton");
    expect(html).toContain("studio-conversation-skeleton");
    expect(html).not.toContain("studio-orbit");
    expect(html).not.toContain("StarterCards");
  });

  it("uses the management navigation and card layout for Studio routes", async () => {
    const html = await renderPage("management");
    expect(html).toContain("management-sidebar");
    expect(html).toContain("management-layout");
    expect(html).toContain("studio-card-skeleton");
    expect(html).not.toContain("studio-conversation-skeleton");
    expect(html).toContain("加载中…");
  });

  it("uses the public document layout for shared answers", async () => {
    const html = await renderPage("share");
    expect(html).toContain("share-sheet");
    expect(html).toContain("studio-document-skeleton");
    expect(html).not.toContain("management-sidebar");
    expect(html).toContain("加载中…");
  });
});
