<script setup lang="ts">
import { computed, onMounted, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import ActivityList from "./ActivityList.vue";
import LiveWorkHeader from "./LiveWorkHeader.vue";
import Chevron from "./Chevron.vue";
import { activityDisclosureKeys } from "@/agent/activityProjection";
import { useCardDisclosureState } from "@/agent/cardDisclosure";
import { toolCallState, presentToolCall } from "@/agent/toolPresentation";
import { formatDuration } from "@/agent/timeline";
import { announce } from "@/agent/announcer";
import { trackWorklog, worklogExpandAll, worklogRevealId, worklogRevealRow } from "@/agent/worklogPrefs";
import type { Exchange } from "@/agent/exchanges";
import type { Live } from "@/api/types";
import type { OpenSheetRequest } from "./dockSheets";

const props = defineProps<{
  exchange: Exchange;
  runActive: boolean;
  live?: Live;
  active?: boolean;
  runStartedAt?: number;
  serverOffset?: number | null;
  /** Compatibility for existing callers: just-finished work starts open, never auto-folds. */
  animateFold?: boolean;
  skills?: { name: string; path: string }[];
  cwd?: string;
  todoPinned?: boolean;
}>();
const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string]; thaw: [string]; "open-sheet": [OpenSheetRequest]; "open-file": [{ path: string; name: string }];
}>();
const { t } = useI18n();
const disclosure = useCardDisclosureState();
const bodyId = `wl-body-${useId()}`;
const head = ref<HTMLButtonElement | null>(null);
const body = ref<HTMLElement | null>(null);
// The initial default belongs to this mounted workspace. Ending a run does not
// replace the component or change that default. History can still start folded.
const defaultOpen = ref(!props.exchange.finished || !props.exchange.answer || props.animateFold === true || worklogExpandAll.value);
const localChoice = ref<boolean>();
const workKey = () => `work:${props.exchange.id}`;
const choice = () => disclosure?.available() ? disclosure.choice(workKey()) : localChoice.value;
const calls = computed(() => props.exchange.turns.flatMap(turn => turn.calls));
const gated = computed(() => calls.value.some(call => !!call.gate || !!call.childGates?.length));
const backgroundRunning = computed(() => calls.value.some(call => call.bgTask?.status === "running"));
const hasOpenDescendant = computed(() => disclosure?.hasOpen(activityDisclosureKeys(props.exchange)) ?? false);
const open = computed({
  get: () => gated.value || backgroundRunning.value || (choice() ?? (defaultOpen.value || hasOpenDescendant.value)),
  set(value: boolean) {
    if (disclosure?.available()) disclosure.set(workKey(), value);
    else localChoice.value = value;
  },
});
const duration = computed(() => {
  const { startedAt, endedAt } = props.exchange;
  return startedAt !== undefined && endedAt !== undefined && endedAt >= startedAt ? formatDuration(endedAt - startedAt) : undefined;
});
const breakdown = computed(() => {
  const names = new Map<string, number>();
  for (const call of calls.value) {
    const name = presentToolCall(call).name;
    names.set(name, (names.get(name) ?? 0) + 1);
  }
  const top = [...names].sort((a, b) => b[1] - a[1]).slice(0, 3).map(([name, n]) => n > 1 ? `${name} (${n})` : name);
  return t("workLog.callsBreakdown", { n: calls.value.length, list: top.join(", ") + (names.size > 3 ? " …" : "") });
});
const failedCount = computed(() => calls.value.filter(call => toolCallState(call) === "error").length);
const incompleteCount = computed(() => calls.value.filter(call => toolCallState(call) === "orphaned").length);
function close() {
  if (gated.value || backgroundRunning.value) return;
  if (body.value?.contains(document.activeElement)) head.value?.focus();
  open.value = false;
}
function toggle() {
  if (open.value) close();
  else { open.value = true; trackWorklog("worklog.manualOpen"); }
}
watch(worklogExpandAll, value => { open.value = value; });
watch(worklogRevealId, id => {
  if (id && (id === bodyId || id === props.exchange.id)) open.value = true;
});
watch(worklogRevealRow, id => {
  if (!id) return;
  if (id.startsWith(`${bodyId}:`)) { open.value = true; return; }
  const belongs = props.exchange.turns.some(turn => id === `${turn.id}:thinking` || turn.calls.some(call =>
    id === call.callId || id === `${call.callId}:explore` || id === `activity:${turn.id}:${call.callId}`));
  if (belongs) open.value = true;
});
watch(() => props.exchange.finished, (ended, before) => {
  if (!ended || before || !duration.value) return;
  announce(t("workLog.doneAnnounce", { d: duration.value }));
});
onMounted(() => trackWorklog("worklog.shown"));
</script>

<template>
  <div class="worklog" :class="{ open, active: active || !exchange.finished }" :data-xid="exchange.id" :data-reveal-id="bodyId" @pogo-reveal-tool="open = true">
    <LiveWorkHeader v-if="active && live" :exchange="exchange" :live="live" :run-started-at="runStartedAt" :server-offset="serverOffset" :folded="!open" @toggle="toggle" />
    <button v-else ref="head" type="button" class="head" :aria-expanded="open" :aria-controls="bodyId" :disabled="gated || backgroundRunning" :aria-label="open ? t('workLog.collapse') : t('workLog.expand')" @click="toggle">
      <Chevron class="chev" :open="open" />
      <span v-if="duration" class="dur">{{ t("workLog.workedFor", { d: duration }) }}</span>
      <span v-if="duration" class="sep" aria-hidden="true">·</span>
      <span class="stat">{{ breakdown }}</span>
      <span v-if="exchange.stats.files" class="files">{{ t("workLog.files", { n: exchange.stats.files }) }}</span>
      <span v-if="failedCount" class="badge bad">{{ t("workLog.failed", { n: failedCount }) }}</span>
      <span v-if="incompleteCount" class="badge incomplete">{{ t("activityGroup.incomplete", { n: incompleteCount }) }}</span>
    </button>
    <!-- The same ActivityList stays mounted while live data settles. It folds
         completed process rows while retaining the conclusion and live work. -->
    <div ref="body" :id="bodyId" class="activity-content">
      <ActivityList :exchange="exchange" :run-active="runActive" :work-folded="!open" :reveal-id="bodyId" :skills="skills" :cwd="cwd" :todo-pinned="todoPinned" :server-offset="serverOffset"
        @suggest="emit('suggest', $event)" @decide="emit('decide', $event)" @freeze="emit('freeze', $event)" @thaw="emit('thaw', $event)" @open-sheet="emit('open-sheet', $event)" @open-file="emit('open-file', $event)" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.worklog { margin: 10px 0 12px; min-width: 0; }
.head { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 9px; width: 100%; min-height: 32px; padding: 5px 8px; border: 1px solid var(--el-border-color-lighter); border-radius: 7px; background: transparent; color: var(--el-text-color-secondary); font: inherit; font-size: 12px; line-height: 1.5; text-align: left; cursor: pointer; }
.head:hover:not(:disabled) { background: var(--el-fill-color-light); }
.head:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.head:disabled { cursor: default; }
.chev { flex: 0 0 auto; color: var(--el-text-color-placeholder); }
.dur { font-variant-numeric: tabular-nums; white-space: nowrap; }
.stat { min-width: 0; overflow-wrap: anywhere; }
.sep { color: var(--el-text-color-placeholder); }
.files { white-space: nowrap; }
.badge { padding: 1px 6px; border-radius: 4px; font-size: 11px; white-space: nowrap; }
.bad { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.incomplete { color: var(--el-color-warning-dark-2); background: var(--el-color-warning-light-9); }
.activity-content { min-width: 0; }
:deep(.livework) { margin-top: 0; min-height: 32px; }
</style>
