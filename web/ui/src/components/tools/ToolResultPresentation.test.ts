import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h, ref } from "vue";
import { renderToString } from "vue/server-renderer";
import { i18n, setLocale } from "@/i18n";
import { cardDisclosureKey, createCardDisclosureState, type CardDisclosureState } from "@/agent/cardDisclosure";
import type { MCPDetails, SkillDetails, ToolResult } from "@/api/types";
import McpResult from "./McpResult.vue";
import SkillResult from "./SkillResult.vue";

vi.mock("../Chevron.vue", () => ({ default: defineComponent({ props: ["open"], setup: props => () => h("span", { "data-chevron-open": props.open }) }) }));

setLocale("zh-CN");
afterEach(() => { setLocale("zh-CN"); vi.unstubAllGlobals(); });
async function render(component: object, props: Record<string, unknown>, state?: CardDisclosureState) {
  const app = createSSRApp({ render: () => h(component, props) }).use(i18n);
  if (state) app.provide(cardDisclosureKey, state);
  return renderToString(app);
}
const result = (text: string, extra: Partial<ToolResult> = {}): ToolResult => ({ call_id: "c1", text, ...extra });
const mcp = (extra: Partial<MCPDetails> = {}): MCPDetails => ({ server: "fixture", tool: "query", ...extra });
const skill = (extra: Partial<SkillDetails> = {}): SkillDetails => ({ capability: "report.create", callId: "sk1", ...extra });

describe("compact MCP result content", () => {
  it("shows useful text immediately and offers one detail entry with arguments initially hidden", async () => {
    const html = await render(McpResult, { result: result("Found 3 records"), details: mcp(), args: { secret_fixture: "raw input" } });
    expect(html).toContain("Found 3 records"); expect(html).toContain('aria-expanded="false"');
    expect(html.match(/aria-expanded=/g)).toHaveLength(1); expect(html).not.toContain("secret_fixture");
    expect(html).not.toContain("（无入参）"); expect(html).not.toContain('class="raw-details"');
  });
  it("preserves structured data alongside distinct prose, images, audio and links", async () => {
    const html = await render(McpResult, { result: result("fallback"), details: mcp({ structured_content: { total: 3 }, content: [
      { type: "text", text: "Independent explanation" }, { type: "text", text: '{"total":3}' },
      { type: "image", mime_type: "image/png", data: "aGk=" }, { type: "audio", mime_type: "audio/wav", data: "AAAA" },
      { type: "resource_link", name: "report", uri: "https://example.test/report" },
    ] }) });
    expect(html).toContain("total"); expect(html).toContain("Independent explanation");
    expect(html).toContain('src="data:image/png;base64,aGk="'); expect(html).toContain("<audio"); expect(html).toContain("report</a>");
    expect(html).not.toContain('{&quot;total&quot;:3}');
  });
  it("never rounds distinct large IDs while deduplicating", async () => {
    const html = await render(McpResult, { result: result(""), details: mcp({ structured_content: { id: 9007199254740992 }, content: [{ type: "text", text: '{"id":9007199254740993}' }] }) });
    expect(html).toContain("9007199254740992"); expect(html).toContain("9007199254740993");
  });
  it("keeps root array values visible without a second JSON-tree toggle", async () => {
    const html = await render(McpResult, { result: result(""), details: mcp({ structured_content: [{ value: "first record" }] }) });
    expect(html).toContain("first record"); expect(html.match(/aria-expanded=/g)).toHaveLength(1);
  });
  it("does not repeat the host's exact structured-only JSON wrapper", async () => {
    const html = await render(McpResult, { result: result('[structured content]\n{"count":3}'), details: mcp({ structured_content: { count: 3 } }) });
    expect(html.match(/count/g)).toHaveLength(1); expect(html).not.toContain("[structured content]");
  });
  it("copies the complete structured payload rather than its visible prefix", async () => {
    const data = { records: Array.from({ length: 40 }, (_, id) => ({ id, label: `record ${id}` })) };
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } }); vi.stubGlobal("window", { setTimeout: vi.fn() });
    const node = h(McpResult, { result: result(""), details: mcp({ structured_content: data }) });
    await renderToString(createSSRApp({ render: () => node }).use(i18n));
    const instance = node.component as unknown as { setupState: { copyStructured(): Promise<void> } };
    await instance.setupState.copyStructured();
    expect(writeText).toHaveBeenCalledWith(JSON.stringify(data, null, 2)); expect(writeText.mock.calls[0][0]).toContain("record 39");
  });
  it("shows error, timeout, truncation, omitted media and error-side diagnostics", async () => {
    const html = await render(McpResult, { headerless: true, result: result("Permission denied", { is_error: true }), details: mcp({ timed_out: true, truncated: true, structured_content: { code: "DENIED" }, content: [{ type: "image", size: 600000, omitted: true }, { type: "audio", size: 700000, omitted: true }, { type: "unknown" }] }) });
    for (const text of ["Permission denied", "DENIED", "已超时", "已截断", "600000", "700000", "未知内容块"]) expect(html).toContain(text);
    expect(html).not.toContain('class="identity"'); expect(html.match(/aria-expanded=/g)).toHaveLength(1);
  });
  it("keeps embedded resource bodies accessible instead of treating the resource link as their contents", async () => {
    const text = '[embedded resource: uri="file:///report"]\nActual embedded body';
    const html = await render(McpResult, { result: result(text), details: mcp({ content: [{ type: "resource", uri: "file:///report", mime_type: "text/plain" }] }) });
    expect(html).toContain("Actual embedded body"); expect(html).toContain('href="file:///report"');
  });
  it("shows the beginning of a long embedded resource and retains its complete body in details", async () => {
    const text = "Embedded body beginning\n" + "x".repeat(1400) + "RESOURCE_BODY_END";
    const props = { result: result(text), details: mcp({ content: [{ type: "resource", uri: "file:///report", mime_type: "text/plain" }] }) };
    const closed = await render(McpResult, props);
    expect(closed).toContain("Embedded body beginning"); expect(closed).toContain("仅显示开头部分"); expect(closed).not.toContain("RESOURCE_BODY_END");
    const state = createCardDisclosureState(() => "session-a"); state.set("call:c1:details", true);
    expect(await render(McpResult, props, state)).toContain("RESOURCE_BODY_END");
  });
  it("keeps long raw outputs behind the one detail entry with an honest visible prefix", async () => {
    const text = "Exact beginning\n" + "x".repeat(1400) + "UNSHOWN_END";
    const closed = await render(McpResult, { result: result(text), details: mcp() });
    expect(closed).toContain("Exact beginning"); expect(closed).toContain("仅显示开头部分"); expect(closed).not.toContain("UNSHOWN_END");
    const state = createCardDisclosureState(() => "session-a"); state.set("call:c1:details", true);
    const opened = await render(McpResult, { result: result(text), details: mcp(), args: { query: "original" } }, state);
    expect(opened).toContain("UNSHOWN_END"); expect(opened).toContain("original"); expect(opened.match(/aria-expanded=/g)).toHaveLength(1);
  });
  it("retains long content block tails when the model-facing result is a different short summary", async () => {
    const text = "Typed block beginning " + "x".repeat(1400) + "TYPED_BLOCK_TAIL";
    const props = { result: result("summary"), details: mcp({ content: [{ type: "text", text }] }) };
    const closed = await render(McpResult, props); expect(closed).toContain("Typed block beginning"); expect(closed).not.toContain("TYPED_BLOCK_TAIL");
    const state = createCardDisclosureState(() => "session-a"); state.set("call:c1:details", true);
    const opened = await render(McpResult, props, state); expect(opened).toContain("TYPED_BLOCK_TAIL"); expect(opened.match(/aria-expanded=/g)).toHaveLength(1);
  });
  it("keeps complete long errors readable without opening details", async () => {
    const text = "Error beginning\n" + "x".repeat(1500) + "ERROR_END";
    const html = await render(McpResult, { result: result(text, { is_error: true }), details: mcp() });
    expect(html).toContain("ERROR_END"); expect(html).toContain('aria-expanded="false"'); expect(html).not.toContain("仅显示开头部分");
  });
});

describe("compact Skill result content", () => {
  it("does not repeat an artifact-only CallResult envelope and retains the artifact itself", async () => {
    const details = skill({ data: null, artifacts: [{ artifactId: "artifact:one", name: "report.csv", mediaType: "text/csv", sizeBytes: 2048 }], packageDigest: "hidden-digest" });
    const html = await render(SkillResult, { headerless: true, result: result(JSON.stringify(details)), details });
    expect(html).toContain("report.csv"); expect(html).toContain("text/csv"); expect(html).toContain("2.0 KiB");
    expect(html).not.toContain("hidden-digest"); expect(html).not.toContain("&quot;artifacts&quot;"); expect(html).not.toContain('class="identity"');
  });
  it.each([false, 0, "", [], {}].map(value => [value]))("does not drop the valid data value %j", async value => {
    const details = skill({ data: value }); const html = await render(SkillResult, { result: result(JSON.stringify(details)), details });
    const visible = html.match(/<pre[^>]*>([\s\S]*?)<\/pre>/)?.[1];
    expect(visible).toBe(JSON.stringify(value, null, 2)?.replace(/"/g, "&quot;"));
    expect(html).not.toContain("&quot;capability&quot;");
  });
  it("preserves independent text and error diagnostics, and escapes all returned markup", async () => {
    const details = skill({ data: { diagnostic: "retained" } });
    for (const is_error of [false, true]) {
      const html = await render(SkillResult, { result: result('<script>alert(1)</script>', { is_error }), details });
      expect(html).toContain("retained"); expect(html).toContain("&lt;script&gt;"); expect(html).not.toContain("<script>");
    }
  });
  it("uses the same scoped detail choice across remounts and isolates sessions and calls", async () => {
    const sid = ref("session-a"), state = createCardDisclosureState(() => sid.value);
    state.set("call:c1:details", true);
    const props = { result: result("short"), details: skill({ packageDigest: "hidden-identity" }), args: { query: "raw argument" } };
    expect(await render(SkillResult, props, state)).toContain("raw argument");
    expect(await render(SkillResult, { ...props, result: result("updated") }, state)).toContain("raw argument");
    sid.value = "session-b"; expect(await render(SkillResult, props, state)).not.toContain("raw argument");
    sid.value = "session-a"; expect(await render(SkillResult, props, state)).toContain("raw argument");
    expect(await render(SkillResult, { ...props, result: result("other", { call_id: "c2" }) }, state)).not.toContain("raw argument");
  });
  it("keeps English result details readable", async () => {
    setLocale("en"); const html = await render(SkillResult, { result: result("x".repeat(1400)), details: skill() });
    expect(html).toContain("Details"); expect(html).toContain("Showing the beginning");
  });
});
