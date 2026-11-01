# ONLYOFFICE 插件桥（隔离原型）

此插件连接**当前正在打开的真实 ONLYOFFICE 文档**，不打开用户电脑的 Office，不包含聊天模型，也不接收任意 JavaScript。canonical 资源位于 `pogo/office-runtime/bridge/`，本目录入口引用同份文件。它现在要求明确的固定版本 SDK 历史扩展；引擎边界、版本、许可与加载方式见 [office-runtime 说明](../../office-runtime/README.md)。不属于未修改 Community 的官方完整能力。

当前要求 ONLYOFFICE `9.4.0` build `129`（`sdkjs v9.4.0.129`），镜像与原始 SDK 摘要由 `office-runtime/engine-manifest.json` 固定。隔离浏览器通过默认 `19898 → 19894` 的 runtime 代理加载扩展；插件直连原始 `19894` 时读写与历史全部拒绝。代理不修改现有 DocServer 容器，也不代表正式 Docker 发布。

## 安装与身份

Lab 主页面与插件资源必须同源；ONLYOFFICE Document Server 可以是另一个来源。主页面使用 `editorConfig.plugins.pluginsData` 加载动态 `config.json`，并通过 `autostart` 启动 GUID `asc.{A6691D12-9A8F-4A33-91D4-548D3BB68D01}`。Lab 服务应复制本目录的配置，将 `baseUrl` 设为同源的会话路径，将 variation `url` 固定为 `index.html`：

```
/plugin/session/<sessionId>/<token>/
```

每个资源请求先由 Host 校验路径中的活动 sessionId 与 token，同目录 `scope.js` 生成 `window.__pogoOfficeLabScope = {sessionId, documentId, token, editor}`。Word 的 editor 是 `word`，表格是 `cell`。Token 至少 32 字符，服务端随机生成；只用于当前隔离会话，不写入文件。插件只接受 `window.top` 发送、`event.origin === location.origin`、身份与 Token 均匹配的消息。Host 必须校验来源、Document/Session/Token，并将 `ready` 的 `event.source` 绑定为该会话唯一插件窗口。后续发往该窗口的 `postMessage` 也应指定准确来源，不使用 `"*"`。

真实 9.4 加载器会无条件追加 `?lang=...`，因此运行路径不使用 variation query 或 fragment 传身份。先加载同目录受控 `scope.js`，再加载 SDK 和插件；严格验证该对象全部身份字段，追加的语言与主题参数不影响身份。仅旧测试 URL 兼容 query/fragment 后备，不混合两处字段；变成 `editor=word?lang=...` 的值仍会拒绝，不能绕过准确 editor 校验。

Host 在会话路径的 `sdk.js` 提供预先下载、记录 SHA-256 的官方 `https://onlyoffice.github.io/sdkjs-plugins/v1/plugins.js` SDK 库；插件运行时只加载同源脚本。

## 消息与受控操作

```js
{
  protocol: "pogo-office-lab/v1", type: "request",
  sessionId, documentId, token,
  operationId: "a-new-unique-id", operation: "word.read", args: {}
}
```

插件初始化发送 `type: "ready"` 和 `capabilities`。响应为相同身份字段、`type: "response"`、`operationId`，以及 `ok: true, result` 或 `ok: false, error: {code,message}`。

| operation | args | 结果 |
|---|---|---|
| `word.read` | `{}` | 固定扩展只读方法读取当前正文 `text`、当前选区 `selectedText`、`paragraphs: [{index,text}]`，不进入 BuilderScript history |
| `word.replace` | `{search,replacement,expectedText,expectedCount:1}` | 同一固定扩展方法先核对全文与唯一目标，再用原生事务搜索替换，返回 `{before,after}` 正文快照；不清空段落重建 |
| `sheet.read` | `{sheet,range}` | 固定扩展只读方法读取指定工作表和 A1 范围的二维 `values`、`inputs`、`display`，以及全部 `sheets` 名称 |
| `sheet.apply` | `{sheet,range,expectedInputs,inputs}` | 同一固定扩展方法先核对当前输入，再进入原生事务 `SetValue` 写入完整矩形；`=SUM(...)` 按官方 API 作为公式输入；返回 `{before,after}`，after 在 SDK 写入和重算回调后读取 |
| `history.undo` / `history.redo` | Word `{}`；Sheet 可传 `{sheet,range}` | 明确 SDK 扩展调用同一 native UI 历史入口，返回 `{native,historyScope,canUndo,canRedo,snapshot}`；Sheet 省略时使用最近观察范围，没有范围时仍执行历史并返回 `snapshot:null` |
| `capabilities` | `{}` | 明示实际支持范围、保存方式和阻断状态 |

Word 文本上限 200000 字符；替换片段 4000 字符，只允许单个无控制符的唯一目标。表格每次最多 400 个单元格，矩阵必须与范围形状一致，值为字符串、有限数字或布尔值。空单元格用空字符串；不接受 `null` 或跳过矩阵元素。`inputs` 是单元格 `GetFormula()` 的结果（有公式时是 `=...`，否则是原始值）；`values` 是 `GetValue()`，`display` 是 `GetText()`。

## 一致性、撤销与保存的边界

- `expectedText` 与 `expectedInputs` 核对**编辑器中尚未保存的当前内容**，并在同一固定 SDK 方法事件内执行修改，校验成功之前不开始历史事务。它们不是引擎 revision，也不能侦测仅格式变化；仍需多用户冲突的额外验收。
- 同插件实例保留最多 512 个 operation ID。相同 ID/请求重发返回缓存结果；不同请求复用 ID 拒绝。在途操作不能重复执行；超时或没有可靠编辑器回调会封锁后续操作，必须重开隔离会话核对结果。刷新后的新插件实例不提供跨重启去重，Host 的持久操作日志应拒绝重放旧写入。
- Word/Excel Undo 是编辑器的**全局最后一步**，用户后续输入也属于该历史；它不表示“选择性撤销某个 agent 操作”。观察范围也不限制真正撤销范围。跨工具操作的分组没有承诺。
- Excel 新编辑器中用户直接输入后，无需先运行 Agent 读取才能撤销。`history.undo/redo` 的空参数在没有既有观察范围时返回 `snapshot:null`，只确认真实历史与 `canUndo/canRedo`；不伪造单元格快照、不自动猜选区。Agent 核对内容仍可显式传 `{sheet,range}` 或另发 `sheet.read`。
- 未修改的 9.4 `callCommand` 只读也开始 BuilderScript history action，真实验证发现它会使 Undo 后的 Redo 失效。现在 `word.read`/`sheet.read` 和 history 后观察只通过固定 `PogoOfficeRead/PogoOfficeHistory` 扩展方法，避免该读取路径。未扩展或版本不匹配时能力为 false，read/edit/history 返回 `ENGINE_EXTENSION_REQUIRED`，不能静默回退到旧路径。
- 2026-10-12 探索期间因此关闭了 Word 程序化重做，Excel 仅用原生工具栏或键盘撤销。该历史限制保留为研究记录，已由 2026-10-13 的固定扩展真实验证补齐；当前 Word 与 Excel 的 `nativeUndo` / `nativeRedo` 均在扩展握手成功后开放。拒绝过期写入也不会先创建历史点、清除 redo。
- 未修改 Community 的公开表格插件 API 没有 native Undo/Redo。现在的扩展只委托 SDK 原有 native history 入口，不复制原生实现，不用“重写旧文件”冒充撤销，也不调用付费 `GroupActions` 或 Automation Connector。升级必须核对固定 SDK hash 并重新验收。
- 保存只有一条路径：Lab Host 调用公开 `docEditor.downloadAs` 导出当前编辑状态，接收 `onDownloadAs`，校验会话、在途 `host.export` 操作与文件地址后下载并持久化。插件操作成功只表示当前编辑器完成了修改，**不表示文件已保存**；只有导出产物持久化、重新打开和内容核对才能证明保存成功。Document Server 的自动回调只核对签名并 ACK，不再写回文件，避免形成第二保存通道。能力字段为 `save: "host-download-as"`。

## 当前真实验证

`.localagent/checks/onlyoffice-history-v3-20261013-first-ui-final/verification/summary.json` 记录最新 **44/44 通过**：Word 读取保留用户选区和光标，撤销 → 过期写入拒绝 → 读取 → 重做；Excel 首次原生输入后、未曾 Agent 读取时点击 UI 撤销/重做，真实公式栏内容正确且回执 `snapshot:null`；程序化原生历史连续循环与两步撤销/重做、未保存输入保留、公式及跨工作表重算；两类文件保存后重开。扩展、代理、manifest 与插件源码在运行前后摘要一致。测试使用可信 Chromium 输入，未测物理输入法性能或全部复杂文件兼容性。

未扩展引擎的最新拒绝验证保存在 `.localagent/checks/onlyoffice-history-no-extension-v3-20261013/verification.json`，11/11 通过，Word/Excel 能力关闭，读写及空参数历史都拒绝且没有 `callCommand` 回退。上一版 41/41 历史与 4/4 负向记录仍保留在 `onlyoffice-history-v2-20261013-final` 和 `onlyoffice-history-no-extension-20261013` 检查目录。

复跑完整历史验证使用 `node pogo/office-runtime/verify-history.mjs`；默认占用独立 `19896` Lab 和 `19898` 代理，已有实例时先选择空闲端口，勿覆盖用户的 `19895` 窗口。`verify-no-extension.mjs` 独立核对未扩展引擎的拒绝行为。运行方式与维护边界见 runtime README。

## 核实的官方资料

- [插件 callCommand 与 Asc.scope](https://api.onlyoffice.com/docs/plugins/interacting-with-editors/overview/how-to-call-commands)
- [Word SearchAndReplace](https://api.onlyoffice.com/docs/office-api/usage-api/document-api/ApiDocument/Methods/SearchAndReplace)
- [表格 SetValue 的公式示例](https://api.onlyoffice.com/docs/office-api/usage-api/spreadsheet-api/ApiRange/Methods/SetValue)
- [Word 原生 Undo](https://api.onlyoffice.com/docs/plugins/interacting-with-editors/document-api/Methods/Undo)
- [表格公开插件 API](https://api.onlyoffice.com/docs/plugins/interacting-with-editors/spreadsheet-api)
- [GroupActions 的商业版限制](https://api.onlyoffice.com/docs/plugins/interacting-with-editors/document-api/Methods/StartAction)
- [公开 downloadAs 方法](https://api.onlyoffice.com/docs/docs-api/usage-api/methods/#downloadas) 与 [从编辑器导出当前状态](https://api.onlyoffice.com/docs/docs-api/get-started/how-it-works/converting-and-downloading-file/#from-the-document-editor)
- 实际引擎版本：[`sdkjs v9.4.0.129` / `75b53d504bfd81bfa35d59bb57dd9e56f7c9235d`](https://github.com/ONLYOFFICE/sdkjs/tree/75b53d504bfd81bfa35d59bb57dd9e56f7c9235d)，镜像及四个 SDK asset 的精确 hash 见 `office-runtime/engine-manifest.json`。源码依据与真实验证分别保存，编译或 transport 单测不代表引擎验收。
