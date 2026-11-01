# 浏览器界面

## 浏览器界面

```bash
$ pogo -web
pogo web  model=glm-5.2  cwd=/Users/you/project
  http://127.0.0.1:7777/?token=6f1c...
```

打开那个地址就能用：左边会话列表，中间时间线（思考 / 工具调用 / 回答），下面输入框。`edit` 的改动以 diff 呈现，`bash` 的输出是终端样式，`todo` 是一份勾选清单，需要批准的调用会当场弹卡。

**任务清单只有最新那份算数。** 一次会话里模型会改好几次清单，而时间线上每一次都是一张卡。`edit` 的 diff 是发生过的事、永远为真；一份清单是**当时的状态**，被下一次写入取代。所以被取代的那些收成一行并降透明度（可以点开，「那一项什么时候出现的」偶尔要查）——全部照原样画着，往上滚看到一张「1/3 done」会被读成现在的进度。判定只认**已结束且成功**的写入：被拒的调用什么都没写成，进行中的还没写，两者都不能把前一份好清单降级。

**跑动中可以追发。** 它在跑的时候发送按钮变成蓝色的「追发」，消息会塞进正在跑的这一轮：等本轮的工具调用跑完、下一次问模型之前送达。所以发现它方向不对时不用取消掉整轮重来，直接说「改用 X」就行。如果这一轮恰好在你按下发送的瞬间结束了，文本会放回输入框并提示你，不会凭空消失。

顶栏有模型切换器（切换保留对话历史，运行中禁用）和**上下文占用条**：超 70% 变黄、85% 变红。那个百分比用的是**最近一轮 prompt 的大小**，不是会话累计——累计值随轮次增长得快得多（每轮都要重发全部历史），只适合看计费。点开它会展开构成面板：谁在吃上下文、以及**该拉哪个杠杆**（见「压缩」——同一份数据会给出两种相反的建议）。

占用条现在会自己往回掉：默认在窗口的五分之四处上下文清理就开始丢弃旧的工具输出（见「上下文清理」），下一轮的实测 prompt 因此变小。**所以占用条的黄/红是按触发线算的，不是按窗口的固定百分比**——清理会把占用压在触发线下面一点，用固定百分比的话那个条会整场发黄，而一个永远亮着的警示色不携带信息。绿=还没到触发线；黄=清理已经在跑（正常）；红=清理跟不上了，该动手。`-context-edit off` 时没有东西把 prompt 拉回来，色带退回固定的 70%/85%。掉下去的原因走 `turn_start` 事件上的 `context_edit` 字段，终端会打一行，浏览器目前只体现为占用条回落。**但清理不是压缩**：被对话文本而不是工具输出撑大的会话没有可清理的对象。那时候该做的是 `/compact`（见「压缩」），而不是开新会话——点开占用条，构成面板会直接告诉你是哪一种。

页面打进了二进制，不需要单独部署。改前端后要重新构建：

```bash
cd web/ui && npm ci && npm run build   # 产物在 web/ui/dist，被 embed 进二进制
cd ../.. && go build -o pogo .
```

开发时用 `pogo -web -web-dev http://localhost:5173`（另一个终端 `cd web/ui && npm run dev`）：浏览器只跟 Go 服务器打交道，非 API 路由被反代给 vite，所以单一 origin、token 照常工作，HMR 也能用。

**外部面板：`-web-panel 名称=url`（可重复）。** 外部应用经 `/panels/<名称>/` 反代接入（与页面同源，不开 CORS），恶意代码分析平台这类场景应用不用改 pogo 就能挂进来。只注册你信任的后端：面板与页面同源，和 `-skill` 一样是运营者的显式决定。面板也可以**运行时注册**：浏览器页签的新标签页添加跨源快捷方式时默认勾选「经内置反代同源打开」，背后是 token 门禁的 `POST /api/panels`（按 URL 幂等，名称从卡片标题推导），注册结果持久化在 `<状态根>/panels.json`（`POGO_PANELS_FILE` 可改路径）——信任门槛与旗标相同，只是决定权从命令行移到了持有 token 的界面用户手上。还有一个**默认关闭**的自动注册开关 `POGO_PANEL_AUTO_REGISTER=1`：导航到任何页面都把它注册成面板。开着它等于把「同源 + 持 token」的身份发给每一次导航的目标——`serve.go` 的注释写着它为什么必须是显式的环境变量而不是默认行为。容器内部署再设 `POGO_PANEL_LOOPBACK_MAP=host.docker.internal`：代理在服务端取数，用户按浏览器视角填的 `localhost` 地址会被改写到宿主，否则在容器里指的会是容器自己。

右侧 dock 的图标栏**固定三颗**：**文件**（工作区）、**面板**和**技能与工具**。面板是一个容器，Shell 和每个注册的 `-web-panel` 都是它的住户，由标题栏左侧的下拉切换——注册再多面板也不会让图标栏变长；面板标题栏还有最大化（临时占满宽度，不持久化）和右侧/底部布局切换。技能与工具面板是 skill 的管理面：**按技能**列出每个 skill 的说明、存放路径和它 `tools.json` 声明的工具，**按工具**平铺全部已声明工具（带过滤），点技能名可读其 `SKILL.md` 原文——工具卡片上的徽章就是审批待遇（只读免审、破坏性必审、仅目录不可直调）。

面板可以把一句话递给对话区（比如详情页上的「问 agent 这个样本」），用的是 MCP Apps 的 `ui/message`（JSON-RPC over postMessage）：

```js
window.parent.postMessage({
  jsonrpc: "2.0", method: "ui/message",
  params: { role: "user", content: { type: "text", text: "分析这个样本" } },
}, window.location.origin)
```

pogo 会校验来源 origin 与发送方 iframe，然后把文本**填进输入框**——面板永远不会代你发送。

**空态引导：skill 目录下的 `starters.json`（可选）。** 空对话上的引导卡片由它提供，pogo 只负责渲染与校验，不含任何领域内容；没有这个文件时空态保持原来那行提示。改完刷新页面即生效（每请求重读）。文件默认在状态根下，`POGO_STARTERS_FILE` 可以挪到数据卷（容器部署指向 `/data` 一类的地方，编辑在容器重建后仍活着）。每轮结束后的下一步 chips 则由服务端在回合结束后**异步生成**（`-followups` 控制，默认开）：模型调用在回答渲染完之后才开始，不占用回答的延迟，chips 以 `followups` 事件推送并随 snapshot 恢复；这里的 `followups` 规则组是生成关闭、失败或主动沉默时的兜底。

```json
{
  "heading": "今天要做什么？",
  "send": false,
  "cards": [
    { "icon": "search", "title": "找一个样本", "label": "零检出", "prompt": "…" },
    { "icon": "graph", "title": "看聚簇图", "panel": "样本库", "at": "#/clusters" }
  ],
  "followups": [
    { "when": ["mal-decompile"], "chips": [{ "title": "生成 Yara 规则", "prompt": "…" }] }
  ]
}
```

一张卡片只能有一个动作：`prompt`（填进输入框）或 `panel`（打开面板，可带 `at` hash 路由）。`send: true` 让 `prompt` 卡片点击即发送，默认是填入。`followups` 的 `when` 匹配**最后一轮**的工具调用与回复，没命中就不显示；生成的 chips 优先于它，规则组是兜底。`icon` 取自固定白名单——约 50 个意向名（2026-08 快照；清单在 `skills/starters.go` 的 `starterIcons`，白名单是封闭的，因为替代方案是让一个文件把标记交给页面）。卡片上限 27 张（空态按 3×3 分页，三页）；生成的追问 chips 最多 8 条。不合法的卡片会在 stderr 上说明原因并跳过。

三件事值得知道：

**运行的寿命属于会话，不属于连接。** 发起（`POST /messages`）和订阅（`GET /stream`）是两个接口，所以关掉浏览器、刷新、换标签页都不会打断正在跑的任务。重连时第一帧是 `snapshot`，带着已定稿的消息、进行中的累积文本、未完成的工具调用和未决的审批卡；网络抖动可以用 `?from=<seq>` 只取增量。

**bash 默认需要你批准。** 三档策略：`strict`（连 write/edit 也问）、`standard`（默认，只问 bash）、`auto`（全放行）。审批卡带绝对到期时间戳，所以刷新页面后剩余时间仍然算得对；超时按**拒绝**处理，而且拒绝只是回给模型的一条错误信息——loop 会继续，模型可以解释或换方案，不用重跑整轮。

**访问一律要凭据：token，或登录账号。** 没设 `POGO_WEB_TOKEN` 就每次启动随机生成一个并打印出来；设了 `POGO_USERS_FILE` 后浏览器可以改走账号密码登录（见下节），token 留给脚本和 API——两条通道任一放行。默认只绑 `127.0.0.1`，绑到外部地址时会额外警告。原因见下面的安全边界：`bash` 工具没有路径限制。

**MCP 端点（给别的 agent 调用你）默认关闭。** 设 `POGO_MCP=1` 后同一端口多出两个端点：`/mcp`（Streamable HTTP，新版客户端）和 `/sse`（2024-11-05 旧传输，对方说「走 SSE」而连不上 `/mcp` 时用）。鉴权与 `/api` 同源：默认认 `POGO_WEB_TOKEN`，设了 `POGO_MCP_TOKEN` 则只认它（可以单独吊销机器通道）。凭据用 `Authorization: Bearer <token>`；`/mcp` 也接受 `?token=`，`/sse` 不接受（旧握手重建回发地址时会丢掉 query）。工具面与语义见 `web/mcp.go` 的文件头注释：`agent_submit_task` 提交任务（每个任务是普通会话，在本页 UI 里可见、可审批），`agent_task_status` / `agent_task_result` 轮询与取结果，`agent_task_wait` 阻塞等待并在调用带 `_meta.progressToken` 时推送进度通知，`agent_task_steer` 向运行中的任务插话，`agent_task_cancel` 取消；skill 声明的 manifest 工具（如 `mal.search`）一并直通，且 skill 上传重载后无需重启即反映到 tools/list。`POGO_MCP_POLICY`（strict|standard|auto，默认 standard）决定新任务的审批档位。

页面和它的 js/css 不需要 token（浏览器没法给自己发现的 `<script>` 加请求头），**`/api/*` 一律需要**。前端从 URL 上取 token 存进 sessionStorage，所以同一个标签页刷新没问题；开新标签页要重新用带 token 的地址打开。

```bash
T=<token>; B=http://127.0.0.1:7777
SID=$(curl -s -X POST -H "Authorization: Bearer $T" $B/api/sessions \
      | sed 's/.*"session_id":"\([^"]*\)".*/\1/')

curl -N -H "Authorization: Bearer $T" "$B/api/sessions/$SID/stream" &   # 先订阅
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"prompt":"读一下 main.go，然后跑 go build"}' \
     "$B/api/sessions/$SID/messages"                                    # 再提问

# 批准一次 bash 调用（gate_id 来自 gate_request 事件）
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"action":"gate_decide","gate_id":"g1","allow":true}' \
     "$B/api/sessions/$SID/control"

# 不想被打断：接下来 3 轮全自动
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"action":"set_policy","mode":"auto","turns":3}' \
     "$B/api/sessions/$SID/control"
```

完整接口与事件清单见 `docs/web-ui-design.md` §6 / §11，设计取舍见同一份文档。

### 密码登录：users.json（可选）

默认是上面那条 token 单通道。把 `POGO_USERS_FILE` 指向一个 users.json 后启用**账号密码登录**（`web/auth.go`）：浏览器走登录页 + session cookie（HttpOnly、SameSite=Strict，7 天期；签名密钥持久化在 users.json 旁边，容器重建不踢人），**token 继续对脚本和 API 有效**——`/api/*` 的门禁是双通道，token 或登录会话任一放行（`web/server.go` 的 `authorized`）。文件不存在时启动会生成一个随机 admin 密码打印到日志（没有默认密码可扫）；登录按 IP 限流，5 次失败锁 15 分钟。

users.json 就是账号管理本身：一个 `{"users": [...]}` 数组，加一行就是加一个账号。账号行用 `-hash-password` 生成（从 stdin 读密码，不进 shell 历史和进程列表）：

```bash
$ echo '正确的密码' | pogo -hash-password
{"username":"<名字>","hash":"pbkdf2-sha256$600000$..."}
```

### 浏览器里的文件面板

dock 里有一个工作区文件面板：目录树、文件预览、以及 `Cmd/Ctrl-P` 的 quick open。两个面板同时打开时可以拖动分隔条调整占比。

- **路径走的是 agent 自己那套沙箱**（`tools.Resolve`），同一个 canonical 逃逸检查。**浏览器不该比模型看得更远。**
- 目录列表里藏掉 `.git`——一个文件面板里几千个 object 是噪声。这**不是安全边界**，逃逸检查才是。
- quick open 的索引额外跳过 `node_modules` / `dist`，否则项目自己的文件会被淹掉。这是一份**内置的噪声名单，刻意不去解析 `.gitignore`**（保持零依赖），而目录树仍然列出一切，留着偶尔要进 `node_modules` 看一眼的那条路。
- 图片预览只提供 `image/*`，靠嗅探判定并加 `nosniff`。原因很具体:响应跑在同一个 origin 上,而那个 origin 里存着 token——一个被当成 HTML 或 SVG 渲染的文件就能拿到它。
- **文件面板里的保存刻意不过审批闸门。** 闸门管的是模型;这个请求只可能来自 loopback 上持有 token 的人,也就是你自己。路径照样过同一个沙箱,而且这次写入**和 agent 的写入一样进 journal**,所以它会出现在下面那个「工作区改动」里。
- 「新建文件夹」只建一层。缺父目录是 404,而不是默默把一整条路径实体化——Finder 也是这个模型。

### 浏览器里的终端

dock 的另一半是一个真 pty 上的登录 shell,跑在这个会话自己的工作目录里。它回答的是一个很自然的问题:**agent 在改我的文件,让我自己也去那个目录里戳一下。** 走的是 websocket(`GET /api/sessions/{id}/terminal`),和其他端点同一道 token 闸门。

浏览器终端使用 `creack/pty` 开 pty、`nhooyr.io/websocket` 传帧。这两项与 CLI 的 `tui/` 无关；删除交互 TUI 不会自动删除浏览器终端或这两个依赖。当前 Go 还依赖 MCP、Schema、SQLite 等库，完整清单见 `go.mod`。

几条值得知道的性质:

- **shell 的寿命属于会话,不属于连接。** 关标签页、切会话只是 detach;下次 attach 会重放 backlog 环(256KB),所以一个跑了一半的 `make` 回来时还在滚。它只随会话消失(淘汰/删除/关服务)或你自己敲 `exit`——之后下次 attach 会开一个新的。
- **同时只有一个视图。** 第二个客户端 attach 会把第一个踢掉:最新那个窗口才是你真正在看的那个。
- shell 是你自己的 `$SHELL`,环境原样继承。比这更少都会让它在你已有的终端旁边显得是坏的。
- 关掉时**杀的是进程组**:shell 启动的 dev server 是它的进程组成员,只收割组长会把它们变成孤儿。这和取消一轮工具调用的处理是同一条规则。

**这一块把安全边界的话说完:一个未加认证的实例等于把 shell 作为服务开放出去。** 这就是为什么访问一律要过凭据（token 或登录账号）、默认只绑 `127.0.0.1`。`bash` 工具本来就没有路径限制,而这里连闸门都不在场——它本来就不是给模型用的。

### 工作区改动

「变更」标签有两个范围:

- **本会话**——从时间线的事件流投影出来,也就是这次会话的 `edit` / `write` 做了什么。
- **工作区**——从**首次触碰时的前像**(file journal)算出来的整个工作区差异。这是客户端答不了的那一半:它不知道 agent 开始之前文件长什么样。

journal 在第一次 `edit`/`write` 那个文件时记一份前像,之后重复调用会去重。渲染有两道上限:单边 2MB、patch 5000 行——超了仍然列出文件名和统计,**只扣掉 patch**。限制的是渲染,从不限制导航。

### 撤回（rewind）与 checkpoint

时间线上的任何一条你的提问都可以撤回:那条消息、回答它的每一轮、以及之后的一切,都从会话的 head 变得不可达。**什么都没有被删除**——transcript 是 append-only 的,撤回是在树上分叉,旧分支还在文件里。

对话之外还可以**把文件一起恢复**到「你发出那条消息时」的状态:改过的回去、删掉的回来、之后新建的被移除。这靠的是一个**影子 git 仓**,每次 run 开始前对整个工作树打一次快照。

几个设计决定:

- **影子仓在会话目录下,永远不在工作区里**(`<session-dir>/checkpoints/<项目>/`)。所以**你的工作区不需要是 git 仓库**,而如果它是,**你自己的 git 历史一个字都不会被碰**。
- 这是市场收敛到的做法(Codex 的 ghost commit、Gemini CLI 的 `~/.gemini/history`、Cline 的 per-task 影子仓),理由是**整树快照能抓住 per-file 钩子抓不到的东西**:bash 写的、删除、重命名。
- checkpoint 用**run 开始那一刻 transcript 的 head 记录 id** 命名(`refs/checkpoints/<recordID>`)。而撤回的分叉点恰好就是这样一个记录 id——**这就是两棵树的连接键**:对话在 JSONL 里分叉,影子分支在这里分叉。
- **checkpoint 的失败模式永远是「不可用」,绝不是「阻塞」。** 没有 git、目录不可写,run 照跑,只是那个点没有文件可恢复,撤回退化成只撤对话——也就是有 checkpoint 之前的行为。
- **恢复会预览并询问,不猜。** 快照分不清「agent 改的」和「checkpoint 之后你自己改的」,所以对话框先列出会动哪些文件(git 的 name-status:`M` 恢复 / `D` 找回 / `A` 删除)和各自的增删行数,并明说**之后新建的文件会被删除、你手动的改动会被覆盖**。二进制文件报「二进制」而不是编一个行数。
- **三档,而不是两档。** 「撤回对话并恢复文件」/「**只恢复文件,保留对话**」/「只恢复对话」。中间那一档服务的场景很具体:**这一轮的思路有用、但改动改坏了**——推理值得留在上下文里,产出不值得留在磁盘上。它不是第一档的弱化版,是另一件事。
- **可以只恢复其中几个文件。** 受影响文件列表里每一行都是一个勾选,默认全选,点一行把它排除(灰掉 + 删除线,不消失——「我漏了哪几个」是事后才问的问题)。所以「只恢复这一个文件」就是取消勾选其余的。**主按钮仍然是唯一会动手的东西**:确认对话框不该在你按下确认之前就产生不可逆副作用。
- **恢复先跑、分叉后跑,同一把锁**,所以撤回是全有或全无:恢复失败就完全不动对话,而且中间没有缝隙能让一次 run 挤进来。
- `reset --hard` 不管未跟踪文件,但被放弃的那次 run 新建的文件正是未跟踪的——所以后面跟一个 `clean -fd`。**被 ignore 的路径留着**,那正是 ignore 它们而不是删掉的意义。

#### 快照要有预算,撤回点要有保留期

每次 run 开始都打一次快照、而且一直留着,所以这两件事都是无界的:一次快照可以有多大、总共留多少个。

- **体积预算**:超过 512MiB 新增内容(或 2 万个新文件)的工作树**不打快照**,run 照跑,stderr 上说明是哪几个目录最大。判断交给 `git ls-files --others --exclude-standard` 而不是自己走目录树,因为只有 git 知道 `add -A` 到底会 stage 什么——你的 `.gitignore`、每一层嵌套的 `.gitignore`、还有影子仓自己的 `info/exclude`。自己走一遍的版本会因为一个 2GB 的构建目录关掉 checkpoint,而那个目录 git 本来就要 ignore。**这是拒绝,不是故障**:下一次 run 会重新判断,所以补一条 `.gitignore` 就好了,不用重启。
- **噪声清单**是 quick open 那份(`indexSkip`)的超集:多出来的是包管理器目录和工具缓存(`.venv`/`__pycache__`/`.gradle`/`.terraform` 等)。**`build/` 和 `target/` 故意不在里面**——它们在有些项目里是手写源码,而猜错的代价是静默的:一次撤回跳过了一个没人被告知的文件。名字只在「这个名字永远不是源码」时才配进清单,剩下的交给上面那条体积预算。
- **保留策略**:100 个撤回点或 30 天,先到先算,`-checkpoints-prune` 执行。清理由 `-checkpoints-prune` 显式触发,避免在一次无关的 run 里删除旧撤回点。在这之前答案是「永不清理」——影子仓会随着工作区被使用一直长。
- **清理必须重写幸存的撤回点**,否则一个字节都不会释放:每个 checkpoint 提交都以前一个为父,分支把整条链拴在一起,删掉旧 ref 之后新提交仍然把它列为祖先。所以 prune 会在同样的 tree 上重建一条新链——**ref 名不变,commit id 变**——然后 `gc` 才真的能丢掉只有被删除的点引用过的对象。改 id 是安全的,因为和 transcript 的连接键是 ref **名**:预览和恢复每次都重新解析这个名字。
- **排序不能用提交时间**。git 时间戳只有秒精度,几次快速的 run 会带着同一个时间,`--sort=-committerdate` 于是静默退化成按 ref 名排序——保留 `rec0`、`rec1` 而丢掉 `rec4`、`rec5`,正好是保留策略的反面。真正的顺序是提交链本身,所以用 `rev-list --all --topo-order` 拿一次全序。

#### Git 产品功能已退役

文件面板不再展示仓库状态、分支、提交入口、提交信息生成或提交规则，模型上下文也不再自动注入用户仓库状态。旧的 `/api/workspace/git`、`/api/workspace/commit`、`/api/workspace/commit/message`、`/api/workspace/commit/rules` 接口已移除，已认证请求返回 404。

「工作区改动」继续使用 file journal，保留文件差异和清空基线。它记录 `edit` / `write` 及文件面板保存；仅由 Bash 或外部编辑器修改的文件不再通过 Git 补进列表。工作区不需要 `.git`，这套查看能力也不依赖 Git 可执行文件。

上面的文件 checkpoint 和 rewind 继续使用独立影子仓，镜像仍保留 Git 来支持文件恢复。产品退役不会删除用户仓库、已有历史或旧 worktree 目录。完整范围见 [Git / worktree 退役说明](git-retirement.md)。

### 会话侧栏:重命名与置顶

侧栏每条会话的 ⋮ 菜单里可以重命名和置顶,置顶的排在前面(`PATCH /api/sessions/{id}`,`title` 和 `pinned` 两个字段可以单独发)。不改名的话标题取自第一条提问。

### 新建会话时选工作区

新建会话时可以挑服务根目录下的哪个子目录作为这个会话的工作目录(`POST /api/sessions` 的 `workspace` 字段,`""` 就是根)。选择器的模型是 Claude Code 那种「你正在看的那个文件夹就是选择」:行进入目录,底部始终写明将要用的是哪个,根目录是默认值一个回车之外。过滤框和键盘确认来自 VSCode 的 quick pick,内联的「新建文件夹」来自 Finder。


---

<a id="english"></a>

# Browser UI

## Browser UI

```bash
$ pogo -web
pogo web  model=glm-5.2  cwd=/Users/you/project
  http://127.0.0.1:7777/?token=6f1c...
```

Open that URL and you're in: session list on the left, timeline in the middle (thinking / tool calls / answers), input box at the bottom. `edit` changes render as a diff, `bash` output as a terminal, `todo` as a checklist, and calls needing approval pop a card on the spot.

**Only the latest task list counts.** A model rewrites the list several times during a session, and each occurrence is a card on the timeline. An `edit` diff is something that happened and is forever true; a list is **state at that moment**, superseded by the next write. So superseded ones collapse to one line and dim (clickable — "when did this item appear" is occasionally worth checking) — drawing them all at full strength, scrolling up to a "1/3 done" card, would read as current progress. The judgment recognizes only **ended-and-successful** writes: a refused call wrote nothing, an in-flight one has not written yet, and neither may downgrade a previous good list.

**You can send more while it runs.** While running, the send button turns into a blue "follow-up" and the message gets queued into the running turn: delivered after this turn's tool calls finish and before the next time the model is asked. So when you notice it going the wrong way you don't have to cancel the whole turn — just say "use X instead." If the turn happens to end the instant you press send, the text goes back into the input box with a notice, it does not vanish.

The top bar has a model switcher (switching preserves conversation history; disabled while running) and a **context usage bar**: yellow past 70%, red past 85%. That percentage is the size of the **most recent turn's prompt**, not the session cumulative — the cumulative grows much faster (every turn resends all history) and is only good for billing. Click it to expand a composition panel: what is eating context, and **which lever to pull** (see "Compaction" — the same data yields two opposite recommendations).

The usage bar now walks back on its own: by default context cleaning starts discarding old tool output around four-fifths of the window (see "Context cleaning"), and the next turn's measured prompt is smaller as a result. **So the bar's yellow/red is computed against the trigger line, not a fixed percentage of the window** — cleaning holds usage just under the trigger line; with a fixed percentage the bar would be yellow the whole time, and a permanently lit warning color carries no information. Green = not yet at the trigger; yellow = cleaning is running (normal); red = cleaning cannot keep up, you should act. With `-context-edit off` nothing pulls the prompt back down, and the band reverts to fixed 70%/85%. The reason it drops is exposed on the `context_edit` field of the `turn_start` event; the terminal prints a line, the browser currently only reflects it as the bar dropping. **But cleaning is not compaction**: a session bloated by conversation text rather than tool output has nothing to clean. What helps then is `/compact` (see "Compaction"), not starting a new session — click the bar and the composition panel tells you which one it is.

The page is embedded in the binary, no separate deploy. To rebuild after front-end changes:

```bash
cd web/ui && npm ci && npm run build   # output in web/ui/dist, embedded into the binary
cd ../.. && go build -o pogo .
```

For development use `pogo -web -web-dev http://localhost:5173` (in another terminal `cd web/ui && npm run dev`): the browser only talks to the Go server, non-API routes are reverse-proxied to vite, so single origin, the token still works, and HMR is usable.

**External panels: `-web-panel name=url` (repeatable).** Panel apps are reverse-proxied at `/panels/<name>/` (same origin as the page, no CORS), so scenario applications plug in without touching pogo. Register only backends you trust: panels share the page's origin, and like `-skill` the flag is an explicit operator decision. Panels can also be **registered at runtime**: the browser sheet's new-tab page offers a checked-by-default "open same-origin via the built-in proxy" option when a shortcut's URL is cross-origin — the token-gated `POST /api/panels` behind it (idempotent per URL, name derived from the card title), persisted to `<state-root>/panels.json` (`POGO_PANELS_FILE` to relocate) — same trust bar as the flag, with the decision moved from the command line to whoever holds the token in the UI. There is also an **off-by-default** auto-register switch, `POGO_PANEL_AUTO_REGISTER=1`: navigating to any page registers it as a panel. Leaving that on hands the "same-origin, token-holding" identity to the target of every navigation — the comment in `serve.go` records why it must be an explicit environment variable rather than a default. Container deployments also set `POGO_PANEL_LOOPBACK_MAP=host.docker.internal`: the proxy fetches server-side, and this rewrites the browser-user's loopback URLs to the host so they keep meaning what the user meant.

The right dock's rail is **fixed at three buttons**: **files** (the workspace), **panel**, and **skills & tools**. The panel is a container whose tenants are the shell and every registered `-web-panel`, switched from a dropdown in its header — registering more panels never grows the rail. The header also carries maximize (fills the width temporarily, not persisted) and the right/bottom layout toggle. The skills & tools panel is the management surface for skills: **by skill** it lists each one's description, on-disk path, and the tools its `tools.json` declares; **by tool** it lays out every declared tool (filterable); clicking a skill's name opens its `SKILL.md` — and the badges on a tool card are its approval treatment (read-only skips review, destructive always reviewed, directory-only not directly callable).

A panel can hand a line to the conversation (a "ask the agent about this" button, say) using MCP Apps' `ui/message` (JSON-RPC over postMessage):

```js
window.parent.postMessage({
  jsonrpc: "2.0", method: "ui/message",
  params: { role: "user", content: { type: "text", text: "analyse this sample" } },
}, window.location.origin)
```

pogo validates the origin and the sending iframe, then **fills the composer**. A panel never sends for you.

**Empty-state starters: `starters.json` in a skill directory (optional).** It supplies the cards on an empty conversation. pogo only renders and validates them — no domain content of its own — and without the file the empty state keeps its original one-line hint. Edits take effect on reload (the file is read per request). The store lives under the state root by default; `POGO_STARTERS_FILE` moves it onto a volume (container deployments point it at something like `/data`, so edits survive container rebuilds). The next-step chips after a turn are **generated asynchronously** by the server once a run ends (gated by `-followups`, on by default): the model call starts only after the answer has fully rendered, so it never adds latency to the answer it builds on, and the chips arrive as a `followups` event and survive a reload via the snapshot. The `followups` rule groups in this file are the fallback for when generation is off, fails, or deliberately stays silent.

```json
{
  "heading": "What are we doing today?",
  "send": false,
  "cards": [
    { "icon": "search", "title": "Find a sample", "label": "zero detections", "prompt": "…" },
    { "icon": "graph", "title": "Open the cluster graph", "panel": "样本库", "at": "#/clusters" }
  ],
  "followups": [
    { "when": ["mal-decompile"], "chips": [{ "title": "Write a Yara rule", "prompt": "…" }] }
  ]
}
```

A card carries exactly one action: `prompt` (fill the composer) or `panel` (open that panel, optionally at an `at` hash route). `send: true` makes a prompt card go out on click; the default is to fill. A follow-up group's `when` is matched against what the **last turn** did — its tool calls and its reply — and no match shows nothing; generated chips take precedence and these groups are the fallback. `icon` comes from a fixed allow-list — about 50 intent names (2026-08 snapshot; the list is `starterIcons` in `skills/starters.go`, and it is a closed set because the alternative is letting a file hand markup to the page). Cards are capped at 27 (the empty state pages them nine at a time, three pages); generated follow-up chips are capped at 8. Invalid cards are skipped with the reason on stderr.

Three things worth knowing:

**The lifetime of a run belongs to the session, not the connection.** Initiate (`POST /messages`) and subscribe (`GET /stream`) are separate endpoints, so closing the browser, refreshing, or switching tabs does not interrupt a running task. On reconnect the first frame is `snapshot`, carrying finalized messages, in-flight accumulated text, unfinished tool calls, and pending approval cards; network jitter can use `?from=<seq>` to take only the delta.

**bash requires your approval by default.** Three policy tiers: `strict` (asks even for write/edit), `standard` (default, asks only for bash), `auto` (allow all). Approval cards carry an absolute expiry timestamp, so the remaining time after a page refresh is still correct; timeout is treated as **deny**, and a deny is just an error message back to the model — the loop continues, the model can explain or pick another approach, no need to rerun the whole turn.

**Access always requires a credential: a token, or a login account.** Without `POGO_WEB_TOKEN` set, one is generated randomly per startup and printed. With `POGO_USERS_FILE` set, browsers can instead log in with a username and password (see the next section), while the token keeps working for scripts and APIs — either channel admits. It binds to `127.0.0.1` only by default and warns additionally when bound to an external address. The reason is in the security boundary below: the `bash` tool has no path restrictions.

**The MCP endpoints (other agents calling you) are off by default.** With `POGO_MCP=1` the same port serves two more endpoints: `/mcp` (Streamable HTTP, for current clients) and `/sse` (the 2024-11-05 legacy transport, for the peer that says "SSE" but cannot do `/mcp`). Credentials work like `/api`: the web token by default, or only `POGO_MCP_TOKEN` when set (so the machine channel can be revoked on its own). Send `Authorization: Bearer <token>`; `/mcp` also accepts `?token=`, `/sse` does not (the legacy handshake rebuilds its POST-back URL without the query). The tool surface and semantics are documented at the top of `web/mcp.go`: `agent_submit_task` submits a task (each task is an ordinary session, visible and approvable in this UI), `agent_task_status` / `agent_task_result` poll and fetch, `agent_task_wait` blocks until terminal and streams progress notifications when the call carries `_meta.progressToken`, `agent_task_steer` redirects a running task, `agent_task_cancel` cancels; manifest tools declared by skills (e.g. `mal.search`) pass through as well, and a skill upload reaches tools/list without a restart. `POGO_MCP_POLICY` (strict|standard|auto, default standard) sets the approval tier for freshly submitted tasks.

The page and its js/css do not need a token (a browser cannot add headers to `<script>` tags it discovers itself), **`/api/*` always does**. The front end takes the token from the URL and stores it in sessionStorage, so refreshing in the same tab is fine; a new tab has to be opened with the token-bearing URL again.

```bash
T=<token>; B=http://127.0.0.1:7777
SID=$(curl -s -X POST -H "Authorization: Bearer $T" $B/api/sessions \
      | sed 's/.*"session_id":"\([^"]*\)".*/\1/')

curl -N -H "Authorization: Bearer $T" "$B/api/sessions/$SID/stream" &   # subscribe first
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"prompt":"read main.go, then run go build"}' \
     "$B/api/sessions/$SID/messages"                                   # then ask

# Approve a bash call (gate_id comes from the gate_request event)
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"action":"gate_decide","gate_id":"g1","allow":true}' \
     "$B/api/sessions/$SID/control"

# Don't want to be interrupted: auto-approve the next 3 turns
curl -X POST -H "Authorization: Bearer $T" \
     -d '{"action":"set_policy","mode":"auto","turns":3}' \
     "$B/api/sessions/$SID/control"
```

Full endpoint and event list in `docs/web-ui-design.md` §6 / §11; design tradeoffs in the same doc.

### Password login: users.json (optional)

The default is the single token channel above. Point `POGO_USERS_FILE` at a users.json to enable **username/password login** (`web/auth.go`): browsers go through a login page plus a session cookie (HttpOnly, SameSite=Strict, 7-day TTL; the signing key persists beside users.json, so container rebuilds don't kick everyone out), while **the token keeps working for scripts and APIs** — the `/api/*` gate is dual-channel, admitting either the token or a login session (`authorized` in `web/server.go`). When the file does not exist, startup generates a random admin password and prints it to the log (there is no default password to scan for); logins are rate-limited per IP, 5 failures locking 15 minutes.

users.json *is* account management: a `{"users": [...]}` array, and adding a line adds an account. Account lines are generated with `-hash-password` (reads the password from stdin, so it never lands in shell history or process listings):

```bash
$ echo 'the password' | pogo -hash-password
{"username":"<name>","hash":"pbkdf2-sha256$600000$..."}
```

### File panel in the browser

A workspace file panel lives in the dock: directory tree, file preview, and `Cmd/Ctrl-P` quick open. With both panels open you can drag the divider to adjust the split.

- **Paths go through the agent's own sandbox** (`tools.Resolve`), the same canonical-escape check. **The browser should not see further than the model can.**
- `.git` is hidden from the directory listing — thousands of objects in a file panel is noise. This is **not a security boundary**, the escape check is.
- The quick-open index additionally skips `node_modules` / `dist`, otherwise the project's own files would be drowned out. This is a **built-in noise list that deliberately does not parse `.gitignore`** (to stay zero-dependency), while the directory tree still lists everything, leaving a path for the occasional need to peek into `node_modules`.
- Image preview serves only `image/*`, decided by sniffing and served with `nosniff`. The reason is concrete: the response runs on the same origin, and that origin holds the token — a file rendered as HTML or SVG could grab it.
- **Saving from the file panel deliberately does not go through the approval gate.** The gate governs the model; this request can only come from someone holding the token over loopback, i.e., you. The path still goes through the same sandbox, and this write **enters the journal the same way an agent write does**, so it shows up in "Workspace changes" below.
- "New folder" creates only one level. A missing parent is a 404, not a silent materialization of an entire path — Finder works the same way.

### Terminal in the browser

The other half of the dock is a login shell on a real pty, running in this session's own working directory. It answers a natural question: **the agent is editing my files, let me poke around that directory myself.** It goes over websocket (`GET /api/sessions/{id}/terminal`), behind the same token gate as the other endpoints.

The browser terminal uses `creack/pty` for the PTY and `nhooyr.io/websocket` for frames. It is separate from the CLI `tui/` package; retiring the TUI does not remove these dependencies. The current project also depends on MCP, Schema and SQLite libraries; see `go.mod`.

A few properties worth knowing:

- **The shell's lifetime belongs to the session, not the connection.** Closing a tab or switching sessions only detaches; the next attach replays the backlog ring (256KB), so a half-finished `make` is still scrolling when you come back. It only disappears with the session (eviction/deletion/server shutdown) or when you type `exit` yourself — after which the next attach opens a new one.
- **Only one view at a time.** A second client attaching kicks the first: the most recent window is the one you are actually looking at.
- The shell is your own `$SHELL`, environment inherited as-is. Anything less would make it look broken next to a terminal you already had open.
- On close, **the process group is killed**: the dev server the shell started is a member of its process group, and reaping only the leader would orphan them. This is the same rule as cancelling a tool call mid-turn.

**This piece closes the security boundary point: an unauthenticated instance equals offering a shell as a service.** That is why access always requires a credential (token or login account) and the default bind is `127.0.0.1` only. The `bash` tool already has no path restrictions, and here the gate is not even present — it was never meant for the model.

### Workspace changes

The "Changes" tab has two scopes:

- **This session** — projected from the timeline's event stream, i.e., what this session's `edit` / `write` did.
- **Workspace** — the whole-workspace diff computed from **the pre-image at first touch** (the file journal). This is the half the client cannot answer: it does not know what the file looked like before the agent started.

The journal records a pre-image the first time a file is `edit`ed/`write`en; subsequent calls dedupe. Rendering has two caps: 2MB per side, 5000 lines per patch — over the cap, the filename and stats are still listed, **only the patch is dropped**. What's limited is rendering, never navigation.

### Rewind and checkpoint

Any of your questions on the timeline can be rewound: that message, every turn answering it, and everything after, becomes unreachable from the session head. **Nothing is deleted** — the transcript is append-only; rewind is forking on a tree, and the old branch is still in the file.

Beyond the conversation you can also **restore the files** to "the state when you sent that message": changed files go back, deleted files come back, files created afterwards are removed. This is backed by a **shadow git repo** that snapshots the entire working tree before each run starts.

A few design decisions:

- **The shadow repo lives under the session directory, never in the workspace** (`<session-dir>/checkpoints/<project>/`). So **your workspace does not need to be a git repo**, and if it is, **not a word of your own git history is touched**.
- This is what the market has converged on (Codex's ghost commit, Gemini CLI's `~/.gemini/history`, Cline's per-task shadow repo); the reason is that **whole-tree snapshots catch things per-file hooks cannot**: what bash wrote, deletions, renames.
- The checkpoint is named with **the transcript head record id at the moment the run started** (`refs/checkpoints/<recordID>`). And the forking point of a rewind happens to be exactly such a record id — **this is the join key between the two trees**: the conversation forks in the JSONL, the shadow branch forks here.
- **The failure mode of a checkpoint is always "unavailable," never "blocking."** No git, directory not writable — the run goes ahead, there is just nothing to restore at that point, and rewind degrades to conversation-only — i.e., the behavior that existed before checkpoints.
- **Restore previews and asks, it does not guess.** A snapshot cannot tell "what the agent changed" from "what you changed after the checkpoint," so the dialog first lists which files will move (git's name-status: `M` restore / `D` recover / `A` delete) and each one's line delta, and states clearly that **files created afterwards will be deleted and your manual edits will be overwritten**. Binary files report "binary" rather than a made-up line count.
- **Three modes, not two.** "Rewind the chat and restore files" / "**restore files only, keep the chat**" / "rewind the chat only". The middle one serves a specific case: **the turn's reasoning was useful and its edit was not** — worth keeping in the context, not worth keeping on disk. It is not a weaker first option; it is a different operation.
- **A restore can be narrowed to some of the files.** Every row in the affected-files list is a toggle, all selected by default; clicking one leaves it out (dimmed and struck through, not removed — "which ones did I leave out" is asked after the fact). So "restore just this one file" is "untick the others". **The main button is still the only thing that acts**: a confirm dialog should not produce irreversible side effects before you confirm.
- **Restore runs first, fork runs after, under the same lock**, so rewind is all-or-nothing: a failed restore leaves the conversation completely untouched, and there is no gap for a run to slip into in between.
- `reset --hard` ignores untracked files, but the files created by the abandoned run are exactly untracked — so a `clean -fd` follows. **Ignored paths are left alone**, which is the point of ignoring them rather than deleting them.

#### Snapshots need a budget, rewind points need a retention period

A snapshot is taken at every run start and kept, so both of those are otherwise unbounded: how big one snapshot may be, and how many are kept.

- **Size budget**: a work tree with more than 512MiB of new content (or 20,000 new files) is **not snapshotted**; the run goes ahead and stderr names the largest directories. The decision is delegated to `git ls-files --others --exclude-standard` rather than a hand-rolled walk, because git is the only thing that knows what `add -A` would actually stage: your `.gitignore`, every nested one below it, and the shadow repo's own `info/exclude`. A hand-rolled walk would disable checkpointing over a 2GB build directory git was going to ignore anyway. **This is a refusal, not a failure**: the next run decides again, so adding a `.gitignore` line fixes it without a restart.
- **The noise list** is a superset of quick open's (`indexSkip`); what it adds is package-manager directories and tool caches (`.venv`, `__pycache__`, `.gradle`, `.terraform`, and so on). **`build/` and `target/` are deliberately absent** — both are hand-written source in some projects, and the cost of guessing wrong is silent: a rewind that skips a file nobody was told about. A name earns a place on that list only when the name is never source; the rest is the size budget's job.
- **Retention**: 100 rewind points or 30 days, whichever runs out first, applied by `-checkpoints-prune`. Cleanup is explicitly requested through `-checkpoints-prune` rather than deleting restore points during an unrelated run. Before this the answer was "never" — a workspace's shadow repo grew for as long as the workspace was worked in.
- **Pruning has to rewrite the survivors** or it frees nothing: every checkpoint commit parents the one before it, so the branch keeps the whole chain reachable and a newer commit still lists a deleted ref's commit as an ancestor. Prune therefore rebuilds a fresh chain over the same trees — **same ref names, new commit ids** — and only then can `gc` drop the objects that only the discarded points referenced. Rewriting ids is safe because the join key with the transcript is the ref **name**: preview and restore resolve it every time.
- **Ordering cannot come from commit dates.** Git timestamps have one-second resolution, so a handful of quick runs share one date and `--sort=-committerdate` silently falls back to sorting by ref name — keeping `rec0` and `rec1` while discarding `rec4` and `rec5`, the exact opposite of a retention policy. The real order is the commit chain, so a single `rev-list --all --topo-order` provides the total order.

#### Git product features have been retired

The file panel no longer exposes repository status, branches, commits, commit-message drafting or commit rules. User-repository state is no longer injected into the model context. The former `/api/workspace/git`, `/api/workspace/commit`, `/api/workspace/commit/message` and `/api/workspace/commit/rules` endpoints have been removed; authenticated requests receive 404.

Workspace changes still use the file journal, with diffs and baseline clearing. The journal records `edit`, `write` and file-panel saves. Files changed only by Bash or external editors are no longer added through Git. This view works without a `.git` directory or a Git executable.

The file checkpoints and rewind described above retain their separate shadow repository. Git stays in the runtime image for file restoration. Retirement does not delete user repositories, existing history or old worktree directories. See [Git / worktree retirement](git-retirement.md) for the scope.

### Session sidebar: rename and pin

The ⋮ menu on each session in the sidebar lets you rename and pin; pinned ones sort to the top (`PATCH /api/sessions/{id}`, `title` and `pinned` are independently submittable). Without a custom name, the title comes from the first question.

### Picking a workspace when creating a session

When creating a session you can pick which subdirectory under the server root this session works in (the `workspace` field of `POST /api/sessions`, `""` means root). The picker's model is Claude Code's "the folder you're looking at is the selection": rows enter directories, the bottom always states which one will be used, and root is one Enter away as the default. The filter box and keyboard confirmation come from VSCode's quick pick; the inline "new folder" comes from Finder.

