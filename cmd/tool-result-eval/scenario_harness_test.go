package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/tools"
)

func TestRecordsScenarioKeepsOriginalInputsAndDeclaration(t *testing.T) {
	f := makeFixture()
	for _, opt := range []options{{}, {Scenario: "records"}} {
		scenario := newEvalScenario(opt, f)
		if scenario.system != fullSystemPrompt() || scenario.task != taskPrompt || scenario.logs != nil || digest(scenario.records.PayloadJSON) != digest(f.PayloadJSON) {
			t.Fatal("default records scenario changed its original system/task/fixture")
		}
		fields := scenario.summaryFields()
		if fields["dataset_rows"] != len(f.Rows) || fields["dataset_rows_sha256"] != digest(f.RowsJSON) {
			t.Fatal("records summary changed fixture identity")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	local, err := newEvalScenario(options{}, f).newMCP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer local.close()
	catalog, err := local.session.ListTools(ctx, nil)
	if err != nil || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "get_records" || catalog.Tools[0].Description != "Return the observed records as one JSON object with a rows array. No filtering, pagination or aggregation parameters. Fetch at most once, then inspect the saved observation with tool_result." {
		t.Fatal("records SDK declaration changed")
	}
}

func TestLogsTraceOnlyChangesOnlyKnownTimeSentence(t *testing.T) {
	f := makeLogFixture()
	originalTask, originalText, originalSHA := f.TaskPrompt, f.Text, f.SHA256
	originalOracle, _ := json.Marshal(f.Expected)
	baseline := newEvalScenario(options{Scenario: "logs", logs: &f}, fixture{})
	traceOnly := newEvalScenario(options{Scenario: "logs", LogTraceOnly: true, logs: &f}, fixture{})
	_, baselineRest, _ := strings.Cut(baseline.task, "\n")
	first, traceRest, _ := strings.Cut(traceOnly.task, "\n")
	if baseline.task != originalTask || baselineRest != traceRest || first != "请分析 get_logs 返回的日志。用户反馈目标请求失败，无法提供发生时间，trace_id="+logTargetTrace+"。" {
		t.Fatal("trace-only variant changed more than the known failure-time sentence")
	}
	if traceOnly.system != baseline.system || traceOnly.task == baseline.task || digest([]byte(traceOnly.task)) == digest([]byte(baseline.task)) || strings.Contains(first, "14:05:10") {
		t.Fatal("trace-only variant must have a distinct task hash and identical system")
	}
	afterOracle, _ := json.Marshal(f.Expected)
	if f.TaskPrompt != originalTask || f.Text != originalText || f.SHA256 != originalSHA || string(originalOracle) != string(afterOracle) || traceOnly.logs != baseline.logs {
		t.Fatal("task variant mutated the frozen fixture/oracle")
	}
	if traceOnly.summaryFields()["log_trace_only"] != true || traceOnly.summaryFields()["log_task_variant"] != "trace_only" || baseline.summaryFields()["log_trace_only"] != false || baseline.summaryFields()["log_task_variant"] != "time_and_trace" {
		t.Fatal("scenario summary did not identify the paired task variant")
	}
	records := makeFixture()
	normalRecords := newEvalScenario(options{Scenario: "records"}, records)
	invalidRecordsFlag := newEvalScenario(options{Scenario: "records", LogTraceOnly: true}, records)
	if normalRecords.system != invalidRecordsFlag.system || normalRecords.task != invalidRecordsFlag.task || digest(normalRecords.records.PayloadJSON) != digest(invalidRecordsFlag.records.PayloadJSON) {
		t.Fatal("records inputs were affected by a logs-only task option")
	}
}

func TestTraceOnlyFlagIsLogsOnly(t *testing.T) {
	for _, tc := range []struct {
		scenario         string
		traceOnly, valid bool
	}{
		{"records", false, true}, {"records", true, false}, {"logs", false, true}, {"logs", true, true}, {"unknown", false, false},
	} {
		if validScenarioVariant(options{Scenario: tc.scenario, LogTraceOnly: tc.traceOnly}) != tc.valid {
			t.Fatalf("unexpected scenario validation: %+v", tc)
		}
	}
}

func TestLogsScenarioSDKTextOnlyPersistenceAndIndependentRecovery(t *testing.T) {
	f := makeLogFixture()
	if len(f.Text) < 2<<20 || len(f.Text) > 4<<20 || digest([]byte(f.Text)) != f.SHA256 {
		t.Fatalf("log fixture must be a deterministic 2-4 MiB plain text observation: bytes=%d", len(f.Text))
	}
	scenario := newEvalScenario(options{Scenario: "logs", logs: &f}, makeFixture())
	oracle, _ := json.Marshal(f.Expected)
	if scenario.logs == nil || scenario.task != f.TaskPrompt || strings.Contains(scenario.system, string(oracle)) || strings.Contains(scenario.task, string(oracle)) {
		t.Fatal("scenario passed the host answer oracle into model instructions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	local, err := scenario.newMCP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer local.close()
	catalog, err := local.session.ListTools(ctx, nil)
	if err != nil || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "get_logs" {
		t.Fatal("logs scenario did not project its own SDK tool")
	}
	empty := []string{}
	private := t.TempDir()
	registry := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true, Enabled: &empty, MCP: []tools.Tool{local.wrapper}, SpillDir: filepath.Join(private, "spill"), ResultScope: "logs-sdk-self-test"})
	tool, found := registry.Get(local.wrapper.Name())
	if !found {
		t.Fatal("SDK log tool was not registered")
	}
	result, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil || result.Payload == nil || result.Payload.Text != f.Text || len(result.Payload.Structured) != 0 {
		t.Fatalf("logs SDK must return complete text without structured rows/oracle: err=%v", err)
	}
	prepared := registry.PrepareResult(ctx, tool.Name(), result, nil)
	if prepared.ResultID == "" || len(prepared.Text) >= len(f.Text) {
		t.Fatal("large text did not reach production managed persistence/preview")
	}
	recovery := scenario.recover(ctx, registry, prepared.ResultID)
	if !recovery.Complete || !recovery.MatchesSource || recovery.TextBytes != len(f.Text) || recovery.TextSHA256 != f.SHA256 || recovery.Pages < 2 || local.attempts.Load() != 1 {
		t.Fatalf("host text recovery failed or fetched the original MCP again: %+v; calls=%d", recovery, local.attempts.Load())
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{}`)); err == nil || local.attempts.Load() != 2 {
		t.Fatal("a repeated original log fetch was not explicitly rejected")
	}
}

func TestLogsRunCannotPassWithOracleAnswerButUndeliveredMiddleEvidence(t *testing.T) {
	f := makeLogFixture()
	final := f.Expected
	final.Sources = []string{"[1](#cite:S1)"}
	finalJSON, _ := json.Marshal(final)
	var requests atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		n := requests.Add(1)
		var delta map[string]any
		finish := "stop"
		if n == 1 {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "logs-original", "type": "function", "function": map[string]any{"name": "mcp_eval_get_logs", "arguments": "{}"}}}}
			finish = "tool_calls"
		} else {
			delta = map[string]any{"content": string(finalJSON)}
		}
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer model.Close()
	log := &safeLog{encoder: json.NewEncoder(io.Discard)}
	resolved := config.Resolved{Model: "scripted-test", Provider: "fixture", BaseURL: model.URL, ContextWindow: 100000, MaxTokens: 1000}
	report := runOne(context.Background(), options{Scenario: "logs", logs: &f}, resolved, fixture{}, "auto", 1, log)
	if !report.FinalFormatCorrect || !report.NumericCorrect || !report.CoverageCorrect || !report.CitationsKnown || !report.Recovery.MatchesSource {
		t.Fatalf("fixture must isolate delivery checks from otherwise correct answer/recovery: format=%t facts=%t coverage=%t citations=%t recovery=%+v", report.FinalFormatCorrect, report.NumericCorrect, report.CoverageCorrect, report.CitationsKnown, report.Recovery)
	}
	if report.LogEvidence == nil || report.LogEvidence.Passed || report.Passed || report.MCPCalls != 1 || report.FileToolCalls != 0 {
		t.Fatalf("correct oracle/UI recovery was incorrectly counted as model evidence: trace=%+v passed=%t mcp=%d files=%d", report.LogEvidence, report.Passed, report.MCPCalls, report.FileToolCalls)
	}
	if len(report.Requests) < 2 || len(report.LogEvidence.MissingEvidence) == 0 {
		t.Fatal("actual model requests did not record the missing middle-log evidence")
	}
}
