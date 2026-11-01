<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { WorkflowRun } from "@/agent/workflowTypes";
import { runLive } from "@/agent/workflowPresentation";

/** The run's swimlane review: one row per node, a pure-CSS bar of started→
    ended against the run's whole window, the human wait in amber. Clicking a
    row jumps to that node's drawer. */
const props = defineProps<{ run: WorkflowRun | null }>();
const emit = defineEmits<{ select: [nodeId: string] }>();

const { t } = useI18n();

const now = ref(Date.now());
const clock = window.setInterval(() => {
  now.value = Date.now();
}, 2000);
onBeforeUnmount(() => window.clearInterval(clock));

interface Lane {
  id: string;
  status: string;
  left: number;
  width: number;
  started: string;
  duration: string;
}

const lanes = computed<Lane[]>(() => {
  const run = props.run;
  if (!run) return [];
  const end = run.ended_at ? new Date(run.ended_at).getTime() : now.value;
  const start = new Date(run.created_at).getTime();
  const span = Math.max(end - start, 1000);
  return run.definition.nodes.map((n) => {
    const rec = run.nodes[n.id];
    if (!rec?.started_at) {
      return { id: n.id, status: rec?.status ?? "waiting", left: 0, width: 0, started: "", duration: "" };
    }
    const s = new Date(rec.started_at).getTime();
    const e = rec.ended_at ? new Date(rec.ended_at).getTime() : now.value;
    return {
      id: n.id,
      status: rec.status,
      left: Math.max(0, ((s - start) / span) * 100),
      width: Math.max(((e - s) / span) * 100, 0.8),
      started: rec.started_at,
      duration: `${Math.max(Math.round((e - s) / 1000), 0)}`,
    };
  });
});

function laneTitle(lane: Lane): string {
  if (!lane.started) return t(`workflow.state.${lane.status}`);
  return `${t("workflow.swimlane.startedAt", { time: lane.started.slice(11, 19) })} · ${t("workflow.swimlane.duration", { n: lane.duration })} · ${t(`workflow.state.${lane.status}`)}`;
}

const live = computed(() => runLive(props.run));
</script>

<template>
  <div class="wf-lanes">
    <p v-if="!run" class="wf-lanes-empty">{{ t("workflow.swimlane.empty") }}</p>
    <template v-else>
      <button
        v-for="lane in lanes"
        :key="lane.id"
        type="button"
        class="wf-lane"
        :title="laneTitle(lane)"
        @click="emit('select', lane.id)"
      >
        <span class="wf-lane-id">{{ lane.id }}</span>
        <span class="wf-lane-track">
          <span v-if="lane.width > 0" class="wf-lane-bar" :class="[lane.status, { live: live && !run?.nodes[lane.id]?.ended_at }]" :style="{ left: `${lane.left}%`, width: `${lane.width}%` }" />
          <span v-else class="wf-lane-none">{{ t(`workflow.state.${lane.status}`) }}</span>
        </span>
        <span class="wf-lane-dur">{{ lane.duration ? t("workflow.swimlane.duration", { n: lane.duration }) : "" }}</span>
      </button>
    </template>
  </div>
</template>

<style scoped>
.wf-lanes {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.wf-lanes-empty {
  margin: 24px 0;
  text-align: center;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}
.wf-lane {
  display: grid;
  grid-template-columns: minmax(90px, 160px) 1fr 44px;
  align-items: center;
  gap: 10px;
  border: 0;
  background: transparent;
  border-radius: 7px;
  padding: 5px 8px;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-lane:hover {
  background: var(--el-fill-color-light);
}
.wf-lane-id {
  font-family: var(--pg-mono);
  font-size: 11.5px;
  color: var(--el-text-color-regular);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-lane-track {
  position: relative;
  height: 14px;
  border-radius: 4px;
  background: var(--el-fill-color-lighter);
  overflow: hidden;
}
.wf-lane-bar {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 4px;
  background: var(--pg-wf-state-waiting);
}
.wf-lane-bar.running {
  background: var(--pg-wf-state-running);
}
.wf-lane-bar.waiting_human {
  background: var(--pg-wf-state-waiting-human);
}
.wf-lane-bar.success {
  background: var(--pg-wf-state-success);
}
.wf-lane-bar.failed {
  background: var(--pg-wf-state-failed);
}
.wf-lane-bar.rejected {
  background: var(--pg-wf-state-rejected);
}
.wf-lane-bar.skipped,
.wf-lane-bar.cancelled {
  background: var(--pg-wf-state-skipped);
}
.wf-lane-bar.live {
  border-radius: 4px 0 0 4px;
  animation: wf-lane-pulse 1.6s ease-in-out infinite;
}
@keyframes wf-lane-pulse {
  0%, 100% {
    opacity: 1;
  }
  50% {
    opacity: 0.55;
  }
}
@media (prefers-reduced-motion: reduce) {
  .wf-lane-bar.live {
    animation: none;
  }
}
.wf-lane-none {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  color: var(--el-text-color-placeholder);
}
.wf-lane-dur {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  text-align: right;
}
</style>
