import { describe,expect,it } from 'vitest';
import { defaultWorkflowCode,workflowNodeTestDefaults,workflowNodeTestInputs } from './agentWorkflowCode';
import { mergeWorkflowEdits,workflowChanges,workflowEditOperations,workflowEditorIssues,workflowVariables } from './agentWorkflowEditor';
import type { AgentWorkflowDefinition } from '@/api/agentWorkflows';
const definition=():AgentWorkflowDefinition=>({id:'flow',name:'Calculate',revision:1,definition_hash:'a'.repeat(64),inputs:[{key:'orders',type:'array'}],steps:[{id:'calculate',kind:'code',code:{...defaultWorkflowCode(),inputs:{orders:{type:'array',source:'inputs.orders'}},outputs:{total:{type:'number'}}}},{id:'review',kind:'confirm',prompt:'Amount {{ steps.calculate.output.total }}'}]});
describe('workflow code authoring and typed trial input',()=>{
 it('retains numbers, booleans, arrays and object constants for standalone tests',()=>{
  const spec={...defaultWorkflowCode(),inputs:{amount:{type:'number' as const,value:3},ok:{type:'boolean' as const,value:false},rows:{type:'array' as const,source:'inputs.rows'},config:{type:'object' as const,value:{tax:0.2}}}};
  expect(workflowNodeTestDefaults(spec)).toEqual({amount:3,ok:false,rows:[],config:{tax:0.2}});
  expect(workflowNodeTestInputs(spec,'{"amount":4,"ok":true,"rows":[1,2],"config":{"tax":0.1}}')).toEqual({amount:4,ok:true,rows:[1,2],config:{tax:0.1}});
  expect(workflowNodeTestInputs(spec,'{"amount":"4","ok":true,"rows":[],"config":{}}')).toBeNull();
  expect(workflowNodeTestInputs(spec,'{"amount":4,"ok":true,"rows":[],"config":{},"extra":1}')).toBeNull();
 });
 it('saves code changes as versioned local patches and detects conflicting concurrent edits',()=>{
  const base=definition(),local=structuredClone(base),remote=structuredClone(base);
  local.steps[0]!.code!.source='def main(inputs):\n    return {"total": 4}';remote.steps[0]!.title='Renamed';
  expect(workflowEditOperations(base,local)).toEqual([{op:'update_step',step_id:'calculate',patch:{code:local.steps[0]!.code}}]);
  expect(workflowChanges(base,local)).toContainEqual(expect.objectContaining({field:'code',stepId:'calculate'}));
  const merged=mergeWorkflowEdits(base,local,remote);expect(merged.conflicts).toEqual([]);expect(merged.definition.steps[0]!.code).toEqual(local.steps[0]!.code);expect(merged.definition.steps[0]!.title).toBe('Renamed');
  remote.steps[0]!.code!.source='def main(inputs):\n    return {"total": 5}';expect(mergeWorkflowEdits(base,local,remote).conflicts).toContain('steps.calculate.code');
 });
 it('offers structured upstream values and blocks downstream bindings and malformed schemas',()=>{
  const value=definition();expect(workflowEditorIssues(value)).toEqual([]);expect(workflowVariables(value,'review')).toContainEqual({value:'{{ steps.calculate.output.total }}',label:'calculate · output.total'});
  value.steps[0]!.code!.inputs.orders!.source='steps.review.output.data';expect(workflowEditorIssues(value)).toContainEqual(expect.objectContaining({code:'codeBinding'}));
  value.steps[0]!.code!.inputs.orders!.source='inputs.orders';value.steps[0]!.code!.outputs.total={type:'array'};expect(workflowEditorIssues(value)).toContainEqual(expect.objectContaining({code:'codeOutput'}));
 });
});
