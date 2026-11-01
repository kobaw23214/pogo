# workflow:把工作流编排在 Agent 之上

> 2026-10-28 核对：自然语言搭建基础闭环见 [Agent 工作流搭建](workflow-authoring.md)，统一权限和 Run 语义见 [第一阶段说明](workflow-stage1.md)。本文保留原引擎格式说明；执行能力边界以当前预检与上述专题为准。

## 当前执行边界

- 节点与动态分派已检查 Agent 的 `Err/EndReason`，缺失结束事件、模型异常和未完成结束不会再被当作正常成功。
- Run 持久化失败已传播为错误并阻止继续执行；排队受理、SSE 展示和最终成功应分别判断。
- 节点复用了旧式 Skill/MCP 的选择逻辑，但未接入普通会话完整的 Skill Runtime 绑定、锁、运行工具和 workspaceData 物化。新的草稿试跑会明确检查这些能力，不将“资源可见”当成“可以执行”。
- 发布快照固定流程图，不固定全部引用 Agent 的配置；节点执行/恢复仍读取 Agent 定义。完整依赖版本锁定留待后续阶段。
- human 节点与 `approval: true` 是流程级人工决策，不替代逐工具审批。新试跑不会把 MCP/声明工具的 `readOnlyHint` 当授权；不支持的能力明确报错，旧发布流程保留原行为。

## 它是什么

Agent Studio 管"创建一个 agent",workflow 管"把几个 agent 编排成一条流水线"。一个工作流由若干**节点**组成:agent 节点引用一个已有的 Agent 定义并给它一段只在这一个环节生效的任务;human 节点挂起等待人工决策;orchestrator 节点在运行时动态分派候选 agent。

节点之间不发明新的协作协议:**产物走文件**。所有节点共享本次 run 的工作目录,上游写下的文件就是下游的输入;任务文本里还可以用 `{{ }}` 模板直接引用上游产物的路径与摘要。

## 定义格式

草稿存 `SessionDir/workflows.json`(克隆 Agent Studio 的存储语义:原子写、损坏拒启、`.initialized` 标记、revision 乐观锁)。工作流:name 1–60 字符、description ≤240、nodes 0–50 个,注册表上限 100 个。**草稿允许零节点**(创建后再到画布上编排);"至少一个节点"的可运行校验在发布与运行时才强制执行。

一个节点:

| 字段 | 适用 | 说明 |
|---|---|---|
| `id` | 全部 | `^[A-Za-z0-9._-]+$`,全图唯一 |
| `kind` | 全部 | `"agent"`(默认)/ `"human"` |
| `agent` | agent | 引用的 Agent 定义 ID(保存时校验活目录) |
| `task` | agent | ≤4000 字符,本环节任务,支持 `{{ }}` 变量 |
| `after` | 全部 | 依赖列表,元素为 `{node, when}`;`when ∈ success(缺省)/failed/rejected/always`。**不许成环**(Kahn 判环);兼容写法 `"after":["a"]` 等价 `[{node:"a"}]` |
| `expect.path` | agent | 声明的产物文件(workspace 相对路径);节点结束时校验存在,缺失算节点失败 |
| `tools` | agent | 内置工具白名单，无独立选择数量上限；对 Agent 自身 tools 只收窄不放宽，仍校验工具名并去重 |
| `model` / `max_turns` | agent | 本环节模型覆写 / 轮次上限(≤100) |
| `approval` | agent | `true` 时节点 agent 跑完后进入人工审批,批准才记 success |
| `orchestrate` | agent | `{candidates: [{agent, hint}], max_dispatches ≤20(默认5), max_parallel ≤4(默认1)}`——见「动态分派」 |
| `prompt` | human | 审批展示的 Markdown,支持同一套 `{{ }}` 插值 |
| `fields` | human | `[{key, label, type: text\|select, options?, required?}]` 人工填写项 |
| `actions` | human | 决策按钮,缺省 `["approve","reject"]` |
| `timeout` | human | 等待上限(默认 72h) |
| `on_timeout` | human | 超时处置:`reject`(默认)/ `fail` |

## 草稿、发布与版本

**run 只从已发布版本启动**——草稿(workflows.json)是编辑面,发布把草稿固化为不可变快照:

- `POST /api/manage/workflows/{wid}/publish` `{name, desc}` → 发布当前草稿;发布前做可运行校验(零节点拒发,400 "无法发布:…");快照写 `workflows/versions/{wid}/{hash}.json`(version_id = 规范化 JSON 的 sha256 前 12 位,同内容重复发布幂等),指针原子写入 `workflows/published.json`。
- `GET .../versions` 列版本(新→旧,含 current 标记);`POST .../rollback` `{version_id}` 把指针回指旧快照(快照不可变,回滚只是一次指针移动)。
- 列表/详情附 `published`(指针或 null)与 `dirty`(草稿哈希 ≠ 已发布哈希)。未发布的工作流启动 run 返回 409。
- run 记录里嵌整个工作流发布快照,后续工作流草稿改动不影响该快照；其中引用的 Agent 仍按 ID 动态读取，Agent 配置变化不受此保证覆盖。
- v1 迁移:打开存储时对 `status=="ready"` 且无指针的工作流自动生成首个发布快照(版本名 "migrated");`status` 字段已退役,旧文件读取时容忍忽略。

## 运行语义

**调度**:就绪队列 + 并发上限 4 的 worker 槽;三条规则——有上游未终态则等;有 active 边则就绪;全部上游 skip 则本节点 skipped 并级联(`skipped_by` 记录元凶)。fan-in 是静态 barrier:所有 `when:success` 上游都成功才放行。多个节点 after 同一节点即并行 fan-out,无新语法。

**失败**:**fail-stop 而非抢占**——节点 failed 后不再调度 success 路径的新节点(已在跑的跑完,不杀半途写文件的 agent);`when:failed` / `when:always` 边是逃生通道(错误处理分支)。run 终态:有 failed → `failed`,否则 `completed`;ctx 取消 → `cancelled`;整段执行超时(默认 30 分钟,挂起时段不计)→ `failed`。

**变量**:task/prompt 里 `{{ nodes.<id>.artifact }}`(上游 expect 产物路径)、`{{ nodes.<id>.summary }}`(收尾摘要)、`{{ run.workspace }}`。保存时静态校验(引用必须存在、在 after 传递闭包内、artifact 引用要求对方声明 expect);装配时求值,失败即节点 failed 并指明引用。每个节点求值结果记入 `inputs`,随 run.json 持久化——run 记录自解释"节点当时看到了什么"。

**动态分派**:`orchestrate` 节点的工具集上会追加合成工具 `dispatch_to_<候选agent>`(hint 即工具描述)。模型用 `task` 入参(≤2000)派发,候选以 fresh agent 跑完即弃,`{summary, artifact, usage}` 作为工具结果回流——agent-as-tool 而非控制权移交,对外仍是一个普通节点(有 expect、失败照层级联)。`max_dispatches` 防死循环;`max_parallel>1` 并发派发(候选间 expect 路径分目录防写冲突)。每次派发记入 `dispatches` 并发 `workflow_dispatch_start/end` 事件。

**人工节点与审批**:执行到 human 节点时渲染 prompt、把节点置 `waiting_human` 后**挂起即退出 goroutine**(不占内存,run 保持 working);`POST .../runs/{rid}/nodes/{nid}/resume` `{action, fields}` 后从 run.json 重建续跑,人工输入落 `_human/{节点id}.json` 成为该节点产物。`approve` → `success`;`reject` 与超时(reject)→ `rejected`(中性终态:不算失败、不触发 fail-stop,仅 `when:rejected` 边放行);`on_timeout: fail` → `failed`。超时由后台 sweep(每分钟)处置。agent 节点 `approval: true` 复用同一机制:agent 跑完先挂起,批准才记 success。

**恢复**:进程重启时,真有执行中节点的 run 标记失败("process restarted");挂起在等待人工的 run 原样保留,重启后可正常 resume;两者皆非的 pending run 自动续跑。

**状态存储**:run 记录(`wf-runs/{runID}/run.json`)在状态迁移时原子重写，作为恢复依据；SSE 流(`.../runs/{rid}/stream`)提供首帧快照与 `?from=` 续传。写入失败会传播并阻止继续执行，不把未落盘的进展视为已完成。

**定时触发**:`POST /api/manage/workflows/{wid}/schedule` `{kind: cron|every|at, ...}`(一条一换,DELETE 移除)。触发经 schedule 的 RunOnce 簿记(防重叠/历史/连续失败停用),进程内跑**已发布版本**;无发布版本计失败;run 挂起等待人工不算失败。触发历史在 `GET /api/schedule/jobs/{id}/runs`(job_id = workflow id)。

## 状态机

run:`working → completed / failed / cancelled`(挂起是 working 的一种,不是独立态)。
node:`waiting → running → success / failed / skipped / cancelled / waiting_human / rejected`。
dispatch:`running → success / failed / cancelled`。
`skipped` 与"未到达"是两个语义;`cancelled`/`rejected` 是中性终态,不用失败色。

## API 一览

```
GET    /api/manage/workflows                  列表(附 published/dirty/schedule;非 admin 脱敏)
POST   /api/manage/workflows                  创建(admin)→ 201
GET    /api/manage/workflows/{wid}            详情(同上视图)
PATCH  /api/manage/workflows/{wid}            更新(admin,带 revision;冲突 409)
DELETE /api/manage/workflows/{wid}            删除(admin,清发布指针)→ 204
POST   /api/manage/workflows/{wid}/publish    发布(admin){name, desc} → 201
GET    /api/manage/workflows/{wid}/versions   版本列表
POST   /api/manage/workflows/{wid}/rollback   回滚 {version_id}
POST   /api/manage/workflows/{wid}/schedule   设置/替换定时(admin)
DELETE /api/manage/workflows/{wid}/schedule   移除定时(admin)
POST   /api/manage/agents/import              md 角色卡导入(admin){markdown} → 201;同名 409
POST   /api/workflows/{wid}/runs              启动 run(未发布 409)→ 201 {id}
GET    /api/workflows/{wid}/runs[/{rid}]      run 列表 / 记录
POST   /api/workflows/{wid}/runs/{rid}/cancel 取消
POST   /api/workflows/{wid}/runs/{rid}/nodes/{nid}/resume  人工决策 {action, fields}
GET    /api/workflows/{wid}/runs/{rid}/nodes/{nid}/artifact 节点产物文件
GET    /api/workflows/{wid}/runs/{rid}/stream SSE(快照 + 增量,?from= 续传)
```

md 角色卡导入:`{markdown}` 解析 YAML frontmatter(`name` 必填 / `description` / `model` / `tools` 逗号分隔)+ 正文进 instructions——兼容 Claude Code subagent 卡片格式,可直接导入社区角色卡。

## v2 明确不做(v3 候选)

版本 diff 视图、自动保存版本、orchestrator 的 handoff(控制权移交)模式、动态 Send/Loop 节点、resume URL 外部化、多人会签。
