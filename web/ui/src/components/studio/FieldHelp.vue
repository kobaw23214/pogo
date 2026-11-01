<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue';

defineProps<{ label: string; text: string }>();
const root = ref<HTMLElement | null>(null);
const id = `field-help-${useId()}`;
const hovering = ref(false);
const focused = ref(false);
const pinned = ref(false);
const visible = computed(() => hovering.value || focused.value || pinned.value);
function dismiss() { hovering.value = false; focused.value = false; pinned.value = false; }
function outside(event: PointerEvent) {
  if (!root.value?.contains(event.target as Node)) dismiss();
}
onMounted(() => document.addEventListener('pointerdown', outside));
onBeforeUnmount(() => document.removeEventListener('pointerdown', outside));
</script>

<template>
  <span ref="root" class="field-help-wrap" @mouseenter="hovering = true" @mouseleave="hovering = false">
    <el-popover :visible="visible" :teleported="false" :persistent="false" :popper-options="{ strategy: 'fixed' }" :popper-style="{ '--el-bg-color-overlay': 'var(--el-bg-color)' }" placement="top" :width="280">
      <template #reference>
        <button type="button" class="field-help" :aria-label="`${label}说明`" :aria-describedby="id"
          :aria-expanded="visible" @focus="focused = true" @blur="focused = false; pinned = false"
          @click.stop="pinned = !pinned" @keydown.esc.stop.prevent="dismiss">?</button>
      </template>
      <span :id="id" role="tooltip" class="field-help-content">{{ text }}</span>
    </el-popover>
  </span>
</template>

<style scoped>
.field-help-wrap { display: inline-flex; vertical-align: middle; flex: none; }
.field-help { display: inline-flex; align-items: center; justify-content: center; width: 20px; height: 20px; padding: 0; border: 1px solid var(--el-border-color); border-radius: 50%; background: transparent; color: var(--el-text-color-secondary); font-family: inherit; font-size: 12px; font-weight: 600; line-height: 1; cursor: help; }
.field-help:hover, .field-help:focus-visible { color: var(--el-color-primary); border-color: var(--el-color-primary); outline-offset: 3px; }
.field-help-content { color: var(--el-text-color-regular); font-size: 12px; line-height: 1.65; overflow-wrap: anywhere; }
</style>
