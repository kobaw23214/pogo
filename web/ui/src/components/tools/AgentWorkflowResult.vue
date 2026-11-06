<script setup lang="ts">
import { computed, inject } from "vue";
import { useI18n } from "vue-i18n";
import { agentWorkflowNavigationKey, agentWorkflowReceiptLocation, type AgentWorkflowDetails } from "@/agent/agentWorkflowNavigation";
import WorkflowDiagnostics from "@/components/WorkflowDiagnostics.vue";
import type { HostWorkflowDiagnostic } from "@/agent/hostWorkflowDiagnostic";
const props = defineProps<{ details: AgentWorkflowDetails }>();
const { t } = useI18n();
const navigation = inject(agentWorkflowNavigationKey, undefined);
const result = computed(() => props.details.result);
const workflow = computed(() => result.value.workflow as { name?: string; revision?: number; deleted?: boolean } | undefined);
const run = computed(() => result.value.run as { status?: string; trial?: boolean } | undefined);
const location = computed(() => agentWorkflowReceiptLocation(props.details, navigation?.agentId()));
const title = computed(() => workflow.value?.name || t(`agentWorkflow.${props.details.group === "edit" ? "editAction" : props.details.group === "enable" ? "enableAction" : props.details.group === "run" ? "runAction" : "read"}`));
const diagnostics = computed(() => Array.isArray(result.value.diagnostics) ? result.value.diagnostics as HostWorkflowDiagnostic[] : []);
const failure = computed(() => result.value.error as string | { message?: string } | undefined);
</script>
<template>
  <section class="internal-workflow-receipt" data-testid="agent-workflow-result">
    <strong>{{ title }}</strong><span v-if="workflow?.revision && !workflow.deleted">{{ t('agentWorkflow.revision', { n: workflow.revision }) }}</span>
    <p v-if="failure" role="alert">{{ typeof failure === 'string' ? failure : failure.message }}</p>
    <p v-else-if="workflow?.deleted">{{ t('agentWorkflow.deleted') }}</p>
    <p v-else-if="run">{{ run.trial ? t('agentWorkflow.testLabel') + ' · ' : '' }}{{ t(`agentWorkflow.status.${run.status || 'queued'}`) }}<template v-if="run.status !== 'completed'"> · {{ t('agentWorkflow.waiting') }}</template></p>
    <p v-else-if="details.action === 'validate'">{{ t(result.valid ? 'agentWorkflow.valid' : 'agentWorkflow.invalid') }}</p>
    <WorkflowDiagnostics v-if="diagnostics.length" :diagnostics="diagnostics" domain="agentWorkflow" />
    <button v-if="location && navigation" type="button" @click="navigation.open(location!)">{{ t('agentWorkflow.open') }}</button>
  </section>
</template>
<style scoped>
.internal-workflow-receipt{display:flex;flex-direction:column;gap:7px;padding:12px 14px;border:1px solid var(--el-border-color);border-radius:10px;color:var(--el-text-color-primary)}span,p{font-size:12px;color:var(--el-text-color-secondary);margin:0}button{align-self:flex-start;font:inherit;font-size:12px;border:0;padding:0;background:none;color:var(--el-color-primary);cursor:pointer}ul{padding-left:18px;margin:0;font-size:12px;line-height:1.8}small{display:block;color:var(--el-text-color-secondary)}
</style>
