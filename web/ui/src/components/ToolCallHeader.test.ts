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

  it("collapses a multiline command to one bounded line and restores the exact command when opened", async () => {
    const command = ["python3 -c '", ...Array.from({ length: 20 }, (_, i) => `  print(\"line ${i}\")`), "'"].join("\n");
    const presentation = presentToolCall({ ...call, name: "bash", args: { command } });
    const target = (html: string) => /<span\b[^>]*class="tool-target"[^>]*>([\s\S]*?)<\/span>/.exec(html)?.[1] ?? "";
    const closed = await renderHeader(presentation, { layered: true, expandable: true, open: false });
    expect(closed).toContain("command-collapsed"); expect(closed).toContain('aria-expanded="false"');
    expect(target(closed)).not.toContain("\n"); expect(target(closed)).toContain("…");
    expect(target(closed)).not.toContain("line 19");
    const opened = await renderHeader(presentation, { layered: true, expandable: true, open: true });
    expect(opened).not.toContain("command-collapsed"); expect(opened).toContain('aria-expanded="true"');
    expect(target(opened)).toContain("\n  print("); expect(target(opened)).toContain("line 19");
    expect(presentation.target).toBe(command);
    const full = await renderHeader(presentation, { layered: true, expandable: false, open: true });
    expect(target(full)).toBe(target(opened)); expect(full).not.toContain("aria-expanded");
  });

  it("does not compact file paths or other command-shaped tool targets", async () => {
    const presentation = { ...presentToolCall(call), glyph: "command" as const, protocolName: "shell_output", target: "first line\nsecond line" };
    const html = await renderHeader(presentation, { layered: true, expandable: true, open: false });
    expect(html).not.toContain("command-collapsed"); expect(html).toContain("first line\nsecond line");
  });

  it("keeps the disclosure and file action as independent controls rather than nested buttons", async () => {
    const html = await renderHeader(presentToolCall(call), { expandable: true, open: false, controls: "details-c1", fileTarget: { path: "文件.txt", name: "文件.txt" } });
    const buttons = [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)].map(match => match[0]);
    expect(buttons).toHaveLength(2);
    expect(buttons.every(button => (button.match(/<button\b/g) ?? []).length === 1)).toBe(true);
    expect(buttons[0]).toContain('aria-controls="details-c1"');
    expect(buttons[1]).toContain('data-open-file="文件.txt"');
    expect(buttons[1]).not.toContain("aria-expanded");
  });

  it("renders the same header in a tool call and retains citation anchors and its result", async () => {
    const settled = { ...call, running: false, result: { call_id: call.callId, text: "saved output", duration_ms: 20, source: { label: "S1" } } } as TimelineCall;
    const html = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: settled, runActive: false }) }).use(i18n));
    expect(html).toContain('data-cite="S1"'); expect(html).toContain('data-tool-kind="read"'); expect(html).toContain("20ms"); expect(html).toContain("saved output");
    const bodyOnly = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: settled, runActive: false, headerless: true }) }).use(i18n));
    expect(bodyOnly).not.toContain("tool-call-header"); expect(bodyOnly).toContain("saved output");
  });

  it("makes file tool paths open in the workspace without linking other arguments", async () => {
    const editCall: TimelineCall = { callId: "edit-1", name: "edit", args: { path: "artifacts/html-demo.html" }, running: false, orphaned: false };
    const html = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: editCall, runActive: false }) }).use(i18n));
    expect(html).toContain('data-open-file="artifacts/html-demo.html"');
    expect(html).toContain("在工作区打开 artifacts/html-demo.html");

    const bashCall: TimelineCall = { callId: "bash-1", name: "bash", args: { command: "pwd" }, running: false, orphaned: false };
    const bash = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: bashCall, runActive: false }) }).use(i18n));
    expect(bash).not.toContain("data-open-file");
  });

  it("marks Skill documents for the read-only preview instead of the workspace", async () => {
    const path = "/home/pogo/.cache/pogo/embedded-skills/browser-use/SKILL.md";
    const skillCall: TimelineCall = { callId: "skill-read", name: "read", args: { path }, running: false, orphaned: false };
    const skills = [{ name: "browser-use", path }];
    const html = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: skillCall, runActive: false, skills, cwd: "/home/pogo/users/u1" }) }).use(i18n));
    expect(html).toContain(`data-open-file="${path}"`);
    expect(html).toContain('data-skill="browser-use"');
    expect(html).toContain('data-skill-path="SKILL.md"');
    const reference = { ...skillCall, args: { path: "/home/pogo/.cache/pogo/embedded-skills/browser-use/references/download.md" } };
    const referenceHtml = await renderToString(createSSRApp({ render: () => h(ToolCall, { call: reference, runActive: false, skills, cwd: "/home/pogo/users/u1" }) }).use(i18n));
    expect(referenceHtml).toContain('data-skill-path="references/download.md"');
  });
});
