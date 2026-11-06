<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from "vue";
import { Check, CopyDocument, Download, FullScreen, Grid, Search } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";
import CodeBlock from "./CodeBlock.vue";
import { parseDataTable, serializeCSV, type DataTableFormat } from "./dataTable";

const props = defineProps<{ source: string; format: DataTableFormat }>();
const { t, locale } = useI18n();
const query = ref("");
const page = ref(1);
const pageSize = ref(10);
const sort = ref<{ id: string; direction: "asc" | "desc" } | null>(null);
const showSource = ref(false);
const fullscreen = ref(false);
const copied = ref(false);
const notice = ref("");
const shell = ref<HTMLElement | null>(null);
const fullscreenButton = ref<HTMLButtonElement | null>(null);
const descriptionId = `data-table-${useId()}-description`;
let previousFocus: HTMLElement | null = null;
let copyTimer: ReturnType<typeof setTimeout> | undefined;

const parsed = computed(() => parseDataTable(props.source, props.format));
const table = computed(() => parsed.value.ok ? parsed.value.table : null);
const collator = computed(() => new Intl.Collator(locale.value, { numeric: true, sensitivity: "base" }));

const filteredRows = computed(() => {
  const rows = table.value?.rows ?? [];
  const needle = query.value.trim().toLocaleLowerCase();
  if (!needle) return rows;
  return rows.filter(row => row.some(value => value.toLocaleLowerCase().includes(needle)));
});
const sortedRows = computed(() => {
  const rows = filteredRows.value;
  const current = sort.value;
  const currentTable = table.value;
  if (!current || !currentTable) return rows;
  const columnIndex = currentTable.columns.findIndex(column => column.id === current.id);
  if (columnIndex < 0) return rows;
  const direction = current.direction === "asc" ? 1 : -1;
  return rows
    .map((row, index) => ({ row, index }))
    .sort((a, b) => collator.value.compare(a.row[columnIndex], b.row[columnIndex]) * direction || a.index - b.index)
    .map(item => item.row);
});
const totalPages = computed(() => Math.max(1, Math.ceil(sortedRows.value.length / pageSize.value)));
const visibleRows = computed(() => sortedRows.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value));
const rangeStart = computed(() => sortedRows.value.length ? (page.value - 1) * pageSize.value + 1 : 0);
const rangeEnd = computed(() => Math.min(page.value * pageSize.value, sortedRows.value.length));

watch(query, () => { page.value = 1; });
watch(pageSize, () => { page.value = 1; });
watch(totalPages, value => { if (page.value > value) page.value = value; });
watch(() => props.source, () => {
  query.value = "";
  sort.value = null;
  page.value = 1;
  showSource.value = false;
  notice.value = "";
});

function toggleSort(id: string) {
  if (sort.value?.id !== id) sort.value = { id, direction: "asc" };
  else if (sort.value.direction === "asc") sort.value = { id, direction: "desc" };
  else sort.value = null;
  page.value = 1;
}
function ariaSort(id: string): "none" | "ascending" | "descending" {
  if (sort.value?.id !== id) return "none";
  return sort.value.direction === "asc" ? "ascending" : "descending";
}
function sortLabel(column: { id: string; label: string }): string {
  const state = sort.value?.id === column.id ? sort.value.direction : "none";
  return t(`dataTable.sort.${state}`, { column: column.label });
}
watch(fullscreen, async value => {
  if (value) {
    previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    await nextTick();
    fullscreenButton.value?.focus();
  } else {
    const target = previousFocus;
    previousFocus = null;
    target?.focus();
  }
});
async function copySource() {
  try {
    await navigator.clipboard.writeText(props.source);
    copied.value = true;
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => { copied.value = false; }, 1200);
  } catch {
    copied.value = false;
  }
}
function downloadCSV() {
  const current = table.value;
  if (!current) return;
  notice.value = "";
  try {
    const blob = new Blob(["\uFEFF", serializeCSV(current, sortedRows.value)], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `table-${new Date().toISOString().replace(/[:.]/g, "-")}.csv`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch {
    notice.value = t("dataTable.downloadFailed");
  }
}
async function toggleFullscreen() {
  const element = shell.value;
  if (!element) return;
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else if (element.requestFullscreen) await element.requestFullscreen();
    else fullscreen.value = !fullscreen.value;
  } catch {
    fullscreen.value = !fullscreen.value;
  }
}
function syncFullscreen() { fullscreen.value = document.fullscreenElement === shell.value; }
function onKeydown(event: KeyboardEvent) {
  if (!fullscreen.value) return;
  if (event.key === "Escape") {
    event.preventDefault();
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => { fullscreen.value = false; });
    else fullscreen.value = false;
    return;
  }
  if (event.key !== "Tab" || !shell.value) return;
  const focusable = Array.from(shell.value.querySelectorAll<HTMLElement>('button:not(:disabled), input, select, a[href], [tabindex="0"]'))
    .filter(element => element.offsetParent !== null);
  if (!focusable.length) {
    event.preventDefault();
    return;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}
onMounted(() => {
  document.addEventListener("fullscreenchange", syncFullscreen);
  document.addEventListener("keydown", onKeydown);
});
onBeforeUnmount(() => {
  clearTimeout(copyTimer);
  document.removeEventListener("fullscreenchange", syncFullscreen);
  document.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <section
    ref="shell"
    class="data-table"
    :class="{ 'is-fullscreen': fullscreen }"
    :role="fullscreen ? 'dialog' : undefined"
    :aria-modal="fullscreen ? 'true' : undefined"
    :aria-label="fullscreen ? t('dataTable.fullscreenTitle') : undefined"
  >
    <template v-if="table">
      <header class="table-bar">
        <div class="table-title">
          <el-icon aria-hidden="true"><Grid /></el-icon>
          <strong>{{ t('dataTable.title') }}</strong>
          <span>{{ t('dataTable.shape', { rows: table.rows.length, columns: table.columns.length }) }}</span>
        </div>
        <div class="table-actions">
          <button type="button" @click="showSource = !showSource">{{ showSource ? t('dataTable.showTable') : t('dataTable.showSource') }}</button>
          <button type="button" @click="copySource"><el-icon aria-hidden="true"><Check v-if="copied" /><CopyDocument v-else /></el-icon>{{ copied ? t('dataTable.copied') : t('dataTable.copy') }}</button>
          <button type="button" @click="downloadCSV"><el-icon aria-hidden="true"><Download /></el-icon>{{ t('dataTable.download') }}</button>
          <button ref="fullscreenButton" type="button" @click="toggleFullscreen"><el-icon aria-hidden="true"><FullScreen /></el-icon>{{ fullscreen ? t('dataTable.exitFullscreen') : t('dataTable.fullscreen') }}</button>
        </div>
      </header>

      <div v-if="!showSource" class="table-tools">
        <label class="table-search">
          <el-icon aria-hidden="true"><Search /></el-icon>
          <input v-model="query" type="search" :placeholder="t('dataTable.search')" :aria-label="t('dataTable.search')" />
        </label>
        <label class="page-size">{{ t('dataTable.pageSize') }}
          <select v-model.number="pageSize">
            <option :value="10">10</option><option :value="25">25</option><option :value="50">50</option><option :value="100">100</option>
          </select>
        </label>
      </div>

      <CodeBlock v-if="showSource" :code="source" :lang="format === 'csv' ? 'csv' : 'json'" :collapsed-height="360" />
      <template v-else>
        <p v-if="notice" class="table-notice" role="status">{{ notice }}</p>
        <p :id="descriptionId" class="sr-only">{{ t('dataTable.keyboardHelp') }}</p>
        <div class="table-scroll" tabindex="0" :aria-label="t('dataTable.title')" :aria-describedby="descriptionId">
          <table>
            <caption class="sr-only">{{ t('dataTable.caption', { rows: table.rows.length, columns: table.columns.length }) }}</caption>
            <thead>
              <tr>
                <th class="row-number" scope="col">#</th>
                <th v-for="column in table.columns" :key="column.id" scope="col" :aria-sort="ariaSort(column.id)">
                  <button type="button" class="sort-button" :aria-label="sortLabel(column)" @click="toggleSort(column.id)">
                    <span>{{ column.label }}</span>
                    <span class="sort-indicator" aria-hidden="true">{{ sort?.id === column.id ? (sort.direction === 'asc' ? '↑' : '↓') : '↕' }}</span>
                  </button>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, rowIndex) in visibleRows" :key="rangeStart + rowIndex">
                <th class="row-number" scope="row">{{ rangeStart + rowIndex }}</th>
                <td v-for="(value, columnIndex) in row" :key="table.columns[columnIndex].id" :title="value"><span>{{ value || '—' }}</span></td>
              </tr>
              <tr v-if="!visibleRows.length">
                <td class="empty-result" :colspan="table.columns.length + 1">{{ t('dataTable.noResults') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <footer class="table-footer">
          <span aria-live="polite" aria-atomic="true">{{ t('dataTable.range', { start: rangeStart, end: rangeEnd, total: sortedRows.length }) }}</span>
          <div class="pagination">
            <button type="button" :disabled="page <= 1" :aria-label="t('dataTable.previous')" @click="page--">‹</button>
            <span>{{ t('dataTable.page', { page, pages: totalPages }) }}</span>
            <button type="button" :disabled="page >= totalPages" :aria-label="t('dataTable.next')" @click="page++">›</button>
          </div>
        </footer>
      </template>
    </template>

    <div v-else class="table-fallback">
      <p role="alert">{{ t(`dataTable.${parsed.ok ? 'empty' : parsed.reason}`) }}</p>
      <CodeBlock :code="source" :lang="format === 'csv' ? 'csv' : 'json'" :collapsed-height="280" />
    </div>
  </section>
</template>

<style scoped>
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
.data-table {
  position: relative;
  min-width: 0;
  margin: 0.85em 0;
  overflow: hidden;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
}
.data-table.is-fullscreen {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 12px;
  border-radius: 0;
}
.data-table:fullscreen { display: flex; flex-direction: column; padding: 12px; }
.table-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px 14px;
  padding: 8px 10px 8px 14px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  background: var(--el-fill-color-lighter);
}
.table-title, .table-actions, .table-tools, .table-footer, .pagination {
  display: flex;
  align-items: center;
}
.table-title { min-width: 0; gap: 7px; font-size: 12px; }
.table-title > .el-icon { color: var(--el-color-primary); }
.table-title > span { color: var(--el-text-color-secondary); font-size: 11px; font-weight: 400; }
.table-actions { flex-wrap: wrap; gap: 3px; }
.table-actions button, .pagination button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  min-height: 27px;
  padding: 4px 7px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}
.table-actions button:hover, .pagination button:not(:disabled):hover { background: var(--el-fill-color); color: var(--el-text-color-primary); }
.table-tools {
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
  padding: 9px 12px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}
.table-search {
  display: flex;
  align-items: center;
  flex: 1 1 220px;
  max-width: 360px;
  gap: 7px;
  padding: 6px 9px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  color: var(--el-text-color-secondary);
}
.table-search:focus-within { border-color: var(--el-color-primary); box-shadow: var(--pg-ring); }
.table-search input { width: 100%; min-width: 0; border: 0; outline: 0; background: transparent; color: inherit; font: inherit; font-size: 12px; }
.page-size { display: inline-flex; align-items: center; gap: 6px; color: var(--el-text-color-secondary); font-size: 11px; }
.page-size select { padding: 4px 6px; border: 1px solid var(--el-border-color-light); border-radius: 5px; background: var(--el-bg-color); color: inherit; }
.table-scroll { max-width: 100%; max-height: 520px; overflow: auto; outline: none; scrollbar-width: thin; }
.table-scroll:focus-visible { box-shadow: inset var(--pg-ring); }
.is-fullscreen .table-scroll, .data-table:fullscreen .table-scroll { flex: 1; max-height: none; }
table { width: 100%; min-width: max-content; border-collapse: separate; border-spacing: 0; font-size: 12px; }
thead { position: sticky; top: 0; z-index: 2; }
th, td { padding: 9px 12px; border-bottom: 1px solid var(--el-border-color-extra-light); text-align: left; vertical-align: top; }
thead th { padding: 0; background: var(--el-fill-color-light); color: var(--el-text-color-regular); font-weight: 600; white-space: nowrap; }
.sort-button {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  min-width: 110px;
  gap: 12px;
  padding: 9px 12px;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
  text-align: left;
}
.sort-button:hover { color: var(--el-color-primary); background: var(--el-fill-color); }
.sort-indicator { color: var(--el-color-primary); font-size: 12px; }
tbody tr:nth-child(even) { background: var(--el-fill-color-lighter); }
tbody tr:hover { background: var(--pg-accent-wash); }
tbody tr:last-child > th, tbody tr:last-child > td { border-bottom: 0; }
td { max-width: 300px; color: var(--el-text-color-regular); }
td > span {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.row-number { min-width: 44px; width: 44px; color: var(--el-text-color-placeholder); text-align: right; font-variant-numeric: tabular-nums; font-weight: 400; }
.empty-result { padding: 34px 16px; color: var(--el-text-color-secondary); text-align: center; }
.table-footer {
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
  padding: 7px 12px;
  border-top: 1px solid var(--el-border-color-extra-light);
  color: var(--el-text-color-secondary);
  font-size: 11px;
}
.pagination { gap: 6px; }
.pagination button { min-width: 27px; padding: 2px 7px; font-size: 18px; line-height: 1; }
.pagination button:disabled { opacity: 0.35; cursor: not-allowed; }
.table-notice { margin: 8px 12px; color: var(--el-color-warning); font-size: 12px; }
.table-fallback { padding: 12px; }
.table-fallback > p { margin: 0 0 8px; color: var(--el-color-warning); font-size: 12px; }
@media (max-width: 600px) {
  .table-bar { align-items: flex-start; }
  .table-actions { width: 100%; }
  .table-actions button { padding: 4px 6px; }
  .table-search { max-width: none; }
  td { max-width: 220px; }
}
</style>
