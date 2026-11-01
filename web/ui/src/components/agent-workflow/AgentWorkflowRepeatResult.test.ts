import {afterEach,describe,expect,it,vi} from 'vitest';
import {createSSRApp,effectScope,nextTick,reactive,type Ref} from 'vue';
import {renderToString} from '@vue/server-renderer';
import type {AgentWorkflowRepeatItem,AgentWorkflowRun,AgentWorkflowStep} from '@/api/agentWorkflows';
const host=vi.hoisted(()=>({call:vi.fn(),cleanup:[] as (()=>void)[]}));
vi.mock('vue',async original=>({...await original<typeof import('vue')>(),onBeforeUnmount:(fn:()=>void)=>host.cleanup.push(fn),useSSRContext:()=>({modules:new Set()})}));
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string,args?:Record<string,number>)=>key+(args?.n?` ${args.n}`:'')})}));
vi.mock('@/api/agentWorkflows',()=>({agentWorkflowsApi:{call:host.call}}));
vi.mock('../TaskSessionResult.vue',()=>({default:{props:['sessionId','activeRunId','agentId','workflowId','workflowRunId'],template:'<div>session</div>'}}));
import AgentWorkflowRepeatResult from './AgentWorkflowRepeatResult.vue';
const step:AgentWorkflowStep={id:'batch',kind:'iteration',iteration:{items:{type:'array',value:[1,2]},body:{id:'body',kind:'code',code:{language:'python',source:'def main(inputs):\n return {"value":inputs["value"]}',inputs:{value:{type:'number',source:'iteration.item'}},outputs:{value:{type:'number'}}}}}};
const rows=():AgentWorkflowRepeatItem[]=>[{index:0,status:'completed',input:1,output:{value:2},artifact:'same.md',code_inputs:{value:1},code_execution:{language:'python',stdout:'item one',exit_code:0}},{index:1,status:'failed',input:2,error:'second item failed',code_execution:{language:'python',error_code:'execution_failed',stderr:'<script>bad</script>'}}];
const run=():AgentWorkflowRun=>({id:'run-a',workflow_id:'flow-a',agent_id:'agent-a',status:'running',definition:{id:'flow-a',name:'Batch',revision:1,definition_hash:'a'.repeat(64),steps:[step]}});
interface Controller{selected:Ref<number|null>;item:Ref<AgentWorkflowRepeatItem|undefined>;artifact:Ref<string>;artifactLoading:Ref<boolean>;choose(value:AgentWorkflowRepeatItem):void;loadArtifact():Promise<void>}
afterEach(()=>{for(const cleanup of host.cleanup.splice(0))cleanup();host.call.mockReset();});
describe('per-item execution history',()=>{
 it('projects an unfinished running item as an unknown outcome after its parent stops without rewriting history',async()=>{
  const stopped=run();stopped.status='interrupted';const original:AgentWorkflowRepeatItem={index:0,status:'running',input:1};
  const html=await renderToString(createSSRApp(AgentWorkflowRepeatResult,{step,run:stopped,record:{step_id:'batch',status:'interrupted',repeat:{kind:'iteration',total:1,items:[original]}}}));
  expect(html).toContain('workflow-repeat-result-unknown');expect(html).toContain('repeatResultUnknown');expect(html).not.toContain('agentWorkflow.status.running');expect(original.status).toBe('running');
 });
 it('shows real code output and retains failures without marking every item successful',async()=>{
  const html=await renderToString(createSSRApp(AgentWorkflowRepeatResult,{step,run:run(),record:{step_id:'batch',status:'completed',repeat:{kind:'iteration',total:2,items:rows()}}}));
  expect(html).toContain('workflow-repeat-item');expect(html).toContain('repeatFailedItems');expect(html).toContain('second item failed');expect(html).toContain('&lt;script&gt;bad&lt;/script&gt;');
  const successful=await renderToString(createSSRApp(AgentWorkflowRepeatResult,{step,run:run(),record:{step_id:'batch',status:'completed',repeat:{kind:'iteration',total:1,items:[rows()[0]!]}}}));expect(successful).toContain('&quot;value&quot;: 2');expect(successful).toContain('item one');expect(html).toContain('agentWorkflow.status.failed');
 });
 it('keeps the selected item stable during parallel updates and scopes artifacts by parent node and item index',async()=>{
  const props=reactive({step,run:run(),visible:true,record:{step_id:'batch',status:'running',repeat:{kind:'iteration' as const,total:2,items:rows()}}});
  const scope=effectScope();const c=scope.run(()=>(AgentWorkflowRepeatResult as unknown as {setup(props:unknown,context:unknown):Controller}).setup(props,{emit(){},expose(){}}))!;
  expect(c.selected.value).toBe(1);c.choose(props.record.repeat.items[0]!);props.record.repeat.items[1]!.status='completed';await nextTick();expect(c.item.value!.index).toBe(0);
  let finish!:(value:unknown)=>void;host.call.mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));const pending=c.loadArtifact();expect(host.call.mock.calls[0]![2]).toEqual({action:'artifact',workflow_id:'flow-a',run_id:'run-a',step_id:'batch',item_index:0});
  c.choose(props.record.repeat.items[1]!);finish({ok:true,text:'old item artifact'});await pending;expect(c.artifact.value).toBe('');expect(c.artifactLoading.value).toBe(false);scope.stop();
 });
});
