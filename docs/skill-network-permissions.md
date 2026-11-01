# 命令联网与独立 Skill 的权限边界

主镜像预装网络客户端，并启用 `POGO_SANDBOX=enforce`、`POGO_SANDBOX_NET=1` 后，普通 Agent Bash 命令可以在保留文件、进程和环境隔离的前提下使用容器网络。该部署开关不会为独立 Skill Runtime 扩大权限。

## 不同执行路径

| 执行路径 | 网络权限来源 | 本次设置后的行为 |
| --- | --- | --- |
| 普通 Agent Bash，以及通过 Bash 执行的内置 Skill 脚本 | 普通命令 `SandboxPolicy.Net`，来自 `POGO_SANDBOX_NET` | 保留沙箱，允许访问容器可达的网络 |
| 导入的 `process-json` Skill Runtime | 包声明、Agent 绑定中的 sandbox grant、独立 LocalExecutor | 仍只支持 `network: "none"`，禁止联网 |
| 可信调用方显式提供的 `CommandScope` | 调用方提供的 `CommandScope.Network`，默认为 false | 不继承普通 Bash 开关；必须显式授权，不能由模型参数指定 |
| Pogo 服务自身的模型、MCP、浏览器或 Office 连接 | 各连接自身的配置与授权 | 不由普通 Bash 或 Skill Runtime 的网络开关决定 |

“使用 Skill”并不等于使用独立 Skill Runtime。内置 `SKILL.md` 指导 Agent 使用 Bash 时，命令受普通 Bash 策略约束；导入带有 `pogo-skill.json` 的可执行包时，受独立 Runtime 的权限约束。

## 独立 Skill 的现有模型

当前 `pogo.skill/v1` 的 `permissions.network` 只接受 `"none"`。权限声明不等于主机授权；选用 Skill 时，主机根据已验证的包创建绑定和 grant，并再次检查资源、效果、隔离方案与权限上限。请求其他 network 值或伪造绑定均被拒绝。安装包是管理员审核边界，但当前管理员导入也不能跳过 `"none"` 的协议限制。

LocalExecutor 固定创建私有网络、PID、IPC、用户等命名空间，通过 `pogo-skill-runner` 监督执行。它不读取 `POGO_SANDBOX_NET`，也不因普通命令关闭或放宽沙箱而降级为裸执行。运行器缺失或命名空间失败时，独立 Skill 不会启动。

因此，本次没有把所有 Skill 全局开放联网。需要 SSH、数据库或 FTP 连接的任务可以使用普通 Bash 中的原生客户端；不能通过修改 `network` 字段让独立 Runtime 获得同样能力。未来若支持联网 Skill，需要新增可审核的协议与按包授权模型，并实现对应执行器策略，不能仅删除 `--unshare-net`。

## 客户端可见性与文件边界

独立 Skill 只读挂载 `/usr`、`/bin`、`/lib`、`/lib64` 等系统目录，因此安装在 `/usr/bin` 或 `/usr/local/bin` 的客户端及 `/usr/lib` 下的动态库、MySQL 插件可以被看见。看见可执行文件不等于获准联网。

其 `/etc` 不从主容器整体挂载：

- 可以读取动态库缓存 `/etc/ld.so.cache`。
- `/etc/hosts`、`/etc/nsswitch.conf`、`/etc/passwd` 由主机为每次调用生成；其中名称解析仅使用 `hosts: files`。
- 不挂载主容器的 `/etc/resolv.conf`、`/etc/ssl/certs`、`/etc/ssh`、数据库配置或其他 `/etc` 配置。
- 不挂载主容器 HOME、Pogo 凭据、Docker socket、私有 Skill 仓库或其他会话目录。
- 清空继承的环境，只设置独立的 HOME、PATH、TMPDIR、LANG 等运行变量；SSH、数据库和模型密钥不会因为安装客户端而自动传入。

上述 DNS 和证书限制与独立 Runtime 当前不联网的设计一致，本次无需扩大挂载。如果将来新增联网 Runtime，需要一起定义 DNS、证书、凭据和目标访问策略，不能假定安装客户端即可完成连接。

普通 Bash 和 `CommandScope` 有各自的系统挂载与环境过滤规则，不能用这份独立 Runtime 清单推断它们的权限。访问宿主机或外部目标时，仍以容器路由、目标监听地址、防火墙及远端账号权限为准。

## 回归验证

`go test ./skillruntime -run 'TestSkillNetwork' -count=1` 验证普通 Bash 网络开关的 0/1 两种设置均不会扩权独立 Skill，合法离线绑定仍可使用；网络包声明及伪造绑定继续被拒绝。

Linux ARM64 候选镜像中的 `TestLocalSkillNetworkIsolationWithShellNetworkEnabled` 使用真实 LocalExecutor、bubblewrap 和 `pogo-skill-runner`。它先证明父容器的临时 loopback socket 可以连接，再在普通 Bash 联网开关为 1 时确认独立 Skill 无法连接该 socket、看不见继承的开关和测试 canary，同时能找到只读系统目录中的 `curl`。这项测试须设置 `POGO_SKILL_RUNTIME_TEST_PLATFORM=1`，使用部署的 seccomp 配置运行；宿主机跳过该测试不代表实际隔离验证通过。
