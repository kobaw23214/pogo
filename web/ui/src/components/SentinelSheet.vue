<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import { sentinelSessionsApi } from "@/api/sentinelSessions";
import { buildTimeline, transcriptAction } from "@/agent/timeline";
import { buildExchanges } from "@/agent/exchanges";
import { taskCenterDate } from "@/agent/taskCenterPresentation";
import { createSentinelSessionFeed, sentinelSessionState } from "@/agent/sentinelSessionView";
import { createAnswerFileNavigation } from "@/agent/answerFileNavigation";
import { approvalUiKey } from "@/agent/approvalState";
import { provideCardDisclosure } from "@/agent/cardDisclosure";
import { provideToolIdentityCatalog } from "@/agent/toolIdentity";
import { workspaceFileNavigationKey, type WorkspaceFileScope } from "@/agent/workspaceFileNavigation";
import TurnCard from "./TurnCard.vue";
import SteeringMessage from "./SteeringMessage.vue";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import ApprovalBar from "./ApprovalBar.vue";
import RollingLine from "./RollingLine.vue";
import AnswerFilePicker from "./AnswerFilePicker.vue";

const props = defineProps<{ sentinelId: string; parentSession?: string; agentId?: string; visible: boolean }>();
const emit = defineEmits<{
  openFile: [request: { path: string; name: string; lineStart?: number; lineEnd?: number; sourceSessionId: string; parentSessionId: string }];
}>();
const { t, locale } = useI18n();
const parentNavigation = inject(workspaceFileNavigationKey, null);
const parentApproval = inject(approvalUiKey, undefined);
const binding = () => ({ sentinelId: props.sentinelId, parentSession: props.parentSession, agentId: props.agentId, visible: props.visible });
const feed = createSentinelSessionFeed({ binding, fetch: sentinelSessionsApi.detail, errorMessage: () => t("sentinelSessions.unavailable") });
const detail = feed.detail, error = feed.error, loading = feed.loading;
// The verified worker snapshot owns this view, not the parent conversation
// selected behind the retained tab. An unbound worker gets local defaults.
provideCardDisclosure(() => detail.value?.session?.id ? `sentinel:${detail.value.session.id}` : undefined);
provideToolIdentityCatalog(() => detail.value?.session?.id);
const snapshot = computed(() => detail.value?.session?.snapshot ?? null);
const gates = computed(() => snapshot.value?.live.pending_gates ?? []);
const timeline = computed(() => {
  const value = snapshot.value;
  return value ? buildTimeline(value.messages, value.results, value.live, Object.fromEntries((value.bg_tasks ?? []).map(task => [task.task_id, task])), value.answer_review) : [];
});
const exchanges = computed(() => {
  let batch = 0;
  return buildExchanges(timeline.value, snapshot.value?.run.active ?? false).map(exchange => ({
    ...exchange,
    batch: exchange.user?.text.startsWith("[Pogo 哨兵任务]") ? ++batch : 0,
  }));
});
const active = computed(() => !!snapshot.value?.run.active);
const state = computed(() => detail.value ? sentinelSessionState(detail.value) : "listening");
const action = computed(() => snapshot.value ? transcriptAction(snapshot.value.messages, snapshot.value.results) : null);
const date = (value: number) => taskCenterDate(value, locale.value);

// An old tab can outlive the currently selected main conversation. File
// navigation is offered only while the verified parent is actually selected,
// with the same cwd. This keeps Office's common opener bound to that parent.
function fileScope(): WorkspaceFileScope | null {
  const value = detail.value, parent = parentNavigation?.scope();
  if (!props.visible || !props.parentSession || !value?.session || !parent ||
      value.watch.parent_session_id !== props.parentSession || parent.conversationId !== props.parentSession || parent.cwd !== value.session.cwd) return null;
  return { ...parent, conversationId: value.session.id, cwd: value.session.cwd };
}
const files = createAnswerFileNavigation({
  scope: fileScope,
  resolve: api.resolveSessionFileLink,
  failureMessage: () => t("sentinelSessions.fileFailed"),
  openFile: target => {
    const scope = fileScope();
    if (!scope?.root || !props.parentSession) return;
    const path = target.path.startsWith("/") ? target.path : `${scope.root.replace(/\/$/, "")}/${target.path}`;
    emit("openFile", { ...target, path, sourceSessionId: scope.conversationId, parentSessionId: props.parentSession });
  },
});
const fileChoice = files.choice;
provide(workspaceFileNavigationKey, { scope: fileScope, open: files.open });
watch(() => {
  const scope = fileScope();
  return scope ? JSON.stringify([scope.conversationId, scope.cwd, scope.workspace, scope.root]) : "";
}, () => files.cancel(), { flush: "sync" });

const approvalPending = ref<Record<string, boolean>>({});
const approvalErrors = ref<Record<string, string>>({});
const approvalError = ref("");
const approving = computed(() => Object.values(approvalPending.value).some(Boolean));
let approvalEpoch = 0;
provide(approvalUiKey, {
  pending: approvalPending, errors: approvalErrors,
  activeGateIds: computed(() => new Set(props.visible ? gates.value.map(gate => gate.gate_id) : [])),
  sessionId: computed(() => detail.value?.session?.id ?? null),
  serverOffset: computed(() => parentApproval?.serverOffset.value ?? null),
  canDecide: parentApproval?.canDecide ?? true,
});
function approvalContext(gateId: string) {
  const value = detail.value;
  if (!props.visible || !value?.session || approvalPending.value[gateId] || !gates.value.some(gate => gate.gate_id === gateId)) return null;
  return { sid: value.session.id, key: JSON.stringify([props.sentinelId, props.parentSession, props.agentId, value.session.id]), epoch: approvalEpoch };
}
function currentApproval(context: { sid: string; key: string; epoch: number }) {
  return approvalEpoch === context.epoch && JSON.stringify([props.sentinelId, props.parentSession, props.agentId, detail.value?.session?.id]) === context.key;
}
async function decide(event: { gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }) {
  const context = approvalContext(event.gateId);
  if (!context) return;
  approvalPending.value = { ...approvalPending.value, [event.gateId]: true };
  approvalErrors.value = { ...approvalErrors.value, [event.gateId]: "" };
  approvalError.value = "";
  try {
    await api.decideGate(context.sid, event.gateId, event.allow, { args: event.args, remember: event.remember });
    if (currentApproval(context) && props.visible) await feed.refresh();
  } catch {
    if (currentApproval(context)) {
      approvalError.value = t("sentinelSessions.decisionFailed");
      approvalErrors.value = { ...approvalErrors.value, [event.gateId]: approvalError.value };
    }
  } finally {
    if (currentApproval(context)) approvalPending.value = { ...approvalPending.value, [event.gateId]: false };
  }
}
async function gateClock(gateId: string, frozen: boolean) {
  const context = approvalContext(gateId);
  if (!context) return;
  approvalPending.value = { ...approvalPending.value, [gateId]: true };
  approvalErrors.value = { ...approvalErrors.value, [gateId]: "" };
  approvalError.value = "";
  try {
    if (frozen) await api.freezeGate(context.sid, gateId);
    else await api.thawGate(context.sid, gateId);
    if (currentApproval(context) && props.visible) await feed.refresh();
  } catch {
    if (currentApproval(context)) {
      approvalError.value = t("sentinelSessions.decisionFailed");
      approvalErrors.value = { ...approvalErrors.value, [gateId]: approvalError.value };
    }
  } finally {
    if (currentApproval(context)) approvalPending.value = { ...approvalPending.value, [gateId]: false };
  }
}

watch(() => [props.sentinelId, props.parentSession, props.agentId, props.visible], () => {
  files.cancel();
  void feed.sync();
}, { immediate: true, flush: "sync" });
watch(() => JSON.stringify([props.sentinelId, props.parentSession, props.agentId, detail.value?.session?.id]), () => {
  ++approvalEpoch; approvalPending.value = {}; approvalErrors.value = {}; approvalError.value = "";
}, { flush: "sync" });
onBeforeUnmount(() => { feed.dispose(); files.cancel(); });

const body = ref<HTMLElement | null>(null);
watch(() => [snapshot.value?.seq, snapshot.value?.messages.length], async () => {
  const element = body.value;
  if (!element || element.scrollHeight - element.scrollTop - element.clientHeight >= 120) return;
  await nextTick();
  if (element === body.value) element.scrollTop = element.scrollHeight;
});
</script>

<template>
  <section class="sentinel-sheet" :aria-label="t('sentinelSessions.title')" data-testid="sentinel-sheet">
    <header class="ss-header">
      <div class="ss-heading"><small>{{ t('sentinelSessions.title') }}</small><h2>{{ detail?.watch.title || t('sentinelSessions.title') }}</h2></div>
      <span v-if="detail" class="ss-state" :class="state">{{ t(`sentinelSessions.state_${state}`) }}</span>
      <button type="button" class="ss-refresh" :disabled="loading" :aria-label="t('sentinelSessions.refresh')" @click="feed.refresh()"><el-icon :class="{ 'is-loading': loading }"><Refresh /></el-icon></button>
    </header>
    <div v-if="detail" class="ss-context">
      <span>{{ t('sentinelSessions.retained') }}</span>
      <span v-if="detail.watch.pending_events">{{ t('sentinelSessions.pending', { n: detail.watch.pending_events }) }}</span>
      <span v-if="detail.watch.expires_at">{{ t('sentinelSessions.expires') }} {{ date(detail.watch.expires_at) }}</span>
      <span v-if="detail.watch.parent_session_id" class="ss-parent" :title="detail.watch.parent_session_id">{{ t('sentinelSessions.parent') }} · {{ detail.watch.parent_session_id }}</span>
      <span v-else>{{ t('sentinelSessions.legacyParent') }}</span>
    </div>
    <div v-if="error" class="ss-error" role="alert">{{ error }} <button type="button" @click="feed.refresh()">{{ t('sentinelSessions.retry') }}</button></div>
    <div ref="body" class="sentinel-session-body conv-inner">
      <div v-if="!detail && loading" class="ss-empty">{{ t('sentinelSessions.loading') }}</div>
      <div v-else-if="detail && !detail.session" class="ss-empty"><strong>{{ t(detail.watch.enabled ? 'sentinelSessions.emptyTitle' : 'sentinelSessions.pausedEmptyTitle') }}</strong><p>{{ t(detail.watch.enabled ? 'sentinelSessions.emptyHint' : 'sentinelSessions.pausedEmptyHint') }}</p></div>
      <div v-else-if="detail && !exchanges.length" class="ss-empty">{{ t('sentinelSessions.noMessages') }}</div>
      <section v-for="exchange in exchanges" :key="`${detail?.session?.id ?? 'unbound'}:${exchange.id}`" class="ss-exchange">
        <details v-if="exchange.batch && exchange.user" class="ss-batch"><summary>{{ t('sentinelSessions.batch', { n: exchange.batch }) }} <span>{{ t('sentinelSessions.inspectBatch') }}</span></summary><pre>{{ exchange.user.text }}</pre></details>
        <div v-else-if="exchange.user" class="ss-user"><OrderedMessageContent v-if="exchange.user.orderedContent" :blocks="exchange.user.orderedContent" /><template v-else>{{ exchange.user.text }}</template></div>
        <SteeringMessage v-for="item in (exchange.steering ?? []).filter(item => !item.afterTurnId)" :key="item.id" :message="item" />
        <template v-for="turn in exchange.turns" :key="turn.id">
          <TurnCard :turn="turn" :run-active="active" :skills="[]" :cwd="detail?.session?.cwd || ''" :todo-pinned="false" @decide="decide" @freeze="gateClock($event, true)" @thaw="gateClock($event, false)" />
          <SteeringMessage v-for="item in (exchange.steering ?? []).filter(item => item.afterTurnId === turn.id)" :key="item.id" :message="item" />
        </template>
        <div v-for="notice in exchange.notices" :key="notice.id" class="ss-notice">{{ notice.text }}</div>
      </section>
    </div>
    <div v-if="active && action" class="ss-live"><span class="ss-live-dot" aria-hidden="true" /><RollingLine :text="action" /></div>
    <div v-if="gates.length || approvalError || approving" class="ss-approval">
      <p v-if="approvalError" class="ss-approval-error" role="alert">{{ approvalError }}</p>
      <p v-if="approving" class="ss-approval-busy" role="status">{{ t('sentinelSessions.approvalBusy') }}</p>
      <ApprovalBar :gates="gates" @decide="decide" @freeze="gateClock($event, true)" @thaw="gateClock($event, false)" />
    </div>
    <AnswerFilePicker :choice="fileChoice" @choose="files.choose" @close="files.cancel" />
  </section>
</template>

<style scoped>
.sentinel-sheet{height:100%;min-height:0;display:flex;flex-direction:column;background:var(--el-bg-color);color:var(--el-text-color-primary)}
.ss-header{display:flex;align-items:center;gap:10px;padding:13px 16px;border-bottom:1px solid var(--el-border-color-lighter);flex:0 0 auto;min-width:0}.ss-heading{min-width:0;flex:1}.ss-heading small{font-size:10px;color:var(--el-text-color-secondary)}h2{font-size:14px;line-height:1.5;margin:2px 0 0;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.ss-state{font-size:11px;white-space:nowrap;border-radius:20px;padding:3px 8px;background:var(--el-fill-color-light);color:var(--el-text-color-secondary)}.ss-state.listening,.ss-state.running{background:var(--el-color-success-light-9);color:var(--el-color-success-dark-2)}.ss-state.waiting_human,.ss-state.blocked,.ss-state.waiting_capacity{background:var(--el-color-warning-light-9);color:var(--el-color-warning-dark-2)}.ss-state.error{background:var(--el-color-danger-light-9);color:var(--el-color-danger)}.ss-refresh{display:inline-flex;align-items:center;justify-content:center;border:1px solid var(--el-border-color);border-radius:7px;width:28px;height:28px;background:var(--el-bg-color);color:var(--el-text-color-secondary);cursor:pointer}.ss-refresh:disabled{opacity:.5;cursor:wait}.ss-context{display:flex;flex-wrap:wrap;gap:5px 13px;padding:8px 16px;border-bottom:1px solid var(--el-border-color-extra-light);font-size:11px;line-height:1.65;color:var(--el-text-color-secondary)}.ss-parent{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:10px}.ss-error{padding:9px 16px;background:var(--el-color-danger-light-9);font-size:12px;color:var(--el-color-danger);line-height:1.6}.ss-error button{border:0;background:none;color:inherit;text-decoration:underline;cursor:pointer}.sentinel-session-body{flex:1;min-height:0;overflow:auto;padding:12px 16px;overscroll-behavior:contain}.ss-empty{display:flex;min-height:160px;flex-direction:column;align-items:center;justify-content:center;text-align:center;font-size:12px;line-height:1.8;color:var(--el-text-color-secondary);padding:20px}.ss-empty strong{font-size:14px;color:var(--el-text-color-regular)}.ss-empty p{max-width:420px;margin:6px 0 0}.ss-exchange+.ss-exchange{margin-top:24px;padding-top:16px;border-top:1px solid var(--el-border-color-extra-light)}.ss-batch{padding:8px 10px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-bg-color-page)}.ss-batch summary{cursor:pointer;font-size:12px;font-weight:550;line-height:1.65}.ss-batch summary span{font-size:10px;font-weight:400;color:var(--el-text-color-secondary);margin-left:7px}.ss-batch pre{max-height:240px;margin:10px 0 0;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font:11px/1.65 var(--pg-mono,monospace);color:var(--el-text-color-regular)}.ss-user{padding:10px 12px;border-radius:9px;background:var(--el-fill-color-lighter);font-size:13px;white-space:pre-wrap;overflow-wrap:anywhere}.ss-notice{font-size:11px;color:var(--el-text-color-secondary);line-height:1.65;white-space:pre-wrap;padding:5px 0}.ss-live{display:flex;align-items:center;gap:7px;padding:7px 16px;border-top:1px solid var(--el-border-color-extra-light);font:11px/1.6 var(--pg-mono,monospace);color:var(--el-text-color-secondary);min-width:0}.ss-live-dot{width:6px;height:6px;border-radius:50%;background:var(--el-color-success);flex:none}.ss-approval{padding:8px;flex:0 0 auto;min-height:0;border-top:1px solid var(--el-border-color-lighter)}.ss-approval-error{font-size:12px;line-height:1.6;color:var(--el-color-danger);margin:0 4px 6px}.ss-approval-busy{font-size:11px;color:var(--el-text-color-secondary);margin:0 4px 6px}
</style>
