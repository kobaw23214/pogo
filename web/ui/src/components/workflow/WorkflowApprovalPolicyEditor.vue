<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { platformApi } from "@/api/platform";
import type { WorkflowApprovalPolicy, WorkflowApprovalSubject } from "@/agent/workflowTypes";
import { cloneWorkflowApprovalPolicy, defaultWorkflowApprovalPolicy } from "@/agent/workflowApprovalPolicy";

const props = defineProps<{ modelValue?: WorkflowApprovalPolicy; workflowId: string; active: boolean; disabled: boolean }>();
const emit = defineEmits<{ "update:modelValue": [WorkflowApprovalPolicy | undefined] }>();
const { t } = useI18n();
const subjects = ref<WorkflowApprovalSubject[]>([]);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
let generation = 0;
const enabled = computed({ get: () => !!props.modelValue, set: value => emit("update:modelValue", value ? defaultWorkflowApprovalPolicy() : undefined) });
function update(value: Partial<WorkflowApprovalPolicy>) {
  if (props.disabled || !props.modelValue) return;
  emit("update:modelValue", { ...cloneWorkflowApprovalPolicy(props.modelValue), ...value });
}
const selected = computed({ get: () => props.modelValue?.approvers ?? [], set: (value: string[]) => update({ approvers: [...value] }) });
function selectAccount(name: string, checked: boolean) {
  selected.value = checked ? [...new Set([...selected.value, name])] : selected.value.filter(value => value !== name);
}
const options = computed(() => {
  const result = subjects.value.map(subject => ({ value: subject.username, label: subject.display_name || subject.username }));
  for (const name of selected.value) if (!result.some(option => option.value === name)) {
    result.push({ value: name, label: loaded.value ? t("workflowApproval.unavailable", { name }) : name });
  }
  return result;
});
async function loadSubjects() {
  const request = ++generation;
  if (!props.active || props.disabled || props.modelValue?.assignment !== "fixed") { loading.value = false; return; }
  const wid = props.workflowId;
  loading.value = true; error.value = "";
  try {
    const response = await platformApi.workflows.approvalSubjects(wid);
    if (request !== generation || props.workflowId !== wid) return;
    subjects.value = response.subjects; loaded.value = true;
  } catch (failure) {
    if (request === generation) error.value = failure instanceof Error ? failure.message : String(failure);
  } finally { if (request === generation) loading.value = false; }
}
watch([() => props.workflowId, () => props.active, () => props.disabled, () => props.modelValue?.assignment], () => {
  subjects.value = []; loaded.value = false; error.value = ""; void loadSubjects();
}, { immediate: true });
</script>

<template>
  <fieldset class="approval-policy">
    <legend>{{ t("workflowApproval.title") }}</legend>
    <label class="policy-check"><input v-model="enabled" type="checkbox" :disabled="disabled">{{ t("workflowApproval.enable") }}</label>
    <p v-if="!modelValue" class="policy-hint">{{ t("workflowApproval.legacy") }}</p>
    <template v-else>
      <label class="policy-check"><input type="checkbox" :checked="modelValue.prevent_self_approval" :disabled="disabled" @change="update({ prevent_self_approval: ($event.target as HTMLInputElement).checked })">{{ t("workflowApproval.preventSelf") }}</label>
      <p v-if="modelValue.prevent_self_approval" class="policy-hint">{{ t("workflowApproval.preventSelfHint") }}</p>
      <label class="policy-field"><span>{{ t("workflowApproval.assignment") }}</span><select :value="modelValue.assignment" :disabled="disabled" @change="update({ assignment: ($event.target as HTMLSelectElement).value === 'business' ? 'business' : 'fixed', approvers: [] })"><option value="fixed">{{ t("workflowApproval.fixed") }}</option><option value="business">{{ t("workflowApproval.business") }}</option></select></label>
      <template v-if="modelValue.assignment === 'fixed'">
        <div class="policy-field"><span>{{ t("workflowApproval.approvers") }}</span><div class="policy-accounts" role="group" :aria-label="t('workflowApproval.choose')"><label v-for="option in options" :key="option.value" class="policy-check"><input type="checkbox" :checked="selected.includes(option.value)" :disabled="disabled || loading" @change="selectAccount(option.value, ($event.target as HTMLInputElement).checked)">{{ option.label }}</label><p v-if="loaded && !options.length" class="policy-hint">{{ t("workflowApproval.noAccounts") }}</p></div></div>
        <p class="policy-hint">{{ t("workflowApproval.fixedHint") }}</p>
        <p v-if="loading" class="policy-hint" role="status">{{ t("workflowApproval.loading") }}</p>
        <p v-if="error" class="policy-error" role="alert">{{ t("workflowApproval.loadFailed", { message: error }) }} <button type="button" :disabled="disabled || loading" @click="loadSubjects">{{ t("workflowApproval.retry") }}</button></p>
      </template>
      <p v-else class="policy-hint">{{ t("workflowApproval.businessHint") }}</p>
      <p class="policy-hint">{{ t("workflowApproval.decisionRule") }}</p>
    </template>
  </fieldset>
</template>

<style scoped>
.approval-policy{display:flex;flex-direction:column;gap:12px;border:1px solid var(--el-border-color-lighter);border-radius:9px;padding:14px;min-width:0}.approval-policy legend{font-size:13px;font-weight:600;padding:0 4px}.policy-check{display:flex;align-items:center;gap:7px;font-size:13px}.policy-field{display:flex;flex-direction:column;gap:7px;font-size:13px}.policy-field select,.policy-accounts{font:inherit;padding:8px;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-primary);width:100%;box-sizing:border-box}.policy-accounts{display:flex;flex-direction:column;gap:10px;max-height:180px;overflow:auto}.policy-hint,.policy-error{margin:0;font-size:12px;line-height:1.6;overflow-wrap:anywhere}.policy-hint{color:var(--el-text-color-secondary)}.policy-error{color:var(--el-color-danger)}.policy-error button{font:inherit;color:inherit;cursor:pointer;background:none;border:0;text-decoration:underline}
</style>
