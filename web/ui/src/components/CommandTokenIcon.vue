<script setup lang="ts">
import { computed } from "vue";
import { contextRingPercent } from "@/agent/contextUsage";
const props = defineProps<{ kind: "skill" | "mcp" | "command"; contextPercent?: number | null }>();
const progress = computed(() => contextRingPercent(props.contextPercent));
</script>

<template>
  <svg class="command-token-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" :stroke-width="kind === 'command' ? 2.3 : 1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <template v-if="kind === 'command'">
      <circle cx="12" cy="12" r="8.5" opacity=".3" />
      <circle v-if="progress !== null && progress > 0" cx="12" cy="12" r="8.5" pathLength="100" :stroke-dasharray="`${progress} 100`" stroke-linecap="butt" transform="rotate(-90 12 12)" />
    </template>
    <path v-else-if="kind === 'mcp'" d="m8 12 3 3 7-7M7 7l-3 3a4 4 0 0 0 0 6l2 2a4 4 0 0 0 6 0l2-2M17 17l3-3a4 4 0 0 0 0-6l-2-2a4 4 0 0 0-6 0l-2 2" />
    <path v-else d="m12 3 9 5v8l-9 5-9-5V8l9-5Zm-9 5 9 5 9-5m-9 5v8M7.5 5.5l9 5" />
  </svg>
</template>

<style scoped>
.command-token-icon { flex: 0 0 18px; display: block; width: 18px; height: 18px; }
</style>
