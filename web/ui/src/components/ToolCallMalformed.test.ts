import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import type { TimelineCall } from "@/agent/timeline";
import { i18n, setLocale } from "@/i18n";
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code"], setup: props => () => h("pre", props.code) }) }));
import ToolCall from "./ToolCall.vue";

setLocale("zh-CN");
const render = (call: TimelineCall) => renderToString(createSSRApp({ render: () => h(ToolCall, { call, runActive: true }) }).use(i18n));

describe("rejected tool argument card", () => {
  it("shows a safe expandable original text and never offers an execution approval", async () => {
    const raw = String.raw`{"action":"create","content":"echo \$HOME <script>alert(1)</script>"}`;
    const html = await render({ callId: "bad", name: "shortcut_manage", args: raw, inputInvalid: true, inputError: "invalid JSON string escape", running: false, orphaned: false });
    expect(html).toContain('data-testid="invalid-tool-arguments"');
    expect(html).toContain("参数格式错误"); expect(html).toContain("未执行");
    expect(html).toContain("查看原始参数"); expect(html).toContain("<details");
    expect(html).toContain("\\$HOME"); expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
    expect(html).not.toContain("<script>"); expect(html).not.toContain("等待批准");
  });

  it("leaves the following successful invocation on the normal result display", async () => {
    const html = await render({ callId: "good", name: "shortcut_manage", args: { action: "create", content: "echo $HOME" }, result: { call_id: "good", text: "Shortcut saved" }, running: false, orphaned: false });
    expect(html).toContain("Shortcut saved"); expect(html).not.toContain("invalid-tool-arguments"); expect(html).not.toContain("未执行");
  });
});
