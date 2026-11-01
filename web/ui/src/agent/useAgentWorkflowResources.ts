import { onScopeDispose, ref, watch } from 'vue';
import { agentWorkflowsApi, type AgentWorkflowResourceCatalog } from '@/api/agentWorkflows';
import { validAgentWorkflowId } from './agentWorkflowNavigation';
import { safeWorkflowFilePath } from './agentWorkflowInputs';

export function useAgentWorkflowResources(source: () => { agentId?: string; sessionId: string | null; visible: boolean; authEpoch: number; revision?: number }) {
  const catalog = ref<AgentWorkflowResourceCatalog | null>(null), loading = ref(false), error = ref('');
  let disposed = false, ticket = 0, controller: AbortController | undefined;
  const identity = () => JSON.stringify(source());
  function cancel() { ++ticket; controller?.abort(); loading.value = false; }
  async function refresh() {
    cancel(); const selected = source(), key = identity(), mine = ticket;
    if (disposed || !selected.visible || !validAgentWorkflowId(selected.agentId)) return;
    const abort = new AbortController(); controller = abort; loading.value = true; error.value = '';
    let timedOut = false;
    const timeout = setTimeout(() => { timedOut = true; abort.abort(); }, 10_000);
    const current = () => !disposed && mine === ticket && key === identity();
    try {
      const result = await agentWorkflowsApi.call(selected.agentId, 'read', { action: 'resources', ...(selected.sessionId ? { source_session_id: selected.sessionId } : {}) }, abort.signal);
      if (!current() || abort.signal.aborted) return;
      const resources = result.resources;
      const agent = (result as { agent_id?: string }).agent_id;
      if (result.ok === false || agent && agent !== selected.agentId || !resources || !Array.isArray(resources.skills) || !Array.isArray(resources.shortcuts) || !Array.isArray(resources.files) || resources.source_session_id && resources.source_session_id !== selected.sessionId || resources.files.some(file => !safeWorkflowFilePath(file.path) || !Number.isFinite(file.size) || file.size < 0)) throw new Error('Invalid resource catalog');
      catalog.value = resources;
    } catch (issue) {
      if (current() && ((issue as Error)?.name !== 'AbortError' || timedOut)) { catalog.value = null; error.value = 'unavailable'; }
    } finally { clearTimeout(timeout); if (current()) loading.value = false; }
  }
  watch(identity, () => { cancel(); catalog.value = null; error.value = ''; if (source().visible) void refresh(); }, { immediate: true, flush: 'sync' });
  onScopeDispose(() => { disposed = true; cancel(); });
  return { catalog, loading, error, refresh };
}
