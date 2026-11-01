<script setup lang="ts">
import { computed } from "vue";
import { mcpHighlightSegments } from "./mcpMonitorPresentation";
const props = defineProps<{ text: string; queries: readonly string[] }>();
const segments = computed(() => mcpHighlightSegments(props.text, props.queries));
</script>

<template>
  <template v-for="(part, index) in segments" :key="index"><mark v-if="part.hit" class="mcp-match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template>
</template>

<style scoped>
.mcp-match { background: var(--el-color-primary-light-8); color: var(--el-text-color-primary); font-weight: 600; border-radius: 2px; padding: 0; }
</style>
