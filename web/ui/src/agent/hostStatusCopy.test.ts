import { afterEach, describe, expect, it } from 'vitest';
import { computed } from 'vue';
import { setLocale } from '@/i18n';
import { knownHostReason } from './hostStatusCopy';

afterEach(() => setLocale('zh-CN'));

describe('successful host status copy', () => {
  it('localizes known readonly and Office availability reasons without changing unknown prose', () => {
    setLocale('en');
    expect(knownHostReason('仅支持编辑 SKILL.md，其他包内文件保持只读')).toContain('Only SKILL.md');
    expect(knownHostReason('当前部署尚未配置 Office 编辑服务。')).toContain('not configured');
    expect(knownHostReason('用户自定义说明')).toBe('用户自定义说明');
    expect(knownHostReason()).toBe('');
  });

  it('keeps a read-only reason reactive to the selected locale', () => {
    setLocale('zh-CN');
    const reason = computed(() => knownHostReason('符号链接不支持浏览或预览'));
    expect(reason.value).toBe('符号链接不支持浏览或预览');
    setLocale('en');
    expect(reason.value).toBe('Symbolic links cannot be browsed or previewed');
  });

  it('localizes runtime descriptions and host version warnings without translating version values', () => {
    setLocale('en');
    expect(knownHostReason('同容器进程沙箱')).not.toMatch(/\p{Script=Han}/u);
    expect(knownHostReason('此 Skill 版本已停用，请上传新的版本。')).not.toMatch(/\p{Script=Han}/u);
    const warning = knownHostReason('已按替换原则删除旧版本 v1.2.3-团队。仍引用它的会话会保留，但不再能使用该 Skill。');
    expect(warning).toContain('1.2.3-团队');
    expect(warning.replace('1.2.3-团队', '')).not.toMatch(/\p{Script=Han}/u);
  });
});
