# 浏览器账号访问候选环境

这套独立 Docker 编排用于验证新的账号访问与浏览器元素选择功能。项目名固定为 `pogo-browser-auth-lab`，不读取现有部署的 Compose、数据卷或环境变量，不重建现有 `pogo-base` 与浏览器容器。

| 入口 | 地址 |
| --- | --- |
| 候选 Pogo（人工使用） | `http://pogo-auth-lab.localhost:19778/manage` |
| 宿主机直接预览模拟平台 | `http://127.0.0.1:5190` |
| 在候选 Pogo 的托管浏览器里访问模拟平台 | `http://mock-platform:5190/` |
| 浏览器 worker | 仅 Docker 私有网络 `worker:7788`，不发布宿主端口 |

托管 Chromium 在容器里运行，不能用容器自己的 `127.0.0.1:5190` 访问另一个容器。宿主机直接预览与托管浏览器使用不同登录会话，这是预期行为。

人工使用候选环境请保留上面的独立主机名。现有 Pogo 登录 Cookie 名称固定为 `pi_session`，Cookie 按主机隔离而不按端口隔离；在同一个日常浏览器里登录 `127.0.0.1:19778` 会覆盖 `127.0.0.1:19777` 的登录 Cookie。已用真实 Chromium 验证 `pogo-auth-lab.localhost` 自动解析到回环地址、页面登录与 API 正常，并保持同一 BrowserContext 中的 `127.0.0.1` 测试 Cookie 不变；无需改 hosts 文件或生产认证代码。脚本的服务探测仍访问 `127.0.0.1`，模型/API 验收使用独立 Cookie jar、UI 验收使用独立 BrowserContext，因此与日常浏览器 Cookie 隔离。Node 的系统 DNS 不一定解析 `.localhost` 子域，不要把人工入口直接替换进基于 Node fetch 的 API 测试。

可用 `node pogo/browser-runtime/lab/hostname-qa.mjs /absolute/new-hostname-report.json` 重跑主机名与 Cookie 隔离验证。它只向独立 Chromium 上下文种入一个合成生产域 Cookie 哨兵，并在候选域通过真实 Vue 登录，完全不请求现有 19777 服务。

## 构建与运行

```sh
python3 pogo/browser-runtime/lab/lab.py init
python3 pogo/browser-runtime/lab/lab.py build
pogo/browser-runtime/lab/start.sh
pogo/browser-runtime/lab/status.sh
pogo/browser-runtime/lab/stop.sh
```

要求 Docker Desktop / Linux Docker、支持 `env_file.format: raw` 的 Compose v2、Python 3.9+。当前 `base-images.json` 锁定已验证的 Linux ARM64 依赖镜像。构建前会核对每个本地镜像的完整 ID 与架构；缺镜像或锁不匹配时失败，不悄悄拉取替代品。其他平台需要先构建对应依赖并审查更新该锁文件。

应用镜像直接使用项目正式 `pogo/Dockerfile`，重新构建当前 Go 与 UI。浏览器镜像使用锁定的 Playwright 1.63 运行依赖，加上当前 `src/`；mock 镜像使用锁定 Node 基础镜像，加上 `mock-platform/`。镜像有 `io.pogo.lab=browser-auth-candidate`、源码 revision 和可审计源码摘要标签；镜像 tag 包含摘要，以区分同一提交上纳入摘要的不同修改。构建前后摘要变化时拒绝更新候选清单。私有目录中的 `build.json` 记录实际镜像 ID、依赖 ID、源码 revision 与源码摘要；启动以最终镜像 ID 为依据，标签漂移时拒绝启动。

源码摘要纳入 `pogo/` 下已跟踪和未被 Git 忽略的未跟踪文件，以及根 `.dockerignore`，记录文件模式和符号链接目标，不读取符号链接指向的外部文件。摘要排除运行时私有配置、环境文件、密钥文件和依赖/产物目录。宿主机 `web/ui/dist` 被根 Docker ignore 排除，镜像内会从 UI 源码重新生成，因此它不属于源文件摘要。该摘要不是完整 Docker 构建上下文或全部构建输入的证明：其他被 Git 忽略的文件、网络依赖解析等不在其保证范围。

`base-images.json` 核对的是当前 ARM64 本地缓存镜像 ID。Dockerfile 的 `FROM` 仍使用标签，正式应用镜像的 APT/Python 安装步骤也没有完全固定包版本，因此这里提供可审计的构建和运行流程，不保证相同源码逐字节重建出同一镜像。最终镜像 ID 与实际运行容器是验收和回溯依据。

`start` 在未构建时自动构建；已构建时使用记录的镜像，因此改代码后要显式重新 `build`。启动只影响该候选项目。`stop` 只移除候选容器和网络，保留其独立的 `lab-home`、`lab-skills` 数据卷，没有自动删除数据操作。

## 私有配置与登录

默认配置目录为仓库根的 `.localagent/browser-auth-lab-private/`，位于 `pogo/` 外；应用的根 `.dockerignore` 只放行 `pogo/`，因此不会把它发送进应用构建上下文，worker/mock 的上下文也不包含此目录。目录权限 0700，文件权限 0600。可以通过 `LAB_PRIVATE_DIR=/absolute/private/path` 或每个命令的 `--private-dir /absolute/private/path` 指定其他位置。拒绝把私有目录放入 `pogo/`。

| 私有文件 | 用途 |
| --- | --- |
| `providers.json` | 仅供候选环境的模型配置 |
| `model.env` | 模型所需 `key_env` 的环境变量，使用 `KEY=value` 原文格式 |
| `users.json` | 随机密码的 `admin` 管理员，文件仅保存 PBKDF2 哈希 |
| `admin-password.txt` | 首次生成的候选管理员密码，用来登录 19778 的页面 |
| `runtime.env` / `worker.env` | 独立生成的候选服务凭据，不输出到终端 |
| `compose.env` / `build.json` | 候选镜像、平台、私有路径与构建清单 |

初始化保留已有文件，不覆盖已有模型配置或密钥。没有模型配置时会生成不可访问的占位模型，页面与模拟业务仍可测试；真实模型评测前应由操作者私下提供批准的配置和 `model.env`，再启动或重建候选 app。不要把这些文件加入 Git、复制进镜像或粘贴到聊天里。

bootstrap 容器没有网络，仅把 provider 和首次用户配置复制进隔离卷；应用通过正常用户登录，不打印带 token 的登录链接。现有候选用户文件保留，避免重启覆盖在 UI 修改的密码。原始 `admin-password.txt` 在改密后不会自动更新。

## 验证内容

候选 app、worker、mock 都提供健康检查；app 与 mock 只发布到宿主机回环地址。app/worker 保持非 root、只读根文件系统、限制 CPU/内存/PID、`cap_drop: ALL`、`no-new-privileges`；worker 使用项目 Chromium seccomp 配置，保留浏览器沙箱。bootstrap 只保留文件初始化所需能力。

模拟平台账号是 `demo / pogo-demo-2026`，模拟二次验证码 `246810`。候选托管浏览器中访问 `http://mock-platform:5190/`，登录后 `api_read` 自动复用当前站点 Cookie 或可识别的 localStorage Bearer；无需授权菜单、接口路径/返回字段白名单或权限有效期。模型可选 `storage_key` 只用键名选择候选，不读取原始令牌。

脚本的独立边界检查：

```sh
python3 -m unittest discover -s pogo/browser-runtime/lab -p 'test_lab.py' -v
```

这些检查覆盖不覆盖私有配置、文件权限、构建目录限制、固定 Compose 项目、停止时保留卷、镜像漂移拒绝与环境变量隔离。真实浏览器/模型端到端测试由本目录的 `e2e-auto.mjs` 单独执行；不要把健康检查当作模型任务成功证明。

## 真实模型会话验收

```sh
# 调用候选环境已配置的glm-5.3，目录必须不存在。
node pogo/browser-runtime/lab/e2e-auto.mjs /absolute/new-model-report-directory
# 同样验证Bearer登录。
LAB_AUTH_SCENARIO=bearer node pogo/browser-runtime/lab/e2e-auto.mjs /absolute/new-bearer-report-directory
```

脚本创建独立Studio Agent/会话，在同一个模型运行中完成合成账号表单登录和自动API查询；实际经过主服务、Go工具、Chromium worker和模拟平台。它不会调用Pogo旧 `/browser/auth` 接口，不模拟任何Pogo授权。随后测试控制器通过真实人类键盘协议模拟网站登录过期，再验证模型停止。真实的登录、API200响应与业务结果均与模拟站服务端计数核对，不能只凭模型文本或页面加载的订单计数通过。

脚本只允许browser动作及内置browser-use指引读取；保留合成会话用于复查，不操作生产会话。HTTP客户端用独立Cookie jar与127回环地址，人工链接使用隔离主机名。真实按钮布局和小手功能由下节验证。旧 `e2e.mjs` 保留为历史手动模式参考，当前候选请使用 `e2e-auto.mjs`。

## 真实 Vue 界面验收

```sh
node pogo/browser-runtime/lab/ui-qa.mjs /absolute/new-ui-report-directory
```

该脚本使用浏览器运行时已安装的 Playwright 与 Chromium，通过候选 Pogo 的真实登录和 Studio API 创建一个独立的合成 Agent/会话，之后通过实际 Vue 按钮完成接管、原站地址导航、元素选择和沉浸切换，并核对授权菜单已删除、没有旧授权请求。不会发送模型消息、模拟 API 响应、重启容器或操作其他会话。

验收同时检查菜单交互不会转发到远端页面，小手只发起只读 pick 请求，所选元素成为输入框中的引用，原草稿保留且没有自动发送。报告保留真实截图、检查结果和独立会话链接；不写入密码、Cookie、lease 或模型密钥。UI场景使用模拟站访客页面验证浏览器控件与元素选择；真实登录后的自动API读取由模型E2E单独验证。
