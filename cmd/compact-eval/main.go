// Command compact-eval measures what a compaction keeps when a real model
// writes the summary.
//
// One fixed coding session is compacted by the configured model, then the
// compacted conversation is asked three independent questions, each by a
// fresh agent over the same compacted history and without tools:
//
//   - constraints: list every requirement the person stated. Five were
//     stated, one in the opening request and four later, only in user
//     messages.
//   - behavior: write the code and the commit message the task needs, which
//     three of those constraints govern.
//   - recall: five facts that appear only in tool output and the assistant's
//     own notes — an error code, a decided signature, a test command, a
//     config key, a benchmark figure.
//
// Every check is deterministic string matching on the answer; no model grades
// another. Recall measures the summary itself. Constraints and behavior
// measure what survives into the next turn: the opening request is pinned
// verbatim either way, the later constraints only if the summary or the
// harness carries them. Run the same command on two builds to compare them.
//
// The provider key is read from the environment the configuration names
// (key_env), never from a flag, and is scrubbed from everything written.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type options struct {
	providers, model, output, label string
	repeats, rounds                 int
	keep                            int64
}

type probeReport struct {
	Name     string          `json:"name"`
	Passed   int             `json:"passed"`
	Total    int             `json:"total"`
	Checks   map[string]bool `json:"checks"`
	Usage    llm.Usage       `json:"usage"`
	WallMS   int64           `json:"wall_ms"`
	Error    string          `json:"error,omitempty"`
	Answer   string          `json:"-"`
	Artifact string          `json:"artifact,omitempty"`
}

type trialReport struct {
	Trial         int             `json:"trial"`
	Rounds        int             `json:"rounds"`
	Error         string          `json:"error,omitempty"`
	BeforeTokens  int64           `json:"before_tokens"`
	AfterTokens   int64           `json:"after_tokens"`
	SummaryBytes  int             `json:"summary_bytes"`
	CompactUsage  llm.Usage       `json:"compact_usage"`
	CompactWallMS int64           `json:"compact_wall_ms"`
	SummaryKeeps  map[string]bool `json:"summary_keeps_constraint"`
	ContextKeeps  map[string]bool `json:"context_keeps_constraint"`
	Probes        []probeReport   `json:"probes"`
}

type evalReport struct {
	Label         string        `json:"label"`
	Model         string        `json:"model"`
	Provider      string        `json:"provider"`
	Keep          int64         `json:"keep_tokens"`
	Rounds        int           `json:"rounds"`
	SessionTokens int64         `json:"session_tokens"`
	Started       time.Time     `json:"started"`
	Finished      time.Time     `json:"finished"`
	Trials        []trialReport `json:"trials"`
}

func main() { os.Exit(run()) }

func run() int {
	var opt options
	flag.StringVar(&opt.providers, "providers", "", "provider configuration path; defaults to POGO_CONFIG")
	flag.StringVar(&opt.model, "model", "", "configured model; defaults to the configuration's default")
	flag.StringVar(&opt.output, "output", "", "new or empty report directory")
	flag.StringVar(&opt.label, "label", "current", "variant name recorded in the report")
	flag.IntVar(&opt.repeats, "repeats", 3, "trials, 1 to 10")
	flag.Int64Var(&opt.keep, "keep", 0, "verbatim tail in tokens; 0 is the manual full-summary shape")
	flag.IntVar(&opt.rounds, "rounds", 1, "compactions per trial, 1 to 5; each later round first adds more work without new constraints")
	flag.Parse()
	if flag.NArg() != 0 || opt.output == "" || opt.repeats < 1 || opt.repeats > 10 || opt.keep < 0 || opt.rounds < 1 || opt.rounds > 5 {
		fmt.Fprintln(os.Stderr, "compact-eval: use --output DIR [--providers PATH] [--model MODEL] [--label NAME] [--repeats 1..10] [--rounds 1..5] [--keep TOKENS]")
		return 2
	}
	if opt.providers != "" {
		if err := os.Setenv(config.PathEnv, opt.providers); err != nil {
			fmt.Fprintln(os.Stderr, "compact-eval:", err)
			return 2
		}
	}
	if _, err := config.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "compact-eval: configuration:", err)
		return 2
	}
	resolved, err := config.Resolve(opt.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compact-eval: model:", err)
		return 2
	}
	if err := freshDir(opt.output); err != nil {
		fmt.Fprintln(os.Stderr, "compact-eval: output:", err)
		return 2
	}
	scrub := scrubber(resolved)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session := sessionHistory()
	report := evalReport{Label: opt.label, Model: resolved.Model, Provider: resolved.Provider, Keep: opt.keep, Rounds: opt.rounds,
		SessionTokens: llm.EstimateTokens(session), Started: time.Now()}
	for i := 1; i <= opt.repeats && ctx.Err() == nil; i++ {
		fmt.Fprintf(os.Stderr, "%s trial %d: compacting %d estimated tokens\n", opt.label, i, report.SessionTokens)
		trial := runTrial(ctx, opt, resolved, i, filepath.Join(opt.output, fmt.Sprintf("trial-%02d", i)), scrub)
		report.Trials = append(report.Trials, trial)
		fmt.Fprintf(os.Stderr, "%s trial %d: %s\n", opt.label, i, trialLine(trial))
	}
	report.Finished = time.Now()
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(opt.output, "report.json"), []byte(scrub(string(data))+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "compact-eval:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(opt.output, "summary.md"), []byte(scrub(summaryMarkdown(report))), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "compact-eval:", err)
		return 1
	}
	fmt.Print(scrub(summaryMarkdown(report)))
	return 0
}

func runTrial(ctx context.Context, opt options, resolved config.Resolved, n int, dir string, scrub func(string) string) (trial trialReport) {
	trial.Trial = n
	if err := os.MkdirAll(dir, 0o700); err != nil {
		trial.Error = err.Error()
		return trial
	}
	cwd, err := os.MkdirTemp("", "pogo-compact-eval-")
	if err != nil {
		trial.Error = err.Error()
		return trial
	}
	defer os.RemoveAll(cwd)
	client := llm.New(llm.Options{BaseURL: resolved.BaseURL, APIKey: resolved.APIKey, Model: resolved.Model, Headers: resolved.Headers,
		ExtraBody: resolved.ExtraBody, Compatibility: resolved.Compatibility, MaxTokens: resolved.MaxTokens, MaxRetries: 2, Timeout: 300 * time.Second})
	system := agent.SystemPrompt(cwd)

	a := agent.New(agent.Config{Client: client, Registry: tools.NewRegistry(), SystemPrompt: system})
	a.SetMessages(sessionHistory())
	var res agent.CompactResult
	for round := 1; round <= opt.rounds; round++ {
		if round > 1 {
			// More work and no new constraints: whatever the person said
			// earlier now has to survive another summary of a summary.
			a.SetMessages(append(append([]llm.Message(nil), a.Messages()...), laterWork(round)...))
		}
		started := time.Now()
		r, err := a.CompactKeep(ctx, opt.keep)
		trial.CompactWallMS += time.Since(started).Milliseconds()
		trial.CompactUsage = addUsage(trial.CompactUsage, r.Usage)
		if round == 1 {
			trial.BeforeTokens = r.BeforeTokens
		}
		trial.AfterTokens, trial.Rounds = r.AfterTokens, round
		if err != nil {
			trial.Error = fmt.Sprintf("compaction round %d: %s", round, scrub(err.Error()))
			return trial
		}
		res = r
	}
	compacted := a.Messages()
	trial.SummaryBytes = len(res.Summary)
	trial.SummaryKeeps = constraintChecks(res.Summary)
	var context strings.Builder
	for _, m := range compacted {
		context.WriteString(m.Text() + "\n")
	}
	trial.ContextKeeps = constraintChecks(context.String())
	_ = os.WriteFile(filepath.Join(dir, "compacted-context.txt"), []byte(scrub(context.String())), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(scrub(res.Summary)), 0o600)

	for _, probe := range probes {
		p := probeReport{Name: probe.name}
		q := agent.New(agent.Config{Client: client, Registry: tools.NewRegistry(), SystemPrompt: system, MaxTurns: 3})
		q.SetMessages(append([]llm.Message(nil), compacted...))
		started := time.Now()
		for event := range q.Run(ctx, probe.prompt) {
			if event.Kind == agent.EventAgentEnd && event.Err != nil {
				p.Error = scrub(event.Err.Error())
			}
		}
		p.WallMS, p.Usage = time.Since(started).Milliseconds(), q.Usage()
		p.Answer = lastAssistantText(q.Messages())
		p.Checks = probe.check(p.Answer)
		for _, ok := range p.Checks {
			p.Total++
			if ok {
				p.Passed++
			}
		}
		p.Artifact = filepath.Join(filepath.Base(dir), "probe-"+probe.name+".txt")
		_ = os.WriteFile(filepath.Join(dir, "probe-"+probe.name+".txt"), []byte(scrub(probe.prompt+"\n\n---\n\n"+p.Answer)), 0o600)
		trial.Probes = append(trial.Probes, p)
	}
	return trial
}

func addUsage(a, b llm.Usage) llm.Usage {
	a.Input += b.Input
	a.Output += b.Output
	a.CacheRead += b.CacheRead
	a.Reasoning += b.Reasoning
	return a
}

func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			if text := strings.TrimSpace(msgs[i].Text()); text != "" {
				return text
			}
		}
	}
	return ""
}

// --- the session --------------------------------------------------------

// The five constraints, each stated once, only by the person. The first is
// in the opening request, which every build pins verbatim.
const (
	constraintJSON    = "命令输出必须是 JSON，不要表格。"
	constraintVendor  = "注意：不要修改 vendor/ 和 internal/legacy/ 下的任何文件。"
	constraintCents   = "金额一律用以分为单位的 int64，字段名叫 amount_cents，禁止用浮点数。"
	constraintComment = "以后新增的每个函数都要写中文注释。"
	constraintCommit  = "提交信息用英文，并以 billing: 开头。"
)

// transcript builds a history the way a session records one.
type transcript struct {
	msgs   []llm.Message
	prefix string
	id     int
}

func (t *transcript) call(name, input, result string) {
	t.id++
	callID := fmt.Sprintf("call_%s%02d", t.prefix, t.id)
	t.msgs = append(t.msgs,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: callID, Name: name, Input: json.RawMessage(input)}}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: callID, Text: result}}})
}

// bash records a bash call the way the tool reports one: with the details
// the host keeps (tools.BashDetails), and a non-zero exit as an error result
// whose text ends with the status line the tool appends.
func (t *transcript) bash(input string, exit int, output string) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		panic(fmt.Sprintf("fixture bash input %s: %v", input, err))
	}
	t.id++
	callID := fmt.Sprintf("call_%s%02d", t.prefix, t.id)
	details, err := json.Marshal(tools.BashDetails{Command: args.Command, ExitCode: exit, DurationMS: int64(120 + 37*t.id)})
	if err != nil {
		panic(err)
	}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: callID, Text: output, Details: details}
	if exit != 0 {
		result.IsError = true
		result.Text = fmt.Sprintf("%s\n\nCommand exited with code %d", output, exit)
	}
	t.msgs = append(t.msgs,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: callID, Name: "bash", Input: json.RawMessage(input)}}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{result}})
}

func (t *transcript) say(text string) {
	t.msgs = append(t.msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}})
}

func (t *transcript) user(text string) { t.msgs = append(t.msgs, llm.UserText(text)) }

// laterWork is another stretch of the same task with no new constraint: the
// next compaction has to keep the earlier ones through a summary of a summary.
func laterWork(round int) []llm.Message {
	t := &transcript{prefix: fmt.Sprintf("r%d_", round)}
	t.user("继续。")
	t.call("read", `{"path":"internal/billing/ledger_test.go"}`, goFile("billing", "s *suite", "Case", 60))
	t.bash(`{"command":"go test -v ./internal/..."}`, 0, verboseTests(300))
	t.bash(`{"command":"git diff"}`, 0, draftDiff(260))
	t.say(fmt.Sprintf("第 %d 段：MonthlyTotals 的跨月边界和混币种测试已补，命令行接线还没做。", round))
	return t.msgs
}

func sessionHistory() []llm.Message {
	t := &transcript{}
	call, bash, say, user := t.call, t.bash, t.say, t.user

	user("帮我给 billing 服务加一个 `ledger monthly` 子命令，按月汇总每个账户的账单。" + constraintJSON)
	call("read", `{"path":"internal/billing/ledger.go"}`, ledgerSource)
	say("ledger.go 里已有 `Totals`，按账户汇总全部历史，没有时间范围。先看命令行入口、调用方和现有测试。")
	call("read", `{"path":"cmd/ledger/main.go"}`, ledgerMain)
	call("grep", `{"pattern":"Totals\\(","path":"."}`, grepTotals)
	call("read", `{"path":"internal/api/handlers.go"}`, goFile("api", "h *Handler", "Account", 56))
	user(constraintVendor)
	bash(`{"command":"go test ./internal/billing/..."}`, 1, failingTests)
	say("失败来自 `TestTotalsMixedCurrency`：错误码 E4127（ledger: currency mismatch (CNY vs USD) at ledger.go:212），" +
		"是 `Totals` 在混合币种时直接相加造成的，属于既有问题。\n\n" +
		"决定：不改 `Totals`，因为 internal/legacy/report.go 依赖它的返回形状；新增 " +
		"`MonthlyTotals(ctx context.Context, month string) ([]MonthlyTotal, error)`，按币种分开汇总。\n" +
		"月份边界按配置项 `BILLING_TZ=Asia/Shanghai` 计算，不用服务器本地时区。")
	call("read", `{"path":"internal/billing/config.go"}`, configSource)
	call("read", `{"path":"internal/store/pg/query.go"}`, goFile("pg", "q *Queries", "Entry", 64))
	bash(`{"command":"go test -v ./internal/..."}`, 0, verboseTests(260))
	user(constraintCents)
	call("read", `{"path":"internal/billing/currency.go"}`, currencySource)
	bash(`{"command":"go vet ./... && staticcheck ./internal/billing/..."}`, 1, vetOutput)
	call("read", `{"path":"internal/billing/export/export.go"}`, goFile("export", "w *Writer", "Batch", 48))
	call("write", `{"path":"internal/billing/monthly.go","content":"package billing\n\n// draft\n"}`, "Wrote internal/billing/monthly.go (3 lines)")
	bash(`{"command":"git diff"}`, 0, draftDiff(240))
	bash(`{"command":"go test ./internal/billing/ -run TestMonthlyTotals"}`, 0, "ok  \tgithub.com/acme/billing/internal/billing\t0.012s")
	say("新测试用 `go test ./internal/billing/ -run TestMonthlyTotals` 跑，草稿已通过。")
	user(constraintComment)
	call("read", `{"path":"internal/api/routes.go"}`, goFile("api", "r *Router", "Route", 40))
	bash(`{"command":"go test -bench BenchmarkMonthly -run '^$' ./internal/billing/"}`, 0, benchOutput)
	say("基准：BenchmarkMonthly 每次 18432 ns/op，412 B/op，9 allocs/op，主要开销在按币种分组的 map 分配。")
	bash(`{"command":"go test -v ./internal/billing/export/..."}`, 0, verboseTests(180))
	bash(`{"command":"git log --oneline -n 40"}`, 0, gitLog)
	user(constraintCommit)
	say("好的。接下来把 `MonthlyTotals` 的正式实现、`ledger monthly` 的命令行接线和测试补齐，然后准备提交。")
	return t.msgs
}

// --- probes -------------------------------------------------------------

type probe struct {
	name, prompt string
	check        func(answer string) map[string]bool
}

var probes = []probe{
	{
		name:   "constraints",
		prompt: "不要调用任何工具。请逐条列出我在这次会话里提出过的全部要求和限制（包括中途补充的），每条一行，尽量用我的原话。",
		check:  constraintChecks,
	},
	{
		name:   "behavior",
		prompt: "不要调用任何工具。现在直接给出 MonthlyTotal 结构体和 MonthlyTotals 函数的完整 Go 实现（放在一个 go 代码块里），然后给出这次改动的提交信息（单独一行）。",
		check:  behaviorChecks,
	},
	{
		name: "recall",
		prompt: "不要调用任何工具，只根据你现在掌握的信息回答，不知道就填 null，不要猜。用一个 JSON 对象回答，字段：" +
			"error_code（之前 go test 报出的错误码）、function（决定新增的函数的完整签名）、test_command（跑新测试用的完整命令）、" +
			"tz_config（月份边界用的配置项及其值）、benchmark_ns_op（BenchmarkMonthly 的 ns/op 数值）。",
		check: recallChecks,
	},
}

// constraintChecks reports which constraints a text states, by the terms each
// one cannot be restated without.
func constraintChecks(text string) map[string]bool {
	lower := strings.ToLower(text)
	return map[string]bool{
		"json":    strings.Contains(lower, "json"),
		"vendor":  strings.Contains(lower, "vendor") && strings.Contains(lower, "internal/legacy"),
		"cents":   strings.Contains(lower, "amount_cents") && (strings.Contains(lower, "int64") || strings.Contains(text, "浮点")),
		"comment": strings.Contains(text, "中文注释") || (strings.Contains(text, "中文") && strings.Contains(text, "注释")),
		"commit":  strings.Contains(lower, "billing:") && (strings.Contains(text, "英文") || strings.Contains(lower, "english")),
	}
}

var codeFence = regexp.MustCompile("(?s)```[a-zA-Z]*\\n(.*?)```")

// behaviorChecks reads the code and the commit line the task asked for.
func behaviorChecks(answer string) map[string]bool {
	var code strings.Builder
	for _, m := range codeFence.FindAllStringSubmatch(answer, -1) {
		code.WriteString(m[1] + "\n")
	}
	src := code.String()
	prose := codeFence.ReplaceAllString(answer, "")
	return map[string]bool{
		"cents_int64":      strings.Contains(src, "amount_cents") && strings.Contains(src, "int64") && !strings.Contains(src, "float"),
		"chinese_comments": funcsHaveChineseComments(src),
		"commit_english":   commitLine(prose) || commitLine(src),
	}
}

func funcsHaveChineseComments(src string) bool {
	lines := strings.Split(src, "\n")
	funcs := 0
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "func ") {
			continue
		}
		funcs++
		commented := false
		for j := i - 1; j >= 0 && j >= i-6; j-- {
			prev := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(prev, "//") {
				break
			}
			if hasHan(prev) {
				commented = true
			}
		}
		if !commented {
			return false
		}
	}
	return funcs > 0
}

var commitRe = regexp.MustCompile(`(?m)billing: [A-Za-z][^\n]*$`)

func commitLine(text string) bool {
	for _, line := range commitRe.FindAllString(text, -1) {
		if !hasHan(line) {
			return true
		}
	}
	return false
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

var jsonObject = regexp.MustCompile(`(?s)\{.*\}`)

// recallChecks reads the facts as values of the JSON fields asked for, and
// falls back to the whole answer when the object does not parse.
func recallChecks(answer string) map[string]bool {
	fields := map[string]string{}
	if raw := jsonObject.FindString(answer); raw != "" {
		var parsed map[string]any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			for k, v := range parsed {
				if v != nil {
					fields[k] = fmt.Sprint(v)
				}
			}
		}
	}
	value := func(key string) string {
		if v, ok := fields[key]; ok {
			return v
		}
		return answer
	}
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return map[string]bool{
		"error_code":   strings.Contains(value("error_code"), "E4127"),
		"function":     strings.Contains(compact(value("function")), "MonthlyTotals(ctxcontext.Context,monthstring)([]MonthlyTotal,error)"),
		"test_command": strings.Contains(value("test_command"), "go test ./internal/billing/ -run TestMonthlyTotals"),
		"tz_config":    strings.Contains(value("tz_config"), "BILLING_TZ") && strings.Contains(value("tz_config"), "Asia/Shanghai"),
		"benchmark":    strings.Contains(value("benchmark_ns_op"), "18432"),
	}
}

// --- reporting ----------------------------------------------------------

func trialLine(t trialReport) string {
	if t.Error != "" {
		return "error: " + t.Error
	}
	parts := []string{fmt.Sprintf("%d→%d tokens", t.BeforeTokens, t.AfterTokens)}
	for _, p := range t.Probes {
		parts = append(parts, fmt.Sprintf("%s %d/%d", p.Name, p.Passed, p.Total))
	}
	return strings.Join(parts, ", ")
}

type tally struct{ passed, total int }

func summaryMarkdown(r evalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# compact-eval: %s\n\nmodel `%s` (%s), keep %d tokens, rounds %d, session ~%d tokens, %d trial(s), %s\n\n",
		r.Label, r.Model, r.Provider, r.Keep, r.Rounds, r.SessionTokens, len(r.Trials), r.Finished.Sub(r.Started).Round(time.Second))
	probeTally := map[string]*tally{}
	checkTally := map[string]*tally{}
	summaryKeeps, contextKeeps := map[string]int{}, map[string]int{}
	ok := 0
	for _, t := range r.Trials {
		if t.Error != "" {
			fmt.Fprintf(&b, "- trial %d failed: %s\n", t.Trial, t.Error)
			continue
		}
		ok++
		for k, v := range t.SummaryKeeps {
			if v {
				summaryKeeps[k]++
			}
		}
		for k, v := range t.ContextKeeps {
			if v {
				contextKeeps[k]++
			}
		}
		for _, p := range t.Probes {
			pt := probeTally[p.Name]
			if pt == nil {
				pt = &tally{}
				probeTally[p.Name] = pt
			}
			pt.passed += p.Passed
			pt.total += p.Total
			for k, v := range p.Checks {
				key := p.Name + "." + k
				ct := checkTally[key]
				if ct == nil {
					ct = &tally{}
					checkTally[key] = ct
				}
				ct.total++
				if v {
					ct.passed++
				}
			}
		}
	}
	b.WriteString("| probe | passed |\n|---|---|\n")
	for _, p := range probes {
		if t := probeTally[p.name]; t != nil {
			fmt.Fprintf(&b, "| %s | %d/%d |\n", p.name, t.passed, t.total)
		}
	}
	b.WriteString("\n| check | passed |\n|---|---|\n")
	for _, p := range probes {
		for _, k := range sortedChecks(p.name, checkTally) {
			t := checkTally[p.name+"."+k]
			fmt.Fprintf(&b, "| %s.%s | %d/%d |\n", p.name, k, t.passed, t.total)
		}
	}
	b.WriteString("\n| constraint | in summary | in compacted context |\n|---|---|---|\n")
	for _, k := range []string{"json", "vendor", "cents", "comment", "commit"} {
		fmt.Fprintf(&b, "| %s | %d/%d | %d/%d |\n", k, summaryKeeps[k], ok, contextKeeps[k], ok)
	}
	return b.String()
}

func sortedChecks(probe string, tallies map[string]*tally) []string {
	var keys []string
	for k := range tallies {
		if name, check, ok := strings.Cut(k, "."); ok && name == probe {
			keys = append(keys, check)
		}
	}
	sort.Strings(keys)
	return keys
}

func freshDir(path string) error {
	entries, err := os.ReadDir(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.MkdirAll(path, 0o700)
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%s is not empty", path)
	}
	return nil
}

// scrubber removes the credential and header values from anything written.
func scrubber(r config.Resolved) func(string) string {
	var secrets []string
	if r.APIKey != "" {
		secrets = append(secrets, r.APIKey)
	}
	for _, v := range r.Headers {
		if len(v) >= 8 {
			secrets = append(secrets, v)
		}
	}
	return func(s string) string {
		for _, secret := range secrets {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
		return s
	}
}
