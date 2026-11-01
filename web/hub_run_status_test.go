package web

import "testing"

func TestHubSnapshotRetainsTerminalStatusUntilNextRun(t *testing.T) {
	h := NewHub()
	h.SetRunInfo("kept-model", "fixture", 128000)
	h.Publish(Event{Type: EvRunStart, RunID: "first"})
	h.Publish(Event{Type: EvRunEnd, EndReason: "transport_error", Error: "response interrupted"})
	got := h.Snapshot().Run
	if got.Active || got.RunID != "" || got.EndReason != "transport_error" || got.Error != "response interrupted" || got.Model != "kept-model" {
		t.Fatalf("reconnect snapshot lost terminal status: %+v", got)
	}
	h.Publish(Event{Type: EvRunStart, RunID: "next"})
	got = h.Snapshot().Run
	if !got.Active || got.EndReason != "" || got.Error != "" {
		t.Fatalf("new run retained an old notice: %+v", got)
	}
	h.Publish(Event{Type: EvRunEnd, EndReason: "token_budget", StopReason: "max_tokens", Error: "budget exceeded"})
	if got = h.Snapshot().Run; got.EndReason != "token_budget" {
		t.Fatalf("provider limit replaced the actual run end: %+v", got)
	}
	h.Publish(Event{Type: EvRunEnd, StopReason: "max_tokens"})
	if got = h.Snapshot().Run; got.EndReason != "max_tokens" || got.Error != "" {
		t.Fatalf("legacy provider limit missing: %+v", got)
	}
	h.Reset(nil, h.Snapshot().Usage)
	if got = h.Snapshot().Run; got.EndReason != "" || got.Error != "" {
		t.Fatalf("rewind retained a notice from the abandoned branch: %+v", got)
	}
}
