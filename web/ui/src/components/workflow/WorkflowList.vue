<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight, Delete, Plus, Share } from "@element-plus/icons-vue";
import { navigateStudio, platformApi } from "@/api/platform";
import type { Workflow } from "@/agent/workflowTypes";
import { formatAgentTime } from "@/agent/studioPresentation";
import { validateWorkflow } from "@/agent/workflowPresentation";

/**
 * The workflows module's own surface: the card list, the creation dialog and
 * the delete confirmation. It loads itself (the catalog in the shell above is
 * the Agents one) and hands each workflow off to the full-screen editor.
 */
const props = defineProps<{ canManage: boolean }>();

const { t } = useI18n();

const workflows = ref<Workflow[]>([]);
const serverCanCreate = ref<boolean | null>(null);
const canCreate = computed(() => serverCanCreate.value ?? props.canManage);
const loading = ref(false);
const error = ref("");
const createOpen = ref(false);
const createName = ref("");
const createDesc = ref("");
const createBusy = ref(false);
const createError = ref("");
const deleting = ref<Workflow | null>(null);
const deleteBusy = ref(false);
const deleteError = ref("");

const visible = computed(() => workflows.value.filter((w) => !w.deleted));

async function load() {
  loading.value = true;
  error.value = "";
  try {
    const out = await platformApi.workflows.list();
    workflows.value = out.workflows;
    serverCanCreate.value = out.can_manage;
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  if (!canCreate.value) return;
  createName.value = "";
  createDesc.value = "";
  createError.value = "";
  createOpen.value = true;
}

/** The dialog's own gate: a name within limits, nothing more — the editor
    does the rest. */
const createIssues = computed(() =>
  validateWorkflow({ name: createName.value, description: createDesc.value, nodes: [{ id: "n1", agent: "x", task: "x" }] }).form.filter((code) => code.startsWith("name") || code.startsWith("description")),
);

async function create() {
  if (!canCreate.value || createBusy.value || createIssues.value.length) return;
  createBusy.value = true;
  createError.value = "";
  try {
    const wf = await platformApi.workflows.create({
      name: createName.value.trim(),
      description: createDesc.value.trim(),
      nodes: [],
    });
    createOpen.value = false;
    navigateStudio(`/manage/workflows/${encodeURIComponent(wf.id)}`);
  } catch (e) {
    createError.value = (e as Error).message;
  } finally {
    createBusy.value = false;
  }
}

async function remove() {
  const target = deleting.value;
  if (!target || deleteBusy.value || !(target.capabilities?.can_delete ?? props.canManage)) return;
  deleteBusy.value = true;
  deleteError.value = "";
  try {
    await platformApi.workflows.remove(target.id);
    workflows.value = workflows.value.filter((w) => w.id !== target.id);
    deleting.value = null;
  } catch (e) {
    deleteError.value = (e as Error).message;
  } finally {
    deleteBusy.value = false;
  }
}

function openEditor(wf: Workflow) {
  navigateStudio(`/manage/workflows/${encodeURIComponent(wf.id)}`);
}

onMounted(() => void load());
defineExpose({ reload: load, openCreate });
</script>

<template>
  <div class="wf-list">
    <div v-if="canCreate && !canManage && visible.length" class="wf-empty-actions">
      <button type="button" class="btn solid" @click="openCreate"><el-icon aria-hidden="true"><Plus /></el-icon>{{ t("workflow.list.create") }}</button>
    </div>
    <div v-if="error" class="wf-list-error" role="alert">
      <span>{{ error }}</span>
      <button type="button" class="btn ghost sm" @click="load">{{ t("workflow.editor.save.refresh") }}</button>
    </div>

    <div v-if="loading && !visible.length" class="wf-cards skel" aria-hidden="true">
      <div v-for="i in 3" :key="i" class="wf-card skel-card">
        <span class="skel-block line w60" />
        <span class="skel-block line w100" />
        <span class="skel-block line w40" />
      </div>
    </div>

    <div v-else-if="!visible.length" class="wf-empty">
      <h2>{{ t("workflow.list.empty") }}</h2>
      <p>{{ t("workflow.list.emptyHint") }}</p>
      <div class="wf-empty-actions">
        <button v-if="canCreate" type="button" class="btn solid" @click="openCreate">
          <el-icon aria-hidden="true"><Plus /></el-icon>
          {{ t("workflow.list.emptyAction") }}
        </button>
      </div>
    </div>

    <div v-else class="wf-cards">
      <article v-for="wf in visible" :key="wf.id" class="wf-card">
        <div class="wf-card-top">
          <span class="wf-card-glyph" aria-hidden="true"><el-icon><Share /></el-icon></span>
          <div class="wf-card-head">
            <button type="button" class="wf-card-name" :title="wf.name" @click="openEditor(wf)">{{ wf.name }}</button>
            <div class="wf-card-chips">
              <span class="wf-chip" :class="wf.published ? 'ready' : 'plain'">{{ wf.published ? t("workflow.list.publishedChip") : t("workflow.list.unpublishedChip") }}</span>
              <span v-if="wf.dirty" class="wf-chip draft">{{ t("workflow.list.dirtyChip") }}</span>
              <span class="wf-chip plain">{{ t("workflow.list.nodeCount", { n: wf.nodes.length }) }}</span>
            </div>
          </div>
          <button
            v-if="wf.capabilities?.can_delete ?? canManage"
            type="button"
            class="wf-iconbtn tip"
            :data-tip="t('workflow.list.remove')"
            :aria-label="t('workflow.list.remove')"
            @click="deleting = wf"
          >
            <el-icon aria-hidden="true"><Delete /></el-icon>
          </button>
        </div>
        <p class="wf-card-desc" :class="{ muted: !wf.description }">{{ wf.description || t("workflow.list.noDescription") }}</p>
        <div class="wf-card-foot">
          <span class="wf-card-stat">{{ t("workflow.list.updatedAt", { time: formatAgentTime(wf.updated_at) || "—" }) }}</span>
          <button type="button" class="wf-enter solid" @click="openEditor(wf)">
            {{ t("workflow.list.open") }}
            <el-icon aria-hidden="true"><ArrowRight /></el-icon>
          </button>
        </div>
      </article>
    </div>

    <el-dialog
      :model-value="createOpen"
      :title="t('workflow.list.createTitle')"
      width="min(440px, 94vw)"
      :close-on-click-modal="false"
      @update:model-value="createOpen = $event"
    >
      <div class="wf-dialog-form">
        <el-input v-model="createName" :placeholder="t('workflow.list.namePlaceholder')" :maxlength="60" autofocus />
        <el-input v-model="createDesc" type="textarea" :rows="3" :maxlength="240" :placeholder="t('workflow.list.descPlaceholder')" />
        <p v-if="createIssues.length" class="wf-dialog-error" role="alert">{{ t(`workflow.errors.${createIssues[0]}`) }}</p>
        <p v-else-if="createError" class="wf-dialog-error" role="alert">{{ createError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn ghost" :disabled="createBusy" @click="createOpen = false">{{ t("workflow.list.cancel") }}</button>
        <button type="button" class="btn solid" :disabled="createBusy || !createName.trim()" @click="create">
          {{ createBusy ? t("workflow.list.creating") : t("workflow.list.confirm") }}
        </button>
      </template>
    </el-dialog>

    <el-dialog
      :model-value="!!deleting"
      :title="t('workflow.list.removeTitle')"
      width="min(400px, 94vw)"
      @update:model-value="deleting = null"
    >
      <p class="wf-dialog-body">{{ t("workflow.list.removeBody", { name: deleting?.name }) }}</p>
      <p v-if="deleteError" class="wf-dialog-error" role="alert">{{ deleteError }}</p>
      <template #footer>
        <button type="button" class="btn ghost" :disabled="deleteBusy" @click="deleting = null">{{ t("workflow.list.keep") }}</button>
        <button type="button" class="btn danger" :disabled="deleteBusy" @click="remove">
          {{ deleteBusy ? t("workflow.list.removeBusy") : t("workflow.list.removeConfirm") }}
        </button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.wf-list {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.wf-list-error {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  color: var(--el-color-danger);
  border-radius: 10px;
  padding: 10px 14px;
  font-size: 13px;
}
.wf-list-error span {
  flex: 1;
  min-width: 0;
}

.wf-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(max(300px, calc((100% - 28px) / 3)), 1fr));
  gap: 14px;
}
.wf-card {
  position: relative;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.wf-card:hover,
.wf-card:focus-within {
  border-color: var(--el-border-color);
  box-shadow: var(--el-box-shadow-light);
}
.wf-card-top {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-width: 0;
}
.wf-card-glyph {
  flex: none;
  width: 40px;
  height: 40px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 12px;
  font-size: 19px;
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  box-shadow: inset 0 0 0 1px var(--pg-accent-line);
}
.wf-card-head {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding-top: 1px;
}
.wf-card-name {
  align-self: flex-start;
  max-width: 100%;
  border: 0;
  background: none;
  padding: 0;
  margin: 0;
  font: inherit;
  font-size: 15px;
  font-weight: 650;
  color: var(--el-text-color-primary);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  transition: color var(--pg-transition);
}
.wf-card-name:hover {
  color: var(--el-color-primary);
}
.wf-card-chips {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.wf-chip {
  font-size: 11.5px;
  border-radius: 999px;
  padding: 2px 9px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}
.wf-chip.ready {
  font-weight: 600;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}
.wf-chip.draft {
  font-weight: 600;
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}
.wf-card-desc {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 13px;
  line-height: 1.6;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 41px;
}
.wf-card-desc.muted {
  color: var(--el-text-color-placeholder);
}
.wf-card-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: auto;
  padding-top: 11px;
  border-top: 1px solid var(--el-border-color-extra-light);
}
.wf-card-stat {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  min-width: 0;
}
.wf-enter {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px solid var(--pg-solid);
  background: var(--pg-solid);
  color: var(--pg-on-solid);
  border-radius: 8px;
  padding: 6px 12px;
  font: inherit;
  font-size: 12.5px;
  font-weight: 550;
  cursor: pointer;
  transition: background var(--pg-transition), border-color var(--pg-transition);
}
.wf-enter:hover {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.wf-iconbtn {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.wf-iconbtn:hover {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.wf-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 72px 24px 80px;
  background: var(--el-bg-color);
  border: 1px dashed var(--el-border-color);
  border-radius: var(--pg-radius-card);
}
.wf-empty h2 {
  margin: 0 0 8px;
  font-size: 17px;
  font-weight: 700;
}
.wf-empty p {
  margin: 0 0 22px;
  color: var(--el-text-color-regular);
  font-size: 13.5px;
  max-width: 420px;
}

.wf-dialog-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.wf-dialog-body {
  margin: 0;
  font-size: 13px;
  line-height: 1.8;
  color: var(--el-text-color-regular);
}
.wf-dialog-error {
  margin: 0;
  font-size: 12px;
  color: var(--el-color-danger);
  line-height: 1.6;
}

/* dialog footer buttons reuse the ManageView shell's .btn look, restated here
   because scoped styles do not cross the el-dialog teleport. */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 8px 15px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  transition: background var(--pg-transition), border-color var(--pg-transition);
}
.btn.sm {
  padding: 6px 12px;
  font-size: 12.5px;
}
.btn.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.btn.solid:hover:not(:disabled) {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.btn.ghost {
  background: transparent;
  border-color: var(--el-border-color);
  color: var(--el-text-color-regular);
}
.btn.ghost:hover:not(:disabled) {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.btn.danger {
  background: var(--el-color-danger);
  border-color: var(--el-color-danger);
  color: var(--el-color-white, #fff);
}
.btn.danger:hover:not(:disabled) {
  background: var(--el-color-danger-dark-2);
  border-color: var(--el-color-danger-dark-2);
}
.btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.skel {
  pointer-events: none;
}
.skel-card {
  gap: 12px;
}
.skel-block {
  display: block;
  background: linear-gradient(90deg, var(--el-fill-color-light) 25%, var(--el-fill-color-lighter) 50%, var(--el-fill-color-light) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s ease-in-out infinite;
}
.skel-block.line {
  height: 12px;
  border-radius: 6px;
}
.w40 {
  width: 40%;
}
.w60 {
  width: 60%;
}
.w100 {
  width: 100%;
}
@keyframes shimmer {
  0% {
    background-position: 180% 0;
  }
  100% {
    background-position: -20% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .skel-block {
    animation: none;
  }
}
</style>
