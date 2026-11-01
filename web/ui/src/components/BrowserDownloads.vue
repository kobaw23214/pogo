<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElPopover } from "element-plus";
import type { BrowserDownload } from "@/agent/nativeBrowser";
import { browserDownloadSize, browserDownloadSizeSummary, browserDownloadStatus } from "@/agent/browserDownloads";

const props = defineProps<{ items: BrowserDownload[]; active: boolean; rejected?: number; rejection?: string; maxFileBytes?: number }>();
const emit = defineEmits<{ openFile: [path: string] }>();
const { t } = useI18n();
const visible = ref(false);
const recent = computed(() => props.items.slice(-20).reverse());
const pending = computed(() => props.items.some(item => item.state === "in_progress" || item.state === "completed"));
const failed = computed(() => !!props.rejected || props.items.some(item => item.state === "failed"));
watch(() => props.active, active => { if (!active) visible.value = false; });
function progress(item: BrowserDownload) { return item.total_bytes > 0 ? Math.min(100, Math.round(item.received_bytes * 100 / item.total_bytes)) : undefined; }
function open(item: BrowserDownload) {
  if (item.state !== "saved" || !item.path) return;
  visible.value = false;
  emit("openFile", item.path);
}
</script>

<template>
  <ElPopover v-if="items.length || rejected" v-model:visible="visible" trigger="click" placement="bottom-end" :width="352" :teleported="true" popper-class="browser-download-popover">
    <template #reference>
      <button class="download-toggle" :class="{ pending, failed }" type="button" :title="t('browserDownloads.title')" :aria-label="t('browserDownloads.label', { count: items.length })" :aria-expanded="visible">
        <svg viewBox="0 0 20 20" width="19" height="19" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 2v10m-4-4 4 4 4-4M3 13v4h14v-4" /></svg>
        <span class="download-dot" aria-hidden="true" />
      </button>
    </template>
    <section class="downloads-panel" :aria-label="t('browserDownloads.title')" @keydown.esc.stop="visible = false">
      <header><strong>{{ t('browserDownloads.title') }}</strong><button class="close" type="button" :aria-label="t('browserDownloads.close')" @click="visible = false">×</button></header>
      <p class="hint">{{ t('browserDownloads.hint') }}</p>
      <p v-if="maxFileBytes && maxFileBytes > 0" class="hint limit">{{ t('browserDownloads.limit', { size: browserDownloadSize(maxFileBytes) }) }}</p>
      <p v-if="rejected" class="download-error" role="status">{{ t('browserDownloads.rejected', { count: rejected }) }}<span v-if="rejection"> · {{ rejection }}</span></p>
      <ul>
        <li v-for="item in recent" :key="item.id" :class="{ error: item.state === 'failed' }">
          <button class="file" type="button" :disabled="item.state !== 'saved' || !item.path" :title="item.state === 'saved' ? t('browserDownloads.open') : item.filename" @click="open(item)">
            <svg viewBox="0 0 20 20" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" aria-hidden="true"><path d="M5 2h7l4 4v12H4V2h1Zm7 0v5h4M7 11h6m-6 3h6" /></svg>
            <span class="file-copy"><strong>{{ item.filename }}</strong><span>{{ t(`browserDownloads.${browserDownloadStatus(item)}`) }}<template v-if="browserDownloadSizeSummary(item)"> · {{ browserDownloadSizeSummary(item) }}</template></span></span>
            <span v-if="item.state === 'saved'" class="open-arrow" aria-hidden="true">↗</span>
          </button>
          <progress v-if="item.state === 'in_progress'" :value="progress(item)" max="100" :aria-label="t('browserDownloads.in_progress')" />
          <p v-if="item.error" class="download-error">{{ item.error }}</p>
        </li>
      </ul>
    </section>
  </ElPopover>
</template>

<style scoped>
.download-toggle { position:relative; display:grid; place-items:center; flex-shrink:0; width:34px; height:34px; padding:0; border:0; border-radius:8px; color:var(--el-text-color-regular); background:transparent; cursor:pointer; }
.download-toggle:hover, .download-toggle[aria-expanded="true"] { background:var(--el-fill-color-light); }
.download-toggle:focus-visible, button:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
.download-dot { position:absolute; right:3px; top:3px; width:5px; height:5px; border-radius:50%; background:var(--el-color-success); }
.pending .download-dot { background:var(--el-color-primary); }.failed .download-dot { background:var(--el-color-warning); }
.downloads-panel { color:var(--el-text-color-primary); font-size:12px; }
header { display:flex; align-items:center; justify-content:space-between; font-size:14px; }.close { border:0; background:transparent; color:var(--el-text-color-secondary); border-radius:6px; width:26px; height:26px; font-size:20px; cursor:pointer; }
.hint { color:var(--el-text-color-secondary); line-height:1.5; margin:6px 0 12px; }
.hint.limit { margin-top:-6px; font-size:11px; }
ul { max-height:min(380px, 55vh); overflow:auto; padding:0; margin:0; list-style:none; }li { padding:5px 0; }li + li { border-top:1px solid var(--el-border-color-lighter); }
.file { display:flex; gap:10px; align-items:center; text-align:left; width:100%; border:0; border-radius:8px; padding:8px; background:transparent; color:inherit; font:inherit; cursor:pointer; }.file:hover:not(:disabled) { background:var(--el-fill-color-light); }.file:disabled { cursor:default; }.file > svg { flex-shrink:0; color:var(--el-color-primary); }
.file-copy { display:flex; flex:1; min-width:0; flex-direction:column; gap:3px; }.file-copy strong { font-weight:550; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }.file-copy > span { color:var(--el-text-color-secondary); font-size:11px; }.open-arrow { color:var(--el-text-color-secondary); font-size:17px; }
progress { display:block; height:3px; width:calc(100% - 48px); margin:0 8px 6px 40px; accent-color:var(--el-color-primary); }.download-error { color:var(--el-color-danger); overflow-wrap:anywhere; font-size:11px; line-height:1.5; margin:4px 8px; }
</style>

<style>
.browser-download-popover.el-popper { max-width:calc(100vw - 24px); border-radius:14px; box-shadow:0 8px 32px rgb(0 0 0 / 16%); }
</style>
