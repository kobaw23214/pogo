# Studio 通用浏览器候选验收

这是独立的 `pogo-browser-v2-lab` Docker 项目，使用新的用户、应用卷及浏览器会话。它不重启旧认证候选项目或已有部署，也不修改 `w_deploy`。保留旧 auth e2e 与历史 CLI 报告，不能把两者改名当作本套验收。

## 链路与边界

`Studio → 生产 Browser 工具 → 私有回执代理 → native worker → 双站点 fixture`。

测试站点与回执代理运行在同一个 Node 进程，使用可比较的单调时钟核对恢复、保存和 DOM 观察的先后关系。应用生成的私有 worker session ID 通过一次受控 PUT 接入，绑定到控制器声明的 Studio 会话；额外 session 被拒绝。准备动作不进入模型计分账本。清理后再核对最终业务状态，不能忽略结束至删除之间的迟到请求。

fixture 只监听两个声明的 Host，不提供 oracle 或控制接口。管理接口是另一个监听端口，使用独立凭据，该凭据不传给 app 或浏览器。模型输出与 SSE 用于核对答案、运行身份和用量；判题证据始终来自代理实际收到的 worker ACK 和站点实际状态。

| 服务 | 入口 |
| --- | --- |
| 候选界面 | `http://pogo-browser-v2.localhost:19779/manage` |
| 控制脚本调用 app | `http://127.0.0.1:19779`，独立 Cookie jar |
| 私有 recorder admin | 宿主机回环 `127.0.0.1:19780`，独立鉴权 |
| 原始浏览器 worker | 仅内部网络 `worker:7788` |
| App 使用的代理 | 仅内部网络 `recorder:7789` |
| 两个 fixture 站点 | `http://fixture-a.test:5191`、`http://fixture-b.test:5191` |

浏览器仅接入单独的 Docker internal 网络，不能直接访问互联网；app 另有模型网络出口，recorder 另接一个控制桥接网络以发布宿主机回环管理端口。管理接口仍独立鉴权，app 与 worker 不持有该凭据。worker 使用生产默认跨站进程隔离启动配置，不另设 fixture 专用 flags；CLI 的 route fence 与 service-worker 配置仍不是生产默认设置，必须以实际 target 观察确认 OOPIF／worker 能力。用户界面需要单独验收，本脚本并不代替点击布局测试。

## 构建和启动

```sh
python3 pogo/browser-runtime/lab/browser-use-v2-lab.py init
# 在独立私有目录配置已授权的 providers.json 和 model.env。
python3 pogo/browser-runtime/lab/browser-use-v2-lab.py build
python3 pogo/browser-runtime/lab/browser-use-v2-lab.py start
python3 pogo/browser-runtime/lab/browser-use-v2-lab.py status
```

默认私有目录为 `.localagent/browser-use-v2-lab-private/`，位于构建上下文之外，目录 0700、文件 0600。初始化保留已配置的模型与凭据；不会默认读取旧候选的私有模型配置。应用使用项目正式 Dockerfile；worker 与 recorder 基于 `base-images.json` 锁定的 Playwright 依赖镜像，加入当前源码。

`v2-build.json` 记录源码指纹、镜像 ID 与依赖镜像。构建期间源码变化、启动时源码或镜像 tag 漂移都会拒绝继续。指纹为可审计源码集合，不是所有外部包的可重现构建承诺。命令固定项目名、Compose 文件及私有配置，不接受环境变量暗中切换项目。

```sh
python3 pogo/browser-runtime/lab/browser-use-v2-lab.py stop
```

stop 只移除这套候选容器和网络，保留命名卷。运行结果由宿主机控制脚本保存；recorder 容器内 `/tmp/v2-recorder` 只是临时副本，不应作为唯一归档。

## 先接线，再调用模型

控制器环境需要 `LAB_PASSWORD`（该候选 admin 密码）、`LAB_RECORDER_ADMIN_TOKEN`（独立管理凭据）；从私有文件载入，不写在命令行、报告或聊天中。显式设置 `LAB_APP_URL=http://127.0.0.1:19779`、`LAB_RECORDER_ADMIN_URL=http://127.0.0.1:19780`。可设置 `LAB_SOURCE_PROVENANCE` 指向构建与实际镜像验证的 JSON。

```sh
node pogo/browser-runtime/lab/browser-use-v2-studio.mjs /absolute/new-smoke-output --mode smoke --seed 20261012
node pogo/browser-runtime/lab/browser-use-v2-studio.mjs /absolute/new-model-output --mode model --seed 20261012
```

默认覆盖 iframe-data 和 debugger-recovery。smoke 不调用模型，验证真实应用会话、接管转发、双站点与 target、删除和暂停取消零保存；它不是 Agent 任务成功率。model 使用 GLM-5.3、原始自然语言任务目标和生产内置 browser-use Skill，不提供业务答案、变量名或固定工具步骤。每次运行必须使用新目录，失败不改写为成功。

每项上限为 120,000 输入加输出 tokens、50 轮、180 秒和 80 个浏览器动作。App 的 `-max-turns 50` 限制轮数，`-followups=false` 关闭运行结束后另行生成的建议，避免把那部分调用混入本轮验收；控制器基于实际 SSE 用量和时间取消，代理限制实际浏览器动作。Studio 没有自动继承 CLI 的总 token 预算，正在进行的模型请求可能超额，任何已报告超额仍判失败。缓存及 reasoning 属于输入／输出子集，不能重复计数。

Agent 选择 browser，但当前产品仍注册 read、ls、find、grep、todo、subagent 等保护工具。验收仅接受 browser 与精确的内置 Skill 读取；发现其他工具会取消并判失败。SSE 取消不能撤销已开始的动作，因此这是实际工具使用审计，不能宣称 schema 只有 browser。

自动判题检查有限必要事实与证据；完整自然语言答案仍需独立复核来源归属。两个合成任务通过仅代表这条应用链路，不能替代全套任务、真实网站和生产发布验收，也不据此退役旧通道。

## 回归检查

```sh
python3 -m unittest discover -s pogo/browser-runtime/lab -p 'test*.py' -v
cd pogo/browser-runtime
node --test lab/browser-use-v2-*.test.mjs eval/test/transport.test.js eval/browser-use-v2/*.test.js
```

共享运输层和 fixtures 新配置均保留原默认行为。涉及跨进程时钟、回执来源、会话绑定、控制权、迟到写入与清理的变化需重新验收，不应只重跑模型分数。
