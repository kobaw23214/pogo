import { computed, createApp, defineComponent, h, reactive, ref } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import { i18n, setLocale } from "../src/i18n";
import { applyTheme } from "../src/theme";
import { provideCardDisclosure } from "../src/agent/cardDisclosure";
import { buildExchanges } from "../src/agent/exchanges";
import type { TimelineCall, TurnItem } from "../src/agent/timeline";
import type { Live, MCPDetails, SkillDetails } from "../src/api/types";
import TurnCard from "../src/components/TurnCard.vue";
import WorkLog from "../src/components/WorkLog.vue";
import ToolCall from "../src/components/ToolCall.vue";
import LiveWorkHeader from "../src/components/LiveWorkHeader.vue";

// Only invented input is rendered. No server, model or real session is used.
setLocale("zh-CN");
applyTheme("paper", false);
const timestamp = Date.now();
const toolName = (name: string) => name as TimelineCall["name"];
const thumbnail = "data:image/svg+xml;base64," + btoa('<svg xmlns="http://www.w3.org/2000/svg" width="160" height="60"><rect width="160" height="60" rx="8" fill="#e6f2ed"/><path d="M16 44 44 28 72 38 100 14 136 22" fill="none" stroke="#278461" stroke-width="3"/></svg>');

function fixtureTurn(label: string): TurnItem {
  const mcp: MCPDetails = {
    server: "example-data", tool: "inventory_lookup", title: "查询库存资料",
    description: "用于隔离验证的虚构数据。", duration_ms: 230,
    structured_content: { count: 2, records: [{ name: "样本甲", amount: 12 }, { name: "样本乙", amount: 8 }] },
    content: [
      { type: "text", text: `${label} 独立说明：仅查询了本批次两条记录。` },
      { type: "image", mime_type: "image/svg+xml", data: thumbnail.split(",")[1] },
      { type: "resource_link", uri: "https://example.test/evidence", name: "示例来源" },
    ],
    annotations: { read_only_hint: true },
  };
  const skill: SkillDetails = {
    capability: "example.report.summarize", callId: "skill-local",
    data: { source: label, processed: 2, result: "摘要已生成" },
    artifacts: [{ artifactId: "demo-output", name: "示例摘要.txt", sizeBytes: 128, mediaType: "text/plain" }],
    packageDigest: "fixture-package", executorIdentity: "fixture-executor",
  };
  return {
    kind: "turn", id: "m1", index: 1, streaming: true,
    thinking: `${label}：先核对来源和字段，再整理结果。实时思考应持续可见；手动展开后，本轮结束也不收起。\n正在检查工具返回的独立文字、结构化数据和图片。`,
    text: "", sources: [],
    calls: [
      { callId: "c1", name: toolName("mcp_hashed_wire"), args: { query: "仅验证示例", limit: 2 }, running: false, orphaned: false,
        result: { call_id: "c1", text: `${label} 返回两条示例记录。`, details: mcp, duration_ms: 230 } },
      { callId: "c2", name: toolName("skill_example_report"), args: { source: "示例资料", format: "text" }, running: false, orphaned: false,
        result: { call_id: "c2", text: JSON.stringify(skill), details: skill, duration_ms: 540 } },
      { callId: "c3", name: "bash", args: { command: "printf 'fixture verification\\n'" }, running: true, orphaned: false,
        liveOutput: `${label}：开始核对\n实时输出仍然可见\n` },
      { callId: "c4", name: "subagent", args: { mode: "read", task: "检查示例字段" }, running: false, orphaned: false,
        liveFrames: [
          { type: "session", model: "fixture-model" }, { type: "turn_start", turn: 1 },
          { type: "tool_start", call_id: "child-read-1", name: "read", args: { path: "child-example.txt" } },
          { type: "tool_end", call_id: "child-read-1", name: "read", text: "子任务读取的示例内容" },
          { type: "run_end", stop_reason: "done" },
        ], result: { call_id: "c4", text: "子任务已完成", duration_ms: 200 } },
    ],
  };
}

const Fixture = defineComponent({
  setup() {
    const displaySession = ref("fixture-A");
    const requestedSession = ref("fixture-A");
    const disclosure = provideCardDisclosure(() => displaySession.value);
    const sessions = reactive<Record<string, { turn: TurnItem; busy: boolean; folded: boolean }>>({
      "fixture-A": { turn: fixtureTurn("会话 A"), busy: true, folded: false },
      "fixture-B": { turn: fixtureTurn("会话 B"), busy: true, folded: false },
    });
    const current = computed(() => sessions[displaySession.value]!);
    const exchanges = computed(() => buildExchanges([
      { kind: "user", id: "u1", text: "整理示例资料", ts: timestamp }, current.value.turn,
    ], current.value.busy));
    const exchange = computed(() => exchanges.value[0]!);
    const live = computed(() => ({
      thinking: current.value.turn.thinking,
      pending_tools: current.value.turn.calls.filter(call => call.running),
      pending_gates: current.value.turn.calls.flatMap(call => call.gate ? [call.gate] : []),
    } as unknown as Live));
    const effectiveFolded = computed(() => current.value.folded && live.value.pending_gates.length === 0);
    function finish() {
      current.value.turn.streaming = false;
      current.value.turn.text = "示例核对完成。最终回答保持可见。";
      current.value.turn.ts = timestamp + 4500;
      current.value.turn.thinkingMs = 1200;
      const command = current.value.turn.calls.find(call => call.callId === "c3")!;
      command.running = false;
      command.result = { call_id: "c3", text: command.liveOutput ?? "", details: { command: "printf 'fixture verification\\n'", exit_code: 0, duration_ms: 180, truncated: false } };
      command.liveOutput = undefined;
      current.value.busy = false;
    }
    function gate() {
      const command = current.value.turn.calls.find(call => call.callId === "c3")!;
      command.gate = {
        gate_id: "fixture-gate", call_id: command.callId, tool: "bash",
        args: command.args, deadline: Date.now() + 600000,
      };
    }
    function decide() {
      const command = current.value.turn.calls.find(call => call.callId === "c3")!;
      command.gate = undefined;
    }
    const controls = [
      ["追加流式内容", () => { current.value.turn.thinking += "\n新增片段：保留已展开的内容。"; }],
      ["完成本轮", finish],
      ["重建快照", () => { current.value.turn = structuredClone(JSON.parse(JSON.stringify(current.value.turn))); }],
      ["切换会话目标", () => { requestedSession.value = displaySession.value === "fixture-A" ? "fixture-B" : "fixture-A"; }],
      ["接收目标快照", () => { displaySession.value = requestedSession.value; }],
      ["审批到达", gate],
      ["结束示例审批", decide],
      ["后台子任务开始", () => {
        const child = current.value.turn.calls.find(call => call.callId === "c4")!;
        child.bgTask = { task_id: "fixture-background", mode: "read", task: "继续检查示例", status: "running", started_at: Date.now() };
      }],
      ["后台子任务完成", () => {
        const child = current.value.turn.calls.find(call => call.callId === "c4")!;
        if (child.bgTask) { child.bgTask.status = "done"; child.bgTask.duration_ms = 300; }
      }],
      ["浅色", () => applyTheme("paper", false)],
      ["深色", () => applyTheme("graphite", false)],
      ["English", () => setLocale("en")],
      ["中文", () => setLocale("zh-CN")],
    ] as const;

    const failures: TimelineCall[] = [
      { callId: "failure", name: toolName("mcp_external"), args: { query: "示例" }, running: false, orphaned: false,
        result: { call_id: "failure", is_error: true, text: "示例错误：来源暂时不可用，尚未完成查询。",
          details: { server: "example-data", tool: "lookup", timed_out: true, structured_content: { retryable: true }, content: [{ type: "text", text: "独立诊断说明应可见。" }] } } },
      { callId: "invalid", name: "shortcut_manage", args: '{"bad":"<script>fixture</script>"', inputInvalid: true,
        inputError: "参数格式错误，未执行", running: false, orphaned: false },
      { callId: "old", name: "read", args: { path: "旧记录.txt" }, running: false, orphaned: false,
        result: { call_id: "old", text: "旧记录缺少 details 时仍显示原文。" } },
      { callId: "office", name: toolName("docs_office"), args: { operation: "open", path: "示例报表.xlsx" }, running: false, orphaned: false,
        result: { call_id: "office", text: "示例 Office 文档已打开。" } },
    ];
    return () => h("main", [
      h("header", { class: "fixture-heading" }, [h("h1", "Pogo · 工具卡片第一轮"),
        h("p", "隔离真实组件 · 虚构数据 · 不连接正式服务、不调用模型")]),
      h("div", { class: "fixture-controls" }, controls.map(([text, onClick]) => h("button", { type: "button", onClick }, text))),
      h("output", { class: "fixture-status", "data-testid": "fixture-status" }, `显示 ${displaySession.value} · 目标 ${requestedSession.value} · ${current.value.busy ? "执行中" : "已结束"}`),
      h("section", { class: "fixture-panel", "data-testid": "lifecycle" }, [
        h("h2", "运行 → 完成 → 快照重建 → 同编号会话切换"),
        current.value.busy
          ? h("div", { key: displaySession.value }, [
            h(LiveWorkHeader, { exchange: exchange.value, live: live.value, folded: effectiveFolded.value,
              onToggle: () => { current.value.folded = !current.value.folded; disclosure.set("work:u1", !current.value.folded); } }),
            h(TurnCard, { turn: current.value.turn, runActive: true, workFolded: effectiveFolded.value, onDecide: decide }),
          ])
          : h(WorkLog, { key: displaySession.value, exchange: exchange.value, runActive: live.value.pending_gates.length > 0, animateFold: true, onDecide: decide }),
      ]),
      h("section", { class: "fixture-panel fixture-gallery", "data-testid": "fallbacks" }, [h("h2", "错误、非法参数和旧记录"),
        ...failures.map(call => h(ToolCall, { key: call.callId, call, runActive: true })),
      ]),
    ]);
  },
});
createApp(Fixture).use(i18n).use(ElementPlus).mount("#app");
