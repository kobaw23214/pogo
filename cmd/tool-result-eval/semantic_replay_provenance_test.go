package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/agent"
)

func TestCoverageReplayContrastRemainsLabelledAndBoundToWording(t *testing.T) {
	f := makeRecoveryFixture()
	answer, _ := json.Marshal(f.Logs.Expected)
	for _, mode := range []string{"valid_contrast", "missing_provenance", "changed_coverage", "unknown_kind", "recorded_answer"} {
		t.Run(mode, func(t *testing.T) {
			source := map[string]any{
				"scenario": "recovery", "status": "finished", "fixture_sha256": f.SHA256,
				"dataset_text_sha256": f.Logs.SHA256,
				"runs": []recoveryRunReport{{ID: "read-only-replay", FixtureSHA256: f.SHA256,
					Late: runReport{EndReason: string(agent.EndCompleted), FinalAnswer: string(answer)}}},
				"calibration_source_kind": "synthetic_coverage_contrast",
				"calibration_provenance": coverageReplayProvenance{
					OriginalSummarySHA256:  digest([]byte("original report")),
					OriginalFinalSHA256:    digest([]byte("original answer")),
					ContrastCoverageSHA256: digest([]byte(f.Logs.Expected.Coverage)),
				},
			}
			wantCode := 2
			switch mode {
			case "valid_contrast":
				wantCode = 0
			case "missing_provenance":
				delete(source, "calibration_provenance")
			case "changed_coverage":
				provenance := source["calibration_provenance"].(coverageReplayProvenance)
				provenance.ContrastCoverageSHA256 = digest([]byte("a different statement"))
				source["calibration_provenance"] = provenance
			case "unknown_kind":
				source["calibration_source_kind"] = "unlabelled_replacement"
			case "recorded_answer":
				wantCode = 0
				delete(source, "calibration_source_kind")
				delete(source, "calibration_provenance")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "input.json")
			raw, _ := json.Marshal(source)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "output")
			if err := os.Mkdir(output, 0700); err != nil {
				t.Fatal(err)
			}
			// No client factory is configured. This tests replay provenance and
			// fail-closed reporting without any task or external model request.
			code := runCoverageReplay(context.Background(), options{CoverageReplay: path, CoverageCheck: "semantic", Output: output},
				&safeLog{encoder: json.NewEncoder(io.Discard)}, nil)
			if code != wantCode {
				t.Fatalf("invalid provenance was relabelled as recorded evidence: got %d want %d", code, wantCode)
			}
			if code == 0 {
				raw, err := os.ReadFile(filepath.Join(output, "summary.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report struct {
					Kind string `json:"calibration_source_kind"`
				}
				if err := json.Unmarshal(raw, &report); err != nil {
					t.Fatal(err)
				}
				want := "synthetic_coverage_contrast"
				if mode == "recorded_answer" {
					want = "recorded_model_answer"
				}
				if report.Kind != want {
					t.Fatalf("replay lost source kind: %s", report.Kind)
				}
			}
		})
	}
}
