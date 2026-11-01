# Pogo Skill runtime v1

本文件描述 Pogo 对标准 Skill 的可选运行扩展。当前实现状态与切换操作见 [Skill 管理说明](../skill-packages-2026-09-24.md)。本轮未执行测试或部署。

## 入口

发行单位是一个标准 Skill 目录，以 SKILL.md 为唯一入口，name/description 从该文件读取。
只有携带原生工具的 Skill 才需要 pogo-skill.json。该声明须为 pogo.skill/v1 / SkillRuntime，严禁使用旧格式或子 Skill 数组。

## 声明

以 [skill-runtime.schema.json](schemas/skill-runtime.schema.json) 为权威结构定义：

- id、version：不可变发行身份。相同版本只允许相同 ZIP 摘要。
- displayName：管理界面标签；执行身份来自验证后的包与契约。
- contracts、requiresCapabilities：能力契约文件及此 Skill 的全部需求，每个需求必须有唯一的随包提供者。
- runtimes：预构建工具环境，声明平台、隔离要求和 payload 摘要/大小。
- assets：随包只读规则与数据。索引作为资产，不使用外部数据库导入槽。
- providers：契约到程序入口的显式映射；宿主支持 process-json 分析执行，拒绝未实现的后端与权限请求。
- resourceSlots：会话输入文件或会话私有输出产物。可声明的远程连接契约属于核心 schema，当前本地执行服务不启用远程 provider；外部 MCP 在 Agent 中单独选择。

安装只解析 JSON、校验、解包并原子发布，不执行安装钩子或安装脚本。

## Agent 与会话

管理客户端只提交 Skill ID；宿主生成 AgentBinding，固定 PackageRef、SkillRef、能力提供者、资源和权限。
创建会话时宿主生成 ResolvedLock，绑定定义版本、运行环境身份与内容摘要。调用只能使用该会话锁中的能力。
停用状态每次重新检查；旧锁不能自动改绑新版本。

## 工具命名

每个能力契约在会话中暴露为一个工具，名字由契约 ID 派生：`skill_` 前缀 + 消毒到 wire 字母表（`^[a-zA-Z0-9_-]{1,64}$`）的契约 ID，如 `malware.sample.search` 对应工具 `skill_malware_sample_search`。超长 ID 截断并附 fnv32a 哈希尾（与 MCP 工具 `WireNameFor` 同一方案）；保留名 `skill_read` 及消毒后撞名的不同契约追加短摘要后缀，保证同一会话内工具名唯一。执行路由只按契约 ID，工具名仅供模型调用与界面展示。

## 完整性校验时机

安装（上传）时对暂存内容全量校验并原子发布；同一进程内首个 resolve 走 `Store.Load` 再全量复核一次，之后经 catalog 缓存复用已验证的包，会话打开与能力调用不再重复全量哈希。停用/吊销状态仍在每次 resolve 重新检查；删除会驱逐缓存。

## 执行

describe/invoke 使用 pogo.tool/1 JSON 帧，结构见 process-request 和 process-response schema。
context 只包含 files、assets、outputDirectory；无单独数据集导入与挂载协议。
输入 fileId 由宿主解析为当前会话上传文件。fileInputs 指向的输入属性可以是可选的：契约可同时声明库内样本引用（如 sampleSha256），此时宿主不为缺席的 fileId 暂存文件，由提供者从摘要锁定的资产中解析样本字节。产物由宿主有界收集，拒绝符号链接、硬链接与越界路径。
工具进程在同一 Pogo 容器的独立 bubblewrap 沙箱中执行。容器总配额提供共享硬上限，每次调用使用进程监督预算，不能把它描述为独立 cgroup 硬配额。

## 文档读取

普通 Skill 按原有文件机制读取。私有工具 Skill 的 SKILL.md 和 references 通过绑定对象读取，不向普通文件工具开放运行目录。
skill-runtime:// 是元数据位置标识。skill_read 只接受已选 Skill 的说明和 references 相对路径。

## 示例

examples 是 schema 和实现用的示意输入，不包含可执行 payload，不是可直接上传的 MalAgent 发行包，也不是此次改动的测试通过证据。
真实完整发行包由 cmd/pogo-malagent-package 或 cmd/pogo-yara-package 生成。
