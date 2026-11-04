// Command pogo runs the Agent platform, its Web server and headless tasks.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/analyze"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/schedule"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/web"
	"github.com/yosukeno/pogo/wire"
)

// Flag descriptions are written as "English\n中文" and split by the help
// printer. Keeping both languages at the definition site is what stops them from
// drifting apart when a flag changes.

// accountUtility reports whether argv is one of the users.json helpers —
// flags that answer and exit before any configuration is consulted.
func accountUtility(argv []string) bool {
	for _, a := range argv {
		if a == "-gen-token" || a == "--gen-token" || a == "-hash-password" || a == "--hash-password" {
			return true
		}
	}
	return false
}

func main() {
	// Before the flags, because -model's help text names the default and -models
	// lists the catalog: both have to describe the configuration that is actually in
	// effect. Warnings are collected rather than printed, because stdout may be a
	// JSONL stream and this runs before we know.
	configWarnings, err := config.Load()
	// The account utilities need no configuration — and needing one would be a
	// chicken-and-egg for a first-time operator creating users before any
	// provider exists. Peek, don't parse: flag values are not read yet.
	if err != nil && !accountUtility(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "pogo:", err)
		os.Exit(2)
	}

	var (
		prompt = flag.String("p", "",
			"Run a single `prompt` and exit.\n运行单个 prompt 后退出")
		model = flag.String("model", "",
			"Model `name` or alias. Run -models to see them all. Default: "+config.DefaultModel()+
				"\n模型 id 或别名，用 -models 查看全部，默认 "+config.DefaultModel())
		listModels = flag.Bool("models", false,
			"List known models and exit.\n列出已知模型后退出")
		listSessions = flag.Bool("sessions", false,
			"List saved sessions and exit.\n列出已保存的会话后退出")
		rebuildIndex = flag.Bool("index-rebuild", false,
			"Discard and rebuild the session index (the listings/search cache) from the transcripts, then exit."+
				"\n丢弃并从会话文件重建索引（会话列表/搜索的缓存）后退出")
		cwd = flag.String("C", "",
			"Working `directory` the agent operates in. Default: the current one"+
				"\nagent 的工作目录，默认为当前目录")
		resume = flag.String("resume", "",
			"Resume a session: \"last\" or a `path` to a .jsonl file.\n恢复会话：\"last\" 或 .jsonl 文件路径")
		quiet = flag.Bool("quiet", false,
			"No effect, kept so existing scripts still run: text mode now prints only the final answer."+
				"\n已无作用，为兼容旧脚本保留：text 模式现在只打印最终回答")
		mode = flag.String("mode", modeText,
			"Output `mode`: text prints only the final answer, json writes one event per line on stdout (for programs)."+
				"\n输出模式：text 只打印最终回答，json 在 stdout 每行输出一个事件（给程序读）")
		maxTurns = flag.Int("max-turns", agent.DefaultMaxTurns,
			"Give up after this many turns (`n`) in one run.\n单次运行的轮次上限")
		softTurns = flag.Int("soft-turns", agent.DefaultSoftTurns,
			"Inject a checkpoint every this many turns (`n`) rather than running silently\n"+
				"to -max-turns: the model is told where it stands, then either finishes or\n"+
				"states what is left. 0 disables; a value >= -max-turns never fires."+
				"\n每经过该轮数插入一次检查点，而不是静默跑到 -max-turns：模型被告知当前进度，"+
				"\n然后要么收尾、要么说明还差什么。0 关闭；大于等于 -max-turns 时不会触发")
		maxRuns = flag.Int("max-runs", 1,
			"Let one task span at most this many runs (`n`), -p only: when a run ends on a\n"+
				"continue-disposition limit (the turn cap or a budget), the session forks and a\n"+
				"fresh run picks up from .pogo/handoff.md. The default 1 is a single run."+
				"\n让一个任务最多跨 n 次运行（仅 -p）：撞 continue 类上限（轮次或预算）就 fork\n"+
				"\n会话、由新 run 从 .pogo/handoff.md 接着做。默认 1 即单次运行")
		evaluate = flag.Bool("evaluate", false,
			"Check the result with a fresh read-only evaluator before accepting it (-p only):\n"+
				"a claimed completion that fails the check chains another run with the findings\n"+
				"(within -max-runs); a chain that ran out of runs still exits 0 when the\n"+
				"evaluator confirms the work landed."+
				"\n接受结果前先过一个全新的只读 evaluator（仅 -p）：声称完工但没通过核验的，"+
				"\n带着发现接力下一棒（在 -max-runs 内）；run 数用尽但活核验属实的，仍算成功")
		retries = flag.Int("retries", llm.DefaultMaxRetries,
			"Retries (`n`) per LLM call on 429/5xx. -1 disables them."+
				"\n每次 LLM 调用在 429/5xx 时的重试次数，-1 关闭")
		stagnationThreshold = flag.Int("stagnation-threshold", 3,
			"Stop after this many identical tool results (`n`). 0 to disable."+
				"\n连续相同工具结果达到此次数后停止，0 禁用")
		tokenBudget = flag.Int64("token-budget", 0,
			"Maximum total tokens (`n`) for this run. 0 means unlimited."+
				"\n本次运行的最大总 token 数，0 表示无限制")
		costBudget = flag.Float64("cost-budget", 0.0,
			"Maximum estimated cost (`f`) for this run, in the same unit as the model's"+
				"\ndeclared price. 0 means unlimited. Needs a price in providers.json;"+
				"\nrefuses to start without one rather than running uncapped."+
				"\n本次运行的最大预估成本，单位与模型声明的价格一致，0 表示无限制。"+
				"\n需要 providers.json 里声明价格；没有价格时拒绝启动而不是不设上限")
		timeBudget = flag.Duration("time-budget", 0,
			"Maximum wall-clock time (`dur`) for this run. 0 means unlimited."+
				"\n本次运行的最大墙钟时间，0 表示无限制")
		contextEdit = flag.String("context-edit", "auto",
			"Drop old tool results from the prompt once it passes this size: `auto` "+
				"(four fifths of the model's window), off, or a token count."+
				"\n超过该大小后丢弃旧的工具输出：auto（模型窗口的五分之四）、off 关闭、或给 token 数")
		compact = flag.String("compact", "off",
			"Arm automatic compaction between runs: `off` (manual /compact only, the default), "+
				"auto, auto:N% or auto:N (a token count). See docs/auto-compact.md."+
				"\n在运行之间启用自动压缩：off（仅手动 /compact，默认）、auto、auto:N% 或 auto:N（token 数）。"+
				"见 docs/auto-compact.md")
		serve = flag.Bool("web", false,
			"Serve the browser UI, the interactive front end."+
				"\n启动浏览器界面（交互使用的入口）")
		listen = flag.String("listen", "127.0.0.1:7777",
			"`address` for -web. Loopback by default; a token is always required."+
				"\n-web 的监听地址，默认只绑本机；无论如何都需要 token")
		webDev = flag.String("web-dev", "",
			"Proxy non-API routes to a vite dev server at this `url`."+
				"\n把非 API 路由反代到 vite 开发服务器")
		gateTimeout = flag.Duration("gate-timeout", web.DefaultGateTimeout,
			"How long (`dur`) a tool call waits for approval before being refused."+
				"\n工具调用等待人工审批的时长，超时按拒绝处理")
		noCitations = flag.Bool("no-citations", false,
			"Do not stamp tool results with source labels, and do not ask the model to cite them."+
				"\n不给工具结果盖来源编号，也不要求模型在回答里标注出处")
		followups = flag.Bool("followups", true,
			"After a run ends, generate follow-up suggestion chips with a model call (-web only):\n"+
				"off the answer's critical path; off leaves rule-based matching as the only source."+
				"\n回合结束后由模型生成追问建议 chips（仅 -web）：异步进行，不阻塞回答；"+
				"\n关闭后只剩规则匹配的兜底来源")
		pruneCheckpoints = flag.Bool("checkpoints-prune", false,
			"Discard rewind points beyond the retention policy for this workspace, then exit."+
				"\n清理本工作区超出保留策略的撤回点后退出")
		pruneSpill = flag.Bool("spill-prune", false,
			"Discard spilled tool output older than the retention policy, then exit."+
				"\n按保留策略清理过期的大输出落盘文件后退出")
		pruneSpillDays = flag.Int("spill-prune-days", 0,
			"Override the retention for -spill-prune (`n` days) without saving it."+
				"\n临时指定 -spill-prune 的保留天数，不写回设置")
		listSkills = flag.Bool("skills", false,
			"List discovered skills and exit.\n列出已发现的 skills 后退出")
		noSkills = flag.Bool("no-skills", false,
			"Skip the default skill locations. -skill paths still load."+
				"\n不扫描默认 skill 目录；-skill 指定的仍然加载")
		projectSkills = flag.Bool("project-skills", false,
			"Also load skills from ./.pogo/skills. Off by default: a skill rewrites the system prompt."+
				"\n同时加载 ./.pogo/skills；默认关闭，因为 skill 会改写 system prompt")
		listMemory = flag.Bool("memory", false,
			"List the agent's memory notes and exit.\n列出 agent 的记忆笔记后退出")
		noMemory = flag.Bool("no-memory", false,
			"Do not give the agent a memory directory."+
				"\n不给 agent 记忆目录")
		projectMemory = flag.Bool("project-memory", false,
			"Also use ./.pogo/memory. Off by default: notes arrive with a checkout and "+
				"speak to the model as its own earlier conclusions."+
				"\n同时使用 ./.pogo/memory；默认关闭，因为笔记会随 checkout 到来，"+
				"而且是以「你自己之前的结论」这个身份说话")
		analyzeSession = flag.String("analyze-session", "",
			"Analyze a session JSONL file and output statistics. A directory, or the\n"+
				"word `sessions` for your own history, reports the turn-count distribution\n"+
				"across every run in it instead — the number a -max-turns cap comes from."+
				"\n分析会话 JSONL 文件并输出统计信息。传目录或 sessions 则统计其中所有 run 的"+
				"\n轮次分布——这是选 -max-turns 上限该依据的数据")
		analyzeFormat = flag.String("analyze-format", "text",
			"Output format for session analysis: text or json. Default: text."+
				"\n会话分析的输出格式：text 或 json，默认 text")
		analyzeOutput = flag.String("analyze-output", "",
			"Write analysis output to the specified file instead of stdout."+
				"\n将分析输出写入指定文件而非标准输出")
		manageSchedule = flag.Bool("schedule", false,
			"Manage scheduled jobs: -schedule <list|add|remove|enable|disable|run-now|logs> [args]."+
				"\n管理定时任务：-schedule <list|add|remove|enable|disable|run-now|logs> [参数]")
		// scheduleExec is how the OS scheduler calls back in. It is hidden from
		// the help (see hiddenFlags): users manage jobs through -schedule, and
		// nobody should be running this by hand — run-now exists for that.
		scheduleExec = flag.String("schedule-exec", "",
			"Execute the scheduled job with this `id` and exit (invoked by the OS scheduler)."+
				"\n执行指定 id 的定时任务后退出（由系统调度器调用）")
	)
	// -skill is repeatable, so it needs a Var rather than a String.
	var skillPaths repeatedFlag
	flag.Var(&skillPaths, "skill",
		"Load a skill from this `path` (file or directory). Repeatable."+
			"\n从指定路径加载 skill（文件或目录），可重复")
	// -read-root is repeatable too, and is folded into POGO_READ_ROOTS after
	// parsing so subagent children — which inherit the environment, not the
	// flags — see the same roots. See mergeReadRoots.
	var readRootPaths repeatedFlag
	flag.Var(&readRootPaths, "read-root",
		"Extra `directory` the read-only tools (read/ls/find/grep) may reach, "+
			"outside the working directory — tool installs, data mounts. Repeatable; "+
			"also composed with POGO_READ_ROOTS, which is how subagent children inherit."+
			"\n只读工具（read/ls/find/grep）额外可访问的目录，在工作目录之外——"+
			"工具安装、数据挂载等。可重复；与 POGO_READ_ROOTS 合并生效，"+
			"子代理正是通过该变量继承")
	// -web-panel is repeatable too: name=url per panel.
	var panelFlags repeatedFlag
	flag.Var(&panelFlags, "web-panel",
		"Show an external web app as a dock sheet: `name=url`. Repeatable. The app is reverse-proxied at /panels/name/."+
			"\n把一个外部 web 应用挂进 dock 作为 sheet 显示：name=url，可重复；应用被反代到 /panels/name/")
	hashPassword := flag.Bool("hash-password", false,
		"Hash a password from stdin into a users.json line, then exit."+
			"\n从 stdin 读一个密码，输出可直接粘进 users.json 的账号行后退出")
	genToken := flag.Bool("gen-token", false,
		"Print a random fixed access token for a users.json account, then exit."+
			"\n生成一个固定访问令牌（粘进 users.json 账号行的 \"token\" 字段）后退出")
	flag.Usage = usage
	flag.Parse()

	// -hash-password: the users.json helper. Read from stdin so the password
	// never lands in shell history or process listings.
	if *hashPassword {
		pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && pw == "" {
			fmt.Fprintln(os.Stderr, "pogo: read password:", err)
			os.Exit(1)
		}
		pw = strings.TrimRight(pw, "\r\n")
		if pw == "" {
			fmt.Fprintln(os.Stderr, "pogo: empty password")
			os.Exit(1)
		}
		hash, err := web.HashPassword(pw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pogo:", err)
			os.Exit(1)
		}
		fmt.Printf(`{"username":"<名字>","hash":%q}`+"\n", hash)
		return
	}

	// -gen-token: the sibling helper for the fixed-token channel. The value
	// carries no expiry by design; binding it is pasting it into users.json.
	if *genToken {
		tok, err := web.GenToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "pogo:", err)
			os.Exit(1)
		}
		fmt.Printf(`{"username":"<名字>","hash":"…","token":%q}`+"\n", tok)
		return
	}

	// Reported after parsing so that -C is known, and on diagOut so that a JSONL
	// stream on stdout stays clean.
	if w := config.WarnIfInWorkingDir(*cwd); w != "" {
		configWarnings = append(configWarnings, w)
	}
	for _, w := range configWarnings {
		fmt.Fprintf(diagOut, "config: %s\n", w)
	}

	if *listModels {
		// Marks the one that would be used, which is the default unless -model says
		// otherwise. Worth doing now that a config file can move the default: a list
		// with nothing marked leaves the reader to guess which line is in effect.
		if *model != "" {
			printModels(*model)
		} else {
			printModels(config.DefaultModel())
		}
		return
	}
	if *listSessions {
		if err := printSessions(); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *rebuildIndex {
		dir := session.DefaultDir()
		n, err := session.RebuildIndex(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("reindexed %d session(s) into %s\n",
			n, filepath.Join(dir, session.IndexName))
		return
	}
	skillOpts := skills.Options{
		Paths:          skillPaths,
		Disabled:       *noSkills,
		IncludeProject: *projectSkills,
	}
	if *listSkills {
		if err := printSkills(skillOpts, *cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	memOpts := memory.Options{
		Cwd:     *cwd,
		User:    !*noMemory,
		Project: *projectMemory && !*noMemory,
	}
	if *listMemory {
		if err := printMemory(memOpts); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *pruneCheckpoints {
		if err := checkpointCommand(os.Stdout, *cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *pruneSpill {
		if err := spillPruneCommand(os.Stdout, *pruneSpillDays); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// -schedule-exec is the OS scheduler's call into us: launchd/systemd/cron
	// fire it, it runs the job's prompt headless and records the outcome. It
	// exits rather than returns because RunScheduled's answer *is* an exit
	// code (the job's), which the scheduler logs as the trigger's result.
	if *scheduleExec != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		os.Exit(schedule.RunScheduled(ctx, schedule.DefaultDir(), *scheduleExec, schedule.TriggerScheduled))
	}
	if *manageSchedule {
		if err := scheduleCommand(os.Stdout, flag.Args()); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *maxRuns < 1 {
		fmt.Fprintf(os.Stderr, "pogo: -max-runs must be at least 1 (got %d)\n", *maxRuns)
		os.Exit(1)
	}
	// Reject invalid concurrency and retired isolation settings before either
	// entrypoint starts. Existing worktree deployments must migrate explicitly.
	if err := checkSubagentEnv(os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
		os.Exit(2)
	}
	// Before either the terminal or the server path builds anything, and before
	// any child could be spawned: children see the merged list through the
	// environment this call writes.
	mergeReadRoots(readRootPaths)
	if *analyzeSession != "" {
		if err := analyzeSessionFile(*analyzeSession, *analyzeFormat, *analyzeOutput); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *serve {
		modelName := *model
		if modelName == "" {
			modelName = config.DefaultModel()
		}
		if err := serveWeb(webOptions{
			listen: *listen, dev: *webDev, model: modelName, cwd: *cwd,
			maxTurns: *maxTurns, retries: *retries, gateTimeout: *gateTimeout,
			stagnationThreshold: *stagnationThreshold, tokenBudget: *tokenBudget,
			costBudget: *costBudget, timeBudget: *timeBudget,
			contextEdit: *contextEdit,
			compact:     *compact,
			citations:   !*noCitations,
			followups:   *followups,
			skills:      skillOpts,
			memory:      memOpts,
			panels:      panelFlags,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
			os.Exit(1)
		}
		return
	}
	opts := options{
		prompt: *prompt, model: *model, cwd: *cwd, mode: *mode,
		maxTurns: *maxTurns, quiet: *quiet, resume: *resume, retries: *retries,
		softTurns:           *softTurns,
		maxRuns:             *maxRuns,
		evaluate:            *evaluate,
		stagnationThreshold: *stagnationThreshold, tokenBudget: *tokenBudget,
		costBudget: *costBudget, timeBudget: *timeBudget,
		contextEdit: *contextEdit,
		compact:     *compact,
		citations:   !*noCitations,
		skills:      skillOpts,
		memory:      memOpts,
	}
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "pogo: %v\n", err)
		os.Exit(1)
	}
}

// repeatedFlag collects a flag given more than once.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// mergeReadRoots folds -read-root entries into POGO_READ_ROOTS and returns the
// union.
//
// The environment variable is the storage rather than a struct field because it
// is the one channel that reaches every process in a tree: a subagent child is
// a fresh pogo invocation that inherits the environment, not the parent's
// flag set. Flag and env compose rather than override, so an operator can bake
// a deployment's roots into the environment (malagent does) and still widen
// them once from a command line.
func mergeReadRoots(flagRoots []string) []string {
	env := readRootsEnv()
	seen := make(map[string]bool, len(env)+len(flagRoots))
	var out []string
	for _, r := range append(append([]string{}, flagRoots...), env...) {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) > 0 {
		os.Setenv(tools.EnvReadRoots, strings.Join(out, string(os.PathListSeparator)))
	}
	return out
}

// readRootsEnv is the merged list every consumer reads — the registry builder
// here and the web manager in serveWeb — so there is exactly one answer to
// "which extra roots are in effect".
func readRootsEnv() []string {
	var out []string
	for _, r := range filepath.SplitList(os.Getenv(tools.EnvReadRoots)) {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

type options struct {
	prompt, model, cwd, resume string
	// mode is modeText or modeJSON. An enum rather than a -json boolean: text,
	// json and a future protocol front end are three points on one axis, and
	// boolean flags for them would be mutually exclusive in a way the flag package
	// cannot express.
	mode                string
	maxTurns, retries   int
	softTurns           int
	maxRuns             int
	stagnationThreshold int
	tokenBudget         int64
	costBudget          float64
	timeBudget          time.Duration
	// contextEdit is the raw -context-edit spec. It is resolved against the model's
	// window in run(), which is the first place both are known.
	contextEdit string
	// compact is the raw -compact spec, resolved in the same place for the same
	// reason: "auto" is a function of the model's window.
	compact  string
	quiet    bool
	evaluate bool
	// citations is the positive form of -no-citations: whether tool results are
	// stamped with source labels and the model is asked to cite them.
	citations bool
	skills    skills.Options
	memory    memory.Options
}

// The output modes. text is the default and behaves exactly as before.
const (
	modeText = "text"
	modeJSON = "json"
)

// diagOut is where everything a human would read goes: load warnings, drift
// notices, retry announcements, and the resumed banner.
//
// A variable rather than os.Stderr at each call site for two reasons. It is the
// seam the JSON mode test uses to prove diagnostics left stdout, and it is the
// single place to look when answering "can this write corrupt a piped stream?".
var diagOut io.Writer = os.Stderr

// errNoPrompt is what a bare `pogo` gets. There is no interactive terminal
// mode any more: the browser UI is the interactive front end and -p the
// headless one, so the answer to "no prompt" is how to start either.
var errNoPrompt = errors.New("no prompt given, and there is no interactive terminal mode. Use one of:\n" +
	"未提供 prompt，且不再有终端交互模式。可用：\n" +
	"  pogo -web               serve the browser UI / 启动浏览器界面\n" +
	"  pogo -p \"<prompt>\"      run one prompt and exit / 运行单个 prompt 后退出\n" +
	"  <stdin> | pogo          piped input becomes the prompt / 管道内容作为 prompt\n" +
	"  pogo -h                 all flags / 查看全部参数")

func run(o options) error {
	switch o.mode {
	case modeText, modeJSON:
	default:
		return fmt.Errorf("unknown -mode %q: want %s or %s", o.mode, modeText, modeJSON)
	}
	if o.mode == modeJSON && o.quiet {
		// Answered rather than silently obeyed. Filtering events would hand the
		// consumer a stream with holes in it, which is worse than a stream it
		// has to filter itself.
		fmt.Fprintf(diagOut, "pogo: -quiet has no effect with -mode json; "+
			"the event stream is always complete. Filter it downstream.\n")
	}

	cwd := o.cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	// Read stdin before anything else: piped content is the prompt, or the
	// document the -p instruction is about.
	piped, err := readPipedStdin()
	if err != nil {
		return err
	}
	prompt := buildPrompt(piped, o.prompt)

	// Refused before anything is resolved, because it is a usage error. Failing
	// here rather than after loading a session and a model keeps the message
	// about how to run pogo instead of about a missing API key, and creates no
	// empty session file.
	if prompt == "" {
		return errNoPrompt
	}

	// A resumed session is opened before the model is chosen, because the model it
	// was running is the right default for continuing it. Resolving the client
	// first would silently switch a resumed conversation to the default model.
	var store *session.Store
	if o.resume != "" {
		if store, err = openExisting(o.resume); err != nil {
			return err
		}
		reportSessionDiagnostics(store)
		if o.model == "" {
			if m := store.Meta(); m != nil && m.Model != "" {
				o.model = m.Model
			}
		}
	}

	client, cfg, err := newClient(o.model, o.retries)
	if err != nil {
		return err
	}

	// Resolved here because "auto" means half this model's window, and the model is
	// only settled after -resume has had its say about which one to use.
	editCfg, err := agent.ParseContextEdit(o.contextEdit, cfg.ContextWindow)
	if err != nil {
		return err
	}

	// Same place, same reason: the compaction threshold is a function of the
	// window of the model -resume settled on.
	compactCfg, err := agent.ParseCompactSpec(o.compact, cfg.ContextWindow)
	if err != nil {
		return err
	}

	// Checked in the same place and for the same reason: the answer depends on which
	// model -resume settled on.
	if err := checkCostBudget(o.costBudget, cfg); err != nil {
		return err
	}

	// Skills are resolved once, here, and never re-scanned. A system prompt that
	// changed mid-session would invalidate the cached prefix every turn and make
	// -resume unreproducible.
	o.skills.Cwd = cwd
	skillList, skillDiags := skills.Load(o.skills)
	reportSkillDiagnostics(skillDiags)

	// Manifest tools resolve with the skills they travel with, for the same
	// reason skills resolve once and never re-scan: the tool set feeds the
	// schema declarations, and a set that changed mid-session would invalidate
	// the cached prefix every turn.
	synthTools, _, synthDiags := tools.LoadSynthTools(synthSources(skillList))
	reportSynthDiagnostics(synthDiags)

	// Observation is additive and opened once per process, after the skill
	// list is settled (identities come from the loaded skills, never from
	// prompts or argv) and before the registry is built (the registry gets
	// the read-target projection). A failure prints a bounded diagnostic on
	// the diagnostics stream and the CLI runs on without it; stdout — the
	// JSON event stream in -mode json — never carries observer output.
	obs := openCLIObservability(skillList)
	if obs != nil {
		defer obs.Close()
	}

	// External MCP servers (mcp.json) connect here, once, for the same reason
	// skills resolve once: the tool set feeds the schema declarations, and a
	// set that changed mid-session would invalidate the cached prefix every
	// turn. A missing or corrupt file degrades to a warning — the MCP client
	// is additive and must not take the CLI down. A *required* server that
	// will not connect is the opposite call: the operator said the workflow
	// cannot run without it, so WaitReady's error is fatal (fail-fast, like
	// the model resolution above).
	//
	// A subagent child gets none of this: an MCP connection is a stateful
	// session owned by this process's pool, and a child is a separate process
	// that would have to re-connect, re-filter and re-pay the startup wait for
	// a ten-minute run — v1 does not inherit.
	var mcpTools []tools.Tool
	if subagentDepth() == 0 {
		mcpCfg, err := mcpclient.Load(mcpclient.Path())
		if err != nil {
			fmt.Fprintf(diagOut, "mcp client: %v\n", err)
		}
		enabled := false
		for _, sc := range mcpCfg.Servers {
			if sc.IsEnabled() {
				enabled = true
				break
			}
		}
		if enabled {
			pool := mcpclient.NewPool(nil)
			defer pool.Close()
			pool.Reconcile(context.Background(), mcpCfg)
			if err := pool.WaitReady(context.Background()); err != nil {
				return err
			}
			mcpTools, _, _ = pool.Snapshot()
		}
	}

	// Memory is resolved here for the same reason skills is: it contributes to the
	// system prompt, and a system prompt that changed mid-session would invalidate the
	// cached prefix every turn. The listing is a snapshot from now, so notes the model
	// writes during this run do not appear in it until the next session — which is the
	// correct behaviour, not a limitation: the model knows what it just wrote.
	o.memory.Cwd = cwd
	mem, memDiags := memory.Load(o.memory)
	for _, d := range memDiags {
		fmt.Fprintln(diagOut, d)
	}

	// The system prompt's sections are fixed for the whole process, chain
	// included: a chained run is a fresh agent from the same config, and the
	// section is what tells run 1 — before any limit is near — that its context
	// will not carry over.
	sections := []string{skills.PromptSection(skillList), mem.PromptSection()}
	if o.maxRuns > 1 {
		sections = append(sections, chainSection)
	}
	// Last, so the citation contract sits closest to the conversation: it is
	// the one section that constrains how the final answer is written.
	if o.citations {
		sections = append(sections, agent.CitationSection())
	}
	// Skill roots join POGO_READ_ROOTS here — not just for this process's own
	// read tools, but because the environment is the inheritance channel: a
	// subagent child rebuilds its roots from this variable, and its skills tell
	// it to read files under the skill directory. mergeReadRoots unions with
	// whatever flags and env already contributed, so nothing is lost.
	mergeReadRoots(skills.Roots(skillList))
	toolOpts := toolOptions(cwd, cfg.Model,
		append(skills.Roots(skillList), readRootsEnv()...),
		mem.Roots(), synthTools, mcpTools, o.skills.Paths)
	// This CLI/FD path has no image resolver or analyze_image tool, even when
	// the selected model accepts images. It can operate the parent's DOM.
	toolOpts.BrowserImageMode = tools.BrowserImageNone
	if obs != nil {
		// The read tool's trusted SKILL.md→identity map: the only skill
		// attribution channel, so a plain file read, bash, or an external
		// MCP tool can never be attributed to a recently read skill.
		toolOpts.SkillReads = obs.readTargets()
	}
	// Retained children are scoped to the parent's stable transcript identity.
	// Allocate the transcript before declaring tools so the initial schema can
	// expose continuation; restoring messages and usage still happens below.
	resumedSession := store != nil
	if store == nil {
		if store, err = session.Create(transcriptDir(), cwd, cfg.Model, skills.Names(skillList)...); err != nil {
			return err
		}
	}
	toolOpts.ResultScope = "cli:" + store.Path()
	if resumedSession {
		toolOpts.LegacyResultPaths = tools.LegacyResultPaths(store.Messages(""))
	}
	if toolOpts.Subagent != nil {
		parentID := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
		toolOpts.Subagent.Store, err = tools.NewSubagentStore(filepath.Dir(store.Path()), parentID, "cli")
		if err != nil {
			return err
		}
	}
	agentCfg := agent.Config{
		Client:           client,
		Registry:         tools.New(toolOpts),
		SystemPrompt:     agent.SystemPrompt(cwd, sections...),
		MaxTurns:         o.maxTurns,
		ToolOutputBudget: agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens},
		SoftTurns:        o.softTurns,
		// Nil unless this process is a subagent whose parent handed it an approval
		// channel. The terminal keeps no gate, which is what keeps -p scriptable.
		Gate:                newChildGate(),
		StagnationThreshold: o.stagnationThreshold,
		TokenBudget:         o.tokenBudget,
		CostBudget:          o.costBudget,
		Price:               cfg.Price,
		TimeBudget:          o.timeBudget,
		ContextEdit:         editCfg,
		Compact:             compactCfg,
		Citations:           o.citations,
		Cwd:                 cwd,
	}
	a := agent.New(agentCfg)

	// recorded tracks how much of the agent's running totals is already on disk;
	// a resumed session starts from the totals recovered below.
	var recorded session.Recorded
	if resumedSession {
		reportSessionDrift(store, cwd, skills.Names(skillList))
		prior := store.Messages("")
		a.SetMessages(prior)
		// The cost counters resume with the conversation: without this a
		// continued session reports only what it spent since the restart.
		usage, delegated := store.UsageTotals()
		a.SetUsage(usage, delegated)
		recorded = session.Recorded{Usage: usage, Delegated: delegated}
		reportResumed(store.Path(), len(prior))
	}
	if obs != nil {
		// Match Web's canonical session ID; a local transcript path must not
		// enter monitoring metadata or an external trace collector.
		obs.sessionID = strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	}
	// persisted tracks how much of the agent's history is already on disk, so
	// each turn appends only what is new.
	persisted := len(a.Messages())
	// recordCost writes the tokens spent since the last such record. A delta rather
	// than the running total, because the analyzer sums these records — writing the
	// total each time would make a five-turn session look like fifteen turns' worth.
	//
	// Shared by flush and compact so the two can never disagree about the baseline:
	// they both move `recorded`, and a second copy of this block would eventually
	// double-count or skip a call.
	recordCost := func(msgs []llm.Message) error {
		nextRecorded := recorded
		st, ok := session.UsageDelta(&nextRecorded, a.Usage(), a.Delegated())
		if !ok {
			return nil
		}
		// Attached after the fact rather than passed into UsageDelta, because it is
		// not an accumulator: it is a snapshot of the history as it now stands, so
		// there is no previous value it has to stay in step with. Safe here for the
		// same reason Messages() is — no run is in flight.
		comp := session.Compose(msgs, a.OverheadTokens())
		comp.Measured = a.LastInput()
		comp.Cleared = a.ClearedFromPrompt()
		st.Composition = &comp
		if err := store.AppendStats(st); err != nil {
			return err
		}
		recorded = nextRecorded
		return nil
	}
	flush := func() error {
		msgs := a.Messages()
		if err := store.AppendAll(msgs[persisted:]); err != nil {
			return err
		}
		persisted = len(msgs)
		return recordCost(msgs)
	}
	// compact summarises the conversation and writes the result as a new branch.
	//
	// The fork is the whole reason this is not just "replace the messages". The
	// transcript is append-only and the original conversation stays in the file;
	// Fork("") abandons it as an unreachable branch and starts a fresh chain, which
	// is exactly what the tree was built for and what rewind already does. A -resume
	// therefore replays the compacted history, while the full original is still there
	// to read.
	//
	// persisted has to move with it. Without that, the next flush would slice the
	// agent's much shorter history at the old offset — out of range, and a panic in
	// the place least able to afford one.
	compact := func(ctx context.Context, keep int64) error {
		if err := flush(); err != nil {
			return err
		}
		res, err := a.CompactWithCommit(ctx, keep, func(msgs []llm.Message) error {
			return store.ForkWithMessages("", msgs)
		})
		if err != nil {
			return errors.Join(err, recordCost(a.Messages()))
		}
		msgs := a.Messages()
		persisted = len(msgs)
		// Recorded now rather than at the next flush: the summarising call is spend
		// that already happened, and a session compacted and then closed would
		// otherwise lose it.
		if err := recordCost(msgs); err != nil {
			return err
		}
		fmt.Printf("compacted: %d messages → %d (kept %d verbatim), about %d → %d tokens (freed ~%d); the summary cost %d in / %d out.\n"+
			"the full conversation is still in %s as an abandoned branch.\n",
			res.Before, res.After, res.Kept, res.BeforeTokens, res.AfterTokens, res.Freed(),
			res.Usage.Input, res.Usage.Output, store.Path())
		return nil
	}

	// compactCircuit is how many consecutive failed auto-compactions disable the
	// automatic paths for the rest of the process. Claude Code ships the same
	// number for the same reason: its motivating data was 1,279 sessions stuck
	// compacting 50+ times in a row, wasting roughly a quarter million API calls
	// a day. Manual /compact is not behind the breaker — a person asking twice is
	// a decision, a loop asking twice is a bug.
	const compactCircuit = 3
	compactFails := 0

	// compactAuto is the automatic paths' wrapper around compact: same body, plus
	// the breaker and its diagnostics. Both callers continue running on failure —
	// a compaction that cannot happen is a notice, not a reason to stop — so the
	// failure prints here rather than travelling up as an error nobody handles.
	compactAuto := func(ctx context.Context) bool {
		if compactFails >= compactCircuit {
			return false
		}
		// The automatic shape keeps a verbatim tail (CompactKeepRecent); the
		// manual /compact below stays the full summary. Two shapes on purpose:
		// a person compacting chose the trade, a threshold did not, and the
		// evidence for what summarisation loses is about the boundary between
		// summarised and verbatim — the automatic path errs on keeping more.
		if err := compact(ctx, agent.CompactKeepRecent); err != nil {
			compactFails++
			fmt.Fprintf(diagOut, "pogo: auto-compaction failed (%v)\n", err)
			if compactFails == compactCircuit {
				fmt.Fprintf(diagOut, "pogo: auto-compaction disabled after %d consecutive failures; /compact still works\n",
					compactCircuit)
			}
			return false
		}
		compactFails = 0
		return true
	}

	// compactIfDue is the between-runs threshold check, pi's "before each new
	// user prompt". Harmless on a fresh chain run: the agent there has one or two
	// messages and ShouldCompact's own floor declines.
	compactIfDue := func(ctx context.Context) {
		if compactCfg.Threshold > 0 && a.ShouldCompact() {
			compactAuto(ctx)
		}
	}

	// One Ctrl-C cancels the in-flight turn; the loop unwinds through the same
	// context that reaches every HTTP request and child process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The front end is chosen once, here, and the rest of run() only knows it can
	// consume events. Everything below this line is identical in both modes, which
	// is the point of the interface.
	var out consumer = newTextEmitter(os.Stdout)
	if o.mode == modeJSON {
		j := newJSONEmitter(os.Stdout)
		// The header goes out before any event so a consumer knows which transcript
		// it is reading — including a subagent's parent, which needs the path to
		// hand back.
		h := wire.Header{Session: store.Path(), Cwd: cwd, Model: cfg.Model, Skills: skills.Names(skillList)}
		if err := j.header(h); err != nil {
			return err
		}
		out = j
	}

	// consume runs one prompt end to end, persisting as it goes: the prompt
	// first (the loop does not announce it), then each turn as its events pass,
	// so a kill mid-run loses only the in-flight turn rather than the run. The
	// flush() that follows is no longer the writer but the reconciliation —
	// whatever the persister failed on still lands there, because persisted
	// counts only appends that succeeded.
	var lastEnd agent.EndReason
	// pendingTrace carries an explicit /skill load's trace into the run its
	// expansion feeds, so the instruction read and that run share one trace.
	// Consumed by the next run; a failed expansion never sets it.
	pendingTrace := ""
	consume := func(prompt string) error {
		if err := flush(); err != nil {
			return fmt.Errorf("failed to persist previous run: %w", err)
		}
		lastEnd = ""
		runCtx := ctx
		if obs != nil {
			// Each actual run gets its own local run ID and trace; discovery
			// is recorded per run, in the run's context, like the web runner.
			rctx, _ := obs.runContext(ctx, pendingTrace)
			pendingTrace = ""
			obs.recordDiscovery(rctx)
			runCtx = rctx
		}
		p := agent.NewTurnPersister(func(ms []llm.Message) error {
			if err := store.AppendAll(ms); err != nil {
				return err
			}
			persisted += len(ms)
			return nil
		}, func(err error) {
			fmt.Fprintf(diagOut, "pogo: incremental persist failed (%v); the run is still saved when it ends\n", err)
		})
		p.Add(llm.UserText(prompt))
		return out.Consume(tapEvents(a.Run(runCtx, prompt), func(e agent.Event) {
			p.OnEvent(e)
			if e.Kind == agent.EventAgentEnd {
				lastEnd = e.EndReason
			}
		}))
	}

	// consumeOverflowed reads the run that just finished: it died on the context
	// overflow that even forced clearing could not save, and the automatic path
	// is armed to do something about it (threshold configured, breaker intact).
	// What to do about it stays with the callers, because the retry has to know
	// which prompt to replay.
	consumeOverflowed := func() bool {
		return overflowRetryable(lastEnd, compactCfg.Threshold > 0, compactFails, compactCircuit)
	}

	// From here on the run is one-shot: the prompt check above guarantees there
	// is a prompt, from -p or a pipe.
	//
	// The same reasoning as the /skill: expansion below, applied to commands: a
	// flag that silently sent the literal text "/compact" to the model would be a
	// trap exactly where people script against it, and the model would answer it.
	//
	// /compact is the only command that does anything here: it operates on the
	// session, which -resume has just handed us, and `-resume <path> -p /compact`
	// is a real thing to want. The other words were commands of the removed
	// interactive terminal mode; they are still refused rather than sent to the
	// model, and the ones that matter have a flag (-model, -models, -skills, -h).
	if cmd := commandWord(prompt); cmd != "" {
		if cmd != "/compact" {
			return fmt.Errorf("%s was a command of the removed interactive mode and does nothing in -p; "+
				"see -h for the flag that covers it", cmd)
		}
		return compact(ctx, 0)
	}
	// -p expands /skill: too. The web UI does, and a flag that silently sent the
	// literal text "/skill:x" to the model would be a trap in exactly the place
	// people script against.
	if name, extra, ok := skills.ParseCommand(prompt); ok {
		s, found := skills.Find(skillList, name)
		if !found {
			return fmt.Errorf("no skill named %q; run -skills to list them", name)
		}
		if obs != nil {
			var trace string
			if prompt, trace, err = obs.loadSkillExplicit(s, extra); err != nil {
				return err
			}
			pendingTrace = trace
		} else if prompt, err = skills.Invocation(s, extra); err != nil {
			return err
		}
	}
	// A task may span several runs (-max-runs). When a run ends because an
	// allowance ran out, the session forks and a fresh run picks up from the
	// handoff file — reset, not compaction: nothing is summarised away, the
	// old transcript stays in the file as an abandoned branch. The
	// disposition table decides what chains: a turn cap or budget is an
	// allowance, stagnation or an overflow is a failure that repeating would
	// reproduce.
	original := prompt
	var next string
	for runN := 1; ; runN++ {
		if runN > 1 {
			if err := store.Fork(""); err != nil {
				return err
			}
			// A fresh agent on the new branch: empty history, per-run budgets
			// refilled, and the same config — the chain section of the system
			// prompt is what it shares with its predecessor.
			a = agent.New(agentCfg)
			persisted = 0
			recorded = session.Recorded{}
			prompt = next
		}
		compactIfDue(ctx)
		runErr := consume(prompt)
		// An overflow that forced clearing could not save is the one failure
		// worth one automatic retry. Compaction happens here, between runs,
		// because the persistence reconciliation inside compact() needs no run
		// in flight; the retry resends the same prompt verbatim so the
		// summary's paraphrase of it is not the model's only anchor (opencode
		// replays the last user message for exactly this reason). Once per
		// prompt: a second overflow is reported, not looped on — the
		// disposition table's judgement of a repeated overflow stands.
		if consumeOverflowed() && compactAuto(ctx) {
			fmt.Fprintf(diagOut, "pogo: context overflow; compacted, retrying the same prompt\n")
			runErr = consume(prompt)
		}
		if err := flush(); err != nil {
			return err
		}

		// The evaluator speaks where the driver otherwise has only the run's
		// own word: a claimed completion, and (below) a chain that ran out of
		// runs on an allowance.
		if o.evaluate && lastEnd == agent.EndCompleted {
			pass, findings, err := evaluateRun(ctx, agentCfg.Client, cwd, original, agentCfg.ToolOutputBudget)
			switch {
			case err != nil:
				// A failed evaluator changes nothing: the run's own word stands.
				fmt.Fprintf(diagOut, "pogo: evaluator failed (%v); the run's own result stands\n", err)
				return runErr
			case pass:
				fmt.Fprintf(diagOut, "pogo: evaluator: PASS\n")
				return runErr
			case runN >= o.maxRuns:
				return fmt.Errorf("run %d claimed done, but the evaluator disagrees:\n%s", runN, findings)
			default:
				fmt.Fprintf(diagOut, "pogo: run %d claimed done; the evaluator disagrees — chaining run %d with its findings\n",
					runN, runN+1)
				next = findingsPrompt(original, runN+1, findings)
				continue
			}
		}
		if !chainContinues(lastEnd, runN, o.maxRuns) {
			if o.evaluate && lastEnd.Disposition() == agent.DispositionContinue {
				pass, findings, err := evaluateRun(ctx, agentCfg.Client, cwd, original, agentCfg.ToolOutputBudget)
				switch {
				case err != nil:
					fmt.Fprintf(diagOut, "pogo: evaluator failed (%v); the run's own result stands\n", err)
				case pass:
					fmt.Fprintf(diagOut, "pogo: runs ran out on %s, but the evaluator confirms the work is done\n",
						lastEnd)
					return nil
				default:
					fmt.Fprintf(diagOut, "pogo: runs ran out on %s, and the evaluator agrees it is not done:\n%s\n",
						lastEnd, findings)
				}
			}
			return runErr
		}
		fmt.Fprintf(diagOut, "pogo: run %d ended on %s — chaining run %d (of at most %d), handoff in .pogo/handoff.md\n",
			runN, lastEnd, runN+1, o.maxRuns)
		next = handoffPrompt(original, runN+1)
	}
}

// overflowRetryable is the one-shot driver's retry decision, package-level so
// it can be tested without standing up a run: the run ended on the one failure
// compaction addresses, the automatic path is armed (-compact configured), and
// the breaker has not tripped.
func overflowRetryable(lastEnd agent.EndReason, armed bool, compactFails, circuit int) bool {
	return lastEnd == agent.EndContextOverflow && armed && compactFails < circuit
}

// checkCostBudget refuses a spend ceiling that cannot be enforced.
//
// -cost-budget needs a per-million-token rate, and pogo ships none: what a model
// costs is a claim about someone's billing arrangement, and both built-in providers
// are subscription plans where per-token cost is not a quantity that exists (see
// llm.Price). So the flag has exactly two honest behaviours, and this is the one
// that matches what was asked. The other — accept the number and ignore it — is what
// it used to do: the flag was defined, documented in two languages, and plumbed all
// the way to the agent, where nothing read it. A silent no-op is worse than a
// missing feature, because the value someone typed reads back as a safeguard.
//
// Refusing to start rather than warning, unlike a bad subagent_model reference. The
// difference is that a subagent mapping has a sane degraded mode — inherit the
// parent's model, session continues — and a spend ceiling does not: "carry on with
// no limit" is the precise opposite of the request, and the run this guards is the
// kind nobody is watching.
func checkCostBudget(budget float64, cfg config.Resolved) error {
	if budget <= 0 || cfg.Price != nil {
		return nil
	}
	path, _ := config.Path()
	return fmt.Errorf("-cost-budget needs a price for %s, and none is declared.\n"+
		"pogo ships no built-in prices: both built-in providers are subscription plans, "+
		"where there is no per-token cost to assert.\n"+
		"Either use -token-budget or -time-budget, which need no price, or declare the rate "+
		"in %s:\n"+
		"    {\"models\": [{\"id\": %q, \"provider\": %q, \"price\": "+
		"{\"input\": 0.5, \"output\": 1.5, \"cache_read\": 0.05}}]}\n"+
		"Rates are per million tokens, in whatever unit you are billed in; -cost-budget is "+
		"compared in that same unit.\n"+
		"Re-declaring a built-in model replaces it wholesale, so copy over its other fields "+
		"too — pogo will name any it loses",
		cfg.Model, path, cfg.Model, cfg.Provider)
}

func newClient(model string, retries int) (llm.Client, config.Resolved, error) {
	cfg, err := config.Resolve(model)
	if err != nil {
		return nil, config.Resolved{}, err
	}
	return llm.New(llm.Options{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		Model:         cfg.Model,
		MaxTokens:     cfg.MaxTokens,
		Timeout:       cfg.Timeout,
		Headers:       cfg.Headers,
		ExtraBody:     cfg.ExtraBody,
		Compatibility: cfg.Compatibility,
		MaxRetries:    retries,
		// Retries are announced so a rate-limited turn looks like waiting rather
		// than hanging.
		OnRetry: func(r llm.RetryInfo) {
			fmt.Fprintf(diagOut, "… retry %d/%d in %s: %s\n",
				r.Attempt, r.Max, r.Delay.Round(100*time.Millisecond), summarize(r.Reason, 120))
		},
	}), cfg, nil
}

func printModels(current string) {
	for _, m := range config.Catalog() {
		mark := " "
		if m.ID == current {
			mark = "*"
		}
		notes := []string{}
		if len(m.Aliases) > 0 {
			notes = append(notes, strings.Join(m.Aliases, ", "))
		}
		if !config.Configured(m.Provider) {
			notes = append(notes, config.KeyEnv(m.Provider)+" not set")
		}
		if m.SubagentModel != "" {
			// Shown because it is the one catalogue entry that changes what happens
			// without the user naming it at the prompt.
			notes = append(notes, "subagents: "+m.SubagentModel)
		}
		note := ""
		if len(notes) > 0 {
			note = fmt.Sprintf("  (%s)", strings.Join(notes, "; "))
		}
		fmt.Printf(" %s %-26s %-6s ctx %-5s%s\n", mark, m.ID, m.Provider, humanCtx(m.ContextWindow), note)
	}
	// Where to add one. A list of five built-ins with no hint that the set is
	// extensible is how a tool ends up looking like it only supports two vendors.
	if path, err := config.Path(); err == nil {
		state := "not present"
		if _, err := os.Stat(path); err == nil {
			state = "loaded"
		}
		fmt.Printf("\nadd your own providers and models in %s (%s)\n", path, state)
	}
}

// humanCtx renders a context-window size compactly: 200000 prints as "200K".
func humanCtx(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.0fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%dK", n/1000)
}

// printSkills implements -skills. It resolves the working directory itself
// because the flag can run without starting a session.
// printMemory answers the two questions -memory exists for: what does the agent
// remember, and where would it look. The second is printed even when the answer to
// the first is "nothing", for the reason printSkills gives: "no notes" is much less
// useful than "no notes, and here is the directory".
func printMemory(o memory.Options) error {
	if o.Cwd == "" {
		var err error
		if o.Cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	mem, diags := memory.Load(o)

	if mem.Empty() {
		fmt.Println("no notes recorded")
	} else {
		// The prompt section itself, verbatim. Showing the model's own view rather than
		// a second rendering of the same data: a listing that can disagree with what the
		// model sees would be a place for the two to drift, and this is the file where
		// the drift would be least visible.
		fmt.Println(mem.PromptSection())
	}

	if user, err := memory.UserDir(); err == nil {
		state := ""
		if !o.User {
			state = " (disabled by -no-memory)"
		}
		fmt.Printf("user: %s%s\n", user, state)
	}
	project := memory.ProjectDir(o.Cwd)
	if o.Project {
		fmt.Printf("project: %s\n", project)
	} else if _, err := os.Stat(project); err == nil {
		fmt.Printf("project: %s exists but is not loaded; pass -project-memory\n", project)
	}
	for _, d := range diags {
		fmt.Fprintln(os.Stderr, d)
	}
	return nil
}

func printSkills(o skills.Options, cwd string) error {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	o.Cwd = cwd
	list, diags := skills.Load(o)
	printSkillList(list)

	// The locations are printed even when empty: "no skills" is much less useful
	// than "no skills, and here is where I looked".
	fmt.Printf("user: %s\n", skills.UserDir())
	project := skills.ProjectDir(cwd)
	if o.IncludeProject {
		fmt.Printf("project: %s\n", project)
	} else if _, err := os.Stat(project); err == nil {
		fmt.Printf("project: %s exists but is not loaded; pass -project-skills\n", project)
	}
	reportSkillDiagnostics(diags)
	return nil
}

func printSkillList(list []skills.Skill) {
	if len(list) == 0 {
		fmt.Println("no skills loaded")
		return
	}
	for _, s := range list {
		notes := []string{s.Source}
		if s.DisableModelInvocation {
			notes = append(notes, "manual only")
		}
		fmt.Printf(" %-24s (%s)\n", s.Name, strings.Join(notes, "; "))
		fmt.Printf("   %s\n", summarize(s.Description, 100))
	}
}

// summarize collapses text to its first line, cut at max bytes, with a count
// of the lines it dropped. Control characters are removed: the text comes from
// a provider's error or a skill's frontmatter, and an escape sequence in either
// would otherwise reach the terminal reading the output.
func summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	first := lines[0]
	if len(first) > max {
		first = first[:max] + "…"
	}
	first = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r >= 0x7f && r <= 0x9f {
			return -1
		}
		return r
	}, first)
	if len(lines) > 1 {
		return fmt.Sprintf("%s (+%d lines)", first, len(lines)-1)
	}
	return first
}

// reportSkillDiagnostics prints load warnings to stderr. They go to stderr so
// that -p stays pipeable: a warning about someone's frontmatter must not end up
// inside the output a script is parsing.
func reportSkillDiagnostics(diags []skills.Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(diagOut, "skill %s: %s (%s)\n", d.Kind, d.Message, d.Path)
	}
}

// synthSources projects the skill list onto manifest sources. A bare .md skill
// has no directory to carry a tools.json and drops out, same as it drops out
// of skills.Roots.
func synthSources(list []skills.Skill) []tools.SynthSource {
	var out []tools.SynthSource
	for _, s := range list {
		if s.Dir == "" {
			continue
		}
		out = append(out, tools.SynthSource{Skill: s.Name, Dir: s.Dir, Identity: cliSkillIdentity(s)})
	}
	return out
}

func reportSynthDiagnostics(diags []tools.SynthDiag) {
	for _, d := range diags {
		if d.Path == "" {
			fmt.Fprintf(diagOut, "manifest tools: %s\n", d.Message)
			continue
		}
		fmt.Fprintf(diagOut, "manifest tools: %s (%s)\n", d.Message, d.Path)
	}
}

func printSessions() error {
	dir := session.DefaultDir()
	list, err := session.List(dir)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Printf("no sessions in %s\n", dir)
		return nil
	}
	for _, s := range list {
		when := time.UnixMilli(s.Updated).Format("01-02 15:04")
		title := s.Title
		if title == "" {
			title = "(empty)"
		}
		fmt.Printf(" %s  %-8s %3d msg  %s\n",
			when, s.Model, s.Messages, title)
	}
	fmt.Println(dir)
	return nil
}

func openExisting(resume string) (*session.Store, error) {
	if resume == "last" {
		path, err := session.Latest(session.DefaultDir())
		if err != nil {
			return nil, err
		}
		return session.Open(path)
	}
	return session.Open(resume)
}

// reportResumed announces which transcript a run is continuing.
//
// It writes to diagOut because it is a note to a person, and stdout belongs to
// the data. On stdout it is the first line `pogo -resume last -p ... | jq`
// reads — a bug in text mode too, not only under -mode json, which is why this
// is unconditional rather than mode-dependent.
func reportResumed(path string, messages int) {
	fmt.Fprintf(diagOut, "resumed %s (%d messages)\n", path, messages)
}

// reportSessionDiagnostics prints damage found while loading a transcript.
//
// A recovered session that says nothing is the failure this prevents: the count
// in "resumed ... (N messages)" is happily printed whether N is everything or
// what is left after a bad line, and only this tells the two apart.
func reportSessionDiagnostics(store *session.Store) {
	for _, d := range store.Diagnostics() {
		fmt.Fprintf(diagOut, "session: %s\n", d)
	}
}

// reportSessionDrift warns when the environment no longer matches what the
// session recorded.
//
// None of these is an error — continuing a session somewhere else is a legitimate
// thing to want. But each one changes behaviour without appearing anywhere in the
// transcript, so resuming into a different one silently is how a session stops
// being reproducible. The model is not checked here because it is not drift: the
// recorded model is used as the default.
func reportSessionDrift(store *session.Store, cwd string, skillNames []string) {
	m := store.Meta()
	if m == nil {
		return
	}
	if m.Cwd != "" && m.Cwd != cwd {
		fmt.Fprintf(diagOut, "session: recorded cwd is %s but running in %s; "+
			"paths in the transcript refer to the old one\n", m.Cwd, cwd)
	}
	if added, removed := diffNames(m.Skills, skillNames); len(added)+len(removed) > 0 {
		fmt.Fprintf(diagOut, "session: skills changed since this session was created\n")
		if len(removed) > 0 {
			fmt.Fprintf(diagOut, "  no longer loaded: %s\n", strings.Join(removed, ", "))
		}
		if len(added) > 0 {
			fmt.Fprintf(diagOut, "  newly loaded: %s\n", strings.Join(added, ", "))
		}
	}
}

// diffNames reports which names were dropped and which are new.
func diffNames(before, after []string) (added, removed []string) {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	has := make(map[string]bool, len(after))
	for _, n := range after {
		has[n] = true
		if !had[n] {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !has[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// readPipedStdin returns everything piped into the process, or "" when stdin is
// a terminal. Matching pi: piped content is data, not a prompt to type at.
//
// The char-device check is what keeps this from blocking forever on an
// interactive terminal. /dev/null is a character device too, so a subagent or
// scheduled child with no stdin reads nothing rather than waiting for EOF.
func readPipedStdin() (string, error) {
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// buildPrompt combines piped input with the -p flag. The piped document goes
// first and the instruction last, which is both pi's order and the one models
// follow best: the ask is the freshest thing in the context.
func buildPrompt(piped, flagPrompt string) string {
	switch {
	case piped == "":
		return flagPrompt
	case flagPrompt == "":
		return piped
	default:
		return piped + "\n\n" + flagPrompt
	}
}

// tapEvents forwards every event to the consumer unchanged, letting f see each
// one first. The channel hop is what lets incremental persistence ride the
// stream without the consumer interface growing a method.
func tapEvents(in <-chan agent.Event, f func(agent.Event)) <-chan agent.Event {
	out := make(chan agent.Event)
	go func() {
		defer close(out)
		for e := range in {
			f(e)
			out <- e
		}
	}()
	return out
}

// analyzeSessionFile analyzes a session JSONL file and outputs statistics.
//
// A directory is analysed too, and answers a different question: one file reports
// what a session cost, a directory reports how long runs actually take, which is
// the number a turn cap has to be chosen from. The two are separate because they
// are not the same unit — see analyze.Run.
//
// The literal "sessions" resolves to session.DefaultDir(), because the whole point
// of the aggregate is one's own history and nobody remembers where it is kept.
func analyzeSessionFile(path, format, outputPath string) error {
	if path == "sessions" {
		path = session.DefaultDir()
	}
	config := analyze.Config{
		IncludeTurns: false, // Set to true for verbose mode
	}

	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return analyzeRunLengths(path, format, outputPath, config)
	}

	stats, err := analyze.AnalyzeSession(path, config)
	if err != nil {
		return fmt.Errorf("failed to analyze session: %w", err)
	}

	var output string
	switch format {
	case "json":
		output, err = analyze.FormatJSON(stats)
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}
	case "text":
		output = analyze.FormatText(stats)
	default:
		return fmt.Errorf("invalid format %q: use 'text' or 'json'", format)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Analysis written to %s\n", outputPath)
	} else {
		fmt.Println(output)
	}

	return nil
}

// analyzeRunLengths reports the turn-count distribution across a directory of
// sessions. Shares the format and output flags with the single-session report,
// since the difference is the population, not the plumbing.
func analyzeRunLengths(dir, format, outputPath string, config analyze.Config) error {
	report, err := analyze.AnalyzeRuns(dir, config)
	if err != nil {
		return fmt.Errorf("failed to analyze sessions: %w", err)
	}

	var output string
	switch format {
	case "json":
		output, err = analyze.FormatRunsJSON(report)
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}
	case "text":
		output = analyze.FormatRunsText(report)
	default:
		return fmt.Errorf("invalid format %q: use 'text' or 'json'", format)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Analysis written to %s\n", outputPath)
		return nil
	}
	fmt.Println(output)
	return nil
}
