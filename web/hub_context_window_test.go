package web

import (
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func TestModelChangeClearsUnknownContextWindowAndRestoresKnownWindow(t *testing.T) {
	h := NewHub()
	h.SetRunInfo("known-first", "fixture", 128000)
	for _, next := range []struct {
		model  string
		window int
	}{
		{model: "unknown-window", window: 0},
		{model: "known-again", window: 256000},
	} {
		h.Publish(Event{Type: EvModelChanged, Model: next.model, Provider: "fixture", ContextWindow: next.window})
		run := h.Snapshot().Run
		if run.Model != next.model || run.ContextWindow != next.window {
			t.Fatalf("model change to %s: snapshot model=%q window=%d, want %d", next.model, run.Model, run.ContextWindow, next.window)
		}
	}
}

func TestContextMeasurementProvenanceSurvivesModelChangeAndSnapshot(t *testing.T) {
	h := NewHub()
	h.SetRunInfo("first", "fixture", 128000)
	h.Publish(Event{Type: EvMessage, Role: "assistant", Usage: &llm.Usage{Input: 1234}})
	if h.Snapshot().ContextEstimated {
		t.Fatal("provider input was marked estimated")
	}
	h.Publish(Event{Type: EvModelChanged, Model: "next", Provider: "fixture", ContextWindow: 256000})
	if snap := h.Snapshot(); snap.ContextTokens != 1234 || !snap.ContextEstimated {
		t.Fatalf("model change lost provenance: tokens=%d estimated=%t", snap.ContextTokens, snap.ContextEstimated)
	}
	// A same-model policy commit must not turn the retained estimate into a measurement.
	h.Publish(Event{Type: EvModelChanged, Model: "next", Provider: "fixture", ContextWindow: 256000})
	if !h.Snapshot().ContextEstimated {
		t.Fatal("same-model update relabelled an old count")
	}
	h.Publish(Event{Type: EvMessage, Role: "assistant", Usage: &llm.Usage{Input: 4321}})
	if snap := h.Snapshot(); snap.ContextTokens != 4321 || snap.ContextEstimated {
		t.Fatalf("new provider measurement not authoritative: tokens=%d estimated=%t", snap.ContextTokens, snap.ContextEstimated)
	}
	h.Publish(Event{Type: EvModelChanged, Model: "third", Provider: "fixture"})
	h.Reset(nil, llm.Usage{Input: 999999})
	if snap := h.Snapshot(); snap.ContextTokens != 0 || snap.ContextEstimated {
		t.Fatalf("replaced history retained stale count: tokens=%d estimated=%t", snap.ContextTokens, snap.ContextEstimated)
	}
}
