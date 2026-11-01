# 原生工具管理：分析与方案报告

日期：2026-09-25
背景：docs skill 的 7 个脚本升格为 tools.json 规范工具、注册收敛为"显式选择才注册"之后，
browser-use / web-drawing 的三型讨论引出的问题——**是否需要新成立一个"原生工具管理"模块**。

---

## 1. 问题定义

"原生工具管理"在 pogo 里可能指四件不同的事，必须先拆开：

1. **可见性**：用户能否在界面上看到系统有哪 9 个原生工具、各自做什么；
2. **按 Agent 选择**：Agent 编辑器能否像勾选 Skill / MCP 那样勾选原生工具；
3. **策略配置**：审批门槛、超时、作用域等参数能否在 UI 配置；
4. **生命周期**：启用/停用/版本管理（Skill 有上传删除、MCP 有 mcp.json 编辑）。

当前四项的现状分别是：**缺、缺、部分有、不适用**（详见 §2）。

## 2. 现状盘点（代码事实）

### 2.1 原生工具清单与分层

`tools.New`（tools/tool.go:289）按固定顺序组装：

| 层 | 工具 | 特性 |
|---|---|---|
| 只读四件套 | `read` `ls` `find` `grep` | 恒注册；root 受限；截断不落盘 |
| 写三件套 | `write` `edit` `bash` | `Options.ReadOnly=true` 时整组剔除 |
| 声明工具 | tools.json / 契约运行时 | 按 readOnlyHint 逐个过滤；本次改动后跟随 Skill 显式选择 |
| MCP 投影 | `mcp:*` + `mcp_describe` | 同上按只读标注过滤；按 Agent 的 mcp_servers 选择投影 |
| 域动词 | `browser` | 只读会话保留——读写判定在 BrowserGate 的 action 级 |
| 协调工具 | `subagent` `todo` | 浏览器会话恒有（子进程自行决定） |

另有 `image`（有 vision_model 才注册，无则零 schema 开销）。

### 2.2 已存在的"管理"原语

- **粗粒度开关**：`Options.ReadOnly`（explore 子代理用）＝已有的"原生工具裁剪"，但不可由 Agent 选择；
- **策略面**：附件策略（`PUT /api/attachments/policy`，可持久化的用户策略）、审批卡（gate）、BrowserGate 武装/解除、bash 的 command_scope/guard、POGO_SANDBOX；
- **观测面**：hub 事件（转录全量）、skillmonitor（skill 归因）、mcplog.db（MCP 调用）；
- **命名保护**：`builtinNames` 白名单（manifest.go:69）禁止第三方声明覆盖 read/bash/browser 等 9 个名字。

### 2.3 缺口

- `/api/tools` 目录只返回声明工具 + MCP 两半（server.go:941-946），**原生工具不在任何用户可见目录里**；
- Agent 的选择模型是三元组 `Models / Skills / MCPServers`（studioInput，指针语义 nil=legacy 继承），**原生工具不在其中**——勾不勾 browser-use，`browser` 工具都在；
- 除 ReadOnly 外没有任何按会话/按 Agent 裁剪原生工具的入口。

## 3. 业界对照

| 体系 | 做法 | 原生工具定位 |
|---|---|---|
| Claude Code | 固定工具集；permissions 规则（allow/deny per tool）做收紧 | 平台原语，管理=权限规则，不是开关 |
| OpenAI Assistants | code_interpreter / file_search 按 assistant 启用 | 内置能力=可选件 |
| MCP | 工具由 server 声明，客户端只做投影过滤 | 外部工具才需要"管理" |
| pogo 现状 | 固定工具集 + 审批卡/只读会话 | 平台原语（与 Claude Code 同流派） |

两个流派都成立，分界在"原生工具是否被当作能力商品"。pogo 的 Skills/MCP 已经把
"能力按 Agent 选择"立为产品原则（能力契约、摘要锁定、快照回放），原生工具是这条原则
**唯一没覆盖的维度**——这是真正的一致性缺口，而不是功能缺失。

## 4. 方案对比

| 方案 | 内容 | 评价 |
|---|---|---|
| A. 独立模块 | 新建"原生工具管理"页面 + 独立存储，仿 Skill 管理 | **不推荐**。原生工具随二进制走，无生命周期（无上传/删除/版本）；独立页面只会复制 Skill 管理的空壳；与能力选择模型进一步割裂 |
| B. 能力选择模型扩展 | Agent 编辑器加第四栏"内置工具"；`/api/tools` 目录加 builtin 段；Skill 卡片三型徽标 | **推荐**。复用 Skills/MCPServers 已有的指针语义、binding 快照、回放安全、迁移路径（nil=全部，零迁移） |
| C. 策略化 | Claude Code 式 per-tool allow/deny 规则 | 适合安全收紧（"这个 Agent 禁 bash"），不适合表达"这个 Agent 是只读分析员"。可作为 B 之上的补充，不作为主体 |
| D. 维持现状 | 不动 | 一致性缺口留存；Agent 编辑器的勾选语义继续有例外（browser-use 勾选≠启用浏览器） |

## 5. 推荐方案 B 的落地设计

### 5.1 数据模型

```go
// studioInput / studioAgent
Tools *[]string `json:"tools,omitempty"` // 原生工具名子集；nil = legacy 全量
```

与 Skills 完全同语义：nil 继承全量（老 Agent 零迁移），显式数组快照进 binding，
resume 的转录语义不变（manager.go 已有同一套注释承诺）。

### 5.2 受保护核心（不可关闭）

- `read ls find grep`：渐进披露的地基——skill_read、spill 文件回读、引用锚全部依赖；
- `todo`：会话协调基础设施，非能力；
- `subagent`：可在编辑器隐藏为"高级"，但不建议关闭（委托是平台核心模式）。

可选项：`write` `edit` `bash` `browser`（四个真正有副作用的动词）。`browser` 关闭时
BrowserTask skill 的 `/skill:` 武装路径同步失效（授权入口随工具走）。

### 5.3 注册点

`tools.New(tools.Options{...})` 增加排除集（或在 New 后按 Tools 过滤），与
`Options.ReadOnly` 正交组合；编辑器侧校验"最低可用集合"防止自锁（无 write/edit/bash
且无任何声明工具的 Agent 保存时给出警告）。

### 5.4 目录与展示

- `/api/tools` 增加 `builtin` 段（name/description/readonly/protected），一次查询三态齐活；
- Skill 卡片三型徽标：**自带工具**（docs/malagent）/ **增强内置**（browser-use）/
  **纯知识**（web-drawing）——顺带解决前两轮讨论的勾选语义问题。

### 5.5 明确不做

- 不允许 manifest/运行时声明覆盖内置名（白名单已有，维持）；
- 不做独立"原生工具管理"页（无生命周期可管理）；
- 不做 per-tool 策略页（bash 超时、guard 规则）——等出现真实需求再议，避免提前抽象。

## 6. 分阶段与工作量

| 阶段 | 内容 | 量级 |
|---|---|---|
| P0 | `/api/tools` builtin 段 + Skill 卡片三型徽标 | ~0.5 天 |
| P1 | `studioAgent.Tools` 选择 + 注册过滤 + binding 快照 + 编辑器第四栏 + 自锁校验 + 测试 | ~2-3 天 |
| P2（可选） | per-tool 策略（Claude Code 式 deny 规则）、bash 参数 UI 化 | 待需求 |

## 7. 结论

**不需要"新成立一个模块"。** 原生工具没有生命周期，"管理"它的正确形态是**加入已有的
能力选择模型**（Agent 定义的一个维度 + 目录的一段展示），而不是复制一个 Skill 管理式的
独立页面。P0 一天以内可交付纯展示收益；P1 补齐一致性缺口，让"勾选 = 启用能力"贯穿
Skill / MCP / 原生工具三个维度。
