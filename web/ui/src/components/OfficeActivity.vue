<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch, type CSSProperties } from "vue";
import { useI18n } from "vue-i18n";
import { officeActivityChangedCount, officeActivityHeadline, officeActivityIsChange, officeActivityShownChanges, officeActivityText, officeActivityTotalChanges, officeActivityWindow, type OfficeActivityItem } from "@/agent/officeActivityView";

const props = defineProps<{ items: readonly OfficeActivityItem[]; totalCount?: number; hasVersions?: boolean }>();
const { t, locale } = useI18n();
const open = ref(false);
const changesOnly = ref(false);
const activeTab = ref<"activity" | "versions">("activity");
const root = ref<HTMLElement | null>(null);
const trigger = ref<HTMLButtonElement | null>(null);
const panel = ref<HTMLElement | null>(null);
const panelStyle = ref<CSSProperties>({ visibility: "hidden" });
let layoutFrame = 0;
let anchorObserver: IntersectionObserver | undefined;
const panelId = `office-activity-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
const head = computed(() => officeActivityHeadline(props.items));
const working = computed(() => head.value?.status === "pending" || head.value?.status === "running");
const changedCount = computed(() => officeActivityChangedCount(props.items));
const records = computed(() => officeActivityWindow(props.items, changesOnly.value));
const recordCount = computed(() => props.totalCount ?? props.items.length);
const matchingCount = computed(() => changesOnly.value ? props.items.filter(officeActivityIsChange).length : recordCount.value);
const label = computed(() => {
  const item = head.value;
  if (!item) return t(props.hasVersions ? "officeVersions.title" : "officeActivity.title");
  if (item.status === "pending") return t("officeActivity.pending");
  if (item.status === "running") return t(`officeActivity.${item.phase}`);
  if (item.status === "rejected" || item.status === "unknown") return t(`officeActivity.${item.status}`);
  if (changedCount.value > 0) return t("officeActivity.changed", { count: changedCount.value });
  const key = { reading: "readDone", writing: "writeDone", verifying: "verifyDone", saving: "saveDone" }[item.phase];
  return t(`officeActivity.${key}`);
});
const operationLabels: Record<string, string> = { "word.read": "read", "word.replace": "replace", "sheet.read": "read", "sheet.apply": "apply", "sheet.list": "sheets", "host.export": "save", "save": "save", "verify": "verify", "history.undo": "undo", "history.redo": "redo" };
const phaseLabels = { reading: "read", writing: "write", verifying: "verify", saving: "save" };
const timeFormatter = computed(() => new Intl.DateTimeFormat(locale.value, { hour: "2-digit", minute: "2-digit", hour12: false }));
function operationLabel(item: OfficeActivityItem) { return t(`officeActivity.${item.phase === "verifying" ? "verify" : operationLabels[item.operation] || phaseLabels[item.phase]}`); }
function timeLabel(item: OfficeActivityItem) {
  const stamp = item.completedAt ?? item.startedAt;
  if (!stamp || !Number.isFinite(stamp)) return "";
  return timeFormatter.value.format(stamp);
}
function collapse(restoreFocus = false) { open.value = false; if (restoreFocus) trigger.value?.focus({ preventScroll: true }); }
function outside(event: PointerEvent) {
  const path = event.composedPath();
  if (!path.includes(root.value!) && !path.includes(panel.value!)) collapse();
}
function keydown(event: KeyboardEvent) { if (event.key === "Escape" && open.value) { event.stopPropagation(); collapse(true); } }
function positionPanel() {
  layoutFrame = 0;
  if (!open.value || !trigger.value) return;
  const bounds = trigger.value.getBoundingClientRect();
  if (!trigger.value.getClientRects().length || !bounds.width || !bounds.height || bounds.bottom <= 0 || bounds.top >= window.innerHeight || bounds.right <= 0 || bounds.left >= window.innerWidth) { collapse(); return; }
  const margin = 12, gap = 8, width = Math.min(420, Math.max(0, window.innerWidth - margin * 2));
  const below = window.innerHeight - bounds.bottom - gap - margin;
  const above = bounds.top - gap - margin;
  const upward = below < 180 && above > below;
  panelStyle.value = {
    left: `${Math.max(margin, Math.min(bounds.right - width, window.innerWidth - width - margin))}px`,
    width: `${width}px`,
    maxHeight: `${Math.max(0, Math.min(560, upward ? above : below))}px`,
    top: upward ? "auto" : `${Math.min(window.innerHeight - margin, Math.max(margin, bounds.bottom + gap))}px`,
    bottom: upward ? `${Math.max(margin, window.innerHeight - bounds.top + gap)}px` : "auto",
    visibility: "visible",
  };
}
function queuePosition(event?: Event) {
  if (event?.target instanceof Node && panel.value?.contains(event.target)) return;
  if (!layoutFrame) layoutFrame = window.requestAnimationFrame(positionPanel);
}
function blurClose() { collapse(); }
function stopListeners() {
  document.removeEventListener("pointerdown", outside, true);
  document.removeEventListener("keydown", keydown);
  document.removeEventListener("scroll", queuePosition, true);
  window.removeEventListener("resize", queuePosition);
  window.removeEventListener("blur", blurClose);
  anchorObserver?.disconnect();
  anchorObserver = undefined;
  if (layoutFrame) window.cancelAnimationFrame(layoutFrame);
  layoutFrame = 0;
}
watch(open, value => {
  if (!value) { stopListeners(); return; }
  if (!props.items.length && props.hasVersions) activeTab.value = "versions";
  positionPanel();
  if (!open.value) return;
  document.addEventListener("pointerdown", outside, true);
  document.addEventListener("keydown", keydown);
  document.addEventListener("scroll", queuePosition, { capture: true, passive: true });
  window.addEventListener("resize", queuePosition, { passive: true });
  window.addEventListener("blur", blurClose);
  if (typeof IntersectionObserver === "function" && trigger.value) {
    anchorObserver = new IntersectionObserver(entries => {
      if (entries.some(entry => !entry.isIntersecting || !entry.boundingClientRect.width || !entry.boundingClientRect.height)) collapse();
    });
    anchorObserver.observe(trigger.value);
  }
}, { flush: "post" });
watch(() => props.items.length, count => { if (!count && !props.hasVersions) collapse(); });
watch(() => props.hasVersions, value => { if (!value) activeTab.value = "activity"; });
onBeforeUnmount(stopListeners);
</script>

<template>
  <div v-if="items.length || hasVersions" ref="root" class="office-activity" :class="[{ working, expanded: open }, head?.status]" @keydown="keydown">
    <button ref="trigger" type="button" class="activity-trigger" :aria-label="`${t(hasVersions && !items.length ? 'officeVersions.title' : 'officeActivity.open')} · ${label}`" :aria-expanded="open" :aria-controls="panelId" @click="open = !open">
      <svg class="activity-symbol" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="m10 2 2.1 5.9L18 10l-5.9 2.1L10 18l-2.1-5.9L2 10l5.9-2.1L10 2Z" fill="currentColor" /><path d="m16.5 1 .65 1.85L19 3.5l-1.85.65L16.5 6l-.65-1.85L14 3.5l1.85-.65L16.5 1Z" fill="currentColor" /></svg>
      <span class="activity-label">{{ label }}</span>
      <span class="activity-divider" aria-hidden="true">·</span>
      <span class="activity-review">{{ t('officeActivity.view') }}</span>
      <svg class="activity-chevron" :class="{ rotated: open }" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="m5 3 5 5-5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
    <span class="activity-announcement" role="status" aria-live="polite" aria-atomic="true">{{ label }}</span>
    <Teleport to="body">
    <Transition name="activity-panel">
      <section v-if="open" :id="panelId" ref="panel" class="activity-panel" :style="panelStyle" :aria-label="t(activeTab === 'versions' ? 'officeVersions.title' : 'officeActivity.title')">
        <header class="activity-header">
          <div class="activity-title"><span class="activity-title-symbol" aria-hidden="true">✦</span><strong>{{ t(activeTab === 'versions' ? 'officeVersions.title' : 'officeActivity.title') }}</strong><span v-if="activeTab === 'activity'" class="activity-count">{{ t('officeActivity.records', { count: recordCount }) }}</span></div>
          <button type="button" class="activity-close" :title="t('officeActivity.close')" :aria-label="t('officeActivity.close')" @click="collapse(true)"><svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="m4 10 4-4 4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
        </header>
        <div v-if="hasVersions" class="activity-tabs" role="group" :aria-label="t('officeVersions.title')">
          <button type="button" :class="{ selected: activeTab === 'activity' }" :aria-pressed="activeTab === 'activity'" @click="activeTab = 'activity'">{{ t('officeVersions.activity') }}</button>
          <button type="button" :class="{ selected: activeTab === 'versions' }" :aria-pressed="activeTab === 'versions'" @click="activeTab = 'versions'">{{ t('officeVersions.title') }}</button>
        </div>
        <slot v-if="activeTab === 'versions' && hasVersions" name="versions" />
        <template v-else>
        <div class="activity-filters" role="group" :aria-label="t('officeActivity.title')">
          <button type="button" :class="{ selected: !changesOnly }" :aria-pressed="!changesOnly" @click="changesOnly = false">{{ t('officeActivity.all') }}</button>
          <button type="button" :class="{ selected: changesOnly }" :aria-pressed="changesOnly" @click="changesOnly = true">{{ t('officeActivity.changes') }}<span v-if="changedCount">{{ changedCount }}</span></button>
        </div>
        <ol v-if="records.length" class="activity-list">
          <li v-for="item in records" :key="item.id" class="activity-entry" :class="[item.status, { 'has-diff': item.changes?.length }]">
            <span class="entry-mark" aria-hidden="true"><svg v-if="item.status === 'succeeded'" viewBox="0 0 16 16" fill="none"><path d="m3 8 3 3 7-7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg><svg v-else-if="item.status === 'rejected' || item.status === 'unknown'" viewBox="0 0 16 16" fill="none"><path d="M8 3v6m0 3v.1" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg><span v-else class="entry-dot" /></span>
            <div class="entry-body">
              <div class="entry-heading"><strong>{{ operationLabel(item) }}</strong><span class="entry-status">{{ t(`officeActivity.${item.status}`) }}</span><time v-if="timeLabel(item)">{{ timeLabel(item) }}</time></div>
              <code v-if="item.location" class="entry-location">{{ officeActivityText(item.location, 120) }}</code>
              <p v-if="item.summary && item.status === 'succeeded'" class="entry-summary">{{ officeActivityText(item.summary, 200) }}</p>
              <p v-if="item.status === 'rejected' || item.status === 'unknown'" class="entry-error">{{ officeActivityText(item.error, 240) || t(item.status === 'unknown' ? 'officeActivity.uncertain' : 'officeActivity.noWrite') }}</p>
              <details v-if="item.changes?.length && item.status === 'succeeded'" class="entry-diff">
                <summary>{{ t('officeActivity.diff') }}<svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="m5 3 5 5-5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg></summary>
                <div v-for="(change, index) in item.changes.slice(0, 4)" :key="index" class="change-pair">
                  <code v-if="change.label" class="change-location">{{ officeActivityText(change.label, 120) }}</code>
                  <div v-if="change.before !== undefined" class="change-value before"><span class="change-prefix" aria-hidden="true">−</span><span class="activity-announcement">{{ t('officeActivity.before') }}: </span><span>{{ officeActivityText(change.before) || t('officeActivity.empty') }}</span></div>
                  <div v-if="change.after !== undefined" class="change-value after"><span class="change-prefix" aria-hidden="true">+</span><span class="activity-announcement">{{ t('officeActivity.after') }}: </span><span>{{ officeActivityText(change.after) || t('officeActivity.empty') }}</span></div>
                </div>
                <p v-if="officeActivityTotalChanges(item) > officeActivityShownChanges(item)" class="more-records">{{ t('officeActivity.moreChanges', { total: officeActivityTotalChanges(item), shown: officeActivityShownChanges(item) }) }}</p>
              </details>
              <p v-if="item.truncated && item.status === 'succeeded'" class="more-records">{{ t('officeActivity.excerpt') }}</p>
            </div>
          </li>
        </ol>
        <p v-else class="activity-empty">{{ t('officeActivity.noChanges') }}</p>
        <p v-if="matchingCount > records.length" class="more-records list-limit">{{ t('officeActivity.latest', { count: records.length }) }}</p>
        <footer class="activity-footer"><svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 1.5 13 3v4c0 3-3 5.6-5 6.5C6 12.6 3 10 3 7V3l5-1.5Z" stroke="currentColor" stroke-width="1.2" /><path d="m5.5 7.5 1.5 1.5 3.5-3.5" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" /></svg><span>{{ t('officeActivity.unsavedNote') }}</span></footer>
        </template>
      </section>
    </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.office-activity { --activity-accent: var(--el-color-primary); position: relative; display: inline-flex; flex: 0 1 auto; min-width: 0; color: var(--el-text-color-primary); font-size: 11px; }
.activity-trigger { display: inline-flex; align-items: center; gap: 6px; min-height: 26px; min-width: 0; max-width: 245px; padding: 3px 8px; border: 1px solid color-mix(in srgb, var(--activity-accent) 24%, var(--el-border-color-lighter)); border-radius: 999px; background: color-mix(in srgb, var(--activity-accent) 7%, var(--el-bg-color)); color: var(--activity-accent); font: inherit; cursor: pointer; transition: background .16s ease, border-color .16s ease; }
.activity-trigger:hover, .expanded .activity-trigger { background: color-mix(in srgb, var(--activity-accent) 13%, var(--el-bg-color)); border-color: color-mix(in srgb, var(--activity-accent) 43%, var(--el-border-color-lighter)); }
.activity-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }.activity-symbol { flex: 0 0 15px; width: 15px; height: 15px; }.activity-divider { opacity: .5; }.activity-review { white-space: nowrap; color: var(--el-text-color-secondary); font-weight: 400; }.activity-chevron { flex: 0 0 11px; width: 11px; height: 11px; transition: transform .16s ease; }.activity-chevron.rotated { transform: rotate(90deg); }
.office-activity.unknown, .office-activity.rejected { --activity-accent: var(--el-color-warning); }.working .activity-symbol { animation: activity-breathe 1.6s ease-in-out infinite; }
.activity-panel { position: fixed; z-index: 2100; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--el-border-color); border-radius: 16px; background: var(--el-bg-color-overlay, var(--el-bg-color)); color: var(--el-text-color-primary); font-size: 11px; box-shadow: 0 12px 40px rgba(0,0,0,.18), 0 2px 7px rgba(0,0,0,.07); }
.activity-header { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 13px 14px 10px; }.activity-title { display: flex; align-items: center; min-width: 0; gap: 7px; }.activity-title > strong { font-size: 12px; font-weight: 600; }.activity-title-symbol { color: var(--el-color-primary); font-size: 17px; }.activity-count { color: var(--el-text-color-secondary); font-size: 10px; white-space: nowrap; margin-left: 1px; }
.activity-close { display: grid; flex: 0 0 25px; place-items: center; width: 25px; height: 25px; padding: 5px; border: 0; border-radius: 7px; background: transparent; color: var(--el-text-color-secondary); cursor: pointer; }.activity-close:hover { background: var(--el-fill-color); }.activity-close svg { width: 16px; height: 16px; }
.activity-tabs { display: flex; flex: 0 0 auto; gap: 16px; margin: 0 14px 11px; border-bottom: 1px solid var(--el-border-color-lighter); }.activity-tabs button { padding: 3px 0 9px; margin-bottom: -1px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--el-text-color-secondary); font: inherit; font-size: 11px; cursor: pointer; }.activity-tabs button.selected { border-bottom-color: var(--el-color-primary); color: var(--el-text-color-primary); font-weight: 600; }
.activity-filters { display: flex; gap: 4px; margin: 0 14px 10px; padding: 3px; align-self: flex-start; border: 1px solid var(--el-border-color-lighter); border-radius: 9px; background: var(--el-fill-color-light); }.activity-filters > button { display: inline-flex; align-items: center; gap: 5px; border: 0; border-radius: 6px; background: transparent; padding: 4px 8px; color: var(--el-text-color-secondary); font: inherit; cursor: pointer; }.activity-filters > button.selected { background: var(--el-bg-color); color: var(--el-text-color-primary); box-shadow: 0 1px 3px rgba(0,0,0,.06); }.activity-filters > button > span { font-size: 10px; color: var(--el-color-primary); }
.activity-list { display: block; flex: 1 1 auto; min-height: 0; margin: 0; padding: 0 14px; overflow-y: auto; overscroll-behavior: contain; list-style: none; scrollbar-width: thin; }.activity-entry { position: relative; display: flex; gap: 10px; padding: 10px 0 13px; }.activity-entry + .activity-entry { border-top: 1px solid var(--el-border-color-lighter); }.entry-mark { display: grid; flex: 0 0 18px; place-items: center; align-self: flex-start; width: 18px; height: 18px; border-radius: 50%; background: var(--el-fill-color); color: var(--el-text-color-secondary); }.entry-mark svg { width: 12px; height: 12px; }.succeeded > .entry-mark { background: color-mix(in srgb, var(--el-color-success) 12%, var(--el-bg-color)); color: var(--el-color-success); }.rejected > .entry-mark, .unknown > .entry-mark { background: color-mix(in srgb, var(--el-color-warning) 13%, var(--el-bg-color)); color: var(--el-color-warning); }.running > .entry-mark { background: color-mix(in srgb, var(--el-color-primary) 13%, var(--el-bg-color)); color: var(--el-color-primary); }.entry-dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.entry-body { flex: 1; min-width: 0; }.entry-heading { display: flex; align-items: center; gap: 7px; min-height: 18px; line-height: 1.5; }.entry-heading strong { font-size: 11px; font-weight: 600; }.entry-heading time { flex: 0 0 auto; margin-left: auto; color: var(--el-text-color-placeholder); font-size: 10px; font-variant-numeric: tabular-nums; }.entry-status { font-size: 10px; color: var(--el-text-color-secondary); }.rejected .entry-status, .unknown .entry-status { color: var(--el-color-warning); }.running .entry-status { color: var(--el-color-primary); }.entry-location { display: block; color: var(--el-text-color-secondary); font-size: 10px; margin-top: 4px; overflow-wrap: anywhere; }.entry-summary { margin: 5px 0 0; color: var(--el-text-color-regular); line-height: 1.65; overflow-wrap: anywhere; }.entry-error { margin: 6px 0 0; padding: 7px 9px; border-radius: 7px; background: color-mix(in srgb, var(--el-color-warning) 8%, var(--el-bg-color)); color: var(--el-text-color-regular); line-height: 1.6; overflow-wrap: anywhere; }
.entry-diff { margin-top: 7px; }.entry-diff > summary { display: inline-flex; gap: 4px; align-items: center; list-style: none; color: var(--el-color-primary); font-size: 10px; cursor: pointer; }.entry-diff > summary::-webkit-details-marker { display: none; }.entry-diff > summary svg { width: 10px; height: 10px; transition: transform .15s ease; }.entry-diff[open] > summary svg { transform: rotate(90deg); }.change-pair { display: flex; flex-direction: column; gap: 3px; margin-top: 7px; }.change-location { display: block; font-size: 10px; color: var(--el-text-color-secondary); padding: 0 2px 2px; overflow-wrap: anywhere; }.change-value { display: flex; align-items: baseline; gap: 7px; padding: 6px 8px; border-radius: 6px; line-height: 1.55; overflow-wrap: anywhere; white-space: pre-wrap; }.change-value > span:last-child { flex: 1; min-width: 0; }.change-prefix { flex: 0 0 8px; font-weight: 700; user-select: none; }.change-value.before { color: var(--el-text-color-secondary); background: color-mix(in srgb, var(--el-color-danger) 6%, var(--el-bg-color)); }.change-value.before .change-prefix { color: var(--el-color-danger); }.change-value.after { color: var(--el-text-color-primary); background: color-mix(in srgb, var(--el-color-success) 9%, var(--el-bg-color)); }.change-value.after .change-prefix { color: var(--el-color-success); }
.more-records { margin: 7px 0 0; color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.55; }.list-limit { padding: 4px 14px 10px; }.activity-empty { margin: 0; padding: 20px 14px 24px; color: var(--el-text-color-secondary); line-height: 1.7; }.activity-footer { display: flex; flex: 0 0 auto; align-items: flex-start; gap: 7px; border-top: 1px solid var(--el-border-color-lighter); padding: 10px 14px; color: var(--el-text-color-secondary); font-size: 10px; line-height: 1.55; background: color-mix(in srgb, var(--el-fill-color-light) 45%, var(--el-bg-color)); }.activity-footer svg { flex: 0 0 13px; width: 13px; height: 13px; margin-top: 1px; }
.activity-announcement { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
button:focus-visible, summary:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }.activity-panel-enter-active, .activity-panel-leave-active { transition: opacity .16s ease, transform .16s ease; transform-origin: right top; }.activity-panel-enter-from, .activity-panel-leave-to { opacity: 0; transform: translateY(-5px) scale(.99); }
@keyframes activity-breathe { 0%, 100% { opacity: .65; transform: scale(.94); } 50% { opacity: 1; transform: scale(1); } }
@media (max-width: 540px) { .activity-review, .activity-divider { display: none; }.activity-trigger { max-width: 160px; gap: 4px; }.activity-count { display: none; } }
@media (prefers-reduced-motion: reduce) { .activity-trigger, .activity-chevron, .entry-diff > summary svg, .activity-panel-enter-active, .activity-panel-leave-active { transition: none; }.working .activity-symbol { animation: none; } }
</style>
