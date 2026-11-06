import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import ts from "typescript";
import * as Vue from "vue";
import { createRenderer, defineComponent, h, nextTick, onMounted, reactive, type App, type Component } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { i18n, setLocale } from "@/i18n";
import { cardDisclosureKey, createCardDisclosureState } from "@/agent/cardDisclosure";
import type { TimelineCall } from "@/agent/timeline";
import ToolCall from "./ToolCall.vue";
import IncomingArgs from "./tools/IncomingArgs.vue";

function clientComponent(component: Component, file: string): Component {
  const template = parse(readFileSync(new URL(file, import.meta.url), "utf8")).descriptor.template!.content;
  const renderCode = ts.transpileModule(compile(template, { mode: "function", prefixIdentifiers: true, expressionPlugins: ["typescript"] }).code, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText;
  const render = new Function("Vue", renderCode)(Vue);
  const original = component as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
  return { ...component, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;
}
const ClientCall = clientComponent(ToolCall, "./ToolCall.vue");
const ClientIncoming = clientComponent(IncomingArgs, "./tools/IncomingArgs.vue");
interface Node { tag: string; text: string; props: Record<string, unknown>; style: Record<string, string>; children: Node[]; parent: Node | null; contains(other: unknown): boolean; focus(): void; }
const node = (tag: string, text = ""): Node => {
  const n: Node = { tag, text, props: {}, style: {}, children: [], parent: null, contains: other => n === other || n.children.some(child => child.contains(other)), focus: vi.fn() };
  return n;
};
const renderer = createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: text => node("text", text), createComment: text => node("comment", text),
  setText: (n, value) => { n.text = value; }, setElementText: (n, value) => { n.text = value; n.children = []; },
  patchProp: (n, key, _old, value) => { n.props[key] = value; }, setScopeId() {},
  insert(n, parent, anchor) {
    if (n.parent) { const i = n.parent.children.indexOf(n); if (i >= 0) n.parent.children.splice(i, 1); }
    n.parent = parent;
    const i = anchor ? parent.children.indexOf(anchor) : -1;
    if (i < 0) parent.children.push(n); else parent.children.splice(i, 0, n);
  },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null; },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
});
const flat = (n: Node): Node[] => [n, ...n.children.flatMap(flat)];
const text = (n: Node): string => n.text + n.children.map(text).join("");
const find = (root: Node, predicate: (n: Node) => boolean) => {
  const result = flat(root).find(predicate); if (!result) throw new Error("Expected node missing"); return result;
};
const shown = (n: Node): boolean => n.style.display !== "none" && (!n.parent || shown(n.parent));
const hasClass = (n: Node, name: string) => String(n.props.class ?? "").split(/\s+/).includes(name);
const call = (extra: Partial<TimelineCall> = {}): TimelineCall => ({ callId: "read-1", name: "read", args: { path: "note.txt" }, running: false, orphaned: false, result: { call_id: "read-1", text: "original result" }, ...extra });
const apps: App[] = [];
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); vi.useRealTimers(); vi.unstubAllGlobals(); });

function mount(extra: Record<string, unknown> = {}, tool = call()) {
  setLocale("zh-CN");
  const props = reactive({ call: tool, runActive: false, layered: true, fullDetails: false, reveal: false, defaultExpanded: false, ...extra });
  const state = createCardDisclosureState(() => "session-a");
  let resultMounts = 0;
  const app = renderer.createApp({ render: () => h(ClientCall, props) }).use(i18n);
  app.provide(Vue.ssrContextKey, { modules: new Set() }); app.provide(cardDisclosureKey, state);
  app.component("ToolCallHeader", defineComponent({ props: ["presentation", "expandable", "open", "controls"], emits: ["toggle", "open-file"], setup: (p, { emit }) => () => h("button", { class: "tool-header", "aria-expanded": p.open, disabled: !p.expandable, onClick: () => emit("toggle") }, p.presentation.name) }));
  app.component("CodeBlock", defineComponent({ props: ["code"], setup: p => { onMounted(() => resultMounts++); return () => h("pre", { class: "test-result" }, p.code); } }));
  app.component("AnsiText", defineComponent({ props: ["text"], setup: p => () => h("pre", { class: "test-live" }, p.text) }));
  app.component("IncomingArgsView", ClientIncoming);
  app.component("GateCard", defineComponent({ props: ["gate"], setup: p => () => h("div", { class: "test-gate" }, p.gate.gate_id) }));
  for (const name of ["SubagentResult", "UserInputResult", "AgentWorkflowResult", "McpResult", "SkillResult", "BashResult", "SkillSavedResult", "ShortcutResult", "ShortcutMutationResult", "ReadResult", "ReadManyResult", "LsResult", "SearchResult", "EditResult", "WriteResult", "AnswerText", "TodoResult"]) {
    app.component(name, defineComponent({ setup: () => () => h("span") }));
  }
  const root = node("root"); apps.push(app); app.mount(root);
  const toggle = () => (find(root, n => hasClass(n, "tool-header")).props.onClick as () => void)();
  return { root, props, state, toggle, mounts: () => resultMounts };
}

describe("layered tool disclosure", () => {
  it("keeps the legacy result visible unless the containing view opts in", () => {
    const { root, mounts } = mount({ layered: false });
    expect(text(root)).toContain("original result"); expect(mounts()).toBe(1);
    expect(find(root, n => hasClass(n, "tool-header")).props.disabled).toBe(true);
  });

  it("mounts the original result on the first click and retains its node and parent through folds", async () => {
    const { root, toggle, state, mounts } = mount();
    expect(mounts()).toBe(0); expect(text(root)).not.toContain("original result");
    toggle(); await nextTick();
    const result = find(root, n => hasClass(n, "test-result")), parent = result.parent;
    expect(shown(result)).toBe(true); expect(state.choice("call:read-1:details")).toBe(true);
    toggle(); await nextTick(); expect(shown(result)).toBe(false);
    toggle(); await nextTick();
    expect(find(root, n => hasClass(n, "test-result"))).toBe(result); expect(result.parent).toBe(parent); expect(mounts()).toBe(1);
  });

  it("temporarily reveals full mode, approvals and explicit locating without erasing a manual close", async () => {
    const { root, props, state, toggle } = mount(); state.set("call:read-1:details", false);
    props.fullDetails = true; await nextTick();
    const result = find(root, n => hasClass(n, "test-result")); expect(shown(result)).toBe(true);
    toggle(); expect(state.choice("call:read-1:details")).toBe(false);
    props.fullDetails = false; await nextTick(); expect(shown(result)).toBe(false);
    props.call.gate = { gate_id: "approval", call_id: "read-1", tool: "read", deadline: 60000 };
    await nextTick(); expect(shown(find(root, n => hasClass(n, "test-gate")))).toBe(true);
    delete props.call.gate; await nextTick();
    const restored = find(root, n => hasClass(n, "test-result")); expect(shown(restored)).toBe(false);
    props.reveal = true; await nextTick(); expect(shown(restored)).toBe(true);
    props.reveal = false; await nextTick(); expect(shown(restored)).toBe(false);
    expect(state.choice("call:read-1:details")).toBe(false);
  });

  it("uses a default-open failure only until the user chooses otherwise", async () => {
    const tool = call({ result: { call_id: "read-1", text: "a precise failure", is_error: true } });
    const { root, props, toggle, state } = mount({ defaultExpanded: true }, tool);
    const output = find(root, n => hasClass(n, "error-text")); expect(shown(output)).toBe(true);
    toggle(); await nextTick(); expect(shown(output)).toBe(false);
    expect(shown(find(root, n => hasClass(n, "error-summary")))).toBe(true);
    props.fullDetails = true; await nextTick(); props.fullDetails = false; await nextTick();
    expect(shown(output)).toBe(false); expect(state.choice("call:read-1:details")).toBe(false);
  });

  it.each([
    { name: "browser" as const, args: { action: "navigate", url: "https://example.test" }, details: { action: "navigate", ok: false }, output: "Browser could not open the page" },
    { name: "bash" as const, args: { command: "exit 2" }, details: { command: "exit 2", exit_code: 2 }, output: "Command failed with exit code 2" },
  ])("shows the $name failure summary when only structured details record failure", async fixture => {
    const { root, toggle } = mount({}, call({ name: fixture.name, args: fixture.args,
      result: { call_id: "read-1", text: fixture.output, details: fixture.details } }));
    const summary = find(root, n => hasClass(n, "error-summary"));
    expect(shown(summary)).toBe(true); expect(text(summary)).toBe(fixture.output);
    expect(flat(root).some(n => hasClass(n, "error-text"))).toBe(false);
    toggle(); await nextTick();
    expect(shown(find(root, n => hasClass(n, "error-text")))).toBe(true);
    expect(text(find(root, n => hasClass(n, "error-text")))).toBe(fixture.output);
  });

  it("lets an explicit row close consume a locate request that is still held by its parent", async () => {
    const { root, props, state, toggle } = mount({ reveal: true });
    const result = find(root, n => hasClass(n, "test-result"));
    expect(shown(result)).toBe(true); expect(state.choice("call:read-1:details")).toBeUndefined();
    toggle(); await nextTick(); expect(shown(result)).toBe(false);
    expect(props.reveal).toBe(true); expect(state.choice("call:read-1:details")).toBe(false);
    props.reveal = false; await nextTick(); props.reveal = true; await nextTick();
    expect(shown(result)).toBe(true); expect(state.choice("call:read-1:details")).toBe(false);
  });

  it("keeps a live output tail visible through disclosure and the same details host on completion", async () => {
    const lines = Array.from({ length: 12 }, (_, i) => `line ${i}`).join("\n");
    const { root, props, toggle } = mount({ runActive: true }, call({ running: true, result: undefined, liveOutput: lines }));
    const live = find(root, n => hasClass(n, "test-live"));
    expect(shown(live)).toBe(true); expect(text(live)).toContain("line 11"); expect(text(live)).not.toContain("line 0");
    toggle(); await nextTick(); expect(text(live)).toContain("line 0");
    const details = find(root, n => hasClass(n, "call-details"));
    props.call.running = false; props.call.liveOutput = undefined; props.call.result = { call_id: "read-1", text: "settled output" };
    await nextTick(); expect(find(root, n => hasClass(n, "call-details"))).toBe(details);
    expect(shown(find(root, n => hasClass(n, "test-result")))).toBe(true); expect(text(root)).toContain("settled output");
  });

  it("distinguishes running without output from an empty completed result", async () => {
    const { root, props, toggle } = mount({ runActive: true }, call({ running: true, result: undefined }));
    expect(text(root)).toContain("执行中 · 尚无输出"); expect(text(root)).not.toContain("等待模型");
    props.call.running = false; props.call.result = { call_id: "read-1", text: "" }; await nextTick(); toggle(); await nextTick();
    expect(text(root)).toContain("没有输出"); expect(text(root)).not.toContain("执行中 · 尚无输出");
  });

  it.each([false, true])("preserves selected live text on completion, including cross-block selections (%s)", async crossBlock => {
    const { root, props } = mount({ runActive: true, fullDetails: true }, call({ running: true, result: undefined, liveOutput: "a line being selected" }));
    const live = find(root, n => hasClass(n, "test-live"));
    let selecting = true;
    vi.stubGlobal("window", { getSelection: () => ({ toString: () => selecting ? "being selected" : "", anchorNode: crossBlock ? node("outside-prose") : live, focusNode: live, removeAllRanges: () => { selecting = false; } }) });
    props.call.liveOutput += "\nnew data"; await nextTick();
    expect(text(live)).toBe("a line being selected");
    props.call.running = false; props.call.result = { call_id: "read-1", text: "complete result" }; props.call.liveOutput = undefined; await nextTick();
    expect(find(root, n => hasClass(n, "test-live"))).toBe(live); expect(shown(live)).toBe(true);
    expect(text(root)).toContain("保留当前阅读位置");
    expect(shown(find(root, n => hasClass(n, "test-result")))).toBe(false);
    const action = find(root, n => n.tag === "button" && text(n) === "查看结果"); (action.props.onClick as () => void)(); await nextTick();
    expect(shown(live)).toBe(false); expect(shown(find(root, n => hasClass(n, "test-result")))).toBe(true);
  });

  it("retains the logical call host and preview choice across incoming arguments and tool execution", async () => {
    const { root, props, state } = mount({ incoming: { call_id: "read-1", name: "write", bytes: 5 } }, call({ name: "write", result: undefined }));
    const host = find(root, n => n.props["data-call-id"] === "read-1");
    expect(text(root)).toContain("write");
    state.set("call:read-1:details", true);
    Object.assign(props, { incoming: undefined }); props.call.running = true; await nextTick();
    expect(find(root, n => n.props["data-call-id"] === "read-1")).toBe(host);
    props.call.running = false; props.call.result = { call_id: "read-1", text: "write receipt" }; await nextTick();
    expect(shown(find(root, n => hasClass(n, "test-result")))).toBe(true); expect(state.choice("call:read-1:details")).toBe(true);
  });

  it.each(["edit", "write"] as const)("keeps %s live counts and code updating while summary details are closed", async name => {
    vi.useFakeTimers();
    const incoming = reactive({ call_id: "read-1", name: "", head: "", tail: "", bytes: 0, lines: 0, preview_added: 0, preview_removed: 0 });
    const { root, props, state, toggle, mounts } = mount({ incoming, runActive: true }, call({ name, result: undefined }));
    const host = find(root, n => n.props["data-call-id"] === "read-1");
    state.set("call:read-1:details", false);
    Object.assign(incoming, { name, head: '{"path":"note.txt","content":"', tail: "first code fragment", bytes: 64, lines: 2, preview_added: 2, preview_removed: 1 });
    await nextTick(); await vi.advanceTimersByTimeAsync(1000);
    const preview = find(root, n => hasClass(n, "preview"));
    expect(shown(preview)).toBe(true); expect(text(preview)).toContain("first code fragment");
    expect(text(find(root, n => hasClass(n, "added")))).toBe("+2");
    if (name === "edit") expect(text(find(root, n => hasClass(n, "removed")))).toBe("-1");
    expect(text(root)).toContain("尚未写入");

    Object.assign(incoming, { tail: "next code fragment", bytes: 128, lines: 7, preview_added: 7, preview_removed: 3 });
    await nextTick(); await vi.advanceTimersByTimeAsync(1000);
    expect(find(root, n => hasClass(n, "preview"))).toBe(preview);
    expect(shown(preview)).toBe(true); expect(text(preview)).toContain("next code fragment");
    expect(text(find(root, n => hasClass(n, "added")))).toBe("+7");
    if (name === "edit") expect(text(find(root, n => hasClass(n, "removed")))).toBe("-3");
    expect(state.choice("call:read-1:details")).toBe(false);

    Object.assign(props, { incoming: undefined }); props.call.running = true; await nextTick();
    props.call.running = false; props.call.result = { call_id: "read-1", text: "final file receipt" }; await nextTick();
    expect(find(root, n => n.props["data-call-id"] === "read-1")).toBe(host);
    expect(flat(root).some(n => hasClass(n, "preview"))).toBe(false); expect(mounts()).toBe(0);
    toggle(); await nextTick(); expect(shown(find(root, n => hasClass(n, "test-result")))).toBe(true);
  });
});
