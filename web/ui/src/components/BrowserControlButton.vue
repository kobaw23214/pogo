<script setup lang="ts">
defineProps<{ mode: "takeover" | "return" }>();
</script>

<template>
  <button class="browser-control" :class="`browser-control--${mode}`" type="button">
    <svg v-if="mode === 'takeover'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <rect x="5" y="2" width="14" height="20" rx="7" />
      <path d="M12 6v4" />
    </svg>
    <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M3 15h10m-4-4 4 4-4 4M17 3l1.4 4.6L23 9l-4.6 1.4L17 15l-1.4-4.6L11 9l4.6-1.4L17 3Z" />
    </svg>
    <span><slot /></span>
  </button>
</template>

<style scoped>
.browser-control.browser-control {
  --control-background: #a94b25;
  --control-hover: #913e1b;
  --control-focus: #c8734d;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex-shrink: 0;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--control-background);
  border-radius: 6px;
  background: var(--control-background);
  color: #fff;
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  line-height: 1;
  white-space: nowrap;
  cursor: pointer;
}
.browser-control.browser-control--return {
  --control-background: #255ed0;
  --control-hover: #1e4daa;
  --control-focus: #719bef;
}
.browser-control.browser-control:not(:disabled):hover {
  background: var(--control-hover);
  border-color: var(--control-hover);
  color: #fff;
}
.browser-control.browser-control:focus-visible {
  outline: 2px solid var(--control-focus);
  outline-offset: 2px;
}
.browser-control.browser-control:disabled { opacity: .5; cursor: default; }
.browser-control svg { width: 17px; height: 17px; flex-shrink: 0; }
@media (max-width: 600px) { .browser-control.browser-control { padding: 0 8px; gap: 5px; } }
</style>
