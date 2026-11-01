<script setup lang="ts">
import { CaretRight } from "@element-plus/icons-vue";

/**
 * The fold affordance, shared by every expandable row: one filled caret that
 * rotates a quarter turn instead of swapping glyphs — a flip reads as the
 * same control moving, a swapped character as a glitch. A vector icon at
 * 14px, because the text triangles this replaces rendered at 10px and read
 * as specks. Colour is inherited, so each site keeps its own dim/accent.
 */
defineProps<{ open?: boolean }>();
</script>

<template>
  <el-icon class="chevron" :class="{ open }" aria-hidden="true"><CaretRight /></el-icon>
</template>

<style scoped lang="scss">
.chevron {
  flex: 0 0 auto;
  font-size: 14px;
  transition: transform 0.18s cubic-bezier(0.2, 0, 0, 1);
}

.chevron.open {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: reduce) {
  .chevron {
    transition: none;
  }
}
</style>
