<script setup lang="ts">
import { useI18n } from "vue-i18n";
import WorkLogGlyph from "./WorkLogGlyph.vue";
import Chevron from "./Chevron.vue";
import type { ToolPresentation } from "@/agent/toolPresentation";
import type { OpenFileTarget } from "./dockSheets";

defineProps<{
  presentation: ToolPresentation;
  expandable?: boolean;
  open?: boolean;
  controls?: string;
  fileTarget?: OpenFileTarget;
}>();
const emit = defineEmits<{ toggle: []; "open-file": [OpenFileTarget] }>();
const { t } = useI18n();
</script>

<template>
  <component
    :is="expandable ? 'button' : 'div'"
    class="tool-call-header"
    :class="[presentation.state, { expandable }]"
    :type="expandable ? 'button' : undefined"
    :aria-expanded="expandable ? !!open : undefined"
    :aria-controls="expandable ? controls : undefined"
    :data-tool-kind="presentation.glyph"
    :data-tool-state="presentation.state"
    @click="expandable && emit('toggle')"
  >
    <span class="tool-glyph"><WorkLogGlyph :kind="presentation.glyph" /></span>
    <span class="tool-identity">
      <span class="tool-name" :title="presentation.protocolName">{{ presentation.name }}</span>
      <span v-if="presentation.secondary" class="tool-secondary">{{ presentation.secondary }}</span>
      <button v-if="fileTarget" type="button" class="tool-target tool-file-link" :title="t('toolCall.openFile', { path: fileTarget.path })" :data-open-file="fileTarget.path" :data-skill="fileTarget.skill" :data-skill-path="fileTarget.skillPath" @click.stop="emit('open-file', fileTarget)">{{ fileTarget.path }}</button>
      <span v-else-if="presentation.target" class="tool-target">{{ presentation.target }}</span>
      <slot />
    </span>
    <span v-if="presentation.meta" class="tool-meta">{{ presentation.meta }}</span>
    <span v-if="presentation.statusLabel" class="tool-status" role="status">{{ presentation.statusLabel }}</span>
    <Chevron v-if="expandable" class="tool-chevron" :open="!!open" />
  </component>
</template>

<style scoped lang="scss">
.tool-call-header {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
  min-width: 0;
  color: var(--el-text-color-primary);
  font-size: 12px;
  line-height: 1.5;
  text-align: left;

  &.expandable {
    border: 0;
    border-radius: 5px;
    background: transparent;
    padding: 3px 0;
    cursor: pointer;
    &:hover { background: var(--el-fill-color-light); }
    &:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
  }
}
.tool-glyph { width: 15px; height: 15px; flex: 0 0 15px; margin-top: 2px; color: var(--el-text-color-secondary); }
.tool-identity { display: flex; flex-wrap: wrap; gap: 2px 8px; min-width: 0; flex: 1; align-items: baseline; }
.tool-name { font-weight: 600; overflow-wrap: anywhere; }
.tool-secondary { color: var(--el-text-color-secondary); font-size: 11px; overflow-wrap: anywhere; }
.tool-target { color: var(--el-text-color-secondary); font-family: var(--pg-mono); white-space: pre-wrap; overflow-wrap: anywhere; min-width: 0; }
.tool-file-link { padding: 0; border: 0; background: transparent; color: var(--pg-link-color); text-align: left; cursor: pointer; }
.tool-file-link:hover { text-decoration: underline; text-underline-offset: 2px; }
.tool-file-link:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; border-radius: 2px; }
.tool-meta { color: var(--el-text-color-secondary); white-space: nowrap; font-size: 11px; font-variant-numeric: tabular-nums; padding-top: 1px; }
.tool-status { font-size: 10px; white-space: nowrap; padding: 1px 7px; border-radius: 999px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); }
.running .tool-status { color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.running .tool-status::before { content: ""; display: inline-block; width: 5px; height: 5px; margin-right: 5px; border-radius: 50%; background: currentColor; animation: tool-pulse 1.2s ease-in-out infinite; }
.gate .tool-status { color: var(--el-color-warning); background: var(--el-color-warning-light-9); }
.error .tool-status, .orphaned .tool-status { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.tool-chevron { flex: 0 0 auto; margin-top: 3px; color: var(--el-text-color-secondary); }
@keyframes tool-pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .running .tool-status::before { animation: none; } }
@media (max-width: 480px) { .tool-call-header { gap: 6px; flex-wrap: wrap; } .tool-identity { flex-basis: calc(100% - 25px); } .tool-meta { margin-left: 21px; } }
</style>
