import { computed, createApp, defineComponent, h, nextTick, reactive, ref } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import { i18n, setLocale } from "../src/i18n";
import { applyTheme } from "../src/theme";
import { provideCardDisclosure } from "../src/agent/cardDisclosure";
import { buildExchanges } from "../src/agent/exchanges";
import { jumpToCite } from "../src/agent/citeJump";
import type { TimelineCall, TurnItem } from "../src/agent/timeline";
import { emptyLive, type Live, type MCPDetails, type PendingGate, type SkillDetails } from "../src/api/types";
import WorkLog from "../src/components/WorkLog.vue";
import ApprovalBar from "../src/components/ApprovalBar.vue";

// Invented inputs only. This page imports production components but never
// connects to the Pogo server, starts an agent, or calls a real tool.
setLocale("zh-CN");
applyTheme("paper", false);
const ts = Date.now();
const name = (value: string) => value as TimelineCall["name"];
const imageData = btoa('<svg xmlns="http://www.w3.org/2000/svg" width="160" height="60"><rect width="160" height="60" fill="#e6f2ed"/><path d="M12 46 44 20 76 36 110 12 148 26" stroke="#278461" fill="none" stroke-width="3"/></svg>');
const mcp: MCPDetails = { server: "fixture-data", tool: "lookup", title: "查询示例资料", duration_ms: 120,
  content: [{ type: "text", text: "独立说明：当前仅返回两条示例，不能声称读取全部记录。" },
    { type: "image", mime_type: "image/svg+xml", data: imageData }, { type: "resource_link", uri: "https://example.test/evidence", name: "示例来源" }],
  structured_content: { count: 2, records: [{ label: "样本甲", amount: 12 }, { label: "样本乙", amount: 8 }] } };
const skill: SkillDetails = { capability: "fixture.report", callId: "wire-skill", data: { processed: 2, result: "示例摘要" },
  artifacts: [{ artifactId: "fixture.txt", name: "示例摘要.txt", sizeBytes: 128, mediaType: "text/plain" }], packageDigest: "fixture-package", executorIdentity: "fixture-executor" };
function read(callId: string, path: string, running = false): TimelineCall {
  return { callId, name: "read", args: { path }, running, orphaned: false,
    ...(!running ? { result: { call_id: callId, text: `示例内容 ${path}`, source: { label: "S1", type: "file", title: path, locator: path, call_id: callId, excerpt: "隔离示例", hash: "fixture", ts } } } : {}) };
}
function turn(label: string): TurnItem {
  return { kind: "turn", id: "m1", index: 1, streaming: true, thinking: `${label}：先`, text: "", sources: [], calls: [read("read-1", "资料甲.txt")] };
}
type Session = { turn: TurnItem; busy: boolean };
const Fixture = defineComponent({
  setup() {
    const displayed = ref("fixture-A");
    const requested = ref("fixture-A");
    provideCardDisclosure(() => displayed.value);
    const sessions = reactive<Record<string, Session>>({ "fixture-A": { turn: turn("会话 A"), busy: true }, "fixture-B": { turn: turn("会话 B"), busy: true } });
    const current = computed(() => sessions[displayed.value]!);
    const exchange = computed(() => buildExchanges([
      { kind: "user", id: "u1", text: "分析示例资料", ts }, current.value.turn,
      { kind: "notice", id: "notice", text: "隔离系统提示：只使用示例资料。" },
      { kind: "user", id: "steering", text: "补充要求：保留来源和附件。", steering: true, files: [{ path: "补充.txt", name: "补充.txt" }] },
    ], current.value.busy)[0]!);
    const gates = computed(() => current.value.turn.calls.flatMap(call => [...(call.gate ? [call.gate] : []), ...(call.childGates ?? [])]));
    const live = computed<Live>(() => ({ ...emptyLive(), active: current.value.busy, thinking: current.value.turn.thinking,
      pending_tools: current.value.turn.calls.filter(call => call.running).map(call => ({ call_id: call.callId, name: call.name, args: call.args, started_at: call.startedAt ?? ts })), pending_gates: gates.value }));
    const log = ref<string[]>([]);
    const panel = ref<HTMLElement>();
    const record = (value: string) => { log.value = [...log.value, value]; };
    async function stableMutation(label: string, fn: () => void) {
      const before = [...(panel.value?.querySelectorAll<HTMLElement>(".activity-content > .activity-list, .activity-turn, .activity-group, .call, .child-tool") ?? [])];
      const focus = document.activeElement;
      fn(); await nextTick();
      const after = [...(panel.value?.querySelectorAll<HTMLElement>(".activity-content > .activity-list, .activity-turn, .activity-group, .call, .child-tool") ?? [])];
      const retained = before.every(element => after.includes(element));
      const order = before.filter(element => after.includes(element)).every((element, index, list) => index === 0 || after.indexOf(list[index - 1]) < after.indexOf(element));
      record(`${label}: mounted=${retained}, order=${order}, focus=${document.activeElement === focus}`);
    }
    function addSecond() { void stableMutation("singleton→group", () => {
      if (!current.value.turn.calls.some(call => call.callId === "read-2")) current.value.turn.calls.push(read("read-2", "资料乙.txt", true));
    }); }
    function addParallel() { void stableMutation("parallel append", () => {
      if (current.value.turn.calls.some(call => call.callId === "read-3")) return;
      current.value.turn.calls.push(read("read-3", "资料丙.txt", true), read("read-4", "资料丁.txt", true));
    }); }
    function settleReverse() { void stableMutation("reverse completion", () => {
      for (const call of [...current.value.turn.calls].reverse()) if (call.name === "read") { call.running = false; call.result = { ...call.result, call_id: call.callId, text: `逆序返回：${(call.args as { path: string }).path}` }; }
    }); }
    function addRich() {
      if (current.value.turn.calls.some(call => call.callId === "mcp-1")) return;
      current.value.turn.calls.push(
        { callId: "mcp-1", name: name("mcp_fixture"), args: { query: "示例", limit: 2 }, running: false, orphaned: false, result: { call_id: "mcp-1", text: "返回两条示例记录", details: mcp, duration_ms: 120 } },
        { callId: "skill-1", name: name("skill_fixture"), args: { source: "示例资料" }, running: false, orphaned: false, result: { call_id: "skill-1", text: "示例 Skill 结果", details: skill, duration_ms: 250 } },
        { callId: "bash-1", name: "bash", args: { command: "printf fixture" }, running: true, orphaned: false, liveOutput: "示例当前动作：持续核对中\n" },
      );
    }
    function child() {
      if (current.value.turn.calls.some(call => call.callId === "child-1")) return;
      current.value.turn.calls.push({ callId: "child-1", name: "subagent", args: { mode: "read", task: "核对来源与字段" }, running: true, orphaned: false, startedAt: Date.now(), liveFrames: [
        { type: "session", session: "/fixture/child-session.jsonl", model: "fixture-model" }, { type: "turn_start", turn: 1 },
        { type: "thinking", turn: 1, text: "正在核对字段，当前是实时片段。" },
        { type: "tool_start", turn: 1, call_id: "nested-mcp", name: name("mcp_fixture"), args: { query: "示例" } },
        { type: "tool_end", turn: 1, call_id: "nested-mcp", name: name("mcp_fixture"), text: "子任务 MCP 示例", details: mcp },
        { type: "tool_start", turn: 1, call_id: "nested-skill", name: name("skill_fixture"), args: { source: "示例" } },
        { type: "tool_end", turn: 1, call_id: "nested-skill", name: name("skill_fixture"), text: "子任务 Skill 示例", details: skill },
        { type: "tool_start", turn: 1, call_id: "nested-read", name: "read", args: { path: "子任务资料.txt" } },
      ] });
    }
    function background() {
      if (current.value.turn.calls.some(call => call.callId === "bg-1")) return;
      for (const n of [1, 2]) current.value.turn.calls.push({ callId: `bg-${n}`, name: "subagent", args: { mode: "read", task: "检查后台示例" }, running: false, orphaned: false,
        result: { call_id: `bg-${n}`, text: "后台任务已受理（不是最终结果）", duration_ms: 3, details: { id: `task-${n}`, mode: "read", background: true } },
        bgTask: { task_id: `task-${n}`, mode: "read", task: "检查后台示例", status: "running", started_at: Date.now(), frames: [{ type: "turn_start", turn: 1 }, { type: "thinking", turn: 1, text: `后台示例 ${n} 正在检查` }] } });
    }
    function gate(childGate = false) {
      const call = current.value.turn.calls.find(call => call.callId === (childGate ? "bg-1" : "bash-1"));
      if (!call) { record("先添加对应示例工具/后台任务"); return; }
      const value: PendingGate = { gate_id: childGate ? "child-gate" : "parent-gate", call_id: childGate ? "nested-bash" : call.callId, tool: "bash", args: { command: "printf fixture" }, deadline: Date.now() + 600000, ...(childGate ? { origin: "task-1" } : {}) };
      if (childGate) call.childGates = [value]; else call.gate = value;
    }
    function decide(payload: { gateId: string; allow: boolean }) {
      for (const call of current.value.turn.calls) { if (call.gate?.gate_id === payload.gateId) call.gate = undefined; call.childGates = call.childGates?.filter(g => g.gate_id !== payload.gateId); }
      record(`审批 ${payload.gateId}: ${payload.allow ? "允许" : "拒绝"}`);
    }
    function finish() { void stableMutation("parent end", () => {
      current.value.turn.streaming = false; current.value.turn.ts = ts + 12000; current.value.turn.thinkingMs = 1200;
      current.value.turn.text = "本轮示例已结束。最终回答和中途文字保持可见；后台子任务单独显示状态。";
      for (const call of current.value.turn.calls) if (call.running) {
        call.running = false;
        if (call.name === "subagent") { call.liveFrames?.push({ type: "run_end", stop_reason: "done" }); call.result = { call_id: call.callId, text: "已核对两条示例资料，待补充第三条来源。", duration_ms: 3200 }; }
        else if (!call.gate) { call.result = { call_id: call.callId, text: call.liveOutput ?? "示例读取完成" }; call.liveOutput = undefined; }
      }
      current.value.busy = false;
    }); }
    const controls: Array<[string, () => void]> = [
      ["追加第二次读取", addSecond], ["追加并行读取", addParallel], ["逆序返回读取", settleReverse],
      ["追加实时思考", () => { current.value.turn.thinking += "\n新片段：继续检查资料和来源。"; }],
      ["填充长思考", () => { current.value.turn.thinking += Array.from({ length: 80 }, (_, i) => `\n第${i + 1}行：用于验证手动上滚不被新内容拉回。`).join(""); }],
      ["长流突发更新", () => { current.value.turn.thinking += Array.from({ length: 20 }, (_, i) => `\n突发${i + 1}：最新完整尾部。`).join(""); }],
      ["添加MCP与Skill", addRich], ["添加子Agent", child], ["添加后台双任务", background],
      ["子Agent继续读取", () => { void stableMutation("child singleton→group", () => {
        const call = current.value.turn.calls.find(c => c.callId === "child-1");
        if (!call?.liveFrames || call.liveFrames.some(frame => frame.call_id === "nested-read-2")) return;
        call.liveFrames.push({ type: "tool_end", turn: 1, call_id: "nested-read", name: "read", text: "子任务第一项已返回" },
          { type: "tool_start", turn: 1, call_id: "nested-read-2", name: "read", args: { path: "子任务第二项.txt" } });
      }); }],
      ["子Agent读取返回", () => { void stableMutation("child reads settle", () => {
        const call = current.value.turn.calls.find(c => c.callId === "child-1");
        call?.liveFrames?.push({ type: "tool_end", turn: 1, call_id: "nested-read-2", name: "read", text: "子任务第二项已返回" });
      }); }],
      ["父审批到达", () => gate()], ["子审批到达", () => gate(true)],
      ["完成本轮", finish],
      ["后台二号先完成", () => { const call = current.value.turn.calls.find(c => c.callId === "bg-2"); if (call?.bgTask) { call.bgTask.status = "done"; call.bgTask.duration_ms = 2100; call.taskNotice = { id: "task-2", brief: "检查后台示例", failed: false, result: "二号的独立结果：资料乙已核对。" }; } }],
      ["后台一号结束待结果", () => { const call = current.value.turn.calls.find(c => c.callId === "bg-1"); if (call?.bgTask) { call.bgTask.status = "done"; call.bgTask.duration_ms = 4200; } }],
      ["后台一号结果到达", () => { const call = current.value.turn.calls.find(c => c.callId === "bg-1"); if (call) call.taskNotice = { id: "task-1", brief: "检查后台示例", failed: false, result: "一号的独立结果：资料甲已核对。" }; }],
      ["重建快照", () => { void stableMutation("snapshot", () => { current.value.turn = JSON.parse(JSON.stringify(current.value.turn)) as TurnItem; }); }],
      ["切换会话目标", () => { requested.value = displayed.value === "fixture-A" ? "fixture-B" : "fixture-A"; }],
      ["接收目标快照", () => { displayed.value = requested.value; }],
      ["定位第一条来源", () => { if (panel.value) jumpToCite("S1", panel.value); }],
      ["浅色", () => applyTheme("paper", false)], ["深色", () => applyTheme("graphite", false)],
      ["English", () => setLocale("en")], ["中文", () => setLocale("zh-CN")],
    ];
    return () => h("main", [
      h("h1", "Pogo · 思考与活动卡片第二轮"), h("p", { class: "note" }, "隔离真实组件 · 虚构输入 · 不连接正式服务、不调用模型"),
      h("div", { class: "controls" }, controls.map(([label, onClick]) => h("button", { type: "button", onMousedown: (event: MouseEvent) => event.preventDefault(), onClick }, label))),
      h("output", { class: "status", "data-testid": "fixture-status" }, `显示 ${displayed.value} · 目标 ${requested.value} · ${current.value.busy ? "运行中" : "已结束"}`),
      h("section", { class: "panel", ref: panel, "data-testid": "lifecycle" }, [h("h2", "同一活动列表贯穿运行、结束和快照恢复"),
        h(WorkLog, { key: `${displayed.value}:u1`, exchange: exchange.value, runActive: current.value.busy, active: current.value.busy, live: current.value.busy ? live.value : undefined, runStartedAt: ts,
          onDecide: decide, onOpenFile: (file: { name: string }) => record(`附件：${file.name}`), onOpenSheet: () => record("打开完整子会话（fixture不连接服务）") })]),
      h("div", { class: "approval-fixture" }, [gates.value.length ? h(ApprovalBar, { gates: gates.value, onDecide: decide }) : null]),
      h("section", { class: "panel" }, [h("h2", "DOM、顺序与焦点证据"), h("output", { class: "evidence", "data-testid": "evidence" }, log.value.join("\n") || "等待交互")]),
    ]);
  },
});
createApp(Fixture).use(ElementPlus).use(i18n).mount("#app");
