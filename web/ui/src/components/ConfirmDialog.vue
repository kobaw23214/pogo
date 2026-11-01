<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";

/**
 * The styled stand-in for window.confirm. One instance per host view; the
 * caller awaits ask() and gets the user's decision back, which is what the
 * studio navigation guards (boolean | Promise<boolean>) expect. `danger`
 * paints the action red for lossy choices; cancel, the X, Esc and a programmatic
 * close all settle the parked promise with false. One question at a time — a
 * second ask() while open settles false immediately.
 */
export interface ConfirmDialogOptions {
  title: string;
  body: string;
  confirmText: string;
  cancelText: string;
  danger?: boolean;
}

const open = ref(false);
const opts = ref<ConfirmDialogOptions>({ title: "", body: "", confirmText: "", cancelText: "" });
let resolver: ((ok: boolean) => void) | null = null;

function ask(options: ConfirmDialogOptions): Promise<boolean> {
  if (resolver) return Promise.resolve(false);
  opts.value = options;
  open.value = true;
  return new Promise<boolean>((resolve) => {
    resolver = resolve;
  });
}

function settle(ok: boolean) {
  open.value = false;
  const resolve = resolver;
  resolver = null;
  resolve?.(ok);
}

defineExpose({ ask });

// A host unmounting mid-question must not leave a caller parked forever —
// the studio navigation guard awaits this promise before it can proceed.
onBeforeUnmount(() => settle(false));
</script>

<template>
  <el-dialog
    :model-value="open"
    :title="opts.title"
    width="min(420px, 94vw)"
    append-to-body
    :close-on-click-modal="false"
    @update:model-value="(v: boolean) => { if (!v) settle(false); }"
  >
    <p class="pgc-body">{{ opts.body }}</p>
    <template #footer>
      <button type="button" class="pgc-btn ghost" @click="settle(false)">{{ opts.cancelText }}</button>
      <button type="button" class="pgc-btn" :class="opts.danger ? 'danger' : 'solid'" @click="settle(true)">
        {{ opts.confirmText }}
      </button>
    </template>
  </el-dialog>
</template>

<style scoped>
.pgc-body {
  margin: 0;
  font-size: 13px;
  line-height: 1.8;
  color: var(--el-text-color-regular);
}

/* The studio's button ladder, restated — scoped styles cannot be borrowed
   across components. */
.pgc-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 8px 15px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  transition: background var(--pg-transition), border-color var(--pg-transition), color var(--pg-transition);
}
.pgc-btn.ghost {
  background: transparent;
  border-color: var(--el-border-color);
  color: var(--el-text-color-regular);
}
.pgc-btn.ghost:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.pgc-btn.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.pgc-btn.solid:hover {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.pgc-btn.danger {
  background: var(--el-color-danger);
  border-color: var(--el-color-danger);
  color: var(--el-color-white, #fff);
}
.pgc-btn.danger:hover {
  background: var(--el-color-danger-dark-2);
  border-color: var(--el-color-danger-dark-2);
}
</style>
