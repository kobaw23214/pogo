// Illustrative workflows only; never presented as live workspace activity.
export const landingScenarios = {
    research: { role: '研究助手', skill: '资料整理与比较', tools: '搜索 · 文档读取', agent: 'research-agent', question: '比较三个方案，梳理依据，给我一份可复核的建议。', answer: '从问题出发，把分散的信息组织为结论。', steps: ['明确目标与比较维度', '查找资料并核对来源', '汇总发现与证据材料'], file: '方案对比与建议.md', title: ['让结论，', '有据可循。'], summary: ['问题、依据与建议，', '收束在同一份材料里。'], outline: ['比较框架', '关键发现', '来源与建议'] },
    engineering: { role: '工程助手', skill: '代码阅读与排查', tools: '文件 · 开发工具', agent: 'engineering-agent', question: '读一下这段实现，定位问题，整理修改和验证建议。', answer: '围绕明确的范围，建立从发现到验证的路径。', steps: ['理解需求与代码上下文', '定位问题并比较解决方案', '整理改动与验证清单'], file: '实现分析与验证计划.md', title: ['让思路，', '走向实现。'], summary: ['发现、方案与验证，', '组成下一步的行动。'], outline: ['问题定位', '方案比较', '验证清单'] },
    analysis: { role: '分析助手', skill: '线索归纳与核查', tools: '数据 · 查询工具', agent: 'analysis-agent', question: '分析这批记录，归纳异常，给出需要继续核查的重点。', answer: '把复杂记录展开，让关联与疑点逐渐清晰。', steps: ['理解数据范围与分析目标', '归纳线索并核对上下文', '输出发现与后续核查建议'], file: '分析发现与核查建议.md', title: ['让信息，', '形成判断。'], summary: ['从记录中的细节，', '找到值得追问的问题。'], outline: ['分析范围', '关键线索', '核查建议'] }
  } as const;

export type LandingScenarioKey = keyof typeof landingScenarios;

export type CapabilityKey = 'agent' | 'model' | 'skill' | 'mcp';

// The left collage layers mirror the four capability folds: front card =
// agent, iris plate = models, back plate = skills, shell corner = mcp.
export const anatomyFocusContent: Record<CapabilityKey, { caption: string; lines: [string, string]; tag: string }> = {
  agent: { caption: 'ANATOMY — 01 / AGENT', lines: ['One foundation.', 'Endless configurations.'], tag: '01 / AGENT · BRINGS IT TOGETHER' },
  model: { caption: 'ANATOMY — 02 / MODELS', lines: ['Choose how', 'your agent thinks.'], tag: '02 / MODELS · HOW IT THINKS' },
  skill: { caption: 'ANATOMY — 03 / SKILLS', lines: ['Experience,', 'distilled into method.'], tag: '03 / SKILLS · HOW IT WORKS' },
  mcp: { caption: 'ANATOMY — 04 / MCP', lines: ['Connected to the', 'world you work in.'], tag: '04 / MCP · WHAT IT REACHES' },
};
