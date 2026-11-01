<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import type { AgentWorkflowCodePolicy, AgentWorkflowValueSchema } from '@/api/agentWorkflows';
import { workflowPolicyValid } from '@/agent/agentWorkflowRepeat';
const props=defineProps<{modelValue?:AgentWorkflowCodePolicy;outputs:Record<string,AgentWorkflowValueSchema>;disabled?:boolean}>();
const emit=defineEmits<{'update:modelValue':[AgentWorkflowCodePolicy|undefined];validity:[boolean]}>();
const {t}=useI18n();const tr=(key:string)=>t(`agentWorkflowCanvas.${key}`);
const fallbackText=ref(''),invalid=ref(false);
const current=computed<AgentWorkflowCodePolicy>(()=>props.modelValue||{max_retries:0,on_failure:'stop'});
watch(()=>JSON.stringify(props.modelValue?.fallback),value=>{fallbackText.value=value===undefined?'{}':JSON.stringify(JSON.parse(value),null,2);invalid.value=false;});
watch(()=>[JSON.stringify(props.modelValue),JSON.stringify(props.outputs),invalid.value],()=>emit('validity',!invalid.value&&(!props.modelValue||workflowPolicyValid(props.modelValue,props.outputs))),{immediate:true});
function update(change:(next:AgentWorkflowCodePolicy)=>void){if(props.disabled)return;const next=JSON.parse(JSON.stringify(current.value));change(next);emit('update:modelValue',next);}
function failure(value:string){update(next=>{next.on_failure=value as 'stop'|'fallback';if(value==='stop'){delete next.fallback;invalid.value=false;}else next.fallback=Object.fromEntries(Object.entries(props.outputs).map(([key,schema])=>[key,schema.type==='string'?'':schema.type==='boolean'?false:schema.type==='array'?[]:schema.type==='object'?{}:0]));});}
function fallback(value:string){if(props.disabled)return;fallbackText.value=value;try{const parsed:unknown=JSON.parse(value);const next={...current.value,fallback:parsed as Record<string,unknown>};if(!workflowPolicyValid(next,props.outputs))throw Error();invalid.value=false;emit('update:modelValue',next);}catch{invalid.value=true;}}
</script>
<template>
  <details class="aw-code-policy" :open="!!modelValue" data-testid="workflow-code-policy"><summary>{{tr('codePolicy')}}</summary><p>{{tr('codePolicyHint')}}</p>
    <div class="aw-policy-row"><label>{{tr('codeRetries')}}<input type="number" min="0" max="2" :value="current.max_retries||0" :disabled="disabled" data-testid="workflow-code-retries" @change="update(next=>next.max_retries=Number(($event.target as HTMLInputElement).value))" /></label><label>{{tr('codeRetryDelay')}}<input type="number" min="0" max="5000" step="100" :value="current.retry_delay_ms||0" :disabled="disabled" @change="update(next=>next.retry_delay_ms=Number(($event.target as HTMLInputElement).value))" /></label></div>
    <label>{{tr('codeOnFailure')}}<select :value="current.on_failure||'stop'" :disabled="disabled" @change="failure(($event.target as HTMLSelectElement).value)"><option value="stop">{{tr('codeFailureStop')}}</option><option value="fallback">{{tr('codeFailureFallback')}}</option></select></label>
    <label v-if="current.on_failure==='fallback'">{{tr('codeFallbackValue')}}<textarea :value="fallbackText||JSON.stringify(modelValue?.fallback||{},null,2)" :disabled="disabled" rows="5" spellcheck="false" data-testid="workflow-code-fallback" @input="fallback(($event.target as HTMLTextAreaElement).value)" /></label>
    <p v-if="invalid" class="aw-policy-error" role="alert">{{tr('codeFallbackInvalid')}}</p><button v-if="modelValue&&!disabled" type="button" @click="emit('update:modelValue',undefined)">{{tr('codePolicyReset')}}</button>
  </details>
</template>
<style scoped>
.aw-code-policy{border-top:1px solid var(--el-border-color-lighter);padding-top:10px;font-size:12px}.aw-code-policy summary{cursor:pointer;font-weight:600}.aw-code-policy p{font-size:11px;line-height:1.65;color:var(--el-text-color-secondary);margin:8px 0}.aw-code-policy label{display:flex;flex-direction:column;gap:5px;margin:8px 0}.aw-policy-row{display:grid;grid-template-columns:1fr 1fr;gap:9px}.aw-code-policy input,.aw-code-policy select,.aw-code-policy textarea{width:100%;min-width:0;box-sizing:border-box;padding:7px;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;font-size:12px}.aw-code-policy textarea{font:11px/1.65 ui-monospace,monospace;resize:vertical}.aw-code-policy button{font:inherit;font-size:11px;padding:5px 7px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-secondary);cursor:pointer}.aw-code-policy p.aw-policy-error{color:var(--el-color-danger)}
</style>
