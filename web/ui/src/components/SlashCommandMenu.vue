<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { ComposerSuggestion } from "@/agent/composerSuggestions";
import CommandTokenIcon from "./CommandTokenIcon.vue";

const props = defineProps<{ id: string; items: ComposerSuggestion[]; selectedId: string | null; contextPercent?: number | null }>();
const emit = defineEmits<{ pick: [id: string]; highlight: [id: string] }>();
const { t } = useI18n();
const menu = ref<HTMLElement>();
const groups = computed(() => [
  { id: "commands", label: "", items: props.items.filter(item => item.kind === "command") },
  { id: "skills", label: t("agentView.commands.skillsGroup"), items: props.items.filter(item => item.kind === "skill" || item.kind === "skill-entry") },
  { id: "mcp", label: t("agentView.commands.mcpGroup"), items: props.items.filter(item => item.kind === "mcp" || item.kind === "mcp-entry") },
].filter(group => group.items.length));

watch(() => [props.selectedId, props.items], async () => {
  await nextTick();
  menu.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: "nearest", inline: "nearest" });
}, { immediate: true, flush: "post" });
</script>

<template>
  <div :id="id" ref="menu" class="slash-command-menu" role="listbox" :aria-label="t('agentView.commands.menuLabel')" @mousedown.prevent @wheel.stop>
    <div class="menu-scroll">
      <div v-for="group in groups" :key="group.id" class="suggestion-group" role="group" :aria-labelledby="group.label ? `${id}-${group.id}-heading` : undefined">
        <div v-if="group.label" :id="`${id}-${group.id}-heading`" class="group-heading">{{ group.label }}</div>
        <button v-for="item in group.items" :id="`${id}-${item.id}`" :key="item.id" type="button" class="suggestion-row" :class="{ selected: selectedId === item.id, command: item.kind === 'command' }" role="option" :aria-selected="selectedId === item.id" :aria-labelledby="`${id}-${item.id}-label`" :aria-describedby="item.description ? `${id}-${item.id}-description` : undefined" tabindex="-1" @mouseenter="emit('highlight', item.id)" @click="emit('pick', item.id)">
          <span class="suggestion-icon" :class="{ command: item.kind === 'command' }" aria-hidden="true">
            <CommandTokenIcon v-if="item.kind === 'command'" kind="command" :context-percent="contextPercent" />
            <svg v-else-if="item.kind === 'mcp' || item.kind === 'mcp-entry'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m9 9 6 6m-9 0 3 3a4.24 4.24 0 0 0 6-6l-1-1m4-2-3-3a4.24 4.24 0 0 0-6 6l1 1M5 19l-2 2m16-16 2-2" /></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="m12 3 9 5v8l-9 5-9-5V8l9-5Zm-9 5 9 5 9-5m-9 5v8M7.5 5.5l9 5" /></svg>
          </span>
          <span class="suggestion-copy">
            <span :id="`${id}-${item.id}-label`" class="suggestion-label">{{ item.label }}</span>
            <span v-if="item.description" :id="`${id}-${item.id}-description`" class="suggestion-description">{{ item.description }}</span>
          </span>
          <span v-if="item.origin" class="suggestion-origin">{{ t(`agentView.commands.${item.origin === 'builtin' ? 'builtin' : 'external'}`) }}</span>
          <kbd v-if="selectedId === item.id" class="completion-key" aria-hidden="true">{{ item.shortcut || 'Tab' }}</kbd>
        </button>
      </div>
    </div>
    <div class="keyboard-hint" role="presentation">{{ t('agentView.commands.keyboardHint') }}</div>
  </div>
</template>

<style scoped>
.slash-command-menu {
  display: flex;
  flex-direction: column;
  width: 100%;
  min-width: 0;
  max-height: min(360px, 45dvh, var(--composer-menu-height, 360px));
  box-sizing: border-box;
  padding: 6px;
  overflow: hidden;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 20px;
  background: var(--el-bg-color-overlay);
  color: var(--el-text-color-primary);
  box-shadow: 0 8px 30px rgb(0 0 0 / 14%), 0 2px 8px rgb(0 0 0 / 6%);
}
.menu-scroll { min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
.suggestion-group + .suggestion-group { margin-top: 4px; padding-top: 4px; border-top: 1px solid var(--el-border-color-extra-light); }
.group-heading { padding: 8px 12px 5px; color: var(--el-text-color-secondary); font-size: 12px; font-weight: 500; line-height: 1.4; }
.suggestion-row {
  display: flex;
  align-items: center;
  gap: 11px;
  width: 100%;
  min-width: 0;
  min-height: 44px;
  box-sizing: border-box;
  padding: 9px 12px;
  border: 0;
  border-radius: 12px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.suggestion-row:hover { background: var(--el-fill-color-light); }
.suggestion-row.selected { background: var(--el-fill-color-dark); color: var(--el-text-color-primary); }
.suggestion-row:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: -2px; }
.suggestion-icon { display: flex; align-items: center; justify-content: center; flex: 0 0 28px; width: 28px; height: 28px; color: var(--el-text-color-regular); }
.suggestion-icon svg { display: block; width: 23px; height: 23px; }
.suggestion-copy { display: flex; flex: 1; align-items: center; gap: 12px; min-width: 0; }
.suggestion-label,.suggestion-description { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.suggestion-label { flex-shrink: 0; max-width: 45%; font-size: 14px; font-weight: 500; line-height: 1.4; }
.suggestion-description { flex: 1; min-width: 0; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.4; }
.suggestion-row.command .suggestion-description { overflow: visible; white-space: normal; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.suggestion-origin { flex-shrink: 0; color: var(--el-text-color-secondary); font-size: 11px; white-space: nowrap; }
.completion-key { flex-shrink: 0; padding: 2px 5px; border: 1px solid var(--el-border-color); border-radius: 5px; background: var(--el-bg-color-overlay); color: var(--el-text-color-secondary); font-family: inherit; font-size: 11px; line-height: 1.4; }
.keyboard-hint { flex-shrink: 0; margin: 4px 6px 0; padding: 7px 6px 3px; border-top: 1px solid var(--el-border-color-extra-light); color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.5; }
@media (max-width: 480px) {
  .slash-command-menu { border-radius: 16px; }
  .suggestion-row { gap: 8px; padding: 9px; }
  .suggestion-label { font-size: 13px; }
  .suggestion-copy { flex-direction: column; align-items: stretch; gap: 3px; }
  .suggestion-label { max-width: none; }
  .suggestion-origin { font-size: 10px; }
}
</style>
