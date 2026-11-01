<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { CiteSource } from "@/api/types";
import type { CiteCardItem } from "@/agent/cite";
import { safeSourceURL, sourceIdentity } from "@/agent/citePreview";
import { citationFileTarget } from "@/agent/citeNavigation";
import CiteSourceIcon from "./CiteSourceIcon.vue";

// The parent owns positioning and dismissal. Events come from the actual
// teleported section, not fallthrough listeners on Teleport.
const props = defineProps<{
  id: string;
  items: CiteCardItem[];
  left: number;
  top: number;
  above: boolean;
  pinned: boolean;
  canOpenFile?: (src: CiteSource) => boolean;
  canLocate?: (label: string) => boolean;
}>();
const emit = defineEmits<{
  close: [];
  locate: [label: string];
  openFile: [src: CiteSource];
  enter: [];
  leave: [];
  blur: [event: FocusEvent];
  resize: [];
}>();
const { t, locale } = useI18n();
const card = ref<HTMLElement | null>(null);
const excerptElement = ref<HTMLElement | null>(null);
const page = ref(0);
const expanded = ref(false);
const excerptOverflows = ref(false);
const copyState = ref<"idle" | "copied" | "failed">("idle");
let copiedTimer: number | undefined;
let observer: ResizeObserver | undefined;

// Streaming rebuilds item objects repeatedly. Preserve the selected label
// until group membership changes, and preserve it then if still available.
watch(
  () => props.items.map((item) => item.label).join("\u0000"),
  (labels, previous) => {
    const selected = previous?.split("\u0000")[page.value];
    const next = selected ? labels.split("\u0000").indexOf(selected) : -1;
    page.value = next >= 0 ? next : 0;
  },
);
const current = computed(() => props.items[Math.min(page.value, props.items.length - 1)]);
const src = computed(() => current.value?.src);
const identity = computed(() => src.value ? sourceIdentity(src.value) : undefined);
const sourceURL = computed(() => src.value ? safeSourceURL(src.value) : undefined);
const fileTarget = computed(() => src.value ? citationFileTarget(src.value) : null);
const readRange = computed(() => {
  const target = fileTarget.value;
  if (target?.lineStart === undefined) return "";
  return target.lineEnd === target.lineStart ? `L${target.lineStart}` : `L${target.lineStart}–${target.lineEnd}`;
});
const pending = computed(() => current.value?.state === "pending");
const ready = computed(() => current.value?.state === "ready" && Boolean(src.value));
const excerpt = computed(() => current.value?.preview?.text ?? src.value?.excerpt ?? "");
const canExpand = computed(() => expanded.value || excerptOverflows.value);
const outputExcerpt = computed(() => current.value?.preview?.browserContent === "output"
  || (src.value && !["web_page", "file_read", "search_result", "information"].includes(src.value.type)));
const typeLabel = computed(() => {
  if (!src.value) return t("cite.sourceLabel");
  if (identity.value?.detail === "PDF") return "PDF";
  if (identity.value?.kind === "web") return identity.value.detail;
  if (identity.value?.kind === "file") return t("cite.fileType");
  if (identity.value?.kind === "library") return t("cite.informationType");
  const keys: Record<string, string> = {
    command_exec: "commandType", mcp_tool: "connectedToolType",
    subagent: "agentType", search_result: "searchType",
  };
  return t("cite." + (keys[src.value.type] ?? "toolType"));
});
const title = computed(() => ready.value
  ? identity.value?.title ?? t("cite.sourceLabel")
  : t(pending.value ? "cite.loadingTitle" : "cite.unavailableTitle"));
const excerptLabel = computed(() => t(outputExcerpt.value
  ? "cite.toolExcerpt"
  : src.value?.type === "search_result" ? "cite.searchExcerpt" : "cite.originalExcerpt"));
const exitCode = computed(() => {
  if (src.value?.type !== "command_exec") return undefined;
  if (src.value.exit_code !== undefined) return src.value.exit_code;
  const legacy = /^exit\s+(-?\d+)\s*·/.exec(src.value.title.trim());
  return legacy ? Number(legacy[1]) : undefined;
});
const date = computed(() => {
  if (!src.value?.ts) return undefined;
  const value = new Date(src.value.ts);
  return Number.isNaN(value.getTime()) ? undefined : value;
});
const time = computed(() => date.value?.toLocaleString(locale.value === "zh-CN" ? "zh-CN" : "en", {
  year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
}) ?? "");
const fileAvailable = computed(() => Boolean(src.value && props.canOpenFile?.(src.value)));
const recordAvailable = computed(() => Boolean(current.value && props.canLocate?.(current.value.label)));

watch(() => current.value?.label, () => {
  expanded.value = false;
  copyState.value = "idle";
  window.clearTimeout(copiedTimer);
});

function measureExcerpt() {
  const element = excerptElement.value;
  if (!element) {
    excerptOverflows.value = false;
    return;
  }
  // Expanded content must retain its collapse control. Check actual layout
  // only when collapsed: CJK, logs and narrow viewports wrap differently.
  if (!expanded.value) excerptOverflows.value = element.scrollHeight > element.clientHeight + 1;
}
watch([excerpt, expanded, excerptElement], measureExcerpt, { flush: "post" });
watch(card, (element) => {
  observer?.disconnect();
  if (!element || typeof ResizeObserver === "undefined") return;
  observer = new ResizeObserver(() => {
    measureExcerpt();
    emit("resize");
  });
  observer.observe(element);
}, { flush: "post" });

function openCurrentFile() {
  if (src.value && fileAvailable.value) emit("openFile", src.value);
}
function locateCurrent() {
  if (current.value && recordAvailable.value) emit("locate", current.value.label);
}
async function writeClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Keep fallback focus inside the dialog so copying cannot dismiss it.
    const previousFocus = document.activeElement;
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.tabIndex = -1;
    textarea.style.cssText = "position:fixed;opacity:0;pointer-events:none;width:1px;height:1px;";
    try {
      (card.value ?? document.body).appendChild(textarea);
      textarea.select();
      return document.execCommand("copy");
    } catch {
      return false;
    } finally {
      textarea.remove();
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected) previousFocus.focus({ preventScroll: true });
    }
  }
}
async function copyCurrent() {
  if (!src.value || !excerpt.value) return;
  const label = current.value?.label;
  const reference = [
    t("cite.copySource") + (identity.value?.title ?? src.value.title),
    src.value.locator,
    date.value ? t("cite.copyCaptured") + date.value.toISOString() : "",
  ].filter(Boolean).join("\n");
  const ok = await writeClipboard(excerpt.value + "\n\n" + reference);
  if (label !== current.value?.label) return;
  copyState.value = ok ? "copied" : "failed";
  window.clearTimeout(copiedTimer);
  copiedTimer = window.setTimeout(() => { copyState.value = "idle"; }, 3500);
}
onBeforeUnmount(() => {
  window.clearTimeout(copiedTimer);
  observer?.disconnect();
});
</script>

<template>
  <Teleport to="body">
    <section
      :id="id"
      ref="card"
      class="cite-card"
      :class="{ above, pinned }"
      :style="{ left: left + 'px', top: top + 'px' }"
      role="dialog"
      :aria-label="t('cite.dialogLabel')"
      :aria-busy="pending"
      tabindex="-1"
      @mouseenter="emit('enter')"
      @mouseleave="emit('leave')"
      @focusin="emit('enter')"
      @focusout="emit('blur', $event)"
      @keydown.esc.stop.prevent="emit('close')"
    >
      <div class="cc-header">
        <div class="cc-publisher">
          <span class="cc-source-icon" aria-hidden="true"><CiteSourceIcon v-if="src" :source="src" /><span v-else>?</span></span>
          <span class="cc-type">{{ typeLabel }}</span>
          <span v-if="current?.num !== undefined" class="cc-num">{{ current.num }}</span>
        </div>
        <button class="cc-icon-btn cc-close" :aria-label="t('common.close')" @click="emit('close')">×</button>
      </div>
      <a v-if="ready && sourceURL" class="cc-title cc-title-link" :href="sourceURL" target="_blank" rel="noopener noreferrer">{{ title }}<span class="cc-external" aria-hidden="true"> ↗</span></a>
      <div v-else class="cc-title">{{ title }}</div>
      <div v-if="!ready" class="cc-state" role="status">
        <span v-if="pending" class="cc-spinner" aria-hidden="true" />
        <span>{{ t(pending ? 'cite.loadingBody' : 'cite.unavailableBody') }}</span>
      </div>
      <template v-else>
        <div class="cc-evidence-meta">
          <span>{{ excerptLabel }}</span>
          <span v-if="readRange">{{ t('cite.readRange', { range: readRange }) }}</span>
          <span v-if="current?.preview?.page !== undefined">{{ t('cite.outputPage', { n: current.preview.page }) }}</span>
        </div>
        <div v-if="excerpt" ref="excerptElement" class="cc-excerpt" :class="{ 'cc-excerpt-collapsed': !expanded, 'cc-code-excerpt': outputExcerpt && identity?.kind !== 'file' }">{{ excerpt }}</div>
        <p v-else class="cc-empty">{{ t('cite.noExcerpt') }}</p>
        <button v-if="canExpand" class="cc-text-btn cc-expand" :aria-expanded="expanded" @click="expanded = !expanded">{{ t(expanded ? 'cite.collapseExcerpt' : 'cite.expandExcerpt') }}</button>
        <p v-if="excerpt && current?.preview?.matched === false" class="cc-hint">{{ t('cite.fallbackExcerpt') }}</p>
        <div v-if="time || exitCode !== undefined || src?.error" class="cc-record-meta">
          <time v-if="time" :datetime="date?.toISOString()">{{ t('cite.capturedAt', { time }) }}</time>
          <span v-if="exitCode !== undefined" :class="{ 'cc-error-text': exitCode !== 0 }">{{ t('cite.exitCode', { code: exitCode }) }}</span>
          <span v-else-if="src?.error" class="cc-error-text">{{ t('cite.toolError') }}</span>
        </div>
      </template>
      <div v-if="items.length > 1" class="cc-pager" :aria-label="t('cite.pagerLabel')">
        <button class="cc-icon-btn" :aria-label="t('cite.pagerPrev')" :disabled="page === 0" @click="page--">←</button>
        <span class="cc-position" aria-live="polite">{{ t('cite.pagerPosition', { n: page + 1, total: items.length }) }}</span>
        <button class="cc-icon-btn" :aria-label="t('cite.pagerNext')" :disabled="page >= items.length - 1" @click="page++">→</button>
      </div>
      <div v-if="(ready && (sourceURL || fileAvailable || excerpt)) || recordAvailable" class="cc-footer">
        <div class="cc-actions">
          <a v-if="ready && sourceURL" class="cc-action cc-primary-action" :href="sourceURL" target="_blank" rel="noopener noreferrer">{{ t('cite.openSource') }} <span aria-hidden="true">↗</span></a>
          <button v-else-if="ready && fileAvailable" class="cc-action cc-primary-action" :title="t(src?.type === 'information' ? 'cite.informationVersionHint' : 'cite.currentFileHint')" @click="openCurrentFile">{{ t(src?.type === 'information' ? 'cite.openInformationVersion' : readRange ? 'cite.openLocation' : 'cite.openFile') }}</button>
          <button v-if="recordAvailable" class="cc-action" @click="locateCurrent">{{ t('cite.viewRecord') }}</button>
          <button v-if="ready && excerpt" class="cc-action" :class="{ 'cc-copy-done': copyState === 'copied' }" @click="copyCurrent">{{ t(copyState === 'copied' ? 'cite.copiedExcerpt' : 'cite.copyExcerpt') }}</button>
        </div>
        <p v-if="fileAvailable && !sourceURL" class="cc-hint cc-file-hint">{{ t(src?.type === 'information' ? 'cite.informationVersionHint' : 'cite.currentFileHint') }}</p>
        <p v-if="copyState !== 'idle'" class="cc-copy-status" :class="{ 'cc-error-text': copyState === 'failed' }" role="status">{{ t(copyState === 'copied' ? 'cite.copySuccess' : 'cite.copyFailure') }}</p>
      </div>
    </section>
  </Teleport>
</template>

<style lang="scss">
.cite-card {
  position: fixed;
  z-index: 3000;
  box-sizing: border-box;
  width: min(360px, calc(100vw - 24px));
  max-height: calc(100vh - 24px);
  max-height: calc(100dvh - 24px);
  overflow: auto;
  overscroll-behavior: contain;
  padding: 14px 16px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 14px;
  background: var(--el-bg-color-overlay);
  color: var(--el-text-color-primary);
  box-shadow: var(--el-box-shadow-dark);
  font-size: 13px;
  line-height: 1.5;
  text-align: start;
  word-break: normal;
  animation: cite-card-in 120ms ease-out;

  button, a { -webkit-tap-highlight-color: transparent; }
  button { font: inherit; }
  button:focus-visible, a:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 3px; }
  p { margin: 0; }
  .cc-header, .cc-publisher, .cc-evidence-meta, .cc-record-meta, .cc-pager, .cc-actions { display: flex; align-items: center; }
  .cc-header { justify-content: space-between; gap: 8px; margin-bottom: 8px; }
  .cc-publisher { gap: 8px; min-width: 0; }
  .cc-source-icon {
    display: inline-flex; align-items: center; justify-content: center;
    flex: 0 0 25px; width: 25px; height: 25px; border-radius: 7px;
    background: var(--el-fill-color-light); color: var(--el-text-color-secondary);
  }
  .cc-type { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--el-text-color-secondary); font-size: 12px; }
  .cc-num { color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; border: 1px solid var(--el-border-color-lighter); border-radius: 5px; padding: 0 5px; }
  .cc-icon-btn {
    display: inline-flex; align-items: center; justify-content: center;
    width: 28px; height: 28px; flex: 0 0 28px; padding: 0; border: 0; border-radius: 7px;
    background: transparent; color: var(--el-text-color-secondary); cursor: pointer;
    &:hover:not(:disabled) { background: var(--el-fill-color); color: var(--el-text-color-primary); }
    &:disabled { opacity: .35; cursor: default; }
  }
  .cc-close { font-size: 22px; line-height: 1; }
  .cc-title { display: block; font-size: 15px; line-height: 1.45; font-weight: 600; color: var(--el-text-color-primary); overflow-wrap: anywhere; text-decoration: none; }
  .cc-title-link:hover { text-decoration: underline; text-underline-offset: 3px; }
  .cc-external { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; }
  .cc-evidence-meta { gap: 5px 12px; flex-wrap: wrap; margin: 13px 0 6px; color: var(--el-text-color-secondary); font-size: 11px; }
  .cc-excerpt { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.7; color: var(--el-text-color-regular); }
  .cc-excerpt-collapsed { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 4; overflow: hidden; }
  .cc-code-excerpt { font-family: var(--pg-mono); font-size: 12px; }
  .cc-text-btn { border: 0; padding: 0; background: transparent; color: var(--el-color-primary); cursor: pointer; }
  .cc-expand { margin-top: 6px; font-size: 12px; }
  .cc-hint, .cc-empty { color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; }
  .cc-hint { margin-top: 7px; }
  .cc-record-meta { flex-wrap: wrap; gap: 4px 10px; margin-top: 10px; color: var(--el-text-color-secondary); font-size: 10px; }
  .cc-error-text { color: var(--el-color-danger); }
  .cc-pager { justify-content: center; gap: 10px; margin-top: 12px; padding-top: 8px; border-top: 1px solid var(--el-border-color-extra-light); }
  .cc-position { color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; }
  .cc-footer { margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--el-border-color-extra-light); }
  .cc-pager + .cc-footer { margin-top: 5px; padding-top: 5px; border-top: 0; }
  .cc-actions { flex-wrap: wrap; gap: 5px 8px; }
  .cc-action {
    display: inline-flex; align-items: center; gap: 4px; padding: 5px 8px;
    border: 0; border-radius: 6px; background: var(--el-fill-color-light);
    color: var(--el-text-color-regular); font-size: 11px; line-height: 1.5; text-decoration: none; cursor: pointer;
    &:hover { background: var(--el-fill-color); color: var(--el-text-color-primary); }
  }
  .cc-primary-action { color: var(--pg-link-color); }
  .cc-copy-done { color: var(--el-color-success); }
  .cc-copy-status { margin-top: 7px; font-size: 11px; }
  .cc-state { display: flex; align-items: flex-start; gap: 9px; margin: 13px 0 3px; color: var(--el-text-color-secondary); font-size: 12px; }
  .cc-spinner { width: 12px; height: 12px; flex: 0 0 12px; margin-top: 3px; border: 2px solid var(--el-border-color); border-top-color: var(--el-color-primary); border-radius: 50%; animation: cite-spin 900ms linear infinite; }
}
@keyframes cite-card-in { from { opacity: 0; transform: translateY(2px); } }
@keyframes cite-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .cite-card, .cite-card .cc-spinner { animation: none; } }
</style>
