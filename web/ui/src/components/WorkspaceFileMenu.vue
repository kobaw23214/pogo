<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Delete, EditPen } from "@element-plus/icons-vue";
import type { WorkspaceFileTarget } from "./workspaceFileActions";
const props = defineProps<{ target: WorkspaceFileTarget | null; x: number; y: number }>();
const emit = defineEmits<{ close: []; rename: [WorkspaceFileTarget]; delete: [WorkspaceFileTarget] }>();
const { t } = useI18n();
const menu = ref<HTMLElement | null>(null);
const position = computed(() => ({ left: `${Math.max(8, Math.min(props.x, window.innerWidth - 202))}px`, top: `${Math.max(8, Math.min(props.y, window.innerHeight - 112))}px` }));
function close() { emit("close"); }
function outside(event: Event) { if (!menu.value?.contains(event.target as Node)) close(); }
function key(event: KeyboardEvent) {
  if (event.key === "Escape" || event.key === "Tab") { close(); return; }
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  event.preventDefault(); const buttons = [...menu.value?.querySelectorAll<HTMLButtonElement>("button") || []];
  const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
  buttons[(index + (event.key === "ArrowDown" ? 1 : buttons.length - 1)) % buttons.length]?.focus();
}
function removeListeners() { window.removeEventListener("pointerdown", outside, true); window.removeEventListener("keydown",key,true); window.removeEventListener("resize",close); window.removeEventListener("scroll",close,true); }
watch(() => props.target, async target => {
  removeListeners();
  if (!target) return;
  window.addEventListener("pointerdown",outside,true); window.addEventListener("keydown",key,true); window.addEventListener("resize",close); window.addEventListener("scroll",close,true);
  await nextTick(); menu.value?.querySelector<HTMLButtonElement>("button")?.focus();
});
onBeforeUnmount(removeListeners);
</script>
<template>
  <Teleport to="body">
    <div v-if="target" ref="menu" class="workspace-file-menu" role="menu" :aria-label="target.name" :style="position" @contextmenu.prevent>
      <div class="menu-target" :title="target.path">{{ target.name }}</div>
      <button role="menuitem" @click="emit('rename', target)"><el-icon><EditPen /></el-icon>{{ t('filesPanel.rename') }}</button>
      <button class="danger" role="menuitem" @click="emit('delete', target)"><el-icon><Delete /></el-icon>{{ t('filesPanel.delete') }}</button>
    </div>
  </Teleport>
</template>
<style scoped>
.workspace-file-menu { position: fixed; z-index: 3600; width: 190px; padding: 6px; border: 1px solid var(--el-border-color-light); border-radius: 11px; background: var(--el-bg-color-overlay); box-shadow: 0 8px 30px #0003; }
.menu-target { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 4px 8px 7px; font-size: 11px; color: var(--el-text-color-secondary); }
button { display: flex; align-items: center; gap: 9px; width: 100%; padding: 8px; border: none; border-radius: 6px; color: var(--el-text-color-primary); font: inherit; font-size: 13px; background: transparent; cursor: pointer; text-align: left; }
button:hover,button:focus-visible { outline: none; background: var(--el-fill-color); }
button.danger { color: var(--el-color-danger); }
</style>
