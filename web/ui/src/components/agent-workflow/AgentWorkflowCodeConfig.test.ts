import {describe,expect,it,vi} from 'vitest';
import {effectScope,nextTick,reactive,type Ref} from 'vue';
import {defaultWorkflowCode} from '@/agent/agentWorkflowCode';
import type {AgentWorkflowCodeSpec} from '@/api/agentWorkflows';
import AgentWorkflowCodeConfig from './AgentWorkflowCodeConfig.vue';
vi.mock('vue',async original=>({...await original<typeof import('vue')>(),useSSRContext:()=>({modules:new Set()})}));
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}));
interface Controller{advanced:Ref<string>;language(value:'python'|'javascript'):void;applyAdvanced():void;inputMode(key:string,value:boolean):void; literal(key:string,value:string):void; literalText(key:string,binding:AgentWorkflowCodeSpec['inputs'][string]):string; update(change:(spec:AgentWorkflowCodeSpec)=>void):void; advancedDirty:Ref<boolean>}
function mount(disabled=false){const props=reactive({modelValue:defaultWorkflowCode(),disabled});const scope=effectScope();const emit=vi.fn((name:string,value:AgentWorkflowCodeSpec)=>{if(name==='update:modelValue')props.modelValue=value;});const c=scope.run(()=>(AgentWorkflowCodeConfig as unknown as {setup(props:unknown,context:unknown):Controller}).setup(props,{emit,expose(){}}))!;return{c,props,emit,scope};}
describe('code configuration safety',()=>{
 it('keeps incomplete negative and JSON literals across unrelated source edits until the user completes them',async()=>{
  const{c,props,emit,scope}=mount();
  props.modelValue.inputs={amount:{type:'number',value:0},config:{type:'object',value:{existing:true}}};
  c.literal('amount','-');c.literal('config','{"tax":');await nextTick();
  expect(c.literalText('amount',props.modelValue.inputs.amount!)).toBe('-');expect(c.literalText('config',props.modelValue.inputs.config!)).toBe('{"tax":');
  expect(props.modelValue.inputs.amount!.value).toBe(0);expect(props.modelValue.inputs.config!.value).toEqual({existing:true});expect(emit).toHaveBeenCalledWith('validity',false);
  c.update(next=>next.source+='\n# edit while fixing input');await nextTick();
  expect(c.literalText('amount',props.modelValue.inputs.amount!)).toBe('-');expect(c.literalText('config',props.modelValue.inputs.config!)).toBe('{"tax":');
  c.literal('amount','-12.5');c.literal('config','{"tax":0.2}');await nextTick();
  expect(props.modelValue.inputs.amount!.value).toBe(-12.5);expect(props.modelValue.inputs.config!.value).toEqual({tax:0.2});expect(emit).toHaveBeenLastCalledWith('validity',true);scope.stop();
 });
 it('preserves an unapplied schema draft when source editing clones the complete code specification',async()=>{
  const{c,props,emit,scope}=mount();const schema='{"rows":{"type":"array","items":{"type":"number"}}}';
  c.advanced.value=schema;await nextTick();expect(c.advancedDirty.value).toBe(true);expect(emit).toHaveBeenCalledWith('validity',false);
  c.update(next=>next.source+='\n# unrelated source edit');await nextTick();
  expect(c.advanced.value).toBe(schema);expect(props.modelValue.outputs).toEqual({result:{type:'string'}});
  c.applyAdvanced();await nextTick();expect(props.modelValue.outputs).toEqual({rows:{type:'array',items:{type:'number'}}});expect(c.advancedDirty.value).toBe(false);expect(emit).toHaveBeenLastCalledWith('validity',true);scope.stop();
 });

 it('changes only untouched starter code on language switch and preserves user-authored code',()=>{const{c,props,scope}=mount();c.language('javascript');expect(props.modelValue.source).toContain('async function main');props.modelValue.source='// user code';c.language('python');expect(props.modelValue.source).toBe('// user code');scope.stop();});
 it('rejects malformed advanced output schemas without replacing saved fields',()=>{const{c,props,emit,scope}=mount();c.advanced.value='{"rows":{"type":"array"}}';c.applyAdvanced();expect(props.modelValue.outputs).toEqual({result:{type:'string'}});expect(emit).toHaveBeenCalledWith('validity',false);c.advanced.value='{"rows":{"type":"array","items":{"type":"number"}}}';c.applyAdvanced();expect(props.modelValue.outputs).toEqual({rows:{type:'array',items:{type:'number'}}});expect(emit).toHaveBeenCalledWith('validity',true);scope.stop();});
 it('keeps historical code and bindings immutable',()=>{const{c,props,emit,scope}=mount(true);const before=JSON.stringify(props.modelValue);c.language('javascript');c.inputMode('value',true);expect(JSON.stringify(props.modelValue)).toBe(before);expect(emit.mock.calls.filter(row=>row[0]==='update:modelValue')).toEqual([]);scope.stop();});
});
