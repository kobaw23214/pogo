<script setup lang="ts">
import { computed } from "vue";
import { highlightSegments } from "./highlight";

// One text with its filter hits marked. Renders segments rather than v-html:
// the text comes from manifests the operator already vetted, but a highlight
// helper that needs innerHTML is a template-injection footgun for no gain.
const props = defineProps<{ text: string; q?: string }>();

const segments = computed(() => highlightSegments(props.text, props.q ?? ""));
</script>

<template>
  <template v-for="(s, i) in segments" :key="i">
    <mark v-if="s.hit" class="hl">{{ s.text }}</mark>
    <template v-else>{{ s.text }}</template>
  </template>
</template>

<style scoped lang="scss">
// mark with the theme's primary tint. Marked runs sit inside names,
// descriptions and paths, so the style is a soft wash rather than the
// browser-default saturated yellow, which would fight the line-clamped cards.
.hl {
  background: var(--el-color-primary-light-8);
  color: var(--el-color-primary-dark-2);
  font-weight: 600;
  border-radius: 2px;
  padding: 0 1px;
}
</style>
