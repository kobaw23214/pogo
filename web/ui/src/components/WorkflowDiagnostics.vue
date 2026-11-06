<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { hostWorkflowDiagnostic, type HostWorkflowDiagnostic, type HostWorkflowDiagnosticDomain } from "@/agent/hostWorkflowDiagnostic";

const props = defineProps<{ diagnostics: readonly HostWorkflowDiagnostic[]; domain: HostWorkflowDiagnosticDomain }>();
const { t } = useI18n();
const rows = computed(() => props.diagnostics.map(diagnostic => hostWorkflowDiagnostic(diagnostic, props.domain)));
</script>

<template>
  <ul class="workflow-diagnostics">
    <li v-for="(row, index) in rows" :key="index">
      <strong>{{ row.message }}</strong>
      <p v-if="row.hint">{{ row.hint }}</p>
      <small v-if="row.location || row.code"><code>{{ [row.location, row.code].filter(Boolean).join(' · ') }}</code></small>
      <details v-if="row.original">
        <summary>{{ t('workflowDiagnostics.originalDetails') }}</summary>
        <pre>{{ row.original }}</pre>
      </details>
    </li>
  </ul>
</template>

<style scoped>
.workflow-diagnostics{margin:12px 0;padding-left:20px;overflow-wrap:anywhere}.workflow-diagnostics li{margin:10px 0;line-height:1.6}.workflow-diagnostics strong{font-weight:600}.workflow-diagnostics p{margin:4px 0}.workflow-diagnostics small,.workflow-diagnostics summary{color:var(--el-text-color-secondary);font-size:12px}.workflow-diagnostics code{white-space:normal}.workflow-diagnostics details{margin-top:4px}.workflow-diagnostics summary{cursor:pointer}.workflow-diagnostics pre{font:inherit;font-size:12px;white-space:pre-wrap;margin:5px 0;color:var(--el-text-color-secondary)}
</style>
