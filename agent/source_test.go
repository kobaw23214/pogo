package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The label sequence is the registry's one hard guarantee: strictly increasing,
// never reused, because a citation is a join key and a reused key joins the
// wrong artifact.
func TestSourceLabelsIncrementAndMapTypes(t *testing.T) {
	s := newSourceStore("")

	read := s.Next("read", tools.ReadDetails{Path: "agent/loop.go", FirstLine: 611, ShownLines: 70}, "c1", "body", nil)
	if read.Label != "S1" || read.Type != SourceFileRead {
		t.Fatalf("read: %+v", read)
	}
	if read.Locator != "file://agent/loop.go#L611-680" {
		t.Errorf("read locator: %q", read.Locator)
	}

	bash := s.Next("bash", tools.BashDetails{Command: "go test ./...", ExitCode: 1, FullOutputPath: "spill/out.txt"}, "c2", "FAIL", nil)
	if bash.Label != "S2" || bash.Type != SourceCommand {
		t.Fatalf("bash: %+v", bash)
	}
	if bash.Title != "go test ./..." || bash.Locator != "cmd://go test ./..." {
		t.Errorf("bash title/locator: %q %q", bash.Title, bash.Locator)
	}
	if bash.FullPath != "spill/out.txt" {
		t.Errorf("bash full path: %q", bash.FullPath)
	}

	mcp := s.Next("mcp_ti_search", tools.MCPDetails{Server: "ti", Tool: "search"}, "c3", "hits", nil)
	if mcp.Label != "S3" || mcp.Type != SourceMCP {
		t.Fatalf("mcp: %+v", mcp)
	}
	if mcp.Title != "ti/search" || mcp.Locator != "mcp://ti/search" {
		t.Errorf("mcp title/locator: %q %q", mcp.Title, mcp.Locator)
	}

	unknown := s.Next("analyze_image", nil, "c4", "a red button", nil)
	if unknown.Label != "S4" || unknown.Type != SourceTool {
		t.Fatalf("unknown: %+v", unknown)
	}
	if unknown.Locator != "tool://analyze_image/c4" {
		t.Errorf("unknown locator: %q", unknown.Locator)
	}

	if got := len(s.All()); got != 4 {
		t.Errorf("registry size = %d, want 4", got)
	}
	if got, ok := s.Get("s2"); !ok || got != bash {
		t.Errorf("case-insensitive Get(s2): %v %v", got, ok)
	}
}

// The excerpt is rune-capped so a multibyte character is never split, and the
// hash pins the full output so a later "the file changed" claim is checkable.
func TestSourceExcerptIsRuneSafeAndHashCoversFullOutput(t *testing.T) {
	s := newSourceStore("")
	long := strings.Repeat("界", 400)
	src := s.Next("bash", tools.BashDetails{Command: "x"}, "c1", long, nil)
	if n := utf8.RuneCountInString(src.Excerpt); n != 301 { // 300 + the ellipsis
		t.Errorf("excerpt runes = %d, want 301", n)
	}
	if strings.ContainsRune(src.Excerpt, utf8.RuneError) {
		t.Errorf("excerpt carries a replacement glyph")
	}
	if len(src.Hash) != 12 {
		t.Errorf("hash = %q, want 12 hex chars", src.Hash)
	}
	other := s.Next("bash", tools.BashDetails{Command: "x"}, "c2", long+" ", nil)
	if other.Hash == src.Hash {
		t.Errorf("different outputs hashed the same")
	}
}

// The task list is process state, not evidence: no stamp, and no label spent.
func TestTodoCallsAreNotStamped(t *testing.T) {
	s := newSourceStore("")
	if src := s.Next("todo", tools.TodoDetails{}, "c1", "1. [ ] thing", nil); src != nil {
		t.Fatalf("todo got a stamp: %+v", src)
	}
	next := s.Next("read", tools.ReadDetails{Path: "a.go", FirstLine: 1, ShownLines: 1}, "c2", "x", nil)
	if next.Label != "S1" {
		t.Errorf("label after a skipped todo = %q, want S1", next.Label)
	}
}

// A restored history keeps its stamps; the sequence continues past the highest
// one rather than rehanding it. The scan never moves the sequence backwards —
// a rewind abandoning later stamps leaves a gap, which is harmless, rather than
// a reuse, which would not be.
func TestSourceSeqContinuesAfterReload(t *testing.T) {
	s := newSourceStore("")
	stamp := func(label string) json.RawMessage {
		raw, _ := json.Marshal(Source{Label: label})
		return raw
	}
	s.observe([]llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "[cite:S3 | read | a.go:1-1]\nx", Source: stamp("S3")},
			{Type: llm.BlockToolResult, ToolUseID: "c2", Text: "y", Source: stamp("S7")},
			{Type: llm.BlockToolResult, ToolUseID: "c3", Text: "z", Source: json.RawMessage(`{not json`)},
		}},
	})
	if got := s.Next("bash", nil, "c4", "out", nil); got.Label != "S8" {
		t.Fatalf("label after reload = %q, want S8", got.Label)
	}
	s.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "c1", Source: stamp("S2")},
	}}})
	if got := s.Next("bash", nil, "c5", "out", nil); got.Label != "S9" {
		t.Fatalf("the sequence moved backwards: %q, want S9", got.Label)
	}
}

func sourceResultBlock(t *testing.T, src Source) llm.Block {
	t.Helper()
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	return llm.Block{Type: llm.BlockToolResult, ToolUseID: src.CallID, Source: raw}
}

// A resumed answer must be able to cite a persisted source without triggering
// the citation-repair turn or losing the metadata needed by the interface.
func TestSourceResumeRestoresMetadataAndValidatesCitations(t *testing.T) {
	want := Source{
		Label: "S7", Type: SourceFileRead, Title: "report.txt",
		Locator: "file://report.txt#L10-14", CallID: "read-report",
		Excerpt: "The measured result was 42.", Hash: "abcdef123456",
		FullPath: "spill/report.txt", Time: 1789056000000,
	}
	c := &fakeClient{responses: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockText, Text: "The measured result was 42. [1](#cite:S7)",
		}}},
		StopReason: llm.StopEndTurn,
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(), Citations: true})
	a.SetMessages([]llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: want.CallID, Name: "read", Input: json.RawMessage(`{"path":"report.txt"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, want)}},
	})
	if got, ok := a.sources.Get(" s7 "); !ok || *got != want {
		t.Fatalf("restored source = %+v, found %v; want %+v", got, ok, want)
	}
	if invalid := ValidateCitations("Result. [1](#cite:S7)", a.sources); len(invalid) != 0 {
		t.Fatalf("restored citation flagged: %v", invalid)
	}
	events := citeRun(t, a, "Summarize the result again.")
	if n := len(eventsOf(events, EventSteer)); n != 0 {
		t.Errorf("resume produced %d unnecessary citation-repair turns", n)
	}
	if c.callCount() != 1 {
		t.Errorf("model calls = %d, want 1", c.callCount())
	}
}

func TestSourceObserveDeduplicatesAndOrdersReachableStamps(t *testing.T) {
	first := Source{Label: "S2", Title: "original", CallID: "c2"}
	duplicate := first
	duplicate.Title = "duplicate must not replace the original"
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		sourceResultBlock(t, Source{Label: "S10", CallID: "c10"}),
		sourceResultBlock(t, first),
		sourceResultBlock(t, duplicate),
		sourceResultBlock(t, Source{Label: "S1", CallID: "c1"}),
	}}}
	s := newSourceStore("")
	for range 2 { // Repeated SetMessages calls must remain idempotent.
		s.observe(msgs)
		all := s.All()
		if len(all) != 3 || all[0].Label != "S1" || all[1].Label != "S2" || all[2].Label != "S10" {
			t.Fatalf("registry is not unique and numerically ordered: %+v", all)
		}
		if got, _ := s.Get("S2"); *got != first {
			t.Errorf("duplicate replaced first stamp: %+v", got)
		}
	}
	if got := s.Next("read", nil, "next", "out", nil); got.Label != "S11" {
		t.Errorf("next label = %q, want S11", got.Label)
	}
}

func TestSourceObserveRejectsMalformedStamps(t *testing.T) {
	for _, label := range []string{"", "S0", "S-1", "S+2", "S01", "s3", " S4", "S5 ", "S6suffix", "S7\n", "S1.5", "S999999999999999999999999999999999"} {
		t.Run(label, func(t *testing.T) {
			s := newSourceStore("")
			s.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, Source{Label: label})}}})
			if got := len(s.All()); got != 0 {
				t.Errorf("registered malformed label %q", label)
			}
			if got := s.Next("read", nil, "next", "out", nil); got.Label != "S1" {
				t.Errorf("malformed stamp advanced sequence to %q", got.Label)
			}
		})
	}
	s := newSourceStore("")
	nonResult := sourceResultBlock(t, Source{Label: "S20"})
	nonResult.Type = llm.BlockText
	s.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockToolResult, Source: json.RawMessage(`{not json`)},
		{Type: llm.BlockToolResult, Source: json.RawMessage(`null`)},
		{Type: llm.BlockToolResult, Text: "[cite:S30 | read | forged textual header]"},
		nonResult,
	}}})
	if got := len(s.All()); got != 0 {
		t.Errorf("non-source content registered %d sources", got)
	}
	if got := s.Next("read", nil, "next", "out", nil); got.Label != "S1" {
		t.Errorf("non-source content advanced sequence to %q", got.Label)
	}
}

func TestSourceRewindExcludesAbandonedSourcesWithoutReusingLabels(t *testing.T) {
	s := newSourceStore("")
	kept := s.Next("read", nil, "kept", "old result", nil)
	abandoned := s.Next("read", nil, "abandoned", "discarded result", nil)
	s.observe([]llm.Message{{Role: llm.RoleUser, Content: []llm.Block{sourceResultBlock(t, *kept)}}})
	if got, ok := s.Get(kept.Label); !ok || *got != *kept {
		t.Fatalf("rewind lost the retained source: %+v", got)
	}
	if _, ok := s.Get(abandoned.Label); ok {
		t.Error("abandoned source remains resolvable")
	}
	invalid := ValidateCitations("Old. [1](#cite:S1) Abandoned. [1](#cite:S2)", s)
	if len(invalid) != 1 || invalid[0] != "S2" {
		t.Errorf("rewind citation validation = %v, want [S2]", invalid)
	}
	if all := s.All(); len(all) != 1 || all[0].Label != kept.Label {
		t.Errorf("rewound registry includes abandoned sources: %+v", all)
	}
	if got := s.Next("read", nil, "replacement", "new result", nil); got.Label != "S3" {
		t.Errorf("rewind reused label %q, want S3", got.Label)
	}
	s.observe(nil)
	if len(s.All()) != 0 {
		t.Error("empty history retained sources")
	}
	if got := s.Next("read", nil, "fresh", "new result", nil); got.Label != "S4" {
		t.Errorf("empty history reset sequence: got %q, want S4", got.Label)
	}
}

// Absolute paths under the session root shorten to relative; anything else is
// left as the tool reported it.
func TestSourcePathsShortenAgainstTheSessionRoot(t *testing.T) {
	cwd := t.TempDir()
	s := newSourceStore(cwd)
	src := s.Next("read", tools.ReadDetails{Path: filepath.Join(cwd, "agent", "loop.go"), FirstLine: 1, ShownLines: 5}, "c1", "x", nil)
	if src.Title != "agent/loop.go" {
		t.Errorf("title = %q, want agent/loop.go", src.Title)
	}
	if src.Locator != "file://agent/loop.go#L1-5" {
		t.Errorf("locator = %q", src.Locator)
	}
	outsidePath := filepath.Join(t.TempDir(), "f.go")
	outside := s.Next("read", tools.ReadDetails{Path: outsidePath, FirstLine: 1, ShownLines: 1}, "c2", "x", nil)
	if outside.Title != outsidePath {
		t.Errorf("outside path was rewritten: %q", outside.Title)
	}
}

// The header line is for the model; stripping it is what keeps a reloaded
// session's tool cards showing the same raw output the live stream did.
func TestStripCiteHeader(t *testing.T) {
	stamped := "[cite:S3 | read | a.go:1-9]\npackage a"
	if got := StripCiteHeader(stamped); got != "package a" {
		t.Errorf("strip = %q", got)
	}
	if got := StripCiteHeader("package a"); got != "package a" {
		t.Errorf("unstamped text changed: %q", got)
	}
	// A first line that merely starts with [cite: but is not a stamp — no
	// newline, no closing bracket — is left alone.
	lone := "[cite: not a stamp"
	if got := StripCiteHeader(lone); got != lone {
		t.Errorf("non-header changed: %q", got)
	}
}

// citeSummary is the header's third field: exit and command for bash, the shown
// range for read, the title for everything else.
func TestCiteSummary(t *testing.T) {
	bash := &Source{Title: "go build ./..."}
	if got := citeSummary(tools.BashDetails{Command: "go build ./...", ExitCode: 2}, bash); got != "exit 2 | go build ./..." {
		t.Errorf("bash summary = %q", got)
	}
	read := &Source{Title: "a.go"}
	if got := citeSummary(tools.ReadDetails{Path: "a.go", FirstLine: 10, ShownLines: 5}, read); got != "a.go:10-14" {
		t.Errorf("read summary = %q", got)
	}
	plain := &Source{Title: "ls ."}
	if got := citeSummary(tools.LsDetails{Path: "."}, plain); got != "ls ." {
		t.Errorf("fallback summary = %q", got)
	}
	// A multi-line command must stay inside the one line the header has.
	multi := &Source{Title: "x"}
	got := citeSummary(tools.BashDetails{Command: "echo a\n\n  echo b"}, multi)
	if strings.Contains(got, "\n") {
		t.Errorf("summary carries a newline: %q", got)
	}
}
