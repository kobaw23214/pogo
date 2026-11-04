<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import type { SkillContent } from "@/api/types";
import SkillDocumentViewer from "./SkillDocumentViewer.vue";

const props = defineProps<{
  skill: string;
  sessionId: string;
  path?: string;
}>();
const { t } = useI18n();
const root = ref<SkillContent | null>(null);
const loading = ref(false);
const error = ref("");
let generation = 0;

async function loadRoot() {
  const request = ++generation;
  loading.value = true;
  error.value = "";
  root.value = null;
  try {
    const document = await api.skillContent(props.skill, props.sessionId);
    if (request === generation) root.value = document;
  } catch (reason) {
    if (request === generation) error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    if (request === generation) loading.value = false;
  }
}
const loadDocument = (path: string) => api.skillContent(props.skill, props.sessionId, path);
watch([() => props.skill, () => props.sessionId], loadRoot, { immediate: true });
onBeforeUnmount(() => { generation++; });
</script>

<template>
  <section class="skill-document-preview" :data-skill="skill" :aria-busy="loading">
    <p v-if="loading" class="document-state" role="status">{{ t("skillsPanel.loading") }}</p>
    <div v-else-if="error" class="document-state document-error" role="alert">
      <strong>{{ t("skillDocuments.failed") }}</strong>
      <p>{{ error }}</p>
      <button type="button" @click="loadRoot">{{ t("common.retry") }}</button>
    </div>
    <SkillDocumentViewer
      v-else-if="root"
      :scope-key="`${sessionId}:${skill}:${path || 'SKILL.md'}`"
      :root-document="root"
      :load-document="loadDocument"
      :selected-path="path || 'SKILL.md'"
    />
  </section>
</template>

<style scoped>
.skill-document-preview{display:flex;flex-direction:column;min-width:0;min-height:0;background:var(--el-bg-color)}
.skill-document-preview>:deep(.skill-document-viewer){display:flex;flex:1;flex-direction:column;min-height:0;border:0;border-radius:0}
.skill-document-preview>:deep(.document-content){flex:1;max-height:none;min-height:0}
.document-state{margin:auto;padding:24px;color:var(--el-text-color-secondary);font-size:13px;line-height:1.8;text-align:center}
.document-error{color:var(--el-color-danger)}
.document-error p{white-space:pre-wrap;overflow-wrap:anywhere}
.document-error button{padding:5px 12px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;cursor:pointer}
</style>
