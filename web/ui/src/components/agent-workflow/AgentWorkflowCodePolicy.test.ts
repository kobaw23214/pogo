import {describe,expect,it,vi} from 'vitest';
import {effectScope,nextTick,reactive,type Ref} from 'vue';
import type {AgentWorkflowCodePolicy} from '@/api/agentWorkflows';
import AgentWorkflowCodePolicyConfig from './AgentWorkflowCodePolicy.vue';
vi.mock('vue',async original=>({...await original<typeof import('vue')>(),useSSRContext:()=>({modules:new Set()})}));
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}));
interface Controller{fallbackText:Ref<string>;fallback(value:string):void;failure(value:string):void;}
function mount(disabled=false){const props=reactive({modelValue:{max_retries:1,on_failure:'fallback',fallback:{total:0}} as AgentWorkflowCodePolicy|undefined,outputs:{total:{type:'number' as const}},disabled});const scope=effectScope();const emit=vi.fn((name:string,value:AgentWorkflowCodePolicy|undefined)=>{if(name==='update:modelValue')props.modelValue=value;});const c=scope.run(()=>(AgentWorkflowCodePolicyConfig as unknown as {setup(props:unknown,context:unknown):Controller}).setup(props,{emit,expose(){}}))!;return{scope,props,c,emit};}
describe('explicit code failure policy',()=>{
 it('retains invalid fallback text without overwriting the last valid output contract',async()=>{const{scope,props,c,emit}=mount();c.fallback('{"total":');await nextTick();expect(c.fallbackText.value).toBe('{"total":');expect(props.modelValue!.fallback).toEqual({total:0});expect(emit).toHaveBeenLastCalledWith('validity',false);c.fallback('{"total":7}');await nextTick();expect(props.modelValue!.fallback).toEqual({total:7});expect(emit).toHaveBeenLastCalledWith('validity',true);scope.stop();});
 it('removes fallback data when choosing stop and cannot change a frozen policy',()=>{const{scope,props,c}=mount();c.failure('stop');expect(props.modelValue!.on_failure).toBe('stop');expect(props.modelValue!.fallback).toBeUndefined();scope.stop();const frozen=mount(true),before=JSON.stringify(frozen.props.modelValue);frozen.c.failure('stop');frozen.c.fallback('{"total":8}');expect(JSON.stringify(frozen.props.modelValue)).toBe(before);frozen.scope.stop();});
});
