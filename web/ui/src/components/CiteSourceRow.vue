<script setup lang="ts">
import { computed, inject, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "./Chevron.vue";
import CiteSourceIcon from "./CiteSourceIcon.vue";
import { citePresentation } from "@/agent/citePresentation";
import { canJumpToCite, jumpToCite } from "@/agent/citeJump";
import { citeNavigationKey } from "@/agent/citeNavigation";
import { safeSourceURL, sourceIdentity, sourcePreview } from "@/agent/citePreview";
import type { CiteSource } from "@/api/types";

const props = defineProps<{ source: CiteSource; num?: number; context?: string }>();
const { t, locale } = useI18n();
const navigation = inject(citeNavigationKey, null);
const root = ref<HTMLElement | null>(null);
const open = ref(false);
const bodyId = `source-evidence-${useId()}`;
const presentation = computed(() => citePresentation(props.source));
const sourceURL = computed(() => safeSourceURL(props.source));
const sourceTitle = computed(() => sourceURL.value ? sourceIdentity(props.source).title : presentation.value.title);
const publisher = computed(() => sourceURL.value ? new URL(sourceURL.value).host.replace(/^www\./i, "") : "");
// Like the inline card, this only selects an unmodified recorded passage by
// word overlap. A preview does not certify that the answer is supported.
// Uncited records have no answer context and simply show their opening text.
const preview = computed(() => open.value ? sourcePreview(props.source, props.context ?? "") : null);
const outputExcerpt = computed(() => preview.value?.browserContent === "output"
  || (presentation.value.iconType !== "file_read"
    && !["web_page", "file_read", "file_write", "search_result", "information"].includes(props.source.type)));
const excerptLabel = computed(() => t(outputExcerpt.value ? "cite.toolExcerpt"
  : props.source.type === "search_result" ? "cite.searchExcerpt" : "cite.originalExcerpt"));
const recordedAt = computed(() => {
  if (!props.source.ts) return "";
  const value = new Date(props.source.ts);
  return Number.isNaN(value.getTime()) ? "" : value.toLocaleString(locale.value, {
    year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
});

function recordScope(): ParentNode | null {
  const local = root.value?.closest(".worklog, .turn");
  if (local && canJumpToCite(props.source.label, local)) return local;
  // An answer can explicitly reuse an earlier source, so fall back within its
  // own conversation. Never search the document or a neighboring subagent pane.
  return root.value?.closest(".conv-inner, .subsheet, .share-page") ?? null;
}
function recordAvailable(): boolean {
  const scope = recordScope();
  return !!scope && canJumpToCite(props.source.label, scope);
}
function locateRecord() {
  const scope = recordScope();
  if (scope) jumpToCite(props.source.label, scope);
}
function fileAvailable(): boolean {
  return !!root.value?.closest(".conv-inner") && !root.value.closest(".subsheet, .share-page")
    && !!navigation?.canOpen(props.source);
}
function openFile() {
  if (fileAvailable()) navigation?.open(props.source);
}
</script>

<template>
  <div ref="root" class="source-row" :class="{ expanded: open }">
    <button
      type="button"
      class="source-summary"
      :aria-expanded="open"
      :aria-controls="bodyId"
      :aria-label="num === undefined ? `${t('cite.uncited')} · ${presentation.title}` : t('cite.refLabel', { n: num, desc: presentation.title })"
      @click="open = !open"
    >
      <span class="source-number" :class="{ uncited: num === undefined }" aria-hidden="true">{{ num ?? '·' }}</span>
      <CiteSourceIcon class="source-glyph" :source="source" />
      <span class="source-identity">
        <span class="source-title">{{ sourceTitle }}</span>
        <span v-if="publisher" class="source-publisher">{{ publisher }}</span>
        <span v-if="presentation.location" class="source-location">{{ t('cite.readRange', { range: presentation.location.replace(/^:/, '') }) }}</span>
      </span>
      <Chevron class="source-chevron" :open="open" />
    </button>
    <div v-if="open" :id="bodyId" class="source-evidence">
      <div class="excerpt-label">{{ excerptLabel }}</div>
      <div v-if="preview?.text" class="source-excerpt" :class="{ 'source-output': outputExcerpt }">{{ preview.text }}</div>
      <p v-else class="source-note">{{ t("cite.noExcerpt") }}</p>
      <p v-if="preview?.text && !preview.matched" class="source-note">{{ t("cite.fallbackExcerpt") }}</p>
      <p v-if="recordedAt" class="source-note source-time">{{ t("cite.capturedAt", { time: recordedAt }) }}</p>
      <div v-if="source.error || source.exit_code !== undefined" class="source-result">
        <span v-if="source.exit_code !== undefined" :class="{ 'source-error': source.exit_code !== 0 }">{{ t('cite.exitCode', { code: source.exit_code }) }}</span>
        <span v-else-if="source.error" class="source-error">{{ t('cite.toolError') }}</span>
      </div>
      <div v-if="sourceURL || fileAvailable() || recordAvailable()" class="source-actions">
        <a v-if="sourceURL" class="source-action source-primary" :href="sourceURL" target="_blank" rel="noopener noreferrer">{{ t("cite.openSource") }} <span aria-hidden="true">↗</span></a>
        <button v-else-if="fileAvailable()" type="button" class="source-action source-primary" :title="t(source.type === 'information' ? 'cite.informationVersionHint' : 'cite.currentFileHint')" @click="openFile">{{ t(source.type === 'information' ? 'cite.openInformationVersion' : presentation.location ? "cite.openLocation" : "cite.openFile") }}</button>
        <button v-if="recordAvailable()" type="button" class="source-action" @click="locateRecord">{{ t("cite.viewRecord") }}</button>
      </div>
      <p v-if="fileAvailable() && !sourceURL" class="source-note">{{ t(source.type === 'information' ? 'cite.informationVersionHint' : "cite.currentFileHint") }}</p>
    </div>
  </div>
</template>

<style scoped lang="scss">
.source-row { min-width: 0; }
.source-summary {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
  min-height: 42px;
  padding: 10px 5px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-primary);
  font: inherit;
  font-size: 12px;
  line-height: 1.6;
  text-align: start;
  cursor: pointer;
  &:hover { background: var(--el-fill-color-extra-light); }
}
.source-summary:focus-visible, .source-action:focus-visible { outline: 2px solid var(--pg-link-color, var(--el-color-primary)); outline-offset: 2px; }
.source-number { flex: 0 0 18px; text-align: center; color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
.source-number.uncited { color: var(--el-text-color-placeholder); }
.source-glyph { flex: 0 0 15px; width: 15px; height: 15px; margin-top: 2px; color: var(--pg-link-color, var(--el-color-primary)); }
.source-identity { display: flex; flex: 1; flex-wrap: wrap; align-items: baseline; gap: 1px 7px; min-width: 0; }
.source-title { overflow-wrap: anywhere; }
.source-publisher { flex-basis: 100%; color: var(--el-text-color-secondary); font-size: 11px; overflow-wrap: anywhere; }
.source-location { white-space: nowrap; color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
.source-chevron { flex: 0 0 12px; width: 12px; height: 12px; margin-top: 3px; color: var(--el-text-color-secondary); }
.source-evidence { margin: 0 5px 12px 49px; min-width: 0; }
.excerpt-label { margin-bottom: 5px; color: var(--el-text-color-secondary); font-size: 11px; }
.source-excerpt {
  max-height: 240px;
  overflow: auto;
  overscroll-behavior: contain;
  padding: 9px 11px;
  border-left: 2px solid var(--el-border-color);
  background: var(--el-fill-color-extra-light);
  color: var(--el-text-color-regular);
  font-size: 12px;
  line-height: 1.75;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.source-output { font-family: var(--pg-mono); font-size: 11px; }
.source-note { margin: 5px 0 0; color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.6; }
.source-time { margin-top: 8px; }
.source-result { margin-top: 5px; color: var(--el-text-color-secondary); font-size: 11px; }
.source-error { color: var(--el-color-danger); }
.source-actions { display: flex; flex-wrap: wrap; gap: 6px 12px; margin-top: 10px; }
.source-action {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 28px;
  padding: 2px 0;
  border: 0;
  border-radius: 3px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font: inherit;
  font-size: 11px;
  text-decoration: none;
  cursor: pointer;
  &:hover { text-decoration: underline; text-underline-offset: 3px; }
}
.source-primary { color: var(--pg-link-color, var(--el-color-primary)); }
@media (max-width: 480px) {
  .source-evidence { margin-left: 23px; }
  .source-actions { gap: 6px 14px; }
  .source-action { min-height: 36px; }
}
</style>
