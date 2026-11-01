export default {
  namespace: 'taskContextRing',
  messages: {
    'zh-CN': {
      title: '此 Agent 节点的上下文',
      usage: '已用 {used} / 上限 {limit} tokens · {percent}',
      unknown: '暂无可归属到此节点的上下文读数；不会使用其他节点的数据。',
      unknownLimit: '未知',
      model: '模型：{model}',
      stale: '显示最近一次已确认的读数；打开执行过程后可继续更新。',
    },
    en: {
      title: 'This Agent node’s context',
      usage: 'Used {used} / limit {limit} tokens · {percent}',
      unknown: 'No context reading can be attributed to this node yet. Other nodes’ readings are not used.',
      unknownLimit: 'unknown',
      model: 'Model: {model}',
      stale: 'Showing the last verified reading. Open execution details to resume updates.',
    },
  },
};
