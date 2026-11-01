# 在线 Office 操作合约

## 在线编辑（配置了 Office 服务时）

Web 会话配置了 Office 且选中本技能时，额外提供 `docs_office` 工具（目录名
`docs.office`）。它在 Pogo 文件界面打开真实的 .docx/.xlsx 编辑器，读取用户
尚未保存的内容，不操作用户电脑上的 Office。没有此工具时，以上解析命令
仍可用，但不要声称能修改当前在线编辑器。

1. `action:list` 查当前文档；或 `action:open,path:<工作区路径>` 打开文件。
   list/open 返回的 session `id` 才是下一次请求的 `document_session_id`。
   同时打开 Word 和 Excel 时，按用户指定文件的 `path/title` 和 `editor`
   匹配（Word=`word`，Excel=`cell`），检查 `ready`、`blocked` 与
   `supportedOperations`；不能因为当前界面显示 Word 就把 Excel 操作发给它，
   也不能复用上一次操作另一份文档的 ID。不猜 ID、不跨账号或其他会话操作。
2. `action:command` 调用 `word.read` 或 `sheet.read`，读取当前编辑器的内容。
   `word.replace` 优先使用 `expectedReadId`：取同一文档 session 的成功
   `word.read` 回执顶部 `operationId`，不是模型工具调用的 ID。宿主从这条回执
   取出准确全文（包括末尾换行），在编辑器中原子核对后替换唯一 `search`。
   不要让模型复述整份正文。兼容参数 `expectedText` 必须逐字符完整，且不能
   与 `expectedReadId` 同时提供；`expectedCount` 固定为 `1`，省略也只替换一个
   唯一目标。`replacement` 必须明确提供字符串；明确的空字符串才表示删除。
   `sheet.apply` 要求指定范围的 `expectedInputs` 与 `inputs` 完整矩阵。
   只修改用户要求的目标，保留其他内容和用户尚未保存的输入。
3. 写入、`history.undo`、`history.redo` 和 `action:save` 都使用新的稳定
   `operation_id`。同 ID 只用于完全相同请求的查询或重试，不能改参数后复用。
   `PRECONDITION_FAILED` 是确定拒绝，保证未写入：重新读取当前内容，核对
   用户的新输入，再用新 ID 发出条件更新后的修改。失联、超时或结果未知时
   先检查状态，必要时仅以同 ID 重试原请求，不用新 ID 重放可能已成功的操作。
   `INVALID_READ_REFERENCE` 表示没有可用的同 session 成功 Word 读取回执；
   重新读，使用返回的新 `operationId` 与新的修改 ID。
4. 每次修改、撤销或重做后再次读取当前编辑器，核对目标结果和用户的其他输入。
   撤销/重做是编辑器的全局历史，可能涉及用户修改；它不是选择性撤销某个
   Agent 操作。如果最后一步已是用户输入，不能声称撤销的是自己的改动。
5. **默认由 Agent 完成保存**：本次任务的修改、撤销或重做全部核对后，由你
   对每份实际修改的文档调用 `action:save`，使用新的稳定 `operation_id`。
   修改成功仅表示画布已变更；只有 `save` 返回成功且宿主确认持久化版本，
   才能报告“已修改并保存”。同一批连续修改在核对后统一保存，不必每改一格
   都保存。保存的是当前共享文档，须保留用户人工输入；不能覆盖成磁盘旧稿。
   用户明确要求“暂不保存/只预览修改”时遵守并说明未保存；纯读取/分析不保存
   只有人工未保存输入的文档。已确认用户保存了同一批改动时，无需重复保存。
   保存失败、失联或结果未知先检查状态；只以相同 ID 核对原保存请求，不无限
   重试或误报成功。无法确认时说明内容仍在编辑器、工作区保存未确认。
   `action:close` 不丢弃未保存修改。不改写磁盘旧文件来冒充撤销或保存。

`list`、`open`、`save`、`close` 不接受 `operation` 或非空命令参数；
`args` 应省略，也可传空对象 `{}`。明确的 `null` 同样按省略处理。
只读命令可省略 `operation_id`，由系统生成新 ID；需要重新读取当前状态时
不要复用旧读取 ID，否则幂等缓存会返回旧快照。前置条件失败后必须取得新的
读取回执，再用新的修改 ID 提交更新后的条件。

`word.read`、`sheet.read` 与历史能力以当前会话的 `capabilities` 为准。
在线编辑暂限 .docx/.xlsx；.doc/.xls、宏及其他格式沿原来的只读解析边界处理。

### Python 批量修改与页面同步

需要加边框、批量调整格式、清理数据、删除整列、调整多段结构等画布命令未支持的操作时，可使用
Python 处理。脚本是正常的执行方式，但已有文件须经 Pogo 文件事务发布，
不能直接覆盖原文件。上传、浏览器下载、Office 打开或批处理登记的 Word/Excel
原文件受命令和写文件工具保护；这些保护不禁止读取或生成新的输出文件。

**首选单次调用 `transform`**：提供准确 `path`、稳定 `operation_id` 和 Python
`script`，可选 `timeout`（最多120秒，省略为120秒）。脚本从 `sys.argv[1]`
取得 Pogo 提供的副本路径，只修改并保存该副本，不拼接原文件路径，也不运行
安装命令。Python 在命令沙箱执行，使用原有 Bash 权限和审批；有 Office 工具
不等于有脚本权限。文件未在编辑器打开时也可处理，不必先创建编辑窗口。

以下示例的准确表名、目标格与原值必须来自本次读取，不得猜测：

```json
{"action":"transform","path":"uploads/结果.xlsx","operation_id":"format_result_001","script":"import sys\nfrom openpyxl import load_workbook\nfrom openpyxl.styles import Font\npath = sys.argv[1]\nwb = load_workbook(path)\nws = wb['高危IP列表-结果']\nassert ws['A1'].value == 'IP地址', '目标表头已变化'\nws['A1'].font = Font(bold=True)\nwb.save(path)"}
```

Pogo 会先冻结并保存已有画布中的人工输入，再在隔离副本执行；脚本正常结束
且格式、原文件版本校验通过后提交。脚本不能写工作区原文件。`state:committed`
且 `result_published:true` 才表示脚本结果已发布，以 `disk_version` 为持久化版本。
副本执行成功不等于原文件已保存；已提交也不等于画布已同步。已打开的文件须
等返回的新版 session ready 后再读取核对。库可能不保留其不支持的 Office 特性，
不能仅凭退出码或版本差异宣称格式完全无损。

同一 `operation_id` 只能重试完全相同的脚本、路径与超时。结果未知时查
`batch_status`（如已返回 `batch_id`）或重试原 `transform`；运行结果不明时
不会自动重跑脚本，不能换新 ID 重放。准备阶段可能已保存人工内容，因此脚本
失败只表示脚本结果未发布，不能声称文档完全没有发生保存。

**高级分步事务**：脚本较大或确需委派给有命令权限的子代理时，保留以下方式。
父会话管理事务，子代理只处理副本，不能覆盖原文件：

1. `docs_office action:batch_prepare,path:<准确工作区文件路径>,operation_id:<稳定ID>`。
   服务端会确认当前画布已冻结并保存，再返回 `batch_id`、`staged_path` 和
   `base_version`。未确认、超时或拒绝时，不执行后续文件修改。
2. 用正常的命令工具运行 Python，**只读取和修改返回的 `staged_path` 副本**。
   核对表名、目标数据、公式/格式与非目标内容。`staged_path` 是绝对路径，
   直接作为 Python 参数，不自行拼接用户根或猜文件名。
   使用 `python-docx` 修改 Word、`openpyxl` 修改 Excel；不要重写 ZIP/XML 解析器。
   本会话有命令工具时由你直接处理，不必为执行 Python 默认派生子代理。
   没有命令工具但可以使用编辑子代理时，只把 `staged_path` 副本和修改要求
   交给子代理；父会话保留批处理身份，负责提交和核对，不把原文件交给子代理覆盖。
3. 成功后执行 `docs_office action:batch_commit,batch_id:<原batch_id>,operation_id:<稳定提交ID>`。
   服务端校验文档格式与原文件版本，以一次提交发布新版本并通知原文件窗口。
   只有 `state:committed` 和 `disk_version` 回执可作为文件已保存的依据。
4. 通过 list/返回的 session 确认新版画布 ready 后，再使用 `word.read` 或
   `sheet.read` 核对实际显示的内容。文件已提交和页面已同步是两个状态，
   不在画布尚未就绪时声称用户已经看到了更新。
5. Python 失败、取消或提交明确拒绝时，用 `batch_abort` 与新的稳定
   `operation_id` 释放该事务。结果未知时先 `batch_status,batch_id:<原ID>`，
   或重试相同提交 ID；不重新准备/重跑脚本造成二次修改。

若外部操作已经直接修改了磁盘，当前 session 会报告 `externalChange`、
`canvasVersion` 与 `diskVersion`。未保存/在途/未知画布会保留，不能强制刷新。
干净画布可用 `action:reload,document_session_id:<原ID>,expected_version:<当前diskVersion>,operation_id:<稳定ID>`
安全加载新版；必须使用返回的新 session ID，不能继续对旧画布发写入/保存。
批量替换属于新的文件版本，不能冒充原画布原生撤销历史中的一次操作。

### 文件版本与恢复

`action:versions,path:<准确路径>` 按同一文件列出已持久化的版本，覆盖原生保存、
脚本提交和版本恢复；可用 `cursor` 翻页，`limit` 最多20。版本界面也可查看
这些记录。保存发起者不是所有内容的作者：一次 Agent 保存可能包含人工输入。

需要恢复文件时先列版本并读取当前状态，再调用
`action:restore_version,path:<准确路径>,version_id:<记录ID>,expected_version:<当前磁盘版本>,operation_id:<新稳定ID>`。
`restore_side` 默认为 `after`；明确要恢复该次修改之前的内容时用 `before`。
有人工未保存内容时先保存并重新核对版本，不得丢弃；版本变化时重新读取确认，
不能盲目覆盖。恢复会产生新的文件版本，结果未知时只重试原请求。

**文件版本恢复与编辑器 Undo 不同**：恢复会替换完整文件，不能称为选择性撤销
某一处修改，也不能承诺保留该版本之后的其他输入。编辑器内的普通撤销/重做
继续使用 `history.undo` / `history.redo`。版本差异由系统生成，当前范围限于
Word 段落文本、Excel 已存储的单元格值和公式；格式、图表等不在完整对比范围，
应遵守返回的 `coverage`、`limitations` 和 `truncated`，不要用模型意图冒充实际差异。
每份文件最多保留50条版本记录，并受256 MiB总量限制，不承诺永久历史。

正常的人工未保存内容不妨碍 Agent 操作。先读取当前画布，完成修改后显式保存。
批处理准备会自动保存当前共享内容并短暂冻结，不要求用户先手动保存或关闭文档。
Office 接口失败不能证明没有画布或没有人工输入，不能据此直接覆盖原文件。

### 请求示例与错误恢复

方法名只用 `capabilities`、`word.read`、`word.replace`、`sheet.read`、
`sheet.list`、`sheet.apply`、`history.undo`、`history.redo`，不要编造 `sheet.write` 等方法。
操作参数全部放入 `args`，不要把另一层 `operation` 嵌入其中：

```json
{"action":"command","document_session_id":"<Word session id>","operation":"word.replace","operation_id":"replace_status_001","args":{"search":"项目状态：待评审","replacement":"项目状态：已通过评审","expectedReadId":"<同一 Word 的成功读取回执 operationId>","expectedCount":1}}
```

```json
{"action":"command","document_session_id":"<Excel session id>","operation":"sheet.read","args":{"sheet":"明细","range":"C2"}}
```

Excel 需要准确的工作表名，不能猜 `Sheet1`。先对正确的 Excel session 执行
`sheet.list`，`args:{}`，读取当前编辑器中的 `sheets`；它包含未保存的重命名，
不改变当前选中单元格或撤销历史。如果能力未提供，不冒充实时发现：磁盘
`docs.xlsx-info` 只能作导航，无法确认未保存表名时向用户核对。
`READ_TIMEOUT` / `READ_CANCELLED` / `READ_FAILED` 是只读调用失败，不表示文档
已经修改或整个工具失效；检查当前连接后使用新的读取 ID 再读，不复用失败快照。
写入回执不确定时，宿主自动核对原执行，不重发写入。`recovery.state:checking`
表示正在核对，list、能力查询和读取仍可使用；新的写入、保存和批处理会有界等待。
`RECOVERY_IN_PROGRESS` 时重新 list 查看状态，恢复后重新读取实际内容，按未完成的
任务继续；原未知操作 ID 的失败回执仍保留，不能把它当成没有执行而改 ID 重放。
`CONVERSATION_CLOSED` 表示旧工具实例身份失效，不是文件权限不足或没有打开画布；
报告会话连接问题并重新获取当前会话工具，不提权、不删除文件、不绕过事务覆盖原文件。
`WRONG_EDITOR`：重新 list，选择正确类型与文件的 session。
`UNSUPPORTED_OPERATION` / `INVALID_ARGUMENT`：按错误中的字段说明纠正请求；
这些宿主拒绝不会执行写入。`SHEET_NOT_FOUND`：核对工作表名，不能通过改文件、
换到另一文档或盲猜表名来绕过。对超时、未知结果仍遵循前面的同 ID 查询规则。
`UNSUPPORTED_TEXT_SHAPE`：`word.replace` 不支持换行、多段或整篇替换。保留
段落和格式，选择单段内唯一文本逐步替换，每次成功后重新读取并使用新回执；
不能删掉换行后强行匹配，也不能绕过工具直接覆盖原文件。
用户要求扩写多段测试步骤时，分别扩写现有每一段，不合并跨段。
通过该画布操作不支持新增段落、改变格式或执行任意脚本；批量需求可用上面的
受控 Python 文件事务，不能通过直接覆盖原文件绕开当前编辑和保存保护。

长文档用 `timeout 120` 包裹（文档可能畸形，解析可能卡）。


保存回执中的 `session`、`savedRevision`、`savedEpoch` 描述该操作已经提交的版本；`currentSession` 描述当前画布。重试旧保存 ID 不会保存后来输入。
