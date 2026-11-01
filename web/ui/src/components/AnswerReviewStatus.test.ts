import { describe, expect, it, vi } from "vitest";
import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import type { AnswerReview } from "@/api/types";
import { i18n, setLocale } from "@/i18n";
import AnswerReviewStatus from "./AnswerReviewStatus.vue";

const stage: AnswerReview = { kind: "citation", state: "running", turn: 2, started_at: 1000 };
const render = (review: AnswerReview) => renderToString(createSSRApp({ render: () => h(AnswerReviewStatus, { review }) }).use(i18n));

describe("compact answer review status", () => {
  it("renders an animated activity icon, elapsed time and accessible status instead of internal instructions", async () => {
    setLocale("zh-CN"); const clock = vi.spyOn(Date, "now").mockReturnValue(9000);
    try {
      const html = await render(stage);
      expect(html).toContain("正在整理引用"); expect(html).toContain("8 秒");
      expect(html).toContain('class="review-spinner"'); expect(html).toContain('aria-busy="true"');
      expect(html).toContain('role="status"'); expect(html).not.toContain("[pogo]");
    } finally { clock.mockRestore(); }
  });
  it.each(["completed", "fallback", "cancelled"] as const)("stops activity and labels the %s outcome", async state => {
    const html = await render({ ...stage, state, duration_ms: 30000, reason: state === "fallback" ? "timeout" : undefined });
    expect(html).toContain(`data-state="${state}"`); expect(html).not.toContain('class="review-spinner"');
    expect(html).toContain('aria-busy="false"'); expect(html).toContain("30 秒");
    expect(html).toContain(state === "completed" ? "引用检查完成" : state === "fallback" ? "引用整理超时，已保留原回答" : "引用整理已停止，已保留原回答");
  });
});
