<script setup lang="ts">
import { computed } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { useI18n } from "vue-i18n";
import type { AgentFlowNodeData } from "@/agent/workflowPresentation";

/** The canvas's agent/human node: one step of the DAG. The color bar borrows
    the Agent's identity color, the dot overlays the live run state (incl.
    the amber human-wait pulse and the neutral rejected), the red corner
    badge counts validation issues, and the chip row carries what the node is
    (human / dispatcher / approval-gated) plus its declared artifact. The
    selection ring follows data.active — the editor owns selection itself. */
const props = defineProps<{ data: AgentFlowNodeData }>();

const { t } = useI18n();

const stateTitle = computed(() => {
  const parts: string[] = [];
  if (props.data.state) parts.push(t(`workflow.state.${props.data.state}`));
  if (props.data.branch) parts.push(t("workflow.node.branchVia", { when: t(`workflow.edge.when.${props.data.branch}`) }));
  if (props.data.skippedBy) parts.push(t("workflow.node.skippedBy", { id: props.data.skippedBy }));
  return parts.join("\n");
});
const issueTitle = computed(() => props.data.issues.map((code) => t(`workflow.errors.${code}`)).join("\n"));
</script>

<template>
  <div class="wf-node" :class="[data.state, { selected: data.active, missing: data.missing, human: data.kind === 'human' }]">
    <span class="wf-node-bar" :style="{ background: data.color }" aria-hidden="true" />
    <div class="wf-node-body">
      <div class="wf-node-title">
        <strong class="wf-node-name" :title="data.label">{{ data.label }}</strong>
        <span v-if="data.kind === 'human'" class="wf-chip human">{{ t("workflow.node.humanBadge") }}</span>
        <span v-if="data.orchestrator" class="wf-chip orch">{{ t("workflow.node.orchBadge") }}</span>
        <span v-if="data.approval" class="wf-chip approval">{{ t("workflow.node.approvalBadge") }}</span>
      </div>
      <span class="wf-node-task" :title="data.task">{{ data.task || t(data.readOnly ? "workflow.node.descriptionHidden" : "workflow.node.noTask") }}</span>
      <span v-if="data.expectPath" class="wf-node-expect" :title="data.expectPath">
        <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
          <path d="M14 3v5h5" />
        </svg>
        {{ data.expectPath }}
      </span>
    </div>
    <span
      v-if="data.state"
      class="wf-node-state"
      :class="data.state"
      :title="stateTitle"
      :aria-label="stateTitle"
    />
    <span
      v-if="data.issues.length"
      class="wf-node-badge"
      :title="issueTitle"
      role="img"
      :aria-label="issueTitle"
    >{{ data.issues.length }}</span>
    <Handle type="target" :position="Position.Top" class="wf-handle" />
    <Handle type="source" :position="Position.Bottom" class="wf-handle" />
  </div>
</template>

<style scoped>
.wf-node {
  position: relative;
  display: flex;
  align-items: stretch;
  width: 260px;
  min-height: 64px;
  background: var(--pg-wf-node-bg);
  border: 1px solid var(--pg-wf-node-border);
  border-radius: var(--pg-wf-node-radius);
  box-shadow: var(--el-box-shadow-lighter);
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.wf-node:hover {
  border-color: var(--el-border-color-hover);
}
.wf-node.selected {
  border: 2px solid var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.wf-node.missing {
  border-style: dashed;
}
.wf-node.human {
  border-style: dashed;
  border-color: var(--el-border-color);
}

.wf-node-bar {
  flex: none;
  width: 4px;
  border-radius: var(--pg-wf-node-radius) 0 0 var(--pg-wf-node-radius);
}
.wf-node.human .wf-node-bar {
  background: var(--pg-wf-state-waiting-human) !important;
}
.wf-node-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 10px 12px;
}
.wf-node-title {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}
.wf-node-name {
  font-size: 13px;
  font-weight: 650;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-chip {
  flex: none;
  font-size: 10px;
  font-weight: 600;
  border-radius: 999px;
  padding: 1px 7px;
  line-height: 1.5;
}
.wf-chip.human {
  background: color-mix(in srgb, var(--pg-wf-state-waiting-human) 15%, transparent);
  color: var(--pg-wf-state-waiting-human);
}
.wf-chip.orch {
  background: color-mix(in srgb, var(--pg-wf-state-rejected) 13%, transparent);
  color: var(--pg-wf-state-rejected);
}
.wf-chip.approval {
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
}
.wf-node-task {
  font-size: 11.5px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.wf-node-expect {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  align-self: flex-start;
  max-width: 100%;
  margin-top: 2px;
  font-family: var(--pg-mono);
  font-size: 10.5px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-radius: 5px;
  padding: 1px 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-node-expect svg {
  flex: none;
}

/* run state dot: the eight-state vocabulary. */
.wf-node-state {
  position: absolute;
  top: 8px;
  right: 8px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--pg-wf-state-waiting);
  box-shadow: 0 0 0 2px var(--pg-wf-node-bg);
}
.wf-node-state.running {
  background: var(--pg-wf-state-running);
  animation: wf-pulse 1.2s ease-in-out infinite;
}
.wf-node-state.waiting_human {
  background: var(--pg-wf-state-waiting-human);
  animation: wf-pulse-human 1.2s ease-in-out infinite;
}
.wf-node-state.success {
  background: var(--pg-wf-state-success);
}
.wf-node-state.failed {
  background: var(--pg-wf-state-failed);
}
.wf-node-state.skipped {
  background: var(--pg-wf-state-skipped);
}
.wf-node-state.cancelled {
  background: var(--pg-wf-state-cancelled);
}
.wf-node-state.rejected {
  background: var(--pg-wf-state-rejected);
}
@keyframes wf-pulse {
  0%, 100% {
    box-shadow: 0 0 0 2px var(--pg-wf-node-bg), 0 0 0 2px color-mix(in srgb, var(--pg-wf-state-running) 45%, transparent);
  }
  50% {
    box-shadow: 0 0 0 2px var(--pg-wf-node-bg), 0 0 0 5px color-mix(in srgb, var(--pg-wf-state-running) 12%, transparent);
  }
}
@keyframes wf-pulse-human {
  0%, 100% {
    box-shadow: 0 0 0 2px var(--pg-wf-node-bg), 0 0 0 2px color-mix(in srgb, var(--pg-wf-state-waiting-human) 45%, transparent);
  }
  50% {
    box-shadow: 0 0 0 2px var(--pg-wf-node-bg), 0 0 0 5px color-mix(in srgb, var(--pg-wf-state-waiting-human) 12%, transparent);
  }
}
@media (prefers-reduced-motion: reduce) {
  .wf-node-state.running,
  .wf-node-state.waiting_human {
    animation: none;
  }
}

/* validation badge: the red corner count. */
.wf-node-badge {
  position: absolute;
  top: -7px;
  left: -7px;
  min-width: 17px;
  height: 17px;
  padding: 0 4px;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 999px;
  background: var(--el-color-danger);
  color: var(--el-color-white, #fff);
  font-size: 10.5px;
  font-weight: 700;
  box-shadow: 0 0 0 2px var(--pg-wf-canvas-bg);
  cursor: help;
}

.wf-handle {
  width: 9px;
  height: 9px;
  background: var(--pg-wf-node-bg);
  border: 2px solid var(--el-color-primary);
}
</style>
