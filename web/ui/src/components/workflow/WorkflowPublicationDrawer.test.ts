import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRenderer, h, nextTick, reactive, ssrContextKey, type App, type Component, type Ref } from "vue";
import type { WorkflowCallbackDelivery, WorkflowPublication, WorkflowPublicationInput, WorkflowPublicationList } from "@/api/workflowPublication";

const api = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn(), update: vi.fn(), rotate: vi.fn(), rotateCallback: vi.fn(), deliveries: vi.fn(), retryDelivery: vi.fn(), versions: vi.fn(), confirm: vi.fn() }));
vi.mock("@/api/workflowPublication", () => ({ workflowPublications: api }));
vi.mock("@/api/platform", () => ({ platformApi: { workflows: { versions: api.versions } } }));
vi.mock("element-plus", () => ({ ElMessage: { success: vi.fn(), warning: vi.fn() }, ElMessageBox: { confirm: api.confirm } }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
import PublicationDrawer from "./WorkflowPublicationDrawer.vue";

interface HostNode { children: HostNode[]; parent: HostNode | null }
const host = (): HostNode => ({ children: [], parent: null });
const renderer = createRenderer<HostNode, HostNode>({
  createElement: host, createText: host, createComment: host,
  setText() {}, setElementText() {}, patchProp() {},
  insert(node, parent) { node.parent = parent; parent.children.push(node); },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); },
  parentNode: node => node.parent, nextSibling: () => null,
});
function publication(id = "pub_a"): WorkflowPublication {
  return { id, workflow_id: "wf_a", name: id, version_id: "version_a", revision: 2, enabled: true,
    allowed_origins: ["https://business.example"], input_fields: [], mappings: [{ subject: "user_a", username: "alice" }],
    callback: { enabled: true, url: "https://business.example/callback" } };
}
type State = {
  data: Ref<WorkflowPublicationList>; form: WorkflowPublicationInput; selectedId: Ref<string>; origins: Ref<string>;
  secret: Ref<string>; callbackSecret: Ref<string>; deliveries: Ref<WorkflowCallbackDelivery[]>; deliveriesLoading: Ref<boolean>;
  clearForm(value?: WorkflowPublication): void; save(): Promise<void>; rotateCallback(): Promise<void>; retryDelivery(value: WorkflowCallbackDelivery): Promise<void>;
};
const apps: App[] = [];
function mount() {
  const props = reactive({ open: true, workflowId: "wf_a", name: "Order review", publishedVersion: "version_a" });
  let state!: State;
  const original = PublicationDrawer as unknown as { setup(props: unknown, context: unknown): State };
  const component = { ...PublicationDrawer, ssrRender: undefined,
    setup: (value: unknown, context: unknown) => { state = original.setup(value, context); return () => h("div"); } } as Component;
  const app = renderer.createApp({ render: () => h(component, props) });
  app.provide(ssrContextKey, { modules: new Set() }); apps.push(app); app.mount(host());
  return { props, state };
}
async function settled() { await nextTick(); await Promise.resolve(); await nextTick(); }
beforeEach(() => {
  vi.clearAllMocks();
  api.list.mockResolvedValue({ publications: [publication(), publication("pub_b")], subjects: [{ username: "alice" }] });
  api.versions.mockResolvedValue({ versions: [{ version_id: "version_a", name: "v1" }] });
  api.deliveries.mockResolvedValue({ deliveries: [] });
  api.confirm.mockResolvedValue(true);
});
afterEach(() => { apps.splice(0).forEach(app => app.unmount()); });

describe("publication business scope and durable callback management", () => {
  it("keeps a legacy scope and callback URL when editing and strips returned signing secrets from stored entries", async () => {
    const { state } = mount(); await settled();
    state.clearForm(state.data.value.publications[0]);
    expect(state.form.history_scope).toBe("workflow");
    state.form.callback!.enabled = false;
    api.update.mockResolvedValue({ ...publication(), history_scope: "workflow", callback: { enabled: false, url: "https://business.example/callback" }, callback_secret: "signing-once" });
    await state.save();
    expect(api.update).toHaveBeenCalledWith("wf_a", "pub_a", expect.objectContaining({ revision: 2, history_scope: "workflow", callback: { enabled: false, url: "https://business.example/callback" } }));
    expect(state.callbackSecret.value).toBe("signing-once");
    expect(state.data.value.publications[0]).not.toHaveProperty("callback_secret");
    expect(state.data.value.publications[0]?.callback?.url).toBe("https://business.example/callback");
  });

  it("defaults a new entry to business isolation and shows its two secrets once, then clears them on close", async () => {
    const { state, props } = mount(); await settled();
    state.clearForm();
    expect(state.form.history_scope).toBe("business");
    state.origins.value = "https://business.example";
    state.form.mappings = [{ subject: "user_a", username: "alice" }];
    state.form.callback = { enabled: true, url: "https://business.example/callback" };
    api.create.mockResolvedValue({ publication: { ...publication("pub_new"), history_scope: "business" }, client_secret: "client-once", callback_secret: "signing-once" });
    await state.save();
    expect(api.create).toHaveBeenCalledWith("wf_a", expect.objectContaining({ history_scope: "business", callback: { enabled: true, url: "https://business.example/callback" } }));
    expect(state.secret.value).toBe("client-once"); expect(state.callbackSecret.value).toBe("signing-once");
    expect(state.data.value.publications[0]).not.toHaveProperty("client_secret");
    props.open = false; await settled();
    expect(state.secret.value).toBe(""); expect(state.callbackSecret.value).toBe(""); expect(state.deliveries.value).toEqual([]);
  });

  it("does not restore another publication's late delivery list", async () => {
    let resolve!: (value: { deliveries: WorkflowCallbackDelivery[] }) => void;
    api.deliveries.mockImplementation((_wid, pid) => pid === "pub_a" ? new Promise(done => { resolve = done; }) : Promise.resolve({ deliveries: [{ id: "event_b", run_id: "run_b", status: "failed", attempts: 3 }] }));
    const { state } = mount(); await settled();
    state.selectedId.value = "pub_b"; await settled();
    resolve({ deliveries: [{ id: "event_a", run_id: "run_a", status: "delivered", attempts: 1 }] }); await settled();
    expect(state.deliveries.value.map(item => item.id)).toEqual(["event_b"]);
    expect(state.deliveriesLoading.value).toBe(false);
  });

  it("requires confirmation before retrying a stable event with the current callback target", async () => {
    const { state } = mount(); await settled();
    const event = { id: "event_a", run_id: "run_a", status: "blocked", attempts: 3 };
    await state.retryDelivery(event);
    expect(api.confirm).toHaveBeenCalledWith("workflowEmbed.deliveryRetryConfirm", "workflowEmbed.deliveryRetry", expect.any(Object));
    expect(api.retryDelivery).toHaveBeenCalledWith("wf_a", "pub_a", "event_a");
  });

  it("does not display a signing secret returned after selecting another entry", async () => {
    const { state } = mount(); await settled();
    let resolve!: (value: { publication: WorkflowPublication; callback_secret: string }) => void;
    api.rotateCallback.mockReturnValue(new Promise(done => { resolve = done; }));
    const pending = state.rotateCallback(); await settled();
    state.selectedId.value = "pub_b";
    resolve({ publication: publication(), callback_secret: "must-not-cross-entry" }); await pending;
    expect(state.callbackSecret.value).toBe(""); expect(state.selectedId.value).toBe("pub_b");
  });
});
