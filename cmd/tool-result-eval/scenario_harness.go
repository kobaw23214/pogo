package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

// A scenario changes its isolated observation and user task, not execution
// limits, SDK transport, model measurement or the production result pipeline.
// Records keep the original prompt, fixture and MCP declaration byte for byte.
type evalScenario struct {
	system       string
	task         string
	records      fixture
	logs         *logFixture
	logTraceOnly bool
	grouped      bool
}

func newEvalScenario(opt options, records fixture) evalScenario {
	if opt.Scenario == "logs" {
		f := opt.logs
		if f == nil {
			value := makeLogFixture()
			f = &value
		}
		task := f.TaskPrompt
		if opt.LogTraceOnly {
			_, rest, _ := strings.Cut(task, "\n")
			task = "请分析 get_logs 返回的日志。用户反馈目标请求失败，无法提供发生时间，trace_id=" + logTargetTrace + "。\n" + rest
		}
		return evalScenario{system: logSystemPrompt + "\n\n" + agent.CitationSection(), task: task, logs: f, logTraceOnly: opt.LogTraceOnly}
	}
	if opt.Scenario == "grouped-records" {
		return evalScenario{system: fullSystemPrompt(), task: groupedTaskPrompt, records: records, grouped: true}
	}
	return evalScenario{system: fullSystemPrompt(), task: taskPrompt, records: records}
}

func validScenarioVariant(opt options) bool {
	if opt.Scenario == "recovery" {
		return !opt.LogTraceOnly && validRecoveryPhase(opt.RecoveryPhase) && (opt.RecoveryPhase != "switch" || opt.RecoverySwitchModel != "")
	}
	return (opt.Scenario == "records" || opt.Scenario == "grouped-records" || opt.Scenario == "logs") && (!opt.LogTraceOnly || opt.Scenario == "logs") && opt.RecoveryPhase == "" && opt.RecoverySwitchModel == ""
}

func (scenario evalScenario) logTaskVariant() string {
	if scenario.logTraceOnly {
		return "trace_only"
	}
	return "time_and_trace"
}

func (scenario evalScenario) newMCP(ctx context.Context) (*localMCP, error) {
	if scenario.logs == nil {
		return newLocalMCP(ctx, scenario.records)
	}
	return newLocalMCPResponse(ctx, "get_logs", "Return this observation's complete raw text log once. No filtering, pagination or analysis arguments. After fetching, inspect the saved result with the available tool_result capabilities; do not fetch the original log again.", func() *mcp.CallToolResult {
		// Deliberately text-only. Structured rows or an embedded oracle would
		// turn a log task into the unrelated records/query benchmark.
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: scenario.logs.Text}}}
	})
}

func (scenario evalScenario) recover(ctx context.Context, registry *tools.Registry, originalID string) recoveryReport {
	if scenario.logs == nil {
		return recoverRows(ctx, registry, originalID, scenario.records)
	}
	return recoverLogText(ctx, registry, originalID, *scenario.logs)
}

func (scenario evalScenario) finishLogs(report *runReport, originalID string, labels map[string]bool) {
	f := *scenario.logs
	checked := checkLogAnswer(report.FinalAnswer, f)
	report.FinalFormatCorrect, report.FactChecksRun = checked.FormatCorrect, checked.FormatCorrect
	report.LogAnswer = checked.Parsed
	report.NumericCorrect, report.CoverageCorrect = checked.FactsCorrect, checked.CoverageCorrect
	report.CitationLabelsKnown, report.CitationFormatCorrect = citationChecks(checked.Sources, labels)
	report.CitationsKnown = report.CitationLabelsKnown && report.CitationFormatCorrect
	report.ToolCalls = visibleCalls(report.ToolCalls, report.Requests)
	trace := checkLogDelivery(report.ToolCalls, report.Requests, originalID, f)
	report.LogEvidence = &trace
	report.Passed = report.EndReason == string(agent.EndCompleted) && report.ErrorClass == "" && report.FinalFormatCorrect && report.NumericCorrect && report.CoverageCorrect && report.CitationsKnown && report.MCPCalls == 1 && report.FileToolCalls == 0 && report.Recovery.MatchesSource && trace.Passed
}

func (scenario evalScenario) summaryFields() map[string]any {
	if scenario.grouped {
		return map[string]any{"scenario": "grouped-records", "dataset_rows": len(scenario.records.Rows), "dataset_rows_sha256": digest(scenario.records.RowsJSON), "dataset_payload_sha256": digest(scenario.records.PayloadJSON), "dataset_payload_bytes": len(scenario.records.PayloadJSON), "expected": groupedExpected(scenario.records), "model_delivery_basis": "Only successful actual model-request JSON bodies with body SHA256, original-result and native-call binding; never host-only output or recovery reads."}
	}
	if scenario.logs == nil {
		return map[string]any{"dataset_rows": len(scenario.records.Rows), "dataset_rows_sha256": digest(scenario.records.RowsJSON), "expected": scenario.records.Expected}
	}
	f := scenario.logs
	return map[string]any{
		"scenario": "logs", "dataset_text_bytes": len(f.Text), "dataset_text_sha256": f.SHA256,
		"log_task_variant": scenario.logTaskVariant(), "log_trace_only": scenario.logTraceOnly,
		"dataset_lines": strings.Count(f.Text, "\n"), "expected": f.Expected,
		"model_delivery_basis": "Only actual model request text/string fragments and bounded verified log evidence; never host/UI tool output.",
	}
}

// Host recovery is independently reported after the agent finishes. It proves
// persistence/readability, never that these bytes reached a model request.
func recoverLogText(ctx context.Context, registry *tools.Registry, id string, f logFixture) recoveryReport {
	report := recoveryReport{Scope: "saved text log"}
	reader, found := registry.Get(tools.ToolResultReadName)
	if !found || id == "" {
		report.ErrorClass = "original_result_id_unavailable"
		return report
	}
	report.Checked = true
	hash := sha256.New()
	for offset := 0; report.Pages <= len(f.Text); {
		args, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": offset, "limit": 32768})
		page, err := reader.Execute(ctx, args)
		if err != nil {
			report.ErrorClass = errorClass(err)
			return report
		}
		details, ok := page.Details.(tools.ToolResultReadDetails)
		if !ok || details.Offset != offset || details.Total != len(f.Text) || details.OriginalIsError || !details.TextComplete {
			report.ErrorClass = "invalid_or_incomplete_log_recovery_receipt"
			return report
		}
		end := details.Total
		if details.NextOffset != nil {
			end = *details.NextOffset
		}
		n := end - offset
		if n < 0 || n > len(page.Text) || n == 0 && !details.Complete {
			report.ErrorClass = "invalid_log_recovery_continuation"
			return report
		}
		fragment := page.Text[len(page.Text)-n:]
		if !utf8.ValidString(fragment) {
			report.ErrorClass = "invalid_log_recovery_utf8"
			return report
		}
		_, _ = hash.Write([]byte(fragment))
		report.TextBytes += len(fragment)
		report.Lines += strings.Count(fragment, "\n")
		report.Pages++
		if details.Complete {
			report.Complete = details.NextOffset == nil && end == len(f.Text)
			break
		}
		if details.NextOffset == nil || end <= offset {
			report.ErrorClass = "invalid_log_recovery_continuation"
			return report
		}
		offset = end
	}
	report.TextSHA256 = hex.EncodeToString(hash.Sum(nil))
	report.MatchesSource = report.Complete && report.TextBytes == len(f.Text) && report.TextSHA256 == f.SHA256
	return report
}
