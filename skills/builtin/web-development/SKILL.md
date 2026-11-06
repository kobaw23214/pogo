---
name: web-development
description: 创建、修改和验证网页或网站，选择现有 HTML Preview 或 web_service 受管 Python/Node.js 服务。适用于交互页面、带后端/API 的应用、启动与重启网站、查看服务日志；网站访问与运行使用统一有效期。
---

# 网页开发与受管预览

先根据页面的实际需求选择交付方式。读取本 Skill 不授予 `web_service`、
`browser` 或宿主部署权限；以当前会话实际提供的工具、用户范围和审批结果为准。

## 选择交付方式

| 需求 | 路径 |
|---|---|
| 项目内 HTML/CSS/JS、module、JSON、图片或字体；无需后端 | 保存真实文件，用现有 `html-preview`；不启动 HTTP server |
| 需要服务端 HTTP API、POST 或持久服务数据 | 按当前工具实际列出的运行类型与任务选择受管 `python-web` 或 `node-web` |
| 长期在线、固定公开域名或正式部署 | 说明临时受管预览的边界；本 Skill 不执行生产发布 |

静态预览使用只含 JSON 的 `html-preview` fence，例如
`{"path":"site/index.html","title":"站点预览"}`。普通 `html` fence 是源码。
资源必须在预览项目内；外部 CDN/API、表单提交、子 frame 和 worker 不受支持。
完成文件编辑后沿用原有自动刷新。

## 图片和其他静态资源

HTML Preview 和受管网站都只加载项目内的图片；不能直接把百度图片搜索页、
搜索结果跳转链接或外站图床 URL 当成站内 `img.src`。图片原地址在独立标签页
能打开，也不代表它能在当前网站中加载。受管应用不能在运行时请求外部图源做代理。

需要真实照片时，先用可用且已授权的浏览器搜索，确认原始图源，再在开发工作区
通过允许联网的命令下载到项目自己的 `assets/images/`。Bash 的开发网络权限
与受管应用的运行网络权限不同：应用不能外连，不等于开发阶段不能准备图片。
按 [references/local-images.md](references/local-images.md) 检查文件、相对路径、
静态路由和实际加载结果。原有服务修改后须显式 restart 才会同步图片快照。

下载是否可用以真实工具回执为准。浏览器 `downloads` 未调用或列表暂为空，
不能据此宣称下载被限制；只有 `state:saved` 且有真实 path 才能当作工作区文件。
沙箱初始化失败时保留准确报错，不重复失败命令、不绕过隔离；用户要求真实照片时，
不要把 SVG 插画、外链文字或缺图占位当成照片任务已完成。

`python-web` 使用 Python 标准库，适合 SQLite、Python 数据处理与 HTTP API；
`node-web` 使用 Node.js 标准库，适合 JavaScript HTTP/流式/WebSocket 服务。
只有管理员已配置且校验通过的运行类型才会出现在工具中；不要猜测未提供的类型。
两种类型都不安装 pip/npm 依赖、不执行 build；Vite/Express 等需要额外依赖的项目
暂不属于标准库配置。非空 `requirements.txt`、含依赖的 `package.json` 或
非空 `build_argv` 会被拒绝。应用读取 `HOST`/`PORT` 监听平台指定的内网端口；
不要把默认 8080 写成固定约束，管理员修改运行设置时会自动重启受影响的网站。
应用不能主动建立
TCP、UDP 或 Unix 连接，连 loopback 也不行。浏览器请求该应用自己的 API 可用。
外部 API、登录/Cookie/localStorage、worker 等超出当前边界时明确说明，不承诺可用。

## 内网 IP 路径预览

管理员可显式启用 `preview-mode=path`，通过同一个 `http://内网IP:19770`
打开 Pogo 和受管网站，无需域名或证书。`open` 返回的网站访问地址字段 `preview_url` 形如
`http://内网IP:19770/_pogo/web/<48hex>/`；只使用真实回执，不能猜测 grant。
域名预览仍可使用，静态项目继续走 HTML Preview。

为使项目兼容路径预览，浏览器资源和 API 必须采用 `./assets/app.js`、
`./assets/style.css`、`./api/counter` 等相对 URL。`/api/counter` 是根绝对路径，
不是相对 URL，会离开 grant 前缀并被 CSP 拦截。不要生成根绝对资源路径或
`<base href="/">`，也不依赖平台改写 HTML、CSS、JavaScript 或请求 URL。
服务端路由仍写 `/`、`/api/counter` 等：gateway 只剥离当前 grant 前缀，
再把余下路径和查询参数转给应用。

WebSocket 也必须留在当前 grant 前缀内。例如当前页面在 grant 根目录时：

```javascript
const endpoint = new URL('./ws', window.location.href);
endpoint.protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
const socket = new WebSocket(endpoint.href);
```

这会保留当前页面的 pathname 前缀和 host；HTTP 内网部署使用 `ws://同host`。
进入应用子路由后，先从当前 pathname 取出 `/_pogo/web/<48hex>/`，再拼接
`ws`，不得写成站点根 `/ws` 或硬编码服务容器地址。预览 iframe 保持
`allow-scripts allow-forms` 且没有 `allow-same-origin`；不要取消 sandbox、
放宽主站认证或全局修改 fetch/XHR/WebSocket 来掩盖路径错误。

只有工具确实可用才开始受管运行。工具缺失、撤权、拒绝或不支持时，解释原因，
保留已完成的源码；不要改用 `nohup`、普通后台命令、主容器 pip、任意 Docker
命令、猜测 localhost URL 或修改端口映射来绕过。静态任务仍可使用 HTML Preview。

## 动态项目的闭环

1. 在会话工作区内完成项目文件，不写入真实密钥或非必要依赖清单。现有项目超出
   profile 时说明限制，不删除原有配置或擅改用户指定框架来伪装支持。需要最小的
   标准库 HTTP/SQLite 示例时，读 [references/python-web.md](references/python-web.md)；
   JavaScript 服务示例读 [references/node-web.md](references/node-web.md)。
2. 先用 `list`/`status` 核对是否已有同名服务；已有服务更新走 restart，
   不盲目重复 start。需要创建时调用 `web_service` 的 `start`，提供相对 `project_root`、稳定 slot `name`、
   可用的 `profile` 和 argv，例如 Python 的 `["python3","app.py"]` 或
   Node.js 的 `["node","app.js"]`。用户指定名称时用 `display_name` 保留其友好显示名（支持中文）；内部 slot
   使用稳定英文标识，不用改名来创建重复实例。不传镜像、
   owner、容器网络、挂载、外部端口或凭据，这些由平台决定。
   未指定运行时长时省略 `lease_seconds`，使用平台默认值；用户指定时填写对应秒数。
   只修改名称时用 `rename` 和 `display_name`，保留 service_id，不新建服务。
3. 保存真实回执的 `service_id`/`generation`。`accepted` 仅表示受理，
   不能据此说“网站已启动”。结合网站卡更新核对 `status`；必要时本轮最多
   查询 6 次且本轮等待总计不超过 60 秒，不做紧密轮询或 shell
   `sleep` 循环。尚未 ready 时报告“正在启动”和当前状态，由网站卡继续显示进展。
4. 只有状态为 `ready` 才 `open`，区分平台回执的三个 URL：`service_url`
   用于打开 owner 服务卡；`preview_url` 用于当前客户端浏览器的 iframe 预览；
   内置 `browser` 工具只能使用非空的 `browser_url`。`browser_url:null` 表示
   内置浏览器路径未被确认，禁止回退到 `preview_url` 或 `service_url`，也不自行
   改写 host、端口或猜测内网地址。保留客户端 iframe 预览，说明内置路径尚未确认；
   待平台修通并验证后，重新 open 获取非空 `browser_url` 再执行内置浏览器验证。
   HTTP 就绪只证明当前代次的入口检查通过；实际页面功能需另做浏览器交互验证。
   若 `browser` 实际可用且页面验证属于用户授权任务，按其既有审批流程检查页面和 API；
   平台处理所需审批，不另外重复询问或用历史同名服务推定执行权限。
   内置路径未确认不等于客户端网站不可用。未实测要明确说未实测。
   需要在对话中直接嵌入在线网站时，确认 ready 后使用 `web-preview` fence，
   内容只能是 JSON，例如 `{"service_id":"ws_实际服务ID","title":"站点预览"}`。
   service_id 必须逐字复制此会话的真实工具回执，不能凭空生成；title 只是无状态时的
   显示备用标题，不会改名，平台实际名称优先。不要在该 JSON 中写 URL、session_id
   或原始 HTML。客户端按当前会话权限获取生命周期地址；嵌入不会启动、重启或续期，
   停止、过期或删除后显示对应状态，公开分享副本不会加载私有网站。
5. 修改运行中的项目后显式 `restart`，再核对新代 ready/open。快照只在
   start/restart 同步，文件已保存不代表运行网站已更新。重启保持 service_id、
   增加 generation；同一租期的网站访问地址保持稳定，重启完成后可继续访问，
   不自动延长服务有效期。

修改启动配置时使用 `restart` 的完整 `replacement_spec`，从 `status.spec` 保留
`project_root`、`profile`、`start_argv`，再修改 `health_path` 等所需字段。
`health_path` 只在 `start` 顶层或 `restart.replacement_spec` 中；`start` 不接受
`replacement_spec`。仅更新源码时 `restart` 无须此对象。运行中的同名 slot
应保持原 service_id；不要为调整配置而 stop/start 重建租期。
健康检查用于就绪与存活判断。验证页面、资源或 API 使用可用且已授权的浏览器或命令，
不通过反复修改健康检查和重启来替代测试；通道不可用时明确保留未验证项。

每个新的写操作使用新的 `operation_id`（1–128 位字母、数字、`-` 或 `_`）。
请求丢失、执行结果不明时，重试同一操作必须使用原 ID 和相同参数，先核对状态，
不另起一个可能重复的操作。
网站访问地址的有效期与服务一致，不向用户引入单独的链接有效期。

## 失败、数据和交付

- 启动失败或不就绪：读该 service/generation 的有界 `logs` 与 `last_error`，
  修正工作区源码后再显式 restart。依赖/profile/网络能力不支持时停止该路径；
  控制面不可用或停止未确认时如实保留待核对状态，不循环重启。
- 持久数据写 `/data`；其跨 restart/stop 保留，删除持久会话或账户会清理。
  `/workspace` 是运行快照，运行写入不会回流源码工作区，不将它当持久数据。
- 服务默认运行时长与数量上限由平台设置决定，用户可以指定自定义时长并显式续期；
  不假定固定 2 小时上限。服务到期会停止，访问地址同时失效。到期前续期后，
  原网站地址继续按新的截止时间可用。status/logs/open 不续服务，renew 必须是
  用户任务范围内的显式操作；停止用 stop，
  不自动续期。关闭预览页或结束模型任务不等于停止服务。
- 最终优先引用回执中的 owner 服务卡链接 `service_url`，不拼造路径。必须交付
  网站访问地址 `preview_url` 时说明网站截止时间，不另列链接截止时间；不把 bearer URL 写进
  公共文档、日志或外部分享。只报告实际完成的源码、HTTP 就绪及浏览器功能证据。
- 用户要求“先不部署”时保留代码和测试结果，不启用宿主功能、不迁移 19770、
  不修改真实运行栈。临时开发服务本身也只在任务已授权且工具可用时启动。
