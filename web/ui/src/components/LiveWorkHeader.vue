<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { useI18n } from "vue-i18n";
import RollingLine from "./RollingLine.vue";
import Chevron from "./Chevron.vue";
import { formatDuration } from "@/agent/timeline";
import { liveActivity } from "@/agent/liveActivity";
import type { Exchange } from "@/agent/exchanges";
import type { Live } from "@/api/types";

/**
 * The live half of the work log.
 *
 * While a run is in flight its exchange stays flat and alive, and this header
 * carries the two facts worth having at a glance: how long the run has been
 * working, and what it is doing right now (the rolling line — the activity
 * changes roll by, the clock digits replace in place because a number that
 * animates every second is a number that flickers).
 *
 * Foldable, with one hard rule: a pending approval force-expands and disables
 * the fold (the toggle greys out rather than vanishing — a control that
 * disappears exactly when it cannot act reads as a bug). WorkLog retains the
 * same activity list when the clock settles; ending the run never folds it.
 */
const props = defineProps<{
  exchange: Exchange;
  live: Live;
  /** Client-side run start, for exchanges whose user message carries no ts. */
  runStartedAt?: number;
  /**
   * Estimated server−browser clock difference, sampled from the event
   * stream's server-side stamps (see useAgentStream). The clock below runs on
   * estimated server time: the run's start is a server stamp too, and only
   * one shared clock makes the difference of the two immune to a skewed
   * server. Absent means browser time — the pre-estimate behaviour.
   */
  serverOffset?: number | null;
  folded: boolean;
}>();

const emit = defineEmits<{ toggle: [] }>();

const { t } = useI18n();

const gated = computed(() => props.live.pending_gates.length > 0);

const now = ref(Date.now());
const timer = window.setInterval(() => (now.value = Date.now()), 1000);
onBeforeUnmount(() => window.clearInterval(timer));

const start = computed(() => props.exchange.startedAt ?? props.runStartedAt);
const elapsed = computed(() => {
  if (!start.value) return undefined;
  const ms = now.value + (props.serverOffset ?? 0) - start.value;
  return ms >= 0 ? formatDuration(ms) : undefined;
});

const activity = computed(() => liveActivity(props.live));
</script>

<template>
  <button
    class="livework"
    :class="{ folded }"
    :disabled="gated"
    :aria-expanded="!folded"
    :title="gated ? undefined : folded ? t('workLog.showWork') : t('workLog.hideWork')"
    @click="emit('toggle')"
  >
    <span class="pulse" aria-hidden="true"><i /><i /></span>
    <span v-if="elapsed" class="dur">{{ t("workLog.workingFor", { d: elapsed }) }}</span>
    <span v-if="elapsed" class="sep" aria-hidden="true">·</span>
    <RollingLine class="act" :text="activity" />
    <Chevron class="chev" :open="!folded" />
  </button>
</template>

<style scoped lang="scss">
.livework {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  margin: 10px 0 0;
  padding: 6px 10px;
  border: 1px solid var(--pg-accent-line);
  border-radius: 9px;
  background: var(--pg-accent-wash);
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);

  &:hover:not(:disabled) {
    background: color-mix(in srgb, var(--el-color-primary) 16%, transparent);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  /* A pending approval owns the run: the fold control greys out instead of
     hiding the gate under it. */
  &:disabled {
    cursor: default;
    opacity: 0.75;
  }
}

/* The sonar, same grammar as the todo bar's: a heartbeat, not machinery. */
.pulse {
  position: relative;
  flex: 0 0 auto;
  width: 14px;
  height: 14px;

  &::before {
    content: "";
    position: absolute;
    inset: 4px;
    border-radius: 50%;
    background: var(--el-color-primary);
    animation: lw-core 2s ease-in-out infinite;
  }

  i {
    position: absolute;
    inset: 0;
    border: 1.5px solid var(--el-color-primary);
    border-radius: 50%;
    opacity: 0;
    animation: lw-sonar 2s cubic-bezier(0.22, 0.61, 0.36, 1) infinite;

    &:nth-child(2) {
      animation-delay: 1s;
    }
  }
}

@keyframes lw-core {
  0%,
  100% {
    transform: scale(1);
  }

  50% {
    transform: scale(0.72);
  }
}

@keyframes lw-sonar {
  0% {
    transform: scale(0.3);
    opacity: 0.85;
  }

  100% {
    transform: scale(1.05);
    opacity: 0;
  }
}

.dur {
  flex: 0 0 auto;
  color: var(--el-text-color-regular);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.sep {
  flex: 0 0 auto;
  color: var(--el-text-color-placeholder);
}

.act {
  flex: 1 1 auto;
  min-width: 0;
}

.chev {
  flex: 0 0 auto;
  color: var(--el-text-color-placeholder);
}

@media (prefers-reduced-motion: reduce) {
  .pulse::before,
  .pulse i {
    animation: none;
  }
}
</style>
