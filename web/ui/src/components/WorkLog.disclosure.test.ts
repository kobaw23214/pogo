import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import * as Vue from "vue";
import { transpileModule, ScriptTarget } from "typescript";
import { createRenderer, defineComponent, h, nextTick, onMounted, reactive, ref, type App, type Component } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Exchange } from "@/agent/exchanges";
import type { TimelineCall, TurnItem } from "@/agent/timeline";
import { emptyLive } from "@/api/types";
import { cardDisclosureKey, createCardDisclosureState } from "@/agent/cardDisclosure";
import { worklogExpandAll, worklogRevealId, worklogRevealRow, executionDetailMode } from "@/agent/worklogPrefs";
import { i18n, setLocale } from "@/i18n";
import WorkLog from "./WorkLog.vue";
import ActivityList from "./ActivityList.vue";
import ActivityRuns from "./ActivityRuns.vue";
import ActivityWaiting from "./ActivityWaiting.vue";

// Use the actual client templates so finishing a run exercises a mounted host,
// rather than comparing two independent SSR snapshots.
function clientComponent(component: Component, file: string): Component {
  const template = parse(readFileSync(new URL(file, import.meta.url), "utf8")).descriptor.template!.content;
  const render = new Function("Vue", transpileModule(compile(template, { mode: "function", expressionPlugins: ["typescript"] }).code, { compilerOptions: { target: ScriptTarget.ES2022 } }).outputText)(Vue);
  render._rc = true;
  const original = component as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
  return { ...component, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;
}
const ClientWorkLog = clientComponent(WorkLog, "./WorkLog.vue");
const ClientActivity = clientComponent(ActivityList, "./ActivityList.vue");
const ClientRuns = clientComponent(ActivityRuns, "./ActivityRuns.vue");
const ClientWaiting = clientComponent(ActivityWaiting, "./ActivityWaiting.vue");
interface HostNode {
  tag: string; text: string; props: Record<string, unknown>; style: Record<string, string>;
  children: HostNode[]; parent: HostNode | null; contains(other: unknown): boolean; focus(): void;
}
function host(tag: string, text = ""): HostNode {
  const node: HostNode = { tag, text, props: {}, style: {}, children: [], parent: null,
    contains: other => node === other || node.children.some(child => child.contains(other)), focus: vi.fn() };
  return node;
}
const renderer = createRenderer<HostNode, HostNode>({
  createElement: tag => host(tag), createText: text => host("text", text), createComment: text => host("comment", text),
  setText: (node, value) => { node.text = value; }, setElementText: (node, value) => { node.text = value; node.children = []; },
  patchProp: (node, key, _previous, value) => { node.props[key] = value; }, setScopeId() {},
  insert(node, parent, anchor) {
    if (node.parent) { const index = node.parent.children.indexOf(node); if (index >= 0) node.parent.children.splice(index, 1); }
    node.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1;
    if (index < 0) parent.children.push(node); else parent.children.splice(index, 0, node);
  },
  remove(node) { if (node.parent) { const index = node.parent.children.indexOf(node); if (index >= 0) node.parent.children.splice(index, 1); } node.parent = null; },
  parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
});
const flat = (node: HostNode): HostNode[] => [node, ...node.children.flatMap(flat)];
const text = (node: HostNode): string => node.text + node.children.map(text).join("");
function find(node: HostNode, predicate: (item: HostNode) => boolean): HostNode { const item = flat(node).find(predicate); if (!item) throw new Error("Rendered node missing"); return item; }
const hasClass = (node: HostNode, name: string) => String(node.props.class ?? "").split(/\s+/).includes(name);
const shown = (node: HostNode): boolean => node.style.display !== "none" && (!node.parent || shown(node.parent));
const click = (node: HostNode) => (node.props.onClick as () => void)();
const call = (id: string, extra: Partial<TimelineCall> = {}): TimelineCall => ({ callId: id, name: "read", args: { path: `${id}.txt` }, result: { call_id: id, text: `result ${id}` }, running: false, orphaned: false, ...extra });
const sample = (finished = true, calls = [call("a"), call("b")]): Exchange => {
  const turn: TurnItem = { kind: "turn", id: "m1", index: 1, thinking: "all model thinking", text: "answer stays readable", streaming: !finished, calls };
  return { id: "u1", notices: [], finished, startedAt: 1000, endedAt: finished ? 2000 : undefined, answer: turn,
    stats: { calls: calls.length, failed: 0, files: 0, tools: { read: calls.length } }, turns: [turn] };
};
const apps: App[] = [];
beforeEach(() => { vi.useFakeTimers(); setLocale("zh-CN"); worklogExpandAll.value = false; executionDetailMode.value = "summary"; worklogRevealId.value = ""; worklogRevealRow.value = ""; vi.stubGlobal("document", { activeElement: null }); });
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });
function mount(exchange = sample(), extra: Record<string, unknown> = {}, state = createCardDisclosureState(() => "session-a")) {
  const props = reactive({ exchange, runActive: !exchange.finished, active: !exchange.finished, live: emptyLive(), ...extra });
  const mounts = new Map<string, number>();
  const app = renderer.createApp({ render: () => h(ClientWorkLog, props) }).use(i18n);
  app.provide(Vue.ssrContextKey, { modules: new Set() }); app.provide(cardDisclosureKey, state);
  app.component("ActivityList", ClientActivity);
  app.component("ActivityRuns", ClientRuns);
  app.component("ActivityWaiting", ClientWaiting);
  app.component("ToolCall", defineComponent({ props: ["call"], setup: props => {
    onMounted(() => mounts.set(props.call.callId, (mounts.get(props.call.callId) ?? 0) + 1));
    return () => h("div", { class: "call", "data-call": props.call.callId, "data-gate": props.call.gate?.gate_id }, `payload:${props.call.callId}`);
  } }));
  app.component("ThinkingBlock", defineComponent({ props: ["id", "text", "compact"], setup: props => () => h("div", { "data-thinking": props.id, "data-compact": props.compact }, props.compact ? "recent thinking" : props.text) }));
  app.component("AnswerText", defineComponent({ props: ["source"], setup: props => () => h("div", { class: "answer" }, props.source) }));
  app.component("SourcesPanel", defineComponent({ props: ["sources"], setup: props => () => h("div", { "data-sources": props.sources.map((source: { label: string }) => source.label).join(" ") }) }));
  app.component("LiveWorkHeader", defineComponent({ props: ["folded"], emits: ["toggle"], setup: (props, { emit }) => () => h("button", { class: "live-work-head", "aria-expanded": !props.folded, onClick: () => emit("toggle") }, "live work") }));
  app.component("SteeringMessage", defineComponent({ props: ["message"], setup: props => () => h("div", { "data-steering": props.message.id }, props.message.text) }));
  for (const name of ["Chevron", "WorkLogGlyph", "AnswerReviewStatus", "IncomingArgs"]) app.component(name, defineComponent({ setup: () => () => h("span") }));
  const root = host("root"); apps.push(app); app.mount(root);
  const work = () => find(root, node => hasClass(node, "worklog"));
  const toggle = () => click(find(root, node => node.tag === "button" && (hasClass(node, "head") || hasClass(node, "live-work-head"))));
  return { root, work, props, mounts, state, toggle };
}

describe("mounted work log disclosure continuity", () => {
  it("keeps the same mounted live host and opened process after completion without a delayed fold", async () => {
    const { root, work, props, mounts, state } = mount(sample(false));
    const originalWork = work(), originalCall = find(root, node => node.props["data-call"] === "a"), originalParent = originalCall.parent;
    expect(hasClass(work(), "open")).toBe(true); expect(shown(originalCall)).toBe(true);
    state.set("m1:thinking", true);
    props.exchange.finished = true; props.exchange.endedAt = 5000; props.exchange.turns[0].streaming = false; props.runActive = false; props.active = false;
    await nextTick(); await vi.advanceTimersByTimeAsync(6000);
    expect(work()).toBe(originalWork); expect(find(root, node => node.props["data-call"] === "a")).toBe(originalCall); expect(originalCall.parent).toBe(originalParent);
    expect(mounts.get("a")).toBe(1); expect(hasClass(work(), "open")).toBe(true); expect(shown(originalCall)).toBe(true); expect(state.choice("m1:thinking")).toBe(true);
  });
  it("starts untouched history folded but keeps just-finished compatible callers open without timers", async () => {
    const history = mount(); expect(hasClass(history.work(), "open")).toBe(false);
    const justFinished = mount(sample(), { animateFold: true }); await vi.advanceTimersByTimeAsync(6000);
    expect(hasClass(justFinished.work(), "open")).toBe(true); expect(justFinished.state.choice("work:u1")).toBeUndefined();
  });
  it("keeps successful tool output visible in interrupted history that never produced an answer", () => {
    const exchange = sample(true, [call("a")]); exchange.answer = undefined; exchange.turns[0].text = "";
    const { root, work, state } = mount(exchange);
    expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
    expect(state.choice("work:u1")).toBeUndefined();
  });
  it("keeps failure and incomplete counts on the summary while their completed cards are folded", () => {
    const { root, work } = mount(sample(true, [call("failed", { result: { call_id: "failed", text: "failed", is_error: true } }), call("orphan", { orphaned: true })]));
    expect(hasClass(work(), "open")).toBe(false);
    expect(text(find(root, node => hasClass(node, "head")))).toContain("1 失败");
    expect(text(find(root, node => hasClass(node, "head")))).toContain("1 项未完成");
    for (const id of ["failed", "orphan"]) expect(shown(find(root, node => node.props["data-call"] === id))).toBe(false);
  });
  it("keeps an explicitly closed outer choice through completion, and temporarily exposes gates and background work", async () => {
    const { root, work, props, state, toggle } = mount(sample(false)); toggle(); await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    props.exchange.turns[0].calls[0].gate = { gate_id: "g1", call_id: "a", tool: "read", deadline: 60000 };
    await nextTick(); expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-gate"] === "g1"))).toBe(true);
    toggle(); expect(state.choice("work:u1")).toBe(false);
    delete props.exchange.turns[0].calls[0].gate;
    await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    props.exchange.turns[0].calls[0].childGates = [{ gate_id: "child-gate", call_id: "child-call", tool: "read", deadline: 60000 }];
    await nextTick(); expect(hasClass(work(), "open")).toBe(true); expect(state.choice("work:u1")).toBe(false);
    delete props.exchange.turns[0].calls[0].childGates;
    await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    props.exchange.turns[0].calls[0].bgTask = { task_id: "bg1", mode: "read", task: "continue", status: "running", started_at: 1000 };
    await nextTick(); expect(hasClass(work(), "open")).toBe(true);
    props.exchange.finished = true; props.exchange.turns[0].streaming = false; props.runActive = false; props.active = false; props.exchange.turns[0].calls[0].bgTask!.status = "done";
    await nextTick(); expect(hasClass(work(), "open")).toBe(false); expect(state.choice("work:u1")).toBe(false);
  });
  it("reveals a containing group for a saved detail without inventing another outer choice", () => {
    const state = createCardDisclosureState(() => "session-a"); state.set("call:a:details", true);
    const { root, work } = mount(sample(), {}, state);
    expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
    expect(state.choice("work:u1")).toBeUndefined();
  });
  it("keeps group choices while rows grow and the live host becomes history", async () => {
    const { root, work, props, state } = mount(sample(false));
    click(find(root, node => hasClass(node, "activity-group-head"))); await nextTick();
    expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(false);
    props.exchange.turns[0].calls.push(call("c")); await nextTick();
    props.exchange.finished = true; props.exchange.turns[0].streaming = false; props.runActive = false; props.active = false; await nextTick();
    expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(false);
    expect(state.choice("row:u1:run:call:a")).toBe(false);
  });
  it("hides intermediate narration with the process while keeping the final answer and user steering readable", async () => {
    const exchange = sample(); exchange.turns[0].text = "first narration";
    exchange.turns.push({ kind: "turn", id: "m2", index: 2, thinking: "more thinking", text: "final answer", streaming: false, calls: [] }); exchange.answer = exchange.turns[1];
    exchange.steering = [{ kind: "user", id: "follow", text: "user follow-up", afterTurnId: "m1" }];
    const { root, work, toggle } = mount(exchange); expect(hasClass(work(), "open")).toBe(false);
    expect(shown(find(root, node => node.text === "first narration"))).toBe(false);
    for (const expected of ["final answer", "user follow-up"]) expect(shown(find(root, node => node.text === expected))).toBe(true);
    toggle(); await nextTick(); toggle(); await nextTick();
    expect(hasClass(work(), "open")).toBe(false); expect(shown(find(root, node => node.text === "final answer"))).toBe(true); expect(shown(find(root, node => node.text === "first narration"))).toBe(false);
  });
  it("applies the global fold to intermediate prose and sources while keeping the final evidence visible", async () => {
    worklogExpandAll.value = true;
    const exchange = sample(); exchange.turns[0].sources = [{ label: "S1" } as NonNullable<TurnItem["sources"]>[number]];
    exchange.turns.push({ kind: "turn", id: "m2", index: 2, thinking: "", text: "final answer", streaming: false, calls: [], sources: [{ label: "S1" }, { label: "S2" }] as TurnItem["sources"] }); exchange.answer = exchange.turns[1];
    const { root, work } = mount(exchange);
    const oldSources = find(root, node => node.props["data-sources"] === "S1"), finalSources = find(root, node => node.props["data-sources"] === "S1 S2");
    expect(shown(oldSources)).toBe(true);
    worklogExpandAll.value = false; await nextTick();
    expect(hasClass(work(), "open")).toBe(false); expect(shown(oldSources)).toBe(false); expect(shown(finalSources)).toBe(true);
    worklogExpandAll.value = true; await nextTick();
    expect(shown(oldSources)).toBe(true); expect(find(root, node => node.props["data-sources"] === "S1")).toBe(oldSources);
  });
  it("isolates identical exchange and call ids by the displayed session and restores choices on return", async () => {
    const sid = ref("session-a"), state = createCardDisclosureState(() => sid.value);
    const { root, work, toggle } = mount(sample(), {}, state); toggle(); await nextTick(); expect(hasClass(work(), "open")).toBe(true);
    sid.value = "session-b"; await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    toggle(); await nextTick(); toggle(); await nextTick(); expect(state.choice("work:u1")).toBe(false);
    sid.value = "session-a"; await nextTick(); expect(hasClass(work(), "open")).toBe(true);
    expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(false); // the historical inner group remains its own choice
  });
  it("ignores another pane's scoped reveal and opens its own target after an explicit close", async () => {
    const { root, work, toggle, state } = mount(); toggle(); await nextTick(); toggle(); await nextTick();
    const bodyId = String(work().props["data-reveal-id"]);
    worklogRevealRow.value = "wl-body-other:activity:m1:a"; await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    worklogRevealRow.value = `${bodyId}:activity:m1:a`; await nextTick(); expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
    expect(state.choice("row:u1:run:call:a")).toBeUndefined(); // reveal is temporary, not a saved manual choice
    toggle(); await nextTick(); expect(hasClass(work(), "open")).toBe(false);
    worklogRevealRow.value = `${bodyId}:a:explore`; await nextTick(); expect(hasClass(work(), "open")).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
  });
  it("shows known abnormal reasons but does not invent a cause for unknown history", () => {
    const unknown = mount(sample());
    expect(hasClass(unknown.work(), "open")).toBe(false);
    expect(flat(unknown.root).some(node => hasClass(node, "end-reason"))).toBe(false);
    const known = mount(sample(), { endReason: "transport_error" });
    expect(hasClass(known.work(), "open")).toBe(true);
    expect(text(known.root)).toContain("模型连接中断");
    const unfamiliar = mount(sample(), { endReason: "not-a-recorded-reason" });
    expect(flat(unfamiliar.root).some(node => hasClass(node, "end-reason"))).toBe(false);
    expect(hasClass(unfamiliar.work(), "open")).toBe(false);
  });
  it("does not force an unrelated exchange open for a different pending question", () => {
    const live = { ...emptyLive(), pending_user_inputs: [{ request_id: "q", call_id: "another-call", questions: [], deadline: 0 }] };
    const other = mount(sample(), { live });
    expect(hasClass(other.work(), "open")).toBe(false);
    const pending = call("ask", { name: "request_user_input_async", result: { call_id: "ask", text: "question sent", details: { status: "pending", async: true, questions: [] } } });
    const current = mount(sample(true, [pending]));
    expect(hasClass(current.work(), "open")).toBe(true);
  });
  it("keeps full mode open without erasing the reader's saved outer choice", async () => {
    const { work, state, toggle } = mount(sample(false));
    toggle(); await nextTick(); expect(state.choice("work:u1")).toBe(false);
    executionDetailMode.value = "full"; await nextTick(); expect(hasClass(work(), "open")).toBe(true);
    toggle(); await nextTick(); expect(hasClass(work(), "open")).toBe(true); expect(state.choice("work:u1")).toBe(false);
    executionDetailMode.value = "summary"; await nextTick(); expect(hasClass(work(), "open")).toBe(false);
  });
});
