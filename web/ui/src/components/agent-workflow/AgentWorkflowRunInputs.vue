<script setup lang="ts">
import { computed, ref, useId } from 'vue';
import { useI18n } from 'vue-i18n';
import type { AgentWorkflowInput, AgentWorkflowResourceCatalog } from '@/api/agentWorkflows';
import { workflowInputOptions, workflowSelectedFiles } from '@/agent/agentWorkflowInputs';

const props = defineProps<{ fields: AgentWorkflowInput[]; modelValue: Record<string,string>; files: string; catalog?: AgentWorkflowResourceCatalog | null; loading?: boolean; error?: string; disabled?: boolean }>();
const emit = defineEmits<{ 'update:modelValue': [Record<string,string>]; 'update:files': [string]; refresh: [] }>();
const { t } = useI18n(), id = useId();
const tr = (key: string, values: Record<string,string|number> = {}) => t(`agentWorkflow.${key}`, values);
const filesOpen = ref(false), search = ref('');
const selectedFiles = computed(() => workflowSelectedFiles(props.files));
const availableFiles = computed(() => (props.catalog?.files || []).filter(file => `${file.path} ${file.name}`.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase())));
const options = (field: AgentWorkflowInput) => workflowInputOptions(field, tr('inputYes'), tr('inputNo'));
function setValue(key: string, value: string) { emit('update:modelValue', { ...props.modelValue, [key]: value }); }
function selectFile(path: string, selected: boolean) {
  if (selected && !selectedFiles.value.includes(path) && selectedFiles.value.length >= 16) return;
  const next = selectedFiles.value.filter(item => item !== path); if (selected) next.push(path);
  emit('update:files', next.join('\n'));
}
function size(value: number) { return value < 1024 ? `${value} B` : value < 1024*1024 ? `${Math.round(value/1024)} KB` : `${(value/1024/1024).toFixed(1)} MB`; }
</script>

<template>
  <div class="aw-run-inputs" data-testid="workflow-run-inputs">
    <template v-for="field in fields" :key="field.key">
      <fieldset v-if="field.type === 'boolean'" class="aw-run-boolean" :disabled="disabled" :data-input-key="field.key">
        <legend>{{ field.label || field.key }} <small v-if="field.required">{{ tr('required') }}</small></legend>
        <div class="aw-run-choices"><label v-for="option in options(field)" :key="option.value" :class="{ selected: modelValue[field.key] === option.value }"><input type="radio" :name="`${id}-${field.key}`" :value="option.value" :checked="modelValue[field.key] === option.value" :required="field.required" @change="setValue(field.key, option.value)" /><span>{{ option.label || option.value }}</span></label></div>
      </fieldset>
      <label v-else class="aw-run-field" :data-input-key="field.key"><span>{{ field.label || field.key }} <small v-if="field.required">{{ tr('required') }}</small></span>
        <select v-if="field.type === 'select'" :value="modelValue[field.key] || ''" :required="field.required" :disabled="disabled" @change="setValue(field.key, ($event.target as HTMLSelectElement).value)"><option value="">{{ tr('inputChoose') }}</option><option v-for="option in options(field)" :key="option.value" :value="option.value">{{ option.label || option.value }}</option></select>
        <textarea v-else-if="field.type === 'object' || field.type === 'array'" :value="modelValue[field.key] || ''" :required="field.required" :disabled="disabled" :placeholder="field.type === 'array' ? '[]' : '{}'" rows="5" spellcheck="false" data-testid="workflow-run-json-input" @input="setValue(field.key, ($event.target as HTMLTextAreaElement).value)" />
        <input v-else-if="field.type === 'number' || field.type === 'integer'" type="number" :step="field.type === 'integer' ? 1 : 'any'" :value="modelValue[field.key] || ''" :required="field.required" :disabled="disabled" data-testid="workflow-run-number-input" @input="setValue(field.key, ($event.target as HTMLInputElement).value)" />
        <input v-else type="text" :value="modelValue[field.key] || ''" :required="field.required" :disabled="disabled" maxlength="16000" @input="setValue(field.key, ($event.target as HTMLInputElement).value)" />
      </label>
    </template>
    <section class="aw-run-files" :aria-label="tr('files')">
      <div class="aw-run-files-head"><strong>{{ tr('files') }}<small v-if="selectedFiles.length">{{ tr('inputFilesSelected', { n: selectedFiles.length }) }}</small></strong><button type="button" :disabled="disabled" :aria-expanded="filesOpen" @click="filesOpen = !filesOpen">{{ tr(filesOpen ? 'inputCloseFiles' : 'inputSelectFiles') }}</button></div>
      <ul v-if="selectedFiles.length" class="aw-run-selected"><li v-for="path in selectedFiles" :key="path"><span :title="path">{{ path }}</span><button type="button" :disabled="disabled" :aria-label="tr('inputRemoveFile', { name:path })" @click="selectFile(path,false)">×</button></li></ul>
      <p v-else class="aw-run-hint">{{ tr('inputFilesHint') }}</p>
      <div v-if="filesOpen" class="aw-run-picker" :aria-busy="loading">
        <div class="aw-run-file-search"><input v-model="search" type="search" :placeholder="tr('inputSearchFiles')" :aria-label="tr('inputSearchFiles')" /><button type="button" :disabled="loading || disabled" @click="emit('refresh')">{{ tr('inputRefreshFiles') }}</button></div>
        <p v-if="loading" class="aw-run-hint" role="status">{{ tr('inputLoadingFiles') }}</p><p v-else-if="error" class="aw-run-file-error" role="alert">{{ tr('inputFilesUnavailable') }}</p>
        <ul v-else-if="availableFiles.length" class="aw-run-file-list"><li v-for="file in availableFiles" :key="file.path"><label><input type="checkbox" :checked="selectedFiles.includes(file.path)" :disabled="disabled || selectedFiles.length >= 16 && !selectedFiles.includes(file.path)" @change="selectFile(file.path,($event.target as HTMLInputElement).checked)" /><span :title="file.path">{{ file.path }}</span><small>{{ size(file.size) }}</small></label></li></ul><p v-else class="aw-run-hint">{{ tr(search ? 'inputNoFilesMatch' : 'inputFilesEmpty') }}</p>
        <p v-if="catalog?.files_truncated" class="aw-run-hint">{{ tr('inputFilesLimited',{ n:catalog.file_limit || 500 }) }}</p>
      </div>
      <details class="aw-run-manual"><summary>{{ tr('inputManualFiles') }}</summary><textarea :value="files" :disabled="disabled" rows="3" :aria-label="tr('inputManualFiles')" @input="emit('update:files',($event.target as HTMLTextAreaElement).value)" /><small>{{ tr('filesHint') }}</small></details>
    </section>
  </div>
</template>

<style scoped>
.aw-run-inputs{display:flex;flex-direction:column;gap:18px;min-width:0}.aw-run-field{display:flex;flex-direction:column;gap:8px;font-size:13px}.aw-run-field small,.aw-run-boolean legend small{font-weight:400;font-size:11px;color:var(--el-text-color-secondary);margin-left:5px}.aw-run-inputs input[type=number],.aw-run-inputs input[type=text],.aw-run-inputs input[type=search],.aw-run-inputs select,.aw-run-inputs textarea{width:100%;box-sizing:border-box;border:1px solid var(--el-border-color);border-radius:8px;background:var(--el-bg-color);color:var(--el-text-color-primary);padding:9px 10px;font:inherit;min-width:0}.aw-run-inputs input:focus-visible,.aw-run-inputs select:focus-visible,.aw-run-inputs textarea:focus-visible{outline:2px solid var(--el-color-primary-light-5);outline-offset:1px}.aw-run-inputs button{border:1px solid var(--el-border-color);border-radius:7px;padding:6px 9px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:11px;cursor:pointer}.aw-run-inputs button:disabled{opacity:.5;cursor:default}.aw-run-boolean{border:0;margin:0;padding:0;min-width:0}.aw-run-boolean legend{font-size:13px;margin-bottom:8px}.aw-run-choices{display:flex;gap:8px;flex-wrap:wrap}.aw-run-choices label{display:flex;align-items:center;gap:7px;min-width:70px;padding:8px 13px;border:1px solid var(--el-border-color);border-radius:8px;cursor:pointer;font-size:13px}.aw-run-choices label.selected{border-color:var(--el-color-primary);background:var(--el-color-primary-light-9);color:var(--el-color-primary)}.aw-run-choices input{accent-color:var(--el-color-primary)}.aw-run-files{padding-top:14px;border-top:1px solid var(--el-border-color-lighter);min-width:0}.aw-run-files-head{display:flex;align-items:center;justify-content:space-between;gap:10px}.aw-run-files-head strong{font-size:13px;font-weight:550}.aw-run-files-head strong small{font-size:10px;font-weight:400;color:var(--el-text-color-secondary);margin-left:8px}.aw-run-hint,.aw-run-manual small{font-size:11px;line-height:1.6;color:var(--el-text-color-secondary);margin:9px 0 0}.aw-run-selected{list-style:none;padding:0;margin:10px 0;display:flex;flex-direction:column;gap:5px}.aw-run-selected li{display:flex;align-items:center;gap:7px;padding:5px 7px;border:1px solid var(--el-border-color-extra-light);border-radius:7px;background:var(--el-fill-color-extra-light)}.aw-run-selected span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px}.aw-run-selected button{padding:0 4px;border:0;background:none;font-size:16px}.aw-run-picker{border:1px solid var(--el-border-color-light);border-radius:9px;padding:9px;margin-top:9px}.aw-run-file-search{display:flex;align-items:center;gap:7px}.aw-run-file-search button{flex:none;white-space:nowrap}.aw-run-file-search input{font-size:12px!important;padding:7px 8px!important}.aw-run-file-list{list-style:none;padding:0;margin:8px 0 0;max-height:180px;overflow:auto;overscroll-behavior:contain;scrollbar-width:thin}.aw-run-file-list label{display:flex;align-items:center;gap:7px;padding:7px 2px;cursor:pointer;font-size:11px}.aw-run-file-list label span{flex:1;min-width:0;overflow-wrap:anywhere}.aw-run-file-list input{accent-color:var(--el-color-primary);flex:none}.aw-run-file-list small{flex:none;color:var(--el-text-color-secondary);font-size:10px}.aw-run-file-error{font-size:11px;color:var(--el-color-danger)}.aw-run-manual{margin-top:11px;font-size:11px;color:var(--el-text-color-secondary)}.aw-run-manual summary{cursor:pointer;padding:4px 0}.aw-run-manual textarea{margin-top:7px;resize:vertical;font-size:11px!important}.aw-run-manual small{display:block}
</style>
