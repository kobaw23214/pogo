import { describe, expect, it, vi } from 'vitest';
import { createSSRApp, effectScope, reactive } from 'vue';
import { renderToString } from '@vue/server-renderer';
import AgentWorkflowRunInputs from './AgentWorkflowRunInputs.vue';
vi.mock('vue',async original=>({...await original<typeof import('vue')>(),useId:()=> 'run-input-test',useSSRContext:()=>({modules:new Set()})}));
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}));
describe('workflow run form controls',()=>{
  it('renders numeric and structured inputs while preserving the legacy string transport',async()=>{
    const html=await renderToString(createSSRApp(AgentWorkflowRunInputs,{fields:[{key:'amount',type:'number'},{key:'count',type:'integer'},{key:'orders',type:'array'},{key:'options',type:'object'}],modelValue:{amount:'1.5',count:'2',orders:'[{"amount":3}]',options:'{"tax":0.2}'},files:''}));
    expect(html.match(/type="number"/g)).toHaveLength(2);expect(html).toContain('step="any"');expect(html).toContain('step="1"');expect(html.match(/workflow-run-json-input/g)).toHaveLength(2);expect(html).toContain('&quot;amount&quot;:3');
  });
  it('renders text, boolean and select controls from schema without changing stored choice values',async()=>{
    const html=await renderToString(createSSRApp(AgentWorkflowRunInputs,{fields:[{key:'topic',label:'主题',required:true},{key:'review',label:'本人确认',type:'boolean',required:true},{key:'audience',label:'读者',type:'select',options:[{value:'team',label:'同事'}]}],modelValue:{topic:'季度报告',review:'yes',audience:'team'},files:''}));
    expect(html).toContain('type="text"');expect(html).toContain('type="radio"');expect(html).toContain('value="yes"');expect(html).toContain('value="no"');expect(html).toContain('<select');expect(html).toContain('value="team"');expect(html).toContain('同事');
  });
  it('keeps file selections distinct, removable and within the existing 16-file limit',()=>{
    const props=reactive({fields:[],modelValue:{},files:'a.md\nb.md'});const emit=vi.fn((name:string,value:string)=>{if(name==='update:files')props.files=value;});const scope=effectScope();
    const c=scope.run(()=>(AgentWorkflowRunInputs as unknown as{setup(p:unknown,ctx:unknown):{selectFile(path:string,selected:boolean):void;setValue(key:string,value:string):void}}).setup(props,{emit,expose(){}}))!;
    c.selectFile('a.md',true);expect(props.files.split('\n').filter(p=>p==='a.md')).toHaveLength(1);c.selectFile('a.md',false);expect(props.files).toBe('b.md');
    props.files=Array.from({length:16},(_,i)=>`${i}.md`).join('\n');c.selectFile('extra.md',true);expect(props.files).not.toContain('extra');c.setValue('review','no');expect(emit).toHaveBeenCalledWith('update:modelValue',{review:'no'});scope.stop();
  });
});
