package web

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The frame rings feed every snapshot, so a coalesced tail frame must replace
// its predecessor rather than stack: a two-minute thinking burst is one slot,
// and the tool and turn frames around it stay in the reconnect window.
func TestAppendLiveFrameCoalescesTailKinds(t *testing.T) {
	frame := func(kind, text string) json.RawMessage {
		b, _ := json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{kind, text})
		return b
	}

	var f []json.RawMessage
	f = appendLiveFrame(f, frame("session", "s"))
	f = appendLiveFrame(f, frame("turn_start", ""))
	f = appendLiveFrame(f, frame("thinking", "a"))
	f = appendLiveFrame(f, frame("thinking", "ab"))
	f = appendLiveFrame(f, frame("thinking", "abc"))
	f = appendLiveFrame(f, frame("token", "answer"))
	f = appendLiveFrame(f, frame("thinking", "abcd"))

	// Adjacent same-kind frames collapsed to one slot each; the thinking after
	// the token is not adjacent, so it takes a fresh slot — exactly the shape
	// of think / answer / think-again within one delegated run.
	if len(f) != 5 {
		t.Fatalf("tail frames did not coalesce: %d frames", len(f))
	}
	probe := struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{}
	_ = json.Unmarshal(f[2], &probe)
	if probe.Type != "thinking" || probe.Text != "abc" {
		t.Errorf("slot 3 = %s/%q, want the pre-answer thinking tail", probe.Type, probe.Text)
	}
	_ = json.Unmarshal(f[3], &probe)
	if probe.Type != "token" || probe.Text != "answer" {
		t.Errorf("slot 4 = %s/%q, want the token tail", probe.Type, probe.Text)
	}
	_ = json.Unmarshal(f[4], &probe)
	if probe.Type != "thinking" || probe.Text != "abcd" {
		t.Errorf("slot 5 = %s/%q, want the post-answer thinking tail in its own slot", probe.Type, probe.Text)
	}

	// Whole-event kinds never coalesce with anything.
	f = appendLiveFrame(f, frame("tool_start", ""))
	f = appendLiveFrame(f, frame("thinking", "z"))
	if len(f) != 7 {
		t.Fatalf("adjacent-kind replacement was too eager: %d frames", len(f))
	}
}

// The cap still applies once the stream is long enough to reach it, and a
// malformed frame degrades to an ordinary append rather than an error.
func TestAppendLiveFrameStillCapsWholeEvents(t *testing.T) {
	kind := func(i int) json.RawMessage {
		b, _ := json.Marshal(struct {
			Type string `json:"type"`
			N    int    `json:"n"`
		}{"tool_start", i})
		return b
	}
	var f []json.RawMessage
	for i := 0; i < maxPendingFrames+50; i++ {
		f = appendLiveFrame(f, kind(i))
	}
	if len(f) != maxPendingFrames {
		t.Fatalf("ring = %d, want the cap %d", len(f), maxPendingFrames)
	}
	var probe struct{ N int }
	_ = json.Unmarshal(f[0], &probe)
	if probe.N != 50 {
		t.Errorf("oldest survivor = %d, want the trim to drop from the front", probe.N)
	}

	// Malformed input (a child that died mid-write): appended untouched.
	f = appendLiveFrame(f, json.RawMessage("{not json"))
	if len(f) != maxPendingFrames {
		t.Fatalf("malformed frame changed the ring size: %d", len(f))
	}
	if !strings.Contains(string(f[len(f)-1]), "not json") {
		t.Errorf("the malformed frame is not the newest entry")
	}
}

// The live tail keeps the last n bytes, and the byte window can open inside a
// 3-byte CJK rune. A stranded leading half renders as U+FFFD in the live
// preview, so every window size must open on a whole rune.
func TestTailOfOpensOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("样本输出", 100)
	for n := 1; n <= len(s); n++ {
		got := tailOf(s, n)
		if !utf8.ValidString(got) || strings.ContainsRune(got, 0xFFFD) {
			t.Fatalf("tailOf(s, %d) = %q: a rune was split", n, got)
		}
	}
}
