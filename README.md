<a id="top"></a>

# pogo

[![CI](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml/badge.svg)](https://github.com/kobaw23214/pogo/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kobaw23214/pogo)](https://goreportcard.com/report/github.com/kobaw23214/pogo)
[![License](https://img.shields.io/github/license/kobaw23214/pogo?color=blue)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)

用 Go 与 Vue 构建的 **Agent 工作台**。每个 Agent 都有真实的浏览器、真实的 Office 编辑、可编排的工作流和后台任务——全程对人可见、可审批、可回放。

<div align="center">
  <img src="docs/images/chat-workspace-live.png" width="880" alt="pogo 对话工作台:会话侧栏、工具调用摘要、引用来源、审批开关与上下文用量">
</div>

## 能力总览

| 能力 | 一句话 |
|---|---|
| [操作真实浏览器](#1-操作真实浏览器) | 每会话私有 Chromium,人与 Agent 看的是同一个页面,操作前先审批 |
| [编辑真实 Office 文档](#2-编辑真实-office-文档) | 在线 ONLYOFFICE 画布 + 两段式解析,写入前必须先读到回执 |
| [对话即富文档](#3-对话即富文档) | Mermaid / ECharts / 数学公式 / CSV 数据表 / callout / 脚注,流式渲染 |
| [公共信息区与 Agent 工作流](#4-公共信息区与-agent-工作流) | 跨 Agent 共享信息库;顺序 + DAG 双形态工作流,版本快照启用制 |
| [Agent 管理与嵌套快捷方式](#5-agent-管理与嵌套快捷方式) | 模板创建、多模型绑定、Skill/MCP 继承;快捷方式 5 层嵌套 |
| [Agent 自己管理 Skill](#6-agent-自己管理-skill) | 上传包 / Agent 自著 / 可执行运行时包,bubblewrap 沙箱执行 |
| [后台 Bash 与后台任务](#7-后台-bash-与后台任务) | 后台 shell 作业、后台子代理、宿主级定时/哨兵任务、持久终端 |
| [多模型接入](#8-多模型接入) | providers.json 单一配置源,OpenAI 兼容 + XML/JSON 工具协议降级 |

---

## 1. 操作真实浏览器

**实现方案**:独立的 [browser-runtime](browser-runtime/) worker(Playwright + Wayland + WebRTC)为每个会话提供**私有 Chromium 与独立 profile**——不是无头截图,而是带真实 UI 管线的有头浏览器(headed Chromium on Wayland → Pixman → VP8 → WebRTC)。Agent 通过 `browser` 工具执行 `snapshot / find / click / type / select / navigate / extract / batch / tabs / upload / calculate / devtools` 等 20+ 动作;页面按原始 URL 加载,无反代、无 iframe 包装、无跨域问题。

**优势**:
- **人与 Agent 共看同一个 Page**:WebRTC 实时画面进对话右侧面板,用户随时逐标签页接管/交还(控制权租约 + 观察代次并发控制),共享网站登录态
- **先审批后动手**:首次页面操作弹出「开启页面操作」审批卡,一次审批管一轮;拒绝即锁定本轮,子 Agent 无权自行开启
- **证据可溯源**:`snapshot/extract` 附带 `dom-evidence-v1` 结构化证据(表格原始行列、跨行跨列、多层表头,含糊处标 unknown 不展平),读取前后页面变化即拒绝发布;`calculate` 用 BigInt 精确核验表格数值(`0.1+0.2 = 0.3`),拒绝调用方自编数字
- **完整页面状态**:`devtools` 走标准 CDP 读网络请求/控制台/worker,不补发任何请求
- **文件双向闭环**:网页下载自动入工作区 `uploads/browser/`,工作区文件可回传网页文件框

## 2. 编辑真实 Office 文档

**实现方案**:内置 `office` skill 三层能力——(1) `docs_office` 工具驱动固定版本的 ONLYOFFICE 引擎,在对话面板里打开 Word/Excel 在线画布,Agent 执行 `word.read / word.replace / sheet.read / sheet.apply` 等命令,用户同一画面所见即所得;(2) 复杂批量修改走 `transform` 受控 Python 事务(python-docx / openpyxl 在受控副本上修改,校验后原子提交);(3) 未打开文件的解析走两段式:`info` 先取页数/标题树/sheet 清单(首屏 token 降一个数量级)→ 按段取正文,引用带定位锚(PDF `[p12]`、docx 块号、xlsx 行号)。

**优势**:
- **写入防盲改**:Word 写入必须携带同会话读取回执的 `operationId` 原子核对,Excel 必须携带完整 `expectedInputs` 矩阵——没读过就不许写
- **编辑器原生历史**:撤销/重做是 ONLYOFFICE 全局历史(含用户手工输入),每文件保留 50 个版本可随时 `restore_version`
- **解析有防护**:PDF 300MB/3000 页上限、扫描版如实报告不做 OCR;docx ZIP 预检防解压炸弹;xlsx 流式只读恒定内存,宏与内嵌对象不执行
- 附件上传、浏览器下载、工作区文件三条来源都能直接进 Office 链路

## 3. 对话即富文档

Agent 回复即渲染,流式输出期间代码块先展示、完成后自动成图:

| 写法 | 渲染 |
|---|---|
| ` ```mermaid ` | 流程图 / 时序图 / 状态图 / 甘特 / 思维导图 / 时间线 / gitgraph…(Mermaid 11) |
| ` ```echarts ` | 交互式图表(ECharts 6,JSON option,经安全过滤) |
| `$…$` / `$$…$$` / ` ```math ` | KaTeX 数学公式(行内 / 块级 / 围栏三种写法) |
| ` ```csv ` / ` ```json-table ` | 直接渲染成数据表,不是源码块 |
| ` ```diff ` 及多语言代码块 | 增删高亮 / lezer 语法高亮 |
| `[TOC]`、`[^1]`、`- [x]`、`> [!NOTE]`、`::: 折叠` | 目录 / 脚注回链 / 任务列表 / 五种 callout / 折叠块 |
| `[文件](路径)` | 工作区文件卡片,点击预览(图片/PDF/音视频内联) |

## 4. 公共信息区与 Agent 工作流

**公共信息区**:对话右侧是六类面板(files / hub / shortcuts / tasks / skills / workflows)。其中**信息库**是用户与 Agent 的共享事实源——文件、快捷方式、网页引用、Office 选区统一编目,同工作空间的**兄弟 Agent 看到同一份视图**;Agent 侧以只读 `information` 工具检索(list/search/read,版本化读取),用户从信息库一键插入对话。外部系统经 `/panels/<名称>/` 同源反代接入 hub 标签条;每轮回答配工作日志卡(耗时、工具调用统计、失败徽标)与来源面板。

**Agent 工作流**(重点):绑定到单个 Agent 的可复用流程,两种形态——**顺序流程**(agent / confirm / code / iteration / loop 节点)与**图流程**(追加 condition / parallel / join,边带 true/false 分支标签,静态校验无环 + 全可达 + 支配分析)。

- **运行**:持久任务队列驱动,图流程每节点独立会话与目录(`nodes/<id>`),agent 节点并发上限 10;`confirm` 节点把整个 Run 挂起等本人确认
- **封存**:输入文件按 SHA256 冻结、前序产物校验后重新物化、Agent 配置(模型/指令/Skill/MCP 指纹)快照——中途配置变化即中断,不静默重跑
- **可观测**:SSE 每 500ms 推送全节点 phase(model/thinking/tool/approval)快照;Vue Flow 画布节点状态着色、跳过分支虚线;每个节点可打开执行会话实时看 Usage 环
- **发布**:编辑走乐观并发 + 幂等;「启用」以内容 hash 存不可变版本快照,正式运行只执行 EnabledVersion;支持草稿试跑(trial)、单节点测试、链式重试
- **Agent 自治**:对话中经 `agent_workflow_read / edit / enable / run` 四个工具管理自己的流程

## 5. Agent 管理与嵌套快捷方式

<div align="center">
  <img src="docs/images/agent-create-templates.png" width="880" alt="从模板创建 Agent:空白/写作/研究/代码/规划,右侧实时预览">
</div>

- **创建与发布**:五个内置模板或空白起步,名称/简介/指令/模型/分类即时预览;草稿 → 发布两态,发布后经 `/a/{id}` 链接直达对话
- **多模型绑定**:一个 Agent 可勾选多个模型并指定默认(GLM / Kimi / DeepSeek / Qwen / 本地 ollama…);对话中随时切换,上下文用量实时可见
- **Skill / MCP 继承**:能力按 Agent 配置组合,卡片直接展示「2 Skill · 1 MCP」继承关系

<div align="center">
  <img src="docs/images/agents-manage-graphite.png" width="880" alt="我的 Agents 管理页:状态筛选、分类、搜索、卡片/列表视图">
</div>

**嵌套快捷方式**:快捷方式是 Agent 私有的可复用资料包(文本/文件/Skill/网页元素/Office 选区/**嵌套快捷方式**,有序组合)。草稿用扁平 DAG 表达,展开上限 5 层 / 512 节点 / 256 KiB,静态校验无环;首页起始卡是**实时投影**而非副本——编辑原卡即时生效,绝不留过期拷贝。Agent 侧配 `shortcut_search / read / create` 三个工具跨源检索与创建。

## 6. Agent 自己管理 Skill

<div align="center">
  <img src="docs/images/skill-management.png" width="880" alt="Skill 管理:技能库 + 调用监控,内置 browser-use / office / web-drawing">
</div>

**三种形态**:
1. **上传 Skill 包**(zip):流式解包防 zip 炸弹(2000 文件/8 层/10 倍压缩比),免重启热加载
2. **Agent 自著 Skill**:对话中用 `skill_save` 工具创建/更新(完整文件快照 + 乐观并发 + 幂等),存私有 `authored-skills`,永不混入全局目录
3. **运行时 Skill 包**:带可执行 capability 与资产(如恶意代码分析:CAPA/YARA/Ghidra + 只读样本库),安装进所有工作区之外的受信根

**沙箱执行**:每个 capability 注册为**该会话的普通工具**,实际执行由 `pogo-skill-runner` 在 bubblewrap 私有命名空间里短命运行(seccomp 放行 userns),请求/响应双向过 JSON Schema 门,超时/输出/内存/进程全有硬限;调用事件入监控存储,管理页可查。协议层还按包内二进制的 ELF 架构做平台匹配。

## 7. 后台 Bash 与后台任务

- **后台 shell**:`bash` 工具 `run_in_background` 立即返回任务 ID(`yield_after` 可先等 N 秒再转后台);配套 `shell_tasks / shell_output(按字节偏移续读) / shell_stop`,完成回执持久化,服务重启后恢复
- **后台子代理**:explore 模式子代理默认后台运行,结果以 `<task-notification>` 送达并合并唤醒,usage 精确一次计入
- **宿主级任务**:`background_task` 统一管理 `schedule`(every/at/cron + 时区 + 绝对截止线)与 `sentinel`(源观察脚本)两类持久任务——不依赖会话存活,**创建默认暂停,启用需用户明示授权**
- **持久终端**:每会话一个 pty login shell,WebSocket 附着;关标签只是 detach,重连回放 256KB backlog

<div align="center">
  <img src="docs/images/chat-tool-transparency.png" width="880" alt="执行透明:耗时与工具调用摘要、引用标记、替我审批开关">
</div>

## 8. 多模型接入

单一 OpenAI 兼容客户端(手写 net/http,支持 `reasoning_content`、缓存 token 等 schema 外字段),`providers.json` 是唯一配置源(零内置 provider,密钥只走环境变量引用)。每模型可声明兼容方言:不原生支持工具调用的模型自动降级 **XML / JSON 文本协议**(含 Qwen function 形式),vision / 上下文窗口 / 价格 / 子代理池全部可标注。

<div align="center">
  <img src="docs/images/agent-editor-models-mcp.png" width="720" alt="编辑 Agent:多模型勾选与 MCP 绑定,实时预览">
</div>

桌面与移动端同一套配置能力:

<div align="center">
  <img src="docs/images/agent-editor-mobile.png" width="360" alt="移动端:模型与 MCP 配置">
</div>

---

## 安全设计

审批门(浏览器/命令/写入类操作 fail-closed 超时)、Skill 沙箱(bwrap + seccomp + 双向 Schema 门)、浏览器动作封闭 schema 重建未知字段不透传、`html:false` 的 markdown 渲染、多用户账号体系(PBKDF2 密码 + 个人 token + 会话隔离)、大输出落盘(spill)保留策略自动清理。

## 快速开始

```bash
# 源码构建(需 Go 1.26+ 与 Node 24+)
go build -o pogo .
./pogo -web -listen 127.0.0.1:7777

# Docker 一键起(compose 内含构建)
docker compose up -d --build     # http://127.0.0.1:19777
```

首次启动需要 `~/.pogo/providers.json`(最小示例会在缺省时报给你);浏览器能力另起 `compose.browser.yml` 的 worker 服务。平台现状与已验证边界见 [项目现状](docs/project-status.md)。

<div align="center">
  <img src="docs/images/agents-manage-docker.png" width="880" alt="Docker 部署下的 Agent 管理页,多模型就绪">
</div>

## 界面一览

| | |
|---|---|
| ![浅色管理页](docs/images/agents-manage-light.png) | ![能力继承信息](docs/images/agents-manage-inheritance.png) |
| ![创建表单与实时预览](docs/images/agent-create-light.png) | ![对话欢迎页与推荐提示词](docs/images/chat-welcome-light.png) |
| ![设置:存储/沙箱/附件/Skill 监控](docs/images/settings-storage-sandbox.png) | ![对话工作台全貌](docs/images/chat-workspace-live.png) |

## 文档

设计文档与实现细节见 [docs/](docs/):[浏览器运行时](docs/native-browser-runtime.md) · [证据规范](docs/browser-evidence.md) · [Web UI 设计](docs/web-ui-design.md) · [Office 集成](docs/office-agent-integration.md) · [项目现状](docs/project-status.md) 等。

## License

见 [LICENSE](LICENSE)。
