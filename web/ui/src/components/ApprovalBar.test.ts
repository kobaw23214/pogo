import { afterEach, describe, expect, it, vi } from "vitest";
import { computed, createSSRApp, h, provide, ref } from "vue";
import { renderToString } from "vue/server-renderer";
import type { PendingGate, ToolName } from "@/api/types";
import { approvalUiKey, type ApprovalUiState } from "@/agent/approvalState";
import { i18n, setLocale } from "@/i18n";
import ApprovalBar from "./ApprovalBar.vue";
import GateCard from "./GateCard.vue";

setLocale("zh-CN");
const baseGate: PendingGate = { gate_id: "g1", call_id: "c1", tool: "bash", args: { command: "echo <hello>" }, deadline: 61000 };
function uiState(gates: PendingGate[], extra: Partial<ApprovalUiState> = {}): ApprovalUiState {
  return { pending: ref({}), errors: ref({}), activeGateIds: computed(() => new Set(gates.map(g => g.gate_id))), serverOffset: ref(0), canDecide: true, sessionId: ref("session-a"), ...extra };
}
function renderGate(gate: PendingGate, state?: ApprovalUiState) {
  return renderToString(createSSRApp({ setup() {
    if (state) provide(approvalUiKey, state);
    return () => h(GateCard, { gate, live: false, compact: true });
  } }).use(i18n));
}
afterEach(() => vi.restoreAllMocks());

describe("approval cards independent of transcript state", () => {
  it("uses the server clock and active approval identity even when its transcript is idle", async () => {
    vi.spyOn(Date, "now").mockReturnValue(121000);
    const html = await renderGate(baseGate, uiState([baseGate], { serverOffset: ref(-120000) }));
    expect(html).toContain("剩余 60s");
    expect(html).toContain("批准</button>");
    expect(html).not.toContain('class="gate expired');
    expect(html).toContain("echo &lt;hello&gt;");
    expect(html).toContain('class="operation-summary"');
  });

  it("keeps frozen approvals actionable after their original deadline and restores the real remaining time", async () => {
    vi.spyOn(Date, "now").mockReturnValue(121000);
    const gate = { ...baseGate, frozen: true, remaining_ms: 25000 };
    const html = await renderGate(gate, uiState([gate]));
    expect(html).toContain("编辑中，计时已暂停");
    expect(html).toContain("批准</button>");
    expect(html).not.toContain("已失效");
  });

  it("disables both copies while submitting and allows retries with a visible API error", async () => {
    vi.spyOn(Date, "now").mockReturnValue(1000);
    const state = uiState([baseGate], { pending: ref({ g1: true }) });
    const submitting = await renderGate(baseGate, state);
    expect(submitting).toContain('class="primary" disabled');
    state.pending.value = { g1: false };
    state.errors.value = { g1: "网络暂时不可用，可重试" };
    const retry = await renderGate(baseGate, state);
    expect(retry).not.toContain('class="primary" disabled');
    expect(retry).toContain('role="alert"');
    expect(retry).toContain("网络暂时不可用，可重试");
  });

  it("rejects stale identity and delegated read-only contexts without leaving active buttons", async () => {
    vi.spyOn(Date, "now").mockReturnValue(1000);
    for (const state of [uiState([]), uiState([baseGate], { canDecide: false })]) {
      const html = await renderGate(baseGate, state);
      expect(html).toContain("已失效");
      expect(html).not.toContain("批准</button>");
    }
  });

  it("shows the pending count, safe operation summary and child provenance in a compact bar", async () => {
    vi.spyOn(Date, "now").mockReturnValue(1000);
    const gates: PendingGate[] = [
      { ...baseGate, call_id: "", tool: "browser-arm" as ToolName, args: { url: "https://example.test/path" }, origin: "child" },
      { ...baseGate, gate_id: "g2", tool: "office_write" as ToolName, args: { path: "report.xlsx" } },
    ];
    const html = await renderToString(createSSRApp({ setup() {
      provide(approvalUiKey, uiState(gates));
      return () => h(ApprovalBar, { gates });
    } }).use(i18n));
    expect(html).toContain('data-testid="approval-bar"');
    expect(html).toContain("1 / 2");
    expect(html).toContain("子 Agent 请求");
    expect(html).toContain("browser-arm");
    expect(html).toContain("https://example.test/path");
    expect(html).toContain("查看调用详情");
    expect(html).not.toContain('type="checkbox"');
  });
});
