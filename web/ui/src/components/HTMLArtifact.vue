<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Document, Download, Lock, Monitor, Refresh } from "@element-plus/icons-vue";
import HTMLBrowserPane from "./HTMLBrowserPane.vue";
import { fileDownloadURL } from "@/api/client";
import { parseHTMLArtifact, resolveHTMLArtifact } from "@/agent/htmlArtifact";
import { workspaceFileNavigationKey, type WorkspaceFileScope } from "@/agent/workspaceFileNavigation";

const props = defineProps<{ source: string; scope: WorkspaceFileScope | null }>();
const navigation = inject(workspaceFileNavigationKey, null);
const { t } = useI18n();
const host = ref<HTMLElement | null>(null);
const pane = ref<{ reload: () => Promise<void> } | null>(null);
const activated = ref(false);
const openingDock = ref(false);
const dockError = ref("");
let observer: IntersectionObserver | undefined;

const spec = computed(() => parseHTMLArtifact(props.source));
const resolved = computed(() => spec.value ? resolveHTMLArtifact(spec.value, props.scope) : null);
const downloadURL = computed(() => resolved.value ? fileDownloadURL(resolved.value.fullPath) : "");

function activate() {
  activated.value = true;
  observer?.disconnect();
  observer = undefined;
}
function openSource() {
  if (!resolved.value || !props.scope || !navigation) return;
  void navigation.open(resolved.value.target, { conversationId: props.scope.conversationId, cwd: props.scope.cwd });
}
async function openDock() {
  if (!resolved.value || !props.scope || !navigation?.openHTML || openingDock.value) return;
  openingDock.value = true;
  dockError.value = "";
  try {
    await navigation.openHTML(resolved.value.target, { conversationId: props.scope.conversationId, cwd: props.scope.cwd });
  } catch (error) {
    dockError.value = error instanceof Error ? error.message : String(error);
  } finally {
    openingDock.value = false;
  }
}

onMounted(() => {
  if (typeof IntersectionObserver === "undefined" || !host.value) return;
  observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting)) activate();
  }, { rootMargin: "240px" });
  observer.observe(host.value);
});
onBeforeUnmount(() => observer?.disconnect());
</script>

<template>
  <section ref="host" class="html-artifact" :class="{ unavailable: !resolved }">
    <header class="html-artifact-bar">
      <div class="html-artifact-heading">
        <strong>{{ resolved?.title || spec?.title || "HTML" }}</strong>
        <span class="html-artifact-badge"><el-icon><Lock /></el-icon>{{ t("htmlArtifact.sandbox") }}</span>
      </div>
      <div v-if="resolved" class="html-artifact-actions">
        <button type="button" :title="t('htmlArtifact.source')" @click="openSource"><el-icon><Document /></el-icon><span>{{ t("htmlArtifact.source") }}</span></button>
        <button v-if="activated" type="button" :title="t('htmlArtifact.refresh')" @click="pane?.reload()"><el-icon><Refresh /></el-icon><span>{{ t("htmlArtifact.refresh") }}</span></button>
        <a :href="downloadURL" :download="resolved.target.name" :title="t('htmlArtifact.download')"><el-icon><Download /></el-icon><span>{{ t("htmlArtifact.download") }}</span></a>
        <button type="button" :disabled="!navigation?.openHTML || openingDock" :title="t('htmlArtifact.openDock')" @click="openDock"><el-icon><Monitor /></el-icon><span>{{ t("htmlArtifact.openDock") }}</span></button>
      </div>
    </header>
    <p v-if="!resolved" class="html-artifact-error" role="alert">{{ t(spec ? "htmlArtifact.unavailable" : "htmlArtifact.invalid") }}</p>
    <p v-if="dockError" class="html-artifact-error" role="alert">{{ t("htmlArtifact.openFailed") }}{{ dockError }}</p>
    <div v-if="resolved" class="html-artifact-preview">
      <HTMLBrowserPane v-if="activated" :key="`${scope?.conversationId}:${resolved.fullPath}`" ref="pane" :path="resolved.fullPath" :session-id="scope!.conversationId" active :show-toolbar="false" />
      <div v-else class="html-artifact-placeholder">
        <button type="button" @click="activate">{{ t("htmlArtifact.load") }}</button>
      </div>
    </div>
  </section>
</template>

<style scoped lang="scss">
.html-artifact {
  overflow: hidden;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-bg-color);
}
.html-artifact-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 46px;
  padding: 7px 10px 7px 13px;
}
.html-artifact-heading { display: flex; align-items: center; gap: 9px; min-width: 0; }
.html-artifact-heading strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.html-artifact-badge { display: inline-flex; align-items: center; gap: 3px; flex: 0 0 auto; color: var(--el-text-color-secondary); font-size: 11px; }
.html-artifact-actions { display: flex; align-items: center; gap: 3px; flex: 0 0 auto; }
.html-artifact-actions button, .html-artifact-actions a {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 5px 7px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 12px;
  text-decoration: none;
  cursor: pointer;
  &:hover { background: var(--el-fill-color-light); color: var(--el-color-primary); }
  &:disabled { cursor: default; opacity: 0.5; }
}
.html-artifact-preview { height: 640px; border-top: 1px solid var(--el-border-color-extra-light); }
.html-artifact-placeholder { display: flex; align-items: center; justify-content: center; height: 100%; background: var(--el-fill-color-lighter); }
.html-artifact-placeholder button { padding: 8px 14px; border: 1px solid var(--el-border-color); border-radius: 7px; background: var(--el-bg-color); color: var(--el-color-primary); cursor: pointer; }
.html-artifact-error { margin: 0; padding: 12px 14px; border-top: 1px solid var(--el-border-color-extra-light); color: var(--el-color-warning); font-size: 12px; white-space: pre-wrap; }
.html-artifact.unavailable .html-artifact-error { border-top: 0; }
@media (max-width: 700px) {
  .html-artifact-bar { align-items: flex-start; flex-direction: column; }
  .html-artifact-actions { width: 100%; flex-wrap: wrap; }
  .html-artifact-preview { height: 540px; }
}
</style>
