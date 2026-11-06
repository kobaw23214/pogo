<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
import type { SkillExecutionIssue, SkillExecutionStatus } from '@/api/skillTypes';
import { skillExecutionMessages, skillExecutionPlatforms, skillExecutionDiagnosticDetails } from '@/agent/skillExecution';

const props = defineProps<{ execution?: SkillExecutionStatus | null; issues?: SkillExecutionIssue[] }>();
const { t, locale } = useI18n();
const platforms = computed(() => skillExecutionPlatforms(props.execution));
const messages = computed(() => skillExecutionMessages(props.execution, props.issues));
const diagnostics = computed(() => skillExecutionDiagnosticDetails(props.execution, props.issues));
const supportedPlatforms = computed(() => new Intl.ListFormat(locale.value, { style: 'long', type: 'conjunction' }).format(platforms.value.supported));
const warning = computed(() => messages.value.length > 0 || platforms.value.mismatch || props.execution?.configured === false);
</script>

<template>
  <div v-if="platforms.host || platforms.expected || warning" class="execution-notice" :class="{ warning }" role="status">
    <p v-if="platforms.host">{{ t('skillExecution.host', { platform: platforms.host }) }}</p>
    <p v-if="platforms.expected">
      {{ t('skillExecution.expected', { platform: platforms.expected }) }}
      <span v-if="platforms.mismatch"> · {{ t('skillExecution.mismatch') }}</span>
    </p>
    <p v-if="warning && platforms.supported.length">{{ t('skillExecution.supported', { platforms: supportedPlatforms }) }}</p>
    <ul v-if="messages.length"><li v-for="message in messages" :key="message">{{ message }}</li></ul>
    <p v-else-if="execution?.configured === false">{{ t('skillExecution.notConfigured') }}</p>
    <details v-if="diagnostics.length"><summary>{{ t('settingsCoverage.technicalDetails') }}</summary><pre v-for="diagnostic in diagnostics" :key="diagnostic">{{ diagnostic }}</pre></details>
  </div>
</template>

<style scoped>
.execution-notice{margin:12px 0;padding:12px 14px;border-radius:8px;background:var(--el-fill-color-lighter);color:var(--el-text-color-secondary);font-size:12px;line-height:1.8;overflow-wrap:anywhere}.execution-notice.warning{background:var(--el-color-warning-light-9);color:var(--el-text-color-regular)}.execution-notice p{margin:0}.execution-notice ul{margin:6px 0 0;padding-left:20px}
.execution-notice details{margin-top:6px}.execution-notice summary{cursor:pointer}.execution-notice pre{white-space:pre-wrap;overflow-wrap:anywhere;margin:6px 0;font:inherit}
</style>
