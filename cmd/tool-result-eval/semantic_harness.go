package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func validCoverageOptions(opt options) bool {
	if opt.CoverageCheck != "" && opt.CoverageCheck != "exact" && opt.CoverageCheck != "semantic" {
		return false
	}
	if opt.CoverageCheck != "semantic" {
		return opt.CoverageJudgeModel == "" && opt.CoverageReplay == ""
	}
	return (opt.Scenario == "logs" || opt.Scenario == "recovery") &&
		(opt.CoverageJudgeModel == "" || opt.CoverageJudgeModel == "glm-5.3") &&
		(opt.CoverageReplay == "" || opt.Scenario == "recovery")
}

func coverageJudgeMetadata(opt options) map[string]any {
	if opt.CoverageCheck != "semantic" || opt.judgeResolved == nil {
		return nil
	}
	return map[string]any{"model": opt.judgeResolved.Model, "provider": opt.judgeResolved.Provider,
		"context_window": opt.judgeResolved.ContextWindow, "max_output_tokens": 8192,
		"timeout_seconds": 90, "max_retries": 0, "tools": 0, "temperature": 0,
		"max_attempts": 2, "timeout_scope": "shared_across_attempts",
		"repair_on": "parse/validation error or supported verdict rejected by host evidence guard; no transport, valid unsupported or unknown retry",
		"thinking":  "enabled", "reasoning_effort": "low",
		"usage_is_separate_from_task": true, "input_scope": "coverage field plus verified delivered-body, scan and citation-availability receipts; no full fixture or root-cause oracle"}
}

func semanticJudgeClient(resolved config.Resolved) llm.Client {
	extra := make(map[string]any, len(resolved.ExtraBody)+1)
	for key, value := range resolved.ExtraBody {
		extra[key] = value
	}
	extra["temperature"] = 0
	extra["thinking"] = map[string]any{"type": "enabled"}
	extra["reasoning_effort"] = "low"
	return llm.New(llm.Options{BaseURL: resolved.BaseURL, APIKey: resolved.APIKey, Model: resolved.Model,
		Headers: resolved.Headers, ExtraBody: extra, Compatibility: resolved.Compatibility,
		MaxTokens: 8192, MaxRetries: -1, Timeout: 90 * time.Second})
}

// The task's prompt, history, tools and measurements never include grader input
// or output. Only this acceptance conjunct changes; every other gate stays the
// same and the old exact wording check remains an explicit diagnostic.
func applyCoverageCheck(ctx context.Context, opt options, report *runReport, originalID string, f logFixture, log *safeLog) {
	if opt.CoverageCheck != "semantic" {
		return
	}
	checked := checkLogAnswer(report.FinalAnswer, f)
	if checked.FormatCorrect {
		exact := checked.CoverageCorrect
		report.CoverageExactMatch = &exact
	}
	semantic := semanticReport{ErrorClass: "task_not_completed"}
	if report.EndReason == string(agent.EndCompleted) && report.ErrorClass == "" {
		semantic.ErrorClass = "final_answer_unparseable"
		if checked.FormatCorrect && checked.Parsed != nil {
			semantic.ErrorClass = "grader_configuration_unavailable"
			if opt.judgeResolved != nil {
				ledger := buildSemanticDeliveryLedger(report.ToolCalls, report.Requests, originalID, f)
				// Facts, quotes and citations keep their deterministic checkers.
				// Review every assertion in coverage, without rejudging those fields.
				answer, _ := json.Marshal(map[string]string{"coverage": checked.Parsed.Coverage})
				if log != nil {
					log.emit(map[string]any{"type": "coverage_semantic_start", "run_id": report.ID, "judge_model": opt.judgeResolved.Model})
				}
				semantic = runSemanticCoverage(ctx, semanticJudgeClient(*opt.judgeResolved), semanticCoverageInput{Answer: string(answer), Ledger: ledger})
			}
		}
	}
	report.CoverageSemantic = &semantic
	report.CoverageCorrect = semantic.Passed
	report.Passed = report.EndReason == string(agent.EndCompleted) && report.ErrorClass == "" && report.FinalFormatCorrect &&
		report.NumericCorrect && report.CoverageCorrect && report.CitationsKnown && report.MCPCalls == 1 &&
		report.FileToolCalls == 0 && report.Recovery.MatchesSource && report.LogEvidence != nil && report.LogEvidence.Passed
	if log != nil {
		log.emit(map[string]any{"type": "coverage_semantic", "run_id": report.ID, "exact_match": report.CoverageExactMatch, "semantic": semantic})
	}
}

type coverageReplaySample struct {
	ID                   string          `json:"id"`
	Phase                string          `json:"phase"`
	BudgetMode           string          `json:"budget_mode"`
	LegacyPassed         bool            `json:"legacy_passed"`
	LegacyLatePassed     bool            `json:"legacy_late_passed"`
	CoverageExactMatch   bool            `json:"coverage_exact_match"`
	Semantic             *semanticReport `json:"coverage_semantic"`
	RegradedPassed       bool            `json:"regraded_passed"`
	RegradedLatePassed   bool            `json:"regraded_late_passed"`
	SourcePassed         bool            `json:"source_passed"`
	FactsPassed          bool            `json:"facts_passed"`
	ActualEvidencePassed bool            `json:"actual_evidence_passed"`
	OriginalFinalSHA256  string          `json:"original_final_sha256"`
	OriginalCoverage     string          `json:"original_coverage"`
}

// A contrast changes only coverage wording over an immutable actual-delivery
// ledger. It is labelled separately from a historical model-produced answer;
// this provenance is reporting metadata and never reaches the grader input.
type coverageReplayProvenance struct {
	OriginalSummarySHA256  string `json:"original_summary_sha256"`
	OriginalFinalSHA256    string `json:"original_final_sha256"`
	ContrastCoverageSHA256 string `json:"contrast_coverage_sha256"`
}

// Replay is grader calibration, not a new agent trial. Its task metrics remain
// in the immutable source report; only new, separately measured grader calls run.
func runCoverageReplay(ctx context.Context, opt options, log *safeLog, secrets []string) int {
	file, err := os.Open(opt.CoverageReplay)
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverage replay source unavailable")
		return 2
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		fmt.Fprintln(os.Stderr, "coverage replay source must be a bounded regular report")
		return 2
	}
	raw, err := os.ReadFile(opt.CoverageReplay)
	if err != nil {
		return 2
	}
	var source struct {
		Scenario              string                    `json:"scenario"`
		Status                string                    `json:"status"`
		Fixture               string                    `json:"fixture_sha256"`
		TextSHA               string                    `json:"dataset_text_sha256"`
		Runs                  []recoveryRunReport       `json:"runs"`
		CalibrationSourceKind string                    `json:"calibration_source_kind"`
		CalibrationProvenance *coverageReplayProvenance `json:"calibration_provenance"`
	}
	f := makeRecoveryFixture()
	if json.Unmarshal(raw, &source) != nil || source.Scenario != "recovery" || source.Status != "finished" ||
		source.Fixture != f.SHA256 || source.TextSHA != f.Logs.SHA256 || len(source.Runs) == 0 || len(source.Runs) > 20 {
		fmt.Fprintln(os.Stderr, "coverage replay source is not a finished matching recovery fixture")
		return 2
	}
	samples := []coverageReplaySample{}
	sourceKind := "recorded_model_answer"
	if source.CalibrationSourceKind != "" && source.CalibrationSourceKind != sourceKind && source.CalibrationSourceKind != "synthetic_coverage_contrast" {
		fmt.Fprintln(os.Stderr, "coverage replay source kind is not recognized")
		return 2
	}
	if source.CalibrationSourceKind == "synthetic_coverage_contrast" {
		sourceKind = source.CalibrationSourceKind
		if source.CalibrationProvenance == nil || !semanticValidSHA(source.CalibrationProvenance.OriginalSummarySHA256) ||
			!semanticValidSHA(source.CalibrationProvenance.OriginalFinalSHA256) || !semanticValidSHA(source.CalibrationProvenance.ContrastCoverageSHA256) {
			fmt.Fprintln(os.Stderr, "coverage contrast is missing its original-answer provenance")
			return 2
		}
	}
	summary := func(status string) map[string]any {
		return map[string]any{"version": 1, "scenario": "coverage-replay", "status": status, "semantic_grader": coverageJudgeMetadata(opt),
			"source_name": filepath.Base(opt.CoverageReplay), "source_sha256": digest(raw),
			"calibration_source_kind": sourceKind, "calibration_provenance": source.CalibrationProvenance,
			"fixture_sha256": f.SHA256, "dataset_text_sha256": f.Logs.SHA256, "samples": samples,
			"notes": []string{"Read-only recorded-answer replay or explicitly labelled coverage-only contrast; no task-model, MCP or file-tool call was repeated.",
				"Legacy reports are immutable. Grader usage is separate, and unknown/error results never pass.",
				"Only coverage acceptance changes. Facts, format, citations, actual late evidence, full recovery and source lineage retain their checks."}}
	}
	for _, run := range source.Runs {
		if ctx.Err() != nil {
			return 1
		}
		if run.FixtureSHA256 != f.SHA256 {
			fmt.Fprintln(os.Stderr, "coverage replay sample fixture mismatch")
			return 2
		}
		late := run.Late
		checked := checkLogAnswer(late.FinalAnswer, f.Logs)
		if sourceKind == "synthetic_coverage_contrast" && (len(source.Runs) != 1 || checked.Parsed == nil ||
			digest([]byte(checked.Parsed.Coverage)) != source.CalibrationProvenance.ContrastCoverageSHA256) {
			fmt.Fprintln(os.Stderr, "coverage contrast wording does not match its declared provenance")
			return 2
		}
		applyCoverageCheck(ctx, opt, &late, run.OriginalResultID, f.Logs, log)
		sample := coverageReplaySample{ID: run.ID, Phase: run.Phase, BudgetMode: run.BudgetMode,
			LegacyPassed: run.Passed, LegacyLatePassed: run.Late.Passed, CoverageExactMatch: checked.CoverageCorrect,
			Semantic: late.CoverageSemantic, RegradedLatePassed: late.Passed, SourcePassed: run.Source.Passed,
			FactsPassed: late.NumericCorrect, ActualEvidencePassed: late.LogEvidence != nil && late.LogEvidence.Passed,
			OriginalFinalSHA256: digest([]byte(run.Late.FinalAnswer))}
		if checked.Parsed != nil {
			sample.OriginalCoverage = checked.Parsed.Coverage
		}
		sample.RegradedPassed = late.Passed && run.Source.Passed && run.EarlyEvidence.Passed && run.FileToolCalls == 0 && run.MCPCalls == 1 && run.ErrorClass == ""
		samples = append(samples, sample)
		if writeSummary(opt.Output, secrets, summary("running")) != nil {
			return 2
		}
		fmt.Fprintf(os.Stderr, "%s: replay semantic=%t legacy_exact=%t\n", run.ID, late.CoverageCorrect, checked.CoverageCorrect)
	}
	if writeSummary(opt.Output, secrets, summary("finished")) != nil || log.err != nil {
		return 2
	}
	return 0 // A negative control rejected correctly is still a valid replay run.
}
