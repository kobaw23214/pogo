import { describe, expect, it } from 'vitest';
import { emptyLive, type Snapshot } from '@/api/types';
import { createTaskContextUsageReader } from './taskContextUsage';
import { projectTaskSessionSnapshot, reduceTaskSessionEvent } from './taskSessionSnapshot';

const snapshot = (change: Partial<Snapshot> = {}): Snapshot => ({ seq: 1, messages: [{ id: 'u', role: 'user', run_id: 'engine-a', content: [{ type: 'text', text: 'a'.repeat(400) }] }],
  results: {}, live: emptyLive(), run: { active: false, model: 'node-model', context_window: 100000 }, policy: { mode: 'standard' }, usage: { input: 900000, output: 100000 }, context_tokens: 5000, overhead_tokens: 10000, ...change });

describe('node-scoped context readings', () => {
  it('uses the latest prompt measurement rather than cumulative billing or overhead estimates', () => {
    const read = createTaskContextUsageReader();
    expect(read('node-a', snapshot(), 'engine-a')).toMatchObject({ sessionId: 'node-a', activeRunId: 'engine-a', used: 5000, window: 100000, source: 'measured', percent: 5 });
  });
  it('keeps the last attributable prompt and model limit after completion', () => {
    const read = createTaskContextUsageReader();
    let value: Snapshot | null = snapshot({ live: { ...emptyLive(), active: true, run_id: 'engine-a' }, run: { active: true, run_id: 'engine-a', model: 'node-model', context_window: 100000 } });
    value = reduceTaskSessionEvent(value, { type: 'message', seq: 2, ts: 2, message_id: 'answer', usage: { input: 12345, output: 30 }, text: 'done' });
    value = reduceTaskSessionEvent(value, { type: 'run_end', seq: 3, ts: 3, usage: { input: 910000, output: 120000 }, end_reason: 'completed' });
    const projected = projectTaskSessionSnapshot(value, 'engine-a').snapshot;
    expect(projected?.run).toMatchObject({ active: false, model: 'node-model', context_window: 100000 });
    expect(read('node-a', projected, 'engine-a')).toMatchObject({ used: 12345, source: 'measured', percent: 12.345 });
  });
  it('never borrows the latest reading or model limit for an older step in a shared session', () => {
    const value = snapshot({ messages: [...snapshot().messages, { id: 'later', role: 'user', run_id: 'engine-b', content: [{ type: 'text', text: 'B' }] }],
      context_tokens: 88000, run: { active: true, run_id: 'engine-b', model: 'later-model', context_window: 200000 }, live: { ...emptyLive(), active: true, run_id: 'engine-b' } });
    const read = createTaskContextUsageReader(), older = projectTaskSessionSnapshot(value, 'engine-a').snapshot;
    expect(older?.context_tokens).toBe(0); expect(older?.run.context_window).toBeUndefined();
    expect(read('shared-session', older, 'engine-a')).toBeNull();
    expect(read('shared-session', value, 'engine-a')).toBeNull();
    expect(read('shared-session', projectTaskSessionSnapshot(value, 'engine-b').snapshot, 'engine-b')).toMatchObject({ used: 88000, window: 200000, percent: 44 });
  });
  it('estimates only the selected node history and labels unknown capacity without inventing a ratio', () => {
    const read = createTaskContextUsageReader();
    const estimated = read('node-a', snapshot({ context_tokens: 0, overhead_tokens: 1000 }), 'engine-a');
    expect(estimated).toMatchObject({ used: 1100, source: 'estimated' }); expect(estimated?.percent).toBeCloseTo(1.1);
    expect(read('node-b', snapshot({ run: { active: false, model: 'unknown' } }), 'engine-a')).toMatchObject({ used: 5000, window: 0, percent: null, free: null });
    expect(read('node-a', snapshot({ messages: [], context_tokens: 0, overhead_tokens: 0 }))).toMatchObject({ source: 'unknown', percent: null, free: null });
  });
  it('marks a retained reading approximate after model change and measured after the next actual response', () => {
    const read = createTaskContextUsageReader();
    const changed = reduceTaskSessionEvent(snapshot(), { type: 'model_changed', seq: 2, ts: 2, model: 'replacement', provider: 'other', context_window: 200000 });
    expect(read('node-a', changed, 'engine-a')).toMatchObject({ used: 5000, source: 'estimated', window: 200000 });
    const measured = reduceTaskSessionEvent(changed, { type: 'message', seq: 3, ts: 3, message_id: 'next', text: 'answer', usage: { input: 8000, output: 300 } });
    expect(read('node-a', measured, 'engine-a')).toMatchObject({ used: 8000, source: 'measured', percent: 4 });
  });
  it('clears a previous node measurement at a new run and does not claim it during the marker gap', () => {
    const started = reduceTaskSessionEvent(snapshot(), { type: 'run_start', seq: 2, ts: 2, run_id: 'engine-b' });
    expect(started?.context_tokens).toBe(0);
    expect(createTaskContextUsageReader()('shared', projectTaskSessionSnapshot(started, 'engine-b').snapshot, 'engine-b')).toBeNull();
  });
});
