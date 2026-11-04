# 工具

## 工具

`bash` 与声明式命令工具在运行期间将输出写入临时文件，内存仅保留约 50 KB 尾部；最终返回仍遵循 2000 行 / 50 KB 上限。单次命令的输出上限为 **64 MiB**，超过后取消整个命令进程组，并明确返回输出超额错误；未完整保存的文件不会被标成“完整输出”。声明式命令的 stdout/stderr 按收到的顺序合并，切换通道时附加标记。此上限不限制命令主动写入工作区的产物文件。

流式显示的 256 KB 上限与上述执行上限分开：停止向界面发送后，不再积累待发送缓冲。完整输出只有在保存成功后才提供可读路径；磁盘故障时保留有限尾部并说明其余输出未能保存。

内置工具在 `tools/` 中实现；实际提供给模型的集合由运行环境、Agent 授权和当前任务上下文决定。管理页的完整目录不等于每个会话都拥有全部工具。下面先介绍基础文件工具，其他宿主工具见后文；Skill 目录还可以通过 `tools.json` **声明领域工具**（见[「声明式工具」](skills.md#声明式工具skill-的-toolsjson)）。

Agent 编辑器把原生工具分成“Skill 关联工具”和“一般工具”。关联工具显示英文名称和中文说明，勾选状态由其内置 Skill 控制，不能单独修改：

| 内置 Skill | 联动的原生工具 |
| --- | --- |
| `agent-workflows` | `agent_workflow_read`、`agent_workflow_edit`、`agent_workflow_enable`、`agent_workflow_run` |
| `background-tasks` | `background_task` |
| `browser-use` | `browser` |
| `skill-creator` | `skill_save` |

关联由宿主维护，只认可可信的内置 Skill 身份；普通同名 Skill、文档内容或外部工具声明不能授予原生权限。`agent-workflows` 的四个工具在本次对话加载该 Skill（读取其 `SKILL.md` 或显式调用）之后才向模型声明，授权和执行不受影响，详见[「Agent 内部工作流」](agent-internal-workflows.md#上下文按需加载)。新建配置由服务端按 Skill 选择统一计算关联工具；已采用关联规则的组不能通过旧客户端省略字段退回独立授权。旧 Agent 不自动迁移或扩大权限，原有部分授权保持有效，直至有权用户主动切换对应 Skill。一般文件工具和快捷方式删除、合并权限仍独立配置。

编辑器会在保存时明确提交本次采用的关联组。旧客户端如果选中了 Skill，却在工具数组中限制了它的关联工具，又没有确认整组关联，服务端会返回刷新和确认提示，不会静默补齐超出请求范围的工具权限。

后台任务使用统一的 `background_task` 入口；`watch`、`watch_test` 不再列为新的可选工具，保留旧授权的兼容执行。只拥有旧 `watch` 或 `watch_test` 不会获得完整 `background_task` 权限。`watch_decide` 是哨兵事件批次的上下文能力，仅在宿主分发真实事件时暴露，普通聊天不提供该工具。

| 工具 | 参数 | 说明 |
|---|---|---|
| `read` | `path` \| `paths[]`, `offset?`, `limit?` | 读文件，超过 2000 行或 50KB 截断并提示用 `offset` 续读。`paths` 一次读多个文件（额度均分，见下） |
| `ls` | `path?`, `limit?` | 列一层目录，目录带 `/`，含点文件，500 条或 50KB 截断 |
| `find` | `pattern`, `path?`, `limit?` | 按 glob 找文件。含 `/` 时匹配相对路径，否则匹配文件名。默认 200 条 |
| `grep` | `pattern`, `path?`, `include?`, `limit?` | Go 正则搜内容，输出 `path:line:text`。`(?i)` 开头即不分大小写。跳过二进制和 8MB 以上的文件，默认 100 条匹配 |
| `write` | `path`, `content` | 写文件，自动建父目录 |
| `edit` | `path`, `edits[{oldText,newText}]` | 精确字符串替换，`oldText` 必须唯一匹配 |
| `bash` | `command`, `workdir?`, `timeout?` | 执行命令，`timeout` 可调（默认 120 秒，上限 600 秒，超出按 600 秒执行），输出保留末尾 2000 行 / 50KB。**输出边跑边显示**，见下。`workdir` 换执行目录，见下 |

**参数会先按 schema 校验再执行**，而且在审批闸门之前 —— 一个缺字段的调用无论怎么批准都跑不起来。错误里会点名缺哪个字段、列出这个工具接受的全部字段，拼错时还会给线索（`file_path` → 「你是不是想写 path」）。未知字段一律容忍：模型常加些无害的额外键，为此废掉一轮不值得。

模型返回的非法 JSON（如字符串中的 `\$`、截断参数）不会执行，也不会自动猜测修正。循环返回明确的“未执行”结果，请模型重新生成合法参数。界面事件、重连快照和历史保存将原始参数按文本编码，并附上 `input_invalid` / `input_error`；合法的旧格式 `input` 保持原样。历史重载后仍保留原始错误参数，避免一次格式错误导致整个会话无法展示或保存；修正后的调用继续遵循原有权限、审批和幂等规则。

最终回答的引用与引文复核通过 `answer_review` 事件报告开始和结束，界面显示紧凑的动态图标、已用时间及结果。复核时模型实际返回的思考仍按普通 `thinking` 增量完整发送；拟修改的正文和工具请求在验证前不会显示或执行。复核默认独立限时 30 秒，超时、失败或取消保留原回答与已收到的思考，并持久保存复核状态。新快照支持恢复真实进度，旧会话的英文内部提醒只显示为静态中文说明，不推断仍在运行。复核完成只代表检查流程结束，不代表来源已被证明支持全部结论。

### 结构化提问（仅交互式 Web 主会话）

`request_user_input` 用于必须由用户决定的方案或偏好：每次 1–4 题，每题 2–4 个带说明的选项，支持单选、多选和自由文本。问题有稳定 ID，宿主生成请求 ID 和版本，回答只恢复原工具调用；重复提交相同回答幂等，旧版本、跨会话提交、超时和取消均不会自动选择推荐项，也不构成其他工具的执行许可。子 Agent、无人值守任务和同名外部 Skill/MCP 工具没有这个宿主能力。

待答状态保存在会话 Hub 中，页面刷新与 SSE 重连可以恢复；进程重启不会恢复一个已中断的同步等待调用。已回答的问题和答案随普通工具结果进入会话记录。

`request_user_input_async` 用于可选偏好和补充信息：工具只登记问题并立即返回受理回执，Agent 可以继续或结束当前回合。问题按会话持久化，任务结束和 Docker 重启后仍可回答；回答经 Inbox 幂等交接，活跃回合在工具边界接收，空闲时开启新的后续回合，不重复执行原工具。记录区分 `pending`、`answered`、`delivered` 和 `cancelled`，回答保存与交付确认分开，交付失败可重试，不会把已回答误记为已交付。异步问题没有同步调用的 30 分钟超时，也不构成执行授权。

### `bash` 的 `workdir`：比 `cd x && ...` 好的那个答案

每次调用都是一个新的 `bash -c`，`cd` 和环境变量都不跨调用。这一点两家大厂给了两个不同的答案，pogo 跟的是后一个：

- **Anthropic 的 Claude Code 让 cwd 持久**。主会话里 `cd` 之后，后续 Bash 命令还在那个目录，只要没跑出项目目录或 `--add-dir` 加的目录；跑出去就重置回项目目录，并在结果里追加一行 `Shell cwd was reset to <dir>`。子代理会话从不继承目录变更。可以用 `CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1` 关掉这个继承。环境变量仍然不持久。（[Claude Code 工具参考](https://code.claude.com/docs/en/tools-reference)，内容已转述以符合许可要求）
- **OpenAI 的 Codex 不持久，而是给 `workdir` 参数**，和 `command`、`timeout_ms` 并列。它甚至是 `required` 的——那是 strict schema 要求「每个 property 都得进 required」的后果，不是「每次调用都该显式给目录」的判断。（[Codex issue #524](https://github.com/openai/codex/issues/524) 里那条 schema 报错正好暴露了这个形状）

pogo 的 bash 本来就是每次新进程，所以 Codex 那个答案是匹配的那个：

```json
{"command": "make", "workdir": "sub"}
```

相对路径按工作目录解析，绝对路径直接用。**不设为 required**：那会让每一次调用都带一个多余的 `"."`，而且现存 transcript 会全部不再符合 schema。

三件必须写清的事：

- **它不过路径守卫，这是有意的。** bash 从来没有路径限制（`tools/tool.go` 的注释里写明了），而在 `workdir` 上加一道、命令串里的 `cd /etc` 又照样能跑，那是一道读起来像边界但不是边界的限制，还会拒掉这个参数本来要服务的场景——在隔壁 checkout 里跑一次构建。bash 真正的边界是 OS 沙箱（Linux 上是 bubblewrap，见[「安全边界」](security.md#安全边界)），沙箱之外还有容器。
- **但它必须过 subagent 的守卫。** 守卫拦 `cd` 靠的是从命令文本里解析出绝对路径目标，而 `workdir` 到达同一个地方却从不出现在那段文本里。不补这一道检查，加这个参数就等于给子代理开了一个「长得像普通参数」的越界出口。`Guard.CheckDir` 就是这道检查，测试直接跑工具而不是调守卫。
- **它参与「总是允许这条命令」的键。** 在有 `workdir` 之前，「总是允许 `go build ./...`」只可能意味着「在会话目录里」，因为没有别的地方可跑。继续只按命令串做键，就会把当初那一次点击悄悄提升成「在 agent 说出的任何目录里」——一次用户没被展示、也不可能有意做出的放宽。所以键是 `命令 + workdir`；给裸命令做的授权不覆盖带 `workdir` 的调用，反之亦然。

目录不存在或指向一个文件，都由工具本身报错，不让 bash 起不来。差别对模型是实的：`chdir: no such file or directory` 加 exit code -1 描述的是这个 harness，而一条点名解析后的绝对路径和解析基准的消息描述的是那个错误。命令实际跑在非默认目录时，`BashDetails.workdir` 会记下解析后的绝对路径，界面上也会显示——只在非默认时显示，因为每次都播报默认值等于什么都没说。

### `read` 的 `paths[]`：一次调用读多个文件

```json
{"paths": ["a.go", "b.go", "sub/c.go"]}
```

`path` 和 `paths` 二选一（`offset` / `limit` 只跟 `path` 配对）。**额度均分而不是每个文件一份**：五个文件各拿满上限就是五倍上限，而这个上限的存在意义就是防止一个 tool_result 吃掉上下文窗口。一个路径失败不让整次调用失败——那正好浪费掉这次调用本来要省的那趟往返；全部失败才返回 error。

值得记一条：**Anthropic 和 OpenAI 的 read 都是单文件的**，`paths[]` 是 pogo 自己的扩展。两家在「一次调用做更多事」上的官方答案是 shell 那一侧（Codex 的 `commands[]` 数组 + 逐条 outcome），而那条 pogo 因为审批闸门否决了（见 `docs/harness-design.md` §5）。

界面上多文件读现在有自己的卡片：文件列表 + 每个文件的行数 / 截断 / 失败标记，点开看单个文件的内容（带语法高亮和行号）。**内容是按工具记录的字节区间切出来的，不是把拼接文本再解析回去。** 后者有一个不能有的失败模式：一个文件的内容里如果出现 `==> other.go <==` 这样一行，它的分段会提前结束，剩下的被算到下一个文件名下——把一个文件的片段挂在另一个文件的名字下面，比什么都不显示更糟。区间由写文本的同一段代码算出来，所以不可能和格式脱节。

**`bash` 的输出边跑边显示**，所以 `go test` 跑一分钟时能看出是在跑还是卡了：

```
· bash {"command":"for i in 1 2 3; do echo \"tick $i\"; sleep 2; done"}
  │ tick 1
  │ tick 2
  │ tick 3
  [exit 0, 6.0s, 3 lines]
```

终端里只在**单个工具在飞**时这样打印 —— 两个命令交织到同一个终端没法读，而这里没有 pane 可以分开放。结束时打的是状态行而不是把已经滚过去的输出再打一遍。Web 界面不受这个限制，而且中途刷新页面也能看到已经产生的输出。

**工具失败不会中断 loop。** 错误文本作为结果回传给模型，让它自己纠正。这是从 pi 继承的最关键的设计：

```
> 把 dup.txt 里第二个 'x = 1' 改成 'x = 99'
· edit {"edits":[{"oldText":"x = 1","newText":"x = 99"}],"path":"dup.txt"}
  ! edits[0].oldText matches 2 places in dup.txt. Add surrounding context to make it unique
· read {"path":"dup.txt"}
· edit {"edits":[{"oldText":"y = 2\nx = 1","newText":"y = 2\nx = 99"}],"path":"dup.txt"}
  Successfully replaced 1 block(s) in dup.txt
```

## 第九个工具:todo

模型自己决定要不要列清单。三步以上、或者你一次交代了好几件事，它会先写一份；单步、琐事、纯提问不写——一条项目的清单帮不了任何人。之后每开工一项、每做完一项就改一次，**整表替换**，没有增量 patch，所以不存在「改到一半」的中间态。

```
· todo {"todos":[{"task":"把超时从 30s 改成 60s","status":"in_progress"}, …]}
  1/3 done
  ✓ 找到超时常量在哪定义
  ▸ 把超时从 30s 改成 60s
  ○ 跑 go test ./config
```

五个状态：`pending` / `in_progress` / `completed` / `cancelled` / `blocked`。后两个不是凑数——「测试没过就不许标完成」这条规则需要一个落点（`blocked`），而计划中途作废的项直接删掉会丢掉「它曾被考虑过」这个记录（`cancelled`）。

**「同时只能有一个 in_progress」是校验强制的，不是提示里请求的。** 走 `ValidateArgs` 那条路，所以它在审批闸门之前就被拦下，错误里点名是第几项：

```
at most one task may be in_progress, but items 1, 3 are all marked in_progress.
Mark the one you are actually working on now and leave the rest pending
```

这跟工具失败不中断 loop 是同一套东西——模型读到这句自己改一次就好。三份参考实现里只有 Gemini CLI 这么做，另外两家都只在提示里恳求。

### 这个工具不持有任何状态

**当前清单就是历史里最新的那条 `todo` 工具结果。** 没有字段、没有锁、没有新的落盘记录：

- 工具在批次 goroutine 上跑，而 `Agent.mu` 刻意只守 steering。加一个字段就是为一件 transcript 已经存着的事新开一个并发面。
- 清单作为普通消息进会话文件，所以 `-resume` 不需要新的记录类型就能恢复。
- 每次更新是**追加**而不是重写，前缀缓存完好。这一点是硬约束而非偏好：Kimi 和 Zhipu 的上下文缓存都是隐式自动的，**没有任何 API 可以编辑已缓存的前缀**，所以一次重写要按全价重付。

代价是被取代的旧清单堆在历史里。那是驱逐层的活，不是这个工具的——只有最新那条是活的，更早的就是普通的过期工具输出。

### subagent 拿不到这个工具

和 subagent 工具同一个道理，同一个位置（`toolOptions`）：**按深度不注册**，而不是注册了再拒绝。不存在的工具不花 schema token，也不会被浪费一轮去尝试。

三家都是这么做的：OpenCode 有一个专门的模块 `deriveSubagentSessionPermission`，在同一条规则里把 `todowrite` 和「继续往下派」一起默认 deny；Claude Code 的 explore / general-purpose 两个子 agent 的提示里根本没提过 TodoWrite；Gemini CLI 的 `codebase-investigator` 只给四个只读工具的白名单。

理由是**子的清单没有读者**。清单挣回 token 靠两件事：给人看进度，以及穿过上下文压缩边界让 agent 还知道自己在哪。子活一次 run、10 分钟超时封顶，永远碰不到压缩边界；而进度早就在父的 subagent 卡片里，以更细的粒度（轮次边界、每次工具调用及其成败）显示着。剩下的只有「现在在做什么」多出第二个写者——而这个问题必须只有一个答案。

反过来才是真正的结合点：**清单是委派的驱动方，而委派回来不等于那一项完成。** edit 子交回的是一个钉在 `refs/pogo/sub/<id>` 上、还没被 cherry-pick 的 commit，所以「子报告成功」和「这项可以标 completed」之间至少还差一次合回和一次验证。工具描述里写了这句。

### browser（仅 web）

web 会话多注册一个 `browser`：单工具多 action，动作声明由实际后端和图像查看链路决定。共同动作包括 snapshot/find/click/type/select/press/hover/scroll/navigate/wait/extract/batch；原生后端增加 tabs/tab，并在图像链路可用时增加 screenshot/坐标点击，不提供 eval。旧面板提供 eval，不提供原生专属动作。每次操作经 `BrowserGate` 授权后交给对应执行器，用户看到同一页面。详见[原生浏览器运行时](native-browser-runtime.md)。

每次调用和每个 batch 子项都必须有 action；batch 限制 1–12 步且不能嵌套。已知后端在执行前校验整批字段，错误参数不会先执行前半段。type 必须提供 text（空串清空），select 的选项也放在 text，不自动猜测错误别名。

两道硬边界：

- **意图门是结构性的，不是提示词级的。** 工具注册在每个 web 会话里，但只有用户以 `/skill:browser-use <任务>` 显式发起（skill frontmatter 声明 `browser-task: true`，消息因此展开）时才 arm，run 结束即 disarm——普通对话里模型调用它会得到一个说明用法的错误。arm 的那一下同时就是审批：任务内的页面动作不再逐次弹卡。
- **限度写在参数校验里**：`batch` 一次最多 12 步、步内不许再嵌 batch；`wait` 上限 30 秒；`eval` 的返回值截到 16000 字符。

终端拿不到这个工具——没有面板可执行，和 read-only 会话拿不到 write 是同一条结构规则（面板链接为 nil 就不注册）。**委派的子 agent 是例外**：它经 `POGO_BROWSER_FD` 管道借用父会话的面板执行（90 秒租约、并发借用排队，`tools/browserfd.go`），armed 门与审批都由父会话裁决，和子代理的 bash 审批走同一条父裁决通道；借来的面板不能再向下转借。

### 后台委派与 subagent_tasks（仅 web）

web 会话里 subagent **默认后台跑**：调用立即返回一个 task id，子继续干活，父可以同时做别的；结果稍后以一条 `<task-notification>` 消息送达会话（`tools/subagent_bg.go`、`web/background.go`）——不要 sleep、轮询或重发任务去等它，通知就是唯一有效的等法。只有「没有结果就走不了下一步」时才传 `"wait": true` 同步等。`subagent_tasks` 工具列出还没回来的委派（运行中/排队）。未完成的委派同时最多 8 个（含排队），跑起来的那部分仍受并发上限约束。

终端没有这个组合：一次性进程没有「稍后」可以投递结果，所以它的委派一律同步——`wait` 参数只在有后台 runner 的会话里才出现在 schema 里，`subagent_tasks` 也只随 runner 注册。

### analyze_image（仅 web，配置了 vision_model 才注册）

providers.json 里写了 `vision_model` 的部署，web 会话会多注册一个 `analyze_image`（`tools/image.go`、`web/vision.go`）：文本模型把用户附件或当前工作区里的图片连同一句话问题交给配置的视觉模型描述，描述作为工具结果回到会话里。图片可用附件名、引用或工作区内的相对/绝对路径指定。它存在的原因是主力模型不收图：与其整个会话迁去一个收图但更贵的模型，不如让「看一眼」成为一个可以按需调用的工具。配置细节见[「模型」](configuration.md#模型)。


---

<a id="english"></a>

# Tools

## Tools

Nine resident in the terminal, all under `tools/`; **a web session registers two more** — `browser` and `subagent_tasks` — and when a `vision_model` is configured in providers.json, also `analyze_image` (hands images to a vision model, end of this section), **twelve at most** (`tools/tool.go`, `web/manager.go`). The table below is the seven resident ones; the eighth `subagent` and the ninth `todo` each get their own section, and the web-only ones are at the end of this section. On top of these, a skill directory can carry a `tools.json` that **declares domain tools** (see [Declarative tools](skills.md#declarative-tools-a-skills-toolsjson)) — they register as first-class tools, after the built-ins.

| Tool | Args | Description |
|---|---|---|
| `read` | `path` \| `paths[]`, `offset?`, `limit?` | read a file; truncated past 2000 lines or 50KB with a hint to continue via `offset`. `paths` reads several in one call (budget split, see below) |
| `ls` | `path?`, `limit?` | list one directory level; directories carry `/`, dotfiles included, truncated at 500 entries or 50KB |
| `find` | `pattern`, `path?`, `limit?` | find files by glob. With `/` it matches the relative path, otherwise the filename. Default 200 entries |
| `grep` | `pattern`, `path?`, `include?`, `limit?` | search content with a Go regex; output `path:line:text`. Leading `(?i)` is case-insensitive. Skips binary and files over 8MB; default 100 matches |
| `write` | `path`, `content` | write a file; auto-creates parent directories |
| `edit` | `path`, `edits[{oldText,newText}]` | exact string replacement; `oldText` must match uniquely |
| `bash` | `command`, `workdir?`, `timeout?` | run a command; `timeout` is adjustable (default 120s, ceiling 600s — larger values are clamped to 600s); output keeps the last 2000 lines / 50KB. **Output streams while running**, see below. `workdir` changes where it runs, see below |

**Args are validated against the schema before execution**, and before the approval gate — a call with a missing field cannot run no matter what you approve. The error names the missing field, lists every field the tool accepts, and on a misspelling gives a hint (`file_path` → "did you mean path"). Unknown fields are always tolerated: the model often adds harmless extra keys, and wasting a turn over that is not worth it.

### Structured questions (interactive web sessions only)

`request_user_input` collects decisions that genuinely belong to the user: 1–4 questions, each with 2–4 described options, single- or multi-select, and custom text. Host-owned request IDs and revisions bind answers to the original call. Identical retries are idempotent; stale, cross-session, cancelled and timed-out submissions never select a recommendation or authorize another tool. Children, unattended runs and same-named external tools do not receive this capability. Browser reconnects restore pending questions from the session Hub; a process restart does not resume an interrupted synchronous wait, while settled answers remain ordinary persisted tool results.

`request_user_input_async` registers optional preferences and returns an acknowledgement immediately. Questions persist across completed turns and process restarts. Answers use the existing Inbox for exactly-once delivery: an active run receives them at a tool boundary, while an idle session starts a new turn instead of re-executing the original tool. `pending`, `answered`, `delivered` and `cancelled` are distinct states; delivery failures remain retryable, and an asynchronous answer never grants execution approval.

### `bash`'s `workdir`: the better answer than `cd x && …`

Every call is a fresh `bash -c`, so neither `cd` nor environment variables carry over. The two major vendors answer this differently, and pogo follows the second:

- **Anthropic's Claude Code makes cwd persist.** After a `cd` in the main session, later Bash commands stay in that directory as long as it is inside the project directory or one added with `--add-dir`; leaving those resets to the project directory and appends `Shell cwd was reset to <dir>` to the result. Subagent sessions never inherit a directory change. `CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1` turns the carry-over off. Environment variables still don't persist. ([Claude Code tools reference](https://code.claude.com/docs/en/tools-reference); content was rephrased for compliance with licensing restrictions)
- **OpenAI's Codex doesn't persist and gives a `workdir` parameter instead**, alongside `command` and `timeout_ms`. It is even `required` — a consequence of the strict-schema rule that every property must be listed, not a judgement that a caller should always name a directory. ([Codex issue #524](https://github.com/openai/codex/issues/524), whose schema error exposes the shape)

pogo's bash is a fresh process per call already, so Codex's answer is the matching one:

```json
{"command": "make", "workdir": "sub"}
```

A relative path resolves against the working directory; an absolute one is used as given. **Not required**: that would put a redundant `"."` on every single call, and would make every existing transcript invalid against the schema.

Three things worth stating plainly:

- **It does not go through the path guard, deliberately.** bash never had a path restriction (the reasoning is in `tools/tool.go`), and confining `workdir` while `cd /etc` in the command string still works would be a restriction that reads like a boundary without being one — while refusing the case the parameter exists for, running a build in a checkout next door. bash's real boundary is the OS sandbox (bubblewrap on Linux — see [Security Boundary](security.md#security-boundary)), and beyond that, the container.
- **But it must go through the subagent guard.** The guard catches a `cd` by parsing an absolute target out of the command text, and `workdir` reaches the same place without ever appearing there. Left unchecked, adding the parameter would have handed a child a way out of its workspace that reads as an ordinary argument. `Guard.CheckDir` is that check, and its test runs the tool rather than calling the guard.
- **It is part of the "always allow this command" key.** Before `workdir` existed, "always allow `go build ./...`" could only mean "in the session's directory", because there was nowhere else to run. Keying on the command alone would silently promote that click to "in any directory the agent names" — a widening the user was never shown and could not have intended. So the key is command + workdir; a grant made for the bare command doesn't cover a `workdir` call, and vice versa.

A directory that doesn't exist, or that is a file, is reported by the tool rather than left to bash failing to start. The difference is real for the model: `chdir: no such file or directory` with exit code -1 describes the harness, while a message naming the resolved path and the base it was resolved against describes the mistake. When a command ran somewhere other than the default, `BashDetails.workdir` records the resolved absolute path and the UI shows it — only when it is not the default, because announcing the default on every call says nothing.

### `read`'s `paths[]`: several files in one call

```json
{"paths": ["a.go", "b.go", "sub/c.go"]}
```

`path` and `paths` are mutually exclusive (`offset` / `limit` pair with `path` only). **The budget is divided, not repeated**: five files at the full ceiling each is five times the limit that exists to stop one tool result from swallowing the context window. One failed path does not fail the call — that would waste the very round trip the call was saving; only all of them failing returns an error.

Worth recording: **both Anthropic's and OpenAI's read tools are single-file**, so `paths[]` is pogo's own extension. Their first-party answer to "do more in one call" is on the shell side (Codex's `commands[]` array with a per-command outcome), and pogo rejected that one because of the approval gate — see `docs/harness-design.md` §5.

A multi-file read now has its own card: the file list with per-file line counts, truncation and failure marks, expandable to one file's content with syntax highlighting and line numbers. **Content is sliced at byte ranges the tool recorded, not parsed back out of the concatenated text.** The latter has a failure this must not have: a file whose own content includes a line reading `==> other.go <==` ends its section early and the remainder is credited to the next file — one file's fragment under another file's name, which is worse than showing nothing. The ranges are computed by the same code that writes the text, so they cannot drift from the format.

**`bash` output streams while running**, so when `go test` runs for a minute you can tell whether it's running or stuck:

```
· bash {"command":"for i in 1 2 3; do echo \"tick $i\"; sleep 2; done"}
  │ tick 1
  │ tick 2
  │ tick 3
  [exit 0, 6.0s, 3 lines]
```

The terminal only prints like this when **a single tool is in flight** — two commands interleaved into the same terminal is unreadable, and there is no pane here to split them into. At the end, what's printed is the status line, not a repeat of the already-scrolled output. The web UI is not bound by this, and refreshing the page mid-flight also shows what was produced so far.

**A tool failure does not break the loop.** The error text is returned to the model as the result, letting it self-correct. This is the most important design inherited from pi:

```
> change the second 'x = 1' in dup.txt to 'x = 99'
· edit {"edits":[{"oldText":"x = 1","newText":"x = 99"}],"path":"dup.txt"}
  ! edits[0].oldText matches 2 places in dup.txt. Add surrounding context to make it unique
· read {"path":"dup.txt"}
· edit {"edits":[{"oldText":"y = 2\nx = 1","newText":"y = 2\nx = 99"}],"path":"dup.txt"}
  Successfully replaced 1 block(s) in dup.txt
```

## The Ninth Tool: todo

The model itself decides whether to make a list. With three steps or more, or when you give it several things at once, it writes one first; a single step, a trifle, a pure question gets none — a one-item list helps nobody. Afterwards it rewrites on every item started and every item finished, **whole-table replace**, no incremental patch, so there is no "half-modified" intermediate state.

```
· todo {"todos":[{"task":"change timeout from 30s to 60s","status":"in_progress"}, …]}
  1/3 done
  ✓ find where the timeout constant is defined
  ▸ change timeout from 30s to 60s
  ○ run go test ./config
```

Five states: `pending` / `in_progress` / `completed` / `cancelled` / `blocked`. The last two are not filler — "tests didn't pass, you may not mark it complete" needs a slot (`blocked`), and an item dropped mid-plan would lose the record that "it was once considered" if just deleted (`cancelled`).

**"At most one in_progress" is enforced by validation, not requested in the prompt.** It goes through the `ValidateArgs` path, so it is caught before the approval gate, and the error names which items:

```
at most one task may be in_progress, but items 1, 3 are all marked in_progress.
Mark the one you are actually working on now and leave the rest pending
```

This is the same machinery as "tool failure does not break the loop" — the model reads this and corrects itself once. Of three reference implementations only Gemini CLI does this; the other two only plead in the prompt.

### This tool holds no state

**The current list is the latest `todo` tool result in the history.** No field, no lock, no new on-disk record:

- The tool runs on the batch goroutine, and `Agent.mu` deliberately only guards steering. Adding a field would open a new concurrency surface for something the transcript already stores.
- The list enters the session file as an ordinary message, so `-resume` needs no new record type to restore it.
- Each update is an **append**, not a rewrite; prefix caching stays intact. This is a hard constraint, not a preference: Kimi's and Zhipu's context caches are implicit and automatic, **there is no API to edit an already-cached prefix**, so one rewrite would be rebilled at full price.

The cost is that superseded old lists pile up in the history. That is the eviction layer's job, not this tool's — only the latest one is live, earlier ones are ordinary stale tool output.

### The subagent does not get this tool

Same reasoning as the subagent tool, same place (`toolOptions`): **not registered by depth**, rather than registered-then-refused. A tool that does not exist costs no schema tokens and cannot be wasted a turn trying.

All three vendors do this: OpenCode has a dedicated module `deriveSubagentSessionPermission` that default-denies `todowrite` together with "delegate further down" in one rule; Claude Code's explore / general-purpose sub prompts never mention TodoWrite at all; Gemini CLI's `codebase-investigator` gets a whitelist of only four read-only tools.

The reason is that **the sub's list has no reader.** A list earns its tokens back by two things: showing a human progress, and carrying the agent's "where am I" across a context-compaction boundary. A sub lives one run, capped at a 10-minute timeout, and never reaches a compaction boundary; and its progress is already shown in the parent's subagent card, at finer grain (turn boundaries, each tool call and its success/failure). What's left is only a second writer for "what is being done right now" — and that question must have exactly one answer.

The reverse is the real junction: **the list is the driver of delegation, and a delegation coming back does not equal that item being done.** What an `edit` sub hands back is a commit pinned at `refs/pogo/sub/<id>` and not yet cherry-picked, so between "the sub reports success" and "this item can be marked completed" there is at least one merge-back and one verification still missing. The tool description says so.

### browser (web only)

A web session registers `browser`, whose actions reflect its actual backend and image delivery path. Common actions are snapshot/find/click/type/select/press/hover/scroll/navigate/wait/extract/batch. Native adds tabs/tab and, when image inspection is available, screenshot/coordinate clicks; it does not expose eval. Legacy panels expose eval without native-only actions. BrowserGate retains task authorization before forwarding to either executor. See [Native browser runtime](native-browser-runtime.md).

Every call and batch item requires action. Batches contain 1–12 non-nested steps; known backends validate all fields before executing any step. type requires explicit text (an empty string clears); select also takes its option in text. Incorrect aliases are rejected rather than guessed.

Two hard boundaries:

- **The intent gate is structural, not prompt-level.** The tool is registered in every web session, but it is armed only when the user explicitly starts a browser task with `/skill:browser-use <task>` (the skill's frontmatter declares `browser-task: true`, so the message expands it), and disarmed the moment that run ends — in an ordinary conversation a model calling it gets an error that explains the rules. Arming is also the approval: page actions inside the task skip the per-action card.
- **The limits live in argument validation**: one `batch` runs at most 12 steps and a step may not itself be a batch; `wait` caps at 30 seconds; an `eval` return value is truncated to 16000 characters.

The terminal does not get this tool — there is no pane to execute in, the same structural rule that keeps `write` out of a read-only session (a nil pane link means no registration). **A delegated subagent is the exception**: it borrows the parent session's pane over the `POGO_BROWSER_FD` pipe (90-second lease, concurrent borrows queue, `tools/browserfd.go`); the armed gate and the approvals are decided by the parent, over the same ask-the-parent channel as the subagent's bash approvals. A borrowed pane cannot be lent further down.

### Background delegation and subagent_tasks (web only)

In a web session a subagent **runs in the background by default**: the call returns a task id immediately, the child keeps working while the parent does other things, and its result arrives later as a `<task-notification>` message in the session (`tools/subagent_bg.go`, `web/background.go`) — never sleep, poll, or re-issue the task to wait; the notification is the only wait that works. Pass `"wait": true` only when no useful next step exists without the result. The `subagent_tasks` tool lists the delegations still outstanding (running or queued). At most 8 delegations may be outstanding at once (queued ones included); the running subset is still bounded by the concurrency cap.

The terminal has none of this: a one-shot process has no "later" to deliver a result in, so its delegations stay synchronous — the `wait` parameter only appears in the schema where a background runner exists, and `subagent_tasks` is only registered alongside one.

### analyze_image (web only; registered when a vision_model is configured)

Deployments whose providers.json declares a `vision_model` get one more web-session tool, `analyze_image` (`tools/image.go`, `web/vision.go`): the text model hands an attached image or any image in the session workspace, plus a one-line question, to the configured vision model, and the description comes back as a tool result. Images can be named by attachment reference or by a workspace-relative/absolute path. The reason it exists is that the main model does not take images: rather than move the whole session to a costlier model that does, "look at this" becomes a tool called on demand. Configuration is in [Models](configuration.md#models).

### Agent 私有快捷方式（Web）

绑定到 Agent 的 Web 会话通过 `shortcut_query` 查询资料，通过 `shortcut_manage` 创建和整理快捷方式。删除和合并仍是独立工具及权限。查询返回宿主授权的资料快照；管理需要在 Agent 的原生工具选择中启用，审批规则与 `write` / `edit` 一致。普通未绑定会话、CLI、子 Agent 和独立工作流执行器不获得这份私有存储的写入口。

快捷方式的目录整理由独立宿主工具提供，不需要业务 MCP，也不通过修改底层 JSON 绕过权限：

| 工具 | 能力 |
| --- | --- |
| `shortcut_query` | `action: search/read/list` 分别搜索资料、读取快照及浏览目录。分页、面包屑、只读来源与版本信息保持原语义；空 `parent_id` 表示根目录。 |
| `shortcut_manage` | `action: create/update/folder_create/folder_rename/move` 分别创建卡片、更新卡片、新建文件夹、文件夹改名和移动。参数与 `action` 放在同一层；更新使用 query/read 返回的完整 graph，保留混排和嵌套。 |
| `shortcut_delete` | 删除指定卡片或目录；非空目录需要显式 `recursive:true`，不删除原文件或历史引用所需的资产。 |
| `shortcut_merge` | 默认仅预览同目录的完全重复卡片；提供原计划 ID、版本和 `apply:true` 才应用，保留一个稳定 ID。 |

`shortcut_query` 为只读基础能力。新启用 `shortcut_manage` 授予其五个管理动作，不包含删除或合并。旧配置中的细分工具名继续有效：例如只勾选过 `shortcut_create`，模型看到的管理工具只开放 `create`，不会因此获得修改或移动权限。管理工具的参数枚举和执行检查均遵循实际授权。已有会话继续沿用工具配置同步规则：空闲时生效，运行中等本轮结束再应用；管理动作范围变化也会触发同步。标准审批模式下，创建、建目录、编辑、改名和移动遵循 `write` / `edit`；删除和应用合并需要审批，完全访问模式沿用其自动执行规则；合法合并预览只读，无需写入审批。

根目录的 `@information`、`@quick-cards` 是只读来源，信息库子目录使用 `@information/<id>` 浏览。源条目可以读取、引用到私有卡片，不能通过整理工具修改、移动、删除；当前账号及 Agent 的私有边界由宿主绑定，不接受模型指定存储路径或切换身份。

整理写入必须携带最新 `expected_revision` 和 `request_id`。整批请求原子保存，版本过期、目标不存在、同名目录冲突、循环或非法预设目标都会整体拒绝；相同请求的重试只返回已执行回执，不重复移动、覆盖人工修改或复活已删除内容。完整快照更新保留文字、网页、文件、Skill、Office 选区与嵌套的顺序；已经发入对话或其他卡片的引用保留当时快照。

自动合并只接受同父目录中标题、正文、图标、显示方式及完整引用快照都相同的卡片，连引用 ID、来源路径、嵌套顺序不同也不会误合并；不会把相似文本交由模型猜测合成。预览计划绑定版本和当前会话，有效期十分钟。应用时重新验证全部条目，计划过期或并发修改后需要重新预览。界面区分预览、已应用、无变化及幂等重放，保存成功后通过对应 Agent 的真实变更事件刷新列表。

### Agent 创建普通指令 Skill

绑定到 Agent 的 Web 会话提供 `skill_search`、`skill_read`；保存能力 `skill_save` 由内置 `skill-creator` 的配置勾选联动启用或停用。搜索和读取保持通用受限能力。`skill_save` 的审批与 `write` / `edit` 一致，Skill 正文或同名外部 Skill 不能增加权限。

- `skill_search` 返回当前会话授权的 Skill 和当前用户在此 Agent 中创建的 Skill，包括草稿；支持分页，不访问其他用户或 Agent 的私有内容。
- `skill_read` 默认使用当前运行所捕获的版本。编辑前先搜索，再显式读取搜索返回的最新 `revision`。同名的运行时读取工具保留兼容，不额外注册第二个 `skill_read`。
- `skill_save` 接收幂等 `request_id` 和完整 `files` 快照；更新还必须提供 `skill_id` 与 `expected_revision`。仅支持 `SKILL.md` 和 `references/` 文本，不安装脚本、工具清单或依赖。
- 新建默认启用；更新省略 `activate` 保留原状态，显式 `false` 保存为未启用状态，`true` 启用。活跃运行保留旧版本及参考资料，安全空闲后更新。共享 Agent 定义、模型、MCP 和其他工具配置不变。
- 私有版本以完整快照保存。重复请求返回原回执；并发编辑冲突不会覆盖新内容。同名创建不能替换内置、已导入或其他已存在 Skill。

结果卡片可查看包内文件、在线编辑 `SKILL.md`、预览历史版本并导出标准 ZIP。技能面板的“本 Agent 创建”保留可重新打开的入口。结构校验与实际行为测试分开标记；首版不提供自动行为评测工具，也不会把“已保存”显示成“已验证可用”。

用户可以直接说：“把刚才的流程保存为快捷方式，把现有的数据库连接资料嵌套进去，保留网页、文件、Skill 和 Office 选区的顺序。”无需专用 Skill 或业务 MCP。Agent 先搜索并读取可用资料，再提交一次创建调用。信息库和快捷卡片始终作为只读引用来源。

创建参数是非递归的节点表：`root_id` 指向根节点，`nodes` 中每个节点的有序 `parts` 由 `text`、`ref` 或 `node` 组成。`ref_id` 只能来自当前会话宿主发出的句柄；模型不能自行指定用户、Agent、文件路径、Office 版本或来源快照。完整展开后限制为 5 层快捷方式（包含根）、512 个片段和 256 KiB 元数据与文本。子节点内嵌保存，只发布根卡一项；浏览器图标、选区来源和混排顺序都保留。这里会拒绝未知参数字段，避免伪造授权字段被悄悄忽略。

`request_id` 用于相同请求的持久去重。重试不会覆盖用户已修改的卡片，也不会重建用户已删除的卡片；后者会明确报错。文件在保存前核对捕获时的内容指纹，优先复用同 Agent 中完整且未修改的私有副本，源文件变化时要求重新取引用。新的根卡、保存回执与资料变更通知只在保存成功后生效；失败会回收本次新建的临时资产。回执只保留有界元数据，达到容量限制会明确报错，不会静默淘汰后重复创建。

成功结果在对话中显示可预览的卡片，并刷新对应 Agent 的快捷方式列表。列表刷新保留正在编辑的草稿。父卡预览里的“编辑此处”只修改选中的那一次子卡快照，支持逐层编辑、取消和撤销；应用到父卡后，仍需保存最外层父卡才会持久保存。


## 通用脚本哨兵

Web Agent 可选择 `watch`、`watch_test`，用自己编写的脚本进行持续观察或定时检查，有事件才唤醒保留上下文的工作会话；当前批次通过 host 绑定的 `watch_decide` 提交处理决定。来源和业务条件不按协议分类。详见 [通用脚本哨兵](sentinels.md)。
