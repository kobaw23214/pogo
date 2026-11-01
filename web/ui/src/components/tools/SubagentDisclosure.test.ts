import { describe, expect, it, vi } from "vitest";
import { createSSRApp, defineComponent, h, ref } from "vue";
import { renderToString } from "vue/server-renderer";
import { cardDisclosureKey, createCardDisclosureState, type CardDisclosureState } from "@/agent/cardDisclosure";
import { i18n, setLocale } from "@/i18n";
import type { BgTask, SubagentFrame } from "@/api/types";
import type { TimelineCall } from "@/agent/timeline";
import type { SubagentSummaryInput } from "@/agent/subagentSummary";
vi.mock("../CodeBlock.vue", () => ({ default: defineComponent({ props: ["code"], setup: props => () => h("pre", props.code) }) }));
vi.mock("../RollingLine.vue", () => ({ default: defineComponent({ props: ["text"], setup: props => () => h("span", props.text) }) }));
vi.mock("../StreamTailWindow.vue", () => ({ default: defineComponent({ props: ["text"], setup: props => () => h("span", props.text) }) }));
import SubagentResult from "./SubagentResult.vue";
import ToolCall from "../ToolCall.vue";

setLocale("zh-CN");
const frames: SubagentFrame[] = [
  { type: "turn_start", turn: 1 },
  { type: "tool_start", name: "read", call_id: "child-read", args: { path: "a.txt" } },
  { type: "tool_end", name: "read", call_id: "child-read", text: "payload-from-child" },
  { type: "tool_start", name: "grep", call_id: "child-grep", args: { pattern: "match" } },
  { type: "tool_end", name: "grep", call_id: "child-grep", text: "grep-output" },
];
interface CardProps extends SubagentSummaryInput { callId?: string; }
async function renderCard(props: CardProps, state?: CardDisclosureState) {
  const app = createSSRApp({ render: () => h(SubagentResult, props) }).use(i18n);
  app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
  if (state) app.provide(cardDisclosureKey, state);
  return renderToString(app);
}
const rememberOpen = (state: CardDisclosureState) => {
  state.set("call:parent:details", true);
  state.set("call:parent:subagent:parent:group:turn:1:call:child-read", true);
  state.set("call:parent:subagent:parent:tool:turn:1:call:child-read", true);
};

describe("subagent disclosure continuity", () => {
  it("retains the parent card, first-call explore group and child result across remount and shifted positions", async () => {
    const state = createCardDisclosureState(() => "session"); rememberOpen(state);
    expect(await renderCard({ callId: "parent", frames, running: true }, state)).toContain("payload-from-child");
    const shifted: SubagentFrame[] = [{ type: "turn_start", turn: 0 }, { type: "tool_start", name: "bash", call_id: "earlier", args: { command: "pwd" } }, { type: "tool_end", name: "bash", call_id: "earlier", text: "done" }, ...frames];
    const settled = await renderCard({ callId: "parent", frames: shifted, running: false, result: { call_id: "parent", text: "finished delegate" } }, state);
    expect(settled).toContain("payload-from-child"); expect(settled).toContain("finished delegate");
    state.set("call:parent:details", false);
    expect(await renderCard({ callId: "parent", frames, running: false }, state)).not.toContain("payload-from-child");
  });

  it("keeps identical parent and child ids isolated between sessions", async () => {
    const session = ref("a"), state = createCardDisclosureState(() => session.value); rememberOpen(state);
    expect(await renderCard({ callId: "parent", frames, running: false }, state)).toContain("payload-from-child");
    session.value = "b";
    expect(await renderCard({ callId: "parent", frames, running: false }, state)).not.toContain("payload-from-child");
    session.value = "a";
    expect(await renderCard({ callId: "parent", frames, running: false }, state)).toContain("payload-from-child");
  });

  it("receives the real parent call identity before any tool result exists", async () => {
    const state = createCardDisclosureState(() => "session"); rememberOpen(state);
    const call: TimelineCall = { callId: "parent", name: "subagent", args: { task: "Review" }, running: true, orphaned: false, liveFrames: frames };
    const app = createSSRApp({ render: () => h(ToolCall, { call, runActive: true }) }).use(i18n);
    app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
    app.provide(cardDisclosureKey, state);
    expect(await renderToString(app)).toContain("payload-from-child");
  });

  it("shows the background task's current action without an acknowledgement duration", async () => {
    const bgTask: BgTask = { task_id: "bg1", mode: "read", task: "Review", status: "running", started_at: 1 };
    const html = await renderCard({ callId: "parent", running: false, frames: frames.slice(0, 2), result: { call_id: "parent", text: "dispatched", duration_ms: 11 }, bgTask });
    expect(html).toContain("a.txt"); expect(html).toContain("running"); expect(html).not.toContain("11ms");
    const finished = await renderCard({ callId: "parent", running: false, result: { call_id: "parent", text: "dispatched", duration_ms: 11 }, bgTask: { ...bgTask, status: "done", duration_ms: 2400 } });
    expect(finished).toContain("2.4s"); expect(finished).not.toContain("11ms");
  });

  it("preserves choices when final run metadata arrives and isolates reused call ids by child turn", async () => {
    const state = createCardDisclosureState(() => "session");
    state.set("call:parent:details", true);
    state.set("call:parent:subagent:parent:tool:turn:1:call:reused", true);
    const repeated: SubagentFrame[] = [{ type: "turn_start", turn: 1 }, { type: "tool_start", call_id: "reused", name: "read" },
      { type: "tool_end", call_id: "reused", name: "read", text: "FIRST_TURN_OUTPUT" },
      { type: "turn_start", turn: 2 }, { type: "tool_start", call_id: "reused", name: "read" },
      { type: "tool_end", call_id: "reused", name: "read", text: "SECOND_TURN_OUTPUT" }];
    const html = await renderCard({ callId: "parent", frames: repeated, running: false,
      result: { call_id: "parent", text: "Final answer", details: { id: "late-run-id", mode: "explore" } } }, state);
    expect(html).toContain("FIRST_TURN_OUTPUT"); expect(html).not.toContain("SECOND_TURN_OUTPUT");
  });

  it("shows one task summary and keeps the full original result one click away", async () => {
    const state = createCardDisclosureState(() => "session");
    const result = { call_id: "parent", text: `Original result\n${"detail\n".repeat(100)}RAW_RESULT_END` };
    const props = { callId: "parent", running: false, args: { task: "Exact delegated task" }, result };
    const collapsed = await renderCard(props, state);
    expect(collapsed).toContain("Exact delegated task"); expect(collapsed).not.toContain("RAW_RESULT_END");
    state.set("call:parent:subagent:result", true);
    expect(await renderCard(props, state)).toContain("RAW_RESULT_END");
    const call: TimelineCall = { callId: "parent", name: "subagent", args: props.args, running: false, orphaned: false, result };
    const app = createSSRApp({ render: () => h(ToolCall, { call, runActive: false }) }).use(i18n);
    app.component("el-icon", defineComponent({ setup: (_, { slots }) => () => h("span", slots.default?.()) }));
    const html = await renderToString(app);
    expect(html.match(/class="summary-head"/g)).toHaveLength(1);
    expect(html).not.toContain('data-tool-kind="subagent"');
  });

  it("keeps a failure readable while activity is closed and never displays a dispatch acknowledgement as output", async () => {
    const html = await renderCard({ callId: "parent", running: false, args: { task: "Inspect records" },
      result: { call_id: "parent", text: "ACK_MARKER", details: { id: "bg1", background: true, mode: "explore" } },
      bgTask: { task_id: "bg1", mode: "explore", task: "Inspect records", status: "failed", started_at: 1, error: "EXACT_FAILURE_REASON" } });
    expect(html).toContain("EXACT_FAILURE_REASON"); expect(html).not.toContain("ACK_MARKER");
    expect(html).toContain('data-subagent-state="failed"'); expect(html).not.toContain('class="summary-details"');
  });

  it("reuses MCP result content and scopes raw parameters by parent run and child turn", async () => {
    const state = createCardDisclosureState(() => "session"); state.set("call:parent:details", true);
    for (const turn of [1, 2]) state.set(`call:parent:subagent:parent:tool:turn:${turn}:call:same`, true);
    state.set("call:parent:subagent:parent:tool:turn:1:call:same:params", true);
    const nested: SubagentFrame[] = [1, 2].flatMap(turn => [
      { type: "turn_start" as const, turn },
      { type: "tool_start" as const, name: "mcp_server_lookup", call_id: "same", args: { query: "lookup", limit: 2, extra: turn === 1 ? "FIRST_RAW_ARGUMENT" : "SECOND_RAW_ARGUMENT" } },
      { type: "tool_end" as const, name: "mcp_server_lookup", call_id: "same", text: "business result", details: { server: "records", tool: "lookup", structured_content: { found: `RECORD_${turn}` } } },
    ]);
    const html = await renderCard({ callId: "parent", running: false, frames: nested }, state);
    expect(html).toContain("RECORD_1"); expect(html).toContain("RECORD_2");
    expect(html).toContain("FIRST_RAW_ARGUMENT"); expect(html).not.toContain("SECOND_RAW_ARGUMENT");
  });
});
