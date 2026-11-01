package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yosukeno/pogo/agent"
)

func TestCoverageSemanticOptionsAreExplicitAndKeepLegacyDefault(t *testing.T) {
	for _, opt := range []options{{}, {Scenario: "records", CoverageCheck: "exact"},
		{Scenario: "recovery", CoverageCheck: "semantic"}, {Scenario: "logs", CoverageCheck: "semantic", CoverageJudgeModel: "glm-5.3"},
		{Scenario: "recovery", CoverageCheck: "semantic", CoverageReplay: "historical-summary.json"}} {
		if !validCoverageOptions(opt) {
			t.Fatalf("valid coverage option rejected: %+v", opt)
		}
	}
	for _, opt := range []options{{Scenario: "records", CoverageCheck: "semantic"}, {Scenario: "recovery", CoverageCheck: "other"},
		{Scenario: "recovery", CoverageCheck: "semantic", CoverageJudgeModel: "unfixed-model"},
		{Scenario: "logs", CoverageCheck: "semantic", CoverageReplay: "historical-summary.json"},
		{Scenario: "recovery", CoverageCheck: "exact", CoverageReplay: "historical-summary.json"},
		{Scenario: "recovery", CoverageJudgeModel: "glm-5.3"}} {
		if validCoverageOptions(opt) {
			t.Fatalf("invalid coverage options accepted: %+v", opt)
		}
	}
}

func TestCoverageSemanticIntegrationNeverFallsBackToExactOnUnknown(t *testing.T) {
	f := makeLogFixture()
	answer, err := json.Marshal(f.Expected)
	if err != nil {
		t.Fatal(err)
	}
	legacy := runReport{ID: "legacy", EndReason: string(agent.EndCompleted), FinalAnswer: string(answer),
		FinalFormatCorrect: true, NumericCorrect: true, CoverageCorrect: true, Passed: true}
	before := legacy
	applyCoverageCheck(context.Background(), options{}, &legacy, "", f, nil)
	if !reflect.DeepEqual(before, legacy) {
		t.Fatal("default legacy mode changed task outcome or added a judge call")
	}
	applyCoverageCheck(context.Background(), options{CoverageCheck: "semantic"}, &legacy, "", f, nil)
	if legacy.Passed || legacy.CoverageCorrect || legacy.CoverageExactMatch == nil || !*legacy.CoverageExactMatch ||
		legacy.CoverageSemantic == nil || legacy.CoverageSemantic.ErrorClass != "grader_configuration_unavailable" || legacy.CoverageSemantic.StreamCalls != 0 {
		t.Fatal("missing semantic grader silently fell back to a passing exact phrase")
	}
	for _, test := range []struct{ end, answer, want string }{
		{"transport_error", string(answer), "task_not_completed"},
		{string(agent.EndCompleted), "malformed output", "final_answer_unparseable"},
	} {
		run := runReport{EndReason: test.end, FinalAnswer: test.answer, Passed: true}
		applyCoverageCheck(context.Background(), options{CoverageCheck: "semantic"}, &run, "", f, nil)
		if run.Passed || run.CoverageSemantic == nil || run.CoverageSemantic.ErrorClass != test.want || run.CoverageSemantic.StreamCalls != 0 {
			t.Fatalf("incomplete task was assigned a semantic pass: %+v", run.CoverageSemantic)
		}
	}
}
