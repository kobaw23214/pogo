import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, reactive, type Ref } from "vue";
import type { ShellJobOutput, ShellJobRecord, ShellJobStatus } from "@/api/types";
const host = vi.hoisted(() => ({ tasks: vi.fn(), output: vi.fn(), stop: vi.fn(), cleanup: [] as (() => void)[] }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), onBeforeUnmount: (fn: () => void) => host.cleanup.push(fn), useSSRContext: () => ({ modules: new Set() }) }));
vi.mock("vue-i18n", async importOriginal => ({ ...await importOriginal<typeof import("vue-i18n")>(), useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/api/client", () => ({ api: { shellTasks: host.tasks, shellTaskOutput: host.output, stopShellTask: host.stop } }));
import BackgroundShellTasks from "./BackgroundShellTasks.vue";

type Controller = {
  running: Ref<number>;
  queued: Ref<number>;
  visibleTasks: Ref<ShellJobRecord[]>;
  selectedId: Ref<string | null>;
  page: Ref<ShellJobOutput | null>;
  select(task: ShellJobRecord): void;
  stop(task: ShellJobRecord): Promise<void>;
  loadOutput(offset?: number): Promise<void>;
  resetOutput(): void;
};
const scopes: ReturnType<typeof effectScope>[] = [];
const task = (status: ShellJobStatus = "queued"): ShellJobRecord => ({ task_id: "q", command: "sleep 60", workdir: "/work", status, started_at: 1, revision: 1, background: true, output_tail: "", output_bytes: 0, output_truncated: false });
const page = (offset: number): ShellJobOutput => ({ task_id: "q", output: "你", offset, next_offset: offset + 3, total_bytes: 6, truncated: true, status: "running", eof: false });

function mount(status: ShellJobStatus = "queued") {
  const props = reactive({ sessionId: "s1", displaySessionId: "s1", tasks: { q: task(status) }, connected: true });
  const scope = effectScope();
  scopes.push(scope);
  const emit = vi.fn();
  const c = scope.run(() => (BackgroundShellTasks as unknown as { setup(props: unknown, context: unknown): Controller }).setup(props, { emit, expose() {} }))!;
  return { c, props, emit };
}

beforeEach(() => {
  host.tasks.mockReset().mockResolvedValue({ tasks: [] });
  host.output.mockReset().mockImplementation((_sid, _id, offset) => Promise.resolve(page(offset)));
  host.stop.mockReset().mockImplementation(() => Promise.resolve({ task: { ...task(), status: "cancelled", revision: 2 } }));
});
afterEach(() => {
  for (const cleanup of host.cleanup.splice(0)) cleanup();
  for (const scope of scopes.splice(0)) scope.stop();
});

describe("Background shell tasks", () => {
  it("exposes queued work and allows stopping it", async () => {
    const { c, emit } = mount();
    expect(c.running.value).toBe(0);
    expect(c.queued.value).toBe(1);
    expect(c.visibleTasks.value.map(item => item.task_id)).toEqual(["q"]);
    const pending = task();
    c.select(pending);
    await c.stop(pending);
    expect(host.stop).toHaveBeenCalledWith("s1", "q");
    expect(emit).toHaveBeenCalledWith("updated", expect.objectContaining({ status: "cancelled" }));
  });

  it("reads bounded output pages and can return to the live tail", async () => {
    const { c } = mount();
    c.select(task());
    await c.loadOutput();
    expect(host.output).toHaveBeenLastCalledWith("s1", "q", 0, 65536, expect.any(AbortSignal));
    expect(c.page.value?.next_offset).toBe(3);
    await c.loadOutput(c.page.value!.next_offset);
    expect(host.output).toHaveBeenLastCalledWith("s1", "q", 3, 65536, expect.any(AbortSignal));
    c.resetOutput();
    expect(c.page.value).toBeNull();
  });

  it("does not claim to stop a task while disconnected", async () => {
    const { c, props } = mount();
    props.connected = false;
    const pending = task();
    c.select(pending);
    await c.stop(pending);
    expect(host.stop).not.toHaveBeenCalled();
  });
});
