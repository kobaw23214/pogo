# 会话与上下文管理

## 会话

每次运行都会写一个 JSONL 会话文件，默认在 `~/.pogo/sessions/`（用 `POGO_SESSION_DIR` 改）。

```bash
pogo -resume last -p "接着上次的改"          # 最近一次会话
pogo -resume ~/.pogo/sessions/xxx.jsonl    # 指定文件
```

记录是**树**而不是列表，每条带 `{id, parentId}`：

```
c45d1bf3 <- root      meta
e9893029 <- c45d1bf3  user
1b3a5e73 <- e9893029  assistant
```

一条线性对话就是没有分支的树。保留 parent 链的意义是：将来「回到某轮重试」是读另一个叶子，而不是改写文件。

**resume 会读回会话记录的元数据。** 不带 `-model` 时用这个会话原本用的模型，而不是默认模型。工作目录或 skills 与记录不一致时会在 stderr 上告警 —— 不阻止（换个目录继续是合理需求），但它们都会改变行为却不出现在任何消息里，所以不该悄悄发生。告警走 stderr，`-p` 因此仍然可以直接管道。

**文件损坏时会尽量恢复并说出来。** 进程被 kill 留下的残缺末行会被静默跳过（正常情况），中间行损坏则会打印一条诊断，并按文件顺序把断掉的 parent 链重新接上，把损坏之前的历史救回来。有分支的文件不做这种缝合，只报告。细节见 `docs/harness-design.md` §8。

### 会话索引：列表与搜索的缓存

JSONL 文件是唯一事实来源；会话目录下还有一个派生索引 `index.db`（SQLite，FTS5 trigram 分词），让侧边栏列表不必解析全部会话文件，并支撑 `GET /api/sessions/search?q=...` 的跨会话全文检索——中文按子串命中。索引是纯缓存：

- **自愈**。每次列表把目录和索引逐文件比对（mtime + size），不一致的重新摘要，文件已删的行自动清掉。进程被杀留下的过期索引在下一次读取时收敛。
- **可丢弃**。`pogo -index-rebuild` 丢弃重建；直接删掉 `index.db` 也安全，下次列表自动重建。
- **可关闭**。`POGO_SESSION_INDEX=off` 完全禁用（列表退回全量扫描）；设为路径则改变索引位置。
- **不挡写入**。索引更新在会话记录安全落盘之后、尽力而为；索引失败绝不影响会话写入。

### 上下文构成：占用条回答不了的那半

顶栏和终端 dock 的占用条告诉你「62% 满了」，`/usage` 告诉你「花了多少钱」。**两个都不告诉你这些 token 是什么**，而这恰好是唯一有行动价值的问题——历史被工具输出撑大可以机械驱逐并按需重读，被对话文本撑大只能摘要压缩。这两条路的代价完全不同，不量就是在猜。

所以每次 run 结束落盘时会多记一条构成快照，`-analyze-session` 读得出来：

```
Context Composition (last recorded state, estimated):
  Fixed (system prompt + tool schemas): 1999 (7%)
  Tool results:                         27000 (90%)
    read                               24300 (81%)
    bash                               2700 (9%)
  Tool call arguments:                  82 (0%)
  Assistant text:                       900 (3%)
  User text:                            15 (0%)
  Estimated total:                      29996 over 13 message(s)
  Provider's own count:                 29100  (estimate reads 0.97x, i.e. high)
```

三条读法上的约定，都是踩过的坑的形状：

**这是快照，不是增量。** Stats 里其他每个字段都是「自上次记录以来的增量」，因为分析器要把它们相加；这一条不是——它描述的是记录写下那一刻的**整个历史**，所以最新那条就是答案，跨记录相加毫无意义。分析器因此是覆盖而不是累加。这是本项目第三个同形状的算术陷阱（前两个是 `CacheRead` 是 `Input` 的子集、`Delegated` 是 `Usage` 的子集），所以写明。

**构成是估算，总量是实测。** 按 4 字节 = 1 token 数字节，与 `agent.New` 估算固定开销用的同一个除数——两个数落在同一条记录里要互相比较，除数不一致就不只是不准而是没有意义。信份额，别信绝对值。

**最后那个比值是重点，而它的方向和这里原来写的相反。** `Provider's own count` 是服务端实测，两者的比值就是「这个除数对本次会话的文本错了多少」。**后面任何人拿 token 定的阈值，精度上限就是这个数**——所以它被记下来而不是被假设。

原来这里断言的是「4 字节/token 对英文大致对，对中文差了大约 2.5 倍（估算偏低）」。把这个比值从 25 份真实的多轮会话里反推出来之后，**两个 provider 上都是反的**：

```
以 ASCII 为主的 prompt   比值中位数 0.98   (n=9)
中文占比高的 prompt      比值中位数 0.83   (n=11，最低 0.60，非 ASCII 占 92%)
总体                     中位数 0.97      区间 0.51 – 1.11
```

**估算偏高，而且对中文偏得最多。** 原因是两家的分词器都在中文上训练过：一个两字词是 6 字节，常常只算 1 个 token，所以对那部分文本真实的除数接近 6 而不是 4。也就是说 **4 字节/token 在这两家上是保守的那一侧，不是危险的那一侧**——在有人据此把阈值往紧的方向调之前，值得先知道这一点。

顺带一个结论,而它后来把默认值改了:**原来的触发线偏早。** `auto` 曾经是窗口的一半,而那是**估算** token——按上面的比值换算过去只占窗口的 **42%–49% 实测**,也就是清理开始时窗口有一半以上真的是空的。现在是**窗口的五分之四**,理由见「上下文清理」那一节。

**「清掉了多少」现在也记下来了,而且是这个比值成立的前提。** `Estimated` 描述的是整个历史,`Measured` 数的是**清理之后真正发出去的那个 prompt**——两个数覆盖的文本不一样,直接相除就不是分词器比值。清理触发过的会话原来会把估算报成高得离谱,而读的人会去怪那个除数。现在报告里多一行 `Cleared from the last prompt`,比值按 `Estimated − Cleared` 算。

一个踩过的方法论坑也记在这里:**委派过的会话,它的 usage 计数器里含着子的 token**,拿它去比父自己历史的估算,会把除数报成差了两个数量级(实测见过 6,848,391 比 36,230,其中 6,812,682 是子花的)。落地实现里 `Measured` 取的是 `LastInput()`——父自己某一次响应的数——所以没有这个问题;但任何把 usage 记录求和的探针都有。

thinking 块不计入：它不回传给 provider（`llm/convert.go` 里那个 case 是空的），计进去会让估算永久高于实测，把上面那个比值变成废数。`Details` 同理——它永远到不了 provider。

### 轮次分布：`-max-turns` 的数从哪来

同一个命令，传目录（或字面量 `sessions`，解析到你自己的会话目录）回答的是另一个问题：**一次 run 实际要几轮**。这是选 `-max-turns` 该依据的数——固定上限设在自己分布的第 75 百分位是论文量出来的甜点（arXiv 2510.16786），而 pogo 有自己的历史，所以这个数自己量，不抄别人的。

```
$ pogo -analyze-session sessions
Run Length Distribution: ~/.pogo/sessions
================================================================================

Population:
  Sessions read: 102
  Runs:          116
    finished (reached an answer):   110
    unfinished (cut off):           6
    no tool call, excluded:         22
  Population:    88 (finished, called at least one tool)

Turns per run (n=88):
  p50: 3
  p75: 5
  p90: 9
  p95: 13
  max: 25
  mean: 4.3
```

四条读法约定，每条都是一个「不这么做就量错」的坑：

**单位是 run，不是会话。** 一份 transcript 里有好几个 run，还可能有撤回放弃的分支——按会话数轮次会朝虚高的方向错两次。分析器沿 parent 链只走活分支，再按 prompt 重新切分：被撤回放弃的分支是人掐断的，不是活需要的轮次。

**被掐断的 run 是下界，不是观测。** 撞上限、Ctrl-C、断连停在第 N 轮的 run，活需要的轮次 ≥ N，混进百分位会把每个数压低、尾部最狠——而上限恰恰从尾部选。所以它们排除出百分位、单独按值列出：停在同一个数上的一簇是「这里曾经有过一个上限」的唯一证据（transcript 不记 `-max-turns`），散开的一小撮报告会明说构不成证据。删失占比过高时报告直接警告：p75 是地板，不是答案。

**没调过工具的 run 被排除。** 它第一轮就结束，不携带任何关于上限的信息，而数量足以拖低整个分布——实测混进来的效果是本机 p75 从 5 变 4、p90 从 9 变 6，上限会按「这段历史里有多少纯问答」来选。

**报告最后那句提醒不是客套。** 这个分布能用来选上限的前提，是这本历史长得像将来被上限约束的活——交互式问答的历史里不会出现无人值守任务，而后者才是上限真正约束的场景。

**这个数的用法是软上限**（`-soft-turns`，默认 10）。撞软上限不结束：往对话里插一条检查点消息，告诉模型「用了几轮、软上限、硬上限」，然后二选一——收尾，或者继续并用一句话说明还差什么。**没有解析器**：模型的下一个动作就是决定本身——继续调工具就是延期，纯文本回答就是完工，走的还是原来那条结束路径。机制只保证两件事：决定是知情做出的，「还差什么」落在 transcript 里可审计。延期不需要自己的计数器，`-max-turns` 就是它的上限。检查点走 steering 的既有事件路径，JSON 模式、web、transcript 自动可见；`[pogo]` 前缀标记它是 harness 写的。web 会话固定关闭（和预算同一个理由：浏览器里有人盯着）。

### 链式运行：reset 而不是压缩

软上限之上还有一层：`-max-runs <n>`（仅 `-p`，默认 1）。当一次 run 因为**额度用尽**而结束——轮次上限、token/花费/时间预算，也就是 `end_reason` 里 disposition 为 `continue` 的那几个——会话 fork 出一个新分支，一次全新的 run 拿着空上下文接着做。这是 reset，不是压缩：一个字都不摘要，旧 transcript 作为被放弃的分支留在同一个文件里，两端都能追溯。

接力的载体是 **`handoff.md`**：链式开启时 system prompt 会多一节，告诉模型它的上下文不会带到下一棒，要求在 `.pogo/handoff.md` 里维护「任务一句话 / 已完成及验证方式 / 还剩什么」。新 run 的开场 prompt 带着原始需求原文（`<original_request>`，和 `/compact` 同一颗钉子）+ 交接协议：先读 handoff，有验证方法就先验证再开工，文件不存在就从工作区自行重建状态。**文件形状是 Markdown 而不是 JSON**——机器校验的契约是第 8 步（evaluator）的事，在没有检查者之前规定字段是仪式。

两个判断有测试钉着（`chain_test.go`）：哪些 `end_reason` 接力（`turn_limit`/`token_budget`/`cost_budget`/`time_budget`/`max_tokens`），哪些不接（`stagnation` 和 `context_overflow` 重跑只会原样失败，`transport_error`/`aborted` 与任务无关——这张表就是 §12.1 的 dispositions，链接只是读它）。web 会话和 REPL 不用这个 flag：有人在场时「继续」两个字比机制便宜。

实测形状（`-max-turns 4 -max-runs 3`，一个要 6+ 轮的写文件任务）：三轮接力完成，handoff.md 按协议写出了 Task/Done/Left 三段。

**判定者：`-evaluate`（默认关）。** 没有它，驱动只能信 run 自己的说法：「完工」是模型说的，「没完工」只是上限打断了它——而最后一棒把轮次花在干活上、收尾回答挤不进预算的情况真实存在（活干完了 exit code 却是 1）。打开后，一个**全新的只读 agent**（read/ls/find/grep，没有 bash/write/edit——就是 explore 子代理的形状）在两个判定点发言：某棒声称完工时（不通过就带着发现接力下一棒，run 预算内）；以及 run 数用尽而结束原因是额度类时（核验通过照样 exit 0）。判定协议一行 `PASS` / `NEEDS_WORK` + 具体发现；**evaluator 自己失败时不改变任何结果**——没有它时的行为就是兜底。

为什么不给 todo 的 `completed` 加证据门槛（Default-FAIL 契约的极简版）：「标完成必须有过一次成功的 bash 调用」会误伤合法的纯读写任务（改个错别字不需要 bash），而绕开它只要一句 `echo`——一个既误伤又挡不住的门槛是负资产。提前宣布完工这个失败模式，在接力判定点上由 evaluator 覆盖，比在每笔待办上盖戳有效。

## 上下文清理（context editing）

**默认开着。** prompt 超过模型窗口的**五分之四**时，最旧的工具输出会从**发给模型的那份**里被换成一行占位符：

```
· read {"path":"internal/store/store.go"}
  1840 lines (+0 -0)
…
… context edit: dropped 6 old tool result(s), ~31,200 tokens (prompt was ~104,900)
```

模型看到的是：

```
[1840 lines / 58KB of read output removed to fit the context window. Call the tool again if you still need it.]
```

它解决的是一个确定会发生的死法：历史只增不减，每轮全量重发，读几十个大文件之后 prompt 越过窗口，provider 回 400——而 400 不在 `llm/retry.go` 的重试白名单里，run 当场结束。更糟的是历史不会因此变小，**同一个会话的下一句话第一次调用就再次失败，永久卡死，只能开新会话**。

**而阈值是个猜测，所以还有一道兜底：provider 拒绝之后强制清一次再重试。** 阈值比对的那个数一半是实测一半是估算，所以它可以在没人注意的情况下被越过；而 provider 的拒绝不是猜测，它有资格直接触发一次清理。这一道是把「这一轮失败了」和「这个会话死了」分开的东西。

强制的那一次会推翻三处常规策略，理由都一样——它们存在的意义是避免为「可能并不需要的清理」付钱，而拒绝已经把这个问题回答了：

| 推翻的 | 为什么 |
|---|---|
| 触发线 | 它比对的数一半是估算的；provider 的计数不是 |
| `clear_at_least` | 它存在是为了不为一点点节省买一次缓存未命中。这里的对照项不是缓存未命中，是一个死掉的会话 |
| 保留窗口降到 1 | **不是 0**：清掉模型正要据此推理的那条结果，等于让重试去回答一个证据刚被拿走的问题 |

**`ExcludeTools` 不推翻。** 那是调用方明确说过「这个永远不要清」，而一个「一旦不方便就丢掉明确指令」的机制比一个「如实报告自己帮不上」的机制更糟。如果恰好是这条排除让 prompt 装不下，那就把 provider 的错误原样返回。

**这一道有界，而且界来自已有的性质**：被清过的集合只增不减，所以同一个 run 里第二次溢出时强制清理找不到新东西可清，`ok` 为 false，错误直接返回——不需要额外的计数器就不会打转。

**它没有事件通知，这是有意的。** 为同一轮再发一个 `turn_start` 是错的——`web/hub.go` 正是在那里铸造这一轮的消息 id，契约写着「一轮产出一条 assistant 消息」——而正确的落点是一个新的事件类型，那是改线上契约而不是改这个文件。最终也不算静默：`editContext` 每一趟报告的是**整个**冻结集合而不只是新增的部分，所以下一轮的 `turn_start` 在两个前端里都会带上变大后的数字。

**分类靠的是 `llm.APIError` 而不是匹配字符串。** 之前非 2xx 被拍平成一个字符串，于是「prompt 太长」和「key 不对」——同一个 provider 上都是 400、需要相反处理的两件事——只能靠匹配散文来区分。现在状态码、provider 自己的 `type` 和 message 都留着。

不过判定**仍然要看 message**，因为两家给的 `type` 分不开：Kimi 上溢出是 `invalid_request_error`，而缺参数、body 格式错也是同一个 type。所以短语表是拿真端点验出来的，不是从文档抄的：

```
400 invalid_request_error
"Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"
```

短语只收「只可能是这个意思」的那些。误判的代价是不对称的：把一个格式错误的请求当成溢出，会让 pogo 用丢弃上下文去绕开一个请求里的 bug，那比如实报错更糟。同理判定**限定在 4xx**——一个 body 里恰好提到 token 上限的 500 是服务端故障，拿丢历史去绕开一次故障是错的。

`-context-edit off` 关掉；给个数字就是绝对阈值。

### 形状抄的是 Anthropic 的 `clear_tool_uses_20250919`

选它是因为这是唯一有公开契约和默认值的设计，不是某篇博客的推测。照搬的部分：

| | |
|---|---|
| 触发 | prompt 超过阈值才开始清 |
| 保留 | 最近 3 条工具结果原样不动（Anthropic 的默认值） |
| **只清结果，不清调用** | 读类工具的调用连参数一起留着（他们的 `clear_tool_inputs` 默认 `false`）——「调用发生过、针对什么」是模型判断要不要重来的依据。**`write` / `edit` 是例外，见下** |
| 占位符 | 每条被清的结果换成说明文本，让模型知道东西被拿走了，不是本来就没有 |
| 顺序 | 最旧的先清 |
| `clear_at_least` | 清得太少就干脆不清，见下 |
| 不碰 transcript | 他们在服务端做，文档明说客户端保留完整未修改的历史 |

**为什么是机械清理而不是 LLM 摘要**：目前唯一有对照实验的证据（Lindenbauer 等，*The Complexity Trap*，DL4C@NeurIPS'25，SWE-bench Verified 上跨五种模型配置）发现**简单丢弃旧观测把成本砍半，同时在解题率上追平、偶尔略超 LLM 摘要**。而它在这里还便宜得多：不多花一次模型调用、没有可能写错却无从校验的摘要、行为可以被单测逐字钉死。

### 四处按 pogo 的情况改了

**`write` / `edit` 的参数也清，读类工具的不清。** 这一条是量出来的，不是想出来的。

把这个项目自己积累的 100 个会话按 `session.Compose` 的同一套账算了一遍，结论是：真正长到有问题的会话（7 个 ≥50K）**无一例外是 88%–99% 的工具输出**，没有一个是对话文本撑大的。但里面还有两个既不是工具输出、也不是对话——它们 **83% 和 96% 是工具调用的参数**，其中一个是 42,328 估算 token 的 `write` content。**而清理对它们能清 0%**，因为它只看结果。

Anthropic 那个 `clear_tool_inputs: false` 的理由是对的——「调用发生过、针对什么」是判断要不要重来的依据。但那个理由覆盖的是 `read` / `ls` / `find` / `grep` / `bash`：它们的参数**就是**对调用的描述（一个路径、一个 glob、一条命令行）。它不覆盖 `write` 和 `edit`：那里的大参数不是对调用的描述，**它就是载荷本身，而且是全世界最确定还存在别处的那份载荷——文件就在磁盘上**。回头读一遍文件比在上下文里背一份副本既便宜又更真实：那份副本从别人下一次编辑那个文件起就开始撒谎。

所以清的是载荷字段，`path` 留着（那才是 Anthropic 真正在保护的东西），结构一字不动——`edits` 仍然是一个数组，每个元素仍然是带两个字符串字段的对象，长度也还在，所以「改了几处」照样答得出来。参数占位符只写自己的大小（`[41KB cleared]`），解释放在配对的那条结果占位符上说一次。

`edit` 的 `oldText` 一起清，这是有意的：一次**成功**的 edit 之后那段文本按定义已经不在文件里了，永远不可能再匹配上——留着它等于专门保住这次调用里唯一已经失效的那个字符串。而**失败**的 edit 反过来，它的参数一律不动：`clearable` 本来就跳过错误结果，而 `oldText` 恰好是模型自我纠正时唯一需要的东西。

**占位符的最后一句按工具分，因为它是模型会照着做的建议。** Anthropic 的策略对每条被清的结果说同一句话——`Call the tool again if you still need it.`。当唯一可清的东西是「读」的时候这句是对的，而 pogo 有三类工具不是：

| 重新拿回来要付什么 | 工具 | 那句话 |
|---|---|---|
| 一次调用，从当前状态重新得到 | `read` `ls` `find` `grep` | 再调一次就行（原话） |
| 效果就是那个文件 | `write` `edit` | 改动在磁盘上，去读那个文件 |
| 会把命令做的事再做一遍 | `bash` | 只在重复是安全的时候再跑 |
| 要再付一个完整的子 run | `subagent` | 拿回来意味着再委派一次 |
| 没分类的工具 | — | 只在重复调用是安全的时候再调 |

`write` / `edit` 那条是错得最实在的:重跑一次 `write` 会用模型已经不持有的内容覆盖文件,重跑一次 `edit` 直接失败,因为 `oldText` 已经不在文件里了。

`bash` 那条是把这个项目自己的 100 份 transcript 翻了一遍才确认的。75 次 bash 调用里真实出现过 `git add -A && git commit -m ...`、`git cherry-pick <sha> && go test ./store/`、以及一个 `cat > ~/.pogo/providers.json << 'EOF'` 的 heredoc——**重跑这些不是「重读」,是第二次副作用。** 所以那句话只陈述事实(再跑会再做一遍),把判断交给模型:命令原文就在紧上方的参数里没被动过,它比这段文本更有资格判断这条命令能不能重复。这和 subagent 围栏那两段的规则是同一条——陈述事实,不给指令。

分类缺省值是**谨慎的那一句而不是鼓励的那一句**,而且查表是显式的:`reRunFree` 恰好是零值,靠零值兜底会让「没登记」静默地等于「可以随便重跑」,而那是唯一一个能把「清理」变成「动作」的缺省。有一条测试要求默认注册表里每个工具都必须在表里,所以以后加工具会被迫做这个决定。

在真实 transcript 上量过：13 个 ≥20K 的会话里，12 个**一个 token 都没多清**（它们是读主导的，原有机制已经够），而那个参数主导的会话从「26,217 的历史里只能清 922」变成「清掉 12,212，其中 11,290 来自参数」——**同一个会话上多清了 43 个百分点**。

### 三处原本就按 pogo 的情况改了

**`auto` 是窗口的五分之四，不是写死的 100,000，也不是他们的一半。** Anthropic 的默认值固定在 100,000，因为他们的模型窗口都在 200K 附近——那个数恰好是一半。pogo 的目录来自你的文件，窗口可以差出好几倍，照抄绝对值会让 1M 的模型在窗口的十分之一处就开始清，那不是同一个策略。窗口值取决于你文件里每个模型的 `context_window`；目录里没有的模型（window 为 0）`auto` 直接关闭而不是猜一个阈值（`agent/contextedit.go`）。切模型时阈值跟着走（`/model` 和浏览器的切换器都会重算）。

比例也不是他们的一半，**而这一改有三条理由，前两条是量出来的**：

- **被比较的那个数本身偏高。** 估算对实测的比值是 0.98（ASCII）/ 0.83（中文），所以「估算的一半」实际只到窗口的 42%–49%。按一半触发，等于在窗口有一半以上真空着的时候就开始丢东西。
- **清理不免费。** 它改写 prompt 的一部分，前缀缓存因此失效：Kimi 上未命中约按命中的十倍计费，智谱约两倍。为了腾出还没人要的空间付这个钱是纯亏，模型还可能去重读它本来看得见的文件。
- **选一半时成立的前提没了。** 那时候上下文用尽是永久性的：provider 返回 400，而重试判定不放 4xx 进去，历史也不会因此变小——同一个会话之后每一句话都会失败。偏早是买一份对着悬崖的保险。**现在那个悬崖没了**：拒绝会触发一次强制清理并重试一次（见上面「provider 拒了之后」），偏晚的代价从「一个会话」变成「一次白跑的调用」。

**剩下那五分之一是留给三件事的**，这也是为什么不是十分之九：Kimi 上模型自己的输出记在同一个限额里（`prompt tokens + max_tokens exceeds the model specification`），所以 `MaxTokens` 必须装得进这个余量；被比较的数一半是估算的，不能信到个位数；而清理发生在调用**之前**，它放行的那一轮还会继续往历史里加工具输出。余量够不够，取决于你文件里每个模型的 `context_window` 与 `max_tokens`——触发线之上的那五分之一必须装得下 MaxTokens。有一条测试盯着这条假设（内置目录已空，测试现在自己装一份目录来断言余量），因为「一个输出上限很大、窗口很小的新模型」正是会打破它的东西。

**失败的结果不清。** Anthropic 不做这个区分，pogo 做：从错误文本里自我纠正是这个项目的签名行为，整个时间线 UI 都建立在「把修复连到它修的那次失败」上。错误文本本来也很短，留着几乎不花钱。

**`clear_at_least` 有默认值（8000 token），他们默认不设。** 他们的文档说清了它是干什么的：清理会让缓存前缀失效，清一点点就是白买一次 cache miss。这里的代价更重——两家 provider 都是隐式缓存、**没有任何 API 能编辑已缓存的前缀**，而 Kimi 的未命中大约按命中价的十倍计费（Zhipu 自己的文档说折扣约五折，所以两家差了五倍）。这个 8000 是拍的，是最该用真实会话数据校准的一个参数。

### 两条不显然但要紧的性质

**清过的就一直是清的。** 已被清理的结果会在后续每一轮继续保持清理状态，哪怕 prompt 已经掉回阈值以下。否则会发生：这轮清了 → 下轮低于阈值于是还原原文 → 再下轮又超了再清——**prompt 前缀来回翻，每个循环白付一次缓存未命中**。Anthropic 那边是白拿的（文档把已清理结果的命运描述为「一旦见过就冻结」，且替换文本每次逐字节相同）；pogo 得自己拿着这个集合。占位符文本因此是原输出形状的纯函数，不含时间也不含清理过了几轮。

**触发判定是「实测基线 + 估算增量」。** 两半都必需，缺一半就是这个机制要防的那个死法：服务端实测的数永远滞后一轮，而滞后的那一轮恰好是危险的一轮——一批工具结果能在两次测量之间加进十万 token，只读上一次测量的阈值会正好在填满窗口的那一轮冲过去。反过来，整个历史都靠估算又会丢掉唯一不是猜的数字：实测值里已经含了 system prompt、工具 schema 和 wire 封装，而 `llm.EstimateTokens` 一个都不建模。所以是实测打底、估算增量——和 Codex 的 `body_after_prefix` 是同一个切法，同一个理由。

### 任务清单在这里被钉住

`todo` 的结果不是事件而是状态，所以规则和别的工具相反：**被取代的旧清单无条件清掉**（不看阈值，它就是一份过期状态的副本），**最新那份永不清掉**，即使它是整个历史里最旧的东西、即使保留窗口本该轮到它。它是「这次要做什么」唯一能活到下一个上下文窗口的记录。它也不占保留窗口的名额——那三个位置是留给工作集的。

### 它没有解决什么

**这不是压缩（compaction）。** 如果历史是被对话文本而不是工具输出撑大的，就没有可清理的对象。真正的分工线在这里：清理处理「工具输出大」，摘要压缩处理「对话长」。哪一种是你的实际负载，`-analyze-session` 的 Context Composition 段会告诉你——那正是它存在的理由。压缩见下一节，**它默认只在你主动要求时发生。**

**模型可能不去重读。** 占位符只能把恢复所需的信息给它，保证不了它去用；它也可能凭记忆继续引用已经不在的内容。这一条和摘要「可能写错」是同级别的不可测项，只是形状不同。

## 压缩（`/compact`）

用一份摘要替换整个对话。**默认只在你敲 `/compact` 时发生；`-compact auto` 显式开启后才武装自动路径**——运行之间阈值触发（窗口减去 max(16384, 窗口/20)）、上下文溢出时压缩并原文重放该条 prompt（每条至多一次）、连续 3 次失败熔断；完整决议与阶段划分见 `docs/auto-compact.md`。两种形态：手动 `/compact` 把整个对话折成一条 continuation；自动路径**保留约 20000 token 的最近轮次原文**（尾部从 assistant 消息处切开，tool_use 永远带着自己的 tool_result），重复压缩经 `<previous-summary>` 更新而非堆叠。

```
❯ /compact
compacted: 47 messages → 1, about 82400 → 1210 tokens (freed ~81190); the summary cost 79300 in / 640 out.
the full conversation is still in ~/.pogo/sessions/20260810T072148Z-f5e350b2.jsonl as an abandoned branch.
```

### 为什么它默认不自动

清理是可逆的——它丢掉的输出可以重新取回来，而且占位符会告诉模型怎么取。摘要不是：它是一次有损重写，由一个可能写错的模型产出，而且没有任何测试抓得住那种错。

所以默认的触发条件是「有人开口要」。这不是把限制说成原则，它就是让这份损失可以接受的那个具体条件——**做这个决定的人正是知道自己还需要什么的那个人。**

两组证据支撑「不自动」：

- 把这个项目自己 100 个会话量了一遍：7 个达到 5 万估算 token 的会话**无一例外是 88%–99% 的工具输出**，而机械清理能释放其中 90%–97%。摘要要解决的是「对话文本撑大」，那个形状在真实数据里**一次都没出现过**。
- 唯一一份对「压缩边界前后策略遵从性」的对照研究（arXiv [2606.22528](https://arxiv.org/abs/2606.22528)，7 个模型、1,323 个 episode）显示：在边界之前声明、又被摘要丢掉的约束，会把违规率从 0% 推到 30%，而**中文场景再差 42 个百分点**——而这个项目的部署在用的两个 provider 都在那个 panel 里。

用一个没发生的问题去换一个量到的风险，不该自动做。你主动要求时，那是你的取舍。

*（论文内容为转述摘要，已按许可要求改写。）*

### 它保证什么

**开头那条请求逐字保留。** 这是整个设计里唯一直接来自测量的一条：上面那份研究横比了各种压缩策略，**只有「保住最早那一轮」把违规率维持在 0%**，因为约束就写在那一轮里。它在这里几乎免费——实测大会话里 user 文本只占 0.4%–0.9%。

**摘要器不带任何工具，历史也被扁平化成一条 user 消息。** 两件事是同一个决定。Anthropic 记录过：模型在有工具可用时被要求写摘要，有时会去调工具而不是回答，结果是一个 `content: null` 的压缩块，他们的对策是在提示词里写「不要调任何工具」。pogo 在客户端做压缩，所以有他们没有的选项——**不提供工具，也不发任何 `tool_use` 块，于是调工具不是被劝阻，而是不存在。** 扁平化是让这件事成立的前提：带着 `tool_use` 块却不带工具声明的请求，有些 OpenAI 兼容端点会直接拒收。

**摘要器永远是 agent 自己的模型，绝不换便宜的。** 诱惑是真的，机制也现成（`subagent_model` 就在手边）。但那份研究里**违规跟随写摘要的那个模型，不跟随执行的那个**：一个 agent 读着弱模型写的摘要，违规率 53%，而读自己写的摘要是 0%。摘要不是一个可以比价的子任务，它是那些指令**唯一剩下的记录**。

**注入边界是显式的。** 工具输出会进摘要器，而这里的工具输出就是工作区里文件的内容——一个文件可以写着一句对着「读到它的人」说的话。所以待摘要的内容整个装在 `<transcript>` 元素里，system prompt 明说那是数据。pogo 的起点还比直接摘要原始历史的实现好一些：清理已经把最旧的工具结果换成占位符了。

**失败一律不动对话。** 摘要为空、比原对话还大、期间有 run 在跑、对话在摘要过程中变了——四种情况都是「保留原样并说明原因」，最多损失一次模型调用。

### 完整记录不会丢

会话文件是追加式的树，`/compact` 走的是和「撤回」同一条路：`Fork("")` **放弃**当前分支并开一条新链，原来的记录全都留在文件里、只是不再可达。所以 `-resume` 回放的是压缩后的历史，而完整原文随时可以读。**transcript 从不被编辑。**

**但被放弃的分支的花费不会跟着消失。** 累计用量数的是文件里**每一条** stats 记录，不是当前分支上的那些——provider 已经为那些 token 收过费了，哪条分支是活的跟这件事无关。这一点对撤回同样成立，而且是必须的：`-token-budget` 和 `-cost-budget` 是天花板，一个每次重组对话就忘掉已花费的天花板不算天花板。这也让它和 `-analyze-session` 对上了口径（那边一直是线性读全部记录）。

### 什么时候它会拒绝

| 情况 | 原因 |
|---|---|
| 对话只有一条消息或没有 | 没东西可压，而且不会为此白花一次调用 |
| 摘要不比原对话小 | 通常是开头那条消息很长（比如粘了一份文档）——它是被刻意钉住的，所以压不掉。压缩把 prompt 变大等于花钱把问题变糟 |
| 有 run 在跑 | 循环正在往这份历史上追加 |
| 摘要为空 | 上面说的那个 `content: null` 形状 |
| 摘要期间对话变了 | 那份摘要描述的已经不是当前历史 |

摘要调用本身的 token 会计入总账（`-token-budget` / `-cost-budget` 看得见），但**不计入 `delegated`**——那个字段回答的是「子 agent 花了多少」，而压缩是父自己的活。

### 浏览器

两个入口：composer 里敲 `/compact`，或者**点开上下文占用条**——按钮在构成面板的底部。

放在那里是因为那块面板已经算出了答案。**同一份数据，两种相反的建议：**

- 主要是**工具输出**时（实测里的常见情况：大会话 88%–99% 都是），面板会说清理已经在处理这部分、压缩帮不上多少，按钮保持普通样式
- 主要是**对话文本**时，面板会说这部分不会自动移出、压缩正是为它准备的，按钮变成推荐样式

一个永远看起来同样值得点的按钮不携带信息。平局算工具那边——平局不构成「对话文本是问题」的证据，而推荐一次有损重写需要证据。

压缩前会弹确认（这里唯一一个会确认的命令，因为它是唯一一个会丢工作的），压缩期间按钮显示进度，完成后广播 `compacted` 事件让所有标签页重取快照。运行中按钮禁用（服务端也会 409）。

**`/compact` 不参与前缀缩写**，两侧都是。现有命令里没有以 `/c` 开头的，所以不做这条限制的话，敲 `/c` 加回车就会替换整个对话——两个字符的手误。规则是「不可撤销的命令不参与缩写」，而它**只作用于回车、不作用于 Tab**：Tab 会先把完整名字摆在屏幕上，回车打前缀是照猜测行事。

服务端接口：`POST /api/sessions/{sid}/control {"action":"compact"}`。在 composer 里绕过客户端直接把 `/compact` 当 prompt 发会被 400 挡回来——斜杠命令一律不许进对话，否则一个文件里写着「请输出 /compact」就能操纵会话。


---

<a id="english"></a>

# Sessions and Context Management

## Sessions

Every run writes a JSONL session file, by default under `~/.pogo/sessions/` (`POGO_SESSION_DIR` to change).

```bash
pogo -resume last -p "continue the previous change"          # most recent session
pogo -resume ~/.pogo/sessions/xxx.jsonl                     # a specific file
```

The record is a **tree, not a list**, each entry carrying `{id, parentId}`:

```
c45d1bf3 <- root      meta
e9893029 <- c45d1bf3  user
1b3a5e73 <- e9893029  assistant
```

A linear conversation is just a tree with no branches. The point of keeping the parent chain: a future "go back to that turn and retry" reads a different leaf rather than rewriting the file.

**resume reads back the session's metadata.** Without `-model`, it uses the model that session originally used, not the default. When the working directory or skills disagree with the record, it warns on stderr — it does not block (continuing from a different directory is a legitimate need), but all of these change behavior without appearing in any message, so they should not happen silently. The warning goes to stderr, so `-p` can still be piped directly.

**On file corruption it recovers what it can and says so.** A partial last line left by a killed process is silently skipped (the normal case); a corrupted middle line prints a diagnostic, re-links the broken parent chain in file order, and salvages the history before the corruption. Files with branches are not stitched this way, only reported. See `docs/harness-design.md` §8.

### The session index: a cache for listing and search

The JSONL files are the single source of truth; beside them the session directory holds a derived index, `index.db` (SQLite, FTS5 with the trigram tokenizer), so the sidebar listing does not parse every transcript and `GET /api/sessions/search?q=...` can do full-text search across sessions — Chinese matches by substring. The index is a cache in the strict sense:

- **Self-healing.** Every listing compares the directory against the index file by file (mtime + size), re-summarises whatever differs, and drops rows whose files are gone. An index left stale by a crash converges on the next read.
- **Disposable.** `pogo -index-rebuild` discards and rebuilds it; deleting `index.db` outright is equally safe — the next listing rebuilds it.
- **Switchable.** `POGO_SESSION_INDEX=off` disables it entirely (listings fall back to a full scan); any other value is a path for the index file.
- **Never in the write path's way.** Index updates run best-effort after the session record is safely on disk; an index failure never fails a transcript write.

### Context composition: the half the usage bar can't answer

The usage bar in the top bar and the terminal dock tells you "62% full," `/usage` tells you "how much was spent." **Neither tells you what those tokens are**, and that is exactly the only question with action value — history bloated by tool output can be mechanically evicted and reread on demand, history bloated by conversation text can only be summarized and compressed. The costs of these two paths are completely different; not measuring is guessing.

So at the end of each run, a composition snapshot is recorded on disk, and `-analyze-session` reads it out:

```
Context Composition (last recorded state, estimated):
  Fixed (system prompt + tool schemas): 1999 (7%)
  Tool results:                         27000 (90%)
    read                               24300 (81%)
    bash                               2700 (9%)
  Tool call arguments:                  82 (0%)
  Assistant text:                       900 (3%)
  User text:                            15 (0%)
  Estimated total:                      29996 over 13 message(s)
  Provider's own count:                 29100  (estimate reads 0.97x, i.e. high)
```

Three reading conventions, each shaped by a pitfall that was hit:

**This is a snapshot, not a delta.** Every other field in Stats is "incremental since the last record," because the analyzer sums them; this one is not — it describes the **entire history** at the moment the record was written, so the latest one is the answer, and summing across records is meaningless. The analyzer therefore overwrites rather than accumulates. This is the project's third arithmetic trap of the same shape (the first two: `CacheRead` is a subset of `Input`, `Delegated` is a subset of `Usage`), hence spelled out.

**Composition is estimated, total is measured.** Bytes counted at 4 bytes = 1 token, the same divisor `agent.New` uses to estimate fixed overhead — two numbers on the same record must be comparable to each other, and an inconsistent divisor is not just inaccurate but meaningless. Trust the shares, not the absolute values.

**That last ratio is the point, and its direction is opposite to what was written here originally.** `Provider's own count` is the server-side measurement, and the ratio of the two is "how wrong this divisor is for this session's text." **The ceiling on the precision of any token-based threshold anyone sets later is this number** — which is why it is recorded rather than assumed.

What used to be asserted here was "4 bytes/token is roughly right for English, off by about 2.5× for Chinese (the estimate is low)." After back-of-envelope from 25 real multi-turn sessions, **on both providers it is the other way**:

```
ASCII-heavy prompts       median ratio 0.98   (n=9)
High-Chinese-share prompts median ratio 0.83   (n=11, lowest 0.60, non-ASCII share 92%)
Overall                   median 0.97         range 0.51 – 1.11
```

**The estimate runs high, and it runs highest for Chinese.** The reason is that both vendors' tokenizers are trained on Chinese: a two-character word is 6 bytes but often counts as a single token, so the real divisor for that text is close to 6, not 4. In other words, **4 bytes/token is on the conservative side on these two providers, not the dangerous side** — worth knowing before anyone tightens a threshold based on it.

A corollary that later moved the default: **the old trigger line was too early.** `auto` used to be half the window, and that was in **estimated** tokens — converted via the ratios above, that is only **42%–49% of the window measured**, i.e., more than half the window was genuinely empty when cleaning started. It is now **four-fifths of the window**; see "Context cleaning."

**"How much was cleared" is now also recorded, and it is a precondition for that ratio to hold.** `Estimated` describes the whole history; `Measured` counts **the prompt actually sent after cleaning** — the two numbers cover different text, and dividing them directly is not the tokenizer ratio. Sessions where cleaning triggered used to report the estimate as absurdly high, and the reader would blame the divisor. The report now carries an extra `Cleared from the last prompt` line, and the ratio is computed as `Estimated − Cleared`.

A methodological pitfall, also recorded here: **a session that has delegated carries the sub's tokens in its usage counter**, and comparing that against the estimate of the parent's own history would report the divisor as off by two orders of magnitude (one measured case: 6,848,391 vs 36,230, of which 6,812,682 was the sub's). In the implementation, `Measured` takes `LastInput()` — a number from one of the parent's own responses — so it does not have this problem; but any probe that sums usage records does.

Thinking blocks are not counted: they are not sent back to the provider (that case in `llm/convert.go` is empty), and counting them would make the estimate permanently higher than measured, turning the ratio above into a garbage number. Same for `Details` — they never reach the provider.

### Turn distribution: where `-max-turns` numbers come from

The same command, given a directory (or the literal `sessions`, resolving to your own session dir), answers a different question: **how many turns does a run actually take**. This is the number a `-max-turns` choice should be based on — a fixed cap set at the 75th percentile of one's own distribution is the sweet spot measured in the literature (arXiv 2510.16786), and pogo has its own history, so it measures its own rather than copying someone else's.

```
$ pogo -analyze-session sessions
Run Length Distribution: ~/.pogo/sessions
================================================================================

Population:
  Sessions read: 102
  Runs:          116
    finished (reached an answer):   110
    unfinished (cut off):           6
    no tool call, excluded:         22
  Population:    88 (finished, called at least one tool)

Turns per run (n=88):
  p50: 3
  p75: 5
  p90: 9
  p95: 13
  max: 25
  mean: 4.3
```

Four reading conventions, each a "measure it this way or measure it wrong" pitfall:

**The unit is the run, not the session.** A transcript contains several runs and possibly rewound-and-abandoned branches — counting turns by session is wrong twice in the upward direction. The analyzer walks only live branches along the parent chain and re-segments by prompt: abandoned branches were cut by a human, they are not turns the live work needed.

**A cut-off run is a lower bound, not an observation.** A run that hit the cap, was Ctrl-C'd, or dropped at turn N had live-needed turns ≥ N; mixing it into the percentiles pushes every number down, hardest at the tail — and the cap is chosen exactly from the tail. So they are excluded from the percentiles and listed separately by value: a cluster sitting on the same number is the only evidence that "there used to be a cap here" (the transcript does not record `-max-turns`); a scattered few reports plainly that they do not constitute evidence. When the censored share is too high, the report warns outright: the p75 is a floor, not an answer.

**Runs that never called a tool are excluded.** They end on turn one, carry no information about caps, and are numerous enough to drag the whole distribution down — measured, mixing them in moves the local p75 from 5 to 4 and p90 from 9 to 6, and the cap would be chosen based on "how much pure Q&A is in this history."

**The reminder at the end of the report is not a pleasantry.** The premise under which this distribution can be used to choose a cap is that this history looks like the work that will later be capped — interactive Q&A history will not contain unattended tasks, and the latter is the scenario a cap actually constrains.

**The use of this number is a soft cap** (`-soft-turns`, default 10). Hitting the soft cap does not end: a checkpoint message is injected telling the model "how many turns used, the soft cap, the hard cap," then a binary choice — wrap up, or continue and explain in one sentence what's still missing. **There is no parser**: the model's next action is the decision itself — calling another tool is an extension, a pure text answer is completion, going through the same end path as before. The mechanism guarantees two things: the decision was made informed, and "what's missing" lands in the transcript and is auditable. Extension needs no counter of its own; `-max-turns` is its cap. The checkpoint goes through the existing steering event path, visible automatically in JSON mode, web, and transcript; the `[pogo]` prefix marks it as harness-written. Web sessions have it fixed off (same reason as budgets: in a browser, a human is watching).

### Chained runs: reset, not compress

Above the soft cap there is one more layer: `-max-runs <n>` (`-p` only, default 1). When a run ends because **a budget was exhausted** — turn limit, token/cost/time budget, i.e., the ones in `end_reason` whose disposition is `continue` — the session forks a new branch, and a fresh run with an empty context picks up. This is reset, not compress: not a word is summarized, the old transcript stays in the same file as an abandoned branch, both ends traceable.

The handoff vehicle is **`handoff.md`**: when chaining is on, the system prompt gets an extra section telling the model its context will not carry to the next leg, and asking it to maintain in `.pogo/handoff.md` "the task in one sentence / what's done and how it was verified / what's left." The new run's opening prompt carries the original request verbatim (`<original_request>`, the same peg as `/compact`) + the handoff protocol: read handoff first, if there is a verification method then verify before starting, if the file does not exist then rebuild state from the workspace yourself. **The file shape is Markdown, not JSON** — a machine-checked contract is the job of step 8 (the evaluator); specifying fields before there is a checker is ceremony.

Two judgments are pinned by tests (`chain_test.go`): which `end_reason`s hand off (`turn_limit`/`token_budget`/`cost_budget`/`time_budget`/`max_tokens`), which don't (`stagnation` and `context_overflow` will just fail the same way on rerun, `transport_error`/`aborted` are unrelated to the task — this table is exactly the dispositions of §12.1, the link just reads it). Web sessions and REPL do not use this flag: with a human present, the word "continue" is cheaper than the mechanism.

Measured shape (`-max-turns 4 -max-runs 3`, a file-writing task needing 6+ turns): three legs completed the handoff, handoff.md wrote out the three Task/Done/Left sections per protocol.

**The adjudicator: `-evaluate` (default off).** Without it, the driver can only trust the run's own claims: "done" is what the model says, "not done" only means the cap interrupted it — and the case where the last leg spent its turns doing the work and the wrap-up answer didn't fit the budget is real (the work is done but the exit code is 1). Turned on, a **fresh read-only agent** (read/ls/find/grep, no bash/write/edit — the shape of an explore sub) speaks at two decision points: when some leg claims completion (if it fails, the findings hand off to the next leg, within the run budget); and when the runs are exhausted and the end reason is a budget-class one (if verification passes, it still exits 0). The adjudication protocol is one line `PASS` / `NEEDS_WORK` + concrete findings; **when the evaluator itself fails, it changes no outcome** — the behavior without it is the fallback.

## Context Cleaning (context editing)

**On by default.** When the prompt exceeds **four-fifths** of the model window, the oldest tool results are replaced in **the copy sent to the model** with a placeholder line:

```
· read {"path":"internal/store/store.go"}
  1840 lines (+0 -0)
…
… context edit: dropped 6 old tool result(s), ~31,200 tokens (prompt was ~104,900)
```

What the model sees is:

```
[1840 lines / 58KB of read output removed to fit the context window. Call the tool again if you still need it.]
```

It solves a death that is certain to happen: history only grows, every turn resends it in full, after reading dozens of large files the prompt crosses the window and the provider returns 400 — and 400 is not on `llm/retry.go`'s retry whitelist, so the run ends on the spot. Worse, the history does not get smaller as a result, **the very next message in the same session fails on the first call, permanently stuck, only a new session can recover**.

**And because the threshold is a guess, there is one more fallback: after the provider refuses, force-clean once and retry.** The number the threshold compares against is half measured and half estimated, so it can be crossed without anyone noticing; the provider's refusal is not a guess, it is entitled to trigger a cleaning directly. This one layer is what separates "this turn failed" from "this session is dead."

The forced one overrides three regular policies, all for the same reason — their purpose is to avoid paying for "a cleaning that may not be needed," and the refusal has already answered that question:

| What's overridden | Why |
|---|---|
| Trigger line | The number it compares against is half estimated; the provider's count is not |
| `clear_at_least` | It exists to not buy one cache miss to save a little. Here the alternative is not a cache miss, it's a dead session |
| Retention window shrinks to 1 | **Not 0**: clearing the one result the model was about to reason on amounts to asking the retry to answer a question whose evidence was just taken away |

**`ExcludeTools` is not overridden.** That is the caller explicitly saying "never clean this," and a mechanism that drops an explicit instruction the moment it's inconvenient is worse than one that honestly reports it cannot help. If it happens that this very exclusion is what makes the prompt not fit, then the provider's error is returned as-is.

**This pass is bounded, and the bound comes from an existing property**: the cleared set only grows, so on the second overflow within the same run forced cleaning finds nothing new to clear, `ok` is false, and the error is returned directly — no extra counter needed, no spinning.

**It has no event notification, by design.** Emitting another `turn_start` for the same turn would be wrong — `web/hub.go` is exactly where this turn's message id is minted, and the contract reads "one turn produces one assistant message" — and the right landing point would be a new event type, which means changing the wire contract rather than this file. In the end it is not silent either: what `editContext` reports on each pass is the **entire** frozen set, not just the newly added part, so the next turn's `turn_start` carries the grown number in both front ends.

**Classification goes through `llm.APIError`, not string matching.** Non-2xx used to be flattened into a single string, so "prompt too long" and "wrong key" — both 400 on the same provider, needing opposite handling — could only be told apart by matching prose. Now the status code, the provider's own `type`, and the message are all retained.

The check still **has to look at the message**, though, because the `type` the two vendors give does not separate them: on Kimi an overflow is `invalid_request_error`, and so is a missing parameter or a malformed body. So the phrase table was validated against the real endpoints, not copied from docs:

```
400 invalid_request_error
"Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"
```

Only phrases that "can only mean this one thing" are collected. The cost of a misjudgment is asymmetric: treating a malformed request as an overflow would make pogo work around a bug in the request by dropping context, which is worse than an honest error. For the same reason the check is **restricted to 4xx** — a 500 whose body happens to mention a token limit is a server fault, and dropping history to work around a fault is wrong.

`-context-edit off` turns it off; a number sets an absolute threshold.

### The shape is copied from Anthropic's `clear_tool_uses_20250919`

Chosen because this is the only design with a public contract and default values, not some blog's speculation. What was lifted verbatim:

| | |
|---|---|
| Trigger | cleaning starts only after the prompt exceeds the threshold |
| Retention | the most recent 3 tool results are kept as-is (Anthropic's default) |
| **Clear results, not calls** | the call of a read-class tool is kept with its arguments (their `clear_tool_inputs` defaults to `false`) — "the call happened, and against what" is the model's basis for deciding whether to redo it. **`write` / `edit` are the exception, see below** |
| Placeholder | each cleared result is replaced with explanatory text, so the model knows something was taken away, not that it was never there |
| Order | oldest cleared first |
| `clear_at_least` | if the cleanable amount is too small, don't clean at all, see below |
| Don't touch the transcript | they do it server-side, and the docs explicitly say the client retains the full unmodified history |

**Why mechanical cleaning rather than an LLM summary**: the only evidence with a controlled experiment (Lindenbauer et al., *The Complexity Trap*, DL4C@NeurIPS'25, on SWE-bench Verified across five model configurations) finds **simple discarding of old observations halves cost while matching, occasionally slightly exceeding, LLM summaries on solve rate**. And it is much cheaper here: no extra model call, no summary that could be wrong but cannot be checked, behavior that can be pinned verbatim by unit tests.

### Four spots adapted for pogo

**`write` / `edit` arguments are cleared too; read-class tools' arguments are not.** This one was measured, not guessed.

Running this project's own accumulated 100 sessions through the same accounting as `session.Compose`, the conclusion: the sessions that actually grew to a problem (7 at ≥50K) were **without exception 88%–99% tool output**, none were bloated by conversation text. But two of them were neither tool output nor conversation — they were **83% and 96% tool call arguments**, one being a `write` content of 42,328 estimated tokens. **And cleaning could clear 0% of them, because it only looks at results.**

Anthropic's `clear_tool_inputs: false` reason is right — "the call happened, and against what" is the basis for deciding whether to redo. But that reason covers `read` / `ls` / `find` / `grep` / `bash`: their arguments **are** a description of the call (a path, a glob, a command line). It does not cover `write` and `edit`: there the large argument is not a description of the call, **it is the payload itself, and the one payload in the world most certainly also present elsewhere — the file is on disk**. Re-reading the file is both cheaper and more truthful than carrying a copy in context: that copy starts lying the moment anyone else edits that file.

So what's cleared is the payload field, `path` stays (that is what Anthropic is really protecting), and the structure is untouched — `edits` is still an array, each element is still an object with two string fields, the length is still there, so "how many edits" remains answerable. The argument placeholder states only its own size (`[41KB cleared]`); the explanation is said once on the paired result placeholder.

`edit`'s `oldText` is cleared too, on purpose: after a **successful** edit that text is by definition no longer in the file, it can never match again — keeping it amounts to specifically preserving the one string in this call that has already gone stale. A **failed** edit is the reverse, and its arguments are never touched: `clearable` skips error results anyway, and `oldText` happens to be the one thing the model needs when self-correcting.

**The placeholder's last sentence varies by tool, because it is a suggestion the model will act on.** Anthropic's policy says the same line to every cleared result — `Call the tool again if you still need it.` That line is right when the only thing clearable is "a read," and pogo has three classes of tools for which it is not:

| What it costs to get back | Tool | That sentence |
|---|---|---|
| One call, from current state | `read` `ls` `find` `grep` | call again (the original) |
| The effect is that file | `write` `edit` | the change is on disk, go read that file |
| Re-runs what the command did | `bash` | only re-run if repeating is safe |
| Costs a full sub-run | `subagent` | getting it back means delegating again |
| Unclassified tool | — | only call again if a repeat call is safe |

The `write` / `edit` line is the most concretely wrong: rerunning a `write` would overwrite the file with content the model no longer holds, rerunning an `edit` fails outright because the `oldText` is no longer in the file.

The `bash` line was confirmed only after going through this project's own 100 transcripts. Across 75 real bash calls there were `git add -A && git commit -m ...`, `git cherry-pick <sha> && go test ./store/`, and a `cat > ~/.pogo/providers.json << 'EOF` heredoc — **rerunning those is not "rereading," it is a second side effect.** So that sentence only states a fact (rerunning does it again) and leaves the judgment to the model: the command text is right there in the argument just above, untouched, and it is better placed than this text to judge whether this command can be repeated. This is the same rule as the two subagent fence paragraphs — state facts, give no instructions.

The default for unclassified is **the cautious sentence rather than the encouraging one**, and the table lookup is explicit: `reRunFree` is exactly the zero value, and relying on the zero value would let "unregistered" silently equal "freely rerunnable," which is the one default that can turn "cleaning" into "an action." A test requires every tool in the default registry to be in the table, so adding a tool later forces this decision.

Measured on real transcripts: of 13 sessions ≥20K, 12 cleared **not a single extra token** (they were read-dominated, the existing mechanism already sufficed), while the one argument-dominated session went from "could clear only 922 of a 26,217 history" to "cleared 12,212, of which 11,290 from arguments" — **43 percentage points more on the same session**.

### Three spots that were already pogo-shaped

**`auto` is four-fifths of the window, not a hardcoded 100,000, and not their half.** Anthropic's default is fixed at 100,000 because their model windows cluster around 200K — that number happens to be half. pogo's catalog comes from your file and its windows can differ by several times; copying an absolute value would make a 1M model start cleaning at a tenth of its window, which is not the same policy. The window comes from each model's `context_window` in your file; a model absent from the catalog (window 0) gets `auto` disabled outright rather than a guessed threshold (`agent/contextedit.go`). The threshold follows the model on switch (`/model` and the browser's switcher both recompute).

The ratio is also not their half, **and this change has three reasons, the first two measured**:

- **The number being compared against is itself high.** The estimate-to-measured ratio is 0.98 (ASCII) / 0.83 (Chinese), so "half the estimate" actually lands at 42%–49% of the window. Triggering at half means dropping things when more than half the window is genuinely empty.
- **Cleaning is not free.** It rewrites part of the prompt, which invalidates the prefix cache: on Kimi a miss bills at about ten times a hit, on Zhipu about twice. Paying that to free up space no one has asked for yet is pure loss, and the model may go reread a file it could already see.
- **The premise that held when "half" was chosen is gone.** Back then running out of context was permanent: the provider returned 400, the retry logic did not let 4xx through, and the history did not get smaller — every subsequent message in the same session would fail. Being early was buying insurance against a cliff. **That cliff is now gone**: a refusal triggers one forced cleaning and one retry (see "after the provider refuses" above), and the cost of being late dropped from "a session" to "one wasted call."

**The remaining fifth is left for three things**, which is also why it's not nine-tenths: on Kimi the model's own output counts against the same quota (`prompt tokens + max_tokens exceeds the model specification`), so `MaxTokens` has to fit in this margin; the number being compared is half estimated and cannot be trusted to the ones digit; and cleaning happens **before** the call, while the turn it lets through will keep adding tool output to the history. Whether the margin suffices depends on each model's `context_window` and `max_tokens` in your file — the fifth above the trigger line must have room for MaxTokens. A test watches this assumption (the built-in catalog is empty now, so the test installs its own catalog to assert the margin against), because "a new model with a huge output cap and a tiny window" is exactly the thing that breaks it.

**Failed results are not cleared.** Anthropic makes no such distinction; pogo does: self-correction from error text is this project's signature behavior, and the entire timeline UI is built on "connecting the fix to the failure it fixed." Error text is also short, so keeping it costs almost nothing.

**`clear_at_least` has a default (8000 tokens); theirs defaults to none.** Their docs explain what it does: cleaning invalidates the cached prefix, so cleaning a tiny amount is just buying a cache miss for nothing. The cost is heavier here — both providers have implicit caching and **no API to edit an already-cached prefix**, and Kimi's miss bills at about ten times the hit (Zhipu's own docs say the discount is about half, so the two differ by 5×). The 8000 is a guess; it is the one parameter most worth calibrating against real session data.

### Two non-obvious but important properties

**Once cleared, always cleared.** A result that has been cleared stays cleared in every subsequent turn, even after the prompt has dropped back below the threshold. Otherwise: this turn cleans → next turn is below threshold so the original is restored → the turn after that exceeds again and cleans — **the prompt prefix flips back and forth, paying one cache miss per cycle for nothing.** On Anthropic's side this is free (their docs describe the fate of cleared results as "once seen, frozen," and the replacement text is byte-identical every time); pogo has to hold this set itself. The placeholder text is therefore a pure function of the original output's shape, containing no time and no "how many turns ago it was cleared."

**The trigger decision is "measured baseline + estimated delta."** Both halves are necessary; missing either is the very death this mechanism prevents: the server-measured number always lags one turn, and that lagging turn is exactly the dangerous one — a batch of tool results can add a hundred thousand tokens between two measurements, and a threshold that reads only the last measurement would sail right past on the turn that fills the window. Conversely, estimating the entire history would throw away the one number that is not a guess: the measured value already includes the system prompt, tool schemas, and wire framing, and `llm.EstimateTokens` models none of them. So it's measured baseline plus estimated delta — the same cut, and the same reason, as Codex's `body_after_prefix`.

### The task list gets pinned here

A `todo` result is state, not an event, so the rule is the reverse of other tools: **superseded old lists are cleared unconditionally** (no threshold consulted, they are copies of stale state), and **the latest one is never cleared**, even if it is the oldest thing in the entire history, even if the retention window would have come to it. It is the one record of "what to do now" that must survive into the next context window. It does not occupy a slot in the retention window either — those three slots are for the working set.

### What it does not solve

**This is not compaction.** If the history is bloated by conversation text rather than tool output, there is nothing to clean. The real dividing line is here: cleaning handles "tool output is large," summary compaction handles "the conversation is long." Which one is your actual load, the Context Composition section of `-analyze-session` will tell you — that is exactly why it exists. Compaction is the next section, and **by default it happens only when you ask.**

**The model may not reread.** The placeholder can only give the model the information it needs to recover; it cannot make it use it. The model may also continue to cite, from memory, content that is no longer there. This is the same class of uncheckable as a summary "may be wrong," just a different shape.

## Compaction (`/compact`)

Replaces the entire conversation with a summary. **By default it happens only when you type `/compact`; passing `-compact auto` arms the automatic paths** — a between-runs threshold (the window minus max(16384, a twentieth of the window)), a compact-and-replay retry on context overflow (at most once per prompt), and a breaker after 3 consecutive failures. Full decisions and staging in `docs/auto-compact.md`.

```
❯ /compact
compacted: 47 messages → 1, about 82400 → 1210 tokens (freed ~81190); the summary cost 79300 in / 640 out.
the full conversation is still in ~/.pogo/sessions/20260810T072148Z-f5e350b2.jsonl as an abandoned branch.
```

### Why it is not automatic by default

Cleaning is reversible — the output it drops can be fetched back, and the placeholder tells the model how. A summary is not: it is a lossy rewrite, produced by a model that may get it wrong, with no test that can catch that kind of wrong.

So the default trigger condition is "a human speaks up and asks." This is not dressing up a limitation as a principle; it is the specific condition that makes this loss acceptable — **the person making this decision is the one who knows what they still need.**

Two bodies of evidence back "not automatic":

- Measuring this project's own 100 sessions: the 7 that reached 50K estimated tokens were **without exception 88%–99% tool output**, and mechanical cleaning can free 90%–97% of that. What summaries exist to address is "conversation text bloating," a shape that **never once appeared** in the real data.
- The only controlled study on "policy compliance across the compaction boundary" (arXiv [2606.22528](https://arxiv.org/abs/2606.22528), 7 models, 1,323 episodes) shows: constraints declared before the boundary and then dropped by the summary push the violation rate from 0% to 30%, and **Chinese scenarios are 42 percentage points worse** — and both providers this project's deployment runs on are in that panel.

Trading a problem that hasn't happened for a measured risk should not be done automatically. When you ask for it, that's your tradeoff.

*(The paper content is a paraphrase of the abstract, rewritten as required by the license.)*

### What it guarantees

**The opening request is preserved verbatim.** This is the one thing in the entire design that comes directly from measurement: the study above compared various compaction strategies, and **only "keep the earliest turn" held the violation rate at 0%**, because the constraints are written in that turn. It is nearly free here — in measured large sessions user text is 0.4%–0.9%.

**The summarizer carries no tools, and the history is flattened into one user message.** These are the same decision. Anthropic recorded that a model asked to summarize with tools available will sometimes call a tool instead of answering, producing a `content: null` compaction block, and their answer was to write "do not call any tools" in the prompt. pogo does compaction client-side, so it has an option they don't — **don't provide tools, and don't send any `tool_use` block, so calling a tool is not discouraged but absent.** Flattening is the precondition that makes this work: a request carrying `tool_use` blocks without the tool declarations is rejected outright by some OpenAI-compatible endpoints.

**The summarizer is always the agent's own model, never a cheaper one.** The temptation is real, and the mechanism is right there (`subagent_model` is at hand). But in that study **violations track the model that wrote the summary, not the one that executes**: an agent reading a weak model's summary has a 53% violation rate, while reading its own summary is 0%. The summary is not a sub-task worth comparison-shopping; it is the one remaining record of those instructions.

**The injection boundary is explicit.** Tool output goes into the summarizer, and tool output here is the contents of files in the workspace — a file can carry a line aimed at "whoever reads it." So the content to be summarized is wrapped entirely in a `<transcript>` element, and the system prompt explicitly calls it data. pogo also starts from a slightly better place than implementations that summarize raw history directly: cleaning has already turned the oldest tool results into placeholders.

**Any failure leaves the conversation untouched.** Empty summary, summary larger than the original, a run in progress, the conversation changing during summarization — all four cases are "preserve as-is and explain why," costing at most one model call.

### The full record is not lost

The session file is an append-only tree, and `/compact` takes the same path as "rewind": `Fork("")` **abandons** the current branch and starts a new chain; the original records all stay in the file, just unreachable. So `-resume` replays the compacted history, while the full original is readable at any time. **The transcript is never edited.**

**But the cost of the abandoned branch does not vanish with it.** The cumulative usage counts **every** stats record in the file, not just the ones on the current branch — the provider has already been billed for those tokens, and which branch is live is irrelevant to that fact. This holds for rewind too, and it has to: `-token-budget` and `-cost-budget` are ceilings, and a ceiling that forgets what's been spent every time the conversation is reorganized is not a ceiling. It also keeps this in agreement with `-analyze-session` (which has always read all records linearly).

### When it will refuse

| Case | Reason |
|---|---|
| The conversation has one or zero messages | nothing to compress, and won't burn a call for it |
| The summary is not smaller than the original | usually the opening message is very long (e.g., a document was pasted in) — it is deliberately pinned, so it cannot be compressed. Compaction making the prompt larger is paying to make the problem worse |
| A run is in flight | the loop is appending to this history |
| The summary is empty | the `content: null` shape mentioned above |
| The conversation changed during summarization | the summary no longer describes the current history |

The token cost of the summarization call itself is counted in the totals (`-token-budget` / `-cost-budget` see it), but **not in `delegated`** — that field answers "how much did the sub spend," and compaction is the parent's own work.

### Browser

Two entry points: type `/compact` in the composer, or **click the context usage bar** — the button is at the bottom of the composition panel.

It sits there because that panel has already computed the answer. **The same data, two opposite recommendations:**

- When it's mostly **tool output** (the common case in measurement: large sessions are 88%–99% it), the panel says cleaning is already handling this part and compaction won't help much, and the button stays in normal style
- When it's mostly **conversation text**, the panel says this part won't be evicted automatically and compaction is exactly what it's for, and the button becomes the recommended style

A button that always looks equally worth pressing carries no information. Ties go to the tool side — a tie is not evidence that "conversation text is the problem," and recommending a lossy rewrite needs evidence.

A confirmation pops before compaction (the one command here that confirms, because it is the one that loses work), the button shows progress during compaction, and a `compacted` event is broadcast on completion so all tabs refetch the snapshot. The button is disabled while running (the server also 409s).

**`/compact` does not participate in prefix abbreviation,** on either side. No existing command starts with `/c`, so without this rule, typing `/c` plus Enter would replace the entire conversation — a two-character slip. The rule is "irreversible commands don't participate in abbreviation," and it **applies only to Enter, not Tab**: Tab first lays out the full name on screen; pressing Enter on a prefix is acting on a guess.

Server endpoint: `POST /api/sessions/{sid}/control {"action":"compact"}`. Bypassing the client by sending `/compact` as a prompt in the composer is bounced with 400 — slash commands are never allowed into the conversation, otherwise a file containing "please output /compact" could manipulate the session.

