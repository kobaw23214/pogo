import type { AgentWorkflowCodeBinding, AgentWorkflowCodePolicy, AgentWorkflowDefinition, AgentWorkflowStep, AgentWorkflowValueSchema } from '@/api/agentWorkflows';
import { guaranteedWorkflowPredecessors } from './agentWorkflowDag';
import { workflowSchemaValid, workflowValueMatches } from './agentWorkflowCode';

const id=/^[A-Za-z][A-Za-z0-9_-]{0,63}$/;
const ref=/^(?:inputs\.([A-Za-z][A-Za-z0-9_-]{0,63})(?:\.[A-Za-z0-9_-]+)*|steps\.([A-Za-z][A-Za-z0-9_-]{0,63})\.(?:summary|artifact|feedback|output(?:\.[A-Za-z0-9_-]+)*))$/;
const bytes=(v:unknown)=>new TextEncoder().encode(JSON.stringify(v)??'').length;
export function workflowNodeCount(definition:Pick<AgentWorkflowDefinition,'steps'>):number { return definition.steps.reduce((count,step)=>count+1+(step.iteration?.body||step.loop?.body?1:0),0); }
export function defaultWorkflowRepeat(kind:'iteration'|'loop'): Pick<AgentWorkflowStep,'iteration'|'loop'> {
  if(kind==='iteration')return {iteration:{items:{type:'array',value:[]},body:{id:'body',kind:'code',code:{language:'python',source:'def main(inputs):\n    return {"result": inputs["item"]}\n',inputs:{item:{type:'object',source:'iteration.item'}},outputs:{result:{type:'object'}}}},concurrency:1,max_items:100,on_item_error:'stop'}};
  return {loop:{initial:{type:'object',value:{count:0}},body:{id:'body',kind:'code',code:{language:'python',source:'def main(inputs):\n    return {"count": inputs["state"]["count"] + 1}\n',inputs:{state:{type:'object',source:'loop.state'}},outputs:{count:{type:'integer'}}}},until:{source:'loop.state.count',operator:'gte',value:'3'},max_iterations:10}};
}
export function workflowTypedValueMatches(schema:AgentWorkflowValueSchema,value:unknown,depth=0):boolean {
  if(depth>8||!workflowValueMatches(schema.type,value))return false;
  if(schema.type==='array'&&schema.items)return (value as unknown[]).every(row=>workflowTypedValueMatches(schema.items!,row,depth+1));
  if(schema.type==='object'&&Object.keys(schema.properties||{}).length){const object=value as Record<string,unknown>;return (schema.required||[]).every(key=>Object.prototype.hasOwnProperty.call(object,key))&&Object.entries(object).every(([key,child])=>!!schema.properties?.[key]&&workflowTypedValueMatches(schema.properties[key],child,depth+1));}
  return true;
}
export function workflowPolicyValid(policy:AgentWorkflowCodePolicy,outputs?:Record<string,AgentWorkflowValueSchema>):boolean {
  if(!Number.isInteger(policy.max_retries??0)||(policy.max_retries??0)<0||(policy.max_retries??0)>2||!Number.isInteger(policy.retry_delay_ms??0)||(policy.retry_delay_ms??0)<0||(policy.retry_delay_ms??0)>5000||!['stop','fallback'].includes(policy.on_failure||'stop'))return false;
  if(policy.on_failure!=='fallback')return policy.fallback===undefined;
  const fallback=policy.fallback;if(!fallback||typeof fallback!=='object'||Array.isArray(fallback)||bytes(fallback)>262144||!outputs)return false;
  return Object.keys(fallback).length===Object.keys(outputs).length&&Object.entries(outputs).every(([key,schema])=>Object.prototype.hasOwnProperty.call(fallback,key)&&workflowTypedValueMatches(schema,fallback[key]));
}
export function workflowRepeatIssues(definition:AgentWorkflowDefinition,step:AgentWorkflowStep):string[] {
  const issues=new Set<string>(), repeat=step.kind==='iteration'?step.iteration:step.kind==='loop'?step.loop:undefined;
  if(!['iteration','loop'].includes(step.kind)){if(step.iteration||step.loop)issues.add('repeat');return [...issues];}
  if(!repeat||step.kind==='iteration'&&step.loop||step.kind==='loop'&&step.iteration||step.code||step.task||step.prompt||step.output_file||step.skills?.length||step.shortcuts?.length){issues.add('repeat');return [...issues];}
  const allowed=guaranteedWorkflowPredecessors(definition,step.id), inputs=new Set(definition.inputs?.map(i=>i.key));
  const sourceValid=(source:string,local=false)=>{
    if(local&&(step.kind==='iteration'?/^iteration\.(?:item(?:\.[A-Za-z0-9_-]+)*|index)$/:/^loop\.(?:state(?:\.[A-Za-z0-9_-]+)*|index)$/).test(source))return true;
    const match=source.match(ref);return !!match&&(!match[1]||inputs.has(match[1]))&&(!match[2]||allowed.has(match[2]));
  };
  const bindingValid=(binding:AgentWorkflowCodeBinding,local=false)=>{if(!binding)return false;const literal=Object.prototype.hasOwnProperty.call(binding,'value');return literal!==!!binding.source&&(literal?workflowValueMatches(binding.type,binding.value)&&bytes(binding.value)<=262144:sourceValid(binding.source||'',local));};
  if(step.kind==='iteration'){
    const spec=step.iteration!;
    if(spec.items?.type!=='array'||!bindingValid(spec.items)||!Number.isInteger(spec.concurrency??1)||(spec.concurrency??1)<1||(spec.concurrency??1)>10||!Number.isInteger(spec.max_items??100)||(spec.max_items??100)<1||(spec.max_items??100)>100||!['stop','continue'].includes(spec.on_item_error||'stop')||Array.isArray(spec.items?.value)&&spec.items.value.length>(spec.max_items??100))issues.add('repeat');
  }else{
    const spec=step.loop!, condition=spec.until;
    if(spec.initial?.type!=='object'||!bindingValid(spec.initial)||!Number.isInteger(spec.max_iterations)||spec.max_iterations<1||spec.max_iterations>100||!condition||!/^loop\.(?:state(?:\.[A-Za-z0-9_-]+)*|index)$/.test(condition.source)||!['equals','not_equals','contains','not_contains','is_empty','not_empty','gt','gte','lt','lte'].includes(condition.operator)||['gt','gte','lt','lte'].includes(condition?.operator)&&(!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test((condition.value||'').trim())||!Number.isFinite(Number(condition.value)))||['is_empty','not_empty'].includes(condition?.operator)&&!!condition.value)issues.add('repeat');
  }
  const body=repeat.body;
  if(!body||!['agent','code'].includes(body.kind)||step.kind==='loop'&&body.kind!=='code'||body.iteration||body.loop||body.condition||body.id&&!id.test(body.id)){issues.add('repeatBody');return [...issues];}
  if(body.kind==='agent'){
    if(!body.task?.trim()||body.prompt||body.code||body.policy||bytes(body.task)>16384)issues.add('repeatBody');
    for(const field of ['skills','shortcuts'] as const)if((body[field]?.length||0)>32||new Set(body[field]).size!==(body[field]?.length||0)||body[field]?.some(value=>!value||/[\s\p{Cc}]/u.test(value)))issues.add('repeatBody');
    if(body.output_file&&(!body.output_file.split('/').every(p=>p&&p!=='.'&&p!=='..')||/[\\\0\r\n:]/.test(body.output_file)||body.output_file.startsWith('/')))issues.add('repeatBody');
    for(const match of (body.task||'').matchAll(/\{\{\s*([^{}]*?)\s*\}\}/g))if(!sourceValid(match[1]!.trim(),true))issues.add('repeatBody');
  }else{
    const code=body.code;
    if(!code||body.task||body.prompt||body.output_file||body.skills?.length||body.shortcuts?.length||!['python','javascript'].includes(code.language)||!code.source.trim()||new TextEncoder().encode(code.source).length>65536||code.source.includes('\0')||code.timeout_seconds!==undefined&&(!Number.isInteger(code.timeout_seconds)||code.timeout_seconds<1||code.timeout_seconds>60)||!Object.keys(code.outputs||{}).length||Object.values(code.outputs||{}).some(schema=>!workflowSchemaValid(schema)))issues.add('repeatBody');
    if(code&&Object.entries(code.inputs||{}).some(([key,binding])=>!id.test(key)||!bindingValid(binding,true)))issues.add('repeatBody');
    if(body.policy&&!workflowPolicyValid(body.policy,code?.outputs))issues.add('codePolicy');
  }
  return [...issues];
}
