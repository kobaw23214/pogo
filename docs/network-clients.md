# 主容器的网络客户端

主镜像同时支持 ARM64 和 AMD64，并预装以下客户端。只安装客户端，不启动 SSH、数据库或 FTP 服务。

| 用途 | 命令 |
| --- | --- |
| HTTP / HTTPS | `curl`、`wget` |
| SSH 与文件传输 | `ssh`、`scp`、`sftp` |
| PostgreSQL | `psql` |
| MySQL / MariaDB | `mysql`、`mariadb`（发行版 MariaDB 客户端） |
| ClickHouse 原生协议 | `clickhouse-client` |
| FTP / FTPS 与目录同步 | `lftp` |
| 网络诊断 | `ping`、`ip`、`nc`、`dig`、`traceroute`、`openssl` |

安装由 `scripts/install-network-clients.sh` 统一实现。ClickHouse 使用官方签名的 LTS 仓库，client 与 common-static 固定为同一版本；构建参数 `POGO_CLICKHOUSE_CLIENT_VERSION` 可选择经过验证的新版本。版本更新应重新检查目标数据库的认证与协议兼容性。

## ClickHouse 客户端兼容回退

官方二进制的指令集基线高于部分部署 CPU：aarch64 构建自 22.10 起要求 ARMv8.2 的 LSE 原子指令、自 23.5 起还要求 LRCPC（ARMv8.2 可选、ARMv8.3 强制），因此飞腾 FT-2000 系（ARMv8.0）与鲲鹏 920（有 LSE、无 LRCPC）上运行会直接 `Illegal instruction`；x86-64 构建要求 x86-64-v3（AVX2），海光/zen1 等旧核同样受影响。官方仓库不发布锁定版本的兼容构建（仅不可锁定的 master 构建），因此镜像把 `clickhouse-client` 替换为兼容壳 `scripts/clickhouse-client-compat`：

- 首次调用探测原生二进制（`--version`），结果按容器缓存在 `/tmp`；可用则原样 `exec`，行为与官方客户端完全一致、零额外开销。
- 不可用（SIGILL 或未安装）则自动把调用翻译为服务端 HTTP 接口（默认 8123/8443，`--port 9000/9440` 自动映射），用 curl 完成：查询经请求体发送，INSERT 的语句走 `query=` 参数、数据走 stdin，凭据经 `X-ClickHouse-User`/`X-ClickHouse-Key` 头（curl 配置文件传递，不进 `ps`），支持 `-h/--host、--port、-u/--user、--password、-d/--database、-q/--query、-f/--format、-s/--secure、-n/--multiquery` 与位置参数查询。其余旗标（交互模式、`--ask-password`、参数化查询等）显式拒绝（exit 64），不静默丢弃。每次回退在 stderr 提示一行原因与缺失的指令集特征。
- 回退模式输出默认为 TabSeparated（HTTP 接口默认），如需 native 默认的展示格式请显式 `-f` 或在查询中写 `FORMAT`。
- 环境变量 `POGO_CLICKHOUSE_REAL`、`POGO_CLICKHOUSE_STATE` 可覆盖原生二进制路径与缓存位置，供测试与运维使用。

单测见 `tools/clickhouse_compat_test.go`（桩二进制 + 桩 HTTP 服务覆盖原生透传、探测缓存、参数映射、INSERT 通道、端口映射、错误透传与拒绝路径）。真实 CPU 上的判定以容器内 `clickhouse-client --version` 为准：正常打印版本即原生路径，输出含 `HTTP compatibility fallback` 即回退路径。

## 普通 Bash 与独立 Skill 的权限

Docker Compose 默认保留 `POGO_SANDBOX=enforce`，并设置 `POGO_SANDBOX_NET=${POGO_SANDBOX_NET:-1}`。普通 Bash 和使用同一普通命令包装器的工具仍经 bubblewrap 执行，保留文件和进程隔离、私有 Skill 仓遮蔽与环境变量过滤，但使用主容器网络。设置 `POGO_SANDBOX_NET=0` 可以禁用普通命令网络；该环境策略在进程启动时读取，需要重建主容器生效。

联网开关属于部署级策略，不是地址白名单，也不是某个会话的审批开关。连接外部服务不需要向外发布主容器的 SSH 或数据库端口；目标服务仍需正确监听，并允许连接来源和认证账号。容器内 `localhost` 指容器自己；访问宿主机需使用可达地址或适当的 `host.docker.internal` 映射。

联网不会自动授予凭据。SSH 密钥、已核对的主机身份、数据库账号和 TLS 证书需单独提供给使用者。普通沙箱默认不会继承模型密钥或 SSH agent 环境；额外环境变量必须由部署者显式加入 `POGO_SANDBOX_ENV`。不要把数据库服务器、SSH 服务端或宿主机 Docker socket 加入主容器来代替客户端。

独立 Skill Runtime 的包声明、绑定授权和执行器目前均只允许 `network: none`，不继承上述普通 Bash 联网开关。它能看见只读系统目录里的客户端程序，但不能因此连接网络；无须扩大独立 Skill 的系统配置挂载。详见 [独立 Skill 网络权限](skill-network-permissions.md)。

已有部署首次开启普通 Bash 网络时，使用 `w_deploy/deploy.sh release --sandbox-network-migration` 和新的现场验收 hook。这一专用迁移只允许主服务联网配置从缺省/`0` 到 `1`，保留 `enforce`、其他配置与浏览器/Office 容器身份；冷备份与回滚保留原联网设置。新装部署直接使用默认配置。

## 验证范围

隔离协议验证使用合成账号、临时文件和独立服务，覆盖 SSH、SCP、SFTP、PostgreSQL SQL、MySQL 8 的默认 SHA2 认证、ClickHouse 原生 SQL、FTP 文件传输与网络诊断。另在候选 ARM64 镜像中通过真实 `Bash.Execute` 检查客户端可用、HTTP 请求成功、环境变量过滤和私有 Skill 仓遮蔽；独立 Skill 使用真实 runner 验证仍不能连接父进程监听端口。

这些结果证明被测客户端和权限路径可用，不替代目标服务器的路由、防火墙、账号或版本兼容性检查。`ping` 使用现有内核允许的 ICMP 数据报权限，不为它增加 `NET_RAW` 或 `privileged`。
