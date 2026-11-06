<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { InfoFilled } from "@element-plus/icons-vue";
import RollingLine from "./RollingLine.vue";
import TurnCard from "./TurnCard.vue";
import SteeringMessage from "./SteeringMessage.vue";
import OrderedMessageContent from "./OrderedMessageContent.vue";
import { api } from "@/api/client";
import { normalizeMessages } from "@/api/messageContent";
import { buildTimeline, transcriptAction, transcriptSettled } from "@/agent/timeline";
import { buildExchanges } from "@/agent/exchanges";
import { provideCardDisclosure } from "@/agent/cardDisclosure";
import { provideToolIdentityCatalog } from "@/agent/toolIdentity";
import { emptyLive, type Message, type ToolResult } from "@/api/types";

/**
 * One delegated run, read as its own conversation.
 *
 * The data comes from the transcript endpoint, which re-reads the child's
 * session file off disk on every call: the in-memory session the manager would
 * adopt is frozen at open time, and a still-running child keeps appending, so
 * polling the file is the only view that follows it.
 *
 * Polling stops on the transcript's own evidence, not on the parent's say-so:
 * the `live` prop is session-scoped, and a tab outlives the session switch
 * that would silence it. transcriptSettled reads the file's tail instead — a
 * run's file never ends on a finished answer while the child is still going —
 * so the sheet keeps up with a run it opened mid-flight even when nobody is
 * watching the parent session. Brisk (2s) while the parent says live, slow
 * (10s) otherwise: an orphaned child that never settles costs one small read.
 *
 * Rendering reuses the parent timeline's own components — the child speaks the
 * same event contract one level down, so nothing here needs a second
 * implementation. Everything is read-only: a sheet is a window into the run,
 * not a way to steer it.
 */
const props = defineProps<{
  /** The child transcript's session id (its file basename minus .jsonl). */
  childSession: string;
  /** Parent captured by the user-facing dock; not accepted from tool args. */
  parentSession?: string;
  agentId?: string;
  /** live while the parent says the delegation is still running: brisk
   * polling, and a trailing fetch when it flips. Not the stop signal — the
   * transcript's own shape is. */
  live: boolean;
}>();

const { t } = useI18n();

const messages = ref<Message[]>([]);
const displaySessionId = ref<string | undefined>();
// This dock lives inside the parent AgentView, but reads another transcript.
// Shadow its provider even before the authenticated response gives us an id.
provideCardDisclosure(() => displaySessionId.value ? `subagent:${displaySessionId.value}` : undefined);
provideToolIdentityCatalog(() => displaySessionId.value);
const results = ref<Record<string, ToolResult>>({});
/** missing distinguishes "never answered" (404: gone or not yet created) from
 * an empty transcript. */
const missing = ref(false);
const loaded = ref(false);

// A transcript sheet has no live run of its own in this process: the settled
// projection is the honest one.
const timeline = computed(() => buildTimeline(messages.value, results.value, emptyLive(), {}));
const exchanges = computed(() => buildExchanges(timeline.value, false));
const settled = computed(() => transcriptSettled(messages.value));
// The footer's one line: what the run is doing right now, deduced from the
// transcript's tail. Null once settled — a finished run's footer goes away
// rather than freezing on its last act.
const action = computed(() => transcriptAction(messages.value, results.value));

let fetchEpoch = 0;
let disposed = false;
async function fetchTranscript() {
  const epoch = fetchEpoch;
  try {
    if (props.agentId && !props.parentSession) throw new Error("Retained child has no parent binding");
    const res = props.agentId && props.parentSession
      ? await api.retainedSubagentTranscript(props.parentSession, props.agentId)
      : await api.transcript(props.childSession);
    if (epoch !== fetchEpoch) return;
    displaySessionId.value = res.session_id || undefined;
    messages.value = normalizeMessages(res.snapshot.messages);
    results.value = res.snapshot.results;
    missing.value = false;
  } catch {
    // A 404 is expected in the first seconds of a run — the child creates the
    // file when it starts, not when the card did — so while live this is a
    // retry, not an error. Settled and still missing is when it reads as gone.
    if (epoch === fetchEpoch) missing.value = true;
  } finally {
    if (epoch === fetchEpoch) loaded.value = true;
  }
}

let timer: ReturnType<typeof setInterval> | undefined;
function stopPolling() {
  if (timer !== undefined) {
    clearInterval(timer);
    timer = undefined;
  }
}

function startPolling() {
  if (disposed) return;
  stopPolling();
  timer = setInterval(() => {
    if (settled.value) {
      stopPolling();
      return;
    }
    void fetchTranscript();
  }, props.live ? 2000 : 10_000);
}

watch(
  () => props.live,
  async (live, was) => {
    if (live) {
      startPolling();
    } else if (was) {
      // The run just settled: the file's last writes race the parent's "done",
      // so one trailing fetch is what keeps the sheet from ending a beat early.
      await fetchTranscript();
      if (!settled.value && !missing.value) startPolling();
    }
  },
);

onMounted(() => {
  void fetchTranscript().then(() => {
    // A missing transcript with no live run behind it is gone, not pending:
    // polling it would ask the same 404 the same question forever.
    if (missing.value && !props.live) return;
    if (!settled.value) startPolling();
  });
});
watch(() => [props.childSession, props.parentSession, props.agentId], () => {
  fetchEpoch++; stopPolling(); messages.value = []; results.value = {}; displaySessionId.value = undefined; loaded.value = false; missing.value = false;
  void fetchTranscript().then(() => { if (!settled.value && (!missing.value || props.live)) startPolling(); });
});
onBeforeUnmount(() => { disposed = true; fetchEpoch++; stopPolling(); });

// The terminal rule: follow the tail while the reader is at it; never undo a
// scroll-up on a poll tick.
const bodyEl = ref<HTMLElement | null>(null);
watch(
  () => messages.value.length,
  async () => {
    const el = bodyEl.value;
    if (!el) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight >= 120) return;
    await nextTick();
    el.scrollTop = el.scrollHeight;
  },
);
</script>

<template>
  <div class="subwrap">
    <div class="subsheet" ref="bodyEl">
      <div v-if="!loaded" class="state">{{ t("subagentSheet.loading") }}</div>
      <div v-else-if="missing" class="state">
        {{ live ? t("subagentSheet.starting") : t("subagentSheet.unavailable") }}
      </div>
      <div v-else-if="!exchanges.length" class="state">{{ t("subagentSheet.empty") }}</div>
      <template v-else v-for="ex in exchanges" :key="`${displaySessionId ?? 'unbound'}:${ex.id}`">
        <!-- The brief the parent delegated, as the first message of the child's
             own conversation — which is exactly what it is. -->
        <div v-if="ex.user" class="brief"><OrderedMessageContent v-if="ex.user.orderedContent" :blocks="ex.user.orderedContent" /><template v-else>{{ ex.user.text }}</template></div>
        <SteeringMessage v-for="item in (ex.steering ?? []).filter(item => !item.afterTurnId)" :key="item.id" :message="item" />
        <template v-for="turn in ex.turns" :key="turn.id">
        <TurnCard
          :turn="turn"
          :run-active="false"
          :skills="[]"
          cwd=""
          :todo-pinned="false"
        />
        <SteeringMessage v-for="item in (ex.steering ?? []).filter(item => item.afterTurnId === turn.id)" :key="item.id" :message="item" />
        </template>
        <!-- Harness nudges (the soft-cap checkpoint, the todo nudge): one quiet
             system line each, never a bubble, and they no longer split the
             exchange — the turn numbering runs straight through them. -->
        <div v-for="n in ex.notices" :key="n.id" class="notice" :title="n.text">
          <el-icon class="nico"><InfoFilled /></el-icon>
          <span class="ntext">{{ n.text }}</span>
        </div>
      </template>
    </div>
    <!-- The run's present tense, pinned under the transcript: the reader
         scrolling a long history still sees what the child is doing. Goes away
         when the run settles rather than freezing on its last act. -->
    <div v-if="action" class="subfoot">
      <span class="dotlive" aria-hidden="true" />
      <RollingLine class="foot-line" :text="action" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.subwrap {
  height: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.subsheet {
  flex: 1 1 0;
  min-height: 0;
  overflow: auto;
  padding: 12px 16px;
  background: var(--el-bg-color);
}

/* The footer mirrors the subagent card's own live strip: same pulsing dot,
   same monospace line, so the two views agree on what "running" looks like. */
.subfoot {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 7px 16px;
  border-top: 1px solid var(--el-border-color-extra-light);
  background: var(--el-bg-color-page);
}

.foot-line {
  flex: 1 1 0;
  min-width: 0;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--el-text-color-primary);
}

.dotlive {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--el-color-primary);
  flex: 0 0 auto;
  animation: rowpulse 1.2s ease-in-out infinite;
}

@keyframes rowpulse {
  50% {
    opacity: 0.4;
  }
}

@media (prefers-reduced-motion: reduce) {
  .dotlive {
    animation: none;
  }
}

.state {
  padding: 24px 0;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

/* The delegated brief, set off from the turns it produced: this is the one
   message the parent wrote into this conversation. */
.brief {
  margin: 4px 0 10px;
  padding: 8px 12px;
  border-radius: 8px;
  background: var(--el-fill-color-light);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

/* A harness nudge: one quiet system line, icon + muted small text — it must
   not read as something anyone typed. */
.notice {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: 6px 0;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  .nico {
    font-size: 11px;
    flex: 0 0 auto;
    position: relative;
    top: 1px;
  }

  .ntext {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}
</style>
