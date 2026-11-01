export default {
  namespace: 'skillExecution',
  messages: {
    'zh-CN': {
      host: '运行平台：{platform}',
      expected: '部署声明：{platform}',
      mismatch: '与运行平台不一致',
      supported: '支持平台：{platforms}',
      notConfigured: '工具执行环境尚未就绪，可先安装 Skill 并保存 Agent 草稿。',
    },
    en: {
      host: 'Runtime platform: {platform}',
      expected: 'Deployment target: {platform}',
      mismatch: 'Does not match the runtime platform',
      supported: 'Supported platforms: {platforms}',
      notConfigured: 'Tool execution is not ready. You can install the Skill and save an Agent draft.',
    },
  },
};
