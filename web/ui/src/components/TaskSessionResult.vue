<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import { buildTimeline } from "@/agent/timeline";
import { buildExchanges } from "@/agent/exchanges";
import { approvalUiKey } from "@/agent/approvalState";
import { provideCardDisclosure } from "@/agent/cardDisclosure";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import WorkLog from "./WorkLog.vue";
import ApprovalBar from "./ApprovalBar.vue";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import SubagentSheet from "./SubagentSheet.vue";
import type { OpenSheetRequest } from "./dockSheets";
import { useTaskSessionStream } from "@/agent/useTaskSessionStream";
import { createTaskContextUsageReader, type TaskContextReading, type TaskContextScope } from "@/agent/taskContextUsage";
import TaskContextRing from './TaskContextRing.vue';

const props = defineProps<{ sessionId: string; agentId: string; visible: boolean; title?: string; workflowId?: string; workflowRunId?: string; activeRunId?: string; compact?: boolean; hideContextRing?: boolean }>();
const emit = defineEmits<{ close: []; context: [TaskContextReading | null, TaskContextScope] }>();
const { t } = useI18n();
const { snapshot, contextSnapshot, verifiedSession, loading, error: streamError, connection, canDecide, isolationMissing, refresh } = useTaskSessionStream(() => props);
const readContext = createTaskContextUsageReader();
const contextScope = (): TaskContextScope => ({ agentId: props.agentId, sessionId: props.sessionId, ...(props.activeRunId ? { activeRunId: props.activeRunId } : {}), ...(props.workflowRunId ? { workflowRunId: props.workflowRunId } : {}) });
const contextReading = computed(() => props.visible && verifiedSession.value === props.sessionId && !isolationMissing.value && (!props.workflowRunId || !!props.activeRunId) ? readContext(props.sessionId, contextSnapshot.value, props.activeRunId, connection.value !== 'live') : null);
watch([contextReading, () => JSON.stringify(contextScope())], () => emit('context', contextReading.value, contextScope()), { immediate: true, flush: 'sync' });
onBeforeUnmount(() => emit('context', null, { ...contextScope(), unmounted: true }));
const error = computed(() => streamError.value ? t(`taskSessionResult.${streamError.value}`) : "");
const actionError = ref(""), stopping = ref(false);
const child = ref<OpenSheetRequest | null>(null);
const approvalPending = ref<Record<string, boolean>>({}), approvalErrors = ref<Record<string, string>>({});
let disposed = false, epoch = 0;
const context = () => JSON.stringify([props.agentId, props.sessionId, props.visible, props.workflowId, props.workflowRunId, props.activeRunId]);
provideCardDisclosure(() => verifiedSession.value ? `task-result:${props.agentId}:${verifiedSession.value}` : undefined);
// Never borrow the normal conversation's file opener for a task workspace.
provide(workspaceFileNavigationKey, { scope: () => null, open: () => {} });
const gates = computed(() => snapshot.value?.live.pending_gates ?? []);
const active = computed(() => !!snapshot.value?.run.active);
const approving = computed(() => Object.values(approvalPending.value).some(Boolean));
provide(approvalUiKey, {
  pending: approvalPending, errors: approvalErrors, get canDecide() { return canDecide.value; },
  activeGateIds: computed(() => new Set(canDecide.value ? gates.value.map(gate => gate.gate_id) : [])),
  sessionId: verifiedSession, serverOffset: computed(() => null),
});
const timeline = computed(() => {
  const value = snapshot.value;
  return value ? buildTimeline(value.messages, value.results, value.live, Object.fromEntries((value.bg_tasks ?? []).map(task => [task.task_id, task])), value.answer_review) : [];
});
const exchanges = computed(() => buildExchanges(timeline.value, active.value));
const current = (key: string, mine: number) => !disposed && props.visible && context() === key && epoch === mine;
function approvalContext(gateId: string) {
  if (!canDecide.value || !verifiedSession.value || approvalPending.value[gateId] || !gates.value.some(gate => gate.gate_id === gateId)) return null;
  return { sid: verifiedSession.value, key: context(), epoch };
}
async function gateAction(gateId: string, action: (sid: string) => Promise<unknown>) {
  const origin = approvalContext(gateId); if (!origin) return;
  approvalPending.value = { ...approvalPending.value, [gateId]: true };
  approvalErrors.value = { ...approvalErrors.value, [gateId]: "" }; actionError.value = "";
  try { await action(origin.sid); if (current(origin.key, origin.epoch)) await refresh(); }
  catch { if (current(origin.key, origin.epoch)) { actionError.value = t("taskSessionResult.actionFailed"); approvalErrors.value = { ...approvalErrors.value, [gateId]: actionError.value }; } }
  finally { if (current(origin.key, origin.epoch)) approvalPending.value = { ...approvalPending.value, [gateId]: false }; }
}
function decide(event: { gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }) {
  void gateAction(event.gateId, sid => api.decideGate(sid, event.gateId, event.allow, { args: event.args, remember: event.remember }));
}
function gateClock(gateId: string, frozen: boolean) { void gateAction(gateId, sid => frozen ? api.freezeGate(sid, gateId) : api.thawGate(sid, gateId)); }
async function stop() {
  if (props.workflowId || props.workflowRunId) return;
  if (!canDecide.value || !verifiedSession.value || !active.value || stopping.value) return;
  const key = context(), mine = epoch, sid = verifiedSession.value;
  stopping.value = true; actionError.value = "";
  try { await api.cancel(sid); if (current(key, mine)) await refresh(); }
  catch { if (current(key, mine)) actionError.value = t("taskSessionResult.actionFailed"); }
  finally { if (current(key, mine)) stopping.value = false; }
}
function openChild(value: OpenSheetRequest) { if (canDecide.value && verifiedSession.value && value.childSession) child.value = value; }
watch(() => [props.agentId, props.sessionId, props.visible, props.workflowId, props.workflowRunId, props.activeRunId, verifiedSession.value], () => {
  ++epoch; actionError.value = ""; stopping.value = false; child.value = null;
  approvalPending.value = {}; approvalErrors.value = {};
}, { immediate: true, flush: "sync" });
onBeforeUnmount(() => { disposed = true; ++epoch; });
defineExpose({ snapshot, verifiedSession, connection, canDecide, isolationMissing, refresh, contextReading });
</script>

<template>
  <section class="task-session-result" :class="{ compact }" data-testid="task-session-result" :data-session-id="verifiedSession || undefined">
    <header v-if="!compact" class="tr-header">
      <button type="button" class="tr-back" @click="emit('close')"><el-icon><ArrowLeft /></el-icon>{{ t('taskSessionResult.back') }}</button>
      <div class="tr-title"><small>{{ t(active ? 'taskSessionResult.active' : 'taskSessionResult.done') }}</small><h2>{{ title || t('taskSessionResult.title') }}</h2></div>
      <button v-if="active && verifiedSession && !workflowId && !workflowRunId" type="button" class="tr-stop" :disabled="stopping" @click="stop">{{ t(stopping ? 'taskSessionResult.stopping' : 'taskSessionResult.stop') }}</button>
      <TaskContextRing v-if="!hideContextRing" :reading="contextReading" />
      <button type="button" class="tr-refresh" :disabled="loading" :aria-label="t('taskSessionResult.refresh')" @click="refresh"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button>
    </header>
    <p v-if="!compact" class="tr-note">{{ t('taskSessionResult.readOnly') }}</p>
    <div class="tr-connection" :class="{ 'is-offline': connection === 'reconnecting' || connection === 'error' }" role="status" data-testid="task-session-connection" :data-connection="connection"><span>{{ t(`taskSessionResult.connection.${connection}`) }}</span><button v-if="compact && (connection === 'reconnecting' || connection === 'error')" type="button" class="tr-reconnect" :disabled="loading" @click="refresh">{{ t('taskSessionResult.reconnect') }}</button><TaskContextRing v-if="compact && !hideContextRing" class="tr-context" :reading="contextReading" /></div>
    <p v-if="error || actionError" class="tr-error" role="alert">{{ error || actionError }}</p>
    <p v-if="isolationMissing" class="tr-note" data-testid="task-session-isolation-missing">{{ t('taskSessionResult.isolationMissing') }}</p>
    <div v-if="child && verifiedSession" class="tr-child">
      <button type="button" class="tr-back" @click="child = null">{{ t('taskSessionResult.closeChild') }}</button>
      <SubagentSheet :key="`${verifiedSession}:${child.childSession}`" :child-session="child.childSession" :parent-session="verifiedSession" :agent-id="child.agentId" :live="active" />
    </div>
    <div v-else class="tr-body conv-inner">
      <p v-if="loading && !snapshot" class="tr-empty">{{ t('taskSessionResult.loading') }}</p>
      <p v-else-if="snapshot && !exchanges.length" class="tr-empty">{{ t('taskSessionResult.empty') }}</p>
      <section v-for="exchange in exchanges" :key="`${verifiedSession}:${exchange.id}`" class="tr-exchange">
        <details v-if="compact && exchange.user" class="tr-input"><summary>{{ t('taskSessionResult.executionInput') }}</summary><div class="tr-user"><OrderedMessageContent v-if="exchange.user.orderedContent" :blocks="exchange.user.orderedContent" /><template v-else>{{ exchange.user.text }}</template></div></details>
        <div v-else-if="exchange.user" class="tr-user"><OrderedMessageContent v-if="exchange.user.orderedContent" :blocks="exchange.user.orderedContent" /><template v-else>{{ exchange.user.text }}</template></div>
        <WorkLog :exchange="exchange" :run-active="active" :active="active && !exchange.finished" :live="active && !exchange.finished ? snapshot?.live : undefined" :skills="[]"
          @decide="decide" @freeze="gateClock($event, true)" @thaw="gateClock($event, false)" @open-sheet="openChild" />
      </section>
    </div>
    <div v-if="gates.length || approving" class="tr-approvals">
      <p v-if="approving" role="status">{{ t('taskSessionResult.approvalBusy') }}</p>
      <ApprovalBar :gates="gates" @decide="decide" @freeze="gateClock($event, true)" @thaw="gateClock($event, false)" />
    </div>
  </section>
</template>

<style scoped>
.tr-input{margin:0 0 7px;font-size:11px;color:var(--el-text-color-secondary)}.tr-input>summary{cursor:pointer;padding:3px 0}.tr-input .tr-user{margin-top:6px;font-size:11px}
.tr-context{margin-left:auto}.task-session-result.compact .tr-connection:has(.tr-context){min-height:34px}
.tr-connection{display:flex;align-items:center;gap:8px;flex:none;margin:0 0 8px;font-size:11px;line-height:1.5;color:var(--el-text-color-secondary)}.tr-connection.is-offline{color:var(--el-color-warning)}.tr-reconnect{font:inherit;color:var(--el-color-primary);background:none;border:1px solid var(--el-border-color);border-radius:5px;padding:2px 6px;cursor:pointer}.task-session-result.compact .tr-connection{margin:0 0 5px}.task-session-result.compact .tr-body{padding:2px 0 12px}.task-session-result.compact .tr-exchange+.tr-exchange{margin-top:12px;padding-top:12px}
.task-session-result{display:flex;flex-direction:column;flex:1;min-height:0;height:100%;min-width:0}.tr-header{display:flex;align-items:center;gap:12px;padding:20px 0 10px;flex:none;flex-wrap:wrap}.tr-title{flex:1;min-width:100px}.tr-title small{font-size:11px;color:var(--el-text-color-secondary)}.tr-title h2{margin:4px 0;font-size:18px;line-height:1.5;overflow-wrap:anywhere}.tr-back,.tr-stop,.tr-refresh{display:inline-flex;align-items:center;gap:5px;border:1px solid var(--el-border-color);background:var(--el-bg-color);color:var(--el-text-color-regular);border-radius:7px;padding:7px 9px;font:inherit;font-size:12px;cursor:pointer}.tr-refresh{padding:7px}.tr-stop{color:var(--el-color-danger)}button:disabled{opacity:.5;cursor:default}.tr-note,.tr-empty{font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.tr-note{margin:0 0 10px}.tr-error{margin:0 0 10px;font-size:12px;line-height:1.7;color:var(--el-color-danger)}.tr-body{flex:1;min-height:0;overflow:auto;overscroll-behavior:contain;padding:8px 0 24px}.tr-exchange+.tr-exchange{border-top:1px solid var(--el-border-color-lighter);margin-top:20px;padding-top:18px}.tr-user{font-size:13px;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere;background:var(--el-fill-color-lighter);padding:10px 12px;border-radius:8px}.tr-approvals{flex:none;padding:8px 0;border-top:1px solid var(--el-border-color-lighter)}.tr-approvals>p{font-size:12px;color:var(--el-text-color-secondary);margin:4px 0 8px}.tr-child{flex:1;min-height:0;display:flex;flex-direction:column;gap:8px}.tr-child>.tr-back{align-self:flex-start}.tr-child>:last-child{flex:1;min-height:0}
</style>
