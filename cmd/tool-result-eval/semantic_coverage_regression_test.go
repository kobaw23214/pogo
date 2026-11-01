package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

type semanticRecordedReplay struct {
	Name                    string           `json:"name"`
	ExpectedAcceptance      *bool            `json:"expected_acceptance"`
	IndependentClass        string           `json:"independent_classification"`
	SourceSummarySHA256     string           `json:"source_summary_sha256"`
	SourceFinalAnswerSHA256 string           `json:"source_final_answer_sha256"`
	SourceFinalAnswer       string           `json:"source_final_answer"`
	Coverage                string           `json:"coverage"`
	CoverageSHA256          string           `json:"coverage_sha256"`
	OriginalResultID        string           `json:"original_result_id"`
	Calls                   []toolCallReport `json:"calls"`
	Requests                []requestReport  `json:"requests"`
}

func semanticRecordedReplays(t *testing.T) []semanticRecordedReplay {
	t.Helper()
	raw, err := os.ReadFile("testdata/semantic_coverage_replay.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version           int                      `json:"version"`
		DatasetTextSHA256 string                   `json:"dataset_text_sha256"`
		Cases             []semanticRecordedReplay `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.DatasetTextSHA256 != makeLogFixture().SHA256 || len(fixture.Cases) != 8 {
		t.Fatal("recorded calibration no longer represents the unchanged eight source observations")
	}
	for _, replay := range fixture.Cases {
		var original struct {
			Coverage string `json:"coverage"`
		}
		if digest([]byte(replay.SourceFinalAnswer)) != replay.SourceFinalAnswerSHA256 || digest([]byte(replay.Coverage)) != replay.CoverageSHA256 || !semanticValidSHA(replay.SourceSummarySHA256) || json.Unmarshal([]byte(replay.SourceFinalAnswer), &original) != nil || original.Coverage != replay.Coverage {
			t.Fatalf("recorded coverage/answer provenance changed: %s", replay.Name)
		}
	}
	return fixture.Cases
}

func semanticV3RecordedReplays(t *testing.T) []semanticRecordedReplay {
	t.Helper()
	raw, err := os.ReadFile("testdata/semantic_coverage_replay_v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version           int                      `json:"version"`
		DatasetTextSHA256 string                   `json:"dataset_text_sha256"`
		Cases             []semanticRecordedReplay `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 3 || fixture.DatasetTextSHA256 != makeLogFixture().SHA256 || len(fixture.Cases) != 15 {
		t.Fatal("v3 replay no longer represents the unchanged fifteen historical observations")
	}
	seen := map[string]bool{}
	for _, replay := range fixture.Cases {
		var original struct {
			Coverage string `json:"coverage"`
		}
		if seen[replay.Name] || digest([]byte(replay.SourceFinalAnswer)) != replay.SourceFinalAnswerSHA256 || digest([]byte(replay.Coverage)) != replay.CoverageSHA256 || !semanticValidSHA(replay.SourceSummarySHA256) || json.Unmarshal([]byte(replay.SourceFinalAnswer), &original) != nil || original.Coverage != replay.Coverage {
			t.Fatalf("v3 recorded name/coverage/answer provenance changed: %s", replay.Name)
		}
		seen[replay.Name] = true
	}
	return fixture.Cases
}

func semanticV3ReplayByName(t *testing.T, name string) semanticRecordedReplay {
	t.Helper()
	for _, replay := range semanticV3RecordedReplays(t) {
		if replay.Name == name {
			return replay
		}
	}
	t.Fatal("missing v3 recorded replay: " + name)
	return semanticRecordedReplay{}
}

func TestSemanticCoverageV3RecordedCalibrationKeepsTwelvePositiveTwoNegativeAndAmbiguity(t *testing.T) {
	positive, negative, ambiguous := 0, 0, 0
	for _, replay := range semanticV3RecordedReplays(t) {
		ledger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())
		if !checkSemanticEvidenceClaim(ledger, semanticClaimForTest(ledger, "all", semanticActualTargetPattern(t, ledger), "目标trace全部匹配已交付")).Passed {
			t.Fatalf("recorded target evidence is incomplete: %s", replay.Name)
		}
		switch {
		case replay.ExpectedAcceptance == nil:
			ambiguous++
		case *replay.ExpectedAcceptance:
			positive++
		default:
			negative++
		}
	}
	if positive != 12 || negative != 2 || ambiguous != 1 {
		t.Fatalf("v3 calibration expectations changed rather than preserving genuine failures: %d/%d/%d", positive, negative, ambiguous)
	}
}

func semanticReplayByName(t *testing.T, name string) semanticRecordedReplay {
	t.Helper()
	for _, replay := range semanticRecordedReplays(t) {
		if replay.Name == name {
			return replay
		}
	}
	t.Fatal("missing recorded replay: " + name)
	return semanticRecordedReplay{}
}

func semanticAllEvidenceIDs(ledger SemanticDeliveryLedger) []string {
	var ids []string
	for _, fragment := range ledger.Fragments {
		ids = append(ids, fragment.ID)
	}
	for _, page := range ledger.Searches {
		ids = append(ids, page.ID)
	}
	return ids
}

func semanticClaimForTest(ledger SemanticDeliveryLedger, quantifier, pattern, quote string) SemanticEvidenceClaim {
	kind := "body_property"
	if quantifier == "count" {
		kind = "count"
	}
	return SemanticEvidenceClaim{ID: "claim", Quote: quote, EvidenceKind: kind, Quantifier: quantifier, Pattern: pattern, ResultID: ledger.ResultID, EvidenceIDs: semanticAllEvidenceIDs(ledger)}
}

func semanticActualTargetPattern(t *testing.T, ledger SemanticDeliveryLedger) string {
	t.Helper()
	for _, page := range ledger.Searches {
		// Some historical models searched the actual trace's unique prefix.
		// Use the exact observed selection, never invent a narrower scan total.
		if page.Pattern != "" && strings.Contains(logTargetTrace, page.Pattern) && page.ScanComplete && page.TotalMatches == 5 {
			return page.Pattern
		}
	}
	t.Fatal("recorded observation lacks an actual five-match target selection")
	return ""
}

// These recorded rows calibrate the host evidence gate, not a pretend semantic
// judge. Real-model classification is separately recorded by --coverage-replay.
func TestSemanticCoverageRecordedReplayInputsAndTargetDelivery(t *testing.T) {
	f := makeLogFixture()
	positive, negative, ambiguous := 0, 0, 0
	for _, replay := range semanticRecordedReplays(t) {
		t.Run(replay.Name, func(t *testing.T) {
			ledger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, f)
			if ledger.ResultID != replay.OriginalResultID || ledger.SourceCompleteness != "unknown" || ledger.StoredTextBytes != len(f.Text) || len(ledger.Fragments) == 0 || len(ledger.Searches) == 0 {
				t.Fatal("real late model views did not construct a source-bounded delivery ledger")
			}
			// Every old answer already had all target-trace lines. This deliberately
			// does not conclude that an unrelated error population was fully read.
			claim := semanticClaimForTest(ledger, "all", logTargetTrace, "已交付目标trace的全部匹配行")
			if guard := checkSemanticEvidenceClaim(ledger, claim); !guard.Passed {
				t.Fatalf("recorded complete target trace was rejected: %+v", guard)
			}
			answer, _ := json.Marshal(map[string]any{"coverage": replay.Coverage})
			inventory, err := semanticCoverageInventory(string(answer))
			if err != nil || len(inventory) == 0 {
				t.Fatalf("coverage-only projection lost genuine recorded clauses: %v", err)
			}
			if replay.ExpectedAcceptance == nil {
				// The original CP-auto answer says that five matching rows were
				// fully read and listed, but its final evidence lists four core
				// lines. Keep this genuine ambiguity, rather than treating a
				// historical wording review as a known semantic positive.
				if replay.Name != "sourcefix-compact-persist-auto" || replay.IndependentClass != "ambiguous_rows_read_vs_rows_listed" {
					t.Fatal("unexpected unresolved calibration case")
				}
				ambiguous++
			} else if *replay.ExpectedAcceptance {
				positive++
			} else {
				negative++
				all := semanticClaimForTest(ledger, "all", "REMOTE_BUSY", "REMOTE_BUSY错误均属无关trace且已恢复")
				if guard := checkSemanticEvidenceClaim(ledger, all); guard.Passed {
					t.Fatal("real 20/21 partial page supported an all-records property claim")
				}
			}
		})
	}
	if positive != 6 || ambiguous != 1 || negative != 1 {
		t.Fatalf("expected six wording positives, one unresolved read/list ambiguity and one 20/21 negative: %d/%d/%d", positive, ambiguous, negative)
	}
}

func TestSemanticCoverageRecordedCountMetadataIsNotUniversalProperty(t *testing.T) {
	replay := semanticReplayByName(t, "baseline-persist-auto")
	ledger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())
	total := 21
	count := semanticClaimForTest(ledger, "count", "REMOTE_BUSY", "完整扫描统计REMOTE_BUSY共21个匹配")
	count.Count = &total
	if guard := checkSemanticEvidenceClaim(ledger, count); !guard.Passed {
		t.Fatalf("actual complete-scan count metadata was rejected merely because page 21 was unseen: %+v", guard)
	}
	all := count
	all.EvidenceKind, all.Quantifier, all.Quote = "body_property", "all", "REMOTE_BUSY错误均属无关trace且已恢复"
	if checkSemanticEvidenceClaim(ledger, all).Passed {
		t.Fatal("same count metadata established every match's recovered/unrelated properties")
	}
	subset := semanticClaimForTest(ledger, "displayed_subset", "REMOTE_BUSY", "仅已展示的REMOTE_BUSY条目属于无关任务")
	if guard := checkSemanticEvidenceClaim(ledger, subset); !guard.Passed {
		t.Fatalf("explicit displayed-subset claim required unseen records: %+v", guard)
	}
	wrong := 22
	count.Count = &wrong
	if checkSemanticEvidenceClaim(ledger, count).Passed {
		t.Fatal("invented total was accepted as host count metadata")
	}
	unknown := semanticClaimForTest(ledger, "unknown", "REMOTE_BUSY", "无法确定范围")
	if checkSemanticEvidenceClaim(ledger, unknown).Passed {
		t.Fatal("unknown evidence verdict became a semantic pass")
	}
}

func semanticRealSearchPage(t *testing.T, registry *tools.Registry, f logFixture, id, callID, pattern string, offset, limit int) (toolCallReport, toolView) {
	t.Helper()
	reader, _ := registry.Get(tools.ToolResultReadName)
	args := logSearchArgs(id, pattern, offset, limit, 0)
	result, err := reader.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	call, view := logSearchCallAndView(callID, args, result.Text, f)
	call.OutputSHA256 = digest([]byte(result.Text))
	call.SourceLabel, call.SourceType = "S2", "tool"
	view.SourceHeader = "[cite:S2 | tool_result | saved local search]"
	return call, view
}

func TestSemanticCoverageRealPagesMergeByUniqueDeliveredMatchNotShownCount(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	firstCall, first := semanticRealSearchPage(t, registry, f, id, "page-first", "REMOTE_BUSY", 0, 20)
	lastCall, last := semanticRealSearchPage(t, registry, f, id, "page-last", "REMOTE_BUSY", 20, 1)
	overlapCall, overlap := semanticRealSearchPage(t, registry, f, id, "page-overlap", "REMOTE_BUSY", 10, 11)
	for _, test := range []struct {
		name  string
		calls []toolCallReport
		views []toolView
		pass  bool
	}{
		{"partial20", []toolCallReport{firstCall}, []toolView{first}, false},
		{"replayed20twice", []toolCallReport{firstCall}, []toolView{first, first}, false},
		{"overlapCompletes21", []toolCallReport{firstCall, overlapCall}, []toolView{first, overlap}, true},
		{"separate21stCompletes", []toolCallReport{firstCall, lastCall}, []toolView{first, last}, true},
		{"outOfOrderAndRepeated", []toolCallReport{firstCall, lastCall}, []toolView{last, first, last}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := buildSemanticDeliveryLedger(test.calls, []requestReport{{ToolViews: test.views}}, id, f)
			claim := semanticClaimForTest(ledger, "all", "REMOTE_BUSY", "全部REMOTE_BUSY匹配已交付")
			if got := checkSemanticEvidenceClaim(ledger, claim); got.Passed != test.pass {
				t.Fatalf("counting shown rows instead of unique actual saved-byte coverage: %+v", got)
			}
		})
	}
	// An actual wider literal read can deliver the missing line. The partial
	// REMOTE_BUSY scan still supplies its own exact population count.
	wideCall, wide := semanticRealSearchPage(t, registry, f, id, "all-errors", "ERROR", 0, 200)
	ledger := buildSemanticDeliveryLedger([]toolCallReport{firstCall, wideCall}, []requestReport{{ToolViews: []toolView{first, wide}}}, id, f)
	if guard := checkSemanticEvidenceClaim(ledger, semanticClaimForTest(ledger, "all", "REMOTE_BUSY", "全部REMOTE_BUSY匹配已交付")); !guard.Passed {
		t.Fatalf("real full ERROR page failed to supplement the previously undisplayed REMOTE_BUSY line: %+v", guard)
	}
}

func TestSemanticCoverageRejectsWrongResultHashAndByteGap(t *testing.T) {
	f := makeLogFixture()
	registry, id := prepareSearchLog(t, f)
	call, view := semanticRealSearchPage(t, registry, f, id, "full-target", logTargetTrace, 0, 20)
	clone := func() toolView {
		raw, _ := json.Marshal(view)
		var copied toolView
		if err := json.Unmarshal(raw, &copied); err != nil {
			t.Fatal(err)
		}
		return copied
	}
	for _, test := range []struct {
		name string
		edit func(*toolView)
	}{
		{"wrongR", func(v *toolView) { v.Log.ResultID = "R" + strings.Repeat("a", 64) }},
		{"wrongBodyHash", func(v *toolView) { v.BodySHA256 = strings.Repeat("0", 64) }},
		{"wrongFragmentHash", func(v *toolView) { v.Log.Ranges[0].SHA256 = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := clone()
			test.edit(&changed)
			ledger := buildSemanticDeliveryLedger([]toolCallReport{call}, []requestReport{{ToolViews: []toolView{changed}}}, id, f)
			if len(ledger.Fragments) != 0 || checkSemanticEvidenceClaim(ledger, semanticClaimForTest(ledger, "all", logTargetTrace, "所有目标行已读取")).Passed {
				t.Fatal("unverified R or content hash entered the semantic evidence ledger")
			}
		})
	}
	firstCall, first := semanticRealSearchPage(t, registry, f, id, "first20", "REMOTE_BUSY", 0, 20)
	lastCall, last := semanticRealSearchPage(t, registry, f, id, "last1", "REMOTE_BUSY", 20, 1)
	lastRange := last.Log.Ranges[0]
	middle := lastRange.Start + strings.Index(f.Text[lastRange.Start:lastRange.End], "REMOTE_BUSY") + 5
	for _, gap := range []int{0, 1} {
		t.Run(fmt.Sprintf("middleGap%d", gap), func(t *testing.T) {
			calls, views := []toolCallReport{firstCall}, []toolView{first}
			for i, interval := range [][2]int{{lastRange.Start, middle}, {middle + gap, lastRange.End}} {
				args, _ := json.Marshal(map[string]any{"result_id": id, "view": "text", "offset": interval[0], "limit": interval[1] - interval[0]})
				reader, _ := registry.Get(tools.ToolResultReadName)
				result, err := reader.Execute(context.Background(), args)
				if err != nil {
					t.Fatal(err)
				}
				partCall, part := logSearchCallAndView(fmt.Sprintf("split-%d", i), args, result.Text, f)
				calls, views = append(calls, partCall), append(views, part)
			}
			ledger := buildSemanticDeliveryLedger(calls, []requestReport{{ToolViews: views}}, id, f)
			guard := checkSemanticEvidenceClaim(ledger, semanticClaimForTest(ledger, "all", "REMOTE_BUSY", "所有REMOTE_BUSY行已读取"))
			if guard.Passed != (gap == 0) {
				t.Fatalf("host filled a missing byte or failed to merge truly delivered adjacent bytes: %+v; last call %s", guard, lastCall.ID)
			}
		})
	}
}

type semanticRegressionJudge struct {
	model           string
	raw             string
	err             error
	stop            llm.StopReason
	decide          func(semanticJudgeRequest) semanticDecision
	calls           int
	toolDefs        int
	seen            semanticJudgeRequest
	system          string
	replies         []semanticRegressionReply
	inputs          []semanticJudgeRequest
	deadlines       []time.Time
	toolDefsPerCall []int
}

type semanticRegressionReply struct {
	raw    string
	decide func(semanticJudgeRequest) semanticDecision
	err    error
	stop   llm.StopReason
	usage  *llm.Usage
	delay  time.Duration
}

func (judge *semanticRegressionJudge) Model() string {
	if judge.model != "" {
		return judge.model
	}
	return semanticJudgeModel
}

func (judge *semanticRegressionJudge) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	judge.calls++
	judge.toolDefs = len(schemas)
	judge.toolDefsPerCall = append(judge.toolDefsPerCall, len(schemas))
	judge.system = system
	deadline, _ := ctx.Deadline()
	judge.deadlines = append(judge.deadlines, deadline)
	if len(history) != 1 || json.Unmarshal([]byte(history[0].Text()), &judge.seen) != nil {
		return llm.Response{}, errors.New("scripted judge did not receive one bounded structured input")
	}
	judge.inputs = append(judge.inputs, judge.seen)
	reply := semanticRegressionReply{raw: judge.raw, decide: judge.decide, err: judge.err, stop: judge.stop}
	if len(judge.replies) > 0 {
		reply = judge.replies[min(judge.calls-1, len(judge.replies)-1)]
	}
	if reply.delay > 0 {
		timer := time.NewTimer(reply.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	output := reply.raw
	if reply.decide != nil {
		decision := reply.decide(judge.seen)
		// Older scripted classifiers retain their original scope intent while
		// expressing it in the current required subject-domain schema. This is
		// test-fixture adaptation, never production output repair.
		semanticV8ScriptedDomains(&decision, judge.seen)
		raw, _ := json.Marshal(decision)
		output = string(raw)
	}
	stop := reply.stop
	if stop == "" {
		stop = llm.StopEndTurn
	}
	usage := llm.Usage{Input: 123, Output: 45}
	if reply.usage != nil {
		usage = *reply.usage
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: output}}}, StopReason: stop,
		Usage: usage}, reply.err
}

func semanticRegressionInput(t *testing.T, coverage string) semanticCoverageInput {
	t.Helper()
	replay := semanticReplayByName(t, "baseline-persist-auto")
	answer, _ := json.Marshal(map[string]string{"coverage": coverage})
	return semanticCoverageInput{Answer: string(answer), Ledger: buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())}
}

// A scripted classification tests protocol/host gates only. It is not evidence
// that a real model can classify these meanings; the recorded real calibration
// is a separate release gate. Quotes and unit IDs follow the host inventory.
func semanticScriptedDecision(request semanticJudgeRequest) semanticDecision {
	decision := semanticDecision{Scope: "saved_only", AllClaimsReviewed: true, Reviews: []semanticUnitReview{}, AnchorReviews: []semanticAnchorReview{}, Assertions: []semanticAssertion{}}
	metadataID := request.Ledger.SourceMetadata[0].ID
	for _, unit := range request.Units {
		review := semanticUnitReview{UnitID: unit.ID, Complete: true, AssertionIDs: []string{}}
		for i := 0; i < max(1, len(unit.Anchors)); i++ {
			id := fmt.Sprintf("assert_%03d", len(decision.Assertions)+1)
			assertion := semanticAssertion{ID: id, UnitID: unit.ID, Quote: unit.Text, Claim: "Saved observation boundary", Selector: "/coverage",
				EvidenceKind: "scope_metadata", Quantifier: "none", ResultID: request.Ledger.ResultID, EvidenceIDs: []string{metadataID}, Verdict: "supported", ReasonCode: "saved_scope_only"}
			decision.Assertions = append(decision.Assertions, assertion)
			review.AssertionIDs = append(review.AssertionIDs, id)
			if i < len(unit.Anchors) {
				decision.AnchorReviews = append(decision.AnchorReviews, semanticAnchorReview{AnchorID: unit.Anchors[i].ID, AssertionID: id, Interpretation: "non_quantifier"})
			}
		}
		decision.Reviews = append(decision.Reviews, review)
	}
	return decision
}

func TestSemanticCoverageScriptedScopeAndDenialDoNotBecomeExhaustiveReadClaims(t *testing.T) {
	for _, test := range []struct {
		name, coverage string
		subset         bool
	}{
		{"savedBoundary", "仅依据本次保存的日志范围", false},
		{"displayedSubset", "只描述已展示的REMOTE_BUSY匹配内容", true},
		{"negatedUniversal", "仅依据本次保存的日志范围；仅描述已展示的REMOTE_BUSY；不能断言所有REMOTE_BUSY已恢复", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := semanticRegressionInput(t, test.coverage)
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				if test.subset {
					decision.Scope = "subset_only"
				}
				for i := range decision.Assertions {
					assertion := &decision.Assertions[i]
					if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
						assertion.EvidenceKind, assertion.Quantifier = "body_property", "displayed_subset"
						assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
						assertion.Claim, assertion.ReasonCode = "Limited delivered observations", "visible_subset_only"
					}
				}
				for i := range decision.AnchorReviews {
					decision.AnchorReviews[i].Interpretation = "negated"
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if !report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) > 0 || report.StreamCalls != 1 || judge.calls != 1 || judge.toolDefs != 0 {
				t.Fatalf("explicit restriction/negation was treated as all-unseen-content read: %+v", report)
			}
			if judge.seen.Answer != input.Answer || report.InputSHA256 == "" || report.OutputSHA256 == "" || report.Usage.Input == nil || *report.Usage.Input != 123 {
				t.Fatal("independent judge input hashes/usage were lost or task answer was replaced")
			}
		})
	}
}

func TestSemanticCoverageScriptedSupportedUniversalStillFailsActual20Of21Guard(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据保存日志；REMOTE_BUSY错误均属无关trace")
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
				assertion.EvidenceKind, assertion.Quantifier = "body_property", "all"
				assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
				assertion.Claim, assertion.ReasonCode = "Every matching row is unrelated", "supported_by_complete_selection"
			}
		}
		for i := range decision.AnchorReviews {
			decision.AnchorReviews[i].Interpretation = "affirmative"
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || report.Decision == nil || len(report.ValidationErrors) > 0 {
		t.Fatalf("fixture did not reach independent pagination gate with a structurally valid overoptimistic judge: %+v", report)
	}
	found := false
	for _, guard := range report.AssertionGuards {
		if !guard.Passed {
			for _, reason := range guard.Reasons {
				found = found || reason == "not_all_domain_properties_were_delivered_as_complete_lines"
			}
		}
	}
	if !found {
		t.Fatalf("count-only evidence or broad saved-only scope bypassed the 20/21 gate: %+v", report)
	}
}

func TestSemanticCoverageScriptedJudgeCannotDowngradeBusinessAllToScopeMetadata(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据本次保存日志；REMOTE_BUSY错误均已恢复")
	// An overoptimistic grader mislabels the body property as scope metadata and
	// the affirmative quantifier as non_quantifier. All JSON/review IDs are valid.
	for _, kind := range []string{"scope_metadata", "count"} {
		t.Run(kind, func(t *testing.T) {
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				if kind == "count" {
					total := 21
					for i := range decision.Assertions {
						assertion := &decision.Assertions[i]
						if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
							assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "count", "count", &total
							assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
							assertion.Claim, assertion.ReasonCode = "The observed total is 21", "observed_metadata"
						}
					}
					for i := range decision.AnchorReviews {
						decision.AnchorReviews[i].Interpretation = "count_metadata"
					}
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed {
				t.Fatal("metadata kind erased an unsupported all-record recovery property")
			}
		})
	}
}

func TestSemanticCoverageScriptedScanAndTotalMetadataAreNotBodyUniversals(t *testing.T) {
	for _, test := range []struct {
		name, coverage, interpretation string
		count                          *int
	}{
		{name: "scanWholeSavedText", coverage: "仅依据本次保存日志；扫描覆盖全部已保存文本", interpretation: "scope_metadata"},
		{name: "totalMatchingCount", coverage: "仅依据本次保存日志；REMOTE_BUSY总共21条匹配", interpretation: "count_metadata", count: func() *int { n := 21; return &n }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := semanticRegressionInput(t, test.coverage)
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				var pageID string
				for _, page := range request.Ledger.Searches {
					if page.Pattern == "REMOTE_BUSY" {
						pageID = page.ID
					}
				}
				for i := range decision.Assertions {
					assertion := &decision.Assertions[i]
					if strings.Contains(assertion.Quote, "扫描") {
						assertion.Claim, assertion.EvidenceIDs, assertion.ReasonCode = "The saved text scan completed", []string{pageID}, "observed_metadata"
					}
					if strings.Contains(assertion.Quote, "总共") {
						assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "count", "count", test.count
						assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", []string{pageID}
						assertion.Claim, assertion.ReasonCode = "The observed literal count is 21", "observed_metadata"
					}
				}
				for i := range decision.AnchorReviews {
					decision.AnchorReviews[i].Interpretation = test.interpretation
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if !report.Passed || len(report.ValidationErrors) > 0 {
				t.Fatalf("actual completed scan/count metadata was confused with reading every matching row: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageCalibratedCompleteAnchorCanDescribeObservedCount(t *testing.T) {
	replay := semanticReplayByName(t, "sourcefix-control-auto")
	answer, _ := json.Marshal(map[string]string{"coverage": "仅依据保存日志；目标trace的完整匹配数为5"})
	input := semanticCoverageInput{Answer: string(answer), Ledger: buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())}
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		var pageID string
		for _, page := range request.Ledger.Searches {
			if page.Pattern == logTargetTrace && page.TotalMatches == 5 && page.ScanComplete {
				pageID = page.ID
			}
		}
		if pageID == "" {
			t.Fatal("recorded observation lacks the real complete scan count of five")
		}
		five := 5
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			if strings.Contains(assertion.Quote, "完整") {
				assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "count", "count", &five
				assertion.Pattern, assertion.EvidenceIDs = logTargetTrace, []string{pageID}
				assertion.Claim, assertion.ReasonCode = "The observed matching count is five", "observed_metadata"
			}
		}
		for i := range decision.AnchorReviews {
			decision.AnchorReviews[i].Interpretation = "count_metadata"
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if !report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 {
		t.Fatalf("a real count was rejected because its completeness anchor was not the word total: %+v", report)
	}
}

func TestSemanticCoverageCalibratedMetadataConjunctionAndBodyWording(t *testing.T) {
	t.Run("oneBoundaryMetadata", func(t *testing.T) {
		input := semanticRegressionInput(t, "仅依据保存导出的完整性未知及大小2881729字节")
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			decision := semanticScriptedDecision(request)
			for i := range decision.Assertions {
				decision.Assertions[i].Claim = "Saved export completeness is unknown and stored size is 2881729 bytes"
			}
			for i := range decision.AnchorReviews {
				decision.AnchorReviews[i].Interpretation = "scope_metadata"
			}
			return decision
		}}
		report := runSemanticCoverage(context.Background(), judge, input)
		if !report.Passed || len(report.ValidationErrors) != 0 {
			t.Fatalf("conjunctive normalized metadata of one boundary was mechanically rejected: %+v", report)
		}
	})
	t.Run("oneReadAttributeWithAnd", func(t *testing.T) {
		input := semanticRegressionInput(t, "仅依据保存日志；已展示匹配行已读取并核对")
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			decision := semanticScriptedDecision(request)
			for i := range decision.Assertions {
				assertion := &decision.Assertions[i]
				if strings.Contains(assertion.Quote, "已读取并核对") {
					assertion.EvidenceKind, assertion.Quantifier = "body_property", "displayed_subset"
					assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
					assertion.Claim, assertion.ReasonCode = "The displayed matching rows were read and verified", "visible_subset_only"
				}
			}
			return decision
		}}
		report := runSemanticCoverage(context.Background(), judge, input)
		if !report.Passed || len(report.ValidationErrors) != 0 {
			t.Fatalf("one read attribute was mechanically rejected for normalized English and: %+v", report)
		}
	})
}

func semanticRegressionContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSemanticCoverageCalibratedValidationFailureRetainsActualPaginationGuards(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据保存日志；REMOTE_BUSY错误均属无关trace")
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
				assertion.EvidenceKind, assertion.Quantifier = "body_property", "all"
				assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
				assertion.Claim, assertion.ReasonCode = "Every matching row is unrelated", "supported_by_complete_selection"
			}
		}
		for i := range decision.AnchorReviews {
			decision.AnchorReviews[i].Interpretation = "affirmative"
		}
		// An independent response-contract failure must still leave the actual
		// 20/21 delivery guard visible; it must never make the response pass.
		decision.Reviews[0].Complete = false
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || report.ErrorClass != "judge_output_failed_validation" || len(report.ValidationErrors) == 0 || report.Decision == nil || len(report.AssertionGuards) != len(report.Decision.Assertions) {
		t.Fatalf("validation either passed or hid independent evidence guards: %+v", report)
	}
	found := false
	for _, guard := range report.AssertionGuards {
		found = found || !guard.Passed && semanticRegressionContains(guard.Reasons, "not_all_domain_properties_were_delivered_as_complete_lines")
	}
	if !found {
		t.Fatalf("real omitted final match was hidden by an unrelated validation error: %+v", report)
	}
}

func TestSemanticCoverageCalibratedImplicitAbsenceCannotAcquireLedgerCount(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据保存日志；除此之外没有其他错误")
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		count := 21
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			if strings.Contains(assertion.Quote, "没有其他错误") {
				// Deliberately overoptimistic extraction: the statement never
				// claimed a number, though the ledger reports 21 matches.
				assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "count", "count", &count
				assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
				assertion.Claim, assertion.ReasonCode = "The observed matching count is 21", "observed_metadata"
			}
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || !semanticRegressionContains(report.ValidationErrors, "count_not_stated_in_original_quote") {
		t.Fatalf("implicit exhaustive absence acquired an invented numeric claim from ledger metadata: %+v", report)
	}
}

func TestSemanticCoverageCalibratedAllReadDoesNotResolveMissingFinalList(t *testing.T) {
	replay := semanticReplayByName(t, "sourcefix-compact-persist-auto")
	answer, _ := json.Marshal(map[string]string{"coverage": "仅依据保存日志；相关行已全部读取并按日志原顺序列出"})
	input := semanticCoverageInput{Answer: string(answer), Ledger: buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())}
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			if strings.Contains(assertion.Quote, "列出") {
				assertion.EvidenceKind, assertion.Quantifier = "body_property", "all"
				assertion.Pattern, assertion.EvidenceIDs = logTargetTrace, semanticAllEvidenceIDs(request.Ledger)
				assertion.Claim = "The related matching rows were listed in source order"
				assertion.Verdict, assertion.ReasonCode = "unknown", "ambiguous"
			}
		}
		for i := range decision.AnchorReviews {
			decision.AnchorReviews[i].Interpretation = "affirmative"
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 || report.Decision == nil {
		t.Fatalf("unknown listing claim was accepted or confused with a response-contract failure: %+v", report)
	}
	for _, guard := range report.AssertionGuards {
		if !guard.Passed {
			t.Fatalf("the recorded five rows really were delivered; this test concerns absent output-list context, not read coverage: %+v", report)
		}
	}
	var projection map[string]json.RawMessage
	if err := json.Unmarshal([]byte(judge.seen.Answer), &projection); err != nil || len(projection) != 1 || projection["coverage"] == nil {
		t.Fatal("coverage-only judge received an implicit final-list oracle")
	}
}

func TestSemanticCoverageCalibratedPromptStatesImplicitAbsenceAndMissingListLimits(t *testing.T) {
	judge := &semanticRegressionJudge{decide: semanticScriptedDecision}
	report := runSemanticCoverage(context.Background(), judge, semanticRegressionInput(t, "仅依据本次保存日志范围"))
	if !report.Passed || judge.calls != 1 || report.SystemSHA256 != digest([]byte(judge.system)) {
		t.Fatalf("fixed prompt was not the system actually sent to the independent judge: %+v", report)
	}
	// This checks the frozen input contract, not whether a real model correctly
	// follows it. Real-language classification remains separately calibrated.
	for _, instruction := range []string{
		"Do not copy a ledger number into a quote that contains none.",
		"exhaustive absence such as '未观察到该trace其他记录'",
		"body_property / all / count=null",
		"A single tail audit's original_event_time",
		"The input contains ONLY coverage, NOT the final answer's evidence list.",
		"The ledger proves reading, not final printing.",
		"is unknown without that final-list context",
	} {
		if !strings.Contains(judge.system, instruction) {
			t.Fatalf("actual grader prompt lost a calibration-discovered boundary: %q", instruction)
		}
	}
}

func TestSemanticCoverageV3OptionalNormalizedFieldsAndOnePropositionAnchors(t *testing.T) {
	t.Run("claimAndSelectorOmitted", func(t *testing.T) {
		input := semanticRegressionInput(t, "仅依据本次保存日志范围")
		units, err := semanticCoverageInventory(input.Answer)
		if err != nil {
			t.Fatal(err)
		}
		decision := semanticScriptedDecision(semanticJudgeRequest{Units: units, Ledger: input.Ledger})
		raw, _ := json.Marshal(decision)
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		for _, value := range document["assertions"].([]any) {
			assertion := value.(map[string]any)
			delete(assertion, "claim")
			delete(assertion, "selector")
		}
		raw, _ = json.Marshal(document)
		judge := &semanticRegressionJudge{raw: string(raw)}
		report := runSemanticCoverage(context.Background(), judge, input)
		if !report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 {
			t.Fatalf("the original quote/unit binding depended on redundant translated claim fields: %+v", report)
		}
	})
	t.Run("twoAnchorsOneDeliveredProposition", func(t *testing.T) {
		replay := semanticV3ReplayByName(t, "semantic-v2-control-auto")
		answer, _ := json.Marshal(map[string]string{"coverage": "在保存日志范围内所有目标trace的完整匹配行已实际读取"})
		input := semanticCoverageInput{Answer: string(answer), Ledger: buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())}
		pattern := semanticActualTargetPattern(t, input.Ledger)
		for _, omitAnchor := range []bool{false, true} {
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				if len(request.Units) != 1 || len(request.Units[0].Anchors) != 2 {
					t.Fatal("fixture must contain two anchors of one row-delivery proposition")
				}
				unit := request.Units[0]
				assertion := semanticAssertion{ID: "read_all", UnitID: unit.ID, Quote: unit.Text, EvidenceKind: "body_property", Quantifier: "all", ResultID: request.Ledger.ResultID, Pattern: pattern, EvidenceIDs: semanticAllEvidenceIDs(request.Ledger), Verdict: "supported", ReasonCode: "supported_by_complete_selection"}
				decision := semanticDecision{Scope: "saved_only", AllClaimsReviewed: true, Reviews: []semanticUnitReview{{UnitID: unit.ID, Complete: true, AssertionIDs: []string{assertion.ID}}}, Assertions: []semanticAssertion{assertion}}
				for _, anchor := range unit.Anchors {
					decision.AnchorReviews = append(decision.AnchorReviews, semanticAnchorReview{AnchorID: anchor.ID, AssertionID: assertion.ID, Interpretation: "affirmative"})
				}
				if omitAnchor {
					decision.AnchorReviews = decision.AnchorReviews[:1]
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed == omitAnchor {
				t.Fatalf("shared proposition anchors were either mechanically split or omitted: omitted=%v %+v", omitAnchor, report)
			}
			if omitAnchor && !semanticRegressionContains(report.ValidationErrors, "quantifier_anchor_not_reviewed") {
				t.Fatalf("missing exhaustive anchor bypassed the anti-omission gate: %+v", report)
			}
		}
	})
	t.Run("quoteCannotMoveAcrossUnits", func(t *testing.T) {
		input := semanticRegressionInput(t, "仅依据保存日志；源完整性未知")
		judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
			decision := semanticScriptedDecision(request)
			if len(request.Units) != 2 {
				t.Fatal("fixture requires two separate original clauses")
			}
			decision.Assertions[0].Quote = request.Units[1].Text
			return decision
		}}
		report := runSemanticCoverage(context.Background(), judge, input)
		if report.Passed || !semanticRegressionContains(report.ValidationErrors, "assertion_quote_not_original") {
			t.Fatalf("a quote from another real clause was treated as a quote of this unit: %+v", report)
		}
	})
}

func TestSemanticCoverageV3ParticularAuditFieldIsNotTraceUniversal(t *testing.T) {
	replay := semanticV3ReplayByName(t, "semantic-v2-persist-auto")
	ledger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())
	pattern := semanticActualTargetPattern(t, ledger)
	var auditID string
	for _, fragment := range ledger.Fragments {
		if strings.Contains(fragment.Text, "original_event_time") && strings.Contains(fragment.Text, logTargetTrace) {
			auditID = fragment.ID
			break
		}
	}
	if auditID == "" {
		t.Fatal("recorded persist view lost its exact tail audit evidence")
	}
	answer, _ := json.Marshal(map[string]string{"coverage": "仅依据保存日志；实际业务失败时间取自尾部延迟审计记录中的original_event_time字段"})
	input := semanticCoverageInput{Answer: string(answer), Ledger: ledger}
	for _, quantifier := range []string{"none", "all"} {
		t.Run(quantifier, func(t *testing.T) {
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				for i := range decision.Assertions {
					assertion := &decision.Assertions[i]
					if strings.Contains(assertion.Quote, "original_event_time") {
						assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "body_property", quantifier, nil
						assertion.EvidenceIDs = []string{auditID}
						if quantifier == "all" {
							assertion.Pattern = pattern
						}
						assertion.ReasonCode = "supported_by_delivered_text"
					}
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed != (quantifier == "none") || report.ErrorClass != "" || len(report.ValidationErrors) != 0 {
				t.Fatalf("particular audit field was lost in translated universal typing: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV3NegativeWithoutEvidenceIsSemanticFailure(t *testing.T) {
	for _, verdict := range []string{"unsupported", "unknown", "supported"} {
		t.Run(verdict, func(t *testing.T) {
			input := semanticRegressionInput(t, "仅依据保存日志；缺少判断其他记录的证据")
			judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				for i := range decision.Assertions {
					assertion := &decision.Assertions[i]
					if strings.Contains(assertion.Quote, "缺少") {
						assertion.EvidenceKind, assertion.Quantifier, assertion.EvidenceIDs = "body_property", "none", []string{}
						assertion.Verdict, assertion.ReasonCode = verdict, "missing_evidence"
					}
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed || report.Decision == nil {
				t.Fatalf("empty evidence negative passed or was not parsed: %+v", report)
			}
			if verdict == "supported" || verdict == "unsupported" {
				if report.ErrorClass != "judge_output_failed_validation" || len(report.ValidationErrors) == 0 {
					t.Fatalf("v8 cannot label absent body as supported or actually contradicted: %+v", report)
				}
			} else if report.ErrorClass != "" || len(report.ValidationErrors) != 0 {
				t.Fatalf("a valid semantic negative was changed into a protocol failure: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV3FalseMissingCitationUsesActualDeliveredReceipt(t *testing.T) {
	replay := semanticV3ReplayByName(t, "semantic-v2-compact-persist-off")
	ledger := buildSemanticDeliveryLedger(replay.Calls, replay.Requests, replay.OriginalResultID, makeLogFixture())
	var citationID string
	for _, citation := range ledger.Citations {
		if citation.Label == "S2" && citation.RequestIndex == 9 && citation.ResultID == replay.OriginalResultID && citation.CallID == "call_669764c1de2f498f90129f19" && citation.HeaderSHA256 == digest([]byte(citation.SourceHeader)) {
			citationID = citation.ID
		}
	}
	if citationID == "" {
		t.Fatal("the real late S2 header was not retained as a verified citation-availability receipt")
	}
	quote := "本次可用结果未被承认携带可引用来源标签"
	if !strings.Contains(replay.Coverage, quote) {
		t.Fatal("original source-false coverage phrase was changed")
	}
	answer, _ := json.Marshal(map[string]string{"coverage": quote})
	input := semanticCoverageInput{Answer: string(answer), Ledger: ledger}
	judge := &semanticRegressionJudge{decide: func(request semanticJudgeRequest) semanticDecision {
		decision := semanticScriptedDecision(request)
		for i := range decision.Assertions {
			assertion := &decision.Assertions[i]
			assertion.EvidenceKind, assertion.Quantifier, assertion.Count = "scope_metadata", "none", nil
			assertion.EvidenceIDs, assertion.Verdict, assertion.ReasonCode = []string{citationID}, "unsupported", "observed_metadata"
		}
		return decision
	}}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 || report.Decision == nil {
		t.Fatalf("false missing-source claim became an empty-ID protocol error or passed: %+v", report)
	}
	for _, guard := range report.AssertionGuards {
		if !guard.Passed {
			t.Fatalf("the actual citation receipt was not usable as metadata: %+v", report)
		}
	}
}

func semanticRegressionResultIDJSON(t *testing.T, decision semanticDecision, value *string) string {
	t.Helper()
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, entry := range document["assertions"].([]any) {
		assertion := entry.(map[string]any)
		if value == nil {
			delete(assertion, "result_id")
		} else {
			assertion["result_id"] = *value
		}
	}
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSemanticCoverageV4BindsOnlyMissingResultIDToActualEvidence(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据本次保存日志范围")
	units, err := semanticCoverageInventory(input.Answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"omitted", "explicitEmpty", "omittedUnknownEvidence"} {
		t.Run(mode, func(t *testing.T) {
			decision := semanticScriptedDecision(semanticJudgeRequest{Units: units, Ledger: input.Ledger})
			var resultID *string
			if mode == "explicitEmpty" {
				empty := ""
				resultID = &empty
			}
			if mode == "omittedUnknownEvidence" {
				decision.Assertions[0].EvidenceIDs = []string{"foreign_evidence_not_in_actual_ledger"}
			}
			raw := semanticRegressionResultIDJSON(t, decision, resultID)
			judge := &semanticRegressionJudge{raw: raw}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.OutputSHA256 != digest([]byte(raw)) || report.Decision == nil || len(report.Decision.Assertions) != len(decision.Assertions) {
				t.Fatalf("host binding overwrote or lost the raw grader output: %+v", report)
			}
			for i, assertion := range report.Decision.Assertions {
				if assertion.ResultID != "" || assertion.Quote != decision.Assertions[i].Quote || assertion.Quantifier != decision.Assertions[i].Quantifier || assertion.Verdict != decision.Assertions[i].Verdict {
					t.Fatalf("local effective identity binding rewrote the original semantic decision: %+v", assertion)
				}
			}
			if mode == "omittedUnknownEvidence" {
				if report.Passed || !semanticRegressionContains(report.ValidationErrors, "assertion_evidence_id_not_unique_or_observed") {
					t.Fatalf("missing R bypassed actual evidence binding: %+v", report)
				}
			} else if !report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 {
				t.Fatalf("missing R was not bound to the unique verified observation: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV4ExplicitForeignOrTruncatedResultIDIsNotCorrected(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据本次保存日志范围")
	units, err := semanticCoverageInventory(input.Answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, resultID := range []string{"R" + strings.Repeat("f", 64), input.Ledger.ResultID[:24]} {
		t.Run(resultID, func(t *testing.T) {
			decision := semanticScriptedDecision(semanticJudgeRequest{Units: units, Ledger: input.Ledger})
			raw := semanticRegressionResultIDJSON(t, decision, &resultID)
			judge := &semanticRegressionJudge{raw: raw}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed || report.ErrorClass != "judge_output_failed_validation" || !semanticRegressionContains(report.ValidationErrors, "assertion_selection_not_bound") || report.Decision == nil {
				t.Fatalf("an explicit foreign/truncated selection was silently repaired: %+v", report)
			}
			if report.OutputSHA256 != digest([]byte(raw)) || report.Decision.Assertions[0].ResultID != resultID {
				t.Fatal("the host replaced an explicit model-supplied selection")
			}
		})
	}
}

func TestSemanticCoverageV4MissingResultIDStillFailsActual20Of21Guard(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据保存日志；REMOTE_BUSY错误均属无关trace")
	units, err := semanticCoverageInventory(input.Answer)
	if err != nil {
		t.Fatal(err)
	}
	decision := semanticScriptedDecision(semanticJudgeRequest{Units: units, Ledger: input.Ledger})
	for i := range decision.Assertions {
		assertion := &decision.Assertions[i]
		if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
			assertion.EvidenceKind, assertion.Quantifier = "body_property", "all"
			assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(input.Ledger)
			assertion.ReasonCode = "supported_by_complete_selection"
		}
	}
	for i := range decision.AnchorReviews {
		decision.AnchorReviews[i].Interpretation = "affirmative"
	}
	semanticV8ScriptedDomains(&decision, semanticJudgeRequest{Units: units, Ledger: input.Ledger})
	raw := semanticRegressionResultIDJSON(t, decision, nil)
	judge := &semanticRegressionJudge{raw: raw}
	report := runSemanticCoverage(context.Background(), judge, input)
	if report.Passed || report.ErrorClass != "" || len(report.ValidationErrors) != 0 || report.Decision == nil {
		t.Fatalf("missing R either bypassed pagination or obscured a valid all assertion: %+v", report)
	}
	found := false
	for _, guard := range report.AssertionGuards {
		found = found || !guard.Passed && semanticRegressionContains(guard.Reasons, "not_all_domain_properties_were_delivered_as_complete_lines")
	}
	if !found {
		t.Fatalf("structural host identity binding erased the genuine missing final match: %+v", report)
	}
}

func TestSemanticCoverageV5NormalNegativeUnknownAndTransportUseOneCall(t *testing.T) {
	for _, test := range []struct {
		name, verdict string
		err           error
		stop          llm.StopReason
		wantPassed    bool
	}{
		{name: "normal", verdict: "supported", wantPassed: true},
		{name: "validUnsupported", verdict: "unsupported"},
		{name: "validUnknown", verdict: "unknown"},
		{name: "transport", err: errors.New("scripted transport failed")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "incompleteText", verdict: "supported", stop: llm.StopMaxTokens},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := semanticRegressionInput(t, "仅依据本次保存日志范围")
			judge := &semanticRegressionJudge{err: test.err, stop: test.stop, decide: func(request semanticJudgeRequest) semanticDecision {
				decision := semanticScriptedDecision(request)
				for i := range decision.Assertions {
					decision.Assertions[i].Verdict = test.verdict
				}
				return decision
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if judge.calls != 1 || report.StreamCalls != 1 || len(report.Attempts) != 1 || report.Passed != test.wantPassed {
				t.Fatalf("normal/negative/unknown/transport was repeatedly sampled: %+v", report)
			}
			if (test.verdict == "unsupported" || test.verdict == "unknown") && (report.ErrorClass != "" || len(report.ValidationErrors) != 0) {
				t.Fatalf("a valid semantic negative became a protocol-repair request: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageV5OneProtocolRepairKeepsBothAttemptsCostAndDeadline(t *testing.T) {
	for _, firstUnknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("firstUsageUnknown%v", firstUnknown), func(t *testing.T) {
			input := semanticRegressionInput(t, "仅依据本次保存日志范围")
			firstRaw := "not-json"
			firstUsage, secondUsage := llm.Usage{Input: 101, Output: 11}, llm.Usage{Input: 202, Output: 22}
			if firstUnknown {
				firstUsage = llm.Usage{}
			}
			judge := &semanticRegressionJudge{replies: []semanticRegressionReply{
				{raw: firstRaw, usage: &firstUsage, delay: 2 * time.Millisecond},
				{decide: semanticScriptedDecision, usage: &secondUsage},
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if !report.Passed || report.ErrorClass != "" || judge.calls != 2 || report.StreamCalls != 2 || len(report.Attempts) != 2 {
				t.Fatalf("one bounded protocol repair did not produce the final validated result: %+v", report)
			}
			first, repair := report.Attempts[0], report.Attempts[1]
			if first.Phase != "first" || repair.Phase != "repair" || first.Passed || !repair.Passed || first.ErrorClass != "invalid_judge_output" || first.Decision != nil || repair.Decision == nil {
				t.Fatalf("the repaired decision overwrote first-attempt evidence: %+v", report.Attempts)
			}
			if first.OutputSHA256 != digest([]byte(firstRaw)) || first.OutputBytes != len(firstRaw) || first.InputSHA256 != report.InputSHA256 || repair.InputSHA256 == first.InputSHA256 || repair.OutputSHA256 != report.OutputSHA256 || first.SystemSHA256 != repair.SystemSHA256 {
				t.Fatal("first/repair input/output fingerprints were merged or replaced")
			}
			if firstUnknown {
				if report.Usage.Input != nil || report.Usage.Output != nil {
					t.Fatal("missing first-attempt usage was filled with zero while totaling repair cost")
				}
			} else if report.Usage.Input == nil || *report.Usage.Input != 303 || report.Usage.Output == nil || *report.Usage.Output != 33 {
				t.Fatalf("grader cost excludes the failed first attempt: %+v", report.Usage)
			}
			if repair.Usage.Input == nil || *repair.Usage.Input != 202 || repair.Usage.Output == nil || *repair.Usage.Output != 22 {
				t.Fatal("per-attempt provider cost was lost")
			}
			if len(judge.inputs) != 2 || judge.inputs[0].Repair != nil || judge.inputs[1].Repair == nil || judge.inputs[1].Repair.PreviousOutput != firstRaw || judge.inputs[1].Repair.PreviousOutputSHA256 != first.OutputSHA256 || judge.inputs[1].Repair.PreviousErrorClass != first.ErrorClass {
				t.Fatal("repair feedback did not preserve the exact first final text and diagnostics")
			}
			for i, request := range judge.inputs {
				ledger, _ := json.Marshal(request.Ledger)
				if request.Answer != input.Answer || digest(ledger) != report.LedgerSHA256 || judge.toolDefsPerCall[i] != 0 {
					t.Fatal("repair changed the original coverage/actual ledger or supplied tools")
				}
			}
			if judge.deadlines[0].IsZero() || !judge.deadlines[0].Equal(judge.deadlines[1]) {
				t.Fatal("repair received a fresh timeout instead of sharing the original deadline")
			}
			feedback, _ := json.Marshal(judge.inputs[1].Repair)
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(feedback, &keys); err != nil {
				t.Fatal(err)
			}
			allowed := map[string]bool{"previous_output": true, "previous_output_sha256": true, "previous_error_class": true, "validation_errors": true, "assertion_guards": true}
			for key := range keys {
				if !allowed[key] {
					t.Fatalf("repair feedback added an expected label/oracle channel: %s", key)
				}
			}
		})
	}
}

func semanticV5PartialSelectionDecision(request semanticJudgeRequest) semanticDecision {
	decision := semanticScriptedDecision(request)
	for i := range decision.Assertions {
		assertion := &decision.Assertions[i]
		if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
			assertion.EvidenceKind, assertion.Quantifier = "body_property", "all"
			assertion.Pattern, assertion.EvidenceIDs = "REMOTE_BUSY", semanticAllEvidenceIDs(request.Ledger)
			assertion.ReasonCode = "supported_by_complete_selection"
		}
	}
	for i := range decision.AnchorReviews {
		decision.AnchorReviews[i].Interpretation = "affirmative"
	}
	return decision
}

func TestSemanticCoverageV5RepairCannotLowerGenuine20Of21Requirement(t *testing.T) {
	for _, mode := range []string{"acknowledgeUnsupported", "attemptSubsetDowngrade"} {
		t.Run(mode, func(t *testing.T) {
			input := semanticRegressionInput(t, "仅依据保存日志；REMOTE_BUSY错误均属无关trace")
			judge := &semanticRegressionJudge{replies: []semanticRegressionReply{
				{decide: semanticV5PartialSelectionDecision},
				{decide: func(request semanticJudgeRequest) semanticDecision {
					decision := semanticV5PartialSelectionDecision(request)
					for i := range decision.Assertions {
						assertion := &decision.Assertions[i]
						if strings.Contains(assertion.Quote, "REMOTE_BUSY") {
							if mode == "attemptSubsetDowngrade" {
								assertion.Quantifier = "displayed_subset"
							} else {
								assertion.Verdict, assertion.ReasonCode = "unsupported", "unsupported_full_selection"
							}
						}
					}
					return decision
				}},
			}}
			report := runSemanticCoverage(context.Background(), judge, input)
			if report.Passed || judge.calls != 2 || report.StreamCalls != 2 || len(report.Attempts) != 2 || report.Attempts[0].ErrorClass != "" || len(report.Attempts[0].ValidationErrors) != 0 {
				t.Fatalf("evidence repair accepted a missing final match or became unbounded: %+v", report)
			}
			found := false
			for _, guard := range report.Attempts[0].AssertionGuards {
				found = found || !guard.Passed && semanticRegressionContains(guard.Reasons, "not_all_domain_properties_were_delivered_as_complete_lines")
			}
			if !found {
				t.Fatal("first real 20/21 refusal was overwritten by the repair attempt")
			}
			if mode == "acknowledgeUnsupported" && (report.ErrorClass != "" || len(report.ValidationErrors) != 0) {
				t.Fatalf("valid acknowledgment of insufficiency became a protocol error: %+v", report)
			}
			if mode == "attemptSubsetDowngrade" && !semanticRegressionContains(report.ValidationErrors, "affirmative_universal_not_checked_as_all") {
				t.Fatalf("repair downgraded an unchanged exhaustive quote to a subset: %+v", report)
			}
		})
	}
}

func TestSemanticCoverageScriptedUnknownMalformedAndWrongJudgeFailClosed(t *testing.T) {
	input := semanticRegressionInput(t, "仅依据本次保存的日志范围")
	for _, test := range []struct {
		name, coverage string
		judge          semanticRegressionJudge
		mutate         func(*semanticDecision)
	}{
		{name: "transport", judge: semanticRegressionJudge{err: errors.New("scripted transport unavailable")}},
		{name: "truncated", judge: semanticRegressionJudge{stop: llm.StopMaxTokens}},
		{name: "prose", judge: semanticRegressionJudge{raw: "I think it passes"}},
		{name: "globalPassOnly", judge: semanticRegressionJudge{raw: `{"passed":true}`}},
		{name: "duplicateKey", judge: semanticRegressionJudge{raw: `{"scope":"saved_only","scope":"remote_full"}`}},
		{name: "oversizedOutput", judge: semanticRegressionJudge{raw: strings.Repeat("x", semanticJudgeOutputMaxBytes+1)}},
		{name: "wrongModel", judge: semanticRegressionJudge{model: "another-model"}},
		{name: "unknownScope", coverage: "错误分析已完成", mutate: func(d *semanticDecision) { d.Scope = "unknown" }},
		{name: "remoteFullScope", coverage: "覆盖远端线上所有日志，全部错误已恢复", mutate: func(d *semanticDecision) { d.Scope = "remote_full" }},
		{name: "unknownVerdict", mutate: func(d *semanticDecision) { d.Assertions[0].Verdict = "unknown" }},
		{name: "unsupportedVerdict", mutate: func(d *semanticDecision) { d.Assertions[0].Verdict = "unsupported" }},
		{name: "unknownQuantifier", mutate: func(d *semanticDecision) { d.Assertions[0].Quantifier = "unknown" }},
		{name: "notAllReviewed", mutate: func(d *semanticDecision) { d.AllClaimsReviewed = false }},
		{name: "unitNotReviewed", mutate: func(d *semanticDecision) { d.Reviews = nil }},
		{name: "emptyAssertions", mutate: func(d *semanticDecision) { d.Assertions = nil }},
		{name: "inventedQuote", mutate: func(d *semanticDecision) { d.Assertions[0].Quote = "not present in original answer" }},
		{name: "wrongSelector", mutate: func(d *semanticDecision) { d.Assertions[0].Selector = "/root_cause" }},
		{name: "wrongR", mutate: func(d *semanticDecision) { d.Assertions[0].ResultID = "R" + strings.Repeat("f", 64) }},
		{name: "unknownEvidence", mutate: func(d *semanticDecision) { d.Assertions[0].EvidenceIDs = []string{"invented-evidence"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			judge := test.judge
			if test.mutate != nil {
				judge.decide = func(request semanticJudgeRequest) semanticDecision {
					decision := semanticScriptedDecision(request)
					test.mutate(&decision)
					return decision
				}
			}
			caseInput := input
			if test.coverage != "" {
				caseInput = semanticRegressionInput(t, test.coverage)
			}
			report := runSemanticCoverage(context.Background(), &judge, caseInput)
			if report.Passed || judge.calls > 2 {
				t.Fatalf("unknown/error/malformed judge accepted or exceeded one permitted repair: %+v", report)
			}
			if test.name == "wrongModel" && judge.calls != 0 {
				t.Fatal("nonfixed grader model received a semantic request")
			}
		})
	}
	for _, answer := range []string{`{}`, `{"coverage":""}`, `{"coverage":null}`, `{"coverage":"bounded","oracle":"not allowed"}`, `{"coverage":"a","coverage":"b"}`} {
		judge := &semanticRegressionJudge{decide: semanticScriptedDecision}
		changed := input
		changed.Answer = answer
		if report := runSemanticCoverage(context.Background(), judge, changed); report.Passed || judge.calls != 0 {
			t.Fatal("missing/ambiguous scope or oracle-bearing object reached the grader")
		}
	}
	if report := runSemanticCoverage(context.Background(), nil, input); report.Passed || report.ErrorClass == "" {
		t.Fatal("missing independent grader was treated as a pass")
	}
}
