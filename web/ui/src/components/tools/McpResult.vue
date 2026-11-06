<script setup lang="ts">
import { computed, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "../Chevron.vue";
import ResultPreview from "./ResultPreview.vue";
import ResultContent from "./ResultContent.vue";
import { hasResultArgs, isExactResultJSON, resultTextPreview, resultValueText, structuredResultText } from "./resultPresentation";
import { useCardDisclosure } from "@/agent/cardDisclosure";
import { formatDuration } from "@/agent/timeline";
import type { MCPDetails, ToolResult } from "@/api/types";

const props = defineProps<{ result: ToolResult; details: MCPDetails; args?: unknown; headerless?: boolean; disclosureKey?: string | null }>();
const { t } = useI18n();
const detailsId = useId();
const detailsOpen = useCardDisclosure(() => props.disclosureKey === null ? undefined : props.disclosureKey ?? (props.result.call_id ? `call:${props.result.call_id}:details` : undefined), false);
const failed = computed(() => props.result.is_error === true);
const duration = computed(() => props.details.duration_ms ?? props.result.duration_ms);
const hasStructured = computed(() => props.details.structured_content !== undefined && props.details.structured_content !== null);
const structuredText = computed(() => structuredResultText(props.details.structured_content));
const blocks = computed(() => (props.details.content ?? []).filter(block => {
  if (block.type !== "text") return true;
  if (failed.value && block.text === props.result.text) return false;
  return !hasStructured.value || !isExactResultJSON(block.text ?? "", props.details.structured_content);
}));
const longTextBlocks = computed(() => blocks.value.filter(block => block.type === "text" &&
  block.text !== props.result.text && resultTextPreview(block.text ?? "").shortened));
const hasArgs = computed(() => hasResultArgs(props.args));
const argsText = computed(() => resultValueText(props.args));
const metadata = computed(() => {
  const { content: _content, structured_content: _structured, ...value } = props.details;
  return resultValueText(value);
});
const fallbackText = computed(() => {
  if (failed.value || !props.result.text) return "";
  const originalBlocks = props.details.content ?? [];
  if (originalBlocks.length === 0) {
    const marker = "[structured content]\n";
    const candidate = props.result.text.startsWith(marker) ? props.result.text.slice(marker.length) : props.result.text;
    return hasStructured.value && isExactResultJSON(candidate, props.details.structured_content) ? "" : props.result.text;
  }
  // Embedded resource bodies live in the model-facing text, not in blocks.
  return originalBlocks.some(block => block.type === "resource") ? props.result.text : "";
});
const copied = ref(false);
async function copyStructured() {
  try {
    await navigator.clipboard.writeText(hasStructured.value ? structuredText.value : props.result.text);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard availability does not affect access to the visible result.
  }
}

function mediaSrc(block: { mime_type?: string; data?: string }, fallback: string): string {
  return `data:${block.mime_type || fallback};base64,${block.data}`;
}
</script>

<template>
  <div class="mcp result-call" :class="{ failed }">
    <div v-if="!headerless || details.timed_out || details.truncated" class="meta">
      <template v-if="!headerless">
        <span class="identity">{{ details.title || details.tool }}</span>
        <span v-if="details.title && details.title !== details.tool" class="protocol">{{ details.tool }}</span>
        <span class="server">{{ details.server }}</span>
        <span v-if="duration != null" class="dur">{{ formatDuration(duration) }}</span>
        <span v-if="failed" class="chip bad">{{ t("mcpResult.failed") }}</span>
      </template>
      <span v-if="details.timed_out" class="chip warn">{{ t("mcpResult.timedOut") }}</span>
      <span v-if="details.truncated" class="chip warn" :title="details.full_output_path">{{ t("mcpResult.truncated") }}</span>
    </div>

    <ResultPreview v-if="failed" :text="result.text" :more-label="t('mcpResult.previewMore')" card error />
    <ResultPreview v-if="hasStructured" :text="structuredText" :more-label="t('mcpResult.previewMore')" card />
    <template v-for="(block, i) in blocks" :key="i">
      <ResultPreview v-if="block.type === 'text'" :text="block.text ?? ''" :more-label="t('mcpResult.previewMore')" card />
      <div v-else-if="block.type === 'image'" class="media">
        <img v-if="block.data" :src="mediaSrc(block, 'image/png')" :alt="block.mime_type ?? 'image'" />
        <span v-else class="omitted">{{ t("mcpResult.imageOmitted", { size: block.size ?? 0 }) }}</span>
      </div>
      <div v-else-if="block.type === 'audio'" class="media">
        <audio v-if="block.data" controls :src="mediaSrc(block, 'audio/mpeg')" />
        <span v-else class="omitted">{{ t("mcpResult.audioOmitted", { size: block.size ?? 0 }) }}</span>
      </div>
      <a v-else-if="(block.type === 'resource' || block.type === 'resource_link') && block.uri"
        class="reslink" :href="block.uri" target="_blank" rel="noopener noreferrer">{{ block.name || block.uri }}</a>
      <span v-else class="omitted">{{ t("mcpResult.unknownBlock") }}</span>
    </template>
    <ResultPreview v-if="fallbackText" :text="fallbackText" :more-label="t('mcpResult.previewMore')" card />

    <button class="detail-toggle" :aria-expanded="detailsOpen" :aria-controls="detailsId" @click="detailsOpen = !detailsOpen">
      <Chevron :open="detailsOpen" />{{ t("mcpResult.details") }}
    </button>
    <button v-if="hasStructured || result.text" class="copy-result" :title="t(hasStructured ? 'mcpResult.copyStructured' : 'mcpResult.copyOutput')" :aria-label="t(hasStructured ? 'mcpResult.copyStructured' : 'mcpResult.copyOutput')" @click="copyStructured">
      {{ copied ? t("common.copied") : t("common.copy") }}
    </button>
    <div v-if="detailsOpen" :id="detailsId" class="raw-details">
      <template v-if="hasArgs"><div class="raw-label">{{ t("mcpResult.arguments") }}</div><ResultContent class="raw" :text="argsText" /></template>
      <template v-if="hasStructured"><div class="raw-label">{{ t("mcpResult.structuredResult") }}</div><ResultContent class="raw" :text="structuredText" /></template>
      <template v-if="result.text"><div class="raw-label">{{ t("mcpResult.originalResult") }}</div><ResultContent class="raw" :text="result.text" /></template>
      <template v-for="(block, i) in longTextBlocks" :key="i"><div class="raw-label">{{ t("mcpResult.textBlock", { n: i + 1 }) }}</div><ResultContent class="raw" :text="block.text ?? ''" /></template>
      <div class="raw-label">{{ t("mcpResult.metadata") }}</div><ResultContent class="raw" :text="metadata" />
    </div>
  </div>
</template>

<style scoped lang="scss" src="./resultPresentation.scss"></style>
<style scoped lang="scss">
.media { margin: 5px 0; }
.media img { display: block; max-width: 100%; max-height: 320px; border-radius: 6px; border: 1px solid var(--el-border-color-lighter); }
.media audio { max-width: 100%; height: 32px; }
.omitted { font-size: 11px; color: var(--el-text-color-secondary); }
.reslink { display: inline-block; margin: 3px 8px 3px 0; color: var(--el-color-primary); font-size: 12px; overflow-wrap: anywhere; }
</style>
