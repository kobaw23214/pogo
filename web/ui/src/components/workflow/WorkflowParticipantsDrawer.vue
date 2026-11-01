<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { platformApi } from "@/api/platform";
import type { WorkflowRun, WorkflowRunParticipant, WorkflowRunParticipants } from "@/agent/workflowTypes";
import { workflowRunCan } from "@/agent/workflowPresentation";
import { watchWorkflowFormIdentity } from "@/agent/workflowFormIdentity";

const props = defineProps<{ open: boolean; workflowId: string; run: WorkflowRun | null }>();
const emit = defineEmits<{ "update:open": [boolean]; changed: [] }>();
const { t } = useI18n();
const data = ref<WorkflowRunParticipants>({ participants: [], subjects: [] });
const loading = ref(false);
const busy = ref(false);
const error = ref("");
const principal = ref("");
const actions = ref<string[]>(["run.read", "artifact.read"]);
const nodeId = ref("");
const actionNames = ["run.read", "run.cancel", "run.retry", "approval.decide", "artifact.read"];
const availableActions = computed(() => nodeId.value ? actionNames.filter((action) => !["run.cancel", "run.retry"].includes(action)) : actionNames);
const canManageParticipants = computed(() => workflowRunCan(props.run, "can_manage_participants", false));
let epoch = 0;

function actionLabel(action: string) {
  return t(`workflow.participants.actions.${action.replaceAll(".", "_")}`);
}

async function load() {
  const current = props.run;
  const request = ++epoch;
  if (!current || !props.open || !workflowRunCan(current, "can_manage_participants", false)) return;
  loading.value = true;
  error.value = "";
  try {
    const result = await platformApi.workflowRuns.participants(props.workflowId, current.id);
    if (request === epoch) data.value = result;
  } catch (e) {
    if (request === epoch) error.value = (e as Error).message;
  } finally {
    if (request === epoch) loading.value = false;
  }
}

async function save(participant?: WorkflowRunParticipant) {
  const current = props.run;
  if (!current || busy.value || !workflowRunCan(current, "can_manage_participants", false)) return;
  const principalId = participant?.principal_id ?? principal.value;
  if (!principalId || (!participant && !actions.value.length)) return;
  busy.value = true;
  error.value = "";
  try {
    await platformApi.workflowRuns.setParticipant(props.workflowId, current.id, {
      principal_id: principalId,
      actions: participant ? [] : [...actions.value],
      node_id: (participant ? participant.node_id : nodeId.value) || undefined,
    });
    if (props.run?.id !== current.id) return;
    await load();
    emit("changed");
  } catch (e) {
    if (props.run?.id === current.id) error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

watchWorkflowFormIdentity([() => props.open, () => props.workflowId, () => props.run?.id, () => canManageParticipants.value], () => {
  ++epoch;
  data.value = { participants: [], subjects: [] };
  loading.value = false;
  principal.value = "";
  nodeId.value = "";
  error.value = "";
  void load();
}, { immediate: true });
watch(nodeId, () => { actions.value = actions.value.filter((action) => availableActions.value.includes(action)); });
</script>

<template>
  <el-drawer :model-value="open" :title="t('workflow.participants.title')" direction="rtl" size="420px" @update:model-value="emit('update:open', $event)">
    <div class="wf-participants">
      <p>{{ t("workflow.participants.hint") }}</p>
      <p v-if="error" role="alert" class="wf-participants-error">{{ error }}</p>
      <p v-if="loading">{{ t("workflow.participants.loading") }}</p>
      <template v-else>
        <div v-for="item in data.participants" :key="`${item.principal_id}:${item.node_id ?? ''}`" class="wf-participant">
          <strong>{{ item.display_name || item.principal_id }}</strong>
          <span>{{ item.node_id ? t("workflow.participants.nodeScope", { node: item.node_id }) : t("workflow.participants.runScope") }}</span>
          <span>{{ item.actions.map(actionLabel).join(" · ") }}</span>
          <button type="button" class="wf-participant-remove" :disabled="busy" @click="save(item)">{{ t("workflow.participants.revoke") }}</button>
        </div>
        <p v-if="!data.participants.length">{{ t("workflow.participants.empty") }}</p>
        <form class="wf-participant-form" @submit.prevent="save()">
          <label>{{ t("workflow.participants.account") }}</label>
          <el-select v-model="principal" :placeholder="t('workflow.participants.account')" :disabled="busy" filterable>
            <el-option v-for="subject in data.subjects" :key="subject.principal_id" :value="subject.principal_id" :label="subject.display_name" />
          </el-select>
          <label>{{ t("workflow.participants.permissions") }}</label>
          <el-checkbox-group v-model="actions" :disabled="busy">
            <el-checkbox v-for="action in availableActions" :key="action" :value="action">{{ actionLabel(action) }}</el-checkbox>
          </el-checkbox-group>
          <label>{{ t("workflow.participants.scope") }}</label>
          <el-select v-model="nodeId" :disabled="busy" :placeholder="t('workflow.participants.runScope')">
            <el-option value="" :label="t('workflow.participants.runScope')" />
            <el-option v-for="node in run?.definition.nodes ?? []" :key="node.id" :value="node.id" :label="t('workflow.participants.nodeScope', { node: node.id })" />
          </el-select>
          <button type="submit" class="wf-participant-save" :disabled="busy || !principal || !actions.length">{{ busy ? t("workflow.participants.saving") : t("workflow.participants.save") }}</button>
        </form>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.wf-participants, .wf-participant, .wf-participant-form { display: flex; flex-direction: column; gap: 10px; }
.wf-participants { font-size: 13px; }
.wf-participants p { margin: 0 0 8px; color: var(--el-text-color-secondary); }
.wf-participants .wf-participants-error { color: var(--el-color-danger); }
.wf-participant { padding: 12px; background: var(--el-fill-color-light); border-radius: 8px; font-size: 12px; }
.wf-participant span { overflow-wrap: anywhere; }
.wf-participant-remove { align-self: flex-end; border: 0; background: transparent; color: var(--el-color-danger); cursor: pointer; }
.wf-participant-form { margin-top: 12px; padding-top: 16px; border-top: 1px solid var(--el-border-color); }
.wf-participant-form label { font-weight: 600; }
.wf-participant-save { padding: 9px 12px; border: 0; border-radius: 6px; background: var(--el-color-primary); color: var(--el-color-white); cursor: pointer; }
.wf-participant-save:disabled, .wf-participant-remove:disabled { opacity: .5; cursor: default; }
</style>
