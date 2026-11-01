import {describe,expect,it,vi} from 'vitest';
import {createSSRApp} from 'vue';
import {renderToString} from '@vue/server-renderer';
import AgentWorkflowCodeResult from './AgentWorkflowCodeResult.vue';
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}));
describe('code result history',()=>{
 it('never labels an unfinished retry as completed and marks stopped attempts as unverified',async()=>{
  const record={step_id:'calculate',status:'running',attempts:[{number:1,error:'first failed',finished_at:10},{number:2,started_at:11}]};
  const live=await renderToString(createSSRApp(AgentWorkflowCodeResult,{status:'running',record}));expect(live).toContain('codeAttemptRunning');expect(live).not.toContain('codeAttemptCompleted');
  const stopped=await renderToString(createSSRApp(AgentWorkflowCodeResult,{status:'interrupted',record}));expect(stopped).toContain('codeAttemptUnknown');expect(stopped).not.toContain('codeAttemptRunning');
 });
 it('labels configured fallback separately and preserves every failed attempt and log',async()=>{
  const html=await renderToString(createSSRApp(AgentWorkflowCodeResult,{status:'completed',record:{step_id:'calculate',status:'completed',fallback_used:true,output:{total:0},code_execution:{language:'python',error_code:'execution_failed',stderr:'last failure',exit_code:1},attempts:[{number:1,error:'first failed',execution:{language:'python',stderr:'original first log'}},{number:2,error:'second failed',execution:{language:'python',stderr:'original second log'}}]}}));
  expect(html).toContain('workflow-code-fallback-used');expect(html).toContain('workflow-code-attempts');expect(html).toContain('original first log');expect(html).toContain('original second log');expect(html).toContain('codeAttemptFailed');expect(html).toContain('&quot;total&quot;: 0');
 });
 it('shows frozen typed inputs, outputs and bounded execution logs without rendering script content as HTML',async()=>{
  const html=await renderToString(createSSRApp(AgentWorkflowCodeResult,{status:'failed',record:{step_id:'calculate',status:'failed',code_inputs:{amount:2,confirmed:false},output:{total:4},code_execution:{language:'python',runtime:'Python isolated',stdout:'<script>alert(1)</script>',stderr:'bad value',stdout_truncated:true,exit_code:1,duration_ms:20,source_sha256:'a'.repeat(64),error_code:'INVALID_OUTPUT'}}}));
  expect(html).toContain('workflow-code-output');expect(html).toContain('&quot;confirmed&quot;: false');expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');expect(html).not.toContain('<script>');expect(html).toContain('codeLogsTruncated');expect(html).toContain('INVALID_OUTPUT');expect(html).toContain('aaaaaaaaaa');
 });
});
