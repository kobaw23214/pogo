<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import { newBookmark, parseBookmarkImport, type BrowserBookmark } from "@/agent/browserBookmarks";
import { accountBookmarksAPI } from "@/api/browserBookmarks";
import { useAccountBrowserBookmarks } from "@/agent/accountBrowserBookmarks";

const props = defineProps<{ currentUrl?: string; currentTitle?: string }>();
const { t } = useI18n();
const store = useAccountBrowserBookmarks();
const cards = computed(() => store.state.bookmarks);
const message = ref("");
const editing = ref<string | null>(null);
const original = ref<BrowserBookmark>();
const editingAccount = ref("");
const title = ref("");
const url = ref("");
const formOpen = ref(false);
const importInput = ref<HTMLInputElement | null>(null);
const saving = computed(() => store.state.saving);
function ready() {
  if (store.state.loaded && store.state.accountId && !store.state.saving && !store.state.loading) return true;
  ElMessage.error(store.state.error || t("nativeBrowser.bookmarkAccountLoading"));
  return false;
}
function edit(card?: BrowserBookmark) {
  if (!ready()) return;
  editing.value = card?.id ?? null;
  original.value = card && cards.value.some(saved => saved.id === card.id) ? { ...card } : undefined;
  editingAccount.value = store.state.accountId;
  title.value = card?.title ?? props.currentTitle ?? "";
  url.value = card?.url ?? (props.currentUrl === "about:blank" ? "" : props.currentUrl) ?? "";
  formOpen.value = true;
  message.value = "";
}
async function submit() {
  if (saving.value) return;
  if (editingAccount.value !== store.state.accountId) { message.value = t("nativeBrowser.bookmarkAccountChanged"); return; }
  const card = newBookmark(title.value, url.value, window.location.origin);
  if (!card) { message.value = t("nativeBrowser.bookmarkInvalid"); return; }
  if (editing.value) card.id = editing.value;
  if (await store.upsert(card, original.value)) { formOpen.value = false; message.value = ""; }
  else message.value = t("nativeBrowser.bookmarkSaveFailed", { message: store.state.error || t("nativeBrowser.bookmarkAccountRetry") });
}
async function importFile(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file || !ready()) return;
  if (file.size > 1024 * 1024) { ElMessage.error(t("nativeBrowser.bookmarkImportInvalid")); return; }
  const accountId = store.state.accountId;
  try {
    const parsed = parseBookmarkImport(await file.text(), window.location.origin);
    if (accountId !== store.state.accountId) { ElMessage.error(t("nativeBrowser.bookmarkAccountChanged")); return; }
    if (!parsed) { ElMessage.error(t("nativeBrowser.bookmarkImportInvalid")); return; }
    const result = await store.importBookmarks(parsed.bookmarks);
    if (result.success) ElMessage.success(t("nativeBrowser.bookmarkImported", { count: result.added, invalid: parsed.invalid }));
    else ElMessage.error(t("nativeBrowser.bookmarkSaveFailed", { message: store.state.error }));
  } catch (err) { ElMessage.error(String(err)); }
}
function exportFile() {
  if (!ready()) return;
  // Also preserve any claimed local entries still awaiting URL repair.
  const entries = [...cards.value, ...(store.state.legacyNeedsClaim ? [] : store.state.localBookmarks)];
  const data = JSON.stringify({ shortcuts: entries.map(({ title, url, needsRepair }) => ({ title, url, ...(needsRepair ? { needsRepair: true } : {}) })) }, null, 2);
  const link = document.createElement("a"), objectURL = URL.createObjectURL(new Blob([data], { type: "application/json" }));
  link.href = objectURL; link.download = "browser-bookmarks.json"; link.click();
  setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
}
function repair(card: BrowserBookmark) { edit(card); message.value = t("nativeBrowser.bookmarkInvalid"); }
async function remove(card: BrowserBookmark) {
  if (!ready()) return;
  const local = store.state.localBookmarks.some(item => item.id === card.id) && !cards.value.some(item => item.id === card.id);
  const success = local ? store.removeLocal(card) : await store.remove(card);
  if (!success) ElMessage.error(t("nativeBrowser.bookmarkSaveFailed", { message: store.state.error }));
}
async function syncLocalBookmarks() {
  if (!ready()) return;
  const result = await store.importLocal();
  if (!result.success) { ElMessage.error(t("nativeBrowser.bookmarkSaveFailed", { message: store.state.error })); return; }
  if (result.invalid) ElMessage.warning(t("nativeBrowser.bookmarkLocalSyncedRepair", { count: result.added, invalid: result.invalid }));
  else ElMessage.success(t("nativeBrowser.bookmarkLocalSynced", { count: result.added }));
}
let closed = false;
const importingAccount = ref(false);
onBeforeUnmount(() => { closed = true; });
async function importAccountBookmarks() {
  if (!store.agentId || !ready() || importingAccount.value) return;
  const target = store.state.accountId, owner = store.state.ownerAccountId;
  importingAccount.value = true;
  try {
    const source = await accountBookmarksAPI.read();
    if (closed || target !== store.state.accountId || source.account_id !== owner) throw new Error(t("nativeBrowser.bookmarkAccountChanged"));
    if (!source.bookmarks.length) { ElMessage.info(t("nativeBrowser.bookmarkSharedEmpty")); return; }
    await ElMessageBox.confirm(t("nativeBrowser.bookmarkImportSharedConfirm", { n: source.bookmarks.length }), t("nativeBrowser.bookmarkImportShared"), { confirmButtonText: t("nativeBrowser.bookmarkImportShared"), cancelButtonText: t("nativeBrowser.bookmarkCancel") });
    if (closed || target !== store.state.accountId || owner !== store.state.ownerAccountId) return;
    const result = await store.importBookmarks(source.bookmarks);
    if (result.success) ElMessage.success(t("nativeBrowser.bookmarkImported", { count: result.added, invalid: 0 }));
    else ElMessage.error(store.state.error);
  } catch (error) { if (!closed && error !== "cancel" && error !== "close") ElMessage.error(error instanceof Error ? error.message : String(error)); }
  finally { importingAccount.value = false; }
}
watch(() => store.state.accountId, next => {
  if (formOpen.value && next !== editingAccount.value) message.value = t("nativeBrowser.bookmarkAccountChanged");
});
defineExpose({ importAccountBookmarks, add: () => edit(), edit, repair, remove, syncLocalBookmarks, importBookmarks: () => { if (ready()) importInput.value?.click(); }, exportBookmarks: exportFile });
</script>

<template>
  <input ref="importInput" type="file" accept="application/json,.json" hidden @change="importFile" />
  <ElDialog v-model="formOpen" class="browser-bookmarks" :title="t(editing ? 'nativeBrowser.bookmarkEditTitle' : 'nativeBrowser.addFavorite')" width="min(440px, calc(100vw - 32px))" append-to-body>
    <form class="bookmark-form" @submit.prevent="submit">
      <p v-if="message" class="bookmark-notice" role="status">{{ message }}</p>
      <label>{{ t('nativeBrowser.bookmarkName') }}
      <input v-model="title" :disabled="saving" :aria-label="t('nativeBrowser.bookmarkName')" :placeholder="t('nativeBrowser.bookmarkName')" maxlength="120" required />
      </label>
      <label>{{ t('nativeBrowser.url') }}
      <input v-model="url" :disabled="saving" :aria-label="t('nativeBrowser.url')" :placeholder="t('nativeBrowser.url')" spellcheck="false" required />
      </label>
      <div class="bookmark-form-actions">
        <ElButton :disabled="saving" @click="formOpen = false">{{ t('nativeBrowser.bookmarkCancel') }}</ElButton>
        <ElButton type="primary" native-type="submit" :loading="saving" :disabled="!store.state.loaded || editingAccount !== store.state.accountId">{{ t('nativeBrowser.bookmarkSave') }}</ElButton>
      </div>
    </form>
  </ElDialog>
</template>

<style scoped>
.bookmark-form { display:flex; flex-direction:column; gap:16px; }
.bookmark-form label { display:flex; flex-direction:column; gap:7px; color:var(--el-text-color-regular); font-size:13px; }
.bookmark-form input { box-sizing:border-box; width:100%; padding:9px 11px; border:1px solid var(--el-border-color); border-radius:7px; background:var(--el-bg-color); font:inherit; color:var(--el-text-color-primary); }
.bookmark-form-actions { display:flex; justify-content:flex-end; margin-top:4px; }
.bookmark-notice { margin:0; color:var(--el-color-warning); font-size:12px; }
input:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
</style>
