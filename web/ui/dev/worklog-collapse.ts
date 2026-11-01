import { computed, createApp, defineComponent, h, nextTick, onMounted, reactive, ref } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import { i18n, setLocale } from "../src/i18n";
import { applyTheme } from "../src/theme";
import { provideCardDisclosure } from "../src/agent/cardDisclosure";
import { buildExchanges } from "../src/agent/exchanges";
import type { TimelineCall, TurnItem } from "../src/agent/timeline";
import { emptyLive, type CiteSource, type Live, type PendingGate } from "../src/api/types";
import WorkLog from "../src/components/WorkLog.vue";
import ApprovalBar from "../src/components/ApprovalBar.vue";

// Entirely invented data. The fixture does not import the API client or start
// a model/tool/browser. Approval events only update this local example.
setLocale("zh-CN");
applyTheme("paper", false);
const startedAt = Date.now() - 18000;
const exchangeId = "fictional-collapse-question";
const labels = {
  complete: "完成：三段过程文字＋纯来源轮＋最终回答",
  thinking: "运行：实时思考",
  tool: "运行：命令执行中",
  "parent-gate": "运行：父工具审批",
  "child-gate": "运行：子 Agent 审批",
  "no-answer": "完成：没有任何回答文字",
} as const;
type Scene = keyof typeof labels;
const isScene = (value: string | null): value is Scene => value !== null && value in labels;
const requestedScene = new URLSearchParams(location.search).get("scene");
const initialScene: Scene = isScene(requestedScene) ? requestedScene : "complete";
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function source(label: string, title: string, callId: string): CiteSource {
  return { label, title, call_id: callId, type: "file", locator: `fiction/${label}.txt`, excerpt: `虚构资料：${title}。仅用于界面验证。`, hash: `fictional-${label}`, ts: startedAt };
}
const sources = [source("S1", "虚构材料甲 · 第一轮过程", "fiction-read-1"), source("S2", "虚构材料乙 · 第一轮过程", "fiction-read-2"),
  source("S3", "虚构检索记录 · 第二轮过程", "fiction-grep"), source("S4", "虚构纯来源轮材料 · 无回答文字", "fiction-read-4"), source("S5", "虚构补充材料 · 第三轮过程", "fiction-find")];
function settled(callId: string, name: TimelineCall["name"], args: unknown, stamp?: CiteSource): TimelineCall {
  return { callId, name, args, running: false, orphaned: false, result: { call_id: callId, text: stamp?.excerpt ?? "虚构工具已结束，没有执行真实操作。", duration_ms: 120, ...(stamp ? { source: stamp } : {}) } };
}
const command = "printf 'FICTIONAL CHECK ONLY\\n'";
function makeTurns(): TurnItem[] {
  const make = (index: number, text: string, stamps: CiteSource[], calls: TimelineCall[]): TurnItem => ({
    kind: "turn", id: `fiction-turn-${index}`, index, text, thinking: `虚构第 ${index} 轮思考：核对本轮已有材料，不补造证据。`,
    calls, sources: stamps, streaming: false, thinkingMs: 650, ts: startedAt + index * 2500,
  });
  return [
    make(1, "过程文字一（虚构）：我先读取甲、乙两份材料，随后继续检索。", sources.slice(0, 2), [settled("fiction-read-1", "read", { path: "fiction/甲.txt" }, sources[0]), settled("fiction-read-2", "read", { path: "fiction/乙.txt" }, sources[1])]),
    make(2, "过程文字二（虚构）：发现一个候选字段，还需要确认来源。", [sources[2]], [settled("fiction-grep", "grep", { pattern: "候选字段", path: "fiction" }, sources[2])]),
    make(3, "过程文字三（虚构）：补充材料已经找到，正在汇总结果。", [sources[4]], [settled("fiction-find", "find", { pattern: "*.txt", path: "fiction" }, sources[4])]),
    make(4, "", [sources[3]], [settled("fiction-read-4", "read", { path: "fiction/纯来源轮.txt" }, sources[3]), {
      callId: "fiction-child", name: "subagent", args: { mode: "read", task: "虚构子任务：核对纯来源轮材料" }, running: false, orphaned: false,
      result: { call_id: "fiction-child", text: "虚构子任务完成：材料名称已核对。", duration_ms: 900 },
      liveFrames: [{ type: "turn_start", turn: 1 }, { type: "text", turn: 1, text: "虚构子任务的局部结果。" }, { type: "run_end", stop_reason: "done" }],
    }]),
    make(5, "## 最终结果（虚构）\n\n甲与纯来源轮材料共同支持这个演示结论，核对结果为 **两项一致**。[1](#cite:S1) [2](#cite:S4)\n\n这是唯一应在总工作记录收起后保留的最终回答；下面保留它自己的引用来源。", [sources[0], sources[3]], [settled("fiction-command", "bash", { command })]),
  ];
}
function configure(turns: TurnItem[], scene: Scene) {
  const fresh = makeTurns();
  for (let index = 0; index < fresh.length; index++) Object.assign(turns[index], clone(fresh[index]));
  const last = turns[4];
  const active = scene !== "complete" && scene !== "no-answer";
  if (active) { last.text = ""; last.sources = []; last.streaming = true; last.ts = undefined; }
  if (scene === "no-answer") for (const turn of turns) turn.text = "";
  if (scene === "thinking") last.thinking = "虚构实时思考：先核对来源。\n当前片段：正在比较两项字段。\n最新片段：最终回答尚未生成。";
  if (scene === "tool" || scene === "parent-gate") {
    const call = last.calls[0]; call.running = true; call.result = undefined; call.startedAt = Date.now() - 1200;
    call.liveOutput = scene === "tool" ? "虚构命令输出：开始检查\n虚构命令输出：正在核对第二项\n" : undefined;
    if (scene === "parent-gate") call.gate = { gate_id: "fiction-parent-gate", call_id: call.callId, tool: "bash", args: { command }, deadline: Date.now() + 600000 };
  }
  if (scene === "child-gate") {
    const call = turns[3].calls[1];
    const gate: PendingGate = { gate_id: "fiction-child-gate", call_id: "fiction-child-command", tool: "bash", args: { command }, origin: "fiction-child-task", deadline: Date.now() + 600000 };
    call.result = { call_id: call.callId, text: "虚构后台任务已受理；这不是最终结果。", details: { id: "fiction-child-task", mode: "read", background: true } };
    call.liveFrames = undefined;
    call.bgTask = { task_id: "fiction-child-task", mode: "read", task: "虚构子任务：核对纯来源轮材料", status: "running", started_at: Date.now() - 800, frames: [
      { type: "turn_start", turn: 1 }, { type: "thinking", turn: 1, text: "虚构子任务正在等待审批。" }, { type: "tool_start", turn: 1, call_id: gate.call_id, name: "bash", args: gate.args },
    ] };
    call.childGates = [gate];
  }
}

type VisibilityRow = { turn: string; dom: number; prose: number; sources: number; tools: number; thinking: number };
const Fixture = defineComponent({
  setup() {
    provideCardDisclosure(() => "fictional-worklog-collapse-session");
    const scene = ref<Scene>(initialScene);
    const turns = reactive(makeTurns()); configure(turns, initialScene);
    const active = computed(() => scene.value !== "complete" && scene.value !== "no-answer");
    const exchange = computed(() => buildExchanges([{ kind: "user", id: exchangeId, text: "虚构请求：读取材料并给出最终结论。", ts: startedAt }, ...turns], active.value)[0]!);
    const gates = computed(() => turns.flatMap(turn => turn.calls.flatMap(call => [...(call.gate ? [call.gate] : []), ...(call.childGates ?? [])])));
    const live = computed<Live>(() => ({ ...emptyLive(), active: active.value, thinking: turns[4].thinking, pending_gates: gates.value,
      pending_tools: turns.flatMap(turn => turn.calls.filter(call => call.running).map(call => ({ call_id: call.callId, name: call.name, args: call.args, started_at: call.startedAt ?? startedAt }))) }));
    const panel = ref<HTMLElement>();
    const evidence = ref<string[]>([]);
    const visibleRows = ref<VisibilityRow[]>([]);
    const foldStatus = ref("等待组件挂载");
    const ids = new WeakMap<Element, number>(); let sequence = 0;
    const id = (element: Element) => { let value = ids.get(element); if (value === undefined) { value = ++sequence; ids.set(element, value); } return value; };
    const visible = (element: Element) => element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden";
    const nodes = () => [...(panel.value?.querySelectorAll<HTMLElement>(".worklog, .activity-list, .activity-turn, .activity-group, .call, .answer, .sources-panel") ?? [])];
    const structural = (element: Element) => !element.matches(".answer, .sources-panel");
    function refresh() {
      const root = panel.value;
      const toggle = root?.querySelector<HTMLElement>(".worklog > .head, .worklog > .livework");
      foldStatus.value = toggle ? `总 WorkLog：${toggle.getAttribute("aria-expanded") === "true" ? "展开" : "收起"}${toggle.hasAttribute("disabled") ? "（审批/后台保护，按钮不可收起）" : ""}` : "未找到总开关";
      visibleRows.value = [...(root?.querySelectorAll<HTMLElement>(".activity-turn") ?? [])].map(element => ({
        turn: element.dataset.mid ?? "?", dom: id(element),
        prose: [...element.querySelectorAll(".answer")].filter(visible).length,
        sources: [...element.querySelectorAll(".sources-panel")].filter(visible).length,
        tools: [...element.querySelectorAll(".call")].filter(visible).length,
        thinking: [...element.querySelectorAll(".thinking-block")].filter(visible).length,
      }));
    }
    async function record(label: string, before: HTMLElement[]) {
      await nextTick(); refresh();
      const after = nodes(), retained = before.filter(element => after.includes(element)), sharedBefore = before.filter(structural), sharedAfter = after.filter(structural);
      const stableOrder = retained.every((element, index) => index === 0 || after.indexOf(retained[index - 1]) < after.indexOf(element));
      const order = after.map(element => `#${id(element)}:${element.dataset.mid ?? element.dataset.rid ?? element.classList[0]}`).join(" → ");
      evidence.value = [...evidence.value, `${label}\n结构节点保留 ${sharedBefore.filter(element => sharedAfter.includes(element)).length}/${sharedBefore.length}；全部既有节点保留 ${retained.length}/${before.length}；共同节点相对顺序=${stableOrder}\n${foldStatus.value}\nDOM顺序 ${order}`].slice(-16);
    }
    async function mutate(label: string, action: () => void) { const before = nodes(); action(); await record(label, before); }
    function choose(next: Scene) { void mutate(`切换场景：${labels[next]}`, () => { scene.value = next; configure(turns, next); }); }
    function fold(open: boolean) {
      void mutate(open ? "点击真实总展开按钮" : "点击真实总收起按钮", () => {
        const button = panel.value?.querySelector<HTMLButtonElement>(".worklog > .head, .worklog > .livework");
        if (button && !button.disabled && (button.getAttribute("aria-expanded") === "true") !== open) button.click();
      });
    }
    function decide(payload: { gateId: string; allow: boolean }) {
      void mutate(`虚构审批 ${payload.gateId}：${payload.allow ? "允许" : "拒绝"}（仅本页数据）`, () => {
        for (const turn of turns) for (const call of turn.calls) { if (call.gate?.gate_id === payload.gateId) call.gate = undefined; call.childGates = call.childGates?.filter(gate => gate.gate_id !== payload.gateId); }
      });
    }
    onMounted(() => { void record(`初次载入：${labels[initialScene]}`, []); });
    return () => h("main", [
      h("h1", "Pogo · 总 WorkLog 收起演示"),
      h("p", { class: "fixture-note" }, "真实 WorkLog / i18n / 样式 / cardDisclosure，全部内容均为虚构数据；不连接正式服务、不调用模型或工具。共有三段过程文字、一个无文字但有来源和工具的轮次，以及最终回答。"),
      h("section", { class: "fixture-panel" }, [h("h2", "场景与总开关"),
        h("div", { class: "fixture-controls" }, (Object.keys(labels) as Scene[]).map(value => h("button", { type: "button", "aria-pressed": scene.value === value, "data-scene": value, onClick: () => choose(value) }, labels[value]))),
        h("div", { class: "fixture-controls" }, [h("button", { type: "button", onClick: () => fold(false) }, "收起总 WorkLog"), h("button", { type: "button", onClick: () => fold(true) }, "展开总 WorkLog"),
          h("button", { type: "button", onClick: () => { void mutate("追加虚构思考尾部", () => { turns[4].thinking += "\n新增实时片段：继续确认已有资料，不生成最终回答。"; }); } }, "追加思考尾部"),
          h("button", { type: "button", onClick: () => { void mutate("追加虚构命令输出", () => { const call = turns[4].calls[0]; if (call.running) call.liveOutput = (call.liveOutput ?? "") + "虚构输出：下一项核对中\n"; }); } }, "追加命令输出"),
          h("button", { type: "button", onClick: () => { void mutate("同ID快照对象重建", () => { turns.splice(0, turns.length, ...clone(turns)); }); } }, "重建相同 ID 快照"),
          h("button", { type: "button", onClick: () => { void record("手动检查", nodes()); } }, "记录当前 DOM"),
          h("a", { href: `?scene=${scene.value}` }, "重新载入此场景，验证首次默认状态"),
        ]),
        h("p", { class: "fixture-note" }, "场景按钮保留同一 WorkLog 实例与既有展开选择。审批只更新虚构数据；需要验证无回答场景的首次默认状态时使用重新载入链接。"),
        h("output", { class: "fixture-status", "data-testid": "collapse-status" }, `${labels[scene.value]}\n${foldStatus.value}\n最终文字轮：${exchange.value.answer?.id ?? "无"}；待审批：${gates.value.length}`),
      ]),
      h("section", { class: "fixture-panel fixture-expected" }, [h("h2", "本次验证目标"), "完成场景收起后：只保留第五轮的“最终结果（虚构）”及它自己的来源。过程文字一/二/三和第四轮“查阅材料”应随过程一起隐藏；展开后按原序恢复。运行时当前思考、当前操作/输出和父/子审批仍须可见。下表和日志只记录实际 DOM，不代替验收结论。"]),
      h("section", { class: "fixture-panel", ref: panel, "data-testid": "worklog-collapse", onClickCapture: () => { const before = nodes(); void record("真实组件内部交互", before); } }, [
        h(WorkLog, { key: "stable-fictional-worklog", exchange: exchange.value, runActive: active.value, active: active.value, live: active.value ? live.value : undefined, runStartedAt: startedAt,
          onDecide: decide, onOpenFile: () => { void record("虚构附件入口（没有请求文件）", nodes()); }, onOpenSheet: () => { void record("虚构子会话入口（没有连接服务）", nodes()); } }),
      ]),
      gates.value.length ? h("aside", { "data-testid": "collapse-approval-bar", style: { position: "fixed", left: "12px", right: "12px", bottom: "12px", zIndex: 1000, maxWidth: "1026px", margin: "0 auto" } }, [
        h(ApprovalBar, { gates: gates.value, onDecide: decide }),
      ]) : null,
      h("section", { class: "fixture-panel" }, [h("h2", "各轮实际可见内容与 DOM 身份"), h("table", { class: "fixture-table", "data-testid": "collapse-visibility" }, [
        h("thead", [h("tr", ["轮次 / DOM", "文字", "来源卡", "工具", "思考"].map(text => h("th", text)))]),
        h("tbody", visibleRows.value.map(row => h("tr", { key: row.turn, "data-turn": row.turn }, [h("td", `${row.turn} / #${row.dom}`), ...[row.prose, row.sources, row.tools, row.thinking].map(value => h("td", String(value)))]))),
      ])]),
      h("section", { class: "fixture-panel" }, [h("h2", "节点保留与相对顺序记录"), h("output", { class: "fixture-evidence", "data-testid": "collapse-evidence" }, evidence.value.join("\n\n") || "等待组件挂载")]),
      h("div", { class: "fixture-controls" }, [h("button", { type: "button", onClick: () => applyTheme("paper", false) }, "浅色"), h("button", { type: "button", onClick: () => applyTheme("graphite", false) }, "深色"), h("button", { type: "button", onClick: () => setLocale("en") }, "English"), h("button", { type: "button", onClick: () => setLocale("zh-CN") }, "中文")]),
    ]);
  },
});
createApp(Fixture).use(ElementPlus).use(i18n).mount("#app");
