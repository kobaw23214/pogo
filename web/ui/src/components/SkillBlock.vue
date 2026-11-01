<script setup lang="ts">
import { computed, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "./Chevron.vue";
import CommandTokenIcon from "./CommandTokenIcon.vue";
import "./commandToken.css";
import type { SkillBlock } from "@/agent/timeline";

const props = defineProps<{ block: SkillBlock }>();
const { t } = useI18n();
const detailsId = `skill-details-${useId()}`;
const hasDetails = computed(() => !!(props.block.location || props.block.body));

// Collapsed by default. A skill body is often hundreds of lines; expanded, it
// would push the question the user asked off the screen.
const open = ref(false);
</script>

<template>
  <div class="skill-block" data-testid="skill-message">
    <div class="skill-header">
      <span class="skill-badge command-token-surface skill" :title="`/skill:${block.name}`">
        <span class="command-token-copy">
          <CommandTokenIcon kind="skill" />
          <span class="command-token-type">{{ t('agentView.commands.skillTokenLabel') }}</span>
          <span class="command-token-name">{{ block.name }}</span>
        </span>
      </span>
      <button v-if="hasDetails" type="button" class="details-toggle" :aria-expanded="open" :aria-controls="detailsId" @click="open = !open">
        <span>{{ t(`agentView.commands.${open ? 'hideSkillDetails' : 'viewSkillDetails'}`) }}</span>
        <Chevron class="caret" :open="open" />
      </button>
    </div>
    <!-- What the user actually typed after the command stays visible either way:
         it is the request, and the skill is only the context for it. -->
    <div v-if="block.trailing || $slots.task" class="trailing"><slot name="task">{{ block.trailing }}</slot></div>
    <div v-if="hasDetails && open" :id="detailsId" class="details">
      <div v-if="block.location" class="location">
        <span class="source-label">{{ t('agentView.commands.skillSource') }}</span>
        <code>{{ block.location }}</code>
      </div>
      <pre v-if="block.body" class="body">{{ block.body }}</pre>
    </div>
  </div>
</template>

<style scoped lang="scss">
.skill-block {
  margin: 2px 0;
  white-space: normal;
}

.skill-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}
.skill-badge { min-height: 36px; padding: 7px 10px; }
.details-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 5px 6px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.details-toggle:hover { background: var(--el-fill-color-light); color: var(--el-text-color-primary); }
.details-toggle:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.caret { color: inherit; }
.details { margin-top: 10px; padding: 10px 12px; border: 1px solid var(--el-border-color-lighter); border-radius: 10px; background: var(--el-fill-color-lighter); }
.location { display: flex; flex-wrap: wrap; gap: 5px 8px; font-size: 12px; line-height: 1.5; }
.source-label { color: var(--el-text-color-secondary); }
.location code { min-width: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }

.body {
  margin: 8px 0 0;
  padding: 0;
  max-height: 320px;
  overflow: auto;
  color: var(--el-text-color-regular);
  font: 12px/1.55 ui-monospace, monospace;
  white-space: pre-wrap;
}

.trailing {
  margin-top: 10px;
  font-size: 14px;
  line-height: 1.7;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 480px), (pointer: coarse) {
  .details-toggle { min-height: 44px; }
}
</style>
