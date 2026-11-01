<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { WorkflowApprovalAssignment } from "@/agent/workflowTypes";
const props = defineProps<{ assignment?: WorkflowApprovalAssignment; pending?: boolean }>();
const { t } = useI18n();
const assignees = computed(() => (props.assignment?.assignees ?? []).map(subject => subject.display_name || subject.principal_id).join("、"));
const initial = computed(() => (props.assignment?.initial_assignees ?? []).map(subject => subject.display_name || subject.principal_id).join("、"));
const knownReasons = new Set(["initiator_cannot_approve", "not_assigned", "no_eligible_approver", "account_disabled"]);
const reason = computed(() => {
  const code = props.assignment?.blocked_reason;
  return code ? knownReasons.has(code) ? t(`workflowApproval.reason.${code}`) : code : "";
});
</script>

<template>
  <section v-if="assignment" class="approval-assignment" :aria-label="t('workflowApproval.title')">
    <p v-if="assignees">{{ t("workflowApproval.assigned", { names: assignees }) }}</p>
    <p v-else-if="pending">{{ t("workflowApproval.unassigned") }}</p>
    <p v-if="initial && initial !== assignees">{{ t("workflowApproval.initial", { names: initial }) }}</p>
    <p v-if="assignment.prevent_self_approval">{{ t("workflowApproval.protected") }}</p>
    <p v-if="pending">{{ t("workflowApproval.decisionRule") }}</p>
    <p v-if="pending && reason" class="assignment-reason" role="note">{{ reason }}</p>
  </section>
</template>

<style scoped>
.approval-assignment{display:flex;flex-direction:column;gap:6px;border:1px solid var(--el-border-color-lighter);border-radius:8px;padding:10px 12px;background:var(--el-fill-color-extra-light);font-size:12px;line-height:1.6;color:var(--el-text-color-secondary);overflow-wrap:anywhere;margin:10px 0}.approval-assignment p{margin:0}.assignment-reason{color:var(--el-color-warning)}
</style>
