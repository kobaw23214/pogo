import { describe, expect, it } from 'vitest';
import { createSSRApp } from 'vue';
import { renderToString } from 'vue/server-renderer';
import { createI18n } from 'vue-i18n';
import contextMessages from '@/i18n/messages/contextMeter';
import ringMessages from '@/i18n/messages/taskContextRing';
import type { TaskContextReading } from '@/agent/taskContextUsage';
import TaskContextRing from './TaskContextRing.vue';

const measured: TaskContextReading = { sessionId: 'node-a', activeRunId: 'engine-a', used: 12500, window: 100000, source: 'measured', percent: 12.5, free: 87500, model: 'node-model' };
async function render(reading?: TaskContextReading | null, locale = 'zh-CN') {
  const app = createSSRApp(TaskContextRing, { reading });
  app.use(createI18n({ legacy: false, locale, messages: { 'zh-CN': { contextMeter: contextMessages.messages['zh-CN'], taskContextRing: ringMessages.messages['zh-CN'] }, en: { contextMeter: contextMessages.messages.en, taskContextRing: ringMessages.messages.en } } }));
  return renderToString(app);
}
describe('task context ring', () => {
  it('draws the actual prompt percentage and exposes used, limit and source accessibly', async () => {
    const html = await render(measured);
    expect(html).toContain('stroke-dasharray="12.5 100"'); expect(html).toContain('12,500'); expect(html).toContain('100,000');
    expect(html).toContain('最近一次模型请求的实测输入'); expect(html).toContain('node-model'); expect(html).toContain('tabindex="0"');
  });
  it('marks estimates and clamps only the graphic, keeping overflow visible in the label', async () => {
    const html = await render({ ...measured, source: 'estimated', percent: 130, used: 130000, free: 0 });
    expect(html).toContain('stroke-dasharray="100 100"'); expect(html).toContain('~130%'); expect(html).toContain('约数');
  });
  it('renders unknown data as a neutral track rather than zero percent', async () => {
    const html = await render(null);
    expect(html).not.toContain('stroke-dasharray='); expect(html).toContain('—'); expect(html).not.toContain('0%');
    expect(html).toContain('不会使用其他节点的数据');
  });
  it('does not draw an invented ratio for an unknown window and labels stale readings', async () => {
    const html = await render({ ...measured, percent: null, window: 0, free: null, stale: true }, 'en');
    expect(html).not.toContain('stroke-dasharray='); expect(html).toContain('12,500'); expect(html).toContain('limit unknown'); expect(html).toContain('last verified reading');
  });
});
