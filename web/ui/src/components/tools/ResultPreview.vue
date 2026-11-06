<script setup lang="ts">
import { computed } from "vue";
import { resultTextPreview } from "./resultPresentation";
import ResultContent from "./ResultContent.vue";
const props = defineProps<{ text: string; moreLabel: string; error?: boolean; card?: boolean }>();
const preview = computed(() => props.error ? { text: props.text, shortened: false } : resultTextPreview(props.text));
</script>

<template>
  <div class="result-preview" :class="{ error, card }">
    <ResultContent v-if="card" :text="preview.text" :json-source="text" :error="error" />
    <pre v-else>{{ preview.text }}</pre>
    <span v-if="preview.shortened" class="preview-note">{{ moreLabel }}</span>
  </div>
</template>

<style scoped>
.result-preview { margin: 5px 0; }
pre { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.6 var(--pg-mono); color: var(--el-text-color-regular); }
.preview-note { display: block; margin-top: 3px; font-size: 11px; color: var(--el-text-color-secondary); }
.error { padding: 7px 10px; border-radius: 6px; background: var(--el-color-danger-light-9); border-left: 2px solid var(--el-color-danger); }
.error pre { color: var(--el-color-danger); max-height: 320px; overflow: auto; }
.error.card { padding: 0; border-left: 0; background: transparent; }
</style>
