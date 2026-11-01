import { ref, reactive } from "vue";
import { createWorkflowEmbedApi, type WorkflowEmbedSession } from "@/api/workflowEmbed";
import type { WorkflowRun } from "./workflowTypes";
import { runLive, runSuspended, sortWorkflowRuns, workflowRunCan } from "./workflowPresentation";

type EmbedApi = ReturnType<typeof createWorkflowEmbedApi>;
export function createWorkflowEmbedState(api: EmbedApi, selected: (rid: string | null) => void, changed: (run: WorkflowRun) => void) {
  const session = ref<WorkflowEmbedSession | null>(null);
  const run = ref<WorkflowRun | null>(null);
  const history = ref<WorkflowRun[]>([]);
  const inputs = reactive<Record<string, string>>(Object.create(null));
  const approvals = reactive<Record<string, Record<string, string>>>(Object.create(null));
  const artifacts = reactive<Record<string, string>>(Object.create(null));
  const error = ref("");
  const unavailable = ref(false);
  const busy = ref(false);
  const loading = ref(false);
  let epoch = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let startIntent: { key: string; content: string } | null = null;
  const retryIntents: Record<string, string> = Object.create(null);
  function clearMap(map: Record<string, unknown>) { Object.keys(map).forEach(key => delete map[key]); }
  function stopTimer() { if (timer !== undefined) clearTimeout(timer); timer = undefined; }
  function invalidate(message = "") {
    ++epoch; stopTimer(); api.clear(); session.value = null; run.value = null; history.value = [];
    clearMap(inputs); clearMap(approvals); clearMap(artifacts); startIntent = null;
    clearMap(retryIntents);
    busy.value = false; loading.value = false; unavailable.value = true; error.value = message;
  }
  function handleError(failure: unknown, request: number) {
    if (request !== epoch) return;
    const status = (failure as { status?: number })?.status;
    if (status === 401 || status === 403 || status === 404) invalidate();
    else error.value = failure instanceof Error ? failure.message : String(failure);
  }
  function adopt(record: WorkflowRun) {
    const previous = run.value;
    if (previous?.id !== record.id) { clearMap(approvals); clearMap(artifacts); }
    run.value = record;
    // A refresh may replace the record, but a pending form belongs to its wait
    // generation, not to that object instance.
    for (const [nodeId, node] of Object.entries(record.nodes ?? {})) {
      if (node.status === "waiting_human" && node.human && workflowRunCan(record, "can_approve", false, nodeId)) {
        const key = `${record.id}:${nodeId}:${node.human.deadline}`;
        if (!approvals[key]) approvals[key] = Object.create(null);
      }
    }
    for (const key of Object.keys(approvals)) {
      const valid = Object.entries(record.nodes ?? {}).some(([nid, node]) => key === `${record.id}:${nid}:${node.human?.deadline}` && node.status === "waiting_human" && workflowRunCan(record, "can_approve", false, nid));
      if (!valid) delete approvals[key];
    }
    history.value = sortWorkflowRuns([record, ...history.value.filter(item => item.id !== record.id)]);
    changed(record);
  }
  function schedule() {
    stopTimer();
    if (!session.value) return;
    // Completed records still recheck the short session and revocation state.
    timer = setTimeout(() => void refresh(), runLive(run.value) && !runSuspended(run.value) ? 1000 : 5000);
  }
  async function loadHistory(request = epoch) {
    try { const result = await api.list(); if (request === epoch) history.value = sortWorkflowRuns(result.runs); }
    catch (failure) { handleError(failure, request); }
  }
  async function select(rid: string | null) {
    const request = ++epoch; stopTimer(); run.value = null; clearMap(approvals); clearMap(artifacts); error.value = "";
    loading.value = false; busy.value = false;
    if (!session.value) return;
    selected(rid);
    if (!rid) { schedule(); return; }
    loading.value = true;
    try { const record = await api.get(rid); if (request === epoch) adopt(record); }
    catch (failure) { handleError(failure, request); }
    finally { if (request === epoch) { loading.value = false; schedule(); } }
  }
  async function authenticate(code: string, origin: string, initialRun: string | null) {
    invalidate(); const request = epoch;
    unavailable.value = false; loading.value = true; error.value = "";
    try {
      const result = await api.exchange(code, origin);
      if (request !== epoch) return;
      session.value = result;
      Object.assign(inputs, result.inputs ?? {});
      await loadHistory(request);
      if (request !== epoch) return;
      await select(result.run_id || initialRun || null);
    } catch (failure) { handleError(failure, request); }
    finally { if (request === epoch) loading.value = false; }
  }
  async function refresh() {
    if (!session.value) return;
    const request = epoch; const rid = run.value?.id; error.value = "";
    try {
      const nextSession = await api.session();
      if (request !== epoch) return;
      session.value = { ...session.value, ...nextSession };
      if (rid) { const record = await api.get(rid); if (request === epoch) adopt(record); }
      await loadHistory(request);
    } catch (failure) { handleError(failure, request); }
    finally { if (request === epoch) schedule(); }
  }
  async function action(work: () => Promise<unknown>) {
    if (busy.value || !session.value) return;
    busy.value = true; error.value = ""; const request = epoch;
    try { await work(); }
    catch (failure) { handleError(failure, request); }
    finally { if (request === epoch) busy.value = false; }
  }
  async function start() {
    if (session.value?.can_invoke !== true || runLive(run.value)) return;
    const content = JSON.stringify(inputs);
    if (startIntent?.content !== content) startIntent = { key: crypto.randomUUID(), content };
    const request = epoch;
    await action(async () => { const result = await api.start({ ...inputs }, startIntent!.key); if (request !== epoch) return; startIntent = null; busy.value = false; await select(result.id); });
  }
  async function cancel() {
    const current = run.value;
    if (!current || !workflowRunCan(current, "can_cancel", false)) return;
    const request = epoch;
    await action(async () => { await api.cancel(current.id); if (request === epoch) await refresh(); });
  }
  async function retry() {
    const current = run.value;
    if (!current || !workflowRunCan(current, "can_retry", false)) return;
    const request = epoch;
    const key = retryIntents[current.id] ??= crypto.randomUUID();
    await action(async () => { const result = await api.retry(current.id, key); if (request !== epoch) return; delete retryIntents[current.id]; busy.value = false; await select(result.id); });
  }
  function approvalValues(nodeId: string) {
    const current = run.value;
    const key = `${current?.id}:${nodeId}:${current?.nodes[nodeId]?.human?.deadline}`;
    return approvals[key] ?? Object.create(null) as Record<string, string>;
  }
  async function decide(nodeId: string, decision: string) {
    const current = run.value;
    if (!current || !workflowRunCan(current, "can_approve", false, nodeId)) return;
    const human = current.nodes[nodeId]?.human;
    if (!human || human.action || !human.actions.includes(decision)) return;
    const values = approvalValues(nodeId);
    if ((human.fields ?? []).some(field => field.required && !values[field.key]?.trim())) return;
    const request = epoch;
    await action(async () => { await api.resume(current.id, nodeId, decision, { ...values }); if (request === epoch) await refresh(); });
  }
  async function readArtifact(nodeId: string) {
    const current = run.value;
    if (!current || !workflowRunCan(current, "can_read_artifacts", false, nodeId)) return;
    const request = epoch;
    await action(async () => { const text = await api.artifact(current.id, nodeId); if (request === epoch) artifacts[nodeId] = text; });
  }
  return { session, run, history, inputs, approvals, artifacts, error, unavailable, busy, loading, authenticate, select, refresh, start, cancel, retry, decide, readArtifact, approvalValues, invalidate };
}
