import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createI18n } from "vue-i18n";
import ShareView from "./ShareView.vue";

afterEach(() => vi.unstubAllGlobals());

describe("public share entry", () => {
  it("keeps the document frame visible while the snapshot is unresolved", async () => {
    vi.stubGlobal("location", { pathname: "/share/example" });
    const i18n = createI18n({
      legacy: false,
      locale: "zh-CN",
      fallbackLocale: false,
      messages: { "zh-CN": { shareView: { loading: "加载中…" } } },
    });
    const app = createSSRApp(ShareView);
    app.use(i18n);
    const html = await renderToString(app);

    expect(html).toContain("studio-document-skeleton");
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('aria-label="加载中…"');
    expect(html).not.toContain('class="state"');
  });
});
