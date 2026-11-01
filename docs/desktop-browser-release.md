# Pogo Desktop：可选 Electron 实验与历史部署记录（2026-10-08）

用户已明确以 **Docker Web 版 Pogo** 为主，继续推进容器内同一 Chromium 的通用 browser-use 和网页接管体验。Electron 保留作可选实验参考，不是 Docker Web 的使用前提。当前实现与验收边界见 [Docker Web 浏览器实时接管](browser-web-live.md)。

下文保留 2026-10-08 较早阶段的本机桌面部署及验收证据，不能作为当前 Docker 运行状态、默认部署方式或 Web 人工操作已验收的证明。该实验采用 **Go 服务 → Pogo Desktop worker → 同一组原生 Chromium WebContentsView**：人工输入由本地 Chromium 处理，Agent 经 Playwright/CDP 操作相同网页；没有引入 Rust，也没有新增第二套 Agent 浏览器工具。

## 当时的运行方式

- 应用：`~/Applications/Pogo Desktop.app`，当时已启动。启动配置和浏览器数据位于 `~/Library/Application Support/Pogo Desktop/`，凭据文件为 `0600`，不进入应用包。
- Studio：`http://127.0.0.1:19777/`。Docker 主服务访问本机 `host.docker.internal:19783`，worker 只监听本机回环并要求鉴权。
- 当时的部署选择器：`w_deploy/.browser-mode` 为 `desktop`；独立的 `compose.desktop.release.yml` 固定应用镜像。桌面设备只绑定配置指定的账号；其他账号不获得浏览器执行器。
- 当时旧容器 `pogo-pogo-browser-1` 已停止，保留作显式回滚。该桌面部署的普通启动不加载旧 worker 服务，也不自动回退；主服务 `pogo-base` 保持运行。这不是容器后续状态的实时记录。
- 使用原生网页输入、拖动或完整 DevTools 时点击“接管”；点击“交还”恢复 Agent 的控制权限。关闭桌面应用会使浏览器任务暂时不可用；重新打开后按新 context 恢复，不重放中断的操作。

应用镜像：`sha256:2f0dc563cfaac243c5a1b28ef3eede2acee12bce5b91de44f9163e3f26c9e8cc`。

## 桌面实验的验证结果

- 用户在真实 Electron 集成窗口确认鼠标拖动、中文输入法顺畅；这是桌面实验的人工手感验收，没有测量输入延迟，也不等于 Docker Web 的物理输入法和拖动已验收。
- 实际 Go/Studio/Agent/tool 链路的 25 项受控检查通过，覆盖一次提交、取消与恢复、会话隔离、人工接管和退役 HTTP 输入。另一次真实重启检查确认持久 Cookie 保留、旧 context/lease 拒绝、不重放操作。
- 桌面专项测试 20 项通过。原生元素选择的第一次点击被拦截后进行 DOM 命中和身份核验；无 hover、有 hover、延迟 mouse-up 均不触发表单提交。真实打包版 UI 也验证引用进入输入框、Esc 退出、零提交。
- Docker 候选实测跨账号浏览器入口及 Skill 拒绝。正式发布再次实测设备绑定、人工控制协议、废弃路径 410、真实 GLM-5.3 的一次保存及 Network 回执、人工审批工作流。正式模型 6 次 browser 调用、7 轮、91,298 tokens，监测预算 120,000，未发生额外提交。
- 发布保留 5 份持久挂载冷备份；原有 3 个会话及 3 份会话记录校验不变。维护状态已解除。停止旧 worker 后，在正式桌面成功打开百度首页并完成接管/交还。

本机证据目录为 `.localagent/checks/desktop-release-20261008/`，最终核验在 `final-verification.json`；候选与原生测试在 `.localagent/checks/desktop-integration-20261008/`。这些是本机验收记录，不应把其中的私有配置、凭据或日志提交到仓库。

## 桌面实验的边界和恢复

该实验的人工点选引用只支持主文档；iframe 元素选择明确拒绝，Agent 既有 iframe/CDP 能力不受影响。百度验收只覆盖当时桌面路径的首页与控制权切换，不代表 Docker Web、所有站点或验证码均已验收。旧容器及 Chrome 的网站登录状态没有自动导入桌面配置，网站首次访问可能需要重新登录。

应用是本机 ad-hoc 签名开发构建，已通过严格签名和包内文件校验，尚非公证分发版本；Windows/Linux 未完成验收。实现和配置见 [Desktop README](../desktop/README.md)，打包见 [PACKAGING](../desktop/PACKAGING.md)。

冷备份位于本机 `w_deploy/backups/desktop-20261008-162401-upkzo469/`，包含私有数据，请勿分享。需要回滚时，先核对并显式恢复原 worker 容器及健康状态，再使用匹配的 `desktop-rollback` 事务和现场验收 hook；不会恢复业务数据归档或隐式重启旧 worker。具体流程见 [发布与恢复](../../w_deploy/RELEASE.md)。
