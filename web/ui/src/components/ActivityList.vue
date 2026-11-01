<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import ThinkingBlock from "./ThinkingBlock.vue";
import ToolCall from "./ToolCall.vue";
import AnswerText from "./AnswerText.vue";
import AnswerReviewStatus from "./AnswerReviewStatus.vue";
import SourcesPanel from "./SourcesPanel.vue";
import SteeringMessage from "./SteeringMessage.vue";
import IncomingArgs from "./tools/IncomingArgs.vue";
import WorkLogGlyph from "./WorkLogGlyph.vue";
import Chevron from "./Chevron.vue";
import { activityCallNeedsAttention, activityDisclosureKey, buildActivityProjection, legacyActivityRowId, type ActivityToolGroup } from "@/agent/activityProjection";
import { useCardDisclosureState } from "@/agent/cardDisclosure";
import { answerReviewNoticeText, showAnswerReviewNotice } from "@/agent/answerReview";
import { summarizeArgs, type TimelineCall, type TurnItem } from "@/agent/timeline";
import { toolCallState } from "@/agent/toolPresentation";
import { worklogRevealRow } from "@/agent/worklogPrefs";
import type { Exchange } from "@/agent/exchanges";
import type { OpenSheetRequest } from "./dockSheets";

const props = defineProps<{
  exchange: Exchange;
  runActive: boolean;
  workFolded?: boolean;
  skills?: { name: string; path: string }[];
  cwd?: string;
  todoPinned?: boolean;
  serverOffset?: number | null;
  revealId?: string;
}>();
const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string]; thaw: [string];
  "open-sheet": [OpenSheetRequest];
  "open-file": [{ path: string; name: string }];
}>();

const { t } = useI18n();
const disclosure = useCardDisclosureState();
const localOpen = ref<ReadonlyMap<string, boolean>>(new Map());
const nodes = computed(() => buildActivityProjection(props.exchange));
const groups = computed(() => nodes.value.flatMap(node => node.kind === "turn" ? node.groups : []));
const groupIndex = computed(() => new Map(groups.value.map(group => [group.id, group])));
const automaticOpen = ref<ReadonlyMap<string, boolean>>(new Map());
watch(groups, value => {
  const next = new Map(automaticOpen.value);
  let changed = false;
  for (const group of value) {
    const key = activityDisclosureKey(props.exchange.id, group.id);
    if (next.has(key)) continue;
    next.set(key, props.runActive && !props.exchange.finished);
    changed = true;
  }
  if (changed) automaticOpen.value = next;
}, { immediate: true, flush: "sync" });
const summaries = computed(() => new Map(groups.value.map(group => {
  const counts = { running: 0, gate: 0, error: 0, orphaned: 0, done: 0 };
  for (const call of group.calls) counts[toolCallState(call)]++;
  return [group.id, { ...counts, target: summarizeArgs(group.calls[0].name, group.calls[0].args) }];
})));
const summary = (group: ActivityToolGroup) => summaries.value.get(group.id)!;

function isGroupOpen(group: ActivityToolGroup): boolean {
  const key = activityDisclosureKey(props.exchange.id, group.id);
  if (disclosure?.available()) {
    const saved = disclosure.choice(key) ?? disclosure.choice(activityDisclosureKey(props.exchange.id, legacyActivityRowId(group)));
    if (saved !== undefined) return saved;
    return disclosure.hasOpen(group.calls.map(call => `call:${call.callId}:details`)) || automaticOpen.value.get(key) === true;
  }
  return localOpen.value.get(key) ?? automaticOpen.value.get(key) ?? false;
}

function setGroupOpen(group: ActivityToolGroup, open: boolean) {
  const key = activityDisclosureKey(props.exchange.id, group.id);
  if (disclosure?.available()) {
    disclosure.set(key, open);
    if (open) disclosure.set(`work:${props.exchange.id}`, true);
  } else localOpen.value = new Map(localOpen.value).set(key, open);
}

const grouped = (group: ActivityToolGroup) => group.explore && group.calls.length > 1;
const needsAttention = (call: TimelineCall) => activityCallNeedsAttention(call, props.exchange.finished);
const visibleGroup = (group: ActivityToolGroup) => !props.workFolded || group.calls.some(needsAttention);
const visibleCall = (group: ActivityToolGroup, call: TimelineCall) => needsAttention(call) || (!props.workFolded && (!grouped(group) || isGroupOpen(group)));
// The outer fold covers the whole process, including narration and its source
// overviews. Keep the actual conclusion and live work outside that fold. Using
// v-show below retains every mounted row and its disclosure/scroll state.
const visibleProse = (turn: TurnItem) => !props.workFolded || (props.exchange.finished && turn.id === props.exchange.answer?.id) || turn.streaming;
const visibleTurn = (turn: TurnItem) => !props.workFolded ||
  (visibleProse(turn) && !!turn.text) || turn.streaming || !!turn.incoming?.length ||
  turn.answerReview?.state === "running" || turn.calls.some(needsAttention);

function revealRow(value: string) {
  const prefix = props.revealId ? `${props.revealId}:` : "";
  if (prefix && value.startsWith("wl-body-") && !value.startsWith(prefix)) return;
  const rowId = prefix && value.startsWith(prefix) ? value.slice(prefix.length) : value;
  const group = groupIndex.value.get(rowId) ?? groups.value.find(item => legacyActivityRowId(item) === rowId || item.calls.some(call => call.callId === rowId));
  if (group) { setGroupOpen(group, true); return; }
  const turn = props.exchange.turns.find(turn => `${turn.id}:thinking` === rowId);
  if (turn) { disclosure?.set(`${turn.id}:thinking`, true); disclosure?.set(`work:${props.exchange.id}`, true); }
}
watch(worklogRevealRow, revealRow);
</script>

<template>
  <div class="activity-list">
    <template v-for="node in nodes" :key="node.id">
      <SteeringMessage v-if="node.kind === 'steering'" :message="node.message" @open-file="emit('open-file', $event)" />
      <div v-else-if="node.kind === 'notice' && showAnswerReviewNotice(node.notice.text, exchange.turns)" v-show="!workFolded" class="activity-notice" :data-mid="node.notice.id">{{ answerReviewNoticeText(node.notice.text) }}</div>
      <div v-else-if="node.kind === 'turn'" v-show="visibleTurn(node.turn)" class="turn activity-turn" :class="{ streaming: node.turn.streaming, folded: workFolded }" :data-mid="node.turn.id">
        <AnswerReviewStatus v-if="node.turn.answerReview" v-show="visibleProse(node.turn) || node.turn.answerReview.state === 'running'" :review="node.turn.answerReview" :server-offset="serverOffset" />
        <div v-show="!workFolded" class="head turn-head">
          <span class="index">{{ t('turnCard.turnIndex', { n: node.turn.index }) }}</span>
          <span v-if="node.turn.streaming" class="live">{{ t('turnCard.streaming') }}</span>
        </div>
        <div v-if="node.turn.thinking" v-show="!workFolded || node.turn.streaming" class="thinking-row" :data-rid="`${node.turn.id}:thinking`">
          <ThinkingBlock :id="node.turn.id" :text="node.turn.thinking" :streaming="node.turn.streaming" :duration-ms="node.turn.thinkingMs" :compact="!!workFolded" />
        </div>
        <div
          v-for="group in node.groups"
          :key="group.id"
          v-show="visibleGroup(group)"
          class="activity-group"
          :class="{ grouped: grouped(group), open: isGroupOpen(group), bad: summary(group).error > 0 }"
          :data-rid="group.id"
          :data-cite-labels="group.citeLabels.join(' ') || undefined"
          @pogo-reveal-tool="setGroupOpen(group, true)"
        >
          <button v-if="grouped(group)" type="button" class="activity-group-head" :aria-expanded="isGroupOpen(group)" :aria-controls="`${group.id}:calls`" @click="setGroupOpen(group, !isGroupOpen(group))">
            <span class="group-glyph"><WorkLogGlyph kind="explore" /></span>
            <span class="group-name">{{ t('activityGroup.title', { n: group.calls.length }) }}</span>
            <span v-if="summary(group).target" class="group-target" :title="summary(group).target">{{ summary(group).target }}</span>
            <span class="group-statuses">
              <span v-if="summary(group).running" class="group-status running" role="status">{{ t('activityGroup.running', { n: summary(group).running }) }}</span>
              <span v-if="summary(group).gate" class="group-status gate" role="status">{{ t('activityGroup.gate', { n: summary(group).gate }) }}</span>
              <span v-if="summary(group).error" class="group-status error" role="status">{{ t('activityGroup.error', { n: summary(group).error }) }}</span>
              <span v-if="summary(group).orphaned" class="group-status incomplete" role="status">{{ t('activityGroup.incomplete', { n: summary(group).orphaned }) }}</span>
            </span>
            <Chevron class="group-chevron" :open="isGroupOpen(group)" />
          </button>
          <div :id="`${group.id}:calls`" class="group-calls">
            <ToolCall
              v-for="call in group.calls"
              :key="call.callId"
              v-show="visibleCall(group, call)"
              :call="call"
              :run-active="runActive"
              :skills="skills"
              :cwd="cwd"
              :todo-pinned="todoPinned"
              :server-offset="serverOffset"
              @suggest="emit('suggest', $event)"
              @decide="emit('decide', $event)"
              @freeze="emit('freeze', $event)"
              @thaw="emit('thaw', $event)"
              @open-sheet="emit('open-sheet', $event)"
            />
          </div>
        </div>
        <IncomingArgs v-for="incoming in node.turn.incoming ?? []" :key="incoming.call_id" :incoming="incoming" />
        <AnswerText v-if="node.turn.text" v-show="visibleProse(node.turn)" :source="node.turn.text" :streaming="node.turn.streaming" :sources="node.turn.sources" />
        <SourcesPanel v-if="node.turn.sources?.length" v-show="visibleProse(node.turn)" :sources="node.turn.sources" :text="node.turn.text" />
      </div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.activity-turn { border-left: 2px solid var(--el-border-color-lighter); padding: 6px 0 8px 14px; margin: 14px 0; }
.activity-turn.streaming { border-left-color: var(--el-color-primary); }
.activity-turn.folded { border-left: 0; padding: 0; margin: 12px 0 0; }
.turn-head { display: flex; align-items: center; gap: 8px; color: var(--el-text-color-placeholder); font-size: 11px; font-variant-numeric: tabular-nums; }
.live { color: var(--el-color-primary); }
.activity-group { min-width: 0; }
.activity-group.grouped { margin: 7px 0; border-left: 1px solid var(--el-border-color-lighter); padding-left: 9px; }
.activity-group-head { display: flex; flex-wrap: wrap; gap: 7px; align-items: baseline; width: 100%; min-width: 0; padding: 4px 3px; border: 0; border-radius: 5px; background: transparent; color: var(--el-text-color-primary); text-align: left; font-size: 12px; cursor: pointer; }
.activity-group-head:hover { background: var(--el-fill-color-light); }
.activity-group-head:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.group-glyph { width: 14px; height: 14px; flex: 0 0 14px; align-self: center; color: var(--el-text-color-secondary); }
.group-name { flex: 0 0 auto; font-weight: 600; }
.group-target { min-width: 0; flex: 1 1 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--el-text-color-secondary); font-family: var(--pg-mono); }
.group-chevron { flex: 0 0 auto; margin-left: auto; align-self: center; color: var(--el-text-color-secondary); }
.group-statuses { display: flex; flex-wrap: wrap; gap: 5px; font-size: 11px; }
.group-status.running { color: var(--el-color-primary); }
.group-status.gate { color: var(--el-color-warning); }
.group-status.error, .group-status.incomplete { color: var(--el-color-danger); }
.group-calls { min-width: 0; }
.activity-notice { margin: 8px 0; color: var(--el-text-color-secondary); font-size: 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
