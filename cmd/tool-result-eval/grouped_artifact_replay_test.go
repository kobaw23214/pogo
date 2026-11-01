package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const groupedReplayRule = "Validated original-reference provenance is allowed as an additional source; actual computed-group and sorted-row source citations are both still required."

type groupedArtifactReplay struct {
	Path               string           `json:"original_summary_path"`
	SHA256             string           `json:"original_summary_sha256"`
	RawPassed          bool             `json:"raw_summary_passed"`
	RawRunPassed       bool             `json:"raw_run_passed"`
	CurrentPassed      bool             `json:"current_passed"`
	Model              string           `json:"model"`
	OriginalRequests   int              `json:"original_recorded_model_requests"`
	CompactedBodies    int              `json:"restored_compact_json_bodies"`
	BodyHashesVerified bool             `json:"actual_body_hashes_verified"`
	Format             bool             `json:"strict_json"`
	Facts              bool             `json:"facts_correct"`
	Scope              bool             `json:"scope_correct"`
	Citations          bool             `json:"citation_format_and_known_labels"`
	Evidence           *groupedEvidence `json:"current_grouped_evidence"`
	Error              string           `json:"replay_error,omitempty"`
}

// Explicit opt-in only: this test re-evaluates two frozen completed reports. It
// never opens a provider/client, invokes an agent or changes raw report files.
func TestGroupedArtifactReplay(t *testing.T) {
	root := os.Getenv("POGO_GROUPED_REPLAY_DIR")
	if root == "" {
		t.Skip("set POGO_GROUPED_REPLAY_DIR to replay frozen GLM/Kimi artifacts without model requests")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "replay-review.json")
	if _, err := os.Stat(destination); err == nil {
		t.Fatal("refuse to overwrite existing replay review")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	f := makeFixture()
	scenario := newEvalScenario(options{Scenario: "grouped-records"}, f)
	var reviews []groupedArtifactReplay
	for _, item := range []struct{ folder, sha string }{
		{"grouped-glm", "fec488b4a06b6f1dcc4fe211db37059743d529e1cfa1fbe28b88829d4e5f7850"},
		{"grouped-kimi", "75f9fba928286650bb32123f4372c6729d484b5223f9499913514aa79be33949"},
	} {
		path := filepath.Join(root, item.folder, "summary.json")
		review := groupedArtifactReplay{Path: path}
		data, err := os.ReadFile(path)
		if err != nil {
			review.Error = "original_summary_unavailable"
			reviews = append(reviews, review)
			continue
		}
		review.SHA256 = digest(data)
		if review.SHA256 != item.sha {
			review.Error = "original_summary_digest_mismatch"
			reviews = append(reviews, review)
			continue
		}
		var summary struct {
			Status     string      `json:"status"`
			Scenario   string      `json:"scenario"`
			Passed     bool        `json:"passed"`
			Rows       int         `json:"dataset_rows"`
			RowsSHA    string      `json:"dataset_rows_sha256"`
			PayloadSHA string      `json:"dataset_payload_sha256"`
			Runs       []runReport `json:"runs"`
		}
		if !groupedDecode(data, &summary, false) || summary.Status != "finished" || summary.Scenario != "grouped-records" || len(summary.Runs) != 1 || summary.Rows != len(f.Rows) || summary.RowsSHA != digest(f.RowsJSON) || summary.PayloadSHA != digest(f.PayloadJSON) {
			review.Error = "completed_fixture_bound_summary_required"
			reviews = append(reviews, review)
			continue
		}
		run := summary.Runs[0]
		review.RawPassed, review.RawRunPassed = summary.Passed, run.Passed
		review.Model, review.OriginalRequests = run.Model, len(run.Requests)
		if run.GroupedEvidence == nil || !sourceResultIDForEval(run.GroupedEvidence.OriginalResultID) {
			review.Error = "original_result_binding_missing"
			reviews = append(reviews, review)
			continue
		}
		originalID := run.GroupedEvidence.OriginalResultID
		review.BodyHashesVerified = true
		for requestIndex := range run.Requests {
			for viewIndex := range run.Requests[requestIndex].ToolViews {
				view := &run.Requests[requestIndex].ToolViews[viewIndex]
				if len(view.GroupedBody) == 0 {
					continue
				}
				// summary.json used indentation around nested RawMessage. Restore
				// only insignificant JSON whitespace, then verify the original hash.
				var compact bytes.Buffer
				if json.Compact(&compact, view.GroupedBody) != nil || digest(compact.Bytes()) != view.BodySHA256 {
					review.BodyHashesVerified = false
					continue
				}
				view.GroupedBody = append(json.RawMessage(nil), compact.Bytes()...)
				review.CompactedBodies++
			}
		}
		if !review.BodyHashesVerified {
			review.Error = "actual_body_digest_mismatch_after_json_compact"
			reviews = append(reviews, review)
			continue
		}
		labels := map[string]bool{}
		for _, call := range run.ToolCalls {
			if call.SourceLabel != "" {
				labels[call.SourceLabel] = true
			}
		}
		scenario.finishGrouped(&run, originalID, labels)
		review.CurrentPassed, review.Evidence = run.Passed, run.GroupedEvidence
		review.Format, review.Facts, review.Scope, review.Citations = run.FinalFormatCorrect, run.NumericCorrect, run.CoverageCorrect, run.CitationsKnown
		after, readErr := os.ReadFile(path)
		if readErr != nil || digest(after) != review.SHA256 {
			review.Error = "original_summary_changed_during_replay"
			review.CurrentPassed = false
		}
		reviews = append(reviews, review)
	}
	passed := len(reviews) == 2
	for _, review := range reviews {
		passed = passed && review.CurrentPassed && review.Error == "" && review.BodyHashesVerified
	}
	rawUnchanged := len(reviews) == 2
	for _, review := range reviews {
		current, err := os.ReadFile(review.Path)
		rawUnchanged = rawUnchanged && err == nil && review.SHA256 != "" && digest(current) == review.SHA256
	}
	passed = passed && rawUnchanged
	artifact := map[string]any{"profile": "grouped-records-offline-artifact-replay-v1", "changed_rule": groupedReplayRule, "model_requests": 0, "raw_summaries_unchanged": rawUnchanged, "raw_summaries_written": false, "passed": passed, "reviews": reviews}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if !passed {
		t.Fatalf("offline replay failed; raw reports preserved; review=%s", destination)
	}
	t.Log(fmt.Sprintf("offline replay accepted 2 frozen runs; model_requests=0; raw reports unchanged; review=%s", destination))
}
