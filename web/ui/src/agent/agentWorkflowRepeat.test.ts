import {describe,expect,it} from 'vitest';
import type {AgentWorkflowDefinition,AgentWorkflowStep} from '@/api/agentWorkflows';
import {defaultWorkflowRepeat,workflowNodeCount,workflowPolicyValid,workflowRepeatIssues} from './agentWorkflowRepeat';
import {cloneWorkflowDefinition,mergeWorkflowEdits,sameWorkflowContent,workflowChanges,workflowEditOperations,workflowEditorIssues,workflowVariables} from './agentWorkflowEditor';
const step=(kind:'iteration'|'loop'):AgentWorkflowStep=>({id:'repeat',kind,...defaultWorkflowRepeat(kind)});
const definition=(kind:'iteration'|'loop'):AgentWorkflowDefinition=>({id:'flow',name:'Independent collection',revision:1,definition_hash:'a'.repeat(64),steps:[step(kind)]});
describe('repeat workflow editing contract',()=>{
 it('keeps iteration and bounded loop defaults valid without introducing graph back edges',()=>{for(const kind of ['iteration','loop'] as const){const d=definition(kind);expect(workflowRepeatIssues(d,d.steps[0]!)).toEqual([]);expect(workflowEditorIssues(d)).toEqual([]);expect(workflowNodeCount(d)).toBe(2);}});
 it('rejects nested flows, invalid local namespaces, limits and code policies',()=>{
  const d=definition('iteration'),node=d.steps[0]!;
  node.iteration!.concurrency=11;expect(workflowRepeatIssues(d,node)).toContain('repeat');node.iteration!.concurrency=2;
  node.iteration!.body.code!.inputs.item!.source='loop.state';expect(workflowRepeatIssues(d,node)).toContain('repeatBody');node.iteration!.body.code!.inputs.item!.source='iteration.item';
  node.iteration!.body.loop=defaultWorkflowRepeat('loop').loop;expect(workflowRepeatIssues(d,node)).toContain('repeatBody');delete node.iteration!.body.loop;
  node.iteration!.body.policy={max_retries:3};expect(workflowRepeatIssues(d,node)).toContain('codePolicy');
  const loop=definition('loop');loop.steps[0]!.loop!.until.value='';expect(workflowRepeatIssues(loop,loop.steps[0]!)).toContain('repeat');
 });
 it('preserves atomic code/repeat/policy patches, history copies and independent merge behavior',()=>{
  const base=definition('iteration'),local=cloneWorkflowDefinition(base),remote=cloneWorkflowDefinition(base);
  local.steps[0]!.iteration!.concurrency=4;remote.name='Renamed in chat';
  expect(sameWorkflowContent(base,local)).toBe(false);expect(workflowEditOperations(base,local)).toEqual([{op:'update_step',step_id:'repeat',patch:{iteration:local.steps[0]!.iteration}}]);
  expect(workflowChanges(base,local)).toEqual([expect.objectContaining({field:'iteration',stepId:'repeat'})]);
  const merged=mergeWorkflowEdits(base,local,remote);expect(merged.conflicts).toEqual([]);expect(merged.definition.steps[0]!.iteration!.concurrency).toBe(4);expect(base.steps[0]!.iteration!.concurrency).toBe(1);
  remote.steps[0]!.iteration!.concurrency=8;expect(mergeWorkflowEdits(base,local,remote).conflicts).toContain('steps.repeat.iteration');
 });
 it('exposes typed repeat results only to later nodes',()=>{
  const d=definition('iteration');d.steps.push({id:'next',kind:'code',code:{language:'python',source:'def main(inputs):\n return {"count": len(inputs["items"])}',inputs:{items:{type:'array',source:'steps.repeat.output.items'}},outputs:{count:{type:'integer'}}}});
  expect(workflowVariables(d,'next').some(value=>value.value==='{{ steps.repeat.output.items }}')).toBe(true);expect(workflowEditorIssues(d)).toEqual([]);
  d.steps[0]!.iteration!.items={type:'array',source:'steps.next.output.items'};expect(workflowRepeatIssues(d,d.steps[0]!)).toContain('repeat');
 });
 it('validates typed fallback output rather than treating failure as ordinary success',()=>{
  const outputs={rows:{type:'array' as const,items:{type:'number' as const}}};
  expect(workflowPolicyValid({max_retries:2,retry_delay_ms:100,on_failure:'fallback',fallback:{rows:[1,2]}},outputs)).toBe(true);
  expect(workflowPolicyValid({on_failure:'fallback',fallback:{rows:['bad']}},outputs)).toBe(false);
  expect(workflowPolicyValid({on_failure:'stop',fallback:{rows:[]}},outputs)).toBe(false);
 });
});
