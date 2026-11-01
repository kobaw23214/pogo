import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, h, nextTick, reactive, ssrContextKey, type App, type Component, type Ref } from "vue";
import type { WorkflowApprovalPolicy, WorkflowApprovalSubject } from "@/agent/workflowTypes";

const api = vi.hoisted(() => ({ approvalSubjects: vi.fn() }));
vi.mock("@/api/platform", () => ({ platformApi: { workflows: api } }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, string>) => values?.name ? `${key}:${values.name}` : key }) }));
import PolicyEditor from "./WorkflowApprovalPolicyEditor.vue";

interface HostNode { children: HostNode[]; parent: HostNode | null }
const host = (): HostNode => ({ children: [], parent: null });
const renderer = createRenderer<HostNode, HostNode>({ createElement: host, createText: host, createComment: host, setText() {}, setElementText() {}, patchProp() {},
  insert(node, parent) { node.parent = parent; parent.children.push(node); }, remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); }, parentNode: node => node.parent, nextSibling: () => null });
type State = { subjects: Ref<WorkflowApprovalSubject[]>; selected: Ref<string[]>; enabled: Ref<boolean>; error: Ref<string>; options: Ref<{ value: string; label: string }[]>; loadSubjects(): Promise<void> };
const apps: App[] = [];
function mount(policy?: WorkflowApprovalPolicy, active = true) {
  const props = reactive<{ modelValue?: WorkflowApprovalPolicy; workflowId: string; active: boolean; disabled: boolean }>({ modelValue: policy, workflowId: "wf_a", active, disabled: false });
  let state!: State;
  const original = PolicyEditor as unknown as { setup(props: unknown, context: unknown): State };
  const component = { ...PolicyEditor, ssrRender: undefined, setup(value: unknown, context: unknown) { state = original.setup(value, context); return () => h("div"); } } as Component;
  const app = renderer.createApp({ render: () => h(component, { ...props, "onUpdate:modelValue": (value: WorkflowApprovalPolicy | undefined) => { props.modelValue = value; } }) });
  app.provide(ssrContextKey, { modules: new Set() }); apps.push(app); app.mount(host());
  return { props, state };
}
const bob = { principal_id: "bob-id", username: "bob", display_name: "Bob" };
async function settled() { await nextTick(); await Promise.resolve(); await nextTick(); }
beforeEach(() => { vi.clearAllMocks(); api.approvalSubjects.mockResolvedValue({ subjects: [bob] }); });
afterEach(() => apps.splice(0).forEach(app => app.unmount()));

describe("mounted approval policy editor", () => {
  it("leaves legacy nodes unchanged until enabled, then loads actual accounts and preserves selection on a same-mode refresh", async () => {
    const { props, state } = mount();
    await settled(); expect(api.approvalSubjects).not.toHaveBeenCalled(); expect(props.modelValue).toBeUndefined();
    state.enabled.value = true; await settled();
    expect(api.approvalSubjects).toHaveBeenCalledWith("wf_a");
    expect(state.options.value).toEqual([{ value: "bob", label: "Bob" }]);
    state.selected.value = ["bob"]; await settled();
    expect(props.modelValue).toEqual({ prevent_self_approval: true, assignment: "fixed", approvers: ["bob"] });
    props.modelValue = { ...props.modelValue!, approvers: [...props.modelValue!.approvers!] }; await settled();
    expect(api.approvalSubjects).toHaveBeenCalledTimes(1); expect(state.selected.value).toEqual(["bob"]);
  });

  it("ignores a previous workflow's late account response", async () => {
    let complete!: (value: { subjects: WorkflowApprovalSubject[] }) => void;
    api.approvalSubjects.mockImplementationOnce(() => new Promise(done => { complete = done; }));
    const { props, state } = mount({ prevent_self_approval: true, assignment: "fixed", approvers: [] });
    props.workflowId = "wf_b"; await settled();
    complete({ subjects: [{ principal_id: "old-id", username: "old", display_name: "Wrong workflow" }] }); await settled();
    expect(state.subjects.value).toEqual([bob]);
  });

  it("retains unavailable saved accounts while exposing load failures and explicit retry", async () => {
    api.approvalSubjects.mockRejectedValueOnce(new Error("offline"));
    const { state } = mount({ prevent_self_approval: true, assignment: "fixed", approvers: ["saved-user"] });
    await settled(); expect(state.error.value).toBe("offline"); expect(state.selected.value).toEqual(["saved-user"]);
    await state.loadSubjects();
    expect(state.options.value).toContainEqual({ value: "saved-user", label: "workflowApproval.unavailable:saved-user" });
    expect(state.selected.value).toEqual(["saved-user"]);
  });
});
