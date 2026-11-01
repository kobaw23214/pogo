# 定时任务

> 2026-10-23 Web 更新：Agent 的入口已升级为「后台任务」，时间与条件触发共用持久执行队列；Web 准入、恢复和操作语义以 [后台任务](background-tasks.md) 为准。下文的独立 CLI / OS 调度说明仍适用，旧 Web 面板描述保留作历史参考。

> 2026-09-29：交互 TUI、无界面 CLI 与 Web 定时器是不同层。普通 prompt 任务仍通过 CLI 子进程执行；工作流定时任务在 Web 进程内运行，见 [工作流](workflow.md)。Windows 的非 Unix 调度锁当前返回不支持，不能仅凭管理页面和 internal 后端存在就认定任务可执行。

自动 worktree 与相关 CLI 已退役；子任务的并发和文件写入规则见 [subagent](subagent.md)，已有数据处理见 [迁移说明](git-retirement.md)。定时任务本身仍在配置的工作目录执行。

## 定时任务 / schedule

独立 CLI 进程不会常驻，因此普通 prompt 任务可交给操作系统调度器：`-schedule add` 写入 `~/.pogo/schedule/jobs.json`，OS 触发器回调 `pogo -schedule-exec <id>`。Web 服务另有 internal 定时器；无论谁触发，普通 prompt 任务最终仍通过无界面 CLI 子进程执行。工作流任务则由 Web 的工作流执行器运行。

```bash
# 每天早上 9 点跑（cron 表达式，也接受 @daily 等宏）
pogo -schedule add --cron "0 9 * * *" --prompt "汇总昨夜的 CI 失败"

# 固定间隔 / 一次性（--at 缺省按本地时区）
pogo -schedule add --every 30m --prompt "看一下队列积压"
pogo -schedule add --at 2026-08-23T09:00 --prompt "提醒我发版"

pogo -schedule list              # ID / Schedule / Enabled / Last Run / Status / Next Run
pogo -schedule run-now morning   # 前台同步跑一次（输出实时透传），结束打印 session id 和耗时
pogo -schedule logs morning      # 最近几次 run 的记录 + 日志尾部 50 行
pogo -schedule disable morning   # 只翻标志位，OS 触发器保留（触发后立刻退出）
pogo -schedule remove morning    # 注销触发器并删任务；runs/ 和 logs/ 保留
```

`add` 的可选参数:`--dir <工作目录>`（默认当前目录)、`--name <id>`、`--max-turns N`、`--budget <花费上限>`（即 `-cost-budget`)、`--model <模型>`。add 成功会打印 id、下一次触发时间和后端名;注册失败会把任务从 jobs.json 回滚掉——留着任务却没有触发器,就是一个静默永远不跑的 schedule。

cron 表达式是手写的五字段解析(纯标准库):支持 `*`、`,` 列表、`-` 范围、`/步进`(含 `5/20` 自动扩展为 5,25,45)、三字母英文名字(`jan`/`mon`,大小写不敏感)、dow 的 0-7(7 归一为周日),以及 `@yearly/@annually/@monthly/@weekly/@daily/@midnight/@hourly` 宏。DOM/DOW 双受限时是 Vixie 的 OR 语义(`0 0 13 * 5` = 每月 13 号**或**周五)。`@every` 不是 cron 表达式,是另一种任务类型。

### 后端差异

| | launchd (macOS) | systemd (Linux) | crontab (兜底) |
|---|---|---|---|
| 判定 | darwin 默认 | `systemctl --user` 可用 | 有 `crontab` 二进制 |
| 睡眠/关机错过补跑 | 日历型有(唤醒即补) | 有(`Persistent=true`) | **无** |
| `--every` | `StartInterval`,睡眠中会错过 | `OnUnitActiveSec` | 退化为最近的分钟步进 |
| `--at` | 单次日历触发 | 绝对时间 `OnCalendar` | 无年份概念,靠执行后自动 disable 兜底 |
| 隔夜无人登录 | 无需处理 | 需要 `loginctl enable-linger`(add 时会警告) | 无需处理 |

`POGO_SCHEDULE_BACKEND=launchd|systemd|cron` 可以强制指定后端；`=noop` 不注册任何 OS 触发器，只用于开发和测试（Web API 靠它端到端跑通而不碰 launchd）；`=internal` 把触发完全交给 Web 服务自己的定时器（见下）。systemd 的 service 单元用 `StandardOutput=append:` 落日志,需要 systemd ≥ 240。

### 进程内触发（Web 模式 / 容器）

CLI 形态把闹钟交给 OS 调度器,因为终端进程是短命的;Web 服务是 pogo 唯一常驻的进程,在没有 OS 调度器的机器上——**容器里既没有 systemd 用户管理器也没有 crontab,Windows 两者皆无**——它自己就是那个"一直在跑的东西"。Web Manager 内置一个 next-deadline 定时器（`web/scheduler.go`）:每轮算出所有启用任务里最早的触发点,睡到 min(那个点, 一个对表周期),到点走与 `-schedule-exec` 完全相同的 `RunOnce`——锁、运行历史、连败自动停用、面板的 running 标记全部共享,不是第二套记账。

规则与边界：

- **只在没有 OS 后端时接管。** 探测到 launchd/systemd/cron 可用时定时器主动让位（否则每次触发都会双跑、留下一堆 skip 记录）;`POGO_SCHEDULE_BACKEND=internal` 强制由它接管。强制值写错仍然报错——打错字应该响。
- **every 任务从创建时刻起算。** OS 后端语义下"从未跑过的 every 任务"没有下次触发时间（OS 从注册时刻计数,而 store 不记录注册）,进程内接管时定时器自己就是那个计数器,锚点用 `CreatedAt`。
- **补跑有界。** 服务停机期间错过的触发最多补最近一次,不排队重放;every 任务的间隔是显式的,整段错过的间隔会留一条 skipped 记录说明"停机错过了 N 个间隔"。过了点的 at 任务补跑一次后自动解除武装。
- **时区即进程时区。** cron 表达式按本地时间算,容器默认 UTC——部署时记得设 `TZ`（malagent 镜像已设 `Asia/Shanghai`）。
- 谁在跑就归谁管:定时器活着任务才触发,和"launchd 开机才触发"是同一条语义。

### Web 界面

`pogo -web` 的界面里也能管理定时任务：侧栏底部（折叠时是最左图标列）的闹钟图标打开定时任务面板——列表内联开关启停、立即运行（异步，行内 spinner）、删除（运行历史和日志保留）、每行的下次运行/上次状态/连败标记；连败 ≥3 给橙色警告，连续失败 10 次自动停用的任务显示红色「已自动暂停」；任一任务有连败时入口图标带红点。新建表单支持每小时/每天/每周/工作日预设和自定义 cron（表达式由服务器校验并计算下次运行时间），可填工作目录、名称、max-turns 和 budget。详情展开是 run 历史（定时/手动、状态、耗时）、out.log 尾部和 session 链接（点开在 dock 里看该次运行的完整回放）。

面板与 CLI 读写同一个 `jobs.json`。没有可用 OS 后端时可选择 internal 触发，但执行还依赖平台锁和子进程支持；`schedule/lock_other.go` 在 Windows 返回不支持，当前不能宣称 Windows 端到端可运行。开发时的 noop 只避免 OS 触发器注册，不替代执行验收。对应 API 是 `GET/POST /api/schedule/jobs`、`POST .../{id}/enable|disable|run-now`、`DELETE .../{id}`、`GET .../{id}/runs`。

### 安全说明

**定时任务是无人值守的全自动 agent。** headless(`-p`)模式下终端没有审批门,所有工具调用——写文件、跑 bash——都直接执行。prompt 的边界就是任务的边界:写清楚它该做什么、不该碰什么,用 `--max-turns` 和 `--budget` 给它戴上上限,必要时配合 `-read-root` 之类限制可达范围。任务与任务之间靠 `<dir>/locks/<id>.lock` 的 flock 防重叠:上一次没跑完又来了新触发,新的记一条 `skipped` 就退出。

**API key 会被复制进 OS 单元文件。** providers.json 里的 key 是 `key_env` 间接引用,而 launchd/systemd 启动的进程没有你的 shell 环境。注册时 pogo 把当前环境里这些 key 的**值**写进 plist/service 文件(0600 权限),add 的输出里会点名复制了哪几个。这把密钥的存放面从「环境变量」扩大到了「环境变量 + 一个 0600 文件」——是刻意的,但你要知道。

### `~/.pogo/schedule/` 目录布局

```
jobs.json        任务清单(0600,pretty JSON,原子写)
runs/<id>.jsonl  每次 run 一行:时刻/触发方式/session/状态/耗时/end_reason
logs/<id>.out.log  子进程的 JSONL 事件流(launchd 的 StandardOutPath 也指这里)
logs/<id>.err.log  子进程 stderr + 执行器自己的诊断
locks/<id>.lock  防重叠的 flock
```

`POGO_SCHEDULE_DIR` 可以整体挪走。runs 文件超 2MB 会截到末尾 2000 行。任务连续失败 10 次会自动 disable(stderr 里说明原因)——一个总是失败的任务是一张账单,不是一个 schedule;`--at` 任务跑完一次也会自动 disable。


---

<a id="english"></a>

# Worktrees and Scheduled Tasks

Automatic worktree management has been retired. See [subagents](subagent.md) and [migration notes](git-retirement.md). Scheduled tasks use their configured working directory.

## Scheduled Tasks

The standalone CLI does not stay alive. An OS scheduler can call `pogo -schedule-exec <id>`, while the Web server also has an internal timer. Ordinary prompt jobs still execute through headless CLI subprocesses; workflow jobs use the in-process workflow runner.

```bash
# Every morning at 9 (cron expression; @daily and friends also accepted)
pogo -schedule add --cron "0 9 * * *" --prompt "summarize last night's CI failures"

# Fixed interval / one-shot (--at defaults to local time)
pogo -schedule add --every 30m --prompt "check the queue backlog"
pogo -schedule add --at 2026-08-23T09:00 --prompt "remind me to ship"

pogo -schedule list              # ID / Schedule / Enabled / Last Run / Status / Next Run
pogo -schedule run-now morning   # run once synchronously in the foreground (output streamed live); prints session id and duration
pogo -schedule logs morning      # recent runs + the last 50 lines of logs
pogo -schedule disable morning   # flips the flag only; the OS trigger stays (fires and exits immediately)
pogo -schedule remove morning    # unregisters the trigger and deletes the job; runs/ and logs/ are kept
```

Optional `add` flags: `--dir <workdir>` (defaults to the current directory), `--name <id>`, `--max-turns N`, `--budget <spend cap>` (same as `-cost-budget`), `--model <model>`. A successful add prints the id, the next fire time, and the backend name; a failed registration rolls the job back out of jobs.json — a job with no trigger is a schedule that silently never runs.

Cron expressions use a hand-written five-field parser (pure stdlib): `*`, `,` lists, `-` ranges, `/step` (`5/20` expands to 5,25,45), three-letter English names (`jan`/`mon`, case-insensitive), dow 0-7 (7 normalized to Sunday), and the `@yearly/@annually/@monthly/@weekly/@daily/@midnight/@hourly` macros. When both DOM and DOW are restricted, the semantics are Vixie's OR (`0 0 13 * 5` = the 13th of the month **or** a Friday). `@every` is not a cron expression; it is the other job type.

### Backend differences

| | launchd (macOS) | systemd (Linux) | crontab (fallback) |
|---|---|---|---|
| Detection | default on darwin | `systemctl --user` available | `crontab` binary present |
| Catch-up after sleep/power-off | calendar jobs yes (fires on wake) | yes (`Persistent=true`) | **no** |
| `--every` | `StartInterval`; missed while asleep | `OnUnitActiveSec` | degrades to nearest-minute stepping |
| `--at` | one-shot calendar trigger | absolute `OnCalendar` | no year concept; auto-disable after firing as the backstop |
| Overnight with nobody logged in | nothing to do | needs `loginctl enable-linger` (warned at add) | nothing to do |

`POGO_SCHEDULE_BACKEND=launchd|systemd|cron` forces a backend; `=noop` registers no OS trigger and is for development and tests only (the Web API is exercised end-to-end without touching launchd); `=internal` hands triggering entirely to the web server's own timer (see below). systemd service units log via `StandardOutput=append:`, which needs systemd >= 240.

### In-process triggering (web mode / containers)

The CLI hands the alarm to the OS scheduler because terminal processes are short-lived; the web server is the only long-lived pogo process, and on machines with no OS scheduler — **containers have neither a systemd user manager nor crontab, and Windows has neither** — it becomes the "always running thing" itself. The web Manager has a next-deadline timer (`web/scheduler.go`): each round it computes the earliest fire point among enabled jobs, sleeps until min(that point, one resync period), and on firing runs the exact same `RunOnce` as `-schedule-exec` — locks, run history, auto-disable after repeated failures, and the panel's running flag are all shared, not a second set of books.

Rules and boundaries:

- **Takes over only when there is no OS backend.** When launchd/systemd/cron is detected, the timer steps aside (otherwise every trigger would double-run and leave a pile of skip records); `POGO_SCHEDULE_BACKEND=internal` forces it to take over. A misspelled forced value still errors — typos should be loud.
- **Every-jobs count from creation.** Under an OS backend, an every-job that has never run has no next-fire time (the OS counts from registration, and the store does not record it); when the in-process timer takes over, it is itself that counter, anchored at `CreatedAt`.
- **Catch-up is bounded.** Triggers missed while the server was down back-fill at most the most recent one; they do not queue up for replay. For every-jobs the interval is explicit, and a wholly missed stretch leaves one skipped record saying "N intervals were missed during downtime". A past-due at-job fires once and disarms.
- **The timezone is the process timezone.** Cron expressions evaluate in local time; containers default to UTC — set `TZ` when deploying (the malagent image sets `Asia/Shanghai`).
- Whoever is running owns it: a job fires only while the timer lives, which is the same semantics as "launchd fires only when the machine is on".

### Web UI

Scheduled tasks are also manageable from the `pogo -web` interface: the alarm icon at the bottom of the sidebar (the leftmost icon column when collapsed) opens the schedule panel — inline toggles, run now (async, inline spinner), delete (run history and logs kept), and per-row next-run/last-status/failure-streak; a streak >= 3 gets an orange warning, and jobs auto-disabled after 10 consecutive failures show a red "auto-paused" badge; the entry icon gets a red dot while any job is failing. The creation form offers hourly/daily/weekly/weekday presets and custom cron (validated server-side, with the next run computed), plus workdir, name, max-turns and budget. The detail row expands to the run history (scheduled/manual, status, duration), the tail of out.log, and a session link (opens the full replay of that run in the dock).

The panel and CLI share `jobs.json`. Internal triggering does not guarantee execution support: the non-Unix lock implementation currently refuses scheduled jobs on Windows. A noop backend only avoids registering OS triggers, and is not end-to-end execution acceptance. The API is `GET/POST /api/schedule/jobs`, `POST .../{id}/enable|disable|run-now`, `DELETE .../{id}`, `GET .../{id}/runs`.

### Security notes

**A scheduled task is an unattended, fully-automatic agent.** In headless (`-p`) mode the terminal has no approval gate; every tool call — writing files, running bash — executes directly. The prompt is the boundary of the job: write down what it should do and what it must not touch, cap it with `--max-turns` and `--budget`, and if needed restrict its reach with `-read-root` and friends. Overlap between runs of the same job is prevented by an flock at `<dir>/locks/<id>.lock`: if the previous run has not finished when a new trigger arrives, the new one records a `skipped` and exits.

**API keys are copied into OS unit files.** Keys in providers.json are indirect `key_env` references, but processes started by launchd/systemd do not have your shell environment. At registration, pogo writes the current **values** of those keys into the plist/service file (mode 0600), and the add output names which ones were copied. This widens where the secrets live from "environment variables" to "environment variables + one 0600 file" — deliberate, but you should know.

### `~/.pogo/schedule/` layout

```
jobs.json           job list (0600, pretty JSON, atomic writes)
runs/<id>.jsonl     one line per run: time/trigger/session/status/duration/end_reason
logs/<id>.out.log   the child's JSONL event stream (launchd's StandardOutPath points here too)
logs/<id>.err.log   child stderr + the executor's own diagnostics
locks/<id>.lock     flock against overlap
```

`POGO_SCHEDULE_DIR` moves the whole directory. Runs files over 2MB are truncated to the last 2000 lines. A job that fails 10 times in a row auto-disables (the reason goes to stderr) — a perpetually failing job is a bill, not a schedule; a `--at` job also auto-disables after its single run.
