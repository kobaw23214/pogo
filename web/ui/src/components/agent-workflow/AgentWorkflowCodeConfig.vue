<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import type { AgentWorkflowCodeBinding, AgentWorkflowCodePolicy, AgentWorkflowCodeSpec, AgentWorkflowValueSchema, AgentWorkflowValueType } from '@/api/agentWorkflows';
import { defaultWorkflowCode, workflowSchemaValid, workflowTypeDefault, workflowValueMatches, workflowValueTypes } from '@/agent/agentWorkflowCode';
import AgentWorkflowCodePolicyConfig from './AgentWorkflowCodePolicy.vue';
import AgentWorkflowCodeEditor from './AgentWorkflowCodeEditor.vue';
const policyValid=ref(true);
const props = defineProps<{ modelValue: AgentWorkflowCodeSpec; disabled?: boolean; policy?: AgentWorkflowCodePolicy; variables?: { value:string; label:string }[] }>();
const emit = defineEmits<{ 'update:modelValue': [AgentWorkflowCodeSpec]; 'update:policy': [AgentWorkflowCodePolicy|undefined]; validity: [boolean] }>();
const { t } = useI18n();
const tr = (key:string) => t(`agentWorkflowCanvas.${key}`);
const errors = ref<Record<string, boolean>>({}), advanced = ref('');
const literalDrafts = ref<Record<string, string>>({});
const advancedDirty = computed(() => advanced.value !== JSON.stringify(props.modelValue.outputs || {}, null, 2));
const hasErrors = computed(() => Object.values(errors.value).some(Boolean));
function notifyValidity() { emit('validity', !hasErrors.value && !advancedDirty.value && policyValid.value); }

const inputRows = computed(() => Object.entries(props.modelValue.inputs || {}));
const outputRows = computed(() => Object.entries(props.modelValue.outputs || {}));
const references = computed(() => (props.variables || []).map(item => ({ label:item.label,value:item.value.replace(/^\{\{\s*|\s*\}\}$/g, '') })));
// Parent edits clone the entire CodeSpec, including an unchanged outputs map.
// Watch semantic content so editing source cannot replace a pending schema edit.
watch(() => JSON.stringify(props.modelValue.outputs || {}), (value, previous) => {
  const previousText = previous === undefined ? undefined : JSON.stringify(JSON.parse(previous), null, 2);
  if (previousText === undefined || advanced.value === previousText) advanced.value = JSON.stringify(JSON.parse(value), null, 2);
}, { immediate:true });
watch(policyValid, notifyValidity);
watch(advancedDirty, notifyValidity, { immediate:true, flush:'sync' });
function invalid(key:string, value:boolean) { errors.value = {...errors.value,[key]:value}; notifyValidity(); }
function clearLiteral(key:string) { const next = {...literalDrafts.value}; delete next[key]; literalDrafts.value = next; }
function literalText(key:string, binding:AgentWorkflowCodeBinding):string {
  if (Object.prototype.hasOwnProperty.call(literalDrafts.value, key)) return literalDrafts.value[key]!;
  return binding.type === 'object' || binding.type === 'array' ? JSON.stringify(binding.value, null, 2) : String(binding.value ?? '');
}

function update(change:(next:AgentWorkflowCodeSpec)=>void) { if(props.disabled) return; const next:AgentWorkflowCodeSpec = JSON.parse(JSON.stringify(props.modelValue)); change(next); emit('update:modelValue',next); }
function language(value:AgentWorkflowCodeSpec['language']) { update(next => { if(next.source === defaultWorkflowCode(next.language).source) next.source = defaultWorkflowCode(value).source; next.language = value; }); }
function name(section:'inputs'|'outputs', old:string,value:string) { if(!value || value===old) return; if(Object.prototype.hasOwnProperty.call(props.modelValue[section],value)) {invalid(`name:${old}`,true);return;} invalid(`name:${old}`,false);
  if (section === 'inputs' && Object.prototype.hasOwnProperty.call(literalDrafts.value, old)) {
    const next = {...literalDrafts.value, [value]:literalDrafts.value[old]!}; delete next[old]; literalDrafts.value = next;
    const nextErrors = {...errors.value, [value]:errors.value[old] || false}; delete nextErrors[old]; errors.value = nextErrors;
  }
  update(next => { Object.assign(next,{[section]:Object.fromEntries(Object.entries(next[section]).map(([k,v])=>[k===old?value:k,v]))}); }); }
function input(key:string, change:(binding:AgentWorkflowCodeBinding)=>void) {update(next => change(next.inputs[key]!));}
function inputType(key:string,type:AgentWorkflowValueType) { clearLiteral(key); input(key,binding => {binding.type=type;if(Object.prototype.hasOwnProperty.call(binding,'value')) binding.value=workflowTypeDefault(type);});invalid(key,false); }
function inputMode(key:string,source:boolean) {clearLiteral(key);input(key,binding => {if(source){delete binding.value;binding.source='';}else{delete binding.source;binding.value=workflowTypeDefault(binding.type);}});invalid(key,false);}
function literal(key:string, value:string) {
  if (props.disabled) return;
  literalDrafts.value = {...literalDrafts.value, [key]:value};
  const binding = props.modelValue.inputs[key]!;
  try {
    const parsed:unknown = binding.type === 'string' ? value : JSON.parse(value);
    if (!workflowValueMatches(binding.type, parsed)) throw new Error('Input type mismatch');
    input(key, row => row.value = parsed); invalid(key, false);
  } catch { invalid(key, true); }
}

function add(section:'inputs'|'outputs') {update(next => {let n=1;while(Object.prototype.hasOwnProperty.call(next[section],`field_${n}`))n++;if(section==='inputs')next.inputs[`field_${n}`]={type:'string',value:''};else next.outputs[`field_${n}`]={type:'string'};});}
function remove(section:'inputs'|'outputs',key:string) {if(section==='inputs')clearLiteral(key);update(next=>delete next[section][key]);invalid(key,false);invalid(`name:${key}`,false);}
function outputType(key:string,type:AgentWorkflowValueType) {update(next=>{next.outputs[key]={type,...(type==='array'?{items:{type:'string' as const}}:{}),...(type==='object'?{properties:{}}:{})};});}
function applyAdvanced() {try {const value:unknown=JSON.parse(advanced.value);if(!value||typeof value!=='object'||Array.isArray(value)||!Object.keys(value).length||Object.values(value).some(schema=>!workflowSchemaValid(schema as AgentWorkflowValueSchema)))throw Error();update(next=>next.outputs=value as Record<string,AgentWorkflowValueSchema>);advanced.value=JSON.stringify(value,null,2);invalid('outputs',false);}catch{invalid('outputs',true);}}
</script>
<template>
  <section class="aw-code-config" data-testid="workflow-code-config">
    <div class="aw-code-settings"><label>{{tr('codeLanguage')}}<select :value="modelValue.language" :disabled="disabled" data-testid="workflow-code-language" @change="language(($event.target as HTMLSelectElement).value as AgentWorkflowCodeSpec['language'])"><option value="python">Python</option><option value="javascript">JavaScript</option></select></label><label>{{tr('codeTimeout')}}<input type="number" min="1" max="60" :value="modelValue.timeout_seconds ?? 10" :disabled="disabled" @change="update(next=>next.timeout_seconds=Number(($event.target as HTMLInputElement).value))" /></label></div>
    <p class="aw-code-hint">{{tr('codeHint')}}</p>
    <div class="aw-code-source-field"><span>{{tr('codeSource')}}</span><AgentWorkflowCodeEditor :model-value="modelValue.source" :language="modelValue.language" :inputs="modelValue.inputs" :disabled="disabled" :label="tr('codeSource')" @update:model-value="update(next=>next.source=$event)" /></div>
    <section class="aw-code-fields"><header><strong>{{tr('codeInputs')}}</strong><button v-if="!disabled" type="button" @click="add('inputs')">＋ {{tr('codeAddField')}}</button></header>
      <article v-for="[key,binding] in inputRows" :key="key" class="aw-code-field"><div class="aw-code-row"><input :value="key" :disabled="disabled" :aria-label="tr('codeFieldName')" @change="name('inputs',key,($event.target as HTMLInputElement).value)" /><select :value="binding.type" :disabled="disabled" :aria-label="tr('codeFieldType')" @change="inputType(key,($event.target as HTMLSelectElement).value as AgentWorkflowValueType)"><option v-for="type in workflowValueTypes" :key="type" :value="type">{{type}}</option></select><button v-if="!disabled" type="button" :aria-label="tr('codeRemoveField')" @click="remove('inputs',key)">×</button></div>
        <label>{{tr('codeBinding')}}<select :value="Object.prototype.hasOwnProperty.call(binding,'value')?'value':'source'" :disabled="disabled" @change="inputMode(key,($event.target as HTMLSelectElement).value==='source')"><option value="source">{{tr('codeUpstream')}}</option><option value="value">{{tr('codeFixedValue')}}</option></select></label>
        <template v-if="!Object.prototype.hasOwnProperty.call(binding,'value')"><select v-if="references.length" :value="references.some(item=>item.value===binding.source)?binding.source:''" :disabled="disabled" :aria-label="tr('codeChooseSource')" @change="input(key,row=>row.source=($event.target as HTMLSelectElement).value)"><option value="">{{tr('codeChooseSource')}}</option><option v-for="item in references" :key="item.value" :value="item.value">{{item.label}} · {{item.value}}</option></select><input :value="binding.source || ''" :disabled="disabled" :aria-label="tr('codeReference')" placeholder="inputs.value / steps.calculate.output.total" @input="input(key,row=>row.source=($event.target as HTMLInputElement).value)" /></template>
        <select v-else-if="binding.type==='boolean'" :value="String(binding.value)" :disabled="disabled" :aria-label="tr('codeFixedValue')" @change="literal(key,($event.target as HTMLSelectElement).value)"><option value="true">true</option><option value="false">false</option></select>
        <textarea v-else-if="binding.type==='object'||binding.type==='array'" :value="literalText(key,binding)" :disabled="disabled" rows="3" spellcheck="false" :aria-label="tr('codeFixedValue')" @input="literal(key,($event.target as HTMLTextAreaElement).value)" />
        <input v-else type="text" :inputmode="binding.type==='string'?'text':'decimal'" :value="literalText(key,binding)" :disabled="disabled" :aria-label="tr('codeFixedValue')" @input="literal(key,($event.target as HTMLInputElement).value)" />
      </article><p v-if="!inputRows.length" class="aw-code-hint">{{tr('codeNoInputs')}}</p>
    </section>
    <section class="aw-code-fields"><header><strong>{{tr('codeOutputs')}}</strong><button v-if="!disabled" type="button" @click="add('outputs')">＋ {{tr('codeAddField')}}</button></header><p class="aw-code-hint">{{tr('codeOutputsHint')}}</p>
      <article v-for="[key,schema] in outputRows" :key="key" class="aw-code-row"><input :value="key" :disabled="disabled" :aria-label="tr('codeFieldName')" @change="name('outputs',key,($event.target as HTMLInputElement).value)" /><select :value="schema.type" :disabled="disabled" :aria-label="tr('codeFieldType')" @change="outputType(key,($event.target as HTMLSelectElement).value as AgentWorkflowValueType)"><option v-for="type in workflowValueTypes" :key="type" :value="type">{{type}}</option></select><button v-if="!disabled" type="button" :aria-label="tr('codeRemoveField')" @click="remove('outputs',key)">×</button></article>
      <details><summary>{{tr('codeAdvancedOutputs')}}</summary><textarea v-model="advanced" :disabled="disabled" rows="7" spellcheck="false" :aria-label="tr('codeAdvancedOutputs')" /><span v-if="advancedDirty && !disabled" class="aw-code-pending" role="status">{{tr('unsaved')}}</span><button v-if="!disabled" type="button" :disabled="!advancedDirty && !errors.outputs" @click="applyAdvanced">{{tr('codeApplySchema')}}</button></details>
    </section>
    <AgentWorkflowCodePolicyConfig :model-value="policy" :outputs="modelValue.outputs" :disabled="disabled" @update:model-value="emit('update:policy',$event)" @validity="policyValid=$event" />
    <p v-if="hasErrors" class="aw-code-error" role="alert">{{tr('codeInvalidJSON')}}</p>
  </section>
</template>
<style scoped>
.aw-code-source-field{display:flex;flex-direction:column;gap:6px;min-width:0;font-size:12px}
.aw-code-config{display:flex;flex-direction:column;gap:12px;min-width:0}.aw-code-config label{display:flex;flex-direction:column;gap:6px;font-size:12px}.aw-code-config input,.aw-code-config select,.aw-code-config textarea{width:100%;min-width:0;box-sizing:border-box;padding:8px;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;font-size:12px}.aw-code-config textarea{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;line-height:1.65;resize:vertical;tab-size:4}.aw-code-settings{display:grid;grid-template-columns:1fr 110px;gap:10px}.aw-code-hint{margin:0;font-size:11px;line-height:1.65;color:var(--el-text-color-secondary)}.aw-code-fields{display:flex;flex-direction:column;gap:8px;padding-top:10px;border-top:1px solid var(--el-border-color-lighter)}.aw-code-fields header{display:flex;align-items:center;justify-content:space-between;gap:8px;font-size:12px}.aw-code-config button{padding:5px 7px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:11px;cursor:pointer}.aw-code-config button:disabled{opacity:.5;cursor:default}.aw-code-field{display:flex;flex-direction:column;gap:7px;padding:9px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-fill-color-extra-light)}.aw-code-row{display:grid;grid-template-columns:minmax(0,1fr) 105px 25px;gap:6px;align-items:center}.aw-code-row button{padding:5px 2px;font-size:16px}.aw-code-config details{font-size:11px;color:var(--el-text-color-secondary)}.aw-code-config summary{cursor:pointer;padding:6px 0}.aw-code-config details textarea{margin:6px 0}.aw-code-pending{display:block;margin:4px 0 7px;color:var(--el-color-warning)}.aw-code-error{font-size:11px;color:var(--el-color-danger)}
</style>
