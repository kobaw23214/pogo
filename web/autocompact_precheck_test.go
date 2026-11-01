package web

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// The pre-run auto-compaction check in Session.Start: after a run whose answer
// left the prompt past the -compact threshold, the next Start must compact
// before the run begins. Reproduces the E2E shape (big first answer, armed
// threshold) inside the harness so the wiring — Config.Compact through
// newSession to the agent — is exercised, not assumed.
func TestAutoCompactPreRunCheckFiresBetweenRuns(t *testing.T) {
	big := textTurn(strings.Repeat("x", 24_000))
	big.Usage = llm.Usage{Input: 8_000, Output: 6_000}
	calls := 0
	h := newHarness(t, func(n int) (llm.Response, error) {
		calls++
		switch calls {
		case 1:
			return big, nil // run one's answer: ~6K estimated tokens
		case 2:
			return textTurn("SUMMARY: the work so far, constraint verbatim: never edit .env"), nil // the compaction call
		default:
			return textTurn("answer two"), nil // run two
		}
	}, func(c *Config) { c.Compact = "auto:2000" })

	sid := h.createSession()
	s1 := h.start(sid, "first question")
	s1.wait(t, EvRunEnd)
	s1.close()

	s2 := h.start(sid, "second question")
	s2.wait(t, EvRunEnd)
	s2.close()

	if calls < 3 {
		t.Fatalf("%d model calls; want 3 (answer, summary, answer) — the pre-run check did not fire", calls)
	}
	snap := h.session(sid).Hub().Snapshot()
	first := snap.Messages[0]
	if !strings.Contains(textOf(first), "SUMMARY") {
		t.Fatalf("message 0 after compaction:\n%.200s\nwant the compaction continuation", textOf(first))
	}
	if strings.Count(textOf(first), "first question") != 1 {
		t.Fatalf("the original request must be pinned verbatim exactly once in the continuation")
	}
}

func textOf(m Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}
