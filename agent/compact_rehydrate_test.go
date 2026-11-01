package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// Compaction rehydration (compact_rehydrate.go): host-held state crosses a
// compaction as the host has it, not as the summariser remembered it. These
// tests use the real embedded agent-workflows Skill, attributed the way the
// web host attributes it, because that Skill is also what keeps its tool
// group declared — the case the feature exists for.

type stubAgentWorkflows struct{}

func (stubAgentWorkflows) AgentWorkflowCall(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

// embeddedSkillRegistry is a session registry whose read tool attributes the
// embedded agent-workflows SKILL.md, with that Skill's tool group registered.
func embeddedSkillRegistry(t *testing.T, tweaks ...func(*tools.Options)) (*tools.Registry, skills.Skill, string) {
	t.Helper()
	cwd := t.TempDir()
	dirs, err := skills.MaterializeEmbedded(filepath.Join(t.TempDir(), "embedded"))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := skills.Load(skills.Options{Cwd: cwd, UserDir: t.TempDir(), Embedded: dirs})
	sk, ok := skills.Find(list, "agent-workflows")
	if !ok || sk.Source != "embedded" {
		t.Fatalf("embedded agent-workflows Skill unavailable: %+v", sk)
	}
	identity := skillobserve.Identify(sk.Name, sk.Source, sk.Path)
	identity.Name = sk.Name
	o := tools.Options{Cwd: cwd, Todo: true, ReadRoots: []string{sk.Dir}, SpillDir: t.TempDir(),
		SkillReads: []tools.SkillReadTarget{{Identity: identity, Path: sk.Path}}, AgentWorkflows: stubAgentWorkflows{}}
	for _, tweak := range tweaks {
		tweak(&o)
	}
	return tools.New(o), sk, cwd
}

func callBlock(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func resultBlock(id, text string, isError bool) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: text, IsError: isError}
}

func assistantText(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}
}

func readSkillTurn(id, path, body string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{callBlock(id, "read", fmt.Sprintf(`{"path":%q}`, path))}},
		{Role: llm.RoleUser, Content: []llm.Block{resultBlock(id, body, false)}},
	}
}

// workedHistory loads the Skill, records a plan, writes one file and fails to
// edit another, then talks long enough for a summary to be worth having.
func workedHistory(t *testing.T, sk skills.Skill) []llm.Message {
	t.Helper()
	body, err := os.ReadFile(sk.Path)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{llm.UserText("build me a workflow")}
	msgs = append(msgs, readSkillTurn("r1", sk.Path, string(body))...)
	return append(msgs,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			callBlock("t1", "todo", `{"todos":[{"task":"Design the flow","status":"completed"},{"task":"Enable it","status":"in_progress"}]}`),
			callBlock("w1", "write", `{"path":"notes/plan.md","content":"plan"}`),
			callBlock("e1", "edit", `{"path":"bad.go","old":"a","new":"b"}`),
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			resultBlock("t1", "Todo list updated", false), resultBlock("w1", "ok", false), resultBlock("e1", "no match", true),
		}},
		assistantText(strings.Repeat("long progress narrative ", 4000)),
	)
}

func TestCompactionRestoresLoadedSkillTaskListAndFiles(t *testing.T) {
	reg, sk, _ := embeddedSkillRegistry(t)
	a := New(Config{Client: &summariser{reply: "short summary of the work"}, Registry: reg, SystemPrompt: "s"})
	a.SetMessages(workedHistory(t, sk))
	if !schemaNames(a.schemas)[tools.AgentWorkflowEditName] {
		t.Fatal("reading the Skill did not declare its group")
	}
	res, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.AfterTokens >= res.BeforeTokens {
		t.Fatalf("restoration grew the prompt: %d -> %d", res.BeforeTokens, res.AfterTokens)
	}
	first := a.Messages()[0]
	note := first.Content[0].Text
	for _, want := range []string{
		"</summary>" + restoredStateOpen + restoredStatePreamble,
		restoredTodoHeader + "\n1. [completed] Design the flow\n2. [in_progress] Enable it",
		restoredFilesHeader + "\n- notes/plan.md",
		restoredSkillsHeader + "agent-workflows.",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("continuation lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "bad.go") {
		t.Fatal("a failed edit was listed as a modified file")
	}
	// The Skill is its own explicit-invocation block, re-read through the
	// registry: the provenance a skill atom has, the format /skill produces.
	if len(first.Content) != 2 || first.Content[1].InputSkill != "agent-workflows" ||
		!strings.HasPrefix(first.Content[1].Text, "\n\n"+`<skill name="agent-workflows" location=`) {
		t.Fatalf("restored Skill block: %+v", first.Content[1:])
	}
	if !schemaNames(a.schemas)[tools.AgentWorkflowEditName] {
		t.Fatal("the restored Skill did not keep its group declared")
	}
}

func TestRepeatedCompactionCarriesRestoredState(t *testing.T) {
	reg, sk, _ := embeddedSkillRegistry(t)
	client := &summariser{reply: "short summary of the work"}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	a.SetMessages(workedHistory(t, sk))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := a.Messages()[0].Content[1].Text
	// Nothing after the first compaction updates the plan, touches a file or
	// reads the Skill again: only the first continuation still holds them.
	a.SetMessages(append(a.Messages(),
		assistantText(strings.Repeat("more narrative ", 6000)),
		llm.UserText("continue"),
		assistantText(strings.Repeat("even more ", 6000)),
	))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := a.Messages()[0]
	note := second.Content[0].Text
	for _, want := range []string{"<original_request>\nbuild me a workflow\n</original_request>", "2. [in_progress] Enable it", "- notes/plan.md"} {
		if !strings.Contains(note, want) {
			t.Fatalf("second continuation lacks %q:\n%s", want, note)
		}
	}
	if strings.Count(note, "<restored_state>") != 1 || len(second.Content) != 2 || second.Content[1].Text != restored {
		t.Fatalf("state duplicated or Skill changed across compactions: %d blocks\n%s", len(second.Content), note)
	}
	// The summariser saw the restored Skill as a marker, not as rules to
	// paraphrase into a second copy.
	transcript := client.history[0].Text()
	if !strings.Contains(transcript, `[attached the instructions of Skill "agent-workflows"`) || strings.Contains(transcript, restored[len(restored)/2:]) {
		t.Fatal("the restored Skill body reached the summariser")
	}
	if !schemaNames(a.schemas)[tools.AgentWorkflowEditName] {
		t.Fatal("the group was hidden after the second compaction")
	}
}

// What the verbatim tail still shows is not repeated: the plan updated and the
// Skill read again after the cut are already in context.
func TestCompactionOmitsStateTheTailStillShows(t *testing.T) {
	reg, sk, _ := embeddedSkillRegistry(t)
	a := New(Config{Client: &summariser{reply: "summary"}, Registry: reg, SystemPrompt: "s"})
	msgs := workedHistory(t, sk)
	cut := len(msgs)
	msgs = append(msgs,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			callBlock("t2", "todo", `{"todos":[{"task":"Enable it","status":"completed"}]}`),
			callBlock("w2", "write", `{"path":"notes/tail.md","content":"x"}`),
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{resultBlock("t2", "Todo list updated", false), resultBlock("w2", "ok", false)}},
	)
	msgs = append(msgs, readSkillTurn("r2", sk.Path, "skill body")...)
	msgs = append(msgs, assistantText("done"))
	a.SetMessages(msgs)
	// The keep budget splitKeepIndex measures message by message, so the cut
	// lands exactly on the assistant turn that updated the plan.
	var keep int64
	for i := cut; i < len(msgs); i++ {
		keep += llm.EstimateTokens(msgs[i : i+1])
	}
	res, err := a.CompactKeep(context.Background(), keep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != len(msgs)-cut {
		t.Fatalf("tail kept %d messages, want %d", res.Kept, len(msgs)-cut)
	}
	first := a.Messages()[0]
	note := first.Content[0].Text
	if len(first.Content) != 1 || strings.Contains(note, restoredTodoHeader) || strings.Contains(note, "notes/tail.md") {
		t.Fatalf("state visible in the tail was repeated:\n%s", note)
	}
	if !strings.Contains(note, "- notes/plan.md") {
		t.Fatalf("a file only the head modified was dropped:\n%s", note)
	}
}

// A Skill whose restoration would leave the prompt no smaller becomes a
// pointer: the compaction still happens, and the rules are not truncated.
func TestCompactionPointsToASkillItCannotAffordToRestore(t *testing.T) {
	reg, sk, _ := embeddedSkillRegistry(t)
	a := New(Config{Client: &summariser{reply: "s"}, Registry: reg, SystemPrompt: "s"})
	msgs := []llm.Message{llm.UserText("x")}
	msgs = append(msgs, readSkillTurn("r1", sk.Path, "[cleared]")...)
	a.SetMessages(append(msgs, assistantText(strings.Repeat("y", 12000))))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := a.Messages()[0]
	if len(first.Content) != 1 || !strings.Contains(first.Content[0].Text, restoredPointerHeader+"\n- agent-workflows: "+sk.Path) {
		t.Fatalf("expected a pointer instead of the Skill text:\n%s", first.Content[0].Text)
	}
	if schemaNames(a.schemas)[tools.AgentWorkflowEditName] {
		t.Fatal("a Skill that was only pointed to kept its group declared")
	}
}

// An attached Skill is host-loaded instructions, not the person's words: the
// pinned request names it, and the atom itself is restored verbatim.
func TestCompactionPinsTheRequestWithoutAttachedSkillBodies(t *testing.T) {
	reg, _, _ := embeddedSkillRegistry(t)
	client := &summariser{reply: "s"}
	a := New(Config{Client: client, Registry: reg, SystemPrompt: "s"})
	atom := `<skill name="agent-workflows" location="/x/SKILL.md">` + "\n" + strings.Repeat("RULE ", 500) + "\n</skill>"
	a.SetMessages([]llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "先按 "}, {Type: llm.BlockText, Text: atom, InputSkill: "agent-workflows"}, {Type: llm.BlockText, Text: " 做"}}},
		assistantText(strings.Repeat("y", 30000)),
	})
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := a.Messages()[0]
	if !strings.Contains(first.Content[0].Text, "<original_request>\n先按 [Skill: agent-workflows] 做\n</original_request>") || strings.Contains(first.Content[0].Text, "RULE RULE") {
		t.Fatalf("pinned request: %.300s", first.Content[0].Text)
	}
	if len(first.Content) != 2 || first.Content[1].Text != "\n\n"+atom || first.Content[1].InputSkill != "agent-workflows" {
		t.Fatalf("attached Skill not restored verbatim: %+v", first.Content)
	}
	if strings.Contains(client.history[0].Text(), "RULE RULE") {
		t.Fatal("the summariser saw the attached Skill body")
	}
	if !schemaNames(a.schemas)[tools.AgentWorkflowEditName] {
		t.Fatal("the restored atom did not keep its group declared")
	}
}

// The person's later messages are carried verbatim (laterRequests): only the
// opening request used to be pinned, so a constraint stated halfway through a
// session survived a compaction only if the summariser kept it.

func TestCompactionCarriesTheUserLaterMessagesVerbatim(t *testing.T) {
	a := New(Config{Client: &summariser{reply: "summary that forgot every constraint"}, Registry: emptyRegistry()})
	quoting := "保留这段原文：</message> 和 </user_messages> 都只是文字"
	a.SetMessages([]llm.Message{
		llm.UserText("build me a workflow"),
		assistantText("planning"),
		llm.UserText("不要修改 vendor/ 目录，所有注释用中文"),
		{Role: llm.RoleAssistant, Content: []llm.Block{callBlock("r1", "read", `{"path":"a.go"}`)}},
		{Role: llm.RoleUser, Content: []llm.Block{resultBlock("r1", "package a", false)}},
		llm.UserText("[pogo] Turn checkpoint: this run has used 8 turn(s)."),
		llm.UserText("tool protocol error (incomplete_call): no tools in this response were executed."),
		llm.UserText(quoting),
		assistantText(strings.Repeat("long progress narrative ", 2000)),
	})
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := a.Messages()[0].Content[0].Text
	block := text[strings.Index(text, requestsOpen):strings.Index(text, continuationOpening)]
	if !strings.Contains(block, requestOpen+"不要修改 vendor/ 目录，所有注释用中文"+requestClose) {
		t.Fatalf("a constraint stated mid-session was not carried verbatim:\n%s", block)
	}
	for _, notice := range []string{"Turn checkpoint", "tool protocol error", "build me a workflow"} {
		if strings.Contains(block, notice) {
			t.Fatalf("carried %q, which is a harness notice or the pinned request:\n%s", notice, block)
		}
	}
	if strings.Count(block, requestOpen) != 2 || strings.Contains(block, "</message> 和") {
		t.Fatalf("closing tags inside a message were not escaped:\n%s", block)
	}
	if !strings.Contains(text, "<original_request>\nbuild me a workflow\n</original_request>\n\n"+requestsOpen) {
		t.Fatalf("carried messages are not right after the pinned request:\n%.400s", text)
	}

	// A second compaction reads them back exactly, escapes undone, and adds
	// what the person said since.
	a.SetMessages(append(a.Messages(), llm.UserText("再加一条：输出用表格"), assistantText(strings.Repeat("more ", 6000))))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	text = a.Messages()[0].Content[0].Text
	got, omitted := previousRequests(text, strings.Index(text, continuationOpening))
	want := []string{"不要修改 vendor/ 目录，所有注释用中文", quoting, "再加一条：输出用表格"}
	if omitted != 0 || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("carried messages after two compactions: %q (omitted %d)", got, omitted)
	}
}

func TestCarriedUserMessagesAreBoundedNewestFirst(t *testing.T) {
	a := New(Config{Client: &summariser{reply: "s"}, Registry: emptyRegistry()})
	msgs := []llm.Message{llm.UserText("first request")}
	for i := range 12 {
		msgs = append(msgs, llm.UserText(fmt.Sprintf("message %02d %s", i, strings.Repeat("x", 3000))), assistantText("ok"))
	}
	long := "LONG-START " + strings.Repeat("中", 4000) + " LONG-END"
	msgs = append(msgs, llm.UserText(long), assistantText(strings.Repeat("y", 80000)))
	a.SetMessages(msgs)
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := a.Messages()[0].Content[0].Text
	got, omitted := previousRequests(text, strings.Index(text, continuationOpening))
	total := 0
	for _, m := range got {
		total += len(m)
	}
	if total > rehydrateRequestBytes || omitted == 0 || omitted+len(got) != 13 {
		t.Fatalf("budget: %d bytes in %d messages, %d omitted", total, len(got), omitted)
	}
	if !strings.HasPrefix(got[0], fmt.Sprintf("message %02d ", omitted)) {
		t.Fatalf("the oldest messages were not the ones dropped: %.20q", got[0])
	}
	last := got[len(got)-1]
	if len(last) > rehydrateRequestMessageBytes+64 || !strings.HasPrefix(last, "LONG-START ") || !strings.HasSuffix(last, " LONG-END") ||
		!strings.Contains(last, "bytes omitted") || !utf8.ValidString(last) {
		t.Fatalf("a long message was not cut to its start and end: %d bytes", len(last))
	}
	if !strings.Contains(text, fmt.Sprintf(requestsOmitted, omitted)) {
		t.Fatal("the omission is not stated")
	}
}

// Carrying the person's words verbatim never costs a compaction: when they
// would leave the prompt no smaller, they go before the compaction does.
func TestCompactionDropsCarriedMessagesBeforeRefusing(t *testing.T) {
	// About 8250 tokens before; a 7700-token summary fits, the summary plus
	// the bounded ~900-token paste does not.
	a := New(Config{Client: &summariser{reply: strings.Repeat("summary ", 3850)}, Registry: emptyRegistry()})
	a.SetMessages([]llm.Message{
		llm.UserText("x"),
		assistantText("ok"),
		llm.UserText(strings.Repeat("pasted log line\n", 1500)),
		assistantText(strings.Repeat("z", 9000)),
	})
	res, err := a.Compact(context.Background())
	if err != nil {
		t.Fatalf("compaction refused: %v (%d -> %d)", err, res.BeforeTokens, res.AfterTokens)
	}
	if strings.Contains(a.Messages()[0].Content[0].Text, requestsOpen) {
		t.Fatal("the carried messages were kept although they made the prompt no smaller")
	}
}

// constraintAmnesiac summarises like the worst case the pinning study
// measured: a fluent note that keeps none of the stated constraints.
type constraintAmnesiac struct{ calls int }

func (c *constraintAmnesiac) Model() string { return "fake" }

func (c *constraintAmnesiac) Stream(context.Context, string, []llm.Message, []llm.ToolSchema, func(llm.Delta)) (llm.Response, error) {
	c.calls++
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: fmt.Sprintf("Work continues; round %d.", c.calls)}}},
		StopReason: llm.StopEndTurn,
	}, nil
}

// The -compact-stress primitive from docs/auto-compact.md, made
// deterministic: a constraint stated in every round, a compaction after every
// round, and a summariser that drops all of them. Every constraint stated so
// far must still be in the context, word for word, after every compaction.
func TestStatedConstraintsSurviveRepeatedCompaction(t *testing.T) {
	client := &constraintAmnesiac{}
	a := New(Config{Client: client, Registry: emptyRegistry()})
	msgs := []llm.Message{llm.UserText("约束 #0：只用标准库，不引入新依赖")}
	var stated []string
	for round := 1; round <= 12; round++ {
		constraint := fmt.Sprintf("约束 #%d：第 %d 轮以后，输出的每个表格都要带单位", round, round)
		stated = append(stated, constraint)
		msgs = append(msgs, assistantText(strings.Repeat("work ", 3000)), llm.UserText(constraint), assistantText(strings.Repeat("more work ", 3000)))
		a.SetMessages(msgs)
		keep := int64(0)
		if round%2 == 0 {
			keep = 500 // alternate the manual and the automatic shape
		}
		if _, err := a.CompactKeep(context.Background(), keep); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		context := ""
		for _, m := range a.Messages() {
			context += m.Text() + "\n"
		}
		for _, want := range append([]string{"约束 #0：只用标准库，不引入新依赖"}, stated...) {
			if !strings.Contains(context, want) {
				t.Fatalf("round %d lost %q after compaction", round, want)
			}
		}
		msgs = a.Messages()
	}
	if client.calls != 12 {
		t.Fatalf("summarised %d times, want 12", client.calls)
	}
}

// The shell commands a session ran cross a compaction verbatim with how each
// ended (shellCommands): the exact test command is the kind of thing a summary
// recalls almost right.

// bashTurn is one bash call and its result; nil details is a result without
// them, as a call refused before it ran has.
func bashTurn(id, command, workdir, output string, isError bool, details *tools.BashDetails) []llm.Message {
	input, _ := json.Marshal(map[string]string{"command": command, "workdir": workdir})
	result := resultBlock(id, output, isError)
	if details != nil {
		result.Details = marshalDetails(*details)
	}
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{callBlock(id, "bash", string(input))}},
		{Role: llm.RoleUser, Content: []llm.Block{result}},
	}
}

func exited(code int, command string) *tools.BashDetails {
	return &tools.BashDetails{Command: command, ExitCode: code}
}

func TestCompactionCarriesShellCommandsVerbatim(t *testing.T) {
	a := New(Config{Client: &summariser{reply: "summary that recalls no command"}, Registry: emptyRegistry()})
	test := "go test ./internal/billing/ -run TestMonthlyTotals"
	script := "set -e\nmake build"
	long := "echo " + strings.Repeat("x", rehydrateCommandBytes)
	quoting := `grep -n "] , in x" notes.txt`
	msgs := []llm.Message{llm.UserText("fix the billing totals")}
	for _, turn := range [][]llm.Message{
		bashTurn("b1", test, "", "--- FAIL: TestMonthlyTotals", true, exited(1, test)),
		bashTurn("b2", "go build ./...", "", "(no output)", false, exited(0, "go build ./...")),
		bashTurn("b3", "npm test", "web/app/[slug]", "ok", false, exited(0, "npm test")),
		bashTurn("b4", "sleep 999", "", "Command timed out after 120 seconds", true, &tools.BashDetails{Command: "sleep 999", ExitCode: -1, TimedOut: true}),
		bashTurn("b5", "npm run dev", "web", "Shell task shell_1 is running.", false, &tools.BashDetails{Command: "npm run dev", Background: true, Status: "running"}),
		bashTurn("b6", "ls missing", "nowhere", "workdir does not exist", true, nil),
		bashTurn("b7", "make lint", "", "Command aborted", true, exited(-1, "make lint")),
		bashTurn("b8", script, "", "ok", false, exited(0, script)),
		bashTurn("b9", long, "", "ok", false, exited(0, long)),
		bashTurn("b10", quoting, "", "(no output)", false, nil),
		// The same command again: listed once, where and how it last ran.
		bashTurn("b11", test, "", "ok", false, exited(0, test)),
	} {
		msgs = append(msgs, turn...)
	}
	a.SetMessages(append(msgs, assistantText(strings.Repeat("long progress narrative ", 2000))))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	note := a.Messages()[0].Content[0].Text
	want := []shellCommand{
		{"exit 0", "", test},
		{"ran", "", quoting},
		{"failed", "", "make lint"},
		{"failed", "nowhere", "ls missing"},
		{"background", "web", "npm run dev"},
		{"timed out", "", "sleep 999"},
		{"exit 0", "web/app/[slug]", "npm test"},
		{"exit 0", "", "go build ./..."},
	}
	lines := make([]string, len(want))
	for i, c := range want {
		lines[i] = c.line()
	}
	if !strings.Contains(note, restoredCommandsHeader+"\n"+strings.Join(lines, "\n")+restoredStateClose) {
		t.Fatalf("command list:\n%s", note)
	}
	if strings.Contains(note, "make build") || strings.Contains(note, "xxxx") || strings.Contains(note, "[exit 1]") {
		t.Fatalf("listed a script, an overlong command or the superseded failure:\n%s", note)
	}
	// The note reads back as written, brackets and commas inside the
	// workdir and the command included.
	if got := previousRestoredState(a.Messages()).commands; !slices.Equal(got, want) {
		t.Fatalf("read back:\n got %q\nwant %q", got, want)
	}
}

// Commands one compaction listed survive the next, behind newer ones and
// with a newer run's status, and what the verbatim tail shows is not repeated.
func TestRepeatedCompactionCarriesShellCommands(t *testing.T) {
	a := New(Config{Client: &summariser{reply: "summary"}, Registry: emptyRegistry()})
	test := "go test ./internal/billing/ -run TestMonthlyTotals"
	msgs := []llm.Message{llm.UserText("fix the billing totals")}
	msgs = append(msgs, bashTurn("b1", "go vet ./...", "", "(no output)", false, exited(0, "go vet ./..."))...)
	msgs = append(msgs, bashTurn("b2", "git log -3 --oneline", "", "abc fix", false, exited(0, "git log -3 --oneline"))...)
	msgs = append(msgs, bashTurn("b3", test, "", "FAIL", true, exited(1, test))...)
	a.SetMessages(append(msgs, assistantText(strings.Repeat("narrative ", 4000))))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	msgs = a.Messages()
	msgs = append(msgs, bashTurn("b4", "go vet ./...", "", "vet: bad", true, exited(2, "go vet ./..."))...)
	msgs = append(msgs, bashTurn("b5", "git diff --stat", "", "1 file changed", false, exited(0, "git diff --stat"))...)
	msgs = append(msgs, assistantText(strings.Repeat("more ", 6000)))
	cut := len(msgs)
	msgs = append(msgs, bashTurn("b6", test, "", "ok", false, exited(0, test))...)
	msgs = append(msgs, assistantText("done"))
	a.SetMessages(msgs)
	var keep int64
	for i := cut; i < len(msgs); i++ {
		keep += llm.EstimateTokens(msgs[i : i+1])
	}
	res, err := a.CompactKeep(context.Background(), keep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != len(msgs)-cut {
		t.Fatalf("tail kept %d messages, want %d", res.Kept, len(msgs)-cut)
	}
	note := a.Messages()[0].Content[0].Text
	want := restoredCommandsHeader + "\n- [exit 0] git diff --stat\n- [exit 2] go vet ./...\n- [exit 0] git log -3 --oneline" + restoredStateClose
	if !strings.Contains(note, want) || strings.Count(note, "<restored_state>") != 1 {
		t.Fatalf("second continuation's commands:\n%s", note)
	}
	if strings.Contains(note, test) {
		t.Fatalf("repeated a command the tail shows:\n%s", note)
	}
}

func TestShellCommandListIsBoundedNewestFirst(t *testing.T) {
	a := New(Config{Client: &summariser{reply: "s"}, Registry: emptyRegistry()})
	msgs := []llm.Message{llm.UserText("x")}
	for i := range rehydrateMaxCommands + 3 {
		command := fmt.Sprintf("echo %02d", i)
		msgs = append(msgs, bashTurn(fmt.Sprintf("b%d", i), command, "", "ok", false, exited(0, command))...)
	}
	a.SetMessages(append(msgs, assistantText(strings.Repeat("y", 30000))))
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := previousRestoredState(a.Messages()).commands
	if len(got) != rehydrateMaxCommands || got[0].command != fmt.Sprintf("echo %02d", rehydrateMaxCommands+2) || got[len(got)-1].command != "echo 03" {
		t.Fatalf("bounded list: %q", got)
	}
}
