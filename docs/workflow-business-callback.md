# 工作流业务单范围与后台回调

本次在第二阶段嵌入入口上增加业务单范围和持久后台通知，沿用原账号、动作权限、队列及 Run。以下能力通过独立临时数据与本地接收器验证，不需要 Docker。

## 业务单范围

发布入口新增 `history_scope`：

| 值 | 行为 |
| --- | --- |
| `business` | 新入口默认。换码必须携带非空、无控制字符的 `business_id`，最长 256 字节。仅能访问当前入口、工作流和业务单的 Run，再检查原动作权限。 |
| `workflow` | 原入口兼容模式。保留该账号在本工作流内原有的授权范围。旧持久入口没有此字段时在内存中按此模式加载，不重写旧文件。 |

业务系统后端从已登录身份和已获准访问的业务单取得 `subject`、`business_id`，请求 `/launch`。Pogo 将范围绑定到启动码和短时会话，iframe 的输入、查询参数和运行显示信息不能替换该范围。业务系统仍须检查当前用户能否访问该业务单。

列表先执行业务单过滤与原读取授权，再分页。详情、取消、重试、审批、产物与带 `run_id` 换码均检查同一范围。即使同一个平台账号拥有 A、B 两条运行，也不能用 A 单的嵌入会话操作 B 单。节点级授权同样不能跨单或跨入口。

旧文件缺少不可变业务归属时不自动回填，不从输入中的订单号推断，也不信任可展示的 `RunRecord.origin`。它们保持原 ID、文件和原管理页访问能力。管理员若需旧入口改用业务模式，应先确认对应历史确有目录归属；未归属记录仍从管理页查看。

同一单可以保留多次运行和显式重试链。启动和重试的幂等请求键按入口、业务单和操作隔离；失败或取消后重试仍创建新 Run，并保留原 `business_id`、入口、输入和 `retry_of`。

## 配置后台通知

空间管理员在“嵌入其他系统”面板中配置：

```json
{
  "history_scope": "business",
  "callback": {
    "enabled": true,
    "url": "https://business.example.com/integrations/pogo/events"
  }
}
```

地址是管理员配置，启动请求不能另选收件地址。使用 HTTPS，本机开发可用 loopback HTTP；不接受 URL 凭据、查询参数或片段，拒绝元数据/链路本地等目标，不跟随重定向。签名密钥与嵌入接入密钥相互独立，首次启用和轮换仅展示一次，业务接收器在后端保存。Pogo 在私有文件和数据库订阅中保存投递所需密钥，不通过普通入口读取接口返回。

- 新 Run 在受理事务中冻结回调订阅，并原子写入首个 `run.queued` 通知。
- 已存在的 Run、未订阅的历史以及未配置回调的流程不会因后来启用回调而批量补发。
- `callback.enabled=false` 暂停已有投递，保留记录和原目标；恢复同一配置后继续投递。
- 修改地址或签名密钥时，旧事件标记为 `blocked`，不会静默转发到新目标。管理员可明确选择“按当前回调配置重试”，保留原事件 ID 和正文。
- 确认重试只改变选中的投递记录。旧 Run 的后续事件仍使用其冻结目标，如不匹配仍须逐条确认；新 Run 使用新配置。
- 停用嵌入入口不会取消已受理的 Run，也不会自动停止独立回调；暂停通知使用回调自己的开关。

## 后台回调协议

Pogo 以 HTTP POST 发送 JSON，收到 2xx 才记为送达。通知采用至少一次投递：网络丢回执时同一事件可能再次到达，接收方须按事件 ID 去重；签名与时间戳用于验证来源和时效。此处理原则参考 [Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)，这里使用下面明确约定的 Pogo 请求头。

```http
Content-Type: application/json
X-Pogo-Event-Id: evt_...
X-Pogo-Timestamp: <本次投递的Unix秒>
X-Pogo-Signature: sha256=<十六进制HMAC>
```

签名算法：

```text
HMAC-SHA256(signing_secret,
    event_id + "." + timestamp + "." + 原始HTTP正文)
```

接收器先限制正文大小、确认事件 ID 与正文一致，再用原始字节验签；不要先解析、重新序列化后验签。以常量时间比较签名，并检查时间戳，例如允许前后 5 分钟。每次重投会产生新的时间戳和签名，事件 ID、事件正文及 `sequence` 保持不变。

正文示例：

```json
{
  "id": "evt_...",
  "type": "run.completed",
  "created_at": "2026-10-27T12:00:00Z",
  "publication_id": "pub_...",
  "workflow_id": "wf_...",
  "version_id": "012345abcdef",
  "business_id": "ORDER-001",
  "external_subject": "employee-42",
  "run_id": "run_...",
  "status": "completed",
  "state_revision": 7,
  "sequence": 3
}
```

事件类型包括 `run.queued`、`run.waiting_human`、`run.completed`、`run.failed`、`run.cancelled` 和 `run.interrupted`。重试 Run 的事件另带 `retry_of`。默认不附带完整业务输入、审批意见、节点结果或产物内容；这些仍由原授权接口读取。

`sequence` 在每个 Run 内单调递增；`state_revision` 是原工作流状态版本，不能替代通知序号，例如排队中取消的两个状态版本都可能为 0。Pogo 按同 Run 的顺序投递，前一事件没有送达时后续等待；不同 Run 可以分别推进。

业务接收器应在同一数据库事务内：

1. 以 `event.id` 唯一约束写入接收记录；重复事件直接确认，不重复执行业务动作。
2. 校验 `business_id` 与 `run_id` 关联，对同 Run 按 `sequence` 更新当前状态。
3. 保存业务状态或后续处理任务，事务提交后返回 2xx。

这样接收成功但 HTTP 回执丢失时，Pogo 可以安全重投。跨 Run 的业务单当前运行切换需结合 `run_id`／`retry_of` 判断，不能让旧 Run 的迟到结果覆盖新 Run。页面 `postMessage` 可更新即时显示，可靠业务写回以验签回调或服务端对账为准。

## 重试、重启与人工处理

回调事件和投递状态存放在原 `task-runs/runs.db` 的新增表中，不修改原队列记录和历史 Run 文件：

| 表 | 用途 |
| --- | --- |
| `workflow_callback_subscriptions` | 每个新 Run 冻结的目标、签名配置及状态游标 |
| `workflow_callback_outbox` | 稳定事件正文、顺序、投递租约、次数、下次时间和失败原因 |

状态落盘后同步登记通知；进程在文件和通知之间中断时，后台对订阅与权威 Run 状态重新对账。虚拟排队状态和排队取消也参与对账。重启后保留待投递任务并回收过期租约，不依赖浏览器、短时会话或前台轮询。

单次 HTTP 超时 10 秒；失败退避从 1 秒递增至最多 64 秒，累计 8 次失败进入 `failed`，记录保留。进程中断留下的投递租约最多 20 秒；接收方可能收到重复事件，因此仍需去重。前序事件 failed、paused 或 blocked 时，同 Run 后续事件继续等待。

管理员可在入口面板查看近期投递、HTTP 状态、次数、暂停/阻塞原因，并确认按当前配置重试。回调投递失败不会要求用户再次启动工作流，人工重试通知也不创建新 Run。

管理 API 沿用原空间管理员及发布入口管理权限：

| 接口 | 用途 |
| --- | --- |
| `GET /api/manage/workflows/{wid}/publications/{pid}/deliveries` | 最近投递记录，不返回签名密钥和原始私有订阅 |
| `POST .../{pid}/deliveries/{event_id}/retry` | `{ "use_current_target": true }`，明确按当前配置重试同一事件 |
| `POST .../{pid}/rotate-callback-secret` | 返回一次性 `callback_secret` |

## 独立本地验证

原 UI 夹具新增 `POGO_WORKFLOW_UI_STAGE3=1`，自动创建 A、B 两张测试业务单及独立的验签、去重回调接收器。`fixture.json` 的 `embedding` 中提供 `alice_a_url`、`alice_b_url`、`bob_a_url` 和 `callback_monitor_url`。所有用户、工作流、数据、回调地址均为隔离测试配置。

接收器第一次故意返回 503，可观察后台自动重试。输出目录的 `receiver-fail` 标记用于模拟持续不可用，删除后恢复；`receiver-drop-ack` 用于模拟接收提交成功但连接中断，验证稳定事件 ID 重投和去重。完成验收后关闭页签并写入 `stop` 标记，让夹具清理临时数据。未部署 Docker，不连接真实业务系统或外部模型。

### 2026-10-27 本地验收结果

Chrome 实际打开同一账号的 A、B 单跨来源 iframe，确认 B 看不到 A 的运行；将 A 的 Run 编号带入 B 的深链接也被拒绝。A 单审批后关闭页面，后台仍送达完成通知；模拟接收成功但回执丢失后，同一事件重投一次，接收器只处理一次。

B 单接收器持续返回 503 期间重启独立测试服务，待投递记录保留；恢复接收器后自动送达。取消、重试保持 B 单归属；修改回调地址后旧事件 blocked，通过管理页面逐条确认才转发到新地址。最终 3 条 Run 的 9 个事件全部送达，20 次投递尝试中出现 1 次已接收事件的重复回执，按事件 ID 去重成功；每个 Run 的尝试序号均未倒退。

原隔离夹具的历史完成记录、待审批记录和工作流仍可访问。后端相关回归、回调 race 检查、前端 13 文件 109 项、类型检查和差异检查通过。夹具 PASS 正常退出，测试服务和临时数据已清理。无凭据运行证据与截图位于 `.ci-artifacts/workflow-business-callback-jlMg91/`（不纳入 Git）。未部署 Docker，未修改现有服务数据。
