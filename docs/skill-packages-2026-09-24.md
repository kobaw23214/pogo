# Skill 管理与完整工具运行包

2026-09-24 的实现记录为仅修改代码、打包器源码和文档，未运行测试、编译、构建 ZIP、构建镜像或部署。该记录不代表之后版本的状态；2026-09-29 核查范围与结果见 [项目现状](project-status.md)。

## 管理流程

1. 在 `/manage/skills` 上传一个标准 Skill ZIP。
2. 查看说明、工具、随包资源和运行环境状态。
3. 创建 Agent 时勾选 Skill，同时选择模型和所需 MCP 服务。
4. Pogo 从声明生成版本固定的工具授权；发布并进入真实 Pogo 会话。
5. 发布的 Agent 可通过原有 Agent MCP 入口消费。

停用的 Skill 不显示在选择列表；管理页仍可处理。被 Agent 或仍可打开的会话引用的版本不能删除。

## 唯一的 ZIP 格式

遵循 [Agent Skills 目录与 frontmatter 规范](https://agentskills.io/specification)：

```text
malagent/
├── SKILL.md                 # 必需，name: malagent + description
├── references/              # 可选，分项工作流程说明
├── pogo-skill.json           # Pogo 可选的工具运行声明
├── contracts/               # 本例的能力输入/输出契约
└── payload/                 # 本例的预构建工具、规则、索引
```

也可直接将这些文件放在 ZIP 根目录。若有外层文件夹，其名称必须与 frontmatter 的 name 一致。
一个 ZIP 只能包含一个 SKILL.md；其他说明用 references/*.md。没有工具运行声明的标准 Skill 可直接安装。
name 必填，1–64 个小写字母、数字或连字符，无首尾或连续连字符；description 必填且最多 1024 字符。

`pogo-skill.json` 是 Pogo 扩展，不是 Agent Skills 公共标准本身，也不意味着其他平台能自动安装这些二进制工具。
当前声明仅接受 `pogo.skill/v1`、`SkillRuntime`，根节点的 requiresCapabilities 表示此 Skill 的全部能力。
没有内部 skills 数组或子 Skill 映射，没有旧包格式识别、旧管理 API、手工工具勾选或外部 SQLite 导入。

## 安装与运行

- 唯一上传地址 `POST /api/skills/upload`，支持 multipart 和原始 ZIP；请求流写入磁盘，保留进度、取消和安装阶段。
- 两类 Skill 共用入口和 SKILL.md 校验。带运行环境的 Skill 使用同一暂存 ZIP，校验声明、摘要、平台与解压边界，然后原子安装。
- 管理 API：`/api/manage/skills`、`/{id}/content`、`/{id}/disable` 和删除 `/{id}`；创建时依赖预览 `/api/manage/skill-selection`。
- Agent 的公开输入只用 skills 数组；绑定及锁由服务端生成。带工具的 Skill 可与普通 Skill、MCP 同时选择。
- 同一 Pogo 容器内使用 bubblewrap 独立进程。工具、规则和索引只挂载给对应调用；普通 shell 不能读取私有运行目录。
- 执行器支持 Linux/amd64 和 Linux/arm64，需要可用 bubblewrap。`POGO_PLATFORM` 可声明 `linux/amd64` 或 `linux/arm64`，未设置时按实际运行架构检测；声明不能覆盖实际架构。系统库和 ELF 校验按架构选择，包内 runtime 声明和程序也必须匹配。当前 MalAgent 1.3.4 包仅提供 amd64 运行时，修改环境变量或 manifest 不能转换其中程序。
- 安装成功与执行就绪分别返回：上传结果的 `status=installed` 表示包已保存，`ready`、`execution`、`issues` 表示平台及配置预检结果，不承诺每次工具调用成功。平台不兼容或执行器不可用时仍可安装、管理并保存 Agent 草稿，但不能发布为就绪状态。Agent 内部工作流沿用所属 Agent 的已授权能力，并在续跑前重新检查能力快照，见 [内部工作流](agent-internal-workflows.md)。
- `workspaceData` 可将声明的文档型资产物化到选中该 Skill 的 Agent 会话工作区；它与留在私有执行目录内的程序、规则和索引不同，不应都描述为普通文件工具不可读。
- Skill 说明可通过会话 Skill 列表和 /skill:name 使用；模型通过 skill_read 读取所选 Skill 的 references 文档，不能借此读取运行时或数据文件。
- 会话文件和产物仍是私有资源；调用重查已停用状态和绑定，不将模型提供的路径当成授权。
- 注册表、安装器、工具分发、辅助进程统一使用 skillruntime / pogo-skill-runner；环境变量为 POGO_SKILL_RUNTIME_STORE、POGO_SKILL_RUNTIME_EXECUTOR。
- ZIP 上传上限统一为 POGO_SKILL_UPLOAD_MAX_BYTES。执行环境仍有现有超时、内存、CPU、输出大小限制。

## MalAgent 发布

源码目录 `skillpackages/malagent/`。默认发行版本改为 1.0.0，入口为 SKILL.md，十个工具属于同一个 Skill。
`cmd/pogo-malagent-package` 必须使用 -sample-index 将索引作为只读资产打包，不再生成绑定模板。
声明协议已更新，需同时重新构建随包 provider 二进制和外层 Skill ZIP；不能只给旧 ZIP 改名。
2026-09-24 的上述记录没有重新生成大体积 ZIP；实际交付必须另外核对产物及摘要。CAPA/YARA/Ghidra 等第三方工具仍在 payload 中，不加入 Pogo 基础镜像。
tiweb、malweb、atkweb 的实时连接继续通过 Agent 的 MCP 服务选择实现；离线索引不会自动成为三个系统的实时接口。

## 切换行为

Studio 注册表格式更新为 2。首次运行新代码时，旧 Agent 保留名称、指令、模型等身份信息，但清空能力选择并转为草稿，需重新选择 Skill/MCP。
旧 Studio 会话及分享按清理日志删除，不转换旧执行授权。新版 Docker 配置使用新的 Skill 数据卷，旧发行包不会自动出现。
这是旧格式首次进入新版时的迁移行为，不是每次启动都重新清理。2026-09-24 记录仅描述当时源码修改；本次文档核查不执行迁移、不清理会话、不变更 Docker 数据卷。部署前需核对实际注册表版本并备份，不能把旧设计记录当成当前数据状态。
