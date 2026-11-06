import { afterEach, describe, expect, it } from "vitest";
import { effectScope, nextTick, ref } from "vue";
import { setLocale } from "@/i18n";
import { createActivityAttention, formatActivityAttention, useActivityAttention, type ActivityAttentionInput } from "./activityAttention";

const pending = (extra: Partial<ActivityAttentionInput> = {}): ActivityAttentionInput => ({ sessionId: "s1", enabled: true, approvalIds: [], answerIds: [], ...extra });
setLocale("zh-CN");
afterEach(() => setLocale("zh-CN"));

describe("activity attention title", () => {
  it("counts unique pending requests and distinguishes approvals, answers and mixed attention", () => {
    expect(formatActivityAttention("pogo", pending({ approvalIds: ["g1", "g1", "", "  "] }))).toBe("(1) 等待审批 · pogo");
    expect(formatActivityAttention("pogo", pending({ answerIds: ["q1", "q2", "q1"] }))).toBe("(2) 等待回答 · pogo");
    expect(formatActivityAttention("pogo", pending({ approvalIds: ["g1", "g1"], answerIds: ["q1"] }))).toBe("(2) 需要你处理 · pogo");
  });

  it("leaves titles untouched when disabled, unbound, or no longer waiting", () => {
    for (const input of [pending(), pending({ enabled: false, approvalIds: ["g1"] }), pending({ sessionId: undefined, answerIds: ["q1"] })]) {
      expect(formatActivityAttention("(1) 用户命名的标题", input)).toBe("(1) 用户命名的标题");
    }
  });

  it("recomputes on session changes and removes its own prefix when pending requests resolve", () => {
    const target = { title: "pogo" }, attention = createActivityAttention(target);
    attention.update(pending({ approvalIds: ["g1", "g2"] }));
    attention.refresh(); attention.refresh();
    expect(target.title).toBe("(2) 等待审批 · pogo");
    attention.update(pending({ sessionId: "s2", answerIds: ["q1"] }));
    expect(target.title).toBe("(1) 等待回答 · pogo");
    attention.update(pending({ sessionId: "s2" }));
    expect(target.title).toBe("pogo");
    attention.dispose();
  });

  it("preserves external title replacements and suffix edits rather than restoring an obsolete title", () => {
    const target = { title: "First · pogo" }, attention = createActivityAttention(target);
    attention.update(pending({ approvalIds: ["g1"] }));
    target.title = "Second · pogo";
    attention.refresh();
    expect(target.title).toBe("(1) 等待审批 · Second · pogo");
    target.title += " *";
    attention.refresh();
    expect(target.title).toBe("(1) 等待审批 · Second · pogo *");
    attention.dispose();
    expect(target.title).toBe("Second · pogo *");
    attention.refresh();
    expect(target.title).toBe("Second · pogo *");
  });

  it("does not overwrite a route title set immediately before disposal", () => {
    const target = { title: "pogo" }, attention = createActivityAttention(target);
    attention.update(pending({ answerIds: ["q1"] }));
    target.title = "Shared report · pogo";
    attention.dispose();
    expect(target.title).toBe("Shared report · pogo");
  });

  it("tracks reactive pending facts and locale, then cleans up on scope disposal", async () => {
    const target = { title: "pogo" };
    const facts = ref(pending({ approvalIds: ["g1"] }));
    const scope = effectScope();
    scope.run(() => useActivityAttention(() => facts.value, target));
    expect(target.title).toBe("(1) 等待审批 · pogo");
    setLocale("en"); await nextTick();
    expect(target.title).toBe("(1) Needs approval · pogo");
    facts.value.answerIds = [...facts.value.answerIds, "q1"]; await nextTick();
    expect(target.title).toBe("(2) Needs you · pogo");
    facts.value = pending({ sessionId: "s2" }); await nextTick();
    expect(target.title).toBe("pogo");
    facts.value.answerIds = [...facts.value.answerIds, "q2"]; await nextTick();
    expect(target.title).toBe("(1) Needs an answer · pogo");
    scope.stop();
    expect(target.title).toBe("pogo");
  });
});
