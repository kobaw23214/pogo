import { afterEach, describe, expect, it } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "@vue/server-renderer";
import { i18n, setLocale } from "@/i18n";
import CompactionCard from "./CompactionCard.vue";

afterEach(() => setLocale("zh-CN"));

describe("compaction result card", () => {
  it("shows usage metrics and a concise overview with the full summary collapsed", async () => {
    setLocale("zh-CN");
    const app = createSSRApp({
      render: () => h(CompactionCard, {
        state: "done",
        startedAt: 0,
        finishedAt: 38_000,
        result: "28 条消息已整理为上下文摘要",
        beforeTokens: 8710,
        afterTokens: 1930,
        summary: "## 任务目标\n\n- 保留销售折线图的同比数据和交互。\n- 第二段只用于完整摘要。",
      }),
    }).use(i18n);
    app.component("ElIcon", defineComponent({ setup: (_, { slots }) => () => h("i", slots.default?.()) }));
    const html = await renderToString(app);
    expect(html).toContain("-78%");
    expect(html).toContain("8.7K");
    expect(html).toContain("1.9K");
    expect(html).toContain("剩余 22%");
    expect(html).toContain("保留销售折线图的同比数据和交互。");
    expect(html).toContain("查看压缩摘要");
    expect(html).toMatch(/compaction-summary[^>]*display:\s*none/);
    expect(html).not.toContain("tok tokens");
  });
});
