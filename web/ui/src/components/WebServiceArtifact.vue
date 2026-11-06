<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Monitor } from "@element-plus/icons-vue";
import WebServicePane from "./WebServicePane.vue";
import { parseWebArtifact, validWebArtifactScope, type WebArtifactScope } from "@/agent/webArtifact";
import { webArtifactNavigationKey } from "@/agent/webArtifactNavigation";
import type { WebsiteBrowserRequest } from "@/agent/browserPreviewNavigation";

const props = defineProps<{ source: string; scope: WebArtifactScope | null }>();
const navigation = inject(webArtifactNavigationKey, null);
const { t } = useI18n();
const host = ref<HTMLElement | null>(null);
const activated = ref(false);
const actionIssue = ref("");
let observer: IntersectionObserver | undefined;
let actionEpoch = 0;
let disposed = false;
const spec = computed(() => parseWebArtifact(props.source));
const available = computed(() => !!spec.value && validWebArtifactScope(props.scope));
const identity = computed(() => available.value ? `${props.scope!.sessionId}:${spec.value!.service_id}` : "");

function activate() {
  if (disposed || !available.value) return;
  activated.value = true;
  observer?.disconnect(); observer = undefined;
}
function current(sessionId: string, serviceId: string) {
  return !disposed && activated.value && available.value && props.scope!.sessionId === sessionId && spec.value!.service_id === serviceId && navigation?.scope()?.sessionId === sessionId;
}
async function openDock(name?: string) {
  if (!props.scope || !spec.value || !current(props.scope.sessionId, spec.value.service_id)) return;
  const epoch = actionEpoch;
  actionIssue.value = "";
  try {
    await navigation!.openDock({ sessionId: props.scope.sessionId, serviceId: spec.value.service_id, label: name || spec.value.title || t("webArtifact.title"), open: true });
  } catch (cause) {
    if (epoch === actionEpoch) actionIssue.value = cause instanceof Error ? cause.message : String(cause);
  }
}
async function openBrowser(request: WebsiteBrowserRequest) {
  if (!current(request.sessionId, request.serviceId)) return;
  const epoch = actionEpoch;
  actionIssue.value = "";
  try { await navigation!.openBrowser(request); }
  catch (cause) { if (epoch === actionEpoch) actionIssue.value = cause instanceof Error ? cause.message : String(cause); }
}
watch(identity, () => { actionEpoch++; actionIssue.value = ""; });
onMounted(() => {
  if (typeof IntersectionObserver === "undefined" || !host.value) return;
  observer = new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting)) activate(); }, { rootMargin: "240px" });
  observer.observe(host.value);
});
onBeforeUnmount(() => { disposed = true; actionEpoch++; observer?.disconnect(); });
</script>

<template>
  <section ref="host" class="web-artifact" :class="{ unavailable: !available }" data-testid="web-artifact">
    <WebServicePane
      v-if="available && activated"
      :key="identity"
      :session-id="scope!.sessionId"
      :service-id="spec!.service_id"
      active
      :open-request="1"
      @open-browser="openBrowser"
    >
      <template #actions="{ record }">
        <button type="button" class="web-artifact-dock" :disabled="!navigation" :title="t('htmlArtifact.openDock')" :aria-label="t('htmlArtifact.openDock')" @click="openDock(record?.name)">
          <el-icon><Monitor /></el-icon><span>{{ t("htmlArtifact.openDock") }}</span>
        </button>
      </template>
    </WebServicePane>
    <template v-else>
      <header class="web-artifact-bar"><strong>{{ spec?.title || t("webArtifact.title") }}</strong><span>{{ t("webArtifact.badge") }}</span></header>
      <p v-if="!available" class="web-artifact-message" role="status">{{ t(spec ? "webArtifact.unavailable" : "webArtifact.invalid") }}</p>
      <div v-else class="web-artifact-placeholder"><button type="button" @click="activate">{{ t("htmlArtifact.load") }}</button></div>
    </template>
    <p v-if="actionIssue" class="web-artifact-message" role="alert">{{ actionIssue }}</p>
  </section>
</template>

<style scoped>
.web-artifact{display:flex;flex-direction:column;height:640px;overflow:hidden;border:1px solid var(--el-border-color-lighter);border-radius:12px;background:var(--el-bg-color)}
.web-artifact.unavailable{height:auto}.web-artifact-bar{display:flex;align-items:center;justify-content:space-between;gap:12px;min-height:46px;padding:7px 13px;box-sizing:border-box;border-bottom:1px solid var(--el-border-color-extra-light)}
.web-artifact-bar strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.web-artifact-bar span{flex:0 0 auto;color:var(--el-text-color-secondary);font-size:11px}
.web-artifact-placeholder{display:flex;align-items:center;justify-content:center;flex:1;background:var(--el-fill-color-lighter)}.web-artifact-placeholder button{padding:8px 14px;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-color-primary);cursor:pointer}
.web-artifact-message{margin:0;padding:12px 14px;color:var(--el-color-warning);font-size:12px;white-space:pre-wrap;overflow-wrap:anywhere}
.web-artifact :deep(.website-pane){flex:1}.web-artifact-dock{display:inline-flex;align-items:center;justify-content:center;gap:5px;flex-shrink:0;height:28px;padding:4px 7px;border:0;border-radius:5px;background:transparent;color:var(--el-text-color-regular);font:inherit;cursor:pointer;box-sizing:border-box}.web-artifact-dock:hover{background:var(--el-fill-color-light)}.web-artifact-dock:disabled{opacity:.45;cursor:default}.web-artifact-dock span{font-size:11px;white-space:nowrap}
@media(max-width:700px){.web-artifact{height:540px}.web-artifact :deep(.website-toolbar){flex-wrap:wrap}.web-artifact-dock span{display:none}}
</style>
