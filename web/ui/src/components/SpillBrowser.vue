<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, ArrowRight, Document, Folder, Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { SpillEntries, SpillEntry, SpillFile } from "@/api/types";

const props = defineProps<{ modelValue: boolean; root: string }>();
const emit = defineEmits<{ "update:modelValue": [boolean] }>();
const { t } = useI18n();
const visible = computed({ get: () => props.modelValue, set: value => emit("update:modelValue", value) });
const directory = ref<SpillEntries | null>(null), file = ref<SpillFile | null>(null);
const directoryLoading = ref(false), fileLoading = ref(false);
const directoryError = ref(""), fileError = ref("");
interface FilePagePosition { offset: number; startLine: number; continued: boolean }
interface FileIdentity { size: number; modifiedAt: string }
const firstFilePage = (): FilePagePosition => ({ offset: 0, startLine: 1, continued: false });
const directoryHistory = ref<number[]>([]), fileHistory = ref<FilePagePosition[]>([]);
const filePosition = ref<FilePagePosition>(firstFilePage());
const selectedPath = ref("");
let directoryRequest: AbortController | undefined, fileRequest: AbortController | undefined;
let directoryVersion = 0, fileVersion = 0;
let requestedDirectory = { path: "", offset: 0, history: [] as number[] };
const breadcrumbs = computed(() => {
  const parts = directory.value?.path.split("/").filter(Boolean) ?? [];
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
});
const shownRoot = computed(() => directory.value?.root || props.root);
const fullDirectory = computed(() => [shownRoot.value.replace(/\/$/, ""), directory.value?.path].filter(Boolean).join("/"));
const fullFile = computed(() => file.value ? `${file.value.root.replace(/\/$/, "")}/${file.value.path}` : "");
const fileLines = computed(() => {
  if (!file.value || file.value.binary) return [];
  const parts = file.value.content.split("\n");
  // A trailing LF starts the next page's first line. Show the empty final
  // line only at EOF, rather than showing it twice across page boundaries.
  if (file.value.next_offset !== null && file.value.content.endsWith("\n")) parts.pop();
  return parts.map((text, index) => ({ number: filePosition.value.startLine + index, text: text.replace(/\r$/, ""), continued: index === 0 && filePosition.value.continued }));
});
const lineNumberWidth = computed(() => `${String(fileLines.value[fileLines.value.length - 1]?.number ?? 1).length + 1}ch`);
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
}
function clearFile() {
  fileVersion++; fileRequest?.abort(); fileRequest = undefined;
  selectedPath.value = ""; file.value = null; fileError.value = ""; fileLoading.value = false; fileHistory.value = []; filePosition.value = firstFilePage();
}
async function loadDirectory(path = "", offset = 0, history: number[] = []) {
  requestedDirectory = { path, offset, history: [...history] };
  const version = ++directoryVersion;
  directoryRequest?.abort(); directoryRequest = new AbortController();
  const request = directoryRequest;
  directoryLoading.value = true; directoryError.value = ""; clearFile();
  try {
    const result = await api.spillEntries(path, offset, request.signal);
    if (version !== directoryVersion || !props.modelValue) return;
    directory.value = result; directoryHistory.value = history;
  } catch (error) {
    if (version === directoryVersion && !request.signal.aborted && props.modelValue) directoryError.value = message(error);
  } finally {
    if (version === directoryVersion) directoryLoading.value = false;
  }
}
function retryDirectory() { return loadDirectory(requestedDirectory.path, requestedDirectory.offset, requestedDirectory.history); }
async function loadFile(path: string, position = firstFilePage(), history: FilePagePosition[] = [], expected?: FileIdentity) {
  const version = ++fileVersion;
  fileRequest?.abort(); fileRequest = new AbortController();
  const request = fileRequest;
  selectedPath.value = path; file.value = null; fileLoading.value = true; fileError.value = "";
  filePosition.value = firstFilePage(); fileHistory.value = [];
  try {
    const result = await api.spillFile(path, position.offset, request.signal);
    if (version !== fileVersion || !props.modelValue) return;
    if (expected && (result.size !== expected.size || result.modified_at !== expected.modifiedAt)) {
      fileError.value = t("spillBrowser.fileChanged");
      return;
    }
    file.value = result; fileHistory.value = history; filePosition.value = { ...position };
  } catch (error) {
    if (version === fileVersion && !request.signal.aborted && props.modelValue) fileError.value = message(error);
  } finally {
    if (version === fileVersion) fileLoading.value = false;
  }
}
function select(entry: SpillEntry) {
  if (entry.type === "directory") return loadDirectory(entry.path);
  return loadFile(entry.path);
}
function directoryPage(next: boolean) {
  const current = directory.value;
  if (!current || directoryLoading.value) return;
  if (next && current.next_offset !== null) return loadDirectory(current.path, current.next_offset, [...directoryHistory.value, current.offset]);
  if (!next && directoryHistory.value.length) return loadDirectory(current.path, directoryHistory.value[directoryHistory.value.length - 1]!, directoryHistory.value.slice(0, -1));
}
function filePage(next: boolean) {
  const current = file.value;
  if (!current || fileLoading.value) return;
  const expected = { size: current.size, modifiedAt: current.modified_at };
  if (next && current.next_offset !== null) {
    const position = { offset: current.next_offset, startLine: filePosition.value.startLine + (current.content.match(/\n/g)?.length ?? 0), continued: !!current.content && !current.content.endsWith("\n") };
    return loadFile(current.path, position, [...fileHistory.value, { ...filePosition.value }], expected);
  }
  if (!next && fileHistory.value.length) return loadFile(current.path, fileHistory.value[fileHistory.value.length - 1]!, fileHistory.value.slice(0, -1), expected);
}
function stop() {
  directoryVersion++; directoryRequest?.abort(); directoryRequest = undefined;
  directoryLoading.value = false; clearFile();
}
watch(() => [props.modelValue, props.root] as const, ([open]) => {
  if (open) { directory.value = null; directoryHistory.value = []; void loadDirectory(); }
  else stop();
}, { immediate: true });
onBeforeUnmount(stop);
</script>

<template>
  <el-dialog v-model="visible" :title="t('spillBrowser.title')" width="min(1100px, calc(100vw - 24px))" top="6vh" append-to-body class="spill-browser-dialog">
    <section class="spill-browser" data-testid="spill-browser">
      <p class="read-only-note">{{ t('spillBrowser.readOnly') }}</p>
      <p class="root-path"><span>{{ t('spillBrowser.rootLabel') }}</span><code>{{ shownRoot }}</code></p>
      <nav class="breadcrumb" :aria-label="t('spillBrowser.breadcrumb')">
        <button type="button" :disabled="directoryLoading || !directory?.path" @click="loadDirectory()">Spill</button>
        <template v-for="crumb in breadcrumbs" :key="crumb.path"><span aria-hidden="true">/</span><button type="button" :disabled="directoryLoading || crumb.path === directory?.path" @click="loadDirectory(crumb.path)">{{ crumb.name }}</button></template>
      </nav>
      <div class="browser-workspace">
        <aside class="directory-pane" :aria-label="t('spillBrowser.files')" :aria-busy="directoryLoading">
          <header class="pane-toolbar">
            <button type="button" data-testid="spill-directory-parent" :disabled="directoryLoading || !directory?.path" @click="loadDirectory(directory?.parent)"><el-icon><ArrowLeft /></el-icon>{{ t('spillBrowser.parent') }}</button>
            <button type="button" :disabled="directoryLoading" :aria-label="t('spillBrowser.refresh')" @click="loadDirectory(directory?.path, directory?.offset, directoryHistory)"><el-icon><Refresh /></el-icon></button>
          </header>
          <div class="directory-location" :title="fullDirectory">{{ fullDirectory }}</div>
          <p v-if="directoryLoading" class="pane-state" role="status">{{ t('spillBrowser.loading') }}</p>
          <div v-else-if="directoryError" class="pane-state bad" role="alert">{{ directoryError }}<button type="button" @click="retryDirectory">{{ t('spillBrowser.retry') }}</button></div>
          <template v-else-if="directory">
            <p v-if="!directory.exists" class="pane-state">{{ t('spillBrowser.notCreated') }}</p>
            <p v-else-if="!directory.entries.length" class="pane-state">{{ t('spillBrowser.empty') }}</p>
            <ul v-else class="entry-list">
              <li v-for="entry in directory.entries" :key="entry.path"><button type="button" class="entry" :class="{ selected: selectedPath === entry.path }" :title="entry.path" :aria-pressed="entry.type === 'file' ? selectedPath === entry.path : undefined" :data-spill-entry="entry.path" @click="select(entry)">
                <el-icon><Folder v-if="entry.type === 'directory'" /><Document v-else /></el-icon><span class="entry-name">{{ entry.name }}</span><span v-if="entry.type === 'file'" class="entry-size">{{ formatBytes(entry.size) }}</span><el-icon v-else class="entry-arrow"><ArrowRight /></el-icon>
              </button></li>
            </ul>
            <p v-if="directory.truncated" class="pane-note">{{ t('spillBrowser.directoryPartial') }}</p>
            <p v-if="directory.skipped" class="pane-note">{{ t('spillBrowser.skipped', { n: directory.skipped }) }}</p>
            <p v-if="directory.scan_limit_reached" class="pane-note caution">{{ t('spillBrowser.scanLimit') }}</p>
          </template>
          <footer v-if="directory" class="page-controls">
            <button type="button" data-testid="spill-directory-previous" :disabled="directoryLoading || !directoryHistory.length" @click="directoryPage(false)">{{ t('spillBrowser.previous') }}</button>
            <span>{{ t('spillBrowser.entriesCount', { n: directory.entries.length }) }}</span>
            <button type="button" data-testid="spill-directory-next" :disabled="directoryLoading || directory.next_offset === null" @click="directoryPage(true)">{{ t('spillBrowser.next') }}</button>
          </footer>
        </aside>
        <main class="file-pane" :aria-label="t('spillBrowser.preview')" :aria-busy="fileLoading">
          <p v-if="fileLoading" class="pane-state" role="status">{{ t('spillBrowser.loading') }}</p>
          <div v-else-if="fileError" class="pane-state bad" role="alert">{{ fileError }}<button type="button" @click="loadFile(selectedPath)">{{ t('spillBrowser.retry') }}</button></div>
          <template v-else-if="file">
            <header class="file-header"><strong>{{ file.name }}</strong><code>{{ fullFile }}</code><span>{{ formatBytes(file.size) }}<template v-if="file.modified_at"> · {{ new Date(file.modified_at).toLocaleString() }}</template></span></header>
            <p v-if="file.binary" class="pane-state">{{ t('spillBrowser.binary') }}</p>
            <template v-else>
              <p v-if="!file.content" class="pane-note">{{ t('spillBrowser.emptyFile') }}</p>
              <p v-if="filePosition.continued" class="pane-note">{{ t('spillBrowser.continuedLine', { n: filePosition.startLine }) }}</p>
              <!-- Each source line owns its gutter, so wrapped text keeps one
                   line number. Vue interpolation keeps HTML and scripts inert. -->
              <pre :key="`${file.path}:${file.offset}`" class="file-content" :style="{ '--spill-line-width': lineNumberWidth }" tabindex="0" data-testid="spill-file-content"><span v-for="line in fileLines" :key="line.number" class="file-line" :data-line="line.number"><span class="spill-line-number" aria-hidden="true" :title="line.continued ? t('spillBrowser.continuedLine', { n: line.number }) : undefined">{{ line.number }}</span><span class="spill-line-content">{{ line.text }}</span></span></pre>
            </template>
            <footer v-if="!file.binary" class="file-footer">
              <p v-if="file.truncated" class="pane-note">{{ t('spillBrowser.filePartial') }}</p>
              <div class="page-controls"><button type="button" data-testid="spill-file-previous" :disabled="!fileHistory.length" @click="filePage(false)">{{ t('spillBrowser.previous') }}</button><span>{{ t('spillBrowser.byteRange', { start: file.offset, end: file.offset + file.bytes, total: file.size }) }}</span><button type="button" data-testid="spill-file-next" :disabled="file.next_offset === null" @click="filePage(true)">{{ t('spillBrowser.next') }}</button></div>
            </footer>
          </template>
          <p v-else class="pane-state preview-empty">{{ t('spillBrowser.selectFile') }}</p>
        </main>
      </div>
    </section>
    <template #footer><el-button @click="visible = false">{{ t('common.close') }}</el-button></template>
  </el-dialog>
</template>

<style scoped>
.spill-browser{color:var(--el-text-color-regular);min-width:0}.read-only-note{margin:0 0 10px;color:var(--el-text-color-secondary);font-size:13px;line-height:1.6}.root-path{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 12px;font-size:12px}.root-path code{overflow-wrap:anywhere;color:var(--el-text-color-primary)}
button{font:inherit;font-size:12px;color:var(--el-text-color-regular);background:var(--el-bg-color);border:1px solid var(--el-border-color);border-radius:5px;padding:5px 8px;cursor:pointer}button:hover:not(:disabled){color:var(--el-color-primary);border-color:var(--el-color-primary-light-5);background:var(--el-color-primary-light-9)}button:disabled{cursor:default;color:var(--el-text-color-placeholder)}button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}
.breadcrumb{display:flex;flex-wrap:wrap;align-items:center;gap:6px;margin:0 0 10px;max-height:90px;overflow:auto}.breadcrumb button{border:0;background:var(--el-fill-color-light);max-width:100%;overflow-wrap:anywhere;text-align:left}.breadcrumb>span{color:var(--el-text-color-placeholder)}
.browser-workspace{display:grid;grid-template-columns:minmax(240px,32%) minmax(0,1fr);height:min(62vh,620px);min-height:320px;border:1px solid var(--el-border-color);border-radius:8px;overflow:hidden}.directory-pane,.file-pane{min-width:0;min-height:0;display:flex;flex-direction:column;overflow:auto}.directory-pane{border-right:1px solid var(--el-border-color);background:var(--el-fill-color-lighter)}.pane-toolbar{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:9px;border-bottom:1px solid var(--el-border-color-lighter)}.pane-toolbar button{display:inline-flex;align-items:center;gap:5px}.directory-location{font:11px/1.5 var(--el-font-family);padding:8px 10px;color:var(--el-text-color-secondary);overflow-wrap:anywhere;border-bottom:1px solid var(--el-border-color-lighter)}.entry-list{list-style:none;padding:5px;margin:0;flex:1;overflow:auto;min-height:80px}.entry{width:100%;display:flex;align-items:center;gap:7px;min-height:34px;border:0;background:none;text-align:left}.entry-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.entry-size{font-size:10px;color:var(--el-text-color-secondary);white-space:nowrap}.entry-arrow{color:var(--el-text-color-placeholder)}.entry.selected{background:var(--el-color-primary-light-9);color:var(--el-color-primary);box-shadow:inset 0 0 0 1px var(--el-color-primary-light-7)}.entry>.el-icon{flex:0 0 auto}
.pane-state{display:flex;align-items:center;gap:10px;margin:0;padding:20px 14px;font-size:13px;line-height:1.7;overflow-wrap:anywhere}.pane-state.bad{color:var(--el-color-danger)}.pane-state button{flex:0 0 auto}.pane-note{font-size:11px;line-height:1.6;color:var(--el-text-color-secondary);margin:0;padding:6px 10px}.pane-note.caution{color:var(--el-color-warning-dark-2)}.page-controls{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:9px;border-top:1px solid var(--el-border-color-lighter);font-size:11px;color:var(--el-text-color-secondary);margin-top:auto}.page-controls>span{overflow-wrap:anywhere;text-align:center}.page-controls button{white-space:nowrap}.file-header{display:flex;flex-direction:column;gap:6px;padding:12px 14px;border-bottom:1px solid var(--el-border-color-lighter);font-size:12px}.file-header code{font-size:11px;overflow-wrap:anywhere}.file-header>span{color:var(--el-text-color-secondary);font-size:11px}.file-content{font:12px/1.7 var(--pg-font-mono,ui-monospace,SFMono-Regular,Menlo,monospace);white-space:pre-wrap;overflow-wrap:anywhere;padding:14px;margin:0;min-height:150px;flex:1;overflow:auto;tab-size:2;background:var(--el-bg-color)}.preview-empty{justify-content:center;flex:1;color:var(--el-text-color-placeholder)}.file-footer{background:var(--el-bg-color);flex:0 0 auto}
.file-content{padding:10px 0;white-space:normal}.file-line{display:grid;grid-template-columns:calc(var(--spill-line-width,2ch) + 9px) minmax(0,1fr);column-gap:12px;min-height:1.7em;padding:0 14px 0 10px}.spill-line-number{box-sizing:border-box;white-space:nowrap;overflow-wrap:normal;word-break:normal;color:var(--el-text-color-placeholder);text-align:right;border-right:1px solid var(--el-border-color-lighter);padding-right:8px;user-select:none;-webkit-user-select:none}.spill-line-content{min-width:0;min-height:1.7em;white-space:pre-wrap;overflow-wrap:anywhere;user-select:text}
@media(max-width:700px){.browser-workspace{grid-template-columns:minmax(0,1fr);height:min(50vh,480px);min-height:320px;grid-template-rows:minmax(150px,40%) minmax(0,1fr)}.directory-pane{border-right:0;border-bottom:1px solid var(--el-border-color)}.file-content{min-height:80px}}
</style>

<style>
.el-dialog.spill-browser-dialog{max-width:calc(100vw - 32px);max-height:88vh;max-height:calc(100dvh - 12vh);margin-top:6vh;display:flex;flex-direction:column;overflow:hidden}
.spill-browser-dialog>.el-dialog__header,.spill-browser-dialog>.el-dialog__footer{flex:0 0 auto}
.spill-browser-dialog>.el-dialog__body{min-height:0;overflow:auto}
</style>
