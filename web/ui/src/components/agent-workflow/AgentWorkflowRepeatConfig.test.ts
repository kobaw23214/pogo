import {describe,expect,it,vi} from 'vitest';
import {effectScope,nextTick,reactive,type Ref} from 'vue';
import {defaultWorkflowRepeat} from '@/agent/agentWorkflowRepeat';
import type {AgentWorkflowStep} from '@/api/agentWorkflows';
import AgentWorkflowRepeatConfig from './AgentWorkflowRepeatConfig.vue';
vi.mock('vue',async original=>({...await original<typeof import('vue')>(),useSSRContext:()=>({modules:new Set()})}));
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}));
interface Controller{raw:Ref<string>;literal(value:string):void;bodyKind(value:'agent'|'code'):void;setCode(value:NonNullable<AgentWorkflowStep['code']>):void;resource(field:'skills'|'shortcuts',id:string,checked:boolean):void;bodyVariables:Ref<{value:string}[]>;}
function mount(disabled=false){const props=reactive({modelValue:{id:'batch',kind:'iteration',...defaultWorkflowRepeat('iteration')} as AgentWorkflowStep,disabled});const scope=effectScope();const emit=vi.fn((name:string,value:AgentWorkflowStep)=>{if(name==='update:modelValue')props.modelValue=value;});const c=scope.run(()=>(AgentWorkflowRepeatConfig as unknown as {setup(props:unknown,context:unknown):Controller}).setup(props,{emit,expose(){}}))!;return{scope,props,c,emit};}
describe('repeat configuration interaction',()=>{
 it('preserves incomplete collection JSON while code changes, then emits the corrected typed collection',async()=>{
  const{scope,props,c,emit}=mount();c.literal('[{"id":');await nextTick();expect(c.raw.value).toBe('[{"id":');expect(props.modelValue.iteration!.items.value).toEqual([]);expect(emit).toHaveBeenLastCalledWith('validity',false);
  c.setCode({...props.modelValue.iteration!.body.code!,source:'def main(inputs):\n return {"result":inputs["item"]}'});await nextTick();expect(c.raw.value).toBe('[{"id":');
  c.literal('[{"id":1},{"id":2}]');await nextTick();expect(props.modelValue.iteration!.items.value).toEqual([{id:1},{id:2}]);expect(emit).toHaveBeenLastCalledWith('validity',true);scope.stop();
 });
 it('switches only the item execution body and keeps collection policy and stable parent identity',()=>{const{scope,props,c}=mount();c.bodyKind('agent');expect(props.modelValue.id).toBe('batch');expect(props.modelValue.iteration!.body.kind).toBe('agent');expect(props.modelValue.iteration!.body.code).toBeUndefined();expect(props.modelValue.iteration!.concurrency).toBe(1);expect(c.bodyVariables.value.map(v=>v.value)).toContain('{{ iteration.item }}');c.resource('skills','skill:known',true);expect(props.modelValue.iteration!.body.skills).toEqual(['skill:known']);scope.stop();});
 it('keeps historical data, code and resource bindings immutable',()=>{const{scope,props,c,emit}=mount(true);const before=JSON.stringify(props.modelValue);c.literal('[1]');c.bodyKind('agent');c.resource('shortcuts','shortcut:new',true);expect(JSON.stringify(props.modelValue)).toBe(before);expect(emit.mock.calls.filter(row=>row[0]==='update:modelValue')).toEqual([]);scope.stop();});
});
