<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";

const props = withDefaults(defineProps<{ path: string; sessionId: string; initialUrl?: string; initialId?: string; active: boolean; showToolbar?: boolean }>(), { showToolbar: true });
const emit = defineEmits<{ claimCapability: [id: string] }>();
const { t } = useI18n();
const current = ref<{ id: string; url: string } | null>(null);
const loading = ref(false), failed = ref(""), updated = ref(false);
const label = computed(() => props.path.split("/").pop() || props.path);
let version = 0, disposed = false, loaded = false;
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
watch(() => [props.path, props.sessionId] as const, () => {
  version++;
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
  disposed = true; version++; clearTimeout(updateTimer);
  if (current.value) revoke(current.value.id, currentOwner);
  for (const [id, owner] of retired) revoke(id, owner);
});
defineExpose({ reload });
</script>

<template>
  <section class="html-browser-pane">
    <header v-if="showToolbar !== false" class="html-browser-bar">
      <button type="button" :aria-label="t('filePreview.htmlReload')" :title="t('filePreview.htmlReload')" :disabled="loading" @click="reload"><el-icon :class="{ rotating: loading }"><Refresh /></el-icon></button>
      <span class="location" :title="path">{{ label }}</span>
      <span v-if="updated" class="updated" role="status">{{ t('filePreview.htmlUpdated') }}</span>
    </header>
    <p v-if="failed" class="error" role="alert">{{ t('filePreview.renderFailed') }}{{ failed }}</p>
    <p v-if="!current && loading" class="waiting">{{ t('filePreview.rendering') }}</p>
    <iframe v-if="current" class="html-browser-frame" :src="current.url" :title="label" sandbox="allow-scripts" referrerpolicy="no-referrer" @load="settle" />
  </section>
</template>

<style scoped>
.html-browser-pane{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--el-bg-color)}
.html-browser-bar{display:flex;align-items:center;gap:10px;padding:8px 12px;border-bottom:1px solid var(--el-border-color-light);flex:0 0 auto}
.html-browser-bar button{display:flex;border:0;background:transparent;color:var(--el-text-color-regular);cursor:pointer;padding:5px;font-size:16px}.html-browser-bar button:disabled{cursor:default;opacity:.6}
.location{min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px;color:var(--el-text-color-regular)}
.updated{color:var(--el-color-success);font-size:12px}.error{margin:0;padding:10px;color:var(--el-color-danger);font-size:13px}.waiting{padding:12px;color:var(--el-text-color-secondary)}
.html-browser-frame{display:block;border:0;width:100%;flex:1;min-height:0;background:white}.rotating{animation:rotate 1s linear infinite}@keyframes rotate{to{transform:rotate(360deg)}}
</style>
