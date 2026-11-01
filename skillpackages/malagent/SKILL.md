---
name: malagent
description: 对当前会话文件执行哈希、CAPA、YARA 和 Ghidra 静态分析，并检索关联样本库；适用于恶意代码分析、样本溯源与证据整理。
compatibility: Pogo Skill runtime v1, Linux amd64, isolated process execution
---

# Malware Analysis

这是一个完整的恶意代码静态分析工作流。宿主会将下列逻辑能力映射为当前会话可调用的工具名；使用宿主给出的映射，不要猜测工具名称。不同引擎是独立能力，但共用本 Skill 的取证流程。

## 首先判断输入

- 用户上传文件：使用宿主返回的当前会话 `fileId`。先取得哈希，再根据问题选择 CAPA、YARA 或 Ghidra。
- 用户询问样本库：使用此 Skill 内置的只读 `sample-index` 索引。先做窄范围检索，再读取选中样本详情。库中可能只有元数据或日志，不代表当前会话拥有样本文件。
- 用户仅给出 SHA-256：可以查库。若该样本 `has_binary=true`，文件级分析能力（`malware.file.*`）可直接传 `sampleSha256` 分析库内样本，无需上传；否则只有在文件实际上传并取得 `fileId` 后才能调用文件分析能力。除这两种形式外，不要把数据库路径或网页链接当作文件输入。
- 能力或数据源未就绪：准确报告缺失项；不以其他工具伪装该引擎的结果。

## 能力选择

| 逻辑能力 | 用途 | 关键边界 |
| --- | --- | --- |
| `malware.sample.search` | 按家族、类型、标签和检出数筛选样本 | 先取少量结果；不要默认拉全库 |
| `malware.sample.get` | 获取选中样本的详情、标签和已有摘要 | 需要动态日志时才读取完整详情 |
| `malware.sample.similar` | 读取库中基于相似哈希的候选 | 哈希距离是线索，不能单独证明同族 |
| `malware.family.get` | 家族列表、画像及已存储统计 | 保留来源里的演示/合成数据标识 |
| `malware.library.stats` | 库规模和已存储统计 | 只说明随 Skill 固定的索引内容，不暗示实时全量 |
| `malware.library.query` | 有界只读 SQL 聚合 | 仅单条 SELECT/WITH；失败不转为任意命令 |
| `malware.file.hash` | 文件的 SHA-256、SHA-1、MD5 和大小 | 本版不提供模糊哈希；不编造其他哈希 |
| `malware.file.capabilities` | CAPA 能力与 ATT&CK 线索 | 能力匹配不等于样本执行时确实发生 |
| `malware.file.yara.scan` | 用锁定规则集扫描文件 | 命中表示规则匹配，未命中不等于安全 |
| `malware.file.decompile` | Ghidra 静态分析并导出伪 C | 只读输入；结果由宿主登记为会话产物 |

文件级能力（上表后四项）的文件输入为 `fileId`（会话上传）或 `sampleSha256`（索引内 `has_binary=true` 的样本），二选一。

## 建议流程

1. 明确问题、输入来源及现有证据。用户只问检索结果时，不自动启动昂贵的文件分析。
2. 文件分析先校验哈希和大小；然后按需要运行 CAPA 与 YARA。保留引擎版本、规则来源及实际错误。
3. 需要解释可疑行为或函数时，再运行 Ghidra。基于实际返回的 `pseudoCPreview` 分析；`previewTruncated=true` 时明确只看到了部分代码，完整代码供用户下载。伪 C 是反编译近似，函数名、类型和控制流可能不完整。
4. 合并证据时明确区分：样本库记录、当前文件静态结果、模型推断。不要把工具输出中未出现的行为写成已观察事实。
5. 提供结论、支持证据、局限与下一步。对产物引用宿主返回的 artifact ID/下载链接，不给容器内或宿主文件路径。

## 输出纪律

- 从工具真实结果中提取少量关键证据，不倾倒整份反编译或数据库 JSON。
- 不把示例规则、测试标记或合成家族统计当作生产威胁情报。检查规则集与数据源说明。
- 工具失败、超时、空结果、规则未命中是不同情况。零命中、零函数和分析失败不得互相替代。
- 只读静态分析：不得运行上传样本，不得从样本内容生成命令，不得请求任意外联或隐式写库。
- `ingest`、`summarize`、`family-init`、`synth`、yarGen 规则生成和样本执行不在本版能力列表中；不能使用 shell 补做。

完整工具参数以当前会话实际注册的 JSON Schema 为准。文件、数据源、运行时和规则的版本由 Pogo 绑定并锁定，本 Skill 不安装依赖、不选择宿主路径。

## 分析流程参考

- [文件静态分析](references/file-static-analysis.md)
- [反编译分析](references/decompilation-analysis.md)
- [样本库检索与关联分析](references/sample-library-research.md)
