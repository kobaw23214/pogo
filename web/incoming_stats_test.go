package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWritePreviewStatsUseRecoveredContentAndOneSecondSampling(t *testing.T) {
	call := &IncomingCall{Name: "write", raw: `{"path":"a.html","content":"one\ntwo\nthree`}
	added, removed := updateIncomingPreviewStats(call, 100)
	if added != 3 || removed != 0 {
		t.Fatalf("initial stats = +%d -%d", added, removed)
	}
	call.raw += "\nfour"
	added, _ = updateIncomingPreviewStats(call, 200)
	if added != 3 {
		t.Fatalf("stats changed inside the sample window: %d", added)
	}
	added, _ = updateIncomingPreviewStats(call, 1100)
	if added != 4 {
		t.Fatalf("sampled stats = %d, want 4", added)
	}
}

func TestEditPreviewStatsSumArrayAndFlatChanges(t *testing.T) {
	array := &IncomingCall{Name: "edit", raw: `{"path":"a.html","edits":[{"oldText":"a\nb\n","newText":"a\nc\n"},{"oldText":"x","newText":"x\ny"}]}`}
	added, removed := updateIncomingPreviewStats(array, 100)
	if added != 2 || removed != 1 {
		t.Fatalf("array stats = +%d -%d, want +2 -1", added, removed)
	}
	flat := &IncomingCall{Name: "edit", raw: `{"path":"a.html","oldText":"a\nb"}`}
	added, removed = updateIncomingPreviewStats(flat, 100)
	if added != 0 || removed != 2 {
		t.Fatalf("unfinished replacement stats = +%d -%d, want +0 -2", added, removed)
	}
}

func TestPartialJSONFieldHandlesIncompleteUnicode(t *testing.T) {
	value, ok := partialJSONField(`{"content":"\u4e2d\u6587\n\u4e2`, "content")
	if !ok || value != "中文\n" {
		t.Fatalf("partial unicode value = %q, ok=%v", value, ok)
	}
}

func TestPreviewStatsStayBoundedAndSerializable(t *testing.T) {
	call := &IncomingCall{Name: "write", raw: strings.Repeat("x", maxIncomingStatsBytes+10)}
	call.PreviewAdded = 7
	added, _ := updateIncomingPreviewStats(call, 100)
	if added != 7 {
		t.Fatalf("bounded input reset existing stats: %d", added)
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"raw":`) {
		t.Fatal("process-local raw arguments leaked into the snapshot")
	}
}

func TestHubPublishesPreviewStatsWithToolArgs(t *testing.T) {
	h := NewHub()
	e := Event{Type: EvToolArgs, CallID: "c1", Name: "write", Text: `{"path":"a.html","content":"one\ntwo`, TS: 100}
	h.apply(&e)
	if e.PreviewAdded == nil || *e.PreviewAdded != 2 || e.PreviewRemoved == nil || *e.PreviewRemoved != 0 {
		t.Fatalf("event stats = +%v -%v", e.PreviewAdded, e.PreviewRemoved)
	}
	encoded, err := json.Marshal(e)
	if err != nil || !strings.Contains(string(encoded), `"preview_removed":0`) {
		t.Fatalf("zero preview count was omitted: %s, err=%v", encoded, err)
	}
	if len(h.live.Incoming) != 1 || h.live.Incoming[0].PreviewAdded != 2 {
		t.Fatalf("snapshot stats = %+v", h.live.Incoming)
	}
}
