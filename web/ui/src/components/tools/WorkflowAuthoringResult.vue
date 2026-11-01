<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { navigateStudio } from "@/api/platform";
import { workflowReceiptLocation, type WorkflowAuthoringDetails } from "@/agent/workflowAuthoringReceipt";
const props = defineProps<{ details: WorkflowAuthoringDetails }>();
const { t } = useI18n();
const result = computed(() => props.details.result);
const location = computed(() => workflowReceiptLocation(props.details));
const workflow = computed(() => (result.value.workflow ?? (typeof result.value.name === 'string' ? result.value : undefined)) as { name?: string; revision?: number } | undefined);
const diagnostics = computed(() => {
  const list = result.value.diagnostics ?? (result.value.validation as { diagnostics?: unknown } | undefined)?.diagnostics;
  return Array.isArray(list) ? list as { code?: string; message?: string; field_path?: string; fix_hint?: string }[] : [];
});
const changes = computed(() => Array.isArray(result.value.changes) ? result.value.changes as { operation?: string; node_id?: string; from?: string; to?: string }[] : []);
const failure = computed(() => result.value.error as { message?: string } | string | undefined);
</script>
<template>
  <section class="workflow-receipt" data-testid="workflow-authoring-result">
    <strong>{{ workflow?.name || t(`workflowAuthoring.${details.action}`) }}</strong>
    <span v-if="workflow?.revision">{{ t('workflowAuthoring.revision', { n: workflow.revision }) }}</span>
    <span v-if="result.version_id">{{ result.version_id }}</span>
    <p v-if="result.test_run">{{ t('workflowAuthoring.testLabel') }} · {{ t('workflowAuthoring.existing') }}</p>
    <p v-if="failure" role="alert">{{ typeof failure === 'string' ? failure : failure.message || t('workflowAuthoring.failed') }}</p>
    <p v-else-if="details.action === 'discover'">{{ t('workflowAuthoring.resources') }}</p>
    <p v-else-if="details.action === 'validate'">{{ t(result.valid === false ? 'workflowAuthoring.invalid' : result.ready_for_test === false ? 'workflowAuthoring.runtimeUnavailable' : 'workflowAuthoring.valid') }}</p>
    <p v-if="!failure && (details.action === 'test' || details.action === 'run')">{{ t('workflowAuthoring.queued') }}</p>
    <ul v-if="diagnostics.length"><li v-for="(d, i) in diagnostics" :key="i"><strong>{{ d.message }}</strong><small v-if="d.field_path">{{ d.field_path }}</small><span v-if="d.fix_hint">{{ d.fix_hint }}</span></li></ul>
    <ul v-if="changes.length"><li v-for="(change, i) in changes" :key="i">{{ t(`workflowAuthoring.operations.${change.operation}`) }} · {{ change.node_id || [change.from, change.to].filter(Boolean).join(' → ') }}</li></ul>
    <a v-if="location" :href="location" @click.prevent="navigateStudio(location!)">{{ t('workflowAuthoring.open') }}</a>
  </section>
</template>
<style scoped>
.workflow-receipt{display:flex;flex-direction:column;gap:7px;padding:12px 14px;border:1px solid var(--el-border-color);border-radius:10px;color:var(--el-text-color-primary)}.workflow-receipt>span,.workflow-receipt p{font-size:12px;color:var(--el-text-color-secondary);margin:0}.workflow-receipt a{font-size:13px;color:var(--el-color-primary)}ul{padding-left:18px;margin:0}li{margin:5px 0;font-size:12px;overflow-wrap:anywhere}li small,li span{display:block;color:var(--el-text-color-secondary);font-weight:normal}
</style>
