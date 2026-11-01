import { computed, onScopeDispose, ref, watch } from 'vue';
import { authHeaders } from '@/api/client';
import { validAgentWorkflowId } from './agentWorkflowNavigation';
import { workflowActivitySnapshot, type AgentWorkflowActivitySnapshot } from './agentWorkflowActivity';

type ActivityConnection = 'idle' | 'connecting' | 'live' | 'reconnecting' | 'ended' | 'denied';
export function useAgentWorkflowActivity(context: () => { agentId?: string; workflowId: string; runId: string; visible: boolean; authEpoch?: number }) {
  const snapshot = ref<AgentWorkflowActivitySnapshot | null>(null);
  const connection = ref<ActivityConnection>('idle');
  const error = ref('');
  let disposed = false, epoch = 0, attempts = 0;
  let controller: AbortController | undefined, retry: ReturnType<typeof setTimeout> | undefined, watchdog: ReturnType<typeof setInterval> | undefined;
  const connected = computed(() => connection.value === 'live' || connection.value === 'ended');
  function stop() { ++epoch; controller?.abort(); clearTimeout(retry); clearInterval(watchdog); }
  async function open(mine: number) {
    const scope = context();
    if (disposed || mine !== epoch || !scope.visible) return;
    const current = () => !disposed && mine === epoch && context().visible;
    const abort = new AbortController(); controller = abort;
    connection.value = attempts ? 'reconnecting' : 'connecting';
    let denied = false, ended = false, received = false, lastBytes = Date.now();
    let attemptWatchdog: ReturnType<typeof setInterval> | undefined;
    const handshake = setTimeout(() => abort.abort(), 10_000);
    try {
      const path = `/api/apps/${encodeURIComponent(scope.agentId!)}/agent-workflows/${encodeURIComponent(scope.workflowId)}/runs/${encodeURIComponent(scope.runId)}/activity`;
      const response = await fetch(path, { headers: { ...authHeaders(), Accept: 'text/event-stream' }, credentials: 'same-origin', cache: 'no-store', signal: abort.signal });
      clearTimeout(handshake);
      if (!current()) return;
      if (!response.ok || !response.body || !response.headers.get('content-type')?.includes('text/event-stream')) { denied = [401, 403, 404].includes(response.status); throw new Error('unavailable'); }
      attemptWatchdog = setInterval(() => { if (Date.now() - lastBytes > 30_000) abort.abort(); }, 5000); watchdog = attemptWatchdog;
      const reader = response.body.getReader(), decoder = new TextDecoder();
      let buffer = '';
      while (current()) {
        const next = await reader.read();
        if (!current()) return;
        if (next.done) break;
        lastBytes = Date.now(); buffer = (buffer + decoder.decode(next.value, { stream: true })).replace(/\r\n/g, '\n');
        if (buffer.length > 1_000_000) throw new Error('invalid');
        let split: number;
        while ((split = buffer.indexOf('\n\n')) >= 0) {
          const frame = buffer.slice(0, split); buffer = buffer.slice(split + 2);
          const lines = frame.split('\n'), event = lines.find(line => line.startsWith('event:'))?.slice(6).trim();
          const data = lines.filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
          if (event === 'error') { const code = Number((JSON.parse(data) as { code?: unknown }).code); denied = [401, 403, 404].includes(code); throw new Error('unavailable'); }
          if (event !== 'snapshot' || !data) continue;
          const value = workflowActivitySnapshot(JSON.parse(data), scope.agentId!, scope.workflowId, scope.runId);
          if (!value) { denied = true; throw new Error('scope'); }
          snapshot.value = value; connection.value = 'live'; error.value = ''; received = true; attempts = 0;
          ended = ['completed', 'failed', 'cancelled', 'interrupted'].includes(value.status);
        }
      }
    } catch { if (current()) error.value = 'unavailable'; }
    finally {
      clearTimeout(handshake);
      abort.abort();
      clearInterval(attemptWatchdog);
      if (current()) {
        if (denied) { snapshot.value = null; connection.value = 'denied'; }
        else if (ended && received) { connection.value = 'ended'; error.value = ''; }
        else { connection.value = 'reconnecting'; error.value = 'unavailable'; retry = setTimeout(() => void open(mine), Math.min(500 * 2 ** attempts++, 5000)); }
      }
    }
  }
  watch(() => JSON.stringify(context()), () => {
    stop(); snapshot.value = null; connection.value = 'idle'; error.value = ''; attempts = 0;
    const scope = context();
    if (scope.visible && validAgentWorkflowId(scope.agentId) && validAgentWorkflowId(scope.workflowId) && validAgentWorkflowId(scope.runId)) void open(epoch);
  }, { immediate: true, flush: 'sync' });
  function refresh() { stop(); attempts = 0; error.value = ''; if (context().visible) void open(epoch); }
  onScopeDispose(() => { disposed = true; stop(); });
  return { snapshot, connection, connected, error, refresh };
}
