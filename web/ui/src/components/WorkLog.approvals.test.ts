import { afterEach, describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h } from "vue";
import { renderToString } from "vue/server-renderer";
import type { Exchange } from "@/agent/exchanges";
import { i18n, setLocale } from "@/i18n";
import { worklogExpandAll } from "@/agent/worklogPrefs";
vi.mock("./ToolCall.vue", () => ({ default: defineComponent({ props: ["call"], setup: props => () => h("div", { "data-live-gate": props.call.gate?.gate_id }, "approval") }) }));
vi.mock("./AnswerText.vue", () => ({ default: defineComponent({ props: ["source"], setup: props => () => h("div", props.source) }) }));
import WorkLog from "./WorkLog.vue";

setLocale("zh-CN");
afterEach(() => vi.unstubAllGlobals());
describe("approval visibility in folded work logs", () => {
  it.each(["read", "bash", "edit"] as const)("keeps the pending %s approval visible without another row click", async name => {
    vi.stubGlobal("window", { clearTimeout: vi.fn() });
    worklogExpandAll.value = false;
    const exchange: Exchange = {
      id: "finished", notices: [], finished: true,
      stats: { calls: 1, failed: 0, files: 0, tools: { [name]: 1 } },
      turns: [{
        id: "t1", kind: "turn", index: 1, text: "", thinking: "", streaming: false,
        calls: [{ callId: "c1", name, args: { path: "file.txt", command: "pwd" }, running: false, orphaned: false,
          gate: { gate_id: "pending", call_id: "c1", tool: name, deadline: Date.now() + 60000 } }],
        sources: [],
      }],
    };
    const html = await renderToString(createSSRApp({ render: () => h(WorkLog, { exchange, runActive: false }) }).use(i18n));
    expect(html).toContain('class="worklog open"');
    expect(html).toContain('data-live-gate="pending"');
    expect(html).toContain('aria-expanded="true"');
  });
});
