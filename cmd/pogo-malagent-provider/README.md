# pogo-malagent-provider

恶意代码分析Skill（malware-analysis Skill）的真实能力提供者：一个 Linux/amd64 ELF
二进制以 `pogo.tool/1` 进程 ABI 提供全部 9 个 operation。协议帧、严格 JSON
（拒绝重复键/未知字段/超深/超大）、文件输入验证与契约摘要规则与
`cmd/pogo-yara-provider` 一致。

## 能力 → operation → 实现

| 契约 ID (v1.0.0) | operation | 实现 |
| --- | --- | --- |
| malware.file.hash | `file.hash` | 纯 Go：sha256/md5/sha1/size（不含模糊哈希） |
| malware.file.capabilities | `file.capabilities` | `/runtime/bin/capa --rules /assets/capa-rules --json --quiet <file>`，输出归一化为有界 capabilities + ATT&CK |
| malware.file.decompile | `file.decompile` | 直接 exec `/runtime/jdk/bin/java ... ghidra.app.util.headless.AnalyzeHeadless`（无 shell launcher），固定 postScript `PseudoCExporter.java` |
| malware.sample.search | `sample.search` | `malquery search`（family/type/tag/maxDetections/hasBinary/limit） |
| malware.sample.get | `sample.get` | `malquery sample <sha256> [--brief]` |
| malware.sample.similar | `sample.similar` | `malquery similar <sha256> [-n]` |
| malware.family.get | `family.get` | `malquery family [name] [--profile/--infra/--trend/--geo ...]` |
| malware.library.stats | `library.stats` | `malquery stats` |
| malware.library.query | `library.query` | `malquery sql`，语句先过 malquery 原版只读白名单（单条 SELECT/WITH） |

文件输入只认 `fileId`：context.files 必须与 `/fileId` 精确一一对应，路径必须在
`/input/` 下且规范，内容 sha256 必须与宿主摘要一致。库级能力只认固定数据集槽
`slotId=sample-index`、`path=/datasets/sample-index/database.sqlite`、revision 非空；
请求永远无法指定库路径。子进程全部固定 argv（无 shell）、固定最小环境
（HOME/TMPDIR=/tmp、LANG、PATH），不继承任何宿主或请求环境变量。

## 打包（主 Agent）

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false \
  -o bin/pogo-malagent-provider ./cmd/pogo-malagent-provider
```

运行时 bundle 布局（只读挂载到 `/runtime`，入口 `./bin/pogo-malagent-provider`）：

```
bin/pogo-malagent-provider   # 本目录构建产物（ELF header 校验见 skillruntime executor）
bin/capa                     # CAPA v9.4.0 standalone（与 malagent 镜像同一 vendor）
bin/malquery                 # malquery 固定版本
jdk/                         # Temurin JDK 21（jdk/bin/java）
ghidra/                      # Ghidra 12.1.2（ghidra/Ghidra/**/lib/*.jar 全部进 classpath）
ghidra-scripts/PseudoCExporter.java   # 复制自本目录 ghidra-scripts/
```

`contracts/*.json` 按字节原样打包（本目录 `.gitattributes` 已锁定 LF；digest 是文件
原始字节的 sha256）。`capa-rules` 资产挂载为 `/assets/capa-rules`。数据集由宿主按
`sample-index` 槽挂载为 `/datasets/sample-index/database.sqlite`（只读快照）。

## 宿主运行依赖（需主 Agent 确认）

- **CAPA PyInstaller 需要可执行临时目录**：standalone 单文件会把 `_MEIxxx` 解到
  `TMPDIR` 并从中 exec；当前宿主 `sandbox:local-bwrap-v1` 提供每次调用独立、
  512 MiB 的可执行 `/tmp`。provider 无权修改挂载策略。仅处理静态分析任务，
  不把用户样本当成程序执行。
- Ghidra 反编译器原生组件从只读 `/runtime` 挂载 exec（ro 挂载默认允许 exec，无需
  改动）；Java 侧 user.home / java.io.tmpdir 均钉在 /tmp（同时经 `-D` 参数与
  `JAVA_TOOL_OPTIONS` 设置），项目目录在 /tmp 临时创建、`-deleteProject` 并由 Go 兜底清理。
- 内部超时：capa 240s、malquery 60s、ghidra 由输入 `timeoutSeconds`（60–900，默认 300）
  决定。宿主 limits 的 timeoutSeconds 需高于这些值（decompile 建议 ≥ 输入值 + 30s 余量）。
- decompile 产物：`pseudo.c`（≤8MiB，text/x-c）与 `manifest.json`（≤64KiB，
  application/json）写入宿主授予的 outputDirectory，artifacts 声明为 `./pseudo.c`、
  `./manifest.json` 相对路径；成功判定要求导出器 JSON 标记 + 函数数 > 0 + 两个产物
  为普通文件且清单与标记一致，exit 0 本身不算成功。

## 测试

`go test ./cmd/pogo-malagent-provider/` 全部为假 CLI 输出/纯解析单测（不触碰真实
CAPA/Ghidra/malquery，不执行任何样本）；契约逐一过 `skillruntime/core` 的宿主文档
校验器，每种响应帧过 `process-response` schema 校验。真实引擎集成与 Docker 端到端
由主 Agent 在目标镜像中验证。
