<script lang="ts">
/** The dataTransfer types the palette writes and the canvas's drop reads. */
export const AGENT_DRAG_MIME = "application/x-pogo-agent";
export const HUMAN_DRAG_MIME = "application/x-pogo-human";
</script>

<script setup lang="ts">
import { useI18n } from "vue-i18n";
import AgentAvatar from "@/components/AgentAvatar.vue";
import type { PlatformAgent } from "@/agent/platformTypes";
import { agentCapabilitySummary } from "@/agent/studioPresentation";

/** The editor's left rail: every Agent is a draggable step material, plus the
    human node — the one step that pauses the run for a person. DnD is plain
    HTML5 — the canvas reads the material off the drop's dataTransfer. */
defineProps<{ agents: PlatformAgent[] }>();
const emit = defineEmits<{ add: [agent: PlatformAgent]; addHuman: [] }>();

const { t } = useI18n();

function onDragStart(event: DragEvent, agent: PlatformAgent) {
  if (!event.dataTransfer) return;
  event.dataTransfer.setData(AGENT_DRAG_MIME, agent.id);
  event.dataTransfer.effectAllowed = "move";
}

function onHumanDragStart(event: DragEvent) {
  if (!event.dataTransfer) return;
  event.dataTransfer.setData(HUMAN_DRAG_MIME, "1");
  event.dataTransfer.effectAllowed = "move";
}
</script>

<template>
  <aside class="wf-palette" :aria-label="t('workflow.palette.title')">
    <header class="wf-palette-head">
      <strong>{{ t("workflow.palette.title") }}</strong>
      <span>{{ t("workflow.palette.hint") }}</span>
    </header>
    <div class="wf-palette-list">
      <button
        type="button"
        class="wf-palette-item human"
        draggable="true"
        :title="t('workflow.palette.humanDesc')"
        @dragstart="onHumanDragStart"
        @click="emit('addHuman')"
      >
        <span class="wf-human-glyph" aria-hidden="true">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
            <circle cx="12" cy="8" r="3.6" />
            <path d="M5 20c1.4-3.4 4-5 7-5s5.6 1.6 7 5" />
          </svg>
        </span>
        <span class="wf-palette-meta">
          <strong>{{ t("workflow.palette.human") }}</strong>
          <span class="wf-palette-cap">{{ t("workflow.palette.humanDesc") }}</span>
        </span>
      </button>
      <p v-if="!agents.length" class="wf-palette-empty">{{ t("workflow.palette.empty") }}</p>
      <button
        v-for="agent in agents"
        :key="agent.id"
        type="button"
        class="wf-palette-item"
        draggable="true"
        :title="agent.name"
        @dragstart="onDragStart($event, agent)"
        @click="emit('add', agent)"
      >
        <AgentAvatar :icon="agent.icon" :color="agent.color" :size="30" />
        <span class="wf-palette-meta">
          <strong>
            {{ agent.name }}
            <em v-if="agent.status === 'draft'" class="wf-palette-draft">{{ t("workflow.palette.draftChip") }}</em>
          </strong>
          <span class="wf-palette-cap">{{ agentCapabilitySummary(agent) }}</span>
        </span>
      </button>
    </div>
  </aside>
</template>

<style scoped>
.wf-palette {
  flex: none;
  width: 232px;
  display: flex;
  flex-direction: column;
  background: var(--el-bg-color);
  border-right: 1px solid var(--el-border-color-lighter);
  min-height: 0;
}
.wf-palette-head {
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}
.wf-palette-head strong {
  font-size: 13px;
  font-weight: 650;
  color: var(--el-text-color-primary);
}
.wf-palette-head span {
  font-size: 11.5px;
  color: var(--el-text-color-placeholder);
}
.wf-palette-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wf-palette-empty {
  margin: 18px 8px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
  text-align: center;
}
.wf-palette-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
  padding: 8px 10px;
  font: inherit;
  text-align: left;
  cursor: grab;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.wf-palette-item:hover {
  border-color: var(--el-border-color-hover);
  box-shadow: var(--el-box-shadow-lighter);
}
.wf-palette-item:active {
  cursor: grabbing;
}
.wf-palette-item.human {
  border-style: dashed;
}
.wf-human-glyph {
  flex: none;
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  background: color-mix(in srgb, var(--pg-wf-state-waiting-human) 14%, transparent);
  color: var(--pg-wf-state-waiting-human);
}
.wf-palette-meta {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.wf-palette-meta strong {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.8px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-palette-draft {
  flex: none;
  font-style: normal;
  font-size: 10.5px;
  font-weight: 600;
  border-radius: 999px;
  padding: 1px 7px;
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}
.wf-palette-cap {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
