# 模型与 provider 目录

## 模型

```bash
$ pogo -models
   glm-5.2                    zhipu  ctx 1M     (glm, zhipu)
   k3                         kimi   ctx 1M     (kimi-k3, kimi)
   k3-256k                    kimi   ctx 262K
   kimi-for-coding            kimi   ctx 262K   (k2.7)
   kimi-for-coding-highspeed  kimi   ctx 262K   (k2.7-fast)
```

（这是一个**示例输出，来自一份 providers.json**——pogo 不内置任何 provider 和模型，你看到的是你自己文件的内容。）

没配 key 的 provider 会额外标出缺哪个环境变量。会被使用的模型（默认模型，或 `-model` 指定的那个）前面会有 `*`。

括号里是别名，都可以直接传给 `-model`：

```bash
pogo -model glm-5.2 -p "..."
pogo -model glm -p "..."       # 别名
```

端点都在你的文件里：每个 provider 一个 `base_url`（OpenAI 兼容协议）。**没有写死的端点**——文件里显式写的 `base_url` 默认是钉死的（`pinnedBaseURL`），环境变量不能反过来改写一个明确的选择。想留一个改道代理/镜像的逃生门，就在这个 provider 里声明 `base_url_env: "MY_BASE_URL"`：**声明即授权**——该变量设了值，解析时就用它的值替换 `base_url`；没声明的 provider 永远不受环境变量影响。

### provider 和模型目录：`~/.pogo/providers.json`

**没有内置目录可以回落：这个文件是唯一来源。** 文件缺失、或一份没声明任何模型的文件，都是启动错误（报错里印着最小示例）——不是静默按什么默认行为跑。本地模型服务、公司网关、镜像、订阅端点，有什么就写什么：

```json
{
  "default": "qwen-big",
  "providers": {
    "local": { "base_url": "http://127.0.0.1:11434/v1", "key_env": "LOCAL_API_KEY" }
  },
  "models": [
    { "id": "qwen-big", "provider": "local", "aliases": ["qwen"],
      "context_window": 262144, "max_tokens": 8192,
      "subagent_model": "qwen-small",
      "price": { "input": 0.5, "output": 1.5, "cache_read": 0.05 } },
    { "id": "qwen-small", "provider": "local", "context_window": 32768, "max_tokens": 4096 }
  ]
}
```

规则：**文件即全部**。内置目录是空的，所以不存在「与内置合并」——加载出来的目录恰好等于文件声明的内容。`default` 可选，不写就是目录第一条。同一个 provider 名或模型 id 在文件里声明两次时，**后一条整条替换前一条**（不是按字段合并，见下）。`POGO_CONFIG` 可以改文件位置。

上面示例没列全。provider 一级还有三个各有真实出处的字段：`headers`（随每个请求附带的额外 header，DashScope 的 `X-DashScope-SSE` 是造出它的那个场景——名字和值都过校验，是配置不是凭据，可以写字面值）、`extra_body`（浅合并进每个请求体的顶层，网关要求的非标参数用）、`allow_insecure_http: true`（对**既非 loopback 也非私网**的远端放行明文 http，见下面的校验清单）。model 一级还有两个标记：`subagent: true` 把模型列入 explore 子代理的轮换池（`config.SubagentPool`，文件书写顺序即偏好顺序）；`vision: true/false` 显式声明模型是否原生收图，不写则按 id 猜——glm-4v、gemma4 这类名字自带答案，为说谎的 id 才需要写。文件一级的 `vision_model` 指定 web 会话读图用的视觉模型，配置后 web 会话多注册 `analyze_image` 工具（见[「工具」](tools.md)）。

#### 保存后同步已有会话

在设置中保存模型配置后，已打开的空闲会话立即更新当前模型的上下文窗口、输出上限和相关自动清理、压缩阈值。正在执行或压缩中的会话保留本轮实际配置，显示待生效提示，并在本轮结束后更新；其他窗口通过会话事件同步，无需重新建立会话。聊天记录、累计用量和用户已选择的模型保持不变。若所用模型已删除或无法解析，保留旧配置并提示同步失败，不自动切到另一个模型。

#### 在 curl 弹窗检测服务配置

模型服务的 curl 弹窗提供“检测配置”：选择 vLLM / OpenAI 兼容模型列表、Ollama 运行实例、Ollama 模型规格或 llama.cpp 运行参数，核对生成的请求后点击“发送检测”。请求由 Pogo 服务端发送，沿用当前接口的鉴权与自定义请求头，不附带生成请求的 `extra_body`。

检测只展示服务返回的上下文、输出上限、字段来源和原始响应；不会回填表单、保存配置或改变运行中的模型参数。运行实例、服务声明和 `num_ctx` 配置分别标注，缺失字段显示未知，不按模型名称猜测。所有模型配置仍由用户手动填写。关闭弹窗会取消本次查询。

#### `price`：`-cost-budget` 唯一的依据

可选，只有 `-cost-budget` 读它。**pogo 不内置任何模型的价格**，理由和不内置 `subagent_model` 映射一样：一个模型多少钱是一句关于**你的计费安排**的断言，pogo 没有依据替你做——订阅套餐的 per-token 成本甚至不是一个存在的量。价格只能来自你的文件。

所以 `-cost-budget` 在没有价格时**拒绝启动**，不是静默不生效：

```
$ pogo -cost-budget 5 -p "..."
pogo: -cost-budget needs a price for glm-5.2, and none is declared.
pogo ships no built-in prices: both built-in providers are subscription plans, ...
Either use -token-budget or -time-budget, which need no price, or declare the rate in ...
```

拒绝而不是警告，是因为花费上限**没有降级态**：「继续跑但不设限」是所请求内容的精确反面，而这恰好是没人盯着的那种运行。（对比 `subagent_model` 指错模型只警告 —— 那个有降级态：继承父模型，会话照跑。）

三个字段，**单位是每百万 token**：

| 字段 | 含义 |
|---|---|
| `input` | 未命中缓存的 prompt token 费率 |
| `output` | completion token 费率 |
| `cache_read` | 命中缓存的 prompt token 费率，可省略（省略即 0，表示缓存读免费） |

几个要点：

- **单位是你自己被计费的那个单位**，pogo 不打印货币符号也不换算，`-cost-budget` 按同一个单位比。这个项目的部署按人民币计费，所以写死 USD 会是又一句没依据的断言。
- **`cache_read` 不是可选的精细化。** kimi 的未命中约按命中价的十倍计、智谱约两倍，所以前缀复用好与不好的两次会话成本差距远大于 token 差距 —— 而那正是上限要抓的差距。算法是先减再算：`(input − cache_read 的量) × input费率 + cache_read的量 × cache_read费率 + output × output费率`，缓存部分不会被收两次。
- **`cache_read` 不许高于 `input`**，直接拒绝启动：缓存读按定义比未命中便宜，两者反了就是抄价格页时把字段调了个。负数和离谱的数一样拒绝。
- **写一个全零的 `price` 是有意义的**，它表示「这个端点不按 token 收费」（本地模型服务），此时 `-cost-budget` 接受并且永不触发。这和**不写** `price` 是两件事：后者会被拒绝。
- 重复声明同一个 id 是**整条替换**，所以第二次声明省掉 `price` 会把它丢掉 —— pogo 会在 stderr 点名（和丢掉 `aliases`、`context_window` 一样）。

**「覆盖那一条」是整条替换,不是按字段合并**——这一点值得单独说,因为它踩过一次。同一个 id 声明两次时，后一条里没写的字段**不会回落到前一条,而是直接没有**：

```json
{ "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576 }
```

这样一条会让前一条的 glm-5.2 **失去 `aliases`**（`-model glm` 从此找不到它）和 `max_tokens`。要么把字段抄全，要么干脆不写第二次。

**替换现在会在 stderr 说一声，而且整条替换丢掉了已有的字段时会点名是哪些：**

```
config: provider "zhipu" from the config file replaces the built-in one
config: model "glm-5.2" from the config file replaces the built-in one, dropping
        aliases, max_tokens (the whole entry is replaced, not merged field by field)
```

（消息里的 "built-in" 是上游内置目录时代留下的措辞；这个 fork 没有内置目录，它实际指的是文件里先声明的那一条。）

model 那一半原来是**静默的**，而那个静默有过实际代价（内置目录时期的事）：glm-5.2 的窗口在目录里从 200,000 更正成 1,048,576 之后,一份重新声明了这个 id 的配置文件把旧值留住了、什么都没说——于是 `-context-edit auto` 一直在真实窗口的十分之一处开始清理,浏览器占用条在六分之一处就变红,而修复看起来是生效的。**provider 会警告、model 不会**这个不对称,正是它没被发现的原因。

只有「行为会因此改变」的字段会被点名（`context_window`、`max_tokens`、`aliases`、`subagent_model`、`followup_model`、`subagent`、`vision`、`price`）。**`provider` 变了不算丢**——把一个 id 指到别的 provider 上是改写声明的正常操作，不是事故。

**`subagent_model`:只读 subagent 跑的模型。** 查一件东西在哪实现,比实现它轻;而且这是延迟最显眼的场景——父的一轮被阻塞着等答案。不写就继承父的模型,也就是现在的行为。**只有 explore 模式吃这个字段**:edit 子做的是父本来要做的活,偷偷降级会改变任务结果,而不是省一次查询的钱。

**`followup_model`:回合结束后生成追问建议 chips 的模型。** 生成是小调用、在回答渲染完之后才开始——这正是便宜模型为之而生的场景。不写就由会话自己的模型生成,`-followups=false` 则整个关掉。

**`vision_model`:web 会话读图用的视觉模型。** 配置后 web 会话多注册 `analyze_image` 工具（文本模型把图片连同问题交给它描述，见[「工具」](tools.md)）。不配置就没有这个工具；指向不存在的模型时警告并当作没配置，不阻断。

pogo **不内置任何 `subagent_model` / `followup_model` 映射**。哪个模型是哪个模型的便宜同族,这是一句定价断言,pogo 没有依据替你做。

#### 为什么只读 home,不读项目目录

这是安全决定,有具体前车之鉴:**CVE-2026-21852**——Claude Code 的项目加载流程允许仓库自带一个设置文件把 base URL 指向攻击者的端点,而 API key 在用户被问到「是否信任这个项目」**之前**就已经发过去了。一个决定「你的凭据发到哪台主机」的文件,不能是 `git clone` 能捎带过来的东西。home 目录本来就和二进制同等可信,工作目录不是。

所以**没有项目级 provider 配置,也没有对它的合并**。在工作目录里发现这个文件时 pogo 会在 stderr 说一句并忽略它——只有两种解释,写错了地方,或者有人在试。

还有三条校验,都会直接拒绝启动:

- **`key_env` 只能是环境变量名**,不能是 key 本身。这是最可能犯的错,而错误信息**不会回显那个值**——它很可能就是你的密钥。
- **远端必须 https**。凭据在每个请求的 header 里,明文发到远端等于交给链路上任何人。loopback(`127.0.0.1` / `localhost` / `::1`)和私网地址例外——本地/内网模型服务恰是写这个文件的主要理由,而且那段链路是你自己的。其余远端要走明文 http,必须在该 provider 上显式写 `allow_insecure_http: true`:把「降级到不加密」从静默行为变成一条看得见的声明,没读过报错就照抄的人拿不到它。
- **不认识的字段直接报错**。`"provders"` 这种拼错如果静默忽略,你会以为某个设置生效了,而它没有。

`subagent_model`、`followup_model` 和 `vision_model` 是仅有的例外:指向一个不存在的模型时只降级（继承/会话模型生成/不注册读图工具）并警告一句,不阻断——它们是可选字段,为它搭掉整个会话是错的取舍。


---

<a id="english"></a>

# Models and the Provider Catalog

## Models

```bash
$ pogo -models
   glm-5.2                    zhipu  ctx 1M     (glm, zhipu)
   k3                         kimi   ctx 1M     (kimi-k3, kimi)
   k3-256k                    kimi   ctx 262K
   kimi-for-coding            kimi   ctx 262K   (k2.7)
   kimi-for-coding-highspeed  kimi   ctx 262K   (k2.7-fast)
```

(This is an **example output from one providers.json** — pogo compiles in no providers or models, so what you see is the content of your own file.)

A provider with no key set is additionally flagged with which env var is missing. The model that would be used (the default, or the one `-model` names) is marked with a `*`.

The parentheses hold aliases, all of which can be passed directly to `-model`:

```bash
pogo -model glm-5.2 -p "..."
pogo -model glm -p "..."       # alias
```

Endpoints live in your file: one `base_url` per provider (OpenAI-compatible protocol). **No endpoint is compiled in.** A `base_url` written in the file is pinned by default (`pinnedBaseURL`) — an ambient environment variable must not reroute an explicit choice. If you want an escape hatch for a proxy or a mirror, declare `base_url_env: "MY_BASE_URL"` on that provider: **declaring it is the opt-in** — when that variable is set, its value replaces `base_url` at resolve time. Providers without the field are never moved by the environment.

### The provider and model catalog: `~/.pogo/providers.json`

**There is no built-in catalog to fall back on: this file is the only source.** A missing file, or one that declares no models, is a startup error whose message prints a minimal example — not a silent fallback to some default behavior. A local model server, a corporate gateway, a mirror, a subscription endpoint: write what you have:

```json
{
  "default": "qwen-big",
  "providers": {
    "local": { "base_url": "http://127.0.0.1:11434/v1", "key_env": "LOCAL_API_KEY" }
  },
  "models": [
    { "id": "qwen-big", "provider": "local", "aliases": ["qwen"],
      "context_window": 262144, "max_tokens": 8192,
      "subagent_model": "qwen-small",
      "price": { "input": 0.5, "output": 1.5, "cache_read": 0.05 } },
    { "id": "qwen-small", "provider": "local", "context_window": 32768, "max_tokens": 4096 }
  ]
}
```

The rule: **the file is everything**. The built-in catalog is empty, so there is no "merging with built-ins" — the loaded catalog equals exactly what the file declares. `default` is optional; without it, the first catalog entry is used. When the same provider name or model id is declared twice in the file, **the later entry replaces the earlier one wholesale** (not field by field — see below). `POGO_CONFIG` can change the file location.

The sample above does not list every field. Providers take three more, each with a real origin: `headers` (extra headers on every request — DashScope's `X-DashScope-SSE` is the case that created it; names and values are validated, they are configuration rather than credentials and may hold literal values), `extra_body` (shallow-merged over every request body's top level, for gateways demanding non-standard parameters), and `allow_insecure_http: true` (permits plain http to a remote that is **neither loopback nor private-network** — see the validation list below). Models take two more flags: `subagent: true` lists the model in the explore-subagent rotation pool (`config.SubagentPool`; the file's writing order is the preference order); `vision: true/false` states explicitly whether the model takes images natively — unset means judge by the id, since glm-4v or gemma4 say so in the name, and the explicit form is for ids that lie. At file level, `vision_model` names the vision model a web session reads images with; configuring it registers the extra `analyze_image` tool (see [Tools](tools.md)).

#### Synchronize existing conversations after saving

Saving model settings immediately updates idle conversations using that model, including the context window, output limit and automatic clearing/compaction thresholds. Active runs and compactions retain their effective configuration and show a pending notice until they finish. Session events update other open windows without replacing transcripts, usage totals or the selected model. If that model was removed or cannot be resolved, synchronization reports an error and retains the old configuration rather than silently selecting another model.

#### Inspect service configuration from the curl dialog

Choose **Inspect configuration** in a model provider's curl dialog, then select a vLLM / OpenAI-compatible model list, Ollama running instances, Ollama model specifications or llama.cpp runtime parameters. Review the request and send the inspection from the Pogo server, using the current provider's credentials and custom headers without generation `extra_body` fields.

Inspection only displays returned context/output limits, source fields and the raw response. It never populates the form, saves configuration or changes running model parameters. Runtime, declared and configured values are labeled separately; missing values remain unknown. All model settings stay manually configured. Closing the dialog cancels the query.

#### `price`: the sole input to `-cost-budget`

Optional; only `-cost-budget` reads it. **pogo ships no built-in prices for any model**, for the same reason it ships no `subagent_model` mapping: how much a model costs is a claim about **your billing arrangement**, and pogo has no basis to make it for you — a subscription plan's per-token cost is not even a quantity that exists. Prices can only come from your file.

So `-cost-budget` **refuses to start** when no price is declared, rather than silently doing nothing:

```
$ pogo -cost-budget 5 -p "..."
pogo: -cost-budget needs a price for glm-5.2, and none is declared.
pogo ships no built-in prices: both built-in providers are subscription plans, ...
Either use -token-budget or -time-budget, which need no price, or declare the rate in ...
```

Refuse rather than warn, because a spend cap **has no degraded mode**: "keep running but without a cap" is the exact opposite of what was asked, and that happens to be the kind of run no one is watching. (Contrast `subagent_model` pointing at a missing model — that one only warns, because it has a degraded mode: inherit the parent model and the session goes on.)

Three fields, **unit: per million tokens**:

| Field | Meaning |
|---|---|
| `input` | rate for prompt tokens that miss cache |
| `output` | rate for completion tokens |
| `cache_read` | rate for prompt tokens that hit cache; may be omitted (omitted = 0, meaning cache reads are free) |

A few notes:

- **The unit is whatever unit you yourself are billed in.** pogo prints no currency symbol and does no conversion; `-cost-budget` compares in the same unit. This project's deployment is billed in CNY, so hardcoding USD would be yet another unfounded assertion.
- **`cache_read` is not an optional refinement.** Kimi's miss bills at roughly ten times its hit, Zhipu's roughly twice, so two sessions with good vs. bad prefix reuse differ in cost far more than they differ in tokens — and that is exactly the gap a cap is meant to catch. The algorithm subtracts first: `(input − cache_read volume) × input rate + cache_read volume × cache_read rate + output × output rate`; the cached portion is not charged twice.
- **`cache_read` may not exceed `input`,** and startup is refused: cache reads by definition cost less than misses; if the two are flipped, someone transcribed the price page and swapped the fields. Negatives and absurd values are refused the same way.
- **Writing an all-zero `price` is meaningful**, it states "this endpoint does not bill per token" (a local model server); in that case `-cost-budget` accepts it and never triggers. This is different from **not writing** `price`, which is refused.
- Declaring the same id twice is a **whole-entry replacement**, so omitting `price` in the second declaration drops it — pogo names it on stderr (same as dropping `aliases`, `context_window`).

**But "override that one entry" is a whole-entry replacement, not a field-by-field merge** — this deserves its own paragraph because it has bitten once. When the same id is declared twice, fields the later entry did not write **do not fall back to the earlier one, they are simply absent**:

```json
{ "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576 }
```

Such an entry makes the earlier glm-5.2 **lose its `aliases`** (`-model glm` no longer finds it) and `max_tokens`. Either copy the fields over in full, or don't declare the id a second time.

**A replacement now announces itself on stderr, and when a whole-entry replacement drops fields that existed, it names which**:

```
config: provider "zhipu" from the config file replaces the built-in one
config: model "glm-5.2" from the config file replaces the built-in one, dropping
        aliases, max_tokens (the whole entry is replaced, not merged field by field)
```

(The "built-in" in the message is wording left over from upstream's built-in-catalog era; this fork has no built-in catalog, and what it actually refers to is the entry declared earlier in the file.)

The model half used to be **silent**, and that silence had a real cost (a story from the built-in-catalog era): after glm-5.2's window was corrected in the catalog from 200,000 to 1,048,576, a config file that redeclared this id kept the old value and said nothing — so `-context-edit auto` kept cleaning at one-tenth of the real window, the browser usage bar went red at one-sixth, and the fix looked as if it had taken effect. **Provider warns, model does not** — that asymmetry is exactly why it went undetected.

Only fields whose behavior would change are named (`context_window`, `max_tokens`, `aliases`, `subagent_model`, `followup_model`, `subagent`, `vision`, `price`). **`provider` changing does not count as a loss** — pointing an id at a different provider is a normal edit of a declaration, not an accident.

**`subagent_model`: the model read-only subagents run.** Looking up where something is implemented is lighter than implementing it; and this is the scenario where latency is most visible — a parent turn is blocked waiting for the answer. Without it, the parent's model is inherited, which is the current behavior. **Only `explore` mode consumes this field**: an `edit` sub does the work the parent would have done, so silently downgrading it changes the task result rather than saving the cost of one lookup.

**`followup_model`: the model that writes the follow-up suggestion chips after a run ends.** The generation is a small call that starts only after the answer has rendered — exactly the scenario a cheaper model is made for. Without it, the session's own model writes them; `-followups=false` turns the whole thing off.

**`vision_model`: the vision model a web session reads images with.** Configuring it registers the extra `analyze_image` tool in web sessions (the text model hands images and a question to it — see [Tools](tools.md)). Without it there is no such tool; pointing it at a nonexistent model warns and behaves as unconfigured, without blocking.

pogo **ships no `subagent_model` or `followup_model` mapping**. Which model is the cheap cousin of which other model is a pricing assertion; pogo has no basis to make it for you.

#### Why it only reads home, not the project dir

This is a security decision with a concrete precedent: **CVE-2026-21852** — Claude Code's project-loading flow allowed a repo to ship a settings file that pointed the base URL at an attacker's endpoint, and the API key had already been sent **before** the user was asked "do you trust this project." A file that decides "which host your credentials go to" cannot be something a `git clone` can carry along. The home directory is by construction as trusted as the binary; the working directory is not.

So **there is no project-level provider config, and no merging against it either**. When this file is found in the working directory, pogo says so on stderr and ignores it — there are only two explanations, you wrote it in the wrong place, or someone is trying.

Three more validations, all of which refuse to start:

- **`key_env` must be an environment variable name,** not the key itself. This is the most likely mistake, and the error message **does not echo the value** — it is very likely your secret.
- **Remote must be https.** Credentials travel in the header of every request; sending them in cleartext to a remote hands them to anyone on the path. Loopback (`127.0.0.1` / `localhost` / `::1`) and private-network addresses are excepted — local and intranet model servers are exactly a main reason to write this file, and that segment of the path is your own. For any other remote over plain http, the provider must say `allow_insecure_http: true` explicitly: it turns "downgrade to unencrypted" from a silent behavior into a visible declaration, and someone who never read the error cannot get it by accident.
- **Unknown fields error out.** A misspelling like `"provders"`, if silently ignored, would let you believe some setting had taken effect when it had not.

`subagent_model`, `followup_model`, and `vision_model` are the only exceptions: pointing at a nonexistent model only degrades (inherit / the session's own model / no image tool registered) with a warning, it does not block — they are optional fields, and tanking an entire session over one would be the wrong trade.

