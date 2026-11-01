package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecoveryFixtureDefersQuestionWithoutOracleOrResultID(t *testing.T) {
	f := makeRecoveryFixture()
	other := makeRecoveryFixture()
	if f.SHA256 != other.SHA256 || f.Logs.SHA256 != makeLogFixture().SHA256 || f.SystemPrompt != recoverySystemPrompt || len(f.BackgroundTurns) != 3 {
		t.Fatal("recovery fixture is not deterministic or reused source changed")
	}
	total := 0
	early := f.SystemPrompt + "\n" + f.EarlyTask
	for _, background := range f.BackgroundTurns {
		if len(background) < 12<<10 || len(background) > 20<<10 || !utf8.ValidString(background) {
			t.Fatal("background turn must be a bounded real UTF-8 user task")
		}
		total += len(background)
		early += "\n" + background
	}
	if total < 32<<10 || total > 64<<10 {
		t.Fatalf("unexpected background size: %d", total)
	}
	for _, hidden := range []string{logTargetTrace, f.Logs.Expected.RootCauseCode, f.Logs.Expected.RootCauseSpanID, f.Logs.Expected.RootCauseTime, "1490520", "2881729"} {
		if strings.Contains(early, hidden) {
			t.Fatalf("early prompts leaked late answer or source location: %s", hidden)
		}
	}
	for _, line := range f.Logs.RequiredEvidence {
		if strings.Contains(early, line.Line) {
			t.Fatal("early prompts leaked oracle evidence")
		}
	}
	if !strings.Contains(f.EarlyTask, "mcp_eval_get_logs") || !strings.Contains(f.LateTask, logTargetTrace) || strings.Contains(f.LateTask, "result_id") || strings.Contains(f.LateTask, "R"+strings.Repeat("a", 64)) {
		t.Fatal("stage prompt contract changed")
	}
	existing := newEvalScenario(options{Scenario: "logs", LogTraceOnly: true, logs: &f.Logs}, fixture{})
	if f.LateTask != existing.task {
		t.Fatal("late task differs from frozen strict trace-only logs task")
	}
}

func TestRecoverySummaryProbeSeparatesTailCluesFromMiddleRootCause(t *testing.T) {
	f := makeRecoveryFixture()
	tail := f.Logs.RequiredEvidence[3].Line
	probe := probeRecoverySummary("日志已保存；"+tail, f)
	if probe.RootCauseLeaked || !probe.FinalAuditMentioned || !containsLogKey(probe.AnswerValueKeys, "request_failure_time") || len(probe.MiddleEvidenceKeys) != 0 {
		t.Fatalf("tail timestamp was confused with root cause leakage: %+v", probe)
	}
	probe = probeRecoverySummary("提示："+f.Logs.Expected.RootCauseCode, f)
	if !probe.RootCauseLeaked || !containsLogKey(probe.AnswerValueKeys, "root_cause_code") {
		t.Fatal("summary root cause was not recorded")
	}
	probe = probeRecoverySummary(f.Logs.RequiredEvidence[0].Line, f)
	if !probe.RootCauseLeaked || !containsLogKey(probe.MiddleEvidenceKeys, "recovered_retry") {
		t.Fatal("summary middle evidence was not recorded")
	}
	if probe.SHA256 != digest([]byte(f.Logs.RequiredEvidence[0].Line)) {
		t.Fatal("summary diagnostic hash is not exact")
	}
}
