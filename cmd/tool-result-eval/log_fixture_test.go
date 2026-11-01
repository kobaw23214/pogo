package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogFixtureSizeOracleAndPreviewBlindSpot(t *testing.T) {
	f := makeLogFixture()
	if len(f.Text) < 2<<20 || len(f.Text) > 4<<20 || !utf8.ValidString(f.Text) || json.Valid([]byte(f.Text)) {
		t.Fatalf("fixture must be a 2-4 MiB pure UTF-8 log: %d bytes", len(f.Text))
	}
	if f.SHA256 != digest([]byte(f.Text)) || f.SHA256 != makeLogFixture().SHA256 || len(f.RequiredEvidence) != 4 {
		t.Fatal("log fingerprint/requirements not deterministic")
	}
	if f.Expected.RootCauseCode != "PRICE_VERSION_MISMATCH" || f.Expected.RootCauseTime != "2026-10-21T14:05:10.350+08:00" || f.Expected.RequestFailureTime != "2026-10-21T14:05:10.410+08:00" {
		t.Fatalf("independent expected incident facts changed: %+v", f.Expected)
	}
	for index, required := range f.RequiredEvidence {
		if f.Text[required.Start:required.End] != required.Line || f.Expected.Evidence[index].Quote != required.Line {
			t.Fatal("oracle line is not at its original byte position")
		}
		if index < 3 && (required.Start < len(f.Text)/3 || required.End > len(f.Text)*2/3) {
			t.Fatal("required middle evidence unexpectedly moved into preview")
		}
		if strings.Contains(f.TaskPrompt, required.Line) || strings.Contains(f.TaskPrompt, f.Expected.RootCauseCode) || strings.Contains(f.TaskPrompt, f.Expected.RootCauseSpanID) {
			t.Fatal("task leaked oracle/hidden causal span")
		}
	}
	last := f.RequiredEvidence[len(f.RequiredEvidence)-1]
	if last.End < len(f.Text)-1024 || strings.Contains(last.Line, f.Expected.RootCauseCode) || strings.Contains(last.Line, f.Expected.RootCauseSpanID) {
		t.Fatal("tail should confirm failure without revealing the middle root cause")
	}
	final, _ := json.Marshal(f.Expected)
	check := checkLogAnswer(string(final), f)
	if !check.FormatCorrect || !check.FactsCorrect || !check.CoverageCorrect {
		t.Fatalf("oracle rejected: %+v", check)
	}
	wrong := f.Expected
	wrong.RootCauseCode = "UPSTREAM_TIMEOUT"
	wrongJSON, _ := json.Marshal(wrong)
	if check := checkLogAnswer(string(wrongJSON), f); !check.FormatCorrect || check.FactsCorrect {
		t.Fatal("recovered retry confused with root cause")
	}
	if check := checkLogAnswer(string(final)+"\nextra explanation", f); check.FormatCorrect || check.FactsCorrect {
		t.Fatal("strict format was silently loosened")
	}
	wrong = f.Expected
	wrong.RequestFailureTime = f.Expected.Evidence[3].Timestamp
	wrongJSON, _ = json.Marshal(wrong)
	if checkLogAnswer(string(wrongJSON), f).FactsCorrect {
		t.Fatal("tail audit write time confused with business failure time")
	}
}
