package analyze

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

// call is one tool_use block as a transcript stores it.
func call(id, name, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, name, input)
}

// result is the matching tool_result, carrying the duration the loop recorded.
func result(id string, ms int64, isError bool) string {
	return fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"text":"ok","is_error":%t,"duration_ms":%d}`,
		id, isError, ms)
}

// turn is one assistant message of tool calls and the user message with their
// results; results may be omitted when a test only needs the schedule.
type turn struct {
	calls, results []string
}

// transcript writes a session made of the given turns, optionally followed by
// extra raw records.
func transcript(t *testing.T, turns []turn, extra ...string) string {
	t.Helper()
	lines := []string{`{"id":"m","type":"meta","time":1,"meta":{"cwd":"/w","model":"m"}}`}
	for i, tr := range turns {
		lines = append(lines, fmt.Sprintf(
			`{"id":"a%d","type":"message","time":%d,"message":{"role":"assistant","content":[%s]}}`,
			i, 10+2*i, strings.Join(tr.calls, ",")))
		if len(tr.results) > 0 {
			lines = append(lines, fmt.Sprintf(
				`{"id":"u%d","type":"message","time":%d,"message":{"role":"user","content":[%s]}}`,
				i, 11+2*i, strings.Join(tr.results, ",")))
		}
	}
	lines = append(lines, extra...)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func analyzeTurns(t *testing.T, turns ...turn) *SessionStats {
	t.Helper()
	stats, err := AnalyzeSession(transcript(t, turns), Config{})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// The report follows the loop's segment rule, not the rule it used to assume. In
// read+read+bash the reads ran together and then bash ran, so nothing was stalled;
// the old report called both reads stalled because the batch held a sequential call.
// In read+bash+read+grep the bash call still isolates the first read.
func TestBatchScheduleFollowsTheSegmentRule(t *testing.T) {
	b := analyzeTurns(t,
		turn{calls: []string{
			call("1", "read", `{"path":"a"}`), call("2", "read", `{"path":"b"}`),
			call("3", "bash", `{"command":"go test ./..."}`),
		}},
		turn{calls: []string{
			call("4", "read", `{"path":"a"}`), call("5", "bash", `{"command":"make"}`),
			call("6", "read", `{"path":"b"}`), call("7", "grep", `{"pattern":"x"}`),
		}},
	).BatchSizes

	if b.DispatchSteps != 5 || b.Overlapped != 4 || b.Stalled != 1 {
		t.Errorf("steps/overlapped/stalled = %d/%d/%d, want 5/4/1", b.DispatchSteps, b.Overlapped, b.Stalled)
	}
	// Only the second bash stood between overlap-capable calls; the first one
	// came after both reads.
	if len(b.SplitBy) != 1 || b.SplitBy["bash"] != 1 {
		t.Errorf("SplitBy = %v, want bash 1", b.SplitBy)
	}
	if b.IfReadOnlyParallel != nil || len(b.UnknownMode) != 0 {
		t.Errorf("counterfactual %+v / unknown %v reported for plain built-ins", b.IfReadOnlyParallel, b.UnknownMode)
	}
}

// Some schedules depend on the arguments. A read-only subagent overlaps and an edit
// subagent does not, including when the model spells out action:"spawn", which a
// web session's schema offers. An Office read runs alone today; the counterfactual
// prices letting it overlap, which is what argument-aware scheduling would buy.
func TestScheduleReadsTheRecordedArguments(t *testing.T) {
	b := analyzeTurns(t,
		turn{calls: []string{
			call("1", "subagent", `{"task":"look","mode":"explore"}`),
			call("2", "subagent", `{"action":"spawn","task":"look too","mode":"explore"}`),
			call("3", "subagent", `{"task":"fix it","mode":"edit"}`),
		}},
		turn{calls: []string{
			call("4", "read", `{"path":"a"}`),
			call("5", tools.OfficeToolName, `{"action":"list"}`),
			call("6", "read", `{"path":"b"}`),
		}},
	).BatchSizes

	// The explore pair overlaps and the edit child runs alone: 2 steps. The Office
	// read separates the two reads: 3 steps, both reads stalled.
	if b.DispatchSteps != 5 || b.Overlapped != 2 || b.Stalled != 2 {
		t.Errorf("steps/overlapped/stalled = %d/%d/%d, want 5/2/2", b.DispatchSteps, b.Overlapped, b.Stalled)
	}
	if len(b.ReadOnlySequential) != 1 || b.ReadOnlySequential[tools.OfficeToolName] != 1 {
		t.Errorf("ReadOnlySequential = %v, want only the Office read", b.ReadOnlySequential)
	}
	cf := b.IfReadOnlyParallel
	if cf == nil || cf.DispatchSteps != 3 || cf.Overlapped != 5 || cf.Stalled != 0 {
		t.Errorf("counterfactual = %+v, want 3 steps, 5 overlapped, 0 stalled", cf)
	}
	if len(b.UnknownMode) != 0 {
		t.Errorf("UnknownMode = %v, want none: every schedule here is decidable", b.UnknownMode)
	}
}

// What a transcript cannot tell is counted, not guessed. An MCP tool's schedule
// rests on its server's annotations and a retained child's follow-up on that child's
// mode. Both are scheduled as sequential, which is what the loop does with any call
// it cannot prove may overlap, and the report says how many calls rest on that.
func TestUnknownSchedulesAreCountedNotGuessed(t *testing.T) {
	b := analyzeTurns(t,
		turn{calls: []string{
			call("1", "read", `{"path":"a"}`),
			call("2", "mcp_docs_search", `{"q":"x"}`),
			call("3", "read", `{"path":"b"}`),
			call("4", "subagent", `{"action":"continue","agent_id":"agent_0123456789abcdef0123456789abcdef","task":"more"}`),
		}},
	).BatchSizes

	if b.UnknownMode["mcp_docs_search"] != 1 || b.UnknownMode["subagent"] != 1 {
		t.Errorf("UnknownMode = %v, want the MCP call and the continuation", b.UnknownMode)
	}
	if b.Stalled != 2 || b.SplitBy["mcp_docs_search"] != 1 || len(b.SplitBy) != 1 {
		t.Errorf("stalled %d, SplitBy %v: want both reads stalled behind the MCP call", b.Stalled, b.SplitBy)
	}
}

// Every native tool needs an instance here, so that its schedule is read from the
// tool itself. A tool added to the catalog without one would be reported as unknown —
// honestly, but uselessly — so this fails instead.
func TestEveryNativeToolHasASchedule(t *testing.T) {
	names := []string{
		// Registered by hosts but absent from the Agent editor's catalog.
		tools.InformationToolName, tools.OfficeToolName, tools.MCPDescribeName, "subagent_tasks",
		tools.RequestUserInputName, tools.RequestUserInputAsyncName,
		// Persisted legacy grants that older transcripts still call.
		tools.ShortcutSearchName, tools.ShortcutReadName, tools.ShortcutCreateName, tools.ShortcutListName,
		tools.ShortcutFolderCreateName, tools.ShortcutUpdateName, tools.ShortcutFolderRenameName, tools.ShortcutMoveName,
		tools.WorkflowDiscoverName, tools.WorkflowReadName, tools.WorkflowValidateName, tools.WorkflowEditName,
		tools.WorkflowTestName, tools.WorkflowRunName, tools.WorkflowPublishName,
	}
	for _, info := range tools.BuiltinRuntimeCatalog() {
		names = append(names, info.Name)
	}
	for _, name := range names {
		if _, ok := nativeTools[name]; !ok {
			t.Errorf("native tool %q has no instance in nativeTools; its calls would be reported as unknown", name)
		}
	}
}

// The time figures come from the durations recorded on each tool_result. An
// overlapped segment costs its longest call, a call that runs alone costs itself, and
// at most agent.DefaultToolConcurrency calls are in flight at once. The Tool Timing
// section is filled from the same durations, because current writers no longer
// append per-turn tool stats.
func TestToolTimeComesFromResultDurations(t *testing.T) {
	var reads, readResults []string
	for i := range 10 {
		id := fmt.Sprintf("r%d", i)
		reads = append(reads, call(id, "read", `{"path":"a"}`))
		readResults = append(readResults, result(id, 100, false))
	}
	stats := analyzeTurns(t,
		// Ten 100ms reads with eight slots: 200ms.
		turn{calls: reads, results: readResults},
		// grep and read overlap (300ms), the Office read runs alone (100ms), then
		// bash (1s). If the Office read could overlap: 300ms + 1s.
		turn{
			calls: []string{
				call("g", "grep", `{"pattern":"x"}`), call("x", "read", `{"path":"b"}`),
				call("o", tools.OfficeToolName, `{"action":"list"}`), call("b", "bash", `{"command":"make"}`),
			},
			results: []string{result("g", 300, false), result("x", 100, false), result("o", 100, false), result("b", 1000, true)},
		},
	)
	b := stats.BatchSizes
	if b.ToolTimeMS != 2500 || b.EstimatedWallMS != 1600 {
		t.Errorf("tool time %dms, wall %dms; want 2500 and 1600", b.ToolTimeMS, b.EstimatedWallMS)
	}
	if cf := b.IfReadOnlyParallel; cf == nil || cf.EstimatedWallMS != 1500 || cf.DispatchSteps != 3 {
		t.Errorf("counterfactual = %+v, want 1500ms over 3 steps", cf)
	}
	if b.DispatchSteps != 4 || b.Overlapped != 12 || b.Stalled != 0 {
		t.Errorf("steps/overlapped/stalled = %d/%d/%d, want 4/12/0", b.DispatchSteps, b.Overlapped, b.Stalled)
	}

	tt := stats.ToolTiming
	if tt.TotalCalls != 14 || tt.TotalDuration != 2500 || tt.MaxDuration != 1000 || tt.MinDuration != 100 {
		t.Errorf("timing = %+v, want 14 calls, 2500ms total, 1000 max, 100 min", tt)
	}
	if read := tt.ByTool["read"]; read.CallCount != 11 || read.AverageDuration != 100 {
		t.Errorf("read timing = %+v, want 11 calls averaging 100ms", read)
	}
	if bash := tt.ByTool["bash"]; bash.ErrorCount != 1 {
		t.Errorf("bash errors = %d, want the failed call counted", bash.ErrorCount)
	}

	out := FormatText(stats)
	for _, want := range []string{
		"Dispatch steps: 4 for 14 calls",
		"Overlapped with a sibling: 12 call(s)",
		"Read-only calls scheduled alone: docs_office 1",
		"If those could overlap: 3 steps, 13 overlapped, 0 serialized",
		"Tool time: 2.5s summed over calls, about 1.6s wall under this rule, about 1.5s if read-only calls could overlap",
		"Total Calls: 14",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// A transcript that carries per-turn tool stats (older writers) as well as result
// durations is timed once, from the stats, as it was before durations were read.
func TestRecordedToolStatsAreNotCountedTwice(t *testing.T) {
	path := transcript(t,
		[]turn{{calls: []string{call("1", "read", `{"path":"a"}`)}, results: []string{result("1", 40, false)}}},
		`{"id":"s","type":"stats","time":20,"stats":{"tools":{"call_count":1,"duration":40,`+
			`"exec_time":[{"name":"read","duration":40}]}}}`,
	)
	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if tt := stats.ToolTiming; tt.TotalCalls != 1 || tt.ByTool["read"].CallCount != 1 {
		t.Errorf("timing = %+v, want the one call counted once", tt)
	}
	// The schedule still reads the result's duration, which is the call itself.
	if stats.BatchSizes.ToolTimeMS != 40 {
		t.Errorf("tool time = %dms, want 40", stats.BatchSizes.ToolTimeMS)
	}
}
