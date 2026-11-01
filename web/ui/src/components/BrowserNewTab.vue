<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { BrowserBookmark } from "@/agent/browserBookmarks";
import type { AccountBookmarkStatus } from "@/agent/accountBrowserBookmarks";
import BrowserBookmarkTransfer from "./BrowserBookmarkTransfer.vue";
const props = defineProps<{ bookmarks: readonly BrowserBookmark[]; agentScoped?: boolean; busy?: boolean; bookmarkStatus?: AccountBookmarkStatus }>();
const emit = defineEmits<{ select: [BrowserBookmark]; add: []; edit: [BrowserBookmark]; remove: [BrowserBookmark]; import: []; export: []; retry: []; sync: []; importAccount: [] }>();
const { t } = useI18n();
const localBookmarks = computed(() => props.bookmarkStatus?.localBookmarks ?? []);
const visibleLocalBookmarks = computed(() => props.bookmarkStatus?.legacyNeedsClaim ? [] : localBookmarks.value);
const displayedBookmarks = computed(() => [...props.bookmarks, ...visibleLocalBookmarks.value]);
const editingDisabled = computed(() => !!props.bookmarkStatus && (!props.bookmarkStatus.loaded || props.bookmarkStatus.loading || props.bookmarkStatus.saving));
function host(url: string) { try { return new URL(url).host; } catch { return url; } }
</script>

<template>
  <section class="browser-new-tab" data-testid="browser-new-tab" :aria-label="t('nativeBrowser.newTabPageTitle')" @click.stop @keydown.stop @wheel.stop>
    <div class="new-tab-content">
      <div class="new-tab-mark" aria-hidden="true"><svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="12" cy="12" r="9" /><path d="m16 8-2.5 5.5L8 16l2.5-5.5L16 8Z" /></svg></div>
      <h2>{{ t('nativeBrowser.newTabPageTitle') }}</h2>
      <p v-if="!bookmarkStatus || bookmarkStatus.loaded" class="new-tab-hint">{{ t(displayedBookmarks.length ? 'nativeBrowser.newTabHint' : 'nativeBrowser.newTabEmpty') }}</p>
      <p v-else-if="bookmarkStatus.loading" class="new-tab-hint" role="status">{{ t('nativeBrowser.bookmarkAccountLoading') }}</p>
      <div v-if="bookmarkStatus?.error" class="bookmark-sync-notice is-error" role="status">
        <span>{{ t('nativeBrowser.bookmarkAccountFailed', { message: bookmarkStatus.error }) }}</span>
        <button type="button" :disabled="bookmarkStatus.loading || bookmarkStatus.saving" @click="emit('retry')">{{ t('nativeBrowser.bookmarkAccountRetry') }}</button>
      </div>
      <p v-if="bookmarkStatus?.localError" class="bookmark-sync-notice is-error" role="status">{{ bookmarkStatus.localError }}</p>
      <div v-if="localBookmarks.length" class="bookmark-sync-notice" data-testid="browser-bookmarks-local-import">
        <span>{{ t('nativeBrowser.bookmarkLocalPending', { count: localBookmarks.length }) }}</span>
        <button type="button" :disabled="editingDisabled" @click="emit('sync')">{{ t('nativeBrowser.bookmarkSyncLocal') }}</button>
      </div>
      <div class="new-tab-bookmarks">
        <div v-for="bookmark in displayedBookmarks" :key="bookmark.id" class="new-tab-card" data-testid="browser-bookmark-card" :data-bookmark-id="bookmark.id">
          <button class="card-link" type="button" :disabled="busy" :title="bookmark.url" @click="emit('select', bookmark)">
          <span class="bookmark-monogram" aria-hidden="true">{{ bookmark.title.slice(0, 1).toUpperCase() }}</span>
          <span class="card-copy"><strong>{{ bookmark.title }}</strong><small>{{ bookmark.needsRepair ? t('nativeBrowser.bookmarkRepair') : host(bookmark.url) }}</small><small v-if="localBookmarks.some(item => item.id === bookmark.id)" class="local-bookmark-label">{{ t('nativeBrowser.bookmarkLocalOnly') }}</small></span>
          </button>
          <button class="card-action card-edit" type="button" :disabled="editingDisabled" :title="t('nativeBrowser.bookmarkEdit')" :aria-label="`${t('nativeBrowser.bookmarkEdit')} ${bookmark.title}`" @pointerdown.stop @click.stop="emit('edit', bookmark)">
            <svg viewBox="0 0 20 20" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m12.5 3.5 4 4M3 17l4.5-1L17 6.5a2.8 2.8 0 0 0-4-4L3.5 12 3 17Z" /></svg>
          </button>
          <button class="card-action card-delete" type="button" :disabled="editingDisabled" :title="t('nativeBrowser.bookmarkDelete')" :aria-label="`${t('nativeBrowser.bookmarkDelete')} ${bookmark.title}`" @pointerdown.stop @click.stop="emit('remove', bookmark)">
            <svg viewBox="0 0 20 20" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><path d="m5 5 10 10M15 5 5 15" /></svg>
          </button>
        </div>
        <button class="new-tab-card add-card" :disabled="editingDisabled" @click="emit('add')"><span class="bookmark-monogram" aria-hidden="true">+</span><strong>{{ t('nativeBrowser.addFavorite') }}</strong></button>
      </div>
      <p v-if="agentScoped" class="agent-bookmark-scope">{{ t('nativeBrowser.bookmarkAgentScope') }} <button type="button" :disabled="editingDisabled" @click="emit('importAccount')">{{ t('nativeBrowser.bookmarkImportShared') }}</button></p>
      <BrowserBookmarkTransfer class="favorite-transfer" :can-export="!!displayedBookmarks.length && !editingDisabled" @import="emit('import')" @export="emit('export')" />
    </div>
  </section>
</template>

<style scoped>
.agent-bookmark-scope { margin:20px 0 0; color:var(--el-text-color-secondary); font-size:12px; text-align:center; }
.agent-bookmark-scope button { border:0; background:none; color:var(--el-color-primary); font:inherit; cursor:pointer; padding:4px 6px; }
.browser-new-tab { width:100%; min-height:100%; display:flex; align-items:flex-start; justify-content:center; box-sizing:border-box; padding:clamp(24px, 6vh, 64px) 24px; background:var(--el-bg-color); color:var(--el-text-color-primary); }
.new-tab-content { width:100%; max-width:650px; text-align:center; }
.new-tab-mark { display:inline-flex; align-items:center; justify-content:center; width:54px; height:54px; border-radius:16px; color:var(--el-color-primary); background:var(--el-color-primary-light-9); }
h2 { margin:16px 0 8px; font-size:23px; line-height:1.4; font-weight:600; }
.new-tab-hint { margin:0 auto 24px; font-size:13px; line-height:1.65; color:var(--el-text-color-secondary); }
.new-tab-bookmarks { display:grid; grid-template-columns:repeat(auto-fit, minmax(min(100%, 165px), 1fr)); gap:12px; text-align:left; }
button { font:inherit; cursor:pointer; color:inherit; }
.new-tab-card { position:relative; display:flex; min-width:0; min-height:94px; border:1px solid var(--el-border-color-lighter); border-radius:11px; background:var(--el-fill-color-extra-light); text-align:left; }
.new-tab-card:hover { border-color:var(--el-color-primary-light-5); background:var(--el-color-primary-light-9); }
.card-link { display:flex; align-items:center; gap:11px; flex:1; min-width:0; width:100%; padding:30px 14px 14px; border:0; border-radius:inherit; background:transparent; text-align:left; }
.card-link:disabled { opacity:.65; cursor:default; }
.card-action { position:absolute; top:6px; display:inline-flex; align-items:center; justify-content:center; width:25px; height:25px; padding:0; border:0; border-radius:6px; background:var(--el-bg-color); color:var(--el-text-color-secondary); opacity:0; pointer-events:none; transition:opacity .12s,background .12s,color .12s; }
.card-edit { left:6px; }.card-delete { right:6px; }
.new-tab-card:hover .card-action,.new-tab-card:focus-within .card-action { opacity:1; pointer-events:auto; }
.card-edit:hover { color:var(--el-color-primary); background:var(--el-color-primary-light-9); }
.card-delete:hover { color:var(--el-color-danger); background:var(--el-color-danger-light-9); }
.bookmark-monogram { display:flex; align-items:center; justify-content:center; flex:0 0 34px; height:34px; background:var(--el-bg-color); border:1px solid var(--el-border-color-lighter); border-radius:9px; color:var(--el-color-primary); font-size:17px; font-weight:500; }
.card-copy { display:flex; flex-direction:column; gap:5px; min-width:0; }
strong { font-size:13px; font-weight:500; } .card-copy strong,.card-copy small { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
small { color:var(--el-text-color-secondary); font-size:11px; }.add-card { align-items:center; gap:11px; padding:14px; border-style:dashed; color:var(--el-text-color-secondary); }.add-card .bookmark-monogram { font-size:22px; border:0; background:transparent; }
.favorite-transfer { margin-top:20px; }
.bookmark-sync-notice { display:flex; align-items:center; justify-content:space-between; flex-wrap:wrap; gap:9px; margin:0 0 16px; padding:11px 13px; border:1px solid var(--el-border-color-lighter); border-radius:8px; text-align:left; color:var(--el-text-color-secondary); font-size:12px; line-height:1.6; }
.bookmark-sync-notice button { flex-shrink:0; padding:5px 9px; border:1px solid var(--el-border-color); border-radius:6px; background:var(--el-bg-color); color:var(--el-color-primary); font-size:12px; }
.bookmark-sync-notice.is-error { color:var(--el-color-warning); }.local-bookmark-label { color:var(--el-color-warning); }
button:disabled { cursor:default; opacity:.6; }
button:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
@media (hover:none) { .card-action { opacity:1; pointer-events:auto; } }
@media (max-width:600px) { .browser-new-tab { padding:28px 16px; }.new-tab-bookmarks { grid-template-columns:repeat(auto-fit, minmax(min(100%, 140px), 1fr)); gap:9px; }.card-link { padding:30px 12px 12px; gap:8px; }.add-card { padding:12px; gap:8px; } }
</style>
