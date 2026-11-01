# Pogo 浏览器 Office 隔离原型

这个原型在网页中嵌入真实 ONLYOFFICE Community 编辑器。用户与 Agent 工具操作同一个打开的文档，不连接用户电脑上的 Word 或 Excel。它使用新造的测试文件，与现有 Pogo 账号、会话和文件存储分开。

用户已授权一个临时隔离的 DocumentServer 容器；没有更新现有 Pogo Docker 部署。早期原型入口为 `http://127.0.0.1:19895/`，原始编辑服务为 `http://127.0.0.1:19894/`。当前插件需要固定 `9.4.0` build `129`（`sdkjs v9.4.0.129`）的 [Pogo runtime 扩展](../office-runtime/README.md)：浏览器连接默认 `19898 → 19894` 的隔离代理，不能继续直连未扩展的 `19894` 来验收 Agent 读写与历史。代理核对原始 SDK 摘要后加载扩展，不修改容器。使用提供的 127.0.0.1 地址以保持同源绑定。

当前代理还核对 DocsAPI、Service Worker、注册脚本和 Word/Excel HTML 的原始摘要，按扩展 SHA-256 生成独立版本路径与缓存名称。ONLYOFFICE 的 `cacheFirst` 可能返回旧 SDK，因此仅设置 HTTP `no-store` 不够；新路径隔离缓存，握手还必须明确返回 `historyWithoutRead:true`，缺少字段的旧 v1 适配器也会拒绝。不会清理已有用户缓存或撤销其他版本的 worker。更新后应先保存并关闭文档，再重新加载宿主页面。

2026-10-13 的最新真实历史验证为 **44/44 通过**：Word 读取保留选区、光标和重做历史；Word/Excel 均支持程序化原生撤销与重做；Excel 首次手动编辑后无需 Agent 预读也能点 UI 撤销/重做；过期前置条件拒绝后仍能重做。详情见下方验证记录。早期 `callCommand` 读取和 Word 重做禁用的限制仅属于历史探索，当前桥没有该回退路径。

## 组成

- `public/`：浏览器工作区、真实 DocEditor、同源插件队列及保存状态。
- `plugin/`：引用 `office-runtime/bridge/` 的同一份有限 Word/Excel 插件，校验页面来源、文档、会话、操作 ID 和能力令牌。
- `../office-runtime/`：固定版本 SDK 扩展、摘要校验、隔离代理及原生历史验证脚本。
- `server.mjs`：独立宿主服务、受控队列和文件保存。
- `cli.mjs`：供 Agent 调用的 JSON 工具入口。网页按钮与 CLI 使用同一队列。
- `fixtures/`：人工文档生成和导出结果检查，不包含用户数据。
- `live-check.mjs`：真实浏览器与 CLI 联合验证，不使用编辑器替身。

测试数据、密钥、操作日志、导出文件及截图位于 `.localagent/checks/onlyoffice-prototype-20261012/`。私有运行文件不提交；日志记录操作状态和文件摘要，不记录能力令牌或模型密钥。

## 运行

需要 Node.js 20 以上、可访问的独立 ONLYOFFICE Docs、人工样本及本地插件 SDK。当前隔离服务固定为 ONLYOFFICE `9.4.0-129 / linux arm64`，精确镜像、源码版本和四份 SDK 资源摘要记录在 `../office-runtime/engine-manifest.json`。官方插件 SDK 的 canonical 文件为 `../office-runtime/bridge/sdk.js`，SHA256 为 `1bc0bd71eebcb477ceaaee3ab00ed1a10ce7eae117ec68ae8024645c435f524e`；Lab 所用检查目录的 `vendor/plugins.js` 必须与其一致。

在仓库根目录运行；仅在端口空闲且没有需要保留的编辑器时启动。先启动代理，再在另一终端启动 Lab：

```sh
node pogo/office-runtime/engine-proxy.mjs
```

```sh
OFFICE_LAB_ENGINE_URL=http://127.0.0.1:19898 node pogo/office-lab/server.mjs
```

可用环境变量：`OFFICE_LAB_DATA`、`OFFICE_LAB_PORT`、`OFFICE_LAB_PUBLIC_URL`、`OFFICE_LAB_ENGINE_URL`、`OFFICE_LAB_FETCH_URL`。上例沿用原型数据目录与 `19895`；已有窗口正在编辑时不要重启它，应使用新造样本、新数据目录和空闲端口。编辑服务需要访问宿主的文件地址；当前 Mac 隔离服务使用 `host.docker.internal:<Lab 端口>`。CLI 同样设置 `OFFICE_LAB_DATA` 读取该实例的运行信息。不要将原型密钥、合成样本路径或公开试用服务作为产品默认配置。未加载扩展或版本不匹配时插件能力为 false，读写和历史返回 `ENGINE_EXTENSION_REQUIRED`，不会静默调用旧 `callCommand`。

浏览器步骤：打开 Word，在文档中输入一句话但不保存，读取当前文档，执行测试修改，测试原生撤销 → 再读取 → 原生重做，然后保存和关闭重开。Excel 同样在单元格中输入内容，测试数量修改、公式计算及程序化原生撤销/重做。网页右侧按钮是受控验证动作，没有增加另一个模型聊天系统。

## Agent 工具

```sh
node pogo/office-lab/cli.mjs list
node pogo/office-lab/cli.mjs describe word
node pogo/office-lab/cli.mjs command word word.read '{}'
node pogo/office-lab/cli.mjs command excel sheet.read '{"sheet":"销售","range":"B2:E2"}'
node pogo/office-lab/cli.mjs save word
```

Word 替换要求从当前编辑器取得完整 `expectedText`，并唯一匹配 `search`；Excel 写入要求 `expectedInputs` 与当前范围完全一致。调用参数是数据，不接受模型生成的任意脚本。重复操作 ID 返回已记录结果，不重复执行；未知结果封锁当前会话的后续操作。

本目录保留独立 Lab 的工具桥与验证证据。Pogo 主界面集成使用同一份 canonical 插件资源，但其会话权限、文件面板及工具注册需要独立验收；Lab 的通过结果不等于产品发布，也不表示已经部署到现有 Pogo Docker。

## 唯一保存通道

```text
Agent 或网页请求保存
  → 排队 host.export
  → 当前 DocEditor.downloadAs(docx 或 xlsx)
  → onDownloadAs 返回当前编辑文件的 URL
  → 宿主核对会话与在途操作，下载并原子写入
  → 返回持久化版本
  → 关闭重开核对文件
```

直接发服务端 `forcesave` 的早期实验返回状态 4：客户端的新修改尚未发送给编辑服务。这个原型因此统一采用公开的当前状态导出接口，不保留第二条自动写回通道。普通 DocumentServer 保存回调只校验并确认收取，不覆盖宿主已保存的导出文件。[公开导出接口](https://api.onlyoffice.com/docs/docs-api/usage-api/methods/#downloadas)、[当前状态转换流程](https://api.onlyoffice.com/docs/docs-api/get-started/how-it-works/converting-and-downloading-file/#from-the-document-editor)

导出期间当前编辑器暂停输入，避免用户另发同类型下载事件。`onDownloadAs` 不带操作 ID，宿主只能把它绑定到当前唯一在途导出。文件超过大小限制、地址不属于本次编辑服务、过期会话、超时回执均不写回。仅收到操作回执或出现画布变化不会被宣布为已保存。

## 已识别的接口边界

- 固定 runtime 扩展已验证 Word/Excel 程序化原生全局撤销与重做；不承诺选择性撤销某个 Agent 操作，用户输入也属于同一全局历史。
- Excel 尚未读取或写入过观察范围时，空参数历史请求仍会执行真实撤销/重做，返回 `snapshot:null`；这不是单元格读取结果。Agent 需要核对内容时再显式读取范围。
- 当前读取和历史后的观察不进入 `callCommand` 的 BuilderScript history action；写入也由固定扩展先同步检查前置条件，再进入 SDK 原有事务。没有任意脚本、补偿写入、付费 Connector 或旧读取回退。
- 这些能力属于明确维护的固定版本 SDK 扩展，不能称为未修改 Community 的公开插件能力。SDK 升级需要重新核对摘要与入口并重跑真实历史验证。
- 独立 Lab 未验收产品级多人协作、刷新恢复、任意用户文件导入或 Pogo 账号权限集成。
- 浏览器测试的中文文本输入不代表物理中文输入法、拖动延迟或所有复杂 Office 文件已验收。

## 验证

2026-10-13 当前固定 runtime 真实验证结果保存在 `.localagent/checks/onlyoffice-history-v4-20261013-cache-version-final/verification/summary.json`：**44/44 通过**，`completed: true`，允许 Service Worker，执行前后的扩展、代理、manifest 与 canonical 插件摘要一致。覆盖 Word 未保存用户输入、选区及光标保留、撤销 → 拒绝过期写入 → 读取 → 重做；Excel 首次手动输入后、未经 Agent 读取的 UI 撤销/重做和 `snapshot:null` 回执；公式与跨工作表重算、相同历史循环、连续两步撤销与重做；两类文件导出保存后重开。最后一轮使用随机空闲代理端口，默认 `19898` 是加载方式示例，不是验收报告中的固定端口。

未扩展原始编辑服务的当前独立验证记录位于 `.localagent/checks/onlyoffice-history-no-extension-v4-20261013/verification.json`：11/11 通过，Word/Excel 能力均关闭，读写与空参数历史请求均按预期拒绝为 `ENGINE_EXTENSION_REQUIRED`，源码摘要一致，没有旧路径回退。

同来源已有缓存升级 **13/13 通过**，证据位于 `.localagent/checks/onlyoffice-cache-upgrade-20261013-final/verification.json`：仅在新造浏览器 context 中种入并实际命中旧 SDK 缓存，旧适配器的新能力握手被拒绝；同端口升级后新编辑器可首次撤销/重做，旧缓存字节和旧 worker 注册仍保留。新 worker 使用独立 scope，未靠清缓存或换来源通过。

上一版固定 runtime 的 **41/41** 与原始引擎 **4/4** 记录仍保留在 `.localagent/checks/onlyoffice-history-v2-20261013-final/verification/summary.json` 和 `.localagent/checks/onlyoffice-history-no-extension-20261013/verification.json`，它们未覆盖这次新增的首次 UI 无观察范围历史场景。

首次 UI 历史 v3 的 44/44 与 11/11 记录也保留在对应 `onlyoffice-history-v3-20261013-first-ui-final` 和 `onlyoffice-history-no-extension-v3-20261013` 检查目录。v3 阻止了 Service Worker，没有覆盖持久浏览器的缓存升级；当前版本不沿用这个假设。

以下保留 **2026-10-12 的历史验证**，结果位于 `.localagent/checks/onlyoffice-prototype-20261012/live-9/`。当时 Word `callCommand` 读取会影响重做，所以禁用了程序化重做；Excel 当时只通过真实编辑器工具栏或 Ctrl/Cmd+Z 验证撤销。上述限制已由 2026-10-13 的固定扩展验证补齐，旧记录不计作新版本的历史能力证明：

| 检查 | 结果 | 范围 |
| --- | --- | --- |
| 真实浏览器 + 外部 Agent CLI | 27/27 通过 | 当前编辑器读写、未保存的用户输入、原生撤销、公式重算、保存及关闭重开 |
| 队列、插件及保存协议 | 23/23 通过 | 来源和会话校验、重复操作、过期结果、导出地址及单次写回 |
| 实际 DOCX/XLSX 文件 | 37/37 通过，限定浅色显示条件 | 正文、测试格式、用户修改、公式与计算缓存；不代表所有 Office 文件兼容性 |

该次历史运行前后，编辑服务宿主、工具入口、插件及网页源码摘要一致。Word 与 Excel 的实际导出为 `word-edited.docx`、`excel-edited.xlsx`；保存后重新打开的画面为 `word-reopened.png`、`excel-reopened.png`。此前未完成的探索记录保留，但不计入该次通过结果。

导出文件存在一项明确的格式转换：`销售!A1` 的白色字体由 `RGB FFFFFF` 变为 `indexed 65` 系统背景色。当前浅色重开画面仍为白字；跨主题颜色保真没有验证。文件检查器默认严格模式会拒绝此差异，本次显式指定 `--render-profile light` 并在报告中保留边界。它没有把此转换视为固定颜色完全等价。[索引颜色定义](https://epplussoftware.com/docs/7.5/api/OfficeOpenXml.Style.ExcelIndexedColor.html)

2026-10-12 验证结束时，浏览器入口恢复为原始人工样本，验收导出与截图没有覆盖。后续历史验证使用新数据目录，保留旧记录。原型只复用已授权的临时 ONLYOFFICE 编辑服务容器，现有 Pogo 应用和浏览器容器未更新。

协议和文件写回边界：

```sh
node --test pogo/office-lab/server.test.mjs pogo/office-lab/plugin/contract.test.mjs
```

当前完整原生历史验收脚本会生成独立测试数据，默认启动 `19896` Lab 和 `19898` 扩展代理，复用已有隔离 DocServer，不触碰用户的 `19895` 编辑器。先确认这两个测试端口空闲，也不要与手动启动的代理同时运行；已有代理占用时用 `OFFICE_ENGINE_PROXY_PORT=0` 分配空闲端口：

```sh
node pogo/office-runtime/verify-history.mjs
```

未扩展引擎和已有缓存升级可分别复跑 `verify-no-extension.mjs`（默认 `19890`）与 `verify-cache-upgrade.mjs`（默认 `19891`、随机引擎端口）；两者均创建独立合成数据，缓存脚本只操作新造浏览器 context，不读取用户 Chrome 的运行状态。

早期基础读写保存脚本继续保留；运行它也必须通过扩展代理启动独立 Lab，使用新造的未修改样本，且仅打开一个测试页面。它不包含完整的程序化历史、选区与光标验收：

```sh
node pogo/office-lab/live-check.mjs
```

`OFFICE_LAB_CHROME` 和 `OFFICE_LAB_PLAYWRIGHT` 可指定浏览器和 Playwright 路径。每次运行记录所检代码的前后 SHA256；协议单测及接口替身检查不算真实 Office 验收。文件检查方法见 `fixtures/README.md`，应检查实际导出产物中的正文、格式、用户修改和公式缓存。
