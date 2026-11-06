<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";

const props = withDefaults(defineProps<{ path: string; sessionId: string; initialUrl?: string; initialId?: string; active: boolean; showToolbar?: boolean; toolbarTarget?: string }>(), { showToolbar: true });
const emit = defineEmits<{ claimCapability: [id: string]; openBrowser: [request: { url: string; id: string; sessionId: string }] }>();
const { t } = useI18n();
const current = ref<{ id: string; url: string } | null>(null);
const loading = ref(false), failed = ref(""), updated = ref(false);
const browserOpening = ref(false);
const label = computed(() => props.path.split("/").pop() || props.path);
let version = 0, browserVersion = 0, disposed = false, loaded = false;
let updateTimer: ReturnType<typeof setTimeout> | undefined;
const retired = new Map<string, string>();
let currentOwner = "";
function revoke(id: unknown, owner: string) {
  if (typeof id === "string" && id) void api.revokeHTMLPreview(owner, id).catch(() => {});
}
function validURL(value: unknown): value is string {
  return typeof value === "string" && /^\/api\/html-previews\/[a-f0-9]{64}\//.test(value) && !/[\r\n]/.test(value);
}
function settle() {
  loading.value = false;
  for (const [id, owner] of retired) revoke(id, owner);
  retired.clear();
  if (loaded) {
    updated.value = true;
    clearTimeout(updateTimer);
    updateTimer = setTimeout(() => { updated.value = false; }, 1800);
  }
  loaded = true;
}
async function reload() {
  if (loading.value || !props.sessionId) return;
  const requestVersion = ++version, path = props.path, owner = props.sessionId;
  const isCurrent = () => !disposed && requestVersion === version && path === props.path && owner === props.sessionId;
  loading.value = true; failed.value = "";
  try {
    const next = await api.createHTMLPreview(owner, path);
    if (!isCurrent()) {
      revoke(next?.id, owner); return;
    }
    if (!validURL(next?.url) || typeof next?.id !== "string" || !next.id) {
      revoke(next?.id, owner);
      failed.value = t("filePreview.htmlInvalidPreview");
      loading.value = false;
      return;
    }
    // Keep the old page and its capability until the replacement has loaded.
    if (current.value) retired.set(current.value.id, currentOwner);
    currentOwner = owner;
    current.value = { id: next.id, url: next.url };
  } catch (error) {
    if (isCurrent()) {
      failed.value = error instanceof Error ? error.message : String(error);
      loading.value = false;
    }
  }
}
async function openBrowser() {
  if (browserOpening.value || !props.sessionId) return;
  const requestVersion = ++browserVersion, owner = props.sessionId, path = props.path;
  const isCurrent = () => !disposed && requestVersion === browserVersion && owner === props.sessionId && path === props.path;
  browserOpening.value = true; failed.value = "";
  try {
    // The native tab gets its own capability. Closing or refreshing this
    // iframe must not revoke a page opened in the built-in browser.
    const next = await api.createHTMLPreview(owner, path);
    if (!isCurrent()) { revoke(next?.id, owner); return; }
    let validBrowserURL = false;
    try {
      const url = new URL(next.browser_url);
      validBrowserURL = ["http:", "https:"].includes(url.protocol) && validURL(url.pathname) && !url.username && !url.password && !url.search && !url.hash && !/[\r\n]/.test(next.browser_url);
    } catch { /* Invalid receipts are revoked rather than navigated. */ }
    if (!validBrowserURL || !/^[a-f0-9]{32}$/.test(next?.id)) {
      revoke(next?.id, owner); failed.value = t("filePreview.htmlInvalidPreview"); return;
    }
    emit("openBrowser", { url: next.browser_url, id: next.id, sessionId: owner });
  } catch (error) {
    if (isCurrent()) failed.value = error instanceof Error ? error.message : String(error);
  } finally { if (isCurrent()) browserOpening.value = false; }
}
watch(() => [props.path, props.sessionId] as const, () => {
  version++; browserVersion++; browserOpening.value = false;
  if (current.value) revoke(current.value.id, currentOwner);
  for (const [id, owner] of retired) revoke(id, owner);
  retired.clear(); current.value = null; loading.value = false; failed.value = ""; loaded = false;
  currentOwner = props.sessionId;
  if (props.initialId && /^[a-f0-9]{32}$/.test(props.initialId) && props.initialUrl && validURL(props.initialUrl)) {
    current.value = { id: props.initialId, url: props.initialUrl };
    loading.value = true;
  } else void reload();
}, { immediate: true });
onMounted(() => {
  // The mounted pane now owns revocation. Consume the one-shot initial URL
  // so returning after a conversation switch obtains a fresh capability.
  if (current.value && current.value.id === props.initialId) emit("claimCapability", current.value.id);
});
onBeforeUnmount(() => {
  disposed = true; version++; browserVersion++; clearTimeout(updateTimer);
  if (current.value) revoke(current.value.id, currentOwner);
  for (const [id, owner] of retired) revoke(id, owner);
});
defineExpose({ reload });
</script>

<template>
  <section class="html-browser-pane">
    <Teleport defer :to="toolbarTarget || 'body'" :disabled="!toolbarTarget">
      <header v-if="showToolbar !== false && (!toolbarTarget || active)" class="html-browser-bar" :class="{ 'in-tabstrip': toolbarTarget }">
        <span v-if="!toolbarTarget" class="location" :title="path">{{ label }}</span>
        <span v-if="updated" class="updated" role="status">{{ t('filePreview.htmlUpdated') }}</span>
        <button type="button" :aria-label="t('filePreview.htmlReload')" :title="t('filePreview.htmlReload')" :disabled="loading" @click="reload"><el-icon :class="{ rotating: loading }"><Refresh /></el-icon></button>
        <button type="button" class="native-open" data-testid="html-preview-open-browser" :aria-label="t('filePreview.htmlOpenBrowser')" :title="t('filePreview.htmlOpenBrowser')" :disabled="browserOpening || !sessionId" @click="openBrowser">
          <svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><rect x="2" y="3" width="16" height="12" rx="2" stroke="currentColor" stroke-width="1.4"/><path d="M7 18h6m-3-3v3M2 7h16" stroke="currentColor" stroke-width="1.4"/></svg>
          <span class="control-label">{{ t(browserOpening ? 'filePreview.htmlOpeningBrowser' : 'filePreview.htmlOpenBrowser') }}</span>
        </button>
      </header>
    </Teleport>
    <p v-if="failed" class="error" role="alert">{{ t('filePreview.renderFailed') }}{{ failed }}</p>
    <p v-if="!current && loading" class="waiting">{{ t('filePreview.rendering') }}</p>
    <iframe v-if="current" class="html-browser-frame" :src="current.url" :title="label" sandbox="allow-scripts" referrerpolicy="no-referrer" @load="settle" />
  </section>
</template>

<style scoped>
.html-browser-pane{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--el-bg-color)}
.html-browser-bar{display:flex;align-items:center;gap:6px;padding:6px 10px;border-bottom:1px solid var(--el-border-color-light);flex:0 0 auto;min-width:0;background:var(--el-bg-color-page)}
.html-browser-bar.in-tabstrip{padding:0 2px;border:0;background:transparent;min-height:34px}
.html-browser-bar button{display:inline-flex;align-items:center;justify-content:center;gap:6px;flex-shrink:0;min-width:28px;height:28px;border:0;border-radius:5px;background:transparent;color:var(--el-text-color-regular);cursor:pointer;padding:4px;font-size:16px}.html-browser-bar button:hover{background:var(--el-fill-color-light)}.html-browser-bar button:disabled{cursor:default;opacity:.6}.html-browser-bar .native-open{font-size:11px;padding:4px 7px;color:var(--el-color-primary)}.control-label{white-space:nowrap}
.location{min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px;color:var(--el-text-color-regular)}
.updated{color:var(--el-color-success);font-size:12px}.error{margin:0;padding:10px;color:var(--el-color-danger);font-size:13px}.waiting{padding:12px;color:var(--el-text-color-secondary)}
.html-browser-frame{display:block;border:0;width:100%;flex:1;min-height:0;background:white}.rotating{animation:rotate 1s linear infinite}@keyframes rotate{to{transform:rotate(360deg)}}
@container preview-toolbar (max-width:700px){.in-tabstrip .control-label,.in-tabstrip .updated{display:none}.html-browser-bar.in-tabstrip{gap:2px}.html-browser-bar.in-tabstrip .native-open{padding:4px}}
</style>
