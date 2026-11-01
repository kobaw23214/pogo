import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import * as Vue from "vue";
import { createRenderer, defineComponent, h, nextTick, onMounted, reactive, ref, type App, type Component } from "vue";
import { afterEach, describe, expect, it } from "vitest";
import { i18n, setLocale } from "@/i18n";
import { cardDisclosureKey, createCardDisclosureState } from "@/agent/cardDisclosure";
import { worklogRevealRow } from "@/agent/worklogPrefs";
import type { Exchange } from "@/agent/exchanges";
import type { TimelineCall, TurnItem } from "@/agent/timeline";
import type { CiteSource } from "@/api/types";
import ActivityList from "./ActivityList.vue";

// Compile the actual SFC template for a small client renderer. Vitest otherwise
// imports its SSR render, which cannot establish live DOM/component identity.
const source = readFileSync(new URL("./ActivityList.vue", import.meta.url), "utf8");
const template = parse(source).descriptor.template!.content;
const render = new Function("Vue", compile(template, { mode: "function" }).code)(Vue);
const original = ActivityList as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
const ClientList = { ...ActivityList, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;

interface HostNode { tag: string; text: string; props: Record<string, unknown>; style: Record<string, string>; children: HostNode[]; parent: HostNode | null; }
const host = (tag: string, text = ""): HostNode => ({ tag, text, props: {}, style: {}, children: [], parent: null });
const renderer = createRenderer<HostNode, HostNode>({
  createElement: tag => host(tag), createText: text => host("text", text), createComment: text => host("comment", text),
  setText: (node, value) => { node.text = value; }, setElementText: (node, value) => { node.text = value; node.children = []; },
  patchProp: (node, key, _previous, value) => { node.props[key] = value; }, setScopeId() {},
  insert(node, parent, anchor) {
    if (node.parent) { const index = node.parent.children.indexOf(node); if (index >= 0) node.parent.children.splice(index, 1); }
    node.parent = parent;
    const index = anchor ? parent.children.indexOf(anchor) : -1;
    if (index < 0) parent.children.push(node); else parent.children.splice(index, 0, node);
  },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null; },
  parentNode: node => node.parent,
  nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
});
const flat = (node: HostNode): HostNode[] => [node, ...node.children.flatMap(flat)];
const text = (node: HostNode): string => node.text + node.children.map(text).join("");
const find = (node: HostNode, predicate: (node: HostNode) => boolean): HostNode => { const found = flat(node).find(predicate); if (!found) throw new Error("Rendered node missing"); return found; };
const shown = (node: HostNode): boolean => node.style.display !== "none" && (!node.parent || shown(node.parent));
const click = (node: HostNode) => (node.props.onClick as () => void)();
const call = (id: string, extra: Partial<TimelineCall> = {}): TimelineCall => ({ callId: id, name: "read", args: { path: `${id}.txt` }, running: false, orphaned: false, ...extra });
const sample = (calls = [call("a")]): Exchange => {
  const turn: TurnItem = { id: "m1", kind: "turn", index: 1, thinking: "live reasoning", text: "answer stays visible", calls, streaming: true };
  return { id: "u1", notices: [], turns: [turn], answer: turn, finished: false, stats: { calls: calls.length, failed: 0, files: 0, tools: {} } };
};
const apps: App[] = [];
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); worklogRevealRow.value = ""; });

function mount(exchange = sample(), extra: Record<string, unknown> = {}, state = createCardDisclosureState(() => "session")) {
  setLocale("zh-CN");
  const props = reactive({ exchange, runActive: true, workFolded: false, revealId: "wl-body-own", ...extra });
  const mounts = new Map<string, number>();
  const app = renderer.createApp({ render: () => h(ClientList, props) }).use(i18n);
  app.provide(Vue.ssrContextKey, { modules: new Set() });
  app.provide(cardDisclosureKey, state);
  app.component("ToolCall", defineComponent({ props: ["call"], setup: props => { onMounted(() => mounts.set(props.call.callId, (mounts.get(props.call.callId) ?? 0) + 1)); return () => h("div", { class: "call", "data-call": props.call.callId, "data-cite": props.call.result?.source?.label }, props.call.callId); } }));
  app.component("ThinkingBlock", defineComponent({ props: ["id", "text", "streaming", "compact"], setup: props => () => h("div", { class: "thinking", "data-thinking": props.id, "data-compact": props.compact }, props.text) }));
  app.component("AnswerText", defineComponent({ props: ["source"], setup: props => () => h("div", { class: "answer" }, props.source) }));
  app.component("SourcesPanel", defineComponent({ props: ["sources", "text"], setup: props => () => h("div", { "data-sources": props.sources.map((source: CiteSource) => source.label).join(" ") }, props.text) }));
  app.component("SteeringMessage", defineComponent({ props: ["message"], emits: ["open-file"], setup: (props, { emit }) => () => h("button", { "data-steering": props.message.id, onClick: () => emit("open-file", { path: "note.txt", name: "note.txt" }) }, props.message.text) }));
  for (const name of ["AnswerReviewStatus", "IncomingArgs", "WorkLogGlyph", "Chevron"]) app.component(name, defineComponent({ setup: () => () => h("span") }));
  const root = host("root"); apps.push(app); app.mount(root);
  return { root, props, mounts, state };
}

describe("shared activity list", () => {
  it("folds complete intermediate turns, including source-only turns, while retaining the final answer and its sources", async () => {
    const source = (label: string) => ({ label } as CiteSource);
    const exchange = sample([call("a"), call("b")]);
    exchange.turns[0].streaming = false;
    exchange.turns[0].text = "I will open another article";
    exchange.turns[0].sources = [source("S1")];
    exchange.turns.push({ id: "m2", kind: "turn", index: 2, thinking: "", text: "", calls: [call("c")], streaming: false, sources: [source("S2")] },
      { id: "m3", kind: "turn", index: 3, thinking: "final reasoning", text: "final result [S1]", calls: [], streaming: false, sources: [source("S1"), source("S2")] });
    exchange.answer = exchange.turns[2]; exchange.finished = true;
    exchange.notices.push({ kind: "notice", id: "n1", text: "intermediate harness reminder", afterTurnId: "m1" });
    const { root, props, mounts } = mount(exchange, { runActive: false });
    const turns = ["m1", "m2", "m3"].map(id => find(root, node => node.props["data-mid"] === id));
    const originalCall = find(root, node => node.props["data-call"] === "a");
    props.workFolded = true; await nextTick();
    expect(shown(turns[0])).toBe(false); expect(shown(turns[1])).toBe(false); expect(shown(turns[2])).toBe(true);
    expect(shown(find(root, node => node.props["data-sources"] === "S1"))).toBe(false);
    expect(shown(find(root, node => node.props["data-sources"] === "S2"))).toBe(false);
    expect(shown(find(root, node => node.props["data-sources"] === "S1 S2"))).toBe(true);
    expect(shown(find(root, node => node.text === "final result [S1]"))).toBe(true);
    expect(shown(find(root, node => node.text === "intermediate harness reminder"))).toBe(false);
    props.workFolded = false; await nextTick();
    expect(shown(turns[0])).toBe(true); expect(shown(turns[1])).toBe(true);
    expect(find(root, node => node.props["data-call"] === "a")).toBe(originalCall); expect(mounts.get("a")).toBe(1);
    expect(flat(root).filter(node => node.props["data-mid"] && node.props["data-mid"] !== "n1")).toEqual(turns);
  });

  it("shows the current tool and approval without leaking its settled narration and sources through the fold", () => {
    const exchange = sample([call("run", { running: true }), call("gate", { gate: { gate_id: "g", call_id: "gate", tool: "read", deadline: 1000 } })]);
    exchange.turns[0].streaming = false;
    exchange.turns[0].sources = [{ label: "old-source" } as CiteSource];
    exchange.turns.push({ id: "m2", kind: "turn", index: 2, thinking: "new live thinking", text: "latest response", calls: [], streaming: true }); exchange.answer = exchange.turns[1];
    const { root } = mount(exchange, { workFolded: true });
    expect(shown(find(root, node => node.props["data-call"] === "run"))).toBe(true);
    expect(shown(find(root, node => node.props["data-call"] === "gate"))).toBe(true);
    expect(shown(find(root, node => node.text === "answer stays visible" && node.props.class === "answer"))).toBe(false);
    expect(shown(find(root, node => node.props["data-sources"] === "old-source"))).toBe(false);
    expect(shown(find(root, node => node.props["data-thinking"] === "m2"))).toBe(true);
  });

  it("does not mistake the latest settled narration for a final answer while the next turn is thinking", () => {
    const exchange = sample(); exchange.turns[0].streaming = false;
    exchange.turns.push({ id: "m2", kind: "turn", index: 2, thinking: "current reasoning", text: "", calls: [], streaming: true });
    const { root } = mount(exchange, { workFolded: true });
    expect(shown(find(root, node => node.props["data-mid"] === "m1"))).toBe(false);
    expect(shown(find(root, node => node.props["data-thinking"] === "m2"))).toBe(true);
  });

  it("folds settled error and incomplete cards with their process after completion", () => {
    const exchange = sample([call("failed", { result: { call_id: "failed", text: "failed", is_error: true } }), call("orphan", { orphaned: true })]);
    exchange.turns[0].streaming = false; exchange.finished = true;
    const { root } = mount(exchange, { runActive: false, workFolded: true });
    expect(shown(find(root, node => node.props["data-call"] === "failed"))).toBe(false);
    expect(shown(find(root, node => node.props["data-call"] === "orphan"))).toBe(false);
    expect(shown(find(root, node => node.props.class === "answer"))).toBe(true);
  });

  it("keeps a leaf instance and parent when one exploration call becomes a group and then finishes", async () => {
    const { root, props, mounts, state } = mount();
    const first = find(root, node => node.props["data-call"] === "a"), parent = first.parent;
    expect(shown(first)).toBe(true);
    props.exchange.turns[0].calls.push(call("b", { name: "grep", running: true })); await nextTick();
    expect(find(root, node => node.props["data-call"] === "a")).toBe(first); expect(first.parent).toBe(parent); expect(mounts.get("a")).toBe(1); expect(shown(first)).toBe(true);
    expect(state.choice("row:u1:activity:m1:a")).toBeUndefined();
    props.exchange.turns[0].calls[1].running = false; props.exchange.turns[0].streaming = false; props.exchange.finished = true; props.runActive = false; await nextTick();
    expect(find(root, node => node.props["data-call"] === "a")).toBe(first); expect(shown(first)).toBe(true); expect(mounts.get("a")).toBe(1);
    expect(state.choice("row:u1:activity:m1:a")).toBeUndefined();
  });

  it("keeps thinking, live tools, approvals, failures and answers visible when the outer work area is folded", async () => {
    const calls = [call("done"), call("run", { running: true }), call("gate", { gate: { gate_id: "g", call_id: "gate", tool: "read", deadline: 1000 } }), call("error", { result: { call_id: "error", text: "failed", is_error: true } })];
    const { root } = mount(sample(calls), { workFolded: true });
    expect(shown(find(root, node => node.props["data-call"] === "done"))).toBe(false);
    for (const id of ["run", "gate", "error"]) expect(shown(find(root, node => node.props["data-call"] === id))).toBe(true);
    expect(shown(find(root, node => node.props["data-thinking"] === "m1"))).toBe(true);
    expect(shown(find(root, node => node.props.class === "answer"))).toBe(true);
    expect(text(root)).toContain("查看资料 · 4 次调用");
    expect(text(root)).toContain("1 项执行中"); expect(text(root)).toContain("1 项待审批"); expect(text(root)).toContain("1 项失败");
  });

  it("keeps a pending child response visible under an explicit outer fold", async () => {
    const child = call("delegate", { name: "subagent", result: { call_id: "delegate", text: "ACK", details: { id: "bg1", mode: "explore", background: true } }, bgTask: { task_id: "bg1", task: "review", mode: "explore", status: "done", started_at: 1 } });
    const { root, props } = mount(sample([child]), { workFolded: true });
    const card = find(root, node => node.props["data-call"] === "delegate");
    expect(shown(card)).toBe(true);
    props.exchange.turns[0].calls[0].taskNotice = { id: "bg1", brief: "review", failed: false, result: "final response" };
    await nextTick(); expect(shown(card)).toBe(false);
  });

  it("keeps only the pending child card from an older completed turn until its final notice arrives", async () => {
    const child = call("delegate", { name: "subagent", result: { call_id: "delegate", text: "ACK", details: { id: "bg1", mode: "explore", background: true } }, bgTask: { task_id: "bg1", task: "review", mode: "explore", status: "done", started_at: 1 } });
    const exchange = sample([child]); exchange.finished = true; exchange.turns[0].streaming = false; exchange.turns[0].sources = [{ label: "old" } as CiteSource];
    exchange.turns.push({ id: "m2", kind: "turn", index: 2, text: "parent final answer", thinking: "", streaming: false, calls: [] }); exchange.answer = exchange.turns[1];
    const { root, props } = mount(exchange, { runActive: false, workFolded: true });
    const oldTurn = find(root, node => node.props["data-mid"] === "m1");
    expect(shown(oldTurn)).toBe(true); expect(shown(find(root, node => node.props["data-call"] === "delegate"))).toBe(true);
    expect(shown(find(root, node => node.props.class === "answer" && node.text === "answer stays visible"))).toBe(false);
    expect(shown(find(root, node => node.props["data-sources"] === "old"))).toBe(false);
    props.exchange.turns[0].calls[0].taskNotice = { id: "bg1", brief: "review", failed: false, result: "final child response" };
    await nextTick(); expect(shown(oldTurn)).toBe(false);
    expect(shown(find(root, node => node.props.class === "answer" && node.text === "parent final answer"))).toBe(true);
  });

  it("respects a manual close while forcing only the active leaf visible and never closes a later manual open", async () => {
    const { root, props } = mount(sample([call("a"), call("b")]));
    const first = find(root, node => node.props["data-call"] === "a"), second = find(root, node => node.props["data-call"] === "b");
    click(find(root, node => node.props.class === "activity-group-head")); await nextTick();
    expect(shown(first)).toBe(false); expect(shown(second)).toBe(false);
    props.exchange.turns[0].calls[1].running = true; await nextTick();
    expect(shown(first)).toBe(false); expect(shown(second)).toBe(true);
    props.exchange.turns[0].calls[1].running = false; await nextTick(); expect(shown(second)).toBe(false);
    click(find(root, node => node.props.class === "activity-group-head")); await nextTick();
    props.exchange.finished = true; props.runActive = false; await nextTick();
    expect(shown(first)).toBe(true); expect(shown(second)).toBe(true);
  });

  it("reveals only its own scoped group, including after an explicit close", async () => {
    const { root, state } = mount({ ...sample([call("a"), call("b")]), finished: true }, { runActive: false });
    const first = find(root, node => node.props["data-call"] === "a"); expect(shown(first)).toBe(false);
    worklogRevealRow.value = "wl-body-other:activity:m1:a"; await nextTick(); expect(shown(first)).toBe(false);
    worklogRevealRow.value = "wl-body-own:activity:m1:a"; await nextTick(); expect(shown(first)).toBe(true);
    click(find(root, node => node.props.class === "activity-group-head")); await nextTick(); expect(shown(first)).toBe(false);
    worklogRevealRow.value = "wl-body-own:a:explore"; await nextTick(); expect(shown(first)).toBe(true); expect(state.choice("work:u1")).toBe(true);
  });

  it("preserves choices per session and forwards steering attachment events", async () => {
    const sid = ref("a"), state = createCardDisclosureState(() => sid.value), opened: unknown[] = [];
    const ex = { ...sample([call("a"), call("b")]), finished: true }; ex.steering = [{ kind: "user", id: "s1", text: "follow up", afterTurnId: "m1" }];
    const { root } = mount(ex, { runActive: false, onOpenFile: (value: unknown) => opened.push(value) }, state);
    click(find(root, node => node.props.class === "activity-group-head")); await nextTick();
    expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
    sid.value = "b"; await nextTick(); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(false);
    sid.value = "a"; await nextTick(); expect(shown(find(root, node => node.props["data-call"] === "a"))).toBe(true);
    click(find(root, node => node.props["data-steering"] === "s1")); expect(opened).toEqual([{ path: "note.txt", name: "note.txt" }]); expect(text(root)).toContain("answer stays visible");
  });
});
