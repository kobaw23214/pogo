# 主界面 Office 执行链路验证

此命令启动独立的 Pogo Web 服务，使用真实 Agent loop、`docs_office` 工具、主界面和 ONLYOFFICE 编辑器。模型输出是确定性的测试计划，不连接外部模型；结果不代表模型自主判断或复杂 Office 文件兼容性。

只能载入 `office-lab/fixtures` 生成器的两个人工样本，新工作区与正式账号、文档和会话完全分开。已有隔离目录内的文件不会在启动时覆盖。

需要固定版本 Office 适配服务、独立 Vite 开发服务及显式 Office 配置。示例路径中的检查目录应每次新建：

```sh
node office-runtime/engine-proxy.mjs
npm --prefix web/ui run dev -- --host 127.0.0.1 --port 19899
```

在 `pogo/` 目录另开进程：

```sh
OFFICE_CHECK_DATA=/absolute/new/check-directory \
OFFICE_CHECK_FIXTURES=/absolute/synthetic-fixtures \
OFFICE_CHECK_DOCS_SKILL=/absolute/pogo/skills/builtin/docs \
POGO_OFFICE_SERVICE_URL=http://127.0.0.1:19898 \
POGO_OFFICE_PUBLIC_URL=http://127.0.0.1:19897 \
POGO_OFFICE_FETCH_URL=http://host.docker.internal:19897 \
POGO_OFFICE_JWT_SECRET_FILE=/absolute/private/docserver-jwt \
go run ./cmd/office-integration-check
```

通过 `private/runtime.json` 中的本次测试凭据访问服务。创建一个明确选中 `docs` 技能的测试 Agent，并把会话工作区设为 `.`。测试程序向该会话发 `OFFICE_CHECK:word:<unique-id>` 或 `OFFICE_CHECK:excel:<unique-id>`；不要向正式 Pogo 会话发送这些测试指令。

测试模型调用工具打开文档，等待主界面真实插件连接。Word 在当前画布中输入 `POGO_WORD_USER_EDIT_001` 标记；Excel 在销售表 E2 输入 `POGO_EXCEL_USER_EDIT_001`。模型观察到未保存输入后才修改指定正文/数量，再依次执行原生撤销、读回、重做、读回和保存。必须额外核对实际保存文件、关闭重开、公式以及截图；仅模型最后输出 `OFFICE_SCENARIO_OK` 不足以证明文件验收。

服务默认绑定 `127.0.0.1:19897`，可用 `OFFICE_CHECK_ADDR=127.0.0.1:<独立端口>` 与 `OFFICE_CHECK_DEV_PROXY=http://127.0.0.1:<Vite端口>` 指定新的隔离端口。两个配置均拒绝非 loopback 服务。运行数据只落在指定新目录；它不重建或发布 Docker，不使用真实模型凭据。

Office 编辑器由 Pogo 的 `/office/editor/` 同源网关加载。`POGO_OFFICE_SERVICE_URL` 指向带固定 runtime 适配的内部 Office 服务；浏览器不使用这个地址。单端口验证时应关闭 Vite HMR 或将 HMR 也代理到 Pogo，记录客户端 HTTP 与 WebSocket 地址，并核对保存后实际导出文件。

需要真实进程重启验证时，可设置 `OFFICE_CHECK_TOKEN_FILE=$OFFICE_CHECK_DATA/private/check-token`，只允许这个固定隔离路径。首次生成 0600 的测试凭证，后续启动读取它；不读取正式 Pogo 凭证，也不会将凭证输出到终端。

保存并重开后，将 `word-reopen.json`、`excel-reopen.json`、`exports-check.json` 与源码身份快照放在该目录的 `verification/` 中，再运行 `OFFICE_CHECK_DATA=/absolute/check-directory node pogo/cmd/office-integration-check/verify.mjs`。当前验证器检查 `OFFICE_CHECK:word:run2b` 与 `OFFICE_CHECK:excel:run2b` 两个场景的实际工具记录；每次使用新的隔离目录，避免操作 ID 复用。界面输入、首次无预读撤销/重做及截图另行记录，不能以模型响应代替。
