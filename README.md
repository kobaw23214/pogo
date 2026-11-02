<a id="top"></a>

# pogo

[![CI](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml/badge.svg)](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kobaw23214/pogo)](https://goreportcard.com/report/github.com/kobaw23214/pogo)
[![License](https://img.shields.io/github/license/kobaw23214/pogo?color=blue)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)

用 Go 与 Vue 构建的 Agent 工作台:每个 Agent 有真实的浏览器、真实的 Office 编辑、可编排的工作流与后台任务——全程可见、可审批、可回放。

<div align="center">
  <img src="docs/images/chat-workspace-live.png" width="880" alt="pogo 对话工作台">
</div>

## 能力总览

**功能面**

| 能力 | 一句话 |
|---|---|
| [操作真实浏览器](#1-操作真实浏览器) | 每会话私有 Chromium,人与 Agent 同看一页,先审批后动手 |
| [编辑真实 Office 文档](#2-编辑真实-office-文档) | 在线 ONLYOFFICE 画布 + 两段式解析,没读到回执不许写 |
| [对话即富文档](#3-对话即富文档) | Mermaid / ECharts / 数学公式 / 数据表,流式渲染 |
| [公共信息区与工作流](#4-公共信息区与-agent-工作流) | 跨 Agent 信息库;顺序 + DAG 工作流,版本快照启用制 |
| [Agent 管理与快捷方式](#5-agent-管理与嵌套快捷方式) | 模板创建、多模型、能力继承;快捷方式 5 层嵌套 |
| [Agent 自管 Skill](#6-agent-自己管理-skill) | 上传包 / 自著 / 运行时包,bubblewrap 沙箱执行 |
| [后台 Bash 与任务](#7-后台-bash-与后台任务) | 后台 shell / 后台子代理 / 定时与哨兵 / 持久终端 |
| [多模型接入](#8-多模型接入) | providers.json 单一配置源,工具协议自动降级 |

**运行时工程**

| 能力 | 一句话 |
|---|---|
| [上下文工程](#9-运行时工程) | 自动压缩 + 软上限 + 上下文编辑,首条消息永久钉住 |
| [检查点与回放](#9-运行时工程) | 影子 git 快照,rewind 三模式,先预览 diff 再回退 |
| [引用核查](#9-运行时工程) | cite 标签由 harness 分配,引用不存在会被打回重写 |
| [子代理治理](#9-运行时工程) | explore 默认后台,写操作租约串行,权限不超父级 |
| [审批门体系](#9-运行时工程) | 「替我审批」三档,freeze/thaw,超时 fail-closed |
| [记忆系统](#9-运行时工程) | 用户/项目/Agent 三层可写根,清单注入防提示词污染 |
| [视觉多模态](#10-平台能力) | visionprobe 实测判卷,纯文本模型可"代看"图片 |
| [成本核算](#10-平台能力) | 拒绝虚构价格,无价格预算拒绝执行,缓存计费防双计 |

**平台与集成**

| 能力 | 一句话 |
|---|---|
| [多用户多租户](#10-平台能力) | PBKDF2 账号 + 目录级工作区隔离,可直接自托管 |
| [会话全文搜索](#10-平台能力) | trigram FTS5,中文可搜,索引自愈 |
| [大输出落盘](#10-平台能力) | 超限输出内容寻址落盘,可 grep/续读,自动清理 |
| [观测体系](#11-集成与形态) | OTLP 导出,W3C traceparent 贯穿 MCP 调用链 |
| [MCP 双向](#11-集成与形态) | 每个 Agent 一个对外 MCP 端点,白名单发布工具 |
| [REST API 与 CLI](#11-集成与形态) | 262 条路由 + SSE;JSONL 事件流与 Web 同契约 |
| [媒体中继](#11-集成与形态) | 单端口复用 HTTP + TURN,内网穿透看浏览器画面 |
| [桌面端(实验性)](#11-集成与形态) | 桌面即浏览器执行器;现阶段不完善,见节内说明 |

## 1. 操作真实浏览器

[browser-runtime](browser-runtime/) worker(Playwright + Wayland + WebRTC)为每个会话提供**私有 Chromium**:20+ 动作(snapshot/click/type/extract/batch/devtools…),页面按原始 URL 加载,无反代无 iframe。

- 人与 Agent **共看同一个 Page**:WebRTC 实时画面,随时接管/交还,共享登录态
- **先审批后动手**:首次页面操作弹审批卡,一轮一授权;子 Agent 无权自行开启
- **证据可溯源**:`dom-evidence-v1` 结构化证据(表格原始行列、跨行跨列,不展平),页面变了即拒绝;BigInt 精确核验数值(`0.1+0.2 = 0.3`)
- 网页下载自动入工作区,工作区文件可回传网页

## 2. 编辑真实 Office 文档

`office` skill 三层能力:`docs_office` 驱动 ONLYOFFICE 在线画布(用户所见即所得)→ `transform` 受控 Python 事务批量修改 → 未打开文件两段式解析(`info` 先取清单再按段取,首屏 token 降一个数量级)。

- **写入防盲改**:Word 写入须携带读取回执 `operationId`,Excel 须携带完整输入矩阵
- 编辑器**原生历史**撤销/重做,每文件 50 个版本可恢复
- 解析有防护:PDF 300MB 上限、扫描版不假装 OCR;docx 防解压炸弹;xlsx 流式只读,宏不执行

<div align="center">
  <img src="docs/images/office-edit.png" width="880" alt="Office 编辑:Agent 在线表格加列加边框并保存">
</div>

## 3. 对话即富文档

| 写法 | 渲染 |
|---|---|
| ` ```mermaid ` / ` ```echarts ` | Mermaid 11 全量图表 / ECharts 交互图表 |
| `$…$` / `$$…$$` / ` ```math ` | KaTeX 数学公式 |
| ` ```csv ` / ` ```json-table ` | 直接渲染成数据表 |
| ` ```diff ` / 多语言代码块 | 增删高亮 / 语法高亮 |
| `[TOC]` `[^1]` `- [x]` `> [!NOTE]` `:::` | 目录 / 脚注 / 任务列表 / callout / 折叠块 |
| `[文件](路径)` | 工作区文件卡片,点击内联预览 |

流式输出期间先显示代码,完成后自动成图。

<div align="center">
  <img src="docs/images/markdown-render-demo.png" width="880" alt="富文档渲染:目录/公式/表格/列表">
</div>
<div align="center">
  <img src="docs/images/chart-datagrid-panel.png" width="880" alt="ECharts 图表与可排序数据表">
</div>

## 4. 公共信息区与 Agent 工作流

**信息库**是用户与 Agent 的共享事实源:文件、快捷方式、网页引用、Office 选区统一编目,同工作空间的**兄弟 Agent 看到同一份视图**(Agent 侧只读检索);外部系统经 `/panels/<名称>/` 反代接入右侧 hub 标签条。

<div align="center">
  <img src="docs/images/information-store.png" width="880" alt="信息库:跨 Agent 共享的文件与引用编目">
</div>

**工作流**:绑定单个 Agent 的可复用流程,顺序(agent/confirm/code/iteration/loop)与图(condition/parallel/join,静态校验无环+全可达)双形态。

- 输入按 SHA256 冻结、前序产物校验物化、Agent 配置指纹快照——变化即中断,不静默重跑
- SSE 每 500ms 推送全节点状态;Vue Flow 画布着色,每节点可打开执行会话看 Usage 环
- 「启用」存不可变版本快照,正式运行只执行 EnabledVersion;支持草稿试跑、单节点测试、链式重试
- Agent 在对话中用 `agent_workflow_read/edit/enable/run` 自管流程

<div align="center">
  <img src="docs/images/workflow-canvas-chat.png" width="880" alt="工作流画布:多分支并行节点编排与对话解释">
</div>

## 5. Agent 管理与嵌套快捷方式

<div align="center">
  <img src="docs/images/agent-create-templates.png" width="880" alt="从模板创建 Agent,右侧实时预览">
</div>

五个内置模板或空白起步,草稿 → 发布,发布后 `/a/{id}` 直达对话;一个 Agent 绑定多模型随时切换;Skill/MCP 按配置组合继承。

**快捷方式**是 Agent 私有的可复用资料包(文本/文件/Skill/网页元素/Office 选区/**嵌套快捷方式**),扁平 DAG、上限 5 层 512 节点、静态校验无环;首页起始卡是实时投影,编辑即时生效不留过期副本。

<div align="center">
  <img src="docs/images/shortcut-db-ref-edit.png" width="880" alt="编辑快捷方式:保存可复用的连接引用资料">
</div>
<div align="center">
  <img src="docs/images/agent-workbench-shortcuts.png" width="880" alt="对话中引用私有资料继续工作">
</div>

<div align="center">
  <img src="docs/images/agents-manage-graphite.png" width="880" alt="我的 Agents 管理页">
</div>

## 6. Agent 自己管理 Skill

<div align="center">
  <img src="docs/images/skill-management.png" width="880" alt="Skill 管理:技能库 + 调用监控">
</div>

三种形态:zip 上传包(防炸弹、热加载)/ Agent 对话中 `skill_save` **自著 Skill**(存私有目录,不混全局)/ 运行时可执行包(如恶意代码分析:CAPA/YARA/Ghidra)。

每个 capability 注册为**该会话的普通工具**,由 `pogo-skill-runner` 在 bubblewrap 沙箱短命执行:请求/响应双向 JSON Schema 门,超时/输出/内存/进程硬限,调用入监控。

<div align="center">
  <img src="docs/images/skill-capabilities.png" width="880" alt="Skill 能力组:agent-workflows/background-tasks/browser-use/office">
</div>

## 7. 后台 Bash 与后台任务

- **后台 shell**:`run_in_background` 立即返回任务 ID,按字节偏移续读输出,回执持久化、重启可恢复
- **后台子代理**:explore 默认后台,结果以通知送达并合并唤醒
- **宿主级任务**:cron/哨兵两类持久任务,不依赖会话存活;**创建默认暂停,启用需用户授权**
- **持久终端**:每会话 pty,关标签只是 detach,重连回放 backlog

<div align="center">
  <img src="docs/images/chat-tool-transparency.png" width="880" alt="执行透明:耗时、工具摘要、审批开关">
</div>

## 8. 多模型接入

单一 OpenAI 兼容客户端(支持 `reasoning_content` 等扩展字段),`providers.json` 唯一配置源、零内置 provider、密钥只走环境变量;不原生支持工具调用的模型自动降级 XML/JSON 文本协议。

<div align="center">
  <img src="docs/images/model-provider-config.png" width="880" alt="模型配置:provider 与密钥环境变量">
</div>
<div align="center">
  <img src="docs/images/mcp-client-panel.png" width="880" alt="MCP 管理:外部工具连接接入">
</div>
<div align="center">
  <img src="docs/images/agent-editor-models-mcp.png" width="720" alt="编辑 Agent:多模型与 MCP 绑定">
</div>
<div align="center">
  <img src="docs/images/agent-editor-mobile.png" width="360" alt="移动端配置">
</div>

<a id="runtime"></a>
## 9. 运行时工程

- **上下文工程**:自动压缩在窗口 92-98% 水位触发——首条用户消息永久逐字钉住、切点对齐 tool 配对、摘要不叠罗汉;软上限注入一条通知由模型自决收尾;上下文编辑清最旧工具结果释放空间,是**视图不改转录**(rewind 仍见原文)
- **检查点与回放**:每轮运行前把工作区快照进影子 git 仓;rewind 三模式(chat/files/both)先预览每文件 diff 再回退;转录 append-only,只留不可达分支不删记录
- **引用核查**:harness 给每个工具调用分配 cite 标签并快照摘录;答案里的引用锚点经正则校验,**引用没产生过的标签或伪页码会被打回重写**;悬停卡直达工具记录,来源面板分组展示
- **子代理治理**:explore 只读模式默认后台,结果以通知送达合并唤醒;写操作经全主机唯一 flock 租约串行;子代理审批经 FD 转发回父进程,权限不可能超过父级
- **审批门**:「替我审批」三档;审批卡编辑参数时 freeze 暂停倒计时;5 分钟超时 **fail-closed**;danger 操作不可被记住为永久授权;主/子代理双卡片车道防堵死
- **记忆系统**:用户级/项目级/Agent 共享三层可写根,不引入新工具而是授权现有读写工具;注入的清单标注"数据非指令"防提示词污染

<a id="platform"></a>
## 10. 平台能力

- **多用户多租户**:PBKDF2 账号 + 个人 token + admin;每用户独立工作区根/影子仓/记忆;会话归属由目录权威记录,列表/搜索按归属过滤——可直接自托管
- **视觉多模态**:visionprobe 用三张已知答案图片**实测判卷**(乱猜概率 1/64 才判通过);纯文本模型自动把图降级为描述,可配 vision helper 代看
- **成本核算**:不内置任何模型价格——无价格配置时预算直接拒绝执行而非静默放行;Usage 区分累计账单/上下文占用/协议开销,缓存计费防双计
- **会话全文搜索**:SQLite FTS5 trigram 分词,**中文可搜**;索引与目录自愈对账
- **大输出落盘**:超限工具输出按内容寻址落盘(去重、稳定前缀),模型可 grep/分页续读;保留天数策略自动清理,另附只读浏览 API

<a id="integration"></a>
## 11. 集成与形态

- **MCP 双向**:既接入外部 MCP 工具,又把每个 Agent 暴露为独立 MCP 端点(`/mcp/agents/{id}`);`include_tools` 白名单控制发布面,token 三态显式(空 = 明示免认证)
- **REST API 与 CLI**:262 条路由覆盖会话/工作流/审批/调度/观测,带 SSE 事件流;CLI `-p -mode json` 输出 JSONL 事件流,与 Web 前端**同一份 wire 契约**——可被外部程序编排驱动
- **观测**:OTLP 导出兼容 `OTEL_*` 标准变量;W3C traceparent 贯穿 MCP 调用链;skill 观测按 discovery/load/execute 分阶段,多进程共享 SQLite 租约安全
- **媒体中继**:主 web 端口同时服务 HTTP 与 TURN/TCP,一次性凭据精确到端口白名单——内网穿透实时观看容器内浏览器画面,无需公网 worker
- **桌面端(实验性)**:Electron 桌面即浏览器执行器——站点以真实 Chromium 呈现,Agent 的 CDP 直接附着同一页面,人机共用。**现阶段不完善:仅 macOS、ad-hoc 签名未公证,Windows/Linux 未验收,请勿作生产依赖**
- **附件与变更**:五类分组门禁在门口拒绝不明类型;预览覆盖图片/PDF/音视频/xlsx 表格到 Office 编辑;无 git 的工作区也有首触基线变更视图(会话/工作区双 scope diff)

## 安全设计

审批门(浏览器/命令/写入类操作 fail-closed 超时)、Skill 沙箱(bwrap + seccomp + 双向 Schema 门)、workflow 代码节点(bwrap/Seatbelt 硬沙箱,不支持即拒绝执行)、浏览器动作封闭 schema 重建未知字段不透传、`html:false` 的 markdown 渲染、多用户账号体系(PBKDF2 密码 + 个人 token + 会话隔离)、大输出落盘(spill)保留策略自动清理。

## 快速开始

```bash
go build -o pogo . && ./pogo -web -listen 127.0.0.1:7777   # 源码运行
docker compose up -d --build                                # Docker,http://127.0.0.1:19777
```

首次启动需要 `~/.pogo/providers.json`(缺省会报最小示例);浏览器能力另起 `compose.browser.yml`。平台现状见 [docs/project-status.md](docs/project-status.md)。

<div align="center">
  <img src="docs/images/agents-manage-docker.png" width="880" alt="Docker 部署下的管理页">
</div>

## 界面一览

| | |
|---|---|
| ![浅色管理页](docs/images/agents-manage-light.png) | ![能力继承](docs/images/agents-manage-inheritance.png) |
| ![创建与实时预览](docs/images/agent-create-light.png) | ![对话欢迎页](docs/images/chat-welcome-light.png) |
| ![设置:存储/沙箱/附件/Skill 监控](docs/images/settings-storage-sandbox.png) | ![对话工作台](docs/images/chat-workspace-live.png) |
| ![管理页:搜索筛选与导入](docs/images/agents-manage-import.png) | ![工作流列表](docs/images/workflow-list.png) |
| ![通用偏好设置](docs/images/settings-preferences.png) | ![快捷方式 Bash 高亮编辑](docs/images/shortcut-bash-edit.png) |
| ![Mermaid 图表集](docs/images/agent-rich-render.png) | ![生成图片预览](docs/images/agent-image-preview.png) |

## 联系方式

<div align="center">

**微信扫码交流 / Scan to chat on WeChat**

<img src="docs/images/contact-wechat-qrcode.jpg" width="220" alt="微信二维码">

**邮箱 / Email**: [1477746214@qq.com](mailto:1477746214@qq.com)

Bug 反馈与功能建议:[提个 Issue](https://github.com/kobaw23214/pogo/issues)

</div>

更多设计文档见 [docs/](docs/)。License 见 [LICENSE](LICENSE)。
