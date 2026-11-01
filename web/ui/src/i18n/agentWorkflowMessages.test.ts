import { describe, expect, it, vi } from 'vitest';
import { createSSRApp } from 'vue';
import { renderToString } from '@vue/server-renderer';
import { createI18n } from 'vue-i18n';
import type { AgentWorkflowRun, AgentWorkflowStep, AgentWorkflowStepRun } from '@/api/agentWorkflows';
import canvas from './messages/agentWorkflowCanvas';
import graph from './messages/agentWorkflowGraph';
import workflow from './messages/agentWorkflow';
vi.mock('../components/TaskSessionResult.vue', () => ({ default: { render: () => null } }));
import AgentWorkflowRepeatResult from '../components/agent-workflow/AgentWorkflowRepeatResult.vue';

function leaves(value: unknown, prefix = ''): Record<string, string> {
  if (!value || typeof value !== 'object') return {};
  return Object.fromEntries(Object.entries(value).flatMap(([key, child]) =>
    typeof child === 'string' ? [[prefix + key, child]] : Object.entries(leaves(child, `${prefix}${key}.`))));
}
const parameters = (message: string) => [...message.matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)].map(match => match[1]!).sort();

describe('actual workflow locale dictionaries', () => {
  for (const module of [canvas, graph]) {
    it(`${module.namespace} ships matching Chinese and English leaf keys and interpolation parameters`, () => {
      const chinese = leaves(module.messages['zh-CN']), english = leaves(module.messages.en);
      expect(Object.keys(chinese).sort()).toEqual(Object.keys(english).sort());
      for (const key of Object.keys(english)) {
        expect(chinese[key]?.trim(), `${module.namespace}.${key} Chinese text`).toBeTruthy();
        expect(parameters(chinese[key]!), `${module.namespace}.${key} parameters`).toEqual(parameters(english[key]!));
      }
    });
  }

  it('renders repeat progress, retries, fallback and unknown outcomes in Chinese without locale fallback or mocked translations', () => {
    const i18n = createI18n({
      legacy: false, locale: 'zh-CN', fallbackLocale: false,
      messages: { 'zh-CN': { agentWorkflowCanvas: canvas.messages['zh-CN'] }, en: { agentWorkflowCanvas: canvas.messages.en } },
    });
    const translate = i18n.global.t as (key: string, params?: Record<string, string | number>) => string;
    const keys = ['iteration', 'loop', 'addIteration', 'addLoop', 'iterationHistory', 'loopHistory', 'repeatProgress', 'repeatFailedItems', 'repeatItem', 'repeatRound', 'codePolicy', 'codeRetries', 'codeFallbackUsed', 'codeAttempts', 'codeAttempt', 'codeAttemptRunning', 'codeAttemptUnknown', 'codeAttemptFailed', 'codeAttemptCompleted', 'repeatResultUnknown', 'repeatResultUnknownHint', 'codeResultUnknown', 'finishFormBeforeSwitch'];
    for (const key of keys) {
      const full = `agentWorkflowCanvas.${key}`, text = translate(full, { n: 2, completed: 1, total: 3 });
      expect(text, full).not.toBe(full);
      expect(text, full).toMatch(/[\u3400-\u9fff]/);
    }
    expect(translate('agentWorkflowCanvas.repeatProgress', { completed: 1, total: 3 })).toBe('已完成 1 / 3');
    expect(translate('agentWorkflowCanvas.repeatItem', { n: 2 })).toBe('第 2 项');
  });

  it('renders the actual repeat and code-attempt result components with the real Chinese dictionaries', async () => {
    const step: AgentWorkflowStep = { id: 'batch', kind: 'iteration', iteration: { items: { type: 'array', value: [1] }, body: { id: 'body', kind: 'code', code: { language: 'python', source: 'def main(inputs):\n    raise ValueError("sample")', inputs: {}, outputs: { value: { type: 'number' } } }, policy: { on_failure: 'fallback', fallback: { value: 0 } } } } };
    const run: AgentWorkflowRun = { id: 'run', workflow_id: 'flow', agent_id: 'agent', status: 'completed', definition: { id: 'flow', name: 'Locale acceptance', revision: 1, definition_hash: 'a'.repeat(64), steps: [step] } };
    const record: AgentWorkflowStepRun = { step_id: 'batch', status: 'completed', repeat: { kind: 'iteration', total: 1, items: [{ index: 0, status: 'completed', input: 1, output: { value: 0 }, fallback_used: true, code_execution: { language: 'python', error_code: 'execution_failed', exit_code: 1 }, attempts: [{ number: 1, finished_at: 2, error: 'sample error', execution: { language: 'python', stderr: 'sample error', error_code: 'execution_failed' } }] }] } };
    const i18n = createI18n({ legacy: false, locale: 'zh-CN', fallbackLocale: false, messages: { 'zh-CN': { agentWorkflowCanvas: canvas.messages['zh-CN'], agentWorkflow: workflow.messages['zh-CN'] } } });
    const app = createSSRApp(AgentWorkflowRepeatResult, { step, run, record }); app.use(i18n);
    const html = await renderToString(app);
    expect(html).not.toContain('agentWorkflowCanvas.');
    expect(html).not.toContain('agentWorkflow.status.');
    for (const text of ['分项执行记录', '已完成 1 / 1', '第 1 项', '每次代码尝试', '尝试', '失败', '备用结果']) expect(html).toContain(text);
  });
});
