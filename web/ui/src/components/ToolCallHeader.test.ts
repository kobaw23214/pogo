import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import { i18n, setLocale } from "@/i18n";
import { presentToolCall, type ToolPresentation } from "@/agent/toolPresentation";
import type { TimelineCall } from "@/agent/timeline";
import ToolCallHeader from "./ToolCallHeader.vue";
vi.mock("./CodeBlock.vue", () => ({ default: defineComponent({ props: ["code"], setup: props => () => h("pre", props.code) }) }));
import ToolCall from "./ToolCall.vue";

setLocale("zh-CN");
const call: TimelineCall = { callId: "read-1", name: "read", args: { path: "文件.txt" }, running: true, orphaned: false };
const renderHeader = (presentation: ToolPresentation, extra = {}) => renderToString(createSSRApp({ render: () => h(ToolCallHeader, { presentation, ...extra }) }).use(i18n));

describe("shared tool header", () => {
  it("keeps semantic identity alongside the running status and wraps the target as text", async () => {
    const html = await renderHeader(presentToolCall(call));
    expect(html).toContain('data-tool-kind="read"');
    expect(html).toContain('data-tool-state="running"');
    expect(html).toContain("读取文件"); expect(html).toContain("文件.txt"); expect(html).toContain("执行中");
    expect(html).toContain("<svg"); expect(html).not.toContain("aria-expanded");
  });

  it("provides an accessible disclosure only when its parent owns an expandable body", async () => {
    const html = await renderHeader(presentToolCall(call), { expandable: true, open: true, controls: "details-c1" });
    expect(html).toContain("<button"); expect(html).toContain('aria-expanded="true"'); expect(html).toContain('aria-controls="details-c1"');
  });

  it("renders the same header in a tool call and retains citation anchors and its result", async () => {
    const settled = { ...call, running: false, result: { call_id: call.callId, text: "saved output", duration_ms: 20, source: { label: "S1" } } } as TimelineCall;
    const html = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: settled, runActive: false }) }).use(i18n));
    expect(html).toContain('data-cite="S1"'); expect(html).toContain('data-tool-kind="read"'); expect(html).toContain("20ms"); expect(html).toContain("saved output");
    const bodyOnly = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: settled, runActive: false, headerless: true }) }).use(i18n));
    expect(bodyOnly).not.toContain("tool-call-header"); expect(bodyOnly).toContain("saved output");
  });
});
