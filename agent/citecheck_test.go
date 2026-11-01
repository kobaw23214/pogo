package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func citedStore(t *testing.T, labels ...string) *SourceStore {
	t.Helper()
	s := newSourceStore("")
	for range labels {
		s.Next("bash", nil, "c", "out", nil)
	}
	return s
}

func TestValidateCitationsAcceptsRegisteredLabels(t *testing.T) {
	s := citedStore(t, "S1", "S2", "S3")
	// Mixed case is normalized; two anchors side by side must both be read.
	if invalid := ValidateCitations("It reads config first. [1](#cite:S1) [1](#cite:s3)", s); len(invalid) != 0 {
		t.Errorf("registered labels flagged: %v", invalid)
	}
}

func TestValidateCitationsFlagsUnregisteredLabelsOnce(t *testing.T) {
	s := citedStore(t, "S1")
	invalid := ValidateCitations("A. [1](#cite:S9)\nB. [1](#cite:S9) [1](#cite:s9)", s)
	if len(invalid) != 1 || invalid[0] != "S9" {
		t.Errorf("invalid = %v, want [S9] (deduplicated, normalized)", invalid)
	}
}

// An anchor inside code is the model *showing* the format — the citation
// section itself does — and must not be read as citing.
func TestValidateCitationsIgnoresCodeSpans(t *testing.T) {
	s := citedStore(t, "S1")
	text := "Like this:\n\n```markdown\nfact. [1](#cite:S9)\n```\n\nor inline `[1](#cite:S8)` here.\n"
	if invalid := ValidateCitations(text, s); len(invalid) != 0 {
		t.Errorf("code-span anchors flagged: %v", invalid)
	}
}

// Out-of-shape labels fail too: S0 is below the sequence and S99999 is past
// the plausible range, and both fail before the registry is even asked.
func TestValidateCitationsRejectsOutOfRangeLabels(t *testing.T) {
	s := citedStore(t, "S1")
	invalid := ValidateCitations("A. [1](#cite:S0) B. [1](#cite:S99999)", s)
	if len(invalid) != 2 {
		t.Errorf("invalid = %v, want [S0 S99999]", invalid)
	}
}

// Two anchors with no space between them are two anchors, not one greedy match.
func TestValidateCitationsAdjacentAnchorsBothRead(t *testing.T) {
	s := citedStore(t, "S1")
	invalid := ValidateCitations("A. [1](#cite:S1)[1](#cite:S7)", s)
	if len(invalid) != 1 || invalid[0] != "S7" {
		t.Errorf("invalid = %v, want [S7]", invalid)
	}
}

// A made-up label on a non-numeric display escapes citeAnchorRe entirely; the
// wide pass is what sees [p1](#cite:p1).
func TestValidateCitationsFlagsInventedPageLabel(t *testing.T) {
	s := citedStore(t, "S1")
	invalid := ValidateCitations("See the table. [p1](#cite:p1)", s)
	if len(invalid) != 1 || invalid[0] != "P1" {
		t.Errorf("invalid = %v, want [P1]", invalid)
	}
}

// Any digit display is fine — the number carries no meaning by contract.
func TestValidateCitationsAcceptsNumericDisplay(t *testing.T) {
	s := citedStore(t, "S1")
	if invalid := ValidateCitations("A. [2](#cite:S1)", s); len(invalid) != 0 {
		t.Errorf("registered label with numeric display flagged: %v", invalid)
	}
}

// ...but the display must be digits even when the label is real.
func TestValidateCitationsFlagsNonNumericDisplay(t *testing.T) {
	s := citedStore(t, "S1")
	invalid := ValidateCitations("A. [p1](#cite:S1)", s)
	if len(invalid) != 1 || invalid[0] != "S1" {
		t.Errorf("invalid = %v, want [S1]", invalid)
	}
}

// --- page markers ---

func TestFindPageMarkersHitsBareForms(t *testing.T) {
	text := "Intro [p1] then [P2], a range [p3-5] and [p4 – 6]. Repeat [p1]."
	got := FindPageMarkers(text)
	want := []string{"[p1]", "[P2]", "[p3-5]", "[p4 – 6]"}
	if len(got) != len(want) {
		t.Fatalf("markers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("markers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A real anchor never starts with p; an anchored page label is the wide pass's
// problem, not a bare marker; and markers inside code are the model showing,
// not citing — same rule as anchors.
func TestFindPageMarkersSkipsAnchorsProseAndCode(t *testing.T) {
	text := "Real cite [1](#cite:S3), anchored page [p1](#cite:S3), prose ref [1].\n" +
		"Inline `[p9]` and fenced:\n```\n[p8]\n```\n"
	if got := FindPageMarkers(text); len(got) != 0 {
		t.Errorf("markers = %v, want none", got)
	}
}

func TestPageMarkerSteerNoticeListsMarkers(t *testing.T) {
	n := pageMarkerSteerNotice([]string{"[p1]", "[p3-5]"})
	if !strings.Contains(n, "[pogo] citation check:") ||
		!strings.Contains(n, "[p1]") || !strings.Contains(n, "[p3-5]") {
		t.Errorf("notice = %q", n)
	}
}

// --- the loop wiring ---

// citeRun drives one scripted conversation and returns the events.
func citeRun(t *testing.T, a *Agent, prompt string) []Event {
	t.Helper()
	return drain(a.Run(context.Background(), prompt))
}

func eventsOf(events []Event, kind EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// With citations on, a finished call is stamped: the event carries the Source,
// the persisted block's text opens with the header, and the event's ToolOutput
// stays raw — the stamp is for the model, not the tool card.
func TestToolResultsAreStampedWhenCitationsOn(t *testing.T) {
	read := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{
			Text:    "package main",
			Details: tools.ReadDetails{Path: "main.go", FirstLine: 1, ShownLines: 1, TotalLines: 1},
		}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("read")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(read), Citations: true})

	events := citeRun(t, a, "go")

	ends := eventsOf(events, EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("tool_end events = %d, want 1", len(ends))
	}
	src := ends[0].Source
	if src == nil {
		t.Fatal("tool_end carries no Source")
	}
	if src.Label != "S1" || src.Type != SourceFileRead || src.Title != "main.go" {
		t.Errorf("source = %+v", src)
	}
	if ends[0].ToolOutput != "package main" {
		t.Errorf("event output stamped too: %q", ends[0].ToolOutput)
	}

	var block llm.Block
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser && len(m.Content) > 0 && m.Content[0].Type == llm.BlockToolResult {
			block = m.Content[0]
		}
	}
	want := "[cite:S1 | read | main.go:1-1]\npackage main"
	if block.Text != want {
		t.Errorf("block text = %q, want %q", block.Text, want)
	}
	var persisted Source
	if err := json.Unmarshal(block.Source, &persisted); err != nil || persisted.Label != "S1" {
		t.Errorf("block Source = %s, err %v", block.Source, err)
	}
}

// Off means off: no stamp, no header, no registry, no validation — the prompts
// and blocks are byte-identical to what they always were.
func TestCitationsOffLeavesNoTrace(t *testing.T) {
	tool := &fakeTool{name: "bash", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "hi", Details: tools.BashDetails{Command: "echo hi"}}, nil
	}}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("bash"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "done [1](#cite:S9)"},
		}}, StopReason: llm.StopEndTurn},
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	events := citeRun(t, a, "go")

	if src := eventsOf(events, EventToolEnd)[0].Source; src != nil {
		t.Errorf("stamped while off: %+v", src)
	}
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				if b.Text != "hi" || len(b.Source) != 0 {
					t.Errorf("block = %q source %s", b.Text, b.Source)
				}
			}
		}
	}
	// The bogus citation in the final answer must not trigger a retry either.
	if n := len(eventsOf(events, EventSteer)); n != 0 {
		t.Errorf("steer events while off = %d, want 0", n)
	}
	if c.callCount() != 2 {
		t.Errorf("model calls = %d, want 2", c.callCount())
	}
}

// A final answer citing a label the session never produced gets exactly one
// rewrite request, on the steering path, like the task-list closing reminder.
func TestInvalidCitationTriggersOneRetry(t *testing.T) {
	bad := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "The sky is green. [1](#cite:S9)"},
	}}, StopReason: llm.StopEndTurn}
	good := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "The sky is blue."},
	}}, StopReason: llm.StopEndTurn}
	c := &fakeClient{responses: []llm.Response{bad, good}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&fakeTool{name: "x"}), Citations: true})

	events := citeRun(t, a, "go")

	steers := eventsOf(events, EventSteer)
	if len(steers) != 1 {
		t.Fatalf("steer events = %d, want 1", len(steers))
	}
	if !strings.Contains(steers[0].Text, "citation check") || !strings.Contains(steers[0].Text, "S9") {
		t.Errorf("steer text = %q", steers[0].Text)
	}
	if c.callCount() != 2 {
		t.Errorf("model calls = %d, want 2", c.callCount())
	}
	// The retry notice entered the conversation as a user message, so the model
	// saw it on the second call.
	last := c.lastSeen()
	if got := last[len(last)-1].Text(); !strings.Contains(got, "citation check") {
		t.Errorf("retry prompt's last message = %q", got)
	}
}

// The retry is spent once. A failed rewrite keeps the original answer rather
// than introducing another invented source or spending another model call.
func TestSecondInvalidAnswerRetainsOriginal(t *testing.T) {
	bad := func(label string) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "claim [1](#cite:" + label + ")"},
		}}, StopReason: llm.StopEndTurn}
	}
	c := &fakeClient{responses: []llm.Response{bad("S9"), bad("S8")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&fakeTool{name: "x"}), Citations: true})

	events := citeRun(t, a, "go")

	if n := len(eventsOf(events, EventSteer)); n != 1 {
		t.Errorf("steer events = %d, want 1 (the retry is spent once)", n)
	}
	if c.callCount() != 2 {
		t.Errorf("model calls = %d, want 2", c.callCount())
	}
	if end := eventsOf(events, EventAgentEnd); len(end) != 1 || end[0].Err != nil {
		t.Errorf("agent_end = %+v", end)
	}
	if got := a.messages[len(a.messages)-1].Text(); got != "claim [1](#cite:S9)" {
		t.Errorf("failed rewrite replaced original answer: %q", got)
	}
}

// Bare page markers ride the same gate as bad labels: one steer, one retry.
func TestPageMarkersTriggerOneRetry(t *testing.T) {
	bad := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "The vendor list is on page 3 [p3]."},
	}}, StopReason: llm.StopEndTurn}
	good := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "The vendor list appears on page 3. [1](#cite:S1)"},
	}}, StopReason: llm.StopEndTurn}
	c := &fakeClient{responses: []llm.Response{bad, good}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&fakeTool{name: "x"}), Citations: true})

	events := citeRun(t, a, "go")

	steers := eventsOf(events, EventSteer)
	if len(steers) != 1 {
		t.Fatalf("steer events = %d, want 1", len(steers))
	}
	if !strings.Contains(steers[0].Text, "citation check") || !strings.Contains(steers[0].Text, "[p3]") {
		t.Errorf("steer text = %q", steers[0].Text)
	}
	if c.callCount() != 2 {
		t.Errorf("model calls = %d, want 2", c.callCount())
	}
}

// A call that produced no output is still registered — the failure to produce
// anything is a fact — but no header is injected into an empty body.
func TestEmptyOutputIsRegisteredButNotStamped(t *testing.T) {
	tool := &fakeTool{name: "bash", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "", Details: tools.BashDetails{Command: "true"}}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("bash")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})

	events := citeRun(t, a, "go")

	if src := eventsOf(events, EventToolEnd)[0].Source; src == nil {
		t.Error("empty output was not registered")
	}
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.Text != "" {
				t.Errorf("empty output gained a header: %q", b.Text)
			}
		}
	}
}

func TestValidateCitationsRejectsIncorrectClosingBracket(t *testing.T) {
	if got := ValidateCitations("结论。 [1](#cite:S1]", citedStore(t, "S1")); !reflect.DeepEqual(got, []string{"S1"}) {
		t.Fatalf("malformed anchor accepted: %v", got)
	}
}

type citationClient struct {
	fakeClient
	schemas [][]llm.ToolSchema
	failAt  int
}

func (c *citationClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	c.schemas = append(c.schemas, append([]llm.ToolSchema(nil), schemas...))
	response, err := c.fakeClient.Stream(ctx, system, history, schemas, delta)
	if len(c.schemas) == c.failAt {
		return llm.Response{}, errors.New("citation test transport failure")
	}
	return response, err
}

func citationAnswer(text string) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}
}

func citationReadTool() *fakeTool {
	return &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: "The project has 10 commits.", Details: tools.ReadDetails{Path: "report.txt", FirstLine: 1, ShownLines: 1}}, nil
	}}
}

func TestMissingCitationsGetsOneTextOnlyRewrite(t *testing.T) {
	read := citationReadTool()
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		toolCalls("read"), citationAnswer("项目有 10 个提交。"), citationAnswer("项目有 10 个提交。 [1](#cite:S1)"),
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(read), Citations: true})
	events := citeRun(t, a, "总结项目")
	steers := eventsOf(events, EventSteer)
	if len(steers) != 1 || !strings.Contains(steers[0].Text, "no inline source citations") {
		t.Fatalf("missing-citation review = %+v", steers)
	}
	if c.callCount() != 3 || len(c.schemas[1]) == 0 || len(c.schemas[2]) != 0 {
		t.Fatalf("model calls or rewrite schema = %d / %+v", c.callCount(), c.schemas)
	}
	if len(read.callArgs()) != 1 || len(eventsOf(events, EventToolEnd)) != 1 {
		t.Fatal("citation rewrite repeated the evidence read")
	}
	if got := a.messages[len(a.messages)-1].Text(); got != "项目有 10 个提交。 [1](#cite:S1)" {
		t.Fatalf("final answer = %q", got)
	}
	// The original tool result and source metadata remain intact; only text
	// messages are added. Persistence can replay the same evidence snapshot.
	for _, message := range a.Messages() {
		for _, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			if block.Text != "[cite:S1 | read | report.txt:1-1]\nThe project has 10 commits." {
				t.Fatalf("evidence bytes changed: %q", block.Text)
			}
			var saved Source
			if err := json.Unmarshal(block.Source, &saved); err != nil {
				t.Fatal(err)
			}
			current, ok := a.sources.Get(saved.Label)
			if !ok || *current != saved {
				t.Fatal("source snapshot changed during rewriting")
			}
		}
	}
}

func TestCitationRewriteFailurePreservesAnswerWithoutExecutingTools(t *testing.T) {
	for _, mode := range []string{"tool", "transport", "empty", "truncated", "still-uncited", "still-invalid"} {
		t.Run(mode, func(t *testing.T) {
			original := "项目有 10 个提交。"
			repair := citationAnswer("")
			switch mode {
			case "tool":
				repair = toolCalls("write")
			case "truncated":
				repair = citationAnswer("项目有")
				repair.StopReason = llm.StopMaxTokens
			case "still-uncited":
				repair = citationAnswer(original)
			case "still-invalid":
				repair = citationAnswer(original + " [1](#cite:S999)")
			}
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
				toolCalls("read"), citationAnswer(original), repair,
			}}}
			if mode == "transport" {
				c.failAt = 3
			}
			write := &fakeTool{name: "write"}
			a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool(), write), Citations: true})
			events := citeRun(t, a, "总结项目")
			if c.callCount() != 3 || len(eventsOf(events, EventSteer)) != 1 {
				t.Fatalf("rewrite escaped its budget: %d calls, events %+v", c.callCount(), events)
			}
			if len(write.callArgs()) != 0 {
				t.Fatal("text-only correction executed a write")
			}
			for _, message := range a.Messages() {
				for _, call := range message.ToolCalls() {
					if call.Name == "write" {
						t.Fatal("rejected rewrite polluted transcript with an unpaired tool call")
					}
				}
			}
			if a.messages[len(a.messages)-1].Text() != original {
				t.Fatalf("original answer lost: %q", a.messages[len(a.messages)-1].Text())
			}
			if ends := eventsOf(events, EventAgentEnd); len(ends) != 1 || ends[0].Err != nil || ends[0].StopReason != llm.StopEndTurn {
				t.Fatalf("optional rewrite turned a completed task into an error: %+v", ends)
			}
		})
	}
}

func TestMissingCitationReviewSkipsAcknowledgementsQuestionsAndCode(t *testing.T) {
	for _, answer := range []string{"好的。", "收到！", "已完成。", "Thanks!", "done", "请问你想查看哪个项目？", "Which project?", "```go\npackage main\n```", "~~~md\n[1](#cite:S9)\n~~~", "`example`", `{"commits":10}`, `[{"commits":10}]`} {
		t.Run(answer, func(t *testing.T) {
			c := &fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(answer)}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
			events := citeRun(t, a, "看看")
			if c.callCount() != 2 || len(eventsOf(events, EventSteer)) != 0 {
				t.Fatalf("non-evidence answer forced through review: %q", answer)
			}
		})
	}
}

func TestMissingCitationReviewSkipsNonReadingFailedAndEmptyResults(t *testing.T) {
	for _, mode := range []string{"failed-read", "empty-read", "write", "command", "unknown", "browser-navigate", "browser-click", "browser-scroll", "browser-tabs", "browser-screenshot"} {
		t.Run(mode, func(t *testing.T) {
			name := "read"
			details := any(tools.ReadDetails{Path: "report.txt", FirstLine: 1, ShownLines: 1})
			input := json.RawMessage(`{}`)
			switch mode {
			case "write":
				name, details = "write", tools.WriteDetails{Path: "report.txt"}
			case "command":
				name, details = "bash", tools.BashDetails{Command: "true"}
			case "unknown":
				name, details = "custom", nil
			default:
				if strings.HasPrefix(mode, "browser-") {
					name, details = "browser", map[string]any{"url": "https://example.com", "title": "Example"}
					input = json.RawMessage(`{"action":"` + strings.TrimPrefix(mode, "browser-") + `"}`)
				}
			}
			tool := &fakeTool{name: name, run: func(context.Context, json.RawMessage) (tools.Result, error) {
				result := tools.Result{Text: "Some result", Details: details}
				if mode == "failed-read" {
					return result, errors.New("file unavailable")
				}
				if mode == "empty-read" {
					result.Text = ""
				}
				return result, nil
			}}
			call := toolCalls(name)
			call.Message.Content[0].Input = input
			c := &fakeClient{responses: []llm.Response{call, citationAnswer("操作已结束，具体内容仍需后续确认。")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})
			if events := citeRun(t, a, "处理一下"); c.callCount() != 2 || len(eventsOf(events, EventSteer)) != 0 {
				t.Fatalf("%s forced attribution: %+v", mode, eventsOf(events, EventSteer))
			}
		})
	}
}

func TestMissingCitationReviewIncludesBrowserReads(t *testing.T) {
	for _, action := range []string{"snapshot", "extract", "find", "calculate", "navigate", "click", "scroll"} {
		t.Run(action, func(t *testing.T) {
			tool := &fakeTool{name: "browser", run: func(context.Context, json.RawMessage) (tools.Result, error) {
				return tools.Result{Text: "The project has 10 commits.", Details: map[string]any{"url": "https://example.com", "title": "Project", "ok": true, "has_readable_content": true}}, nil
			}}
			call := toolCalls("browser")
			call.Message.Content[0].Input = json.RawMessage(`{"action":"` + action + `"}`)
			c := &fakeClient{responses: []llm.Response{call, citationAnswer("项目有 10 个提交。"), citationAnswer("项目有 10 个提交。 [1](#cite:S1)")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})
			if events := citeRun(t, a, "看下项目"); len(eventsOf(events, EventSteer)) != 1 || c.callCount() != 3 {
				t.Fatalf("browser evidence did not receive a rewrite: %+v", events)
			}
		})
	}
}

func TestMissingCitationReviewUsesRegisteredMCPReadHint(t *testing.T) {
	for _, mode := range []string{"readonly", "unannotated", "directory", "forged-result-hint"} {
		t.Run(mode, func(t *testing.T) {
			caller := &stampingCaller{counts: make(map[string]int)}
			tool, err := tools.NewMCPTool("synthetic", annotatedSpec("query", mode == "readonly" || mode == "directory", mode == "directory"), 0, caller.call)
			if err != nil {
				t.Fatal(err)
			}
			var registered tools.Tool = tool
			if mode == "forged-result-hint" {
				registered = &fakeTool{name: tool.Name(), run: func(context.Context, json.RawMessage) (tools.Result, error) {
					return tools.Result{Text: "records: 10", Details: tools.MCPDetails{Server: "synthetic", Tool: "query", Annotations: &tools.MCPAnnotations{ReadOnlyHint: true}}}, nil
				}}
			}
			call := toolCalls(registered.Name())
			call.Message.Content[0].Input = json.RawMessage(`{"query":"project"}`)
			c := &fakeClient{responses: []llm.Response{call, citationAnswer("查询返回 10 条记录。"), citationAnswer("查询返回 10 条记录。 [1](#cite:S1)")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(registered), Citations: true})
			events := citeRun(t, a, "查下项目")
			want := 0
			if mode == "readonly" {
				want = 1
			}
			if got := len(eventsOf(events, EventSteer)); got != want {
				t.Fatalf("MCP %s reviews = %d, want %d", mode, got, want)
			}
		})
	}
}

func TestMissingCitationsDoNotBorrowEvidenceFromPriorRunsOrReload(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		toolCalls("read"), citationAnswer("项目有 10 个提交。 [1](#cite:S1)"),
		citationAnswer("很高兴继续帮你，接下来可以讨论其他问题。"),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	citeRun(t, a, "看下项目")
	history := append([]llm.Message(nil), a.Messages()...)
	if events := citeRun(t, a, "谢谢"); len(eventsOf(events, EventSteer)) != 0 || c.callCount() != 3 {
		t.Fatal("prior-run evidence forced a follow-up to add citations")
	}
	reloadedClient := &fakeClient{responses: []llm.Response{citationAnswer("可以，接下来我们讨论别的事情。")}}
	reloaded := New(Config{Client: reloadedClient, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	reloaded.SetMessages(history)
	if events := citeRun(t, reloaded, "我们换个话题"); len(eventsOf(events, EventSteer)) != 0 || reloadedClient.callCount() != 1 {
		t.Fatal("reloaded history became current-task evidence")
	}
	if source, ok := reloaded.sources.Get("S1"); !ok || source.Excerpt != "The project has 10 commits." {
		t.Fatal("citation repair changed existing source metadata")
	}
}

func TestCitationRewriteHonorsTurnAndTokenLimits(t *testing.T) {
	for _, mode := range []string{"turn-limit", "token-limit"} {
		t.Run(mode, func(t *testing.T) {
			answer := citationAnswer("项目有 10 个提交。")
			answer.Usage.Output = 10
			c := &fakeClient{responses: []llm.Response{toolCalls("read"), answer}}
			config := Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true}
			if mode == "turn-limit" {
				config.MaxTurns = 2
			} else {
				config.TokenBudget = 10
			}
			a := New(config)
			events := citeRun(t, a, "总结项目")
			if c.callCount() != 2 {
				t.Fatal("citation repair exceeded the request budget")
			}
			if ends := eventsOf(events, EventAgentEnd); len(ends) != 1 || ends[0].Err != nil || ends[0].StopReason != llm.StopEndTurn {
				t.Fatalf("budget exhaustion lost the completed answer: %+v", ends)
			}
		})
	}
}

func TestCitationCheckIgnoresBothMarkdownFenceKinds(t *testing.T) {
	store := citedStore(t, "S1")
	text := "~~~~markdown\n[1](#cite:S9)\n~~~\n[p2]\n~~~~\n" +
		"````markdown\n[1](#cite:S8)\n```\n[p3]\n````\n实际结论。 [1](#cite:S1)"
	if invalid := ValidateCitations(text, store); len(invalid) != 0 {
		t.Fatalf("code samples treated as live citations: %v", invalid)
	}
	if markers := FindPageMarkers(text); len(markers) != 0 {
		t.Fatalf("code samples treated as live page markers: %v", markers)
	}
	if got := missingCitationNotice("~~~markdown\n[1](#cite:S1)\n~~~\n项目有 10 个提交。", []string{"S1"}, store); got == "" {
		t.Fatal("a citation sample inside a fence satisfied answer attribution")
	}
}

func TestCitationReviewExcludesRuntimeSkillBootstrapButKeepsUserFiles(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "pogo-builtin")
	t.Setenv("POGO_EMBEDDED_SKILLS_DIR", custom)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/opt/skills/browser-use/SKILL.md", false},
		{filepath.Join(cache, "pogo", "embedded-skills", "browser-use", "SKILL.md"), false},
		{filepath.Join(os.TempDir(), "pogo-embedded-skills", "browser-use", "references", "browser.md"), true},
		{filepath.Join(custom, "browser-use", "SKILL.md"), false},
		{"skill-runtime://package-id/SKILL.md", false},
		{"skill-runtime://package-id/references/report.md", true},
		{"SKILL.md", true},
		{"project/skills/browser-use/SKILL.md", true},
		{"/opt/skills-user/report.md", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			tool := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
				return tools.Result{Text: "Use snapshot to inspect page contents.", Details: tools.ReadDetails{Path: tc.path, ShownLines: 1}}, nil
			}}
			c := &fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer("网页已经打开，接下来可以直接进行操作。"), citationAnswer("网页已经打开，接下来可以直接进行操作。")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true, Cwd: "/work"})
			events := citeRun(t, a, "打开网页")
			if got := len(eventsOf(events, EventSteer)) > 0; got != tc.want {
				t.Fatalf("automatic review = %v, want %v", got, tc.want)
			}
			if invalid := ValidateCitations("这份技能说明要求先读取网页。 [1](#cite:S1)", a.sources); len(invalid) != 0 {
				t.Fatalf("bootstrap exclusion disabled explicit attribution: %v", invalid)
			}
		})
	}
}

func TestCitationReviewMultiReadRequiresMaterialOutsideBootstrap(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry(), Citations: true})
	for _, tc := range []struct {
		name  string
		files []tools.ReadFileDetails
		want  bool
	}{
		{"bootstrap only", []tools.ReadFileDetails{{Path: "/opt/skills/browser-use/SKILL.md", BodyLength: 20}}, false},
		{"bootstrap and unreadable report", []tools.ReadFileDetails{{Path: "/opt/skills/browser-use/SKILL.md", BodyLength: 20}, {Path: "report.md", Error: "not found"}}, false},
		{"bootstrap and empty report", []tools.ReadFileDetails{{Path: "/opt/skills/browser-use/SKILL.md", BodyLength: 20}, {Path: "report.md"}}, false},
		{"bootstrap and report", []tools.ReadFileDetails{{Path: "/opt/skills/browser-use/SKILL.md", BodyLength: 20}, {Path: "report.md", BodyLength: 12}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tools.ReadManyDetails{Files: tc.files})
			if got := a.citationFileRead(raw); got != tc.want {
				t.Fatalf("material read = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCitationReviewExcludesBlankAndFailedBrowserObservations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		url     string
		ok      bool
		content bool
	}{
		{"blank initialization", "about:blank", true, false},
		{"blank with runtime status", "about:blank", true, true},
		{"Chromium error page", "chrome-error://chromewebdata/", true, true},
		{"failed page read", "https://example.com", false, true},
		{"metadata only", "https://example.com", true, false},
		{"find without results", "https://example.com", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &fakeTool{name: "browser", run: func(context.Context, json.RawMessage) (tools.Result, error) {
				return tools.Result{Text: "runtime: native; page: " + tc.url + "\nNo matching elements or readable page text.",
					Details: map[string]any{"url": tc.url, "title": "browser", "ok": tc.ok, "has_readable_content": tc.content}}, nil
			}}
			call := toolCalls("browser")
			call.Message.Content[0].Input = json.RawMessage(`{"action":"snapshot"}`)
			if tc.name == "find without results" {
				call.Message.Content[0].Input = json.RawMessage(`{"action":"find","name":"absent"}`)
			}
			c := &fakeClient{responses: []llm.Response{call, citationAnswer("浏览器已就绪，可以继续操作。")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})
			events := citeRun(t, a, "打开网页")
			if c.callCount() != 2 || len(eventsOf(events, EventSteer)) != 0 {
				t.Fatal("status-only browser result forced an attribution review")
			}
		})
	}
}

func TestCitationReviewOnlySearchesWithMatchesTriggerMissingCheck(t *testing.T) {
	for _, matches := range []int{0, 2} {
		tool := &fakeTool{name: "find", run: func(context.Context, json.RawMessage) (tools.Result, error) {
			return tools.Result{Text: "Search completed.", Details: tools.FindDetails{Pattern: "*.md", Matches: matches, Scanned: 12}}, nil
		}}
		c := &fakeClient{responses: []llm.Response{toolCalls("find"), citationAnswer("搜索已经完成，可以继续处理文件。")}}
		a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})
		events := citeRun(t, a, "搜索文件")
		if reviewed := len(eventsOf(events, EventSteer)) > 0; reviewed != (matches > 0) {
			t.Fatalf("matches=%d received review=%v", matches, reviewed)
		}
	}
}

func TestCitationEvidenceScopeResetsOnHumanSteering(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		toolCalls("read"), citationAnswer("项目有 10 个提交。"),
		citationAnswer("可以，接下来我们讨论别的事情。"),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
	c.onStream = func(n int) {
		if n == 2 && !a.Steer("不用继续查项目，换个话题") {
			t.Fatal("human steering was not accepted")
		}
	}
	events := citeRun(t, a, "看下项目")
	if c.callCount() != 3 {
		t.Fatalf("steering calls = %d, want 3", c.callCount())
	}
	for _, event := range eventsOf(events, EventSteer) {
		if strings.Contains(event.Text, "[pogo] citation check:") {
			t.Fatal("previous task's reads forced citations into human's new topic")
		}
	}
}

func TestCitationRewriteCancellationPreservesTranscriptWithoutToolDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := "项目有 10 个提交。"
	c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{
		toolCalls("read"), citationAnswer(original), toolCalls("write"),
	}}}
	write := &fakeTool{name: "write"}
	a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool(), write), Citations: true})
	c.onStream = func(n int) {
		if n == 3 {
			cancel()
		}
	}
	events := drain(a.Run(ctx, "看下项目"))
	if len(write.callArgs()) != 0 || a.messages[len(a.messages)-1].Text() != original {
		t.Fatal("canceled optional rewrite executed a tool or lost the original answer")
	}
	if ends := eventsOf(events, EventAgentEnd); len(ends) != 1 || ends[0].StopReason != llm.StopAborted {
		t.Fatalf("cancellation outcome lost: %+v", ends)
	}
	for _, message := range a.Messages() {
		for _, call := range message.ToolCalls() {
			if call.Name == "write" {
				t.Fatal("canceled rewrite persisted an unpaired tool call")
			}
		}
	}
}

func TestCitationOpeningConfirmationDoesNotHideFollowingFacts(t *testing.T) {
	for _, tc := range []struct {
		answer string
		review bool
	}{
		{"已打开。", false},
		{"已打开 GitLab。", false},
		{"已打开 [GitLab](https://gitlab.example.com)。", false},
		{"Opened GitLab.", false},
		{"Opened gitlab.example.com.", false},
		{"Opened https://gitlab.example.com.", false},
		{"已打开 GitLab，项目有 10 个提交。", true},
		{"已打开 GitLab。项目有 10 个提交。", true},
		{"已打开 GitLab 项目有 10 个提交。", true},
		{"Opened GitLab. The project has 10 commits.", true},
		{"已打开 GitLab。\n- 项目有 10 个提交。", true},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			tool := &fakeTool{name: "browser", run: func(context.Context, json.RawMessage) (tools.Result, error) {
				return tools.Result{Text: "The project has 10 commits.", Details: map[string]any{
					"url": "https://gitlab.example.com", "title": "Project · GitLab", "ok": true, "has_readable_content": true,
				}}, nil
			}}
			call := toolCalls("browser")
			call.Message.Content[0].Input = json.RawMessage(`{"action":"navigate","url":"https://gitlab.example.com"}`)
			c := &fakeClient{responses: []llm.Response{call, citationAnswer(tc.answer), citationAnswer(tc.answer)}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Citations: true})
			events := citeRun(t, a, "打开 GitLab")
			if got := len(eventsOf(events, EventSteer)) > 0; got != tc.review {
				t.Fatalf("review = %v, want %v", got, tc.review)
			}
		})
	}
}

func TestCitationRewriteKeepsSubstantiveUncitedAnswersButRejectsMetaAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rewrite  string
		fallback bool
	}{
		{"different factual answer", "项目有 10 个提交，读取结果没有提供其他指标。", false},
		{"complete evidence limitation", "这份结果只记录提交数量，无法判断代码质量；没有直接证据支持质量方面的结论。", false},
		{"correction acknowledgement", "已根据来源重新整理，请参考。", true},
		{"short correction acknowledgement", "已补充引用。", true},
		{"short acknowledgement with valid label", "已补充引用。 [1](#cite:S1)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := "项目有 10 个提交。"
			c := &citationClient{fakeClient: fakeClient{responses: []llm.Response{toolCalls("read"), citationAnswer(original), citationAnswer(tc.rewrite)}}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(citationReadTool()), Citations: true})
			events := citeRun(t, a, "总结项目")
			want := tc.rewrite
			if tc.fallback {
				want = original
			}
			if got := a.messages[len(a.messages)-1].Text(); got != want {
				t.Fatalf("final text = %q, want %q", got, want)
			}
			if c.callCount() != 3 || len(eventsOf(events, EventSteer)) != 1 {
				t.Fatal("substantive evidence review exceeded its single correction call")
			}
		})
	}
}

func TestCitationOpeningConfirmationMatchesHostAndPort(t *testing.T) {
	store := newSourceStore("")
	store.Next("browser", map[string]any{"url": "http://gitlab.example.com:8929", "title": "GitLab"}, "navigate", "Project data", nil)
	for _, answer := range []string{"Opened gitlab.example.com:8929.", "已打开 gitlab.example.com:8929。"} {
		if notice := missingCitationNotice(answer, []string{"S1"}, store); notice != "" {
			t.Fatalf("bare hostname with port treated as factual prose: %q", answer)
		}
	}
	if notice := missingCitationNotice("Opened gitlab.example.com:8929. The project has 10 commits.", []string{"S1"}, store); notice == "" {
		t.Fatal("opening confirmation swallowed the factual sentence after it")
	}
}
