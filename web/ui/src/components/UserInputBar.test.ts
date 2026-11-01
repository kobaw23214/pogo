import { createSSRApp, h } from "vue";
import { renderToString } from "vue/server-renderer";
import { describe, expect, it } from "vitest";
import type { PendingUserInput } from "@/api/types";
import { i18n, setLocale } from "@/i18n";
import { collectUserInputAnswers } from "@/agent/userInputDraft";
import UserInputBar from "./UserInputBar.vue";
import UserInputResult from "./tools/UserInputResult.vue";

setLocale("zh-CN");
const questions: PendingUserInput["questions"] = [
  { id: "approach", header: "实现方案", question: "选择哪种 <em>实现</em>？", multi_select: false, options: [
    { label: "局部扩展", description: "保留现有结构，逐步替换" },
    { label: "整体重写", description: "迁移成本更高，但减少历史包袱" },
  ] },
  { id: "checks", header: "验收", question: "需要哪些检查？", multi_select: true, options: [
    { label: "单元测试", description: "覆盖状态与映射" },
    { label: "真实交互", description: "验证刷新后恢复" },
  ] },
];
const request: PendingUserInput = { request_id: "q-one", call_id: "call-one", revision: 1, deadline: 200000, questions };
function render(props: { requests?: PendingUserInput[]; pending?: Record<string, boolean>; errors?: Record<string, string> } = {}) {
  return renderToString(createSSRApp({ render: () => h(UserInputBar, { requests: props.requests ?? [request], pending: props.pending ?? {}, errors: props.errors ?? {} }) }).use(i18n));
}

describe("structured user input", () => {
  it("renders safe native controls with descriptions and a custom answer", async () => {
    const html = await render();
    expect(html).toContain('data-testid="user-input-bar"');
    expect(html).toContain('role="radiogroup"');
    expect(html).toContain('type="radio"');
    expect(html).toContain("保留现有结构，逐步替换");
    expect(html).toContain("选择哪种 &lt;em&gt;实现&lt;/em&gt;？");
    expect(html).not.toContain("<em>实现</em>");
    expect(html).toContain("自定义回答");
    expect(html).toContain("1 / 2");
    expect(html).toContain("question-scroll");
    expect(html).toContain("不代表批准执行");
  });

  it("renders multi-select and keeps failed submissions retryable", async () => {
    const html = await render({ requests: [{ ...request, questions: [questions[1]] }], errors: { "q-one": "网络失败，请重试" } });
    expect(html).toContain('type="checkbox"');
    expect(html).toContain("可多选");
    expect(html).toContain('role="alert"');
    expect(html).toContain("网络失败，请重试");
    expect(html).toContain("提交回答</button>");
  });

  it("disables the whole current form while submitting", async () => {
    const html = await render({ pending: { "q-one": true } });
    expect(html).toContain("<fieldset");
    expect(html).toContain("disabled");
    expect(html).toContain("正在提交…");
  });

  it("maps option and custom answers by stable question id, including multi-select", () => {
    const result = collectUserInputAnswers(questions, {
      approach: { selected: ["局部扩展"], other: false, custom: "" },
      checks: { selected: ["单元测试", "真实交互"], other: true, custom: "  刷新后继续  " },
    });
    expect(result.missing).toBe(-1);
    expect(result.answers).toEqual({ approach: ["局部扩展"], checks: ["单元测试", "真实交互", "刷新后继续"] });
    expect(collectUserInputAnswers(questions, { checks: { selected: [], other: true, custom: " " }, approach: { selected: [], other: false, custom: "" } }).missing).toBe(0);
  });

  it("renders the recorded answer rather than raw tool JSON", async () => {
    const html = await renderToString(createSSRApp({ render: () => h(UserInputResult, { args: { questions: [questions[0]] }, result: {
      call_id: "call-one", text: "tool output", details: { questions: [questions[0]], answers: { approach: ["局部扩展"] } },
    } }) }).use(i18n));
    expect(html).toContain("你的回答");
    expect(html).toContain("局部扩展");
    expect(html).toContain("选择哪种 &lt;em&gt;实现&lt;/em&gt;？");
    expect(html).not.toContain("tool output");
    expect(html).not.toContain("&quot;answers&quot;");
  });
});
