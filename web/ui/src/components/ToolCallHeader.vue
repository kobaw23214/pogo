<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import WorkLogGlyph from "./WorkLogGlyph.vue";
import Chevron from "./Chevron.vue";
import type { ToolPresentation } from "@/agent/toolPresentation";
import type { OpenFileTarget } from "./dockSheets";

const props = defineProps<{
  presentation: ToolPresentation;
  expandable?: boolean;
  open?: boolean;
  controls?: string;
  fileTarget?: OpenFileTarget;
  layered?: boolean;
}>();
const emit = defineEmits<{ toggle: []; "open-file": [OpenFileTarget] }>();
const { t } = useI18n();
const commandCollapsed = computed(() => props.layered && props.expandable && !props.open && props.presentation.protocolName === "bash");
const displayTarget = computed(() => {
  const target = props.presentation.target;
  if (!target || !commandCollapsed.value) return target;
  const characters = Array.from(target.replace(/\s+/g, " ").trim());
  return characters.length > 96 ? characters.slice(0, 95).join("") + "…" : characters.join("");
});

function toggle() {
  if (!props.expandable || (typeof window !== "undefined" && window.getSelection?.()?.toString())) return;
  emit("toggle");
}
function clickHeader(event: MouseEvent) {
  // The file/slot controls are siblings of the disclosure button. Their
  // actions must never also toggle the result. Plain row space remains a
  // convenient mouse target, while the native button owns keyboard access.
  if ((event.target as Element | null)?.closest?.("button, a, input, select, textarea")) return;
  toggle();
}
</script>

<template>
  <div
    class="tool-call-header"
    :class="[presentation.state, { expandable, layered, 'command-collapsed': commandCollapsed }]"
    :data-tool-kind="presentation.glyph"
    :data-tool-state="presentation.state"
    @click="clickHeader"
  >
    <span class="tool-glyph"><WorkLogGlyph :kind="presentation.glyph" /></span>
    <span class="tool-identity">
      <component :is="expandable ? 'button' : 'span'" class="tool-label" :type="expandable ? 'button' : undefined"
        :aria-expanded="expandable ? !!open : undefined" :aria-controls="expandable ? controls : undefined"
        @click.stop="toggle">
        <span class="tool-name" :title="presentation.protocolName">{{ presentation.name }}</span>
        <span v-if="presentation.secondary" class="tool-secondary">{{ presentation.secondary }}</span>
        <span v-if="!fileTarget && presentation.target" class="tool-target" :title="commandCollapsed ? presentation.target : undefined">{{ displayTarget }}</span>
      </component>
      <button v-if="fileTarget" type="button" class="tool-target tool-file-link" :title="t('toolCall.openFile', { path: fileTarget.path })" :data-open-file="fileTarget.path" :data-skill="fileTarget.skill" :data-skill-path="fileTarget.skillPath" @click.stop="emit('open-file', fileTarget)">{{ fileTarget.path }}</button>
      <slot />
    </span>
    <span v-if="presentation.meta" class="tool-meta">{{ presentation.meta }}</span>
    <span v-if="presentation.statusLabel" class="tool-status" role="status">{{ presentation.statusLabel }}</span>
    <Chevron v-if="expandable" class="tool-chevron" :open="!!open" />
  </div>
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
.tool-label { display: inline-flex; align-items: baseline; flex-wrap: wrap; gap: 2px 8px; min-width: 0; padding: 0; border: 0; border-radius: 3px; background: transparent; color: inherit; font: inherit; text-align: left; }
button.tool-label { cursor: pointer; }
button.tool-label:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
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
.tool-call-header.layered { min-height: 28px; color: var(--pg-activity-text, var(--el-text-color-primary)); }
.layered .tool-secondary, .layered .tool-target, .layered .tool-meta, .layered .tool-glyph, .layered .tool-chevron { color: var(--pg-activity-muted, var(--el-text-color-regular)); }
.layered .tool-file-link { color: var(--pg-activity-link, var(--pg-link-color)); }
.layered .tool-status { color: var(--pg-activity-text, var(--el-text-color-regular)); }
.layered.running .tool-glyph, .layered.running .tool-status::before { color: var(--pg-activity-live, var(--el-color-primary)); }
.layered.gate .tool-glyph { color: var(--pg-activity-warning, var(--el-color-warning)); }
.layered.error .tool-glyph, .layered.orphaned .tool-glyph { color: var(--pg-activity-error, var(--el-color-danger)); }
@keyframes tool-pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .running .tool-status::before { animation: none; } }
@media (max-width: 480px) { .tool-call-header { gap: 6px; flex-wrap: wrap; } .tool-identity { flex-basis: calc(100% - 25px); } .tool-meta { margin-left: 21px; } }
@media (max-width: 480px) { .tool-call-header.layered { min-height: 32px; } }
/* Only the command preview is shortened. Opening the row restores the exact
   command, including newlines, quoting and indentation. */
.tool-call-header.command-collapsed { flex-wrap: nowrap; align-items: center; }
.command-collapsed .tool-identity { flex-basis: 0; flex-wrap: nowrap; overflow: hidden; }
.command-collapsed .tool-label { flex: 1; min-width: 0; flex-wrap: nowrap; overflow: hidden; }
.command-collapsed .tool-name { flex: 0 0 auto; white-space: nowrap; }
.command-collapsed .tool-target { display: block; max-width: 64ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.command-collapsed .tool-meta, .command-collapsed .tool-status, .command-collapsed .tool-chevron { flex: 0 0 auto; margin-left: 0; }
</style>
