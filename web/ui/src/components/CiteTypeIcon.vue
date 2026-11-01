<script setup lang="ts">
import { computed } from "vue";
import { citeIconInner } from "@/agent/citeIcons";

// The glyph a citation source wears, by stamp type: terminal for a command,
// document for a read, and so on. The path data lives in agent/citeIcons.ts,
// shared with MarkdownView's chip renderer, which needs the same glyph as a
// string. Inline SVG stroked with currentColor, so the chip's theme token
// colours it and no hex is pinned anywhere.
const props = defineProps<{ type: string }>();
const inner = computed(() => citeIconInner(props.type));
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    class="cite-type-icon"
    v-html="inner"
  />
</template>

<style scoped>
.cite-type-icon {
  width: 13px;
  height: 13px;
  flex: 0 0 auto;
}
</style>
