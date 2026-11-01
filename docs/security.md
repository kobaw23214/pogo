# 安全边界

> 2026-09-29 核对：下文描述各工具的保护机制，不是多租户安全验收结论。完整范围见 [项目现状](project-status.md)。

## 用户、Agent 与进程的边界

会话归属由账号和 SQLite catalog 校验；Studio 默认目录按用户和 Agent 划分，同一用户的 memory、journal、checkpoint 仍共享。工作目录不同不意味着进程只能看见该目录。

当前普通会话的 Bash 没有传入已有的严格 `CommandScope`，终端入口也传 nil scope。通用 bubblewrap 沙箱先以 `--ro-bind / /` 提供文件系统视图，再开放 workspace、HOME、TMPDIR 等写入范围，并额外遮蔽私有 Skill 仓库。只读视图仍允许读取，HOME 写入范围也不能视为每用户专属。

因此，不能用下文的路径守卫、沙箱存在或会话 404 测试，替代跨用户 Bash/终端/子进程隔离验收。工具型 Skill 的调用级隔离也不能自动覆盖普通 Shell。该边界本轮仅做静态核查，未尝试读取真实用户的其他数据。

## 安全边界

路径守卫防手滑，bash 走 OS 沙箱：

- `read` / `ls` / `find` / `grep` / `write` / `edit` 的路径被限制在工作目录内，`../` 逃逸会被拒。两组额外根，不对称是有意的：已加载的 **skill 目录只读**（四个读工具能进，`write` / `edit` 进不去——模型改不了自己的指令），**记忆目录可写**（六个都能进，因为它必须能修正自己的笔记）。两者是 `tools.Options` 上两个**不同的字段**，所以没有哪个调用方能靠传错一个布尔把 skill bundle 变成可写的。逃逸检查在记忆目录里照样生效
- 判定先解析符号链接再比对前缀，所以「root 内的链接指向外部」拦得住。**大小写不敏感的卷（macOS / Windows 默认）上会多问文件系统一次**：那里 `/Users/x` 和 `/users/x` 是同一个目录的两种拼法，而解析符号链接并不规范化大小写，所以拼法不同的合法路径曾经被拒。现在字节比对不中时，再用 device+inode 确认两个拼法是不是同一个目录——**折叠的是比对，不是判定**，因为大小写敏感的卷上 `/tmp/Foo` 和 `/tmp/foo` 确实是两个目录，把它们当成一个才是真开了洞
- `bash` **没有**这个限制（它就是个 shell），受的是 OS 级沙箱的约束（Linux 上是 bubblewrap，见下节），并有可调超时（默认 120 秒、上限 600 秒）和输出截断
- **输出被截断时全文落盘，而落盘目录是可读根之一。** 适用于所有「跑了才有输出」的工具——`bash`、`tools.json` 声明的工具、`browser`、`subagent`——因为命令已经跑过了，砍掉的部分不重跑一次就拿不回来。`read` / `ls` / `find` / `grep` **刻意不落盘**：源还在那儿，read 提示用 `offset` 续读、grep 收窄 pattern 就行，而给它们落盘会造出一条「读 spill 文件→又被截断→又落盘」的级联。目录默认 `~/.pogo/spill`（`POGO_SPILL_DIR` 可改），由 `tools.New` 在算可读根时一并并进去——**这一点是结构保证而不是部署纪律**：note 让模型去 read 那个路径，而 `read` 拒绝可读根之外的一切，所以设了 `POGO_READ_ROOTS` 的部署曾经拿到的是死链。文件名按内容寻址（`{工具}-{sha256前缀}.log`），所以同样的输出只占一个文件，而且 note 逐字稳定——否则每次调用一个新的随机文件名会让停滞检测永远不触发。**只在整份都落盘之后才报路径**，写失败时明说「存不下来」而不是给一个指向空文件的路径
- 命令字符串作为单个 argv 传给 `bash -c`，不做字符串拼接，没有注入面
- 取消会杀掉整个进程组，不只是 `bash` 本身。否则 `(sleep 60; ...) &` 这样的子孙进程会活下来被 init 收养——取消一轮的目的本来就是停掉它启动的 `go test`
- `-web` 默认只绑 `127.0.0.1`，`/api/*` 一律要凭据（token 或密码登录的会话 cookie，见「密码登录」），跨域请求一律拒绝；`bash` 在 `standard` 模式下需要人工批准

`bash` 能做的事和你自己在终端里一样。Linux 上有 bubblewrap 沙箱兜底（下节）；macOS / Windows 没有对应实现，放到不受信任的环境里跑之前，请自己加隔离（容器、专用用户）。审批闸门是给人留一道确认，不是安全边界——尤其别指望它拦住什么，`auto` 模式下它完全不在场。

### bash 的 OS 沙箱（Linux）

Linux 上 `bash` 与 `tools.json` 声明的命令都跑在 [bubblewrap](https://github.com/containers/bubblewrap) 里：可写只有工作区、`HOME` 和 `TMPDIR`，**默认断网**（文件系统圈住了、网络没圈，等于把「读到凭据」放行成「传出凭据」，所以网络这半默认关），环境变量只带 shell 活下去所需的最小集（`PATH`/`HOME`/`TMPDIR`/`LANG`/`LC_ALL`/`TERM`/`TZ`）——**没有任何 `*_API_KEY`**，这正是沙箱的意义。启动时探测一次 bwrap 是否存在且真的能跑（`tools/sandbox_linux.go`）。

三档模式（`POGO_SANDBOX`，默认 `degrade`）：

| 模式 | 行为 |
|---|---|
| `enforce` | 沙箱不可用就**拒绝执行**命令。生产档位：想要隔离的部署静默失去隔离，比响亮地失败更糟——前者靠事故才发现 |
| `degrade`（默认） | 沙箱可用就用，不可用带警告跑在圈外。默认档，因为 pogo 也跑在 macOS 和关了非特权 userns 的 Linux 上，把它们变成硬失败是伪装成安全改进的破坏性变更 |
| `off` | 从不沙箱。显式逃生门，让「关掉」是一个被记录的决定 |

三个细调变量：`POGO_SANDBOX_NET=1` 放行沙箱内网络；`POGO_SANDBOX_ENV=JAVA_HOME,CAPA_RULES` 追加带入的环境变量名（部署工具链需要自己的变量——这个包不该认识 Ghidra 才能让部署用上它）；`POGO_SANDBOX_WRITABLE` 追加可写目录（os.PathListSeparator 分隔）。

沙箱还承载着一条审批规则：**`tools.json` 里声明 `readOnlyHint: true` 的工具免审，前提就是命令在沙箱内跑。** 设置对话框可以在运行时切换沙箱（`PUT /api/sandbox`，改动持久化进 prefs.json 且每次变更都打日志）——而免审优待是按**生效模式**计算的：把边界关掉的那一刻，免审跟着收回、审批卡回来，不会留一个静默的洞（`web/sandboxapi.go`）。定时任务是无人值守的全自动 agent，只要沙箱可用就**强制 `enforce`**（`schedule/exec.go`）——没有人在看，降级等于没有边界。

绑定到非 loopback 地址时的启动警告也按沙箱状态措辞（`serve.go`）：沙箱活着就说命令「跑在沙箱内（工作区/HOME/TMPDIR 可写、断网）」，不活着才说「未沙箱」——一句过时的安抚比没有信息更糟。

### spill 目录的自动清理

spill 文件只增不减会无限长大，所以 `-web` 进程带一个定时清扫：**启动时扫一次，之后每 6 小时扫一次**，把 mtime 超过保留天数的 `*.log` 删掉；写入中途崩溃留下的 `.partial-*` 残骸超过 1 小时一律删（与保留天数无关）。保留策略只看 **mtime 年龄**——文件名按内容寻址、跨会话共享去重，没有「哪个会话还在引用它」的计数可做，所以引用计数从设计上就不存在。

- **设置项**：`prefs.json` 里的 `spill_retention_days`，默认 **7**，**0 = 不自动清理**（目录会一直长，直到手动清理），上限 3650。文件在 `~/.pogo/prefs.json`（`POGO_PREFS_FILE` 可改）；浏览器设置对话框的「存储」一节可以直接改，并显示当前目录占用（文件数/字节数）和上次清扫结果。
- **手动清理**：CLI 无常驻进程，不做后台清扫（和 `-checkpoints-prune` 同一个理由），手动跑 `pogo -spill-prune`，与自动清扫读同一个 prefs.json；`-spill-prune-days N` 临时指定保留天数、不写回设置。
- **失败规则**：prefs.json 存在但读不出来（坏 JSON、值越界）时，自动清扫**暂停并打一行告警**，`-spill-prune` 直接报错——绝不退回默认值去删文件，删多是唯一不可恢复的方向。
- **死链行为**：transcript 里的落盘路径只是文本，没人解引用；文件被清掉后，旧会话里那条路径再读就是 ENOENT，和「当初就没落盘成功」等价，回放与 `-resume` 都不受影响。


---

<a id="english"></a>

# Security Boundary

Reviewed on 2026-09-29. Account/API ownership and per-Agent default directories are implemented, but strict cross-user process containment has not been accepted. Ordinary Bash and terminal entrypoints do not currently receive the available strict `CommandScope`; the generic sandbox exposes the filesystem read-only and opens writable roots including HOME. Private Skill-store masking is a separate protection. The mechanisms below must not be read as a tenant-isolation guarantee. See [project status](project-status.md).

## Security Boundary

The path guard is slip-proofing; bash runs in an OS sandbox:

- Paths of `read` / `ls` / `find` / `grep` / `write` / `edit` are confined to the working directory; `../` escapes are refused. Two extra roots, asymmetrically, on purpose: a loaded **skill directory is read-only** (the four read tools enter, `write` / `edit` cannot — the model cannot rewrite its own instructions); the **memory directory is writable** (all six enter, because it must be able to correct its own notes). These are two **different fields** on `tools.Options`, so no caller can turn a skill bundle writable by passing the wrong boolean. The escape check still applies inside the memory directory.
- The check resolves symlinks before comparing prefixes, so "a link inside root pointing outside" is caught. **On case-insensitive volumes (macOS / Windows default) it asks the filesystem one more question**: there, `/Users/x` and `/users/x` are two spellings of the same directory, and resolving symlinks does not normalize case, so legitimately different spellings of the same path used to be refused. Now, when byte comparison fails, device+inode is used to confirm whether two spellings are the same directory — **what's folded is the comparison, not the judgment**, because on case-sensitive volumes `/tmp/Foo` and `/tmp/foo` really are two directories, and treating them as one would be an actual hole.
- `bash` **does not** have this restriction (it is a shell); what confines it is the OS-level sandbox (bubblewrap on Linux — see the section below), plus an adjustable timeout (default 120s, ceiling 600s) and output truncation.
- **Truncated output is spilled to a file, and the spill directory is one of the readable roots.** This covers every tool whose output only exists because something ran — `bash`, tools declared in a `tools.json`, `browser`, `subagent` — because the command has already happened and the part that was cut cannot be had again without paying for the whole run. `read` / `ls` / `find` / `grep` are **deliberately not spilled**: the source is still there, so read's `offset` hint and a narrower grep pattern are the recovery path, and spilling them would set up a truncate-spill-read-truncate cascade. The directory defaults to `~/.pogo/spill` (`POGO_SPILL_DIR` overrides) and `tools.New` folds it into the readable roots as it computes them — **a structural guarantee rather than a deployment discipline**: the note tells the model to read that path, `read` refuses everything outside the roots, and any deployment setting `POGO_READ_ROOTS` used to get a dead link. Filenames are content-addressed (`{tool}-{sha256 prefix}.log`), so identical output occupies one file and the note is byte-stable — a fresh random name per call is what kept stagnation detection from ever firing. **The path is reported only once the whole thing is on disk**; on write failure it says plainly "could not be saved" rather than handing back a path to an empty file.
- The command string is passed as a single argv to `bash -c`, no string concatenation, no injection surface.
- Cancellation kills the whole process group, not just `bash` itself. Otherwise grandchildren like `(sleep 60; ...) &` would survive and be reaped by init — and the point of cancelling a turn was to stop the `go test` it started.
- `-web` binds to `127.0.0.1` only by default, `/api/*` always requires a credential (the token or a password-login session cookie — see "Password login"), cross-origin requests are always refused; `bash` requires human approval under `standard` mode.

What `bash` can do is what you could do yourself in a terminal. On Linux the bubblewrap sandbox is the backstop (next section); macOS / Windows have no counterpart, so before running in an untrusted environment there, add isolation yourself (container, dedicated user). The approval gate is there to give a human one confirmation step, not a security boundary — in particular, do not expect it to stop anything; under `auto` mode it is not present at all.

### The bash OS sandbox (Linux)

On Linux, `bash` and every command declared in a `tools.json` run inside [bubblewrap](https://github.com/containers/bubblewrap): only the workspace, `HOME`, and `TMPDIR` are writable, **the network is off by default** (confining the filesystem while leaving the network reachable would be approving "read a credential" as "exfiltrate a credential"), and the environment carries only the minimum a shell needs (`PATH`/`HOME`/`TMPDIR`/`LANG`/`LC_ALL`/`TERM`/`TZ`) — **no `*_API_KEY` of any kind**, which is the point. bwrap's presence and actual working-ness are probed once at startup (`tools/sandbox_linux.go`).

Three modes (`POGO_SANDBOX`, default `degrade`):

| Mode | Behavior |
|---|---|
| `enforce` | **Refuses to run commands** when the sandbox is unavailable. The production setting: a deployment that wanted containment and silently stopped having it is worse off than one that fails loudly — the first kind is discovered by an incident. |
| `degrade` (default) | Uses the sandbox when it works, falls back to running unconfined with a warning when it does not. The default, because pogo also runs on macOS and on Linux hosts with unprivileged user namespaces switched off, and turning those into hard failures would be a breaking change dressed up as a security improvement. |
| `off` | Never sandboxes. An explicit escape hatch, so "turn it off" is a recorded decision. |

Three tuning variables: `POGO_SANDBOX_NET=1` leaves the network reachable from inside; `POGO_SANDBOX_ENV=JAVA_HOME,CAPA_RULES` names extra environment variables to carry in (a deployment's tool chain needs its own — this package should not have to know about Ghidra to let a deployment use it); `POGO_SANDBOX_WRITABLE` names extra writable directories (os.PathListSeparator separated).

The sandbox also carries an approval rule: **a `tools.json` tool declared `readOnlyHint: true` is auto-allowed precisely because its command runs inside the sandbox.** The settings dialog can switch the sandbox at runtime (`PUT /api/sandbox`, persisted to prefs.json and logged on every change) — and the auto-allow is computed from the **effective mode**: the moment the override takes the boundary away, the exemption collapses and the approval cards come back instead of leaving a silent hole (`web/sandboxapi.go`). Scheduled tasks are unattended fully-automatic agents; whenever the sandbox is available they **pin `enforce`** (`schedule/exec.go`) — nobody is watching, and degrade means no boundary.

The startup warning for non-loopback binds is also worded by sandbox state (`serve.go`): with the sandbox alive it says commands run "inside a sandbox (workspace, HOME and TMPDIR writable, no network)", and only says "not sandboxed" when that is true — a stale reassurance is worse than no message.

### Automatic spill cleanup

Spill files only ever grow otherwise, so the `-web` process runs a timed sweep: **once at startup, then every 6 hours**, deleting `*.log` files whose mtime exceeds the retention; `.partial-*` debris from a write that crashed mid-rename goes after one hour regardless of the retention. Retention is by **mtime age alone** — filenames are content-addressed and shared across sessions, so there is no reference count to consult and none is attempted.

- **The setting**: `spill_retention_days` in `prefs.json`, default **7**; **0 disables automatic cleanup** (the directory then grows until pruned by hand); 3650 at most. The file is `~/.pogo/prefs.json` (`POGO_PREFS_FILE` overrides); the settings dialog's "Storage" section edits it and shows the directory's current usage (files/bytes) and the last sweep's outcome.
- **Manual cleanup**: the CLI has no daemon and runs no background sweep (same reason as `-checkpoints-prune`), so by hand it is `pogo -spill-prune`, reading the same prefs.json; `-spill-prune-days N` overrides the retention for one run without writing it back.
- **Failure rule**: when prefs.json exists but cannot be read (bad JSON, an out-of-range value), the sweep **pauses with a one-line warning** and `-spill-prune` errors out — it never falls back to the default to delete with, because deleting too much is the one unrecoverable direction.
- **Dead-link behaviour**: the path in a transcript is plain text nothing dereferences; once its file is pruned, reading it back is ENOENT, the same answer a failed spill gives — replay and `-resume` are unaffected.

