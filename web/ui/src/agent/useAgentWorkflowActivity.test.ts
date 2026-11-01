import { afterEach, describe, expect, it, vi } from 'vitest';
import { effectScope, nextTick, reactive } from 'vue';
import { useAgentWorkflowActivity } from './useAgentWorkflowActivity';
vi.mock('@/api/client', () => ({ authHeaders: () => ({ Authorization: 'Bearer isolated-account' }) }));
const scopes: ReturnType<typeof effectScope>[] = [];
const streams: ReadableStreamDefaultController<Uint8Array>[] = [];
const flush = async () => { for (let i = 0; i < 10; ++i) await Promise.resolve(); await nextTick(); };
function response() { return new Response(new ReadableStream<Uint8Array>({ start(c) { streams.push(c); } }), { headers: { 'Content-Type': 'text/event-stream' } }); }
function send(index: number, runId = 'run-a', phase = 'thinking') {
  const body = { agent_id: 'agent-a', workflow_id: 'flow-a', run_id: runId, status: 'running', updated_at: 100, nodes: [{ step_id: 'left', status: 'running', phase, observed: true, turn: 2, updated_at: 100 }] };
  streams[index]!.enqueue(new TextEncoder().encode(`event: snapshot\ndata: ${JSON.stringify(body)}\n\n`));
}
function mount() {
  const props = reactive({ agentId: 'agent-a', workflowId: 'flow-a', runId: 'run-a', visible: true, authEpoch: 0 });
  const scope = effectScope(); scopes.push(scope);
  return { props, result: scope.run(() => useAgentWorkflowActivity(() => props))! };
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); for (const stream of streams.splice(0)) { try { stream.close(); } catch { /* already ended */ } } vi.useRealTimers(); vi.unstubAllGlobals(); });
describe('workflow activity subscription lifetime', () => {
  it('uses one scoped connection and applies independent node activity', async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(response())); vi.stubGlobal('fetch', fetch);
    const { result } = mount(); await flush(); send(0); await flush();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0]![0]).toContain('/agent-workflows/flow-a/runs/run-a/activity');
    expect(result.connection.value).toBe('live'); expect(result.snapshot.value?.nodes[0]?.turn).toBe(2);
  });
  it('clears state and aborts the old scope before accepting a different Run', async () => {
    const fetch = vi.fn().mockImplementation(() => Promise.resolve(response())); vi.stubGlobal('fetch', fetch);
    const { props, result } = mount(); await flush(); send(0); await flush();
    props.runId = 'run-b'; await flush();
    expect((fetch.mock.calls[0]![1] as RequestInit).signal?.aborted).toBe(true); expect(result.snapshot.value).toBeNull();
    send(0, 'run-a', 'answer'); await flush(); expect(result.snapshot.value).toBeNull();
    send(1, 'run-b'); await flush(); expect(result.snapshot.value?.run_id).toBe('run-b');
    props.visible = false; await flush(); expect(result.snapshot.value).toBeNull(); expect(result.connection.value).toBe('idle');
  });
  it('marks stale activities on disconnect, then reconnects with a fresh snapshot', async () => {
    vi.useFakeTimers(); const fetch = vi.fn().mockImplementation(() => Promise.resolve(response())); vi.stubGlobal('fetch', fetch);
    const { result } = mount(); await flush(); send(0); await flush(); streams[0]!.close(); await flush();
    expect(result.snapshot.value?.nodes[0]?.phase).toBe('thinking'); expect(result.connected.value).toBe(false);
    await vi.advanceTimersByTimeAsync(500); await flush(); send(1, 'run-a', 'approval'); await flush();
    expect(result.connected.value).toBe(true); expect(result.snapshot.value?.nodes[0]?.phase).toBe('approval');
  });
  it('drops old data on a scoped stream error and does not retry a denial', async () => {
    vi.useFakeTimers(); const fetch = vi.fn().mockImplementation(() => Promise.resolve(response())); vi.stubGlobal('fetch', fetch);
    const { result } = mount(); await flush(); send(0); await flush();
    streams[0]!.enqueue(new TextEncoder().encode('event: error\ndata: {"code":403}\n\n')); await flush();
    expect(result.connection.value).toBe('denied'); expect(result.snapshot.value).toBeNull();
    await vi.advanceTimersByTimeAsync(6000); expect(fetch).toHaveBeenCalledTimes(1);
  });
  it('times out a handshake that never returns headers and retries without staying stuck', async () => {
    vi.useFakeTimers();
    const fetch = vi.fn().mockImplementation((_url: string, options: RequestInit) => new Promise<Response>((_resolve, reject) => {
      options.signal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
    }));
    vi.stubGlobal('fetch', fetch);
    const { props, result } = mount(); await flush(); expect(result.connection.value).toBe('connecting');
    await vi.advanceTimersByTimeAsync(10_000); await flush();
    expect((fetch.mock.calls[0]![1] as RequestInit).signal?.aborted).toBe(true);
    expect(result.connection.value).toBe('reconnecting');
    await vi.advanceTimersByTimeAsync(500); await flush(); expect(fetch).toHaveBeenCalledTimes(2);
    props.visible = false; await flush(); expect(result.connection.value).toBe('idle');
  });
});
