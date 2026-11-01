import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, h, nextTick, reactive, ssrContextKey, type App, type Component, type Ref } from "vue";
import type { WorkflowRun, WorkflowRunParticipants } from "@/agent/workflowTypes";

const api = vi.hoisted(() => ({ participants: vi.fn(), setParticipant: vi.fn() }));
vi.mock("@/api/platform", () => ({ platformApi: { workflowRuns: api } }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
import ParticipantsDrawer from "./WorkflowParticipantsDrawer.vue";

interface HostNode { children: HostNode[]; parent: HostNode | null; }
const host = (): HostNode => ({ children: [], parent: null });
const renderer = createRenderer<HostNode, HostNode>({
  createElement: host, createText: host, createComment: host,
  setText() {}, setElementText() {}, patchProp() {},
  insert(node, parent) { node.parent = parent; parent.children.push(node); },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); },
  parentNode: node => node.parent, nextSibling: () => null,
});
const accounts: WorkflowRunParticipants = { participants: [], subjects: [
  { principal_id: "admin", display_name: "admin" },
  { principal_id: "alice", display_name: "alice" },
  { principal_id: "bob", display_name: "bob" },
] };
function run(canManage = true): WorkflowRun {
  return { id: "run_a", workflow_id: "wf_a", revision: 1, status: "working", nodes: {}, created_at: "2026-10-27T10:00:00Z",
    definition: { id: "wf_a", name: "review", description: "", revision: 1, nodes: [], published: null, dirty: false, created_at: "", updated_at: "" },
    capabilities: { can_read: true, can_cancel: true, can_approve: true, can_retry: false, can_read_artifacts: true, can_manage_participants: canManage } };
}
type Form = { data: Ref<WorkflowRunParticipants>; principal: Ref<string>; loading: Ref<boolean> };
const apps: App[] = [];
function mount(open: boolean, current = run()) {
  const props = reactive({ open, workflowId: "wf_a", run: current });
  let form!: Form;
  const original = ParticipantsDrawer as unknown as { setup(props: unknown, context: unknown): Form };
  const component = { ...ParticipantsDrawer, ssrRender: undefined,
    setup: (value: unknown, context: unknown) => { form = original.setup(value, context); return () => h("div"); } } as Component;
  const app = renderer.createApp({ render: () => h(component, props) });
  app.provide(ssrContextKey, { modules: new Set() });
  apps.push(app); app.mount(host());
  return { props, form };
}
async function settled() { await nextTick(); await Promise.resolve(); await nextTick(); }
beforeEach(() => { vi.clearAllMocks(); api.participants.mockResolvedValue(accounts); });
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); });

describe("mounted workflow participant loading", () => {
  it("loads account options when the drawer is initially mounted open", async () => {
    const { form } = mount(true);
    await settled();
    expect(api.participants).toHaveBeenCalledWith("wf_a", "run_a");
    expect(form.data.value.subjects.map(item => item.display_name)).toEqual(["admin", "alice", "bob"]);
  });

  it("loads after opening and keeps options and selection across same-Run polling", async () => {
    const { props, form } = mount(false);
    props.open = true;
    await settled();
    expect(form.data.value.subjects).toHaveLength(3);
    form.principal.value = "bob";
    props.run = run();
    await settled();
    expect(api.participants).toHaveBeenCalledTimes(1);
    expect(form.data.value.subjects).toHaveLength(3);
    expect(form.principal.value).toBe("bob");
  });

  it("loads options when management capability arrives after the Run identity", async () => {
    const { props, form } = mount(true, run(false));
    await settled();
    expect(api.participants).not.toHaveBeenCalled();
    props.run = run(true);
    await settled();
    expect(api.participants).toHaveBeenCalledTimes(1);
    expect(form.data.value.subjects).toHaveLength(3);
  });
});
