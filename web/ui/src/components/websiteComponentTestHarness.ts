import { readFileSync } from "node:fs";
import { compile } from "@vue/compiler-dom";
import { parse } from "@vue/compiler-sfc";
import ts from "typescript";
import * as Vue from "vue";
import { createRenderer, defineComponent, h, nextTick, type App, type Component } from "vue";
import WebsiteDurationInput from "./WebsiteDurationInput.vue";

export interface HostNode {
  tag: string; text: string; props: Record<string, unknown>; children: HostNode[]; parent: HostNode | null; value: unknown; type: string;
  events: Record<string, ((event: unknown) => void)[]>;
  addEventListener(name: string, handler: (event: unknown) => void): void;
  removeEventListener(name: string, handler: (event: unknown) => void): void;
  getRootNode(): HostNode;
}
function node(tag: string, text = ""): HostNode {
  const n: HostNode = { tag, text, props: {}, children: [], parent: null, value: "", type: "", events: {},
    addEventListener(name, handler) { (n.events[name] ||= []).push(handler); },
    removeEventListener(name, handler) { n.events[name] = (n.events[name] || []).filter(h => h !== handler); },
    getRootNode() { let root = n; while (root.parent) root = root.parent; return root; },
  }; return n;
}
const renderer = createRenderer<HostNode, HostNode>({
  createElement: tag => node(tag), createText: value => node("text", value), createComment: value => node("comment", value),
  setText: (n, value) => { n.text = value; }, setElementText: (n, value) => { n.text = value; n.children = []; },
  patchProp: (n, key, _old, value) => { n.props[key] = value; if (key === "value") n.value = value; if (key === "type") n.type = String(value); }, setScopeId() {},
  insert(n, parent, anchor) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; if (i < 0) parent.children.push(n); else parent.children.splice(i, 0, n); },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null; },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
});
export function clientComponent(component: Component, file: string): Component {
  const template = parse(readFileSync(new URL(file, import.meta.url), "utf8")).descriptor.template!.content;
  const code = ts.transpileModule(compile(template, { mode: "function", prefixIdentifiers: true, expressionPlugins: ["typescript"] }).code, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText;
  const render = new Function("Vue", code)(Vue);
  const original = component as unknown as { setup(props: unknown, context: unknown): Record<string, unknown> };
  return { ...component, ssrRender: undefined, setup: (props: unknown, context: unknown) => ({ ...original.setup(props, context) }), render } as Component;
}
const InputNumber = defineComponent({ props: ["modelValue", "disabled", "min", "max", "step", "precision"], emits: ["update:modelValue"], setup: (p, { emit, attrs }) => () => h("input", { ...attrs, type: "number", value: p.modelValue, disabled: p.disabled, onInput: (e: { target: { value: string } }) => emit("update:modelValue", Number(e.target.value)) }) });
const Select = defineComponent({ props: ["modelValue", "disabled"], emits: ["update:modelValue"], setup: (p, { emit, attrs, slots }) => () => h("select", { ...attrs, value: p.modelValue, disabled: p.disabled, onChange: (e: { target: { value: string } }) => emit("update:modelValue", Number(e.target.value)) }, slots.default?.()) });
const Option = defineComponent({ props: ["value", "label"], setup: p => () => h("option", { value: p.value }, p.label) });
const Button = defineComponent({ props: ["disabled", "loading", "type"], setup: (p, { attrs, slots }) => () => h("button", { ...attrs, disabled: p.disabled, "data-loading": p.loading }, slots.default?.()) });
const Popover = defineComponent({ props: ["visible"], setup: (p, { slots }) => () => p.visible ? h("section", { "data-test-popover": true }, slots.default?.()) : null });
const ClientDuration = clientComponent(WebsiteDurationInput, "./WebsiteDurationInput.vue");
export function mountComponent(component: Component, file: string, props: Record<string, unknown> = {}): { app: App; root: HostNode; exposed(): Record<string, unknown> | null } {
  const client = clientComponent(component, file), root = node("root");
  let exposed: Record<string, unknown> | null = null;
  const app = renderer.createApp({ render: () => h(client, { ...props, ref: (value: unknown) => { exposed = value as Record<string, unknown> | null; } }) });
  app.provide(Vue.ssrContextKey, { modules: new Set() });
  app.component("el-input-number", InputNumber); app.component("el-select", Select); app.component("el-option", Option); app.component("el-button", Button); app.component("el-popover", Popover); app.component("WebsiteDurationInput", ClientDuration);
  app.mount(root); return { app, root, exposed: () => exposed };
}
export const flat = (n: HostNode): HostNode[] => [n, ...n.children.flatMap(flat)];
export const text = (n: HostNode): string => n.text + n.children.map(text).join("");
export function find(root: HostNode, predicate: (node: HostNode) => boolean): HostNode { const result = flat(root).find(predicate); if (!result) throw new Error("Expected test node missing"); return result; }
export function button(root: HostNode, label: string): HostNode { return find(root, n => n.tag === "button" && text(n) === label); }
export async function click(n: HostNode): Promise<void> { if (n.props.disabled) throw new Error("Cannot click a disabled button"); await (n.props.onClick as (() => unknown))?.(); await flush(); }
export async function input(n: HostNode, value: unknown, change = false): Promise<void> {
  if (n.props.disabled) throw new Error("Cannot edit a disabled field"); n.value = value;
  const event = { target: n }; const handler = n.props[change ? "onChange" : "onInput"] as ((event: unknown) => unknown) | undefined;
  if (handler) await handler(event); else for (const fn of n.events[change ? "change" : "input"] || []) fn(event);
  await flush();
}
export async function submit(n: HostNode): Promise<void> { await (n.props.onSubmit as (event: unknown) => unknown)({ target: n, preventDefault() {} }); await flush(); }
export async function flush(): Promise<void> { for (let i = 0; i < 20; i++) await Promise.resolve(); await nextTick(); }
export function deferred<T>() { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j; }); return { promise, resolve, reject }; }
