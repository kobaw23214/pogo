<script setup lang="ts">
import { computed, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "../Chevron.vue";
import ResultPreview from "./ResultPreview.vue";
import { hasResultArgs, isExactResultJSON, resultValueText, structuredResultText } from "./resultPresentation";
import { useCardDisclosure } from "@/agent/cardDisclosure";
import { formatDuration } from "@/agent/timeline";
import type { SkillDetails, ToolResult } from "@/api/types";

const props = defineProps<{ result: ToolResult; details: SkillDetails; args?: unknown; headerless?: boolean; disclosureKey?: string | null }>();
const { t } = useI18n();
const detailsId = useId();
const detailsOpen = useCardDisclosure(() => props.disclosureKey === null ? undefined : props.disclosureKey ?? (props.result.call_id ? `call:${props.result.call_id}:details` : undefined), false);
const failed = computed(() => props.result.is_error === true);
const hasData = computed(() => props.details.data !== undefined && props.details.data !== null);
const dataText = computed(() => structuredResultText(props.details.data));
const artifacts = computed(() => props.details.artifacts ?? []);
const hasArgs = computed(() => hasResultArgs(props.args));
const argsText = computed(() => resultValueText(props.args));
const metadata = computed(() => {
  const { data: _data, artifacts: _artifacts, ...value } = props.details;
  return resultValueText(value);
});
const independentText = computed(() => {
  if (failed.value || !props.result.text || isExactResultJSON(props.result.text, props.details)) return "";
  return hasData.value && isExactResultJSON(props.result.text, props.details.data) ? "" : props.result.text;
});
const copied = ref(false);
async function copyResult() {
  try {
    await navigator.clipboard.writeText(hasData.value ? dataText.value : props.result.text);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard availability does not affect access to the visible result.
  }
}

function formatSize(bytes?: number): string {
  const n = bytes ?? 0;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${n} B`;
}
</script>

<template>
  <div class="skill-call result-call" :class="{ failed }">
    <div v-if="!headerless" class="meta">
      <span class="identity">{{ details.capability }}</span>
      <span v-if="result.duration_ms != null" class="dur">{{ formatDuration(result.duration_ms) }}</span>
      <span v-if="failed" class="chip bad">{{ t("skillResult.failed") }}</span>
    </div>
    <ResultPreview v-if="failed" :text="result.text" :more-label="t('skillResult.previewMore')" error />
    <ResultPreview v-if="hasData" :text="dataText" :more-label="t('skillResult.previewMore')" />
    <ResultPreview v-if="independentText" :text="independentText" :more-label="t('skillResult.previewMore')" />

    <div v-if="artifacts.length" class="artifacts">
      <span v-for="(artifact, i) in artifacts" :key="artifact.artifactId ?? i" class="chip" :title="artifact.digest">
        {{ artifact.name || artifact.artifactId }}<template v-if="artifact.mediaType"> · {{ artifact.mediaType }}</template> · {{ formatSize(artifact.sizeBytes) }}
      </span>
    </div>

    <button class="detail-toggle" :aria-expanded="detailsOpen" :aria-controls="detailsId" @click="detailsOpen = !detailsOpen">
      <Chevron :open="detailsOpen" />{{ t("skillResult.details") }}
    </button>
    <button v-if="hasData || result.text" class="copy-result" :title="t('skillResult.copyResult')" :aria-label="t('skillResult.copyResult')" @click="copyResult">
      {{ copied ? t("common.copied") : t("common.copy") }}
    </button>
    <div v-if="detailsOpen" :id="detailsId" class="raw-details">
      <template v-if="hasArgs"><div class="raw-label">{{ t("skillResult.arguments") }}</div><pre class="raw">{{ argsText }}</pre></template>
      <template v-if="hasData"><div class="raw-label">{{ t("skillResult.result") }}</div><pre class="raw">{{ dataText }}</pre></template>
      <template v-if="result.text"><div class="raw-label">{{ t("skillResult.originalResult") }}</div><pre class="raw">{{ result.text }}</pre></template>
      <div class="raw-label">{{ t("skillResult.metadata") }}</div><pre class="raw">{{ metadata }}</pre>
      <template v-if="artifacts.length"><div class="raw-label">{{ t("skillResult.artifacts") }}</div><pre class="raw">{{ structuredResultText(artifacts) }}</pre></template>
    </div>
  </div>
</template>

<style scoped lang="scss" src="./resultPresentation.scss"></style>
