import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import * as Vue from "vue";
import { transpileModule, ScriptTarget } from "typescript";
import { createRenderer, defineComponent, h, nextTick, onMounted, reactive, ref, type App, type Component } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { i18n, setLocale } from "@/i18n";
import { cardDisclosureKey, createCardDisclosureState } from "@/agent/cardDisclosure";
import { activityReadingKey } from "@/agent/activityReading";
import { provideActivityExecution } from "@/agent/activityExecutionContext";
import type { ActivityExecutionSnapshot } from "@/agent/activityExecution";
import { executionDetailMode, worklogRevealRow } from "@/agent/worklogPrefs";
import type { Exchange } from "@/agent/exchanges";
import type { TimelineCall, TurnItem } from "@/agent/timeline";
import ActivityRuns from "./ActivityRuns.vue";
import ActivityWaiting from "./ActivityWaiting.vue";

function clientComponent(component: Component, file: string): Component {
  const template = parse(readFileSync(new URL(file, import.meta.url), "utf8")).descriptor.template!.content;
  const render = new Function("Vue", transpileModule(compile(template, { mode: "function", expressionPlugins: ["typescript"] }).code, { compilerOptions: { target: ScriptTarget.ES2022 } }).outputText)(Vue);
  render._rc = true;
  const original = component as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
  return { ...component, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;
}
const ClientRuns = clientComponent(ActivityRuns, "./ActivityRuns.vue");
const ClientWaiting = clientComponent(ActivityWaiting, "./ActivityWaiting.vue");
interface HostNode { tag: string; text: string; props: Record<string, unknown>; style: Record<string, string>; children: HostNode[]; parent: HostNode | null; contains(other: unknown): boolean }
function host(tag: string, text = ""): HostNode {
  const node: HostNode = { tag, text, props: {}, style: {}, children: [], parent: null, contains: other => node === other || node.children.some(child => child.contains(other)) }; return node;
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
const find = (node: HostNode, predicate: (item: HostNode) => boolean): HostNode => { const item = flat(node).find(predicate); if (!item) throw new Error("Rendered node missing"); return item; };
const hasClass = (node: HostNode, name: string) => String(node.props.class ?? "").split(/\s+/).includes(name);
const shown = (node: HostNode): boolean => node.style.display !== "none" && (!node.parent || shown(node.parent));
const click = (node: HostNode) => (node.props.onClick as () => void)();
const call = (id: string, extra: Partial<TimelineCall> = {}): TimelineCall => ({ callId: id, name: "read", args: { path: `${id}.txt` }, result: { call_id: id, text: `result ${id}` }, running: false, orphaned: false, ...extra });
const turn = (id: string, calls: TimelineCall[], extra: Partial<TurnItem> = {}): TurnItem => ({ kind: "turn", id, index: 1, thinking: "", text: "", calls, streaming: false, ...extra });
const exchange = (turns = [turn("m1", [call("a"), call("b")])], finished = false): Exchange => ({ id: "u1", notices: [], finished, turns, stats: { calls: 0, failed: 0, files: 0, tools: {} } });
const apps: App[] = [];
beforeEach(() => { vi.useFakeTimers(); setLocale("zh-CN"); executionDetailMode.value = "summary"; worklogRevealRow.value = ""; vi.stubGlobal("document", { activeElement: null }); vi.stubGlobal("window", { getSelection: () => null }); });
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); });

function mount(ex = exchange(), extra: Record<string, unknown> = {}, sid = ref("session"), evidence?: ActivityExecutionSnapshot) {
  const state = createCardDisclosureState(() => sid.value), following = ref(false), pause = vi.fn(() => { following.value = false; });
  const props = reactive({ exchange: ex, runActive: !ex.finished, workFolded: false, waitingForModel: false, revealId: "wl-body-own", ...extra });
  const mounts = new Map<string, number>();
  const execution = ref<ActivityExecutionSnapshot>(evidence ?? { callOrigins: new Map(), endReasons: new Map() });
  const app = renderer.createApp({ setup() { provideActivityExecution(execution); return () => h(ClientRuns, props); } }).use(i18n);
  app.provide(Vue.ssrContextKey, { modules: new Set() }); app.provide(cardDisclosureKey, state); app.provide(activityReadingKey, { following, pause });
  app.component("ActivityWaiting", ClientWaiting);
  app.component("ToolCall", defineComponent({ props: ["call", "incoming", "fullDetails", "reveal", "defaultExpanded", "layered"], setup: props => {
    onMounted(() => mounts.set(props.call.callId, (mounts.get(props.call.callId) ?? 0) + 1));
    return () => h("div", { class: "call", "data-call": props.call.callId, "data-incoming": !!props.incoming, "data-full": props.fullDetails, "data-reveal": props.reveal, "data-default-open": props.defaultExpanded, "data-layered": props.layered }, `result:${props.call.callId}`);
  } }));
  app.component("AnswerText", defineComponent({ props: ["source"], setup: props => () => h("div", { class: "answer" }, props.source) }));
  app.component("ThinkingBlock", defineComponent({ props: ["id", "text", "streaming"], setup: props => () => h("div", { "data-thinking": props.id }, props.text) }));
  app.component("SourcesPanel", defineComponent({ props: ["sources"], setup: props => () => h("div", { "data-sources": props.sources.map((source: { label: string }) => source.label).join(" ") }) }));
  app.component("SteeringMessage", defineComponent({ props: ["message"], setup: props => () => h("div", { "data-steering": props.message.id }, props.message.text) }));
  for (const name of ["Chevron", "AnswerReviewStatus"]) app.component(name, defineComponent({ setup: () => () => h("span") }));
  const root = host("root"); apps.push(app); app.mount(root);
  return { root, props, state, mounts, following, pause, sid, execution };
}
const leaf = (root: HostNode, id: string) => find(root, node => node.props["data-call"] === id);
const groupHead = (root: HostNode) => find(root, node => hasClass(node, "activity-group-head"));
const failedBrowser = (id: string): TimelineCall => call(id, {
  name: "browser", args: { action: "navigate", context_ref: "context", tab_id: "tab", control_epoch: 1, url: "https://example.test/alerts" },
  result: { call_id: id, is_error: true, text: "browser navigate failed: BROWSER_RESOURCE_CAPACITY: unavailable" },
});
const retryEvidence = (): ActivityExecutionSnapshot => ({ endReasons: new Map(), callOrigins: new Map(["a", "b"].map(id => [id, { sessionId: "session", exchangeId: "u1", runId: "run-1" }])) });
const flushActivity = async () => { await nextTick(); await nextTick(); await nextTick(); };

describe("mounted cross-turn activity", () => {
  it("keeps the first tool instance and parent when a later model turn extends its run", async () => {
    const { root, props, mounts } = mount(exchange([turn("m1", [call("a")])]));
    const first = leaf(root, "a"), parent = first.parent;
    props.exchange.turns.push(turn("m2", [call("b", { name: "bash", args: { command: "pwd" } })])); await nextTick();
    expect(leaf(root, "a")).toBe(first); expect(first.parent).toBe(parent); expect(mounts.get("a")).toBe(1);
    expect(flat(root).filter(node => hasClass(node, "activity-run"))).toHaveLength(1);
    expect(groupHead(root).props["aria-expanded"]).toBe(true);
  });

  it("keeps growing short findings in a fixed independent prose host", async () => {
    const { root, props } = mount(exchange([turn("m1", [call("a")]), turn("m2", [call("b"), call("c")], { text: "已找到原因：旧配置仍在生效。", streaming: true })]));
    const prose = find(root, node => hasClass(node, "activity-prose")), answer = find(prose, node => hasClass(node, "answer"));
    props.exchange.turns[1].text = "x".repeat(39); await nextTick();
    props.exchange.turns[1].text = "x".repeat(81) + "\n"; await nextTick();
    expect(find(root, node => hasClass(node, "activity-prose"))).toBe(prose);
    expect(find(prose, node => hasClass(node, "answer"))).toBe(answer);
    click(groupHead(root)); await nextTick(); expect(shown(prose)).toBe(true); expect(text(prose)).toContain("x".repeat(81));
  });

  it("keeps the same ToolCall instance from arguments through start and final result", async () => {
    const incoming = { call_id: "a", name: "write", bytes: 18, head: '{"path":"note.md"' };
    const { root, props, mounts } = mount(exchange([turn("live", [], { incoming: [incoming] })]));
    const first = leaf(root, "a"), parent = first.parent;
    expect(first.props["data-incoming"]).toBe(true);
    props.exchange.turns[0].incoming = [];
    props.exchange.turns[0].calls = [call("a", { name: "write", running: true, result: undefined })]; await nextTick();
    props.exchange.turns[0].id = "m1"; props.exchange.turns[0].calls[0].running = false;
    props.exchange.turns[0].calls[0].result = { call_id: "a", text: "saved" }; await nextTick();
    expect(leaf(root, "a")).toBe(first); expect(first.parent).toBe(parent); expect(mounts.get("a")).toBe(1);
    expect(first.props["data-incoming"]).toBe(false);
  });

  it("temporarily overrides a manual close in full mode and restores it without remounting", async () => {
    const { root, state, mounts } = mount();
    const first = leaf(root, "a"); click(groupHead(root)); await nextTick(); expect(shown(first)).toBe(false);
    expect(state.choice("row:u1:run:call:a")).toBe(false);
    executionDetailMode.value = "full"; await nextTick();
    expect(shown(first)).toBe(true); expect(first.props["data-full"]).toBe(true); expect(groupHead(root).props.disabled).toBe(true);
    click(groupHead(root)); await nextTick(); expect(shown(first)).toBe(true);
    executionDetailMode.value = "summary"; await nextTick();
    expect(shown(first)).toBe(false); expect(state.choice("row:u1:run:call:a")).toBe(false); expect(mounts.get("a")).toBe(1);
  });

  it("shows the active leaf through a manually closed group and returns to the saved choice", async () => {
    const { root, props, state } = mount(); click(groupHead(root)); await nextTick();
    props.exchange.turns[0].calls[1].running = true; await nextTick();
    expect(shown(leaf(root, "a"))).toBe(false); expect(shown(leaf(root, "b"))).toBe(true);
    props.exchange.turns[0].calls[1].running = false; await nextTick();
    expect(shown(leaf(root, "b"))).toBe(false); expect(state.choice("row:u1:run:call:a")).toBe(false);
  });

  it.each(["edit", "write"] as const)("keeps incoming %s visible through both folded layers without calling it executed", async name => {
    const incoming = { call_id: "b", name, head: '{"path":"note.md","content":"', tail: "live content", bytes: 18 };
    const { root, props, mounts } = mount(exchange([turn("live", [call("a")], { incoming: [incoming] })]));
    const live = leaf(root, "b"); click(groupHead(root)); await nextTick();
    expect(shown(live)).toBe(true); expect(text(groupHead(root))).toContain(name === "edit" ? "生成编辑内容" : "生成写入内容");
    expect(text(groupHead(root))).toContain("note.md"); expect(text(groupHead(root))).toContain("尚未执行");
    expect(text(groupHead(root))).not.toContain("正在执行");
    props.workFolded = true; await nextTick(); expect(shown(live)).toBe(true);
    executionDetailMode.value = "full"; await nextTick(); expect(shown(live)).toBe(true);
    executionDetailMode.value = "summary"; await nextTick(); expect(shown(live)).toBe(true);
    props.exchange.turns[0].incoming = []; props.exchange.turns[0].calls.push(call("b", { name })); await nextTick();
    expect(leaf(root, "b")).toBe(live); expect(mounts.get("b")).toBe(1); expect(shown(live)).toBe(false);
  });

  it("temporarily shows approvals without changing the saved group choice", async () => {
    const { root, props, state } = mount(); click(groupHead(root)); await nextTick();
    props.exchange.turns[0].calls[1].gate = { gate_id: "g", call_id: "b", tool: "read", deadline: 1000 }; await nextTick();
    expect(groupHead(root).props["aria-expanded"]).toBe(true); expect(groupHead(root).props.disabled).toBe(true);
    delete props.exchange.turns[0].calls[1].gate; await nextTick();
    expect(groupHead(root).props["aria-expanded"]).toBe(false); expect(state.choice("row:u1:run:call:a")).toBe(false);
  });

  it("reveals only its own scoped source and never writes a manual preference", async () => {
    const { root, state } = mount(exchange(undefined, true));
    expect(shown(leaf(root, "a"))).toBe(false);
    worklogRevealRow.value = "wl-body-other:a"; await nextTick(); expect(shown(leaf(root, "a"))).toBe(false);
    worklogRevealRow.value = "wl-body-own:a"; await nextTick();
    expect(shown(leaf(root, "a"))).toBe(true); expect(leaf(root, "a").props["data-reveal"]).toBe(true);
    expect(state.choice("row:u1:run:call:a")).toBeUndefined(); expect(state.choice("call:a:details")).toBeUndefined();
  });

  it("isolates the same ids across sessions and restores saved explicit choices", async () => {
    const { root, sid } = mount(exchange(undefined, true));
    click(groupHead(root)); await nextTick(); expect(shown(leaf(root, "a"))).toBe(true);
    sid.value = "another"; await nextTick(); expect(shown(leaf(root, "a"))).toBe(false);
    sid.value = "session"; await nextTick(); expect(shown(leaf(root, "a"))).toBe(true);
  });

  it("does not auto-fold a closing run while the reader is detached or inspecting a child", async () => {
    const { root, props, state, following } = mount();
    props.exchange.turns.push(turn("m2", [], { text: "Found a cause" })); await nextTick(); await nextTick();
    expect(shown(leaf(root, "a"))).toBe(true);
    state.set("call:a:details", true); following.value = true; await nextTick(); await nextTick();
    expect(shown(leaf(root, "a"))).toBe(true);
    state.set("call:a:details", false); await nextTick(); await nextTick();
    await nextTick(); // post-watch -> nextTick(maybeFold) -> visibility render
    expect(shown(leaf(root, "a"))).toBe(false);
  });

  it("preserves independent card renderers while only persistent receipts remain outside an outer fold", async () => {
    const child = call("child", { name: "subagent", result: { call_id: "child", text: "done", details: { id: "child-run", mode: "read", background: false } } });
    const answered = call("answered", { name: "request_user_input_async", result: { call_id: "answered", text: "answered", details: { status: "answered", async: true, questions: [], answers: {} } } });
    const { root, props, mounts } = mount(exchange([turn("m1", [call("a", { name: "todo" }), call("b", { name: "skill_save" }), child, answered])], true), { workFolded: false });
    const originals = ["a", "b", "child", "answered"].map(id => leaf(root, id));
    expect(shown(leaf(root, "a"))).toBe(true); expect(shown(leaf(root, "b"))).toBe(true);
    expect(leaf(root, "a").props["data-layered"]).toBe(false); expect(leaf(root, "b").props["data-layered"]).toBe(false);
    props.workFolded = true; await nextTick();
    expect(shown(leaf(root, "a"))).toBe(false); expect(shown(leaf(root, "child"))).toBe(false); expect(shown(leaf(root, "answered"))).toBe(false);
    expect(shown(leaf(root, "b"))).toBe(true);
    for (const [index, id] of ["a", "b", "child", "answered"].entries()) {
      expect(leaf(root, id)).toBe(originals[index]); expect(leaf(root, id).props["data-layered"]).toBe(false); expect(mounts.get(id)).toBe(1);
    }
  });

  it("shows waiting feedback only after the threshold and clears it when the active condition ends", async () => {
    const { root, props } = mount(exchange(), { waitingForModel: true });
    expect(flat(root).some(node => hasClass(node, "waiting-dots"))).toBe(false);
    await vi.advanceTimersByTimeAsync(1000); expect(flat(root).some(node => hasClass(node, "waiting-dots"))).toBe(true);
    await vi.advanceTimersByTimeAsync(9000); expect(text(root)).toContain("10s");
    props.waitingForModel = false; await nextTick(); expect(text(root)).not.toContain("10s"); expect(flat(root).some(node => hasClass(node, "waiting-dots"))).toBe(false);
  });

  it("keeps focused or selected activity visible until reading protection is released", async () => {
    const { root, props, following } = mount();
    const first = leaf(root, "a"); following.value = true;
    vi.stubGlobal("document", { activeElement: first.parent });
    props.exchange.turns.push(turn("m2", [], { text: "Next step" }));
    await nextTick(); await nextTick(); await nextTick();
    expect(shown(first)).toBe(true);
    vi.stubGlobal("document", { activeElement: null });
    vi.stubGlobal("window", { getSelection: () => ({ isCollapsed: false, anchorNode: first.parent, focusNode: first.parent, toString: () => "selected output" }) });
    await vi.advanceTimersByTimeAsync(1000); expect(shown(first)).toBe(true);
    vi.stubGlobal("window", { getSelection: () => null });
    await vi.advanceTimersByTimeAsync(1000); expect(shown(first)).toBe(false);
  });

  it("keeps the observed clock running across model waiting and freezes it at the boundary", async () => {
    vi.setSystemTime(10_000);
    const { root, props } = mount();
    await vi.advanceTimersByTimeAsync(5000);
    // All calls have returned; the model has not yet emitted the next boundary.
    expect(text(root)).toContain("本页观察 5.0s");
    await vi.advanceTimersByTimeAsync(7000);
    props.exchange.turns.push(turn("m2", [], { text: "Final answer" })); await nextTick();
    expect(text(root)).toContain("本页观察 12.0s");
    await vi.advanceTimersByTimeAsync(5000); expect(text(root)).toContain("本页观察 12.0s");
    const history = mount(exchange(undefined, true));
    expect(text(history.root)).not.toContain("本页观察");
  });

  it("starts a new waiting clock when switching sessions with identical live states", async () => {
    const { root, sid } = mount(exchange(), { waitingForModel: true });
    await vi.advanceTimersByTimeAsync(12_000); expect(text(root)).toContain("12s");
    sid.value = "another-session"; await nextTick();
    expect(text(root)).not.toContain("12s");
    expect(flat(root).some(node => hasClass(node, "waiting-dots"))).toBe(false);
    await vi.advanceTimersByTimeAsync(1000); expect(flat(root).some(node => hasClass(node, "waiting-dots"))).toBe(true);
  });

  it("groups proven historical failures only at a safe boundary and opens every original result in one click", async () => {
    const { root, props, following, mounts } = mount(exchange([turn("m1", [failedBrowser("a"), failedBrowser("b")])]), {}, ref("session"), retryEvidence());
    const originals = [leaf(root, "a"), leaf(root, "b")], parents = originals.map(node => node.parent);
    expect(flat(root).some(node => hasClass(node, "retry-head"))).toBe(false);
    props.exchange.turns.push(turn("m2", [], { text: "The browser needs attention" }));
    props.exchange.finished = true; props.runActive = false; await flushActivity();
    // Merely receiving the boundary must not aggregate while the reader is detached.
    expect(flat(root).some(node => hasClass(node, "retry-head"))).toBe(false);
    following.value = true; await flushActivity();
    expect(groupHead(root).props["aria-expanded"]).toBe(false);
    click(groupHead(root)); await nextTick();
    const retry = find(root, node => hasClass(node, "retry-head"));
    expect(shown(retry)).toBe(true); expect(retry.props["aria-expanded"]).toBe(false);
    expect(originals.every(node => !shown(node))).toBe(true);
    click(retry); await nextTick();
    expect(retry.props["aria-expanded"]).toBe(true);
    for (const [index, id] of ["a", "b"].entries()) {
      expect(leaf(root, id)).toBe(originals[index]); expect(leaf(root, id).parent).toBe(parents[index]);
      expect(shown(leaf(root, id))).toBe(true); expect(leaf(root, id).props["data-full"]).toBe(true); expect(mounts.get(id)).toBe(1);
    }
    expect(text(root)).toContain("发生过 2 次失败");
  });

  it("keeps identical failed calls independent when execution origin is missing", async () => {
    const { root, props, following } = mount(exchange([turn("m1", [failedBrowser("a"), failedBrowser("b")])]));
    props.exchange.turns.push(turn("m2", [], { text: "Blocked" })); props.exchange.finished = true; props.runActive = false;
    following.value = true; await flushActivity();
    expect(flat(root).some(node => hasClass(node, "retry-head"))).toBe(false);
    click(groupHead(root)); await nextTick();
    expect(shown(leaf(root, "a"))).toBe(true); expect(shown(leaf(root, "b"))).toBe(true);
    expect(text(root)).toContain("发生过 2 次失败");
  });

  it("restores retry disclosure after full mode and reveals the exact second source without another click", async () => {
    const { root, props, following, state, mounts } = mount(exchange([turn("m1", [failedBrowser("a"), failedBrowser("b")])]), {}, ref("session"), retryEvidence());
    props.exchange.turns.push(turn("m2", [], { text: "Blocked" })); props.exchange.finished = true; props.runActive = false;
    following.value = true; await flushActivity();
    click(groupHead(root)); await nextTick();
    const retry = find(root, node => hasClass(node, "retry-head"));
    expect(shown(leaf(root, "b"))).toBe(false);
    executionDetailMode.value = "full"; await nextTick();
    expect(shown(retry)).toBe(false);
    for (const id of ["a", "b"]) { expect(shown(leaf(root, id))).toBe(true); expect(leaf(root, id).props["data-full"]).toBe(true); }
    executionDetailMode.value = "summary"; await nextTick();
    expect(shown(retry)).toBe(true); expect(retry.props["aria-expanded"]).toBe(false);
    expect(shown(leaf(root, "a"))).toBe(false); expect(shown(leaf(root, "b"))).toBe(false);
    worklogRevealRow.value = "wl-body-other:b"; await nextTick(); expect(shown(leaf(root, "b"))).toBe(false);
    worklogRevealRow.value = "wl-body-own:b"; await nextTick();
    expect(retry.props["aria-expanded"]).toBe(true);
    for (const id of ["a", "b"]) { expect(shown(leaf(root, id))).toBe(true); expect(leaf(root, id).props["data-full"]).toBe(true); expect(mounts.get(id)).toBe(1); }
    expect(leaf(root, "b").props["data-reveal"]).toBe(true);
    expect(leaf(root, "a").props["data-reveal"]).toBe(false);
    expect(state.choice("call:b:details")).toBeUndefined();
    expect(text(root)).toContain("发生过 2 次失败");
  });
});
