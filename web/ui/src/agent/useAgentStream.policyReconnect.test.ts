import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '@/api/client';
import { emptyLive, type AgentEvent } from '@/api/types';
import { useAgentStream } from './useAgentStream';

const oldInstance = 'OLD_STREAM_INSTANCE_0123456789';
const newInstance = 'NEW_STREAM_INSTANCE_0123456789';
function snapshot(seq: number, instance: string, mode: 'auto' | 'standard' = 'standard'): AgentEvent {
  return { type: 'snapshot', seq, ts: seq, snapshot: { seq, stream_instance: instance, messages: [], results: {}, live: emptyLive(), run: { active: false }, policy: { mode }, usage: { input: 0, output: 0 }, context_tokens: 0 } };
}
function transport() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
  return {
    response: new Response(body, { headers: { 'X-Pogo-Stream-Backlog-Through': '100' } }),
    send(...events: AgentEvent[]) { controller.enqueue(new TextEncoder().encode(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(''))); },
    end() { controller.close(); },
  };
}

describe('approval mode across reconstructed stream instances', () => {
  const streams: ReturnType<typeof useAgentStream>[] = [];
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const stream of streams.splice(0)) stream.disconnect();
    vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks();
  });
  function store() { const stream = useAgentStream({ attachments: false }); streams.push(stream); return stream; }

  it('sends the saved instance on reconnect and replaces stale full access even when the new sequence is larger', async () => {
    const first = transport(), restarted = transport(), incremental = transport();
    const fetch = vi.fn().mockResolvedValueOnce(first.response).mockResolvedValueOnce(restarted.response).mockResolvedValueOnce(incremental.response);
    vi.stubGlobal('fetch', fetch);
    const stream = store(), connection = stream.connect('chat-one');
    first.send(snapshot(10, oldInstance, 'auto'));
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.policy.value.mode).toBe('auto');
    first.end(); await connection;
    restarted.send(snapshot(15, newInstance), { type: 'gate_request', seq: 16, ts: 16, gate_id: 'new-gate', call_id: 'new-edit', name: 'agent_workflow_edit', deadline: 1000 });
    await vi.advanceTimersByTimeAsync(500);
    expect(fetch.mock.calls[1]?.[0]).toBe(`/api/sessions/chat-one/stream?from=10&epoch=${oldInstance}`);
    expect(stream.policy.value.mode).toBe('standard');
    expect(stream.live.value.pending_gates.map(item => item.gate_id)).toEqual(['new-gate']);
    restarted.end();
    incremental.send({ type: 'policy_changed', seq: 17, ts: 17, policy: { mode: 'auto' } });
    await vi.advanceTimersByTimeAsync(500);
    expect(fetch.mock.calls[2]?.[0]).toBe(`/api/sessions/chat-one/stream?from=16&epoch=${newInstance}`);
    expect(stream.policy.value.mode).toBe('auto');
    stream.disconnect(); incremental.end();
  });

  it('clears the instance on conversation changes and explicit disconnect', async () => {
    const first = transport(), other = transport(), reopened = transport();
    const fetch = vi.fn().mockResolvedValueOnce(first.response).mockResolvedValueOnce(other.response).mockResolvedValueOnce(reopened.response);
    vi.stubGlobal('fetch', fetch);
    const stream = store(), old = stream.connect('chat-one');
    first.send(snapshot(10, oldInstance)); await vi.advanceTimersByTimeAsync(1);
    const next = stream.connect('chat-two');
    other.send(snapshot(20, newInstance)); await vi.advanceTimersByTimeAsync(1);
    expect(fetch.mock.calls[1]?.[0]).toBe('/api/sessions/chat-two/stream');
    stream.disconnect();
    const last = stream.connect('chat-two');
    reopened.send(snapshot(20, newInstance)); await vi.advanceTimersByTimeAsync(1);
    expect(fetch.mock.calls[2]?.[0]).toBe('/api/sessions/chat-two/stream');
    stream.disconnect(); first.end(); other.end(); reopened.end(); await Promise.all([old, next, last]);
  });

  it('clears the instance when the account changes before a reconnect', async () => {
    vi.stubGlobal('window', new EventTarget());
    const first = transport(), changed = transport();
    const fetch = vi.fn().mockResolvedValueOnce(first.response).mockResolvedValueOnce(changed.response);
    vi.stubGlobal('fetch', fetch);
    const stream = store(), connection = stream.connect('chat-one');
    first.send(snapshot(10, oldInstance)); await vi.advanceTimersByTimeAsync(1);
    window.dispatchEvent(new Event('pogo:auth-changed'));
    first.end(); await connection;
    changed.send(snapshot(12, newInstance)); await vi.advanceTimersByTimeAsync(500);
    expect(fetch.mock.calls[1]?.[0]).toBe('/api/sessions/chat-one/stream?from=10');
    stream.disconnect(); changed.end();
  });

  it('ignores a delayed REST snapshot from the previous stream instance', async () => {
    const first = transport(), restarted = transport();
    const fetch = vi.fn().mockResolvedValueOnce(first.response).mockResolvedValueOnce(restarted.response);
    vi.stubGlobal('fetch', fetch);
    let resolve!: (value: Awaited<ReturnType<typeof api.session>>) => void;
    vi.spyOn(api, 'session').mockImplementation(() => new Promise(done => { resolve = done as typeof resolve; }));
    vi.spyOn(api, 'sessionAttachments').mockResolvedValue([]);
    const stream = store(), connection = stream.connect('chat-one');
    first.send(snapshot(10, oldInstance, 'auto'), { type: 'rewound', seq: 11, ts: 11 });
    await vi.advanceTimersByTimeAsync(1);
    first.end(); await connection;
    restarted.send(snapshot(15, newInstance)); await vi.advanceTimersByTimeAsync(500);
    resolve({ session_id: 'chat-one', snapshot: snapshot(100, oldInstance, 'auto').snapshot! });
    await vi.advanceTimersByTimeAsync(1);
    expect(stream.policy.value.mode).toBe('standard');
    expect(stream.seq.value).toBe(15);
    stream.disconnect(); restarted.end();
  });
});
