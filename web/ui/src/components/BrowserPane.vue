<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import type { NativeBrowserState, NativeBrowserTarget } from "@/agent/nativeBrowser";
import type { ElementPick } from "@/agent/elementPicker";
import NativeBrowserPane from "./NativeBrowserPane.vue";
import BrowserBookmarks from "./BrowserBookmarks.vue";
import DesktopBrowserPane from "./DesktopBrowserPane.vue";
import { desktopBrowserBridge } from "@/agent/desktopBrowser";
import { browserErrorDetails, browserRecoveryReason, browserReopenURL } from "@/agent/browserRecovery";

const props = defineProps<{
  initialUrl?: string;
  title: string;
  sessionId?: string | null;
  active?: boolean;
  binding?: NativeBrowserTarget;
  unavailable?: boolean;
  restoring?: boolean;
  restoreError?: string;
  closing?: boolean;
}>();
const emit = defineEmits<{ pick: [ElementPick]; title: [string]; navigate: [string]; state: [NativeBrowserState]; recover: []; reopen: [string]; openFile: [string] }>();
const { t } = useI18n();
const desktop = desktopBrowserBridge(window.pogoDesktopBrowser);
const incompatibleDesktop = window.pogoDesktopBrowser !== undefined && !desktop;
const localTarget = ref<NativeBrowserTarget>();
const target = computed(() => props.closing ? localTarget.value ?? props.binding : props.binding ?? localTarget.value);
const empty = ref(false);
const nativeState = ref<NativeBrowserState | null>(null);
const desktopRequired = computed(() => nativeState.value?.display_mode === "desktop" && !desktop);
const probeError = ref("");
const bookmarksEditor = ref<InstanceType<typeof BrowserBookmarks>>();
const connecting = ref(false);
const recovering = ref(false);
const awaitingDiscovery = computed(() => props.restoring || recovering.value);
const connectionError = computed(() => props.closing ? "" : props.restoreError || probeError.value);
const retainedPane = computed(() => props.closing && !!nativeState.value && !!target.value);
const retiredPage = computed(() => props.unavailable && !awaitingDiscovery.value && !props.restoreError);
const reopenURL = computed(() => browserReopenURL(props.initialUrl));
let controller: AbortController | undefined;
let epoch = 0;
let deferredConnect = false;

function recover() {
  if (props.closing) return;
  ++epoch; controller?.abort();
  nativeState.value = null; localTarget.value = undefined;
  recovering.value = true; connecting.value = false; probeError.value = "";
  emit("recover");
}

async function connect() {
  if (props.closing) return;
  const current = ++epoch;
  controller?.abort();
  connecting.value = false;
  if (props.unavailable || awaitingDiscovery.value || props.restoreError) return;
  if (!props.sessionId || props.active === false) return;
  connecting.value = true;
  probeError.value = "";
  const pending = new AbortController();
  controller = pending;
  const timeout = setTimeout(() => pending.abort(), 30000);
  try {
    const state = await api.browserState(props.sessionId, pending.signal, props.binding);
    if (current !== epoch || props.closing) return;
    if (state.backend !== "native") throw new Error(state.error || t("nativeBrowser.unavailable"));
    emit("state", state);
    if (current !== epoch || props.closing) return;
    if (!state.viewed_tab || !state.context_ref) { empty.value = true; return; }
    if (props.binding && (state.context_ref !== props.binding.context_ref || state.viewed_tab !== props.binding.tab_id)) throw new Error(t("nativeBrowser.tabUnavailable"));
    localTarget.value = { context_ref: state.context_ref, tab_id: state.viewed_tab };
    nativeState.value = state;
    if (state.display_mode === "desktop" && incompatibleDesktop) throw new Error(t("desktopBrowser.invalidBridge"));
  } catch (err) {
    if (current === epoch && !props.closing) {
      if (props.binding && browserRecoveryReason(err)) recover();
      else probeError.value = browserErrorDetails(err);
    }
  } finally { clearTimeout(timeout); if (current === epoch) connecting.value = false; }
}
function resetConnection() {
  ++epoch; controller?.abort(); connecting.value = false;
  if (props.closing) { deferredConnect = true; return; }
  nativeState.value = null; localTarget.value = undefined; empty.value = false; probeError.value = ""; recovering.value = false; void connect();
}
watch(() => `${props.sessionId}:${props.binding?.context_ref}:${props.binding?.tab_id}:${props.unavailable}:${props.restoring}:${props.restoreError}`, resetConnection, { immediate: true, flush: "sync" });
watch(() => props.closing, closing => {
  if (closing) { ++epoch; controller?.abort(); connecting.value = false; }
  else if (deferredConnect) { deferredConnect = false; resetConnection(); }
  else if (!nativeState.value) void connect();
}, { flush: "sync" });
watch(() => props.active, active => {
  if (props.closing) return;
  if (active === false) { ++epoch; controller?.abort(); connecting.value = false; }
  else if (!nativeState.value) void connect();
});
onBeforeUnmount(() => { ++epoch; controller?.abort(); });
</script>

<template>
  <DesktopBrowserPane v-if="(retainedPane || !awaitingDiscovery && !unavailable) && desktop && nativeState?.display_mode === 'desktop' && sessionId && target" :key="`${sessionId}:${target.context_ref}:${target.tab_id}`" :bridge="desktop" :target="target" :session-id="sessionId" :active="active !== false" :initial-state="nativeState" :initial-url="initialUrl" @title="emit('title', $event)" @navigate="emit('navigate', $event)" @pick="emit('pick', $event)" @state="emit('state', $event)" @recover="recover" />
  <NativeBrowserPane v-else-if="(retainedPane || !awaitingDiscovery && !unavailable) && !desktopRequired && nativeState && sessionId && target" :key="`${sessionId}:${target.context_ref}:${target.tab_id}`" :target="target" :session-id="sessionId" :active="active !== false" :closing="closing" :initial-state="nativeState" :initial-url="initialUrl" @title="emit('title', $event)" @navigate="emit('navigate', $event)" @pick="emit('pick', $event)" @state="emit('state', $event)" @open-file="emit('openFile', $event)" @recover="recover" />
  <section v-else class="browser-connect" :inert="closing" :data-testid="connectionError ? 'browser-recovery-error' : awaitingDiscovery ? 'browser-restoring' : retiredPage ? 'browser-retired-page' : 'browser-connect'" :aria-label="t('nativeBrowser.title')">
    <div class="connect-toolbar" role="toolbar" :aria-label="t('nativeBrowser.navigation')">
      <div class="connect-location">
        <span class="connect-indicator" :class="{ loading: !connectionError && !retiredPage && (awaitingDiscovery || connecting), failed: !!connectionError && !retiredPage }" role="img" :aria-label="connectionError ? t('nativeBrowser.offline') : awaitingDiscovery ? t('nativeBrowser.restoring') : connecting ? t('nativeBrowser.connecting') : t('nativeBrowser.ready')" />
        <input :value="initialUrl || ''" :placeholder="t('nativeBrowser.url')" :aria-label="t('nativeBrowser.url')" readonly />
      </div>
      <button @click="bookmarksEditor?.add()">{{ t('nativeBrowser.addFavorite') }}</button>
      <button v-if="connectionError && !retiredPage" data-testid="browser-recover" @click="recover">{{ t('nativeBrowser.reconnect') }}</button>
    </div>
    <div v-if="retiredPage" class="retired-page" role="status">
      <strong>{{ t('nativeBrowser.retiredTitle') }}</strong>
      <p>{{ t('nativeBrowser.retiredHint') }}</p>
      <button v-if="reopenURL" data-testid="browser-reopen" @click="emit('reopen', reopenURL)">{{ t('nativeBrowser.reopen') }}</button>
    </div>
    <p v-else-if="connectionError || !sessionId || empty || desktopRequired" class="connect-status" :class="{ failed: !!connectionError }" :role="connectionError ? 'alert' : 'status'">{{ connectionError ? t('nativeBrowser.connectionFailed') : desktopRequired ? t('desktopBrowser.openInDesktop') : t(empty ? 'nativeBrowser.noTabs' : 'nativeBrowser.noSession') }}</p>
    <details v-if="connectionError && !retiredPage" class="connection-details"><summary>{{ t('nativeBrowser.connectionDetails') }}</summary>{{ connectionError }}</details>
    <BrowserBookmarks ref="bookmarksEditor" />
    <div class="connect-page" />
  </section>
</template>

<style scoped>
.browser-connect { display:flex; flex:1; min-width:0; min-height:0; flex-direction:column; gap:8px; padding:8px; background:var(--el-bg-color); color:var(--el-text-color-secondary); }
.connect-toolbar { display:flex; align-items:center; gap:6px; min-height:34px; }
.connect-location { display:flex; align-items:center; gap:7px; flex:1; min-width:0; height:34px; padding:0 9px; border:1px solid var(--el-border-color-lighter); border-radius:7px; background:var(--el-fill-color-light); box-sizing:border-box; }
.connect-location input { width:0; flex:1; min-width:0; padding:6px 0; border:0; background:transparent; font:inherit; font-size:12px; color:var(--el-text-color-regular); outline:none; }
.connect-indicator { width:6px; height:6px; flex-shrink:0; border-radius:50%; background:var(--el-text-color-placeholder); }
.connect-indicator.loading { width:12px; height:12px; box-sizing:border-box; border:1.5px solid var(--el-border-color); border-top-color:var(--el-color-primary); background:transparent; animation:browser-spin .8s linear infinite; }
.connect-indicator.failed { background:var(--el-color-danger); }
.connect-page { flex:1; min-height:220px; background:var(--el-bg-color); border-radius:7px; }
.connect-status { margin:0; font-size:12px; line-height:1.5; overflow-wrap:anywhere; }.connect-status.failed { color:var(--el-color-danger); }
.retired-page { margin:24px 8px 8px; padding:20px; border:1px solid var(--el-border-color-light); border-radius:10px; color:var(--el-text-color-primary); }
.retired-page p { color:var(--el-text-color-secondary); font-size:13px; line-height:1.6; }
.connection-details { font-size:12px; overflow-wrap:anywhere; color:var(--el-text-color-secondary); }
button { flex-shrink:0; font:inherit; font-size:12px; padding:6px 9px; border:1px solid var(--el-border-color); border-radius:6px; background:var(--el-bg-color); color:var(--el-text-color-primary); cursor:pointer; }
@keyframes browser-spin { to { transform:rotate(360deg); } }
@media (max-width:600px) { .browser-connect { gap:6px; padding:6px; } }
@media (prefers-reduced-motion:reduce) { .connect-indicator.loading { animation:none; border-right-color:var(--el-color-primary); } }
</style>
